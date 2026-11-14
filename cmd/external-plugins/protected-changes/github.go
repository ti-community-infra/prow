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
	"strings"

	"sigs.k8s.io/prow/pkg/github"
)

// githubAPI is the subset of the Prow GitHub client this plugin needs. Keeping
// it narrow makes the webhook handler testable without a GitHub instance.
type githubAPI interface {
	GetPullRequest(org, repo string, number int) (*github.PullRequest, error)
	GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error)
	ListIssueComments(org, repo string, number int) ([]github.IssueComment, error)
	CreateStatus(org, repo, sha string, status github.Status) error
	CreateComment(org, repo string, number int, body string) error
	EditComment(org, repo string, id int, body string) error
	DeleteComment(org, repo string, id int) error
}

const (
	// tiChiBot posts the approval notification that carries the approver list.
	tiChiBotLogin     = "ti-chi-bot"
	tiChiBotLoginBold = "ti-chi-bot[bot]"

	approvalNotificationPrefix = "[APPROVALNOTIFIER]"
	approvalDetailsPrefix      = "This pull-request has been approved by:"
)

// approvalLinkRegex matches the approver links ti-chi-bot renders in its
// approval notification.
var approvalLinkRegex = regexp.MustCompile(`<a[^>]*title="Approved"[^>]*>([^<]+)</a>`)

// approversFromComments extracts the approvers ti-chi-bot reported, in comment
// order and without duplicates.
func approversFromComments(comments []github.IssueComment) []string {
	var approvers []string
	seen := map[string]bool{}

	for _, comment := range comments {
		if comment.User.Login != tiChiBotLogin && comment.User.Login != tiChiBotLoginBold {
			continue
		}
		if !strings.HasPrefix(comment.Body, approvalNotificationPrefix) {
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
