package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) settingsPage() fyne.CanvasObject {
	signIn := widget.NewButton("Sign in", func() {
		if s.hooks.OnSignIn != nil {
			s.hooks.OnSignIn()
		}
	})
	signIn.Importance = widget.HighImportance
	signOut := widget.NewButton("Sign out", func() {
		if s.hooks.OnSignOut != nil {
			s.hooks.OnSignOut()
		}
		s.Refresh()
	})
	folder := widget.NewButton("Choose workspace…", func() {
		s.pickWorkspaceFolder()
	})
	updateBtn := widget.NewButton("Check for updates", func() {
		if s.hooks.OnCheckUpdate != nil {
			s.hooks.OnCheckUpdate()
		}
	})
	webPortal := widget.NewButton("Team, org & billing (web)", func() {
		if s.hooks.OnOpenWebPortal != nil {
			s.hooks.OnOpenWebPortal()
		}
	})
	webPortal.Importance = widget.LowImportance
	quit := widget.NewButton("Quit Nexus", func() {
		if s.hooks.OnQuit != nil {
			s.hooks.OnQuit()
		}
	})

	note := mutedLabel("Team, Org, Overlays, and Admin stay on the web portal until those pages exist natively (see docs/native-app-direction.md). Fyne is the long-term desktop shell.")

	return container.NewVBox(
		sectionHeading("Settings"),
		note,
		widget.NewSeparator(),
		container.NewHBox(signIn, signOut),
		folder,
		updateBtn,
		widget.NewSeparator(),
		webPortal,
		widget.NewSeparator(),
		quit,
		widget.NewLabel(versionText(s.hooks)),
	)
}
