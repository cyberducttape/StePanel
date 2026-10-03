package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// slowExecutor blocks site requests until released and answers health
// immediately, standing in for a long privileged operation.
type slowExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *slowExecutor) Execute(_ context.Context, req *rootbroker.Request) (*rootbroker.Response, error) {
	if req.RequestType == "health" {
		return &rootbroker.Response{OK: true}, nil
	}
	close(e.started)
	<-e.release
	return &rootbroker.Response{OK: true}, nil
}

func roundTrip(t *testing.T, socketPath string, req *rootbroker.Request, timeout time.Duration) (*rootbroker.Response, error) {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return nil, err
	}
	var resp rootbroker.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func startTestSocket(t *testing.T, broker executor) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	logger := log.New(io.Discard, "", 0)
	go func() {
		_ = acceptConnections(listener, broker, func(net.Conn) error { return nil }, logger)
	}()
	return socketPath
}

func TestSocketServesHealthWhileOperationRuns(t *testing.T) {
	exec := &slowExecutor{started: make(chan struct{}), release: make(chan struct{})}
	socketPath := startTestSocket(t, exec)

	slow := make(chan error, 1)
	go func() {
		_, err := roundTrip(t, socketPath, &rootbroker.Request{RequestType: "site", Site: &rootbroker.SiteRequest{Action: "delete", Site: "alpha"}}, 10*time.Second)
		slow <- err
	}()
	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow operation did not start")
	}

	resp, err := roundTrip(t, socketPath, &rootbroker.Request{RequestType: "health"}, 2*time.Second)
	if err != nil || !resp.OK {
		t.Fatalf("health behind a running operation = %+v, %v", resp, err)
	}

	close(exec.release)
	if err := <-slow; err != nil {
		t.Fatalf("slow operation: %v", err)
	}
}

func TestSocketClosesStalledConnection(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		serveConn(&slowExecutor{}, server, connLimits{read: 50 * time.Millisecond, write: 50 * time.Millisecond}, log.New(io.Discard, "", 0))
		close(done)
	}()
	// The peer connects and never sends a request.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled connection was not closed by the read deadline")
	}
}

func TestSocketRejectsMalformedRequestAndCloses(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		serveConn(&slowExecutor{}, server, defaultConnLimits, log.New(io.Discard, "", 0))
		close(done)
	}()
	go func() { _, _ = client.Write([]byte("{not json\n")) }()
	var resp rootbroker.Response
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("malformed request response = %+v", resp)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection stayed open after a malformed request")
	}
}
