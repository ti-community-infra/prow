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

import "fmt"

// Verdict is the decision for one pull request.
type Verdict struct {
	// Matches are the protected lines the pull request adds.
	Matches []Match
	// Required are the approvers the policy lists for the repository.
	Required []string
	// ApprovedBy are the listed approvers that approved the pull request.
	ApprovedBy []string
	// Approved is true when there is nothing to protect or MinApprovals is met.
	Approved bool
	// Minimum is the number of approvals the policy asks for.
	Minimum int
	// Mode is the configured check behaviour.
	Mode string
}

// Evaluate decides whether matches are approved by approvers.
//
// Approvers are compared verbatim against the repository whitelist, because the
// list carries the canonical casing of each GitHub login; this keeps the
// decision identical to the presubmit checker it replaces.
func (c *Checker) Evaluate(repoName string, matches []Match, approvers []string) (*Verdict, error) {
	repo := c.config.GetRepository(repoName)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found in config", repoName)
	}

	verdict := &Verdict{
		Matches:  matches,
		Required: repo.Approvers,
		Minimum:  c.config.Settings.MinApprovals,
		Mode:     c.config.Settings.CheckBehavior.Mode,
	}

	if len(matches) == 0 {
		verdict.Approved = true
		return verdict, nil
	}

	seen := map[string]bool{}
	for _, approver := range approvers {
		if seen[approver] {
			continue
		}
		for _, required := range repo.Approvers {
			if approver == required {
				seen[approver] = true
				verdict.ApprovedBy = append(verdict.ApprovedBy, approver)
				break
			}
		}
	}

	verdict.Approved = len(verdict.ApprovedBy) >= c.config.Settings.MinApprovals
	return verdict, nil
}
