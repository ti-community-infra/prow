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

package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"sigs.k8s.io/prow/pkg/github"
)

// selfLogin is the identity the fake plugin runs as.
const selfLogin = "prow-protected-changes-bot"

// fakeGitHub records what the plugin asks of GitHub.
type fakeGitHub struct {
	mu sync.Mutex

	pr       *github.PullRequest
	changes  []github.PullRequestChange
	reviews  []github.Review
	comments []github.IssueComment

	changesErr  error
	reviewsErr  error
	commentsErr error
	statusErr   error

	statuses        []github.Status
	createdComments []string
	editedComments  map[int]string
	deletedComments []int
	calls           map[string]int
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		editedComments: map[int]string{},
		calls:          map[string]int{},
		pr: &github.PullRequest{
			Number: 1,
			State:  github.PullRequestStateOpen,
			Head:   github.PullRequestBranch{SHA: "abc123"},
		},
	}
}

func (f *fakeGitHub) count(operation string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[operation]
}

func (f *fakeGitHub) recordedStatuses() []github.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]github.Status(nil), f.statuses...)
}

func (f *fakeGitHub) GetPullRequest(org, repo string, number int) (*github.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["GetPullRequest"]++
	if f.pr == nil {
		return nil, fmt.Errorf("no pull request")
	}
	return f.pr, nil
}

func (f *fakeGitHub) GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["GetPullRequestChanges"]++
	if f.changesErr != nil {
		return nil, f.changesErr
	}
	return f.changes, nil
}

func (f *fakeGitHub) ListReviews(org, repo string, number int) ([]github.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["ListReviews"]++
	if f.reviewsErr != nil {
		return nil, f.reviewsErr
	}
	return f.reviews, nil
}

func (f *fakeGitHub) ListIssueComments(org, repo string, number int) ([]github.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["ListIssueComments"]++
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.comments, nil
}

func (f *fakeGitHub) CreateStatus(org, repo, sha string, status github.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["CreateStatus"]++
	if f.statusErr != nil {
		return f.statusErr
	}
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeGitHub) CreateComment(org, repo string, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["CreateComment"]++
	f.createdComments = append(f.createdComments, body)
	// Behave like GitHub: the comment is visible on the next list call.
	f.comments = append(f.comments, github.IssueComment{
		ID:   1000 + len(f.createdComments),
		User: github.User{Login: selfLogin},
		Body: body,
	})
	return nil
}

func (f *fakeGitHub) EditComment(org, repo string, id int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["EditComment"]++
	f.editedComments[id] = body
	for i := range f.comments {
		if f.comments[i].ID == id {
			f.comments[i].Body = body
		}
	}
	return nil
}

func (f *fakeGitHub) DeleteComment(org, repo string, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["DeleteComment"]++
	f.deletedComments = append(f.deletedComments, id)
	return nil
}

const testPolicy = `
repositories:
  - name: tikv/pd
    patterns:
      - name: error_log
        description: Error log needs review
        regex: 'log\.Error\(".*"\)'
        excludes:
          - "tests/**"
      - name: go_mod_version
        description: go.mod directives need review
        regex: '^\s*(?:go|toolchain)\s+.*$'
        files:
          - "go.mod"
    approvers:
      - "reviewer-one"
      - "reviewer-two"
settings:
  min_approvals: 1
  check_behavior:
    mode: check_and_fail
  hint: "See the policy for details."
`

// protectedChange returns a pull request change adding one error log line.
func protectedChange() []github.PullRequestChange {
	return []github.PullRequestChange{{
		Filename:  "pkg/scheduler/log.go",
		Additions: 1,
		Patch:     "@@ -1,0 +1,1 @@\n+\tlog.Error(\"boom\")",
	}}
}

func review(login string, state github.ReviewState, at time.Time) github.Review {
	return github.Review{User: github.User{Login: login}, State: state, SubmittedAt: at}
}

func commentFrom(login, body string, at time.Time) github.IssueComment {
	return github.IssueComment{User: github.User{Login: login}, Body: body, CreatedAt: at}
}

func newTestPlugin(t *testing.T, policy string, fake *fakeGitHub, dryRun bool) *handler {
	t.Helper()

	path := writePolicy(t, policy)
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetLevel(logrus.PanicLevel)

	configs, err := newConfigSource(path, log)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}

	isSelf := func(login string) bool { return login == selfLogin }
	return newHandler(configs, fake, newCache(defaultCacheTTL, defaultCacheSize), dryRun, isSelf, log)
}

func writePolicy(t *testing.T, policy string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}

func TestUnapprovedProtectedChangeFails(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != github.StatusFailure {
		t.Errorf("state = %q, want %q", statuses[0].State, github.StatusFailure)
	}
	if statuses[0].Context != statusContext {
		t.Errorf("context = %q, want %q", statuses[0].Context, statusContext)
	}
	if !strings.Contains(statuses[0].Description, "reviewer-one") {
		t.Errorf("description %q does not name the required approvers", statuses[0].Description)
	}

	if len(fake.createdComments) != 1 {
		t.Fatalf("got %d comments, want 1", len(fake.createdComments))
	}
	body := fake.createdComments[0]
	for _, want := range []string{commentMarker, "pkg/scheduler/log.go", "error_log", "@reviewer-one", "See the policy for details."} {
		if !strings.Contains(body, want) {
			t.Errorf("comment does not contain %q:\n%s", want, body)
		}
	}
}

// A GitHub review is the primary approval signal and works without any approve
// plugin installed.
func TestApprovedByReviewSucceeds(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.reviews = []github.Review{review("reviewer-two", github.ReviewStateApproved, time.Now())}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
	if len(fake.createdComments) != 0 {
		t.Errorf("posted an advisory comment for an approved change: %v", fake.createdComments)
	}
}

// Repositories that never receive a review approve with /approve or /lgtm
// comments; that must still pass.
func TestApprovedByCommandWithoutReviewsSucceeds(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.comments = []github.IssueComment{commentFrom("reviewer-one", "/approve", time.Now())}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
	if !strings.Contains(statuses[0].Description, "reviewer-one") {
		t.Errorf("description %q does not name the approver", statuses[0].Description)
	}
}

func TestLGTMCommandAlsoApproves(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.comments = []github.IssueComment{commentFrom("reviewer-two", "/lgtm", time.Now())}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
}

func TestApprovalCancelWithdraws(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	now := time.Now()
	fake.comments = []github.IssueComment{
		commentFrom("reviewer-one", "/approve", now.Add(-time.Minute)),
		commentFrom("reviewer-one", "/approve cancel", now),
	}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusFailure {
		t.Fatalf("got statuses %+v, want one failure", statuses)
	}
}

func TestChangesRequestedWithdrawsReviewApproval(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	now := time.Now()
	fake.reviews = []github.Review{
		review("reviewer-one", github.ReviewStateApproved, now.Add(-time.Hour)),
		review("reviewer-one", github.ReviewStateChangesRequested, now),
	}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusFailure {
		t.Fatalf("got statuses %+v, want one failure", statuses)
	}
}

// An approval by someone outside the repository whitelist must not pass.
func TestApprovalByUnlistedUserFails(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.reviews = []github.Review{review("random-contributor", github.ReviewStateApproved, time.Now())}
	fake.comments = []github.IssueComment{commentFrom("random-contributor", "/approve", time.Now())}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusFailure {
		t.Fatalf("got statuses %+v, want one failure", statuses)
	}
}

// When a review from a listed approver exists, the commands are not needed.
func TestReviewApprovalIsPreferredOverCommands(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.reviews = []github.Review{review("reviewer-one", github.ReviewStateApproved, time.Now())}
	fake.comments = []github.IssueComment{commentFrom("reviewer-two", "/approve", time.Now())}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
	if !strings.Contains(statuses[0].Description, "reviewer-one") {
		t.Errorf("description %q does not name the review approver", statuses[0].Description)
	}
}

func TestCleanPullRequestSucceedsWithoutComment(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = []github.PullRequestChange{{
		Filename:  "README.md",
		Additions: 1,
		Patch:     "@@ -1,0 +1,1 @@\n+hello",
	}}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
	if len(fake.createdComments) != 0 {
		t.Errorf("posted a comment for a clean pull request: %v", fake.createdComments)
	}
	if fake.count("ListIssueComments") != 1 {
		t.Errorf("ListIssueComments calls = %d, want 1 (to clear a stale comment)", fake.count("ListIssueComments"))
	}
}

// go.mod files are only protected when a rule says so; the shipped policy does
// not enable the go.mod rule, but the capability must work when configured.
func TestFileScopedRuleMatchesNestedModule(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = []github.PullRequestChange{{
		Filename:  "tools/foo/go.mod",
		Additions: 1,
		Patch:     "@@ -1,0 +1,1 @@\n+toolchain go1.25.0",
	}}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusFailure {
		t.Fatalf("got statuses %+v, want one failure for the go.mod rule", statuses)
	}
}

func TestUnconfiguredRepositoryIsIgnored(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("pingcap", "tidb", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if len(fake.calls) != 0 {
		t.Errorf("made GitHub calls for an unconfigured repository: %v", fake.calls)
	}
}

// The plugin must never leave a commit looking green when it could not finish.
func TestInternalErrorReportsErrorStatus(t *testing.T) {
	fake := newFakeGitHub()
	fake.changesErr = fmt.Errorf("boom")
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err == nil {
		t.Fatal("expected the evaluation error to be returned")
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != github.StatusError {
		t.Errorf("state = %q, want %q", statuses[0].State, github.StatusError)
	}
}

func TestDryRunDoesNotMutate(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, testPolicy, fake, true)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if len(fake.recordedStatuses()) != 0 {
		t.Errorf("dry run posted statuses: %+v", fake.recordedStatuses())
	}
	if len(fake.createdComments) != 0 || len(fake.editedComments) != 0 {
		t.Errorf("dry run wrote comments: %v / %v", fake.createdComments, fake.editedComments)
	}
}

// An approval after a failed evaluation must reuse the computed matches for the
// same commit instead of fetching the diff again.
func TestIssueCommentReusesMatchesForTheSameCommit(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := fake.count("GetPullRequestChanges"); got != 1 {
		t.Fatalf("GetPullRequestChanges calls = %d, want 1", got)
	}

	// The approve plugin is not installed here: the approval only exists as a
	// comment command, and the webhook carries it.
	fake.comments = []github.IssueComment{commentFrom("reviewer-one", "/approve", time.Now())}
	event := &github.IssueCommentEvent{
		Action:  github.IssueCommentActionCreated,
		Repo:    github.Repo{Owner: github.User{Login: "tikv"}, Name: "pd"},
		Issue:   github.Issue{Number: 1, State: github.PullRequestStateOpen, PullRequest: &struct{}{}},
		Comment: commentFrom("reviewer-one", "/approve", time.Now()),
	}
	if err := h.handleIssueCommentEvent(event); err != nil {
		t.Fatalf("handle comment: %v", err)
	}

	if got := fake.count("GetPullRequestChanges"); got != 1 {
		t.Errorf("GetPullRequestChanges calls = %d, want 1 after an approval comment", got)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 2 || statuses[1].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want a second, successful status", statuses)
	}
}

func TestReviewEventTriggersEvaluation(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.reviews = []github.Review{review("reviewer-one", github.ReviewStateApproved, time.Now())}
	h := newTestPlugin(t, testPolicy, fake, false)

	event := &github.ReviewEvent{
		Action:      github.ReviewActionSubmitted,
		Repo:        github.Repo{Owner: github.User{Login: "tikv"}, Name: "pd"},
		PullRequest: github.PullRequest{Number: 1, State: github.PullRequestStateOpen, Head: github.PullRequestBranch{SHA: "abc123"}},
	}
	if err := h.handlePullRequestReviewEvent(event); err != nil {
		t.Fatalf("handle review: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
}

// A comment without an approval command cannot change the verdict, and our own
// comment must never re-trigger the check that wrote it.
func TestIrrelevantIssueCommentsAreIgnored(t *testing.T) {
	tests := []struct {
		name  string
		login string
		body  string
	}{
		{name: "bot welcome message", login: "ti-chi-bot", body: "Welcome to the pull request!"},
		{name: "human chatter", login: "someone", body: "looks good to me, thanks!"},
		{name: "our own comment", login: selfLogin, body: "/approve"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeGitHub()
			fake.changes = protectedChange()
			h := newTestPlugin(t, testPolicy, fake, false)

			event := &github.IssueCommentEvent{
				Action:  github.IssueCommentActionCreated,
				Repo:    github.Repo{Owner: github.User{Login: "tikv"}, Name: "pd"},
				Issue:   github.Issue{Number: 1, State: github.PullRequestStateOpen, PullRequest: &struct{}{}},
				Comment: commentFrom(tt.login, tt.body, time.Now()),
			}
			if err := h.handleIssueCommentEvent(event); err != nil {
				t.Fatalf("handle comment: %v", err)
			}

			if len(fake.calls) != 0 {
				t.Errorf("made GitHub calls for an irrelevant comment: %v", fake.calls)
			}
		})
	}
}

// A redelivered event with the same commit, policy and verdict must not add a
// second identical status to the commit.
func TestRepeatedEvaluationPublishesOneStatus(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, testPolicy, fake, false)

	for i := range 3 {
		if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
			t.Fatalf("evaluate %d: %v", i, err)
		}
	}

	if got := fake.count("CreateStatus"); got != 1 {
		t.Errorf("CreateStatus calls = %d, want 1", got)
	}
	if got := fake.count("CreateComment"); got != 1 {
		t.Errorf("CreateComment calls = %d, want 1", got)
	}
}

// A new commit must be evaluated from scratch.
func TestNewCommitInvalidatesCachedMatches(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if err := h.evaluate("tikv", "pd", 1, "def456"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if got := fake.count("GetPullRequestChanges"); got != 2 {
		t.Errorf("GetPullRequestChanges calls = %d, want 2", got)
	}
}

// The advisory comment is a single comment that is edited in place.
func TestAdvisoryCommentIsUpdatedInPlace(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.comments = []github.IssueComment{{
		ID:   42,
		User: github.User{Login: selfLogin},
		Body: commentMarker + "\n**Protected changes**\n\nold text",
	}}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if len(fake.createdComments) != 0 {
		t.Errorf("created a second comment instead of editing: %v", fake.createdComments)
	}
	body, ok := fake.editedComments[42]
	if !ok {
		t.Fatal("expected the existing comment to be edited")
	}
	if !strings.Contains(body, "pkg/scheduler/log.go") {
		t.Errorf("edited comment lacks the current finding:\n%s", body)
	}
}

// A contributor can paste the marker; rewriting their comment would be wrong.
func TestMarkerCommentFromSomeoneElseIsNotEdited(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	fake.comments = []github.IssueComment{{
		ID:   7,
		User: github.User{Login: "someone"},
		Body: commentMarker + "\nnot ours",
	}}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if len(fake.editedComments) != 0 {
		t.Errorf("edited a comment this plugin did not write: %v", fake.editedComments)
	}
	if len(fake.createdComments) != 1 {
		t.Errorf("got %d created comments, want 1", len(fake.createdComments))
	}
}

// Once the pull request no longer touches protected files, the advisory comment
// must go away.
func TestStaleCommentIsDeletedWhenClean(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = nil
	fake.comments = []github.IssueComment{{ID: 7, User: github.User{Login: selfLogin}, Body: commentMarker + "\nold finding"}}
	h := newTestPlugin(t, testPolicy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if len(fake.deletedComments) != 1 || fake.deletedComments[0] != 7 {
		t.Errorf("deleted comments = %v, want [7]", fake.deletedComments)
	}
}

// check_behavior: check_and_warn keeps the merge gate green but still explains
// what lacks approval.
func TestWarnModeReportsSuccessWithAdvisory(t *testing.T) {
	policy := strings.Replace(testPolicy, "mode: check_and_fail", "mode: check_and_warn", 1)
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, policy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 || statuses[0].State != github.StatusSuccess {
		t.Fatalf("got statuses %+v, want one success", statuses)
	}
	if !strings.Contains(statuses[0].Description, "advisory") {
		t.Errorf("description %q does not say the check is advisory", statuses[0].Description)
	}
	if len(fake.createdComments) != 1 {
		t.Errorf("got %d comments, want 1", len(fake.createdComments))
	}
}

func TestStatusDescriptionFitsGitHubLimit(t *testing.T) {
	policy := strings.Replace(testPolicy, `      - "reviewer-one"
      - "reviewer-two"`, `      - "`+strings.Repeat("a", 100)+`"
      - "reviewer-two"`, 1)
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	h := newTestPlugin(t, policy, fake, false)

	if err := h.evaluate("tikv", "pd", 1, "abc123"); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	statuses := fake.recordedStatuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if got := len([]rune(statuses[0].Description)); got > githubStatusDescriptionLimit {
		t.Errorf("description is %d characters, want at most %d", got, githubStatusDescriptionLimit)
	}
}
