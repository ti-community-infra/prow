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
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"sigs.k8s.io/prow/pkg/github"
)

const webhookSecret = "a-test-secret"

func TestRouteEvent(t *testing.T) {
	tests := []struct {
		name         string
		eventType    string
		payload      string
		wantOrg      string
		wantRepo     string
		wantRelevant bool
		wantErr      bool
	}{
		{
			name:         "opened pull request",
			eventType:    "pull_request",
			payload:      `{"action":"opened","repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: true,
		},
		{
			name:         "synchronize pull request",
			eventType:    "pull_request",
			payload:      `{"action":"synchronize","repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: true,
		},
		{
			name:         "labeled pull request is ignored",
			eventType:    "pull_request",
			payload:      `{"action":"labeled","repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: false,
		},
		{
			name:         "issue comment with an approve command",
			eventType:    "issue_comment",
			payload:      `{"action":"created","repository":{"full_name":"tikv/pd"},"comment":{"user":{"login":"someone"},"body":"/approve"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: true,
		},
		{
			name:         "issue comment with an lgtm cancel command",
			eventType:    "issue_comment",
			payload:      `{"action":"created","repository":{"full_name":"tikv/pd"},"comment":{"user":{"login":"someone"},"body":"/lgtm cancel"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: true,
		},
		{
			// The bot comments for the welcome message, command help and label
			// churn too; none of that can change the approval state.
			name:         "issue comment without an approval command is ignored",
			eventType:    "issue_comment",
			payload:      `{"action":"created","repository":{"full_name":"tikv/pd"},"comment":{"user":{"login":"ti-chi-bot"},"body":"Welcome to the pull request!"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: false,
		},
		{
			name:         "edited issue comment is ignored",
			eventType:    "issue_comment",
			payload:      `{"action":"edited","repository":{"full_name":"tikv/pd"},"comment":{"user":{"login":"someone"},"body":"/approve"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: false,
		},
		{
			name:         "submitted pull request review",
			eventType:    "pull_request_review",
			payload:      `{"action":"submitted","repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: true,
		},
		{
			name:         "dismissed pull request review",
			eventType:    "pull_request_review",
			payload:      `{"action":"dismissed","repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: true,
		},
		{
			name:         "edited pull request review is ignored",
			eventType:    "pull_request_review",
			payload:      `{"action":"edited","repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: false,
		},
		{
			name:         "unrelated event type is ignored",
			eventType:    "push",
			payload:      `{"repository":{"full_name":"tikv/pd"}}`,
			wantOrg:      "tikv",
			wantRepo:     "pd",
			wantRelevant: false,
		},
		{
			name:      "payload without a repository is malformed",
			eventType: "pull_request",
			payload:   `{"action":"opened"}`,
			wantErr:   true,
		},
		{
			name:      "invalid json is malformed",
			eventType: "pull_request",
			payload:   `{`,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org, repo, relevant, err := routeEvent(tt.eventType, []byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if org != tt.wantOrg || repo != tt.wantRepo {
				t.Errorf("repository = %q/%q, want %q/%q", org, repo, tt.wantOrg, tt.wantRepo)
			}
			if relevant != tt.wantRelevant {
				t.Errorf("relevant = %v, want %v", relevant, tt.wantRelevant)
			}
		})
	}
}

func TestWebhookPublishesStatusForConfiguredRepository(t *testing.T) {
	fake := newFakeGitHub()
	fake.changes = protectedChange()
	srv := newTestServer(t, fake)

	payload := []byte(`{"action":"opened","number":1,"repository":{"full_name":"tikv/pd","owner":{"login":"tikv"},"name":"pd"},"pull_request":{"number":1,"state":"open","head":{"sha":"abc123"}}}`)
	recorder := postWebhook(t, srv, "pull_request", payload)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	waitFor(t, "a commit status", func() bool { return fake.count("CreateStatus") == 1 })

	statuses := fake.recordedStatuses()
	if statuses[0].State != github.StatusFailure {
		t.Errorf("state = %q, want %q", statuses[0].State, github.StatusFailure)
	}
}

// The webhook must answer immediately: hook fans events out synchronously.
func TestWebhookAcknowledgesUnconfiguredRepository(t *testing.T) {
	fake := newFakeGitHub()
	srv := newTestServer(t, fake)

	payload := []byte(`{"action":"opened","number":1,"repository":{"full_name":"pingcap/tidb","owner":{"login":"pingcap"},"name":"tidb"},"pull_request":{"number":1,"state":"open","head":{"sha":"abc123"}}}`)
	recorder := postWebhook(t, srv, "pull_request", payload)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if len(fake.calls) != 0 {
		t.Errorf("made GitHub calls for an unconfigured repository: %v", fake.calls)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	fake := newFakeGitHub()
	srv := newTestServer(t, fake)

	payload := []byte(`{"action":"opened","repository":{"full_name":"tikv/pd"}}`)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", "guid")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")

	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if len(fake.calls) != 0 {
		t.Errorf("made GitHub calls for an unauthenticated request: %v", fake.calls)
	}
}

func newTestServer(t *testing.T, fake *fakeGitHub) *server {
	t.Helper()

	log := logrus.NewEntry(logrus.New())
	log.Logger.SetLevel(logrus.PanicLevel)

	configs, err := newConfigSource(writePolicy(t, testPolicy), log)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}

	return &server{
		tokenGenerator: func() []byte { return []byte(webhookSecret) },
		configs:        configs,
		handler:        newHandler(configs, fake, newCache(defaultCacheTTL, defaultCacheSize), false, func(login string) bool { return login == selfLogin }, &pluginConfigSource{config: lgtmCountsAsApprove()}, log),
		log:            log,
	}
}

func postWebhook(t *testing.T, srv *server, eventType string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()

	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	request.Header.Set("X-GitHub-Event", eventType)
	request.Header.Set("X-GitHub-Delivery", "test-guid")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Hub-Signature-256", signature)

	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
