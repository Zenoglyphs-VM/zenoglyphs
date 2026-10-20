# An EVM whose contracts are inscriptions

**Status:** working prototype, not normative. **Opened:** 2026-09-09.
**Code:** `internal/evm/`, `cmd/zcribe/evm.go`. **Background:**
[`on-chain-execution.md`](on-chain-execution.md). **Open concerns are tracked in
[`evm-todo.md`](evm-todo.md)** — that list is authoritative, this document is
the explanation.

Everything below was run on the devnet. Asset ids and the state root are from
that run.

---

## 1. What this is

A metaprotocol on top of a metaprotocol. Contracts and calls are ordinary
Zenoglyphs inscriptions; the world state is derived by replaying them in
canonical order.

```
                    on chain, permanent                derived, disposable
  ┌───────────────────────────────────────────┐   ┌──────────────────────┐
  │ mint  mime=application/evm                │   │  accounts            │
  │       ext={"evm":{"op":"deploy"}}         │──▶│  code                │
  │       content = init bytecode             │   │  storage             │
  │                                           │   │  balances / nonces   │
  │ mint  mime=application/evm                │   │  logs, receipts      │
  │       ext={"evm":{"op":"call","to":"0x…"}}│──▶│  state root          │
  │       content = calldata                  │   └──────────────────────┘
  └───────────────────────────────────────────┘      recomputable, always
```

**It required no change to Zenoglyphs consensus.** No opcode was added, and the
indexer was not touched. The operation lives in the envelope's extension lane,
which R-2.9 defines as a bounded, ignorable map and `internal/api` surfaces
verbatim while explicitly never interpreting it. An indexer that has never
heard of the EVM stays exactly as correct as before; it sees an
`application/evm` mint and files it like any other asset.

The one consensus-relevant change is a single MIME allowlist entry,
`application/evm` — free before mainnet, a version bump after.

## 2. Why the EVM specifically

`on-chain-execution.md` §6.3 lists what consensus-side execution needs and what
WebAssembly lacks. The EVM has both, and did not have to acquire them:

| requirement | wasm | EVM |
|---|---|---|
| deterministic arithmetic | floats carry unspecified NaN payloads — CosmWasm bans floating point outright | 256-bit integers only; there is no float opcode to ban |
| bounded execution | no metering; needs instrumentation (fuel) or an interpreter | gas, in the instruction set since 2015 |
| auditable artifacts | opaque binary | opaque binary, but a decade of tooling reads it |

So the awkward pairing is the other way round from what the earlier document
assumed. WebAssembly is the better *render-side* choice — smaller, faster, and
it needs no agreement between viewers. The EVM is the better *consensus-side*
choice, and it is the one being layered here.

## 3. Two design decisions worth not re-deriving

### 3.1 A contract address is a commitment to its code

Ethereum derives a contract address from `(sender, nonce)`, or under CREATE2
from `(sender, salt, codehash)`. Here:

```
contractAddress = 0x16 ‖ keccak256(assetId)[13:]
```

The asset id is the mint block's hash, which the Zenoglyphs envelope already
makes a commitment to the content (the whole deterministic-CBOR argument in
`internal/protocol/envelope.go` exists to make that true). So a contract
address is a commitment to the artifact, transitively.

Three consequences:

- **The address names its own code.** Anyone holding it can ask any Zenoglyphs
  indexer for exactly the bytes that address runs. No trusted registry, no
  verified-source portal, no "is this really the contract I think it is".
- **Deployed code is immutable in a stronger sense than on Ethereum.** There is
  no CREATE2-plus-SELFDESTRUCT redeploy trick, because the address is a
  function of the code rather than of who deployed it or when. You cannot swap
  the code at an address; you can only deploy different code, which is a
  different address.
- **The same bytes deployed twice collide,** deliberately — the second deploy
  targets an address that already holds identical code. Nothing is lost.
- **The address is outside every namespace a keypair can reach.** Byte 0 is
  forced to `0x16` rather than taken from the digest, so a contract can never
  collide with somebody's account and never lands in NoM's embedded-contract
  space. It was unconstrained until 2026-09-10, which put roughly one contract
  in every 256 inside real user space; fixing it changed every contract address
  and the devnet was re-launched. See
  [`evm-address-scheme.md`](evm-address-scheme.md).

### 3.2 A NoM address *is* an EVM address

Both are 20 bytes. A NoM address is one type byte plus a 19-byte core; the EVM
takes those 20 bytes unchanged. So an inscriber's EVM identity is their NoM
identity — no registration step, no second keypair, and no way to forge a
sender, because the sender is whoever the ledger says signed the block that
carried the inscription.

**The practical form of that: the account already in someone's Syrius extension
is their EVM account.** They can connect it to the explorer and publish calls
from it, and every surface renders and accepts the `z1…` spelling. Nothing is
derived and nothing is registered — there is no "create an EVM account" step
anywhere, because there is nothing to create.

## 4. Running it

```sh
zcribe evm deploy -devnet -file counter.bin -name Counter.evm
zcribe evm call   -devnet -to z1ze5z8s… -gas 100000 \
                  -sig "transfer(address,uint256)" -args "z1qzx2uq…,2500"
#   addresses in either spelling, anywhere
#   burned: 997928 raw ZNN for 100000 gas — the fee IS the gas limit
zcribe evm state    -db zenoglyphs.db
zcribe evm receipts -db zenoglyphs.db
zcribe evm address  -addr z1qq6e…      # the same address, both spellings
```

Or from the browser: connect Syrius on any EVM page and use the Write panel on
a contract. The page builds the inscription envelope itself, byte-identical to
what `zcribe` produces — `npm run verify:vectors` checks that against the
protocol's golden vectors and against a real inscription off the chain.

The devnet run, with a counter contract hand-assembled from opcodes (there is
no `solc` in this tree):

```
OP       INSCRIPTION  CONTRACT                                     OK    GAS     RESULT
deploy   534bdc314e   0xbf0cf8931d49553cc66352930168c213a1d72cd5   yes   26649   0x6000…
call     ccade0c2cb   0xbf0cf8931d49553cc66352930168c213a1d72cd5   yes   6274    1 (0x1)
           log topic0=20d8a6f5a693f9d1… data=0000…0001
call     4465fcb709   0xbf0cf8931d49553cc66352930168c213a1d72cd5   yes   6274    2 (0x2)
call     8dea195807   0xbf0cf8931d49553cc66352930168c213a1d72cd5   yes   6274    3 (0x3)
call     d555150c71   0xbf0cf8931d49553cc66352930168c213a1d72cd5   yes   5292    42 (0x2a)   add(39)
call     6882b64d3c   0xbf0cf8931d49553cc66352930168c213a1d72cd5   yes   262     42 (0x2a)   get()
call     0d93d33e09   0xbf0cf8931d49553cc66352930168c213a1d72cd5   no    64      reverted    bad selector
```

```
derived from 7 EVM inscriptions (7 applied, 0 skipped)
state root: 23f4caa3ac2f52e8f4d4256f2bffba06e1ec4d18844cbe1791de2cc912770337

0x0035941e758a1f155fb30c1217a8186ed9e12651  account
  nonce 7  balance 0
0xbf0cf8931d49553cc66352930168c213a1d72cd5  contract, 133 bytes of code
  nonce 1  balance 0
  slot 0x0 = 42
```

The contract's code is served from the content origin as
`Content-Type: application/evm`, addressable by asset id — which is the
"permanent artifact" claim made concrete.

## 5. One correctness trap, found while building it

`Store.ListAssets` excludes assets hidden by moderation, because moderation is
a serving-layer decision. **A deriver must not read through it.** Moderation is
per-indexer policy (A-6); if the EVM replayed the moderated listing, an
operator hiding one inscription would silently fork their derived state away
from everyone else's — the exact failure the codebase separates the event log
from the serving path to prevent.

Hence `Store.AssetsOfMime`, which deliberately omits the moderation join and
says so. Serving decisions belong at the edge; replay reads the log.

## 5b. ERC-20, and the explorer

A full ERC-20 is deployed on the devnet — `totalSupply`, `balanceOf`,
`transfer`, `approve`, `allowance`, `transferFrom`, `decimals`, with real
`Transfer` and `Approval` events. 686 bytes of runtime, hand-assembled, because
there is no `solc` in this tree. Storage slot 0 is the supply, mapping slot 1
the balances, mapping slot 2 the allowances; the constructor mints the whole
supply to whoever deployed it.

```sh
zcribe evm deploy -devnet -file erc20.bin -name GLYPH.evm
zcribe evm call   -devnet -to 0x37de… -sig "transfer(address,uint256)" -args "0x00b0…,250000000000000000000000"
zcribe evm call   -devnet -to 0x37de… -sig "approve(address,uint256)"  -args "0x00b6…,50000000000000000000000"
zcribe evm call   -devnet -index 2 -to 0x37de… -sig "transferFrom(address,address,uint256)" -args "0x0035…,0x00ca…,20000000000000000000000"
zcribe evm token  -addr 0x37de…
zcribe evm query  -to 0x37de… -sig "balanceOf(address)" -args "0x0035…"
```

The devnet result, with the balances summing to exactly the supply:

```
token 0x37def257b4c77af4194bd28ce7376e13fdd40b87
  totalSupply 1000000000000000000000000  decimals 18

HOLDER                                       BALANCE
0x0035941e758a1f155fb30c1217a8186ed9e12651   605000000000000000000000
0x00000000000000000000000000000000000000b0   250000000000000000000000
0x00000000000000000000000000000000000000ca   145000000000000000000000
```

**The explorer** is an EVM tab in the viewer, with four pages: an overview
(state root, counts, contracts, latest transactions), an account — which is
also a contract page when it has code — a token page, and a transaction page.
`internal/api/evm.go` serves it and derives on read, cached against the count
of `application/evm` inscriptions.

Three things it does that are worth keeping:

- **Every contract links to its artifact.** A contract page names the
  inscription its code lives in, so the chain of custody from address to bytes
  is one click and needs no verified-source registry (§3.1).
- **Balances are read through the contract, not out of storage.** A mapping
  slot is `keccak(key ‖ slot)` and cannot be inverted, so the storage table
  shows hashes and the token page runs `balanceOf` against derived state
  instead. That is `Chain.Query`, the `eth_call` equivalent — read-only by
  construction, since it runs in a static frame against a snapshot.
- **Holders come from events.** A mapping has no enumerable key set, so the
  holder list is reconstructed from `Transfer` logs and each balance read back.
  Every explorer does this; it is the standard, not a shortcut.

Method and event signatures are decoded from a small known-selector table, and
anything outside it renders as a raw selector rather than a guess.

## 5c. What gas actually costs, in node CPU and in plasma

Measured on this machine, 2026-09-09. The two currencies are unrelated, and
that is the finding.

### Gas is node CPU

A tight `JUMPDEST PUSH1 0 JUMP` loop, which is the cheapest way to burn gas:

| gas cap | burned | wall clock |
|---|---|---|
| 1,000,000 | 1,000,000 | 19 ms |
| 10,000,000 | 10,000,000 | 172 ms |
| 100,000,000 | 100,000,000 | 1.82 s |

**≈55,000 gas per millisecond**, linear, single-threaded. This EVM is `big.Int`
based and deliberately unoptimised, so treat that as a floor for a real
implementation, not a ceiling.

So the current `GasCap` of 10,000,000 means: **one inscription can cost every
deriver up to ~180 ms of CPU.** Everything actually deployed so far totals
249,481 gas — about 4.5 ms for the whole chain.

Real operations are nowhere near the cap:

| operation | gas | calldata |
|---|---|---|
| ERC-20 deploy | 147,426 | 751 B |
| `transferFrom` | 18,271 | 100 B |
| `transfer` | 12,652 | 68 B |
| `approve` | 7,028 | 68 B |
| `balanceOf` (view, reverted call) | 363 | 68 B |
| counter `increment()` | 6,274 | 4 B |

### Plasma is chain bytes

Zenon charges `plasma = len(data) × 68 + 21,000`. It prices **data**, and has
no idea what the bytes mean:

| calldata | plasma |
|---|---|
| 4 B (a bare selector) | 21,272 |
| 68 B (ERC-20 transfer) | 25,624 |
| 16,384 B (a full block) | 1,135,112 |

Plasma is a *capital requirement, not a burn*: a 20,200 QSR account holds a
42,420,000 tank that **refills**. That is ~1,655 ERC-20 transfers per full tank,
repeatable forever.

### The asymmetry, and why it matters

```
        attacker pays                     every deriver pays
  ┌────────────────────────┐        ┌──────────────────────────┐
  │ 4 bytes of calldata    │        │ up to 10,000,000 gas      │
  │ = 21,272 plasma        │  ───▶  │ = ~180 ms of CPU          │
  │ (refills, ~10 QSR)     │        │ (permanent, every replay) │
  └────────────────────────┘        └──────────────────────────┘
```

**Nothing connects the two.** The cheapest possible inscription — four bytes of
selector pointed at an expensive contract — costs the minimum plasma and the
maximum gas. Worse, replay is from genesis, so the cost is not paid once: it is
paid by every deriver, on every cold start, forever. A full plasma tank buys
~1,990 such calls, which is ~6 minutes of CPU added permanently to every node
that ever syncs, and the tank refills.

Gas as it stands bounds a *single* inscription and bounds nothing in aggregate.
It stops runaway contracts and accidental infinite loops. It does not stop a
deliberate attacker, because the thing it meters is not the thing anyone pays
for.

### The fix, and why it is small

**Make the gas budget a function of the inscription's size.** Plasma is linear
in bytes; if gas is too, the ratio becomes constant and NoM's own pricing
prices EVM compute for free:

```go
// instead of a flat GasCap
gas := uint64(len(ins.Content)) * GasPerByte
```

At `GasPerByte = 3000`, sized against the measurements above:

| calldata | gas budget | covers |
|---|---|---|
| 4 B | 12,000 | a simple read |
| 68 B | 204,000 | an ERC-20 transfer (12,652) with 16× headroom |
| 751 B | 2,253,000 | the ERC-20 deploy (147,426) with 15× headroom |
| 16,384 B | 49,152,000 | anything |

Buying more compute then requires buying more bytes, and bytes cost plasma. The
attack above stops working because the cheap inscription is also the one with
the smallest budget.

Two things this does **not** fix, both worth writing down:

- **Replay is still O(history).** Checkpointing derived state is the same
  problem `internal/store/snapshot.go` already solved for the indexer, and the
  same solution applies.
- **There is still no per-momentum ceiling.** Ethereum has a block gas limit;
  the analogue here would be a cap on total gas per momentum, which also bounds
  how fast an attacker can grow the replay cost.

## 6. What is and is not implemented

**Implemented:** the arithmetic, comparison and bitwise opcodes including the
signed ones; `KECCAK256`; the environment and block opcodes; memory, storage,
`JUMP`/`JUMPI` with up-front `JUMPDEST` analysis; `PUSH0`–`PUSH32`,
`DUP1`–`DUP16`, `SWAP1`–`SWAP16`; `LOG0`–`LOG4`; `CALL`, `STATICCALL`,
`RETURN`, `REVERT`; deployment via init code; gas; snapshot/revert; a state
root.

**Not implemented, deliberately:** `CREATE`/`CREATE2` (contract addresses come
from artifacts here, so in-EVM creation needs a design decision, not just an
opcode), `SELFDESTRUCT`, `DELEGATECALL`, `EXTCODECOPY`, `BLOCKHASH`, precompiles,
and access lists. Gas uses a flattened schedule rather than mainnet prices —
there is no fee market here, because the *data* was already paid for in plasma;
gas exists only to bound replay work.

**Not compatible with Ethereum mainnet**, and not trying to be. The state root
is a sorted digest, not a Merkle-Patricia root: nothing here proves a single
account to a light client, and a digest is auditable by eye.

## 7. Open questions

- **Value.** Balances exist and `CALL` moves them, but nothing mints them.
  Seeding from a genesis inscription is the obvious next step; bridging ZNN or
  a ZTS into EVM balances is a real design problem, not a small one.
- **Read-only calls.** Every call here is an inscription, so `get()` costs a
  block. An `eth_call`-style local execution against derived state needs no
  chain write at all and is a few lines — it just was not the interesting half.
- **`CREATE` from inside the EVM.** If a contract deploys a contract, what is
  the new address a commitment to? There is no artifact. Probably
  `keccak(parentAddress, nonce)` as on Ethereum, accepting that such contracts
  lose the property in §3.1.
- **Reorgs.** Zenoglyphs already handles them by replaying the event log from a
  snapshot. The EVM deriver currently replays from scratch every time, which is
  correct but O(history); checkpointing it is the same problem the indexer
  already solved in `internal/store/snapshot.go`.
- **Gas is not paid by anyone** — measured and quantified in §5c. The fix is to
  size the budget from the inscription's byte count so that plasma prices
  compute; it is a one-line change and has not been made, because it is a
  consensus decision about what every deriver must agree to charge.
