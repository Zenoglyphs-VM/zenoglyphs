package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// A node that serves one branch, so a test can hand the indexer a chain that
// disagrees with what it already stored — a fork, or a devnet wiped and
// restarted under a running indexer.
type fakeNode struct {
	chainID uint64
	tip     uint64
	// hashAt names the branch: two fakeNodes with different tags produce
	// different hashes at every height, which is what a reset looks like.
	tag string
	// probes counts DetailedMomentumsByHeight calls with count == 1, the
	// shape the fork search uses. A search that walks linearly over a
	// seventeen-thousand-momentum reset is correct and unusable.
	probes int
}

func (n *fakeNode) hashAt(h uint64) types.Hash {
	return types.NewHash([]byte(fmt.Sprintf("%s-%d", n.tag, h)))
}

func (n *fakeNode) FrontierMomentum(context.Context) (*rpcclient.Momentum, error) {
	return &rpcclient.Momentum{
		Height: n.tip, Hash: n.hashAt(n.tip), PreviousHash: n.hashAt(n.tip - 1),
		ChainIdentifier: n.chainID,
	}, nil
}

func (n *fakeNode) DetailedMomentumsByHeight(_ context.Context, height, count uint64) ([]*rpcclient.DetailedMomentum, error) {
	if count == 1 {
		n.probes++
	}
	var out []*rpcclient.DetailedMomentum
	for h := height; h < height+count && h <= n.tip; h++ {
		out = append(out, &rpcclient.DetailedMomentum{Momentum: &rpcclient.Momentum{
			Height: h, Hash: n.hashAt(h), PreviousHash: n.hashAt(h - 1),
			ChainIdentifier: n.chainID,
		}})
	}
	return out, nil
}

func newIndexerWithNode(t *testing.T, node Node) (*Indexer, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "reorg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := DefaultConfig()
	cfg.SnapshotInterval, cfg.SnapshotRetain = 0, 0
	return New(cfg, node, st, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

// indexBranch writes momentums 1..h as if they had been indexed off `node`.
func indexBranch(t *testing.T, st *store.Store, node *fakeNode, h uint64) {
	t.Helper()
	for i := uint64(1); i <= h; i++ {
		if err := st.PutMomentum(store.MomentumRow{
			Height: i, Hash: node.hashAt(i).String(), PreviousHash: node.hashAt(i - 1).String(),
			Timestamp: int64(i), StateRoot: "",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The gap this closes: a devnet wiped and restarted under a long-running
// indexer. The node's tip drops below ours, so no momentum is ever fetched,
// so the parent-hash check never runs — and the indexer serves the old
// chain's inscriptions forever while reporting itself caught up.
func TestChainResetIsDetectedAndRolledBack(t *testing.T) {
	old := &fakeNode{chainID: 69, tip: 500, tag: "old"}
	ix, st := newIndexerWithNode(t, old)
	indexBranch(t, st, old, 500)
	ix.syncHeight.Store(500)

	// Same network, wiped: a new genesis, a much lower tip, and nothing in
	// common with what we hold.
	fresh := &fakeNode{chainID: 69, tip: 12, tag: "fresh"}
	ix.client = fresh

	if _, err := ix.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	tip, err := st.TipHeight()
	if err != nil {
		t.Fatal(err)
	}
	if tip != 0 {
		t.Fatalf("tip is %d after the reset; the abandoned branch was kept", tip)
	}
	if ix.SyncHeight() != 0 {
		t.Fatalf("sync height is %d, want 0", ix.SyncHeight())
	}
}

// A node that is merely behind — restarted from an older snapshot, still
// resyncing — is on the same chain, and throwing away good history because it
// has not caught up would be the more expensive mistake.
func TestNodeBehindIsNotTreatedAsAReset(t *testing.T) {
	node := &fakeNode{chainID: 69, tip: 500, tag: "same"}
	ix, st := newIndexerWithNode(t, node)
	indexBranch(t, st, node, 500)
	ix.syncHeight.Store(500)

	node.tip = 300 // same branch, shorter
	caughtUp, err := ix.step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if !caughtUp {
		t.Error("a node that is behind should leave the indexer idle, not busy")
	}
	if tip, _ := st.TipHeight(); tip != 500 {
		t.Fatalf("tip is %d; history was discarded for a node that was only behind", tip)
	}
}

// Pointing an existing database at another network must stop the indexer, not
// interleave two chains in one event log — there is no rollback that can
// separate them afterwards.
func TestDifferentChainIsFatal(t *testing.T) {
	node := &fakeNode{chainID: 1, tip: 100, tag: "mainnet"}
	ix, st := newIndexerWithNode(t, node)
	if _, err := ix.step(context.Background()); err != nil {
		t.Fatalf("first sync against chain 1: %v", err)
	}
	if got, _ := st.Meta("chain_id"); got != "1" {
		t.Fatalf("chain id recorded as %q, want 1", got)
	}

	ix.client = &fakeNode{chainID: 69, tip: 100, tag: "devnet"}
	_, err := ix.step(context.Background())
	if !errors.Is(err, ErrChainMismatch) {
		t.Fatalf("step against another chain returned %v, want ErrChainMismatch", err)
	}
	// And Run must not retry it, the way it retries a node that is down.
	if err := ix.Run(context.Background()); !errors.Is(err, ErrChainMismatch) {
		t.Fatalf("Run returned %v, want it to stop on ErrChainMismatch", err)
	}
}

// The fork search is a binary search, not a walk. A reset disagrees at every
// height, which is the case that used to cost one round trip per momentum
// ever indexed.
func TestForkSearchDoesNotWalkTheWholeChain(t *testing.T) {
	old := &fakeNode{chainID: 69, tip: 5000, tag: "old"}
	ix, st := newIndexerWithNode(t, old)
	indexBranch(t, st, old, 5000)

	fresh := &fakeNode{chainID: 69, tip: 5000, tag: "fresh"}
	ix.client = fresh

	agreed, err := ix.findLastAgreeingHeight(context.Background(), 5000)
	if err != nil {
		t.Fatal(err)
	}
	if agreed != 0 {
		t.Fatalf("last agreeing height = %d, want 0 (nothing in common)", agreed)
	}
	if fresh.probes > 40 {
		t.Fatalf("fork search made %d round trips over 5000 momentums; a walk is what this replaces", fresh.probes)
	}
}

// The ordinary case still has to work, and cheaply: a shallow fork a few
// momentums deep must find its exact fork point.
func TestForkSearchFindsAShallowFork(t *testing.T) {
	node := &fakeNode{chainID: 69, tip: 1000, tag: "branch-a"}
	ix, st := newIndexerWithNode(t, node)
	indexBranch(t, st, node, 1000)

	// The chain kept everything to 995 and rewrote the last five.
	forked := &fakeNode{chainID: 69, tip: 1000, tag: "branch-b"}
	ix.client = forkAt{base: node, after: forked, height: 995}

	agreed, err := ix.findLastAgreeingHeight(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if agreed != 995 {
		t.Fatalf("fork point = %d, want 995", agreed)
	}
}

// forkAt serves `base` up to and including height, and `after` above it.
type forkAt struct {
	base, after *fakeNode
	height      uint64
}

func (f forkAt) FrontierMomentum(ctx context.Context) (*rpcclient.Momentum, error) {
	return f.after.FrontierMomentum(ctx)
}

func (f forkAt) DetailedMomentumsByHeight(ctx context.Context, height, count uint64) ([]*rpcclient.DetailedMomentum, error) {
	if height <= f.height {
		return f.base.DetailedMomentumsByHeight(ctx, height, count)
	}
	return f.after.DetailedMomentumsByHeight(ctx, height, count)
}
