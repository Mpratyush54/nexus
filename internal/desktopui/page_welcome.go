package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) welcomePage() fyne.CanvasObject {
	brand := widget.NewLabelWithStyle("Nexus", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	brand.SizeName = theme.SizeNameHeadingText
	headline := widget.NewLabelWithStyle("Memory that travels with your agents", fyne.TextAlignCenter, fyne.TextStyle{})
	headline.Wrapping = fyne.TextWrapWord
	support := mutedLabel("Sign in once with your browser. Harvest, workspace files, and cloud memory stay in this window.")
	support.Alignment = fyne.TextAlignCenter

	signIn := primaryButton("Sign in", func() {
		if s.hooks.OnSignIn != nil {
			s.hooks.OnSignIn()
		}
	})

	column := container.NewVBox(
		container.NewCenter(brandBlock()),
		brand,
		headline,
		support,
		widget.NewSeparator(),
		container.NewCenter(signIn),
	)
	return container.NewCenter(paddedVBox(column))
}
