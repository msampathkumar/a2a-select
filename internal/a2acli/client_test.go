package a2acli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
)

func TestParseAgentCard_V1AndV0(t *testing.T) {
	t.Parallel()

	v1Raw := []byte(`{
  "name": "Osaka Fulfillment Agent",
  "description": "Autonomous warehouse controller",
  "version": "1.2.0",
  "supportedInterfaces": [
    {"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8090"}
  ],
  "capabilities": {"streaming": true},
  "skills": [
    {"id": "inventory_query", "name": "Inventory Query", "description": "Query stock"}
  ]
}`)

	card, err := a2acli.ParseAgentCard(v1Raw)
	if err != nil {
		t.Fatalf("ParseAgentCard(v1) error = %v, want nil", err)
	}
	if card.Name != "Osaka Fulfillment Agent" {
		t.Fatalf("card.Name = %q, want %q", card.Name, "Osaka Fulfillment Agent")
	}
	if got := card.PrimaryURL(); got != "http://127.0.0.1:8090" {
		t.Fatalf("card.PrimaryURL() = %q, want %q", got, "http://127.0.0.1:8090")
	}
	if !card.Capabilities.Streaming {
		t.Fatalf("card.Capabilities.Streaming = false, want true")
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "inventory_query" {
		t.Fatalf("card.Skills = %+v, want 1 skill inventory_query", card.Skills)
	}

	v0Raw := []byte(`{
  "name": "Legacy Agent",
  "version": "0.3.0",
  "url": "http://127.0.0.1:8091"
}`)
	cardV0, err := a2acli.ParseAgentCard(v0Raw)
	if err != nil {
		t.Fatalf("ParseAgentCard(v0) error = %v, want nil", err)
	}
	if got := cardV0.PrimaryURL(); got != "http://127.0.0.1:8091" {
		t.Fatalf("cardV0.PrimaryURL() = %q, want %q", got, "http://127.0.0.1:8091")
	}
}

func TestParseSendOutput_TaskAndMessage(t *testing.T) {
	t.Parallel()

	taskJSON := []byte(`{
  "task": {
    "id": "task-101",
    "contextId": "ctx-202",
    "status": {
      "state": "TASK_STATE_INPUT_REQUIRED",
      "message": {
        "messageId": "msg-1",
        "role": "ROLE_AGENT",
        "parts": [{"text": "Here are 5 options. Which one do you prefer?"}]
      }
    },
    "artifacts": [
      {
        "artifactId": "art-1",
        "name": "options",
        "parts": [{"text": "1. A\n2. B\n3. C\n4. D\n5. E"}]
      }
    ]
  }
}`)

	res, err := a2acli.ParseSendOutput(taskJSON)
	if err != nil {
		t.Fatalf("ParseSendOutput(taskJSON) error = %v, want nil", err)
	}
	if res.TaskID != "task-101" {
		t.Fatalf("res.TaskID = %q, want %q", res.TaskID, "task-101")
	}
	if res.ContextID != "ctx-202" {
		t.Fatalf("res.ContextID = %q, want %q", res.ContextID, "ctx-202")
	}
	if res.State != "input-required" {
		t.Fatalf("res.State = %q, want %q", res.State, "input-required")
	}
	if !strings.Contains(res.ReplyText, "Here are 5 options.") || !strings.Contains(res.ReplyText, "4. D") {
		t.Fatalf("res.ReplyText = %q, want status message and artifact text", res.ReplyText)
	}

	msgJSON := []byte(`{
  "message": {
    "messageId": "msg-99",
    "taskId": "task-102",
    "contextId": "ctx-202",
    "role": "ROLE_AGENT",
    "parts": [{"text": "Direct reply from agent"}]
  }
}`)
	resMsg, err := a2acli.ParseSendOutput(msgJSON)
	if err != nil {
		t.Fatalf("ParseSendOutput(msgJSON) error = %v, want nil", err)
	}
	if resMsg.TaskID != "task-102" || resMsg.ContextID != "ctx-202" {
		t.Fatalf("resMsg IDs = (%q, %q), want (task-102, ctx-202)", resMsg.TaskID, resMsg.ContextID)
	}
	if resMsg.ReplyText != "Direct reply from agent" {
		t.Fatalf("resMsg.ReplyText = %q, want %q", resMsg.ReplyText, "Direct reply from agent")
	}
}

func TestCLIRunner_GetCardAndSend(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	argsFile := filepath.Join(tmpDir, "last_args.txt")
	fakeBin := filepath.Join(tmpDir, "fake-a2a")

	script := `#!/bin/sh
printf "%s\n" "$*" > "` + argsFile + `"
if [ "$1" = "card" ] && [ "$2" = "get" ]; then
  cat << 'EOF'
{
  "name": "Fake Agent",
  "version": "1.0.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8090"}]
}
EOF
  exit 0
fi
if [ "$1" = "send" ]; then
  cat << 'EOF'
{
  "task": {
    "id": "task-abc",
    "contextId": "ctx-xyz",
    "status": {"state": "TASK_STATE_COMPLETED"},
    "artifacts": [{"artifactId": "a1", "parts": [{"text": "Option 4 executed."}]}]
  }
}
EOF
  exit 0
fi
if [ "$1" = "task" ] && [ "$2" = "list" ]; then
  cat << 'EOF'
{
  "tasks": [
    {"id": "task-abc", "contextId": "ctx-xyz", "status": {"state": "TASK_STATE_COMPLETED"}}
  ]
}
EOF
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile(fakeBin) error = %v", err)
	}

	runner, err := a2acli.NewCLIRunner(fakeBin)
	if err != nil {
		t.Fatalf("NewCLIRunner() error = %v", err)
	}

	card, raw, err := runner.GetCard(context.Background(), "http://127.0.0.1:8090")
	if err != nil {
		t.Fatalf("GetCard() error = %v", err)
	}
	if card.Name != "Fake Agent" || len(raw) == 0 {
		t.Fatalf("GetCard() card = %+v", card)
	}

	sendRes, err := runner.Send(context.Background(), a2acli.SendRequest{
		CardRef:   "/tmp/agent.json",
		ContextID: "ctx-xyz",
		TaskID:    "task-prev",
		Message:   "Give me the fourth option",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sendRes.TaskID != "task-abc" || sendRes.ContextID != "ctx-xyz" || sendRes.State != "completed" {
		t.Fatalf("Send() result = %+v", sendRes)
	}
	gotArgsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("ReadFile(argsFile) error = %v", err)
	}
	gotArgs := strings.TrimSpace(string(gotArgsBytes))
	wantArgs := "send -a /tmp/agent.json -o json --context-id ctx-xyz --task-id task-prev --text-part Give me the fourth option"
	if gotArgs != wantArgs {
		t.Fatalf("Send() args = %q, want %q", gotArgs, wantArgs)
	}

	tasks, err := runner.ListTasks(context.Background(), "/tmp/agent.json", "ctx-xyz")
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "task-abc" || tasks[0].State != "completed" {
		t.Fatalf("ListTasks() = %+v", tasks)
	}
}

func TestCLIRunner_ExtractsJSONErrorFromStdout(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "fake-a2a-err")
	script := `#!/bin/sh
cat << 'EOF'
{"error":{"code":"A2ACLI_ERR_PROTOCOL","message":"failed to list tasks: operation not supported"}}
EOF
exit 1
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile(fakeBin) error = %v", err)
	}
	runner, err := a2acli.NewCLIRunner(fakeBin)
	if err != nil {
		t.Fatalf("NewCLIRunner() error = %v", err)
	}
	_, err = runner.ListTasks(context.Background(), "/tmp/agent.json", "ctx-1")
	if err == nil || !strings.Contains(err.Error(), "operation not supported") {
		t.Fatalf("ListTasks() error = %v, want message containing 'operation not supported'", err)
	}
}
