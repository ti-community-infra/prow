/*
Copyright The Kubernetes Authors.

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

package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// A config without a settings block must not turn the gate off: zero approvals
// and an empty mode would otherwise make every run pass.
func TestLoadConfigDefaultsToFailClosed(t *testing.T) {
	path := writeConfig(t, `
repositories:
  - name: pingcap/tidb
    patterns:
      - name: basic
        regex: 'LOG_ERROR'
    approvers:
      - someone
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.Settings.MinApprovals != 1 {
		t.Errorf("min approvals = %d, want 1", config.Settings.MinApprovals)
	}
	if config.Settings.CheckBehavior.Mode != ModeFail {
		t.Errorf("mode = %q, want %q", config.Settings.CheckBehavior.Mode, ModeFail)
	}
}

// An explicit threshold and mode are preserved.
func TestLoadConfigKeepsExplicitSettings(t *testing.T) {
	path := writeConfig(t, `
repositories: []
settings:
  min_approvals: 2
  check_behavior:
    mode: check_and_warn
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.Settings.MinApprovals != 2 {
		t.Errorf("min approvals = %d, want 2", config.Settings.MinApprovals)
	}
	if config.Settings.CheckBehavior.Mode != ModeWarn {
		t.Errorf("mode = %q, want %q", config.Settings.CheckBehavior.Mode, ModeWarn)
	}
}

func TestLoadConfigRejectsUnknownMode(t *testing.T) {
	path := writeConfig(t, `
repositories: []
settings:
  check_behavior:
    mode: check_and_pray
`)

	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for an unknown check mode")
	}
}

func TestLoadConfigRejectsInvalidRegex(t *testing.T) {
	path := writeConfig(t, `
repositories:
  - name: tikv/pd
    patterns:
      - name: broken
        regex: '([a-z'
`)

	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for an invalid regex")
	}
}

// The digest tracks the file content, so editing the policy invalidates cached
// evaluations while reloading an unchanged file does not.
func TestConfigHashFollowsContent(t *testing.T) {
	path := writeConfig(t, `
repositories:
  - name: tikv/pd
    patterns:
      - name: basic
        regex: 'LOG_ERROR'
`)

	first, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	again, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if first.Hash() == "" {
		t.Fatal("hash is empty")
	}
	if first.Hash() != again.Hash() {
		t.Errorf("hash changed without a content change: %s != %s", first.Hash(), again.Hash())
	}

	if err := os.WriteFile(path, []byte(`
repositories:
  - name: tikv/pd
    patterns:
      - name: basic
        regex: 'LOG_FATAL'
`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	changed, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load changed config: %v", err)
	}
	if changed.Hash() == first.Hash() {
		t.Error("hash did not change after the policy changed")
	}
}

func TestGetRepositoryIsCaseInsensitive(t *testing.T) {
	path := writeConfig(t, `
repositories:
  - name: tikv/pd
    patterns: []
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.GetRepository("TiKV/PD") == nil {
		t.Error("expected a case-insensitive repository lookup")
	}
	if config.GetRepository("pingcap/tidb") != nil {
		t.Error("expected an unconfigured repository to be absent")
	}
}
