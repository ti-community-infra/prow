/*
Copyright 2020 The Kubernetes Authors.

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

import (
	"fmt"
	"sort"
	"strings"
)

// CreateCherrypickBody creates the body of a cherrypick PR.
//
// The body of the source PR is copied so that issue references such as
// "Issue Number: close #123" survive, which release tooling relies on to link
// the cherry-pick to the same issue. The release note is appended only when the
// copied body does not already contain a release-note block.
func CreateCherrypickBody(num int, requestor, sourceBody, note string, chainBranches []string, kindLabels []string) string {
	cherryPickBody := fmt.Sprintf("This is an automated cherry-pick of #%d", num)
	if len(requestor) != 0 {
		cherryPickBody = fmt.Sprintf("%s\n\n/assign %s", cherryPickBody, requestor)
	}
	sourceBody = strings.TrimSpace(sourceBody)
	if len(sourceBody) != 0 {
		cherryPickBody = fmt.Sprintf("%s\n\n%s", cherryPickBody, sourceBody)
	}
	if len(note) != 0 && !strings.Contains(sourceBody, "```release-note") {
		cherryPickBody = fmt.Sprintf("%s\n\n%s", cherryPickBody, note)
	}
	if len(chainBranches) != 0 {
		cherryPickBody = fmt.Sprintf("%s\n\n/cherrypick %s", cherryPickBody, strings.Join(chainBranches, " "))
	}
	if len(kindLabels) > 0 {
		// Sort for deterministic output
		sorted := make([]string, len(kindLabels))
		copy(sorted, kindLabels)
		sort.Strings(sorted)
		var kindLines []string
		for _, name := range sorted {
			kindLines = append(kindLines, fmt.Sprintf("/kind %s", name))
		}
		cherryPickBody = fmt.Sprintf("%s\n\n%s", cherryPickBody, strings.Join(kindLines, "\n"))
	}
	return cherryPickBody
}
