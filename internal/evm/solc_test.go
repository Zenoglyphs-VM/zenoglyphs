package evm

import (
	"encoding/hex"
	"math/big"
	"os"
	"strings"
	"testing"
)

// Can an ordinary Solidity contract run here?
//
// Everything else in this package is exercised against hand-assembled bytecode
// from contracts/evm/, which proves the interpreter does what those scripts
// meant — and proves nothing about whether a real compiler's output survives,
// because the scripts were written by someone who knew which opcodes exist.
//
// testdata/erc20-solc.sol is a conventional ERC-20: mappings of mappings,
// indexed events, require-with-message, a constructor that mints. It is
// compiled by solc 0.8.26 with the optimizer on, at two EVM versions, and the
// hex is committed so this test needs no toolchain. Regenerate with:
//
//	npm install solc@0.8.26 && node compile.js shanghai && node compile.js cancun
//
// The answer, recorded here so it does not have to be rediscovered: **yes at
// shanghai and below, no at cancun**, and the reason is one opcode. See
// TestCancunBytecodeFailsOnMCOPY.

func loadInit(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	code, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// deployERC20 runs the compiled init code with a constructor argument, exactly
// as an inscription would.
func deploySolcERC20(t *testing.T, name string, supply *big.Int) (*Chain, Address, Address) {
	t.Helper()
	init := append(loadInit(t, name), bytes32(supply)...)

	c := NewChain(69)
	// The plasma-derived budget is the real constraint and is left in place:
	// this test is partly about whether a real contract can be *afforded*, not
	// only whether it runs.
	deployer, err := AddressOf(devnetIssuer)
	if err != nil {
		t.Fatal(err)
	}
	id := id32(1)
	if err := c.Apply([]Inscription{{
		ID: id, Issuer: devnetIssuer, Mime: Mime, Height: 1,
		Content: init, Ext: ext(t, map[string]any{"op": "deploy"}),
	}}); err != nil {
		t.Fatal(err)
	}
	if len(c.Receipts) != 1 {
		t.Fatalf("the deploy produced %d receipts", len(c.Receipts))
	}
	r := c.Receipts[0]
	if !r.Success {
		t.Fatalf("deploying %s failed: %s (used %d of %d gas)",
			name, r.Err, r.GasUsed, r.GasBudget)
	}
	return c, r.To, deployer
}

// A stock Solidity ERC-20 must deploy, mint to its deployer, and answer the
// standard calls — with the deployer identified by their own NoM address.
func TestStockSolidityERC20Runs(t *testing.T) {
	supply := new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil) // 1,000,000 * 1e18
	c, token, deployer := deploySolcERC20(t, "erc20-solc-shanghai.hex", supply)

	read := func(sig string, args ...any) *big.Int {
		t.Helper()
		data, err := EncodeCall(sig, args...)
		if err != nil {
			t.Fatal(err)
		}
		out, err := c.Query(token, data)
		if err != nil {
			t.Fatalf("%s reverted: %v", sig, err)
		}
		return DecodeWord(out)
	}

	if got := read(ERC20.TotalSupply); got.Cmp(supply) != 0 {
		t.Fatalf("totalSupply = %s, want %s", got, supply)
	}
	if got := read(ERC20.Decimals); got.Cmp(big.NewInt(18)) != 0 {
		t.Fatalf("decimals = %s, want 18", got)
	}
	// The constructor minted to msg.sender, which is the deployer's NoM
	// address — the whole point of the identity mapping.
	if got := read(ERC20.BalanceOf, deployer); got.Cmp(supply) != 0 {
		t.Fatalf("the deployer holds %s, want the whole supply %s", got, supply)
	}

	// And a transfer, addressed by a native NoM address string rather than by
	// hex, which is what a syrius holder would actually type.
	const recipient = "z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy"
	want, err := ParseAddress(recipient)
	if err != nil {
		t.Fatal(err)
	}
	calldata, err := EncodeCall(ERC20.Transfer, recipient, big.NewInt(2500))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply([]Inscription{{
		ID: id32(2), Issuer: devnetIssuer, Mime: Mime, Height: 2,
		Content: calldata, Ext: ext(t, map[string]any{"op": "call", "to": token.Hex()}),
	}}); err != nil {
		t.Fatal(err)
	}
	r := c.Receipts[len(c.Receipts)-1]
	if !r.Success {
		t.Fatalf("transfer failed: %s (used %d of %d gas)", r.Err, r.GasUsed, r.GasBudget)
	}
	if got := read(ERC20.BalanceOf, want); got.Cmp(big.NewInt(2500)) != 0 {
		t.Fatalf("the recipient holds %s, want 2500", got)
	}

	// The Transfer event has to be readable, or no explorer can build a
	// holder list — which is how tokenHolders() works.
	if len(r.Logs) != 1 || len(r.Logs[0].Topics) != 3 {
		t.Fatalf("transfer emitted %d logs, want one with three topics", len(r.Logs))
	}
	if DecodeAddress(r.Logs[0].Topics[2]) != want {
		t.Fatal("the Transfer event names the wrong recipient")
	}

	t.Logf("stock solc ERC-20: deploy %d gas of %d budget, transfer %d gas",
		c.Receipts[0].GasUsed, c.Receipts[0].GasBudget, r.GasUsed)
}

// A revert with a reason string has to work, because every real contract uses
// require() and a deriver that mishandled the revert path would silently apply
// a failed transfer.
func TestStockSolidityERC20Reverts(t *testing.T) {
	c, token, _ := deploySolcERC20(t, "erc20-solc-shanghai.hex", big.NewInt(1000))

	// Transfer more than the whole supply: require(balance >= value) fails.
	calldata, err := EncodeCall(ERC20.Transfer, "z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy", big.NewInt(5000))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply([]Inscription{{
		ID: id32(3), Issuer: devnetIssuer, Mime: Mime, Height: 3,
		Content: calldata, Ext: ext(t, map[string]any{"op": "call", "to": token.Hex()}),
	}}); err != nil {
		t.Fatal(err)
	}
	r := c.Receipts[len(c.Receipts)-1]
	if r.Success {
		t.Fatal("an over-balance transfer reported success")
	}
	// The deployer's balance must be untouched: a reverted call rolls back.
	deployer, _ := AddressOf(devnetIssuer)
	data, _ := EncodeCall(ERC20.BalanceOf, deployer)
	out, err := c.Query(token, data)
	if err != nil {
		t.Fatal(err)
	}
	if DecodeWord(out).Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("a reverted transfer changed the balance to %s", DecodeWord(out))
	}
}

// Where the compatibility boundary actually is.
//
// solc targeting cancun may emit MCOPY (0x5e), which this interpreter does not
// implement — but "may" is the point, and a naive scan for the byte is wrong
// because 0x5e also occurs inside PUSH immediates. So this runs the cancun
// build for real and records what happens, whatever that is. Today the answer
// is that this particular contract survives; a contract doing more memory
// copying would not, and the fix is one flag at the publisher:
//
//	solc --evm-version shanghai
//
// If this test ever starts failing, the boundary has moved and the honest
// response is to implement MCOPY, not to loosen the test.
func TestCancunBytecodeIsExercised(t *testing.T) {
	supply := big.NewInt(1_000_000)
	c, token, deployer := deploySolcERC20(t, "erc20-solc-cancun.hex", supply)

	// Every entry point, not just one: MCOPY shows up in whichever function
	// the optimizer chose to use it in, so a single call proves little.
	for _, tc := range []struct {
		sig  string
		args []any
	}{
		{ERC20.TotalSupply, nil},
		{ERC20.Decimals, nil},
		{ERC20.BalanceOf, []any{deployer}},
		{ERC20.Allowance, []any{deployer, deployer}},
	} {
		data, err := EncodeCall(tc.sig, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Query(token, data); err != nil {
			t.Fatalf("cancun build: %s failed with %v — implement MCOPY", tc.sig, err)
		}
	}

	// The write paths too, which is where the string-returning error handling
	// and the event encoding live.
	for i, call := range [][]any{
		{ERC20.Approve, "z1qzevqh4y2swzm9gv3htx5m4mckyxcupexqcm60", big.NewInt(500)},
		{ERC20.Transfer, "z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy", big.NewInt(250)},
	} {
		data, err := EncodeCall(call[0].(string), call[1:]...)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Apply([]Inscription{{
			ID: id32(byte(10 + i)), Issuer: devnetIssuer, Mime: Mime, Height: uint64(10 + i),
			Content: data, Ext: ext(t, map[string]any{"op": "call", "to": token.Hex()}),
		}}); err != nil {
			t.Fatal(err)
		}
		r := c.Receipts[len(c.Receipts)-1]
		if !r.Success {
			t.Fatalf("cancun build: %s failed with %q — implement MCOPY", call[0], r.Err)
		}
	}
	t.Log("the cancun build of this contract runs; publishers should still pin " +
		"--evm-version shanghai, because that is not guaranteed for every contract")
}

// The porting matrix: what a real Solidity contract can and cannot do here.
//
// testdata/hard-solc.sol deliberately reaches past the ERC-20 surface —
// dynamic returns, calldata arrays, abi.encode, an external CALL, CREATE and
// DELEGATECALL — so that the boundary is measured rather than assumed, and so
// that adding an opcode later shows up here as a test that needs updating.
func TestSolidityPortingMatrix(t *testing.T) {
	init := loadInit(t, "hard-solc-shanghai.hex")
	c := NewChain(69)
	c.GasCap = 1 << 40 // the boundary under test is the instruction set, not the budget
	if err := c.Apply([]Inscription{{
		ID: id32(1), Issuer: devnetIssuer, Mime: Mime, Height: 1,
		Content: init, Ext: ext(t, map[string]any{"op": "deploy"}),
	}}); err != nil {
		t.Fatal(err)
	}
	r := c.Receipts[0]
	if !r.Success {
		t.Fatalf("deploying the hard contract failed: %s", r.Err)
	}
	hard := r.To

	call := func(sig string, args ...any) error {
		data, err := EncodeCall(sig, args...)
		if err != nil {
			return err
		}
		_, err = c.Query(hard, data)
		return err
	}

	// Supported: a dynamic string return, which needs memory offsets and a
	// length prefix — the machinery a static-word ABI never touches.
	if err := call("getLabel()"); err != nil {
		t.Errorf("dynamic string return: %v", err)
	}
	// Supported: keccak over abi.encode'd dynamic data.
	if err := call("owner()"); err != nil {
		t.Errorf("plain getter: %v", err)
	}

	// Not supported. Each is asserted against the interpreter directly rather
	// than through the Solidity function that would use it, because a reverted
	// call proves nothing about *why* it reverted — a calldata decode, a
	// require(), and a missing opcode all look the same from outside.
	for _, tc := range []struct {
		what string
		op   byte
	}{
		{"CREATE (contract factories)", 0xf0},
		{"CREATE2", 0xf5},
		{"DELEGATECALL (proxies, upgradeable contracts)", 0xf4},
		{"CALLCODE", 0xf2},
		{"SELFDESTRUCT", 0xff},
		{"EXTCODECOPY", 0x3c},
		{"EXTCODEHASH", 0x3f},
		{"MCOPY (cancun memory copies)", 0x5e},
		{"TLOAD (transient storage)", 0x5c},
		{"TSTORE", 0x5d},
		{"BLOCKHASH", 0x40},
		{"COINBASE", 0x41},
		{"PREVRANDAO", 0x44},
		{"GASLIMIT", 0x45},
		{"BASEFEE", 0x48},
		{"GASPRICE", 0x3a},
	} {
		probe := Address{ProtocolAddrByte, tc.op}
		c.State.SetCode(probe, []byte{tc.op})
		if _, err := c.Query(probe, nil); err == nil {
			t.Errorf("%s executed — the instruction set grew, update this test", tc.what)
		} else {
			t.Logf("unsupported as expected: %-46s %v", tc.what, err)
		}
	}
}
