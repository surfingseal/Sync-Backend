// Package apidocs serves the checked-in OpenAPI contract without runtime generation.
package apidocs

import (
	"embed"
	"github.com/gin-gonic/gin"
	"net/http"
)

//go:embed openapi.json swagger.html
var files embed.FS

func Spec() []byte { b, _ := files.ReadFile("openapi.json"); return b }

// Register exposes nonsecret, read-only documentation in all environments.
// API readiness/auth guards are independent and remain unchanged.
func Register(r *gin.Engine) {
	r.GET("/swagger", func(c *gin.Context) {
		b, _ := files.ReadFile("swagger.html")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Data(http.StatusOK, "text/html; charset=utf-8", b)
	})
	r.GET("/swagger/openapi.json", func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "application/json; charset=utf-8", Spec())
	})
}
