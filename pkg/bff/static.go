package bff

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assets embed.FS

// embeddedAssets is the browser shell the front serves when none is
// configured: the entry document, its styles and the modules under shell/.
func embeddedAssets() fs.FS {
	root, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil
	}
	return root
}

// static serves the browser entry: a served file as it is, and any other path
// the page shell routes as the entry document.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" {
		if served, err := fs.Stat(s.assets, name); err == nil && !served.IsDir() {
			http.ServeFileFS(w, r, s.assets, name)
			return
		}
		// A path naming a file is not a document: answering the entry document
		// here would hand a module request HTML.
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
	}
	// The shell routes its own pages, so every unknown document path answers
	// the entry document rather than a 404.
	http.ServeFileFS(w, r, s.assets, "index.html")
}

// contentSecurityPolicy is the policy every browser document is served with:
// same-origin script, style and connection, and frames and images only for the
// origins an application was installed for (KOSD-F-18, KOSD-C-75).
func contentSecurityPolicy(configured, installed []string) string {
	frames := []string{"'self'"}
	images := []string{"'self'", "data:"}
	for _, origin := range append(append([]string(nil), configured...), installed...) {
		trimmed := strings.TrimSpace(origin)
		if trimmed == "" {
			continue
		}
		frames = append(frames, trimmed)
		if !strings.Contains(strings.Join(images, " "), trimmed) {
			images = append(images, trimmed)
		}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src " + strings.Join(images, " "),
		"font-src 'self'",
		"connect-src 'self'",
		"frame-src " + strings.Join(frames, " "),
		"frame-ancestors 'self'",
		"base-uri 'none'",
		"form-action 'self'",
		"object-src 'none'",
	}, "; ")
}
