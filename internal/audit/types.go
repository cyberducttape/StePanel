package audit

import (
	"context"
	"net/http"
)

type Event struct {
	Time         string `json:"time"`
	Sequence     uint64 `json:"sequence"`
	Actor        string `json:"actor"`
	Action       string `json:"action"`
	Target       string `json:"target"`
	Detail       string `json:"detail"`
	PreviousHash string `json:"previous_hash"`
	Hash         string `json:"hash"`
}

type state struct {
	Version           int    `json:"version"`
	Sequence          uint64 `json:"sequence"`
	Hash              string `json:"hash"`
	FirstSequence     uint64 `json:"first_sequence"`
	FirstPreviousHash string `json:"first_previous_hash"`
	KeyCheck          string `json:"key_check"`
	Signature         string `json:"signature"`
	LastValidatedSize int64  `json:"last_validated_size,omitempty"`
}

type Logger interface {
	Log(ctx context.Context, action, target, detail string) error
	LogAs(ctx context.Context, actor, action, target, detail string) error
	Events(w http.ResponseWriter, r *http.Request)
	SecurityChecks(w http.ResponseWriter, r *http.Request)
	PersistenceError() error
	Verify(path string) error
}
