package router

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"example.com/sync/internal/apidocs"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directapi"
	"example.com/sync/internal/mobileauth"
	"example.com/sync/internal/model"
)

func TestOpenAPIExactlyMatchesRegisteredAPIRoutes(t *testing.T) {
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(apidocs.Spec(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.0.3" {
		t.Fatal(doc.OpenAPI)
	}
	for _, env := range []string{"development", "production"} {
		r := New(config.Config{AppEnv: env}, nil, nil)
		actual := map[string]bool{}
		for _, route := range r.Routes() {
			if strings.HasPrefix(route.Path, "/swagger") {
				continue
			}
			actual[strings.ToLower(route.Method)+" "+route.Path] = true
		}
		for path, methods := range doc.Paths {
			for method := range methods {
				key := method + " " + path
				if !actual[key] {
					t.Errorf("spec-only route: %s", key)
				}
				delete(actual, key)
			}
		}
		for key := range actual {
			t.Errorf("missing documentation: %s", key)
		}
	}
}
func TestDocsHealthAndDirectGuard(t *testing.T) {
	r := New(config.Config{AppEnv: "production"}, nil, nil)
	for _, tc := range []struct {
		method, path, contains string
		status                 int
	}{{"GET", "/health", `"status":"ok"`, 200}, {"GET", "/swagger", "supportedSubmitMethods:[]", 200}, {"GET", "/swagger/openapi.json", `"openapi": "3.0.3"`, 200}, {"POST", "/api/v1/recommend/direct", "DIRECT_RECOMMENDATION_UNAVAILABLE", 503}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s status%d body%s", tc.path, w.Code, w.Body)
		}
	}
}
func TestOpenAPIDTOResponseFields(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(apidocs.Spec(), &doc); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"DirectapiResponse": directapi.Response{}, "DirectapiTrack": directapi.Track{}, "DirectapiLanguagePolicy": directapi.LanguagePolicy{}, "ModelCreatePlaylistResponse": model.CreatePlaylistResponse{}, "ModelPlaylistItemResult": model.PlaylistItemResult{}, "MobileauthStartResponse": mobileauth.StartResponse{}, "MobileauthExchangeResponse": mobileauth.ExchangeResponse{}, "AuthConnection": auth.Connection{}, "AuthChannel": auth.Channel{}, "ModelErrorResponse": model.ErrorResponse{}} {
		typ := reflect.TypeOf(v)
		props := doc.Components.Schemas[name].Properties
		if len(props) != typ.NumField() {
			t.Errorf("%s field count mismatch", name)
		}
		for i := 0; i < typ.NumField(); i++ {
			key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if _, ok := props[key]; !ok {
				t.Errorf("%s missing %s", name, key)
			}
		}
	}
}
func TestOpenAPINullableArtworkAndAuth(t *testing.T) {
	var doc map[string]any
	json.Unmarshal(apidocs.Spec(), &doc)
	c := doc["components"].(map[string]any)
	s := c["schemas"].(map[string]any)
	props := s["DirectapiTrack"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"album_title", "album_artwork_url"} {
		if props[name].(map[string]any)["nullable"] != true {
			t.Error("artwork not nullable")
		}
	}
	sec := c["securitySchemes"].(map[string]any)
	if sec["SyncBearer"].(map[string]any)["scheme"] != "bearer" || sec["BrowserSession"].(map[string]any)["in"] != "cookie" {
		t.Fatal("auth mismatch")
	}
	paths := doc["paths"].(map[string]any)
	direct := paths["/api/v1/recommend/direct"].(map[string]any)["post"].(map[string]any)
	if len(direct["security"].([]any)) != 0 {
		t.Fatal("direct incorrectly requires auth")
	}
	if _, ok := direct["requestBody"].(map[string]any)["content"].(map[string]any)["multipart/form-data"]; !ok {
		t.Fatal("missing upload")
	}
}
