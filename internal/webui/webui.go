// Package webui serves the embedded, zero-build-step read-only dashboard for mill.
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var staticFS embed.FS

// BoardAppHTML returns a single self-contained HTML document for the MCP App board UI.
// When bootstrap is non-nil, it is JSON-encoded into window.__MILL_BOOTSTRAP__.
func BoardAppHTML(bootstrap any) (string, error) {
	indexBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		return "", fmt.Errorf("read index.html: %w", err)
	}
	cssBytes, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		return "", fmt.Errorf("read app.css: %w", err)
	}
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		return "", fmt.Errorf("read app.js: %w", err)
	}

	bootstrapTag := ""
	if bootstrap != nil {
		encoded, err := json.Marshal(bootstrap)
		if err != nil {
			return "", fmt.Errorf("marshal bootstrap: %w", err)
		}
		replacer := strings.NewReplacer(
			"</", `<\/`,
			"\u2028", `\u2028`,
			"\u2029", `\u2029`,
		)
		bootstrapTag = "<script>window.__MILL_BOOTSTRAP__ = " + replacer.Replace(string(encoded)) + ";</script>\n  "
	}

	html := string(indexBytes)
	html = strings.Replace(html, `<link rel="stylesheet" href="/static/app.css">`, "<style>\n"+string(cssBytes)+"\n</style>", 1)
	html = strings.Replace(html, `<script type="module" src="/static/app.js"></script>`, bootstrapTag+"<script type=\"module\">\n"+string(jsBytes)+"\n</script>", 1)
	return html, nil
}

// Handler returns an HTTP handler serving "/" and "/static/*".
// All slice and ledger data is loaded client-side via authenticated API endpoints.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("embedded webui static missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "ui unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, "/static/")
		if trimmed == "" || strings.Contains(trimmed, "..") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.StripPrefix("/static/", fileServer).ServeHTTP(w, r)
	})
	return mux
}
