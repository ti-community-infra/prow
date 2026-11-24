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
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	cherryconfig "sigs.k8s.io/prow/cmd/external-plugins/cherrypicker/config"
	"sigs.k8s.io/prow/pkg/github"
)

func newAgent(t *testing.T, contents string) *cherryconfig.Agent {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "external_plugins_config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	agent := cherryconfig.NewAgent()
	if err := agent.Load(path); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	return agent
}

func TestServerOptionsLayerConfigOverDefaults(t *testing.T) {
	agent := newAgent(t, `
cherry-pick:
  - repos:
      - base-org
    allow_all: true
    picked_label_prefix: "type/cherry-pick-for-"
  - repos:
      - base-org/repo
    label_prefix: custom-pick-
    on_conflict: fail
`)

	s := &Server{
		configAgent: agent,
		defaults: cherryconfig.Defaults{
			LabelPrefix:        "needs-cherry-pick-",
			AllowAll:           false,
			UseProwAssignments: true,
			Labels:             []string{"from-flag"},
			OnConflict:         cherryconfig.OnConflictHold,
		},
	}

	got := s.options("base-org", "repo")
	if !got.AllowAll {
		t.Error("expected allow_all from the organization entry")
	}
	if got.LabelPrefix != "custom-pick-" {
		t.Errorf("expected the repository label prefix, got %q", got.LabelPrefix)
	}
	if got.PickedLabelPrefix != "type/cherry-pick-for-" {
		t.Errorf("expected the organization picked label prefix, got %q", got.PickedLabelPrefix)
	}
	if got.OnConflict != cherryconfig.OnConflictFail {
		t.Errorf("expected the repository conflict policy, got %q", got.OnConflict)
	}
	if !got.UseProwAssignments || len(got.Labels) != 1 {
		t.Errorf("expected the flag defaults to survive, got %+v", got)
	}

	// A repository without an entry only gets the flag defaults.
	other := s.options("other-org", "repo")
	if other.AllowAll || other.LabelPrefix != "needs-cherry-pick-" {
		t.Errorf("expected the flag defaults for an unconfigured repository, got %+v", other)
	}
}

func TestServerOptionsWithoutAgent(t *testing.T) {
	s := &Server{defaults: cherryconfig.Defaults{LabelPrefix: "needs-cherry-pick-", AllowAll: true}}
	got := s.options("org", "repo")
	if !got.AllowAll || got.LabelPrefix != "needs-cherry-pick-" {
		t.Fatalf("expected the flag defaults, got %+v", got)
	}
}

func TestCherryPickLabels(t *testing.T) {
	opts := cherryconfig.Options{
		LabelPrefix:       "needs-cherry-pick-",
		Labels:            []string{"cla: yes"},
		PickedLabelPrefix: "type/cherry-pick-for-",
		ExcludeLabels:     []string{"lgtm"},
	}
	source := []github.Label{
		{Name: "lgtm"},
		{Name: "approved"},
		{Name: "needs-cherry-pick-release-1.5"},
		{Name: "kind/bug"},
		{Name: "status/can-merge"},
	}

	got := cherryPickLabels(opts, source, "release-1.5")
	want := []string{"approved", "cla: yes", "status/can-merge", "type/cherry-pick-for-release-1.5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestCherryPickLabelsWithoutPickedPrefix(t *testing.T) {
	got := cherryPickLabels(cherryconfig.Options{LabelPrefix: "needs-cherry-pick-"}, nil, "release-1.5")
	if len(got) != 0 {
		t.Fatalf("expected no labels, got %v", got)
	}
}

func TestLabelPrefixGating(t *testing.T) {
	event := github.PullRequestEvent{
		Action: github.PullRequestActionLabeled,
		Label:  github.Label{Name: "needs-cherry-pick-release-1.5"},
		PullRequest: github.PullRequest{
			Number: 7,
			User:   github.User{Login: "outsider"},
			Base: github.PullRequestBranch{
				Ref:  "master",
				Repo: github.Repo{Owner: github.User{Login: "base-org"}, Name: "repo"},
			},
		},
	}
	logger := logrus.WithField("test", t.Name())

	t.Run("empty prefix disables label initiated picks", func(t *testing.T) {
		ghc := &fghc{isMember: false}
		s := &Server{ghc: ghc}
		if _, err := s.handlePullRequestLabelAdded(logger, event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 0 {
			t.Fatalf("expected no comments, got %v", ghc.comments)
		}
	})

	t.Run("matching prefix is handled", func(t *testing.T) {
		ghc := &fghc{isMember: false}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{LabelPrefix: "needs-cherry-pick-"}}
		if _, err := s.handlePullRequestLabelAdded(logger, event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 1 {
			t.Fatalf("expected a rejection comment, got %v", ghc.comments)
		}
	})
}
