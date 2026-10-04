// SPDX-License-Identifier: Apache-2.0
// check-cards verifies card references and hash scopes after JSON Schema validation.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

type check struct {
	State       string   `json:"state"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type digest struct {
	SHA256    string   `json:"sha256"`
	Algorithm string   `json:"algorithm"`
	Inventory []string `json:"inventory"`
}

type file struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type artifact struct {
	Files []file `json:"files"`
	Tree  digest `json:"tree_digest"`
}

type recipe struct {
	ID              string           `json:"id"`
	Role            string           `json:"role"`
	AmeshProfileIDs []string         `json:"amesh_profile_ids"`
	Validation      map[string]check `json:"validation"`
	Capabilities    map[string]check `json:"capabilities"`
	BenchmarkIDs    []string         `json:"benchmark_ids"`
	ContextTokens   *int             `json:"context_tokens"`
	MinMemoryGiB    *int             `json:"min_unified_memory_gib"`
	Engine          struct {
		Build *struct {
			EvidenceIDs []string `json:"evidence_ids"`
		} `json:"build"`
	} `json:"engine"`
	Launch struct {
		Availability string          `json:"availability"`
		RuntimePack  json.RawMessage `json:"runtime_pack"`
	} `json:"launch"`
	Speculation struct {
		Method       string    `json:"method"`
		Head         *artifact `json:"head"`
		Drafter      *artifact `json:"drafter"`
		MaxOffered   int       `json:"max_offered_draft_tokens"`
		MaxEffective int       `json:"max_effective_draft_tokens"`
		BlockTokens  int       `json:"block_tokens"`
	} `json:"speculation"`
}

type evidence struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	WorkloadID  string          `json:"workload_id"`
	Hardware    json.RawMessage `json:"hardware"`
	Measurement json.RawMessage `json:"measurement"`
	Correctness *struct {
		EmittedTokens int `json:"emitted_tokens"`
		MatchedTokens int `json:"matched_tokens"`
		ReferenceRows int `json:"reference_rows"`
		DeclaredRows  int `json:"declared_rows"`
		ResidualCount int `json:"residual_count"`
	} `json:"correctness"`
	ValidatedChecks       []string `json:"validated_checks"`
	ValidatedCapabilities []string `json:"validated_capabilities"`
	RecipeIDs             []string `json:"recipe_ids"`
}

type card struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ModelType string  `json:"model_type"`
	Display   display `json:"display"`
	Selection struct {
		RequiredChecks       []string `json:"required_checks"`
		Metric               string   `json:"metric"`
		DefaultWorkloadID    string   `json:"default_workload_id"`
		DefaultWorkloadLabel string   `json:"default_workload_label"`
	} `json:"selection"`
	Recipes  []recipe   `json:"recipes"`
	Evidence []evidence `json:"evidence"`
}

type display struct {
	TypeSymbol        string            `json:"type_symbol"`
	CapabilitySymbols map[string]string `json:"capability_symbols"`
}

type indexEntry struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Path            string   `json:"path"`
	SHA256          string   `json:"sha256"`
	ModelType       string   `json:"model_type"`
	Display         display  `json:"display"`
	AmeshProfileIDs []string `json:"amesh_profile_ids"`
}

type index struct {
	SchemaRef     string       `json:"$schema"`
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	Authority     string       `json:"authority"`
	GeneratedAt   string       `json:"generated_at"`
	Repository    string       `json:"repository"`
	Cards         []indexEntry `json:"cards"`
}

func verifyIndex(entries []indexEntry) error {
	data, err := os.ReadFile("index.json")
	if err != nil {
		return err
	}
	var published index
	if err := json.Unmarshal(data, &published); err != nil {
		return err
	}
	if published.SchemaVersion != 1 || published.Kind != "amesh.model-card-index" ||
		published.Authority != "display_only" || published.Repository != "https://github.com/byollm/modelcards" {
		return fmt.Errorf("index identity or display authority is invalid")
	}
	if _, err := time.Parse(time.RFC3339, published.GeneratedAt); err != nil {
		return fmt.Errorf("index generation time: %w", err)
	}
	want, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(published.Cards)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, actual) {
		return fmt.Errorf("index card metadata or hashes are stale; regenerate with -write-index")
	}
	return nil
}

func verifyArtifact(recipeID, name string, a *artifact) error {
	if a == nil || len(a.Files) == 0 {
		return fmt.Errorf("recipe %s needs a pinned %s file inventory", recipeID, name)
	}
	files := make(map[string]file)
	for _, f := range a.Files {
		if _, found := files[f.Path]; found {
			return fmt.Errorf("recipe %s has duplicate %s path %q", recipeID, name, f.Path)
		}
		if f.Bytes < 0 {
			return fmt.Errorf("recipe %s %s has negative file size", recipeID, name)
		}
		files[f.Path] = f
	}
	if len(files) != len(a.Tree.Inventory) {
		return fmt.Errorf("recipe %s %s inventory does not match files", recipeID, name)
	}
	paths := append([]string(nil), a.Tree.Inventory...)
	sort.Strings(paths)
	hasher := sha256.New()
	for i, path := range paths {
		f, found := files[path]
		if !found || (i > 0 && paths[i-1] == path) {
			return fmt.Errorf("recipe %s %s inventory has unknown or duplicate file %q", recipeID, name, path)
		}
		fileHash, err := hex.DecodeString(f.SHA256)
		if err != nil || len(fileHash) != sha256.Size {
			return fmt.Errorf("recipe %s %s has invalid file digest", recipeID, name)
		}
		switch a.Tree.Algorithm {
		case "sha256_of_sorted_file_sha256_two_spaces_relative_path_lf":
			fmt.Fprintf(hasher, "%s  %s\n", f.SHA256, path)
		case "sha256_of_sorted_relative_path_nul_raw_file_sha256_nul":
			hasher.Write([]byte(path))
			hasher.Write([]byte{0})
			hasher.Write(fileHash)
			hasher.Write([]byte{0})
		case "sha256_of_sorted_relative_path_nul_decimal_bytes_nul_file_sha256_lf":
			fmt.Fprintf(hasher, "%s%c%d%c%s\n", path, 0, f.Bytes, 0, f.SHA256)
		default:
			return fmt.Errorf("recipe %s %s digest algorithm is unsupported", recipeID, name)
		}
	}
	if hex.EncodeToString(hasher.Sum(nil)) != a.Tree.SHA256 {
		return fmt.Errorf("recipe %s %s tree digest disagrees with its file hashes and sizes", recipeID, name)
	}
	return nil
}

func verify(c card, profiles map[string]string) error {
	recipeIDs := make(map[string]bool)
	for _, r := range c.Recipes {
		if recipeIDs[r.ID] {
			return fmt.Errorf("duplicate recipe %q", r.ID)
		}
		recipeIDs[r.ID] = true
	}
	records := make(map[string]evidence)
	for _, e := range c.Evidence {
		if _, found := records[e.ID]; found {
			return fmt.Errorf("duplicate evidence %q", e.ID)
		}
		if e.Kind == "local_benchmark" && (len(e.Hardware) == 0 || len(e.Measurement) == 0) {
			return fmt.Errorf("local benchmark %q needs hardware and measurement", e.ID)
		}
		if e.State == "passed" && e.Correctness != nil {
			c := e.Correctness
			if c.EmittedTokens != c.MatchedTokens || c.ResidualCount != 0 ||
				c.ReferenceRows < c.EmittedTokens || c.DeclaredRows < c.EmittedTokens {
				return fmt.Errorf("passed correctness evidence %q has mismatches or incomplete rows", e.ID)
			}
		}
		for _, id := range e.RecipeIDs {
			if !recipeIDs[id] {
				return fmt.Errorf("evidence %q names unknown recipe %q", e.ID, id)
			}
		}
		records[e.ID] = e
	}
	if c.Selection.DefaultWorkloadID == "" && c.Selection.DefaultWorkloadLabel != "" {
		return fmt.Errorf("default workload label needs a default workload ID")
	}
	if c.Selection.DefaultWorkloadID != "" {
		found := false
		for _, e := range records {
			if e.WorkloadID != c.Selection.DefaultWorkloadID || e.Kind != "local_benchmark" || e.State != "passed" {
				continue
			}
			var measurement struct {
				Metric string   `json:"metric"`
				Value  *float64 `json:"value"`
			}
			if err := json.Unmarshal(e.Measurement, &measurement); err != nil ||
				measurement.Metric != c.Selection.Metric || measurement.Value == nil || *measurement.Value <= 0 {
				continue
			}
			for _, recipe := range c.Recipes {
				for _, id := range recipe.BenchmarkIDs {
					found = found || id == e.ID
				}
			}
		}
		if !found {
			return fmt.Errorf("default workload %q needs a referenced passed local benchmark for the selection metric", c.Selection.DefaultWorkloadID)
		}
	}
	for _, r := range c.Recipes {
		comparisonOnly := r.Launch.Availability == "comparison_only"
		if comparisonOnly && (r.Role != "candidate" || len(r.AmeshProfileIDs) != 0 || len(r.Launch.RuntimePack) != 0) {
			return fmt.Errorf("comparison-only recipe %s must be an unmapped candidate without a runtime pack", r.ID)
		}
		if !comparisonOnly && (r.ContextTokens == nil || r.MinMemoryGiB == nil) {
			return fmt.Errorf("recipe %s needs known context and memory limits", r.ID)
		}
		if (r.ContextTokens != nil && *r.ContextTokens <= 0) || (r.MinMemoryGiB != nil && *r.MinMemoryGiB <= 0) {
			return fmt.Errorf("recipe %s context and memory limits must be positive", r.ID)
		}
		if r.Engine.Build != nil {
			for _, id := range r.Engine.Build.EvidenceIDs {
				e, found := records[id]
				if !found || e.Kind != "local_functional" || !slices.Contains(e.RecipeIDs, r.ID) {
					return fmt.Errorf("recipe %s has invalid scoped build evidence %q", r.ID, id)
				}
			}
		}
		for _, profile := range r.AmeshProfileIDs {
			if prior, found := profiles[profile]; found {
				return fmt.Errorf("profile %q maps to both %s and %s", profile, prior, r.ID)
			}
			profiles[profile] = r.ID
		}
		for _, id := range r.BenchmarkIDs {
			e, found := records[id]
			if !found || e.Kind == "local_functional" || len(e.Measurement) == 0 || !slices.Contains(e.RecipeIDs, r.ID) {
				return fmt.Errorf("recipe %s has invalid benchmark reference %q", r.ID, id)
			}
		}
		for group, checks := range map[string]map[string]check{"validation": r.Validation, "capabilities": r.Capabilities} {
			for name, ch := range checks {
				if ch.State == "passed" && len(ch.EvidenceIDs) == 0 {
					return fmt.Errorf("%s %s.%s passed without evidence", r.ID, group, name)
				}
				coveredLocally := false
				for _, id := range ch.EvidenceIDs {
					e, found := records[id]
					if !found || (ch.State == "passed" && e.State != "passed") {
						return fmt.Errorf("%s %s.%s has invalid evidence %q", r.ID, group, name, id)
					}
					coverage := e.ValidatedChecks
					if group == "capabilities" {
						coverage = e.ValidatedCapabilities
					}
					coveredLocally = coveredLocally || (e.Kind != "upstream_benchmark" &&
						slices.Contains(e.RecipeIDs, r.ID) && slices.Contains(coverage, name))
				}
				if ch.State == "passed" && !coveredLocally {
					return fmt.Errorf("%s %s.%s needs local evidence covering that check; upstream and build-only evidence cannot validate serving", r.ID, group, name)
				}
			}
		}
		if r.Role == "validated" || r.Role == "serial_fallback" {
			for _, name := range c.Selection.RequiredChecks {
				if r.Validation[name].State != "passed" {
					return fmt.Errorf("recipe %s lacks required serving check %s", r.ID, name)
				}
			}
		}
		if r.Role == "serial_fallback" && r.Speculation.Method != "none" {
			return fmt.Errorf("serial fallback %s enables speculation", r.ID)
		}
		if r.Speculation.Method == "none" {
			if r.Speculation.Head != nil || r.Speculation.Drafter != nil {
				return fmt.Errorf("serial recipe %s includes a speculative artifact", r.ID)
			}
			continue
		}
		if r.Speculation.MaxOffered <= 0 || r.Speculation.MaxEffective > r.Speculation.MaxOffered {
			return fmt.Errorf("recipe %s has invalid offered or effective speculative depth", r.ID)
		}
		switch r.Speculation.Method {
		case "native_mtp":
			if r.Speculation.Drafter != nil {
				return fmt.Errorf("native MTP recipe %s must pin a head, not a separate drafter", r.ID)
			}
			if err := verifyArtifact(r.ID, "head", r.Speculation.Head); err != nil {
				return err
			}
		case "dflash2", "draft_model":
			if r.Speculation.Head != nil {
				return fmt.Errorf("recipe %s must pin its separate drafter", r.ID)
			}
			if err := verifyArtifact(r.ID, "drafter", r.Speculation.Drafter); err != nil {
				return err
			}
			if r.Speculation.BlockTokens != 0 && r.Speculation.BlockTokens != r.Speculation.MaxOffered+1 {
				return fmt.Errorf("recipe %s block must include one anchor plus offered drafts", r.ID)
			}
		default:
			return fmt.Errorf("recipe %s uses unsupported speculation method", r.ID)
		}
	}
	return nil
}

func main() {
	writeIndex := flag.Bool("write-index", false, "regenerate the display index after validating all cards")
	flag.Parse()
	paths := flag.Args()
	allCards := len(paths) == 0
	if *writeIndex && !allCards {
		fmt.Fprintln(os.Stderr, "-write-index requires the complete default card set")
		os.Exit(1)
	}
	if len(paths) == 0 {
		var err error
		paths, err = filepath.Glob("cards/*.json")
		if err != nil || len(paths) == 0 {
			fmt.Fprintln(os.Stderr, "no cards found")
			os.Exit(1)
		}
	}
	profiles := make(map[string]string)
	cards := make(map[string]bool)
	entries := make([]indexEntry, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		var c card
		if err == nil {
			err = json.Unmarshal(data, &c)
		}
		if err == nil && cards[c.ID] {
			err = fmt.Errorf("duplicate card %q", c.ID)
		}
		if err == nil {
			cards[c.ID] = true
			err = verify(c, profiles)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		profileIDs := make([]string, 0)
		for _, recipe := range c.Recipes {
			profileIDs = append(profileIDs, recipe.AmeshProfileIDs...)
		}
		sort.Strings(profileIDs)
		cardHash := sha256.Sum256(data)
		entries = append(entries, indexEntry{
			ID: c.ID, Name: c.Name, Path: filepath.ToSlash(path),
			SHA256: hex.EncodeToString(cardHash[:]), ModelType: c.ModelType,
			Display: c.Display, AmeshProfileIDs: profileIDs,
		})
		fmt.Printf("%s: references, serving gates, and speculative artifact hash scopes valid\n", path)
	}
	if *writeIndex {
		feed := index{
			SchemaRef: "schemas/model-card-index.schema.json", SchemaVersion: 1,
			Kind: "amesh.model-card-index", Authority: "display_only",
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Repository:  "https://github.com/byollm/modelcards", Cards: entries,
		}
		data, err := json.MarshalIndent(feed, "", "  ")
		if err == nil {
			err = os.WriteFile("index.json", append(data, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if allCards {
		if err := verifyIndex(entries); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("index.json: exact card hashes and display metadata valid")
	}
}
