// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type cardFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type treeDigestDoc struct {
	SHA256    string   `json:"sha256"`
	Algorithm string   `json:"algorithm"`
	Inventory []string `json:"inventory"`
}

type artifactDoc struct {
	Repository   string         `json:"repository"`
	Revision     string         `json:"revision"`
	Quantization string         `json:"quantization"`
	Files        []cardFile     `json:"files,omitempty"`
	TreeDigest   *treeDigestDoc `json:"tree_digest,omitempty"`
	Notes        string         `json:"notes,omitempty"`
}

type engineDoc struct {
	Backend        string `json:"backend"`
	SourceURL      string `json:"source_url"`
	SourceRevision string `json:"source_revision"`
	Product        string `json:"product"`
	Notes          string `json:"notes"`
}

type speculationDoc struct {
	Method         string       `json:"method"`
	Head           *artifactDoc `json:"head,omitempty"`
	MaxOffered     int          `json:"max_offered_draft_tokens,omitempty"`
	Schedule       string       `json:"schedule,omitempty"`
	ScheduleSource string       `json:"schedule_source,omitempty"`
	Notes          string       `json:"notes,omitempty"`
}

type samplingDoc struct {
	Mode              string   `json:"mode"`
	Temperature       float64  `json:"temperature"`
	TopP              *float64 `json:"top_p,omitempty"`
	UnsupportedPolicy string   `json:"unsupported_policy"`
}

type checkDoc struct {
	State       string   `json:"state"`
	EvidenceIDs []string `json:"evidence_ids"`
	Notes       string   `json:"notes,omitempty"`
}

type runtimePackRef struct {
	PackID    string `json:"pack_id"`
	Revision  int    `json:"revision"`
	SHA256    string `json:"sha256"`
	ExpiresAt string `json:"expires_at"`
}

type launchDoc struct {
	Availability string          `json:"availability"`
	Protocol     string          `json:"protocol"`
	Argv         []string        `json:"argv"`
	RuntimePack  *runtimePackRef `json:"runtime_pack,omitempty"`
}

type recipeDoc struct {
	ID              string              `json:"id"`
	Role            string              `json:"role"`
	Target          artifactDoc         `json:"target"`
	Engine          engineDoc           `json:"engine"`
	Speculation     speculationDoc      `json:"speculation"`
	ContextTokens   *int64              `json:"context_tokens"`
	MinMemoryGiB    *int64              `json:"min_unified_memory_gib"`
	Concurrency     int                 `json:"concurrency"`
	Sampling        samplingDoc         `json:"sampling"`
	Capabilities    map[string]checkDoc `json:"capabilities"`
	Validation      map[string]checkDoc `json:"validation"`
	Launch          launchDoc           `json:"launch"`
	BenchmarkIDs    []string            `json:"benchmark_ids"`
	Notes           string              `json:"notes"`
	AmeshProfileIDs []string            `json:"amesh_profile_ids"`
}

type displayDoc struct {
	TypeSymbol        string            `json:"type_symbol"`
	CapabilitySymbols map[string]string `json:"capability_symbols"`
}

type selectionDoc struct {
	Policy           string   `json:"policy"`
	Metric           string   `json:"metric"`
	RequiredChecks   []string `json:"required_checks"`
	ComparableFields []string `json:"comparable_fields"`
	FallbackPolicy   string   `json:"fallback_policy"`
}

type cardDoc struct {
	Schema         string       `json:"$schema"`
	SchemaVersion  int          `json:"schema_version"`
	Kind           string       `json:"kind"`
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	ModelType      string       `json:"model_type"`
	ParameterCount int64        `json:"parameter_count,omitempty"`
	Display        displayDoc   `json:"display"`
	Selection      selectionDoc `json:"selection"`
	Recipes        []recipeDoc  `json:"recipes"`
	Evidence       []any        `json:"evidence"`
}

// Conventions shared with the published cards.
var (
	requiredChecks = []string{
		"local_output_fidelity", "openai_api", "streaming", "usage", "output_limit",
		"stream_cancel_recovery", "typed_tool_continuation", "gui_launch", "private_mesh",
	}
	comparableFields = []string{
		"target.repository", "target.revision", "hardware", "context_tokens", "sampling",
		"concurrency", "workload_id", "measurement.method", "measurement.cache_policy",
		"measurement.includes_reasoning",
	}
	typeSymbols = map[string]string{
		"dense":              "square.stack.3d.up",
		"mixture_of_experts": "circle.hexagongrid",
	}
	capabilitySymbols = map[string]string{
		"chat":        "bubble.left.and.bubble.right",
		"speculation": "bolt.fill",
		"streaming":   "waveform",
		"thinking":    "point.3.connected.trianglepath.dotted",
		"tools":       "wrench.and.screwdriver",
	}
	memoryTiersGiB = []int64{8, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512}
)

type backendSpec struct {
	name      string
	sourceURL string
	product   string
	format    string
	argv      []string
}

var hfBackends = map[string]backendSpec{
	"mlx-lm": {
		name: "mlx-lm", sourceURL: "https://github.com/ml-explore/mlx-lm", product: "mlx_lm.server", format: "mlx",
		argv: []string{"mlx_lm.server", "--model", "{target_directory}", "--host", "{host}", "--port", "{port}"},
	},
	"llama.cpp": {
		name: "llama.cpp", sourceURL: "https://github.com/ggml-org/llama.cpp", product: "llama-server", format: "gguf",
		argv: []string{"llama-server", "--model", "{target_file}", "--host", "{host}", "--port", "{port}", "--ctx-size", "{context_tokens}"},
	},
	"vllm": {
		name: "vllm", sourceURL: "https://github.com/vllm-project/vllm", product: "vllm serve", format: "safetensors",
		argv: []string{"vllm", "serve", "{target_directory}", "--host", "{host}", "--port", "{port}", "--max-model-len", "{context_tokens}"},
	},
	"transformers": {
		name: "transformers", sourceURL: "https://github.com/huggingface/transformers", product: "transformers serve", format: "safetensors",
		argv: []string{"transformers", "serve", "--host", "{host}", "--port", "{port}"},
	},
}

var defaultBackend = map[string]string{"mlx": "mlx-lm", "gguf": "llama.cpp", "safetensors": "vllm"}

// packBackend maps a runtime pack executable name to its engine.
func packBackend(executable string, speculative bool) (backendSpec, error) {
	switch executable {
	case "amesh-yukon-server":
		spec := backendSpec{name: "mlx-swift", sourceURL: "https://github.com/Layr-Labs/qwen-3.8-mtp-challenge", product: "amesh-yukon-native", format: "mlx"}
		if speculative {
			spec.name = "mlx-swift-native-mtp-api"
		}
		return spec, nil
	case "llama-server":
		return hfBackends["llama.cpp"], nil
	case "mlx_lm.server":
		return hfBackends["mlx-lm"], nil
	}
	return backendSpec{}, fmt.Errorf("runtime pack executable %q has no known engine", executable)
}

var (
	ggufShard = regexp.MustCompile(`^(.*)-(\d{5})-of-(\d{5})\.gguf$`)
	ggufQuant = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:IQ[1-4]_(?:XXS|XS|NL|S|M)|Q[2-8]_K(?:_(?:XL|S|M|L))?|Q[4-8]_[01]|TQ[12]_0|MXFP4(?:_MOE)?|BF16|F16|F32))(?:[-_.]|$)`)
)

// analysis is everything derived from one pinned weights directory.
type analysis struct {
	format         string
	quantization   string
	modelType      string
	parameterCount int64
	contextTokens  int64
	contextSource  string
	weightsBytes   int64
	primaryFile    string
	files          []cardFile
	sampling       samplingDoc
	samplingNote   string
	chat           string
	tools          string
	thinking       string
	hasTools       bool
	hasThinking    bool
	notes          []string
}

func lookup(config map[string]any, key string) (any, bool) {
	if v, found := config[key]; found && v != nil {
		return v, true
	}
	if text, ok := config["text_config"].(map[string]any); ok {
		if v, found := text[key]; found && v != nil {
			return v, true
		}
	}
	return nil, false
}

func number(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok
}

func intValue(config map[string]any, key string) int64 {
	if v, found := lookup(config, key); found {
		if n, ok := number(v); ok && n > 0 && n == float64(int64(n)) {
			return int64(n)
		}
	}
	return 0
}

func stringValue(config map[string]any, key string) string {
	v, _ := lookup(config, key)
	s, _ := v.(string)
	return s
}

func isMoE(config map[string]any) bool {
	for _, key := range []string{"num_experts", "n_routed_experts", "num_local_experts", "moe_num_experts"} {
		if intValue(config, key) > 1 {
			return true
		}
	}
	scopes := []map[string]any{config}
	if text, ok := config["text_config"].(map[string]any); ok {
		scopes = append(scopes, text)
	}
	for _, scope := range scopes {
		for key, v := range scope {
			if n, ok := number(v); ok && n > 0 && strings.Contains(key, "moe") {
				return true
			}
		}
		if model, ok := scope["model_type"].(string); ok && strings.Contains(model, "moe") {
			return true
		}
	}
	return false
}

// memoryFloor is weights bytes × 1.2, rounded up to the next unified-memory tier.
func memoryFloor(weightsBytes int64) int64 {
	const gib = int64(1) << 30
	needed := (weightsBytes*12/10 + gib - 1) / gib
	for _, tier := range memoryTiersGiB {
		if needed <= tier {
			return tier
		}
	}
	return needed
}

func hasTag(tags []string, names ...string) bool {
	for _, tag := range tags {
		for _, name := range names {
			if strings.EqualFold(tag, name) {
				return true
			}
		}
	}
	return false
}

func (f *fetcher) parseJSON(ctx context.Context, s *snapshot, name string) (map[string]any, error) {
	data, err := f.readNamed(ctx, s, name)
	if err != nil || data == nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// chatTemplate returns the first chat template found in the pinned directory,
// then in the repository-level metadata the Hub parsed for us.
func (f *fetcher) chatTemplate(ctx context.Context, s *snapshot, tokenizer map[string]any) (string, error) {
	data, err := f.readNamed(ctx, s, "chat_template.jinja")
	if err != nil || data != nil {
		return string(data), err
	}
	templateText := func(v any) string {
		switch t := v.(type) {
		case string:
			return t
		case []any:
			var parts []string
			for _, item := range t {
				if m, ok := item.(map[string]any); ok {
					if text, ok := m["template"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
		return ""
	}
	if text := templateText(tokenizer["chat_template"]); text != "" {
		return text, nil
	}
	if s.ref.dir == "" {
		if tc, ok := s.info.Config["tokenizer_config"].(map[string]any); ok {
			if text := templateText(tc["chat_template"]); text != "" {
				return text, nil
			}
		}
	}
	if s.info.GGUF != nil {
		return s.info.GGUF.ChatTemplate, nil
	}
	return "", nil
}

func selectGGUF(s *snapshot) ([]treeEntry, error) {
	groups := make(map[string][]treeEntry)
	for _, e := range s.files {
		name := path.Base(e.Path)
		if !strings.HasSuffix(name, ".gguf") || strings.HasPrefix(strings.ToLower(name), "mmproj") {
			continue
		}
		key := e.Path
		if m := ggufShard.FindStringSubmatch(e.Path); m != nil {
			key = m[1] + "-of-" + m[3]
		}
		groups[key] = append(groups[key], e)
	}
	key := ""
	if s.ref.file != "" {
		for k, members := range groups {
			for _, e := range members {
				if e.Path == s.ref.file {
					key = k
				}
			}
		}
		if key == "" {
			return nil, fmt.Errorf("%s is not a GGUF model file at %s", s.ref.file, s.sha)
		}
	} else {
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 1 {
			return nil, fmt.Errorf("directory holds %d GGUF models (%s); choose one with a /blob/<rev>/<file>.gguf URL", len(keys), strings.Join(keys, ", "))
		}
		if len(keys) == 0 {
			return nil, nil
		}
		key = keys[0]
	}
	members := groups[key]
	sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	if m := ggufShard.FindStringSubmatch(members[0].Path); m != nil {
		if want, _ := strconv.Atoi(m[3]); want != len(members) {
			return nil, fmt.Errorf("GGUF %s has %d of %d shards", key, len(members), want)
		}
	}
	return members, nil
}

func (f *fetcher) analyze(ctx context.Context, s *snapshot) (*analysis, error) {
	a := &analysis{modelType: "dense"}
	ggufs, err := selectGGUF(s)
	if err != nil {
		return nil, err
	}
	var safetensors []treeEntry
	for _, e := range s.files {
		if strings.HasSuffix(e.Path, ".safetensors") {
			safetensors = append(safetensors, e)
		}
	}
	if s.ref.file != "" && !strings.HasSuffix(s.ref.file, ".gguf") {
		return nil, fmt.Errorf("a /blob/ source must name a .gguf file")
	}
	if len(ggufs) > 0 && len(safetensors) > 0 && s.ref.file == "" {
		return nil, fmt.Errorf("directory holds both GGUF and safetensors weights; choose a GGUF file with a /blob/ URL")
	}
	config, err := f.parseJSON(ctx, s, "config.json")
	if err != nil {
		return nil, err
	}
	tokenizer, err := f.parseJSON(ctx, s, "tokenizer_config.json")
	if err != nil {
		return nil, err
	}
	switch {
	case len(ggufs) > 0:
		a.format = "gguf"
		if config == nil {
			config = s.info.Config
		}
		for _, e := range ggufs {
			pinned, err := f.pinFile(ctx, s, e)
			if err != nil {
				return nil, err
			}
			a.files = append(a.files, pinned)
			a.weightsBytes += pinned.Bytes
		}
		a.primaryFile = ggufs[0].Path
		a.quantization = "GGUF, quantization not encoded in file name"
		if m := ggufQuant.FindStringSubmatch(path.Base(a.primaryFile)); m != nil {
			a.quantization = "GGUF " + strings.ToUpper(m[1])
		}
		if g := s.info.GGUF; g != nil {
			a.parameterCount = g.Total
			if g.ContextLength > 0 {
				a.contextTokens, a.contextSource = g.ContextLength, "GGUF context_length"
			}
			if strings.Contains(g.Architecture, "moe") {
				a.modelType = "mixture_of_experts"
			}
		}
		a.notes = append(a.notes, "Target inventory is the selected GGUF file set only.")
	case len(safetensors) > 0:
		if config == nil {
			return nil, fmt.Errorf("%s@%s has safetensors weights but no config.json in %q", s.ref.repo, s.sha, s.ref.dir)
		}
		for _, e := range s.files {
			pinned, err := f.pinFile(ctx, s, e)
			if err != nil {
				return nil, err
			}
			a.files = append(a.files, pinned)
		}
		for _, e := range safetensors {
			a.weightsBytes += e.Size
		}
		quantization, quantized := config["quantization"].(map[string]any)
		if quantized || s.info.LibraryName == "mlx" || hasTag(s.info.Tags, "mlx") {
			a.format = "mlx"
			a.quantization = mlxQuantization(quantization, config)
		} else {
			a.format = "safetensors"
			a.quantization = safetensorsQuantization(config)
		}
		_, declaresQuantization := config["quantization_config"]
		if s.ref.dir == "" && !quantized && !declaresQuantization && s.info.Safetensors != nil {
			a.parameterCount = s.info.Safetensors.Total
		}
		scope := "repository root"
		if s.ref.dir != "" {
			scope = s.ref.dir + "/"
		}
		a.notes = append(a.notes, fmt.Sprintf("Target inventory is every file directly under %s at the pinned revision.", scope))
	default:
		return nil, fmt.Errorf("%s@%s has no GGUF or safetensors weights in %q", s.ref.repo, s.sha, s.ref.dir)
	}
	if config != nil {
		if isMoE(config) {
			a.modelType = "mixture_of_experts"
		}
		if a.contextTokens == 0 {
			if n := intValue(config, "max_position_embeddings"); n > 0 {
				a.contextTokens, a.contextSource = n, "max_position_embeddings"
			}
		}
	}
	generation, err := f.parseJSON(ctx, s, "generation_config.json")
	if err != nil {
		return nil, err
	}
	a.sampling, a.samplingNote = samplingDefaults(generation)
	template, err := f.chatTemplate(ctx, s, tokenizer)
	if err != nil {
		return nil, err
	}
	a.describeCapabilities(template, s.info)
	return a, nil
}

func mlxQuantization(q map[string]any, config map[string]any) string {
	if q == nil {
		if dtype := firstString(config, "torch_dtype", "dtype"); dtype != "" {
			return "MLX unquantized " + dtype
		}
		return "MLX unquantized"
	}
	mode, _ := q["mode"].(string)
	if mode == "" {
		mode = "affine"
	}
	bits, _ := number(q["bits"])
	group, _ := number(q["group_size"])
	text := fmt.Sprintf("MLX %s %g-bit, group size %g", mode, bits, group)
	for _, v := range q {
		if _, perLayer := v.(map[string]any); perLayer {
			return text + ", with per-layer overrides"
		}
	}
	return text
}

func safetensorsQuantization(config map[string]any) string {
	if qc, ok := config["quantization_config"].(map[string]any); ok {
		if method, ok := qc["quant_method"].(string); ok && method != "" {
			return "safetensors, quant_method " + method
		}
	}
	if dtype := firstString(config, "torch_dtype", "dtype"); dtype != "" {
		return "safetensors " + dtype
	}
	return "safetensors, dtype not declared"
}

func firstString(config map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := stringValue(config, key); s != "" {
			return s
		}
	}
	return ""
}

func samplingDefaults(generation map[string]any) (samplingDoc, string) {
	doc := samplingDoc{Mode: "greedy", Temperature: 0, UnsupportedPolicy: "reject"}
	if generation == nil {
		return doc, "No generation_config.json; greedy temperature 0 is a neutral placeholder, not an upstream default."
	}
	var extras []string
	for _, key := range []string{"top_k", "min_p", "repetition_penalty", "presence_penalty"} {
		if v, found := generation[key]; found && v != nil {
			extras = append(extras, fmt.Sprintf("%s=%v", key, v))
		}
	}
	note := "Sampling copies generation_config.json."
	if len(extras) > 0 {
		note += " Upstream also sets " + strings.Join(extras, ", ") + ", which the card sampling block cannot express."
	}
	if doSample, _ := generation["do_sample"].(bool); !doSample {
		return doc, note
	}
	doc.Mode = "stochastic"
	doc.Temperature = 1
	if t, ok := number(generation["temperature"]); ok && t >= 0 {
		doc.Temperature = t
	}
	if p, ok := number(generation["top_p"]); ok && p > 0 && p <= 1 {
		doc.TopP = &p
	}
	return doc, note
}

func (a *analysis) describeCapabilities(template string, info modelInfo) {
	a.hasTools = strings.Contains(template, "tools") || strings.Contains(template, "tool_call") ||
		hasTag(info.Tags, "function-calling", "tool-use")
	a.hasThinking = strings.Contains(template, "<think>") || strings.Contains(template, "enable_thinking") ||
		strings.Contains(template, "reasoning_content") || hasTag(info.Tags, "reasoning", "thinking")
	switch {
	case template != "":
		a.chat = "Upstream chat template present."
	case hasTag(info.Tags, "conversational"):
		a.chat = "Tagged conversational upstream; no chat template found."
	default:
		a.chat = "No chat template or conversational tag found upstream."
	}
	a.tools = "No tool markup found in the upstream template or tags."
	if a.hasTools {
		a.tools = "Upstream template or tags reference tool calls."
	}
	a.thinking = "No reasoning markup found in the upstream template or tags."
	if a.hasThinking {
		a.thinking = "Upstream template or tags reference reasoning output."
	}
}

// treeDigest hashes exactly the named files with the two-space manifest algorithm.
func treeDigest(files []cardFile) *treeDigestDoc {
	sorted := append([]cardFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	hasher := sha256.New()
	inventory := make([]string, 0, len(sorted))
	for _, f := range sorted {
		fmt.Fprintf(hasher, "%s  %s\n", f.SHA256, f.Path)
		inventory = append(inventory, f.Path)
	}
	return &treeDigestDoc{
		SHA256:    hex.EncodeToString(hasher.Sum(nil)),
		Algorithm: "sha256_of_sorted_file_sha256_two_spaces_relative_path_lf",
		Inventory: inventory,
	}
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-._")
	if len(out) > 128 {
		out = strings.Trim(out[:128], "-._")
	}
	return out
}

func notTested(note string) checkDoc {
	return checkDoc{State: "not_tested", EvidenceIDs: []string{}, Notes: note}
}

// buildCard assembles a candidate card. Every check is not_tested and no
// evidence is attached: generation reads metadata, it never measures.
func buildCard(id, name string, a *analysis, recipe recipeDoc) cardDoc {
	symbols := map[string]string{"chat": capabilitySymbols["chat"], "streaming": capabilitySymbols["streaming"]}
	if a.hasTools {
		symbols["tools"] = capabilitySymbols["tools"]
	}
	if a.hasThinking {
		symbols["thinking"] = capabilitySymbols["thinking"]
	}
	if recipe.Speculation.Method != "none" {
		symbols["speculation"] = capabilitySymbols["speculation"]
	}
	recipe.Role = "candidate"
	recipe.Concurrency = 1
	recipe.Sampling = a.sampling
	recipe.Capabilities = map[string]checkDoc{
		"chat":      notTested(a.chat),
		"tools":     notTested(a.tools),
		"streaming": notTested(""),
		"thinking":  notTested(a.thinking),
	}
	recipe.Validation = make(map[string]checkDoc)
	for _, check := range requiredChecks {
		recipe.Validation[check] = notTested("")
	}
	if recipe.Speculation.Method != "none" {
		recipe.Validation["same_workload_speedup"] = notTested("")
	}
	recipe.BenchmarkIDs = []string{}
	if recipe.AmeshProfileIDs == nil {
		recipe.AmeshProfileIDs = []string{}
	}
	return cardDoc{
		Schema: "../schemas/model-card.schema.json", SchemaVersion: 1, Kind: "amesh.model-card",
		ID: id, Name: name, ModelType: a.modelType, ParameterCount: a.parameterCount,
		Display: displayDoc{TypeSymbol: typeSymbols[a.modelType], CapabilitySymbols: symbols},
		Selection: selectionDoc{
			Policy: "fastest_validated", Metric: "request_tokens_per_second",
			RequiredChecks: requiredChecks, ComparableFields: comparableFields,
			FallbackPolicy: "visible_reason_required",
		},
		Recipes:  []recipeDoc{recipe},
		Evidence: []any{},
	}
}
