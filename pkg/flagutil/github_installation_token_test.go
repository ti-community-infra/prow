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

package flagutil

import (
	"errors"
	"testing"
)

func TestInstallationTokenFor(t *testing.T) {
	t.Run("before the client is constructed", func(t *testing.T) {
		o := &GitHubOptions{}
		if _, err := o.InstallationTokenFor("any-owner"); err == nil {
			t.Fatal("expected an error before the GitHub client is constructed")
		}
	})

	t.Run("app auth resolves the owner", func(t *testing.T) {
		o := &GitHubOptions{
			tokenGenerator: func(owner string) (string, error) {
				return "token-for-" + owner, nil
			},
		}
		got, err := o.InstallationTokenFor("fork-owner")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "token-for-fork-owner" {
			t.Fatalf("expected the owner token, got %q", got)
		}
	})

	t.Run("token resolution failure is returned", func(t *testing.T) {
		o := &GitHubOptions{
			tokenGenerator: func(string) (string, error) {
				return "", errors.New("no installation")
			},
		}
		if _, err := o.InstallationTokenFor("fork-owner"); err == nil {
			t.Fatal("expected the generator error to be returned")
		}
	})
}
