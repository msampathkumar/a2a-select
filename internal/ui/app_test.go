package ui_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
	"github.com/msampathkumar/a2a-select/internal/ards"
	"github.com/msampathkumar/a2a-select/internal/cards"
	"github.com/msampathkumar/a2a-select/internal/session"
	"github.com/msampathkumar/a2a-select/internal/ui"
)

type mockRunner struct {
	getCardFn   func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error)
	sendFn      func(ctx context.Context, req a2acli.SendRequest) (*a2acli.SendResult, error)
	listTasksFn func(ctx context.Context, cardRef, contextID string) ([]a2acli.TaskSummary, error)
	sentReqs    []a2acli.SendRequest
}

func (m *mockRunner) GetCard(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
	return m.getCardFn(ctx, ref)
}

func (m *mockRunner) Send(ctx context.Context, req a2acli.SendRequest) (*a2acli.SendResult, error) {
	m.sentReqs = append(m.sentReqs, req)
	return m.sendFn(ctx, req)
}

func (m *mockRunner) ListTasks(ctx context.Context, cardRef, contextID string) ([]a2acli.TaskSummary, error) {
	if m.listTasksFn != nil {
		return m.listTasksFn(ctx, cardRef, contextID)
	}
	return nil, nil
}

func TestApp_SelectLocalCard_Verify_MultiTurnChat_AndExitResumeCommand(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cardStore, err := cards.NewStore(filepath.Join(tmpDir, "agents"))
	if err != nil {
		t.Fatalf("cards.NewStore() error = %v", err)
	}
	sessStore, err := session.NewStore(filepath.Join(tmpDir, "sessions.json"))
	if err != nil {
		t.Fatalf("session.NewStore() error = %v", err)
	}

	rawV1 := []byte(`{
  "name": "Osaka Agent",
  "description": "Osaka warehouse assistant",
  "version": "1.0.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8090"}]
}`)
	cardV1, _ := a2acli.ParseAgentCard(rawV1)
	if _, err := cardStore.Save(cardV1, rawV1); err != nil {
		t.Fatalf("cardStore.Save() error = %v", err)
	}

	// Remote card has version 1.1.0 so verification updates it.
	rawV2 := []byte(`{
  "name": "Osaka Agent",
  "description": "Osaka warehouse assistant v1.1",
  "version": "1.1.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8090"}]
}`)
	cardV2, _ := a2acli.ParseAgentCard(rawV2)

	turn := 0
	runner := &mockRunner{
		getCardFn: func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
			return cardV2, rawV2, nil
		},
		sendFn: func(ctx context.Context, req a2acli.SendRequest) (*a2acli.SendResult, error) {
			turn++
			switch turn {
			case 1:
				// First message: returns input-required with 5 options on task-1, ctx-500.
				return &a2acli.SendResult{
					TaskID:    "task-1",
					ContextID: "ctx-500",
					State:     "input-required",
					ReplyText: "Options: 1, 2, 3, 4, 5. Which option do you want?",
				}, nil
			case 2:
				// Second message: user picks option 4 -> task-1 completes!
				return &a2acli.SendResult{
					TaskID:    "task-1",
					ContextID: "ctx-500",
					State:     "completed",
					ReplyText: "Option 4 completed!",
				}, nil
			default:
				// Third message: task-1 was completed, so a new task (task-2) runs under ctx-500!
				return &a2acli.SendResult{
					TaskID:    "task-2",
					ContextID: "ctx-500",
					State:     "completed",
					ReplyText: "Receipt sent for Option 4.",
				}, nil
			}
		},
	}

	// User inputs:
	// "1" -> select local agent card #1 (Osaka Agent)
	// "Give me 5 options" -> turn 1
	// "I like the fourth option" -> turn 2 (continues task-1)
	// "Send me the receipt" -> turn 3 (new task under ctx-500)
	// "/exit" -> exit chat
	// "n" -> do not immediately restart
	input := strings.Join([]string{
		"1",
		"Give me 5 options",
		"I like the fourth option",
		"Send me the receipt",
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
		t.Fatalf("app.Run() error = %v\nOutput:\n%s", err, out.String())
	}

	// Verify the 3 Send requests followed the exact A2A task/context rules:
	if len(runner.sentReqs) != 3 {
		t.Fatalf("len(sentReqs) = %d, want 3", len(runner.sentReqs))
	}
	// Turn 1: new conversation -> no contextId, no taskId
	if runner.sentReqs[0].ContextID != "" || runner.sentReqs[0].TaskID != "" {
		t.Fatalf("Turn 1 req = %+v, want empty ContextID and TaskID", runner.sentReqs[0])
	}
	// Turn 2: after input-required -> continues task-1 in ctx-500
	if runner.sentReqs[1].ContextID != "ctx-500" || runner.sentReqs[1].TaskID != "task-1" {
		t.Fatalf("Turn 2 req = %+v, want ContextID=ctx-500 and TaskID=task-1", runner.sentReqs[1])
	}
	// Turn 3: after task-1 completed -> starts a NEW task (TaskID="") in the SAME context (ContextID="ctx-500")
	if runner.sentReqs[2].ContextID != "ctx-500" || runner.sentReqs[2].TaskID != "" {
		t.Fatalf("Turn 3 req = %+v, want ContextID=ctx-500 and TaskID=\"\"", runner.sentReqs[2])
	}

	outputStr := out.String()
	// Check that verification update was displayed
	if !strings.Contains(outputStr, "1.1.0") {
		t.Fatalf("output missing updated card version 1.1.0:\n%s", outputStr)
	}
	// Check that task ID and context ID were printed at each step
	for _, want := range []string{"task-1", "task-2", "ctx-500", "--context-id ctx-500", "Session Tasks in Context ctx-500:"} {
		if !strings.Contains(outputStr, want) {
			t.Fatalf("output missing %q:\n%s", want, outputStr)
		}
	}
}

func TestApp_ResumePastConversationFromList(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cardStore, _ := cards.NewStore(filepath.Join(tmpDir, "agents"))
	sessStore, _ := session.NewStore(filepath.Join(tmpDir, "sessions.json"))

	rawCard := []byte(`{
  "name": "Kyoto Agent",
  "version": "1.0.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8092"}]
}`)
	card, _ := a2acli.ParseAgentCard(rawCard)
	savedCard, _ := cardStore.Save(card, rawCard)

	pastTime := time.Date(2026, 10, 16, 19, 45, 0, 0, time.UTC)
	_ = sessStore.Upsert(session.Conversation{
		ContextID:     "ctx-past-777",
		AgentName:     "Kyoto Agent",
		CardPath:      savedCard.Path,
		LastTaskID:    "task-old-1",
		LastTaskState: "completed",
		CreatedAt:     pastTime,
		UpdatedAt:     pastTime,
		Turns: []session.Turn{
			{
				Timestamp:   pastTime,
				UserMessage: "Check Kyoto inventory",
				AgentReply:  "42 pallets in stock",
				TaskID:      "task-old-1",
				ContextID:   "ctx-past-777",
				TaskState:   "completed",
			},
		},
	})

	runner := &mockRunner{
		getCardFn: func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
			return card, rawCard, nil
		},
		sendFn: func(ctx context.Context, req a2acli.SendRequest) (*a2acli.SendResult, error) {
			return &a2acli.SendResult{
				TaskID:    "task-new-2",
				ContextID: req.ContextID,
				State:     "completed",
				ReplyText: "Reserved 5 pallets from Kyoto.",
			}, nil
		},
	}

	// Inputs:
	// "1" -> select local agent card #1 (Kyoto Agent)
	// "1" -> select past conversation #1 (ctx-past-777)
	// "Reserve 5 pallets" -> follow-up message
	// "/exit" -> exit
	// "n" -> don't restart immediately
	input := strings.Join([]string{
		"1",
		"1",
		"Reserve 5 pallets",
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
		t.Fatalf("app.Run() error = %v\nOutput:\n%s", err, out.String())
	}

	if len(runner.sentReqs) != 1 {
		t.Fatalf("len(sentReqs) = %d, want 1", len(runner.sentReqs))
	}
	if runner.sentReqs[0].ContextID != "ctx-past-777" || runner.sentReqs[0].TaskID != "" {
		t.Fatalf("sentReq = %+v, want ContextID=ctx-past-777 and TaskID=\"\"", runner.sentReqs[0])
	}

	outputStr := out.String()
	if !strings.Contains(outputStr, "ctx-past-777") || !strings.Contains(outputStr, "2026-10-16 19:45:00") {
		t.Fatalf("output missing past context ID or timestamp:\n%s", outputStr)
	}
}

func TestApp_ARDSRegistrySearch_FetchesAndSavesAgentCard(t *testing.T) {
	t.Parallel()

	ardsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "query": "fulfillment",
  "total_results": 1,
  "execution_time_ms": 0.5,
  "results": [
    {
      "id": "agent-osaka",
      "type": "agent",
      "name": "Osaka Fulfillment Agent",
      "description": "Autonomous warehouse controller",
      "endpoint": "http://127.0.0.1:8009",
      "version": "1.0.0",
      "capabilities": ["inventory_query"],
      "health_status": "healthy"
    }
  ]
}`))
	}))
	t.Cleanup(ardsSrv.Close)

	tmpDir := t.TempDir()
	cardStore, _ := cards.NewStore(filepath.Join(tmpDir, "agents"))
	sessStore, _ := session.NewStore(filepath.Join(tmpDir, "sessions.json"))
	ardsClient, _ := ards.NewClient(ardsSrv.URL)

	rawFetched := []byte(`{
  "name": "Osaka Fulfillment Agent",
  "description": "Autonomous warehouse controller",
  "version": "1.0.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8009"}]
}`)
	cardFetched, _ := a2acli.ParseAgentCard(rawFetched)

	runner := &mockRunner{
		getCardFn: func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
			return cardFetched, rawFetched, nil
		},
		sendFn: func(ctx context.Context, req a2acli.SendRequest) (*a2acli.SendResult, error) {
			return &a2acli.SendResult{
				TaskID:    "task-ards-1",
				ContextID: "ctx-ards-1",
				State:     "completed",
				ReplyText: "Hello from Osaka!",
			}, nil
		},
	}

	// Inputs:
	// "a" -> Search ARDS Registry
	// "all" -> resource filter
	// "fulfillment" -> search query
	// "1" -> select result #1 (Osaka Fulfillment Agent) -> fetches & saves card locally!
	// "Hello Osaka" -> chat message
	// "/exit" -> exit chat
	// "n" -> don't restart
	input := strings.Join([]string{
		"a",
		"all",
		"fulfillment",
		"1",
		"Hello Osaka",
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
		ARDSClient:   ardsClient,
	}

	if err := app.Run(context.Background(), ui.Options{}); err != nil {
		t.Fatalf("app.Run() error = %v\nOutput:\n%s", err, out.String())
	}

	savedCards, err := cardStore.List()
	if err != nil || len(savedCards) != 1 {
		t.Fatalf("cardStore.List() = %+v, err = %v; want 1 saved card from ARDS", savedCards, err)
	}
	if savedCards[0].Card.Name != "Osaka Fulfillment Agent" {
		t.Fatalf("saved card name = %q, want Osaka Fulfillment Agent", savedCards[0].Card.Name)
	}
}
