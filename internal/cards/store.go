package cards

import (
	"bytes"
	"context"
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

// VerifyStatus indicates the outcome of checking a local Agent Card against its live endpoint.
type VerifyStatus string

const (
	StatusUpToDate    VerifyStatus = "up-to-date"
	StatusUpdated     VerifyStatus = "updated"
	StatusUnreachable VerifyStatus = "unreachable"
)

// StoredCard represents a saved local Agent Card JSON file.
type StoredCard struct {
	Path     string
	Filename string
	ModTime  time.Time
	Card     a2acli.AgentCard
	RawJSON  []byte
}

// VerifyResult summarizes the result of verifying a local Agent Card.
type VerifyResult struct {
	Status  VerifyStatus
	Updated bool
	Message string
	Card    StoredCard
}

// Store manages local Agent Card JSON files within a directory.
type Store struct {
	Dir string
}

// NewStore creates or opens a local Agent Card directory.
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("cards directory path is required")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving cards directory: %w", err)
	}
	cleanDir := filepath.Clean(absDir)
	if err := os.MkdirAll(cleanDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating cards directory %q: %w", cleanDir, err)
	}
	return &Store{Dir: cleanDir}, nil
}

// List scans the store directory for valid Agent Card JSON files.
func (s *Store) List() ([]StoredCard, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("reading cards directory %q: %w", s.Dir, err)
	}

	var items []StoredCard
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		fullPath, err := s.safeJoin(entry.Name())
		if err != nil {
			continue
		}
		sc, err := LoadFile(fullPath)
		if err != nil {
			continue
		}
		items = append(items, sc)
	}

	sort.Slice(items, func(i, j int) bool {
		if strings.EqualFold(items[i].Card.Name, items[j].Card.Name) {
			return items[i].Filename < items[j].Filename
		}
		return strings.ToLower(items[i].Card.Name) < strings.ToLower(items[j].Card.Name)
	})
	return items, nil
}

// LoadFile loads a single StoredCard from an explicit path.
func LoadFile(path string) (StoredCard, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return StoredCard{}, err
	}
	cleanPath := filepath.Clean(absPath)
	info, err := os.Stat(cleanPath)
	if err != nil {
		return StoredCard{}, err
	}
	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		return StoredCard{}, err
	}
	card, err := a2acli.ParseAgentCard(raw)
	if err != nil {
		return StoredCard{}, err
	}
	return StoredCard{
		Path:     cleanPath,
		Filename: filepath.Base(cleanPath),
		ModTime:  info.ModTime(),
		Card:     *card,
		RawJSON:  raw,
	}, nil
}

// Save writes an AgentCard to the local store directory using a sanitized filename slug.
func (s *Store) Save(card *a2acli.AgentCard, raw []byte) (StoredCard, error) {
	if card == nil {
		return StoredCard{}, errors.New("card is nil")
	}
	filename := slugifyName(card.Name) + ".json"
	targetPath, err := s.safeJoin(filename)
	if err != nil {
		return StoredCard{}, err
	}
	return s.writeCardAt(targetPath, card, raw)
}

func (s *Store) writeCardAt(targetPath string, card *a2acli.AgentCard, raw []byte) (StoredCard, error) {
	for i := range card.SupportedInterfaces {
		if strings.TrimSpace(card.SupportedInterfaces[i].ProtocolVersion) == "" {
			card.SupportedInterfaces[i].ProtocolVersion = "1.0"
		}
	}
	formatted := normalizeAndFormatCardJSON(raw)
	if err := os.WriteFile(targetPath, formatted, 0o600); err != nil {
		return StoredCard{}, fmt.Errorf("writing card file %q: %w", targetPath, err)
	}
	info, err := os.Stat(targetPath)
	var modTime time.Time
	if err == nil {
		modTime = info.ModTime()
	} else {
		modTime = time.Now()
	}
	return StoredCard{
		Path:     targetPath,
		Filename: filepath.Base(targetPath),
		ModTime:  modTime,
		Card:     *card,
		RawJSON:  formatted,
	}, nil
}

// VerifyAndUpdate checks the live endpoint of a StoredCard via `a2a card get`.
// If the remote card has changed, it updates the local file on disk.
// If the remote endpoint cannot be reached, it returns StatusUnreachable and keeps the cached card.
func (s *Store) VerifyAndUpdate(ctx context.Context, item StoredCard, runner a2acli.Runner) (VerifyResult, error) {
	endpoint := item.Card.PrimaryURL()
	if endpoint == "" {
		return VerifyResult{
			Status:  StatusUnreachable,
			Updated: false,
			Message: "card has no endpoint URL to verify against; using local copy",
			Card:    item,
		}, nil
	}

	remoteCard, remoteRaw, err := runner.GetCard(ctx, endpoint)
	if err != nil {
		return VerifyResult{
			Status:  StatusUnreachable,
			Updated: false,
			Message: fmt.Sprintf("live endpoint %s unreachable (%v); using cached local card", endpoint, err),
			Card:    item,
		}, nil
	}

	if cardsEquivalent(item.RawJSON, remoteRaw) {
		return VerifyResult{
			Status:  StatusUpToDate,
			Updated: false,
			Message: fmt.Sprintf("verified online at %s (v%s, up to date)", endpoint, remoteCard.Version),
			Card:    item,
		}, nil
	}

	// Ensure target path is inside s.Dir if managed by store, or update in place if valid.
	targetPath := filepath.Clean(item.Path)
	updatedItem, err := s.writeCardAt(targetPath, remoteCard, remoteRaw)
	if err != nil {
		return VerifyResult{}, err
	}

	msg := fmt.Sprintf("remote card changed (v%s -> v%s); updated local card %s", item.Card.Version, remoteCard.Version, updatedItem.Filename)
	if item.Card.Version == remoteCard.Version {
		msg = fmt.Sprintf("remote card metadata updated at %s; refreshed local card %s", endpoint, updatedItem.Filename)
	}
	return VerifyResult{
		Status:  StatusUpdated,
		Updated: true,
		Message: msg,
		Card:    updatedItem,
	}, nil
}

func (s *Store) safeJoin(filename string) (string, error) {
	base := filepath.Base(filename)
	if base == "." || base == "/" || base == ".." {
		return "", fmt.Errorf("invalid card filename %q", filename)
	}
	joined := filepath.Clean(filepath.Join(s.Dir, base))
	prefix := s.Dir + string(filepath.Separator)
	if !strings.HasPrefix(joined, prefix) {
		return "", fmt.Errorf("path %q escapes store directory %q", joined, s.Dir)
	}
	return joined, nil
}

func slugifyName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "agent-card"
	}
	return slug
}

func normalizeAndFormatCardJSON(raw []byte) []byte {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err == nil {
		if ifaces, ok := doc["supportedInterfaces"].([]any); ok {
			for _, item := range ifaces {
				if m, ok := item.(map[string]any); ok {
					if pv, exists := m["protocolVersion"]; !exists || pv == "" {
						m["protocolVersion"] = "1.0"
					}
				}
			}
		}
		if out, err := json.MarshalIndent(doc, "", "  "); err == nil {
			return append(out, '\n')
		}
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, bytes.TrimSpace(raw), "", "  "); err == nil {
		pretty.WriteByte('\n')
		return pretty.Bytes()
	}
	return raw
}

func cardsEquivalent(a, b []byte) bool {
	a = normalizeAndFormatCardJSON(a)
	b = normalizeAndFormatCardJSON(b)
	return bytes.Equal(a, b)
}
