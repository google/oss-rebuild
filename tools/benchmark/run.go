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

type Limiter struct {
	CurrentPeriod time.Duration
	minimum       time.Duration
	current       *time.Timer
	C             chan time.Time
}

func NewLimiter(minimum time.Duration) *Limiter {
	l := &Limiter{
		CurrentPeriod: minimum,
		minimum:       minimum,
		C:             make(chan time.Time, 1),
	}
	l.newTick()
	return l
}

func (l *Limiter) newTick() {
	l.current = time.AfterFunc(l.CurrentPeriod, func() {
		// Don't accumulate ticks.
		if len(l.C) == 0 {
			l.C <- time.Now()
		}
		l.CurrentPeriod -= time.Second
		if l.CurrentPeriod < l.minimum {
			l.CurrentPeriod = l.minimum
		}
		go l.newTick()
	})
}

func (l *Limiter) Backoff() {
	l.current.Stop()
	l.CurrentPeriod *= 2
	l.newTick()
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
	limiters map[string]*Limiter
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
			errMsg = errors.Wrap(err, "making request").Error()
		}
		var resp *http.Response
		for range 3 {
			<-w.limiters[p.Ecosystem].C
			resp, err = w.client.Do(req)
			if err == nil && resp.StatusCode == 429 {
				w.limiters[p.Ecosystem].Backoff()
				log.Printf("rate limited, backing off to %d seconds", w.limiters[p.Ecosystem].CurrentPeriod/time.Second)
				continue
			}
			break
		}
		if err != nil {
			errMsg = errors.Wrap(err, "sending request").Error()
		} else if resp.StatusCode != 200 {
			errMsg = errors.Wrapf(errors.New(resp.Status), "sending request").Error()
		}
		var verdict schema.Verdict
		if errMsg != "" {
			verdict = schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: errMsg,
			}
		} else {
			// TODO: Once the attestation endpoint returns verdict objects,
			// support that here.
			verdict = schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: "",
			}
		}
		out <- verdict
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
	var errMsg string
	req, err := makeHTTPRequest(ctx, w.url.JoinPath("smoketest"), &schema.SmoketestRequest{
		Ecosystem: rebuild.Ecosystem(p.Ecosystem),
		Package:   p.Name,
		Versions:  p.Versions,
		ID:        w.run,
	})
	if err != nil {
		errMsg = errors.Wrap(err, "making request").Error()
	}
	var resp *http.Response
	for range 3 {
		<-w.limiters[p.Ecosystem].C
		resp, err = w.client.Do(req)
		if err == nil && resp.StatusCode == 429 {
			w.limiters[p.Ecosystem].Backoff()
			log.Printf("rate limited, backing off to %d seconds", w.limiters[p.Ecosystem].CurrentPeriod/time.Second)
			continue
		}
		break
	}
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
		var r schema.SmoketestResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			log.Fatalf("Failed to decode smoketest response: %v", err)
		}
		for _, v := range r.Verdicts {
			out <- v
		}
	}
}

func defaultLimiters() map[string]*Limiter {
	return map[string]*Limiter{
		"debian": NewLimiter(time.Second),
		"pypi":   NewLimiter(time.Second),
		"npm":    NewLimiter(2 * time.Second),
		"maven":  NewLimiter(2 * time.Second),
		// NOTE: cratesio needs to be especially slow given our registry API
		// constraint of 1QPS. At minimum, we expect to make 4 calls per test.
		"cratesio": NewLimiter(8 * time.Second),
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
