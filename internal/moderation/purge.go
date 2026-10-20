// Package moderation holds the serving-policy machinery that lives outside
// the store: the blocklist text format shared by `zcribe moderate` and the
// admin panel, the CDN URL shapes a withheld id must be purged from, and the
// calls that do the purging.
//
// The policy itself — permitted/withheld per asset — stays a store.Moderation
// row in internal/store, deliberately: that is the part every request path
// reads on every response and it must not import net/http. This package is
// the part that makes outbound calls and holds no state of its own, so the
// CLI and the HTTP admin API both call it instead of one reimplementing the
// other and drifting.
package moderation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Entry is one line of a blocklist: an asset id and an optional operator note
// carried alongside it, purely for the operator's own records.
type Entry struct {
	ID     string
	Reason string
}

// ParseBlocklist reads the format `zcribe moderate -export` writes and
// `-import` / the admin panel's bulk import both read back: one 64-character
// hex asset id per line, blank lines and lines starting with '#' ignored, and
// text after a '#' on a data line kept as an advisory reason. Sharing one
// parser is what lets operators hand each other lists, or paste one into the
// admin panel, without either side having to agree on anything else.
func ParseBlocklist(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, reason := line, ""
		if i := strings.IndexByte(line, '#'); i >= 0 {
			id, reason = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		id = strings.ToLower(strings.Fields(id + " x")[0])
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("not a 64-character hex asset id: %q", id)
		}
		out = append(out, Entry{ID: id, Reason: reason})
	}
	return out, sc.Err()
}

// FetchBlocklist reads a blocklist from a local path or, if path is empty, an
// HTTP URL. A remote list is untrusted input from another operator, so the
// read is bounded rather than an unbounded stream.
func FetchBlocklist(ctx context.Context, path, rawURL string) ([]Entry, error) {
	var r io.Reader
	switch {
	case path != "":
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	default:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("blocklist fetch returned %d", resp.StatusCode)
		}
		r = io.LimitReader(resp.Body, 8<<20)
	}
	return ParseBlocklist(r)
}

// WriteBlocklist writes entries in the same format ParseBlocklist reads, so
// what one operator exports another can import unchanged.
func WriteBlocklist(w io.Writer, entries []Entry) {
	fmt.Fprintf(w, "# zenoglyphs blocklist, exported %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprint(w, "# One asset id per line. Text after '#' is a reason and is advisory.\n")
	for _, e := range entries {
		if e.Reason != "" {
			fmt.Fprintf(w, "%s # %s\n", e.ID, strings.ReplaceAll(e.Reason, "\n", " "))
			continue
		}
		fmt.Fprintln(w, e.ID)
	}
}

// URLsFor returns every cacheable URL shape that must be purged when an id's
// content is withheld: the canonical URL, the redirect that points at it, and
// the import-slot redirect other composites reach it through.
func URLsFor(origin string, ids []string) []string {
	origin = strings.TrimRight(origin, "/")
	urls := make([]string, 0, len(ids)*3)
	for _, id := range ids {
		urls = append(urls,
			origin+"/content/"+id+"/",
			origin+"/content/"+id,
			origin+"/r/asset/"+id)
	}
	return urls
}

// Doc explains how CDN purging is configured. It is printed by
// `zcribe moderate -purge-help` and shown by the admin panel.
const Doc = `CDN purging

Content responses carry Cache-Control: public, max-age=31536000, immutable,
because an inscription's bytes genuinely cannot change — the id is the block
hash. That is correct for caching and inconvenient for moderation: a cache
told the answer is good for a year will keep giving it after the origin stops.

So blocking an id at the origin is only half the job. Configure one of these
and a purge (` + "`zcribe moderate -purge`" + ` or the admin panel's purge action) will
do the other half.

Cloudflare (purge by URL):
    ZCRIBE_CF_ZONE     zone id from the dashboard overview
    ZCRIBE_CF_TOKEN    API token with the Zone:Cache Purge permission
    ZCRIBE_CONTENT_ORIGIN  https://content.example.org

Anything else (a generic webhook):
    ZCRIBE_PURGE_WEBHOOK   a URL that will be POSTed {"urls":[...]}
    ZCRIBE_PURGE_TOKEN     optional; sent as Authorization: Bearer <token>

With neither set, purging is unavailable: URLs can still be listed so an
operator can paste them into whatever purges their own cache.

Purging everything (` + "`zcribe moderate -purge-all`" + ` or the admin panel's "flush
entire cache") uses the same configuration. Cloudflare gets a dedicated
purge_everything call; a webhook instead receives {"purgeAll":true} with no
"urls" key, and must treat that as "ignore urls, evict all of it" — there is
no standard verb for this beyond what this project has already invented, so
document it in whatever the webhook fronts.
`

// Configured reports whether a CDN is set up to purge against, so a caller
// can decide whether to offer a purge action at all rather than let it fail.
func Configured() bool {
	zone, token := os.Getenv("ZCRIBE_CF_ZONE"), os.Getenv("ZCRIBE_CF_TOKEN")
	return (zone != "" && token != "") || os.Getenv("ZCRIBE_PURGE_WEBHOOK") != ""
}

// Purge purges urls from whichever CDN this deployment has configured via
// environment variables (see Doc), and reports which one it used so a caller
// can tell the operator what actually happened.
func Purge(ctx context.Context, urls []string) (backend string, err error) {
	zone, token := os.Getenv("ZCRIBE_CF_ZONE"), os.Getenv("ZCRIBE_CF_TOKEN")
	hook := os.Getenv("ZCRIBE_PURGE_WEBHOOK")
	switch {
	case zone != "" && token != "":
		if err := purgeCloudflare(ctx, zone, token, urls); err != nil {
			return "", err
		}
		return fmt.Sprintf("Cloudflare zone %s", zone), nil
	case hook != "":
		if err := purgeWebhook(ctx, hook, os.Getenv("ZCRIBE_PURGE_TOKEN"), urls); err != nil {
			return "", err
		}
		return "webhook", nil
	}
	return "", fmt.Errorf("no CDN purge configured; see moderation.Doc ('zcribe moderate -purge-help')")
}

// PurgeAll purges the entire cache, not just the ids named — Cloudflare's
// purge_everything, or a webhook told {"purgeAll":true}.
//
// Content is served immutable (Cache-Control: max-age=31536000): an id's
// bytes never change, so this is not the routine "the origin changed" flush
// ordinary web content needs. It exists for the coarser cases per-id purging
// cannot reach — a CDN or zone reconfigured after content was already cached
// under the old settings, or a "just get us clean" panic button when the
// exact set of stale ids is not known. Prefer Purge whenever the affected ids
// ARE known: it is far cheaper and does not evict content nobody asked about.
func PurgeAll(ctx context.Context) (backend string, err error) {
	zone, token := os.Getenv("ZCRIBE_CF_ZONE"), os.Getenv("ZCRIBE_CF_TOKEN")
	hook := os.Getenv("ZCRIBE_PURGE_WEBHOOK")
	switch {
	case zone != "" && token != "":
		if err := purgeCloudflareAll(ctx, zone, token); err != nil {
			return "", err
		}
		return fmt.Sprintf("Cloudflare zone %s", zone), nil
	case hook != "":
		if err := purgeWebhookAll(ctx, hook, os.Getenv("ZCRIBE_PURGE_TOKEN")); err != nil {
			return "", err
		}
		return "webhook", nil
	}
	return "", fmt.Errorf("no CDN purge configured; see moderation.Doc ('zcribe moderate -purge-help')")
}

func purgeCloudflareAll(ctx context.Context, zone, token string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	body, _ := json.Marshal(map[string]any{"purge_everything": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.cloudflare.com/client/v4/zones/"+zone+"/purge_cache",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudflare purge-everything returned %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

func purgeWebhookAll(ctx context.Context, hook, token string) error {
	body, _ := json.Marshal(map[string]any{"purgeAll": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("purge webhook (purge-everything) returned %d", resp.StatusCode)
	}
	return nil
}

func purgeCloudflare(ctx context.Context, zone, token string, urls []string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	// Cloudflare takes at most 30 URLs per purge call.
	for i := 0; i < len(urls); i += 30 {
		end := i + 30
		if end > len(urls) {
			end = len(urls)
		}
		body, _ := json.Marshal(map[string]any{"files": urls[i:end]})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.cloudflare.com/client/v4/zones/"+zone+"/purge_cache",
			bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("cloudflare purge returned %d: %s", resp.StatusCode, string(raw))
		}
	}
	return nil
}

func purgeWebhook(ctx context.Context, hook, token string, urls []string) error {
	body, _ := json.Marshal(map[string]any{"urls": urls})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("purge webhook returned %d", resp.StatusCode)
	}
	return nil
}
