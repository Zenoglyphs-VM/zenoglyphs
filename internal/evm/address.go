package evm

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Addresses, and why this EVM has no address type of its own.
//
// A NoM address is 20 bytes. An EVM address is 20 bytes. This EVM makes the
// mapping between them the identity, so **an inscriber's EVM account is their
// NoM account** — the one their syrius extension already holds the key for.
// There is no registration step, no second keypair, and no way to claim someone
// else's account: the sender is whoever the ledger says signed the block that
// carried the inscription, and AddressOf just reads those bytes.
//
// So `z1q…` and `0x…` are two spellings of one thing, the way an IPv4 address
// is one thing whether written in dotted quad or as an integer. This file is
// the two spellings and the rules for telling them apart:
//
//	z1q…          a NoM user account. A real keypair exists for it, in syrius.
//	z1z…          this metaprotocol's own namespace: contracts, the burn sink,
//	              dispenser escrow. No keypair can exist for one (see below).
//	0x…           the same 20 bytes, for Ethereum-shaped tooling.
//
// Every parse accepts all three, and every render is the NoM one — the hex
// spelling is kept as the canonical key and offered alongside, never instead.
// Whether an address is one a wallet may *pay* is a separate question from how
// it is written, and is answered by Sendable.

// bech32 is implemented here rather than imported from go-zenon's types package
// for the reason stated in chain.go's decodeBech32Address, which this replaces:
// a deriver written against this package must be able to reach every conclusion
// it draws from block bytes alone, with no node and no consensus dependency.
//
// The checksum is verified, which the function this replaces did not do — it
// sliced the last six characters off and trusted the rest. That was safe for an
// issuer address read off the chain, which is where it was used, and is not
// safe for an address a human typed or pasted into a fee-bearing call. bech32's
// whole purpose is catching that typo.
const (
	bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

	// nomHRP is the human-readable part of every NoM address.
	nomHRP = "z"

	// addressDataChars is 20 bytes at 5 bits per character, plus the six
	// characters of checksum the encoder appends.
	addressDataChars = 32
	addressChecksum  = 6
)

// Address type bytes. The first byte of a NoM address is a namespace, and which
// values are reachable is a structural fact rather than a convention:
//
//	0x00  user      types.PubKeyToAddress hardcodes it, so every keypair lands here
//	0x01  embedded  NoM's own contracts; a send here goes to the contract sequencer
//	0x16  ours      no keypair and no NoM contract can reach it
//
// 0x16 is not a number this project picked out of the air: it is what the
// characters "ze" encode to, which is why the namespace reads z1zen0glyphx.
// A-26 verified on devnet that a send to such an address is accepted, sits
// unreceived forever, and is logged by no node — which is what makes this
// protocol's burn sink provably unspendable rather than probably so.
const (
	UserAddrByte     = 0x00
	EmbeddedAddrByte = 0x01
	ProtocolAddrByte = 0x16
)

var (
	errBadBech32   = errors.New("evm: not a valid NoM address")
	errBadChecksum = errors.New("evm: address checksum does not match — check for a typo")
)

// Hex is the Ethereum spelling: 0x followed by 40 lowercase hex characters.
func (a Address) Hex() string { return "0x" + hex.EncodeToString(a[:]) }

// Zenon is the NoM spelling: bech32 with the "z" prefix, exactly what syrius,
// znn-cli and every explorer show.
//
// Every 20 bytes encode to a valid address, so this never fails — including for
// a contract, whose bytes come out of a hash. Whether that is an address anyone
// should *send* to is a different question, answered by Sendable.
func (a Address) Zenon() string {
	data := make([]byte, 0, addressDataChars)
	var acc, bits uint16
	for _, b := range a {
		acc = acc<<8 | uint16(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			data = append(data, byte(acc>>bits)&0x1f)
		}
	}
	if bits > 0 {
		data = append(data, byte(acc<<(5-bits))&0x1f)
	}

	full := append(data, bech32Checksum(nomHRP, data)...)
	var sb strings.Builder
	sb.WriteString(nomHRP)
	sb.WriteByte('1')
	for _, v := range full {
		sb.WriteByte(bech32Charset[v])
	}
	return sb.String()
}

// Kind names the namespace an address sits in.
func (a Address) Kind() string {
	switch a[0] {
	case UserAddrByte:
		return "user"
	case EmbeddedAddrByte:
		return "embedded"
	case ProtocolAddrByte:
		return "protocol"
	default:
		return "unreachable"
	}
}

// Sendable reports whether a NoM account block may usefully be sent to this
// address — that is, whether a wallet paying it would be paying somebody.
//
// Only user addresses are. This is the guard rail that makes rendering
// contracts in bech32 safe: a contract address is a valid-looking `z1z…`
// string, and someone will eventually paste one into syrius. The answer must
// not be "the funds are gone"; it is "this is not an address you can pay", and
// this is the flag that lets a UI say so — the API publishes it as `sendable`
// beside every address, and the contract page and the ABI decoder both render
// it.
func (a Address) Sendable() bool { return a[0] == UserAddrByte }

// Display is the spelling to show a human: the NoM one, always.
//
// An earlier version printed contracts as hex, on the reasoning that a bech32
// contract address is one somebody might pay from a wallet. That was the wrong
// trade twice over.
//
// It was inconsistent: this protocol already renders its own `0x16` addresses
// natively and deliberately — the burn sink is published as
// `z1zen0glyphxdurn…` and dispenser escrow as `z1zen0glyphxescr0w…`, both
// equally unspendable. Contracts sit in the same namespace, so hiding only
// their bech32 spelling made one third of a listing unreadable to the audience
// the spelling exists for.
//
// And it solved the wrong problem. The danger is not that a contract *has* a
// bech32 name; it is that a reader might mistake one for a wallet. A contract
// reads `z1z…` and a wallet reads `z1q…`, which separates the two on sight —
// and `Sendable` is the authority for anything acting on it, because the
// prefix is only a hint: bech32's first character covers a range of type bytes,
// so `z1q…` spans 0x00–0x07 while only 0x00 is a real account. Withholding the
// name communicates the same fact far more weakly, and only to people who
// already knew.
func (a Address) Display() string { return a.Zenon() }

// ParseAddress accepts either spelling, and is what every boundary should use:
// CLI flags, query parameters, path segments, the `to` field of an inscription.
//
// Taking both is not a convenience. A user who has only ever seen their address
// in syrius knows it as z1q…, and a user coming from Ethereum tooling knows a
// contract as 0x…; a surface that accepts one and not the other is unusable to
// half its audience for no reason, since the bytes are the same either way.
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, nomHRP+"1") {
		return AddressFromZenon(s)
	}
	return AddressFromHex(s)
}

// AddressFromZenon decodes a bech32 NoM address, checksum and all.
func AddressFromZenon(s string) (Address, error) {
	var a Address
	// Mixed case is invalid in bech32 — the checksum is defined over one case
	// — but NoM addresses are lowercase in practice, so accept an uppercase
	// paste by folding rather than rejecting it.
	if strings.ToUpper(s) != s && strings.ToLower(s) != s {
		return a, errBadBech32
	}
	s = strings.ToLower(s)

	if !strings.HasPrefix(s, nomHRP+"1") {
		return a, errBadBech32
	}
	body := s[len(nomHRP)+1:]
	if len(body) != addressDataChars+addressChecksum {
		return a, fmt.Errorf("%w: %d characters after the prefix, want %d",
			errBadBech32, len(body), addressDataChars+addressChecksum)
	}

	values := make([]byte, len(body))
	for i := 0; i < len(body); i++ {
		v := strings.IndexByte(bech32Charset, body[i])
		if v < 0 {
			return a, fmt.Errorf("%w: %q is not a bech32 character (b, i, o and 1 never are)",
				errBadBech32, body[i])
		}
		values[i] = byte(v)
	}
	if !bech32Verify(nomHRP, values) {
		return a, errBadChecksum
	}

	// 32 five-bit groups is 160 bits exactly, so there is no partial trailing
	// group to check for non-zero padding the way a general bech32 decoder
	// would need to.
	var acc, bits uint16
	out := a[:0]
	for _, v := range values[:addressDataChars] {
		acc = acc<<5 | uint16(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return a, nil
}

// --- bech32 checksum (BIP-173) ---

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

// hrpExpand is the checksum's encoding of the human-readable part: the high
// bits of every character, a separator, then the low bits.
func hrpExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&0x1f)
	}
	return out
}

func bech32Checksum(hrp string, data []byte) []byte {
	values := append(hrpExpand(hrp), data...)
	values = append(values, 0, 0, 0, 0, 0, 0)
	mod := bech32Polymod(values) ^ 1
	out := make([]byte, 6)
	for i := 0; i < 6; i++ {
		out[i] = byte(mod>>uint(5*(5-i))) & 0x1f
	}
	return out
}

func bech32Verify(hrp string, values []byte) bool {
	return bech32Polymod(append(hrpExpand(hrp), values...)) == 1
}
