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

const (
	// approvalNotificationPrefix marks the comment an approval bot posts when the
	// approval state changes. It is what the presubmit this plugin replaces read,
	// so the verdict keeps matching it.
	approvalNotificationPrefix = "[APPROVALNOTIFIER]"
	approvalDetailsPrefix      = "This pull-request has been approved by:"
)

// approvalLinkRegex matches the approver links an approval notification renders.
var approvalLinkRegex = regexp.MustCompile(`<a[^>]*title="Approved"[^>]*>([^<]+)</a>`)

// isApprovalNotification reports whether a comment is an approval notification,
// whatever bot posted it: the author only has to be a bot account, no login is
// hardcoded.
func isApprovalNotification(comment github.IssueComment) bool {
	if comment.User.Type != github.UserTypeBot {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(comment.Body), approvalNotificationPrefix)
}

// approversFromNotifications extracts the approvers the approval notifications
// report, in comment order and without duplicates.
func approversFromNotifications(comments []github.IssueComment) []string {
	var approvers []string
	seen := map[string]bool{}

	for _, comment := range comments {
		if !isApprovalNotification(comment) {
			continue
		}
		if !strings.Contains(comment.Body, approvalDetailsPrefix) {
			continue
		}
		for _, match := range approvalLinkRegex.FindAllStringSubmatch(comment.Body, -1) {
			if len(match) < 2 {
				continue
			}
			login := strings.TrimSpace(match[1])
			if login == "" || seen[login] {
				continue
			}
			seen[login] = true
			approvers = append(approvers, login)
		}
	}

	return approvers
}

// approversFromReviews falls back to the GitHub review state, for repositories
// that do not run the approval bot. The latest review of each person wins, so a
// later CHANGES_REQUESTED or a dismissal withdraws an earlier approval.
func approversFromReviews(reviews []github.Review) []string {
	sorted := append([]github.Review(nil), reviews...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SubmittedAt.Before(sorted[j].SubmittedAt) })

	state := map[string]bool{}
	for _, review := range sorted {
		login := review.User.Login
		if login == "" {
			continue
		}
		switch review.State {
		case github.ReviewStateApproved:
			state[login] = true
		case github.ReviewStateChangesRequested, github.ReviewStateDismissed:
			state[login] = false
		}
	}

	// Report in the order the reviewers first appear, so the list is stable.
	var approvers []string
	seen := map[string]bool{}
	for _, review := range sorted {
		login := review.User.Login
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		if state[login] {
			approvers = append(approvers, login)
		}
	}
	return approvers
}

// whitelistedApprovers keeps the reviewers the policy lists, in policy order so
// the reported approver list is stable.
func whitelistedApprovers(candidates []string, whitelist []string) []string {
	approved := map[string]bool{}
	for _, login := range candidates {
		approved[login] = true
	}

	var approvers []string
	for _, login := range whitelist {
		if approved[login] {
			approvers = append(approvers, login)
		}
	}
	return approvers
}
