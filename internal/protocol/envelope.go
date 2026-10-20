package protocol

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// ZDU is the Zenoglyphs data unit — the envelope wrapping every payload
// before it becomes AccountBlock.data.
//
// Wire shape (spec R-2.1):
//
//	{ "p": tstr, "op": uint, "v": uint, "pHash"?: bstr, "tHash"?: bstr, "data": any }
//
// Prefix and Version carry whatever the block declared, not what this build
// happens to implement. A decoder that overwrites them can never branch on
// the version a block was minted under, and every "is this valid" check
// downstream becomes a tautology.
type ZDU struct {
	Prefix  string
	Opcode  Opcode
	Version uint64
	PHash   []byte // previous part, multi-part only
	THash   []byte // tail of the chain, multi-part head only
	Data    cbor.RawMessage

	// Ext is the optional-extension lane (spec R-2.9): a map whose keys any
	// indexer may ignore without becoming wrong. See the key-ordering note
	// below for why it is named "ext" and not "x".
	Ext cbor.RawMessage
}

// Envelope keys, in canonical CBOR order. Canonical ordering for definite
// maps is by encoded key: shorter keys first, then bytewise. For these six
// that is: "p", "v", "op", "data", "pHash", "tHash".
const (
	keyP     = "p"
	keyOp    = "op"
	keyV     = "v"
	keyPHash = "pHash"
	keyTHash = "tHash"
	keyData  = "data"

	// keyExt is "ext", not "x", and the reason is canonical ordering.
	// RFC 8949 sorts map keys bytewise by their encoded form, so "x"
	// (0x6178) would sort between "v" (0x6176) and "op" (0x626f70) — landing
	// in the middle of the byte signature and destroying the fixed-length
	// fast path in R-2.4. Any 3-character key encodes with a 0x63 header and
	// therefore sorts after every 1- and 2-character key, which puts "ext"
	// safely between "op" and "data". Two extra bytes, and only when used.
	keyExt = "ext"
)

var (
	// encMode is RFC 8949 §4.2 deterministic encoding: keys sorted by the
	// canonical rule, definite-length maps and strings, shortest-form ints.
	// Injectivity is what makes assetId a genuine commitment to content.
	encMode cbor.EncMode

	// decMode rejects anything the deterministic encoder could not have
	// produced. DupMapKeyEnforcedAPF and IndefLength are the two that matter:
	// both are ways to encode the same logical value twice.
	decMode cbor.DecMode
)

func init() {
	var err error
	encMode, err = cbor.EncOptions{
		Sort:          cbor.SortBytewiseLexical, // RFC 8949 §4.2 core deterministic
		ShortestFloat: cbor.ShortestFloat16,
		NaNConvert:    cbor.NaNConvertReject,
		InfConvert:    cbor.InfConvertReject,
		IndefLength:   cbor.IndefLengthForbidden,
		Time:          cbor.TimeUnix,
	}.EncMode()
	if err != nil {
		panic(err)
	}
	decMode, err = cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		MaxNestedLevels:   16,
		UTF8:              cbor.UTF8RejectInvalid,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
	}.DecMode()
	if err != nil {
		panic(err)
	}
}

// Marshal encodes v in deterministic CBOR.
func Marshal(v any) ([]byte, error) { return encMode.Marshal(v) }

// Unmarshal decodes strictly: no indefinite lengths, no duplicate keys.
func Unmarshal(b []byte, v any) error { return decMode.Unmarshal(b, v) }

var (
	ErrNotZDU        = errors.New("not a zenoglyphs data unit")
	ErrNotCanonical  = errors.New("cbor is not in canonical form")
	ErrTrailingBytes = errors.New("trailing bytes after cbor item")
	ErrUnknownKey    = errors.New("unknown envelope key")
	ErrMissingKey    = errors.New("missing required envelope key")
	ErrBadHashLen    = errors.New("hash field is not 32 bytes")
	ErrFutureVersion = errors.New("zdu version exceeds implemented version")
	ErrUnknownOpcode = errors.New("unknown opcode")
	ErrEmptyPayload  = errors.New("empty payload")
	ErrBadExt        = errors.New("malformed extension map")
)

// Encode produces the deterministic CBOR bytes for this ZDU, ready to be the
// account block's data field.
func (z *ZDU) Encode() ([]byte, error) {
	if len(z.PHash) != 0 && len(z.PHash) != HashLen {
		return nil, ErrBadHashLen
	}
	if len(z.THash) != 0 && len(z.THash) != HashLen {
		return nil, ErrBadHashLen
	}
	m := make(map[string]any, 6)
	m[keyP] = z.Prefix
	m[keyOp] = uint64(z.Opcode)
	m[keyV] = z.Version
	m[keyData] = z.Data
	if len(z.Ext) > 0 {
		m[keyExt] = z.Ext
	}
	if len(z.PHash) > 0 {
		m[keyPHash] = z.PHash
	}
	if len(z.THash) > 0 {
		m[keyTHash] = z.THash
	}
	return encMode.Marshal(m)
}

// rawEnvelope is the decode target. Every field is RawMessage so the decoder
// can enforce types itself rather than trusting cbor's coercion, and so an
// unknown key is visible instead of silently dropped.
type rawEnvelope struct {
	P     cbor.RawMessage `cbor:"p"`
	Op    cbor.RawMessage `cbor:"op"`
	V     cbor.RawMessage `cbor:"v"`
	PHash cbor.RawMessage `cbor:"pHash"`
	THash cbor.RawMessage `cbor:"tHash"`
	Data  cbor.RawMessage `cbor:"data"`
	Ext   cbor.RawMessage `cbor:"ext"`
}

// Decode parses account-block data into a ZDU.
//
// It is deliberately unforgiving. Permissive parsing is how two indexers
// diverge: every branch that guesses at a malformed block is a branch a
// reimplementation has to guess identically. A block this function rejects
// is simply not an inscription.
func Decode(data []byte) (*ZDU, error) {
	if !HasSignature(data) {
		return nil, ErrNotZDU
	}
	if err := requireCanonical(data); err != nil {
		return nil, err
	}

	var raw rawEnvelope
	if err := decMode.Unmarshal(data, &raw); err != nil {
		// ExtraDecErrorUnknownField surfaces stray keys here.
		return nil, fmt.Errorf("%w: %v", ErrUnknownKey, err)
	}
	if len(raw.P) == 0 || len(raw.Op) == 0 || len(raw.V) == 0 || len(raw.Data) == 0 {
		return nil, ErrMissingKey
	}

	z := &ZDU{Data: raw.Data, Ext: raw.Ext}
	if err := decMode.Unmarshal(raw.P, &z.Prefix); err != nil {
		return nil, fmt.Errorf("prefix: %w", err)
	}
	var op, v uint64
	if err := decMode.Unmarshal(raw.Op, &op); err != nil {
		return nil, fmt.Errorf("opcode: %w", err)
	}
	if err := decMode.Unmarshal(raw.V, &v); err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	z.Opcode, z.Version = Opcode(op), v

	for _, f := range []struct {
		raw cbor.RawMessage
		dst *[]byte
	}{{raw.PHash, &z.PHash}, {raw.THash, &z.THash}} {
		if len(f.raw) == 0 {
			continue
		}
		if err := decMode.Unmarshal(f.raw, f.dst); err != nil {
			return nil, fmt.Errorf("hash field: %w", err)
		}
		if len(*f.dst) != HashLen {
			return nil, ErrBadHashLen
		}
	}
	return z, z.ValidateEnvelope()
}

// ValidateEnvelope applies the envelope rules. Payload rules are per-opcode
// and live with the opcode's wire type.
func (z *ZDU) ValidateEnvelope() error {
	if z.Prefix != Prefix {
		return ErrNotZDU
	}
	// A ZDU from the future is ignored, not guessed at: an indexer that
	// half-applies a v2 rule it does not implement corrupts state silently,
	// which is strictly worse than lagging visibly behind.
	if z.Version > Version {
		return ErrFutureVersion
	}
	if z.Version == 0 || z.Version > MaxSingleByteCborUint {
		return ErrFutureVersion
	}
	if !z.Opcode.Known() {
		return ErrUnknownOpcode
	}
	if len(z.Data) == 0 {
		return ErrEmptyPayload
	}
	return ValidateExt(z.Ext)
}

// ValidateExt checks the optional-extension lane.
//
// Two rules, and both matter:
//
//   - It MUST be a map. "Ignore what you do not recognise" is only
//     well-defined over keys; a bare integer or string has no keys to skip,
//     so an indexer would have to guess whether it mattered.
//   - It MUST be bounded. Otherwise ext is a second content channel with no
//     declared type, bypassing the mime allowlist that exists precisely to
//     bound what a viewer can be talked into rendering.
//
// What it does NOT do is interpret anything inside. That is the point.
func ValidateExt(ext cbor.RawMessage) error {
	if len(ext) == 0 {
		return nil
	}
	if len(ext) > MaxExtLen {
		return fmt.Errorf("%w: ext %d > %d bytes", ErrBoundsExceed, len(ext), MaxExtLen)
	}
	var v any
	if err := decMode.Unmarshal(ext, &v); err != nil {
		return fmt.Errorf("%w: ext: %v", ErrBadExt, err)
	}
	if _, ok := v.(map[any]any); !ok {
		return fmt.Errorf("%w: ext must be a map", ErrBadExt)
	}
	if d := depth(v, 0); d > MaxAttributesDep {
		return fmt.Errorf("%w: ext depth %d > %d", ErrBoundsExceed, d, MaxAttributesDep)
	}
	return nil
}

// requireCanonical rejects any encoding the deterministic encoder could not
// have produced, by decoding to a generic value and re-encoding. Round-trip
// equality is the check: it catches non-shortest integers, indefinite
// lengths, and out-of-order map keys in one pass.
//
// This is what makes the encoding injective, and injectivity is what lets a
// block hash stand as a commitment to its content.
func requireCanonical(data []byte) error {
	var v any
	rest, err := decMode.UnmarshalFirst(data, &v)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotCanonical, err)
	}
	if len(rest) != 0 {
		return ErrTrailingBytes
	}
	round, err := encMode.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotCanonical, err)
	}
	if !bytes.Equal(round, data) {
		return ErrNotCanonical
	}
	return nil
}

// --- byte-signature fast path (spec R-2.4) ---
//
// An indexer sees every account block in every momentum. Full CBOR parsing
// of each one to discover that it is an ordinary transfer is wasted work, so
// non-ZDU blocks are rejected on a fixed-length byte comparison first.
//
// Layout after the map header byte (0xA4/0xA5/0xA6, which varies with entry
// count and is therefore skipped):
//
//	61 70          text(1) "p"
//	6? <prefix>    text(n) prefix
//	61 76          text(1) "v"
//	<version>      uint, single byte
//	62 6F 70       text(2) "op"
//	<opcode>       uint, single byte
//
// Canonical key order puts "v" before "op" — both are 1- and 2-byte keys, and
// canonical sorting is by encoded length first. The signature is derived from
// the live Prefix constant so it cannot desync from the encoder.

var sigPrefixPart = func() []byte {
	p := []byte(Prefix)
	if len(p) > 23 {
		panic("protocol prefix must encode in a single-byte CBOR text header")
	}
	b := []byte{0x61, 0x70}          // text(1) "p"
	b = append(b, byte(0x60|len(p))) // text(n)
	b = append(b, p...)              //   prefix
	b = append(b, 0x61, 0x76)        // text(1) "v"
	return b
}()

const sigOpKey = "\x62\x6F\x70" // text(2) "op"

// sigOffset is the index at which the signature begins: past the map header.
const sigOffset = 1

// HasSignature reports whether data could be a ZDU, using only a byte
// comparison. False positives are possible and harmless — Decode does the
// real work. False negatives are not: anything this rejects is never parsed,
// so the signature must hold for every ZDU the encoder can produce.
func HasSignature(data []byte) bool {
	// 4..7 entries: p, op, v, data are mandatory; ext, pHash and tHash are
	// optional. All three optional keys sort after "op", so the signature
	// below stays contiguous whatever combination is present.
	if len(data) < sigOffset+len(sigPrefixPart)+1+len(sigOpKey)+1 {
		return false
	}
	if data[0] < 0xA4 || data[0] > 0xA7 {
		return false
	}
	i := sigOffset
	if !bytes.Equal(data[i:i+len(sigPrefixPart)], sigPrefixPart) {
		return false
	}
	i += len(sigPrefixPart)
	if data[i] > MaxSingleByteCborUint { // version
		return false
	}
	i++
	if string(data[i:i+len(sigOpKey)]) != sigOpKey {
		return false
	}
	i += len(sigOpKey)
	return data[i] <= MaxSingleByteCborUint // opcode
}
