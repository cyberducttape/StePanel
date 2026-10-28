package stepanel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitefiles"
)

// Customer file manager (ADR-0007). Every operation is confined to one placed
// site's document root and, on a production host, runs as the hosting
// account's Unix user through the root broker and stepanel-filectl.
//
//	GET    /api/sites/files/{site}?path=P             list a directory
//	GET    /api/sites/files/{site}/content?path=P     read a file (JSON)
//	GET    /api/sites/files/{site}/content?path=P&download=1
//	PUT    /api/sites/files/{site}/content?path=P     replace a file (raw body)
//	POST   /api/sites/files/{site}/mkdir   {"path"}
//	POST   /api/sites/files/{site}/rename  {"path","destination"}
//	POST   /api/sites/files/{site}/delete  {"path"}

const siteFilesPrefix = "/api/sites/files/"

// siteFileWrites bounds concurrent file uploads so slow clients and helper
// processes cannot exhaust host resources.
var siteFileWrites = make(chan struct{}, 8)

// errSiteNotPlaced reports a legacy site, whose tree the file manager does
// not manage.
var errSiteNotPlaced = errors.New("the file manager is available for sites in a hosting account")

// brokerFileStore is the production sitefiles.Store.
type brokerFileStore struct{ cfg Config }

func (s brokerFileStore) run(ctx context.Context, req rootbroker.FilesRequest) (rootbroker.FilesResponse, error) {
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", s.cfg.WebRoot)
	if err != nil {
		return rootbroker.FilesResponse{}, err
	}
	return client.Files(ctx, req)
}

func (s brokerFileStore) List(ctx context.Context, site, rel string) (sitefiles.Listing, error) {
	resp, err := s.run(ctx, rootbroker.FilesRequest{Action: "list", Site: site, Path: rel})
	if err != nil {
		return sitefiles.Listing{}, err
	}
	if resp.Listing == nil {
		return sitefiles.Listing{}, errors.New("file listing is missing from the broker response")
	}
	return *resp.Listing, nil
}

func (s brokerFileStore) Read(ctx context.Context, site, rel string) ([]byte, error) {
	resp, err := s.run(ctx, rootbroker.FilesRequest{Action: "read", Site: site, Path: rel})
	return resp.Content, err
}

func (s brokerFileStore) Write(ctx context.Context, site, rel string, content io.Reader) error {
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", s.cfg.WebRoot)
	if err != nil {
		return err
	}
	return client.FilesWrite(ctx, site, rel, content)
}

func (s brokerFileStore) Mkdir(ctx context.Context, site, rel string) error {
	_, err := s.run(ctx, rootbroker.FilesRequest{Action: "mkdir", Site: site, Path: rel})
	return err
}

func (s brokerFileStore) Rename(ctx context.Context, site, rel, destination string) error {
	_, err := s.run(ctx, rootbroker.FilesRequest{Action: "rename", Site: site, Path: rel, Destination: destination})
	return err
}

func (s brokerFileStore) Delete(ctx context.Context, site, rel string) error {
	_, err := s.run(ctx, rootbroker.FilesRequest{Action: "delete", Site: site, Path: rel})
	return err
}

// siteFileStore returns the store for this host: the broker on production
// (and lab broker) hosts, the document root itself in development.
func (a *App) siteFileStore() sitefiles.Store {
	if a.Config.Production || labDirectRootBrokerEnabled() {
		return brokerFileStore{cfg: a.Config}
	}
	cfg := a.Config
	return sitefiles.Local{DocRoot: func(site string) (string, error) {
		location, err := siteLocationFor(cfg, site)
		if err != nil {
			return "", err
		}
		if !location.Placed {
			return "", errSiteNotPlaced
		}
		return existingRealDirectory(location.DocRoot, "site document root")
	}}
}

func (a *App) siteFiles(w http.ResponseWriter, r *http.Request) {
	site, operation, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, siteFilesPrefix), "/")
	if safeUser(site) != site || site == "" {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	location, err := siteLocationFor(a.Config, site)
	if err != nil {
		http.Error(w, "site location is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !location.Placed {
		http.Error(w, errSiteNotPlaced.Error(), http.StatusConflict)
		return
	}
	store := a.siteFileStore()
	switch {
	case operation == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		a.siteFilesList(w, r, store, site)
	case operation == "content" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		a.siteFilesRead(w, r, store, site)
	case operation == "content" && r.Method == http.MethodPut:
		rel, err := sitefiles.CleanTarget(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		select {
		case siteFileWrites <- struct{}{}:
			defer func() { <-siteFileWrites }()
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many file uploads are in progress; try again shortly", http.StatusTooManyRequests)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, sitefiles.MaxWriteBytes)
		a.siteFilesMutate(w, r, site, "site.files.write", rel+" (streamed upload)", func(ctx context.Context) error {
			return store.Write(ctx, site, rel, r.Body)
		})
	case r.Method == http.MethodPost && (operation == "mkdir" || operation == "rename" || operation == "delete"):
		var input struct {
			Path        string `json:"path"`
			Destination string `json:"destination"`
		}
		if err := decodeJSON(w, r, 16<<10, &input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		rel, err := sitefiles.CleanTarget(input.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		switch operation {
		case "mkdir":
			a.siteFilesMutate(w, r, site, "site.files.mkdir", rel, func(ctx context.Context) error { return store.Mkdir(ctx, site, rel) })
		case "delete":
			a.siteFilesMutate(w, r, site, "site.files.delete", rel, func(ctx context.Context) error { return store.Delete(ctx, site, rel) })
		case "rename":
			destination, err := sitefiles.CleanTarget(input.Destination)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			a.siteFilesMutate(w, r, site, "site.files.rename", rel+" -> "+destination, func(ctx context.Context) error {
				return store.Rename(ctx, site, rel, destination)
			})
		}
	default:
		http.Error(w, "unsupported file manager operation", http.StatusMethodNotAllowed)
	}
}

func (a *App) siteFilesList(w http.ResponseWriter, r *http.Request, store sitefiles.Store, site string) {
	if !a.requireCustomerScope(w, r, "files:read") {
		return
	}
	rel, err := sitefiles.Clean(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	listing, err := store.List(r.Context(), site, rel)
	if err != nil {
		writeSiteFilesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (a *App) siteFilesRead(w http.ResponseWriter, r *http.Request, store sitefiles.Store, site string) {
	if !a.requireCustomerScope(w, r, "files:read") {
		return
	}
	rel, err := sitefiles.CleanTarget(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	data, err := store.Read(r.Context(), site, rel)
	if err != nil {
		writeSiteFilesError(w, err)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		// Never rendered by the browser: the panel origin must not serve
		// customer HTML or scripts.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(rel)))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			if _, err := w.Write(data); err != nil {
				return
			}
		}
		return
	}
	text := utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
	result := map[string]any{"path": rel, "size": len(data), "text": text}
	if text {
		result["content"] = string(data)
	}
	writeJSON(w, http.StatusOK, result)
}

// siteFilesMutate runs one file change under the site's mutation lease (the
// broker requires its fencing token) with a Class A audit record.
func (a *App) siteFilesMutate(w http.ResponseWriter, r *http.Request, site, action, detail string, change func(context.Context) error) {
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if !a.requireCustomerScope(w, r, "files:write") {
		return
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(r.Context(), site)
	if err != nil {
		http.Error(w, "the site has another change in progress; try again shortly", http.StatusConflict)
		return
	}
	defer release()
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), action, site, detail)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	if err := change(operationCtx); err != nil {
		intent.Failed(err.Error())
		writeSiteFilesError(w, err)
		return
	}
	if err := operationCtx.Err(); err != nil {
		intent.Failed("site mutation lease lost")
		http.Error(w, "the change was interrupted because the site lock was lost; reload to check its state", http.StatusConflict)
		return
	}
	intent.Completed(detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeSiteFilesError shows the customer why an operation was refused. Only
// refusals about the customer's own files carry their message; anything else
// is a host failure and stays generic.
func writeSiteFilesError(w http.ResponseWriter, err error) {
	var refused *sitefiles.RefusedError
	switch {
	case errors.Is(err, sitefiles.ErrNotFound):
		http.Error(w, "the file or directory does not exist", http.StatusNotFound)
	case errors.Is(err, errSiteNotPlaced):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &refused):
		http.Error(w, refused.Message, http.StatusUnprocessableEntity)
	case errors.Is(err, sitefiles.ErrTooLarge):
		http.Error(w, fmt.Sprintf("file content must be at most %d bytes", sitefiles.MaxWriteBytes), http.StatusRequestEntityTooLarge)
	case errors.Is(err, sitefiles.ErrDurabilityUncertain):
		http.Error(w, "the file was replaced, but crash durability could not be confirmed; check its contents before retrying", http.StatusServiceUnavailable)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "the file operation was interrupted", http.StatusServiceUnavailable)
	default:
		log.Printf("site file operation failed: %v", err)
		http.Error(w, "the file operation failed", http.StatusBadGateway)
	}
}
