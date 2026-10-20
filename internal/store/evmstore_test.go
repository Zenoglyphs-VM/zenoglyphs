package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "evm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func receiptRows(heights ...uint64) []EVMReceiptRow {
	var out []EVMReceiptRow
	for _, h := range heights {
		out = append(out, EVMReceiptRow{Height: h, Inscription: "i", Body: []byte{byte(h)}})
	}
	return out
}

func countReceipts(t *testing.T, s *Store) (n int) {
	t.Helper()
	if err := s.EVMReceiptsUpTo(1<<62, 0, func(EVMReceiptRow) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// A derived row computed before a rewrite must not be written after it: that
// is how a checkpoint of an abandoned branch would outlive the reorg that
// abandoned it.
func TestEVMWritesAreFencedOnGeneration(t *testing.T) {
	s := openTest(t)
	gen, err := s.ProjectionGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEVMReceipts(gen, receiptRows(1, 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetProjections(); err != nil {
		t.Fatal(err)
	}
	if got := countReceipts(t, s); got != 0 {
		t.Fatalf("%d receipts survived a projection reset", got)
	}
	if err := s.AppendEVMReceipts(gen, receiptRows(3)); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("receipts from before the reset: %v, want ErrStaleGeneration", err)
	}
	if err := s.PutEVMCheckpoint(gen, EVMCheckpoint{Height: 3, Version: 1, ChainID: 69, State: []byte{1}}, 360); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("checkpoint from before the reset: %v, want ErrStaleGeneration", err)
	}
	now, _ := s.ProjectionGeneration()
	if err := s.AppendEVMReceipts(now, receiptRows(3)); err != nil {
		t.Fatalf("current generation refused: %v", err)
	}
}

// A checkpoint restore rewinds the EVM projections with everything else, so
// whatever EVM checkpoint survives it belongs to the surviving history.
func TestEVMRowsAreTruncatedByARestore(t *testing.T) {
	s := openTest(t)
	gen, _ := s.ProjectionGeneration()
	if err := s.AppendEVMReceipts(gen, receiptRows(5, 10, 15, 20)); err != nil {
		t.Fatal(err)
	}
	for _, h := range []uint64{8, 16} {
		if err := s.PutEVMCheckpoint(gen, EVMCheckpoint{Height: h, Version: 1, ChainID: 69, State: []byte{1}}, 360); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutSnapshot(12, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreSnapshot(12); err != nil {
		t.Fatal(err)
	}
	if got := countReceipts(t, s); got != 2 {
		t.Fatalf("%d receipts after restoring to 12, want the 2 at or below it", got)
	}
	cp, err := s.LatestEVMCheckpoint(1, 69)
	if err != nil {
		t.Fatal(err)
	}
	if cp == nil || cp.Height != 8 {
		t.Fatalf("newest checkpoint after the restore = %+v, want the one at 8", cp)
	}
	if again, _ := s.ProjectionGeneration(); again == gen {
		t.Fatal("a restore did not bump the projection generation")
	}
}

// Pruning keeps the reorg window plus one checkpoint below it — the same
// guarantee as the indexer's own checkpoints — and only this version's are
// offered.
func TestEVMCheckpointsArePrunedAndVersioned(t *testing.T) {
	s := openTest(t)
	gen, _ := s.ProjectionGeneration()
	for _, h := range []uint64{100, 200, 300, 400, 500, 600, 700} {
		if err := s.PutEVMCheckpoint(gen, EVMCheckpoint{Height: h, Version: 1, ChainID: 69, State: []byte{1}}, 360); err != nil {
			t.Fatal(err)
		}
	}
	// Tip 700, window 360 -> floor 340; the newest at or below it is 300.
	var lowest, n int
	if err := s.db.QueryRow(`SELECT MIN(height), COUNT(*) FROM evm_checkpoints`).Scan(&lowest, &n); err != nil {
		t.Fatal(err)
	}
	if lowest != 300 || n != 5 {
		t.Fatalf("kept %d checkpoints from %d, want 5 from 300", n, lowest)
	}
	if cp, _ := s.LatestEVMCheckpoint(2, 69); cp != nil {
		t.Fatal("offered a checkpoint from other derivation rules")
	}
	if cp, _ := s.LatestEVMCheckpoint(1, 1); cp != nil {
		t.Fatal("offered a checkpoint from another chain")
	}
}
