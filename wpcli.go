package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// runWordPress runs one named wp-cli operation against a managed site.
// wp-cli loads wp-config.php and WordPress core from the site tree, so in
// production (and on lab hosts using the broker) it runs through the root
// broker as the site's isolated user: tenant PHP must never execute with the
// panel's privileges. Development hosts run the configured wp-cli directly
// with the same arguments.
func runWordPress(ctx context.Context, cfg Config, request rootbroker.WordPressRequest) (rootbroker.WordPressResponse, error) {
	args, err := rootbroker.WordPressArgs(&request)
	if err != nil {
		return rootbroker.WordPressResponse{}, err
	}
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return rootbroker.WordPressResponse{}, err
		}
		response, err := client.Execute(ctx, &rootbroker.Request{RequestType: "wordpress", WordPress: &request})
		if err != nil {
			return rootbroker.WordPressResponse{}, err
		}
		if !response.OK {
			return rootbroker.WordPressResponse{}, errors.New(response.Error)
		}
		var result rootbroker.WordPressResponse
		if len(response.Details) > 0 {
			if err := json.Unmarshal(response.Details, &result); err != nil {
				return rootbroker.WordPressResponse{}, fmt.Errorf("decode WordPress response: %w", err)
			}
		}
		return result, nil
	}
	root, err := safePath(cfg.WebRoot, "sites", request.Site, "public")
	if err != nil {
		return rootbroker.WordPressResponse{}, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, rootbroker.RequestTimeout(&rootbroker.Request{RequestType: "wordpress", WordPress: &request}))
	defer cancel()
	cmd := exec.CommandContext(commandCtx, cfg.WPCLI, append([]string{"--path=" + root, "--no-color"}, args...)...)
	output, runErr := runBoundedCommandInput(commandCtx, cmd, strings.NewReader(request.Secret+"\n"))
	result := rootbroker.WordPressResponse{Output: string(output)}
	if request.Action == "maintenance-status" {
		result.Active, runErr = rootbroker.WordPressMaintenanceActive(runErr)
	}
	if runErr != nil {
		return rootbroker.WordPressResponse{}, fmt.Errorf("WordPress %s failed: %w", request.Action, runErr)
	}
	return result, nil
}
