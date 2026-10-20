package evm

import (
	"encoding/hex"
	"math/big"
	"testing"
)

// A complete ERC-20, hand-assembled from EVM opcodes (there is no solc in this
// tree). Storage slot 0 is the total supply, mapping slot 1 the balances,
// mapping slot 2 the allowances. The constructor mints the whole supply to
// whoever deployed it.
const erc20Init = "7f00000000000000000000000000000000000000000000d3c21bcecceda1000000806000553360005260016020526040600020556102ae806100416000396000f360003560e01c806318160ddd1461005857806370a082311461006f578063a9059cbb1461009f578063095ea7b314610123578063dd62ed3e1461019357806323b872dd146101e7578063313ce567146100645760006000fd5b60005460005260206000f35b601260005260206000f35b60043573ffffffffffffffffffffffffffffffffffffffff16600052600160205260406000205460005260206000f35b60243560043573ffffffffffffffffffffffffffffffffffffffff16336000526001602052604060002080548311610053578054839003905580600052600160205260406000208054830190558160005280337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005260206000f35b60243560043573ffffffffffffffffffffffffffffffffffffffff1633600052600260205260406000206020528060005260406000208290558160005280337f8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b92560206000a3600160005260206000f35b60043573ffffffffffffffffffffffffffffffffffffffff166000526002602052604060002060205260243573ffffffffffffffffffffffffffffffffffffffff1660005260406000205460005260206000f35b60443560243573ffffffffffffffffffffffffffffffffffffffff1660043573ffffffffffffffffffffffffffffffffffffffff16806000526002602052604060002060205233600052604060002080548411610053578054849003905580600052600160205260406000208054841161005357805484900390559080600052600160205260406000208054840190558260005280827fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005260206000f3"

// oneMillion is the constructor's fixed supply: 1,000,000 at 18 decimals.
func oneMillion() *big.Int {
	n, _ := new(big.Int).SetString("1000000000000000000000000", 10)
	return n
}

func deployERC20(t *testing.T) (*Chain, Address, Address) {
	t.Helper()
	deployID := id32(1)
	token := ContractAddress(hexb(t, deployID))
	c := NewChain(69)
	err := c.Apply([]Inscription{{
		ID: deployID, Issuer: devnetIssuer, Mime: Mime, Height: 100,
		Content: hexb(t, erc20Init), Ext: ext(t, map[string]any{"op": "deploy"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Receipts[0]; !r.Success {
		t.Fatalf("deploy failed: %s", r.Err)
	}
	deployer, err := AddressOf(devnetIssuer)
	if err != nil {
		t.Fatal(err)
	}
	return c, token, deployer
}

// query reads through the contract rather than through its storage layout —
// the eth_call equivalent an explorer needs for every balance it renders.
func query(t *testing.T, c *Chain, to Address, sig string, args ...any) *big.Int {
	t.Helper()
	data, err := EncodeCall(sig, args...)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Query(to, data)
	if err != nil {
		t.Fatalf("%s: %v", sig, err)
	}
	return DecodeWord(out)
}

func send(t *testing.T, c *Chain, n byte, h uint64, issuer string, to Address, sig string, args ...any) Receipt {
	t.Helper()
	data, err := EncodeCall(sig, args...)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply([]Inscription{{
		ID: id32(n), Issuer: issuer, Mime: Mime, Height: h,
		Content: data, Ext: ext(t, map[string]any{"op": "call", "to": to.Hex()}),
	}}); err != nil {
		t.Fatal(err)
	}
	return c.Receipts[len(c.Receipts)-1]
}

func addr(t *testing.T, s string) Address {
	t.Helper()
	a, err := AddressFromHex(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestERC20ConstructorMintsToDeployer(t *testing.T) {
	c, token, deployer := deployERC20(t)
	if got := query(t, c, token, ERC20.TotalSupply); got.Cmp(oneMillion()) != 0 {
		t.Fatalf("totalSupply = %s, want %s", got, oneMillion())
	}
	if got := query(t, c, token, ERC20.BalanceOf, deployer); got.Cmp(oneMillion()) != 0 {
		t.Fatalf("deployer balance = %s, want the whole supply", got)
	}
	if got := query(t, c, token, ERC20.Decimals); got.Cmp(big.NewInt(18)) != 0 {
		t.Fatalf("decimals = %s, want 18", got)
	}
	if !LooksLikeERC20(c.State.Code(token)) {
		t.Fatal("deployed code was not recognised as ERC-20")
	}
}

func TestERC20TransferMovesBalanceAndLogs(t *testing.T) {
	c, token, deployer := deployERC20(t)
	bob := addr(t, "0x00000000000000000000000000000000000000b0")
	amount := big.NewInt(2500)

	r := send(t, c, 2, 101, devnetIssuer, token, ERC20.Transfer, bob, amount)
	if !r.Success {
		t.Fatalf("transfer failed: %s", r.Err)
	}
	if got := DecodeWord(r.Output); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("transfer returned %s, want true", got)
	}
	if got := query(t, c, token, ERC20.BalanceOf, bob); got.Cmp(amount) != 0 {
		t.Fatalf("bob balance = %s, want %s", got, amount)
	}
	want := new(big.Int).Sub(oneMillion(), amount)
	if got := query(t, c, token, ERC20.BalanceOf, deployer); got.Cmp(want) != 0 {
		t.Fatalf("deployer balance = %s, want %s", got, want)
	}

	// A real ERC-20 Transfer event: three topics, from and to indexed, the
	// amount in the data.
	if len(r.Logs) != 1 {
		t.Fatalf("got %d logs, want 1", len(r.Logs))
	}
	lg := r.Logs[0]
	if len(lg.Topics) != 3 {
		t.Fatalf("got %d topics, want 3", len(lg.Topics))
	}
	if hex.EncodeToString(lg.Topics[0]) != hex.EncodeToString(Topic(ERC20.TransferEvent)) {
		t.Fatalf("topic0 = %x, want %x", lg.Topics[0], Topic(ERC20.TransferEvent))
	}
	if DecodeAddress(lg.Topics[1]) != deployer {
		t.Fatalf("from topic = %s, want %s", DecodeAddress(lg.Topics[1]).Hex(), deployer.Hex())
	}
	if DecodeAddress(lg.Topics[2]) != bob {
		t.Fatalf("to topic = %s, want %s", DecodeAddress(lg.Topics[2]).Hex(), bob.Hex())
	}
	if DecodeWord(lg.Data).Cmp(amount) != 0 {
		t.Fatalf("log data = %s, want %s", DecodeWord(lg.Data), amount)
	}
}

// Overspending must revert. Without the check an ERC-20 underflows to
// 2^256-1 rather than failing, which is the classic token bug.
func TestERC20TransferBeyondBalanceReverts(t *testing.T) {
	c, token, deployer := deployERC20(t)
	bob := addr(t, "0x00000000000000000000000000000000000000b0")
	tooMuch := new(big.Int).Add(oneMillion(), big.NewInt(1))

	r := send(t, c, 2, 101, devnetIssuer, token, ERC20.Transfer, bob, tooMuch)
	if r.Success {
		t.Fatal("transferring more than the balance succeeded")
	}
	if got := query(t, c, token, ERC20.BalanceOf, bob); got.Sign() != 0 {
		t.Fatalf("bob gained %s from a reverted transfer", got)
	}
	if got := query(t, c, token, ERC20.BalanceOf, deployer); got.Cmp(oneMillion()) != 0 {
		t.Fatalf("deployer balance = %s, want it unchanged", got)
	}
}

func TestERC20ApproveAndTransferFrom(t *testing.T) {
	c, token, deployer := deployERC20(t)
	// A second inscriber, so the spender really is a different account.
	const spenderNom = "z1zen0glyphxescr0wat0emq0rrazm9r0ny47hpr"
	spender, err := AddressOf(spenderNom)
	if err != nil {
		t.Fatal(err)
	}
	carol := addr(t, "0x00000000000000000000000000000000000000ca")
	allowance := big.NewInt(1000)

	if r := send(t, c, 2, 101, devnetIssuer, token, ERC20.Approve, spender, allowance); !r.Success {
		t.Fatalf("approve failed: %s", r.Err)
	}
	if got := query(t, c, token, ERC20.Allowance, deployer, spender); got.Cmp(allowance) != 0 {
		t.Fatalf("allowance = %s, want %s", got, allowance)
	}

	spend := big.NewInt(400)
	r := send(t, c, 3, 102, spenderNom, token, ERC20.TransferFrom, deployer, carol, spend)
	if !r.Success {
		t.Fatalf("transferFrom failed: %s", r.Err)
	}
	if got := query(t, c, token, ERC20.BalanceOf, carol); got.Cmp(spend) != 0 {
		t.Fatalf("carol balance = %s, want %s", got, spend)
	}
	left := new(big.Int).Sub(allowance, spend)
	if got := query(t, c, token, ERC20.Allowance, deployer, spender); got.Cmp(left) != 0 {
		t.Fatalf("allowance after spend = %s, want %s", got, left)
	}

	// Spending past what is left must fail without moving anything.
	over := send(t, c, 4, 103, spenderNom, token, ERC20.TransferFrom, deployer, carol, big.NewInt(999))
	if over.Success {
		t.Fatal("transferFrom beyond the allowance succeeded")
	}
	if got := query(t, c, token, ERC20.BalanceOf, carol); got.Cmp(spend) != 0 {
		t.Fatalf("carol balance moved on a reverted transferFrom: %s", got)
	}
}

// Token accounting must survive replay: the same inscriptions in a different
// order must produce the same balances and the same root.
func TestERC20AccountingIsDeterministic(t *testing.T) {
	bob := addr(t, "0x00000000000000000000000000000000000000b0")
	token := ContractAddress(hexb(t, id32(1)))

	build := func(order []int) *Chain {
		mk := func(n byte, h uint64, amt int64) Inscription {
			d, err := EncodeCall(ERC20.Transfer, bob, big.NewInt(amt))
			if err != nil {
				t.Fatal(err)
			}
			return Inscription{ID: id32(n), Issuer: devnetIssuer, Mime: Mime, Height: h,
				Content: d, Ext: ext(t, map[string]any{"op": "call", "to": token.Hex()})}
		}
		all := []Inscription{
			{ID: id32(1), Issuer: devnetIssuer, Mime: Mime, Height: 100,
				Content: hexb(t, erc20Init), Ext: ext(t, map[string]any{"op": "deploy"})},
			mk(2, 101, 10), mk(3, 102, 20), mk(4, 103, 30),
		}
		picked := make([]Inscription, 0, len(all))
		for _, i := range order {
			picked = append(picked, all[i])
		}
		c := NewChain(69)
		if err := c.Apply(picked); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a := build([]int{0, 1, 2, 3})
	b := build([]int{3, 1, 0, 2})
	if a.State.Root() != b.State.Root() {
		t.Fatalf("token state diverged by input order:\n a %s\n b %s", a.State.Root(), b.State.Root())
	}
	if got := query(t, a, token, ERC20.BalanceOf, bob); got.Cmp(big.NewInt(60)) != 0 {
		t.Fatalf("bob balance = %s, want 60", got)
	}
}

// A Query must not be able to change anything, or an explorer rendering a page
// would mutate the chain it is rendering.
func TestQueryCannotWrite(t *testing.T) {
	c, token, deployer := deployERC20(t)
	before := c.State.Root()

	data, err := EncodeCall(ERC20.Transfer, addr(t, "0x00000000000000000000000000000000000000b0"), big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(token, data); err == nil {
		t.Fatal("a state-changing call succeeded inside Query")
	}
	if c.State.Root() != before {
		t.Fatal("Query changed the state root")
	}
	if got := query(t, c, token, ERC20.BalanceOf, deployer); got.Cmp(oneMillion()) != 0 {
		t.Fatalf("deployer balance = %s after a query, want it unchanged", got)
	}
}
