package store

import (
	"database/sql"
	"errors"
	"strconv"
)

// Projection writes. Every one of these is derived from the event log and is
// safe to drop: nothing here may be the only record of something.
//
// Amounts cross the SQL boundary as decimal strings because supply runs to
// 2^64-1 and SQLite's INTEGER is signed 64-bit. In Go they are uint64, which
// holds the full range natively; the total in circulation is the asset's
// supply, so moving units between addresses cannot overflow.

var ErrInsufficient = errors.New("insufficient balance")

func u64(s string) uint64 { v, _ := strconv.ParseUint(s, 10, 64); return v }
func str(v uint64) string { return strconv.FormatUint(v, 10) }

type Asset struct {
	ID           string
	Number       int64
	Issuer       string
	Owner        string
	CollectionID string
	Name         string
	Description  string
	Supply       uint64
	Decimals     uint64
	Mime         string
	Attributes   []byte
	Ext          []byte // optional-extension map: served, never interpreted

	// Content is the inscribed bytes, and Size is how many of them there
	// are. They are separate because a listing needs the second and not the
	// first: an asset holds up to a megabyte (64 parts of 16 KB), a gallery
	// page shows twenty of them, and reading twenty megabytes of blobs to
	// print twenty file sizes is the difference between a listing that scales
	// and one that does not. A row from ListAssets has Size set and Content
	// nil; a row from Asset or AssetByNumber has both.
	Content       []byte
	Size          int
	PartCount     int
	Height        uint64
	MomentumIndex int
	Timestamp     int64
}

func (s *Store) PutAsset(a Asset) error {
	_, err := s.db.Exec(
		`INSERT INTO assets
		   (id, number, issuer, owner, collection_id, name, description, supply,
		    decimals, mime, attributes, ext, content, part_count, height, momentum_index, timestamp)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO NOTHING`,
		a.ID, a.Number, a.Issuer, a.Owner, a.CollectionID, a.Name, a.Description,
		str(a.Supply), a.Decimals, a.Mime, a.Attributes, a.Ext, a.Content, a.PartCount,
		a.Height, a.MomentumIndex, a.Timestamp)
	return err
}

func (s *Store) Asset(id string) (*Asset, error) {
	return scanAsset(s.db.QueryRow(assetCols+` WHERE id = ?`, id))
}

// AssetByNumber resolves an inscription number. Numbers are assigned by
// counting valid mints in canonical order — they are derived, never on-chain,
// and are reassigned from scratch after a rollback.
func (s *Store) AssetByNumber(n int64) (*Asset, error) {
	return scanAsset(s.db.QueryRow(assetCols+` WHERE number = ?`, n))
}

const assetCols = `SELECT id, number, issuer, owner, collection_id, name, description,
	supply, decimals, mime, attributes, assets.ext, content, part_count, assets.height, assets.momentum_index, timestamp
	FROM assets`

type rowScanner interface{ Scan(dest ...any) error }

func scanAsset(r rowScanner) (*Asset, error) {
	var a Asset
	var supply string
	err := r.Scan(&a.ID, &a.Number, &a.Issuer, &a.Owner, &a.CollectionID, &a.Name,
		&a.Description, &supply, &a.Decimals, &a.Mime, &a.Attributes, &a.Ext,
		&a.Content, &a.PartCount, &a.Height, &a.MomentumIndex, &a.Timestamp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Supply, a.Size = u64(supply), len(a.Content)
	return &a, nil
}

// listCols is assetCols without the blob: length(content) instead of content,
// so a listing reads sizes rather than megabytes. Everything else about the
// row is identical, which is what lets both feed the same scan.
const listCols = `SELECT id, number, issuer, owner, collection_id, name, description,
	supply, decimals, mime, attributes, assets.ext, length(content), part_count, assets.height, assets.momentum_index, timestamp
	FROM assets`

func scanAssetRow(r rowScanner) (*Asset, error) {
	var a Asset
	var supply string
	err := r.Scan(&a.ID, &a.Number, &a.Issuer, &a.Owner, &a.CollectionID, &a.Name,
		&a.Description, &supply, &a.Decimals, &a.Mime, &a.Attributes, &a.Ext,
		&a.Size, &a.PartCount, &a.Height, &a.MomentumIndex, &a.Timestamp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Supply = u64(supply)
	return &a, nil
}

// AssetFilter narrows a listing. Zero values mean "no constraint".
type AssetFilter struct {
	Owner      string
	Issuer     string
	Collection string
	Mime       string
	Limit      int
	Offset     int
}

// ListAssets returns assets newest-first, excluding any hidden by moderation.
//
// Moderation is applied here, in the serving path, and never to the event log
// or the state root: two indexers running different policies must still agree
// on history.
func (s *Store) ListAssets(f AssetFilter) ([]Asset, int, error) {
	where := ` WHERE NOT EXISTS (SELECT 1 FROM moderation md
	             WHERE md.asset_id = assets.id AND md.metadata_permitted = 0)`
	args := []any{}
	for _, c := range []struct {
		col string
		val string
	}{
		{"owner", f.Owner}, {"issuer", f.Issuer},
		{"collection_id", f.Collection}, {"mime", f.Mime},
	} {
		if c.val != "" {
			where += " AND " + c.col + " = ?"
			args = append(args, c.val)
		}
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM assets`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		listCols+where+` ORDER BY height DESC, momentum_index DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Asset
	for rows.Next() {
		a, err := scanAssetRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	return out, total, rows.Err()
}

// CarriedAsset is an asset together with its carrier block's own value.
//
// The two live in different tables because they answer different questions:
// the asset is what was inscribed, the event is what the account block did.
// A metaprotocol that prices itself in burned ZNN needs both — the content to
// execute, and the payment to know what it may cost.
type CarriedAsset struct {
	Asset
	Recipient     string // the carrier block's toAddress
	Amount        string // its value, raw units, decimal
	TokenStandard string

	// What the carrier block paid for its bandwidth. The ratio
	// (FusedPlasma + work) / BasePlasma is the dynamic-plasma resource price
	// it met, and that is the congestion term in the EVM's gas price.
	//
	// For a multi-part asset these describe the *head* block only. That is
	// the block the join is keyed on and the one the asset id names; a fuller
	// answer would have to sum the tail, which no rule needs yet.
	BasePlasma  uint64
	FusedPlasma uint64
	Difficulty  uint64
}

// AssetsOfMimeCarried is AssetsOfMime plus what the carrier block moved and
// what it paid, joined from the event log by block hash.
//
// A LEFT JOIN, not an inner one: an asset whose event has been pruned is still
// an asset, and losing it here would silently change derived state. It carries
// no payment, which is the correct reading — an unprovable burn is not a burn —
// and no plasma, which reads as an uncongested block for the same reason.
func (s *Store) AssetsOfMimeCarried(mime string) ([]CarriedAsset, error) {
	return s.carried(`WHERE mime = ?`, mime)
}

// AssetsOfMimeCarriedAfter is AssetsOfMimeCarried restricted to the assets
// that come after one canonical position — the tail a cached replay has not
// seen yet. It is only a correct continuation while ProjectionGeneration is
// unchanged: projections grow in canonical order except across a reset or a
// checkpoint restore, and both of those bump the generation.
func (s *Store) AssetsOfMimeCarriedAfter(mime string, height uint64, index int) ([]CarriedAsset, error) {
	return s.carried(`WHERE mime = ? AND (assets.height > ? OR (assets.height = ? AND assets.momentum_index > ?))`,
		mime, height, height, index)
}

func (s *Store) carried(where string, args ...any) ([]CarriedAsset, error) {
	rows, err := s.db.Query(assetCols[:len(assetCols)-len(" FROM assets")]+
		`, COALESCE(e.recipient,''), COALESCE(e.amount,''), COALESCE(e.token_standard,''),
		   COALESCE(e.base_plasma,0), COALESCE(e.fused_plasma,0), COALESCE(e.difficulty,0)
		 FROM assets LEFT JOIN events e ON e.block_hash = assets.id
		 `+where+` ORDER BY assets.height ASC, assets.momentum_index ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CarriedAsset
	for rows.Next() {
		var (
			a                        Asset
			supply                   string
			recipient, amt, zts      string
			basePlasma, fused, diffi uint64
		)
		if err := rows.Scan(&a.ID, &a.Number, &a.Issuer, &a.Owner, &a.CollectionID,
			&a.Name, &a.Description, &supply, &a.Decimals, &a.Mime, &a.Attributes,
			&a.Ext, &a.Content, &a.PartCount, &a.Height, &a.MomentumIndex,
			&a.Timestamp, &recipient, &amt, &zts,
			&basePlasma, &fused, &diffi); err != nil {
			return nil, err
		}
		a.Supply, a.Size = u64(supply), len(a.Content)
		out = append(out, CarriedAsset{Asset: a, Recipient: recipient,
			Amount: amt, TokenStandard: zts,
			BasePlasma: basePlasma, FusedPlasma: fused, Difficulty: diffi})
	}
	return out, rows.Err()
}

// AssetsOfMime returns every asset of one mime type in canonical order, with
// content and extensions, and **deliberately without the moderation join that
// ListAssets applies**.
//
// That omission is the whole point of a separate method. Moderation is a
// per-indexer serving policy (A-6): two indexers running different blocklists
// must still agree about history. A metaprotocol layered on top — internal/evm
// is the one this exists for — derives state by replaying inscriptions, so if
// it read through the moderated listing then an operator hiding one asset
// would silently fork their derived state away from everyone else's. Serving
// decisions belong at the edge; replay reads the log.
//
// It is therefore not a listing helper and must not be used to render one.
func (s *Store) AssetsOfMime(mime string) ([]Asset, error) {
	rows, err := s.db.Query(
		assetCols+` WHERE mime = ? ORDER BY height ASC, momentum_index ASC`, mime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// NextAssetNumber is the inscription number the next valid mint receives.
func (s *Store) NextAssetNumber() (int64, error) {
	var n sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(number) FROM assets`).Scan(&n); err != nil {
		return 0, err
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64 + 1, nil
}

// --- balances ---

func (s *Store) Balance(assetID, address string) (uint64, error) {
	var amt string
	err := s.db.QueryRow(
		`SELECT amount FROM balances WHERE asset_id = ? AND address = ?`,
		assetID, address).Scan(&amt)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return u64(amt), nil
}

func (s *Store) SetBalance(assetID, address string, amount uint64) error {
	if amount == 0 {
		_, err := s.db.Exec(
			`DELETE FROM balances WHERE asset_id = ? AND address = ?`, assetID, address)
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO balances (asset_id, address, amount) VALUES (?,?,?)
		 ON CONFLICT (asset_id, address) DO UPDATE SET amount = excluded.amount`,
		assetID, address, str(amount))
	return err
}

// Move debits from and credits to, atomically at the projection level.
// A transfer that would overdraw is rejected whole — never partially applied.
func (s *Store) Move(assetID, from, to string, amount uint64) error {
	fromBal, err := s.Balance(assetID, from)
	if err != nil {
		return err
	}
	if fromBal < amount {
		return ErrInsufficient
	}
	toBal, err := s.Balance(assetID, to)
	if err != nil {
		return err
	}
	if err := s.SetBalance(assetID, from, fromBal-amount); err != nil {
		return err
	}
	return s.SetBalance(assetID, to, toBal+amount)
}

// Burn destroys units: debited from the sender and credited to no one, so the
// circulating total drops below supply.
func (s *Store) Burn(assetID, from string, amount uint64) error {
	bal, err := s.Balance(assetID, from)
	if err != nil {
		return err
	}
	if bal < amount {
		return ErrInsufficient
	}
	return s.SetBalance(assetID, from, bal-amount)
}

type Holding struct {
	AssetID string
	Amount  uint64
}

func (s *Store) HoldingsOf(address string) ([]Holding, error) {
	rows, err := s.db.Query(
		`SELECT asset_id, amount FROM balances WHERE address = ? ORDER BY asset_id`, address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Holding
	for rows.Next() {
		var h Holding
		var amt string
		if err := rows.Scan(&h.AssetID, &amt); err != nil {
			return nil, err
		}
		h.Amount = u64(amt)
		out = append(out, h)
	}
	return out, rows.Err()
}

type HolderRow struct {
	Address string
	Amount  uint64
}

func (s *Store) HoldersOf(assetID string) ([]HolderRow, error) {
	rows, err := s.db.Query(
		`SELECT address, amount FROM balances WHERE asset_id = ?
		  ORDER BY CAST(amount AS INTEGER) DESC, address ASC`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HolderRow
	for rows.Next() {
		var h HolderRow
		var amt string
		if err := rows.Scan(&h.Address, &amt); err != nil {
			return nil, err
		}
		h.Amount = u64(amt)
		out = append(out, h)
	}
	return out, rows.Err()
}

// --- collections ---

type Collection struct {
	ID            string
	Issuer        string
	Name          string
	Description   string
	URL           string
	Height        uint64
	MomentumIndex int
	Timestamp     int64
}

func (s *Store) PutCollection(c Collection) error {
	_, err := s.db.Exec(
		`INSERT INTO collections (id, issuer, name, description, url, height, momentum_index, timestamp)
		 VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Issuer, c.Name, c.Description, c.URL, c.Height, c.MomentumIndex, c.Timestamp)
	return err
}

func (s *Store) Collection(id string) (*Collection, error) {
	var c Collection
	err := s.db.QueryRow(
		`SELECT id, issuer, name, description, url, height, momentum_index, timestamp
		   FROM collections WHERE id = ?`, id).
		Scan(&c.ID, &c.Issuer, &c.Name, &c.Description, &c.URL, &c.Height,
			&c.MomentumIndex, &c.Timestamp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (s *Store) ListCollections() ([]Collection, error) {
	rows, err := s.db.Query(
		`SELECT id, issuer, name, description, url, height, momentum_index, timestamp
		   FROM collections ORDER BY height DESC, momentum_index DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Issuer, &c.Name, &c.Description, &c.URL,
			&c.Height, &c.MomentumIndex, &c.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- dispensers ---

type Dispenser struct {
	ID            string
	Issuer        string
	AssetID       string
	Remaining     uint64
	Price         uint64
	ZTS           string
	Cancelled     bool
	Height        uint64
	MomentumIndex int
	Timestamp     int64
}

func (s *Store) PutDispenser(d Dispenser) error {
	_, err := s.db.Exec(
		`INSERT INTO dispensers (id, issuer, asset_id, remaining, price, zts, cancelled,
		                         height, momentum_index, timestamp)
		 VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`,
		d.ID, d.Issuer, d.AssetID, str(d.Remaining), str(d.Price), d.ZTS,
		boolInt(d.Cancelled), d.Height, d.MomentumIndex, d.Timestamp)
	return err
}

func (s *Store) Dispenser(id string) (*Dispenser, error) {
	var d Dispenser
	var remaining, price string
	var cancelled int
	err := s.db.QueryRow(
		`SELECT id, issuer, asset_id, remaining, price, zts, cancelled, height, momentum_index, timestamp
		   FROM dispensers WHERE id = ?`, id).
		Scan(&d.ID, &d.Issuer, &d.AssetID, &remaining, &price, &d.ZTS, &cancelled,
			&d.Height, &d.MomentumIndex, &d.Timestamp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.Remaining, d.Price, d.Cancelled = u64(remaining), u64(price), cancelled != 0
	return &d, nil
}

func (s *Store) UpdateDispenser(id string, remaining uint64, cancelled bool) error {
	_, err := s.db.Exec(
		`UPDATE dispensers SET remaining = ?, cancelled = ? WHERE id = ?`,
		str(remaining), boolInt(cancelled), id)
	return err
}

func (s *Store) ListDispensers(openOnly bool) ([]Dispenser, error) {
	q := `SELECT id, issuer, asset_id, remaining, price, zts, cancelled, height, momentum_index, timestamp
	        FROM dispensers`
	if openOnly {
		q += ` WHERE cancelled = 0 AND CAST(remaining AS INTEGER) > 0`
	}
	q += ` ORDER BY height DESC, momentum_index DESC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDispensers(rows)
}

// --- multi-part chunks ---

type Part struct {
	BlockHash     string
	Issuer        string
	Height        uint64
	MomentumIndex int
	PHash         string
	Chunk         []byte
}

func (s *Store) PutPart(p Part) error {
	_, err := s.db.Exec(
		`INSERT INTO parts (block_hash, issuer, height, momentum_index, p_hash, chunk)
		 VALUES (?,?,?,?,?,?) ON CONFLICT (block_hash) DO NOTHING`,
		p.BlockHash, p.Issuer, p.Height, p.MomentumIndex, p.PHash, p.Chunk)
	return err
}

func (s *Store) Part(blockHash string) (*Part, error) {
	var p Part
	err := s.db.QueryRow(
		`SELECT block_hash, issuer, height, momentum_index, p_hash, chunk
		   FROM parts WHERE block_hash = ?`, blockHash).
		Scan(&p.BlockHash, &p.Issuer, &p.Height, &p.MomentumIndex, &p.PHash, &p.Chunk)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// ClaimTail records that a head consumed a tail. It returns false if the tail
// was already consumed — the first head in canonical order wins, and every
// later claim on the same chain is invalid.
func (s *Store) ClaimTail(tailHash, assetID string, height uint64) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO consumed_tails (tail_hash, asset_id, height) VALUES (?, ?, ?)
		 ON CONFLICT (tail_hash) DO NOTHING`, tailHash, assetID, height)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// --- transfers (activity feed) ---

type Transfer struct {
	Height        uint64
	MomentumIndex int
	BlockHash     string
	AssetID       string
	Sender        string
	Recipient     string
	Amount        uint64
	Burned        bool
	Timestamp     int64
}

func (s *Store) PutTransfer(t Transfer) error {
	_, err := s.db.Exec(
		`INSERT INTO transfers (height, momentum_index, block_hash, asset_id,
		                        sender, recipient, amount, burned, timestamp)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (height, momentum_index) DO NOTHING`,
		t.Height, t.MomentumIndex, t.BlockHash, t.AssetID, t.Sender, t.Recipient,
		str(t.Amount), boolInt(t.Burned), t.Timestamp)
	return err
}

func (s *Store) TransfersOf(assetID string, limit int) ([]Transfer, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT height, momentum_index, block_hash, asset_id, sender, recipient,
		        amount, burned, timestamp
		   FROM transfers WHERE asset_id = ?
		  ORDER BY height DESC, momentum_index DESC LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transfer
	for rows.Next() {
		var t Transfer
		var amt string
		var burned int
		if err := rows.Scan(&t.Height, &t.MomentumIndex, &t.BlockHash, &t.AssetID,
			&t.Sender, &t.Recipient, &amt, &burned, &t.Timestamp); err != nil {
			return nil, err
		}
		t.Amount, t.Burned = u64(amt), burned != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- moderation (serving policy only) ---

type Moderation struct {
	MetadataPermitted bool
	DataPermitted     bool
	Reason            string
}

func (s *Store) Moderation(assetID string) (Moderation, error) {
	m := Moderation{MetadataPermitted: true, DataPermitted: true}
	var meta, data int
	err := s.db.QueryRow(
		`SELECT metadata_permitted, data_permitted, reason FROM moderation WHERE asset_id = ?`,
		assetID).Scan(&meta, &data, &m.Reason)
	if err == sql.ErrNoRows {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	m.MetadataPermitted, m.DataPermitted = meta != 0, data != 0
	return m, nil
}

// ModerationRow is one stored policy, for the operator's own review.
type ModerationRow struct {
	AssetID string
	Moderation
}

// ListModeration returns every asset with a policy set. Only rows that
// actually restrict something are listed — a row that permits everything is
// indistinguishable from no row at all.
func (s *Store) ListModeration() ([]ModerationRow, error) {
	rows, err := s.db.Query(
		`SELECT asset_id, metadata_permitted, data_permitted, reason
		   FROM moderation
		  WHERE metadata_permitted = 0 OR data_permitted = 0
		  ORDER BY asset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModerationRow
	for rows.Next() {
		var r ModerationRow
		var meta, data int
		if err := rows.Scan(&r.AssetID, &meta, &data, &r.Reason); err != nil {
			return nil, err
		}
		r.MetadataPermitted, r.DataPermitted = meta != 0, data != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetModeration(assetID string, m Moderation) error {
	_, err := s.db.Exec(
		`INSERT INTO moderation (asset_id, metadata_permitted, data_permitted, reason)
		 VALUES (?,?,?,?)
		 ON CONFLICT (asset_id) DO UPDATE SET
		   metadata_permitted = excluded.metadata_permitted,
		   data_permitted = excluded.data_permitted,
		   reason = excluded.reason`,
		assetID, boolInt(m.MetadataPermitted), boolInt(m.DataPermitted), m.Reason)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
