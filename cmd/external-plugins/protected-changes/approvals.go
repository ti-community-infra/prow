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
	"time"

	"sigs.k8s.io/prow/pkg/github"
)

// approvalCommandRegex matches the commands contributors use to approve without
// the GitHub review UI, including the cancel forms.
var approvalCommandRegex = regexp.MustCompile(`(?m)^\s*/(approve|lgtm)(\s+cancel)?\s*$`)

// hasApprovalCommand reports whether body carries an /approve or /lgtm command.
func hasApprovalCommand(body string) bool {
	return approvalCommandRegex.MatchString(body)
}

// approvalAction is one decision about one person's approval, at a point in time.
type approvalAction struct {
	login    string
	at       time.Time
	approved bool
}

// approvalState reduces reviews and comments to the current approval state per
// reviewer, following the approve plugin: an APPROVE review is an approval and a
// CHANGES_REQUESTED review (or a dismissal) is its withdrawal, /approve and
// /approve cancel are the command equivalents, and /lgtm only counts where the
// approve plugin is configured to accept it.
//
// Both sources feed one timeline, so the latest action of a person wins no matter
// where it came from: an /approve cancel after a review approval withdraws it,
// exactly as it does for the approve plugin.
func approvalState(reviews []github.Review, comments []github.IssueComment, opts approvalOptions) map[string]bool {
	var actions []approvalAction

	if opts.considerReviewState {
		for _, review := range reviews {
			login := review.User.Login
			if login == "" {
				continue
			}
			switch review.State {
			case github.ReviewStateApproved:
				actions = append(actions, approvalAction{login: login, at: review.SubmittedAt, approved: true})
			case github.ReviewStateChangesRequested, github.ReviewStateDismissed:
				actions = append(actions, approvalAction{login: login, at: review.SubmittedAt, approved: false})
			}
		}
	}

	for _, comment := range comments {
		login := comment.User.Login
		if login == "" {
			continue
		}
		for _, match := range approvalCommandRegex.FindAllStringSubmatch(comment.Body, -1) {
			if match[1] == "lgtm" && !opts.lgtmActsAsApprove {
				continue
			}
			actions = append(actions, approvalAction{
				login:    login,
				at:       comment.CreatedAt,
				approved: strings.TrimSpace(match[2]) == "",
			})
		}
	}

	sort.SliceStable(actions, func(i, j int) bool { return actions[i].at.Before(actions[j].at) })

	approved := map[string]bool{}
	for _, action := range actions {
		approved[action.login] = action.approved
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
