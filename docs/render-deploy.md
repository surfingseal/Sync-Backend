# Render deployment readiness — Sync

Scope: run the current **legacy production server** and serve unauthenticated
`GET /health`. No deployment was performed. No recommendation architecture,
Gemini model/prompt, OAuth semantics, Android App Link or playlist logic changed.

## Service setup

| Render setting | Value |
|---|---|
| Service type | Web Service |
| Language / runtime | Go native runtime |
| Root Directory | The directory containing this module's go.mod |
| Build Command | `go build -o bin/server ./cmd/server` |
| Start Command | `./bin/server` |
| Health Check Path | `/health` |
| Production entrypoint | `cmd/server/main.go` |

This workspace contains the module at `outputs/sync`. If your remote Git repo
contains the whole workspace, set Root Directory to `outputs/sync`. If you publish
only the Sync module as repository root, leave Root Directory empty. Commands
above are relative to the module directory. The source repository target is `https://github.com/surfingseal/Sync-Backend`.
Link that repository to Render during service setup.
No Blueprint was added: a single service with dashboard-managed secrets needs no
additional configuration file.

## Port, health and process lifecycle

The server uses OS `PORT` when present, otherwise local default8080. Render's
assigned port is honored; `PORT=10000` creates `Addr=":10000"`, listening on all
interfaces rather than localhost. Empty, non-integer and values outside1–65535
fail configuration validation. Don't set PORT to a hostname or URL.
Render requires binding on all interfaces and normally provides PORT10000.
See [Render Web Services / port binding](https://render.com/docs/web-services#port-binding).

`/health` always returns HTTP200 with `{"status":"ok"}` once the server is running.
No session, Gemini, YouTube, OAuth, database or provider health probe is involved.
It is process liveness, not a promise that recommendation providers are usable.
Startup still loads configuration and initializes the Vertex SDK **before**
listening; malformed/missing ADC or project configuration prevents startup.

The server has read-header timeout5s, idle timeout60s and existing SIGINT/SIGTERM
handling. On a signal it calls http.Server.Shutdown with an independent timeout
based on the existing operation limits (max +5s), closing on shutdown failure.
No short write timeout was introduced for sequential playlist operations.

After deployment, check:

```sh
curl -i https://<render-service>.onrender.com/health
```

Expected200 and JSON status ok. HTTPS terminates at Render; the backend serves
plain HTTP internally. Do not use the loopback-only android-contract-server or
live E2E/debug commands as the Render start command.

## Go version

`go.mod` declares **Go1.26.0**. Local verification uses **Go1.27.1**. The directive
and dependencies are unchanged. Native Render uses the latest stable Go1.x and
does not support pinning it to a specific version; therefore no unsupported
`GO_VERSION` setting is prescribed. Verify the deployed toolchain satisfies the
module and dependencies in build logs. Exact toolchain pinning would require a
separate Docker decision, which is outside this milestone.
[Render supported languages](https://render.com/docs/language-support).

## Environment inventory (real code names)

No dotenv library loads `.env`: configure Render Environment Variables and Secret
Files. Defaults below are behavior, not secret values. No real credentials are
included here.

### A. Startup / production service

| Name | Required? | Secret? | Current behavior |
|---|---|---|---|
| PORT | Platform-managed; omit locally | No | Render supplies it; local8080 fallback |
| APP_ENV | Set production on Render | No | Otherwise defaults development |
| RECOMMENDATION_ENGINE | Optional; retain legacy | No | Empty defaults legacy; gemini_direct startup rejected |
| GOOGLE_CLOUD_PROJECT | **Required to start** | No | Config.Load rejects missing project |
| GOOGLE_APPLICATION_CREDENTIALS | **Required for chosen Render Secret File ADC setup** | File content yes; path no | Google SDK ADC discovery reads file; not read directly by Config.Load |
| GOOGLE_CLOUD_LOCATION | Optional | No | Default global |
| VERTEX_MODEL | Optional | No | Existing default gemini-3.8-flash |
| VERTEX_THINKING_LEVEL | Optional | No | Existing default MEDIUM |
| VERTEX_TIMEOUT_SECONDS | Optional | No | Existing default20 |
| VERTEX_RETRY_MODE / VERTEX_RETRY_ATTEMPTS | Optional | No | Existing retry defaults unchanged |
| RECOMMENDATION_DATA_MODE | Optional | No | production defaults live; local development defaults fixture |
| YOUTUBE_API_KEY | Not needed to start or serve health; required for **legacy live search** | **Yes** | Empty key builds an unavailable search client; health still works |
| YOUTUBE_LIVE_SEARCH_ENABLED | Optional | No | production defaults true; health does not search |
| RECOMMENDATION_REPLAY_PATH | Only if explicitly choosing replay mode | No | Missing path in replay mode fails config |

Vertex settings are startup dependencies **even with legacy**, because main
always constructs the image analyzer. They must not be mislabeled Direct-only.
Providing ADC does not mean startup/health performs Gemini inference.

Other existing optional settings (retain defaults; no tuning for deployment):

- IMAGE_MAX_DIMENSION, IMAGE_HARD_MAX_BYTES
- YOUTUBE_REGION, YOUTUBE_RELEVANCE_LANGUAGE, YOUTUBE_SEARCH_QUERY_COUNT,
  YOUTUBE_SEARCH_MAX_RESULTS, YOUTUBE_SEARCH_MAX_CALLS_PER_RUN,
  YOUTUBE_SEARCH_CACHE_TTL_SECONDS
- RECOMMENDATION_COUNT,
  RECOMMENDATION_QUERY_MODE, RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS,
  RECOMMENDATION_RANKER, RECOMMENDATION_ADAPTIVE_SAFETY_MARGIN,
  RECOMMENDATION_MIXED_QUERY_KEYWORD,
  RECOMMENDATION_MIN_MEDIUM_ESTABLISHED,
  RECOMMENDATION_MIN_LANGUAGE_COVERAGE_PER_PREFERENCE,
  RECOMMENDATION_REQUIRE_RELEASE_CONFIDENCE,
  RECOMMENDATION_ALLOWED_RELEASE_CONFIDENCE,
  RECOMMENDATION_Q1_ORDER, RECOMMENDATION_Q2_POPULARITY_ORDER

These names come from current config. Invalid explicit settings can fail startup;
there is no need to copy every local experiment variable into Render.

### B. Gemini Direct only

No additional Direct-only environment variable is required for this **legacy
Render target**. RECOMMENDATION_ENGINE selects an engine, but **gemini_direct
remains blocked by the existing production readiness guard**. Local Direct
benchmark flags/environment do not enable production Direct. Do not force this
engine for deployment. The shared Vertex/ADC settings above are required earlier
for the current legacy image analyzer as well.

### C. Google OAuth / playlist only

| Name | Required? | Secret? |
|---|---|---|
| GOOGLE_OAUTH_CLIENT_ID | Required to enable OAuth | Treat as configuration; not a client secret |
| GOOGLE_OAUTH_CLIENT_SECRET | Required to enable OAuth | **Yes** |
| GOOGLE_OAUTH_REDIRECT_URL | Required correct deployed callback to use OAuth | No |
| GOOGLE_OAUTH_SCOPE | Optional; existing youtube scope default | No |
| OAUTH_COOKIE_SECURE | Optional; production defaults true | No |
| OAUTH_FORCE_CONSENT | Optional; production defaults false | No |

Without OAuth settings, the server logs that OAuth is disabled and still serves
health/analyze/recommend. Missing/invalid OAuth does not block startup. When
later enabling it, register the exact
`https://<render-service>.onrender.com/api/v1/auth/google/callback` with Google
Cloud and use the same GOOGLE_OAUTH_REDIRECT_URL. Don't copy the localhost URL to
a deployed OAuth configuration. Keep production secure cookies. No OAuth login,
callback exchange or playlist write was tested against live services here.
TokenStore remains in memory, so restart loses browser connections; this readiness
milestone does not implement durable sessions, native handoff or production
playlist guarantees.

## Service account and secrets

The Vertex SDK uses ADC. Existing code does **not** accept service-account JSON
through a custom JSON-content environment variable. A file is not intrinsic to
all ADC mechanisms, but Render's chosen setup supplies one because this app does
not configure an alternate Render-to-Google workload identity mechanism.

In Render Dashboard, upload the existing JSON as a **Secret File** with a generic
filename, e.g. `service-account.json`. Set GOOGLE_APPLICATION_CREDENTIALS to the
mounted path `/etc/secrets/service-account.json`, and set GOOGLE_CLOUD_PROJECT to
the intended project. The developer Mac absolute path does not exist on Render.
Do not put JSON contents in GOOGLE_APPLICATION_CREDENTIALS: its value is a path.
We did not read, print, modify or upload the real file during this milestone.
See [Render Environment Variables and Secret Files](https://render.com/docs/configure-environment-variables#secret-files).

Use backend-only Render secrets for YOUTUBE_API_KEY and
GOOGLE_OAUTH_CLIENT_SECRET when enabling those features. Service account, public
YouTube search key and user OAuth credentials remain distinct. GEMINI_API_KEY,
GOOGLE_API_KEY and LASTFM_API_KEY are not required by this production startup;
don't carry over unrelated experiment keys.

The module `.gitignore` ignores `.env`, `.env.*` except `.env.example`, service
account filename patterns, and bin output. `.env` is not read or changed here.
Ignore rules protect this source checkout, but no claim is made that a remote
repository has never tracked secrets. If previously tracked,
ignore rules alone do not remove them from repository history. No credential is
hardcoded by this change.

## Local readiness verification

- Build the actual production entrypoint to bin/server.
- Tests cover PORT10000, absent PORT→8080, invalid PORT rejection and dependency-free health200.
- gofmt, go test ./..., go vet ./... and relevant race tests.
- Production binary startup, local health200, SIGTERM shutdown using an explicitly
  fake ADC fixture and no real API credentials. No analyze/recommend/OAuth/playlist
  endpoint calls are made.
- No deployment, Gemini inference, YouTube call, OAuth exchange or playlist write.

Repository readiness does not replace supplying valid Render secrets and linking
source code. The Render health-only goal does not certify native Android OAuth,
Gemini Direct production activation or production recommendation quality.
