//go:build windows

package cli

import (
	"os"
	"os/signal"
)

// notifySignals registers for Ctrl-C and Ctrl-Break, which are the only
// termination signals Go delivers on Windows. There is no SIGTERM equivalent,
// so asking for one would register a signal that never fires.
func notifySignals(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt)
}
