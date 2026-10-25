// Package webui serves the minimal web client: a live order book, the trade
// tape, the engine and read-model sequence numbers, balances and an order
// form. It is plain HTML, CSS and JavaScript, compiled into the binary with
// go:embed, so cexd ships it with no build step and no extra files.
//
//	GET /           index.html
//	GET /ui/app.js  the client (talks to /stream/book and the /v1 API)
//	GET /ui/app.css styles
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var files embed.FS

// Handler serves the page and its assets with strict security headers.
func Handler() http.Handler {
	static, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // the embedded directory is fixed at compile time
	}
	assets := http.StripPrefix("/ui/", http.FileServerFS(static))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Content-Security-Policy: the browser may load scripts, styles and
		// connections from this server only, and runs NO inline script. Even
		// if some value ever slipped into the page as HTML, it could not run.
		// ws:// and wss:// are listed explicitly for browsers whose 'self'
		// does not cover WebSockets.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; "+
			"connect-src 'self' ws://"+r.Host+" wss://"+r.Host+"; img-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff") // never guess a content type
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache") // revalidate, so a new release is picked up

		if r.URL.Path == "/" {
			http.ServeFileFS(w, r, static, "index.html")
			return
		}
		assets.ServeHTTP(w, r)
	})
}
