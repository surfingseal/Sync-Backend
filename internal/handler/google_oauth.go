package handler

import (
	"errors"
	"log"
	"net/http"
	"time"

	"example.com/sync/internal/auth"
	"github.com/gin-gonic/gin"
)

const stateCookie = "sync_oauth_state"
const sessionCookie = "sync_session"

type GoogleOAuthHandler struct{ oauth *auth.GoogleOAuthService }

func NewGoogleOAuthHandler(oauth *auth.GoogleOAuthService) *GoogleOAuthHandler {
	return &GoogleOAuthHandler{oauth}
}
func (h *GoogleOAuthHandler) ready(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if !h.oauth.Configured() {
		writeError(c, 500, "OAUTH_CONFIGURATION_ERROR", "Google OAuth is not configured")
		return false
	}
	return true
}
func (h *GoogleOAuthHandler) cookie(c *gin.Context, name, value string, age time.Duration) {
	expires := time.Now().Add(age)
	maxAge := int(age.Seconds())
	if age < 0 {
		maxAge = -1
		expires = time.Unix(1, 0)
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, Expires: expires, HttpOnly: true, Secure: h.oauth.CookieSecure(), SameSite: http.SameSiteLaxMode})
}
func cookieValue(c *gin.Context, name string) string { value, _ := c.Cookie(name); return value }
func (h *GoogleOAuthHandler) Start(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	state, location, err := h.oauth.Start(c.Request.Context(), cookieValue(c, sessionCookie))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.cookie(c, stateCookie, state, auth.StateLifetime)
	c.Redirect(http.StatusFound, location)
}
func (h *GoogleOAuthHandler) Callback(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	state := c.Query("state")
	cookie := cookieValue(c, stateCookie)
	h.cookie(c, stateCookie, "", -time.Second)
	if upstreamError := c.Query("error"); upstreamError != "" {
		h.oauth.Cancel(state, cookie)
		if upstreamError == "access_denied" {
			writeError(c, 400, "OAUTH_ACCESS_DENIED", "Google OAuth access was denied")
		} else {
			writeError(c, 400, "OAUTH_AUTHORIZATION_FAILED", "Google OAuth authorization failed")
		}
		log.Print("OAuth authorization declined or failed")
		return
	}
	id, connection, err := h.oauth.Complete(c.Request.Context(), state, cookie, c.Query("code"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.cookie(c, sessionCookie, id, auth.SessionLifetime)
	c.JSON(200, connection)
}
func (h *GoogleOAuthHandler) Status(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	connection, err := h.oauth.Status(c.Request.Context(), cookieValue(c, sessionCookie))
	if err != nil {
		h.fail(c, err)
		return
	}
	if !connection.Connected {
		h.cookie(c, sessionCookie, "", -time.Second)
	}
	c.JSON(200, connection)
}
func (h *GoogleOAuthHandler) Disconnect(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	if err := h.oauth.Disconnect(c.Request.Context(), cookieValue(c, sessionCookie)); err != nil {
		h.fail(c, err)
		return
	}
	h.oauth.Cancel(cookieValue(c, stateCookie), cookieValue(c, stateCookie))
	h.cookie(c, stateCookie, "", -time.Second)
	h.cookie(c, sessionCookie, "", -time.Second)
	c.JSON(200, auth.Connection{})
}
func (h *GoogleOAuthHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrConfiguration):
		writeError(c, 500, "OAUTH_CONFIGURATION_ERROR", "Google OAuth is not configured")
	case errors.Is(err, auth.ErrStateInvalid):
		writeError(c, 400, "OAUTH_STATE_INVALID", "OAuth state is invalid or expired")
	case errors.Is(err, auth.ErrCodeMissing):
		writeError(c, 400, "OAUTH_CODE_MISSING", "OAuth authorization code is required")
	case errors.Is(err, auth.ErrTokenExchange):
		writeError(c, 502, "OAUTH_TOKEN_EXCHANGE_FAILED", "failed to connect Google account")
	case errors.Is(err, auth.ErrSessionNotFound):
		writeError(c, 401, "OAUTH_SESSION_NOT_FOUND", "OAuth session was not found")
	case errors.Is(err, auth.ErrYouTubeAuth):
		writeError(c, 502, "YOUTUBE_AUTH_FAILED", "failed to verify YouTube connection")
	default:
		log.Print("OAuth operation failed")
		writeError(c, 500, "INTERNAL_ERROR", "internal server error")
	}
}
