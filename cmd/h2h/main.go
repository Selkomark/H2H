// Command h2h supervises agent harness sessions.
package main

import (
	"os"

	"github.com/Selkomark/H2H/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
