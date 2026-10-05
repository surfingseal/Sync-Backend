package router

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"github.com/gin-gonic/gin"
)

func TestHealth(t *testing.T) {
	r := New(config.Config{AppEnv: "production"}, nil, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body["status"] != "ok" || len(body) != 1 {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", w.Header().Get("Content-Type"))
	}
}

func TestUnimplementedRoutes(t *testing.T) {
	r := New(config.Config{AppEnv: "production"}, nil, nil)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/not-implemented"},
	} {
		t.Run(route.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			var body model.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusNotFound || body.Error.Code != "NOT_FOUND" || body.Error.Message == "" {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRecovery(t *testing.T) {
	r := New(config.Config{AppEnv: "production"}, nil, nil)
	r.GET("/panic", func(_ *gin.Context) { panic("test panic") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panic", nil))
	var body model.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusInternalServerError || body.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthWithoutConfig(t *testing.T) {
	r := New(config.Config{AppEnv: "production"}, nil, nil)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/auth/google"}, {"GET", "/api/v1/auth/google/callback"}, {"GET", "/api/v1/auth/google/status"}, {"DELETE", "/api/v1/auth/google"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if w.Code != 500 || !strings.Contains(w.Body.String(), "OAUTH_CONFIGURATION_ERROR") {
			t.Fatal("OAuth config error missing")
		}
	}
}
func TestLogsNeverContainOAuthSecrets(t *testing.T) {
	var output bytes.Buffer
	oldWriter := gin.DefaultWriter
	gin.DefaultWriter = &output
	defer func() { gin.DefaultWriter = oldWriter }()
	oldLog := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(oldLog)
	r := New(config.Config{AppEnv: "development"}, nil, nil)
	r.GET("/panic-secret", func(c *gin.Context) { panic("panic-sensitive-value") })
	for _, path := range []string{"/api/v1/auth/google/callback?code=authorization-sensitive-value&state=state-sensitive-value", "/panic-secret?code=authorization-sensitive-value"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer token-sensitive-value")
		req.Header.Set("Cookie", "sync_session=cookie-sensitive-value")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	for _, secret := range []string{"authorization-sensitive-value", "state-sensitive-value", "token-sensitive-value", "cookie-sensitive-value", "panic-sensitive-value"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("sensitive data leaked in logs")
		}
	}
}
