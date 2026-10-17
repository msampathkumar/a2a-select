# a2a-select

[![Release](https://img.shields.io/badge/release-v0.1.0-blue.svg)](https://github.com/msampathkumar/a2a-select/releases)
[![Go Version](https://img.shields.io/badge/go-1.24%2B-00ADD8.svg)](https://go.dev/)
[![A2A Protocol](https://img.shields.io/badge/A2A_Protocol-v1.0_%7C_v0.3-6f42c1.svg)](https://a2a-protocol.org/)
[![License](https://img.shields.io/badge/License-Apache_2.0-green.svg)](./LICENSE)

**`a2a-select`** is an interactive **Agent Card selector, ARDS Registry explorer, and stateful multi-turn A2A conversation client** built on top of the [`a2a` CLI](https://github.com/a2aproject/a2a).

Instead of manually copying `--agent-card`, `--context-id`, and `--task-id` flags on every turn, `a2a-select` gives you a human-friendly interactive session that automatically verifies and refreshes saved Agent Cards, manages the A2A Task & Context state machine across multi-turn conversations, and prints exact resume commands whenever you pause or exit.

It works both as a standalone binary (`a2a-select`) and as a native **`a2a` Command Plugin** (`a2a select`), and pairs seamlessly with [**`a2a-mailbox`**](https://github.com/msampathkumar/a2a-mailbox).

---

## Architecture & Workflows

### 1. System Architecture

```mermaid
flowchart LR
    User(["👤 Developer / Operator"])
    subgraph CLI["a2a CLI Ecosystem"]
        A2A["a2a CLI (v0.3+)"]
        Select["a2a-select (a2a select)"]
    end

    subgraph LocalStorage["Local State (0600 perms)"]
        CardsDir["./agents/*.json\nSaved Agent Cards"]
        SessionsFile["~/.config/a2a-select/sessions.json\nContext & Task History"]
    end

    subgraph Remote["Remote Services"]
        ARDS["ARDS Registry\nGET /api/v1/search"]
        Agent["Remote A2A Agent or a2a-mailbox\n/.well-known/agent-card.json\nPOST /message:send"]
    end

    User -->|"Interactive REPL"| Select
    A2A -.->|"Plugin dispatch"| Select
    Select <-->|"Read / Auto-Update"| CardsDir
    Select <-->|"Persist turns & contextId"| SessionsFile
    Select -->|"Discover skills, MCPs & agents"| ARDS
    Select -->|"Subprocess: a2a card get / send"| A2A
    A2A <-->|"A2A v1.0 HTTP+JSON / gRPC"| Agent
```

### 2. A2A Task & Context Lifecycle State Machine

In the A2A protocol, **a completed task (`completed`, `failed`, `canceled`, `rejected`) cannot be restarted**, whereas a paused task (`input-required`, `auth-required`) must be continued with the same `taskId`. `a2a-select` handles this automatically on every turn:

```mermaid
stateDiagram-v2
    [*] --> NewConversation: Select Agent Card -> [N] New Conversation
    NewConversation --> Turn1: Send message (no --context-id, no --task-id)

    state "Active Turn Evaluation" as Turn1
    Turn1 --> PausedTask: Agent returns input-required / auth-required / working
    Turn1 --> CompletedTask: Agent returns completed / canceled / failed / rejected

    PausedTask --> Turn1: Next reply sends --context-id <ctx> --task-id <task>
    CompletedTask --> Turn1: Next reply sends --context-id <ctx> (omits --task-id to start new task in same context!)

    PausedTask --> ExitSummary: User types /exit
    CompletedTask --> ExitSummary: User types /exit
    ExitSummary --> [*]: Prints exact resume CLI command
```

### 3. End-to-End Discovery, Auto-Update & Multi-Turn Sequence

```mermaid
sequenceDiagram
    autonumber
    actor User
    participant Select as a2a-select
    participant ARDS as ARDS Registry
    participant Store as Local Card & Session Store
    participant A2A as a2a CLI
    participant Agent as Remote A2A Agent

    User->>Select: Launch `a2a select` -> [A] Search ARDS
    Select->>ARDS: GET /api/v1/search?type=agent&q=fulfillment
    ARDS-->>Select: Matching Agent metadata + endpoint URL
    Select->>A2A: a2a card get <endpoint>
    A2A->>Agent: GET /.well-known/agent-card.json
    Agent-->>A2A: Agent Card JSON (v1.0)
    Select->>Store: Save ./agents/<slug>.json (0600)

    User->>Select: Select saved local Agent Card
    Select->>A2A: Verify live endpoint (`a2a card get`)
    alt Remote card updated
        Select->>Store: Auto-update ./agents/<slug>.json
    else Endpoint offline
        Select-->>User: Prompt to continue with cached local card
    end

    User->>Select: "Give me 5 shipping options"
    Select->>A2A: a2a send -a <card> "Give me 5 shipping options" -o json
    A2A->>Agent: POST /message:send
    Agent-->>Select: Task (id=task-1, contextId=ctx-1, state=input-required)
    Select->>Store: Save Context ID ctx-1 & Task ID task-1

    User->>Select: "I like the 4th option"
    Select->>A2A: a2a send -a <card> --context-id ctx-1 --task-id task-1 "I like the 4th option"
    Agent-->>Select: Task (id=task-1, contextId=ctx-1, state=completed)

    User->>Select: "Send receipt to accounting"
    Note over Select,A2A: task-1 is completed -> omit --task-id, keep --context-id ctx-1
    Select->>A2A: a2a send -a <card> --context-id ctx-1 "Send receipt to accounting"
    Agent-->>Select: Task (id=task-2, contextId=ctx-1, state=completed)
```

---

## Installation

### Prerequisites
- [`a2a` CLI](https://github.com/a2aproject/a2a) (`v0.3.0+`) installed on your `PATH`.
- Go `1.24+` (zero external dependencies).

### Install via `go install` or `Makefile`

```bash
git clone https://github.com/msampathkumar/a2a-select.git
cd a2a-select
make install
```

### Enable as a Native `a2a` Command Plugin

```bash
a2a plugin set-enabled true
a2a plugin list
```

Output:
```text
Command plugins: enabled
NAME     VERSION  DESCRIPTION                                                                     PATH
mailbox  0.1.0    Executor-less A2A inbox server backed by local DB, tk tickets, or Pub/Sub       /opt/homebrew/bin/a2a-mailbox
select   0.1.0    Interactive agent card selector, ARDS discovery, and stateful A2A chat session  /opt/homebrew/bin/a2a-select
```

---

## Usage & Interactive Walkthrough

### Launch Interactive Selector
```bash
a2a select
# or directly:
a2a-select
```

```text
╭──────────────────────────────────────────────────────────────╮
│  a2a-select — Interactive Agent Selector & Stateful Chat     │
╰──────────────────────────────────────────────────────────────╯

=== Local Saved Agent Cards (./agents) ===
  [1] Osaka Fulfillment Agent      v1.1.0     http://127.0.0.1:8080  (osaka-fulfillment-agent.json)
  [2] A2A Mailbox (tk backend)     v0.1.0     http://127.0.0.1:8085  (a2a-mailbox.json)

Actions:
  [1-2] Select a local Agent Card
  [A]   Search ARDS Registry (skills, tools, APIs, or A2A agents)
  [F]   Fetch & save an Agent Card from URL
  [Q]   Quit
```

### Multi-Turn Chat Session & Exit Resume Summary
```text
You > Give me 5 shipping options for order #42

Agent (Osaka Fulfillment Agent):
Here are 5 options: [1] Air [2] Sea [3] Rail [4] Express Drone [5] Courier. Which one do you want?
  ┌─ Turn Details ─────────────────────────────────────────────
  │ Task ID:     task-101
  │ Task Status: input-required
  │ Context ID:  ctx-900
  └────────────────────────────────────────────────────────────

You > I like the 4th option, please book Express Drone
  ↳ Continuing active task task-101 in context ctx-900 ...

Agent (Osaka Fulfillment Agent):
Option #4 (Express Drone) confirmed and booked!
  ┌─ Turn Details ─────────────────────────────────────────────
  │ Task ID:     task-101
  │ Task Status: completed
  │ Context ID:  ctx-900
  └────────────────────────────────────────────────────────────

You > /exit

--- Session Paused ---
  Agent:        Osaka Fulfillment Agent
  Context ID:   ctx-900
  Last Task ID: task-101 (status: completed)
Would you like to restart/continue this conversation right now? [y/N]: n

=== Continue This Conversation Later ===
1. Resume interactive chat in a2a-select:
   a2a-select --card ./agents/osaka-fulfillment-agent.json --context-id ctx-900

2. Or send the next turn directly with the a2a CLI:
   a2a send -a ./agents/osaka-fulfillment-agent.json --context-id ctx-900 "<your follow-up message>"
```

---

## CLI Flags & In-Chat Commands

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--cards-dir` | `./agents` (`$A2A_SELECT_CARDS_DIR`) | Directory containing saved local Agent Card JSON files |
| `--sessions-file` | `~/.config/a2a-select/sessions.json` | File path for storing conversation history and Context IDs |
| `--card` | *(interactive)* | Open a specific local Agent Card JSON file directly |
| `--context-id` | *(interactive)* | Resume a specific conversation `contextId` directly |
| `--task-id` | *(optional)* | Continue a specific paused `taskId` (`input-required`) |
| `--ards-url` | `http://127.0.0.1:8000` (`$ARDS_URL`) | Default ARDS Registry base URL |
| `--a2a-bin` | `a2a` (`$A2A_BIN`) | Path or name of the `a2a` CLI binary |
| `--auth` | `$A2ACLI_AUTH` | Authorization credential passed through to `a2a` commands |
| `--version` | `false` | Print version information (`a2a-select 0.1.0`) and exit |

| In-Chat Command | Description |
| :--- | :--- |
| `/status` | Display current Agent Card path, `Context ID`, last `Task ID`, and task state |
| `/tasks` | Query the remote agent for all tasks belonging to the current `Context ID` |
| `/new` | Start a brand-new `Context ID` with the current agent |
| `/help` | Show available in-chat slash commands |
| `/exit` or `/quit` | Pause session, optionally restart immediately, and print resume commands |

---

## Testing & Black-Box Evals

Run the full unit, integration, and compiled-binary black-box evaluation suite with the Go race detector enabled:

```bash
make test
# or:
go test -v -count=1 -race ./...
```

---

## License

Licensed under the [Apache License, Version 2.0](./LICENSE).
