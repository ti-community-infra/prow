/*
Copyright 2026 The TiCommunity Authors.

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

// Package config holds the per-repository configuration of the cherry-pick
// external plugin.
//
// The configuration lives under the `cherry-pick` key of the external plugins
// config file. Command line flags act as process-wide defaults; an entry in
// this file overrides them for the repositories it matches. Entries are applied
// field by field: the flags first, then a matching organization entry, then a
// matching repository entry.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

// OnConflict is the policy applied when a cherry-pick cannot be applied cleanly.
type OnConflict string

const (
	// OnConflictIssue comments the failure, opens an issue assigned to the
	// requester and stops.
	OnConflictIssue OnConflict = "issue"
	// OnConflictHold opens the cherry-pick PR with conflict markers, holds it
	// and asks the requester to resolve the conflicts.
	OnConflictHold OnConflict = "hold"
	// OnConflictFail comments the failure and stops without a branch or PR.
	OnConflictFail OnConflict = "fail"
)

// DefaultLabelPrefix is the flag default for label initiated cherry-picks.
const DefaultLabelPrefix = "needs-cherry-pick-"

// reloadInterval is the interval at which Start reloads the config file.
var reloadInterval = 1 * time.Minute

// Defaults are the process-wide values taken from command line flags.
type Defaults struct {
	LabelPrefix        string
	AllowAll           bool
	UseProwAssignments bool
	Labels             []string
	ForkOwner          string
	OnConflict         OnConflict
}

// Options are the effective settings for one repository.
type Options struct {
	LabelPrefix                        string
	AllowAll                           bool
	UseProwAssignments                 bool
	Labels                             []string
	ForkOwner                          string
	PickedLabelPrefix                  string
	ExcludeLabels                      []string
	OnConflict                         OnConflict
	CopyIssueNumbersFromSquashedCommit bool
}

// Config is the top-level serialization target of the plugin configuration.
type Config struct {
	// CherryPick is the list of per-repository cherry-pick settings.
	CherryPick []RepoConfig `json:"cherry-pick,omitempty"`
}

// RepoConfig is the configuration of one entry.
//
// Fields that must be distinguishable from their zero value are pointers, so
// that an explicit `false` or empty value overrides a flag default while an
// omitted field keeps it.
type RepoConfig struct {
	// Repos is either of the form org/repo or just org.
	Repos []string `json:"repos,omitempty"`
	// LabelPrefix is the prefix used for label initiated cherry-picks.
	LabelPrefix *string `json:"label_prefix,omitempty"`
	// AllowAll skips the organization membership check.
	AllowAll *bool `json:"allow_all,omitempty"`
	// UseProwAssignments assigns the requester to the cherry-pick PR.
	UseProwAssignments *bool `json:"use_prow_assignments,omitempty"`
	// Labels are added to every cherry-pick PR.
	Labels []string `json:"labels,omitempty"`
	// ForkOwner overrides the process-wide fork owner. Empty pushes the
	// cherry-pick branch into the base repository.
	ForkOwner *string `json:"fork_owner,omitempty"`
	// PickedLabelPrefix is added to the cherry-pick PR as `<prefix><target>`;
	// empty disables it.
	PickedLabelPrefix *string `json:"picked_label_prefix,omitempty"`
	// ExcludeLabels are not copied from the source PR.
	ExcludeLabels []string `json:"exclude_labels,omitempty"`
	// OnConflict is one of OnConflictIssue, OnConflictHold, OnConflictFail.
	OnConflict *OnConflict `json:"on_conflict,omitempty"`
	// CopyIssueNumbersFromSquashedCommit copies issue references from the
	// squashed commit message into the cherry-pick commit message.
	CopyIssueNumbersFromSquashedCommit *bool `json:"copy_issue_numbers_from_squashed_commit,omitempty"`
}

// Parse decodes the cherry-pick section of the external plugins config file.
//
// The file is shared with the other external plugins, so its other top-level
// sections are ignored. Unknown fields inside the cherry-pick section are
// rejected. Parse does not validate the decoded values; call Validate for that.
func Parse(data []byte) (*Config, error) {
	sections := map[string]json.RawMessage{}
	if err := yaml.Unmarshal(data, &sections); err != nil {
		return nil, fmt.Errorf("failed to decode the external plugin config: %w", err)
	}

	cfg := &Config{}
	section, ok := sections["cherry-pick"]
	if !ok {
		return cfg, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(section))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg.CherryPick); err != nil {
		return nil, fmt.Errorf("failed to decode cherry-pick config: %w", err)
	}
	return cfg, nil
}

// LoadFile reads, strictly decodes and validates a configuration file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateFile is LoadFile without returning the config; it exists for the
// --validate-config mode.
func ValidateFile(path string) error {
	_, err := LoadFile(path)
	return err
}

// Validate checks the configuration for structural errors.
func (c *Config) Validate() error {
	seen := map[string]int{}
	for i, entry := range c.CherryPick {
		if len(entry.Repos) == 0 {
			return fmt.Errorf("entry %d: repos must not be empty", i)
		}
		for _, repo := range entry.Repos {
			if err := validateRepoName(repo); err != nil {
				return fmt.Errorf("entry %d: %w", i, err)
			}
			if first, ok := seen[repo]; ok {
				return fmt.Errorf("duplicate repos entry %q in entries %d and %d", repo, first, i)
			}
			seen[repo] = i
		}
		if entry.ForkOwner != nil && strings.Contains(*entry.ForkOwner, "/") {
			return fmt.Errorf("entry %d: invalid fork_owner %q: must be a user or organization, not a repository", i, *entry.ForkOwner)
		}
		if entry.OnConflict != nil {
			switch *entry.OnConflict {
			case OnConflictIssue, OnConflictHold, OnConflictFail:
			default:
				return fmt.Errorf("entry %d: invalid on_conflict %q, must be one of %q, %q, %q",
					i, *entry.OnConflict, OnConflictIssue, OnConflictHold, OnConflictFail)
			}
		}
	}
	return nil
}

func validateRepoName(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) > 2 {
		return fmt.Errorf("invalid repos entry %q: must be org or org/repo", repo)
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("invalid repos entry %q: must be org or org/repo", repo)
		}
	}
	return nil
}

// Options resolves the effective settings for a repository.
func (c *Config) Options(org, repo string, defaults Defaults) Options {
	out := Options{
		LabelPrefix:        defaults.LabelPrefix,
		AllowAll:           defaults.AllowAll,
		UseProwAssignments: defaults.UseProwAssignments,
		Labels:             append([]string(nil), defaults.Labels...),
		ForkOwner:          defaults.ForkOwner,
		OnConflict:         defaults.OnConflict,
	}
	if c == nil {
		return out
	}
	for _, entry := range c.CherryPick {
		if entry.matchesOrg(org) {
			entry.apply(&out)
		}
	}
	for _, entry := range c.CherryPick {
		if entry.matchesRepo(org, repo) {
			entry.apply(&out)
		}
	}
	return out
}

func (c *RepoConfig) matchesOrg(org string) bool {
	return slices.Contains(c.Repos, org)
}

func (c *RepoConfig) matchesRepo(org, repo string) bool {
	return slices.Contains(c.Repos, org+"/"+repo)
}

func (c *RepoConfig) apply(out *Options) {
	if c.LabelPrefix != nil {
		out.LabelPrefix = *c.LabelPrefix
	}
	if c.AllowAll != nil {
		out.AllowAll = *c.AllowAll
	}
	if c.UseProwAssignments != nil {
		out.UseProwAssignments = *c.UseProwAssignments
	}
	if c.Labels != nil {
		out.Labels = append([]string(nil), c.Labels...)
	}
	if c.ForkOwner != nil {
		out.ForkOwner = *c.ForkOwner
	}
	if c.PickedLabelPrefix != nil {
		out.PickedLabelPrefix = *c.PickedLabelPrefix
	}
	if c.ExcludeLabels != nil {
		out.ExcludeLabels = append([]string(nil), c.ExcludeLabels...)
	}
	if c.OnConflict != nil {
		out.OnConflict = *c.OnConflict
	}
	if c.CopyIssueNumbersFromSquashedCommit != nil {
		out.CopyIssueNumbersFromSquashedCommit = *c.CopyIssueNumbersFromSquashedCommit
	}
}

// Agent holds the current configuration and reloads it periodically.
type Agent struct {
	mu  sync.RWMutex
	cfg *Config
}

// NewAgent returns an empty agent.
func NewAgent() *Agent {
	return &Agent{}
}

// Load reads the file and replaces the current configuration on success. On
// error the previous configuration is kept.
func (a *Agent) Load(path string) error {
	cfg, err := LoadFile(path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = cfg
	return nil
}

// Start loads the configuration and keeps reloading it. The first load must
// succeed; later failures are logged and the last good configuration is kept.
func (a *Agent) Start(path string) error {
	if err := a.Load(path); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(reloadInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := a.Load(path); err != nil {
				logrus.WithField("path", path).WithError(err).Error("Error reloading external plugin config.")
			}
		}
	}()
	return nil
}

// Config returns the current configuration, or nil before the first load.
func (a *Agent) Config() *Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}
