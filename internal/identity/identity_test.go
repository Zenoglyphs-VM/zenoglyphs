package identity

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/wallet"
)

func testKey(t *testing.T, seed byte) *wallet.KeyPair {
	t.Helper()
	raw := make([]byte, ed25519.SeedSize)
	for i := range raw {
		raw[i] = seed
	}
	priv := ed25519.NewKeyFromSeed(raw)
	pub, _ := priv.Public().(ed25519.PublicKey)
	return &wallet.KeyPair{Public: pub, Private: priv, Address: types.PubKeyToAddress(pub)}
}

func TestVerifyAcceptsAGenuineProof(t *testing.T) {
	key := testKey(t, 1)
	addr := key.Address.String()
	msg := Message(4242, addr, "0f0f", time.Unix(1800000000, 0))

	pub, sig := Sign(key, msg)
	if err := Verify(msg, addr, pub, sig); err != nil {
		t.Fatalf("a genuine proof was rejected: %v", err)
	}
	// The wallet people actually have signs raw message bytes with no
	// envelope (syrius's znn_sign). Verify must accept exactly that.
	direct := hex.EncodeToString(ed25519.Sign(key.Private, []byte(msg)))
	if err := Verify(msg, addr, pub, direct); err != nil {
		t.Fatalf("a raw ed25519 signature over the message was rejected: %v", err)
	}
}

// The half that ties a signature to an address. Without it, anyone with any
// key could prove control of anyone's address.
func TestVerifyRejectsAKeyThatIsNotTheAddress(t *testing.T) {
	mine, theirs := testKey(t, 1), testKey(t, 2)
	msg := Message(4242, theirs.Address.String(), "0f0f", time.Unix(1800000000, 0))

	pub, sig := Sign(mine, msg)
	err := Verify(msg, theirs.Address.String(), pub, sig)
	if err == nil {
		t.Fatal("signing my own key proved control of somebody else's address")
	}
	if !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}
}

// The account id is inside the signed bytes, so a proof copied out of one
// chat cannot be pasted by a different Telegram account.
func TestProofDoesNotTransferBetweenAccounts(t *testing.T) {
	key := testKey(t, 3)
	addr := key.Address.String()
	expires := time.Unix(1800000000, 0)

	pub, sig := Sign(key, Message(1111, addr, "abcd", expires))
	if err := Verify(Message(2222, addr, "abcd", expires), addr, pub, sig); err == nil {
		t.Fatal("a proof made for one Telegram account verified for another")
	}
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	key := testKey(t, 4)
	addr := key.Address.String()
	msg := Message(1, addr, "ab", time.Unix(1800000000, 0))
	pub, sig := Sign(key, msg)

	for name, tc := range map[string]struct{ addr, pub, sig string }{
		"short key":      {addr, "aabb", sig},
		"short sig":      {addr, pub, "aabb"},
		"not hex":        {addr, pub, "zzzz"},
		"not an address": {"nonsense", pub, sig},
	} {
		if err := Verify(msg, tc.addr, tc.pub, tc.sig); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The message layout is a compatibility surface: reformatting it invalidates
// every signature made against the old one. This pins it.
func TestMessageLayoutIsStable(t *testing.T) {
	got := Message(4242, "z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9", "0f0f", time.Unix(1800000000, 0).UTC())
	want := "zenoglyphs: prove address control\n" +
		"address:  z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9\n" +
		"telegram: 4242\n" +
		"nonce:    0f0f\n" +
		"expires:  2027-01-15T08:00:00Z"
	if got != want {
		t.Fatalf("challenge text changed:\n got %q\nwant %q", got, want)
	}
}

func TestParseProofAcceptsWhatPeoplePaste(t *testing.T) {
	for _, in := range []string{"aa.bb", "aa bb", "  aa\tbb  ", "aa,bb"} {
		p, err := ParseProof(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if p.PublicKey != "aa" || p.Signature != "bb" {
			t.Fatalf("%q parsed as %+v", in, p)
		}
	}
	if _, err := ParseProof("only-one-part"); err == nil {
		t.Error("a single field should not parse as a proof")
	}
}
