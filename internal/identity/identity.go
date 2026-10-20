// Package identity is the challenge-response that binds an off-chain account
// — a Telegram user, today — to a Zenon address its owner can prove they
// control.
//
// The problem it solves is narrow and worth stating precisely. A bot can read
// anybody's address; what it cannot do is know that the person typing at it
// is the person holding the key. Everything that follows from on-chain
// identity — crediting a payment to the account that sent it, gating a
// command on what an address holds, addressing a mint to a wallet — needs
// that link, and needs it to be unforgeable by someone who merely knows the
// address.
//
// The shape is the ordinary one: the verifier picks a nonce, the holder signs
// a statement containing it, and the verifier checks the signature against
// the public key AND checks that the key hashes to the address being claimed.
// Both halves are load-bearing. A valid signature over the right message
// proves possession of *a* key; only the address derivation ties it to *the*
// address.
//
// # What gets signed
//
// The raw UTF-8 bytes of the message below, with no length prefix, hash or
// envelope of any kind. That is not a preference — it is what the ecosystem
// already does. Syrius signs WalletConnect's znn_sign by handing the message
// string's code units straight to ed25519 (blocs/wallet_connect/chains/
// nom_service.dart), and the Dart SDK's KeyPair.sign does the same. A scheme
// that prefixed or hashed first would be unsignable by the wallet people
// actually have.
//
// Since nothing separates domains for us, the message does it in plain text:
// the first line names this protocol, and the address, account and expiry are
// all inside the signed bytes. A signature made here therefore cannot be
// lifted into another protocol's verifier, and — more importantly, because
// this is the attack that costs someone money — a signature captured from one
// Telegram account cannot be replayed by another, because the account id is
// part of what was signed.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/wallet"
)

// ChallengeTTL is how long a nonce stays valid. Long enough to open a wallet
// on another machine, short enough that a screenshot of a challenge in a group
// chat is stale by the time anyone acts on it.
const ChallengeTTL = 15 * time.Minute

// NonceBytes is the nonce width. 16 bytes is 128 bits of unpredictability,
// which is the only property a nonce needs here — it is never a key.
const NonceBytes = 16

// NewNonce returns a fresh hex nonce from the system CSPRNG.
func NewNonce() (string, error) {
	b := make([]byte, NonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Message is the exact text a holder signs.
//
// Every field in it is there because leaving it out breaks something:
//
//	the first line   domain separation, in the only place there is room for it
//	address          what is being claimed; the signature is over the claim,
//	                 not over a bare nonce that could be re-presented for a
//	                 different address
//	account          who is claiming it, so one account's proof is not another
//	                 account's proof
//	nonce            freshness
//	expires          an upper bound on how long a leaked signature is worth
//	                 anything, readable by the person signing it
//
// The layout is fixed and pinned by a test. Reformatting it invalidates every
// signature made against the old form, which is a compatibility break even
// though nothing about it is consensus.
func Message(account int64, address, nonce string, expires time.Time) string {
	return strings.Join([]string{
		"zenoglyphs: prove address control",
		"address:  " + address,
		"telegram: " + fmt.Sprint(account),
		"nonce:    " + nonce,
		"expires:  " + expires.UTC().Format(time.RFC3339),
	}, "\n")
}

// Verify checks a signature over the message and that the signing key belongs
// to the claimed address.
//
// pubKey and signature are hex, with or without a 0x prefix, because that is
// what people paste. Everything else about the input is treated as hostile.
func Verify(message, address, pubKeyHex, signatureHex string) error {
	pub, err := decodeHex(pubKeyHex)
	if err != nil {
		return fmt.Errorf("public key is not hex: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	sig, err := decodeHex(signatureHex)
	if err != nil {
		return fmt.Errorf("signature is not hex: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}

	// The address check comes first and is the one that matters. A valid
	// signature by a key that is not this address's key proves nothing about
	// the address, and checking the cheap half first also means a mismatched
	// address never reaches the verifier at all.
	claimed, err := types.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("not an address: %w", err)
	}
	if derived := types.PubKeyToAddress(pub); derived != claimed {
		return fmt.Errorf("that key belongs to %s, not %s", derived, claimed)
	}

	ok, err := wallet.VerifySignature(pub, []byte(message), sig)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("signature does not match the message")
	}
	return nil
}

// Sign produces the proof for a message. It exists so the CLI can hand
// someone a signature to paste, and so the tests sign the way a wallet does
// rather than the way the verifier hopes a wallet does.
func Sign(key *wallet.KeyPair, message string) (pubKeyHex, signatureHex string) {
	return hex.EncodeToString(key.Public), hex.EncodeToString(key.Sign([]byte(message)))
}

// Proof is the two hex strings a holder sends back, joined so there is one
// thing to copy instead of two things to get in the right order.
type Proof struct {
	PublicKey string
	Signature string
}

// String is the paste-able form: pubkey.signature.
func (p Proof) String() string { return p.PublicKey + "." + p.Signature }

// ParseProof accepts either the joined form or the two halves with any
// whitespace between them, in that order.
func ParseProof(fields ...string) (Proof, error) {
	joined := strings.TrimSpace(strings.Join(fields, " "))
	parts := strings.FieldsFunc(joined, func(r rune) bool {
		return r == '.' || r == ' ' || r == '\n' || r == '\t' || r == ','
	})
	if len(parts) != 2 {
		return Proof{}, fmt.Errorf("expected <public key>.<signature>, got %d parts", len(parts))
	}
	return Proof{PublicKey: parts[0], Signature: parts[1]}, nil
}

func decodeHex(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	return hex.DecodeString(s)
}
