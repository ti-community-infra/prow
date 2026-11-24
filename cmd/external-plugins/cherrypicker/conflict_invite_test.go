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
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	cherryconfig "sigs.k8s.io/prow/cmd/external-plugins/cherrypicker/config"
	localgit "sigs.k8s.io/prow/pkg/git/localgit"
	"sigs.k8s.io/prow/pkg/github"
)

func TestConflictPolicies(t *testing.T) {
	tests := []struct {
		policy    cherryconfig.OnConflict
		wantIssue int
		wantPRs   int
	}{
		{policy: cherryconfig.OnConflictFail, wantIssue: 0, wantPRs: 0},
		{policy: cherryconfig.OnConflictIssue, wantIssue: 1, wantPRs: 0},
		// Without a merge commit the conflicts cannot be turned into a
		// commit, so the hold policy reports the failure instead.
		{policy: cherryconfig.OnConflictHold, wantIssue: 0, wantPRs: 0},
	}

	for _, tc := range tests {
		t.Run(string(tc.policy), func(t *testing.T) {
			lg, c := makeFakeRepoWithCommit(localgit.NewV2, t)
			if err := lg.CheckoutNewBranch("foo", "bar", "stage"); err != nil {
				t.Fatalf("failed to create the target branch: %v", err)
			}

			ghc := &fghc{
				pr: &github.PullRequest{
					Number: fakePR.GetPRNumber(),
					Merged: true,
					Title:  "This is a fix for X",
				},
				isMember: true,
				patch:    []byte("this is not a valid patch"),
			}

			s := &Server{
				botUser:        &github.UserData{Login: "ci-robot"},
				gc:             c,
				pusher:         fakePusher{},
				ghc:            ghc,
				tokenGenerator: func() []byte { return []byte("sha=abcdefg") },
				log:            logrus.WithField("test", t.Name()),
			}
			s.defaults.OnConflict = tc.policy

			_ = s.handle(s.log, "wiseguy", nil, "foo", "bar", "stage", "base", nil, "This is a fix for X", body, ghc.pr.Number)

			if len(ghc.issues) != tc.wantIssue {
				t.Errorf("expected %d issues, got %d", tc.wantIssue, len(ghc.issues))
			}
			if len(ghc.prs) != tc.wantPRs {
				t.Errorf("expected %d pull requests, got %d", tc.wantPRs, len(ghc.prs))
			}
			if len(ghc.comments) == 0 {
				t.Error("expected the failure to be reported in a comment")
			}
		})
	}
}

func TestConflictHoldPolicyOpensHeldPullRequest(t *testing.T) {
	lg, c := makeFakeRepoWithCommit(localgit.NewV2, t)

	// The default branch name depends on the git configuration, so remember
	// the base commit and detach to it instead of checking it out by name.
	baseSHA, err := lg.RevParse("foo", "bar", "HEAD")
	if err != nil {
		t.Fatalf("failed to resolve the base commit: %v", err)
	}

	if err := lg.CheckoutNewBranch("foo", "bar", "feature"); err != nil {
		t.Fatalf("failed to create the feature branch: %v", err)
	}
	if err := lg.AddCommit("foo", "bar", map[string][]byte{"bar.go": []byte("feature version\n")}); err != nil {
		t.Fatalf("failed to commit the feature change: %v", err)
	}

	if err := lg.Checkout("foo", "bar", baseSHA); err != nil {
		t.Fatalf("failed to check out the base commit: %v", err)
	}
	if err := lg.CheckoutNewBranch("foo", "bar", "stage"); err != nil {
		t.Fatalf("failed to create the target branch: %v", err)
	}
	if err := lg.AddCommit("foo", "bar", map[string][]byte{"bar.go": []byte("stage version\n")}); err != nil {
		t.Fatalf("failed to commit the conflicting change: %v", err)
	}
	if err := lg.Checkout("foo", "bar", baseSHA); err != nil {
		t.Fatalf("failed to check out the base commit: %v", err)
	}

	if _, err := lg.Merge("foo", "bar", "feature"); err != nil {
		t.Fatalf("failed to create the merge commit: %v", err)
	}
	mergeSHA, err := lg.RevParse("foo", "bar", "HEAD")
	if err != nil {
		t.Fatalf("failed to resolve the merge commit: %v", err)
	}

	ghc := &fghc{
		pr: &github.PullRequest{
			Number:   fakePR.GetPRNumber(),
			Merged:   true,
			MergeSHA: &mergeSHA,
			Title:    "This is a fix for X",
		},
		isMember: true,
		patch:    []byte("this is not a valid patch"),
	}

	s := &Server{
		botUser:        &github.UserData{Login: "ci-robot"},
		gc:             c,
		pusher:         fakePusher{},
		ghc:            ghc,
		tokenGenerator: func() []byte { return []byte("sha=abcdefg") },
		log:            logrus.WithField("test", t.Name()),
	}
	s.defaults.OnConflict = cherryconfig.OnConflictHold

	_ = s.handle(s.log, "wiseguy", nil, "foo", "bar", "stage", "base", nil, "This is a fix for X", body, ghc.pr.Number)

	if len(ghc.prs) != 1 {
		t.Fatalf("expected one held pull request, got %d (comments: %v)", len(ghc.prs), ghc.comments)
	}
	held := false
	for _, label := range ghc.prs[0].Labels {
		if label.Name == "do-not-merge/hold" {
			held = true
		}
	}
	if !held {
		t.Errorf("expected the do-not-merge/hold label, got %v", ghc.prs[0].Labels)
	}
	pinged := false
	for _, comment := range ghc.comments {
		if strings.Contains(comment, "/unhold") {
			pinged = true
		}
	}
	if !pinged {
		t.Errorf("expected the /unhold instructions, got %v", ghc.comments)
	}
}

func TestCommitConflictsRequiresMergedPullRequest(t *testing.T) {
	s := &Server{ghc: &fghc{pr: &github.PullRequest{Number: 1}}}
	if err := s.commitConflicts(nil, "foo", "bar", 1, false); err == nil {
		t.Fatal("expected an error when the pull request has no merge commit")
	}
}

func TestCherryPickCommitMessage(t *testing.T) {
	sha := "abc123"
	ghc := &fghc{
		pr: &github.PullRequest{Number: 7, MergeSHA: &sha},
		singleCommit: github.RepositoryCommit{
			Commit: github.GitCommit{Message: "Fix the things\n\nFix #12, close foo/bar#34"},
		},
	}
	s := &Server{ghc: ghc}

	if got := s.cherryPickCommitMessage("foo", "bar", 7, false); got != "This is an automated cherry-pick of #7" {
		t.Errorf("unexpected message without issue numbers: %q", got)
	}

	got := s.cherryPickCommitMessage("foo", "bar", 7, true)
	for _, want := range []string{"This is an automated cherry-pick of #7", "#12", "foo/bar#34"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in the commit message, got %q", want, got)
		}
	}
}

func inviteEvent(body string) github.IssueCommentEvent {
	return github.IssueCommentEvent{
		Action: github.IssueCommentActionCreated,
		Repo: github.Repo{
			Owner: github.User{Login: "base-org"},
			Name:  "repo",
		},
		Issue: github.Issue{Number: 11, PullRequest: &struct{}{}},
		Comment: github.IssueComment{
			User: github.User{Login: "wiseguy"},
			Body: body,
		},
	}
}

func forkPR(headOwner string) *github.PullRequest {
	return &github.PullRequest{
		Number: 11,
		Head: github.PullRequestBranch{
			Ref:  "cherry-pick-1-to-release-1.5",
			Repo: github.Repo{Owner: github.User{Login: headOwner}},
		},
	}
}

func TestInviteCollaborator(t *testing.T) {
	logger := logrus.WithField("test", t.Name())

	t.Run("no fork owner", func(t *testing.T) {
		ghc := &fghc{pr: forkPR("base-org")}
		s := &Server{ghc: ghc, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 1 || !strings.Contains(ghc.comments[0], "no invitation is needed") {
			t.Fatalf("expected a no-invitation comment, got %v", ghc.comments)
		}
	})

	t.Run("pull request is not from the fork", func(t *testing.T) {
		ghc := &fghc{pr: forkPR("base-org")}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{ForkOwner: "bot-org"}, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 1 || !strings.Contains(ghc.comments[0], "not created from") {
			t.Fatalf("expected a not-from-fork comment, got %v", ghc.comments)
		}
	})

	t.Run("already a collaborator", func(t *testing.T) {
		ghc := &fghc{
			pr:            forkPR("bot-org"),
			collaborators: []github.User{{Login: "wiseguy"}},
		}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{ForkOwner: "bot-org"}, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 1 || !strings.Contains(ghc.comments[0], "already a collaborator") {
			t.Fatalf("expected an already-collaborator comment, got %v", ghc.comments)
		}
	})

	t.Run("pending invitation", func(t *testing.T) {
		ghc := &fghc{
			pr: forkPR("bot-org"),
			invitations: []github.CollaboratorRepoInvitation{
				{Invitee: &github.User{Login: "wiseguy"}},
			},
		}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{ForkOwner: "bot-org"}, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 0 {
			t.Fatalf("expected no comment for a pending invitation, got %v", ghc.comments)
		}
		if len(ghc.addedCollaborators) != 0 {
			t.Fatalf("expected no new invitation, got %v", ghc.addedCollaborators)
		}
	})

	t.Run("not an org member", func(t *testing.T) {
		ghc := &fghc{pr: forkPR("bot-org"), isMember: false}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{ForkOwner: "bot-org"}, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 1 || !strings.Contains(ghc.comments[0], "not a member") {
			t.Fatalf("expected a not-a-member comment, got %v", ghc.comments)
		}
	})

	t.Run("invitation rejected", func(t *testing.T) {
		ghc := &fghc{
			pr:                 forkPR("bot-org"),
			isMember:           true,
			addCollaboratorErr: errors.New("administration permission required"),
		}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{ForkOwner: "bot-org"}, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.comments) != 1 || !strings.Contains(ghc.comments[0], "failed to invite") {
			t.Fatalf("expected a failure comment, got %v", ghc.comments)
		}
	})

	t.Run("invited", func(t *testing.T) {
		ghc := &fghc{pr: forkPR("bot-org"), isMember: true}
		s := &Server{ghc: ghc, defaults: cherryconfig.Defaults{ForkOwner: "bot-org"}, githubURL: "https://github.com"}
		if err := s.inviteCollaborator(logger, inviteEvent("/cherry-pick-invite")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ghc.addedCollaborators) != 1 || ghc.addedCollaborators[0] != "bot-org/repo:wiseguy:write" {
			t.Fatalf("expected a write invitation, got %v", ghc.addedCollaborators)
		}
		if len(ghc.comments) != 1 || !strings.Contains(ghc.comments[0], "bot-org/repo/invitations") {
			t.Fatalf("expected an invitation comment, got %v", ghc.comments)
		}
		if mentions := strings.Count(ghc.comments[0], "@wiseguy"); mentions != 1 {
			t.Errorf("expected exactly one mention of the requester, got %d in %q", mentions, ghc.comments[0])
		}
	})
}
