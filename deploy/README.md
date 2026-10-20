# Running zcribe on mainnet

Three services, two hostnames, one cache. Everything here follows from two
rules in the spec, so the shape is not a matter of taste:

- **R-8.2** — content bytes are served from a different origin than the API,
  because the allowlist includes `text/html` and `image/svg+xml` and both
  execute script.
- **R-8.5** — with recursion on, an inscription may load other inscriptions and
  nothing else. That is enforced by a CSP built from the content origin, so the
  origin the indexer is told about has to be the one browsers actually reach.

---

## 1. Names

| Host | Serves | Public |
|---|---|---|
| `glyphs.zenon.foo` | viewer + JSON API (`zcribe serve -api`) | yes |
| `content.zenon.foo` | inscription bytes and `/r/…` (`zcribe serve -content`) | yes, behind the CDN |
| — | the NoM node's RPC | **no**, localhost only |

### Why these are subdomains, and what that costs

Two subdomains of one registrable domain is what this deployment uses. Two
separate registrable domains would be stronger, and if you have them, use them
— the config needs no change beyond the names. What follows is the reasoning
for the layout that ships, so the difference is a decision rather than an
oversight.

**A subdomain is a different origin.** That is the boundary R-8.2 is asking
for and the one that carries almost all of the weight: the DOM, `localStorage`,
IndexedDB, `fetch`, and every same-origin read are keyed by origin, so script
in an inscription on `content.zenon.foo` cannot reach anything belonging to
`glyphs.zenon.foo`. On this axis a subdomain and a separate domain are
identical.

**A subdomain is not a different site**, and the cookie jar is scoped per site.
That is the entire gap, and it has two directions:

- *Outward* — the admin cookie leaking to the content host. It cannot: it is
  issued host-only (no `Domain` attribute) and `Path=/admin`, so the browser
  will not send it to a sibling subdomain, and does not attach it to ordinary
  API or viewer requests either.
- *Inward* — script on the content host setting a cookie with
  `Domain=zenon.foo`, which the browser then sends here. It cannot forge a
  session; the value is HMAC-signed with a secret the content origin never
  sees. What it could do is *shadow* the operator's real cookie, since a
  `Cookie` header does not say which host set each entry. `validSession` in
  `internal/api/admin.go` therefore checks every cookie of the name rather
  than the first, which closes it.

**Add the content host to the [Public Suffix List](https://publicsuffix.org)
private section.** That makes `content.zenon.foo` a site in its own right and
closes the cookie gap at the browser rather than in application code. Until
that lands, the two mitigations above are what stands in for it — which is
enough, but is the reason to prefer a separate registrable domain if the choice
is open to you.

**If you do use two registrable domains,** the `validSession` behaviour and the
cookie scoping above cost nothing and stay as they are; the PSL entry becomes
unnecessary.

## 2. The indexer

```bash
zcribe serve \
  -node http://127.0.0.1:35997 \
  -db /var/lib/zcribe/zenoglyphs.db \
  -api 127.0.0.1:8710 \
  -content 127.0.0.1:8711 \
  -content-origin https://content.zenon.foo \
  -start <LAUNCH_MOMENTUM> \
  -confirmations 60 \
  -snapshot-interval 120 \
  -snapshot-retain 360 \
  -scripts \
  -explorer auto
```

Two flags decide things you cannot change later without telling your users:

- **`-start`** is the launch momentum and is part of the consensus base case
  (A-25): `stateRoot(start − 1)` is 32 zero bytes. **Every operator must use the
  same value**, or no two indexers will ever agree on a state root. Publish it
  next to the software.
- **`-scripts`** decides whether inscribed HTML and SVG execute. On, and
  recursion works — an inscription can import another as a library. Off, and
  every inscription is a static picture. See §7.

`-snapshot-retain 360` is one hour of momentums: a reorg shallower than that
replays at most `-snapshot-interval` momentums of events instead of the whole
log. `-confirmations 60` is what the API calls fully confirmed. Note that the
second number is smaller than the first on purpose — see §8.

`-explorer` decides where the viewer links hashes for on-chain verification.
`auto` picks by the chain identifier the node reports — zenonhub.io on
mainnet, the static devnet bundle on chain 69, and nothing on a chain it does
not recognise, because a link to the wrong network's explorer is worse than no
link. Override it with `zenonhub`, `devnet`, `none`, or a URL template
containing `{v}`. Note that zenonhub has no per-momentum page: the preset
leaves that template empty and the viewer renders heights as plain text rather
than as links that 404.

Both HTTP servers bind to localhost. The reverse proxy is what faces the world.

## 3. Reverse proxy

`Caddyfile` and `nginx.conf` in this directory are complete and equivalent.
Caddy is the shorter path — it gets certificates on its own.

```bash
cp deploy/Caddyfile /etc/caddy/Caddyfile   # edit the two hostnames
systemctl reload caddy
```

Whichever you use, two headers must survive the hop:

- `Content-Security-Policy` — the proxy must not add, replace, or merge one.
  The indexer builds it per response and it is the sandbox.
- `Content-Type` — served exactly as recorded at mint time, with
  `X-Content-Type-Options: nosniff` beside it.

## 4. systemd

```bash
useradd --system --home /var/lib/zcribe --shell /usr/sbin/nologin zcribe
install -d -o zcribe -g zcribe /var/lib/zcribe
install -m755 zcribe /usr/local/bin/zcribe
cp deploy/systemd/*.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now zcribe
```

The Telegram front end is a second unit and is optional:

```bash
install -d -m750 -o zcribe -g zcribe /etc/zcribe
printf 'ZCRIBE_TELEGRAM_TOKEN=…\n' > /etc/zcribe/bot.env
chmod 600 /etc/zcribe/bot.env
systemctl enable --now zcribe-bot
```

[`scripts/`](scripts/) has this install, plus day-2 operation on top of it:
restarting, shipping a new binary without racing a running process, tailing
logs by unit name instead of by hand, and a health check that goes past
`systemctl is-active` to check sync lag and the confirmations/retain
invariant from §8. See [`scripts/README.md`](scripts/README.md).

## 5. Admin panel

Unset by default — `ZCRIBE_ADMIN_PASSWORD` turns it on, and belongs in an
`EnvironmentFile`, the same way the bot's token does:

```bash
printf 'ZCRIBE_ADMIN_PASSWORD=…\n' > /etc/zcribe/admin.env
chmod 600 /etc/zcribe/admin.env
```

then add to `zcribe.service` (the shipped unit already has this line,
commented out):

```ini
EnvironmentFile=-/etc/zcribe/admin.env
```

The leading `-` makes the file optional, so a host that never creates it
starts the same as before this existed. `systemctl restart zcribe` picks up a
password change or a newly-created file; `serve` logs `admin panel enabled`
or `admin panel disabled` on every start so a look at the journal says which.

The panel lives at `/admin` on the `glyphs.zenon.foo` reverse-proxy config
above — nothing in `Caddyfile` or `nginx.conf` needs to change, since it is
already proxying everything on that host to `127.0.0.1:8710`. See
[`internal/api/admin.go`](../internal/api/admin.go) for the auth model and §1
for why the session cookie it sets does not undermine the two-hostname split.

## 6. The CDN

Put a cache in front of **the content host only**. The API host publishes a
moving sync height and gains nothing from being cached; the content host serves
bytes that are immutable by construction — the id *is* the block hash — so
`Cache-Control: public, max-age=31536000, immutable` on those responses is
simply true rather than optimistic.

### 5.1 Turn off everything that rewrites bytes

This is the part that is easy to get wrong, and getting it wrong is not a
performance problem — it breaks the protocol's central claim.

An inscription's id is a commitment to its bytes. A CDN feature that "optimises"
those bytes serves something the chain never committed to, while the URL still
says it did. On Cloudflare, for the content host, **all of these must be off**:

| Feature | What it would do |
|---|---|
| Speed → Optimization → **Auto Minify** | rewrites inscribed HTML, CSS and JS |
| Speed → Optimization → **Rocket Loader** | injects a script into inscribed HTML |
| Speed → Optimization → **Mirage** / **Polish** | re-encodes inscribed images |
| Scrape Shield → **Email Obfuscation** | rewrites anything that looks like an address |
| Scrape Shield → **Server-side Excludes** | rewrites HTML |
| Rules → **HTML Rewrite / Managed Transforms** | rewrites HTML |

Compression (gzip, Brotli) is fine: the bytes are restored before the page ever
sees them.

### 5.2 Cache rules

| Path | Rule | Why |
|---|---|---|
| `/content/*` | Cache eligible, Edge TTL **1 year**, respect origin headers | immutable by construction |
| `/r/momentum` | **Bypass cache** | it is the chain tip; a cached one is a lie |
| `/r/asset/*`, `/r/refs/*` | Edge TTL **60 s** | confirmation counts and reverse edges move |
| everything else | bypass | there is nothing else |

Cache on the **full URL including query string**, and do not normalise the
trailing slash: `/content/<id>` (a 301) and `/content/<id>/` (the bytes) are
different resources, and `/content/<id>/0` is a third.

### 5.3 Purging is part of moderation

An immutable, year-long TTL means a blocked id keeps being served from cache
after the origin stops serving it. `zcribe moderate` knows this and will do the
purge if you give it credentials:

```bash
# /etc/zcribe/moderate.env, read by whoever runs the command
ZCRIBE_CONTENT_ORIGIN=https://content.zenon.foo
ZCRIBE_CF_ZONE=<zone id>
ZCRIBE_CF_TOKEN=<API token with Zone:Cache Purge>
```

```bash
zcribe moderate -db … -asset <id> -hide -reason "…" -purge
zcribe moderate -db … -import blocklist.txt -purge
zcribe moderate -purge-help          # the full list of options
```

Without credentials it prints the URLs that need purging and does nothing else,
which is also a reasonable way to run it. `zcribe moderate -export` writes the
list in the same format `-import` reads, so operators can share lists without
having to agree on anything else.

Blocking is a **serving** decision and never a protocol one: it does not touch
the event log, does not change validity, and does not enter the state root
(R-6.5). An operator who hides an asset still agrees with every other operator
about what the chain contains, and anyone who disagrees with the list can run
their own indexer and serve it. That is what makes it acceptable for an
operator to keep a list at all.

`-unseen` blocks an id this indexer has not reached yet. Waiting until content
is indexed before you are allowed to refuse it means serving it first.

## 7. Deciding about `-scripts`

With `-scripts` on, an inscribed page executes inside
`sandbox allow-scripts`, in an opaque origin, with every fetch directive pinned
to the content origin. Concretely it may:

- load another inscription as a script, stylesheet, image, font or data file;
- read `/r/momentum`, `/r/asset/<id>`, `/r/refs/<id>`, `/r/children/<id>`.

It may not: reach the API origin, reach any other host on the internet, set or
read a cookie, navigate the top window, submit a form, or touch another
inscription's frame — `allow-same-origin` is never granted, so every inscribed
page is in its own opaque origin even though they share a host.

The residual risk is the honest one: arbitrary JavaScript from strangers runs in
your visitors' browsers, and the sandbox is only as good as their browser. What
you get for it is the whole point of composability — one library inscribed once,
imported by everything after it. `ord` made the same trade and it is why
recursive inscriptions exist there.

`-scripts=false` restores the older behaviour, and the viewer follows the
indexer: it reads `scriptsEnabled` from `/info` and sets the iframe sandbox to
match, so the two can never drift apart.

## 8. Confirmation depth and the reorg window

They answer different questions and the numbers do not have to match:

- `-confirmations 60` (~10 minutes) is what the API reports as `status: "full"`.
- `-snapshot-retain 360` (~1 hour) is how deep a reorg the indexer can recover
  from without replaying the whole event log.

The second is deliberately larger. Checkpoints cost nothing to keep and being
able to unwind further than you promise is the right direction for the
inequality to point. `zcribe serve` warns if you invert it. If you would rather
tell users nothing is final until the indexer could not be surprised, set
`-confirmations 360` and the two claims become the same claim.

## 9. Checking it

```bash
curl -s https://glyphs.zenon.foo/info | jq '{syncHeight,nodeHeight,stateRoot,scriptsEnabled}'

# content is at the trailing-slash URL; the short form redirects to it
curl -sI https://content.zenon.foo/content/<id>          # 301
curl -sI https://content.zenon.foo/content/<id>/ | grep -i content-security-policy

# a composite's import slot resolves to the imported asset's own URL
curl -sI https://content.zenon.foo/content/<composite>/0 # 301 -> /content/<import>/

# two indexers agreeing here agree on the entire history to that height
curl -s https://glyphs.zenon.foo/stateroot/1000000
zcribe compare -a https://glyphs.zenon.foo -b https://someone-else.example
```

`zcribe compare` binary-searches the two state-root chains and names the exact
momentum where they part company. `GET /state` adds a digest of the whole
projected state, which catches the case two state roots cannot: two indexers
that agree on which events applied and still compute different balances.
