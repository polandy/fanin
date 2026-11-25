package forge

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// Kind is the forge software; Gitea and Forgejo share KindGitea.
type Kind string

// The forge kinds.
const (
	KindGitHub Kind = "github"
	KindGitea  Kind = "gitea"
)

// AllKinds lists every Kind.
var AllKinds = []Kind{KindGitHub, KindGitea}

// Resolver errors; match them with errors.Is.
var (
	// ErrWrongForge is an operation on a project whose forge is of another kind (see SCENARIOS GT-2).
	ErrWrongForge = errors.New("forge: wrong forge for the project")
	// ErrUnknownProject is a project the resolver was not built with.
	ErrUnknownProject = errors.New("forge: unknown project")
)

// Named is one configured forge: its kind and the client that talks to it.
type Named struct {
	Kind   Kind
	Client Forge
}

// Resolver picks the forge client of each project.
type Resolver struct {
	forges   map[string]Named
	projects map[string]string
}

// NewResolver builds a Resolver from the forges by name and the forge name of each project. A forge without a
// client or with an unknown kind, and a project that names a missing forge, is ErrInvalidRequest.
func NewResolver(forges map[string]Named, projects map[string]string) (*Resolver, error) {
	for name, f := range forges {
		if f.Client == nil || !slices.Contains(AllKinds, f.Kind) {
			return nil, fmt.Errorf("%w: forge %q kind %q client set %t; set kind to one of %v and a client",
				ErrInvalidRequest, name, f.Kind, f.Client != nil, AllKinds)
		}
	}
	for _, project := range slices.Sorted(maps.Keys(projects)) {
		if _, ok := forges[projects[project]]; !ok {
			return nil, fmt.Errorf("%w: project %q names forge %q, which is not configured; add it to forges.yaml",
				ErrInvalidRequest, project, projects[project])
		}
	}
	return &Resolver{forges: forges, projects: projects}, nil
}

// For returns the client and kind of project's forge.
func (r *Resolver) For(project string) (Forge, Kind, error) {
	name, ok := r.projects[project]
	if !ok {
		return nil, "", fmt.Errorf("%w: %q; add the project to the config", ErrUnknownProject, project)
	}
	f := r.forges[name]
	return f.Client, f.Kind, nil
}

// Require returns project's client only when its forge is of kind want; otherwise ErrWrongForge, such as
// "beta is not a gitea project".
func (r *Resolver) Require(project string, want Kind) (Forge, error) {
	client, kind, err := r.For(project)
	if err != nil {
		return nil, err
	}
	if kind != want {
		return nil, fmt.Errorf("%w: %s is not a %s project (its forge %q is %s)", ErrWrongForge, project, want,
			r.projects[project], kind)
	}
	return client, nil
}
