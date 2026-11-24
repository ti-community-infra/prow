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

// Package rules holds the protected-change policy schema and the matcher that
// decides whether a pull request touches a protected area of a repository.
//
// It is a port of the error-log-review checker that used to run inside the
// pull-error-log-review presubmit jobs, plus the file-scoped pattern semantics
// (Pattern.Files) that let a rule apply to specific files only.
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Check behaviour modes.
const (
	// ModeFail reports a failure when a protected change has no approval.
	ModeFail = "check_and_fail"
	// ModeWarn reports success but still explains what lacks approval.
	ModeWarn = "check_and_warn"
)

// Pattern is one rule: a regular expression evaluated against added lines,
// optionally limited to a set of files and excluding others.
type Pattern struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Regex       string   `yaml:"regex"`
	Files       []string `yaml:"files,omitempty"`
	Excludes    []string `yaml:"excludes,omitempty"`

	compiled *regexp.Regexp
}

// Repository is the policy for one repository.
type Repository struct {
	Name      string    `yaml:"name"`
	Patterns  []Pattern `yaml:"patterns"`
	Approvers []string  `yaml:"approvers"`
	Excludes  []string  `yaml:"excludes,omitempty"`
}

// CheckBehavior is kept in the schema because the existing policy files use it.
type CheckBehavior struct {
	Mode string `yaml:"mode"`
}

// Settings are the policy-wide options.
type Settings struct {
	MinApprovals                 int           `yaml:"min_approvals"`
	RequireRepoSpecificApprovers bool          `yaml:"require_repo_specific_approvers"`
	CheckBehavior                CheckBehavior `yaml:"check_behavior"`
	// Hint is an optional message shown to contributors when approval is required.
	Hint string `yaml:"hint,omitempty"`
}

// Config is the whole policy document.
type Config struct {
	Repositories []Repository `yaml:"repositories"`
	Settings     Settings     `yaml:"settings"`

	digest string
}

// LoadConfig reads and compiles the policy document.
func LoadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := config.Compile(); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(data)
	config.digest = hex.EncodeToString(sum[:])

	return &config, nil
}

// Compile applies the fail-closed defaults and compiles every rule.
//
// A zero threshold means "no approval needed" and an empty mode means "do not
// fail the check", so a missing or truncated settings block would silently turn
// the gate off; both fall back to the strict value instead.
func (c *Config) Compile() error {
	if c.Settings.MinApprovals <= 0 {
		c.Settings.MinApprovals = 1
	}
	if strings.TrimSpace(c.Settings.CheckBehavior.Mode) == "" {
		c.Settings.CheckBehavior.Mode = ModeFail
	}
	if mode := c.Settings.CheckBehavior.Mode; mode != ModeFail && mode != ModeWarn {
		return fmt.Errorf("unknown check_behavior.mode %q, want %q or %q", mode, ModeFail, ModeWarn)
	}

	for i := range c.Repositories {
		for j := range c.Repositories[i].Patterns {
			pattern := &c.Repositories[i].Patterns[j]
			compiled, err := regexp.Compile(pattern.Regex)
			if err != nil {
				return fmt.Errorf("failed to compile regex for %s/%s: %w", c.Repositories[i].Name, pattern.Name, err)
			}
			pattern.compiled = compiled
		}
	}

	return nil
}

// Hash identifies the policy content. It is part of the idempotency key, so a
// policy change invalidates every cached evaluation.
func (c *Config) Hash() string {
	return c.digest
}

// GetRepository returns the policy for name, or nil when the repository is not
// configured.
func (c *Config) GetRepository(name string) *Repository {
	for i := range c.Repositories {
		if strings.EqualFold(c.Repositories[i].Name, name) {
			return &c.Repositories[i]
		}
	}
	return nil
}

// matchesAnyPattern reports whether filePath matches any of the glob patterns.
// The "**" handling is intentionally the one the presubmit checker used, so a
// migrated policy keeps excluding exactly the same files.
func matchesAnyPattern(filePath string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, filePath)
		if err != nil {
			// If pattern is invalid, skip it
			continue
		}
		if matched {
			return true
		}
		// Also try matching with /** pattern (e.g., tests/** should match tests/a/b/c.go)
		if matched, _ := filepath.Match(pattern, filepath.Dir(filePath)+"/"); matched {
			return true
		}
		// Check if the pattern ends with /** and the path starts with the prefix
		if len(pattern) > 3 && pattern[len(pattern)-3:] == "/**" {
			prefix := pattern[:len(pattern)-3]
			if filePath == prefix || len(filePath) > len(prefix) && filePath[:len(prefix)+1] == prefix+"/" {
				return true
			}
		}
	}
	return false
}

// matchesFileScope reports whether filePath is in scope for a file-limited
// pattern. Patterns without a path separator match against the file basename,
// so "go.mod" applies to nested modules like "tools/foo/go.mod" as well as to
// the repository root.
func matchesFileScope(filePath string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}

	base := filepath.Base(filePath)
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") {
			if matchesAnyPattern(filePath, []string{pattern}) {
				return true
			}
			continue
		}

		matched, err := filepath.Match(pattern, base)
		if err != nil {
			continue
		}
		if matched {
			return true
		}
	}

	return false
}
