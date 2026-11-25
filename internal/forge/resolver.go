package forge

import "errors"

// Kind is the forge software.
type Kind string

// The forge kinds.
const (
	KindGitHub Kind = "github"
	KindGitea  Kind = "gitea"
)

// Resolver errors.
var (
	// ErrWrongForge is an operation on a project whose forge is of another kind.
	ErrWrongForge = errors.New("forge: wrong forge for the project")
	// ErrUnknownProject is a project the resolver was not built with.
	ErrUnknownProject = errors.New("forge: unknown project")
)

// Named is one configured forge.
type Named struct {
	Kind   Kind
	Client Forge
}

// Resolver picks the forge client of each project.
type Resolver struct{}

// NewResolver builds a Resolver.
func NewResolver(map[string]Named, map[string]string) (*Resolver, error) { return &Resolver{}, nil }

// For returns the client and kind of project's forge.
func (r *Resolver) For(string) (Forge, Kind, error) { return nil, "", nil }

// Require returns project's client when its forge is of kind want.
func (r *Resolver) Require(string, Kind) (Forge, error) { return nil, nil }
