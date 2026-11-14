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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"

	"sigs.k8s.io/prow/pkg/config"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/pluginhelp"
)

// pluginName is the plugin name used in logs and in the help page.
const pluginName = "protected-changes"

func helpProvider(_ []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	return &pluginhelp.PluginHelp{
		Description: "The protected-changes plugin reports a commit status when a pull request adds lines " +
			"that a repository policy protects without an approval from a listed reviewer. " +
			"It runs on pull request updates and on ti-chi-bot approval notifications, and keeps one " +
			"advisory comment per pull request.",
	}, nil
}

type server struct {
	tokenGenerator func() []byte
	configs        *configSource
	handler        *handler
	log            *logrus.Entry
}

// ServeHTTP acknowledges the event quickly and hands the work to a goroutine.
//
// hook dispatches webhooks synchronously across every repository, so this
// handler must respond within seconds and never block on the GitHub API.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	eventType, eventGUID, payload, ok, _ := github.ValidateWebhook(w, r, s.tokenGenerator)
	if !ok {
		return
	}

	l := s.log.WithFields(logrus.Fields{"event-type": eventType, github.EventGUID: eventGUID})

	org, repo, relevant, err := routeEvent(eventType, payload)
	if err != nil {
		webhookEvents.WithLabelValues(eventType, "malformed").Inc()
		l.WithError(err).Error("Failed to read the webhook payload.")
		// The signature was valid, so a retry would fail the same way: ack it.
		fmt.Fprintln(w, "Event received. Have a nice day.")
		return
	}

	if !relevant || s.configs.Current().GetRepository(org+"/"+repo) == nil {
		webhookEvents.WithLabelValues(eventType, "ignored").Inc()
		fmt.Fprintln(w, "Event received. Have a nice day.")
		return
	}

	webhookEvents.WithLabelValues(eventType, "accepted").Inc()
	fmt.Fprintln(w, "Event received. Have a nice day.")

	go s.dispatch(l, eventType, payload)
}

func (s *server) dispatch(l *logrus.Entry, eventType string, payload []byte) {
	var err error

	switch eventType {
	case "pull_request":
		event := &github.PullRequestEvent{}
		if err = json.Unmarshal(payload, event); err == nil {
			err = s.handler.handlePullRequestEvent(event)
		}
	case "pull_request_review":
		event := &github.ReviewEvent{}
		if err = json.Unmarshal(payload, event); err == nil {
			err = s.handler.handlePullRequestReviewEvent(event)
		}
	default:
		event := &github.IssueCommentEvent{}
		if err = json.Unmarshal(payload, event); err == nil {
			err = s.handler.handleIssueCommentEvent(event)
		}
	}

	if err != nil {
		l.WithError(err).Error("Failed to handle the event.")
	}
}

// routeEvent decides, from the envelope alone, whether an event is worth a
// goroutine and a GitHub call.
func routeEvent(eventType string, payload []byte) (org, repo string, relevant bool, err error) {
	var envelope struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Comment struct {
			User struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
			Body string `json:"body"`
		} `json:"comment"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", "", false, err
	}

	parts := strings.SplitN(envelope.Repository.FullName, "/", 2)
	if len(parts) != 2 {
		return "", "", false, fmt.Errorf("no repository in the payload")
	}
	org, repo = parts[0], parts[1]

	switch eventType {
	case "pull_request":
		switch envelope.Action {
		case string(github.PullRequestActionOpened), string(github.PullRequestActionSynchronize), string(github.PullRequestActionReopened):
			return org, repo, true, nil
		}
	case "pull_request_review":
		// A review can approve or withdraw an approval. Repositories that do not
		// use the review UI produce none; the /approve and /lgtm comments below
		// cover them.
		switch envelope.Action {
		case string(github.ReviewActionSubmitted), string(github.ReviewActionDismissed):
			return org, repo, true, nil
		}
	case "issue_comment":
		// Only an approval notification from a bot can change the verdict; every
		// other comment is acknowledged without any GitHub call.
		comment := github.IssueComment{
			Body: envelope.Comment.Body,
			User: github.User{Login: envelope.Comment.User.Login, Type: envelope.Comment.User.Type},
		}
		if envelope.Action == string(github.IssueCommentActionCreated) && isApprovalNotification(comment) {
			return org, repo, true, nil
		}
	}

	return org, repo, false, nil
}
