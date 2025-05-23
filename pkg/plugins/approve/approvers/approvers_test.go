/*
Copyright 2016 The Kubernetes Authors.

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

package approvers

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/prow/pkg/plugins/ownersconfig"
)

func TestUnapprovedFiles(t *testing.T) {
	rootApprovers := sets.New[string]("Alice", "Bob")
	aApprovers := sets.New[string]("Art", "Anne")
	bApprovers := sets.New[string]("Bill", "Ben", "Barbara")
	cApprovers := sets.New[string]("Chris", "Carol")
	dApprovers := sets.New[string]("David", "Dan", "Debbie")
	eApprovers := sets.New[string]("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.Set[string]{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
	}
	tests := []struct {
		testName           string
		filenames          []string
		currentlyApproved  sets.Set[string]
		expectedUnapproved sets.Set[string]
	}{
		{
			testName:           "Empty PR",
			filenames:          []string{},
			currentlyApproved:  sets.New[string](),
			expectedUnapproved: sets.New[string](),
		},
		{
			testName:           "Single Root File PR Approved",
			filenames:          []string{"kubernetes.go"},
			currentlyApproved:  sets.New[string](sets.List(rootApprovers)[0]),
			expectedUnapproved: sets.New[string](),
		},
		{
			testName:           "Single Root File PR No One Approved",
			filenames:          []string{"kubernetes.go"},
			currentlyApproved:  sets.New[string](),
			expectedUnapproved: sets.New[string](""),
		},
		{
			testName:           "B Only UnApproved",
			currentlyApproved:  bApprovers,
			expectedUnapproved: sets.New[string](),
		},
		{
			testName:           "B Files Approved at Root",
			filenames:          []string{"b/test.go", "b/test_1.go"},
			currentlyApproved:  rootApprovers,
			expectedUnapproved: sets.New[string](),
		},
		{
			testName:           "B Only UnApproved",
			filenames:          []string{"b/test_1.go", "b/test.go"},
			currentlyApproved:  sets.New[string](),
			expectedUnapproved: sets.New[string]("b"),
		},
		{
			testName:           "Combo and Other; Neither Approved",
			filenames:          []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved:  sets.New[string](),
			expectedUnapproved: sets.New[string]("a/combo", "a/d"),
		},
		{
			testName:           "Combo and Other; Combo Approved",
			filenames:          []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved:  edcApprovers.Difference(dApprovers),
			expectedUnapproved: sets.New[string]("a/d"),
		},
		{
			testName:           "Combo and Other; Both Approved",
			filenames:          []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved:  edcApprovers.Intersection(dApprovers),
			expectedUnapproved: sets.New[string](),
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: TestSeed, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = false
		for approver := range test.currentlyApproved {
			testApprovers.AddApprover(approver, "REFERENCE", false)
		}
		calculated := testApprovers.UnapprovedFiles()
		if !test.expectedUnapproved.Equal(calculated) {
			t.Errorf("Failed for test %v.  Expected unapproved files: %v. Found %v", test.testName, test.expectedUnapproved, calculated)
		}
	}
}

func TestGetFiles(t *testing.T) {
	rootApprovers := sets.New[string]("Alice", "Bob")
	aApprovers := sets.New[string]("Art", "Anne")
	bApprovers := sets.New[string]("Bill", "Ben", "Barbara")
	cApprovers := sets.New[string]("Chris", "Carol")
	dApprovers := sets.New[string]("David", "Dan", "Debbie")
	eApprovers := sets.New[string]("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.Set[string]{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
	}
	tests := []struct {
		testName          string
		filenames         []string
		currentlyApproved sets.Set[string]
		expectedFiles     []File
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: sets.New[string](),
			expectedFiles:     []File{},
		},
		{
			testName:          "Single Root File PR Approved",
			filenames:         []string{"kubernetes.go"},
			currentlyApproved: sets.New[string](sets.List(rootApprovers)[0]),
			expectedFiles:     []File{ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "", ownersconfig.DefaultOwnersFile, sets.New[string](sets.List(rootApprovers)[0]), "master"}},
		},
		{
			testName:          "Single File PR in B No One Approved",
			filenames:         []string{"b/test.go"},
			currentlyApproved: sets.New[string](),
			expectedFiles:     []File{UnapprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "b", ownersconfig.DefaultOwnersFile, nil, "master"}},
		},
		{
			testName:          "Single File PR in B Fully Approved",
			filenames:         []string{"b/test.go"},
			currentlyApproved: bApprovers,
			expectedFiles:     []File{ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "b", ownersconfig.DefaultOwnersFile, bApprovers, "master"}},
		},
		{
			testName:          "Single Root File PR No One Approved",
			filenames:         []string{"kubernetes.go"},
			currentlyApproved: sets.New[string](),
			expectedFiles:     []File{UnapprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "", ownersconfig.DefaultOwnersFile, nil, "master"}},
		},
		{
			testName:          "Combo and Other; Neither Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved: sets.New[string](),
			expectedFiles: []File{
				UnapprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/combo", ownersconfig.DefaultOwnersFile, nil, "master"},
				UnapprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/d", ownersconfig.DefaultOwnersFile, nil, "master"},
			},
		},
		{
			testName:          "Combo and Other; Combo Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved: eApprovers,
			expectedFiles: []File{
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/combo", ownersconfig.DefaultOwnersFile, eApprovers, "master"},
				UnapprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/d", ownersconfig.DefaultOwnersFile, nil, "master"},
			},
		},
		{
			testName:          "Combo and Other; Both Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			currentlyApproved: edcApprovers.Intersection(dApprovers),
			expectedFiles: []File{
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/combo", ownersconfig.DefaultOwnersFile, edcApprovers.Intersection(dApprovers), "master"},
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/d", ownersconfig.DefaultOwnersFile, edcApprovers.Intersection(dApprovers), "master"},
			},
		},
		{
			testName:          "Combo, C, D; Combo and C Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go", "c/test"},
			currentlyApproved: cApprovers,
			expectedFiles: []File{
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/combo", ownersconfig.DefaultOwnersFile, cApprovers, "master"},
				UnapprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a/d", ownersconfig.DefaultOwnersFile, nil, "master"},
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "c", ownersconfig.DefaultOwnersFile, cApprovers, "master"},
			},
		},
		{
			testName:          "Files Approved Multiple times",
			filenames:         []string{"a/test.go", "a/d/test.go", "b/test"},
			currentlyApproved: rootApprovers.Union(aApprovers).Union(bApprovers),
			expectedFiles: []File{
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "a", ownersconfig.DefaultOwnersFile, rootApprovers.Union(aApprovers), "master"},
				ApprovedFile{&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "b", ownersconfig.DefaultOwnersFile, rootApprovers.Union(bApprovers), "master"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.testName, func(t *testing.T) {
			testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: TestSeed, log: logrus.WithField("plugin", "some_plugin")})
			testApprovers.RequireIssue = false
			for approver := range test.currentlyApproved {
				testApprovers.AddApprover(approver, "REFERENCE", false)
			}
			calculated := testApprovers.GetFiles(&url.URL{Scheme: "https", Host: "github.com", Path: "org/repo"}, "master")
			if diff := cmp.Diff(test.expectedFiles, calculated, cmpopts.EquateEmpty(), cmp.Exporter(func(_ reflect.Type) bool { return true })); diff != "" {
				t.Errorf("expected files differ from actual: %s", diff)
			}
		})
	}
}

func TestGetCCs(t *testing.T) {
	rootApprovers := sets.New[string]("Alice", "Bob")
	aApprovers := sets.New[string]("Art", "Anne")
	bApprovers := sets.New[string]("Bill", "Ben", "Barbara")
	cApprovers := sets.New[string]("Chris", "Carol")
	dApprovers := sets.New[string]("David", "Dan", "Debbie")
	eApprovers := sets.New[string]("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.Set[string]{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
	}
	tests := []struct {
		testName          string
		filenames         []string
		currentlyApproved sets.Set[string]
		// testSeed affects who is chosen for CC
		testSeed  int64
		assignees []string
		// order matters for CCs
		expectedCCs          []string
		expectedAssignedCCs  []string
		expectedSuggestedCCs []string
	}{
		{
			testName:             "Empty PR",
			filenames:            []string{},
			currentlyApproved:    sets.New[string](),
			testSeed:             0,
			expectedCCs:          []string{},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{},
		},
		{
			testName:             "Single Root FFile PR Approved",
			filenames:            []string{"kubernetes.go"},
			currentlyApproved:    sets.New[string](sets.List(rootApprovers)[0]),
			testSeed:             13,
			expectedCCs:          []string{},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{},
		},
		{
			testName:             "Single Root File PR Unapproved Seed = 13",
			filenames:            []string{"kubernetes.go"},
			currentlyApproved:    sets.New[string](),
			testSeed:             13,
			expectedCCs:          []string{"alice"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"alice"},
		},
		{
			testName:             "Single Root File PR No One Seed = 10",
			filenames:            []string{"kubernetes.go"},
			testSeed:             10,
			currentlyApproved:    sets.New[string](),
			expectedCCs:          []string{"bob"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"bob"},
		},
		{
			testName:             "Combo and Other; Neither Approved",
			filenames:            []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:             0,
			currentlyApproved:    sets.New[string](),
			expectedCCs:          []string{"dan"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"dan"},
		},
		{
			testName:             "Combo and Other; Combo Approved",
			filenames:            []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:             0,
			currentlyApproved:    eApprovers,
			expectedCCs:          []string{"dan"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"dan"},
		},
		{
			testName:             "Combo and Other; Both Approved",
			filenames:            []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:             0,
			currentlyApproved:    dApprovers, // dApprovers can approve combo and d directory
			expectedCCs:          []string{},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{},
		},
		{
			testName:          "Combo, C, D; None Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			// chris can approve c and combo, debbie can approve d
			expectedCCs:          []string{"chris", "debbie"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"chris", "debbie"},
		},
		{
			testName:          "A, B, C; Nothing Approved",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			// Need an approver from each of the three owners files
			expectedCCs:          []string{"anne", "bill", "carol"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"anne", "bill", "carol"},
		},
		{
			testName:  "A, B, C; Partially approved by non-suggested approvers",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			// Approvers are valid approvers, but not the one we would suggest
			currentlyApproved: sets.New[string]("Art", "Ben"),
			// We don't suggest approvers for a and b, only for unapproved c.
			expectedCCs:          []string{"carol"},
			expectedAssignedCCs:  []string{},
			expectedSuggestedCCs: []string{"carol"},
		},
		{
			testName:  "A, B, C; Nothing approved, but assignees can approve",
			filenames: []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:  0,
			// Approvers are valid approvers, but not the one we would suggest
			currentlyApproved: sets.New[string](),
			assignees:         []string{"Art", "Ben"},
			// We suggest assigned people rather than "suggested" people
			// Suggested would be "Anne", "Bill", "Carol" if no one was assigned.
			expectedCCs:          []string{"art", "ben", "carol"},
			expectedAssignedCCs:  []string{"art", "ben"},
			expectedSuggestedCCs: []string{"carol"},
		},
		{
			testName:          "A, B, C; Nothing approved, but SOME assignees can approve",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			// Assignees are a mix of potential approvers and random people
			assignees: []string{"Art", "Ben", "John", "Jack"},
			// We suggest assigned people rather than "suggested" people
			expectedCCs:          []string{"art", "ben", "carol"},
			expectedAssignedCCs:  []string{"art", "ben"},
			expectedSuggestedCCs: []string{"carol"},
		},
		{
			testName:          "Assignee is top OWNER, No one has approved",
			filenames:         []string{"a/test.go"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			// Assignee is a root approver
			assignees:            []string{"alice"},
			expectedCCs:          []string{"alice"},
			expectedAssignedCCs:  []string{"alice"},
			expectedSuggestedCCs: []string{},
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: test.testSeed, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = false
		for approver := range test.currentlyApproved {
			testApprovers.AddApprover(approver, "REFERENCE", false)
		}
		testApprovers.AddAssignees(test.assignees...)
		calculated := testApprovers.GetCCs()
		if !reflect.DeepEqual(test.expectedCCs, calculated) {
			t.Errorf("Failed for test %v.  Expected CCs: %v. Found %v", test.testName, test.expectedCCs, calculated)
		}
		calculated = testApprovers.AssignedCCs()
		if !reflect.DeepEqual(test.expectedAssignedCCs, calculated) {
			t.Errorf("Failed for test %v.  Expected AssignedCCs: %v. Found %v", test.testName, test.expectedAssignedCCs, calculated)
		}
		calculated = testApprovers.SuggestedCCs()
		if !reflect.DeepEqual(test.expectedSuggestedCCs, calculated) {
			t.Errorf("Failed for test %v.  Expected SuggestedCCs: %v. Found %v", test.testName, test.expectedSuggestedCCs, calculated)
		}
	}
}

func TestIsApproved(t *testing.T) {
	rootApprovers := sets.New[string]("Alice", "Bob")
	aApprovers := sets.New[string]("Art", "Anne")
	bApprovers := sets.New[string]("Bill", "Ben", "Barbara")
	cApprovers := sets.New[string]("Chris", "Carol")
	dApprovers := sets.New[string]("David", "Dan", "Debbie")
	eApprovers := sets.New[string]("Eve", "Erin")
	edcApprovers := eApprovers.Union(dApprovers).Union(cApprovers)
	FakeRepoMap := map[string]sets.Set[string]{
		"":        rootApprovers,
		"a":       aApprovers,
		"b":       bApprovers,
		"c":       cApprovers,
		"a/d":     dApprovers,
		"a/combo": edcApprovers,
		"d":       {},
	}
	tests := []struct {
		testName                        string
		filenames                       []string
		autoApproveUnownedSubfoldersMap map[string]bool
		currentlyApproved               sets.Set[string]
		testSeed                        int64
		isApproved                      bool
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: sets.New[string](),
			testSeed:          0,
			isApproved:        false,
		},
		{
			testName:          "Single Root File PR Approved",
			filenames:         []string{"kubernetes.go"},
			currentlyApproved: sets.New[string](sets.List(rootApprovers)[0]),
			testSeed:          3,
			isApproved:        true,
		},
		{
			testName:          "Single Root File PR No One Approved",
			filenames:         []string{"kubernetes.go"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			isApproved:        false,
		},
		{
			testName:          "Combo and Other; Neither Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			isApproved:        false,
		},
		{
			testName:          "Combo and Other; Both Approved",
			filenames:         []string{"a/combo/test.go", "a/d/test.go"},
			testSeed:          0,
			currentlyApproved: edcApprovers.Intersection(dApprovers),
			isApproved:        true,
		},
		{
			testName:          "A, B, C; Nothing Approved",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: sets.New[string](),
			isApproved:        false,
		},
		{
			testName:          "A, B, C; Partially Approved",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: aApprovers.Union(bApprovers),
			isApproved:        false,
		},
		{
			testName:          "A, B, C; Approved At the Root",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: rootApprovers,
			isApproved:        true,
		},
		{
			testName:          "A, B, C; Approved At the Leaves",
			filenames:         []string{"a/test.go", "b/test.go", "c/test"},
			testSeed:          0,
			currentlyApproved: sets.New[string]("Anne", "Ben", "Carol"),
			isApproved:        true,
		},
		{
			testName:                        "File in folder with AutoApproveUnownedSubfolders does not get approved",
			filenames:                       []string{"a/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders gets approved",
			filenames:                       []string{"a/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      true,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders whose ownersfile has no approvers gets approved",
			filenames:                       []string{"d/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"d": true},
			isApproved:                      true,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders and other unapproved file does not get approved",
			filenames:                       []string{"b/unapproved.go", "a/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
		{
			testName:                        "Subfolder in folder with AutoApproveUnownedSubfolders and approved file, approved",
			filenames:                       []string{"b/approved.go", "a/new-folder/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			currentlyApproved:               sets.New[string](sets.List(bApprovers)[0]),
			isApproved:                      true,
		},
		{
			testName:                        "Nested subfolder in folder with AutoApproveUnownedSubfolders gets approved",
			filenames:                       []string{"a/new-folder/child/grandchild/test.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      true,
		},
		{
			testName:                        "Change in folder with Owners whose parent has AutoApproveUnownedSubfolders does not get approved",
			filenames:                       []string{"a/d/new-file.go"},
			autoApproveUnownedSubfoldersMap: map[string]bool{"a": true},
			isApproved:                      false,
		},
	}

	for _, test := range tests {
		t.Run(test.testName, func(t *testing.T) {
			testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap, func(fr *FakeRepo) { fr.autoApproveUnownedSubfolders = test.autoApproveUnownedSubfoldersMap }), seed: test.testSeed, log: logrus.WithField("plugin", "some_plugin")})
			for approver := range test.currentlyApproved {
				testApprovers.AddApprover(approver, "REFERENCE", false)
			}
			calculated := testApprovers.IsApproved()
			if test.isApproved != calculated {
				t.Errorf("Failed for test %v.  Expected Approval Status: %v. Found %v", test.testName, test.isApproved, calculated)
			}
		})
	}
}

func TestIsApprovedWithIssue(t *testing.T) {
	aApprovers := sets.New[string]("Author", "Anne", "Carl")
	bApprovers := sets.New[string]("Bill", "Carl")
	FakeRepoMap := map[string]sets.Set[string]{"a": aApprovers, "b": bApprovers}
	tests := []struct {
		testName          string
		filenames         []string
		currentlyApproved map[string]bool
		associatedIssue   int
		isApproved        bool
	}{
		{
			testName:          "Empty PR",
			filenames:         []string{},
			currentlyApproved: map[string]bool{},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Single file missing issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{"Carl": false},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Single file no-issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{"Carl": true},
			associatedIssue:   0,
			isApproved:        true,
		},
		{
			testName:          "Single file with issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{"Carl": false},
			associatedIssue:   100,
			isApproved:        true,
		},
		{
			testName:          "Two files missing issue",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Carl": false},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Two files no-issue",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Carl": true},
			associatedIssue:   0,
			isApproved:        true,
		},
		{
			testName:          "Two files two no-issue two approvers",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Anne": true, "Bill": true},
			associatedIssue:   0,
			isApproved:        true,
		},
		{
			testName:          "Two files one no-issue two approvers",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Anne": true, "Bill": false},
			associatedIssue:   0,
			isApproved:        true,
		},
		{
			testName:          "Two files missing issue two approvers",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Anne": false, "Bill": false},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Self approval (implicit) missing issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Self approval (implicit) with issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{},
			associatedIssue:   10,
			isApproved:        true,
		},
		{
			testName:          "Self approval (explicit) missing issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{"Author": false},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Self approval (explicit) no-issue",
			filenames:         []string{"a/file.go"},
			currentlyApproved: map[string]bool{"Author": true},
			associatedIssue:   0,
			isApproved:        true,
		},
		{
			testName:          "Self approval (explicit) missing issue, two files",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Author": false, "Bill": false},
			associatedIssue:   0,
			isApproved:        false,
		},
		{
			testName:          "Self approval (explicit) no-issue from author, two files",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Author": true, "Bill": false},
			associatedIssue:   0,
			isApproved:        true,
		},
		{
			testName:          "Self approval (explicit) no-issue from friend, two files",
			filenames:         []string{"a/file.go", "b/file2.go"},
			currentlyApproved: map[string]bool{"Author": false, "Bill": true},
			associatedIssue:   0,
			isApproved:        true,
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(FakeRepoMap), seed: 0, log: logrus.WithField("plugin", "some_plugin")})
		testApprovers.RequireIssue = true
		testApprovers.AssociatedIssue = test.associatedIssue
		for approver, noissue := range test.currentlyApproved {
			testApprovers.AddApprover(approver, "REFERENCE", noissue)
		}
		testApprovers.AddAuthorSelfApprover("Author", "REFERENCE", false)
		calculated := testApprovers.IsApproved()
		if test.isApproved != calculated {
			t.Errorf("Failed for test %v.  Expected Approval Status: %v. Found %v", test.testName, test.isApproved, calculated)
		}
	}
}

func TestGetFilesApprovers(t *testing.T) {
	tests := []struct {
		testName       string
		filenames      []string
		approvers      []string
		owners         map[string]sets.Set[string]
		expectedStatus map[string]sets.Set[string]
	}{
		{
			testName:       "Empty PR",
			filenames:      []string{},
			approvers:      []string{},
			owners:         map[string]sets.Set[string]{},
			expectedStatus: map[string]sets.Set[string]{},
		},
		{
			testName:       "No approvers",
			filenames:      []string{"a/a", "c"},
			approvers:      []string{},
			owners:         map[string]sets.Set[string]{"": sets.New[string]("RootOwner")},
			expectedStatus: map[string]sets.Set[string]{"": {}},
		},
		{
			testName: "Approvers approves some",
			filenames: []string{
				"a/a",
				"c/c",
			},
			approvers: []string{"CApprover"},
			owners: map[string]sets.Set[string]{
				"a": sets.New[string]("AApprover"),
				"c": sets.New[string]("CApprover"),
			},
			expectedStatus: map[string]sets.Set[string]{
				"a": {},
				"c": sets.New[string]("CApprover"),
			},
		},
		{
			testName: "Multiple approvers",
			filenames: []string{
				"a/a",
				"c/c",
			},
			approvers: []string{"RootApprover", "CApprover"},
			owners: map[string]sets.Set[string]{
				"":  sets.New[string]("RootApprover"),
				"a": sets.New[string]("AApprover"),
				"c": sets.New[string]("CApprover"),
			},
			expectedStatus: map[string]sets.Set[string]{
				"a": sets.New[string]("RootApprover"),
				"c": sets.New[string]("RootApprover", "CApprover"),
			},
		},
		{
			testName:       "Case-insensitive approvers",
			filenames:      []string{"file"},
			approvers:      []string{"RootApprover"},
			owners:         map[string]sets.Set[string]{"": sets.New[string]("rOOtaPProver")},
			expectedStatus: map[string]sets.Set[string]{"": sets.New[string]("RootApprover")},
		},
	}

	for _, test := range tests {
		testApprovers := NewApprovers(Owners{filenames: test.filenames, repo: createFakeRepo(test.owners), log: logrus.WithField("plugin", "some_plugin")})
		for _, approver := range test.approvers {
			testApprovers.AddApprover(approver, "REFERENCE", false)
		}
		calculated := testApprovers.GetFilesApprovers()
		if !reflect.DeepEqual(test.expectedStatus, calculated) {
			t.Errorf("Failed for test %v.  Expected approval status: %v. Found %v", test.testName, test.expectedStatus, calculated)
		}
	}
}

func TestGetMessage(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Bill", "REFERENCE", false)

	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Bill</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign [alice](https://github.com/alice) for approval. For more information see [the Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).
**Please ensure that each of them provides their approval before proceeding.**

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/dev/a/OWNERS)**
- ~~[b/OWNERS](https://github.com/org/repo/blob/dev/b/OWNERS)~~ [Bill]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice"]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "dev"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}

	ap = NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go", "c/c.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
				"c": sets.New[string]("Bob"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Bill", "REFERENCE", false)

	want = `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Bill</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign [alice](https://github.com/alice), [bob](https://github.com/bob) for approval. For more information see [the Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).
**Please ensure that each of them provides their approval before proceeding.**

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/dev/a/OWNERS)**
- ~~[b/OWNERS](https://github.com/org/repo/blob/dev/b/OWNERS)~~ [Bill]
- **[c/OWNERS](https://github.com/org/repo/blob/dev/c/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice","bob"]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "dev"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageMDSuggestedCCAssignedCC(t *testing.T) {
	linkURL, _ := url.Parse("https://github.com")
	commandHelpLink := "https://commandHelpLink"
	prProcessLink := "https://prProcessLink"
	org := "org"
	repo := "repo"
	branch := "branch"

	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go", "c/c.go", "d/d.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
				"c": sets.New[string]("Cathy"),
				"d": sets.New[string]("Diana"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)

	ap.RequireIssue = true
	// ap.AddApprover("Bill", "REFERENCE", false)
	ap.AddAssignees("Cathy")
	ap.AddAssignees("Diana")

	fmt.Println(len(ap.SuggestedCCs()))
	fmt.Println(len(ap.AssignedCCs()))

	expectedOutput := "**Once this PR has been reviewed and has the lgtm label**, please ask for approval from [cathy](https://github.com/cathy), [diana](https://github.com/diana) and additionally assign [alice](https://github.com/alice), [bill](https://github.com/bill) for approval."

	got := GetMessage(ap, linkURL, commandHelpLink, prProcessLink, org, repo, branch)
	if got != nil && !strings.Contains(*got, expectedOutput) {
		t.Errorf("GetMessage() = %v, want %v", *got, expectedOutput)
	}

}

func TestGetMessageMDSuggestedCCNoAssignedCC(t *testing.T) {
	linkURL, _ := url.Parse("https://github.com")
	commandHelpLink := "https://commandHelpLink"
	prProcessLink := "https://prProcessLink"
	org := "org"
	repo := "repo"
	branch := "branch"

	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go", "c/c.go", "d/d.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
				"c": sets.New[string]("Cathy"),
				"d": sets.New[string]("Diana"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)

	ap.RequireIssue = true
	ap.AddApprover("Bill", "REFERENCE", false)

	expectedOutput := "**Once this PR has been reviewed and has the lgtm label**, please assign [alice](https://github.com/alice), [cathy](https://github.com/cathy), [diana](https://github.com/diana) for approval."

	got := GetMessage(ap, linkURL, commandHelpLink, prProcessLink, org, repo, branch)
	if got != nil && !strings.Contains(*got, expectedOutput) {
		t.Errorf("GetMessage() = %v, want %v", *got, expectedOutput)
	}

}

func TestGetMessageNoSuggestedCCMDAssignedCC(t *testing.T) {
	linkURL, _ := url.Parse("https://github.com")
	commandHelpLink := "https://commandHelpLink"
	prProcessLink := "https://prProcessLink"
	org := "org"
	repo := "repo"
	branch := "branch"

	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go", "c/c.go", "d/d.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
				"c": sets.New[string]("Cathy"),
				"d": sets.New[string]("Diana"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)

	ap.RequireIssue = true
	ap.AddApprover("Bill", "REFERENCE", false)
	ap.AddAssignees("Alice")
	ap.AddAssignees("Cathy")
	ap.AddAssignees("Diana")

	expectedOutput := "**Once this PR has been reviewed and has the lgtm label**, please ask for approval from [alice](https://github.com/alice), [cathy](https://github.com/cathy), [diana](https://github.com/diana). For more information see [the Code Review Process](https://prProcessLink)."

	got := GetMessage(ap, linkURL, commandHelpLink, prProcessLink, org, repo, branch)
	if got != nil && !strings.Contains(*got, expectedOutput) {
		t.Errorf("GetMessage() = %v, want %v", *got, expectedOutput)
	}

}

func TestGetMessageAllApproved(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddApprover("Alice", "REFERENCE", false)
	ap.AddLGTMer("Bill", "REFERENCE", false)

	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="LGTM">Bill</a>*

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]
- ~~[b/OWNERS](https://github.com/org/repo/blob/master/b/OWNERS)~~ [Bill]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageNotFullyApprovedAndAssigneeIsUsefulApprover(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAssignees("Bill") // Bill is an approver for b/b.go and assignee.
	ap.AddApprover("Alice", "REFERENCE", false)

	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from [bill](https://github.com/bill). For more information see [the Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).
**Please ensure that each of them provides their approval before proceeding.**

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]
- **[b/OWNERS](https://github.com/org/repo/blob/master/b/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["bill"]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageNoneApproved(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAuthorSelfApprover("John", "REFERENCE", false)
	ap.RequireIssue = true
	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Author self-approved">John</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign [alice](https://github.com/alice), [bill](https://github.com/bill) for approval. For more information see [the Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).
**Please ensure that each of them provides their approval before proceeding.**

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**
- **[b/OWNERS](https://github.com/org/repo/blob/master/b/OWNERS)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice","bill"]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageApprovedIssueAssociated(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.go"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AssociatedIssue = 12345
	ap.AddAuthorSelfApprover("John", "REFERENCE", false)
	ap.AddApprover("Bill", "REFERENCE", false)
	ap.AddApprover("Alice", "REFERENCE", false)

	want := `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="Approved">Bill</a>*, *<a href="REFERENCE" title="Author self-approved">John</a>*

Associated issue: *#12345*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]
- ~~[b/OWNERS](https://github.com/org/repo/blob/master/b/OWNERS)~~ [Bill]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageApprovedNoIssueByPassed(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/b.md"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a": sets.New[string]("Alice"),
				"b": sets.New[string]("Bill"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.RequireIssue = true
	ap.AddAuthorSelfApprover("John", "REFERENCE", false)
	ap.AddApprover("Bill", "REFERENCE", true)
	ap.AddApprover("Alice", "REFERENCE", true)

	want := `[APPROVALNOTIFIER] This PR is **APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="Approved">Bill</a>*, *<a href="REFERENCE" title="Author self-approved">John</a>*

Associated issue requirement bypassed by: *<a href="REFERENCE" title="Approved">Alice</a>*, *<a href="REFERENCE" title="Approved">Bill</a>*

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

The pull request process is described [here](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process)

<details >
Needs approval from an approver in each of these files:

- ~~[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)~~ [Alice]
- ~~[b/OWNERS](https://github.com/org/repo/blob/master/b/OWNERS)~~ [Bill]

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":[]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageMDOwners(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/README.md"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a":           sets.New[string]("Alice"),
				"b":           sets.New[string]("Bill"),
				"b/README.md": sets.New[string]("Doctor"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAuthorSelfApprover("John", "REFERENCE", false)
	ap.RequireIssue = true
	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Author self-approved">John</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign [alice](https://github.com/alice), [doctor](https://github.com/doctor) for approval. For more information see [the Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).
**Please ensure that each of them provides their approval before proceeding.**

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.com/org/repo/blob/master/a/OWNERS)**
- **[b/README.md](https://github.com/org/repo/blob/master/b/README.md)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice","doctor"]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}

func TestGetMessageDifferentGitHubLink(t *testing.T) {
	ap := NewApprovers(
		Owners{
			filenames: []string{"a/a.go", "b/README.md"},
			repo: createFakeRepo(map[string]sets.Set[string]{
				"a":           sets.New[string]("Alice"),
				"b":           sets.New[string]("Bill"),
				"b/README.md": sets.New[string]("Doctor"),
			}),
			log: logrus.WithField("plugin", "some_plugin"),
		},
	)
	ap.AddAuthorSelfApprover("John", "REFERENCE", false)
	ap.RequireIssue = true
	want := `[APPROVALNOTIFIER] This PR is **NOT APPROVED**

This pull-request has been approved by: *<a href="REFERENCE" title="Author self-approved">John</a>*
**Once this PR has been reviewed and has the lgtm label**, please assign [alice](https://github.com/alice), [doctor](https://github.com/doctor) for approval. For more information see [the Code Review Process](https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process).
**Please ensure that each of them provides their approval before proceeding.**

*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with ` + "`/approve no-issue`" + `

The full list of commands accepted by this bot can be found [here](https://go.k8s.io/bot-commands?repo=org%2Frepo).

<details open>
Needs approval from an approver in each of these files:

- **[a/OWNERS](https://github.mycorp.com/org/repo/blob/master/a/OWNERS)**
- **[b/README.md](https://github.mycorp.com/org/repo/blob/master/b/README.md)**

Approvers can indicate their approval by writing ` + "`/approve`" + ` in a comment
Approvers can cancel approval by writing ` + "`/approve cancel`" + ` in a comment
</details>
<!-- META={"approvers":["alice","doctor"]} -->`
	if got := GetMessage(ap, &url.URL{Scheme: "https", Host: "github.mycorp.com"}, "https://go.k8s.io/bot-commands", "https://git.k8s.io/community/contributors/guide/owners.md#the-code-review-process", "org", "repo", "master"); got == nil {
		t.Error("GetMessage() failed")
	} else if *got != want {
		t.Errorf("GetMessage() = %+v, want = %+v", *got, want)
	}
}
