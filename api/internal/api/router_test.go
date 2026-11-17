package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/jobs"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestHealth(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := SetupRouter(logger, jobs.New(jobs.Config{Logger: logger}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, healthPath, nil))
	if w.Code != http.StatusOK || w.Body.String() != `{"status":"ok"}` {
		t.Errorf("got %d %s, want 200 {\"status\":\"ok\"}", w.Code, w.Body)
	}
}
