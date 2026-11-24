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
	"sigs.k8s.io/prow/pkg/github"
)

// githubAPI is the subset of the Prow GitHub client this plugin needs. Keeping
// it narrow makes the webhook handler testable without a GitHub instance.
type githubAPI interface {
	GetPullRequest(org, repo string, number int) (*github.PullRequest, error)
	GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error)
	ListReviews(org, repo string, number int) ([]github.Review, error)
	ListIssueComments(org, repo string, number int) ([]github.IssueComment, error)
	CreateStatus(org, repo, sha string, status github.Status) error
	CreateComment(org, repo string, number int, body string) error
	EditComment(org, repo string, id int, body string) error
	DeleteComment(org, repo string, id int) error
}
