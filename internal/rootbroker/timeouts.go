package rootbroker

import "time"

const defaultRequestTimeout = 30 * time.Second

// RequestTimeout returns the broker-side safety deadline for a typed request.
// The unprivileged caller still supplies its own, usually shorter, context.
func RequestTimeout(req *Request) time.Duration {
	if req != nil && req.RequestType == "certificate" && req.Certificate != nil && req.Certificate.Action == "issue" {
		return 15 * time.Minute
	}
	return defaultRequestTimeout
}
