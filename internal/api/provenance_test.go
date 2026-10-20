package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// A dispenser, its escrow, one sale and a cancel — the whole life of a
// listing, laid down the way the indexer lays it down: balances move, but
// only the sale writes a transfer row.
func seedDispenser(t *testing.T, st *store.Store, a store.Asset, cancelled bool) store.Dispenser {
	t.Helper()

	const escrowed, sold = 5, 2
	d := store.Dispenser{
		ID:      strings.Repeat("d1", 32),
		Issuer:  a.Owner,
		AssetID: a.ID, Remaining: escrowed - sold, Price: 10,
		ZTS: hex.EncodeToString(make([]byte, protocol.ZTSLen)), Cancelled: cancelled,
		Height: a.Height + 1, MomentumIndex: 0, Timestamp: a.Timestamp + 10,
	}

	// The createDispenser block, in the log: the only surviving record of how
	// much was escrowed once `remaining` has counted down.
	payload, err := protocol.Marshal(protocol.DispenserWire{
		Asset:  mustHex(t, a.ID),
		Amount: escrowed, Price: d.Price, ZTS: make([]byte, protocol.ZTSLen),
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(t, st, store.Event{
		Height: d.Height, MomentumIndex: d.MomentumIndex, BlockHash: d.ID,
		Opcode: uint64(protocol.OpCreateDispenser), Version: protocol.Version,
		Sender: d.Issuer, Recipient: d.Issuer, Payload: payload,
	})
	if err := st.PutDispenser(d); err != nil {
		t.Fatal(err)
	}
	if err := st.Move(a.ID, a.Owner, d.ID, escrowed); err != nil {
		t.Fatal(err)
	}

	// One fill. This is the only leg the indexer records as a transfer.
	if err := st.Move(a.ID, d.ID, "z1buyer", sold); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTransfer(store.Transfer{
		Height: d.Height + 1, MomentumIndex: 0, BlockHash: strings.Repeat("f1", 32),
		AssetID: a.ID, Sender: d.ID, Recipient: "z1buyer", Amount: sold,
		Timestamp: d.Timestamp + 10,
	}); err != nil {
		t.Fatal(err)
	}

	if cancelled {
		ref, err := protocol.Marshal(protocol.DispenserRefWire{Dispenser: mustHex(t, d.ID)})
		if err != nil {
			t.Fatal(err)
		}
		appendEvent(t, st, store.Event{
			Height: d.Height + 2, MomentumIndex: 0, BlockHash: strings.Repeat("c1", 32),
			Opcode: uint64(protocol.OpCancelDispenser), Version: protocol.Version,
			Sender: d.Issuer, Recipient: d.Issuer, Payload: ref,
		})
		if err := st.Move(a.ID, d.ID, d.Issuer, escrowed-sold); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateDispenser(d.ID, 0, true); err != nil {
			t.Fatal(err)
		}
		d.Remaining, d.Cancelled = 0, true
	}
	return d
}

func appendEvent(t *testing.T, st *store.Store, e store.Event) {
	t.Helper()
	e.Amount = "0"
	if err := st.AppendEvent(e); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEventApplied(e.Height, e.MomentumIndex, true); err != nil {
		t.Fatal(err)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func transfersOf(t *testing.T, h http.Handler, id string) []transferJSON {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/asset/"+id+"/transfers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("transfers status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Transfers []transferJSON `json:"transfers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Transfers
}

func rowOfKind(rows []transferJSON, kind string) *transferJSON {
	for i := range rows {
		if rows[i].Kind == kind {
			return &rows[i]
		}
	}
	return nil
}

// The oldest row in the feed is where the supply came from. Without it the
// history of an asset starts at its first move, which reads as if the units
// arrived from nowhere.
func TestTransferFeedCarriesIssuance(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)

	rows := transfersOf(t, s.Routes(nil), a.ID)
	if len(rows) != 1 {
		t.Fatalf("feed has %d rows, want the mint alone: %+v", len(rows), rows)
	}
	mint := rows[0]
	if mint.Kind != kindMint {
		t.Fatalf("kind = %q, want %q", mint.Kind, kindMint)
	}
	if mint.From != a.Issuer || mint.To != a.Owner {
		t.Errorf("mint row %s -> %s, want %s -> %s", mint.From, mint.To, a.Issuer, a.Owner)
	}
	if mint.Amount != "7" {
		t.Errorf("mint amount = %s, want the supply (7)", mint.Amount)
	}
	if mint.Block != a.ID {
		t.Errorf("mint block = %s, want the mint block itself (%s)", mint.Block, a.ID)
	}
	if !mint.Synthetic {
		t.Error("the mint row is reconstructed at serve time and must say so")
	}
}

// A purchase used to appear as a transfer out of an address that had never
// received anything. Both escrow legs are reconstructed so the units can be
// followed from the seller, into the listing, and out to the buyer.
func TestTransferFeedExplainsEscrow(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	d := seedDispenser(t, st, a, false)

	rows := transfersOf(t, s.Routes(nil), a.ID)
	escrowAddr := protocol.EscrowAddress(d.ID)

	in := rowOfKind(rows, kindEscrow)
	if in == nil {
		t.Fatalf("no escrow row in %+v", rows)
	}
	if in.From != a.Owner || in.To != escrowAddr {
		t.Errorf("escrow row %s -> %s, want %s -> %s", in.From, in.To, a.Owner, escrowAddr)
	}
	if in.Amount != "5" {
		t.Errorf("escrow amount = %s, want the whole 5 units the dispenser locked", in.Amount)
	}
	if in.ToDispenser != d.ID {
		t.Errorf("escrow row does not name the dispenser: %q", in.ToDispenser)
	}

	sale := rowOfKind(rows, kindSale)
	if sale == nil {
		t.Fatalf("no sale row in %+v", rows)
	}
	if sale.From != escrowAddr {
		t.Errorf("sale from %s, want the escrow address %s", sale.From, escrowAddr)
	}
	if sale.FromDispenser != d.ID {
		t.Errorf("sale row does not name the dispenser: %q", sale.FromDispenser)
	}
	if sale.Synthetic {
		t.Error("a fill is a recorded transfer, not a reconstructed one")
	}

	// Newest first, and every party accounted for.
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Height < rows[i].Height {
			t.Fatalf("feed is not newest-first: %+v", rows)
		}
	}
}

// Cancelling releases whatever did not sell. The amount is not stored
// anywhere — `remaining` is zeroed by the cancel — so it is recomputed from
// what was escrowed minus what left.
func TestTransferFeedExplainsCancel(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	d := seedDispenser(t, st, a, true)

	rows := transfersOf(t, s.Routes(nil), a.ID)
	out := rowOfKind(rows, kindRelease)
	if out == nil {
		t.Fatalf("no release row in %+v", rows)
	}
	if out.Amount != "3" {
		t.Errorf("released %s units, want 3 (5 escrowed, 2 sold)", out.Amount)
	}
	if out.From != protocol.EscrowAddress(d.ID) || out.To != d.Issuer {
		t.Errorf("release row %s -> %s, want escrow -> %s", out.From, out.To, d.Issuer)
	}
	if out.Block == "" || out.Block == d.ID {
		t.Errorf("release row should cite the cancel block, got %q", out.Block)
	}
	// Balances must still add up to the supply after the round trip.
	if bal, _ := st.Balance(a.ID, a.Owner); bal != 5 {
		t.Errorf("issuer holds %d, want 5 back after the cancel", bal)
	}
}

// Escrow is a pseudo-address, not an account. It must never be rendered as a
// bare block hash in a column of addresses — that is what made a dispenser
// look like an anonymous whale in the holders table.
func TestHoldersRenderEscrowInTheNamespace(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	d := seedDispenser(t, st, a, false)

	rec := do(t, s.Routes(nil), http.MethodGet, "/asset/"+a.ID+"/holders", nil)
	var got struct {
		Holders []struct {
			Address   string `json:"address"`
			Amount    string `json:"amount"`
			Dispenser string `json:"dispenser"`
		} `json:"holders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range got.Holders {
		if h.Address == d.ID {
			t.Fatalf("holders list still shows the raw dispenser id as an address")
		}
		if h.Dispenser == d.ID {
			found = true
			if !protocol.IsEscrowAddress(h.Address) {
				t.Errorf("escrow rendered as %q, want a %s… address", h.Address, protocol.EscrowPrefix)
			}
			if h.Amount != "3" {
				t.Errorf("escrow holds %s, want 3 unsold units", h.Amount)
			}
		}
	}
	if !found {
		t.Errorf("escrow missing from the holders list: %+v", got.Holders)
	}
}

// An escrow address is rendered wherever an account is rendered, so it has to
// answer where an account answers. It cannot be inverted, so the API matches
// it back to its dispenser — without that, following the link from a holders
// row lands on a page saying the address holds nothing.
func TestAddressLookupResolvesEscrow(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	d := seedDispenser(t, st, a, false)

	rec := do(t, s.Routes(nil), http.MethodGet,
		"/address/"+protocol.EscrowAddress(d.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		Address   string `json:"address"`
		Dispenser string `json:"dispenser"`
		Holdings  []struct {
			Amount string `json:"amount"`
		} `json:"holdings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Dispenser != d.ID {
		t.Errorf("dispenser = %q, want %s", got.Dispenser, d.ID)
	}
	if len(got.Holdings) != 1 || got.Holdings[0].Amount != "3" {
		t.Fatalf("escrow holdings = %+v, want 3 unsold units", got.Holdings)
	}
}
