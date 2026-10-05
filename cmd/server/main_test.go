package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"example.com/sync/internal/config"
	"example.com/sync/internal/router"
)

// No provider constructors or credentials are used in these server tests.
func TestRenderListenAddress(t *testing.T) {
	for _, tc := range []struct {
		name, port, want string
		unset, invalid   bool
	}{
		{name: "render", port: "10000", want: ":10000"},
		{name: "local default", unset: true, want: ":8080"},
		{name: "empty", port: "", invalid: true},
		{name: "non integer", port: "abc", invalid: true},
		{name: "out of range", port: "65536", invalid: true},
		{name: "host in port", port: "localhost:10000", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOOGLE_CLOUD_PROJECT", "offline-test-project")
			t.Setenv("APP_ENV", "production")
			t.Setenv("RECOMMENDATION_ENGINE", "legacy")
			t.Setenv("PORT", tc.port)
			if tc.unset {
				if err := os.Unsetenv("PORT"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.Load()
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid PORT accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			server := newHTTPServer(cfg, http.NotFoundHandler())
			if server.Addr != tc.want {
				t.Fatalf("address=%s want=%s", server.Addr, tc.want)
			}
			if server.ReadHeaderTimeout <= 0 || server.IdleTimeout <= 0 {
				t.Fatal("server timeouts missing")
			}
		})
	}
}
func TestProductionHealthWithoutDependencies(t *testing.T) {
	cfg := config.Config{AppEnv: "production", RecommendationEngine: config.LegacyEngine, Port: "10000"}
	// A nil image/recommendation/OAuth dependency cannot make health contact an API.
	server := newHTTPServer(cfg, router.New(cfg, nil, nil))
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(body) != 1 || body["status"] != "ok" {
		t.Fatalf("status=%d body=%v", w.Code, body)
	}
}
