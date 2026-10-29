// Package stepanel is the supported Go client for the StePanel HTTP API.
// It intentionally exposes the same paths and response semantics used by the
// dashboard: asynchronous mutations return their durable job response.
package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Client calls a StePanel instance using a scoped bearer token.
type Client struct {
	BaseURL       string
	Token         string
	HTTP          *http.Client
	UploadTimeout time.Duration
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: 2 * time.Minute}, UploadTimeout: 55 * time.Minute}
}

// Response retains the HTTP status and request ID needed for operational
// troubleshooting. Callers must close Body.
type Response struct {
	Status    int
	RequestID string
	Body      io.ReadCloser
}

// OperationPath returns the method and path generated from the OpenAPI
// operationId. Dynamic path suffixes remain the caller's responsibility.
func OperationPath(operationID string) (string, string, bool) {
	for _, operation := range contractOperations {
		if operation.ID == operationID {
			return operation.Method, operation.Path, true
		}
	}
	return "", "", false
}

// DoOperation executes a non-dynamic operation declared in the API contract.
func (c *Client) DoOperation(ctx context.Context, operationID string, body io.Reader, contentType string, headers map[string]string) (*Response, error) {
	method, apiPath, ok := OperationPath(operationID)
	if !ok {
		return nil, fmt.Errorf("OpenAPI operation %q is not generated", operationID)
	}
	return c.Do(ctx, method, apiPath, body, contentType, headers)
}

func (c *Client) JSONOperation(ctx context.Context, operationID string, value any, headers map[string]string) (*Response, error) {
	method, apiPath, ok := OperationPath(operationID)
	if !ok {
		return nil, fmt.Errorf("OpenAPI operation %q is not generated", operationID)
	}
	return c.JSON(ctx, method, apiPath, value, headers)
}

// Do sends a request to a documented API path. JSON values are encoded when
// body is non-nil; callers may supply multipart bodies and content types too.
func (c *Client) Do(ctx context.Context, method, apiPath string, body io.Reader, contentType string, headers map[string]string) (*Response, error) {
	return c.do(ctx, method, apiPath, body, contentType, headers, -1)
}

func (c *Client) do(ctx context.Context, method, apiPath string, body io.Reader, contentType string, headers map[string]string, contentLength int64) (*Response, error) {
	if c == nil || c.BaseURL == "" {
		return nil, fmt.Errorf("StePanel base URL is empty")
	}
	if !strings.HasPrefix(apiPath, "/") || strings.HasPrefix(apiPath, "//") {
		return nil, fmt.Errorf("API path must be absolute and same-origin")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid StePanel base URL")
	}
	if c.Token != "" && u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("refusing to send a bearer token over non-TLS HTTP to a non-loopback host")
	}
	relative, err := url.Parse(apiPath)
	if err != nil || relative.IsAbs() || relative.Host != "" || !strings.HasPrefix(relative.Path, "/") {
		return nil, fmt.Errorf("invalid same-origin API path")
	}
	endpoint := *u
	basePath := strings.TrimSuffix(u.Path, "/")
	joinedPath := path.Clean(basePath + "/" + strings.TrimPrefix(relative.Path, "/"))
	if strings.HasSuffix(relative.Path, "/") && !strings.HasSuffix(joinedPath, "/") {
		joinedPath += "/"
	}
	endpoint.Path = joinedPath
	endpoint.RawQuery = relative.RawQuery
	endpoint.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	upload := strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") && c.UploadTimeout > 0
	if upload || client.CheckRedirect == nil {
		copy := *client
		if upload {
			copy.Timeout = c.UploadTimeout
		}
		if copy.CheckRedirect == nil {
			copy.CheckRedirect = refuseInsecureRedirect
		}
		client = &copy
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, RequestID: resp.Header.Get("X-Request-ID"), Body: resp.Body}, nil
}

// refuseInsecureRedirect keeps the bearer token on TLS: Go forwards the
// Authorization header on same-host redirects, including https to http.
func refuseInsecureRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" && !isLoopbackHost(req.URL.Hostname()) {
		return fmt.Errorf("refusing redirect to non-TLS URL %s", req.URL.Redacted())
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (c *Client) JSON(ctx context.Context, method, apiPath string, value any, headers map[string]string) (*Response, error) {
	var body io.Reader
	contentType := ""
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
		contentType = "application/json"
	}
	return c.Do(ctx, method, apiPath, body, contentType, headers)
}

// Multipart creates a bounded in-memory multipart request for small metadata
// forms. Large archive inspection should use UploadFile, which streams bytes.
func (c *Client) Multipart(ctx context.Context, method, apiPath string, fields map[string]string, headers map[string]string) (*Response, error) {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	headers = copyHeaders(headers)
	headers["Content-Type"] = writer.FormDataContentType()
	return c.Do(ctx, method, apiPath, &data, writer.FormDataContentType(), headers)
}

func (c *Client) MultipartOperation(ctx context.Context, operationID string, fields map[string]string, headers map[string]string) (*Response, error) {
	method, apiPath, ok := OperationPath(operationID)
	if !ok {
		return nil, fmt.Errorf("OpenAPI operation %q is not generated", operationID)
	}
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return nil, fmt.Errorf("OpenAPI operation %q does not accept a multipart mutation", operationID)
	}
	return c.Multipart(ctx, method, apiPath, fields, headers)
}

func copyHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// UploadFile streams one file part without buffering the archive in memory.
// The size is required because StePanel uses Content-Length for disk admission.
func (c *Client) UploadFile(ctx context.Context, apiPath, field, filename string, src io.Reader, size int64, headers map[string]string) (*Response, error) {
	if size < 0 {
		return nil, fmt.Errorf("upload size must be known")
	}
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		return nil, err
	}
	_ = part
	boundary := writer.Boundary()
	contentType := writer.FormDataContentType()
	suffix := []byte("\r\n--" + boundary + "--\r\n")
	body := io.MultiReader(bytes.NewReader(prefix.Bytes()), src, bytes.NewReader(suffix))
	headers = copyHeaders(headers)
	headers["Content-Type"] = contentType
	return c.do(ctx, http.MethodPost, apiPath, body, contentType, headers, int64(prefix.Len()+len(suffix))+size)
}

func (c *Client) UploadOperation(ctx context.Context, operationID, field, filename string, src io.Reader, size int64, headers map[string]string) (*Response, error) {
	method, apiPath, ok := OperationPath(operationID)
	if !ok {
		return nil, fmt.Errorf("OpenAPI operation %q is not generated", operationID)
	}
	if method != http.MethodPost {
		return nil, fmt.Errorf("OpenAPI operation %q is not an upload POST", operationID)
	}
	return c.UploadFile(ctx, apiPath, field, filename, src, size, headers)
}
