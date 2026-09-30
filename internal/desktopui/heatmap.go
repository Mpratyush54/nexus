package desktopui

import (
	"fmt"
	"image/color"
	"time"

	"central-memory/internal/cloudclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// heatLevel maps an event count to a GitHub-style intensity 0–4.
func heatLevel(count int) int {
	switch {
	case count <= 0:
		return 0
	case count == 1:
		return 1
	case count <= 3:
		return 2
	case count <= 6:
		return 3
	default:
		return 4
	}
}

func heatColor(level int) color.Color {
	switch level {
	case 1:
		return color.NRGBA{R: 0x3a, G: 0x2e, B: 0x1f, A: 0xff}
	case 2:
		return color.NRGBA{R: 0x6b, G: 0x4a, B: 0x28, A: 0xff}
	case 3:
		return color.NRGBA{R: 0x9a, G: 0x62, B: 0x2e, A: 0xff}
	case 4:
		return emberAccent
	default:
		return colorRaised
	}
}

// heatCell is one day in the contribution grid.
type heatCell struct {
	Date  string
	Count int
	Level int
}

// buildHeatCells expands sparse heatmap days into a Sunday-aligned year grid
// (same shape as the web ActivityHeatmap).
func buildHeatCells(days []cloudclient.HeatDay, now time.Time) (cells []heatCell, total int) {
	byDate := make(map[string]int, len(days))
	for _, d := range days {
		if d.Date == "" {
			continue
		}
		byDate[d.Date] = d.Count
	}
	now = now.UTC().Truncate(24 * time.Hour)
	start := now.AddDate(0, 0, -364)
	for start.Weekday() != time.Sunday {
		start = start.AddDate(0, 0, -1)
	}
	for d := start; !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		n := byDate[key]
		total += n
		cells = append(cells, heatCell{Date: key, Count: n, Level: heatLevel(n)})
	}
	return cells, total
}

const heatCellSide float32 = 11

// activityHeatmap is a reusable Fyne contribution strip (week columns × 7 days).
type activityHeatmap struct {
	title  *widget.Label
	weeks  *fyne.Container
	cells  []*canvas.Rectangle
	root   fyne.CanvasObject
}

func newActivityHeatmap() *activityHeatmap {
	h := &activityHeatmap{
		title: mutedLabel("Activity loads after a project is linked."),
		weeks: container.NewHBox(),
	}
	const maxWeeks = 54
	h.cells = make([]*canvas.Rectangle, 0, maxWeeks*7)
	weekObjs := make([]fyne.CanvasObject, 0, maxWeeks)
	for w := 0; w < maxWeeks; w++ {
		dayObjs := make([]fyne.CanvasObject, 0, 7)
		for d := 0; d < 7; d++ {
			r := canvas.NewRectangle(colorRaised)
			r.SetMinSize(fyne.NewSize(heatCellSide, heatCellSide))
			r.CornerRadius = 2
			h.cells = append(h.cells, r)
			dayObjs = append(dayObjs, r)
		}
		weekObjs = append(weekObjs, container.NewVBox(dayObjs...))
	}
	h.weeks.Objects = weekObjs

	legend := container.NewHBox(
		dimCaption("Less"),
		heatSwatch(0), heatSwatch(1), heatSwatch(2), heatSwatch(3), heatSwatch(4),
		dimCaption("More"),
	)
	scroll := container.NewHScroll(container.NewPadded(h.weeks))
	scroll.SetMinSize(fyne.NewSize(440, 110))
	h.root = container.NewVBox(
		sectionHeading("Activity"),
		h.title,
		scroll,
		legend,
	)
	return h
}

func heatSwatch(level int) fyne.CanvasObject {
	r := canvas.NewRectangle(heatColor(level))
	r.SetMinSize(fyne.NewSize(10, 10))
	r.CornerRadius = 2
	return r
}

func (h *activityHeatmap) canvasObject() fyne.CanvasObject {
	return h.root
}

func (h *activityHeatmap) setDays(days []cloudclient.HeatDay) {
	cells, total := buildHeatCells(days, time.Now())
	if total == 0 && len(days) == 0 {
		h.title.SetText("No project events yet — harvest and memory writes show up here.")
	} else {
		h.title.SetText(fmt.Sprintf("%d project events in the last year", total))
	}
	for i, r := range h.cells {
		if i < len(cells) {
			r.FillColor = heatColor(cells[i].Level)
			r.Show()
		} else {
			r.Hide()
		}
		r.Refresh()
	}
	h.weeks.Refresh()
}
