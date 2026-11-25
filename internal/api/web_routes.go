package api

import (
	"embed"
	"io/fs"
	"net/http"
)

// uiPath is where the embedded web UI is served.
const uiPath = "/ui/"

// webFS holds the static web UI (docs/dev/spec/ui.md); it has no build step and no Go files.
//
//go:embed web
var webFS embed.FS

func init() { RegisterBuiltin(registerWeb) }

func registerWeb(r *Registry) {
	files, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("api: embedded web tree is missing: " + err.Error())
	}
	r.Add(Route{Pattern: "GET /{$}", Handler: func(*Server) http.Handler {
		return http.RedirectHandler(uiPath, http.StatusFound)
	}})
	r.Add(Route{Pattern: "GET " + uiPath, Handler: func(*Server) http.Handler {
		return http.StripPrefix(uiPath, http.FileServerFS(files))
	}})
}
