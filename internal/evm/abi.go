package evm

import (
	"fmt"
	"math/big"
	"strings"
)

// Just enough ABI to build and read the calls this prototype makes.
//
// Only the static 32-byte types: address, uintN, intN, bool, bytes32. That
// covers the whole ERC-20 surface, which is the point — dynamic types (string,
// bytes, arrays) need offset/length encoding that nothing here uses, and a
// half-implemented version of it would be a trap rather than a convenience.

// Selector is the first four bytes of keccak256 over the canonical signature,
// e.g. Selector("transfer(address,uint256)") == a9059cbb.
func Selector(sig string) []byte { return keccak([]byte(sig))[:4] }

// Topic is the full 32-byte event signature hash.
func Topic(sig string) []byte { return keccak([]byte(sig)) }

// EncodeCall builds calldata: the selector followed by each argument padded
// into a 32-byte word.
func EncodeCall(sig string, args ...any) ([]byte, error) {
	out := append([]byte(nil), Selector(sig)...)
	for i, a := range args {
		w, err := encodeArg(a)
		if err != nil {
			return nil, fmt.Errorf("argument %d of %s: %w", i, sig, err)
		}
		out = append(out, w...)
	}
	return out, nil
}

func encodeArg(a any) ([]byte, error) {
	switch v := a.(type) {
	case Address:
		return bytes32(new(big.Int).SetBytes(v[:])), nil
	case string:
		// An address in either spelling. This is what makes
		// `transfer(z1q…, 100)` work without the caller converting first —
		// and the reason it is a string case rather than a helper the caller
		// must remember to reach for is that forgetting the helper would
		// encode a plausible-looking word from a bech32 string's raw
		// characters and send the tokens nowhere. There is no other string
		// argument in the static ABI, so this cannot shadow one.
		addr, err := ParseAddress(v)
		if err != nil {
			return nil, err
		}
		return bytes32(new(big.Int).SetBytes(addr[:])), nil
	case *big.Int:
		if v.Sign() < 0 {
			return bytes32(fromSigned(v)), nil
		}
		return bytes32(v), nil
	case uint64:
		return bytes32(new(big.Int).SetUint64(v)), nil
	case int:
		return bytes32(big.NewInt(int64(v))), nil
	case bool:
		n := big.NewInt(0)
		if v {
			n.SetInt64(1)
		}
		return bytes32(n), nil
	default:
		return nil, fmt.Errorf("unsupported type %T", a)
	}
}

// DecodeWord reads a single 32-byte return value.
func DecodeWord(out []byte) *big.Int { return new(big.Int).SetBytes(out) }

// DecodeAddress reads a 32-byte return value as an address.
func DecodeAddress(out []byte) Address {
	var a Address
	if len(out) >= 32 {
		copy(a[:], out[12:32])
	}
	return a
}

// --- the ERC-20 surface this prototype knows how to talk to ---
//
// Listed once, so the CLI, the explorer API and the tests all agree about what
// a signature is rather than each spelling it out.
var ERC20 = struct {
	TotalSupply, BalanceOf, Transfer, Approve, Allowance, TransferFrom, Decimals string
	TransferEvent, ApprovalEvent                                                 string
}{
	TotalSupply:   "totalSupply()",
	BalanceOf:     "balanceOf(address)",
	Transfer:      "transfer(address,uint256)",
	Approve:       "approve(address,uint256)",
	Allowance:     "allowance(address,address)",
	TransferFrom:  "transferFrom(address,address,uint256)",
	Decimals:      "decimals()",
	TransferEvent: "Transfer(address,address,uint256)",
	ApprovalEvent: "Approval(address,address,uint256)",
}

// LooksLikeERC20 reports whether deployed code contains the selectors of the
// mandatory ERC-20 methods.
//
// It is a heuristic and says so: a selector appearing in the bytecode means the
// dispatcher compares against it, not that the function behaves correctly. That
// is enough for an explorer deciding whether to render a "Token" panel, and it
// is not enough for anything that matters.
func LooksLikeERC20(code []byte) bool {
	for _, sig := range []string{ERC20.TotalSupply, ERC20.BalanceOf, ERC20.Transfer} {
		if !containsBytes(code, Selector(sig)) {
			return false
		}
	}
	return true
}

func containsBytes(hay, needle []byte) bool {
	return strings.Contains(string(hay), string(needle))
}
