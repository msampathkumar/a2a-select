package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
	"github.com/msampathkumar/a2a-select/internal/cards"
	"github.com/msampathkumar/a2a-select/internal/session"
	"github.com/msampathkumar/a2a-select/internal/ui"
)

const version = "0.1.0"

// pluginInfo matches the a2a-cli command plugin Info JSON schema.
type pluginInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	// Support the a2a CLI command plugin `info` subcommand (`a2a-select info`).
	if len(args) > 0 && args[0] == "info" {
		enc := json.NewEncoder(out)
		_ = enc.Encode(pluginInfo{
			Name:        "select",
			Version:     version,
			Description: "Interactive agent card selector, ARDS discovery, and stateful A2A chat session",
		})
		return 0
	}

	fs := flag.NewFlagSet("a2a-select", flag.ContinueOnError)
	fs.SetOutput(errOut)

	var (
		cardsDir     = fs.String("cards-dir", defaultCardsDir(), "Directory containing saved local A2A Agent Card JSON files")
		sessionsFile = fs.String("sessions-file", defaultSessionsFile(), "File path for storing conversation history and context IDs")
		cardPath     = fs.String("card", "", "Path to a specific local Agent Card JSON file to open directly")
		contextID    = fs.String("context-id", "", "Resume a specific conversation Context ID")
		taskID       = fs.String("task-id", "", "Continue a specific interrupted Task ID")
		ardsURL      = fs.String("ards-url", defaultARDSURL(), "Default ARDS Registry base URL")
		a2aBin       = fs.String("a2a-bin", defaultA2ABin(), "Path or name of the a2a CLI binary")
		auth         = fs.String("auth", os.Getenv("A2ACLI_AUTH"), "Authorization credential passed to a2a commands")
		showVersion  = fs.Bool("version", false, "Print version information and exit")
	)

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		_, _ = fmt.Fprintf(out, "a2a-select %s\n", version)
		return 0
	}

	runner, err := a2acli.NewCLIRunner(*a2aBin)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}

	cardStore, err := cards.NewStore(*cardsDir)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}

	sessStore, err := session.NewStore(*sessionsFile)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}

	app := &ui.App{
		In:           in,
		Out:          out,
		Runner:       runner,
		CardStore:    cardStore,
		SessionStore: sessStore,
	}

	err = app.Run(ctx, ui.Options{
		CardPath:  *cardPath,
		ContextID: *contextID,
		TaskID:    *taskID,
		Auth:      *auth,
		ARDSURL:   *ardsURL,
	})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	return 0
}

func defaultCardsDir() string {
	if v := strings.TrimSpace(os.Getenv("A2A_SELECT_CARDS_DIR")); v != "" {
		return v
	}
	return "./agents"
}

func defaultSessionsFile() string {
	if v := strings.TrimSpace(os.Getenv("A2A_SELECT_SESSIONS_FILE")); v != "" {
		return v
	}
	if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
		return filepath.Join(cfgDir, "a2a-select", "sessions.json")
	}
	return "./.a2a-select-sessions.json"
}

func defaultARDSURL() string {
	if v := strings.TrimSpace(os.Getenv("ARDS_URL")); v != "" {
		return v
	}
	return "http://127.0.0.1:8000"
}

func defaultA2ABin() string {
	if v := strings.TrimSpace(os.Getenv("A2A_BIN")); v != "" {
		return v
	}
	return "a2a"
}
