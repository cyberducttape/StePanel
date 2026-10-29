package stepanel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/archivesafe"
	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

type CPMoveInfo struct {
	Archive           string   `json:"archive"`
	UploadID          string   `json:"upload_id,omitempty"`
	ArchiveBytes      int64    `json:"archive_bytes"`
	ExpandedBytes     int64    `json:"expanded_bytes"`
	DatabaseBytes     int64    `json:"database_bytes"`
	RequiredFreeBytes int64    `json:"required_free_bytes"`
	Entries           int      `json:"entries"`
	User              string   `json:"detected_user"`
	HasHome           bool     `json:"has_home"`
	HasMySQL          bool     `json:"has_mysql"`
	HasMail           bool     `json:"has_mail"`
	Mailboxes         []string `json:"mailboxes,omitempty"`
	DNSZones          []string `json:"dns_zones,omitempty"`
	CronSchedules     []string `json:"cron_schedules,omitempty"`
	DNSZoneOmitted    int      `json:"dns_zone_omitted_count,omitempty"`
	CronOmitted       int      `json:"cron_omitted_count,omitempty"`
	Databases         []string `json:"databases"`
}

// CPMoveInspectionResponse contains only metadata needed by the import form.
// Archive-derived names and paths remain server-side so they cannot become
// browser-controlled markup through the inspection response.
type CPMoveInspectionResponse struct {
	UploadID          string                      `json:"upload_id,omitempty"`
	ArchiveBytes      int64                       `json:"archive_bytes"`
	ExpandedBytes     int64                       `json:"expanded_bytes"`
	DatabaseBytes     int64                       `json:"database_bytes"`
	RequiredFreeBytes int64                       `json:"required_free_bytes"`
	Entries           int                         `json:"entries"`
	HasHome           bool                        `json:"has_home"`
	HasMySQL          bool                        `json:"has_mysql"`
	HasMail           bool                        `json:"has_mail"`
	DatabaseCount     int                         `json:"database_count"`
	MailboxCount      int                         `json:"mailbox_count"`
	DNSZoneCount      int                         `json:"dns_zone_count"`
	CronJobCount      int                         `json:"cron_job_count"`
	Components        []MigrationPreviewComponent `json:"components"`
	Compatibility     []MigrationCheck            `json:"compatibility"`
}

func (info CPMoveInfo) response(uploadID string) CPMoveInspectionResponse {
	response := CPMoveInspectionResponse{
		UploadID:          uploadID,
		ArchiveBytes:      info.ArchiveBytes,
		ExpandedBytes:     info.ExpandedBytes,
		DatabaseBytes:     info.DatabaseBytes,
		RequiredFreeBytes: info.RequiredFreeBytes,
		Entries:           info.Entries,
		HasHome:           info.HasHome,
		HasMySQL:          info.HasMySQL,
		HasMail:           info.HasMail,
		DatabaseCount:     len(info.Databases),
		MailboxCount:      len(info.Mailboxes),
		DNSZoneCount:      len(info.DNSZones) + info.DNSZoneOmitted,
		CronJobCount:      len(info.CronSchedules) + info.CronOmitted,
	}
	response.Components = previewCPMoveComponents(info)
	return response
}

func previewCPMoveComponents(info CPMoveInfo) []MigrationPreviewComponent {
	var components []MigrationPreviewComponent
	if info.HasHome {
		components = append(components, MigrationPreviewComponent{Category: "website_files", Source: "cPanel home directory", PlannedDisposition: MigrationImported, Details: "Public document roots are scanned and activated into isolated site directories."})
	} else {
		components = append(components, MigrationPreviewComponent{Category: "website_files", Source: "website files", PlannedDisposition: MigrationSkipped, Details: "No home directory entries were found."})
	}
	for _, name := range info.Databases {
		components = append(components, MigrationPreviewComponent{Category: "databases", Source: name, PlannedDisposition: MigrationTransformed, Details: "Database names and credentials are mapped to destination-managed identities; import is optional."})
	}
	for _, name := range info.Mailboxes {
		components = append(components, MigrationPreviewComponent{Category: "mailboxes", Source: name, PlannedDisposition: MigrationTransformed, Details: "Mailbox files are staged privately; delivery, credentials, aliases, and MX are not activated."})
	}
	for _, name := range info.DNSZones {
		components = append(components, MigrationPreviewComponent{Category: "dns", Source: name, PlannedDisposition: MigrationUnsupported, Details: "DNS zone records are not imported and require provider-side cutover review."})
	}
	for _, schedule := range info.CronSchedules {
		components = append(components, MigrationPreviewComponent{Category: "cron_jobs", Source: schedule, PlannedDisposition: MigrationUnsupported, Details: "Cron commands are intentionally not imported; review them and recreate explicitly."})
	}
	if info.DNSZoneOmitted > 0 || info.CronOmitted > 0 {
		components = append(components, MigrationPreviewComponent{Category: "archive_inventory", Source: "additional inventory entries", PlannedDisposition: MigrationSkipped, Details: "The inspection report bounds displayed items; omitted counts are shown in the summary."})
	}
	return components
}

func cpmoveCompatibilityChecks(cfg Config, info CPMoveInfo) []MigrationCheck {
	checks := []MigrationCheck{{Name: "Archive structure", Status: "passed", Details: fmt.Sprintf("Validated %d archive entries and %s expanded content against archive path and type policies.", info.Entries, humanBytes(info.ExpandedBytes))}}
	if mysqlCompatible(cfg) {
		checks = append(checks, MigrationCheck{Name: "Database engine", Status: "passed", Details: "Destination supports MySQL/MariaDB cPanel dumps; each database is still validated during import."})
	} else if len(info.Databases) > 0 {
		checks = append(checks, MigrationCheck{Name: "Database engine", Status: "blocked", Details: "Destination database engine cannot import cPanel MySQL/MariaDB dumps. Website files may be imported without databases."})
	} else {
		checks = append(checks, MigrationCheck{Name: "Database engine", Status: "unknown", Details: "No database dumps were found in the archive."})
	}
	checks = append(checks,
		MigrationCheck{Name: "Application compatibility", Status: "unknown", Details: "Source PHP version and required extensions are not declared reliably in cpmove archives; the post-import rehearsal determines runtime compatibility when supported."},
		MigrationCheck{Name: "DNS and scheduled tasks", Status: "warning", Details: "DNS records and cron commands are not activated by import; the report includes manual cutover dispositions."},
		MigrationCheck{Name: "Mail service", Status: "warning", Details: "Mailbox data can be staged, but mail identities, delivery configuration, aliases, and DNS cutover are not migrated."},
	)
	if info.RequiredFreeBytes > 0 {
		checks = append(checks, MigrationCheck{Name: "Storage admission", Status: "passed", Details: fmt.Sprintf("Current archive staging admission passed at an estimated %s; space is checked again immediately before import.", humanBytes(info.RequiredFreeBytes))})
	}
	return checks
}

func applyRehearsalCompatibility(checks []MigrationCheck, rehearsal *MigrationRehearsal) {
	if rehearsal == nil {
		return
	}
	for i := range checks {
		if checks[i].Name != "Application compatibility" {
			continue
		}
		switch rehearsal.Status {
		case "passed":
			checks[i].Status, checks[i].Details = "passed", "Disposable WordPress database restore and application HTTP checks passed."
		case "failed":
			checks[i].Status, checks[i].Details = "blocked", "The disposable WordPress application rehearsal failed; inspect the report and job diagnostics."
		case "unsupported":
			checks[i].Status, checks[i].Details = "warning", rehearsal.Details
		case "skipped":
			checks[i].Status, checks[i].Details = "unknown", rehearsal.Details
		}
		return
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

const maxCPMoveExpandedBytes int64 = 80 << 30
const maxCPMoveUploadMetadataBytes = 16 << 10
const maxCPMoveCronFileBytes = 1 << 20
const maxCPMoveCronInventoryBytes = 8 << 20

// randomSecret uses base64.RawURLEncoding, whose first character may be '-'
// or '_' as well as an alphanumeric. Keep the full URL-safe alphabet valid.
var cpmoveUploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type cpmoveUpload struct {
	ID            string    `json:"id"`
	Path          string    `json:"path"`
	Filename      string    `json:"filename"`
	Size          int64     `json:"size"`
	ExpandedBytes int64     `json:"expanded_bytes"`
	DatabaseBytes int64     `json:"database_bytes"`
	SHA256        string    `json:"sha256"`
	Owner         string    `json:"owner"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func cpmoveUploadPath(root, id string) string {
	return filepath.Join(root, "upload-"+id+".tar.gz")
}

func cpmoveUploadMetadataPath(root, id string) string {
	return filepath.Join(root, "upload-"+id+".json")
}

func readCPMoveUpload(root, id string) (cpmoveUpload, error) {
	if !cpmoveUploadIDPattern.MatchString(id) {
		return cpmoveUpload{}, errors.New("invalid upload ID")
	}
	data, err := readStateFile(cpmoveUploadMetadataPath(root, id), maxCPMoveUploadMetadataBytes)
	if err != nil {
		return cpmoveUpload{}, err
	}
	var upload cpmoveUpload
	if err := json.Unmarshal(data, &upload); err != nil || upload.ID != id || upload.Owner == "" || upload.Size < 0 || upload.ExpandedBytes < 0 || upload.DatabaseBytes < 0 || len(upload.SHA256) != 2*sha256.Size {
		return cpmoveUpload{}, errors.New("invalid upload metadata")
	}
	if _, err := hex.DecodeString(upload.SHA256); err != nil {
		return cpmoveUpload{}, errors.New("invalid upload checksum")
	}
	if time.Now().After(upload.ExpiresAt) {
		return cpmoveUpload{}, errors.New("upload has expired")
	}
	if upload.Path != cpmoveUploadPath(root, id) || ensureInside(root, upload.Path) != nil {
		return cpmoveUpload{}, errors.New("invalid upload path")
	}
	return upload, nil
}

// verifyCPMoveUpload revalidates the durable upload immediately before a
// restore. Inspection happens in the HTTP request, but the worker may execute
// much later; metadata and byte length alone are not sufficient to prove that
// the staged archive is still the one that was inspected.
func verifyCPMoveUpload(upload cpmoveUpload) error {
	info, err := os.Stat(upload.Path)
	if err != nil {
		return fmt.Errorf("inspect staged cpmove archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != upload.Size {
		return errors.New("staged cpmove archive size does not match upload metadata")
	}
	checksum, err := fileSHA256(upload.Path)
	if err != nil {
		return fmt.Errorf("hash staged cpmove archive: %w", err)
	}
	if checksum != upload.SHA256 {
		return errors.New("staged cpmove archive checksum does not match upload metadata")
	}
	return nil
}

type cpmovePathInfo struct {
	Path string
	User string
}

type ImportResult struct {
	User              string   `json:"user"`
	Home              string   `json:"home"`
	FilesRestored     bool     `json:"files_restored"`
	DatabasesRestored []string `json:"databases_restored"`
	DatabaseErrors    []string `json:"database_errors,omitempty"`
	MailStaged        bool     `json:"mail_staged"`
	MailboxesStaged   []string `json:"mailboxes_staged,omitempty"`
	MailErrors        []string `json:"mail_errors,omitempty"`
	StagedAt          string   `json:"staged_at"`
	// Account and the domain fields describe a restore into a hosting
	// account (ADR-0007): the main domain is served from public_html, addon
	// and subdomains become their own sites beside it, and parked domains
	// are aliases of the main site waiting for a route.
	Account                  string               `json:"account,omitempty"`
	MainDomain               string               `json:"main_domain,omitempty"`
	ParkedDomains            []string             `json:"parked_domains,omitempty"`
	Domains                  []ImportedDomain     `json:"domains,omitempty"`
	DomainErrors             []string             `json:"domain_errors,omitempty"`
	Evidence                 []MigrationEvidence  `json:"migration_evidence,omitempty"`
	Components               []MigrationComponent `json:"components,omitempty"`
	Compatibility            []MigrationCheck     `json:"compatibility,omitempty"`
	Rehearsal                *MigrationRehearsal  `json:"application_rehearsal,omitempty"`
	ReportCreatedAt          time.Time            `json:"report_created_at,omitempty"`
	SourceArchive            string               `json:"source_archive,omitempty"`
	ArchiveSHA256            string               `json:"archive_sha256,omitempty"`
	ArchiveBytes             int64                `json:"archive_bytes,omitempty"`
	ExpandedBytes            int64                `json:"expanded_bytes,omitempty"`
	StartedAt                time.Time            `json:"started_at,omitempty"`
	FinishedAt               time.Time            `json:"finished_at,omitempty"`
	DatabaseRestoreRequested bool                 `json:"database_restore_requested"`
}

// MigrationDisposition is the final operator-facing outcome for one source
// component. Planned actions are represented separately in preview responses.
type MigrationDisposition string

const (
	MigrationImported    MigrationDisposition = "imported"
	MigrationTransformed MigrationDisposition = "transformed"
	MigrationSkipped     MigrationDisposition = "skipped"
	MigrationUnsupported MigrationDisposition = "unsupported"
	MigrationFailed      MigrationDisposition = "failed"
)

type MigrationComponent struct {
	Category    string               `json:"category"`
	Source      string               `json:"source"`
	Target      string               `json:"target,omitempty"`
	Disposition MigrationDisposition `json:"disposition"`
	Details     string               `json:"details,omitempty"`
}

type MigrationPreviewComponent struct {
	Category           string               `json:"category"`
	Source             string               `json:"source"`
	PlannedDisposition MigrationDisposition `json:"planned_disposition"`
	Details            string               `json:"details,omitempty"`
}

type MigrationCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // passed, warning, blocked, unknown
	Details string `json:"details"`
}

type MigrationRehearsal struct {
	Status      string    `json:"status"` // passed, unsupported, skipped, failed
	CompletedAt time.Time `json:"completed_at,omitempty"`
	DurationMS  int64     `json:"duration_ms,omitempty"`
	Checks      []string  `json:"checks,omitempty"`
	Details     string    `json:"details,omitempty"`
}

// MigrationEvidence preserves the category-oriented report for API clients.
// Components is the authoritative item-by-item disposition inventory.
type MigrationEvidence struct {
	Category         string               `json:"category"`
	Status           string               `json:"status"` // completed, not_selected, not_inspected
	Disposition      MigrationDisposition `json:"disposition,omitempty"`
	SourceItems      []string             `json:"source_items,omitempty"`
	ImportedItems    []string             `json:"imported_items,omitempty"`
	SkippedItems     []string             `json:"skipped_items,omitempty"`
	OmittedItemCount int                  `json:"omitted_item_count,omitempty"`
	Details          string               `json:"details,omitempty"`
}

const maxMigrationEvidenceItems = 500

// ImportedDomain is an addon or subdomain restored as its own site.
type ImportedDomain struct {
	Domain       string `json:"domain"`
	Site         string `json:"site"`
	Kind         string `json:"kind"`
	DocumentRoot string `json:"document_root"`
}

func InspectCPMove(file multipart.File, header *multipart.FileHeader) (CPMoveInfo, error) {
	return inspectCPMove(file, header, 1000000)
}

func inspectCPMove(file multipart.File, header *multipart.FileHeader, maxEntries int) (CPMoveInfo, error) {
	if header == nil {
		return CPMoveInfo{}, errors.New("backup file metadata is missing")
	}
	if header.Size > 20<<30 {
		return CPMoveInfo{}, errors.New("backup exceeds the 20 GiB upload limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return CPMoveInfo{}, err
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		return CPMoveInfo{}, errors.New("backup is not a valid gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	// The filename is returned in the API inspection response. Escape it at
	// the trust boundary so a hostile multipart filename cannot become markup
	// if a client renders the response in a browser.
	info := CPMoveInfo{Archive: html.EscapeString(header.Filename), ArchiveBytes: header.Size, User: html.EscapeString(userFromArchiveName(header.Filename))}
	seen := map[string]bool{}
	seenMail := map[string]bool{}
	seenZones := map[string]bool{}
	var cronInventoryBytes int64
	paths := archivesafe.NewPathSet()
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, errors.New("backup tar stream is damaged")
		}
		if h.Size < 0 || h.Size > 2<<30 || total > maxCPMoveExpandedBytes-h.Size {
			return info, errors.New("archive contents exceed the 80 GiB inspection limit")
		}
		total += h.Size
		if !safeArchivePath(h.Name) {
			return info, fmt.Errorf("unsafe archive path: %s", h.Name)
		}
		// Apply the extractor's rules here so inspection rejects what
		// restore would: unsupported entry types and colliding paths.
		kind, kindErr := archivesafe.TarKind(h)
		if kindErr != nil {
			return info, fmt.Errorf("unsupported archive entry type: %w", kindErr)
		}
		if _, claimErr := paths.Claim(h.Name, kind); claimErr != nil {
			return info, fmt.Errorf("unsafe archive path: %w", claimErr)
		}
		info.Entries++
		if maxEntries > 0 && info.Entries > maxEntries {
			return info, errors.New("archive contains too many entries")
		}
		entry := normalizeCPMovePath(h.Name)
		name := entry.Path
		if info.User == "" && entry.User != "" {
			info.User = html.EscapeString(entry.User)
		}
		parts := strings.Split(name, "/")
		if len(parts) > 1 && parts[0] == "homedir" {
			info.HasHome = true
		}
		if strings.HasPrefix(name, "mysql/") {
			info.HasMySQL = true
			if strings.HasSuffix(name, ".sql") {
				// The dumps bound the database server's data growth; the
				// import admission check reserves for it.
				info.DatabaseBytes += h.Size
				db := html.EscapeString(strings.TrimSuffix(filepath.Base(name), ".sql"))
				if !seen[db] {
					info.Databases = append(info.Databases, db)
					seen[db] = true
				}
			}
		}
		if strings.HasPrefix(name, "homedir/mail/") {
			info.HasMail = true
			if len(parts) >= 4 {
				mailbox := html.EscapeString(parts[2] + "/" + parts[3])
				if !seenMail[mailbox] {
					info.Mailboxes = append(info.Mailboxes, mailbox)
					seenMail[mailbox] = true
				}
			}
		}
		if strings.HasPrefix(name, "dnszones/") && strings.HasSuffix(strings.ToLower(name), ".db") {
			zone := filepath.Base(name)
			zone = strings.ToLower(strings.TrimSuffix(zone, filepath.Ext(zone)))
			// Zone names come from archive file names; report only valid
			// domain names so the evidence never echoes arbitrary text.
			if domainname.Validate(zone) == nil && !seenZones[zone] {
				if len(info.DNSZones) < maxMigrationEvidenceItems {
					info.DNSZones = append(info.DNSZones, zone)
				} else {
					info.DNSZoneOmitted++
				}
				seenZones[zone] = true
			}
		}
		if strings.HasPrefix(name, "cron/") && kind == archivesafe.File {
			if h.Size > maxCPMoveCronFileBytes {
				return info, errors.New("cPanel cron inventory exceeds the 1 MiB per-file inspection limit")
			}
			if cronInventoryBytes > maxCPMoveCronInventoryBytes-h.Size {
				return info, errors.New("cPanel cron inventory exceeds the 8 MiB total inspection limit")
			}
			cronInventoryBytes += h.Size
			data, readErr := io.ReadAll(io.LimitReader(tr, maxCPMoveCronFileBytes+1))
			if readErr != nil || int64(len(data)) > maxCPMoveCronFileBytes {
				return info, errors.New("could not safely inspect cPanel cron inventory")
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				// Record only the schedule. Commands can contain secrets and are
				// not imported by this workflow.
				schedule, ok := cronSchedule(line)
				if !ok {
					continue
				}
				if len(info.CronSchedules) < maxMigrationEvidenceItems {
					info.CronSchedules = append(info.CronSchedules, schedule)
				} else {
					info.CronOmitted++
				}
			}
		}
	}
	sort.Strings(info.Databases)
	sort.Strings(info.Mailboxes)
	sort.Strings(info.DNSZones)
	sort.Strings(info.CronSchedules)
	info.ExpandedBytes = total
	if info.Entries == 0 {
		return info, errors.New("backup archive is empty")
	}
	return info, nil
}

func RestoreCPMove(cfg Config, file multipart.File, header *multipart.FileHeader, site SiteCapability, databases bool, ledgers ...*capacityLedger) (ImportResult, error) {
	return RestoreCPMoveContext(context.Background(), cfg, file, header, site, databases, ledgers...)
}

func RestoreCPMoveContext(ctx context.Context, cfg Config, file multipart.File, header *multipart.FileHeader, site SiteCapability, databases bool, ledgers ...*capacityLedger) (ImportResult, error) {
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}
	if err := os.MkdirAll(cfg.ImportRoot, 0700); err != nil {
		return ImportResult{}, err
	}
	temp, err := os.CreateTemp(cfg.ImportRoot, "restore-upload-*.tar.gz")
	if err != nil {
		return ImportResult{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		temp.Close()
		return ImportResult{}, err
	}
	if _, err := io.Copy(temp, file); err != nil {
		temp.Close()
		return ImportResult{}, err
	}
	if err := temp.Close(); err != nil {
		return ImportResult{}, err
	}
	return restoreCPMoveArchiveContext(ctx, cfg, tempPath, header, site, databases, ledgers...)
}

// restoreCPMoveArchiveContext restores from an immutable archive already held
// under ImportRoot. Keeping the durable upload as the extraction source avoids
// copying a large archive into every restore stage.
func restoreCPMoveArchiveContext(ctx context.Context, cfg Config, archive string, header *multipart.FileHeader, site SiteCapability, databases bool, ledgers ...*capacityLedger) (ImportResult, error) {
	return restoreCPMoveArchiveContextWithApp(ctx, cfg, archive, header, site, databases, nil, ledgers...)
}

func restoreCPMoveArchiveContextWithApp(ctx context.Context, cfg Config, archive string, header *multipart.FileHeader, site SiteCapability, databases bool, app *App, ledgers ...*capacityLedger) (ImportResult, error) {
	if databases {
		var ledger *capacityLedger
		if len(ledgers) > 0 {
			ledger = ledgers[0]
		}
		if err := requireDatabaseRestoreAdmission(cfg, ledger); err != nil {
			return ImportResult{}, err
		}
	}
	if ensureInside(cfg.ImportRoot, archive) != nil {
		return ImportResult{}, errors.New("archive is outside the import root")
	}
	input, _, err := openRegularNoFollow(archive, nil)
	if err != nil {
		return ImportResult{}, err
	}
	archiveInfo, err := inspectCPMove(input, header, cfg.MaxEntries)
	if err != nil {
		input.Close()
		return ImportResult{}, err
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		input.Close()
		return ImportResult{}, err
	}
	randomID, err := randomSecret()
	if err != nil {
		input.Close()
		return ImportResult{}, fmt.Errorf("create import staging ID: %w", err)
	}
	user := site.Site()
	id := time.Now().UTC().Format("20060102-150405") + "-" + user + "-" + randomID[:12]
	stage, stageLock, err := createNamedImportStage(cfg.ImportRoot, id)
	if err != nil {
		input.Close()
		return ImportResult{}, err
	}
	defer func() {
		if err := releaseBackupStageLock(stageLock); err != nil {
			log.Printf("release cPanel import stage lease for %s: %v", id, err)
		}
	}()
	if err = extractArchiveReaderContext(ctx, input, stage, archive); err != nil {
		_ = input.Close()
		return ImportResult{}, err
	}
	if err = input.Close(); err != nil {
		return ImportResult{}, fmt.Errorf("close verified cpmove archive: %w", err)
	}
	root, err := cpmoveRoot(stage)
	if err != nil {
		return ImportResult{}, err
	}
	// Restore into a hosting account named after the cPanel user (ADR-0007):
	// the main domain is served from /home/<user>/public_html and addon and
	// subdomains become their own sites beside it. A site that already
	// exists in the legacy layout keeps it.
	db := sitePlacementDB.Load()
	_, legacyErr := existingManagedSiteRoot(cfg.WebRoot, user)
	intoAccount := db != nil && legacyErr != nil
	var plan cpmoveDomainPlan
	var domainInventoryErr error
	placedByJob := false
	if intoAccount {
		if plan, err = planCPMoveDomains(root, user); err != nil {
			return ImportResult{}, fmt.Errorf("read the backup's domains: %w", err)
		}
		main := sitelayout.Placement{Site: user, Account: user, UnixUser: user, Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory}
		if placedByJob, err = placeSiteInAccount(ctx, cfg, db, main); err != nil {
			return ImportResult{}, fmt.Errorf("place the cPanel account %s in a hosting account: %w", user, err)
		}
	}
	importCommitted := false
	defer func() {
		if placedByJob && !importCommitted {
			if err := unplaceSite(context.WithoutCancel(ctx), cfg, db, user); err != nil {
				log.Printf("forget placement of failed cpmove restore %s: %v", user, err)
			}
		}
	}()
	if !intoAccount {
		plan, domainInventoryErr = planCPMoveDomains(root, user)
	}
	home, err := siteDocRoot(cfg, user)
	if err != nil {
		return ImportResult{}, err
	}
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		return ImportResult{}, fmt.Errorf("initialize site manager: %w", err)
	}
	managerStage, stagingOwner, err := manager.CreateSiteStagingLease(ctx, user, ".stepanel-cpmove-")
	if err != nil {
		return ImportResult{}, fmt.Errorf("create site manager staging tree: %w", err)
	}
	defer func() {
		if err := manager.ReleaseStagingLease(managerStage, stagingOwner); err != nil {
			log.Printf("release cpmove staging lease for %s: %v", user, err)
		}
	}()
	activated := false
	defer func() {
		if !activated {
			logStagingDiscardFailure(managerStage, manager.DiscardStaging(context.Background(), managerStage))
		}
	}()
	txn, err := beginManagedSiteTransaction(ctx, cfg, home, "cpmove.restore", site)
	if err != nil {
		return ImportResult{}, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// The operation context may already be cancelled; keep its lease
		// token so the broker accepts the cleanup.
		if err := txn.cleanupDatabases(context.WithoutCancel(ctx), cfg); err != nil {
			log.Printf("defer cpmove database recovery for transaction %s: %v", txn.ID, err)
		}
		if err := txn.Rollback(); err != nil {
			log.Printf("defer cpmove filesystem recovery for transaction %s: %v", txn.ID, err)
		}
		if txn.HadExisting {
			if err := siteHelperContext(context.WithoutCancel(ctx), cfg, "seal", user); err != nil {
				log.Printf("defer cpmove site sealing for transaction %s: %v", txn.ID, err)
			}
		}
	}()
	if err := siteHelperContext(ctx, cfg, "prepare", user); err != nil {
		return ImportResult{}, fmt.Errorf("prepare isolated site: %w", err)
	}
	source := firstExisting(filepath.Join(root, "homedir", "public_html"), filepath.Join(root, "homedir", user, "public_html"))
	if source != "" {
		if err := ctx.Err(); err != nil {
			return ImportResult{}, err
		}
		if findings, scanErr := scanPHP(source); scanErr != nil {
			return ImportResult{}, fmt.Errorf("suspicious PHP heuristic scan failed: %w", scanErr)
		} else if len(findings) > 0 {
			return ImportResult{}, fmt.Errorf("restore blocked: suspicious PHP heuristic scan found %d file(s) requiring review", len(findings))
		}
		if err = copyTreeExcludingContext(ctx, source, managerStage, plan.MainExcludes); err != nil {
			return ImportResult{}, err
		}
	}
	if _, err := manager.ActivateStaged(ctx, user, managerStage); err != nil {
		return ImportResult{}, fmt.Errorf("activate cpmove site through manager: %w", err)
	}
	activated = true
	// The transaction journal is already durable before activation.  Keep a
	// process-death boundary here so disposable-host recovery drills can kill
	// the worker after the canonical swap and verify that startup rolls the
	// interrupted import back safely.
	processKillInjection("cpmove", "activate")

	result := ImportResult{User: user, Home: home, FilesRestored: source != "", StagedAt: stage}
	if databases {
		result.DatabasesRestored, result.DatabaseErrors = restoreSQLContext(ctx, cfg, root, user, txn, ledgers...)
		if len(result.DatabaseErrors) > 0 {
			return result, fmt.Errorf("database restore completed with %d error(s)", len(result.DatabaseErrors))
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if cfg.MailRoot != "" {
		mailSource := filepath.Join(root, "homedir", "mail")
		if info, statErr := os.Stat(mailSource); statErr == nil && info.IsDir() {
			txn.MailRoot, err = safePath(cfg.MailRoot, user)
			if err != nil {
				return result, fmt.Errorf("resolve destination mail root: %w", err)
			}
			txn.MailBackup = filepath.Join(txn.dir, "mail-before")
			if existing, destinationErr := os.Lstat(txn.MailRoot); destinationErr == nil {
				if existing.Mode()&os.ModeSymlink != 0 {
					return result, errors.New("destination mail root is a symlink")
				}
				txn.MailExisting = true
				if err := txn.persist(); err != nil {
					return result, fmt.Errorf("record mail recovery state: %w", err)
				}
				if err := os.Rename(txn.MailRoot, txn.MailBackup); err != nil {
					return result, fmt.Errorf("snapshot existing mail: %w", err)
				}
			} else if !errors.Is(destinationErr, os.ErrNotExist) {
				return result, fmt.Errorf("inspect destination mail root: %w", destinationErr)
			} else if err := txn.persist(); err != nil {
				return result, fmt.Errorf("record mail recovery state: %w", err)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return result, fmt.Errorf("inspect staged mail: %w", statErr)
		}
	}
	result.MailStaged, result.MailboxesStaged, result.MailErrors = restoreMailContext(ctx, cfg, root, user)
	if len(result.MailErrors) > 0 {
		return result, fmt.Errorf("mail restore completed with %d error(s)", len(result.MailErrors))
	}
	if err := siteHelperContext(ctx, cfg, "seal", user); err != nil {
		return result, fmt.Errorf("seal isolated site: %w", err)
	}
	if err := txn.Commit(); err != nil {
		return result, fmt.Errorf("commit site recovery transaction: %w", err)
	}
	committed = true
	importCommitted = true
	if intoAccount {
		result.Account, result.MainDomain, result.ParkedDomains = user, plan.MainDomain, plan.Parked
		result.Domains, result.DomainErrors = restoreCPMoveDomainSites(ctx, cfg, db, manager, user, plan)
	}
	result.Evidence = buildCPMoveMigrationEvidence(archiveInfo, result, databases, plan, domainInventoryErr)
	result.Compatibility = cpmoveCompatibilityChecks(cfg, archiveInfo)
	result.ReportCreatedAt = time.Now().UTC()
	if app != nil && wordpressProofEnabled(cfg, false) {
		proofStarted := time.Now()
		proof, proofErr := app.proveWordPressRestore(ctx, root, archiveInfo.Databases)
		rehearsal := &MigrationRehearsal{CompletedAt: time.Now().UTC(), DurationMS: time.Since(proofStarted).Milliseconds()}
		switch {
		case errors.Is(proofErr, errRecoveryProofUnsupported):
			rehearsal.Status, rehearsal.Details = "unsupported", proofErr.Error()
		case proofErr != nil:
			rehearsal.Status, rehearsal.Details = "failed", "The imported application did not pass its disposable WordPress restore and HTTP proof; inspect the job diagnostics."
		default:
			rehearsal.Status, rehearsal.Checks = "passed", proof.Checks
			rehearsal.Details = "The archive was restored into a disposable site, its database was imported, and the application HTTP checks passed."
		}
		result.Rehearsal = rehearsal
	} else if app != nil {
		result.Rehearsal = &MigrationRehearsal{Status: "skipped", Details: "The built-in WordPress recovery proof is not enabled or its site, database, and web-server helpers are unavailable."}
	}
	applyRehearsalCompatibility(result.Compatibility, result.Rehearsal)
	result.Components = buildMigrationComponents(archiveInfo, result, databases, nil)
	return result, nil
}

func buildMigrationComponents(info CPMoveInfo, result ImportResult, databasesSelected bool, operationErr error) []MigrationComponent {
	components := make([]MigrationComponent, 0, len(info.Databases)+len(info.Mailboxes)+len(info.DNSZones)+len(info.CronSchedules)+4)
	add := func(category, source, target string, disposition MigrationDisposition, details string) {
		components = append(components, MigrationComponent{Category: category, Source: source, Target: target, Disposition: disposition, Details: details})
	}
	failed := operationErr != nil
	accountTarget := result.Account
	if accountTarget == "" {
		accountTarget = result.User
	}
	accountDisposition := MigrationTransformed
	accountDetails := "Source account mapped to the selected destination account."
	if failed {
		accountDisposition, accountDetails = MigrationFailed, "Import did not complete; inspect job diagnostics and recovery state."
	}
	add("account", info.User, accountTarget, accountDisposition, accountDetails)
	if info.HasHome {
		disposition, target, details := MigrationImported, "main site document root", "Website files were activated through the site staging workflow."
		if failed {
			disposition, target, details = MigrationFailed, "", "The import did not complete; inspect job diagnostics and recovery state."
		} else if !result.FilesRestored {
			disposition, target, details = MigrationSkipped, "", "No public_html document root was present."
		}
		add("website_files", "cPanel home directory", target, disposition, details)
	}
	for _, source := range info.Databases {
		switch {
		case !databasesSelected:
			add("databases", source, "", MigrationSkipped, "Database restoration was not selected.")
		case failed:
			add("databases", source, "", MigrationFailed, "The database restore operation did not complete; inspect the durable job and recovery state.")
		default:
			target := ""
			for _, restored := range result.DatabasesRestored {
				if strings.HasSuffix(restored, "_"+source) || restored == source {
					target = restored
					break
				}
			}
			if target == "" {
				disposition, details := MigrationFailed, "No successful destination database was recorded."
				if failed {
					details = "Database import did not complete; consult job diagnostics and recovery state."
				}
				add("databases", source, "", disposition, details)
			} else {
				add("databases", source, target, MigrationTransformed, "Imported under a destination-managed database identity.")
			}
		}
	}
	for _, source := range info.Mailboxes {
		if failed {
			add("mailboxes", source, "", MigrationFailed, "The import did not complete; inspect the durable job and recovery state.")
		} else if result.MailStaged {
			add("mailboxes", source, source, MigrationTransformed, "Mailbox data was staged privately; delivery and credentials require separate setup.")
		} else if failed && len(result.MailErrors) > 0 {
			add("mailboxes", source, "", MigrationFailed, "Mailbox staging failed; consult job diagnostics.")
		} else {
			add("mailboxes", source, "", MigrationSkipped, "Mailbox data was not staged on this destination.")
		}
	}
	for _, evidence := range result.Evidence {
		if evidence.Category != "domains" {
			continue
		}
		for _, source := range evidence.SourceItems {
			if source == "" {
				continue
			}
			found := false
			if source == result.MainDomain && source != "" {
				disposition, details := MigrationTransformed, "Main domain content was placed in the destination account; DNS and route cutover are separate."
				if failed {
					disposition, details = MigrationFailed, "Domain import did not complete; inspect job diagnostics."
				}
				add("domains", source, result.User, disposition, details)
				found = true
			}
			for _, parked := range result.ParkedDomains {
				if source == parked {
					disposition, details := MigrationTransformed, "Parked-domain mapping was recorded; DNS and route activation remain separate."
					if failed {
						disposition, details = MigrationFailed, "Domain import did not complete; inspect job diagnostics."
					}
					add("domains", source, result.User+" (route pending)", disposition, details)
					found = true
					break
				}
			}
			for _, domain := range result.Domains {
				if found {
					break
				}
				if domain.Domain == source {
					disposition, details := MigrationTransformed, "Domain content was placed in its own destination site; route and DNS cutover are separate."
					if failed {
						disposition, details = MigrationFailed, "Domain import did not complete; inspect job diagnostics."
					}
					add("domains", source, domain.Site, disposition, details)
					found = true
					break
				}
			}
			if !found {
				disposition, details := MigrationSkipped, "Domain is not activated by this import workflow."
				for _, skipped := range evidence.SkippedItems {
					if strings.HasPrefix(skipped, source+":") {
						details = strings.TrimSpace(strings.TrimPrefix(skipped, source+":"))
						break
					}
				}
				if failed {
					disposition, details = MigrationFailed, "Domain import did not complete; inspect job diagnostics."
				}
				add("domains", source, "", disposition, details)
			}
		}
		break
	}
	for _, domain := range result.Domains {
		add("domains", domain.Domain, domain.Site, MigrationTransformed, "Domain files were placed into a separate destination site; DNS and route cutover remain separate.")
	}
	for _, zone := range info.DNSZones {
		add("dns", zone, "", MigrationUnsupported, "DNS records are not imported; review and apply them at the authoritative DNS provider.")
	}
	for _, schedule := range info.CronSchedules {
		add("cron_jobs", schedule, "", MigrationUnsupported, "Command text is intentionally not retained or executed; review and recreate this schedule explicitly.")
	}
	return components
}

func buildCPMoveMigrationEvidence(info CPMoveInfo, result ImportResult, databasesSelected bool, plan cpmoveDomainPlan, domainInventoryErr error) []MigrationEvidence {
	accountTarget := result.Account
	if accountTarget == "" {
		accountTarget = result.User
	}
	evidence := []MigrationEvidence{boundMigrationEvidence(MigrationEvidence{
		Category:      "account",
		Status:        "completed",
		SourceItems:   []string{info.User},
		ImportedItems: []string{accountTarget},
		Details:       "The source account was mapped to the destination account shown.",
	})}

	website := MigrationEvidence{Category: "website_files", Status: "completed", Details: fmt.Sprintf("Inspected %d archive entries (%d expanded bytes); file-level names are not retained in the migration report.", info.Entries, info.ExpandedBytes)}
	if info.HasHome {
		website.SourceItems = append(website.SourceItems, "cPanel home directory")
	}
	if result.FilesRestored {
		website.ImportedItems = append(website.ImportedItems, "main site document root")
	} else if info.HasHome {
		website.SkippedItems = append(website.SkippedItems, "main site document root: no public_html tree was present")
	}
	for _, domain := range plan.Sites {
		website.SourceItems = append(website.SourceItems, domain.Domain)
		if domain.Source == "" {
			website.SkippedItems = append(website.SkippedItems, domain.Domain+": source document root was absent")
		}
	}
	for _, domain := range result.Domains {
		website.ImportedItems = append(website.ImportedItems, domain.Domain)
	}
	evidence = append(evidence, boundMigrationEvidence(website))

	db := MigrationEvidence{Category: "databases", Status: "completed", SourceItems: append([]string(nil), info.Databases...), ImportedItems: append([]string(nil), result.DatabasesRestored...)}
	if !databasesSelected {
		db.Status = "not_selected"
		for _, name := range info.Databases {
			db.SkippedItems = append(db.SkippedItems, name+": database restore was not selected")
		}
	} else if len(result.DatabaseErrors) > 0 {
		db.Details = "The job failed during database restore; inspect its error and retry/recovery state."
	}

	mail := MigrationEvidence{Category: "mailboxes", Status: "completed", SourceItems: append([]string(nil), info.Mailboxes...), ImportedItems: append([]string(nil), result.MailboxesStaged...), Details: "Mailbox data is staged privately; mail delivery, credentials, aliases, forwarding, and MX records are not activated by this import."}
	if info.HasMail && !result.MailStaged {
		mail.Status = "not_selected"
		mail.Details = "Mail data was present but was not staged on the destination."
	}

	domains := []string{}
	if plan.MainDomain != "" {
		domains = append(domains, plan.MainDomain)
	}
	domains = append(domains, plan.Parked...)
	for _, domain := range plan.Sites {
		domains = append(domains, domain.Domain)
	}
	destinationDomains := []string{}
	if result.MainDomain != "" {
		destinationDomains = append(destinationDomains, result.MainDomain)
	}
	for _, domain := range result.ParkedDomains {
		destinationDomains = append(destinationDomains, domain+" (route pending)")
	}
	for _, domain := range result.Domains {
		destinationDomains = append(destinationDomains, domain.Domain)
	}
	domainEvidence := MigrationEvidence{Category: "domains", Status: "completed", SourceItems: domains, ImportedItems: destinationDomains, Details: "Destination routes are separate from public DNS changes."}
	switch {
	case domainInventoryErr != nil:
		domainEvidence.Status = "not_inspected"
		domainEvidence.Details = "Could not safely read domain declarations from the archive; inspect userdata/main before cutover."
	case !plan.InventoryAvailable:
		domainEvidence.Status = "not_inspected"
		domainEvidence.Details = "The archive did not include a readable userdata/main domain inventory."
	case result.Account == "" && len(domains) > 0:
		domainEvidence.Status = "not_selected"
		for _, domain := range domains {
			domainEvidence.SkippedItems = append(domainEvidence.SkippedItems, domain+": addon/parked-domain placement was not part of this legacy-layout import")
		}
	case result.Account != "":
		for _, domain := range plan.Sites {
			if containsImportedDomain(result.Domains, domain.Domain) {
				continue
			}
			reason := domain.Domain + ": domain site was not restored"
			for _, failure := range result.DomainErrors {
				if strings.HasPrefix(failure, domain.Domain+":") {
					reason = failure
					break
				}
			}
			domainEvidence.SkippedItems = append(domainEvidence.SkippedItems, reason)
		}
	}
	domainEvidence = boundMigrationEvidence(domainEvidence)
	evidence = append(evidence, boundMigrationEvidence(db), boundMigrationEvidence(mail), domainEvidence)
	dns := MigrationEvidence{Category: "dns", Status: "observed_not_migrated", SourceItems: append([]string(nil), info.DNSZones...), OmittedItemCount: info.DNSZoneOmitted, Details: "The archive contains these DNS zone files. Individual records were not parsed or changed; compare authoritative A/AAAA, CNAME, MX, SPF, DKIM, DMARC, CAA, and TTL values before cutover."}
	for _, zone := range info.DNSZones {
		dns.SkippedItems = append(dns.SkippedItems, zone+": DNS records require explicit review and were not changed")
	}
	if len(info.DNSZones) == 0 {
		dns.Status = "not_inspected"
		dns.Details = "No DNS zone files were found in the archive. Inventory records at the authoritative DNS provider before cutover."
	}
	cron := MigrationEvidence{Category: "cron_jobs", Status: "observed_not_migrated", SourceItems: append([]string(nil), info.CronSchedules...), OmittedItemCount: info.CronOmitted, Details: "Schedules were read from the cPanel archive; command text is intentionally omitted because it may contain credentials. Tasks were not installed. Review source commands, paths, environment, and permissions, then recreate and test them on the destination."}
	for _, schedule := range info.CronSchedules {
		cron.SkippedItems = append(cron.SkippedItems, schedule+": command was not migrated")
	}
	if len(info.CronSchedules) == 0 {
		cron.Status = "not_inspected"
		cron.Details = "No readable scheduled tasks were found in the archive. Verify the source account crontab before cutover."
	}
	evidence = append(evidence, boundMigrationEvidence(dns), boundMigrationEvidence(cron))
	for i := range evidence {
		switch {
		case evidence[i].Category == "dns" || evidence[i].Category == "cron_jobs":
			if len(evidence[i].SourceItems) > 0 {
				evidence[i].Disposition = MigrationUnsupported
			} else {
				evidence[i].Disposition = MigrationSkipped
			}
		case evidence[i].Status == "not_selected" || evidence[i].Status == "not_inspected":
			evidence[i].Disposition = MigrationSkipped
		case evidence[i].Category == "account" || evidence[i].Category == "databases" || evidence[i].Category == "mailboxes" || evidence[i].Category == "domains":
			evidence[i].Disposition = MigrationTransformed
		default:
			evidence[i].Disposition = MigrationImported
		}
	}
	return evidence
}

var (
	cronNumericField = regexp.MustCompile(`^[0-9*/,-]{1,64}$`)
	cronNamedField   = regexp.MustCompile(`^(?:[0-9*]+|[A-Za-z]{3})(?:[-/,](?:[0-9*]+|[A-Za-z]{3})){0,15}$`)
	cronMacros       = map[string]bool{"@reboot": true, "@yearly": true, "@annually": true, "@monthly": true, "@weekly": true, "@daily": true, "@midnight": true, "@hourly": true}
)

// cronSchedule returns the schedule of a crontab job line. A line whose
// leading fields are not schedule syntax (an environment assignment or a
// malformed entry) is rejected rather than reporting part of its command.
func cronSchedule(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false
	}
	if strings.HasPrefix(fields[0], "@") {
		macro := strings.ToLower(fields[0])
		return macro, cronMacros[macro]
	}
	if len(fields) < 6 {
		return "", false
	}
	for i, field := range fields[:5] {
		// Only month and weekday may use names (jan, mon).
		pattern := cronNumericField
		if i >= 3 {
			pattern = cronNamedField
		}
		if !pattern.MatchString(field) {
			return "", false
		}
	}
	return strings.Join(fields[:5], " "), true
}

func containsImportedDomain(imported []ImportedDomain, domain string) bool {
	for _, item := range imported {
		if item.Domain == domain {
			return true
		}
	}
	return false
}

func boundMigrationEvidence(item MigrationEvidence) MigrationEvidence {
	for _, values := range []*[]string{&item.SourceItems, &item.ImportedItems, &item.SkippedItems} {
		if len(*values) > maxMigrationEvidenceItems {
			item.OmittedItemCount += len(*values) - maxMigrationEvidenceItems
			*values = (*values)[:maxMigrationEvidenceItems]
		}
	}
	return item
}

func siteHelperContext(ctx context.Context, cfg Config, action, site string) error {
	if cfg.SiteCtl == "" {
		return nil
	}
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return err
		}
		resp, err := client.Execute(ctx, &rootbroker.Request{
			RequestType: "site",
			Site:        &rootbroker.SiteRequest{Action: action, Site: site},
		})
		if err != nil {
			return err
		}
		if !resp.OK {
			return errors.New(resp.Error)
		}
		return nil
	}
	if action == "prepare" {
		// Every prepare caller publishes the public tree through the site
		// manager afterwards, so ask the helper for the isolation contract
		// without creating public/ (the typed broker does the same).
		action = "prepare-root"
	}
	return runHelperCommand(ctx, cfg, cfg.SiteCtl, action, site)
}

// restoreMail preserves cPanel mailbox data and account mail metadata under a
// private StePanel root. Host-specific Exim/Dovecot configuration is not
// copied into /etc because it can break the destination mail server.
func restoreMail(cfg Config, stage, user string) (bool, []string, []string) {
	return restoreMailContext(context.Background(), cfg, stage, user)
}

func restoreMailContext(ctx context.Context, cfg Config, stage, user string) (bool, []string, []string) {
	if err := ctx.Err(); err != nil {
		return false, nil, []string{err.Error()}
	}
	sourceMail := filepath.Join(stage, "homedir", "mail")
	sourceEtc := filepath.Join(stage, "homedir", "etc")
	if _, err := os.Stat(sourceMail); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, []string{"inspect staged mail: " + err.Error()}
	}
	if cfg.MailRoot == "" {
		return false, nil, []string{"mail root is not configured; set STEPANEL_MAIL_ROOT"}
	}
	root, err := safePath(cfg.MailRoot, user)
	if err != nil {
		return false, nil, []string{"resolve mail root: " + err.Error()}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return false, nil, []string{"create mail root: " + err.Error()}
	}
	if err := copyTreeContext(ctx, sourceMail, filepath.Join(root, "mail")); err != nil {
		return false, nil, []string{"copy mailbox data: " + err.Error()}
	}
	if _, err := os.Stat(sourceEtc); err == nil {
		if err := copyTreeContext(ctx, sourceEtc, filepath.Join(root, "etc")); err != nil {
			return false, nil, []string{"copy mail metadata: " + err.Error()}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, nil, []string{"inspect mail metadata: " + err.Error()}
	}
	mailboxes := []string{}
	mailRoot := filepath.Join(root, "mail")
	walkErr := filepath.Walk(mailRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && path != mailRoot {
			rel, relErr := filepath.Rel(mailRoot, path)
			if relErr != nil {
				return relErr
			}
			if strings.Count(filepath.ToSlash(rel), "/") == 1 {
				mailboxes = append(mailboxes, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(mailboxes)
	if walkErr != nil {
		return true, mailboxes, []string{"discover mailbox inventory: " + walkErr.Error()}
	}
	return true, mailboxes, nil
}

func extractArchive(archive, destination string) error {
	return extractArchiveContext(context.Background(), archive, destination)
}

func extractArchiveContext(ctx context.Context, archive, destination string) error {
	f, _, err := openRegularNoFollow(archive, nil)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractArchiveReaderContext(ctx, f, destination, archive)
}

// extractArchiveReaderContext extracts a cPanel or StePanel tar.gz into
// destination through internal/archivesafe: entries cannot escape it,
// collide with one another, or overwrite anything already there.
func extractArchiveReaderContext(ctx context.Context, input io.Reader, destination, archivePath string) error {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	extractor, err := archivesafe.OpenExtractor(destination)
	if err != nil {
		return err
	}
	defer extractor.Close()
	// The staged upload may live in the destination; no entry may name it.
	stagedUpload := ""
	if archivePath != "" {
		if rel, relErr := filepath.Rel(destination, archivePath); relErr == nil && filepath.IsLocal(rel) {
			stagedUpload = filepath.ToSlash(rel)
			if err := extractor.Reserve(stagedUpload); err != nil {
				return err
			}
		}
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !safeArchivePath(h.Name) {
			return errors.New("unsafe archive path")
		}
		if stagedUpload != "" {
			if clean, cleanErr := archivesafe.NormalizeName(h.Name); cleanErr == nil && clean == stagedUpload {
				return errors.New("archive entry conflicts with staged upload")
			}
		}
		kind, err := archivesafe.TarKind(h)
		if err != nil {
			return fmt.Errorf("unsupported archive entry type: %w", err)
		}
		if kind == archivesafe.Directory {
			if _, err := extractor.Dir(h.Name, 0o700); err != nil {
				return fmt.Errorf("unsafe archive path: %w", err)
			}
			continue
		}
		if h.Size < 0 || h.Size > 2<<30 || total+h.Size > 20<<30 {
			return errors.New("archive contents exceed the 20 GiB extraction limit")
		}
		_, written, err := extractor.File(h.Name, 0o600, tr, h.Size)
		if err != nil {
			return fmt.Errorf("unsafe archive path: %w", err)
		}
		total += written
	}
}

func restoreSQL(cfg Config, stage, user string, txn *SiteTransaction, ledgers ...*capacityLedger) ([]string, []string) {
	return restoreSQLContext(context.Background(), cfg, stage, user, txn, ledgers...)
}

func restoreSQLContext(parent context.Context, cfg Config, stage, user string, txn *SiteTransaction, ledgers ...*capacityLedger) ([]string, []string) {
	if cfg.Production && cfg.DBCtl != "" {
		var ledger *capacityLedger
		if len(ledgers) > 0 {
			ledger = ledgers[0]
		}
		if err := requireDatabaseRestoreAdmission(cfg, ledger); err != nil {
			return nil, []string{"restore SQL dumps: " + err.Error()}
		}
	}
	matches, discoveryErr := sqlDumps(filepath.Join(stage, "mysql"))
	restored, failures := []string{}, []string{}
	if discoveryErr != nil {
		failures = append(failures, "discover SQL dumps: "+discoveryErr.Error())
		return restored, failures
	}
	if cfg.Production && cfg.DBCtl == "" && len(matches) > 0 {
		return restored, []string{"restore SQL dumps: production database restores require STEPANEL_DBCTL and the root broker"}
	}
	for _, dump := range matches {
		if err := parent.Err(); err != nil {
			failures = append(failures, err.Error())
			break
		}
		db := strings.TrimSuffix(filepath.Base(dump), ".sql")
		name, nameErr := cpmoveDatabaseName(user, db, databaseNameLimit(cfg))
		if nameErr != nil {
			failures = append(failures, filepath.Base(dump)+": invalid database name")
			continue
		}
		quotedName, quoteErr := managedDatabaseSQLIdentifier(name, databaseNameLimit(cfg))
		if quoteErr != nil {
			failures = append(failures, filepath.Base(dump)+": invalid database name")
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
		if cfg.DBCtl != "" {
			if txn != nil {
				if err := txn.TrackDatabase(ManagedDatabase{Name: name, Kind: "cpmove"}); err != nil {
					failures = append(failures, name+": record recovery state: "+err.Error())
					cancel()
					continue
				}
			}
			restoreErr := runDatabaseRestoreFromPath(ctx, cfg, "restore", user, name, user, "", dump, ledgers...)
			cancel()
			if restoreErr != nil {
				failures = append(failures, name+": restore failed: "+restoreErr.Error())
				continue
			}
			restored = append(restored, name)
			continue
		}
		exists, existsErr := mysqlObjectExists(cfg, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME="+sqlString(name))
		if existsErr != nil {
			failures = append(failures, name+": existence check failed: "+existsErr.Error())
			cancel()
			continue
		}
		if exists {
			failures = append(failures, name+": database already exists")
			cancel()
			continue
		}
		if txn != nil {
			if err := txn.TrackDatabase(ManagedDatabase{Name: name, Kind: "cpmove"}); err != nil {
				failures = append(failures, name+": record recovery state: "+err.Error())
				cancel()
				continue
			}
		}
		cmd, cleanupCommand, commandErr := newMySQLCommand(ctx, cfg, "--batch")
		if commandErr != nil {
			failures = append(failures, name+": create command failed: "+commandErr.Error())
			cancel()
			continue
		}
		cmd.Stdin = strings.NewReader("CREATE DATABASE " + quotedName + ";\n")
		output, err := runMySQLCommand(ctx, cfg, cmd)
		if cleanupErr := cleanupCommand(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private MySQL credential directory: %w", cleanupErr))
		}
		if err != nil {
			failures = append(failures, name+": create failed: "+strings.TrimSpace(string(output)))
			cancel()
			continue
		}
		input, err := os.Open(dump)
		if err != nil {
			failures = append(failures, name+": open failed: "+err.Error())
			cancel()
			continue
		}
		cmd, cleanupCommand, commandErr = newMySQLCommand(ctx, cfg, name)
		if commandErr != nil {
			_ = input.Close()
			failures = append(failures, name+": import command failed: "+commandErr.Error())
			cancel()
			continue
		}
		cmd.Stdin = input
		output, err = runMySQLCommand(ctx, cfg, cmd)
		if cleanupErr := cleanupCommand(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private MySQL credential directory: %w", cleanupErr))
		}
		closeErr := input.Close()
		if closeErr != nil {
			failures = append(failures, name+": close dump failed: "+closeErr.Error())
		}
		cancel()
		if err != nil {
			failures = append(failures, name+": import failed: "+strings.TrimSpace(string(output)))
			if cleanupErr := dropDatabase(parent, cfg, name); cleanupErr != nil {
				failures = append(failures, name+": cleanup failed: "+cleanupErr.Error())
			}
			continue
		}
		restored = append(restored, name)
	}
	return restored, failures
}

// dropDatabase removes a managed database. parent must carry the site lease
// (its fencing token) when the drop goes through the root broker.
func dropDatabase(parent context.Context, cfg Config, name string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Minute)
	defer cancel()
	quotedName, err := managedDatabaseSQLIdentifier(name, databaseNameLimit(cfg))
	if err != nil {
		return err
	}
	if cfg.DBCtl != "" {
		output, err := runDatabaseHelperContext(ctx, cfg, 2*time.Minute, "", "drop", name)
		if err != nil {
			return fmt.Errorf("drop database: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	output, err := runMySQLQuery(ctx, cfg, "DROP DATABASE IF EXISTS "+quotedName+";", "--batch")
	if err != nil {
		return fmt.Errorf("drop database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func mysqlArgs(cfg Config) []string {
	args := []string{}
	if cfg.DBHost != "" {
		args = append(args, "--host", cfg.DBHost)
	}
	if cfg.DBUser != "" {
		args = append(args, "--user", cfg.DBUser)
	}
	return args
}

func sqlDumps(root string) ([]string, error) {
	matches := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".sql") {
			matches = append(matches, path)
		}
		return nil
	})
	sort.Strings(matches)
	return matches, err
}

func databaseName(user, db string) string {
	user = strings.ReplaceAll(user, "-", "_")
	db = strings.ReplaceAll(db, "-", "_")
	if strings.HasPrefix(db, user+"_") {
		return db
	}
	return user + "_" + db
}

func cpmoveDatabaseName(user, db string, max int) (string, error) {
	if safeUser(user) != user || safeUser(db) != db {
		return "", errors.New("invalid cPanel database identity")
	}
	name := databaseName(user, db)
	if !validManagedDatabaseIdentifier(name, max) {
		return "", errors.New("invalid cPanel database identity")
	}
	return name, nil
}

func copyTreeContext(ctx context.Context, src, dst string) error {
	return copyTreeExcludingContext(ctx, src, dst, nil)
}

// copyTreeExcludingContext copies src into dst, skipping the directories
// whose path relative to src is in exclude. A cPanel backup's addon and
// subdomain document roots nested in public_html are restored as their own
// sites instead of a second copy inside the main site.
func copyTreeExcludingContext(ctx context.Context, src, dst string, exclude map[string]bool) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if info.IsDir() && exclude[rel] {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if err := rejectSymlinkParents(target, dst); err != nil {
			return err
		}
		if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination symlink is not allowed: %s", target)
		}
		if info.IsDir() {
			if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("destination symlink is not allowed: %s", target)
			}
			return os.MkdirAll(target, 0750)
		}
		in, _, err := openRegularNoFollow(path, info)
		if err != nil {
			return err
		}
		out, err := openWriteNoFollow(target, 0640)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		inErr := in.Close()
		closeErr := out.Close()
		if err == nil {
			err = inErr
		}
		if err == nil {
			err = closeErr
		}
		return err
	})
}
func firstExisting(paths ...string) string {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	return ""
}
func safeArchivePath(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !strings.Contains(path, "\\") && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !filepath.IsAbs(path)
}

func normalizeCPMovePath(path string) cpmovePathInfo {
	name := strings.Trim(filepath.ToSlash(path), "/")
	parts := strings.Split(name, "/")
	if len(parts) >= 2 && isCPMoveRoot(parts[0]) && (parts[1] == "homedir" || parts[1] == "mysql" || parts[1] == "dnszones" || parts[1] == "cron") {
		return cpmovePathInfo{Path: strings.Join(parts[1:], "/"), User: userFromArchiveName(parts[0])}
	}
	return cpmovePathInfo{Path: name}
}

func isCPMoveRoot(name string) bool {
	return strings.HasPrefix(name, "cpmove-") || strings.HasPrefix(name, "backup-")
}

func userFromArchiveName(name string) string {
	base := filepath.Base(strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".tar"))
	if strings.HasPrefix(base, "cpmove-") {
		candidate := strings.TrimPrefix(base, "cpmove-")
		if i := strings.Index(candidate, "."); i > 0 {
			candidate = candidate[:i]
		}
		if safeUser(candidate) != "" && !isRecoveryProofSiteName(candidate) {
			return candidate
		}
	}
	if strings.HasPrefix(base, "backup-") {
		candidate := strings.TrimPrefix(base, "backup-")
		if i := strings.LastIndex(candidate, "_"); i >= 0 && i < len(candidate)-1 {
			candidate = candidate[i+1:]
		} else {
			return ""
		}
		if safeUser(candidate) != "" && !isRecoveryProofSiteName(candidate) {
			return candidate
		}
	}
	return ""
}

func cpmoveRoot(stage string) (string, error) {
	if firstExisting(filepath.Join(stage, "homedir"), filepath.Join(stage, "mysql")) != "" {
		return stage, nil
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() && firstExisting(filepath.Join(stage, entry.Name(), "homedir"), filepath.Join(stage, entry.Name(), "mysql")) != "" {
			return filepath.Join(stage, entry.Name()), nil
		}
	}
	return "", errors.New("backup does not contain a cPanel homedir or mysql directory")
}
