package evm

import (
	"math/big"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// The burned fee.
//
// An EVM call may pay for its own computation by destroying ZNN or QSR. The
// price of one unit of gas is the product of two terms, and they answer
// different questions:
//
//	price(gas) = supply(token, epoch) / GasPerSupply     <- the long run
//	           x resourcePrice / PriceScaleFactor        <- right now
//
// The first is a fixed fraction of the token's own supply, so the fee keeps its
// real value as NoM's perpetual emission inflates it. The second is NoM's own
// dynamic-plasma resource price, so gas costs more exactly when the network's
// bandwidth is scarce — an EIP-1559 base fee, except that the network already
// computes one and this borrows it rather than inventing a second congestion
// signal over a different resource.
//
// The design and its economics are in research/evm-fee-design.md; this file is
// the arithmetic.
//
// Two properties are load-bearing and everything here is shaped by them:
//
//   - **Determinism.** Two derivers replaying the same inscription years apart,
//     offline, must charge the same fee. That rules out a price feed, a DEX
//     quote, or any observed value — so the fee is computed from NoM's own
//     emission schedule, which is a pure function of the epoch. It also rules
//     out floating point: every value below is an integer or a big.Int, for the
//     same reason CosmWasm bans floats in contracts.
//
//   - **No exchange rate.** The obvious design computes a ZNN price and then a
//     QSR price via a ratio. This does not: each token's fee is the same
//     fraction of *its own* supply, so the ratio is emergent rather than
//     computed and there is no rate to get wrong. Paying in QSR costs
//     supplyQSR/supplyZNN times as many units, which today is ~2.28, without
//     that number appearing anywhere.

// The NoM emission schedule, mirrored from go-zenon
// vm/constants/embedded.go. Mirrored rather than imported because a deriver
// must be able to compute a fee from a timestamp alone, with no node and no
// consensus dependency — the same reason decodeBech32Address is local.
//
// If go-zenon ever changes these, the fee schedule changes with them and that
// is a consensus event for this metaprotocol, not a silent follow.
const (
	// MainnetGenesisTimestamp is chain/genesis/embedded_genesis_string.go:
	// 2021-11-24 12:00 UTC.
	MainnetGenesisTimestamp int64 = 1637755200

	// EpochSeconds is consensus.EpochDuration. An epoch is a day, and an
	// epoch — not a momentum height — is what the reward schedule is indexed
	// by, because go-zenon derives it from the momentum's timestamp.
	EpochSeconds int64 = 24 * 60 * 60

	// RewardTickEpochs is constants.RewardTickDurationInEpochs.
	RewardTickEpochs uint64 = 30

	// tokenDecimals is constants.Decimals: both ZNN and QSR carry 8.
	tokenDecimals int64 = 100_000_000

	momentumsPerEpoch int64 = 3600 / 10 * 24 // 8640
)

// Per-epoch emission, in raw units. Past the end of each table the last entry
// repeats forever — NetworkZnnRewardPerEpoch clamps the tick index — so both
// tokens inflate perpetually and neither has a cap.
var (
	znnRewardPerEpoch = []int64{
		10 * momentumsPerEpoch / 6 * tokenDecimals,
		6 * momentumsPerEpoch / 6 * tokenDecimals,
		5 * momentumsPerEpoch / 6 * tokenDecimals,
		7 * momentumsPerEpoch / 6 * tokenDecimals,
		5 * momentumsPerEpoch / 6 * tokenDecimals,
		4 * momentumsPerEpoch / 6 * tokenDecimals,
		7 * momentumsPerEpoch / 6 * tokenDecimals,
		4 * momentumsPerEpoch / 6 * tokenDecimals,
		3 * momentumsPerEpoch / 6 * tokenDecimals,
		7 * momentumsPerEpoch / 6 * tokenDecimals,
		3 * momentumsPerEpoch / 6 * tokenDecimals, // tail: 4,320 ZNN/epoch
	}
	qsrRewardPerEpoch = []int64{
		20000 * tokenDecimals,
		20000 * tokenDecimals,
		20000 * tokenDecimals,
		20000 * tokenDecimals,
		15000 * tokenDecimals,
		15000 * tokenDecimals,
		15000 * tokenDecimals,
		5000 * tokenDecimals, // tail: 5,000 QSR/epoch
	}
)

// The NoM token standards a fee may be paid in.
const (
	ZnnStandard = "zts1znnxxxxxxxxxxxxx9z4ulx"
	QsrStandard = "zts1qsrxxxxxxxxxxxxxmrhjll"
)

// GasPerSupply is the policy knob, and the only one: how much gas the *entire
// supply* of a token buys if it were all burned.
//
// It is dimensionless, so the fee stays a constant fraction of supply as supply
// inflates — a fee fixed in coins would get cheaper in real terms every year,
// because ZNN inflates 11.5% annually today and the schedule never stops.
//
// At 1.37e14 gas, and the measured ~55,000 gas/ms of deriver CPU, **burning the
// whole ZNN supply buys about 29 days of node compute.** That sets today's
// price at ~10 raw units (1e-7 ZNN) per gas: an ERC-20 transfer costs
// 0.00127 ZNN, and a saturated chain under a per-momentum ceiling would burn
// ~12.7% of supply per year against emission of 11.5% and falling.
const GasPerSupply int64 = 137_000_000_000_000

// SupplyCurve is the protocol's declared supply, not an observation of it.
//
// The distinction is deliberate. Real supply also moves by small sinks the
// emission schedule does not model — the 1 ZNN token-issuance fee, accelerator
// flows — so a fee that tried to track *actual* supply would need archive state
// and would drift between implementations. Declaring the curve makes it exact
// by definition, computable offline, and identical for every deriver, while
// tracking reality closely because emission dominates everything else.
//
// RefZnn and RefQsr are the calibration: the supply at RefEpoch, pinned once
// from mainnet. Everything before RefEpoch is not modelled and not needed.
type SupplyCurve struct {
	GenesisTimestamp int64
	RefEpoch         uint64
	RefZnn           *big.Int // raw units at the start of RefEpoch
	RefQsr           *big.Int
}

// MainnetSupply is the calibration for NoM mainnet, read from
// node.zenonhub.io on 2026-09-09 at momentum 14,154,189 (chainId 1):
//
//	embedded.token.getByZts zts1znnxxxxxxxxxxxxx9z4ulx -> 1366728966519283  (13,667,289.665 ZNN)
//	embedded.token.getByZts zts1qsrxxxxxxxxxxxxxmrhjll -> 3118999966129297  (31,189,999.661 QSR)
//	ledger.getFrontierMomentum                          -> timestamp 1788991540 -> epoch 1750
//
// Both tokens report maxSupply 9007199254740991 — 2^53-1, the "no cap"
// sentinel — which is the chain confirming what the schedule already says:
// emission never stops.
//
// These three numbers are consensus values for this metaprotocol. Every deriver
// must use the same ones or they compute different fees and fork, so they
// belong in the spec beside the burn address rather than in a config file.
//
// The anchor is exact at RefEpoch and carried forward by the schedule, so it
// does not matter that emission alone does not explain the whole supply
// (it accounts for 8.77M of the 13.67M ZNN; the rest is the genesis allocation
// and the legacy swap). What matters is that the curve is right *from here on*,
// and from here on emission is the only term.
var MainnetSupply = SupplyCurve{
	GenesisTimestamp: MainnetGenesisTimestamp,
	RefEpoch:         1750,
	RefZnn:           mustBig("1366728966519283"),
	RefQsr:           mustBig("3118999966129297"),
}

func mustBig(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("evm: bad supply constant " + s)
	}
	return n
}

// EpochAt is the epoch a momentum timestamp falls in.
//
// go-zenon derives this from the timestamp rather than the height
// (consensus.go builds its epoch ticker from the genesis time), so a deriver
// needs the momentum's timestamp and nothing else.
func (c SupplyCurve) EpochAt(unixSeconds int64) uint64 {
	if unixSeconds <= c.GenesisTimestamp {
		return 0
	}
	return uint64((unixSeconds - c.GenesisTimestamp) / EpochSeconds)
}

// SupplyAt returns the declared ZNN and QSR supply at an epoch, in raw units.
func (c SupplyCurve) SupplyAt(epoch uint64) (znn, qsr *big.Int) {
	znn = new(big.Int).Set(c.RefZnn)
	qsr = new(big.Int).Set(c.RefQsr)
	if epoch <= c.RefEpoch {
		return znn, qsr
	}
	znn.Add(znn, sumRewards(c.RefEpoch, epoch, znnRewardPerEpoch))
	qsr.Add(qsr, sumRewards(c.RefEpoch, epoch, qsrRewardPerEpoch))
	return znn, qsr
}

// sumRewards totals a schedule over [from, to).
//
// The tail is closed-form rather than iterated: past the last table entry the
// rate is constant forever, so a mainnet epoch thousands of ticks out costs the
// same work as one near genesis. Only the pre-tail span is walked, and that is
// bounded by the table length — 330 epochs at most.
func sumRewards(from, to uint64, cfg []int64) *big.Int {
	total := new(big.Int)
	if to <= from || len(cfg) == 0 {
		return total
	}
	tailStart := uint64(len(cfg)-1) * RewardTickEpochs
	for e := from; e < to && e < tailStart; e++ {
		total.Add(total, big.NewInt(cfg[e/RewardTickEpochs]))
	}
	lo := from
	if tailStart > lo {
		lo = tailStart
	}
	if to > lo {
		n := new(big.Int).SetUint64(to - lo)
		total.Add(total, n.Mul(n, big.NewInt(cfg[len(cfg)-1])))
	}
	return total
}

// supplyOf picks the curve for a token standard. An unknown standard has no
// supply and therefore buys no gas, which is the safe direction: a fee paid in
// some other ZTS is worth nothing rather than worth anything.
func (c SupplyCurve) supplyOf(zts string, epoch uint64) *big.Int {
	znn, qsr := c.SupplyAt(epoch)
	switch zts {
	case ZnnStandard:
		return znn
	case QsrStandard:
		return qsr
	default:
		return new(big.Int)
	}
}

// ResourcePrice is the dynamic-plasma congestion multiplier for one
// inscription, held as the exact rational Paid/Base rather than as a price.
//
// **It is read off the carrier block instead of off the momentum header, and
// that is the whole trick.** Consensus requires a block to pay
//
//	ceil(basePlasma x fusionPrice / PriceScaleFactor)
//
// of fused plasma (or the PoW equivalent, or a mix), so the ratio of what a
// block *paid* to what its bytes nominally cost already is the price it met. No
// momentum field is needed, mixed fused/PoW payment falls out of the sum
// instead of needing a rule, and the two numbers are hash-committed — a
// deriver holding the block can reach this conclusion with no node at all.
//
// Base is deliberately not the block's own `basePlasma` field, which is outside
// the hash; the indexer recomputes it from the data length.
//
// Overpaying plasma raises Paid and so raises the payer's own fee. That is the
// safe direction and the reason this is trustworthy: the multiplier can only be
// pushed *up* by its own payer, never ground down, so the cheapest valid block
// is the one that pays exactly the network price — and that is what everyone
// will publish.
type ResourcePrice struct {
	Base uint64 // nominal plasma for the block's bytes: 21000 + 68n
	Paid uint64 // fused plasma + the plasma its proof of work stood in for
}

// PriceOf reads the multiplier off a block's committed plasma fields.
func PriceOf(basePlasma, fusedPlasma, difficulty uint64) ResourcePrice {
	return ResourcePrice{
		Base: basePlasma,
		Paid: fusedPlasma + protocol.DifficultyToPlasma(difficulty),
	}
}

// ratio is the multiplier as num/den, never below 1.
//
// The floor is what makes this rule safe to apply to the whole history at once,
// with no activation height of its own: a pre-spork block, a block from a node
// that reports no plasma at all, and a block on an uncongested network all
// yield 1 and are priced exactly as they were before dynamic plasma existed.
// A multiplier below 1 is not a discount the network ever offers — it means the
// fields are missing or the block underpaid — and treating it as one would let
// a malformed row buy gas cheaply.
func (p ResourcePrice) ratio() (num, den uint64) {
	if p.Base == 0 || p.Paid <= p.Base {
		return 1, 1
	}
	return p.Paid, p.Base
}

// Scaled is the multiplier expressed the way go-zenon quotes a resource price:
// in units of protocol.PriceScaleFactor, so 1000 is an uncongested network and
// 2500 is 2.5x. For display and tooling; the arithmetic below never rounds
// through it.
func (p ResourcePrice) Scaled() uint64 {
	num, den := p.ratio()
	return num * protocol.PriceScaleFactor / den
}

// Congested reports whether this block met a price above the floor.
func (p ResourcePrice) Congested() bool { num, den := p.ratio(); return num > den }

// Fee is what `gas` costs, in raw units of `zts`, at `epoch`, for a block that
// met resource price `price`.
//
//	fee = ceil(gas * supply(zts, epoch) * priceNum / (GasPerSupply * priceDen))
//
// Rounded up, so that no amount of gas is ever free through truncation. The
// congestion term multiplies the numerator, so a zero-value ResourcePrice is
// exactly the pre-dynamic-plasma fee — the reason callers with no plasma facts
// may pass one.
func (c SupplyCurve) Fee(gas uint64, zts string, epoch uint64, price ResourcePrice) *big.Int {
	supply := c.supplyOf(zts, epoch)
	if supply.Sign() == 0 || gas == 0 {
		return new(big.Int)
	}
	num, den := price.ratio()
	n := new(big.Int).Mul(new(big.Int).SetUint64(gas), supply)
	n.Mul(n, new(big.Int).SetUint64(num))
	d := new(big.Int).Mul(big.NewInt(GasPerSupply), new(big.Int).SetUint64(den))
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// GasFor is the inverse: how much gas a burned amount buys.
//
//	gas = floor(amount * GasPerSupply * priceDen / (supply(zts, epoch) * priceNum))
//
// Rounded down, for the same reason Fee rounds up — the pair must never let a
// caller extract more gas than they paid for. Under congestion the same burn
// buys proportionally less gas, which is the deriver-protecting direction: the
// busier the network, the less deriver CPU a given amount of destroyed value
// commands.
func (c SupplyCurve) GasFor(amount *big.Int, zts string, epoch uint64, price ResourcePrice) uint64 {
	supply := c.supplyOf(zts, epoch)
	if amount == nil || amount.Sign() <= 0 || supply.Sign() == 0 {
		return 0
	}
	num, den := price.ratio()
	gas := new(big.Int).Mul(amount, big.NewInt(GasPerSupply))
	gas.Mul(gas, new(big.Int).SetUint64(den))
	gas.Quo(gas, new(big.Int).Mul(supply, new(big.Int).SetUint64(num)))
	if !gas.IsUint64() {
		return ^uint64(0) // saturate; the caller's own cap still applies
	}
	return gas.Uint64()
}
