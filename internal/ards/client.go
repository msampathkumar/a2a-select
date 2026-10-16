package ards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20 // 4 MiB

// Resource represents an entry registered in an ARDS registry (agent, skill, mcp, plugin).
type Resource struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Endpoint     string   `json:"endpoint"`
	Version      string   `json:"version"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
	Provider     string   `json:"provider"`
	Location     string   `json:"location"`
	SchemaURL    string   `json:"schema_url"`
	HealthStatus string   `json:"health_status"`
}

// IsAgent reports whether the ARDS resource represents an A2A Agent.
func (r Resource) IsAgent() bool {
	return strings.EqualFold(strings.TrimSpace(r.Type), "agent")
}

// SearchResponse models the `/api/v1/search` JSON response from ARDS.
type SearchResponse struct {
	Query           string     `json:"query"`
	TypeFilter      string     `json:"type_filter"`
	TotalResults    int        `json:"total_results"`
	ExecutionTimeMs float64    `json:"execution_time_ms"`
	Results         []Resource `json:"results"`
	Backend         string     `json:"backend"`
}

// Client queries an ARDS discovery server over HTTP(S).
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient validates the ARDS base URL and returns a ready Client.
func NewClient(baseURL string) (*Client, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return nil, errors.New("ARDS base URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid ARDS URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("ARDS URL %q must use http or https with a valid host", raw)
	}
	return &Client{
		BaseURL: strings.TrimRight(parsed.String(), "/"),
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// Search queries `/api/v1/search` with an optional query string and resource type filter.
func (c *Client) Search(ctx context.Context, query, resourceType string) (*SearchResponse, error) {
	endpoint, err := url.Parse(c.BaseURL + "/api/v1/search")
	if err != nil {
		return nil, err
	}
	q := endpoint.Query()
	q.Set("q", strings.TrimSpace(query))
	rt := strings.ToLower(strings.TrimSpace(resourceType))
	if rt != "" && rt != "all" {
		q.Set("type", rt)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating ARDS search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying ARDS registry at %s: %w", c.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading ARDS response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ARDS registry returned HTTP %d", resp.StatusCode)
	}

	var parsed SearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decoding ARDS search response: %w", err)
	}
	return &parsed, nil
}
