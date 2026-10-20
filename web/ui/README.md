# zcribe viewer

The Zenoglyphs inscription viewer. Vue 3 + Vite + Tailwind CSS 4 on
[`nom-ui`](https://github.com/digitalSloth/nom-ui) — the same stack as
[`nom-webwallet`](https://github.com/digitalSloth/nom-webwallet), so a wallet, an
explorer and this viewer read as one product rather than three.

## Commands

```sh
npm install --allow-git=all   # nom-ui is a GitHub dependency; npm 12 needs the flag
npm run dev                   # dev server on :5173, API proxied from :8710
npm run build                 # → ../dist, which web/embed.go embeds
npm run typecheck             # vue-tsc, strict
npm run lint
npm run format
```

`npm run dev` proxies the metadata API to a running `zcribe serve` on
`127.0.0.1:8710`. Inscribed bytes are not proxied: they come from the separate
content origin the API names in `/info`, which is an absolute URL and a
different origin on purpose.

`zcribe serve -web ../dist` serves a build from disk instead of the embedded
copy, which is useful when iterating without rebuilding the Go binary.

## The admin panel

`admin/` is a second, separate app — its own Vite entry (see
`vite.config.ts`'s `rollupOptions.input`), its own bundle, nothing imported
from `src/` outside the shared components and design tokens. It builds to
`../dist/admin/index.html`, which is what makes plain `GET /admin` resolve to
it (the redirect and directory-index serving are `net/http`'s, not anything
this app configures). It is never linked from the viewer and never appears in
`router.ts` — see `internal/api/admin.go` for why the path is not the
security boundary. `npm run dev` proxies `/admin/api` alongside the public
paths, so `http://localhost:5173/admin/` works against a live `zcribe serve`
too.

## What is load-bearing here

**Nothing is fetched at runtime.** Every dependency, the webfonts included, is
bundled into `../dist`. The viewer has to render identically on a machine with
no internet running against a local devnet, and a page that silently depends on
a remote script is a page that renders differently depending on the network.
Do not add a CDN link, a Google Fonts `@import`, or a runtime-loaded library.

**Inscribed bytes never touch this document.** Inscribed HTML and SVG can
execute script, so they are only ever rendered inside a sandboxed `<iframe>`
pointed at the content origin — `allow-scripts` when the indexer permits it (see
`useIndexer`), `allow-same-origin` never, because withholding it is what keeps
one inscription's script out of another inscription frame's DOM. Everything
else reaches the DOM as text through Vue's interpolation.

The one `v-html` in the app renders `core/markdown.ts`, which escapes its input
before emitting any tag of its own. If you touch that file, the escaping is the
part to review.

**Hashes link out; the indexer decides where.** `/info` publishes one URL
template per object kind (`account`, `transaction`, `momentum`, `token`), and
`core/explorer.ts` substitutes into it. A missing template is meaningful — it
says the configured explorer has no page for that kind of object, and the value
must render as plain copyable text rather than a link that 404s. zenonhub.io
has no per-momentum page, so heights are unlinked on mainnet and linked on the
devnet bundle. Adding an explorer is a preset in `internal/api/explorer.go`,
never a change here.

**Prose belongs on `/docs`, not on detail views.** `DocsPage.vue` reads its
numbers — the MIME allowlist, block ceilings, confirmation depth, checkpoint
window — from `/info`, so it cannot drift from the binary serving it. An
explanation that is the same for every asset (how a composite slot resolves,
what soft confirmation means) goes there and is linked to, rather than being
reprinted above every asset that happens to have one.

**A listing shows a page, and says so.** The gallery asks for 20 inscriptions
and the heading reads "20 of 137" whenever there are more — it used to ask for
everything, receive the backend's default 100, and show no sign that the rest
existed. Its search box takes an inscription id or number as well as an address:
`GET /asset/{id|number}` accepts either, so a 64-hex string or an integer
navigates straight to the asset and anything else filters by owner.

**An address is a page, not a filter.** `/#/address/:addr` reads the balance
table, which is the live answer to "what does this hold"; the gallery's
`?owner=` filter reads `assets.owner`, which is written once at the mint and
never updated by a transfer. Both exist and each links to the other, but an
address rendered anywhere else links here. A dispenser's escrow is an address
too (`z1zen0glyphxescr0w…`) and resolves like any other, because the API matches it
back to its listing.

**Withheld means redacted, and the bytes are never offered.** An inscription
whose content this operator will not serve keeps its metadata — number, size,
issuer, activity — and shows "redacted" in place of the render, on the card, the
thumbnail and the detail page. The "raw bytes" link is replaced rather than left
to answer 410. What the chain holds is unaffected, which is why the wording says
who is refusing.

**The design system's rules apply.** Amounts, addresses, hashes and heights are
mono + `tabular-nums`; labels use `text-ledger`; the plasma gradient is reserved
for the primary action. See `zenon-docs/frontend/design-system.md` in the
monorepo for the full index.

## Layout

```
admin/index.html     the admin panel's own entry — see "The admin panel" above
src/
├── core/            API client, formatters, the markdown renderer, explorer links
│   └── composables/ useIndexer — one shared /info poll for the whole app
├── components/      chrome + the inscription renderers
├── pages/           one per route
├── types/           the shapes internal/api/api.go returns
├── router.ts         hash routes (#/asset/…), so existing links keep working
└── admin/           the admin panel: its own main.ts, api.ts, views/ — not
                      part of the router above, see admin/index.html
```
