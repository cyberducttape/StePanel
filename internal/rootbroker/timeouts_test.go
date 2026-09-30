package rootbroker

import (
	"testing"
	"time"
)

func TestRequestTimeoutBoundsCertificateIssuance(t *testing.T) {
	if got := RequestTimeout(nil); got != defaultRequestTimeout {
		t.Fatalf("nil request timeout = %s", got)
	}
	if got := RequestTimeout(&Request{RequestType: "site"}); got != defaultRequestTimeout {
		t.Fatalf("default request timeout = %s", got)
	}
	request := &Request{RequestType: "certificate", Certificate: &CertificateRequest{Action: "issue"}}
	if got, want := RequestTimeout(request), 15*time.Minute; got != want {
		t.Fatalf("certificate timeout = %s, want %s", got, want)
	}
	request.Certificate.Action = "unsupported"
	if got := RequestTimeout(request); got != defaultRequestTimeout {
		t.Fatalf("unsupported certificate timeout = %s", got)
	}
}
