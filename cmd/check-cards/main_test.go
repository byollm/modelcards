// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func publishedCard(t *testing.T, filename string) card {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate card fixtures")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "cards", filename))
	if err != nil {
		t.Fatal(err)
	}
	var c card
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPublishedCardsPassSemantics(t *testing.T) {
	profiles := make(map[string]string)
	for _, filename := range []string{"qwen3.6-27b.json", "qwen3.8-27b.json"} {
		if err := verify(publishedCard(t, filename), profiles); err != nil {
			t.Fatalf("%s: %v", filename, err)
		}
	}
}

func TestUnqualifiedRecipesCannotBorrowEvidenceOrLaunchAuthority(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		mutate   func(*card)
		want     string
	}{
		{
			name:     "serial needs known context",
			filename: "qwen3.6-27b.json",
			mutate:   func(c *card) { c.Recipes[0].ContextTokens = nil },
			want:     "known context and memory limits",
		},
		{
			name:     "serial needs known memory",
			filename: "qwen3.6-27b.json",
			mutate:   func(c *card) { c.Recipes[0].MinMemoryGiB = nil },
			want:     "known context and memory limits",
		},
		{
			name:     "comparison cannot be validated",
			filename: "qwen3.6-27b.json",
			mutate:   func(c *card) { c.Recipes[1].Role = "validated" },
			want:     "unmapped candidate without a runtime pack",
		},
		{
			name:     "comparison cannot add a helper profile",
			filename: "qwen3.6-27b.json",
			mutate:   func(c *card) { c.Recipes[1].AmeshProfileIDs = []string{"untrusted-profile"} },
			want:     "unmapped candidate without a runtime pack",
		},
		{
			name:     "comparison cannot attach a runtime pack",
			filename: "qwen3.6-27b.json",
			mutate:   func(c *card) { c.Recipes[1].Launch.RuntimePack = json.RawMessage(`{"pack_id":"untrusted-pack"}`) },
			want:     "unmapped candidate without a runtime pack",
		},
		{
			name:     "build cannot validate API",
			filename: "qwen3.6-27b.json",
			mutate: func(c *card) {
				c.Recipes[1].Validation["openai_api"] = check{State: "passed", EvidenceIDs: []string{"qwen36-mlx-node-local-build-20261004"}}
			},
			want: "needs local evidence covering that check",
		},
		{
			name:     "serial API proof cannot validate a different candidate",
			filename: "qwen3.6-27b.json",
			mutate: func(c *card) {
				c.Recipes[1].Validation["openai_api"] = check{State: "passed", EvidenceIDs: []string{"qwen36-m4max-serial-serving-20261003"}}
			},
			want: "needs local evidence covering that check",
		},
		{
			name:     "standalone token parity cannot validate API",
			filename: "qwen3.8-27b.json",
			mutate: func(c *card) {
				c.Recipes[0].Validation["openai_api"] = check{State: "passed", EvidenceIDs: []string{"qwen38-m4max-native-mtp-fixed-window-parity-20261004"}}
			},
			want: "needs local evidence covering that check",
		},
		{
			name:     "build cannot validate tools capability",
			filename: "qwen3.6-27b.json",
			mutate: func(c *card) {
				c.Recipes[1].Capabilities["tools"] = check{State: "passed", EvidenceIDs: []string{"qwen36-mlx-node-local-build-20261004"}}
			},
			want: "needs local evidence covering that check",
		},
		{
			name:     "DFlash requires a distinct drafter inventory",
			filename: "qwen3.8-27b.json",
			mutate:   func(c *card) { c.Recipes[2].Speculation.Drafter = nil },
			want:     "needs a pinned drafter file inventory",
		},
		{
			name:     "staged drafter digest includes file sizes",
			filename: "qwen3.8-27b.json",
			mutate:   func(c *card) { c.Recipes[2].Speculation.Drafter.Files[0].Bytes++ },
			want:     "tree digest disagrees",
		},
		{
			name:     "staged drafter digest includes file hashes",
			filename: "qwen3.8-27b.json",
			mutate:   func(c *card) { c.Recipes[2].Speculation.Drafter.Files[0].SHA256 = strings.Repeat("a", 64) },
			want:     "tree digest disagrees",
		},
		{
			name:     "draft block includes anchor",
			filename: "qwen3.8-27b.json",
			mutate:   func(c *card) { c.Recipes[2].Speculation.BlockTokens = 7 },
			want:     "block must include one anchor",
		},
		{
			name:     "build evidence must reference its candidate",
			filename: "qwen3.6-27b.json",
			mutate: func(c *card) {
				c.Recipes[1].Engine.Build.EvidenceIDs = []string{"qwen36-m4max-serial-serving-20261003"}
			},
			want: "invalid scoped build evidence",
		},
		{
			name:     "upstream metrics cannot become local default",
			filename: "qwen3.8-27b.json",
			mutate:   func(c *card) { c.Selection.DefaultWorkloadID = "ranked-eight-prompt-greedy-512" },
			want:     "needs a referenced passed local benchmark",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := publishedCard(t, test.filename)
			test.mutate(&c)
			err := verify(c, make(map[string]string))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want rejection containing %q; got %v", test.want, err)
			}
		})
	}
}
