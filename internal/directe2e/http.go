package directe2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type App struct {
	Runner  *Runner
	Base    http.Handler
	OAuth   *auth.GoogleOAuthService
	writeMu sync.Mutex
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code string) {
	respond(w, status, model.ErrorResponse{Error: model.APIError{Code: code, Message: "local E2E request could not be completed"}})
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/e2e" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(Page))
	case r.URL.Path == "/api/v1/recommend" && r.Method == "POST":
		a.recommend(w, r)
	case r.URL.Path == "/api/v1/e2e/recommendation" && r.Method == "GET":
		a.Runner.Mu.Lock()
		defer a.Runner.Mu.Unlock()
		if a.Runner.Checkpoint == nil {
			problem(w, 409, "CHECKPOINT_NOT_READY")
			return
		}
		respond(w, 200, a.Runner.Response())
	case r.URL.Path == "/api/v1/playlists" && r.Method == "POST":
		a.playlist(w, r)
	case r.URL.Path == "/api/v1/auth/google/status" && r.Method == "GET":
		rec := httptest.NewRecorder()
		a.Base.ServeHTTP(rec, r)
		var connection auth.Connection
		if json.Unmarshal(rec.Body.Bytes(), &connection) == nil {
			_ = Save(a.Runner.Output, "oauth-result-sanitized.json", connection)
		}
		copyResponse(w, rec)
	default:
		a.Base.ServeHTTP(w, r)
	}
}
func copyResponse(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}
func (a *App) recommend(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		problem(w, 403, "INVALID_REQUEST_ORIGIN")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxImageSize+64*1024)
	defer r.Body.Close()
	reader, e := r.MultipartReader()
	if e != nil {
		problem(w, 415, "DIRECT_IMAGE_REQUIRED")
		return
	}
	found := false
	var image model.UploadedImage
	for {
		p, e := reader.NextPart()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			problem(w, 400, "INVALID_REQUEST")
			return
		}
		if p.FormName() == "image" && p.FileName() != "" {
			if found {
				problem(w, 400, "INVALID_REQUEST")
				return
			}
			found = true
			image, e = service.ReadImage(r.Context(), p.FileName(), p)
		} else {
			_, e = io.Copy(io.Discard, p)
		}
		p.Close()
		if e != nil {
			problem(w, 400, "INVALID_IMAGE")
			return
		}
	}
	if _, e = io.Copy(io.Discard, r.Body); e != nil {
		problem(w, 413, "REQUEST_TOO_LARGE")
		return
	}
	if !found {
		problem(w, 400, "IMAGE_REQUIRED")
		return
	}
	out, e := a.Runner.Run(r.Context(), image)
	if e != nil {
		problem(w, 502, "DIRECT_E2E_FAILED")
		return
	}
	respond(w, 200, out)
}
func (a *App) playlist(w http.ResponseWriter, r *http.Request) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.Runner.Mu.Lock()
	cp := a.Runner.Checkpoint
	a.Runner.Mu.Unlock()
	if cp == nil || len(cp.Tracks) == 0 {
		problem(w, 409, "VERIFIED_TRACKS_REQUIRED")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	data, e := io.ReadAll(r.Body)
	r.Body.Close()
	if e != nil {
		problem(w, 400, "INVALID_REQUEST")
		return
	}
	req, e := model.DecodePlaylistRequest(data)
	if e != nil || req.PrivacyStatus != "private" || len(req.Tracks) != len(cp.Tracks) {
		problem(w, 400, "PRIVATE_VERIFIED_PLAYLIST_REQUIRED")
		return
	}
	for i, t := range req.Tracks {
		if t.VideoID != cp.Tracks[i].VideoID {
			problem(w, 400, "UNVERIFIED_TRACK")
			return
		}
	}
	// Return an already captured write result without any insert replay.
	if b, e := os.ReadFile(filepath.Join(a.Runner.Output, "playlist-result.json")); e == nil {
		var v any
		json.Unmarshal(b, &v)
		respond(w, 201, v)
		return
	}
	marker, e := os.OpenFile(filepath.Join(a.Runner.Output, "phase-b-started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		problem(w, 409, "PLAYLIST_WRITE_ALREADY_ATTEMPTED")
		return
	}
	marker.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	rec := httptest.NewRecorder()
	a.Base.ServeHTTP(rec, r)
	if rec.Code == 401 || rec.Code == 400 || rec.Code == 403 {
		_ = os.Remove(filepath.Join(a.Runner.Output, "phase-b-started"))
		copyResponse(w, rec)
		return
	}
	var result model.CreatePlaylistResponse
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.Playlist.ID == "" {
		_ = Save(a.Runner.Output, "e2e-summary.json", map[string]any{"status": "E2E_BLOCKED", "phase": "playlist", "http_status": rec.Code, "recommendation_checkpoint_retained": true})
		copyResponse(w, rec)
		return
	}
	if e = Save(a.Runner.Output, "playlist-result.json", result); e != nil {
		problem(w, 500, "WRITE_RESULT_PERSISTENCE_FAILED")
		return
	}
	_ = Save(a.Runner.Output, "playlist-items-result.json", result.Items)
	session, _ := r.Cookie("sync_session")
	readback := map[string]any{"verified": false, "reason": "READBACK_UNAVAILABLE"}
	if session != nil && a.OAuth != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var ids []string
		e = a.OAuth.WithAuthenticatedClient(ctx, session.Value, func(hc *http.Client) error {
			api, e := youtube.NewService(ctx, option.WithHTTPClient(hc), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if e != nil {
				return e
			}
			list, e := api.PlaylistItems.List([]string{"snippet"}).PlaylistId(result.Playlist.ID).MaxResults(50).Context(ctx).Do()
			if e != nil {
				return e
			}
			for _, item := range list.Items {
				if item.Snippet != nil && item.Snippet.ResourceId != nil {
					ids = append(ids, item.Snippet.ResourceId.VideoId)
				}
			}
			return nil
		})
		expected := []string{}
		for _, item := range result.Items {
			if item.Status == "added" {
				expected = append(expected, item.VideoID)
			}
		}
		match := e == nil && len(ids) == len(expected)
		if match {
			for i := range ids {
				if ids[i] != expected[i] {
					match = false
				}
			}
		}
		readback = map[string]any{"verified": match, "expected_video_ids": expected, "actual_video_ids": ids, "read_error": e != nil}
	}
	_ = Save(a.Runner.Output, "playlist-readback.json", readback)
	status := "E2E_PASS"
	if result.FailedCount > 0 || len(cp.Tracks) < a.Runner.Service.Config.FinalCount || readback["verified"] != true {
		status = "E2E_PARTIAL"
	}
	if result.Playlist.PrivacyStatus != "private" {
		status = "E2E_BLOCKED"
	}
	_ = Save(a.Runner.Output, "e2e-summary.json", map[string]any{"status": status, "configured_search_budget": SearchCap, "actual_search_calls": cp.SearchCalls, "verified_count": len(cp.Tracks), "private": result.Playlist.PrivacyStatus == "private", "playlist_id": result.Playlist.ID, "added": result.AddedCount, "failed": result.FailedCount, "readback": readback, "production_default": "legacy"})
	copyResponse(w, rec)
}
