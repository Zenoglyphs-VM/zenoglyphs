package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/moderation"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// Moderation is deliberately weak: it is a *serving* decision, not a protocol
// one. It never touches the event log, never changes validity, and never
// enters the state root — so an operator who hides an asset still agrees with
// every other operator about what the chain contains. That separation is what
// keeps "we index the same history" and "we serve the same bytes" as two
// independent claims, and only the first one has to be universal.
//
// The bytes remain on the chain regardless. This withholds them from *this*
// indexer's HTTP surface; it is not deletion and must not be described as it.
// Anyone who disagrees with an operator's list runs their own indexer and
// serves the content — which is the property that makes it acceptable for an
// operator to have a list at all.
//
// Three things this command has to get right, all of which follow from the
// worst case being illegal material rather than a copyright complaint:
//
//   - It must work on an id this indexer has not seen yet. Waiting until the
//     content is indexed to be allowed to refuse it means serving it first.
//   - It must be bulk. A list arriving from another operator, or from a
//     clearing house, is a file, not a sequence of hand-typed hashes.
//   - It must reach the CDN. A cache in front of the content origin will keep
//     answering with bytes the origin has stopped serving, so a block that
//     stops at the database is not a block.

func cmdModerate(ctx context.Context, args []string) error {
	set := fs("moderate")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	asset := set.String("asset", "", "asset id to set policy for")
	hideMeta := set.Bool("hide", false, "withhold the asset from listings and metadata responses")
	hideData := set.Bool("hide-content", false, "withhold the content bytes but keep the metadata")
	allow := set.Bool("allow", false, "clear any restriction on this asset")
	reason := set.String("reason", "", "note recorded alongside the policy, for the operator's own records")
	list := set.Bool("list", false, "list every asset with a policy set")
	imp := set.String("import", "", "block every id in a blocklist file (one id per line, '#' comments)")
	impURL := set.String("import-url", "", "block every id in a blocklist fetched over HTTP")
	export := set.Bool("export", false, "write this indexer's blocklist to stdout in the import format")
	pre := set.Bool("unseen", false, "allow setting policy for an id this indexer has not indexed yet")
	purge := set.Bool("purge", false, "purge the affected URLs from the CDN after applying (see -purge-help)")
	purgeAll := set.Bool("purge-all", false, "purge the ENTIRE CDN cache, not just specific ids, then exit (see -purge-help)")
	purgeHelp := set.Bool("purge-help", false, "explain how CDN purging is configured, then exit")
	contentOrigin := set.String("content-origin", "", "public content base URL, needed to build purge URLs")
	_ = set.Parse(args)

	if *purgeHelp {
		fmt.Print(moderation.Doc)
		return nil
	}
	if *purgeAll {
		return purgeEverything(ctx)
	}

	st, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer st.Close()

	switch {
	case *export:
		return exportBlocklist(st)
	case *imp != "" || *impURL != "":
		entries, err := moderation.FetchBlocklist(ctx, *imp, *impURL)
		if err != nil {
			return err
		}
		return importBlocklist(ctx, st, entries, *contentOrigin, *purge)
	case *list:
		return listModeration(st)
	}

	if *asset == "" {
		return fmt.Errorf("-asset is required (or pass -list, -import, or -export)")
	}
	id := strings.ToLower(strings.TrimSpace(*asset))

	// An id this indexer has never seen is usually a typo, and a typo that
	// sits in the table doing nothing leaves the operator believing something
	// is hidden that is being served. But it is also how a pre-emptive block
	// looks — an id from someone else's list, for content this indexer has not
	// reached yet — and that case has to be possible or the only way to refuse
	// material is to serve it first. So it is refused by default and allowed
	// explicitly.
	a, err := st.Asset(id)
	if err != nil {
		return err
	}
	if a == nil && !*pre {
		return fmt.Errorf("no asset %s in this database.\n"+
			"Check the id, let the indexer catch up, or pass -unseen to block it pre-emptively.", id)
	}

	m := store.Moderation{MetadataPermitted: true, DataPermitted: true, Reason: *reason}
	switch {
	case *allow:
		// leave both permitted
	case *hideMeta:
		// Hiding the metadata implies hiding the bytes: serving content for an
		// asset that appears nowhere would be a policy with a hole in it.
		m.MetadataPermitted, m.DataPermitted = false, false
	case *hideData:
		m.DataPermitted = false
	default:
		return fmt.Errorf("choose one of -hide, -hide-content, or -allow")
	}

	if err := st.SetModeration(id, m); err != nil {
		return err
	}

	name := "(not indexed here yet)"
	if a != nil {
		name = a.Name
	}
	fmt.Printf("policy set for %s (%s)\n  metadata: %s\n  content:  %s\n",
		id, name, permitted(m.MetadataPermitted), permitted(m.DataPermitted))
	if m.Reason != "" {
		fmt.Printf("  reason:   %s\n", m.Reason)
	}
	fmt.Print("\nThis affects what this indexer serves. The inscription remains on the\n" +
		"chain, other indexers are unaffected, and the state root does not change.\n")

	if !m.DataPermitted {
		return afterBlock(ctx, []string{id}, *contentOrigin, *purge)
	}
	return nil
}

func listModeration(st *store.Store) error {
	rows, err := st.ListModeration()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no moderation policies set")
		return nil
	}
	fmt.Printf("%-66s %-9s %-9s %s\n", "ASSET", "METADATA", "CONTENT", "REASON")
	for _, r := range rows {
		fmt.Printf("%-66s %-9s %-9s %s\n", r.AssetID,
			permitted(r.MetadataPermitted), permitted(r.DataPermitted), r.Reason)
	}
	return nil
}

// exportBlocklist writes this operator's list in the same format -import
// reads, so operators can hand lists to each other — or paste one into the
// admin panel — without either side having to agree about anything else.
func exportBlocklist(st *store.Store) error {
	rows, err := st.ListModeration()
	if err != nil {
		return err
	}
	entries := make([]moderation.Entry, 0, len(rows))
	for _, r := range rows {
		if r.MetadataPermitted && r.DataPermitted {
			continue
		}
		entries = append(entries, moderation.Entry{ID: r.AssetID, Reason: r.Reason})
	}
	moderation.WriteBlocklist(os.Stdout, entries)
	return nil
}

// importBlocklist applies a whole list. Every entry is a full block — metadata
// and content both — because a shared list exists for material an operator
// must not serve at all, not for one they would rather de-emphasise.
func importBlocklist(ctx context.Context, st *store.Store, entries []moderation.Entry, origin string, purge bool) error {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		reason := e.Reason
		if reason == "" {
			reason = "imported blocklist"
		}
		if err := st.SetModeration(e.ID, store.Moderation{Reason: reason}); err != nil {
			return err
		}
		ids = append(ids, e.ID)
	}
	fmt.Printf("blocked %d id(s)\n", len(ids))
	return afterBlock(ctx, ids, origin, purge)
}

// afterBlock deals with everything in front of the origin.
//
// A CDN is the whole point of putting one there: it answers without asking.
// That also means an id blocked at the origin keeps being served from cache
// until the cache is told otherwise, for as long as the immutable
// Cache-Control on content responses says it may — a year. So the URLs are
// always printed, and purged automatically when credentials are configured.
func afterBlock(ctx context.Context, ids []string, origin string, purge bool) error {
	if origin == "" {
		origin = os.Getenv("ZCRIBE_CONTENT_ORIGIN")
	}
	if origin == "" {
		fmt.Print("\nCDN: no content origin known, so no purge URLs were built.\n" +
			"Pass -content-origin or set ZCRIBE_CONTENT_ORIGIN if this indexer sits behind a cache.\n")
		return nil
	}

	urls := moderation.URLsFor(origin, ids)
	fmt.Printf("\nCDN: %d URL(s) must be purged:\n", len(urls))
	for _, u := range urls {
		fmt.Println("  " + u)
	}
	if !purge {
		fmt.Print("Re-run with -purge to do it now, or purge them yourself.\n" +
			"Run 'zcribe moderate -purge-help' for how to configure it.\n")
		return nil
	}
	backend, err := moderation.Purge(ctx, urls)
	if err != nil {
		return err
	}
	fmt.Printf("purged %d URL(s) via %s\n", len(urls), backend)
	return nil
}

// purgeEverything is -purge-all: evict the whole cache rather than a list of
// URLs. It takes no ids and needs no content origin, unlike afterBlock —
// there is nothing to build URLs for when the point is "all of it".
func purgeEverything(ctx context.Context) error {
	backend, err := moderation.PurgeAll(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("purged the entire cache via %s\n", backend)
	return nil
}

func permitted(ok bool) string {
	if ok {
		return "served"
	}
	return "withheld"
}
