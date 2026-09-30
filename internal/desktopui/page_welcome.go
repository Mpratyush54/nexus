package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) welcomePage() fyne.CanvasObject {
	brand := widget.NewLabelWithStyle("Nexus", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	brand.TextStyle = fyne.TextStyle{Bold: true}
	headline := widget.NewLabelWithStyle("Memory that travels with your agents", fyne.TextAlignCenter, fyne.TextStyle{})
	headline.Wrapping = fyne.TextWrapWord
	support := mutedLabel("Sign in once with your browser. Harvest, workspace files, and cloud memory stay in this window.")
	support.Alignment = fyne.TextAlignCenter

	signIn := widget.NewButton("Sign in", func() {
		if s.hooks.OnSignIn != nil {
			s.hooks.OnSignIn()
		}
	})
	signIn.Importance = widget.HighImportance

	column := container.NewVBox(
		brand,
		widget.NewSeparator(),
		headline,
		support,
		widget.NewSeparator(),
		container.NewCenter(signIn),
	)
	return container.NewCenter(paddedVBox(column))
}
