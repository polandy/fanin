package api

import (
	"net/http"
	"slices"
	"sync"
)

// Auth is who may call a command endpoint.
type Auth int

// The command endpoint guards; the zero value is the owner token.
const (
	// AuthOwner needs the owner token.
	AuthOwner Auth = iota
	// AuthRun needs a bearer token that the handler checks against the run in the path.
	AuthRun
)

// Route is one endpoint: a Go 1.22 mux pattern ("GET /api/v1/health") and the handler built for a Server.
// A pattern whose method is not GET or HEAD is a command endpoint and always needs the token Auth names.
type Route struct {
	Pattern string
	Handler func(*Server) http.Handler
	Auth    Auth
}

// Registry collects the routes of the API. Each file adds its own, so strands never edit one shared list.
type Registry struct {
	mu     sync.Mutex
	routes []Route
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers route.
func (r *Registry) Add(route Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, route)
}

// Routes returns the registered routes in registration order.
func (r *Registry) Routes() []Route {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.routes)
}

// Patterns returns the registered patterns, sorted.
func (r *Registry) Patterns() []string {
	var out []string
	for _, rt := range r.Routes() {
		out = append(out, rt.Pattern)
	}
	slices.Sort(out)
	return out
}

var (
	builtinMu sync.Mutex
	builtins  []func(*Registry)
)

// RegisterBuiltin lists fn to run on every registry that RegisterBuiltins fills. A file calls it from init
// with the function that adds its routes.
func RegisterBuiltin(fn func(*Registry)) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	builtins = append(builtins, fn)
}

// RegisterBuiltins adds the routes of every file that called RegisterBuiltin to r.
func RegisterBuiltins(r *Registry) {
	builtinMu.Lock()
	fns := slices.Clone(builtins)
	builtinMu.Unlock()
	for _, fn := range fns {
		fn(r)
	}
}
