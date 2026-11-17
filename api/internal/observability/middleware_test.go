package observability

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestSkippedPathIsNotLogged(t *testing.T) {
	var logs bytes.Buffer
	router := gin.New()
	router.Use(ObserveRequests(slog.New(slog.NewJSONHandler(&logs, nil)), "/health"))
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /health: status %d, want 200", w.Code)
	}
	if logs.Len() != 0 {
		t.Errorf("health check was logged: %s", logs.String())
	}
}
