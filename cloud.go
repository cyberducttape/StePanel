package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
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

type durableCloudRequest struct {
	Operation string          `json:"operation"`
	Provider  string          `json:"provider"`
	Action    string          `json:"action"`
	ID        string          `json:"id"`
	Service   string          `json:"service,omitempty"`
	DNS       cloudDNSRequest `json:"dns,omitempty"`
	LB        cloudLBRequest  `json:"load_balancer,omitempty"`
	Actor     string          `json:"actor"`
}

func (a *App) enqueueCloudJob(request durableCloudRequest) (Job, error) {
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
	// Exclude the actor from the idempotency identity: a retried operator
	// request must converge on the same provider mutation regardless of which
	// authenticated process re-submits it.
	identity := request
	identity.Actor = ""
	identityPayload, err := json.Marshal(identity)
	if err != nil {
		return Job{}, err
	}
	hash := sha256.Sum256(identityPayload)
	operationKey := request.Operation + ":" + request.Action + ":" + owner + ":" + hex.EncodeToString(hash[:8])
	job, _, err := a.Jobs.EnqueueIdempotent("cloud.action", owner, operationKey, payload, 3)
	return job, err
}

func (a *App) handleCloudJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableCloudRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode cloud job payload: %w", err)
	}
	if request.Actor == "" {
		return nil, errors.New("cloud job actor is required")
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
		err = executeCloudAction(workerCtx, request.Provider, request.Action, request.ID)
		result = CloudActionResult{Provider: request.Provider, Action: request.Action, ID: request.ID, CompletedAt: time.Now().UTC()}
	case "ssh":
		workerCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		err = executeSSHAction(workerCtx, request.ID, request.Action, request.Service)
		result = CloudActionResult{Provider: "ssh", Action: request.Action, ID: request.ID, CompletedAt: time.Now().UTC()}
	case "dns":
		// Validate DNS IDs to prevent SSRF attacks
		if !cloudNumericID.MatchString(request.DNS.DomainID) || !cloudNumericID.MatchString(request.DNS.RecordID) {
			return nil, fmt.Errorf("invalid domain or record ID format")
		}
		request.DNS.Type = strings.ToUpper(request.DNS.Type)
		var path, method string
		var body any
		alreadyPresent := false
		if request.Action == "delete" {
			path = "/domains/" + request.DNS.DomainID + "/records/" + request.DNS.RecordID
			method = http.MethodDelete
		} else {
			if request.Action == "create" {
				if existing, lookupErr := linodeAPIRequest(ctx, http.MethodGet, "/domains/"+request.DNS.DomainID+"/records", nil); lookupErr == nil && dnsRecordExists(existing, request.DNS) {
					alreadyPresent = true
				}
			}
			if !alreadyPresent {
				path = "/domains/" + request.DNS.DomainID + "/records"
				method = http.MethodPost
				if request.Action == "update" {
					path += "/" + request.DNS.RecordID
					method = http.MethodPut
				}
				body = map[string]any{"type": request.DNS.Type, "name": request.DNS.Name, "target": request.DNS.Target, "ttl_sec": request.DNS.TTL}
			}
		}
		if !alreadyPresent {
			_, err = linodeAPIRequest(ctx, method, path, body)
		}
		result = CloudActionResult{Provider: "linode", Action: "dns." + request.Action, ID: request.DNS.DomainID, CompletedAt: time.Now().UTC()}
	case "loadbalancer":
		// Validate load balancer IDs to prevent SSRF attacks
		if !cloudNumericID.MatchString(request.LB.NodeBalancerID) || !cloudNumericID.MatchString(request.LB.ConfigID) || !cloudNumericID.MatchString(request.LB.NodeID) {
			return nil, fmt.Errorf("invalid nodebalancer, config, or node ID format")
		}
		var path, method string
		var body any
		if request.LB.Action == "remove" {
			path = "/nodebalancers/" + request.LB.NodeBalancerID + "/configs/" + request.LB.ConfigID + "/nodes/" + request.LB.NodeID
			method = http.MethodDelete
		} else {
			path = "/nodebalancers/" + request.LB.NodeBalancerID + "/configs/" + request.LB.ConfigID + "/nodes"
			method = http.MethodPost
			body = map[string]any{"address": request.LB.Address, "label": request.LB.Label, "port": request.LB.Port, "weight": request.LB.Weight}
		}
		_, err = linodeAPIRequest(ctx, method, path, body)
		result = CloudActionResult{Provider: "linode", Action: "loadbalancer." + request.LB.Action, ID: request.LB.NodeBalancerID, CompletedAt: time.Now().UTC()}
	case "snapshot.delete":
		// Validate snapshot ID to prevent SSRF attacks
		if !cloudNumericID.MatchString(request.ID) {
			return nil, fmt.Errorf("invalid snapshot ID format")
		}
		_, err = linodeAPIRequest(ctx, http.MethodDelete, "/account/linode/backups/"+request.ID, nil)
		result = CloudActionResult{Provider: "linode", Action: "snapshot.delete", ID: request.ID, CompletedAt: time.Now().UTC()}
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
		_ = ShouldAudit(a.Config.AuditLog, request.Actor, auditPrefix+auditAction+".failed", result.ID, err.Error())
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
	if auditErr := AuditAs(a.Config.AuditLog, request.Actor, auditPrefix+auditAction, result.ID, result.Provider); auditErr != nil {
		log.Printf("cloud action completed but audit persistence is unavailable: %v", auditErr)
	}
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
	var in struct{ Provider, Action, ID string }
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
	if !cloudIDPattern.MatchString(in.ID) || (in.Action != "start" && in.Action != "stop" && in.Action != "reboot" && in.Action != "snapshot") {
		http.Error(w, "invalid provider, action, or resource ID", http.StatusUnprocessableEntity)
		return
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "instance", Provider: in.Provider, Action: in.Action, ID: in.ID, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
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
		desired := []DNSDesiredRecord(nil)
		if a.DNSDesired != nil {
			desired = a.DNSDesired.list(domain)
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
	} else if net.ParseIP(in.Address) == nil || in.Port < 1 || in.Port > 65535 || in.Weight < 1 || in.Weight > 100 {
		http.Error(w, "invalid backend address, port, or weight", 422)
		return
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "loadbalancer", Provider: "linode", Action: in.Action, ID: in.NodeBalancerID, LB: in, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
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
	if r.Method == http.MethodGet {
		value, err := linodeAPIRequest(r.Context(), http.MethodGet, "/account/linode/backups", nil)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		writeJSON(w, 200, value)
		return
	}
	if r.Method != http.MethodDelete || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	id := r.URL.Query().Get("id")
	if !cloudNumericID.MatchString(id) {
		http.Error(w, "invalid snapshot id", 422)
		return
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "snapshot.delete", Provider: "linode", Action: "snapshot.delete", ID: id, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		http.Error(w, "could not persist snapshot job", 500)
		return
	}
	writeJSON(w, 202, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

var cloudNumericID = regexp.MustCompile(`^[0-9]{1,12}$`)
var dnsTypePattern = regexp.MustCompile(`^(A|AAAA|CNAME|MX|TXT|NS|SRV)$`)
var dnsNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.*@-]{1,253}$`)

func cloudDNSRecordValid(in cloudDNSRequest) bool {
	typ := strings.ToUpper(in.Type)
	if !dnsTypePattern.MatchString(typ) || !dnsNamePattern.MatchString(in.Name) || len(in.Target) == 0 || len(in.Target) > 512 || in.TTL < 30 || in.TTL > 604800 {
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
		parts := strings.Fields(in.Target)
		if len(parts) != 2 {
			return false
		}
		priority, err := strconv.Atoi(parts[0])
		return err == nil && priority >= 0 && priority <= 65535 && dnsNamePattern.MatchString(strings.TrimSuffix(parts[1], "."))
	case "SRV":
		parts := strings.Fields(in.Target)
		if len(parts) != 4 {
			return false
		}
		priority, priorityErr := strconv.Atoi(parts[0])
		weight, weightErr := strconv.Atoi(parts[1])
		port, portErr := strconv.Atoi(parts[2])
		return priorityErr == nil && weightErr == nil && portErr == nil && priority >= 0 && priority <= 65535 && weight >= 0 && weight <= 65535 && port >= 0 && port <= 65535 && dnsNamePattern.MatchString(strings.TrimSuffix(parts[3], "."))
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

func (a *App) queueDNSJob(w http.ResponseWriter, r *http.Request, in cloudDNSRequest, action string) error {
	if a.DNSDesired != nil {
		if err := a.DNSDesired.markPending(in, action, a.Auth.UsernameForRequest(r)); err != nil {
			http.Error(w, "could not persist DNS desired state", http.StatusServiceUnavailable)
			return err
		}
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: action, ID: in.DomainID, DNS: in, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		if a.DNSDesired != nil {
			_ = a.DNSDesired.markResult(in, action, err)
		}
		http.Error(w, "could not persist DNS job", 500)
		return nil
	}
	writeJSON(w, 202, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
	return nil
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
		if strings.EqualFold(typ, in.Type) && strings.EqualFold(strings.TrimSuffix(name, "."), strings.TrimSuffix(in.Name, ".")) && strings.EqualFold(strings.TrimSuffix(target, "."), strings.TrimSuffix(in.Target, ".")) {
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
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.linode.com/v4"+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
	var value any
	const maxCloudResponseBytes = 8 << 20
	limited := io.LimitReader(res.Body, maxCloudResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxCloudResponseBytes {
		return nil, errors.New("Linode API response exceeds the 8 MiB limit")
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

var cloudIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func executeCloudAction(ctx context.Context, provider, action, id string) error {
	// Validate ID to prevent SSRF attacks via URL manipulation
	if !cloudIDPattern.MatchString(id) {
		return fmt.Errorf("invalid instance ID format: %s", id)
	}

	switch provider {
	case "linode":
		token := os.Getenv("STEPANEL_LINODE_TOKEN")
		if token == "" {
			return errors.New("STEPANEL_LINODE_TOKEN is not configured")
		}
		path := "/linode/instances/" + id
		if action == "snapshot" {
			path += "/backups"
		} else {
			path += "/" + map[string]string{"start": "boot", "stop": "shutdown", "reboot": "reboot"}[action]
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linode.com/v4"+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			return fmt.Errorf("Linode API returned %s", res.Status)
		}
		return nil
	case "aws":
		args := []string{"ec2"}
		switch action {
		case "start":
			args = append(args, "start-instances")
		case "stop":
			args = append(args, "stop-instances")
		case "reboot":
			args = append(args, "reboot-instances")
		case "snapshot":
			args = append(args, "create-snapshot", "--volume-id")
		}
		args = append(args, id, "--output", "json")
		return runCloudCLI(ctx, "aws", "AWS", args...)
	case "openstack":
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
	token := os.Getenv("STEPANEL_LINODE_TOKEN")
	if token == "" {
		return CloudInventory{}, errors.New("STEPANEL_LINODE_TOKEN is not configured")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	get := func(path string) (any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.linode.com/v4"+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			return nil, fmt.Errorf("Linode API returned %s", res.Status)
		}
		var value any
		if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	paths := []struct{ name, path string }{{"servers", "/linode/instances"}, {"dns", "/domains"}, {"load_balancers", "/nodebalancers"}, {"snapshots", "/account/linode/backups"}}
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
