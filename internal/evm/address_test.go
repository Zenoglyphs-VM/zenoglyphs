package evm

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// The local bech32 codec must agree with go-zenon's, byte for byte, on every
// address — otherwise this deriver and the chain disagree about who someone is.
//
// go-zenon is imported *here and only here*: the package itself must reach
// every conclusion from block bytes alone with no node and no consensus
// dependency, which is why address.go reimplements bech32. A test is the right
// place to prove the reimplementation is faithful, because the test is allowed
// to depend on the thing being matched.
func TestZenonSpellingMatchesGoZenon(t *testing.T) {
	var buf [20]byte
	for i := 0; i < 2000; i++ {
		if _, err := rand.Read(buf[:]); err != nil {
			t.Fatal(err)
		}
		// Cover every namespace deliberately, not just whatever random bytes
		// happen to produce: user (0x00) is the common case and 0x16 is ours,
		// and both are rare in 2000 uniform draws.
		switch i % 4 {
		case 0:
			buf[0] = UserAddrByte
		case 1:
			buf[0] = EmbeddedAddrByte
		case 2:
			buf[0] = ProtocolAddrByte
		}

		want, err := types.BytesToAddress(buf[:])
		if err != nil {
			t.Fatal(err)
		}
		var a Address
		copy(a[:], buf[:])

		if got := a.Zenon(); got != want.String() {
			t.Fatalf("encoding %x: got %s, go-zenon says %s", buf, got, want)
		}
		// And back again.
		back, err := AddressFromZenon(want.String())
		if err != nil {
			t.Fatalf("decoding %s: %v", want, err)
		}
		if back != a {
			t.Fatalf("round trip of %x produced %x", buf, back)
		}
	}
}

// Known addresses from go-zenon's own source, so a change to the codec fails
// against fixed strings and not only against another implementation of itself.
func TestKnownAddresses(t *testing.T) {
	for _, s := range []string{
		"z1qxemdeddedxpyllarxxxxxxxxxxxxxxxsy3fmg", // NoM's pillar contract
		"z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp",
		"z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d", // the devnet issuer
		"z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9", // this protocol's burn sink
	} {
		a, err := AddressFromZenon(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got := a.Zenon(); got != s {
			t.Fatalf("%s round-tripped to %s", s, got)
		}
	}
}

// The checksum has to be checked, not skipped. The decoder this replaced sliced
// the last six characters off and trusted the rest, so a mistyped address
// decoded silently to different bytes — and for an address naming the recipient
// of a token transfer, that is a loss of funds rather than an error message.
func TestTyposAreRejected(t *testing.T) {
	const good = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
	if _, err := AddressFromZenon(good); err != nil {
		t.Fatalf("the control address did not decode: %v", err)
	}

	// Every single-character substitution in the data part must be caught.
	caught := 0
	for i := 2; i < len(good)-6; i++ {
		for _, c := range bech32Charset {
			if byte(c) == good[i] {
				continue
			}
			bad := good[:i] + string(c) + good[i+1:]
			if _, err := AddressFromZenon(bad); err == nil {
				t.Fatalf("a one-character typo decoded cleanly: %s", bad)
			}
			caught++
		}
	}
	if caught == 0 {
		t.Fatal("no substitutions were tested")
	}

	for _, bad := range []string{
		"", "z1", "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5", // one short
		"z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5dd", // one long
		"z1bq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d",  // 'b' is not bech32
		"Z1qq6eg8n43g032hanpsfp02qcdmv7zfJ3y2lt5d",  // mixed case
	} {
		if _, err := AddressFromZenon(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
	// Uppercase throughout is a valid bech32 spelling and must fold, not fail.
	if a, err := AddressFromZenon(strings.ToUpper(good)); err != nil || a.Zenon() != good {
		t.Fatalf("an uppercase paste was rejected: %v", err)
	}
}

// Both spellings must reach the same 20 bytes, everywhere a boundary parses one.
func TestParseAddressAcceptsEitherSpelling(t *testing.T) {
	const nom = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
	a, err := ParseAddress(nom)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseAddress(a.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("%s and %s are not the same address", nom, a.Hex())
	}
	// Surrounding whitespace is a paste artefact, not an error.
	if c, err := ParseAddress("  " + nom + "\n"); err != nil || c != a {
		t.Fatalf("a padded paste was rejected: %v", err)
	}
	// And nonsense still fails rather than producing a zero address.
	for _, bad := range []string{"", "0x", "0xdeadbeef", "not-an-address", "z1nope"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Fatalf("%q parsed as an address", bad)
		}
	}
}

// A user's EVM account IS their syrius account. This is the property the whole
// design rests on, so it gets a test that states it in those terms.
func TestASyriusAccountIsAnEvmAccount(t *testing.T) {
	const syrius = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
	a, err := AddressOf(syrius)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind() != "user" {
		t.Fatalf("a wallet address landed in the %q namespace", a.Kind())
	}
	if !a.Sendable() {
		t.Fatal("a wallet address is not payable")
	}
	// It renders back as the string in the wallet, unchanged.
	if a.Display() != syrius {
		t.Fatalf("displayed as %s, want %s", a.Display(), syrius)
	}
}

// Contract addresses must be structurally unreachable by any keypair, and must
// never land in NoM's embedded-contract space. Byte 0 is what guarantees it.
func TestContractAddressesCannotCollideWithAccounts(t *testing.T) {
	var id [32]byte
	for i := 0; i < 5000; i++ {
		if _, err := rand.Read(id[:]); err != nil {
			t.Fatal(err)
		}
		a := ContractAddress(id[:])
		if a[0] != ProtocolAddrByte {
			t.Fatalf("contract address %s has type byte %#x", a.Hex(), a[0])
		}
		if a.Sendable() {
			t.Fatalf("contract address %s is payable", a.Hex())
		}
		if a.Kind() != "protocol" {
			t.Fatalf("contract address %s is in the %q namespace", a.Hex(), a.Kind())
		}
		// It still has to be a well-formed NoM address, since it will be
		// rendered as one — and it must read as ours at a glance.
		z := a.Zenon()
		if !strings.HasPrefix(z, "z1z") {
			t.Fatalf("contract address rendered as %s, want a z1z… address", z)
		}
		if back, err := AddressFromZenon(z); err != nil || back != a {
			t.Fatalf("contract address %s did not round-trip: %v", z, err)
		}
	}
}

// The derivation must still be a commitment to the artifact: same asset id,
// same address, always; different id, different address.
func TestContractAddressCommitsToTheArtifact(t *testing.T) {
	a := ContractAddress([]byte("asset-one"))
	if a != ContractAddress([]byte("asset-one")) {
		t.Fatal("the same asset id produced two addresses")
	}
	if a == ContractAddress([]byte("asset-two")) {
		t.Fatal("two asset ids produced one address")
	}
}

// Every address displays natively, contracts included — the same treatment
// this protocol already gives its burn sink and its dispenser escrow, which are
// exactly as unspendable.
//
// What keeps that safe is not withholding the name but the namespace: a
// contract reads z1z… and a wallet reads z1q…, so the two separate on sight,
// and Sendable is the authority for anything that has to act on it — the
// prefix is a hint, since one bech32 character spans eight type bytes.
func TestEveryAddressDisplaysNatively(t *testing.T) {
	contract := ContractAddress([]byte("asset-one"))
	if !strings.HasPrefix(contract.Display(), "z1z") {
		t.Fatalf("a contract displayed as %s, want a z1z… address", contract.Display())
	}
	if contract.Sendable() {
		t.Fatal("a contract is marked payable")
	}

	account, err := AddressOf("z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(account.Display(), "z1q") || !account.Sendable() {
		t.Fatalf("an account displayed as %s (sendable=%v)", account.Display(), account.Sendable())
	}

	// The two must be distinguishable at a glance by their third character,
	// which is the whole reason the type byte is forced.
	if contract.Display()[:3] == account.Display()[:3] {
		t.Fatal("a contract and an account are indistinguishable by prefix")
	}

	// And a displayed address must parse back, or a reader cannot paste what
	// the explorer showed them.
	for _, a := range []Address{contract, account} {
		if got, err := ParseAddress(a.Display()); err != nil || got != a {
			t.Fatalf("%s did not parse back: %v", a.Display(), err)
		}
	}
}
