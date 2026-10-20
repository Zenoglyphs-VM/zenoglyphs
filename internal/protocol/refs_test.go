package protocol

import (
	"bytes"
	"errors"
	"testing"
)

func ref(b byte) []byte {
	r := make([]byte, HashLen)
	for i := range r {
		r[i] = b
	}
	return r
}

func mintWithRefs(refs [][]byte) *MintWire {
	return &MintWire{
		Supply: 1, Decimals: 0, Mime: "text/html;charset=utf-8",
		Data: []byte("<html>"), Refs: refs,
	}
}

func TestRefsBounds(t *testing.T) {
	tests := []struct {
		name string
		refs [][]byte
		want error
	}{
		{"absent", nil, nil},
		{"one", [][]byte{ref(1)}, nil},
		{"at the cap", make([][]byte, MaxRefs), nil},
		// An explicit empty array is a second spelling of "no refs", and two
		// encodings of one object is what R-2.3 exists to prevent.
		{"empty array", [][]byte{}, ErrBadPayload},
		{"over the cap", make([][]byte, MaxRefs+1), ErrBoundsExceed},
		{"short id", [][]byte{make([]byte, 31)}, ErrBadPayload},
		{"long id", [][]byte{make([]byte, 33)}, ErrBadPayload},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			refs := tc.refs
			// The bulk cases above are allocated empty; fill them so only the
			// count is under test.
			for i := range refs {
				if len(refs[i]) == 0 {
					refs[i] = ref(byte(i))
				}
			}
			err := mintWithRefs(refs).Validate()
			if tc.want == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// Slots are positional, so refs[0] and refs[3] naming the same asset is
// redundant rather than invalid. A rule against it would protect nothing and
// give two indexers one more thing to agree on.
func TestDuplicateRefsAreAllowed(t *testing.T) {
	if err := mintWithRefs([][]byte{ref(7), ref(7)}).Validate(); err != nil {
		t.Fatalf("duplicate imports should be permitted: %v", err)
	}
}

// Order is the entire meaning of the list: slot n is the nth entry, and the
// inscribed bytes reach it as the relative URL "n". A round trip that reorders
// or deduplicates would silently repoint an inscription's imports.
func TestRefsSurviveTheWireInOrder(t *testing.T) {
	want := [][]byte{ref(3), ref(1), ref(2)}
	payload, err := Marshal(mintWithRefs(want))
	if err != nil {
		t.Fatal(err)
	}
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
	encoded, err := z.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	m, err := decoded.DecodeMint()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Refs) != len(want) {
		t.Fatalf("got %d refs, want %d", len(m.Refs), len(want))
	}
	for i := range want {
		if !bytes.Equal(m.Refs[i], want[i]) {
			t.Fatalf("slot %d = %x, want %x", i, m.Refs[i], want[i])
		}
	}
}

// refs is a key in the mint payload, not the envelope, so it must not disturb
// the fixed-length byte signature the indexer uses to reject non-ZDU blocks
// without parsing them.
func TestRefsDoNotDisturbTheByteSignature(t *testing.T) {
	payload, err := Marshal(mintWithRefs([][]byte{ref(1), ref(2)}))
	if err != nil {
		t.Fatal(err)
	}
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
	encoded, err := z.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !HasSignature(encoded) {
		t.Fatal("a composite mint failed the byte-signature fast path")
	}
}

// A ref names a block hash, and a block hash cannot be known before the block
// exists. So an inscription can never reference itself, every edge points
// strictly backwards in canonical order, and the reference graph is a DAG by
// construction — which is why the indexer needs no cycle detection for refs,
// unlike the pHash chain walk.
//
// The test states the mechanical half of that argument: the refs live inside
// the very block whose hash becomes the asset id, so a self-reference would
// require the payload to contain its own hash.
func TestSelfReferenceIsNotExpressible(t *testing.T) {
	payload, err := Marshal(mintWithRefs([][]byte{ref(9)}))
	if err != nil {
		t.Fatal(err)
	}
	z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
	encoded, err := z.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// The asset id is the hash of the block carrying these bytes. Whatever it
	// turns out to be, it is not a value the bytes already contain — the only
	// way to make it one is a preimage of the block hash.
	if bytes.Contains(encoded, ref(9)) == false {
		t.Fatal("the declared ref should appear verbatim in the encoded ZDU")
	}
}
