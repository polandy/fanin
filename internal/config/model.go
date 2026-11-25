package config

import (
	"fmt"
	"slices"
)

// WorkerModel returns the model a worker of p runs with: the owner's, else the project's, else the picked one,
// else the kind's default (PK-5). A kind without models, such as opencode, takes none: the result is "".
func (c Config) WorkerModel(p Project, owner, picked string) (string, error) {
	kind, ok := c.WorkerKinds[p.WorkerKind]
	if !ok {
		return "", fmt.Errorf("%w: project %s: worker kind %q is not in %s", ErrModel, p.Name, p.WorkerKind,
			fileWorkerKinds)
	}
	if len(kind.Models) == 0 {
		return "", nil
	}
	for _, m := range []string{owner, p.Model, picked, kind.DefaultModel} {
		if m == "" {
			continue
		}
		if !slices.Contains(kind.Models, m) {
			return "", fmt.Errorf("%w: project %s: model %q is not one of kind %s's models %v; pick one of them",
				ErrModel, p.Name, m, p.WorkerKind, kind.Models)
		}
		return m, nil
	}
	return "", fmt.Errorf("%w: project %s: no model for kind %s; set default_model in %s", ErrModel, p.Name,
		p.WorkerKind, fileWorkerKinds)
}
