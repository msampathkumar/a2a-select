package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunMain_PluginInfoSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"info"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(info) exit code = %d, want 0; stderr = %s", code, stderr.String())
	}

	var info pluginInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("json.Unmarshal(info) error = %v; stdout = %s", err, stdout.String())
	}
	if info.Name != "select" || info.Version == "" || info.Description == "" {
		t.Fatalf("plugin info = %+v, want name=select and non-empty version/description", info)
	}
}

func TestRunMain_VersionFlag(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(--version) exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "a2a-select") {
		t.Fatalf("version stdout = %q, want a2a-select", stdout.String())
	}
}
