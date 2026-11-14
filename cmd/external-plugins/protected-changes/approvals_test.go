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

func TestApprovalFromReviews(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		reviews []github.Review
		want    map[string]bool
	}{
		{
			name:    "approved review",
			reviews: []github.Review{review("alice", github.ReviewStateApproved, now)},
			want:    map[string]bool{"alice": true},
		},
		{
			name: "a later change request withdraws",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateChangesRequested, now),
			},
			want: map[string]bool{"alice": false},
		},
		{
			name: "a dismissal withdraws",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateDismissed, now),
			},
			want: map[string]bool{"alice": false},
		},
		{
			name: "a plain comment does not change the state",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateCommented, now),
			},
			want: map[string]bool{"alice": true},
		},
		{
			name: "re-approving after a change request approves again",
			reviews: []github.Review{
				review("alice", github.ReviewStateChangesRequested, now.Add(-time.Hour)),
				review("alice", github.ReviewStateApproved, now),
			},
			want: map[string]bool{"alice": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approvalFromReviews(tt.reviews)
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

func TestApprovalFromComments(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		comments []github.IssueComment
		want     map[string]bool
	}{
		{
			name:     "approve command",
			comments: []github.IssueComment{commentFrom("alice", "/approve", now)},
			want:     map[string]bool{"alice": true},
		},
		{
			name:     "lgtm command",
			comments: []github.IssueComment{commentFrom("alice", "/lgtm", now)},
			want:     map[string]bool{"alice": true},
		},
		{
			name: "a later cancel withdraws",
			comments: []github.IssueComment{
				commentFrom("alice", "/approve", now.Add(-time.Hour)),
				commentFrom("alice", "/approve cancel", now),
			},
			want: map[string]bool{"alice": false},
		},
		{
			name: "comments are applied in chronological order",
			comments: []github.IssueComment{
				commentFrom("alice", "/approve cancel", now),
				commentFrom("alice", "/approve", now.Add(-time.Hour)),
			},
			want: map[string]bool{"alice": false},
		},
		{
			name: "several commands in one comment: the last one wins",
			comments: []github.IssueComment{
				commentFrom("alice", "/approve\n/lgtm cancel", now),
			},
			want: map[string]bool{"alice": false},
		},
		{
			name:     "a comment without a command changes nothing",
			comments: []github.IssueComment{commentFrom("alice", "lgtm", now)},
			want:     map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approvalFromComments(tt.comments)
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
