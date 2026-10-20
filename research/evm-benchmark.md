# EVM swarm benchmark — devnet, 2026-09-10

A time-bound, step-load benchmark of the whole EVM path: NoM account blocks,
the zenoglyphs indexer, and the EVM state the API derives from it. It answers
"where does this architecture stop scaling, and what gives first?"

## Summary

Six load steps, 4 → 128 concurrent wallets, 4 minutes of load each, then two
128-wallet re-runs after fixing what the first one found. **Through 64
wallets there were zero unexpected failures and zero state mismatches.** At 128
wallets the chain's per-momentum cap was hit. In the first run, 18 wallets were
wedged by the way dynamic plasma prices a block that misses its momentum.

Limits, in the order they arrive:

1. **Price, at ~31 calls per momentum (~2 calls/s here).** An EVM call block
   costs ~34,000 base plasma, and dynamic plasma's fused target is 1.05 M per
   momentum. Above the target the price compounds every momentum, and it does
   not level off while demand stays above it: +5.3% per momentum at 64
   calls/momentum, +10% at 128. **Sustainable throughput at a stable price is
   ~2 calls/s (~120/min)** at this devnet's 15 s momentum interval, or ~3/s
   at a healthy 10 s.
2. **The hard cap: ~124 calls per momentum (~8/s here).** That is
   `MaxBasePlasmaInMomentum` (4.2 M) ÷ ~34 k per call, and momentums in the
   [re-run](#fix-headroom-and-rebids) held 124–125. *The first 128-wallet run
   showed a flat 110. That was not a cap: it was 128 wallets minus the 18 that
   were wedged (item 3).*
3. **Blocks that miss a congested momentum are wedged, not delayed**, when
   they pay the quoted price exactly. A block commits to the price in force
   when it was signed. If it isn't included in that momentum and the price then
   rises, it becomes invalid and stays pending until the price decays back
   below what it paid. Every later block from that account builds on it, so
   **the whole account is frozen for the duration of the congestion**. In the
   first 128-wallet run that was 18 wallets, and 36 calls (1.9%) missed the
   2-minute confirm timeout. All 36 confirmed together once the load stopped
   and the price decayed back to 1.0×, 10–12 minutes after signing
   ([details](#wedged-accounts)). **Fixed in the swarm** with price headroom
   plus replace-by-fee rebids; see [the fix](#fix-headroom-and-rebids).
4. **The EVM rebuild grows with history, not load.** The API re-derives the
   entire EVM chain from every EVM inscription each time a new one arrives:
   ~55–80 µs per historical transaction, under a mutex every `/evm/*` request
   waits on. It was 347 ms at 4,374 transactions and 767 ms at 8,019. On the
   rebuild alone, the API would fall behind a 15 s momentum at ~190–270 k
   transactions. In practice it breaks far sooner. Every `/evm/*` request also
   reloads all EVM rows under that mutex, and at 8 k transactions with 128
   pollers, `/evm/overview` took 6 s at p50 and 30 s at worst. **The API
   became the bottleneck, ahead of the chain** ([details](#the-re-runs-moved-the-bottleneck-to-the-api)).
   This was an implementation choice, not a protocol limit. **Fixed on
   2026-09-11**; see [EVM API fixes](#evm-api-fixes).

Momentum production was never affected by load: mean 15 s, max 20 s and no
stalls in any load window, including the saturated one.

## Setup

| | |
|---|---|
| Network | go-zenon docker devnet, chain 69: 3 pillars, 1 observer, 1 RPC node, all on one Windows 11 host |
| Dynamic plasma | spork active; price floor 1000 (=1.0×). The fused target (1.05 M) and max (4.2 M) base plasma per momentum are go-zenon's defaults, not read from the chain (this node has no RPC for them). The run corroborates the target: the price first moved at 32 × 34 k ≈ 1.09 M |
| Indexer / API | `zcribe serve` on the same host, SQLite (`zenoglyphs.db`), confirmation depth 6 |
| Load | `zcribe evm swarm -devnet -swarm-index 100 -wallets N -duration 4m -report …` |
| Steps | 4, 8, 16, 32, 64 wallets with 100 QSR plasma and 0.5 ZNN floor per wallet; then 128 wallets with 300 QSR and 5 ZNN (headroom for the price) |
| Per wallet | serial: publish → wait for momentum → wait for the API receipt → next call |
| Call mix | ~15% deliberate bad-selector reverts, the rest split across `increment()`, `add(uint256)`, `transfer(address,uint256)`; every step deploys a fresh counter and ERC-20 |
| Fee | 100,000 gas per call (`-call-gas` default), burned in ZNN |
| Sampling | every 10 s: `du` of each node's `/root/.znn`, indexer DB size, `zcribe serve` RSS, `/info` and `/evm/overview` latency, frontier momentum and price |

Each wallet is serial, so the offered load is **N calls per momentum**, and
the chain confirms it. Every momentum in the 4-, 8-, 16-, 32- and 64-wallet
windows held exactly N blocks.

### Baseline: the devnet was already degraded

For at least the 12 hours before the run, the idle devnet averaged **15 s per
momentum**, against a 10 s design. One producer misses every other slot, so
intervals alternate between 10 s and 20 s. Every latency below inherits that
clock. Just before the benchmark there were two momentum stalls of 80 s and
70 s, with three node containers at over 100% CPU. **No stall over 30 s occurred
in any load window.**

## Results by step

| Wallets | Calls | Throughput | Unexpected | Total p50 / p99 | Confirm p50 | Index p50 / max | Price at end | Fee p50 | Funding |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 4 | 64 | 16.0/min | 0 | 15.0 / 20.1 s | 13.0 s | 2.0 / 2.0 s | 1.000× | 0.0100 ZNN | 59 s |
| 8 | 136 | 32.9/min | 0 | 10.0 / 20.1 s | 8.0 s | 2.0 / 2.0 s | 1.000× | 0.0100 ZNN | 71 s |
| 16 | 272 | 64.4/min | 0 | 13.1 / 20.2 s | 11.0 s | 2.0 / 2.2 s | 1.000× | 0.0100 ZNN | 107 s |
| 32 | 544 | 132.9/min | 0 | 10.1 / 20.1 s | 8.0 s | 2.1 / 4.1 s | 1.014× | 0.0101 ZNN | 154 s |
| 64 | 1,088 | 261.3/min | 0 | 10.7 / 21.0 s | 8.0 s | 2.4 / 3.1 s | 2.383× | 0.0150 ZNN | 281 s |
| 128 | 1,904 | 450.2/min | **36** | 13.4 / 24.4 s | 8.0 s | 4.3 / 9.4 s | 5.046× | 0.0214 ZNN | 567 s |

Throughput is linear in wallets up to the cap: each wallet completes about one
call per momentum. In the first 128-wallet run it flattened to 110 per
momentum (450/min at 15 s), because 18 wallets were wedged and the other 110
never missed a momentum.

The 128-wallet step also reported 8 "balance mismatches". They are all
swarm wallets holding *more* than the swarm's ledger expected, and each one is
the sender of a stuck transfer. The swarm debits a transfer when it publishes
it, and a timed-out one stays debited. These transfers never executed, so the
chain is correct and the checker is not. `evmswarm.go`'s comment on `transfer`
predicts exactly this case.

## Settlement time

Each call has three phases:

- **Publish** (quote, plasma, sign, `publishRawTransaction`): p50 8–12 ms at
  every step. p99 grows with concurrency, from 43 ms at 4 wallets to 484 ms at
  128, because every goroutine shares one RPC node.
- **Confirm** (published → in a momentum): 8 s or 18 s, bimodal, depending on
  where in the 10/20 s momentum cycle the call was published. It does not move
  with load until the momentum is full.
- **Index** (in a momentum → receipt on `/evm/tx/{id}`): ~2.0 s through 32
  wallets. That is the 2 s poll interval, so the real figure is below it. It
  rose to p50 2.4 s at 64 wallets and p50 4.3 s (max 9.4 s) at 128. That is
  the EVM rebuild plus `/evm/*` contention from 128 pollers. At 128 wallets
  the API, not the chain, is the slower half of the path.

The indexer applies blocks at the tip. Its confirmation depth (6) only decides
how long a result is labelled "pending", so **soft settlement** (receipt
visible) is ~10–25 s. **Final settlement** at depth 6 adds ~6 × 15 s ≈ 90 s on
this devnet, or ~60 s at a healthy 10 s interval.

## Wedged accounts

What happened at 128 wallets, step by step:

1. All 128 wallets signed their first call against the frontier price, 1.0×.
   Each block's fused plasma was exactly its base plasma (for example, 36,232
   and 36,232).
2. 18 of them missed that first momentum and stayed in the pool. The re-run's
   ~124 cap would predict 4, so something else also took space in that
   momentum. This run didn't record what.
3. That momentum raised the price 10%. A block paying 1.0× is now below the
   price, and `dp.ValidPrice` rejects it for every later momentum while the
   price stays above 1.0×.
4. Each of those 18 wallets then signed round 2 at the new price. Round 2
   builds on the frontier, and the frontier includes the stuck round 1, so
   round 2 can't be included either. Both calls timed out after 2 minutes, and
   the wallet was frozen for the rest of the step.

The other 110 wallets never missed a momentum. The price then rose every
momentum, so any block that missed once would have been wedged in the same way.

For example, `z1qqfydswz2wfpvvsnnswr3f0nnzxhptkrsm4azm` (swarm wallet 116):

```
frontier height 3, unconfirmed
  h2 c1af711f…  fused 36,232  base 36,232   (paid 1.0×)
  h3 9d9d4e18…  fused 74,950  base 31,812   (paid 2.36×, waits on h2)
price at the time of checking: 4.108×
```

**Recovery: automatic, but only once the congestion is gone.** At idle, the
price decays at up to about the rate it climbed: 5,046 → 1,000 in ~16
momentums here (~10% per momentum), and ~5% per momentum on the way down from
a lower peak later. **All 36 stuck calls confirmed together, in momentum
57677**, the first momentum back at 1.0× and ~8 minutes after the load
stopped. Each had waited 620–741 s from signing. Nothing was lost and no call
needed republishing. The node keeps the stuck blocks, and they execute in
their original order. For a wallet caught in the congestion, though, that
means **"confirmed" can arrive 10+ minutes late, at a price far below what
everyone else paid while it waited**. A client with a 2-minute confirm
timeout reports that as a failure.

What this means for clients:

- **Sign with headroom over the quoted price.** A block that pays exactly the
  frontier price is only valid for one momentum.
- **Headroom alone does not survive sustained overload.** The momentum builder
  admits the highest price first (`pillar/content_selector.go`), and blocks
  signed later are signed at the later, higher price. So a block that missed
  once loses to every fresher block until it bids again.
- **Rebid, don't wait.** go-zenon's account pool accepts a block at an
  occupied height if it pays a higher price, and rolls back the one it
  displaces (`chain/account_pool.go`, `higherPriority`). That is
  replace-by-fee, and it is how a stuck wallet unsticks itself without waiting
  for the price to fall.
- **This is easy to hit by accident.** After the first re-run, the funder
  account itself wedged: a `devnet-wallet receive`, run by hand while the price
  was still decaying at ~1.5×, published two blocks paying 1.0×. The next
  swarm's deploy queued behind them for 2 minutes and the run aborted. Any
  tool that pays the quote exactly is exposed, including Syrius.

## Fix: headroom and rebids

Two changes in `internal/inscribe`, both on by default in `evm simulate` and
`evm swarm`:

- **`-price-headroom 25`**: fused plasma is paid at the quoted price + 25%.
  That outlasts two momentums of the steepest climb measured (1.1² = 1.21).
  `Quote` pads by the same amount, so the EVM fee is quoted at the padded
  price and the call's gas budget is unchanged. The cost is **25% more fee per
  call**, because the deriver prices the burn at what the block paid
  (`evm/fee.go`).
- **`-rebid-after 30s`**: `ConfirmRebidding` re-signs an unconfirmed call at
  its own height at today's price (`Inscriber.Rebid`), and scales its burn by
  the same ratio so it keeps its gas. It only ever rebids an account's
  frontier; replacing a block with descendants would silently roll them back.
  It watches every bid for the height and returns whichever one confirms.
  Replacement is a per-node pool decision, so the pillar that builds the
  momentum can still commit the original: that happened in 2 of the first 6
  rebids.

Verified live by a `devnet`-tagged test (`go test -tags devnet -run Rebid
./internal/inscribe`). It publishes at the exact price and rebids before the
next momentum, then checks that only the replacement confirms and that its
burn was scaled.

| 128-wallet run | Calls | Unexpected | Mismatches | Calls per momentum | Price at end | Rebids |
|---|---:|---:|---:|---|---:|---:|
| first (exact price, no rebid) | 1,904 | 36 | 8 | flat 110 | 5.05× | – |
| re-run (headroom + rebid, 500 QSR) | 1,757 | 2 | 2 | 124–125, alternating with 70–98 | 4.24× | 6 |
| verification (defaults, 1,000 QSR) | 1,591 | **0** | **0** | 124–125, alternating with 42–72 | 3.33× | 4 |

The re-run's 2 failures were a bug in the first version of the wait loop,
fixed before the verification. Each original confirmed 30 s after publishing,
in the same instant its rebid went out, and the loop was watching only the
rebid. Every rebid in the verification confirmed in 31–33 s.

### The re-runs moved the bottleneck to the API

The two re-runs ran against more EVM history: ~6 k and ~8 k transactions,
against 4.4 k in the first. Their throughput fell (416 and 371/min, from 450)
even though the chain now had room. Every other momentum was only half full.
Each wallet waits for its receipt before its next call, and the receipt wait
grew:

| EVM history | Rebuild p50 | `/evm/overview` p50 / max | Index wait p50 |
|---:|---:|---:|---:|
| 4.4 k (first run) | 264 ms | 56 ms / 0.9 s | 4.3 s |
| ~6 k (re-run) | 508 ms | – | 7.3 s |
| ~8 k (verification) | 767 ms | **6 s / 30 s** (the sampler's timeout) | **11.4 s** |

The `/evm/*` wait is far longer than the rebuild alone. Every request also
loads every EVM inscription from SQLite, under the same mutex, just to check
whether the cache is stale. 128 pollers queue behind one another. That is the
ceiling [the EVM rebuild](#the-evm-rebuild) section extrapolates to, arriving
at 8 k transactions rather than 190 k, because the per-request load stacks on
top of the rebuild.

## EVM API fixes

Implemented after the runs above (2026-09-11):

- **The API follows the tail.** It fetches only the EVM inscriptions after its
  last canonical position (`store.AssetsOfMimeCarriedAfter`, with a new
  `(mime, height, momentum_index)` index) and applies them onto the cached
  chain. It no longer loads every row to count them, or replays from genesis
  on every change. A reset or checkpoint restore bumps
  `store.ProjectionGeneration` in the same transaction as the rows it
  rewrites, and a changed generation means a full rebuild. This also fixes
  the count-keyed cache serving a stale branch after a same-count reorg
  (evm-todo O2).
- **Locking.** Handlers hold a read lock for their whole run. The tail is
  applied under the write lock, taking microseconds per transaction. A rebuild
  runs outside the lock and is swapped in.
- **Reads no longer write.** The exported `evm.State` getters are pure, so an
  explorer lookup no longer creates the account it shows. `Query` runs on a
  per-request copy (`Chain.View`) instead of the live state (evm-todo O6, O7).
- **An undo journal replaces the per-call deep copy** (evm-todo O8). Full
  replay of the 8,019-transaction devnet history went from 958 ms to 104 ms
  with an identical state root and receipts (`TestEVMReplayOfDevnetHistory`,
  `devnet` tag).
- **O(1) work per poll.** `/evm/tx/{id}` is an indexed lookup, and
  `/evm/overview` reads a root and counts computed once per catch-up.
- **A background check replaces the self-check a full replay used to give.**
  `zcribe serve -evm-verify 10m` replays from scratch and compares state
  roots, rebuilding on disagreement.

**A/B, same machine, same chain, same moment.** The old build served on
:8710. The new build ran as a second indexer on :8720, against a copy of the
database. A 16-wallet swarm kept inscriptions arriving throughout, and 8,019 →
8,288 EVM transactions of history were covered. The load generator is
`scratchpad`-only.

| Load | Build | Served | `/evm/tx` p50 / p99 | `/evm/overview` p50 / p99 |
|---|---|---:|---:|---:|
| 128 clients polling every 2 s | old | 8.4 req/s | 17.4 s / 19.5 s | 26.0 s / 34.7 s |
| | **new** | **63.9 req/s** | **2.6 ms / 5.7 ms** | **2.6 ms / 31 ms** |
| 32 clients, no pause | old | 6.5 req/s | 4.1 s / 6.9 s | 9.9 s / 12.9 s |
| | **new** | **532 req/s** | 57 ms / 100 ms | 57 ms / 102 ms |

The polling clients asked for about 64 req/s. The old build managed 8. Both
builds reported the same state root at the end (`2f666eb8…`). The new one had
rebuilt once (at startup), and its verifier had replayed from scratch
mid-test and agreed.

The next limit, visible at saturation, was the catch-up itself. Every request
did two small SQLite reads (the generation, then the tail), serialised so a
tail is applied once, and at ~530 req/s that queue was the ~57 ms. **Fixed**
by the two follow-ups below.

### Follow-ups: skipping idle catch-ups, and checkpoints

- **A request skips the store when nothing can have changed.** The indexer
  now publishes `Progress()`, an atomic counter bumped after every committed
  momentum and every reorg. A request that sees the value the last catch-up
  saw reads nothing. A rewrite by another process (`zcribe reindex`) doesn't
  move it, so the skip is bounded by `Server.EVMMaxStale` (1 s): after that
  the generation is read again regardless. `syncHeight`, `nodeHeight` and
  `chainID` became atomics too, fixing an existing read race with `/info`.
- **Restarts and reorgs resume from a checkpoint** instead of replaying from
  genesis. See evm-todo R2 for the mechanism, O1 for the end-to-end reorg
  test.

Same machine, same chain, against the 8,464-transaction devnet history:

| | Before these follow-ups | After |
|---|---:|---:|
| 32 clients, no pause | 532 req/s, p50 57 ms | **3,825 req/s, p50 8.1 ms** |
| 128 clients polling every 2 s | p50 2.6 ms | **p50 0.6 ms** |
| Rebuild on restart | 261 ms from genesis | **90 ms from checkpoint** |

All three builds agreed on the state root throughout (`6bfd6610…`).

The restart gain is only ~3× at this size, because the journal already made
replay cheap. The 90 ms is mostly decoding 8,464 persisted receipts, and it
scales with receipt count, not with execution. A replay scales with the gas
history actually burned. Persisting costs ~530 bytes per EVM transaction on
disk: a 451-byte receipt body on average, plus a checkpoint of ~74 KB for
this state every 1,024 inscriptions.

## Costs

### Gas: calls overpay by 8× to 1,500×

The fee is the gas limit, with no refunds (`research/evm-fee-design.md`). At the
default of 100,000 gas:

| Op | Gas used | Share of what was paid for |
|---|---:|---:|
| `erc20.transfer` | 12,652 | 12.7% |
| `counter.increment` | 6,274 | 6.3% |
| `counter.add` | 5,292 | 5.3% |
| bad selector (revert) | 64 | 0.06% |

At 1.0× price, 100,000 gas costs 997,928 raw = **0.00998 ZNN**. A
`-call-gas 20000` default would cover every op here with 50% headroom, at a
fifth of the fee.

### Plasma

Every call block's base plasma was 30,000–36,232 (p50 34,056): 21,000 base plus
68 per byte of the ~190-byte envelope. It was paid entirely from fusion; no
block fell back to proof of work.

Fused plasma paid tracks the price. p50 was 34,056 at 1.0×, 50,263 over the
64-wallet window and 72,948 over the 128-wallet window, with a max of 166,233
at 5×. One QSR fused gives 2,100 plasma, so 100 QSR covers about 6 calls in
flight at 1.0× and 1 at 5×. A single 34 k block at 5× needs ~81 QSR fused.
That is why the 128-wallet step fused 300 QSR per wallet.

### What sustained overload costs

Price per momentum through each step window:

```
64 wallets:  1051 1107 1165 1226 1290 1358 1428 1502 1580 1663 1751 1844 1941 2043 2151 2264 2383   (+5.3%/momentum)
128 wallets: 1100 1210 1331 1464 1610 1771 1948 2142 2356 2591 2850 3135 3448 3792 4171 4588 5046   (+10%/momentum)
```

The EVM fee has a congestion term (`evm.ResourcePrice`), so the fee per call
follows the plasma price. At 128 wallets it went from 0.0100 to 0.0458 ZNN
per call in four minutes. At +10% per momentum, sustained overload costs 10×
in 24 momentums (6 minutes). Price, not failure, is the designed rate
limiter. The failure mode is item 3 of the summary: an underpriced block in
flight when the price moves.

## Storage

Measured across the full benchmark window: 217 momentums, 5,162 account
blocks, 4,236 of them EVM inscriptions.

| Where | Growth | Per unit |
|---|---:|---:|
| RPC node (`/root/.znn`, LevelDB) | +10.9 MB | ~2.1 KB per account block |
| All five nodes together | +46.9 MB | ~9.1 KB per account block, network-wide |
| Indexer DB (`zenoglyphs.db`) | +6.60 MB | **~1.56 KB per EVM call** (plus ~0.53 KB since receipts are persisted; see [follow-ups](#follow-ups-skipping-idle-catch-ups-and-checkpoints)) |
| EVM calldata itself | +148 KB | ~35 B per call |
| EVM state (in memory only) | `zcribe serve` RSS 83 → 102 MB | ~4.5 KB per call, GC-noisy |

The indexer stores about **44 bytes for every byte of calldata**. That's the
events row, the asset row, the provenance columns and the SQLite indexes. For
calls this small, the envelope and bookkeeping are nearly all of it. The ratio
falls as calldata grows.

At the time of these runs the EVM "index" was not stored at all: receipts,
logs and state existed only in the API process and were rebuilt from the
inscription rows on every restart. Receipts and periodic state checkpoints
are now persisted as projections (see
[follow-ups](#follow-ups-skipping-idle-catch-ups-and-checkpoints)). The live
state and receipts are still held in memory as well (evm-todo R7).

## The EVM rebuild

`api.evmChain()` (`internal/api/evm.go`) does two things on **every** `/evm/*`
request:

1. `AssetsOfMimeCarried("application/evm")` loads every EVM inscription,
   content included, from SQLite, just to compare the count with the cache's.
2. If the count changed, it replays every inscription from genesis into a
   fresh `evm.Chain`, under the mutex.

Measured rebuild time against total EVM transactions:

| EVM txs | 138 | 468 | 806 | 1,190 | 1,736 | 2,120 | 3,826 | 4,374 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Rebuild | 3 ms | 16 ms | 32 ms | 48 ms | 92 ms | 117 ms | 278 ms | 347 ms |

That's ~55 µs per transaction up to the 64-wallet step, and ~80 µs under the
128-wallet load, where the rebuild competed with 128 pollers for the same CPU.
`/evm/overview` p50 followed it: 3, 5, 8, 12.5, 24 and 56 ms across the six
steps, with a p99 of 889 ms at 128.

A new EVM transaction lands in nearly every momentum under any load, so
**effectively every momentum triggers a full rebuild**. Extrapolated at
55–80 µs per transaction:

| EVM history | Rebuild per momentum | Consequence |
|---:|---:|---|
| 10 k txs | 0.5–0.8 s | the first request after each momentum is visibly slow |
| 100 k txs | 5.5–8 s | a third to a half of every 15 s momentum goes on re-deriving |
| 190–270 k txs | ~15 s | the rebuild never finishes before the next is due; `/evm/*` is permanently stale and blocked |

At 2 calls/s, the price-stable rate, 190–270 k transactions is **1–1.5 days**
of sustained use. The fix, since implemented (see
[EVM API fixes](#evm-api-fixes)):

- an incremental `Apply` of only the new inscriptions onto the cached chain
  (ordering is canonical (height, index), so appending is sound whenever the
  new inscriptions are all above the cached height, and a reorg past it is the
  only case that needs a full replay);
- a count query that doesn't load content.

## Stability

- **Through 64 wallets, no unexpected failures in 2,104 calls.** 323
  deliberate bad-selector reverts all reverted as expected. Every receipt was
  checked, and the final counter and all ERC-20 balances matched the swarm's
  own ledger at every step.
- **At 128 wallets, 36 of 1,904 calls (1.9%) missed the 2-minute timeout**,
  all from the wedging above. All confirmed ~8 minutes after the load stopped. There were no other failures: no refused publishes, no PoW
  fallbacks, no reverted funding.
- **Momentum production was unaffected by load**, saturated or not: mean 15 s,
  max 20 s, no stall over 30 s in any load window.
- **RPC node:** CPU p50 rose from 0.4% (4 wallets) through 16.8% (64) and
  peaked at 23.6% (128); memory peaked at 402 MiB.
- **Indexer:** sync lag never exceeded one momentum. `/info` stayed at 1–5 ms.
  All the added latency was in `/evm/*`.

## Swarm funding: the harness's own limit

Funding is serial by construction: one funder, one account chain. It publishes
three blocks per wallet (fee send, fuse, token send) and scaled at about **4.4
s per wallet** (59 s for 4, 281 s for 64, 567 s for 128). Two hard edges follow:

- go-zenon refuses more than **500 uncommitted blocks per account**, so a
  single funding pass tops out at ~166 new wallets.
- At most ~124 call-sized blocks fit in a momentum, so funding N wallets
  occupies at least 3N ÷ 124 momentums, and it competes with any other load
  for those slots.

Beyond ~150 wallets, funding needs to be batched across momentums or spread
over several funders.

## What was fixed to make this run

`zcribe evm swarm` could not fund a swarm before this run:

- **Token funding always failed on a fresh token.** The holder list is rebuilt
  from Transfer events, and the fixture's constructor mints without emitting
  one. So the deployer read as holding 0, and the swarm refused with "the
  deployer holds 0 token units". `seedLedger` now keeps the amount `setup`
  minted when the deployer isn't listed yet.
- **Swarm wallets had no plasma.** Every receive and call was paid with proof
  of work, capped at 1,080 bytes of data, and CPU-bound across the whole swarm.
  The funder now fuses QSR for each wallet with that wallet as beneficiary
  (`-plasma-qsr`, default 100), and waits for the plasma to appear before
  releasing the swarm.
- **Fee funding ignored the run length.** `-fund` is now a floor, raised to 2×
  the quoted per-call fee × rounds.
- **The default swarm range overlapped devnet infrastructure.** With
  `-index 1`, the swarm starts at index 2, and index 4 (`z1qz9zr5…`) is one of
  the devnet's pillar producer addresses. This run used `-swarm-index 100`.

New flags: `-duration` (time-bound instead of `-rounds`), `-report file.json`
(one row per call: publish, confirm and index timings, fee, base/fused plasma,
difficulty, gas used, rebids, outcome), `-plasma-qsr` (default 1,000 QSR per
wallet), `-price-headroom` and `-rebid-after` (see
[the fix](#fix-headroom-and-rebids)).

## Reproducing

The harness is in [`evm-benchmark/`](evm-benchmark/):

- `run.ps1`: steps through wallet counts, running the swarm with `-report`
  while `sample.ps1` records the environment every 10 s.
- `analyze.py`: joins the per-call reports, the environment samples and the
  chain's own momentum record (`ledger.getMomentumsByHeight`) into
  `summary.json`, and prints one line per step.

```powershell
go build -o $env:TEMP\zcribe-swarm.exe ./cmd/zcribe
research\evm-benchmark\run.ps1 -Steps 4,8,16,32,64 -Duration 4m
research\evm-benchmark\run.ps1 -Steps 128 -Duration 4m -PlasmaQsr 300 -Fund 5
python research\evm-benchmark\analyze.py
```

Run it against a devnet whose momentum interval is healthy first. On this
one, every latency figure carries the 15 s clock described above.
