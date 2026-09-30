package audit

import (
	"bufio"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// ReadScopedEvents verifies the complete audit chain, then returns only
// events whose target belongs to the supplied tenant or whose actor is that
// tenant. It is intentionally separate from the operator HTTP endpoint so a
// customer can never provide an arbitrary target filter to enumerate another
// tenant's history.
func ReadScopedEvents(path string, targets []string, actor string, limit int) ([]Event, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("audit event limit must be between 1 and 500")
	}
	if path == "" {
		return []Event{}, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []Event{}, nil
	}
	logger := &defaultLogger{path: path}
	all, err := logger.readVerifiedEvents("", "", 500)
	if err != nil {
		return nil, err
	}
	targetSet := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targetSet[strings.ToLower(strings.TrimSpace(target))] = struct{}{}
	}
	actor = strings.ToLower(strings.TrimSpace(actor))
	filtered := make([]Event, 0, limit)
	for _, event := range all {
		_, targetMatch := targetSet[strings.ToLower(event.Target)]
		if !targetMatch && (actor == "" || !strings.EqualFold(event.Actor, actor)) {
			continue
		}
		filtered = append(filtered, event)
		if len(filtered) > limit {
			filtered = filtered[1:]
		}
	}
	return filtered, nil
}

func (l *defaultLogger) Events(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			http.Error(w, "limit must be between 1 and 500", http.StatusUnprocessableEntity)
			return
		}
		limit = value
	}

	if l.path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"events": []Event{}, "integrity": "unconfigured"})
		return
	}

	if _, err := os.Stat(l.path); os.IsNotExist(err) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []Event{}, "integrity": "empty"})
		return
	}

	target, action := r.URL.Query().Get("target"), r.URL.Query().Get("action")
	events, err := l.readVerifiedEvents(target, action, limit)
	if err != nil {
		http.Error(w, "audit integrity verification failed", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"events": events, "integrity": "verified"})
}

func (l *defaultLogger) SecurityChecks(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(l.path) == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"checks": []map[string]string{{
				"name":     "audit chain",
				"status":   "unconfigured",
				"severity": "high",
				"detail":   "No audit log path is configured.",
			}},
			"integrity": "unconfigured",
		})
		return
	}

	if _, err := os.Stat(l.path); os.IsNotExist(err) {
		writeJSON(w, http.StatusOK, map[string]any{
			"checks": []map[string]string{{
				"name":     "audit chain",
				"status":   "empty",
				"severity": "high",
				"detail":   "The configured audit log has no signed events yet.",
			}},
			"integrity": "empty",
		})
		return
	}

	if _, err := l.readVerifiedEvents("", "", 1); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"checks": []map[string]string{{
				"name":     "audit chain",
				"status":   "fail",
				"severity": "critical",
				"detail":   "The signed audit chain failed integrity verification.",
			}},
			"integrity": "failed",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"checks": []map[string]string{{
			"name":     "audit chain",
			"status":   "pass",
			"severity": "low",
			"detail":   "The signed audit chain and durable state match.",
		}},
		"integrity": "verified",
	})
}

func (l *defaultLogger) readVerifiedEvents(target, action string, limit int) ([]Event, error) {
	file, err := os.Open(l.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	events := make([]Event, 0, limit)
	var first, previous *Event

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)

	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}

		if err := validateEvent(event); err != nil {
			return nil, err
		}

		expected, err := hashEvent(event)
		if err != nil || !hmac.Equal([]byte(expected), []byte(event.Hash)) {
			return nil, fmt.Errorf("audit event %d has an invalid signature", event.Sequence)
		}

		if previous != nil && (event.Sequence != previous.Sequence+1 || event.PreviousHash != previous.Hash) {
			return nil, fmt.Errorf("audit chain breaks at event %d", event.Sequence)
		}

		eventCopy := event
		if first == nil {
			first = &eventCopy
		}
		previous = &eventCopy

		if target == "" || strings.EqualFold(target, event.Target) {
			if action == "" || strings.EqualFold(action, event.Action) {
				events = append(events, event)
				if len(events) > limit {
					events = events[1:]
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if previous == nil {
		return nil, errors.New("audit log contains no signed events")
	}

	s, err := l.loadState(l.path + ".state")
	if err != nil {
		return nil, err
	}

	if s.Sequence != previous.Sequence || s.Hash != previous.Hash ||
		first.Sequence != s.FirstSequence || first.PreviousHash != s.FirstPreviousHash {
		return nil, errors.New("audit chain state does not match the log")
	}

	return events, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
