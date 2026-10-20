package store

import (
	"database/sql"
)

// Reads that exist for the serving edge, not for the state machine.
//
// An asset's activity feed is missing three kinds of row that the transfers
// table will never contain, because the events behind them move balances
// without writing one: the mint itself, stock going into a dispenser's
// escrow, and stock coming back out when the dispenser is cancelled
// (internal/indexer/apply.go). Writing those rows in the indexer would change
// Store.ProjectionDigest, which `zcribe compare` and /state both hash over,
// so every deployment would have to upgrade in the same momentum to avoid
// reporting a divergence that is not one.
//
// The alternative is to reconstruct them when the feed is served, out of the
// event log — which already holds every fact needed and is the source of
// truth for all of it. That is what these queries are for. They are all
// read-only, none of them is reachable from Apply, and nothing here
// participates in a digest.

// DispensersOfAsset lists every dispenser ever opened for one asset, oldest
// first, cancelled ones included: a closed dispenser still explains where an
// asset's units were between the day it opened and the day it closed.
func (s *Store) DispensersOfAsset(assetID string) ([]Dispenser, error) {
	rows, err := s.db.Query(
		`SELECT id, issuer, asset_id, remaining, price, zts, cancelled, height, momentum_index, timestamp
		   FROM dispensers WHERE asset_id = ?
		  ORDER BY height ASC, momentum_index ASC`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDispensers(rows)
}

// EventByBlockHash returns one logged event by the block that carried it, or
// nil when no such block was ever indexed.
//
// This is how a dispenser's original escrow amount is recovered. The
// dispensers projection tracks what is left, not what was put in, and after a
// cancel that number is zero — so the only surviving record of the amount is
// the createDispenser payload in the log, under the dispenser's own id.
func (s *Store) EventByBlockHash(blockHash string) (*Event, error) {
	rows, err := s.db.Query(eventCols+` WHERE e.block_hash = ? LIMIT 1`, blockHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanEvents(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

// AppliedEventsWithOpcode returns every event of one opcode that changed
// state, oldest first, above a height.
//
// Only ever called with an opcode whose events are rare — cancelDispenser —
// and always with the dispenser's own height as the floor, because a
// dispenser cannot be cancelled before it exists. The opcode index makes it a
// scan of that one opcode rather than of the log.
func (s *Store) AppliedEventsWithOpcode(opcode, aboveHeight uint64) ([]Event, error) {
	rows, err := s.db.Query(
		eventCols+` WHERE e.opcode = ? AND e.applied = 1 AND e.height > ?
		            ORDER BY e.height ASC, e.momentum_index ASC`, opcode, aboveHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// TransferredFrom sums every recorded transfer of one asset out of one
// address. Used against an escrow key, where it answers "how much of what was
// escrowed has already been sold" — which is what turns the create payload's
// amount into the amount a cancel released.
func (s *Store) TransferredFrom(assetID, sender string) (uint64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(
		`SELECT SUM(CAST(amount AS INTEGER)) FROM transfers
		  WHERE asset_id = ? AND sender = ?`, assetID, sender).Scan(&total)
	if err != nil || !total.Valid {
		return 0, err
	}
	return uint64(total.Int64), nil
}

// eventCols is the select list every event read shares, so a column added to
// the log lands in one place rather than in each query that forgot it.
const eventCols = `SELECT e.height, e.momentum_index, e.block_hash, e.opcode, e.version,
                          e.sender, e.recipient, e.payload, e.p_hash, e.t_hash,
                          e.amount, e.token_standard, e.applied, e.ext,
                          e.base_plasma, e.fused_plasma, e.difficulty,
                          COALESCE(m.timestamp, 0)
                     FROM events e LEFT JOIN momentums m ON m.height = e.height`

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var out []Event
	for rows.Next() {
		var e Event
		var applied int
		if err := rows.Scan(&e.Height, &e.MomentumIndex, &e.BlockHash, &e.Opcode,
			&e.Version, &e.Sender, &e.Recipient, &e.Payload, &e.PHash, &e.THash,
			&e.Amount, &e.TokenStandard, &applied, &e.Ext,
			&e.BasePlasma, &e.FusedPlasma, &e.Difficulty, &e.Timestamp); err != nil {
			return nil, err
		}
		e.Applied = applied != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanDispensers(rows *sql.Rows) ([]Dispenser, error) {
	var out []Dispenser
	for rows.Next() {
		var d Dispenser
		var remaining, price string
		var cancelled int
		if err := rows.Scan(&d.ID, &d.Issuer, &d.AssetID, &remaining, &price, &d.ZTS,
			&cancelled, &d.Height, &d.MomentumIndex, &d.Timestamp); err != nil {
			return nil, err
		}
		d.Remaining, d.Price, d.Cancelled = u64(remaining), u64(price), cancelled != 0
		out = append(out, d)
	}
	return out, rows.Err()
}
