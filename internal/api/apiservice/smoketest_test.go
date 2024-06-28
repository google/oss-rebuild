package apiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
)

func TestRebuildSmoketest(t *testing.T) {
	tests := []struct {
		name           string
		request        schema.SmoketestRequest
		smoketestStub  func(context.Context, schema.SmoketestRequest) (*schema.SmoketestResponse, error)
		versionStub    func(context.Context, schema.VersionRequest) (*schema.VersionResponse, error)
		expectedResp   *schema.SmoketestResponse
		expectedErr    error
		expectedErrMsg string
	}{
		{
			name: "Successful smoketest",
			request: schema.SmoketestRequest{
				Ecosystem: "npm",
				Package:   "test-package",
				Versions:  []string{"1.0.0"},
			},
			smoketestStub: func(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
				return &schema.SmoketestResponse{
					Executor: "1",
					Verdicts: []schema.Verdict{
						{
							Target: rebuild.Target{
								Ecosystem: rebuild.Ecosystem(req.Ecosystem),
								Package:   req.Package,
								Version:   req.Versions[0],
							},
							Message: "", // Success
						},
					},
				}, nil
			},
			versionStub: func(ctx context.Context, req schema.VersionRequest) (*schema.VersionResponse, error) {
				return &schema.VersionResponse{Version: "1"}, nil
			},
			expectedResp: &schema.SmoketestResponse{
				Executor: "1",
				Verdicts: []schema.Verdict{
					{
						Target: rebuild.Target{
							Ecosystem: rebuild.NPM,
							Package:   "test-package",
							Version:   "1.0.0",
						},
						Message: "",
					},
				},
			},
		},
		{
			name: "Failed smoketest",
			request: schema.SmoketestRequest{
				Ecosystem: "npm",
				Package:   "test-package",
				Versions:  []string{"1.0.0"},
			},
			smoketestStub: func(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
				return nil, api.ErrNotOK
			},
			versionStub: func(ctx context.Context, req schema.VersionRequest) (*schema.VersionResponse, error) {
				return &schema.VersionResponse{Version: "1"}, nil
			},
			expectedResp: &schema.SmoketestResponse{
				Executor: "1",
				Verdicts: []schema.Verdict{
					{
						Target: rebuild.Target{
							Ecosystem: rebuild.NPM,
							Package:   "test-package",
							Version:   "1.0.0",
						},
						Message: "build-local failed: non-OK response",
					},
				},
			},
		},
		{
			name: "Failed smoketest and version",
			request: schema.SmoketestRequest{
				Ecosystem: "npm",
				Package:   "test-package",
				Versions:  []string{"1.0.0"},
			},
			smoketestStub: func(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
				return nil, api.ErrNotOK
			},
			versionStub: func(ctx context.Context, req schema.VersionRequest) (*schema.VersionResponse, error) {
				return nil, api.ErrNotOK
			},
			expectedResp: &schema.SmoketestResponse{
				Executor: "unknown",
				Verdicts: []schema.Verdict{
					{
						Target: rebuild.Target{
							Ecosystem: rebuild.NPM,
							Package:   "test-package",
							Version:   "1.0.0",
						},
						Message: "build-local failed: non-OK response",
					},
				},
			},
		},
		{
			name: "Internal error",
			request: schema.SmoketestRequest{
				Ecosystem: "npm",
				Package:   "test-package",
				Versions:  []string{"1.0.0"},
			},
			smoketestStub: func(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
				return nil, errors.New("internal error")
			},
			versionStub: func(ctx context.Context, req schema.VersionRequest) (*schema.VersionResponse, error) {
				return &schema.VersionResponse{Version: "1"}, nil
			},
			expectedResp:   nil,
			expectedErr:    api.AsStatus(codes.Internal, errors.New("making smoketest request: internal error")),
			expectedErrMsg: "rpc error: code = Internal desc = making smoketest request: internal error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &RebuildSmoketestDeps{
				SmoketestStub: tt.smoketestStub,
				VersionStub:   tt.versionStub,
			}

			ctx := context.Background()
			resp, err := rebuildSmoketest(ctx, tt.request, deps)

			if tt.expectedErr != nil {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if err.Error() != tt.expectedErrMsg {
					t.Errorf("expected error message %q, got %q", tt.expectedErrMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if diff := cmp.Diff(tt.expectedResp, resp); diff != "" {
					t.Errorf("response mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
