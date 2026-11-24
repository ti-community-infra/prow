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

func TestIsApprovalNotification(t *testing.T) {
	isSelf := func(login string) bool { return login == selfLogin }

	tests := []struct {
		name string
		user github.User
		body string
		want bool
	}{
		{
			name: "notification from our own app",
			user: github.User{Login: selfLogin, Type: github.UserTypeBot},
			body: "[APPROVALNOTIFIER] This PR is **APPROVED**",
			want: true,
		},
		{
			name: "leading whitespace",
			user: github.User{Login: selfLogin, Type: github.UserTypeBot},
			body: "  [APPROVALNOTIFIER] This PR is **NOT APPROVED**",
			want: true,
		},
		{
			// Another bot cannot speak for this app.
			name: "notification from a different bot",
			user: github.User{Login: "another-bot", Type: github.UserTypeBot},
			body: "[APPROVALNOTIFIER] This PR is **APPROVED**",
			want: false,
		},
		{
			name: "notification text from a human",
			user: github.User{Login: "someone", Type: github.UserTypeUser},
			body: "[APPROVALNOTIFIER] This PR is **APPROVED**",
			want: false,
		},
		{
			name: "our own comment without the marker",
			user: github.User{Login: selfLogin, Type: github.UserTypeBot},
			body: "Welcome to the pull request!",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := github.IssueComment{User: tt.user, Body: tt.body}
			if got := isApprovalNotification(comment, isSelf); got != tt.want {
				t.Errorf("isApprovalNotification(%q by %s) = %v, want %v", tt.body, tt.user.Login, got, tt.want)
			}
		})
	}
}

func TestApproversFromNotifications(t *testing.T) {
	isSelf := func(login string) bool { return login == selfLogin }

	comments := []github.IssueComment{
		notification("alice"),
		notification("alice", "bob"),
		{User: github.User{Login: "someone", Type: github.UserTypeUser}, Body: approvalDetailsPrefix + " <a title=\"Approved\">mallory</a>"},
		{User: github.User{Login: "another-bot", Type: github.UserTypeBot}, Body: "[APPROVALNOTIFIER] This PR is **APPROVED** " + approvalDetailsPrefix + " <a title=\"Approved\">mallory</a>"},
		{User: github.User{Login: selfLogin, Type: github.UserTypeBot}, Body: "[APPROVALNOTIFIER] This PR is **NOT APPROVED**"},
	}

	got := approversFromNotifications(comments, isSelf)
	want := []string{"alice", "bob"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// selfLogin is the identity the fake plugin runs as; the approval notification is
// posted by that same app in this deployment.
const selfLogin = "prow-protected-changes-bot"

func TestApproversFromReviews(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		reviews []github.Review
		want    []string
	}{
		{
			name:    "approved review",
			reviews: []github.Review{review("alice", github.ReviewStateApproved, now)},
			want:    []string{"alice"},
		},
		{
			name: "a later change request withdraws",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateChangesRequested, now),
			},
			want: nil,
		},
		{
			name: "a dismissal withdraws",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateDismissed, now),
			},
			want: nil,
		},
		{
			name: "a plain comment changes nothing",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("alice", github.ReviewStateCommented, now),
			},
			want: []string{"alice"},
		},
		{
			name: "re-approving after a change request",
			reviews: []github.Review{
				review("alice", github.ReviewStateChangesRequested, now.Add(-time.Hour)),
				review("alice", github.ReviewStateApproved, now),
			},
			want: []string{"alice"},
		},
		{
			name: "several approvers",
			reviews: []github.Review{
				review("alice", github.ReviewStateApproved, now.Add(-time.Hour)),
				review("bob", github.ReviewStateApproved, now),
			},
			want: []string{"alice", "bob"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approversFromReviews(tt.reviews)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestWhitelistedApprovers(t *testing.T) {
	got := whitelistedApprovers([]string{"alice", "mallory"}, []string{"carol", "alice", "bob"})
	want := []string{"alice"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
