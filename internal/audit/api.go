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
	http.Error(w, "not implemented", http.StatusNotImplemented)
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
