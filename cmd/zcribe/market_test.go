package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// party is the one place the bot decides what a balance row's address column
// actually names. Getting it wrong is not cosmetic: a dispenser's escrow
// printed as a plain address reads as a whale nobody can identify, and the
// burn address printed as a plain address reads as a holder who will never
// sell.
func TestPartyDistinguishesEscrowAndBurnFromWallets(t *testing.T) {
	escrow := "9f2c" + strings.Repeat("0", 60) // a dispenser id, as the indexer parks escrow under

	if got := party(escrow); !strings.Contains(got, "escrow") {
		t.Errorf("party(dispenser id) = %q, want it labelled as escrow", got)
	}
	if got := party(protocol.BurnAddress.String()); !strings.Contains(got, "burn") {
		t.Errorf("party(burn address) = %q, want it labelled as the burn address", got)
	}

	// An ordinary wallet must not be mistaken for either, and must survive
	// escaping intact enough to still be recognisable.
	addr := "z1qzalph0000000000000000000000000000000000000000000000000000"
	got := party(addr)
	if strings.Contains(got, "escrow") || strings.Contains(got, "burn") {
		t.Errorf("party(wallet) = %q, want a plain address", got)
	}
	if !strings.Contains(got, addr[:10]) {
		t.Errorf("party(wallet) = %q, want it to keep the %q prefix", got, addr[:10])
	}
}

func TestIsHash64RejectsAddresses(t *testing.T) {
	for _, s := range []string{
		"",
		"z1qzalph0000000000000000000000000000000000000000000000000000",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("g", 64), // right length, not hex
	} {
		if isHash64(s) {
			t.Errorf("isHash64(%q) = true, want false", truncate(s, 20))
		}
	}
	if !isHash64(strings.Repeat("0123456789abcdef", 4)) {
		t.Error("isHash64 rejected a 64-character hex string")
	}
}

// Telegram rejects the entire sendMessage when any callback_data exceeds 64
// bytes, so an over-long payload costs the whole card, not just its button.
// keyboard drops the button instead; shortRef is what keeps that from
// happening in the first place.
func TestKeyboardDropsOverlongCallbackData(t *testing.T) {
	markup := keyboard([]button{
		{Text: "fits", Data: "asset:12"},
		{Text: "too long", Data: "asset:" + strings.Repeat("a", 64)},
	})

	var decoded struct {
		Rows [][]button `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(markup), &decoded); err != nil {
		t.Fatalf("keyboard produced invalid JSON: %v", err)
	}
	if len(decoded.Rows) != 1 || len(decoded.Rows[0]) != 1 {
		t.Fatalf("got %v, want exactly the one button that fits", decoded.Rows)
	}
	if decoded.Rows[0][0].Text != "fits" {
		t.Errorf("kept %q, want the button under the limit", decoded.Rows[0][0].Text)
	}

	// A row left empty by the drop must vanish rather than ship as [].
	if markup := keyboard([]button{{Text: "x", Data: strings.Repeat("a", 65)}}); markup != "" {
		t.Errorf("keyboard with nothing left = %q, want no markup at all", markup)
	}
}

func TestShortRefRoundTripsWithinTheCallbackBudget(t *testing.T) {
	b := &bot{}
	id := strings.Repeat("0123456789abcdef", 4)

	token := b.shortRef(id)
	if len("history:"+token) > callbackDataMax {
		t.Errorf("history:%s is %d bytes, over the %d-byte limit",
			token, len("history:"+token), callbackDataMax)
	}
	if got := b.longRef(token); got != id {
		t.Errorf("longRef(%q) = %q, want the full id back", token, got)
	}

	// An inscription number is already short and must pass through both ways
	// untouched, since every endpoint accepts it in place of an id.
	if got := b.shortRef("37"); got != "37" {
		t.Errorf("shortRef(%q) = %q, want it unchanged", "37", got)
	}
	if got := b.longRef("37"); got != "37" {
		t.Errorf("longRef(%q) = %q, want it unchanged", "37", got)
	}
}

// The table is cleared rather than grown without bound. What must not happen
// is that a clear leaves a token resolving to the *wrong* id.
func TestShortRefClearsRatherThanGrowingForever(t *testing.T) {
	b := &bot{}
	first := strings.Repeat("a", 64)
	firstToken := b.shortRef(first)

	for i := 0; i < shortRefCap+1; i++ {
		b.shortRef(strings.Repeat("b", 48) + pad16(i))
	}

	if got := b.longRef(firstToken); got != first && got != firstToken {
		t.Errorf("longRef after overflow = %q, want the original id or the token itself", got)
	}
	if len(b.refs) > shortRefCap {
		t.Errorf("table holds %d entries, over the %d cap", len(b.refs), shortRefCap)
	}
}

// pad16 renders i as 16 hex characters, so each generated id is distinct and
// still 64 characters long.
func pad16(i int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 16)
	for j := 15; j >= 0; j-- {
		out[j] = hex[i&0xf]
		i >>= 4
	}
	return string(out)
}

// tradeText is the answer to /burn and friends. It has one job beyond being
// polite: say that the bot has no key of its own to sign with, so nobody
// reads the refusal as a permission that could be granted.
func TestTradeTextExplainsTheCustodyModel(t *testing.T) {
	for _, cmd := range []string{"/send", "/transfer", "/burn", "/sell", "/buy", "/cancel"} {
		got := (&bot{}).tradeText(cmd)
		if !strings.Contains(got, "one key") {
			t.Errorf("tradeText(%s) does not mention the shared key: %q", cmd, got)
		}
		if !strings.Contains(got, "zcribe transfer") {
			t.Errorf("tradeText(%s) does not point at the CLI: %q", cmd, got)
		}
	}
}
