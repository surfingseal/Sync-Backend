#!/usr/bin/env python3
"""Build balanced blind-review documents using saved files only. No network access."""
import argparse
import csv
import hashlib
import html
import io
import json
from pathlib import Path
import random
import re
from urllib.parse import quote


def md(value):
    # Escape formatting only; preserve original metadata in CSV exactly.
    value = html.escape(value, quote=False).replace('\n', ' ').replace('\r', ' ')
    return re.sub(r'([\\`*_{}\[\]<>|])', r'\\\1', value)


def build(source, out, seed):
    if out.exists():
        raise ValueError(f'Refusing to overwrite existing directory: {out}')
    inputs = json.loads((source / 'saved-analyses.json').read_text())
    photos = sorted(item['id'] for item in inputs)
    if len(photos) != len(set(photos)):
        raise ValueError('Duplicate photo IDs')
    shuffled = photos.copy()
    random.Random(seed).shuffle(shuffled)
    x_modes = {photo: ('gemini' if i < len(photos) // 2 else 'deterministic')
               for i, photo in enumerate(shuffled)}
    mapping = {'seed': seed, 'photos': {}}
    summary = {'photo_count': len(photos), 'playlist_count': 2 * len(photos),
               'track_count': 0, 'result_exists': {'gemini': 0, 'deterministic': 0},
               'zero_result_playlists': [], 'failed_request_playlists': [],
               'successful_empty_playlists': [], 'query_fallback_count': {'gemini': 0, 'deterministic': 0},
               'x_mode_counts': {'gemini': 0, 'deterministic': 0},
               'source_policy': 'ranker-final/query-ab NewRanking; original query-ab only for recorded failed requests',
               'seed': seed, 'actual_api_calls': 0,
               'external_api_calls': {'youtube_search_list': 0, 'youtube_videos_list': 0,
                                      'vertex_ai': 0, 'google_oauth': 0, 'playlist_write': 0},
               'within_playlist_duplicate_count': 0, 'photos': []}
    review = ['# Sync Recommendation Blind Review', '', '## 안내', '',
              '각 사진을 보며 Playlist X와 Playlist Y를 원래 순서대로 듣고 비교하세요. 경로 정보는 공개하지 않습니다.', '',
              '- Mood fit: 사진의 시각적 분위기와 음악이 잘 맞는가 (1~5)',
              '- Familiarity / Trust: 실제 음악 추천으로 신뢰가 가는가 (1~5)',
              '- Would Actually Listen: 실제로 듣고 싶은가 (1~5)', '',
              '청취 링크와 사진 링크는 review-links.md에 있습니다. 점수는 직접 입력하고, 추천이 없으면 N/A로 표시하세요.', '']
    links = ['# Blind Review Listening Links', '',
             '저장된 사진 출처와 video ID로만 구성한 링크입니다. 링크 생성 중 외부 조회는 하지 않았습니다.',
             '사진과 곡을 확인한 뒤 review.md에 평가하세요. 현재 영상의 공개/재생 상태는 재검증하지 않았습니다.', '']
    manifest_path = source.parent / 'dataset-manifest.json'
    manifest = {item['id']: item for item in json.loads(manifest_path.read_text())} if manifest_path.exists() else {}
    buf = io.StringIO(newline='')
    writer = csv.writer(buf)
    writer.writerow(['photo_id', 'playlist_label', 'rank', 'title', 'channel', 'video_id',
                     'mood_fit', 'trust', 'would_listen', 'overall_preference', 'comment'])
    for photo in photos:
        review.extend([f'## {photo}', ''])
        links.extend([f'## {photo}', ''])
        if photo in manifest:
            links.extend([f"[사진 보기]({manifest[photo]['source']})", ''])
        mapping['photos'][photo] = {}
        photo_info = {'photo_id': photo, 'gemini_result_exists': True, 'deterministic_result_exists': True,
                      'playlists': {}}
        mode_tracks = {}
        summary['x_mode_counts'][x_modes[photo]] += 1
        for label, mode in [('X', x_modes[photo]), ('Y', 'deterministic' if x_modes[photo] == 'gemini' else 'gemini')]:
            path = source / 'ranker-final' / 'query-ab' / f'{photo}-{mode}.json'
            if not path.exists():
                path = source / 'query-ab' / f'{photo}-{mode}.json'
                if not path.exists() or json.loads(path.read_text()).get('Success'):
                    raise ValueError(f'Missing final result for {photo}/{mode}')
            raw = path.read_bytes()
            result = json.loads(raw)
            if result['ImageID'] != photo or result['Mode'] != mode:
                raise ValueError(f'Source identity mismatch: {path}')
            if not result['Success'] and result.get('NewRanking'):
                raise ValueError('Failed request unexpectedly contains ranked tracks')
            tracks = [(item['Video']['VideoID'], item['Video']['Title'], item['Video']['ChannelTitle'])
                      for item in result.get('NewRanking') or []]
            ids = [t[0] for t in tracks]
            if any(not isinstance(v, str) or not v.strip() for t in tracks for v in t):
                raise ValueError(f'Missing track metadata: {path}')
            if len(ids) != len(set(ids)):
                raise ValueError(f'Duplicate video ID within playlist: {path}')
            mode_tracks[mode] = set(ids)
            entry = {'mode': mode, 'original_query_mode': result['Mode'],
                     'source_file': path.relative_to(source).as_posix(),
                     'source_sha256': hashlib.sha256(raw).hexdigest(),
                     'success': result['Success'], 'query_fallback': bool(result['Trace'].get('QueryFallback')),
                     'track_count': len(tracks)}
            mapping['photos'][photo][label] = entry
            photo_info['playlists'][label] = entry.copy()
            summary['result_exists'][mode] += 1
            summary['track_count'] += len(tracks)
            summary['query_fallback_count'][mode] += int(entry['query_fallback'])
            review.extend([f'### Playlist {label}', ''])
            links.extend([f'### Playlist {label}', ''])
            if not tracks:
                review.extend(['- No recommendations', ''])
                links.extend(['- No recommendations', ''])
                writer.writerow([photo, label, '', 'No recommendations', '', '', '', '', '', '', ''])
                zero = {'photo_id': photo, 'playlist_label': label, 'mode': mode, 'source_file': entry['source_file']}
                summary['zero_result_playlists'].append(zero)
                summary['successful_empty_playlists' if result['Success'] else 'failed_request_playlists'].append(zero)
            for rank, (video_id, title, channel) in enumerate(tracks, 1):
                review.append(f'{rank}. {md(title)} — {md(channel)}')
                links.append(f'{rank}. [{md(title)} — {md(channel)}](https://www.youtube.com/watch?v={quote(video_id, safe="")})')
                writer.writerow([photo, label, rank, title, channel, video_id, '', '', '', '', ''])
            review.extend(['', 'Mood fit: __ / 5', '', 'Familiarity / Trust: __ / 5', '',
                           'Would Actually Listen: __ / 5', ''])
            links.append('')
        photo_info['cross_playlist_shared_video_count'] = len(mode_tracks['gemini'] & mode_tracks['deterministic'])
        photo_info['has_zero_result'] = any(p['track_count'] == 0 for p in photo_info['playlists'].values())
        summary['photos'].append(photo_info)
        review.extend(['Overall Preference:', '', '- [ ] X', '- [ ] Y', '- [ ] Tie', '',
                       'Optional Comment:', '', '____________________________', ''])
    instructions = '''# 평가 안내 및 실험 한계

## 평가 방법

1. review-links.md의 사진을 보고 X/Y 곡을 원래 순서대로 들어보세요.
2. review.md에서 각 playlist에 Mood fit, Familiarity / Trust, Would Actually Listen을 1~5로 입력하세요.
3. 사진별 Overall Preference를 X/Y/Tie 중 하나로 고르고 필요하면 의견을 남기세요.
4. No recommendations도 평가 대상입니다. 청취 점수는 N/A로 표시하고 전체 선호는 직접 판단하세요.
5. 평가 완료 전 mapping.json, source-summary.json 및 기존 결과 보고서를 열지 마세요.

## CSV 입력 규칙

점수와 선호는 모두 비어 있습니다. 평가는 곡별 점수가 아니라 playlist별 점수입니다.
CSV로 평가할 경우 각 photo_id/playlist_label의 첫 행에만 세 점수를 기록하고,
사진별 overall_preference와 comment는 X의 첫 행에만 기록하세요. 나머지 행은 비워 두세요.
추천 없음은 N/A로 입력하고, 평균 점수에서는 제외하되 no-result 비율에는 반드시 포함하세요.
두 목록 모두 비었을 때에도 선호를 임의로 채우지 않습니다.

## Important Limitation

이 자료는 **기존 Gemini production path(+fallback 포함)와 deterministic path의 저장 결과에 대한 blind recommendation review**입니다.
이번 입력에서 Gemini 경로 12건 모두 query fallback이 기록되어 있습니다. 그중 10건은 추천 요청 성공,
2건(forest, mountains)은 요청 실패로 최종 ranker 파일이 없어 원본 실패 기록을 사용했습니다.
최종 V2 NewRanking의 순서와 곡을 그대로 사용했으며, 성공했지만 0곡인 결과 4개와 요청 실패 2개를 모두 유지했습니다.
요청 실패는 취향 품질과 다른 가용성 문제이므로 집계할 때 따로 보고해야 합니다.
이 결과만으로 순수 Gemini-generated query와 deterministic query의 우열을 단정할 수 없습니다.

평가자는 실제 저장된 제목/채널을 봅니다. 곡 수나 제목으로 경로를 추측할 가능성은 남아 있습니다.
사진 출처 링크와 청취 링크는 기존 metadata로 구성했으며, 현재 재생 가능 여부는 재조회하지 않았습니다.
목록 간 또는 사진 간 반복 곡은 비교 데이터이므로 삭제하지 않았습니다. 목록 내부 중복은 없습니다.

## 재현과 집계

build-blind-review.py는 고정 seed 20261003으로 X/Y를 6:6 배정합니다.
네트워크/외부 API/인증을 사용하지 않으며 기존 출력 디렉터리 덮어쓰기를 거부합니다.
기존 결과와 review 파일은 수정하지 않았고 이번 작업의 외부 API 호출은 0회입니다.
평가 완료 후 mapping을 해제하여 경로별 평균 Mood Fit, Trust, Would Listen,
X/Y/Tie 선호 승수와 경로별 승수, no-result 비율 및 요청 실패 비율을 집계하세요.
N/A와 미입력 점수는 0점으로 대체하지 마세요. 이 결과와 latency/가용성을 함께 보고 production mode를 결정하세요.
'''
    files = {'review.md': '\n'.join(review), 'review-links.md': '\n'.join(links),
             'review.csv': buf.getvalue(), 'mapping.json': json.dumps(mapping, ensure_ascii=False, indent=2) + '\n',
             'source-summary.json': json.dumps(summary, ensure_ascii=False, indent=2) + '\n',
             'instructions.md': instructions}
    # Validate blindness before writing any outputs. Actual titles remain unchanged.
    if re.search(r'gemini|deterministic|MoodScore|PopularityScore|TrustScore|FinalScore|licensedContent|ViewCount|LikeCount|ranker', files['review.md'], re.I):
        raise ValueError('Blind document contains disallowed metadata')
    out.mkdir(parents=True, exist_ok=False)
    for name, text in files.items():
        (out / name).write_text(text, encoding='utf-8')
    print(json.dumps({'photo_count': len(photos), 'playlist_count': summary['playlist_count'],
                      'track_count': summary['track_count'], 'zero_result_count': len(summary['zero_result_playlists']),
                      'failed_request_count': len(summary['failed_request_playlists']), 'actual_api_calls': 0}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--seed', type=int, default=20261003)
    args = parser.parse_args()
    build(args.source, args.out, args.seed)
