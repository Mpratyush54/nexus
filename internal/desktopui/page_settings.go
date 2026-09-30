package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) settingsPage() fyne.CanvasObject {
	signIn := primaryButton("Sign in", func() {
		if s.hooks.OnSignIn != nil {
			s.hooks.OnSignIn()
		}
	})
	signOut := outlineButton("Sign out", func() {
		if s.hooks.OnSignOut != nil {
			s.hooks.OnSignOut()
		}
		s.Refresh()
	})
	folder := secondaryButton("Choose workspace…", func() {
		s.pickWorkspaceFolder()
	})
	updateBtn := outlineButton("Check for updates", func() {
		if s.hooks.OnCheckUpdate != nil {
			s.hooks.OnCheckUpdate()
		}
	})
	webPortal := outlineButton("Team, org & billing (web)", func() {
		if s.hooks.OnOpenWebPortal != nil {
			s.hooks.OnOpenWebPortal()
		}
	})
	quit := outlineButton("Quit Nexus", func() {
		if s.hooks.OnQuit != nil {
			s.hooks.OnQuit()
		}
	})

	note := mutedLabel("Team, Org, Overlays, and Admin stay on the web portal until those pages exist natively. Fyne is the long-term desktop shell.")

	return container.NewVBox(
		pageHeader("Settings", "Account, workspace, updates, and quit."),
		note,
		widget.NewSeparator(),
		toolbar(signIn, signOut),
		toolbar(folder, updateBtn),
		widget.NewSeparator(),
		webPortal,
		widget.NewSeparator(),
		quit,
		mutedLabel(versionText(s.hooks)),
	)
}
