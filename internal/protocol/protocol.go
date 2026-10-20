// Package protocol is the frozen Zenoglyphs v1 wire format.
//
// Everything in this package is consensus-relevant: changing a value here
// changes which inscriptions exist. Nothing here may become a config knob.
// The normative description lives in spec/SPEC.md; this file is its
// executable form, and testdata/vectors are the tests that bind the two.
package protocol

import (
	"github.com/zenon-network/go-zenon/common/types"
)

const (
	// Prefix is the protocol namespace carried in every ZDU (spec R-2.1).
	//
	// "glyph". Two earlier builds chose otherwise: "zsc", on the argument that
	// a short opaque tag squats nothing, and then "zen", on the argument that
	// the network's own name is the honest claim for the first metaprotocol on
	// it. Both lost to the same objection from the other side — "zsc" is
	// unreadable to anyone who has not read the spec, and "zen" squats the
	// namespace of the network rather than of this protocol, which is exactly
	// the deference the second argument said was owed.
	//
	// "glyph" names this protocol and nothing else. It reads on sight in a
	// block explorer, and it leaves both the network's name and every short
	// tag free for whatever comes next.
	//
	// It costs two bytes more than "zen" — six CBOR bytes against four. That
	// is 0.2% of a 1080-byte PoW block and nothing at all against the 16 KB
	// fused ceiling; the chunker measures real encodings rather than assuming
	// a fixed overhead, so nothing downstream needs to know.
	//
	// A second metaprotocol is not locked out by this. The prefix only has to
	// be unique, and R-2.1 rejects every ZDU that does not match exactly, so
	// "zts", "nom-x" or anything else remains free.
	Prefix = "glyph"

	// Version is the protocol version this implementation writes and the
	// highest it understands. A ZDU declaring a higher version is ignored,
	// never guessed at (spec R-2.6).
	Version = 1

	// MaxSingleByteCborUint is the largest uint CBOR encodes inline in its
	// initial byte. Opcode and version are both bounded by it so the
	// byte-signature fast path stays a fixed-length comparison (spec R-2.4).
	MaxSingleByteCborUint = 23

	// MaxAssetParts bounds a multi-part mint at ~1 MB of block data. It
	// bounds indexer work per asset and terminates a malicious pHash chain
	// even before cycle detection notices the loop (spec R-7.4).
	MaxAssetParts = 64

	// HashLen is the byte length of a NoM hash, as carried in pHash/tHash.
	HashLen = 32
)

// Wire-object bounds (spec R-4.3). All consensus-relevant: a payload that
// exceeds any of them is invalid, not truncated.
const (
	MaxNameLen        = 128  // bytes, UTF-8
	MaxDescriptionLen = 1024 // bytes, UTF-8
	MaxAttributesLen  = 4096 // bytes, canonical CBOR encoding of the map
	MaxAttributesDep  = 4    // nesting depth of the attributes map
	MaxDecimals       = 18
	MaxMimeLen        = 128

	// MaxExtLen bounds the extension map (spec R-2.9). Without a bound, ext
	// becomes a parallel content channel that bypasses the mime allowlist
	// entirely — arbitrary bytes every indexer must store and serve, under no
	// declared type.
	MaxExtLen = 2048

	// MaxRefs bounds a composite's import list (spec R-4.7). Sixteen 32-byte
	// ids cost 544 bytes of a 1080-byte PoW block, which is the real ceiling:
	// a composite that cannot fit its own imports alongside its content is
	// not a useful object.
	MaxRefs = 16
)

// MaxRefDepth bounds *transitive* resolution — how far a client or an API
// walks a composite's imports, and their imports, before it stops.
//
// This is a work bound, not a safety one. Cycles cannot occur: a ref names a
// block hash, and a block hash cannot be known before the block exists, so
// every edge points strictly backwards in canonical order and the reference
// graph is a DAG by construction (spec R-4.7.3). The plan called for cycle
// detection here; it is not needed, and saying so is worth more than
// implementing it.
const MaxRefDepth = 8

// Protocol-owned addresses live under one namespace: every one of them
// begins z1zen0glyphx, and the rest of the address spells what it is followed
// by x padding.
//
//	z1zen0glyphx <function> xxxxxxxx…  + bech32 checksum
//
// This adapts the convention NoM uses for its own protocol addresses —
// z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp, z1qxemdeddedxt0kenxxxxxxxxxxxxxxxxh9amk0
// — with one deliberate difference, and it is the important one.
//
// A NoM address is 20 bytes, of which the first is a type byte: 0 for a user
// address, 1 for a contract. Those two values pin the leading bech32
// characters, because the first character encodes the type byte's top five
// bits and both values put zeroes there:
//
//	byte0 = 0x00  (user)      z1qq  z1qp  z1qr  z1qz
//	byte0 = 0x01  (contract)  z1qy  z1q9  z1qx  z1q8
//
// So every address NoM can mint begins z1q. This namespace deliberately does
// not: "zen0glyphx" puts 0x16 in the type byte, which is neither.
//
// That is the whole point. types.PubKeyToAddress hardcodes byte0 = 0, so no
// keypair that exists or could ever be generated lands anywhere in this
// namespace. The earlier z1zen0glyphx namespace sat inside user space, where its
// prefix was a label anyone could grind — about 2^25 hashes, minutes — and
// where only the full 32 characters (a 152-bit preimage) were out of reach.
// Here the prefix itself is unreachable, so a lookalike cannot be produced at
// any price and the addresses are unspendable by construction rather than by
// improbability.
//
// What it costs is that nothing in NoM blesses byte0 = 0x16; it works because
// the ledger never asks. The only check on a type byte anywhere in go-zenon is
// types.IsEmbeddedAddress (byte0 == 1), which gates one thing on the receiving
// side: whether a send is pushed to the sequencer for contract execution
// (chain/momentum/ledger_store.go). A contract address that names no contract
// fails there — which is exactly how the first burn sink below died. 0x16 does
// not match that test, so a send to it takes the same path a send to a user
// address takes: marked unreceived, and left there.
//
// Verified on devnet rather than argued: a 1 ZNN send to the sink below
// confirmed in momentum 49671 and sits unreceived, accountHeight 0. Syrius
// accepts it too — its validator is Address.isValid, a bech32 round-trip with
// no type-byte check (syrius/lib/utils/input_validators.dart).
//
// The residual risk is a node release that starts rejecting sends to a type
// byte it does not recognise. That would be a new consensus rule, it would
// break visibly (the send is refused, nothing is silently lost), and the sink
// is the only address here that is ever sent to — escrow addresses are
// rendered, never funded.
const AddressNamespace = "z1zen0glyphx"

// BurnAddress is an unspendable sink. A transfer to it destroys the amount
// rather than crediting a balance (spec R-7.2).
//
// Three addresses were discarded getting here:
//
//	z1qxzenscrypty0nxdurnxxxxxxxxxxxxxxtsa9t   first byte 0x01 — a contract
//	                                           address. The node refuses every
//	                                           send: "contract doesn't exist".
//	z1qqzenscryptdurnxxxxxxxxxxxxxxxxxhuj660   works, but spends 13 characters
//	                                           on a prefix before saying what
//	                                           the address is for.
//	z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9   works and reads well, but its
//	                                           namespace is the network's name,
//	                                           not this protocol's.
//	z1qzen0glyphxdurnxxxxxxxxxxxxxxxxx9yshp2   names the protocol, but sits in
//	                                           user space, where the prefix is
//	                                           only as good as its grind cost.
//
// "durn" rather than "burn" because bech32's charset excludes b, i, o and 1
// (1 is the separator, so it never appears in the data part either).
var BurnAddress = types.ParseAddressPanic("z1zen0glyphxdurnxxxxxxxxxxxxxxxxxx8zwfj9")

// Opcode identifies the operation a ZDU performs. The numeric value is the
// wire value — never renumber, only append (spec R-3).
type Opcode uint64

const (
	OpMint              Opcode = 0 // creates an asset; head of a multi-part mint
	OpTransfer          Opcode = 1 // moves balance
	OpMulti             Opcode = 2 // non-terminal part; never creates state alone
	OpCreateCollection  Opcode = 3 // creates a collection
	OpCreateDispenser   Opcode = 4 // escrows an amount for sale
	OpCancelDispenser   Opcode = 5 // releases escrow to the issuer
	OpDispenserPurchase Opcode = 6 // buys from a dispenser

	opCount = 7
)

var opcodeNames = [opCount]string{
	"mint", "transfer", "multi", "createCollection",
	"createDispenser", "cancelDispenser", "dispenserPurchase",
}

func (o Opcode) String() string {
	if !o.Known() {
		return "unknown"
	}
	return opcodeNames[o]
}

// Known reports whether this version implements the opcode.
//
// The bound matters: opcode arrives from arbitrary on-chain data, so an
// unchecked table index is a remote crash — one block carrying op:99 takes
// the indexer down. Unknown opcodes are ignored, never fatal.
func (o Opcode) Known() bool {
	return o < opCount && uint64(o) <= MaxSingleByteCborUint
}

// NoM account-block limits, mirrored from go-zenon.
//
//	plasma(block) = len(data)*ABByteDataPlasma + AccountBlockBasePlasma
//
// Source: vm/constants/plasma.go, enforced in vm/plasma.go.
const (
	MaxDataLength               = 1024 * 16 // 16384; ErrABDataTooBig above this
	AccountBlockBasePlasma      = 21000
	ABByteDataPlasma            = 68
	MaxPoWPlasmaForAccountBlock = 94500 // 4.5 * base
	PlasmaPerFusionUnit         = 2100  // base / 10

	// MaxDataLengthPoW is the largest payload a PoW-only block can pay for:
	// (94500-21000)/68 = 1080 bytes. PoW inscriptions are small because the
	// ceiling is plasma, not MaxDataLength.
	MaxDataLengthPoW = (MaxPoWPlasmaForAccountBlock - AccountBlockBasePlasma) / ABByteDataPlasma
)

// PlasmaForDataLength is the *nominal* base plasma an account block carrying n
// bytes costs — the quantity go-zenon calls basePlasma.
//
// It is nominal because dynamic plasma no longer charges it directly: a block
// pays ceil(basePlasma x resourcePrice / PriceScaleFactor) of an actual
// resource. basePlasma stays the unit of account, which is exactly why it can
// be recomputed here from the data length alone — no node, no momentum, no
// price. Blocks report a `basePlasma` field on the wire and this is
// deliberately not it: a wire field is a node's claim, and go-zenon itself
// re-derives the value (vm.CanonicalBasePlasma) rather than trusting it.
//
// The inscriber still asks the node what to *pay*, because that needs the
// price and the price moves per momentum.
func PlasmaForDataLength(n int) uint64 {
	return uint64(n)*ABByteDataPlasma + AccountBlockBasePlasma
}

// Dynamic plasma, mirrored from go-zenon dp/dp.go.
//
// Under the dynamic-plasma spork the network prices its two plasma resources —
// fused QSR and proof of work — against a per-momentum target, EIP-1559 style:
// each momentum header carries the next fusion and work prices, and a block
// must pay
//
//	requiredFusedPlasma = ceil(basePlasma x fusionPrice / PriceScaleFactor)
//
// or the PoW equivalent, or a mix of both. Prices float above a floor and never
// below it, so an uncongested network charges exactly basePlasma and every
// number in this file keeps the meaning it had before the spork.
//
// Mirrored rather than imported for the same reason the emission schedule is
// (internal/evm/fee.go): a deriver must be able to reach these conclusions from
// block bytes alone.
const (
	// PriceScaleFactor is the denominator a resource price is quoted in.
	PriceScaleFactor = 1000

	// MinResourcePrice is the floor a price can fall to. It equals
	// PriceScaleFactor, so the minimum multiplier is exactly 1.
	MinResourcePrice = 1000

	// PoWDifficultyPerPlasma converts a block's difficulty into the plasma
	// that work stood in for.
	PoWDifficultyPerPlasma = 1500

	// MaxPoWPlasmaForBlock is dp.MaxPoWPlasmaForAccountBlock — the
	// post-spork ceiling, which is far above the pre-spork 94,500 because
	// dynamic plasma lets work substitute for fusion without limit. It is
	// used only to clamp DifficultyToPlasma; MaxDataLengthPoW above is a
	// client-side packing bound and deliberately keeps the older, smaller
	// figure.
	MaxPoWPlasmaForBlock = 210_000_000_000
)

// DifficultyToPlasma is dp.DifficultyToPlasma: how much plasma a block's proof
// of work paid for. Zero difficulty is a fully fused block and pays none.
func DifficultyToPlasma(difficulty uint64) uint64 {
	if difficulty == 0 {
		return 0
	}
	if difficulty > MaxPoWPlasmaForBlock*PoWDifficultyPerPlasma {
		return MaxPoWPlasmaForBlock
	}
	return difficulty / PoWDifficultyPerPlasma
}

// PlasmaMode is how the inscribing account pays for a block. It determines
// the usable payload size per block, which is a client-side packing concern
// and never a protocol rule — an indexer accepts any block the network did.
type PlasmaMode int

const (
	PlasmaPoW PlasmaMode = iota
	PlasmaFused
)

// MaxBlockData is the payload ceiling for the mode.
func (m PlasmaMode) MaxBlockData() int {
	if m == PlasmaPoW {
		return MaxDataLengthPoW
	}
	return MaxDataLength
}

func (m PlasmaMode) String() string {
	if m == PlasmaPoW {
		return "pow"
	}
	return "fused"
}
