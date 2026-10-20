package evm

import (
	"encoding/hex"
	"math/big"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The arithmetic that a wrong EVM gets wrong. Every expectation here is EVM
// semantics, not this implementation's opinion: 256-bit wrapping, division by
// zero yielding zero rather than trapping, and two's-complement signedness.
func TestArithmeticMatchesEvmSemantics(t *testing.T) {
	maxU := new(big.Int).Sub(two256, big.NewInt(1))
	cases := []struct {
		name string
		op   byte
		x, y *big.Int
		want *big.Int
	}{
		{"add wraps", ADD, maxU, big.NewInt(1), big.NewInt(0)},
		{"sub underflows to max", SUB, big.NewInt(0), big.NewInt(1), maxU},
		{"mul wraps", MUL, maxU, big.NewInt(2), new(big.Int).Sub(two256, big.NewInt(2))},
		{"div by zero is zero", DIV, big.NewInt(7), big.NewInt(0), big.NewInt(0)},
		{"mod by zero is zero", MOD, big.NewInt(7), big.NewInt(0), big.NewInt(0)},
		{"sdiv -6/2", SDIV, fromSigned(big.NewInt(-6)), big.NewInt(2), fromSigned(big.NewInt(-3))},
		{"smod -7%3 keeps sign", SMOD, fromSigned(big.NewInt(-7)), big.NewInt(3), fromSigned(big.NewInt(-1))},
		{"slt signed not unsigned", SLT, fromSigned(big.NewInt(-1)), big.NewInt(1), big.NewInt(1)},
		{"lt unsigned says opposite", LT, fromSigned(big.NewInt(-1)), big.NewInt(1), big.NewInt(0)},
		{"exp mod 2^256", EXP, big.NewInt(2), big.NewInt(256), big.NewInt(0)},
		{"shr", SHR, big.NewInt(4), big.NewInt(0xff), big.NewInt(0x0f)},
		{"sar keeps sign", SAR, big.NewInt(1), fromSigned(big.NewInt(-2)), fromSigned(big.NewInt(-1))},
		{"byte 31 of 0xff", BYTE, big.NewInt(31), big.NewInt(0xff), big.NewInt(0xff)},
		{"signextend byte0 of 0xff", SIGNEXTEND, big.NewInt(0), big.NewInt(0xff), maxU},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := binop(c.op, c.x, c.y); got.Cmp(c.want) != 0 {
				t.Fatalf("%s: got %s, want %s", Name(c.op), got, c.want)
			}
		})
	}
}

// KECCAK256 has to be Keccak-256, not SHA3-256. They differ only in padding,
// so a wrong choice produces plausible-looking hashes that disagree with every
// Ethereum tool in existence — the exact class of bug this pins.
func TestKeccakIsKeccakNotSha3(t *testing.T) {
	// keccak256("") is a constant every EVM developer recognises.
	const empty = "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	if got := hex.EncodeToString(keccak(nil)); got != empty {
		t.Fatalf("keccak256('') = %s, want %s", got, empty)
	}
	// And the function selector for transfer(address,uint256).
	if got := hex.EncodeToString(keccak([]byte("transfer(address,uint256)"))[:4]); got != "a9059cbb" {
		t.Fatalf("transfer selector = %s, want a9059cbb", got)
	}
}

// A jump into the middle of a PUSH immediate must fail. Without the up-front
// analysis it would land on a byte that merely happens to equal JUMPDEST.
func TestJumpIntoPushImmediateIsRejected(t *testing.T) {
	// PUSH1 0x5b (0x5b is JUMPDEST), then jump to offset 1 — inside the push.
	code := []byte{PUSH1, JUMPDEST, PUSH1, 0x01, JUMP}
	st := NewState()
	var self Address
	st.SetCode(self, code)
	e := New(st, BlockContext{})
	if _, err := e.Call(Address{}, self, nil, new(big.Int), 100000, false); err != ErrBadJump {
		t.Fatalf("err = %v, want %v", err, ErrBadJump)
	}
}

// Gas has to actually bound an infinite loop, or a single hostile inscription
// hangs every deriver that replays it (research/on-chain-execution.md §6.1).
func TestInfiniteLoopTerminatesOnGas(t *testing.T) {
	code := []byte{JUMPDEST, PUSH1, 0x00, JUMP} // jump to 0, forever
	st := NewState()
	var self Address
	st.SetCode(self, code)
	e := New(st, BlockContext{})
	if _, err := e.Call(Address{}, self, nil, new(big.Int), 100000, false); err != ErrOutOfGas {
		t.Fatalf("err = %v, want %v", err, ErrOutOfGas)
	}
}

// REVERT must undo storage writes; RETURN must keep them.
func TestRevertRollsBackStorage(t *testing.T) {
	st := NewState()
	var self, caller Address
	self[19] = 1
	// SSTORE(0, 42) then REVERT
	code := []byte{PUSH1, 42, PUSH1, 0x00, SSTORE, PUSH1, 0x00, PUSH1, 0x00, REVERT}
	st.SetCode(self, code)
	e := New(st, BlockContext{})
	if _, err := e.Call(caller, self, nil, new(big.Int), 100000, false); err != ErrRevert {
		t.Fatalf("err = %v, want revert", err)
	}
	if v := st.Load(self, big.NewInt(0)); v.Sign() != 0 {
		t.Fatalf("storage kept %s after revert, want 0", v)
	}
}

func TestStaticCallCannotWrite(t *testing.T) {
	st := NewState()
	var self Address
	self[19] = 1
	st.SetCode(self, []byte{PUSH1, 42, PUSH1, 0x00, SSTORE, STOP})
	e := New(st, BlockContext{})
	if _, err := e.Call(Address{}, self, nil, new(big.Int), 100000, true); err != ErrWriteProtect {
		t.Fatalf("err = %v, want %v", err, ErrWriteProtect)
	}
}

// A contract address is a commitment to its code, not to its deployer.
func TestContractAddressIsDerivedFromTheArtifact(t *testing.T) {
	id := mustHex(t, "e71f4edc78fea0ae620f276396b8639b7973a3c46950e6dfce0ee70988ac4bb7")
	a1 := ContractAddress(id)
	a2 := ContractAddress(id)
	if a1 != a2 {
		t.Fatal("derivation is not deterministic")
	}
	other := ContractAddress(mustHex(t, "0000000000000000000000000000000000000000000000000000000000000001"))
	if a1 == other {
		t.Fatal("different artifacts produced the same address")
	}
}

// Two states holding the same accounts must produce the same root regardless
// of insertion order — the property that lets two derivers compare in one step.
func TestStateRootIsOrderIndependent(t *testing.T) {
	var a, b Address
	a[19], b[19] = 1, 2

	s1 := NewState()
	s1.Store(a, big.NewInt(1), big.NewInt(10))
	s1.Store(b, big.NewInt(2), big.NewInt(20))

	s2 := NewState()
	s2.Store(b, big.NewInt(2), big.NewInt(20))
	s2.Store(a, big.NewInt(1), big.NewInt(10))

	if s1.Root() != s2.Root() {
		t.Fatalf("roots differ by insertion order:\n %s\n %s", s1.Root(), s2.Root())
	}
	// And a real difference must change it.
	s2.Store(a, big.NewInt(1), big.NewInt(11))
	if s1.Root() == s2.Root() {
		t.Fatal("root did not change when a value did")
	}
}

// Setting a slot to zero must be indistinguishable from never setting it, or
// two derivers that reached the same state by different routes disagree.
func TestZeroingASlotMatchesNeverSettingIt(t *testing.T) {
	var a Address
	s1, s2 := NewState(), NewState()
	s1.Store(a, big.NewInt(1), big.NewInt(5))
	s1.Store(a, big.NewInt(1), big.NewInt(0))
	s2.get(a) // same account exists, slot never written
	if s1.Root() != s2.Root() {
		t.Fatalf("zeroed slot left a trace:\n %s\n %s", s1.Root(), s2.Root())
	}
}
