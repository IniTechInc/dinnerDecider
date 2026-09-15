// Package web carries the embedded single-page UI the service serves at `/`.
//
// Everything the browser needs ships inside the binary: markup, stylesheet,
// scripts, fonts and licences. There is no build step, no bundler and no
// external origin of any kind. That is not a preference - the router sends
// `Content-Security-Policy: default-src 'self'`, so a reference to a CDN would
// not merely be untidy, it would fail to load at runtime on a box that very
// likely has no route to the internet anyway.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"time"
)

// FS is the raw embedded tree, rooted at the package directory, so callers who
// want `static/...` paths can have them.
//
// `all:` is deliberate: it keeps files whose names begin with `.` or `_`, which
// the default pattern silently drops.
//
//go:embed all:static
var FS embed.FS

// Static is the same tree rooted at `static`, so `index.html` is at the top.
// This is what an http.FileServer wants.
func Static() fs.FS {
	sub, err := fs.Sub(FS, "static")
	if err != nil {
		// Unreachable: the directory is embedded at compile time, so a failure
		// here would mean the binary was built without its own assets.
		panic("web: embedded static tree is missing: " + err.Error())
	}
	return sub
}

// IndexHTML returns the app shell. Useful for a router that wants to serve the
// same document for any unmatched non-API path.
func IndexHTML() ([]byte, error) {
	return FS.ReadFile("static/index.html")
}

// buildTime is a fixed, non-zero modification time for every embedded file.
//
// embed.FS reports a zero ModTime, which makes http.ServeContent skip
// Last-Modified and If-Modified-Since entirely. A constant stamp gives the
// browser something to revalidate against; it changes whenever the binary is
// rebuilt because the asset bytes travel with the binary.
var buildTime = time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)

type stampedFS struct{ fs.FS }

type stampedFile struct{ fs.File }

type stampedInfo struct{ fs.FileInfo }

func (s stampedInfo) ModTime() time.Time { return buildTime }

func (f stampedFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return stampedInfo{info}, nil
}

func (s stampedFS) Open(name string) (fs.File, error) {
	f, err := s.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return stampedFile{f}, nil
}

// Handler serves the embedded UI. Mount it at `/`.
//
// It is unauthenticated on purpose: the shell has to render before the user can
// type a pairing token into it. Nothing here is user data.
func Handler() http.Handler {
	files := http.FileServer(http.FS(stampedFS{Static()}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The assets change only when the binary does, but a stale shell after
		// an upgrade is a confusing bug to chase, so revalidate every time.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
}
