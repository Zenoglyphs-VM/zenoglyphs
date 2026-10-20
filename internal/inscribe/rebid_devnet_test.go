//go:build devnet

package inscribe

import (
	"context"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

// TestRebidReplacesPendingBlock runs against the docker devnet (chain 69):
//
//	go test -tags devnet -run Rebid ./internal/inscribe
//
// It needs no congestion. A block published at the exact price is rebid at
// once with headroom, before the next momentum, so the node holds two blocks
// at one height and must keep the better-paying one. Only the replacement may
// ever confirm, and its burn must be scaled by the same ratio as its plasma.
func TestRebidReplacesPendingBlock(t *testing.T) {
	const mnemonic = "abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million"
	key, err := LoadKey(KeySource{Mnemonic: mnemonic, Index: 100}) // the first benchmark swarm wallet
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in, err := New(rpcclient.New("http://localhost:35997"), key, 69, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	burn := big.NewInt(1_000_000)
	first, err := in.Send(ctx, protocol.BurnAddress, burn, types.ZnnTokenStandard, []byte("rebid test"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	in.HeadroomPercent = 25
	second, ok, err := in.Rebid(ctx, first)
	if err != nil || !ok {
		t.Fatalf("rebid: ok=%v err=%v", ok, err)
	}

	confirmed, _, _, err := in.ConfirmRebidding(ctx, second, 0)
	if err != nil {
		t.Fatalf("replacement never confirmed: %v", err)
	}
	if confirmed != second {
		t.Fatalf("confirmed %s, want the replacement %s", confirmed, second)
	}
	got, err := in.client.AccountBlockByHash(ctx, second)
	if err != nil || got == nil {
		t.Fatalf("read replacement: %v", err)
	}
	base := protocol.PlasmaForDataLength(len(got.Data))
	if got.FusedPlasma < base+base/4 {
		t.Errorf("replacement paid %d fused plasma, want at least 1.25 x %d", got.FusedPlasma, base)
	}
	// ceil(1,000,000 x paid / base) — at least the 25% the plasma rose by.
	if want := big.NewInt(1_250_000); got.Amount.Cmp(want) < 0 {
		t.Errorf("replacement burned %s, want at least %s", got.Amount, want)
	}

	time.Sleep(15 * time.Second) // one more momentum for the loser to have had its chance
	if old, err := in.client.AccountBlockByHash(ctx, first); err == nil && old != nil &&
		old.ConfirmationDetail != nil && old.ConfirmationDetail.MomentumHeight > 0 {
		t.Fatalf("the replaced block %s confirmed too, at momentum %d", first, old.ConfirmationDetail.MomentumHeight)
	}
}
