package evals_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newARDSMockServer(t *testing.T, agentEndpoint string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_found": 1,
			"results": []map[string]any{
				{
					"id":            "agent-osaka-fulfillment",
					"type":          "agent",
					"name":          "Osaka Fulfillment Agent",
					"description":   "Stateful multi-turn warehouse fulfillment agent",
					"endpoint":      agentEndpoint,
					"version":       "1.0.0",
					"capabilities":  []string{"inventory", "fulfillment"},
					"location":      "Osaka, Japan",
					"health_status": "healthy",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// buildA2ASelectBinary compiles the production `a2a-select` binary from source into a temp directory
// so black-box tests execute the compiled executable strictly via stdin/stdout/stderr and CLI flags.
func buildA2ASelectBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "a2a-select")
	cmd := exec.Command("go", "build", "-trimpath", "-o", binPath, "..")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to compile a2a-select binary: %v\n%s", err, stderr.String())
	}
	return binPath
}

func TestBlackBox_CompiledBinary_PluginInfoAndVersion(t *testing.T) {
	t.Parallel()

	bin := buildA2ASelectBinary(t)

	// 1. `a2a-select info` (a2a Command Plugin contract)
	out, err := exec.Command(bin, "info").CombinedOutput()
	if err != nil {
		t.Fatalf("a2a-select info failed: %v\n%s", err, string(out))
	}
	var info map[string]string
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatalf("invalid JSON from `a2a-select info`: %v\nraw: %s", err, string(out))
	}
	if info["name"] != "select" || info["version"] != "0.1.0" {
		t.Fatalf("unexpected plugin info: %+v", info)
	}

	// 2. `a2a-select --version`
	verOut, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("a2a-select --version failed: %v", err)
	}
	if strings.TrimSpace(string(verOut)) != "a2a-select 0.1.0" {
		t.Fatalf("version output = %q, want %q", strings.TrimSpace(string(verOut)), "a2a-select 0.1.0")
	}
}

func TestBlackBox_CompiledBinary_EndToEnd_Lifecycle(t *testing.T) {
	t.Parallel()

	a2aPath, err := exec.LookPath("a2a")
	if err != nil {
		t.Skipf("a2a CLI not found on PATH: %v", err)
	}

	bin := buildA2ASelectBinary(t)
	agentSrv := newStatefulA2AServer(t)
	ardsSrv := newARDSMockServer(t, agentSrv.srv.URL)

	tmpDir := t.TempDir()
	cardsDir := filepath.Join(tmpDir, "agents")
	sessionsFile := filepath.Join(tmpDir, "sessions.json")

	// Phase 1: Black-box run of `a2a-select` to search ARDS, fetch card, and save locally.
	// Menu: [a] Search ARDS -> type "agent" -> query "osaka" -> select [1] -> /exit
	phase1Input := strings.Join([]string{
		"a",
		"agent",
		"osaka",
		"1",
		"/exit",
	}, "\n") + "\n"

	cmd1 := exec.Command(bin,
		"--cards-dir", cardsDir,
		"--sessions-file", sessionsFile,
		"--ards-url", ardsSrv.URL,
		"--a2a-bin", a2aPath,
	)
	cmd1.Stdin = strings.NewReader(phase1Input)
	out1, err := cmd1.CombinedOutput()
	if err != nil {
		t.Fatalf("Phase 1 black-box execution failed: %v\nOutput:\n%s", err, string(out1))
	}
	if !strings.Contains(string(out1), "Saved Agent Card") {
		t.Fatalf("expected Saved Agent Card in output:\n%s", string(out1))
	}

	savedCardPath := filepath.Join(cardsDir, "osaka-fulfillment-agent.json")
	if _, err := os.Stat(savedCardPath); err != nil {
		t.Fatalf("expected saved card file at %s: %v", savedCardPath, err)
	}

	// Upgrade remote agent version to verify black-box auto-update on selection.
	agentSrv.SetVersion("2.0.0")

	// Phase 2: Black-box run selecting local card #1, executing a 3-turn conversation
	// (input-required -> completed -> new task in same context), and exiting cleanly.
	phase2Input := strings.Join([]string{
		"1",
		"Check inventory for SKU-900",
		"Confirm warehouse B",
		"Also schedule express pickup",
		"/exit",
		"n",
	}, "\n") + "\n"

	cmd2 := exec.Command(bin,
		"--cards-dir", cardsDir,
		"--sessions-file", sessionsFile,
		"--a2a-bin", a2aPath,
	)
	cmd2.Stdin = strings.NewReader(phase2Input)
	out2, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("Phase 2 black-box execution failed: %v\nOutput:\n%s", err, string(out2))
	}

	outStr := string(out2)
	for _, want := range []string{
		"remote card changed (v1.0.0 -> v2.0.0); updated local card osaka-fulfillment-agent.json",
		"Task ID:     task-eval-101",
		"Context ID:  ctx-eval-001",
		"Task Status: input-required",
		"Task Status: completed",
		"Starting new task in existing context ctx-eval-001",
		"Task ID:     task-eval-102",
		"=== Continue This Conversation Later ===",
		"--context-id ctx-eval-001",
	} {
		if !strings.Contains(outStr, want) {
			t.Fatalf("Phase 2 black-box output missing %q:\n%s", want, outStr)
		}
	}
}
