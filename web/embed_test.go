package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The viewer is embedded from ui/'s build output. A checkout without it, or a
// build that landed somewhere else, produces a binary that serves a blank page
// -- which looks like an indexer problem to whoever hits it. That is a
// build-time mistake and belongs here rather than in a bug report.
func TestViewerIsEmbedded(t *testing.T) {
	index, err := fs.ReadFile(files, "dist/index.html")
	if err != nil {
		t.Fatalf("no embedded viewer build: %v (run `npm run build` in web/ui)", err)
	}
	if !strings.Contains(string(index), "<script") {
		t.Error("embedded index.html loads no script; the build looks empty")
	}

	// The bundles live under dist/static/ rather than Vite's default
	// dist/assets/, because the API already answers GET /assets with the
	// inscription list. If a future config change moves them back, the two
	// namespaces collide and this is where it should surface.
	entries, err := fs.ReadDir(files, "dist/static")
	if err != nil {
		t.Fatalf("no dist/static in the build: %v", err)
	}
	if len(entries) == 0 {
		t.Error("dist/static is empty")
	}
}

// Every asset index.html references has to be reachable through the handler --
// including the fonts, which are what keep the viewer working with no network.
func TestHandlerServesReferencedAssets(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	index, err := fs.ReadFile(files, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}

	var checked int
	for _, field := range strings.Split(string(index), `"`) {
		if !strings.HasPrefix(field, "./static/") && !strings.HasPrefix(field, "./favicon") {
			continue
		}
		path := strings.TrimPrefix(field, ".")
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, res.StatusCode)
		}
		checked++
	}
	if checked == 0 {
		t.Error("index.html references no local assets; the build looks wrong")
	}
}
