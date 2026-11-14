package jobs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// jobsAccepted counts jobs recorded and enqueued (202s), by format and size class: the job
// counts on the local Grafana dashboard. Completion counts come from job records, not metrics.
var jobsAccepted = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "jobs_accepted_total",
	Help: "Jobs recorded and enqueued, by output format and size class.",
}, []string{"output_format", "size_class"})
