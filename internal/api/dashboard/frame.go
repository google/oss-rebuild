// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Section is one page in the sidebar with the figure that sums it up and a
// sparkline of how the figure got there.
type Section struct {
	Label, Href  string
	Figure, Unit string // empty when the figure could not be read
	Spark        string // SVG path data in the 56 by 18 sparkline box
	Bars         bool   // Spark outlines filled bars rather than a line
}

// Frame is what the layout shows around every page's content.
type Frame struct {
	Sections []Section // nil renders the sections without figures
	AsOf     string    // the snapshot's rollup watermark
}

const (
	homeSection = iota
	coverageSection
	packagesSection
	resourcesSection
)

var sections = []Section{
	homeSection:      {Label: "Home", Href: "/"},
	coverageSection:  {Label: "Coverage", Href: "/coverage"},
	packagesSection:  {Label: "Packages", Href: "/packages"},
	resourcesSection: {Label: "Resources", Href: "/resources"},
}

// Nav lists the sidebar's sections.
func (f Frame) Nav() []Section {
	if f.Sections == nil {
		return sections
	}
	return f.Sections
}

func (f *Frame) setFrame(v Frame) { *f = v }

// Framed fills the frame of the page h returns, for page data that embeds
// Frame.
func Framed[I act.Input, O any](h api.HandlerFn[I, O, *Deps]) api.HandlerFn[I, O, *Deps] {
	return func(ctx context.Context, req I, deps *Deps) (*O, error) {
		o, err := h(ctx, req, deps)
		if f, ok := any(o).(interface{ setFrame(Frame) }); ok && o != nil && err == nil {
			f.setFrame(loadFrame(ctx, deps))
		}
		return o, err
	}
}

// snapshotFrame caches the snapshot's figures, which change only with it.
var snapshotFrame struct {
	sync.Mutex
	fresh time.Time
	frame Frame
}

// loadFrame reads the sidebar's figures. A figure that fails to load is left
// out rather than failing the page.
func loadFrame(ctx context.Context, deps *Deps) Frame {
	f := Frame{Sections: append([]Section(nil), sections...)}
	if a := deps.Analytics; a != nil {
		snapshotFrame.Lock()
		if fresh := a.Freshness(); !fresh.Equal(snapshotFrame.fresh) {
			if sf, err := loadSnapshotFrame(a); err == nil {
				snapshotFrame.fresh, snapshotFrame.frame = fresh, sf
			} else {
				snapshotFrame.fresh, snapshotFrame.frame = time.Time{}, Frame{}
			}
		}
		if sf := snapshotFrame.frame; sf.Sections != nil {
			f.AsOf = sf.AsOf
			copy(f.Sections, sf.Sections)
		}
		snapshotFrame.Unlock()
	}
	return f
}

func loadSnapshotFrame(a Analytics) (Frame, error) {
	f := Frame{Sections: append([]Section(nil), sections...)}
	var err error
	if f.AsOf, err = rolledUpAt(a); err != nil {
		return f, err
	}
	r, err := loadReproduced(a)
	if err != nil {
		return f, err
	}
	counts := func(steps [][2]int64) []int64 {
		ys := make([]int64, len(steps))
		for i, s := range steps {
			ys[i] = s[1]
		}
		return ys
	}
	total := r.Total()
	f.Sections[homeSection].Figure, f.Sections[homeSection].Unit, f.Sections[homeSection].Spark = groupDigits(total.Versions), "versions", sparkLine(counts(r.Versions))
	f.Sections[packagesSection].Figure, f.Sections[packagesSection].Unit, f.Sections[packagesSection].Spark = groupDigits(total.Packages), "packages", sparkLine(counts(r.Packages))
	if top := r.Top(); top.Packages > 0 {
		var shares []float64
		for _, e := range r.Ecos {
			if t := e.Top(); t.Packages > 0 {
				shares = append(shares, t.Share())
			}
		}
		f.Sections[coverageSection].Figure = fmt.Sprintf("%.1f%%", top.Share())
		f.Sections[coverageSection].Unit, f.Sections[coverageSection].Spark, f.Sections[coverageSection].Bars = "top 1k", sparkBars(shares, 100), true
	}
	fresh := a.Freshness().UTC()
	today := fresh.Format(time.DateOnly)
	var builds int64
	var days []int64
	err = forEachRow(a, `
		SELECT day, sum(attempts) FROM ecosystem_daily
		WHERE day >= '`+fresh.AddDate(0, 0, -13).Format(time.DateOnly)+`' GROUP BY 1 ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			days = append(days, stmt.ColumnInt64(1))
			if stmt.ColumnText(0) == today {
				builds = stmt.ColumnInt64(1)
			}
			return nil
		})
	if err != nil {
		return f, errors.Wrap(err, "querying daily builds")
	}
	f.Sections[resourcesSection].Figure, f.Sections[resourcesSection].Unit, f.Sections[resourcesSection].Spark = groupDigits(builds), "builds today", sparkLine(days)
	return f, nil
}

// sparkLine draws ys left to right across the sparkline box, scaled to their
// maximum.
func sparkLine(ys []int64) string {
	if len(ys) < 2 {
		return ""
	}
	peak := int64(1)
	for _, y := range ys {
		peak = max(peak, y)
	}
	var b strings.Builder
	for i, y := range ys {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&b, "%s%.1f %.1f", cmd, float64(i)*56/float64(len(ys)-1), 16-14*float64(y)/float64(peak))
	}
	return b.String()
}

// sparkBars draws ys as bars on the sparkline box's baseline, scaled so peak
// fills its height.
func sparkBars(ys []float64, peak float64) string {
	if len(ys) == 0 {
		return ""
	}
	step := 56 / float64(len(ys))
	var b strings.Builder
	for i, y := range ys {
		x := float64(i) * step
		fmt.Fprintf(&b, "M%.1f 16V%.1fH%.1fV16Z", x+step*0.1, 16-14*min(y/peak, 1), x+step*0.8)
	}
	return b.String()
}
