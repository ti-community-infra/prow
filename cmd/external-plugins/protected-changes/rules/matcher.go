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
	"fmt"
	"strings"
)

// FileChange is one file of a pull request diff. Patch is the unified hunk
// text GitHub returns for the file, with "+"/"-"/" " prefixes and no file
// headers.
type FileChange struct {
	Filename string
	Patch    string
}

// Match is one added line that a rule flagged.
type Match struct {
	Pattern     string
	Description string
	Line        string
	File        string
}

// Checker evaluates pull request changes against a policy.
type Checker struct {
	config *Config
}

// NewChecker returns a checker for config.
func NewChecker(config *Config) *Checker {
	return &Checker{config: config}
}

// Config returns the policy the checker was built from.
func (c *Checker) Config() *Config {
	return c.config
}

// CheckFileChanges returns every added line that matches a rule configured for
// repoName. Files excluded at repository level are skipped before any rule is
// evaluated; each rule may additionally limit itself with Files and Excludes.
func (c *Checker) CheckFileChanges(repoName string, changes []FileChange) ([]Match, error) {
	repo := c.config.GetRepository(repoName)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found in config", repoName)
	}

	var matches []Match
	for _, change := range changes {
		if matchesAnyPattern(change.Filename, repo.Excludes) {
			continue
		}

		for _, line := range addedLines(change.Patch) {
			for i := range repo.Patterns {
				pattern := &repo.Patterns[i]
				if !matchesFileScope(change.Filename, pattern.Files) {
					continue
				}
				if matchesAnyPattern(change.Filename, pattern.Excludes) {
					continue
				}
				if pattern.compiled == nil || !pattern.compiled.MatchString(line) {
					continue
				}

				matches = append(matches, Match{
					Pattern:     pattern.Name,
					Description: pattern.Description,
					Line:        strings.TrimSpace(line),
					File:        change.Filename,
				})
			}
		}
	}

	return matches, nil
}

// addedLines extracts the added lines of a file patch. A patch has no file
// headers, so every line starting with "+" is an addition; hunk headers start
// with "@@" and context/removed lines with " " and "-".
func addedLines(patch string) []string {
	var lines []string
	for line := range strings.SplitSeq(patch, "\n") {
		if !strings.HasPrefix(line, "+") {
			continue
		}
		lines = append(lines, strings.TrimPrefix(line, "+"))
	}
	return lines
}
