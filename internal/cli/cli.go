// Package cli implements getfm's command line interface.
package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/paths"
)

const usage = `getfm - find free models and check that their endpoints work.

Usage:
  getfm [flags]             open the interactive browser
  getfm list [flags]        list free models from the configured providers
  getfm test [flags] [MODEL]  send a minimal completion to verify an endpoint
  getfm providers [flags]   show the configured providers and key status
  getfm help                show this help

In the browser, movement follows vim: j/k move, / searches, gg and G jump,
ctrl-d and ctrl-u page. Press t to probe the selected model and T to probe
everything currently listed. Press c or ctrl+c to copy a model id.

MODEL is written provider/model, for example openrouter/qwen/qwen3.8-27b:free.
With -all, every free model is probed instead.

Run "getfm <command> -h" for the flags of a single command.
`

// Exit codes. Usage problems are separated from upstream failures so scripts
// can tell "you called me wrong" from "the provider is down".
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Main runs the command line interface and returns a process exit code.
//
// With no arguments the interactive browser starts, since that is the tool's
// main mode. The subcommands exist for scripting.
func Main(args []string) int {
	if len(args) < 2 {
		return runTUI(nil)
	}
	command, rest := args[1], args[2:]

	var code int
	switch command {
	case "list":
		code = runList(rest)
	case "test":
		code = runTest(rest)
	case "providers":
		code = runProviders(rest)
	case "tui", "browse":
		code = runTUI(rest)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		return exitOK
	case "version", "-v", "--version":
		fmt.Println("getfm", version)
		return exitOK
	default:
		// A leading flag belongs to the browser, which accepts the same global
		// and filter flags: "getfm -providers kilocode" should not read as a
		// subcommand called "-providers".
		if strings.HasPrefix(command, "-") {
			return runTUI(args[1:])
		}
		fmt.Fprintf(os.Stderr, "getfm: unknown command %q\n\n", command)
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	return code
}

const version = "0.1.0"

// globals are the flags every subcommand accepts.
type globals struct {
	configPath string
	storePath  string
	providers  string
	keyFlags   keyList
	timeout    time.Duration
}

// keyList collects repeatable -key name=value flags.
type keyList map[string]string

func (k *keyList) String() string { return "" }

func (k *keyList) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		// The value is deliberately never echoed back: it would land in the
		// shell history and in any redirected output.
		return fmt.Errorf("expected -key provider=value")
	}
	if *k == nil {
		*k = keyList{}
	}
	(*k)[name] = value
	return nil
}

// register adds the shared flags to a flag set.
func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.configPath, "config", "", "path to providers.json (default: ./providers.json, then the user config directory, then the directory of this binary)")
	fs.StringVar(&g.storePath, "store", "", "path to the remembered model store (default: the user cache directory)")
	fs.StringVar(&g.providers, "providers", "", "comma separated providers to use (default: all)")
	fs.Var(&g.keyFlags, "key", "API key for a provider, as provider=value; repeatable")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "per request timeout")
}

// load reads the catalog and applies the -providers filter.
func (g *globals) load() (*config.File, error) {
	path, err := resolveConfigPath(g.configPath)
	if err != nil {
		return nil, err
	}
	file, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if g.providers == "" {
		return file, nil
	}

	want := map[string]bool{}
	for _, name := range strings.Split(g.providers, ",") {
		if name = strings.TrimSpace(name); name != "" {
			want[name] = true
		}
	}
	kept := file.Providers[:0]
	for _, p := range file.Providers {
		if want[p.Name] {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("no configured provider matched %q", g.providers)
	}
	filtered := &config.File{Providers: append([]config.Provider(nil), kept...)}
	return filtered, nil
}

// resolveConfigPath finds the catalog. An explicit -config wins; otherwise the
// working directory is checked first so a checkout works with no setup, then
// the per-user config directory, then the directory holding the executable.
//
// The user directory comes before the executable directory on purpose: a
// system-wide install such as C:\Program Files is read-only, so a user editing
// their catalog has to be able to put it somewhere they own.
func resolveConfigPath(explicit string) (string, error) {
	const name = "providers.json"
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file %s not found", explicit)
		}
		return explicit, nil
	}

	var tried []string
	try := func(path string) (string, bool) {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		tried = append(tried, path)
		return "", false
	}

	if path, ok := try(name); ok {
		return path, nil
	}
	if dir, err := paths.ConfigDir(); err == nil {
		if path, ok := try(filepath.Join(dir, name)); ok {
			return path, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if path, ok := try(filepath.Join(filepath.Dir(exe), name)); ok {
			return path, nil
		}
	}

	return "", fmt.Errorf("no %s found; looked in %s. Pass -config /path/to/%s",
		name, strings.Join(tried, ", "), name)
}

// newFlagSet builds a flag set that reports errors through the returned code
// rather than exiting the process itself. name is the full invocation as the
// user would type it, so the usage line reads "getfm list [flags]".
func newFlagSet(invocation string) *flag.FlagSet {
	fs := flag.NewFlagSet(invocation, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", invocation)
		fs.PrintDefaults()
	}
	return fs
}

// fail prints an error and returns the code to exit with.
func fail(err error) int {
	fmt.Fprintf(os.Stderr, "getfm: %v\n", err)
	return exitError
}

func usageErr(err error) int {
	fmt.Fprintf(os.Stderr, "getfm: %v\n", err)
	return exitUsage
}
