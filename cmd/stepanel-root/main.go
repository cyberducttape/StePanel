package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func main() {
	webRootFlag := flag.String("webroot", "/var/www", "Web root directory")
	socketFlag := flag.String("socket", "", "serve the broker on a Unix socket instead of stdin/stdout")
	socketGroupFlag := flag.String("socket-group", "", "group allowed to access the Unix socket")
	flag.Parse()

	if *webRootFlag == "" {
		log.Fatal("webroot is required")
	}

	logger := log.New(os.Stderr, "[stepanel-root] ", log.LstdFlags)

	broker, err := rootbroker.NewBroker(*webRootFlag, logger)
	if err != nil {
		logger.Fatalf("failed to create broker: %v", err)
	}

	if *socketFlag != "" {
		if err := serveSocket(broker, *socketFlag, *socketGroupFlag, logger); err != nil {
			logger.Fatal(err)
		}
		return
	}

	serveRequests(broker, os.Stdin, os.Stdout, logger)
}

func serveRequests(broker *rootbroker.Broker, reader io.Reader, writer io.Writer, logger *log.Logger) {
	// Read requests from stdin, write responses to stdout. Each request and
	// response is a single JSON line.
	decoder := json.NewDecoder(reader)
	encoder := json.NewEncoder(writer)

	for {
		var req rootbroker.Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				break
			}
			logger.Printf("decode error: %v", err)
			resp := rootbroker.Response{
				OK:    false,
				Error: fmt.Sprintf("decode error: %v", err),
			}
			_ = encoder.Encode(resp)
			continue
		}

		// Set a reasonable timeout for each operation
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := broker.Execute(ctx, &req)
		cancel()

		if err != nil {
			logger.Printf("execute error: %v", err)
			resp = &rootbroker.Response{
				OK:    false,
				Error: fmt.Sprintf("execute error: %v", err),
			}
		}

		if err := encoder.Encode(resp); err != nil {
			logger.Printf("encode error: %v", err)
		}
	}
}

func serveSocket(broker *rootbroker.Broker, socketPath, socketGroup string, logger *log.Logger) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing broker socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on broker socket: %w", err)
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return fmt.Errorf("set broker socket mode: %w", err)
	}
	if socketGroup != "" {
		group, err := user.LookupGroup(socketGroup)
		if err != nil {
			return fmt.Errorf("lookup broker socket group %q: %w", socketGroup, err)
		}
		gid, err := strconv.Atoi(group.Gid)
		if err != nil {
			return fmt.Errorf("parse broker socket group %q: %w", socketGroup, err)
		}
		if err := os.Chown(socketPath, os.Getuid(), gid); err != nil {
			return fmt.Errorf("set broker socket group: %w", err)
		}
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept broker socket connection: %w", err)
		}
		serveRequests(broker, conn, conn, logger)
		_ = conn.Close()
	}
}
