# `a2a-select` — Design Specification

**Date:** 2026-10-16  
**Project:** `a2a-select`  
**Language & Runtime:** Go (100% Go standard library, zero external dependencies; compatible with `a2a` CLI and `a2a` Command Plugin contract)

---

## 1. Intent & Why

The official `a2a` CLI (`github.com/a2aproject/a2a-cli`) is a stateless protocol client: every invocation requires passing the agent card reference (`-a`), and multi-turn conversations require manually tracking and passing `--task-id` (when a task is paused in `input-required` / `auth-required`) or `--context-id` (when a task completes and a follow-up turn starts a new task in the same context). Discovering agents in an ARDS (Agent & Resource Directory Service) registry and saving their A2A Agent Cards locally also requires separate manual steps.

`a2a-select` provides a single, human-friendly interactive CLI (which also works natively as the `a2a select` command plugin) to:
1. Select a saved local A2A Agent Card (or discover/fetch one from an ARDS registry or remote URL and save it locally).
2. Verify that the selected local Agent Card is still valid and up to date against its live endpoint, updating the local file when the remote card has changed.
3. Choose between starting a new conversation or resuming a past conversation (`contextId` + timestamp + summary) stored for that agent.
4. Conduct a stateful, multi-turn chat session that automatically enforces A2A's task/context lifecycle rules, prints the active `taskId` and `contextId` on every turn, and prints a ready-to-run continuation command upon exit.

---

## 2. Goals, Non-Goals, and Anti-Goals

### Goals
- **Seamless `a2a` Integration & Command Plugin Support:** Work as both `a2a-select` standalone and `a2a select` (by implementing the `a2a-select info` command plugin contract).
- **Local Agent Card Library:** Scan and display all saved Agent Card JSON files in the local cards directory (`./agents` by default, configurable via `--cards-dir` or `A2A_SELECT_CARDS_DIR`).
- **Live Card Verification & Update:** When a local card is selected, resolve its live card via `a2a card get <endpoint> -o json`. If the remote card has changed (e.g., version, description, interfaces, capabilities, skills), update the local JSON file safely. If the agent is unreachable, warn clearly and allow the user to either abort or continue using the cached card.
- **ARDS Registry Discovery & Card Fetching:** Provide an interactive option (and `--ards-url` flag) to search an ARDS registry (`/api/v1/search`) across resources (`agent`, `skill`, `mcp`, `plugin`), inspect details, and for A2A agents, automatically fetch their A2A card via `a2a card get` and save it into the local cards directory.
- **Per-Agent Conversation History (`contextId` & Timestamp):** Persist conversation sessions locally (`~/.config/a2a-select/sessions.json` or `--sessions-file`), recording `contextId`, last `taskId`, last task state, `createdAt`, `updatedAt`, and message turn history.
- **Correct A2A Task/Context State Machine:**
  - First message in a new conversation sends without `--context-id` or `--task-id`, capturing the server-assigned `contextId` and `taskId`.
  - If the latest task is in an **interrupted** state (`TASK_STATE_INPUT_REQUIRED`, `TASK_STATE_AUTH_REQUIRED`, `input-required`, `auth-required`), the next message passes `--task-id <taskId>` (and `--context-id <contextId>`) to continue that task.
  - Once a task reaches a **terminal** state (`TASK_STATE_COMPLETED`, `TASK_STATE_FAILED`, `TASK_STATE_CANCELED`, `TASK_STATE_REJECTED`, `completed`, etc.), it cannot be restarted; the next message in the conversation omits `--task-id` and passes `--context-id <contextId>` to start a new task under the same context.
- **Turn-by-Turn Telemetry & Clean Exit:** Print `Task ID`, `Task Status`, and `Context ID` at every step of the conversation. On exit (`/exit`, `/quit`, or EOF), display a session summary and the exact CLI command(s) to resume that conversation later.

### Non-Goals
- Re-implementing A2A transport protocols (gRPC, JSON-RPC, REST) from scratch—`a2a-select` delegates A2A network operations to the `a2a` CLI binary (`--a2a-bin`, default `a2a`).
- Hosting an A2A server or ARDS server inside `a2a-select`.

### Anti-Goals
- Requiring complex TUI raw-terminal curses dependencies that break scrollback, copy-paste, or non-TTY pipes. Prompts must use clean, readable line-oriented ANSI output that works reliably in any terminal and in automated tests.
- Silently swallowing task/context IDs or mutating card files outside the designated cards directory.

---

## 3. Architecture & Components

```
a2a-select/
├── main.go                       # Entrypoint & `info` plugin subcommand handler
├── internal/
│   ├── a2acli/                   # Wrapper around the `a2a` binary (card get, send, task list)
│   ├── ards/                     # HTTP client for ARDS Registry search (/api/v1/search)
│   ├── cards/                    # Local Agent Card store, slugging, verification, and diff/update
│   ├── session/                  # Conversation history store & A2A Task/Context state machine
│   └── ui/                       # Interactive CLI flow (menus, ARDS browser, chat REPL, exit summary)
```

### Key Decisions (Builder Calls)
1. **Language:** Go (`go 1.25+`), matching `a2a-cli`, with zero third-party dependencies for instant compilation, high reliability, and trivial distribution alongside `a2a`.
2. **Cards Directory Default:** Defaults to `./agents` in the current working directory (or `--cards-dir`), automatically creating the directory if needed.
3. **Sessions Store Default:** Defaults to `~/.config/a2a-select/sessions.json` (or `--sessions-file`, overridable via `A2A_SELECT_SESSIONS_FILE`).
4. **Security Hardening:**
   - **Command Execution:** `internal/a2acli` executes only the validated `a2a` binary via `exec.CommandContext` with explicit argument slices (never shell string interpolation `sh -c`).
   - **Path Traversal Protection:** `internal/cards` sanitizes all card filenames using `filepath.Base` and a strict alphanumeric/hyphen slug allow-list, verifying that resolved file paths remain strictly inside the configured cards directory boundary (`strings.HasPrefix(resolvedPath, cleanDir + string( filepath.Separator ))`).
   - **Network/SSRF & Response Limits:** `internal/ards` validates URLs (`http`/`https` only) and bounds HTTP response bodies with `io.LimitReader` (4 MiB max).
   - **File Permissions:** Session files and saved agent cards are written with `0o600` permissions (`0o700` for directories).
