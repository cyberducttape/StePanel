package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/sitefiles"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// --- Customer file manager (ADR-0007) ---

// FilesRequest runs one file manager operation inside a placed site's
// document root. The broker resolves the site's account user from the
// control-plane placement and stepanel-filectl runs the operation as that
// user, so a request can only do what the account's own SFTP login can.
type FilesRequest struct {
	Action        string    `json:"action"` // list, read, write, mkdir, rename, delete
	Site          string    `json:"site"`
	TenantID      string    `json:"tenant_id"`
	Path          string    `json:"path,omitempty"`
	Destination   string    `json:"destination,omitempty"`
	StreamContent bool      `json:"stream_content,omitempty"`
	ContentReader io.Reader `json:"-"`
}

// FilesResponse carries a listing or a file's bytes.
type FilesResponse struct {
	Listing *sitefiles.Listing `json:"listing,omitempty"`
	Content []byte             `json:"content,omitempty"`
}

const filectlPath = "/usr/local/sbin/stepanel-filectl"

// filesReadOnlyActions do not change the site and need no fencing token.
var filesReadOnlyActions = map[string]bool{"list": true, "read": true}

func (v *Validator) validateFilesRequest(req *FilesRequest) error {
	if req == nil {
		return errors.New("files request is required")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return err
	}
	if err := sitelayout.ValidUnixUser(req.TenantID); err != nil {
		return fmt.Errorf("invalid file-operation tenant: %w", err)
	}
	clean := sitefiles.Clean
	if !filesReadOnlyActions[req.Action] {
		clean = sitefiles.CleanTarget
	}
	switch req.Action {
	case "list", "read", "write", "mkdir", "rename", "delete":
	default:
		return fmt.Errorf("unsupported files action %q", req.Action)
	}
	if cleaned, err := clean(req.Path); err != nil {
		return err
	} else if cleaned != req.Path {
		return errors.New("file path is not in canonical form")
	}
	if req.Action == "rename" {
		if cleaned, err := sitefiles.CleanTarget(req.Destination); err != nil {
			return err
		} else if cleaned != req.Destination {
			return errors.New("destination path is not in canonical form")
		}
	} else if req.Destination != "" {
		return fmt.Errorf("files %s does not take a destination", req.Action)
	}
	if req.Action == "write" {
		if !req.StreamContent || req.ContentReader == nil {
			return errors.New("file writes require a streamed content body")
		}
	} else if req.StreamContent || req.ContentReader != nil {
		return fmt.Errorf("files %s does not take content", req.Action)
	}
	return nil
}

func (b *Broker) handleFilesRequest(ctx context.Context, req *FilesRequest) (*Response, error) {
	if b.fencingDB == nil {
		return &Response{OK: false, Error: "file operations require durable tenant ownership state"}, nil
	}
	var owner string
	if err := b.fencingDB.QueryRow(`SELECT username FROM tenant_sites WHERE site = ?`, req.Site).Scan(&owner); err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("resolve tenant owner for site %q: %v", req.Site, err)}, nil
	}
	if owner != req.TenantID {
		return &Response{OK: false, Error: fmt.Sprintf("tenant %q is not authorized for site %q", req.TenantID, req.Site)}, nil
	}
	user, placed, err := placedSiteUser(b.fencingDB, req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if !placed {
		return &Response{OK: false, Error: fmt.Sprintf("site %q is not placed in a hosting account", req.Site)}, nil
	}
	args := []string{req.Action, req.Site, user, req.Path}
	if req.Action == "rename" {
		args = append(args, req.Destination)
	}
	cmd := stepanelhelper.NewCommand(ctx, b.filectlPath(), args...)
	var staged *os.File
	if req.Action == "write" {
		staged, err = os.CreateTemp("", "stepanel-file-upload-*")
		if err != nil {
			return nil, fmt.Errorf("stage streamed file content: %w", err)
		}
		defer func() {
			name := staged.Name()
			_ = staged.Close()
			_ = os.Remove(name)
		}()
		written, copyErr := io.Copy(staged, io.LimitReader(req.ContentReader, sitefiles.MaxWriteBytes+1))
		if copyErr != nil {
			return &Response{OK: false, Error: "files write failed: incomplete streamed content"}, nil
		}
		if written > sitefiles.MaxWriteBytes {
			return &Response{OK: false, Error: fmt.Sprintf("files write failed: file is larger than %d bytes", sitefiles.MaxWriteBytes)}, nil
		}
		if _, err := staged.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind streamed file content: %w", err)
		}
		cmd.Stdin = staged
	}
	stdout, stderr, err := stepanelhelper.RunCappedSeparate(ctx, cmd, sitefiles.MaxReadBytes+1, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("files %s failed: %s", req.Action, filesHelperError(err, stderr))}, nil
	}
	var result FilesResponse
	switch req.Action {
	case "list":
		listing, err := sitefiles.ParseListing(req.Path, stdout)
		if err != nil {
			return &Response{OK: false, Error: err.Error()}, nil
		}
		result.Listing = &listing
	case "read":
		if len(stdout) > sitefiles.MaxReadBytes {
			return &Response{OK: false, Error: fmt.Sprintf("files read failed: file is larger than %d bytes", sitefiles.MaxReadBytes)}, nil
		}
		result.Content = stdout
	}
	details, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

// filesHelperError keeps the helper's own explanation, which is written for
// the customer ("path already exists"), and falls back to the exit status.
func filesHelperError(err error, stderr []byte) string {
	if message := strings.TrimSpace(string(stderr)); message != "" {
		if index := strings.LastIndexByte(message, '\n'); index >= 0 {
			message = message[index+1:]
		}
		return message
	}
	return err.Error()
}

func (b *Broker) filectlPath() string {
	if b.filectl != "" {
		return b.filectl
	}
	return filectlPath
}

// Files runs one file manager operation and decodes its result.
func (c *Client) Files(ctx context.Context, req FilesRequest) (FilesResponse, error) {
	resp, err := c.Execute(ctx, &Request{RequestType: "files", Files: &req})
	if err != nil {
		return FilesResponse{}, err
	}
	if !resp.OK {
		// The helper's explanation describes the customer's own files.
		if _, message, found := strings.Cut(resp.Error, "files "+req.Action+" failed: "); found {
			if message == "path does not exist" {
				return FilesResponse{}, sitefiles.ErrNotFound
			}
			return FilesResponse{}, sitefiles.Refused("%s", message)
		}
		return FilesResponse{}, fmt.Errorf("root broker rejected files %s: %s", req.Action, resp.Error)
	}
	var details FilesResponse
	if len(resp.Details) > 0 {
		if err := json.Unmarshal(resp.Details, &details); err != nil {
			return FilesResponse{}, fmt.Errorf("decode files %s response: %w", req.Action, err)
		}
	}
	return details, nil
}

// FilesWrite streams file bytes separately from the JSON request metadata.
// This avoids base64 expansion and whole-file copies in the panel and broker.
func (c *Client) FilesWrite(ctx context.Context, site, tenantID, rel string, content io.Reader) error {
	if content == nil {
		return errors.New("file content is required")
	}
	req := &Request{RequestType: "files", Files: &FilesRequest{
		Action: "write", Site: site, TenantID: tenantID, Path: rel, StreamContent: true,
	}}
	if tokens := operations.FencingTokens(ctx); len(tokens) > 0 {
		req.Fencing = tokens
	}
	resp, err := c.executeStream(ctx, req, content)
	if err != nil {
		if errors.Is(err, errFileStreamTooLarge) {
			return sitefiles.ErrTooLarge
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return sitefiles.ErrTooLarge
		}
		return err
	}
	if !resp.OK {
		if _, message, found := strings.Cut(resp.Error, "files write failed: "); found {
			if strings.Contains(message, "parent directory durability is uncertain") {
				return sitefiles.ErrDurabilityUncertain
			}
			if strings.Contains(message, "larger than") {
				return sitefiles.ErrTooLarge
			}
			return sitefiles.Refused("%s", message)
		}
		return fmt.Errorf("root broker rejected files write: %s", resp.Error)
	}
	return nil
}
