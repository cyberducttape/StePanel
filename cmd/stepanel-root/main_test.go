package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func TestOpenFencingDatabaseDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-initialized.db")
	if db, err := openFencingDatabase(path); err == nil {
		_ = db.Close()
		t.Fatal("opening a missing fencing database unexpectedly succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing fencing database was created: stat error = %v", err)
	}
}

func TestOpenFencingDatabaseOpensInitializedDatabaseReadWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state with spaces", "control-plane.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	seed, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`CREATE TABLE initialized (value TEXT NOT NULL)`); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openFencingDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO initialized(value) VALUES ('broker-write')`); err != nil {
		t.Fatalf("broker could not write initialized database: %v", err)
	}
}

func TestOpenFencingDatabaseBusyWaitFitsLeaseSafetyMargin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.db")
	seed, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`CREATE TABLE initialized (value TEXT NOT NULL)`); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openFencingDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	locker, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	lockConn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer lockConn.ExecContext(context.Background(), `ROLLBACK`)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	var value string
	err = db.QueryRowContext(ctx, `SELECT value FROM initialized`).Scan(&value)
	if err == nil {
		t.Fatal("query unexpectedly succeeded while another connection held an exclusive lock")
	}
	if elapsed := time.Since(started); elapsed > rootbroker.FencingDatabaseBusyTimeout+500*time.Millisecond {
		t.Fatalf("SQLite lock wait took %s, exceeding configured busy timeout %s", elapsed, rootbroker.FencingDatabaseBusyTimeout)
	}
}

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

type healthExecutor struct{}

func (healthExecutor) Execute(context.Context, *rootbroker.Request) (*rootbroker.Response, error) {
	return &rootbroker.Response{OK: true}, nil
}

func TestSocketRejectsOversizedRequest(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go serveConn(healthExecutor{}, server, defaultConnLimits, log.New(io.Discard, "", 0))
	go func() {
		payload := strings.Repeat("A", maxRequestBytes)
		_, _ = client.Write([]byte(`{"type":"db","db":{"action":"restore-dump","dump_data":"` + payload + `"}}` + "\n"))
	}()
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var resp rootbroker.Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "request limit") {
		t.Fatalf("oversized request response = %+v", resp)
	}
	if used := brokerRequestBudget.used.Load(); used != 0 {
		// serveConn may still be returning; give it a moment to release.
		time.Sleep(100 * time.Millisecond)
		if used = brokerRequestBudget.used.Load(); used != 0 {
			t.Fatalf("budget still holds %d bytes after the connection closed", used)
		}
	}
}

// The per-request limit restarts for each request: a long-lived connection
// is not cut off by the cumulative size of many small requests.
func TestRequestLimitAppliesPerRequest(t *testing.T) {
	budget := &requestBudget{limit: 1 << 20}
	var input strings.Builder
	for range 100 {
		input.WriteString(`{"type":"health"}` + "\n")
	}
	reader := newBudgetedReader(strings.NewReader(input.String()), budget, 64)
	decoder := json.NewDecoder(reader)
	for i := range 100 {
		var req rootbroker.Request
		if err := decoder.Decode(&req); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		reader.nextRequest()
	}
	if budget.used.Load() != 0 {
		t.Fatalf("budget holds %d bytes after all requests were served", budget.used.Load())
	}
}

func TestRequestBudgetIsSharedAcrossConnections(t *testing.T) {
	budget := &requestBudget{limit: 1024}
	first := newBudgetedReader(strings.NewReader(strings.Repeat("x", 1024)), budget, 4096)
	if _, err := io.ReadAll(io.LimitReader(first, 1024)); err != nil {
		t.Fatal(err)
	}
	second := newBudgetedReader(strings.NewReader("y"), budget, 4096)
	if _, err := second.Read(make([]byte, 1)); !errors.Is(err, errRequestBudgetExhausted) {
		t.Fatalf("read beyond the shared budget error = %v", err)
	}
	first.nextRequest()
	if n, err := second.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("read after release = %d, %v", n, err)
	}
}

func TestStdinTransportRejectsOversizedRequest(t *testing.T) {
	payload := strings.Repeat("A", maxRequestBytes)
	input := strings.NewReader(`{"type":"db","db":{"dump_data":"` + payload + `"}}` + "\n" + `{"type":"health"}` + "\n")
	var output strings.Builder
	serveRequests(healthExecutor{}, input, &output, log.New(io.Discard, "", 0))
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	var resp rootbroker.Response
	if err := decoder.Decode(&resp); err != nil || resp.OK || !strings.Contains(resp.Error, "request limit") {
		t.Fatalf("oversized stdin request response = %+v, %v", resp, err)
	}
	if decoder.More() {
		t.Fatal("transport kept reading after an oversized request")
	}
}
