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
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
	cherryconfig "sigs.k8s.io/prow/cmd/external-plugins/cherrypicker/config"
	"sigs.k8s.io/prow/pkg/git/v2"
)

type recordingPusher struct {
	calls int
	err   error
}

func (p *recordingPusher) Push(_ git.RepoClient, _ string, _ bool) error {
	p.calls++
	return p.err
}

func TestPushTargets(t *testing.T) {
	forkErr := errors.New("fork not found")

	tests := []struct {
		name       string
		forkOwner  string
		repoErrors map[string]error
		wantOrgs   []string
		wantFork   bool
	}{
		{
			name:     "no fork owner pushes into the base repository",
			wantOrgs: []string{"base-org"},
		},
		{
			name:      "existing fork is preferred",
			forkOwner: "bot-org",
			wantOrgs:  []string{"bot-org", "base-org"},
			wantFork:  true,
		},
		{
			name:       "missing fork falls back to the base repository",
			forkOwner:  "bot-org",
			repoErrors: map[string]error{"repo": forkErr},
			wantOrgs:   []string{"base-org"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				ghc:       &fghc{repoErrors: tc.repoErrors},
				defaults:  cherryconfig.Defaults{ForkOwner: tc.forkOwner},
				githubURL: "https://github.com",
				log:       logrus.WithField("test", t.Name()),
				installationToken: func(string) (string, error) {
					return "token", nil
				},
			}

			targets := s.pushTargets(s.log, "base-org", "repo")
			if len(targets) != len(tc.wantOrgs) {
				t.Fatalf("expected %d targets, got %d", len(tc.wantOrgs), len(targets))
			}
			for i, want := range tc.wantOrgs {
				if targets[i].org != want {
					t.Errorf("target %d: expected owner %q, got %q", i, want, targets[i].org)
				}
			}
			if tc.wantFork {
				if _, ok := targets[0].pusher.(*authenticatedPusher); !ok {
					t.Errorf("expected the first target to be an authenticated pusher, got %T", targets[0].pusher)
				}
			}
		})
	}
}

func TestPushTargetsPerRepositoryForkOwner(t *testing.T) {
	agent := newAgent(t, `
cherry-pick:
  - repos:
      - base-org/central
    fork_owner: ""
  - repos:
      - base-org/other-fork
    fork_owner: other-fork
`)
	s := &Server{
		ghc:         &fghc{},
		configAgent: agent,
		defaults:    cherryconfig.Defaults{ForkOwner: "bot-org"},
		githubURL:   "https://github.com",
		log:         logrus.WithField("test", t.Name()),
		installationToken: func(string) (string, error) {
			return "token", nil
		},
	}

	if targets := s.pushTargets(s.log, "base-org", "repo"); len(targets) != 2 || targets[0].org != "bot-org" {
		t.Fatalf("expected the flag fork by default, got %+v", targets)
	}
	if targets := s.pushTargets(s.log, "base-org", "central"); len(targets) != 1 || targets[0].org != "base-org" {
		t.Fatalf("expected the repository override to push into the base repo, got %+v", targets)
	}
	if targets := s.pushTargets(s.log, "base-org", "other-fork"); len(targets) != 2 || targets[0].org != "other-fork" {
		t.Fatalf("expected the repository fork override, got %+v", targets)
	}
}

func TestPushTargetsWithoutTokenSource(t *testing.T) {
	s := &Server{
		ghc:       &fghc{},
		defaults:  cherryconfig.Defaults{ForkOwner: "bot-org"},
		githubURL: "https://github.com",
		log:       logrus.WithField("test", t.Name()),
	}
	targets := s.pushTargets(s.log, "base-org", "repo")
	if len(targets) != 1 || targets[0].org != "base-org" {
		t.Fatalf("expected a single base-repository target, got %+v", targets)
	}
}

func TestPushBranchFallback(t *testing.T) {
	t.Run("falls back to the second target", func(t *testing.T) {
		fork := &recordingPusher{err: errors.New("forbidden")}
		central := &recordingPusher{}
		s := &Server{}

		org, errs := s.pushBranch(nil, []pushTarget{
			{org: "bot-org", pusher: fork},
			{org: "base-org", pusher: central},
		}, "branch")

		if org != "base-org" {
			t.Errorf("expected the base repository to win, got %q", org)
		}
		if len(errs) != 1 {
			t.Errorf("expected one collected error, got %d", len(errs))
		}
		if fork.calls != 1 || central.calls != 1 {
			t.Errorf("expected both targets to be attempted, got fork=%d central=%d", fork.calls, central.calls)
		}
	})

	t.Run("reports failure when every target fails", func(t *testing.T) {
		fork := &recordingPusher{err: errors.New("forbidden")}
		central := &recordingPusher{err: errors.New("protected branch")}
		s := &Server{}

		org, errs := s.pushBranch(nil, []pushTarget{
			{org: "bot-org", pusher: fork},
			{org: "base-org", pusher: central},
		}, "branch")

		if org != "" {
			t.Errorf("expected no successful owner, got %q", org)
		}
		if len(errs) != 2 {
			t.Errorf("expected two collected errors, got %d", len(errs))
		}
	})
}

func TestAuthenticatedPusherPushURL(t *testing.T) {
	p := &authenticatedPusher{
		owner:     "bot-org",
		repo:      "repo",
		githubURL: "https://github.com",
		token: func(owner string) (string, error) {
			if owner != "bot-org" {
				t.Fatalf("expected the fork owner token, got owner %q", owner)
			}
			return "secret-token", nil
		},
	}

	u, err := p.pushURL("secret-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://x-access-token:secret-token@github.com/bot-org/repo.git"
	if u != want {
		t.Fatalf("expected %q, got %q", want, u)
	}
}

func TestCensorToken(t *testing.T) {
	got := censorToken("fatal: unable to access https://x-access-token:secret@github.com/bot-org/repo.git", "secret")
	if got != "fatal: unable to access https://x-access-token:***@github.com/bot-org/repo.git" {
		t.Fatalf("expected the token to be censored, got %q", got)
	}
	if got := censorToken("plain text", ""); got != "plain text" {
		t.Fatalf("expected empty tokens to be a no-op, got %q", got)
	}
}

func TestMaintainerCanModify(t *testing.T) {
	if !maintainerCanModify("base-org", "base-org") {
		t.Error("expected same-repository heads to allow maintainer edits")
	}
	if maintainerCanModify("bot-owner", "base-org") {
		t.Error("expected fork heads to skip maintainer edits")
	}
}

func TestGitHubBaseURL(t *testing.T) {
	tests := map[string]string{
		"github.com":               "https://github.com",
		"https://github.com":       "https://github.com",
		"https://ghe.example.com/": "https://ghe.example.com",
	}
	for host, want := range tests {
		if got := githubBaseURL(host); got != want {
			t.Errorf("githubBaseURL(%q) = %q, want %q", host, got, want)
		}
	}
}
