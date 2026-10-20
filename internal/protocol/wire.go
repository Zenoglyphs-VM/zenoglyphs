package protocol

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
)

// Wire schemas are not API schemas.
//
// The types here are exactly what goes on the chain and nothing more. Derived
// fields — issuer, owner, momentum height, index number — come from the block
// and the indexer, never from a payload, so they have no place in a wire
// struct. Letting one struct serve both roles is how a payload ends up able to
// assert its own issuer.
//
// Every payload is validated by exact key set. An unknown key makes the
// payload invalid rather than being ignored: permissive parsing is the single
// most reliable way for two indexers to diverge.
//
// The one deliberate exception is `ext` (spec R-2.9): an optional map whose
// *contents* an indexer may ignore key by key. It is the optional upgrade
// lane, and it carries one hard rule —
//
//	Nothing inside ext may affect protocol state.
//
// That is stricter than Runes' odd tags, and the state root is why. If an ext
// key could change a balance, then an indexer that understood it and one that
// did not would both mark the event applied, commit to the same root, and
// hold different state — reintroducing exactly the blindness R-6.4 exists to
// remove. So ext is consensus-inert: carried, committed to by the block hash,
// served to clients, never interpreted by the state machine. Anything that
// must change state uses a new top-level key (mandatory lane) or a version
// bump.

var (
	ErrBadPayload   = errors.New("payload does not match wire schema")
	ErrBoundsExceed = errors.New("payload exceeds a protocol bound")
	ErrBadMime      = errors.New("mime type not in allowlist")
)

// MintWire is the op=0 payload: a complete asset definition.
//
// ASSUMPTION A-1: data is always a CBOR byte string, never a text string.
// The plan allowed bstr|tstr; permitting both gives the same content two
// distinct encodings, which costs injectivity for nothing, and a multi-part
// chain could otherwise mix the two across chunks. Interpretation is the
// mime type's job — this mirrors Ordinals exactly: a content type plus a
// byte string.
type MintWire struct {
	Name        string          `cbor:"name,omitempty"`
	Description string          `cbor:"description,omitempty"`
	Collection  []byte          `cbor:"collection,omitempty"` // 32-byte collection id
	Supply      uint64          `cbor:"supply"`
	Decimals    uint64          `cbor:"decimals"`
	Attributes  cbor.RawMessage `cbor:"attributes,omitempty"`
	Mime        string          `cbor:"mime"`
	Data        []byte          `cbor:"data"`

	// Refs is the composite import list (spec R-4.7): the asset ids this
	// inscription is declared to be built from, in slot order. It is the
	// "list of sequential imports, referenced like variables" from the
	// original design notes, and the content origin serves slot n of asset id
	// at /content/{id}/{n} — so inscribed HTML, CSS or SVG reaches an import
	// with the relative URL "0", "1", "2" and never has to embed a hash.
	//
	// The indexer records the edges. It never resolves them, never fetches
	// them, and never validates that the target exists: a ref is a claim
	// about composition, not a dependency the state machine has.
	Refs [][]byte `cbor:"refs,omitempty"`

	Ext cbor.RawMessage `cbor:"ext,omitempty"`
}

var mintKeys = keySet("name", "description", "collection", "supply", "decimals", "attributes", "mime", "data", "refs", "ext")

func (m *MintWire) Validate() error {
	if len(m.Name) > MaxNameLen {
		return fmt.Errorf("%w: name %d > %d", ErrBoundsExceed, len(m.Name), MaxNameLen)
	}
	if !utf8.ValidString(m.Name) || !utf8.ValidString(m.Description) {
		return fmt.Errorf("%w: name/description not valid utf-8", ErrBadPayload)
	}
	if len(m.Description) > MaxDescriptionLen {
		return fmt.Errorf("%w: description %d > %d", ErrBoundsExceed, len(m.Description), MaxDescriptionLen)
	}
	if len(m.Collection) != 0 && len(m.Collection) != HashLen {
		return fmt.Errorf("%w: collection id not %d bytes", ErrBadPayload, HashLen)
	}
	if m.Supply < 1 {
		return fmt.Errorf("%w: supply must be >= 1", ErrBoundsExceed)
	}
	if m.Decimals > MaxDecimals {
		return fmt.Errorf("%w: decimals %d > %d", ErrBoundsExceed, m.Decimals, MaxDecimals)
	}
	if len(m.Attributes) > 0 {
		if len(m.Attributes) > MaxAttributesLen {
			return fmt.Errorf("%w: attributes %d > %d", ErrBoundsExceed, len(m.Attributes), MaxAttributesLen)
		}
		if err := checkAttributes(m.Attributes); err != nil {
			return err
		}
	}
	if len(m.Mime) == 0 || len(m.Mime) > MaxMimeLen {
		return fmt.Errorf("%w: mime length", ErrBadPayload)
	}
	if !MimeAllowed(m.Mime) {
		return fmt.Errorf("%w: %q", ErrBadMime, m.Mime)
	}
	if err := validateRefs(m.Refs); err != nil {
		return err
	}
	if err := ValidateExt(m.Ext); err != nil {
		return err
	}
	// Data may be empty only in a multi-part head, where the remaining
	// chunks supply the content; the caller knows which case it is, so the
	// emptiness rule lives in the mint validator, not here.
	return nil
}

// TransferWire is the op=1 payload.
//
// The block's own ZTS and amount are irrelevant to a transfer and MUST be
// ignored — the protocol amount is n, and the carrier is a 0-value send.
type TransferWire struct {
	Asset  []byte          `cbor:"a"` // 32-byte asset id
	Amount uint64          `cbor:"n"`
	Ext    cbor.RawMessage `cbor:"ext,omitempty"`
}

var transferKeys = keySet("a", "n", "ext")

func (t *TransferWire) Validate() error {
	if len(t.Asset) != HashLen {
		return fmt.Errorf("%w: asset id not %d bytes", ErrBadPayload, HashLen)
	}
	if t.Amount < 1 {
		return fmt.Errorf("%w: amount must be >= 1", ErrBoundsExceed)
	}
	return ValidateExt(t.Ext)
}

// CollectionWire is the op=3 payload.
type CollectionWire struct {
	Name        string          `cbor:"name"`
	Description string          `cbor:"description,omitempty"`
	URL         string          `cbor:"url,omitempty"`
	Ext         cbor.RawMessage `cbor:"ext,omitempty"`
}

var collectionKeys = keySet("name", "description", "url", "ext")

func (c *CollectionWire) Validate() error {
	if len(c.Name) == 0 || len(c.Name) > MaxNameLen {
		return fmt.Errorf("%w: name length", ErrBoundsExceed)
	}
	if len(c.Description) > MaxDescriptionLen {
		return fmt.Errorf("%w: description length", ErrBoundsExceed)
	}
	if len(c.URL) > MaxDescriptionLen {
		return fmt.Errorf("%w: url length", ErrBoundsExceed)
	}
	if !utf8.ValidString(c.Name) || !utf8.ValidString(c.Description) || !utf8.ValidString(c.URL) {
		return fmt.Errorf("%w: not valid utf-8", ErrBadPayload)
	}
	return ValidateExt(c.Ext)
}

// DispenserWire is the op=4 payload: offer Amount units of Asset at Price
// each, paid in ZTS.
type DispenserWire struct {
	Asset  []byte          `cbor:"a"`
	Amount uint64          `cbor:"n"`
	Price  uint64          `cbor:"price"` // in the smallest unit of ZTS
	ZTS    []byte          `cbor:"zts"`   // 10-byte Zenon token standard
	Ext    cbor.RawMessage `cbor:"ext,omitempty"`
}

var dispenserKeys = keySet("a", "n", "price", "zts", "ext")

// ZTSLen is the byte length of a Zenon token standard.
const ZTSLen = 10

func (d *DispenserWire) Validate() error {
	if len(d.Asset) != HashLen {
		return fmt.Errorf("%w: asset id not %d bytes", ErrBadPayload, HashLen)
	}
	if d.Amount < 1 {
		return fmt.Errorf("%w: amount must be >= 1", ErrBoundsExceed)
	}
	if d.Price < 1 {
		return fmt.Errorf("%w: price must be >= 1", ErrBoundsExceed)
	}
	if len(d.ZTS) != ZTSLen {
		return fmt.Errorf("%w: zts not %d bytes", ErrBadPayload, ZTSLen)
	}
	return ValidateExt(d.Ext)
}

// DispenserRefWire is the op=5 and op=6 payload.
type DispenserRefWire struct {
	Dispenser []byte          `cbor:"d"`
	Ext       cbor.RawMessage `cbor:"ext,omitempty"`
}

var dispenserRefKeys = keySet("d", "ext")

func (d *DispenserRefWire) Validate() error {
	if len(d.Dispenser) != HashLen {
		return fmt.Errorf("%w: dispenser id not %d bytes", ErrBadPayload, HashLen)
	}
	return ValidateExt(d.Ext)
}

// --- decoding ---

type validator interface{ Validate() error }

// decodePayload unmarshals a payload and enforces its exact key set.
//
// cbor's ExtraDecErrorUnknownField already rejects unknown keys, but it
// cannot tell a missing required key from a zero value, so the key set is
// checked explicitly against the map actually on the wire.
func decodePayload(raw cbor.RawMessage, allowed map[string]bool, required []string, dst validator) error {
	var seen map[string]cbor.RawMessage
	if err := decMode.Unmarshal(raw, &seen); err != nil {
		return fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	for k := range seen {
		if !allowed[k] {
			return fmt.Errorf("%w: unknown key %q", ErrBadPayload, k)
		}
	}
	for _, k := range required {
		if _, ok := seen[k]; !ok {
			return fmt.Errorf("%w: missing key %q", ErrBadPayload, k)
		}
	}
	if err := decMode.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	return dst.Validate()
}

func (z *ZDU) DecodeMint() (*MintWire, error) {
	m := &MintWire{}
	return m, decodePayload(z.Data, mintKeys, []string{"supply", "decimals", "mime", "data"}, m)
}

func (z *ZDU) DecodeTransfer() (*TransferWire, error) {
	t := &TransferWire{}
	return t, decodePayload(z.Data, transferKeys, []string{"a", "n"}, t)
}

func (z *ZDU) DecodeCollection() (*CollectionWire, error) {
	c := &CollectionWire{}
	return c, decodePayload(z.Data, collectionKeys, []string{"name"}, c)
}

func (z *ZDU) DecodeDispenser() (*DispenserWire, error) {
	d := &DispenserWire{}
	return d, decodePayload(z.Data, dispenserKeys, []string{"a", "n", "price", "zts"}, d)
}

func (z *ZDU) DecodeDispenserRef() (*DispenserRefWire, error) {
	d := &DispenserRefWire{}
	return d, decodePayload(z.Data, dispenserRefKeys, []string{"d"}, d)
}

// DecodeChunk reads an op=2 payload: a bare byte string, nothing else.
func (z *ZDU) DecodeChunk() ([]byte, error) {
	var b []byte
	if err := decMode.Unmarshal(z.Data, &b); err != nil {
		return nil, fmt.Errorf("%w: chunk is not a byte string: %v", ErrBadPayload, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: empty chunk", ErrBadPayload)
	}
	return b, nil
}

// validateRefs checks a composite's import list.
//
// Three rules, and the third is the one that matters:
//
//   - Each id is exactly 32 bytes, because it is a block hash.
//   - At most MaxRefs, because the list shares a block with the content.
//   - Present means non-empty. An explicit `refs: []` is a second spelling of
//     "no refs", and two encodings of one object is exactly what R-2.3 exists
//     to prevent.
//
// Duplicates are permitted. Slots are positional — refs[0] and refs[3] may
// name the same asset — and a rule against it would protect nothing while
// giving two indexers one more thing to agree on.
//
// Notably absent: any check that the target exists. A ref to an unknown id is
// a dangling edge a client renders as missing, not an invalid mint. Requiring
// the target to be indexed would make validity depend on how far an indexer
// had synced.
func validateRefs(refs [][]byte) error {
	if refs == nil {
		return nil
	}
	if len(refs) == 0 {
		return fmt.Errorf("%w: refs present but empty", ErrBadPayload)
	}
	if len(refs) > MaxRefs {
		return fmt.Errorf("%w: %d refs > %d", ErrBoundsExceed, len(refs), MaxRefs)
	}
	for i, r := range refs {
		if len(r) != HashLen {
			return fmt.Errorf("%w: ref %d is %d bytes, not %d", ErrBadPayload, i, len(r), HashLen)
		}
	}
	return nil
}

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkAttributes bounds the nesting depth of the attributes map.
//
// Depth is a denial-of-service bound, not a taste judgement: an indexer that
// walks attacker-supplied nesting without a limit can be made to recurse
// until it dies. The CBOR decoder's own MaxNestedLevels covers the envelope;
// this narrows it to the protocol's own, tighter rule.
func checkAttributes(raw cbor.RawMessage) error {
	var v any
	if err := decMode.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%w: attributes: %v", ErrBadPayload, err)
	}
	if _, ok := v.(map[any]any); !ok {
		return fmt.Errorf("%w: attributes must be a map", ErrBadPayload)
	}
	if d := depth(v, 0); d > MaxAttributesDep {
		return fmt.Errorf("%w: attributes depth %d > %d", ErrBoundsExceed, d, MaxAttributesDep)
	}
	return nil
}

func depth(v any, cur int) int {
	if cur > MaxAttributesDep {
		return cur // already over; stop walking
	}
	max := cur
	switch t := v.(type) {
	case map[any]any:
		for k, val := range t {
			if d := depth(k, cur+1); d > max {
				max = d
			}
			if d := depth(val, cur+1); d > max {
				max = d
			}
		}
	case []any:
		for _, val := range t {
			if d := depth(val, cur+1); d > max {
				max = d
			}
		}
	}
	return max
}
