package protocol

import (
	"path/filepath"
	"sort"
	"strings"
)

// The MIME allowlist is consensus-relevant: a mint declaring a type outside
// it is invalid, so the list cannot be a config knob and cannot be extended
// without a version bump.
//
// An open-ended mime field is a rendering-side security hole — the viewer
// decides how to treat bytes based on a string an attacker wrote. Bounding
// the set bounds what a viewer can be talked into doing. The structure is a
// deliberately small port of ord's media.rs.
//
// ASSUMPTION A-2: mime strings are compared byte-for-byte against this
// table, not parsed. "text/plain;charset=utf-8" is a distinct entry from
// "text/plain", and only the listed spelling is valid. Parsing parameters
// would mean two indexers must agree on whitespace and case folding rules
// inside the type — a needless divergence surface for zero benefit.

// Class is how a client should treat the bytes. It is advisory metadata for
// renderers and never affects validity.
type Class string

const (
	ClassImage    Class = "image"
	ClassText     Class = "text"
	ClassCode     Class = "code"
	ClassMarkdown Class = "markdown"
	ClassPDF      Class = "pdf"
	ClassAudio    Class = "audio"
	ClassVideo    Class = "video"
	ClassIframe   Class = "iframe" // executes script: must be sandboxed
	ClassBinary   Class = "binary"
)

type MediaKind struct {
	Mime       string
	Class      Class
	Extensions []string
}

// mediaTypes is the frozen v1 allowlist.
var mediaTypes = []MediaKind{
	{"application/cbor", ClassBinary, []string{"cbor"}},
	// EVM bytecode: contract code and calldata alike. ClassBinary, so no viewer
	// ever renders it — an EVM artifact is executed by a deriver replaying the
	// log (internal/evm), never by a browser opening a page. What the bytes
	// *mean* is not in the mime: the extension lane says whether an inscription
	// deploys a contract or calls one, which keeps this one entry sufficient.
	{"application/evm", ClassBinary, []string{"evm", "bin"}},
	{"application/json", ClassCode, []string{"json"}},
	{"application/pdf", ClassPDF, []string{"pdf"}},
	// WebAssembly is ClassBinary: never rendered inline, never handed to a
	// viewer as a document. It reaches a page only when an inscription fetches
	// it and instantiates it deliberately (R-8.5), which is why it needs no
	// class of its own — the executing document is the HTML that imports it,
	// and that document is already sandboxed.
	//
	// The content origin must also carry 'wasm-unsafe-eval' or instantiation
	// is blocked; see contentCSP in internal/api/api.go.
	{"application/wasm", ClassBinary, []string{"wasm"}},
	{"application/yaml", ClassCode, []string{"yaml", "yml"}},
	{"audio/mpeg", ClassAudio, []string{"mp3"}},
	{"audio/wav", ClassAudio, []string{"wav"}},
	{"font/otf", ClassBinary, []string{"otf"}},
	{"font/ttf", ClassBinary, []string{"ttf"}},
	{"font/woff2", ClassBinary, []string{"woff2"}},
	{"image/avif", ClassImage, []string{"avif"}},
	{"image/gif", ClassImage, []string{"gif"}},
	{"image/jpeg", ClassImage, []string{"jpg", "jpeg"}},
	{"image/png", ClassImage, []string{"png"}},
	{"image/svg+xml", ClassIframe, []string{"svg"}},
	{"image/webp", ClassImage, []string{"webp"}},
	{"model/gltf-binary", ClassBinary, []string{"glb"}},
	{"text/css;charset=utf-8", ClassCode, []string{"css"}},
	{"text/html;charset=utf-8", ClassIframe, []string{"html"}},
	{"text/javascript;charset=utf-8", ClassCode, []string{"js"}},
	{"text/markdown;charset=utf-8", ClassMarkdown, []string{"md"}},
	{"text/plain;charset=utf-8", ClassText, []string{"txt"}},
	{"video/mp4", ClassVideo, []string{"mp4"}},
	{"video/webm", ClassVideo, []string{"webm"}},
}

var (
	byMime map[string]MediaKind
	byExt  map[string]MediaKind
)

func init() {
	byMime = make(map[string]MediaKind, len(mediaTypes))
	byExt = make(map[string]MediaKind)
	for _, m := range mediaTypes {
		byMime[m.Mime] = m
		for _, e := range m.Extensions {
			byExt[e] = m
		}
	}
}

// MimeAllowed reports whether a mint may declare this type.
func MimeAllowed(mime string) bool {
	_, ok := byMime[mime]
	return ok
}

// ClassOf returns how a client should render the type. Unknown types are
// binary, which is the safe default — they are never rendered inline.
func ClassOf(mime string) Class {
	if m, ok := byMime[mime]; ok {
		return m.Class
	}
	return ClassBinary
}

// MimeForFile guesses a permitted mime type from a filename extension, for
// the inscriber's convenience. It is a client-side helper with no consensus
// role: the inscriber writes a mime string, and only that string is checked.
func MimeForFile(path string) (string, bool) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	m, ok := byExt[ext]
	if !ok {
		return "", false
	}
	return m.Mime, true
}

// AllowedMimes lists the allowlist, sorted, for /info and error messages.
func AllowedMimes() []string {
	out := make([]string, 0, len(mediaTypes))
	for _, m := range mediaTypes {
		out = append(out, m.Mime)
	}
	sort.Strings(out)
	return out
}
