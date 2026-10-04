package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cyberducttape/StePanel/internal/safehttp"
)

// taskWebhookTimeout bounds the whole delivery, matching the previous curl
// --max-time budget so a slow receiver cannot stall the task unit's stop.
const taskWebhookTimeout = 5 * time.Second

// taskWebhookPayload is the completion notification body. Field names are
// part of the public webhook contract.
type taskWebhookPayload struct {
	Site       string `json:"site"`
	Task       string `json:"task"`
	Result     string `json:"result"`
	ExitCode   string `json:"exit_code"`
	ExitStatus string `json:"exit_status"`
}

// runTaskWebhook delivers a scheduled task completion notification. It runs
// from the task unit's ExecStopPost as the site user with an empty
// environment, and enforces the shared outbound policy on the connected
// address, so a URL that was public when saved but now resolves to a private
// or metadata address is refused. Redirects are not followed.
//
// Usage: stepanel task-webhook URL SITE TASK RESULT EXIT_CODE EXIT_STATUS
func runTaskWebhook(ctx context.Context, policy safehttp.Policy, args []string) error {
	client := policy.Client(taskWebhookTimeout, 0, safehttp.TransportOptions{DialTimeout: taskWebhookTimeout})
	return sendTaskWebhook(ctx, client, policy, args)
}

// sendTaskWebhook posts the notification with client, which must enforce
// policy on every connection.
func sendTaskWebhook(ctx context.Context, client *http.Client, policy safehttp.Policy, args []string) error {
	if len(args) != 6 {
		return fmt.Errorf("usage: stepanel task-webhook URL SITE TASK RESULT EXIT_CODE EXIT_STATUS")
	}
	target := args[0]
	if !validTaskWebhookFor(policy, target) {
		return fmt.Errorf("task webhook URL is not allowed")
	}
	body, err := json.Marshal(taskWebhookPayload{Site: args[1], Task: args[2], Result: args[3], ExitCode: args[4], ExitStatus: args[5]})
	if err != nil {
		return fmt.Errorf("encode task webhook: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, taskWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build task webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "StePanel-Task-Webhook")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver task webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("task webhook receiver returned %s", resp.Status)
	}
	return nil
}
