package evm

// The opcode set. Numbers are the wire values and are not this project's to
// choose — they are the EVM's, and the whole value of picking the EVM is that
// existing tooling, compilers and auditors already know them.
const (
	STOP       = 0x00
	ADD        = 0x01
	MUL        = 0x02
	SUB        = 0x03
	DIV        = 0x04
	SDIV       = 0x05
	MOD        = 0x06
	SMOD       = 0x07
	ADDMOD     = 0x08
	MULMOD     = 0x09
	EXP        = 0x0a
	SIGNEXTEND = 0x0b

	LT     = 0x10
	GT     = 0x11
	SLT    = 0x12
	SGT    = 0x13
	EQ     = 0x14
	ISZERO = 0x15
	AND    = 0x16
	OR     = 0x17
	XOR    = 0x18
	NOT    = 0x19
	BYTE   = 0x1a
	SHL    = 0x1b
	SHR    = 0x1c
	SAR    = 0x1d

	KECCAK256 = 0x20

	ADDRESS        = 0x30
	BALANCE        = 0x31
	ORIGIN         = 0x32
	CALLER         = 0x33
	CALLVALUE      = 0x34
	CALLDATALOAD   = 0x35
	CALLDATASIZE   = 0x36
	CALLDATACOPY   = 0x37
	CODESIZE       = 0x38
	CODECOPY       = 0x39
	RETURNDATASIZE = 0x3d
	RETURNDATACOPY = 0x3e
	EXTCODESIZE    = 0x3b

	NUMBER      = 0x43
	TIMESTAMP   = 0x42
	CHAINID     = 0x46
	SELFBALANCE = 0x47

	POP      = 0x50
	MLOAD    = 0x51
	MSTORE   = 0x52
	MSTORE8  = 0x53
	SLOAD    = 0x54
	SSTORE   = 0x55
	JUMP     = 0x56
	JUMPI    = 0x57
	PC       = 0x58
	MSIZE    = 0x59
	GAS      = 0x5a
	JUMPDEST = 0x5b
	PUSH0    = 0x5f

	PUSH1  = 0x60
	PUSH32 = 0x7f
	DUP1   = 0x80
	DUP16  = 0x8f
	SWAP1  = 0x90
	SWAP16 = 0x9f

	LOG0 = 0xa0
	LOG4 = 0xa4

	CALL       = 0xf1
	RETURN     = 0xf3
	STATICCALL = 0xfa
	REVERT     = 0xfd
	INVALID    = 0xfe
)

// gasOf is the per-opcode base cost.
//
// A deliberately flattened schedule. Ethereum's exact prices exist to allocate
// a fee market across competing transactions; there is no such market here,
// because the *data* was already paid for in plasma when the inscription was
// published. What gas has to do here is bound how much work an inscription can
// impose on every deriver that replays it — so what matters is that expensive
// things cost more than cheap ones and that nothing costs zero, not that
// SSTORE costs exactly 20000.
func gasOf(op byte) uint64 {
	switch {
	case op == STOP || op == RETURN || op == REVERT || op == JUMPDEST || op == INVALID:
		return 1
	case op == SSTORE:
		return 5000
	case op == SLOAD || op == BALANCE || op == EXTCODESIZE:
		return 200
	case op == KECCAK256:
		return 30
	case op == CALL || op == STATICCALL:
		return 700
	case op >= LOG0 && op <= LOG4:
		return 375
	case op == EXP:
		return 60
	case op == MUL || op == DIV || op == SDIV || op == MOD || op == SMOD ||
		op == ADDMOD || op == MULMOD || op == SIGNEXTEND:
		return 5
	default:
		return 3
	}
}

// Name renders an opcode for traces and error messages. Unknown bytes render
// as their hex value rather than as a guess.
func Name(op byte) string {
	if n, ok := names[op]; ok {
		return n
	}
	if op >= PUSH1 && op <= PUSH32 {
		return "PUSH" + itoa(int(op-PUSH1)+1)
	}
	if op >= DUP1 && op <= DUP16 {
		return "DUP" + itoa(int(op-DUP1)+1)
	}
	if op >= SWAP1 && op <= SWAP16 {
		return "SWAP" + itoa(int(op-SWAP1)+1)
	}
	if op >= LOG0 && op <= LOG4 {
		return "LOG" + itoa(int(op-LOG0))
	}
	return "0x" + hexByte(op)
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func hexByte(b byte) string {
	const d = "0123456789abcdef"
	return string([]byte{d[b>>4], d[b&0xf]})
}

var names = map[byte]string{
	STOP: "STOP", ADD: "ADD", MUL: "MUL", SUB: "SUB", DIV: "DIV", SDIV: "SDIV",
	MOD: "MOD", SMOD: "SMOD", ADDMOD: "ADDMOD", MULMOD: "MULMOD", EXP: "EXP",
	SIGNEXTEND: "SIGNEXTEND", LT: "LT", GT: "GT", SLT: "SLT", SGT: "SGT",
	EQ: "EQ", ISZERO: "ISZERO", AND: "AND", OR: "OR", XOR: "XOR", NOT: "NOT",
	BYTE: "BYTE", SHL: "SHL", SHR: "SHR", SAR: "SAR", KECCAK256: "KECCAK256",
	ADDRESS: "ADDRESS", BALANCE: "BALANCE", ORIGIN: "ORIGIN", CALLER: "CALLER",
	CALLVALUE: "CALLVALUE", CALLDATALOAD: "CALLDATALOAD", CALLDATASIZE: "CALLDATASIZE",
	CALLDATACOPY: "CALLDATACOPY", CODESIZE: "CODESIZE", CODECOPY: "CODECOPY",
	RETURNDATASIZE: "RETURNDATASIZE", RETURNDATACOPY: "RETURNDATACOPY",
	EXTCODESIZE: "EXTCODESIZE", NUMBER: "NUMBER", TIMESTAMP: "TIMESTAMP",
	CHAINID: "CHAINID", SELFBALANCE: "SELFBALANCE", POP: "POP", MLOAD: "MLOAD",
	MSTORE: "MSTORE", MSTORE8: "MSTORE8", SLOAD: "SLOAD", SSTORE: "SSTORE",
	JUMP: "JUMP", JUMPI: "JUMPI", PC: "PC", MSIZE: "MSIZE", GAS: "GAS",
	JUMPDEST: "JUMPDEST", PUSH0: "PUSH0", CALL: "CALL", RETURN: "RETURN",
	STATICCALL: "STATICCALL", REVERT: "REVERT", INVALID: "INVALID",
}
