package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/botstore"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

// The Telegram front end.
//
// The original design notes named this as the first client: "I think the
// initial client will be a Telegram bot, that way I don't have to develop a
// website for the PoC." The website exists now, so this is the second client
// rather than the first — which makes it the more useful test. It exercises
// the HTTP API exactly the way any third-party client would, from outside
// the process, with no access to the database.
//
// Two properties follow from that and are the reason it is written this way:
//
//   - Moderation is enforced without the bot knowing the rules. A withheld
//     asset's dataWithheld flag comes from the API, and the bot reports that
//     without ever fetching the bytes. An operator who blacklists an id does
//     not have to configure the bot too.
//   - Content is never fetched by the bot at all. An asset's card carries a
//     hyperlink to its contentUrl, and Telegram's own link-preview fetcher —
//     not this process — pulls the live bytes to build the preview card
//     (sayWithPreview picks which link that is via link_preview_options,
//     confirmed against the live Bot API: Telegram otherwise defaults to
//     whichever URL appears first in the message text). This only works once
//     that URL is one Telegram's servers can reach, which a devnet on
//     localhost is not — see -web and each asset's own contentUrl.

const telegramAPI = "https://api.telegram.org"

// createCooldown throttles /create per Telegram user. A mint spends this
// bot's own plasma on the caller's behalf, so the cost of one person
// hammering the command lands on the operator, not on them.
const createCooldown = 45 * time.Second

// burnAddr is the string form of protocol.BurnAddress, resolved once so that
// party() can compare against it without re-deriving the address for every
// row of every transfer list it renders.
var burnAddr = protocol.BurnAddress.String()

type bot struct {
	token     string
	api       string // zcribe API base, e.g. http://127.0.0.1:8710
	content   string // content origin, read from /info — informational only; asset links use each asset's own contentUrl
	webOrigin string // public base for the site link, e.g. https://glyphs.zenon.foo — defaults to api
	channel   string // optional broadcast target
	http      *http.Client
	log       logger
	admins    map[int64]bool
	startedAt time.Time

	// in signs and publishes creations. Nil means /create is disabled — the
	// safe default, since nothing about the read side requires a wallet.
	// Every mint made through this bot is issued from in.Address(); there is
	// one key, chosen by the operator at startup, not one per caller.
	in   *inscribe.Inscriber
	inMu sync.Mutex // Send() reads the account's frontier fresh each call; two in flight would race the same height.

	createMu   sync.Mutex
	lastCreate map[int64]time.Time

	// adminPassword enables /block and /unblock. The bot holds no moderation
	// rules of its own — it logs in to the same admin API the panel uses and
	// posts the same request, so an operator has one policy store, not two.
	// Empty leaves both commands disabled, which is the default.
	adminPassword string
	adminMu       sync.Mutex
	adminCookie   string // the signed session cookie, re-fetched whenever it stops working

	// refs remembers full ids behind the short tokens inline-keyboard
	// buttons carry, because callback_data cannot hold one. See shortRef.
	refMu sync.Mutex
	refs  map[string]string

	// store holds what the chain does not: which address a Telegram user has
	// proved they control, and what credit they have. Nil disables /link,
	// /verify and everything downstream of them — which is the dev default,
	// because none of it is needed to read the chain.
	store *botstore.Store

	// node is the bot's own RPC client, used for the deposit watcher. It is
	// the same endpoint the inscriber signs against and is nil whenever the
	// inscriber is.
	node *rpcclient.Client

	// credit is the prod gate: mints cost tokens. Nil means free, which is
	// what a dev instance runs. See botcredit.go.
	credit *creditConfig
}

func cmdBot(ctx context.Context, args []string) error {
	set := fs("bot")
	token := set.String("token", "", "Telegram bot token (prefer ZCRIBE_TELEGRAM_TOKEN)")
	api := set.String("api", "http://127.0.0.1:8710", "zcribe API base URL")
	web := set.String("web", "", "public base URL for the site link in asset cards (defaults to -api, which is correct in prod: the viewer and the API share a host)")
	channel := set.String("channel", "", "chat id to announce new inscriptions to (optional)")
	announce := set.Bool("announce", false, "post new inscriptions to -channel as they confirm")
	post := set.String("post", "", "post one inscription to -channel and exit (an id or an inscription number)")
	admins := set.String("admins", "", "comma-separated Telegram user ids allowed to run /block and /unblock")
	adminPassword := set.String("admin-password", "", "zcribe admin-panel password, enabling /block and /unblock (prefer ZCRIBE_ADMIN_PASSWORD)")
	verbose := set.Bool("v", false, "debug logging")

	// Key source for /create. Empty (the default) leaves creation disabled;
	// these mirror walletFlags' own names deliberately so an operator who has
	// used `zcribe inscribe` already knows them.
	node := set.String("node", defaultNode, "NoM node JSON-RPC endpoint, for /create")
	mnemonicFile := set.String("mnemonic-file", "", "file holding a BIP-39 phrase; enables /create, signing as this key")
	mnemonicEnv := set.String("mnemonic-env", "", "environment variable holding a BIP-39 phrase; enables /create")
	keystore := set.String("keystore", "", "encrypted go-zenon keystore file; enables /create")
	password := set.String("password", "", "keystore password (prefer ZCRIBE_PASSWORD)")
	index := set.Uint("index", 1, "account index under m/44'/73404'/i' for the bot's inscribing key")
	devnet := set.Bool("devnet", false, "sign creates with the public devnet mnemonic (chain 69 only)")

	// On-chain identity, and the prod-only credit gate built on it. Both are
	// off by default: a dev bot needs neither, and turning them on is one
	// flag each rather than a mode switch that changes several things at
	// once. -credit-zts is what makes mints cost anything; without it the
	// database still records who proved which address and nothing is charged.
	botDB := set.String("db", "", "path to the bot's own database, enabling /link and /verify (identities and credit; separate from the indexer's)")
	creditZTS := set.String("credit-zts", "", "token standard users deposit to pay for mints (enables the credit gate; needs -db and a key)")
	mintCost := set.Uint64("mint-cost", 1000, "cost of one /create, in whole units of -credit-zts")
	_ = set.Parse(args)

	// A token on the command line lands in shell history and in the process
	// table, where any local user can read it. The flag exists for a quick
	// devnet run; the environment variable is the one to use.
	tok := os.Getenv("ZCRIBE_TELEGRAM_TOKEN")
	if tok == "" {
		tok = *token
	}
	if tok == "" {
		return fmt.Errorf("no bot token: set ZCRIBE_TELEGRAM_TOKEN or pass -token")
	}

	webOrigin := strings.TrimRight(*web, "/")
	if webOrigin == "" {
		webOrigin = strings.TrimRight(*api, "/")
	}
	b := &bot{
		token:      tok,
		api:        strings.TrimRight(*api, "/"),
		webOrigin:  webOrigin,
		channel:    *channel,
		http:       &http.Client{Timeout: 60 * time.Second},
		log:        newLogger(*verbose),
		admins:     map[int64]bool{},
		startedAt:  time.Now(),
		lastCreate: map[int64]time.Time{},
	}
	for _, a := range strings.Split(*admins, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(a), 10, 64); err == nil {
			b.admins[id] = true
		}
	}

	// Same reasoning as the bot token: a password on the command line is
	// readable by any local user out of the process table.
	b.adminPassword = os.Getenv("ZCRIBE_ADMIN_PASSWORD")
	if b.adminPassword == "" {
		b.adminPassword = *adminPassword
	}

	me, err := b.getMe(ctx)
	if err != nil {
		return fmt.Errorf("reach Telegram: %w", err)
	}
	info, err := b.info(ctx)
	if err != nil {
		return fmt.Errorf("reach the zcribe API at %s: %w", b.api, err)
	}
	b.content = strings.TrimRight(str(info["contentOrigin"]), "/")

	createStatus := "disabled (no key source: pass -devnet, -mnemonic-file, -mnemonic-env, or -keystore)"
	if *devnet || *mnemonicFile != "" || *mnemonicEnv != "" || *keystore != "" {
		w := walletFlags{
			node: *node, mnemonicFile: *mnemonicFile, mnemonicEnv: *mnemonicEnv,
			keystore: *keystore, password: *password, index: *index,
			devnet: *devnet, verbose: *verbose,
		}
		in, err := w.inscriber(ctx)
		if err != nil {
			return fmt.Errorf("wallet for /create: %w", err)
		}
		b.in = in
		b.node = rpcclient.New(*node)
		createStatus = fmt.Sprintf("enabled, issuing as %s", in.Address())
	}

	// The bot's own database. Opened whenever -db is given, because proving an
	// address is useful on its own — the credit gate is a second thing built
	// on top of it, not the reason for it.
	identityStatus := "disabled (pass -db)"
	if *botDB != "" {
		store, err := botstore.Open(*botDB)
		if err != nil {
			return fmt.Errorf("open bot database: %w", err)
		}
		defer store.Close()
		b.store = store
		identityStatus = "enabled, " + *botDB
	}

	// The credit gate. All three parts have to be present for it to mean
	// anything, and each missing one fails differently, so each is refused by
	// name rather than collapsed into "not configured".
	creditStatus := "disabled — mints are free"
	if *creditZTS != "" {
		switch {
		case b.store == nil:
			return fmt.Errorf("-credit-zts needs -db: a balance has to be recorded somewhere")
		case b.in == nil:
			return fmt.Errorf("-credit-zts needs a key: the bot must be able to receive deposits")
		}
		zts, err := types.ParseZTS(*creditZTS)
		if err != nil {
			return fmt.Errorf("bad -credit-zts %q: %w", *creditZTS, err)
		}
		cfg, err := newCreditConfig(ctx, b.node, zts, *mintCost)
		if err != nil {
			return err
		}
		b.credit = cfg
		creditStatus = fmt.Sprintf("%s %s per mint, deposits to %s",
			cfg.MintCostDisplay, cfg.Symbol, b.in.Address())
	}

	if err := b.setCommands(ctx); err != nil {
		// Not fatal: the bot still answers every command by hand, this only
		// costs the "/" menu Telegram would otherwise show for free.
		b.log.Info("setMyCommands failed", "err", err)
	}

	// Two ways /block can be dead, and they fail differently, so they are
	// reported separately rather than as one "disabled": an operator who set
	// a password but no ids has a working credential nobody may use, and one
	// who listed ids but no password has admins who will be told the bot
	// cannot reach the panel.
	blockStatus := "disabled (set -admin-password and -admins)"
	switch {
	case b.adminPassword == "" && len(b.admins) > 0:
		blockStatus = fmt.Sprintf("disabled for %d listed admin(s): no -admin-password", len(b.admins))
	case b.adminPassword != "" && len(b.admins) == 0:
		blockStatus = "disabled: -admin-password is set but -admins is empty, so nobody may use it"
	case b.adminPassword != "":
		blockStatus = fmt.Sprintf("enabled for %d admin(s)", len(b.admins))
	}

	fmt.Printf("zcribe bot @%s running\n  api:      %s\n  content:  %s\n  web:      %s\n  synced:   %v/%v\n  create:   %s\n  block:    %s\n  identity: %s\n  credit:   %s\n",
		str(me["username"]), b.api, b.content, b.webOrigin, info["syncHeight"], info["nodeHeight"],
		createStatus, blockStatus, identityStatus, creditStatus)

	// A one-shot post is how an operator announces a mint, and how anyone
	// checks the whole path — API, content links, moderation — in a single
	// command without waiting for someone to type at the bot.
	if *post != "" {
		if b.channel == "" {
			return fmt.Errorf("-post needs -channel")
		}
		chat, err := strconv.ParseInt(b.channel, 10, 64)
		if err != nil {
			return fmt.Errorf("bad -channel %q: %w", b.channel, err)
		}
		if err := b.showAssetByRef(ctx, chat, *post); err != nil {
			return err
		}
		fmt.Printf("posted %s to chat %s\n", *post, b.channel)
		return nil
	}

	if *announce && b.channel != "" {
		go b.announceLoop(ctx)
	}
	if b.creditsEnabled() {
		// One sweep before the loop, so a deposit made while the bot was
		// down is credited at startup rather than one interval later.
		go func() {
			b.sweepDeposits(ctx)
			b.watchDeposits(ctx)
		}()
	}
	return b.poll(ctx)
}

// --- Telegram transport ---

func (b *bot) call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	endpoint := fmt.Sprintf("%s/bot%s/%s", telegramAPI, b.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return b.do(req)
}

// do sends the request and unwraps Telegram's {ok, result, description}
// envelope. Result is left as raw JSON rather than decoded into a
// map[string]any here: most methods return an object (sendMessage, getMe,
// getFile), but some — setMyCommands, deleteMessage — return a bare boolean,
// and unmarshalling straight into a map fails on those with a confusing
// "cannot unmarshal bool into Go value" error. Callers that need an object
// decode it themselves with resultObject.
func (b *bot) do(req *http.Request) (json.RawMessage, error) {
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("telegram returned %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if !env.OK {
		return nil, fmt.Errorf("telegram: %s", env.Description)
	}
	return env.Result, nil
}

// resultObject decodes a call()/upload() result that is expected to be a
// JSON object, for the callers that read fields out of it.
func resultObject(raw json.RawMessage, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("expected an object result: %w", err)
	}
	return m, nil
}

func (b *bot) getMe(ctx context.Context) (map[string]any, error) {
	return resultObject(b.call(ctx, "getMe", url.Values{}))
}

// setCommands registers the "/" menu Telegram shows next to the message
// box, which is the entire reason someone opening this bot for the first
// time can discover it has commands at all rather than typing into the void.
// It runs once at startup and is not fatal if it fails — every command
// still works by hand, and /help repeats the same list in the chat itself.
//
// /create only appears when this bot actually has a wallet: advertising a
// command that would just reply "disabled" is worse than not listing it.
func (b *bot) setCommands(ctx context.Context) error {
	type tgCommand struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	cmds := []tgCommand{
		{"help", "list every command this bot understands"},
		{"info", "indexer status: sync height, state root, protocol version"},
		{"recent", "the newest inscriptions"},
		{"asset", "metadata for one inscription, with a link to its content (id or number)"},
		{"refs", "what a composite imports, and what imports it"},
		{"address", "what a z1... address holds"},
		{"collections", "indexed collections"},
	}
	if b.in != nil {
		cmds = append(cmds, tgCommand{"create", "mint a new inscription from an attached photo or document"})
	}
	if b.store != nil {
		cmds = append(cmds,
			tgCommand{"link", "prove you hold a Zenon address by signing a challenge"},
			tgCommand{"whoami", "which address this account has proved"})
	}
	if b.creditsEnabled() {
		cmds = append(cmds, tgCommand{"balance", "your mint credit, and where to top it up"})
	}
	enc, err := json.Marshal(cmds)
	if err != nil {
		return err
	}
	v := url.Values{}
	v.Set("commands", string(enc))
	_, err = b.call(ctx, "setMyCommands", v)
	return err
}

func (b *bot) say(ctx context.Context, chat int64, text string) {
	b.sayWithPreview(ctx, chat, text, "")
}

// sayWithPreview is say, plus control over Telegram's own link-preview
// fetcher. previewURL, when set, is passed as link_preview_options.url — this
// picks which of possibly several links in the message gets the preview
// card, overriding Telegram's default of "whichever URL appears first in the
// text" (confirmed against the live API: a message with a plain webpage link
// before a direct image link previews the webpage by default, but forcing
// url to the image previews the image regardless of position). An empty
// previewURL disables the preview entirely, same as say()'s old behavior.
//
// Forcing url is not merely "best-effort, falls back to no preview" on
// Telegram's side — a url it cannot itself validate (a devnet on localhost,
// today; a dead link, always) fails the whole sendMessage with
// WEBPAGE_URL_INVALID, confirmed live against the Bot API. Silently losing
// the entire card over a preview that was never guaranteed anyway is worse
// than the missing preview, so on that specific failure this retries once
// with the preview simply turned off.
func (b *bot) sayWithPreview(ctx context.Context, chat int64, text, previewURL string) {
	b.sayWith(ctx, chat, text, previewURL, "")
}

// --- inline keyboards ---
//
// Buttons are the whole difference between a bot someone uses once and one
// they keep using. Every card this bot posts is a dead end without them: a
// caller who wants an asset's holders has to retype a 64-character id, on a
// phone, having first scrolled back up to copy it. A button carries that id
// for them.
//
// callback_data is capped by Telegram at 64 bytes, which a full 64-character
// asset id already exhausts on its own. Every button therefore addresses an
// asset by its inscription number where one exists, and the id is only used
// for things that have no number (a dispenser). keyboardFor enforces the cap
// rather than trusting it: an over-long callback_data makes Telegram reject
// the whole sendMessage, which would cost the entire card to save a button.

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// keyboard marshals rows of buttons into reply_markup, dropping any button
// whose callback_data would exceed Telegram's 64-byte limit.
func keyboard(rows ...[]button) string {
	out := make([][]button, 0, len(rows))
	for _, row := range rows {
		keep := make([]button, 0, len(row))
		for _, bt := range row {
			if len(bt.Data) > callbackDataMax {
				continue
			}
			keep = append(keep, bt)
		}
		if len(keep) > 0 {
			out = append(out, keep)
		}
	}
	if len(out) == 0 {
		return ""
	}
	enc, err := json.Marshal(map[string]any{"inline_keyboard": out})
	if err != nil {
		return ""
	}
	return string(enc)
}

// sayWith is say plus an optional preview url and an optional keyboard. The
// three-argument form exists because almost every card wants both.
func (b *bot) sayWith(ctx context.Context, chat int64, text, previewURL, markup string) {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chat, 10))
	v.Set("text", text)
	v.Set("parse_mode", "HTML")
	if previewURL == "" {
		v.Set("link_preview_options", `{"is_disabled":true}`)
	} else {
		opts, _ := json.Marshal(map[string]any{
			"url":                previewURL,
			"prefer_large_media": true,
			"show_above_text":    false,
		})
		v.Set("link_preview_options", string(opts))
	}
	if markup != "" {
		v.Set("reply_markup", markup)
	}
	if _, err := b.call(ctx, "sendMessage", v); err != nil {
		b.log.Info("sendMessage failed", "err", err)
		if previewURL != "" && strings.Contains(err.Error(), "WEBPAGE_URL_INVALID") {
			b.log.Info("retrying without a forced preview", "url", previewURL)
			b.sayWith(ctx, chat, text, "", markup)
		}
	}
}

// ack clears the spinner Telegram shows on a tapped button. Skipping it
// leaves the button looking stuck for several seconds even though the reply
// has already arrived, so it is sent before doing any work, not after.
func (b *bot) ack(ctx context.Context, callbackID, notice string) {
	v := url.Values{"callback_query_id": {callbackID}}
	if notice != "" {
		v.Set("text", notice)
	}
	if _, err := b.call(ctx, "answerCallbackQuery", v); err != nil {
		b.log.Info("answerCallbackQuery failed", "err", err)
	}
}

// --- polling ---

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		From      struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"` // "private", "group", "supergroup", or "channel"
		} `json:"chat"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
		// Photo is the size ladder Telegram generates for an image sent as a
		// photo, smallest first; the caller's original bytes are gone by the
		// time this arrives; document is the only attachment kind that
		// preserves them exactly.
		Photo []struct {
			FileID   string `json:"file_id"`
			FileSize int64  `json:"file_size"`
		} `json:"photo"`
		Document *struct {
			FileID   string `json:"file_id"`
			FileName string `json:"file_name"`
			MimeType string `json:"mime_type"`
			FileSize int64  `json:"file_size"`
		} `json:"document"`
	} `json:"message"`

	// CallbackQuery arrives when someone taps an inline-keyboard button.
	// Data is whatever keyboardFor put there; Message is the card the button
	// is attached to, and is what gives the reply a chat to land in.
	CallbackQuery *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// poll is a long-poll loop. getUpdates with a 30 s timeout is one HTTP request
// per half minute when nothing is happening, which is what Telegram asks for
// and what keeps this from being a busy loop against their rate limits.
func (b *bot) poll(ctx context.Context) error {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		endpoint := fmt.Sprintf("%s/bot%s/getUpdates?timeout=30&offset=%d",
			telegramAPI, b.token, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := b.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.log.Info("getUpdates failed; retrying", "err", err)
			time.Sleep(3 * time.Second)
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		var env struct {
			OK     bool       `json:"ok"`
			Result []tgUpdate `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
			b.log.Info("getUpdates returned something unexpected", "body", truncate(string(raw), 200))
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range env.Result {
			offset = u.UpdateID + 1
			if u.CallbackQuery != nil {
				b.handleCallback(ctx, u)
				continue
			}
			if u.Message == nil || (u.Message.Text == "" && u.Message.Caption == "") {
				continue
			}
			b.handle(ctx, u)
		}
	}
}

func (b *bot) handle(ctx context.Context, u tgUpdate) {
	chat := u.Message.Chat.ID
	text := strings.TrimSpace(u.Message.Text)
	if text == "" {
		// A photo or a document carries its command in the caption, not text.
		text = strings.TrimSpace(u.Message.Caption)
	}
	// A command in a group arrives as /get@zcribe_bot; strip the mention.
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))

	defer func() {
		// One malformed reply from the API must not take the bot down: it is
		// reachable by anyone who can find the username.
		if r := recover(); r != nil {
			b.log.Info("recovered from a panic while handling a command", "cmd", cmd, "panic", r)
		}
	}()

	switch cmd {
	case "/start", "/help":
		b.say(ctx, chat, b.helpText())
	case "/info":
		b.cmdInfo(ctx, chat)
	case "/recent":
		b.cmdRecent(ctx, chat, arg)
	case "/asset", "/info_asset", "/get", "/content":
		// /get and /content are kept as aliases of /asset: they used to fetch
		// and re-upload the inscribed bytes, but that's exactly what
		// showAsset's content hyperlink now leaves to Telegram's own preview
		// fetcher, so there is no longer a behavioral difference to keep two
		// commands for.
		b.cmdAsset(ctx, chat, arg)
	case "/refs":
		b.cmdRefs(ctx, chat, arg)
	case "/holders":
		b.cmdHolders(ctx, chat, arg)
	case "/history", "/provenance":
		b.cmdHistory(ctx, chat, arg)
	case "/market", "/dispensers":
		b.cmdMarket(ctx, chat, arg)
	case "/dispenser":
		b.cmdDispenser(ctx, chat, arg)
	case "/address":
		b.cmdAddress(ctx, chat, arg)
	case "/collections":
		b.cmdCollections(ctx, chat)
	case "/link":
		b.cmdLink(ctx, u, arg)
	case "/verify":
		b.cmdVerify(ctx, u, arg)
	case "/whoami":
		b.cmdWhoami(ctx, u)
	case "/unlink":
		b.cmdUnlink(ctx, u)
	case "/balance":
		b.cmdBalance(ctx, u)
	case "/block", "/unblock":
		b.cmdModerate(ctx, u, cmd == "/block", arg)
	case "/send", "/transfer", "/burn", "/sell", "/buy", "/cancel":
		// These are real protocol operations and this bot deliberately does
		// not perform them. Answering "unknown command" would be a lie by
		// omission — the operation exists, it is the custody model that
		// stops here. See tradeText.
		b.say(ctx, chat, b.tradeText(cmd))
	case "/create", "/mint":
		// Runs in its own goroutine: a composite chain can take a couple of
		// minutes end to end, and this is a long-poll loop handling one
		// update at a time — a synchronous /create would stall every other
		// command in every chat until it finished.
		go b.cmdCreate(ctx, u, arg)
	default:
		if strings.HasPrefix(cmd, "/") {
			b.say(ctx, chat, "don't know that one.\n\n"+b.helpText())
			return
		}
		// Plain text, no leading slash.
		//
		// A bare id or inscription number is not a mistake — it is what
		// someone does after copying one out of a card, and making them
		// prefix it with /asset teaches them nothing. Look it up. Anything
		// else in a private chat is almost always someone who did not know
		// this is a command-driven bot, which is the case /help exists for.
		// In a group, where conversation is happening that was not addressed
		// to the bot, say nothing at all.
		if isHash64(text) {
			_ = b.showAssetByRef(ctx, chat, strings.ToLower(text))
			return
		}
		if n, err := strconv.Atoi(text); err == nil && n > 0 {
			_ = b.showAssetByRef(ctx, chat, text)
			return
		}
		if u.Message.Chat.Type == "private" {
			b.say(ctx, chat, b.helpText())
		}
	}
}

// helpText is the whole surface of this bot in one screen.
//
// It is written short on purpose. Someone reading it is on a phone, in a
// chat window, and every line they have to scroll past is a line they will
// not read — so each command gets one line, the two things that surprise
// people (what /create costs whom, and what "owner" does not mean) get a
// sentence each, and everything else is left to the commands themselves to
// explain when they are actually run.
func (b *bot) helpText() string {
	base := `<b>zcribe</b> — Zenoglyphs on the Network of Momentum

<b>look</b>
/recent — newest inscriptions
/asset &lt;id|№&gt; — one inscription, with a live preview
/refs &lt;id|№&gt; — what it imports, and what imports it
/collections — indexed collections

<b>who has what</b>
/holders &lt;id|№&gt; — who holds it now
/history &lt;id|№&gt; — where it has been
/address &lt;z1…&gt; — what an address holds

<b>market</b>
/market — what is for sale (add <code>all</code> for closed listings too)
/dispenser &lt;id&gt; — one listing

/info — indexer status

Cards come with buttons — tap rather than retype a 64-character id. An id is
that hash, or just the inscription number.

Trading is signed from your own wallet, never here: try /send or /buy to see
why. Content is served from this operator's own link and never uploaded by
this bot; anything withheld here stays on the chain regardless.`

	// Identity and credit are described only when they exist. A help screen
	// listing commands that answer "not enabled on this bot" teaches nothing
	// and makes the dev instance read like a broken prod one.
	if b.store != nil {
		base += `

<b>your address</b>
/link &lt;z1…&gt; — prove you hold an address by signing a challenge
/whoami — which address this account has proved
/unlink — forget it

Signing moves nothing and costs no plasma: it is a statement, not a
transaction.`
	}
	if b.creditsEnabled() {
		base += fmt.Sprintf(`

<b>credit</b>
/balance — what you have, and where to send more

Minting costs %s %s. Send %s to this bot from your linked address and it is
credited when it lands; a deposit from an address nobody has linked is left
unreceived rather than kept.`,
			esc(b.credit.MintCostDisplay), esc(b.credit.Symbol), esc(b.credit.Symbol))
	}

	if b.in == nil {
		return base + "\n\n/create is off on this bot."
	}
	return base + fmt.Sprintf(`

<b>/create</b> — attach a photo or a document, <code>/create</code> in the
caption, plus optional flags:
  <code>name="…" desc="…" supply=1 decimals=0</code>
  <code>collection=&lt;id&gt; refs=&lt;id&gt;,&lt;id&gt; to=z1… pow=1</code>

Send a document, not a photo — Telegram re-encodes photos, so a document is
the only way to inscribe your exact bytes.

Every mint is signed by this bot's key (<code>%s</code>) and paid for by this
operator, so: one per %v seconds. Add <code>to=z1…</code> to have the result
land in your own wallet rather than the bot's.`,
		esc(b.in.Address().String()), int(createCooldown.Seconds()))
}

// --- zcribe API client ---

func (b *bot) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("indexer returned %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		if msg, ok := out["error"].(string); ok {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("indexer returned %d", resp.StatusCode)
	}
	return out, nil
}

func (b *bot) info(ctx context.Context) (map[string]any, error) { return b.get(ctx, "/info") }

// --- commands ---

func (b *bot) cmdInfo(ctx context.Context, chat int64) {
	info, err := b.info(ctx)
	if err != nil {
		b.say(ctx, chat, "cannot reach the indexer: "+esc(err.Error()))
		return
	}
	lag := num(info["nodeHeight"]) - num(info["syncHeight"])
	b.say(ctx, chat, fmt.Sprintf(
		"<b>indexer</b>\nprotocol: %s v%v (prefix <code>%s</code>)\n"+
			"synced: %v of %v (%d behind)\nevents: %v\n"+
			"full confirmation: %v momentums\ncheckpoints: every %v, covering %v\n"+
			"state root: <code>%s</code>",
		esc(str(info["protocol"])), info["protocolVersion"], esc(str(info["prefix"])),
		info["syncHeight"], info["nodeHeight"], lag, info["events"],
		info["confirmationDepth"], info["snapshotInterval"], info["snapshotRetain"],
		esc(truncate(str(info["stateRoot"]), 16))))
}

// cmdRecent lists the newest inscriptions and gives each one a button, so
// the way to look at #37 is to tap it rather than to copy a 64-character id
// off a phone screen. arg is either a count (typed) or "page:offset" (from
// the "older" button); pageArg splits the two apart.
func (b *bot) cmdRecent(ctx context.Context, chat int64, arg string) {
	const perPage = 10
	n, offset := perPage, 0
	if rest, ok := strings.CutPrefix(strings.TrimSpace(arg), "page:"); ok {
		offset, _ = strconv.Atoi(rest)
	} else if typed, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil && typed > 0 {
		n = min(typed, 20)
	}

	data, err := b.get(ctx, fmt.Sprintf("/assets?limit=%d&offset=%d", n, offset))
	if err != nil {
		b.say(ctx, chat, "cannot list: "+esc(err.Error()))
		return
	}
	list, _ := data["assets"].([]any)
	if len(list) == 0 {
		b.say(ctx, chat, "nothing here.")
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%v inscriptions</b> · newest first\n\n", data["total"])
	var rows [][]button
	for _, item := range list {
		a, _ := item.(map[string]any)
		mark := ""
		if str(a["status"]) != "full" {
			mark = " · unconfirmed"
		}
		fmt.Fprintf(&sb, "#%v · %s\n<i>%s%s</i>\n",
			a["number"], esc(nameOf(a)), esc(str(a["mime"])), mark)
		rows = append(rows, []button{{
			Text: fmt.Sprintf("#%v %s", a["number"], truncate(nameOf(a), 24)),
			Data: "asset:" + b.assetRef(a),
		}})
	}
	if next := offset + len(list); int64(next) < num(data["total"]) {
		rows = append(rows, []button{{Text: "older →", Data: fmt.Sprintf("recent:page:%d", next)}})
	}
	b.sayWith(ctx, chat, sb.String(), "", keyboard(rows...))
}

func (b *bot) cmdAsset(ctx context.Context, chat int64, arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		b.say(ctx, chat, "usage: /asset &lt;id or inscription number&gt;")
		return
	}
	_ = b.showAssetByRef(ctx, chat, id)
}

// showAssetByRef looks up one asset and posts its card. It returns the
// lookup error, rather than only reporting it into the chat, because the
// -post startup path (an operator announcing one mint from the command
// line) needs to know whether it actually worked; every other caller here
// discards it — the error is already where it belongs once say() has run.
func (b *bot) showAssetByRef(ctx context.Context, chat int64, ref string) error {
	a, err := b.get(ctx, "/asset/"+url.PathEscape(ref))
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return err
	}
	b.showAsset(ctx, chat, a)
	return nil
}

// showAsset posts an asset's card as a text message carrying two hyperlinks
// — the site's own page for it, and its content URL — instead of the bot
// downloading the inscribed bytes and re-uploading them to Telegram itself.
// Telegram's own link-preview fetcher does the work of pulling the live
// image, video, or audio from the content link; link_preview_options.url
// picks that one out explicitly rather than leaving it to whichever link
// happens to come first in the text (see sayWithPreview).
//
// This also means moderation needs no special handling here any more: a
// withheld asset's bytes never pass through this process at all, because
// the bot never fetches them — it just doesn't offer the content link.
// The link to the site is a button rather than an inline <a>, so the card's
// text stays the inscription and the ways onward from it stay in one row —
// see assetKeyboard, which carries holders, history and imports beside it.
func (b *bot) showAsset(ctx context.Context, chat int64, a map[string]any) {
	markup := b.assetKeyboard(a)

	var sb strings.Builder
	sb.WriteString(describe(a))

	if withheld, _ := a["dataWithheld"].(bool); withheld {
		sb.WriteString("\n\n⊘ content withheld under this operator's policy — untouched on the chain, and any other indexer may still serve it.")
		b.sayWith(ctx, chat, sb.String(), "", markup)
		return
	}

	contentURL := str(a["contentUrl"])
	if contentURL == "" {
		b.sayWith(ctx, chat, sb.String(), "", markup)
		return
	}
	fmt.Fprintf(&sb, "\n\n<a href=\"%s\">content</a>", esc(contentURL))
	b.sayWith(ctx, chat, sb.String(), contentURL, markup)
}

func (b *bot) cmdRefs(ctx context.Context, chat int64, arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		b.say(ctx, chat, "usage: /refs &lt;id or inscription number&gt;")
		return
	}
	data, err := b.get(ctx, "/asset/"+url.PathEscape(id)+"/refs")
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	refs, _ := data["refs"].([]any)
	back, _ := data["referencedBy"].([]any)
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>composite graph</b>\n<code>%s</code>\n\n", esc(truncate(str(data["id"]), 16)))
	if len(refs) == 0 {
		sb.WriteString("imports: none — this is a standalone inscription.\n")
	} else {
		sb.WriteString("<b>imports</b> (slot → asset)\n")
		for _, r := range refs {
			m, _ := r.(map[string]any)
			mark := "missing"
			if ok, _ := m["resolved"].(bool); ok {
				mark = esc(str(m["mime"]))
			}
			fmt.Fprintf(&sb, "  %v → <code>%s</code> · %s\n",
				m["slot"], esc(truncate(str(m["id"]), 12)), mark)
		}
	}
	if len(back) > 0 {
		fmt.Fprintf(&sb, "\n<b>imported by</b> %d inscription(s)\n", len(back))
		for i, r := range back {
			if i == 5 {
				fmt.Fprintf(&sb, "  …and %d more\n", len(back)-5)
				break
			}
			fmt.Fprintf(&sb, "  <code>%s</code>\n", esc(truncate(str(r), 12)))
		}
	}
	b.sayWith(ctx, chat, sb.String(), "", keyboard([]button{
		{Text: "asset", Data: "asset:" + b.shortRef(id)},
		{Text: "holders", Data: "holders:" + b.shortRef(id)},
	}))
}

func (b *bot) cmdAddress(ctx context.Context, chat int64, arg string) {
	addr := strings.TrimSpace(arg)
	if !strings.HasPrefix(addr, "z1") {
		b.say(ctx, chat, "usage: <code>/address &lt;z1…&gt;</code>")
		return
	}
	data, err := b.get(ctx, "/address/"+url.PathEscape(addr))
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	holdings, _ := data["holdings"].([]any)
	if len(holdings) == 0 {
		b.say(ctx, chat, "nothing held at "+party(addr)+".")
		return
	}

	const show = 12
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%d holding(s)</b>\n%s\n\n", len(holdings), party(addr))
	var rows [][]button
	for i, h := range holdings {
		m, _ := h.(map[string]any)
		a, _ := m["asset"].(map[string]any)
		if i == show {
			fmt.Fprintf(&sb, "…and %d more\n", len(holdings)-show)
			break
		}
		fmt.Fprintf(&sb, "#%v · %s × %v\n", a["number"], esc(nameOf(a)), m["amount"])
		rows = append(rows, []button{{
			Text: fmt.Sprintf("#%v %s", a["number"], truncate(nameOf(a), 24)),
			Data: "asset:" + b.assetRef(a),
		}})
	}
	b.sayWith(ctx, chat, sb.String(), "", keyboard(rows...))
}

func (b *bot) cmdCollections(ctx context.Context, chat int64) {
	data, err := b.get(ctx, "/collections")
	if err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}
	cols, _ := data["collections"].([]any)
	if len(cols) == 0 {
		b.say(ctx, chat, "no collections indexed.")
		return
	}
	var sb strings.Builder
	sb.WriteString("<b>collections</b>\n\n")
	for _, c := range cols {
		m, _ := c.(map[string]any)
		fmt.Fprintf(&sb, "%s · <code>%s</code>\n", esc(str(m["Name"])), esc(truncate(str(m["ID"]), 12)))
	}
	b.say(ctx, chat, sb.String())
}

// cmdCreate mints a new inscription from an attached photo or document.
//
// This is a service, not a wallet: the bot signs with its own key
// (in.Address()), the same way for every caller. There is no per-user
// custody here — the trade a bot-operated key makes is centralizing
// issuance in exchange for never touching anyone else's private key over a
// chat transport. A caller who wants the result somewhere else passes
// to=<z1…>; the bot still pays the plasma.
func (b *bot) cmdCreate(ctx context.Context, u tgUpdate, arg string) {
	chat := u.Message.Chat.ID
	user := u.Message.From.ID

	if b.in == nil {
		b.say(ctx, chat, "creation is disabled on this bot.")
		return
	}

	defer func() {
		if r := recover(); r != nil {
			b.log.Info("recovered from a panic in cmdCreate", "panic", r)
			b.say(ctx, chat, "something went wrong building that inscription.")
		}
	}()

	if wait, ok := b.checkCooldown(user); !ok {
		b.say(ctx, chat, fmt.Sprintf("one at a time — try again in %ds.", wait))
		return
	}

	// Checked here, charged after the block publishes (see charge()). A mint
	// that fails costs the operator nothing and must cost the caller nothing;
	// a mint that succeeds cannot be un-published, so the debit cannot be
	// refused at that point either.
	if why, ok := b.canAfford(user); !ok {
		b.say(ctx, chat, why)
		return
	}

	fileID, filename, tgMime, size, err := pickMedia(u)
	if err != nil {
		b.say(ctx, chat, esc(err.Error())+"\n\n"+b.helpText())
		return
	}
	const maxContent = protocol.MaxAssetParts * protocol.MaxDataLength // the multi-part ceiling, ~1 MB
	if size > maxContent {
		b.say(ctx, chat, fmt.Sprintf("too big: %d bytes exceeds the %d-byte protocol ceiling for one asset.", size, maxContent))
		return
	}

	flags, err := parseCaptionFlags(arg)
	if err != nil {
		b.say(ctx, chat, "couldn't parse that: "+esc(err.Error()))
		return
	}

	mimeType := flags["mime"]
	if mimeType == "" && filename != "" {
		if guess, ok := protocol.MimeForFile(filename); ok {
			mimeType = guess
		}
	}
	if mimeType == "" {
		mimeType = tgMime
	}
	if !protocol.MimeAllowed(mimeType) {
		b.say(ctx, chat, fmt.Sprintf("mime %q is not allowed.\nallowed: %s",
			esc(mimeType), esc(strings.Join(protocol.AllowedMimes(), ", "))))
		return
	}

	name := flags["name"]
	if name == "" {
		name = filename
	}
	supply, err := parseFlagUint(flags, "supply", 1)
	if err != nil {
		b.say(ctx, chat, "supply must be a whole number.")
		return
	}
	decimals, err := parseFlagUint(flags, "decimals", 0)
	if err != nil {
		b.say(ctx, chat, "decimals must be a whole number.")
		return
	}

	meta := protocol.MintWire{
		Name: name, Description: flags["desc"],
		Supply: supply, Decimals: decimals, Mime: mimeType,
	}
	if c := flags["collection"]; c != "" {
		raw, err := hex.DecodeString(c)
		if err != nil || len(raw) != protocol.HashLen {
			b.say(ctx, chat, "collection= must be 64 hex characters.")
			return
		}
		meta.Collection = raw
	}
	if r := flags["refs"]; r != "" {
		parsed, err := parseRefs(r)
		if err != nil {
			b.say(ctx, chat, esc(err.Error()))
			return
		}
		meta.Refs = parsed
	}
	if err := meta.Validate(); err != nil {
		b.say(ctx, chat, esc(err.Error()))
		return
	}

	// Where the supply lands. The bot's own address is the fallback, not the
	// intent: once someone has proved they hold an address, minting into it
	// is what they meant, and the alternative is an asset they have to ask
	// the operator to send them.
	recipient := b.in.Address()
	if b.store != nil {
		if id, err := b.store.Identity(user); err == nil && id != nil {
			if addr, err := types.ParseAddress(id.Address); err == nil {
				recipient = addr
			}
		}
	}
	if to := flags["to"]; to != "" {
		addr, err := types.ParseAddress(to)
		if err != nil {
			b.say(ctx, chat, "to= is not a valid address.")
			return
		}
		recipient = addr
	}

	mode := protocol.PlasmaFused
	if flags["pow"] == "1" || flags["pow"] == "true" {
		mode = protocol.PlasmaPoW
	}

	b.say(ctx, chat, "⏳ fetching the file and submitting…")
	content, err := b.getFile(ctx, fileID)
	if err != nil {
		b.say(ctx, chat, "couldn't fetch that from Telegram: "+esc(err.Error()))
		return
	}

	// Serialized: Send() reads this account's frontier fresh on every call,
	// so a second mint started before the first publishes would sign against
	// the same height and one of the two would be rejected by the network.
	b.inMu.Lock()
	hash, err := b.in.InscribeFile(ctx, meta, content, mode, recipient)
	b.inMu.Unlock()
	if err != nil {
		b.say(ctx, chat, "inscribe failed: "+esc(err.Error()))
		return
	}

	id := hash.String()
	b.charge(user, id)
	// "issued to", not "owner", for the same reason describe() says it: the
	// recipient here is where the supply lands at the mint, and the moment it
	// moves, this line is history rather than ownership.
	b.say(ctx, chat, fmt.Sprintf(
		"✓ submitted.\n<code>%s</code>\n\nminted by %s\nissued to %s\n\n"+
			"the card lands here on its own once the indexer catches up — usually the next momentum.",
		esc(id), party(b.in.Address().String()), party(recipient.String())))

	// Best-effort: post the finished card once indexed, so nobody has to
	// remember to come back and type /asset.
	go b.announceOnceIndexed(ctx, chat, id)
}

// checkCooldown enforces createCooldown per Telegram user id. The zero value
// of the map (never seen before) always passes.
func (b *bot) checkCooldown(user int64) (waitSeconds int, ok bool) {
	b.createMu.Lock()
	defer b.createMu.Unlock()
	if last, seen := b.lastCreate[user]; seen {
		if elapsed := time.Since(last); elapsed < createCooldown {
			return int((createCooldown - elapsed).Seconds()) + 1, false
		}
	}
	b.lastCreate[user] = time.Now()
	return 0, true
}

// pickMedia finds the attachment on a /create message.
//
// A photo's bytes are Telegram's JPEG re-encode, never the sender's
// original — fine for a picture, wrong for anything claiming to inscribe
// exact bytes. A document is untouched, which is why it is preferred and is
// the only path that can carry a non-image mime type at all.
func pickMedia(u tgUpdate) (fileID, filename, mime string, size int64, err error) {
	m := u.Message
	if m.Document != nil {
		return m.Document.FileID, m.Document.FileName, m.Document.MimeType, m.Document.FileSize, nil
	}
	if len(m.Photo) > 0 {
		best := m.Photo[len(m.Photo)-1] // Telegram lists sizes smallest first
		return best.FileID, "", "image/jpeg", best.FileSize, nil
	}
	return "", "", "", 0, fmt.Errorf("attach a photo or a document, with /create in the caption")
}

// parseFlagUint reads a decimal flag, defaulting when absent.
func parseFlagUint(flags map[string]string, key string, def uint64) (uint64, error) {
	v, ok := flags[key]
	if !ok || v == "" {
		return def, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

// parseCaptionFlags reads key=value pairs out of a /create caption, e.g.
//
//	name="Sunset" desc="a test mint" supply=1 refs=<id>,<id>
//
// A value may be double-quoted to hold spaces or commas; anything else runs
// to the next space. This is deliberately not flag.FlagSet: a caption is one
// line typed on a phone, not a shell command line, and has no leading
// dashes.
func parseCaptionFlags(s string) (map[string]string, error) {
	out := map[string]string{}
	i, n := 0, len(s)
	for i < n {
		for i < n && s[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n && s[i] != '=' && s[i] != ' ' {
			i++
		}
		key := strings.ToLower(s[start:i])
		if i >= n || s[i] != '=' {
			return nil, fmt.Errorf("expected key=value near %q", key)
		}
		i++ // '='
		var val string
		if i < n && s[i] == '"' {
			i++
			start = i
			for i < n && s[i] != '"' {
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("unterminated quote after %s=", key)
			}
			val = s[start:i]
			i++ // closing quote
		} else {
			start = i
			for i < n && s[i] != ' ' {
				i++
			}
			val = s[start:i]
		}
		if key == "" {
			return nil, fmt.Errorf("empty key before '='")
		}
		out[key] = val
	}
	return out, nil
}

// getFile downloads the bytes behind a Telegram file id.
//
// This is the intake side of the bot's content policy: whatever the caller
// attached becomes the inscription's exact bytes, read once as-is and never
// re-encoded or transformed by this code.
func (b *bot) getFile(ctx context.Context, fileID string) ([]byte, error) {
	res, err := resultObject(b.call(ctx, "getFile", url.Values{"file_id": {fileID}}))
	if err != nil {
		return nil, err
	}
	path := str(res["file_path"])
	if path == "" {
		return nil, fmt.Errorf("telegram did not return a file path")
	}
	endpoint := fmt.Sprintf("%s/file/bot%s/%s", telegramAPI, b.token, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download returned %d", resp.StatusCode)
	}
	const maxContent = protocol.MaxAssetParts * protocol.MaxDataLength
	return io.ReadAll(io.LimitReader(resp.Body, maxContent+1))
}

// announceOnceIndexed posts the finished card as soon as the indexer has the
// just-submitted id, so a caller does not have to remember to come back and
// type /asset themselves. It gives up quietly after a couple of minutes —
// the id was already reported in cmdCreate's reply, so nothing is lost if
// the indexer is running behind and this loop never finds it.
func (b *bot) announceOnceIndexed(ctx context.Context, chat int64, id string) {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		if _, err := b.get(ctx, "/asset/"+url.PathEscape(id)); err == nil {
			_ = b.showAssetByRef(ctx, chat, id)
			return
		}
	}
}

// announceLoop posts newly indexed inscriptions to a channel.
//
// It announces only what is fully confirmed. A soft-confirmed mint can still
// be undone by a reorg, and a channel post cannot be, so announcing one would
// publish a claim the chain has not yet made.
func (b *bot) announceLoop(ctx context.Context) {
	seen := map[string]bool{}
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		data, err := b.get(ctx, "/assets?limit=25")
		if err != nil {
			continue
		}
		list, _ := data["assets"].([]any)
		// Walk oldest-first so a burst is announced in the order it happened.
		for i := len(list) - 1; i >= 0; i-- {
			a, _ := list[i].(map[string]any)
			id := str(a["id"])
			if id == "" || seen[id] {
				continue
			}
			if str(a["status"]) != "full" {
				continue
			}
			seen[id] = true
			if first {
				// The backlog that existed when the bot started is not news.
				continue
			}
			chat, err := strconv.ParseInt(b.channel, 10, 64)
			if err != nil {
				b.log.Info("bad -channel id", "channel", b.channel)
				return
			}
			_ = b.showAssetByRef(ctx, chat, id)
		}
		first = false
	}
}

// --- formatting ---

func describe(a map[string]any) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>#%v · %s</b>\n", a["number"], esc(nameOf(a)))
	if d := str(a["description"]); d != "" {
		fmt.Fprintf(&sb, "%s\n", esc(truncate(d, 300)))
	}
	fmt.Fprintf(&sb, "\n<code>%s</code>\n", esc(str(a["id"])))
	fmt.Fprintf(&sb, "%s · %v bytes", esc(str(a["mime"])), a["size"])
	if p := num(a["parts"]); p > 1 {
		fmt.Fprintf(&sb, " · %d parts", p)
	}
	fmt.Fprintf(&sb, "\nsupply %v · momentum %v\n", a["supply"], a["momentum"])
	status := "soft — indexed, not yet final"
	if str(a["status"]) == "full" {
		status = fmt.Sprintf("confirmed · %v momentums deep", a["confirmations"])
	}
	fmt.Fprintf(&sb, "%s\n", status)

	// "issued to", not "owner". The API's owner field is written once, at
	// the mint, and no transfer ever updates it — so on an asset that has
	// changed hands it names the wrong address, and calling it the owner
	// makes that a wrong answer rather than a stale one. Who holds it now is
	// what /holders answers, from the balances the transfers actually moved.
	fmt.Fprintf(&sb, "minted by %s\nissued to %s",
		party(str(a["issuer"])), party(str(a["owner"])))
	if refs, ok := a["refs"].([]any); ok && len(refs) > 0 {
		fmt.Fprintf(&sb, "\ncomposite · imports %d", len(refs))
	}
	return sb.String()
}

func nameOf(a map[string]any) string {
	if n := str(a["name"]); n != "" {
		return n
	}
	return "(untitled)"
}

// esc escapes the three characters Telegram's HTML mode treats as markup.
// Names and descriptions come from arbitrary on-chain blocks, so this is the
// same class of problem as the viewer's textContent rule: never hand attacker
// bytes to a parser that will act on them.
func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary so the result is still valid UTF-8.
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func num(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}
