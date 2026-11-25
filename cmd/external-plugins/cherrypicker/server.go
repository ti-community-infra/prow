/*
Copyright 2017 The Kubernetes Authors.

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
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/exec"
	cherryconfig "sigs.k8s.io/prow/cmd/external-plugins/cherrypicker/config"
	cherrypicker "sigs.k8s.io/prow/cmd/external-plugins/cherrypicker/lib"
	"sigs.k8s.io/prow/pkg/config"
	"sigs.k8s.io/prow/pkg/git/v2"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/pluginhelp"
	"sigs.k8s.io/prow/pkg/pluginhelp/externalplugins"
	"sigs.k8s.io/prow/pkg/plugins"
)

const pluginName = "cherrypick"

const defaultLabelPrefix = cherryconfig.DefaultLabelPrefix

var cherryPickRe = regexp.MustCompile(`(?m)^(?:/cherrypick|/cherry-pick)\s+(.+)$`)
var cherryPickInviteRe = regexp.MustCompile(`(?m)^(?:/cherrypick|/cherry-pick)-invite\b`)
var releaseNoteRe = regexp.MustCompile(`(?s)(?:Release note\*\*:\s*(?:<!--[^<>]*-->\s*)?` + "```(?:release-note)?|```release-note)(.+?)```")
var titleTargetBranchIndicatorTemplate = `[%s] `

var notOrgMemberMessageTemplate = "only [%s](https://github.com/orgs/%s/people) org members may request cherry picks. If you are already part of the org, make sure to [change](https://github.com/orgs/%s/people?query=%s) your membership to public. Otherwise you can still do the cherry-pick manually. "

type githubClient interface {
	AddCollaborator(org, repo, user string, permission github.RepoPermissionLevel) error
	AddLabel(org, repo string, number int, label string) error
	AssignIssue(org, repo string, number int, logins []string) error
	CreateComment(org, repo string, number int, comment string) error
	CreatePullRequest(org, repo, title, body, head, base string, canModify bool) (int, error)
	CreateIssue(org, repo, title, body string, milestone int, labels, assignees []string) (int, error)
	GetPullRequest(org, repo string, number int) (*github.PullRequest, error)
	GetPullRequestPatch(org, repo string, number int) ([]byte, error)
	GetPullRequests(org, repo string) ([]github.PullRequest, error)
	GetRepo(owner, name string) (github.FullRepo, error)
	GetSingleCommit(org, repo, SHA string) (github.RepositoryCommit, error)
	IsMember(org, user string) (bool, error)
	ListCollaborators(org, repo string) ([]github.User, error)
	ListIssueComments(org, repo string, number int) ([]github.IssueComment, error)
	GetIssueLabels(org, repo string, number int) ([]github.Label, error)
	ListOrgMembers(org, role string) ([]github.TeamMember, error)
	ListRepoInvitations(org, repo string) ([]github.CollaboratorRepoInvitation, error)
}

// HelpProvider constructs the PluginHelp for this plugin, including the
// effective configuration of every enabled repository.
func HelpProvider(agent *cherryconfig.Agent, defaults cherryconfig.Defaults) externalplugins.ExternalPluginHelpProvider {
	return func(enabledRepos []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
		pluginHelp := &pluginhelp.PluginHelp{
			Description: `The cherrypick plugin is used for cherrypicking PRs across branches. For every successful cherrypick invocation a new PR is opened against the target branch and assigned to the requester. The body of the parent PR is copied to the cherrypick PR, so issue references and the release note survive. Kind labels (e.g. kind/bug, kind/cleanup) from the original PR are copied into the cherry-pick PR description as /kind commands so the bot can re-apply them.`,
			Config:      map[string]string{},
		}
		if agent != nil && agent.Config() != nil {
			for _, repo := range enabledRepos {
				pluginHelp.Config[repo.String()] = describeOptions(agent.Config().Options(repo.Org, repo.Repo, defaults))
			}
		}
		pluginHelp.AddCommand(pluginhelp.Command{
			Usage:       "/cherrypick [branch]",
			Description: "Cherrypick a PR to a different branch. This command works both in merged PRs (the cherrypick PR is opened immediately) and open PRs (the cherrypick PR opens as soon as the original PR merges). If multiple branches are specified, separated by a space, a cherrypick for the first branch will be created with a comment to cherrypick the remaining branches after the first merges.",
			Featured:    true,
			// depends on how the cherrypick server runs; needs auth by default (--allow-all=false)
			WhoCanUse: "Members of the trusted organization for the repo.",
			Examples:  []string{"/cherrypick release-3.9", "/cherry-pick release-1.15", "/cherrypick release-1.6 release-1.5 release-1.4"},
		})
		pluginHelp.AddCommand(pluginhelp.Command{
			Usage:       "/cherry-pick-invite",
			Description: "Request a collaborator invitation to the bot fork so you can push to a cherry-pick pull request created from that fork.",
			Featured:    true,
			WhoCanUse:   "Members of the trusted organization for the repo.",
			Examples:    []string{"/cherry-pick-invite"},
		})
		return pluginHelp, nil
	}
}

func describeOptions(o cherryconfig.Options) string {
	return strings.Join([]string{
		"<ul>",
		fmt.Sprintf("<li>label_prefix: <code>%s</code></li>", o.LabelPrefix),
		fmt.Sprintf("<li>allow_all: <code>%t</code></li>", o.AllowAll),
		fmt.Sprintf("<li>use_prow_assignments: <code>%t</code></li>", o.UseProwAssignments),
		fmt.Sprintf("<li>labels: <code>%s</code></li>", strings.Join(o.Labels, ", ")),
		fmt.Sprintf("<li>fork_owner: <code>%s</code></li>", o.ForkOwner),
		fmt.Sprintf("<li>picked_label_prefix: <code>%s</code></li>", o.PickedLabelPrefix),
		fmt.Sprintf("<li>exclude_labels: <code>%s</code></li>", strings.Join(o.ExcludeLabels, ", ")),
		fmt.Sprintf("<li>on_conflict: <code>%s</code></li>", o.OnConflict),
		fmt.Sprintf("<li>copy_issue_numbers_from_squashed_commit: <code>%t</code></li>", o.CopyIssueNumbersFromSquashedCommit),
		"</ul>",
	}, "\n")
}

// Server implements http.Handler. It validates incoming GitHub webhooks and
// then dispatches them to the appropriate plugins.
type Server struct {
	tokenGenerator func() []byte
	botUser        *github.UserData
	email          string

	// githubURL is the web URL of the GitHub instance, e.g. https://github.com.
	githubURL string
	// installationToken returns a token that can write to the given owner.
	installationToken func(owner string) (string, error)

	gc git.ClientFactory
	// Used for unit testing
	pusher pusher
	ghc    githubClient
	log    logrus.FieldLogger

	// configAgent holds the per-repository overrides of the flag defaults.
	configAgent *cherryconfig.Agent

	// defaults are the process-wide settings taken from the flags; the
	// per-repository config overrides them.
	defaults cherryconfig.Defaults

	bare     *http.Client
	patchURL string

	mapLock sync.Mutex
	lockMap map[cherryPickRequest]*sync.Mutex
}

// options resolves the effective settings for a repository, layering the
// per-repository config over the flag defaults.
func (s *Server) options(org, repo string) cherryconfig.Options {
	var cfg *cherryconfig.Config
	if s.configAgent != nil {
		cfg = s.configAgent.Config()
	}
	return cfg.Options(org, repo, s.defaults)
}

// hasLabelPrefix reports whether the label starts with the configured prefix.
// An empty prefix disables label initiated cherry-picks.
func hasLabelPrefix(label, prefix string) bool {
	return prefix != "" && strings.HasPrefix(label, prefix)
}

type pusher interface {
	Push(r git.RepoClient, newBranch string, force bool) error
}

type centralPusher struct{}

func (p *centralPusher) Push(r git.RepoClient, newBranch string, force bool) error {
	return r.PushToCentral(newBranch, force)
}

// pushTarget is one place a cherry-pick branch can be pushed to, in order of
// preference.
type pushTarget struct {
	org    string
	pusher pusher
}

// authenticatedPusher pushes a branch to an explicit repository URL using a
// token resolved for that repository's owner. It is used for forks owned by a
// different organization than the base repository.
type authenticatedPusher struct {
	owner     string
	repo      string
	githubURL string
	token     func(owner string) (string, error)
}

func (p *authenticatedPusher) Push(r git.RepoClient, newBranch string, force bool) error {
	token, err := p.token(p.owner)
	if err != nil {
		return fmt.Errorf("failed to get a token for %s: %w", p.owner, err)
	}
	remote, err := p.pushURL(token)
	if err != nil {
		return err
	}

	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remote, newBranch)
	cmd := exec.New().Command("git", args...)
	cmd.SetDir(r.Directory())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push to %s/%s failed: %w: %s", p.owner, p.repo, err, censorToken(string(out), token))
	}
	return nil
}

func (p *authenticatedPusher) pushURL(token string) (string, error) {
	u, err := url.Parse(p.githubURL)
	if err != nil {
		return "", fmt.Errorf("invalid github URL %q: %w", p.githubURL, err)
	}
	u.User = url.UserPassword("x-access-token", token)
	u.Path = path.Join(u.Path, p.owner, p.repo) + ".git"
	return u.String(), nil
}

func censorToken(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "***")
}

type cherryPickRequest struct {
	org          string
	repo         string
	pr           int
	targetBranch string
}

// ServeHTTP validates an incoming webhook and puts it into the event channel.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	eventType, eventGUID, payload, ok, _ := github.ValidateWebhook(w, r, s.tokenGenerator)
	if !ok {
		return
	}
	fmt.Fprint(w, "Event received. Have a nice day.")

	if err := s.handleEvent(eventType, eventGUID, payload); err != nil {
		s.log.WithError(err).Error("Error parsing event.")
	}
}

func (s *Server) handleEvent(eventType, eventGUID string, payload []byte) error {
	l := s.log.WithFields(logrus.Fields{
		"event-type":     eventType,
		github.EventGUID: eventGUID,
	})
	switch eventType {
	case "issue_comment":
		var ic github.IssueCommentEvent
		if err := json.Unmarshal(payload, &ic); err != nil {
			return err
		}
		go func() {
			if log, err := s.handleIssueComment(l, ic); err != nil {
				log.WithError(err).Info("Cherry-pick failed.")
			}
		}()
	case "pull_request":
		var pr github.PullRequestEvent
		if err := json.Unmarshal(payload, &pr); err != nil {
			return err
		}
		go func() {
			if log, err := s.handlePullRequest(l, pr); err != nil {
				log.WithError(err).Info("Cherry-pick failed.")
			}
		}()
	default:
		l.Debugf("skipping event of type %q", eventType)
	}
	return nil
}

func (s *Server) handleIssueComment(log logrus.FieldLogger, ic github.IssueCommentEvent) (logrus.FieldLogger, error) {
	// Only consider new comments in PRs.
	if !ic.Issue.IsPullRequest() || ic.Action != github.IssueCommentActionCreated {
		return log, nil
	}

	org := ic.Repo.Owner.Login
	repo := ic.Repo.Name
	num := ic.Issue.Number
	commentAuthor := ic.Comment.User.Login
	opts := s.options(org, repo)

	log = log.WithFields(logrus.Fields{
		github.OrgLogField:  org,
		github.RepoLogField: repo,
		github.PrLogField:   num,
	})

	if cherryPickInviteRe.MatchString(ic.Comment.Body) {
		return log, s.inviteCollaborator(log, ic)
	}

	// fetch pull request
	pr, err := s.ghc.GetPullRequest(org, repo, num)
	if err != nil {
		return log, fmt.Errorf("failed to get pull request %s/%s#%d: %w", org, repo, num, err)
	}
	baseBranch := pr.Base.Ref
	title := pr.Title
	body := pr.Body

	// Collect all branches for which a PR should be created as an immediate response
	// to it. This excludes all subsequent branches in chained cherrypicks.
	commands := parseComment(ic.Comment)

	// Due to the regular expression, this should never happen.
	if len(commands) == 0 {
		return log, nil
	}

	_, hasInvalidBranch := commands[baseBranch]
	delete(commands, baseBranch)

	// If the user requested multiple cherry-picks, we inform them later if they also included
	// the base branch by mistake.
	if len(commands) == 0 {
		resp := fmt.Sprintf("I cannot cherry-pick the present PR on top of its base branch (`%s`).", baseBranch)
		log.Info(resp)
		return log, s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, resp))
	}

	// PR is not yet done, inform the user we will remember their instructions and create PRs later.
	if ic.Issue.State != "closed" {
		if !opts.AllowAll {
			// Only members should be able to do cherry-picks.
			ok, err := s.ghc.IsMember(org, commentAuthor)
			if err != nil {
				return log, err
			}
			if !ok {
				resp := fmt.Sprintf(notOrgMemberMessageTemplate, org, org, org, commentAuthor)
				log.Info(resp)
				return log, s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, resp))
			}
		}

		var branchNames []string
		for _, branch := range sets.List(sets.KeySet(commands)) {
			branchNames = append(branchNames, fmt.Sprintf("`%s`", branch))
		}

		var resp string
		if len(branchNames) == 1 {
			resp = fmt.Sprintf("once the present PR merges, I will cherry-pick it on top of %s in a new PR and assign it to you.", branchNames[0])
		} else {
			resp = fmt.Sprintf("once the present PR merges, I will cherry-pick it on top of %s in new PRs and assign them to you.", strings.Join(branchNames, ", "))
		}
		if hasInvalidBranch {
			resp += fmt.Sprintf(" I cannot cherry-pick it on top of `%s` because that is already its base branch.", baseBranch)
		}

		log.Info(resp)
		return log, s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, resp))
	}

	// Cherry-pick only merged PRs.
	if !pr.Merged {
		resp := "cannot cherry-pick an unmerged PR"
		log.Info(resp)
		return log, s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, resp))
	}

	if !opts.AllowAll {
		// Only org members should be able to do cherry-picks.
		ok, err := s.ghc.IsMember(org, commentAuthor)
		if err != nil {
			return log, err
		}
		if !ok {
			resp := fmt.Sprintf(notOrgMemberMessageTemplate, org, org, org, commentAuthor)
			log.Info(resp)
			return log, s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, resp))
		}
	}

	// If the user requested multiple cherry-picks and one of them was invalid, warn them about it.
	if hasInvalidBranch {
		resp := fmt.Sprintf("I cannot cherry-pick the present PR on top of its base branch (`%s`).", baseBranch)
		log.Info(resp)

		if err := s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, resp)); err != nil {
			log.WithError(err).WithField("response", resp).Error("Failed to create comment.")
		}
	}

	// A failure in one cherry-pick must NOT prevent processing of the remaining
	// targets, so we collect errors and continue the loop.
	var errs []error

	for _, targetBranch := range sets.List(sets.KeySet(commands)) {
		branchLog := log.WithFields(logrus.Fields{
			"requester":     ic.Comment.User.Login,
			"target_branch": targetBranch,
		})
		branchLog.Debug("Cherrypick request.")

		if err := s.handle(branchLog, ic.Comment.User.Login, &ic.Comment, org, repo, targetBranch, baseBranch, commands[targetBranch], title, body, num); err != nil {
			errs = append(errs, fmt.Errorf("failed to handle cherrypick for %s: %w", targetBranch, err))
			continue
		}
	}

	return log, utilerrors.NewAggregate(errs)
}

type cherrypickCommands map[string][]string

func parseComment(comment github.IssueComment) cherrypickCommands {
	cmds := cherrypickCommands{}

	for _, match := range cherryPickRe.FindAllStringSubmatch(comment.Body, -1) {
		targetBranches := strings.Fields(match[1])
		targetBranch := targetBranches[0]

		cmds[targetBranch] = targetBranches[1:]
	}

	return cmds
}

func (s *Server) handlePullRequest(log logrus.FieldLogger, pre github.PullRequestEvent) (logrus.FieldLogger, error) {
	switch pre.Action {
	case github.PullRequestActionLabeled:
		return s.handlePullRequestLabelAdded(log, pre)
	case github.PullRequestActionClosed:
		return s.handlePullRequestClosed(log, pre)
	default:
		return log, nil
	}
}

func (s *Server) handlePullRequestLabelAdded(log logrus.FieldLogger, pre github.PullRequestEvent) (logrus.FieldLogger, error) {
	pr := pre.PullRequest
	org := pr.Base.Repo.Owner.Login
	repo := pr.Base.Repo.Name
	num := pr.Number
	baseBranch := pr.Base.Ref
	opts := s.options(org, repo)

	// Ignore non cherrypick labels
	if pre.Label.Name == "" || !hasLabelPrefix(pre.Label.Name, opts.LabelPrefix) {
		return log, nil
	}
	targetBranch := strings.TrimPrefix(pre.Label.Name, opts.LabelPrefix)
	log = log.WithFields(logrus.Fields{
		github.OrgLogField:  org,
		github.RepoLogField: repo,
		github.PrLogField:   num,
	})
	prAuthor := pr.User.Login

	if !opts.AllowAll {
		// Only org members should be able to do cherry-picks.
		ok, err := s.ghc.IsMember(org, prAuthor)
		if err != nil {
			return log, err
		}
		if !ok {
			resp := fmt.Sprintf(notOrgMemberMessageTemplate, org, org, org, prAuthor)
			if err := s.createComment(log, org, repo, num, nil, resp); err != nil {
				log.WithError(err).WithField("response", resp).Error("Failed to create comment.")
			}
			return log, nil
		}
	}
	if targetBranch == baseBranch {
		resp := fmt.Sprintf("base branch (%s) needs to differ from target branch (%s)", baseBranch, targetBranch)
		return log, s.createComment(log, org, repo, num, nil, resp)
	}

	// Cherry-pick only merged PRs.
	if !pr.Merged {
		resp := fmt.Sprintf(
			"@%s once the present PR merges, I will cherry-pick it on top of `%s` in a new PR and assign it to you.",
			prAuthor,
			targetBranch,
		)
		return log, s.createComment(log, org, repo, num, nil, resp)
	}

	return log, s.handle(
		log.WithFields(logrus.Fields{
			"requester":     prAuthor,
			"target_branch": targetBranch,
		}),
		prAuthor,
		nil,
		org,
		repo,
		targetBranch,
		baseBranch,
		nil,
		pr.Title,
		pr.Body,
		num,
	)
}

func (s *Server) handlePullRequestClosed(log logrus.FieldLogger, pre github.PullRequestEvent) (logrus.FieldLogger, error) {
	pr := pre.PullRequest
	if !pr.Merged || pr.MergeSHA == nil {
		return log, nil
	}

	org := pr.Base.Repo.Owner.Login
	repo := pr.Base.Repo.Name
	baseBranch := pr.Base.Ref
	num := pr.Number
	title := pr.Title
	body := pr.Body
	opts := s.options(org, repo)

	log = log.WithFields(logrus.Fields{
		github.OrgLogField:  org,
		github.RepoLogField: repo,
		github.PrLogField:   num,
	})

	comments, err := s.ghc.ListIssueComments(org, repo, num)
	if err != nil {
		return log, fmt.Errorf("failed to list comments: %w", err)
	}

	// requester -> target branch -> issue comment
	requesterToComments := make(map[string]map[string]*github.IssueComment)
	// target branch -> chain branches (eg. "release-1.6" -> []string{"release-1.5", "release-1.4"})
	targetBranchToChainBranches := make(map[string][]string)

	// treat PR body/description as a comment
	comments = append([]github.IssueComment{{
		Body: body,
		User: pr.User,
	}}, comments...)

	// first look for our special comments
	for _, comment := range comments {
		commands := parseComment(comment)

		for targetBranch, chainedBranches := range commands {
			if requesterToComments[comment.User.Login] == nil {
				requesterToComments[comment.User.Login] = make(map[string]*github.IssueComment)
			}
			requesterToComments[comment.User.Login][targetBranch] = &comment

			if len(chainedBranches) > 0 {
				targetBranchToChainBranches[targetBranch] = chainedBranches
			}
		}
	}
	labels, err := s.ghc.GetIssueLabels(org, repo, num)
	if err != nil {
		return log, fmt.Errorf("failed to get issue labels: %w", err)
	}
	for _, label := range labels {
		if hasLabelPrefix(label.Name, opts.LabelPrefix) {
			if requesterToComments[pr.User.Login] == nil {
				requesterToComments[pr.User.Login] = make(map[string]*github.IssueComment)
			}
			requesterToComments[pr.User.Login][strings.TrimPrefix(label.Name, opts.LabelPrefix)] = nil // leave this nil which indicates a label-initiated cherry-pick
		}
	}
	if len(requesterToComments) == 0 {
		return log, nil
	}
	// Figure out membership.
	if !opts.AllowAll {
		// TODO: Possibly cache this.
		members, err := s.ghc.ListOrgMembers(org, "all")
		if err != nil {
			return log, err
		}
		for requester := range requesterToComments {
			isMember := slices.ContainsFunc(members, func(member github.TeamMember) bool {
				return requester == member.Login
			})
			if !isMember {
				delete(requesterToComments, requester)
			}
		}
	}

	// Handle multiple comments serially. Make sure to filter out
	// comments targeting the same branch.
	handledBranches := make(map[string]bool)
	var errs []error
	for requester, branches := range requesterToComments {
		for targetBranch, ic := range branches {
			if handledBranches[targetBranch] {
				// Branch already handled. Skip.
				continue
			}
			if targetBranch == baseBranch {
				resp := fmt.Sprintf("base branch (%s) needs to differ from target branch (%s)", baseBranch, targetBranch)
				log.Info(resp)
				if err := s.createComment(log, org, repo, num, ic, resp); err != nil {
					log.WithError(err).WithField("response", resp).Error("Failed to create comment.")
				}
				continue
			}
			handledBranches[targetBranch] = true
			branchLog := log.WithFields(logrus.Fields{
				"requester":     requester,
				"target_branch": targetBranch,
			})
			branchLog.Debug("Cherrypick request.")
			err := s.handle(branchLog, requester, ic, org, repo, targetBranch, baseBranch, targetBranchToChainBranches[targetBranch], title, body, num)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to create cherrypick: %w", err))
			}
		}
	}
	return log, utilerrors.NewAggregate(errs)
}

var cherryPickBranchFmt = "cherry-pick-%d-to-%s"

func (s *Server) handle(logger logrus.FieldLogger, requester string, comment *github.IssueComment, org, repo, targetBranch, baseBranch string, chainBranches []string, title, body string, num int) error {
	var lock *sync.Mutex
	func() {
		s.mapLock.Lock()
		defer s.mapLock.Unlock()
		if _, ok := s.lockMap[cherryPickRequest{org, repo, num, targetBranch}]; !ok {
			if s.lockMap == nil {
				s.lockMap = map[cherryPickRequest]*sync.Mutex{}
			}
			s.lockMap[cherryPickRequest{org, repo, num, targetBranch}] = &sync.Mutex{}
		}
		lock = s.lockMap[cherryPickRequest{org, repo, num, targetBranch}]
	}()
	lock.Lock()
	defer lock.Unlock()

	opts := s.options(org, repo)

	targets := s.pushTargets(logger, org, repo)
	if s.pusher != nil {
		for i := range targets {
			targets[i].pusher = s.pusher
		}
	}

	// Clone the repo, checkout the target branch.
	startClone := time.Now()
	r, err := s.gc.ClientFor(org, repo)
	if err != nil {
		return fmt.Errorf("failed to get git client for %s/%s: %w", org, repo, err)
	}
	defer func() {
		if err := r.Clean(); err != nil {
			logger.WithError(err).Error("Error cleaning up repo.")
		}
	}()
	if err := r.Checkout(targetBranch); err != nil {
		logger.WithError(err).Warn("failed to checkout target branch")
		resp := fmt.Sprintf("cannot checkout `%s`: %v", targetBranch, err)
		return s.createComment(logger, org, repo, num, comment, resp)
	}
	logger.WithField("duration", time.Since(startClone)).Info("Cloned and checked out target branch.")

	// Fetch the patch from GitHub
	localPath, err := s.getPatch(org, repo, targetBranch, num)
	if err != nil {
		logger.WithError(err).Errorf("Failed to get patch for %s/%s#%d", org, repo, num)
		return s.createComment(logger, org, repo, num, comment, fmt.Sprintf("Failed to get PR patch from GitHub. This PR will need to be manually cherrypicked.\n<details><summary>Error message</summary>%v</details>", err))
	}

	if err := r.Config("user.name", s.botUser.Login); err != nil {
		return fmt.Errorf("failed to configure git user: %w", err)
	}
	email := s.email
	if email == "" {
		email = s.botUser.Email
	}
	if err := r.Config("user.email", email); err != nil {
		return fmt.Errorf("failed to configure git email: %w", err)
	}

	// New branch for the cherry-pick.
	newBranch := fmt.Sprintf(cherryPickBranchFmt, num, targetBranch)

	// Check if that branch already exists, which means there is already a PR for that cherry-pick.
	if r.BranchExists(newBranch) {
		// Find the PR and link to it.
		prs, err := s.ghc.GetPullRequests(org, repo)
		if err != nil {
			return fmt.Errorf("failed to get pullrequests for %s/%s: %w", org, repo, err)
		}
		for _, pr := range prs {
			expectedHeads := sets.New(newBranch, fmt.Sprintf("%s:%s", opts.ForkOwner, newBranch))
			if expectedHeads.Has(pr.Head.Ref) {
				logger.WithField("preexisting_cherrypick", pr.HTMLURL).Info("PR already has cherrypick")
				resp := fmt.Sprintf("Looks like #%d has already been cherry picked in %s", num, pr.HTMLURL)
				return s.createComment(logger, org, repo, num, comment, resp)
			}
		}
	}

	// Create the branch for the cherry-pick.
	if err := r.CheckoutNewBranch(newBranch); err != nil {
		return fmt.Errorf("failed to checkout %s: %w", newBranch, err)
	}

	// Title for GitHub issue/PR.
	titleTargetBranchIndicator := fmt.Sprintf(titleTargetBranchIndicatorTemplate, targetBranch)
	title = fmt.Sprintf("%s%s", titleTargetBranchIndicator, omitBaseBranchFromTitle(title, baseBranch))

	conflicts := false
	if err := s.applyToBranch(r, org, repo, localPath, num); err != nil {
		logger.WithError(err).Warn("failed to apply PR on top of target branch")
		resp := fmt.Sprintf("#%d failed to apply on top of branch %q:\n```\n%v\n```", num, targetBranch, err)

		switch opts.OnConflict {
		case cherryconfig.OnConflictHold:
			// Keep the conflicts in the branch and open the pull request so
			// the requester can resolve them.
			if commitErr := s.commitConflicts(r, org, repo, num, opts.CopyIssueNumbersFromSquashedCommit); commitErr != nil {
				logger.WithError(commitErr).Warn("failed to commit the conflicted cherry-pick")
				if err := s.createComment(logger, org, repo, num, comment, resp); err != nil {
					return fmt.Errorf("failed to create comment: %w", err)
				}
				return commitErr
			}
			conflicts = true
		case cherryconfig.OnConflictIssue:
			if err := s.createComment(logger, org, repo, num, comment, resp); err != nil {
				return fmt.Errorf("failed to create comment: %w", err)
			}
			resp = fmt.Sprintf("Manual cherrypick required.\n\n%v", resp)
			if err := s.createIssue(logger, org, repo, title, resp, num, comment, nil, []string{requester}); err != nil {
				return fmt.Errorf("failed to create issue: %w", err)
			}
			return nil
		default:
			if err := s.createComment(logger, org, repo, num, comment, resp); err != nil {
				return fmt.Errorf("failed to create comment: %w", err)
			}
			return nil
		}
	}

	// Push the new branch, falling back to the base repository when the
	// configured fork is missing or not writable.
	pushOrg, pushErrs := s.pushBranch(r, targets, newBranch)
	if pushOrg == "" {
		pushErr := utilerrors.NewAggregate(pushErrs)
		logger.WithError(pushErr).Warn("failed to push cherry-picked changes to GitHub")
		resp := fmt.Sprintf("failed to push cherry-picked changes in GitHub: %v", pushErr)
		return utilerrors.NewAggregate([]error{pushErr, s.createComment(logger, org, repo, num, comment, resp)})
	}

	// Open a PR in GitHub.
	var kindLabels []string
	var sourceLabels []github.Label
	if labels, err := s.ghc.GetIssueLabels(org, repo, num); err != nil {
		logger.WithError(err).Debug("Failed to get issue labels, omitting kind commands and copied labels from the cherry-pick PR.")
	} else {
		sourceLabels = labels
		kindLabels = kindLabelsFromIssueLabels(labels)
	}
	var cherryPickBody string
	if opts.UseProwAssignments {
		cherryPickBody = cherrypicker.CreateCherrypickBody(num, requester, bodyForCherryPick(body), releaseNoteFromParentPR(body), chainBranches, kindLabels)
	} else {
		cherryPickBody = cherrypicker.CreateCherrypickBody(num, "", bodyForCherryPick(body), releaseNoteFromParentPR(body), chainBranches, kindLabels)
	}

	head := fmt.Sprintf("%s:%s", pushOrg, newBranch)
	if pushOrg == org {
		head = newBranch
	}

	createdNum, err := s.ghc.CreatePullRequest(org, repo, title, cherryPickBody, head, targetBranch, maintainerCanModify(pushOrg, org))
	if err != nil {
		logger.WithError(err).Warn("failed to create new pull request")
		resp := fmt.Sprintf("new pull request could not be created: %v", err)
		return utilerrors.NewAggregate([]error{err, s.createComment(logger, org, repo, num, comment, resp)})
	}
	logger = logger.WithField("new_pull_request_number", createdNum)
	resp := fmt.Sprintf("new pull request created: #%d", createdNum)
	logger.Info("new pull request created")
	if err := s.createComment(logger, org, repo, num, comment, resp); err != nil {
		return fmt.Errorf("failed to create comment: %w", err)
	}

	labelsToAdd := cherryPickLabels(opts, sourceLabels, targetBranch)
	if conflicts {
		labelsToAdd = append(labelsToAdd, "do-not-merge/hold")
	}
	for _, label := range labelsToAdd {
		if err := s.ghc.AddLabel(org, repo, createdNum, label); err != nil {
			return fmt.Errorf("failed to add label %s: %w", label, err)
		}
	}
	if opts.UseProwAssignments {
		if err := s.ghc.AssignIssue(org, repo, createdNum, []string{requester}); err != nil {
			logger.WithError(err).Warn("failed to assign to new PR")
			// Ignore returning errors on failure to assign as this is most likely
			// due to users not being members of the org so that they can't be assigned
			// in PRs.
			return nil
		}
	}

	if conflicts {
		instructions := fmt.Sprintf("@%s This PR has conflicts, I have held it. "+
			"Please resolve them or ask others to resolve them, then comment `/unhold` to remove the hold label.", requester)
		if err := s.ghc.CreateComment(org, repo, createdNum, instructions); err != nil {
			logger.WithError(err).Warn("failed to comment the conflict instructions")
		}
	}
	return nil
}

// commitConflicts commits the conflicted working tree, keeping the conflict
// markers, so the cherry-pick pull request can be opened and held.
func (s *Server) commitConflicts(r git.RepoClient, org, repo string, num int, copyIssueNumbers bool) error {
	pr, err := s.ghc.GetPullRequest(org, repo, num)
	if err != nil {
		return fmt.Errorf("failed to get pull request %s/%s#%d: %w", org, repo, num, err)
	}
	if pr.MergeSHA == nil {
		return fmt.Errorf("pull request %s/%s#%d has no merge commit to cherry-pick", org, repo, num)
	}

	ex := exec.New()
	dir := r.Directory()

	cherrypick := ex.Command("git", "cherry-pick", "-m", "1", "--cleanup=verbatim", *pr.MergeSHA)
	cherrypick.SetDir(dir)
	// A failure here means the conflicts are in the working tree; they are
	// committed below. Any other failure makes the commit fail as well.
	_, _ = cherrypick.CombinedOutput()

	add := ex.Command("git", "add", "-A")
	add.SetDir(dir)
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add the conflicted files: %w: %s", err, string(out))
	}

	commit := ex.Command("git", "commit", "-s", "-m", s.cherryPickCommitMessage(org, repo, num, copyIssueNumbers))
	commit.SetDir(dir)
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to commit the conflicted cherry-pick: %w: %s", err, string(out))
	}
	return nil
}

// cherryPickCommitMessage builds the commit message of a conflicted
// cherry-pick, optionally carrying the issue references of the squashed commit.
func (s *Server) cherryPickCommitMessage(org, repo string, num int, copyIssueNumbers bool) string {
	message := fmt.Sprintf("This is an automated cherry-pick of #%d", num)
	if !copyIssueNumbers {
		return message
	}

	pr, err := s.ghc.GetPullRequest(org, repo, num)
	if err != nil || pr.MergeSHA == nil {
		return message
	}
	commit, err := s.ghc.GetSingleCommit(org, repo, *pr.MergeSHA)
	if err != nil {
		return message
	}

	numbers := github.NormalizeIssueNumbers(commit.Commit.Message, org, repo)
	if len(numbers) == 0 {
		return message
	}
	texts := make([]string, 0, len(numbers))
	for _, number := range numbers {
		texts = append(texts, fmt.Sprintf("%s %s/%s#%d", number.AssociatePrefix, number.Org, number.Repo, number.Number))
	}
	return fmt.Sprintf("%s\n\n%s", message, strings.Join(texts, ", "))
}

// maintainerCanModify reports whether the maintainer edit permission can be
// requested. GitHub rejects a pull request created from a fork when the request
// asks for it, because the base repository cannot grant the fork owner
// collaboration (fork_collab), so it is only requested for same-repository
// heads.
func maintainerCanModify(pushOrg, org string) bool {
	return pushOrg == org
}

func (s *Server) applyToBranch(r git.RepoClient, org, repo, localPath string, num int) error {
	var errs []error

	// try to apply the patch using git am.
	{
		err := r.Am(localPath)
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("[try 1] failed to `git am`: %w", err))
	}
	// try to cherry-pick the merge commit
	{
		pr, err := s.ghc.GetPullRequest(org, repo, num)
		if err != nil {
			return fmt.Errorf("[try 2] failed to get pull request %s/%s#%d: %w", org, repo, num, err)
		}
		if pr.MergeSHA == nil {
			return utilerrors.NewAggregate(errs)
		}
		cherrypickCmd := exec.New().Command("git", "cherry-pick", "-m", "1", "--cleanup=verbatim", *pr.MergeSHA)
		cherrypickCmd.SetDir(r.Directory())
		out, err := cherrypickCmd.CombinedOutput()
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("[try 2] Failed to cherry-pick: %v\n```\n%v\n```", err, string(out)))
	}

	return utilerrors.NewAggregate(errs)
}

// omitBaseBranchFromTitle returns the title without the base branch's
// indicator, if there is one. We do this to avoid long cherry-pick titles when
// doing a backport of a backport.
//
// Example of long cherry-pick titles:
// Original PR title: "Hello world"
// Backport to release-9.9 title: "[release-9.9] Hello world"
// Backport to release-9.8 title: "[release-9.8] [release-9.9] Hello world"
//
// This function helps by making the second backport title
// be "[release-9.8] Hello world" instead, by deleting the first occurrence
// of "[release-9.9]" from the first backport's title.
//
// When baseBranch is empty, this function simply returns the title as-is for convenience.
func omitBaseBranchFromTitle(title, baseBranch string) string {
	if baseBranch == "" {
		return title
	}

	return strings.Replace(title, fmt.Sprintf(titleTargetBranchIndicatorTemplate, baseBranch), "", 1)
}

func (s *Server) createComment(l logrus.FieldLogger, org, repo string, num int, comment *github.IssueComment, resp string) error {
	if err := func() error {
		if comment != nil {
			return s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(*comment, resp))
		}
		return s.ghc.CreateComment(org, repo, num, fmt.Sprintf("In response to a cherrypick label: %s", resp))
	}(); err != nil {
		l.WithError(err).Warn("failed to create comment")
		return err
	}
	l.Debug("Created comment")
	return nil
}

// inviteCollaborator adds the requester of a cherry-pick pull request as a
// collaborator of the bot fork so they can push conflict resolutions.
func (s *Server) inviteCollaborator(log logrus.FieldLogger, ic github.IssueCommentEvent) error {
	org := ic.Repo.Owner.Login
	repo := ic.Repo.Name
	num := ic.Issue.Number
	user := ic.Comment.User.Login
	forkOwner := s.options(org, repo).ForkOwner

	comment := func(message string) error {
		return s.ghc.CreateComment(org, repo, num, plugins.FormatICResponse(ic.Comment, message))
	}

	if forkOwner == "" {
		return comment(fmt.Sprintf("no invitation is needed: cherry-pick branches are created in `%s/%s`.", org, repo))
	}

	pr, err := s.ghc.GetPullRequest(org, repo, num)
	if err != nil {
		return fmt.Errorf("failed to get pull request %s/%s#%d: %w", org, repo, num, err)
	}
	if pr.Head.Repo.Owner.Login != forkOwner {
		return comment(fmt.Sprintf("no invitation is needed: this pull request was not created from the `%s` fork.", forkOwner))
	}

	collaborators, err := s.ghc.ListCollaborators(forkOwner, repo)
	if err != nil {
		return fmt.Errorf("failed to list collaborators of %s/%s: %w", forkOwner, repo, err)
	}
	for _, collaborator := range collaborators {
		if collaborator.Login == user {
			return comment(fmt.Sprintf("you are already a collaborator of `%s/%s`.", forkOwner, repo))
		}
	}

	invitations, err := s.ghc.ListRepoInvitations(forkOwner, repo)
	if err != nil {
		return fmt.Errorf("failed to list invitations of %s/%s: %w", forkOwner, repo, err)
	}
	for _, invitation := range invitations {
		if invitation.Invitee != nil && invitation.Invitee.Login == user {
			log.WithField("invitation", invitation.URL).Info("The user is already invited to the fork.")
			return nil
		}
	}

	member, err := s.ghc.IsMember(org, user)
	if err != nil {
		return err
	}
	if !member {
		return comment(fmt.Sprintf("you are not a member of org `%s`, so you cannot be added to the fork.", org))
	}

	if err := s.ghc.AddCollaborator(forkOwner, repo, user, github.Write); err != nil {
		log.WithError(err).Warn("failed to add the collaborator")
		return comment(fmt.Sprintf("failed to invite you as a collaborator of `%s/%s`: %v", forkOwner, repo, err))
	}

	return comment(fmt.Sprintf("Please accept the invitation, then you can push to the cherry-pick pull requests. "+
		"Comment with `/cherry-pick-invite` if the invitation expired. %s/%s/%s/invitations",
		s.githubURL, forkOwner, repo))
}

// createIssue creates an issue on GitHub.
func (s *Server) createIssue(l logrus.FieldLogger, org, repo, title, body string, num int, comment *github.IssueComment, labels, assignees []string) error {
	issueNum, err := s.ghc.CreateIssue(org, repo, title, body, 0, labels, assignees)
	if err != nil {
		return s.createComment(l, org, repo, num, comment, fmt.Sprintf("new issue could not be created for failed cherrypick: %v", err))
	}

	return s.createComment(l, org, repo, num, comment, fmt.Sprintf("new issue created for failed cherrypick: #%d", issueNum))
}

func (s *Server) pushTargets(l logrus.FieldLogger, org, repo string) []pushTarget {
	central := pushTarget{org: org, pusher: &centralPusher{}}
	forkOwner := s.options(org, repo).ForkOwner
	if forkOwner == "" || s.installationToken == nil {
		return []pushTarget{central}
	}

	if _, err := s.ghc.GetRepo(forkOwner, repo); err != nil {
		l.WithError(err).WithFields(logrus.Fields{
			github.OrgLogField:  org,
			github.RepoLogField: repo,
		}).Info("Bot fork is not available, pushing the cherry-pick branch into the base repository.")
		return []pushTarget{central}
	}

	fork := pushTarget{
		org: forkOwner,
		pusher: &authenticatedPusher{
			owner:     forkOwner,
			repo:      repo,
			githubURL: s.githubURL,
			token:     s.installationToken,
		},
	}
	return []pushTarget{fork, central}
}

// pushBranch pushes the branch to the first target that accepts it and returns
// the owner the branch landed in. An empty owner means every target failed.
func (s *Server) pushBranch(r git.RepoClient, targets []pushTarget, branch string) (string, []error) {
	var errs []error
	for _, target := range targets {
		if err := target.pusher.Push(r, branch, true); err != nil {
			errs = append(errs, err)
			continue
		}
		return target.org, errs
	}
	return "", errs
}

// getPatch gets the patch for the provided PR and creates a local
// copy of it. It returns its location in the filesystem and any
// encountered error.
func (s *Server) getPatch(org, repo, targetBranch string, num int) (string, error) {
	patch, err := s.ghc.GetPullRequestPatch(org, repo, num)
	if err != nil {
		return "", err
	}

	localPath := fmt.Sprintf("/tmp/%s_%s_%d_%s.patch", org, repo, num, normalize(targetBranch))
	if err := os.WriteFile(localPath, patch, 0644); err != nil {
		return "", err
	}

	return localPath, nil
}

func normalize(input string) string {
	return strings.ReplaceAll(input, "/", "-")
}

// releaseNoteNoteFromParentPR gets the release note from the
// parent PR and formats it as per the PR template so that
// it can be copied to the cherry-pick PR.
func releaseNoteFromParentPR(body string) string {
	potentialMatch := releaseNoteRe.FindStringSubmatch(body)
	if potentialMatch == nil {
		return ""
	}
	return fmt.Sprintf("```release-note\n%s\n```", strings.TrimSpace(potentialMatch[1]))
}

// bodyForCherryPick returns the source PR body that is copied into the
// cherry-pick PR. Issue references of the source PR survive this way, which
// release tooling needs to associate the cherry-pick with the same issue.
//
// Cherry-pick commands are dropped: the plugin treats a PR body as a comment
// when the PR merges, so a copied command would start another pick.
func bodyForCherryPick(body string) string {
	return strings.TrimSpace(cherryPickRe.ReplaceAllString(body, ""))
}

// cherryPickLabels returns the labels to apply to a new cherry-pick PR: the
// configured labels, the picked label for the target branch, and the source PR
// labels except the excluded ones and the cherry-pick request labels. Kind
// labels are omitted because they are re-applied through /kind commands in the
// PR body.
func cherryPickLabels(opts cherryconfig.Options, sourceLabels []github.Label, targetBranch string) []string {
	labels := sets.New[string](opts.Labels...)
	if opts.PickedLabelPrefix != "" {
		labels.Insert(opts.PickedLabelPrefix + targetBranch)
	}
	excluded := sets.New[string](opts.ExcludeLabels...)
	for _, label := range sourceLabels {
		if excluded.Has(label.Name) || hasLabelPrefix(label.Name, opts.LabelPrefix) || strings.HasPrefix(label.Name, kindLabelPrefix) {
			continue
		}
		labels.Insert(label.Name)
	}
	return sets.List(labels)
}

const kindLabelPrefix = "kind/"

// kindLabelsFromIssueLabels returns the kind names (without "kind/" prefix) from
// the given labels, deduplicated and sorted. Labels that are not kind/* or have
// an empty name after stripping the prefix are skipped.
func kindLabelsFromIssueLabels(labels []github.Label) []string {
	kindSet := sets.New[string]()
	for _, label := range labels {
		if !strings.HasPrefix(label.Name, kindLabelPrefix) {
			continue
		}
		name := strings.TrimPrefix(label.Name, kindLabelPrefix)
		if name != "" {
			kindSet.Insert(name)
		}
	}
	return sets.List(kindSet)
}
