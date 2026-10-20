package indexer

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// The drift guard for A-17.
//
// Checkpoints exist so a reorg does not replay the whole event log. The
// argument that makes them safe is that they move the base case of the replay
// rather than inverting anything — Apply is untouched, and the rollback half
// is a mechanical "delete rows above this height" that encodes no protocol
// rule and so cannot disagree with one.
//
// That argument has one load-bearing assumption: every projection is either
// insert-only with a height on the row, or copied into the checkpoint. If a
// future opcode mutates a table that is neither, restore stops being
// equivalent to replay, silently. These tests are what turns that from a
// silent failure into a red one.

// buildHistory writes a log that touches every projection: mints, a
// multi-part chain, transfers, a burn, a collection, a composite, and a
// dispenser taken through escrow, purchase and cancel. Balances and dispenser
// rows are the two that mutate in place, so the point is to leave both in a
// state that a naive truncation would get wrong.
func buildHistory(t *testing.T, ix *Indexer, st *store.Store) *evBuilder {
	t.Helper()
	b := &evBuilder{height: 1}

	// A collection, then an asset that joins it.
	col := b.next(protocol.OpCreateCollection, alice, alice,
		marshal(t, &protocol.CollectionWire{Name: "Set"}))
	feed(t, ix, st, col)

	b.newMomentum()
	mint := b.next(protocol.OpMint, alice, alice, marshal(t, &protocol.MintWire{
		Name: "one", Supply: 1000, Mime: "text/plain;charset=utf-8",
		Data: []byte("first"), Collection: mustDecode(t, col.BlockHash),
	}))
	feed(t, ix, st, mint)

	// A composite importing it: exercises the asset_refs projection.
	b.newMomentum()
	comp := b.next(protocol.OpMint, alice, alice, marshal(t, &protocol.MintWire{
		Name: "composite", Supply: 1, Mime: "text/html;charset=utf-8",
		Data: []byte(`<img src="0">`), Refs: [][]byte{mustDecode(t, mint.BlockHash)},
	}))
	feed(t, ix, st, comp)

	// A multi-part chain: parts, then a head that consumes the tail.
	b.newMomentum()
	tail := b.next(protocol.OpMulti, alice, alice, marshal(t, []byte("-tail")))
	feed(t, ix, st, tail)
	b.newMomentum()
	head := b.next(protocol.OpMint, alice, alice, marshal(t, &protocol.MintWire{
		Name: "chained", Supply: 5, Mime: "text/plain;charset=utf-8", Data: []byte("head"),
	}))
	head.PHash, head.THash = tail.BlockHash, tail.BlockHash
	feed(t, ix, st, head)

	// Transfers and a burn: balances mutate in place.
	b.newMomentum()
	feed(t, ix, st, b.next(protocol.OpTransfer, alice, bob,
		marshal(t, &protocol.TransferWire{Asset: mustDecode(t, mint.BlockHash), Amount: 400})))
	feed(t, ix, st, b.next(protocol.OpTransfer, alice, protocol.BurnAddress.String(),
		marshal(t, &protocol.TransferWire{Asset: mustDecode(t, mint.BlockHash), Amount: 100})))

	// A dispenser: escrow moves balance to a pseudo-address, and the
	// dispenser row's remaining/cancelled are the only fields in the whole
	// schema that a later event rewrites on an existing row.
	b.newMomentum()
	disp := b.next(protocol.OpCreateDispenser, alice, alice, marshal(t, &protocol.DispenserWire{
		Asset: mustDecode(t, mint.BlockHash), Amount: 200, Price: 10,
		ZTS: types.ZnnTokenStandard.Bytes(),
	}))
	feed(t, ix, st, disp)
	return b
}

// buy and cancel are the events replayed after a checkpoint, so they are the
// ones whose effect a restore must reproduce rather than remember.
func extendHistory(t *testing.T, ix *Indexer, st *store.Store, b *evBuilder, dispenserHash string) {
	t.Helper()
	b.newMomentum()
	buy := b.next(protocol.OpDispenserPurchase, carol, alice,
		marshal(t, &protocol.DispenserRefWire{Dispenser: mustDecode(t, dispenserHash)}))
	buy.Amount = "150" // 15 units at 10
	feed(t, ix, st, buy)

	b.newMomentum()
	feed(t, ix, st, b.next(protocol.OpCancelDispenser, alice, alice,
		marshal(t, &protocol.DispenserRefWire{Dispenser: mustDecode(t, dispenserHash)})))
}

// TestSnapshotRestoreMatchesFullRebuild is the guard the comment in
// store/snapshot.go names: restoring a checkpoint and replaying forward must
// produce byte-identical state to replaying the whole log from nothing.
func TestSnapshotRestoreMatchesFullRebuild(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := buildHistory(t, ix, st)
	dispenserHash := blockHash(b.height, b.index-1)

	// Checkpoint here, mid-history, with a dispenser open and balances split
	// across three addresses plus escrow.
	if err := st.PutSnapshot(b.height, ""); err != nil {
		t.Fatal(err)
	}
	checkpoint := b.height

	extendHistory(t, ix, st, b, dispenserHash)

	// Momentum rows have to exist for the state-root recomputation both paths
	// perform; their contents do not matter here.
	for h := uint64(1); h <= b.height; h++ {
		if err := st.PutMomentum(store.MomentumRow{
			Height: h, Hash: "h", PreviousHash: "p", Timestamp: int64(h),
		}); err != nil {
			t.Fatal(err)
		}
	}

	live, err := st.ProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}

	// Path A: restore the checkpoint and replay only what came after.
	if err := ix.RebuildFrom(checkpoint); err != nil {
		t.Fatal(err)
	}
	fromCheckpoint, err := st.ProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if fromCheckpoint != live {
		t.Fatalf("checkpoint replay diverged from the live state\n  live:       %s\n  checkpoint: %s",
			live, fromCheckpoint)
	}

	// Path B: throw everything away and replay from nothing.
	if err := ix.Rebuild(); err != nil {
		t.Fatal(err)
	}
	full, err := st.ProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if full != fromCheckpoint {
		t.Fatalf("a checkpointed rebuild is not equivalent to a full one\n  full:       %s\n  checkpoint: %s",
			full, fromCheckpoint)
	}
}

// With no checkpoint at or below the rollback point, RebuildFrom must degrade
// to a full rebuild rather than to a partial one. This is what makes a lost or
// corrupt checkpoint cost time and never correctness.
func TestRebuildFromWithoutCheckpointReplaysEverything(t *testing.T) {
	ix, st := newTestIndexer(t)
	b := buildHistory(t, ix, st)
	for h := uint64(1); h <= b.height; h++ {
		if err := st.PutMomentum(store.MomentumRow{Height: h, Hash: "h", PreviousHash: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	want, err := st.ProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}

	if snap, err := st.SnapshotAtOrBelow(b.height); err != nil || snap != nil {
		t.Fatalf("expected no checkpoints: %v %v", snap, err)
	}
	if err := ix.RebuildFrom(b.height); err != nil {
		t.Fatal(err)
	}
	got, err := st.ProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("digest changed with no checkpoint in play:\n  want %s\n  got  %s", want, got)
	}
}

// A rollback past a checkpoint must not restore from it: that checkpoint
// describes a branch the chain abandoned.
func TestSnapshotsAboveTheForkAreDropped(t *testing.T) {
	_, st := newTestIndexer(t)
	for _, h := range []uint64{100, 200, 300} {
		if err := st.PutSnapshot(h, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DropSnapshotsAbove(150); err != nil {
		t.Fatal(err)
	}
	snaps, err := st.ListSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Height != 100 {
		t.Fatalf("checkpoints after the drop = %+v, want only 100", snaps)
	}
}

// Pruning keeps the window plus one checkpoint below it, so the guaranteed
// depth is a floor rather than an average: immediately after a prune there is
// still something at least `retain` deep to fall back to.
func TestPruneKeepsOneCheckpointBelowTheWindow(t *testing.T) {
	_, st := newTestIndexer(t)
	for _, h := range []uint64{100, 200, 300, 400, 500, 600, 700} {
		if err := st.PutSnapshot(h, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Tip 700, window 360 -> floor 340. The newest checkpoint at or below 340
	// is 300, so 100 and 200 go and everything from 300 up stays.
	if _, err := st.PruneSnapshots(700, 360); err != nil {
		t.Fatal(err)
	}
	snaps, err := st.ListSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 5 || snaps[0].Height != 300 {
		t.Fatalf("after prune = %+v, want 300..700", snaps)
	}
	if depth := 700 - snaps[0].Height; depth < 360 {
		t.Fatalf("deepest checkpoint is only %d momentums back, below the 360 window", depth)
	}
}
