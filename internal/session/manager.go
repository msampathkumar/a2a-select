package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
)

// Turn represents a single user-to-agent exchange within a Conversation.
type Turn struct {
	Timestamp   time.Time `json:"timestamp"`
	UserMessage string    `json:"userMessage"`
	AgentReply  string    `json:"agentReply"`
	TaskID      string    `json:"taskId"`
	ContextID   string    `json:"contextId"`
	TaskState   string    `json:"taskState"`
}

// Conversation tracks an A2A conversation session identified by its ContextID.
type Conversation struct {
	ContextID     string    `json:"contextId"`
	AgentName     string    `json:"agentName"`
	CardPath      string    `json:"cardPath"`
	LastTaskID    string    `json:"lastTaskId"`
	LastTaskState string    `json:"lastTaskState"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Turns         []Turn    `json:"turns"`
}

// NewConversation initializes an empty Conversation for the given agent.
func NewConversation(agentName, cardPath string) *Conversation {
	now := time.Now().UTC()
	return &Conversation{
		AgentName: agentName,
		CardPath:  cardPath,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// IsInterruptedState reports whether a task state is paused awaiting further input or auth,
// meaning the next message in the conversation should continue the same TaskID.
func IsInterruptedState(state string) bool {
	norm := a2acli.NormalizeState(state)
	return norm == "input-required" || norm == "auth-required"
}

// IsTerminalState reports whether a task state is terminal (completed, failed, canceled, rejected),
// meaning the task cannot be restarted and the next turn must start a new task under the same ContextID.
func IsTerminalState(state string) bool {
	norm := a2acli.NormalizeState(state)
	switch norm {
	case "completed", "failed", "canceled", "rejected":
		return true
	default:
		return false
	}
}

// NextSendParams returns the (contextID, taskID) pair to pass to `a2a send` for the next message.
// - On a brand-new conversation, both are "".
// - When the latest task is in an interrupted state (input-required / auth-required), both ContextID and LastTaskID are returned.
// - Once a task is completed (or otherwise terminal), only ContextID is returned and taskID is "" so a new task starts in the same context.
func (c *Conversation) NextSendParams() (contextID, taskID string) {
	if c == nil || strings.TrimSpace(c.ContextID) == "" {
		return "", ""
	}
	if IsInterruptedState(c.LastTaskState) && strings.TrimSpace(c.LastTaskID) != "" {
		return c.ContextID, c.LastTaskID
	}
	return c.ContextID, ""
}

// RecordTurn updates the Conversation state with a completed exchange.
func (c *Conversation) RecordTurn(userMsg string, res *a2acli.SendResult, now time.Time) {
	if res == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if strings.TrimSpace(res.ContextID) != "" {
		c.ContextID = strings.TrimSpace(res.ContextID)
	}
	if strings.TrimSpace(res.TaskID) != "" {
		c.LastTaskID = strings.TrimSpace(res.TaskID)
	}
	c.LastTaskState = a2acli.NormalizeState(res.State)
	c.Turns = append(c.Turns, Turn{
		Timestamp:   now,
		UserMessage: userMsg,
		AgentReply:  res.ReplyText,
		TaskID:      res.TaskID,
		ContextID:   c.ContextID,
		TaskState:   c.LastTaskState,
	})
}

// LastPreview returns a concise single-line preview of the conversation's latest exchange.
func (c Conversation) LastPreview() string {
	if len(c.Turns) == 0 {
		return "(no messages yet)"
	}
	last := c.Turns[len(c.Turns)-1]
	text := strings.TrimSpace(last.UserMessage)
	if text == "" {
		text = strings.TrimSpace(last.AgentReply)
	}
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 48 {
		return text[:45] + "..."
	}
	return text
}

// Store persists conversations to a local JSON file.
type Store struct {
	FilePath string
}

type diskStore struct {
	Conversations []Conversation `json:"conversations"`
}

// NewStore creates a session Store backed by the given file path.
func NewStore(filePath string) (*Store, error) {
	if strings.TrimSpace(filePath) == "" {
		return nil, errors.New("sessions file path is required")
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolving sessions file path: %w", err)
	}
	cleanPath := filepath.Clean(absPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating sessions directory: %w", err)
	}
	return &Store{FilePath: cleanPath}, nil
}

// ListForAgent returns all saved conversations for the given agent name or card path,
// ordered from most recently updated to oldest.
func (s *Store) ListForAgent(agentName, cardPath string) ([]Conversation, error) {
	all, err := s.loadAll()
	if err != nil {
		return nil, err
	}
	cleanCardPath := filepath.Clean(cardPath)
	var matched []Conversation
	for _, conv := range all {
		if (agentName != "" && strings.EqualFold(conv.AgentName, agentName)) ||
			(cardPath != "" && filepath.Clean(conv.CardPath) == cleanCardPath) {
			matched = append(matched, conv)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
	})
	return matched, nil
}

// Get retrieves a Conversation by its ContextID.
func (s *Store) Get(contextID string) (*Conversation, bool, error) {
	contextID = strings.TrimSpace(contextID)
	if contextID == "" {
		return nil, false, nil
	}
	all, err := s.loadAll()
	if err != nil {
		return nil, false, err
	}
	for _, conv := range all {
		if conv.ContextID == contextID {
			copyConv := conv
			return &copyConv, true, nil
		}
	}
	return nil, false, nil
}

// Upsert inserts or updates a Conversation keyed by ContextID.
func (s *Store) Upsert(conv Conversation) error {
	if strings.TrimSpace(conv.ContextID) == "" {
		return nil
	}
	all, err := s.loadAll()
	if err != nil {
		return err
	}
	replaced := false
	for i := range all {
		if all[i].ContextID == conv.ContextID {
			all[i] = conv
			replaced = true
			break
		}
	}
	if !replaced {
		all = append(all, conv)
	}
	return s.saveAll(all)
}

func (s *Store) loadAll() ([]Conversation, error) {
	data, err := os.ReadFile(s.FilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading sessions file: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var ds diskStore
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, fmt.Errorf("parsing sessions file: %w", err)
	}
	return ds.Conversations, nil
}

func (s *Store) saveAll(convs []Conversation) error {
	ds := diskStore{Conversations: convs}
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding sessions file: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(s.FilePath, data, 0o600); err != nil {
		return fmt.Errorf("writing sessions file: %w", err)
	}
	return nil
}
