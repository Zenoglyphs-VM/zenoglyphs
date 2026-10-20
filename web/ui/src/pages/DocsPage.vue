<script setup lang="ts">
import {computed, nextTick, onMounted, watch} from 'vue'
import {Badge, Heading, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import {useRouter} from 'vue-router'
import PageHeading from '@/components/PageHeading.vue'
import DocsSection from '@/components/DocsSection.vue'
import {baseMime, bytes} from '@/core/format'
import {useIndexer} from '@/core/composables/useIndexer'

// The documentation the viewer used to scatter across its own chrome, plus
// what someone needs to run an indexer of their own.
//
// Two rules keep this page honest:
//
//   - Anything the indexer already knows is READ FROM /info, never retyped.
//     The allowlist, the block ceiling and the import cap below are this
//     deployment's actual numbers, so a doc page cannot drift from the binary
//     serving it — which is the usual failure mode of a docs page in a repo.
//   - Anything that is prose stays here rather than on a detail view. A
//     sentence explaining how composite slots resolve is worth reading once;
//     printed above every composite, it is furniture.
const {info} = useIndexer()
const router = useRouter()

const props = defineProps<{section?: string}>()

const sections = [
  {id: 'overview', label: 'Overview'},
  {id: 'wire', label: 'How it is carried'},
  {id: 'content', label: 'Content types'},
  {id: 'limits', label: 'Limits'},
  {id: 'composites', label: 'Composites & recursion'},
  {id: 'confirmation', label: 'Confirmation & reorgs'},
  {id: 'origins', label: 'The two origins'},
  {id: 'extensions', label: 'Extensions'},
  {id: 'agreement', label: 'Cross-indexer agreement'},
  {id: 'indexer', label: 'Run your own indexer'},
  {id: 'cli', label: 'CLI'},
  {id: 'api', label: 'API'},
  {id: 'moderation', label: 'Moderation'},
]

// Images and mime types come off /info so the list is this indexer's, not a
// copy of one. Grouped by top-level type because "what can I inscribe" is
// asked by kind, not alphabetically.
const mimeGroups = computed(() => {
  const groups = new Map<string, string[]>()
  for (const m of info.value?.allowedMimes ?? []) {
    const base = baseMime(m)
    const top = base.split('/')[0]
    const list = groups.get(top) ?? []
    list.push(base)
    groups.set(top, list)
  }
  return [...groups.entries()]
})

// Hash routing means an in-page #anchor would collide with the route itself,
// so the section is a route param and the scroll is done here.
//
// Arriving on a link lands instantly; clicking the contents list animates. A
// deep link that scrolls smoothly from the top is the page moving underneath
// someone who has not read anything yet, and it makes where they landed less
// clear, not more.
function scrollTo(id: string, smooth: boolean) {
  document
    .getElementById('doc-' + id)
    ?.scrollIntoView({behavior: smooth ? 'smooth' : 'auto', block: 'start'})
}

let fromClick = false

function go(id: string) {
  fromClick = true
  void router.replace({name: 'docs', params: {section: id}})
}

function reveal() {
  const id = props.section
  if (!id) return
  const smooth = fromClick
  fromClick = false
  void nextTick(() => scrollTo(id, smooth))
}

onMounted(reveal)
watch(() => props.section, reveal)
</script>

<template>
  <div>
    <PageHeading title="Docs" description="The protocol, and how to run an indexer of your own." />

    <div class="grid gap-10 lg:grid-cols-[14rem_minmax(0,1fr)]">
      <nav class="lg:sticky lg:top-20 lg:self-start">
        <p class="text-ledger mb-2 text-muted-foreground">Contents</p>
        <ul class="flex flex-wrap gap-x-3 gap-y-1 lg:flex-col lg:gap-y-0.5">
          <li v-for="s in sections" :key="s.id">
            <button
              type="button"
              class="rounded-md py-1 text-left text-sm transition-colors lg:w-full lg:px-2"
              :class="
                props.section === s.id
                  ? 'bg-accent text-accent-foreground lg:px-2'
                  : 'text-muted-foreground hover:text-foreground'
              "
              @click="go(s.id)"
            >
              {{ s.label }}
            </button>
          </li>
        </ul>
      </nav>

      <div class="min-w-0 space-y-8">
        <DocsSection id="overview" title="Overview">
          <p>
            Zenoglyphs is a metaprotocol for the Zenon Network of Momentum. An inscription is an
            ordinary account block that moves zero funds and carries its payload in the block's
            <code>data</code> field. Pillars already relay and price that field, so nothing about
            the network has to change: no consensus fork, no new embedded contract, no new node
            requirement, and no protocol fee — plasma already prices data.
          </p>
          <p>
            What an inscription <strong>means</strong> is decided entirely off-chain, by indexers
            like this one. Anyone can run one, they are mutually verifiable, and two honest indexers
            reading the same chain compute the same state. That last property is not a hope; it is
            checkable, and
            <a href="#" @click.prevent="go('agreement')">cross-indexer agreement</a> below says how.
          </p>
          <p>
            The load-bearing idea inside an indexer:
            <strong>the event log is the source of truth and projections are disposable.</strong>
            Balances, owners and inscription numbers are a pure function of the log and its
            canonical order, so reorgs, double-spends and disagreements all become things you can
            recompute rather than things you have to reconcile.
          </p>
        </DocsSection>

        <DocsSection id="wire" title="How an inscription is carried">
          <p>
            Every inscription is a canonical-CBOR envelope — a ZDU — in the
            <code>data</code> field of a 0-amount block. It starts with the protocol prefix
            <code>{{ info?.prefix ?? 'glyph' }}</code> and a version, so a block that is not an
            inscription is rejected in the first few bytes rather than parsed hopefully.
          </p>
          <Table class="my-4">
            <TableHeader>
              <TableRow>
                <TableHead class="w-16">op</TableHead>
                <TableHead>name</TableHead>
                <TableHead>does</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell class="font-mono tabular-nums">0</TableCell>
                <TableCell class="font-mono text-xs">mint</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Creates an asset; also the head of a multi-part mint.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono tabular-nums">1</TableCell>
                <TableCell class="font-mono text-xs">transfer</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Moves units. Sending to the burn address destroys them.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono tabular-nums">2</TableCell>
                <TableCell class="font-mono text-xs">multi</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >A non-terminal part of a large inscription; never creates state alone.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono tabular-nums">3</TableCell>
                <TableCell class="font-mono text-xs">createCollection</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Creates a collection that the same issuer's mints may join.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono tabular-nums">4</TableCell>
                <TableCell class="font-mono text-xs">createDispenser</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Escrows units for sale at a fixed price.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono tabular-nums">5</TableCell>
                <TableCell class="font-mono text-xs">cancelDispenser</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Releases the escrow back to the issuer.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono tabular-nums">6</TableCell>
                <TableCell class="font-mono text-xs">dispenserPurchase</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Pays the seller and takes units out of escrow. No partial fills.</TableCell
                >
              </TableRow>
            </TableBody>
          </Table>
          <p>
            Opcode numbers are wire values: they are appended to, never renumbered. An
            <strong>asset id is the hash of the block that minted it</strong>, which is why every
            inscription on this site links straight out to a block explorer — there is nothing extra
            to look up.
          </p>
          <p>
            Content larger than one block is split. The parts are published tail-first and the head
            last, so the mint that creates the asset is also the block that proves every earlier
            part belongs to it; an indexer never has to hold a half-finished asset in a "pending"
            state.
          </p>
        </DocsSection>

        <DocsSection id="content" title="Content types">
          <p>
            The allowlist is closed and part of the rules — not this indexer's preference. An
            inscription whose type is not on it is not an inscription. These
            {{ info?.allowedMimes?.length ?? 0 }} types are what <strong>this</strong> indexer is
            serving:
          </p>
          <div v-for="[top, list] in mimeGroups" :key="top" class="mt-4">
            <p class="text-ledger mb-1.5 text-muted-foreground">{{ top }}</p>
            <div class="flex flex-wrap gap-1.5">
              <Badge v-for="m in list" :key="m" variant="outline" class="font-mono">{{ m }}</Badge>
            </div>
          </div>
          <p class="mt-5">
            Two of them — <code>text/html</code> and <code>image/svg+xml</code> — execute script.
            That single fact is why the bytes are served from
            <a href="#" @click.prevent="go('origins')">a second origin</a> and rendered in a
            sandboxed frame, and it is the reason the list is closed rather than "anything with a
            MIME type".
          </p>
        </DocsSection>

        <DocsSection id="limits" title="Limits">
          <Table class="my-4">
            <TableHeader>
              <TableRow>
                <TableHead>limit</TableHead>
                <TableHead class="text-right">value</TableHead>
                <TableHead>why</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell class="font-mono text-xs">block data, fused</TableCell>
                <TableCell class="text-right font-mono tabular-nums">
                  {{ bytes(info?.maxBlockData ?? 0) }}
                </TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >The consensus ceiling on an account block's data field.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono text-xs">block data, PoW</TableCell>
                <TableCell class="text-right font-mono tabular-nums">
                  {{ bytes(info?.maxBlockDataPoW ?? 0) }}
                </TableCell>
                <TableCell class="text-sm whitespace-normal">
                  What a PoW-only block can pay for. The binding limit here is plasma, not the
                  ceiling above — fuse plasma to inscribe at full size.
                </TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="font-mono text-xs">parts per asset</TableCell>
                <TableCell class="text-right font-mono tabular-nums">
                  {{ info?.maxAssetParts ?? '—' }}
                </TableCell>
                <TableCell class="text-sm whitespace-normal">
                  About 1 MB of content, bounding what one asset can cost an indexer to reassemble.
                </TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="font-mono text-xs">imports per composite</TableCell>
                <TableCell class="text-right font-mono tabular-nums">
                  {{ info?.maxRefs ?? '—' }}
                </TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Bounds the declared import list of a composite.</TableCell
                >
              </TableRow>
            </TableBody>
          </Table>
          <p>
            CBOR byte strings rather than base64 is worth a line: the same 48 KB image takes four
            blocks instead of five, because the payload is never expanded by a third on its way onto
            the chain.
          </p>
        </DocsSection>

        <DocsSection id="composites" title="Composites and recursion">
          <p>Two mechanisms that get conflated, and both work here.</p>
          <p>
            <strong>Recursion</strong> is an inscribed page loading another inscription's bytes
            while it renders — the shared-library pattern, where one inscribed copy of a drawing
            library serves every piece that uses it. It needs no wire format at all: content already
            has a URL, and every inscription shares one origin.
          </p>
          <p>
            <strong>Composites</strong> are the declaration. A mint may carry a list of up to
            {{ info?.maxRefs ?? 16 }} asset ids in slot order. The content origin then serves slot
            <em>n</em> at the asset's own URL plus <code>n</code> — an asset's canonical URL ends in
            a slash precisely so that it is its own base — and the inscribed bytes reach an import
            with a relative reference:
          </p>
          <pre><code>&lt;script src="0"&gt;&lt;/script&gt;   &lt;!-- the first import  --&gt;
&lt;img src="1"&gt;                &lt;!-- the second import --&gt;</code></pre>
          <p>
            So an author never has to embed a 64-character hash they could not have known at the
            time they wrote the file. Three properties keep composition out of consensus:
          </p>
          <ul>
            <li>
              <strong>A ref is a claim, not a dependency.</strong> The indexer records the edge and
              resolves nothing — it never fetches the target and never requires it to exist.
              Otherwise an indexer that had synced further would accept a mint that one further
              behind would reject, and the two would never agree again.
            </li>
            <li>
              <strong>Cycles are impossible by construction.</strong> A ref names a block hash, and
              a block hash cannot be known before its block exists, so every edge points strictly
              backwards. There is no cycle detection anywhere, only a depth cap on how far a client
              will resolve.
            </li>
            <li>
              <strong>An inscription can load other inscriptions and nothing else.</strong>
              Every fetch directive in the sandbox policy is pinned to the content origin: no CDN,
              no analytics, and no author-controlled URL that could be repointed after the mint.
            </li>
          </ul>
        </DocsSection>

        <DocsSection id="confirmation" title="Confirmation and reorgs">
          <p>
            An inscription reports two confirmation levels, because they answer different questions:
          </p>
          <ul>
            <li>
              <Badge variant="warning">soft</Badge> — this indexer has parsed and applied the block.
              The inscription exists and renders now. It says nothing about surviving a reorg.
            </li>
            <li>
              <Badge variant="success">full</Badge> — {{ info?.confirmationDepth ?? 60 }} momentums
              sit above it. The inscription number is settled and the mint is as final as anything
              else on the chain.
            </li>
          </ul>
          <p>
            A reorg truncates the event log to the last agreeing height and replays. Replay normally
            starts from a checkpoint rather than from genesis — this deployment takes one every
            {{ info?.snapshotInterval ?? 120 }} momentums and keeps enough to cover a
            {{ info?.snapshotRetain ?? 360 }}-momentum reorg. A checkpoint is never authoritative;
            it only moves the base case of the same replay forward, so the state machine is
            untouched and deleting every checkpoint costs replay time and nothing else.
          </p>
          <p>
            Note that the checkpoint window is deeper than the confirmation depth on purpose. Being
            able to unwind further than you promise is the safe direction.
          </p>
        </DocsSection>

        <DocsSection id="origins" title="The two origins">
          <p>
            <code>zcribe serve</code> starts two HTTP servers, and the split is the security model
            rather than a deployment detail:
          </p>
          <Table class="my-4">
            <TableHeader>
              <TableRow>
                <TableHead>origin</TableHead>
                <TableHead>serves</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell class="font-mono text-xs">the API origin</TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Metadata API, this viewer, and the operator's admin panel.</TableCell
                >
              </TableRow>
              <TableRow>
                <TableCell class="font-mono text-xs break-all">
                  {{ info?.contentOrigin ?? 'the content origin' }}
                </TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Inscription bytes and the recursion endpoints. Nothing else.</TableCell
                >
              </TableRow>
            </TableBody>
          </Table>
          <p>
            Because the allowlist includes HTML and SVG, serving inscribed bytes from the API's
            origin would hand attacker-authored script that origin — and with it, anything a
            viewer's browser will do on its behalf. Content is served instead with
            <code>nosniff</code>, the mime type recorded at mint time, and a sandbox policy that
            grants <code>allow-scripts</code> but never <code>allow-same-origin</code>. Withholding
            the second one is what keeps one inscription's script out of another's frame even though
            both come from the same host.
          </p>
          <p>
            On this indexer, inscribed scripts are
            <Badge :variant="info?.scriptsEnabled ? 'success' : 'warning'">
              {{ info?.scriptsEnabled ? 'enabled' : 'blocked' }}
            </Badge>
            — with them off, every inscription is a static picture and recursion does not work. In
            production the two origins must be different hostnames, and preferably different
            registrable domains: a cookie scoped to <code>.example.org</code>
            reaches every subdomain, including the one running someone else's HTML.
          </p>
        </DocsSection>

        <DocsSection id="extensions" title="Extensions and upgrade lanes">
          <p>
            A protocol that can only change by version bump churns. There are three lanes, in
            decreasing order of disruption:
          </p>
          <Table class="my-4">
            <TableHeader>
              <TableRow>
                <TableHead>change</TableHead>
                <TableHead>mechanism</TableHead>
                <TableHead>old indexers</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell class="text-sm whitespace-normal">The ruleset itself</TableCell>
                <TableCell class="font-mono text-xs">bump the version</TableCell>
                <TableCell class="text-sm whitespace-normal">Refuse the whole envelope.</TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="text-sm whitespace-normal"
                  >A field that changes meaning</TableCell
                >
                <TableCell class="font-mono text-xs">a new top-level key</TableCell>
                <TableCell class="text-sm whitespace-normal">Refuse the whole envelope.</TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="text-sm whitespace-normal">A purely additional field</TableCell>
                <TableCell class="font-mono text-xs">a key inside <code>ext</code></TableCell>
                <TableCell class="text-sm whitespace-normal"
                  >Ignore it, and stay correct.</TableCell
                >
              </TableRow>
            </TableBody>
          </Table>
          <p>
            <code>ext</code> is an optional map on the envelope and on every payload, and its
            contents are <strong>consensus-inert</strong>: carried on chain, committed to by the
            block hash, served to clients, never read by the state machine. That is stricter than it
            needs to be on purpose. If an <code>ext</code> key could move a balance, two indexers at
            different versions would agree on the state root while holding different state —
            precisely the blindness the root exists to remove.
          </p>
          <p>
            The result is a strong guarantee: an old indexer and a new one compute identical state
            forever. The newer one just shows its users more. Extension keys an inscription carries
            are displayed verbatim on its page, under the author's own names, whether or not this
            build knows what they mean.
          </p>
        </DocsSection>

        <DocsSection id="agreement" title="Cross-indexer agreement">
          <p>Every indexed height has a state root, chained from the one below it:</p>
          <pre><code>stateRoot(h) = sha3_256( stateRoot(h-1) ‖ uint64_be(h) ‖ orderedAppliedTxHashes(h) )</code></pre>
          <p>
            The root commits to the blocks that actually <strong>changed state</strong>, not to
            every block that parsed. That distinction is what lets it catch a disagreement about the
            rules rather than only one about encoding. The base case is fixed by the deployment's
            start height: <code>stateRoot(start − 1)</code> is 32 zero bytes, which is why every
            operator has to use the same start height or no two indexers will ever agree.
          </p>
          <p>Run a second indexer on its own database and ports, then compare:</p>
          <pre><code>zcribe serve -db second.db -api 127.0.0.1:8720 -content 127.0.0.1:8721
zcribe compare -a http://localhost:8710 -b http://localhost:8720</code></pre>
          <p>
            Agreement is monotone in height, so <code>compare</code> binary-searches the chained
            roots and prints the first divergent momentum along with the blocks each side accepted.
            A disagreement is localised in a handful of queries rather than by reading two logs side
            by side.
          </p>
          <p>
            <code>GET /state</code> adds a digest of the whole projected state. The state root
            proves two indexers agree about <em>which events applied</em>; it structurally cannot
            catch two that agree on that and still compute different balances. The digest can.
            Moderation is deliberately excluded from both, so two honest operators with different
            serving policies still agree on history.
          </p>
        </DocsSection>

        <DocsSection id="indexer" title="Run your own indexer">
          <p>
            Indexers are permissionless and the protocol expects several. Running one is how you
            stop trusting this one.
          </p>

          <Heading :level="3" class="mt-6 mb-2 text-base">Build</Heading>
          <p>
            Go 1.23+, and a checkout of <code>go-zenon</code> as a sibling directory — block
            hashing, PoW and key derivation come from the consensus implementation rather than a
            reimplementation that can drift.
          </p>
          <pre><code>git clone https://github.com/zenon-network/go-zenon
git clone https://github.com/Zenoglyphs-VM/zenoglyphs
cd zenoglyphs
go build -o zcribe ./cmd/zcribe
go test ./...</code></pre>
          <p>
            The viewer is a Vue application that <code>go build</code> embeds from
            <code>web/dist/</code>, which is committed. Rebuild it after changing anything under
            <code>web/ui/</code>:
          </p>
          <pre><code>cd web/ui &amp;&amp; npm install --allow-git=all &amp;&amp; npm run build</code></pre>

          <Heading :level="3" class="mt-6 mb-2 text-base">Against the devnet</Heading>
          <p>
            Bring up the five-node devnet from <code>go-zenon/docker/devnet</code>, then index it
            from genesis:
          </p>
          <pre><code>cd ../go-zenon &amp;&amp; make devnet-up

# back in zenoglyphs
./zcribe serve -start 1 -confirmations 3
./zcribe demo -devnet          # one document of every renderable class</code></pre>
          <p>
            The <code>-devnet</code> flag uses the BIP-39 phrase committed to the devnet compose
            file — public by design and worthless outside chain 69. Account index 1 there is
            pre-fused with enough plasma for full-size blocks.
          </p>

          <Heading :level="3" class="mt-6 mb-2 text-base">On mainnet</Heading>
          <pre><code>zcribe serve \
  -node http://127.0.0.1:35997 \
  -db /var/lib/zcribe/zenoglyphs.db \
  -api 127.0.0.1:8710 \
  -content 127.0.0.1:8711 \
  -content-origin https://your-content-host \
  -start &lt;LAUNCH_MOMENTUM&gt; \
  -confirmations 60 \
  -snapshot-interval 120 \
  -snapshot-retain 360 \
  -scripts</code></pre>
          <p>Three flags decide things you cannot quietly change later:</p>
          <ul>
            <li>
              <code>-start</code> is the launch momentum and part of the consensus base case.
              <strong>Every operator must use the same value</strong>, or no two indexers will ever
              agree on a state root. Publish it next to the software.
            </li>
            <li>
              <code>-scripts</code> decides whether inscribed HTML and SVG execute. On, and an
              inscription can import another as a library. Off, and every inscription is a static
              picture.
            </li>
            <li>
              <code>-content-origin</code> is the URL browsers actually reach for bytes. The sandbox
              policy is built from it, so a value that does not match reality either breaks
              rendering or weakens the isolation.
            </li>
          </ul>
          <p>
            Both HTTP servers bind to localhost; a reverse proxy faces the world. Two headers must
            survive that hop untouched: <code>Content-Security-Policy</code>, which the indexer
            builds per response and <em>is</em> the sandbox, and <code>Content-Type</code>, served
            exactly as recorded at mint time with <code>nosniff</code> beside it. A proxy or CDN
            that rewrites, merges or "optimises" either one has broken the security model.
          </p>
          <p>
            Set <code>-explorer</code> to choose where this viewer links hashes:
            <code>auto</code> picks by the node's chain id, and <code>zenonhub</code>,
            <code>devnet</code>, <code>none</code> or a URL template containing
            <code>{v}</code> override it. The repository's <code>deploy/</code> directory has
            complete Caddy and nginx configs, systemd units, and install and health-check scripts.
          </p>
        </DocsSection>

        <DocsSection id="cli" title="CLI">
          <pre><code>zcribe serve       run the indexer, API, content origin, and viewer
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
zcribe demo        inscribe a sample set of documents</code></pre>
          <pre><code>zcribe inscribe -devnet -file art.png -name "Study #1" -supply 100
zcribe inscribe -devnet -file doc.md -ext '{"license":"CC0-1.0"}'
zcribe transfer -devnet -asset &lt;id&gt; -amount 25 -to z1qp3y...
zcribe transfer -devnet -asset &lt;id&gt; -amount 5 -burn
zcribe dispenser create -devnet -asset &lt;id&gt; -amount 20 -price 100000000

# a composite: inscribe the library, then declare it as import slot 0
zcribe inscribe -devnet -file zenlib.js -name zenlib
zcribe inscribe -devnet -file page.html -refs &lt;zenlib-id&gt;</code></pre>
          <p>
            A key never comes from a command-line argument — arguments land in shell history and in
            the process table. Use <code>-devnet</code>, <code>-mnemonic-file</code>,
            <code>-mnemonic-env</code>, or <code>-keystore</code> with <code>ZCRIBE_PASSWORD</code>.
          </p>
        </DocsSection>

        <DocsSection id="api" title="API">
          <p>
            Everything this viewer shows comes from these endpoints, unauthenticated and CORS-open,
            so anything else can read them too.
          </p>
          <pre><code>GET /info                    protocol + indexer version, sync height, state root, allowlist
GET /assets?owner=&amp;issuer=&amp;collection=&amp;mime=&amp;limit=&amp;offset=
GET /asset/{id|number}
GET /asset/{id}/holders      one row per holder; escrow is an address, not a hash
GET /asset/{id}/transfers    mint, transfers, escrow legs and sales, newest first
GET /asset/{id}/refs         composite imports, and what imports this
GET /address/{addr}          every asset an address holds, and how much
GET /collections  ·  /collection/{id}
GET /dispensers   ·  /dispenser/{id}
GET /momentum/{h}            {height, hash, txs[], stateRoot}
GET /stateroot/{h}
GET /state                   projection digest — what the state root cannot see</code></pre>
          <p>And, on the content origin:</p>
          <pre><code>GET /content/{id}/           raw bytes, sandboxed, with the inscribed mime type
GET /content/{id}/{n}        composite import slot n
GET /r/momentum              the chain tip this indexer has reached
GET /r/asset/{id}            one asset's metadata
GET /r/refs/{id}             the import graph, both directions
GET /r/children/{id}         the ids in a collection</code></pre>
          <p>
            The <code>/r/</code> endpoints live on the content origin because a relative reference
            is the only kind an inscription can carry without hard-coding where an indexer happens
            to be deployed.
          </p>
          <p>
            <code>/transfers</code> is an asset's whole history, not only the rows the indexer
            wrote. Three movements never produce a transfer row — the mint itself, stock going into
            a dispenser's escrow, and stock coming back out when the dispenser is cancelled — so
            they are reconstructed when the feed is served and flagged <code>synthetic</code>. Every
            row carries a <code>kind</code>:
            <code>mint · transfer · burn · escrow · release · sale</code>, and every row still cites
            the block that caused it.
          </p>
          <p>
            A dispenser's escrow appears as an address in the protocol's own namespace —
            <code>z1zen0glyphxescr0w…</code>, one per dispenser — rather than as the block hash it is
            keyed by internally. The dispenser id travels beside it, because the address is a
            display form and cannot be turned back into an id by anyone holding it.
          </p>
        </DocsSection>

        <DocsSection id="moderation" title="Moderation">
          <p>
            An indexer chooses what it serves; it does not choose what happened. Moderation is a
            per-indexer serving policy and is deliberately excluded from the state root, so two
            operators with different policies still agree on history and can still check each other.
          </p>
          <p>Two levels, and the difference matters:</p>
          <ul>
            <li>
              <strong>Withholding data</strong> keeps the inscription in the index — number, owner,
              transfers, all still there and still counted — while refusing to serve its bytes. The
              record stays auditable.
            </li>
            <li>
              <strong>Withholding metadata</strong> removes it from listings on this indexer
              entirely. Another indexer still has it.
            </li>
          </ul>
          <p>
            Both are reachable from <code>zcribe moderate</code> and, when an operator has set a
            password, from the admin panel — which also purges the CDN in front of the content
            origin, because bytes already cached are the part that policy alone does not reach.
          </p>
        </DocsSection>
      </div>
    </div>
  </div>
</template>
