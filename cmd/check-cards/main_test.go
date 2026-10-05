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

func TestMeasuredPromptCountsRejectMalformedAggregateScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong-length", func(m map[string]any) { m["prompt_token_counts"] = []int{71, 68} }},
		{"zero", func(m map[string]any) { m["prompt_token_counts"] = []int{71, 0, 69} }},
		{"negative", func(m map[string]any) { m["prompt_token_counts"] = []int{71, -1, 69} }},
		{"oversized", func(m map[string]any) { m["prompt_token_counts"] = []int{71, 1048577, 69} }},
		{"mixed-scalar", func(m map[string]any) { m["prompt_tokens"] = 71 }},
		{"mixed-zero-scalar", func(m map[string]any) { m["prompt_tokens"] = 0 }},
		{"null", func(m map[string]any) { m["prompt_token_counts"] = nil }},
		{"empty", func(m map[string]any) { m["prompt_token_counts"] = []int{} }},
		{"too-many-runs", func(m map[string]any) {
			counts := make([]int, 129)
			for n := range counts {
				counts[n] = 71
			}
			m["prompt_token_counts"], m["measured_runs"] = counts, 129
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := publishedCard(t, "qwen3.6-27b.json")
			var m map[string]any
			if err := json.Unmarshal(c.Evidence[1].Measurement, &m); err != nil {
				t.Fatal(err)
			}
			delete(m, "prompt_tokens")
			m["prompt_token_counts"], m["measured_runs"] = []int{71, 68, 69}, 3
			test.mutate(m)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			c.Evidence[1].Measurement = data
			if err := verify(c, make(map[string]string)); err == nil || !strings.Contains(err.Error(), "prompt token counts") {
				t.Fatalf("malformed aggregate prompt token counts were accepted: %v", err)
			}
		})
	}
}

func TestMeasuredPromptCountsPreserveValidAggregateAndLegacyScalar(t *testing.T) {
	c := publishedCard(t, "qwen3.6-27b.json")
	if err := verify(c, make(map[string]string)); err != nil {
		t.Fatalf("legacy scalar stopped validating: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(c.Evidence[1].Measurement, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "prompt_tokens")
	m["prompt_token_counts"], m["measured_runs"] = []int{71, 68, 69}, 3
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c.Evidence[1].Measurement = data
	if err := verify(c, make(map[string]string)); err != nil {
		t.Fatalf("valid ordered aggregate was rejected: %v", err)
	}
}

func TestMeasuredPromptCountsRejectNoncanonicalMeasurementKeys(t *testing.T) {
	for _, test := range []struct {
		name, canonical, alias, value string
		series                        bool
		removeCanonical               bool
	}{
		{"series-uppercase-only", "prompt_token_counts", "PROMPT_TOKEN_COUNTS", "[71,68,69]", true, true},
		{"series-last-write-alias", "prompt_token_counts", "PROMPT_TOKEN_COUNTS", "[71,68,70]", true, false},
		{"series-mixed-uppercase-scalar", "prompt_tokens", "PROMPT_TOKENS", "107", true, false},
		{"series-uppercase-runs", "measured_runs", "MEASURED_RUNS", "3", true, true},
		{"legacy-uppercase-scalar", "prompt_tokens", "PROMPT_TOKENS", "107", false, true},
		{"legacy-uppercase-runs", "measured_runs", "MEASURED_RUNS", "3", false, true},
		{"legacy-uppercase-metric", "metric", "METRIC", `"decode_tokens_per_second"`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := publishedCard(t, "qwen3.6-27b.json")
			var m map[string]any
			if err := json.Unmarshal(c.Evidence[1].Measurement, &m); err != nil {
				t.Fatal(err)
			}
			if test.series {
				delete(m, "prompt_tokens")
				m["prompt_token_counts"], m["measured_runs"] = []int{71, 68, 69}, 3
			}
			if test.removeCanonical {
				delete(m, test.canonical)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			c.Evidence[1].Measurement = json.RawMessage(string(raw[:len(raw)-1]) + `,"` + test.alias + `":` + test.value + `}`)
			if err := verify(c, make(map[string]string)); err == nil || !strings.Contains(err.Error(), "prompt token counts") {
				t.Fatalf("noncanonical measurement key was accepted: %v", err)
			}
		})
	}
}


func TestQueryGuidanceAcceptsValidBlock(t *testing.T) {
	c := publishedCard(t, "qwen3.8-27b.json")
	c.Recipes[0].QueryGuidance = validGuidance()
	if err := verify(c, make(map[string]string)); err != nil {
		t.Fatalf("valid query guidance rejected: %v", err)
	}
}

func validGuidance() *queryGuidance {
	yes := true
	q := &queryGuidance{
		SamplingModes: []struct {
			Mode        string   `json:"mode"`
			Temperature *float64 `json:"temperature"`
			TopP        *float64 `json:"top_p"`
			TopK        *int     `json:"top_k"`
			MinP        *float64 `json:"min_p"`
		}{{Mode: "thinking"}, {Mode: "non_thinking"}},
		SamplingLocked: &struct {
			Locked bool   `json:"locked"`
			Reason string `json:"reason"`
		}{Locked: true, Reason: "thinking forced on; sampling knobs are not settable"},
		ReasoningEffort: &struct {
			Supported bool     `json:"supported"`
			Levels    []string `json:"levels"`
		}{Supported: true, Levels: []string{"low", "medium", "high"}},
		Thinking: &struct {
			Mode         string `json:"mode"`
			DisableError string `json:"disable_error"`
		}{Mode: "forced", DisableError: "thinking cannot be disabled on this model"},
		Warnings: []struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}{{Kind: "degradation_mode", Message: "greedy decoding in thinking mode causes repetition loops"}},
		PromptLayout: &struct {
			StablePrefixFirst      *bool  `json:"stable_prefix_first"`
			DynamicContentPosition string `json:"dynamic_content_position"`
		}{StablePrefixFirst: &yes, DynamicContentPosition: "end"},
		SpeculationGuidance: &struct {
			Recommendation          string    `json:"recommendation"`
			CrossoverConcurrency    int       `json:"crossover_concurrency"`
			CrossoverBasis          string    `json:"crossover_basis"`
			ExpectedAcceptanceRates []float64 `json:"expected_acceptance_rates"`
			AcceptanceBasis         string    `json:"acceptance_basis"`
			RecommendedDepth        struct {
				Min     *int   `json:"min"`
				Max     *int   `json:"max"`
				Policy  string `json:"policy"`
				Scalar  *int   `json:"-"`
			} `json:"recommended_depth"`
		}{
			Recommendation:          "measure_first",
			CrossoverConcurrency:    4,
			CrossoverBasis:          "estimated",
			ExpectedAcceptanceRates: []float64{0.73, 0.48, 0.32},
			AcceptanceBasis:         "measured",
		},
	}
	q.SpeculationGuidance.RecommendedDepth.Min = intPtr(1)
	q.SpeculationGuidance.RecommendedDepth.Max = intPtr(3)
	q.SpeculationGuidance.RecommendedDepth.Policy = "confidence_adaptive"
	return q
}

func intPtr(v int) *int { return &v }

func TestQueryGuidanceRejectsInvalidBlocks(t *testing.T) {
	for _, test := range []struct {
		name string
		mut  func(*queryGuidance)
		want string
	}{
		{"increasing-acceptance-rates", func(q *queryGuidance) {
			q.SpeculationGuidance.ExpectedAcceptanceRates = []float64{0.4, 0.9}
			q.SpeculationGuidance.AcceptanceBasis = "measured"
			q.SpeculationGuidance.RecommendedDepth.Max = intPtr(2)
		}, "non-increasing"},
		{"crossover-without-basis", func(q *queryGuidance) {
			q.SpeculationGuidance.CrossoverConcurrency = 8
			q.SpeculationGuidance.CrossoverBasis = ""
		}, "crossover basis"},
		{"rates-without-basis", func(q *queryGuidance) {
			q.SpeculationGuidance.AcceptanceBasis = ""
		}, "acceptance basis"},
		{"depth-rates-length-mismatch", func(q *queryGuidance) {
			q.SpeculationGuidance.ExpectedAcceptanceRates = []float64{0.7, 0.5, 0.3, 0.2}
			q.SpeculationGuidance.AcceptanceBasis = "measured"
			q.SpeculationGuidance.RecommendedDepth.Max = intPtr(3)
		}, "equal max depth"},
		{"depth-min-exceeds-max", func(q *queryGuidance) {
			q.SpeculationGuidance.RecommendedDepth.Min = intPtr(5)
			q.SpeculationGuidance.RecommendedDepth.Max = intPtr(3)
		}, "min exceeds max"},
		{"depth-partial-schedule", func(q *queryGuidance) {
			q.SpeculationGuidance.RecommendedDepth.Max = nil
			q.SpeculationGuidance.RecommendedDepth.Policy = ""
		}, "min, max, and policy"},
		{"unknown-depth-policy", func(q *queryGuidance) {
			q.SpeculationGuidance.RecommendedDepth.Min = intPtr(1)
			q.SpeculationGuidance.RecommendedDepth.Max = intPtr(3)
			q.SpeculationGuidance.RecommendedDepth.Policy = "sometimes"
		}, "unknown policy"},
		{"unlocked-sampling-lock", func(q *queryGuidance) {
			q.SamplingLocked = &struct {
				Locked bool   `json:"locked"`
				Reason string `json:"reason"`
			}{Locked: false, Reason: "x"}
		}, "must be true or absent"},
		{"lock-without-reason", func(q *queryGuidance) {
			q.SamplingLocked = &struct {
				Locked bool   `json:"locked"`
				Reason string `json:"reason"`
			}{Locked: true}
		}, "needs a reason"},
		{"effort-without-levels", func(q *queryGuidance) {
			q.ReasoningEffort = &struct {
				Supported bool     `json:"supported"`
				Levels    []string `json:"levels"`
			}{Supported: true}
		}, "needs levels"},
		{"forced-thinking-without-error", func(q *queryGuidance) {
			q.Thinking = &struct {
				Mode         string `json:"mode"`
				DisableError string `json:"disable_error"`
			}{Mode: "forced"}
		}, "disable_error"},
		{"unknown-warning-kind", func(q *queryGuidance) {
			q.Warnings = []struct {
				Kind    string `json:"kind"`
				Message string `json:"message"`
			}{{Kind: "suggestion", Message: "x"}}
		}, "unknown kind"},
		{"dynamic-content-first", func(q *queryGuidance) {
			q.PromptLayout.DynamicContentPosition = "start"
		}, "defeating prefix caching"},
		{"duplicate-sampling-mode", func(q *queryGuidance) {
			q.SamplingModes = append(q.SamplingModes, q.SamplingModes[0])
		}, "repeats sampling mode"},
		{"unknown-recommendation", func(q *queryGuidance) {
			q.SpeculationGuidance.Recommendation = "maybe"
		}, "unknown recommendation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := publishedCard(t, "qwen3.8-27b.json")
			q := validGuidance()
			test.mut(q)
			c.Recipes[0].QueryGuidance = q
			err := verify(c, make(map[string]string))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}
