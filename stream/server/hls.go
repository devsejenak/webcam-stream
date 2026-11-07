package server

import (
	"net/http"
	"path"
	"path/filepath"
	"strings"
)

func HLSHandler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		if name == "." || name == "/" || strings.Contains(name, "..") {
			http.NotFound(w, r)
			return
		}

		switch path.Ext(name) {
		case ".m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-cache, no-store")
		case ".ts":
			w.Header().Set("Content-Type", "video/mp2t")
			w.Header().Set("Cache-Control", "private, max-age=60")
		default:
			http.NotFound(w, r)
			return
		}

		http.ServeFile(w, r, filepath.Join(dir, name))
	})
}
