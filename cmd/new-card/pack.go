// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const packLimit = 1 << 20

var (
	idPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]+$`)
	executableHashTag = regexp.MustCompile(`-[a-fA-F0-9]{64}$`)
	artifactRef       = regexp.MustCompile(`^\{artifact\.([a-z0-9][a-z0-9._-]+)\}$`)
)

type packArtifact struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	HFRepo     string `json:"hf_repo"`
	HFRevision string `json:"hf_revision"`
	HFSubdir   string `json:"hf_subdir"`
}

// runtimePack is the subset of a model runtime pack that a card may display.
// Unknown fields are rejected because the pack schema forbids them.
type runtimePack struct {
	SchemaVersion  int             `json:"schema_version"`
	Kind           string          `json:"kind"`
	PackID         string          `json:"pack_id"`
	Revision       int             `json:"revision"`
	Priority       int             `json:"priority"`
	IssuedAt       string          `json:"issued_at"`
	ExpiresAt      string          `json:"expires_at"`
	Platforms      []string        `json:"platforms"`
	FallbackPolicy string          `json:"fallback_policy"`
	Signature      json.RawMessage `json:"signature"`
	Profile        struct {
		ProfileID     string   `json:"profile_id"`
		Aliases       []string `json:"aliases"`
		Tier          string   `json:"tier"`
		ModelID       string   `json:"model_id"`
		ContextTokens int64    `json:"context_tokens"`
		MinUnifiedGB  int64    `json:"min_unified_gb"`
	} `json:"profile"`
	Runtime struct {
		Executable    string   `json:"executable"`
		SHA256        string   `json:"sha256"`
		Args          []string `json:"args"`
		HealthPath    string   `json:"health_path"`
		WarmupSeconds int      `json:"warmup_seconds"`
	} `json:"runtime"`
	Artifacts []packArtifact `json:"artifacts"`
}

// packSource is what a card derives from a runtime pack.
type packSource struct {
	pack       runtimePack
	sha256     string
	executable string
	weights    packArtifact
	head       *packArtifact
	mtpDepth   int
	// targetManifest is the local file the engine verifies the weights against.
	targetManifest *packArtifact
}

func readPack(ctx context.Context, f *fetcher, source string) ([]byte, error) {
	if strings.HasPrefix(source, "https://") {
		data, _, err := f.get(ctx, source, "application/json", packLimit)
		return data, err
	}
	if strings.Contains(source, "://") {
		return nil, fmt.Errorf("runtime pack URL must use HTTPS")
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if info.Size() > packLimit {
		return nil, fmt.Errorf("runtime pack exceeds %d bytes", packLimit)
	}
	return os.ReadFile(source)
}

func parsePack(data []byte) (*packSource, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var p runtimePack
	if err := decoder.Decode(&p); err != nil {
		return nil, fmt.Errorf("runtime pack: %w", err)
	}
	if p.SchemaVersion != 1 || p.Kind != "amesh.model-runtime-pack" {
		return nil, fmt.Errorf("runtime pack: unsupported schema_version or kind")
	}
	if !idPattern.MatchString(p.PackID) || !idPattern.MatchString(p.Profile.ProfileID) || len(p.Profile.ProfileID) > 128 {
		return nil, fmt.Errorf("runtime pack: invalid pack_id or profile_id")
	}
	if p.Profile.ContextTokens < 1 {
		return nil, fmt.Errorf("runtime pack: profile context_tokens must be positive")
	}
	if _, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil {
		return nil, fmt.Errorf("runtime pack: expires_at: %w", err)
	}
	p.Runtime.SHA256 = strings.ToLower(p.Runtime.SHA256)
	if !sha256Pattern.MatchString(p.Runtime.SHA256) {
		return nil, fmt.Errorf("runtime pack: runtime sha256 is invalid")
	}
	sum := sha256.Sum256(data)
	src := &packSource{pack: p, sha256: hex.EncodeToString(sum[:])}
	src.executable = executableHashTag.ReplaceAllString(path.Base(p.Runtime.Executable), "")
	artifacts := make(map[string]packArtifact)
	for _, a := range p.Artifacts {
		if _, found := artifacts[a.Name]; found {
			return nil, fmt.Errorf("runtime pack: duplicate artifact %q", a.Name)
		}
		artifacts[a.Name] = a
	}
	args := p.Runtime.Args
	argValue := func(flag string) (string, bool) {
		for i, arg := range args {
			if arg == flag && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	for _, arg := range args {
		switch arg {
		case "--model-draft", "-md", "--draft-model", "--spec-model":
			return nil, fmt.Errorf("runtime pack: speculation flag %s is not supported by this generator", arg)
		}
	}
	weightsName := "weights"
	if value, found := argValue("--model"); found {
		if m := artifactRef.FindStringSubmatch(value); m != nil {
			weightsName = m[1]
		}
	}
	weights, found := artifacts[weightsName]
	if !found || weights.Kind != "huggingface_snapshot" {
		return nil, fmt.Errorf("runtime pack: weights artifact %q must be a huggingface_snapshot", weightsName)
	}
	weights.HFRevision = strings.ToLower(weights.HFRevision)
	if !validRepo(weights.HFRepo) || !commitPattern.MatchString(weights.HFRevision) ||
		(weights.HFSubdir != "" && !validRelativePath(weights.HFSubdir)) {
		return nil, fmt.Errorf("runtime pack: weights artifact has an invalid repository, revision or subdirectory")
	}
	src.weights = weights
	if value, found := argValue("--mtp-head"); found {
		m := artifactRef.FindStringSubmatch(value)
		if m == nil {
			return nil, fmt.Errorf("runtime pack: --mtp-head must name a pinned artifact")
		}
		head, found := artifacts[m[1]]
		if !found {
			return nil, fmt.Errorf("runtime pack: --mtp-head names unknown artifact %q", m[1])
		}
		head.SHA256 = strings.ToLower(head.SHA256)
		head.HFRevision = strings.ToLower(head.HFRevision)
		if head.Kind == "local_file" && !sha256Pattern.MatchString(head.SHA256) {
			return nil, fmt.Errorf("runtime pack: MTP head %q has no valid sha256", head.Name)
		}
		src.head = &head
		depth, found := argValue("--mtp-max-depth")
		if !found {
			return nil, fmt.Errorf("runtime pack: --mtp-head without --mtp-max-depth leaves the offered depth unknown")
		}
		src.mtpDepth, _ = strconv.Atoi(depth)
		if src.mtpDepth < 1 || src.mtpDepth > 64 {
			return nil, fmt.Errorf("runtime pack: --mtp-max-depth must be between 1 and 64")
		}
	}
	if value, found := argValue("--target-manifest"); found {
		m := artifactRef.FindStringSubmatch(value)
		if m == nil {
			return nil, fmt.Errorf("runtime pack: --target-manifest must name a pinned artifact")
		}
		manifest, found := artifacts[m[1]]
		manifest.SHA256 = strings.ToLower(manifest.SHA256)
		if !found || manifest.Kind != "local_file" || !sha256Pattern.MatchString(manifest.SHA256) {
			return nil, fmt.Errorf("runtime pack: --target-manifest must name a local_file artifact with a sha256")
		}
		src.targetManifest = &manifest
	}
	return src, nil
}

// launchArgv renders the pack command with card placeholders instead of local paths.
func (src *packSource) launchArgv(targetPlaceholder string) []string {
	argv := []string{src.executable}
	for _, arg := range src.pack.Runtime.Args {
		switch {
		case arg == "{artifact."+src.weights.Name+"}":
			arg = targetPlaceholder
		case src.head != nil && arg == "{artifact."+src.head.Name+"}" && src.head.Kind == "local_file":
			arg = "{head_file}"
		case src.head != nil && arg == "{artifact."+src.head.Name+"}":
			arg = "{head_directory}"
		case src.targetManifest != nil && arg == "{artifact."+src.targetManifest.Name+"}":
			arg = "{target_manifest_file}"
		}
		argv = append(argv, arg)
	}
	return argv
}
