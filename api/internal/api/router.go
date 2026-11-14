// Package api wires the service's HTTP endpoints and middleware.
package api

import (
	"log/slog"
	"net/http"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/jobs"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsPath is where Prometheus scrapes the service's metrics.
const metricsPath = "/metrics"

// healthPath is where the load balancer checks that a task is up. It
// answers as soon as the server is listening.
const healthPath = "/health"

// SetupRouter returns the service's Gin engine with every route and
// middleware registered. Requests are logged to logger; job routes are
// served by jobHandlers.
func SetupRouter(logger *slog.Logger, jobHandlers *jobs.Handlers) *gin.Engine {
	router := gin.New()
	// One JSON log line per request. Prometheus scrapes every few seconds and
	// the load balancer checks health every 30; counting those would add
	// busy endpoints to the dashboard and the logs that no user ever calls.
	router.Use(observability.ObserveRequests(logger, metricsPath, healthPath))
	// Report panics with api.yaml's Error body instead of Gin's empty 500.
	router.Use(gin.CustomRecovery(func(c *gin.Context, _ any) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "INTERNAL_ERROR",
			Message: "Internal server error",
		})
	}))

	jobHandlers.Register(router)
	router.GET(metricsPath, gin.WrapH(promhttp.Handler()))
	router.GET(healthPath, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return router
}
