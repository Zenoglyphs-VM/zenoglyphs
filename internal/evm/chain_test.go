package evm

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// The counter contract, assembled from EVM opcodes (no solc in this tree).
//
//	increment()      d09de08a  storage[0] += 1, emits Incremented(uint256)
//	get()            6d4ce63c  returns storage[0]
//	add(uint256)     1003e2d2  storage[0] += arg
//	anything else              reverts
const counterInit = "6100858061000d6000396000f360003560e01c8063d09de08a1461002c5780636d4ce63c146100655780631003e2d2146100715760006000fd5b600054600101806000556000527f20d8a6f5a693f9d1d627a598e8820f7a55ee74c183aa8f1a30e8d4e8dd9a8d8460206000a160206000f35b60005460005260206000f35b600435600054018060005560005260206000f3"

// incrementedTopic is keccak256("Incremented(uint256)").
const incrementedTopic = "20d8a6f5a693f9d1d627a598e8820f7a55ee74c183aa8f1a30e8d4e8dd9a8d84"

const devnetIssuer = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"

func ext(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := cbor.Marshal(map[string]any{"evm": m})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hexb(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// id32 fabricates a distinct asset id, standing in for a mint block hash.
func id32(n byte) string {
	b := make([]byte, 32)
	b[31] = n
	return hex.EncodeToString(b)
}

// counterFixture is a deploy followed by calls, in canonical order.
func counterFixture(t *testing.T) ([]Inscription, Address) {
	t.Helper()
	deployID := id32(1)
	addr := ContractAddress(hexb(t, deployID))
	call := func(n byte, h uint64, data string) Inscription {
		return Inscription{ID: id32(n), Issuer: devnetIssuer, Mime: Mime,
			Height: h, Index: 0, Content: hexb(t, data),
			Ext: ext(t, map[string]any{"op": "call", "to": addr.Hex()})}
	}
	return []Inscription{
		{ID: deployID, Issuer: devnetIssuer, Mime: Mime, Height: 100, Index: 0,
			Content: hexb(t, counterInit), Ext: ext(t, map[string]any{"op": "deploy"})},
		call(2, 101, "d09de08a"), // increment
		call(3, 102, "d09de08a"), // increment
		call(4, 103, "d09de08a"), // increment
	}, addr
}

// The end-to-end claim: contracts and calls are ordinary inscriptions, and
// replaying them in canonical order produces the state.
func TestDeployAndCallDerivesState(t *testing.T) {
	ins, addr := counterFixture(t)
	c := NewChain(69)
	if err := c.Apply(ins); err != nil {
		t.Fatal(err)
	}
	if c.Applied != 4 || c.Skipped != 0 {
		t.Fatalf("applied=%d skipped=%d, want 4/0", c.Applied, c.Skipped)
	}
	for i, r := range c.Receipts {
		if !r.Success {
			t.Fatalf("receipt %d (%s) failed: %s", i, r.Op, r.Err)
		}
	}
	if code := c.State.Code(addr); len(code) != 133 {
		t.Fatalf("deployed code is %d bytes, want 133", len(code))
	}
	if got := c.State.Load(addr, big.NewInt(0)); got.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("counter = %s, want 3", got)
	}
	// The last increment must have returned the new value and logged it.
	last := c.Receipts[3]
	if got := new(big.Int).SetBytes(last.Output); got.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("increment returned %s, want 3", got)
	}
	if len(last.Logs) != 1 {
		t.Fatalf("got %d logs, want 1", len(last.Logs))
	}
	if got := hex.EncodeToString(last.Logs[0].Topics[0]); got != incrementedTopic {
		t.Fatalf("topic = %s, want %s", got, incrementedTopic)
	}
}

func TestCallWithArgumentAndReadBack(t *testing.T) {
	ins, addr := counterFixture(t)
	// add(39) -> 3 + 39 = 42, then get() must return it
	arg := hex.EncodeToString(bytes32(big.NewInt(39)))
	ins = append(ins,
		Inscription{ID: id32(5), Issuer: devnetIssuer, Mime: Mime, Height: 104,
			Content: hexb(t, "1003e2d2"+arg),
			Ext:     ext(t, map[string]any{"op": "call", "to": addr.Hex()})},
		Inscription{ID: id32(6), Issuer: devnetIssuer, Mime: Mime, Height: 105,
			Content: hexb(t, "6d4ce63c"),
			Ext:     ext(t, map[string]any{"op": "call", "to": addr.Hex()})},
	)
	c := NewChain(69)
	if err := c.Apply(ins); err != nil {
		t.Fatal(err)
	}
	got := new(big.Int).SetBytes(c.Receipts[len(c.Receipts)-1].Output)
	if got.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("get() = %s, want 42", got)
	}
}

// An unknown selector reverts, and a reverted call must leave no trace beyond
// its receipt — including not bumping the counter.
func TestRevertedCallIsRecordedButChangesNothing(t *testing.T) {
	ins, addr := counterFixture(t)
	ins = append(ins, Inscription{ID: id32(9), Issuer: devnetIssuer, Mime: Mime,
		Height: 110, Content: hexb(t, "deadbeef"),
		Ext: ext(t, map[string]any{"op": "call", "to": addr.Hex()})})
	c := NewChain(69)
	if err := c.Apply(ins); err != nil {
		t.Fatal(err)
	}
	last := c.Receipts[len(c.Receipts)-1]
	if last.Success {
		t.Fatal("call with an unknown selector succeeded")
	}
	if got := c.State.Load(addr, big.NewInt(0)); got.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("counter = %s after a revert, want 3", got)
	}
}

// The property the whole design rests on: canonical order, not arrival order,
// decides the state. Two derivers handed the same inscriptions in different
// orders must agree.
func TestDerivationIsOrderIndependentAndDeterministic(t *testing.T) {
	ins, _ := counterFixture(t)

	a := NewChain(69)
	if err := a.Apply(ins); err != nil {
		t.Fatal(err)
	}

	shuffled := []Inscription{ins[3], ins[1], ins[0], ins[2]}
	b := NewChain(69)
	if err := b.Apply(shuffled); err != nil {
		t.Fatal(err)
	}

	if a.State.Root() != b.State.Root() {
		t.Fatalf("derivers disagree:\n a %s\n b %s", a.State.Root(), b.State.Root())
	}
	if a.State.Root() == NewState().Root() {
		t.Fatal("root equals the empty state; the fixture did nothing")
	}
}

// Inscriptions that are not EVM transactions must be ignored rather than
// breaking the replay — the same rule the indexer applies to unknown opcodes.
func TestNonEvmInscriptionsAreIgnored(t *testing.T) {
	ins, _ := counterFixture(t)
	noise := []Inscription{
		{ID: id32(20), Issuer: devnetIssuer, Mime: "image/png", Height: 100, Index: 1,
			Content: []byte{0x89, 'P', 'N', 'G'}},
		{ID: id32(21), Issuer: devnetIssuer, Mime: Mime, Height: 100, Index: 2,
			Content: []byte{1}, Ext: nil}, // right mime, no evm ext
		{ID: id32(22), Issuer: devnetIssuer, Mime: Mime, Height: 100, Index: 3,
			Content: []byte{1}, Ext: ext(t, map[string]any{"op": "nonsense"})},
	}
	clean := NewChain(69)
	if err := clean.Apply(ins); err != nil {
		t.Fatal(err)
	}
	noisy := NewChain(69)
	if err := noisy.Apply(append(append([]Inscription{}, ins...), noise...)); err != nil {
		t.Fatal(err)
	}
	if clean.State.Root() != noisy.State.Root() {
		t.Fatal("non-EVM inscriptions changed the derived state")
	}
	if noisy.Skipped != 2 {
		t.Fatalf("skipped=%d, want 2 (the png is not counted, it is not this mime)", noisy.Skipped)
	}
}

// A NoM address is an EVM address without a derivation step, and the mapping
// must be injective or two inscribers could share an account.
func TestNomAddressMapsToEvmAddress(t *testing.T) {
	a, err := AddressOf(devnetIssuer)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AddressOf("z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two different NoM addresses mapped to one EVM address")
	}
	again, _ := AddressOf(devnetIssuer)
	if a != again {
		t.Fatal("mapping is not deterministic")
	}
	if _, err := AddressOf("not-an-address"); err == nil {
		t.Fatal("garbage parsed as an address")
	}
}

// Gas is charged per inscription, so one hostile contract cannot make replay
// unbounded for everyone else.
func TestGasCapBoundsOneInscription(t *testing.T) {
	loop := "5b60005600" // JUMPDEST PUSH1 00 JUMP
	deployID := id32(30)
	addr := ContractAddress(hexb(t, deployID))
	c := NewChain(69)
	c.GasCap = 50_000
	err := c.Apply([]Inscription{
		{ID: deployID, Issuer: devnetIssuer, Mime: Mime, Height: 1,
			Content: hexb(t, "600580600b6000396000f3"+loop),
			Ext:     ext(t, map[string]any{"op": "deploy"})},
		{ID: id32(31), Issuer: devnetIssuer, Mime: Mime, Height: 2, Content: nil,
			Ext: ext(t, map[string]any{"op": "call", "to": addr.Hex()})},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := c.Receipts[len(c.Receipts)-1]
	if last.Success {
		t.Fatal("an infinite loop reported success")
	}
	if last.GasUsed > c.GasCap {
		t.Fatalf("gas used %d exceeded the cap %d", last.GasUsed, c.GasCap)
	}
}

// The budget must be proportional to what NoM charged, at every size. A ratio
// that varies with size hands an attacker an optimal shape to aim at, which is
// exactly what the byte-linear alternative did.
func TestGasBudgetTracksPlasmaAtEverySize(t *testing.T) {
	c := NewChain(69)
	c.GasCap = 1 << 62 // lift the backstop so the ratio itself is under test
	for _, n := range []int{4, 68, 100, 751, 3334, 16384} {
		plasma := protocol.PlasmaForDataLength(n)
		got := float64(c.Budget(Inscription{Content: make([]byte, n)})) / float64(plasma)
		if got != float64(GasPerPlasma) {
			t.Fatalf("%d bytes: %.2f gas per plasma, want exactly %d", n, got, GasPerPlasma)
		}
	}
}

// A four-byte call must not be able to buy a large deriver's-time budget.
func TestTinyInscriptionCannotBuyTheWholeCap(t *testing.T) {
	c := NewChain(69)
	if b := c.Budget(Inscription{Content: make([]byte, 4)}); b >= c.GasCap {
		t.Fatalf("a 4-byte inscription got %d gas against a cap of %d", b, c.GasCap)
	}
	// And the cap still binds somewhere, or it is not a backstop.
	if c.Budget(Inscription{Content: make([]byte, 1<<20)}) != c.GasCap {
		t.Fatalf("the cap did not bind on a huge inscription")
	}
}

// An inscription may lower its own ceiling but never raise it: the budget is
// what the chain was paid for.
func TestDeclaredGasCannotExceedTheBudget(t *testing.T) {
	deployID := id32(1)
	addr := ContractAddress(hexb(t, deployID))
	c := NewChain(69)
	// A contract that loops forever, called with a 4-byte selector that asks
	// for far more gas than four bytes of plasma paid for.
	if err := c.Apply([]Inscription{
		{ID: deployID, Issuer: devnetIssuer, Mime: Mime, Height: 1,
			Content: hexb(t, "600580600b6000396000f35b60005600"),
			Ext:     ext(t, map[string]any{"op": "deploy"})},
		{ID: id32(2), Issuer: devnetIssuer, Mime: Mime, Height: 2,
			Content: hexb(t, "deadbeef"),
			Ext: ext(t, map[string]any{
				"op": "call", "to": addr.Hex(), "gas": uint64(9_000_000),
			})},
	}); err != nil {
		t.Fatal(err)
	}
	last := c.Receipts[len(c.Receipts)-1]
	if last.Success {
		t.Fatal("the infinite loop reported success")
	}
	if want := c.Budget(Inscription{Content: make([]byte, 4)}); last.GasUsed > want {
		t.Fatalf("burned %d gas on a 4-byte inscription entitled to %d", last.GasUsed, want)
	}
}

// Memory must be quadratically priced, or a gas budget converts into an
// unbounded allocation on every deriver. Nine bytes of code once reached 32 MB.
func TestMemoryExpansionIsQuadraticallyPriced(t *testing.T) {
	linear := func(words uint64) uint64 { return 3 * words }
	for _, w := range []uint64{1 << 10, 1 << 15, 1 << 20} {
		if memoryGas(w) <= linear(w) {
			t.Fatalf("%d words priced at %d, no better than linear %d", w, memoryGas(w), linear(w))
		}
	}
	// A 10M budget must not reach anywhere near 100 MB.
	var words uint64
	for memoryGas(words+1) <= 10_000_000 {
		words++
	}
	if mb := float64(words*32) / (1 << 20); mb > 4 {
		t.Fatalf("a 10M budget reaches %.1f MB of memory, want a few MB at most", mb)
	}
}

// --- the fee, and what congestion does to it ---

// feeIns is an inscription that burned `amount` raw ZNN to the protocol sink,
// on a carrier block that met resource price `scaled` (in thousandths).
func feeIns(n byte, height uint64, content []byte, amount string, scaled uint64) Inscription {
	base := protocol.PlasmaForDataLength(len(content))
	return Inscription{
		ID: id32(n), Issuer: devnetIssuer, Mime: Mime, Height: height,
		Timestamp: mainnetRefTimestamp, Content: content,
		Recipient: protocol.BurnAddress.String(), Amount: amount,
		TokenStandard: ZnnStandard,
		BasePlasma:    base,
		FusedPlasma:   base * scaled / protocol.PriceScaleFactor,
	}
}

// The two limits compose: the plasma term bounds the bytes, the fee term prices
// them, and the smaller wins. Congestion moves only the second.
func TestCongestionShrinksTheFeeBudgetAndNotThePlasmaOne(t *testing.T) {
	content := make([]byte, 100)
	plasmaOnly := protocol.PlasmaForDataLength(len(content)) * GasPerPlasma

	c := NewChain(69)
	c.FeeFrom = 1

	// A burn large enough that the plasma term binds at the floor price.
	huge := c.Supply.Fee(plasmaOnly*4, ZnnStandard, c.Supply.EpochAt(mainnetRefTimestamp),
		ResourcePrice{}).String()

	if got := c.Budget(feeIns(1, 10, content, huge, 1000)); got != plasmaOnly {
		t.Fatalf("at the floor price a well-funded call got %d gas, want the plasma bound %d",
			got, plasmaOnly)
	}
	// The same burn on a 10x congested block buys a tenth as much gas, which is
	// now the binding limit.
	busy := c.Budget(feeIns(2, 11, content, huge, 10_000))
	if busy >= plasmaOnly {
		t.Fatalf("10x congestion still allowed the full plasma budget of %d", plasmaOnly)
	}
	if want := plasmaOnly * 4 / 10; busy < want-2 || busy > want+2 {
		t.Fatalf("10x congestion allowed %d gas, want ~%d", busy, want)
	}

	// And the plasma bound itself must not have moved: a call that burns
	// nothing at all is still limited by its bytes, congested or not.
	c.FeeFrom = 0
	for _, scaled := range []uint64{1000, 10_000} {
		if got := c.Budget(feeIns(3, 12, content, "0", scaled)); got != plasmaOnly {
			t.Fatalf("at price %d the plasma bound was %d, want %d", scaled, got, plasmaOnly)
		}
	}
}

// A fee paid to anyone other than the burn sink buys nothing, however large,
// and however uncongested the network was.
func TestOnlyABurnBuysGas(t *testing.T) {
	c := NewChain(69)
	c.FeeFrom = 1
	ins := feeIns(4, 10, make([]byte, 100), "100000000000", 1000)
	ins.Recipient = devnetIssuer
	if got := c.Budget(ins); got != 0 {
		t.Fatalf("a payment to an ordinary address bought %d gas, want 0", got)
	}
}

// The activation height is what makes turning the fee on safe, so it gets a
// test that states the property in those terms: the same inscription, paying
// nothing, is fully funded below the height and unfunded at it.
//
// This is not a detail. Inscriptions are permanent and cannot be re-signed, so
// a rule that applied retroactively would strand every call ever published —
// and it would strand them *silently*, because a call with no budget does not
// look different from one that reverted.
func TestFeeActivationHeightProtectsEarlierHistory(t *testing.T) {
	const activation = 500
	content := make([]byte, 100)
	plasmaOnly := protocol.PlasmaForDataLength(len(content)) * GasPerPlasma

	c := NewChain(69)
	c.FeeFrom = activation

	// Published before the rule, paying nothing: entitled to its full plasma
	// budget, exactly as it was when it was published.
	before := feeIns(1, activation-1, content, "0", 1000)
	if got := c.Budget(before); got != plasmaOnly {
		t.Fatalf("a pre-activation call got %d gas, want its plasma budget %d", got, plasmaOnly)
	}

	// Published at the rule, paying nothing: entitled to nothing.
	at := feeIns(2, activation, content, "0", 1000)
	if got := c.Budget(at); got != 0 {
		t.Fatalf("an unpaid call at the activation height got %d gas, want 0", got)
	}

	// And paying, at the same height: entitled to what it bought.
	fee := c.Supply.Fee(50_000, ZnnStandard, c.Supply.EpochAt(mainnetRefTimestamp), ResourcePrice{})
	paid := feeIns(3, activation, content, fee.String(), 1000)
	if got := c.Budget(paid); got < 49_000 || got > 51_000 {
		t.Fatalf("a call that burned for 50,000 gas got %d", got)
	}
}

// The activation height is a pinned constant per chain, never a flag: two
// derivers that disagree about it compute different budgets and fork.
func TestFeeActivationIsPinnedPerChain(t *testing.T) {
	// A chain with no entry has no fee rule, which is the safe default — a new
	// devnet must not start rejecting calls that paid nothing because nobody
	// has built a way to pay yet.
	if got := NewChain(12345).FeeFrom; got != 0 {
		t.Fatalf("an unknown chain has a fee rule from %d, want none", got)
	}
	// Mainnet is deliberately absent: switching it on there is a decision to
	// publish, not a value to fill in quietly.
	if got := FeeFromHeight[1]; got != 0 {
		t.Fatalf("mainnet has an activation height of %d — that is a governance decision", got)
	}
	if got := NewChain(69).FeeFrom; got == 0 {
		t.Fatal("the devnet lost its activation height")
	}
}
