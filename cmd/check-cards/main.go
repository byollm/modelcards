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
}

type recipe struct {
	ID              string           `json:"id"`
	Role            string           `json:"role"`
	AmeshProfileIDs []string         `json:"amesh_profile_ids"`
	Validation      map[string]check `json:"validation"`
	Capabilities    map[string]check `json:"capabilities"`
	BenchmarkIDs    []string         `json:"benchmark_ids"`
	Speculation     struct {
		Method string `json:"method"`
		Head   struct {
			Files []file `json:"files"`
			Tree  digest `json:"tree_digest"`
		} `json:"head"`
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

func verify(c card, profiles map[string]string) error {
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
	ids := make(map[string]bool)
	for _, r := range c.Recipes {
		if ids[r.ID] {
			return fmt.Errorf("duplicate recipe %q", r.ID)
		}
		ids[r.ID] = true
		for _, profile := range r.AmeshProfileIDs {
			if prior, found := profiles[profile]; found {
				return fmt.Errorf("profile %q maps to both %s and %s", profile, prior, r.ID)
			}
			profiles[profile] = r.ID
		}
		for _, id := range r.BenchmarkIDs {
			e, found := records[id]
			if !found || e.Kind == "local_functional" || len(e.Measurement) == 0 {
				return fmt.Errorf("recipe %s has invalid benchmark reference %q", r.ID, id)
			}
		}
		for group, checks := range map[string]map[string]check{"validation": r.Validation, "capabilities": r.Capabilities} {
			for name, ch := range checks {
				if ch.State == "passed" && len(ch.EvidenceIDs) == 0 {
					return fmt.Errorf("%s %s.%s passed without evidence", r.ID, group, name)
				}
				local := false
				for _, id := range ch.EvidenceIDs {
					e, found := records[id]
					if !found || (ch.State == "passed" && e.State != "passed") {
						return fmt.Errorf("%s %s.%s has invalid evidence %q", r.ID, group, name, id)
					}
					local = local || e.Kind != "upstream_benchmark"
				}
				if ch.State == "passed" && !local {
					return fmt.Errorf("%s %s.%s cannot use upstream benchmarking as local serving validation", r.ID, group, name)
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
			continue
		}
		head := r.Speculation.Head
		files := make(map[string]string)
		for _, f := range head.Files {
			if _, found := files[f.Path]; found {
				return fmt.Errorf("recipe %s has duplicate head path %q", r.ID, f.Path)
			}
			files[f.Path] = f.SHA256
		}
		if len(files) != len(head.Tree.Inventory) {
			return fmt.Errorf("recipe %s head inventory does not match files", r.ID)
		}
		paths := append([]string(nil), head.Tree.Inventory...)
		sort.Strings(paths)
		hasher := sha256.New()
		for _, path := range paths {
			fileHash, found := files[path]
			if !found {
				return fmt.Errorf("recipe %s head inventory has unknown file %q", r.ID, path)
			}
			switch head.Tree.Algorithm {
			case "sha256_of_sorted_file_sha256_two_spaces_relative_path_lf":
				fmt.Fprintf(hasher, "%s  %s\n", fileHash, path)
			case "sha256_of_sorted_relative_path_nul_raw_file_sha256_nul":
				bytes, err := hex.DecodeString(fileHash)
				if err != nil || len(bytes) != sha256.Size {
					return fmt.Errorf("recipe %s head has invalid file digest", r.ID)
				}
				hasher.Write([]byte(path))
				hasher.Write([]byte{0})
				hasher.Write(bytes)
				hasher.Write([]byte{0})
			default:
				return fmt.Errorf("recipe %s head digest algorithm is unsupported", r.ID)
			}
		}
		if hex.EncodeToString(hasher.Sum(nil)) != head.Tree.SHA256 {
			return fmt.Errorf("recipe %s head tree digest disagrees with its file hashes", r.ID)
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
		fmt.Printf("%s: references, serving gates, and head hash scopes valid\n", path)
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
