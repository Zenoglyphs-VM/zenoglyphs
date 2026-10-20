# Zenoglyphs — v1 wire specification

**Status:** draft for review · **Protocol version:** 1 · **Prefix:** `glyph`

Every rule here is numbered and testable. A change to any of them changes
which inscriptions exist and is therefore a consensus change, not a bug fix.
The golden vectors in [`testdata/vectors/v1.json`](../testdata/vectors/v1.json)
are this document in executable form; a reimplementation that reproduces them
byte for byte agrees with this one.

---

## R-1 Scope

Zenoglyphs is a metaprotocol. It runs entirely inside data that the Network
of Momentum already carries.

**R-1.1** — No consensus change. No fork, no new embedded contract, no change
to `go-zenon`.

**R-1.2** — No new node requirement. Pillars and full nodes see ordinary
0-amount sends with a `data` field they already relay and already price.

**R-1.3** — No protocol fee. Plasma already prices data (R-9), and a fee would
introduce a trusted recipient. If a fee is ever added it must be a burn, never
a payment.

**R-1.4** — Indexers are optional, permissionless, and mutually verifiable
(R-6). A user with a full node can validate the entire history alone.

---

## R-2 The ZDU envelope

A **Zenoglyph Data Unit** is the CBOR map placed in `AccountBlock.data`.

```
{
  "p":      "glyph",   ; tstr  — protocol prefix
  "op":     0..23,     ; uint  — opcode (R-3)
  "v":      1,         ; uint  — protocol version
  "ext":    {...},     ; map   — optional, ignorable extensions (R-2.9)
  "pHash":  h'..',     ; bstr  — optional, exactly 32 bytes, multi-part only
  "tHash":  h'..',     ; bstr  — optional, exactly 32 bytes, multi-part head only
  "data":   ...        ; payload, shape determined by op (R-4)
}
```

**R-2.1** — `p` MUST equal `glyph`. A ZDU with any other prefix is not a
Zenoglyphs inscription and MUST be ignored.

The prefix only has to be unique, and this rule rejects every ZDU that does not
match it exactly, so a second metaprotocol on NoM is not locked out by this
choice — it picks different bytes and the two never see each other's blocks.

**R-2.2** — Encoding is CBOR in **RFC 8949 §4.2 deterministic** form: map keys
sorted bytewise by their encoded form, definite-length maps and strings,
shortest-form integers, no indefinite lengths, no duplicate keys.

**R-2.3** — A block whose data is not in canonical form is **invalid**, even
if it decodes. This makes the encoding injective, which is what lets a block
hash stand as a commitment to its content. An implementation MAY verify this
by decoding and re-encoding and comparing bytes.

> Under R-2.2 the v1 key set encodes in the order `p, v, op, data, pHash,
> tHash`. Length-first (RFC 7049) and bytewise (RFC 8949) canonical orderings
> coincide for these six keys.

**R-2.4** — `op` and `v` MUST each be in `0..23`, so each encodes in a single
CBOR byte. This keeps the byte-signature fast path a fixed-length comparison:
an indexer can reject a non-ZDU block without a full parse, which matters
because it sees every block in every momentum. Opcodes 24 and above are
reserved for a future version willing to pay the second byte.

**R-2.5** — `pHash` and `tHash`, when present, MUST be exactly 32 bytes,
encoded as CBOR byte strings. Hex text is invalid — it costs 64 bytes to say
the same thing.

**R-2.6** — An indexer MUST ignore any ZDU whose `v` exceeds the version it
implements, and MUST implement every version at or below it. Half-applying an
unknown version's rules corrupts state silently, which is worse than visibly
lagging. `v` is reported at `GET /info` so a client can refuse a stale indexer.

**R-2.7** — The key set is exact. An envelope carrying any key other than the
seven above is invalid. A decoder MUST NOT ignore unknown keys: permissive
parsing is the most reliable way for two indexers to diverge.

**R-2.8** — `data` MUST be present and non-empty.

### R-2.9 The extension lane

Zenoglyphs has **three** upgrade lanes, in decreasing order of disruption:

| Change | Mechanism | Old indexers |
| --- | --- | --- |
| The ruleset itself | bump `v` (R-2.6) | refuse the whole ZDU |
| A field that changes meaning | a new top-level key | refuse the whole ZDU (R-2.7) |
| A field that is purely additional | a key inside `ext` | **ignore it, and stay correct** |

`ext` is an optional CBOR map, permitted in the envelope and in every
map-shaped payload (R-4). An indexer that does not recognise a key inside it
MUST ignore that key and MUST NOT treat the ZDU as invalid.

**R-2.9.1** — `ext`, if present, MUST be a map. "Ignore what you do not
recognise" is only well-defined over keys; a bare integer or string offers
nothing to skip, so an indexer would have to guess whether it mattered.

**R-2.9.2** — The canonical encoding of `ext` MUST NOT exceed **2048 bytes**,
with nesting depth ≤ 4. Without a bound, `ext` is a second content channel
carrying arbitrary bytes under no declared type — which is precisely what the
R-8.1 allowlist exists to prevent.

**R-2.9.3 — Nothing inside `ext` may affect protocol state.**

This is stricter than Runes' odd tags, and R-6.4 is the reason. If an `ext`
key could change a balance, an indexer that understood it and one that did not
would both mark the event applied, commit to the *same* state root, and hold
*different* state — reintroducing exactly the blindness the state root exists
to remove. So `ext` is **consensus-inert**: carried on the chain, committed to
by the block hash (and therefore by `assetId`), served to clients, and never
read by the state machine. Anything that must change state uses one of the
other two lanes.

The practical consequence is a strong guarantee: an old indexer and a new one
compute **identical state** for every block, forever. The newer one simply
shows its clients more.

**R-2.9.4 — Why the key is `ext` and not `x`.** Canonical CBOR sorts keys
bytewise by encoded form. `"x"` encodes as `0x6178`, which sorts *between*
`"v"` (`0x6176`) and `"op"` (`0x626f70`) — landing inside the byte signature
and destroying the fixed-length fast path in R-2.4. Every 3-character key
encodes with a `0x63` header and therefore sorts after all 1- and 2-character
keys, placing `ext` safely between `op` and `data`. Two extra bytes, paid only
when the field is used.

**R-2.9.5 — What `ext` is for.** Non-normative; these are examples, not a
registry. The rule is only that a key here changes no state, so the test for
whether something belongs in `ext` is: *if half the indexers ignored this, would
anyone hold a different balance?* If yes, it is not an `ext` key.

| Key | Carries | Why it is not a protocol field |
| --- | --- | --- |
| `license` | `"CC0-1.0"`, an SPDX id | A licence is a claim by the issuer. No indexer can check it and none should try. |
| `alt` | alt text for an image | Accessibility metadata. A viewer that renders it is better; one that does not is not wrong. |
| `sig` | a detached signature over the content by an off-chain key | Ties an inscription to an identity the chain has never heard of — an artist's existing key, a studio's. Verification is the client's business. |
| `src` | a URL for the generator that produced the art | Provenance for generative work. It is a pointer off-chain, so it can rot; that is exactly why it must not be load-bearing. |
| `seed` | the RNG seed a generative piece was rendered from | Lets anyone reproduce the output. A renderer uses it; the state machine has no opinion. |
| `lang` | BCP-47 tag for text content | Display and search. |
| `origin` | `{"chain": "bitcoin", "id": "…i0"}` | "This is a port of that inscription." A claim about another chain, unverifiable here by construction. |
| `royalty` | `{"bps": 500, "to": "z1…"}` | A request to marketplaces. Recording it costs nothing; **enforcing** it would need a rule, and a rule is not what this lane is for. |
| `theme` | `{"bg": "#0b0e14"}` | A hint for how a gallery should frame the piece. |

Two anti-examples, both of which look like they fit and do not:

- **A supply change, an owner override, a fee waiver.** Anything that moves
  state must use a new top-level key or a version bump (R-2.9.3).
- **More content.** `ext` has no declared type, so bytes in it bypass the R-8.1
  allowlist entirely. The 2048-byte cap is there to make that impractical
  rather than merely discouraged.

### R-2.10 On activation heights (non-normative)

Ethscriptions gates rule changes on a block height; Ordinals did the same once,
at block 824544, when "cursed" inscriptions became ordinary ones; Runes went
live at 840000. The mechanism is worth naming because Zenoglyphs **does not
have it**, and the absence is a decision rather than an oversight.

A version bump and an activation height are not two spellings of the same idea:

| | version bump (`v`) | activation height |
| --- | --- | --- |
| Scope | one inscription, chosen by its author | every inscription, from height *H* |
| Old indexer at the change | refuses the new ZDU, stays **correct** about everything else | keeps applying the old rule and is **wrong**, silently |
| Can change how *existing* inscriptions are read | no | yes |
| Needs coordination | no | yes — a flag day |

`v` covers every change where new inscriptions want to do something new. It
cannot cover one case: changing how inscriptions **already on the chain** are
interpreted. That is the case Ordinals actually had — thousands of malformed
inscriptions existed, and the question was what they had always meant — and it
is the only case that justifies a flag day.

Zenoglyphs is unlikely to have that case, because R-3.3 already answers it:
malformed input is ignored entirely, never partially applied, never recorded as
a lesser class of thing. The permanent second class of asset that forced
Ordinals' flag day is the thing R-3.3 exists to prevent.

Two further notes:

- **One activation height already exists.** `startHeight` (A-25) is the height
  at which the protocol itself begins, it is a base case of the state root
  chain (R-6.4), and every operator must configure the same value. It is the
  precedent to follow if a second one is ever needed.
- **A silent divergence would not stay silent.** If an activation height were
  added and half the indexers missed it, R-6.4 makes the disagreement visible
  at the first affected momentum and `zcribe compare` localises it in about
  twenty queries. That is the property that makes a flag day survivable, and it
  is worth having before one is ever attempted.

---

## R-3 Opcodes

| op | Name | Payload | Effect |
| ---: | --- | --- | --- |
| 0 | `mint` | mint wire object (R-4.1) | Creates an asset. `assetId` = this block's hash |
| 1 | `transfer` | `{a: bstr, n: uint}` | Moves balance |
| 2 | `multi` | `bstr` chunk | Non-terminal part of a multi-part mint. Creates no state alone |
| 3 | `createCollection` | `{name, description?, url?}` | Creates a collection. `collectionId` = block hash |
| 4 | `createDispenser` | `{a, n, price, zts}` | Escrows `n` units for sale |
| 5 | `cancelDispenser` | `{d: bstr}` | Releases escrow to the issuer |
| 6 | `dispenserPurchase` | `{d: bstr}` | Buys from a dispenser |

**R-3.1** — The numeric value is the wire value. Opcodes are never renumbered
and only ever appended.

**R-3.2** — An unknown opcode makes the ZDU invalid. It MUST NOT be an error:
an indexer that halts on `op: 99` can be stopped by anyone for the price of
one block.

**R-3.3 — There is exactly one outcome for malformed input: it is ignored
entirely, never partially applied.**

A block that breaks any rule in this specification changes no state. It is not
burned, not quarantined, not recorded as a lesser class of inscription, and
not applied up to the point of failure. It stays in an indexer's event log —
because the log records what the chain contained — and it contributes nothing
to state and nothing to the state root (R-6.4).

This rule exists because the alternative is what produced Ordinals' "cursed"
inscriptions: undefined behaviour around malformed input created a permanent
second class of asset and required a coordinated flag day at block 824544 to
resolve. The specific choice matters less than that it is written down. Runes
burns; burning is disproportionate in an account model, where there is no
UTXO to destroy and the sender keeps their balance either way.

---

## R-4 Wire schemas

Wire schemas are **not** API schemas. Derived fields — issuer, owner, momentum
height, inscription number — come from the block and the indexer, never from a
payload. A payload that could assert its own issuer would let anyone claim
anything.

**R-4.1 — mint**

```
{ name?: tstr, description?: tstr, collection?: bstr(32),
  supply: uint, decimals: uint, attributes?: map,
  mime: tstr, data: bstr, refs?: [bstr(32), ...] }
```

**R-4.2** — `data` is always a byte string. Text content is UTF-8 bytes under
a text mime type. Permitting a text-string alternative would give the same
content two encodings for no benefit, and would let a multi-part chain mix the
two across chunks.

**R-4.3 — bounds.** All consensus-relevant. A payload exceeding any of them is
invalid, never truncated.

| Field | Bound |
| --- | --- |
| `name` | ≤ 128 bytes, valid UTF-8 |
| `description` | ≤ 1024 bytes, valid UTF-8 |
| `attributes` | ≤ 4096 bytes encoded, nesting depth ≤ 4 |
| `supply` | ≥ 1, ≤ 2⁶⁴−1 |
| `decimals` | ≤ 18 |
| `mime` | present, ≤ 128 bytes, in the R-8.1 allowlist |
| `collection` | absent, or exactly 32 bytes |
| `refs` | absent, or 1..16 entries, each exactly 32 bytes (R-4.7) |
| `ext` | absent, or a map ≤ 2048 bytes encoded, depth ≤ 4 (R-2.9) |

**R-4.4** — Validation is by **exact key set**, not by map size. A payload
carrying an unrecognised key is invalid — with the single exception of `ext`
(R-2.9), whose *contents* are ignorable.

**R-4.6** — Every map-shaped payload accepts an optional `ext` under the rules
in R-2.9. `op=2` payloads are bare byte strings and therefore have no payload
lane; a chunk that needs an annotation uses the envelope's `ext`.

### R-4.7 Composites

A mint may declare the inscriptions it is built from:

```
refs: [ bstr(32), bstr(32), ... ]     ; 1..16 asset ids, in slot order
```

This is the "list of sequential imports, referenced like variables" from the
original design notes. Slot *n* is the *n*th entry, and slots are positional:
the order is the meaning.

**R-4.7.1 — Bounds.** At most **16** entries, each exactly 32 bytes. If the key
is present the array MUST be non-empty: an explicit `refs: []` is a second
spelling of "no refs", and two encodings of one object is what R-2.3 exists to
prevent. Duplicates are permitted — `refs[0]` and `refs[3]` may name the same
asset — because slots are positional and a rule against it would protect
nothing.

**R-4.7.2 — A ref is recorded, never resolved.** An indexer stores the edge. It
MUST NOT fetch the target, MUST NOT require the target to exist, and MUST NOT
let any state depend on it. A ref naming an id nobody has inscribed is a
dangling edge that a client renders as missing — not an invalid mint.

This is the load-bearing half of the rule. If validity depended on the target
being present, an indexer that had synced further would accept a mint that one
that had not would reject, and the two would disagree about history for a reason
that has nothing to do with the chain.

**R-4.7.3 — The reference graph is acyclic by construction, so no cycle
detection is required.** A ref names a block hash. A block hash cannot be known
before the block exists, and `refs` lives *inside* the block whose hash becomes
the asset id. So an inscription can never reference itself, and every edge
points strictly backwards in canonical order. Constructing a cycle would mean
finding a preimage of sha3-256.

This is the difference between `refs` and the multi-part chain walk, which
follows `pHash` and *does* need the cycle detection in R-7.1.4 — that walk
happens at index time over pointers an attacker chose, and reaching a repeated
hash there is cheap. Composite resolution happens client-side and only needs a
**depth cap** (8) to bound work, not to prevent a loop.

**R-4.7.4 — Slot addressing.** The content origin serves slot *n* of asset *id*
at:

```
{contentOrigin}/content/{id}/{n}      →  301 to {contentOrigin}/content/{refs[n]}/
```

which is why an asset's canonical content URL carries a trailing slash
(R-8.6). With `/content/{id}/` as the base, inscribed bytes reach an import
with the relative reference `"0"`, `"1"`, `"2"` — and never have to embed a
64-character hash the author could not have known at authoring time:

```html
<script src="0"></script>
<img src="1">
```

Serving the slot as a **redirect** rather than a copy is deliberate: one content
blob keeps one canonical URL, so a cache stores it once however many composites
import it, and a moderation policy on the target applies at the target instead
of having to be mirrored onto every importer.

**R-4.7.5 — What `refs` buys over a plain URL.** An inscribed page can already
name another inscription by writing its full id into a `src` attribute; that
needs no protocol at all (R-8.5). `refs` adds two things a URL cannot:

- **Discoverability.** A client can answer "what is this built from" and "what
  is built on this" without parsing content it may not be able to parse.
- **Provenance.** The import list is inside the block, so it is committed to by
  the block hash and therefore by the asset id. A `src` attribute is committed
  to as *text*; the list of things this inscription declares itself made of is
  committed to as *structure*, and slot addressing means the author never wrote
  a URL that could have pointed somewhere else.

**R-4.5 — other payloads**

```
transfer          { a: bstr(32), n: uint, ext? }        ; n >= 1
createCollection  { name: tstr, description?, url?, ext? }  ; name 1..128 bytes
createDispenser   { a: bstr(32), n: uint, price: uint, zts: bstr(10), ext? }
cancelDispenser   { d: bstr(32), ext? }
dispenserPurchase { d: bstr(32), ext? }
multi             bstr                                  ; non-empty, no payload ext
```

---

## R-5 Canonical ordering

> **Canonical order = (momentum height ASC, index within `momentum.content` ASC).**

**R-5.1** — Every indexer MUST apply events in this order. It is not a
heuristic and needs no tiebreaker: NoM already provides a total order.

**R-5.2** — The ordering is **recomputable, not merely observable**.
`NewMomentumContent` (`chain/nom/momentum_content.go`) canonically sorts the
content before hashing it:

```go
sort.Slice(content, AccountBlockHeaderComparer(content))
// comparer: bytes.Compare(a.Bytes(), b.Bytes())
// AccountHeader.Bytes() = Address ‖ BigEndian(Height) ‖ Hash
```

`MomentumContent.Hash()` hashes the concatenation in that order, and that hash
feeds `Momentum.ComputeHash()`. An indexer SHOULD therefore verify the order it
was served rather than trusting it.

**R-5.3** — Because `Uint64ToBytes` is big-endian, lexicographic byte order
equals numeric order, so the effective sort key is
**(address, account-block height, hash)**. Two consequences:

- **Per-account causality is preserved.** A sender's own transfers always
  apply in chain order, which is what makes single-account double-spend
  impossible.
- **Cross-account ties break by address bytes, not time.** A lower address
  always sorts first, permanently. This is deterministic and safe, but an
  adversary could grind a low-prefix address to win *same-momentum* races.
  Relevant only to dispenser purchases and bounded by the ~10 s momentum
  interval. Documented, not fixed.

**R-5.4** — Two conflicting transfers in the same momentum: the earlier
`content` index wins and the later is invalid. Not "both fail", not "last
wins" — **first wins**.

**R-5.5** — Inscription numbers are assigned by counting valid mints in
canonical order. They are derived, never on-chain, and are recomputed from
scratch after a rollback.

**R-5.6** — Only **send** blocks (`blockType == 2`) may carry an inscription.
A receive block's data is not authored by the account that signs it.

---

## R-6 Reorgs, finality, and indexer agreement

**R-6.1 — Reorg detection.** On each new momentum, compare `previousHash`
against the stored hash at `height − 1`. On mismatch, walk back to the last
agreeing height *H*, delete every event above *H*, drop the projections, and
replay.

**R-6.2 — Projections are derived.** Every table other than the event log and
the momentum chain MUST be reproducible by replaying the log in canonical
order. Recomputation is the only supported rollback: an inverse for every
projection write is a second implementation of the state machine, and the two
drift.

An implementation MAY checkpoint state periodically and replay from the
checkpoint instead of from the start (A-17). That is still recomputation — the
base case of the replay moves, the state machine does not change — so it is an
optimisation with no protocol visibility. A checkpoint is never authoritative:
losing every one of them costs replay time and nothing else.

**R-6.3 — Finality, at two levels.** They answer different questions, so they
are reported as two different things.

| Level | Meaning | Reported as |
| --- | --- | --- |
| **soft** | This indexer has parsed the block and applied it. The inscription exists and renders now. It can still be undone by a reorg. | `status: "soft"` |
| **full** | The configured depth of momentums sits above it (default **60**, ~10 minutes). The inscription number is settled. | `status: "full"`, `confirmed: true` |

Soft confirmation is not a depth. It is the answer to "has anyone seen this
yet", and everything the API returns is at least soft — an inscription that is
not soft-confirmed is one the indexer has never heard of. `confirmations`
counts the inscription's own momentum, so a mint in the momentum just processed
reports 1.

Inscription numbers are provisional until full confirmation. A client that
displays one before then should say so.

> **Confirmation depth is not the reorg window.** An indexer also keeps
> checkpoints so that a reorg does not replay the whole event log (A-17), and
> that window defaults to 360 momentums — deeper than the 60 at which it calls
> an inscription final. The inequality points that way on purpose: being able
> to unwind further than you promise is the safe direction. An operator who
> would rather make one claim instead of two sets the confirmation depth to the
> checkpoint window.

**R-6.4 — State root.**

```
stateRoot(h) = sha3_256( stateRoot(h-1) ‖ uint64_be(h) ‖ orderedAppliedTxHashes(h) )
```

`stateRoot(startHeight − 1)` is 32 zero bytes.

**`orderedAppliedTxHashes(h)` is the block hashes of the events that *changed
state* at height h, in canonical order** — not every block that parsed as a
ZDU. The distinction is the whole value of the commitment. An earlier draft of
this rule said "valid", and an implementation read that as envelope validity;
the result was that two indexers enforcing different opcode rules — a
different part limit, a missed issuer check, a different bound — admitted the
same blocks to their logs and computed *identical* roots while holding
different balances. A commitment that cannot see the disagreement it exists to
detect is worse than none, because it is trusted.

So an indexer MUST record, per event, whether the R-7 rules applied it, and
MUST commit to that set. The flag is derived state: it is recomputed on every
replay, and a rules change between runs must change it — and therefore change
the root.

Two indexers agreeing on `stateRoot(H)` agree on the entire history to H. When
they disagree, agreement is monotone in height, so a binary search over
`GET /stateroot/{h}` finds the exact divergent momentum in about log₂(H)
queries — roughly twenty for a mainnet-scale chain.

**R-6.5** — Moderation MUST NOT affect the state root. It is a per-indexer
serving policy; two honest operators with different policies must still agree
on history.

---

## R-7 Per-opcode validation

### R-7.1 Mint

**Standalone** (`op=0`, neither hash): payload is a complete wire object with
non-empty `data`.

- `assetId` = the carrier block's hash
- `issuer` = `block.address`
- `owner` = `block.toAddress` — a mint to yourself is the ordinary case; a
  mint to someone else issues directly to them
- The full `supply` is credited to `owner`
- Never record an unconfirmed block. A mint at momentum height 0 is a bug, not
  a pending inscription: skip the block and index it when it confirms.

**Multi-part**: head (`op=0`, **both** hashes) → intermediaries (`op=2`,
`pHash` only) → tail (`op=2`, neither). Reassembly starts at the head and
walks `pHash`, concatenating head-first, terminating when the walk reaches
`tHash`. A head carrying only one of the two hashes is invalid.

Four rules bound the walk, all because it follows attacker-chosen pointers:

- **R-7.1.1 Same issuer.** Every part MUST have `block.address` equal to the
  head's. Without this, anyone can build a head pointing into someone else's
  chunks and mint a duplicate for the cost of one block. This is the most
  important rule in the section.
- **R-7.1.2 Strictly earlier.** Every part MUST precede the head in canonical
  order.
- **R-7.1.3 One head per chain.** A tail may be consumed by at most one head;
  the first head in canonical order wins, later ones are invalid.

  The reason is storage amplification, not ownership. R-7.1.1 already stops
  anyone else from reusing your chunks, so a second head over the same chain
  could only be *your own* — but each head is a separate asset with its own
  content, so N heads over one 1 MB chain cost every indexer N megabytes for
  the price of N single blocks. Without this rule the ratio between what an
  attacker pays and what an indexer stores is unbounded.

  Intermediaries are not claimed, only tails, so one issuer reusing their own
  chunks across chains is permitted (A-11). And the thing this rule appears to
  forbid — many inscriptions sharing one payload — is what composites are for:
  a mint that declares `refs: [otherAssetId]` and renders slot `"0"` costs 32
  bytes and stores the payload once (R-4.7).
- **R-7.1.4 Depth cap and cycles.** At most **64** parts (~1 MB). Visited
  hashes MUST be tracked and the walk aborted on repeat — a `pHash` cycle
  otherwise walks forever.

Publishing runs in reverse (tail first, head last), because each part
references a hash that must already exist. That ordering satisfies R-7.1.2 for
free.

### R-7.2 Transfer

`op=1`. Sender = `block.address`, recipient = `block.toAddress`.

- The block's own ZTS and amount are irrelevant and MUST be ignored. Send 0
  ZNN.
- Applied on **send-block confirmation**. NoM sends are unilateral, so the
  recipient need not receive the block.
- Amount ≥ 1 and ≤ the sender's balance at that point in canonical order.
- Self-sends are valid no-ops.
- Transfers to `z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9` **burn** the amount.
  It is the burn slot of the protocol address namespace (R-8.6).

### R-7.3 Collection

`op=3`. `collectionId` = block hash, `issuer` = `block.address`.

An asset's `collection` field binds **only if `asset.issuer ==
collection.issuer`**. Otherwise the asset stays valid and the reference is
dropped. Without this, anyone can claim membership in any collection.

### R-7.4 Dispensers

**`op=4`** requires the issuer to hold ≥ `n` of `a` at that point in canonical
order. That balance is **escrowed** — moved out of spendable, held against the
dispenser — so it cannot be sold twice or transferred out from under a buyer.

**`op=6`** requires all of:

- the payment block's `toAddress` **is the dispenser's issuer**. Checking only
  that a payment exists lets a buyer pay themselves and claim the asset;
- the block's token standard equals the dispenser's `zts`;
- `amount ≥ price`.

Quantity is `floor(amount / price)`. If that exceeds `remaining`, the **whole
purchase is invalid** — no partial fills, because there is no refund path and
a partial fill silently overcharges. The remainder below one unit is a tip to
the seller.

**`op=5`** releases escrow to the issuer. Issuer only, and only once.

---

## R-8 Serving content

**R-8.1 — MIME allowlist.** A mint declaring a type outside this list is
invalid. Comparison is byte-for-byte against the listed spelling; the string
is not parsed, so `text/plain` and `text/plain;charset=utf-8` are distinct and
only the listed one is valid.

```
application/cbor              image/avif                text/css;charset=utf-8
application/evm               image/gif                 text/html;charset=utf-8
application/json              image/jpeg                text/javascript;charset=utf-8
application/pdf               image/png                 text/markdown;charset=utf-8
application/wasm              image/svg+xml             text/plain;charset=utf-8
application/yaml              image/webp                video/mp4
audio/mpeg, audio/wav                                   video/webm
font/otf, font/ttf, font/woff2
model/gltf-binary
```

`application/wasm` and `application/evm` are both **binary class**: no viewer
renders either inline. WebAssembly reaches a page only when an inscription
fetches and instantiates it deliberately (R-8.5), and EVM bytecode is executed
by a deriver replaying the log, never by a browser. What an EVM artifact *means*
— a contract deployment or a call — is not in the mime; it is declared in the
extension lane (R-2.9), which keeps one entry sufficient and keeps this
protocol from having to know what an EVM is.

**R-8.1.1 — WebAssembly needs an explicit CSP grant.** An indexer serving
`application/wasm` for inscriptions to instantiate MUST include
`'wasm-unsafe-eval'` in the content origin's `script-src`. Without it
`WebAssembly.validate()` still returns true — it is only a parse — and
`instantiate()` fails with a CompileError, so the failure looks like a broken
module rather than a missing policy. Use that token and not `'unsafe-eval'`:
verified in Chrome against this exact policy, the narrow one permits
WebAssembly while `eval()` and `new Function()` both still throw.

**R-8.2 — Separate origin.** Content bytes MUST be served from a different
origin than the API. The allowlist includes `text/html` and `image/svg+xml`,
both of which execute script; same-origin serving would hand that script the
API's origin.

**R-8.3 — Headers.** Content responses MUST carry a
`Content-Security-Policy` beginning with the `sandbox` directive and
`X-Content-Type-Options: nosniff`, and MUST be served with the mime type
recorded at mint time.

**R-8.5 — Script execution and recursion.**

An indexer MAY permit inscribed HTML and SVG to execute. Doing so is what makes
**recursion** possible: an inscribed page fetching another inscription's bytes
at render time and using them as a library, a data file, or a trait layer —
`ord`'s shared-`p5.js` pattern. It needs no protocol support, because
`/content/{id}/` already exists and every inscription shares one content origin.

An implementation that permits it MUST:

- grant `allow-scripts` in the sandbox and **never** `allow-same-origin`. The
  frame then has an opaque origin, so one inscription's script cannot reach
  another inscription frame's DOM or storage even though both are served from
  the same host. Same-origin is not needed for recursion: the content origin
  answers with `Access-Control-Allow-Origin: *`, so a fetch from an opaque
  origin still reads the response.
- pin every CSP fetch directive (`default-src`, `script-src`, `style-src`,
  `img-src`, `media-src`, `font-src`, `connect-src`, `frame-src`) to the
  content origin, plus `data:`/`blob:` where a renderer needs them, and set
  `base-uri 'none'` and `form-action 'none'`.

The second requirement is what makes the claim worth something: **an inscription
may compose itself out of other inscriptions and out of nothing else.** No CDN,
no analytics, no author-controlled URL that can be repointed after the mint.
Without it, "this artwork is on the chain" would be a statement about one file
rather than about what renders.

An indexer that does not permit script execution serves the same bytes with the
bare `sandbox` directive and renders every inscription as a static document.
Both are conforming; the choice MUST be published at `GET /info` so a client can
render the same policy the origin enforces.

**R-8.6 — URL shape and protocol addresses.**

An asset's canonical content URL ends in a slash:

```
{contentOrigin}/content/{id}/          the bytes
{contentOrigin}/content/{id}           301 to the above
{contentOrigin}/content/{id}/{n}       301 to composite import slot n (R-4.7.4)
```

The trailing slash is load-bearing: it makes the asset its own base URL, which
is what lets inscribed bytes address an import as `"0"` rather than as a hash.
The short form redirects rather than serving, so a page can never end up with a
base URL that would resolve `"0"` against `/content/` and silently fetch a
different asset.

Recursion endpoints live on the **content origin**, because a relative
reference is the only kind an inscription can carry without hard-coding where an
indexer happens to be deployed:

```
GET /r/momentum         the chain tip this indexer has reached
GET /r/asset/{id}       one asset's metadata
GET /r/refs/{id}        a composite's imports, and what imports it
GET /r/children/{id}    the ids in a collection
```

**Protocol-owned addresses** all begin `z1zen0glyphx` and then spell their function,
padded with `x`:

```
z1zen0glyphx <function> xxxxxxxx…            + bech32 checksum
z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9   the burn sink (R-7.2)
```

This adapts the convention NoM uses for its own embedded contracts
(`z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp`). The namespace claims 10 of the 32
data characters, leaving 22 for the function name.

**The prefix is a lock, not merely a label.** A NoM address's first byte is a
type byte — `0x00` user, `0x01` contract — and both leave the top five bits
zero, so every address the ledger can mint begins `z1q`. This namespace begins
`z1ze` (byte 0 = `0x16`), which neither type byte can reach, and key derivation
hardcodes byte 0 to `0x00`. No keypair can therefore land in this namespace at
any grind cost, and every address in it is unspendable by construction rather
than by the improbability of a 152-bit preimage.

Sends to such an address are ordinary. The only type-byte check in NoM is
"is this a contract" (byte 0 == 1), which decides whether a send is handed to
the contract sequencer; `0x16` fails that test and is marked unreceived like
any user address. A conforming indexer needs to know only that R-7.2's sink is
the exact address above.

**R-8.4 — Moderation.** Per-indexer policy applied at the serving layer only:
`metadataPermitted` hides an asset from listings, `assetDataPermitted`
withholds its bytes. Neither affects validity, the event log, or the state
root (R-6.5).

---

## R-9 Block limits and cost

Mirrored from `vm/constants/plasma.go`; enforced by the node, not by this
protocol.

```
plasma(len)                 = len · 68 + 21000
MaxDataLength               = 16384                  ; ErrABDataTooBig above this
MaxPoWPlasmaForAccountBlock = 4.5 · 21000 = 94500
  → PoW-only max data       = (94500 − 21000) / 68 = 1080 bytes
plasma(16384)               = 1,135,112 → ~541 QSR fused
```

| Tier | Max block data | Cost | Fits |
| --- | ---: | --- | --- |
| **PoW** | **1,080 B** | feeless | transfers, text, small SVG, collection and dispenser ops |
| **Fused** | **16,384 B** | ~541 QSR fused | images and anything larger |

Chunking is a client-side packing concern and never a protocol rule: an
indexer accepts any chain of blocks the network accepted, however it was
split. The base layer already tiers the cost of data, which is R-1.3's whole
argument.

---

## R-10 Golden vectors

[`testdata/vectors/v1.json`](../testdata/vectors/v1.json) carries, for each
opcode: a description, the object, the canonical CBOR hex, and the sha3-256 of
those bytes.

The digest is over the **encoded ZDU**, not the account block hash. A block
hash commits to height, previous hash, plasma, and nonce — none of which this
protocol defines — so it cannot be pinned by a wire-format vector.

`zcribe vectors -check` verifies the file against the current encoder. Any
change that moves a vector is a consensus change by definition.

---

## Appendix — Composability, and how the two mechanisms differ

Both are now normative and both are implemented, but they are still commonly
conflated, so it is worth stating what each one is and what it costs.

| | **Recursion** (R-8.5) | **Composites** (R-4.7) |
| --- | --- | --- |
| What it is | an inscribed page loads another inscription's bytes at render time | a mint declares the inscriptions it is built from |
| Where it lives | inside the content, as an ordinary URL | in the wire format, as `refs` |
| Who resolves it | the browser | the client, via slot URLs the content origin serves |
| Does the indexer know | no — it never parses content | yes — it records the edges, and resolves nothing |
| Wire-format cost | none | one payload key |
| Cycles | a browser-tab problem, bounded by browser limits | impossible by construction (R-4.7.3) |
| What it buys | composition | composition **plus** discoverability and provenance |

They compose. A page reaches an import as the relative URL `"0"` (that is
recursion, doing the fetching) because slot 0 was declared in `refs` (that is
the composite, saying what the inscription is made of). Either works without
the other: an inscription can hard-code another id in a `src` attribute and
declare no `refs`, or declare `refs` and never fetch them.

The one thing to understand about the pairing is that `refs` is a **claim**,
not a dependency. The indexer never checks that a declared import exists,
never fetches it, and never lets state depend on it, so a composite whose
imports have all been withheld by an operator is still a valid inscription with
a valid supply — it just does not render. That separation is what keeps
composition entirely out of consensus.
