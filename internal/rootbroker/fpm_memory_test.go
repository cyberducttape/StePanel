package rootbroker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeFakeProc(t *testing.T, root string, pid, ppid int, uid uint32, rssKB int, title string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	u := strconv.FormatUint(uint64(uid), 10)
	status := "Name:\tphp-fpm8.3\nPPid:\t" + strconv.Itoa(ppid) + "\nUid:\t" + u + "\t" + u + "\t" + u + "\t" + u + "\nVmRSS:\t" + strconv.Itoa(rssKB) + " kB\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(title+"\x00\x00   "), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSampleFPMWorkerMemoryMatchesOnlyTheSitePool(t *testing.T) {
	proc := t.TempDir()
	writeFakeProc(t, proc, 10, 1, 0, 30000, "php-fpm: master process (/etc/php/8.3/fpm/php-fpm.conf)")
	writeFakeProc(t, proc, 20, 1, 0, 30000, "php-fpm: master process (/etc/php/8.2/fpm/php-fpm.conf)")
	for i, rss := range []int{10240, 20480, 40960} {
		writeFakeProc(t, proc, 100+i, 10, 1001, rss, "php-fpm: pool stepanel-demo")
	}
	writeFakeProc(t, proc, 200, 20, 1001, 99999, "php-fpm: pool stepanel-demo")      // other PHP version
	writeFakeProc(t, proc, 300, 10, 1001, 88888, "php-fpm: pool stepanel-demo-shop") // prefix site
	writeFakeProc(t, proc, 400, 10, 1002, 77777, "php-fpm: pool stepanel-demo")      // other account
	if err := os.WriteFile(filepath.Join(proc, "uptime"), []byte("1 1"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sampleFPMWorkerMemory(proc, 1001, "demo", "8.3", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.SampledWorkers != 3 || got.TotalRSSBytes != 71680*1024 || got.MaxRSSBytes != 40960*1024 || got.P95RSSBytes != 40960*1024 || got.MeanRSSBytes != 71680*1024/3 {
		t.Fatalf("sample = %#v", got)
	}

	all, err := sampleFPMWorkerMemory(proc, 1001, "demo", "8.3", false)
	if err != nil {
		t.Fatal(err)
	}
	if all.SampledWorkers != 4 {
		t.Fatalf("single-pool sample = %d workers, want 4 (prefix site and other uid excluded)", all.SampledWorkers)
	}

	if _, err := sampleFPMWorkerMemory(proc, 1001, "missing", "8.3", false); err == nil {
		t.Fatal("expected error when no worker matches")
	}
}

func TestProcRSSBytesRejectsMalformedStatus(t *testing.T) {
	for _, status := range []string{"", "VmRSS:\t12 MB\n", "VmRSS:\tabc kB\n", "VmRSS:\n"} {
		if _, ok := procRSSBytes([]byte(status)); ok {
			t.Fatalf("status %q accepted", status)
		}
	}
	if got, ok := procRSSBytes([]byte("VmRSS:\t   12 kB\n")); !ok || got != 12*1024 {
		t.Fatalf("rss = %d/%v", got, ok)
	}
}

func TestFPMPoolGuidanceIncludesWorkerMemory(t *testing.T) {
	pool := FPMStatusPool{MaxChildren: 4, WorkerMemory: &FPMWorkerMemory{SampledWorkers: 2, MeanRSSBytes: 32 << 20, P95RSSBytes: 40 << 20, MaxRSSBytes: 40 << 20, ProjectedRSSAtMaxChildrenBytes: 160 << 20, OneAdditionalWorkerBytes: 40 << 20}}
	guidance := fpmPoolGuidance("8.3", pool)
	if !strings.Contains(guidance.Evidence, "160.0 MiB") || !strings.Contains(guidance.Recommendation, "host memory headroom") {
		t.Fatalf("guidance = %#v", guidance)
	}
}
