// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/byollm/modelcards/internal/cardcheck"
)

const (
	testToken     = "hf_test_secret_token"
	engineHead    = "1111111111111111111111111111111111111111"
	mlxSHA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ggufSHA       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	headSHA       = "cccccccccccccccccccccccccccccccccccccccc"
	headFileHash  = "d038fd41e2d5dab1b3905c115d859fdc98dfbfde9862c14ebb82c2b3247ec2f1"
	gib           = int64(1) << 30
	treePageSize  = 3
	fixedNowValue = "2026-10-04T12:00:00Z"
)

type fakeFile struct {
	content []byte
	lfsOID  string
	size    int64
}

type fakeRepo struct {
	sha   string
	info  map[string]any
	files map[string]fakeFile
}

func lfs(oidSeed string, size int64) fakeFile {
	sum := sha256.Sum256([]byte(oidSeed))
	return fakeFile{lfsOID: hex.EncodeToString(sum[:]), size: size}
}

func small(content string) fakeFile {
	return fakeFile{content: []byte(content), size: int64(len(content))}
}

func gitBlobID(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// hub serves the subset of the Hugging Face API that new-card reads.
type hub struct {
	repos      map[string]fakeRepo
	authorized []string
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "Bearer "+testToken {
		h.authorized = append(h.authorized, r.URL.Path)
	}
	p := r.URL.Path
	for name, repo := range h.repos {
		switch {
		case strings.HasPrefix(p, "/api/models/"+name+"/revision/"):
			info := map[string]any{"sha": repo.sha}
			for k, v := range repo.info {
				info[k] = v
			}
			json.NewEncoder(w).Encode(info)
			return
		case strings.HasPrefix(p, "/api/models/"+name+"/tree/"+repo.sha):
			dir := strings.Trim(strings.TrimPrefix(p, "/api/models/"+name+"/tree/"+repo.sha), "/")
			h.serveTree(w, r, repo, dir)
			return
		case strings.HasPrefix(p, "/"+name+"/resolve/"+repo.sha+"/"):
			file, found := repo.files[strings.TrimPrefix(p, "/"+name+"/resolve/"+repo.sha+"/")]
			if !found || file.content == nil {
				http.NotFound(w, r)
				return
			}
			w.Write(file.content)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *hub) serveTree(w http.ResponseWriter, r *http.Request, repo fakeRepo, dir string) {
	if dir == "" {
		dir = "."
	}
	var paths []string
	for p := range repo.files {
		if path.Dir(p) == dir {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(start+treePageSize, len(paths))
	entries := []map[string]any{}
	for _, p := range paths[start:end] {
		f := repo.files[p]
		entry := map[string]any{"type": "file", "path": p, "size": f.size}
		if f.lfsOID != "" {
			entry["oid"] = "0000000000000000000000000000000000000000"
			entry["lfs"] = map[string]any{"oid": f.lfsOID, "size": f.size}
		} else {
			entry["oid"] = gitBlobID(f.content)
		}
		entries = append(entries, entry)
	}
	if end < len(paths) {
		next := *r.URL
		next.Scheme, next.Host = "https", r.Host
		q := next.Query()
		q.Set("cursor", strconv.Itoa(end))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next.String()))
	}
	json.NewEncoder(w).Encode(entries)
}

const qwenTemplate = `{%- if tools %}<tools>{{ tools }}</tools>{%- endif %}<|im_start|>assistant\n<think>\n`

func fixtureRepos() map[string]fakeRepo {
	return map[string]fakeRepo{
		"org/Model-MLX": {
			sha: mlxSHA,
			info: map[string]any{
				"tags": []string{"mlx", "safetensors", "license:apache-2.0"}, "library_name": "mlx",
				"pipeline_tag": "text-generation", "config": map[string]any{},
				"cardData": map[string]any{"license": "apache-2.0", "base_model": "org/Model"},
			},
			files: map[string]fakeFile{
				"README.md": small("# root readme\n"),
				"4bit/config.json": small(`{"model_type":"qwen3_5","quantization":{"bits":4,"group_size":64,"mode":"affine"},
					"text_config":{"max_position_embeddings":262144,"num_hidden_layers":64}}`),
				"4bit/generation_config.json":           small(`{"do_sample":true,"temperature":0.6,"top_p":0.95,"top_k":20}`),
				"4bit/chat_template.jinja":              small(qwenTemplate),
				"4bit/tokenizer_config.json":            small(`{"eos_token":"<|im_end|>"}`),
				"4bit/model-00001-of-00002.safetensors": lfs("mlx-1", 5*gib),
				"4bit/model-00002-of-00002.safetensors": lfs("mlx-2", 3*gib),
				"4bit/tokenizer.json":                   lfs("tokenizer", 19989325),
				"8bit/config.json":                      small(`{"quantization":{"bits":8,"group_size":64}}`),
				"8bit/model.safetensors":                lfs("mlx-8bit", 16*gib),
			},
		},
		"org/Model-GGUF": {
			sha: ggufSHA,
			info: map[string]any{
				"tags": []string{"gguf", "conversational"}, "library_name": "transformers",
				"config": map[string]any{"model_type": "qwen3_moe", "num_experts": 128},
				"gguf": map[string]any{
					"total": 30532122624, "architecture": "qwen3moe", "context_length": 40960,
					"chat_template": qwenTemplate,
				},
				"cardData": map[string]any{"license": "apache-2.0"},
			},
			files: map[string]fakeFile{
				".gitattributes":        small("*.gguf filter=lfs\n"),
				"config.json":           small(`{"model_type":"qwen3_moe","num_experts":128,"max_position_embeddings":40960}`),
				"Model-Q4_K_M.gguf":     lfs("q4", 18556686912),
				"Model-Q8_0.gguf":       lfs("q8", 32483932736),
				"mmproj-Model-F16.gguf": lfs("mmproj", 1<<20),
			},
		},
		"org/mtp-head": {
			sha:  headSHA,
			info: map[string]any{"tags": []string{"safetensors"}},
			files: map[string]fakeFile{
				"README.md":         small("head\n"),
				"model.safetensors": {lfsOID: headFileHash, size: 427742600},
			},
		},
	}
}

type harness struct {
	t       *testing.T
	fetcher *fetcher
	hub     *hub
	root    string
	stdout  bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &hub{repos: fixtureRepos()}
	hfServer := httptest.NewTLSServer(h)
	t.Cleanup(hfServer.Close)
	githubServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("GitHub request carried an Authorization header")
		}
		if r.Header.Get("Accept") != "application/vnd.github.sha" || !strings.HasSuffix(r.URL.Path, "/commits/HEAD") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, engineHead)
	}))
	t.Cleanup(githubServer.Close)
	transport := hfServer.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs.AddCert(githubServer.Certificate())
	return &harness{
		t:   t,
		hub: h,
		fetcher: &fetcher{
			client:    newHTTPClient(transport),
			hfBase:    hfServer.URL,
			githubAPI: githubServer.URL,
			hfToken:   testToken,
		},
		root: t.TempDir(),
	}
}

func (h *harness) run(opts options) error {
	h.t.Helper()
	if opts.root == "" {
		opts.root = h.root
	}
	now, _ := time.Parse(time.RFC3339, fixedNowValue)
	err := run(context.Background(), h.fetcher, opts, now, &h.stdout)
	if err != nil && strings.Contains(err.Error(), testToken) {
		h.t.Fatalf("error leaked the token: %v", err)
	}
	if strings.Contains(h.stdout.String(), testToken) {
		h.t.Fatal("output leaked the token")
	}
	return err
}

// checkRoot repeats check-cards over a repository root: every card, the
// cross-card profile mapping, and the exact index hashes.
func checkRoot(t *testing.T, root string) []cardcheck.IndexEntry {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "cards", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no cards under %s", root)
	}
	verifier := cardcheck.NewVerifier()
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifier.Add("cards/"+filepath.Base(p), data); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	index, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cardcheck.VerifyIndex(index, verifier.Entries()); err != nil {
		t.Fatal(err)
	}
	return verifier.Entries()
}

func readCard(t *testing.T, root, id string) (map[string]any, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "cards", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	return card, data
}

func field(v any, keys ...string) any {
	for _, key := range keys {
		switch node := v.(type) {
		case map[string]any:
			v = node[key]
		case []any:
			i, _ := strconv.Atoi(key)
			v = node[i]
		default:
			return nil
		}
	}
	return v
}

func assertCandidateOnly(t *testing.T, card map[string]any) {
	t.Helper()
	if evidence := field(card, "evidence").([]any); len(evidence) != 0 {
		t.Fatalf("generated evidence: %v", evidence)
	}
	recipe := field(card, "recipes", "0").(map[string]any)
	if recipe["role"] != "candidate" || len(recipe["benchmark_ids"].([]any)) != 0 {
		t.Fatalf("recipe is not an unmeasured candidate: role=%v benchmarks=%v", recipe["role"], recipe["benchmark_ids"])
	}
	for _, group := range []string{"capabilities", "validation"} {
		for name, check := range recipe[group].(map[string]any) {
			if field(check, "state") != "not_tested" || len(field(check, "evidence_ids").([]any)) != 0 {
				t.Fatalf("%s.%s = %v; want not_tested without evidence", group, name, check)
			}
		}
	}
	if got := len(recipe["validation"].(map[string]any)); got < len(requiredChecks) {
		t.Fatalf("validation lists %d checks; want every required check", got)
	}
}

func TestMLXSubdirectoryCardCreatesIndex(t *testing.T) {
	h := newHarness(t)
	if err := h.run(options{source: "https://huggingface.co/org/Model-MLX/tree/main/4bit"}); err != nil {
		t.Fatal(err)
	}
	entries := checkRoot(t, h.root)
	if len(entries) != 1 || entries[0].ID != "model-mlx" || entries[0].Path != "cards/model-mlx.json" {
		t.Fatalf("index entries = %+v", entries)
	}
	card, _ := readCard(t, h.root, "model-mlx")
	assertCandidateOnly(t, card)
	recipe := field(card, "recipes", "0")
	checks := map[string]any{
		"name":                                "Model MLX",
		"model_type":                          "dense",
		"display.type_symbol":                 "square.stack.3d.up",
		"display.capability_symbols.tools":    "wrench.and.screwdriver",
		"display.capability_symbols.thinking": "point.3.connected.trianglepath.dotted",
	}
	for key, want := range checks {
		if got := field(card, strings.Split(key, ".")...); got != want {
			t.Errorf("%s = %v; want %v", key, got, want)
		}
	}
	if _, found := field(card, "display", "capability_symbols").(map[string]any)["speculation"]; found {
		t.Error("serial recipe shows a speculation symbol")
	}
	if field(card, "parameter_count") != nil {
		t.Error("quantized MLX card claimed a parameter count")
	}
	recipeChecks := map[string]any{
		"id":                          "model-mlx-mlx-lm-aaaaaaa",
		"target.repository":           "org/Model-MLX",
		"target.revision":             mlxSHA,
		"target.quantization":         "MLX affine 4-bit, group size 64",
		"engine.backend":              "mlx-lm",
		"engine.source_revision":      engineHead,
		"speculation.method":          "none",
		"context_tokens":              float64(262144),
		"min_unified_memory_gib":      float64(16),
		"sampling.mode":               "stochastic",
		"sampling.temperature":        0.6,
		"sampling.top_p":              0.95,
		"launch.availability":         "comparison_only",
		"launch.argv.0":               "mlx_lm.server",
		"capabilities.tools.notes":    "Upstream template or tags reference tool calls.",
		"capabilities.thinking.notes": "Upstream template or tags reference reasoning output.",
	}
	for key, want := range recipeChecks {
		if got := field(recipe, strings.Split(key, ".")...); got != want {
			t.Errorf("recipe %s = %v; want %v", key, got, want)
		}
	}
	if profiles := field(recipe, "amesh_profile_ids").([]any); len(profiles) != 0 {
		t.Errorf("unmapped candidate has profiles %v", profiles)
	}
	var paths []string
	for _, f := range field(recipe, "target", "files").([]any) {
		paths = append(paths, field(f, "path").(string))
		if sha := field(f, "sha256").(string); !sha256Pattern.MatchString(sha) {
			t.Errorf("%v has no SHA-256", f)
		}
	}
	wantPaths := []string{"4bit/chat_template.jinja", "4bit/config.json", "4bit/generation_config.json",
		"4bit/model-00001-of-00002.safetensors", "4bit/model-00002-of-00002.safetensors", "4bit/tokenizer.json", "4bit/tokenizer_config.json"}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("target files = %v; want %v", paths, wantPaths)
	}
	notes := field(recipe, "notes").(string)
	for _, want := range []string{"top_k=20", "Upstream license: apache-2.0.", "not a tested serving limit", "not a measured footprint"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q: %s", want, notes)
		}
	}
	for _, p := range h.hub.authorized {
		if !strings.HasPrefix(p, "/api/models/org/") && !strings.HasPrefix(p, "/org/") {
			t.Errorf("token sent to unexpected path %s", p)
		}
	}
	if len(h.hub.authorized) == 0 {
		t.Error("HF_TOKEN was not sent to the Hugging Face API")
	}
}

func TestGGUFRepositoryNeedsAnExactFile(t *testing.T) {
	h := newHarness(t)
	err := h.run(options{source: "https://huggingface.co/org/Model-GGUF", engineRevision: engineHead})
	if err == nil || !strings.Contains(err.Error(), "/blob/") {
		t.Fatalf("ambiguous GGUF repository: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "index.json")); !os.IsNotExist(err) {
		t.Fatal("a failed run wrote index.json")
	}
	if err := h.run(options{source: "https://huggingface.co/org/Model-GGUF/blob/main/Model-Q4_K_M.gguf", engineRevision: engineHead}); err != nil {
		t.Fatal(err)
	}
	checkRoot(t, h.root)
	card, _ := readCard(t, h.root, "model-gguf")
	assertCandidateOnly(t, card)
	if card["model_type"] != "mixture_of_experts" || card["parameter_count"] != float64(30532122624) {
		t.Errorf("model_type=%v parameter_count=%v", card["model_type"], card["parameter_count"])
	}
	if field(card, "display", "type_symbol") != "circle.hexagongrid" {
		t.Errorf("type symbol = %v", field(card, "display", "type_symbol"))
	}
	recipe := field(card, "recipes", "0")
	checks := map[string]any{
		"target.quantization":    "GGUF Q4_K_M",
		"target.files.0.path":    "Model-Q4_K_M.gguf",
		"target.files.0.bytes":   float64(18556686912),
		"engine.backend":         "llama.cpp",
		"engine.source_revision": engineHead,
		"context_tokens":         float64(40960),
		"min_unified_memory_gib": float64(24),
		"sampling.mode":          "greedy",
		"sampling.temperature":   float64(0),
		"launch.argv.2":          "{target_file}",
	}
	for key, want := range checks {
		if got := field(recipe, strings.Split(key, ".")...); got != want {
			t.Errorf("recipe %s = %v; want %v", key, got, want)
		}
	}
	if files := field(recipe, "target", "files").([]any); len(files) != 1 {
		t.Errorf("GGUF inventory = %v; want only the selected file", files)
	}
}

func writePack(t *testing.T, dir string, mutate func(map[string]any)) string {
	t.Helper()
	pack := map[string]any{
		"schema_version": 1, "kind": "amesh.model-runtime-pack",
		"pack_id": "yukon-native-model-mtp-20261004", "revision": 1, "priority": 110,
		"issued_at": "2026-10-04T15:00:00Z", "expires_at": "2026-11-03T15:00:00Z",
		"platforms": []string{"darwin/arm64"}, "fallback_policy": "fail_closed",
		"profile": map[string]any{
			"profile_id": "yukon-model-mtp-4bit", "model_id": "org/Model-MLX",
			"context_tokens": 65536, "min_unified_gb": 32,
		},
		"runtime": map[string]any{
			"executable":     "/Users/operator/.amesh/rt/runtime/yukon/amesh-yukon-server-ee0f520a5a7a00dab6b307ce9d660f3348d2a904c6d2d86d2908291167d02853",
			"sha256":         "ee0f520a5a7a00dab6b307ce9d660f3348d2a904c6d2d86d2908291167d02853",
			"args":           []string{"--model", "{artifact.weights}", "--host", "{host}", "--port", "{port}", "--mtp-head", "{artifact.mtp_head}", "--mtp-max-depth", "8"},
			"health_path":    "/health",
			"warmup_seconds": 120,
		},
		"artifacts": []map[string]any{
			{"name": "weights", "kind": "huggingface_snapshot", "hf_repo": "org/Model-MLX", "hf_revision": mlxSHA, "hf_subdir": "4bit"},
			{"name": "mtp_head", "kind": "local_file", "path": "/Users/operator/.amesh/rt/heads/model.safetensors", "sha256": headFileHash},
		},
	}
	if mutate != nil {
		mutate(pack)
	}
	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "model.runtime-pack.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRuntimePackCard(t *testing.T) {
	h := newHarness(t)
	packPath := writePack(t, t.TempDir(), nil)
	err := h.run(options{source: packPath, engineRevision: engineHead})
	if err == nil || !strings.Contains(err.Error(), "--head-source") {
		t.Fatalf("local head without a source: err = %v", err)
	}
	if err := h.run(options{source: packPath, engineRevision: engineHead, headSource: "https://huggingface.co/org/mtp-head"}); err != nil {
		t.Fatal(err)
	}
	entries := checkRoot(t, h.root)
	if got := entries[0].AmeshProfileIDs; len(got) != 1 || got[0] != "yukon-model-mtp-4bit" {
		t.Errorf("index profiles = %v", got)
	}
	card, data := readCard(t, h.root, "model-mlx")
	assertCandidateOnly(t, card)
	if bytes.Contains(data, []byte("/Users/")) {
		t.Error("card leaks a local pack path")
	}
	if field(card, "display", "capability_symbols", "speculation") != "bolt.fill" {
		t.Error("speculative recipe lacks the speculation symbol")
	}
	recipe := field(card, "recipes", "0")
	checks := map[string]any{
		"id":                                     "model-mlx-mlx-swift-native-mtp-api-aaaaaaa",
		"amesh_profile_ids.0":                    "yukon-model-mtp-4bit",
		"target.revision":                        mlxSHA,
		"engine.backend":                         "mlx-swift-native-mtp-api",
		"engine.product":                         "amesh-yukon-native",
		"speculation.method":                     "native_mtp",
		"speculation.max_offered_draft_tokens":   float64(8),
		"speculation.head.repository":            "org/mtp-head",
		"speculation.head.revision":              headSHA,
		"speculation.head.files.0.path":          "model.safetensors",
		"speculation.head.files.0.sha256":        headFileHash,
		"speculation.head.files.0.bytes":         float64(427742600),
		"speculation.head.tree_digest.sha256":    "559b24ebca354018e4402fdb1f5af1afe5a0721bd2ebf04133500d846f7d5f71",
		"context_tokens":                         float64(65536),
		"min_unified_memory_gib":                 float64(32),
		"launch.availability":                    "local_validation_only",
		"launch.argv.0":                          "amesh-yukon-server",
		"launch.argv.2":                          "{target_directory}",
		"launch.argv.8":                          "{head_file}",
		"validation.same_workload_speedup.state": "not_tested",
	}
	for key, want := range checks {
		if got := field(recipe, strings.Split(key, ".")...); got != want {
			t.Errorf("recipe %s = %v; want %v", key, got, want)
		}
	}
	if field(recipe, "launch", "runtime_pack") != nil {
		t.Error("an unsigned pack was linked as a runtime pack")
	}
	notes := field(recipe, "notes").(string)
	if !strings.Contains(notes, "did not verify its signature") || strings.Contains(notes, "validated") {
		t.Errorf("pack notes overclaim: %s", notes)
	}
}

func TestRefusesToOverwriteWithoutForce(t *testing.T) {
	h := newHarness(t)
	source := "https://huggingface.co/org/Model-MLX/tree/" + mlxSHA + "/4bit"
	if err := h.run(options{source: source, engineRevision: engineHead}); err != nil {
		t.Fatal(err)
	}
	_, before := readCard(t, h.root, "model-mlx")
	err := h.run(options{source: source, engineRevision: engineHead, name: "Renamed"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite without --force: err = %v", err)
	}
	if _, after := readCard(t, h.root, "model-mlx"); !bytes.Equal(before, after) {
		t.Fatal("refused overwrite still changed the card")
	}
	if err := h.run(options{source: source, engineRevision: engineHead, name: "Renamed", force: true}); err != nil {
		t.Fatal(err)
	}
	entries := checkRoot(t, h.root)
	if len(entries) != 1 || entries[0].Name != "Renamed" {
		t.Fatalf("forced overwrite did not replace the index entry: %+v", entries)
	}
}

func TestAddsEntryToPublishedIndex(t *testing.T) {
	h := newHarness(t)
	_, source, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(source), "..", "..")
	if err := os.MkdirAll(filepath.Join(h.root, "cards"), 0o755); err != nil {
		t.Fatal(err)
	}
	published, _ := filepath.Glob(filepath.Join(repo, "cards", "*.json"))
	for _, p := range append(published, filepath.Join(repo, "index.json")) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(repo, p)
		if err := os.WriteFile(filepath.Join(h.root, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := checkRoot(t, h.root)
	if err := h.run(options{source: "https://huggingface.co/org/Model-MLX/tree/main/4bit", id: "zz-model", engineRevision: engineHead}); err != nil {
		t.Fatal(err)
	}
	after := checkRoot(t, h.root)
	if len(after) != len(before)+1 || after[len(after)-1].ID != "zz-model" {
		t.Fatalf("index entries after = %+v", after)
	}
	for i := range before {
		if before[i].SHA256 != after[i].SHA256 {
			t.Errorf("existing entry %s changed", before[i].ID)
		}
	}
}

func TestRejectsUntrustedSources(t *testing.T) {
	for _, source := range []string{
		"http://huggingface.co/org/repo",
		"https://example.com/org/repo",
		"https://huggingface.co/org",
		"https://huggingface.co/org/repo/resolve/main/x.gguf",
		"https://huggingface.co/org/repo/tree/main/../x",
		"https://huggingface.co/org/repo?x=1",
	} {
		if _, err := parseHFURL(source); err == nil {
			t.Errorf("parseHFURL(%q) accepted", source)
		}
	}
	if _, err := readPack(context.Background(), &fetcher{}, "http://example.com/x.runtime-pack.json"); err == nil {
		t.Error("readPack accepted a plain HTTP URL")
	}
	ref, err := parseHFURL("https://huggingface.co/org/repo/blob/" + mlxSHA + "/sub/Model-Q4_K_M.gguf")
	if err != nil || ref.repo != "org/repo" || ref.revision != mlxSHA || ref.dir != "sub" || ref.file != "sub/Model-Q4_K_M.gguf" {
		t.Errorf("blob ref = %+v, %v", ref, err)
	}
}
