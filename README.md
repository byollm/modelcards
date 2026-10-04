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
| [Qwen 3.6 27B](cards/qwen3.6-27b.json) | Validated serial API baseline; MLX-Node MTP comparison at depths 1, 3 and 5 |
| [Qwen 3.8 27B](cards/qwen3.8-27b.json) | Validated serial API baseline; Yukon native-MTP worker; MLX-Node DFlash2 comparison |

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

Run both checks from the repository root:

```sh
npx --yes --package ajv-cli@5.0.0 --package ajv-formats@3.0.1 ajv validate --spec=draft2020 --strict=true -s schemas/model-card.schema.json -d 'cards/*.json' -c ajv-formats --all-errors
go run ./cmd/check-cards/main.go
go test ./cmd/check-cards/main.go ./cmd/check-cards/main_test.go
```

The Go check rejects unresolved references, duplicate profile mappings, promotion
without serving checks, and head or drafter tree digests that disagree with their
file hashes and sizes. Passed checks need local evidence for that recipe and that
check. A local source build cannot stand in for a serving test. Evidence declares
its scope with `recipe_ids`, `validated_checks`, and `validated_capabilities`.
Before publishing card changes, regenerate `index.json` with
`go run ./cmd/check-cards/main.go -write-index`. Its hashes must describe the exact
published card bytes.

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

The serial results were measured on an M4 Max with 40 GPU cores and 128 GiB of
unified memory, running macOS 15.4.1. They use one request at a time, temperature
zero, a fresh per-request cache, and a 128-token output cap. The short and long
default cases emit reasoning within that cap. They do not establish answer quality
or a sustained long-context speed.

The Qwen 3.8 MTP candidate is pinned to the winning Yukon source and head. Its
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

The MLX-Node candidates pin unchanged source and exact staged head or drafter
files. Qwen 3.6 requests fixed native-MTP depths 1, 3 and 5. Qwen 3.8 DFlash2 uses
one anchor plus seven proposals. These use the upstream `/v1/responses` endpoint,
not an Amesh Chat Completions adapter. The local build, help and import checks
passed without loading a model. Remote execution, output parity, speed, tools,
cancellation, GUI launch and private sharing remain untested. No performance value
or launch recommendation comes from that build check.

Repository data and documentation use [Apache-2.0](LICENSE). Each model, head,
drafter, and engine keeps its own license. This repository's license does not
relicense their weights or code.
