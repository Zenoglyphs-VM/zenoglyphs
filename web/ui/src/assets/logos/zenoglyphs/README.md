# Mark exploration — round two (zenoglyphs)

Eight marks drawn for the **zenoglyphs** rebrand, compared against the
then-adopted `../node-z.svg` at the sizes the mark actually ships at (64px,
header lockup, 16px favicon tab, light and dark).

The comparison is [`zenoglyphs.mockup.html`](zenoglyphs.mockup.html) in this
folder — open it directly in a browser, no build, the same way the round-one
mockup works. (It does load Space Grotesk / JetBrains Mono from Google Fonts,
unlike that one, which used system stacks: half the question this round is how
each mark sits against the wordmark, and a substituted face answers a different
question.)

**`rune-z.svg` was adopted.** It's live in
[`../../../components/ZcribeMark.vue`](../../../components/ZcribeMark.vue) and
[`../../../../public/favicon.svg`](../../../../public/favicon.svg), replacing
`node-z`. The other seven are kept here unchanged, as the record of what else
was considered.

## Why a second round

The name change is the whole reason. *Zenscriptions* named the act of
inscribing, and `node-z` answered it with a route **traced** between nodes.
*Zenoglyphs* names the artifact left behind, so most of these ask whether the
mark should instead read as something **cut**.

| File               | Concept                                                                                                                                                          |             |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| `rune-z.svg`       | Mitred joins, square caps, one weight, a bind-stroke struck across the diagonal. Nothing rounded — the radius token's own logic taken to the mark.               | **adopted** |
| `chisel-z.svg`     | Three tapering wedges — a Z as a chisel leaves it. The most direct answer to the new name (glyphs began as cuneiform), and all fill, so it holds weight at 16px. | considered  |
| `slotted-z.svg`    | One continuous route with a hairline slot at each corner. `fragment-z`'s idea, kept corner-to-corner so the slots close up small instead of falling apart.        | considered  |
| `lattice-z.svg`    | `node-z`'s four nodes grown to the full seven-point lattice, the Z demoted to the route through it. Says block-lattice, but the letter goes quiet.               | considered  |
| `momentum-z.svg`   | The route ticked rather than solid — a ledger advancing in discrete momenta. Watch at 16px.                                                                      | considered  |
| `seal-glyph-z.svg` | Round one's strongest runner-up (`seal-z`) redrawn as an outline with the route inside, instead of a knockout slab.                                              | considered  |
| `stele-z.svg`      | The glyph on its tablet. The most literal, and the most at risk small — two stroke weights inside 16px is a lot to ask.                                          | considered  |
| `coin-z.svg`       | The Z struck on a disc. Sits naturally beside `TokenIcon` / `ZnnLogo` — which is either the point or the objection, since an inscription index is not a token.   | considered  |

All eight: 24×24 viewBox, the same `--plasma` gradient as every brand mark in the
design system (`zenon-docs/frontend/design-system.md` in the monorepo),
`fill="none"` at the root — the same constraints round one held to. Each file
carries its own `defs` gradient so it stands alone; the mockup declares `#plasma`
once and shares it, which is the only difference between the two.
