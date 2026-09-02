package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:dist
var dist embed.FS

func HasIndex() bool {
	f, err := dist.Open("dist/index.html")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func Handler() http.Handler {
	return spaHandler
}

var spaHandler = newSPAHandler()

func newSPAHandler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	hfs := http.FS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || !serveFile(w, r, hfs, name) {
			if !serveFile(w, r, hfs, "index.html") {
				http.NotFound(w, r)
			}
		}
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, hfs http.FileSystem, name string) bool {
	setCacheControl(w.Header(), name)

	servedName := name
	compressedName := name + ".gz"
	if fileExists(hfs, compressedName) {
		w.Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) && r.Header.Get("Range") == "" {
			servedName = compressedName
			w.Header().Set("Content-Encoding", "gzip")
		}
	}

	f, err := hfs.Open(servedName)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	return true
}

func fileExists(hfs http.FileSystem, name string) bool {
	f, err := hfs.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && !st.IsDir()
}

func acceptsGzip(header string) bool {
	wildcard := false
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(value, ";")
		encoding := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, raw, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(key, "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				quality = 0
			} else {
				quality = parsed
			}
		}
		if encoding == "gzip" {
			return quality > 0
		}
		if encoding == "*" && quality > 0 {
			wildcard = true
		}
	}
	return wildcard
}

func setCacheControl(header http.Header, name string) {
	if strings.HasPrefix(name, "assets/") {
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	header.Set("Cache-Control", "no-cache")
}
