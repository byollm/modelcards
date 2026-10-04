# Model cards

Reproducible model recipes and measured serving results for Amesh.

A model can have several recipes. Each recipe names its backend, exact weights,
engine build, launch parameters, and validation evidence. Speculative recipes also
pin the prediction head or drafter. Speed belongs to a tested recipe and workload.
It is not a property of the model name.

[index.json](index.json) lists the cards, their SHA-256 hashes, model types,
symbols, and exact helper profile mappings. It is a display feed. It grants no
download, install, launch, or sharing authority.

| Card | Recipes |
| --- | --- |
| [Qwen 3.6 27B](cards/qwen3.6-27b.json) | Validated serial API baseline; failed MLX-Node MTP comparisons at depths 1, 3 and 5 |
| [Qwen 3.8 27B](cards/qwen3.8-27b.json) | Validated NativeV4/APIv5 MTP serving recipe and serial baseline; separate standalone MTP worker; failed MLX-Node DFlash2 comparison |
| [Qwen 3.8 27B Abliterated](cards/qwen3.8-27b-abliterated.json) | Untested native MTP candidate for a third-party abliterated checkpoint; every check is `not_tested` |

## Choose the fastest validated recipe

1. Start with profiles that the helper already trusts and can launch. A public
   card cannot add a profile or authorize its command. Signed runtime packs still
   need valid signatures, expiry, platform, artifact pins, and executable pins.
2. Match the target revision, hardware, operating system, memory, context,
   concurrency, and requested sampling settings.
3. Require the capabilities the request uses. A tool request needs a successful
   typed tool call and continuation, not just a declared tool parser.
4. Require serving validation for output, streaming, usage, output limits,
   cancellation, and recovery. A raw decoder benchmark does not establish API or
   GUI support.
5. Compare local results from the same hardware, operating system, workload,
   context, cache policy, sampling, and measurement method. An upstream number
   cannot rank recipes on a local host. Keep decode
   speed, first visible content, and total request time separate. Do not rank a
   single request against aggregate throughput from several clients.
6. Choose the fastest eligible recipe. Recipe order and backend names do not set
   priority. Retain every slower and failed result.

A serial recipe is an explicit fallback. The interface must show the chosen
recipe, whether speculation is active, and the fallback reason. An unsupported
sampling setting, missing head, or failed validation must never cause a silent
switch to serial. If no tested recipe matches, report that fact.

These rules are the card contract. They do not claim that an existing Amesh build
already implements the selector. The cards describe experiments; signed runtime
packs remain the authority for downloading and launching executable code.
Never execute a card's unsigned `launch.argv`. Do not replace the helper's actual
runtime settings or capability results with card metadata.

`selection.default_workload_id` can declare one representative local benchmark
for a GUI recommendation. It must reference a passed local result for the selected
metric. `default_workload_label` gives it a short display name. The recommendation
means fastest tested for that workload on this Mac; it does not predict every
future prompt. If the workload is missing or unknown, show a fallback reason.
An API short-prompt result cannot be ranked against a standalone 512-token worker
window. Future API candidates must use the same prepared short or long request,
cache and sampling settings, and measurement method as the API baseline.

## Schema

[model-card.schema.json](schemas/model-card.schema.json) uses JSON Schema 2020-12.
[model-card-index.schema.json](schemas/model-card-index.schema.json) describes the
display feed.
IDs within a card must be unique. Every evidence and recipe reference must resolve
within that card. Consumers must apply the selection rules above in addition to
structural schema validation.

`measurement.prompt_token_counts` records ordered input-token counts for measured
requests. Its length must equal `measured_runs`; warmups are excluded. Use this
series or the fixed `prompt_tokens` scalar, never both. Recipe comparisons require
the same ordered series. Measurement field names use exact lowercase spelling.

Run both checks from the repository root:

```sh
npx --yes --package ajv-cli@5.0.0 --package ajv-formats@3.0.1 ajv validate --spec=draft2020 --strict=true -s schemas/model-card.schema.json -d 'cards/*.json' -c ajv-formats --all-errors
go run ./cmd/check-cards
go test ./...
```

The Go check rejects unresolved references, duplicate profile mappings, promotion
without serving checks, and head or drafter tree digests that disagree with their
file hashes and sizes. Passed checks need local evidence for that recipe and that
check. A local source build cannot stand in for a serving test. Evidence declares
its scope with `recipe_ids`, `validated_checks`, and `validated_capabilities`.
Before publishing card changes, regenerate `index.json` with
`go run ./cmd/check-cards -write-index`. Its hashes must describe the exact
published card bytes.

## Generate a candidate card

`cmd/new-card` drafts a card from upstream metadata. It never measures anything:
every capability and validation check is `not_tested`, the recipe role is
`candidate`, and the card has no evidence. Review and edit the draft before
publishing it.

```sh
# Hugging Face repository, folder or GGUF file
go run ./cmd/new-card https://huggingface.co/PocketAiHub/Qwen3.8-27B-Abliterated-MLX/tree/1e90b68cc16d79e3f44b3ade10257f99f4b7baff/4bit
go run ./cmd/new-card https://huggingface.co/unsloth/Qwen3-30B-A3B-GGUF/blob/main/Qwen3-30B-A3B-Q4_K_M.gguf

# Amesh runtime pack, by path or HTTPS URL
go run ./cmd/new-card yukon-native-qwen38-abliterated-mtp.template.json \
  --head-source https://huggingface.co/amal-david/qwen38-mtp-head-q2-q4-rerank-v1
```

The generator pins the exact commit SHA and records every file directly in the
chosen folder, or the chosen GGUF file set, with its LFS SHA-256 or the hash of
its verified bytes. It then writes `cards/<id>.json`, adds or replaces that card's
`index.json` entry, and runs the same checks as `check-cards`. It writes nothing
if any check fails, and refuses to overwrite a card without `--force`.

| Format | Default backend | Notes |
| --- | --- | --- |
| MLX (`quantization` in `config.json`, or an MLX library tag) | `mlx-lm` | Quantization from bits, group size and mode |
| GGUF | `llama.cpp` | Quantization from the file name; a folder with several models needs a `/blob/` URL |
| Other safetensors | `vllm` (`--backend transformers`) | Flagged as not a Mac recipe |

Without profiles, a recipe is an unlinked `comparison_only` candidate.
`--profile-id` (repeatable) maps exact helper profiles and makes it
`local_validation_only`. A runtime pack adds its own `profile.profile_id`,
context and memory minimum, and derives native MTP from `--mtp-head` and
`--mtp-max-depth`. A pack that pins its head only as a local file needs
`--head-source` naming the Hugging Face location of a file with that SHA-256. A
pack is display data: the generator does not verify its signature, and links it
as `launch.runtime_pack` only when it carries one.

Derived values are labeled in the recipe notes. Context is the advertised
`max_position_embeddings` or GGUF `context_length`, not a tested limit. The
memory floor is weight bytes × 1.2, rounded up to the next unified-memory tier
(8, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384 or 512 GiB). Sampling follows
`generation_config.json`, or greedy temperature 0 when that file is absent.
Yukon pack recipes follow the engine contract instead: greedy, temperature 0,
unsupported settings rejected; upstream generation defaults go in the notes.
Mixture-of-experts cards use the `circle.hexagongrid` type symbol. The engine
`source_revision` is the engine's current default-branch commit unless
`--engine-revision` names one. Other flags: `--id`, `--name`, `--root`,
`--timeout`, and a repeatable `--note` appended to the recipe notes. Requests use HTTPS only, with bounded timeouts and sizes. An
optional `HF_TOKEN` is sent only to Hugging Face and never logged.

`amesh_profile_ids` maps a recipe to exact helper profiles. An empty list means no
helper profile has been linked. Display symbols use SF Symbols names; other
clients can map them to their own icon set.

## Helper display projection

Match the active trusted profile against `recipes[].amesh_profile_ids`. Project
only the matching recipe and its referenced evidence:

| Display field | Card field |
| --- | --- |
| Model type and symbol | `model_type`, `display.type_symbol` |
| Capability symbols and states | `display.capability_symbols`, `recipes[].capabilities` |
| Recipe and speculation | `recipes[].id`, `recipes[].speculation` |
| Declared launch settings | `recipes[].context_tokens`, `sampling`, `concurrency` |
| Engine and source pins | `recipes[].engine`, `target`, `transform` |
| Drafter and block policy | `recipes[].speculation.drafter`, `block_tokens`, `adaptive_depth` |
| macOS artifact minimum | `recipes[].engine.minimum_macos_version` |
| Validation states | `recipes[].validation` |
| Scoped benchmark results | `recipes[].benchmark_ids` joined to `evidence[].id` |

Keep the source URL and recipe ID in the information view. Label the benchmark's
hardware, workload, metric, and measurement scope. Show unknown or untested fields
as such. A listed MTP candidate does not mean the current process uses speculation.

Validation states are `passed`, `failed`, `not_tested`, or `in_progress`. A recipe
with any required check outside `passed` is ineligible for automatic serving.
An upstream result is evidence for its own test environment. It cannot validate a
different Mac or the Amesh serving adapter.

Hash scopes are explicit. A file SHA-256, a SHA-256 manifest, and a digest of a
staged file tree are different values. Never substitute one for another. Build
only the named inventory when a recipe pins a subset of an upstream repository.

`comparison_only` recipes are unlinked candidates. They have no helper profile or
runtime pack. Their context and memory fields may be `null` until a trial records
real limits; other recipes need known positive limits. Keep those values unknown
in the interface. An artifact's advertised context is not a tested serving limit.

## Current evidence

The earlier serial short and long results were measured on an M4 Max with 40 GPU cores and 128 GiB of
unified memory, running macOS 15.4.1. They use one request at a time, temperature
zero, a fresh per-request cache, and a 128-token output cap. The short and long
default cases emit reasoning within that cap. They do not establish answer quality
or a sustained long-context speed.

The separate Qwen 3.8 standalone MTP candidate is pinned to the winning Yukon source and head. Its
upstream benchmark passed exact-greedy parity on the ranked prompt set. Local
standalone validation matched a fresh same-build serial reference for one fixed
512-token window and reconstructed every declared row with zero residuals. That
window continues beyond EOS. It is not an API stopping or output-limit test.
Local serving, tools, cancellation, and GUI integration remain unvalidated for that
recipe. It has no published Amesh runtime pack and is not the default.

The upstream mode is named `qwen-mtp-paired-decode-only`, but its parent timing
charges seed prefill within the 512-token window. Model loading and
input-independent warmup are excluded. The official speedup compares the original
pinned serial baseline worker with the winning candidate. It is not a pure
steady-state decode rate or a same-current-candidate serial comparison.

The GUI serving recipe `qwen38-yukon-native-mtp-api-v5` pairs NativeV4 with the
APIv5 Go launcher and signed pack revision 4. On the same M4 Max, its measured
code and prose medians were 61.82 and 51.97 request tokens/s. The retained serial
baseline measured 26.51 and 26.87. Each workload has three measured requests and
one excluded warmup, with 128 reported output tokens per request. Ordered input
counts are `[71,68,69]` for code and `[61,58,64]` for prose.

These rates include prompt processing in full client request time. They exclude
model loading because the worker is resident. Thinking is explicitly disabled.
The serial baseline was not rerun as fresh interleaved thermal pairs. Cache misses
are not independently proven. These results do not establish a decode rate or
predict every future prompt.

The API passed 21 completed self-checks and matched 18 commonly accepted serial
responses exactly. The global paired suite remains **FAIL**: serial rejects three
stop requests that MTP supports. Cancellation proves only a client abort before
response headers and recovery on the same chain. Native admission, prefill-stage
cancellation and nonstream disconnect handling remain unproven.

Actual web GUI Start and Confirm launched the exact profile. This launch check
does not qualify GUI client sampling or default-temperature behavior. A two-turn FloCode
session used the canonical private route, completed a typed read with numeric
arguments, and answered correctly. Explicit `--temperature 0` is required.
Repetition penalty must be omitted or 1; presence and frequency penalties must be
omitted or 0. The configured 65,536-token context and 32 GiB memory floor are not
full-context performance or fidelity tests.

The MLX-Node candidates pin unchanged source and exact staged head or drafter
files. Qwen 3.6 requests fixed native-MTP depths 1, 3 and 5. Qwen 3.8 DFlash2 uses
one anchor plus seven proposals. These use the upstream `/v1/responses` endpoint,
not an Amesh Chat Completions adapter. Remote trials completed with active native
speculation, but exact serial-output fidelity failed for all four configurations.
Qwen 3.6 passed 13 of 24 pairs across the three depths; every depth failed at least
one code or prose pair. Qwen 3.8 DFlash2 passed four of eight pairs and failed both
code and both prose pairs. These candidates have no eligible helper profile, rate
claim or launch recommendation. Tools, cancellation, GUI launch and private
sharing remain unqualified for them.

Repository data and documentation use [Apache-2.0](LICENSE). Each model, head,
drafter, and engine keeps its own license. This repository's license does not
relicense their weights or code.
