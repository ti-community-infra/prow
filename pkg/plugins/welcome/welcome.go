/*
Copyright 2018 The Kubernetes Authors.

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

// Package welcome implements a prow plugin to welcome new contributors
package welcome

import (
	"bytes"
	"fmt"
	"html/template"

	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/prow/pkg/config"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/pluginhelp"
	"sigs.k8s.io/prow/pkg/plugins"
	"sigs.k8s.io/prow/pkg/plugins/trigger"
)

const (
	pluginName            = "welcome"
	defaultWelcomeMessage = "Welcome @{{.AuthorLogin}}! It looks like this is your first PR to {{.Org}}/{{.Repo}} 🎉"

	// ~~~ hacked parts compared with upstream Prow project.
	defaultIssueWelcomeMessage = "Welcome @{{.AuthorLogin}}! It looks like this is your first issue to {{.Org}}/{{.Repo}} 🎉"
	firstTimeContributorLabel  = "first-time-contributor"
	contributionLabel          = "contribution"
)

// PRInfo contains info used provided to the welcome message template
type PRInfo struct {
	Org         string
	Repo        string
	AuthorLogin string
	AuthorName  string
}

func init() {
	plugins.RegisterPullRequestHandler(pluginName, handlePullRequest, helpProvider)

	// ~~~ hacked parts compared with upstream Prow project.
	plugins.RegisterIssueHandler(pluginName, handleIssue, helpProvider)
}

func helpProvider(config *plugins.Configuration, enabledRepos []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	welcomeConfig := map[string]string{}
	for _, repo := range enabledRepos {
		messageTemplate := welcomeMessageForRepo(optionsForRepo(config, repo.Org, repo.Repo))
		welcomeConfig[repo.String()] = fmt.Sprintf("The welcome plugin is configured to post using following welcome template: %s.", messageTemplate)
	}

	// The {WhoCanUse, Usage, Examples} fields are omitted because this plugin is not triggered with commands.
	yamlSnippet, err := plugins.CommentMap.GenYaml(&plugins.Configuration{
		Welcome: []plugins.Welcome{
			{
				Repos: []string{
					"org/repo1",
					"org/repo2",
				},
				MessageTemplate: "Welcome @{{.AuthorLogin}}!",
				AlwaysPost:      false,
			},
		},
	})
	if err != nil {
		logrus.WithError(err).Warnf("cannot generate comments for %s plugin", pluginName)
	}
	return &pluginhelp.PluginHelp{
			Description: "The welcome plugin greets incoming PRs and issues with a welcoming message, and adds labels to clarify the contribution.",
			Config:      welcomeConfig,
			Snippet:     yamlSnippet,
		},
		nil
}

type githubClient interface {
	CreateComment(owner, repo string, number int, comment string) error
	FindIssuesWithOrg(org, query, sort string, asc bool) ([]github.Issue, error)
	IsCollaborator(org, repo, user string) (bool, error)
	IsMember(org, user string) (bool, error)
	BotUserChecker() (func(candidate string) bool, error)

	// hacked parts compared with upstream Prow project.
	AddLabel(org, repo string, number int, label string) error
}

type client struct {
	GitHubClient githubClient
	Logger       *logrus.Entry
}

func getClient(pc plugins.Agent) client {
	return client{
		GitHubClient: pc.GitHubClient,
		Logger:       pc.Logger,
	}
}

func handlePullRequest(pc plugins.Agent, pre github.PullRequestEvent) error {
	t := pc.PluginConfig.TriggerFor(pre.PullRequest.Base.Repo.Owner.Login, pre.PullRequest.Base.Repo.Name)
	options := optionsForRepo(pc.PluginConfig, pre.Repo.Owner.Login, pre.Repo.Name)
	return handlePR(getClient(pc), t, pre, welcomeMessageForRepo(options), options.AlwaysPost)
}

func handlePR(c client, t plugins.Trigger, pre github.PullRequestEvent, welcomeTemplate string, alwaysPost bool) error {
	// Only consider newly opened PRs
	if pre.Action != github.PullRequestActionOpened {
		return nil
	}

	org := pre.PullRequest.Base.Repo.Owner.Login
	repo := pre.PullRequest.Base.Repo.Name
	user := pre.PullRequest.User.Login
	pullRequestNumber := pre.PullRequest.Number

	log := c.Logger.WithFields(logrus.Fields{"org": org, "repo": repo, "user": user, "number": pullRequestNumber})

	// ignore bots, we can't query their PRs
	if pre.PullRequest.User.Type != github.UserTypeUser {
		log.Debug("Ignoring bot user, as querying their PRs is not possible")
		return nil
	}

	trustedResponse, err := trigger.TrustedUser(c.GitHubClient, t.OnlyOrgMembers, t.TrustedApps, t.TrustedOrg, user, org, repo)
	if err != nil {
		return fmt.Errorf("check if user %s is trusted: %w", user, err)
	}

	// ~~~ hacked parts compared with upstream Prow project.
	if !trustedResponse.IsTrusted {
		log.Debug("User is not trusted. Add contribution label on the PR")
		err := c.GitHubClient.AddLabel(org, repo, pullRequestNumber, contributionLabel)
		if err != nil {
			return fmt.Errorf("add label to PR %d: %w", pullRequestNumber, err)
		}
	}

	if !alwaysPost && trustedResponse.IsTrusted {
		log.Debug("User is trusted. Skipping their welcome message")
		return nil
	}

	// search for PRs from the author in this repo
	query := fmt.Sprintf("is:pr repo:%s/%s author:%s", org, repo, user)
	issues, err := c.GitHubClient.FindIssuesWithOrg(org, query, "", false)
	if err != nil {
		return err
	}

	isFirstPR := len(issues) == 0 || len(issues) == 1 && issues[0].Number == pre.Number

	// ~~~ hacked parts compared with upstream Prow project.
	// if it's the first PR of the contributor, add first time contributor label on it.
	if !trustedResponse.IsTrusted && isFirstPR {
		log.Debug("Adding first-time-contributor label, it's the first PR in the repo")
		if err := c.GitHubClient.AddLabel(org, repo, pullRequestNumber, firstTimeContributorLabel); err != nil {
			return err
		}
	}

	// if there are no results, or if configured to greet any PR - post the welcome comment
	if alwaysPost || isFirstPR {
		// load the template, and run it over the PR info
		parsedTemplate, err := template.New("welcome").Parse(welcomeTemplate)
		if err != nil {
			return err
		}
		var msgBuffer bytes.Buffer
		err = parsedTemplate.Execute(&msgBuffer, PRInfo{
			Org:         org,
			Repo:        repo,
			AuthorLogin: user,
			AuthorName:  pre.PullRequest.User.Name,
		})
		if err != nil {
			return err
		}

		log.Debug("Posting a welcome message for pull request")
		return c.GitHubClient.CreateComment(org, repo, pullRequestNumber, msgBuffer.String())
	} else {
		log.WithField("issues_count", len(issues)).Debug("Ignoring PR, as user already has previous contributions")
	}

	return nil
}

// ~~~ hacked parts compared with upstream Prow project.
func handleIssue(pc plugins.Agent, issue github.IssueEvent) error {
	t := pc.PluginConfig.TriggerFor(issue.Repo.Owner.Login, issue.Repo.Name)
	c := getClient(pc)
	options := optionsForRepo(pc.PluginConfig, issue.Repo.Owner.Login, issue.Repo.Name)
	if options == nil {
		return nil
	}

	// Only consider newly opened Issues
	if issue.Action != github.IssueActionOpened {
		return nil
	}

	org := issue.Repo.Owner.Login
	repo := issue.Repo.Name
	user := issue.Issue.User.Login
	issueNumber := issue.Issue.Number

	log := c.Logger.WithFields(logrus.Fields{"org": org, "repo": repo, "user": user, "number": issueNumber})

	// ignore bots, we can't query their issues
	if issue.Issue.User.Type != github.UserTypeUser {
		log.Debug("Ignoring bot user, as querying their issues is not possible")
		return nil
	}

	trustedResponse, err := trigger.TrustedUser(c.GitHubClient, t.OnlyOrgMembers, t.TrustedApps, t.TrustedOrg, user, org, repo)
	if err != nil {
		return fmt.Errorf("check if user %s is trusted: %w", user, err)
	}

	if !trustedResponse.IsTrusted {
		log.Debug("User is not trusted. Add contribution label on the issue")
		err := c.GitHubClient.AddLabel(org, repo, issueNumber, contributionLabel)
		if err != nil {
			return fmt.Errorf("add label to issue %d: %w", issueNumber, err)
		}
	}

	if !options.AlwaysPost && trustedResponse.IsTrusted {
		log.Debug("User is trusted. Skipping their welcome message")
		return nil
	}

	// search for PRs from the author in this repo
	query := fmt.Sprintf("is:issue repo:%s/%s author:%s", org, repo, user)
	issues, err := c.GitHubClient.FindIssuesWithOrg(org, query, "", false)
	if err != nil {
		return err
	}

	isFirstIssue := len(issues) == 0 || len(issues) == 1 && issues[0].Number == issue.Issue.Number

	// if it's the first issue of the contributor, add first time contributor label on it.
	if !trustedResponse.IsTrusted && isFirstIssue {
		log.Debug("Adding first-time-contributor label, it's the first issue in the repo")
		if err := c.GitHubClient.AddLabel(org, repo, issueNumber, "first-time-contributor"); err != nil {
			return err
		}
	}

	// if there are no results, or if configured to greet any issue - post the welcome comment
	if options.AlwaysPost || isFirstIssue {
		// load the template, and run it over the PR info
		parsedTemplate, err := template.New("welcome").Parse(defaultIssueWelcomeMessage)
		if err != nil {
			return err
		}
		var msgBuffer bytes.Buffer
		err = parsedTemplate.Execute(&msgBuffer, PRInfo{
			Org:         org,
			Repo:        repo,
			AuthorLogin: user,
			AuthorName:  issue.Issue.User.Name,
		})
		if err != nil {
			return err
		}

		log.Debug("Posting a welcome message for issue")
		return c.GitHubClient.CreateComment(org, repo, issueNumber, msgBuffer.String())
	} else {
		log.WithField("issues_count", len(issues)).Debug("Ignoring PR, as user already has previous contributions")
	}

	return nil
}

func welcomeMessageForRepo(options *plugins.Welcome) string {
	if options.MessageTemplate != "" {
		return options.MessageTemplate
	}
	return defaultWelcomeMessage
}

// optionsForRepo gets the plugins.Welcome struct that is applicable to the indicated repo.
func optionsForRepo(config *plugins.Configuration, org, repo string) *plugins.Welcome {
	fullName := fmt.Sprintf("%s/%s", org, repo)

	// First search for repo config
	for _, c := range config.Welcome {
		if !sets.NewString(c.Repos...).Has(fullName) {
			continue
		}
		return &c
	}

	// If you don't find anything, loop again looking for an org config
	for _, c := range config.Welcome {
		if !sets.NewString(c.Repos...).Has(org) {
			continue
		}
		return &c
	}

	// Return an empty config, and default to defaultWelcomeMessage
	return &plugins.Welcome{}
}
