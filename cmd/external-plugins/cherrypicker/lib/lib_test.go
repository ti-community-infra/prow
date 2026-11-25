/*
Copyright 2026 The Kubernetes Authors.

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

package cherrypicker

import "testing"

func TestCreateCherrypickBody(t *testing.T) {
	tests := []struct {
		name          string
		requestor     string
		sourceBody    string
		note          string
		chainBranches []string
		kindLabels    []string
		want          string
	}{
		{
			name:       "copies the source body with its issue reference",
			requestor:  "octocat",
			sourceBody: "### What problem does this PR solve?\n\nIssue Number: close #13018\n\n```release-note\nNone\n```",
			note:       "```release-note\nNone\n```",
			want:       "This is an automated cherry-pick of #7\n\n/assign octocat\n\n### What problem does this PR solve?\n\nIssue Number: close #13018\n\n```release-note\nNone\n```",
		},
		{
			name:       "appends the normalized release note when the body has none",
			sourceBody: "### Release note\n\n**Release note**:\n```\nNone\n```",
			note:       "```release-note\nNone\n```",
			want:       "This is an automated cherry-pick of #7\n\n### Release note\n\n**Release note**:\n```\nNone\n```\n\n```release-note\nNone\n```",
		},
		{
			name:       "omits the assign line when assignments are disabled",
			sourceBody: "a fix",
			want:       "This is an automated cherry-pick of #7\n\na fix",
		},
		{
			name:       "trims an empty source body",
			sourceBody: "\n\n",
			note:       "",
			want:       "This is an automated cherry-pick of #7",
		},
		{
			name:          "chain and kind lines follow the copied body",
			sourceBody:    "a fix",
			chainBranches: []string{"release-1.5", "release-1.4"},
			kindLabels:    []string{"cleanup", "bug"},
			want:          "This is an automated cherry-pick of #7\n\na fix\n\n/cherrypick release-1.5 release-1.4\n\n/kind bug\n/kind cleanup",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CreateCherrypickBody(7, tc.requestor, tc.sourceBody, tc.note, tc.chainBranches, tc.kindLabels)
			if got != tc.want {
				t.Errorf("unexpected body:\nwant:\n%s\n\ngot:\n%s", tc.want, got)
			}
		})
	}
}
