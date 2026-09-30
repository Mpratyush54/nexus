package desktopui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/test"
)

// Regression: Fyne FileDialog.Resize before Show nil-derefs dialog.win via MinSize.
// This is the exact crash behind "Choose workspace folder…" + recover message.
func TestFolderOpenResizeBeforeShowPanics(t *testing.T) {
	a := test.NewTempApp(t)
	_ = a
	w := test.NewTempWindow(t, nil)
	d := dialog.NewFolderOpen(func(fyne.ListableURI, error) {}, w)

	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		d.Resize(fyne.NewSize(720, 480))
	}()
	if !panicked {
		t.Fatal("expected Resize-before-Show to panic (Fyne FileDialog.MinSize nil dialog.win)")
	}
}

func TestFolderOpenShowThenResizeOK(t *testing.T) {
	a := test.NewTempApp(t)
	_ = a
	w := test.NewTempWindow(t, nil)
	d := dialog.NewFolderOpen(func(fyne.ListableURI, error) {}, w)
	d.Show()
	d.Resize(fyne.NewSize(720, 480)) // must not panic after Show
}
