// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/google/oss-rebuild/internal/cache"
	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
	cratesrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/iterator"
)

var (
	project               = flag.String("project", "", "GCP Project ID for storage and build resources")
	buildRemoteIdentity   = flag.String("build-remote-identity", "", "Identity from which to run remote rebuilds")
	buildLocalURL         = flag.String("build-local-url", "", "URL of the rebuild service")
	inferenceURL          = flag.String("inference-url", "", "URL of the inference service")
	signingKeyVersion     = flag.String("signing-key-version", "", "Resource name of the signing CryptoKeyVersion")
	metadataBucket        = flag.String("metadata-bucket", "", "GCS bucket for rebuild metadata")
	attestationBucket     = flag.String("attestation-bucket", "", "GCS bucket to which to publish rebuild attestation")
	logsBucket            = flag.String("logs-bucket", "", "GCS bucket for rebuild logs")
	prebuildBucket        = flag.String("prebuild-bucket", "", "GCS bucket from which prebuilt build tools are stored")
	overwriteAttestations = flag.Bool("overwrite-attestations", false, "whether to overwrite existing attestations when writing to GCS")
)

var httpcfg = httpegress.Config{}

func checkClose(closer io.Closer) {
	if err := closer.Close(); err != nil {
		panic(errors.Wrap(err, "deferred close failed"))
	}
}

func HandleRebuildSmoketest(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	sreq, err := schema.NewSmoketestRequest(req.Form)
	if err != nil {
		log.Println(errors.Wrap(err, "parsing smoketest request"))
		http.Error(rw, err.Error(), 400)
		return
	}
	if sreq.ID == "" {
		sreq.ID = time.Now().UTC().Format(time.RFC3339)
	}
	log.Printf("Dispatching smoketest: %v", sreq)
	u, err := url.Parse(*buildLocalURL)
	if err != nil {
		log.Printf("url.Parse: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	runclient, err := idtoken.NewClient(ctx, *buildLocalURL)
	if err != nil {
		log.Printf("idtoken.NewClient: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	versionChan := make(chan string, 1)
	go func() {
		versionURL := u.JoinPath("version")
		versionURL.RawQuery = url.Values{"service": []string{"build-local"}}.Encode()
		resp, err := runclient.Get(versionURL.String())
		if err != nil {
			log.Println(errors.Wrap(err, "sending version request"))
			versionChan <- "unknown"
		} else if resp.StatusCode != 200 {
			log.Println(errors.Wrap(errors.New(resp.Status), "version request"))
			versionChan <- "unknown"
		} else if body, err := io.ReadAll(resp.Body); err != nil {
			log.Println(errors.Wrap(err, "reading version response"))
			versionChan <- "unknown"
		} else {
			versionChan <- string(body)
		}
		close(versionChan)
	}()
	smoketestURL := u.JoinPath("smoketest")
	smoketestVals, err := sreq.ToValues()
	if err != nil {
		log.Printf("failed to parse smoketest vals: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	resp, err := runclient.PostForm(smoketestURL.String(), smoketestVals)
	if err != nil {
		log.Printf("idtoken.Client.Get: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	var verdicts []schema.Verdict
	respCopy := new(bytes.Buffer)
	var executor string
	if resp.StatusCode != 200 {
		log.Printf("smoketest failed: %v\n", resp.Status)
		// If smoketest failed, populate the verdicts with as much info as we can (pulling executor
		// version from the smoketest version endpoint if possible.
		executor = <-versionChan
		for _, v := range sreq.Versions {
			verdicts = append(verdicts, schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(sreq.Ecosystem),
					Package:   sreq.Package,
					Version:   v,
					// TODO: This should be populated by the sreq when we support different artifact types.
					Artifact: "",
				},
				Message: fmt.Sprintf("build-local failed: %d \"%s\"", resp.StatusCode, resp.Status),
			})
		}
	} else {
		defer checkClose(resp.Body)
		d := json.NewDecoder(io.TeeReader(resp.Body, respCopy))
		var resp schema.SmoketestResponse
		if err := d.Decode(&resp); err != nil {
			log.Printf("json.Decode: %v\n", err)
			http.Error(rw, "Internal Error", 500)
			return
		}
		verdicts = resp.Verdicts
		executor = resp.Executor
	}
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		log.Printf("Firestore client: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	for _, v := range verdicts {
		var rawStrategy string
		if enc, err := json.Marshal(v.StrategyOneof); err == nil {
			rawStrategy = string(enc)
		} else {
			log.Printf("invalid strategy returned from smoketest: %v\n", err)
		}
		_, err := client.Collection("ecosystem").Doc(string(v.Target.Ecosystem)).Collection("packages").Doc(sanitize(sreq.Package)).Collection("versions").Doc(v.Target.Version).Collection("attempts").Doc(sreq.ID).Set(ctx, schema.SmoketestAttempt{
			Ecosystem:         string(v.Target.Ecosystem),
			Package:           v.Target.Package,
			Version:           v.Target.Version,
			Artifact:          v.Target.Artifact,
			Success:           v.Message == "",
			Message:           v.Message,
			Strategy:          rawStrategy,
			TimeCloneEstimate: v.Timings.Source.Seconds(),
			TimeSource:        v.Timings.Source.Seconds(),
			TimeInfer:         v.Timings.Infer.Seconds(),
			TimeBuild:         v.Timings.Build.Seconds(),
			ExecutorVersion:   executor,
			RunID:             sreq.ID,
			Created:           time.Now().UnixMilli(),
		})
		if err != nil {
			log.Printf("Failed to write record for [pkg=%s, version=%s]: %v\n", sreq.Package, v.Target.Version, err)
			http.Error(rw, "Internal Error", 500)
			return
		}
	}
	if respCopy.Len() > 0 {
		io.Copy(rw, respCopy)
	} else {
		log.Println("No SmoketestResponse to forward.")
		http.Error(rw, "Internal Error", 500)
		return
	}
}

func getInference(ctx context.Context, ireq schema.InferenceRequest) (rebuild.Strategy, error) {
	u, err := url.Parse(*inferenceURL)
	if err != nil {
		log.Fatalf("inference URL invalid: %v", err)
	}
	u = u.JoinPath("infer")
	runclient, err := idtoken.NewClient(ctx, *inferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "initializing client")
	}
	vals, err := ireq.ToValues()
	if err != nil {
		return nil, errors.Wrap(err, "marshalling inference request")
	}
	resp, err := runclient.PostForm(u.String(), vals)
	if err != nil {
		return nil, errors.Wrap(err, "sending request")
	}
	// TODO: Have inference signal application-level failure in-band.
	if resp.StatusCode != 200 {
		return nil, errors.Errorf("response not ok: %s", resp.Status)
	}
	oneof := schema.StrategyOneOf{}
	if err := json.NewDecoder(resp.Body).Decode(&oneof); err != nil {
		return nil, errors.Wrap(err, "decoding inference response")
	}
	strategy, err := oneof.Strategy()
	if err != nil {
		return nil, errors.Wrap(err, "unpacking strategy")
	}
	return strategy, nil
}

func populateArtifact(ctx context.Context, t *rebuild.Target, mux rebuild.RegistryMux) error {
	if t.Artifact != "" {
		return nil
	}
	switch t.Ecosystem {
	case rebuild.NPM:
		t.Artifact = fmt.Sprintf("%s-%s.tgz", sanitize(t.Package), t.Version)
	case rebuild.CratesIO:
		t.Artifact = fmt.Sprintf("%s-%s.crate", sanitize(t.Package), t.Version)
	case rebuild.PyPI:
		release, err := mux.PyPI.Release(t.Package, t.Version)
		if err != nil {
			return errors.Wrap(err, "fetching metadata failed")
		}
		a, err := pypirb.FindPureWheel(release.Artifacts)
		if err != nil {
			return errors.Wrap(err, "locating pure wheel failed")
		}
		t.Artifact = a.Filename
	default:
		return errors.New("unknown ecosystem")
	}
	return nil
}

func doNPMRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	if err := npmrb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	vmeta, err := mux.NPM.Version(t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	return vmeta.Dist.URL, nil
}

func doCratesRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	if err := cratesrb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	vmeta, err := mux.CratesIO.Version(t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	return vmeta.DownloadURL, nil
}

func doPyPIRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	release, err := mux.PyPI.Release(t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	for _, r := range release.Artifacts {
		if r.Filename == t.Artifact {
			upstreamURL = r.URL
		}
	}
	if upstreamURL == "" {
		return "", errors.New("artifact not found in release")
	}
	if err := pypirb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	return upstreamURL, nil
}

func makeKMSSigner(ctx context.Context, cryptoKeyVersion string) (*dsse.EnvelopeSigner, error) {
	kc, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating KMS client")
	}
	ckv, err := kc.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: cryptoKeyVersion})
	if err != nil {
		return nil, errors.Wrap(err, "fetching CryptoKeyVersion")
	}
	kmsSigner, err := kmsdsse.NewCloudKMSSigner(ctx, kc, ckv)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudKMSSigner")
	}
	dsseSigner, err := dsse.NewEnvelopeSigner(kmsSigner)
	if err != nil {
		return nil, errors.Wrap(err, "creating EnvelopeSigner")
	}
	return dsseSigner, nil
}

func HandleRebuildPackage(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	rbreq, err := schema.NewRebuildPackageRequest(req.Form)
	if err != nil {
		log.Println(errors.Wrap(err, "parsing rebuild request"))
		http.Error(rw, err.Error(), 400)
		return
	}
	t := rebuild.Target{Ecosystem: rbreq.Ecosystem, Package: rbreq.Package, Version: rbreq.Version}
	client, err := httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		log.Fatalf("Failed to initialize HTTP egress client: %v", err)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, client)
	regclient := httpinternal.NewCachedClient(client, &cache.CoalescingMemoryCache{})
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: regclient},
		NPM:      npmreg.HTTPRegistry{Client: regclient},
		PyPI:     pypireg.HTTPRegistry{Client: regclient},
	}
	if err := populateArtifact(ctx, &t, mux); err != nil {
		log.Println(errors.Wrap(err, "selecting artifact"))
		http.Error(rw, "Internal Error", 500)
		return
	}
	dsseSigner, err := makeKMSSigner(ctx, *signingKeyVersion)
	if err != nil {
		log.Println(errors.Wrap(err, "creating signer"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	// NOTE: Use an empty RunID here to reuse the path structure of the other buckets.
	attestation, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+*attestationBucket)
	if err != nil {
		log.Println(errors.Wrap(err, "creating attestation uploader"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	signer := verifier.InTotoEnvelopeSigner{EnvelopeSigner: dsseSigner}
	a := verifier.Attestor{Store: attestation, Signer: signer, AllowOverwrite: *overwriteAttestations}
	if !*overwriteAttestations {
		if exists, err := a.BundleExists(ctx, t); err != nil {
			log.Println(errors.Wrap(err, "checking existing bundle"))
			http.Error(rw, "Internal server error", 500)
			return
		} else if exists {
			http.Error(rw, "Conflict with existing attestation bundle", http.StatusConflict)
			return
		}
	}
	var manualStrategy, strategy rebuild.Strategy
	ireq := schema.InferenceRequest{
		Ecosystem: rbreq.Ecosystem,
		Package:   rbreq.Package,
		Version:   rbreq.Version,
	}
	if rbreq.StrategyOneof != nil {
		manualStrategy, err = rbreq.StrategyOneof.Strategy()
		if err != nil {
			err := errors.Wrap(err, "parsing provided strategy")
			log.Println(err)
			http.Error(rw, err.Error(), 400)
			return
		}
		if hint, ok := manualStrategy.(*rebuild.LocationHint); ok && hint != nil {
			ireq.LocationHint = hint
		} else if manualStrategy != nil {
			strategy = manualStrategy
		}
	}
	if strategy == nil {
		var err error
		strategy, err = getInference(ctx, ireq)
		if err != nil {
			log.Printf("Inference failed: %v", err)
			http.Error(rw, "Internal server error", 500)
			return
		}
	}
	id := uuid.New().String()
	var upstreamURI string
	hashes := []crypto.Hash{crypto.SHA256}
	opts := rebuild.RemoteOptions{
		Project:             *project,
		BuildServiceAccount: *buildRemoteIdentity,
		UtilPrebuildBucket:  *prebuildBucket,
		LogsBucket:          *logsBucket,
		MetadataBucket:      *metadataBucket,
	}
	// TODO: These doRebuild functions should return a verdict, and this handler
	// endpoint should forward those to the caller as a schema.Verdict.
	switch t.Ecosystem {
	case rebuild.NPM:
		hashes = append(hashes, crypto.SHA512)
		upstreamURI, err = doNPMRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.CratesIO:
		upstreamURI, err = doCratesRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.PyPI:
		upstreamURI, err = doPyPIRebuild(ctx, t, id, mux, strategy, opts)
	default:
		http.Error(rw, "Bad Request", 400)
		return
	}
	if err != nil {
		log.Println(errors.Wrap(err, "error rebuilding"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	metadata, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, id), "gs://"+*metadataBucket)
	if err != nil {
		log.Println(errors.Wrap(err, "creating metadata uploader"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	rb, up, err := verifier.SummarizeArtifacts(ctx, metadata, t, upstreamURI, hashes)
	if err != nil {
		log.Println(errors.Wrap(err, "comparing artifacts"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	exactMatch := bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	canonicalizedMatch := bytes.Equal(rb.CanonicalHash.Sum(nil), up.CanonicalHash.Sum(nil))
	if !exactMatch && !canonicalizedMatch {
		log.Println(errors.New("rebuild content mismatch"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	input := rebuild.Input{Target: t, Strategy: manualStrategy}
	eqStmt, buildStmt, err := verifier.CreateAttestations(ctx, input, strategy, id, rb, up, metadata)
	if err != nil {
		log.Println(errors.Wrap(err, "error creating attestations"))
		http.Error(rw, "Internal server error", 500)
		return
	}
	if err := a.PublishBundle(ctx, t, eqStmt, buildStmt); err != nil {
		log.Println(errors.Wrap(err, "error publishing bundle"))
		http.Error(rw, "Internal server error", 500)
		return
	}
}

func sanitize(key string) string {
	return strings.ReplaceAll(key, "/", "!")
}

func HandleGet(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	// FIXME encode scope in docref
	pkg, version := req.Form.Get("pkg"), req.Form.Get("version")
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		log.Println(err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	var snapshot *firestore.DocumentSnapshot
	iter := client.Collection("packages").Doc(sanitize(pkg)).Collection("versions").Doc(version).Collection("attempts").Limit(1).Documents(ctx)
	snapshot, err = iter.Next()
	if err == iterator.Done {
		http.Error(rw, "Not Found", 404)
		return
	}
	if err != nil {
		log.Println(err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	ret, err := json.Marshal(map[string]any{
		"package":          snapshot.Data()["package"].(string),
		"version":          snapshot.Data()["version"].(string),
		"success":          snapshot.Data()["success"].(bool),
		"message":          snapshot.Data()["message"].(string),
		"executor_version": snapshot.Data()["executor_version"].(string),
		"created":          time.UnixMilli(snapshot.Data()["created"].(int64)),
	})
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	rw.Write(ret)
}

func getRemoteVersion(ctx context.Context, endpoint string) ([]byte, error) {
	runclient, err := idtoken.NewClient(ctx, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "initializing authorized client")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "parsing endpoint URL '%s'", endpoint)
	}
	u = u.JoinPath("version")
	resp, err := runclient.Get(u.String())
	if err != nil {
		return nil, errors.Wrap(err, "sending version request")
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "version response")
	}
	v, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "parsing version response body")
	}
	return v, nil
}

func HandleVersion(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	s := req.Form.Get("service")
	var version []byte
	var err error
	switch s {
	case "":
		version = []byte(os.Getenv("K_REVISION"))
	case "build-local":
		version, err = getRemoteVersion(ctx, *buildLocalURL)
	case "inference":
		version, err = getRemoteVersion(ctx, *inferenceURL)
	default:
		rw.WriteHeader(http.StatusBadRequest)
		err = errors.Errorf("unknown service '%s'", s)
	}
	if err != nil {
		log.Println(err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	rw.Write(version)
}

func HandleCreateRun(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		log.Printf("Firestore client: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	id := time.Now().UTC().Format(time.RFC3339)
	if _, err := hex.DecodeString(req.Form.Get("hash")); err != nil {
		log.Printf("Hash decode failed: %v\n", err)
		http.Error(rw, "Failed to decode hex-encoded hash", http.StatusBadRequest)
		return
	}
	err = client.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		return t.Create(client.Collection("runs").Doc(id), map[string]any{
			"benchmark_name": req.Form.Get("name"),
			"benchmark_hash": req.Form.Get("hash"),
			"run_type":       req.Form.Get("type"),
			"created":        time.Now().UTC().UnixMilli(),
		})
	})
	if err != nil {
		log.Printf("Failed to create run: %v\n", err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	rw.Write([]byte(id))
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/smoketest", HandleRebuildSmoketest)
	http.HandleFunc("/rebuild", HandleRebuildPackage)
	http.HandleFunc("/get", HandleGet)
	http.HandleFunc("/version", HandleVersion)
	http.HandleFunc("/runs", HandleCreateRun)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
