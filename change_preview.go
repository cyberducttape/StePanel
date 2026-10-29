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
	Action       string   `json:"action"`
	Summary      string   `json:"summary"`
	Before       any      `json:"before"`
	Proposed     any      `json:"proposed"`
	Effects      []string `json:"effects"`
	RecoveryPlan []string `json:"recovery_plan"`
	PreviewToken string   `json:"preview_token"`
	ExpiresAt    string   `json:"expires_at"`
}

const changePreviewTTL = 5 * time.Minute

func (a *App) makeChangePreview(action string, before, proposed any, effects, recovery []string) (ChangePreview, error) {
	preview := ChangePreview{Action: action, Summary: action, Before: before, Proposed: proposed, Effects: effects, RecoveryPlan: recovery}
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
