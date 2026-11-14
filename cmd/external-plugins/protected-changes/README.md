# protected-changes

Reports whether a pull request adds lines that a repository policy protects, and
whether a listed reviewer approved them. It replaces the `pull-error-log-review`
presubmit jobs: one long-running process with the Prow GitHub App credentials
instead of a per-pull-request pod holding a GitHub token.

## What it publishes

- a commit status with context `protected-changes`, on every commit of a pull
  request that the policy covers;
- one advisory comment per pull request, updated in place, that names the files,
  rules and required reviewers when an approval is missing. The comment is
  deleted once the pull request no longer touches protected files or is approved.

Whether the status blocks a merge is decided by tide (`required-contexts`), not by
this plugin.

## Events

| Event | Action | Handling |
| --- | --- | --- |
| `pull_request` | `opened`, `synchronize`, `reopened` | recompute rules and approval |
| `pull_request_review` | `submitted`, `dismissed` | recompute approval, reuse the rules computed for the current commit |
| `issue_comment` | `created`, body carries `/approve` or `/lgtm` (or a `cancel` form) | recompute approval, same reuse |

Every other event is acknowledged without a GitHub call: `hook` dispatches
webhooks synchronously for the whole instance, so this handler must answer fast
and does the work in the background. Only a comment that carries an approval
command can change the verdict, and a status identical to the one already
published for the commit is not posted again.

## Approval sources

No bot login is hardcoded. The plugin resolves its own identity from the GitHub
App it runs as and only rewrites comments it wrote itself.

An approval is read from the same signals the `approve` plugin uses, so the
verdict agrees with what that plugin reports:

- a review with state `APPROVED` (and `CHANGES_REQUESTED` or a dismissal
  withdraws it);
- a `/approve` comment, or `/approve cancel`;
- `/lgtm` and `/lgtm cancel`, where the approve plugin is configured to accept it
  (`lgtm_acts_as_approve`).

Reviews and commands feed one timeline, so the latest action of a person wins:
`/approve cancel` after a review approval withdraws it, just as it does for the
approve plugin. `--plugin-config` points at the plugin config hook already mounts
(`/etc/prow-plugin/plugins.yaml`); the `approve` section of that file decides
whether reviews and `/lgtm` count. Without the file the approve plugin's defaults
apply (reviews count, `/lgtm` does not).

The resulting approvers are filtered by the repository's `approvers` list, so who
may approve does not change: only how an approval is detected.

## Policy

`--config` points at a YAML document. It is re-read every
`--config-reload-interval`, and a changed content is swapped in without a restart;
a broken file keeps the previous policy in force.

```yaml
repositories:
  - name: "pingcap/tidb"
    excludes: ["vendor/**"]        # repository-wide, checked before any rule
    patterns:
      - name: "go_mod_version"
        description: "go and toolchain directives need review"
        regex: '^\s*(?:go|toolchain)\s+.*$'
        files: ["go.mod"]          # optional; a bare name matches the basename
        excludes: ["tools/**"]     # optional, relative to the file path
    approvers: ["XuHuaiyu"]        # verbatim GitHub logins, case sensitive
settings:
  min_approvals: 1
  check_behavior:
    mode: "check_and_fail"         # or "check_and_warn"
  hint: "How to change the policy."
```

- A rule without `files` applies to every changed file.
- A pattern without `/` in `files` matches the file basename, so `go.mod` also
  covers `tools/foo/go.mod`.
- Only added lines are matched.
- `min_approvals` and `check_behavior.mode` fall back to `1` and
  `check_and_fail` when absent, so a truncated document cannot turn the check
  off.
- An internal failure publishes an `error` status, never a success.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | `/etc/protected-changes/config.yaml` | policy file |
| `--plugin-config` | `/etc/prow-plugin/plugins.yaml` | the `approve` settings that decide what counts as an approval |
| `--config-reload-interval` | `30s` | how often the policy is re-read |
| `--port` | `8888` | webhook listener |
| `--hmac-secret-file` | `/etc/webhook/hmac` | webhook HMAC secret |
| `--metrics-port` / `--health-port` | `9090` / `8081` | metrics and probes |
| `--dry-run` | `false` | log the verdict instead of posting it |
| `--github-app-id`, `--github-app-private-key-path` | | GitHub App credentials |
| `--github-endpoint` | GitHub API | point at ghproxy to reuse its cache |

Metrics: `protected_changes_webhook_events_total`,
`protected_changes_evaluations_total`,
`protected_changes_evaluation_duration_seconds`,
`protected_changes_github_errors_total`,
`protected_changes_match_cache_hits_total`,
`protected_changes_status_total`.
