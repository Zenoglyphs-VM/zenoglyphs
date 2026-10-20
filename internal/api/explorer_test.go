package api

import "testing"

// The contract this file pins down is not "the URLs are right" — those were
// checked against the live sites and can only be rechecked there — but that
// the *shape* holds: auto picks by chain, an unknown chain gets nothing rather
// than a guess, and a preset that cannot address an object leaves that
// template empty so the viewer renders plain text instead of a dead link.

func TestResolveExplorerAuto(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chainID uint64
		want    string
	}{
		{"mainnet", 1, "zenonhub"},
		{"devnet", 69, "devnet explorer"},
		{"unknown chain gets no explorer", 12345, ""},
		{"node not yet answered", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ResolveExplorer("auto", tc.chainID)
			if err != nil {
				t.Fatalf("auto on chain %d: %v", tc.chainID, err)
			}
			if e.Name != tc.want {
				t.Errorf("name = %q, want %q", e.Name, tc.want)
			}
			// An explorer with no name must have no links either, or the
			// viewer would render an unlabelled link out to nowhere.
			if tc.want == "" && (e.Account != "" || e.Transaction != "" || e.Momentum != "" || e.Token != "") {
				t.Errorf("unnamed explorer still carries templates: %+v", e)
			}
		})
	}

	// The empty string is the flag's zero value and must behave as "auto"
	// rather than as an unknown preset, so a Server built by hand still links.
	if e, err := ResolveExplorer("", 1); err != nil || e.Name != "zenonhub" {
		t.Errorf(`ResolveExplorer("", 1) = %+v, %v; want the zenonhub preset`, e, err)
	}
}

func TestResolveExplorerPresets(t *testing.T) {
	// zenonhub has no per-momentum page. That absence is load-bearing: the
	// viewer keys off it to decide whether a height is a link, so a preset
	// that grew a momentum template by accident would ship 404s.
	zh, err := ResolveExplorer("zenonhub", 0)
	if err != nil {
		t.Fatal(err)
	}
	if zh.Momentum != "" {
		t.Errorf("zenonhub momentum template = %q, want empty (no such page exists)", zh.Momentum)
	}
	for name, tpl := range map[string]string{
		"account": zh.Account, "transaction": zh.Transaction, "token": zh.Token,
	} {
		if tpl == "" {
			t.Errorf("zenonhub %s template is empty", name)
		}
	}

	// The devnet bundle is the only one that can address a momentum, which is
	// what makes it useful before mainnet.
	dev, err := ResolveExplorer("devnet", 0)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Momentum == "" {
		t.Error("devnet preset should be able to link a momentum")
	}

	if none, err := ResolveExplorer("none", 1); err != nil || none != (Explorer{}) {
		t.Errorf(`ResolveExplorer("none", 1) = %+v, %v; want the zero Explorer`, none, err)
	}
}

func TestResolveExplorerTemplateAndErrors(t *testing.T) {
	// The escape hatch: a URL nobody wrote a preset for.
	custom := "https://example.test/tx/{v}"
	e, err := ResolveExplorer(custom, 1)
	if err != nil {
		t.Fatal(err)
	}
	if e.Transaction != custom {
		t.Errorf("transaction = %q, want %q", e.Transaction, custom)
	}
	// A custom template overrides the chain rather than merging with it —
	// otherwise "auto plus one override" would silently link half the page to
	// one explorer and half to another.
	if e.Account != "" || e.Momentum != "" {
		t.Errorf("custom template should not inherit preset links: %+v", e)
	}

	if _, err := ResolveExplorer("zenonhu", 1); err == nil {
		t.Error("a typo'd preset must be rejected, not silently ignored")
	}
}

// Every preset's templates must contain the placeholder, or the substitution
// in the viewer is a no-op and every link points at the same page.
func TestExplorerTemplatesCarryPlaceholder(t *testing.T) {
	for name, e := range explorerPresets {
		for field, tpl := range map[string]string{
			"account": e.Account, "transaction": e.Transaction,
			"momentum": e.Momentum, "token": e.Token,
		} {
			if tpl != "" && !contains(tpl, "{v}") {
				t.Errorf("preset %q field %q = %q, missing {v}", name, field, tpl)
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
