package rootbroker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseManagedFPMPool(t *testing.T) {
	valid := []byte(`[stepanel-demo]
user = sp-demo
listen = /run/php/stepanel-demo-8.3.sock
pm = ondemand
pm.max_children = 12
pm.status_path = /stepanel-fpm-status
`)
	got, err := parseManagedFPMPool(valid, "demo", "/run/php", "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if got.version != "8.3" || got.max != 12 || got.socket != "/run/php/stepanel-demo-8.3.sock" {
		t.Fatalf("pool = %#v", got)
	}

	for name, input := range map[string][]byte{
		"status disabled":         []byte(strings.ReplaceAll(string(valid), fpmStatusPath, "/status")),
		"socket outside root":     []byte(strings.ReplaceAll(string(valid), "/run/php/stepanel-demo-8.3.sock", "/tmp/stepanel-demo.sock")),
		"socket for another site": []byte(strings.ReplaceAll(string(valid), "stepanel-demo-8.3.sock", "stepanel-other-8.3.sock")),
		"invalid max children":    []byte(strings.ReplaceAll(string(valid), "pm.max_children = 12", "pm.max_children = 0")),
		"duplicate listen":        []byte(string(valid) + "listen = /run/php/stepanel-demo.sock\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseManagedFPMPool(input, "demo", "/run/php", "8.3"); err == nil {
				t.Fatal("expected invalid pool config to be rejected")
			}
		})
	}
}

func TestFPMStatusBrokerRequestRejectsCallerControlledInputs(t *testing.T) {
	validator := NewValidator("/var/www")
	if err := validator.validateSiteRequest(&SiteRequest{Action: "fpm-status", Site: "demo"}); err != nil {
		t.Fatalf("valid aggregate status request rejected: %v", err)
	}
	forged := &SiteRequest{Action: "fpm-status", Site: "demo", SiteUser: "root"}
	if err := validator.validateSiteRequest(forged); err == nil {
		t.Fatal("caller-selected account was accepted")
	}
	forged = &SiteRequest{Action: "fpm-status", Site: "demo", PHPVersion: "8.3"}
	if err := validator.validateSiteRequest(forged); err == nil {
		t.Fatal("caller-selected pool metadata was accepted")
	}
}

func TestParseFPMStatusNumericFormsAndSaturationSemantics(t *testing.T) {
	for _, raw := range []string{
		`{"active processes":12,"idle processes":0,"total processes":12,"listen queue":1,"max listen queue":4,"max children reached":7,"slow requests":2}`,
		`{"active processes":"12","idle processes":"0","total processes":"12","listen queue":"0","max listen queue":"4","max children reached":"7","slow requests":"2"}`,
	} {
		var parsed fpmRawStatus
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatal(err)
		}
		values := []*json.RawMessage{&parsed.Active, &parsed.Idle, &parsed.Total, &parsed.Queue, &parsed.MaxQueue, &parsed.MaxChildrenReached, &parsed.SlowRequests}
		got := make([]uint64, len(values))
		for i, value := range values {
			var err error
			got[i], err = parseFPMUint(*value)
			if err != nil {
				t.Fatalf("parse field %d: %v", i, err)
			}
		}
		pool := FPMStatusPool{ActiveProcesses: got[0], ListenQueue: got[3], MaxChildren: 13}
		pool.AtCapacity, pool.Saturated = fpmPoolIndicators(pool.ActiveProcesses, pool.ListenQueue, pool.MaxChildren)
		if strings.Contains(raw, `"listen queue":1`) && !pool.Saturated {
			t.Fatal("queued requests must indicate current saturation")
		}
		if !strings.Contains(raw, `"listen queue":1`) && pool.Saturated {
			t.Fatal("historical max_children_reached must not imply current saturation")
		}
	}
	if atCapacity, saturated := fpmPoolIndicators(12, 0, 12); !atCapacity || saturated {
		t.Fatal("a full pool without a queued request must be at capacity, not marked saturated")
	}
	if atCapacity, saturated := fpmPoolIndicators(11, 1, 12); atCapacity || !saturated {
		t.Fatal("a queued request must indicate current saturation")
	}
}

func TestFCGIRequestContainsOnlyPrivateStatusTarget(t *testing.T) {
	var request bytes.Buffer
	if err := sendFCGIRequest(&request); err != nil {
		t.Fatal(err)
	}
	data := request.Bytes()
	if !bytes.Contains(data, []byte(fpmStatusPath)) || !bytes.Contains(data, []byte("QUERY_STRINGjson")) {
		t.Fatalf("FastCGI request missing status params: %q", data)
	}
	if bytes.Contains(data, []byte("HTTP_HOST")) || bytes.Contains(data, []byte("REMOTE_ADDR")) {
		t.Fatal("status request unexpectedly contains client-controlled HTTP parameters")
	}
}

func TestReadFCGIResponseBoundsAndValidatesRecords(t *testing.T) {
	body := []byte("Status: 200 OK\r\nContent-Type: application/json\r\n\r\n{\"active processes\":\"1\"}")
	response := appendFCGIRecord(nil, 6, body)
	response = appendFCGIRecord(response, 3, make([]byte, 8))
	got, err := readFCGIResponse(bytes.NewReader(response), len(body))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("response = %q, err = %v", got, err)
	}
	if _, err := readFCGIResponse(bytes.NewReader(response), len(body)-1); err == nil {
		t.Fatal("oversized response was accepted")
	}
	if _, err := fastCGIBody(body); err != nil {
		t.Fatal(err)
	}
	if _, err := fastCGIBody([]byte("Status: 403 Forbidden\r\n\r\nno")); err == nil {
		t.Fatal("non-200 status was accepted")
	}
}

func TestRequestFPMStatusOverUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "fpm.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var params []byte
		for {
			var header [8]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				serverErr <- err
				return
			}
			size, padding := int(binary.BigEndian.Uint16(header[4:6])), int(header[6])
			payload := make([]byte, size+padding)
			if _, err := io.ReadFull(conn, payload); err != nil {
				serverErr <- err
				return
			}
			if header[1] == 4 {
				params = append(params, payload[:size]...)
			}
			if header[1] == 5 {
				break
			}
		}
		if !bytes.Contains(params, []byte(fpmStatusPath)) || !bytes.Contains(params, []byte("QUERY_STRINGjson")) {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		body := []byte("Content-Type: application/json\r\n\r\n{" +
			`"active processes":"2","idle processes":"3","total processes":"5","listen queue":"0","max listen queue":"4","max children reached":"1","slow requests":"0"}`)
		response := appendFCGIRecord(nil, 6, body)
		response = appendFCGIRecord(response, 6, nil)
		response = appendFCGIRecord(response, 3, make([]byte, 8))
		_, err = conn.Write(response)
		serverErr <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := requestFPMStatus(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active != 2 || got.Idle != 3 || got.Total != 5 || got.MaxChildrenReached != 1 {
		t.Fatalf("status = %#v", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestReadRootOwnedConfigRejectsSymlinks(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(target), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRootOwnedConfig(link, 1024); err == nil {
		t.Fatal("symlinked config was accepted")
	}
}

func appendFCGIRecord(dst []byte, typ byte, content []byte) []byte {
	header := []byte{1, typ, 0, 1, byte(len(content) >> 8), byte(len(content)), 0, 0}
	dst = append(dst, header...)
	dst = append(dst, content...)
	return dst
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return w.Buffer.Write(p)
}

func TestWriteFCGIRecordHandlesShortWrites(t *testing.T) {
	var output shortWriter
	content := []byte("payload")
	if err := writeFCGIRecord(&output, 1, 6, content); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{1, 6, 0, 1, 0, byte(len(content)), 0, 0}, content...)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("write = %v, want %v", output.Bytes(), want)
	}
	if err := writeAll(zeroWriter{}, []byte("x")); err != io.ErrShortWrite {
		t.Fatalf("zero writer error = %v, want ErrShortWrite", err)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestAppendFCGILengthEncoding(t *testing.T) {
	for _, length := range []uint32{0, 127, 128, 65535} {
		encoded := appendFCGILength(nil, length)
		var decoded uint32
		if length < 128 {
			if len(encoded) != 1 {
				t.Fatalf("length %d encoded as %v", length, encoded)
			}
			decoded = uint32(encoded[0])
		} else {
			if len(encoded) != 4 {
				t.Fatalf("length %d encoded as %v", length, encoded)
			}
			decoded = binary.BigEndian.Uint32([]byte{encoded[0] & 0x7f, encoded[1], encoded[2], encoded[3]})
		}
		if decoded != length {
			t.Fatalf("decoded length = %d, want %d", decoded, length)
		}
	}
}
