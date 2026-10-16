// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// seedProposalRecord names the seed build's proposal object in the transcript.
const seedProposalRecord = "0-seed-proposal.json"

// releaseParams are the strategy fields inference derives from a release.
// A seed written against the sibling's values gets the target's.
var releaseParams = []string{"npm_version", "node_version", "rust_version", "python_version"}

// readSeedOrigin reads the seed session's inference proposal, which names the
// release parameters its build was written for. It is nil if unreadable.
func readSeedOrigin(ctx context.Context, client *gcs.Client, bucket, session string) *schema.StrategyOneOf {
	if client == nil || bucket == "" {
		return nil
	}
	r, err := client.Bucket(bucket).Object(path.Join(session, "messages", "1", "0-inference-proposal.json")).NewReader(ctx)
	if err != nil {
		log.Printf("seed: reading the inference proposal of session %s: %v", session, err)
		return nil
	}
	defer r.Close()
	var s schema.StrategyOneOf
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		log.Printf("seed: decoding the inference proposal of session %s: %v", session, err)
		return nil
	}
	return &s
}

// strategyFields is a strategy's kind and its fields as JSON values.
func strategyFields(s *schema.StrategyOneOf) (string, map[string]any, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", nil, err
	}
	var oneof map[string]map[string]any
	if err := json.Unmarshal(b, &oneof); err != nil {
		return "", nil, err
	}
	for kind, fields := range oneof {
		if len(oneof) == 1 {
			return kind, fields, nil
		}
	}
	return "", nil, errors.Errorf("strategy has %d kinds", len(oneof))
}

// relocate rewrites the seed's build for t at loc. The version and each release
// parameter both proposals name become t's wherever the build spells them.
// The seed session's own settings are kept.
//
// TODO: Consider a prompt that carries the build over instead. Rewriting the
// serialized strategy as text can change unrelated values.
func relocate(seed *schema.AgentSeed, from, to *schema.StrategyOneOf, t rebuild.Target, loc rebuild.Location) (*schema.StrategyOneOf, error) {
	kind, fields, err := strategyFields(seed.Strategy)
	if err != nil {
		return nil, errors.Wrap(err, "reading the seed")
	}
	oldDir, _ := fields["dir"].(string)
	fields["repo"], fields["ref"], fields["dir"] = loc.Repo, loc.Ref, loc.Dir
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	text := string(b)
	if oldDir != "" && loc.Dir != "" {
		text = strings.ReplaceAll(text, oldDir, loc.Dir)
	}
	text = replaceVersion(text, seed.Version, t.Version)
	if from != nil && to != nil {
		_, fromFields, ferr := strategyFields(from)
		_, toFields, terr := strategyFields(to)
		if ferr == nil && terr == nil {
			for _, k := range releaseParams {
				old, _ := fromFields[k].(string)
				new, _ := toFields[k].(string)
				text = replaceVersion(text, old, new)
			}
			old, _ := fromFields["registry_time"].(string)
			new, _ := toFields["registry_time"].(string)
			text = replaceTime(text, old, new)
		}
	}
	var out schema.StrategyOneOf
	if err := json.Unmarshal([]byte(`{"`+kind+`":`+text+`}`), &out); err != nil {
		return nil, errors.Wrap(err, "rebuilding the seed")
	}
	if _, err := out.Strategy(); err != nil {
		return nil, err
	}
	return &out, nil
}

// replaceVersion replaces old with new where old stands as a whole version:
// not inside a longer one, as 1.2.3 is inside 11.2.3 or 1.2.3.4.
func replaceVersion(s, old, new string) string {
	if old == "" || new == "" || old == new {
		return s
	}
	isDigit := func(c byte) bool { return '0' <= c && c <= '9' }
	var b strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(old)
		before := i > 0 && (isDigit(s[i-1]) || s[i-1] == '.')
		after := end < len(s) && (isDigit(s[end]) || s[end] == '.' && end+1 < len(s) && isDigit(s[end+1]))
		b.WriteString(s[:i])
		if before || after {
			b.WriteString(old)
		} else {
			b.WriteString(new)
		}
		s = s[end:]
	}
}

// replaceTime replaces a registry time, which builds also spell to the
// second, as timewarp URLs do.
func replaceTime(s, old, new string) string {
	if old == "" || new == "" || old == new {
		return s
	}
	s = strings.ReplaceAll(s, old, new)
	if len(old) > 19 && len(new) > 19 {
		s = strings.ReplaceAll(s, old[:19]+"Z", new[:19]+"Z")
	}
	return s
}

// seedProposal relocates the seed's build to this version for the first
// proposal after the inference attempt, and returns nil on later calls.
func (a *defaultAgent) seedProposal() *schema.StrategyOneOf {
	seed := a.deps.Seed
	if seed == nil || seed.Strategy == nil || a.seedTried || len(a.iterHistory) == 0 {
		return nil
	}
	a.seedTried = true
	s, err := relocate(seed, a.deps.SeedOrigin, a.iterHistory[0].Strategy, a.t, a.loc)
	if err != nil {
		log.Printf("seed: relocating the build of version %s: %v", seed.Version, err)
		return nil
	}
	a.seedBuilt = true
	return s
}

// renderSeed shows the model the sibling's build.
func (a *defaultAgent) renderSeed(seed *schema.AgentSeed) []string {
	out := []string{"", "## Another version of this package", fmt.Sprintf("Version %s was reproduced.", seed.Version)}
	if a.seedBuilt {
		return append(out, "An earlier attempt in this session ran that build adapted to this version: the source commit, the version, and the toolchain versions and registry time inference chose were replaced with this version's. Settings that session chose for its release, such as pinned dependency versions, dates or toolchains, may not fit this one.")
	}
	if seed.Strategy == nil {
		return out
	}
	s, err := seed.Strategy.Strategy()
	if err != nil {
		return out
	}
	inst, err := s.GenerateFor(rebuild.Target{Ecosystem: a.t.Ecosystem, Package: a.t.Package, Version: seed.Version, Artifact: a.t.Artifact}, a.buildEnv())
	if err != nil {
		return out
	}
	return append(out, "Its build, for that version:", "```bash", AttemptScript(inst), "```")
}

// Bounds on an attempt's script as AttemptScript cuts it.
const scriptHeadBytes, scriptTailBytes = 1000, 500

// AttemptScript joins the deps phase, where a typed strategy pins its
// toolchain, and the build into one script cut to its head and tail.
func AttemptScript(inst rebuild.Instructions) string {
	script := inst.Build
	if inst.Deps != "" {
		script = inst.Deps + "\n" + inst.Build
	}
	out, _ := truncateEnds(script, scriptHeadBytes, scriptTailBytes)
	return out
}
