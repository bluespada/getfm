package tui

import (
	"fmt"

	"github.com/atotto/clipboard"
)

// systemClipboard is the default copy implementation, using whatever the host
// provides. It is a variable so tests can substitute a recorder, and so a
// headless run can be detected without touching the real clipboard.
var systemClipboard = clipboard.WriteAll

// newClipboard returns a copy function, defaulting to the system clipboard.
func newClipboard() func(string) error {
	return func(text string) error {
		if clipboard.Unsupported {
			return fmt.Errorf("no clipboard tool found; install xclip, xsel or wl-clipboard")
		}
		return systemClipboard(text)
	}
}

// copySelected puts the id of the model under the cursor on the clipboard.
//
// The id is copied rather than the provider/id pair because that is what the
// MODEL column shows, and it is what a search or a config file expects.
func (m *model) copySelected() {
	sel, ok := m.selected()
	if !ok {
		m.status = "nothing selected to copy"
		return
	}
	if err := m.opts.Copy(sel.ID); err != nil {
		m.status = "copy failed: " + shortError(err.Error())
		return
	}
	m.status = "copied " + sel.ID
}
