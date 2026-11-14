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
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/prow/pkg/github"
)

// approvalCommandRegex matches the commands contributors and the approve/lgtm
// plugins use to approve without the GitHub review UI, including the cancel
// forms. Both are accepted: a repository that only runs one of the two plugins
// still produces a signal.
var approvalCommandRegex = regexp.MustCompile(`(?m)^\s*/(approve|lgtm)(\s+cancel)?\s*$`)

// hasApprovalCommand reports whether body carries an /approve or /lgtm command.
func hasApprovalCommand(body string) bool {
	return approvalCommandRegex.MatchString(body)
}

// approvalFromReviews reduces reviews to the current approval state per reviewer.
// The latest review wins, so a later CHANGES_REQUESTED or a dismissal withdraws
// an earlier approval; a plain comment says nothing either way.
func approvalFromReviews(reviews []github.Review) map[string]bool {
	sorted := append([]github.Review(nil), reviews...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SubmittedAt.Before(sorted[j].SubmittedAt) })

	approved := map[string]bool{}
	for _, review := range sorted {
		login := review.User.Login
		if login == "" {
			continue
		}
		switch review.State {
		case github.ReviewStateApproved:
			approved[login] = true
		case github.ReviewStateChangesRequested, github.ReviewStateDismissed:
			approved[login] = false
		}
	}
	return approved
}

// approvalFromComments applies /approve and /lgtm commands in comment order: the
// last command of a user wins and "cancel" withdraws the approval.
func approvalFromComments(comments []github.IssueComment) map[string]bool {
	sorted := append([]github.IssueComment(nil), comments...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) })

	approved := map[string]bool{}
	for _, comment := range sorted {
		login := comment.User.Login
		if login == "" {
			continue
		}
		for _, match := range approvalCommandRegex.FindAllStringSubmatch(comment.Body, -1) {
			approved[login] = strings.TrimSpace(match[2]) == ""
		}
	}
	return approved
}

// whitelistedApprovers keeps the reviewers the policy lists, in policy order so
// the reported approver list is stable.
func whitelistedApprovers(state map[string]bool, whitelist []string) []string {
	var approvers []string
	for _, login := range whitelist {
		if state[login] {
			approvers = append(approvers, login)
		}
	}
	return approvers
}
