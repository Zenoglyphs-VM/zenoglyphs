// Package botstore is the Telegram front end's own database.
//
// Deliberately separate from the indexer's store, and not a table inside it.
// Everything the indexer holds is derived from the chain and can be thrown
// away and rebuilt; nothing here can. A signed statement linking a Telegram
// account to an address, and a credit balance that says who has paid for how
// many mints, are facts about this operator's users — they are not in any
// block, no replay reproduces them, and a `zcribe reindex` must never touch
// them. Two files, two lifetimes.
//
// Three things live here:
//
//	identities   which Zenon address a Telegram user has proved they control
//	challenges   the nonce they are currently being asked to sign
//	credits      a balance, and the ledger that explains it
//
// Amounts are TEXT holding a decimal integer, for the same reason the
// indexer's are: a token amount is in the smallest unit, PP has decimals, and
// SQLite's INTEGER is signed 64-bit. Every amount crossing this API is a
// *big.Int and never a uint64.
package botstore

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
PRAGMA journal_mode = WAL;

-- A Telegram account that has proved control of a Zenon address by signing a
-- challenge. The public key is kept because an address is sha3(pubkey)[:19]
-- and a signature cannot be checked without the key it was made with.
CREATE TABLE IF NOT EXISTS identities (
    telegram_id INTEGER PRIMARY KEY,
    username    TEXT NOT NULL DEFAULT '',
    address     TEXT NOT NULL,
    public_key  TEXT NOT NULL,
    verified_at INTEGER NOT NULL
);
-- One address, one Telegram account. Without this a deposit from an address
-- two accounts had both "proved" would have no single owner to credit.
CREATE UNIQUE INDEX IF NOT EXISTS identities_address ON identities (address);

-- The outstanding challenge, one per user: asking for a second one replaces
-- the first, so an old nonce can never be replayed after a new one is issued.
CREATE TABLE IF NOT EXISTS challenges (
    telegram_id INTEGER PRIMARY KEY,
    address     TEXT NOT NULL,
    nonce       TEXT NOT NULL,
    issued_at   INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credits (
    telegram_id INTEGER PRIMARY KEY,
    balance     TEXT NOT NULL DEFAULT '0',
    updated_at  INTEGER NOT NULL
);

-- Every movement, so a balance is explainable rather than asserted. The ref
-- column holds the on-chain block hash for a deposit and the mint's block hash
-- for a spend, and the unique index over (reason, ref) is what makes crediting
-- a deposit idempotent — which matters because the deposit watcher re-reads
-- the same unreceived block until it manages to receive it.
CREATE TABLE IF NOT EXISTS credit_ledger (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_id INTEGER NOT NULL,
    delta       TEXT NOT NULL,
    reason      TEXT NOT NULL,
    ref         TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS credit_ledger_ref
    ON credit_ledger (reason, ref) WHERE ref <> '';
CREATE INDEX IF NOT EXISTS credit_ledger_user ON credit_ledger (telegram_id, id DESC);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// --- identities ---

type Identity struct {
	TelegramID int64
	Username   string
	Address    string
	PublicKey  string
	VerifiedAt int64
}

// ErrAddressTaken is returned when another Telegram account has already
// proved control of the address. Both accounts may genuinely have the key —
// nothing stops two people sharing a wallet — but a deposit from it can only
// be credited to one of them, so the first proof wins and the second is told
// why rather than silently overwriting it.
var ErrAddressTaken = errors.New("that address is already linked to another Telegram account")

// Link records a verified identity, replacing whatever this user had before.
func (s *Store) Link(id Identity) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only if commit did not happen

	var other int64
	err = tx.QueryRow(`SELECT telegram_id FROM identities WHERE address = ?`, id.Address).Scan(&other)
	switch {
	case err == nil && other != id.TelegramID:
		return ErrAddressTaken
	case err != nil && err != sql.ErrNoRows:
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO identities (telegram_id, username, address, public_key, verified_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT (telegram_id) DO UPDATE SET
		   username = excluded.username, address = excluded.address,
		   public_key = excluded.public_key, verified_at = excluded.verified_at`,
		id.TelegramID, id.Username, id.Address, id.PublicKey, id.VerifiedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM challenges WHERE telegram_id = ?`, id.TelegramID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Unlink(telegramID int64) error {
	_, err := s.db.Exec(`DELETE FROM identities WHERE telegram_id = ?`, telegramID)
	return err
}

func (s *Store) Identity(telegramID int64) (*Identity, error) {
	return s.identity(`WHERE telegram_id = ?`, telegramID)
}

// IdentityByAddress is the deposit watcher's lookup: a payment names its
// sender, and this says whose credit it is.
func (s *Store) IdentityByAddress(address string) (*Identity, error) {
	return s.identity(`WHERE address = ?`, address)
}

func (s *Store) identity(where string, arg any) (*Identity, error) {
	var id Identity
	err := s.db.QueryRow(
		`SELECT telegram_id, username, address, public_key, verified_at FROM identities `+where,
		arg).Scan(&id.TelegramID, &id.Username, &id.Address, &id.PublicKey, &id.VerifiedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// --- challenges ---

type Challenge struct {
	TelegramID int64
	Address    string
	Nonce      string
	IssuedAt   int64
	ExpiresAt  int64
}

func (s *Store) PutChallenge(c Challenge) error {
	_, err := s.db.Exec(
		`INSERT INTO challenges (telegram_id, address, nonce, issued_at, expires_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT (telegram_id) DO UPDATE SET
		   address = excluded.address, nonce = excluded.nonce,
		   issued_at = excluded.issued_at, expires_at = excluded.expires_at`,
		c.TelegramID, c.Address, c.Nonce, c.IssuedAt, c.ExpiresAt)
	return err
}

// Challenge returns the user's outstanding challenge, or nil if there is none
// or it has expired. An expired challenge is treated as absent rather than
// returned for the caller to check: there is one right answer and it is not
// worth two of them.
func (s *Store) Challenge(telegramID int64) (*Challenge, error) {
	var c Challenge
	err := s.db.QueryRow(
		`SELECT telegram_id, address, nonce, issued_at, expires_at
		   FROM challenges WHERE telegram_id = ?`, telegramID).
		Scan(&c.TelegramID, &c.Address, &c.Nonce, &c.IssuedAt, &c.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.ExpiresAt {
		return nil, nil
	}
	return &c, nil
}

// --- credits ---

// ErrInsufficient is returned by Spend when the balance will not cover it.
var ErrInsufficient = errors.New("insufficient credit")

// ErrAlreadyApplied is returned when a movement with the same (reason, ref)
// has already been recorded. The deposit watcher relies on it: an unreceived
// block is seen again on every poll until the receive lands, and crediting it
// twice would mint credit out of one payment.
var ErrAlreadyApplied = errors.New("this movement was already recorded")

func (s *Store) Balance(telegramID int64) (*big.Int, error) {
	var raw string
	err := s.db.QueryRow(`SELECT balance FROM credits WHERE telegram_id = ?`, telegramID).Scan(&raw)
	if err == sql.ErrNoRows {
		return big.NewInt(0), nil
	}
	if err != nil {
		return nil, err
	}
	return parseAmount(raw), nil
}

// Credit adds to a balance and records why. `ref` makes it idempotent when it
// is not empty.
func (s *Store) Credit(telegramID int64, amount *big.Int, reason, ref string) (*big.Int, error) {
	return s.move(telegramID, amount, reason, ref, false)
}

// Spend deducts, refusing to go negative. It is called after the thing being
// paid for has actually happened, so a failed mint costs nothing — a user
// charged for a block that never landed has no way to get it back through
// this bot, and that asymmetry is why the debit goes last.
func (s *Store) Spend(telegramID int64, amount *big.Int, reason, ref string) (*big.Int, error) {
	return s.move(telegramID, amount, reason, ref, true)
}

func (s *Store) move(telegramID int64, amount *big.Int, reason, ref string, debit bool) (*big.Int, error) {
	if amount == nil || amount.Sign() < 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only if commit did not happen

	if ref != "" {
		var n int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM credit_ledger WHERE reason = ? AND ref = ?`,
			reason, ref).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, ErrAlreadyApplied
		}
	}

	var raw string
	err = tx.QueryRow(`SELECT balance FROM credits WHERE telegram_id = ?`, telegramID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	balance := parseAmount(raw)

	delta := new(big.Int).Set(amount)
	if debit {
		if balance.Cmp(amount) < 0 {
			return nil, ErrInsufficient
		}
		delta.Neg(delta)
	}
	balance.Add(balance, delta)

	now := time.Now().Unix()
	if _, err := tx.Exec(
		`INSERT INTO credits (telegram_id, balance, updated_at) VALUES (?,?,?)
		 ON CONFLICT (telegram_id) DO UPDATE SET
		   balance = excluded.balance, updated_at = excluded.updated_at`,
		telegramID, balance.String(), now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO credit_ledger (telegram_id, delta, reason, ref, created_at)
		 VALUES (?,?,?,?,?)`,
		telegramID, delta.String(), reason, ref, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return balance, nil
}

// Movement is one row of the ledger behind a balance.
type Movement struct {
	Delta     *big.Int
	Reason    string
	Ref       string
	CreatedAt int64
}

func (s *Store) Ledger(telegramID int64, limit int) ([]Movement, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT delta, reason, ref, created_at FROM credit_ledger
		  WHERE telegram_id = ? ORDER BY id DESC LIMIT ?`, telegramID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movement
	for rows.Next() {
		var m Movement
		var delta string
		if err := rows.Scan(&delta, &m.Reason, &m.Ref, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Delta = parseAmount(delta)
		out = append(out, m)
	}
	return out, rows.Err()
}

// parseAmount is deliberately total: a balance column that cannot be parsed
// reads as zero rather than taking the bot down mid-command. The column is
// only ever written from a *big.Int, so the fallback is unreachable outside
// of a hand-edited database.
func parseAmount(raw string) *big.Int {
	if raw == "" {
		return big.NewInt(0)
	}
	v, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}
