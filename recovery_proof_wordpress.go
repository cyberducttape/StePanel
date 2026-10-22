package stepanel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/importer"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// The WordPress recovery proof restores a verified backup into a throwaway
// site through the production lifecycle path and proves the restored
// application serves real requests:
//
//  1. the site's files are published through SiteManager into a new site
//     whose Unix account and PHP-FPM pool the root broker creates;
//  2. the backed-up database is imported by stepanel-dbctl restore-wordpress,
//     which creates a fresh database and user and imports as that user;
//  3. wp-config.php is rewritten to those credentials and to a private
//     *.localhost hostname, so the proof never serves a customer domain;
//  4. the web server publishes a route for that hostname, and loopback HTTP
//     requests must reach WordPress backed by the restored database.
//
// The proof's site transaction is never committed. Every exit path tears
// the site down; a crash leaves an uncommitted transaction whose route,
// database, and site the startup recovery removes.

const (
	recoveryProofSitePrefix      = "stepanel-proof-"
	recoveryProofTransactionKind = "recovery.proof"
	wordpressProofProbeTimeout   = 2 * time.Minute
	wordpressProofBodyLimit      = 2 << 20
	// wordpressProofReloadRetries bounds retries of 404/502/503 responses
	// while the web server and PHP-FPM reload (about a minute).
	wordpressProofReloadRetries = 15
	wordpressProofTreeMaxBytes  = maxBackupBytes
)

// errRecoveryProofUnsupported reports a backup the built-in WordPress proof
// cannot exercise. The rehearsal then records archive-level evidence and the
// reason, rather than failing.
var errRecoveryProofUnsupported = errors.New("built-in WordPress recovery proof is unsupported for this backup")

// Recovery proof modes (STEPANEL_WORDPRESS_RECOVERY_PROOF).
const (
	recoveryProofModeManual = "manual" // operator-requested and offsite rehearsals
	recoveryProofModeAlways = "always" // also after scheduled backups
	recoveryProofModeOff    = "off"
)

// isRecoveryProofSiteName reports names reserved for throwaway proof sites.
// No other workflow may create a site with this prefix, so recovery can
// remove a proof site without risk of touching a customer site.
func isRecoveryProofSiteName(name string) bool {
	return strings.HasPrefix(name, recoveryProofSitePrefix)
}

// wordpressProofTreeSize measures the extra site-volume space required by
// the staging copy. Refuse links and special files rather than following
// mutable paths or silently under-reserving them.
func wordpressProofTreeSize(ctx context.Context, root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("WordPress proof tree contains symlink %s", filepath.Base(path))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 {
			return fmt.Errorf("WordPress proof tree contains non-regular file %s", filepath.Base(path))
		}
		size := uint64(info.Size())
		if size > uint64(wordpressProofTreeMaxBytes)-total {
			return fmt.Errorf("WordPress proof tree exceeds %d-byte limit", wordpressProofTreeMaxBytes)
		}
		total += size
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure WordPress proof tree: %w", err)
	}
	return total, nil
}

type wordpressProofResult struct {
	Site       string   `json:"site"`
	Hostname   string   `json:"hostname"`
	Database   string   `json:"database"`
	Checks     []string `json:"checks"`
	DurationMS int64    `json:"duration_ms"`
}

type wordpressProofPlan struct {
	site, hostname, scheme, route string
	database, user, password      string
	dump, public                  string
	config                        []byte
}

// wordpressProofPorts are the loopback ports the probe dials; tests replace
// them with an httptest server.
var wordpressProofPorts = struct{ http, https string }{http: "80", https: "443"}

// wordpressProofEnabled reports whether the built-in proof runs for a
// rehearsal with the given trigger.
func wordpressProofEnabled(cfg Config, scheduled bool) bool {
	switch cfg.WordPressRecoveryProof {
	case recoveryProofModeOff:
		return false
	case recoveryProofModeAlways:
	default:
		if scheduled {
			return false
		}
	}
	return cfg.SiteCtl != "" && cfg.DBCtl != "" && cfg.VHostCtl != "" && cfg.WebRoot != "" && cfg.RecoveryRoot != ""
}

// planWordPressProof inspects an extracted backup stage. It returns
// errRecoveryProofUnsupported, wrapped with the reason, when the backup is
// not a WordPress site the proof can restore.
func planWordPressProof(cfg Config, stage string, databases []string) (wordpressProofPlan, error) {
	unsupported := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", errRecoveryProofUnsupported, fmt.Sprintf(format, args...))
	}
	public, err := safePath(stage, "site", "public")
	if err != nil {
		return wordpressProofPlan{}, err
	}
	configPath := filepath.Join(public, "wp-config.php")
	file, info, err := openRegularNoFollow(configPath, nil)
	if errors.Is(err, os.ErrNotExist) {
		return wordpressProofPlan{}, unsupported("the backup has no public/wp-config.php")
	}
	if err != nil {
		return wordpressProofPlan{}, fmt.Errorf("open backed-up wp-config.php: %w", err)
	}
	if info.Size() > 1<<20 {
		_ = file.Close()
		return wordpressProofPlan{}, errors.New("backed-up wp-config.php exceeds 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return wordpressProofPlan{}, fmt.Errorf("read backed-up wp-config.php: %w", errors.Join(err, closeErr))
	}
	if _, err := os.Lstat(filepath.Join(public, "wp-settings.php")); err != nil {
		return wordpressProofPlan{}, unsupported("the backup has no WordPress core (public/wp-settings.php)")
	}
	switch cfg.DBEngine {
	case "", "mysql", "mariadb":
	default:
		return wordpressProofPlan{}, unsupported("WordPress requires MySQL or MariaDB, not %s", cfg.DBEngine)
	}
	var scheme string
	switch cfg.WebServer {
	case "caddy":
		// Caddy serves *.localhost names with its internal CA and
		// redirects plain HTTP to HTTPS.
		scheme = "https"
	case "apache", "openlitespeed":
		scheme = "http"
	default:
		return wordpressProofPlan{}, unsupported("web server %q is not supported by the proof", cfg.WebServer)
	}
	database, found, err := importer.ReadPHPDefine(string(data), "DB_NAME")
	if errors.Is(err, importer.ErrFunctionBackedDefine) {
		return wordpressProofPlan{}, unsupported("wp-config.php computes DB_NAME at runtime")
	}
	if err != nil {
		return wordpressProofPlan{}, fmt.Errorf("parse backed-up wp-config.php: %w", err)
	}
	if !found || !slices.Contains(databases, database) {
		return wordpressProofPlan{}, unsupported("the backup does not include the database named by wp-config.php DB_NAME")
	}
	dump, err := safePath(stage, "databases", database+".sql")
	if err != nil {
		return wordpressProofPlan{}, err
	}
	id, err := randomProofHex(4)
	if err != nil {
		return wordpressProofPlan{}, err
	}
	password, err := randomProofHex(16)
	if err != nil {
		return wordpressProofPlan{}, err
	}
	plan := wordpressProofPlan{
		site:     recoveryProofSitePrefix + id,
		scheme:   scheme,
		database: "spproof_" + id,
		user:     "spproof_" + id,
		password: password,
		dump:     dump,
		public:   public,
	}
	plan.hostname = plan.site + ".localhost"
	plan.route = siteVHostConfigName(cfg.WebServer, plan.site, plan.hostname)
	plan.config, err = rewriteWordPressProofConfig(data, plan)
	if err != nil {
		return wordpressProofPlan{}, err
	}
	return plan, nil
}

func randomProofHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate recovery proof identity: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// rewriteWordPressProofConfig points the restored configuration at the
// proof's database credentials and private hostname. Values computed at
// runtime (getenv) cannot be substituted, so such a backup is unsupported.
func rewriteWordPressProofConfig(data []byte, plan wordpressProofPlan) ([]byte, error) {
	content := string(data)
	url := plan.scheme + "://" + plan.hostname
	required := []struct{ key, value string }{
		{"DB_NAME", plan.database},
		{"DB_USER", plan.user},
		{"DB_PASSWORD", plan.password},
		{"DB_HOST", "localhost"},
	}
	for _, item := range required {
		updated, found, err := importer.ReplacePHPDefine(content, item.key, item.value)
		if errors.Is(err, importer.ErrFunctionBackedDefine) {
			return nil, fmt.Errorf("%w: wp-config.php computes %s at runtime", errRecoveryProofUnsupported, item.key)
		}
		if err != nil {
			return nil, fmt.Errorf("rewrite wp-config.php %s: %w", item.key, err)
		}
		if !found {
			return nil, fmt.Errorf("%w: wp-config.php does not define %s", errRecoveryProofUnsupported, item.key)
		}
		content = updated
	}
	// WP_HOME and WP_SITEURL override the restored siteurl/home options, so
	// WordPress serves the private hostname instead of redirecting to the
	// customer's domain.
	var missing []string
	for _, key := range []string{"WP_HOME", "WP_SITEURL"} {
		updated, found, err := importer.ReplacePHPDefine(content, key, url)
		if errors.Is(err, importer.ErrFunctionBackedDefine) {
			return nil, fmt.Errorf("%w: wp-config.php computes %s at runtime", errRecoveryProofUnsupported, key)
		}
		if err != nil {
			return nil, fmt.Errorf("rewrite wp-config.php %s: %w", key, err)
		}
		if !found {
			missing = append(missing, key)
		}
		content = updated
	}
	if len(missing) > 0 {
		open := strings.Index(content, "<?php")
		if open < 0 {
			return nil, fmt.Errorf("%w: wp-config.php has no <?php opening tag", errRecoveryProofUnsupported)
		}
		literal, err := importer.EncodePHPString(url)
		if err != nil {
			return nil, err
		}
		var defines strings.Builder
		defines.WriteString("\n// Added by the StePanel recovery proof.\n")
		for _, key := range missing {
			fmt.Fprintf(&defines, "define('%s', %s);\n", key, literal)
		}
		insert := open + len("<?php")
		content = content[:insert] + defines.String() + content[insert:]
	}
	return []byte(content), nil
}

// proveWordPressRestore runs the built-in proof for an extracted backup.
func (a *App) proveWordPressRestore(ctx context.Context, stage string, databases []string) (result wordpressProofResult, err error) {
	started := time.Now()
	plan, err := planWordPressProof(a.Config, stage, databases)
	if err != nil {
		return wordpressProofResult{}, err
	}
	result = wordpressProofResult{Site: plan.site, Hostname: plan.hostname, Database: plan.database}
	access, err := a.authorizeDurableSiteJob(plan.site, a.Auth.Username, true)
	if err != nil {
		return result, err
	}
	lockCtx, release, err := a.acquireSiteMutationLockContext(ctx, plan.site)
	if err != nil {
		return result, fmt.Errorf("acquire recovery proof site lock: %w", err)
	}
	defer release()
	if exists, err := a.siteExists(plan.site); err != nil || exists {
		return result, fmt.Errorf("recovery proof site %s is unavailable (exists=%t): %v", plan.site, exists, err)
	}
	proofBytes, err := wordpressProofTreeSize(lockCtx, plan.public)
	if err != nil {
		return result, err
	}
	reservation, err := a.capacityLedger().reserve(a.Config, "wordpress-recovery-proof", a.Config.WebRoot, []capacityDemand{{Path: a.Config.WebRoot, Bytes: proofBytes}})
	if err != nil {
		return result, fmt.Errorf("admit WordPress recovery proof site copy: %w", err)
	}
	defer reservation.release()
	intent, err := BeginSecurityAudit(a.Config.AuditLog, "system", "recovery.proof.site", plan.site, "throwaway WordPress site "+plan.hostname)
	if err != nil {
		return result, err
	}
	var outcome string
	defer intent.Finish(&outcome, "WordPress recovery proof did not pass")

	manager := a.siteManager
	if manager == nil {
		if manager, err = newSiteManagerForConfig(a.Config); err != nil {
			return result, fmt.Errorf("initialize site manager: %w", err)
		}
	}
	staging, stagingOwner, err := manager.CreateStagingLease(lockCtx, ".stepanel-proof-")
	if err != nil {
		return result, fmt.Errorf("create proof staging: %w", err)
	}
	defer func() {
		if err := manager.ReleaseStagingLease(staging, stagingOwner); err != nil {
			log.Printf("release recovery-proof staging lease for %s: %v", plan.site, err)
		}
	}()
	activated := false
	defer func() {
		if !activated {
			if discardErr := manager.DiscardStaging(context.Background(), staging); discardErr != nil {
				log.Printf("discard recovery proof staging: %v", discardErr)
			}
		}
	}()
	if err := copyTreeContext(lockCtx, plan.public, staging); err != nil {
		return result, fmt.Errorf("stage restored site files: %w", err)
	}
	configPath := filepath.Join(staging, "wp-config.php")
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() {
		return result, fmt.Errorf("staged wp-config.php is not a regular file: %v", err)
	}
	if err := writeAtomic(configPath, plan.config, info.Mode().Perm()); err != nil {
		return result, fmt.Errorf("write proof wp-config.php: %w", err)
	}

	canonical, err := safePath(a.Config.WebRoot, "sites", plan.site, "public")
	if err != nil {
		return result, err
	}
	txn, err := beginManagedSiteTransaction(lockCtx, a.Config, canonical, recoveryProofTransactionKind, access)
	if err != nil {
		return result, fmt.Errorf("begin recovery proof transaction: %w", err)
	}
	defer func() {
		// The transaction is never committed. Remove everything the proof
		// published, keeping the lease's fencing token even if ctx ended.
		if teardownErr := a.teardownWordPressProof(context.WithoutCancel(lockCtx), txn, manager, plan.site); teardownErr != nil {
			err = errors.Join(err, fmt.Errorf("tear down recovery proof site %s (startup recovery will retry): %w", plan.site, teardownErr))
		}
	}()
	if err := siteHelperContext(lockCtx, a.Config, "prepare", plan.site); err != nil {
		return result, fmt.Errorf("prepare proof site account and PHP-FPM pool: %w", err)
	}
	if _, err := manager.ActivateStaged(lockCtx, plan.site, staging); err != nil {
		return result, fmt.Errorf("publish proof site files: %w", err)
	}
	activated = true
	if err := siteHelperContext(lockCtx, a.Config, "seal", plan.site); err != nil {
		return result, fmt.Errorf("seal proof site ownership: %w", err)
	}
	result.Checks = append(result.Checks, "site account, PHP-FPM pool, and files published")

	if err := txn.TrackDatabase(ManagedDatabase{Name: plan.database, User: plan.user, Kind: "wordpress"}); err != nil {
		return result, fmt.Errorf("record proof database: %w", err)
	}
	if err := runDatabaseRestoreFromPath(lockCtx, a.Config, "restore-wordpress", plan.site, plan.database, plan.user, plan.password, plan.dump, a.capacityLedger()); err != nil {
		return result, fmt.Errorf("import backed-up database as the proof database user: %w", err)
	}
	result.Checks = append(result.Checks, "database imported with the credentials written to wp-config.php")

	if err := txn.TrackRoute(plan.route); err != nil {
		return result, fmt.Errorf("record proof route: %w", err)
	}
	if err := runVhostMutation(lockCtx, a.Config, "apply", plan.site, plan.hostname); err != nil {
		return result, fmt.Errorf("publish proof route: %w", err)
	}
	if err := txn.MarkRouteApplied(plan.route); err != nil {
		return result, fmt.Errorf("record published proof route: %w", err)
	}
	result.Checks = append(result.Checks, "web server route published for "+plan.hostname)

	probeCtx, cancel := context.WithTimeout(lockCtx, wordpressProofProbeTimeout)
	defer cancel()
	checks, err := probeWordPress(probeCtx, plan.scheme, plan.hostname)
	result.Checks = append(result.Checks, checks...)
	if err != nil {
		return result, fmt.Errorf("WordPress HTTP probe: %w", err)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	outcome = "restored WordPress served " + plan.hostname
	return result, nil
}

// teardownWordPressProof removes the proof site in dependency order: the
// route that points at it, its database, then the site itself. The site is
// deleted before the transaction is rolled back, so rollback has nothing to
// preserve and no copy of the restored site is left in the recovery root.
// Until every step succeeds the transaction stays open for startup recovery.
func (a *App) teardownWordPressProof(ctx context.Context, txn *SiteTransaction, manager siteauthority.Manager, site string) error {
	if err := txn.cleanupRoutes(ctx, a.Config); err != nil {
		return err
	}
	if err := txn.cleanupDatabases(ctx, a.Config); err != nil {
		return err
	}
	if err := siteHelperContext(ctx, a.Config, "delete", site); err != nil {
		return fmt.Errorf("remove proof site account and files: %w", err)
	}
	if err := manager.Delete(ctx, site); err != nil {
		return fmt.Errorf("finalize proof site removal: %w", err)
	}
	if err := txn.Rollback(); err != nil {
		return fmt.Errorf("close proof transaction: %w", err)
	}
	if txn.FailedSite == "" {
		if err := os.RemoveAll(txn.dir); err != nil {
			return fmt.Errorf("remove closed proof transaction: %w", err)
		}
	}
	return nil
}

// probeWordPress requests the restored site over loopback. Redirects are
// followed only within the proof hostname: a redirect elsewhere means
// WordPress ignored the proof's WP_HOME and would have served a customer
// domain.
func probeWordPress(ctx context.Context, scheme, hostname string) ([]string, error) {
	return probeWordPressWithRoots(ctx, scheme, hostname, nil)
}

func probeWordPressWithRoots(ctx context.Context, scheme, hostname string, roots *x509.CertPool) ([]string, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			port := wordpressProofPorts.http
			if strings.HasSuffix(address, ":443") {
				port = wordpressProofPorts.https
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
		// Caddy installs its internal CA in the host trust store. Verify the
		// certificate and hostname normally; a loopback dial is not a reason to
		// disable TLS authentication.
		TLSClientConfig:       &tls.Config{ServerName: hostname, RootCAs: roots, MinVersion: tls.VersionTLS12},
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Hostname() != hostname {
				return fmt.Errorf("%w: to %s", errProofRedirectedAway, request.URL.Host)
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	var checks []string
	home, err := probeWordPressPage(ctx, client, scheme+"://"+hostname+"/")
	if err != nil {
		return checks, fmt.Errorf("home page: %w", err)
	}
	if !strings.Contains(home, "/wp-content/") && !strings.Contains(home, "/wp-includes/") && !strings.Contains(home, "wp-json") {
		return checks, errors.New("home page does not look like WordPress output")
	}
	checks = append(checks, "home page rendered by WordPress from the restored database")
	login, err := probeWordPressPage(ctx, client, scheme+"://"+hostname+"/wp-login.php")
	if err != nil {
		return checks, fmt.Errorf("login page: %w", err)
	}
	if !strings.Contains(login, `name="log"`) {
		return checks, errors.New("login page has no WordPress login form")
	}
	checks = append(checks, "wp-login.php served the WordPress login form")
	return checks, nil
}

// errProofRedirectedAway reports that WordPress redirected the probe to
// another host, which it does when it ignores the proof's WP_HOME.
var errProofRedirectedAway = errors.New("redirected away from the proof hostname")

// probeWordPressPage retries while the web server and PHP-FPM finish
// reloading, then requires a 200 that is neither a database error nor the
// installer (which WordPress shows when the restored tables are missing).
func probeWordPressPage(ctx context.Context, client *http.Client, url string) (string, error) {
	var last error
	for attempt := 0; ; attempt++ {
		body, final, status, err := fetchWordPressPage(ctx, client, url)
		switch {
		case errors.Is(err, errProofRedirectedAway):
			return "", err
		case err != nil:
			last = err
		case status == http.StatusOK && strings.Contains(final, "/wp-admin/install.php"):
			return "", errors.New("WordPress showed its installer: the restored database has no WordPress tables for this configuration")
		case strings.Contains(body, "Error establishing a database connection"):
			return "", errors.New("WordPress could not connect to the restored database")
		case status == http.StatusOK:
			return body, nil
		case (status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusNotFound) && attempt < wordpressProofReloadRetries:
			// The route or PHP-FPM pool may still be reloading.
			last = fmt.Errorf("HTTP %d", status)
		default:
			return "", fmt.Errorf("HTTP %d", status)
		}
		delay := time.Duration(attempt+1) * 500 * time.Millisecond
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%w (last result: %v)", ctx.Err(), last)
		case <-time.After(delay):
		}
	}
}

func fetchWordPressPage(ctx context.Context, client *http.Client, url string) (string, string, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", 0, err
	}
	request.Header.Set("User-Agent", "StePanel-Recovery-Proof")
	response, err := client.Do(request)
	if err != nil {
		return "", "", 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, wordpressProofBodyLimit))
	if err != nil {
		return "", "", 0, err
	}
	return string(body), response.Request.URL.String(), response.StatusCode, nil
}
