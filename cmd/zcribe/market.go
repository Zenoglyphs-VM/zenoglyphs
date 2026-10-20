package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// The market, provenance, and moderation half of the Telegram front end.
//
// Everything here is a read against the same public HTTP API a third-party
// client would use, with one exception — /block, which authenticates to the
// admin API — that is called out where it happens.
//
// The reason this file exists at all is a gap the read side had: an asset
// card reported `owner`, and `owner` is not what a reader takes it to mean.
// It is written once, at the mint, and never updated by a transfer, so on
// any asset that has moved it names the wrong address with no hint that it
// is stale. /holders and /history are the two answers it was standing in for.

// adminCookieName mirrors the session cookie internal/api sets. It is
// duplicated rather than exported from there on purpose: the bot is a client
// of the HTTP API, and a client that imports the server's internals stops
// being a test of the API it is supposed to exercise.
const adminCookieName = "zenoglyphs_admin"

// --- callbacks: the button half of every card ---

// callbackDataMax is Telegram's hard limit on callback_data. It is the
// single fact that shapes every button in this file: a Zenoglyphs id is
// 64 hex characters, so an id plus any prefix at all is already over
// budget. Nothing can be addressed by raw id from a button.
//
// Two answers, and which one applies depends on what is being addressed:
//
//   - An asset has an inscription number, which is a handful of digits and
//     which every endpoint accepts in place of an id. assetRef uses it.
//   - A dispenser has no number, only its id. shortRef trades that id for a
//     token the bot can trade back — see below.
const callbackDataMax = 64

// shortRef and longRef are a two-way table between a full 64-character id and
// a prefix short enough to travel in callback_data.
//
// A prefix alone would not be enough to look the id back up: the API matches
// dispenser ids exactly, so the bot has to remember the whole thing. It
// learns them for free — it only ever mints a token for an id it is
// rendering right now — and the table is capped, because a bot left running
// for months would otherwise accumulate one entry per listing it ever
// displayed. Clearing it on overflow costs nothing but a stale button, and a
// stale button already has an answer (see handleCallback).
const shortRefCap = 4096

func (b *bot) shortRef(id string) string {
	if len(id) <= 16 {
		return id
	}
	token := id[:16]
	b.refMu.Lock()
	defer b.refMu.Unlock()
	if b.refs == nil || len(b.refs) >= shortRefCap {
		b.refs = map[string]string{}
	}
	b.refs[token] = id
	return token
}

// longRef returns the full id behind a token, or the token unchanged when it
// was never shortened (an inscription number, say) — so callers can pass
// whatever they were handed straight through to an endpoint.
func (b *bot) longRef(token string) string {
	b.refMu.Lock()
	defer b.refMu.Unlock()
	if full, ok := b.refs[token]; ok {
		return full
	}
	return token
}

// handleCallback routes an inline-keyboard tap. The payload grammar is
// "<verb>:<arg>", deliberately tiny — see callbackDataMax.
func (b *bot) handleCallback(ctx context.Context, u tgUpdate) {
	q := u.CallbackQuery
	if q.Message == nil {
		// A callback on a message too old for Telegram to still hand back.
		// There is nowhere to reply, so only the spinner can be cleared.
		b.ack(ctx, q.ID, "that card is too old — run the command again")
		return
	}
	chat := q.Message.Chat.ID

	defer func() {
		if r := recover(); r != nil {
			b.log.Info("recovered from a panic handling a callback", "data", q.Data, "panic", r)
		}
	}()

	verb, token, _ := strings.Cut(q.Data, ":")
	b.ack(ctx, q.ID, "")

	// Every verb but the two paging ones addresses something by id, so the
	// token is expanded once here rather than in each branch.
	arg := token
	switch verb {
	case "asset", "holders", "history", "refs", "disp":
		arg = b.longRef(token)
	}

	switch verb {
	case "asset":
		_ = b.showAssetByRef(ctx, chat, arg)
	case "holders":
		b.cmdHolders(ctx, chat, arg)
	case "history":
		b.cmdHistory(ctx, chat, arg)
	case "refs":
		b.cmdRefs(ctx, chat, arg)
	case "market":
		b.cmdMarket(ctx, chat, arg)
	case "disp":
		b.cmdDispenser(ctx, chat, arg)
	case "recent":
		b.cmdRecent(ctx, chat, arg)
	default:
		b.log.Info("unknown callback", "data", q.Data)
	}
}

// assetKeyboard is the button row every asset card carries. It addresses the
// asset by whatever is shortest and still resolves — the inscription number
// when there is one, the id otherwise (see handleCallback on the 64-byte cap).
func (b *bot) assetKeyboard(a map[string]any) string {
	ref := b.assetRef(a)
	row := []button{
		{Text: "holders", Data: "holders:" + ref},
		{Text: "history", Data: "history:" + ref},
	}
	if refs, ok := a["refs"].([]any); ok && len(refs) > 0 {
		row = append(row, button{Text: "imports", Data: "refs:" + ref})
	}
	return keyboard(row, []button{
		{Text: "open on the site", URL: b.webOrigin + "/#/asset/" + str(a["id"])},
	})
}

// assetRef picks the shortest handle /asset, /holders and the rest will
// still resolve: the inscription number, which is a few digits where an id
// is 64 characters — the entire callback_data budget on its own.
func (b *bot) assetRef(a map[string]any) string {
	if n := num(a["number"]); n > 0 {
		return strconv.FormatInt(n, 10)
	}
	// No number yet — a mint the indexer has seen but not numbered. Fall
	// back to the id, carried as a token so the button still fits.
	return b.shortRef(str(a["id"]))
}

// --- provenance ---

// cmdHolders answers "who has it now" — the question an asset card's owner
// line looks like it answers and does not.
func (b *bot) cmdHolders(ctx context.Context, chat int64, arg string) {
	ref := strings.TrimSpace(arg)
	if ref == "" {
		b.say(ctx, chat, "usage: <code>/holders &lt;id or number&gt;</code>")
		return
	}
	data, err := b.get(ctx, "/asset/"+url.PathEscape(ref)+"/holders")
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	list, _ := data["holders"].([]any)
	if len(list) == 0 {
		b.say(ctx, chat, "nobody holds this — every unit has been burned.")
		return
	}

	const show = 15
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>holders</b> · %d\n\n", len(list))
	var total uint64
	for i, h := range list {
		m, _ := h.(map[string]any)
		amt := uint64(num(m["amount"]))
		total += amt
		if i < show {
			fmt.Fprintf(&sb, "%d × %s\n", amt, party(str(m["address"])))
		}
	}
	if len(list) > show {
		fmt.Fprintf(&sb, "…and %d more\n", len(list)-show)
	}
	fmt.Fprintf(&sb, "\n%d unit(s) outstanding.", total)

	b.sayWith(ctx, chat, sb.String(), "", keyboard([]button{
		{Text: "asset", Data: "asset:" + b.shortRef(ref)},
		{Text: "history", Data: "history:" + b.shortRef(ref)},
	}))
}

// cmdHistory is the chain of custody: every transfer, newest first.
//
// The feed used to have two holes in it, and both are now filled by the
// indexer at serve time rather than here (internal/api/provenance.go): the
// mint, which is not a transfer row, and a dispenser's escrow legs, which
// move balance without writing one. Each row says which kind it is, so this
// prints the kind rather than inferring it — a purchase and a plain send
// differ only in who the sender is, and the sender of a purchase is a
// pseudo-address that party() labels rather than passing off as a wallet.
//
// An older indexer answers without the kinds and without those rows. The
// origin line below is printed either way: it costs one line and it is the
// only thing standing between a truncated feed and an asset whose units
// appear to have come from nowhere.
func (b *bot) cmdHistory(ctx context.Context, chat int64, arg string) {
	ref := strings.TrimSpace(arg)
	if ref == "" {
		b.say(ctx, chat, "usage: <code>/history &lt;id or number&gt;</code>")
		return
	}
	a, err := b.get(ctx, "/asset/"+url.PathEscape(ref))
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	data, err := b.get(ctx, "/asset/"+url.PathEscape(ref)+"/transfers?limit=25")
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	list, _ := data["transfers"].([]any)

	const show = 20
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>#%v · %s</b>\nprovenance\n\n", a["number"], esc(nameOf(a)))
	if len(list) == 0 {
		sb.WriteString("no transfers — still exactly where it was minted.\n")
	}
	for i, t := range list {
		if i == show {
			fmt.Fprintf(&sb, "…and %d earlier\n", len(list)-show)
			break
		}
		m, _ := t.(map[string]any)
		arrow, to := "→", party(str(m["to"]))
		if burned, _ := m["burned"].(bool); burned {
			arrow, to = "⇥", "🔥 burned"
		}
		fmt.Fprintf(&sb, "%s%d × %s %s %s\n<i>momentum %v · %s</i>\n",
			kindLabel(str(m["kind"])), num(m["amount"]), party(str(m["from"])), arrow, to,
			m["momentum"], esc(stamp(num(m["timestamp"]))))
	}

	// The origin, repeated from the asset itself. The feed carries it too on
	// an indexer that synthesises it, but a feed long enough to be truncated
	// drops its oldest row first — and that row is this one.
	fmt.Fprintf(&sb, "\n<b>origin</b>\nminted by %s\nissued to %s\n<i>momentum %v · %s</i>",
		party(str(a["issuer"])), party(str(a["owner"])), a["momentum"], esc(stamp(num(a["timestamp"]))))

	b.sayWith(ctx, chat, sb.String(), "", keyboard([]button{
		{Text: "asset", Data: "asset:" + b.shortRef(ref)},
		{Text: "holders", Data: "holders:" + b.shortRef(ref)},
	}))
}

// kindLabel prefixes a movement with what it was, for the kinds where "from
// and to" does not say it. A plain transfer gets nothing: it is the default,
// and labelling the common case is how a list stops being scannable. An older
// indexer sends no kind at all, which lands here as the default too.
func kindLabel(kind string) string {
	switch kind {
	case "mint":
		return "🌱 "
	case "escrow":
		return "🔒 "
	case "release":
		return "🔓 "
	case "sale":
		return "🛒 "
	default:
		return ""
	}
}

// party renders one side of a transfer or one row of a holders list.
//
// The balances table's address column holds three different kinds of thing
// and only one of them is a wallet. Printing all three the same way is how a
// dispenser's escrow ends up looking like a mystery whale.
//
// Escrow arrives as a z1zen0glyphxescr0w… address (spec R-8.6) from any indexer
// new enough to format it; the raw-hash branch below is what an older one
// still sends, and keeping both means the bot reads either without the
// operator having to upgrade the two together.
func party(addr string) string {
	switch {
	case addr == "":
		return "?"
	case addr == burnAddr:
		return "🔥 burn address"
	case protocol.IsEscrowAddress(addr):
		return "dispenser escrow <code>" + esc(short(addr)) + "</code>"
	case isHash64(addr):
		// The indexer parks a dispenser's stock under the dispenser's own id
		// while it is for sale, in the same balances table as everyone else.
		return "dispenser escrow <code>" + esc(addr[:8]) + "…</code>"
	default:
		return "<code>" + esc(short(addr)) + "</code>"
	}
}

func isHash64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// short keeps an address recognisable — the prefix says which network, the
// suffix is what people actually compare — without eating a phone screen.
func short(addr string) string {
	if len(addr) <= 20 {
		return addr
	}
	return addr[:10] + "…" + addr[len(addr)-6:]
}

func stamp(unix int64) string {
	if unix <= 0 {
		return "time unknown"
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 UTC")
}

// --- the market ---

func (b *bot) cmdMarket(ctx context.Context, chat int64, arg string) {
	all := strings.EqualFold(strings.TrimSpace(arg), "all")
	path := "/dispensers"
	if all {
		path += "?open=false"
	}
	data, err := b.get(ctx, path)
	if err != nil {
		b.say(ctx, chat, "cannot reach the market: "+esc(err.Error()))
		return
	}
	list, _ := data["dispensers"].([]any)
	if len(list) == 0 {
		if all {
			b.say(ctx, chat, "no dispensers have ever been opened here.")
			return
		}
		b.sayWith(ctx, chat, "nothing for sale right now.", "",
			keyboard([]button{{Text: "include closed", Data: "market:all"}}))
		return
	}

	const show = 12
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>market</b> · %d listing(s)\n\n", len(list))
	var rows [][]button
	for i, d := range list {
		if i == show {
			fmt.Fprintf(&sb, "…and %d more — see the site.\n", len(list)-show)
			break
		}
		m, _ := d.(map[string]any)
		state := ""
		if c, _ := m["cancelled"].(bool); c {
			state = " · closed"
		}
		fmt.Fprintf(&sb, "%v left at %v %s each%s\n<code>%s</code>\n",
			m["remaining"], m["price"], esc(short(str(m["zts"]))), state,
			esc(truncate(str(m["id"]), 12)))
		rows = append(rows, []button{{
			Text: fmt.Sprintf("%v @ %v", m["remaining"], m["price"]),
			Data: "disp:" + b.shortRef(str(m["id"])),
		}})
	}
	sb.WriteString("\ntap a listing to see what is behind it.")
	b.sayWith(ctx, chat, sb.String(), "", keyboard(rows...))
}

func (b *bot) cmdDispenser(ctx context.Context, chat int64, arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		b.say(ctx, chat, "usage: <code>/dispenser &lt;id&gt;</code> — or /market to list them")
		return
	}
	d, err := b.get(ctx, "/dispenser/"+url.PathEscape(id))
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}

	state := "open"
	switch {
	case boolOf(d["cancelled"]):
		state = "closed by the seller"
	case num(d["remaining"]) == 0:
		state = "sold out"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>dispenser</b> · %s\n<code>%s</code>\n\n", state, esc(str(d["id"])))
	fmt.Fprintf(&sb, "%v left at %v %s each\nseller %s\n<i>momentum %v · %s</i>\n\n",
		d["remaining"], d["price"], esc(short(str(d["zts"]))), party(str(d["issuer"])),
		d["momentum"], esc(stamp(num(d["timestamp"]))))

	// Buying is a signed payment to the seller, and this bot has no key it
	// could sign one with on a caller's behalf (see tradeText). Handing over
	// the exact command is the honest version of a buy button.
	if state == "open" {
		fmt.Fprintf(&sb, "to buy, from your own wallet:\n<code>zcribe dispenser buy -id %s -seller %s -pay &lt;total&gt;</code>",
			esc(str(d["id"])), esc(str(d["issuer"])))
	}

	var rows [][]button
	if asset := str(d["asset"]); asset != "" {
		rows = append(rows, []button{{Text: "the asset", Data: "asset:" + b.shortRef(asset)}})
	}
	b.sayWith(ctx, chat, sb.String(), "", keyboard(rows...))
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

// --- writes this bot will not perform ---

// tradeText answers /send, /transfer, /burn, /sell, /buy and /cancel.
//
// Every one of them exists in the protocol and in the CLI; none can be
// safely offered here, and the reason is the same reason /create is safe.
// /create spends the operator's plasma to bring something new into
// existence. A transfer, a burn, or a sale spends the operator's *holdings*
// — and this bot has exactly one key, shared by every caller, with no way to
// tell whose asset is whose. A /burn anyone can type is a button that
// destroys the operator's collection.
//
// That is a missing wallet, not a missing permission check, so the reply
// says so rather than implying the feature is merely switched off.
func (b *bot) tradeText(cmd string) string {
	noun := map[string]string{
		"/send": "transfers", "/transfer": "transfers", "/burn": "burns",
		"/sell": "sales", "/buy": "purchases", "/cancel": "cancellations",
	}[cmd]
	if noun == "" {
		noun = "transfers"
	}
	return fmt.Sprintf(`this bot doesn't sign %s.

it has one key, shared by everyone who messages it — so a %s here would move whatever <i>the bot</i> holds, at anyone's asking. that's a wallet it doesn't have, not a permission it forgot to check.

sign from your own instead:
<code>zcribe transfer -asset &lt;id&gt; -to z1…</code>
<code>zcribe transfer -asset &lt;id&gt; -burn</code>
<code>zcribe dispenser create|cancel|buy …</code>

/market — what's for sale
/history &lt;id&gt; — where an inscription has been`, noun, strings.TrimPrefix(cmd, "/"))
}

// --- moderation ---

// cmdModerate is /block and /unblock: the one thing in this file that is not
// a public-API read. It logs in to the same admin API the web panel uses and
// posts the request the panel posts, so an operator keeps one moderation
// policy rather than one per client.
//
// Two gates, both required. The Telegram user id must be in -admins, which
// says who may ask; the bot must hold the admin password, which says whether
// it can carry the request at all. Neither substitutes for the other — a
// Telegram user id arrives over the same transport as the request itself, so
// it is an authorisation list and never authentication.
func (b *bot) cmdModerate(ctx context.Context, u tgUpdate, block bool, arg string) {
	chat := u.Message.Chat.ID
	if !b.admins[u.Message.From.ID] {
		// Silent in groups: answering "you are not an admin" to a room tells
		// everyone present that the command exists and who to go asking.
		if u.Message.Chat.Type == "private" {
			b.say(ctx, chat, "not for you.")
		}
		return
	}
	if b.adminPassword == "" {
		b.say(ctx, chat, "moderation isn't wired up here — restart the bot with <code>-admin-password</code>.")
		return
	}

	ref, reason, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if ref == "" {
		b.say(ctx, chat, "usage: <code>/block &lt;id or number&gt; [reason]</code>")
		return
	}
	status, verb := "hidden", "blocked"
	if !block {
		status, verb = "visible", "unblocked"
	}
	body := map[string]any{"status": status, "reason": strings.TrimSpace(reason)}
	if err := b.adminPost(ctx, "/admin/api/moderation/"+url.PathEscape(ref), body); err != nil {
		b.say(ctx, chat, "couldn't do that: "+esc(err.Error()))
		return
	}
	b.say(ctx, chat, fmt.Sprintf(
		"%s <code>%s</code>.\nuntouched on the chain — this only changes what this indexer serves.",
		verb, esc(ref)))
}

// adminPost sends one authenticated admin request, logging in first if it
// holds no session, and once more if the one it holds has gone stale.
//
// The retry is not optional. Any admin logging out of the web panel bumps
// the session generation, which invalidates every outstanding cookie
// including this bot's — so a 401 is the expected state after ordinary panel
// use, not a failure worth reporting to the caller.
func (b *bot) adminPost(ctx context.Context, path string, body any) error {
	err := b.adminTry(ctx, path, body)
	if err == nil || !strings.HasPrefix(err.Error(), "401") {
		return err
	}
	b.adminMu.Lock()
	b.adminCookie = ""
	b.adminMu.Unlock()
	return b.adminTry(ctx, path, body)
}

func (b *bot) adminTry(ctx context.Context, path string, body any) error {
	b.adminMu.Lock()
	cookie := b.adminCookie
	b.adminMu.Unlock()
	if cookie == "" {
		var err error
		if cookie, err = b.adminLogin(ctx); err != nil {
			return err
		}
	}

	enc, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+path, bytes.NewReader(enc))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		var out map[string]any
		if json.Unmarshal(raw, &out) == nil {
			if msg, ok := out["error"].(string); ok {
				return fmt.Errorf("%d: %s", resp.StatusCode, msg)
			}
		}
		return fmt.Errorf("%d", resp.StatusCode)
	}
	return nil
}

// adminLogin exchanges the password for a session cookie and caches it.
func (b *bot) adminLogin(ctx context.Context) (string, error) {
	enc, _ := json.Marshal(map[string]string{"password": b.adminPassword})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+"/admin/api/login", bytes.NewReader(enc))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		// Deliberately not echoing the body. A wrong password answers
		// "invalid password", and relaying that into a chat says more about
		// the operator's credential than the caller needs to know.
		return "", fmt.Errorf("admin login failed (%d)", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == adminCookieName {
			cookie := c.Name + "=" + c.Value
			b.adminMu.Lock()
			b.adminCookie = cookie
			b.adminMu.Unlock()
			return cookie, nil
		}
	}
	return "", fmt.Errorf("admin login returned no session cookie")
}
