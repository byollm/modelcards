// SPDX-License-Identifier: Apache-2.0
// new-card generates a candidate model card from a Hugging Face URL or an Amesh
// runtime pack, then updates index.json. Generated cards are untested candidates.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/byollm/modelcards/internal/cardcheck"
)

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

type options struct {
	source         string
	root           string
	id             string
	name           string
	backend        string
	engineRevision string
	headSource     string
	profileIDs     []string
	force          bool
}

func main() {
	var opts options
	var profiles stringList
	flags := flag.NewFlagSet("new-card", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: go run ./cmd/new-card <huggingface-url | runtime-pack.json | https://…runtime-pack.json> [flags]")
		flags.PrintDefaults()
	}
	flags.StringVar(&opts.root, "root", ".", "repository root that holds cards/ and index.json")
	flags.StringVar(&opts.id, "id", "", "card ID (default: slug of the repository name)")
	flags.StringVar(&opts.name, "name", "", "display name (default: repository name)")
	flags.Var(&profiles, "profile-id", "exact helper profile ID to map; repeatable")
	flags.StringVar(&opts.backend, "backend", "", "Hugging Face sources only: mlx-lm, llama.cpp, vllm or transformers (default by format)")
	flags.StringVar(&opts.engineRevision, "engine-revision", "", "engine source commit (default: the engine repository's current default-branch commit)")
	flags.StringVar(&opts.headSource, "head-source", "", "Hugging Face URL that holds a runtime pack's local_file MTP head")
	flags.BoolVar(&opts.force, "force", false, "overwrite an existing card")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall network deadline")
	// Accept the source before or after flags.
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		opts.source, args = args[0], args[1:]
	}
	flags.Parse(args)
	if opts.source == "" && flags.NArg() == 1 {
		opts.source = flags.Arg(0)
	} else if opts.source == "" || flags.NArg() != 0 {
		flags.Usage()
		os.Exit(2)
	}
	opts.profileIDs = profiles
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	f := &fetcher{
		client:    newHTTPClient(http.DefaultTransport),
		hfBase:    "https://huggingface.co",
		githubAPI: "https://api.github.com",
		hfToken:   os.Getenv("HF_TOKEN"),
	}
	if err := run(ctx, f, opts, time.Now(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "new-card:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, f *fetcher, opts options, now time.Time, out io.Writer) error {
	var ref hfRef
	var pack *packSource
	if strings.HasPrefix(opts.source, "https://huggingface.co/") {
		var err error
		if ref, err = parseHFURL(opts.source); err != nil {
			return err
		}
		if opts.headSource != "" {
			return errors.New("--head-source applies only to runtime pack sources")
		}
	} else {
		data, err := readPack(ctx, f, opts.source)
		if err != nil {
			return err
		}
		if pack, err = parsePack(data); err != nil {
			return err
		}
		if opts.backend != "" {
			return errors.New("--backend applies only to Hugging Face sources; a runtime pack names its executable")
		}
		ref = hfRef{repo: pack.weights.HFRepo, revision: pack.weights.HFRevision, dir: pack.weights.HFSubdir}
		opts.profileIDs = append(opts.profileIDs, pack.pack.Profile.ProfileID)
	}
	repoName := path.Base(ref.repo)
	id := opts.id
	if id == "" {
		id = slug(repoName)
	}
	if !idPattern.MatchString(id) || len(id) > 128 {
		return fmt.Errorf("card ID %q must match ^[a-z0-9][a-z0-9._-]+$ and be at most 128 characters", id)
	}
	name := opts.name
	if name == "" {
		name = strings.NewReplacer("-", " ", "_", " ").Replace(repoName)
	}
	seenProfiles := make(map[string]bool)
	for _, profile := range opts.profileIDs {
		if !idPattern.MatchString(profile) || len(profile) > 128 || seenProfiles[profile] {
			return fmt.Errorf("invalid or repeated profile ID %q", profile)
		}
		seenProfiles[profile] = true
	}
	cardRel := "cards/" + id + ".json"
	cardPath := filepath.Join(opts.root, filepath.FromSlash(cardRel))
	if _, err := os.Stat(cardPath); err == nil && !opts.force {
		return fmt.Errorf("%s exists; pass --force to overwrite", cardRel)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if opts.engineRevision != "" && !commitPattern.MatchString(opts.engineRevision) {
		return errors.New("--engine-revision must be a 40-character lowercase commit SHA")
	}

	s, err := f.resolve(ctx, ref)
	if err != nil {
		return err
	}
	a, err := f.analyze(ctx, s)
	if err != nil {
		return err
	}
	var spec backendSpec
	if pack != nil {
		if spec, err = packBackend(pack.executable, pack.head != nil); err != nil {
			return err
		}
	} else {
		backend := opts.backend
		if backend == "" {
			backend = defaultBackend[a.format]
		}
		var found bool
		if spec, found = hfBackends[backend]; !found {
			return fmt.Errorf("unknown backend %q; mlx-swift recipes come from a runtime pack", backend)
		}
	}
	if spec.format != a.format {
		return fmt.Errorf("backend %s serves %s weights, but %s@%s holds %s weights", spec.name, spec.format, s.ref.repo, s.sha, a.format)
	}
	engineRevision, engineNote := opts.engineRevision, "source_revision was supplied by the card author."
	if engineRevision == "" {
		if engineRevision, err = f.githubHead(ctx, spec.sourceURL); err != nil {
			return err
		}
		engineNote = fmt.Sprintf("source_revision is the default-branch commit on %s.", now.UTC().Format("2006-01-02"))
	}
	engineNote += " No build of this engine has been tested with this recipe."

	recipe := recipeDoc{
		Target: artifactDoc{
			Repository: s.ref.repo, Revision: s.sha, Quantization: a.quantization,
			Files: a.files, Notes: strings.Join(a.notes, " "),
		},
		Engine:          engineDoc{Backend: spec.name, SourceURL: spec.sourceURL, SourceRevision: engineRevision, Product: spec.product},
		Speculation:     speculationDoc{Method: "none"},
		AmeshProfileIDs: opts.profileIDs,
	}
	targetPlaceholder := "{target_directory}"
	if a.format == "gguf" {
		targetPlaceholder = "{target_file}"
	}
	notes := []string{fmt.Sprintf("Generated by cmd/new-card on %s.", now.UTC().Format("2006-01-02")),
		"Candidate only: every capability and validation check is not_tested and no evidence is attached."}
	contextNote := "The upstream metadata declares no context length."
	if a.contextTokens > 0 {
		recipe.ContextTokens = &a.contextTokens
		contextNote = fmt.Sprintf("context_tokens is the advertised %s, not a tested serving limit.", a.contextSource)
	}
	floor := memoryFloor(a.weightsBytes)
	recipe.MinMemoryGiB = &floor
	memoryNote := fmt.Sprintf("min_unified_memory_gib is %d weight bytes × 1.2, rounded up to the next memory tier; it is not a measured footprint.", a.weightsBytes)
	if pack != nil {
		p := pack.pack
		notes[0] = fmt.Sprintf("Generated by cmd/new-card on %s from runtime pack %s revision %d.", now.UTC().Format("2006-01-02"), p.PackID, p.Revision)
		notes = append(notes, "The pack is display data only: this generator did not verify its signature, expiry, platforms or pins.")
		recipe.ContextTokens = &p.Profile.ContextTokens
		contextNote = "context_tokens is the pack profile setting, not a tested serving limit."
		if p.Profile.MinUnifiedGB > 0 {
			recipe.MinMemoryGiB = &p.Profile.MinUnifiedGB
			memoryNote = "min_unified_memory_gib is the pack profile minimum."
		}
		engineNote += fmt.Sprintf(" The pack executable %s has SHA-256 %s; the pack does not say whether it is the native binary or a launcher, so no engine hash field is set.", pack.executable, p.Runtime.SHA256)
		recipe.Launch.Argv = pack.launchArgv(targetPlaceholder)
		if len(p.Signature) != 0 && string(p.Signature) != "null" {
			recipe.Launch.RuntimePack = &runtimePackRef{PackID: p.PackID, Revision: p.Revision, SHA256: pack.sha256, ExpiresAt: p.ExpiresAt}
		} else {
			notes = append(notes, "The source pack is unsigned, so no runtime_pack is linked.")
		}
		if pack.head != nil {
			head, err := f.resolveHead(ctx, pack.head, opts.headSource)
			if err != nil {
				return err
			}
			recipe.Speculation = speculationDoc{
				Method: "native_mtp", Head: head, MaxOffered: pack.mtpDepth,
				Schedule:       fmt.Sprintf("The runtime pack passes --mtp-max-depth %d. Realized draft depth is not recorded.", pack.mtpDepth),
				ScheduleSource: spec.sourceURL,
			}
		}
	} else {
		recipe.Launch.Argv = spec.argv
		notes[0] = fmt.Sprintf("Generated by cmd/new-card on %s from %s.", now.UTC().Format("2006-01-02"), opts.source)
	}
	recipe.Engine.Notes = engineNote
	notes = append(notes, contextNote, memoryNote)
	if a.format == "safetensors" {
		warning := fmt.Sprintf("%s weights for %s: this is not a Mac recipe.", a.quantization, spec.name)
		notes = append(notes, warning)
		fmt.Fprintln(out, "warning:", warning)
	}
	notes = append(notes, a.samplingNote)
	notes = append(notes, upstreamNotes(s.info)...)
	recipe.Notes = strings.Join(notes, " ")
	recipe.Launch.Protocol = "openai_chat_completions"
	recipe.Launch.Availability = "comparison_only"
	if len(recipe.AmeshProfileIDs) > 0 {
		recipe.Launch.Availability = "local_validation_only"
		if recipe.ContextTokens == nil || recipe.MinMemoryGiB == nil {
			return errors.New("a recipe mapped to a helper profile needs known context and memory limits")
		}
	}
	recipeKind := spec.name
	if recipe.Speculation.Method == "native_mtp" && !strings.Contains(recipeKind, "mtp") {
		recipeKind += "-native-mtp"
	}
	recipe.ID = slug(fmt.Sprintf("%s-%s-%s", id, recipeKind, s.sha[:7]))

	card := buildCard(id, name, a, recipe)
	data, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := publish(opts.root, cardRel, data, now); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s (recipe %s, %s@%s, %s, %s)\n", cardRel, recipe.ID, s.ref.repo, s.sha, a.quantization, spec.name)
	fmt.Fprintln(out, "updated index.json")
	return nil
}

func upstreamNotes(info modelInfo) []string {
	var notes []string
	render := func(raw json.RawMessage) string {
		var one string
		if json.Unmarshal(raw, &one) == nil {
			return one
		}
		var many []string
		if json.Unmarshal(raw, &many) == nil {
			return strings.Join(many, ", ")
		}
		return ""
	}
	if license := render(info.CardData.License); license != "" {
		notes = append(notes, "Upstream license: "+license+".")
	}
	if base := render(info.CardData.BaseModel); base != "" {
		notes = append(notes, "Upstream base model: "+base+".")
	}
	if info.PipelineTag != "" {
		notes = append(notes, "Upstream pipeline tag: "+info.PipelineTag+".")
	}
	return notes
}

// resolveHead pins a runtime pack's MTP head to an exact upstream file set.
func (f *fetcher) resolveHead(ctx context.Context, head *packArtifact, headSource string) (*artifactDoc, error) {
	var ref hfRef
	switch head.Kind {
	case "huggingface_snapshot":
		if headSource != "" {
			return nil, errors.New("--head-source applies only to a local_file head; this pack pins a snapshot")
		}
		ref = hfRef{repo: head.HFRepo, revision: head.HFRevision, dir: head.HFSubdir}
		if !validRepo(ref.repo) || !commitPattern.MatchString(ref.revision) || (ref.dir != "" && !validRelativePath(ref.dir)) {
			return nil, fmt.Errorf("MTP head %q has an invalid repository, revision or subdirectory", head.Name)
		}
	case "local_file":
		if headSource == "" {
			return nil, fmt.Errorf("the pack pins MTP head %q only as local file SHA-256 %s; pass --head-source with the Hugging Face URL that holds it", head.Name, head.SHA256)
		}
		var err error
		if ref, err = parseHFURL(headSource); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("MTP head %q has unsupported kind %q", head.Name, head.Kind)
	}
	s, err := f.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	var files []cardFile
	for _, e := range s.files {
		if ref.file != "" && e.Path != ref.file {
			continue
		}
		if head.Kind == "local_file" && (e.LFS == nil || e.LFS.OID != head.SHA256) {
			continue
		}
		pinned, err := f.pinFile(ctx, s, e)
		if err != nil {
			return nil, err
		}
		files = append(files, pinned)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s@%s holds no file with the pack's head SHA-256 %s", s.ref.repo, s.sha, head.SHA256)
	}
	if head.Kind == "local_file" && len(files) != 1 {
		return nil, fmt.Errorf("%s@%s holds %d files with the head SHA-256; name one with a /blob/ URL", s.ref.repo, s.sha, len(files))
	}
	config, err := f.parseJSON(ctx, s, "config.json")
	if err != nil {
		return nil, err
	}
	quantization := "not declared in the upstream head config"
	if q, ok := config["quantization"].(map[string]any); ok {
		quantization = mlxQuantization(q, config)
	}
	return &artifactDoc{
		Repository: s.ref.repo, Revision: s.sha, Quantization: quantization,
		Files: files, TreeDigest: treeDigest(files),
		Notes: "The tree digest covers only the listed head files, not the whole repository. No local head load is implied.",
	}, nil
}

// publish verifies the prospective card set and index, then writes the card
// and that card's index entry. Nothing is written if any check fails.
func publish(root, cardRel string, data []byte, now time.Time) error {
	cardsDir := filepath.Join(root, "cards")
	paths, err := filepath.Glob(filepath.Join(cardsDir, "*.json"))
	if err != nil {
		return err
	}
	existing := make(map[string][]byte)
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		existing["cards/"+filepath.Base(p)] = content
	}
	verifySet := func(cards map[string][]byte) ([]cardcheck.IndexEntry, error) {
		rels := make([]string, 0, len(cards))
		for rel := range cards {
			rels = append(rels, rel)
		}
		// Same order as check-cards' filepath.Glob over cards/*.json.
		sort.Strings(rels)
		verifier := cardcheck.NewVerifier()
		for _, rel := range rels {
			if err := verifier.Add(rel, cards[rel]); err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
		}
		return verifier.Entries(), nil
	}
	indexPath := filepath.Join(root, "index.json")
	published, err := os.ReadFile(indexPath)
	switch {
	case err == nil:
		before, err := verifySet(existing)
		if err != nil {
			return fmt.Errorf("existing cards are invalid: %w", err)
		}
		if err := cardcheck.VerifyIndex(published, before); err != nil {
			return fmt.Errorf("existing index.json does not match the existing cards (%w); run check-cards -write-index first", err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	prospective := make(map[string][]byte, len(existing)+1)
	for rel, content := range existing {
		prospective[rel] = content
	}
	prospective[cardRel] = data
	entries, err := verifySet(prospective)
	if err != nil {
		return fmt.Errorf("generated card fails check-cards validation: %w", err)
	}
	if err := os.MkdirAll(cardsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(cardRel)), data, 0o644); err != nil {
		return err
	}
	return cardcheck.WriteIndex(indexPath, entries, now)
}
