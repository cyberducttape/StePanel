package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func uploadTestConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	cfg := Config{ImportRoot: filepath.Join(root, "imports"), WebRoot: filepath.Join(root, "web"), MaxUpload: 1 << 20, MinFreeBytes: 1}
	for _, path := range []string{cfg.ImportRoot, filepath.Join(cfg.WebRoot, "sites")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func archiveUploadRequest(t *testing.T, path, field, filename string, content []byte) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// unreadBody fails the test if the handler consumes the request body.
type unreadBody struct {
	t *testing.T
}

func (b unreadBody) Read([]byte) (int, error) {
	b.t.Error("request body was read before admission rejected it")
	return 0, io.EOF
}

func TestInspectStreamsArchiveWithoutMultipartSpool(t *testing.T) {
	cfg := uploadTestConfig(t)
	archive, err := os.ReadFile(makeTarGz(t, map[string]string{"cpmove-account/homedir/public_html/index.php": "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, Auth: Auth{}}
	request := archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "cpmove-account.tar.gz", archive)
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body = %s", response.Code, response.Body.String())
	}
	// MultipartReader leaves an empty sentinel form; FormFile and
	// ParseMultipartForm would have recorded the spooled file part here.
	if request.MultipartForm != nil && len(request.MultipartForm.File) > 0 {
		t.Fatal("inspect parsed the multipart form; the archive must stream without a spool copy")
	}
	var info struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil || info.UploadID == "" {
		t.Fatalf("inspect response = %s, err = %v", response.Body.String(), err)
	}
	staged, err := os.ReadFile(cpmoveUploadPath(cfg.ImportRoot, info.UploadID))
	if err != nil || !bytes.Equal(staged, archive) {
		t.Fatalf("staged upload does not match the archive: %v", err)
	}
}

func TestInspectRejectsUploadBeforeReadingBodyWhenCapacityIsShort(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 1 << 62
	app := &App{Config: cfg, Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", unreadBody{t})
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.ContentLength = 1 << 20
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusInsufficientStorage || !strings.Contains(response.Body.String(), "insufficient free space") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if list, err := os.ReadDir(cfg.ImportRoot); err != nil || len(list) != 0 {
		t.Fatalf("rejected upload staged files: %v %v", list, err)
	}
}

func TestInspectRequiresLengthWithoutUploadCeiling(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MaxUpload = 0
	app := &App{Config: cfg, Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", unreadBody{t})
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.ContentLength = -1
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusLengthRequired {
		t.Fatalf("status = %d, want 411; body = %s", response.Code, response.Body.String())
	}
}

func TestInspectRejectsOversizedArchiveWithoutLeavingStagedObject(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MaxUpload = 1024
	app := &App{Config: cfg, Auth: Auth{}}
	request := archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "big.tar.gz", bytes.Repeat([]byte("x"), 4096))
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", response.Code, response.Body.String())
	}
	if list, err := os.ReadDir(cfg.ImportRoot); err != nil || len(list) != 0 {
		t.Fatalf("oversized upload staged files: %v %v", list, err)
	}
}

func TestCPMoveImportRejectsArchiveSizedBodies(t *testing.T) {
	app := &App{Config: uploadTestConfig(t), Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/import", unreadBody{t})
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.ContentLength = cpmoveImportRequestBytes + 1
	response := httptest.NewRecorder()
	app.importBackup(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func TestAdmitCapacitySumsDemandsOnSharedFilesystem(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	share := free / 10 * 6
	sites := filepath.Join(cfg.WebRoot, "sites")
	if err := admitCapacity(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: share}}); err != nil {
		t.Fatalf("single demand of 60%% free was rejected: %v", err)
	}
	err = admitCapacity(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: share}, {Path: sites, Bytes: share}})
	var capacity *capacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("two 60%% demands on one filesystem were admitted: %v", err)
	}
	if err := admitCapacity(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: ^uint64(0)}, {Path: sites, Bytes: 1}}); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflowing demand error = %v", err)
	}
}

func TestUploadSpaceCheckChargesOnlyRemainingBytes(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	declared := free + free/2
	check := uploadSpaceCheck(cfg, declared)
	if err := check(0); err == nil {
		t.Fatal("space check admitted more remaining bytes than are free")
	}
	if err := check(int64(declared)); err != nil {
		t.Fatalf("space check rejected a fully written upload: %v", err)
	}
}

func TestDeclaredUploadBytes(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.ContentLength = 42
	if size, err := declaredUploadBytes(request, 10); err != nil || size != 42 {
		t.Fatalf("declared = %d, %v; want Content-Length", size, err)
	}
	request.ContentLength = -1
	if size, err := declaredUploadBytes(request, 10); err != nil || size != 10 {
		t.Fatalf("declared = %d, %v; want upload ceiling", size, err)
	}
	if _, err := declaredUploadBytes(request, 0); !errors.Is(err, errUploadLengthRequired) {
		t.Fatalf("err = %v, want errUploadLengthRequired", err)
	}
}

// TestJobEventsOutliveServerWriteTimeout guards the event stream against the
// server-wide write deadline: a job update sent after WriteTimeout elapsed
// must still reach the client.
func TestJobEventsOutliveServerWriteTimeout(t *testing.T) {
	app := &App{Config: uploadTestConfig(t), Auth: Auth{}, Jobs: NewJobs()}
	server := httptest.NewUnstartedServer(http.HandlerFunc(app.jobEvents))
	server.Config.WriteTimeout = 200 * time.Millisecond
	server.Start()
	defer server.Close()
	response, err := http.Get(server.URL + "/api/jobs/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := bufio.NewReader(response.Body)
	if line, err := lines.ReadString('\n'); err != nil || line != "event: snapshot\n" {
		t.Fatalf("first line = %q, err = %v", line, err)
	}
	time.Sleep(400 * time.Millisecond)
	app.Jobs.publish(Job{ID: "job-late", Kind: "site.backup", State: "running"})
	deadline := time.After(5 * time.Second)
	found := make(chan error, 1)
	go func() {
		for {
			line, err := lines.ReadString('\n')
			if err != nil {
				found <- err
				return
			}
			if strings.HasPrefix(line, "data:") && strings.Contains(line, "job-late") {
				found <- nil
				return
			}
		}
	}()
	select {
	case err := <-found:
		if err != nil {
			t.Fatalf("stream closed before the late job event arrived: %v", err)
		}
	case <-deadline:
		t.Fatal("late job event never arrived")
	}
}

func TestWriteUploadErrorMapsStatuses(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{&uploadRejection{http.StatusUnprocessableEntity, "bad"}, http.StatusUnprocessableEntity},
		{&capacityError{"full"}, http.StatusInsufficientStorage},
		{errUploadLengthRequired, http.StatusLengthRequired},
		{http.ErrNotMultipart, http.StatusBadRequest},
		{errors.New("disk exploded"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		writeUploadError(response, test.err)
		if response.Code != test.want {
			t.Errorf("writeUploadError(%v) = %d, want %d", test.err, response.Code, test.want)
		}
	}
}
