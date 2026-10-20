package indexer

import (
	"bytes"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"testing"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// These tests drive the projection layer directly with a synthetic event log.
// That is where every consensus rule lives, and feeding it events is the same
// thing a replay does — so a rule that holds here holds after a rollback too.

const (
	alice = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
	bob   = "z1qp3yph55qgresyytz83anynr2f4z39x2z3ej3e"
	carol = "z1qpeet8dcjg0m6x6m3tg437wnc42aa2nez2fzth"
)

func newTestIndexer(t *testing.T) (*Indexer, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := DefaultConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, nil, st, log), st
}

// feed appends an event to the log and applies it, exactly as the live
// pipeline does, so tests exercise the same ordering guarantees.
func feed(t *testing.T, ix *Indexer, st *store.Store, e store.Event) {
	t.Helper()
	if err := st.AppendEvent(e); err != nil {
		t.Fatal(err)
	}
	applied, err := ix.Apply(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEventApplied(e.Height, e.MomentumIndex, applied); err != nil {
		t.Fatal(err)
	}
}

type evBuilder struct {
	height uint64
	index  int
}

func (b *evBuilder) next(op protocol.Opcode, sender, recipient string, payload []byte) store.Event {
	e := store.Event{
		Height: b.height, MomentumIndex: b.index,
		BlockHash: blockHash(b.height, b.index),
		Opcode:    uint64(op), Version: protocol.Version,
		Sender: sender, Recipient: recipient, Payload: payload,
		Amount: "0", TokenStandard: types.ZnnTokenStandard.String(),
		Timestamp: int64(1700000000 + b.height),
	}
	b.index++
	return e
}

func (b *evBuilder) newMomentum() {
	b.height++
	b.index = 0
}

func blockHash(height uint64, index int) string {
	return hex.EncodeToString(common.Uint64ToBytes(height*1000 + uint64(index) + 1))[:2] +
		hex.EncodeToString(types.NewHash(append(common.Uint64ToBytes(height), byte(index))).Bytes())[2:]
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := protocol.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mintPayload(t *testing.T, name string, supply uint64, data []byte) []byte {
	return marshal(t, &protocol.MintWire{
		Name: name, Supply: supply, Decimals: 0,
		Mime: "text/plain;charset=utf-8", Data: data,
	})
}

// --- basic mint and transfer ---

func TestMintCreditsOwner(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 10}

	// A mint addressed to bob issues the supply to bob, not to the issuer.
	e := b.next(protocol.OpMint, alice, bob, mintPayload(t, "gift", 100, []byte("hi")))
	feed(t, ix, st, e)

	a, err := st.Asset(e.BlockHash)
	if err != nil || a == nil {
		t.Fatalf("asset not created: %v", err)
	}
	if a.Issuer != alice || a.Owner != bob {
		t.Fatalf("issuer/owner: got %s/%s", a.Issuer, a.Owner)
	}
	if bal, _ := st.Balance(a.ID, bob); bal != 100 {
		t.Fatalf("owner balance = %d, want 100", bal)
	}
	if bal, _ := st.Balance(a.ID, alice); bal != 0 {
		t.Fatalf("issuer holds %d, want 0", bal)
	}
}

func TestTransferAndOverdraw(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 10}

	mint := b.next(protocol.OpMint, alice, alice, mintPayload(t, "coin", 10, []byte("x")))
	feed(t, ix, st, mint)
	id, _ := hex.DecodeString(mint.BlockHash)

	feed(t, ix, st, b.next(protocol.OpTransfer, alice, bob,
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 4})))

	if bal, _ := st.Balance(mint.BlockHash, alice); bal != 6 {
		t.Fatalf("sender balance = %d, want 6", bal)
	}
	if bal, _ := st.Balance(mint.BlockHash, bob); bal != 4 {
		t.Fatalf("recipient balance = %d, want 4", bal)
	}

	// Overdrawing is rejected whole — never partially applied.
	feed(t, ix, st, b.next(protocol.OpTransfer, bob, carol,
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 99})))
	if bal, _ := st.Balance(mint.BlockHash, bob); bal != 4 {
		t.Fatalf("overdraw changed the balance: %d", bal)
	}
	if bal, _ := st.Balance(mint.BlockHash, carol); bal != 0 {
		t.Fatalf("overdraw credited the recipient: %d", bal)
	}
}

// Two conflicting transfers in the same momentum: the earlier content index
// wins and the later is invalid. Not "both fail", not "last wins".
func TestDoubleSpendFirstWins(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 10}

	mint := b.next(protocol.OpMint, alice, alice, mintPayload(t, "coin", 10, []byte("x")))
	feed(t, ix, st, mint)
	id, _ := hex.DecodeString(mint.BlockHash)

	b.newMomentum()
	first := b.next(protocol.OpTransfer, alice, bob,
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 10}))
	second := b.next(protocol.OpTransfer, alice, carol,
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 10}))
	feed(t, ix, st, first)
	feed(t, ix, st, second)

	if bal, _ := st.Balance(mint.BlockHash, bob); bal != 10 {
		t.Fatalf("first transfer did not win: bob has %d", bal)
	}
	if bal, _ := st.Balance(mint.BlockHash, carol); bal != 0 {
		t.Fatalf("second transfer applied: carol has %d", bal)
	}
}

func TestBurn(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 10}

	mint := b.next(protocol.OpMint, alice, alice, mintPayload(t, "coin", 10, []byte("x")))
	feed(t, ix, st, mint)
	id, _ := hex.DecodeString(mint.BlockHash)

	feed(t, ix, st, b.next(protocol.OpTransfer, alice, protocol.BurnAddress.String(),
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 3})))

	if bal, _ := st.Balance(mint.BlockHash, alice); bal != 7 {
		t.Fatalf("burner balance = %d, want 7", bal)
	}
	// Burned units are credited to no one: circulation drops below supply.
	if bal, _ := st.Balance(mint.BlockHash, protocol.BurnAddress.String()); bal != 0 {
		t.Fatalf("burn address holds %d, want 0", bal)
	}
}

// --- multi-part chains ---

// chain feeds a tail, n intermediaries, and a head, and returns the head's
// event. Parts are published in reverse, exactly as the inscriber does.
func chain(t *testing.T, ix *Indexer, st *store.Store, b *evBuilder, issuer string, chunks [][]byte, partIssuer string) store.Event {
	t.Helper()
	if partIssuer == "" {
		partIssuer = issuer
	}
	// chunks[0] goes in the head; chunks[len-1] is the tail.
	var hashes []string
	for i := len(chunks) - 1; i >= 1; i-- {
		e := b.next(protocol.OpMulti, partIssuer, partIssuer, marshal(t, chunks[i]))
		if i != len(chunks)-1 {
			e.PHash = hashes[len(hashes)-1] // points at the part published before it
		}
		feed(t, ix, st, e)
		hashes = append(hashes, e.BlockHash)
	}
	head := b.next(protocol.OpMint, issuer, issuer, mintPayload(t, "big", 1, chunks[0]))
	head.PHash = hashes[len(hashes)-1] // the last intermediary published
	head.THash = hashes[0]             // the tail, published first
	feed(t, ix, st, head)
	return head
}

func TestMultiPartAssembly(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 20}

	chunks := [][]byte{[]byte("part-one:"), []byte("part-two:"), []byte("part-three")}
	head := chain(t, ix, st, b, alice, chunks, "")

	a, err := st.Asset(head.BlockHash)
	if err != nil || a == nil {
		t.Fatalf("multi-part asset not created: %v", err)
	}
	want := bytes.Join(chunks, nil)
	if !bytes.Equal(a.Content, want) {
		t.Fatalf("assembled %q, want %q", a.Content, want)
	}
	if a.PartCount != 3 {
		t.Fatalf("part count = %d, want 3", a.PartCount)
	}
}

// The most important rule in the multi-part section: without it, anyone can
// point a head into someone else's chunks and mint a duplicate for the cost
// of one block.
func TestMultiPartRejectsForeignChunks(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 20}

	chunks := [][]byte{[]byte("mine:"), []byte("yours")}
	head := chain(t, ix, st, b, alice, chunks, bob) // parts issued by bob

	if a, _ := st.Asset(head.BlockHash); a != nil {
		t.Fatal("a head consumed another account's chunks")
	}
}

// A tail may be consumed by at most one head; the first in canonical order
// wins. Without this, one chain can be re-minted indefinitely.
func TestTailConsumedOnce(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 20}

	chunks := [][]byte{[]byte("a:"), []byte("b")}
	first := chain(t, ix, st, b, alice, chunks, "")
	if a, _ := st.Asset(first.BlockHash); a == nil {
		t.Fatal("first head should have minted")
	}

	// A second head naming the same tail, later in canonical order.
	b.newMomentum()
	second := b.next(protocol.OpMint, alice, alice, mintPayload(t, "copy", 1, []byte("a:")))
	second.PHash = first.THash
	second.THash = first.THash
	feed(t, ix, st, second)

	if a, _ := st.Asset(second.BlockHash); a != nil {
		t.Fatal("a second head re-minted an already-consumed tail")
	}
}

// A pHash cycle would otherwise walk forever. This is a live denial of
// service: the attacker pays for two blocks, the indexer never returns.
func TestCycleDetection(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 20}

	p1 := b.next(protocol.OpMulti, alice, alice, marshal(t, []byte("one")))
	p2 := b.next(protocol.OpMulti, alice, alice, marshal(t, []byte("two")))
	p1.PHash, p2.PHash = p2.BlockHash, p1.BlockHash // a two-node loop
	feed(t, ix, st, p1)
	feed(t, ix, st, p2)

	head := b.next(protocol.OpMint, alice, alice, mintPayload(t, "loop", 1, []byte("head")))
	head.PHash = p1.BlockHash
	head.THash = hex.EncodeToString(bytes.Repeat([]byte{0xEE}, protocol.HashLen)) // never reached
	feed(t, ix, st, head)

	if a, _ := st.Asset(head.BlockHash); a != nil {
		t.Fatal("a cyclic chain produced an asset")
	}
}

func TestPartMustPrecedeHead(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 20}

	// Head first, part after: a chain that depends on a block that has not
	// happened yet.
	head := b.next(protocol.OpMint, alice, alice, mintPayload(t, "early", 1, []byte("head")))
	b.newMomentum()
	tail := b.next(protocol.OpMulti, alice, alice, marshal(t, []byte("tail")))
	feed(t, ix, st, tail)

	head.PHash, head.THash = tail.BlockHash, tail.BlockHash
	feed(t, ix, st, head)

	if a, _ := st.Asset(head.BlockHash); a != nil {
		t.Fatal("a head consumed a part that follows it in canonical order")
	}
}

// --- collections ---

func TestCollectionMembershipRequiresSameIssuer(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 30}

	col := b.next(protocol.OpCreateCollection, alice, alice,
		marshal(t, &protocol.CollectionWire{Name: "Alice's Set"}))
	feed(t, ix, st, col)
	colID, _ := hex.DecodeString(col.BlockHash)

	mine := b.next(protocol.OpMint, alice, alice, marshal(t, &protocol.MintWire{
		Name: "mine", Supply: 1, Mime: "text/plain;charset=utf-8",
		Data: []byte("x"), Collection: colID,
	}))
	feed(t, ix, st, mine)

	theirs := b.next(protocol.OpMint, bob, bob, marshal(t, &protocol.MintWire{
		Name: "theirs", Supply: 1, Mime: "text/plain;charset=utf-8",
		Data: []byte("x"), Collection: colID,
	}))
	feed(t, ix, st, theirs)

	a1, _ := st.Asset(mine.BlockHash)
	if a1 == nil || a1.CollectionID != col.BlockHash {
		t.Fatal("the issuer's own asset did not join the collection")
	}
	a2, _ := st.Asset(theirs.BlockHash)
	if a2 == nil {
		t.Fatal("an unowned collection reference should drop, not invalidate the asset")
	}
	if a2.CollectionID != "" {
		t.Fatal("an outsider joined someone else's collection")
	}
}

// --- dispensers ---

func setupDispenser(t *testing.T, ix *Indexer, st *store.Store, b *evBuilder, stock, price uint64) (assetID, dispID string) {
	t.Helper()
	mint := b.next(protocol.OpMint, alice, alice, mintPayload(t, "wares", 100, []byte("x")))
	feed(t, ix, st, mint)
	id, _ := hex.DecodeString(mint.BlockHash)

	disp := b.next(protocol.OpCreateDispenser, alice, alice, marshal(t, &protocol.DispenserWire{
		Asset: id, Amount: stock, Price: price, ZTS: types.ZnnTokenStandard.Bytes(),
	}))
	feed(t, ix, st, disp)
	return mint.BlockHash, disp.BlockHash
}

func TestDispenserEscrowsStock(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 40}
	assetID, dispID := setupDispenser(t, ix, st, b, 30, 5)

	// Escrowed stock leaves the seller's spendable balance, so it cannot be
	// sold twice or moved out from under a buyer.
	if bal, _ := st.Balance(assetID, alice); bal != 70 {
		t.Fatalf("seller spendable = %d, want 70", bal)
	}
	if bal, _ := st.Balance(assetID, dispID); bal != 30 {
		t.Fatalf("escrow = %d, want 30", bal)
	}
}

func TestPurchaseMustPayTheIssuer(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 40}
	assetID, dispID := setupDispenser(t, ix, st, b, 30, 5)
	ref := marshal(t, &protocol.DispenserRefWire{Dispenser: mustDecode(t, dispID)})

	// Bob pays himself and claims the goods. Without the recipient check this
	// is a free withdrawal from anyone's dispenser.
	self := b.next(protocol.OpDispenserPurchase, bob, bob, ref)
	self.Amount = "50"
	feed(t, ix, st, self)

	if bal, _ := st.Balance(assetID, bob); bal != 0 {
		t.Fatalf("a self-paid purchase settled: bob holds %d", bal)
	}

	// Paying the issuer works.
	good := b.next(protocol.OpDispenserPurchase, bob, alice, ref)
	good.Amount = "50"
	feed(t, ix, st, good)

	if bal, _ := st.Balance(assetID, bob); bal != 10 {
		t.Fatalf("bought units = %d, want 10", bal)
	}
	d, _ := st.Dispenser(dispID)
	if d.Remaining != 20 {
		t.Fatalf("remaining = %d, want 20", d.Remaining)
	}
}

func TestPurchaseNoPartialFill(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 40}
	assetID, dispID := setupDispenser(t, ix, st, b, 5, 10)
	ref := marshal(t, &protocol.DispenserRefWire{Dispenser: mustDecode(t, dispID)})

	// Paying for 8 units when 5 remain. There is no refund path, so filling
	// part of the order would silently overcharge.
	over := b.next(protocol.OpDispenserPurchase, bob, alice, ref)
	over.Amount = "80"
	feed(t, ix, st, over)

	if bal, _ := st.Balance(assetID, bob); bal != 0 {
		t.Fatalf("an over-sized purchase partially filled: bob holds %d", bal)
	}
	d, _ := st.Dispenser(dispID)
	if d.Remaining != 5 {
		t.Fatalf("remaining = %d, want 5", d.Remaining)
	}
}

func TestPurchaseWrongToken(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 40}
	assetID, dispID := setupDispenser(t, ix, st, b, 10, 5)
	ref := marshal(t, &protocol.DispenserRefWire{Dispenser: mustDecode(t, dispID)})

	e := b.next(protocol.OpDispenserPurchase, bob, alice, ref)
	e.Amount = "50"
	e.TokenStandard = types.QsrTokenStandard.String() // dispenser priced in ZNN
	feed(t, ix, st, e)

	if bal, _ := st.Balance(assetID, bob); bal != 0 {
		t.Fatalf("a purchase in the wrong token settled: bob holds %d", bal)
	}
}

func TestCancelReturnsEscrowToIssuerOnly(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 40}
	assetID, dispID := setupDispenser(t, ix, st, b, 30, 5)
	ref := marshal(t, &protocol.DispenserRefWire{Dispenser: mustDecode(t, dispID)})

	feed(t, ix, st, b.next(protocol.OpCancelDispenser, bob, bob, ref)) // not the issuer
	if bal, _ := st.Balance(assetID, dispID); bal != 30 {
		t.Fatalf("an outsider cancelled the dispenser: escrow = %d", bal)
	}

	feed(t, ix, st, b.next(protocol.OpCancelDispenser, alice, alice, ref))
	if bal, _ := st.Balance(assetID, alice); bal != 100 {
		t.Fatalf("escrow not returned: seller has %d, want 100", bal)
	}
	if bal, _ := st.Balance(assetID, dispID); bal != 0 {
		t.Fatalf("escrow still held: %d", bal)
	}
}

// --- replay ---

// The property the whole design rests on: state is a pure function of the
// event log and canonical order, so replaying reproduces it exactly. If this
// fails, rollback is unsound no matter how carefully it is written.
func TestRebuildReproducesState(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 50}

	col := b.next(protocol.OpCreateCollection, alice, alice,
		marshal(t, &protocol.CollectionWire{Name: "Set"}))
	feed(t, ix, st, col)
	colID, _ := hex.DecodeString(col.BlockHash)

	mint := b.next(protocol.OpMint, alice, alice, marshal(t, &protocol.MintWire{
		Name: "coin", Supply: 50, Mime: "text/plain;charset=utf-8",
		Data: []byte("x"), Collection: colID,
	}))
	feed(t, ix, st, mint)
	id, _ := hex.DecodeString(mint.BlockHash)

	b.newMomentum()
	feed(t, ix, st, b.next(protocol.OpTransfer, alice, bob,
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 20})))
	feed(t, ix, st, b.next(protocol.OpTransfer, bob, carol,
		marshal(t, &protocol.TransferWire{Asset: id, Amount: 5})))
	feed(t, ix, st, b.next(protocol.OpCreateDispenser, alice, alice,
		marshal(t, &protocol.DispenserWire{
			Asset: id, Amount: 10, Price: 3, ZTS: types.ZnnTokenStandard.Bytes(),
		})))

	before := snapshot(t, st, mint.BlockHash)

	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, st, mint.BlockHash)

	if before != after {
		t.Fatalf("replay produced different state:\n before: %s\n after:  %s", before, after)
	}
	// A second rebuild must be a no-op too — idempotence catches projections
	// that accumulate rather than recompute.
	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if again := snapshot(t, st, mint.BlockHash); again != before {
		t.Fatalf("second replay diverged:\n want: %s\n got:  %s", before, again)
	}
}

func snapshot(t *testing.T, st *store.Store, assetID string) string {
	t.Helper()
	holders, err := st.HoldersOf(assetID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].Address < holders[j].Address })

	var sb bytes.Buffer
	a, _ := st.Asset(assetID)
	if a != nil {
		sb.WriteString(a.ID + "|" + a.Owner + "|" + a.CollectionID + "|")
		sb.WriteString(hex.EncodeToString(a.Content))
	}
	for _, h := range holders {
		sb.WriteString("\n" + h.Address + "=" + itoa(h.Amount))
	}
	return sb.String()
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- canonical ordering ---

// V-1 in the plan: momentum.content is canonically sorted by
// (address, big-endian height, hash) and that ordering is committed to by the
// momentum hash — so an indexer can recompute it rather than trust the node.
func TestHeaderBytesMatchesConsensusSort(t *testing.T) {
	addrA := types.ParseAddressPanic(alice)
	addrB := types.ParseAddressPanic(bob)

	headers := []rpcclient.AccountHeader{
		{Address: addrB, Height: 1, Hash: types.NewHash([]byte("b1"))},
		{Address: addrA, Height: 9, Hash: types.NewHash([]byte("a9"))},
		{Address: addrA, Height: 2, Hash: types.NewHash([]byte("a2"))},
	}
	sort.SliceStable(headers, func(i, j int) bool {
		return bytes.Compare(headerBytes(headers[i]), headerBytes(headers[j])) < 0
	})

	// go-zenon's own comparer, on the same inputs.
	want := []types.AccountHeader{
		{Address: addrB, HashHeight: types.HashHeight{Height: 1, Hash: types.NewHash([]byte("b1"))}},
		{Address: addrA, HashHeight: types.HashHeight{Height: 9, Hash: types.NewHash([]byte("a9"))}},
		{Address: addrA, HashHeight: types.HashHeight{Height: 2, Hash: types.NewHash([]byte("a2"))}},
	}
	ptrs := make([]*types.AccountHeader, len(want))
	for i := range want {
		ptrs[i] = &want[i]
	}
	sort.SliceStable(ptrs, func(i, j int) bool {
		return bytes.Compare(ptrs[i].Bytes(), ptrs[j].Bytes()) < 0
	})

	for i := range headers {
		if headers[i].Hash != ptrs[i].Hash {
			t.Fatalf("position %d: recomputed order does not match go-zenon's", i)
		}
	}

	// Big-endian height is what makes lexicographic order equal numeric
	// order, which is what preserves per-account causality.
	if headers[0].Address != addrA || headers[0].Height != 2 {
		t.Fatalf("lowest address should sort first, then by height: got %s h=%d",
			headers[0].Address, headers[0].Height)
	}
}

// The state root must commit to what actually changed state, not to what
// merely parsed. Two indexers that admit the same blocks to their logs but
// enforce different opcode rules have to produce different roots — otherwise
// the commitment cannot see the one disagreement it exists to detect.
func TestStateRootCommitsToAppliedSetOnly(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 70}

	// A well-formed transfer of an asset that does not exist: it parses, so
	// it enters the log, but no rule lets it change anything.
	ghost := b.next(protocol.OpTransfer, alice, bob,
		marshal(t, &protocol.TransferWire{
			Asset: bytes.Repeat([]byte{0x5A}, protocol.HashLen), Amount: 1,
		}))
	feed(t, ix, st, ghost)

	if err := st.PutMomentum(store.MomentumRow{
		Height: ghost.Height, Hash: "h", PreviousHash: "p",
	}); err != nil {
		t.Fatal(err)
	}

	hashes, err := st.TxHashesAt(ghost.Height)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 0 {
		t.Fatalf("a rejected event entered the commitment: %v", hashes)
	}
	if n, _ := st.EventCount(); n != 1 {
		t.Fatalf("the rejected event should still be in the log, got %d events", n)
	}

	// A mint in the same position does change state, so it must appear.
	b2 := &evBuilder{height: 71}
	mint := b2.next(protocol.OpMint, alice, alice, mintPayload(t, "real", 1, []byte("x")))
	feed(t, ix, st, mint)
	if err := st.PutMomentum(store.MomentumRow{
		Height: mint.Height, Hash: "h2", PreviousHash: "h",
	}); err != nil {
		t.Fatal(err)
	}
	hashes, err = st.TxHashesAt(mint.Height)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 || hashes[0] != mint.BlockHash {
		t.Fatalf("applied event missing from the commitment: %v", hashes)
	}
}

// Rebuild recomputes the applied flags and the roots, not just the
// projections. A reindex that leaves stale roots is a commitment describing
// state that no longer exists.
func TestRebuildRecomputesRoots(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := &evBuilder{height: 80}

	mint := b.next(protocol.OpMint, alice, alice, mintPayload(t, "coin", 5, []byte("x")))
	feed(t, ix, st, mint)
	if err := st.PutMomentum(store.MomentumRow{
		Height: mint.Height, Hash: "h", PreviousHash: "p", StateRoot: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}

	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	root, err := st.StateRoot(mint.Height)
	if err != nil {
		t.Fatal(err)
	}
	if root == "deadbeef" || root == "" {
		t.Fatalf("rebuild left the root stale: %q", root)
	}

	// And it is stable: replaying the same log twice gives the same root.
	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	again, _ := st.StateRoot(mint.Height)
	if again != root {
		t.Fatalf("root not deterministic across replays: %q vs %q", root, again)
	}
}
