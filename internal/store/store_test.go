package store

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Supply runs to 2^64-1, and SQLite's INTEGER is signed 64-bit. Storing
// amounts as decimal TEXT is what keeps a large supply from silently
// wrapping — this is the test that would have caught B-6.
func TestLargeSupplySurvivesRoundTrip(t *testing.T) {
	s := open(t)
	const max = ^uint64(0) // 18446744073709551615, past 2^63-1

	if err := s.PutAsset(Asset{
		ID: "a1", Number: 0, Issuer: "z1", Owner: "z1",
		Supply: max, Mime: "text/plain;charset=utf-8", Content: []byte("x"),
		Height: 1, PartCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Asset("a1")
	if err != nil || got == nil {
		t.Fatalf("asset missing: %v", err)
	}
	if got.Supply != max {
		t.Fatalf("supply came back as %d, want %d", got.Supply, max)
	}

	if err := s.SetBalance("a1", "z1", max); err != nil {
		t.Fatal(err)
	}
	bal, err := s.Balance("a1", "z1")
	if err != nil {
		t.Fatal(err)
	}
	if bal != max {
		t.Fatalf("balance came back as %d, want %d", bal, max)
	}
}

// On-chain strings reach the store from arbitrary senders. Every statement is
// parameterised; this asserts that a name built to break out of a quoted
// literal is stored and returned verbatim, and that the schema survives it.
func TestHostileStringsAreDataNotSQL(t *testing.T) {
	s := open(t)
	hostile := `'); DROP TABLE assets; --`

	if err := s.PutAsset(Asset{
		ID: "a1", Name: hostile, Description: `" OR 1=1 --`,
		Supply: 1, Mime: "text/plain;charset=utf-8", Content: []byte("x"), Height: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Asset("a1")
	if err != nil {
		t.Fatalf("the assets table did not survive: %v", err)
	}
	if got == nil || got.Name != hostile {
		t.Fatalf("name was not stored verbatim: %+v", got)
	}
	// The table must still exist and still be queryable.
	if _, _, err := s.ListAssets(AssetFilter{}); err != nil {
		t.Fatalf("listing failed after hostile insert: %v", err)
	}
}

// Cold start: an empty database reports tip 0, and the syncer must read that
// as "start at the configured launch height", not "wait for height to grow".
// B-3 was exactly this deadlock.
func TestEmptyDatabaseReportsZeroTip(t *testing.T) {
	s := open(t)
	h, err := s.TipHeight()
	if err != nil {
		t.Fatal(err)
	}
	if h != 0 {
		t.Fatalf("empty tip = %d, want 0", h)
	}
	if root, err := s.StateRoot(5); err != nil || root != "" {
		t.Fatalf("state root for an unindexed height should be empty: %q %v", root, err)
	}
	if m, err := s.Momentum(5); err != nil || m != nil {
		t.Fatalf("momentum for an unindexed height should be nil: %+v %v", m, err)
	}
}

// Replay must see events in canonical order across page boundaries. The
// paging cursor is (height, momentum_index) precisely so that a page break
// cannot reorder or drop an event the way an OFFSET can.
func TestEventsPageInCanonicalOrder(t *testing.T) {
	s := open(t)
	const momentums, perMomentum = 40, 30 // 1200 events, well past the 512 page size

	for h := uint64(1); h <= momentums; h++ {
		for i := 0; i < perMomentum; i++ {
			if err := s.AppendEvent(Event{
				Height: h, MomentumIndex: i,
				BlockHash: "h", Opcode: 1, Version: 1,
				Sender: "z1", Recipient: "z2", Payload: []byte{1},
				Amount: "0",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	var seen int
	var lastH uint64
	lastI := -1
	err := s.Events(func(e Event) error {
		seen++
		if e.Height < lastH || (e.Height == lastH && e.MomentumIndex <= lastI) {
			t.Fatalf("out of order at %d: (%d,%d) followed (%d,%d)",
				seen, e.Height, e.MomentumIndex, lastH, lastI)
		}
		lastH, lastI = e.Height, e.MomentumIndex
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := momentums * perMomentum; seen != want {
		t.Fatalf("replayed %d events, want %d", seen, want)
	}
}

// Replay writes projections from inside the Events callback. The store holds
// one connection so writers cannot interleave, so a cursor held open across
// the callback deadlocks against itself. This asserts the callback can write.
func TestEventsCallbackMayWrite(t *testing.T) {
	s := open(t)
	for h := uint64(1); h <= 600; h++ {
		if err := s.AppendEvent(Event{
			Height: h, MomentumIndex: 0, BlockHash: "h", Opcode: 0,
			Version: 1, Sender: "z1", Recipient: "z1", Payload: []byte{1}, Amount: "0",
		}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	err := s.Events(func(e Event) error {
		n++
		return s.SetBalance("asset", "z1", uint64(n))
	})
	if err != nil {
		t.Fatal(err)
	}
	if bal, _ := s.Balance("asset", "z1"); bal != 600 {
		t.Fatalf("callback writes did not all land: %d", bal)
	}
}

func TestTruncateAboveAndResetProjections(t *testing.T) {
	s := open(t)
	for h := uint64(1); h <= 10; h++ {
		if err := s.PutMomentum(MomentumRow{
			Height: h, Hash: "h", PreviousHash: "p", StateRoot: "r",
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendEvent(Event{
			Height: h, MomentumIndex: 0, BlockHash: "b", Opcode: 0,
			Version: 1, Sender: "z1", Recipient: "z1", Payload: []byte{1}, Amount: "0",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutAsset(Asset{ID: "a1", Supply: 1, Mime: "m", Content: []byte("x"), Height: 3}); err != nil {
		t.Fatal(err)
	}

	if err := s.TruncateAbove(6); err != nil {
		t.Fatal(err)
	}
	if tip, _ := s.TipHeight(); tip != 6 {
		t.Fatalf("tip after truncate = %d, want 6", tip)
	}
	if n, _ := s.EventCount(); n != 6 {
		t.Fatalf("events after truncate = %d, want 6", n)
	}
	// Truncation leaves projections alone; clearing them is a separate step,
	// because a rollback is "delete above H, drop projections, replay".
	if a, _ := s.Asset("a1"); a == nil {
		t.Fatal("truncate should not touch projections")
	}
	if err := s.ResetProjections(); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Asset("a1"); a != nil {
		t.Fatal("projections should be gone")
	}
	// The event log and momentum chain must survive, or replay is impossible.
	if n, _ := s.EventCount(); n != 6 {
		t.Fatalf("ResetProjections destroyed the event log: %d events left", n)
	}
}

// A tail may be consumed by exactly one head. The claim has to be atomic at
// the storage layer, because "first head wins" is decided by insertion order.
func TestClaimTailIsExclusive(t *testing.T) {
	s := open(t)
	ok, err := s.ClaimTail("tail", "asset1", 10)
	if err != nil || !ok {
		t.Fatalf("first claim failed: %v %v", ok, err)
	}
	ok, err = s.ClaimTail("tail", "asset2", 11)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a second head claimed an already-consumed tail")
	}
}

// Moderation hides an asset from listings but must never remove it from the
// store — the bytes stay indexed, only the serving layer declines.
func TestModerationHidesFromListingsOnly(t *testing.T) {
	s := open(t)
	if err := s.PutAsset(Asset{
		ID: "a1", Number: 0, Supply: 1, Mime: "text/plain;charset=utf-8",
		Content: []byte("x"), Height: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.ListAssets(AssetFilter{}); total != 1 {
		t.Fatalf("baseline listing total = %d", total)
	}

	if err := s.SetModeration("a1", Moderation{MetadataPermitted: false, DataPermitted: false, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.ListAssets(AssetFilter{}); total != 0 {
		t.Fatalf("hidden asset still listed (total %d)", total)
	}
	// Direct lookup still finds it: the API decides what to do about that,
	// and the indexer still needs it for replay and for the state root.
	if a, _ := s.Asset("a1"); a == nil {
		t.Fatal("moderation removed the asset from the store")
	}
	rows, err := s.ListModeration()
	if err != nil || len(rows) != 1 || rows[0].AssetID != "a1" {
		t.Fatalf("moderation listing: %+v %v", rows, err)
	}

	// Clearing the policy restores it, and clears it from the listing.
	if err := s.SetModeration("a1", Moderation{MetadataPermitted: true, DataPermitted: true}); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.ListAssets(AssetFilter{}); total != 1 {
		t.Fatal("clearing the policy did not restore the asset to listings")
	}
	if rows, _ := s.ListModeration(); len(rows) != 0 {
		t.Fatalf("a fully-permissive row should not be listed: %+v", rows)
	}
}

func TestAssetFilters(t *testing.T) {
	s := open(t)
	rows := []Asset{
		{ID: "a1", Number: 0, Issuer: "zA", Owner: "zA", Mime: "image/png", Supply: 1, Content: []byte("x"), Height: 1, CollectionID: "c1"},
		{ID: "a2", Number: 1, Issuer: "zA", Owner: "zB", Mime: "image/png", Supply: 1, Content: []byte("x"), Height: 2},
		{ID: "a3", Number: 2, Issuer: "zB", Owner: "zB", Mime: "application/json", Supply: 1, Content: []byte("x"), Height: 3},
	}
	for _, a := range rows {
		if err := s.PutAsset(a); err != nil {
			t.Fatal(err)
		}
	}
	for name, f := range map[string]struct {
		filter AssetFilter
		want   int
	}{
		"owner":      {AssetFilter{Owner: "zB"}, 2},
		"issuer":     {AssetFilter{Issuer: "zA"}, 2},
		"mime":       {AssetFilter{Mime: "image/png"}, 2},
		"collection": {AssetFilter{Collection: "c1"}, 1},
		"combined":   {AssetFilter{Issuer: "zA", Owner: "zB"}, 1},
		"none":       {AssetFilter{}, 3},
	} {
		got, total, err := s.ListAssets(f.filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if total != f.want || len(got) != f.want {
			t.Errorf("%s: got %d rows / total %d, want %d", name, len(got), total, f.want)
		}
	}

	// Newest first, so the gallery leads with the most recent inscription.
	got, _, _ := s.ListAssets(AssetFilter{})
	if got[0].ID != "a3" {
		t.Fatalf("listing is not newest-first: leads with %s", got[0].ID)
	}
}

func TestNextAssetNumberIsDense(t *testing.T) {
	s := open(t)
	if n, _ := s.NextAssetNumber(); n != 0 {
		t.Fatalf("first number = %d, want 0", n)
	}
	for i := int64(0); i < 5; i++ {
		n, _ := s.NextAssetNumber()
		if n != i {
			t.Fatalf("number %d, want %d", n, i)
		}
		if err := s.PutAsset(Asset{
			ID: strings.Repeat("a", int(i)+1), Number: n,
			Supply: 1, Mime: "m", Content: []byte("x"), Height: uint64(i + 1),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// A listing must not drag the inscribed bytes along. An asset holds up to a
// megabyte and a gallery page shows twenty of them, so a listing that selects
// the blob reads twenty megabytes to print twenty file sizes — the cost that
// decides whether this store scales past a devnet, and it is paid on the
// most-requested endpoint there is.
func TestListingsReadSizesNotBlobs(t *testing.T) {
	st := open(t)
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i)
	}
	if err := st.PutAsset(Asset{
		ID: "aa" + strings.Repeat("0", 62), Number: 0, Issuer: "z1a", Owner: "z1a",
		Supply: 1, Mime: "text/plain;charset=utf-8", Content: content, PartCount: 1,
		Height: 1,
	}); err != nil {
		t.Fatal(err)
	}

	listed, _, err := st.ListAssets(AssetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("%d assets listed", len(listed))
	}
	if listed[0].Content != nil {
		t.Errorf("a listing loaded %d bytes of content", len(listed[0].Content))
	}
	if listed[0].Size != len(content) {
		t.Errorf("listed size = %d, want %d", listed[0].Size, len(content))
	}

	// Fetching one asset still returns the bytes: that is what the content
	// endpoint serves.
	one, err := st.Asset("aa" + strings.Repeat("0", 62))
	if err != nil || one == nil {
		t.Fatalf("Asset: %v %v", one, err)
	}
	if !bytes.Equal(one.Content, content) {
		t.Error("fetching one asset lost its content")
	}
	if one.Size != len(content) {
		t.Errorf("size = %d on a full read, want %d", one.Size, len(content))
	}
}
