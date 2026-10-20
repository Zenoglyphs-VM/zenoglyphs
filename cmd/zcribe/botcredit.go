package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/botstore"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/identity"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

// On-chain identity, and paying for mints with it.
//
// Two features, one mechanism, and the order matters: the credit ledger is
// only possible because a deposit can be attributed to a person. A payment
// names its sender's address and nothing else — no Telegram account, no
// memo — so without a proof that someone controls that address, the bot's
// only options are to credit nobody or to credit whoever asks. That is why
// /link comes first and /balance depends on it.
//
// # Deposits
//
// On NoM a send is final for the sender the moment it confirms, but the value
// only lands in the recipient's balance once the recipient publishes a receive
// block. The watcher below uses that gap deliberately: it receives a deposit
// only once it has credited it to somebody. A payment from an address nobody
// has linked is left unreceived, which means the sender can link the address
// afterwards and be credited, or — if they never do — the money is still
// theirs to claim back in the sense that this operator never took delivery of
// it. Receiving first and asking questions later would turn every
// unattributable payment into the operator's money.
//
// # Prices
//
// The mint price is quoted in whole PP and held in the smallest unit, because
// that is what the chain moves. The conversion needs the token's decimals,
// which the node knows, so the bot asks once at startup rather than carrying
// a constant that is wrong the day the token is reissued.

// creditConfig is the whole prod-only surface. A dev instance leaves ZTS
// unset, and every path below turns into a no-op — no gating, no watcher, no
// second database.
type creditConfig struct {
	ZTS      types.ZenonTokenStandard
	Symbol   string
	Decimals uint8
	// MintCost is what one /create costs, in the smallest unit.
	MintCost *big.Int
	// MintCostDisplay is the same number as the operator typed it, for
	// messages: "1000 PP" rather than "100000000000 PP".
	MintCostDisplay string
}

// creditsEnabled reports whether this instance gates minting on a balance.
func (b *bot) creditsEnabled() bool { return b.credit != nil && b.store != nil }

// --- /link, /verify, /whoami, /unlink ---

func (b *bot) cmdLink(ctx context.Context, u tgUpdate, arg string) {
	chat, user := u.Message.Chat.ID, u.Message.From.ID
	if b.store == nil {
		b.say(ctx, chat, "address linking is not enabled on this bot.")
		return
	}
	// A link is between one person and one address, and the challenge is
	// bound to the Telegram account it was issued to. Issuing one in a group
	// would put a proof-of-control flow in front of an audience that can
	// screenshot it, so it is private chats only.
	if u.Message.Chat.Type != "private" {
		b.say(ctx, chat, "message me privately to link an address.")
		return
	}

	addr := strings.TrimSpace(arg)
	if addr == "" {
		b.say(ctx, chat, "usage: <code>/link &lt;z1…&gt;</code>")
		return
	}
	parsed, err := types.ParseAddress(addr)
	if err != nil {
		b.say(ctx, chat, "that is not a Zenon address.")
		return
	}
	addr = parsed.String()

	nonce, err := identity.NewNonce()
	if err != nil {
		b.say(ctx, chat, "could not generate a challenge; try again.")
		return
	}
	expires := time.Now().Add(identity.ChallengeTTL)
	if err := b.store.PutChallenge(botstore.Challenge{
		TelegramID: user, Address: addr, Nonce: nonce,
		IssuedAt: time.Now().Unix(), ExpiresAt: expires.Unix(),
	}); err != nil {
		b.log.Info("could not store a challenge", "err", err)
		b.say(ctx, chat, "could not start that; try again.")
		return
	}

	msg := identity.Message(user, addr, nonce, expires)
	b.say(ctx, chat, fmt.Sprintf(
		"<b>prove you hold %s</b>\n\nSign this exact text with that address's key:\n\n<pre>%s</pre>\n\n"+
			"With the CLI:\n<pre>zcribe sign -keystore &lt;file&gt;</pre>\n"+
			"(paste the text above on stdin — it prints one line to send back)\n\n"+
			"Then: <code>/verify &lt;public key&gt;.&lt;signature&gt;</code>\n\n"+
			"<i>Expires in %d minutes. Signing this moves nothing and costs no plasma — "+
			"it is a statement, not a transaction.</i>",
		party(addr), esc(msg), int(identity.ChallengeTTL.Minutes())))
}

func (b *bot) cmdVerify(ctx context.Context, u tgUpdate, arg string) {
	chat, user := u.Message.Chat.ID, u.Message.From.ID
	if b.store == nil {
		b.say(ctx, chat, "address linking is not enabled on this bot.")
		return
	}

	challenge, err := b.store.Challenge(user)
	if err != nil {
		b.log.Info("could not read a challenge", "err", err)
		b.say(ctx, chat, "could not check that; try again.")
		return
	}
	if challenge == nil {
		b.say(ctx, chat, "no challenge outstanding — start with <code>/link &lt;z1…&gt;</code>.\n"+
			"(they expire after "+fmt.Sprint(int(identity.ChallengeTTL.Minutes()))+" minutes)")
		return
	}

	proof, err := identity.ParseProof(arg)
	if err != nil {
		b.say(ctx, chat, "usage: <code>/verify &lt;public key&gt;.&lt;signature&gt;</code>")
		return
	}
	msg := identity.Message(user, challenge.Address, challenge.Nonce,
		time.Unix(challenge.ExpiresAt, 0))
	if err := identity.Verify(msg, challenge.Address, proof.PublicKey, proof.Signature); err != nil {
		// The reason is safe to repeat: it says what did not match, never
		// anything the sender did not already send.
		b.say(ctx, chat, "that proof does not check out: "+esc(err.Error()))
		return
	}

	err = b.store.Link(botstore.Identity{
		TelegramID: user, Username: u.Message.From.Username,
		Address: challenge.Address, PublicKey: proof.PublicKey,
		VerifiedAt: time.Now().Unix(),
	})
	if errors.Is(err, botstore.ErrAddressTaken) {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	if err != nil {
		b.log.Info("could not store an identity", "err", err)
		b.say(ctx, chat, "verified, but could not save it; try again.")
		return
	}

	reply := "✓ linked " + party(challenge.Address) + "."
	if b.creditsEnabled() {
		reply += "\n\nDeposits from that address now credit your balance — /balance."
		// A deposit that arrived before the link is still sitting unreceived
		// and is now attributable, so sweep immediately rather than making
		// them wait for the next poll.
		go b.sweepDeposits(ctx)
	}
	b.say(ctx, chat, reply)
}

func (b *bot) cmdWhoami(ctx context.Context, u tgUpdate) {
	chat, user := u.Message.Chat.ID, u.Message.From.ID
	if b.store == nil {
		b.say(ctx, chat, "address linking is not enabled on this bot.")
		return
	}
	id, err := b.store.Identity(user)
	if err != nil || id == nil {
		b.say(ctx, chat, "no address linked. <code>/link &lt;z1…&gt;</code> to prove one.")
		return
	}
	b.say(ctx, chat, fmt.Sprintf("you are %s\n<i>verified %s</i>",
		party(id.Address), esc(stamp(id.VerifiedAt))))
}

func (b *bot) cmdUnlink(ctx context.Context, u tgUpdate) {
	chat, user := u.Message.Chat.ID, u.Message.From.ID
	if b.store == nil {
		b.say(ctx, chat, "address linking is not enabled on this bot.")
		return
	}
	if err := b.store.Unlink(user); err != nil {
		b.say(ctx, chat, "could not unlink; try again.")
		return
	}
	// The balance is deliberately kept. It was paid for, and unlinking is a
	// statement about which address speaks for this account, not a refund.
	b.say(ctx, chat, "unlinked. Any balance stays with this Telegram account; "+
		"future deposits need a linked address again.")
}

// --- /balance ---

func (b *bot) cmdBalance(ctx context.Context, u tgUpdate) {
	chat, user := u.Message.Chat.ID, u.Message.From.ID
	if !b.creditsEnabled() {
		b.say(ctx, chat, "this bot does not charge for mints.")
		return
	}

	balance, err := b.store.Balance(user)
	if err != nil {
		b.say(ctx, chat, "could not read your balance; try again.")
		return
	}
	id, _ := b.store.Identity(user)

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s %s</b>\n%s per mint\n\n",
		esc(b.credit.format(balance)), esc(b.credit.Symbol),
		esc(b.credit.MintCostDisplay+" "+b.credit.Symbol))

	mints := new(big.Int).Div(balance, b.credit.MintCost)
	switch {
	case mints.Sign() > 0:
		fmt.Fprintf(&sb, "that is %s mint(s).\n\n", mints)
	default:
		sb.WriteString("not enough for a mint yet.\n\n")
	}

	if id == nil {
		fmt.Fprintf(&sb, "<b>to top up</b>\nFirst link the address you will pay from:\n"+
			"<code>/link &lt;z1…&gt;</code>\n\n<i>A deposit from an address nobody has linked is "+
			"left unreceived rather than kept — this bot cannot tell whose it is.</i>")
	} else {
		fmt.Fprintf(&sb, "<b>to top up</b>\nSend %s from %s to:\n<code>%s</code>",
			esc(b.credit.Symbol), party(id.Address), esc(b.in.Address().String()))
	}
	b.say(ctx, chat, sb.String())
}

// charge takes the mint price off a user's balance, after the mint landed.
//
// The order is the whole design. A block that failed to publish has cost the
// operator nothing and must cost the user nothing; a block that published
// cannot be un-published, so the debit cannot be refused at that point. It is
// therefore checked before (canAfford) and applied after, and the gap between
// them is one mint's worth of credit that a user racing two /create commands
// could spend twice — which the per-user cooldown already prevents, and which
// is a cheaper failure than charging for a mint that never happened.
func (b *bot) charge(user int64, ref string) {
	if !b.creditsEnabled() {
		return
	}
	if _, err := b.store.Spend(user, b.credit.MintCost, "mint", ref); err != nil {
		// Nothing to do but record it: the inscription exists.
		b.log.Info("could not charge for a mint", "user", user, "ref", ref, "err", err)
	}
}

// canAfford reports whether a user may mint, and why not when they may not.
func (b *bot) canAfford(user int64) (string, bool) {
	if !b.creditsEnabled() {
		return "", true
	}
	id, err := b.store.Identity(user)
	if err != nil {
		return "could not check your balance; try again.", false
	}
	if id == nil {
		return "minting on this bot costs " + esc(b.credit.MintCostDisplay+" "+b.credit.Symbol) +
			".\n\nLink the address you pay from first: <code>/link &lt;z1…&gt;</code>", false
	}
	balance, err := b.store.Balance(user)
	if err != nil {
		return "could not check your balance; try again.", false
	}
	if balance.Cmp(b.credit.MintCost) < 0 {
		return fmt.Sprintf("not enough %s: you have %s, a mint costs %s.\n\nTop up — /balance",
			esc(b.credit.Symbol), esc(b.credit.format(balance)),
			esc(b.credit.MintCostDisplay)), false
	}
	return "", true
}

// --- the deposit watcher ---

// watchDeposits credits payments as they arrive.
//
// Polling rather than subscribing: the node's pub-sub is another connection to
// keep alive for a signal that arrives minutes apart, and an unreceived block
// waits indefinitely, so a missed poll costs latency and never a payment.
func (b *bot) watchDeposits(ctx context.Context) {
	const interval = 20 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		b.sweepDeposits(ctx)
	}
}

// sweepDeposits credits and then receives every attributable payment.
func (b *bot) sweepDeposits(ctx context.Context) {
	if !b.creditsEnabled() || b.in == nil {
		return
	}
	blocks, err := b.node.UnreceivedBlocks(ctx, b.in.Address(), 0, 50)
	if err != nil {
		b.log.Info("could not read unreceived blocks", "err", err)
		return
	}
	for _, blk := range blocks {
		if blk == nil || blk.TokenStandard != b.credit.ZTS || blk.Amount.Int == nil ||
			blk.Amount.Sign() <= 0 {
			continue
		}
		sender := blk.Address.String()
		id, err := b.store.IdentityByAddress(sender)
		if err != nil {
			b.log.Info("could not look up a depositor", "address", sender, "err", err)
			continue
		}
		if id == nil {
			// Not ours to take. Left unreceived on purpose — see the file
			// comment — so it can still be credited if they link later.
			b.log.Info("unattributable deposit left unreceived",
				"from", sender, "amount", blk.Amount, "block", blk.Hash)
			continue
		}

		hash := blk.Hash.String()
		balance, err := b.store.Credit(id.TelegramID, blk.Amount.Int, "deposit", hash)
		switch {
		case errors.Is(err, botstore.ErrAlreadyApplied):
			// Credited on an earlier pass; the receive below is what did not
			// finish, so fall through to retry exactly that.
			balance, err = b.store.Balance(id.TelegramID)
			if err != nil {
				continue
			}
		case err != nil:
			b.log.Info("could not credit a deposit", "user", id.TelegramID, "err", err)
			continue
		default:
			b.say(ctx, id.TelegramID, fmt.Sprintf(
				"✓ received %s %s\nbalance: <b>%s %s</b> — /balance",
				esc(b.credit.format(blk.Amount.Int)), esc(b.credit.Symbol),
				esc(b.credit.format(balance)), esc(b.credit.Symbol)))
		}
		_ = balance

		// Take delivery. This is the only point at which the value becomes
		// the operator's, and it happens strictly after the credit is
		// durable: a crash between the two leaves a credited deposit that is
		// received on the next sweep, which is the harmless direction.
		b.inMu.Lock()
		_, rerr := b.in.Receive(ctx, blk.Hash)
		b.inMu.Unlock()
		if rerr != nil {
			b.log.Info("could not receive a credited deposit; will retry",
				"block", hash, "err", rerr)
		}
	}
}

// format renders a smallest-unit amount in whole tokens, which is the only
// form anybody quotes a price in.
func (c *creditConfig) format(v *big.Int) string {
	if v == nil {
		return "0"
	}
	if c.Decimals == 0 {
		return v.String()
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.Decimals)), nil)
	whole, rest := new(big.Int).QuoRem(v, scale, new(big.Int))
	if rest.Sign() == 0 {
		return whole.String()
	}
	// Trailing zeros in the fraction carry no information at a glance.
	frac := strings.TrimRight(fmt.Sprintf("%0*s", int(c.Decimals), rest.String()), "0")
	return whole.String() + "." + frac
}

// newCreditConfig resolves a price quoted in whole tokens against the token's
// own decimals, which only the node knows.
func newCreditConfig(ctx context.Context, node tokenLookup, zts types.ZenonTokenStandard, whole uint64) (*creditConfig, error) {
	tok, err := node.TokenByZTS(ctx, zts)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", zts, err)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tok.Decimals)), nil)
	return &creditConfig{
		ZTS: zts, Symbol: tok.Symbol, Decimals: tok.Decimals,
		MintCost:        new(big.Int).Mul(new(big.Int).SetUint64(whole), scale),
		MintCostDisplay: fmt.Sprint(whole),
	}, nil
}

// tokenLookup is the one node call newCreditConfig needs, named so a test can
// answer it without a chain.
type tokenLookup interface {
	TokenByZTS(ctx context.Context, zts types.ZenonTokenStandard) (*rpcclient.Token, error)
}
