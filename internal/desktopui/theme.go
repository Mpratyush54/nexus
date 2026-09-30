package desktopui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// nexusTheme is a warm neutral palette (aligned with the web portal, not default purple).
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
		return color.NRGBA{R: 0xc4, G: 0xa8, B: 0x6a, A: 0xff}
	case theme.ColorNameHover:
		return color.NRGBA{R: 0x34, G: 0x32, B: 0x2c, A: 0xff}
	case theme.ColorNameSelection:
		return color.NRGBA{R: 0x5c, G: 0x53, B: 0x46, A: 0xff}
	case theme.ColorNameSeparator:
		return color.NRGBA{R: 0x32, G: 0x30, B: 0x2b, A: 0xff}
	case theme.ColorNameShadow:
		return color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x66}
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
