// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package modal

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	defaultModalBackground = tcell.ColorDarkCyan
)

// Returns a new primitive which puts the provided primitive in the center and
// adds vertical and horizontal margin.
// vertMargin and horizMargin are total margin. If margin is odd (can't be evenly split on either side), the primitive goes to the top and left.
func center(p tview.Primitive, vertMargin, horizMargin int) tview.Primitive {
	topMargin := vertMargin / 2
	bottomMargin := vertMargin - topMargin
	leftMargin := horizMargin / 2
	rightMargin := horizMargin - leftMargin
	return tview.NewFlex().
		AddItem(nil, leftMargin, 0, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, topMargin, 0, false).
			AddItem(p, 0, 1, true).
			AddItem(nil, bottomMargin, 0, false), 0, 1, true).
		AddItem(nil, rightMargin, 0, false)
}

type InputCaptureable interface {
	tview.Primitive
	GetInputCapture() func(event *tcell.EventKey) *tcell.EventKey
	SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey) *tview.Box
}

type ModalOpts struct {
	Height int
	Width  int
	Margin int
}

func Show(app *tview.Application, container *tview.Pages, contents InputCaptureable, opts ModalOpts) (exitFunc func()) {
	pageName := fmt.Sprintf("modal%d", container.GetPageCount()+1)
	exitFunc = func() {
		container.RemovePage(pageName)
	}
	oldCapture := contents.GetInputCapture()
	contents.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyESC {
			contents.SetInputCapture(oldCapture)
			exitFunc()
			// Returning nil prevents further primatives from receiving this event.
			return nil
		}
		return event
	})
	_, _, containerWidth, containerHeight := container.GetInnerRect()
	// If opts.Width or opts.Height is zero, assume the full container size.
	if opts.Width == 0 {
		opts.Width = containerWidth
	}
	if opts.Height == 0 {
		opts.Height = containerHeight
	}
	// Always apply the margin (default is zero).
	opts.Height = min(opts.Height, containerHeight-(2*opts.Margin))
	opts.Width = min(opts.Width, containerWidth-(2*opts.Margin))
	app.QueueUpdateDraw(func() {
		container.AddPage(pageName, center(contents, containerHeight-opts.Height, containerWidth-opts.Width), true, true)
	})
	return exitFunc
}

func Text(app *tview.Application, container *tview.Pages, contents string) {
	tv := tview.NewTextView()
	tv.SetText("\n" + contents + "\n").
		SetTextAlign(tview.AlignCenter).
		SetTextColor(tcell.ColorWhite).
		SetBackgroundColor(defaultModalBackground)
	Show(app, container, tv, ModalOpts{Height: 3, Margin: 10})
}
