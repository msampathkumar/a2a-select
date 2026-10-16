package ards_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/msampathkumar/a2a-select/internal/ards"
)

func TestClient_Search(t *testing.T) {
	t.Parallel()

	var gotQuery, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query().Get("q")
		gotType = r.URL.Query().Get("type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "query": "warehouse",
  "type_filter": "agent",
  "total_results": 2,
  "execution_time_ms": 0.42,
  "results": [
    {
      "id": "agent-osaka",
      "type": "agent",
      "name": "Osaka Fulfillment Agent",
      "description": "Autonomous warehouse controller for Osaka",
      "endpoint": "http://127.0.0.1:8009",
      "version": "1.0.0",
      "tags": ["warehouse", "apac"],
      "capabilities": ["inventory_query", "robotics_dispatch"],
      "location": "Osaka, Japan",
      "health_status": "healthy"
    },
    {
      "id": "mcp-kyoto",
      "type": "mcp",
      "name": "Kyoto Analytics MCP",
      "description": "SQL analytics driver",
      "endpoint": "http://127.0.0.1:8010/mcp",
      "version": "1.0.0",
      "capabilities": ["sql_query"],
      "location": "Kyoto, Japan",
      "health_status": "healthy"
    }
  ]
}`))
	}))
	t.Cleanup(srv.Close)

	client, err := ards.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Search(context.Background(), "warehouse", "agent")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if gotQuery != "warehouse" || gotType != "agent" {
		t.Fatalf("query params = (%q, %q), want (\"warehouse\", \"agent\")", gotQuery, gotType)
	}
	if resp.TotalResults != 2 || len(resp.Results) != 2 {
		t.Fatalf("resp = %+v, want 2 results", resp)
	}
	if resp.Results[0].Name != "Osaka Fulfillment Agent" || !resp.Results[0].IsAgent() {
		t.Fatalf("resp.Results[0] = %+v", resp.Results[0])
	}
	if resp.Results[1].IsAgent() {
		t.Fatalf("resp.Results[1].IsAgent() = true, want false for mcp")
	}
}

func TestNewClient_RejectsInvalidScheme(t *testing.T) {
	t.Parallel()

	if _, err := ards.NewClient("file:///etc/passwd"); err == nil {
		t.Fatal("NewClient(file://) error = nil, want error")
	}
}
