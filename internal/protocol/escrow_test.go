package protocol

import (
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// A dispenser's escrow address has to satisfy everything the burn address
// does — it is rendered wherever an account is rendered, and a client that
// parses addresses will parse it — plus one thing the burn address does not:
// there is a different one per dispenser.

func TestEscrowAddressIsAUsableAddress(t *testing.T) {
	id := strings.Repeat("9f", 32)
	s := EscrowAddress(id)

	addr, err := types.ParseAddress(s)
	if err != nil {
		t.Fatalf("%s does not parse as an address: %v", s, err)
	}
	if types.IsEmbeddedAddress(addr) {
		t.Fatalf("%s is a contract address", s)
	}
	// Unlike the burn sink, nothing is ever sent to an escrow address — it is
	// a label the API renders. So the only requirements are that it parses and
	// that it carries the namespace's unreachable type byte like every other
	// address here (see TestNamespaceIsUnreachableByAnyKeypair).
	if addr[0] == types.UserAddrByte || addr[0] == types.ContractAddrByte {
		t.Fatalf("%s has first byte 0x%02x, which a keypair can reach", s, addr[0])
	}
	if addr.String() != s {
		t.Fatalf("round trip changed the address: %s -> %s", s, addr.String())
	}
}

func TestEscrowAddressIsInTheNamespace(t *testing.T) {
	s := EscrowAddress(strings.Repeat("ab", 32))
	if !strings.HasPrefix(s, AddressNamespace) {
		t.Fatalf("%s is outside the %s… namespace", s, AddressNamespace)
	}
	if !strings.HasPrefix(s, EscrowPrefix) {
		t.Fatalf("%s does not spell its function: want prefix %s", s, EscrowPrefix)
	}
	if !IsEscrowAddress(s) {
		t.Fatalf("IsEscrowAddress(%s) = false", s)
	}
	// The burn sink is in the same family and must not be mistaken for one.
	if IsEscrowAddress(BurnAddress.String()) {
		t.Error("the burn address reads as an escrow address")
	}
	if IsEscrowAddress(AddressNamespace + "escr0w") {
		t.Error("a truncated string reads as an escrow address")
	}
}

// One address per dispenser, always the same one for the same dispenser.
// Both halves matter: a shared address would merge two dispensers' stock into
// one holder row, and an unstable one would make the same escrow look like a
// different party every time the page was reloaded.
func TestEscrowAddressIsPerDispenserAndStable(t *testing.T) {
	a := EscrowAddress(strings.Repeat("11", 32))
	b := EscrowAddress(strings.Repeat("22", 32))
	if a == b {
		t.Fatalf("two dispensers share the escrow address %s", a)
	}
	if again := EscrowAddress(strings.Repeat("11", 32)); again != a {
		t.Fatalf("not stable: %s then %s", a, again)
	}
}

// The id cannot be recovered from the address — 105 free bits against 256 —
// so nothing may try. This test exists to stop a future "just truncate the
// id" shortcut, which would produce a string that looks like a lookup key
// and is not one.
func TestEscrowAddressDoesNotLeakTheDispenserID(t *testing.T) {
	id := strings.Repeat("9f", 32)
	s := EscrowAddress(id)
	for n := 8; n <= len(id); n += 8 {
		if strings.Contains(s, id[:n]) {
			t.Fatalf("%s embeds the first %d characters of the dispenser id", s, n)
		}
	}
}

func TestNamespaceAddressRejectsUnspellableFunctions(t *testing.T) {
	// b, i, o and 1 are not in the bech32 charset; a function name using one
	// must fail loudly rather than produce a silently different address.
	if _, err := namespaceAddress("escrow", nil); err == nil {
		t.Error("'escrow' contains an o and should not be spellable")
	}
	if _, err := namespaceAddress(strings.Repeat("x", 28), nil); err == nil {
		t.Error("a 28-character function leaves no room and should be rejected")
	}
}
