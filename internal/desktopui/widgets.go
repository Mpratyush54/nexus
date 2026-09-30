package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func sectionHeading(title string) *widget.Label {
	l := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return l
}

func mutedLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	l.Importance = widget.LowImportance
	return l
}

func leadingButton(label string, tapped func()) *widget.Button {
	btn := widget.NewButton(label, tapped)
	btn.Alignment = widget.ButtonAlignLeading
	return btn
}

func checklistMark(done bool) string {
	if done {
		return "✓"
	}
	return "○"
}

func paddedVBox(objs ...fyne.CanvasObject) fyne.CanvasObject {
	return container.NewPadded(container.NewVBox(objs...))
}
