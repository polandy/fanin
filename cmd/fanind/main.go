// Command fanind is the fanin daemon. It is a stub until the scheduler lands.
package main

import (
	"fmt"
	"os"

	"github.com/polandy/fanin/internal/version"
)

func main() {
	fmt.Println(version.String("fanind"))
	fmt.Fprintln(os.Stderr, "fanind: concept stage, nothing runnable yet; see docs/dev/concept.md")
	os.Exit(1)
}
