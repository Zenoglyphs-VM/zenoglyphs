package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// cmdCompare finds where two indexers stop agreeing.
//
// This is the operational half of the state-root design. Publishing a chained
// commitment only helps if disagreement is cheap to localise, and it is: the
// roots are chained, so "do we agree at height h" is monotone — true for
// every height below the divergence and false at and above it. Binary search
// therefore finds the exact momentum in about log2(H) queries instead of
// diffing whole databases.
//
// The failure this catches is the one the research says killed BRC-20's early
// years: two indexers quietly computing different state with no way to tell
// which one is wrong, or when they parted.
func cmdCompare(ctx context.Context, args []string) error {
	set := fs("compare")
	a := set.String("a", "http://127.0.0.1:8710", "first indexer's API base URL")
	b := set.String("b", "http://127.0.0.1:8720", "second indexer's API base URL")
	at := set.Uint64("height", 0, "compare at this height instead of the common tip")
	_ = set.Parse(args)

	client := &http.Client{Timeout: 20 * time.Second}

	infoA, err := fetchInfo(ctx, client, *a)
	if err != nil {
		return fmt.Errorf("indexer A: %w", err)
	}
	infoB, err := fetchInfo(ctx, client, *b)
	if err != nil {
		return fmt.Errorf("indexer B: %w", err)
	}

	fmt.Printf("A  %s\n   protocol v%d prefix %q · zcribe %s · synced %d · %d events\n",
		*a, infoA.ProtocolVersion, infoA.Prefix, infoA.IndexerVersion, infoA.SyncHeight, infoA.Events)
	fmt.Printf("B  %s\n   protocol v%d prefix %q · zcribe %s · synced %d · %d events\n\n",
		*b, infoB.ProtocolVersion, infoB.Prefix, infoB.IndexerVersion, infoB.SyncHeight, infoB.Events)

	// Two indexers on different protocol versions or prefixes are not
	// disagreeing about history — they are indexing different protocols. Say
	// so rather than reporting a divergence height that means nothing.
	if infoA.Prefix != infoB.Prefix || infoA.ProtocolVersion != infoB.ProtocolVersion {
		return fmt.Errorf("these indexers implement different protocols (%q v%d vs %q v%d); nothing to compare",
			infoA.Prefix, infoA.ProtocolVersion, infoB.Prefix, infoB.ProtocolVersion)
	}
	if infoA.StartHeight != infoB.StartHeight {
		fmt.Printf("warning: different start heights (%d vs %d) — the state-root chains have different base cases\n\n",
			infoA.StartHeight, infoB.StartHeight)
	}

	top := *at
	if top == 0 {
		top = min64(infoA.SyncHeight, infoB.SyncHeight)
	}
	low := max64(infoA.StartHeight, infoB.StartHeight)
	if top < low {
		return fmt.Errorf("no common indexed range")
	}

	agree, err := rootsAgree(ctx, client, *a, *b, top)
	if err != nil {
		return err
	}
	if agree {
		root, _ := stateRoot(ctx, client, *a, top)
		fmt.Printf("AGREE at %d\n  stateRoot %s\n\nBoth indexers agree on the entire history to momentum %d.\n",
			top, root, top)
		return nil
	}

	fmt.Printf("DISAGREE at %d — searching for the first divergent momentum\n\n", top)

	// Invariant: agreement holds at `low` and fails at `high`. Each step
	// halves the window, so the loop runs about log2(top-low) times.
	high := top
	queries := 0
	if low > 0 {
		ok, err := rootsAgree(ctx, client, *a, *b, low)
		if err != nil {
			return err
		}
		queries++
		if !ok {
			fmt.Printf("They already disagree at the earliest common height (%d).\n"+
				"That points at a different start height or a different genesis, not a single bad momentum.\n", low)
			return nil
		}
	}

	for high-low > 1 {
		mid := low + (high-low)/2
		ok, err := rootsAgree(ctx, client, *a, *b, mid)
		if err != nil {
			return err
		}
		queries++
		if ok {
			low = mid
		} else {
			high = mid
		}
	}

	fmt.Printf("first divergence: momentum %d  (%d queries)\n\n", high, queries)

	ma, errA := fetchMomentum(ctx, client, *a, high)
	mb, errB := fetchMomentum(ctx, client, *b, high)
	if errA != nil || errB != nil {
		fmt.Printf("  (could not fetch momentum detail: %v %v)\n", errA, errB)
		return nil
	}

	fmt.Printf("  momentum hash   A %s\n                  B %s%s\n",
		ma.Hash, mb.Hash, same(ma.Hash == mb.Hash))
	fmt.Printf("  stateRoot(%d-1) A %s\n                  B %s\n", high, rootAt(ctx, client, *a, high-1), rootAt(ctx, client, *b, high-1))
	fmt.Printf("  stateRoot(%d)   A %s\n                  B %s\n\n", high, ma.StateRoot, mb.StateRoot)

	fmt.Printf("  inscriptions accepted in this momentum:\n")
	fmt.Printf("    A (%d): %s\n", len(ma.Txs), joinShort(ma.Txs))
	fmt.Printf("    B (%d): %s\n", len(mb.Txs), joinShort(mb.Txs))

	onlyA := difference(ma.Txs, mb.Txs)
	onlyB := difference(mb.Txs, ma.Txs)
	if len(onlyA) > 0 {
		fmt.Printf("\n  A accepted, B rejected:\n")
		for _, h := range onlyA {
			fmt.Printf("    %s\n", h)
		}
	}
	if len(onlyB) > 0 {
		fmt.Printf("\n  B accepted, A rejected:\n")
		for _, h := range onlyB {
			fmt.Printf("    %s\n", h)
		}
	}
	if len(onlyA) == 0 && len(onlyB) == 0 {
		fmt.Printf("\n  Both accepted the same blocks in the same order, so the roots diverged\n" +
			"  earlier and this height only inherits it — or the two are computing the\n" +
			"  commitment differently. Check stateRoot(h-1) above.\n")
	}
	return nil
}

// --- helpers ---

type infoResp struct {
	Prefix          string `json:"prefix"`
	ProtocolVersion uint64 `json:"protocolVersion"`
	IndexerVersion  string `json:"indexerVersion"`
	SyncHeight      uint64 `json:"syncHeight"`
	StartHeight     uint64 `json:"startHeight"`
	StateRoot       string `json:"stateRoot"`
	Events          int64  `json:"events"`
}

type momentumResp struct {
	Height    uint64   `json:"height"`
	Hash      string   `json:"hash"`
	StateRoot string   `json:"stateRoot"`
	Txs       []string `json:"txs"`
}

func getJSON(ctx context.Context, c *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: http %d", url, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

func fetchInfo(ctx context.Context, c *http.Client, base string) (*infoResp, error) {
	var i infoResp
	return &i, getJSON(ctx, c, strings.TrimRight(base, "/")+"/info", &i)
}

func fetchMomentum(ctx context.Context, c *http.Client, base string, h uint64) (*momentumResp, error) {
	var m momentumResp
	return &m, getJSON(ctx, c, fmt.Sprintf("%s/momentum/%d", strings.TrimRight(base, "/"), h), &m)
}

func stateRoot(ctx context.Context, c *http.Client, base string, h uint64) (string, error) {
	var r struct {
		StateRoot string `json:"stateRoot"`
	}
	err := getJSON(ctx, c, fmt.Sprintf("%s/stateroot/%d", strings.TrimRight(base, "/"), h), &r)
	return r.StateRoot, err
}

func rootAt(ctx context.Context, c *http.Client, base string, h uint64) string {
	r, err := stateRoot(ctx, c, base, h)
	if err != nil || r == "" {
		return "(unavailable)"
	}
	return r
}

func rootsAgree(ctx context.Context, c *http.Client, a, b string, h uint64) (bool, error) {
	ra, err := stateRoot(ctx, c, a, h)
	if err != nil {
		return false, fmt.Errorf("A at %d: %w", h, err)
	}
	rb, err := stateRoot(ctx, c, b, h)
	if err != nil {
		return false, fmt.Errorf("B at %d: %w", h, err)
	}
	return ra == rb, nil
}

func difference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, h := range b {
		inB[h] = true
	}
	var out []string
	for _, h := range a {
		if !inB[h] {
			out = append(out, h)
		}
	}
	return out
}

func joinShort(hashes []string) string {
	if len(hashes) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if len(h) > 12 {
			h = h[:12] + "…"
		}
		parts = append(parts, h)
	}
	return strings.Join(parts, " ")
}

func same(ok bool) string {
	if ok {
		return "  (same)"
	}
	return "  (DIFFERENT)"
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
