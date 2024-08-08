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
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"path"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"github.com/spf13/cobra"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

var (
	output = flag.String("output", "payload", "Output format [bundle, payload, dockerfile, build, steps]")
	bucket = flag.String("bucket", "google-rebuild-attestations", "GCS bucket from which to pull rebuild attestations")
)

var rootCmd = &cobra.Command{
	Use:   "oss-rebuild [subcommand]",
	Short: "A CLI tool for OSS Rebuild",
}

// Bundle is a SLSA rebuild attestation bundle.
type Bundle struct {
	Bytes []byte
}

func NewBundle(ctx context.Context, t rebuild.Target, attestation rebuild.AssetStore) (*Bundle, error) {
	r, _, err := attestation.Reader(ctx, rebuild.Asset{Target: t, Type: rebuild.AttestationBundleAsset})
	if err != nil {
		log.Fatal(errors.Wrap(err, "opening bundle"))
	}
	bundle := bytes.NewBuffer(nil)
	defer r.Close()
	if _, err := io.Copy(bundle, r); err != nil {
		log.Fatal(errors.Wrap(err, "reading bundle"))
	}
	return &Bundle{bundle.Bytes()}, nil
}

func unwrapEnvelope(e *dsse.Envelope) (*in_toto.ProvenanceStatementSLSA1, error) {
	if e.Payload == "" {
		return nil, errors.New("no payload")
	}
	b, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, errors.New("payload is not b64 encoded")
	}
	var decoded *in_toto.ProvenanceStatementSLSA1
	if json.Unmarshal(b, &decoded) != nil {
		return nil, errors.New("payload is not valid json")
	}
	return decoded, nil
}

// Payloads returns all payloads in the bundle.
func (b *Bundle) Payloads() ([]*in_toto.ProvenanceStatementSLSA1, error) {
	bundle := bytes.NewBuffer(b.Bytes)
	d := json.NewDecoder(bundle)
	var payloads []*in_toto.ProvenanceStatementSLSA1
	for {
		var envelope dsse.Envelope
		err := d.Decode(&envelope)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.Wrap(err, "decoding envelope")
		}
		p, err := unwrapEnvelope(&envelope)
		if err != nil {
			return nil, errors.Wrap(err, "unwrapping envelope")
		}
		payloads = append(payloads, p)
	}
	return payloads, nil
}

func (b *Bundle) rebuildAttestation() (*in_toto.ProvenanceStatementSLSA1, error) {
	payloads, err := b.Payloads()
	if err != nil {
		return nil, err
	}
	for _, p := range payloads {
		if p.Predicate.BuildDefinition.BuildType == verifier.RebuildBuildType {
			return p, nil
		}
	}
	return nil, errors.New("no rebuild attestation found")
}

// Byproduct returns the named byproduct from the rebuild attestation.
func (b *Bundle) Byproduct(name string) ([]byte, error) {
	att, err := b.rebuildAttestation()
	if err != nil {
		return nil, err
	}
	for _, b := range att.Predicate.RunDetails.Byproducts {
		if b.Name == name {
			return b.Content, nil
		}
	}
	return nil, fmt.Errorf("byproduct named %s not found", name)
}

func writeIndentedJson(out io.Writer, b []byte) error {
	var decoded interface{}
	if err := json.NewDecoder(bytes.NewBuffer(b)).Decode(&decoded); err != nil {
		return errors.Wrap(err, "decoding json")
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	if err := e.Encode(decoded); err != nil {
		log.Fatal(errors.Wrap(err, "encoding json"))
	}
	return nil
}

var getCmd = &cobra.Command{
	Use:   "get <ecosystem> <package> <version> [<artifact>]",
	Short: "Get rebuild attestation for a specific artifact.",
	Long: `Get rebuild attestation for a specific ecosystem/package/version/artifact.
The ecosystem is one of npm, pypi, or cratesio. For npm the artifact is the <package>-<version>.tar.gz file. For pypi the artifact is the wheel file. For cratesio the artifact is the <package>-<version>.crate file.`,
	Args: cobra.MinimumNArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 4 {
			log.Fatal("Too many arguments")
		}
		var t rebuild.Target
		{
			ecosystem := rebuild.Ecosystem(args[0])
			pkg := args[1]
			version := args[2]
			var artifact string
			if len(args) < 4 {
				switch ecosystem {
				case rebuild.CratesIO:
					artifact = fmt.Sprintf("%s-%s.crate", pkg, version)
				case rebuild.PyPI:
					artifact = fmt.Sprintf("%s-%s-py3-none-any.whl", strings.ReplaceAll(pkg, "-", "_"), version)
					l := log.New(cmd.OutOrStderr(), "", 0)
					l.Printf("pypi artifact is being inferred as %s\n", artifact)
				case rebuild.NPM:
					artifact = fmt.Sprintf("%s-%s.tgz", pkg, version)
				default:
					log.Fatalf("Unsupported ecosystem: \"%s\"", ecosystem)
				}
			} else {
				artifact = args[3]
			}
			t = rebuild.Target{
				Ecosystem: ecosystem,
				Package:   pkg,
				Version:   version,
				Artifact:  artifact,
			}
		}
		var bundle *Bundle
		{
			ctx := cmd.Context()
			ctx = context.WithValue(ctx, rebuild.RunID, "")
			ctx = context.WithValue(ctx, rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
			attestation, err := rebuild.NewGCSStore(ctx, "gs://"+*bucket)
			if err != nil {
				log.Fatal(errors.Wrap(err, "initializing GCS store"))
			}
			bundle, err = NewBundle(ctx, t, attestation)
			if err != nil {
				log.Fatal(err)
			}
		}
		switch *output {
		case "bundle":
			cmd.OutOrStdout().Write(bundle.Bytes)
			return
		case "payload":
			payloads, err := bundle.Payloads()
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting payloads"))
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			for _, p := range payloads {
				if encoder.Encode(p) != nil {
					log.Fatal(errors.Wrap(err, "pprinting payload"))
				}
			}
		case "dockerfile":
			dockerfile, err := bundle.Byproduct("Dockerfile")
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting dockerfile"))
			}
			cmd.OutOrStdout().Write(dockerfile)
		case "build":
			build, err := bundle.Byproduct("build.json")
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting build.json"))
			}
			cmd.OutOrStdout().Write(build)
			if err := writeIndentedJson(cmd.OutOrStdout(), build); err != nil {
				log.Fatal(errors.Wrap(err, "encoding build.json"))
			}
		case "steps":
			steps, err := bundle.Byproduct("steps.json")
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting steps.json"))
			}
			if err := writeIndentedJson(cmd.OutOrStdout(), steps); err != nil {
				log.Fatal(errors.Wrap(err, "encoding steps.json"))
			}
		default:
			log.Fatal(errors.New("unsupported format: " + *output))
		}
	},
}

var listCmd = &cobra.Command{
	Use:   "list <ecosystem> <package> [<version>]",
	Short: "List artifacts with rebuild attestations for a given query",
	Args:  cobra.MaximumNArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 2 {
			log.Fatal("Please include at least an ecosystem and package")
		}
		gcsClient, err := gcs.NewClient(cmd.Context(), option.WithoutAuthentication())
		if err != nil {
			log.Fatal(errors.Wrap(err, "initializing GCS client"))
		}
		query := &gcs.Query{
			Prefix: path.Join(args...),
		}
		query.SetAttrSelection([]string{"Name"})
		it := gcsClient.Bucket(*bucket).Objects(cmd.Context(), query)
		for {
			obj, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				log.Fatal(errors.Wrap(err, "listing objects"))
			}
			io.WriteString(cmd.OutOrStdout(), obj.Name+"\n")
		}
	},
}

func init() {
	rootCmd.AddCommand(getCmd)

	getCmd.Flags().AddGoFlag(flag.Lookup("output"))
	getCmd.Flags().AddGoFlag(flag.Lookup("bucket"))

	rootCmd.AddCommand(listCmd)

	listCmd.Flags().AddGoFlag(flag.Lookup("bucket"))
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
