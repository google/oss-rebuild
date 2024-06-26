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
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/apiservice"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/api/rebuilderservice"
	gcb "github.com/google/oss-rebuild/internal/cloudbuild"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/cloudbuild/v1"
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
	buildDefRepo          = flag.String("build-def-repo", "", "repository for build definitions")
	buildDefRepoDir       = flag.String("build-def-repo-dir", ".", "relpath within the build definitions repository")
	overwriteAttestations = flag.Bool("overwrite-attestations", false, "whether to overwrite existing attestations when writing to GCS")
)

var httpcfg = httpegress.Config{}

func RebuildSmoketestInit(ctx context.Context) (*apiservice.RebuildSmoketestDeps, error) {
	var d apiservice.RebuildSmoketestDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	u, err := url.Parse(*buildLocalURL)
	if err != nil {
		return nil, errors.Wrap(err, "parsing build local URL")
	}
	runclient, err := idtoken.NewClient(ctx, *buildLocalURL)
	if err != nil {
		return nil, errors.Wrap(err, "initializing build local client")
	}
	d.SmoketestStub = api.StubFromHandler(runclient, *u.JoinPath("smoketest"), rebuilderservice.RebuildSmoketest)
	d.VersionStub = api.StubFromHandler(runclient, *u.JoinPath("version"), rebuilderservice.Version)
	return &d, nil
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

func RebuildPackageInit(ctx context.Context) (*apiservice.RebuildPackageDeps, error) {
	var d apiservice.RebuildPackageDeps
	var err error
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	d.Signer, err = makeKMSSigner(ctx, *signingKeyVersion)
	if err != nil {
		return nil, errors.Wrap(err, "creating signer")
	}
	svc, err := cloudbuild.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudBuild service")
	}
	d.GCBClient = &gcb.Service{Service: svc}
	d.BuildProject = *project
	d.BuildServiceAccount = *buildRemoteIdentity
	d.UtilPrebuildBucket = *prebuildBucket
	d.BuildLogsBucket = *logsBucket
	repo, err := uri.CanonicalizeRepoURI(*buildDefRepo)
	if err != nil {
		return nil, errors.Wrap(err, "canonicalizing build def repo")
	}
	d.BuildDefRepo = rebuild.Location{
		Repo: repo,
		Ref:  plumbing.Main.String(),
		Dir:  path.Clean(*buildDefRepoDir),
	}
	d.AttestationStore, err = rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+*attestationBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating attestation uploader")
	}
	d.MetadataBuilder = func(ctx context.Context, id string) (rebuild.AssetStore, error) {
		return rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, id), "gs://"+*metadataBucket)
	}
	d.OverwriteAttestations = *overwriteAttestations
	u, err := url.Parse(*inferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "parsing inference URL")
	}
	u = u.JoinPath("infer")
	runclient, err := idtoken.NewClient(ctx, *inferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "initializing inference client")
	}
	d.InferStub = api.StubFromHandler(runclient, *u, inferenceservice.Infer)
	return &d, nil
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

func VersionInit(ctx context.Context) (*apiservice.VersionDeps, error) {
	var d apiservice.VersionDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	{
		u, err := url.Parse(*buildLocalURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing build local URL")
		}
		runclient, err := idtoken.NewClient(ctx, *buildLocalURL)
		if err != nil {
			return nil, errors.Wrap(err, "initializing build local client")
		}
		d.BuildLocalVersionStub = api.StubFromHandler(runclient, *u.JoinPath("version"), rebuilderservice.Version)
	}
	{
		u, err := url.Parse(*inferenceURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing inference URL")
		}
		runclient, err := idtoken.NewClient(ctx, *inferenceURL)
		if err != nil {
			return nil, errors.Wrap(err, "initializing inference client")
		}
		d.InferenceVersionStub = api.StubFromHandler(runclient, *u.JoinPath("version"), inferenceservice.Version)
	}
	return &d, nil
}

func CreateRunInit(ctx context.Context) (*apiservice.CreateRunDeps, error) {
	var d apiservice.CreateRunDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &d, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/smoketest", api.Handler(RebuildSmoketestInit, apiservice.RebuildSmoketest))
	http.HandleFunc("/rebuild", api.Handler(RebuildPackageInit, apiservice.RebuildPackage))
	http.HandleFunc("/get", HandleGet)
	http.HandleFunc("/version", api.Handler(VersionInit, apiservice.Version))
	http.HandleFunc("/runs", api.Handler(CreateRunInit, apiservice.CreateRun))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
