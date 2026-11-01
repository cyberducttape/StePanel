package stepanel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ChangePreview is the common response contract for high-risk mutations.
// Tokens bind the requested operation to both its observed prior state and
// proposed state, and expire quickly so stale previews cannot authorize a
// later change.
type ChangePreview struct {
	Action             string                    `json:"action"`
	Summary            string                    `json:"summary"`
	Before             any                       `json:"before"`
	Proposed           any                       `json:"proposed"`
	Effects            []string                  `json:"effects"`
	ValidationCommands []ChangeValidationCommand `json:"validation_commands"`
	InterruptionPoints []ChangeInterruptionPoint `json:"expected_interruptions"`
	DesiredStateProof  []string                  `json:"desired_state_proof"`
	RecoveryPlan       []string                  `json:"recovery_plan"`
	PreviewToken       string                    `json:"preview_token"`
	ExpiresAt          string                    `json:"expires_at"`
}

type ChangeValidationCommand struct {
	When      string `json:"when"` // before, during, after
	Command   string `json:"command"`
	Expected  string `json:"expected"`
	Automated bool   `json:"automated"`
}

type ChangeInterruptionPoint struct {
	Point    string `json:"point"`
	Expected string `json:"expected"`
	Recovery string `json:"recovery"`
}

const changePreviewTTL = 5 * time.Minute

func (a *App) makeChangePreview(action string, before, proposed any, effects, recovery []string) (ChangePreview, error) {
	preview := ChangePreview{
		Action: action, Summary: action, Before: before, Proposed: proposed,
		Effects: effects, RecoveryPlan: recovery,
	}
	preview.ValidationCommands, preview.InterruptionPoints, preview.DesiredStateProof = changePreviewEvidence(action, proposed)
	expires := time.Now().UTC().Add(changePreviewTTL).Unix()
	message, err := json.Marshal(struct {
		Action   string `json:"action"`
		Before   any    `json:"before"`
		Proposed any    `json:"proposed"`
		Expires  int64  `json:"expires"`
	}{action, before, proposed, expires})
	if err != nil {
		return ChangePreview{}, err
	}
	mac := hmac.New(sha256.New, []byte(a.Auth.Secret))
	_, _ = mac.Write(message)
	preview.PreviewToken = fmt.Sprintf("%d.%s", expires, hex.EncodeToString(mac.Sum(nil)))
	preview.ExpiresAt = time.Unix(expires, 0).UTC().Format(time.RFC3339)
	return preview, nil
}

func changePreviewEvidence(action string, proposed any) ([]ChangeValidationCommand, []ChangeInterruptionPoint, []string) {
	validation := []ChangeValidationCommand{{
		When: "before", Command: "StePanel validates the signed preview token against the current observed state and proposed configuration",
		Expected: "The preview is current, unexpired, and the before-state has not changed.", Automated: true,
	}}
	interruptions := []ChangeInterruptionPoint{{
		Point: "Before the first host mutation", Expected: "No host configuration change has been applied.",
		Recovery: "Discard the expired preview and generate a new one from current state.",
	}}
	proof := []string{"The operation response must report the persisted desired state and the host helper's successful validation/apply result; a queued or pending result is not success."}

	var site, webserver, path string
	if encoded, err := json.Marshal(proposed); err == nil {
		var object map[string]any
		if json.Unmarshal(encoded, &object) == nil {
			site, _ = object["site"].(string)
			if site == "" {
				site, _ = object["target_site"].(string)
			}
			webserver, _ = object["web_server"].(string)
			path, _ = object["vhost_path"].(string)
		}
	}
	if site == "" {
		if _, suffix, ok := strings.Cut(action, ":"); ok {
			site = suffix
		}
	}

	switch {
	case strings.HasPrefix(action, "php.runtime:"):
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "Root broker runtime operation: validate generated PHP-FPM pool, then reload the selected PHP-FPM service", Expected: "The pool configuration is valid and the runtime helper exits successfully.", Automated: true})
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "stepanelctl site inspect " + site, Expected: "The site reports the requested PHP runtime and applied profile state.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "After desired profile persistence, before runtime apply completes", Expected: "The profile remains explicitly pending reconciliation; the request must not report success.", Recovery: "Startup reconciliation reapplies the saved profile. Inspect the PHP-FPM pool and service before retrying if it remains pending."})
		proof = []string{"PHP profile state is `applied` only after the managed runtime helper succeeds and the final state write succeeds.", "Run the listed site inspection command to confirm the desired version and profile after the request."}
	case strings.HasPrefix(action, "domain.route."):
		if webserver == "apache" {
			validation = append(validation, ChangeValidationCommand{When: "after", Command: "apachectl configtest", Expected: "Syntax is OK and the managed route helper reports successful reload.", Automated: false})
		} else {
			validation = append(validation, ChangeValidationCommand{When: "after", Command: "caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile", Expected: "Caddy accepts the complete configuration and the managed route helper reports successful reload.", Automated: false})
		}
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "StePanel managed route helper: validate selected web-server configuration, then apply/reload", Expected: "Helper returns success; route desired state is persisted as applied (or removed for deletion).", Automated: true})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "After recovery snapshot, during validation or reload", Expected: "The previous vhost snapshot remains available; a failed helper does not claim the route reached its desired state.", Recovery: "Inspect route desired state and web-server logs, restore the recorded snapshot if required, run the listed syntax check, and reload the service."})
		proof = []string{"The response is returned only after the route helper succeeds and managed route state is persisted.", "Use the listed web-server syntax check and an HTTPS request to verify external service behavior; DNS is not changed by this operation."}
	case action == "certificate.issue":
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "Root broker certificate issuance job", Expected: "The certificate helper reports successful issuance; the resulting certificate lineage is then re-read and verified.", Automated: true})
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "certbot certificates", Expected: "The requested domain appears in the active Certbot lineage.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "During ACME validation or certificate installation", Expected: "The durable job remains non-successful and follows retry/dead-letter handling; do not infer installation from request acceptance.", Recovery: "Inspect the durable job result and Certbot lineage, then verify Apache configuration before requesting another certificate."})
		proof = []string{"The worker must verify that the certificate lineage exists after the helper reports success.", "A successful issuance does not prove public DNS propagation or an external TLS handshake."}
	case strings.HasPrefix(action, "site.resources:"):
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "Root broker resource operation: apply cgroup, PHP-FPM worker, and requested quota settings", Expected: "All requested resource operations succeed before the stored profile is marked applied.", Automated: true})
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "stepanelctl site inspect " + site, Expected: "The returned profile and quota state match the proposed limits.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "After desired profile write, during host resource application", Expected: "The operation reports pending reconciliation and retains the intended profile; it must not claim enforcement.", Recovery: "Inspect broker/resource health and actual cgroup/quota state, then retry reconciliation from the persisted desired profile."})
		proof = []string{"The endpoint returns applied state only after the resource helper succeeds and the applied profile is durably saved.", "Confirm live enforcement with the site inspection response; configured limits alone do not prove workload isolation."}
	case strings.HasPrefix(action, "wordpress.staging-promote:"):
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "WP-CLI database check and active database identity verification", Expected: "The candidate database passes WP-CLI validation and the production configuration points to that database before commit.", Automated: true})
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "stepanelctl site inspect " + site, Expected: "The production site reports the promoted database and a successful application health check.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "During database clone, filesystem activation, or URL rewrite", Expected: "The durable site transaction restores the previous file generation and removes the candidate database; failed compensation is marked recovery-required.", Recovery: "Use the operation journal, pre-promotion backup, and retained previous database. Verify the public site before deleting recovery copies."})
		proof = []string{"Promotion commits only after WP-CLI database validation, active database identity verification, and promotion journal persistence.", "The result identifies the safety backup and retained previous database; verify the public HTTPS application separately."}
	case strings.HasPrefix(action, "webserver.htaccess.import:"):
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "Caddy configuration validation and reload through the managed web-server helper", Expected: "Caddy accepts the complete generated configuration; unsupported source directives are listed separately.", Automated: true})
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile", Expected: "The active Caddy configuration validates successfully.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "During generated configuration validation or reload", Expected: "The previous configuration recovery snapshot remains available; unsupported directives are never silently presented as imported.", Recovery: "Restore the recorded configuration snapshot, validate the full Caddyfile, and reload Caddy."})
		proof = []string{"The result reports whether the translated configuration was applied and identifies the recovery snapshot when one was created.", "A Caddy syntax check does not establish that the site's application responds correctly."}
	case strings.HasPrefix(action, "git.rollback:"):
		validation = append(validation, ChangeValidationCommand{When: "during", Command: "StePanel release activation helper: validate target release identity and atomically switch the document-root generation", Expected: "The target generation is active and the replaced generation is retained as the next rollback point.", Automated: true})
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "stepanelctl site inspect " + site, Expected: "The active release identity matches the requested rollback target.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "During atomic release activation", Expected: "The active or previous generation remains available; a failed activation is not reported as complete.", Recovery: "Inspect the deployment record and active document-root identity, then use the retained release or recovery journal to converge."})
		proof = []string{"The response names the activated release and the replaced release retained for another rollback.", "Run the listed site inspection command and an HTTPS application check to verify the live site."}
	default:
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "stepanelctl doctor", Expected: "The deployment readiness report identifies any unavailable or degraded dependencies.", Automated: false})
		interruptions = append(interruptions, ChangeInterruptionPoint{Point: "During the external mutation", Expected: "The operation must remain pending/failed until host state is reconciled.", Recovery: "Inspect the operation record and subsystem journal before retrying."})
	}
	if path != "" {
		validation = append(validation, ChangeValidationCommand{When: "after", Command: "Inspect managed configuration at " + path, Expected: "The active file matches the proposed managed configuration.", Automated: false})
	}
	return validation, interruptions, proof
}

func (a *App) verifyChangePreview(token, action string, before, proposed any) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[1]) != sha256.Size*2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expires < time.Now().UTC().Unix() {
		return false
	}
	provided, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	message, err := json.Marshal(struct {
		Action   string `json:"action"`
		Before   any    `json:"before"`
		Proposed any    `json:"proposed"`
		Expires  int64  `json:"expires"`
	}{action, before, proposed, expires})
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.Auth.Secret))
	_, _ = mac.Write(message)
	return hmac.Equal(provided, mac.Sum(nil))
}

func changePreviewRequired(want string) error {
	if strings.TrimSpace(want) == "" {
		return errors.New("a current change preview is required")
	}
	return nil
}
