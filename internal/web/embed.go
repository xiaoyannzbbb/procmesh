package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
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
	f, err := hfs.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	return true
}
