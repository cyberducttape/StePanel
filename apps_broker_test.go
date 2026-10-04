package stepanel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// serveOneAppBrokerRequest accepts one typed broker request on a lab socket,
// checks it and replies with response.
func serveOneAppBrokerRequest(t *testing.T, check func(*rootbroker.AppRequest) error, response rootbroker.Response) <-chan error {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "root-broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	t.Setenv("STEPANEL_LAB_DIRECT_ROOT_BROKER", "1")
	t.Setenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE", "1")
	t.Setenv("STEPANEL_LAB_ROOT_BROKER_SOCKET", socketPath)
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		var request rootbroker.Request
		if decodeErr := json.NewDecoder(conn).Decode(&request); decodeErr != nil {
			serverErr <- decodeErr
			return
		}
		if request.RequestType != "app" || request.App == nil {
			serverErr <- fmt.Errorf("request type = %q, want typed app request", request.RequestType)
			return
		}
		if checkErr := check(request.App); checkErr != nil {
			serverErr <- checkErr
			return
		}
		serverErr <- json.NewEncoder(conn).Encode(response)
	}()
	return serverErr
}

func TestProductionAppApplyUsesTypedBrokerRequest(t *testing.T) {
	app := AppManifest{Site: "demo", Version: "v18.0.0", Port: 3000, Root: "/var/www/sites/demo/public"}
	serverErr := serveOneAppBrokerRequest(t, func(req *rootbroker.AppRequest) error {
		if req.Action != "apply" || req.Site != "demo" || req.Version != "18.0.0" || req.Port != 3000 || req.Root != app.Root {
			return fmt.Errorf("app apply request = %+v", req)
		}
		return nil
	}, rootbroker.Response{OK: true})
	if err := applyAppProcess(context.Background(), Config{Production: true, WebRoot: "/var/www"}, app); err != nil {
		t.Fatalf("typed production app apply failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestProductionAppLifecycleUsesTypedBrokerRequest(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "delete"} {
		t.Run(action, func(t *testing.T) {
			serverErr := serveOneAppBrokerRequest(t, func(req *rootbroker.AppRequest) error {
				if req.Action != action || req.Site != "demo" {
					return fmt.Errorf("app %s request = %+v", action, req)
				}
				return nil
			}, rootbroker.Response{OK: true})
			if err := runAppLifecycle(context.Background(), Config{Production: true, WebRoot: "/var/www"}, action, "demo"); err != nil {
				t.Fatalf("typed production app %s failed: %v", action, err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductionAppLifecycleReportsBrokerFailure(t *testing.T) {
	serverErr := serveOneAppBrokerRequest(t, func(*rootbroker.AppRequest) error { return nil },
		rootbroker.Response{OK: false, Error: "application start failed: application is not configured"})
	err := runAppLifecycle(context.Background(), Config{Production: true, WebRoot: "/var/www"}, "start", "demo")
	if err == nil || !strings.Contains(err.Error(), "application is not configured") {
		t.Fatalf("broker failure error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}
