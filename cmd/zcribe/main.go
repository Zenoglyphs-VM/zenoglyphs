// Command zcribe is the Zenoglyphs reference implementation: an indexer,
// a REST API with a web viewer, and an inscriber.
//
//	zcribe serve      run the indexer, API, content origin, and viewer
//	zcribe inscribe   publish a file as an inscription
//	zcribe transfer   move units of an asset
//	zcribe collection create a collection
//	zcribe dispenser  create, cancel, or buy from a dispenser
//	zcribe address    print the address a key derives to
//	zcribe sign       sign a challenge, proving control of an address
//	zcribe reindex    rebuild projections from the event log
//	zcribe vectors    write the golden test vectors
//	zcribe snapshot   inspect or manage projection checkpoints
//	zcribe bot        run the Telegram front end
//	zcribe demo       inscribe a sample set of documents
//	zcribe evm        deploy, call and derive EVM contracts held as inscriptions
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

const defaultNode = "http://localhost:35997"

type command struct {
	name string
	run  func(ctx context.Context, args []string) error
	help string
}

func main() {
	commands := []command{
		{"serve", cmdServe, "run the indexer, API, content origin, and web viewer"},
		{"inscribe", cmdInscribe, "publish a file as an inscription"},
		{"transfer", cmdTransfer, "move units of an asset to another address"},
		{"collection", cmdCollection, "create a collection"},
		{"dispenser", cmdDispenser, "create, cancel, or buy from a dispenser"},
		{"address", cmdAddress, "print the address a key derives to"},
		{"sign", cmdSign, "sign a challenge with an address's key, proving you hold it"},
		{"reindex", cmdReindex, "rebuild projections from the event log"},
		{"compare", cmdCompare, "find where two indexers stop agreeing"},
		{"moderate", cmdModerate, "set this indexer's serving policy for an asset"},
		{"snapshot", cmdSnapshot, "inspect or manage projection checkpoints"},
		{"bot", cmdBot, "run the Telegram front end"},
		{"vectors", cmdVectors, "write the golden test vectors"},
		{"demo", cmdDemo, "inscribe a sample set of documents"},
		{"evm", cmdEVM, "deploy, call and derive EVM contracts held as inscriptions"},
	}

	if len(os.Args) < 2 {
		usage(commands)
		os.Exit(2)
	}
	name := os.Args[1]
	for _, c := range commands {
		if c.name == name {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := c.run(ctx, os.Args[2:]); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				fmt.Fprintf(os.Stderr, "zcribe %s: %v\n", name, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
	usage(commands)
	os.Exit(2)
}

func usage(commands []command) {
	fmt.Fprint(os.Stderr, "zcribe — the Zenoglyphs reference implementation\n\nusage: zcribe <command> [flags]\n\n")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-11s %s\n", c.name, c.help)
	}
	fmt.Fprintln(os.Stderr, "\nrun 'zcribe <command> -h' for a command's flags")
}

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// chainID reads the node's chain identifier rather than taking it as a flag.
// A block signed for the wrong chain is rejected with an error that says
// nothing useful, so guessing here is worse than asking.
func chainID(ctx context.Context, client *rpcclient.Client) (uint64, error) {
	m, err := client.FrontierMomentum(ctx)
	if err != nil {
		return 0, fmt.Errorf("reach node: %w", err)
	}
	return m.ChainIdentifier, nil
}

func fs(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ExitOnError)
	return set
}
