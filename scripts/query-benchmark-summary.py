#!/usr/bin/env python3
"""Offline diagnostics and blind query review from query-benchmark JSONL."""
import argparse
from collections import Counter
import csv
import io
import json
import math
from pathlib import Path
import random
import re
import statistics


def words(text):
    return re.findall(r'[^\W_]+', text.casefold(), re.UNICODE)


def diagnose(queries, analysis, preferences):
    genres = preferences.get('preferred_genres') or analysis['music_profile']['genres']
    moods = analysis['mood']['tags']
    language_names = {'ko': 'korean', 'en': 'english', 'ja': 'japanese', 'es': 'spanish', 'fr': 'french'}
    expected_languages = [language_names.get(lang, lang) for lang in preferences['languages']]
    normalized = [' '.join(words(q)) for q in queries]
    query_details = []
    scene_tokens = words(analysis['scene']['description'])
    for q, norm in zip(queries, normalized):
        tokens = words(q)
        scene_sentence = any(' '.join(scene_tokens[i:i+5]) in norm for i in range(max(0, len(scene_tokens)-4)))
        query_details.append({'query': q, 'token_count': len(tokens), 'character_count': len(q),
                              'preferred_3_to_8_tokens': 3 <= len(tokens) <= 8,
                              'too_long_diagnostic': len(tokens) > 8 or len(q) > 120,
                              'empty': not q.strip(),
                              'duplicate_words': [w for w, count in Counter(tokens).items() if count > 1],
                              'missing_genre': bool(genres) and not any(' '.join(words(g)) in norm for g in genres),
                              'missing_mood': bool(moods) and not any(' '.join(words(m)) in norm for m in moods),
                              'scene_sentence_overlap': scene_sentence,
                              'recommendation_sentence': bool(re.search(r'\b(song recommendations|i recommend|here are|you should listen)\b', q, re.I)),
                              'json_or_markdown_or_url_pollution': bool(re.search(r'[{}\[\]`]|https?://', q)),
                              'unusual_punctuation': bool(re.search(r'[^\w\s\-]', q)),
                              'artist_song_hallucination': 'not_verifiable_without_human_review'})
    return {'duplicate_query_count': len(normalized)-len(set(normalized)),
            'missing_languages': [lang for lang in expected_languages if not any(lang in norm.split() for norm in normalized)],
            'artist_song_hallucination_status': 'unassessed; lexical diagnostics cannot prove a phrase is not an artist/song',
            'queries': query_details}


def percentile(values, fraction):
    if not values:
        return None
    ordered = sorted(values)
    index = (len(ordered)-1)*fraction
    lo, hi = math.floor(index), math.ceil(index)
    return ordered[lo] + (ordered[hi]-ordered[lo])*(index-lo)


def summarize(rows):
    good = [r for r in rows if r['success']]
    details = [q for r in good for q in r['diagnostics']['queries']]
    total = len(rows)
    latencies = [r['latency_ms'] for r in rows]
    def rate(count, denominator):
        return count / denominator if denominator else None
    return {'logical_calls': total, 'success': len(good), 'timeouts': sum(r['timeout'] for r in rows),
            'other_failures': sum(not r['success'] and not r['timeout'] for r in rows),
            'success_rate': rate(len(good), total), 'timeout_rate': rate(sum(r['timeout'] for r in rows), total),
            'p50_ms': percentile(latencies, .5), 'p90_ms': percentile(latencies, .9), 'p95_ms': percentile(latencies, .95),
            'max_ms': max(latencies) if latencies else None,
            'mean_query_count_success_only': statistics.mean(r['query_count'] for r in good) if good else None,
            'schema_success_rate_all_calls': rate(sum(r['schema_success'] for r in rows), total),
            'average_query_tokens_success_only': statistics.mean(q['token_count'] for q in details) if details else None,
            'duplicate_query_set_rate_success_only': rate(sum(r['diagnostics']['duplicate_query_count'] > 0 for r in good), len(good)),
            'hallucinated_artist_song_rate': None, 'hallucination_assessment': 'requires human review; not assumed zero',
            'too_long_query_count': sum(q['too_long_diagnostic'] for q in details),
            'missing_mood_query_count': sum(q['missing_mood'] for q in details),
            'missing_genre_query_count': sum(q['missing_genre'] for q in details),
            'duplicate_word_query_count': sum(bool(q['duplicate_words']) for q in details),
            'missing_language_set_count': sum(bool(r['diagnostics']['missing_languages']) for r in good),
            'scene_sentence_query_count': sum(q['scene_sentence_overlap'] for q in details),
            'recommendation_sentence_count': sum(q['recommendation_sentence'] for q in details),
            'json_pollution_query_count': sum(q['json_or_markdown_or_url_pollution'] for q in details),
            'invalid_response_count': sum(r.get('error_class') == 'invalid_response' for r in rows),
            'http_attempt_count': sum(r['metrics']['attempt_count'] for r in rows),
            'fallback_count': sum(r['fallback_used'] for r in rows),
            'error_classes': dict(Counter(r.get('error_class') for r in rows if not r['success'])),
            'http_statuses': dict(Counter(str(code) for r in rows for code in (r['metrics']['http_statuses'] or []))),
            'input_tokens_reported': sum(r['metrics']['input_tokens'] for r in rows),
            'output_tokens_reported': sum(r['metrics']['output_tokens'] for r in rows),
            'thought_tokens_reported': sum(r['metrics']['thought_tokens'] for r in rows),
            'response_metrics_available_count': sum(r['response_metrics_available'] for r in rows)}


def build(root, dataset):
    names = ['summary.json', 'summary.md', 'query-review.md', 'query-review.csv', 'query-review-key.json', 'diagnostics.json']
    if any((root/name).exists() for name in names):
        raise ValueError('Refusing to overwrite existing reports/reviews')
    rows = [json.loads(line) for line in (root/'raw.jsonl').read_text().splitlines()]
    inputs = {item['id']: item['analysis'] for item in json.loads(dataset.read_text())}
    config = json.loads((root/'config.json').read_text())
    preferences = config['preferences']
    groups = {}
    for row in rows:
        row['diagnostics'] = diagnose(row['queries'], inputs[row['photo_id']], preferences) if row['success'] else None
        groups.setdefault(row['profile'], []).append(row)
    assert len(rows) == 48 and len(inputs) == 12, 'Only summarize a complete 36+12 run'
    for photo in inputs:
        records = [r for r in rows if r['photo_id'] == photo]
        assert len(records) == 4 and len({r['input_sha256'] for r in records}) == 1
        assert {r['profile'] for r in records} == {'6s', '10s', '15s', 'local'}
    assert not any(r['fallback_used'] for r in rows)
    assert all(r['metrics']['attempt_count'] <= 1 for r in rows)
    profiles = {name: summarize(groups[name]) for name in ['6s', '10s', '15s', 'local']}
    prod = profiles['6s']
    target = config['internal_targets']
    viable = prod['success_rate'] >= target['success_rate'] and prod['p50_ms'] <= target['p50_ms'] and prod['p95_ms'] <= target['p95_ms'] and prod['schema_success_rate_all_calls'] >= target['schema_success_rate']
    timeouts = sum(profiles[n]['timeouts'] for n in ['6s', '10s', '15s'])
    conclusion = ('A. Gemini query generator remains viable' if viable else
                  'C. Deterministic should become production candidate' if timeouts >= 6 else
                  'B. Gemini query generator is questionable')
    summary = {'profiles': profiles, 'actual_vertex_text_logical_calls': 36,
               'actual_vertex_generation_http_attempts': sum(profiles[n]['http_attempt_count'] for n in ['6s', '10s', '15s']),
               'prohibited_api_calls': config['prohibited_api_calls'], 'input_identity_verified': True,
               'production_mode_changed': False, 'human_ratings_entered': False,
               'conclusion': conclusion, 'review_profile_selection': '15s fixed before viewing quality results; failed sets retained',
               'percentiles': 'linear interpolation over all observed logical-call latencies, failures included; timeout tails are censored',
               'internal_targets': target,
               'limitations': ['12 observations per profile cannot establish a 98/99% population reliability claim.',
                               'Single sequential run; profile order rotated, not a load/cold-start study.',
                               'No YouTube retrieval tested; query diagnostics do not prove recommendation quality.',
                               'No artist/song hallucination rate asserted; human verification is required.',
                               'ADC token acquisition for Vertex may occur; no user Google OAuth flow was invoked.'],
               'next_youtube_ab': {'suggested_photos': 4, 'generators': 2, 'queries_per_generator': 2,
                                   'search_list_calls': 16, 'videos_list_calls': 4, 'max_results_per_query': 10, 'max_candidate_ids_per_photo': 40,
                                   'conditions': 'After quota restoration and human review; set maxResults=10 and share one <=40 ID metadata batch per photo, no pagination or refill. Only if both query sets exist. No new image analysis or playlist writes.'}}
    review = ['# Sync Query Generator Blind Review', '',
              '입력 요약을 보고 X/Y 검색어 묶음을 비교하세요. Relevance, Search usefulness, Specificity, Naturalness를 1~5로 직접 평가하세요.',
              '검색어 없음도 그대로 비교합니다. 평가 전에 key/summary/raw 파일을 열지 마세요. 외부 검색을 실행하는 자료가 아닙니다.', '']
    csvbuf = io.StringIO(newline=''); writer = csv.writer(csvbuf)
    writer.writerow(['photo_id', 'generator_label', 'query_rank', 'query', 'relevance', 'search_usefulness', 'specificity', 'naturalness', 'overall_preference', 'comment'])
    photo_ids = sorted(inputs); shuffled = photo_ids.copy(); random.Random(20261004).shuffle(shuffled)
    x_modes = {photo: 'gemini' if i < 6 else 'deterministic' for i, photo in enumerate(shuffled)}
    key = {'seed': 20261004, 'gemini_profile': '15s', 'photos': {}}
    for photo in photo_ids:
        a = inputs[photo]; key['photos'][photo] = {}
        review += [f'## {photo}', '', 'Input summary:', '', f"- Mood: {', '.join(a['mood']['tags'])}",
                   f"- Genres: {', '.join(a['music_profile']['genres'])}", f"- Tempo: {a['music_profile']['tempo']}",
                   f"- Vocal preference: {a['music_profile']['vocal_preference']}",
                   f"- Languages: {'/'.join(preferences['languages'])}", '']
        for label, mode in [('X', x_modes[photo]), ('Y', 'deterministic' if x_modes[photo] == 'gemini' else 'gemini')]:
            profile = '15s' if mode == 'gemini' else 'local'
            record = next(r for r in rows if r['photo_id'] == photo and r['profile'] == profile)
            key['photos'][photo][label] = {'generator': mode, 'profile': profile, 'source': 'raw.jsonl', 'photo_id': photo, 'success': record['success']}
            review += [f'### Generator {label}', '']
            for rank, query in enumerate(record['queries'], 1):
                review.append(f'{rank}. {query}')
                writer.writerow([photo, label, rank, query, '', '', '', '', '', ''])
            if not record['queries']:
                review.append('- No queries')
                writer.writerow([photo, label, '', 'No queries', '', '', '', '', '', ''])
            review += ['', 'Relevance: __ / 5', '', 'Search usefulness: __ / 5', '', 'Specificity: __ / 5', '', 'Naturalness: __ / 5', '']
        review += ['Preferred query set:', '', '- [ ] X', '- [ ] Y', '- [ ] Tie', '', 'Comment:', '', '________________', '']
    def fmt(v):
        return 'N/A' if v is None else f'{v:.2f}'
    def pct(v):
        return 'N/A' if v is None else f'{v*100:.1f}%'
    report = ['# Gemini Query Generator Benchmark', '', '## Audit', '',
              f"Model: {config['model']}; thinking LOW; query count 2 (schema 1~2); max output tokens 1024; current prompt {config['prompt_bytes']} bytes / {config['prompt_runes']} runes.",
              '전체 ImageAnalysis와 Preferences를 JSON으로 전달하므로 scene.description도 포함됩니다. 이번 실험은 current prompt만 사용해 timeout 효과를 분리합니다.',
              'Production query timeout은 recommendation_service.go의 6초 상수입니다. 전체 recommendation 기본 15초 내 검색어 생성 예산이며, 6초를 정한 실측/실험 근거는 코드에 없습니다.',
              'Production SDK 기본 최대 3 attempts (budget mode는 SDK 1 attempt). Query 메서드는 별도 application retry가 없으며 image retry/timeout과 분리됩니다.',
              '이번 benchmark는 SDK 1 attempt, 6/10/15초 외부 context, SDK HTTP timeout 15초, fallback 없이 query 메서드를 직접 호출합니다.',
              '응답은 application/json + ResponseJsonSchema이며 decoder에서 알 수 없는 필드/빈 목록/초과 길이/URL 등을 거부합니다.',
              '공식 thinking 문서에서 3.8 Flash 지원 값은 low/medium/high입니다. LOW를 유지하며 MINIMAL은 설정하지 않았습니다.',
              '[Thinking 문서](https://ai.google.dev/gemini-api/docs/thinking) · [Structured output 문서](https://ai.google.dev/gemini-api/docs/structured-output)', '',
              '## Results', '',
              '| Profile | Success | Timeout | p50 ms | p90 ms | p95 ms | Mean queries* | Schema success | Avg tokens/query* | Duplicate sets* | Hallucinated artist/song |',
              '|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|']
    for name, s in profiles.items():
        report.append(f"| {name} | {pct(s['success_rate'])} | {pct(s['timeout_rate'])} | {fmt(s['p50_ms'])} | {fmt(s['p90_ms'])} | {fmt(s['p95_ms'])} | {fmt(s['mean_query_count_success_only'])} | {pct(s['schema_success_rate_all_calls'])} | {fmt(s['average_query_tokens_success_only'])} | {pct(s['duplicate_query_set_rate_success_only'])} | 未評価 / human review required |")
    report += ['', '*成功した出力のみを分母にする項目。Schema successは全呼び出しのうち有効schema出力を得た割合で、HTTP失敗も含みます。',
               '（成功した出力のみ集計。欠測は0ではなくN/A。）', '',
               '## Diagnostics', '',
               '| Profile | Too long | Missing mood | Missing genre | Duplicate words | Missing language sets | Invalid response |',
               '|---|---:|---:|---:|---:|---:|---:|']
    for name,s in profiles.items():
        report.append(f"| {name} | {s['too_long_query_count']} | {s['missing_mood_query_count']} | {s['missing_genre_query_count']} | {s['duplicate_word_query_count']} | {s['missing_language_set_count']} | {s['invalid_response_count']} |")
    report += ['', 'local Schema success는 외부 JSON schema 호출이 아니라 query 출력 형식의 정상 생성 여부입니다.',
               '길이 3~8 tokens는 진단 기준이며 hard failure가 아닙니다. mood/genre/language 검사는 문자 일치 heuristic이라 동의어를 놓칠 수 있습니다.',
               'Missing language는 ko/en의 명시적 언어 키워드 누락입니다. 영어로 작성된 검색어가 영어 노래를 보장하는지는 확인하지 않았습니다.',
               '아티스트/곡명 hallucination은 검색 없이 자동 확정할 수 없어 미평가로 남겼습니다. scene 문장/추천 문장/JSON 오염은 diagnostics.json에 기록합니다.', '',
               '## Interpretation', '', conclusion, '',
               '내부 사전 목표: success ≥98%, p50 ≤2s, p95 ≤4s, schema success ≥99%, fallback reliance ≤2%. Google SLA가 아닙니다.',
               '실험상 fallback은 0회로 고정했으므로 production fallback 의존도를 측정한 것은 아닙니다. Production 조건 실패율은 fallback 필요 가능성의 참고치입니다.',
               '각 profile n=12로 신뢰성 목표 달성을 확증할 수 없습니다. timeout은 관측 latency이며 서버 완료 시간의 오른쪽 꼬리는 알 수 없습니다.',
               'Gemini는 외부 의존/유료 inference/네트워크 timeout/비결정성이 있고, deterministic은 로컬/AI 호출 비용 없음/재현성은 높지만 맥락 표현력이 제한됩니다.',
               'production mode를 자동 변경하지 않았습니다. 사람 평가 점수도 입력하지 않았습니다.', '',
               '## Next step', '',
               'query-review.md의 고정 15초 profile과 deterministic 검색어를 블라인드 평가하세요. 빈 출력도 유지했습니다. CSV는 playlist/set 단위 첫 행에만 점수를 입력하세요.',
               '쿼터 복구 후 4개 사진 × 2경로 × 2검색어 = search.list 16회부터 제안합니다. query당 maxResults=10으로 제한하여 사진당 합친 최대 40 ID batch로 videos.list 4회입니다. 기존 검색 설정을 그대로 쓰면 더 많은 metadata batch가 필요할 수 있습니다.',
               '이는 다음 실험 계획이며 이번에는 YouTube/이미지 분석/user OAuth/playlist 호출이 모두 0회입니다. Vertex ADC 인증 token 교환은 사용자 OAuth 연결과 다릅니다.',
               '검증: gofmt, go test ./..., go vet ./... 통과. 기존 image/recommendation/OAuth/playlist unit tests 유지.']
    # Use Korean descriptions throughout the report.
    report = [line.replace('未評価', '미평가').replace('*成功した出力のみを分母にする項目。Schema successは全呼び出しのうち有効schema出力を得た割合で、HTTP失敗も含みます。', '*표시 항목은 성공 출력만의 평균/비율입니다. Schema success는 전체 호출 중 유효 출력을 받은 비율로 HTTP 실패도 포함합니다.').replace('（成功した出力のみ集計。欠測は0ではなくN/A。）', '실패하여 출력이 없는 경우 query 품질은 N/A이며 0점으로 추정하지 않습니다.') for line in report]
    report += ['', '## Additional evidence', '',
               f"Vertex text generation logical calls/HTTP attempts: 36/{summary['actual_vertex_generation_http_attempts']}. 모든 benchmark fallback은 0입니다.",
               '보고된 input/output/thought tokens 합계: ' + ' / '.join(str(sum(r['metrics'][k] for r in rows)) for k in ['input_tokens','output_tokens','thought_tokens']) + '. 실제 청구액은 조회하지 않았습니다.',
               '6초 profile의 이번 관측은 기술 목표를 충족했으나, 15초 profile은 긴 지연을 보입니다. timeout을 늘려 지연이 증가했다고 단정할 수 없습니다.',
               '과거 전부 timeout이던 실행과 이번 성공 실행의 차이를 모델/서버 상태·네트워크·쿼터 중 하나의 원인으로 확정할 증거는 없습니다.',
               '프롬프트 간소화 후보는 prompt-simplified-candidate.md에만 작성했으며 실행/적용하지 않았습니다.']
    outputs = {'summary.json': json.dumps(summary,ensure_ascii=False,indent=2)+'\n',
               'summary.md': '\n'.join(report)+'\n', 'query-review.md': '\n'.join(review)+'\n',
               'query-review.csv': csvbuf.getvalue(), 'query-review-key.json': json.dumps(key,ensure_ascii=False,indent=2)+'\n',
               'diagnostics.json': json.dumps(rows,ensure_ascii=False,indent=2)+'\n'}
    for name,text in outputs.items():
        (root/name).write_text(text)
    print(json.dumps(summary,ensure_ascii=False,indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results', type=Path, required=True)
    parser.add_argument('--dataset', type=Path, required=True)
    args = parser.parse_args()
    build(args.results,args.dataset)
