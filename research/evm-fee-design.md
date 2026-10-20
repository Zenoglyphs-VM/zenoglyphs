# A burned dual-token fee for EVM calls

**Status:** **live on devnet** from momentum 55,100. Implemented in `internal/evm/fee.go`; activation heights are pinned per chain in `evm.FeeFromHeight`, and **mainnet is deliberately absent**. **Opened:** 2026-09-09. **Congestion term added:** 2026-09-10.
Companion to [`evm-todo.md`](evm-todo.md) (items C2, C3, R1) and
[`evm-prototype.md`](evm-prototype.md) §5c.

Every emission figure below is read from `go-zenon/vm/constants/embedded.go`,
not recalled. Every gas figure is measured on the deployed devnet contracts.

---

## 1. The supply facts this has to sit on

Both tokens inflate **perpetually**. Neither is capped. The reward schedule runs
out of entries after 11 ticks (ZNN) and 8 ticks (QSR) of 30 epochs each, and
`NetworkZnnRewardPerEpoch` then returns the *last* entry forever:

```
tail emission:  4,320 ZNN/epoch = 1,576,800 ZNN/yr
                5,000 QSR/epoch = 1,825,000 QSR/yr
```

Starting from 13.67M ZNN and 31.2M QSR:

| year | ZNN supply | QSR supply | QSR:ZNN | ZNN inflation |
|---|---|---|---|---|
| now | 13,670,000 | 31,200,000 | **2.282** | 11.53% |
| +1 | 15,246,800 | 33,025,000 | 2.166 | 10.34% |
| +5 | 21,554,000 | 40,325,000 | 1.871 | 7.32% |
| +10 | 29,438,000 | 49,450,000 | 1.680 | 5.36% |
| +30 | 60,974,000 | 85,950,000 | 1.410 | 2.59% |
| ∞ | — | — | **1.157** | → 0% |

Two consequences the design has to respect:

- **The ratio is not stable.** It decays from 2.28 toward 1.157, the ratio of
  the tail emissions. Anything that hard-codes 2.5 is wrong within a year.
- **A fee fixed in nominal ZNN gets cheaper in real terms every year**, because
  the supply it is denominated against grows. The price has to be a fraction of
  supply, not a number of coins — see §3.

## 2. Why the ratio must come from supply and not from a market

The exchange rate has to be computed identically by every deriver replaying the
same inscription, years apart, offline. That rules out an oracle, a DEX price,
or anything observed: two derivers reading a price feed at different times reach
different states and the chain forks.

Total supply is the only value that is both economically meaningful and a **pure
function of the momentum height**, because emission is a published schedule. So:

```
supplyZNN(epoch) = GenesisZNN + Σ NetworkZnnRewardPerEpoch(e)   for e < epoch
supplyQSR(epoch) = GenesisQSR + Σ NetworkQsrRewardPerEpoch(e)
R(epoch)         = supplyQSR(epoch) / supplyZNN(epoch)
```

**Define this curve as normative rather than observational.** The real supply
also moves by a few small sinks the schedule does not model — the 1 ZNN ZTS
issuance fee, accelerator flows. If the fee schedule tried to track actual
supply it would need archive state and would drift between implementations. If
it uses its *own declared curve*, it is exact by definition, needs no node
query, and tracks reality closely because emission dominates. The two genesis
constants are calibration, pinned once against mainnet.

## 3. The price

The price of one unit of gas is the product of two terms, and they answer
different questions:

```
price(gas) = supply(token, epoch) / GasPerSupply     <- the long run
           x resourcePrice / PriceScaleFactor        <- right now
```

§3.1 is the first term and §3.2 the second.

### 3.1 The long run: a fraction of supply

Denominate in a **fraction of the ZNN supply per gas**, so the fee holds its
economic meaning as supply inflates and never needs re-tuning:

```
price(epoch) = supplyZNN(epoch) × BurnFraction        [ZNN per gas]
feeZNN(gas)  = ceil(gas × price(epoch))
feeQSR(gas)  = ceil(gas × price(epoch) × R(epoch))
```

Implemented as a single dimensionless constant, `GasPerSupply = 1.37e14` — the
gas the entire supply of a token buys — so no fraction and no ratio appears:

```go
fee(gas, token, epoch) = ceil(gas * supply(token, epoch) / GasPerSupply)
```

**Each token's fee is the same fraction of its own supply**, so the exchange
rate is emergent rather than computed. There is no rate to get wrong, and
nothing to keep in sync.

The legible way to state that price level:

> **Burning the entire ZNN supply buys about 29 days of global node CPU.**
> (1/BurnFraction = 1.37e14 gas, at the measured 55,000 gas/ms.)

### 3.2 Right now: NoM's own dynamic-plasma price

The supply term is a pure function of the epoch, so it moves once a day and
knows nothing about load. NoM already computes a congestion signal — the
dynamic-plasma spork prices fused plasma and proof of work against a
per-momentum target, EIP-1559 style, and carries the next prices in every
momentum header. The fee borrows it rather than inventing a second congestion
signal over a different resource.

**The multiplier is read off the carrier block, not off the momentum header,
and that is the trick.** Consensus requires a block to pay

```
ceil(basePlasma x fusionPrice / PriceScaleFactor)
```

of fused plasma, or the PoW equivalent, or a mix. So the ratio of what a block
*paid* to what its bytes nominally cost already **is** the price it met:

```
mu = (fusedPlasma + DifficultyToPlasma(difficulty)) / (21000 + 68n)
```

Three properties fall out of reading it there:

- **No momentum field is needed.** `fusedPlasma` and `difficulty` are committed
  to by the block hash, so a deriver holding the block reaches this conclusion
  with no node at all — the same property the emission schedule has.
- **Mixed payment needs no rule.** Fused and PoW plasma add.
- **It cannot be ground down.** Overpaying plasma raises `mu` and so raises the
  payer's own fee, so the cheapest valid block is the one that pays exactly the
  network price — which is what everyone will publish.

`mu` is floored at 1. A pre-spork block, a block from a node that reports no
plasma, and an uncongested network all yield exactly 1, which is why this rule
needs **no activation height of its own**: applied to all of history it changes
nothing that came before.

### 3.3 Congestion moves the price and deliberately not the budget

The gas budget stays `min(plasma-derived, fee-derived)`, and only the second
term moves:

```
budget = min( PlasmaForDataLength(n) x GasPerPlasma,   // unchanged, nominal
              GasFor(burned, token, epoch, mu) )       // mu shrinks it
```

It would be defensible to say a busy block, which pays more real plasma for the
same bytes, has bought proportionally more gas. It is the wrong call: a
deriver's replay cost would then scale with someone else's congestion, and
nothing an inscriber does should make everyone else's history more expensive to
verify. So the byte bound stays fixed and congestion raises only the price of
using it — **the same burn buys less gas when the network is busy**, which is
the deriver-protecting direction.

### What it costs a user

| operation | gas | fee (ZNN) | fee (QSR, today) |
|---|---|---|---|
| counter `increment()` | 6,274 | 0.0006274 | 0.0014320 |
| ERC-20 `approve` | 7,028 | 0.0007028 | 0.0016040 |
| **ERC-20 `transfer`** | **12,652** | **0.0012652** | **0.0028877** |
| ERC-20 `transferFrom` | 18,271 | 0.0018271 | 0.0041701 |
| ERC-20 deploy | 147,426 | 0.0147426 | 0.0336481 |
| a full 16 KB block at cap | 9,080,896 | 0.9080896 | 2.0725966 |

### What it burns

| load | gas/yr | ZNN burned/yr | vs emission |
|---|---|---|---|
| 1 tx/momentum | 3.99e10 | 3,990 | 0.3% |
| 10 tx/momentum | 3.99e11 | 39,899 | 2.5% |
| **saturated at R1's ceiling** | 1.73e13 | **1,734,480** | **110%** |

Because the price is supply-linked, the saturated case is a constant **12.7% of
supply per year, forever**, against emission that is 11.53% now and falls every
year. **The design is net-deflationary at saturation and permanently so**, while
ordinary use burns a small fraction of emission. That is the right shape: the
sink bites exactly when load is heavy.

### What abuse costs

| | |
|---|---|
| 1 second of permanent node CPU | **5.5 ZNN** |
| a full 42.42M plasma tank of abuse (339M gas) | **33.9 ZNN** |

## 4. Mechanics

**The fee rides the carrier block.** An EVM inscription sends its fee as the
account block's own value, to the burn sink that already exists:

```
to     = z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9
amount = the fee
zts    = zts1znnxxxxxxxxxxxxx9z4ulx  or  zts1qsrxxxxxxxxxxxxxmrhjll
```

This needs no new machinery. Dispenser purchases already prove a carrier block
can carry consensus-relevant value, and the sink is structurally unspendable
(byte 0 = `0x16`, reachable by no keypair — A-26), so the burn is provable
rather than promised. It does need a spec rule, because R-7.2 currently says a
carrier block's amount **MUST be ignored** — that is item C5.

**The budget composes with the plasma coupling** rather than replacing it —
see §3.3 for the two terms and why only one of them is congestion-sensitive.
Both must be satisfied. Plasma stops compute being free; the burn prices it. For
a well-priced call the fee is the binding limit and plasma is the backstop,
which is the intended shape.

**Choosing the fee is choosing the gas limit.** There is no refund — no
mechanism exists to send ZNN back to a sender from derived state — so an
overpaid fee is simply burned. The tooling answer is to simulate first:
`Chain.Query` already returns the exact gas a call will use, so
`zcribe evm call` can price the inscription precisely before publishing it.

## 5. The thing that will actually happen

**Users will pay in whichever token the formula overprices, and only that one.**

The formula says 1 ZNN ≡ 2.282 QSR. If the market says 1 ZNN ≡ 5 QSR, then
paying the QSR side costs 2.282 QSR ≈ 0.456 ZNN of value against 1 ZNN — so
everybody pays QSR and the ZNN side is never used. The reverse if the market
moves the other way. A dual-token fee at a formula rate does not burn both
tokens; it burns one, and which one flips when the market crosses the formula.

**This is worth accepting rather than fixing**, for two reasons:

- The alternative is an oracle, which breaks the determinism in §2. There is no
  third option.
- The behaviour is mildly countercyclical: the fee always burns whichever token
  the market currently values *less* relative to its supply share. The sink
  lands where the surplus is.

What it means practically: do not model this as "burns ZNN and QSR". Model it as
"burns the cheaper of the two, at a rate the protocol sets", and size the
economics on the total, not on either leg.

## 5a. What "implemented" did not mean, and what closed the gap

For a day this document said the fee was "implemented, gated behind
`Chain.FeeFrom`". That was true of the arithmetic and misleading about the
system, and the distinction is worth keeping:

> **Nothing could pay.** `Inscriber.InscribeFile` had no amount parameter at
> all — every carrier block was hardcoded to send zero. So `FeeFrom` could not
> have been switched on: turning it on would have given every inscription a
> budget of zero, because no publisher had any way to attach value.

A fee is three things, and only the first existed:

| | |
|---|---|
| **the price** | `fee.go` — supply curve times resource price. Was done. |
| **the payment** | a carrier block that sends the fee to the sink. Did not exist. |
| **the activation** | a height, pinned per chain, that the rule begins at. Was a zero field. |

All three now exist. `Inscriber.Payment` attaches value to the head block;
`Inscriber.Quote` sizes the block exactly and reads the momentum's resource
price so the quote matches what the deriver will compute; `evm.FeeFromHeight`
pins the height. `zcribe evm call -gas N` quotes and burns, and the browser
does the same through `GET /evm/fee` — quoted server-side, because a second
implementation of the emission curve in TypeScript is a second thing to keep
in step.

Measured on devnet, momentum 55,032:

```
transfer(address,uint256), 100,000 gas
  burned    997,928 raw ZNN  (0.00998 ZNN) to z1zen0glyphxdurn…
  budget    100,000 gas      (the fee bought it; plasma alone allowed 289,856)
  used       13,244 gas
```

The burn sits unreceived at the sink, which is what makes it a burn: byte 0 is
`0x16`, no keypair can reach it, and A-26 verified on devnet that such a send is
accepted and left there forever by every node.

### What switching it on broke, quietly

The activation was verified live rather than assumed, and it caught something:

```
momentum 55109, a faucet request, no fee attached
  budget    0
  error     evm: out of gas
  HTTP      200, with an inscription id
```

**Every publisher has to quote, and the faucet was not one.** It called
`InscribeFile` — the non-paying variant — so from momentum 55,100 every faucet
request kept returning an inscription id and a 200 while deriving to `out of
gas`. Nothing in the API layer was wrong; the faucet simply was not on the list
of things that had been taught to pay.

That is the general shape of the risk at an activation height, and it is worth
stating separately from the rule itself: **a rule that begins at a height turns
every un-upgraded publisher into one that silently produces dead inscriptions.**
The publisher gets a block hash, the chain accepts the block, and only the
derived receipt says the call did nothing. Anything that publishes has to be
audited against the height, not just the deriver.

Verified after the fix, at momentum 55,121: the faucet burns 598,757 raw ZNN for
its own 60,000 gas, and the transfer succeeds.

**The gas limit is the fee, and vice versa.** `-gas` sets both the declared
ceiling in `ext` and the amount quoted, deliberately: an inscription may lower
its own budget but never raise it, so burning for 600,000 while declaring
100,000 would destroy value for gas the deriver will not grant.

## 6. Open questions before this is buildable

| | |
|---|---|
| ~~Calibration~~ | **Done.** Read from node.zenonhub.io at momentum 14,154,189 (chainId 1): 1366728966519283 ZNN, 3118999966129297 QSR, epoch 1750. |
| ~~No congestion response~~ | **Done.** §3.2: NoM's own dynamic-plasma resource price multiplies the fee, read off the carrier block's committed plasma fields. Floored at 1, so it needs no activation height. |
| **Quoting under congestion** | `FeeFor` takes the price the block is expected to meet, which a publisher learns from the same `getRequiredPoWForAccountBlock` call that tells it what plasma to pay. If the network gets busier between the quote and the publish, the call underpays — and there are no refunds, so tooling should quote late and round up. |
| **Spec rule** | C5: R-7.2 says the carrier amount is ignored. The EVM needs its own rule saying it is not, the way dispensers got one. |
| **Interaction with R1** | The burn prices approaching the ceiling; the ceiling bounds the worst case absolutely. They are complements, not alternatives — neither alone is sufficient. |
| **Bootstrapping** | Every EVM call now requires holding ZNN or QSR. That is a real barrier for a metaprotocol whose other operations are free, and it is a deliberate trade. |
| **Failed calls** | A reverted call still consumed deriver CPU, so the fee must still burn. Confirm that is acceptable — it is what Ethereum does. |
| **Does it fix C3?** | No. This burns; it pays no operator. Operator incentive stays unsolved and is discussed under "Who pays the operator" in `evm-todo.md`. |
