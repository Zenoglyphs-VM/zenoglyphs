package store

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"time"

	"golang.org/x/crypto/sha3"
)

// Checkpoints.
//
// A-17 chose to handle a reorg by replaying the whole event log, on the
// argument that an inverse for every projection write is a second
// implementation of the state machine and the two drift. That argument is
// still right, and nothing here weakens it: a checkpoint does not invert
// anything. It moves the *base case* of the same replay forward.
//
//	Rebuild()        base case = empty state at startHeight-1, replay everything
//	RebuildFrom(S)   base case = the checkpoint at S,          replay events > S
//
// One code path, two starting points. The forward direction — Apply — is
// untouched, so there is no second implementation to disagree with the first.
//
// The rollback half is mechanical rather than semantic. Every insert-only
// projection carries the height of the event that wrote it, so undoing them is
// "delete rows above S"; that statement knows nothing about opcodes and cannot
// drift from a rule it never encoded. Only balances and dispensers mutate in
// place, and those two are copied verbatim into the checkpoint.
//
// TestSnapshotRestoreMatchesFullRebuild is the guard: it runs both paths over
// the same log and compares ProjectionDigest. If truncation ever stops being
// equivalent to replay, that test says so.

// Snapshot is one checkpoint's header.
type Snapshot struct {
	Height    uint64
	CreatedAt int64
	Events    int64
	StateRoot string
}

// PutSnapshot checkpoints the mutable projections at height h.
//
// It is idempotent: taking a checkpoint at a height that already has one
// replaces it, because the state at a height is a function of the log, so
// re-taking it can only produce the same answer or reveal a bug.
func (s *Store) PutSnapshot(h uint64, stateRoot string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM snapshot_balances   WHERE snapshot_height = ?`,
		`DELETE FROM snapshot_dispensers WHERE snapshot_height = ?`,
	} {
		if _, err := tx.Exec(q, h); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO snapshot_balances (snapshot_height, asset_id, address, amount)
		 SELECT ?, asset_id, address, amount FROM balances`, h); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO snapshot_dispensers (snapshot_height, id, remaining, cancelled)
		 SELECT ?, id, remaining, cancelled FROM dispensers`, h); err != nil {
		return err
	}

	var events int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE height <= ?`, h).Scan(&events); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO snapshots (height, created_at, events, state_root) VALUES (?,?,?,?)
		 ON CONFLICT (height) DO UPDATE SET
		   created_at = excluded.created_at, events = excluded.events,
		   state_root = excluded.state_root`,
		h, time.Now().Unix(), events, stateRoot); err != nil {
		return err
	}
	return tx.Commit()
}

// SnapshotAtOrBelow returns the newest checkpoint no higher than h, which is
// the one a rollback to h restores from. Nil means there is none, and the
// caller must replay from the beginning.
func (s *Store) SnapshotAtOrBelow(h uint64) (*Snapshot, error) {
	var snap Snapshot
	err := s.db.QueryRow(
		`SELECT height, created_at, events, state_root FROM snapshots
		  WHERE height <= ? ORDER BY height DESC LIMIT 1`, h).
		Scan(&snap.Height, &snap.CreatedAt, &snap.Events, &snap.StateRoot)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &snap, err
}

func (s *Store) ListSnapshots() ([]Snapshot, error) {
	rows, err := s.db.Query(
		`SELECT height, created_at, events, state_root FROM snapshots ORDER BY height ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		if err := rows.Scan(&snap.Height, &snap.CreatedAt, &snap.Events, &snap.StateRoot); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// DropSnapshotsAbove removes checkpoints of a branch that no longer exists.
// A checkpoint above the fork point describes state that was never real, and
// restoring from one would silently reintroduce the abandoned branch.
func (s *Store) DropSnapshotsAbove(h uint64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM snapshot_balances   WHERE snapshot_height > ?`,
		`DELETE FROM snapshot_dispensers WHERE snapshot_height > ?`,
		`DELETE FROM snapshots           WHERE height          > ?`,
	} {
		if _, err := tx.Exec(q, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneSnapshots keeps every checkpoint within retain momentums of the tip,
// plus the newest one below that window, and deletes the rest.
//
// The window is the reorg depth the operator is willing to survive without a
// full replay. The default is 360 momentums — one hour at NoM's ~10 s interval
// — and keeping one checkpoint below the window is what makes that a floor
// rather than an average: right after a prune there is still a checkpoint at
// least `retain` deep, not one sitting at the window's edge.
func (s *Store) PruneSnapshots(tip, retain uint64) (int, error) {
	var floor uint64
	if tip > retain {
		floor = tip - retain
	}
	keep, err := s.SnapshotAtOrBelow(floor)
	if err != nil || keep == nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM snapshot_balances   WHERE snapshot_height < ?`,
		`DELETE FROM snapshot_dispensers WHERE snapshot_height < ?`,
	} {
		if _, err := tx.Exec(q, keep.Height); err != nil {
			return 0, err
		}
	}
	res, err := tx.Exec(`DELETE FROM snapshots WHERE height < ?`, keep.Height)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// RestoreSnapshot rewinds the projections to the state they held at the
// checkpoint's height.
//
// Two halves, and the split is the whole design:
//
//   - Insert-only tables are truncated by height. No inverse is computed; rows
//     written above the checkpoint are simply gone.
//   - balances and dispensers are replaced from the copy. dispensers keep the
//     rows that existed at the checkpoint and take back their remaining and
//     cancelled values, because those two fields are the only projection state
//     a later event can change in place.
func (s *Store) RestoreSnapshot(h uint64) error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE height = ?`, h).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("no checkpoint at height %d", h)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range truncatableTables {
		if _, err := tx.Exec("DELETE FROM "+t+" WHERE height > ?", h); err != nil { //nolint:gosec // fixed table list
			return fmt.Errorf("truncate %s: %w", t, err)
		}
	}
	// A dispenser created above the checkpoint never existed at it.
	if _, err := tx.Exec(`DELETE FROM dispensers WHERE height > ?`, h); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM balances`); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO balances (asset_id, address, amount)
		 SELECT asset_id, address, amount FROM snapshot_balances WHERE snapshot_height = ?`, h); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE dispensers SET
		   remaining = (SELECT remaining FROM snapshot_dispensers
		                 WHERE snapshot_height = ? AND id = dispensers.id),
		   cancelled = (SELECT cancelled FROM snapshot_dispensers
		                 WHERE snapshot_height = ? AND id = dispensers.id)
		 WHERE EXISTS (SELECT 1 FROM snapshot_dispensers
		                WHERE snapshot_height = ? AND id = dispensers.id)`, h, h, h); err != nil {
		return err
	}
	if err := bumpGeneration(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ProjectionDigest hashes the entire projected state.
//
// The state root (R-6.4) commits to *which events applied*, which catches two
// indexers that disagree about the rules. It cannot catch two indexers that
// agree on the rules and still compute different balances — a projection bug
// on one side leaves the applied set identical. This digest closes that gap,
// and it is also what the checkpoint test compares.
//
// It is a diagnostic, not consensus: moderation is excluded and no protocol
// rule refers to it.
func (s *Store) ProjectionDigest() (string, error) {
	h := sha3.New256()
	for _, q := range []struct{ label, query string }{
		{"assets", `SELECT id, number, issuer, owner, collection_id, name, description,
		              supply, decimals, mime, part_count, height, momentum_index,
		              length(content) FROM assets ORDER BY id`},
		{"balances", `SELECT asset_id, address, amount FROM balances ORDER BY asset_id, address`},
		{"collections", `SELECT id, issuer, name, description, url, height FROM collections ORDER BY id`},
		{"dispensers", `SELECT id, issuer, asset_id, remaining, price, zts, cancelled, height
		                  FROM dispensers ORDER BY id`},
		{"refs", `SELECT asset_id, idx, ref_id FROM asset_refs ORDER BY asset_id, idx`},
		{"tails", `SELECT tail_hash, asset_id FROM consumed_tails ORDER BY tail_hash`},
		{"parts", `SELECT block_hash, issuer, p_hash, length(chunk) FROM parts ORDER BY block_hash`},
		{"transfers", `SELECT height, momentum_index, asset_id, sender, recipient, amount, burned
		                 FROM transfers ORDER BY height, momentum_index`},
	} {
		_, _ = h.Write([]byte("\x00" + q.label + "\x00"))
		if err := digestQuery(s.db, h, q.query); err != nil {
			return "", fmt.Errorf("%s: %w", q.label, err)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// digestQuery folds every row of a query into the hash, column by column, each
// value length-prefixed so that ("ab","c") and ("a","bc") cannot collide.
func digestQuery(db *sql.DB, h interface{ Write([]byte) (int, error) }, query string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var n uint64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		n++
		for _, v := range vals {
			var b []byte
			switch t := v.(type) {
			case nil:
				b = []byte("\xffnil")
			case []byte:
				b = t
			case string:
				b = []byte(t)
			case int64:
				b = binary.BigEndian.AppendUint64(nil, uint64(t))
			default:
				b = []byte(fmt.Sprint(t))
			}
			_, _ = h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(b))))
			_, _ = h.Write(b)
		}
	}
	_, _ = h.Write(binary.BigEndian.AppendUint64(nil, n))
	return rows.Err()
}
