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
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CloudInventory struct {
	Provider      string   `json:"provider"`
	Servers       any      `json:"servers,omitempty"`
	DNS           any      `json:"dns,omitempty"`
	LoadBalancers any      `json:"load_balancers,omitempty"`
	Snapshots     any      `json:"snapshots,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

type CloudActionResult struct {
	Provider    string    `json:"provider"`
	Action      string    `json:"action"`
	ID          string    `json:"id"`
	CompletedAt time.Time `json:"completed_at"`
}

type cloudDNSRequest struct {
	DomainID string `json:"domain_id"`
	RecordID string `json:"record_id,omitempty"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Target   string `json:"target,omitempty"`
	TTL      int    `json:"ttl,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Service  string `json:"service,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Port     int    `json:"port,omitempty"`
	Weight   int    `json:"weight,omitempty"`
}

type cloudLBRequest struct {
	NodeBalancerID string `json:"nodebalancer_id"`
	ConfigID       string `json:"config_id"`
	NodeID         string `json:"node_id,omitempty"`
	Address        string `json:"address,omitempty"`
	Label          string `json:"label,omitempty"`
	Port           int    `json:"port,omitempty"`
	Weight         int    `json:"weight,omitempty"`
	Action         string `json:"action"`
}

// AWSInstanceAction and AWSVolumeSnapshot keep AWS resource types explicit.
// An EC2 instance ID must never be accidentally used as an EBS volume ID (or
// vice versa) when constructing provider commands.
type AWSInstanceAction struct {
	InstanceID string `json:"instance_id"`
}

type AWSVolumeSnapshot struct {
	VolumeID string `json:"volume_id"`
}

type durableCloudRequest struct {
	Operation                  string             `json:"operation"`
	Provider                   string             `json:"provider"`
	Action                     string             `json:"action"`
	ID                         string             `json:"id"`
	AWSInstance                *AWSInstanceAction `json:"aws_instance,omitempty"`
	AWSVolume                  *AWSVolumeSnapshot `json:"aws_volume,omitempty"`
	Service                    string             `json:"service,omitempty"`
	DNS                        cloudDNSRequest    `json:"dns,omitempty"`
	LB                         cloudLBRequest     `json:"load_balancer,omitempty"`
	SnapshotLabel              string             `json:"snapshot_label,omitempty"`
	ConfirmSnapshotReplacement bool               `json:"confirm_snapshot_replacement,omitempty"`
	Actor                      string             `json:"actor"`
}

func (a *App) enqueueCloudJob(request durableCloudRequest, suppliedKey string) (Job, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return Job{}, err
	}
	owner := request.ID
	if owner == "" {
		owner = request.DNS.DomainID
	}
	if owner == "" {
		owner = request.LB.NodeBalancerID
	}
	operationKey := cloudOperationKey(request, suppliedKey, time.Now().UTC())
	job, _, err := a.Jobs.EnqueueIdempotent("cloud.action", owner, operationKey, payload, 3)
	return job, err
}

// cloudOperationKey scopes an optional request idempotency key to one action
// and a UTC day. Retried submissions converge for at most 24 hours, while a
// later deliberate action can never be permanently hidden by old job history.
func cloudOperationKey(request durableCloudRequest, suppliedKey string, now time.Time) string {
	if suppliedKey == "" {
		return ""
	}
	return request.Operation + ":" + request.Action + ":" + now.UTC().Format("20060102") + ":" + suppliedKey
}

func (a *App) handleCloudJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableCloudRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode cloud job payload: %w", err)
	}
	if request.Actor == "" {
		return nil, errors.New("cloud job actor is required")
	}
	// Convert pre-typed AWS jobs written by older panel versions at the
	// deserialization boundary. All provider execution below still uses the
	// explicit typed request structs.
	if request.Operation == "instance" && request.Provider == "aws" && request.AWSInstance == nil && request.AWSVolume == nil {
		if request.Action == "snapshot" {
			request.AWSVolume = &AWSVolumeSnapshot{VolumeID: request.ID}
		} else {
			request.AWSInstance = &AWSInstanceAction{InstanceID: request.ID}
		}
	}
	if ctx.Err() != nil || a.Jobs.CancellationRequested(item.ID) {
		return nil, context.Canceled
	}
	var result CloudActionResult
	var err error
	switch request.Operation {
	case "instance":
		workerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if request.Provider == "aws" {
			switch {
			case request.AWSInstance != nil:
				err = executeAWSInstanceAction(workerCtx, request.Action, *request.AWSInstance)
			case request.AWSVolume != nil:
				err = executeAWSVolumeSnapshot(workerCtx, *request.AWSVolume)
			default:
				err = errors.New("AWS cloud job is missing a typed resource request")
			}
		} else {
			err = executeCloudAction(workerCtx, request.Provider, request.Action, request.ID, request.SnapshotLabel, request.ConfirmSnapshotReplacement)
		}
		result = CloudActionResult{Provider: request.Provider, Action: request.Action, ID: request.ID, CompletedAt: time.Now().UTC()}
	case "ssh":
		workerCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		err = executeSSHAction(workerCtx, request.ID, request.Action, request.Service)
		result = CloudActionResult{Provider: "ssh", Action: request.Action, ID: request.ID, CompletedAt: time.Now().UTC()}
	case "dns":
		if request.Action != "create" && request.Action != "update" && request.Action != "delete" {
			return nil, fmt.Errorf("unsupported DNS action %q", request.Action)
		}
		if !cloudNumericID.MatchString(request.DNS.DomainID) || (request.Action != "create" && !cloudNumericID.MatchString(request.DNS.RecordID)) {
			return nil, fmt.Errorf("invalid domain or record ID format")
		}
		if request.Action != "delete" && !cloudDNSRecordValid(request.DNS) {
			return nil, fmt.Errorf("invalid DNS record")
		}
		request.DNS.Type = strings.ToUpper(request.DNS.Type)
		var path, method string
		var body any
		alreadyPresent := false
		if request.Action == "delete" {
			// lgtm[go/request-forgery]: DomainID and RecordID are validated against cloudNumericID regex above
			path = "/domains/" + request.DNS.DomainID + "/records/" + request.DNS.RecordID
			method = http.MethodDelete
		} else {
			if request.Action == "create" {
				// lgtm[go/request-forgery]: DomainID is validated against cloudNumericID regex above
				if existing, lookupErr := linodeAPIRequest(ctx, http.MethodGet, "/domains/"+request.DNS.DomainID+"/records", nil); lookupErr == nil && dnsRecordExists(existing, request.DNS) {
					alreadyPresent = true
				}
			}
			if !alreadyPresent {
				// lgtm[go/request-forgery]: DomainID is validated against cloudNumericID regex above
				path = "/domains/" + request.DNS.DomainID + "/records"
				method = http.MethodPost
				if request.Action == "update" {
					// lgtm[go/request-forgery]: RecordID is validated against cloudNumericID regex above
					path += "/" + request.DNS.RecordID
					method = http.MethodPut
				}
				body = linodeDNSRecordPayload(request.DNS)
			}
		}
		if !alreadyPresent {
			_, err = linodeAPIRequest(ctx, method, path, body)
		}
		result = CloudActionResult{Provider: "linode", Action: "dns." + request.Action, ID: request.DNS.DomainID, CompletedAt: time.Now().UTC()}
	case "loadbalancer":
		if request.LB.Action != "add" && request.LB.Action != "remove" {
			return nil, fmt.Errorf("unsupported load balancer action %q", request.LB.Action)
		}
		if !cloudNumericID.MatchString(request.LB.NodeBalancerID) || !cloudNumericID.MatchString(request.LB.ConfigID) || (request.LB.Action == "remove" && !cloudNumericID.MatchString(request.LB.NodeID)) {
			return nil, fmt.Errorf("invalid nodebalancer, config, or node ID format")
		}
		if request.LB.Action == "add" && !validLinodeNodeRequest(request.LB) {
			return nil, fmt.Errorf("invalid backend address, port, or weight")
		}
		var path, method string
		var body any
		if request.LB.Action == "remove" {
			// lgtm[go/request-forgery]: All IDs are validated against cloudNumericID regex above
			path = "/nodebalancers/" + request.LB.NodeBalancerID + "/configs/" + request.LB.ConfigID + "/nodes/" + request.LB.NodeID
			method = http.MethodDelete
		} else {
			// lgtm[go/request-forgery]: All IDs are validated against cloudNumericID regex above
			path = "/nodebalancers/" + request.LB.NodeBalancerID + "/configs/" + request.LB.ConfigID + "/nodes"
			method = http.MethodPost
			body, err = linodeNodePayload(request.LB)
			if err != nil {
				return nil, err
			}
		}
		_, err = linodeAPIRequest(ctx, method, path, body)
		result = CloudActionResult{Provider: "linode", Action: "loadbalancer." + request.LB.Action, ID: request.LB.NodeBalancerID, CompletedAt: time.Now().UTC()}
	default:
		return nil, fmt.Errorf("unsupported cloud job operation %q", request.Operation)
	}
	if err != nil {
		if request.Operation == "dns" && a.DNSDesired != nil {
			if desiredErr := a.DNSDesired.markResult(request.DNS, request.Action, err); desiredErr != nil {
				return nil, fmt.Errorf("%w; persist DNS failure state: %v", err, desiredErr)
			}
		}
		auditPrefix := "cloud."
		if request.Operation == "ssh" {
			auditPrefix = "ssh."
		}
		auditAction := request.Action
		if request.Operation == "dns" {
			auditAction = "dns." + request.Action
		} else if request.Operation == "loadbalancer" {
			auditAction = "loadbalancer." + request.Action
		}
		TelemetryAudit(a.Config.AuditLog, request.Actor, auditPrefix+auditAction+".failed", result.ID, err.Error())
		return nil, err
	}
	if request.Operation == "dns" && a.DNSDesired != nil {
		if desiredErr := a.DNSDesired.markResult(request.DNS, request.Action, nil); desiredErr != nil {
			return nil, fmt.Errorf("persist DNS applied state: %w", desiredErr)
		}
	}
	auditPrefix := "cloud."
	if request.Operation == "ssh" {
		auditPrefix = "ssh."
	}
	auditAction := request.Action
	if request.Operation == "dns" {
		auditAction = "dns." + request.Action
	} else if request.Operation == "loadbalancer" {
		auditAction = "loadbalancer." + request.Action
	}
	TelemetryAudit(a.Config.AuditLog, request.Actor, auditPrefix+auditAction, result.ID, result.Provider)
	output, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (a *App) cloudInventory(w http.ResponseWriter, _ *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(a.Config.CloudProvider))
	if provider == "" {
		writeJSON(w, http.StatusOK, CloudInventory{Warnings: []string{"no cloud provider configured"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var inv CloudInventory
	var err error
	switch provider {
	case "linode":
		inv, err = linodeInventory(ctx)
	case "aws":
		inv, err = cliCloudInventory(ctx, "aws", "AWS")
	case "openstack":
		inv, err = cliCloudInventory(ctx, "openstack", "OpenStack")
	default:
		err = errors.New("unsupported cloud provider")
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"provider": provider, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (a *App) cloudAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var in struct {
		Provider                   string `json:"provider"`
		Action                     string `json:"action"`
		ID                         string `json:"id"`
		InstanceID                 string `json:"instance_id"`
		VolumeID                   string `json:"volume_id"`
		SnapshotLabel              string `json:"snapshot_label"`
		ConfirmSnapshotReplacement bool   `json:"confirm_snapshot_replacement"`
	}
	if err := decodeJSON(w, r, 4096, &in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if in.Provider == "" {
		in.Provider = a.Config.CloudProvider
	}
	if in.Provider == "" || (a.Config.CloudProvider != "" && in.Provider != a.Config.CloudProvider) {
		http.Error(w, "provider is not configured for this installation", http.StatusUnprocessableEntity)
		return
	}
	if in.Action != "start" && in.Action != "stop" && in.Action != "reboot" && in.Action != "snapshot" {
		http.Error(w, "invalid provider, action, or resource ID", http.StatusUnprocessableEntity)
		return
	}
	request := durableCloudRequest{Operation: "instance", Provider: in.Provider, Action: in.Action, Actor: a.Auth.UsernameForRequest(r)}
	if in.Provider == "aws" {
		if in.SnapshotLabel != "" || in.ConfirmSnapshotReplacement {
			http.Error(w, "snapshot_label is only supported for Linode snapshots", http.StatusUnprocessableEntity)
			return
		}
		if in.Action == "snapshot" {
			if !cloudIDPattern.MatchString(in.VolumeID) || in.ID != "" || in.InstanceID != "" {
				http.Error(w, "AWS snapshots require a valid volume_id", http.StatusUnprocessableEntity)
				return
			}
			request.ID = in.VolumeID
			request.AWSVolume = &AWSVolumeSnapshot{VolumeID: in.VolumeID}
		} else {
			if !cloudIDPattern.MatchString(in.InstanceID) || in.ID != "" || in.VolumeID != "" {
				http.Error(w, "AWS instance actions require a valid instance_id", http.StatusUnprocessableEntity)
				return
			}
			request.ID = in.InstanceID
			request.AWSInstance = &AWSInstanceAction{InstanceID: in.InstanceID}
		}
	} else {
		if !cloudIDPattern.MatchString(in.ID) || in.InstanceID != "" || in.VolumeID != "" {
			http.Error(w, "invalid provider, action, or resource ID", http.StatusUnprocessableEntity)
			return
		}
		request.ID = in.ID
		if in.Provider == "linode" {
			if in.Action == "snapshot" && (!validLinodeSnapshotLabel(in.SnapshotLabel) || !in.ConfirmSnapshotReplacement) {
				http.Error(w, "Linode snapshot actions require a valid snapshot_label and confirm_snapshot_replacement=true because the provider may replace the current manual snapshot", http.StatusUnprocessableEntity)
				return
			}
			if in.Action != "snapshot" && (in.SnapshotLabel != "" || in.ConfirmSnapshotReplacement) {
				http.Error(w, "snapshot replacement confirmation only applies to Linode snapshot actions", http.StatusUnprocessableEntity)
				return
			}
			request.SnapshotLabel = strings.TrimSpace(in.SnapshotLabel)
			request.ConfirmSnapshotReplacement = in.ConfirmSnapshotReplacement
		} else if in.SnapshotLabel != "" || in.ConfirmSnapshotReplacement {
			http.Error(w, "snapshot_label is only supported for Linode snapshots", http.StatusUnprocessableEntity)
			return
		}
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	job, err := a.enqueueCloudJob(request, operationKey)
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "could not persist cloud job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

func (a *App) cloudDNS(w http.ResponseWriter, r *http.Request) {
	if a.Config.CloudProvider != "linode" {
		http.Error(w, "Linode DNS integration requires STEPANEL_CLOUD_PROVIDER=linode", 422)
		return
	}
	if r.Method == http.MethodGet {
		domain := r.URL.Query().Get("domain_id")
		if !cloudNumericID.MatchString(domain) {
			http.Error(w, "invalid domain_id", 422)
			return
		}
		value, err := linodeAPIRequest(r.Context(), http.MethodGet, "/domains/"+domain+"/records", nil)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		desired, err := a.dnsDesiredView(domain)
		if err != nil {
			http.Error(w, "could not read DNS desired state", http.StatusServiceUnavailable)
			return
		}
		if response, ok := value.(map[string]any); ok {
			response["desired"] = desired
			writeJSON(w, 200, response)
		} else {
			writeJSON(w, 200, map[string]any{"provider": value, "desired": desired})
		}
		return
	}
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var in cloudDNSRequest
	if err := decodeJSON(w, r, 8192, &in); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if !cloudNumericID.MatchString(in.DomainID) {
		http.Error(w, "invalid domain_id", 422)
		return
	}
	if r.Method == http.MethodDelete {
		if !cloudNumericID.MatchString(in.RecordID) {
			http.Error(w, "invalid record_id", 422)
			return
		}
		a.queueDNSJob(w, r, in, "delete")
		return
	}
	if r.Method != http.MethodPost || !cloudDNSRecordValid(in) {
		http.Error(w, "invalid DNS record", 422)
		return
	}
	in.Type = strings.ToUpper(in.Type)
	action := "create"
	if in.RecordID != "" {
		action = "update"
	}
	a.queueDNSJob(w, r, in, action)
}

func (a *App) cloudLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if a.Config.CloudProvider != "linode" {
		http.Error(w, "Linode load balancer integration requires STEPANEL_CLOUD_PROVIDER=linode", 422)
		return
	}
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var in cloudLBRequest
	if err := decodeJSON(w, r, 8192, &in); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if !cloudNumericID.MatchString(in.NodeBalancerID) || !cloudNumericID.MatchString(in.ConfigID) || (in.Action != "add" && in.Action != "remove") {
		http.Error(w, "invalid load balancer action or ID", 422)
		return
	}
	if in.Action == "remove" {
		if !cloudNumericID.MatchString(in.NodeID) {
			http.Error(w, "invalid node_id", 422)
			return
		}
	} else if !validLinodeNodeRequest(in) {
		http.Error(w, "invalid backend address, port, or weight", 422)
		return
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "loadbalancer", Provider: "linode", Action: in.Action, ID: in.NodeBalancerID, LB: in, Actor: a.Auth.UsernameForRequest(r)}, operationKey)
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "could not persist load balancer job", 500)
		return
	}
	writeJSON(w, 202, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

func (a *App) cloudSnapshots(w http.ResponseWriter, r *http.Request) {
	if a.Config.CloudProvider != "linode" {
		http.Error(w, "Linode snapshot integration requires STEPANEL_CLOUD_PROVIDER=linode", 422)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	linodeID := r.URL.Query().Get("linode_id")
	if !cloudNumericID.MatchString(linodeID) {
		http.Error(w, "invalid linode_id", 422)
		return
	}
	path, err := linodeBackupsPath(linodeID)
	if err != nil {
		http.Error(w, "invalid linode_id", 422)
		return
	}
	value, err := linodeAPIRequest(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	writeJSON(w, 200, value)
}

var cloudNumericID = regexp.MustCompile(`^[0-9]{1,12}$`)
var dnsTypePattern = regexp.MustCompile(`^(A|AAAA|CNAME|MX|TXT|NS|SRV)$`)
var dnsNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.*@-]{1,253}$`)
var linodeNodeLabelPattern = regexp.MustCompile(`^[A-Za-z0-9-_.]{3,32}$`)

func validLinodeNodeRequest(request cloudLBRequest) bool {
	return net.ParseIP(request.Address) != nil && request.Port >= 1 && request.Port <= 65535 && request.Weight >= 1 && request.Weight <= 255 && linodeNodeLabelPattern.MatchString(request.Label)
}

func linodeNodePayload(request cloudLBRequest) (map[string]any, error) {
	if !validLinodeNodeRequest(request) {
		return nil, errors.New("invalid NodeBalancer node address, port, weight, or label")
	}
	ip := net.ParseIP(request.Address)
	return map[string]any{
		"address": net.JoinHostPort(ip.String(), strconv.Itoa(request.Port)),
		"label":   request.Label,
		"weight":  request.Weight,
	}, nil
}

func cloudDNSRecordValid(in cloudDNSRequest) bool {
	typ := strings.ToUpper(in.Type)
	if !dnsTypePattern.MatchString(typ) || !validLinodeDNSName(typ, in.Name) || len(in.Target) == 0 || len(in.Target) > 512 || !validLinodeTTL(in.TTL) {
		return false
	}
	switch typ {
	case "A":
		return net.ParseIP(in.Target) != nil && strings.Count(in.Target, ".") == 3
	case "AAAA":
		return net.ParseIP(in.Target) != nil
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

func linodeDNSRecordPayload(in cloudDNSRequest) map[string]any {
	payload := map[string]any{"type": strings.ToUpper(in.Type), "target": in.Target, "ttl_sec": in.TTL}
	if !strings.EqualFold(in.Type, "SRV") {
		payload["name"] = in.Name
	}
	switch strings.ToUpper(in.Type) {
	case "MX":
		payload["priority"] = in.Priority
	case "SRV":
		payload["service"] = in.Service
		payload["protocol"] = in.Protocol
		payload["port"] = in.Port
		payload["priority"] = in.Priority
		payload["weight"] = in.Weight
	}
	return payload
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
func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// queueDNSJob enqueues the provider change as one durable job. The job is
// the only write: while it is queued or running, dnsDesiredView reports the
// record as pending, and the job records applied or failed when it ends, so
// desired state can never claim a pending change that has no job behind it.
func (a *App) queueDNSJob(w http.ResponseWriter, r *http.Request, in cloudDNSRequest, action string) error {
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return err
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: action, ID: in.DomainID, DNS: in, Actor: a.Auth.UsernameForRequest(r)}, operationKey)
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return err
		}
		http.Error(w, "could not persist DNS job", http.StatusServiceUnavailable)
		return err
	}
	writeJSON(w, 202, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
	return nil
}

// dnsDesiredView returns stored DNS outcomes overlaid with every queued or
// running DNS job for the domain as a pending record.
func (a *App) dnsDesiredView(domainID string) ([]DNSDesiredRecord, error) {
	if a.DNSDesired == nil {
		return nil, nil
	}
	records := map[string]DNSDesiredRecord{}
	for _, record := range a.DNSDesired.list(domainID) {
		records[record.Key] = record
	}
	if a.Jobs != nil {
		payloads, err := a.Jobs.ActivePayloads("cloud.action")
		if err != nil {
			return nil, err
		}
		for _, payload := range payloads {
			var request durableCloudRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				return nil, fmt.Errorf("decode active cloud job payload: %w", err)
			}
			if request.Operation != "dns" || request.DNS.DomainID != domainID {
				continue
			}
			records[dnsDesiredKey(request.DNS)] = newPendingDNSRecord(request.DNS, request.Action, request.Actor)
		}
	}
	items := make([]DNSDesiredRecord, 0, len(records))
	for _, record := range records {
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func dnsRecordExists(value any, in cloudDNSRequest) bool {
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	data, ok := root["data"].([]any)
	if !ok {
		return false
	}
	for _, item := range data {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := record["type"].(string)
		name, _ := record["name"].(string)
		target, _ := record["target"].(string)
		ttl, _ := record["ttl_sec"].(float64)
		priority, _ := record["priority"].(float64)
		port, _ := record["port"].(float64)
		weight, _ := record["weight"].(float64)
		service, _ := record["service"].(string)
		protocol, _ := record["protocol"].(string)
		if strings.EqualFold(typ, in.Type) && strings.EqualFold(strings.TrimSuffix(name, "."), strings.TrimSuffix(in.Name, ".")) && strings.EqualFold(strings.TrimSuffix(target, "."), strings.TrimSuffix(in.Target, ".")) && int(ttl) == in.TTL && int(priority) == in.Priority && int(port) == in.Port && int(weight) == in.Weight && strings.EqualFold(service, in.Service) && strings.EqualFold(protocol, in.Protocol) {
			return true
		}
	}
	return false
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
	if token == "" {
		return nil, errors.New("STEPANEL_LINODE_TOKEN is not configured")
	}
	if client == nil {
		return nil, errors.New("Linode API client is required")
	}
	const maxCloudResponseBytes = 8 << 20
	requestPage := func(page int) (map[string]any, error) {
		var reader io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			reader = bytes.NewReader(data)
		}
		// Build URL safely using url.URL to prevent SSRF attacks.
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
		res, err := client.Do(req) // lgtm[go/request-forgery]: URL uses hardcoded host (api.linode.com) and HTTPS scheme
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
	if pageCount > 1000 {
		return nil, fmt.Errorf("Linode API list response requires %d pages; refusing incomplete inventory above the 1000-page safety limit", pageCount)
	}
	data, ok := value["data"].([]any)
	if !ok {
		return nil, errors.New("Linode API list response has invalid data")
	}
	totalResults, err := linodeResponseInt(value, "results")
	if err != nil || totalResults < len(data) {
		return nil, errors.New("Linode API list response has an invalid total result count")
	}
	if pageCount == 1 && totalResults != len(data) {
		return nil, errors.New("Linode API single-page result count does not match its data")
	}
	for page := 2; page <= pageCount; page++ {
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

var cloudIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func awsInstanceActionArgs(action string, request AWSInstanceAction) ([]string, error) {
	if !cloudIDPattern.MatchString(request.InstanceID) {
		return nil, fmt.Errorf("invalid AWS instance ID format: %s", request.InstanceID)
	}
	command := map[string]string{"start": "start-instances", "stop": "stop-instances", "reboot": "reboot-instances"}[action]
	if command == "" {
		return nil, fmt.Errorf("unsupported AWS instance action: %s", action)
	}
	return []string{"ec2", command, "--instance-ids", request.InstanceID, "--output", "json"}, nil
}

func awsVolumeSnapshotArgs(request AWSVolumeSnapshot) ([]string, error) {
	if !cloudIDPattern.MatchString(request.VolumeID) {
		return nil, fmt.Errorf("invalid AWS volume ID format: %s", request.VolumeID)
	}
	return []string{"ec2", "create-snapshot", "--volume-id", request.VolumeID, "--output", "json"}, nil
}

func executeAWSInstanceAction(ctx context.Context, action string, request AWSInstanceAction) error {
	args, err := awsInstanceActionArgs(action, request)
	if err != nil {
		return err
	}
	return runCloudCLI(ctx, "aws", "AWS", args...)
}

func executeAWSVolumeSnapshot(ctx context.Context, request AWSVolumeSnapshot) error {
	args, err := awsVolumeSnapshotArgs(request)
	if err != nil {
		return err
	}
	return runCloudCLI(ctx, "aws", "AWS", args...)
}

func linodeBackupsPath(linodeID string) (string, error) {
	if !cloudNumericID.MatchString(linodeID) {
		return "", errors.New("invalid Linode instance ID")
	}
	return "/linode/instances/" + linodeID + "/backups", nil
}

func linodeInstanceActionRequest(action, linodeID, snapshotLabel string, confirmSnapshotReplacement bool) (string, string, any, error) {
	if !cloudNumericID.MatchString(linodeID) {
		return "", "", nil, errors.New("invalid Linode instance ID")
	}
	path := "/linode/instances/" + linodeID
	switch action {
	case "start":
		return http.MethodPost, path + "/boot", nil, nil
	case "stop":
		return http.MethodPost, path + "/shutdown", nil, nil
	case "reboot":
		return http.MethodPost, path + "/reboot", nil, nil
	case "snapshot":
		if !validLinodeSnapshotLabel(snapshotLabel) {
			return "", "", nil, errors.New("a valid snapshot label is required")
		}
		if !confirmSnapshotReplacement {
			return "", "", nil, errors.New("snapshot replacement must be explicitly confirmed")
		}
		return http.MethodPost, path + "/backups", map[string]string{"label": strings.TrimSpace(snapshotLabel)}, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported Linode instance action %q", action)
	}
}

func executeCloudAction(ctx context.Context, provider, action, id, snapshotLabel string, confirmSnapshotReplacement bool) error {
	switch provider {
	case "linode":
		method, path, payload, err := linodeInstanceActionRequest(action, id, snapshotLabel, confirmSnapshotReplacement)
		if err != nil {
			return err
		}
		_, err = linodeAPIRequest(ctx, method, path, payload)
		return err
	case "aws":
		if action == "snapshot" {
			return executeAWSVolumeSnapshot(ctx, AWSVolumeSnapshot{VolumeID: id})
		}
		return executeAWSInstanceAction(ctx, action, AWSInstanceAction{InstanceID: id})
	case "openstack":
		if !cloudIDPattern.MatchString(id) {
			return fmt.Errorf("invalid instance ID format: %s", id)
		}
		args := []string{"server"}
		switch action {
		case "start":
			args = append(args, "start")
		case "stop":
			args = append(args, "stop")
		case "reboot":
			args = append(args, "reboot", "--hard")
		case "snapshot":
			args = append(args, "backup", "create", "--name", "stepanel-"+id)
		default:
			return fmt.Errorf("unsupported OpenStack instance action %q", action)
		}
		args = append(args, id, "-f", "json")
		return runCloudCLI(ctx, "openstack", "OpenStack", args...)
	default:
		return errors.New("unsupported cloud provider")
	}
}

func runCloudCLI(ctx context.Context, command, provider string, args ...string) error {
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("%s CLI is not installed", provider)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = cloudCommandEnv()
	out, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		return fmt.Errorf("%s action failed: %w: %s", provider, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Cloud CLIs need provider credentials and region settings, but should not
// inherit unrelated panel secrets (database passwords, session keys, etc.).
func cloudCommandEnv() []string {
	blocked := func(key string) bool {
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "STEPANEL_") {
			return true
		}
		for _, fragment := range []string{"DATABASE_PASSWORD", "DB_PASSWORD", "SESSION_SECRET", "ADMIN_PASSWORD", "TOTP_SECRET", "AUDIT_KEY", "PRIVATE_KEY"} {
			if strings.Contains(upper, fragment) {
				return true
			}
		}
		return false
	}
	env := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && !blocked(key) {
			env = append(env, item)
		}
	}
	return env
}

func linodeInventory(ctx context.Context) (CloudInventory, error) {
	get := func(path string) (any, error) {
		return linodeAPIRequest(ctx, http.MethodGet, path, nil)
	}
	paths := []struct{ name, path string }{{"servers", "/linode/instances"}, {"dns", "/domains"}, {"load_balancers", "/nodebalancers"}}
	inv := CloudInventory{Provider: "linode"}
	for _, item := range paths {
		value, err := get(item.path)
		if err != nil {
			inv.Warnings = append(inv.Warnings, item.name+": "+err.Error())
			continue
		}
		switch item.name {
		case "servers":
			inv.Servers = value
		case "dns":
			inv.DNS = value
		case "load_balancers":
			inv.LoadBalancers = value
		case "snapshots":
			inv.Snapshots = value
		}
	}
	inv.Warnings = append(inv.Warnings, "snapshots: Linode backups are scoped to an instance; use GET /api/cloud/snapshots?linode_id=<id>")
	return inv, nil
}

func cliCloudInventory(ctx context.Context, command, provider string) (CloudInventory, error) {
	if _, err := exec.LookPath(command); err != nil {
		return CloudInventory{}, fmt.Errorf("%s CLI is not installed", provider)
	}
	run := func(args ...string) (any, error) {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Env = cloudCommandEnv()
		out, err := runBoundedCommandLimit(ctx, cmd, 8<<20)
		if err != nil {
			return nil, fmt.Errorf("%s command failed: %w", provider, err)
		}
		var value any
		if err := json.Unmarshal(out, &value); err != nil {
			return nil, fmt.Errorf("%s returned invalid JSON: %w", provider, err)
		}
		return value, nil
	}
	inv := CloudInventory{Provider: strings.ToLower(provider)}
	var failures int
	assign := func(name string, target *any, args ...string) {
		value, err := run(args...)
		if err != nil {
			inv.Warnings = append(inv.Warnings, name+": "+err.Error())
			failures++
			return
		}
		*target = value
	}
	if provider == "AWS" {
		assign("servers", &inv.Servers, "ec2", "describe-instances", "--output", "json")
		assign("dns", &inv.DNS, "route53", "list-hosted-zones", "--output", "json")
		assign("load_balancers", &inv.LoadBalancers, "elbv2", "describe-load-balancers", "--output", "json")
		assign("snapshots", &inv.Snapshots, "ec2", "describe-snapshots", "--owner-ids", "self", "--output", "json")
	} else {
		assign("servers", &inv.Servers, "server", "list", "-f", "json")
		assign("dns", &inv.DNS, "recordset", "list", "-f", "json")
		assign("load_balancers", &inv.LoadBalancers, "loadbalancer", "list", "-f", "json")
		assign("snapshots", &inv.Snapshots, "server", "backup", "list", "-f", "json")
	}
	if failures == 4 {
		return inv, errors.New("all cloud inventory queries failed")
	}
	return inv, nil
}
