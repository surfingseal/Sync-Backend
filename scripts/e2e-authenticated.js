// This run uses only video IDs independently verified from the actual photo E2E.
// Paste the whole file into the same localhost:8080 browser console AFTER OAuth.
// Initialization does not write. Call: await syncE2E.run()
window.syncE2E = (() => {
  const request = {
  "title": "Sync Latency Test",
  "description": "Sync latency benchmark E2E test.",
  "privacy_status": "private",
  "tracks": [
    {
      "video_id": "ZJ_BMk5q7aQ"
    },
    {
      "video_id": "Dq84EgSH22I"
    },
    {
      "video_id": "9zoXMX906mM"
    }
  ]
};
  let attempted = false;
  async function run() {
    if (location.origin !== 'http://localhost:8080') throw new Error('Open the localhost:8080 API page first.');
    if (attempted) throw new Error('A write was already attempted. Check YouTube; do not retry automatically.');
    const statusResponse = await fetch('/api/v1/auth/google/status', {credentials: 'same-origin'});
    const status = await statusResponse.json();
    if (!statusResponse.ok || !status.connected) throw new Error('Reconnect through /api/v1/auth/google first.');
    if (request.privacy_status !== 'private') throw new Error('E2E must be private.');
    attempted = true;
    const started = performance.now();
    const response = await fetch('/api/v1/playlists', {
      method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(request), signal: AbortSignal.timeout(135000),
    });
    const result = await response.json();
    result.e2e_playlist_total_ms = performance.now() - started;
    // Contains only playlist/track results. Never reads cookies or OAuth tokens.
    console.log(JSON.stringify(result, null, 2));
    if (response.status !== 201) throw new Error(result.error?.code || 'Playlist write failed');
    if (!result.playlist?.id || !result.playlist?.url || result.playlist.privacy_status !== 'private' ||
        result.requested_count !== request.tracks.length || result.added_count < 1 ||
        !Array.isArray(result.items) || result.items.length !== request.tracks.length ||
        result.added_count + result.failed_count !== result.requested_count ||
        result.items.some((item, index) => item.video_id !== request.tracks[index].video_id) ||
        result.items.some(item => item.status === 'added' && !item.playlist_item_id)) {
      throw new Error('Playlist response validation failed; check YouTube before retrying.');
    }
    console.log('E2E write result: added=' + result.added_count + ', failed=' + result.failed_count + ', private=true');
    return result;
  }
  return {run};
})();
