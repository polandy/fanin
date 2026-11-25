// Command schemacheck loads each config directory it is given and exits 1 on the first that fails.
package main

import (
	"fmt"
	"os"

	"github.com/polandy/fanin/internal/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dirs []string) error {
	if len(dirs) == 0 {
		return fmt.Errorf("usage: schemacheck DIR...; pass at least one config directory")
	}
	for _, d := range dirs {
		cfg, err := config.Load(d)
		if err != nil {
			return fmt.Errorf("schemacheck: %w", err)
		}
		fmt.Printf("schemacheck: %s ok (%d projects)\n", d, len(cfg.Projects))
	}
	return nil
}
