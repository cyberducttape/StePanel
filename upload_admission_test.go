package stepanel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCapacityCheckSumsDemandsOnSharedFilesystem(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	share := free / 10 * 6
	sites := filepath.Join(cfg.WebRoot, "sites")
	var ledger capacityLedger
	if err := ledger.check(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: share}}); err != nil {
		t.Fatalf("single demand of 60%% free was rejected: %v", err)
	}
	err = ledger.check(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: share}, {Path: sites, Bytes: share}})
	var capacity *capacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("two 60%% demands on one filesystem were admitted: %v", err)
	}
	if err := ledger.check(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: ^uint64(0)}, {Path: sites, Bytes: 1}}); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflowing demand error = %v", err)
	}
}

// TestCapacityReservationsPreventDoubleAdmission is the concurrency guard:
// two uploads that each fit alone must not both be admitted against the same
// free space while the first is still in progress.
func TestCapacityReservationsPreventDoubleAdmission(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	demand := []capacityDemand{{Path: cfg.ImportRoot, Bytes: free / 10 * 6}}
	var ledger capacityLedger
	first, err := ledger.reserve(cfg, "test", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("first reservation rejected: %v", err)
	}
	if ledger.heldBytes(cfg.ImportRoot) != demand[0].Bytes {
		t.Fatalf("held = %d, want %d", ledger.heldBytes(cfg.ImportRoot), demand[0].Bytes)
	}
	_, err = ledger.reserve(cfg, "test", cfg.ImportRoot, demand)
	var capacity *capacityError
	if !errors.As(err, &capacity) || !strings.Contains(err.Error(), "reserved by in-progress uploads") {
		t.Fatalf("second reservation error = %v, want capacity error naming the outstanding reservation", err)
	}
	if err := ledger.check(cfg, "test", demand); !errors.As(err, &capacity) {
		t.Fatalf("job check ignored the outstanding reservation: %v", err)
	}
	first.release()
	first.release() // idempotent
	if ledger.heldBytes(cfg.ImportRoot) != 0 {
		t.Fatalf("held after release = %d, want 0", ledger.heldBytes(cfg.ImportRoot))
	}
	second, err := ledger.reserve(cfg, "test", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("reservation after release rejected: %v", err)
	}
	second.release()
}

func TestCapacityReservationsCoordinateAcrossLedgerInstances(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	demand := []capacityDemand{{Path: cfg.ImportRoot, Bytes: free / 10 * 6}}
	hold, err := first.reserve(cfg, "cross-process", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("first shared reservation rejected: %v", err)
	}
	defer hold.release()
	if _, err := second.reserve(cfg, "cross-process", cfg.ImportRoot, demand); err == nil {
		t.Fatal("second ledger admitted a reservation already held by another process")
	}
	hold.release()
	secondHold, err := second.reserve(cfg, "cross-process", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("reservation after shared release rejected: %v", err)
	}
	secondHold.release()
}

func TestCapacityReservationConsumeReleasesWrittenBytes(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	var ledger capacityLedger
	reservation, err := ledger.reserve(cfg, "test", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if err := reservation.consume(400); err != nil {
		t.Fatal(err)
	}
	if held := ledger.heldBytes(cfg.ImportRoot); held != 600 {
		t.Fatalf("held after 400 written = %d, want 600", held)
	}
	if err := reservation.consume(400); err != nil || ledger.heldBytes(cfg.ImportRoot) != 600 {
		t.Fatalf("repeated consume changed the hold: %d %v", ledger.heldBytes(cfg.ImportRoot), err)
	}
	if err := reservation.consume(5000); err != nil || ledger.heldBytes(cfg.ImportRoot) != 0 {
		t.Fatalf("overrun consume should floor the hold at zero: %d %v", ledger.heldBytes(cfg.ImportRoot), err)
	}
	cfg.MinFreeBytes = 1 << 62
	short, err := (&capacityLedger{}).reserve(Config{ImportRoot: cfg.ImportRoot, WebRoot: cfg.WebRoot}, "test", cfg.ImportRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	short.reserve = 1 << 62
	var capacity *capacityError
	if err := short.consume(1); !errors.As(err, &capacity) {
		t.Fatalf("consume did not detect free space falling below the reserve: %v", err)
	}
}

func TestDeclaredUploadBytes(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.ContentLength = 42
	if size, known, err := declaredUploadBytes(request, 10); err != nil || size != 42 || !known {
		t.Fatalf("declared = %d, %v, %v; want known Content-Length", size, known, err)
	}
	request.ContentLength = -1
	if size, known, err := declaredUploadBytes(request, 10); err != nil || size != 10 || known {
		t.Fatalf("declared = %d, %v, %v; want the ceiling as an unknown-length limit", size, known, err)
	}
	if _, _, err := declaredUploadBytes(request, 0); !errors.Is(err, errUploadLengthRequired) {
		t.Fatalf("err = %v, want errUploadLengthRequired", err)
	}
}

func withProgressiveStep(t *testing.T, step uint64) {
	t.Helper()
	previous := progressiveUploadStep
	progressiveUploadStep = step
	t.Cleanup(func() { progressiveUploadStep = previous })
}

// An upload of unknown length holds one step ahead of the bytes written
// instead of the whole upload ceiling.
func TestProgressiveReservationExtendsAheadOfWrites(t *testing.T) {
	withProgressiveStep(t, 1000)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	var ledger capacityLedger
	demandsFor := func(archive uint64) []capacityDemand { return []capacityDemand{{Path: cfg.ImportRoot, Bytes: archive}} }
	reservation, err := ledger.reserveProgressive(cfg, "test", cfg.ImportRoot, 3500, demandsFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if held := ledger.heldBytes(cfg.ImportRoot); held != 1000 {
		t.Fatalf("initial hold = %d, want one step (1000), not the 3500 ceiling", held)
	}
	if err := reservation.consume(400); err != nil || ledger.heldBytes(cfg.ImportRoot) != 600 || reservation.admitted != 1000 {
		t.Fatalf("after 400 written: held %d admitted %d err %v", ledger.heldBytes(cfg.ImportRoot), reservation.admitted, err)
	}
	// Within half a step of the admitted size: extend before the writer arrives.
	if err := reservation.consume(600); err != nil || reservation.admitted != 2000 || ledger.heldBytes(cfg.ImportRoot) != 1400 {
		t.Fatalf("after 600 written: held %d admitted %d err %v", ledger.heldBytes(cfg.ImportRoot), reservation.admitted, err)
	}
	if err := reservation.consume(3200); err != nil || reservation.admitted != 3500 {
		t.Fatalf("extension must stop at the ceiling: admitted %d err %v", reservation.admitted, err)
	}
	var capacity *capacityError
	if err := reservation.consume(3501); !errors.As(err, &capacity) {
		t.Fatalf("writing past the ceiling = %v, want capacity error", err)
	}
}

func TestProgressiveReservationStopsWhenSpaceRunsOut(t *testing.T) {
	withProgressiveStep(t, 1000)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	var ledger capacityLedger
	demandsFor := func(archive uint64) []capacityDemand { return []capacityDemand{{Path: cfg.ImportRoot, Bytes: archive}} }
	reservation, err := ledger.reserveProgressive(cfg, "test", cfg.ImportRoot, 1<<40, demandsFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	reservation.reserve = 1 << 62 // the filesystem can no longer absorb another step
	var capacity *capacityError
	if err := reservation.consume(900); !errors.As(err, &capacity) || !strings.Contains(err.Error(), "to continue this test") {
		t.Fatalf("extension without space = %v, want capacity error", err)
	}
}

// A chunked upload smaller than free space must not be refused because the
// configured ceiling exceeds free space.
func TestChunkedUploadIsNotRefusedForTheUploadCeiling(t *testing.T) {
	withProgressiveStep(t, 64<<10)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	cfg.MaxUpload = 1 << 60 // far beyond any filesystem
	app := &App{Config: cfg, Auth: Auth{}}
	request := archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "site.tar.gz", bytes.Repeat([]byte("x"), 4096))
	request.ContentLength = -1
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code == http.StatusInsufficientStorage || strings.Contains(response.Body.String(), "insufficient free space") {
		t.Fatalf("small chunked upload refused for capacity: %d %s", response.Code, response.Body.String())
	}
	if held := app.capacity.heldBytes(cfg.ImportRoot); held != 0 {
		t.Fatalf("reservation leaked after the request: %d bytes held", held)
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
		{fmt.Errorf("read body: %w", os.ErrDeadlineExceeded), http.StatusRequestTimeout},
		{errors.New("disk exploded"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		(&App{}).writeUploadError(response, httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", nil), test.err)
		if response.Code != test.want {
			t.Errorf("writeUploadError(%v) = %d, want %d", test.err, response.Code, test.want)
		}
	}
}

func TestWriteUploadErrorDoesNotReflectMultipartParserDetails(t *testing.T) {
	response := httptest.NewRecorder()
	err := fmt.Errorf("request-controlled <script>alert(1)</script>: %w", http.ErrNotMultipart)
	(&App{}).writeUploadError(response, httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", nil), err)

	if got := response.Body.String(); strings.Contains(got, "request-controlled") || strings.Contains(got, "<script>") || !strings.Contains(got, "invalid multipart upload") {
		t.Fatalf("multipart parser detail was reflected: %q", got)
	}
}

// TestInspectAbortsStalledUpload guards the upload slot and reservation: a
// client that stops sending body bytes is cut off after uploadIdleTimeout
// with 408, and its partial object and capacity hold are released.
func TestInspectAbortsStalledUpload(t *testing.T) {
	previous := uploadIdleTimeout
	uploadIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { uploadIdleTimeout = previous })
	cfg := uploadTestConfig(t)
	app := &App{Config: cfg, Auth: Auth{}, Metrics: NewMetrics()}
	server := httptest.NewServer(http.HandlerFunc(app.inspect))
	defer server.Close()

	reader, writer := io.Pipe()
	defer writer.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/cpmove/inspect", reader)
	if err != nil {
		t.Fatal(err)
	}
	const boundary = "stall"
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	request.ContentLength = 1 << 20
	go func() {
		// Send the part header and some archive bytes, then stall.
		_, _ = io.WriteString(writer, "--"+boundary+"\r\nContent-Disposition: form-data; name=\"backup\"; filename=\"a.tar.gz\"\r\n\r\n")
		_, _ = writer.Write(bytes.Repeat([]byte("x"), 64<<10))
	}()
	started := time.Now()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 408; body = %s", response.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("stall detected after %s", elapsed)
	}
	if list, err := os.ReadDir(cfg.ImportRoot); err != nil || len(list) != 0 {
		t.Fatalf("stalled upload left staged files: %v %v", list, err)
	}
	if held := app.capacity.heldBytes(cfg.ImportRoot); held != 0 {
		t.Fatalf("stalled upload kept %d reserved bytes", held)
	}
	var metrics bytes.Buffer
	app.Metrics.Write(&metrics)
	if !strings.Contains(metrics.String(), `stepanel_upload_rejections_total{reason="stalled"} 1`) {
		t.Fatal("stalled upload was not counted")
	}
}

func TestInspectReleasesReservationAfterSuccessfulUpload(t *testing.T) {
	cfg := uploadTestConfig(t)
	archive, err := os.ReadFile(makeTarGz(t, map[string]string{"cpmove-account/homedir/public_html/index.php": "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, Auth: Auth{}, Metrics: NewMetrics()}
	response := httptest.NewRecorder()
	app.inspect(response, archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "cpmove-account.tar.gz", archive))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if held := app.capacity.heldBytes(cfg.ImportRoot); held != 0 {
		t.Fatalf("completed upload kept %d reserved bytes", held)
	}
}
