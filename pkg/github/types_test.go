/*
Copyright 2017 The Kubernetes Authors.

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

package github

import (
	"encoding/json"
	"testing"
)

func TestPullRequestEventStackUnmarshal(t *testing.T) {
	t.Parallel()
	const payload = `{
		"action": "stacked",
		"number": 3,
		"pull_request": {
			"number": 3,
			"base": {"ref": "feat/parent", "sha": "abc123"},
			"head": {"ref": "feat/child", "sha": "def456"},
			"stack": {
				"id": 123456,
				"number": 50,
				"size": 5,
				"position": 3,
				"base": {"ref": "main", "sha": "trunksha"}
			}
		}
	}`

	var event PullRequestEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("failed to unmarshal pull_request event: %v", err)
	}

	if got, want := event.Action, PullRequestActionStacked; got != want {
		t.Errorf("action: got %q, want %q", got, want)
	}
	if event.PullRequest.Stack == nil {
		t.Fatal("expected Stack to be populated")
	}
	stack := event.PullRequest.Stack
	if got, want := stack.ID, int64(123456); got != want {
		t.Errorf("stack.id: got %d, want %d", got, want)
	}
	if got, want := stack.Number, 50; got != want {
		t.Errorf("stack.number: got %d, want %d", got, want)
	}
	if got, want := stack.Size, 5; got != want {
		t.Errorf("stack.size: got %d, want %d", got, want)
	}
	if got, want := stack.Position, 3; got != want {
		t.Errorf("stack.position: got %d, want %d", got, want)
	}
	if got, want := stack.Base.Ref, "main"; got != want {
		t.Errorf("stack.base.ref: got %q, want %q", got, want)
	}
	if got, want := stack.Base.SHA, "trunksha"; got != want {
		t.Errorf("stack.base.sha: got %q, want %q", got, want)
	}
	// The direct parent branch must remain untouched.
	if got, want := event.PullRequest.Base.Ref, "feat/parent"; got != want {
		t.Errorf("base.ref: got %q, want %q", got, want)
	}
}

func TestPullRequestEventWithoutStack(t *testing.T) {
	t.Parallel()
	const payload = `{
		"action": "opened",
		"number": 1,
		"pull_request": {
			"number": 1,
			"base": {"ref": "main", "sha": "abc123"},
			"head": {"ref": "feat/x", "sha": "def456"}
		}
	}`

	var event PullRequestEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("failed to unmarshal pull_request event: %v", err)
	}
	if event.PullRequest.Stack != nil {
		t.Errorf("expected Stack to be nil, got %+v", event.PullRequest.Stack)
	}
}

func TestRepoRequestToRepoSetsPrivate(t *testing.T) {
	testCases := []struct {
		name        string
		private     *bool
		visibility  *RepoVisibility
		wantPrivate bool
	}{
		{
			name:        "private field true",
			private:     new(true),
			wantPrivate: true,
		},
		{
			name:    "private field false",
			private: new(false),
		},
		{
			name:       "public visibility",
			visibility: new(RepoVisibilityPublic),
		},
		{
			name:        "private visibility",
			visibility:  new(RepoVisibilityPrivate),
			wantPrivate: true,
		},
		{
			name:        "internal visibility",
			visibility:  new(RepoVisibilityInternal),
			wantPrivate: true,
		},
		{
			name:       "visibility overrides private",
			private:    new(true),
			visibility: new(RepoVisibilityPublic),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := (RepoRequest{Private: tc.private, Visibility: tc.visibility}).ToRepo()
			if got := repo.Private; got != tc.wantPrivate {
				t.Errorf("Private = %t, want %t", got, tc.wantPrivate)
			}
		})
	}
}

func TestRepoRequestDefinedWithVisibility(t *testing.T) {
	if !(RepoRequest{Visibility: new(RepoVisibilityInternal)}).Defined() {
		t.Error("expected request with Visibility set to be defined")
	}
}

func TestIssueCapsLogin(t *testing.T) {
	// some valid logins that should all normalize to match the first
	validLoginVariants := []string{
		"BenTheElder",
		"BENTHEELDER",
		"bentheelder",
		"BenTHEElder",
		"BeNtHeElDeR",
		"bEnThEeLdEr",
	}
	// add an explicitly normalized version for sanity
	validLoginVariants = append(validLoginVariants, NormLogin(validLoginVariants[0]))

	issue := Issue{
		User: User{
			Login: validLoginVariants[0],
		},
		Assignees: []User{
			{
				Login: validLoginVariants[0],
			},
		},
	}
	for _, login := range validLoginVariants {
		if !issue.IsAuthor(login) {
			t.Errorf("expected issue.IsAuthor(%s) to be true", login)
		}
		if !issue.IsAssignee(login) {
			t.Errorf("expected issue.IsAssignee(%s) to be true", login)
		}
	}
}

func TestUnmarshalClientError(t *testing.T) {
	var testcases = []struct {
		name string
		body string
	}{
		{
			name: "invalid JSON",
			body: `{"message":"Problems parsing JSON"}`,
		},
		{
			name: "wrong type of JSON values",
			body: `{"message":"Body should be a JSON object"}`,
		},
		{
			name: "invalid fields",
			body: `{
				"message": "Validation Failed",
				"errors": [
				  {
					"resource": "Issue",
					"field": "title",
					"code": "missing_field"
				  }
				]
			  }`,
		},
		{
			name: "requires authentication",
			body: `{
				"message": "Requires authentication",
				"documentation_url": "https://developer.github.com/v3"
			  }`,
		},
		{
			name: "validation failed, position is invalid",
			body: `{
				"message": "Validation Failed",
				"errors": [
				  "Position is invalid"
				],
				"documentation_url": "https://developer.github.com/v3/pulls/reviews/#create-a-pull-request-review"
			  }`,
		},
	}
	for _, tc := range testcases {
		b := []byte(tc.body)
		err := unmarshalClientError(b)
		_, isClientError := err.(ClientError)
		_, isAlternativeClientError := err.(AlternativeClientError)
		if !(isClientError || isAlternativeClientError) {
			t.Errorf("For case %s, json.Unmarshal error: %v", tc.name, err)
		}
	}
}
