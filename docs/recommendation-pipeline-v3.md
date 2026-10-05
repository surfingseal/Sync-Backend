# Recommendation pipeline v3

사진 → Gemini 이미지 분석 → Enhanced MusicProfile → deterministic query → adaptive YouTube retrieval → 기존 V2 ranking → vocal mix → recommendations.

Gemini 모델(`gemini-3.8-flash`), 이미지 전처리, OAuth/TokenStore, playlist orchestration은 유지한다. Gemini text query generator는 `RECOMMENDATION_QUERY_MODE=gemini`로 남지만 기본값은 `deterministic`이다. fixture/replay에서는 설정과 관계없이 deterministic을 사용한다. deterministic 경로는 추가 Vertex 호출이 없다.

## ImageAnalysis

새 provider schema는 `schema_version: "3"`이다. 기존 v1/v2 입력과 benchmark 원본은 유지한다. 과거 `vocal_preference` / `instrumental_preference`는 읽을 수 있지만 query adapter에서 사용하지 않는다. 새 Gemini 응답에서는 두 필드를 허용하지 않는다.

분석 순서는 visual → mood → music attributes → genre이다. 사람의 감정·정신 상태·의도·개인 취향을 추론하지 않는다. primary 1개, secondary 0–3개, tags는 이를 순서대로 복사한다. calm/peaceful/serene 같은 중복 의미를 불필요하게 늘리지 않는다.

controlled vocabulary는 mood 24개, family 12개, genre 61개(other/unknown 포함)이다. `internal/music/taxonomy.json`이 schema와 query mapping의 공통 원본이다. Korean-oriented (`korean`)는 검색을 위한 묶음이며 음악학적 독립 장르라고 주장하지 않는다.

`music_profile.genres`의 기존 문자열 배열은 유지하고, `genre_candidates`에 `{category,name,score}` Top3를 담는다. 예시는 `internal/model/testdata/analysis-v3.json`이다. score는 사진과 음악 방향의 model-reported relevance이며 확률이 아니다. optional `confidence.{mood,genre,tempo}`도 확률이 아니다. 숫자 범위, canonical enum, category/name 관계, 중복 및 mirrors를 검증한다.

## Retrieval policy and deterministic query

초기 정책은 `internal/music/retrieval_policy.go`에서 관리한다. 우선순위는 model score × retrieval weight이다.

| Genre | Weight |
|---|---:|
| ambient / dream-pop / 기타 기본 | 1.00 |
| neo-classical | .95 |
| soundtrack | .70 |
| cinematic | .80 |
| drone | .60 |

높은 soundtrack 분석 점수를 검색 우선순위와 동일시하지 않는다. 사용자 preferred_genres가 있으면 검색 방향에서 우선한다. mood와 genre의 단어 mapping은 controlled registry만 사용한다. other/unknown의 raw_label은 검색에 사용하지 않는다.

query 1은 adjusted genre + primary mood + optional language, query 2는 다른 genre/mood/language 조합이다. mixed/vocal-first에서 분석에 vocal-friendly genre가 존재하면 최소 한 query에 사용한다. 낮은 genre confidence(<.5, 중앙 상수)나 약한 relevance, unknown은 mood + tempo + 사용자 vocal hint로 fallback한다. 3–7 token을 목표로, 최대 7 token / 120 characters, 소문자·공백·hyphen·중복 단어를 정규화한다. 기본 최대 2개, 설정 최대 3개이며 pagination은 없다.

예: soundtrack .85 / ambient .80 / neo-classical .70, serene + reflective, ko/en, mixed:

- `korean ambient serene music`
- `english neo classical reflective music`

Gemini query 오류/빈 응답도 deterministic으로 fallback한다. 실제 곡/아티스트를 AI가 만들어 반환하지 않는다.

## Vocal policy

`preferences.vocal_mode`: `mixed`(기본), `vocal-first`, `instrumental-first`, `instrumental-only`.

기존 사용자 `instrumental_only=true`는 instrumental-only로 해석하며 새 mode보다 우선한다. 이미지의 과거 vocal 필드는 정책에 영향을 주지 않는다. mixed는 instrumental 검색어를 강제하지 않는다.

V2로 확대 후보 풀을 정렬한 뒤 metadata 단서로 soft mix를 선택한다. 목표는 대략 vocal-friendly 70% / instrumental 30%이다. 0.08 이내 score 차이에서만 soft promotion하므로 수량이나 비율을 보장하지 않는다. instrumental-only만 명시적인 instrumental 후보로 제한한다. 제목의 lyrics/vocal, instrumental/karaoke/piano solo 등의 **제한적인 휴리스틱**이며 실제 오디오 판별이 아니다. 증거가 없으면 unknown으로 유지한다. unknown을 보컬로 단정하거나 mixed에서 제거하지 않는다. instrumental-only의 recall은 제한적일 수 있다.

V2 Mood/Popularity/Trust/Engagement/Diversity 가중치는 .45/.25/.15/.05/.10으로 유지한다. 후보 풀 확장으로 discovery 상한이 풀리지 않도록 원래 requested_count 기준 상한도 유지한다. 최종 mix 순서는 score의 엄격한 내림차순과 다를 수 있다.

## Adaptive retrieval and cache

1. query 1의 ID cache 확인, miss일 때만 search.list 실행.
2. 새로운 ID를 deduplicate하고 videos.list로 <=50개씩 batch 조회.
3. 기존 음악 category, 공개/embeddable, 길이 90–720초, 지역 제한, AI transform, 제외 아티스트 필터 적용.
4. V2 minimum score/quality gate까지 통과한 후보가 requested_count + safety margin(기본 3) 이상이면 다음 query 생략.
5. mixed/vocal-first에서 명시적으로 instrumental이 과도한 pool은 fallback query를 더 확인한다. 부족하면 다음 query, 최대 설정 개수에서 중단한다.

같은 요청의 query와 ID를 deduplicate한다. 이미 metadata를 확인한 ID는 다시 조회하지 않는다. adaptive는 query 1 결과를 판단해야 하므로 videos.list가 search 단계별로 1회씩 필요할 수 있다. 최대 20 ID/query이므로 보통 search당 metadata batch 1회이며 ID별 개별 호출은 없다. cache hit도 metadata를 새로 조회한다.

`SearchCache` / `InMemorySearchCache`는 candidate ID만 저장한다. 최종 personalized response는 저장하지 않는다. key는 normalized query, region, relevanceLanguage, order, maxResults와 고정 part/type/category/embeddable/syndicated/safeSearch를 모두 포함하며 API key는 제외한다. 검색 NOT 연산자(`-mix`)는 key에서 보존한다. TTL 기본 300초, 허용 1–3600초, 최대 1000 entries. 재시작하면 사라진다.

[YouTube Developer Policies III.E.4](https://developers.google.com/youtube/terms/developer-policies#e.-handling-youtube-data-and-content)의 Non-Authorized Data 저장/refresh 제한을 확인했다. 300초는 앱이 선택한 보수적인 값이며 Google 권장 TTL이라고 주장하지 않는다. persistent cache/미디어 다운로드는 구현하지 않았다. replay는 원본 recorded_at을 보존하고 30일을 초과한 API 데이터를 거부한다. 기존 benchmark 파일은 삭제하거나 덮어쓰지 않았다. 만료된 API snapshot은 서비스용 저장에서 삭제/refresh하는 운영 절차가 필요하며, replay는 만료된 데이터를 fresh 결과처럼 서비스하지 않는다.

## Data modes and budget

| Mode | Retrieval | External recommendation calls |
|---|---|---:|
| fixture | sunset/cafe/city-night/ocean synthetic pools | 0 |
| replay | recorded pools의 로컬 keyword mapping | 0 |
| live | official YouTube API + ID cache | 필요한 경우만 |

개발 기본값은 fixture / live_search=false. production 기본값은 live / live_search=true이며 `.env.example`의 명시적인 개발 설정을 production에 복사하면 그 값이 우선한다. fixture/replay는 **recommendation만** offline으로 바꾼다. `/analyze`와 OAuth/playlist는 별도 실제 기능이므로 호출하면 각각의 외부 API를 사용할 수 있다.

response에 `data_mode`를 추가한다. fixture title은 SYNTHETIC, video_id는 `fixture_...`, youtube_url은 빈 문자열이다. fixture ID는 playlist 입력 검증에서도 거부하여 실제 write 전에 차단한다. replay는 이전 시점의 실제 metadata이며 현재 존재/접근 가능 여부를 보장하지 않는다. production 추천에는 live를 사용한다.

`YOUTUBE_SEARCH_MAX_CALLS_PER_RUN` 기본 10은 서버 process 전체의 atomic budget이다. cache hit/fixture/replay는 차감하지 않고 actual source search 시도만 차감한다. 소진되면 HTTP 503 `YOUTUBE_SEARCH_BUDGET_EXCEEDED`. live opt-in이 꺼져 있으면 503 `YOUTUBE_LIVE_SEARCH_DISABLED`. quota 오류는 503 `YOUTUBE_QUOTA_EXCEEDED`이며 실패 결과를 cache하지 않는다. retry, key rotation, 다른 프로젝트 우회가 없다.

live integration은 `YOUTUBE_INTEGRATION_TEST=1`일 때만 실행하며 계획된 search <=2를 출력하고 budget 2로 제한한다. 기존 live recommendation benchmark도 `YOUTUBE_LIVE_SEARCH_ENABLED=true`를 요구하고 process budget으로 제한한다. 이번 작업에서는 실행하지 않았다.

측정 항목: queries_generated/deduplicated, search_cache_hits/misses, search_list_calls, second_query_skipped/executed, raw/eligible/ranked_candidates, quota_errors, fixture/replay_requests. stage latency와 trace는 request 단위다. token/key/credential/이미지/사용자 문자열은 로그에 남기지 않는다.

## Configuration

```dotenv
RECOMMENDATION_QUERY_MODE=deterministic
RECOMMENDATION_DATA_MODE=fixture
RECOMMENDATION_REPLAY_PATH=
YOUTUBE_LIVE_SEARCH_ENABLED=false
YOUTUBE_SEARCH_MAX_CALLS_PER_RUN=10
YOUTUBE_SEARCH_CACHE_TTL_SECONDS=300
RECOMMENDATION_ADAPTIVE_SAFETY_MARGIN=3
YOUTUBE_SEARCH_QUERY_COUNT=2
```

Replay 파일 contract는 `{recorded_at: RFC3339, pools:[{name, keywords, videos}]}`이며 videos는 저장된 YouTubeVideo source metadata이다. pool 선택은 local keyword-overlap simulation이다. 실제 새 query에 대한 YouTube 검색 품질을 재현한다고 주장하지 않는다.

## Offline validation

모듈 디렉터리에서:

```sh
go test ./...
go vet ./...
go run ./cmd/retrieval-audit \
  -dataset ../benchmark-results/recommendation-v2/saved-analyses.json \
  -replay ../benchmark-results/retrieval-refactor/replay-pools.json \
  -out ../benchmark-results/retrieval-refactor/offline-audit-new.json
```

새 경로를 사용하여 과거 benchmark 파일을 덮어쓰지 않는다. 12개 저장 분석/22개 저장 pool은 query, filtering, V2, vocal mix, adaptive simulation에만 사용했다. 수량 부족을 숨기거나 낮은 품질 후보로 채우지 않는다. 이 dataset으로는 fresh retrieval 품질 향상을 입증할 수 없다. 충분한 pool의 search 생략은 fake unit test로 검증한다.

## Future image-quality benchmark (not run)

20–30개 사진 × 2–3회 평가를 준비했다. `examples/profile-quality-v3/photo-manifest.template.json`의 24개 path를 직접 작성한다. settings는 model 3.8 Flash / MEDIUM / 현재 전처리 1920 / 3 repeats / 1 attempt이다. 2 repeats로 변경 가능하다. 새 output directory를 사용한다.

다음은 **유료 실제 Vertex 호출**이며 이번 작업에서 실행하지 않은 명령이다:

```sh
go run ./cmd/benchmark --live \
  -config examples/profile-quality-v3/settings.json \
  -dataset /absolute/path/filled-photo-manifest.json \
  -out /absolute/path/new-profile-v3-results
```

저장된 결과만 평가:

```sh
python3 scripts/pack-profile-repeats.py \
  /absolute/path/new-profile-v3-results/profile-v3.jsonl \
  /absolute/path/new-repeats.json
go run ./cmd/profile-benchmark -repeats /absolute/path/new-repeats.json \
  -out /absolute/path/new-consistency-report
```

primary mood agreement, secondary Jaccard, genre Top3 Jaccard, highest-analysis-score family agreement, tempo agreement, energy/valence pairwise MAE, genre score variance/stddev를 계산한다. confidence가 없으면 이를 만들어 넣지 않는다. 반복 일치도는 정확도와 다르다. 실제 사진 적합도는 `human-review.template.md`의 빈 칸을 사람이 평가한다.
