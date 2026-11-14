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

package rules

import (
	"strings"
	"testing"
)

func TestMatchesAnyPattern(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		patterns []string
		want     bool
	}{
		{
			name:     "exact match",
			filePath: "test.go",
			patterns: []string{"test.go"},
			want:     true,
		},
		{
			name:     "wildcard match",
			filePath: "test_file.go",
			patterns: []string{"*_test.go", "test_*.go"},
			want:     true,
		},
		{
			name:     "directory wildcard match",
			filePath: "tests/integration/test.go",
			patterns: []string{"tests/**"},
			want:     true,
		},
		{
			name:     "directory wildcard deep match",
			filePath: "tests/integration/subdir/test.go",
			patterns: []string{"tests/**"},
			want:     true,
		},
		{
			name:     "no match",
			filePath: "src/main.go",
			patterns: []string{"tests/**", "*_test.go"},
			want:     false,
		},
		{
			name:     "multiple patterns one matches",
			filePath: "examples/demo.go",
			patterns: []string{"tests/**", "examples/**", "vendor/**"},
			want:     true,
		},
		{
			name:     "empty patterns",
			filePath: "any/file.go",
			patterns: []string{},
			want:     false,
		},
		{
			name:     "top level directory",
			filePath: "tests/test.go",
			patterns: []string{"tests/**"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesAnyPattern(tt.filePath, tt.patterns)
			if got != tt.want {
				t.Errorf("matchesAnyPattern(%q, %v) = %v, want %v", tt.filePath, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestMatchesFileScope(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		patterns []string
		want     bool
	}{
		{
			name:     "no scope means every file",
			filePath: "pkg/any/file.go",
			patterns: nil,
			want:     true,
		},
		{
			name:     "bare name matches the repository root",
			filePath: "go.mod",
			patterns: []string{"go.mod"},
			want:     true,
		},
		{
			name:     "bare name matches nested modules by basename",
			filePath: "tools/foo/go.mod",
			patterns: []string{"go.mod"},
			want:     true,
		},
		{
			name:     "bare name does not match a different file",
			filePath: "tools/foo/go.sum",
			patterns: []string{"go.mod"},
			want:     false,
		},
		{
			name:     "glob on the basename",
			filePath: "pkg/go.mod",
			patterns: []string{"go.*"},
			want:     true,
		},
		{
			name:     "path pattern matches one directory",
			filePath: "pkg/config/settings.go",
			patterns: []string{"pkg/config/*.go"},
			want:     true,
		},
		{
			name:     "path pattern does not match elsewhere",
			filePath: "pkg/other/settings.go",
			patterns: []string{"pkg/config/*.go"},
			want:     false,
		},
		{
			name:     "path pattern with a directory wildcard",
			filePath: "pkg/a/b/settings.go",
			patterns: []string{"pkg/**"},
			want:     true,
		},
		{
			name:     "several patterns, one matches",
			filePath: "pkg/settings.go",
			patterns: []string{"go.mod", "pkg/settings.go"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesFileScope(tt.filePath, tt.patterns)
			if got != tt.want {
				t.Errorf("matchesFileScope(%q, %v) = %v, want %v", tt.filePath, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestCheckFileChangesHonoursExclusions(t *testing.T) {
	checker := newTestChecker(t, `
repositories:
  - name: test/repo
    excludes:
      - "vendor/**"
    patterns:
      - name: error_pattern
        description: Test error pattern
        regex: 'log\.Error\(".*"\)'
        excludes:
          - "tests/**"
          - "*_test.go"
`)

	matches, err := checker.CheckFileChanges("test/repo", []FileChange{
		{Filename: "main.go", Patch: "@@ -1,0 +1,1 @@\n+\tlog.Error(\"production error\")"},
		{Filename: "tests/integration_test.go", Patch: "@@ -1,0 +1,1 @@\n+\tlog.Error(\"test error\")"},
		{Filename: "vendor/lib/lib.go", Patch: "@@ -1,0 +1,1 @@\n+\tlog.Error(\"vendored error\")"},
	})
	if err != nil {
		t.Fatalf("check changes: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(matches), matches)
	}
	if matches[0].File != "main.go" {
		t.Errorf("matched %q, want main.go", matches[0].File)
	}
	if matches[0].Pattern != "error_pattern" {
		t.Errorf("matched pattern %q, want error_pattern", matches[0].Pattern)
	}
}

// #4560: a rule that only applies to go.mod files covers the repository root
// and nested modules, but not unrelated files.
func TestCheckFileChangesHonoursFileScope(t *testing.T) {
	checker := newTestChecker(t, `
repositories:
  - name: test/repo
    patterns:
      - name: go_mod_version
        description: go.mod directives need review
        regex: '^\s*(?:go|toolchain)\s+.*$'
        files:
          - "go.mod"
`)

	matches, err := checker.CheckFileChanges("test/repo", []FileChange{
		{Filename: "go.mod", Patch: "@@ -1,0 +1,1 @@\n+go 1.25.0"},
		{Filename: "tools/foo/go.mod", Patch: "@@ -1,0 +1,1 @@\n+toolchain go1.25.0"},
		{Filename: "tools/foo/main.go", Patch: "@@ -1,0 +1,1 @@\n+go run ./cmd"},
	})
	if err != nil {
		t.Fatalf("check changes: %v", err)
	}

	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matches), matches)
	}
	if matches[0].File != "go.mod" || matches[1].File != "tools/foo/go.mod" {
		t.Errorf("unexpected files: %q, %q", matches[0].File, matches[1].File)
	}
}

// Files and Excludes combine: the rule is limited to go.mod files and still
// honours its own exclusion list.
func TestCheckFileChangesCombinesFilesAndExcludes(t *testing.T) {
	checker := newTestChecker(t, `
repositories:
  - name: test/repo
    patterns:
      - name: go_mod_version
        regex: '^\s*(?:go|toolchain)\s+.*$'
        files:
          - "go.mod"
        excludes:
          - "tools/**"
`)

	matches, err := checker.CheckFileChanges("test/repo", []FileChange{
		{Filename: "go.mod", Patch: "@@ -1,0 +1,1 @@\n+go 1.25.0"},
		{Filename: "tools/foo/go.mod", Patch: "@@ -1,0 +1,1 @@\n+toolchain go1.25.0"},
		{Filename: "README.md", Patch: "@@ -1,0 +1,1 @@\n+go 1.25.0"},
	})
	if err != nil {
		t.Fatalf("check changes: %v", err)
	}

	if len(matches) != 1 || matches[0].File != "go.mod" {
		t.Fatalf("got %+v, want only the root go.mod", matches)
	}
}

// Only added lines count: a protected line that is removed or merely present as
// context must not trigger the rule.
func TestCheckFileChangesCountsOnlyAddedLines(t *testing.T) {
	checker := newTestChecker(t, `
repositories:
  - name: test/repo
    patterns:
      - name: error_pattern
        regex: 'log\.Error'
`)

	matches, err := checker.CheckFileChanges("test/repo", []FileChange{
		{Filename: "main.go", Patch: "@@ -1,3 +1,3 @@\n-\tlog.Error(\"removed\")\n+\tlog.Error(\"added\")\n \tlog.Error(\"context\")"},
	})
	if err != nil {
		t.Fatalf("check changes: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(matches), matches)
	}
	if !strings.Contains(matches[0].Line, "added") {
		t.Errorf("matched %q, want the added line", matches[0].Line)
	}
}

func TestCheckFileChangesRejectsUnknownRepository(t *testing.T) {
	checker := newTestChecker(t, `repositories: []`)

	if _, err := checker.CheckFileChanges("test/repo", nil); err == nil {
		t.Fatal("expected an error for an unconfigured repository")
	}
}

func TestEvaluate(t *testing.T) {
	matches := []Match{{File: "go.mod", Pattern: "go_mod_version", Line: "go 1.25.0"}}

	tests := []struct {
		name      string
		policy    string
		approvers []string
		want      bool
		wantBy    []string
	}{
		{
			name: "no matches needs no approval",
			policy: `
settings:
  min_approvals: 1
repositories:
  - name: test/repo
    approvers: ["Someone"]
`,
			want: true,
		},
		{
			name: "listed approver approves",
			policy: `
repositories:
  - name: test/repo
    approvers: ["Someone", "Other"]
`,
			approvers: []string{"Someone"},
			want:      true,
			wantBy:    []string{"Someone"},
		},
		{
			name: "unlisted approver does not approve",
			policy: `
repositories:
  - name: test/repo
    approvers: ["Someone"]
`,
			approvers: []string{"stranger"},
			want:      false,
		},
		{
			name: "approver matching stays case sensitive",
			policy: `
repositories:
  - name: test/repo
    approvers: ["XuHuaiyu"]
`,
			approvers: []string{"xuhuaiyu"},
			want:      false,
		},
		{
			name: "min approvals requires more than one",
			policy: `
settings:
  min_approvals: 2
repositories:
  - name: test/repo
    approvers: ["A", "B"]
`,
			approvers: []string{"A"},
			want:      false,
			wantBy:    []string{"A"},
		},
		{
			name: "duplicate approvals count once",
			policy: `
settings:
  min_approvals: 2
repositories:
  - name: test/repo
    approvers: ["A"]
`,
			approvers: []string{"A", "A"},
			want:      false,
			wantBy:    []string{"A"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := newTestChecker(t, tt.policy)
			matched := matches
			if tt.name == "no matches needs no approval" {
				matched = nil
			}

			verdict, err := checker.Evaluate("test/repo", matched, tt.approvers)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if verdict.Approved != tt.want {
				t.Errorf("approved = %v, want %v", verdict.Approved, tt.want)
			}
			if len(verdict.ApprovedBy) != len(tt.wantBy) {
				t.Fatalf("approved by %v, want %v", verdict.ApprovedBy, tt.wantBy)
			}
			for i := range tt.wantBy {
				if verdict.ApprovedBy[i] != tt.wantBy[i] {
					t.Errorf("approved by %v, want %v", verdict.ApprovedBy, tt.wantBy)
				}
			}
		})
	}
}

func TestEvaluateKeepsConfiguredMode(t *testing.T) {
	checker := newTestChecker(t, `
settings:
  check_behavior:
    mode: check_and_warn
repositories:
  - name: test/repo
    approvers: ["Someone"]
`)

	verdict, err := checker.Evaluate("test/repo", []Match{{File: "go.mod"}}, nil)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if verdict.Approved {
		t.Error("an unapproved protected change must not be reported as approved")
	}
	if verdict.Mode != ModeWarn {
		t.Errorf("mode = %q, want %q", verdict.Mode, ModeWarn)
	}
}

func newTestChecker(t *testing.T, body string) *Checker {
	t.Helper()
	config, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return NewChecker(config)
}
