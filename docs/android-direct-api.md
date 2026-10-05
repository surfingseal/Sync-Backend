# Android Direct API contract — local milestone

Status: **NEEDS_BACKEND_CONTRACT_FIX** for native Android OAuth-to-playlist E2E.
Direct upload/UI integration and checkpoint playlist mock integration are available.
The missing piece is secure native app/session handoff after browser OAuth, not the recommendation algorithm.
This milestone makes no live provider calls.

## 1. Architecture and availability

Android photo URI → multipart image → backend preprocessing → Gemini Direct → exact resolver/cache → verified recommendations + server checkpoint → Android review → authenticated checkpoint playlist creation.
Android does not generate ImageAnalysis/taxonomy/queries or re-search artist/title.
`fit_score` is Gemini's image/music fit estimate, not user rating, provider popularity or calibrated probability.
Canonical track identity originates in Gemini and is checked against YouTube metadata by resolver v2.2; this is metadata evidence, not audio fingerprint verification.
Normal Direct limits remain 20 candidates / 10 final / 2 per artist. No tuning in this milestone.

`cmd/server` keeps the `legacy` default and rejects production `gemini_direct` startup through the existing readiness guard. Its new Direct route returns 503 until enabled through explicit local dependency injection. `router.NewWithDirect` accepts Direct dependencies only for `APP_ENV=development` and the typed `gemini_direct` engine; ordinary `router.New` never enables them.
Each request must get a fresh Direct runner/resolver because call counters are mutable. Cache and the thread-safe checkpoint store may be shared. The normal Direct runner must use `directmusic.DefaultConfig()`.

The new runnable local mechanism is **offline**, not another live experiment:

```sh
cd outputs/sync # relative to workspace root
# Run from the project directory; credentials and .env are not loaded.
go run ./cmd/android-contract-server --port 8081
```

It listens only on 127.0.0.1, responds with `X-Sync-Data-Mode: fixture`, validates/preprocesses uploads but replays the previously saved five-track E2E result regardless of the new image. It never instantiates Vertex, OAuth token exchange, YouTube search/metadata or write clients. Simulated playlist IDs are explicitly `simulation-only`, and no real playlist URL is returned. This is contract testing, not fresh analysis or OAuth validation. The fixture server's ordinary OAuth routes are unconfigured (configuration error); its mock write session is the fixed, non-secret `offline-session` cookie below. Never use that mock cookie for real authentication.

Android emulator can use `adb reverse tcp:8081 tcp:8081` with `http://127.0.0.1:8081/` as its debug base URL. Android cleartext HTTP permission is debug-only; production needs HTTPS. No production deployment is enabled here.

## 2. Existing legacy API remains unchanged

`POST /api/v1/recommend`: `application/json`, whole body ≤64 KiB.
Required `analysis` is the existing ImageAnalysis object; optional `preferences` defaults count 10, languages `ko`/`en`, vocal mode `mixed`. Count is 5–20. Analysis numeric ranges, enhanced schema/vocabulary and preference limits are validated by the existing model decoder. Existing basic-analysis compatibility is preserved. For an accepted basic request, see `internal/model/testdata/analysis.json`; wrap it in `{"analysis": ...}`. Enhanced clients should send their unchanged `/analyze` output as `analysis`.

```json
{"tracks":[],"requested_count":10,"returned_count":0,"partial":true}
```

Actual populated tracks retain `video_id`, `title`, `channel_title`, `thumbnail_url`, `duration_seconds`, `match_score`, `match_reasons`, `youtube_url`. **Legacy `title` remains the actual YouTube title.** Existing optional Direct debug fields remain optional. No recommendation ID is retrofitted onto this response.
Success 200; unsupported content type 415; malformed/invalid request 400; body too large 413; timeout 504; provider error 502; unavailable/quota/search budget/live-disabled 503. Existing common error envelope is preserved. Legacy playlist `{title, description?, privacy_status?, tracks:[{video_id}]}` remains supported and retains private/public/unlisted behavior; the new checkpoint protections do not retrospectively cover that legacy path.

## 3. Direct upload

`POST /api/v1/recommend/direct`, `multipart/form-data`:

| Field | Contract |
|---|---|
| image | Required, exactly one file; JPEG/PNG/WebP by actual content |
| request_id | Optional ASCII letters/digits/underscore/hyphen, 1–64 chars; echoed as X-Request-ID, correlation only, no idempotency guarantee |

Unknown or duplicated fields are rejected. No ImageAnalysis, genres, mood, language/country preference or fit_score is accepted. File ≤10 MiB (10,485,760 bytes); whole multipart request ≤10 MiB +64 KiB. Empty file and undecodable image are rejected. Original limits ≤10,000 per side /40 million pixels apply. Existing max dimension1920, target processed size2.5 MiB, processed hard limit6 MiB, EXIF orientation and resize/compression policy stay unchanged. A request has a 120-second outer deadline and forwards cancellation through preprocessing and Direct providers. In live local DI, existing per-provider timeout/policy still applies; this milestone does not call those providers.

```sh
curl -i -F 'image=@sample.jpg' -F 'request_id=android_debug_1' \
  http://127.0.0.1:8081/api/v1/recommend/direct
```

## 4. Response and variable count

HTTP 200, `Cache-Control: no-store`:

```json
{
  "recommendation_id":"rec_<43 opaque base64url characters>",
  "partial":true,
  "target_track_count":10,
  "tracks":[{
    "rank":1,
    "artist":"Norah Jones",
    "track_title":"Sunrise",
    "video_id":"cpqBhRymiEU",
    "youtube_title":"Sunrise",
    "thumbnail_url":"https://i.ytimg.com/vi/cpqBhRymiEU/hqdefault.jpg",
    "fit_score":0.95
  }]
}
```

Example only; response metadata comes from the resolver's provider result. `track_title` is canonical Gemini song title, `youtube_title` is real provider title. New response does not reuse legacy `title` for a different meaning. `rank` is final display order starting at1. An empty thumbnail is permitted. `fit_score` is numeric 0–1.

Tracks may contain 0,3,5,8,10 items. No placeholders or duplicates are padded. `partial = tracks.size < target_track_count`, where target is10 for this contract. Five returned fixture tracks therefore have partial=true even though the earlier five-track E2E target was fulfilled. Partial usable provider results return200. A successful run with zero verified tracks returns200 with `tracks:[]`, partial=true; show an empty state and disable playlist creation. A provider failure with no verified tracks is an error, not an empty success.

## 5. Checkpoint lifecycle

Opaque cryptographically random32-byte `rec_` IDs identify process-local server checkpoints. Store keeps ordered tracks (artist, canonical title, rank, fit score, actual video title/thumbnail), resolver evidence/version, created/expiry times. It does not retain image bytes, tokens or secrets. Expiry:1 hour. Capacity:128 active checkpoints; 503 at capacity rather than silently removing active results. Expired records may be cleaned at capacity; afterwards lookup is404 rather than410. Restart loses all checkpoints. No persistence added.

ID is a bearer capability for this local unauthenticated recommendation contract: keep it private, do not log or share it. It is not yet an app-user ownership system. Android stores the ID and the displayed response, never fabricates an ID or verified flag. The browser and native client must ultimately refer to the same authenticated user/session before writes. A production ownership/persistence design is still required before public deployment.

## 6. Checkpoint playlist request

Same `POST /api/v1/playlists` path, separate strict JSON variant:

```json
{
  "recommendation_id":"rec_<ID from the Direct response>",
  "title":"My Sync Playlist",
  "description":"Created by Sync.",
  "privacy_status":"private"
}
```

`title` required, trimmed, 1–100 Unicode characters; description optional ≤4,000. These names preserve existing repository conventions instead of introducing `name`/`privacy`. New checkpoint variant only allows private (default); legacy privacy options unchanged. `tracks`, `video_id`, `verified` and unknown client fields are rejected. Backend uses **all** checkpoint tracks in stored order, deduplicating video IDs by first appearance. Selection/reordering is not introduced in this milestone.

Requires existing HttpOnly `sync_session` cookie and valid server TokenStore entry. Handler never handles tokens. Existing OAuth service builds persisted OAuth TokenSource, refreshes when needed without discarding an existing refresh token, then the existing playlist service creates and sequentially inserts. Partial failures preserve playlist and successful items. No rollback or write retry.

HTTP201 (including partial and successful replay), existing response:

```json
{
 "playlist":{"id":"<provider ID>","title":"My Sync Playlist","privacy_status":"private","url":"https://www.youtube.com/playlist?list=<provider ID>"},
 "submitted_count":5,"requested_count":5,"added_count":4,"failed_count":1,"partial":true,
 "items":[{"video_id":"<stored ID>","status":"added","playlist_item_id":"<provider item ID>","attempted":true}]
}
```

Example abbreviates items; real response has one result per requested unique video. `submitted_count` is checkpoint track count; `requested_count` is unique count. Failed items have `error_code`, and unattempted items have attempted=false. No access/refresh token or session value is returned.

Offline simulation (the cookie is a mock value, not copied from a browser):

```sh
curl -H 'Content-Type: application/json' -b 'sync_session=offline-session' \
  -d '{"recommendation_id":"<ID from upload>","title":"Offline contract test"}' \
  http://127.0.0.1:8081/api/v1/playlists
```

## 7. OAuth boundary — native handoff gap

Actual endpoints:

- `GET /api/v1/auth/google/status`: configured service with no valid session returns connected=false; connected state verifies channels.list(mine=true).
- `GET /api/v1/auth/google`: browser302 to Google. No `/start` endpoint.
- `GET /api/v1/auth/google/callback`: server exchanges code and saves tokens; returns connection result, never tokens.
- `DELETE /api/v1/auth/google`: local disconnect; no grant revoke.

Existing authorization uses youtube scope, offline access, include_granted_scopes, crypto32-byte state, HttpOnly/Lax cookies, pending state expiry10minutes and PKCE. Pending state is consumed once; repeated callback/state is intentionally rejected400. Don't reopen or reuse callback URLs; after completion check status from the established browser session. Do not send authorization code to Android logs or diagnostics.

**Current callback does not redirect to an Android deeplink/app link.** `sync_session` is set for the backend browser origin and HttpOnly. Chrome Custom Tabs use browser state/cookies ([official documentation](https://developer.chrome.com/docs/android/custom-tabs)); Retrofit/OkHttp has its own client/cookie store. Therefore opening Custom Tab does **not** by itself authenticate subsequent Retrofit status or playlist calls. This follows from the two separate HTTP clients. A cookie jar on OkHttp only preserves cookies received by OkHttp; it does not import Chrome cookies.

Intended status → browser OAuth → app return → status → playlist flow remains blocked at browser→native session handoff. This milestone deliberately preserves OAuth semantics and does not invent an endpoint/deeplink, expose HttpOnly cookies/tokens, embed login in WebView or ask users to copy cookies. Full Android E2E requires a separately reviewed short-lived single-use app-session handoff (or another explicit native authorization design). Until that exists, real OAuth/playlist integration remains browser-only. A302 authorization redirect should be opened in the browser, not followed as Retrofit JSON.

## 8. Retry and state machine

```
IDLE → IMAGE_SELECTED → RECOMMENDING → RECOMMENDATION_READY
  → OAUTH_REQUIRED / READY_TO_CREATE → CREATING_PLAYLIST → PLAYLIST_CREATED
errors: RECOMMENDATION_FAILED / AUTH_FAILED / PLAYLIST_FAILED
```

Partial recommendations are ready states; zero tracks is an empty state. Persist ID in app state; do not assume exactly10 tracks. Android app return currently needs the missing handoff design.

Recommendation retry can generate a new ID and incur fresh Gemini/resolver costs when live mode is later enabled. `request_id` doesn't deduplicate it. Playlist failure must **not** upload the image again. Retain recommendation_id and retry only the exact playlist body when safe.

Checkpoint playlist deduplication key = server-side hash(session + recommendation_id), with normalized body fingerprint. Concurrent identical requests wait (cancellable), completed result is replayed; changed title/description/privacy gives409. Replay rechecks authenticated session. Partial result replay does not re-add failed tracks automatically. Auth/config failures before writes permit reconnect/retry. Other failed creations retain a conservative marker: duplicate returns `PLAYLIST_CREATE_RESULT_UNKNOWN` without another write. Inspect YouTube after an ambiguous timeout. Do not automatically retry writes through an interceptor.

Protection is in-memory only, max128 write markers per process, no marker eviction that could repeat writes. Server restart, expiry, session replacement, multiple server processes and direct legacy track requests are outside the guarantee. Do not present exactly-once delivery as assured. Restart means the old ID is gone; do not regenerate automatically after an unconfirmed write. Playlist orchestration keeps the existing120-second timeout.

## 9. Error envelope

```json
{"error":{"code":"IMAGE_REQUIRED","message":"image file is required"}}
```

| HTTP | Direct upload / checkpoint playlist codes |
|---|---|
| 400 | IMAGE_REQUIRED, INVALID_IMAGE, INVALID_REQUEST, INVALID_RECOMMENDATION_ID |
| 401 | YOUTUBE_NOT_CONNECTED, YOUTUBE_AUTH_EXPIRED (playlist only) |
| 403 | INVALID_REQUEST_ORIGIN (explicit cross-origin browser write) |
| 404 | RECOMMENDATION_NOT_FOUND |
| 408 | REQUEST_CANCELED |
| 409 | NO_VERIFIED_TRACKS, PLAYLIST_REQUEST_CONFLICT |
| 410 | RECOMMENDATION_EXPIRED |
| 413 | IMAGE_TOO_LARGE, REQUEST_TOO_LARGE |
| 415 | UNSUPPORTED_IMAGE_TYPE, UNSUPPORTED_CONTENT_TYPE |
| 500 | INTERNAL_ERROR, OAUTH_CONFIGURATION_ERROR |
| 502 | DIRECT_RECOMMENDATION_FAILED, INVALID_RECOMMENDATION_RESPONSE, playlist provider errors, PLAYLIST_CREATE_RESULT_UNKNOWN |
| 503 | DIRECT_RECOMMENDATION_UNAVAILABLE, CHECKPOINT_CAPACITY_EXCEEDED, YOUTUBE_QUOTA_EXCEEDED, YOUTUBE_SEARCH_BUDGET_EXCEEDED |
| 504 | RECOMMENDATION_TIMEOUT, PLAYLIST_TIMEOUT |

Raw provider errors/keys/tokens aren't serialized. Direct image recommendation is unauthenticated in this local contract; playlist requires OAuth. Client reads error body separately from success payload.

## 10. Image lifecycle

Upload streams multipart in bounded memory; no ParseMultipartForm disk spill. Existing processing decodes raster, corrects EXIF orientation and re-encodes, removing original EXIF/GPS metadata. No new image file/database/cloud storage or raw image checkpoint. Request buffers become eligible for garbage collection after processing/request completion; this is not an explicit secure memory wipe. Gemini receives processed bytes when a live local runner is explicitly used; no claim is made about provider retention policy. Current offline server does not send them anywhere.

## 11. Kotlin / Retrofit (Gson)

```kotlin
interface SyncApi {
    @Multipart @POST("api/v1/recommend/direct")
    suspend fun recommendDirect(
        @Part image: MultipartBody.Part,
        @Part("request_id") requestId: RequestBody? = null
    ): Response<DirectRecommendationResponse>

    @GET("api/v1/auth/google/status")
    suspend fun googleAuthStatus(): Response<AuthStatusResponse>

    @POST("api/v1/playlists")
    suspend fun createPlaylist(@Body body: CheckpointPlaylistRequest): Response<PlaylistResponse>
}

data class DirectRecommendationResponse(
    @SerializedName("recommendation_id") val recommendationId: String,
    val partial: Boolean,
    @SerializedName("target_track_count") val targetTrackCount: Int,
    val tracks: List<RecommendedTrack>
)
data class RecommendedTrack(
    val rank: Int, val artist: String,
    @SerializedName("track_title") val trackTitle: String,
    @SerializedName("video_id") val videoId: String,
    @SerializedName("youtube_title") val youtubeTitle: String?,
    @SerializedName("thumbnail_url") val thumbnailUrl: String?,
    @SerializedName("fit_score") val fitScore: Double?
)
data class CheckpointPlaylistRequest(
    @SerializedName("recommendation_id") val recommendationId: String,
    val title: String, val description: String? = null,
    @SerializedName("privacy_status") val privacyStatus: String = "private"
)
data class AuthStatusResponse(val connected: Boolean, val youtube: ChannelInfo?)
data class ChannelInfo(
    @SerializedName("channel_id") val channelId: String?,
    @SerializedName("channel_title") val channelTitle: String?
)
data class PlaylistInfo(
    val id: String, val title: String,
    @SerializedName("privacy_status") val privacyStatus: String, val url: String
)
data class PlaylistItem(
    @SerializedName("video_id") val videoId: String, val status: String,
    @SerializedName("playlist_item_id") val playlistItemId: String?,
    @SerializedName("error_code") val errorCode: String?, val attempted: Boolean
)
data class PlaylistResponse(
    val playlist: PlaylistInfo,
    @SerializedName("submitted_count") val submittedCount: Int,
    @SerializedName("requested_count") val requestedCount: Int,
    @SerializedName("added_count") val addedCount: Int,
    @SerializedName("failed_count") val failedCount: Int,
    val partial: Boolean, val items: List<PlaylistItem>
)
data class ErrorEnvelope(val error: ApiError)
data class ApiError(val code: String, val message: String)
```

URI streaming multipart (OkHttp; use on an IO dispatcher, retain read permission):

```kotlin
fun imagePart(resolver: ContentResolver, uri: Uri): MultipartBody.Part {
    val mime = resolver.getType(uri) ?: error("Unknown MIME type")
    require(mime in setOf("image/jpeg", "image/png", "image/webp"))
    val displayName = resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME),
        null, null, null)?.use { cursor ->
        if (cursor.moveToFirst()) cursor.getString(0) else null
    } ?: "upload"
    // Display metadata only; never derive a filesystem path from the URI.
    val safeName = displayName.substringAfterLast('/').substringAfterLast('\\')
        .replace("\r", "").replace("\n", "").take(128).ifEmpty { "upload" }
    val body = object : RequestBody() {
        override fun contentType() = mime.toMediaType()
        override fun writeTo(sink: BufferedSink) {
            val input = resolver.openInputStream(uri) ?: error("Cannot open image")
            input.use { source -> source.source().use { sink.writeAll(it) } }
        }
    }
    return MultipartBody.Part.createFormData("image", safeName, body)
}
```

Client-side size check using OpenableColumns.SIZE is optional since some providers omit it; server remains authoritative and caps chunked uploads too. Don't manually set multipart Content-Type/boundary. Use read timeout above the server operation limit for debug (e.g.150s), no automatic retry of unconfirmed writes. Do not log request bodies, headers or cookies containing credentials. Debug fixture cookie, if used, must be restricted to the fixture host and never bundled in release builds. Real session provisioning for Retrofit is still pending.

## 12. Android handoff checklist

- [ ] Choose photo and retain URI read permission.
- [ ] Upload one `image` multipart part; show loading/cancellation.
- [ ] Render variable-length tracks and optional/empty thumbnails.
- [ ] Treat partial200 as usable; handle empty array with no playlist button.
- [ ] Save recommendation_id and response; never re-search artist/title.
- [ ] Check OAuth status with an authenticated app session.
- [ ] Open the actual `/api/v1/auth/google` URL in Custom Tab/browser.
- [ ] **Resolve backend app-return and secure browser/native session handoff first.**
- [ ] After handoff, recheck status; don't replay callback URL.
- [ ] Send checkpoint playlist request; default private; no tracks or verified flags.
- [ ] Handle partial playlist results and unconfirmed creation separately.
- [ ] Reuse identical request on eligible retry; don't regenerate recommendations.
- [ ] Open playlist.url only for a real nonempty returned URL.
- [ ] Handle checkpoint expiry/restart explicitly; no automatic image rerun after ambiguous writes.

OpenAPI/Swagger source was not found in the current project/workspace outputs; no new Swagger framework was introduced. Full production activation still requires the existing readiness gate and the separate OAuth handoff design.
