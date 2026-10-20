package evm

import (
	"encoding/hex"
	"math/big"
	"sort"

	"github.com/fxamacker/cbor/v2"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// Mime is the content type an EVM artifact declares. Contract code and call
// data are both raw bytes with no self-describing structure, so the operation
// cannot live in the mime — it lives in the extension lane below.
const Mime = "application/evm"

// The extension lane (spec R-2.9) is where this metaprotocol says what an
// inscription means.
//
// This is the seam that makes an EVM possible without touching Zenoglyphs
// consensus. R-2.9 defines ext as a bounded, ignorable map, and internal/api
// surfaces it verbatim while explicitly never interpreting it. So a Zenoglyphs
// indexer sees an ordinary `application/evm` mint and is exactly as correct as
// before; a deriver sees a contract deployment. Neither has to know about the
// other, and no opcode was added.
//
//	ext = {"evm": {"op": "deploy"}}                  content = init bytecode
//	ext = {"evm": {"op": "call", "to": "0x…"}}       content = calldata
type Op string

const (
	OpDeploy Op = "deploy"
	OpCall   Op = "call"
)

type extRoot struct {
	EVM *extEVM `cbor:"evm"`
}

type extEVM struct {
	Op    string `cbor:"op"`
	To    string `cbor:"to"`
	Gas   uint64 `cbor:"gas"`
	Value string `cbor:"value"` // decimal string; JSON numbers cannot hold 256 bits
}

// permissive is deliberately not the protocol's strict decoder. ext is a lane
// several metaprotocols may share, so an unknown sibling key must be skipped
// rather than rejected — that is what "ignorable" means.
var permissive, _ = cbor.DecOptions{}.DecMode()

// Inscription is the subset of a Zenoglyphs asset a deriver needs. It is an
// input type rather than a store query so that the deriver can be fed from the
// SQLite store, from the JSON API, or from a test fixture — the property that
// makes "anyone can derive" more than a slogan.
type Inscription struct {
	ID      string // asset id: the mint block's hash, hex
	Issuer  string // NoM address of the publisher
	Mime    string
	Height  uint64 // momentum height
	Index   int    // index within the momentum
	Content []byte
	Ext     []byte // canonical CBOR, verbatim from the envelope
	// Timestamp is the momentum's, in seconds. It feeds TIMESTAMP, so it has
	// to come from the chain rather than from the deriver's clock: two
	// derivers replaying the same history must see the same value, and
	// time.Now() is the one input guaranteed to differ between them.
	Timestamp int64

	// The carrier block's own value — the burned fee (see fee.go). Recipient
	// must be the protocol burn sink for the fee to count; a payment anywhere
	// else went to somebody rather than to nobody, and buys no gas.
	Recipient     string
	Amount        string // raw units, decimal
	TokenStandard string

	// The carrier block's plasma. BasePlasma is the nominal cost of its bytes,
	// recomputed from the data length by whoever built this; FusedPlasma and
	// Difficulty are what it actually paid, and are hash-committed. Their
	// ratio is the dynamic-plasma resource price the block met, which is the
	// congestion term in the fee (fee.go). All zero means "no dynamic-plasma
	// information", which prices as an uncongested network.
	BasePlasma  uint64
	FusedPlasma uint64
	Difficulty  uint64
}

// Price is the dynamic-plasma congestion multiplier this inscription's carrier
// block met.
func (i Inscription) Price() ResourcePrice {
	return PriceOf(i.BasePlasma, i.FusedPlasma, i.Difficulty)
}

// Receipt is the outcome of one EVM inscription.
type Receipt struct {
	Inscription string
	Op          Op
	From        Address
	To          Address // the contract deployed, or the contract called
	Success     bool
	GasUsed     uint64

	// Height and Timestamp are the carrying inscription's momentum — the
	// closest thing this derived chain has to a block, since every EVM
	// "transaction" here is one NoM inscription. Explorers group by Height
	// to show something block-shaped without this chain having its own.
	Height    uint64
	Timestamp int64
	// Input is the calldata verbatim. Kept on the receipt so that naming a
	// call needs no second lookup: a listing of a hundred transactions would
	// otherwise be a hundred queries to recover four bytes each.
	Input  []byte
	Output []byte
	Logs   []Log
	Err    string

	// GasBudget is what this inscription was actually entitled to — the
	// smaller of its plasma allowance and its burned fee. Recorded rather than
	// left for readers to recompute, because recomputing needs the carrier
	// value and the epoch; a reader holding only the receipt would otherwise
	// report the plasma limit as though no fee rule existed.
	GasBudget uint64

	// FeeBurned is the carrier block's value, set only when it went to the
	// protocol burn sink. Empty means nothing was burned for this call.
	FeeBurned string
	FeeToken  string

	// ResourcePrice is the dynamic-plasma congestion multiplier the carrier
	// block met, in units of protocol.PriceScaleFactor — 1000 is an
	// uncongested network. Recorded for the same reason GasBudget is: the
	// price is a property of the momentum this executed in and cannot be
	// recovered from the receipt afterwards, so a reader without it would
	// quietly report today's price for a call made under yesterday's.
	ResourcePrice uint64
}

// GasPerPlasma is how much EVM gas one unit of NoM plasma buys.
//
// This is the coupling between the two costs, and having one is the whole
// point. NoM charges an inscription for its *bytes*
// (plasma = 68n + 21000, protocol.PlasmaForDataLength); this EVM charges its
// *computation*. Nothing connected them, so the cheapest inscription bought
// the most compute per unit paid: a four-byte call to an expensive contract
// cost the minimum plasma and, under a flat cap, the maximum gas.
//
// Deriving the budget from the plasma the inscription actually cost makes the
// ratio constant by construction — 8.0 gas per plasma at four bytes and at
// sixteen kilobytes alike — so no inscription size is a better deal than any
// other and NoM's own pricing prices EVM compute for free.
//
// A byte-linear budget was the obvious alternative and is worse: plasma's
// 21,000-unit base term means gas/plasma would climb from 0.6 at four bytes to
// 40 at ~3.3 KB, handing an attacker an optimal size to aim at.
//
// The value: 8 is chosen so a full 16 KB block earns ~9.08M gas, just under
// GasCap, and so the ERC-20 deploy measured at 147,426 gas fits its 576,544
// budget with roughly 4x headroom. Raising it buys contracts more room and
// costs every deriver proportionally more replay.
const GasPerPlasma = 8

// QueryGas bounds a read-only call.
//
// Deliberately far below GasCap, because a query is not paid for by anyone: no
// inscription exists, no plasma was spent, and an explorer rendering one page
// may run dozens of them — a holder list calls balanceOf once per holder. At
// the full cap a single hostile token whose balanceOf loops would turn one page
// render into minutes of server CPU, which is a denial of service on the
// indexer rather than on the chain.
//
// A view function that cannot answer in 500k gas is not a view function.
const QueryGas = 500_000

// Chain is a derived EVM.
type Chain struct {
	State   *State
	ChainID uint64

	// GasCap is an absolute ceiling, applied after the plasma-derived budget.
	// With GasPerPlasma at 8 it binds only above ~16 KB, so it is a backstop
	// against a future GasPerPlasma rather than the primary limit.
	GasCap uint64

	// GasPerPlasma overrides the constant above, for experiments. Zero means
	// the constant.
	GasPerPlasma uint64

	// Supply is the declared supply curve the burned fee is priced against.
	Supply SupplyCurve

	// FeeFrom is the momentum height from which a burned fee is *required*.
	// Zero disables the rule entirely.
	//
	// An activation height rather than a flag day, because inscriptions are
	// permanent: every call published before this height was valid when it was
	// published and must stay valid on replay forever. This is the same shape
	// as the launch momentum in A-25 — a rule that begins, rather than a rule
	// that was always true and everyone must pretend it was.
	FeeFrom uint64

	Receipts []Receipt
	Applied  int
	Skipped  int
	Failed   int // receipts whose execution failed, kept so no reader has to count

	// byID indexes Receipts by inscription id. Apply maintains it; an
	// explorer answering /evm/tx/{id} for every poll of every client should
	// not scan the whole history to do it.
	byID   map[string]int
	Height uint64 // the highest momentum replayed, for NUMBER in queries
	Time   uint64 // its timestamp, for TIMESTAMP in queries
}

// FeeFromHeight is the momentum at which the burned fee becomes *required*,
// per chain.
//
// An activation height rather than a flag day, and pinned here rather than
// exposed as a flag, for the same reason every other consensus value in this
// project is: two derivers that disagree about it compute different budgets
// and fork. An operator who could set it with a command-line argument is an
// operator who can fork by typo.
//
// A chain absent from this map has no fee rule at all, which is the safe
// default — a new devnet does not silently start rejecting calls that paid
// nothing because nobody had built a way to pay yet.
//
// **Mainnet is deliberately absent.** Turning it on there is a decision to
// publish, not a value to fill in while nobody is looking; it needs a height
// far enough ahead that every publisher has upgraded, because every call
// published under the old rule stays valid on replay forever and every call
// published after it without a fee is dead on arrival.
var FeeFromHeight = map[uint64]uint64{
	// The docker devnet. 55_100 is a few minutes past the tip at which the
	// payment path was first built (55_008 on 2026-09-10), which is the whole
	// point of choosing it: everything inscribed before it — 63 EVM
	// transactions that had no way to pay a fee, because none existed — stays
	// exactly as valid as it was.
	69: 55_100,
}

func NewChain(chainID uint64) *Chain {
	return &Chain{State: NewState(), ChainID: chainID, GasCap: 10_000_000,
		Supply: MainnetSupply, FeeFrom: FeeFromHeight[chainID]}
}

// budget is the gas an inscription is entitled to.
//
// Two independent limits, and the smaller wins:
//
//	plasma  what the bytes nominally cost, x GasPerPlasma (R3)
//	fee     what the sender burned, at the supply curve and the
//	        dynamic-plasma resource price of the moment (fee.go)
//
// They answer different questions and neither subsumes the other. Plasma stops
// computation being free — it is already paid and cannot be avoided. The burn
// prices it — plasma is a *rate* that refills, so on its own it rate-limits
// without ever costing anyone anything. Requiring both means a caller must have
// paid the chain for the data *and* destroyed value for the work.
//
// **Congestion moves the second limit and deliberately not the first.** A busy
// network charges more real plasma for the same bytes, so it would be defensible
// to say the block bought proportionally more gas — but a deriver's replay cost
// would then scale with someone else's congestion, and nothing an inscriber does
// should make everyone else's history more expensive to verify. So the byte
// bound stays nominal and fixed, and congestion raises only the price of using
// it: the same burn buys less gas when the network is busy.
func (c *Chain) budget(ins Inscription) uint64 {
	perPlasma := c.GasPerPlasma
	if perPlasma == 0 {
		perPlasma = GasPerPlasma
	}
	gas := protocol.PlasmaForDataLength(len(ins.Content)) * perPlasma
	if gas > c.GasCap {
		gas = c.GasCap
	}
	if c.FeeFrom == 0 || ins.Height < c.FeeFrom {
		return gas
	}
	if fee := c.feeBudget(ins); fee < gas {
		return fee
	}
	return gas
}

// feeBudget is the gas the burned fee bought, or zero if nothing was burned.
//
// The payment only counts if it went to the protocol burn sink. That address is
// structurally unspendable — no keypair can reach byte 0 = 0x16 (A-26) — so
// "was this really burned" is answered by the address itself rather than by
// trusting anyone, and every deriver answers it identically.
func (c *Chain) feeBudget(ins Inscription) uint64 {
	if ins.Recipient != protocol.BurnAddress.String() {
		return 0
	}
	amount, ok := new(big.Int).SetString(ins.Amount, 10)
	if !ok {
		return 0
	}
	return c.Supply.GasFor(amount, ins.TokenStandard, c.Supply.EpochAt(ins.Timestamp), ins.Price())
}

// Budget exposes the same calculation for explorers and tooling, so a UI can
// show what an inscription was entitled to alongside what it used.
func (c *Chain) Budget(ins Inscription) uint64 { return c.budget(ins) }

// FeeFor is what a call of this size would have to burn, in raw units of zts,
// to be entitled to the gas its plasma already allows. Tooling uses it to price
// an inscription before publishing it.
//
// price is the dynamic-plasma multiplier the block is expected to meet, which a
// publisher learns from the same getRequiredPoWForAccountBlock call that tells
// it what plasma to pay. Quoting at the wrong price underpays if the network
// gets busier between the quote and the publish, so a caller should quote late
// and, since there are no refunds, round up rather than down.
func (c *Chain) FeeFor(gas uint64, zts string, timestamp int64, price ResourcePrice) *big.Int {
	return c.Supply.Fee(gas, zts, c.Supply.EpochAt(timestamp), price)
}

// AddressOf maps a NoM address to its EVM address.
//
// Both are 20 bytes and the mapping is the identity on those bytes, so an
// inscriber's EVM identity is their NoM identity: no registration, no key
// reuse, and no way to claim someone else's account — the sender is whoever
// the ledger says signed the block that carried the inscription. A holder
// signing with syrius is already, without doing anything, the owner of the
// matching EVM account.
func AddressOf(nomAddress string) (Address, error) {
	return AddressFromZenon(nomAddress)
}

// canonical sorts by (height, index) — the same canonical order the Zenoglyphs
// indexer applies its own opcodes in. Sharing the ordering is what makes two
// derivers agree without any coordination beyond reading the same chain.
func canonical(in []Inscription) []Inscription {
	out := append([]Inscription(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Height != out[j].Height {
			return out[i].Height < out[j].Height
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// Apply replays inscriptions in canonical order.
//
// A malformed or failing inscription is *recorded and skipped*, never fatal.
// This mirrors the indexer's own rule for unknown opcodes: an inscription this
// deriver rejects is simply not an EVM transaction, and every deriver rejects
// it identically because the reasons are all deterministic.
func (c *Chain) Apply(in []Inscription) error {
	defer c.State.Settle()
	for _, ins := range canonical(in) {
		c.State.Settle()
		if ins.Mime != Mime {
			continue
		}
		meta, err := parseExt(ins.Ext)
		if err != nil || meta == nil {
			c.Skipped++
			continue
		}
		from, err := AddressOf(ins.Issuer)
		if err != nil {
			c.Skipped++
			continue
		}
		id, err := hex.DecodeString(ins.ID)
		if err != nil {
			c.Skipped++
			continue
		}

		gas := c.budget(ins)
		// An inscription may declare a *lower* ceiling for itself, never a
		// higher one: the budget is what the chain was paid for, and no field
		// an author writes can buy more of a deriver's time than that.
		if meta.Gas > 0 && meta.Gas < gas {
			gas = meta.Gas
		}
		value := new(big.Int)
		if meta.Value != "" {
			if _, ok := value.SetString(meta.Value, 10); !ok {
				c.Skipped++
				continue
			}
		}

		blk := BlockContext{Number: ins.Height, Time: uint64(ins.Timestamp),
			ChainID: c.ChainID, Origin: from}
		e := New(c.State, blk)
		r := Receipt{Inscription: ins.ID, From: from, Input: ins.Content, GasBudget: gas,
			Height: ins.Height, Timestamp: ins.Timestamp,
			ResourcePrice: ins.Price().Scaled()}
		if ins.Recipient == protocol.BurnAddress.String() && ins.Amount != "" && ins.Amount != "0" {
			r.FeeBurned, r.FeeToken = ins.Amount, ins.TokenStandard
		}

		switch Op(meta.Op) {
		case OpDeploy:
			addr := ContractAddress(id)
			r.Op, r.To = OpDeploy, addr
			out, err := e.Create(from, addr, ins.Content, value, gas)
			r.Output, r.GasUsed = out, gas-e.GasLeft()
			r.Success, r.Logs = err == nil, e.Logs
			if err != nil {
				r.Err = err.Error()
			}
		case OpCall:
			// Either spelling. A call published by a wallet that only knows
			// z1… addresses is the same call as one published by Ethereum
			// tooling, and refusing one of them would fork this deriver from
			// any other that accepted it.
			to, err := ParseAddress(meta.To)
			if err != nil {
				c.Skipped++
				continue
			}
			r.Op, r.To = OpCall, to
			out, cerr := e.Call(from, to, ins.Content, value, gas, false)
			r.Output, r.GasUsed = out, gas-e.GasLeft()
			r.Success, r.Logs = cerr == nil, e.Logs
			if cerr != nil {
				r.Err = cerr.Error()
			}
		default:
			c.Skipped++
			continue
		}

		// The sender's nonce counts inscriptions accepted from them, so a
		// replayed log produces the same nonces as the original run.
		c.State.SetNonce(from, c.State.nonce(from)+1)
		// A top-level transaction has finished and nothing can revert into
		// it, so its undo records can go (see State.Settle).
		c.State.Settle()
		if c.byID == nil {
			c.byID = map[string]int{}
		}
		c.byID[r.Inscription] = len(c.Receipts)
		c.Receipts = append(c.Receipts, r)
		if !r.Success {
			c.Failed++
		}
		c.Applied++
		if ins.Height > c.Height {
			c.Height, c.Time = ins.Height, uint64(ins.Timestamp)
		}
	}
	return nil
}

func parseExt(b []byte) (*extEVM, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var root extRoot
	if err := permissive.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	if root.EVM == nil || root.EVM.Op == "" {
		return nil, nil
	}
	return root.EVM, nil
}

// Query runs a read-only call against derived state and discards any writes.
//
// This is the eth_call equivalent, and an explorer cannot work without it: a
// token balance lives in a mapping slot whose key is keccak(address ‖ slot),
// so reading it means either running balanceOf or reimplementing the
// contract's storage layout in the UI. The first is correct for every token;
// the second is correct for the tokens you guessed right about.
//
// Nothing is published and nothing persists: the call runs against a copy of
// the state, under a static frame, so even a contract that tries to write
// cannot. It never touches c.State — not even with an undo — so it is safe
// alongside other readers. A caller making several queries should take one
// View and reuse it: each Query here pays for a copy.
func (c *Chain) Query(to Address, data []byte) ([]byte, error) {
	return c.View().Query(to, data)
}

// View is a private copy of a chain's state for running read-only calls.
type View struct {
	st  *State
	blk BlockContext
}

// View copies the state once, so any number of queries can run against it
// without writing to the chain's own. O(state) to take, then each query
// costs only what it executes.
func (c *Chain) View() *View {
	return &View{st: c.State.Copy(), blk: BlockContext{Number: c.Height, Time: c.Time, ChainID: c.ChainID}}
}

func (v *View) Query(to Address, data []byte) ([]byte, error) {
	mark := v.st.snapshot()
	out, err := New(v.st, v.blk).Call(Address{}, to, data, new(big.Int), QueryGas, true)
	// Undo even a successful query: its reads created accounts, and the
	// next query on this view should see the state as it was copied.
	v.st.revert(mark)
	return out, err
}

// Receipt looks a receipt up by its inscription id.
func (c *Chain) Receipt(id string) (Receipt, bool) {
	i, ok := c.byID[id]
	if !ok {
		return Receipt{}, false
	}
	return c.Receipts[i], true
}
