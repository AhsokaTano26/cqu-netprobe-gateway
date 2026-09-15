package metrics

import "github.com/prometheus/client_golang/prometheus"

// Self holds the gateway's own instrumentation. Reason labels are drawn from a
// fixed set of error codes; raw error strings are never used as labels, because
// they would be unbounded.
type Self struct {
	PushTotal     prometheus.Counter
	PushRejected  *prometheus.CounterVec
	AuthFailed    *prometheus.CounterVec
	HTTPRequests  *prometheus.CounterVec
	CollectErrors prometheus.Counter
}

// NewSelf registers the gateway's self-metrics against reg.
func NewSelf(reg prometheus.Registerer) *Self {
	s := &Self{
		PushTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cqu_netprobe_gateway_push_total",
			Help: "Accepted push requests.",
		}),
		PushRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cqu_netprobe_gateway_push_rejected_total",
			Help: "Rejected push requests by reason.",
		}, []string{"reason"}),
		AuthFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cqu_netprobe_gateway_auth_failed_total",
			Help: "Failed authentication attempts by reason.",
		}, []string{"reason"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cqu_netprobe_gateway_http_requests_total",
			Help: "HTTP requests by method, route pattern and status.",
		}, []string{"method", "path", "status"}),
		CollectErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cqu_netprobe_gateway_collect_errors_total",
			Help: "Failed metric collection attempts.",
		}),
	}
	reg.MustRegister(s.PushTotal, s.PushRejected, s.AuthFailed, s.HTTPRequests, s.CollectErrors)
	return s
}
