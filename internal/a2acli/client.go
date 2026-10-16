package a2acli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// AgentInterface represents a transport and URL pair from an A2A AgentCard.
type AgentInterface struct {
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	URL             string `json:"url"`
}

// AgentCapabilities represents declared capabilities on an AgentCard.
type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications,omitempty"`
}

// AgentSkill represents a skill entry on an AgentCard.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// AgentProvider represents provider metadata on an AgentCard.
type AgentProvider struct {
	Organization string `json:"organization,omitempty"`
	URL          string `json:"url,omitempty"`
}

// AgentCard represents a parsed A2A Agent Card (supporting both v1.0 and v0.3 fields).
type AgentCard struct {
	Name                string            `json:"name"`
	Description         string            `json:"description,omitempty"`
	Version             string            `json:"version"`
	URL                 string            `json:"url,omitempty"`
	SupportedInterfaces []AgentInterface  `json:"supportedInterfaces,omitempty"`
	Capabilities        AgentCapabilities `json:"capabilities"`
	Skills              []AgentSkill      `json:"skills,omitempty"`
	Provider            *AgentProvider    `json:"provider,omitempty"`
}

// PrimaryURL returns the primary endpoint URL for verifying or contacting the agent.
func (c AgentCard) PrimaryURL() string {
	for _, iface := range c.SupportedInterfaces {
		if strings.TrimSpace(iface.URL) != "" {
			return strings.TrimSpace(iface.URL)
		}
	}
	return strings.TrimSpace(c.URL)
}

// ParseAgentCard parses raw JSON bytes into an AgentCard.
func ParseAgentCard(raw []byte) (*AgentCard, error) {
	var card AgentCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("invalid agent card JSON: %w", err)
	}
	if strings.TrimSpace(card.Name) == "" {
		return nil, errors.New("agent card is missing required 'name' field")
	}
	return &card, nil
}

// ArtifactView holds a simplified view of an artifact returned by an agent.
type ArtifactView struct {
	ID   string
	Name string
	Text string
}

// SendResult holds the parsed result of `a2a send -o json`.
type SendResult struct {
	TaskID    string
	ContextID string
	State     string
	ReplyText string
	Artifacts []ArtifactView
}

// SendRequest specifies parameters for sending a message via `a2a send`.
type SendRequest struct {
	CardRef   string
	ContextID string
	TaskID    string
	Message   string
	Auth      string
}

// TaskSummary represents a single task entry returned by `a2a task list -o json`.
type TaskSummary struct {
	ID        string
	ContextID string
	State     string
}

// Runner abstracts operations executed against the `a2a` CLI.
type Runner interface {
	GetCard(ctx context.Context, ref string) (*AgentCard, []byte, error)
	Send(ctx context.Context, req SendRequest) (*SendResult, error)
	ListTasks(ctx context.Context, cardRef, contextID string) ([]TaskSummary, error)
}

// CLIRunner invokes the `a2a` CLI binary as a subprocess.
type CLIRunner struct {
	binaryPath string
}

// NewCLIRunner validates and resolves the `a2a` binary path.
func NewCLIRunner(bin string) (*CLIRunner, error) {
	trimmed := strings.TrimSpace(bin)
	if trimmed == "" {
		trimmed = "a2a"
	}
	if strings.ContainsAny(trimmed, ";|&$`<> \t\n") {
		return nil, fmt.Errorf("invalid characters in a2a binary path %q", trimmed)
	}
	resolved, err := exec.LookPath(trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolving a2a binary %q: %w", trimmed, err)
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute binary path: %w", err)
	}
	return &CLIRunner{binaryPath: filepath.Clean(absPath)}, nil
}

// GetCard fetches an agent card via `a2a card get <ref> -o json`.
func (r *CLIRunner) GetCard(ctx context.Context, ref string) (*AgentCard, []byte, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil, errors.New("card reference is required")
	}
	out, err := r.exec(ctx, "card", "get", ref, "-o", "json")
	if err != nil {
		return nil, nil, err
	}
	card, err := ParseAgentCard(out)
	if err != nil {
		return nil, nil, err
	}
	// Normalize indentation for clean local storage.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, bytes.TrimSpace(out), "", "  "); err == nil {
		pretty.WriteByte('\n')
		out = pretty.Bytes()
	}
	return card, out, nil
}

// Send sends a message via `a2a send` and parses the JSON output.
func (r *CLIRunner) Send(ctx context.Context, req SendRequest) (*SendResult, error) {
	if strings.TrimSpace(req.CardRef) == "" {
		return nil, errors.New("card reference is required")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, errors.New("message text is required")
	}
	args := []string{"send", "-a", req.CardRef, "-o", "json"}
	if strings.TrimSpace(req.Auth) != "" {
		args = append(args, "--auth", req.Auth)
	}
	if strings.TrimSpace(req.ContextID) != "" {
		args = append(args, "--context-id", req.ContextID)
	}
	if strings.TrimSpace(req.TaskID) != "" {
		args = append(args, "--task-id", req.TaskID)
	}
	args = append(args, "--text-part", req.Message)

	out, err := r.exec(ctx, args...)
	if err != nil {
		return nil, err
	}
	return ParseSendOutput(out)
}

// ListTasks lists tasks via `a2a task list -a <cardRef> [--context <contextID>] -o json`.
func (r *CLIRunner) ListTasks(ctx context.Context, cardRef, contextID string) ([]TaskSummary, error) {
	args := []string{"task", "list", "-a", cardRef, "-o", "json"}
	if strings.TrimSpace(contextID) != "" {
		args = append(args, "--context", contextID)
	}
	out, err := r.exec(ctx, args...)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tasks []rawTask `json:"tasks"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parsing task list output: %w", err)
	}
	summaries := make([]TaskSummary, 0, len(payload.Tasks))
	for _, t := range payload.Tasks {
		summaries = append(summaries, TaskSummary{
			ID:        t.ID,
			ContextID: t.ContextID,
			State:     NormalizeState(t.Status.State),
		})
	}
	return summaries, nil
}

func (r *CLIRunner) exec(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := extractCLIError(stdout.Bytes(), stderr.Bytes()); msg != "" {
			return nil, fmt.Errorf("a2a %s failed: %s", args[0], msg)
		}
		return nil, fmt.Errorf("a2a %s failed: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

func extractCLIError(stdout, stderr []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(stdout) > 0 && json.Unmarshal(stdout, &env) == nil && strings.TrimSpace(env.Error.Message) != "" {
		return strings.TrimSpace(env.Error.Message)
	}
	if errMsg := strings.TrimSpace(string(stderr)); errMsg != "" {
		return errMsg
	}
	return strings.TrimSpace(string(stdout))
}

type rawPart struct {
	Text      string         `json:"text,omitempty"`
	URL       string         `json:"url,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

type rawMessage struct {
	MessageID string    `json:"messageId"`
	TaskID    string    `json:"taskId"`
	ContextID string    `json:"contextId"`
	Role      string    `json:"role"`
	Parts     []rawPart `json:"parts"`
}

type rawArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Name       string    `json:"name"`
	Parts      []rawPart `json:"parts"`
}

type rawTask struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	Status    struct {
		State   string      `json:"state"`
		Message *rawMessage `json:"message,omitempty"`
	} `json:"status"`
	Artifacts []rawArtifact `json:"artifacts,omitempty"`
	History   []rawMessage  `json:"history,omitempty"`
}

type rawStreamEnvelope struct {
	Task    *rawTask    `json:"task,omitempty"`
	Message *rawMessage `json:"message,omitempty"`
	// Support direct un-wrapped Task objects if returned directly.
	ID        string `json:"id,omitempty"`
	ContextID string `json:"contextId,omitempty"`
	Status    struct {
		State   string      `json:"state"`
		Message *rawMessage `json:"message,omitempty"`
	} `json:"status,omitempty"`
	Artifacts []rawArtifact `json:"artifacts,omitempty"`
}

// ParseSendOutput decodes JSON emitted by `a2a send -o json`.
func ParseSendOutput(data []byte) (*SendResult, error) {
	var env rawStreamEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decoding a2a send response: %w", err)
	}

	if env.Task != nil {
		return sendResultFromTask(env.Task), nil
	}
	if env.Message != nil {
		return &SendResult{
			TaskID:    env.Message.TaskID,
			ContextID: env.Message.ContextID,
			State:     "completed",
			ReplyText: extractPartsText(env.Message.Parts),
		}, nil
	}
	if env.ID != "" || env.ContextID != "" {
		t := &rawTask{
			ID:        env.ID,
			ContextID: env.ContextID,
			Status:    env.Status,
			Artifacts: env.Artifacts,
		}
		return sendResultFromTask(t), nil
	}
	return nil, errors.New("unrecognized a2a send JSON response")
}

func sendResultFromTask(t *rawTask) *SendResult {
	var replyParts []string
	if t.Status.Message != nil {
		if msgText := extractPartsText(t.Status.Message.Parts); msgText != "" {
			replyParts = append(replyParts, msgText)
		}
	}
	var artifacts []ArtifactView
	for _, art := range t.Artifacts {
		artText := extractPartsText(art.Parts)
		artifacts = append(artifacts, ArtifactView{
			ID:   art.ArtifactID,
			Name: art.Name,
			Text: artText,
		})
		if artText != "" {
			replyParts = append(replyParts, artText)
		}
	}
	if len(replyParts) == 0 && len(t.History) > 0 {
		for i := len(t.History) - 1; i >= 0; i-- {
			role := strings.ToUpper(t.History[i].Role)
			if strings.Contains(role, "AGENT") {
				if txt := extractPartsText(t.History[i].Parts); txt != "" {
					replyParts = append(replyParts, txt)
					break
				}
			}
		}
	}
	return &SendResult{
		TaskID:    t.ID,
		ContextID: t.ContextID,
		State:     NormalizeState(t.Status.State),
		ReplyText: strings.Join(replyParts, "\n"),
		Artifacts: artifacts,
	}
}

func extractPartsText(parts []rawPart) string {
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
			continue
		}
		if p.URL != "" {
			label := p.Filename
			if label == "" {
				label = p.URL
			}
			out = append(out, fmt.Sprintf("[file: %s (%s)]", label, p.URL))
			continue
		}
		if len(p.Data) > 0 {
			if b, err := json.Marshal(p.Data); err == nil {
				out = append(out, string(b))
			}
		}
	}
	return strings.Join(out, " ")
}

// NormalizeState converts protocol task states (e.g. TASK_STATE_INPUT_REQUIRED)
// into human-readable kebab-case names matching `a2a` CLI conventions.
func NormalizeState(state string) string {
	s := strings.TrimSpace(state)
	upper := strings.ToUpper(s)
	upper = strings.TrimPrefix(upper, "TASK_STATE_")
	switch upper {
	case "SUBMITTED":
		return "submitted"
	case "WORKING":
		return "working"
	case "COMPLETED":
		return "completed"
	case "FAILED":
		return "failed"
	case "CANCELED", "CANCELLED":
		return "canceled"
	case "REJECTED":
		return "rejected"
	case "INPUT_REQUIRED", "INPUT-REQUIRED":
		return "input-required"
	case "AUTH_REQUIRED", "AUTH-REQUIRED":
		return "auth-required"
	default:
		if s == "" {
			return "unknown"
		}
		return strings.ToLower(s)
	}
}
