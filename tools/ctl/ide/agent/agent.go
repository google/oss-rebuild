// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Agent provides visualization components for an agent sessions
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	gcs "cloud.google.com/go/storage"
	"github.com/gdamore/tcell/v2"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

// SessionViewDeps are the external dependencies
type SessionViewDeps struct {
	GCS            *gcs.Client
	App            *tview.Application
	MetadataBucket string
	LogsBucket     string
}

const (
	IterationTabName  = "Iteration"
	DockerfileTabName = "Dockerfile"
	LogsTabName       = "Logs"
)

var tabNames = []string{IterationTabName, DockerfileTabName, LogsTabName}

// sessionViewModel contains all the sessionView tview objects
type sessionViewModel struct {
	iterView       *tview.TextView
	dockerfileView *tview.TextView
	logsView       *tview.TextView
	pages          *tview.Pages
	tabs           *tview.TextView
	list           *tview.List
	root           *tview.Flex
	currentTab     int
}

type sessionView struct {
	session         *schema.AgentSession
	iters           []schema.AgentIteration
	deps            SessionViewDeps
	model           *sessionViewModel
	dockerfileCache map[string]string
	logsCache       map[string]string
}

func NewSessionView(session *schema.AgentSession, iters []schema.AgentIteration, deps SessionViewDeps) *sessionView {
	sort.Slice(iters, func(i, j int) bool {
		return iters[i].Number < iters[j].Number
	})
	v := &sessionView{
		session:         session,
		iters:           iters,
		deps:            deps,
		dockerfileCache: make(map[string]string),
		logsCache:       make(map[string]string),
	}
	var m *sessionViewModel
	{
		// rightPane contains the different data views represented as tabs
		pages := tview.NewPages()
		iterView := tview.NewTextView().SetDynamicColors(true)
		dockerfileView := tview.NewTextView().SetDynamicColors(true)
		logsView := tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
		// Shortcuts to get to the bottom and top
		logsView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyRune && (event.Rune() == 'v' || event.Rune() == 'G') {
				logsView.ScrollToEnd()
				return nil
			} else if event.Key() == tcell.KeyRune && (event.Rune() == '^' || event.Rune() == 'g') {
				logsView.ScrollToBeginning()
				return nil
			}
			return event
		})
		pages.
			AddPage(IterationTabName, iterView, true, true).
			AddPage(DockerfileTabName, dockerfileView, true, false).
			AddPage(LogsTabName, logsView, true, false)
		pages.SetBorder(true)
		tabs := tview.NewTextView().
			SetDynamicColors(true).
			SetRegions(true)
		tabs.SetBorder(true)
		rightPane := tview.NewFlex().
			SetDirection(tview.FlexRow).
			AddItem(tabs, 3, 1, false).
			AddItem(pages, 0, 1, false)
		rightPane.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			// Disable left-right scrolling in text views
			if event.Key() == tcell.KeyRight || event.Key() == tcell.KeyLeft {
				return nil
			}
			return event
		})

		// list contains the list of agentIterations
		list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true)
		list.SetBorder(true)
		for _, iter := range iters {
			list.AddItem(fmt.Sprintf("Iteration %d (%s)", iter.Number, iter.ID), "", 0, nil)
		}
		list.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
			iterView.Clear()
			dockerfileView.Clear()
			logsView.Clear()
			v.updateFocusedTab(context.Background())
		})
		root := tview.NewFlex().
			AddItem(list, 37, 0, true).
			AddItem(rightPane, 0, 1, true)
		root.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyTab {
				v.model.currentTab = (v.model.currentTab + 1) % len(tabNames)
				v.model.pages.SwitchToPage(tabNames[v.model.currentTab])
				v.updateFocusedTab(context.Background())
				return nil // Event handled
			} else if event.Key() == tcell.KeyBacktab {
				v.model.currentTab = (v.model.currentTab - 1 + len(tabNames)) % len(tabNames)
				v.model.pages.SwitchToPage(tabNames[v.model.currentTab])
				v.updateFocusedTab(context.Background())
				return nil // Event handled
			} else if event.Key() == tcell.KeyRight {
				v.deps.App.SetFocus(v.model.pages)
				return nil
			} else if event.Key() == tcell.KeyLeft {
				v.deps.App.SetFocus(v.model.list)
				return nil
			}
			return event
		})
		m = &sessionViewModel{
			iterView:       iterView,
			dockerfileView: dockerfileView,
			logsView:       logsView,
			pages:          pages,
			tabs:           tabs,
			list:           list,
			root:           root,
			currentTab:     0,
		}
	}
	v.model = m
	if len(v.iters) > 0 {
		v.model.list.SetCurrentItem(0)
		v.model.currentTab = 0
		v.updateFocusedTab(context.Background())
	} else {
		v.model.iterView.SetText("No iterations found for this session.")
	}
	return v
}

func (v *sessionView) metadata(ctx context.Context, obliviousID string) (rebuild.ReadOnlyAssetStore, error) {
	if obliviousID == "" {
		return nil, errors.New("no oblivious ID provided")
	}
	metadata, err := rebuild.NewGCSStoreFromClient(context.WithValue(ctx, rebuild.RunID, obliviousID), v.deps.GCS, fmt.Sprintf("gs://%s", v.deps.MetadataBucket))
	return metadata, errors.Wrap(err, "creating metadata store")
}

func (v *sessionView) dockerfile(ctx context.Context, obliviousID string) (string, error) {
	if d, ok := v.dockerfileCache[obliviousID]; ok {
		return d, nil
	}
	meta, err := v.metadata(ctx, obliviousID)
	if err != nil {
		return "", err
	}
	r, err := meta.Reader(ctx, rebuild.DockerfileAsset.For(v.session.Target))
	if err != nil {
		return "", errors.Wrap(err, "opening dockerfile")
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrap(err, "reading dockerfile")
	}
	dockerfile := string(data)
	v.dockerfileCache[obliviousID] = dockerfile
	return dockerfile, nil
}

func (v *sessionView) logs(ctx context.Context, obliviousID string) (string, error) {
	if l, ok := v.logsCache[obliviousID]; ok {
		return l, nil
	}
	meta, err := v.metadata(ctx, obliviousID)
	if err != nil {
		return "", err
	}
	r, err := meta.Reader(ctx, rebuild.BuildInfoAsset.For(v.session.Target))
	if err != nil {
		return "", errors.Wrap(err, "reading build info")
	}
	bi := new(rebuild.BuildInfo)
	if json.NewDecoder(r).Decode(bi) != nil {
		return "", errors.Wrap(err, "parsing build info")
	}
	if bi.BuildID == "" {
		return "", errors.New("BuildID is empty, cannot read gcb logs")
	}
	obj := v.deps.GCS.Bucket(v.deps.LogsBucket).Object(gcb.MergedLogFile(bi.BuildID))
	r, err = obj.NewReader(ctx)
	if err != nil {
		return "", errors.Wrap(err, "opening log")
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrap(err, "reading log")
	}
	logs := string(data)
	v.logsCache[obliviousID] = logs
	return logs, nil
}

func (v *sessionView) updateFocusedTab(ctx context.Context) error {
	index := v.model.list.GetCurrentItem()
	if index < 0 || index >= len(v.iters) {
		return errors.New("invalid index")
	}
	selectedIter := v.iters[index]
	// Use a goroutine to load data in the background
	go func() {
		// Update tab graphics immediately,
		v.deps.App.QueueUpdateDraw(func() {
			v.model.tabs.Clear()
			for i, name := range tabNames {
				fmt.Fprintf(v.model.tabs, `["%s"]`, name) // Region tag
				if i == v.model.currentTab {
					fmt.Fprintf(v.model.tabs, "[yellow:black:b] %s [-:-:-]", name)
				} else {
					fmt.Fprintf(v.model.tabs, "[white:black:u] %s [-:-:-]", name)
				}
				fmt.Fprint(v.model.tabs, `[""] `) // End region
			}
		})
		var content string
		var tabToUpdate *tview.TextView
		switch tabNames[v.model.currentTab] {
		case "Iteration":
			jsonData, jsonErr := json.MarshalIndent(selectedIter, "", "  ")
			if jsonErr != nil {
				content = fmt.Sprintf("[red]Error marshalling JSON: %v[-]", jsonErr)
			} else {
				content = tview.Escape(string(jsonData))
			}
			tabToUpdate = v.model.iterView
		case "Dockerfile":
			dockerfile, err := v.dockerfile(ctx, selectedIter.ObliviousID)
			if err != nil {
				content = fmt.Sprintf("[red]Error fetching dockerfile: %v[-]", err)
			} else {
				content = fmt.Sprintf("[yellow]--- Dockerfile for %s ---[-]\n\n%s", selectedIter.ID, tview.Escape(dockerfile))
			}
			tabToUpdate = v.model.dockerfileView
		case "Logs":
			logs, err := v.logs(ctx, selectedIter.ObliviousID)
			if err != nil {
				content = fmt.Sprintf("[red]Error fetching logs: %v[-]", err)
			} else {
				content = fmt.Sprintf("[yellow]--- Build logs for build ID: %s ---[-]\n\n[green]%s", selectedIter.ObliviousID, tview.Escape(logs))
			}
			tabToUpdate = v.model.logsView
		}
		// Update the view from the main UI thread
		v.deps.App.QueueUpdateDraw(func() {
			if tabToUpdate.GetText(false) != content {
				tabToUpdate.Clear().SetText(content)
				if tabToUpdate == v.model.logsView {
					tabToUpdate.ScrollToEnd()
				} else {
					tabToUpdate.ScrollToBeginning()
				}
			}
		})
	}()
	return nil
}

// Run is a convenience function if you want to run the SessionView as the main app
func (v *sessionView) Run() error {
	return v.deps.App.SetRoot(v.model.root, true).SetFocus(v.model.list).Run()
}
