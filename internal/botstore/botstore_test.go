package botstore

import (
	"errors"
	"math/big"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func amount(n int64) *big.Int { return big.NewInt(n) }

func TestLinkAndLookUpAnIdentity(t *testing.T) {
	s := newStore(t)
	id := Identity{TelegramID: 7, Username: "someone", Address: "z1alice",
		PublicKey: "aa", VerifiedAt: 1000}
	if err := s.Link(id); err != nil {
		t.Fatal(err)
	}

	got, err := s.Identity(7)
	if err != nil || got == nil {
		t.Fatalf("Identity: %v %v", got, err)
	}
	if got.Address != "z1alice" {
		t.Errorf("address = %q", got.Address)
	}
	// The deposit watcher's direction: a payment names an address, and this
	// is what turns it into a person.
	byAddr, err := s.IdentityByAddress("z1alice")
	if err != nil || byAddr == nil || byAddr.TelegramID != 7 {
		t.Fatalf("IdentityByAddress = %v, %v", byAddr, err)
	}

	if err := s.Unlink(7); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Identity(7); got != nil {
		t.Error("identity survived an unlink")
	}
}

// A deposit can only be credited to one account, so the second claim on an
// address has to be refused rather than silently taking the first one's place.
func TestOneAddressCannotBeClaimedTwice(t *testing.T) {
	s := newStore(t)
	if err := s.Link(Identity{TelegramID: 1, Address: "z1shared", PublicKey: "aa"}); err != nil {
		t.Fatal(err)
	}
	err := s.Link(Identity{TelegramID: 2, Address: "z1shared", PublicKey: "aa"})
	if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("second claim returned %v, want ErrAddressTaken", err)
	}
	// Re-proving your own address is not a conflict: keys get re-derived,
	// challenges get answered twice.
	if err := s.Link(Identity{TelegramID: 1, Address: "z1shared", PublicKey: "bb"}); err != nil {
		t.Fatalf("relinking my own address failed: %v", err)
	}
}

// Verifying consumes the challenge. Leaving it behind would let a second
// proof be replayed against a nonce the user has already answered.
func TestLinkClearsTheChallenge(t *testing.T) {
	s := newStore(t)
	if err := s.PutChallenge(Challenge{
		TelegramID: 5, Address: "z1a", Nonce: "ff",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Challenge(5); c == nil {
		t.Fatal("challenge not stored")
	}
	if err := s.Link(Identity{TelegramID: 5, Address: "z1a", PublicKey: "aa"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Challenge(5); c != nil {
		t.Error("challenge survived a successful link")
	}
}

func TestExpiredChallengeReadsAsAbsent(t *testing.T) {
	s := newStore(t)
	if err := s.PutChallenge(Challenge{
		TelegramID: 9, Address: "z1a", Nonce: "ff",
		IssuedAt: 0, ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Challenge(9)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Fatal("an expired challenge was returned as usable")
	}
}

func TestCreditAndSpend(t *testing.T) {
	s := newStore(t)
	if b, _ := s.Balance(1); b.Sign() != 0 {
		t.Fatalf("a new account starts at %s, want 0", b)
	}

	if _, err := s.Credit(1, amount(2500), "deposit", "block-a"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Spend(1, amount(1000), "mint", "asset-a")
	if err != nil {
		t.Fatal(err)
	}
	if after.Cmp(amount(1500)) != 0 {
		t.Fatalf("balance after one mint = %s, want 1500", after)
	}

	// Two mints' worth of credit does not buy three.
	if _, err := s.Spend(1, amount(1000), "mint", "asset-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Spend(1, amount(1000), "mint", "asset-c"); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("overspend returned %v, want ErrInsufficient", err)
	}
	if b, _ := s.Balance(1); b.Cmp(amount(500)) != 0 {
		t.Fatalf("a refused spend moved the balance to %s, want 500", b)
	}
}

// The watcher re-reads the same unreceived block on every poll until the
// receive lands. Crediting it twice would mint credit out of one payment.
func TestDepositsAreIdempotent(t *testing.T) {
	s := newStore(t)
	if _, err := s.Credit(3, amount(1000), "deposit", "block-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Credit(3, amount(1000), "deposit", "block-x"); !errors.Is(err, ErrAlreadyApplied) {
		t.Fatalf("second credit of the same block returned %v, want ErrAlreadyApplied", err)
	}
	if b, _ := s.Balance(3); b.Cmp(amount(1000)) != 0 {
		t.Fatalf("balance = %s after a repeated deposit, want 1000", b)
	}
	// A different block from the same sender is a different deposit.
	if _, err := s.Credit(3, amount(1000), "deposit", "block-y"); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Balance(3); b.Cmp(amount(2000)) != 0 {
		t.Fatalf("balance = %s, want 2000", b)
	}
}

// A balance has to be explainable, not just asserted: the ledger is what an
// operator reads when someone says the number is wrong.
func TestLedgerExplainsTheBalance(t *testing.T) {
	s := newStore(t)
	if _, err := s.Credit(4, amount(3000), "deposit", "block-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Spend(4, amount(1000), "mint", "asset-1"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Ledger(4, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d ledger rows, want 2", len(rows))
	}
	if rows[0].Reason != "mint" || rows[0].Delta.Sign() >= 0 {
		t.Errorf("newest row = %+v, want a negative mint", rows[0])
	}
	total := new(big.Int)
	for _, r := range rows {
		total.Add(total, r.Delta)
	}
	balance, _ := s.Balance(4)
	if total.Cmp(balance) != 0 {
		t.Fatalf("ledger sums to %s but the balance is %s", total, balance)
	}
}

// Token amounts are in the smallest unit and PP has decimals, so a balance
// can exceed what an int64 column would hold. The store keeps them as text
// for exactly this reason.
func TestBalancesSurviveAmountsBeyondInt64(t *testing.T) {
	s := newStore(t)
	huge, _ := new(big.Int).SetString("340282366920938463463374607431768211456", 10) // 2^128
	if _, err := s.Credit(8, huge, "deposit", "block-huge"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Balance(8)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(huge) != 0 {
		t.Fatalf("balance came back as %s, want %s", got, huge)
	}
}
