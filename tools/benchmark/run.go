package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/schema/form"
	"github.com/pkg/errors"
)

type BenchmarkMode string

const (
	SmoketestMode BenchmarkMode = "smoketest"
	AttestMode    BenchmarkMode = "attest"
)

func getExecutorVersion(ctx context.Context, client *http.Client, api *url.URL, service string) (string, error) {
	verURL := api.JoinPath("version")
	verURL.RawQuery = url.Values{"service": []string{service}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, verURL.String(), nil)
	if err != nil {
		return "", errors.Wrap(err, "creating API version request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "sending API version request")
	}
	if resp.StatusCode != 200 {
		return "", errors.Wrap(errors.New(resp.Status), "API version request")
	}
	vb, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "reading API version")
	}
	return string(vb), nil
}

func makeHTTPRequest(ctx context.Context, u *url.URL, msg schema.Message) (*http.Request, error) {
	values, err := form.Marshal(msg)
	if err != nil {
		return nil, errors.Wrap(err, "creating values")
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "creating request")
	}
	return req, nil
}

type packageWorker interface {
	Setup(ctx context.Context)
	ProcessOne(ctx context.Context, p Package, out chan schema.Verdict)
}

type executor struct {
	Concurrency int
	Worker      packageWorker
}

func (ex *executor) Process(ctx context.Context, out chan schema.Verdict, packages []Package) {
	ex.Worker.Setup(ctx)
	jobs := make(chan Package)
	go func() {
		for _, p := range packages {
			jobs <- p
		}
		close(jobs)
	}()
	var wg sync.WaitGroup
	for range ex.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				ex.Worker.ProcessOne(ctx, p, out)
			}
		}()
	}
	wg.Wait()
	close(out)
}

type workerConfig struct {
	client   *http.Client
	url      *url.URL
	limiters map[string]<-chan time.Time
	run      string
}

type attestWorker struct {
	workerConfig
}

var _ packageWorker = &attestWorker{}

func (w *attestWorker) Setup(ctx context.Context) {}

func (w *attestWorker) ProcessOne(ctx context.Context, p Package, out chan schema.Verdict) {
	if len(p.Artifacts) > 0 && len(p.Artifacts) != len(p.Versions) {
		log.Fatalf("Provided artifact slice does not match versions: %s", p.Name)
	}
	for i, v := range p.Versions {
		<-w.limiters[p.Ecosystem]
		var artifact string
		if len(p.Artifacts) > 0 {
			artifact = p.Artifacts[i]
		}
		var errMsg string
		req, err := makeHTTPRequest(ctx, w.url.JoinPath("rebuild"), &schema.RebuildPackageRequest{
			Ecosystem: rebuild.Ecosystem(p.Ecosystem),
			Package:   p.Name,
			Version:   v,
			Artifact:  artifact,
			ID:        w.run,
		})
		if err != nil {
			log.Fatal(errors.Wrap(err, "making request"))
		}
		resp, err := w.client.Do(req)
		if err != nil {
			errMsg = errors.Wrap(err, "sending request").Error()
		} else if resp.StatusCode != 200 {
			errMsg = errors.Wrapf(errors.New(resp.Status), "sending request").Error()
		}
		if errMsg != "" {
			out <- schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: errMsg,
			}
		} else {
			// TODO: Use stubs instead of http requests.
			var v schema.Verdict
			if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
				log.Fatalf("Failed to decode attestation verdict: %v", err)
			}
			out <- v
		}
	}
}

type smoketestWorker struct {
	workerConfig
	warmup bool
}

func (w *smoketestWorker) Setup(ctx context.Context) {
	if w.warmup {
		// First, warm up the instances to ensure it can handle actual load.
		// Warm up requires the service fulfill sequentially successful version
		// requests (which hit both the API and the builder jobs).
		for i := 0; i < 5; {
			_, err := getExecutorVersion(ctx, w.client, w.url, "build-local")
			if err != nil {
				i = 0
			} else {
				i++
			}
		}
	}
}

func (w *smoketestWorker) ProcessOne(ctx context.Context, p Package, out chan schema.Verdict) {
	<-w.limiters[p.Ecosystem]
	var errMsg string
	req, err := makeHTTPRequest(ctx, w.url.JoinPath("smoketest"), &schema.SmoketestRequest{
		Ecosystem: rebuild.Ecosystem(p.Ecosystem),
		Package:   p.Name,
		Versions:  p.Versions,
		ID:        w.run,
	})
	if err != nil {
		log.Fatal(errors.Wrap(err, "making request"))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		errMsg = errors.Wrap(err, "sending request").Error()
	} else if resp.StatusCode != 200 {
		log.Println(req.URL.String())
		io.Copy(log.Writer(), resp.Body)
		errMsg = errors.Wrapf(errors.New(resp.Status), "sending request").Error()
	}
	if errMsg != "" {
		for _, v := range p.Versions {
			out <- schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: errMsg,
			}
		}
	} else {
		// TODO: Use stubs instead of http requests.
		var r schema.SmoketestResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			log.Fatalf("Failed to decode smoketest response: %v", err)
		}
		for _, v := range r.Verdicts {
			out <- v
		}
	}
}

func defaultLimiters() map[string]<-chan time.Time {
	return map[string]<-chan time.Time{
		"debian": time.Tick(time.Second),
		"pypi":   time.Tick(time.Second),
		"npm":    time.Tick(2 * time.Second),
		"maven":  time.Tick(2 * time.Second),
		// NOTE: cratesio needs to be especially slow given our registry API
		// constraint of 1QPS. At minimum, we expect to make 4 calls per test.
		"cratesio": time.Tick(8 * time.Second),
	}
}

func isCloudRun(u *url.URL) bool {
	return strings.HasSuffix(u.Host, ".run.app")
}

type RunBenchOpts struct {
	Mode BenchmarkMode
	// RunID is the ID for this run. Leave blank for one to be generated.
	RunID          string
	MaxConcurrency int
}

func RunBench(ctx context.Context, client *http.Client, apiURL *url.URL, set PackageSet, opts RunBenchOpts) (<-chan schema.Verdict, error) {
	if opts.RunID == "" {
		return nil, errors.New("opts.RunID must be set")
	}
	conf := workerConfig{
		client:   client,
		url:      apiURL,
		limiters: defaultLimiters(),
		run:      opts.RunID,
	}
	ex := executor{Concurrency: opts.MaxConcurrency}
	if opts.Mode == SmoketestMode {
		ex.Worker = &smoketestWorker{
			workerConfig: conf,
			warmup:       isCloudRun(apiURL),
		}
	} else if opts.Mode == AttestMode {
		ex.Worker = &attestWorker{
			workerConfig: conf,
		}
	} else {
		return nil, fmt.Errorf("invalid mode: %s", string(opts.Mode))
	}
	verdictChan := make(chan schema.Verdict)
	go ex.Process(ctx, verdictChan, set.Packages)
	return verdictChan, nil
}
