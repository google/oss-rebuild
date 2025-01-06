package benchmark

import (
	"context"
	"net/url"
	"testing"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
)

type queueCall struct {
	URL  string
	Body string
}

type mockQueue struct {
	calls []queueCall
}

func (q *mockQueue) Add(ctx context.Context, url, body string) (*taskspb.Task, error) {
	q.calls = append(q.calls, queueCall{url, body})
	return &taskspb.Task{}, nil
}

func TestRunBenchAsync(t *testing.T) {
	testCases := []struct {
		name     string
		mode     BenchmarkMode
		set      PackageSet
		expected []queueCall
	}{
		{
			name: "attest",
			mode: AttestMode,
			set: PackageSet{
				Packages: []Package{
					{
						Ecosystem: "npm",
						Name:      "package_name",
						Versions:  []string{"1.0.0", "1.1.0"},
					},
				},
			},
			expected: []queueCall{
				{
					"https://example.com/rebuild",
					"ecosystem=npm&id=runid&package=package_name&version=1.0.0",
				},
				{
					"https://example.com/rebuild",
					"ecosystem=npm&id=runid&package=package_name&version=1.1.0",
				},
			},
		},
		{
			name: "smoketest",
			mode: SmoketestMode,
			set: PackageSet{
				Packages: []Package{
					{
						Ecosystem: "npm",
						Name:      "package_name",
						Versions:  []string{"1.0.0", "1.1.0"},
					},
				},
			},
			expected: []queueCall{
				{
					"https://example.com/smoketest",
					"ecosystem=npm&id=runid&package=package_name&versions=1.0.0&versions=1.1.0",
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			queue := &mockQueue{}
			url, _ := url.Parse("https://example.com")
			if err := RunBenchAsync(context.Background(), tc.set, tc.mode, url, "runid", queue); err != nil {
				t.Error(errors.Wrap(err, "RunBenchAsync"))
			}
			if diff := cmp.Diff(queue.calls, tc.expected); diff != "" {
				t.Errorf("Unexpected calls to queue: got %v, want %v", queue.calls, tc.expected)
			}
		})
	}
}
