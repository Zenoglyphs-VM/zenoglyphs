package evm

import (
	"math/big"
	"reflect"
	"testing"
)

// A restored chain must be the chain it was taken from: same root, same
// receipts, and — the property resuming depends on — the same future, so that
// applying more inscriptions to either lands on the same state.
func TestCheckpointRoundTrip(t *testing.T) {
	ins, addr := counterFixture(t)
	orig := NewChain(69)
	if err := orig.Apply(ins); err != nil {
		t.Fatal(err)
	}

	blob, err := orig.MarshalCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	var receipts []Receipt
	for _, r := range orig.Receipts {
		b, err := MarshalReceipt(r)
		if err != nil {
			t.Fatal(err)
		}
		back, err := UnmarshalReceipt(b)
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, back)
	}
	restored, err := RestoreChain(69, blob, receipts)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State.Root() != orig.State.Root() {
		t.Fatalf("restored root %s, original %s", restored.State.Root(), orig.State.Root())
	}
	if !reflect.DeepEqual(normReceipts(restored.Receipts), normReceipts(orig.Receipts)) {
		t.Fatal("receipts changed across a checkpoint")
	}
	if restored.Applied != orig.Applied || restored.Failed != orig.Failed || restored.Height != orig.Height {
		t.Fatalf("counters: restored %d/%d/%d, original %d/%d/%d",
			restored.Applied, restored.Failed, restored.Height, orig.Applied, orig.Failed, orig.Height)
	}
	if _, ok := restored.Receipt(ins[2].ID); !ok {
		t.Fatal("receipt index not rebuilt")
	}

	// The same next inscription on both sides.
	next := []Inscription{{ID: id32(9), Issuer: devnetIssuer, Mime: Mime, Height: 104,
		Content: hexb(t, "d09de08a"), Ext: ext(t, map[string]any{"op": "call", "to": addr.Hex()})}}
	for _, c := range []*Chain{orig, restored} {
		if err := c.Apply(next); err != nil {
			t.Fatal(err)
		}
	}
	if restored.State.Root() != orig.State.Root() {
		t.Fatal("restored chain diverged on the next inscription")
	}
	if got := restored.State.Load(addr, new(big.Int)); got.Cmp(big.NewInt(4)) != 0 {
		t.Fatalf("counter = %s after one more increment, want 4", got)
	}
}

// A checkpoint that is not exactly what the reader expects is refused, so the
// caller replays instead of resuming something wrong.
func TestCheckpointRefusesMismatches(t *testing.T) {
	ins, _ := counterFixture(t)
	c := NewChain(69)
	if err := c.Apply(ins); err != nil {
		t.Fatal(err)
	}
	blob, err := c.MarshalCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreChain(1, blob, c.Receipts); err == nil {
		t.Error("restored a chain-69 checkpoint as chain 1")
	}
	if _, err := RestoreChain(69, blob, c.Receipts[:len(c.Receipts)-1]); err == nil {
		t.Error("restored with a receipt missing")
	}
	if _, err := RestoreChain(69, []byte{0xff, 0x00}, c.Receipts); err == nil {
		t.Error("restored garbage")
	}
}

// Nil and empty slices are the same receipt; CBOR need not preserve which.
func normReceipts(rs []Receipt) []Receipt {
	out := make([]Receipt, len(rs))
	for i, r := range rs {
		if len(r.Input) == 0 {
			r.Input = nil
		}
		if len(r.Output) == 0 {
			r.Output = nil
		}
		if len(r.Logs) == 0 {
			r.Logs = nil
		}
		for j := range r.Logs {
			if len(r.Logs[j].Data) == 0 {
				r.Logs[j].Data = nil
			}
		}
		out[i] = r
	}
	return out
}
