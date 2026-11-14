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
	"testing"
	"time"

	"sigs.k8s.io/prow/pkg/github"
)

var (
	reviewStateOptions = approvalOptions{considerReviewState: true, lgtmActsAsApprove: false}
	commandOptions     = approvalOptions{considerReviewState: true, lgtmActsAsApprove: true}
)

func TestHasApprovalCommand(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{body: "/approve", want: true},
		{body: "/approve\n", want: true},
		{body: "  /approve  ", want: true},
		{body: "/lgtm", want: true},
		{body: "/approve cancel", want: true},
		{body: "/lgtm cancel", want: true},
		{body: "sounds good\n/approve\nthanks", want: true},
		{body: "you need to /approve this", want: false},
		{body: "/approved", want: false},
		{body: "Welcome to the pull request!", want: false},
	}

	for _, tt := range tests {
		if got := hasApprovalCommand(tt.body); got != tt.want {
			t.Errorf("hasApprovalCommand(%q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}

func TestApprovalState(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		reviews  []github.Review
		comments []github.IssueComment
		opts     approvalOptions
		want     map[string]bool
	}{
		{
			name:    "an approved review approves",
			reviews: []github.Review{review("alice", github.ReviewStateApproved, now)},
			opts:    reviewStateOptions,
			want:    map[string]bool{"alice": true},
		},
		{
			name:    "reviews are ignored when the approve plugin ignores them",
			reviews: []github.Review{review("alice", github.ReviewStateApproved, now)},
			opts:    approvalOptions{considerReviewState: false, lgtmActsAsApprove: true},
			want:    map[string]bool{},
		},
		{
			name: "a later change request withdraws",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateChangesRequested, now),
			},
			opts: reviewStateOptions,
			want: map[string]bool{"alice": false},
		},
		{
			name: "a dismissal withdraws",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateDismissed, now),
			},
			opts: reviewStateOptions,
			want: map[string]bool{"alice": false},
		},
		{
			name: "a plain review comment changes nothing",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateCommented, now),
			},
			opts: reviewStateOptions,
			want: map[string]bool{"alice": true},
		},
		{
			name:     "an approve command approves",
			comments: []github.IssueComment{commentFrom("alice", "/approve", now)},
			opts:     reviewStateOptions,
			want:     map[string]bool{"alice": true},
		},
		{
			name:     "lgtm approves where the approve plugin counts it",
			comments: []github.IssueComment{commentFrom("alice", "/lgtm", now)},
			opts:     commandOptions,
			want:     map[string]bool{"alice": true},
		},
		{
			name:     "lgtm does not approve where the approve plugin ignores it",
			comments: []github.IssueComment{commentFrom("alice", "/lgtm", now)},
			opts:     reviewStateOptions,
			want:     map[string]bool{},
		},
		{
			name: "a later cancel withdraws a command approval",
			comments: []github.IssueComment{
				commentFrom("alice", "/approve", now.Add(-time.Hour)),
				commentFrom("alice", "/approve cancel", now),
			},
			opts: reviewStateOptions,
			want: map[string]bool{"alice": false},
		},
		{
			// The approve plugin treats /approve cancel as the equivalent of a
			// review that withdraws, whichever came first.
			name:     "a cancel after a review approval withdraws it",
			reviews:  []github.Review{review("alice", github.ReviewStateApproved, now.Add(-time.Hour))},
			comments: []github.IssueComment{commentFrom("alice", "/approve cancel", now)},
			opts:     reviewStateOptions,
			want:     map[string]bool{"alice": false},
		},
		{
			name:     "a review approval after a cancel approves again",
			reviews:  []github.Review{review("alice", github.ReviewStateApproved, now)},
			comments: []github.IssueComment{commentFrom("alice", "/approve cancel", now.Add(-time.Hour))},
			opts:     reviewStateOptions,
			want:     map[string]bool{"alice": true},
		},
		{
			name: "approvals of different people add up",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now),
			},
			comments: []github.IssueComment{commentFrom("bob", "/approve", now)},
			opts:     reviewStateOptions,
			want:     map[string]bool{"alice": true, "bob": true},
		},
		{
			name:     "a comment without a command changes nothing",
			comments: []github.IssueComment{commentFrom("alice", "lgtm", now)},
			opts:     commandOptions,
			want:     map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approvalState(tt.reviews, tt.comments, tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for login, want := range tt.want {
				if got[login] != want {
					t.Errorf("%s: got %v, want %v", login, got[login], want)
				}
			}
		})
	}
}

func TestWhitelistedApprovers(t *testing.T) {
	state := map[string]bool{"alice": true, "bob": false, "carol": true}
	whitelist := []string{"carol", "bob", "dave", "alice"}

	got := whitelistedApprovers(state, whitelist)
	want := []string{"carol", "alice"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
