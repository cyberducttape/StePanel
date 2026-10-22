package rootbroker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatorFilesRequestsAcceptOnlyCanonicalPaths(t *testing.T) {
	v := NewValidator("/var/www")
	valid := []*FilesRequest{
		{Action: "list", Site: "alice"},
		{Action: "list", Site: "alice", Path: "wp-content/themes"},
		{Action: "read", Site: "alice", Path: "index.php"},
		{Action: "write", Site: "alice", Path: "index.php", Content: []byte("<?php")},
		{Action: "mkdir", Site: "alice", Path: "assets"},
		{Action: "rename", Site: "alice", Path: "a.txt", Destination: "assets/a.txt"},
		{Action: "delete", Site: "alice", Path: "assets"},
	}
	for _, req := range valid {
		if err := v.ValidateRequest(&Request{RequestType: "files", Files: req}); err != nil {
			t.Errorf("valid %s request rejected: %v", req.Action, err)
		}
	}
	invalid := []*FilesRequest{
		{Action: "list", Site: "../alice"},
		{Action: "list", Site: "alice", Path: "../bob"},
		{Action: "list", Site: "alice", Path: "/etc"},
		{Action: "read", Site: "alice", Path: "a/./b"},
		{Action: "write", Site: "alice", Path: ""},
		{Action: "delete", Site: "alice", Path: ""},
		{Action: "rename", Site: "alice", Path: "a", Destination: ""},
		{Action: "rename", Site: "alice", Path: "a", Destination: "../a"},
		{Action: "mkdir", Site: "alice", Path: "a", Destination: "b"},
		{Action: "read", Site: "alice", Path: "a", Content: []byte("x")},
		{Action: "write", Site: "alice", Path: "big", Content: make([]byte, 4<<20+1)},
		{Action: "chmod", Site: "alice", Path: "a"},
	}
	for _, req := range invalid {
		if err := v.ValidateRequest(&Request{RequestType: "files", Files: req}); err == nil {
			t.Errorf("invalid files request accepted: %s %q %q", req.Action, req.Path, req.Destination)
		}
	}
}

func TestFilesMutationsRequireFencing(t *testing.T) {
	for action, fenced := range map[string]bool{"list": false, "read": false, "write": true, "mkdir": true, "rename": true, "delete": true} {
		if got := requestRequiresFencing(&Request{RequestType: "files", Files: &FilesRequest{Action: action}}); got != fenced {
			t.Errorf("files %s requires fencing = %v, want %v", action, got, fenced)
		}
	}
}

func TestBrokerFilesRunAsTheRecordedAccountUser(t *testing.T) {
	broker, db, _ := newAccountBroker(t)
	if _, err := db.Exec(`INSERT INTO site_identities (site, username, created_at) VALUES ('legacy', 'sp-legacy', 0)`); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "filectl.args")
	stdin := filepath.Join(t.TempDir(), "filectl.stdin")
	stub := filepath.Join(t.TempDir(), "stepanel-filectl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + record + "\ncase $1 in\n" +
		"  list) printf 'f\\t5\\t640\\t1700000000\\tindex.php\\nd\\t4096\\t2750\\t1700000000\\tassets\\n' ;;\n" +
		"  read) printf 'hello' ;;\n" +
		"  write) cat > " + stdin + " ;;\n" +
		"  delete) echo 'path does not exist' >&2; exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	broker.filectl = stub
	ctx := context.Background()
	run := func(req *FilesRequest) (*Response, FilesResponse) {
		t.Helper()
		resp, err := broker.handleFilesRequest(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var details FilesResponse
		if resp.OK && len(resp.Details) > 0 {
			if err := json.Unmarshal(resp.Details, &details); err != nil {
				t.Fatal(err)
			}
		}
		return resp, details
	}
	resp, details := run(&FilesRequest{Action: "list", Site: "alice"})
	if !resp.OK || details.Listing == nil || len(details.Listing.Entries) != 2 || details.Listing.Entries[0].Name != "assets" {
		t.Fatalf("list = %+v, %+v", resp, details)
	}
	if resp, details = run(&FilesRequest{Action: "read", Site: "alice", Path: "index.php"}); !resp.OK || string(details.Content) != "hello" {
		t.Fatalf("read = %+v, %q", resp, details.Content)
	}
	if resp, _ = run(&FilesRequest{Action: "write", Site: "alice", Path: "index.php", Content: []byte("<?php echo 1;")}); !resp.OK {
		t.Fatalf("write = %s", resp.Error)
	}
	if data, err := os.ReadFile(stdin); err != nil || string(data) != "<?php echo 1;" {
		t.Fatalf("helper stdin = %q, %v", data, err)
	}
	if resp, _ = run(&FilesRequest{Action: "rename", Site: "alice", Path: "a", Destination: "b"}); !resp.OK {
		t.Fatalf("rename = %s", resp.Error)
	}
	if resp, _ = run(&FilesRequest{Action: "delete", Site: "alice", Path: "gone"}); resp.OK || !strings.Contains(resp.Error, "path does not exist") {
		t.Fatalf("delete error = %+v", resp)
	}
	// A legacy site is not in a hosting account and never reaches the helper.
	if resp, _ = run(&FilesRequest{Action: "list", Site: "legacy"}); resp.OK {
		t.Fatal("a legacy site was listed")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "list alice alice \nread alice alice index.php\nwrite alice alice index.php\nrename alice alice a b\ndelete alice alice gone\n"
	if string(data) != want {
		t.Fatalf("helper calls =\n%s\nwant\n%s", data, want)
	}
}
