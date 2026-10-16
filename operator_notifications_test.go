package stepanel

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/safehttp"
)

func TestTerminalJobTransitionsCreateDurableOperatorAlertEvents(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := newOperatorNotificationStore(db, Config{AlertWebhookURL: "https://alerts.example.test/hook"}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO jobs(id, kind, operation_key, state, owner, started_at, payload, cancel_requested, updated_at)
		VALUES ('job-1', 'site.restore', '', 'queued', 'shop', ?, '{}', 0, unixepoch())`, time.Now().UTC().UnixNano()); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var value int
		if err := db.QueryRow(`SELECT COUNT(*) FROM operator_notification_events`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := count(); got != 0 {
		t.Fatalf("queued job created %d alerts, want none", got)
	}
	if _, err := db.Exec(`UPDATE jobs SET state='dead-letter', updated_at=unixepoch() WHERE id='job-1'`); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 1 {
		t.Fatalf("first dead-letter transition created %d alerts, want 1", got)
	}
	if _, err := db.Exec(`UPDATE jobs SET state='queued', updated_at=unixepoch() WHERE id='job-1'; UPDATE jobs SET state='dead-letter', updated_at=unixepoch() WHERE id='job-1'`); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 2 {
		t.Fatalf("second dead-letter transition created %d alerts, want 2", got)
	}
	var jobID, kind, owner, state string
	if err := db.QueryRow(`SELECT job_id, job_kind, owner, state FROM operator_notification_events ORDER BY created_at, id LIMIT 1`).Scan(&jobID, &kind, &owner, &state); err != nil {
		t.Fatal(err)
	}
	if jobID != "job-1" || kind != "site.restore" || owner != "shop" || state != "dead-letter" {
		t.Fatalf("captured alert = %q %q %q %q", jobID, kind, owner, state)
	}
	var webhookRequired, emailRequired int
	if err := db.QueryRow(`SELECT webhook_required, email_required FROM operator_notification_events ORDER BY created_at, id LIMIT 1`).Scan(&webhookRequired, &emailRequired); err != nil {
		t.Fatal(err)
	}
	if webhookRequired != 1 || emailRequired != 0 {
		t.Fatalf("captured channel requirements = webhook:%d email:%d; want webhook-only", webhookRequired, emailRequired)
	}
	columns, err := db.Query(`SELECT name FROM pragma_table_info('operator_notification_events')`)
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	var names []string
	for columns.Next() {
		var name string
		if err := columns.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := columns.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "payload" || name == "job_error" || name == "error_detail" {
			t.Fatalf("operator outbox stores sensitive job data field %q: %v", name, names)
		}
	}
}

func TestOperatorNotificationOutboxCapturesExistingDeadLetters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-alerts.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TRIGGER jobs_alert_on_terminal_insert; DROP TRIGGER jobs_alert_on_terminal_transition;
		DROP TABLE operator_notification_events; DELETE FROM control_plane_migrations WHERE version=14;
		INSERT INTO jobs(id, kind, operation_key, state, owner, started_at, finished_at, payload, cancel_requested, updated_at)
		VALUES ('dead-1', 'site.delete', '', 'dead-letter', 'shop', ?, unixepoch(), '{}', 0, unixepoch())`, time.Now().UTC().UnixNano()); err != nil {
		t.Fatal(err)
	}
	// Migration from the preceding schema seeds unresolved dead-letter rows.
	if err := runControlPlaneMigrations(db, path); err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM operator_notification_events WHERE job_id='dead-1'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration created %d events for existing dead-letter, want 1", count)
	}
}

func TestOperatorNotificationMigrationPreservesKnownChannelsAndDiscardsAmbiguousLegacyRequirements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-alert-channel.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := time.Now().Add(-operatorNotificationRetention - time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO operator_notification_events(id,job_id,job_kind,owner,state,created_at,webhook_delivered_at,email_delivered_at,webhook_attempts,email_attempts,webhook_required,email_required,webhook_next_attempt_at,email_next_attempt_at,last_webhook_error,last_email_error)
		VALUES ('known','j1','site.restore','shop','failed',?, ?, NULL, 1, 0, 0, 0, 0, 0, '', ''),
		('ambiguous','j2','site.restore','shop','failed',?, NULL, NULL, 0, 0, 0, 0, 0, 0, '', '')`, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER jobs_alert_on_terminal_insert; DROP TRIGGER jobs_alert_on_terminal_transition;
		DROP TABLE operator_notification_policy;
		UPDATE operator_notification_events SET webhook_required=0,email_required=0;
		DELETE FROM control_plane_migrations WHERE version=19`); err != nil {
		t.Fatal(err)
	}
	if err := runControlPlaneMigrations(db, path); err != nil {
		t.Fatalf("reapply alert channel migration: %v", err)
	}
	var knownWebhook, knownEmail, ambiguousWebhook, ambiguousEmail int
	if err := db.QueryRow(`SELECT webhook_required,email_required FROM operator_notification_events WHERE id='known'`).Scan(&knownWebhook, &knownEmail); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT webhook_required,email_required FROM operator_notification_events WHERE id='ambiguous'`).Scan(&ambiguousWebhook, &ambiguousEmail); err != nil {
		t.Fatal(err)
	}
	if knownWebhook != 1 || knownEmail != 0 || ambiguousWebhook != 0 || ambiguousEmail != 0 {
		t.Fatalf("legacy channel migration: known=%d/%d ambiguous=%d/%d", knownWebhook, knownEmail, ambiguousWebhook, ambiguousEmail)
	}
}

func TestOperatorNotificationRetentionUsesRequiredChannelsAndPrunesWithoutConfiguration(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "alert-retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := newOperatorNotificationStore(db, Config{})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-operatorNotificationRetention - time.Hour).Unix()
	recent := time.Now().UTC().Unix()
	for i := 0; i < operatorNotificationPruneBatch+1; i++ {
		if _, err := db.Exec(`INSERT INTO operator_notification_events(id,job_id,job_kind,owner,state,created_at,webhook_delivered_at,email_delivered_at,webhook_required,email_required,webhook_next_attempt_at,email_next_attempt_at)
			VALUES (?,?,'site.restore','shop','failed',?, ?, NULL, 1, 0, 0, 0)`, fmt.Sprintf("delivered-%d", i), fmt.Sprintf("job-%d", i), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO operator_notification_events(id,job_id,job_kind,owner,state,created_at,webhook_delivered_at,email_delivered_at,webhook_required,email_required,webhook_next_attempt_at,email_next_attempt_at)
		VALUES ('pending','pending','site.restore','shop','failed',?,NULL,NULL,1,0,0,0),
		('unconfigured','unconfigured','site.restore','shop','failed',?,NULL,NULL,0,0,0,0),
		('recent','recent','site.restore','shop','failed',?,?,NULL,1,0,0,0)`, old, old, recent, recent); err != nil {
		t.Fatal(err)
	}
	if err := store.flush(context.Background(), Config{}); err != nil {
		t.Fatalf("prune without configured channels: %v", err)
	}
	var agedCompleted, pending, unconfigured, recentCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_notification_events WHERE id LIKE 'delivered-%'`).Scan(&agedCompleted); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_notification_events WHERE id='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_notification_events WHERE id='unconfigured'`).Scan(&unconfigured); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_notification_events WHERE id='recent'`).Scan(&recentCount); err != nil {
		t.Fatal(err)
	}
	if agedCompleted != 1 || pending != 1 || unconfigured != 1 || recentCount != 1 {
		t.Fatalf("after bounded no-channel prune: aged completed=%d pending=%d unconfigured=%d recent=%d; want 1,1,1,1", agedCompleted, pending, unconfigured, recentCount)
	}
	if err := store.flush(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_notification_events WHERE id LIKE 'delivered-%'`).Scan(&agedCompleted); err != nil {
		t.Fatal(err)
	}
	if agedCompleted != 0 {
		t.Fatalf("second batch left %d aged completed events", agedCompleted)
	}
	webhookCfg := Config{AlertWebhookURL: "https://alerts.example.test/hook", AlertWebhookSecret: strings.Repeat("s", 32)}
	items, err := store.pending(context.Background(), webhookCfg, operatorNotificationBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "pending" {
		t.Fatalf("later webhook configuration would deliver stale or unconfigured events: %+v", items)
	}
}

func TestValidateOperatorAlertConfig(t *testing.T) {
	valid := Config{WebServer: "caddy", AlertWebhookURL: "https://alerts.example.test/stepanel", AlertWebhookSecret: strings.Repeat("s", 32)}
	if err := validateOperatorAlertConfig(valid); err != nil {
		t.Fatalf("valid signed webhook config rejected: %v", err)
	}
	for _, config := range []Config{
		{WebServer: "caddy", AlertWebhookURL: "http://127.0.0.1/alerts"},
		{WebServer: "caddy", AlertWebhookURL: "https://alerts.example.test/alerts"},
		{WebServer: "caddy", AlertWebhookURL: "https://alerts.example.test/alerts", AlertWebhookSecret: "short"},
		{WebServer: "caddy", AlertEmailTo: "operator@example.test"},
		{WebServer: "caddy", AlertEmailTo: "operator@example.test\r\nBcc: attacker@example.test", AlertEmailFrom: "panel@example.test", AlertSMTPHost: "smtp.example.test"},
	} {
		if err := validateOperatorAlertConfig(config); err == nil {
			t.Errorf("unsafe/incomplete alert config accepted: %+v", config)
		}
	}
	validSMTP := Config{WebServer: "caddy", AlertEmailTo: "operator@example.test", AlertEmailFrom: "panel@example.test", AlertSMTPHost: "smtp.example.test", AlertSMTPPort: 587}
	if err := validateOperatorAlertConfig(validSMTP); err != nil {
		t.Fatalf("valid STARTTLS SMTP config rejected: %v", err)
	}
	validSMTP.AlertSMTPPort = -1
	if err := validateOperatorAlertConfig(validSMTP); err == nil {
		t.Fatal("invalid SMTP port accepted")
	}
}

func TestOperatorWebhookDeliversSignedEventOverTLS(t *testing.T) {
	secret := strings.Repeat("k", 32)
	var gotID, gotSignature, gotBody string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("X-StePanel-Event-ID")
		gotSignature = r.Header.Get("X-StePanel-Signature")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := Config{AlertWebhookURL: server.URL, AlertWebhookSecret: secret}
	policy := safehttp.Policy{AllowPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	body := []byte(`{"event":"stepanel.operator-alert.v1","state":"dead-letter"}`)
	if err := deliverOperatorWebhookWithClient(t.Context(), cfg, "event-123", body, policy, server.Client()); err != nil {
		t.Fatalf("deliver operator webhook: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotID != "event-123" || !hmac.Equal([]byte(gotSignature), []byte(want)) || gotBody != string(body) {
		t.Fatalf("webhook id/signature/body = %q %q %q", gotID, gotSignature, gotBody)
	}
}

func TestOperatorEmailDeliversUsingVerifiedSTARTTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "StePanel SMTP test"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := x509.Certificate{Raw: der}
	serverCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: &certificate}
	roots := x509.NewCertPool()
	roots.AddCert(&certificate)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	captured := make(chan string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		if _, writeErr := io.WriteString(conn, "220 smtp.test ESMTP\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		reader := bufio.NewReader(conn)
		line, readErr := reader.ReadString('\n')
		if readErr != nil || !strings.HasPrefix(line, "EHLO ") {
			serverErr <- fmt.Errorf("expected initial EHLO, got %q: %v", line, readErr)
			return
		}
		if _, writeErr := io.WriteString(conn, "250-smtp.test\r\n250-STARTTLS\r\n250 SIZE 65536\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		line, readErr = reader.ReadString('\n')
		if readErr != nil || strings.TrimSpace(line) != "STARTTLS" {
			serverErr <- fmt.Errorf("expected STARTTLS, got %q: %v", line, readErr)
			return
		}
		if _, writeErr := io.WriteString(conn, "220 begin TLS\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12})
		if handshakeErr := tlsConn.Handshake(); handshakeErr != nil {
			serverErr <- handshakeErr
			return
		}
		conn = tlsConn
		reader = bufio.NewReader(conn)
		line, readErr = reader.ReadString('\n')
		if readErr != nil || !strings.HasPrefix(line, "EHLO ") {
			serverErr <- fmt.Errorf("expected post-TLS EHLO, got %q: %v", line, readErr)
			return
		}
		if _, writeErr := io.WriteString(conn, "250-smtp.test\r\n250-AUTH PLAIN\r\n250 SIZE 65536\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		line, readErr = reader.ReadString('\n')
		if readErr != nil || !strings.HasPrefix(line, "AUTH PLAIN ") {
			serverErr <- fmt.Errorf("expected AUTH PLAIN after TLS, got %q: %v", line, readErr)
			return
		}
		credentials, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(line, "AUTH PLAIN ")))
		if decodeErr != nil || string(credentials) != "\x00smtp-user\x00smtp-password" {
			serverErr <- fmt.Errorf("SMTP credentials were not protected by the expected TLS session: %q (%v)", credentials, decodeErr)
			return
		}
		if _, writeErr := io.WriteString(conn, "235 2.7.0 authenticated\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		for _, expected := range []string{"MAIL FROM:<panel@example.test>", "RCPT TO:<operator@example.test>"} {
			line, readErr = reader.ReadString('\n')
			if readErr != nil || strings.TrimSpace(line) != expected {
				serverErr <- fmt.Errorf("expected %q, got %q: %v", expected, line, readErr)
				return
			}
			if _, writeErr := io.WriteString(conn, "250 accepted\r\n"); writeErr != nil {
				serverErr <- writeErr
				return
			}
		}
		line, readErr = reader.ReadString('\n')
		if readErr != nil || strings.TrimSpace(line) != "DATA" {
			serverErr <- fmt.Errorf("expected DATA, got %q: %v", line, readErr)
			return
		}
		if _, writeErr := io.WriteString(conn, "354 end with dot\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		var message strings.Builder
		for {
			line, readErr = reader.ReadString('\n')
			if readErr != nil {
				serverErr <- readErr
				return
			}
			if line == ".\r\n" {
				break
			}
			message.WriteString(line)
		}
		captured <- message.String()
		if _, writeErr := io.WriteString(conn, "250 queued\r\n"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		line, readErr = reader.ReadString('\n')
		if readErr != nil || strings.TrimSpace(line) != "QUIT" {
			serverErr <- fmt.Errorf("expected QUIT, got %q: %v", line, readErr)
			return
		}
		_, serverWriteErr := io.WriteString(conn, "221 bye\r\n")
		serverErr <- serverWriteErr
	}()

	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AlertEmailTo:      "operator@example.test",
		AlertEmailFrom:    "panel@example.test",
		AlertSMTPHost:     "127.0.0.1",
		AlertSMTPPort:     port,
		AlertSMTPUser:     "smtp-user",
		AlertSMTPPassword: "smtp-password",
	}
	alert := operatorAlertPayload{JobID: "job-123", JobKind: "site.restore", Resource: "demo", State: "dead-letter", CreatedAt: now.UTC()}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := deliverOperatorEmailWithTLSConfig(ctx, cfg, alert, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatalf("deliver STARTTLS operator email: %v", err)
	}
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("SMTP server protocol: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP server did not finish protocol exchange")
	}
	select {
	case message := <-captured:
		for _, expected := range []string{"To: operator@example.test", "From: panel@example.test", "Subject: StePanel operator job alert", "Job: job-123", "Type: site.restore", "Resource: demo", "State: dead-letter", "Error details are intentionally omitted"} {
			if !strings.Contains(message, expected) {
				t.Errorf("SMTP message is missing %q: %s", expected, message)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP server did not capture the delivered message")
	}
}

func TestOperatorNotificationDeliveryFailuresAreDurableAndRedacted(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "alert-delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := Config{AlertWebhookURL: "https://alerts.example.test/hook"}
	store, err := newOperatorNotificationStore(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO operator_notification_events(id, job_id, job_kind, owner, state, created_at, webhook_next_attempt_at, email_next_attempt_at, webhook_required)
		VALUES ('event-1','job-1','site.restore','shop','failed',unixepoch(),unixepoch(),unixepoch(),1)`); err != nil {
		t.Fatal(err)
	}
	status, err := store.status(t.Context(), cfg)
	if err != nil || status.Pending != 1 || status.FailedDeliveries != 0 {
		t.Fatalf("initial notification status = %+v, err=%v", status, err)
	}
	if err := store.recordFailure(t.Context(), "event-1", "webhook", 0, errors.New("secret=must-not-be-persisted")); err != nil {
		t.Fatal(err)
	}
	var lastError string
	if err := db.QueryRow(`SELECT last_webhook_error FROM operator_notification_events WHERE id='event-1'`).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lastError, "must-not-be-persisted") {
		t.Fatalf("notification outbox retained raw delivery error: %q", lastError)
	}
	var failedAt sql.NullInt64
	if err := db.QueryRow(`SELECT last_webhook_failed_at FROM operator_notification_events WHERE id='event-1'`).Scan(&failedAt); err != nil {
		t.Fatal(err)
	}
	if !failedAt.Valid || time.Since(time.Unix(failedAt.Int64, 0)) > time.Minute {
		t.Fatalf("delivery failure timestamp was not recorded accurately: %+v", failedAt)
	}
	status, err = store.status(t.Context(), cfg)
	if err != nil || status.Pending != 1 || status.FailedDeliveries != 1 || status.LastFailureAt == nil {
		t.Fatalf("failed notification status = %+v, err=%v", status, err)
	}
	if err := store.markDelivered(t.Context(), "event-1", "webhook"); err != nil {
		t.Fatal(err)
	}
	status, err = store.status(t.Context(), cfg)
	if err != nil || status.Pending != 0 || status.FailedDeliveries != 0 {
		t.Fatalf("delivered notification status = %+v, err=%v", status, err)
	}
}

func TestOperatorNotificationChannelsUseIndependentRetryDeadlines(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "alert-retries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := Config{AlertWebhookURL: "https://alerts.example.test/hook", AlertSMTPHost: "smtp.example.test", AlertSMTPPort: 587, AlertEmailTo: "ops@example.test", AlertEmailFrom: "panel@example.test"}
	store, err := newOperatorNotificationStore(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO operator_notification_events(id, job_id, job_kind, owner, state, created_at, webhook_next_attempt_at, email_next_attempt_at, webhook_required, email_required)
		VALUES ('event-1','job-1','site.restore','shop','failed',unixepoch(),unixepoch(),unixepoch(),1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := store.recordFailure(t.Context(), "event-1", "webhook", 0, errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if err := store.markDelivered(t.Context(), "event-1", "email"); err != nil {
		t.Fatal(err)
	}
	items, err := store.pending(t.Context(), cfg, operatorNotificationBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("webhook retry became due immediately after email delivery: %+v", items)
	}
	var webhookNext, emailDelivered sql.NullInt64
	if err := db.QueryRow(`SELECT webhook_next_attempt_at, email_delivered_at FROM operator_notification_events WHERE id='event-1'`).Scan(&webhookNext, &emailDelivered); err != nil {
		t.Fatal(err)
	}
	if !webhookNext.Valid || !emailDelivered.Valid || webhookNext.Int64 <= time.Now().UTC().Unix() {
		t.Fatalf("independent retry state incorrect: webhook next=%+v email delivered=%+v", webhookNext, emailDelivered)
	}
}
