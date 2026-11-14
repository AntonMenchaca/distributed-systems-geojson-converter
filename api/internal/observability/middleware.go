package observability

import (
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The RED signals for every endpoint: Rate, Errors and Duration. Each
// request is labeled with its route template, not its raw path, so a route
// with path parameters adds to one series instead of one per value.
var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests handled, by method, endpoint and status code.",
	}, []string{"method", "endpoint", "status"})

	requestErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_errors_total",
		Help: "HTTP requests that returned a non-2xx status code.",
	}, []string{"method", "endpoint", "status"})

	// Order requests span four orders of magnitude: a rejected order takes
	// well under a millisecond, an async order a few milliseconds, and a sync
	// order at least the 3s payment plus however long it waits for a payment
	// slot, up to the load balancer's 60s timeout. These run from 0.5ms to
	// about 65s, doubling, so each of those lands in its own buckets instead
	// of the top one.
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time to handle an HTTP request, in seconds.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 18),
	}, []string{"method", "endpoint"})
)

// ObserveRequests records each request's RED signals twice: as Prometheus
// metrics, for live dashboards, and as one JSON log line, for debugging
// after the fact. It must run before the recovery middleware, so that a
// panic's 500 is recorded too. Requests to skipPaths, such as health checks
// and metrics scrapes, are neither counted nor logged.
func ObserveRequests(logger *slog.Logger, skipPaths ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		endpoint := c.FullPath()
		// Prometheus scrapes every few seconds and the load balancer checks
		// health every 30; counting those would add busy endpoints to the
		// dashboard and the logs that no user ever calls.
		if slices.Contains(skipPaths, endpoint) {
			c.Next()
			return
		}
		// FullPath is empty when no route matched, i.e. Gin's 404.
		if endpoint == "" {
			endpoint = "unmatched"
		}

		start := time.Now()
		c.Next()
		duration := time.Since(start)

		method := c.Request.Method
		status := c.Writer.Status()
		statusLabel := strconv.Itoa(status)

		requestsTotal.WithLabelValues(method, endpoint, statusLabel).Inc()
		if status < 200 || status > 299 {
			requestErrorsTotal.WithLabelValues(method, endpoint, statusLabel).Inc()
		}
		requestDuration.WithLabelValues(method, endpoint).Observe(duration.Seconds())

		attrs := []any{
			"method", method,
			"endpoint", endpoint,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", float64(duration) / float64(time.Millisecond),
		}
		logger.Info("request", attrs...)
	}
}
