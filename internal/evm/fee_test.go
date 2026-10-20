package evm

import (
	"math/big"
	"testing"
)

// The mainnet reading this curve is calibrated against, so a change to either
// constant fails here rather than silently repricing every call.
const (
	mainnetRefTimestamp = 1788991540 // momentum 14,154,189, chainId 1
	mainnetRefZnn       = "1366728966519283"
	mainnetRefQsr       = "3118999966129297"
)

func TestEpochMatchesGoZenonDerivation(t *testing.T) {
	c := MainnetSupply
	// go-zenon builds its epoch ticker from the genesis timestamp and a 24h
	// interval, so the epoch is (t - genesis) / 86400 — a timestamp question,
	// not a height one.
	if got := c.EpochAt(mainnetRefTimestamp); got != c.RefEpoch {
		t.Fatalf("epoch at the reference reading = %d, want RefEpoch %d", got, c.RefEpoch)
	}
	if got := c.EpochAt(MainnetGenesisTimestamp); got != 0 {
		t.Fatalf("genesis is epoch %d, want 0", got)
	}
	// One second before the next day rolls over is still the same epoch.
	base := MainnetGenesisTimestamp + 100*EpochSeconds
	if c.EpochAt(base) != 100 || c.EpochAt(base+EpochSeconds-1) != 100 || c.EpochAt(base+EpochSeconds) != 101 {
		t.Fatal("epoch boundaries are off by one")
	}
	// Anything before genesis clamps rather than underflowing a uint64.
	if got := c.EpochAt(MainnetGenesisTimestamp - 10_000); got != 0 {
		t.Fatalf("a pre-genesis timestamp gave epoch %d, want 0", got)
	}
}

func TestSupplyIsExactAtTheReferenceEpoch(t *testing.T) {
	znn, qsr := MainnetSupply.SupplyAt(MainnetSupply.RefEpoch)
	if znn.String() != mainnetRefZnn {
		t.Fatalf("ZNN at RefEpoch = %s, want the mainnet reading %s", znn, mainnetRefZnn)
	}
	if qsr.String() != mainnetRefQsr {
		t.Fatalf("QSR at RefEpoch = %s, want the mainnet reading %s", qsr, mainnetRefQsr)
	}
}

// Both tokens inflate forever at the tail rate, because the reward tables clamp
// their tick index rather than ending. A year past the reference must add
// exactly 365 epochs of tail emission.
func TestSupplyGrowsAtTheTailRate(t *testing.T) {
	c := MainnetSupply
	z0, q0 := c.SupplyAt(c.RefEpoch)
	z1, q1 := c.SupplyAt(c.RefEpoch + 365)

	wantZnn := new(big.Int).Mul(big.NewInt(365), big.NewInt(4320*tokenDecimals))
	wantQsr := new(big.Int).Mul(big.NewInt(365), big.NewInt(5000*tokenDecimals))

	if got := new(big.Int).Sub(z1, z0); got.Cmp(wantZnn) != 0 {
		t.Fatalf("a year added %s ZNN, want %s (4,320/epoch)", got, wantZnn)
	}
	if got := new(big.Int).Sub(q1, q0); got.Cmp(wantQsr) != 0 {
		t.Fatalf("a year added %s QSR, want %s (5,000/epoch)", got, wantQsr)
	}
}

// The early schedule must be honoured too, or a devnet curve calibrated near
// genesis would be wrong. Epoch 0-29 is tick 0: 10 * 8640/6 = 14,400 ZNN.
func TestSupplyHonoursTheEarlyTicks(t *testing.T) {
	c := SupplyCurve{GenesisTimestamp: 0, RefEpoch: 0,
		RefZnn: new(big.Int), RefQsr: new(big.Int)}
	znn, qsr := c.SupplyAt(30) // exactly one tick
	wantZnn := new(big.Int).Mul(big.NewInt(30), big.NewInt(14400*tokenDecimals))
	wantQsr := new(big.Int).Mul(big.NewInt(30), big.NewInt(20000*tokenDecimals))
	if znn.Cmp(wantZnn) != 0 {
		t.Fatalf("tick 0 emitted %s ZNN, want %s", znn, wantZnn)
	}
	if qsr.Cmp(wantQsr) != 0 {
		t.Fatalf("tick 0 emitted %s QSR, want %s", qsr, wantQsr)
	}
	// And crossing into tick 1 must change the rate.
	z31, _ := c.SupplyAt(31)
	step := new(big.Int).Sub(z31, znn)
	if step.Cmp(big.NewInt(8640*tokenDecimals)) != 0 {
		t.Fatalf("epoch 30 emitted %s ZNN, want 8,640 (tick 1)", step)
	}
}

// Fee and GasFor must round in opposite directions, or a caller could extract
// more gas than they paid for by round-tripping — at every congestion level,
// since the congestion term adds a second division that could round the wrong
// way on its own.
func TestFeeAndGasForNeverFavourTheCaller(t *testing.T) {
	c := MainnetSupply
	e := c.RefEpoch
	prices := []ResourcePrice{
		{},                              // no plasma information at all
		{Base: 21000, Paid: 21000},      // uncongested
		{Base: 21000, Paid: 52500},      // 2.5x
		{Base: 22088, Paid: 61234},      // an awkward, non-dividing ratio
		{Base: 21000, Paid: 21_000_000}, // 1000x
	}
	for _, price := range prices {
		for _, gas := range []uint64{1, 7, 6274, 12652, 147426, 9_080_896} {
			for _, zts := range []string{ZnnStandard, QsrStandard} {
				fee := c.Fee(gas, zts, e, price)
				back := c.GasFor(fee, zts, e, price)
				if back < gas {
					t.Fatalf("%v %s: paying the quoted fee for %d gas bought only %d",
						price, zts, gas, back)
				}
				// And a single unit less must not still buy it.
				if fee.Sign() > 0 {
					short := new(big.Int).Sub(fee, big.NewInt(1))
					if c.GasFor(short, zts, e, price) >= gas {
						t.Fatalf("%v %s: %d gas was affordable one unit below the quoted fee",
							price, zts, gas)
					}
				}
			}
		}
	}
}

// Paying in QSR must cost the supply ratio more, without a ratio appearing in
// the arithmetic: each token's fee is the same fraction of its own supply.
func TestQsrCostsTheSupplyRatioMore(t *testing.T) {
	c := MainnetSupply
	e := c.RefEpoch
	const gas = 1_000_000
	znnFee := c.Fee(gas, ZnnStandard, e, ResourcePrice{})
	qsrFee := c.Fee(gas, QsrStandard, e, ResourcePrice{})
	znnSupply, qsrSupply := c.SupplyAt(e)

	// qsrFee/znnFee must equal qsrSupply/znnSupply, cross-multiplied to stay
	// in integers.
	lhs := new(big.Int).Mul(qsrFee, znnSupply)
	rhs := new(big.Int).Mul(znnFee, qsrSupply)
	diff := new(big.Int).Sub(lhs, rhs)
	diff.Abs(diff)
	// Allow one unit of rounding slack at each end.
	slack := new(big.Int).Add(znnSupply, qsrSupply)
	if diff.Cmp(slack) > 0 {
		t.Fatalf("QSR fee is not the supply ratio of the ZNN fee: %s vs %s", qsrFee, znnFee)
	}
}

// An unrecognised token buys nothing. Anything else would let a worthless ZTS
// stand in for a burned fee.
func TestUnknownTokenBuysNoGas(t *testing.T) {
	c := MainnetSupply
	huge, _ := new(big.Int).SetString("999999999999999999999", 10)
	if got := c.GasFor(huge, "zts1someothertokenxxxxxxxx", c.RefEpoch, ResourcePrice{}); got != 0 {
		t.Fatalf("an unknown ZTS bought %d gas, want 0", got)
	}
	if got := c.Fee(1000, "zts1someothertokenxxxxxxxx", c.RefEpoch, ResourcePrice{}); got.Sign() != 0 {
		t.Fatalf("an unknown ZTS was quoted a fee of %s, want 0", got)
	}
}

// The headline price, so a change to GasPerSupply is visible rather than
// discovered later in an invoice.
func TestPriceLevelIsWhatTheDesignSays(t *testing.T) {
	c := MainnetSupply
	e := c.RefEpoch

	// An ERC-20 transfer measured at 12,652 gas should cost ~0.00127 ZNN.
	fee := c.Fee(12652, ZnnStandard, e, ResourcePrice{})
	lo, hi := big.NewInt(120_000), big.NewInt(135_000) // raw units, 8 decimals
	if fee.Cmp(lo) < 0 || fee.Cmp(hi) > 0 {
		t.Fatalf("an ERC-20 transfer costs %s raw ZNN, want ~126,520", fee)
	}

	// And burning the whole supply should buy about GasPerSupply gas.
	znn, _ := c.SupplyAt(e)
	if got := c.GasFor(znn, ZnnStandard, e, ResourcePrice{}); got != uint64(GasPerSupply) {
		t.Fatalf("the whole ZNN supply bought %d gas, want GasPerSupply %d", got, GasPerSupply)
	}
}

// --- the dynamic-plasma congestion term ---

// The multiplier is read off the block's own payment, and a block that pays
// exactly its nominal base plasma met the floor price.
func TestResourcePriceIsReadOffWhatTheBlockPaid(t *testing.T) {
	base := uint64(21000 + 68*100) // a 100-byte inscription
	for _, tc := range []struct {
		name              string
		fused, difficulty uint64
		want              uint64
	}{
		{"fully fused at the floor", base, 0, 1000},
		{"fully fused at 2.5x", base * 5 / 2, 0, 2500},
		// PoW is priced through difficulty/1500, so paying the whole base
		// plasma in work meets the floor exactly the way fusing it does.
		{"pure proof of work", 0, base * 1500, 1000},
		// The two resources add, so a block that splits its payment between
		// them meets the same price as one that pays it all either way.
		{"half fused, half worked", base / 2, base / 2 * 1500, 1000},
		{"mixed, congested", base, base * 1500, 2000},
		// Absent fields are not a discount.
		{"no plasma information", 0, 0, 1000},
	} {
		got := PriceOf(base, tc.fused, tc.difficulty).Scaled()
		if got != tc.want {
			t.Errorf("%s: price = %d, want %d", tc.name, got, tc.want)
		}
	}
	// A block with no base plasma at all — an embedded or unrecognised block —
	// cannot be priced and must fall to the floor rather than divide by zero.
	if got := PriceOf(0, 999999, 0).Scaled(); got != 1000 {
		t.Errorf("a block with no base plasma priced at %d, want the floor", got)
	}
}

// Underpayment is not a discount. Consensus will not accept such a block, but a
// deriver must not price one cheaply if it ever sees one.
func TestUnderpaymentPricesAtTheFloor(t *testing.T) {
	if p := (ResourcePrice{Base: 21000, Paid: 1}); p.Scaled() != 1000 || p.Congested() {
		t.Fatalf("an underpaying block priced at %d, want the floor", p.Scaled())
	}
}

// The headline property: congestion multiplies the fee, exactly.
func TestCongestionMultipliesTheFee(t *testing.T) {
	c := MainnetSupply
	e := c.RefEpoch
	const gas = 1_000_000

	floor := c.Fee(gas, ZnnStandard, e, ResourcePrice{Base: 21000, Paid: 21000})
	busy := c.Fee(gas, ZnnStandard, e, ResourcePrice{Base: 21000, Paid: 21000 * 4})

	want := new(big.Int).Mul(floor, big.NewInt(4))
	diff := new(big.Int).Sub(busy, want)
	diff.Abs(diff)
	if diff.Cmp(big.NewInt(4)) > 0 { // ceil() at each end
		t.Fatalf("4x congestion cost %s, want ~%s (4 x %s)", busy, want, floor)
	}

	// And an uncongested block must be priced identically to one carrying no
	// plasma information at all — that equality is what lets this rule apply
	// to the whole history without an activation height of its own.
	if c.Fee(gas, ZnnStandard, e, ResourcePrice{}).Cmp(floor) != 0 {
		t.Fatal("the floor price is not the same as no price at all")
	}
}

// Congestion must shrink the gas a fixed burn buys, or the fee term would stop
// binding exactly when the network most needs it to.
func TestCongestionShrinksWhatABurnBuys(t *testing.T) {
	c := MainnetSupply
	e := c.RefEpoch
	burn, _ := new(big.Int).SetString("1000000000", 10) // 10 ZNN

	cheap := c.GasFor(burn, ZnnStandard, e, ResourcePrice{Base: 21000, Paid: 21000})
	dear := c.GasFor(burn, ZnnStandard, e, ResourcePrice{Base: 21000, Paid: 21000 * 4})
	if dear >= cheap {
		t.Fatalf("10 ZNN bought %d gas at 4x congestion and %d at the floor", dear, cheap)
	}
	if got, want := cheap/4, dear; got > want+1 || want > got+1 {
		t.Fatalf("4x congestion bought %d gas, want ~%d (a quarter of %d)", dear, got, cheap)
	}
}
