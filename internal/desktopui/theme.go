package desktopui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// emberAccent is the Shell v2 primary (warm copper, not gold/purple).
var emberAccent = color.NRGBA{R: 0xc4, G: 0x78, B: 0x3a, A: 0xff}

// nexusTheme is a warm neutral palette with ember primary.
type nexusTheme struct {
	base fyne.Theme
}

func newNexusTheme() fyne.Theme {
	return &nexusTheme{base: theme.DefaultTheme()}
}

func (t *nexusTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 0x14, G: 0x13, B: 0x11, A: 0xff}
	case theme.ColorNameButton:
		return color.NRGBA{R: 0x2a, G: 0x28, B: 0x24, A: 0xff}
	case theme.ColorNameDisabledButton:
		return color.NRGBA{R: 0x22, G: 0x21, B: 0x1e, A: 0xff}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 0xe8, G: 0xe4, B: 0xdc, A: 0xff}
	case theme.ColorNameInputBackground:
		return color.NRGBA{R: 0x1c, G: 0x1b, B: 0x18, A: 0xff}
	case theme.ColorNamePlaceHolder:
		return color.NRGBA{R: 0x8a, G: 0x84, B: 0x7a, A: 0xff}
	case theme.ColorNamePrimary:
		return emberAccent
	case theme.ColorNameHover:
		return color.NRGBA{R: 0x2e, G: 0x2c, B: 0x28, A: 0xff}
	case theme.ColorNameSelection:
		// Soft machine highlight — avoid the giant orange full-width bar.
		return color.NRGBA{R: 0x3a, G: 0x36, B: 0x30, A: 0xff}
	case theme.ColorNameSeparator:
		return color.NRGBA{R: 0x32, G: 0x30, B: 0x2b, A: 0xff}
	case theme.ColorNameShadow:
		return color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x66}
	case theme.ColorNameFocus:
		return emberAccent
	case theme.ColorNameInputBorder:
		return color.NRGBA{R: 0x3a, G: 0x36, B: 0x30, A: 0xff}
	}
	return t.base.Color(name, variant)
}

func (t *nexusTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *nexusTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *nexusTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 10
	case theme.SizeNameInnerPadding:
		return 8
	case theme.SizeNameText:
		return 13
	case theme.SizeNameHeadingText:
		return 18
	}
	return t.base.Size(name)
}
