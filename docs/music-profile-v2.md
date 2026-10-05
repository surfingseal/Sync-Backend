# Controlled Music Profile v2

## Architecture

Photo → Gemini visual atmosphere analysis → ImageAnalysis/MusicProfile → deterministic query
→ YouTube candidate retrieval → eligibility filters → V2 ranker → user-selected playlist.

Gemini interprets visible atmosphere. It never generates real songs, artists, or speculative YouTube results.
The controlled vocabulary defines musical intent. Code creates search queries; YouTube remains the source of
actual video metadata. V2 ranker weights are unchanged. This implementation performed no live inference/search.

## Existing schema audit

The previous schema used unrestricted `mood.tags` (1–6 strings) and `music_profile.genres` (1–5 strings).
It had brightness/color temperature/colors/motion and tempo/energy/vocal preference, but no contrast,
saturation, primary/secondary mood, scored genre hierarchy, instrumental preference or confidence.
Language belonged to `preferences.languages`, not to image inference. That remains true: the photo does not
establish a listener's preferred language. The previous deterministic builder used genre/tag order,
language and vocal/tempo fallback; it did not use genre score/confidence.

## Compatibility decision

Do not change `music_profile.genres` from strings to objects in the existing API. Instead v2 is additive:

- `schema_version: "2"`
- `visual.contrast`, `visual.saturation`
- `mood.primary`, `mood.secondary` (0–3); `tags` mirrors primary + secondary
- `music_profile.genre_candidates` (1–3 category/name/score objects); `genres` mirrors their names
- `music_profile.instrumental_preference` (0–1)
- optional public `confidence` (mood/genre/tempo, 0–1); required in new provider responses

Existing requests and stored v1 analyses remain valid without these fields. v2 requires the full enhanced
profile rather than accepting partly contradictory fields. A serialized empty secondary list can be omitted
from the public v2 object; new provider responses must explicitly include the array. `DecodeEnhancedImageAnalysis`
rejects legacy or incomplete Vertex results; `DecodeImageAnalysis` accepts validated legacy/v2 API input.
The live client uses the enhanced schema and prompt. Older test data and benchmark files were not migrated in place.

`AdaptQueryIntent` projects legacy input into controlled query fields through curated aliases. It does not
invent numeric genre scores, instrumental preference or confidence. Those values remain absent. It preserves
unsupported genre names only as diagnostic `raw_label` on `other`, never as query text. Legacy scene/mood/genre
fields are not mutated, and existing ranker inputs retain their previous contract.

## Vocabulary

`internal/music/taxonomy.json` is the single registry: 12 families, 54 entries (52 musical genre entries
and other/unknown), 22 canonical moods. Each genre has a parent category, unique slug/display name,
curated aliases and query tokens. Unknown/other deliberately map to no search tokens.

Examples: dream-pop → dream pop; r&b → rnb; k-indie → korean indie; lo-fi-hip-hop → lofi hip hop.
Only canonical slugs are accepted in v2 model output; aliases are for legacy/user preference normalization.
Unknown free labels are never admitted as new production genres. Category/name mismatches and duplicate
v2 genres fail validation. Same-family distinct genres remain allowed; no forced family diversity.
Legacy mood aliases are explicit (e.g. ethereal→dreamy, cheerful→bright, majestic→powerful).
The vocabulary and these alias decisions should be reviewed alongside the later human quality study.

`AnalysisSchema()` injects enum lists from the registry into the embedded base schema at runtime. The emitted
`enhanced-schema.json` snapshot is the effective SDK schema. Category/name pairing is additionally checked
by server validation and spelled out in the system prompt; the schema's separate enums alone cannot enforce pairing.

## Prompt priority

The system prompt explicitly instructs:

1. Visible scene and visual characteristics.
2. Controlled primary/secondary image mood.
3. Musical tempo/energy/vocal/instrumental attributes.
4. Canonical top 1–3 genres with relevance scores; other/unknown when needed.
5. Confidence-like signals.

The prompt includes the complete category→slug hierarchy, anti-injection instructions, bans on artists/songs,
and a ban on inferring the uploader's emotions, personality, identity, intentions or mental state. `lonely`
can only describe the image atmosphere. Scores/confidence are model-reported relevance/uncertainty signals,
not calibrated probabilities. Actual accuracy improvement has not been demonstrated without a live quality study.

## Deterministic policy

Constants live in `internal/client/deterministic_queries.go`:

- Genre confidence <0.5: omit model genre, use mood/tempo/musical attributes.
- 0.5≤confidence<0.7: mood first + genre.
- Confidence ≥0.7: highest-score genre first + primary mood.
- Missing legacy confidence: preserve canonical genre + mood policy; no inferred confidence.
- A selected genre score <0.5 or primary other/unknown: mood/tempo fallback.
- Explicit known user preferred genres take precedence over model genre confidence.
- Stable descending score order; duplicate canonical names removed, retaining the highest scored occurrence.
- Second query uses the second genre and secondary mood; a single genre uses mood/language variation.
- InstrumentalOnly, vocal=instrumental, or instrumental preference ≥0.7 adds instrumental.
- Soft vocal is used in genre-free fallback if it fits. Vocal/either do not force instrumental.
- Tempo is used in genre-free fallback. If tempo is absent, music energy can select slow/fast;
  empty mood does not fabricate an atmosphere tag.
- Query length is bounded to 7 normalized words and 120 characters; duplicates are removed.

Primary other/unknown never uses raw_label, even if its relevance score is high. Scores sort genres but are
not fed into V2 ranking weights. ko/en alternate Korean/English keywords. Language keyword inclusion is a
separate boolean option; words in a query do not guarantee the language of retrieved music.

Example (`examples/enhanced-analysis.json`):

```text
korean dream pop dreamy music
english indie pop nostalgic music
```

Production keeps `RECOMMENDATION_QUERY_MODE=gemini` by default. Existing flags retain both generators;
`RECOMMENDATION_QUERY_MODE=deterministic` selects the prepared-query path with no second Gemini call.
`RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS=true` is the default. This work did not auto-switch production mode.

## Offline audit

From the sync project directory:

```sh
go run ./cmd/profile-benchmark \
  --dataset ../benchmark-results/recommendation-v2/saved-analyses.json \
  --out ../benchmark-results/music-profile-v2-new
```

No API key, ADC or external API is needed. The command refuses an existing output directory. It audits
12 saved analyses, stores query-intent projections and generated strings, and emits the effective enhanced
schema. These are legacy projections, not new v2 Gemini results or newly retrieved recommendations.
Human evaluation fields remain blank.

## Repeat consistency harness (prepared, not a live benchmark)

Supply independently collected new enhanced analysis results in a JSON manifest:

```json
[
  {"photo_id": "sunset", "analyses": ["REPLACE_WITH_OBJECT_1", "REPLACE_WITH_OBJECT_2", "REPLACE_WITH_OBJECT_3"]}
]
```

Replace each placeholder with an actual ImageAnalysis object; strings are not accepted by the harness.
Use different output directories for each live collection, when explicitly authorized. Do not copy one result
three times and call it model consistency. Twelve photos × three real analyses would require 36 separate
future image-inference calls; this implementation made zero.

```sh
go run ./cmd/profile-benchmark --repeats /path/to/saved-repeats.json --out /path/to/new-consistency-report
```

The reader makes no external calls. It requires exactly three valid v2 analyses per photo and measures:
primary mood pairwise agreement, secondary mood Jaccard, genre Top3 Jaccard, top-scored genre family agreement,
tempo agreement, music/mood energy and valence pairwise mean absolute differences, per-observed-genre score
population standard deviation/sample count, and confidence variation. Missing genres are not zero-imputed.
Pairwise MAE measures repeat disagreement, not error against a ground-truth label. Empty-set Jaccard is defined as 1.

The harness is unit-tested with synthetic fixtures; no real repeated model-consistency conclusion was produced.
Future human review should rate primary/secondary mood, genre Top3, energy, tempo and query usefulness.

## Validation and remaining work

Enums, category/name consistency, required numeric field presence, finite 0–1 ranges, mood mirroring,
genre mirroring/counts, duplicate genres/moods and raw-label usage are validated. Unit tests use fake transports;
all live integration flags must remain off in ordinary `go test ./...`.

Remaining: new-schema live Vertex quality checks, three-run consistency on 12 photos, human blind review,
then minimal deterministic/Gemini YouTube A/B after quota restoration. Keep V2 ranking fixed during this
comparison. Choose the final production query mode only after evaluating those results.
