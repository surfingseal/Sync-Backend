# Sync OpenAPI / Swagger

- Swagger UI: `/swagger`
- OpenAPI 3.0.3 JSON: `/swagger/openapi.json`
- Source file: `internal/apidocs/openapi.json` (embedded with Go embed; no new runtime Go dependency)

Swagger UI uses pinned `swagger-ui-dist@5.17.14` CDN assets. The JSON spec itself is local and works without CDN access. UI is read-only (`supportedSubmitMethods: []`), remote validator disabled, authorization persistence disabled. No secret/API key/OAuth token or actual recommendation ID is in examples. Download JSON to Android/client tooling as needed. CDN access is necessary for the visual UI; no spec or credentials are sent to a validator service.

Documentation is exposed in development and production: existing routes are public contract, docs contain no credentials, and read-only UI cannot submit playlist/OAuth requests. This is a documentation exposure choice only. Production readiness/default engine, all API auth checks, and handlers remain unchanged. Deploying docs does not activate Gemini Direct. Ordinary `cmd/server` still uses the guarded legacy wiring; the registered Direct/checkpoint playlist variants can return503.

## Android contract notes

- Direct multipart: one `image`, optional `request_id` (correlation, not idempotency), JPEG/PNG/WebP ≤10MiB; request envelope ≤10MiB+64KiB. No Direct auth requirement.
- Direct final≤5. `partial=true` if count<5 or Korean model-classified final count<2. Verified tracks are returned even when the Korean constraint is missed. Zero verified tracks is200 with empty tracks, partial=true.
- `lyric_language` is model-classified, not verified lyric metadata. `language_policy.satisfied` uses final returned ko/ko_en count. Current known model classification inaccuracies are not concealed by docs.
- `album_title` and `album_artwork_url` are nullable, genuine iTunes metadata; enrichment failure leaves tracks intact. UI fallback: album_artwork_url → youtube_thumbnail_url → client placeholder. Existing `thumbnail_url` remains.
- `recommendation_id` is an opaque process-local checkpoint capability, TTL1h, lost on restart. It is not a login credential. Keep it with the returned verified order; do not log/share it. New recommendations get new IDs.
- Mobile exchange returns an opaque **Sync** bearer session, not Google access/refresh token and not a JWT. Use `Authorization: Bearer <session>` without sending Google tokens to these APIs. Swagger examples omit credential values.
- Browser uses HttpOnly `sync_session` cookie. Bad explicit bearer never falls back to a cookie; conflicting live identities produce409. Missing credentials on status can return connected:false200; writes require auth.
- Google callback is a browser flow: mobile302 to App Link completion, browser200 connection + session cookie. State cookie/redirect URI must agree; do not call callback as Android JSON login API.
- `/playlists` is one route with two existing variants. `recommendation_id` selects strict private-only checkpoint payload (no tracks field); legacy raw-video payload supports private/default, unlisted, public. Both require authenticated YouTube connection. Browser cross-origin rejection remains even with bearer.
- Playlist creation is201 even with partial per-item failures. No automatic rollback. Avoid blind retries of unconfirmed write; checkpoint replay guards are in-memory.
- Android App Link package/SHA/settings must be truly configured; docs do not invent them. Mobile routes may503 without settings.

## Validation / maintenance

`internal/router/openapi_test.go` checks exact route/spec coverage in development and production, DTO response fields, nullable artwork and auth/multipart, Swagger/health responses and unchanged Direct503 guard. `go test ./...` sends no real provider calls.

When handlers/DTOs change, update this static spec and tests. JSON Schema enum/type correctness cannot prove actual model lyrics or metadata identity. Optional standard validator:

```sh
python -m venv /tmp/sync-openapi-validator
/tmp/sync-openapi-validator/bin/pip install openapi-spec-validator==0.7.2
/tmp/sync-openapi-validator/bin/python -c 'import json; from openapi_spec_validator import validate_spec; validate_spec(json.load(open("internal/apidocs/openapi.json")))'
```

Standards: [OpenAPI 3.0.3](https://spec.openapis.org/oas/v3.0.3), [Swagger UI configuration](https://swagger.io/docs/open-source-tools/swagger-ui/usage/configuration/).
