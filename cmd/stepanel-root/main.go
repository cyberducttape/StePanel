package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func main() {
	webRootFlag := flag.String("webroot", "/var/www", "Web root directory")
	flag.Parse()

	if *webRootFlag == "" {
		log.Fatal("webroot is required")
	}

	logger := log.New(os.Stderr, "[stepanel-root] ", log.LstdFlags)

	broker, err := rootbroker.NewBroker(*webRootFlag, logger)
	if err != nil {
		logger.Fatalf("failed to create broker: %v", err)
	}

	// Read requests from stdin, write responses to stdout
	// Each request/response is a single JSON line
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

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
