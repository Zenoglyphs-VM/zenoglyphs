package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// cmdDemo inscribes one document of every renderable class, so the viewer can
// be exercised end to end without hand-assembling files.
//
// The last two entries are the interesting ones: a large PNG that has to be
// split across several blocks, which exercises the whole multi-part path, and
// a collection that the others join, which exercises the issuer check on
// collection membership.
func cmdDemo(ctx context.Context, args []string) error {
	set := fs("demo")
	var w walletFlags
	w.register(set)
	skipBig := set.Bool("skip-large", false, "skip the multi-block image (much faster)")
	_ = set.Parse(args)

	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}
	me := in.Address()
	fmt.Printf("inscribing demo set from %s\n\n", me)

	collectionID, err := in.CreateCollection(ctx, protocol.CollectionWire{
		Name:        "Demo Set",
		Description: "One document of every content class the v1 allowlist permits.",
		URL:         "https://zenon.network",
	})
	if err != nil {
		return fmt.Errorf("collection: %w", err)
	}
	fmt.Printf("  collection  %s  Demo Set\n", collectionID)

	// The collection must be indexed before a mint can join it — membership
	// binds only when the indexer can see that the same account issued both.
	if _, err := in.WaitForConfirmation(ctx, collectionID); err != nil {
		return fmt.Errorf("waiting for collection: %w", err)
	}

	items := []struct {
		name, desc, mime string
		body             []byte
		supply           uint64
	}{
		{"readme.md", "A markdown document, rendered by the viewer.", "text/markdown;charset=utf-8", []byte(demoMarkdown), 1},
		{"note.txt", "Plain text, shown verbatim.", "text/plain;charset=utf-8", []byte(demoText), 1},
		{"sigil.svg", "Vector art. SVG executes script, so it renders in a sandboxed frame.", "image/svg+xml", []byte(demoSVG), 1},
		{"card.html", "An inscribed HTML page, sandboxed on a separate origin.", "text/html;charset=utf-8", []byte(demoHTML), 1},
		{"manifest.json", "Structured data, shown as code.", "application/json", []byte(demoJSON), 1},
		{"gradient.png", "A deterministic gradient, minted with a supply of 100.", "image/png", makePNG(64, 64), 100},
	}

	for _, it := range items {
		meta := protocol.MintWire{
			Name: it.name, Description: it.desc, Mime: it.mime,
			Supply: it.supply, Decimals: 0, Collection: collectionID.Bytes(),
		}
		hash, err := in.InscribeFile(ctx, meta, it.body, protocol.PlasmaFused, me)
		if err != nil {
			return fmt.Errorf("%s: %w", it.name, err)
		}
		fmt.Printf("  %-11s %s  %-28s %6d B\n", "asset", hash, it.name, len(it.body))
	}

	if !*skipBig {
		big := makePNG(360, 360)
		// The description says what the table cannot: size and part count are
		// already columns on the asset page, so restating them there is the
		// one thing a description should never spend itself on.
		meta := protocol.MintWire{
			Name:        "mural.png",
			Description: "The multi-part path: one image, many blocks, reassembled byte-exact.",
			Mime:        "image/png", Supply: 1, Collection: collectionID.Bytes(),
		}
		plan, err := protocol.BuildPlan(meta, big, protocol.PlasmaFused)
		if err != nil {
			return err
		}
		fmt.Printf("\n  multi-part: %d bytes across %d blocks (tail first, head last)\n", len(big), len(plan.Parts))
		hash, err := in.InscribeFile(ctx, meta, big, protocol.PlasmaFused, me)
		if err != nil {
			return fmt.Errorf("mural.png: %w", err)
		}
		fmt.Printf("  %-11s %s  %-28s %6d B\n", "asset", hash, "mural.png", len(big))
	}

	fmt.Printf("\ndone — open the viewer and wait for the indexer to catch up\n")
	return nil
}

// makePNG draws a deterministic gradient with some noise, so the bytes do not
// compress to nothing and a large canvas actually needs several blocks.
func makePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(w * h)))
	cx, cy := float64(w)/2, float64(h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := math.Sqrt(dx*dx+dy*dy) / math.Sqrt(cx*cx+cy*cy)
			ring := 0.5 + 0.5*math.Sin(d*18)
			img.Set(x, y, color.RGBA{
				R: clamp(20 + ring*40 + float64(rng.Intn(12))),
				G: clamp(200*(1-d) + ring*60 + float64(rng.Intn(12))),
				B: clamp(150*(1-d*0.5) + ring*30 + float64(rng.Intn(12))),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func clamp(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// verify that the demo address type stays in sync with the CLI helper
var _ = func() types.Address { return types.Address{} }

var _ = inscribe.HexHash

const demoMarkdown = `# Zenoglyphs

A metaprotocol for the **Network of Momentum**. No consensus change, no new
embedded contract, no new node requirement — just ordinary 0-amount sends
carrying a CBOR payload that pillars already relay and already price.

## What this document is

This file is itself an inscription. The bytes you are reading were split into
CBOR byte strings, written into account-block data fields, ordered by the
momentum they landed in, and reassembled by an indexer that anyone can run.

## The three ideas worth keeping

1. **The event log is the truth.** Projections — balances, ownership, index
   numbers — are derived and can be dropped and recomputed at any time.
2. **Canonical order comes for free.** ` + "`momentum.content`" + ` is sorted by
   (address, height, hash) and committed to by the momentum hash, so every
   indexer applies events in the same sequence without a heuristic.
3. **State roots make divergence visible.** Two indexers that agree on
   ` + "`stateRoot(H)`" + ` agree on all of history to H; when they do not, a
   binary search finds the exact momentum where they parted.

> A frozen spec invites review of the design. A running indexer with no spec
> invites review of the author's motives.

## Cost

| Tier  | Max block data | Pays with |
|-------|---------------:|-----------|
| PoW   |        1,080 B | work      |
| Fused |       16,384 B | ~541 QSR  |

The base layer already tiers the cost of data, which is why there is no
protocol fee.
`

const demoText = `zenoglyphs — plain text inscription

This is the simplest thing the protocol can carry: a byte string with a
declared content type, stored in one account block, served back with the
type it was inscribed under.

Nothing here is base64. The old encoding wrapped binary in a data: URI,
JSON-quoted it, and then wrapped that in CBOR — which cost a third of every
block to say the same thing. A CBOR byte string says it directly.

  block limit         16,384 bytes
  proof-of-work cap    1,080 bytes
  max parts per asset         64
`

const demoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 300" width="300" height="300">
  <rect width="300" height="300" fill="#0b0f0e"/>
  <g fill="none" stroke="#35d6a4" stroke-width="1.5">
    <circle cx="150" cy="150" r="110" opacity="0.25"/>
    <circle cx="150" cy="150" r="82"  opacity="0.4"/>
    <circle cx="150" cy="150" r="54"  opacity="0.6"/>
    <rect x="108" y="108" width="84" height="84" transform="rotate(45 150 150)"/>
  </g>
  <text x="150" y="252" fill="#7d8b88" font-family="monospace" font-size="11"
        text-anchor="middle" letter-spacing="2">ZENOGLYPHS</text>
</svg>
`

const demoHTML = `<!doctype html>
<meta charset="utf-8">
<title>inscribed page</title>
<style>
  body { margin:0; height:100vh; display:grid; place-items:center;
         background:#0b0f0e; color:#d9e2e0;
         font:14px/1.6 ui-monospace, monospace; }
  .card { border:1px solid #1d2725; border-radius:8px; padding:28px 34px; max-width:380px; }
  h1 { margin:0 0 10px; font-size:15px; color:#35d6a4; letter-spacing:.06em; }
  p { margin:0 0 10px; color:#7d8b88; }
  code { color:#d9e2e0; }
</style>
<div class="card">
  <h1>INSCRIBED HTML</h1>
  <p>This page lives entirely in account-block data on the Network of Momentum.</p>
  <p>It is served from a separate origin under
     <code>Content-Security-Policy: sandbox</code>, and rendered inside an
     iframe with no <code>allow-scripts</code>. Whatever it tries to do, it
     does it alone.</p>
</div>
`

const demoJSON = `{
  "protocol": "zenoglyphs",
  "version": 1,
  "prefix": "zsc",
  "opcodes": {
    "0": "mint",
    "1": "transfer",
    "2": "multi",
    "3": "createCollection",
    "4": "createDispenser",
    "5": "cancelDispenser",
    "6": "dispenserPurchase"
  },
  "encoding": {
    "container": "CBOR, RFC 8949 section 4.2 deterministic",
    "binary": "byte strings, never base64",
    "hashes": "32-byte byte strings, never hex text"
  },
  "ordering": "(momentum height ASC, index within momentum.content ASC)"
}
`
