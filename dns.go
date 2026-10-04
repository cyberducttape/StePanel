package stepanel

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// DNSProvider is the provider-neutral contract owned by the domain layer.
// Adapters must make operations idempotent and must not expose provider
// credentials to the control plane or customer API.
type DNSProvider interface {
	ListZones(context.Context) ([]DNSZone, error)
	CreateZone(context.Context, DNSZone) error
	DeleteZone(context.Context, string) error
	ListRecords(context.Context, string) ([]DNSRecord, error)
	UpsertRecord(context.Context, DNSRecord) error
	DeleteRecord(context.Context, string, string) error
	DNSSEC(context.Context, string) (DNSSECStatus, error)
}

type DNSZone struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}
type DNSRecord struct {
	ID     string `json:"id,omitempty"`
	ZoneID string `json:"zone_id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Target string `json:"target"`
	TTL    int    `json:"ttl"`
}
type DNSSECStatus struct {
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
}

type DNSCapability struct {
	Provider string   `json:"provider"`
	Status   string   `json:"status"`
	Adapters []string `json:"adapters"`
	DNSSEC   bool     `json:"dnssec"`
	Detail   string   `json:"detail"`
}

func (a *App) dnsCapability() DNSCapability {
	provider := strings.ToLower(strings.TrimSpace(a.Config.CloudProvider))
	adapters := []string{"powerdns", "rfc2136", "cloudflare", "route53", "linode", "digitalocean"}
	if provider == "linode" {
		detail := "Linode DNS is available through the existing provider adapter; zone ownership and DNSSEC remain operator-managed."
		if a.DNSDesired != nil {
			detail = "Linode DNS record mutations use durable desired state and retryable jobs; zone ownership and DNSSEC remain operator-managed."
		}
		return DNSCapability{Provider: provider, Status: "adapter-available", Adapters: adapters, DNSSEC: false, Detail: detail}
	}
	return DNSCapability{Provider: provider, Status: "not-configured", Adapters: adapters, Detail: "No DNS provider adapter is configured. DNS changes are not simulated or silently applied."}
}

func (a *App) dnsCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"capability": a.dnsCapability(), "desired_state": a.DNSDesired != nil, "dnssec": false})
}

var errDNSProviderNotConfigured = errors.New("DNS provider adapter is not configured")
