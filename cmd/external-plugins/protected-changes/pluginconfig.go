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
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	pluginconfig "sigs.k8s.io/prow/pkg/plugins"
)

// approvalOptions mirrors the approve plugin's configuration for one repository.
// They are read from the plugin config hook already uses, so the verdict keeps
// matching the approve plugin instead of duplicating its settings.
type approvalOptions struct {
	// considerReviewState counts an APPROVE review as an approval and a
	// CHANGES_REQUESTED review as its withdrawal (approve.ignore_review_state).
	considerReviewState bool
	// lgtmActsAsApprove counts /lgtm as an approval
	// (approve.lgtm_acts_as_approve).
	lgtmActsAsApprove bool
}

// pluginConfigSource serves the approve plugin's configuration.
//
// The file is the ConfigMap hook mounts into every external plugin, so this
// needs no flag in the deployment; when it is absent the approve plugin's own
// defaults apply.
type pluginConfigSource struct {
	config *pluginconfig.Configuration
}

func loadPluginConfig(path string) (*pluginConfigSource, error) {
	config := &pluginconfig.Configuration{}
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, config); err != nil {
				return nil, fmt.Errorf("failed to parse the plugin config %s: %w", path, err)
			}
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("failed to read the plugin config %s: %w", path, err)
		}
	}
	return &pluginConfigSource{config: config}, nil
}

// approvalOptions returns the effective approve settings for a repository.
func (p *pluginConfigSource) approvalOptions(org, repo string) approvalOptions {
	approve := p.config.ApproveFor(org, repo)
	return approvalOptions{
		considerReviewState: approve.ConsiderReviewState(),
		lgtmActsAsApprove:   approve.LgtmActsAsApprove,
	}
}
