package main

import (
	"context"
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

// fakeToken answers the one node call a price needs.
type fakeToken struct {
	symbol   string
	decimals uint8
}

func (f fakeToken) TokenByZTS(context.Context, types.ZenonTokenStandard) (*rpcclient.Token, error) {
	return &rpcclient.Token{Symbol: f.symbol, Decimals: f.decimals}, nil
}

// A price is quoted in whole tokens and moved in the smallest unit. Getting
// the conversion wrong is not cosmetic: it charges a thousand times too much
// or lets every mint through for nothing.
func TestMintPriceScalesByTheTokensDecimals(t *testing.T) {
	cfg, err := newCreditConfig(context.Background(), fakeToken{"PP", 8}, types.ZnnTokenStandard, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Int).SetString("100000000000", 10) // 1000 * 10^8
	if cfg.MintCost.Cmp(want) != 0 {
		t.Fatalf("mint cost = %s, want %s", cfg.MintCost, want)
	}
	if cfg.MintCostDisplay != "1000" {
		t.Errorf("display price = %q, want the number the operator typed", cfg.MintCostDisplay)
	}

	// A token with no decimals must not be scaled at all.
	plain, err := newCreditConfig(context.Background(), fakeToken{"PP", 0}, types.ZnnTokenStandard, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if plain.MintCost.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("mint cost = %s for a 0-decimal token, want 1000", plain.MintCost)
	}
}

func TestBalanceFormatting(t *testing.T) {
	cfg := &creditConfig{Symbol: "PP", Decimals: 8}
	for raw, want := range map[string]string{
		"0":            "0",
		"100000000":    "1",
		"250000000000": "2500",
		"150000000":    "1.5",
		"1":            "0.00000001",
	} {
		v, _ := new(big.Int).SetString(raw, 10)
		if got := cfg.format(v); got != want {
			t.Errorf("format(%s) = %q, want %q", raw, got, want)
		}
	}
	if got := (&creditConfig{Decimals: 0}).format(big.NewInt(42)); got != "42" {
		t.Errorf("a 0-decimal amount formatted as %q", got)
	}
	if got := cfg.format(nil); got != "0" {
		t.Errorf("a missing balance formatted as %q, want 0", got)
	}
}

// With no credit gate configured, nothing about /create changes: the dev
// instance must not start asking people to link an address.
func TestNoCreditGateLetsEveryoneMint(t *testing.T) {
	b := &bot{}
	if why, ok := b.canAfford(1); !ok {
		t.Fatalf("an ungated bot refused a mint: %s", why)
	}
	b.charge(1, "asset") // must not panic with no store
}
