# Direct MVP production activation

The server defaults to `legacy`. Explicit `RECOMMENDATION_ENGINE=gemini_direct`
now activates the existing independent multipart `POST /api/v1/recommend/direct`
and its shared recommendation/checkpoint playlist wiring. The JSON legacy
`POST /api/v1/recommend`, image analysis and OAuth routes keep their handlers.
No Gemini prompt, identity matcher, artwork matcher or language policy changes.

## Guard audit

The previous `ValidateServerEngine` rejected every Direct selection. It did not
read benchmark artifacts. Its raw-image compatibility warning predated the
separate multipart route. `router.NewWithDirect` also discarded production
injection, and `cmd/server` never constructed a Direct recommender.

The historical `directbench.Evaluate` is an independent experiment gate: minimum
20 cold-cache images, >=95% eight-track rate, >=80% ten-track rate, average >=9,
verification >=80%, API error <=2%, p50 <=20s/p95 <=30s and safety/resource checks.
It is NOT invoked by server startup; a single successful five-track MVP run does
not prove these historical benchmark thresholds passed. Those tools are unchanged.

Activation uses explicit opt-in and the current independent five-track MVP
contract, its offline safety/contract regressions and saved live evidence.
This is operational integration readiness, not a guarantee of recommendation
quality, identity perfection or production reliability at scale.

## Fail-closed prerequisites

Before listening: known engine; Direct requires live data, enabled live search,
nonempty YouTube key, valid Vertex project/location/model/thinking/timeout,
positive search budget. Vertex ADC/SDK, YouTube SDK and processor construction
must succeed. Assembled Direct dependencies and resolver policy must be valid.
Missing OAuth/mobile setup retains the existing feature-specific disabled policy.
SDK construction checks credential initialization, not live IAM/API-key validity.

## Render environment

```text
APP_ENV=production
RECOMMENDATION_ENGINE=gemini_direct
RECOMMENDATION_DATA_MODE=live
YOUTUBE_LIVE_SEARCH_ENABLED=true
YOUTUBE_SEARCH_MAX_CALLS_PER_RUN=10
GOOGLE_CLOUD_PROJECT=graduation-exhibition-510404
GOOGLE_CLOUD_LOCATION=global
VERTEX_MODEL=gemini-3.8-flash
VERTEX_THINKING_LEVEL=MEDIUM
VERTEX_TIMEOUT_SECONDS=60
```

Supply `YOUTUBE_API_KEY` and valid
Vertex ADC via Render secret settings; never a developer Mac absolute path.
Render supplies `PORT`. Keep current OAuth client/secret, HTTPS redirect URL,
secure cookies, app link values and certificates. No OAuth changes are needed
for the recommendation route. No Apple secret is required.

The 60-second Vertex timeout above matches the successful live validation process
setting; code/.env defaults were not changed. Direct handler deadline remains120s.
Each request owns resolver counters and an in-memory cache (<=12 candidates),
searches capped at min(configured budget,10), no fixture/replay substitution.
Artwork metadata cache is shared and bounded; checkpoint store is shared within
this process and used by the existing private checkpoint playlist variant.

## Limitations / rollback

No new live calls were made for this integration. Startup tests validate wiring,
not live provider health. Single-photo evidence cannot establish p95 or failure
rates. Public Direct has no new per-user rate limiting; protect quota operationally.
Request-local identity cache does not reuse search results across requests.
OAuth sessions, checkpoints and write replay protection are in-memory: restart
and multiple replicas require care. Language classification is model-generated;
Korean insufficiency remains partial. Artwork can be null. Set engine back to
legacy to disable Direct without changing the legacy route. No commit/push/deploy
is included in this change.
