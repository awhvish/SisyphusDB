package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Raft Metrics
	TermGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "raft_current_term",
		Help: "Current Raft term",
	}, []string{"node_id"})

	CommitIndexGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "raft_commit_index",
		Help: "Current commit index",
	}, []string{"node_id"})

	StateGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "raft_state",
		Help: "Current state (0=Follower, 1=Candidate, 2=Leader)",
	}, []string{"node_id"})

	LeaderGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "raft_leader",
		Help: "Current leader ID",
	}, []string{"node_id"})

	// KV Store Metrics
	LevelFileCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kv_level_file_count",
		Help: "Number of SSTables at each level",
	}, []string{"node_id", "level"})

	LevelSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kv_level_size_bytes",
		Help: "Total size of all SSTables at a level",
	}, []string{"node_id", "level"})

	// HTTP Metrics
	HttpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests",
	}, []string{"method", "endpoint", "status"}) // Labels

	HttpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Histogram of response latency (seconds)",
		Buckets: prometheus.DefBuckets, // Default buckets (.005, .01, .025, ... 10)
	}, []string{"method", "endpoint"})
)
