# Mark exploration

Six concepts were drawn for zcribe's app mark — the original angular Z plus
five alternatives — compared at the sizes the mark actually ships at (header
lockup, 64px, 16px favicon tab, light and dark):
<https://claude.ai/code/artifact/b0944023-6839-4bba-a29c-07845fcab81a>

**`node-z.svg` was adopted.** It's live in
[`../../components/ZcribeMark.vue`](../../components/ZcribeMark.vue) and
[`../../../public/favicon.svg`](../../../public/favicon.svg).
[`node-z.mockup.html`](node-z.mockup.html) in this folder is the kept record of
why: the same header/64px/16px-tab swatches from the comparison above, isolated
to just this mark, self-contained (open it directly in a browser — no build,
no CDN, matching how the rest of this project ships).

| File                 | Concept                                                                                                                                                                                                                                                    |             |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| `node-z.svg`         | A single-weight Z route between four small nodes — closer to the system's network glyphs than to a drawn letterform.                                                                                                                                       | **adopted** |
| `ledger-z.svg`       | A bold, straight-edged block Z — no rounding, matching the network glyphs' sharp corners.                                                                                                                                                                  | considered  |
| `inscribed-chip.svg` | A Z cut as a knockout through a tight-radius square — reads as a stamped die.                                                                                                                                                                              | considered  |
| `fragment-z.svg`     | Three unconnected strokes that read as a Z only through alignment — a nod to composite inscriptions assembled from separately addressed parts. Weakest of the five at 16px — the gaps that make it interesting at header size read as noise in a real tab. | considered  |
| `seal-z.svg`         | The `inscribed-chip` knockout inside a hexagonal seal — reads best small (16px favicon scale), the strongest runner-up.                                                                                                                                    | considered  |

All six: 24×24 viewBox, filled/stroked with the same `--plasma` gradient as
every brand mark in the design system (see `zenon-docs/frontend/design-system.md`
in the monorepo), `fill="none"` at the root. The four still-fill-based concepts
(`ledger-z`, `inscribed-chip`, `fragment-z`, `seal-z`) are kept here unchanged,
as the record of what else was considered — not wired into the app, and there's
no plan to adopt a second one.

---

**Superseded.** `node-z` was replaced by `zenoglyphs/rune-z.svg` for the
*zenoglyphs* rebrand — see [`zenoglyphs/README.md`](zenoglyphs/README.md).
`node-z.svg` is kept here unchanged as the record of round one; it is no
longer wired into the app.
