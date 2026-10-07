package k8s

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// Standard Client Errors
var (
	ErrUnauthorized      = errors.New("k8s: unauthorized (401)")
	ErrForbidden         = errors.New("k8s: forbidden (403) - insufficient RBAC permissions")
	ErrNotFound          = errors.New("k8s: resource not found (404)")
	ErrThrottled         = errors.New("k8s: api request throttled (429)")
	ErrServerUnavailable = errors.New("k8s: api server unavailable (5xx)")
	ErrWatchClosed       = errors.New("k8s: watch stream closed")
)

// Client is the read-only contract for interacting with the Kubernetes API.
// HARD ARCHITECTURAL GUARANTEE: This interface exposes zero mutation methods.
type Client interface {
	ListNamespaces(ctx context.Context) ([]Namespace, error)
	ListNodes(ctx context.Context) ([]Node, error)
	ListPods(ctx context.Context, namespace string) ([]Pod, error)
	ListDeployments(ctx context.Context, namespace string) ([]Deployment, error)
	ListReplicaSets(ctx context.Context, namespace string) ([]ReplicaSet, error)
	ListServices(ctx context.Context, namespace string) ([]Service, error)
	ListIngresses(ctx context.Context, namespace string) ([]Ingress, error)
	ListConfigMaps(ctx context.Context, namespace string) ([]ConfigMap, error)
	ListSecretsMetadata(ctx context.Context, namespace string) ([]SecretMetadata, error)
	ListPVCs(ctx context.Context, namespace string) ([]PersistentVolumeClaim, error)
	ListPVs(ctx context.Context) ([]PersistentVolume, error)
	Watch(ctx context.Context, resourcePath string, resourceVersion string) (<-chan WatchEvent, <-chan error, error)
	Ping(ctx context.Context) error
}

// ClientConfig holds configuration for constructing a Kubernetes REST Client.
type ClientConfig struct {
	BaseURL       string
	BearerToken   string
	TokenFilePath string
	CACertPath    string
	InsecureTLS   bool
	Timeout       time.Duration
	Logger        logging.Logger
	HTTPClient    *http.Client
}

// RESTClient implements Client using standard net/http for Kubernetes REST API interaction.
type RESTClient struct {
	baseURL     *url.URL
	bearerToken string
	httpClient  *http.Client
	logger      logging.Logger
	mu          sync.RWMutex
}

// NewRESTClient constructs a new read-only Kubernetes REST Client.
func NewRESTClient(cfg ClientConfig) (*RESTClient, error) {
	if cfg.BaseURL == "" {
		// Standard in-cluster fallback
		host := os.Getenv("KUBERNETES_SERVICE_HOST")
		port := os.Getenv("KUBERNETES_SERVICE_PORT")
		if host != "" && port != "" {
			cfg.BaseURL = fmt.Sprintf("https://%s:%s", host, port)
		} else {
			cfg.BaseURL = "http://localhost:8001"
		}
	}

	parsedURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid kubernetes api base url: %w", err)
	}

	token := cfg.BearerToken
	if token == "" && cfg.TokenFilePath != "" {
		tokenBytes, err := os.ReadFile(cfg.TokenFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read kubernetes token file: %w", err)
		}
		token = strings.TrimSpace(string(tokenBytes))
	} else if token == "" {
		// Standard in-cluster token path check
		inClusterTokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
		if tb, err := os.ReadFile(inClusterTokenPath); err == nil {
			token = strings.TrimSpace(string(tb))
		}
	}

	logger := cfg.Logger
	if logger == nil {
		logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}

		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.InsecureTLS,
		}

		if cfg.CACertPath != "" {
			caCert, err := os.ReadFile(cfg.CACertPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read ca cert file: %w", err)
			}
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = caCertPool
		} else {
			// Check standard in-cluster CA cert
			inClusterCAPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
			if caCert, err := os.ReadFile(inClusterCAPath); err == nil {
				caCertPool := x509.NewCertPool()
				caCertPool.AppendCertsFromPEM(caCert)
				tlsConfig.RootCAs = caCertPool
			}
		}

		httpClient = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig:    tlsConfig,
				MaxIdleConns:       50,
				IdleConnTimeout:    60 * time.Second,
				DisableCompression: false,
			},
		}
	}

	return &RESTClient{
		baseURL:     parsedURL,
		bearerToken: token,
		httpClient:  httpClient,
		logger:      logger,
	}, nil
}

// ---------------------------------------------------------------------------
// HTTP Request Helpers (Read-Only)
// ---------------------------------------------------------------------------

func (c *RESTClient) newRequest(ctx context.Context, path string) (*http.Request, error) {
	rel, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	u := c.baseURL.ResolveReference(rel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	return req, nil
}

func (c *RESTClient) execute(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kubernetes api call failed: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusUnauthorized:
		_ = resp.Body.Close()
		return nil, ErrUnauthorized
	case http.StatusForbidden:
		_ = resp.Body.Close()
		return nil, ErrForbidden
	case http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, ErrNotFound
	case http.StatusTooManyRequests:
		_ = resp.Body.Close()
		return nil, ErrThrottled
	default:
		if resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%w: status %d", ErrServerUnavailable, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

func getList[T any](ctx context.Context, c *RESTClient, path string) ([]T, error) {
	req, err := c.newRequest(ctx, path)
	if err != nil {
		return nil, err
	}

	resp, err := c.execute(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var list ResourceList[T]
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("failed to decode list response from %s: %w", path, err)
	}

	return list.Items, nil
}

// ---------------------------------------------------------------------------
// Client Interface Implementation
// ---------------------------------------------------------------------------

func (c *RESTClient) Ping(ctx context.Context) error {
	req, err := c.newRequest(ctx, "/readyz")
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kubernetes api ping returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *RESTClient) ListNamespaces(ctx context.Context) ([]Namespace, error) {
	return getList[Namespace](ctx, c, "/api/v1/namespaces")
}

func (c *RESTClient) ListNodes(ctx context.Context) ([]Node, error) {
	return getList[Node](ctx, c, "/api/v1/nodes")
}

func (c *RESTClient) ListPods(ctx context.Context, namespace string) ([]Pod, error) {
	path := "/api/v1/pods"
	if namespace != "" {
		path = fmt.Sprintf("/api/v1/namespaces/%s/pods", url.PathEscape(namespace))
	}
	return getList[Pod](ctx, c, path)
}

func (c *RESTClient) ListDeployments(ctx context.Context, namespace string) ([]Deployment, error) {
	path := "/apis/apps/v1/deployments"
	if namespace != "" {
		path = fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments", url.PathEscape(namespace))
	}
	return getList[Deployment](ctx, c, path)
}

func (c *RESTClient) ListReplicaSets(ctx context.Context, namespace string) ([]ReplicaSet, error) {
	path := "/apis/apps/v1/replicasets"
	if namespace != "" {
		path = fmt.Sprintf("/apis/apps/v1/namespaces/%s/replicasets", url.PathEscape(namespace))
	}
	return getList[ReplicaSet](ctx, c, path)
}

func (c *RESTClient) ListServices(ctx context.Context, namespace string) ([]Service, error) {
	path := "/api/v1/services"
	if namespace != "" {
		path = fmt.Sprintf("/api/v1/namespaces/%s/services", url.PathEscape(namespace))
	}
	return getList[Service](ctx, c, path)
}

func (c *RESTClient) ListIngresses(ctx context.Context, namespace string) ([]Ingress, error) {
	path := "/apis/networking.k8s.io/v1/ingresses"
	if namespace != "" {
		path = fmt.Sprintf("/apis/networking.k8s.io/v1/namespaces/%s/ingresses", url.PathEscape(namespace))
	}
	return getList[Ingress](ctx, c, path)
}

func (c *RESTClient) ListConfigMaps(ctx context.Context, namespace string) ([]ConfigMap, error) {
	path := "/api/v1/configmaps"
	if namespace != "" {
		path = fmt.Sprintf("/api/v1/namespaces/%s/configmaps", url.PathEscape(namespace))
	}
	return getList[ConfigMap](ctx, c, path)
}

// rawSecretItem is used strictly during unmarshaling to extract metadata and key names only.
type rawSecretItem struct {
	Metadata ObjectMeta                 `json:"metadata"`
	Type     string                     `json:"type,omitempty"`
	Data     map[string]json.RawMessage `json:"data,omitempty"`
}

// ListSecretsMetadata fetches secrets and strictly sanitizes the result, extracting ONLY
// metadata and secret key names. Values, stringData, and payload buffers are NEVER stored or returned.
func (c *RESTClient) ListSecretsMetadata(ctx context.Context, namespace string) ([]SecretMetadata, error) {
	path := "/api/v1/secrets"
	if namespace != "" {
		path = fmt.Sprintf("/api/v1/namespaces/%s/secrets", url.PathEscape(namespace))
	}

	req, err := c.newRequest(ctx, path)
	if err != nil {
		return nil, err
	}

	resp, err := c.execute(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var list struct {
		Metadata ListMeta        `json:"metadata"`
		Items    []rawSecretItem `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("failed to decode secrets list response: %w", err)
	}

	sanitized := make([]SecretMetadata, len(list.Items))
	for i, item := range list.Items {
		keys := make([]string, 0, len(item.Data))
		for key := range item.Data {
			keys = append(keys, key)
		}
		sanitized[i] = SecretMetadata{
			ObjectMeta: item.Metadata,
			Type:       item.Type,
			KeyNames:   keys,
		}
	}

	return sanitized, nil
}

func (c *RESTClient) ListPVCs(ctx context.Context, namespace string) ([]PersistentVolumeClaim, error) {
	path := "/api/v1/persistentvolumeclaims"
	if namespace != "" {
		path = fmt.Sprintf("/api/v1/namespaces/%s/persistentvolumeclaims", url.PathEscape(namespace))
	}
	return getList[PersistentVolumeClaim](ctx, c, path)
}

func (c *RESTClient) ListPVs(ctx context.Context) ([]PersistentVolume, error) {
	return getList[PersistentVolume](ctx, c, "/api/v1/persistentvolumes")
}

// Watch opens a streaming chunked HTTP connection to the Kubernetes API.
// It parses JSON watch events ("ADDED", "MODIFIED", "DELETED") line by line
// and sends them onto the returned events channel.
func (c *RESTClient) Watch(
	ctx context.Context,
	resourcePath string,
	resourceVersion string,
) (<-chan WatchEvent, <-chan error, error) {
	separator := "?"
	if strings.Contains(resourcePath, "?") {
		separator = "&"
	}
	path := fmt.Sprintf("%s%swatch=true", resourcePath, separator)
	if resourceVersion != "" {
		path = fmt.Sprintf("%s&resourceVersion=%s", path, url.QueryEscape(resourceVersion))
	}

	req, err := c.newRequest(ctx, path)
	if err != nil {
		return nil, nil, err
	}

	resp, err := c.execute(req)
	if err != nil {
		return nil, nil, err
	}

	events := make(chan WatchEvent, 64)
	errs := make(chan error, 1)

	go func() {
		defer resp.Body.Close()
		defer close(events)
		defer close(errs)

		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadBytes('\n')
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
					return
				}
				errs <- fmt.Errorf("watch stream read error: %w", err)
				return
			}

			line = []byte(strings.TrimSpace(string(line)))
			if len(line) == 0 {
				continue
			}

			var event WatchEvent
			if err := json.Unmarshal(line, &event); err != nil {
				c.logger.Warn("Failed to decode watch event line", "error", err.Error())
				continue
			}

			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return events, errs, nil
}
