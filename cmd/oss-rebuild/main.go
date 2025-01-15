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
	"slices"
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
	output       = flag.String("output", "payload", "Output format [bundle, payload, dockerfile, build, steps]")
	bucket       = flag.String("bucket", "google-rebuild-attestations", "GCS bucket from which to pull rebuild attestations")
	verify       = flag.Bool("verify", true, "whether to verify rebuild attestation signatures")
	verifyWith   = flag.String("verify-with", ossRebuildKeyURI, "comma-separated list of key URIs used to verify rebuild attestation signatures")
	verifyOnline = flag.Bool("verify-online", false, "whether to always fetch --verify-with key contents, ignoring embedded contents")
)

var rootCmd = &cobra.Command{
	Use:   "oss-rebuild [subcommand]",
	Short: "A CLI tool for OSS Rebuild",
}

type VerifiedEnvelope struct {
	Raw     *dsse.Envelope
	Payload *in_toto.ProvenanceStatementSLSA1
}

type Bundle struct {
	envelopes []VerifiedEnvelope
}

func decodeEnvelopePayload(e *dsse.Envelope) (*in_toto.ProvenanceStatementSLSA1, error) {
	if e.Payload == "" {
		return nil, errors.New("empty payload")
	}
	b, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, errors.Wrap(err, "decoding base64 payload")
	}
	var decoded in_toto.ProvenanceStatementSLSA1
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, errors.Wrap(err, "unmarshaling payload")
	}
	return &decoded, nil
}

func NewBundle(ctx context.Context, data []byte, verifier *dsse.EnvelopeVerifier) (*Bundle, error) {
	d := json.NewDecoder(bytes.NewBuffer(data))
	var envelopes []VerifiedEnvelope
	for {
		var env dsse.Envelope
		if err := d.Decode(&env); err != nil {
			if err == io.EOF {
				break
			}
			return nil, errors.Wrap(err, "decoding envelope")
		}
		if _, err := verifier.Verify(ctx, &env); err != nil {
			return nil, errors.Wrap(err, "verifying envelope")
		}
		payload, err := decodeEnvelopePayload(&env)
		if err != nil {
			return nil, errors.Wrap(err, "decoding payload")
		}
		envelopes = append(envelopes, VerifiedEnvelope{
			Raw:     &env,
			Payload: payload,
		})
	}
	return &Bundle{envelopes: envelopes}, nil
}

func (b *Bundle) Payloads() []*in_toto.ProvenanceStatementSLSA1 {
	result := make([]*in_toto.ProvenanceStatementSLSA1, len(b.envelopes))
	for i, env := range b.envelopes {
		result[i] = env.Payload
	}
	return result
}

func (b *Bundle) RebuildAttestation() (*in_toto.ProvenanceStatementSLSA1, error) {
	for _, env := range b.envelopes {
		if env.Payload.Predicate.BuildDefinition.BuildType == verifier.RebuildBuildType {
			return env.Payload, nil
		}
	}
	return nil, errors.New("no rebuild attestation found")
}

func (b *Bundle) Byproduct(name string) ([]byte, error) {
	att, err := b.RebuildAttestation()
	if err != nil {
		return nil, errors.Wrap(err, "getting rebuild attestation")
	}
	for _, b := range att.Predicate.RunDetails.Byproducts {
		if b.Name == name {
			return b.Content, nil
		}
	}
	return nil, errors.Errorf("byproduct %q not found", name)
}

func writeIndentedJson(out io.Writer, b []byte) error {
	var decoded any
	if err := json.NewDecoder(bytes.NewBuffer(b)).Decode(&decoded); err != nil {
		return errors.Wrap(err, "decoding json")
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	if err := e.Encode(decoded); err != nil {
		return errors.Wrap(err, "encoding json")
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
		var bundleBytes []byte
		{
			ctx := cmd.Context()
			ctx = context.WithValue(ctx, rebuild.RunID, "")
			ctx = context.WithValue(ctx, rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
			attestation, err := rebuild.NewGCSStore(ctx, "gs://"+*bucket)
			if err != nil {
				log.Fatal(errors.Wrap(err, "initializing GCS store"))
			}
			var verifiers []dsse.Verifier
			if !*verify {
				verifiers = append(verifiers, &trustAllVerifier{})
			} else {
				keysToAdd := slices.DeleteFunc(strings.Split(*verifyWith, ","), func(s string) bool { return s == "" })
				var keysAdded []string
				if !*verifyOnline {
					for _, key := range embeddedKeys {
						if !slices.Contains(keysToAdd, key.ID) {
							continue
						}
						verifiers = append(verifiers, &keyVerifier{key})
						keysAdded = append(keysAdded, key.ID)
					}
				}
				for _, uri := range keysToAdd {
					if slices.Contains(keysAdded, uri) {
						continue
					}
					switch {
					case strings.HasPrefix(uri, kmsV1API):
						verifier, err := makeKMSVerifier(ctx, ossRebuildKeyResource)
						if err != nil {
							log.Fatal(err)
						}
						verifiers = append(verifiers, verifier)
					default:
						log.Fatalf("unsupported key URI: %s", uri)
					}
					keysAdded = append(keysAdded, uri)
				}
			}
			dsseVerifier, err := dsse.NewEnvelopeVerifier(verifiers...)
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating EnvelopeVerifier"))
			}
			r, err := attestation.Reader(ctx, rebuild.AttestationBundleAsset.For(t))
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating attestation reader"))
			}
			bundleBytes, err = io.ReadAll(r)
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating attestation reader"))
			}
			bundle, err = NewBundle(ctx, bundleBytes, dsseVerifier)
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating bundle"))
			}
		}
		switch *output {
		case "bundle":
			cmd.OutOrStdout().Write(bundleBytes)
			return
		case "payload":
			payloads := bundle.Payloads()
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			for _, p := range payloads {
				if err := encoder.Encode(p); err != nil {
					log.Fatal(errors.Wrap(err, "pprinting payload"))
				}
			}
		case "dockerfile":
			dockerfile, err := bundle.Byproduct("Dockerfile")
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting dockerfile"))
			}
			if _, err := cmd.OutOrStdout().Write(dockerfile); err != nil {
				log.Fatal(errors.Wrap(err, "writing dockerfile"))
			}
		case "build":
			build, err := bundle.Byproduct("build.json")
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting build.json"))
			}
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
	getCmd.Flags().AddGoFlag(flag.Lookup("verify"))
	getCmd.Flags().AddGoFlag(flag.Lookup("verify-with"))
	getCmd.Flags().AddGoFlag(flag.Lookup("verify-online"))

	rootCmd.AddCommand(listCmd)

	listCmd.Flags().AddGoFlag(flag.Lookup("bucket"))
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
