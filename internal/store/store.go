// Package store is the indexer's persistence layer.
//
// The shape follows one rule: the event log is the source of truth and the
// projections are disposable. Every hard problem — reorgs, double-spend,
// index numbers, cross-indexer agreement — becomes tractable once state is a
// pure function of (event log, canonical order) and can be recomputed.
//
// Practical consequences visible in the schema:
//
//   - events is append-only and keyed by canonical order, never by block hash.
//   - every projection table can be dropped and rebuilt without touching the
//     network, so a rollback is a delete plus a replay.
//   - amounts are TEXT, not INTEGER. Supply runs to 2^64-1 and SQLite's
//     INTEGER is signed 64-bit, so a large supply would silently overflow.
//   - every statement is prepared with bound parameters. Asset names arrive
//     from arbitrary attacker-controlled blocks; string-interpolated SQL here
//     would be a remote injection with a public write path.
package store

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- Chain tracking. previous_hash is what reorg detection compares against.
CREATE TABLE IF NOT EXISTS momentums (
    height        INTEGER PRIMARY KEY,
    hash          TEXT NOT NULL,
    previous_hash TEXT NOT NULL,
    timestamp     INTEGER NOT NULL,
    state_root    TEXT NOT NULL
);

-- The event log: every valid ZDU, in canonical order. Append-only.
-- (height, momentum_index) is the canonical order and therefore the key.
CREATE TABLE IF NOT EXISTS events (
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    block_hash     TEXT NOT NULL,
    opcode         INTEGER NOT NULL,
    version        INTEGER NOT NULL,
    sender         TEXT NOT NULL,
    recipient      TEXT NOT NULL,
    payload        BLOB NOT NULL,
    p_hash         TEXT NOT NULL DEFAULT '',
    t_hash         TEXT NOT NULL DEFAULT '',
    -- The carrier block's own value transfer. Irrelevant to most opcodes and
    -- explicitly ignored by transfer, but a dispenser purchase is settled in
    -- it, so the log has to carry it or a replay could not re-validate one.
    amount         TEXT NOT NULL DEFAULT '0',
    token_standard TEXT NOT NULL DEFAULT '',
    -- Whether the opcode rules accepted this event and it changed state.
    -- The state root commits to the applied set, not to everything that
    -- merely parsed: two indexers that admit the same blocks but apply
    -- different rules must produce different roots, or the commitment
    -- detects nothing.
    applied        INTEGER NOT NULL DEFAULT 0,
    -- The envelope's optional-extension map, preserved verbatim. A future
    -- version may give a key here meaning; the log has to have kept it, or
    -- replaying under the newer rules would see nothing to interpret.
    ext            BLOB,
    -- What the carrier block paid for its bandwidth under dynamic plasma.
    -- fused_plasma and difficulty are committed to by the block hash, so they
    -- are chain facts; base_plasma is recomputed here from the data length
    -- rather than taken from the node, because the wire field is not hashed.
    -- Their ratio is the congestion price the block met, which is what prices
    -- EVM gas (internal/evm/fee.go). Zero on rows written before this column
    -- existed, and read as "uncongested" there.
    base_plasma    INTEGER NOT NULL DEFAULT 0,
    fused_plasma   INTEGER NOT NULL DEFAULT 0,
    difficulty     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (height, momentum_index)
);
CREATE INDEX IF NOT EXISTS events_block_hash ON events (block_hash);
CREATE INDEX IF NOT EXISTS events_opcode     ON events (opcode);

-- ---- projections: derived, droppable, never written before the event ----

CREATE TABLE IF NOT EXISTS assets (
    id             TEXT PRIMARY KEY,
    number         INTEGER NOT NULL,
    issuer         TEXT NOT NULL,
    owner          TEXT NOT NULL,
    collection_id  TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    supply         TEXT NOT NULL,
    decimals       INTEGER NOT NULL,
    mime           TEXT NOT NULL,
    attributes     BLOB,
    ext            BLOB,
    content        BLOB NOT NULL,
    part_count     INTEGER NOT NULL DEFAULT 1,
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    timestamp      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS assets_owner      ON assets (owner);
CREATE INDEX IF NOT EXISTS assets_issuer     ON assets (issuer);
CREATE INDEX IF NOT EXISTS assets_collection ON assets (collection_id);
CREATE UNIQUE INDEX IF NOT EXISTS assets_number ON assets (number);
-- A metaprotocol replays one mime in canonical order and then follows the
-- tail (AssetsOfMimeCarriedAfter); without this, following the tail scans
-- every asset.
CREATE INDEX IF NOT EXISTS assets_mime_order ON assets (mime, height, momentum_index);

-- Persisted EVM derivation (evmstore.go): projections like the rest, keyed by
-- the canonical position of the inscription they derive from, so rewinding
-- history truncates them with everything else.
CREATE TABLE IF NOT EXISTS evm_receipts (
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    inscription    TEXT NOT NULL,
    body           BLOB NOT NULL,
    PRIMARY KEY (height, momentum_index)
);
CREATE TABLE IF NOT EXISTS evm_checkpoints (
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    version        INTEGER NOT NULL,
    chain_id       INTEGER NOT NULL,
    receipts       INTEGER NOT NULL,
    root           TEXT NOT NULL,
    state          BLOB NOT NULL,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (height, momentum_index)
);

CREATE TABLE IF NOT EXISTS balances (
    asset_id TEXT NOT NULL,
    address  TEXT NOT NULL,
    amount   TEXT NOT NULL,
    PRIMARY KEY (asset_id, address)
);
CREATE INDEX IF NOT EXISTS balances_address ON balances (address);

CREATE TABLE IF NOT EXISTS collections (
    id             TEXT PRIMARY KEY,
    issuer         TEXT NOT NULL,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    url            TEXT NOT NULL DEFAULT '',
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    timestamp      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS dispensers (
    id             TEXT PRIMARY KEY,
    issuer         TEXT NOT NULL,
    asset_id       TEXT NOT NULL,
    remaining      TEXT NOT NULL,
    price          TEXT NOT NULL,
    zts            TEXT NOT NULL,
    cancelled      INTEGER NOT NULL DEFAULT 0,
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    timestamp      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS dispensers_asset ON dispensers (asset_id);

-- Chunks of a multi-part mint that have landed but whose head has not.
-- Keyed by block hash because that is what pHash/tHash point at.
CREATE TABLE IF NOT EXISTS parts (
    block_hash     TEXT PRIMARY KEY,
    issuer         TEXT NOT NULL,
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    p_hash         TEXT NOT NULL DEFAULT '',
    chunk          BLOB NOT NULL
);

-- A tail may be consumed by at most one head; the first head in canonical
-- order wins and later ones are invalid. Without this, a chain can be
-- re-minted indefinitely.
CREATE TABLE IF NOT EXISTS consumed_tails (
    tail_hash TEXT PRIMARY KEY,
    asset_id  TEXT NOT NULL,
    -- The height of the head that claimed it. Every other projection row
    -- already carries the height it was created at, which is what lets a
    -- rollback delete rows above a checkpoint instead of replaying from
    -- genesis (A-17). This column exists so this table is not the exception.
    height    INTEGER NOT NULL DEFAULT 0
);

-- Composite import edges (spec R-4.7): asset_id declares ref_id in slot idx.
-- Recorded, never resolved: the indexer does not fetch the target, does not
-- require it to exist, and no state depends on it.
CREATE TABLE IF NOT EXISTS asset_refs (
    asset_id TEXT NOT NULL,
    idx      INTEGER NOT NULL,
    ref_id   TEXT NOT NULL,
    height   INTEGER NOT NULL,
    PRIMARY KEY (asset_id, idx)
);
CREATE INDEX IF NOT EXISTS asset_refs_ref ON asset_refs (ref_id);

-- Human-facing activity feed; carries no consensus weight.
CREATE TABLE IF NOT EXISTS transfers (
    height         INTEGER NOT NULL,
    momentum_index INTEGER NOT NULL,
    block_hash     TEXT NOT NULL,
    asset_id       TEXT NOT NULL,
    sender         TEXT NOT NULL,
    recipient      TEXT NOT NULL,
    amount         TEXT NOT NULL,
    burned         INTEGER NOT NULL DEFAULT 0,
    timestamp      INTEGER NOT NULL,
    PRIMARY KEY (height, momentum_index)
);
CREATE INDEX IF NOT EXISTS transfers_asset ON transfers (asset_id);

-- Per-indexer serving policy. Deliberately NOT a projection: moderation is
-- applied at the serving layer and MUST NOT affect state_root, or two honest
-- indexers with different policies would appear to disagree on history.
CREATE TABLE IF NOT EXISTS moderation (
    asset_id           TEXT PRIMARY KEY,
    metadata_permitted INTEGER NOT NULL DEFAULT 1,
    data_permitted     INTEGER NOT NULL DEFAULT 1,
    reason             TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- ---- checkpoints (A-17) ----
--
-- A reorg used to mean replaying the entire event log. Correct, and O(events)
-- forever. A checkpoint moves the base case of that replay forward: restore
-- the state at height S, replay only what came after.
--
-- Only two projections need copying. Every other one is insert-only and its
-- rows carry the height they were written at, so rolling them back to S is
-- DELETE ... WHERE height > S — a mechanical truncation that knows nothing
-- about opcodes. balances and dispensers are the ones that mutate in place,
-- and they are small: one row per (asset, holder) and per dispenser, with no
-- content blobs. Checkpointing those two is what keeps a checkpoint from
-- being a copy of the whole database.
CREATE TABLE IF NOT EXISTS snapshots (
    height     INTEGER PRIMARY KEY,
    created_at INTEGER NOT NULL,
    events     INTEGER NOT NULL,
    state_root TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS snapshot_balances (
    snapshot_height INTEGER NOT NULL,
    asset_id        TEXT NOT NULL,
    address         TEXT NOT NULL,
    amount          TEXT NOT NULL,
    PRIMARY KEY (snapshot_height, asset_id, address)
);

CREATE TABLE IF NOT EXISTS snapshot_dispensers (
    snapshot_height INTEGER NOT NULL,
    id              TEXT NOT NULL,
    remaining       TEXT NOT NULL,
    cancelled       INTEGER NOT NULL,
    PRIMARY KEY (snapshot_height, id)
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	// modernc's driver serialises writes internally, but a single connection
	// removes any chance of two goroutines interleaving a transaction.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// Size is the on-disk footprint of the database: the main file plus its WAL
// and shared-memory index, since WAL mode (see the schema pragma) can leave a
// meaningful fraction of committed data sitting in -wal until the next
// checkpoint. A file that does not exist yet (fresh -shm before any write)
// contributes zero rather than an error.
func (s *Store) Size() (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// ContentBytes is the sum of every asset's inscribed content — the raw
// inscribed data volume, as distinct from Size, which also counts the event
// log, indexes and other projections the database keeps alongside it.
func (s *Store) ContentBytes() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(length(content)) FROM assets`).Scan(&total)
	return total.Int64, err
}

// migrate adds columns that CREATE TABLE IF NOT EXISTS cannot add to an
// existing table. Each step is idempotent and checked against the live schema
// rather than a stored version number, so a database at any prior shape
// converges to the current one.
func migrate(db *sql.DB) error {
	for _, m := range []struct{ table, column, ddl string }{
		{"events", "applied", "ALTER TABLE events ADD COLUMN applied INTEGER NOT NULL DEFAULT 0"},
		{"events", "ext", "ALTER TABLE events ADD COLUMN ext BLOB"},
		{"assets", "ext", "ALTER TABLE assets ADD COLUMN ext BLOB"},
		{"consumed_tails", "height", "ALTER TABLE consumed_tails ADD COLUMN height INTEGER NOT NULL DEFAULT 0"},
		{"events", "base_plasma", "ALTER TABLE events ADD COLUMN base_plasma INTEGER NOT NULL DEFAULT 0"},
		{"events", "fused_plasma", "ALTER TABLE events ADD COLUMN fused_plasma INTEGER NOT NULL DEFAULT 0"},
		{"events", "difficulty", "ALTER TABLE events ADD COLUMN difficulty INTEGER NOT NULL DEFAULT 0"},
	} {
		has, err := hasColumn(db, m.table, m.column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

// projectionTables are everything derived from the event log. Dropping them
// must always be safe: if it is not, something wrote state that the log does
// not describe, and that is the bug.
var projectionTables = []string{
	"assets", "balances", "collections", "dispensers",
	"parts", "consumed_tails", "transfers", "asset_refs",
	"evm_receipts", "evm_checkpoints",
}

// truncatableTables are the projections that are insert-only and carry the
// event height they were written at. Rolling one back to height S is a
// DELETE of everything above S — no inverse of any protocol rule, no
// knowledge of opcodes, nothing that can drift from the forward path.
//
// balances and dispensers are deliberately absent: both mutate in place, so
// they are restored from a checkpoint copy instead (see snapshot.go).
var truncatableTables = []string{
	"assets", "collections", "parts", "consumed_tails", "transfers", "asset_refs",
	"evm_receipts", "evm_checkpoints",
}

// ResetProjections clears derived state, leaving the event log and momentum
// chain intact.
func (s *Store) ResetProjections() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range projectionTables {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil { //nolint:gosec // fixed table list
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}
	if err := bumpGeneration(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// generationKey counts rewrites of projection history.
//
// Projections normally only grow at the tail, so a reader that caches
// something derived from them (the API's EVM state) can follow the tail
// instead of re-deriving. Two operations break that: a full reset and a
// checkpoint restore, which between them are every reorg and every reindex.
// Each bumps this counter in the same transaction that rewrites the rows, so
// a reader that sees the new rows also sees the new generation — including a
// reader in another process, which is where `zcribe reindex` runs.
const generationKey = "projection_generation"

func bumpGeneration(tx *sql.Tx) error {
	_, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, '1')
		ON CONFLICT (key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)`, generationKey)
	if err != nil {
		return fmt.Errorf("bump projection generation: %w", err)
	}
	return nil
}

// ProjectionGeneration is the number of times projection history has been
// rewritten. Anything cached from a lower generation describes rows that may
// no longer exist.
func (s *Store) ProjectionGeneration() (int64, error) {
	v, err := s.Meta(generationKey)
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// TruncateAbove removes every event and momentum above height, which is the
// first half of a reorg: the second half is replaying what remains.
func (s *Store) TruncateAbove(height uint64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM events WHERE height > ?`, height); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM momentums WHERE height > ?`, height); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// --- momentum chain ---

type MomentumRow struct {
	Height       uint64
	Hash         string
	PreviousHash string
	Timestamp    int64
	StateRoot    string
}

func (s *Store) PutMomentum(m MomentumRow) error {
	_, err := s.db.Exec(
		`INSERT INTO momentums (height, hash, previous_hash, timestamp, state_root)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (height) DO UPDATE SET
		   hash = excluded.hash, previous_hash = excluded.previous_hash,
		   timestamp = excluded.timestamp, state_root = excluded.state_root`,
		m.Height, m.Hash, m.PreviousHash, m.Timestamp, m.StateRoot)
	return err
}

func (s *Store) Momentum(height uint64) (*MomentumRow, error) {
	var m MomentumRow
	err := s.db.QueryRow(
		`SELECT height, hash, previous_hash, timestamp, state_root
		   FROM momentums WHERE height = ?`, height).
		Scan(&m.Height, &m.Hash, &m.PreviousHash, &m.Timestamp, &m.StateRoot)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &m, err
}

// TipHeight is the highest momentum this indexer has processed. Zero means
// nothing has been indexed yet, which the syncer must treat as "start at the
// configured launch height" rather than "wait for height to grow" — the
// latter is a cold-start deadlock.
func (s *Store) TipHeight() (uint64, error) {
	var h sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(height) FROM momentums`).Scan(&h); err != nil {
		return 0, err
	}
	if !h.Valid {
		return 0, nil
	}
	return uint64(h.Int64), nil
}

// MomentumHeights lists every indexed height in ascending order, so the
// commitment chain can be recomputed in the order it was built.
func (s *Store) MomentumHeights() ([]uint64, error) {
	rows, err := s.db.Query(`SELECT height FROM momentums ORDER BY height ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint64
	for rows.Next() {
		var h uint64
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) SetStateRoot(height uint64, root string) error {
	_, err := s.db.Exec(`UPDATE momentums SET state_root = ? WHERE height = ?`, root, height)
	return err
}

func (s *Store) StateRoot(height uint64) (string, error) {
	var r string
	err := s.db.QueryRow(`SELECT state_root FROM momentums WHERE height = ?`, height).Scan(&r)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return r, err
}

// --- event log ---

type Event struct {
	Height        uint64
	MomentumIndex int
	BlockHash     string
	Opcode        uint64
	Version       uint64
	Sender        string
	Recipient     string
	Payload       []byte
	PHash         string
	THash         string
	Amount        string // decimal; the carrier block's value, not the protocol amount
	TokenStandard string
	Applied       bool   // did the opcode rules accept it and change state
	Ext           []byte // envelope extension map, verbatim

	// The carrier block's plasma, under dynamic plasma. BasePlasma is the
	// nominal cost of its bytes, recomputed locally; FusedPlasma and
	// Difficulty are what it actually paid, and both are hash-committed.
	// Their ratio is the congestion price it met. All zero for a block
	// indexed before these columns existed.
	BasePlasma  uint64
	FusedPlasma uint64
	Difficulty  uint64

	Timestamp int64 // joined from momentums, not stored on the event
}

func (s *Store) AppendEvent(e Event) error {
	_, err := s.db.Exec(
		`INSERT INTO events
		   (height, momentum_index, block_hash, opcode, version, sender, recipient,
		    payload, p_hash, t_hash, amount, token_standard, ext,
		    base_plasma, fused_plasma, difficulty)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (height, momentum_index) DO NOTHING`,
		e.Height, e.MomentumIndex, e.BlockHash, e.Opcode, e.Version,
		e.Sender, e.Recipient, e.Payload, e.PHash, e.THash, e.Amount, e.TokenStandard, e.Ext,
		e.BasePlasma, e.FusedPlasma, e.Difficulty)
	return err
}

// Events replays the log in canonical order, in pages.
//
// Paging is not an optimisation. The callback writes projections, and this
// store holds a single connection so that no two writers can interleave — so
// a cursor left open across the callback would hold that connection while the
// callback waits for it. Reading a page, closing the cursor, then applying it
// is what keeps replay from deadlocking against itself.
//
// The cursor is (height, momentum_index) — the canonical order itself — so
// paging cannot reorder or skip an event the way an OFFSET can.
func (s *Store) Events(fn func(Event) error) error { return s.EventsAfter(0, -1, fn) }

// EventsAbove replays every event strictly above a momentum height. maxIndex
// stands in for "past the last block of that momentum": momentum_index counts
// blocks within one momentum, so nothing can reach it.
func (s *Store) EventsAbove(height uint64, fn func(Event) error) error {
	const maxIndex = 1 << 30
	return s.EventsAfter(height, maxIndex, fn)
}

// EventsAfter replays the log from just past (height, index), which is what a
// checkpointed rebuild needs: the checkpoint already accounts for everything
// at or below its height, so replaying those again would double-apply them.
func (s *Store) EventsAfter(afterHeight uint64, afterIndex int, fn func(Event) error) error {
	const pageSize = 512
	lastHeight := afterHeight
	lastIndex := afterIndex

	for {
		page, err := s.eventPage(lastHeight, lastIndex, pageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, e := range page {
			if err := fn(e); err != nil {
				return err
			}
		}
		last := page[len(page)-1]
		lastHeight, lastIndex = last.Height, last.MomentumIndex
	}
}

func (s *Store) eventPage(afterHeight uint64, afterIndex, limit int) ([]Event, error) {
	rows, err := s.db.Query(
		eventCols+` WHERE (e.height, e.momentum_index) > (?, ?)
		            ORDER BY e.height ASC, e.momentum_index ASC
		            LIMIT ?`, afterHeight, afterIndex, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// TxHashesAt returns the block hashes that actually changed state in one
// momentum, in canonical order. This is the input to the state-root
// commitment.
//
// The `applied` filter is the whole point. An earlier version committed to
// every block that merely parsed as a ZDU, which meant two indexers enforcing
// different opcode rules — different part limits, different bounds, a missed
// issuer check — produced identical roots while holding different balances.
// A commitment that cannot see the disagreement it exists to detect is worse
// than none, because it is trusted.
func (s *Store) TxHashesAt(height uint64) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT block_hash FROM events
		  WHERE height = ? AND applied = 1
		  ORDER BY momentum_index ASC`, height)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetEventApplied records the outcome of the opcode rules for one event.
// Called after Apply, and again on every replay, so the flag is derived state
// like any projection rather than a fact the log asserts on its own.
func (s *Store) SetEventApplied(height uint64, index int, applied bool) error {
	_, err := s.db.Exec(
		`UPDATE events SET applied = ? WHERE height = ? AND momentum_index = ?`,
		boolInt(applied), height, index)
	return err
}

func (s *Store) EventCount() (int64, error) {
	var n int64
	return n, s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
}

// HexBytes renders a hash for storage. Hashes are stored as lowercase hex
// text rather than BLOBs so that a human debugging the database can read
// them, and so JSON responses need no conversion step.
func HexBytes(b []byte) string { return hex.EncodeToString(b) }
