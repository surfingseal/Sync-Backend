# Android native OAuth handoff

Backend implementation status: **DEBUG_ANDROID_VALUES_AVAILABLE_DEVICE_VALIDATION_PENDING**.
The supplied applicationId is `com.example.sync_app`; the supplied certificate is
Debug SHA-256. Local Git-ignored `.env` holds these values. No deployment or device
App Link/login validation is implied. Release/Play App Signing fingerprints remain
separate, not yet supplied.

## Boundaries and configuration

Google Web Client, youtube scope, server-side code exchange and refresh-token
preservation are unchanged. Google redirect remains:
`https://sync-backend-c2lv.onrender.com/api/v1/auth/google/callback`.
Do not register the Android completion URL as a Google callback.

Optional feature configuration:

```dotenv
MOBILE_APP_LINK_BASE_URL=https://sync-backend-c2lv.onrender.com
ANDROID_PACKAGE_NAME=<actual applicationId>
ANDROID_APP_SIGNING_SHA256=<actual colon-separated SHA-256 certificate fingerprint>
```

The base must be HTTPS, contain no credentials/query/fragment/path, and match the
origin of GOOGLE_OAUTH_REDIRECT_URL. Both Android fields are required. Missing or
invalid values give MOBILE_AUTH_UNAVAILABLE (503) on mobile routes and assetlinks;
health and browser OAuth remain available. No redundant APP_BASE_URL is added.
TTL constants: transaction 300s, handoff 120s, Sync session 86400s. The transaction
must still be valid at exchange, so the effective handoff TTL is the shorter of
120s and the remaining transaction lifetime. Expiry is not sliding. Server Google
credentials also expire under the existing 24h policy; missing credentials require
reconnecting even if a Sync session has time remaining.

## Endpoint contract

| Endpoint | Request | Success |
|---|---|---|
| POST /api/v1/auth/google/mobile/start | `{ "code_challenge": "<S256>", "code_challenge_method": "S256" }` | 200 `{ "authorization_url": "<browser bootstrap URL>", "expires_in": 300 }` |
| GET /api/v1/auth/google/mobile/authorize?transaction=… | Browser only | 302 to existing Google authorization URL; HttpOnly/Lax/Secure state cookie |
| GET /api/v1/auth/google/callback | Existing Google callback | Web: existing 200 connection JSON + browser session cookie. Mobile: 302 to configured `/auth/android/complete?code=…` |
| POST /api/v1/auth/mobile/exchange | `{ "code": "<handoff>", "code_verifier": "<original verifier>" }` | 200 `{ "session_token": "<Sync opaque bearer>", "token_type": "Bearer", "expires_in": 86400 }` |
| GET /api/v1/auth/google/status | Sync Bearer or browser cookie | Existing connected/channel JSON |
| POST /api/v1/playlists | Sync Bearer or browser cookie; existing request | Existing playlist response/partial-failure behavior |
| DELETE /api/v1/auth/mobile/session | Sync Bearer | 200 `{ "logged_out": true }`; revoke just this Sync session |
| DELETE /api/v1/auth/google | Sync Bearer or browser cookie | Existing `{ "connected": false }`; delete Google credential locally and revoke associated Sync sessions |
| GET /.well-known/assetlinks.json | None | Direct application/json, no redirect, configured association only |

All authorization responses use no-store/no-referrer. Completion fallback HTML
contains no code reflection, scripts, external resources or analytics, and uses a
restrictive CSP. It cannot finish login without the Android app/verifier.

**Browser bootstrap is intentional:** Retrofit cannot set Chrome's HttpOnly state
cookie. Returning a Google URL directly would bypass the existing cookie CSRF
check. Open authorization_url in Custom Tab/external browser; the same-origin
bootstrap sets the cookie and redirects using the existing provider. It also
attaches the actual browser's previous session for existing refresh preservation.
Bootstrap is single-use. Do not follow it through Retrofit. A mobile callback does
not issue a new browser session cookie; the credential is linked to the handoff.
If it replaced an existing browser credential, that old session is invalidated by
the existing OAuth service.

Google's OAuth PKCE verifier remains backend-owned and distinct from the native
handoff verifier. Native verifier never goes to Google, nor to mobile/start.

## Android implementation example (integration guidance only)

Generate one verifier per pending login; preserve it securely across Custom Tab
and process lifecycle. Serialize pending logins; do not accept unsolicited App
Links or a code without the original pending verifier. Clear pending verifier on
success/cancel/expiry. Generate a new flow after expiry, never reuse a callback.

```kotlin
val random = ByteArray(32).also { java.security.SecureRandom().nextBytes(it) }
val flags = android.util.Base64.URL_SAFE or android.util.Base64.NO_WRAP or android.util.Base64.NO_PADDING
val verifier = android.util.Base64.encodeToString(random, flags)
val challenge = android.util.Base64.encodeToString(
    java.security.MessageDigest.getInstance("SHA-256")
        .digest(verifier.toByteArray(Charsets.US_ASCII)), flags)
// Save verifier in protected pending-login state, never log it.
val start = api.mobileStart(MobileStart(challenge, "S256"))
androidx.browser.customtabs.CustomTabsIntent.Builder().build()
    .launchUrl(activity, android.net.Uri.parse(start.authorization_url))
```

Use Custom Tabs/external browser, never WebView OAuth. Handle onCreate and
onNewIntent. Check scheme=https, configured host, **exact** path
/auth/android/complete; require exactly one nonempty code and a pending verifier.
Do not forward arbitrary deep-link hosts or parameters to the exchange endpoint.

```kotlin
// After validating the App Link and loading the original pending verifier:
val session = api.mobileExchange(MobileExchange(code, verifier))
// session.session_token is a Sync credential, NOT a Google access token.
// Keep it in app-private storage encrypted with an Android Keystore-backed key.
// Exclude it and pending-login state from backups; never log headers or bodies.
val auth = "Bearer ${session.session_token}"
val status = api.googleStatus(auth)
// connected=true before explicit user-confirmed playlist creation.
```

Retrofit contract (DTO properties deliberately use the wire field names here):

```kotlin
interface SyncApi {
    @POST("api/v1/auth/google/mobile/start") suspend fun mobileStart(@Body body: MobileStart): MobileStartResponse
    @POST("api/v1/auth/mobile/exchange") suspend fun mobileExchange(@Body body: MobileExchange): MobileSessionResponse
    @GET("api/v1/auth/google/status") suspend fun googleStatus(@Header("Authorization") auth: String): Connection
    @POST("api/v1/playlists") suspend fun createPlaylist(@Header("Authorization") auth: String, @Body body: PlaylistRequest): PlaylistResponse
    @DELETE("api/v1/auth/mobile/session") suspend fun logout(@Header("Authorization") auth: String): LogoutResponse
}
data class MobileStart(val code_challenge: String, val code_challenge_method: String)
data class MobileStartResponse(val authorization_url: String, val expires_in: Int)
data class MobileExchange(val code: String, val code_verifier: String)
data class MobileSessionResponse(val session_token: String, val token_type: String, val expires_in: Int)
```

Use the existing playlist DTO/contract. For Direct checkpoint requests send
recommendation_id + title, not replacement track IDs. Authentication does not
activate the production Direct engine; its existing readiness guard remains.
Disable automatic retry of exchange and playlist writes. Lost exchange responses
require a new login: consumed codes cannot be replayed. 401 MOBILE_SESSION_INVALID
means clear local Sync credential and reconnect; 409 AUTH_IDENTITY_CONFLICT means
remove unintended credentials and explicitly choose the connection. Never mix a
cookie jar imported from the browser with native Bearer authentication.

## Verified App Link configuration (external prerequisite)

Manifest example, replace the host when using a custom domain:

```xml
<intent-filter android:autoVerify="true">
    <action android:name="android.intent.action.VIEW" />
    <category android:name="android.intent.category.DEFAULT" />
    <category android:name="android.intent.category.BROWSABLE" />
    <data android:scheme="https"
          android:host="sync-backend-c2lv.onrender.com"
          android:path="/auth/android/complete" />
</intent-filter>
```

The exported receiving Activity must validate the URI and pending login before
exchange. Association example below is documentation, not a deployable certificate:

```json
[
  {
    "relation": ["delegate_permission/common.handle_all_urls"],
    "target": {
      "namespace": "android_app",
      "package_name": "<ANDROID_APPLICATION_ID>",
      "sha256_cert_fingerprints": ["<ANDROID_SIGNING_SHA256>"]
    }
  }
]
```

For Play-distributed builds use the **Play App Signing** certificate, not merely
the upload certificate. Only one configured certificate is supported in this MVP;
debug/release variants must use matching configuration. After supplying real
values and deploying, verify the HTTPS association has no redirect and test:

```sh
adb shell pm verify-app-links --re-verify <actual-package>
adb shell pm get-app-links <actual-package>
```

Primary references: [Android association configuration](https://developer.android.com/training/app-links/configure-assetlinks),
[verification](https://developer.android.com/training/app-links/verify-applinks),
[troubleshooting](https://developer.android.com/training/app-links/troubleshoot),
[RFC 7636 S256 and verifier grammar](https://www.rfc-editor.org/rfc/rfc7636.html).

## Security and production boundaries

- 32 crypto-random bytes per transaction/state/handoff/session; verifier 43–128
  RFC unreserved ASCII characters. Only canonical S256 challenges are accepted.
- Handoff/session storage keys are SHA-256 hashes. Verifier is never retained.
- Exchange validates transaction, handoff TTL and phase, verifier constant-time,
  and live local Google credential reference; atomic consume + issuance under
  one coordinator lock permits exactly one success, including concurrent calls.
- Stores have independent 4096-entry caps. Expired entries clean up on access.
  Mobile transaction tombstones remain until after the shared Google state TTL,
  preventing expired mobile callbacks from accidentally becoming web callbacks.
- Invalid Bearer never falls back to cookies. Two live **credential references**
  must match; separate Google logins are conservatively conflicts even if they
  happen to represent the same account. Stale cookies have no live identity.
- Sync logout revokes one session only. Google disconnect removes backend
  credentials and associated Sync sessions; it does not revoke Google's grant.
- No Google tokens/secrets/cookies are transferred to Android. Application logs
  omit raw queries/headers/bodies. Render/proxy access-log configuration is outside
  source-level tests: it must also omit callback/App Link query strings.
- Recommendations remain unchanged. A high-entropy recommendation_id is still a
  bearer capability, not user ownership. Leaked IDs can be used by another
  authenticated account to create its own playlist; anonymous writes remain denied.
- Single process only: restart clears Google tokens, transactions, handoffs,
  sessions and checkpoints. Multiple replicas cannot safely share these records.
  Durable, shared transactional stores are required for durable production auth.
- Anonymous start is capacity-bounded, but public production still needs edge
  rate limits and a durable store; bounded memory alone is not anti-abuse protection.

No new live OAuth, Gemini, YouTube read/search or playlist write occurred during
this milestone. Device App Link validation remains pending actual Android values.

## Supplied Android debug build

Render environment values for the supplied debug build (public association metadata):

```dotenv
MOBILE_APP_LINK_BASE_URL=https://sync-backend-c2lv.onrender.com
ANDROID_PACKAGE_NAME=com.example.sync_app
ANDROID_APP_SIGNING_SHA256=A5:07:6F:4B:4E:D5:30:D1:91:9E:2F:90:3B:0C:57:64:61:30:B7:1E:7A:76:8A:AC:0F:5E:0B:89:20:6E:CA:03
```

The production Google redirect must remain the existing HTTPS callback on this
same Render host. Local `.env` keeps its localhost OAuth callback unchanged,
so setting the Render App Link base alone does not enable local mobile routes.
No deployment/environment update to Render was performed when recording these values.

This fingerprint applies only to APKs signed with the supplied debug certificate.
Release or Play-distributed APKs require their actual signing certificate. Do not
label this as a verified production signing identity. On device, use a VIEW intent
filter with `android:autoVerify="true"`, scheme `https`, host
`sync-backend-c2lv.onrender.com`, path `/auth/android/complete`; after deployment,
verify `/.well-known/assetlinks.json` and device App Link resolution separately.
