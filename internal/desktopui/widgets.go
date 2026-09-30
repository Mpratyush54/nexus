package desktopui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func sectionHeading(title string) *widget.Label {
	l := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return l
}

func mutedLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	l.Importance = widget.MediumImportance
	l.SizeName = theme.SizeNameCaptionText
	return l
}

func dimCaption(text string) *canvas.Text {
	t := canvas.NewText(text, colorFgDim)
	t.TextSize = 11
	return t
}

func pageHeader(title, subtitle string) fyne.CanvasObject {
	h := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	h.SizeName = theme.SizeNameHeadingText
	sub := mutedLabel(subtitle)
	return container.NewVBox(h, sub)
}

func leadingButton(label string, tapped func()) *widget.Button {
	btn := widget.NewButton(label, tapped)
	btn.Alignment = widget.ButtonAlignLeading
	btn.Importance = widget.LowImportance
	return btn
}

func primaryButton(label string, tapped func()) *widget.Button {
	btn := widget.NewButton(label, tapped)
	btn.Importance = widget.HighImportance
	return btn
}

func secondaryButton(label string, tapped func()) *widget.Button {
	btn := widget.NewButton(label, tapped)
	btn.Importance = widget.MediumImportance
	return btn
}

func outlineButton(label string, tapped func()) *widget.Button {
	btn := widget.NewButton(label, tapped)
	btn.Importance = widget.LowImportance
	return btn
}

func iconButton(label string, icon fyne.Resource, tapped func()) *widget.Button {
	btn := widget.NewButtonWithIcon(label, icon, tapped)
	btn.Importance = widget.LowImportance
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

// paneBG paints a surface panel behind content (reads as a column, not void).
func paneBG(content fyne.CanvasObject, bg color.Color) fyne.CanvasObject {
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = 0
	return container.NewStack(rect, content)
}

// card is a raised bordered block for interactive/status groupings.
func card(title, body string, actions ...fyne.CanvasObject) fyne.CanvasObject {
	head := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	detail := mutedLabel(body)
	inner := container.NewVBox(head, detail)
	if len(actions) > 0 {
		inner.Add(layout.NewSpacer())
		inner.Add(container.NewHBox(actions...))
	}
	return cardWrap(inner)
}

// cardWrap paints a raised bordered panel around arbitrary content.
func cardWrap(inner fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(colorRaised)
	bg.CornerRadius = 6
	border := canvas.NewRectangle(color.Transparent)
	border.StrokeColor = colorBorder
	border.StrokeWidth = 1
	border.CornerRadius = 6
	pad := container.NewPadded(inner)
	return container.NewStack(bg, border, pad)
}

// statusPill renders connection state as a compact chrome chip.
func statusPill(connected bool, label string) fyne.CanvasObject {
	dot := canvas.NewCircle(colorMuted)
	if connected {
		dot.FillColor = colorTeal
	} else {
		dot.FillColor = emberAccent
	}
	dot.Resize(fyne.NewSize(8, 8))

	chipBG := canvas.NewRectangle(colorRaised)
	chipBG.CornerRadius = 10
	chipBG.StrokeColor = colorBorder
	chipBG.StrokeWidth = 1

	txt := canvas.NewText(label, colorFg)
	txt.TextSize = 12

	inner := container.NewHBox(
		container.NewCenter(container.New(&dotSize{}, dot)),
		txt,
	)
	return container.NewStack(chipBG, container.NewPadded(inner))
}

// navRow is a sidebar item: left accent + icon + label (selected gets ember bar).
func navRow(label string, icon fyne.Resource, selected bool, onTap func()) fyne.CanvasObject {
	accent := canvas.NewRectangle(color.Transparent)
	accent.SetMinSize(fyne.NewSize(navAccentW, 1))
	if selected {
		accent.FillColor = emberAccent
	}

	bg := canvas.NewRectangle(color.Transparent)
	if selected {
		bg.FillColor = emberSoft
	}
	bg.CornerRadius = 4

	btn := widget.NewButtonWithIcon(label, icon, onTap)
	btn.Alignment = widget.ButtonAlignLeading
	btn.Importance = widget.LowImportance

	row := container.NewBorder(nil, nil, accent, nil, btn)
	return container.NewStack(bg, row)
}

// brandBlock is the sidebar header mark.
func brandBlock() fyne.CanvasObject {
	mark := canvas.NewRectangle(emberAccent)
	mark.CornerRadius = 4
	mark.SetMinSize(fyne.NewSize(22, 22))
	n := canvas.NewText("N", colorBase)
	n.TextStyle = fyne.TextStyle{Bold: true}
	n.TextSize = 13
	n.Alignment = fyne.TextAlignCenter
	badge := container.NewStack(mark, container.NewCenter(n))

	title := widget.NewLabelWithStyle("Nexus", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := dimCaption("Desktop")
	textCol := container.NewVBox(title, sub)
	return container.NewPadded(container.NewHBox(badge, textCol))
}

// listRowTemplate returns primary + muted secondary for file-manager density.
func listRowTemplate() fyne.CanvasObject {
	primary := widget.NewLabel("title")
	primary.TextStyle = fyne.TextStyle{Bold: true}
	secondary := widget.NewLabel("meta")
	secondary.Importance = widget.MediumImportance
	secondary.SizeName = theme.SizeNameCaptionText
	return container.NewPadded(container.NewVBox(primary, secondary))
}

func updateListRow(obj fyne.CanvasObject, title, subtitle string) {
	pad, ok := obj.(*fyne.Container)
	if !ok || len(pad.Objects) == 0 {
		return
	}
	box, ok := pad.Objects[0].(*fyne.Container)
	if !ok || len(box.Objects) < 2 {
		return
	}
	if l, ok := box.Objects[0].(*widget.Label); ok {
		l.SetText(title)
	}
	if l, ok := box.Objects[1].(*widget.Label); ok {
		l.SetText(subtitle)
	}
}

// fixedWidth keeps the sidebar a stable column.
type fixedWidth struct {
	width float32
}

func (f *fixedWidth) MinSize(objects []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	for _, o := range objects {
		if o == nil {
			continue
		}
		s := o.MinSize()
		if s.Height > h {
			h = s.Height
		}
	}
	return fyne.NewSize(f.width, h)
}

func (f *fixedWidth) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		if o == nil {
			continue
		}
		o.Resize(fyne.NewSize(f.width, size.Height))
		o.Move(fyne.NewPos(0, 0))
	}
}

func withFixedWidth(width float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.New(&fixedWidth{width: width}, obj)
}

// minWidth ensures a pane does not collapse below a usable column width.
type minWidth struct {
	width float32
}

func (m *minWidth) MinSize(objects []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	w := m.width
	for _, o := range objects {
		if o == nil {
			continue
		}
		s := o.MinSize()
		if s.Height > h {
			h = s.Height
		}
		if s.Width > w {
			w = s.Width
		}
	}
	return fyne.NewSize(w, h)
}

func (m *minWidth) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		if o == nil {
			continue
		}
		o.Resize(size)
		o.Move(fyne.NewPos(0, 0))
	}
}

func withMinWidth(width float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.New(&minWidth{width: width}, obj)
}

// dotSize keeps status indicator circles readable in HBox layouts.
type dotSize struct{}

func (d *dotSize) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSquareSize(10)
}

func (d *dotSize) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	side := float32(8)
	for _, o := range objects {
		if o == nil {
			continue
		}
		o.Resize(fyne.NewSquareSize(side))
		o.Move(fyne.NewPos((size.Width-side)/2, (size.Height-side)/2))
	}
}

func emptyPreviewState(msg string) fyne.CanvasObject {
	icon := widget.NewIcon(theme.VisibilityIcon())
	title := widget.NewLabelWithStyle("Select an item to preview", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	title.Importance = widget.MediumImportance
	body := mutedLabel(msg)
	body.Alignment = fyne.TextAlignCenter
	return container.NewCenter(container.NewVBox(
		container.NewCenter(icon),
		title,
		body,
	))
}

func toolbar(actions ...fyne.CanvasObject) fyne.CanvasObject {
	return container.NewHBox(actions...)
}
