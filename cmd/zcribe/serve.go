package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/api"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/indexer"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
	"github.com/Zenoglyphs-VM/zenoglyphs/web"
)

func cmdServe(ctx context.Context, args []string) error {
	set := fs("serve")
	node := set.String("node", defaultNode, "NoM node JSON-RPC endpoint")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	apiAddr := set.String("api", "127.0.0.1:8710", "API and viewer listen address")
	contentAddr := set.String("content", "127.0.0.1:8711", "content origin listen address")
	contentOrigin := set.String("content-origin", "", "content base URL clients should use (defaults to http://<content addr>)")
	start := set.Uint64("start", 1, "momentum height at which this deployment begins")
	depth := set.Uint64("confirmations", 60, "momentums before an inscription is reported fully confirmed")
	batch := set.Uint64("batch", 100, "momentums per RPC round trip")
	snapInterval := set.Uint64("snapshot-interval", 120, "momentums between projection checkpoints (0 disables)")
	snapRetain := set.Uint64("snapshot-retain", 360, "reorg depth, in momentums, that checkpoints must cover (0 disables)")
	scripts := set.Bool("scripts", true, "let inscribed HTML and SVG execute, which is what makes recursion work (spec R-8.5)")
	explorer := set.String("explorer", "auto", "block explorer the viewer links hashes to: "+api.ExplorerNames()+", or a URL template containing {v}")
	webDir := set.String("web", "", "serve the viewer from this directory instead of the embedded copy")
	verbose := set.Bool("v", false, "debug logging")
	evmVerify := set.Duration("evm-verify", 10*time.Minute, "how often to replay the EVM from scratch and check the incrementally derived state against it (0 disables)")

	// The EVM faucet behind the viewer's "receive test tokens" button. Opt-in
	// and off by default; see EnableEVMFaucet. There is no rate limiting on
	// this yet — see the warning logged below when it is enabled.
	faucetMnemonicFile := set.String("evm-faucet-mnemonic-file", "", "file holding a BIP-39 phrase; enables the EVM faucet")
	faucetDevnet := set.Bool("evm-faucet-devnet", false, "sign the EVM faucet with the public devnet mnemonic (chain 69 only)")
	faucetToken := set.String("evm-faucet-token", "", "ERC-20 contract address the faucet sends from, 0x… or z1… (required to enable it)")
	faucetAmount := set.String("evm-faucet-amount", "100000000000000000000", "raw units sent per faucet request (default: 100 tokens at 18 decimals)")
	_ = set.Parse(args)

	log := newLogger(*verbose)

	st, err := store.Open(*db)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	client := rpcclient.New(*node)
	cfg := indexer.Config{
		StartHeight:       *start,
		ConfirmationDepth: *depth,
		BatchSize:         *batch,
		PollInterval:      2 * time.Second,
		SnapshotInterval:  *snapInterval,
		SnapshotRetain:    *snapRetain,
	}
	// Worth saying out loud rather than leaving in a flag default: the
	// checkpoint window is how deep a reorg this indexer survives cheaply,
	// and the confirmation depth is what it tells users is final. Claiming
	// finality shallower than the depth you have prepared to unwind is a
	// promise the storage layer is not making.
	if *snapRetain > 0 && *depth > *snapRetain {
		log.Warn("confirmation depth is deeper than the checkpoint window",
			"confirmations", *depth, "snapshot_retain", *snapRetain)
	}
	ix := indexer.New(cfg, client, st, log)

	origin := *contentOrigin
	if origin == "" {
		origin = "http://" + *contentAddr
	}
	srv := api.New(st, ix, origin, log)
	srv.Scripts = *scripts

	// Validated here and stored unresolved: with "auto" the answer depends on
	// the chain identifier, which arrives with the first frontier momentum.
	// Rejecting a typo at startup rather than on the first /info is the whole
	// point of resolving it once here as well.
	if _, err := api.ResolveExplorer(*explorer, 0); err != nil {
		return err
	}
	srv.ExplorerPreset = *explorer

	// The admin panel (moderation, mainly — see internal/api/admin.go) is
	// opt-in and configured the same way every other secret in this codebase
	// is: an environment variable, never a flag, because a flag's value sits
	// in shell history and the process table (see 'zcribe moderate -h').
	// Leaving it unset is a supported choice, not a misconfiguration — it is
	// how an operator who does not want a web admin surface at all opts out.
	if pw := os.Getenv("ZCRIBE_ADMIN_PASSWORD"); pw != "" {
		if err := srv.EnableAdmin(pw); err != nil {
			return fmt.Errorf("enable admin panel: %w", err)
		}
		if len(pw) < 12 {
			log.Warn("ZCRIBE_ADMIN_PASSWORD is short; the admin panel is reachable by anyone who guesses it")
		}
		log.Info("admin panel enabled", "path", "/admin")
	} else {
		log.Info("admin panel disabled (set ZCRIBE_ADMIN_PASSWORD to enable it)")
	}

	// The EVM faucet. Like /create on the bot, this signs with an operator
	// key on a caller's behalf — but unlike the bot, nothing here throttles
	// it yet, so enabling it on a publicly reachable instance means anyone
	// who can reach the API can drain the key at will.
	if *faucetDevnet || *faucetMnemonicFile != "" {
		if *faucetToken == "" {
			return fmt.Errorf("-evm-faucet-token is required when the EVM faucet is enabled")
		}
		token, err := evm.ParseAddress(*faucetToken)
		if err != nil {
			return fmt.Errorf("-evm-faucet-token: %w", err)
		}
		amount, ok := new(big.Int).SetString(*faucetAmount, 10)
		if !ok {
			return fmt.Errorf("-evm-faucet-amount must be a decimal integer of raw units")
		}
		w := walletFlags{node: *node, mnemonicFile: *faucetMnemonicFile, devnet: *faucetDevnet, index: 1, verbose: *verbose}
		in, err := w.inscriber(ctx)
		if err != nil {
			return fmt.Errorf("EVM faucet wallet: %w", err)
		}
		srv.EnableEVMFaucet(in, token, amount)
		log.Warn("EVM faucet enabled with NO rate limiting — anyone who can reach the API can drain it",
			"address", in.Address(), "token", token.Hex(), "amount", amount)
	} else {
		log.Info("EVM faucet disabled (set -evm-faucet-mnemonic-file or -evm-faucet-devnet, plus -evm-faucet-token, to enable it)")
	}

	var ui http.Handler
	if *webDir != "" {
		abs, _ := filepath.Abs(*webDir)
		log.Info("serving viewer from disk", "dir", abs)
		ui = http.FileServer(http.Dir(abs))
	} else {
		ui = web.Handler()
	}

	// Both chains are wrapped in the access log. The content origin is
	// wrapped in the quiet variant: it answers every recursion fetch an
	// inscribed page makes, so at info level it would drown out the API it
	// sits next to. Successful content responses are debug — visible under
	// -v — and its failures are not.
	apiSrv := &http.Server{
		Addr:              *apiAddr,
		Handler:           srv.AccessLog(srv.Routes(ui), "api", false),
		ReadHeaderTimeout: 10 * time.Second,
	}
	contentSrv := &http.Server{
		Addr:              *contentAddr,
		Handler:           srv.AccessLog(srv.ContentRoutes(), "content", true),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 3)
	go func() { errs <- serveHTTP(apiSrv, "api", log) }()
	go func() { errs <- serveHTTP(contentSrv, "content", log) }()
	if *evmVerify > 0 {
		go srv.VerifyEVM(ctx, *evmVerify)
	}
	go func() {
		if err := ix.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
			return
		}
		errs <- nil
	}()

	log.Info("zenoglyphs indexer running",
		"node", *node, "api", "http://"+*apiAddr, "content", origin, "db", *db,
		"scripts", *scripts, "confirmations", *depth,
		"checkpoints", fmt.Sprintf("every %d, covering %d", *snapInterval, *snapRetain))
	if *scripts && !strings.HasPrefix(origin, "http://127.0.0.1") &&
		!strings.HasPrefix(origin, "http://localhost") &&
		strings.TrimPrefix(origin, "http://") == strings.TrimPrefix("http://"+*apiAddr, "http://") {
		log.Warn("inscribed scripts are enabled and the content origin equals the API origin; " +
			"in production these must be different hostnames (spec R-8.2)")
	}
	fmt.Fprintf(os.Stderr, "\n  viewer:  http://%s/\n  api:     http://%s/info\n  content: %s/content/<id>\n\n",
		*apiAddr, *apiAddr, origin)

	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil {
			return err
		}
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(shutdown)
	_ = contentSrv.Shutdown(shutdown)
	log.Info("stopped")
	return nil
}

func serveHTTP(s *http.Server, name string, log logger) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

type logger interface {
	Info(msg string, args ...any)
}

func cmdReindex(ctx context.Context, args []string) error {
	set := fs("reindex")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	node := set.String("node", defaultNode, "NoM node JSON-RPC endpoint")
	start := set.Uint64("start", 1, "momentum height at which this deployment begins")
	verbose := set.Bool("v", false, "debug logging")
	_ = set.Parse(args)

	log := newLogger(*verbose)
	st, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer st.Close()

	cfg := indexer.DefaultConfig()
	cfg.StartHeight = *start
	ix := indexer.New(cfg, rpcclient.New(*node), st, log)

	// Rebuild reads only the event log — the network is not consulted. That
	// is the point: if replaying the log does not reproduce the state, the
	// bug is in the projection, and no amount of refetching would show it.
	//
	// Checkpoints are not used here either, deliberately. reindex exists to
	// answer "does the log still produce this state", and starting from a
	// checkpoint would assume the answer for everything below it.
	if err := ix.Rebuild(); err != nil {
		return err
	}
	n, _ := st.EventCount()
	digest, err := st.ProjectionDigest()
	if err != nil {
		return err
	}
	fmt.Printf("rebuilt projections from %d events\n  projection digest: %s\n", n, digest)
	return nil
}

// cmdSnapshot inspects and manages the checkpoints behind A-17.
//
// A checkpoint is never authoritative: it is a memo of state the event log can
// always recompute. Deleting every one of them costs replay time on the next
// reorg and nothing else, which is why this command is allowed to be blunt.
func cmdSnapshot(ctx context.Context, args []string) error {
	set := fs("snapshot")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	list := set.Bool("list", false, "list stored checkpoints")
	at := set.Uint64("at", 0, "take a checkpoint at this indexed height")
	prune := set.Uint64("prune", 0, "delete checkpoints deeper than this many momentums below the tip")
	drop := set.Bool("drop", false, "delete every checkpoint")
	_ = set.Parse(args)

	st, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer st.Close()

	tip, err := st.TipHeight()
	if err != nil {
		return err
	}

	switch {
	case *drop:
		if err := st.DropSnapshotsAbove(0); err != nil {
			return err
		}
		fmt.Println("all checkpoints dropped; the next reorg replays the whole log")
		return nil
	case *at > 0:
		root, err := st.StateRoot(*at)
		if err != nil {
			return err
		}
		if root == "" {
			return fmt.Errorf("height %d is not indexed", *at)
		}
		if *at != tip {
			// A checkpoint records the state of the projections right now, so
			// it can only be labelled with the height they are actually at.
			return fmt.Errorf("checkpoints can only be taken at the tip (%d), not %d", tip, *at)
		}
		if err := st.PutSnapshot(*at, root); err != nil {
			return err
		}
		fmt.Printf("checkpoint written at %d\n", *at)
		return nil
	case *prune > 0:
		n, err := st.PruneSnapshots(tip, *prune)
		if err != nil {
			return err
		}
		fmt.Printf("pruned %d checkpoints; kept everything within %d momentums of %d\n", n, *prune, tip)
		return nil
	}

	_ = list
	snaps, err := st.ListSnapshots()
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Printf("no checkpoints (tip %d)\n", tip)
		return nil
	}
	fmt.Printf("tip %d\n\n%-12s %-10s %-10s %s\n", tip, "HEIGHT", "DEPTH", "EVENTS", "TAKEN")
	for _, s := range snaps {
		depth := "-"
		if tip >= s.Height {
			depth = strconv.FormatUint(tip-s.Height, 10)
		}
		fmt.Printf("%-12d %-10s %-10d %s\n", s.Height, depth, s.Events,
			time.Unix(s.CreatedAt, 0).Format(time.RFC3339))
	}
	return nil
}
