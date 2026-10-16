# `a2a-select` — Interactive A2A Agent Card Selector, ARDS Browser & Stateful Chat CLI

`a2a-select` builds on top of the official [`a2a` CLI](https://github.com/a2aproject/a2a-cli) (`github.com/a2aproject/a2a-cli`) to turn multi-step, stateless CLI invocations into a single interactive terminal experience.

It works both as a standalone CLI (`a2a-select`) and as a native **`a2a` Command Plugin** (`a2a select`).

---

## ✨ Key Features

1. **Local Saved Agent Cards Library (`./agents`)**
   - Displays all locally saved A2A Agent Card `.json` files with their name, version, and primary endpoint.
2. **Live Agent Card Verification & Auto-Update**
   - Whenever you select a local Agent Card, `a2a-select` checks the agent's live endpoint via `a2a card get <url> -o json`.
   - If the remote card has changed (e.g., new version, description, or skills), `a2a-select` automatically updates your local `.json` file.
   - If the remote endpoint is offline, `a2a-select` warns you and lets you continue with the cached card.
3. **ARDS Registry Discovery & Card Import**
   - Search an **ARDS (Agent & Resource Directory Service)** registry (`/api/v1/search`) across `agent`, `skill`, `mcp`, or `plugin` entries.
   - Inspect capabilities, tags, health status, and endpoints interactively.
   - When you select an A2A `agent`, `a2a-select` fetches its live A2A Agent Card and saves it into your local cards folder automatically.
4. **Past Conversation History (`contextId` & Date/Time)**
   - Tracks past conversations per agent, displaying a selectable list of `Context ID`, timestamp (`YYYY-MM-DD HH:MM:SS UTC`), last task state, last task ID, and message preview—or lets you start a fresh conversation.
5. **Protocol-Aware A2A Task & Context State Machine**
   - **New conversation:** Sends the first message without `--context-id` or `--task-id` and records the server-assigned identifiers.
   - **Interrupted task (`input-required` / `auth-required`):** Automatically passes `--task-id <taskId> --context-id <contextId>` on your next message to continue the paused task.
   - **Completed task (`completed` / terminal):** Since A2A tasks cannot be restarted once completed, your next message automatically omits `--task-id` and passes `--context-id <contextId>` to start a **new task** within the **same conversation context**.
6. **Turn-by-Turn Telemetry & One-Command Resume**
   - Prints `Task ID`, `Task Status`, and `Context ID` in a structured box after every turn.
   - On exit (`/exit`, `/quit`, or `Ctrl+D`), asks if you want to immediately restart/continue the conversation, and prints copy-pasteable commands to resume the exact session later.

---

## 🚀 Quickstart

### Prerequisites
Make sure the `a2a` CLI is installed and on your `PATH`:
```bash
a2a version
```

### Build & Install
```bash
go build -o a2a-select .

# Optional: Install onto your PATH so `a2a` discovers it as `a2a select`
cp a2a-select /opt/homebrew/bin/a2a-select
a2a plugin set-enabled true
a2a plugin list
```

### Usage

```bash
# Start interactive selector (uses ./agents for cards by default)
./a2a-select

# Or run via the a2a CLI plugin system:
a2a select

# Resume a specific conversation directly:
./a2a-select --card ./agents/echo-agent.json --context-id <context-id>
```

### In-Chat Slash Commands

| Command | Description |
| :--- | :--- |
| `/status` or `/info` | Display active Agent Card, `Context ID`, `Last Task ID`, and task status |
| `/tasks` | Query the remote agent (`a2a task list`) for all tasks in the current `Context ID` |
| `/new` | Start a brand-new conversation (`Context ID` reset) with the current agent |
| `/help` | List available chat commands |
| `/exit` or `/quit` | Exit the chat session and print session continuation commands |

### CLI Flags

| Flag | Environment Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `--cards-dir` | `A2A_SELECT_CARDS_DIR` | `./agents` | Directory storing local Agent Card JSON files |
| `--sessions-file` | `A2A_SELECT_SESSIONS_FILE` | `~/.config/a2a-select/sessions.json` | File storing conversation history and context IDs |
| `--card` | | | Path to a local Agent Card JSON file to open directly |
| `--context-id` | | | Resume a specific conversation `contextId` directly |
| `--task-id` | | | Continue a specific interrupted `taskId` directly |
| `--ards-url` | `ARDS_URL` | `http://127.0.0.1:8000` | Default ARDS Registry URL |
| `--a2a-bin` | `A2A_BIN` | `a2a` | Path or name of the `a2a` CLI binary |
| `--auth` | `A2ACLI_AUTH` | | Authorization credential forwarded to `a2a` |

---

## 🧪 Running Tests & End-to-End Evals

`a2a-select` includes both unit tests and an end-to-end evaluation suite ([`evals/eval_test.go`](./evals/eval_test.go)) that spins up a live stateful A2A v1.0 HTTP+JSON server, a live ARDS discovery server, and a live `a2a server --echo` instance driven by the real `a2a` CLI binary:

```bash
# Run all unit tests and end-to-end evals with race detection
go test -v -count=1 -race ./...

# Run only the end-to-end eval suite
go test -v -count=1 ./evals/...
```

