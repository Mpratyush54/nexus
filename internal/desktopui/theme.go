package desktopui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Portal-aligned tokens (frontend/src/index.css) — warm machine, ember sparingly.
var (
	emberAccent   = color.NRGBA{R: 0xc4, G: 0x78, B: 0x3a, A: 0xff}
	emberSoft     = color.NRGBA{R: 0xc4, G: 0x78, B: 0x3a, A: 0x24}
	colorBase     = color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xff}
	colorSurface  = color.NRGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}
	colorRaised   = color.NRGBA{R: 0x18, G: 0x18, B: 0x18, A: 0xff}
	colorBorder   = color.NRGBA{R: 0x26, G: 0x26, B: 0x26, A: 0xff}
	colorBorderHi = color.NRGBA{R: 0x3a, G: 0x3a, B: 0x3a, A: 0xff}
	colorFg       = color.NRGBA{R: 0xe4, G: 0xe1, B: 0xdb, A: 0xff}
	colorFgDim    = color.NRGBA{R: 0x9a, G: 0x96, B: 0x90, A: 0xff}
	colorMuted    = color.NRGBA{R: 0x6e, G: 0x6e, B: 0x6e, A: 0xff}
	colorTeal     = color.NRGBA{R: 0x7d, G: 0x9b, B: 0x8a, A: 0xff}
)

const (
	sidebarWidth float32 = 196
	previewMinW  float32 = 280
	chromePad    float32 = 8
	navAccentW   float32 = 3
)

type nexusTheme struct {
	base fyne.Theme
}

func newNexusTheme() fyne.Theme {
	return &nexusTheme{base: theme.DefaultTheme()}
}

func (t *nexusTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colorBase
	case theme.ColorNameButton:
		return colorRaised
	case theme.ColorNameDisabledButton:
		return colorSurface
	case theme.ColorNameForeground:
		return colorFg
	case theme.ColorNameDisabled:
		return colorMuted
	case theme.ColorNameInputBackground:
		return colorSurface
	case theme.ColorNamePlaceHolder:
		return colorMuted
	case theme.ColorNamePrimary:
		return emberAccent
	case theme.ColorNameHover:
		return color.NRGBA{R: 0x1e, G: 0x1e, B: 0x1e, A: 0xff}
	case theme.ColorNamePressed:
		return color.NRGBA{R: 0x22, G: 0x22, B: 0x22, A: 0xff}
	case theme.ColorNameSelection:
		// Subtle list highlight — never a full-width ember slab.
		return color.NRGBA{R: 0x1f, G: 0x1d, B: 0x1a, A: 0xff}
	case theme.ColorNameSeparator:
		return colorBorder
	case theme.ColorNameShadow:
		return color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x66}
	case theme.ColorNameFocus:
		return emberAccent
	case theme.ColorNameInputBorder:
		return colorBorderHi
	case theme.ColorNameHeaderBackground:
		return colorSurface
	case theme.ColorNameMenuBackground:
		return colorSurface
	case theme.ColorNameOverlayBackground:
		return color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xe6}
	case theme.ColorNameScrollBar:
		return colorBorderHi
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
		return chromePad
	case theme.SizeNameInnerPadding:
		return 8
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameInlineIcon:
		return 16
	case theme.SizeNameScrollBar:
		return 8
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameSeparatorThickness:
		return 1
	}
	return t.base.Size(name)
}
