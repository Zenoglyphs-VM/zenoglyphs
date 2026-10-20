# zcribe / Zenoglyphs — adversarial review

**Date:** 2026-09-04 · **Reviewed tree:** `C:\dev\zenon\zenoglyphs` at the state of
`TODO.txt` "last updated 2026-09-04" · **Reviewer posture:** adversarial — the question
asked of every line was *"what does a hostile party get out of this"*, not *"does this
look right"*.

Scope: the v1 wire protocol (`spec/SPEC.md`, `internal/protocol`), the indexer and its
store (`internal/indexer`, `internal/store`), the HTTP API and admin panel
(`internal/api`), the inscriber (`internal/inscribe`), the Telegram front end
(`cmd/zcribe/bot.go`, `market.go`, `botcredit.go`, `internal/botstore`, `internal/identity`),
the Vue viewer and admin UI (`web/ui/src`), and the deployment material (`deploy/`).

Findings marked **[proven]** were reproduced with a throwaway test in this tree; the test
body is quoted with the finding so it can be re-run. Everything else is a code reading
with the file and line cited. Temporary probe files were removed after use — the tree is
unchanged apart from this document.

---

## Contents

- [Severity summary](#severity-summary)
- [C — Critical](#c--critical)
- [H — High](#h--high)
- [M — Medium](#m--medium)
- [L — Low and informational](#l--low-and-informational)
- [Discrepancies between documentation and code](#discrepancies-between-documentation-and-code)
- [Checked and found sound](#checked-and-found-sound)
- [Suggested remediation order](#suggested-remediation-order)

---

## Severity summary

| ID | Severity | Area | One line |
|---|---|---|---|
| [C-1](#c-1--live-secrets-in-the-working-tree-and-not-gitignored) | Critical | deploy | Live admin password (5 chars) and a live-shaped Telegram token sit in `deploy/*.env`; `.gitignore` covers neither |
| [H-1](#h-1--a-partially-processed-momentum-is-re-applied-on-the-next-pass-supply-inflation) | High | indexer | A momentum is not applied atomically; a retry re-applies its earlier blocks and inflates supply **[proven]** |
| [H-2](#h-2--postadminapilogout-is-unauthenticated) | High | admin API | Unauthenticated `POST /admin/api/logout`: nil-deref panic when the panel is off, global session revocation when it is on **[proven]** |
| [H-3](#h-3--admin-login-throttling-is-bypassable-and-the-password-hash-is-unsalted-sha-256) | High | admin API | `X-Forwarded-For` is attacker-controlled, so login throttling is unlimited-attempt; password digest is bare SHA-256 |
| [H-4](#h-4--a-whitespace-only-telegram-message-crashes-the-bot) | High | bot | `fields[0]` on an empty slice, before the `recover()` is registered — kills the process **[proven]** |
| [H-5](#h-5--fifty-dust-deposits-permanently-stall-the-credit-watcher) | High | bot | `UnreceivedBlocks(…, 0, 50)` plus "never receive what you cannot attribute" is a permanent head-of-line block |
| [H-6](#h-6--every-metadata-endpoint-reads-the-full-content-blob-and-discards-it) | High | API | `/asset`, `/holders`, `/refs`, `/transfers`, `/address` read whole inscriptions to print a byte count; `/address` does it per holding, unbounded |
| [M-1](#m-1--r-713-does-not-bound-storage-amplification-chain-prefixes-are-reusable) | Medium | protocol | One head per *tail* is enforced; one head per *chain prefix* is not — ~30× storage amplification survives **[proven]** |
| [M-2](#m-2--balances-above-2⁶³1-sort-wrongly-cast-amount-as-integer) | Medium | store | `ORDER BY CAST(amount AS INTEGER)` reintroduces the exact overflow the TEXT column exists to avoid **[proven]** |
| [M-3](#m-3--the-indexer-trusts-the-node-completely) | Medium | indexer | Order is re-derived; content membership and block hashes are not verified at all |
| [M-4](#m-4--moderation-fails-open-on-a-store-error) | Medium | API | `if err == nil && !mod.…` serves withheld bytes when the moderation read fails |
| [M-5](#m-5--a-crash-inside-handlereorg-strands-the-abandoned-branchs-projections) | Medium | indexer | `TruncateAbove` commits before `RebuildFrom` runs; nothing re-derives on restart |
| [M-6](#m-6--state-is-an-unauthenticated-full-database-hash) | Medium | API | `/state` scans and hashes every projection row, for anyone, unthrottled |
| [M-7](#m-7--addressz1zen0glyphxescr0w-forces-a-full-dispenser-scan-per-request) | Medium | API | Prefix-triggered O(dispensers) sha3 + bech32 loop |
| [M-8](#m-8--a-short-escrow-wedges-the-indexer-instead-of-invalidating-the-event) | Medium | indexer | `applyCancelDispenser` returns `ErrInsufficient` as a storage fault → infinite retry loop |
| [M-9](#m-9--assetsowner-is-a-mint-time-fact-served-under-an-ownership-name) | Medium | API/UI | `?owner=` filters a column no transfer updates; the gallery labels it "owner address" |
| [M-10](#m-10--protocol-address-prefixes-are-cheap-to-grind-and-are-treated-as-identity) | ~~Medium~~ **resolved** | protocol/UI | Was: `z1qzenx…` a ~2²⁵ grind labelled "protocol-owned". Fixed by the `z1ze…` namespace (A-26) — no keypair can reach it |
| [M-11](#m-11--devnet-uses-a-publicly-known-key-with-no-chain-guard) | Medium | CLI | "chain 69 only" is a comment, not a check |
| [M-12](#m-12--keystore-password-is-accepted-on-the-command-line) | Medium | CLI | `-password` contradicts the file's own rule three lines above it |
| [M-13](#m-13--unbounded-listings) | Medium | API | `/collections`, `/dispensers`, `/asset/{id}/holders`, `/address/{addr}` have no limit |
| [M-14](#m-14--five-reachable-vulnerabilities-in-go-ethereum-11022) | Medium | deps | Transitive via go-zenon; all DoS-class |
| [M-15](#m-15--reindex-has-no-guard-against-a-running-serve) | Medium | CLI | Two writers, one SQLite file, no lock |
| L-1 … L-20 | Low | — | See [below](#l--low-and-informational) |

---

## C — Critical

### C-1 · Live secrets in the working tree, and not gitignored

**Where:** `deploy/admin.env`, `deploy/bot.env`, `.gitignore`

Two files in the repository tree hold live credentials rather than placeholders:

- `deploy/admin.env` sets `ZCRIBE_ADMIN_PASSWORD` to a **5-character** value.
- `deploy/bot.env` sets `ZCRIBE_TELEGRAM_TOKEN` to a 46-character string matching the
  live Telegram token shape `<digits>:<35-char secret>`, and a real-looking channel id.

`.gitignore` is three lines and covers only the built binary and the SQLite files:

```
/zcribe
/zcribe.exe
*.db
*.db-shm
*.db-wal
```

There is no `*.env` rule and no `deploy/*.env` rule. The `.example` files exist beside
them, which is the correct pattern — but the non-example files were filled in rather than
copied to `/etc/zcribe/`.

**Why it matters.** The tree is not currently a git repository (`git rev-parse` fails), so
nothing is committed *yet*. The first `git init && git add .` publishes both secrets, and
history is not something you can take back. Independently of git, these are readable by
anything with filesystem access to the checkout, including any build agent or backup.

The admin password compounds three other findings: it is 5 characters, it is stored as
unsalted SHA-256 ([H-3](#h-3--admin-login-throttling-is-bypassable-and-the-password-hash-is-unsalted-sha-256)),
the login throttle is bypassable ([H-3](#h-3--admin-login-throttling-is-bypassable-and-the-password-hash-is-unsalted-sha-256)),
and `serve` only *warns* about a short password rather than refusing it
(`cmd/zcribe/serve.go:88`). `TODO.txt` already lists "Rotate ZCRIBE_ADMIN_PASSWORD in
deploy/admin.env" as open, which is the right call — but the entry describes it as a
hygiene task, and with the throttle bypass it is closer to an open door.

**Fix.**

1. Revoke both credentials now: `/revoke` in @BotFather for the token; change the admin
   password through the panel's Settings tab (which enforces the 12-character minimum) or
   by setting a new `ZCRIBE_ADMIN_PASSWORD` and restarting.
2. Move both files out of the tree to `/etc/zcribe/`, `chmod 600`.
3. Add to `.gitignore`:
   ```
   *.env
   !*.env.example
   ```
4. Consider making `serve` refuse to start with a password below `adminMinPasswordLen`
   rather than warning — the back-compat argument in `internal/api/admin.go:60` is weaker
   than it looks, because the only deployment it protects is the one with the weak
   password.

---

## H — High

### H-1 · A partially processed momentum is re-applied on the next pass (supply inflation)

**Where:** `internal/indexer/indexer.go:316` (`processMomentum`), `:462` (`processBlock`),
`:500–511`, `:353` (`PutMomentum`); `internal/store/projection.go:223` (`Move`)

`processMomentum` writes each block's event, applies it, records the applied flag, and only
then writes the momentum row — each as a **separate autocommit statement**. There is no
transaction around the momentum.

```go
// processBlock, indexer.go:500-511
if err := ix.st.AppendEvent(ev); err != nil { … }      // ON CONFLICT DO NOTHING
applied, err := ix.Apply(ev)                            // many writes, none idempotent
if err := ix.st.SetEventApplied(…); err != nil { … }
// …then, back in processMomentum, after every block:
ix.st.PutMomentum(…)                                    // indexer.go:353
```

`TipHeight()` reads `MAX(height) FROM momentums`, so if the process dies — or if any block
in the momentum returns a storage error — the next pass starts at the *same* height and
runs every block in it again. `AppendEvent` is idempotent (`ON CONFLICT DO NOTHING`) and so
is `PutAsset`/`SetBalance`, but **`store.Move` is not**: it reads a balance, subtracts, and
writes. Replaying a transfer moves the amount a second time.

**Reachability is not limited to a crash.** `Run` (indexer.go:151–159) catches any error
from `step` — including a storage fault raised half-way through a momentum — logs it,
sleeps three seconds, and retries from `tip+1`, which is that same momentum. A single
`SQLITE_BUSY` past the 10 s busy timeout, a full disk, or an I/O error is enough. The
systemd unit sets `Restart=always`, so a crash retries automatically too.

**[proven]** Probe (`internal/indexer/zz_probe_test.go`, since removed):

```go
mint := b.next(protocol.OpMint, alice, alice, mintPayload(t, "coin", 100, []byte("x")))
feed(t, ix, st, mint)
xfer := b.next(protocol.OpTransfer, alice, bob,
    marshal(t, &protocol.TransferWire{Asset: id, Amount: 50}))
feed(t, ix, st, xfer)
// the momentum row was never written, so the syncer re-runs the same two blocks
feed(t, ix, st, mint)
feed(t, ix, st, xfer)
```

Output:

```
after first pass:  alice=50 bob=50 (supply 100)
after second pass: alice=50 bob=100 total=150 supply=100
SUPPLY INFLATION: circulating 150 > supply 100
```

**Why this is the worst finding in the indexer.** The corruption is invisible to the
mechanism designed to catch it. `computeStateRoot` commits to the *applied set*
(`TxHashesAt` filters `applied = 1`), and the applied flags are rewritten identically on
the retry — so this indexer's state root still **matches** every honest indexer's while its
balances differ. Only `ProjectionDigest` (`/state`) sees it, and nothing polls that
automatically. Spec R-6.4 argues at length that a commitment which cannot see the
disagreement it exists to detect is worse than none; this is a path that produces exactly
that.

It can also cascade: an over-credited escrow makes
[M-8](#m-8--a-short-escrow-wedges-the-indexer-instead-of-invalidating-the-event) reachable,
which wedges the indexer in a retry loop.

**Two comments in the tree assert the opposite and should be corrected with the fix:**

- `internal/indexer/indexer.go:154` — *"the next pass re-reads from the same height and is idempotent."*
- `deploy/systemd/zcribe.service` — *"A crash mid-momentum is safe to restart into: … every projection write is idempotent, so the next pass re-reads from the same height and lands in the same place."*

The existing idempotence test (`apply_test.go:523`) exercises `Rebuild()`, which calls
`ResetProjections()` first — it tests replay-from-empty, not re-processing without a reset,
so it cannot catch this.

**Fix.** Wrap the whole momentum — every event append, every projection write, the applied
flags, the state root and the momentum row — in one transaction, so `TipHeight()` and the
projections advance together. That requires threading a `*sql.Tx` through `Apply`, which is
a real refactor; the store already pins `SetMaxOpenConns(1)`, so there is no concurrency
obstacle. A cheaper interim guard: record an `in_progress_height` in `meta` before the first
block of a momentum and clear it after `PutMomentum`; on startup, if it is set, run
`RebuildFrom(height-1)` before resuming.

### H-2 · `POST /admin/api/logout` is unauthenticated

**Where:** `internal/api/admin.go:233` (route), `:458` (`handleAdminLogout`), `:219`
(`revokeAdminSessions`)

`logout` is registered on the *unprotected* mux, alongside `login` and `session`, and its
handler calls `revokeAdminSessions()` before anything else:

```go
func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	s.revokeAdminSessions()      // s.admin.mu.Lock() — s.admin may be nil
	clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

Two distinct defects fall out:

**(a) Nil dereference when the panel is not configured.** `s.admin` is nil until
`EnableAdmin` is called, and `revokeAdminSessions` locks `s.admin.mu` unconditionally.
Every other handler checks (`requireAdmin` at `:265`, `handleAdminLogin` at `:430`,
`handleAdminSession` at `:485`); this one does not. `net/http` recovers the panic per
connection, so the process survives, but an anonymous request aborts the connection and
writes a stack trace to the log — from a route that exists on **every** deployment,
including the ones that deliberately opted out of the admin panel.

**(b) Anyone can log every admin out, repeatedly.** When the panel *is* configured, an
unauthenticated `POST /admin/api/logout` bumps the session generation, invalidating every
outstanding cookie, and persists the new generation with `SetMeta` — a database write per
request. Looping it makes the panel unusable: an operator can log in but is signed out
before they finish anything. `SameSite=Strict` does not help, because the handler needs no
cookie, so an ordinary cross-site form POST also triggers it.

**[proven]** Probe (`internal/api/zz_probe_test.go`, since removed):

```
=== RUN   TestProbeLogoutWithoutAdmin
    PANIC on unauthenticated logout with admin disabled: runtime error: invalid memory address or nil pointer dereference
=== RUN   TestProbeUnauthenticatedLogoutRevokes
    before: status=200
    anon logout status=200 body={"ok":true}
    after anon logout: status=401 (401 means the anon request killed the session)
```

**Fix.** Move `logout` behind `requireAdmin` (it has no reason to be reachable without a
session), or at minimum: return 503 when `s.admin == nil`, and require a valid session
before revoking — falling back to only clearing the caller's own cookie otherwise. The
"log out everywhere" semantics are right; they just need to be an authenticated action.

### H-3 · Admin login throttling is bypassable, and the password hash is unsalted SHA-256

**Where:** `internal/api/admin.go:372` (`clientIP`), `:386–405` (`allowAttempt`,
`recordFailure`), `:201` (`checkPassword`), `:57` (`adminAttemptCap`);
`deploy/nginx.conf:37`, `deploy/Caddyfile`

Three weaknesses that are individually defensible and jointly serious.

**(a) The rate-limit key is attacker-controlled.**

```go
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i >= 0 {
			return strings.TrimSpace(fwd[:i])   // the FIRST entry
		}
		return strings.TrimSpace(fwd)
	}
	…
}
```

nginx is configured with `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`,
which *appends* the real client to whatever the client sent. Caddy behaves the same way.
The first element is therefore whatever the attacker put there. A different value per
request means a fresh bucket per request and **no rate limit at all** — five attempts each,
forever.

The comment above the function states the opposite:

> *"A spoofed X-Forwarded-For only ever degrades this to a shared bucket, never grants
> access to anyone who does not already have the password."*

Degrading to a shared bucket would be the effect of trusting the *last* entry, or of
ignoring the header. Trusting the first entry escapes the bucket entirely. The comment
should not be relied on when reasoning about this code.

**(b) The attempt table is flushed wholesale under pressure.**

```go
if len(a.attempts) > adminAttemptCap {   // 10000
	a.attempts = make(map[string]*loginAttempts)
}
```

Even if (a) were fixed, spraying 10 001 distinct `X-Forwarded-For` values clears every
recorded lockout — including genuine ones — and the attacker resumes from zero. A bounded
LRU, or per-bucket expiry, would keep the memory bound without discarding live state.

**(c) The stored digest is a bare, unsalted SHA-256** (`admin.go:201`, `:118`, `:544`), kept
in the `meta` table of the same SQLite file the API process reads on every request. Nothing
here is a password KDF: no salt, no work factor. Anyone who obtains the database file
recovers a short password essentially instantly, and a rainbow table covers any common one.

Together with the deployed 5-character password
([C-1](#c-1--live-secrets-in-the-working-tree-and-not-gitignored)) and `serve`'s
warn-don't-refuse startup check, the admin panel — which can hide any inscription, import a
blocklist, and flush the CDN — is in practice guessable online.

**Fix.**

- Take the client IP from `r.RemoteAddr` by default, and only honour `X-Forwarded-For` when
  the peer is in an operator-configured trusted-proxy list; when trusted, take the
  *rightmost untrusted* entry, not the leftmost. If a config knob is unwanted, use
  `RemoteAddr` alone — behind the documented reverse proxies that is the proxy's IP, which
  is a shared bucket, which is the behaviour the current comment already claims.
- Add a global attempt ceiling in addition to the per-key one, so a spray cannot outrun the
  limiter regardless of keying.
- Replace SHA-256 with `argon2id` or `bcrypt` (`golang.org/x/crypto` is already a
  dependency). Migration is one `meta` key: store `algo$params$salt$hash`, treat a bare
  64-hex value as legacy SHA-256, and upgrade it on the next successful login.
- Make `serve` refuse to start below `adminMinPasswordLen`.

### H-4 · A whitespace-only Telegram message crashes the bot

**Where:** `cmd/zcribe/bot.go:623–641`

```go
func (b *bot) handle(ctx context.Context, u tgUpdate) {
	chat := u.Message.Chat.ID
	text := strings.TrimSpace(u.Message.Text)
	if text == "" {
		text = strings.TrimSpace(u.Message.Caption)
	}
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])   // line 632 — panics when fields is empty
	…
	defer func() {                      // line 638 — registered SIX LINES TOO LATE
		if r := recover(); r != nil { … }
	}()
```

`poll` (`:606`) only skips an update when `Text` **and** `Caption` are both the empty
string. A message whose text is `"  "`, a non-breaking space, or an ideographic space
passes that gate; `TrimSpace` then reduces it to `""`, `strings.Fields("")` returns an empty
slice, and `fields[0]` panics.

The `recover()` intended to contain exactly this — its comment says *"One malformed reply
from the API must not take the bot down: it is reachable by anyone who can find the
username"* — is registered **after** the panicking line. `handle` is called synchronously
from `poll`, which is called from `cmdBot`, which is called from `main`: the panic unwinds
all the way out and the process exits.

The most reliable trigger is a photo or document with a whitespace-only caption, which every
Telegram client will send. `strings.Fields` uses `unicode.IsSpace`, so U+00A0 and U+2000–200A
also qualify.

**[proven]** `strings.Fields("   ")` and `strings.Fields("\u00A0")` both return length 0
(verified with Go 1.27 in this environment).

**Fix.** Move the `defer` to the first statement of the function, and guard the parse:

```go
defer func() { if r := recover(); r != nil { … } }()
fields := strings.Fields(text)
if len(fields) == 0 {
	return
}
```

Guarding alone is enough for this instance; moving the `defer` is what makes the next one
survivable. Note the same ordering is correct in `cmdCreate` (`:1081`) and `handleCallback`
(`market.go:101`) — this is the one place it slipped.

### H-5 · Fifty dust deposits permanently stall the credit watcher

**Where:** `cmd/zcribe/botcredit.go:312–375` (`sweepDeposits`), `:316`

```go
blocks, err := b.node.UnreceivedBlocks(ctx, b.in.Address(), 0, 50)
```

The watcher reads **page 0, size 50** of the bot's unreceived blocks and, by design, leaves
anything it cannot attribute **unreceived**:

```go
if id == nil {
	// Not ours to take. Left unreceived on purpose … so it can still be
	// credited if they link later.
	b.log.Info("unattributable deposit left unreceived", …)
	continue
}
```

The design decision is a good one — taking delivery of money you cannot attribute is worse.
But it means unattributable blocks accumulate at the head of the unreceived list forever,
and the watcher never looks past the first 50. Once 50 unattributable deposits exist, every
legitimate deposit lands at position 51 or beyond and is **never seen**: no credit, no
`/balance` change, no notification. Users pay and get nothing.

The attack costs 50 sends of the credit token from 50 unlinked addresses — the smallest
nonzero amount each. It is permanent, it is silent (only a `log.Info` line per block per
20-second sweep, which also becomes a log-volume problem), and there is no operator command
to clear it.

**Fix.** Page through the unreceived list rather than reading only the first page, and keep
a per-block-hash "seen and unattributable" set so each sweep skips known dead weight cheaply.
Two extras worth having: a cap on how many unattributable blocks are retained per sender
address, and an operator command that lists them so the money can be handled deliberately.

### H-6 · Every metadata endpoint reads the full content blob and discards it

**Where:** `internal/api/api.go:387` (`lookupAsset`), `:75–79` (routes), `:729`
(`handleAddress`); `internal/store/projection.go:75` (`assetCols`)

`assetCols` selects `content`. `lookupAsset` uses it. Every one of these routes calls
`lookupAsset` and then renders `assetJSON`, which has **no content field** — the bytes are
read from disk, decoded into a `[]byte`, and thrown away:

```
GET /asset/{id}              GET /asset/{id}/holders
GET /asset/{id}/refs         GET /asset/{id}/transfers
GET /r/asset/{id}            GET /content/{id}/{slot}     (the redirect!)
```

For a 1 MB multi-part inscription that is 1 MB of disk read and allocation per request, from
an unauthenticated endpoint, to produce a few hundred bytes of JSON.

`handleAddress` is worse, because it does it in a loop with no bound:

```go
holdings, err := s.st.HoldingsOf(key)     // no LIMIT
for _, h := range holdings {
	a, err := s.st.Asset(h.AssetID)       // full content blob, per holding
	…
	out = append(out, holding{Asset: s.assetToJSON(*a), …})
}
```

An address holding 1 000 megabyte-scale inscriptions makes `GET /address/{addr}` a ~1 GB
read and allocation. An attacker can build that address themselves and then hit their own
page. `SetMaxOpenConns(1)` means the read also blocks the indexer's writes for its duration.

This is the same defect `TODO.txt` records as fixed under "Is SQLite the right database":

> *"ListAssets selected `content` while the API used only len(content), so every gallery
> page read up to twenty megabytes of bytes it discarded; it now selects length(content)."*

The fix landed in `ListAssets` (via `listCols`, `projection.go:100`) and nowhere else.

**Fix.** Split the store read: a `store.AssetMeta(id)` using `listCols` (`length(content)`)
for every path that renders `assetJSON`, and keep the blob-loading `Asset(id)` for
`handleContent` alone. Add a `LIMIT`/`OFFSET` to `HoldingsOf` while you are there
(see [M-13](#m-13--unbounded-listings)).

---

## M — Medium

### M-1 · R-7.1.3 does not bound storage amplification: chain prefixes are reusable

**Where:** `internal/indexer/apply.go:188–248` (`assemble`), `internal/store/projection.go:404`
(`ClaimTail`); spec R-7.1.3; `ASSUMPTIONS.md` A-11

R-7.1.3 claims one head per chain, and A-11 states the reason explicitly:

> *"Each head is a separate asset with its own content, so N heads over one 1 MB chain cost
> every indexer N megabytes for the price of N single blocks. The ratio between what the
> attacker pays and what every indexer on the network stores is unbounded."*

What is implemented is one head per **tail**: `ClaimTail(head.THash, …)` succeeds once per
`tHash`. But the walk terminates at *whatever block `tHash` names*, and an intermediary is
never claimed. So the same issuer can mint a second head over the same chunks by declaring a
**shorter** walk that stops at an intermediary — a block no previous head claimed.

**[proven]** Probe (`internal/indexer/zz_probe2_test.go`, since removed): publish
`tail("CCC") → p2("BBB") → p1("AAA")`, then three heads all with `pHash = p1`:

```
h1 (full, tHash=tail)   ACCEPTED  content="HAAABBBCCC" parts=4
h2 (prefix, tHash=p2)   ACCEPTED  content="HAAABBB"    parts=3
h3 (prefix, tHash=p1)   ACCEPTED  content="HAAA"       parts=2
```

Three distinct assets, three copies of overlapping content, one chain paid for.

**Cost model at the protocol's own limits.** Publish a 64-part chain of 16 KB chunks
(~1.0 MB of chain data), then 64 heads — each head may carry an *empty* `data` field, since
`applyMint` only rejects empty content for a standalone mint (`apply.go:139`, and confirmed
in the probe: *"multi-part head with empty data: ACCEPTED"*), so each head is a few hundred
bytes. Stored content is `16 KB × (1 + 2 + … + 64) = 16 KB × 2080 ≈ 33 MB` for roughly
1.1 MB of chain. **~30× amplification**, repeatable indefinitely, and every asset also
consumes an inscription number and a row in `assets`.

The rule reduces the ratio from unbounded to `O(MaxAssetParts)`, which is a real
improvement — but A-11's stated guarantee ("the thing the rule appears to forbid is
available anyway, for 32 bytes") does not hold, and an operator sizing storage from that
paragraph will be wrong by a factor of thirty.

**Fix (all are consensus changes — do not ship one silently).**
- Tighten: claim **every** part on the walk, not only the tail. `consumed_tails` becomes
  `consumed_parts`; the first head in canonical order wins each part. This makes the
  document match the code and caps amplification at 1×.
- Or: content-address the blobs (already on `TODO.txt`) so N heads over one chain cost one
  copy, and then A-11's loosening argument becomes true rather than aspirational.
- Either way, correct the A-11 and R-7.1.3 text to say "tail" where it currently implies
  "chain".

### M-2 · Balances above 2⁶³−1 sort wrongly (`CAST(amount AS INTEGER)`)

**Where:** `internal/store/projection.go:287` (`HoldersOf`),
`internal/store/provenance.go:84` (`TransferredFrom`), `projection.go:387`
(`ListDispensers`)

The schema comment is explicit about why amounts are TEXT
(`internal/store/store.go:16`):

> *"amounts are TEXT, not INTEGER. Supply runs to 2^64-1 and SQLite's INTEGER is signed
> 64-bit, so a large supply would silently overflow."*

Three queries cast them straight back:

```sql
ORDER BY CAST(amount AS INTEGER) DESC, address ASC          -- HoldersOf
SELECT SUM(CAST(amount AS INTEGER)) FROM transfers …        -- TransferredFrom
WHERE cancelled = 0 AND CAST(remaining AS INTEGER) > 0      -- ListDispensers
```

SQLite clamps an out-of-range decimal string to `9223372036854775807`, so every balance
above 2⁶³−1 sorts as if it were exactly that, and ties break on `address ASC`.

**[proven]** Probe (`internal/store/zz_probe_test.go`, since removed): two holders, one at
2⁶⁴−1 and one at 2⁶³−1, named so the alphabetical tie-break runs the wrong way:

```
0: zAAA_half    9223372036854775807
1: zZZZ_huge   18446744073709551615
ORDERING WRONG: largest holder (18446744073709551615) is not first
```

`ListDispensers` is unaffected in practice (a clamped huge value is still `> 0`).
`TransferredFrom` is a provenance read only, but it both clamps *and* can overflow the
`SUM`, and its result feeds `escrowLegs`' `sold > escrowed` guard — a wrong sum there
suppresses a legitimate release row and logs a spurious "dispenser sold more than it
escrowed" warning.

**Fix.** Order by `(length(amount) DESC, amount DESC)` — for canonical unpadded decimal
strings that is exact numeric order — or store a zero-padded fixed-width form alongside for
sorting. For `TransferredFrom`, sum in Go over `big.Int` instead of in SQL.

### M-3 · The indexer trusts the node completely

**Where:** `internal/indexer/indexer.go:410` (`canonicalOrder`), `:462` (`processBlock`);
`internal/rpcclient/client.go:105`

`canonicalOrder` does exactly what R-5.2 asks and re-derives the sort of `momentum.content`,
using its own answer when the node's differs. That is good, and it is the only verification
performed. In particular the indexer never checks that:

- `momentum.content` is the content the momentum hash commits to. A node that **omits**
  entries from `content` censors those inscriptions silently — the recomputed sort of a
  truncated list is a perfectly valid sort of a truncated list.
- an `AccountBlock`'s `Data` hashes to the `Hash` it was served under. A node that alters
  `data`, `address`, `toAddress`, `amount` or `tokenStandard` changes what every opcode
  handler decides while the asset id (the block hash) stays the same.
- the momentum's own `Hash` is consistent with its fields.

The threat model matters: the node is normally the operator's own process on `localhost`,
so this is not a remote attack. But the entire cross-indexer agreement design assumes that
two indexers pointed at different nodes derive the same history from the chain. As written,
they derive the same history from *whatever their nodes told them*, and `zcribe compare`
would report the difference as a protocol divergence rather than as a bad node.

There is also a direct contradiction to resolve: `rpcclient/client.go:105` says

> *"Its position in that list is the second half of the canonical order, so the slice order
> the node returns is load-bearing and must never be re-sorted client side."*

while `canonicalOrder` re-sorts and prefers its own order. The indexer's reasoning is the
correct one; the rpcclient comment predates it.

**Fix.** Verify `MomentumContent.Hash()` against `momentum.Hash` for each fetched momentum,
and `AccountBlock.ComputeHash()` against `b.Hash` for each block, before anything is
appended to the log. Both primitives are already imported from `go-zenon` by
`internal/inscribe`. A mismatch is `ErrChainMismatch`-class: stop, do not keep polling.

### M-4 · Moderation fails open on a store error

**Where:** `internal/api/api.go:325`, `:405`, `:542`, `:623`, `:697`, `:761`

Every moderation check is written as:

```go
if mod, err := s.st.Moderation(a.ID); err == nil && !mod.MetadataPermitted { … withhold }
```

and, for content bytes (`:542`):

```go
mod, err := s.st.Moderation(a.ID)
if err == nil && (!mod.DataPermitted || !mod.MetadataPermitted) { … 410 }
```

If the moderation read fails — a locked database, an I/O error, the connection contended by
the indexer under `SetMaxOpenConns(1)` — the condition short-circuits and the asset is
**served**. For a policy whose stated worst case is illegal material, "when in doubt, serve
it" is the wrong default; a 500 is the right one.

**Fix.** Treat a moderation error as withheld and answer 503:

```go
mod, err := s.st.Moderation(a.ID)
if err != nil {
	writeErr(w, http.StatusServiceUnavailable, "moderation policy unavailable")
	return
}
if !mod.DataPermitted || !mod.MetadataPermitted { … }
```

`store.Moderation` already returns permissive defaults for `sql.ErrNoRows`, so the error
branch only fires on a genuine fault.

### M-5 · A crash inside `handleReorg` strands the abandoned branch's projections

**Where:** `internal/indexer/indexer.go:635–660`

```go
if err := ix.st.TruncateAbove(agreed); err != nil { return err }   // commits
if err := ix.st.DropSnapshotsAbove(agreed); err != nil { return err }
if err := ix.RebuildFrom(agreed); err != nil { return err }        // separate
```

`TruncateAbove` commits its own transaction. If the process dies between it and
`RebuildFrom`, the event log is truncated to the fork point but the projections still hold
every balance, asset, dispenser and tail claim from the branch that no longer exists.
Nothing on the startup path notices: `Run` reads `TipHeight()` and resumes indexing forward
from stale state, and nothing ever re-derives.

Same shape as [H-1](#h-1--a-partially-processed-momentum-is-re-applied-on-the-next-pass-supply-inflation),
different window, and a `Restart=always` unit makes it a live path.

**Fix.** Record a `rollback_in_progress` marker in `meta` before `TruncateAbove` and clear
it after `RebuildFrom` succeeds; on startup, if it is set, run `Rebuild()` before serving or
indexing anything. The same marker mechanism covers H-1.

### M-6 · `/state` is an unauthenticated full-database hash

**Where:** `internal/api/api.go:85`, `:909`; `internal/store/snapshot.go:245`
(`ProjectionDigest`)

`GET /state` runs eight full-table scans — assets, balances, collections, dispensers, refs,
tails, parts, transfers — ordered, and folds every column of every row into a sha3-256. No
authentication, no rate limit, no cache. On a large index this is seconds of CPU and a
sustained read across the whole database, on the single connection the indexer also writes
through.

It is genuinely useful (it is the only check that catches a projection bug two indexers'
state roots agree over — see [H-1](#h-1--a-partially-processed-momentum-is-re-applied-on-the-next-pass-supply-inflation)),
so removing it is wrong. But as an anonymous endpoint it is a free amplification lever.

**Fix.** Cache the digest per `syncHeight` (it cannot change without the height changing,
outside a reindex) and serve the cached value; recompute at most once per momentum. If a
fresh recomputation must stay available, put it behind the admin session or a separate
opt-in flag.

### M-7 · `/address/z1zen0glyphxescr0w…` forces a full dispenser scan per request

**Where:** `internal/api/api.go:729–795` (`handleAddress`, `dispenserForEscrow`)

```go
if protocol.IsEscrowAddress(addr) {
	if id, err := s.dispenserForEscrow(addr); err == nil && id != "" { … }
}
```

`IsEscrowAddress` is a length check plus a 13-character prefix compare — no checksum, no
validation. Any string of the right length starting `z1zen0glyphxescr0w` gets in.
`dispenserForEscrow` then lists **every dispenser ever created** and computes
`EscrowAddress(d.ID)` — a sha3-256, a 32-character bech32 fold, and an address decode — for
each, comparing until it matches or the list ends. A non-matching input always walks the
whole list.

The function's own comment acknowledges the cost ("paid here, once, on a lookup nobody makes
by accident") — but nothing stops someone making it on purpose, in a loop, with 39 arbitrary
trailing characters each time.

**Fix.** Materialise the mapping: either a small `escrow_address → dispenser_id` index
maintained beside `dispensers`, or an in-process map built once and updated as dispensers
are created. Both turn the lookup into O(1) and remove the lever.

### M-8 · A short escrow wedges the indexer instead of invalidating the event

**Where:** `internal/indexer/apply.go:369–392` (`applyCancelDispenser`)

```go
if d.Remaining > 0 {
	if err := ix.st.Move(d.AssetID, escrowHolder(id), d.Issuer, d.Remaining); err != nil {
		return err          // ErrInsufficient reaches Apply as a storage fault
	}
}
```

Every other balance move in `apply.go` translates `store.ErrInsufficient` into
`invalid(...)`: `applyTransfer` (`:281`, `:290`), `applyCreateDispenser` (`:355`),
`applyPurchase` (`:449`). This one does not. `Apply` therefore returns `(false, err)`,
`processBlock` wraps it, `step` returns it, and `Run` retries the same momentum every three
seconds forever — the indexer stops making progress and never says why in terms an operator
can act on.

It should be unreachable, because escrow accounting is supposed to be exact. But
[H-1](#h-1--a-partially-processed-momentum-is-re-applied-on-the-next-pass-supply-inflation)
is a live path to an inconsistent escrow balance, and this is what happens next.

**Fix.** Match the other handlers:

```go
if err := ix.st.Move(…); err != nil {
	if errors.Is(err, store.ErrInsufficient) {
		return invalid("dispenser escrow is short")
	}
	return err
}
```

### M-9 · `assets.owner` is a mint-time fact served under an ownership name

**Where:** `internal/store/store.go:84` (column), `internal/store/projection.go:52`
(only write), `internal/api/api.go:143` (`?owner=` filter), `:229` (`"owner"` JSON key);
`web/ui/src/pages/GalleryPage.vue:111`, `:139`

`assets.owner` is written once by `applyMint` and never updated — a grep for `UPDATE assets`
returns nothing. It is served as `"owner"` in every asset response and is the column
`GET /assets?owner=` filters on.

The tree already knows this. `AssetPage.vue:154`, `bot.go:1445` and `market.go:26` all label
it "issued to" and explain why. But two surfaces still present it as ownership:

- `GET /assets?owner=<addr>` — the API's own parameter name.
- The gallery search box: placeholder `"Inscription id, number, or owner address"`, and a
  bare address pushes `?owner=` (`GalleryPage.vue:111`).

So a user who buys an inscription and searches their own address in the viewer's search box
gets nothing, and the box told them to search for an "owner address".

**Fix.** Rename the query parameter to `issuedTo` (keeping `owner` as a deprecated alias),
rename the JSON field to `issuedTo`, and change the placeholder to "Inscription id, number,
or address". `AddressPage` already answers "what does this hold now" correctly from
`balances` — point the search box there for an address instead of at the gallery filter.

### M-10 · Protocol-address prefixes are cheap to grind and are treated as identity

**Where:** `internal/protocol/address.go:79` (`IsEscrowAddress`),
`web/ui/src/core/explorer.ts:57` (`isEscrow`), `:62` (`isProtocolAddress`);
`cmd/zcribe/market.go:305` (`party`)

Both the viewer and the bot decide what an address *is* from a string prefix:

```ts
const NAMESPACE = 'z1zen0glyphx'
export function isProtocolAddress(v) { return !!v && v.startsWith(NAMESPACE) }
```

When this was written the namespace was `z1qzenx`, which sat inside user address
space: an address core is `sha3_256(pubkey)[:19]` behind a `0x00` type byte, so the first two
bech32 characters were near-fixed and only the remaining three of `qzenx` had to be ground —
a few million hashes, seconds on a laptop. Any ordinary wallet could therefore be made to
render in the viewer as protocol-owned. `IsEscrowAddress` needed `qzenxescr0w`, roughly 2⁴⁷ —
out of reach casually, within reach of a determined party — and there was no checksum check
either, so the *string* only had to look right in contexts that never parse it.

**RESOLVED by A-26.** The namespace moved to `z1zen0glyphx`, whose type byte is `0x16`.
Both type bytes NoM can mint (`0x00` user, `0x01` contract) leave the top five bits zero, so
every address the ledger can produce begins `z1q`; `types.PubKeyToAddress` hardcodes byte 0 to
`0x00`. No keypair can land in `z1ze…` at any grind cost, so the prefix stopped being a
guessable label and became a structural one. `TestNamespaceIsUnreachableByAnyKeypair`
enumerates every address both type bytes can reach and fails if any collides.

The prefix match in `explorer.ts` and `market.go` is therefore now sound for the question it
asks ("is this the protocol's own address?"), because a forgery cannot be constructed rather
than merely being expensive.

*Still worth doing, and unaffected by the above:* `provenance.go`'s `escrowParties` builds an
authoritative escrow map from the `dispensers` table, and identifying **which** dispenser an
escrow belongs to still cannot come from the string — the id does not fit in the address
(see `EscrowAddress`). Prefix matching answers "is this escrow", never "whose".

### M-11 · `-devnet` uses a publicly known key with no chain guard

**Where:** `cmd/zcribe/inscribe.go:21–25`, `:56`; `cmd/zcribe/bot.go:132`;
`cmd/zcribe/main.go:100`

```go
// It is public by design and worthless outside chain 69 … and it must never be
// the default for any other network.
const devnetMnemonic = "abstract affair idle …"
```

The flag help says "chain 69 only" and the constant's comment says it must never be used
elsewhere — but nothing enforces it. `walletFlags.inscriber` calls `chainID()` only to sign
with the value it gets back; it never compares it. `zcribe inscribe -devnet -node
<mainnet>` signs and publishes from a key whose mnemonic is committed to a public
repository, and anything ever sent to that address on mainnet is anyone's to take.

The bot inherits the same flag, so `zcribe bot -devnet` pointed at mainnet would mint and
hold assets under that key.

**Fix.** In `walletFlags.inscriber`, after resolving the chain id: if `w.devnet` and
`id != 69`, refuse with a message naming both values. Two lines.

### M-12 · Keystore password is accepted on the command line

**Where:** `cmd/zcribe/inscribe.go:44`, `:50–52`; `cmd/zcribe/bot.go:137`

```go
// A phrase is never accepted as a command-line argument: arguments land in
// shell history and in the process table, where any local user can read them.
func (w *walletFlags) source() (inscribe.KeySource, error) { …
```

Three lines above that comment:

```go
set.StringVar(&w.password, "password", "", "keystore password (prefer ZCRIBE_PASSWORD)")
```

The rule is applied to mnemonics, to the Telegram token, and to the admin password — every
one of which is env-var-only or file-only — and then broken for the keystore password, which
unlocks exactly the same key material. `ZCRIBE_PASSWORD` silently overrides the flag when
set (`inscribe.go:70`), which is the safer precedence but also means the flag exists purely
as a footgun.

**Fix.** Delete the flag. `ZCRIBE_PASSWORD` already covers the non-interactive case; add a
terminal prompt (`golang.org/x/term.ReadPassword`) for the interactive one. If the flag must
stay for compatibility, print a warning when it is used.

### M-13 · Unbounded listings

**Where:** `internal/api/api.go:796` (`handleCollections`), `:838` (`handleDispensers`),
`:407` (`handleHolders`), `:729` (`handleAddress`); `internal/store/projection.go:264`
(`HoldingsOf`), `:284` (`HoldersOf`), `:340` (`ListCollections`), `:381` (`ListDispensers`)

`ListAssets` caps at 200 and defaults to 50. None of the others take a limit at all:

| endpoint | grows with |
|---|---|
| `GET /collections` | every collection ever created |
| `GET /dispensers?open=false` | every dispenser ever created |
| `GET /asset/{id}/holders` | every holder of a high-supply asset |
| `GET /address/{addr}` | every asset an address holds — **and each one's content blob** ([H-6](#h-6--every-metadata-endpoint-reads-the-full-content-blob-and-discards-it)) |

Each row is cheap to create on chain (a collection is one small PoW block, a holder is one
transfer of a single unit), so an attacker chooses how large these responses get and then
requests them. The viewer also fetches `/collections` on every visit to that page and
`ListDispensers(false)` is walked in full by
[M-7](#m-7--addressz1zen0glyphxescr0w-forces-a-full-dispenser-scan-per-request).

**Fix.** Give all four the `limit`/`offset` treatment `ListAssets` already has, with the
same 200 ceiling, and return `total` so the viewer can page.

### M-14 · Five reachable vulnerabilities in go-ethereum 1.10.22

**Where:** `go.mod` (indirect, via the `replace`d `../go-zenon`)

`govulncheck ./...` reports:

| ID | Summary | Fixed in |
|---|---|---|
| GO-2026-4508 | DoS via malicious p2p message | 1.17.0 |
| GO-2026-4315 | DoS via malicious p2p message affecting a vulnerable node | 1.16.8 |
| GO-2026-4314 | High CPU usage leading to DoS via malicious p2p message | 1.16.8 |
| GO-2024-2819 | Denial of Service | 1.13.15 |
| GO-2023-2046 | Unbounded memory consumption | 1.12.1 |

Plus 3 in imported packages and 36 in required modules that govulncheck does not consider
called.

All five are p2p-stack issues and zcribe runs no Ethereum p2p stack — the traces govulncheck
prints are through `common.Address.Format`, `common.Hash.String` and package `init`
functions, i.e. nominal reachability rather than an exploitable path. Practical risk here is
close to zero. It is listed because the version is pinned by go-zenon and will keep showing
up in any downstream scan, and because "we looked and these are why" is worth having
written down.

`npm audit` on `web/ui` reports 0 vulnerabilities, dev dependencies included.

**Fix.** Nothing to do in this tree. Raise the go-ethereum floor in go-zenon when
convenient, or add a `govulncheck` suppression file recording this analysis.

### M-15 · `reindex` has no guard against a running `serve`

**Where:** `cmd/zcribe/serve.go:170` (`cmdReindex`), `internal/store/store.go:243` (`Open`)

`zcribe reindex` opens the same SQLite file `serve` holds, calls `ResetProjections()` —
`DELETE FROM` every projection table — and replays the whole log. WAL mode plus the 10 s
busy timeout means the two processes will not corrupt the file, but for the duration of the
rebuild the live API serves an empty or half-rebuilt projection set while the running
indexer continues writing new momentums into it. `zcribe moderate` has the same shape but is
a single small write, so it is fine.

**Fix.** Take an exclusive advisory lock for the destructive commands — a `meta` row holding
a pid and a timestamp, or an OS-level lock file beside the database — and refuse with a
clear message if `serve` holds it.

---

## L — Low and informational

**L-1 · `ext: {}` is a second spelling of "no ext".** `internal/protocol/envelope.go:220`
(`ValidateExt`) accepts an empty map; `validateRefs` (`wire.go:410`) rejects `refs: []` and
the comment explains why ("two encodings of one object is exactly what R-2.3 exists to
prevent"). Both were verified: `refs:[]` → `refs present but empty`, `ext:{}` → `nil`. Not a
canonicalisation break (they produce different block hashes), but the two rules should agree.

**L-2 · A mint addressed to the burn sink credits a balance instead of burning.**
`internal/indexer/apply.go:170` sets `Owner: e.Recipient` and credits the full supply.
R-7.2 burns only on `op=1`. Minting directly to `z1zen0glyphxdurn…` produces an asset whose
entire supply shows as a holder row at the burn address rather than as burned. Harmless but
inconsistent; either burn it at mint time or document the asymmetry.

**L-3 · Inscription number 0 is a real number that several call sites treat as absent.**
`NextAssetNumber` (`projection.go:200`) returns 0 for the first mint. `bot.go:1547`
(`assetRef`) tests `num(a["number"]) > 0` and falls back to a `shortRef` token for #0;
`bot.go:711` only looks up a bare number when `n > 0`, so typing `0` at the bot does
nothing. Both work by accident. Either start numbering at 1 or use `>= 0`.

**L-4 · `/collections` and `/collection/{id}` return Go-style PascalCase JSON keys.**
`handleCollections` (`api.go:796`) marshals `store.Collection` directly and the struct has no
json tags, so the response is `{"ID":…,"Name":…,"Issuer":…}` while every other endpoint is
lowerCamelCase. Both clients work around it (`web/ui/src/types/index.ts:147` — *"Collections
come off the store with Go's exported field names"* — and `bot.go:1440` reads `m["Name"]`).
It also leaks `MomentumIndex`/`Timestamp` that the UI type does not declare, and omits
nothing. Add json tags or a `collectionJSON` view type; keep the old keys for a release.

**L-5 · `/content/{id}/{slot}` does not check the source asset's moderation.**
`handleRefSlot` (`api.go:500`) looks the asset up, reads `RefsOf`, and 301s. A *hidden*
asset therefore still reveals how many import slots it declares and what each one points at,
via the `Location` header. The target's own handler refuses correctly, so no withheld bytes
escape — only structure. Add the same `MetadataPermitted` check the sibling handlers use.

**L-6 · Error bodies echo raw store errors.** `writeErr(w, 500, err.Error())` appears
throughout `api.go` and `admin.go`. A SQLite error message can name tables and columns.
Log the detail; return a generic string.

**L-7 · `converge()` can return an unvalidated capacity.** `internal/protocol/chunk.go:236`
loops eight times and, if it never lands under the ceiling, falls through to `return n, nil`
without re-measuring. In practice the first correction is exact (overhead is
size-independent to within a length header), so it converges in two passes. Client-side
only — the node would reject an oversized block — but the fallthrough should be
`return 0, oob`.

**L-8 · Three inscriber helpers skip `Validate()`.** `Transfer` (`inscribe.go:296`),
`CancelDispenser` (`:325`) and `Purchase` (`:337`) marshal their wire objects without
calling `Validate()`, while `CreateCollection` and `CreateDispenser` do. `zcribe transfer
-amount 0` therefore publishes and pays for a block every indexer discards. One line each.

**L-9 · `-mint-cost 0` panics `/balance`.** `newCreditConfig` (`botcredit.go:396`) accepts
`whole = 0`, producing `MintCost = 0`; `cmdBalance` (`:222`) then runs
`new(big.Int).Div(balance, b.credit.MintCost)`, which panics on division by zero. The
`handle` recover contains it, so it is one dead command rather than a crash. Reject
`-mint-cost 0` at startup.

**L-10 · Two unbounded in-memory maps in the bot.** `b.lastCreate` (`bot.go:80`) gains an
entry per Telegram user who ever runs `/create` and is never pruned; `announceLoop`'s `seen`
(`bot.go:1385`) gains one per announced asset for the process lifetime. Slow leaks on a
long-running instance. `shortRef` (`market.go:63`) is capped and is the model to follow.

**L-11 · Internal errors are reported as 404.** `handleHolders` (`api.go:407`),
`handleTransfers` (`:434`), `handleRefs` (`:618`), `handleRefSlot` (`:508`) and
`handleRecursiveAsset` (`:693`) all use `if err != nil || a == nil { 404 }`, so a database
fault is indistinguishable from a missing asset — for a client and for the operator reading
the access log.

**L-12 · `contentCSP` uses `ContentOrigin` verbatim.** `api.go:588`. If an operator sets
`-content-origin https://host/` (trailing slash), every CSP source becomes a *path* source
with different matching semantics, and `contentURL` emits `https://host//content/…`.
Normalise with `strings.TrimRight(origin, "/")` at construction — `moderation.URLsFor`
already does exactly that.

**L-13 · `collection.url` is captured and never rendered.** Validated
(`wire.go:172`), stored (`store.go:118`), returned by the API — and no viewer surface shows
it. Either render it (as text, or as a link with `rel="noopener noreferrer"` and a scheme
allowlist — it is attacker-controlled) or stop storing it.

**L-14 · A same-height tip reorg is not detected until the chain advances.**
`step` (`indexer.go:200–210`) handles `frontier.Height < tip` via `chainWentBackwards` and
`next > frontier.Height` as "caught up". A fork that replaces the tip at the *same* height
falls into the second branch and is invisible until the node produces height `tip+1`, at
which point `processMomentum`'s parent-hash check catches it. Self-healing, with a delay of
one momentum. Worth a comment at minimum; comparing the stored hash at `frontier.Height`
against `frontier.Hash` on every poll would close it.

**L-15 · The admin panel's CSRF defence is `SameSite=Strict` alone.** No token, no
`Origin`/`Sec-Fetch-Site` check (`admin.go:265` `requireAdmin`). That is defensible modern
practice, but note the shape of the deployment: the same origin serves the public viewer,
which renders attacker-authored inscription markdown through `v-html`. Any escaping bug in
`web/ui/src/core/markdown.ts` becomes full admin compromise rather than a defaced page. The
markdown renderer was reviewed and looks correct (see
[Checked and found sound](#checked-and-found-sound)); a `Sec-Fetch-Site: same-origin` check
on the admin mutating routes is cheap insurance behind it.

**L-16 · `ParseBlocklist` uses a default `bufio.Scanner`.** `moderation/purge.go:40`. A line
over 64 KB returns `bufio.ErrTooLong` and aborts the whole import. Fail-closed, so only a
usability issue, but a shared blocklist from another operator is exactly the kind of file
that arrives malformed. Set `sc.Buffer` explicitly, or skip over-long lines with a warning.

**L-17 · `assetToJSON` is an N+1 query.** `api.go:288` calls `s.st.RefsOf` and
`s.st.Moderation` for every asset it renders. `GET /assets?limit=200` is 400 extra
round-trips on the single connection. Batch both with an `IN` query in the list path.

**L-18 · `-scripts` defaults to true and the same-origin warning is fragile.**
`serve.go:34`, `:131–137`. The default is defensible (recursion is the point), but the
warning that fires when the content origin equals the API origin only compares
`strings.TrimPrefix(origin, "http://")` against `apiAddr` — an `https://` content origin
never matches, and neither does a different-port-same-host setup. It also never fires for
localhost, which is where people develop. Compare parsed URLs, or promote it to a refusal
when `-scripts` is on and the two origins share a host.

**L-19 · `ClaimTail` mutates state before the mint completes.** `apply.go:242`. `assemble`
claims the tail and returns; `applyMint` then does a collection lookup, allocates a number,
writes the asset, refs and balance. None of those can currently return `invalid`, so no
claim is ever orphaned — but nothing enforces that, and the failure mode if it changes is a
tail permanently consumed by an asset that does not exist, which is unrecoverable without a
reindex. Move the claim to the end of `applyMint`, or add a comment naming the invariant.

**L-20 · `u64()` swallows parse errors.** `internal/store/projection.go:21`:
`func u64(s string) uint64 { v, _ := strconv.ParseUint(s, 10, 64); return v }`. An
unparseable amount column silently reads as zero — a balance vanishing rather than an error.
The column is only ever written from a `uint64`, so it is unreachable outside a hand-edited
database; `botstore.parseAmount` makes the same trade and says so explicitly. Worth the same
comment here.

---

## Discrepancies between documentation and code

Collected separately because they matter for whoever reads the design documents next and
takes them at face value.

| Where | Says | Actually |
|---|---|---|
| `internal/indexer/indexer.go:154` | "the next pass re-reads from the same height and is idempotent" | It is not — [H-1](#h-1--a-partially-processed-momentum-is-re-applied-on-the-next-pass-supply-inflation) |
| `deploy/systemd/zcribe.service` | "A crash mid-momentum is safe to restart into … every projection write is idempotent" | `store.Move` is not; `Restart=always` makes this the default recovery path |
| `internal/api/admin.go:369` | "A spoofed X-Forwarded-For only ever degrades this to a shared bucket" | It escapes the bucket entirely — [H-3](#h-3--admin-login-throttling-is-bypassable-and-the-password-hash-is-unsalted-sha-256) |
| `ASSUMPTIONS.md` A-11 / spec R-7.1.3 | The tail rule closes the storage-amplification hole | It closes the *tail* case; chain prefixes remain, ~30× — [M-1](#m-1--r-713-does-not-bound-storage-amplification-chain-prefixes-are-reusable) |
| `internal/rpcclient/client.go:105` | "the slice order the node returns … must never be re-sorted client side" | `canonicalOrder` re-sorts and prefers its own answer, correctly — [M-3](#m-3--the-indexer-trusts-the-node-completely) |
| `TODO.txt` (DONE) | The discarded-content-blob read "is gone" | Fixed in `ListAssets` only; six other paths still do it — [H-6](#h-6--every-metadata-endpoint-reads-the-full-content-blob-and-discards-it) |
| `internal/store/store.go:16` | Amounts are TEXT because SQLite INTEGER would overflow | Three queries cast them back to INTEGER — [M-2](#m-2--balances-above-2⁶³1-sort-wrongly-cast-amount-as-integer) |
| `cmd/zcribe/inscribe.go:50` | "A phrase is never accepted as a command-line argument" | `-password` is — [M-12](#m-12--keystore-password-is-accepted-on-the-command-line) |
| `cmd/zcribe/inscribe.go:22`, `bot.go:132` | devnet mnemonic is "chain 69 only" | Nothing checks the chain id — [M-11](#m-11--devnet-uses-a-publicly-known-key-with-no-chain-guard) |
| `internal/api/admin.go:29` | "The panel never renders inscribed bytes on this origin" | True of the panel; the *viewer* on the same origin does — [L-15](#l--low-and-informational) |
| `spec/SPEC.md` R-4.7.1 rationale | Two encodings of one object are rejected | `refs: []` is; `ext: {}` is not — [L-1](#l--low-and-informational) |

---

## Checked and found sound

An adversarial review is only worth reading if it says what it failed to break. These were
attacked specifically and held up.

**Wire format and canonical CBOR.** `requireCanonical` (`envelope.go:249`) decodes to a
generic value and re-encodes with the deterministic encoder, comparing bytes — which catches
non-shortest integers, indefinite lengths and key misordering in one pass. `decMode` sets
`DupMapKeyEnforcedAPF`, `IndefLengthForbidden`, `UTF8RejectInvalid` and
`ExtraDecErrorUnknownField`. `decodePayload` (`wire.go:325`) checks the exact key set against
the map actually on the wire *and* re-decodes into the typed struct, so a missing required
key is distinguishable from a zero value. Probed with `refs: []`, `ext: {}`,
`attributes: [1]`, `supply: 0`, `supply: -1` — every one rejected with the right message
except `ext: {}` ([L-1](#l--low-and-informational)). `zcribe vectors -check` reports
`12 vectors match`.

**The byte-signature fast path.** `HasSignature` (`envelope.go:311`) accepts map headers
`0xA4`–`0xA7`, which is exactly the 4-to-7-entry range the encoder can produce (four
mandatory keys plus `ext`, `pHash`, `tHash`, all of which sort after `op`). The signature
is derived from the live `Prefix` constant, so it cannot desync. Length is checked before
every slice.

**Multi-part walk bounds.** Same-issuer (R-7.1.1), strictly-earlier (R-7.1.2), the 64-part
cap and cycle detection are all present in `assemble` and all check *before* appending
bytes. `MaxAssetParts` is enforced at the top of the loop with `count` seeded at 1, so the
head counts toward the limit. The two-block case (`pHash == tHash`) works. The only gap is
the prefix reuse in [M-1](#m-1--r-713-does-not-bound-storage-amplification-chain-prefixes-are-reusable).

**Opcode dispatch cannot be crashed by chain data.** `Opcode.Known()` bounds the table index
(`protocol.go:145`), `Apply`'s `default` returns `(false, nil)`, and `errInvalid` cleanly
separates "the rules rejected it" from "storage failed". One block carrying `op: 99` costs
nothing.

**Dispenser settlement.** `applyPurchase` (`apply.go:404`) checks the payment recipient is
the dispenser's issuer *before* anything else, matches the token standard against the
dispenser's `zts`, parses the amount as a `big.Int`, guards `price == 0`, computes
`floor(paid/price)`, and refuses the whole purchase when the quantity exceeds `remaining` or
does not fit a `uint64`. No partial fills, no refund path assumed. Escrow moves stock out of
the seller's spendable balance at create time, so it cannot be double-sold. Cancel is
issuer-only and once-only.

**SQL injection.** Every statement uses bound parameters. The two places that build SQL by
concatenation — `ListAssets`' filter clause (`projection.go:135`) and the table loops in
`ResetProjections`/`RestoreSnapshot` — concatenate only from fixed literal lists, never from
request data. Asset names and descriptions arrive from arbitrary blocks and are always bound.

**Identity / `/link` / `/verify`.** `identity.Verify` (`identity.go:105`) checks the address
derivation *before* the signature, so a valid signature by an unrelated key proves nothing.
The signed message binds the protocol name, the address, the Telegram account id, a
128-bit CSPRNG nonce and an expiry — a proof captured from one account cannot be replayed by
another. `botstore.Challenge` (`botstore.go:196`) returns nil for an expired row, so
`cmdVerify` cannot accept a stale challenge, and `Link` deletes the challenge in the same
transaction as the identity write. A unique index on `identities.address` prevents two
accounts claiming one address. `/link` refuses to run in a group chat.

**Credit ledger idempotency.** `credit_ledger` has a unique index on `(reason, ref)` where
`ref <> ''`, and `move()` checks it inside the transaction, so the deposit watcher re-reading
the same unreceived block cannot mint credit twice. Crediting strictly before receiving means
a crash between the two leaves a credited-but-unreceived deposit, which the next sweep
finishes — the harmless direction. Amounts are `*big.Int` end to end.

**Content isolation.** `contentCSP` (`api.go:588`) emits `sandbox allow-scripts` and never
`allow-same-origin`, pins all eight fetch directives to the content origin, and sets
`base-uri 'none'` / `form-action 'none'`. The viewer's iframes use the same token list from
`/info` (`useIndexer.ts:45`), so the page never renders more than the origin enforces.
`nosniff`, `Content-Disposition: inline` with no filename, and the recorded mime type are
all set. The deploy configs put the two origins on separate hostnames and deliberately add
no CSP at the content proxy, with the merge semantics explained correctly.

**The markdown renderer.** `web/ui/src/core/markdown.ts` escapes `& < > "` on the whole
input first and only then emits its own tags, so no caller-supplied markup survives to the
`v-html`. The link rule requires an `https?://` scheme and forbids `)` and whitespace in the
URL, and the `"` that could close the `href` attribute has already become `&quot;` — which
an HTML parser reads as a character reference inside the value, not as a delimiter. The
blockquote rule correctly matches `&gt;` post-escape. Everything else in the viewer uses
`{{ }}` interpolation. `grep` for `innerHTML`, `eval`, `new Function` across `web/ui/src`
returns only this one, documented, `v-html`.

**Access logging.** `accesslog.go` logs no headers and no bodies, by rule and in fact — so
the admin session cookie and the login password never reach the log.

**Reorg search.** `findLastAgreeingHeight` (`indexer.go:672`) doubles back then binary
searches, which is right for both a shallow fork and a full chain reset, and the underflow
case (`hi <= floor+step`) is handled. `checkChain` pins the chain identifier in `meta` and
makes a mismatch fatal. `chainWentBackwards` distinguishes a lagging node from a replaced
chain by asking what the node has at its own tip. The reorg tests cover all four cases
against a fake node.

**Checkpoints.** `RestoreSnapshot` truncates the insert-only projections by height and
restores `balances` and `dispensers` from the copy — the split matches which tables mutate in
place, and `TestSnapshotRestoreMatchesFullRebuild` compares `ProjectionDigest` between the
checkpointed and full-replay paths. `DropSnapshotsAbove` runs before `RebuildFrom` in the
reorg path, so an abandoned branch's checkpoint is never restored.

**`format()` in the credit config.** `fmt.Sprintf("%0*s", …)` was suspected of padding with
spaces; verified that Go's `fmt` zero-pads strings under the `0` flag. Not a bug.

---

## Suggested remediation order

1. **[C-1]** Revoke the Telegram token and the admin password; move both `.env` files out of
   the tree; add `*.env` to `.gitignore`. Nothing else on this list matters if these leak.
2. **[H-2]** Put `logout` behind `requireAdmin` and nil-check `s.admin`. Four lines.
3. **[H-4]** Move the `defer recover()` to the top of `bot.handle` and guard `len(fields)`.
   Three lines.
4. **[H-3]** Fix the `clientIP` trust model, then move the password to argon2id. The first
   half is small and closes the practical attack; the second can follow.
5. **[H-1]** + **[M-5]** + **[M-8]**: momentum-level atomicity. Ship the `meta` marker guard
   first (small, closes both crash windows), then the transaction refactor.
6. **[H-6]** + **[M-13]**: split `AssetMeta` from `Asset`, and cap the four unbounded
   listings.
7. **[H-5]** Page the unreceived-block sweep.
8. **[M-4]** Make moderation fail closed — one-line change per call site, six sites.
9. **[M-2]**, **[M-6]**, **[M-7]**: correctness and cost fixes with no protocol impact.
10. **[M-1]** Decide between tightening the claim to every part and building content-addressed
    storage, then correct A-11 and R-7.1.3 to match whichever is chosen. This is a consensus
    change; it needs a version bump and coordination, so it should not be rushed ahead of the
    items above.
11. **[M-3]** Verify momentum content and block hashes against the node's own commitments.
12. Everything under **L**, and the documentation corrections in
    [Discrepancies](#discrepancies-between-documentation-and-code), as they are touched.

---

## Method notes

- Probe tests were written into `internal/{protocol,api,indexer,store}/zz_probe*_test.go`,
  run, and deleted. `go test ./...` passes on the tree as left.
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` — 5 called vulnerabilities, all
  go-ethereum ([M-14](#m-14--five-reachable-vulnerabilities-in-go-ethereum-11022)).
- `npm audit` in `web/ui` — 0 vulnerabilities, dev dependencies included.
- `go run ./cmd/zcribe vectors -check` — 12 vectors match.
- Nothing in this review was run against a live network, and no live devnet or mainnet state
  was touched.
