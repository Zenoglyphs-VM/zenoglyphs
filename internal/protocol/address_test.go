package protocol

import (
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// The protocol address namespace.
//
// Every address this protocol owns begins z1zen0glyphx and then spells what it
// is, padded with x — adapting the convention NoM uses for its own embedded
// contracts (z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp), but deliberately
// outside both address types NoM can mint (A-26).
//
// These tests pin the facts that make such an address usable as a sink, all of
// which were learned by trying it on a devnet rather than by reading: it must
// not be a contract address, it must round-trip through the node's own parser,
// and — the property the namespace exists for — no keypair may be able to
// reach it.

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// dataChars is the bech32 payload of an address: everything between the "z1"
// human-readable prefix and the six-character checksum.
func dataChars(addr string) string {
	body := strings.TrimPrefix(addr, "z1")
	return body[:len(body)-6]
}

func TestBurnAddressIsUsable(t *testing.T) {
	s := BurnAddress.String()

	// Byte 0 == 1 is the one value the ledger treats specially: such a send is
	// handed to the contract sequencer, which fails with "contract doesn't
	// exist" when no contract is registered there. The address the design
	// notes originally proposed had exactly that problem — unspendable, but
	// also unsendable, which makes it useless as a burn sink.
	//
	// Every other value, including this namespace's 0x16, takes the ordinary
	// path: marked unreceived and left there. So the requirement is not "is a
	// user address" but "is not a contract address".
	if types.IsEmbeddedAddress(BurnAddress) {
		t.Fatalf("%s is a contract address; the node will refuse every send to it", s)
	}

	round, err := types.ParseAddress(s)
	if err != nil || round != BurnAddress {
		t.Fatalf("round trip through the node's parser failed: %v", err)
	}
}

func TestBurnAddressIsInTheNamespace(t *testing.T) {
	s := BurnAddress.String()
	if !strings.HasPrefix(s, AddressNamespace) {
		t.Fatalf("%s is outside the %s… namespace", s, AddressNamespace)
	}
	// Readability is the whole reason for the namespace, so the function name
	// has to survive next to it. "durn" rather than "burn" because the bech32
	// charset excludes b, i, o and 1.
	if !strings.HasPrefix(s, AddressNamespace+"durn") {
		t.Fatalf("%s does not name its function after the namespace", s)
	}
	// Everything after the function is padding, up to the checksum.
	tail := strings.TrimPrefix(dataChars(s), AddressNamespace[len("z1"):]+"durn")
	if strings.Trim(tail, "x") != "" {
		t.Fatalf("padding after the function name is %q, want only x", tail)
	}
}

// The namespace is unreachable by any keypair, which is the property it exists
// for and the one that separates it from its predecessors.
//
// A NoM address's first byte is a type byte, and types.PubKeyToAddress
// hardcodes it to 0 (user); the ledger only ever mints 0 or 1 (contract). Both
// values leave the top five bits zero, so every address NoM can produce begins
// "z1q". This namespace begins "z1ze" — byte 0 is 0x16 — so no key that exists
// or could be generated lands inside it, at any grind cost.
//
// The earlier z1zen0glyphx and z1qzen0glyphx namespaces both sat in user space,
// where the prefix was merely expensive to forge (about 2^25 and 2^55 hashes).
// This test is what stops a future edit from quietly moving back there.
func TestNamespaceIsUnreachableByAnyKeypair(t *testing.T) {
	claimed := AddressNamespace[len("z1"):] // "zen0glyphx"

	for _, c := range claimed {
		if !strings.ContainsRune(bech32Charset, c) {
			t.Fatalf("namespace character %q is not in the bech32 charset", c)
		}
	}
	total := len(dataChars(BurnAddress.String()))
	if total != 32 {
		t.Fatalf("an address carries %d data characters, want 32", total)
	}
	if free := total - len(claimed); free != 22 {
		t.Fatalf("%d characters left for the function name, want 22", free)
	}

	// The load-bearing assertion. Enumerating every second byte covers every
	// address either type byte can reach, because byte 0 and the top bits of
	// byte 1 are all that the first two characters encode.
	for _, typeByte := range []byte{types.UserAddrByte, types.ContractAddrByte} {
		for b1 := 0; b1 < 256; b1++ {
			raw := make([]byte, types.AddressSize)
			raw[0], raw[1] = typeByte, byte(b1)
			a, err := types.BytesToAddress(raw)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(a.String(), AddressNamespace[:len("z1")+2]) {
				t.Fatalf("type byte 0x%02x can reach %s…; the namespace is forgeable",
					typeByte, AddressNamespace[:len("z1")+2])
			}
		}
	}

	// And the burn address really does carry that unreachable type byte.
	if BurnAddress[0] == types.UserAddrByte || BurnAddress[0] == types.ContractAddrByte {
		t.Fatalf("burn address type byte is 0x%02x, which a keypair can reach", BurnAddress[0])
	}
}
