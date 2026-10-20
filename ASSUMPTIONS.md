# Assumptions

Everything this implementation decided that the plan left open, or decided
differently. Each entry says **what** was assumed, **why**, and **what it
costs to change**.

Cost is the useful column. A *free* change is a config edit. A *cheap* one is
a code change with no effect on which inscriptions exist. A **consensus**
change alters which inscriptions are valid, so it invalidates the golden
vectors and requires a version bump once anything is live.

---

## Decisions the plan asked for

### A-1 · Prefix is `glyph` — **consensus**
`internal/protocol/protocol.go` · spec R-2.1

Two earlier builds chose otherwise, and each was answered by the other's
objection. The first chose `zsc`, on the argument that `zen` squats a namespace
the next metaprotocol on NoM would have an equal claim to. The second chose
`zen`, on the argument that this protocol is first, the name is the network's
own, and a prefix nobody recognises buys deference to a protocol that does not
exist.

Both objections are right, and `glyph` is what is left when they are both
taken. It squats nothing — it names this protocol, not the network — and it
reads on sight in a block explorer without the reader having seen the spec.

It costs two bytes more than `zen`: six CBOR bytes against four. That is 0.2%
of a 1080-byte PoW block and negligible against the 16 KB fused ceiling. The
chunker measures real encodings rather than assuming a fixed envelope overhead
(`internal/protocol/chunk.go`), and the byte-signature fast path derives
itself from the constant (R-2.4), so no other code carries the length.

Nothing is locked out by this. The prefix only has to be *unique*, and R-2.1
rejects every ZDU that does not match exactly, so a second metaprotocol picks
different bytes and the two never see each other's blocks.

*To change:* one constant plus regenerated vectors. Free today, never again
once anything is inscribed on mainnet.

### A-2 · No protocol fee, and no 1,000 PP requirement — **consensus (absence of a rule)**

D-4 recommended no fee; you also asked to drop the 1,000 PP gate outright.
Both point the same way, so nothing in this implementation charges anything or
checks a balance before inscribing. Plasma already tiers the cost of data
(R-9), which is the argument.

*To change:* adding a fee later means adding a validity rule, which is a
consensus change. If it is ever added, make it a burn — a payment introduces a
trusted recipient.

### A-3 · Names: *Zenoglyphs* public, `zcribe` the binary — **free**

D-5. *Ztamp* is not used anywhere in this tree.

### A-4 · Monorepo — **free**

D-6, as recommended. `spec/`, `internal/`, `cmd/`, `web/`, `deploy/`,
`testdata/` in one tree so the vectors are shared and the spec versions with
the code.

### A-5 · Written in Go, not Dart — **free**

The plan's reference implementation is Dart. This is a fresh tree in Go for
one reason that matters: `go-zenon` is the consensus implementation and is
sitting next to it, so block hashing, PoW, address derivation, and key
handling are *imported* rather than reimplemented. A reimplementation that
drifts by one byte produces blocks the network silently refuses.

`go.mod` carries `replace github.com/zenon-network/go-zenon => ../go-zenon`.
**This is the one piece of setup that will not survive being moved out of this
directory layout.**

---

## Wire format

### A-6 · `data` is always a byte string, never a text string — **consensus**
`internal/protocol/wire.go`

The plan's §4.4 allowed `bstr|tstr`. Permitting both gives the same content
two distinct encodings, which costs injectivity for nothing, and a multi-part
chain could otherwise mix the two across chunks. Interpretation is the mime
type's job. This mirrors Ordinals exactly: a content type plus a byte string.

*To change:* the decoder would need to accept both and define which one a
chunk walk produces. Consensus change.

### A-6b · Unknown fields: three lanes, via an `ext` map — **consensus**
`internal/protocol/envelope.go`, `wire.go` · spec R-2.9

The prior-art survey's top recommendation was Runes' **even/odd rule**:
unknown even tags invalidate, unknown odd tags are ignored. It cannot be
retrofitted once assets exist, and the build had neither lane — it rejected
every unknown key, which is safe but means any future field, however
cosmetic, is a version bump every existing indexer must refuse wholesale.

Adopted the *benefit* without the rewrite. Even/odd needs an integer tag space
to have a natural parity (Ordinals and Runes both use one); switching to
integer tags would save 65 bytes on a full mint — 6.0% of a PoW block, 0.40%
of a fused one — but rewrites both schemas, the byte-signature fast path, all
vectors, and most of the spec, and leaves the wire unreadable without it.

Instead: one optional `ext` map. That gives three lanes —

| Change | Mechanism | Old indexers |
|---|---|---|
| the ruleset | bump `v` | refuse the ZDU |
| a field that changes meaning | new top-level key | refuse the ZDU |
| a purely additional field | a key inside `ext` | ignore it, stay correct |

Two decisions inside it are worth calling out:

**`ext` is consensus-inert (R-2.9.3).** Nothing inside it may affect protocol
state. This is *stricter* than Runes' odd tags, and A-18 is why: if an `ext`
key could move a balance, an indexer that understood it and one that did not
would both mark the event applied, commit to the same state root, and hold
different state — the exact blindness that was just fixed. The payoff is a
strong guarantee: an old indexer and a new one compute identical state
forever; the newer one just shows its clients more.

**The key is `ext`, not `x` (R-2.9.4).** Canonical CBOR sorts bytewise, so
`"x"` (0x6178) sorts between `"v"` (0x6176) and `"op"` (0x626f70) — inside the
byte signature, destroying the fixed-length fast path. Any 3-character key
sorts after every 1- and 2-character key, so `ext` lands safely between `op`
and `data`. Two bytes more, paid only when used, and there is a test pinning
the reasoning so a rename cannot silently undo it.

Bounded at 2048 bytes and depth 4, because an unbounded `ext` is a second
content channel with no declared type — bypassing the very allowlist that
bounds what a viewer can be talked into rendering.

**Worked examples are now in the spec at R-2.9.5** — licence, alt text, a
detached signature by an off-chain key, a generative seed, a cross-chain
origin claim, a royalty request — along with the test for whether something
belongs in the lane at all: *if half the indexers ignored this, would anyone
hold a different balance?*

**Activation heights: considered, not adopted.** Written up at spec R-2.10.
The short version is that `v` and an activation height solve different
problems, and Zenoglyphs only has the problem `v` solves. A version bump is
per-inscription and leaves an old indexer *correct* about everything it does
understand; an activation height is a flag day that leaves an unpatched
indexer silently *wrong*. The only thing `v` cannot express is changing how
inscriptions **already on the chain** are read — which is exactly the case
Ordinals had at block 824544, and exactly the case R-3.3 exists to prevent
here. One activation height already exists in all but name: `startHeight`
(A-25). If a second is ever needed, that is the precedent, and A-18 means a
half-adopted one would be visible at the first affected momentum rather than
silent.

### A-7 · MIME strings are compared, not parsed — **consensus**
`internal/protocol/mime.go` · spec R-8.1

`text/plain;charset=utf-8` is a distinct allowlist entry from `text/plain`,
and only the listed spelling is valid. Parsing parameters would mean two
indexers must agree on whitespace and case-folding rules inside the type — a
divergence surface for no benefit.

**Is the strictness a problem?** Three consequences, and only the third is
worth watching:

1. *An inscriber must write the exact spelling.* In practice never surfaces:
   `zcribe inscribe` fills it in from the file extension, `/info` publishes the
   list, and a wrong spelling fails locally before a block is ever signed.
2. *`text/plain` without the charset is invalid.* Deliberate. One spelling per
   type is what keeps the allowlist a set rather than a parsing problem, and
   the charset being mandatory removes an entire class of rendering ambiguity.
3. *The list cannot grow without a consensus change.* This is the real cost.
   Adding a type makes previously-invalid mints valid, so it needs a version
   bump; removing one is worse. **A type that is missing is missing until v2.**
   The mitigation is to have got the list right, which is why A-8 widened it
   well past the Dart original before anything was live.

Ordinals took the opposite approach — `content_type` is arbitrary bytes, and
`ord` decides at render time — and pays for it with a renderer that must be
defensive about every string anyone ever wrote. The allowlist moves that
decision to mint time, where it can be refused instead of survived.

### A-8 · The allowlist is larger than the Dart original — **consensus**

Added `audio/*`, `video/*`, fonts, `text/css`, `text/javascript`,
`model/gltf-binary`, `image/webp`, `image/avif`. Dropped
`application/pgp-signature` and `application/protobuf`. The shape is still
ord's `media.rs`.

`text/javascript` and `text/css` matter more now than they did: they are what
a shared library is inscribed as (A-32).

*To change:* adding a type is a consensus change (it makes previously-invalid
mints valid). Removing one is worse.

### A-9 · A multi-part head must carry **both** `pHash` and `tHash` — **consensus**
`internal/indexer/apply.go`

The plan implied it; this makes it explicit and enforced. A head with only one
hash describes a chain with no defined end, which no two indexers would walk
alike.

### A-10 · Chunk order: the head carries the *first* chunk — **consensus**

Reassembly starts at the head, walks `pHash`, concatenates head-first, and
terminates at `tHash`. This follows the plan's wording ("concatenates
head-first, walking `pHash`") and means publication runs backwards: tail
first, head last. That ordering also satisfies "every part precedes the head
in canonical order" for free.

### A-11 · Intermediaries may be shared; a tail may not — **consensus**
spec R-7.1.3

R-7.1.3 says a tail may be consumed by at most one head. Nothing forbids two
chains sharing an intermediary, and combined with the same-issuer rule (A-12)
that is just an issuer deduplicating their own chunks.

**Why the tail rule stays, now that the reason is written down.** The rule
looks like it is about ownership, and it is not — A-12 already stops anyone
else from reusing your chunks, so a second head over the same chain could only
be your own. It is about **storage amplification**. Each head is a separate
asset with its own content, so *N* heads over one 1 MB chain cost every indexer
*N* megabytes for the price of *N* single blocks. The ratio between what the
attacker pays and what every indexer on the network stores is unbounded, and
the attacker is the person who already paid for the chain once.

**And the thing the rule appears to forbid is available anyway, for 32 bytes.**
Every use case for a swappable head — a hundred editions of one image, the same
artwork under a new name, a shared library used by everything after it — is a
composite: a mint carrying `refs: [thatAssetId]` whose own content renders slot
`"0"` (A-32). One payload, stored once, many assets, each with its own id,
supply, owner and metadata. That is `ord`'s *delegate* pattern arrived at from
the other direction, and it is strictly better than head-swapping because the
imported asset keeps its own identity instead of being an anonymous chunk.

*To change:* claiming every part rather than only the tail would be a
tightening, and dropping the tail claim a loosening. Both are consensus
changes, and the loosening should not be made without content-addressed
storage underneath it — dedupe the blob by its hash first, and the
amplification argument goes away.

### A-12 · Every part must share the head's issuer — **consensus**
spec R-7.1.1

The plan calls this "the most important missing check" and it is implemented.
Here is the attack it stops, since the reason is easy to lose:

A multi-part mint is a chain of blocks. The head names `pHash` and `tHash`, and
the indexer walks that chain and concatenates the chunks into the asset's
content. **The pointers are the attacker's choice.** Nothing about a chunk says
who is allowed to use it — it is just a block with bytes in it, sitting on a
public chain.

So without this rule: you spend 64 blocks inscribing a 1 MB artwork. I watch
the chunks land, and before or after your head does, I publish **one** block
whose `pHash` and `tHash` point at *your* chunks. The indexer walks my chain,
finds your bytes, and mints them as my asset — my id, my issuer, my supply, my
address holding all of it. I paid for one block; you paid for 64. The tail
claim (A-11) does not save you, because it is first-come and I can be first.

Requiring `block.address` to equal the head's on every part reduces that to
"you can duplicate your own work", which is not an attack. It is the difference
between a chain of blocks being an *object* and being a *free-for-all of
bytes*, and it is why the walk in `assemble()` checks the issuer before it
appends a single byte.

### A-13 · `owner` is `block.toAddress` — **consensus**

From §4.7. A mint to yourself is the ordinary case; a mint to someone else
issues the supply directly to them. Worth stating because it means a
mis-addressed mint gives the asset away irrecoverably.

### A-14 · A collection mismatch drops the reference, it does not invalidate the asset — **consensus**

From §4.7. The asset stays valid; only the claimed membership is dropped.

### A-15 · Dispensers settle in a real ZTS — **consensus** · *raised for review*
`internal/indexer/apply.go` · spec R-7.4

The `zts` field is 10 raw bytes naming which **Network of Momentum token** a
buyer must pay in. It came from the Dart reference implementation
(`src/model/dispenser.dart`) by way of the plan (§"createDispenser
`{a, n, price, zts}`"), not from a decision taken here — you flagged that you
do not remember agreeing to involve real ZTS, and you are right that it was
inherited.

**What it actually does.** The protocol never holds, mints, moves or custodies
a ZTS. A dispenser purchase is one block: the buyer sends a real payment to the
seller and puts `op=6` in that same block's `data`. The indexer then *observes*
three fields the carrier block already has — `toAddress`, `tokenStandard`,
`amount` — and checks them against the dispenser. It reads `blockType` and
`toAddress` for every other opcode already; this reads two more.

**Why it is worth keeping.** Because the payment and the claim are the same
block, a sale settles **atomically with no escrow agent and no counterparty
risk**, on a chain that has no PSBT-equivalent to build that any other way.
Take the ZTS out and there are only worse options: an offer/accept pair that
can be griefed between the two blocks, or no on-chain market at all.

The prior art is direct. **Counterparty's dispensers** — where the word comes
from — take the base-layer coin: send BTC to the dispenser address, receive the
Counterparty-layer asset. That is "involving real BTC in the protocol" in
exactly the sense being questioned here, and it is the most-used feature they
have. **Ordinals, Runes and BRC-20 all have no market primitive at all** and
push trading to PSBTs or centralised order books; NoM has no PSBT analogue, so
copying that would mean copying the centralised order book.

**What it does cost, and these are real:**

1. **Price is denominated in the smallest unit of a token whose decimals the
   protocol does not know.** `price: uint` is atomic units of `zts`; ZNN has 8
   of them. Any UI must fetch token metadata from the node to render "1.5 ZNN".
   Correct — decimals are the token's business, not this protocol's — but it
   means a raw price is unreadable and a client that skips the lookup will
   mislead someone by eight orders of magnitude.
2. **Any ZTS can be named, including one the seller issued last week.** A
   dispenser priced in a worthless token looks exactly like a real sale in the
   data. This is a marketplace-UI problem, not a protocol one, but it needs to
   be solved *in the UI* before a market exists rather than after.
3. It is the only place a Zenoglyphs rule reads base-layer value. Nothing
   else in the protocol cares what a block is worth.

**If you would rather it went away**, in increasing order of cost: restrict
`zts` to ZNN and QSR with a one-byte selector (saves 12 bytes, kills the scam
token surface, rules out community-token pricing); or drop opcodes 4/5/6 from
v1 entirely and let markets be an off-chain layer. Both are consensus changes
and both are free *today*.

---

## Indexer behaviour

### A-16 · Invalid is not an error — **cheap**
`internal/indexer/apply.go`

A block that breaks a protocol rule stays in the event log (the log records
what the chain contained) and is skipped by the projection (the rules say it
does nothing). Only a storage fault is returned as an error. Otherwise one
malformed block from an anonymous sender halts every indexer on the network.

### A-17 · Reorgs replay from a checkpoint, not from genesis — **cheap**
`internal/store/snapshot.go`, `internal/indexer/indexer.go`

The original decision was to replay the whole event log on every reorg, on the
argument that an inverse for every projection write is a second implementation
of the state machine and the two drift. That argument is still right, and
checkpointing does not weaken it, because **a checkpoint does not invert
anything — it moves the base case of the same replay forward**:

```
Rebuild()        base case = empty state at startHeight-1, replay everything
RebuildFrom(S)   base case = the checkpoint at S,          replay events > S
```

One code path, two starting points. `Apply` is untouched, so there is no second
implementation to disagree with the first.

The rollback half is mechanical rather than semantic, and that is what makes it
safe. Every insert-only projection carries the height of the event that wrote
it, so undoing them is `DELETE ... WHERE height > S` — a statement that encodes
no protocol rule and therefore cannot drift from one. Only `balances` and
`dispensers` mutate in place, and those two are copied verbatim into the
checkpoint; both are small, with no content blobs, which is what keeps a
checkpoint from being a copy of the database. (`consumed_tails` gained a
`height` column so it is not the exception.)

**Defaults: a checkpoint every 120 momentums, retained to cover 360** — one
hour at NoM's ~10 s interval, as asked. Pruning keeps everything inside the
window *plus the newest checkpoint below it*, so the guaranteed depth is a
floor rather than an average: right after a prune there is still something at
least 360 momentums back, not one sitting at the window's edge.

Checkpoints are skipped during backfill. A momentum a thousand deep is not
going to be reorged, and checkpointing through an initial sync of a million
momentums would copy the balance table thousands of times to produce
checkpoints the next prune deletes. Crash recovery does not need them either:
the event log survives a restart, so a restart resumes at the tip.

**The drift guard is a test, not a promise.**
`TestSnapshotRestoreMatchesFullRebuild` builds a log touching every projection
— mints, a multi-part chain, a composite, transfers, a burn, a dispenser taken
through escrow, purchase and cancel — checkpoints mid-history, and asserts that
restore-and-replay produces a byte-identical `ProjectionDigest` to replaying
from nothing. If a future opcode ever mutates a table that is neither
insert-only-with-a-height nor checkpointed, that test goes red instead of the
divergence being silent.

A checkpoint is never authoritative. `zcribe snapshot -drop` deletes every one
of them and costs replay time on the next reorg and nothing else.
`zcribe reindex` deliberately ignores checkpoints entirely: it exists to answer
"does the log still produce this state", and starting from a checkpoint would
assume the answer for everything below it.

### A-17b · A chain that goes backwards is a reorg, not silence — **cheap**
`internal/indexer/indexer.go`

Reorg detection compared a momentum's parent hash against the stored one, which
only runs for momentums the node still has. A devnet wiped and restarted under a
running indexer produces a node whose tip is *below* the indexer's, so no
momentum was ever fetched, no comparison ever ran, and the indexer went on
serving the dead chain's inscriptions while reporting itself caught up. This was
observed: one long-running instance held 29 events and 11 assets from a chain
that no longer existed, while a fresh index of the same node found 5 events and
4 assets.

Three changes close it, and they are separate because the failures are:

- **A node tip below ours is investigated, not ignored.** If the node's
  momentum at its own tip matches ours it is the same chain and merely behind,
  which is a wait; if it differs, the history we hold is gone and it is handled
  by the ordinary reorg path, however deep.
- **The chain identifier is pinned in `meta` on first sight** and compared on
  every poll. Pointing an existing database at another network is an ordinary
  operational mistake whose failure is otherwise silent — two chains interleaved
  in one event log, with no rollback that can separate them — so it stops the
  indexer rather than being retried.
- **The fork search is a binary search.** Walking back one height at a time is
  fine for a three-momentum fork and pathological for a reset, where every
  height disagrees and the walk costs one round trip per momentum ever indexed.
  Doubling strides to find an agreeing height, then bisecting, makes a total
  reset about 2·log₂(depth) requests instead of seventeen thousand.

### A-18 · The state root commits to the *applied* set — **consensus**

```
stateRoot(h) = sha3_256( stateRoot(h-1) ‖ uint64_be(h) ‖ orderedAppliedTxHashes(h) )
stateRoot(startHeight - 1) = 32 zero bytes
```

The plan gave the shape and wrote "orderedValidTxHashes". Three details it did
not pin, all of which two indexers must agree on or they disagree at every
height:

1. **`h` is a big-endian uint64**, matching `common.Uint64ToBytes`.
2. **The base case is 32 zero bytes** at `startHeight - 1`.
3. **"Valid" means *applied*, not *parseable*.**

The third was implemented the wrong way first, and `zcribe compare` found it.
The original build committed to every block that decoded as a well-formed ZDU.
Two indexers were then run against the same devnet with one consensus constant
changed between them (`MaxAssetParts` 64 vs 4, so one rejected a 14-block
inscription the other accepted) — and they reported **identical state roots**
while holding different assets. The commitment could not see the one class of
disagreement it exists to detect.

Fixed: each event carries an `applied` flag, set by the R-7 rules, recomputed
on every replay, and the root commits to that set. Re-running the same
experiment now localises the divergence to the exact momentum and names the
block. See R-6.4.

*Cost of reversing:* consensus. Every root in the chain changes.

### A-18b · `GET /state` publishes a projection digest — **cheap**
`internal/store/snapshot.go`

The state root proves two indexers agree about **which events applied**. It
cannot prove they agree about **what those events produced**: a projection bug
on one side leaves the applied set identical and the roots equal while the
balances differ. `ProjectionDigest` hashes every projection table in a fixed
order with length-prefixed columns, and `GET /state` publishes it.

Diagnostic, never consensus — no rule refers to it, and moderation is excluded
so two operators with different policies still match. It is also what the
checkpoint test compares.

### A-19 · Only send blocks carry inscriptions — **consensus**

`blockType == 2`. A receive block's data is not authored by the account that
signs it; treating it as an inscription would let anyone inscribe from any
address that ever received a transaction. Implied by the plan, not stated.

### A-20 · Canonical order is verified, not trusted — **cheap**

The node returns blocks parallel to `momentum.content`, and this indexer
re-derives the sort by `(address, big-endian height, hash)` and compares. On
disagreement it uses its own answer and logs a warning. This is the payoff of
V-1: the ordering is recomputable, so trusting the node is a choice rather
than a necessity.

### A-21 · Batching uses `ledger.getDetailedMomentumsByHeight` — **cheap**

**This answers V-4, which the plan left open.** The method returns momentums
*with their account block bodies*, up to `RpcMaxCountSize = 1024` per call, and
`PrefetchMomentum` builds the block array parallel to `momentum.content`. One
call per 100 momentums instead of one `getAccountBlockByHash` per block. A full
devnet sync of 12,569 momentums takes about twelve seconds.

### A-22 · Balances are `uint64`, stored as decimal TEXT — **cheap**

Supply is bounded at 2⁶⁴−1 (R-4.3) and SQLite's INTEGER is signed 64-bit, so
INTEGER would silently overflow above 2⁶³−1. Go's `uint64` holds the full
range natively, and the total in circulation is the asset's supply, so moving
units between addresses cannot overflow.

### A-23 · Dispenser escrow is a balance held at the dispenser id — **cheap**

The dispenser's own id acts as a pseudo-address in the balances table. Keeps
"supply is conserved" as a single invariant rather than two accounting systems
that have to agree. Dispenser ids are 64-hex and addresses are `z1…`, so they
cannot collide.

They also cannot be *read*, which is what A-35 fixes at the serving edge.

### A-24 · Two confirmation levels: soft and full — **free**
`internal/api/api.go` · spec R-6.3

| Level | Meaning | JSON |
|---|---|---|
| **soft** | this indexer has parsed the block and applied it | `status: "soft"` |
| **full** | `-confirmations` momentums sit above it (default 60) | `status: "full"`, `confirmed: true` |

Soft confirmation is not a depth — it is "has anyone seen this yet" — so
everything the API returns is at least soft; an inscription that is not
soft-confirmed is one the indexer has never heard of. `confirmations` counts
the inscription's own momentum, so a mint in the momentum just processed
reports 1 rather than 0.

The viewer badges the two differently. The old label for the first state was
"pending", which undersold it: the inscription is real, indexed and renderable,
and the only thing outstanding is finality.

**V-3 remains open** — nobody has confirmed what depth exchanges actually use
on NoM, so 60 is still the plan's number rather than a measured one. It is a
flag, affects only what the API reports, and never affects the state root.

**Note the tension with A-17, deliberately left in.** The checkpoint window is
360 momentums and full confirmation is 60, so the indexer is prepared to unwind
six times deeper than it calls an inscription final. That is the safe direction
for the inequality to point, and `zcribe serve` warns if it is inverted. An
operator who would rather make one claim instead of two sets
`-confirmations 360`.

### A-25 · Start height defaults to 1 — **free**

D-2 resolved that nothing carries over, so 743270 is not a constraint. On
devnet, 1 is correct. On mainnet this becomes the launch parameter and must be
set by every operator to the same value, since it is the base case for the
state root chain (A-18). It is also, in all but name, the protocol's one
activation height — see the note in A-6b.

### A-26 · Protocol addresses live in one namespace, `z1zen0glyphx…` — **consensus**
`internal/protocol/protocol.go` · spec R-8.6

Every address this protocol owns now begins `z1zen0glyphx` and then spells its
function, padded with `x`:

```
z1zen0glyphx <function> xxxxxxxx…              + bech32 checksum
z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9     the burn sink
```

This adapts the convention **NoM uses for its own protocol addresses** —
`z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp`,
`z1qxemdeddedxt0kenxxxxxxxxxxxxxxxxh9amk0` — with one deliberate difference,
and it is the one that matters.

**No keypair can reach this namespace, at any price.** A NoM address is 20
bytes whose first is a type byte: `0x00` for a user address, `0x01` for a
contract. Both values leave the top five bits zero, so the first bech32
character is always `q` and every address the ledger can mint begins `z1q`:

| type byte | reachable openings |
|---|---|
| `0x00` user | `z1qq` `z1qp` `z1qr` `z1qz` |
| `0x01` contract | `z1qy` `z1q9` `z1qx` `z1q8` |
| `0x16` this namespace | `z1ze` — reachable by neither |

`types.PubKeyToAddress` hardcodes byte 0 to `0x00`, so no key that exists or
could ever be generated lands in `z1ze…`. This is what the two earlier
namespaces could not offer: both sat inside user space, where the prefix was
merely *expensive* to forge — about 2²⁵ hashes for `z1qzenx` (minutes) and 2⁵⁵
for `z1qzen0glyphx`. Here a lookalike cannot be produced at all, and the
addresses are unspendable **by construction** rather than by improbability.
That closes [M-10](REVIEW-ADVERSARIAL.md), which was open against exactly this
gap.

**What it rests on** is that nothing in NoM blesses byte 0 = `0x16`; it works
because the ledger never asks. The only type-byte check anywhere in go-zenon is
`types.IsEmbeddedAddress` (byte 0 == 1), and on the receiving side it gates one
thing (`chain/momentum/ledger_store.go`):

```go
othStore.MarkAsUnreceived(block.Hash)          // every address, unconditionally
if types.IsEmbeddedAddress(block.ToAddress) {
    othStore.SequencerPushBack(block.Header()) // contracts only
}
```

A send to `0x16` fails that test and therefore takes the ordinary path: marked
unreceived, and left there. Contract space is the only space with a
restriction, and it is what killed the first sink below.

**Verified on devnet rather than argued.** A 1 ZNN send to
`z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9` confirmed in momentum 49671 and sits
unreceived at `accountHeight: 0`, balance `{}`. All five devnet nodes logged
**nothing** — no error, no warning, not even a mention of the address, at
`LogLevel: info`. The control is the differential: a send to a contract-space
address naming no contract is refused at the plasma quote, before a block is
built, with `contract doesn't exist`. Syrius accepts the address too; its
validator is `Address.isValid`, a bech32 round-trip with no type-byte check
(`syrius/lib/utils/input_validators.dart:127`).

*The residual risk* is a node release that starts rejecting sends to an
unrecognised type byte. That would be a new consensus rule, it would fail
**visibly** — the send is refused, nothing is silently lost — and the sink is
the only address here ever sent to: escrow addresses are rendered, never
funded.

Three addresses were discarded getting here:

| | |
|---|---|
| `z1qxzenscrypty0nxdurnxxxxxxxxxxxxxxtsa9t` | first byte `0x01` — a contract address. The node refuses every send: `rpc error -32000: contract doesn't exist`. Unspendable *and* unsendable, so useless as a sink. |
| `z1qqzenscryptdurnxxxxxxxxxxxxxxxxxhuj660` | works, but spends 13 characters on a prefix before saying what the address is for. |
| `z1qzenxdurnxxxxxxxxxxxxxxxxxxxxxxxaw8z2e` | works and reads well, but names the *network* rather than this protocol, and its prefix is a ~2²⁵ grind. |

`durn` rather than `burn` because the bech32 charset excludes `b`, `i`, `o` and `1`;
the `0` in `zen0glyphx` is the same substitution for `o`.

The namespace costs 10 of the 32 data characters, leaving **22 for the function
name** — more than any function here needs.

**This answers V-5**, and with a stronger result than expected — the question
was never just "is it unspendable" but "can it receive at all". Verified on
devnet: a burn to the new address settles and the units leave circulation.

Constructing another one: choose 32 bech32 data characters starting
`zen0glyphx`, pack them into 20 bytes, and let the checksum follow.
`TestBurnAddressIsUsable` and `TestNamespaceIsUnreachableByAnyKeypair` pin the
rules — the second enumerates every address both type bytes can reach and fails
if any of them collides with the namespace.

---

### A-40 · `application/wasm` is on the allowlist, binary class — **consensus**
`internal/protocol/mime.go` · spec R-8.1

WebAssembly is inscribable. It is **ClassBinary**, so no viewer renders it: a
module reaches a page only when an inscribed document fetches and instantiates
it deliberately (R-8.5), and the executing document is the HTML doing that,
which is already sandboxed.

Verified on devnet: a 155-byte kernel and a 745-byte engine, inscribed
separately, linked at instantiation through the composite slots. 900 bytes of
WebAssembly driving an interactive fractal explorer.

The measurement that shaped it: a valid module is 8 bytes, useful ones are
tens to hundreds, and Go's `println("hi")` is **1.8 MB** — past the 1,020 KB
single-asset ceiling. Any toolchain shipping a GC is excluded by arithmetic.

*To change:* removing it invalidates every wasm inscription. Free before
mainnet, a version bump after.

### A-41 · The content CSP grants `'wasm-unsafe-eval'`, not `'unsafe-eval'` — **cheap**
`internal/api/api.go` · spec R-8.1.1

Without a grant, WebAssembly cannot compile at all — and the failure is
misleading, because `validate()` still returns true (it is only a parse) while
`instantiate()` throws a CompileError. Verified in Chrome against this exact
policy, the narrow token permits WebAssembly while `eval()` and `new Function()`
both still throw, and the pinned fetch directives are untouched. `'unsafe-eval'`
would also work and would additionally re-open `eval`, for no gain.

*To change:* serving-layer only; `-scripts=false` already turns the whole
execution surface off.

### A-42 · `application/evm` is one mime, and the operation lives in `ext` — **consensus**
`internal/protocol/mime.go` · `internal/evm/` · spec R-8.1

Contract code and calldata are both raw bytes with no self-describing
structure, so one entry covers both and the *operation* — deploy or call —
is declared in the extension lane (R-2.9), which this protocol defines as
ignorable and never interprets.

That seam is the whole point: an EVM exists on top of Zenoglyphs **without a
single change to Zenoglyphs consensus beyond this allowlist entry.** An indexer
that has never heard of the EVM files an `application/evm` mint like any other
asset and stays exactly as correct as before. World state is derived by
replaying those inscriptions in canonical order — the same relationship the
indexer already has with its own event log.

Two decisions inside it are worth not re-deriving:

- **A contract address is `0x16 ‖ keccak(assetId)[13:]`** — a commitment to the
  artifact, because the asset id already commits to the bytes. So an address
  names its own code, and there is no CREATE2-plus-SELFDESTRUCT redeploy trick:
  you cannot swap the code at an address, only deploy different code, which is
  a different address. The forced first byte costs eight bits of the 160 and
  buys a guarantee: `0x16` is reachable by no keypair (A-26) and is not NoM's
  contract byte, so a contract address can never collide with somebody's
  account and never lands where a send is handed to the sequencer. It was
  unconstrained until 2026-09-10, which put roughly one contract in every 256
  inside genuine user space; changing it changed every contract address and the
  devnet was re-launched rather than migrated
  (`research/evm-address-scheme.md`).
- **A NoM address *is* an EVM address.** Both are 20 bytes, mapped unchanged,
  so an inscriber's EVM identity is their NoM identity with no registration and
  no forgeable sender — the account already in someone's Syrius extension is
  the EVM account, and the explorer renders the `z1…` spelling everywhere and
  accepts either spelling everywhere. Whether an address is one a wallet may
  *pay* is carried as a separate `sendable` flag rather than by withholding its
  name — the protocol already publishes `z1zen0glyphxdurn…` and
  `z1zen0glyphxescr0w…`, which are just as unspendable.
- **Gas is priced by NoM's own two clocks.** The burned fee is a fixed fraction
  of the paying token's supply, which is a pure function of the epoch, times
  NoM's dynamic-plasma resource price, which is read off the carrier block's
  hash-committed `fusedPlasma`/`difficulty` and floored at 1. Both terms are
  computable from block bytes alone, which is what keeps two derivers replaying
  the same history years apart in agreement — an oracle or a price feed could
  not be (`research/evm-fee-design.md`). The fee rides the carrier block as a
  send to the burn sink, so the burn is provable by the recipient rather than
  promised, and it begins at an activation height pinned per chain in
  `evm.FeeFromHeight` — never a flag, because an operator who can set it with a
  command-line argument is an operator who can fork by typo. Mainnet has no
  entry, and adding one is a governance decision.

*To change:* removing the entry invalidates every EVM inscription. The
derivation rules are not Zenoglyphs consensus at all — they are the EVM
metaprotocol's, and live in `research/evm-prototype.md`.


## Serving and rendering

### A-27 · Two origins in development, two **hostnames** in production — **cheap**
`deploy/` · spec R-8.2

D-3 requires a separate origin. Two ports on `localhost` are different origins
by the same-origin policy, so that is correct for local use. Production is now
written down rather than left as a warning: `deploy/README.md`, with a
`Caddyfile`, an `nginx.conf`, and systemd units.

Three things in there are not obvious:

1. **Use two registrable domains, not two subdomains.** A subdomain is a
   different *origin*, which stops script access, but not a different *site*: a
   cookie set with `Domain=.example.org` reaches every subdomain, including the
   one running attacker-authored HTML. Nothing sets a cookie today, so a
   subdomain is survivable — but "we must never add a cookie" is a worse
   guarantee than "the browser cannot carry one across".
2. **The CDN must not rewrite bytes.** An inscription's id is a commitment to
   its bytes, so a cache feature that "optimises" them serves something the
   chain never committed to while the URL still says it did. On Cloudflare that
   means Auto Minify, Rocket Loader, Mirage, Polish, Email Obfuscation and
   Server-side Excludes all **off** for the content host. Compression is fine —
   the bytes are restored before the page sees them.
3. **The proxy must not add a `Content-Security-Policy`.** The indexer builds
   one per response and it is the sandbox; a browser given two CSP headers
   enforces both, so an added one intersects with it and either breaks
   recursion or loosens the sandbox depending on which directive was stricter.

Cache rules: `/content/*` for a year (immutable by construction),
`/r/momentum` never (it is the chain tip), `/r/asset/*` and `/r/refs/*` for
60 s (confirmation counts move), everything else bypass. Cache on the full URL
and do **not** normalise the trailing slash — `/content/{id}`,
`/content/{id}/` and `/content/{id}/0` are three different resources.

### A-27b · Moderation reaches the CDN — **cheap**
`cmd/zcribe/moderate.go`

A year-long immutable TTL means an id blocked at the origin keeps being served
from cache. A block that stops at the database is not a block, so
`zcribe moderate` now:

- **works on ids this indexer has not seen** (`-unseen`). Waiting until content
  is indexed before being allowed to refuse it means serving it first.
- **imports and exports lists** (`-import`, `-import-url`, `-export`), in one
  text format, so operators can share lists without agreeing on anything else.
  A remote list is untrusted input and the read is bounded.
- **purges the cache** (`-purge`) via Cloudflare or a generic webhook, and
  prints the exact URLs — all three shapes per id — when no credentials are
  configured. `-purge-help` documents the environment.

None of this touches the event log, validity, or the state root (R-6.5). An
operator who hides an asset still agrees with every other operator about what
the chain contains, and anyone who disagrees with the list runs their own
indexer and serves it. That is what makes it acceptable for an operator to keep
a list at all — and it is why this is called withholding rather than deletion.

### A-28 · Inscribed scripts execute, in an opaque origin, pinned to the content origin — **cheap**
`internal/api/api.go`, `web/ui/src/core/composables/useIndexer.ts` · spec R-8.5

Previously: `<iframe sandbox="">` with no tokens, so nothing executed. That was
the right default before there was anything to compose. Recursion needs script
execution to exist at all, so the default flipped, with two hard rules:

- **`allow-scripts` yes, `allow-same-origin` never.** The frame keeps an opaque
  origin, so one inscription's script cannot reach another inscription frame's
  DOM or storage even though both come from the same host. Same-origin is not
  needed for recursion: the content origin sends
  `Access-Control-Allow-Origin: *`, so a fetch from an opaque origin still
  reads the response. Granting it would buy nothing and cost the isolation.
- **Every CSP fetch directive is pinned to the content origin.** So an
  inscription may compose itself out of other inscriptions and *out of nothing
  else* — no CDN, no analytics, no author-controlled URL that can be repointed
  after the mint. Without that, "this artwork is on the chain" would be a claim
  about one file rather than about what renders.

`zcribe serve -scripts=false` restores the old behaviour, `/info` publishes
which it is, and the viewer reads that field to set its own iframe sandbox — so
the page can never render more than the origin is willing to serve.

The residual risk is the honest one: arbitrary JavaScript from strangers runs
in visitors' browsers, bounded by the sandbox and by their browser. `ord` made
the same trade, and it is why recursive inscriptions exist there at all.

### A-29 · Attributes are opaque CBOR, converted to JSON only at the serving edge — **cheap**

The store keeps the exact bytes the block committed to. `map[any]any` keys are
stringified and byte strings hex-encoded for JSON. A round trip through JSON
would not be injective.

### A-30 · The viewer has no build step and no CDN — **free**

Vanilla HTML/CSS/JS embedded in the binary, including a small markdown
renderer written out rather than imported. The viewer has to work offline
against a local devnet, and a page that silently depends on a remote script
renders differently depending on the network.

This is about the **viewer**, and is untouched by A-27's CDN: that one sits in
front of inscribed content, which is immutable and needs no build step either.

### A-31 · The golden-vector digest is sha3-256 of the ZDU bytes, not the block hash — **cheap**

The plan asked for "expected CBOR hex, expected block hash". A block hash
commits to height, previous hash, plasma, and nonce — none of which this
protocol defines — so it cannot be pinned by a wire-format vector. The digest
pins exactly what this layer decides.

---

## Composability

### A-32 · Composites: `refs` recorded, never resolved — **consensus**
`internal/protocol/wire.go`, `internal/store/refs.go` · spec R-4.7

Phase 6 of the plan, implemented. A mint may declare
`refs: [assetId, …]` — up to 16, in slot order — and the indexer records the
edges. This is the "list of sequential imports, referenced like variables" from
the original design notes, arrived at with one change of mechanism that turned
out to matter.

**Slot addressing instead of `zen:N` placeholders.** The plan proposed
resolving `zen:0`, `zen:1` inside the content, client-side. That would mean
rewriting inscribed bytes before rendering them, which breaks the one property
the whole design rests on: what renders is what the block committed to. Instead
the content origin serves slot *n* at `/content/{id}/{n}`, and an asset's
canonical URL carries a trailing slash so it is its own base:

```html
<script src="0"></script>     <!-- refs[0] -->
<img src="1">                 <!-- refs[1] -->
```

Nothing is rewritten, nothing is parsed, and it works for CSS `url()` and SVG
`href` too — which a JavaScript placeholder resolver never could. The slot is a
**301 redirect** to the imported asset's own canonical URL, so one blob keeps
one cache entry however many composites import it, and a moderation policy on
the target applies at the target.

**Three rules, and the third is the one to remember:**

- *A ref is a claim, not a dependency.* The indexer never fetches the target,
  never requires it to exist, and never lets state depend on it. Otherwise an
  indexer that had synced further would accept a mint one that had not would
  reject, and the two would disagree about history for a reason unrelated to
  the chain.
- *Present means non-empty.* `refs: []` is a second spelling of "no refs", and
  two encodings of one object is what R-2.3 exists to prevent. Duplicates are
  fine — slots are positional.
- *No cycle detection is needed, and this is provable.* A ref names a block
  hash; a block hash cannot be known before the block exists; and `refs` lives
  *inside* the block whose hash becomes the asset id. So an inscription can
  never reference itself, every edge points strictly backwards in canonical
  order, and the graph is a DAG by construction. **The plan called for cycle
  detection at the storage layer. It is not needed** — only a depth cap (8) to
  bound client-side resolution work. This is the opposite of the `pHash` walk
  (R-7.1.4), which happens at index time over attacker-chosen pointers and
  genuinely can loop.

*To change:* removing `refs` is a consensus change and would orphan every
composite. Raising the 16-slot cap is one too.

### A-33 · Recursion is a serving decision, not a protocol one — **cheap**
spec R-8.5

The other half of Phase 6, and the half that costs nothing. An inscribed page
loading another inscription's bytes at render time needs no wire format at all
— `/content/{id}/` already exists and all inscriptions share one origin. It was
blocked only by A-28's sandbox, and A-28 has changed.

The two mechanisms compose: a page reaches an import as `"0"` (recursion, doing
the fetching) because slot 0 was declared in `refs` (the composite, saying what
the inscription is made of). Either works without the other.

Four recursion endpoints live on the **content origin**, not the API origin,
because a relative reference is the only kind an inscription can carry without
hard-coding where an indexer is deployed: `/r/momentum`, `/r/asset/{id}`,
`/r/refs/{id}`, `/r/children/{id}`. `ord` puts its `/r/blockheight` and
`/r/children/<id>` in the same place for the same reason.

Verified end to end on devnet, in a real browser: a `text/javascript`
inscription defining a drawing library, and a `text/html` inscription
declaring it as `refs[0]` and loading it with `<script src="0">`, which then
reads `/r/momentum` and renders a canvas seeded from the chain tip. Nothing but
on-chain bytes and the indexer's own endpoints.

### A-34 · The Telegram front end is an API client — **cheap**
`cmd/zcribe/bot.go`

The design notes wanted a Telegram bot as the *first* client. It is the second,
which makes it the more useful one: it talks to the HTTP API and the content
origin from outside the process, with no database access, exactly as any
third-party client would. Two consequences are the reason it is built that way:

- **Moderation is enforced without the bot knowing the rules.** A withheld
  asset returns 410 from the content origin and the bot reports that. An
  operator who blocks an id does not have to configure the bot as well.
- **Content is uploaded as bytes, never handed over as a URL.** Telegram cannot
  reach a devnet on localhost, and on mainnet a URL hand-off would make
  Telegram's fetch — not the operator's policy — decide what gets served.

`/get` renders by the class the indexer derived from the mime allowlist: images
as photos, audio and video natively, text and code inline, everything else as a
file. HTML and SVG go out as files rather than being rendered, because Telegram
has no sandbox to run them in and the bot will not build one.

The token comes from `ZCRIBE_TELEGRAM_TOKEN`, never a flag in production:
arguments land in the process table where any local user can read them.

### A-35 · Escrow is displayed as a namespaced address, not re-keyed — **cheap**
`internal/protocol/address.go` · `internal/api/provenance.go` · spec R-8.6

A-23 keys escrow by the dispenser id, which is correct as accounting and wrong
as presentation: a 64-hex block hash appears in a column of `z1…` addresses,
and a dispenser fill reads as a transfer from an anonymous whale nobody can
look up. Every protocol-owned address is supposed to live in the `z1zen0glyphx`
namespace (A-26) and spell what it is; escrow was outside it entirely.

Two ways to fix that, and they cost very different things:

1. **Change the ledger key.** `escrowHolder` synthesises a real namespaced
   address and `store.Move` uses it. Clean, and it changes what
   `ProjectionDigest` hashes over — so every indexer on the network has to
   upgrade in the same momentum or `zcribe compare` reports a divergence that
   is not one.
2. **Format it on the way out.** The key stays the dispenser id; the API
   renders it as `z1zen0glyphxescr0w…` wherever an address is rendered, and carries
   the dispenser id alongside so a client can still link to the listing.

The second, on the same precedent that keeps moderation out of the state root:
a presentation decision must not be able to make two honest indexers disagree
about history. The address is a display convention that no key anywhere depends
on, and the file that defines it says so in as many words.

It is one-way. Thirty-two data characters minus eleven for `qzenx` + `escr0w`
leaves 105 bits, and a dispenser id is 256 — so the address is a digest, not a
truncation, and `GET /address/{addr}` resolves one by matching every dispenser
rather than by inverting it. A truncation would have looked like an id and
invited someone to paste half of one into a lookup that would not find it.

*To change:* moving to option 1 later is a coordinated upgrade, not a patch.

### A-36 · Issuance and escrow legs are reconstructed at the serving edge — **cheap**
`internal/api/provenance.go`

Three balance movements never wrote a `transfers` row, because the opcodes
behind them call `SetBalance` or `Move` directly: the mint itself, stock going
into a dispenser's escrow, and stock coming back out on a cancel. The visible
result was a provenance feed whose oldest entry was an asset's first *move*
rather than its origin, and in which a purchase arrived from an address that had
never been funded.

Writing those rows in the indexer would put them in `transfers`, which
`ProjectionDigest` hashes over — the same lockstep-upgrade problem as A-35. So
they are rebuilt when the feed is served, out of the event log, which already
holds every fact needed: the create payload carries the escrowed amount, the
dispensers projection carries the issuer and the height, and what a cancel
released is what was escrowed minus what the recorded sales took.

Each row carries a `kind` (`mint`, `transfer`, `burn`, `escrow`, `release`,
`sale`) and the reconstructed ones are flagged `synthetic`, so a client can tell
which rows two indexers must agree on byte for byte and which are this one's
reading of the log. Every row still cites a real block hash.

*To change:* if every deployment can upgrade together, the indexer can write
these rows and this code deletes itself. Until then the digest is the reason not
to.

### A-37 · Both servers keep an access log; the content origin's is quieter — **free**
`internal/api/accesslog.go`

Neither server logged anything at all: no method, path, status or latency, so
"the viewer is blank" and "the content origin is refusing everything" looked
identical from the operator's side. One line per response now, from the logger
the API server was already carrying and never using.

The content origin logs successes at **debug** and failures at info. It answers
every recursion fetch an inscribed page makes, so a single composite with
sixteen imports is seventeen lines that say nothing; `-v` brings them back when
someone is actually debugging recursion.

Two things are deliberately absent, and both are the same rule as `noStore` in
`admin.go`: **no request headers** — the admin session cookie is the credential,
and a log that copies it turns log-read access into panel access — and **no
bodies**, because the login handler's body is the password. Query strings are
logged: they carry filters this API hands to anyone who asks, and no credential
travels in one.

### A-38 · SQLite, and the conditions under which it stops being the answer — **cheap**
`internal/store/store.go`

Worth answering with numbers rather than instinct, because the honest answer
today is that the storage engine is not the limit.

**What comparable systems chose.** Almost every chain indexer in production is
on an embedded store, for the same reasons this one is: the index is derived,
single-writer, and rebuildable. `ord`, the Bitcoin ordinals indexer this
protocol is modelled on, uses redb — embedded, one process — across millions of
inscriptions. electrs uses RocksDB. Bitcoin Core's own UTXO set is LevelDB. The
projects that reach for Postgres or MySQL are the *explorers* — mempool.space
runs MariaDB plus Elasticsearch — and they do it for many-client analytical
queries, not for indexing throughput.

**What actually binds here, in order.**

1. **Content blobs, not row count.** An asset carries up to 1 MB (64 parts of
   16 KB) in an `assets` row. A million inscriptions averaging 50 KB is a 50 GB
   database — fine for SQLite, whose documented ceiling is 281 TB — but the
   blobs are what evict the page cache. The first real fix was not a new
   engine: `ListAssets` used to `SELECT content` while the API used only
   `len(content)`, so every gallery page read up to twenty megabytes of bytes
   it discarded. It now selects `length(content)`. Content-addressed storage
   (listed as not-built below) would take the blobs out of the row entirely and
   is the next step, still inside SQLite.
2. **One writer, one connection.** `db.SetMaxOpenConns(1)` serialises
   everything, reads included, behind the indexer's writes. This is the first
   thing real read load will hit, and it is a configuration change rather than
   a migration: WAL mode already permits concurrent readers, so a second
   read-only connection pool is the fix. It has not been made because nothing
   has needed it.
3. **Reads that scan.** `dispenserForEscrow` walks every dispenser and
   `AppliedEventsWithOpcode` walks every cancel. Both are bounded by rare
   events today and both would want an index before they were not.

**When to switch.** Not on inscription count. Switch when one of these is true,
and prefer the smallest move that fixes it:

- **More than one process must write.** SQLite's model is one writer, and a
  horizontally scaled ingest is the one thing it cannot be configured into.
  That is the real Postgres trigger.
- **Read concurrency saturates the connection** after a read-only pool has been
  tried and writes still starve.
- **Queries the projections are not shaped for** become a product requirement —
  full-text search over descriptions, aggregate analytics across holders. That
  is the mempool.space case, and the answer is a second system *alongside*
  SQLite rather than a replacement: the event log stays authoritative and the
  search index is another disposable projection.

None of this is a consensus concern. The event log is the source of truth and
every projection is rebuildable, which is exactly what makes changing engines a
migration script and a replay rather than a fork.

### A-39 · Telegram identity is a signed challenge; credit is the operator's, not the protocol's — **cheap**
`internal/identity` · `internal/botstore` · `cmd/zcribe/botcredit.go` · `cmd/zcribe/sign.go`

A bot can read anyone's address; what it cannot do is know that the person
typing is the person holding the key. Everything built on on-chain identity —
crediting a payment to the account that sent it, minting into someone's own
wallet — needs that link, and needs it to be unforgeable by someone who merely
knows the address.

The challenge is the ordinary shape: the bot picks a nonce, the holder signs a
statement containing it, and the bot checks the signature against the public key
**and** that the key hashes to the claimed address. Only the second half ties a
proof to an address rather than to a key.

What gets signed is the raw UTF-8 of the message, with no prefix, hash or
envelope — because that is what the wallets do. Syrius signs WalletConnect's
`znn_sign` by handing the string's code units straight to ed25519, and the Dart
SDK's `KeyPair.sign` does the same; a scheme that hashed first would be
unsignable by the wallet people actually have. Domain separation therefore lives
in the text: the first line names the protocol, and the address, the Telegram
account id and an expiry are all inside the signed bytes, so a proof cannot be
replayed by another account or lifted into another protocol.

Nothing in the ecosystem could produce such a signature — znn-cli has no
sign-message command and syrius only signs over WalletConnect — so `zcribe sign`
exists to close the loop.

**The 1,000 PP gate does not contradict A-2.** A-2 removed a *protocol* fee, and
that stands: no rule anywhere checks a balance before an inscription is valid,
and anyone with their own key and plasma mints for free. A-39 charges for using
**this operator's** key and plasma through **their** bot — an off-chain account
with an off-chain balance, in a database the indexer never reads. A different
operator charges differently, or nothing, and both index the same chain
identically.

Two decisions inside it are worth stating:

- **A deposit is credited before it is received, and an unattributable deposit
  is never received at all.** On NoM, value only lands when the recipient
  publishes a receive block, so declining to receive is a real choice. A payment
  from an address nobody has linked stays unreceived: the sender can link it
  later and be credited, and the operator has not taken delivery of money it
  cannot attribute. Receiving first would turn every unattributable payment into
  the operator's.
- **The debit happens after the mint publishes.** A block that failed to publish
  costs the operator nothing and must cost the caller nothing; a block that
  published cannot be un-published. The balance is therefore checked before and
  spent after, and the per-user cooldown covers the gap between the two.

---

## Not implemented

Stated so the gaps are not mistaken for oversights.

| Area | Status |
|---|---|
| **Recursion and composites** (Phase 6) | **Done.** See A-32, A-33, spec R-4.7 and R-8.5. Verified in a browser against the devnet. |
| **Telegram bot** (Phase 4) | **Done.** See A-34. `/start /help /info /recent /asset /get /refs /address /collections`, plus `-announce` and a one-shot `-post`. |
| **Checkpointing** | **Done.** See A-17: every 120 momentums, covering a 360-momentum reorg, with an equivalence test against full replay. |
| **Two-instance state-root comparison** (Phase 2 exit) | **Done.** Two indexers on separate databases synced the devnet independently and produced identical `stateRoot` and identical projections. |
| **Forced-rollback test** | Partly. Replay is exercised against real devnet data and unit-tested for idempotence, and the checkpoint path has its own equivalence test. The reorg path is now driven against a fake node in `internal/indexer/reorg_test.go`, covering a shallow fork, a chain reset below the tip, a node that is merely behind, and a wrong-network database. No reorg has been *induced* on a live devnet. |
| **Divergence detection** | **Done, and it earned its keep.** `zcribe compare` binary-searches two indexers' state roots; run against deliberately mismatched builds it localised the divergence to one momentum in 15 queries and exposed A-18. `GET /state` now adds the projection digest that catches what the state root structurally cannot (A-18b). |
| **Content-addressed storage** | Not built. Would let A-11's tail rule be relaxed safely, and would deduplicate identical content across assets. No protocol impact either way. |
| **Dynamic plasma auto-fusing** | The inscriber asks the node what a block costs and pays it; it will not fuse QSR for you. On devnet the dev account is already fused. |
