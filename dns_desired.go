package main

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
			validRecord = dnsTypePattern.MatchString(strings.ToUpper(record.Type)) && dnsNamePattern.MatchString(record.Name) && record.TTL >= 30 && record.TTL <= 604800
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
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", strings.ToUpper(in.Type), strings.ToLower(in.Name), in.Target, in.TTL)))
	return in.DomainID + "/pending-" + hex.EncodeToString(hash[:8])
}

func (s *DNSDesiredStore) markPending(in cloudDNSRequest, action, actor string) error {
	key := dnsDesiredKey(in)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[key]
	s.values[key] = DNSDesiredRecord{Key: key, DomainID: in.DomainID, RecordID: in.RecordID, Type: strings.ToUpper(in.Type), Name: in.Name, Target: in.Target, TTL: in.TTL, Action: action, Actor: actor, State: "pending", UpdatedAt: time.Now().UTC()}
	if err := s.persistLocked(); err != nil {
		if existed {
			s.values[key] = previous
		} else {
			delete(s.values, key)
		}
		return err
	}
	return nil
}

func (s *DNSDesiredStore) markResult(in cloudDNSRequest, action string, resultErr error) error {
	key := dnsDesiredKey(in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if action == "delete" && resultErr == nil {
		delete(s.values, key)
		return s.persistLocked()
	}
	record, ok := s.values[key]
	if !ok {
		record = DNSDesiredRecord{Key: key, DomainID: in.DomainID, RecordID: in.RecordID, Type: strings.ToUpper(in.Type), Name: in.Name, Target: in.Target, TTL: in.TTL, Action: action}
	}
	record.State = "applied"
	record.LastError = ""
	if resultErr != nil {
		record.State = "failed"
		record.LastError = resultErr.Error()
	}
	record.UpdatedAt = time.Now().UTC()
	s.values[key] = record
	return s.persistLocked()
}

// failOrphanedPending marks every pending record whose key is not in active
// as failed. A pending record without a live durable job can only come from
// an interrupted request between persisting desired state and enqueueing its
// job; leaving it pending would show an operation that will never run.
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
		for key, record := range previous {
			s.values[key] = record
		}
		return 0, err
	}
	return len(previous), nil
}

// reconcileOrphanedDNSDesired fails pending DNS desired-state records that
// have no queued or running cloud job, so operators see them and resubmit.
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
