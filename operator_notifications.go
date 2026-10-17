package stepanel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cyberducttape/StePanel/internal/safehttp"
)

const (
	operatorNotificationBatch       = 20
	operatorNotificationConcurrency = 4
	operatorNotificationPruneBatch  = 500
	operatorNotificationRetention   = 90 * 24 * time.Hour
	operatorNotificationTimeout     = 10 * time.Second
	operatorNotificationRetryMax    = 6 * time.Hour
	operatorNotificationPayloadName = "stepanel.operator-alert.v1"
)

type operatorNotificationStore struct{ db *sql.DB }

type operatorNotification struct {
	ID, JobID, JobKind, Owner, State string
	CreatedAt                        time.Time
	WebhookAttempts, EmailAttempts   int
	WebhookDelivered, EmailDelivered bool
	WebhookDue, EmailDue             bool
}

type operatorNotificationStatus struct {
	Configured       bool       `json:"configured"`
	Pending          int        `json:"pending"`
	FailedDeliveries int        `json:"failed_deliveries"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	OldestPendingAt  *time.Time `json:"oldest_pending_at,omitempty"`
	OldestPendingAge int64      `json:"oldest_pending_age_seconds"`
}

type operatorAlertPayload struct {
	Event     string    `json:"event"`
	EventID   string    `json:"event_id"`
	JobID     string    `json:"job_id"`
	JobKind   string    `json:"job_kind"`
	Resource  string    `json:"resource,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

func operatorAlertsConfigured(cfg Config) bool {
	return cfg.AlertWebhookURL != "" || cfg.AlertSMTPHost != ""
}

func validateOperatorAlertConfig(cfg Config) error {
	if cfg.AlertWebhookURL != "" {
		if err := (safehttp.Policy{}).ValidateURL(cfg.AlertWebhookURL); err != nil {
			return fmt.Errorf("STEPANEL_ALERT_WEBHOOK_URL: %w", err)
		}
		if len(cfg.AlertWebhookSecret) < 32 {
			return errors.New("STEPANEL_ALERT_WEBHOOK_SECRET must contain at least 32 characters when a webhook URL is configured")
		}
	} else if cfg.AlertWebhookSecret != "" {
		return errors.New("STEPANEL_ALERT_WEBHOOK_SECRET requires a webhook URL")
	}
	anySMTP := cfg.AlertEmailTo != "" || cfg.AlertEmailFrom != "" || cfg.AlertSMTPHost != "" || cfg.AlertSMTPUser != "" || cfg.AlertSMTPPassword != ""
	if !anySMTP {
		return nil
	}
	if cfg.AlertEmailTo == "" || cfg.AlertEmailFrom == "" || cfg.AlertSMTPHost == "" {
		return errors.New("SMTP alerts require STEPANEL_ALERT_EMAIL_TO, STEPANEL_ALERT_EMAIL_FROM, and STEPANEL_ALERT_SMTP_HOST")
	}
	if cfg.AlertSMTPPort < 1 || cfg.AlertSMTPPort > 65535 || strings.ContainsAny(cfg.AlertSMTPHost, "\r\n /\\") {
		return errors.New("STEPANEL_ALERT_SMTP_HOST or STEPANEL_ALERT_SMTP_PORT is invalid")
	}
	if (cfg.AlertSMTPUser == "") != (cfg.AlertSMTPPassword == "") {
		return errors.New("SMTP alert username and password must be configured together")
	}
	for name, value := range map[string]string{"STEPANEL_ALERT_EMAIL_TO": cfg.AlertEmailTo, "STEPANEL_ALERT_EMAIL_FROM": cfg.AlertEmailFrom} {
		address, err := mail.ParseAddress(value)
		if err != nil || address.Address != value || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must be a single email address", name)
		}
	}
	return nil
}

func newOperatorNotificationStore(db *sql.DB, cfg Config) (*operatorNotificationStore, error) {
	if db == nil {
		return nil, errors.New("operator notification database is required")
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='operator_notification_events'`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check operator notification schema: %w", err)
	}
	if exists != 1 {
		return nil, errors.New("operator notification schema is not initialized")
	}
	if _, err := db.Exec(`UPDATE operator_notification_policy SET webhook_required=?, email_required=? WHERE id=1`, boolInt(cfg.AlertWebhookURL != ""), boolInt(cfg.AlertSMTPHost != "")); err != nil {
		return nil, fmt.Errorf("configure required operator alert channels: %w", err)
	}
	return &operatorNotificationStore{db: db}, nil
}

func (s *operatorNotificationStore) pending(ctx context.Context, cfg Config, limit int) ([]operatorNotification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("operator notification store is unavailable")
	}
	if !operatorAlertsConfigured(cfg) {
		return []operatorNotification{}, nil
	}
	if limit < 1 || limit > operatorNotificationBatch {
		limit = operatorNotificationBatch
	}
	now := time.Now().UTC().Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, job_kind, owner, state, created_at, webhook_attempts, email_attempts, webhook_delivered_at IS NOT NULL, email_delivered_at IS NOT NULL,
		(webhook_required=1 AND webhook_delivered_at IS NULL AND webhook_next_attempt_at <= ? AND ?=1),
		(email_required=1 AND email_delivered_at IS NULL AND email_next_attempt_at <= ? AND ?=1)
		FROM operator_notification_events
		WHERE (webhook_required=1 AND webhook_delivered_at IS NULL AND webhook_next_attempt_at <= ? AND ?=1)
		OR (email_required=1 AND email_delivered_at IS NULL AND email_next_attempt_at <= ? AND ?=1)
		ORDER BY created_at, id LIMIT ?`, now, boolInt(cfg.AlertWebhookURL != ""), now, boolInt(cfg.AlertSMTPHost != ""), now, boolInt(cfg.AlertWebhookURL != ""), now, boolInt(cfg.AlertSMTPHost != ""), limit)
	if err != nil {
		return nil, fmt.Errorf("read operator notification outbox: %w", err)
	}
	defer rows.Close()
	items := make([]operatorNotification, 0, limit)
	for rows.Next() {
		var item operatorNotification
		var created int64
		if err := rows.Scan(&item.ID, &item.JobID, &item.JobKind, &item.Owner, &item.State, &created, &item.WebhookAttempts, &item.EmailAttempts, &item.WebhookDelivered, &item.EmailDelivered, &item.WebhookDue, &item.EmailDue); err != nil {
			return nil, fmt.Errorf("scan operator notification outbox: %w", err)
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operator notification outbox: %w", err)
	}
	return items, nil
}

func (s *operatorNotificationStore) flush(ctx context.Context, cfg Config) error {
	if err := validateOperatorAlertConfig(cfg); err != nil {
		return err
	}
	if !operatorAlertsConfigured(cfg) {
		return s.pruneDelivered(ctx)
	}
	items, err := s.pending(ctx, cfg, operatorNotificationBatch)
	if err != nil {
		return err
	}
	type delivery struct {
		item    operatorNotification
		channel string
		payload operatorAlertPayload
		body    []byte
	}
	deliveries := make([]delivery, 0, len(items)*2)
	for _, item := range items {
		payload := operatorAlertPayload{Event: operatorNotificationPayloadName, EventID: item.ID, JobID: item.JobID, JobKind: item.JobKind, Resource: item.Owner, State: item.State, CreatedAt: item.CreatedAt}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode operator alert: %w", err)
		}
		if item.WebhookDue {
			deliveries = append(deliveries, delivery{item: item, channel: "webhook", payload: payload, body: body})
		}
		if item.EmailDue {
			deliveries = append(deliveries, delivery{item: item, channel: "email", payload: payload})
		}
	}
	semaphore := make(chan struct{}, operatorNotificationConcurrency)
	var workers sync.WaitGroup
	var errorMu sync.Mutex
	var deliveryErrors []error
	for _, task := range deliveries {
		workers.Add(1)
		go func(task delivery) {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			var deliveryErr error
			if task.channel == "webhook" {
				deliveryErr = deliverOperatorWebhook(ctx, cfg, task.item.ID, task.body)
			} else {
				deliveryErr = deliverOperatorEmail(ctx, cfg, task.payload)
			}
			if deliveryErr != nil {
				attempts := task.item.EmailAttempts
				if task.channel == "webhook" {
					attempts = task.item.WebhookAttempts
				}
				if updateErr := s.recordFailure(ctx, task.item.ID, task.channel, attempts, deliveryErr); updateErr != nil {
					errorMu.Lock()
					deliveryErrors = append(deliveryErrors, errors.Join(deliveryErr, updateErr))
					errorMu.Unlock()
				}
				return
			}
			if err := s.markDelivered(ctx, task.item.ID, task.channel); err != nil {
				errorMu.Lock()
				deliveryErrors = append(deliveryErrors, err)
				errorMu.Unlock()
			}
		}(task)
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := errors.Join(deliveryErrors...); err != nil {
		return err
	}
	return s.pruneDelivered(ctx)
}

func (s *operatorNotificationStore) markDelivered(ctx context.Context, id, channel string) error {
	column := "webhook_delivered_at"
	if channel == "email" {
		column = "email_delivered_at"
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE operator_notification_events SET `+column+`=?, last_`+channel+`_error='' WHERE id=?`, time.Now().UTC().Unix(), id); err != nil {
		return fmt.Errorf("record operator alert delivery: %w", err)
	}
	return nil
}

func (s *operatorNotificationStore) recordFailure(ctx context.Context, id, channel string, attempts int, cause error) error {
	if channel != "webhook" && channel != "email" {
		return errors.New("unknown operator alert channel")
	}
	seconds := 1 << min(attempts, 15)
	delay := min(time.Duration(seconds)*time.Second, operatorNotificationRetryMax)
	_, err := s.db.ExecContext(ctx, `UPDATE operator_notification_events SET `+channel+`_attempts=`+channel+`_attempts+1, `+channel+`_next_attempt_at=?, last_`+channel+`_error=?, last_`+channel+`_failed_at=? WHERE id=?`, time.Now().UTC().Add(delay).Unix(), safeNotificationError(cause), time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("record operator alert delivery failure: %w", err)
	}
	return nil
}

func safeNotificationError(err error) string {
	if err == nil {
		return "delivery failed"
	}
	// Provider errors can contain request URLs or mailbox details. The admin
	// status only needs a stable failure category; secrets stay in config.
	return "delivery failed: " + notificationErrorCategory(err)
}

func notificationErrorCategory(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "timeout") || strings.Contains(text, "deadline"):
		return "timeout"
	case strings.Contains(text, "certificate") || strings.Contains(text, "tls"):
		return "tls"
	case strings.Contains(text, "status"):
		return "receiver rejected the request"
	default:
		return "transport or authentication error"
	}
}

// transportFailureClass names the class of an HTTP transport error without
// echoing its text.
func transportFailureClass(err error) string {
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.As(err, &certErr) || strings.Contains(strings.ToLower(err.Error()), "tls"):
		return "tls"
	default:
		return "connection"
	}
}

func (s *operatorNotificationStore) pruneDelivered(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-operatorNotificationRetention).Unix()
	result, err := s.db.ExecContext(ctx, `DELETE FROM operator_notification_events WHERE id IN (
		SELECT id FROM operator_notification_events
		WHERE created_at < ?
		AND (webhook_required=0 OR webhook_delivered_at IS NOT NULL)
		AND (email_required=0 OR email_delivered_at IS NOT NULL)
		ORDER BY created_at, id LIMIT ?
	)`, cutoff, operatorNotificationPruneBatch)
	if err != nil {
		return fmt.Errorf("prune delivered operator alerts: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count pruned operator alerts: %w", err)
	}
	if deleted > 0 {
		log.Printf("pruned %d completed operator alert outbox row(s) older than %s", deleted, operatorNotificationRetention)
	}
	return nil
}

func (s *operatorNotificationStore) status(ctx context.Context, cfg Config) (operatorNotificationStatus, error) {
	status := operatorNotificationStatus{Configured: operatorAlertsConfigured(cfg)}
	if s == nil || s.db == nil {
		return status, errors.New("operator notification store is unavailable")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_notification_events WHERE (webhook_required=1 AND webhook_delivered_at IS NULL) OR (email_required=1 AND email_delivered_at IS NULL)`).Scan(&status.Pending); err != nil {
		return status, err
	}
	var last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(MAX(COALESCE(last_webhook_failed_at, 0), COALESCE(last_email_failed_at, 0))) FROM operator_notification_events WHERE ((webhook_required=1 AND webhook_delivered_at IS NULL AND last_webhook_error <> '') OR (email_required=1 AND email_delivered_at IS NULL AND last_email_error <> ''))`).Scan(&status.FailedDeliveries, &last); err != nil {
		return status, err
	}
	if last.Valid {
		at := time.Unix(last.Int64, 0).UTC()
		status.LastFailureAt = &at
	}
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM operator_notification_events WHERE (webhook_required=1 AND webhook_delivered_at IS NULL) OR (email_required=1 AND email_delivered_at IS NULL)`).Scan(&oldest); err != nil {
		return status, err
	}
	if oldest.Valid {
		at := time.Unix(oldest.Int64, 0).UTC()
		status.OldestPendingAt = &at
		status.OldestPendingAge = max(int64(time.Since(at).Seconds()), 0)
	}
	return status, nil
}

func deliverOperatorWebhook(parent context.Context, cfg Config, id string, body []byte) error {
	policy := safehttp.Policy{}
	if err := policy.ValidateURL(cfg.AlertWebhookURL); err != nil {
		return err
	}
	client := policy.Client(operatorNotificationTimeout, 0, safehttp.TransportOptions{DialTimeout: operatorNotificationTimeout, TLSHandshakeTimeout: operatorNotificationTimeout, ResponseHeaderTimeout: operatorNotificationTimeout})
	return deliverOperatorWebhookWithClient(parent, cfg, id, body, policy, client)
}

func deliverOperatorWebhookWithClient(parent context.Context, cfg Config, id string, body []byte, policy safehttp.Policy, client *http.Client) error {
	if err := policy.ValidateURL(cfg.AlertWebhookURL); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, operatorNotificationTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.AlertWebhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("build webhook request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "StePanel-Operator-Alerts")
	request.Header.Set("X-StePanel-Event-ID", id)
	if cfg.AlertWebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.AlertWebhookSecret))
		_, _ = mac.Write(body)
		request.Header.Set("X-StePanel-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := client.Do(request)
	if err != nil {
		// Keep only the failure class: the transport error can carry the
		// webhook URL, which may embed a token.
		return errors.New("webhook transport failed: " + transportFailureClass(err))
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook status %d", response.StatusCode)
	}
	return nil
}

func deliverOperatorEmail(parent context.Context, cfg Config, alert operatorAlertPayload) error {
	return deliverOperatorEmailWithTLSConfig(parent, cfg, alert, &tls.Config{ServerName: cfg.AlertSMTPHost, MinVersion: tls.VersionTLS12})
}

// deliverOperatorEmailWithTLSConfig keeps the production TLS policy explicit
// while allowing the SMTP protocol test to trust its own ephemeral test CA.
func deliverOperatorEmailWithTLSConfig(parent context.Context, cfg Config, alert operatorAlertPayload, tlsConfig *tls.Config) error {
	if tlsConfig == nil {
		return errors.New("SMTP TLS configuration is required")
	}
	tlsConfig = tlsConfig.Clone()
	tlsConfig.ServerName = cfg.AlertSMTPHost
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	ctx, cancel := context.WithTimeout(parent, operatorNotificationTimeout)
	defer cancel()
	address := net.JoinHostPort(cfg.AlertSMTPHost, strconv.Itoa(cfg.AlertSMTPPort))
	conn, err := (&net.Dialer{Timeout: operatorNotificationTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, cfg.AlertSMTPHost)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server does not offer STARTTLS")
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		return errors.New("SMTP TLS negotiation failed")
	}
	if cfg.AlertSMTPUser != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.AlertSMTPUser, cfg.AlertSMTPPassword, cfg.AlertSMTPHost)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	from, _ := mail.ParseAddress(cfg.AlertEmailFrom)
	to, _ := mail.ParseAddress(cfg.AlertEmailTo)
	if err := client.Mail(from.Address); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err := client.Rcpt(to.Address); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message was rejected")
	}
	subject := "StePanel operator job alert"
	body := fmt.Sprintf("StePanel operator alert\n\nJob: %s\nType: %s\nResource: %s\nState: %s\nTime: %s\n\nReview the job in the StePanel Job Center. Error details are intentionally omitted from email.\n", alert.JobID, alert.JobKind, alert.Resource, alert.State, alert.CreatedAt.Format(time.RFC3339))
	message := "From: " + from.Address + "\r\nTo: " + to.Address + "\r\nSubject: " + subject + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return errors.New("SMTP message write failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP message finalization failed")
	}
	if err := client.Quit(); err != nil {
		return errors.New("SMTP session close failed")
	}
	return nil
}

func (a *App) runOperatorNotificationLoop(ctx context.Context) {
	if a.Notifications == nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	flush := func() {
		flushCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := a.Notifications.flush(flushCtx, a.Config); err != nil {
			log.Printf("operator notification delivery deferred: %s", safeNotificationError(err))
		}
	}
	flush()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			flush()
		}
	}
}
