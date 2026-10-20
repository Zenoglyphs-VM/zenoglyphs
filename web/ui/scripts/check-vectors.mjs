// Does the browser encoder produce the same bytes as the Go one?
//
// It has to, exactly. An inscription's asset id is sha3 over the envelope, and
// the indexer's decoder rejects anything its own encoder could not have
// written, so a one-byte difference is not a slightly-wrong inscription — it is
// not an inscription, and the plasma is spent discovering that.
//
// So this checks src/wallet against two things that are not each other:
//
//   1. testdata/vectors/v1.json — the protocol's own golden vectors, which the
//      Go implementation is bound to by `zcribe vectors -check`. Agreeing with
//      them is agreeing with the spec.
//   2. A real EVM call taken off the devnet chain, byte for byte. The vectors
//      were written by hand; this one was produced by the Go inscriber, signed,
//      published, and accepted by a node — so it closes the gap between "the
//      encoder is right" and "the thing we actually build is right".
//
// Run with `npm run verify:vectors`. It uses esbuild, which vite already
// depends on, so it adds no package to install.

/* eslint-disable no-console -- this is a command-line checker; its output IS console. */
import {readFileSync} from 'node:fs'
import {dirname, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import {build} from 'esbuild'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '..')
const repo = resolve(root, '../..')

/** Bundles the wallet encoder to a data URL so Node can import the TypeScript. */
async function loadWallet() {
  const out = await build({
    entryPoints: [resolve(root, 'src/wallet/envelope.ts')],
    bundle: true,
    format: 'esm',
    platform: 'neutral',
    write: false,
    logLevel: 'silent',
  })
  const js = out.outputFiles[0].text
  return import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`)
}

const hex = (b) => Buffer.from(b).toString('hex')
const unhex = (s) => new Uint8Array(Buffer.from(s, 'hex'))

let failures = 0
function check(name, got, want) {
  if (got === want) {
    console.log(`  ok    ${name}`)
    return
  }
  failures++
  console.log(`  FAIL  ${name}`)
  console.log(`        got  ${got}`)
  console.log(`        want ${want}`)
  // Naming the first differing byte turns "these two long strings differ" into
  // a field, which is the difference between a five-second fix and an hour.
  for (let i = 0; i < Math.max(got.length, want.length); i += 2) {
    if (got.slice(i, i + 2) !== want.slice(i, i + 2)) {
      console.log(`        first difference at byte ${i / 2}`)
      break
    }
  }
}

const wallet = await loadWallet()
const {encodeZDU, buildEvmInscription} = wallet
const {encode, RawCbor} = await (async () => {
  const out = await build({
    entryPoints: [resolve(root, 'src/wallet/cbor.ts')],
    bundle: true,
    format: 'esm',
    platform: 'neutral',
    write: false,
    logLevel: 'silent',
  })
  return import(
    `data:text/javascript;base64,${Buffer.from(out.outputFiles[0].text).toString('base64')}`
  )
})()

// --- 1. the protocol's golden vectors ---

const vectors = JSON.parse(
  readFileSync(resolve(repo, 'testdata/vectors/v1.json'), 'utf8'),
)

console.log(`protocol vectors (${vectors.prefix}):`)

// Which of the object's fields are byte strings rather than text. Everything
// else in a mint payload is a string or an integer.
const BYTE_FIELDS = new Set(['data', 'collection'])
// Fields that are already-encoded CBOR and must be spliced in verbatim.
const RAW_FIELDS = new Set(['attributes', 'ext'])

for (const v of vectors.vectors) {
  // Only the mint vectors exercise this encoder; the other opcodes are never
  // built in a browser, and pretending to cover them would be a test of
  // nothing.
  if (v.opcodeName !== 'mint') continue
  // The composite vector's refs are not in its `object` (they live in the
  // encoded form only), so it cannot be reconstructed from the JSON alone.
  if (v.name === 'mint-composite') {
    console.log(`  skip  ${v.name} (refs are not represented in the fixture's object)`)
    continue
  }

  const payload = {}
  for (const [k, val] of Object.entries(v.object)) {
    if (BYTE_FIELDS.has(k)) payload[k] = unhex(val)
    else if (RAW_FIELDS.has(k)) payload[k] = new RawCbor(unhex(val))
    else payload[k] = val
  }

  const envelope = {p: vectors.prefix, v: 1, op: v.opcode, data: payload}
  if (v.pHash) envelope.pHash = unhex(v.pHash)
  if (v.tHash) envelope.tHash = unhex(v.tHash)
  // mint-with-extensions carries ext at the envelope level too.
  if (v.object.ext && v.cbor.startsWith('a5')) envelope.ext = new RawCbor(unhex(v.object.ext))

  check(v.name, hex(encode(envelope)), v.cbor)
}

// encodeZDU is what the page actually calls, so it gets its own pass rather
// than being trusted because encode() agreed.
{
  const v = vectors.vectors.find((x) => x.name === 'mint-text-standalone')
  const payload = {...v.object, data: unhex(v.object.data)}
  check('encodeZDU/mint-text-standalone', hex(encodeZDU(v.opcode, payload)), v.cbor)
}

// --- 2. a real inscription, off the chain ---

console.log('\nlive inscriptions:')

// devnet momentum 53641, asset 283196567e9a…, an ERC-20 transfer published by
// `zcribe evm call`. Recorded here as the bytes the node accepted.
const LIVE = {
  name: 'evm call (devnet 283196567e9a…)',
  to: '0x166823c0dd7c67c19da24218fd114254bed0c114',
  calldata:
    'a9059cbb000000000000000000000000008cae01441c732738573a0d332ec4aede77e0a6' +
    '0000000000000000000000000000000000000000000000878678326eac900000',
  data:
    'a4617065676c797068617601626f70006464617461a663657874a16365766da2626f70646' +
    '3616c6c62746f782a30783136363832336330646437633637633139646132343231386664' +
    '3131343235346265643063313134646461746158' +
    '44a9059cbb000000000000000000000000008cae01441c732738573a0d332ec4aede77e0a' +
    '60000000000000000000000000000000000000000000000878678326eac900000646d696d' +
    '656f6170706c69636174696f6e2f65766d646e616d656863616c6c2e65766d66737570706' +
    'c790168646563696d616c7300',
}

const built = buildEvmInscription({op: 'call', to: LIVE.to}, unhex(LIVE.calldata))
check(LIVE.name, hex(built.data), LIVE.data)

// --- 3. the ABI and address layers ---
//
// These are what the write panel builds calldata with, so they get the same
// treatment: fixed expected values, not a round trip against themselves.

console.log('\nabi and addresses:')

const abi = await (async () => {
  const out = await build({
    entryPoints: [resolve(root, 'src/wallet/abi.ts')],
    bundle: true,
    format: 'esm',
    platform: 'neutral',
    write: false,
    logLevel: 'silent',
  })
  return import(
    `data:text/javascript;base64,${Buffer.from(out.outputFiles[0].text).toString('base64')}`
  )
})()

// Selectors are keccak-256 with the pre-NIST padding. Getting the padding
// wrong yields a hash that is valid, plausible and wrong for every one of
// these, which is why they are checked against published constants.
for (const [sig, want] of Object.entries({
  'transfer(address,uint256)': 'a9059cbb',
  'balanceOf(address)': '70a08231',
  'approve(address,uint256)': '095ea7b3',
  'transferFrom(address,address,uint256)': '23b872dd',
  'totalSupply()': '18160ddd',
  'decimals()': '313ce567',
})) {
  check(`selector ${sig}`, hex(abi.selector(sig)), want)
}

// A NoM address, both directions. The devnet issuer, whose hex form appears in
// every receipt on that chain.
const ISSUER = 'z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d'
check('address z1 -> bytes', hex(abi.addressBytes(ISSUER)), '0035941e758a1f155fb30c1217a8186ed9e12651')
check('address bytes -> z1', abi.toBech32(abi.addressBytes(ISSUER)), ISSUER)
check(
  'address 0x -> bytes',
  hex(abi.addressBytes('0x0035941e758a1f155fb30c1217a8186ed9e12651')),
  '0035941e758a1f155fb30c1217a8186ed9e12651',
)

// A checksum that does not match must be refused rather than decoded. The
// decoder this replaced sliced the last six characters off and trusted the
// rest, which turns a typo in a transfer recipient into a permanent loss.
{
  const typo = ISSUER.slice(0, 10) + (ISSUER[10] === 'q' ? 'p' : 'q') + ISSUER.slice(11)
  let refused = false
  try {
    abi.addressBytes(typo)
  } catch {
    refused = true
  }
  check('a one-character typo is refused', String(refused), 'true')
}

// The whole path the write panel takes: typed arguments in, calldata out,
// against the bytes the live transfer actually carried.
check(
  'encodeCall/transfer (live calldata)',
  abi.encodeCall('transfer(address,uint256)', [
    abi.parseArg('address', 'z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy'),
    abi.parseArg('uint256', '2500000000000000000000'),
  ]),
  LIVE.calldata,
)

console.log('')
if (failures > 0) {
  console.error(`${failures} check${failures === 1 ? '' : 's'} failed`)
  process.exit(1)
}
console.log('all checks passed')
