package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Persisted EVM derivation: receipts, one row per EVM transaction, and
// periodic checkpoints of the derived state.
//
// Both are projections in the strict sense of this package — derived from
// the event log, droppable, and rewritten with it. That is not an analogy:
// the two tables are in projectionTables and truncatableTables, so a reset
// clears them and a checkpoint restore truncates them above its height in
// the same transaction that rewrites the assets they were derived from. A
// checkpoint that survives is therefore always one of the current history,
// and resuming from it is sound (see internal/api/evm.go).
//
// The one thing a projection write here has that the indexer's do not is a
// different writer: the API process derives the EVM, not the indexer. So
// every write is fenced on ProjectionGeneration — it commits only if the
// history it was derived from has not been rewritten since. Without the
// fence, a checkpoint computed just before a reorg and written just after it
// would outlive the rewrite and resurrect the abandoned branch on the next
// restart.

// ErrStaleGeneration is a fenced write refused because projection history
// was rewritten after the caller read it.
var ErrStaleGeneration = errors.New("projection history was rewritten; derived rows not written")

// EVMReceiptRow is one persisted receipt. Body is the deriver's own encoding
// (evm.MarshalReceipt); this package does not interpret it.
type EVMReceiptRow struct {
	Height        uint64
	MomentumIndex int
	Inscription   string
	Body          []byte
}

// EVMCheckpoint is persisted derived state at a canonical position: the
// last inscription row the deriver had consumed when it was taken.
type EVMCheckpoint struct {
	Height        uint64
	MomentumIndex int
	Version       int
	ChainID       uint64
	Receipts      int
	Root          string
	State         []byte
	CreatedAt     int64
}

func (s *Store) fenced(gen int64, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v string
	err = tx.QueryRow(`SELECT value FROM meta WHERE key = ?`, generationKey).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		v = "0"
	case err != nil:
		return err
	}
	if v != fmt.Sprint(gen) {
		return ErrStaleGeneration
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendEVMReceipts persists receipts derived under generation gen. A row at
// an existing position is replaced: a deriver resuming from a checkpoint
// re-derives the rows after it, and they are the same rows.
func (s *Store) AppendEVMReceipts(gen int64, rows []EVMReceiptRow) error {
	if len(rows) == 0 {
		return nil
	}
	return s.fenced(gen, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT OR REPLACE INTO evm_receipts
			(height, momentum_index, inscription, body) VALUES (?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range rows {
			if _, err := stmt.Exec(r.Height, r.MomentumIndex, r.Inscription, r.Body); err != nil {
				return err
			}
		}
		return nil
	})
}

// EVMReceiptsUpTo streams the persisted receipts at or before a canonical
// position, in canonical order.
func (s *Store) EVMReceiptsUpTo(height uint64, index int, fn func(EVMReceiptRow) error) error {
	rows, err := s.db.Query(`SELECT height, momentum_index, inscription, body FROM evm_receipts
		WHERE height < ? OR (height = ? AND momentum_index <= ?)
		ORDER BY height, momentum_index`, height, height, index)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r EVMReceiptRow
		if err := rows.Scan(&r.Height, &r.MomentumIndex, &r.Inscription, &r.Body); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// PutEVMCheckpoint persists a checkpoint derived under generation gen and
// prunes the ones no rollback could need.
//
// The policy is PruneSnapshots': keep every checkpoint within `retain`
// momentums of the new one, plus the newest one below that window. A reorg
// the indexer survives from its own checkpoints — at most `retain` deep —
// then always finds an EVM checkpoint at or below the fork, so the EVM
// resumes instead of replaying from genesis. With retain 0 (the indexer keeps
// no checkpoints, so any rollback is a full reset) only the newest two are
// kept, for restarts.
func (s *Store) PutEVMCheckpoint(gen int64, cp EVMCheckpoint, retain uint64) error {
	if cp.CreatedAt == 0 {
		cp.CreatedAt = time.Now().Unix()
	}
	return s.fenced(gen, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO evm_checkpoints
			(height, momentum_index, version, chain_id, receipts, root, state, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			cp.Height, cp.MomentumIndex, cp.Version, cp.ChainID, cp.Receipts, cp.Root, cp.State, cp.CreatedAt); err != nil {
			return err
		}
		if retain == 0 {
			_, err := tx.Exec(`DELETE FROM evm_checkpoints WHERE rowid NOT IN
				(SELECT rowid FROM evm_checkpoints ORDER BY height DESC, momentum_index DESC LIMIT 2)`)
			return err
		}
		if cp.Height <= retain {
			return nil
		}
		var keepFrom sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(height) FROM evm_checkpoints WHERE height <= ?`,
			cp.Height-retain).Scan(&keepFrom); err != nil {
			return err
		}
		if keepFrom.Valid {
			_, err := tx.Exec(`DELETE FROM evm_checkpoints WHERE height < ?`, keepFrom.Int64)
			return err
		}
		return nil
	})
}

// LatestEVMCheckpoint is the newest checkpoint derived under this version of
// the rules for this chain, or nil.
func (s *Store) LatestEVMCheckpoint(version int, chainID uint64) (*EVMCheckpoint, error) {
	var cp EVMCheckpoint
	err := s.db.QueryRow(`SELECT height, momentum_index, version, chain_id, receipts, root, state, created_at
		FROM evm_checkpoints WHERE version = ? AND chain_id = ?
		ORDER BY height DESC, momentum_index DESC LIMIT 1`, version, chainID).
		Scan(&cp.Height, &cp.MomentumIndex, &cp.Version, &cp.ChainID, &cp.Receipts, &cp.Root, &cp.State, &cp.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cp, nil
}

// MimeContentBytes is the total inscribed size of one mime's assets. length()
// of a blob is read from the record header, so this does not load content.
func (s *Store) MimeContentBytes(mime string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(length(content)), 0) FROM assets WHERE mime = ?`, mime).Scan(&n)
	return n, err
}

// ClearEVMDerived drops every persisted receipt and checkpoint, for a deriver
// about to replay from genesis — typically because what is stored was derived
// under other rules.
func (s *Store) ClearEVMDerived(gen int64) error {
	return s.fenced(gen, func(tx *sql.Tx) error {
		for _, t := range []string{"evm_receipts", "evm_checkpoints"} {
			if _, err := tx.Exec("DELETE FROM " + t); err != nil { //nolint:gosec // fixed table list
				return err
			}
		}
		return nil
	})
}
