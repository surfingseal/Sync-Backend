# YouTube identity resolver MVP

Policy version: `exact_metadata_mvp_v3`.

On a cache miss, use one relevance-ordered `artist + title` search, without quoted search operators. Request at most five results and enforce the five-result bound again before metadata lookup. Deduplicate IDs, batch videos.list once, and inspect metadata in search order. No fallback query or application-level retry. Existing request deadline, per-run search budget and upstream quota/error handling remain in place. Positive cache entries are revalidated against current metadata and policy. Prior negative policy entries are not migrated; old positive mappings are eligible only after current-policy validation.

Comparison strings use NFKC, whitespace and punctuation normalization; source title/channel/description remain unchanged. Remove known MV/Official Video/Official Audio/Music Video labels only at prefix/suffix boundaries. Parse explicit artist/title delimiters (spaced hyphen/dashes/underscore), bilingual parenthetical artist credits, quoted titles, or an exact requested artist prefix. Do not infer an artist from an arbitrary mention.

Artist matching prefers normalized whole-credit equality. Latin/Hangul parenthetical bilingual credits require whole-segment equality and supporting release/channel evidence; no transliteration or runtime alias generation. Solo artist requests do not collapse feat/X/guest credits. Existing explicitly requested multi-artist matching and video/channel-bound operator aliases remain available, without adding aliases.

Title matching requires equality of the comparison title segment. A whole parenthetical localized title is accepted only with separate artist evidence, release evidence and description repetition of the complete provider title segment. General substring containment, fuzzy distance and title-only acceptance are excluded. Existing bounded HQ/year formatting handling remains. Keep meaningful version words; unrequested cover/live/remix/acoustic/demo/karaoke/sped-up/slowed/nightcore (and existing version markers) fail. Existing music-category, public, embeddable, duration, region and AI/transformed-content guards remain.

Only accepted candidates are ordered:

1. Named artist / Topic / VEVO channel evidence, or explicit same-channel copyright/distributor statements.
2. Compatible release/copyright metadata, including the provider licensed flag as a supporting signal.
3. View count, descending.
4. Original search order, stable even when videos.list returns a different order.

Views never influence artist/title identity or eligibility. Channel names, descriptions and licensedContent are metadata heuristics, not independent ownership or audio-fingerprint verification. A fan upload cannot become official just through a decorative title label. Self-reported copyright/distributor descriptions can be spoofed. Different releases with identical artist/title and no version markers may still be ambiguous. Other bilingual script pairs, complex credit layouts or missing localized-title corroboration remain unresolved. No padding or unsafe fallback.

## Validation (2026-10-05)

- Recorded audit: 4 positive original-release candidates accepted (Colde, Yerin Baek, two Crush uploads); 5 live/cover candidates rejected.
- Synthetic formatting/version/collaboration negatives, top-five cap, one-search policy, ranking priorities, batch dedupe and raw metadata checks pass.
- `gofmt`, `go test ./...`, `go vet ./...` pass.
- `go test -race ./internal/directmusic ./internal/directe2e ./internal/directapi` passes.
- Full tests require localhost listeners for existing httptest fake servers; no live integrations were enabled.
- Live Gemini/YouTube/OAuth/iTunes/Last.fm/playlist calls: zero. Stored audit evidence is replayed, not a fresh live validation.

OAuth, playlist production flow, Gemini generation, Last.fm, engine defaults and production readiness guard are unchanged by this task. E2E fake query parsing and expected search count are updated to reflect one query per track. No commit/push/deploy performed.
