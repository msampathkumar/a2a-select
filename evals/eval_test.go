package evals_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
	"github.com/msampathkumar/a2a-select/internal/ards"
	"github.com/msampathkumar/a2a-select/internal/cards"
	"github.com/msampathkumar/a2a-select/internal/session"
	"github.com/msampathkumar/a2a-select/internal/ui"
)

// recordedTurn captures the raw A2A SendMessageRequest fields received by our stateful A2A test server.
type recordedTurn struct {
	ContextID string
	TaskID    string
	Text      string
}

// statefulA2AServer implements a real A2A v1.0 HTTP+JSON server (`/.well-known/agent-card.json`,
// `POST /message:send`, and `GET /tasks`) so the real `a2a` CLI binary executes full protocol roundtrips.
type statefulA2AServer struct {
	mu      sync.Mutex
	srv     *httptest.Server
	version string
	turns   []recordedTurn
	tasks   map[string]string // taskID -> state
}

func newStatefulA2AServer(t *testing.T) *statefulA2AServer {
	t.Helper()
	s := &statefulA2AServer{
		version: "1.0.0",
		tasks:   make(map[string]string),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("/message:send", s.handleSendMessage)
	mux.HandleFunc("/tasks", s.handleListTasks)

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *statefulA2AServer) SetVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
}

func (s *statefulA2AServer) RecordedTurns() []recordedTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedTurn, len(s.turns))
	copy(out, s.turns)
	return out
}

func (s *statefulA2AServer) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ver := s.version
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	card := map[string]any{
		"name":        "Osaka Fulfillment Agent",
		"description": "Stateful multi-turn warehouse fulfillment agent",
		"version":     ver,
		"supportedInterfaces": []map[string]any{
			{
				"protocolBinding": "HTTP+JSON",
				"protocolVersion": "1.0",
				"url":             s.srv.URL,
			},
		},
		"capabilities": map[string]any{
			"streaming": false,
		},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills": []map[string]any{
			{
				"id":          "shipping_options",
				"name":        "Shipping Options & Booking",
				"description": "Lists shipping options and books selected option",
			},
		},
	}
	_ = json.NewEncoder(w).Encode(card)
}

func (s *statefulA2AServer) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message struct {
			MessageID string `json:"messageId"`
			TaskID    string `json:"taskId"`
			ContextID string `json:"contextId"`
			Role      string `json:"role"`
			Parts     []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var text string
	if len(req.Message.Parts) > 0 {
		text = req.Message.Parts[0].Text
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reject attempts to continue an already completed task (enforcing the A2A protocol rule).
	if req.Message.TaskID != "" {
		if prevState, ok := s.tasks[req.Message.TaskID]; ok && prevState == "TASK_STATE_COMPLETED" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    400,
					"message": fmt.Sprintf("task %s is already completed and cannot be restarted", req.Message.TaskID),
				},
			})
			return
		}
	}

	s.turns = append(s.turns, recordedTurn{
		ContextID: req.Message.ContextID,
		TaskID:    req.Message.TaskID,
		Text:      text,
	})
	turnNum := len(s.turns)

	ctxID := req.Message.ContextID
	if ctxID == "" {
		ctxID = "ctx-eval-001"
	}

	var taskID, state, replyText string
	switch turnNum {
	case 1:
		// Turn 1: Return 5 options in INPUT_REQUIRED state
		taskID = "task-eval-101"
		state = "TASK_STATE_INPUT_REQUIRED"
		replyText = "Here are 5 options: [1] Air [2] Sea [3] Rail [4] Express Drone [5] Courier. Which one do you want?"
	case 2:
		// Turn 2: Continue task-eval-101 and mark COMPLETED
		taskID = req.Message.TaskID
		if taskID == "" {
			taskID = "task-eval-unexpected-empty"
		}
		state = "TASK_STATE_COMPLETED"
		replyText = "Option 4 (Express Drone) booked and dispatched!"
	default:
		// Turn 3+: New task under the same contextId
		taskID = fmt.Sprintf("task-eval-%d", 100+turnNum-1)
		state = "TASK_STATE_COMPLETED"
		replyText = fmt.Sprintf("Follow-up turn #%d processed in context %s: %s", turnNum, ctxID, text)
	}

	s.tasks[taskID] = state

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"task": map[string]any{
			"id":        taskID,
			"contextId": ctxID,
			"status": map[string]any{
				"state": state,
				"message": map[string]any{
					"messageId": fmt.Sprintf("msg-agent-%d", turnNum),
					"taskId":    taskID,
					"contextId": ctxID,
					"role":      "ROLE_AGENT",
					"parts": []map[string]any{
						{"text": replyText},
					},
				},
			},
			"artifacts": []map[string]any{
				{
					"artifactId": fmt.Sprintf("art-%d", turnNum),
					"name":       "response",
					"parts": []map[string]any{
						{"text": replyText},
					},
				},
			},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *statefulA2AServer) handleListTasks(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctxFilter := r.URL.Query().Get("contextId")
	var list []map[string]any
	for id, st := range s.tasks {
		list = append(list, map[string]any{
			"id":        id,
			"contextId": ctxFilter,
			"status": map[string]any{
				"state": st,
			},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tasks": list,
	})
}

func TestEvalsSuite_EndToEndWithRealA2ACLI(t *testing.T) {
	if _, err := exec.LookPath("a2a"); err != nil {
		t.Skip("skipping live a2a CLI evals: a2a binary not on PATH")
	}

	a2aSrv := newStatefulA2AServer(t)

	// Start a live ARDS registry server offering skills, MCP tools, and our stateful A2A agent.
	ardsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		typeFilter := r.URL.Query().Get("type")
		allResults := []map[string]any{
			{
				"id":            "mcp-kyoto-sql",
				"type":          "mcp",
				"name":          "Kyoto SQL MCP Tool",
				"description":   "Read-only warehouse SQL MCP server",
				"endpoint":      "http://127.0.0.1:18010/mcp",
				"version":       "1.0.0",
				"capabilities":  []string{"sql_query"},
				"location":      "Kyoto, Japan",
				"health_status": "healthy",
			},
			{
				"id":            "agent-osaka-fulfillment",
				"type":          "agent",
				"name":          "Osaka Fulfillment Agent",
				"description":   "Stateful multi-turn warehouse fulfillment agent",
				"endpoint":      a2aSrv.srv.URL,
				"version":       "1.0.0",
				"capabilities":  []string{"shipping_options", "robotics_dispatch"},
				"tags":          []string{"warehouse", "apac"},
				"location":      "Osaka, Japan",
				"health_status": "healthy",
			},
		}
		var filtered []map[string]any
		for _, item := range allResults {
			if typeFilter == "" || typeFilter == "all" || item["type"] == typeFilter {
				filtered = append(filtered, item)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query":             r.URL.Query().Get("q"),
			"type_filter":       typeFilter,
			"total_results":     len(filtered),
			"execution_time_ms": 0.35,
			"results":           filtered,
		})
	}))
	t.Cleanup(ardsSrv.Close)

	tmpDir := t.TempDir()
	cardsDir := filepath.Join(tmpDir, "agents")
	sessionsFile := filepath.Join(tmpDir, "sessions.json")

	runner, err := a2acli.NewCLIRunner("a2a")
	if err != nil {
		t.Fatalf("NewCLIRunner(a2a) error = %v", err)
	}
	cardStore, err := cards.NewStore(cardsDir)
	if err != nil {
		t.Fatalf("cards.NewStore() error = %v", err)
	}
	sessStore, err := session.NewStore(sessionsFile)
	if err != nil {
		t.Fatalf("session.NewStore() error = %v", err)
	}
	ardsClient, err := ards.NewClient(ardsSrv.URL)
	if err != nil {
		t.Fatalf("ards.NewClient() error = %v", err)
	}

	// =========================================================================
	// EVAL 1: ARDS Registry Search (non-agent inspection + A2A Agent Card fetch)
	// =========================================================================
	t.Run("Eval1_ARDS_Discovery_And_Local_Card_Save", func(t *testing.T) {
		// Step A: Search ARDS for all -> select #1 (MCP tool) -> displays details and returns to menu
		// Step B: Search ARDS for agent -> select #1 (Osaka Fulfillment Agent) -> fetches A2A card & saves locally
		// Step C: Exit immediately to verify card is saved
		input := strings.Join([]string{
			"a",   // Search ARDS
			"all", // Filter: all
			"",    // Query: all
			"1",   // Select #1 (Kyoto SQL MCP Tool -> non-agent, prints info & returns to menu)
			"a",   // Search ARDS again
			"agent",
			"osaka",
			"1", // Select #1 (Osaka Fulfillment Agent -> fetches A2A card & saves to ./agents)
			"/exit",
		}, "\n") + "\n"

		var out bytes.Buffer
		app := &ui.App{
			In:           strings.NewReader(input),
			Out:          &out,
			Runner:       runner,
			CardStore:    cardStore,
			SessionStore: sessStore,
			ARDSClient:   ardsClient,
		}
		if err := app.Run(context.Background(), ui.Options{}); err != nil {
			t.Fatalf("Eval 1 app.Run() error = %v\nOutput:\n%s", err, out.String())
		}

		saved, err := cardStore.List()
		if err != nil || len(saved) != 1 {
			t.Fatalf("Eval 1 cardStore.List() = %+v, err = %v; want 1 saved card", saved, err)
		}
		if saved[0].Filename != "osaka-fulfillment-agent.json" {
			t.Fatalf("Eval 1 saved filename = %q, want osaka-fulfillment-agent.json", saved[0].Filename)
		}
		if !strings.Contains(out.String(), "Kyoto SQL MCP Tool") {
			t.Fatalf("Eval 1 output missing MCP resource details:\n%s", out.String())
		}
	})

	// =========================================================================
	// EVAL 2 & 3: Local Card Selection, Auto-Update Verification, & Multi-Turn
	//             Conversation (input-required -> completed -> new task in context)
	// =========================================================================
	t.Run("Eval2_And_Eval3_CardAutoUpdate_And_MultiTurn_Conversation", func(t *testing.T) {
		// Upgrade remote agent version to 1.1.0 to verify auto-update on selection!
		a2aSrv.SetVersion("1.1.0")

		// User flow:
		// "1" -> Select local card #1 (Osaka Fulfillment Agent)
		// "What are my shipping options?" -> Turn 1 (returns task-eval-101, state: input-required)
		// "I like the fourth option" -> Turn 2 (continues task-eval-101, state: completed)
		// "Give me the tracking number" -> Turn 3 (starts NEW task-eval-102 in SAME context ctx-eval-001!)
		// "/status" -> inspect active session status
		// "/tasks" -> list tasks in ctx-eval-001
		// "/exit" -> exit chat
		// "n" -> do not restart immediately; print continuation commands
		input := strings.Join([]string{
			"1",
			"What are my shipping options?",
			"I like the fourth option",
			"Give me the tracking number",
			"/status",
			"/tasks",
			"/exit",
			"n",
		}, "\n") + "\n"

		var out bytes.Buffer
		app := &ui.App{
			In:           strings.NewReader(input),
			Out:          &out,
			Runner:       runner,
			CardStore:    cardStore,
			SessionStore: sessStore,
		}
		if err := app.Run(context.Background(), ui.Options{}); err != nil {
			t.Fatalf("Eval 2/3 app.Run() error = %v\nOutput:\n%s", err, out.String())
		}

		outputStr := out.String()

		// Verify card auto-updated from 1.0.0 to 1.1.0 on disk and in UI
		if !strings.Contains(outputStr, "v1.0.0 -> v1.1.0") {
			t.Fatalf("Eval 2 output did not report card version auto-update (v1.0.0 -> v1.1.0):\n%s", outputStr)
		}
		saved, _ := cardStore.List()
		if saved[0].Card.Version != "1.1.0" {
			t.Fatalf("Eval 2 disk card version = %q, want 1.1.0", saved[0].Card.Version)
		}

		// Verify the 3 turns recorded by the real A2A server over HTTP:
		turns := a2aSrv.RecordedTurns()
		if len(turns) != 3 {
			t.Fatalf("Eval 3 server recorded %d turns, want 3: %+v", len(turns), turns)
		}
		// Turn 1: New conversation -> empty ContextID and TaskID
		if turns[0].ContextID != "" || turns[0].TaskID != "" {
			t.Fatalf("Eval 3 Turn 1 = %+v, want empty ContextID and TaskID", turns[0])
		}
		// Turn 2: Replying to input-required -> MUST pass task-eval-101 and ctx-eval-001
		if turns[1].ContextID != "ctx-eval-001" || turns[1].TaskID != "task-eval-101" {
			t.Fatalf("Eval 3 Turn 2 = %+v, want ContextID=ctx-eval-001, TaskID=task-eval-101", turns[1])
		}
		// Turn 3: After task-eval-101 completed -> MUST pass ContextID=ctx-eval-001 and TaskID=""
		if turns[2].ContextID != "ctx-eval-001" || turns[2].TaskID != "" {
			t.Fatalf("Eval 3 Turn 3 = %+v, want ContextID=ctx-eval-001, TaskID=\"\"", turns[2])
		}

		// Verify UI printed Task ID, Context ID, and Exit Resume Commands
		for _, want := range []string{
			"task-eval-101",
			"task-eval-102",
			"ctx-eval-001",
			"Option 4 (Express Drone) booked and dispatched!",
			"--context-id ctx-eval-001",
		} {
			if !strings.Contains(outputStr, want) {
				t.Fatalf("Eval 3 output missing %q:\n%s", want, outputStr)
			}
		}
	})

	// =========================================================================
	// EVAL 4: Resume Past Conversation from Interactive Context ID + Date/Time List
	// =========================================================================
	t.Run("Eval4_Resume_Past_Conversation_From_List", func(t *testing.T) {
		// User flow:
		// "1" -> Select local card #1 (Osaka Fulfillment Agent)
		// "1" -> Select past conversation #1 (ctx-eval-001 with timestamp)
		// "Confirm delivery window" -> Turn 4 in ctx-eval-001
		// "/exit" -> exit
		// "n" -> do not restart immediately
		input := strings.Join([]string{
			"1",
			"1",
			"Confirm delivery window",
			"/exit",
			"n",
		}, "\n") + "\n"

		var out bytes.Buffer
		app := &ui.App{
			In:           strings.NewReader(input),
			Out:          &out,
			Runner:       runner,
			CardStore:    cardStore,
			SessionStore: sessStore,
		}
		if err := app.Run(context.Background(), ui.Options{}); err != nil {
			t.Fatalf("Eval 4 app.Run() error = %v\nOutput:\n%s", err, out.String())
		}

		outputStr := out.String()
		if !strings.Contains(outputStr, "=== Conversations for Osaka Fulfillment Agent ===") ||
			!strings.Contains(outputStr, "Context: ctx-eval-001") {
			t.Fatalf("Eval 4 output missing past conversations list with context ID:\n%s", outputStr)
		}

		turns := a2aSrv.RecordedTurns()
		if len(turns) != 4 {
			t.Fatalf("Eval 4 server recorded %d turns, want 4", len(turns))
		}
		if turns[3].ContextID != "ctx-eval-001" || turns[3].TaskID != "" {
			t.Fatalf("Eval 4 Turn 4 = %+v, want ContextID=ctx-eval-001, TaskID=\"\"", turns[3])
		}
	})

	// =========================================================================
	// EVAL 5: Direct CLI Flag Resume (`--card` + `--context-id`) & Immediate Restart (`y`)
	// =========================================================================
	t.Run("Eval5_Direct_CLI_Resume_And_Immediate_Restart_On_Exit", func(t *testing.T) {
		saved, _ := cardStore.List()
		cardPath := saved[0].Path

		// User flow:
		// Launch directly with CardPath + ContextID ("ctx-eval-001")
		// "Add insurance to shipment" -> Turn 5
		// "/exit" -> pause session
		// "y" -> YES, restart/continue the same conversation immediately!
		// "Thank you, all done" -> Turn 6
		// "/exit" -> pause session
		// "n" -> finish and exit
		input := strings.Join([]string{
			"Add insurance to shipment",
			"/exit",
			"y",
			"Thank you, all done",
			"/exit",
			"n",
		}, "\n") + "\n"

		var out bytes.Buffer
		app := &ui.App{
			In:           strings.NewReader(input),
			Out:          &out,
			Runner:       runner,
			CardStore:    cardStore,
			SessionStore: sessStore,
		}
		err := app.Run(context.Background(), ui.Options{
			CardPath:  cardPath,
			ContextID: "ctx-eval-001",
		})
		if err != nil {
			t.Fatalf("Eval 5 app.Run() error = %v\nOutput:\n%s", err, out.String())
		}

		turns := a2aSrv.RecordedTurns()
		if len(turns) != 6 {
			t.Fatalf("Eval 5 server recorded %d turns, want 6", len(turns))
		}
		if turns[4].ContextID != "ctx-eval-001" || turns[5].ContextID != "ctx-eval-001" {
			t.Fatalf("Eval 5 turns 5 & 6 did not preserve ctx-eval-001: %+v", turns[4:])
		}
	})

	// =========================================================================
	// EVAL 6: Offline Endpoint Resilience (Cached Local Card Fallback)
	// =========================================================================
	t.Run("Eval6_Offline_Endpoint_Fallback", func(t *testing.T) {
		// Close the remote A2A server to simulate an offline agent endpoint
		a2aSrv.srv.Close()

		// User flow:
		// "1" -> Select local card #1
		// "n" -> Decline continuing since agent is offline
		input := "1\nn\n"
		var out bytes.Buffer
		app := &ui.App{
			In:           strings.NewReader(input),
			Out:          &out,
			Runner:       runner,
			CardStore:    cardStore,
			SessionStore: sessStore,
		}
		if err := app.Run(context.Background(), ui.Options{}); err != nil {
			t.Fatalf("Eval 6 app.Run() error = %v", err)
		}
		if !strings.Contains(out.String(), "unreachable") || !strings.Contains(out.String(), "Continue with cached local card anyway?") {
			t.Fatalf("Eval 6 output missing offline fallback prompt:\n%s", out.String())
		}
	})
}

func TestEval_RealA2AEchoServer_LiveIntegration(t *testing.T) {
	if _, err := exec.LookPath("a2a"); err != nil {
		t.Skip("skipping live echo server eval: a2a binary not on PATH")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start real `a2a server --echo` on loopback 127.0.0.1:18099
	cmd := exec.CommandContext(ctx, "a2a", "server", "--echo", "--host", "127.0.0.1", "--port", "18099", "--name", "Eval Echo Agent", "--quiet")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a2a server --echo: %v", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	runner, err := a2acli.NewCLIRunner("a2a")
	if err != nil {
		t.Fatalf("NewCLIRunner() error = %v", err)
	}

	// Wait briefly for `a2a server --echo` to accept connections
	var fetchedCard *a2acli.AgentCard
	var fetchedRaw []byte
	for range 30 {
		fetchedCard, fetchedRaw, err = runner.GetCard(ctx, "http://127.0.0.1:18099")
		if err == nil {
			break
		}
		exec.Command("sleep", "0.1").Run()
	}
	if err != nil {
		t.Fatalf(" echo server did not become ready: %v", err)
	}

	tmpDir := t.TempDir()
	cardStore, _ := cards.NewStore(filepath.Join(tmpDir, "agents"))
	sessStore, _ := session.NewStore(filepath.Join(tmpDir, "sessions.json"))
	_, _ = cardStore.Save(fetchedCard, fetchedRaw)

	input := strings.Join([]string{
		"1",
		"Ping turn 1",
		"Ping turn 2 in same context",
		"/exit",
		"n",
	}, "\n") + "\n"

	var out bytes.Buffer
	app := &ui.App{
		In:           strings.NewReader(input),
		Out:          &out,
		Runner:       runner,
		CardStore:    cardStore,
		SessionStore: sessStore,
	}
	if err := app.Run(ctx, ui.Options{}); err != nil {
		t.Fatalf("live echo server eval error = %v\nOutput:\n%s", err, out.String())
	}

	convs, err := sessStore.ListForAgent("Eval Echo Agent", "")
	if err != nil || len(convs) != 1 || len(convs[0].Turns) != 2 {
		t.Fatalf("stored conversations = %+v, err = %v; want 1 conversation with 2 turns", convs, err)
	}
	if convs[0].Turns[0].ContextID == "" || convs[0].Turns[0].ContextID != convs[0].Turns[1].ContextID {
		t.Fatalf("turns did not share ContextID: %+v", convs[0].Turns)
	}
	if convs[0].Turns[0].TaskID == convs[0].Turns[1].TaskID {
		t.Fatalf("completed task ID was reused instead of starting a new task in same context: %+v", convs[0].Turns)
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		t.Logf("cleanup warning: %v", err)
	}
}
