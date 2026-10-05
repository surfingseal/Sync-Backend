# Direct MVP recommendation contract

Direct supports explicit production activation with runtime prerequisite guards; default remains legacy. No live
Gemini, YouTube, Google OAuth, playlist or iTunes call was used in this milestone.
Mobile OAuth files and resolver identity implementation remain unchanged.

## Selection and generation

The Direct API calls Service.RunMVP: one twelve-candidate Gemini generation;
search budget <=10, exact_metadata_mvp_v3 identity unchanged, one primary query
per track, no fallback or retry. Stop only when artist-aware selectable capacity
reaches five including two Korean-eligible tracks.
Resolve the eligible candidate pool, then reserve two ko/ko_en tracks, fill by
fit_score, keep max2 per artist, deduplicate video ID AND conservative canonical
artist/title. Unknown/other languages are rejected; no artist nationality inference.
Final output is <=5, ordered by fit, rank1..N. Korean shortage returns available eligible verified tracks with partial=true
and language_policy.satisfied=false; shortage never empties a usable pool.
Partial is also true whenever final count is below five.
No extra model/retrieval retry loop is added. Legacy experiment Run/default20/10
configuration is retained; the new prompt/schema version is separately identified.

Scene schema requires a nonempty Korean description; prompt requests10–25-character
noun phrase (up to30), observable facts, general location, clear event, supported
season/time. Decoder does not pretend to verify semantic hallucination. No scene
name/title duplicate. playlist.title is the only generated title, English guidance.
Lyric classification is model-classified metadata, not verified lyrics.

## Artwork composition

Real artwork provider injection (when a live Direct API is explicitly composed):

```go
artwork := client.NewAlbumArtworkResolver(nil, "", client.DefaultArtworkCountry)
recommender := &directapi.Recommender{
    Processor: processor,
    NewRunner: newRunner,
    Store: directapi.NewStore(),
    Artwork: artwork, // singleton per process, shared bounded cache
}
```

Nil Artwork means no enrichment and is used for offline/fixture tests. There is no
hidden provider construction/network fallback. The ordinary production server
still rejects Direct activation; this change does not silently enable that engine.

iTunes uses media=music, entity=song, limit10, countryUS. US is catalog context,
not language proof. Artist/title NFKC + case + punctuation + whitespace matching;
no fuzzy matching, artist alias guessing or remix/live/version stripping. Wrong
artist/title, missing artwork, non-Apple CDN, ambiguous differing releases fail
closed. Only provider-returned HTTPS artworkUrl100 is used; no guessed resolution
URL, no image download, no artwork bytes. Some genuine alias/transliteration and
multi-release matches therefore remain empty. Artwork is optional enrichment.

HTTP: body<=1MiB, request deadline1.5s, JSON result-count validation, bounded flights,
no retry. Enrichment: three workers, overall2s local budget for <=5 lookups, request
cancellation propagated. Singleton resolver enforces a20/minute window; rate/busy
failure skips artwork. The limit is local per process, not distributed.
Cache: normalized artist/title JSON pair, max1024, positive30days, negative1day,
expired cleanup then earliest-expiry eviction. Same-key requests are coalesced.
Transient errors/429/timeouts are not cached as day-long no-match results.

Metrics in checkpoint: lookup count, artwork wall latency, total recommendation
wall latency, available Korean candidates, **provider-global** cache hits/misses.
Global totals are labelled rather than falsely attributed to one concurrent
request. Resolver Snapshot additionally counts HTTP calls and failures. Logs
contain counts/times only, never song queries, images or credentials.

## Checkpoints and UI

Checkpoints defensively copy final scene, title, language, artwork, verified IDs,
ordered tracks, resolver evidence and timings. Playlist creation still uses only
verified YouTube IDs; UI metadata is not authorization evidence. IDs retain1h TTL,
process-memory persistence and existing capability risk. The client chooses
playlist.title or supplies a valid edited title to the unchanged playlist endpoint.

Fallback: album_artwork_url → youtube_thumbnail_url → local default. Preserve
thumbnail_url for old clients. Nullable album fields always serialize, no fake cover.
See [Android DTO/response examples](android-direct-api.md).

Primary sources: [Gemini structured output](https://ai.google.dev/gemini-api/docs/structured-output),
[Apple search parameters and caching/rate guidance](https://developer.apple.com/library/archive/documentation/AudioVideo/Conceptual/iTuneSearchAPI/Searching.html),
[Apple response fields](https://developer.apple.com/library/archive/documentation/AudioVideo/Conceptual/iTuneSearchAPI/UnderstandingSearchResults.html).

## Boundaries

No production engine activation/deployment; no recommendation benchmark rerun.
No OAuth handoff, App Link config, DB, lyrics scraping, Last.fm, Odesli or Moments.
Artwork/CDN semantic correctness and actual Korean model classifications need a
separate explicitly budgeted live evaluation; mocked tests only verify contracts.
Archived v1 E2E results lack scene/language metadata: they must not be relabelled
or presented as satisfying the new policy. Use a labelled synthetic v2 contract
fixture for offline UI work; original historical evidence remains untouched.

Artwork accepts explicit Latin/Hangul bilingual artist pairs in provider metadata or in the already identity-verified YouTube title for this exact song. No transliteration, learned aliases or same-title-only acceptance. Evidence-scoped cache keys prevent unsupported requests inheriting a cross-script match. Failed enrichment keeps the track and YouTube thumbnail.
