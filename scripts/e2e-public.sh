#!/bin/sh
# Does not start/stop the server or access browser cookies/tokens.
set -eu
if [ "$#" -ne 1 ]; then
  echo 'Usage: scripts/e2e-public.sh /absolute/path/to/image.jpg' >&2
  exit 2
fi
exec python3 - "$1" <<'PY'
import json, math, os, pathlib, subprocess, sys, time, urllib.parse, urllib.request
base = os.environ.get('SYNC_E2E_BASE_URL', 'http://localhost:8080').rstrip('/')
out = pathlib.Path(os.environ.get('SYNC_E2E_OUTPUT_DIR', '/tmp/sync-e2e'))
image = pathlib.Path(sys.argv[1]).resolve()
stage = 'Environment'
timings = {}
def require(condition, message):
    if not condition: raise ValueError(message)
def strict_json(raw):
    return json.loads(raw, parse_constant=lambda value: (_ for _ in ()).throw(ValueError('non-finite JSON number')))
def call(path, args=()):
    deadline = max(35, min(120, int(os.environ.get('VERTEX_TIMEOUT_SECONDS', '20')) + 5))
    started = time.monotonic()
    result = subprocess.run(['curl', '-sS', '--max-time', str(deadline), *args, base + path, '-w', '\n%{http_code}'], capture_output=True, text=True, timeout=deadline+5)
    timings[path] = (time.monotonic() - started) * 1000
    (out / 'timings.json').write_text(json.dumps(timings, indent=2))
    require(result.returncode == 0, 'HTTP transport failed')
    raw, status = result.stdout.rsplit('\n', 1)
    require(status == '200', 'unexpected HTTP status ' + status)
    return strict_json(raw)
try:
    defaults = {'GOOGLE_CLOUD_LOCATION': 'global', 'VERTEX_MODEL': 'gemini-3.8-flash', 'YOUTUBE_REGION': 'KR', 'YOUTUBE_RELEVANCE_LANGUAGE': 'ko', 'GOOGLE_OAUTH_REDIRECT_URL': 'http://localhost:8080/api/v1/auth/google/callback'}
    for name, value in defaults.items():
        if not os.environ.get(name, '').strip(): os.environ[name] = value
    names = ['GOOGLE_CLOUD_PROJECT', 'YOUTUBE_API_KEY', 'GOOGLE_OAUTH_CLIENT_ID', 'GOOGLE_OAUTH_CLIENT_SECRET']
    missing = [name for name in names if not os.environ.get(name, '').strip()]
    require(not missing, 'missing environment variables: ' + ', '.join(missing))
    if os.environ.get('GOOGLE_APPLICATION_CREDENTIALS'):
        require(pathlib.Path(os.environ['GOOGLE_APPLICATION_CREDENTIALS']).is_file(), 'credential file is missing')
    # If unset, the running server may use default ADC; actual analyze checks it.
    require(os.environ['GOOGLE_OAUTH_REDIRECT_URL'] == base + '/api/v1/auth/google/callback', 'redirect URI must match the backend origin and registered callback')
    require(image.is_file(), 'test image is missing')
    require(image.stat().st_size <= 10 * 1024 * 1024, 'test image exceeds upload limit')
    out.mkdir(parents=True, exist_ok=True)
    print('Environment: PASS (required settings present; values hidden)', flush=True)
    stage = 'Health'
    require(call('/health') == {'status': 'ok'}, 'invalid health response')
    print('Health: PASS (HTTP 200)', flush=True)
    stage = 'Analyze'
    analyze = call('/api/v1/analyze', ['-X', 'POST', '-F', 'image=@' + str(image)])
    (out / 'analyze.json').write_text(json.dumps(analyze, ensure_ascii=False, indent=2))
    analysis = analyze['analysis']
    for name in ['scene', 'visual', 'mood', 'music_profile']:
        require(isinstance(analysis[name], dict), 'analysis object missing: ' + name)
    for value in [analysis['visual']['brightness'], analysis['mood']['energy'], analysis['mood']['valence'], analysis['music_profile']['energy']]:
        require(type(value) in (int, float) and math.isfinite(value) and 0 <= value <= 1, 'analysis numeric range invalid')
    for values, maximum in [(analysis['mood']['tags'], 6), (analysis['music_profile']['genres'], 5), (analysis['visual']['dominant_colors'], 5)]:
        require(isinstance(values, list) and 1 <= len(values) <= maximum and all(isinstance(value, str) and value.strip() for value in values), 'analysis array invalid')
    require(analyze['image']['size'] == image.stat().st_size, 'original image size mismatch')
    print('Analyze / Vertex AI: PASS (HTTP 200; schema/ranges/arrays)', flush=True)
    stage = 'Recommendation'
    request = {'analysis': analysis, 'preferences': {'languages': ['ko', 'en'], 'count': 10}}
    (out / 'recommend-request.json').write_text(json.dumps(request, ensure_ascii=False, indent=2))
    response = call('/api/v1/recommend', ['-X', 'POST', '-H', 'Content-Type: application/json', '--data-binary', '@' + str(out / 'recommend-request.json')])
    (out / 'recommend.json').write_text(json.dumps(response, ensure_ascii=False, indent=2))
    tracks = response['tracks']
    require(isinstance(tracks, list) and len(tracks) > 0, 'no music candidates; stop before playlist write')
    require(response['requested_count'] == 10 and response['returned_count'] == len(tracks), 'recommend count mismatch')
    ids = [track['video_id'] for track in tracks]
    require(len(ids) == len(set(ids)) and all(isinstance(value, str) and value for value in ids), 'empty or duplicate video IDs')
    for track in tracks:
        require(track['title'] and track['channel_title'], 'missing YouTube metadata')
        require(90 <= track['duration_seconds'] <= 720, 'invalid track duration')
        require(0 <= track['match_score'] <= 1, 'invalid score')
    print('Recommendation: PASS (HTTP 200; %d/10 verified candidates)' % len(tracks), flush=True)
    stage = 'YouTube video verification'
    selected = ids[:3]
    query = urllib.parse.urlencode({'key': os.environ['YOUTUBE_API_KEY'], 'id': ','.join(ids), 'part': 'snippet,contentDetails,status,statistics'})
    # Never print this URL or a raw HTTPError: it contains the API key.
    with urllib.request.urlopen('https://www.googleapis.com/youtube/v3/videos?' + query, timeout=15) as reply:
        details = strict_json(reply.read().decode())
    actual = {item['id']: item for item in details.get('items', [])}
    for track in tracks:
        require(track['video_id'] in actual, 'video missing from independent videos.list')
        item = actual[track['video_id']]
        require(item['snippet']['title'] == track['title'] and item['snippet']['channelTitle'] == track['channel_title'], 'metadata does not match YouTube')
        require(item['snippet']['categoryId'] == '10' and item['status']['embeddable'], 'not a usable music video')
        restriction = item.get('contentDetails', {}).get('regionRestriction', {})
        region = os.environ['YOUTUBE_REGION']
        require(region not in restriction.get('blocked', []) and ('allowed' not in restriction or region in restriction['allowed']), 'region restriction')
    (out / 'video-verification.json').write_text(json.dumps(details, ensure_ascii=False, indent=2))
    payload = {'title': os.environ.get('SYNC_E2E_PLAYLIST_TITLE', 'Sync E2E Test'), 'description': os.environ.get('SYNC_E2E_PLAYLIST_DESCRIPTION', 'Created during backend end-to-end test.'), 'privacy_status': 'private', 'tracks': [{'video_id': value} for value in selected]}
    (out / 'playlist-request.json').write_text(json.dumps(payload, ensure_ascii=False, indent=2))
    print('YouTube video verification: PASS (%d independently rechecked IDs)' % len(ids), flush=True)
    timings['time_to_recommendations_ms'] = timings['/api/v1/analyze'] + timings['/api/v1/recommend']
    (out / 'timings.json').write_text(json.dumps(timings, indent=2))
    print('Time to recommendations: %.2f seconds' % (timings['time_to_recommendations_ms'] / 1000), flush=True)
    print('Public E2E: PASS; OAuth/private playlist verification remains', flush=True)
    print('Responses: ' + str(out), flush=True)
except Exception as error:
    # Validation messages above contain no credentials; other exceptions are opaque.
    reason = str(error) if type(error) is ValueError else type(error).__name__
    print(stage + ': FAIL (' + reason + ')', file=sys.stderr)
    sys.exit(1)
PY
