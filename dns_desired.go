package stepanel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type DNSDesiredRecord struct {
	Key       string    `json:"key"`
	DomainID  string    `json:"domain_id"`
	RecordID  string    `json:"record_id,omitempty"`
	Type      string    `json:"type"`
	Name      string    `json:"name"`
	Target    string    `json:"target"`
	TTL       int       `json:"ttl"`
	Priority  int       `json:"priority,omitempty"`
	Service   string    `json:"service,omitempty"`
	Protocol  string    `json:"protocol,omitempty"`
	Port      int       `json:"port,omitempty"`
	Weight    int       `json:"weight,omitempty"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	State     string    `json:"state"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DNSDesiredStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]DNSDesiredRecord
}

func OpenDNSDesiredStore(path string) (*DNSDesiredStore, error) {
	store := &DNSDesiredStore{path: path, values: map[string]DNSDesiredRecord{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("DNS desired state exceeds 4 MiB")
	}
	if err := json.Unmarshal(data, &store.values); err != nil {
		return nil, fmt.Errorf("decode DNS desired state: %w", err)
	}
	for key, record := range store.values {
		validRecord := record.Action == "delete" && cloudNumericID.MatchString(record.RecordID)
		if record.Action != "delete" {
			recordType := strings.ToUpper(record.Type)
			validName := dnsNamePattern.MatchString(record.Name)
			if (recordType == "SRV" && record.Name == "") || (recordType == "MX" && record.Name == "" && record.Target == "") {
				// SRV's owner is represented by service/protocol, and legacy
				// JSON may contain the provider's empty name or an older ignored
				// name value. Null MX also requires an empty name and target.
				validName = true
			}
			validRecord = dnsTypePattern.MatchString(recordType) && validName && record.TTL >= 30 && record.TTL <= 2419200
		}
		if key == "" || record.Key != key || !cloudNumericID.MatchString(record.DomainID) || !validRecord || (record.State != "pending" && record.State != "applied" && record.State != "failed") {
			return nil, errors.New("invalid DNS desired state")
		}
	}
	return store, nil
}

func (s *DNSDesiredStore) persistLocked() error {
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, data); bound {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0600)
}

func dnsDesiredKey(in cloudDNSRequest) string {
	if in.RecordID != "" {
		return in.DomainID + "/" + in.RecordID
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%d\x00%d", strings.ToUpper(in.Type), strings.ToLower(in.Name), in.Target, in.TTL, in.Priority, strings.ToLower(in.Service), strings.ToLower(in.Protocol), in.Port, in.Weight)))
	return in.DomainID + "/pending-" + hex.EncodeToString(hash[:8])
}

func newPendingDNSRecord(in cloudDNSRequest, action, actor string) DNSDesiredRecord {
	return DNSDesiredRecord{Key: dnsDesiredKey(in), DomainID: in.DomainID, RecordID: in.RecordID, Type: strings.ToUpper(in.Type), Name: in.Name, Target: in.Target, TTL: in.TTL, Priority: in.Priority, Service: in.Service, Protocol: in.Protocol, Port: in.Port, Weight: in.Weight, Action: action, Actor: actor, State: "pending", UpdatedAt: time.Now().UTC()}
}

func (s *DNSDesiredStore) markResult(in cloudDNSRequest, action string, resultErr error) error {
	key := dnsDesiredKey(in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if action == "delete" && resultErr == nil {
		return persistMapKeyChange(s.values, key, (*DNSDesiredRecord)(nil), s.persistLocked)
	}
	previous, existed := s.values[key]
	record, ok := previous, existed
	if !ok {
		record = DNSDesiredRecord{Key: key, DomainID: in.DomainID, RecordID: in.RecordID, Type: strings.ToUpper(in.Type), Name: in.Name, Target: in.Target, TTL: in.TTL, Priority: in.Priority, Service: in.Service, Protocol: in.Protocol, Port: in.Port, Weight: in.Weight, Action: action}
	}
	record.State = "applied"
	record.LastError = ""
	if resultErr != nil {
		record.State = "failed"
		record.LastError = resultErr.Error()
	}
	record.UpdatedAt = time.Now().UTC()
	return persistMapKeyChange(s.values, key, &record, s.persistLocked)
}

// failOrphanedPending marks every stored pending record whose key is not in
// active as failed. Pending is no longer stored (it is derived from live
// jobs), so these are records written by earlier releases before the job
// was enqueued; leaving them pending would show work that will never run.
func (s *DNSDesiredStore) failOrphanedPending(active map[string]bool, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := make(map[string]DNSDesiredRecord)
	now := time.Now().UTC()
	for key, record := range s.values {
		if record.State != "pending" || active[key] {
			continue
		}
		previous[key] = record
		record.State = "failed"
		record.LastError = reason
		record.UpdatedAt = now
		s.values[key] = record
	}
	if len(previous) == 0 {
		return 0, nil
	}
	if err := s.persistLocked(); err != nil {
		if !errors.Is(err, errControlPlaneStateRefreshed) {
			for key, record := range previous {
				s.values[key] = record
			}
		}
		return 0, err
	}
	return len(previous), nil
}

// reconcileOrphanedDNSDesired fails stored pending DNS records left by
// earlier releases that have no queued or running cloud job, so operators
// see them and resubmit.
func (a *App) reconcileOrphanedDNSDesired() (int, error) {
	if a.DNSDesired == nil || a.Jobs == nil {
		return 0, nil
	}
	payloads, err := a.Jobs.ActivePayloads("cloud.action")
	if err != nil {
		return 0, err
	}
	active := make(map[string]bool, len(payloads))
	for _, payload := range payloads {
		var request durableCloudRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return 0, fmt.Errorf("decode active cloud job payload: %w", err)
		}
		if request.Operation == "dns" {
			active[dnsDesiredKey(request.DNS)] = true
		}
	}
	return a.DNSDesired.failOrphanedPending(active, "no durable job was recorded for this change; resubmit it")
}

func (s *DNSDesiredStore) list(domainID string) []DNSDesiredRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]DNSDesiredRecord, 0)
	for _, record := range s.values {
		if domainID == "" || record.DomainID == domainID {
			items = append(items, record)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}
