// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timing

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// histLine renders one docker history JSON row.
func histLine(created string) string {
	return `{"Comment":"buildkit.dockerfile.v0","CreatedAt":"` + created + `","CreatedBy":"RUN ...","ID":"<missing>","Size":"0"}`
}

func TestParseHistory(t *testing.T) {
	layers := Layers{Appended: 3, Setup: 0, Source: 1, Deps: -1}
	for _, tc := range []struct {
		name    string
		out     string
		layers  Layers
		want    []string
		wantErr bool
	}{
		{
			name: "NewestFirstReversed",
			out: strings.Join([]string{
				histLine("2026-07-24T10:03:00Z"),
				histLine("2026-07-24T10:02:00Z"),
				histLine("2026-07-24T10:01:00Z"),
				histLine("2026-07-24T09:00:00Z"), // base image row
			}, "\n"),
			layers: layers,
			want:   []string{"2026-07-24T10:01:00Z", "2026-07-24T10:02:00Z", "2026-07-24T10:03:00Z"},
		},
		{
			name: "SkipsNonJSONAndBadTimes",
			out: strings.Join([]string{
				"2026-07-24T10:07:00.123456789Z 2026-07-24T10:08:00.987654321Z", // inspect output
				histLine("2026-07-24T10:03:00+02:00"),
				`{"CreatedAt":"3 minutes ago"}`, // human format skipped
				histLine("2026-07-24T10:02:00+02:00"),
				"not json at all",
				histLine("2026-07-24T10:01:00+02:00"),
			}, "\n"),
			layers: layers,
			want:   []string{"2026-07-24T10:01:00+02:00", "2026-07-24T10:02:00+02:00", "2026-07-24T10:03:00+02:00"},
		},
		{
			name:    "TooFewRows",
			out:     histLine("2026-07-24T10:03:00Z"),
			layers:  layers,
			wantErr: true,
		},
		{
			name:    "InvalidLayers",
			out:     histLine("2026-07-24T10:03:00Z"),
			layers:  Layers{Appended: 1, Setup: 0, Source: 0, Deps: -1},
			wantErr: true,
		},
		{
			name:    "InvalidDeps",
			out:     histLine("2026-07-24T10:03:00Z"),
			layers:  Layers{Appended: 3, Setup: 0, Source: 1, Deps: 1},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lt, err := ParseHistory([]byte(tc.out), tc.layers)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseHistory() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			var got []string
			for _, at := range lt.Times {
				got = append(got, at.Format(time.RFC3339))
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseHistory() times diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPhases(t *testing.T) {
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	buildStart := at("2026-07-24T10:00:00Z")
	for _, tc := range []struct {
		name                string
		layers              Layers
		times               []string
		setup, source, deps time.Duration
		wantErr             bool
	}{
		{
			name:   "Normal",
			layers: Layers{Setup: 0, Source: 1, Deps: 2},
			times:  []string{"2026-07-24T10:00:30Z", "2026-07-24T10:02:00Z", "2026-07-24T10:02:10Z"},
			setup:  30 * time.Second,
			source: 90 * time.Second,
			deps:   10 * time.Second,
		},
		{
			name:   "NoDepsLayer",
			layers: Layers{Setup: 0, Source: 1, Deps: -1},
			times:  []string{"2026-07-24T10:00:30Z", "2026-07-24T10:02:00Z"},
			setup:  30 * time.Second,
			source: 90 * time.Second,
		},
		{
			// Metadata entries complete within the last RUN's clock second.
			name:   "EqualClocksTolerated",
			layers: Layers{Setup: 0, Source: 1, Deps: -1},
			times:  []string{"2026-07-24T10:00:30Z", "2026-07-24T10:02:00Z", "2026-07-24T10:02:00Z", "2026-07-24T10:02:00Z"},
			setup:  30 * time.Second,
			source: 90 * time.Second,
		},
		{
			name:    "CachedLayerPredatesBuild",
			layers:  Layers{Setup: 0, Source: 1, Deps: 2},
			times:   []string{"2026-07-24T09:00:00Z", "2026-07-24T10:02:00Z", "2026-07-24T10:02:10Z"},
			wantErr: true,
		},
		{
			name:    "Disordered",
			layers:  Layers{Setup: 0, Source: 1, Deps: 2},
			times:   []string{"2026-07-24T10:02:00Z", "2026-07-24T10:00:30Z", "2026-07-24T10:02:10Z"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lt := LayerTimes{layers: tc.layers}
			for _, s := range tc.times {
				lt.Times = append(lt.Times, at(s))
			}
			setup, source, deps, err := lt.Phases(buildStart)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Phases() error = %v, wantErr %v", err, tc.wantErr)
			}
			if setup != tc.setup || source != tc.source || deps != tc.deps {
				t.Errorf("Phases() = (%v, %v, %v), want (%v, %v, %v)", setup, source, deps, tc.setup, tc.source, tc.deps)
			}
		})
	}
}

func TestContainerSpan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		out     string
		want    time.Duration
		wantErr bool
	}{
		{
			name: "SkipsTraceAndSingleClockLines",
			out: strings.Join([]string{
				"+ docker inspect container -f {{.State.StartedAt}} {{.State.FinishedAt}}",
				"2026-07-01T00:01:12.000000000Z",
				"2026-07-01T00:01:20.500000000Z 2026-07-01T00:01:44.500000000Z",
			}, "\n"),
			want: 24 * time.Second,
		},
		{
			name:    "NoSpan",
			out:     "no state clocks here\n",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ContainerSpan([]byte(tc.out))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ContainerSpan() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ContainerSpan() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidated(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   rebuild.BuildTimings
		want *rebuild.BuildTimings
	}{
		{
			name: "Complete",
			in:   rebuild.BuildTimings{Setup: 30 * time.Second, Source: 45 * time.Second, Deps: 45 * time.Second, Build: 60 * time.Second},
			want: &rebuild.BuildTimings{Setup: 30 * time.Second, Source: 45 * time.Second, Deps: 45 * time.Second, Build: 60 * time.Second},
		},
		{
			name: "DepsLegitimatelyZero",
			in:   rebuild.BuildTimings{Setup: 30 * time.Second, Source: 90 * time.Second, Build: 60 * time.Second},
			want: &rebuild.BuildTimings{Setup: 30 * time.Second, Source: 90 * time.Second, Build: 60 * time.Second},
		},
		{
			name: "NilWithoutBuild",
			in:   rebuild.BuildTimings{Setup: 30 * time.Second, Source: 90 * time.Second},
		},
		{
			name: "NilOnNegativePhase",
			in:   rebuild.BuildTimings{Setup: 30 * time.Second, Source: -time.Second, Build: 60 * time.Second},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Validated(tc.in)); diff != "" {
				t.Errorf("Validated() diff (-want +got):\n%s", diff)
			}
		})
	}
}
