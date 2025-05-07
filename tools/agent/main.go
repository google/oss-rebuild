// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"cloud.google.com/go/vertexai/genai"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const (
	maxFileSize  = 100 * 1024      // 100KB max file size to upload
	maxTotalSize = 2 * 1024 * 1024 // 2MB max total upload size (stay safely below Gemini's limits)
	maxFiles     = 200             // Maximum number of files to include
	maxAttempts  = 3               // Maximum number of times to attempt a rebuild
)

var (
	pkg         = flag.String("pkg", "", "package to rebuild")
	version     = flag.String("version", "", "version to rebuild")
	projectID   = flag.String("project", "", "GCP Project ID for Vertex AI")
	location    = flag.String("location", "us-central1", "GCP location for Vertex AI")
	boostrapURL = flag.String("prebuild-url", "", "URL of the prebuild tools to be used in builds")
)

func main() {
	flag.Parse()

	if *pkg == "" || *version == "" {
		log.Fatal("pkg and version must be specified")
	}

	ctx := context.Background()

	client, err := genai.NewClient(ctx, *projectID, *location)
	if err != nil {
		log.Fatalf("Failed to initialize Vertex AI client: %v", err)
	}
	defer client.Close()

	tg := rebuild.Target{Ecosystem: rebuild.NPM, Package: *pkg, Version: *version}
	tg.Artifact = npm.ArtifactName(tg)
	reg := npmreg.HTTPRegistry{Client: http.DefaultClient}
	var repo *git.Repository
	var loc rebuild.Location
	{
		vmeta, err := reg.Version(ctx, tg.Package, tg.Version)
		if err != nil {
			log.Fatalf("Failed to get version metadata: %v", err)
		}

		var repoURL string
		if vmeta.Repository.URL != "" {
			repoURL, err = uri.CanonicalizeRepoURI(vmeta.Repository.URL)
			if err != nil {
				log.Fatalf("Failed to canonicalize repo URI: %v", err)
			}
		} else {
			pmeta, err := reg.Package(ctx, tg.Package)
			if err != nil {
				log.Fatalf("Failed to get package metadata: %v", err)
			}

			keys := make([]string, 0, len(pmeta.Versions))
			for k := range pmeta.Versions {
				keys = append(keys, k)
			}
			slices.SortFunc(keys, func(a, b string) int { return semver.Cmp(a, b) })
			var after bool
			for _, key := range keys {
				if !after {
					if key == tg.Version {
						after = true
					}
					continue
				}
				if u := pmeta.Versions[key].Repository.URL; u != "" {
					repoURL, err = uri.CanonicalizeRepoURI(u)
					if err != nil {
						log.Fatalf("Failed to canonicalize repo URI: %v", err)
					}
					break
				}
			}
		}
		repo, err = git.CloneContext(ctx, memory.NewStorage(), memfs.New(), &git.CloneOptions{
			URL:        repoURL,
			NoCheckout: true,
		})
		if err != nil {
			log.Fatalf("Failed to clone repo: %v", err)
		}
		loc, _, err = npm.InferLocation(tg, vmeta, &rebuild.RepoConfig{Repository: repo, URI: repoURL})
		if err != nil {
			log.Fatalf("Failed to infer location: %v", err)
		}
		if loc.Dir == "." {
			loc.Dir = ""
		}
	}

	log.Println("Cloning repository using go-git...")
	t, err := getRepoTree(repo, loc.Ref)
	if err != nil {
		log.Fatalf("Failed to get repo tree: %v", err)
	}
	functionDefinitions := []*llm.FunctionDefinition{
		{
			FunctionDeclaration: genai.FunctionDeclaration{
				Name:        "read_repo_file",
				Description: "Fetch the content of the file from the source repository",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Path of the file to be read, relative to the repository root"},
					},
					Required: []string{"path"},
				},
				Response: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"content": {Type: genai.TypeString, Description: "The file content, if read was successful"},
						"error":   {Type: genai.TypeString, Description: "The error reading the requested file, if unsuccessful"},
					},
				},
			},
			Function: func(args map[string]any) genai.FunctionResponse {
				path := args["path"].(string)
				var content, errStr string
				content, err := getRepoFile(t, path)
				if err != nil {
					errStr = err.Error()
				}
				return genai.FunctionResponse{
					Name: "read_repo_file", // Name must match the FunctionDeclaration
					Response: map[string]any{
						"content": content,
						"error":   errStr,
					},
				}
			},
		},
		{
			FunctionDeclaration: genai.FunctionDeclaration{
				Name:        "list_repo_files",
				Description: "Fetch the list of the file from the source repository",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Path of the directory to be read, relative to the repository root. Omit or use empty string for root."}, // Clarified description
					},
					Required: []string{},
				},
				Response: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"entries": {Type: genai.TypeArray, Description: "The list of files and directories at the requested path, if read was successful", Items: &genai.Schema{Type: genai.TypeString, Description: "A file path, ending with a slash if a directory"}},
						"error":   {Type: genai.TypeString, Description: "The error listing the requested path, if unsuccessful"},
					},
				},
			},
			Function: func(args map[string]any) genai.FunctionResponse {
				var path string
				if patharg, ok := args["path"]; ok {
					if p, ok := patharg.(string); ok {
						path = p
					}
					// TODO: Handle case where path exists but is not a string?
				}
				var errStr string
				content, err := listRepoFiles(t, path)
				if err != nil {
					errStr = err.Error()
				}
				entries := make([]any, 0, len(content))
				for _, entry := range content {
					entries = append(entries, entry)
				}
				return genai.FunctionResponse{
					Name: "list_repo_files", // Name must match the FunctionDeclaration
					Response: map[string]any{
						"entries": entries,
						"error":   errStr,
					},
				}
			},
		},
	}
	// Create the model with appropriate configuration
	model := client.GenerativeModel(llm.GeminiFlash)
	model.GenerationConfig = genai.GenerationConfig{
		Temperature:     genai.Ptr[float32](.1),
		MaxOutputTokens: genai.Ptr[int32](16000),
	}
	model.ToolConfig = &genai.ToolConfig{
		FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingAuto},
	}
	systemPrompt := []genai.Part{
		genai.Text(`You are an expert in building npm packages from source. Given a git repository with a JavaScript/TypeScript project, you will analyze the code to determine how to build an npm package from it.
Focus on identifying the build process from package.json scripts and any build configuration files.
Provide clear, executable shell commands that would successfully build the package.`),
	}
	model = llm.WithSystemPrompt(*model, systemPrompt...)
	chat, err := llm.NewChat(model, &llm.ChatOpts{Tools: functionDefinitions})
	if err != nil {
		log.Fatalf("NewChat error: %v", err)
	}
	var response genai.Content
	{
		// Set up a initialPrompt and start the conversation
		initialPrompt := fmt.Sprintf(`
You are an expert in building npm packages from source. I have a git repository that contains an npm package.
Please use the available tools to analyze the repository and provide detailed instructions on how to build this package.

Repository URL: %s
Commit Hash: %s

   - Clone the repository and checkout the specific commit
   - Install any required dependencies
   - Build the package
   - Pack the package

Only include commands that are needed to build the package, excluding things like linting and testing. Focus on standard npm procedures.
`, loc.Repo, loc.Ref)[1:]
		contentParts := []genai.Part{genai.Text(initialPrompt)}
		log.Println("Requesting build instructions from Gemini...")
		for content, err := range chat.SendMessageStream(ctx, contentParts...) {
			if err != nil {
				log.Fatalf("Chat error: %v", err)
			}
			log.Printf("%s\n\n", llm.FormatContent(*content))
			response = *content
		}
	}
	toScript := func(instructions genai.Text) (*llm.ScriptResponse, error) {
		prompt := fmt.Sprintf(`
From the following llm response, extract only the shell commands to be run and exclude any commands used to clone, checkout, and navigate to the git repo:

%s
`, instructions)[1:]
		contentParts := []genai.Part{genai.Text(prompt)}
		log.Println(llm.FormatContent(*genai.NewUserContent(contentParts...)))
		model.GenerationConfig.ResponseSchema = llm.ScriptResponseSchema
		model.GenerationConfig.ResponseMIMEType = llm.JSONMIMEType
		script := llm.ScriptResponse{}
		err := llm.GenerateTypedContent(ctx, model, &script, contentParts...)
		if err != nil {
			return nil, err
		}
		return &script, nil
	}
	script, err := toScript(response.Parts[0].(genai.Text))
	if err != nil {
		log.Fatalf("model error: %v", err)
	}
	log.Printf("model response:\n%+v\n", script)
	tmp, err := os.MkdirTemp("/tmp", "agent*")
	if err != nil {
		log.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tmp)
	ofs := osfs.New(tmp)
	store := rebuild.NewFilesystemAssetStore(ofs)
	for range maxAttempts {
		strat, err := generate(ctx, tg, loc, *script)
		if err != nil {
			log.Fatalf("Failed to generate strategy: %v", err)
		}
		err = execute(ctx, tg, *strat, store)
		if err != nil {
			log.Fatalf("Failed to execute strategy: %v", err)
		}
		var diff string
		{
			// diff strategy against upstream
			if err := upstream(ctx, tg, store); err != nil {
				log.Fatalf("Failed to retrieve upstream artifact: %v", err)
			}
			up := store.URL(rebuild.DebugUpstreamAsset.For(tg)).Path
			rb := store.URL(rebuild.RebuildAsset.For(tg)).Path
			if err := stabilizeInPlace(rb, osfs.New("/"), tg); err != nil {
				log.Fatalf("Failed to stabilize rebuild artifact: %v", err)
			}
			if err := stabilizeInPlace(up, osfs.New("/"), tg); err != nil {
				log.Fatalf("Failed to stabilize upstream artifact: %v", err)
			}
			diffOutput, err := runDiffoscope(rb, up)
			if err != nil {
				log.Fatalf("Failed to run diffoscope: %v", err)
			}
			diff = string(diffOutput)
			if len(diff) == 0 {
				log.Println("Success!")
				strategyData, err := yaml.Marshal(schema.NewStrategyOneOf(strat))
				if err != nil {
					log.Fatalf("Failed to marshal strategy: %v", err)
				}
				log.Println(string(strategyData))
				break
			}
		}
		var response genai.Content
		{
			logsReader, err := store.Reader(ctx, rebuild.DebugLogsAsset.For(tg))
			if err != nil {
				log.Fatalf("Failed to read build logs: %v", err)
			}
			logsData, err := io.ReadAll(logsReader)
			if err != nil {
				log.Fatalf("Failed to read build logs: %v", err)
			}
			recoveryPrompt := fmt.Sprintf(`
The previous build completed but produced a different artifact to the one published to npm.

Tasks:
- Understand the diff and explore possible causes of the difference.
- Propose a new set of commands for the build that might resolve this difference.

The diff of the rebuild versus the upstream is as follows:

%s

And here is the log of the build that produced this artifact:

%s

Again, the end goal should be the commands that are needed to build the package, excluding things like linting and testing. Focus on standard npm procedures.
`, diff, string(logsData))[1:]
			contentParts := []genai.Part{genai.Text(recoveryPrompt)}
			// Get build instructions from Gemini
			log.Println("Requesting updated build instructions from Gemini...")
			for content, err := range chat.SendMessageStream(ctx, contentParts...) {
				if err != nil {
					log.Fatalf("Chat error: %v", err)
				}
				log.Printf("%s\n\n", llm.FormatContent(*content))
				response = *content
			}
		}
		script, err = toScript(response.Parts[0].(genai.Text))
		if err != nil {
			log.Fatalf("Recovery generation error: %s", err)
		}
	}
}

func stabilizeInPlace(pth string, tfs billy.Filesystem, t rebuild.Target) error {
	buf := bytes.NewBuffer(nil)
	orig, err := tfs.OpenFile(pth, os.O_RDWR, os.ModePerm)
	if err != nil {
		return errors.Wrap(err, "opening file")
	}
	defer orig.Close()
	if err := archive.Stabilize(buf, orig, t.ArchiveType()); err != nil {
		return errors.Wrap(err, "stabilizing")
	}
	if _, err := orig.Seek(0, io.SeekStart); err != nil {
		return errors.Wrap(err, "seeking to start")
	}
	_, err = io.Copy(orig, buf)
	return errors.Wrap(err, "copying back to file")
}

func runDiffoscope(rebuildPath, upstreamPath string) ([]byte, error) {
	args := []string{"--no-progress", rebuildPath, upstreamPath}
	if _, err := exec.LookPath("uvx"); err == nil {
		args = slices.Concat([]string{"uvx", "diffoscope"}, args)
	} else if _, err := exec.LookPath("diffoscope"); err == nil {
		args = slices.Concat([]string{"diffoscope"}, args)
	} else if _, err := exec.LookPath("docker"); err == nil {
		updir := filepath.Dir(upstreamPath)
		rbdir := filepath.Dir(rebuildPath)
		args = slices.Concat([]string{"docker", "run", "--rm", "-t", "-v", updir + ":" + updir + ":ro", "-v", rbdir + ":" + rbdir + ":ro", "registry.salsa.debian.org/reproducible-builds/diffoscope"}, args)
	} else {
		log.Println("No execution option found for diffoscope. Attempted {diffoscope,uvx,docker}")
		return nil, errors.New("failed to run diffoscope")
	}
	buf := bytes.NewBuffer(nil)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = io.MultiWriter(os.Stdout, buf)
	cmd.Stderr = os.Stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func upstream(ctx context.Context, t rebuild.Target, out rebuild.AssetStore) error {
	reg := npmreg.HTTPRegistry{Client: http.DefaultClient}
	r, err := reg.Artifact(ctx, t.Package, t.Version)
	if err != nil {
		return err
	}
	w, err := out.Writer(ctx, rebuild.DebugUpstreamAsset.For(t))
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, r)
	return err
}

func execute(ctx context.Context, t rebuild.Target, strategy rebuild.WorkflowStrategy, out rebuild.AssetStore) error {
	inst, err := strategy.GenerateFor(t, rebuild.BuildEnv{TimewarpHost: "localhost:8081"})
	if err != nil {
		return err
	}
	buf := &bytes.Buffer{}
	err = template.Must(template.New("").Parse(
		`set -eux
apk add curl
curl `+*boostrapURL+`/timewarp > timewarp
chmod +x timewarp
./timewarp -port 8081 &
while ! nc -z localhost 8081;do sleep 1;done
mkdir /src && cd /src
apk add {{.SystemDeps}}
{{.Inst.Source}}
{{.Inst.Deps}}
{{.Inst.Build}}
cp /src/{{.Inst.OutputPath}} /out/rebuild`)).Execute(buf, map[string]any{"Inst": inst, "SystemDeps": strings.Join(inst.SystemDeps, " ")})
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("/tmp", "rebuild*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	logbuf := &bytes.Buffer{}
	outw := io.MultiWriter(os.Stdout, logbuf)
	cmd := exec.CommandContext(ctx, "docker", "run", "-i", "--rm", "-v", tmp+":/out", "alpine", "sh")
	cmd.Stdin = buf
	cmd.Stdout = outw
	cmd.Stderr = outw
	if err := cmd.Run(); err != nil {
		if logw, err := out.Writer(ctx, rebuild.DebugLogsAsset.For(t)); err == nil {
			defer logw.Close()
			io.Copy(logw, logbuf)
		}
		return err
	}
	logw, err := out.Writer(ctx, rebuild.DebugLogsAsset.For(t))
	if err != nil {
		return err
	}
	defer logw.Close()
	if _, err := io.Copy(logw, logbuf); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(tmp, "rebuild"))
	if err != nil {
		return err
	}

	w, err := out.Writer(ctx, rebuild.RebuildAsset.For(t))
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, f)
	return err
}

var errSkip = errors.New("skip")

func generate(ctx context.Context, t rebuild.Target, location rebuild.Location, candidate llm.ScriptResponse) (*rebuild.WorkflowStrategy, error) {
	reg := npmreg.HTTPRegistry{Client: http.DefaultClient}
	vmeta, err := reg.Version(ctx, t.Package, t.Version)
	if err != nil {
		return nil, err
	}
	nodeVersion, err := npm.PickNodeVersion(vmeta)
	if err != nil {
		return nil, err
	}
	npmv, err := npm.PickNPMVersion(vmeta)
	if err != nil {
		return nil, err
	}
	var newCommands []string
	if strings.HasPrefix(npmv, "6") {
		newCommands = append(newCommands, "npm config set unsafe-perm true")
	}
	pkg, err := reg.Package(ctx, t.Package)
	if err != nil {
		return nil, err
	}
	regTime := pkg.UploadTimes[t.Version]
	var doInstall bool
	for _, cmd := range candidate.Commands {
		if cmd == "npm install" {
			doInstall = true
		} else {
			newCommands = append(newCommands, cmd)
		}
	}
	strat := &rebuild.WorkflowStrategy{
		Location: location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "npm/install-node",
			With: map[string]string{"nodeVersion": nodeVersion},
		}},
		Build: []flow.Step{{
			Uses: "npm/npx",
			With: map[string]string{
				"command":      strings.Join(newCommands, " && "),
				"npmVersion":   npmv,
				"registryTime": regTime.Format(time.RFC3339),
				"dir":          "{{.Location.Dir}}",
				"locator":      "/usr/local/bin/",
			},
		}},
		OutputDir: location.Dir,
	}
	if doInstall {
		strat.Deps = append(strat.Deps, flow.Step{
			Uses: "npm/install",
			With: map[string]string{
				"npmVersion":   npmv,
				"registryTime": regTime.Format(time.RFC3339),
			},
		})
	}
	return strat, nil
}
