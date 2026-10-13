package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Linode-specific request validation and wire payloads live here so the
// provider's contract is auditable independently of cloud job orchestration.
var (
	dnsTypePattern         = regexp.MustCompile(`^(A|AAAA|CNAME|MX|TXT|NS|SRV)$`)
	dnsNamePattern         = regexp.MustCompile(`^[A-Za-z0-9_.*@-]{1,253}$`)
	linodeNodeLabelPattern = regexp.MustCompile(`^[A-Za-z0-9-_.]{3,32}$`)
)

func validLinodeNodeRequest(request cloudLBRequest) bool {
	return net.ParseIP(request.Address) != nil && request.Port >= 1 && request.Port <= 65535 && request.Weight >= 1 && request.Weight <= 255 && linodeNodeLabelPattern.MatchString(request.Label)
}

type linodeNodeCreatePayload struct {
	Address string `json:"address"`
	Label   string `json:"label"`
	Weight  int    `json:"weight"`
}

func linodeNodePayload(request cloudLBRequest) (linodeNodeCreatePayload, error) {
	if !validLinodeNodeRequest(request) {
		return linodeNodeCreatePayload{}, errors.New("invalid NodeBalancer node address, port, weight, or label")
	}
	ip := net.ParseIP(request.Address)
	return linodeNodeCreatePayload{Address: net.JoinHostPort(ip.String(), strconv.Itoa(request.Port)), Label: request.Label, Weight: request.Weight}, nil
}

func cloudDNSRecordValid(in cloudDNSRequest) bool {
	typ := strings.ToUpper(in.Type)
	if !dnsTypePattern.MatchString(typ) || !validLinodeDNSName(typ, in.Name) || len(in.Target) == 0 || len(in.Target) > 512 || !validLinodeTTL(in.TTL) {
		return false
	}
	switch typ {
	case "A":
		address, err := netip.ParseAddr(in.Target)
		return err == nil && address.Is4()
	case "AAAA":
		address, err := netip.ParseAddr(in.Target)
		return err == nil && address.Is6() && !address.Is4In6()
	case "CNAME", "NS":
		return dnsNamePattern.MatchString(strings.TrimSuffix(in.Target, "."))
	case "MX":
		return in.Priority >= 0 && in.Priority <= 255 && validLinodeDNSName("NS", in.Target)
	case "SRV":
		return in.Priority >= 0 && in.Priority <= 255 && in.Weight >= 0 && in.Weight <= 65535 && in.Port >= 0 && in.Port <= 65535 && validDNSServiceLabel(in.Service) && validDNSServiceLabel(in.Protocol) && validLinodeDNSName("NS", in.Target)
	case "TXT":
		for _, r := range in.Target {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func validLinodeTTL(ttl int) bool {
	switch ttl {
	case 300, 3600, 7200, 14400, 28800, 57600, 86400, 172800, 345600, 604800, 1209600, 2419200:
		return true
	default:
		return false
	}
}

func validLinodeDNSName(recordType, name string) bool {
	if strings.EqualFold(recordType, "SRV") {
		return name == "" || dnsNamePattern.MatchString(name)
	}
	return dnsNamePattern.MatchString(name)
}

func validDNSServiceLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

type linodeDNSRecordBasePayload struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	TTL    int    `json:"ttl_sec"`
}

type linodeDNSNamedRecordPayload struct {
	linodeDNSRecordBasePayload
	Name string `json:"name"`
}

type linodeDNSMXRecordPayload struct {
	linodeDNSNamedRecordPayload
	Priority int `json:"priority"`
}

type linodeDNSSRVRecordPayload struct {
	linodeDNSRecordBasePayload
	Service  string `json:"service"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}

func linodeDNSRecordPayload(in cloudDNSRequest) any {
	base := linodeDNSRecordBasePayload{Type: strings.ToUpper(in.Type), Target: in.Target, TTL: in.TTL}
	switch strings.ToUpper(in.Type) {
	case "MX":
		return linodeDNSMXRecordPayload{linodeDNSNamedRecordPayload{base, in.Name}, in.Priority}
	case "SRV":
		return linodeDNSSRVRecordPayload{base, in.Service, in.Protocol, in.Port, in.Priority, in.Weight}
	default:
		return linodeDNSNamedRecordPayload{base, in.Name}
	}
}

func validLinodeSnapshotLabel(label string) bool {
	label = strings.TrimSpace(label)
	if len(label) == 0 || len(label) > 255 {
		return false
	}
	for _, r := range label {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func linodeAPIRequest(ctx context.Context, method, path string, payload any) (any, error) {
	token := os.Getenv("STEPANEL_LINODE_TOKEN")
	if token == "" {
		return nil, errors.New("STEPANEL_LINODE_TOKEN is not configured")
	}
	return linodeAPIRequestWithClient(ctx, method, path, payload, token, &http.Client{Timeout: 30 * time.Second})
}

func linodeResponseInt(value map[string]any, field string) (int, error) {
	raw, ok := value[field].(float64)
	if !ok || math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 || raw > math.MaxInt32 || math.Trunc(raw) != raw {
		return 0, fmt.Errorf("Linode API list response has an invalid %s", field)
	}
	return int(raw), nil
}

func linodeAPIRequestWithClient(ctx context.Context, method, path string, payload any, token string, client *http.Client) (any, error) {
	return linodeAPIRequestWithClientBudget(ctx, method, path, payload, token, client, maxCloudPaginationDuration)
}

func linodeAPIRequestWithClientBudget(ctx context.Context, method, path string, payload any, token string, client *http.Client, paginationBudget time.Duration) (any, error) {
	if token == "" {
		return nil, errors.New("STEPANEL_LINODE_TOKEN is not configured")
	}
	if client == nil {
		return nil, errors.New("Linode API client is required")
	}
	if paginationBudget <= 0 {
		return nil, errors.New("Linode API pagination budget must be positive")
	}
	started := time.Now()
	if method == http.MethodGet {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, paginationBudget)
		defer cancel()
	}
	var aggregateResponseBytes int64
	requestPage := func(page int) (map[string]any, error) {
		var reader io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			reader = bytes.NewReader(data)
		}
		baseURL := url.URL{Scheme: "https", Host: "api.linode.com", Path: "/v4" + path}
		if method == http.MethodGet && page > 1 {
			query := baseURL.Query()
			query.Set("page", strconv.Itoa(page))
			baseURL.RawQuery = query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL.String(), reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req) // lgtm[go/request-forgery]: URL uses hardcoded host and HTTPS.
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			return nil, fmt.Errorf("Linode API returned %s", res.Status)
		}
		if res.StatusCode == http.StatusNoContent {
			return nil, nil
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, maxCloudResponseBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxCloudResponseBytes {
			return nil, errors.New("Linode API response exceeds the 8 MiB limit")
		}
		aggregateResponseBytes += int64(len(data))
		if method == http.MethodGet && aggregateResponseBytes > maxCloudPaginationBytes {
			return nil, fmt.Errorf("Linode API pagination exceeded the %d MiB aggregate response limit", maxCloudPaginationBytes>>20)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		return value, nil
	}
	value, err := requestPage(1)
	if err != nil || method != http.MethodGet || value == nil {
		return value, err
	}
	if _, hasData := value["data"]; !hasData {
		if _, hasPageMetadata := value["page"]; !hasPageMetadata {
			return value, nil
		}
	}
	firstPage, err := linodeResponseInt(value, "page")
	if err != nil || firstPage != 1 {
		return nil, errors.New("Linode API list response has an invalid first page")
	}
	pageCount, err := linodeResponseInt(value, "pages")
	if err != nil || pageCount < 1 {
		return nil, errors.New("Linode API list response has an invalid page count")
	}
	if pageCount > maxCloudPaginationPages {
		return nil, fmt.Errorf("Linode API list response requires %d pages; refusing inventory above the %d-page safety limit", pageCount, maxCloudPaginationPages)
	}
	data, ok := value["data"].([]any)
	if !ok {
		return nil, errors.New("Linode API list response has invalid data")
	}
	totalResults, err := linodeResponseInt(value, "results")
	if err != nil || totalResults < len(data) {
		return nil, errors.New("Linode API list response has an invalid total result count")
	}
	if totalResults > maxCloudPaginationItems {
		return nil, fmt.Errorf("Linode API list response advertises %d records; refusing inventory above the %d-item safety limit", totalResults, maxCloudPaginationItems)
	}
	if pageCount == 1 && totalResults != len(data) {
		return nil, errors.New("Linode API single-page result count does not match its data")
	}
	for page := 2; page <= pageCount; page++ {
		if time.Since(started) >= paginationBudget {
			return nil, fmt.Errorf("Linode API pagination exceeded its %s time budget", paginationBudget)
		}
		next, err := requestPage(page)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, fmt.Errorf("Linode API returned an empty response for page %d of %d", page, int(pageCount))
		}
		reportedPage, err := linodeResponseInt(next, "page")
		if err != nil || reportedPage != page {
			return nil, fmt.Errorf("Linode API returned the wrong page for request %d", page)
		}
		nextPageCount, err := linodeResponseInt(next, "pages")
		if err != nil || nextPageCount != pageCount {
			return nil, fmt.Errorf("Linode API page %d reported an inconsistent page count", page)
		}
		items, ok := next["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("Linode API page %d has invalid data", page)
		}
		nextTotal, err := linodeResponseInt(next, "results")
		if err != nil || nextTotal != totalResults {
			return nil, fmt.Errorf("Linode API page %d reported an inconsistent total result count", page)
		}
		data = append(data, items...)
	}
	if totalResults != len(data) {
		return nil, fmt.Errorf("Linode API pagination returned %d records; response advertised %d", len(data), totalResults)
	}
	if pageCount == 1 {
		return value, nil
	}
	value["data"] = data
	value["results"] = float64(len(data))
	return value, nil
}
