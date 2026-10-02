//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// notifySignals registers for the termination signals Unix actually delivers.
// os.Interrupt covers Ctrl-C everywhere; SIGTERM covers `kill` and service
// managers, which have no Windows equivalent.
func notifySignals(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
}
