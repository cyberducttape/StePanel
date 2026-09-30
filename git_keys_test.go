package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func TestProductionGitKeyDeleteUsesTypedBrokerRequest(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "root-broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
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
		if request.RequestType != "git" || request.Git == nil || request.Git.Action != "delete" || request.Git.Site != "demo" {
			serverErr <- errors.New("Git key deletion did not use its typed broker request")
			return
		}
		serverErr <- json.NewEncoder(conn).Encode(rootbroker.Response{OK: true})
	}()

	if err := deleteGitDeployKey(context.Background(), Config{Production: true, WebRoot: "/var/www"}, "demo"); err != nil {
		t.Fatalf("typed production Git key deletion failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestProductionGitPublicUsesTypedBrokerRequest(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "root-broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
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
		if request.RequestType != "git" || request.Git == nil || request.Git.Action != "public" || request.Git.Site != "demo" {
			serverErr <- errors.New("Git public-key lookup did not use its typed broker request")
			return
		}
		response := rootbroker.Response{OK: true}
		response.Details, _ = json.Marshal(rootbroker.GitResponse{PublicKey: "ssh-ed25519 AAAA demo"})
		serverErr <- json.NewEncoder(conn).Encode(response)
	}()

	publicKey, err := gitDeployPublicKey(context.Background(), Config{Production: true, WebRoot: "/var/www"}, "demo")
	if err != nil || publicKey != "ssh-ed25519 AAAA demo" {
		t.Fatalf("typed production Git public key = %q, error = %v", publicKey, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestProductionGitKeyGenerateUsesTypedBrokerRequest(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "root-broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
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
		if request.RequestType != "git" || request.Git == nil || request.Git.Action != "generate" || request.Git.Site != "demo" {
			serverErr <- errors.New("Git key generation did not use its typed broker request")
			return
		}
		response := rootbroker.Response{OK: true}
		response.Details, _ = json.Marshal(rootbroker.GitResponse{PublicKey: "ssh-ed25519 AAAA demo"})
		serverErr <- json.NewEncoder(conn).Encode(response)
	}()

	publicKey, err := gitDeployKeyGenerate(context.Background(), Config{Production: true, WebRoot: "/var/www"}, "demo")
	if err != nil || publicKey != "ssh-ed25519 AAAA demo" {
		t.Fatalf("typed production Git key generation = %q, error = %v", publicKey, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestParseGitRepository(t *testing.T) {
	for _, test := range []struct {
		raw string
		ok  bool
		ssh bool
	}{
		{"https://github.com/acme/site.git", true, false},
		{"git@github.com:acme/site.git", true, true},
		{"https://user:token@github.com/acme/site.git", false, false},
		{"ssh://git@github.com/acme/site.git", false, false},
		{"git@evil.example:acme/site.git", false, false},
	} {
		got, err := parseGitRepository(test.raw, "github.com")
		if (err == nil) != test.ok || err == nil && got.Private != test.ssh {
			t.Fatalf("parseGitRepository(%q) = %#v, %v", test.raw, got, err)
		}
	}
}
