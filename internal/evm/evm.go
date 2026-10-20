package evm

import (
	"errors"
	"math/big"
)

var (
	errBadAddress    = errors.New("evm: address must be 20 bytes")
	ErrStackOverflow = errors.New("evm: stack overflow")
	ErrStackUnder    = errors.New("evm: stack underflow")
	ErrOutOfGas      = errors.New("evm: out of gas")
	ErrBadJump       = errors.New("evm: invalid jump destination")
	ErrInvalidOp     = errors.New("evm: invalid opcode")
	ErrRevert        = errors.New("evm: execution reverted")
	ErrDepth         = errors.New("evm: call depth exceeded")
	ErrWriteProtect  = errors.New("evm: state modification in a static call")
)

// Limits. maxDepth and maxStack are Ethereum's; gas is charged on a
// deliberately simplified schedule (see charge) because the purpose here is to
// *bound* replay, not to reproduce mainnet pricing. A deriver that runs out of
// gas stops; it never diverges.
const (
	maxStack = 1024
	maxDepth = 64
)

// Log is an emitted event. Kept because logs are how a contract tells the
// outside world what happened, and a deriver that discards them can answer
// "what is the state" but never "how did it get there".
type Log struct {
	Address Address
	Topics  [][]byte
	Data    []byte
}

// BlockContext is what the EVM can observe about its surroundings.
//
// Every field maps to something the NoM chain already decides, so no deriver
// has to invent a value: Number is the momentum height, Time is the momentum
// timestamp, and ChainID is the chain identifier the node reports. Nothing
// here is chosen by the caller, which is what keeps two derivers in agreement.
type BlockContext struct {
	Number  uint64
	Time    uint64
	ChainID uint64
	Origin  Address
}

type frame struct {
	code     []byte
	pc       int
	stack    []*big.Int
	mem      []byte
	self     Address
	caller   Address
	value    *big.Int
	input    []byte
	static   bool
	ret      []byte
	jumpdest map[int]bool
}

// EVM executes against a State.
type EVM struct {
	State *State
	Block BlockContext
	Logs  []Log

	gas   uint64
	depth int
}

func New(st *State, blk BlockContext) *EVM { return &EVM{State: st, Block: blk} }

func (e *EVM) charge(n uint64) error {
	if e.gas < n {
		e.gas = 0
		return ErrOutOfGas
	}
	e.gas -= n
	return nil
}

// GasUsed reports consumption for the most recent top-level call.
func (e *EVM) GasLeft() uint64 { return e.gas }

// --- stack ---

func (f *frame) push(x *big.Int) error {
	if len(f.stack) >= maxStack {
		return ErrStackOverflow
	}
	f.stack = append(f.stack, x)
	return nil
}

func (f *frame) pop() (*big.Int, error) {
	if len(f.stack) == 0 {
		return nil, ErrStackUnder
	}
	x := f.stack[len(f.stack)-1]
	f.stack = f.stack[:len(f.stack)-1]
	return x, nil
}

func (f *frame) popN(n int) ([]*big.Int, error) {
	if len(f.stack) < n {
		return nil, ErrStackUnder
	}
	out := make([]*big.Int, n)
	for i := 0; i < n; i++ {
		out[i] = f.stack[len(f.stack)-1-i]
	}
	f.stack = f.stack[:len(f.stack)-n]
	return out, nil
}

// --- memory ---
//
// Memory is charged Ethereum's way: 3 gas per 32-byte word **plus a quadratic
// term**, and the quadratic term is the load-bearing half.
//
//	cost(words) = 3*words + words^2/512
//
// An earlier version here charged only the linear part, on the reasoning that
// a gas cap already bounds the work. That reasoning is wrong, and measurably
// so: linear pricing bounds how much *time* a call spends touching memory, and
// says nothing about the *footprint* it leaves while doing it. Nine bytes of
// contract code — one MSTORE at a far offset — allocated 32 MB for 3.1M gas,
// which under a 10M budget scales to roughly 106 MB in a single call, on every
// deriver replaying it.
//
// With the quadratic term the same 10M budget reaches about 2.3 MB. That is
// what the term is for: it makes the marginal word more expensive than the
// last, so a budget converts to a bounded allocation rather than a linear one.
func memoryGas(words uint64) uint64 { return 3*words + words*words/512 }

func (e *EVM) expand(f *frame, offset, size int) error {
	if size == 0 {
		return nil
	}
	need := offset + size
	if need <= len(f.mem) {
		return nil
	}
	words := uint64((need + 31) / 32)
	have := uint64((len(f.mem) + 31) / 32)
	if err := e.charge(memoryGas(words) - memoryGas(have)); err != nil {
		return err
	}
	grown := make([]byte, words*32)
	copy(grown, f.mem)
	f.mem = grown
	return nil
}

// analyse finds every JUMPDEST, skipping bytes that are PUSH immediates.
// Doing this up front is what makes a jump into the middle of a PUSH illegal
// rather than merely unlikely.
func analyse(code []byte) map[int]bool {
	dests := map[int]bool{}
	for i := 0; i < len(code); i++ {
		op := code[i]
		if op == JUMPDEST {
			dests[i] = true
			continue
		}
		if op >= PUSH1 && op <= PUSH32 {
			i += int(op-PUSH1) + 1
		}
	}
	return dests
}

// Call runs code at `to` with `input`, moving `value` from `from`.
func (e *EVM) Call(from, to Address, input []byte, value *big.Int, gas uint64, static bool) ([]byte, error) {
	e.gas = gas
	e.Logs = e.Logs[:0]
	return e.call(from, to, input, value, static)
}

func (e *EVM) call(from, to Address, input []byte, value *big.Int, static bool) ([]byte, error) {
	if e.depth > maxDepth {
		return nil, ErrDepth
	}
	if value.Sign() > 0 {
		if static {
			return nil, ErrWriteProtect
		}
		if !e.State.SubBalance(from, value) {
			return nil, errors.New("evm: insufficient balance")
		}
		e.State.AddBalance(to, value)
	}
	code := e.State.code(to)
	if len(code) == 0 {
		return nil, nil // a plain value transfer to an account with no code
	}
	snap := e.State.snapshot()
	f := &frame{code: code, self: to, caller: from, value: value, input: input,
		static: static, jumpdest: analyse(code)}
	e.depth++
	out, err := e.run(f)
	e.depth--
	if err != nil {
		e.State.revert(snap)
	}
	return out, err
}

// Create deploys code produced by running initCode, at the address derived
// from the inscription that carried it.
func (e *EVM) Create(from Address, addr Address, initCode []byte, value *big.Int, gas uint64) ([]byte, error) {
	e.gas = gas
	e.Logs = e.Logs[:0]
	if e.depth > maxDepth {
		return nil, ErrDepth
	}
	if value.Sign() > 0 {
		if !e.State.SubBalance(from, value) {
			return nil, errors.New("evm: insufficient balance")
		}
		e.State.AddBalance(addr, value)
	}
	snap := e.State.snapshot()
	f := &frame{code: initCode, self: addr, caller: from, value: value,
		jumpdest: analyse(initCode)}
	e.depth++
	runtime, err := e.run(f)
	e.depth--
	if err != nil {
		e.State.revert(snap)
		return nil, err
	}
	// Deposit cost, so that deploying a large contract is not free work for
	// every future deriver that has to store and re-read it.
	if err := e.charge(uint64(len(runtime)) * 200); err != nil {
		e.State.revert(snap)
		return nil, err
	}
	e.State.SetCode(addr, runtime)
	e.State.SetNonce(addr, 1)
	return runtime, nil
}

func (e *EVM) run(f *frame) ([]byte, error) {
	for {
		if f.pc >= len(f.code) {
			return nil, nil // running off the end is an implicit STOP
		}
		op := f.code[f.pc]
		if err := e.charge(gasOf(op)); err != nil {
			return nil, err
		}
		stop, out, err := e.step(f, op)
		if err != nil {
			return out, err
		}
		if stop {
			return out, nil
		}
	}
}
