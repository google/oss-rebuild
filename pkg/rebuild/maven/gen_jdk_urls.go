// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// This program generates jdk_urls.go by scanning Adoptium and AdoptOpenJDK
// GitHub repositories for JDK releases.
//
// Prerequisites:
//   - GitHub CLI (gh) must be installed and authenticated
//   - Run: go generate ./pkg/rebuild/maven
//
// The program:
//  1. Queries GitHub GraphQL API via gh CLI
//  2. Scans Adoptium (JDK 16+ and earlier LTS) and AdoptOpenJDK (JDK 8-16) repositories
//  3. Filters for stable releases (no prereleases, beta, EA, RC)
//  4. Selects Linux x64 HotSpot tar.gz binaries
//  5. Generates sorted map of all patch versions
//
// Version formats:
//   - JDK 8: Legacy "8u292" format
//   - JDK 9+: Semantic versioning "11.0.1"
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/semver"
)

// Configuration constants
const (
	MinMajorVersion    = 8
	MaxConsecutiveMiss = 3 // Stop after failing to find repos for 3 consecutive major versions
)

// Release represents a parsed GitHub release
type Release struct {
	TagName       string
	IsPrerelease  bool
	ReleaseAssets []ReleaseAsset
}

// ReleaseAsset represents an individual asset within a release
type ReleaseAsset struct {
	Name        string
	DownloadURL string
}

func main() {
	log.SetFlags(0) // No timestamp prefix for cleaner output
	// Validate gh CLI exists
	if _, err := exec.LookPath("gh"); err != nil {
		log.Fatal("ERROR: gh CLI not found. Install from https://cli.github.com/")
	}
	// Compile asset regex once
	assetRegex := regexp.MustCompile(`OpenJDK\d+U-jdk_x64_linux_hotspot_.*\.tar\.gz$`)
	urlMap := make(map[string]string)
	major := MinMajorVersion
	consecutiveMisses := 0
	log.Printf("Starting JDK URL generation...")
	log.Printf("Scanning repositories for JDK versions %d and above", MinMajorVersion)
	for {
		if consecutiveMisses >= MaxConsecutiveMiss {
			log.Printf("Stopping: No releases found for %d consecutive major versions", MaxConsecutiveMiss)
			break
		}
		repos := getReposForVersion(major)
		foundAnyForMajor := false
		log.Printf("Checking JDK %d...", major)
		log.Printf("  Repositories to scan: %v", repos)
		for _, repoPath := range repos {
			log.Printf("  Querying repository: %s", repoPath)
			releases, err := fetchReleases(repoPath)
			if err != nil {
				log.Printf("  Warning: Failed to fetch %s: %v", repoPath, err)
				continue
			}
			log.Printf("  Found %d releases in %s", len(releases), repoPath)
			if len(releases) > 0 {
				foundAnyForMajor = true
			}
			for _, rel := range releases {
				// Skip prereleases
				if rel.IsPrerelease {
					log.Printf("    Skipping prerelease: %s", rel.TagName)
					continue
				}
				// Skip beta/ea/rc tags
				if isUnstableTag(rel.TagName) {
					log.Printf("    Skipping unstable tag: %s", rel.TagName)
					continue
				}
				// Parse version from tag
				key, valid := parseTag(rel.TagName)
				if !valid {
					log.Printf("    Warning: Could not parse version from tag '%s'", rel.TagName)
					continue
				}
				// Find matching asset
				var downloadURL string
				for _, asset := range rel.ReleaseAssets {
					if assetRegex.MatchString(asset.Name) {
						downloadURL = asset.DownloadURL
						break
					}
				}
				if downloadURL == "" {
					log.Printf("    Warning: No matching Linux x64 asset for release '%s'", rel.TagName)
					continue
				}
				// Store (prefer first occurrence - API returns newest first)
				if _, exists := urlMap[key]; !exists {
					log.Printf("    Selected: %s -> %s", key, downloadURL)
					urlMap[key] = downloadURL
				} else {
					log.Printf("    Skipping duplicate version: %s", key)
				}
			}
		}
		if foundAnyForMajor {
			consecutiveMisses = 0
		} else {
			consecutiveMisses++
			log.Printf("  No releases found for JDK %d (miss %d/%d)", major, consecutiveMisses, MaxConsecutiveMiss)
		}
		major++
	}
	// Add major version aliases that point to the latest patch version
	// This allows using "11" instead of "11.0.11"
	addMajorVersionAliases(urlMap)
	log.Printf("Generation complete: Found %d JDK versions", len(urlMap))
	log.Printf("Writing to jdk_urls.go...")
	if err := writeGeneratedFile(urlMap); err != nil {
		log.Fatalf("Failed to write output: %v", err)
	}
	log.Printf("Done!")
}

// addMajorVersionAliases adds entries for major versions (e.g., "11", "17")
// that point to the latest patch version for that major.
// This allows fallback JDK inference to work with just major version numbers.
func addMajorVersionAliases(urlMap map[string]string) {
	log.Printf("Adding major version aliases...")
	// Group versions by major version
	majorVersions := make(map[string][]string) // major -> list of full versions
	for version := range urlMap {
		major := extractMajorVersion(version)
		if major != "" && major != version { // Skip if already a major-only version
			majorVersions[major] = append(majorVersions[major], version)
		}
	}
	// For each major version, find the latest patch and add an alias
	for major, versions := range majorVersions {
		if _, exists := urlMap[major]; exists {
			continue // Already have this major version
		}
		// Sort to find the latest
		sort.Slice(versions, func(i, j int) bool {
			return compareVersions(versions[i], versions[j])
		})
		latest := versions[len(versions)-1]
		urlMap[major] = urlMap[latest]
		log.Printf("  Added alias: %s -> %s", major, latest)
	}
}

// extractMajorVersion returns the major version number from a version string.
// "11.0.11" -> "11", "8u292" -> "8", "17" -> "17"
func extractMajorVersion(version string) string {
	if strings.Contains(version, "u") {
		// Legacy format: 8u292
		parts := strings.Split(version, "u")
		if len(parts) >= 1 {
			return parts[0]
		}
	}
	// Semver format: 11.0.11
	parts := strings.Split(version, ".")
	if len(parts) >= 1 {
		return parts[0]
	}
	return version
}

// getReposForVersion returns the candidate repositories for a given major version.
func getReposForVersion(major int) []string {
	var repos []string
	// JDK 16+: Adoptium (Eclipse Temurin) is the standard
	if major >= 16 || major == 11 || major == 8 {
		repos = append(repos, fmt.Sprintf("adoptium/temurin%d-binaries", major))
	}
	// JDK <= 16: Check the old AdoptOpenJDK org
	if major <= 16 {
		repos = append(repos,
			fmt.Sprintf("AdoptOpenJDK/openjdk%d-binaries", major),
		)
	}
	return repos
}

// parseTag extracts a clean version key from a release tag.
// Handles both legacy (jdk8u292-b10) and modern (jdk-11.0.1+13) formats.
func parseTag(tag string) (string, bool) {
	// Legacy format: jdk8u292-b10 or jdk8u292-b10.1
	legacyRegex := regexp.MustCompile(`^jdk(\d+)u(\d+)`)
	if matches := legacyRegex.FindStringSubmatch(tag); matches != nil {
		return fmt.Sprintf("%su%s", matches[1], matches[2]), true
	}
	// Modern SemVer format: jdk-11.0.1+13 or jdk-17.0.2
	modernRegex := regexp.MustCompile(`^jdk-?(\d+\.\d+\.\d+)`)
	if matches := modernRegex.FindStringSubmatch(tag); matches != nil {
		return matches[1], true
	}
	// Also handle GA releases like jdk-11+28
	gaRegex := regexp.MustCompile(`^jdk-?(\d+)\+`)
	if matches := gaRegex.FindStringSubmatch(tag); matches != nil {
		return matches[1], true
	}
	return "", false
}

// isUnstableTag checks if a tag indicates a prerelease/beta/EA version.
func isUnstableTag(tag string) bool {
	t := strings.ToLower(tag)
	return strings.Contains(t, "beta") || strings.Contains(t, "-ea") || strings.Contains(t, "_ea") || strings.Contains(t, "rc")
}

// fetchReleases queries GitHub for all releases of the given repository.
// Uses a two-step approach:
// 1. GraphQL to list all release tags efficiently
// 2. REST API to fetch full asset lists for non-prerelease tags
func fetchReleases(repoPath string) ([]Release, error) {
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo path: %s", repoPath)
	}
	owner, repo := parts[0], parts[1]
	// Step 1: Get list of all release tags via GraphQL
	var releaseTags []struct {
		TagName      string
		IsPrerelease bool
	}
	cursor := ""
	hasNext := true
	for hasNext {
		var query string
		if cursor == "" {
			query = fmt.Sprintf(`{
				repository(owner: %q, name: %q) {
					releases(first: 100, orderBy: {field: CREATED_AT, direction: DESC}) {
						nodes { tagName isPrerelease }
						pageInfo { hasNextPage endCursor }
					}
				}
			}`, owner, repo)
		} else {
			query = fmt.Sprintf(`{
				repository(owner: %q, name: %q) {
					releases(first: 100, after: %q, orderBy: {field: CREATED_AT, direction: DESC}) {
						nodes { tagName isPrerelease }
						pageInfo { hasNextPage endCursor }
					}
				}
			}`, owner, repo, cursor)
		}
		cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("gh command failed: %w", err)
		}
		var resp struct {
			Data struct {
				Repository struct {
					Releases struct {
						Nodes []struct {
							TagName      string `json:"tagName"`
							IsPrerelease bool   `json:"isPrerelease"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"releases"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("GraphQL error: %s", resp.Errors[0].Message)
		}
		for _, node := range resp.Data.Repository.Releases.Nodes {
			releaseTags = append(releaseTags, struct {
				TagName      string
				IsPrerelease bool
			}{node.TagName, node.IsPrerelease})
		}
		hasNext = resp.Data.Repository.Releases.PageInfo.HasNextPage
		cursor = resp.Data.Repository.Releases.PageInfo.EndCursor
	}
	// Step 2: For non-prerelease/stable releases, fetch full asset list via REST API
	var allReleases []Release
	for _, tag := range releaseTags {
		// Skip prereleases and unstable tags early to avoid unnecessary API calls
		if tag.IsPrerelease || isUnstableTag(tag.TagName) {
			allReleases = append(allReleases, Release{
				TagName:      tag.TagName,
				IsPrerelease: true, // Mark as prerelease so main loop skips it
			})
			continue
		}
		// Fetch full release details with all assets via REST API
		apiPath := fmt.Sprintf("/repos/%s/%s/releases/tags/%s", owner, repo, tag.TagName)
		cmd := exec.Command("gh", "api", apiPath)
		out, err := cmd.Output()
		if err != nil {
			log.Printf("    Warning: Could not fetch release %s: %v", tag.TagName, err)
			continue
		}
		var restResp struct {
			TagName      string `json:"tag_name"`
			IsPrerelease bool   `json:"prerelease"`
			Assets       []struct {
				Name               string `json:"name"`
				BrowserDownloadURL string `json:"browser_download_url"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(out, &restResp); err != nil {
			log.Printf("    Warning: Could not parse release %s: %v", tag.TagName, err)
			continue
		}
		rel := Release{
			TagName:      restResp.TagName,
			IsPrerelease: restResp.IsPrerelease,
		}
		for _, asset := range restResp.Assets {
			rel.ReleaseAssets = append(rel.ReleaseAssets, ReleaseAsset{
				Name:        asset.Name,
				DownloadURL: asset.BrowserDownloadURL,
			})
		}
		allReleases = append(allReleases, rel)
	}
	return allReleases, nil
}

// writeGeneratedFile generates the Go source file and writes it atomically.
func writeGeneratedFile(urlMap map[string]string) error {
	var buf bytes.Buffer
	generateCode(&buf, urlMap)
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format generated code: %w", err)
	}
	tmpFile := "jdk_urls.go.tmp"
	if err := os.WriteFile(tmpFile, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmpFile, "jdk_urls.go"); err != nil {
		os.Remove(tmpFile) // Clean up on failure
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// generateCode writes the Go source code to the given writer.
func generateCode(w io.Writer, urlMap map[string]string) {
	fmt.Fprintln(w, "// Code generated by: go generate ./pkg/rebuild/maven")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "package maven")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "// JDKDownloadURLs maps Java version strings to their download URLs")
	fmt.Fprintln(w, "// from Adoptium (Eclipse Temurin) and AdoptOpenJDK projects.")
	fmt.Fprintln(w, "var JDKDownloadURLs = map[string]string{")
	// Sort keys by version
	keys := make([]string, 0, len(urlMap))
	for k := range urlMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareVersions(keys[i], keys[j])
	})
	for _, k := range keys {
		fmt.Fprintf(w, "\t%q: %q,\n", k, urlMap[k])
	}
	fmt.Fprintln(w, "}")
}

// compareVersions compares two version strings for sorting.
// Handles both legacy (8u212) and modern (11.0.1) formats.
// Returns true if v1 < v2.
func compareVersions(v1, v2 string) bool {
	// Convert to semver-compatible format for comparison
	s1 := toSemverString(v1)
	s2 := toSemverString(v2)
	return semver.Cmp(s1, s2) < 0
}

// toSemverString converts a JDK version string to semver format.
// "8u212" -> "8.0.212"
// "11.0.1" -> "11.0.1"
// "17" -> "17.0.0"
func toSemverString(v string) string {
	// Handle legacy format: 8u212 -> 8.0.212
	if strings.Contains(v, "u") {
		parts := strings.Split(v, "u")
		if len(parts) == 2 {
			return fmt.Sprintf("%s.0.%s", parts[0], parts[1])
		}
	}
	// Handle major-only versions: 17 -> 17.0.0
	if !strings.Contains(v, ".") {
		return v + ".0.0"
	}
	// Handle semver with only major.minor: 11.0 -> 11.0.0
	parts := strings.Split(v, ".")
	if len(parts) == 2 {
		return v + ".0"
	}
	return v
}
