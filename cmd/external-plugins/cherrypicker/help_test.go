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

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cherryconfig "sigs.k8s.io/prow/cmd/external-plugins/cherrypicker/config"
	prowconfig "sigs.k8s.io/prow/pkg/config"
)

func TestHelpProviderRendersEffectiveConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "external_plugins_config.yaml")
	if err := os.WriteFile(path, []byte(`
cherry-pick:
  - repos:
      - foo/bar
    allow_all: true
    on_conflict: fail
`), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	agent := cherryconfig.NewAgent()
	if err := agent.Load(path); err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	provider := HelpProvider(agent, cherryconfig.Defaults{
		LabelPrefix:        cherryconfig.DefaultLabelPrefix,
		UseProwAssignments: true,
		OnConflict:         cherryconfig.OnConflictHold,
	})
	help, err := provider([]prowconfig.OrgRepo{{Org: "foo", Repo: "bar"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	config, ok := help.Config["foo/bar"]
	if !ok {
		t.Fatalf("expected help config for foo/bar, got %v", help.Config)
	}
	for _, want := range []string{
		"label_prefix: <code>needs-cherry-pick-</code>",
		"allow_all: <code>true</code>",
		"on_conflict: <code>fail</code>",
		"fork_owner: <code></code>",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("expected help config to contain %q, got:\n%s", want, config)
		}
	}

	var usages []string
	for _, command := range help.Commands {
		usages = append(usages, command.Usage)
	}
	for _, want := range []string{"/cherrypick [branch]", "/cherry-pick-invite"} {
		if !slices.Contains(usages, want) {
			t.Errorf("expected help to document %q, got %v", want, usages)
		}
	}
}

func TestHelpProviderWithoutConfig(t *testing.T) {
	provider := HelpProvider(nil, cherryconfig.Defaults{
		LabelPrefix:        cherryconfig.DefaultLabelPrefix,
		UseProwAssignments: true,
		OnConflict:         cherryconfig.OnConflictHold,
	})
	help, err := provider([]prowconfig.OrgRepo{{Org: "foo", Repo: "bar"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(help.Config) != 0 {
		t.Fatalf("expected no per-repo config without an agent, got %v", help.Config)
	}
}
