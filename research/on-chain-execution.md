# Executing code stored on chain

**Status:** research, not normative. Nothing here is a spec rule or an
assumption of record; A-numbers and R-numbers reference the existing ones.
**Opened:** 2026-09-09. **Context:** Zenoglyphs / `zcribe` on NoM.

Every number in this document was measured on the devnet on the day it was
opened, not recalled. Where something is inferred rather than measured, it says
so.

---

## 1. The distinction that decides everything

"Executing code stored on chain" names two different problems, and almost every
disagreement about it comes from conflating them.

| | **render-side** | **consensus-side** |
|---|---|---|
| Who runs the code | the viewer's browser | every indexer |
| Must all runners agree? | no | **yes, bit for bit** |
| Failure mode | one tab renders badly | indexers fork |
| Nondeterminism | harmless | fatal |
| Resource abuse | hurts one viewer | hurts the network |
| Status in Zenoglyphs | **shipped** (R-8.5) | **does not exist, by design** |

Zenoglyphs is render-side, and the architecture is explicit about it: an indexer
records that an inscription *declares* imports and never fetches or executes
anything (A-33 — "a ref is a claim, not a dependency"). The state root covers
the event log, not the output of any program.

This is the same trust model as Arweave's SmartWeave, where contracts are stored
on chain and evaluated by whoever reads them, and the opposite of the EVM,
CosmWasm or Solana SBF, where execution *is* consensus and every validator must
reproduce it exactly.

**The rest of this document is mostly about render-side execution, because that
is what exists.** §6.3 treats consensus-side separately, because the constraints
invert and most of the interesting risks live there.

---

## 2. What is already true

Measured 2026-09-09, devnet chain 69.

**WebAssembly runs today**, once two things are in place — one serving-layer,
one consensus:

- `application/wasm` in the frozen MIME allowlist (`internal/protocol/mime.go`).
- `'wasm-unsafe-eval'` in the content-origin CSP (`internal/api/api.go`).
  Without it, `WebAssembly.validate()` still returns **true** — it is only a
  parse — and `instantiate()` throws a `CompileError`. That asymmetry is a trap:
  a naive capability check reports success while the thing is still dead.

The narrow token is the right one. Verified in Chrome against the exact policy:

```
wasm instantiate : ALLOWED
js eval()        : BLOCKED  EvalError
new Function()   : BLOCKED  EvalError
offsite fetch    : BLOCKED  (connect-src pin holds)
```

`'unsafe-eval'` would also work, and would additionally re-open `eval` and
`new Function`, for no gain.

**Cross-module linking works, and is the interesting part.** A deployed PoC
inscribes a kernel and a stateful engine as separate `application/wasm` assets;
the engine imports the kernel's export, and the page holds no state and does no
arithmetic:

```
page   wasm-live.html                4,944 B   text/html
 |- slot 0 -> mandelbrot-kernel.wasm    155 B   iter(cx,cy,maxiter)->i32
 `- slot 1 -> mandelbrot-engine.wasm    745 B   viewport, pan, zoom, framebuffer
                `- slot 0 -> the same kernel
```

900 bytes of WebAssembly driving an interactive fractal explorer to 1972x zoom.
The engine exports `init down move up zoom setIter reset render probe cx cy
zoomLevel`; all interaction state lives in wasm globals.

---

## 3. Size, and why it is the whole story

| artefact | bytes |
|---|---|
| valid empty module | **8** |
| `add(i32,i32)->i32` | **41** |
| loop + memory, `sum(n)` | **87** |
| Mandelbrot kernel | **155** |
| stateful engine, 12 exports | **745** |
| Go `println("hi")`, `GOOS=js GOARCH=wasm` | **1,894,024** |
| the same, `-ldflags="-s -w"` | 1,859,151 |

Against the ceilings:

```
fused  block payload 16,384 B   max asset 1,044,422 B (1020 KB) / 64 parts
pow    block payload  1,080 B   max asset    64,966 B (63 KB)   / 64 parts
composite: 16 refs, depth 8  ->  ~15.94 MB reachable from one page
```

**Go cannot be the toolchain.** 1.8 MB for a `println` exceeds the 1,020 KB
single-asset ceiling outright, before any of this project's own dependencies —
and `common/types` alone costs ~15 MB in a `js/wasm` build, because
`common/errors.go` transitively links a JSON-RPC server. Anything shipping a GC
and a runtime (Go, .NET, full CPython) is disqualified by arithmetic, not by
preference.

What fits comfortably: hand-written wasm, Rust `no_std` with `wasm-opt -Oz`,
Zig `ReleaseSmall`, AssemblyScript, C via clang. All land in single-digit KB for
real work.

**PoW-only inscribers cap at 63 KB per asset**, so wasm of any substance is
effectively a fused-plasma feature.

---

## 4. The costs nobody mentions until they hit them

Two separate limits bind on a large payload, and both were measured.

**Plasma is a capital requirement, not a fee.** A full 16,384-byte block was
charged `basePlasma = fusedPlasma = 1,135,112`, `difficulty = 0`. A well-fused
account (20,200 QSR, `maxPlasma` 42,420,000) therefore affords **37
maximum-size blocks from a full tank**. A 64-part 1 MB asset needs ~72.6 M
plasma and *cannot* be published without waiting for regeneration, however much
QSR is fused — `maxPlasma` caps the tank, not the throughput.

**Multi-part publishing is latency-bound, structurally.** Parts are chained by
`pHash`, so `internal/inscribe/inscribe.go` waits for each part to confirm
before building the next: "the next part names this one, so this one has to be
on chain first." Measured: **~30 s per link**, 14 parts in 6 minutes.
Extrapolated, a 64-part asset is **~30 minutes**.

By contrast, six *independent* assets published **13 ms apart**, because nothing
referenced anything.

> **Design consequence.** To ship a large runtime, split it across **refs**, not
> across **parts**. Sixteen independent 64 KB assets cost sixteen unchained
> sends; one 1 MB asset costs a 64-link confirmation chain. The composite
> mechanism is the cheap axis and multi-part is the expensive one — the opposite
> of the intuition that "one asset is simpler".
>
> *Unverified optimisation:* the wait may be avoidable. A block's hash is known
> at construction, so a client could build part *n+1* before part *n* confirms.
> Whether the node accepts a chain of unconfirmed account blocks inside one
> momentum was not tested.

---

## 5. Four execution models

### 5.1 Interpreted, in the host's JavaScript — *status quo*

Inscribe JS or HTML; the browser runs it. Zero protocol change, works since
R-8.5.

- **For:** no new machinery; human-readable on chain; smallest possible
  programs; `text/javascript` already allowlisted.
- **Against:** performance is the host's problem; the source *is* the artefact,
  so minification is a permanent readability loss; `'unsafe-inline'` is already
  required and cannot be tightened while self-contained inscriptions exist.

### 5.2 Compiled to WebAssembly — *demonstrated in §2*

- **For:** 10-100x smaller than equivalent JS for numeric work (155 B for a
  Mandelbrot kernel); near-native speed; a real import/export contract declared
  in the module's own type section, which makes `refs` a *typed* dependency
  rather than a convention; language-agnostic.
- **Against:** opaque on chain — the bytes are not readable, which sits awkwardly
  beside "the artwork is on the chain"; auditing needs a toolchain;
  source-to-artefact reproducibility is a build-determinism problem this project
  does not currently solve; needs the CSP grant.

### 5.3 Microkernel — an interpreter inscribed once, programs as data

Inscribe a small language runtime **once**; every program is then a tiny
`text/plain` inscription that `refs` it.

This is the p5.js pattern (A-32) with the economics inverted in its favour:

```
runtime   ~100-300 KB   inscribed once, amortised across every program
program        ~1 KB    human-readable, auditable, diffable on chain
```

- **For:** best cost-per-program by two orders of magnitude; programs stay
  readable on chain, which is exactly what §5.2 gives up; one audited runtime
  instead of N opaque binaries; a runtime can be *sandboxed by construction*,
  since it decides what its programs may do — a hostile program is bounded by an
  interpreter you reviewed rather than by the browser alone; upgrades are a new
  runtime inscription, and old programs keep pointing at the old one, which is
  immutability working *for* you.
- **Against:** interpretation is 10-100x slower than compiled wasm; the runtime
  is a single point of failure for everything referencing it; you inherit the
  language's semantics permanently, since a fix is a new inscription with a new
  id; one of the 16 ref slots and one level of the depth-8 budget go to the
  runtime link.
- **Candidates worth measuring:** a Forth or Lisp in wasm (single-digit KB); a
  small Scheme; `wasm3` compiled to wasm (~100 KB, giving wasm-in-wasm and
  therefore metering — see §6.3); QuickJS via emscripten (~250 KB, real
  JavaScript, probably the highest value-per-byte for general programs).

**This is the model most worth prototyping next.** It is the only one that gets
small programs, readable programs, *and* a place to enforce limits.

### 5.4 Native code — not possible, and worth saying once

There is no path. The browser will not execute inscribed machine code, and the
indexer must never try. Any proposal in this direction is a proposal to make
every indexer operator run untrusted binaries.

---

## 6. Risks

### 6.1 Resource exhaustion is the live one

A wasm module can loop forever, and **nothing outside it can interrupt it**.
There is no preemption, no timeout, no `AbortController` for a running
instantiation. Measured on the deployed PoC: 48 ms/frame at 255 iterations,
147 ms/frame at 500 on a deep zoom. A hostile inscription scales that to seconds,
or to never.

What contains it today: the sandbox is per-iframe with an opaque origin, so a
hang costs *that frame*, not the viewer. What does not contain it: everything
else. There is no CPU or memory budget, and `memory.grow` toward 4 GiB is a
per-instance decision.

Mitigations, cheapest first: keep rendering inscriptions in an `<iframe>` the
viewer can drop (already true); move instantiation into a Worker that can be
`terminate()`d; cap declared `memory` at render time and refuse to *render* —
never to index — modules above it; a per-asset render timeout with a visible
"this inscription did not finish" state.

**This risk exists today for JavaScript inscriptions and is not new with wasm.**
Wasm makes it faster to reach, not newly possible.

### 6.2 Opacity and moderation

Moderation (A-6 / R-8.4) works on whole assets: metadata hidden, bytes withheld.
That still works for wasm. What changes is *review*: an operator deciding
whether to restrict an asset can read inscribed HTML and cannot read a wasm
binary. The admin panel deliberately refuses to render inscribed bytes on the
API origin (`internal/api/admin.go`), so review happens through the ordinary
viewer — which for a binary shows nothing useful.

This is a second argument for §5.3: readable programs are moderatable programs.

### 6.3 Determinism — dormant here, decisive if the model ever changes

Render-side execution does not need determinism and does not have it:
`Math.random`, `Date.now`, `performance.now`, canvas readback and device pixel
ratio all vary per viewer, and the deployed PoC itself uses `performance.now`.

If consensus-side execution were ever proposed, this inverts completely — and
wasm is *nearly* but not fully deterministic. The known sources:

- **NaN payload bits** are unspecified. CosmWasm's response is to **ban
  floating-point instructions outright** in contracts. The PoC in §2 is built on
  `f64` throughout: perfect for rendering, disqualifying for consensus.
- **`memory.grow` failure** depends on host memory pressure.
- **SIMD**, threads and atomics add more.
- **No metering.** Wasm has no gas. Metering means instrumenting the module
  (Wasmtime/wasmi "fuel") or interpreting it — which is where §5.3's
  wasm-in-wasm option becomes interesting, because an interpreter you control
  can count.

Consensus-side execution would also mean every indexer runs attacker-supplied
code, turning §6.1 from a per-viewer nuisance into a network-wide DoS, and would
put a wasm engine's CVE surface inside the indexer. **The current architecture
avoids all of this for free, and that is worth defending explicitly rather than
losing by accident.**

### 6.4 Supply chain, inverted

The CSP pins every fetch to the content origin, so an inscription composes
itself out of other inscriptions **and nothing else** — no CDN, no
author-controlled URL repointable after the mint (A-33). A referenced runtime is
named by block hash, so it cannot change underneath you.

The flip side: it also cannot be *patched*. A bug in a widely-referenced runtime
is permanent for every inscription pointing at it. Immutability is the security
property and the maintenance hazard, and they are the same fact.

### 6.5 Version pinning is already solved, quietly

`refs` name block hashes, and A-32 proves the graph is a DAG by construction: a
hash cannot be known before the block exists, so every edge points strictly
backwards. There is no dependency resolution, no version ranges, no lockfile,
and no possibility of a cycle. For a package ecosystem that is an unusually
strong starting position, and it came free.

---

## 7. Where this leaves things

**Settled by measurement:**

1. Wasm runs render-side; it needs `'wasm-unsafe-eval'` plus the MIME entry, and
   nothing more.
2. Cross-module linking works and maps onto `refs` cleanly — 900 B for a working
   interactive application.
3. Go is out. Rust / Zig / AssemblyScript / hand-written are in.
4. Refs are the cheap axis for large payloads; multi-part is the expensive one
   (~30 min for 1 MB).

**Worth doing next, in order:**

1. **Prototype §5.3.** Inscribe QuickJS or a small Forth and measure real
   cost-per-program against §5.2. Highest-information experiment left, and it
   needs no protocol change.
2. **Bound rendering, not indexing.** A Worker-based render host with
   terminate-on-timeout addresses §6.1 for JS and wasm at once, entirely at the
   serving layer.
3. **Decide §6.3 deliberately.** Either write down that consensus-side execution
   is out of scope, or accept that floats, metering and engine CVEs all become
   protocol problems. Leaving it undecided is the only bad option.

**Open questions:**

- Can chained parts skip `WaitForConfirmation` (§4)? Worth half an hour to test;
  worth ~30x on large-asset publishing if it holds.
- Should `refs` carry an expected content hash *of the import's meaning* rather
  than only its id? The id already binds bytes, so probably not — recorded so it
  is not re-derived.
- Is there value in a `class` for wasm distinct from `binary`, so a viewer can
  offer "inspect module exports" without rendering? Cheap, non-consensus, and it
  would partly answer §6.2.
