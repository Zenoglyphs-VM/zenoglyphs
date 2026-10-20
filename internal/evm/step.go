package evm

import (
	"math/big"
)

// step executes one instruction. It returns (halt, output, error).
//
// Everything is in one switch on purpose. An EVM's correctness is mostly the
// question "does each opcode pop and push exactly what the spec says", and
// that is answered by reading the cases next to each other, not by chasing a
// dispatch table through three files.
func (e *EVM) step(f *frame, op byte) (bool, []byte, error) {
	switch {

	// --- arithmetic ---
	case op == STOP:
		return true, nil, nil

	case op == ADD, op == MUL, op == SUB, op == DIV, op == SDIV,
		op == MOD, op == SMOD, op == EXP, op == SIGNEXTEND,
		op == LT, op == GT, op == SLT, op == SGT, op == EQ,
		op == AND, op == OR, op == XOR, op == BYTE, op == SHL, op == SHR, op == SAR:
		v, err := f.popN(2)
		if err != nil {
			return false, nil, err
		}
		if err := f.push(binop(op, v[0], v[1])); err != nil {
			return false, nil, err
		}

	case op == ADDMOD, op == MULMOD:
		v, err := f.popN(3)
		if err != nil {
			return false, nil, err
		}
		r := new(big.Int)
		if v[2].Sign() != 0 {
			if op == ADDMOD {
				r.Add(v[0], v[1])
			} else {
				r.Mul(v[0], v[1])
			}
			r.Mod(r, v[2])
		}
		if err := f.push(norm(r)); err != nil {
			return false, nil, err
		}

	case op == ISZERO, op == NOT:
		x, err := f.pop()
		if err != nil {
			return false, nil, err
		}
		r := new(big.Int)
		if op == ISZERO {
			if x.Sign() == 0 {
				r.SetUint64(1)
			}
		} else {
			r.Xor(x, mask256)
		}
		if err := f.push(norm(r)); err != nil {
			return false, nil, err
		}

	// --- keccak ---
	case op == KECCAK256:
		v, err := f.popN(2)
		if err != nil {
			return false, nil, err
		}
		off, size := toInt(v[0]), toInt(v[1])
		if err := e.expand(f, off, size); err != nil {
			return false, nil, err
		}
		if err := e.charge(uint64((size+31)/32) * 6); err != nil {
			return false, nil, err
		}
		if err := f.push(new(big.Int).SetBytes(keccak(f.mem[off : off+size]))); err != nil {
			return false, nil, err
		}

	// --- environment ---
	//
	// Every case below falls through to the shared f.pc++ at the bottom.
	// Returning early from here is a real bug and not a hypothetical one: an
	// opcode that skips the increment re-executes itself until gas runs out,
	// which presents as "out of gas" on a contract that should cost nothing.
	case op == ADDRESS, op == CALLER, op == ORIGIN, op == CALLVALUE,
		op == CALLDATASIZE, op == CODESIZE, op == RETURNDATASIZE,
		op == NUMBER, op == TIMESTAMP, op == CHAINID, op == SELFBALANCE,
		op == PC, op == MSIZE, op == GAS:
		if err := f.push(e.nullary(f, op)); err != nil {
			return false, nil, err
		}

	case op == BALANCE, op == EXTCODESIZE:
		x, err := f.pop()
		if err != nil {
			return false, nil, err
		}
		var a Address
		copy(a[:], bytes32(x)[12:])
		v := new(big.Int)
		if op == BALANCE {
			v = e.State.balance(a)
		} else {
			v.SetInt64(int64(len(e.State.code(a))))
		}
		if err := f.push(v); err != nil {
			return false, nil, err
		}

	case op == CALLDATALOAD:
		x, err := f.pop()
		if err != nil {
			return false, nil, err
		}
		if err := f.push(wordFromBytes(getData(f.input, x, big.NewInt(32)))); err != nil {
			return false, nil, err
		}

	case op == CALLDATACOPY, op == CODECOPY, op == RETURNDATACOPY:
		v, err := f.popN(3)
		if err != nil {
			return false, nil, err
		}
		dst, size := toInt(v[0]), toInt(v[2])
		if err := e.expand(f, dst, size); err != nil {
			return false, nil, err
		}
		if err := e.charge(uint64((size+31)/32) * 3); err != nil {
			return false, nil, err
		}
		src := f.input
		if op == CODECOPY {
			src = f.code
		} else if op == RETURNDATACOPY {
			src = f.ret
		}
		copy(f.mem[dst:dst+size], getData(src, v[1], v[2]))

	// --- stack, memory, storage ---
	case op == POP:
		if _, err := f.pop(); err != nil {
			return false, nil, err
		}

	case op == MLOAD:
		x, err := f.pop()
		if err != nil {
			return false, nil, err
		}
		off := toInt(x)
		if err := e.expand(f, off, 32); err != nil {
			return false, nil, err
		}
		if err := f.push(new(big.Int).SetBytes(f.mem[off : off+32])); err != nil {
			return false, nil, err
		}

	case op == MSTORE, op == MSTORE8:
		v, err := f.popN(2)
		if err != nil {
			return false, nil, err
		}
		off := toInt(v[0])
		n := 32
		if op == MSTORE8 {
			n = 1
		}
		if err := e.expand(f, off, n); err != nil {
			return false, nil, err
		}
		if op == MSTORE8 {
			f.mem[off] = bytes32(v[1])[31]
		} else {
			copy(f.mem[off:off+32], bytes32(v[1]))
		}

	case op == SLOAD:
		k, err := f.pop()
		if err != nil {
			return false, nil, err
		}
		if err := f.push(e.State.load(f.self, k)); err != nil {
			return false, nil, err
		}

	case op == SSTORE:
		if f.static {
			return false, nil, ErrWriteProtect
		}
		v, err := f.popN(2)
		if err != nil {
			return false, nil, err
		}
		e.State.Store(f.self, v[0], v[1])

	// --- control flow ---
	case op == JUMP, op == JUMPI:
		n := 1
		if op == JUMPI {
			n = 2
		}
		v, err := f.popN(n)
		if err != nil {
			return false, nil, err
		}
		if op == JUMPI && v[1].Sign() == 0 {
			f.pc++
			return false, nil, nil
		}
		dst := toInt(v[0])
		if !f.jumpdest[dst] {
			return false, nil, ErrBadJump
		}
		f.pc = dst
		return false, nil, nil

	case op == JUMPDEST:
		// a no-op marker; analyse() already recorded it

	case op == PUSH0:
		if err := f.push(new(big.Int)); err != nil {
			return false, nil, err
		}

	case op >= PUSH1 && op <= PUSH32:
		n := int(op-PUSH1) + 1
		start := f.pc + 1
		end := start + n
		if end > len(f.code) {
			end = len(f.code) // an immediate running off the end reads as zeros
		}
		buf := make([]byte, n)
		copy(buf, f.code[start:end])
		if err := f.push(new(big.Int).SetBytes(buf)); err != nil {
			return false, nil, err
		}
		f.pc += n

	case op >= DUP1 && op <= DUP16:
		n := int(op-DUP1) + 1
		if len(f.stack) < n {
			return false, nil, ErrStackUnder
		}
		if err := f.push(new(big.Int).Set(f.stack[len(f.stack)-n])); err != nil {
			return false, nil, err
		}

	case op >= SWAP1 && op <= SWAP16:
		n := int(op-SWAP1) + 2
		if len(f.stack) < n {
			return false, nil, ErrStackUnder
		}
		top := len(f.stack) - 1
		f.stack[top], f.stack[top-n+1] = f.stack[top-n+1], f.stack[top]

	// --- logs ---
	case op >= LOG0 && op <= LOG4:
		if f.static {
			return false, nil, ErrWriteProtect
		}
		n := int(op - LOG0)
		v, err := f.popN(2 + n)
		if err != nil {
			return false, nil, err
		}
		off, size := toInt(v[0]), toInt(v[1])
		if err := e.expand(f, off, size); err != nil {
			return false, nil, err
		}
		if err := e.charge(uint64(size)*8 + uint64(n)*375); err != nil {
			return false, nil, err
		}
		lg := Log{Address: f.self, Data: append([]byte(nil), f.mem[off:off+size]...)}
		for i := 0; i < n; i++ {
			lg.Topics = append(lg.Topics, bytes32(v[2+i]))
		}
		e.Logs = append(e.Logs, lg)

	// --- calls ---
	case op == CALL, op == STATICCALL:
		var (
			v   []*big.Int
			err error
		)
		if op == CALL {
			v, err = f.popN(7) // gas, to, value, inOff, inSize, outOff, outSize
		} else {
			v, err = f.popN(6) // gas, to, inOff, inSize, outOff, outSize
		}
		if err != nil {
			return false, nil, err
		}
		var (
			to              Address
			value           = new(big.Int)
			inOff, inSize   int
			outOff, outSize int
		)
		copy(to[:], bytes32(v[1])[12:])
		if op == CALL {
			value = v[2]
			inOff, inSize = toInt(v[3]), toInt(v[4])
			outOff, outSize = toInt(v[5]), toInt(v[6])
		} else {
			inOff, inSize = toInt(v[2]), toInt(v[3])
			outOff, outSize = toInt(v[4]), toInt(v[5])
		}
		if err := e.expand(f, inOff, inSize); err != nil {
			return false, nil, err
		}
		if err := e.expand(f, outOff, outSize); err != nil {
			return false, nil, err
		}
		in := append([]byte(nil), f.mem[inOff:inOff+inSize]...)
		out, cerr := e.call(f.self, to, in, value, f.static || op == STATICCALL)
		f.ret = out
		if outSize > 0 && len(out) > 0 {
			copy(f.mem[outOff:outOff+outSize], out)
		}
		ok := new(big.Int)
		if cerr == nil {
			ok.SetUint64(1)
		}
		if err := f.push(ok); err != nil {
			return false, nil, err
		}

	// --- halting ---
	case op == RETURN, op == REVERT:
		v, err := f.popN(2)
		if err != nil {
			return false, nil, err
		}
		off, size := toInt(v[0]), toInt(v[1])
		if err := e.expand(f, off, size); err != nil {
			return false, nil, err
		}
		out := append([]byte(nil), f.mem[off:off+size]...)
		if op == REVERT {
			return true, out, ErrRevert
		}
		return true, out, nil

	default:
		return false, nil, ErrInvalidOp
	}

	f.pc++
	return false, nil, nil
}

// binop is every two-operand arithmetic, comparison and bitwise opcode.
// Split out only because putting it inline would bury the control flow above.
func binop(op byte, x, y *big.Int) *big.Int {
	r := new(big.Int)
	one := big.NewInt(1)
	switch op {
	case ADD:
		r.Add(x, y)
	case MUL:
		r.Mul(x, y)
	case SUB:
		r.Sub(x, y)
	case DIV:
		if y.Sign() != 0 {
			r.Div(x, y)
		}
	case SDIV:
		if y.Sign() != 0 {
			r = fromSigned(new(big.Int).Quo(signed(x), signed(y)))
		}
	case MOD:
		if y.Sign() != 0 {
			r.Mod(x, y)
		}
	case SMOD:
		if y.Sign() != 0 {
			r = fromSigned(new(big.Int).Rem(signed(x), signed(y)))
		}
	case EXP:
		r.Exp(x, y, two256)
	case SIGNEXTEND:
		// x is the byte index; y the value whose sign is extended from it
		if x.Cmp(big.NewInt(31)) < 0 {
			bit := uint(x.Uint64()*8 + 7)
			mask := new(big.Int).Sub(new(big.Int).Lsh(one, bit), one)
			if y.Bit(int(bit)) == 1 {
				r.Or(y, new(big.Int).Xor(mask256, mask))
			} else {
				r.And(y, mask)
			}
		} else {
			r.Set(y)
		}
	case LT:
		if x.Cmp(y) < 0 {
			r.SetUint64(1)
		}
	case GT:
		if x.Cmp(y) > 0 {
			r.SetUint64(1)
		}
	case SLT:
		if signed(x).Cmp(signed(y)) < 0 {
			r.SetUint64(1)
		}
	case SGT:
		if signed(x).Cmp(signed(y)) > 0 {
			r.SetUint64(1)
		}
	case EQ:
		if x.Cmp(y) == 0 {
			r.SetUint64(1)
		}
	case AND:
		r.And(x, y)
	case OR:
		r.Or(x, y)
	case XOR:
		r.Xor(x, y)
	case BYTE:
		if x.Cmp(big.NewInt(32)) < 0 {
			r.SetUint64(uint64(bytes32(y)[x.Uint64()]))
		}
	case SHL:
		if x.Cmp(big.NewInt(256)) < 0 {
			r.Lsh(y, uint(x.Uint64()))
		}
	case SHR:
		if x.Cmp(big.NewInt(256)) < 0 {
			r.Rsh(y, uint(x.Uint64()))
		}
	case SAR:
		sy := signed(y)
		if x.Cmp(big.NewInt(256)) >= 0 {
			if sy.Sign() < 0 {
				r = fromSigned(big.NewInt(-1))
			}
		} else {
			r = fromSigned(new(big.Int).Rsh(sy, uint(x.Uint64())))
		}
	}
	return norm(r)
}

// nullary returns the value for an opcode that pushes one word and pops
// nothing. Grouped so that all of them share the pc increment rather than each
// remembering to do it.
func (e *EVM) nullary(f *frame, op byte) *big.Int {
	switch op {
	case ADDRESS:
		return new(big.Int).SetBytes(f.self[:])
	case CALLER:
		return new(big.Int).SetBytes(f.caller[:])
	case ORIGIN:
		return new(big.Int).SetBytes(e.Block.Origin[:])
	case CALLVALUE:
		return new(big.Int).Set(f.value)
	case CALLDATASIZE:
		return big.NewInt(int64(len(f.input)))
	case CODESIZE:
		return big.NewInt(int64(len(f.code)))
	case RETURNDATASIZE:
		return big.NewInt(int64(len(f.ret)))
	case NUMBER:
		return new(big.Int).SetUint64(e.Block.Number)
	case TIMESTAMP:
		return new(big.Int).SetUint64(e.Block.Time)
	case CHAINID:
		return new(big.Int).SetUint64(e.Block.ChainID)
	case SELFBALANCE:
		return e.State.balance(f.self)
	case PC:
		return big.NewInt(int64(f.pc))
	case MSIZE:
		return big.NewInt(int64(len(f.mem)))
	case GAS:
		return new(big.Int).SetUint64(e.gas)
	}
	return new(big.Int)
}
