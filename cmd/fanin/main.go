// Command fanin is the fanin CLI. It is a stub until the daemon API lands.
package main

import (
	"fmt"
	"os"

	"github.com/polandy/fanin/internal/version"
)

func main() {
	fmt.Println(version.String("fanin"))
	fmt.Fprintln(os.Stderr, "fanin: concept stage, nothing runnable yet; see docs/dev/concept.md")
	os.Exit(1)
}
