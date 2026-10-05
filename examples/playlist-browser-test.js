// Paste this file into the console of a localhost:8080 API page AFTER OAuth.
// Initialization makes no API call. Run recommend(), review, then create().
window.syncPlaylistTest = (() => {
  const example = {
  "analysis": {
    "scene": {
      "category": "ocean",
      "description": "sunset over a calm sea",
      "time_of_day": "sunset",
      "weather": "clear"
    },
    "visual": {
      "brightness": 0.55,
      "color_temperature": "warm",
      "dominant_colors": [
        "orange",
        "blue"
      ],
      "motion": "low"
    },
    "mood": {
      "tags": [
        "calm",
        "warm",
        "nostalgic",
        "dreamy"
      ],
      "energy": 0.3,
      "valence": 0.65
    },
    "music_profile": {
      "tempo": "slow-medium",
      "energy": 0.35,
      "vocal_preference": "soft-vocal",
      "genres": [
        "indie-pop",
        "acoustic",
        "dream-pop"
      ]
    }
  },
  "preferences": {
    "languages": [
      "ko",
      "en"
    ],
    "preferred_genres": [],
    "excluded_artists": [],
    "instrumental_only": false,
    "count": 10
  }
};
  let tracks = [];
  let writeAttempted = false;
  async function connected() {
    const response = await fetch('/api/v1/auth/google/status', {credentials: 'same-origin'});
    const body = await response.json();
    if (!response.ok || !body.connected) throw new Error('Connect YouTube through /api/v1/auth/google first.');
  }
  async function recommend(analysis = example.analysis) {
    await connected();
    const response = await fetch('/api/v1/recommend', {
      method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({analysis, preferences: {languages: ['ko', 'en'], count: 10}}),
      signal: AbortSignal.timeout(20000),
    });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error?.code || 'Recommendation failed');
    tracks = body.tracks;
    console.table(tracks.map((track, index) => ({index, video_id: track.video_id, title: track.title})));
    return tracks;
  }
  async function create(indices = [0, 1]) {
    if (writeAttempted) throw new Error('A write was already attempted. Check YouTube before another request.');
    if (!Array.isArray(indices) || indices.length < 1 || indices.length > 20 ||
        indices.some(index => !Number.isInteger(index) || !tracks[index])) {
      throw new Error('Select 1–20 valid indices from the latest recommendation.');
    }
    await connected();
    const payload = {
      title: 'Sync Test Playlist', description: 'Created by Sync during development.',
      privacy_status: 'private', tracks: indices.map(index => ({video_id: tracks[index].video_id})),
    };
    // Set this before sending: network failure does not prove that no write occurred.
    writeAttempted = true;
    const response = await fetch('/api/v1/playlists', {
      method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload), signal: AbortSignal.timeout(135000),
    });
    const body = await response.json();
    console.log(body);
    if (!response.ok) throw new Error(body.error?.code || 'Playlist creation failed');
    return body;
  }
  return {recommend, create};
})();
