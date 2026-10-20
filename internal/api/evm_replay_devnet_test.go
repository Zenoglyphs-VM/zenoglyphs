//go:build devnet

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// TestEVMReplayOfDevnetHistory derives the EVM from a real indexer database
// and pins the result, so a change to the engine or the cache can be checked
// against thousands of real transactions rather than hand-built fixtures:
//
//	ZG_DB=zenoglyphs.db ZG_EVM_DIGEST=<digest> go test -tags devnet -run EVMReplay -v ./internal/api
//
// The database is copied first (with its WAL), because a running indexer
// holds the original. The digest covers the state root and every receipt's
// outcome, gas and logs; without ZG_EVM_DIGEST the test prints it.
func TestEVMReplayOfDevnetHistory(t *testing.T) {
	src := os.Getenv("ZG_DB")
	if src == "" {
		src = filepath.Join("..", "..", "zenoglyphs.db")
	}
	dir := t.TempDir()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		copyFile(t, src+suffix, filepath.Join(dir, "zg.db"+suffix))
	}
	st, err := store.Open(filepath.Join(dir, "zg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	assets, err := st.AssetsOfMimeCarried(evm.Mime)
	if err != nil {
		t.Fatal(err)
	}
	ins := make([]evm.Inscription, 0, len(assets))
	for _, a := range assets {
		ins = append(ins, evm.Inscription{
			ID: a.ID, Issuer: a.Issuer, Mime: a.Mime,
			Height: a.Height, Index: a.MomentumIndex, Timestamp: a.Timestamp,
			Content: a.Content, Ext: a.Ext,
			Recipient: a.Recipient, Amount: a.Amount, TokenStandard: a.TokenStandard,
			BasePlasma: a.BasePlasma, FusedPlasma: a.FusedPlasma, Difficulty: a.Difficulty,
		})
	}

	start := time.Now()
	full := evm.NewChain(69)
	if err := full.Apply(ins); err != nil {
		t.Fatal(err)
	}
	t.Logf("full replay: %d inscriptions, %d receipts, %s", len(ins), len(full.Receipts), time.Since(start))
	got := chainDigest(t, full)

	// The same history applied a momentum-sized slice at a time must land on
	// exactly the same state: that is what makes following the tail sound.
	inc := evm.NewChain(69)
	for i := 0; i < len(ins); i += 97 {
		end := min(i+97, len(ins))
		if err := inc.Apply(ins[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	if d := chainDigest(t, inc); d != got {
		t.Fatalf("incremental replay digest %s, full replay %s", d, got)
	}

	want := os.Getenv("ZG_EVM_DIGEST")
	if want == "" {
		t.Logf("digest %s (root %s) — set ZG_EVM_DIGEST to pin it", got, full.State.Root())
		return
	}
	if got != want {
		t.Fatalf("digest %s, want %s (root %s)", got, want, full.State.Root())
	}
}

func chainDigest(t *testing.T, c *evm.Chain) string {
	t.Helper()
	h := sha256.New()
	h.Write([]byte(c.State.Root()))
	for _, r := range c.Receipts {
		b, err := json.Marshal(struct {
			ID, Err     string
			Success     bool
			Gas, Budget uint64
			Out         []byte
			Logs        []evm.Log
		}{r.Inscription, r.Err, r.Success, r.GasUsed, r.GasBudget, r.Output, r.Logs})
		if err != nil {
			t.Fatal(err)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if os.IsNotExist(err) {
		return // no WAL is fine
	}
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}
