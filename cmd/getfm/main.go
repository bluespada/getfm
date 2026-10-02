// Command getfm finds free models across configurable providers and checks
// that their endpoints actually work.
package main

import (
	"os"

	"github.com/bluespada/getfm/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
