// Package web carries the inscription viewer.
//
// The viewer is a Vue 3 + Vite application under ui/, built to dist/ and
// embedded here, so `zcribe serve` still needs nothing on disk. The build step
// is new; the property the old hand-written page had is not, and it is the one
// that matters: nothing is fetched at runtime. Every dependency, the webfonts
// included, is bundled into dist/, so the viewer renders identically on a
// machine with no internet running against a local devnet — a page that
// silently depends on a remote script is a page that renders differently
// depending on the network.
//
// Rebuild after changing anything under ui/:
//
//	cd web/ui && npm install && npm run build
//
// dist/ is committed for that reason: `go build` embeds it, and a checkout
// without it does not compile. `zcribe serve -web <dir>` serves the viewer
// from disk instead, which is what `npm run dev` is for.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// all: is required because the build output can contain files whose names
// start with an underscore, which the default embed pattern skips.
//
//go:embed all:dist
var files embed.FS

// Handler serves the viewer at the root path.
//
// The viewer uses hash routing (#/asset/…), so every route resolves to
// index.html without the server needing to know the app's routes, and a
// missing file is a genuine 404 rather than a silently rewritten page.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
