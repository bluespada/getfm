package cli

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/bluespada/getfm/internal/tui"
	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/store"
)

// runTUI starts the interactive browser, falling back to a plain listing when
// there is no terminal to draw on.
func runTUI(args []string) int {
	fs := newFlagSet("getfm")
	var g globals
	g.register(fs)
	var f filterFlags
	f.register(fs)

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rules, err := f.rules()
	if err != nil {
		return usageErr(err)
	}
	file, err := g.load()
	if err != nil {
		return fail(err)
	}

	ctx, cancel := commandContext()
	defer cancel()

	if !isTerminal() {
		fmt.Fprintln(os.Stderr, "getfm: no terminal detected, showing the listing instead")
		fmt.Fprintln(os.Stderr, "         run getfm list for the same output")
		return runList(append([]string{}, args...))
	}

	// The store is optional. When it cannot be read the browser still works,
	// it just cannot tell a new model from a familiar one.
	storePath, err := resolveStorePath(g.storePath)
	if err != nil {
		return fail(err)
	}
	remembered, err := store.Load(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "getfm: %v; new models cannot be detected\n", err)
	}

	err = tui.Run(ctx, tui.Options{
		Catalog:   file,
		Rules:     rules,
		Key:       func(p config.Provider) string { return keyFor(p, &g) },
		Timeout:   g.timeout,
		Store:     remembered,
		StorePath: storePath,
	})
	if err != nil {
		return fail(err)
	}
	return exitOK
}

// resolveStorePath prefers an explicit -store and otherwise defers to the
// platform's cache directory.
func resolveStorePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return store.DefaultPath()
}

// isTerminal reports whether stdout is attached to a terminal, which the
// browser needs in order to take over the screen.
//
// This asks the terminal package rather than inspecting the file mode, because
// a Windows console handle is not reliably reported as a character device and
// x/term knows about console modes and ConPTY.
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
