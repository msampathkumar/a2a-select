package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
	"github.com/msampathkumar/a2a-select/internal/ards"
	"github.com/msampathkumar/a2a-select/internal/cards"
	"github.com/msampathkumar/a2a-select/internal/session"
)

// Options configures initial parameters when launching App.Run.
type Options struct {
	CardPath  string
	ContextID string
	TaskID    string
	Auth      string
	ARDSURL   string
}

// App coordinates the interactive agent card selector, ARDS discovery, verification, and chat loop.
type App struct {
	In           io.Reader
	Out          io.Writer
	Runner       a2acli.Runner
	CardStore    *cards.Store
	SessionStore *session.Store
	ARDSClient   *ards.Client
	scanner      *bufio.Scanner
}

// Run executes the interactive CLI workflow.
func (a *App) Run(ctx context.Context, opts Options) error {
	a.scanner = bufio.NewScanner(a.In)
	// Allow long user messages in interactive chat.
	a.scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	a.printBanner()

	var selectedCard cards.StoredCard
	var err error

	if strings.TrimSpace(opts.CardPath) != "" {
		selectedCard, err = cards.LoadFile(opts.CardPath)
		if err != nil {
			return fmt.Errorf("loading specified card %q: %w", opts.CardPath, err)
		}
	} else {
		var ok bool
		selectedCard, ok, err = a.selectOrDiscoverCard(ctx, opts)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	// Verify whether the selected local card is still valid and up to date.
	verifiedCard, proceed, err := a.verifyCard(ctx, selectedCard)
	if err != nil {
		return err
	}
	if !proceed {
		return nil
	}

	// Choose a conversation (either from explicit --context-id, past history list, or new).
	conv, err := a.selectConversation(verifiedCard, opts)
	if err != nil {
		return err
	}

	for {
		if err := a.chatLoop(ctx, verifiedCard, conv, opts.Auth); err != nil {
			return err
		}

		if strings.TrimSpace(conv.ContextID) == "" {
			a.printf("\nSession ended (no messages exchanged).\n")
			return nil
		}

		restart := a.promptRestart(verifiedCard, conv)
		if !restart {
			a.printResumeInstructions(verifiedCard, conv)
			return nil
		}
		a.printf("\n↻ Resuming conversation (Context ID: %s)...\n", conv.ContextID)
	}
}

func (a *App) selectOrDiscoverCard(ctx context.Context, opts Options) (cards.StoredCard, bool, error) {
	for {
		localCards, err := a.CardStore.List()
		if err != nil {
			return cards.StoredCard{}, false, err
		}

		a.printf("\n=== Local Saved Agent Cards (%s) ===\n", a.CardStore.Dir)
		if len(localCards) == 0 {
			a.printf("  (No local agent cards saved yet)\n")
		} else {
			for i, item := range localCards {
				endpoint := item.Card.PrimaryURL()
				if endpoint == "" {
					endpoint = "no-url"
				}
				a.printf("  [%d] %-28s v%-8s  %s  (%s)\n",
					i+1, item.Card.Name, item.Card.Version, endpoint, item.Filename)
			}
		}

		a.printf("\nActions:\n")
		if len(localCards) > 0 {
			a.printf("  [1-%d] Select a local Agent Card\n", len(localCards))
		}
		a.printf("  [A]   Search ARDS Registry (skills, tools, APIs, or A2A agents)\n")
		a.printf("  [F]   Fetch & save an Agent Card from URL\n")
		a.printf("  [Q]   Quit\n")

		choice, ok := a.prompt("\nSelect an option: ")
		if !ok {
			return cards.StoredCard{}, false, nil
		}
		lower := strings.ToLower(strings.TrimSpace(choice))
		switch lower {
		case "q", "quit", "exit":
			a.printf("Goodbye!\n")
			return cards.StoredCard{}, false, nil
		case "a", "ards":
			card, found, err := a.browseARDS(ctx, opts.ARDSURL)
			if err != nil {
				a.printf("  ! ARDS error: %v\n", err)
				continue
			}
			if found {
				return card, true, nil
			}
		case "f", "fetch":
			card, found, err := a.fetchCardFromURL(ctx)
			if err != nil {
				a.printf("  ! Fetch error: %v\n", err)
				continue
			}
			if found {
				return card, true, nil
			}
		default:
			idx, err := strconv.Atoi(lower)
			if err != nil || idx < 1 || idx > len(localCards) {
				a.printf("  ! Invalid selection %q. Enter a number or action letter.\n", choice)
				continue
			}
			return localCards[idx-1], true, nil
		}
	}
}

func (a *App) browseARDS(ctx context.Context, defaultARDSURL string) (cards.StoredCard, bool, error) {
	client := a.ARDSClient
	if client == nil {
		urlStr := strings.TrimSpace(defaultARDSURL)
		if urlStr == "" {
			inputURL, ok := a.prompt("ARDS Registry URL [http://127.0.0.1:8000]: ")
			if !ok {
				return cards.StoredCard{}, false, nil
			}
			urlStr = strings.TrimSpace(inputURL)
			if urlStr == "" {
				urlStr = "http://127.0.0.1:8000"
			}
		}
		var err error
		client, err = ards.NewClient(urlStr)
		if err != nil {
			return cards.StoredCard{}, false, err
		}
	}

	typeFilter, ok := a.prompt("Filter by type (all / agent / skill / mcp / plugin) [all]: ")
	if !ok {
		return cards.StoredCard{}, false, nil
	}
	if strings.TrimSpace(typeFilter) == "" {
		typeFilter = "all"
	}

	query, ok := a.prompt("Search query (press Enter to list all): ")
	if !ok {
		return cards.StoredCard{}, false, nil
	}

	resp, err := client.Search(ctx, query, typeFilter)
	if err != nil {
		return cards.StoredCard{}, false, err
	}

	if len(resp.Results) == 0 {
		a.printf("  No results found in ARDS registry for query %q (type=%s).\n", query, typeFilter)
		return cards.StoredCard{}, false, nil
	}

	a.printf("\n=== ARDS Registry Results (%d found in %.2f ms) ===\n", resp.TotalResults, resp.ExecutionTimeMs)
	for i, res := range resp.Results {
		caps := strings.Join(res.Capabilities, ", ")
		if caps == "" {
			caps = "-"
		}
		a.printf("  [%d] [%-6s] %-28s (%s) — %s [capabilities: %s]\n",
			i+1, strings.ToUpper(res.Type), res.Name, res.HealthStatus, res.Endpoint, caps)
	}

	selStr, ok := a.prompt(fmt.Sprintf("\nSelect an item [1-%d] (or B to go back): ", len(resp.Results)))
	if !ok || strings.EqualFold(strings.TrimSpace(selStr), "b") || strings.TrimSpace(selStr) == "" {
		return cards.StoredCard{}, false, nil
	}
	idx, err := strconv.Atoi(strings.TrimSpace(selStr))
	if err != nil || idx < 1 || idx > len(resp.Results) {
		return cards.StoredCard{}, false, fmt.Errorf("invalid selection %q", selStr)
	}

	chosen := resp.Results[idx-1]
	a.printf("\n--- Selected ARDS Resource ---\n")
	a.printf("  ID:           %s\n", chosen.ID)
	a.printf("  Type:         %s\n", chosen.Type)
	a.printf("  Name:         %s (v%s)\n", chosen.Name, chosen.Version)
	a.printf("  Description:  %s\n", chosen.Description)
	a.printf("  Endpoint:     %s\n", chosen.Endpoint)
	if chosen.Location != "" {
		a.printf("  Location:     %s\n", chosen.Location)
	}
	if len(chosen.Capabilities) > 0 {
		a.printf("  Capabilities: %s\n", strings.Join(chosen.Capabilities, ", "))
	}
	if len(chosen.Tags) > 0 {
		a.printf("  Tags:         %s\n", strings.Join(chosen.Tags, ", "))
	}

	if !chosen.IsAgent() {
		a.printf("  Note: %q is a %s resource (not an A2A agent). Returning to menu.\n", chosen.Name, chosen.Type)
		return cards.StoredCard{}, false, nil
	}

	a.printf("\nFetching A2A Agent Card from %s ...\n", chosen.Endpoint)
	card, raw, err := a.Runner.GetCard(ctx, chosen.Endpoint)
	if err != nil {
		return cards.StoredCard{}, false, fmt.Errorf("fetching A2A card from %s: %w", chosen.Endpoint, err)
	}
	stored, err := a.CardStore.Save(card, raw)
	if err != nil {
		return cards.StoredCard{}, false, fmt.Errorf("saving fetched card locally: %w", err)
	}
	a.printf("✓ Saved Agent Card locally to %s\n", stored.Path)
	return stored, true, nil
}

func (a *App) fetchCardFromURL(ctx context.Context) (cards.StoredCard, bool, error) {
	urlStr, ok := a.prompt("Enter Agent URL or Card URL: ")
	if !ok || strings.TrimSpace(urlStr) == "" {
		return cards.StoredCard{}, false, nil
	}
	a.printf("Fetching A2A Agent Card from %s ...\n", strings.TrimSpace(urlStr))
	card, raw, err := a.Runner.GetCard(ctx, strings.TrimSpace(urlStr))
	if err != nil {
		return cards.StoredCard{}, false, err
	}
	stored, err := a.CardStore.Save(card, raw)
	if err != nil {
		return cards.StoredCard{}, false, err
	}
	a.printf("✓ Saved Agent Card locally to %s\n", stored.Path)
	return stored, true, nil
}

func (a *App) verifyCard(ctx context.Context, item cards.StoredCard) (cards.StoredCard, bool, error) {
	a.printf("\n--- Verifying Agent Card: %s ---\n", item.Card.Name)
	a.printf("  Local File:  %s\n", item.Path)
	a.printf("  Endpoint:    %s\n", item.Card.PrimaryURL())

	res, err := a.CardStore.VerifyAndUpdate(ctx, item, a.Runner)
	if err != nil {
		return item, false, err
	}

	switch res.Status {
	case cards.StatusUpToDate:
		a.printf("  Status:      ✓ %s\n", res.Message)
		return res.Card, true, nil
	case cards.StatusUpdated:
		a.printf("  Status:      ↻ %s\n", res.Message)
		return res.Card, true, nil
	case cards.StatusUnreachable:
		a.printf("  Status:      ⚠ %s\n", res.Message)
		ans, ok := a.prompt("Continue with cached local card anyway? [Y/n]: ")
		if !ok {
			return res.Card, false, nil
		}
		trimmed := strings.ToLower(strings.TrimSpace(ans))
		if trimmed == "n" || trimmed == "no" {
			return res.Card, false, nil
		}
		return res.Card, true, nil
	default:
		return res.Card, true, nil
	}
}

func (a *App) selectConversation(card cards.StoredCard, opts Options) (*session.Conversation, error) {
	// If the user passed --context-id on the command line, resume or initialize that context directly.
	if strings.TrimSpace(opts.ContextID) != "" {
		ctxID := strings.TrimSpace(opts.ContextID)
		existing, found, err := a.SessionStore.Get(ctxID)
		if err != nil {
			return nil, err
		}
		if found {
			existing.CardPath = card.Path
			if strings.TrimSpace(opts.TaskID) != "" {
				existing.LastTaskID = strings.TrimSpace(opts.TaskID)
			}
			a.printf("\n✓ Resumed saved conversation for Context ID: %s (%d previous turns)\n", existing.ContextID, len(existing.Turns))
			return existing, nil
		}
		conv := session.NewConversation(card.Card.Name, card.Path)
		conv.ContextID = ctxID
		if strings.TrimSpace(opts.TaskID) != "" {
			conv.LastTaskID = strings.TrimSpace(opts.TaskID)
			conv.LastTaskState = "input-required"
		}
		a.printf("\n✓ Attached to Context ID: %s\n", conv.ContextID)
		return conv, nil
	}

	past, err := a.SessionStore.ListForAgent(card.Card.Name, card.Path)
	if err != nil {
		return nil, err
	}
	if len(past) == 0 {
		a.printf("\nStarting a new conversation with %s...\n", card.Card.Name)
		return session.NewConversation(card.Card.Name, card.Path), nil
	}

	a.printf("\n=== Conversations for %s ===\n", card.Card.Name)
	a.printf("  [N] Start a new conversation\n")
	for i, c := range past {
		ts := c.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		a.printf("  [%d] Context: %-24s | %s | State: %-14s | Task: %-14s | %q\n",
			i+1, c.ContextID, ts, c.LastTaskState, c.LastTaskID, c.LastPreview())
	}

	for {
		choice, ok := a.prompt(fmt.Sprintf("\nSelect conversation [N or 1-%d] (default N): ", len(past)))
		if !ok {
			return session.NewConversation(card.Card.Name, card.Path), nil
		}
		trimmed := strings.ToLower(strings.TrimSpace(choice))
		if trimmed == "" || trimmed == "n" || trimmed == "new" || trimmed == "0" {
			a.printf("Starting a new conversation with %s...\n", card.Card.Name)
			return session.NewConversation(card.Card.Name, card.Path), nil
		}
		idx, err := strconv.Atoi(trimmed)
		if err != nil || idx < 1 || idx > len(past) {
			a.printf("  ! Invalid choice %q. Enter N for a new conversation or 1-%d.\n", choice, len(past))
			continue
		}
		chosen := past[idx-1]
		chosen.CardPath = card.Path
		return &chosen, nil
	}
}

func (a *App) chatLoop(ctx context.Context, card cards.StoredCard, conv *session.Conversation, auth string) error {
	a.printf("\n====================================================================\n")
	a.printf("Chat Session with %s (v%s)\n", card.Card.Name, card.Card.Version)
	if card.Card.Description != "" {
		a.printf("Description: %s\n", card.Card.Description)
	}
	if conv.ContextID != "" {
		a.printf("Active Context ID: %s (Last Task: %s, Status: %s)\n",
			conv.ContextID, emptyDash(conv.LastTaskID), emptyDash(conv.LastTaskState))
	} else {
		a.printf("Active Context ID: (new — will be assigned on first message)\n")
	}
	a.printf("Commands: /exit (or /quit), /status, /tasks, /new, /help\n")
	a.printf("====================================================================\n")

	if len(conv.Turns) > 0 {
		a.printf("\n--- Conversation History (%d turns) ---\n", len(conv.Turns))
		for _, turn := range conv.Turns {
			a.printf("  [%s] You:   %s\n", turn.Timestamp.UTC().Format("15:04:05"), turn.UserMessage)
			a.printf("  [%s] Agent: %s  (Task: %s | State: %s | Context: %s)\n",
				turn.Timestamp.UTC().Format("15:04:05"), turn.AgentReply, turn.TaskID, turn.TaskState, turn.ContextID)
		}
		a.printf("---------------------------------------\n")
	}

	for {
		line, ok := a.prompt("\nYou > ")
		if !ok {
			a.printf("\n")
			return nil
		}
		msg := strings.TrimSpace(line)
		if msg == "" {
			continue
		}

		var files []string
		if strings.HasPrefix(msg, "/file ") || strings.HasPrefix(msg, "/attach ") {
			parts := strings.Fields(msg)
			if len(parts) < 2 {
				a.printf("  ! Usage: /file <path> [message]\n")
				continue
			}
			files = append(files, parts[1])
			msg = strings.TrimSpace(strings.Join(parts[2:], " "))
			if msg == "" {
				msg = "Attached file: " + filepath.Base(parts[1])
			}
		} else if strings.HasPrefix(msg, "/") {
			done, err := a.handleSlashCommand(ctx, msg, card, conv)
			if err != nil {
				a.printf("  ! Error: %v\n", err)
			}
			if done {
				return nil
			}
			continue
		}

		ctxID, taskID := conv.NextSendParams()
		if taskID != "" {
			a.printf("  ↳ Continuing active task %s in context %s ...\n", taskID, ctxID)
		} else if ctxID != "" {
			a.printf("  ↳ Starting new task in existing context %s (previous task %s reached %s) ...\n",
				ctxID, emptyDash(conv.LastTaskID), emptyDash(conv.LastTaskState))
		}

		res, err := a.Runner.Send(ctx, a2acli.SendRequest{
			CardRef:   card.Path,
			ContextID: ctxID,
			TaskID:    taskID,
			Message:   msg,
			Files:     files,
			Auth:      auth,
		})
		if err != nil {
			a.printf("  ! Failed to send message: %v\n", err)
			continue
		}

		conv.RecordTurn(msg, res, time.Now().UTC())
		if err := a.SessionStore.Upsert(*conv); err != nil {
			a.printf("  ! Warning: failed to persist session history: %v\n", err)
		}

		reply := strings.TrimSpace(res.ReplyText)
		if reply == "" {
			reply = "(no text content returned)"
		}
		a.printf("\nAgent (%s):\n%s\n", card.Card.Name, reply)
		a.printf("  ┌─ Turn Details ─────────────────────────────────────────────\n")
		a.printf("  │ Task ID:     %s\n", emptyDash(res.TaskID))
		a.printf("  │ Task Status: %s\n", emptyDash(res.State))
		a.printf("  │ Context ID:  %s\n", emptyDash(res.ContextID))
		a.printf("  └────────────────────────────────────────────────────────────\n")
	}
}

func (a *App) handleSlashCommand(ctx context.Context, cmd string, card cards.StoredCard, conv *session.Conversation) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "/exit", "/quit", "/q":
		return true, nil
	case "/help", "/h", "/?":
		a.printf("  Available commands:\n")
		a.printf("    /file <path> [msg]  Attach a file part to your message turn\n")
		a.printf("    /status             Show current agent card, Context ID, and Task ID state\n")
		a.printf("    /tasks              Query the agent for all tasks in the current Context ID\n")
		a.printf("    /new                Start a fresh conversation context with %s\n", card.Card.Name)
		a.printf("    /exit               Exit the session and print continuation commands\n")
		return false, nil
	case "/status", "/info":
		a.printf("  Agent:          %s (v%s)\n", card.Card.Name, card.Card.Version)
		a.printf("  Card File:      %s\n", card.Path)
		a.printf("  Endpoint:       %s\n", card.Card.PrimaryURL())
		a.printf("  Context ID:     %s\n", emptyDash(conv.ContextID))
		a.printf("  Last Task ID:   %s\n", emptyDash(conv.LastTaskID))
		a.printf("  Last Status:    %s\n", emptyDash(conv.LastTaskState))
		a.printf("  Turns Recorded: %d\n", len(conv.Turns))
		return false, nil
	case "/tasks":
		if len(conv.Turns) > 0 {
			a.printf("  Session Tasks in Context %s:\n", emptyDash(conv.ContextID))
			seen := map[string]string{}
			var order []string
			for _, turn := range conv.Turns {
				if turn.TaskID == "" {
					continue
				}
				if _, ok := seen[turn.TaskID]; !ok {
					order = append(order, turn.TaskID)
				}
				seen[turn.TaskID] = turn.TaskState
			}
			for _, id := range order {
				a.printf("    - Task: %-28s  Status: %-15s  Context: %s\n", id, seen[id], emptyDash(conv.ContextID))
			}
		}
		tasks, err := a.Runner.ListTasks(ctx, card.Path, conv.ContextID)
		if err != nil {
			if len(conv.Turns) == 0 {
				return false, err
			}
			return false, nil
		}
		if len(tasks) > 0 {
			a.printf("  Remote Tasks Reported by Agent:\n")
			for _, t := range tasks {
				a.printf("    - Task: %-28s  Status: %-15s  Context: %s\n", t.ID, t.State, t.ContextID)
			}
		} else if len(conv.Turns) == 0 {
			a.printf("  No tasks reported by agent for Context ID %s.\n", emptyDash(conv.ContextID))
		}
		return false, nil
	case "/new":
		*conv = *session.NewConversation(card.Card.Name, card.Path)
		a.printf("  ✓ Started a new conversation context with %s.\n", card.Card.Name)
		return false, nil
	default:
		return false, errors.New("unknown command; type /help for available commands")
	}
}

func (a *App) promptRestart(card cards.StoredCard, conv *session.Conversation) bool {
	a.printf("\n--- Session Paused ---\n")
	a.printf("  Agent:        %s\n", card.Card.Name)
	a.printf("  Context ID:   %s\n", emptyDash(conv.ContextID))
	a.printf("  Last Task ID: %s (status: %s)\n", emptyDash(conv.LastTaskID), emptyDash(conv.LastTaskState))
	ans, ok := a.prompt("Would you like to restart/continue this conversation right now? [y/N]: ")
	if !ok {
		return false
	}
	trimmed := strings.ToLower(strings.TrimSpace(ans))
	return trimmed == "y" || trimmed == "yes"
}

func (a *App) printResumeInstructions(card cards.StoredCard, conv *session.Conversation) {
	a.printf("\n=== Continue This Conversation Later ===\n")
	a.printf("1. Resume interactive chat in a2a-select:\n")
	if session.IsInterruptedState(conv.LastTaskState) && conv.LastTaskID != "" {
		a.printf("   a2a-select --card %s --context-id %s --task-id %s\n",
			shellQuote(card.Path), shellQuote(conv.ContextID), shellQuote(conv.LastTaskID))
	} else {
		a.printf("   a2a-select --card %s --context-id %s\n",
			shellQuote(card.Path), shellQuote(conv.ContextID))
	}

	a.printf("\n2. Or send the next turn directly with the a2a CLI:\n")
	if session.IsInterruptedState(conv.LastTaskState) && conv.LastTaskID != "" {
		a.printf("   a2a send -a %s --context-id %s --task-id %s \"<your reply>\"\n",
			shellQuote(card.Path), shellQuote(conv.ContextID), shellQuote(conv.LastTaskID))
	} else {
		a.printf("   a2a send -a %s --context-id %s \"<your follow-up message>\"\n",
			shellQuote(card.Path), shellQuote(conv.ContextID))
	}
}

func (a *App) printBanner() {
	a.printf("╭──────────────────────────────────────────────────────────────╮\n")
	a.printf("│  a2a-select — Interactive Agent Selector & Stateful Chat     │\n")
	a.printf("╰──────────────────────────────────────────────────────────────╯\n")
}

func (a *App) prompt(label string) (string, bool) {
	a.printf("%s", label)
	if !a.scanner.Scan() {
		return "", false
	}
	return a.scanner.Text(), true
}

func (a *App) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Out, format, args...)
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t\"'$") {
		return fmt.Sprintf("%q", s)
	}
	return s
}
