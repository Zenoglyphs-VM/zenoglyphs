# Zenoglyphs guide

A metaprotocol for the Zenon Network of Momentum: inscriptions carried in
ordinary 0-amount account blocks, indexed by anyone, rendered by anything.

- No consensus change, no new embedded contract, no new node requirement.
- Pillars see 0-amount sends with a `data` field they already relay and price.
- Indexers are optional, permissionless, and mutually verifiable.
- No protocol fee — plasma already prices data.

**Picking this up mid-stream?** `HANDOFF.md` is the current state,
what is running, and what is deliberately unfinished.

**Read first:** [`spec/SPEC.md`](../spec/SPEC.md) is the frozen v1 wire format;
[`ASSUMPTIONS.md`](../ASSUMPTIONS.md) is every judgement call this implementation
made and what it costs to change.

---

## What's here

```
spec/SPEC.md         the v1 specification — numbered, testable rules
ASSUMPTIONS.md       every decision made, with the cost of reversing it
internal/protocol/   the frozen wire format: canonical CBOR, opcodes, bounds, chunker
internal/indexer/    momentum scanning, canonical ordering, validation, projections, reorgs
internal/store/      SQLite event log (truth) + projections (derived, droppable)
internal/api/        REST API, the sandboxed content origin, and the admin panel's API
internal/moderation/ blocklist format + CDN purging, shared by the CLI and the admin panel
internal/inscribe/   block building, PoW, signing, publishing
internal/rpcclient/  JSON-RPC client for a NoM node
web/ui/              the viewer's source: Vue 3 + Vite + Tailwind 4 + nom-ui
web/ui/admin/         the admin panel's own entry — see web/ui/README.md
web/ui/src/wallet/    publishing from the browser: CBOR, keccak, bech32, Syrius
web/dist/            its build output — no CDN at runtime, embedded in the binary
cmd/zcribe/          the CLI, including the Telegram front end
deploy/              mainnet: two hostnames, a CDN that must not rewrite bytes
testdata/vectors/    golden vectors: the spec in executable form
contracts/           hand-assembled EVM and wasm artifacts + the assemblers that build them
research/            open questions, measured rather than argued — not normative
                     evm-todo.md is the live concern list for the EVM prototype
                     evm-address-scheme.md is how native addresses and Syrius work
```

The load-bearing idea: **the event log is the source of truth and projections
are disposable.** Reorgs, double-spend, index numbers, and cross-indexer
agreement all become tractable once state is a pure function of
`(event log, canonical order)` and can be recomputed.

## Build

Requires Go 1.23+ and the `go-zenon` submodule (clone with
`--recurse-submodules`): `go.mod` carries
`replace github.com/zenon-network/go-zenon => ./go-zenon`, so block hashing,
PoW, and key derivation come from the consensus implementation rather than a
reimplementation that can drift. The submodule is upstream go-zenon, pinned to
the commit this was built and tested against.

```sh
go build -o zcribe ./cmd/zcribe
go test ./...
```

The viewer is a Vue application that `go build` embeds from `web/dist/`, which
is committed — a checkout without it does not compile. Rebuild it after
changing anything under `web/ui/`:

```sh
cd web/ui && npm install --allow-git=all && npm run build
```

`--allow-git=all` is needed because `nom-ui` is a GitHub dependency and npm 12
refuses git specs by default. See [`web/ui/README.md`](../web/ui/README.md) for the
dev server, which proxies the API so the viewer can be developed against a live
devnet with hot reload.

The viewer can also *publish*, through the Syrius extension, which means it
builds inscription envelopes itself. Those bytes have to match what Go
produces exactly — an asset id is sha3 over them, and the indexer rejects
anything its own encoder could not have written — so there is a check that
binds the two, against the golden vectors and against a real inscription taken
off the chain:

```sh
cd web/ui && npm run verify:vectors
```

## Run it against the devnet

Bring up the five-node devnet first (see
[`go-zenon/docker/devnet/README.md`](../go-zenon/docker/devnet/README.md)):

```sh
cd go-zenon && make devnet-up
```

Then, in this directory:

```sh
# index from genesis and serve the API, content origin, and viewer
./zcribe serve -start 1 -confirmations 3

# in another shell: inscribe one document of every renderable class
./zcribe demo -devnet
```

Open **http://localhost:8710/**. The viewer's own
[docs page](http://localhost:8710/#/docs) covers the protocol, the API and how
to run an indexer, and reads its numbers from the running indexer rather than
from this file.

The devnet compose file also brings up the static Zenon explorer on
`:36000`, which is where the viewer's hash links point when it detects chain
69. On mainnet they point at zenonhub.io. `-explorer` overrides the choice —
`zenonhub`, `devnet`, `none`, or any URL template containing `{v}`.

`-devnet` uses the BIP-39 phrase committed to `go-zenon/docker/devnet` — public
by design, worthless outside chain 69. Account index 1 there is pre-fused with
42 M plasma, which is enough for full 16 KB blocks.

## The CLI

```
zcribe serve       run the indexer, API, content origin, and viewer
zcribe inscribe    publish a file as an inscription
zcribe transfer    move units of an asset, or burn them
zcribe collection  create a collection
zcribe dispenser   create, cancel, or buy from a dispenser
zcribe address     print the address a key derives to
zcribe sign        sign a challenge, proving you control an address
zcribe reindex     rebuild projections and state roots from the event log
zcribe compare     find where two indexers stop agreeing
zcribe moderate    set this indexer's serving policy for an asset
zcribe snapshot    inspect or manage projection checkpoints
zcribe bot         run the Telegram front end
zcribe vectors     write (or -check) the golden vectors
zcribe demo        inscribe a sample set of documents
```

Examples:

```sh
./zcribe inscribe -devnet -file art.png -name "Study #1" -supply 100
./zcribe inscribe -devnet -file doc.md -ext '{"license":"CC0-1.0"}'
./zcribe transfer -devnet -asset <id> -amount 25 -to z1qp3y...
./zcribe transfer -devnet -asset <id> -amount 5 -burn
./zcribe dispenser create -devnet -asset <id> -amount 20 -price 100000000
./zcribe dispenser buy -devnet -index 3 -id <disp> -seller <addr> -pay 300000000

# a composite: declare an import, then reach it from the content as "0"
./zcribe inscribe -devnet -file zenlib.js -name zenlib
./zcribe inscribe -devnet -file page.html -refs <zenlib-id>

# withhold something, and purge it from the CDN in front of the origin
./zcribe moderate -asset <id> -hide -reason "…" -purge
./zcribe moderate -import blocklist.txt -purge

# prove you hold an address: paste the bot's challenge on stdin, send back
# the one line this prints
./zcribe sign -keystore wallet.json
./zcribe sign -verify <pubkey>.<signature>   # check one, no key needed
```

A key never comes from a command-line argument — arguments land in shell
history and in the process table. Use `-devnet`, `-mnemonic-file`,
`-mnemonic-env`, or `-keystore` with `ZCRIBE_PASSWORD`.

## Upgrade lanes

A protocol that can only change by version bump churns. Zenoglyphs has
three lanes, in decreasing order of disruption (spec R-2.9):

| Change | Mechanism | Old indexers |
|---|---|---|
| The ruleset itself | bump `v` | refuse the whole ZDU |
| A field that changes meaning | a new top-level key | refuse the whole ZDU |
| A purely additional field | a key inside `ext` | **ignore it, and stay correct** |

`ext` is an optional map on the envelope and on every payload. Its contents
are **consensus-inert**: carried on chain, committed to by the block hash,
served to clients, never read by the state machine. That is stricter than
Runes' odd tags, and deliberately so — if an `ext` key could move a balance,
two indexers at different versions would agree on the state root while holding
different state, which is precisely the blindness the root exists to remove.
The result is a strong guarantee: an old indexer and a new one compute
identical state forever; the newer one just shows its clients more.

## Composites and recursion

Two mechanisms, commonly conflated, and both implemented (spec R-4.7, R-8.5).

**Recursion** is an inscribed page loading another inscription's bytes at
render time — the shared-`p5.js` pattern. It needs no wire format: content
already has a URL and every inscription shares one origin.

**Composites** are the declaration. A mint may carry `refs: [assetId, …]`, up
to 16, in slot order — the "list of sequential imports, referenced like
variables" from the original design notes. The content origin serves slot *n*
at `/content/{id}/{n}`, and an asset's canonical URL carries a trailing slash
so it is its own base. So the inscribed bytes say:

```html
<script src="0"></script>     <!-- refs[0] -->
<img src="1">                 <!-- refs[1] -->
```

and never embed a 64-character hash the author could not have known.

Three properties are worth stating because they are what keep composition out
of consensus:

- **A ref is a claim, not a dependency.** The indexer records the edge and
  resolves nothing. It never fetches the target and never requires it to exist,
  because otherwise an indexer that had synced further would accept a mint one
  that had not would reject.
- **Cycles are impossible by construction.** A ref names a block hash, and a
  block hash cannot be known before the block exists — so every edge points
  strictly backwards and the graph is a DAG. No cycle detection is needed, only
  a depth cap on client-side resolution.
- **An inscription can load other inscriptions and nothing else.** Every CSP
  fetch directive is pinned to the content origin, so there is no CDN, no
  analytics, and no author-controlled URL that can be repointed after the mint.

## Two origins

`serve` starts **two** HTTP servers, and the split is the security model:

| Port | Serves |
|---|---|
| `8710` | metadata API + the viewer + the admin panel, opt-in (see below) |
| `8711` | inscription bytes, `/r/…` recursion endpoints, nothing else |

The MIME allowlist includes `text/html` and `image/svg+xml`, both of which
execute script. Content is served from the second origin with
`X-Content-Type-Options: nosniff` and a `Content-Security-Policy` that grants
`allow-scripts` — so recursion works — while **never** granting
`allow-same-origin`, so every inscription renders in its own opaque origin and
cannot reach another's frame. `zcribe serve -scripts=false` turns execution off
entirely; `/info` publishes which it is, and the viewer matches it.

In production these must be different hostnames, not just different ports —
and preferably different registrable domains, because a cookie set with
`Domain=.example.org` reaches every subdomain including the one running
attacker-authored HTML. See [`deploy/`](../deploy/).

## Admin panel

Everything `zcribe moderate` does — setting a policy, bulk import/export,
purging a CDN — is also reachable from a browser, at `/admin` on the metadata
origin. It is unauthenticated *by default in the sense that it does not
exist*: set `ZCRIBE_ADMIN_PASSWORD` (never a flag — see "A key never comes
from a command-line argument" above) and `serve` logs `admin panel enabled`;
leave it unset and every `/admin/api/` route answers 503. The panel adds a
read-only status dashboard (sync height, checkpoints, whether a CDN purge is
configured) alongside moderation, but stops short of reindexing or dropping
checkpoints from the browser — those touch a running indexer in ways that
need an operator's full attention, so they stay on the CLI (`zcribe reindex`,
`zcribe snapshot`).

The path is not the security boundary, the password is: `/admin` is never
linked from the viewer and carries `<meta name="robots" content="noindex">`,
but it is still an ordinary reachable URL on a public port, the way
`/wp-admin` is on a WordPress site. What actually guards it is the password
(rate-limited, constant-time compared), a signed session cookie that is
host-only, `Path=/admin`, `HttpOnly`, `SameSite=Strict` — and, because logout
had to mean something, revoked server-side by a counter rather than only
cleared client-side. See [`internal/api/admin.go`](../internal/api/admin.go) for
the full model and why the cookie does not weaken the two-origins split above.

## Telegram front end

`zcribe bot` is an API client and nothing more: it reads the same public HTTP
API a third-party would, from outside the process, with no database access. A
withheld inscription is refused because the API refuses it, not because the bot
knows the rules.

```sh
# read-only, against a local indexer
ZCRIBE_TELEGRAM_TOKEN=… ./zcribe bot -api http://127.0.0.1:8710

# plus minting, signed by this operator's key
ZCRIBE_TELEGRAM_TOKEN=… ./zcribe bot -devnet

# plus on-chain identity, and charging for mints
ZCRIBE_TELEGRAM_TOKEN=… ./zcribe bot -devnet \
    -db zcribe-bot.db -credit-zts zts1… -mint-cost 1000
```

**Identity** (`-db`). `/link z1…` issues a challenge; the holder signs its exact
text with that address's key and answers `/verify <pubkey>.<signature>`. The bot
checks the signature *and* that the key hashes to the claimed address — only the
second half ties a proof to an address rather than to a key. The signed bytes
are the raw message, the way syrius signs WalletConnect's `znn_sign`, and they
contain the address, the Telegram account id and an expiry, so a proof cannot be
replayed by another account. `zcribe sign` produces one from the CLI. Once
linked, `/create` mints into that address instead of the bot's own.

**Credit** (`-credit-zts`, prod only). Minting spends the operator's key and
plasma, so a production instance can charge for it: users send a token to the
bot's address and each mint costs `-mint-cost` whole units of it, tracked in an
off-chain ledger keyed by Telegram account. This is not a protocol fee — nothing
in the protocol checks a balance, and anyone with their own key mints for free
(A-2, A-39).

A deposit is credited only once its sender's address has been linked, and a
deposit that cannot be attributed is **left unreceived** rather than kept: on
NoM value only lands when the recipient publishes a receive block, so declining
to take delivery is a real choice, and the sender can link the address later and
still be credited. The debit for a mint happens after the block publishes — a
mint that fails costs nothing.

The bot's database is separate from the indexer's on purpose. Everything the
indexer holds is derived from the chain and rebuildable; a signed identity and a
credit balance are neither, and `zcribe reindex` must never be able to touch
them.

## API

```
GET /info                     protocol + indexer version, sync height, state root, allowlist
GET /assets?owner=&issuer=&collection=&mime=&limit=&offset=
GET /asset/{id|number}
GET /asset/{id}/holders       one row per holder; escrow is an address, not a hash
GET /asset/{id}/transfers     the full feed: mint, transfers, escrow legs, sales
GET /asset/{id}/refs          composite imports, and what imports this
GET /address/{addr}           every asset an address holds, and how much
GET /collections · /collection/{id}
GET /dispensers  · /dispenser/{id}
GET /momentum/{h}             {height, hash, txs[], stateRoot}
GET /stateroot/{h}
GET /state                    projection digest — what the state root cannot see

# content origin
GET /content/{id}/            raw bytes, sandboxed, with the inscribed mime type
GET /content/{id}             301 to the above
GET /content/{id}/{n}         301 to composite import slot n
GET /r/momentum               the chain tip this indexer has reached
GET /r/asset/{id}             one asset's metadata
GET /r/refs/{id}              the import graph, both directions
GET /r/children/{id}          the ids in a collection
```

The `/r/` endpoints live on the content origin because a relative reference is
the only kind an inscription can carry without hard-coding where an indexer is
deployed.

Assets report **two** confirmation levels (spec R-6.3): `status: "soft"` means
this indexer has parsed and applied the block — the inscription exists and
renders now — and `status: "full"` means the configured depth of momentums sits
above it and the inscription number is settled.

`/transfers` is the whole history of an asset's units, not just the rows the
indexer wrote. Three movements never produce a transfer row — the mint, stock
going into a dispenser's escrow, and stock coming back out on a cancel — so
they are reconstructed when the feed is served and flagged `synthetic`. Every
row carries a `kind`:

```json
{"kind": "escrow", "from": "z1qq6e…", "to": "z1zen0glyphxescr0wat0emq0rrazm9r0ny47hpr",
 "amount": "6", "block": "d3e5e6…", "momentum": 17963,
 "toDispenser": "d3e5e6…", "synthetic": true}
```

`mint · transfer · burn · escrow · release · sale`. A dispenser's escrow is
displayed as an address in the protocol's own namespace (spec R-8.6) rather
than as the raw block hash it is keyed by, with the dispenser id alongside —
see A-35 and A-36 for why the key itself was left alone.

Both servers write an access log: one line per response, with method, path,
status, bytes and latency. The content origin logs successes at debug level
(`-v` shows them) because it answers every recursion fetch a page makes;
failures are always logged. Request headers and bodies never are — the admin
session cookie is a credential and the login body is a password.

## Cross-indexer agreement

```
stateRoot(h) = sha3_256( stateRoot(h-1) ‖ uint64_be(h) ‖ orderedAppliedTxHashes(h) )
```

The root commits to the blocks that actually **changed state**, not to every
block that parsed — that distinction is what lets it catch a rules
disagreement rather than only an encoding one (see A-18).

Run a second indexer on its own database and ports, then compare:

```sh
./zcribe serve -db second.db -api 127.0.0.1:8720 -content 127.0.0.1:8721
./zcribe compare -a http://localhost:8710 -b http://localhost:8720
```

If they disagree, `compare` binary-searches the chained roots — agreement is
monotone in height — and prints the first divergent momentum plus the blocks
each side accepted:

```
first divergence: momentum 12599  (15 queries)
  momentum hash   A 68d947c4…  B 68d947c4…  (same)
  A accepted, B rejected:
    dc6ea61d7e0fed0c0d1e900441e5bda1c433e5546d2f916c08964fb1baca03ec
```

Moderation is deliberately excluded from the state root: it is a per-indexer
serving policy, so two honest operators with different policies still agree on
history.

`GET /state` adds a digest of the whole projected state. The state root proves
two indexers agree about *which events applied*; it structurally cannot catch
two indexers that agree on that and still compute different balances. The
digest can.

## Reorgs

A reorg truncates the event log to the last agreeing height and replays. By
default the replay starts from a **checkpoint** rather than from genesis:

```sh
./zcribe snapshot -list          # what checkpoints exist, and how deep
./zcribe snapshot -drop          # delete them; costs replay time, nothing else
```

Checkpoints are taken every 120 momentums and retained to cover a
360-momentum reorg — one hour at NoM's ~10 s interval. This is not an inverse
of the state machine, which would drift: it moves the *base case* of the same
replay forward, so `Apply` is untouched.
`TestSnapshotRestoreMatchesFullRebuild` asserts the two paths produce a
byte-identical projection digest.

Note that the checkpoint window (360) is deeper than the confirmation depth
(60) on purpose. Being able to unwind further than you promise is the safe
direction; `serve` warns if the inequality is inverted.

A chain that goes *backwards* — a devnet wiped and restarted under a running
indexer — is the case that looks like silence rather than like a fork: the
node's tip drops below the indexer's, so no momentum is fetched and the parent
hash is never compared. It is detected by checking the node's own tip against
what this indexer stored there, and handled as a reorg however deep it turns
out to be. The fork search is a binary search for that reason: a total reset
disagrees at every height, and walking back one momentum at a time would cost
one round trip per momentum ever indexed.

Pointing an existing database at a **different network** is refused outright.
The chain identifier is recorded on first sight and compared on every poll;
mismatching it stops the indexer rather than interleaving two chains in one
event log, which no rollback can separate afterwards.

## What was measured on devnet

| | |
|---|---|
| Full sync, genesis → tip | 12,569 momentums in ~12 s (batched at 100/call) |
| Largest inscription | 219,959-byte PNG across 14 blocks, reassembled byte-exact |
| Two independent indexers | identical `stateRoot` and identical projections |
| Deliberately mismatched builds | divergence localised to one momentum in 15 queries |
| Rebuild from event log | reproduces state exactly; twice is a no-op |
| CBOR byte strings vs base64 | 48 KB image: 4 blocks instead of 5 |
| Composite render, real browser | `<script src="0">` resolved slot 0 to the imported library, which read `/r/momentum` and drew a canvas — only on-chain bytes involved |
| Burn to `z1zen0glyphxdurn…` | settles; the units leave circulation |
| Telegram front end | metadata, provenance, market and file rendering against the live API |

## Status

Phases 0–4 and 6 of the plan are substantially done: the wire format is frozen
with golden vectors, the indexer validates all seven opcodes with checkpointed
reorg recovery, the API and sandboxed content origin are up, the viewer renders
every content class, composites and recursion work end to end, and there is a
Telegram front end and a mainnet deployment guide.

What is not done, and one thing that is open for a decision, are both in
[`ASSUMPTIONS.md`](../ASSUMPTIONS.md): the "Not implemented" table, and **A-15**,
which asks whether dispensers should keep settling in a real ZTS.
