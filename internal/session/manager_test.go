package session_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
	"github.com/msampathkumar/a2a-select/internal/session"
)

func TestConversation_TaskAndContextLifecycle(t *testing.T) {
	t.Parallel()

	conv := session.NewConversation("Osaka Fulfillment Agent", "/tmp/agents/osaka.json")

	// 1. Brand new conversation: both contextID and taskID are empty.
	ctxID, taskID := conv.NextSendParams()
	if ctxID != "" || taskID != "" {
		t.Fatalf("initial NextSendParams() = (%q, %q), want (\"\", \"\")", ctxID, taskID)
	}

	// 2. Agent replies with input-required on task-1 under ctx-100.
	t1 := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	conv.RecordTurn("Show me shipping options", &a2acli.SendResult{
		TaskID:    "task-1",
		ContextID: "ctx-100",
		State:     "input-required",
		ReplyText: "Here are 5 options. Pick one.",
	}, t1)

	// Next turn MUST continue task-1 under ctx-100.
	ctxID, taskID = conv.NextSendParams()
	if ctxID != "ctx-100" || taskID != "task-1" {
		t.Fatalf("after input-required NextSendParams() = (%q, %q), want (\"ctx-100\", \"task-1\")", ctxID, taskID)
	}

	// 3. User replies "Option 4", and task-1 completes!
	t2 := t1.Add(30 * time.Second)
	conv.RecordTurn("I like the fourth option", &a2acli.SendResult{
		TaskID:    "task-1",
		ContextID: "ctx-100",
		State:     "completed",
		ReplyText: "Option 4 booked.",
	}, t2)

	// 4. Next turn in the SAME conversation: since task-1 is completed, we CANNOT reuse task-1;
	// we MUST start a new task with the SAME contextId ("ctx-100") and taskID = "".
	ctxID, taskID = conv.NextSendParams()
	if ctxID != "ctx-100" || taskID != "" {
		t.Fatalf("after completed NextSendParams() = (%q, %q), want (\"ctx-100\", \"\")", ctxID, taskID)
	}

	// 5. User asks a follow-up question in the same context; server assigns task-2 under ctx-100.
	t3 := t2.Add(1 * time.Minute)
	conv.RecordTurn("Send me the tracking number", &a2acli.SendResult{
		TaskID:    "task-2",
		ContextID: "ctx-100",
		State:     "completed",
		ReplyText: "Tracking: JP-998877",
	}, t3)

	if conv.LastTaskID != "task-2" || conv.ContextID != "ctx-100" || len(conv.Turns) != 3 {
		t.Fatalf("conv state = %+v, want LastTaskID=task-2, ContextID=ctx-100, Turns=3", conv)
	}
}

func TestStore_SaveListAndGetByContextID(t *testing.T) {
	t.Parallel()

	storePath := filepath.Join(t.TempDir(), "sessions.json")
	store, err := session.NewStore(storePath)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	tOld := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)
	tNew := time.Date(2026, 10, 16, 20, 30, 0, 0, time.UTC)

	c1 := session.Conversation{
		ContextID:     "ctx-older",
		AgentName:     "Osaka Agent",
		CardPath:      "/agents/osaka.json",
		LastTaskID:    "task-10",
		LastTaskState: "completed",
		CreatedAt:     tOld,
		UpdatedAt:     tOld,
		Turns: []session.Turn{
			{Timestamp: tOld, UserMessage: "Hello", AgentReply: "Hi", TaskID: "task-10", ContextID: "ctx-older", TaskState: "completed"},
		},
	}
	c2 := session.Conversation{
		ContextID:     "ctx-newer",
		AgentName:     "Osaka Agent",
		CardPath:      "/agents/osaka.json",
		LastTaskID:    "task-20",
		LastTaskState: "input-required",
		CreatedAt:     tNew,
		UpdatedAt:     tNew,
		Turns: []session.Turn{
			{Timestamp: tNew, UserMessage: "List options", AgentReply: "1..5", TaskID: "task-20", ContextID: "ctx-newer", TaskState: "input-required"},
		},
	}
	cOther := session.Conversation{
		ContextID:     "ctx-kyoto",
		AgentName:     "Kyoto Agent",
		CardPath:      "/agents/kyoto.json",
		LastTaskID:    "task-99",
		LastTaskState: "completed",
		CreatedAt:     tNew,
		UpdatedAt:     tNew,
	}

	for _, c := range []session.Conversation{c1, c2, cOther} {
		if err := store.Upsert(c); err != nil {
			t.Fatalf("store.Upsert(%s) error = %v", c.ContextID, err)
		}
	}

	osakaConvs, err := store.ListForAgent("Osaka Agent", "/agents/osaka.json")
	if err != nil {
		t.Fatalf("ListForAgent() error = %v", err)
	}
	if len(osakaConvs) != 2 {
		t.Fatalf("len(osakaConvs) = %d, want 2", len(osakaConvs))
	}
	// Most recent conversation should be first
	if osakaConvs[0].ContextID != "ctx-newer" || osakaConvs[1].ContextID != "ctx-older" {
		t.Fatalf("osakaConvs order = [%s, %s], want [ctx-newer, ctx-older]", osakaConvs[0].ContextID, osakaConvs[1].ContextID)
	}

	got, ok, err := store.Get("ctx-newer")
	if err != nil || !ok {
		t.Fatalf("store.Get(ctx-newer) = (%v, %v, %v), want found", got, ok, err)
	}
	if got.LastTaskID != "task-20" || got.LastTaskState != "input-required" {
		t.Fatalf("got = %+v", got)
	}
}
