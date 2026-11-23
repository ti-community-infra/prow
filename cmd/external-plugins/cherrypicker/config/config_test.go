/*
Copyright 2026 The TiCommunity Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDefaults() Defaults {
	return Defaults{
		LabelPrefix:        "needs-cherry-pick-",
		AllowAll:           false,
		UseProwAssignments: true,
		OnConflict:         OnConflictHold,
	}
}

const validConfig = `
cherry-pick:
  - repos:
      - test-org
    allow_all: true
  - repos:
      - test-org/specific
    label_prefix: custom-pick-
    exclude_labels:
      - lgtm
    on_conflict: issue
`

func TestParseStrictRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`
cherry-pick:
  - repos:
      - test-org/repo
    allow_evryone: true
`))
	if err == nil {
		t.Fatal("expected an error for the unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "allow_evryone") {
		t.Fatalf("expected the error to name the unknown field, got %v", err)
	}
}

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(cfg.CherryPick); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid",
			yaml: validConfig,
		},
		{
			name: "empty repos",
			yaml: `
cherry-pick:
  - allow_all: true
`,
			wantErr: "repos",
		},
		{
			name: "duplicate repo",
			yaml: `
cherry-pick:
  - repos:
      - test-org/repo
  - repos:
      - test-org/repo
`,
			wantErr: "duplicate",
		},
		{
			name: "invalid on_conflict",
			yaml: `
cherry-pick:
  - repos:
      - test-org/repo
    on_conflict: retry
`,
			wantErr: "on_conflict",
		},
		{
			name: "invalid repo form",
			yaml: `
cherry-pick:
  - repos:
      - test-org/repo/extra
`,
			wantErr: "repos",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			err = cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestOptions(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defaults := testDefaults()

	tests := []struct {
		name string
		org  string
		repo string
		want Options
	}{
		{
			name: "defaults only",
			org:  "other-org",
			repo: "repo",
			want: Options{
				LabelPrefix:        "needs-cherry-pick-",
				AllowAll:           false,
				UseProwAssignments: true,
				OnConflict:         OnConflictHold,
			},
		},
		{
			name: "org entry",
			org:  "test-org",
			repo: "any",
			want: Options{
				LabelPrefix:        "needs-cherry-pick-",
				AllowAll:           true,
				UseProwAssignments: true,
				OnConflict:         OnConflictHold,
			},
		},
		{
			name: "repo entry overrides org entry field-wise",
			org:  "test-org",
			repo: "specific",
			want: Options{
				LabelPrefix:        "custom-pick-",
				AllowAll:           true,
				UseProwAssignments: true,
				OnConflict:         OnConflictIssue,
				ExcludeLabels:      []string{"lgtm"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.Options(tc.org, tc.repo, defaults)
			if got.LabelPrefix != tc.want.LabelPrefix {
				t.Errorf("label prefix: got %q want %q", got.LabelPrefix, tc.want.LabelPrefix)
			}
			if got.AllowAll != tc.want.AllowAll {
				t.Errorf("allow all: got %v want %v", got.AllowAll, tc.want.AllowAll)
			}
			if got.UseProwAssignments != tc.want.UseProwAssignments {
				t.Errorf("use prow assignments: got %v want %v", got.UseProwAssignments, tc.want.UseProwAssignments)
			}
			if got.OnConflict != tc.want.OnConflict {
				t.Errorf("on conflict: got %q want %q", got.OnConflict, tc.want.OnConflict)
			}
			if strings.Join(got.ExcludeLabels, ",") != strings.Join(tc.want.ExcludeLabels, ",") {
				t.Errorf("exclude labels: got %v want %v", got.ExcludeLabels, tc.want.ExcludeLabels)
			}
		})
	}
}

func TestOptionsExplicitFalseOverridesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
cherry-pick:
  - repos:
      - test-org/repo
    use_prow_assignments: false
    allow_all: false
    labels: []
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.Options("test-org", "repo", Defaults{
		AllowAll:           true,
		UseProwAssignments: true,
		Labels:             []string{"from-flag"},
		OnConflict:         OnConflictHold,
	})
	if got.AllowAll {
		t.Error("expected allow_all=false from config to override the flag default")
	}
	if got.UseProwAssignments {
		t.Error("expected use_prow_assignments=false from config to override the flag default")
	}
	if len(got.Labels) != 0 {
		t.Errorf("expected an explicit empty labels list to override the flag default, got %v", got.Labels)
	}
}

func TestOptionsUnsetKeepsDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
cherry-pick:
  - repos:
      - test-org/repo
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.Options("test-org", "repo", Defaults{
		LabelPrefix:        "flag-prefix-",
		AllowAll:           true,
		UseProwAssignments: false,
		Labels:             []string{"from-flag"},
		OnConflict:         OnConflictFail,
	})
	if got.LabelPrefix != "flag-prefix-" || !got.AllowAll || got.UseProwAssignments ||
		got.OnConflict != OnConflictFail || len(got.Labels) != 1 {
		t.Fatalf("expected unset fields to keep the flag defaults, got %+v", got)
	}
}

func TestRepoEntryOverridesOnlyItsFields(t *testing.T) {
	cfg, err := Parse([]byte(`
cherry-pick:
  - repos:
      - test-org
    allow_all: true
    picked_label_prefix: "type/cherry-pick-for-"
  - repos:
      - test-org/repo
    on_conflict: fail
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.Options("test-org", "repo", testDefaults())
	if !got.AllowAll {
		t.Error("expected allow_all from the org entry to survive the repo entry")
	}
	if got.PickedLabelPrefix != "type/cherry-pick-for-" {
		t.Errorf("expected picked label prefix from the org entry, got %q", got.PickedLabelPrefix)
	}
	if got.OnConflict != OnConflictFail {
		t.Errorf("expected on_conflict from the repo entry, got %q", got.OnConflict)
	}
}

func TestAgentLoadAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "external_plugins_config.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	agent := NewAgent()
	if err := agent.Load(path); err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if agent.Config() == nil {
		t.Fatal("expected a loaded config")
	}
	if got := agent.Config().Options("test-org", "specific", testDefaults()).OnConflict; got != OnConflictIssue {
		t.Fatalf("expected on_conflict=issue after load, got %q", got)
	}

	if err := os.WriteFile(path, []byte(`
cherry-pick:
  - repos:
      - test-org/specific
    on_conflict: fail
`), 0644); err != nil {
		t.Fatalf("failed to rewrite config: %v", err)
	}
	if err := agent.Load(path); err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if got := agent.Config().Options("test-org", "specific", testDefaults()).OnConflict; got != OnConflictFail {
		t.Fatalf("expected on_conflict=fail after reload, got %q", got)
	}
}

func TestAgentKeepsPreviousConfigOnBadReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "external_plugins_config.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	agent := NewAgent()
	if err := agent.Load(path); err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if err := os.WriteFile(path, []byte("cherry-pick:\n  - unknown_field: true\n"), 0644); err != nil {
		t.Fatalf("failed to rewrite config: %v", err)
	}
	if err := agent.Load(path); err == nil {
		t.Fatal("expected a reload error for an invalid file")
	}
	if got := agent.Config().Options("test-org", "specific", testDefaults()).OnConflict; got != OnConflictIssue {
		t.Fatalf("expected the previous config to be kept, got on_conflict=%q", got)
	}
}

func TestAgentStartPolls(t *testing.T) {
	old := reloadInterval
	reloadInterval = 10 * time.Millisecond
	t.Cleanup(func() { reloadInterval = old })

	dir := t.TempDir()
	path := filepath.Join(dir, "external_plugins_config.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	agent := NewAgent()
	if err := agent.Start(path); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	if err := os.WriteFile(path, []byte(`
cherry-pick:
  - repos:
      - test-org/specific
    on_conflict: fail
`), 0644); err != nil {
		t.Fatalf("failed to rewrite config: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if agent.Config().Options("test-org", "specific", testDefaults()).OnConflict == OnConflictFail {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected the polling agent to pick up the rewritten config")
}

func TestValidateFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte(validConfig), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := ValidateFile(good); err != nil {
		t.Fatalf("unexpected error for a valid file: %v", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("cherry-pick:\n  - repos: [org/repo]\n    bogus: 1\n"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := ValidateFile(bad); err == nil {
		t.Fatal("expected an error for an invalid file")
	}

	if err := ValidateFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
