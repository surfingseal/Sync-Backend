package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"example.com/sync/internal/mobileauth"
	"github.com/gin-gonic/gin"
)

const credentialContextKey = "sync.auth.credential_reference"

// credentialReference never exposes the Sync bearer token to Google clients.
func credentialReference(c *gin.Context) string {
	if value, ok := c.Get(credentialContextKey); ok {
		return value.(string)
	}
	return cookieValue(c, sessionCookie)
}

// CredentialResolver implements only local identity lookup, without provider calls.
type CredentialResolver interface {
	SessionExists(context.Context, string) (bool, error)
}

// Authenticate retains cookie-only behavior. An explicitly supplied bad bearer
// never falls back to cookies. Two live credential references must match exactly;
// separate Google logins are conservatively treated as distinct identities.
func Authenticate(mobile *mobileauth.Service, credentials CredentialResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		values := c.Request.Header.Values("Authorization")
		if len(values) == 0 {
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		if len(values) != 1 {
			writeError(c, 401, "MOBILE_SESSION_INVALID", "mobile session is invalid or expired")
			c.Abort()
			return
		}
		fields := strings.Fields(values[0])
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			writeError(c, 401, "MOBILE_SESSION_INVALID", "mobile session is invalid or expired")
			c.Abort()
			return
		}
		ref, err := mobile.Resolve(c.Request.Context(), fields[1])
		if err != nil {
			writeError(c, 401, "MOBILE_SESSION_INVALID", "mobile session is invalid or expired")
			c.Abort()
			return
		}
		cookie := cookieValue(c, sessionCookie)
		if cookie != "" && cookie != ref {
			exists, e := credentials.SessionExists(c.Request.Context(), cookie)
			if e != nil || exists {
				writeError(c, 409, "AUTH_IDENTITY_CONFLICT", "browser and mobile sessions conflict")
				c.Abort()
				return
			}
		}
		c.Set(credentialContextKey, ref)
		c.Next()
	}
}

type MobileAuthHandler struct {
	mobile *mobileauth.Service
	google *GoogleOAuthHandler
}

func NewMobileAuthHandler(mobile *mobileauth.Service, google *GoogleOAuthHandler) *MobileAuthHandler {
	return &MobileAuthHandler{mobile, google}
}
func privateAuthResponse(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
}
func (h *MobileAuthHandler) ready(c *gin.Context) bool {
	privateAuthResponse(c)
	if !h.mobile.Configured() {
		mobileError(c, mobileauth.ErrUnavailable)
		return false
	}
	return true
}
func decodeMobile(c *gin.Context, out any) bool {
	media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(c, 415, "UNSUPPORTED_CONTENT_TYPE", "application/json is required")
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	defer c.Request.Body.Close()
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeError(c, 400, "INVALID_REQUEST", "invalid mobile authorization request")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		writeError(c, 400, "INVALID_REQUEST", "invalid mobile authorization request")
		return false
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(c, 400, "INVALID_REQUEST", "invalid mobile authorization request")
		return false
	}
	return true
}
func (h *MobileAuthHandler) Start(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var req struct {
		Challenge string `json:"code_challenge"`
		Method    string `json:"code_challenge_method"`
	}
	if !decodeMobile(c, &req) {
		return
	}
	res, e := h.mobile.Start(c.Request.Context(), req.Challenge, req.Method)
	if e != nil {
		mobileError(c, e)
		return
	}
	c.JSON(200, res)
}
func (h *MobileAuthHandler) Authorize(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	t, e := h.mobile.Begin(c.Request.Context(), c.Query("transaction"), cookieValue(c, sessionCookie))
	if e != nil {
		mobileError(c, e)
		return
	}
	h.google.cookie(c, stateCookie, t.OAuthState, mobileauth.TransactionTTL)
	c.Redirect(302, t.AuthorizationURL)
}
func (h *MobileAuthHandler) Exchange(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var req struct {
		Code     string `json:"code"`
		Verifier string `json:"code_verifier"`
	}
	if !decodeMobile(c, &req) {
		return
	}
	res, e := h.mobile.Exchange(c.Request.Context(), req.Code, req.Verifier)
	if e != nil {
		mobileError(c, e)
		return
	}
	c.JSON(200, res)
}
func (h *MobileAuthHandler) Logout(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	values := strings.Fields(c.GetHeader("Authorization"))
	if len(values) != 2 || !strings.EqualFold(values[0], "Bearer") {
		writeError(c, 401, "MOBILE_SESSION_INVALID", "a mobile bearer session is required")
		return
	}
	h.mobile.Revoke(values[1])
	c.JSON(200, gin.H{"logged_out": true})
}
func (h *MobileAuthHandler) AssetLinks(c *gin.Context) {
	c.Header("X-Content-Type-Options", "nosniff")
	if !h.mobile.Configured() {
		mobileError(c, mobileauth.ErrUnavailable)
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(200, h.mobile.AssetLinks())
}
func (h *MobileAuthHandler) Completion(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	c.Data(200, "text/html; charset=utf-8", []byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Sync authorization</title><p>Return to the Sync app to finish connecting. If the app did not open, check its verified App Link configuration.</p></html>"))
}
func mobileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mobileauth.ErrUnavailable):
		writeError(c, 503, "MOBILE_AUTH_UNAVAILABLE", "mobile authorization is not configured")
	case errors.Is(err, mobileauth.ErrChallenge):
		writeError(c, 400, "MOBILE_CHALLENGE_INVALID", "a valid S256 challenge is required")
	case errors.Is(err, mobileauth.ErrInvalid):
		writeError(c, 400, "MOBILE_HANDOFF_INVALID", "mobile authorization is invalid, expired or already used")
	case errors.Is(err, mobileauth.ErrCapacity):
		writeError(c, 503, "MOBILE_AUTH_CAPACITY", "mobile authorization is temporarily unavailable")
	default:
		writeError(c, 500, "INTERNAL_ERROR", "internal server error")
	}
}
