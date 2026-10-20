package protocol

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func mustEncodeMint(t *testing.T, m MintWire) []byte {
	t.Helper()
	payload, err := Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
	b, err := z.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEnvelopeRoundTrip(t *testing.T) {
	m := MintWire{
		Name:     "hello",
		Supply:   1,
		Decimals: 0,
		Mime:     "text/plain;charset=utf-8",
		Data:     []byte("hello, momentum"),
	}
	raw := mustEncodeMint(t, m)

	z, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if z.Prefix != Prefix || z.Opcode != OpMint || z.Version != Version {
		t.Fatalf("envelope mismatch: %+v", z)
	}
	got, err := z.DecodeMint()
	if err != nil {
		t.Fatalf("decode mint: %v", err)
	}
	if got.Name != m.Name || !bytes.Equal(got.Data, m.Data) || got.Mime != m.Mime {
		t.Fatalf("payload mismatch: %+v", got)
	}
}

// The decoded prefix and version must be what the block said, not what this
// build implements — B-1 in the plan's ledger. A decoder that overwrites them
// makes every downstream validity check a tautology.
func TestDecodePreservesDeclaredFields(t *testing.T) {
	payload, _ := Marshal(&MintWire{Supply: 1, Mime: "text/plain;charset=utf-8", Data: []byte("x")})
	z := &ZDU{Prefix: "xxx", Opcode: OpMint, Version: 7, Data: payload}
	raw, err := z.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// A foreign prefix must not even reach the parser.
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected foreign prefix to be rejected")
	}

	z2 := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: 7, Data: payload}
	raw2, _ := z2.Encode()
	got, err := Decode(raw2)
	if err == nil {
		t.Fatal("expected a future version to be rejected")
	}
	if got == nil || got.Version != 7 {
		t.Fatalf("declared version not preserved through decode: %+v", got)
	}
}

func TestSignatureFastPath(t *testing.T) {
	// Every opcode, with and without the optional hash fields, must be
	// recognised — a false negative here means a valid inscription is never
	// parsed at all.
	probe := bytes.Repeat([]byte{1}, HashLen)
	for op := Opcode(0); op < opCount; op++ {
		for _, hashes := range [][2][]byte{{nil, nil}, {probe, nil}, {probe, probe}} {
			payload, _ := Marshal([]byte("x"))
			z := &ZDU{Prefix: Prefix, Opcode: op, Version: Version,
				Data: payload, PHash: hashes[0], THash: hashes[1]}
			raw, err := z.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if !HasSignature(raw) {
				t.Fatalf("op=%d hashes=%v not recognised: %s", op, hashes, hex.EncodeToString(raw))
			}
		}
	}
	for _, bad := range [][]byte{
		nil,
		{0xA4},
		[]byte("not cbor at all, just some bytes here"),
	} {
		if HasSignature(bad) {
			t.Fatalf("false positive on %x", bad)
		}
	}
	// An ordinary non-protocol CBOR map must be rejected cheaply.
	other, _ := Marshal(map[string]any{"hello": "world"})
	if HasSignature(other) {
		t.Fatal("false positive on unrelated cbor")
	}
}

// Canonical form is what makes the encoding injective, and injectivity is
// what lets a block hash stand as a commitment to content.
func TestNonCanonicalRejected(t *testing.T) {
	payload, _ := Marshal(&MintWire{Supply: 1, Mime: "text/plain;charset=utf-8", Data: []byte("x")})
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
	good, _ := z.Encode()

	// Same logical map, keys in insertion order rather than canonical order.
	insertion, err := cbor.EncOptions{Sort: cbor.SortNone}.EncMode()
	if err != nil {
		t.Fatal(err)
	}
	type ordered struct {
		P    string          `cbor:"p"`
		Op   uint64          `cbor:"op"`
		V    uint64          `cbor:"v"`
		Data cbor.RawMessage `cbor:"data"`
	}
	bad, err := insertion.Marshal(&ordered{Prefix, uint64(OpMint), Version, payload})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(bad, good) {
		t.Skip("encoder produced identical bytes; nothing to test")
	}
	if _, err := Decode(bad); err == nil {
		t.Fatal("non-canonical key order was accepted")
	}

	// Trailing bytes after a complete item.
	if _, err := Decode(append(append([]byte{}, good...), 0x00)); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	// An extra envelope key.
	raw, _ := Marshal(map[string]any{
		"p": Prefix, "op": uint64(OpMint), "v": uint64(Version),
		"data": []byte("x"), "extra": 1,
	})
	if _, err := Decode(raw); err == nil {
		t.Fatal("unknown envelope key accepted")
	}

	// An extra payload key.
	payload, _ := Marshal(map[string]any{
		"supply": uint64(1), "decimals": uint64(0),
		"mime": "text/plain;charset=utf-8", "data": []byte("x"), "surprise": true,
	})
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
	b, _ := z.Encode()
	zdu, err := Decode(b)
	if err != nil {
		t.Fatalf("envelope should decode: %v", err)
	}
	if _, err := zdu.DecodeMint(); err == nil {
		t.Fatal("unknown payload key accepted")
	}
}

func TestUnknownOpcodeIsNotFatal(t *testing.T) {
	// op=99 must be rejected, never panic — it arrives straight off chain.
	payload, _ := Marshal([]byte("x"))
	raw, _ := Marshal(map[string]any{
		"p": Prefix, "op": uint64(99), "v": uint64(Version), "data": cbor.RawMessage(payload),
	})
	if _, err := Decode(raw); err == nil {
		t.Fatal("op=99 accepted")
	}
	if Opcode(99).Known() {
		t.Fatal("op=99 reported as known")
	}
	if Opcode(99).String() != "unknown" {
		t.Fatal("op=99 name")
	}
}

func TestMintBounds(t *testing.T) {
	base := MintWire{Supply: 1, Mime: "text/plain;charset=utf-8", Data: []byte("x")}
	cases := map[string]func(*MintWire){
		"long name":        func(m *MintWire) { m.Name = string(bytes.Repeat([]byte("a"), MaxNameLen+1)) },
		"long description": func(m *MintWire) { m.Description = string(bytes.Repeat([]byte("a"), MaxDescriptionLen+1)) },
		"zero supply":      func(m *MintWire) { m.Supply = 0 },
		"decimals":         func(m *MintWire) { m.Decimals = MaxDecimals + 1 },
		"bad mime":         func(m *MintWire) { m.Mime = "application/x-evil" },
		"short collection": func(m *MintWire) { m.Collection = []byte{1, 2, 3} },
	}
	for name, mutate := range cases {
		m := base
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline rejected: %v", err)
	}
}

// B-8 in the plan: the old chunker dropped tail bytes on exact multiples and
// gave the head the shorter allowance, so a head with two hashes could
// overflow the block limit. Both are size invariants, so test them by size.
func TestChunkerInvariants(t *testing.T) {
	meta := MintWire{
		Name:        "invariant probe",
		Description: string(bytes.Repeat([]byte("d"), 300)),
		Supply:      1,
		Mime:        "image/png",
	}
	rng := rand.New(rand.NewSource(1))
	probe := bytes.Repeat([]byte{0xAB}, HashLen)

	for _, mode := range []PlasmaMode{PlasmaPoW, PlasmaFused} {
		max := mode.MaxBlockData()
		sizes := []int{1, 100, 500, max - 1, max, max + 1, 2 * max, 3*max + 7, 5 * max}
		for _, size := range sizes {
			content := make([]byte, size)
			rng.Read(content)

			plan, err := BuildPlan(meta, content, mode)
			if err != nil {
				// Only ever legitimate if the content cannot fit in MaxAssetParts.
				if size < MaxAssetParts*max/2 {
					t.Fatalf("mode=%s size=%d: %v", mode, size, err)
				}
				continue
			}

			// Lossless: the parts in content order rebuild the input exactly.
			var rebuilt []byte
			for _, p := range plan.Parts {
				rebuilt = append(rebuilt, p.Chunk...)
			}
			if !bytes.Equal(rebuilt, content) {
				t.Fatalf("mode=%s size=%d: lossy — rebuilt %d of %d bytes",
					mode, size, len(rebuilt), len(content))
			}

			// No block over the limit, with worst-case hashes present.
			for _, p := range plan.Parts {
				var ph, th []byte
				switch p.Role {
				case RoleHead:
					ph, th = probe, probe
				case RoleIntermediary:
					ph = probe
				}
				b, err := plan.EncodePart(p, ph, th)
				if err != nil {
					t.Fatalf("mode=%s size=%d %s: %v", mode, size, p.Role, err)
				}
				if len(b) > max {
					t.Fatalf("mode=%s size=%d %s: block %d bytes > limit %d",
						mode, size, p.Role, len(b), max)
				}
			}

			// Roles are well-formed: exactly one head-ish part at the front,
			// at most one tail, and it is last.
			first := plan.Parts[0].Role
			if first != RoleHead && first != RoleStandalone {
				t.Fatalf("mode=%s size=%d: first part is %s", mode, size, first)
			}
			if len(plan.Parts) > 1 {
				if first != RoleHead {
					t.Fatalf("mode=%s size=%d: multi-part chain led by %s", mode, size, first)
				}
				if last := plan.Parts[len(plan.Parts)-1].Role; last != RoleTail {
					t.Fatalf("mode=%s size=%d: chain ends with %s", mode, size, last)
				}
			}
			// Submission order is reversed: the head goes last.
			sub := plan.SubmitOrder()
			if sub[len(sub)-1].Role != first {
				t.Fatalf("mode=%s size=%d: submit order does not end at the head", mode, size)
			}
		}
	}
}

// The measured saving from CBOR byte strings over a base64 data-URI, which is
// the reason §4.1 changed the encoding. Reported, not asserted at a threshold.
func TestByteStringBeatsBase64(t *testing.T) {
	content := make([]byte, 48*1024)
	rand.New(rand.NewSource(2)).Read(content)
	meta := MintWire{Name: "sizing", Supply: 1, Mime: "image/png"}

	plan, err := BuildPlan(meta, content, PlasmaFused)
	if err != nil {
		t.Fatal(err)
	}
	// base64 inflates by 4/3, plus the "data:image/png;base64," preamble.
	b64len := (len(content)+2)/3*4 + len("data:image/png;base64,")
	b64blocks := (b64len + MaxDataLength - 1) / MaxDataLength

	t.Logf("source %d B -> cbor bstr: %d blocks; base64 data-uri: ~%d B, %d blocks",
		len(content), len(plan.Parts), b64len, b64blocks)
	if len(plan.Parts) > b64blocks {
		t.Fatalf("byte strings used more blocks (%d) than base64 (%d)", len(plan.Parts), b64blocks)
	}
}

// --- the optional-extension lane (R-2.9) ---

// The whole point of ext: an indexer that has never heard of a key inside it
// still decodes the ZDU, still validates it, and still computes the same
// state. That is what makes it an upgrade lane rather than a version bump.
func TestExtIsIgnorableButPreserved(t *testing.T) {
	future, err := Marshal(map[string]any{
		"license":   "CC0",
		"unheardOf": []any{uint64(1), uint64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, err := Marshal(&MintWire{
		Supply: 1, Mime: "text/plain;charset=utf-8", Data: []byte("x"), Ext: future,
	})
	if err != nil {
		t.Fatal(err)
	}
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload, Ext: future}
	raw, err := z.Encode()
	if err != nil {
		t.Fatal(err)
	}

	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("a ZDU carrying unknown extension keys must still decode: %v", err)
	}
	if !bytes.Equal(got.Ext, future) {
		t.Fatal("envelope ext not preserved through decode")
	}
	mint, err := got.DecodeMint()
	if err != nil {
		t.Fatalf("payload with ext rejected: %v", err)
	}
	if !bytes.Equal(mint.Ext, future) {
		t.Fatal("payload ext not preserved through decode")
	}
	// And it is still recognised by the fast path.
	if !HasSignature(raw) {
		t.Fatal("ext broke the byte signature")
	}
}

// ext is named "ext" and not "x" because canonical CBOR sorts keys bytewise:
// "x" (0x6178) lands between "v" (0x6176) and "op" (0x626f70), splitting the
// fixed-length signature. This pins that reasoning so a future rename cannot
// silently undo it.
func TestExtSortsAfterOpcodeInCanonicalOrder(t *testing.T) {
	probe := bytes.Repeat([]byte{7}, HashLen)
	ext, _ := Marshal(map[string]any{"k": "v"})
	payload, _ := Marshal([]byte("chunk"))

	for _, z := range []*ZDU{
		{Prefix: Prefix, Opcode: OpMulti, Version: Version, Data: payload, Ext: ext},
		{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload, Ext: ext, PHash: probe, THash: probe},
	} {
		raw, err := z.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if !HasSignature(raw) {
			t.Fatalf("signature broken with ext present: %x", raw)
		}
		// The signature region must be byte-identical with and without ext.
		bare := *z
		bare.Ext = nil
		bareRaw, err := bare.Encode()
		if err != nil {
			t.Fatal(err)
		}
		n := sigOffset + len(sigPrefixPart) + 1 + len(sigOpKey) + 1
		if !bytes.Equal(raw[1:n], bareRaw[1:n]) {
			t.Fatalf("ext shifted the signature region:\n with %x\n without %x", raw[1:n], bareRaw[1:n])
		}
	}
}

func TestExtMustBeABoundedMap(t *testing.T) {
	notAMap, _ := Marshal("just a string")
	tooBig, _ := Marshal(map[string]any{"blob": make([]byte, MaxExtLen+64)})

	for name, ext := range map[string][]byte{
		"not a map":           notAMap,
		"over the size bound": tooBig,
	} {
		payload, _ := Marshal(&MintWire{
			Supply: 1, Mime: "text/plain;charset=utf-8", Data: []byte("x"), Ext: ext,
		})
		z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
		raw, err := z.Encode()
		if err != nil {
			t.Fatal(err)
		}
		zdu, err := Decode(raw)
		if err != nil {
			continue // rejected at the envelope, which is also fine
		}
		if _, err := zdu.DecodeMint(); err == nil {
			t.Errorf("%s: ext accepted, should be rejected", name)
		}
	}
}
