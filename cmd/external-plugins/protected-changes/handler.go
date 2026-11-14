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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"sigs.k8s.io/prow/cmd/external-plugins/protected-changes/rules"
	"sigs.k8s.io/prow/pkg/github"
)

const (
	// statusContext is the commit status context; it is also the name tide uses
	// if the check is ever made required.
	statusContext = "protected-changes"
	// commentMarker identifies the advisory comment, so it is updated in place.
	commentMarker = "<!-- protected-changes -->"

	// githubStatusDescriptionLimit is the API limit for a status description.
	githubStatusDescriptionLimit = 140
	maxCommentRows               = 20
	maxCommentFieldLength        = 160
)

type handler struct {
	configs *configSource
	ghc     githubAPI
	cache   *cache
	dryRun  bool
	log     *logrus.Entry
}

func newHandler(configs *configSource, ghc githubAPI, cache *cache, dryRun bool, log *logrus.Entry) *handler {
	return &handler{
		configs: configs,
		ghc:     ghc,
		cache:   cache,
		dryRun:  dryRun,
		log:     log,
	}
}

// handlePullRequestEvent evaluates a push to a pull request.
func (h *handler) handlePullRequestEvent(event *github.PullRequestEvent) error {
	switch event.Action {
	case github.PullRequestActionOpened, github.PullRequestActionSynchronize, github.PullRequestActionReopened:
	default:
		return nil
	}
	if event.PullRequest.State != github.PullRequestStateOpen {
		return nil
	}

	return h.evaluate(event.Repo.Owner.Login, event.Repo.Name, event.Number, event.PullRequest.Head.SHA)
}

// handleIssueCommentEvent re-evaluates the approval half of the verdict.
//
// Only ti-chi-bot comments matter: an approval or a withdrawn approval makes the
// bot post a new notification. Re-checking every comment would turn the busiest
// webhook event into a GitHub API consumer for no verdict change.
func (h *handler) handleIssueCommentEvent(event *github.IssueCommentEvent) error {
	if event.Action != github.IssueCommentActionCreated {
		return nil
	}
	if event.Issue.PullRequest == nil || event.Issue.State == github.PullRequestStateClosed {
		return nil
	}
	if login := event.Comment.User.Login; login != tiChiBotLogin && login != tiChiBotLoginBold {
		return nil
	}

	org, repo, number := event.Repo.Owner.Login, event.Repo.Name, event.Issue.Number
	pr, err := h.ghc.GetPullRequest(org, repo, number)
	if err != nil {
		githubErrors.WithLabelValues("get_pull_request").Inc()
		return fmt.Errorf("failed to read pull request %s/%s#%d: %w", org, repo, number, err)
	}

	return h.evaluate(org, repo, number, pr.Head.SHA)
}

// evaluate computes the verdict for one commit of a pull request and publishes
// it as a status plus one advisory comment.
//
// Everything here runs off the webhook response path: the caller has already
// acknowledged the event.
func (h *handler) evaluate(org, repo string, number int, sha string) error {
	fullName := org + "/" + repo
	config := h.configs.Current()
	if config.GetRepository(fullName) == nil {
		return nil
	}

	started := time.Now()
	defer func() {
		evaluationDuration.Observe(time.Since(started).Seconds())
	}()

	unlock := h.cache.lock(fullName, number)
	defer unlock()

	checker := rules.NewChecker(config)
	key := cacheKey{repo: fullName, number: number, sha: sha, configHash: config.Hash()}

	matches, cached := h.cache.matches(key)
	if cached {
		matchCacheHits.Inc()
	} else {
		changes, err := h.ghc.GetPullRequestChanges(org, repo, number)
		if err != nil {
			githubErrors.WithLabelValues("get_pull_request_changes").Inc()
			return h.reportFailure(org, repo, sha, "could not read the pull request diff", err)
		}

		files := make([]rules.FileChange, 0, len(changes))
		for _, change := range changes {
			if change.Patch == "" && change.Additions > 0 {
				// GitHub omits the patch of a very large or binary file; those
				// added lines cannot be evaluated.
				h.log.WithField("file", change.Filename).Warn("GitHub returned no patch for a changed file.")
			}
			files = append(files, rules.FileChange{Filename: change.Filename, Patch: change.Patch})
		}

		matches, err = checker.CheckFileChanges(fullName, files)
		if err != nil {
			return h.reportFailure(org, repo, sha, "could not evaluate the protected-change rules", err)
		}
		h.cache.storeMatches(key, matches)
	}

	if len(matches) == 0 {
		evaluations.WithLabelValues("clean").Inc()
		if err := h.postStatus(org, repo, sha, github.StatusSuccess, "No protected changes detected."); err != nil {
			return err
		}
		return h.clearComment(org, repo, number)
	}

	comments, err := h.ghc.ListIssueComments(org, repo, number)
	if err != nil {
		githubErrors.WithLabelValues("list_issue_comments").Inc()
		return h.reportFailure(org, repo, sha, "could not read the pull request comments", err)
	}

	verdict, err := checker.Evaluate(fullName, matches, approversFromComments(comments))
	if err != nil {
		return h.reportFailure(org, repo, sha, "could not evaluate the approval state", err)
	}

	description, state := h.describe(verdict)
	if err := h.postStatus(org, repo, sha, state, description); err != nil {
		return err
	}

	if verdict.Approved {
		evaluations.WithLabelValues("approved").Inc()
		return h.clearCommentWith(comments, org, repo, number)
	}

	if verdict.Mode == rules.ModeWarn {
		evaluations.WithLabelValues("warned").Inc()
	} else {
		evaluations.WithLabelValues("unapproved").Inc()
	}

	return h.upsertComment(comments, org, repo, number, renderComment(verdict, config.Settings.Hint))
}

// describe renders the status description and state for a verdict.
func (h *handler) describe(verdict *rules.Verdict) (string, string) {
	ruleCount := distinctRules(verdict.Matches)

	if verdict.Approved {
		return truncate(fmt.Sprintf("%d protected rule(s) matched, approved by %s.",
			ruleCount, strings.Join(verdict.ApprovedBy, ", ")), githubStatusDescriptionLimit), github.StatusSuccess
	}

	if len(verdict.Required) == 0 {
		// Nobody can approve, so the check can never pass: say so instead of
		// asking for an approval that no login can satisfy.
		return truncate(fmt.Sprintf("%d protected rule(s) matched, but no approver is configured for this repository.",
			ruleCount), githubStatusDescriptionLimit), github.StatusError
	}

	description := fmt.Sprintf("%d protected rule(s) matched, approval needed from %s.",
		ruleCount, strings.Join(verdict.Required, ", "))
	if verdict.Mode == rules.ModeWarn {
		return truncate(description+" (advisory)", githubStatusDescriptionLimit), github.StatusSuccess
	}
	return truncate(description, githubStatusDescriptionLimit), github.StatusFailure
}

// reportFailure publishes an error status. An internal failure must never leave
// the commit looking green.
func (h *handler) reportFailure(org, repo, sha, description string, cause error) error {
	statusErr := h.postStatus(org, repo, sha, github.StatusError, "protected-changes could not finish: "+description)
	if statusErr != nil {
		h.log.WithError(statusErr).Error("Failed to post the error status.")
	}
	if cause != nil {
		return cause
	}
	return errors.New(description)
}

func (h *handler) postStatus(org, repo, sha, state, description string) error {
	status := github.Status{
		State:       state,
		Context:     statusContext,
		Description: truncate(description, githubStatusDescriptionLimit),
	}

	if h.dryRun {
		h.log.WithFields(logrus.Fields{
			"github-org": org, "github-repo": repo, "sha": sha, "state": state, "description": description,
		}).Info("Dry run: would post a commit status.")
		return nil
	}

	if err := h.ghc.CreateStatus(org, repo, sha, status); err != nil {
		githubErrors.WithLabelValues("create_status").Inc()
		return fmt.Errorf("failed to post the %s status: %w", state, err)
	}
	reportedStatuses.WithLabelValues(state).Inc()
	return nil
}

// clearComment removes the advisory comment of this pull request, if any.
func (h *handler) clearComment(org, repo string, number int) error {
	comments, err := h.ghc.ListIssueComments(org, repo, number)
	if err != nil {
		githubErrors.WithLabelValues("list_issue_comments").Inc()
		// The verdict is already published; a stale comment must not turn a
		// clean result into a failure.
		h.log.WithError(err).Warn("Failed to list comments, so a stale advisory comment may remain.")
		return nil
	}
	return h.clearCommentWith(comments, org, repo, number)
}

func (h *handler) clearCommentWith(comments []github.IssueComment, org, repo string, number int) error {
	existing := findMarkerComment(comments)
	if existing == nil {
		return nil
	}

	if h.dryRun {
		h.log.WithField("comment-id", existing.ID).Info("Dry run: would delete the advisory comment.")
		return nil
	}

	if err := h.ghc.DeleteComment(org, repo, existing.ID); err != nil {
		githubErrors.WithLabelValues("delete_comment").Inc()
		h.log.WithError(err).Warn("Failed to delete the advisory comment.")
	}
	return nil
}

// upsertComment keeps exactly one advisory comment per pull request.
func (h *handler) upsertComment(comments []github.IssueComment, org, repo string, number int, body string) error {
	existing := findMarkerComment(comments)

	if h.dryRun {
		h.log.WithFields(logrus.Fields{"comment-id": commentID(existing), "body": body}).Info("Dry run: would publish the advisory comment.")
		return nil
	}

	if existing != nil {
		if err := h.ghc.EditComment(org, repo, existing.ID, body); err != nil {
			githubErrors.WithLabelValues("edit_comment").Inc()
			return fmt.Errorf("failed to update the advisory comment: %w", err)
		}
		return nil
	}

	if err := h.ghc.CreateComment(org, repo, number, body); err != nil {
		githubErrors.WithLabelValues("create_comment").Inc()
		return fmt.Errorf("failed to post the advisory comment: %w", err)
	}
	return nil
}

func findMarkerComment(comments []github.IssueComment) *github.IssueComment {
	for i := range comments {
		if strings.Contains(comments[i].Body, commentMarker) {
			return &comments[i]
		}
	}
	return nil
}

func commentID(comment *github.IssueComment) int {
	if comment == nil {
		return 0
	}
	return comment.ID
}

// renderComment explains the verdict where contributors will see it: the
// comment stands in for the log of the presubmit job this check replaces.
func renderComment(verdict *rules.Verdict, hint string) string {
	var builder strings.Builder
	builder.WriteString(commentMarker + "\n")
	builder.WriteString("**Protected changes**\n\n")
	builder.WriteString("This pull request adds lines that a repository policy protects. ")
	builder.WriteString("Merge needs an approval from a reviewer listed for it.\n\n")

	fmt.Fprintf(&builder, "| File | Rule | Line |\n| --- | --- | --- |\n")
	for i, match := range verdict.Matches {
		if i == maxCommentRows {
			fmt.Fprintf(&builder, "| _and %d more_ | | |\n", len(verdict.Matches)-maxCommentRows)
			break
		}
		fmt.Fprintf(&builder, "| %s | %s | %s |\n",
			codeSpan(match.File), codeSpan(match.Pattern), codeSpan(match.Line))
	}

	builder.WriteString("\n")
	if len(verdict.Required) > 0 {
		fmt.Fprintf(&builder, "Required reviewers: %s (%d approval(s) needed).\n",
			mentionAll(verdict.Required), verdict.Minimum)
		fmt.Fprintf(&builder, "Approved by: %s.\n", mentionAllOrNone(verdict.ApprovedBy))
	} else {
		builder.WriteString("No reviewer is configured for this repository, so this check cannot pass.\n")
	}

	if hint := strings.TrimSpace(hint); hint != "" {
		fmt.Fprintf(&builder, "\n%s\n", hint)
	}

	builder.WriteString("\n<sub>This comment is updated on every push instead of adding a new one.</sub>\n")
	return builder.String()
}

func distinctRules(matches []rules.Match) int {
	seen := map[string]bool{}
	for _, match := range matches {
		seen[match.Pattern] = true
	}
	return len(seen)
}

func mentionAll(logins []string) string {
	mentions := make([]string, 0, len(logins))
	for _, login := range logins {
		mentions = append(mentions, "@"+login)
	}
	return strings.Join(mentions, ", ")
}

func mentionAllOrNone(logins []string) string {
	if len(logins) == 0 {
		return "none yet"
	}
	return mentionAll(logins)
}

// codeSpan renders value as inline code without letting the content break the
// table cell or the code span.
func codeSpan(value string) string {
	clean := strings.NewReplacer("`", "'", "|", "\\|", "\n", " ", "\r", " ").Replace(strings.TrimSpace(value))
	return "`" + truncate(clean, maxCommentFieldLength) + "`"
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit < 2 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
