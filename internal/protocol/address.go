package protocol

import (
	"fmt"

	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
)

// Protocol-owned pseudo-addresses, built on the z1zen0glyphx namespace declared
// in protocol.go.
//
// Everything in this file is a DISPLAY convention and nothing in it is
// consensus-relevant — the one exception to this package's usual rule, stated
// here because the rest of the package is the opposite. No ledger key is
// derived from these functions, no digest hashes over them, and two indexers
// that disagree about them still agree about history.
//
// That distinction is the whole reason the escrow address is synthesised at
// the serving edge instead of replacing the internal key. The indexer parks a
// dispenser's stock under the dispenser's own id (internal/indexer/apply.go's
// escrowHolder), and that id is what store.Move and Store.ProjectionDigest
// hash over. Changing it would change the digest, so every indexer on the
// network would have to upgrade in the same momentum or `zcribe compare`
// would report a divergence that is not one. Formatting it on the way out
// costs nothing and needs no coordination — the same reasoning that keeps
// moderation out of the state root.
//
// If the key itself is ever changed, this is the function that says what to
// change it to, and the change is a coordinated one.

// bech32Alphabet is the bech32 charset in value order: the character at index
// i encodes the 5-bit value i. Note the three letters it does not contain —
// b, i, o and 1 — which is why a function name here is spelled "escr0w" and the
// burn sink is spelled "durn".
const bech32Alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// addressDataChars is how many bech32 characters carry an address: 20 bytes
// at 5 bits per character. The checksum is six more and is added by the
// encoder, never written by hand.
const addressDataChars = 32

// EscrowFunction is the function name a dispenser escrow address spells,
// immediately after the namespace. Zero for the o, as in "durn" for burn.
const EscrowFunction = "escr0w"

// EscrowPrefix is what every dispenser escrow address begins with. A client
// that wants to know whether an address is escrow without asking an indexer
// tests this prefix; a client that wants to know WHICH dispenser has to ask,
// because the id does not fit (see EscrowAddress).
const EscrowPrefix = AddressNamespace + EscrowFunction

// EscrowAddress is the address a dispenser's escrowed stock is displayed
// under: z1zen0glyphxescr0w… , one per dispenser.
//
// It is one-way. An address carries 32 data characters, the namespace and
// function name take 16 of them, and 16 characters is 80 bits — not the 256
// a dispenser id needs. So the address identifies the escrow to a reader and
// tells the reader's software nothing; every API response that renders one
// also carries the dispenser id beside it, which is the half a link needs.
//
// A digest rather than a truncation of the id, because a truncated id looks
// like an id and invites someone to paste the visible half into a lookup that
// will not find it. 80 bits of sha3-256 is far past collision concern for a
// label that is never a key.
func EscrowAddress(dispenserID string) string {
	addr, err := namespaceAddress(EscrowFunction, []byte("zenoglyphs/escrow/"+dispenserID))
	if err != nil {
		// Unreachable: the function name is a constant that fits, and the
		// seed is always hashed to a fixed width. A panic here would be a
		// programming error surfacing at render time, so return something
		// visibly wrong instead of taking a request down.
		return dispenserID
	}
	return addr.String()
}

// IsEscrowAddress reports whether a string is one of the addresses above.
func IsEscrowAddress(s string) bool {
	return len(s) == len("z1")+addressDataChars+6 && hasPrefix(s, EscrowPrefix)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// namespaceAddress builds a z1zen0glyphx address whose data characters read
// "zen0glyphx" + function, with the remainder filled from a digest of seed.
//
// The address is assembled as bech32 characters and then decoded back to the
// 20 bytes an Address holds, rather than the other way round, because the
// readable half is the part that has to come out exactly right: five bits per
// character do not land on byte boundaries, so choosing bytes to spell a word
// means solving for them. Choosing the characters and converting is the same
// operation run in the direction that has an answer.
//
// The first byte lands at 0x16, which is neither the user type byte (0x00)
// nor the contract one (0x01) — see AddressNamespace in protocol.go for why
// that is the point rather than a defect. "zen0glyphx" begins z (00010) e
// (11001), so the leading eight bits are 00010110 whatever follows.
//
// A send to such an address is accepted and left unreceived, the same path a
// send to a user address takes; only byte0 == 1 is treated specially, and only
// to hand the block to the contract sequencer.
func namespaceAddress(function string, seed []byte) (types.Address, error) {
	data := AddressNamespace[len("z1"):] + function
	if len(data) > addressDataChars {
		return types.ZeroAddress, fmt.Errorf(
			"function %q leaves no room: %d characters, %d available",
			function, len(data), addressDataChars-len(AddressNamespace[len("z1"):]))
	}
	for _, c := range data {
		if indexOfByte(bech32Alphabet, byte(c)) < 0 {
			return types.ZeroAddress, fmt.Errorf(
				"character %q is not in the bech32 charset (b, i, o and 1 never are)", c)
		}
	}

	// The tail. sha3-256 gives 32 bytes and the tail is at most 27
	// characters, so one hash is always enough; each byte is folded into the
	// 32-character alphabet, and 256 is a multiple of 32, so the fold is
	// uniform rather than biased toward the low characters.
	digest := crypto.Hash(seed)
	tail := make([]byte, addressDataChars-len(data))
	for i := range tail {
		tail[i] = bech32Alphabet[digest[i]%32]
	}

	chars := data + string(tail)
	raw := make([]byte, 0, types.AddressSize)
	var acc, bits uint16
	for i := 0; i < len(chars); i++ {
		v := indexOfByte(bech32Alphabet, chars[i])
		acc = acc<<5 | uint16(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			raw = append(raw, byte(acc>>bits))
		}
	}
	return types.BytesToAddress(raw)
}

func indexOfByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
