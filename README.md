# sync

사진의 시각적 분위기를 분석하고, 향후 음악 추천과 YouTube 플레이리스트 생성으로 확장할 Go 백엔드입니다. 현재는 이미지 업로드, **Vertex AI Gemini 분석**, 분석 결과를 이용한 **실제 YouTube 음악 후보 추천**을 구현합니다. **Google OAuth를 통한 사용자 YouTube 계정 연결 및 channels.list 검증**까지 지원합니다. **OAuth 계정의 YouTube playlist 생성 및 선택한 곡 추가**까지 지원합니다. 데이터베이스, 이미지 영구 저장은 아직 구현하지 않습니다.

## 기술 스택

- Go 1.26 이상, Gin v1.12.0, REST/JSON, Go Modules
- Google 공식 Gen AI Go SDK `google.golang.org/genai v1.72.0`
- 공식 YouTube Data API client `google.golang.org/api v0.300.0`
- 공식 Go OAuth 라이브러리 `golang.org/x/oauth2 v0.37.0`와 `oauth2/google`
- `github.com/disintegration/imaging v1.6.2`: EXIF orientation와 Lanczos resize
- `golang.org/x/image v0.46.0`: WebP 디코딩
- OS 환경변수, Service Account + Application Default Credentials(ADC)

## 프로젝트 구조와 역할

```text
sync/
├── cmd/server/main.go
├── internal/
│   ├── auth/         # Google OAuth service, TokenStore와 테스트
│   ├── client/
│   │   ├── analyzer.go
│   │   ├── vertex.go
│   │   ├── vertex_queries.go
│   │   ├── prompt.go
│   │   ├── analysis_schema.json
│   │   ├── music.go
│   │   ├── youtube.go
│   │   └── *_test.go
│   ├── config/       # 환경변수 검증
│   ├── handler/      # health, analyze, recommend, google_oauth, playlist와 테스트
│   ├── image/        # processor.go와 테스트/benchmark
│   ├── service/      # image, recommendation, playlist service와 테스트
│   ├── model/        # analysis, image, error, recommendation, 검증과 testdata
│   └── router/       # /health, /api/v1 그룹과 테스트
├── examples/recommend-request.json
├── .env.example
├── .gitignore
├── go.mod
├── go.sum
└── README.md
```

## 로컬 인증과 실행

서비스 계정 JSON은 repository 밖(예: `~/secrets/sync-service-account.json`)에 보관합니다. 실제 JSON 내용이나 private key를 코드, `.env`, README에 복사하지 않습니다. SDK가 ADC를 자동 탐색하며 애플리케이션은 credential JSON을 직접 파싱하지 않습니다.

Google Cloud 프로젝트에서 Vertex AI API를 활성화하고 서비스 계정에 모델 호출 권한을 부여해야 합니다. 일반적인 사전 정의 역할은 `roles/aiplatform.user`이며, 모델 호출의 핵심 권한은 `aiplatform.endpoints.predict`입니다. 조직 정책에 따라 이 권한을 포함한 custom role을 사용할 수 있습니다. Owner/Editor를 요구하거나 코드가 IAM을 변경하지 않습니다.

```sh
export GOOGLE_APPLICATION_CREDENTIALS="$HOME/secrets/sync-service-account.json"
export GOOGLE_CLOUD_PROJECT="graduation-exhibition-510404"
export GOOGLE_CLOUD_LOCATION="global"
export GOOGLE_GENAI_USE_VERTEXAI="true"
export VERTEX_MODEL="gemini-3.8-flash"
export VERTEX_TIMEOUT_SECONDS="20"
export PORT="8080"
export APP_ENV="development"
export IMAGE_MAX_DIMENSION="1920"
export IMAGE_HARD_MAX_BYTES="6291456"

# YouTube 추천을 사용할 때만 설정합니다. 실제 키는 repository에 저장하지 않습니다.
export YOUTUBE_API_KEY="YOUR_YOUTUBE_API_KEY"
export YOUTUBE_REGION="KR"
export YOUTUBE_RELEVANCE_LANGUAGE="ko"
export RECOMMENDATION_COUNT="10"
export YOUTUBE_SEARCH_QUERY_COUNT="2"

go mod download
go run ./cmd/server
```

`.env.example`은 참고용이며 자동으로 읽지 않습니다. 환경변수를 shell이나 실행 환경에서 설정합니다.

| 변수 | 기본값 / 동작 |
| --- | --- |
| `PORT` | `8080`, 1~65535 |
| `APP_ENV` | `development`; production에서 Gin release 모드 |
| `GOOGLE_APPLICATION_CREDENTIALS` | 로컬 서비스 계정 JSON의 절대경로. SDK가 읽음 |
| `GOOGLE_CLOUD_PROJECT` | 필수. 없으면 시작 실패 |
| `GOOGLE_CLOUD_LOCATION` | `global` |
| `GOOGLE_GENAI_USE_VERTEXAI` | 예제는 `true`; 코드에서도 BackendVertexAI를 명시 |
| `VERTEX_MODEL` | `gemini-3.8-flash`, 교체 가능 |
| `VERTEX_TIMEOUT_SECONDS` | `20`, 양의 정수 |
| `IMAGE_MAX_DIMENSION` | `1920`, 1~10000 |
| `IMAGE_HARD_MAX_BYTES` | `6291456` (6 MiB), 더 작은 값으로 조절 가능 |
| `YOUTUBE_API_KEY` | 공개 YouTube 검색 전용 key; 없으면 recommend만 503 |
| `YOUTUBE_REGION` | `KR`, 두 자리 국가 코드 |
| `YOUTUBE_RELEVANCE_LANGUAGE` | `ko`, 소문자 두 자리 언어 코드 |
| `RECOMMENDATION_COUNT` | `10`, 5~20 |
| `YOUTUBE_SEARCH_QUERY_COUNT` | `2`, 1~3 |

SDK 클라이언트는 시작 시 한 번 생성하고 재사용합니다. `BackendVertexAI`, project/location, `HTTPOptions.APIVersion="v1"`을 명시하며 API key를 사용하지 않습니다. ADC를 찾을 수 없거나 초기화할 수 없으면 안전한 오류 메시지와 함께 시작을 중단합니다. IAM 및 모델 접근 권한은 실제 호출 시 확인됩니다.

향후 Cloud Run/GKE/Compute Engine에서는 서비스 계정을 실행 환경에 연결하고 ADC를 사용합니다. JSON key 파일 배포나 인증 코드 변경이 필요 없는 구조입니다.

## 모델과 이미지 입력

기본 모델은 `gemini-3.8-flash`입니다. 해당 프로젝트/global에서 실제 JPEG·PNG 요청 성공을 확인했습니다. [공식 모델 문서](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/gemini/3-8-flash)는 이미지 입력, structured output, inline 이미지당 7 MB 한도를 설명합니다.

공개 업로드 API는 기존 **10 MiB(10,485,760 bytes)** 한도를 유지합니다. multipart 전체 요청은 추가로 64 KiB만 허용합니다. 파일 내용을 `http.DetectContentType`으로 검사하며 JPEG/PNG/WebP만 받습니다. filename은 경로로 사용하지 않습니다. multipart를 stream으로 읽고 메모리에서 처리하므로 서버 디스크에 이미지나 multipart 임시 파일을 쓰지 않습니다.

### 일관된 전처리 파이프라인

```text
Handler → ReadImage 검증 → ImageService → ImageProcessor → VertexImageAnalyzer
                                      원본 bytes        최종 bytes/MIME
```

**모든 이미지**를 decode하고 다시 인코딩합니다. 파일이 작더라도 원본 bytes를 Vertex로 우회 전달하지 않습니다. 별도 `ImageProcessor` interface를 service에 주입하며 Vertex client에는 resize/compression 코드가 없습니다.

1. MIME 검증 후 `image.DecodeConfig`로 할당 전 해상도를 확인합니다.
2. width/height 각각 최대 10000px, 총 40MP를 초과하면 거부합니다. 공유 processor는 한 번에 하나의 이미지 raster만 처리하도록 대기열을 제한합니다. 대기 중 context 취소가 가능합니다.
3. JPEG는 `imaging.Decode(..., imaging.AutoOrientation(true))`로 EXIF orientation 1~8의 회전/반전을 보정합니다. EXIF가 없거나 orientation을 읽을 수 없으면 pixel 방향을 유지합니다. EXIF 내용을 로그에 쓰지 않습니다.
4. 긴 변이 기본 1920px보다 클 때 비율을 유지해 축소하며 확대하지 않습니다. JPEG orientation 보정 후의 dimensions를 기준으로 합니다.
5. JPEG → JPEG, PNG → PNG, WebP → JPEG로 인코딩합니다. PNG는 사진 여부를 임의 추정하지 않고 투명도/문자/그림 품질을 위해 모두 유지합니다. WebP의 투명 영역은 JPEG 변환 시 흰 배경에 합성합니다.
6. JPEG quality 85로 시작합니다. 2.5MiB의 soft target을 초과하면 80, 75 순서로 제한적으로 시도합니다. 더 낮은 품질은 사용하지 않습니다. PNG는 손실 변환으로 soft target에 맞추지 않습니다.
7. 최종 hard limit은 **6MiB**입니다. 초과하면 긴 변 1600, 1280 순서의 추가 축소를 시도하고, 그래도 초과하면 `413 IMAGE_TOO_LARGE`를 반환합니다. encoder output buffer 자체에도 hard limit을 적용합니다.

목표 크기는 상한 최적화를 위한 참고값입니다. 작은 이미지를 1MB까지 키우거나 PNG를 2.5MB 이하로 만들기 위해 강제 JPEG 변환하지 않습니다. 재인코딩한 WebP는 원본보다 커질 수도 있습니다.

표준 JPEG/PNG encoder는 원본 EXIF/GPS/기기/시간/PNG ancillary metadata를 복사하지 않습니다. API 응답 `image.filename`, `image.content_type`, `image.size`는 **원본 업로드 정보**를 유지합니다. 내부 `ProcessedImage`에 최종 Data/MIMEType/width/height/size/resize 여부를 기록합니다. GCS나 영구 저장은 사용하지 않습니다.

### 라이브러리와 context

`x/image/draw`의 ApproxBiLinear/Catmull-Rom과 `imaging`의 Lanczos를 비교했습니다. 최종 선택은 **imaging Lanczos**입니다. EXIF 처리에 쓰는 동일한 의존성을 재사용하면서 작은 중간 버퍼와 고품질 축소를 제공합니다. `x/image`는 WebP decoder에만 사용합니다. Opaque NRGBA는 zero-copy RGBA view로 JPEG encoder에 전달해 per-pixel allocation을 줄입니다.

processor는 자체 background goroutine을 만들지 않습니다. 라이브러리 내부의 orientation/resize 작업은 동기 호출이 끝나기 전에 완료됩니다. context는 gate 대기, 입력 read, 출력 write, 각 처리 단계와 압축 반복 전후에 확인합니다. codec/resize의 내부 CPU 작업 한가운데를 선점해 중단하는 구조는 아니며 현재 단계가 끝나면 취소를 반환합니다. 전체 전처리/AI 요청은 service의 timeout을 공유합니다.

## API

### GET /health

```sh
curl -i http://localhost:8080/health
```

HTTP 200: `{"status":"ok"}`

### POST /api/v1/analyze

`multipart/form-data`의 `image`에 파일 한 장을 지정합니다.

```sh
curl -i -X POST -F "image=@sunset.jpg" http://localhost:8080/api/v1/analyze
curl -i -X POST -F "image=@sample.png" http://localhost:8080/api/v1/analyze
```

HTTP 200 예시(실제 결과는 이미지에 따라 달라짐):

```json
{
  "image": {"filename": "sunset.jpg", "content_type": "image/jpeg", "size": 245123},
  "analysis": {
    "scene": {"category": "ocean", "description": "sunset over a calm sea", "time_of_day": "sunset", "weather": "clear"},
    "visual": {"brightness": 0.55, "color_temperature": "warm", "dominant_colors": ["orange", "blue"], "motion": "low"},
    "mood": {"tags": ["calm", "warm", "nostalgic", "dreamy"], "energy": 0.3, "valence": 0.65},
    "music_profile": {"tempo": "slow-medium", "energy": 0.35, "vocal_preference": "soft-vocal", "genres": ["indie-pop", "acoustic", "dream-pop"]}
  }
}
```

Google OAuth API는 아래 계정 연결 절을 참조하세요. 플레이리스트 API는 아래 생성 절을 참조하세요.

## Prompt와 Structured Output

`internal/client/prompt.go`는 사진의 시각적 분위기만 분석하도록 지정합니다. 다음 원칙을 포함합니다.

```text
Analyze only the visual atmosphere of the image.
Do not infer the user's actual emotions, personality, mental state, identity, age, health condition, or intentions.
Mood tags describe the image, not the person who uploaded it.
Do not recommend specific songs or artists.
Do not invent factual details that are not reasonably visible in the image.
If a visual property is uncertain, use a neutral or unknown value rather than guessing.
```

이미지 안의 지시문을 명령으로 취급하지 않습니다. 불확실한 scene은 `unknown`, 색온도는 `neutral`, 수치는 `0.5`를 사용하도록 안내합니다.

`ResponseMIMEType="application/json"`과 `ResponseJsonSchema`를 함께 전달합니다. 완전한 JSON Schema는 `internal/client/analysis_schema.json`에 있으며 `scene`, `visual`, `mood`, `music_profile`와 모든 하위 필드를 required로 지정하고 arbitrary property를 거부합니다.

JSON을 Go struct로 변환하고 필수/null/추가 필드, 빈 문자열, enum, 수치와 배열 제한을 검사합니다. service에서도 결과를 검증합니다. markdown 제거나 substring parsing은 하지 않습니다.

- brightness, mood.energy, mood.valence, music_profile.energy: 유한한 0~1
- mood.tags: 1~6, dominant_colors: 1~5, genres: 1~5
- color_temperature: warm/neutral/cool; motion: low/medium/high
- tempo: slow/slow-medium/medium/medium-fast/fast
- vocal_preference: instrumental/soft-vocal/vocal/either

## Timeout, retry와 오류

request context가 handler → service → client로 전달됩니다. 20초 기본 timeout은 이미지 준비, 모든 호출과 backoff를 포함한 전체 분석에 적용됩니다. SDK HTTP timeout도 설정합니다.

SDK의 native retry만 사용합니다. 429/500/502/503/504에 대해 최초 요청을 포함해 최대 3회 시도합니다. 0.5초에서 시작하는 exponential backoff(배수 2), 최대 2초, 0~0.25초 jitter를 설정합니다. 최종 실패 뒤에는 기다리거나 다시 호출하지 않습니다. context 취소/timeout을 존중하고 별도의 중첩 retry는 없습니다.

공통 오류 형식: `{"error":{"code":"...","message":"..."}}`

| 상황 | HTTP | code |
| --- | --- | --- |
| image 없음 | 400 | IMAGE_REQUIRED |
| multipart/읽기 오류 | 400 | INVALID_REQUEST / IMAGE_READ_ERROR |
| 이미지 decode 실패 | 400 | IMAGE_DECODE_FAILED |
| 해상도/총 pixel 한도 초과 | 400 | IMAGE_DIMENSIONS_TOO_LARGE |
| 전처리 encoder 오류 | 500 | IMAGE_PROCESSING_FAILED |
| 전처리 결과 hard limit 초과 | 413 | IMAGE_TOO_LARGE |
| 지원하지 않는 MIME | 415 | UNSUPPORTED_IMAGE_TYPE |
| 이미지/전체 요청 한도 초과 | 413 | IMAGE_TOO_LARGE |
| 전체 timeout / upstream 504 | 504 | AI_TIMEOUT |
| 최종 upstream 429 | 503 | AI_RATE_LIMITED |
| 최종 upstream 500/502/503 | 503 | AI_SERVICE_UNAVAILABLE |
| ADC/권한/모델 설정 오류 | 500 | AI_CONFIGURATION_ERROR |
| 잘못된 structured response | 502 | INVALID_AI_RESPONSE |
| 기타 upstream 오류 | 502 | AI_SERVICE_ERROR |

raw Vertex 오류, credential 경로/내용, 토큰, Authorization, 원본/base64 이미지는 응답이나 로그에 노출하지 않습니다. 전처리 로그에는 원본/최종 MIME, dimensions, byte size, resized/reencoded 여부와 duration만 기록합니다. Vertex 로그에는 모델, 프로젝트, 위치, 전달한 MIME/크기, latency, status, 최종 성공 여부와 설정한 최대 시도 횟수를 기록합니다. 인증된 HTTP transport의 실제 시도 횟수와 status를 기록하며 inter-attempt gap을 retry wait 추정치로 남깁니다. 이미지/토큰/URL/Authorization 내용은 읽거나 출력하지 않습니다.

SIGINT/SIGTERM 시 `http.Server.Shutdown()`으로 진행 중 요청의 종료를 기다립니다. 종료 유예 시간은 분석/recommendation timeout 중 더 긴 값 + 5초입니다.

## 테스트

```sh
gofmt -w cmd internal
go mod tidy
go test ./...
go vet ./...
go build ./cmd/server
```

기본 unit test는 fake analyzer/HTTP transport만 사용합니다. 실제 Vertex 호출은 `VERTEX_INTEGRATION_TEST=1`일 때만 실행하므로 기본 테스트에는 외부 API 비용이 없습니다. 업로드 JPEG/PNG/WebP, 누락/잘못된 MIME/초과 크기, portrait/aspect ratio, EXIF orientation 1~8, metadata 제거, transparency, 12MP resize, dimensions/decode 오류, hard-limit fallback, context/timeout, processed bytes의 analyzer 전달, 구조 검증, HTTP 오류 매핑, retry를 검증합니다.

위 실행 환경변수를 설정한 뒤 실제 호출을 명시적으로 활성화할 수 있습니다(과금 가능):

```sh
VERTEX_INTEGRATION_TEST=1 VERTEX_TEST_IMAGE="$PWD/sunset.jpg" \
  go test ./internal/client -run '^TestVertexIntegration$' -v -count=1

VERTEX_INTEGRATION_TEST=1 VERTEX_TEST_IMAGE="$PWD/sunset.jpg" \
  go test ./internal/client -run '^TestVertexUnknownModelIntegration$' -v -count=1
```

실제 테스트는 태그 문자열을 고정 비교하지 않고 필수 필드/범위와 모델 오류의 안전한 HTTP 응답을 검증합니다.

잘못된 요청 확인:

```sh
curl -i -X POST -F "image=@README.md" http://localhost:8080/api/v1/analyze
curl -i -X POST -F "other=value" http://localhost:8080/api/v1/analyze
```

첫 요청은 415, 두 번째는 400입니다. 로컬에 다른 ADC가 있으면 credential 변수를 unset해도 SDK가 이를 찾을 수 있습니다. ADC 없음의 재현은 존재하지 않는 파일 경로를 지정하여 확인할 수 있습니다.

```sh
GOOGLE_APPLICATION_CREDENTIALS="/absolute/path/that-does-not-exist.json" go run ./cmd/server
```

선택적 benchmark:

```sh
go test ./internal/image -run '^$' -bench BenchmarkImageProcessor4000x3000JPEG -benchmem
```

## 실제 검증 결과 (2026-10-02)

최종 빌드의 검증 서버를 포트 8084에서 실행해 아래 요청을 실제 Vertex AI `gemini-3.8-flash`로 분석했습니다. processor 출력 MIME/bytes와 Vertex 호출 로그의 MIME/size가 일치함을 확인했습니다. API의 image metadata는 원본 기준임도 확인했습니다.

| 이미지 | 원본 | 전처리 | 최종 MIME | 실제 API |
| --- | --- | --- | --- | --- |
| scones.jpg | 1280×853, 394,671 bytes | 1280×853, 341,052 bytes | image/jpeg | 200 |
| large-photo.jpg | 4032×3024, 3,489,618 bytes | 1920×1440, 542,769 bytes | image/jpeg | 200 |
| cupcakes.png | 1024×1024, 1,537,926 bytes | 1024×1024, 1,437,651 bytes | image/png | 200 |
| photo.webp | 550×368, 30,320 bytes | 550×368, 46,284 bytes | image/jpeg | 200 |

- gofmt, go mod tidy, go test ./..., go vet ./..., go build: 통과
- 기존 health: HTTP 200 / `{"status":"ok"}`
- 손상된 JPEG: 400 IMAGE_DECODE_FAILED
- text 파일: 415 UNSUPPORTED_IMAGE_TYPE
- image field 없음: 400 IMAGE_REQUIRED
- SIGTERM: 정상 종료(exit 0). 검증 서버는 종료했습니다.
- opt-in `TestVertexIntegration`: 실제 4032×3024 JPEG를 1920×1440, 542,769 bytes로 준비한 뒤 Vertex structured JSON 분석 통과.
- 메모리 생성 fixture로 orientation 1~8, metadata 제거, PNG transparency, portrait/aspect ratio, 4000×3000 resize, dimension 거부, hard limit, cancellation, processed bytes의 AI 전달을 검증했습니다. 대용량 fixture는 repository에 추가하지 않았습니다.
- Apple M3 Pro의 12MP JPEG benchmark 1회 참고값: 약 119ms/op, 54,714,616 B/op, 209 allocs/op. B/op는 peak 메모리가 아닌 총 할당량입니다. 동일한 Catmull-Rom 경로의 1회 참고값은 약 334ms, 215,134,792 B/op였습니다. 실제 성능은 이미지/기기/동시 요청에 따라 달라집니다.

## POST /api/v1/recommend

분석 결과와 optional preferences를 JSON으로 받습니다. 완전한 요청 예제는 `examples/recommend-request.json`입니다.

```sh
curl -i -X POST \
  -H "Content-Type: application/json" \
  --data-binary @examples/recommend-request.json \
  http://localhost:8080/api/v1/recommend
```

실제 `/analyze` 응답을 이어 쓰는 예:

```sh
curl -sS -F "image=@sunset.jpg" http://localhost:8080/api/v1/analyze > analysis.json
python3 -c 'import json; print(json.dumps({"analysis": json.load(open("analysis.json"))["analysis"]}))' > recommendation-request.json
curl -i -H "Content-Type: application/json" --data-binary @recommendation-request.json \
  http://localhost:8080/api/v1/recommend
```

preferences 생략 시 languages `["ko","en"]`, count는 `RECOMMENDATION_COUNT`(기본 10), instrumental_only는 false입니다. count는 5~20, languages는 소문자 두 자리 코드 1~5개, preferred_genres/excluded_artists는 각각 최대 20개입니다. 요청은 64KiB로 제한하고 기존 strict analysis 검증(필수 필드/null/수치 범위)을 재사용합니다. unknown field나 잘못된 입력은 400입니다.

성공 응답에는 `tracks`, `requested_count`, `returned_count`, `partial`이 있습니다. 각 track에는 YouTube에서 조회한 `video_id`, `title`, `channel_title`, `thumbnail_url`, `duration_seconds`와 내부 deterministic `match_score`, `match_reasons`, 표준 `youtube_url`이 있습니다. 제목을 artist/song으로 임의 분리하지 않습니다. 후보가 부족해도 재검색하지 않고 적은 개수 또는 `tracks: []`로 HTTP 200을 반환합니다.

### 인증과 키 관리

Google Cloud Console에서 **YouTube Data API v3를 활성화**하고 별도 API key를 발급합니다. [API key 제한](https://cloud.google.com/docs/authentication/api-keys-best-practices)에 따라 YouTube Data API v3만 허용하는 API restriction을 적용하고, 고정 egress IP가 있는 배포 환경에서는 IP 기반 application restriction도 적용할 수 있습니다.

Vertex는 Service Account + ADC로 Gemini inference를 인증합니다. 공개 YouTube 검색은 API key로 인증하며 서비스 계정/ADC를 사용하지 않습니다. 사용자 계정 연결은 별도 Web OAuth client와 사용자 토큰으로 인증합니다. 키를 Android, 코드, `.env.example`, 응답, 로그에 넣지 않습니다. SDK의 상세 HTTP debug logger도 비활성화합니다. `YOUTUBE_API_KEY`가 없으면 health/analyze는 계속 동작하고 recommend는 503 MUSIC_SEARCH_UNAVAILABLE입니다.

### 추천 흐름과 quota

```text
검증된 ImageAnalysis + Preferences
→ Vertex structured 검색어 생성 (이미지를 다시 보내지 않음)
→ 실패하면 genre + mood + language의 deterministic fallback
→ YouTube search.list (기본 2회, query당 최대 10개)
→ videoId 중복 제거
→ videos.list 1회 batch (최대 30 IDs)
→ 검증/필터 → deterministic ranking → 요청 개수만큼 반환
```

Vertex는 broad genre/mood/language 검색어만 생성합니다. 특정 곡/아티스트 추천이나 candidate reranking은 하지 않습니다. prompt는 song titles/artist names 생성을 금지하며 JSON Schema의 `queries` 배열만 허용합니다. 각 AI query는 최대 120자, 기본 최대 2개이고 config로 최대 3개까지 설정할 수 있습니다. 최종 query에는 언어/instrumental 조건을 반영하고 최대 160자로 제한합니다. `LOW` thinking으로 작은 query 작업의 latency를 줄입니다.

추천 전체 timeout은 15초, Vertex query 생성 예산은 4초입니다. 실패/invalid response/timeout이면 안전한 분류 로그를 남기고 남은 시간에 fallback을 검색합니다. 클라이언트가 취소했거나 전체 timeout이 끝났다면 fallback을 계속하지 않습니다.

검색 조건: `part=snippet`, `type=video`, `videoCategoryId=10`, 설정한 region/relevanceLanguage, `videoEmbeddable=true`, `videoSyndicated=true`, `safeSearch=moderate`, `maxResults=10`. bounded sequential 검색을 사용하며 pagination, refill, 동일 query 반복, YouTube custom retry를 하지 않습니다. [공식 search.list 문서](https://developers.google.com/youtube/v3/docs/search/list)

검색으로 모은 ID는 deduplicate 후 **한 번의 videos.list**로 `snippet,contentDetails,status`만 요청합니다. statistics는 요청하지 않습니다. [공식 videos.list 문서](https://developers.google.com/youtube/v3/docs/videos/list)

### 검증과 점수

반환되는 ID는 search 결과에도 존재하고 videos.list에도 존재해야 합니다. 삭제/조회 누락/요청하지 않은 ID와 duplicate 상세 결과는 제외합니다.

필터: category 10, 90~720초(경계 포함), public, embeddable, live/upcoming 아님, region allowed 목록에 포함/blocked 목록에 없음, 빈 title/channel 아님. allowed가 명시적으로 빈 목록이면 모든 지역에서 제외합니다. excluded_artists는 title/channel에서 정규화한 정확한 단어 경계의 phrase로만 제외하고 fuzzy matching을 하지 않습니다.

`instrumental_only`는 검색 방향에 반영합니다. 공개 metadata만으로 실제 음원에 보컬이 없는지 증명할 수 없습니다. language preference도 query에 반영하며 ranking의 언어 점수는 YouTube의 defaultAudioLanguage/defaultLanguage가 있을 때만 부여합니다.

가중치는 `recommendation_service.go` 한 곳에 있습니다: 검색 순위 0.50, 선호 genre keyword 0.20, 분석 genre keyword 0.10, mood keyword 0.10, 명시 언어 metadata 0.05, official-looking keyword 0.05. 600초 초과는 0.05 penalty입니다. 최종 0~1로 제한하고 동점은 videoId 순서로 정렬합니다. genre/mood/official keyword는 metadata 기반 heuristic이며 실제 genre/분위기/공식 업로드임을 보장하지 않습니다.

`MusicQueryGenerator`와 `MusicSearchClient` interface를 주입하므로 fake 테스트와 향후 query/region/language별 cache decorator를 추가하기 쉽습니다. 이번 단계에는 Redis나 cache를 도입하지 않습니다.

### 추천 오류

| 상황 | HTTP | code |
| --- | --- | --- |
| 잘못된 JSON/analysis/preferences | 400 | INVALID_REQUEST |
| JSON이 아닌 요청 | 415 | UNSUPPORTED_CONTENT_TYPE |
| 요청 body 초과 | 413 | REQUEST_TOO_LARGE |
| 전체 timeout | 504 | RECOMMENDATION_TIMEOUT |
| YouTube quota/rate limit | 503 | YOUTUBE_QUOTA_EXCEEDED |
| YouTube key 미설정 | 503 | MUSIC_SEARCH_UNAVAILABLE |
| 기타 YouTube 실패 | 502 | MUSIC_SEARCH_ERROR |

Vertex query 실패는 fallback 대상이며 YouTube의 실패는 가상의 추천 결과로 대체하지 않습니다. raw 외부 오류/키/credential을 응답에 노출하지 않습니다.

### 추천 테스트

기본 `go test ./...`는 fake query generator / music client / SDK HTTP transport를 사용합니다. 실제 Vertex/YouTube 비용이나 quota를 사용하지 않습니다. 정상 추천, 중복/상세 누락, category/duration/지역/embedding/public/live 필터, 요청 count와 partial/빈 결과, language/instrumental/genre preference, 제외 artist phrase, fallback, timeout, quota/오류 매핑을 검증합니다.

실제 YouTube는 명시적으로 활성화한 경우만 호출합니다. 이 integration test는 고정 broad query를 사용하여 Vertex 비용 없이 YouTube만 검증하고, 반환한 모든 ID를 한 번 더 batch 조회합니다. 특정 곡이나 ID를 고정 비교하지 않습니다.

```sh
YOUTUBE_INTEGRATION_TEST=1 go test ./internal/client -run '^TestYouTubeIntegration$' -v -count=1
```

## 추천 실제 검증 결과 (2026-10-02)

- gofmt, go mod tidy, go test ./..., go vet ./..., build: 통과
- 서버 /health: 200
- 기존 /analyze: 실제 JPEG → 전처리 → Vertex 분석 200
- 최종 /recommend: 실제 Vertex structured query 생성 성공, search.list 2회, videos.list 20 IDs batch 1회, **3/10개 반환**(200, partial=true). 세 ID 모두 별도 videos.list로 다시 확인. 이전 실행의 빈 배열 반환도 명세에 맞게 검증했고 가상의 곡을 채우지 않음
- 반환한 모든 videoId를 별도 videos.list 요청으로 다시 조회해 title/channel/category/embedding이 일치함을 검증
- 잘못된 recommendation input: 400 INVALID_REQUEST
- SIGTERM 정상 종료와 서버 로그에 API key가 없음을 검증; 검증 서버는 종료함
- 최종 YouTube opt-in integration: 20개 상세 조회 후 2개 추천 및 모든 반환 ID의 재조회 검증 통과. 결과 수와 영상은 시점마다 달라짐

## 다음 단계

별도 OAuth client/service로 Google 동의와 사용자 토큰 관리를 구현한 뒤, 검증된 추천 결과의 videoId를 `playlists.insert`로 만든 playlist에 `playlistItems.insert`로 추가할 수 있습니다. 공개 검색용 API key와 사용자의 OAuth 인증을 분리한 채 확장합니다. 사용자 계정 연결과 메모리 token 저장은 아래 절처럼 구현했습니다. Playlist 작성 API는 아래 절처럼 별도 사용자 요청으로 구현합니다.

## 공식 참고 문서

- [Google Gen AI Go SDK](https://github.com/googleapis/go-genai)
- [Gemini 3.8 Flash](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/gemini/3-8-flash)
- [Vertex structured output](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/multimodal/control-generated-output)
- [Vertex IAM](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/access-control)

- [YouTube search.list](https://developers.google.com/youtube/v3/docs/search/list)
- [YouTube videos.list](https://developers.google.com/youtube/v3/docs/videos/list)


## Google OAuth: 사용자 YouTube 계정 연결

[Google Web Server OAuth flow](https://developers.google.com/identity/protocols/oauth2/web-server)를 공식 Go `oauth2.Config`로 구현합니다. 로그인 scope는 **`https://www.googleapis.com/auth/youtube` 하나**이며 email/profile/openid는 요청하지 않습니다.

| 인증 목적 | 설정 | 사용처 |
|---|---|---|
| Vertex Gemini inference | `GOOGLE_APPLICATION_CREDENTIALS` 서비스 계정 / ADC | 이미지 분석, 검색어 생성 |
| 공개 YouTube 영상 검색 | `YOUTUBE_API_KEY` | search.list, videos.list |
| 사용자 YouTube 계정 접근 | Web Client ID/Secret + 사용자 OAuth token | channels.list(mine=true), 향후 playlist 작성 |

### 설정

아래는 **placeholder**입니다. 실제 Client ID/Secret은 OS 환경변수로 설정하며 Git에 저장하지 않습니다. `.env.example`에는 빈 값만 포함합니다. `.env` 파일을 자동 로드하지 않습니다.

```sh
export GOOGLE_OAUTH_CLIENT_ID="YOUR_WEB_CLIENT_ID"
export GOOGLE_OAUTH_CLIENT_SECRET="YOUR_WEB_CLIENT_SECRET"
export GOOGLE_OAUTH_REDIRECT_URL="http://localhost:8080/api/v1/auth/google/callback"
export GOOGLE_OAUTH_SCOPE="https://www.googleapis.com/auth/youtube"
export OAUTH_COOKIE_SECURE="false"
export OAUTH_FORCE_CONSENT="true"
export PORT="8080"
```

Client ID/Secret이 없거나 설정이 잘못되면 OAuth만 비활성화합니다. 해당 endpoint는 HTTP 500 `OAUTH_CONFIGURATION_ERROR`를 반환하며 health/analyze/recommend의 기존 설정 정책에는 영향을 주지 않습니다. `GOOGLE_CLOUD_PROJECT`와 ADC 등 기존 필수 설정은 여전히 필요합니다. boolean 환경변수 오류도 OAuth만 비활성화합니다.

Google Cloud Web OAuth client에 등록한 redirect URI와 **scheme/host/port/path/trailing slash까지 정확히 일치**해야 합니다. 기본값과 위 예시는 등록된 **`http://localhost:8080/api/v1/auth/google/callback`**와 같습니다. 로그인도 `localhost`에서 시작하세요. `127.0.0.1`에서 시작하면 callback의 localhost 쿠키와 호스트가 달라 state 오류가 날 수 있습니다. `PORT`만 변경하면 callback 포트가 일치하지 않습니다.

`APP_ENV=production`에서는 `OAUTH_COOKIE_SECURE=true`, `OAUTH_FORCE_CONSENT=false`가 기본입니다. 운영 HTTPS redirect URI를 별도로 등록하고 환경변수에도 같은 URI를 설정해야 합니다. production에서 Secure=false를 명시하면 OAuth를 비활성화합니다. 이 단계에는 production 배포를 포함하지 않습니다.

### Endpoint와 흐름

| Method | Path | 동작 |
|---|---|---|
| GET | `/api/v1/auth/google` | state 생성, Google 로그인/동의로 HTTP 302 |
| GET | `/api/v1/auth/google/callback` | state 및 code 검증 → token 교환 → YouTube 검증 → 저장 |
| GET | `/api/v1/auth/google/status` | 토큰이 없으면 connected=false; 있으면 실제 YouTube API 검증 |
| DELETE | `/api/v1/auth/google` | local token 및 state/session 쿠키 삭제 |

`main → GoogleOAuthService → TokenStore / YouTubeChannelVerifier`로 주입합니다. handler는 cookie/query 및 HTTP 응답을 다루며 OAuth SDK 호출과 토큰 관리는 service에서 처리합니다.

state와 session ID는 각각 `crypto/rand` 32 bytes를 URL-safe base64로 변환합니다. state cookie `sync_oauth_state`는 **HttpOnly, SameSite=Lax, Path=/, 10분**이며 `OAUTH_COOKIE_SECURE`를 따릅니다. callback에서는 길이 및 constant-time 비교를 수행하고, 서버의 pending-flow 기록도 확인해 만료/재사용을 거부합니다. 유효하지 않은 state로는 code exchange를 수행하지 않습니다. pending flow는 최대 4096개로 제한합니다. **PKCE S256**도 적용하며 verifier는 서버 메모리에만 보관합니다.

`access_type=offline`, `include_granted_scopes=true`를 항상 지정하고, `OAUTH_FORCE_CONSENT=true`일 때만 `prompt=consent`를 추가합니다. refresh token은 항상 새로 발급되는 것이 아닙니다. 같은 session의 token 업데이트는 기존 refresh token을 보존합니다. 재연결에서는 session을 새로 발급하고, 이전/새 token으로 확인한 **YouTube channel ID가 동일한 경우에만** 이전 refresh token을 넘깁니다. 계정이 다르거나 동일성을 확인할 수 없으면 이전 계정 token을 재사용하지 않습니다.

`sync_session`은 **HttpOnly, SameSite=Lax, Path=/, 24시간**이며 ID만 담습니다. 토큰이나 개인정보를 cookie에 넣지 않습니다. TokenStore는 mutex로 보호되는 메모리 구현이며 24시간의 session 수명을 서버에서도 적용합니다. **서버 재시작 시 모든 토큰과 진행 중 OAuth state가 사라집니다.** 이후 DB store로 교체할 수 있도록 Save/Get/Update/Delete interface를 둡니다. Update는 기존 session만 수정하므로 logout 이후 refresh가 session을 다시 만들지 않습니다.

status는 `oauth2.Config.TokenSource`와 Bearer HTTP client를 사용합니다. 만료된 access token은 라이브러리가 refresh하며 새 token을 store에 저장합니다. 같은 session의 status/refresh는 직렬화해 중복 refresh를 막고, 다른 session은 독립적으로 처리합니다. 매 요청의 context를 전달하며 token 교환과 YouTube 검증에는 **10초 timeout**이 적용됩니다.

인증 검증은 공식 YouTube Go client의 [channels.list](https://developers.google.com/youtube/v3/docs/channels/list)로 `part=snippet`, `mine=true`, `maxResults=1`만 요청합니다. API key를 추가하지 않습니다. 예:

```json
{"connected":true,"youtube":{"channel_id":"UC...","channel_title":"..."}}
```

channel resource가 없어도 OAuth 자체는 성공입니다:

```json
{"connected":true,"youtube":{"channel_id":null,"channel_title":null}}
```

callback 및 status에는 access_token, refresh_token, client_secret, authorization code를 반환하지 않습니다. `Cache-Control: no-store`, `Referrer-Policy: no-referrer`를 설정합니다. SDK 상세 HTTP debug logger도 명시적으로 비활성화합니다. 개발 요청 로그는 URL **path만** 출력합니다. query string, Cookie/Authorization header, raw Google 오류, panic 내용을 출력하지 않습니다. 외부 오류 로그는 실패 단계와 고정된 분류/HTTP status만 남깁니다.

### 오류

| 상황 | HTTP | Code |
|---|---|---|
| OAuth 설정 누락/잘못된 설정 | 500 | OAUTH_CONFIGURATION_ERROR |
| state 누락/불일치/만료/재사용 | 400 | OAUTH_STATE_INVALID |
| 사용자 동의 거부 | 400 | OAUTH_ACCESS_DENIED |
| 기타 Google authorization 오류 | 400 | OAUTH_AUTHORIZATION_FAILED |
| authorization code 누락 | 400 | OAUTH_CODE_MISSING |
| token 교환 실패/timeout | 502 | OAUTH_TOKEN_EXCHANGE_FAILED |
| 필요한 저장 session 없음 | 401 | OAUTH_SESSION_NOT_FOUND |
| YouTube 검증/refresh 실패 | 502 | YOUTUBE_AUTH_FAILED |

session cookie/저장 token이 없는 status는 오류 대신 HTTP 200 `{"connected":false}`입니다. disconnect는 Google grant를 revoke하지 않으며 향후 별도 기능으로 추가할 TODO가 있습니다.

### 수동 OAuth integration test

1. Google Cloud에서 Web application OAuth client와 위 redirect URI를 확인합니다. OAuth 동의 화면이 Testing 상태라면 로그인할 Google 계정을 test user로 등록하세요.
2. 기존 Vertex/YouTube 설정과 OAuth 환경변수를 설정하고 기존 8080 서버를 종료합니다.
3. 프로젝트 루트에서 실행합니다:

   ```sh
   go mod download
   go run ./cmd/server
   ```

4. 브라우저에서 `http://localhost:8080/api/v1/auth/google`을 엽니다. **curl만으로 Google의 계정 선택과 사용자 동의를 완료할 수 없습니다.**
5. Google 계정을 선택하고 YouTube 권한을 승인합니다. callback의 `connected:true`와 channel 정보를 확인합니다.
6. **같은 브라우저**에서 `http://localhost:8080/api/v1/auth/google/status`를 엽니다. 실제 channels.list로 연결이 검증됩니다.
7. 같은 브라우저의 localhost 페이지 개발자 도구 console에서 아래를 실행해 local disconnect를 확인합니다:

   ```js
   await fetch('/api/v1/auth/google', {method: 'DELETE'}).then(r => r.json())
   ```

8. status를 다시 열어 `connected:false`를 확인합니다. Google grant는 그대로이므로 재연결 시 force-consent 설정에 따라 refresh token 발급 여부가 달라질 수 있습니다.

리다이렉트 직전의 HTTP 상태를 확인하는 curl(로그인/동의는 수행하지 않음):

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/api/v1/auth/google
curl -s http://localhost:8080/api/v1/auth/google/status
curl -s -X DELETE http://localhost:8080/api/v1/auth/google
```

### 자동 테스트와 다음 단계

```sh
gofmt -w cmd internal
go mod tidy
go test ./...
go vet ./...
```

기본 OAuth 테스트는 모든 token/YouTube HTTP 요청을 로컬 fake provider로 전달합니다. 실제 Google 로그인이나 외부 API 요청은 발생하지 않습니다. redirect 옵션, state/session cookie, 잘못된 callback, state 만료/재사용, token 교환 성공/실패, token 비노출, 실제 SDK channels.list 요청 형식, 채널 없음, status, refresh 저장/기존 refresh 보존, 계정 전환 시 refresh 분리, disconnect, context 취소, 설정 격리, 로그 비노출을 검사합니다. 실제 사용자 동의와 Google token 검증은 위 수동 절차로 수행해야 합니다.

다음 단계에서 이 사용자 token source/HTTP client를 재사용해 `playlists.insert`, `playlistItems.insert`에 연결할 수 있습니다. 서버에서 session과 추천 결과의 실제 videoId를 확인한 뒤 사용자가 요청한 playlist 작성만 수행하도록 확장합니다. 공개 검색용 API key나 Vertex 서비스 계정으로 사용자 playlist를 작성하지 않습니다. 아래 플레이리스트 생성 절에서 해당 write method를 구현합니다.


### 이번 OAuth 구현의 실행 검증 (2026-10-03)

- `gofmt`, `go mod tidy`, `go test ./...`, `go vet ./...`, 서버 빌드 통과.
- auth/handler/router의 `go test -race` 통과.
- 실제 8080 서버: health 200, OAuth 시작 302, 연결 전 status 200/connected=false, state 불일치 및 code 누락 400, local disconnect 200 확인.
- authorization URL의 offline/include_granted_scopes/YouTube scope/PKCE 및 등록된 redirect URI 일치 확인.
- 기존 analyze/recommend의 잘못된 요청 400 확인. 기존 unit test도 모두 통과.
- 프로젝트의 실제 OAuth Client ID/Secret 및 API key 미포함 확인. callback query의 검증용 code가 서버 로그에 나타나지 않음을 확인.
- 이후 사용자의 실제 브라우저 로그인/동의에 대해 callback과 YouTube 검증 성공 로그, status HTTP 200을 확인했습니다. 메모리 store인 만큼 playlist 서버로 교체한 후에는 재연결이 필요합니다.


## YouTube playlist 생성

`POST /api/v1/playlists`는 추천 결과를 사용자가 확인/선택한 뒤 호출하는 **별도 write API**입니다. `/recommend`는 playlist를 자동 생성하지 않습니다. 응답은 공식 YouTube playlist이며 YouTube Music 표시 여부를 보장하지 않습니다.

### 요청과 validation

```json
{
  "title": "Sync - Warm Sunset",
  "description": "Created by Sync from your photo mood.",
  "privacy_status": "private",
  "tracks": [{"video_id": "ACTUAL_VIDEO_ID_FROM_RECOMMEND"}]
}
```

위 ID는 placeholder입니다. 실제 테스트/사용에서는 최신 `/recommend` 응답의 `video_id`를 전달합니다. title/artist 등의 메타데이터를 요청에 섞지 않습니다.

- Content-Type: application/json, 최대 body 32 KiB, 알 수 없는 필드/여러 JSON 문서 거부.
- title: trim 후 필수, 최대 100 Unicode characters. description: optional, 최대 4,000 characters.
- [YouTube의 playlist 제한](https://support.google.com/youtube/answer/10232933?hl=en-GB)은 title 150, description 5,000 characters이므로 더 작은 앱 제한을 사용합니다.
- privacy_status 생략 시 **private**. private/unlisted/public만 허용합니다.
- 제출 tracks는 최소 1, 최대 20. 중복 제거 **전** 20개를 넘으면 400 PLAYLIST_TOO_MANY_TRACKS.
- video_id는 trim 후 필수, 최대 128 bytes, whitespace/control/URL 구분자 거부. 11자리 같은 고정 길이 제약을 두지 않습니다. 존재/추가 가능 여부는 YouTube API가 판단합니다.
- video_id 기준 중복 제거는 첫 등장 순서를 유지합니다.
- Cookie 기반 write이므로 명시적인 cross-site 요청과 설정된 callback origin과 다른 Origin을 거부합니다. 브라우저는 같은 backend origin에서 호출하고 CORS를 활성화하지 않습니다.

### 구조, OAuth와 순서

```text
PlaylistHandler → PlaylistService → GoogleOAuthService.WithAuthenticatedClient
                                    → TokenStore / persisted TokenSource
                                    → YouTubePlaylistClient
                                    → playlists.insert
                                    → playlistItems.insert (순차)
```

`internal/client/youtube_playlist.go`가 공식 SDK의 `Playlists.Insert([snippet,status], ...)` 및 `PlaylistItems.Insert([snippet], ...)`를 호출합니다. `sync_session`을 조회하고 token이 없으면 401 YOUTUBE_NOT_CONNECTED를 반환합니다. handler에는 token을 전달하지 않습니다. OAuth Config.TokenSource와 기존 store Update를 재사용하며, write 전에 refresh를 해결합니다. 갱신된 token은 저장하고 기존 refresh token을 유지합니다. 사용자 OAuth HTTP client를 사용하고 Vertex ADC/공개검색 API key를 섞지 않습니다.

playlist ID/item ID/title/privacy는 SDK 응답에서 가져옵니다. URL은 반환된 playlist ID로 `https://www.youtube.com/playlist?list=...`를 생성합니다. insertion은 순차 실행하며 snippet.position을 **확인된 성공 개수**로 지정합니다. 첫 position=0도 SDK ForceSendFields로 명시합니다. 실패한 곡을 제외한 성공 곡의 입력 순서를 유지합니다.

전체 timeout은 **120초**입니다. request context를 token refresh 및 SDK 호출까지 전달합니다. 같은 session의 인증 작업은 직렬화하되, lock 대기 중 context 취소도 처리합니다. main의 graceful shutdown은 playlist timeout을 포함해 최대 125초 동안 진행 중 요청을 기다립니다. http.Server의 5초 ReadHeaderTimeout은 header 수신만 제한하고 작업 전체 timeout이 아닙니다.

### 응답과 부분 실패

생성 성공은 **HTTP 201**, 일부 item이 실패해도 playlist가 생성되었다면 201과 부분 결과를 반환합니다.

```json
{
  "playlist": {
    "id": "ID_RETURNED_BY_YOUTUBE",
    "title": "Sync - Warm Sunset",
    "privacy_status": "private",
    "url": "https://www.youtube.com/playlist?list=ID_RETURNED_BY_YOUTUBE"
  },
  "submitted_count": 3,
  "requested_count": 3,
  "added_count": 2,
  "failed_count": 1,
  "partial": true,
  "items": [
    {"video_id":"A","status":"added","playlist_item_id":"ITEM_A","attempted":true},
    {"video_id":"B","status":"failed","error_code":"VIDEO_NOT_FOUND","attempted":true},
    {"video_id":"C","status":"added","playlist_item_id":"ITEM_C","attempted":true}
  ]
}
```

위 응답은 형식 예시입니다. 실제 ID는 API 응답을 사용합니다. submitted_count는 원본 제출 개수, requested_count는 dedup 후 개수입니다. added_count는 **성공 응답을 받은 개수**이며 failed_count는 실패/불명/미시도 항목을 모두 포함합니다. attempted=false 항목은 중단 정책에 의해 실제 호출하지 않았습니다.

일반적인 videoNotFound/403 등 개별 실패는 다음 곡을 계속 처리합니다. quota/인증/playlistNotFound/서비스 제한/결과 불명/context 종료는 남은 insertion을 중단하고 각 항목에 안전한 error_code와 attempted=false를 기록합니다. **이미 생성된 playlist 및 성공 항목은 자동 삭제/rollback하지 않습니다.**

| 상황 | 생성 전 HTTP / code |
|---|---|
| session/token 없음 | 401 YOUTUBE_NOT_CONNECTED |
| refresh/인증 실패 | 401 YOUTUBE_AUTH_EXPIRED |
| playlist 생성 실패 | 502 PLAYLIST_CREATE_FAILED |
| quota 제한 | 503 YOUTUBE_QUOTA_EXCEEDED |
| 일시적 서비스 제한 | 503 YOUTUBE_SERVICE_UNAVAILABLE |
| write 결과를 확인할 수 없음 | 502 PLAYLIST_CREATE_RESULT_UNKNOWN |
| 호출 전 timeout/cancel | 504 PLAYLIST_TIMEOUT / 408 REQUEST_CANCELED |

개별 item에는 VIDEO_NOT_FOUND, VIDEO_ADD_FORBIDDEN, PLAYLIST_NOT_FOUND, YOUTUBE_QUOTA_EXCEEDED, YOUTUBE_AUTH_EXPIRED, YOUTUBE_SERVICE_UNAVAILABLE, VIDEO_ADD_FAILED, VIDEO_ADD_RESULT_UNKNOWN 등을 사용합니다. Google raw error body/credential/code/token/Authorization/session cookie는 응답과 로그에 출력하지 않습니다. SDK 상세 debug logger도 비활성화합니다.

### Retry와 quota

설치된 Google Go SDK v0.300.0의 두 insert 메서드는 `gensupport.SendRequest`를 사용하며 `SendRequestWithRetry`를 사용하지 않습니다. 앱도 자동 write retry를 하지 않습니다. HTTP 5xx, 네트워크 오류, 응답 timeout 또는 응답 ID 누락은 **이미 실제 write가 반영되었을 수 있으므로 결과 불명**으로 분리합니다. item의 경우 남은 호출도 중단합니다. 생성 결과 불명에는 playlist ID를 만들어 넣지 않습니다. 클라이언트도 전체 POST를 자동 재전송하지 말고 먼저 사용자의 YouTube 계정에서 결과를 확인해야 합니다. 현재 서버에는 요청 간 idempotency key/영구 기록이 없습니다.

공식 [playlists.insert](https://developers.google.com/youtube/v3/docs/playlists/insert), [playlistItems.insert](https://developers.google.com/youtube/v3/docs/playlistItems/insert)는 각각 50 quota units입니다. 중복 제거 후 N곡을 모두 시도하면 **50 + 50×N**: 1곡 100, 10곡 550, 20곡 1,050 units. 여기에 추천 검색 및 검증 비용이 별도로 붙습니다. 중복 제거, 최대 20곡, fatal 오류 후 중단으로 불필요한 write를 줄입니다.

### 실제 수동 integration test: cookie 복사 없이

기본 go test는 실제 계정을 변경하지 않습니다. 이번 단계의 실제 integration은 아래 **명시적인 수동 호출**로 수행합니다. 자동으로 테스트 playlist를 생성하거나 삭제하는 test를 추가하지 않습니다.

1. 기존 환경변수를 설정하고 `go run ./cmd/server`로 PORT=8080 서버를 실행합니다. 메모리 TokenStore이므로 서버를 재시작했다면 다시 연결해야 합니다.
2. 브라우저에서 `http://localhost:8080/api/v1/auth/google`을 열어 로그인/동의합니다.
3. 같은 브라우저에서 `http://localhost:8080/api/v1/auth/google/status`의 connected=true를 확인합니다.
4. 이 localhost 페이지의 개발자 도구 Console에 `examples/playlist-browser-test.js` 내용을 붙여 넣습니다. 초기화만으로 write가 발생하지 않습니다. 코드는 cookie/token을 읽거나 출력하지 않습니다.
5. 아래로 최신 실제 추천을 받고 console table에서 곡을 확인합니다. 인자를 생략하면 예시 분위기를 사용하며, 실제 사진을 사용하려면 `/analyze`의 analysis를 인자로 전달합니다.

   ```js
   const candidates = await syncPlaylistTest.recommend();
   ```

6. 선택한 곡의 **0부터 시작하는 index**를 전달합니다. 예를 들어 추천이 두 곡 이상 있을 때:

   ```js
   const result = await syncPlaylistTest.create([0, 1]);
   ```

   기본 title은 Sync Test Playlist, privacy는 **private**입니다. 실제 `/recommend`의 ID만 보내며 같은 브라우저의 HttpOnly cookie는 fetch(credentials=same-origin)가 자동 첨부합니다. cookie jar 수동 추출/토큰 복사/추가 login scope가 필요 없습니다. 한 번 write를 시도하면 helper는 중복 호출을 막습니다.
7. 응답의 added_count/failed_count/items를 확인하고 playlist.url을 **로그인된 같은 계정**으로 열어 private 여부와 곡 순서를 확인합니다. 오류/불명/부분 실패라면 전체 POST를 그대로 재전송하지 않습니다.

curl의 아래 예는 **미인증 401 확인용**입니다. 실제 로그인 브라우저 cookie를 curl이 자동 공유하지 않으므로 실제 write 검증은 위 절차를 권장합니다.

```sh
curl -i -X POST http://localhost:8080/api/v1/playlists \
  -H 'Content-Type: application/json' \
  --data '{"title":"Sync Test Playlist","tracks":[{"video_id":"VALIDATION_ONLY"}]}'
```

### 테스트와 Android API contract

model/service/handler/SDK fake tests는 정상 1/10/20곡, 입력 순서와 중복 제거, private 기본값/public/unlisted, title/description/privacy/개수 validation, session/token/refresh, 생성 실패, 첫/중간 item 실패, partial response, quota 중단, context 취소, secret 비노출, SDK body/position/ID와 write 재시도 없음을 검사합니다. 실제 SDK 요청은 로컬 fake provider로만 전달합니다.

Android/frontend 계약은 `/analyze` → `/recommend` → **사용자 선택/순서 변경** → `/playlists`를 유지합니다. 선택된 video_id만 tracks에 보내고 HTTP 201이어도 partial/items를 확인해야 합니다. 401이면 재연결을 안내합니다. 브라우저의 OAuth session cookie를 Android 앱이 자동 공유하지는 않으므로 Android의 인증/세션 전달은 후속 단계에서 별도로 설계해야 합니다. OAuth secret/token을 Android에 복사하는 구조를 사용하지 않습니다. 현재 Android OAuth, 사용자 DB, 영구 token store, player, playlist 수정/삭제 UI는 구현하지 않습니다.


### Playlist 단계 실행 검증 (2026-10-03)

- gofmt / go mod tidy / go test ./... / go vet ./... / 서버 빌드 통과.
- auth/client/service/handler/router race 검사 통과. 테스트에서 실제 Google write는 없음.
- 서버 8080 실행: health HTTP 200, OAuth status 미연결 HTTP 200, /playlists 미인증 HTTP 401 YOUTUBE_NOT_CONNECTED 확인.
- 기존 recommend의 실제 Vertex/YouTube 연동 HTTP 200, 검증된 후보 4곡 반환 확인. 기존 analyze 요청 validation 및 전체 unit test 통과.
- browser-test.js 구문 검사 및 프로젝트 실제 credential 미포함 검사 통과.
- 서버 교체 시 SIGINT graceful shutdown 확인. 최종 서버는 8080에서 실행 중.
- **실제 사용자 계정 수동 integration 성공 확인**: 사용자가 OAuth 재연결 후 browser-test.js로 최신 recommend 결과의 1곡을 선택했습니다. 사용자 제공 API 응답에서 private `Sync Test Playlist` 생성, submitted_count=1, requested_count=1, added_count=1, failed_count=0, partial=false를 확인했습니다. 이는 실제 API 응답에 기반한 확인이며 YouTube 화면에서의 콘텐츠/순서 확인과는 구분합니다. 실제 playlist ID와 사용자 계정 정보는 문서에 저장하지 않습니다.


## 사진 기반 전체 E2E 검증

기능 추가가 아닌 실제 검증용 `scripts/e2e-public.sh`는 필수 환경변수 존재 여부(값 비출력)와 callback origin 일치를 확인한 뒤 health → 실제 이미지 analyze → 결과 validation → 실제 analyze JSON으로 recommend → 상위 3개 IDs의 독립 videos.list 조회를 수행합니다. 기본 산출물은 `/tmp/sync-e2e`입니다. 실패하면 이후 단계를 진행하지 않습니다. 기존 서버의 config를 변경하거나 로그인 cookie/token을 읽지 않습니다.

```sh
./scripts/e2e-public.sh /absolute/path/to/test-image.jpg
```

재현 시 기존 Vertex/YouTube/OAuth 환경변수를 OS에 설정해야 합니다. script는 별도 서버를 시작하지 않으므로 먼저 서버를 실행합니다. 실제 API 비용/쿼터가 발생하는 명시적인 개발 테스트입니다. 현재 실행의 권한 있는 browser 단계는 `scripts/e2e-authenticated.js`에 해당 사진의 실제 추천/독립 검증된 3개 video_id를 고정했습니다. 이것은 이번 테스트 전용 snapshot이며 다른 사진/새 테스트에서는 playlist-request.json으로 request 부분을 교체해야 합니다.

OAuth 재연결 후 같은 localhost 페이지 Console에서 authenticated.js 전체를 실행하고 `await syncE2E.run()`을 명시적으로 실행합니다. private playlist 1개만 생성하며 cookie/token을 출력하지 않습니다. write는 자동 재시도하거나 rollback하지 않습니다. 결과 JSON을 기록한 뒤 playlist.url에서 private와 곡 순서를 확인합니다. 테스트 서버를 종료해 8080 포트도 정리하고, 만들어진 playlist는 보존합니다.

검증 중 첫 추천이 0곡이어서 write를 중단했습니다. 진단 검색은 19개 중 13개가 12분 초과, 3개가 90초 미만이었습니다. 검색어에 [YouTube 공식 NOT 연산자](https://developers.google.com/youtube/v3/docs/search/list)의 `-mix -playlist -compilation`을 적용하는 최소 수정과 suffix 길이 회귀 테스트를 추가했습니다. 기존 길이/지역/category/embeddable 검증 및 검색 호출 상한은 유지합니다. 새 빌드의 전체 공개 E2E에서 9/10곡을 반환했고 상위 3개를 별도 videos.list로 재확인했습니다.

### 전체 E2E 최종 결과 (2026-10-03)

실제 사진 → Vertex 분석 → 추천 9곡 → 상위 3곡 독립 YouTube 조회 → 새 브라우저 OAuth 및 YouTube 연결 검증 → private playlist 1개 생성 → 순차 3곡 추가까지 PASS입니다. 서버 로그에서 HTTP 201, added=3, failed=0을 확인했고 사용자가 실제 YouTube 화면에서 private 설정과 곡 순서를 확인했습니다. gofmt/tidy/test/vet 및 service race 검사는 통과했습니다. 테스트 서버는 graceful shutdown 후 8080 listener가 없음을 확인했고 playlist는 보존했습니다. 이번 write의 예상 quota는 200 units이며 검색/조회 및 실패 진단 비용은 별도입니다.


## 추천 품질 랭킹

검색은 기본 2회 × 최대 20개로 최대 40개 raw candidate를 확보합니다. 첫 검색은 분석/preference의 장르와 언어를 사용한 broad query + viewCount 순서이고, 두 번째는 AI 또는 fallback의 분위기 query + relevance 순서입니다. [공식 search.list order](https://developers.google.com/youtube/v3/docs/search/list)를 사용하며 AI가 임의의 곡/아티스트를 생성하지 않습니다. ID 중복 제거 후 `videos.list`의 `snippet,contentDetails,status,statistics`를 조회합니다. 기본 40개는 한 batch이며 query 3개 설정 시 50개씩 최대 두 batch입니다. 기존 `/recommend` 응답 계약은 유지합니다.

순수 함수 패키지 `internal/recommendation`은 분위기 0.45, 인기도 0.20, 신뢰 0.15, engagement 0.10, 다양성 0.10을 사용합니다. 조회수와 좋아요는 pool 안에서 log10(count+1)을 min/max normalize합니다. 조회수 없음/0은 인기도 0이며 제거하지 않습니다. Go SDK에서 미공개 likeCount와 0은 구분되지 않으므로 모두 unavailable로 처리하고 engagement 중립값 0.5를 사용합니다. licensedContent는 trust의 작은 가산 신호이며 false를 제거하지 않습니다. Topic/VEVO 및 official 문구는 heuristic만 제공합니다. [YouTube video resource 문서](https://developers.google.com/youtube/v3/docs/videos)의 필드를 사용합니다.

분위기 점수는 장르/태그의 metadata 문구 일치, 언어 metadata, 약한 검색 순위를 사용합니다. 오디오의 실제 분위기나 곡의 인지도를 확정하는 모델이 아닙니다. 같은 channelId는 기본 최대 2곡을 우선 선택하며 대체 채널이 없을 때만 완화합니다. pool 상대 순위로 high/medium/discovery bucket을 나누고 분위기 일치가 높은 미노출 bucket에 작은 다양성 보너스를 제공합니다. 비율을 강제하거나 낮은 조회수 곡을 일괄 삭제하지 않습니다.

AI cover/generated 문구는 title+description에서 경계 기반으로 검사합니다. 단순 AI 두 글자는 제거하지 않습니다. slowed/reverb/sped-up/nightcore/loop/mix/playlist/fanmade/karaoke 등은 **title**에서 검사합니다. 설명에 공식 playlist 링크가 있다는 이유로 정상 곡을 제거하지 않기 위해서입니다. remix는 0.03 penalty만 적용하고 lyrics는 제외하지 않습니다. 이 필터는 AI/원곡 여부를 완벽히 판별하지 못하며, 곡 제목 자체에 해당 단어가 있으면 오탐 가능성이 있습니다. category=Music, public, non-live, embeddable, 지역 및 90~720초 조건을 유지합니다.

APP_ENV=development일 때 내부 score component와 video ID/조회수/license boolean만 로그에 출력합니다. secret 및 이미지 bytes를 출력하지 않으며 운영 응답에 내부 점수를 추가하지 않습니다. query generator 실패 시 장르 1개·태그 1개·언어를 조합하는 bounded fallback을 사용합니다. query timeout은 6초, 전체 recommendation timeout은 기존 15초입니다.

품질 E2E는 같은 사진의 실제 `/analyze` 응답으로 `/recommend`를 호출하고 최종 후보 전체를 YouTube metadata로 독립 재검증합니다. 새 E2E playlist는 private **Sync Recommendation Test**, 최종 Top 3만 추가합니다. 서버 재시작 후 OAuth 재연결이 필요합니다. `scripts/e2e-authenticated.js`는 테스트마다 해당 실행 payload로 갱신해야 하며 한 번의 명시적 호출만 수행하고 write retry/rollback하지 않습니다.

이번 실제 테스트에서 기본 Vertex 20초와 테스트 40초 상한에서 timeout이 관측되어, 최종 E2E 서버는 기존 환경변수 VERTEX_TIMEOUT_SECONDS=90으로 실행했습니다. 기본 설정은 20초를 유지합니다. 외부 응답 시간은 변동하며 timeout 시 recommendation/playlist 쓰기를 진행하지 않습니다.

### 추천 품질 개선 최종 검증 (2026-10-03)

전체 E2E PASS: 새 실제 분석 → 추천 10곡 → 10개 ID/statistics 독립 재검증 → 새 OAuth/channels.list 검증 → private Sync Recommendation Test 1개 생성 → Top 3 순차 추가(3 성공/0 실패). 사용자가 실제 곡 순서도 확인했습니다. 최종 10곡은 명시적 AI/변형 문구 0건, licensedContent 10/10, unique channels 10개, 최대 반복 1회, 조회수 중앙값 50,203,423.5회였습니다. 이는 source metadata의 신호이며 공식성/비AI 여부나 오디오 적합도를 보장하지 않습니다. 발견형 노출이 부족한 한계는 남습니다. 테스트 서버는 graceful shutdown 후 8080 포트가 비었음을 확인했고 테스트 playlist는 보존했습니다. 기본 20초 Vertex timeout의 간헐적 실패도 기록했으며 최종 검증 설정은 90초였습니다. 단위·회귀 테스트 및 vet, service/ranker race 검사는 통과했고 write retry/rollback은 수행하지 않았습니다.

## Vertex latency benchmark (explicit paid integration)

`cmd/benchmark` reuses a Vertex client per profile, bounds workers to 1–4, uses the production prompt/schema/validation, and resumes exact profile/image/repetition rows from JSONL to avoid repeated costs. It never saves raw images or credentials in results. Default `go test ./...` performs no real inference.

```sh
go run ./cmd/benchmark \
  -config examples/benchmark-config.json \
  -dataset /absolute/path/to/image-manifest.json \
  -profiles baseline \
  -out /tmp/sync-benchmark/screening
python3 scripts/benchmark-summary.py /tmp/sync-benchmark/screening
```

Manifest is an array of `{ "id": "image-01", "path": "/absolute/path/photo.jpg" }`. Keep original photos outside the repository. Baseline requires 12 distinct images × 2 repetitions. Then use `-profiles raw-medium,low-1920`, followed by `low-1280,lite-1280`; 1024 is conditional on quality and latency acceptance. Finalist settings use 20 images × 3 repetitions. Benchmark JSON config controls model/thinking/dimension/deadline/retry; no model candidate is hardcoded in the runner. `examples/benchmark-thresholds.json` centralizes Sync's experimental thresholds, not Google guarantees. p95 with 24/60 samples remains an estimate. Percentiles exclude failed calls and separately report censored deadlines, success/failure/timeout counts and reference coverage.

Production defaults remain MEDIUM, 1920px, 20-second budget, SDK max 3 attempts until quality acceptance. Configurable `VERTEX_THINKING_LEVEL`, `VERTEX_RETRY_MODE=sdk|budget`, `VERTEX_RETRY_ATTEMPTS` validate at startup; 3.8+MINIMAL is rejected. budget mode allows 1–2 attempts and forces SDK to one attempt; only fast transient HTTP failures with sufficient remaining budget may retry. The fallback wrapper is available to test, but must not be enabled without measured quality/p95 acceptance. It never hedges or hides credential errors.

Instrumentation logs timing/count/status/model/thinking/dimensions/byte counts/token usage, never image content, credentials, token strings or Authorization headers. `retry_wait_ms` is an estimated gap, not a server-provided duration. Query generation is a second Vertex call in recommend, with its own 6-second limit inside the overall 15-second recommendation budget.

`scripts/benchmark-recommend.py` sends saved successful analysis JSON to the real `/recommend` endpoint, independently verifies final YouTube metadata, and saves blind-review outputs without configuration labels or invented human ratings. Start the server, then run explicitly with YOUTUBE_API_KEY in the OS environment. Its representative image subset is stated in results; it does not call YouTube for every inference repetition.


2026-10-03 actual benchmark decision: retain `gemini-3.8-flash / MEDIUM / 1920 / 20s / SDK 3 attempts`. LOW and Flash-Lite did not pass the combined quality/stability thresholds. Successful-call latency alone is insufficient: finalist MEDIUM timed out in 18/60 and LOW in 19/60. The internal UX target remains unmet. No 90-second production timeout, model fallback, hedging, prompt/schema contraction or automatic model selection is enabled. See `../benchmark-results/summary.md` for raw result links, limitations and final HTTP E2E timing.

Deterministic recommendation queries remain a **separate experiment candidate**. The current production AI query generator is retained; final E2E measured its extra 1.39 seconds separately. Compare identical saved analyses with/without AI queries, YouTube candidate quality and blind review before removing it.

## Recommendation V2: absolute reaction signals and query A/B

The image analyzer remains gemini-3.8-flash / MEDIUM / 1920 / 20s, SDK3, Vertex global. API JSON and OAuth/playlist contracts are unchanged.

`RECOMMENDATION_RANKER=v2` selects the measured new ranking policy; `legacy` allows comparison/rollback. `RECOMMENDATION_QUERY_MODE=gemini` retains the existing generator and fallback. `deterministic` is an experimental candidate, not selected before blind mood-fit acceptance.

V2 mood/popularity/trust/engagement/diversity weights are .45/.25/.15/.05/.10. Popularity combines relative log views .4 with absolute log anchors .6: 0→0, 1k→.08, 10k→.30, 100k→.60, 1m→.85, 10m→1. Likes similarly combine relative/absolute signals; 0/5/20/100/1k/10k map to 0/.08/.25/.60/.85/1. All weights/anchors/penalties are in `internal/recommendation/policy.go`.

View penalties are .12 below1k, .06 below10k, .015 below100k; like penalties .05 below5, .025 below20. These are initial experimental soft penalties, not quality guarantees. Composite gate excludes only views<1k AND known likes<5 AND no trust heuristic. Unknown SDK zero/omitted likes/comments stay nil; absence is not asserted to mean zero and does not satisfy the compound gate. Positive comments give only a very small bonus, zero comments never cause exclusion. Smoothed like/view ratio is omitted to keep small-sample noise out.

Discovery (<10k views) is limited to ceil(requested_count*.2), minimum1, without forcing a quota. Medium is10k–100k; established≥100k. After the initial comparison, a .30 minimum combined score was validated by offline re-ranking the identical saved pools (initial results preserved). Missing qualified candidates produce partial/empty results instead of low-reaction refill. Licensed/Topic/VEVO/official phrases are heuristics, not ownership verification; channel is not necessarily artist identity. Existing content/duration/region/embeddability filters and channel max2 (relaxed only if scarce) remain.

Explicit paid benchmark uses **saved** analysis; it never calls image inference:

```sh
go run ./cmd/recommend-benchmark \
  -dataset ../benchmark-results/recommendation-v2/saved-analyses.json \
  -out /tmp/sync-query-ab -images 12
python3 scripts/recommendation-ab-summary.py /tmp/sync-query-ab
```

Set ADC/GOOGLE_CLOUD_PROJECT/YOUTUBE_API_KEY in OS environment. Two searches per mode/image, no pagination/refill. Existing complete rows resume without new calls. Upstream failure is saved; quota/429 classification stops execution. `-retry-failed` explicitly permits one read-only retry with the failed outcome preserved under `failures/`; it is not a production retry policy and never applies to writes.

`query-ab-review.md` hides modes and leaves three human ratings blank. Mapping is separate. Review actual audio for mood fit/familiarity/listening preference; views and lexical mood matches alone cannot certify recommendation quality. See `../benchmark-results/recommendation-v2/summary.md` for observed baseline/search/ranking/latency limitations.

YouTube statistics definitions: https://developers.google.com/youtube/v3/docs/videos#statistics . Installed official SDK exposes positive counts, but its zero-valued integer fields cannot distinguish omitted statistics from explicit0, so missing/zero are recorded as unknown.

### Query generator만 비교하는 벤치마크

저장된 ImageAnalysis 12개를 그대로 사용합니다. YouTube 검색/상세 조회, 이미지 분석,
사용자 OAuth, playlist write는 호출하지 않습니다. `--live`를 붙였을 때만 Vertex 텍스트 생성
36회(12개 × 현재 production timeout 6초/10초/15초)를 호출하며 각 요청은 SDK 1 attempt입니다.
실패는 그대로 저장하고 production fallback은 실행하지 않습니다. production 설정은 변경하지 않습니다.

프로젝트 디렉터리에서 실행합니다. ADC 경로와 프로젝트는 OS 환경변수로 설정하세요.

```sh
export GOOGLE_APPLICATION_CREDENTIALS="/absolute/path/to/service-account.json"
export GOOGLE_CLOUD_PROJECT="YOUR_PROJECT"
export GOOGLE_CLOUD_LOCATION="global"
export VERTEX_MODEL="gemini-3.8-flash"
go run ./cmd/query-benchmark \
  --dataset ../benchmark-results/recommendation-v2/saved-analyses.json \
  --out ../benchmark-results/query-generator-offline-new \
  --live
python3 scripts/query-benchmark-summary.py \
  --results ../benchmark-results/query-generator-offline-new \
  --dataset ../benchmark-results/recommendation-v2/saved-analyses.json
```

출력 디렉터리와 보고서가 이미 존재하면 덮어쓰기를 거부합니다. `--live` 없이 실행하면
외부 호출 없이 deterministic 결과 12개만 기록합니다(전체 48건을 요구하는 summary는
live 실행 완료 자료용입니다). 일반 `go test ./...`에는 유료 벤치마크가 포함되지 않습니다.
블라인드 검색어 리뷰는 사전에 고른 15초 profile과 deterministic을 비교하고 점수는 공란으로 둡니다.
리뷰 완료 전 key/summary/raw를 열지 마세요. 아티스트/곡명 환각 여부는 문자열만으로
확정하지 않으며 사람이 검토해야 합니다. CSV 점수는 각 사진·Generator의 첫 행에만 입력하세요.

### Controlled MusicProfile v2

이미지 분석은 visual → mood → musical attributes → genre 순서로 수행하고,
장르 12개 대분류·54개 항목(other/unknown 포함)과 mood 22개를 controlled vocabulary로 사용합니다.
기존 `mood.tags`와 문자열 `music_profile.genres`는 유지하며 `schema_version`, primary/secondary,
`genre_candidates`(category/name/score), contrast/saturation, instrumental preference, confidence를 추가했습니다.
점수·confidence는 모델 보고 신호이며 통계적으로 보정된 확률이 아닙니다.

deterministic builder는 genre score 순서, confidence, mood/tempo, vocal/instrumental, 언어 preference를
사용합니다. unknown의 raw_label은 진단 전용이며 검색어에 넣지 않습니다. legacy adapter는 없는
점수/confidence를 만들어 넣지 않습니다. 기본 query mode와 V2 ranker weight는 유지합니다.

- [구조·호환성·오프라인/반복 consistency 검증 방법](docs/music-profile-v2.md)
- [새 분석 형식 예시 — 실제 Gemini 출력 아님](examples/enhanced-analysis.json)

```sh
# 저장된 분석 12개 검증 및 query 생성만 수행: 외부 API 호출 없음
go run ./cmd/profile-benchmark \
  --dataset ../benchmark-results/recommendation-v2/saved-analyses.json \
  --out ../benchmark-results/music-profile-v2-new
```

`RECOMMENDATION_QUERY_MODE=deterministic`로 선택하면 이미지 분석 이후 두 번째 Gemini 호출 없이
검색어를 생성합니다. 언어 키워드는 `RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS=true`(기본값)로 관리합니다.
실제 Vertex 품질·반복 일관성·YouTube 추천 품질은 별도 후속 검증 대상입니다.

## Recommendation pipeline v3 / offline development

기본 recommendation은 deterministic query + adaptive retrieval + V2 + vocal mix다.
개발 기본값은 `RECOMMENDATION_DATA_MODE=fixture`, `YOUTUBE_LIVE_SEARCH_ENABLED=false`이며
추천 개발에 YouTube API key가 필요하지 않다. `/analyze`, OAuth, playlist는 별도 기능이다.

`preferences.vocal_mode`는 `mixed`(기본), `vocal-first`, `instrumental-first`, `instrumental-only`를 지원한다.
사진의 과거 vocal preference는 검색 정책에 사용하지 않는다. fixture 응답은 `data_mode: "fixture"`와
SYNTHETIC title로 표시하며 실제 음악 추천/playlist 입력으로 사용하지 않는다.

새 ImageAnalysis v3, 24 moods/61 genres, retrieval weights, cache TTL, process budget,
replay 파일 형식, 검색 호출 예상 및 오프라인/실제 품질 검증 절차는
[Recommendation pipeline v3](docs/recommendation-pipeline-v3.md)를 참고한다.
기존 문서/benchmark의 v2 및 Gemini 기본값 설명은 이전 실험의 기록이며 현재 기본값은 이 절을 따른다.

### Retrieval v2

`YOUTUBE_SEARCH_MAX_RESULTS=50` (1–50) increases candidates per request without pagination. `RECOMMENDATION_MIXED_QUERY_KEYWORD=song` accepts `song` or `music` for mixed mode; vocal-first uses song, instrumental modes use music. Controlled genres and image scores are unchanged. Korean-oriented genres (`k-indie`, `k-pop`, `korean-ballad`, `korean-r&b`) are assigned to a compatible Korean direction when requested; English directions use a compatible non-Korean candidate or mood/tempo fallback.

Prepared deterministic queries preserve the documented YouTube NOT operator: `-mix -playlist -compilation -backing -karaoke`. They never blanket-exclude instrumental, lyrics or remix. The [official search.list reference](https://developers.google.com/youtube/v3/docs/search/list) documents NOT (`-`) and maxResults up to 50. The SDK parameter and cache-key tests preserve these operators. Exclusion effectiveness depends on YouTube search behavior; metadata is still validated.

Title-only song-form filtering excludes explicit backing/practice/jam tracks, karaoke, tutorials/lessons and conservative long-form phrases. Descriptions often link unrelated playlists, so those links are not song-form exclusion evidence. Instrumental, lyrics, official remix, album version and official album audio alone remain allowed. Existing AI/transformation filters, 90–720-second duration, V2 weights, quality gate and vocal policy remain unchanged. Diagnostic reason counts overlap when a candidate fails multiple predicates.

Adaptive search skips Q2 if qualified candidates meet requested count + safety margin, known instrumental balance is acceptable and at least two source channels are represented. Otherwise it records quantity, instrumental or channel-diversity reason. Metadata queries batch up to 50 **new** IDs; candidate-ID cache keys include query/operators, maxResults and order.

Explicit one-shot retrieval comparison using saved analysis (no Vertex inference, no playlist writes):

```sh
# Set normal backend env, YOUTUBE_API_KEY, deterministic/live/enabled,
# YOUTUBE_SEARCH_MAX_CALLS_PER_RUN=2, YOUTUBE_SEARCH_QUERY_COUNT=2,
# YOUTUBE_SEARCH_MAX_RESULTS=50, RECOMMENDATION_ADAPTIVE_SAFETY_MARGIN=3.
go run ./cmd/live-v3-single --live \
  --saved-analysis ../benchmark-results/live-v3-single/analysis.json \
  --out ../benchmark-results/live-v3-retrieval-v2
# Output must not already exist. Never retry a quota failure with another key.
go run ./cmd/live-v3-single --report-only --out ../benchmark-results/live-v3-retrieval-v2
```

The saved analysis is marked reused; this is retrieval comparison, not fresh image E2E. Unit tests do not run this command or consume external quota.

### Recommendation v4: neutral retrieval

The server now injects `WithNeutralRetrieval` into RecommendationService. `/recommend` keeps its request/response contract. Gemini continues image analysis only; the neutral path never calls a Gemini text query generator. Existing deterministic v3 builders remain available to offline comparison tools.

Q1 discovers genre candidates: `city pop song -mix -playlist -compilation -backing -karaoke`, relevance order, maxResults 50. It has no explicit language or mood keywords and omits API `relevanceLanguage`. Playback-region validation still uses YOUTUBE_REGION. Korean-oriented taxonomy tokens may naturally contain "korean" when that is the selected genre; no additional language preference is imposed. The city-pop baseline therefore has neither Korean nor English hint.

Hard filters are unchanged. A separate release gate follows: licensedContent gives HIGH; Topic/VEVO channel-name or "Provided to YouTube by" distributor-description signals give MEDIUM; official title text alone gives LOW; otherwise UNKNOWN. These are fallible metadata heuristics, not certification of ownership, official release or human origin. This API has no universal verified Official Artist Channel field, so no verified artist-channel identity is invented. "Auto-generated by YouTube" is distribution metadata and does not indicate generative AI music. See the [official video resource documentation](https://developers.google.com/youtube/v3/docs/videos).

Production defaults allow HIGH/MEDIUM only. Missing candidates never relax that gate. TrustScore remains a separate unchanged ranking signal after eligibility. Release settings:

```text
RECOMMENDATION_REQUIRE_RELEASE_CONFIDENCE=true
RECOMMENDATION_ALLOWED_RELEASE_CONFIDENCE=HIGH,MEDIUM
RECOMMENDATION_MIN_MEDIUM_ESTABLISHED=6
RECOMMENDATION_MIN_LANGUAGE_COVERAGE_PER_PREFERENCE=2
RECOMMENDATION_Q1_ORDER=relevance
RECOMMENDATION_Q2_POPULARITY_ORDER=viewCount
```

Configured order values are validated against those role-specific policies. Candidate sufficiency requires requested count + safety margin after release/quality/vocal eligibility, at least requested count HIGH/MEDIUM hard-eligible, minimum medium/established candidates, at least two channels, plausible preferred-language coverage, and no >40% known instrumental pool bias for mixed/vocal-first. Coverage thresholds are explicit configuration, not hard per-language output quotas.

Language evidence separates reported audio language (`defaultAudioLanguage`), upload text language (`defaultLanguage`), title/description scripts, retrieval intent and genre orientation. Only reported audio language with no strong instrumental/non-song contradiction contributes to plausible preferred vocal-language coverage. Upload text, scripts, queries and genre hints never fill coverage thresholds. Latin script is not assigned to English. Missing evidence stays unknown; even reported audio metadata does not certify actual singing language. Diagnostic affinity remains available but is not used for coverage sufficiency.

If Q1 is insufficient, Q2 prioritizes candidate count, release trust, popularity strength and diversity before plausible language coverage. A weak core pool uses the same neutral top-genre query with viewCount order; language coverage is used only after the core pool is sufficient. Language coverage searches a compatible direction with relevance order; popularity rescue reuses the broad neutral genre query with viewCount order; secondary genre uses the registry and relevance order. All deficient axes and the priority explanation remain in opt-in traces. At most two searches per recommendation, no pagination. Cache keys preserve order, NOT operators and maxResults; identical query+order is not repeated, and metadata fetches only new IDs in batches <=50.

V2 weights, reaction anchors, discovery cap, minimum score, channel diversity and vocal mix remain unchanged. Channel diversity occurs within the existing V2 selection; no extra post-vocal reorder or unreliable artist-name parser is introduced. Mood audit exposes search-rank contribution, title/text mood and genre matches and language metadata contribution. Tempo/energy audio compatibility is **not currently scored**; those unsupported measurements are labelled absent rather than invented. The formula is not automatically tuned.

Single controlled live validation (requires external env/key, default unit tests never execute it):

```sh
go run ./cmd/live-v3-single --live --neutral-retrieval \
  --saved-analysis ../benchmark-results/live-v3-single/analysis.json \
  --out ../benchmark-results/recommendation-v4-neutral-retrieval
go run ./cmd/live-v3-single --report-only \
  --out ../benchmark-results/recommendation-v4-neutral-retrieval
```

The output must be new. This reuses saved image analysis; it is not fresh image E2E. No Gemini or playlist writes. The command refuses policy values outside the controlled default release/strength settings. Preserve the first run, stop on quota errors, and do not rerun with another key.


### v4 eligibility correctness audit

Song-form checks preserve original YouTube titles and use comparison-only Unicode NFKC, camel boundaries and separator normalization. Whole-concept aliases cover BackingTrack/backing-track/backing_track, Japanese カラオケ (including halfwidth), Korean 가라오케/노래방 and practice/jam/tutorial forms. Titles are checked conservatively; description advertisements alone do not exclude a song. Instrumental, piano, acoustic and orchestral releases are not blanket-excluded. Shared matching also feeds vocal diagnostics. A HIGH licensed release cannot bypass song-form eligibility.

Offline replay reads captured metadata only and never constructs an external API client. It reranks the same captured pool; it does not simulate results of a different adaptive query. Output must be a new directory:

```sh
go run ./cmd/live-v3-single \
  --eligibility-replay ../benchmark-results/recommendation-v4-neutral-retrieval/run.json \
  --out ../benchmark-results/recommendation-v4-eligibility-fix/offline
```

The one-run eligibility validation uses saved schema-v3 analysis, deterministic queries, live YouTube retrieval, maxResults 50, at most two searches, no pagination, no Gemini inference and no playlist writes. Retrieval/ranking weights, release thresholds and vocal mix selection are unchanged.


### v4 recall / popularity rescue validation

Popularity strength requires the existing configured medium+established minimum, at least one established candidate, no majority of discovery candidates, and a median at or above the existing medium bucket boundary (10,000 views). This changes retrieval decisions only; V2 scores, eligibility gates and final-selection language preferences remain unchanged. Traces include `popularity_strength_sufficient` and all deficient axes in priority order.

Content diagnostics classify metadata hints (normal track, live/cover/street performance, reaction, type beat, AI-labelled worship, unknown) without excluding or rescoring candidates. `prod.`/worship are diagnostic cues; Topic and “Auto-generated by YouTube” alone are not AI music evidence. These categories are not audio verification.

An explicit controlled A/B command reuses the captured Q1 search and metadata and performs at most one new neutral viewCount search plus one batch for new IDs. Variant A remains the captured language-Q2 result; Variant B uses current production service through Gin HTTP. No Gemini or playlist API is called. Output must be a new directory:

```sh
go run ./cmd/live-v3-single --live \
 --popularity-ab ../benchmark-results/recommendation-v4-eligibility-fix/live/run.json \
 --out ../benchmark-results/recommendation-v4-recall-rescue/live
```

The combined `run.json` includes historical Q1 records; `quota.json`, `new-search.json` and `new-metadata.json` count only new external calls. This controlled retrieval comparison is not a fresh image E2E or a multi-photo benchmark.


Trace `sources` retains every query/order for duplicate video IDs (including Q1 relevance and Q2 popularity rescue) without refetching metadata or granting a score bonus. The first-source `source` field remains compatible. Additional `beat for sale` diagnostics are metadata-only.


### Fresh relevance / viewCount diagnostic

`go run ./cmd/live-v3-single --live --fresh-order-audit --saved-analysis ../benchmark-results/live-v3-single/analysis.json --out ../benchmark-results/recommendation-v4-fresh-order-audit/live` bypasses cache and adaptive planning only in this diagnostic command. It issues the unchanged query with relevance then viewCount consecutively, and fetches fresh metadata for their deduplicated union in <=50-ID batches. Bounded SDK transport permits at most two searches/two metadata calls and records only allowlisted non-secret request parameters/timestamps. It then applies the existing eligibility and V2/vocal selection separately to Q1, Q2 and merged pools. No history, Vertex inference, playlist writes or tuning is used. Production cache and policies are unchanged.

## Last.fm Tag Vocabulary Audit (isolated CLI)

기존 `/api/v1/recommend`와 YouTube 로직은 변경하지 않습니다. Last.fm 키는 서버 startup 필수 값이 아니며 이 CLI에서만 사용합니다. Shared Secret은 사용하지 않습니다.

```sh
# LASTFM_API_KEY를 OS 환경변수로 설정한 뒤 실행. 실제 키를 저장소에 쓰지 마세요.
go run ./cmd/tag-audit --live --fresh --out artifacts/lastfm-audit-new
```

최초 실제 실행은 `artifacts/lastfm-live-20261004/`에 보존합니다. `tag.getTopTags` 1회로 반환 목록 전체를 수집하고, 실제 global family 대표 → 14개 mood 가설 probe → 추가 상위 genre/style 순으로 최대 40개만 enrich합니다. 사용량이 양수인 경우에만 Top 10을 조회합니다. getTopTags의 문서에는 limit/page 옵션이 없으므로 추정 pagination을 사용하지 않습니다. 자동 retry는 없습니다. API key/URL/raw API error는 로그에 남기지 않습니다. 응답별 timeout은 15초이며 호출은 순차 수행합니다.

`--sample-limit` 10~20, `--max-enrich` 31~60, `--interval-ms` 최소 200, `--cache-dir`(24시간 TTL)을 지원합니다. cache identity에는 base URL, method, 정규화 tag, limit, page, JSON format이 포함됩니다. `--fresh`는 cache를 우회합니다. output directory가 존재하면 덮어쓰지 않습니다. 캐시는 공개 응답만 담고 API key는 담지 않습니다.

Seed 규칙은 provisional 분류일 뿐 자동 validation이 아닙니다. unknown은 그대로 남깁니다. reach/taggings가 없으면 null이고 실제 JSON의 `total`은 별도 보존합니다. mood와 genre의 의미/분포는 사람이 표본 metadata를 검토해야 하며 audio mood까지 검증했다는 뜻은 아닙니다.

Offline review는 외부 API를 호출하지 않습니다. review JSON은 tag별 `{ "status": "validated", "notes": "실제 표본 검토 근거" }` 형태입니다. validated는 사용량과 최소 5개 표본 근거를 요구합니다.

```sh
go run ./cmd/tag-audit --offline artifacts/lastfm-live-20261004/lastfm_tag_audit.json \
  --review artifacts/lastfm-live-20261004/semantic-review.json \
  --out artifacts/lastfm-reviewed-20261004 \
  --registry internal/music/lastfm_tag_registry.json
```

JSON/CSV/report와 registry를 생성하며 initial raw run은 수정하지 않습니다. registry의 candidate는 production retrieval allowlist로 사용하면 안 됩니다. canonical/Last.fm tag는 별도 필드이고 mapping draft에는 명시적으로 검토된 동일 개념만 포함합니다. `reflective → mellow` 같은 추측 alias는 만들지 않습니다. 새로운 태그를 Gemini가 생성하도록 변경하지 않습니다.

```sh
go test ./internal/lastfm
go test ./...
go vet ./...
```

unit test는 httptest.Server만 사용하며 실제 Last.fm 호출 수는 0입니다. 현재 단계는 최종 음악 추천·resolver·playlist 생성이 아닙니다. Last.fm만으로 playable IDs, 객관적 mood, 모든 장르 coverage를 확보할 수 있다는 가정을 하지 않습니다.

## Last.fm Photo Candidate Retrieval Experiment

Production `/api/v1/recommend`와 YouTube/Gemini 로직은 변경하지 않습니다. 이 CLI는 saved Gemini analysis를 deterministic profile로 변환하고 validated mapping만으로 Last.fm 후보 pool을 평가합니다. 새로운 Gemini 호출이나 resolver/playlist/track enrichment는 없습니다.

```sh
# LASTFM_API_KEY를 OS 환경변수로 설정한 후, 새 output directory를 사용하세요.
go run ./cmd/lastfm-retrieval-eval \
  --analysis ../benchmark-results/e2e/analyze.json \
  --photo-id still-life-saved-e2e \
  --live --fresh \
  --out artifacts/lastfm-retrieval-new
```

순수 ImageAnalysis JSON, analyze 응답의 `analysis`, benchmark wrapper의 `analysis`를 지원합니다. 기존 ImageAnalysis validation을 사용하고 schema를 바꾸지 않습니다. artifact에는 analysis subtree만 저장합니다. registry/mapping snapshot과 SHA256, policy/timeout/run configuration도 보존합니다.

기본 정책은 genre > subgenre > style > mood 순이며, 같은 category 안에서는 원래 concept weight가 큰 순입니다. Genre/style 최대 3, mood 최대 1, 총 최대 4 routes입니다. **장르가 하나도 매핑되지 않으면 기본적으로 API 호출 0회**이며 insufficient mapping을 보고합니다. Mood-only는 `--allow-mood-only`를 명시했을 때만 가능한 별도 실험입니다. Candidate/rejected/unknown 태그는 route에 사용할 수 없습니다. 이전 baseline(`--legacy-direct-mapping`)은 family fallback을 하지 않습니다. 현재 기본 genre 경로는 아래 canonical/provider resolver를 따르며 semantic alias는 만들지 않습니다.

Enhanced genre score는 model relevance로 유지합니다. Primary/secondary mood weight는 정책상 1/.7이며 Gemini가 개별 mood 점수를 준 것이라고 표현하지 않습니다. Legacy analysis에서는 genre와 mood 각각 전체 입력에 동일 weight를 배분하고, unmapped concept을 제외한 뒤 다시 normalize해서 신뢰도를 부풀리지 않습니다. Confidence, tempo, energy, valence는 보고서에 보존하되 audio 속성이 없는 현재 retrieval score에서 track fit으로 오인하지 않습니다.

각 route는 `tag.getTopTracks` page=1, 기본 limit=50으로 호출합니다. 기본 동시 요청 최대 2, route timeout=15초, 전체 timeout=45초, 자동 retry는 없습니다. Provider는 요청별 Last.fm client/event storage를 가지므로 audit client를 동시에 공유하지 않습니다. `--fresh`와 별도 retrieval cache directory로 audit cache 혼용을 방지합니다. Actual HTTP attempt / cache hit를 분리합니다. 일부 route 실패는 partial 결과로 저장하고 전부 실패하면 오류와 report를 남깁니다.

Score 공식:

```text
route weight = source relevance × category factor
category factor: genre/subgenre/style = 1.0, mood = 0.6
rank score = 1 / log2(rank + 1)
overlap bonus = min(0.15, 0.05 × (distinct route count - 1))
retrieval score = min(1, max(route weight × rank score) + overlap bonus)
```

이는 final ranker나 popularity score가 아닙니다. 동일 route 중복은 overlap bonus를 주지 않습니다. Score constants는 DefaultPolicy/Score에 모여 있으며 결과를 보고 live 중간에 튜닝하지 않습니다.

Dedupe는 NFKC/lowercase/whitespace와 typographic quote/dash normalization을 사용합니다. 괄호/feat/remaster/version은 보존합니다. 같은 MBID 또는 normalized artist/title이면 merge하고 모든 provider records/route evidence를 보존합니다. MBID가 합친 다른 title/artist는 경고합니다. MBID 누락으로 탈락시키지 않습니다.

Diversity는 원래 score 순서를 안정적으로 훑어 Top 20에서 같은 normalized artist 최대 2곡을 선택합니다. 밀린 곡들은 deferred file과 raw ranking에 남깁니다. 후보가 부족해도 cap을 몰래 완화하지 않습니다.

출력: `input-analysis.json`, `routes.json`, `raw-candidates.json`, `merged-candidates.json`, `reranked-candidates.json`, `deferred-candidates.json`, `evaluation.json`, vocabulary snapshots, `run-config.json`, `report.md`, `human-review.csv`. Human score column은 빈 칸입니다.

설정: `--per-route-limit`, `--concurrency`, `--max-genre-routes`, `--max-mood-routes`, `--max-routes`, `--artist-cap`, `--top`, `--timeout`, `--route-timeout`, `--cache-dir`, `--fresh`, `--registry`, `--mapping`. Bound를 벗어나면 외부 호출 전에 거부합니다.

```sh
go test ./internal/lastfmeval
go test ./...
go vet ./...
```

Unit test는 fake retriever/httptest.Server만 사용하며 실제 Last.fm 호출은 0입니다. Candidate pool 수집 성공을 추천 품질 성공으로 판정하지 않습니다.

## Sync Canonical Taxonomy → Last.fm Provider Mapping

Source of truth는 기존 `internal/music/taxonomy.json`입니다. 61개 ID(음악 genre 59 + other/unknown), 12개 category를 그대로 사용합니다. `sync_genre_taxonomy.json`은 여기서 생성한 **읽기용 snapshot**이며 별도로 수동 관리하지 않습니다. Family ID도 그대로 유지합니다. Bossa nova는 현재 jazz, ambient는 electronic입니다. 기존 production /api/v1/analyze·/recommend, YouTube ranking은 변경하지 않습니다.

Canonical resolver는 ID/display name/기존 Aliases만 NFKC + case/whitespace/punctuation normalization으로 resolve합니다. QueryTokens는 검색 표현이므로 새로운 canonical alias로 취급하지 않습니다. Unknown label은 unknown-observations.json에 집계할 수 있습니다. Lo-fi→lo-fi-hip-hop, urban jazz→jazz 등의 기존 explicit alias는 유지하지만 이 milestone에서 semantic alias를 추가하지 않습니다.

Provider resolver의 순서와 factor:

```text
validated exact              1.00
validated orthographic alias 0.95
validated family primary     0.65
unmapped/related/candidate   runtime 사용 안 함
```

Provider tag evidence에는 실제 reach/total/taggings, samples, concentration, duplicate/MBID diagnostics, 재사용 여부가 들어갑니다. 현재 validated는 이전 metadata-semantic 검토의 제한적 의미이며 audio-level genre나 추천 품질 인증이 아닙니다. Alias provider samples는 개별 보존합니다. Family fallback은 exact synonym이 아닌 broad retrieval입니다. 검증된 family 대표가 없으면 parent/fuzzy fallback을 만들지 않습니다.

```sh
# 기존 evidence 재사용 + probe 계획. 네트워크 호출 없음.
go run ./cmd/lastfm-mapping-audit --dry-run \
  --out artifacts/lastfm-mapping-new \
  --runtime-out internal/music/lastfm_genre_mapping.json \
  --taxonomy-out internal/music/sync_genre_taxonomy.json

# 이미 OS 환경에 LASTFM_API_KEY가 있을 때만 explicit live.
# 기존 evidence는 기본 재사용, 한 실행의 probe cap은 12개.
go run ./cmd/lastfm-mapping-audit --live --genre indie-folk \
  --sample 20 --out artifacts/lastfm-mapping-indie-folk-new
```

`--family`, `--genre`, `--sample`, `--max-probes`, `--concurrency`, `--cache-dir`, `--fresh`, `--orthographic-variants`, `--unknown-input`을 지원합니다. `--fresh`만 선택된 개념의 evidence/cache를 강제로 다시 조회합니다. 기본적으로 primary display name exact probe만 계획하며, 표기 변형 추가 조회는 `--orthographic-variants`에서 독립 evidence로 기록합니다. Semantic synonym probe는 만들지 않습니다. Positive usage가 없으면 topTracks 호출을 생략합니다. Context cancellation / 15초 request timeout / 동시 probe 최대 2(설정 1~3) / retry 없음. API key가 없으면 live 시도 없이 report를 남깁니다. 신규 probe는 candidate에 남기며 자동 validated 승격하지 않습니다.

Audit `mappings.json`, `mappings.csv`, candidates/unmapped/raw/human-review는 diagnostics용이고, `internal/music/lastfm_genre_mapping.json`은 validated exact/alias/family 정보만 가진 runtime용입니다. Sentinels와 미검증 genre ID는 unmapped placeholder로 유지합니다. Runtime file은 아직 API 서버에 연결하지 않습니다.

Candidate experiment CLI의 기본 genre path는 이제 canonical/provider resolver입니다. Mood는 기존 conservative validated-only mapping을 유지합니다. `--legacy-direct-mapping`은 이전 baseline 재현에만 사용합니다. 동일 provider tag로 resolve된 여러 canonical concepts는 단일 요청으로 묶되 모든 concept sources를 routes.json에 보존하고 가장 강한 contribution을 사용합니다. 같은 provider route를 중복 검색하거나 overlap bonus를 부풀리지 않습니다.

```sh
# 현재 키가 없을 때 saved provider responses로 offline 비교만 가능.
# 기존 folk/pop audit 표본이 10곡이라 before/after 모두 limit=10.
go run ./cmd/lastfm-retrieval-eval \
  --analysis ../benchmark-results/e2e/analyze.json \
  --replay-audit artifacts/lastfm-reviewed-20261004/lastfm_tag_audit.json \
  --replay-retrieval artifacts/lastfm-retrieval-live-20261004/evaluation.json \
  --per-route-limit 10 --out artifacts/lastfm-canonical-replay-new
```

Replay와 live는 혼합하지 않습니다. 부족한 captured sample을 fixture로 채우지 않으며 실패/partial을 명시합니다. Replay는 actual API call/cache hit로 계산하지 않고, local 처리 latency를 Last.fm latency로 표현하지 않습니다. 인간 평가 점수는 빈 칸입니다. 추후 키가 OS 환경에 설정되면 같은 saved analysis로 `--live --fresh --per-route-limit 50`을 실행할 수 있습니다. Gemini를 다시 호출하지 않습니다.

이번 offline 결과는 `artifacts/lastfm-mapping-offline-20261004/`와 `artifacts/lastfm-canonical-regression-20261004/`에 보존합니다. Mapping coverage 개선은 상당 부분 broad fallback이며 추천 품질 개선이라고 선언하지 않습니다.

### 세부 장르 specificity 조사 (실험 전용)

```sh
# LASTFM_API_KEY는 실행 프로세스의 OS 환경변수에 미리 설정합니다.
# 이 명령은 실제 API를 호출합니다. 새 output 경로를 사용하세요.
go run ./cmd/lastfm-specificity-audit --live \
  --out artifacts/lastfm-specificity-new
```

`indie folk`, `chamber pop`, `bossa nova`의 공백/하이픈/붙여쓰기 9개 표기와 비교용 `folk`, `pop`, `jazz`만 fresh 조사합니다. 최대 12 probes × 2 calls = 24 HTTP 호출이며 sample 20, 동시 2개, 요청 timeout 15초, retry 없음입니다. Positive usage가 없으면 Top Tracks를 조회하지 않습니다. API key는 환경변수로만 읽고 저장하지 않습니다.

새 증거는 candidate/unmapped 상태로 저장하며 기존 runtime registry를 수정하지 않습니다. 가장 충분한 표본과 reported usage를 가진 표기는 **제안**일 뿐 genre purity 판정이 아닙니다. Saved photo 비교는 이 표본과 이전 acoustic/dreamy 표본을 사용한 mixed-vintage replay입니다. 별도 `proposed-experiment/`에서만 local planner adapter로 candidate 조회를 허용하며, 저장된 신규 genre evidence/status는 candidate입니다. 이는 validated 승격이나 fresh 전체 E2E가 아닙니다. 기존 3 genre + 1 mood 예산, 점수와 다양성 정책을 유지해 route 선택 변화도 보고합니다.

최초 결과: `artifacts/lastfm-specificity-live-20261004/summary.md`, `specificity.json`, `proposed-mapping.json`, `quota-timing.json`, `human-review.md`. 사람 검토 점수는 비워 두었습니다. `runtime-mapping.json`은 해당 audit의 filtered diagnostic snapshot이며 production 파일을 대체할 승인된 결과가 아닙니다.

### 자동 Last.fm retrieval eligibility policy v1

Human review를 검색 경로의 필수 조건으로 사용하지 않습니다. `internal/music/lastfm_retrieval_registry.json`은 새로운 experiment용 자동 registry(version 2)이고, 기존 `lastfm_genre_mapping.json`은 변경 전 legacy baseline으로 보존합니다. API 서버의 `/recommend`는 계속 기존 YouTube 경로를 사용합니다.

```sh
# 저장된 evidence 평가, 보완 계획 생성, 동일 saved-photo offline regression
go run ./cmd/lastfm-eligibility-audit

# OS LASTFM_API_KEY가 이미 설정된 경우에만 보완 live. 최대 24 HTTP attempts.
# --fresh는 선정된 부족한 observation에만 적용합니다.
go run ./cmd/lastfm-eligibility-audit --live --fresh
```

정책은 `internal/lastfmeval/eligibility.go`의 `RetrievalPolicyV1()`에 있습니다. 성공한 info/TopTracks, requested >=20, valid unique tracks >=15, duplicate ratio <=10%가 기본 조건입니다. Reach >=5000 및 unique artists >=8이면 eligible; reach 1000..4999 또는 충분한 기본 표본에서 artists <8이면 provisional; <1000/누락/기본 observation 부족이면 insufficient_evidence입니다. Top1 >40% 경고, >60%이면 provisional로 제한합니다. Duplicate ratio의 분모는 정상 제목/아티스트가 있는 받은 레코드 수이며, dedupe는 기존 normalized artist/title 및 transitive MBID identity를 재사용합니다. Low usage/API 실패/편중만으로 rejected로 처리하지 않습니다.

Reach threshold는 초기 운영 정책이며 음악 품질의 경계가 아닙니다. `total`/`taggings`/`reach`는 독립 nullable 필드로 유지합니다. API eligibility와 semantic/human review를 분리하고, 이전 validated를 자동 eligible로 간주하지 않습니다. Equivalent 표기는 status → completeness(Top20 범위) → artist diversity → severe concentration → reach → exact spelling tie-break → lexical 순서입니다. Related/fuzzy 태그는 제외하고 기존 explicit alias와 family 관계만 사용합니다. Factor 1/.95/.65는 유지합니다.

`lastfm-retrieval-eval`은 이제 기본적으로 자동 registry를 사용합니다. `--allow-provisional`은 실험 전용 opt-in입니다. 이전 canonical baseline은 `--legacy-provider-policy`(기본 legacy 파일)로, 가장 초기 direct mapping은 `--legacy-direct-mapping`으로 재현합니다. Legacy 예외는 명시적 baseline 옵션에서만 허용됩니다.

```sh
go run ./cmd/lastfm-retrieval-eval \
  --analysis ../benchmark-results/e2e/analyze.json \
  --replay-audit artifacts/lastfm-reviewed-20261004/lastfm_tag_audit.json \
  --replay-retrieval artifacts/lastfm-retrieval-live-20261004/evaluation.json \
  --per-route-limit 20 --out artifacts/automatic-retrieval-new
```

이 예시 replay 입력에는 최근 specificity 표본이 없어 새 eligible route가 partial일 수 있습니다. 모든 최신 표본을 합친 matched regression은 `lastfm-eligibility-audit`이 생성하는 regression-before/after를 사용합니다. Mixed-vintage/표본 부족을 명시하고 fixture로 채우지 않습니다. Offline 실행에서는 실제 API call=0이며 저장 evidence 재사용을 response cache hit로 세지 않습니다. 새 timestamped artifacts에는 policy/before/offline decisions/missing/live plan/updated registry/coverage/regression/quota/summary를 저장합니다. 새 키를 코드나 결과에 저장하지 않으며 OS 키가 없으면 live를 생략합니다.

### Policy v1 core-family evidence supplementation

`go run ./cmd/lastfm-eligibility-supplement --live --fresh` probes only the prior
observed `Classical`, `Hip-Hop`, `hip hop`, and `electronic` representations.
Existing successful getInfo/reach is reused; missing Top20 snapshots are fetched
independently, with a hard cap of 12 HTTP attempts and no retries. `--fresh`
only affects this scoped missing-evidence plan. `LASTFM_API_KEY` must be in the
process environment; `.env` is not automatically loaded. Without `--live` or a
key, this command records an offline assessment and plan with zero API calls.

A new `artifacts/lastfm-eligibility-supplement-<UTC timestamp>/` preserves raw
observations, independent Hip-Hop spelling statistics, v1 comparator inputs,
family coverage changes, a captured still-life replay, synthetic canonical-ID
resolver regressions, and API accounting. Existing audit artifacts and the legacy
mapping registry are preserved. The experiment-only automatic retrieval registry
is recalculated with unchanged v1 thresholds and the existing explicit family
graph. The public recommendation API is unaffected. Evidence sufficiency does not
certify semantic genre accuracy or recommendation quality.

### Final high-leverage Last.fm evidence batch

`go run ./cmd/lastfm-final-evidence-batch --live --fresh` consumes the preserved
core-family supplement observations and first verifies they reproduce the current
experiment registry. It ranks only remaining insufficient equivalent provider
candidates. Selection is frozen before HTTP calls: at most six tags, at most
16 HTTP attempts, no retries. Existing info is reused; only missing Top20 samples
and missing info are fetched. No alternate spelling probes are invented.

Priority is an audit scheduling heuristic:
`100*new_family_fallbacks + 10*unmapped_recovery + 50*unique_saved_analysis_usage
+ 5*reusable_info + equivalent_upgrade_count`.
Potential recovery is a synthetic resolver counterfactual, not fabricated API
evidence. Duplicate saved analysis snapshots are counted once; this small corpus
is not production telemetry. Known reach below the eligible threshold and already
complete but failing samples are not refetched to chase eligibility.

Outputs under `artifacts/lastfm-final-evidence-batch-<UTC timestamp>/` include
priority/selection, independent raw evidence, per-tag coverage attribution,
family resolver replay, unchanged still-life replay, API accounting and a freeze
readiness assessment. There are no Gemini/YouTube calls and no public API changes.
The experiment-only registry is refreshed with the unchanged policy and family
graph. Prior artifacts and the legacy mapping registry remain intact. This command
is a scoped milestone with an explicit source snapshot, not a production registry
maintenance scheduler; future audits must choose a matching observation snapshot.

### Frozen Last.fm baseline and unsupported profile coverage

`go run ./cmd/lastfm-freeze-readiness` performs an entirely offline freeze/readiness
report. No API key is needed; no Last.fm, Gemini or YouTube requests are made.
A new `artifacts/lastfm-freeze-and-ranking-readiness-<UTC timestamp>/` preserves the
exact registry bytes, policy v1, taxonomy/registry/evidence SHA256 identities,
deduplicated saved-fixture coverage, unsupported observations, night-city and
captured still-life regressions, candidate signal inventory and ranking readiness.
The current experiment registry is not rewritten. The saved evidence snapshot must
reproduce it; mismatches stop the command. Prior artifacts remain intact.

Automatic Last.fm plans now expose stable resolver reason codes and genre coverage:
`FULL`, `PARTIAL`, or `INSUFFICIENT_ROUTES`. Unsupported canonical genres create no
provider route and consume no route budget; their original weight stays in the
coverage denominator. Remaining usable weights are never renormalized. Sentinels
other/unknown are separately excluded, and noncanonical inputs are separately
observed. Coverage includes all profile inputs before budget selection; it does not
prove audio match or recommendation quality. Legacy uniform-weight coverage is
explicitly diagnostic; model relevance is not calibrated confidence. Existing
explicit mood-only experiment policy remains unchanged, with zero usable genre
routes still reported as `INSUFFICIENT_ROUTES`. Transport partial failures are a
separate field from profile coverage.

CandidateTrack adds metadata-only `retrieval_provenance`: distinct provider route
counts, exact/alias/family/mood specificity, original/effective weight, unchanged
mapping factor and provider rank decay, plus all canonical sources for shared tags.
Existing identity, RetrievalScore, rank order and artist diversity are unchanged.
Direct legacy plans without sufficient provenance remain `unclassified`; no exact
mapping is invented. Enrichment/popularity/audio/resolver trust signals remain absent.

Use the frozen registry in the existing experiment CLI:
`go run ./cmd/lastfm-retrieval-eval --provider-mapping <snapshot>/frozen-registry.json`
with its usual analysis/captured-data arguments. Frozen means reproducible baseline,
not permanently immutable or fully supported taxonomy. Further provider audits are
paused. Reopen only on repeated independent unsupported observations/high missing
profile weight, explicit evidence need or a planned policy version change. Saved
fixtures are not production telemetry. Production `/api/v1/recommend`, YouTube,
Gemini schema, OAuth and playlist behavior are unaffected.

## Gemini direct track experiment v1

This is a separate CLI experiment. The production `POST /api/v1/recommend`,
ImageAnalysis schema, Last.fm research, OAuth and playlist API are preserved.

```
actual image → existing EXIF/metadata-safe preprocessing
→ Gemini structured artist/title candidates
→ normalize + conservative dedupe
→ exact YouTube metadata identity resolution
→ Gemini order, max 2 tracks per artist, up to 10 verified tracks
```

The frozen prompt is `direct_music_prompt_v1`; schema is
`direct_music_schema_v1`. Gemini sees the image directly, not an ImageAnalysis
or taxonomy. It generates up to 20 officially released track candidates with
fit scores and short reasons. Fit score is model relevance, not a probability;
reason is not a ranking input. YouTube chooses the matching playable video,
not the music recommendation. No Last.fm API, broad mood query, substitute
song, automatic retry or playlist write runs in this command.

Credentials are read from OS environment only:

```sh
export GOOGLE_APPLICATION_CREDENTIALS=/absolute/path/service-account.json
export GOOGLE_CLOUD_PROJECT=YOUR_PROJECT
export GOOGLE_CLOUD_LOCATION=global
export GEMINI_MODEL=gemini-3.8-flash
export VERTEX_THINKING_LEVEL=MEDIUM
export YOUTUBE_API_KEY=YOUR_KEY

go run ./cmd/direct-recommend-eval --live \
  --still-life /absolute/path/still-life.jpg \
  --night-city /absolute/path/night-city.png
```

Without `--live`, the command processes images and saves a preflight/comparison
report with no external requests. It accepts exactly these two photo inputs;
`--candidates` is 15–20, `--final` is 1–10, and `--max-search-calls` is 1–20 per
photo. Default max budget is 40 searches across two photos. It stops as soon as
10 diversity-valid final tracks exist, and stops further live work on quota
failure. Default direct-only Gemini timeout is 60s, YouTube request timeout
15s, whole-photo timeout 6min; no production timeout is changed.

Resolver v1 requires lexical title-prefix and whole-artist phrase matches plus
public, embeddable, non-live, music-category, 90–720s and KR-region availability.
Official/Topic/VEVO naming and licensed metadata improve *identity* ranking.
It rejects unrequested cover/live/remix/transformed/AI/compilation metadata.
These are conservative metadata heuristics, not audio fingerprints or proof of
channel ownership. Ambiguous spelling, collaborations, localized metadata and
non-Music-category videos may remain unresolved. No fuzzy substitute is returned.

`.cache/direct-resolver-v1.json` uses normalized artist/title + region + policy
version/limits as its key. Positive TTL is 7 days, negative TTL is 10min. Positive
hits still use `videos.list` to revalidate availability and identity. API failures
are not negative cached. Every search result's IDs are deduplicated and retrieved
in one metadata batch, not separate per-ID requests. The official SDK's normal
`.Do()` uses `gensupport.SendRequest`, not `SendRequestWithRetry` for these reads.

Each new `artifacts/gemini-direct-v1-<timestamp>/` directory contains frozen
prompt/schema hashes, whitelist-only config, image dimensions/byte counts,
raw Gemini candidate JSON, normalized candidates, resolver checks, final tracks,
diagnostics/timings, a same-photo captured Last.fm comparison and blind-review
CSV/JSON. No image data or credentials are persisted. Existing artifacts are
never overwritten. Last.fm comparison is offline mixed-vintage evidence, not a
simultaneous live test, and its playback is unverified. Share only the review
files; keep `blind-review-source-key.json` separate. Ratings remain blank.

`unresolved_rate_attempted` uses attempted candidates as its denominator.
Early-stopped/diversity-skipped candidates are `NOT_ATTEMPTED`, not unresolved.
`generated_not_verified_count` includes these and normalization drops; neither
metric is a hallucination rate. A short final list is explicitly partial.
There is no automatic promotion to the primary engine without human evidence.

```sh
go test ./...
go vet ./...
go test -race ./internal/directmusic ./internal/client ./internal/model
```

Unit tests use fake generators/HTTP transports and never call live providers.

### Direct resolver v2 milestone

`cmd/direct-recommend-eval` now defaults to a new
`artifacts/gemini-direct-resolver-v2-<timestamp>/` directory. Frozen v1 artifacts
remain intact. It replays the four recorded failures and twenty winners without
network access **before** any explicitly enabled live run. The original Gemini
model/prompt v1, fit-score meaning, production API, taxonomy, Last.fm, playlist,
artist cap and final count are unchanged. No language/country quotas or ranking
signals are introduced.

V2 removes only known comparison metadata labels (`[MV]`, `(MV)`, bounded
Official Video/Audio/Lyric/Visualizer labels). Raw titles remain saved. It keeps
Remix/Live/Cover/Acoustic/Demo/Version/Edit markers and rejects unrequested
versions; this deliberately rejects v1's `Mushaboom (Album Version)` mapping.
Title and artist evidence are checked separately. Collaborative credits may
relax only with exact title boundaries, main credited artist presence, at least
two matching credits, >=50% requested-credit coverage and Topic/VEVO/licensed
metadata. This is a release heuristic, not proof of the same performance.

Localized titles are unsupported by default. `--title-aliases file.json` accepts
an explicit evidence array with artist, canonical_title, provider_title, video_id,
channel_id, evidence_url and evidence_text. Matching requires the bound video and
channel plus stronger release metadata. No translation/fuzzy/LLM alias is inferred;
provider evidence must actually establish the relation. Alias contents hash into
cache keys, so changed evidence invalidates negative decisions.

Each candidate uses at most one primary quoted query and one artist/title-token
fallback. Fallback follows only a successful primary with no accepted identity;
API failure never triggers it. Per-photo cap remains 20 total search calls,
including fallback. Full losing-video metadata, each query kind/count/latency,
identity/officiality/playability, failure class and cache migrations are saved.
V1 negative cache entries cannot block v2; positive entries can migrate after
current v2 metadata revalidation. API failures are not negative cached.

States: RESOLVED_STRONG, RESOLVED_ACCEPTABLE, UNRESOLVED_IDENTITY,
UNRESOLVED_VERSION, UNRESOLVED_NO_CANDIDATE, API_FAILURE and NOT_ATTEMPTED.
Officiality is separate: official_artist (name consistency only), topic, vevo,
licensed, ordinary_channel or unknown. YouTube video metadata does not attest OAC
status or channel ownership. Stronger-evidence policy B (Topic/VEVO/licensed) is
an offline coverage experiment only; it is not a production hard gate.

V1 loser records lack full videos.list metadata. Snippet-only replay never
fabricates duration/licensing/playability or promotes a text match to a final
track. Read offline-v2-replay.json and accepted-v1-regression.json together.
Live results use a fresh Gemini response and a warm positive cache, so latency
and count differences are not a controlled resolver-only quality comparison.
The report includes blank blind-review.csv plus Direct-only direct-review.csv;
keep blind-review-source-key.json away from reviewers. These historical review files are not used by the current technical promotion
gates; missing human ratings do not block READY_FOR_PRIMARY.

Resolver cache policy is now `exact_metadata_v2.1`: defensive post-build guards
exclude description-only artist mentions and classify title/artist failures before
version failures. V1/v2 positive mappings still migrate after revalidation; their
negative entries never migrate. The single v2 live run was not repeated. Its full
saved metadata was rechecked under v2.1, retaining all 20 final IDs with zero
accepted/rejected changes. Seven losing-video reason labels changed. See
current-policy-recheck.json for the executable-policy/source-policy distinction.

```sh
go run ./cmd/direct-recommend-eval --recheck-recorded artifacts/RECORDED_V2_RUN
```

This offline-only mode adds a new recheck receipt without provider clients and
refuses overwriting an existing receipt. It cannot be combined with `--live`.

## Direct production technical benchmark

The current product decision excludes human review from promotion. Older v1/v2
review CSVs are historical artifacts and do not block this milestone. Technical
readiness uses cold-cache coverage, identity safety, reliability, latency and
bounded calls; it does not average Gemini fit scores or impose language quotas.

```sh
# Offline preflight and regression only: zero provider requests.
go run ./cmd/direct-production-benchmark

# Load the existing Git-ignored local configuration into OS environment.
# This file contains credentials: never commit it or display its contents.
set -a
. ./.env
set +a
# One controlled live run, empty resolver cache separately for each image.
go run ./cmd/direct-production-benchmark --live --cold-cache --max-search-calls 50
```

The saved Vertex credential path is
`/Users/surfing_seal/secrets/sync-service-account.json`. The JSON itself stays
outside the project; the command reads ADC and YouTube credentials from OS
variables. `.env` is ignored, with local permissions 0600. No dotenv dependency
or automatic credential loading is added to production code.

`benchmarks/direct/images.json` contains 22 existing real photos, verified and
deduplicated by SHA256. Paths point to this workspace's existing photo inventory;
update paths and content hashes when moving the dataset. No external downloads or
synthetic benchmark photos are generated. `--manifest`, `--max-images`, `--output`,
`--cold-cache`, `--warm-cache` and `--max-search-calls` are supported. Output paths
must be new. Warm cache is supplementary and cannot promote primary readiness.

Configuration and gates are frozen before provider client creation. At least 20
unique **executed live** images are required, alongside >=8 tracks on 95% of
images, 10 tracks on 80%, average >=9, resolver success >=80%, API failure <=2%,
pipeline p50 <=20s and p95 <=30s. Artist cap is 2; final cap is 10. Identity false
positives, broad searches, substitutes, Last.fm calls, API errors used as tracks,
and resource/diversity violations block promotion. Each candidate gets at most
one primary and one identity-only fallback search; fallback follows an identity
miss, never an API error. No automatic provider retry or subsequent benchmark run.

A 50-search whole-run budget may stop before 20 photos complete. Cloud remaining
quota is unknown; local budget is not a claim about Cloud quota. Missing execution
is recorded as unavailable data, not fake zero-latency success. Reports include
per-image candidate/metadata provenance, normalization, diagnostics, aggregate
metrics, fixed gates and READY_FOR_PRIMARY / NEEDS_MORE_TECHNICAL_VALIDATION /
BLOCKED. No OAuth or playlist writes occur.

`RECOMMENDATION_ENGINE` is a typed setting, default `legacy`. Invalid values fail
startup. `gemini_direct` is recognized but explicitly rejected at server startup
until readiness and a compatible raw-image input contract are implemented. The
current `/api/v1/recommend` JSON ImageAnalysis contract is preserved; it contains
no image bytes for Direct. There is no silent fallback or deployment switch.

Resolver `exact_metadata_v2.2` replaces arbitrary title-prefix matching with exact
comparison after known MV/music-video/lyrics labels, year and HQ normalization.
Unknown suffixes remain rejected. Recorded v1/v2 winners and known failures are
replayed offline; conservative recall losses are visible in regression artifacts,
not repaired with substitutes. Adversarial tests use artificial metadata only,
not artificial photos in the live dataset.

## Single-photo Direct local E2E (search budget 9)

This is a separate local command, not a production promotion or benchmark.
Production `cmd/server` still defaults to legacy and refuses unpromoted Direct.
The local command requires an explicit engine and binds only 127.0.0.1:8080.

```sh
set -a
. ./.env
set +a
RECOMMENDATION_ENGINE=gemini_direct APP_ENV=development \
DIRECT_E2E_FINAL_TRACK_LIMIT=5 go run ./cmd/direct-e2e-server --live
```

Without `--live` the command prints a zero-provider-call dry run. Live preflight:
one existing actual scones photo, max one Gemini call, up to 8 confident candidates,
final max 5, total search HTTP attempts max 9 including fallback/retries, private
playlist. Normal Direct defaults remain 20 candidates and max 10 final tracks.
The current resolver policy v2.2 is reused without threshold changes.

Phase A uses the same-origin HTTP API with a local-only multipart input:

```sh
curl -X POST -F 'image=@../../work/gemini-photos/scones.jpg' \
  http://localhost:8080/api/v1/recommend
```

The existing JSON response keys (`tracks`, `video_id`, `title`, `channel_title`,
`thumbnail_url`, `duration_seconds`, `match_score`, `match_reasons`, `youtube_url`,
`requested_count`, `returned_count`, `partial`, `data_mode`) remain available.
`title` remains actual YouTube source metadata. Optional `artist`, `track_title`,
`rank` (original Gemini rank) and `fit_score` add verified-candidate context.
Legacy JSON ImageAnalysis requests remain supported by the production legacy
command; local Direct requires image bytes and rejects JSON explicitly. It does
not silently call the legacy path. This is not an Android input-contract migration;
no Android sources exist in this repository.

After Phase A, `verified-e2e-tracks.json` is persisted immediately, alongside
candidate/resolver/response and search accounting artifacts. Duplicate requests
return that result without calling Gemini or searching again. A disk attempt
marker prevents re-execution after a crash. Existing positive cache is preserved
and revalidated; legacy negative cache never migrates. Actual HTTP search attempts
share one atomic 9-call budget; metadata and OAuth use separate clients/budgets.
Automatic provider retries are disabled. No broad search, substitution or Last.fm.

Phase B: open http://localhost:8080/e2e . The page reads the checkpoint only,
shows a variable-length list, and never triggers Phase A. Click YouTube account
connection, complete Google consent, then return to `/e2e` and check connection.
Click the private-test-playlist button once. No console scripts, cookie copying or
token exports are required. Only the exact saved verified IDs in the saved order
are accepted, and privacy must be private. The existing playlist service performs
sequential insertions. A write-attempt marker and saved-result reuse prevent
accidental repeated playlist creation. Partial successes are preserved without
rollback. Search and Gemini are never repeated when Phase B fails.

On success the local wrapper saves sanitized OAuth status, playlist result,
playlist items and authenticated `playlistItems.list` read-back with ordered ID
comparison. Read-back uses the existing OAuth TokenSource and official SDK;
no API key can authorize writes. Open the returned playlist URL to confirm privacy
and contents. Do not send cookies, tokens or callback authorization codes.

If the server needs restarting after a completed Phase A, use the same output:

```sh
RECOMMENDATION_ENGINE=gemini_direct APP_ENV=development \
go run ./cmd/direct-e2e-server --live --resume --output artifacts/EXISTING_E2E_RUN
```

Resume validates the checkpoint against saved resolver metadata. It never creates
Gemini/YouTube search clients. In-memory OAuth tokens require reconnecting after a
restart. If a previous write result is uncertain, inspect YouTube first; do not
remove markers or blindly retry. Private playlist mutations occur only on explicit
POST; page initialization is read-only. Tests use fake providers and zero live APIs.
The Structured Output schema remains JSON Schema as documented in the
[official Gemini documentation](https://ai.google.dev/gemini-api/docs/structured-output).

## Android Direct API contract (local / offline)

See [Android Direct API](docs/android-direct-api.md) for the new multipart
`POST /api/v1/recommend/direct`, in-memory recommendation checkpoints, and the
additive checkpoint request variant of `POST /api/v1/playlists`.
Legacy JSON `/recommend`, its track `title` meaning, the default legacy engine,
and production Direct readiness guard remain unchanged.

```sh
go run ./cmd/android-contract-server --port 8081
```

This loopback server validates uploads but replays the saved five-track E2E
fixture (`X-Sync-Data-Mode: fixture`). It makes no provider calls and simulates
playlist writes only. Do not interpret it as analysis of the uploaded photo.
Android native OAuth integration still needs a secure browser-to-app session
handoff; Custom Tab cookies are not automatically Retrofit cookies.

## Render deployment readiness

Production entrypoint: `cmd/server/main.go`. Build with
`go build -o bin/server ./cmd/server`, start with `./bin/server`, and use `/health`
for the health check. The server honors OS `PORT` on all interfaces.
See [Render setup and exact environment requirements](docs/render-deploy.md).
Legacy remains the default engine; the production Direct readiness guard stays
in place. Real ADC configuration is required before the server starts.

### Android native authentication

[Android OAuth handoff contract](docs/android-mobile-auth.md) describes the optional
S256-bound one-time browser-to-app handoff and Sync-owned Bearer sessions. Existing
Web OAuth and production recommendation guards remain unchanged. Actual Android
package/signing values are required; mobile auth is unavailable until configured.
In-memory authentication is single-process and is lost on server restart.

### Swagger / OpenAPI contract

Swagger UI는 `/swagger`, OpenAPI 3.0.3 JSON은 `/swagger/openapi.json`에서 제공합니다. [Android contract 및 배포 주의사항](docs/openapi.md)을 참고하세요. 비밀값 없는 열람용 문서이며 production readiness guard와 인증/API 동작은 변경하지 않습니다.
