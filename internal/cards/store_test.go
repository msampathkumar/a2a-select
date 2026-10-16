package cards_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msampathkumar/a2a-select/internal/a2acli"
	"github.com/msampathkumar/a2a-select/internal/cards"
)

type fakeRunner struct {
	getCardFn func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error)
}

func (f *fakeRunner) GetCard(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
	return f.getCardFn(ctx, ref)
}

func (f *fakeRunner) Send(ctx context.Context, req a2acli.SendRequest) (*a2acli.SendResult, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRunner) ListTasks(ctx context.Context, cardRef, contextID string) ([]a2acli.TaskSummary, error) {
	return nil, errors.New("not implemented")
}

func TestStore_SaveAndList(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "agents")
	store, err := cards.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	raw := []byte(`{
  "name": "Kyoto Analytics Agent",
  "description": "Warehouse analytics",
  "version": "1.0.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8090"}]
}`)
	card, err := a2acli.ParseAgentCard(raw)
	if err != nil {
		t.Fatalf("ParseAgentCard() error = %v", err)
	}

	saved, err := store.Save(card, raw)
	if err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}
	if saved.Filename != "kyoto-analytics-agent.json" {
		t.Fatalf("saved.Filename = %q, want %q", saved.Filename, "kyoto-analytics-agent.json")
	}

	items, err := store.List()
	if err != nil {
		t.Fatalf("store.List() error = %v", err)
	}
	if len(items) != 1 || items[0].Card.Name != "Kyoto Analytics Agent" {
		t.Fatalf("store.List() = %+v, want 1 item", items)
	}
	if len(items[0].Card.SupportedInterfaces) == 0 || items[0].Card.SupportedInterfaces[0].ProtocolVersion != "1.0" {
		t.Fatalf("saved card SupportedInterfaces = %+v, want ProtocolVersion=1.0 defaulted", items[0].Card.SupportedInterfaces)
	}
}

func TestStore_SavePreventsPathTraversal(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "agents")
	store, err := cards.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	raw := []byte(`{
  "name": "../../etc/passwd",
  "version": "1.0.0",
  "url": "http://127.0.0.1:8090"
}`)
	card, err := a2acli.ParseAgentCard(raw)
	if err != nil {
		t.Fatalf("ParseAgentCard() error = %v", err)
	}

	saved, err := store.Save(card, raw)
	if err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}
	cleanDir, _ := filepath.Abs(dir)
	if !strings.HasPrefix(saved.Path, cleanDir+string(filepath.Separator)) {
		t.Fatalf("saved.Path = %q escaped directory %q", saved.Path, cleanDir)
	}
}

func TestStore_VerifyAndUpdate(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "agents")
	store, err := cards.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	v1Raw := []byte(`{
  "name": "Tokyo Router",
  "description": "Initial description",
  "version": "1.0.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8095"}]
}`)
	cardV1, _ := a2acli.ParseAgentCard(v1Raw)
	stored, err := store.Save(cardV1, v1Raw)
	if err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}

	// Case 1: Up to date
	runnerUpToDate := &fakeRunner{
		getCardFn: func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
			return cardV1, v1Raw, nil
		},
	}
	res1, err := store.VerifyAndUpdate(context.Background(), stored, runnerUpToDate)
	if err != nil {
		t.Fatalf("VerifyAndUpdate(up-to-date) error = %v", err)
	}
	if res1.Status != cards.StatusUpToDate || res1.Updated {
		t.Fatalf("VerifyAndUpdate(up-to-date) = %+v, want StatusUpToDate & Updated=false", res1)
	}

	// Case 2: Remote card has a new version -> updates file on disk
	v2Raw := []byte(`{
  "name": "Tokyo Router",
  "description": "Updated description with new skill",
  "version": "1.1.0",
  "supportedInterfaces": [{"protocolBinding": "HTTP+JSON", "url": "http://127.0.0.1:8095"}]
}`)
	cardV2, _ := a2acli.ParseAgentCard(v2Raw)
	runnerUpdated := &fakeRunner{
		getCardFn: func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
			return cardV2, v2Raw, nil
		},
	}
	res2, err := store.VerifyAndUpdate(context.Background(), stored, runnerUpdated)
	if err != nil {
		t.Fatalf("VerifyAndUpdate(updated) error = %v", err)
	}
	if res2.Status != cards.StatusUpdated || !res2.Updated || res2.Card.Card.Version != "1.1.0" {
		t.Fatalf("VerifyAndUpdate(updated) = %+v, want StatusUpdated & Version=1.1.0", res2)
	}
	diskBytes, err := os.ReadFile(stored.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(diskBytes), "1.1.0") {
		t.Fatalf("disk file not updated: %s", string(diskBytes))
	}

	// Case 3: Remote endpoint unreachable -> returns StatusUnreachable without failing hard
	runnerOffline := &fakeRunner{
		getCardFn: func(ctx context.Context, ref string) (*a2acli.AgentCard, []byte, error) {
			return nil, nil, errors.New("connection refused")
		},
	}
	res3, err := store.VerifyAndUpdate(context.Background(), res2.Card, runnerOffline)
	if err != nil {
		t.Fatalf("VerifyAndUpdate(unreachable) unexpected hard error = %v", err)
	}
	if res3.Status != cards.StatusUnreachable || res3.Updated {
		t.Fatalf("VerifyAndUpdate(unreachable) = %+v, want StatusUnreachable", res3)
	}
}
