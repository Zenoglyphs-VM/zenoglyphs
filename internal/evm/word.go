// Package evm is a small EVM, and a deriver that replays one out of
// Zenoglyphs inscriptions.
//
// The point is not to reimplement Ethereum. It is that the EVM is the one
// widely-understood execution model whose semantics are *already* what a
// multi-indexer metaprotocol needs: 256-bit integers and no floating point,
// so no NaN-payload divergence (research/on-chain-execution.md §6.3); and gas,
// so replay is bounded rather than trusting every contract author not to loop
// forever (§6.1). Those are the two properties WebAssembly lacks and would
// have to bolt on.
//
// Nothing here touches Zenoglyphs consensus. Contracts and calls are ordinary
// inscriptions; the operation lives in the envelope's extension lane, which
// R-2.9 defines as ignorable and internal/api never interprets. A Zenoglyphs
// indexer that has never heard of the EVM stays exactly as correct as before.
package evm

import (
	"math/big"
)

// A word is a 256-bit unsigned integer. It is *big.Int masked to 256 bits
// after every operation rather than a fixed [4]uint64, because this is a
// prototype and being obviously correct is worth more here than being fast:
// every arithmetic bug in an EVM is a consensus bug in disguise.
type word = big.Int

var (
	// two256 is 2^256; two255 is the sign bit for signed interpretation.
	two256   = new(big.Int).Lsh(big.NewInt(1), 256)
	two255   = new(big.Int).Lsh(big.NewInt(1), 255)
	mask256  = new(big.Int).Sub(two256, big.NewInt(1))
	wordSize = 32
)

func newWord() *word { return new(big.Int) }

// norm reduces x into [0, 2^256) in place and returns it. Every opcode that
// produces a value ends here; forgetting it is how an EVM silently stops
// agreeing with every other EVM.
func norm(x *big.Int) *big.Int { return x.And(x, mask256) }

// signed reinterprets a 256-bit word as two's complement.
func signed(x *big.Int) *big.Int {
	if x.Cmp(two255) < 0 {
		return new(big.Int).Set(x)
	}
	return new(big.Int).Sub(x, two256)
}

// fromSigned folds a signed value back into 256-bit two's complement.
func fromSigned(x *big.Int) *big.Int { return norm(new(big.Int).Set(x)) }

// bytes32 renders a word as the fixed 32-byte big-endian encoding the EVM
// uses everywhere: storage values, log topics, return data, hash preimages.
func bytes32(x *big.Int) []byte {
	b := make([]byte, 32)
	x.FillBytes(b)
	return b
}

// wordFromBytes reads up to 32 bytes big-endian, zero-padded on the right if
// the slice runs out. Short reads padding with zero — rather than failing — is
// the EVM's actual behaviour for CALLDATALOAD past the end of calldata.
func wordFromBytes(b []byte) *big.Int {
	buf := make([]byte, 32)
	copy(buf, b)
	return new(big.Int).SetBytes(buf)
}

// getData reads len bytes from data at offset, zero-filling anything past the
// end. Used by CALLDATACOPY, CODECOPY and RETURNDATACOPY, all of which pad
// rather than revert.
func getData(data []byte, offset, size *big.Int) []byte {
	out := make([]byte, toInt(size))
	if !offset.IsUint64() {
		return out
	}
	off := int(offset.Uint64())
	if off >= len(data) {
		return out
	}
	copy(out, data[off:])
	return out
}

// toInt narrows a word to an int for use as a length or offset. Anything that
// cannot be a sane in-memory size is clamped to a value that will fail the
// caller's own gas or bounds check rather than panicking here.
func toInt(x *big.Int) int {
	if !x.IsInt64() {
		return 1 << 31
	}
	v := x.Int64()
	if v < 0 || v > 1<<31 {
		return 1 << 31
	}
	return int(v)
}
