# zcribe — Telegram avatar candidates

Profile-photo candidates for the `zcribe` Telegram bot (`cmd/zcribe/bot.go`).
One gets picked and set via BotFather (`/setuserpic`); the rest stay here as
the record of what was considered.

Each one is a headed, armless figure with the ledger Z on its chest — no
antenna, no arms — on the plasma gradient the web UI already uses
(`hsl(120 86% 63%)` → `hsl(145 100% 38%)`) over the same dark ink ground. The
Z is the one from `web/ui/src/assets/logos/ledger-z.svg`, unmodified; only its
scale and placement differ between variants.

| file | what changes |
| --- | --- |
| `avatar-chest-z-plain` | The baseline: square head, two eyes, box torso. Head, torso and Z all grew into the space the antenna and arms used to take. |
| `avatar-chest-z-visor` | One wide aperture instead of two eyes and a mouth — a single large feature survives the small crop where three small ones close up. |
| `avatar-chest-z-round` | Circular head over a capsule torso. Softer, and a round head is already the shape the crop wants. |
| `avatar-chest-z-wide` | A squat head broader than the torso under it. Reads as a character rather than a diagram. |
| `avatar-chest-z-shoulders` | Torso tapered into a shoulder line, so the figure has shoulders without needing arms. |
| `avatar-chest-z-screen` | The Z lit inside a dark chest panel instead of cut out of the torso — something the bot displays rather than a hole in it. |

Faces and panels are knockouts through one plasma silhouette, not dark shapes
painted on top, so the ink ground shows through — the way
`web/ui/src/assets/logos/inscribed-chip.svg` does it. The tapered torso is a
stroked polygon with round joins rather than a hand-fitted path, which is why
its corners match the head radius for free.

`preview.html` / `preview.png` show all six under the circular crop at the
three sizes Telegram renders a bot photo at. Judge them there, not at 512 —
detail that reads well large goes mushy at 28px.

## Regenerating

The `.svg` files are the sources; the PNGs are build output.

```sh
./render.sh            # all avatar-*.png at 512x512, then preview.png
CHROME=/path/to/chrome ./render.sh
```

512×512 is what Telegram wants for a bot photo; it produces the smaller sizes
itself. Edit an SVG and re-render rather than touching a PNG.
