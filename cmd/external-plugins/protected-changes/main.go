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

// Protected-changes reports whether a pull request adds lines that a repository
// policy protects and whether a listed reviewer approved them.
//
// It replaces the pull-error-log-review presubmit jobs: instead of a per-pull-request
// pod that holds a GitHub token, one long-running process uses the Prow GitHub App,
// reloads its policy from a mounted ConfigMap and re-evaluates on every push and
// approval notification.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	"sigs.k8s.io/prow/pkg/config/secret"
	"sigs.k8s.io/prow/pkg/flagutil"
	prowflagutil "sigs.k8s.io/prow/pkg/flagutil"
	"sigs.k8s.io/prow/pkg/interrupts"
	"sigs.k8s.io/prow/pkg/logrusutil"
	"sigs.k8s.io/prow/pkg/pjutil"
	"sigs.k8s.io/prow/pkg/pluginhelp/externalplugins"
)

const (
	defaultConfigPath       = "/etc/protected-changes/config.yaml"
	defaultPluginConfigPath = "/etc/prow-plugin/plugins.yaml"
	defaultCacheTTL         = 6 * time.Hour
	defaultCacheSize        = 5000
	defaultReloadInterval   = 30 * time.Second
)

type options struct {
	port              int
	dryRun            bool
	config            string
	pluginConfig      string
	reloadInterval    time.Duration
	logLevel          string
	webhookSecretFile string

	github                 prowflagutil.GitHubOptions
	instrumentationOptions prowflagutil.InstrumentationOptions
}

func (o *options) Validate() error {
	for _, group := range []flagutil.OptionGroup{&o.github} {
		if err := group.Validate(o.dryRun); err != nil {
			return err
		}
	}
	if o.config == "" {
		return fmt.Errorf("-config must be set")
	}
	if o.reloadInterval <= 0 {
		return fmt.Errorf("-config-reload-interval must be positive")
	}
	if _, err := logrus.ParseLevel(o.logLevel); err != nil {
		return fmt.Errorf("invalid -log-level: %w", err)
	}
	return nil
}

func gatherOptions() options {
	var o options
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.IntVar(&o.port, "port", 8888, "Port to listen on.")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Report to the log instead of posting statuses and comments. Set explicitly to true for local testing.")
	fs.StringVar(&o.config, "config", defaultConfigPath, "Path to the protected-changes policy file.")
	fs.StringVar(&o.pluginConfig, "plugin-config", defaultPluginConfigPath, "Path to the Prow plugin config; the approve plugin's settings decide whether a review or /lgtm counts as an approval. Missing file means the plugin defaults.")
	fs.DurationVar(&o.reloadInterval, "config-reload-interval", defaultReloadInterval, "How often to re-read the policy file; a changed content is swapped in without a restart.")
	fs.StringVar(&o.logLevel, "log-level", "info", fmt.Sprintf("Log level is one of %v.", logrus.AllLevels))
	fs.StringVar(&o.webhookSecretFile, "hmac-secret-file", "/etc/webhook/hmac", "Path to the file containing the GitHub HMAC secret.")

	for _, group := range []flagutil.OptionGroup{&o.github, &o.instrumentationOptions} {
		group.AddFlags(fs)
	}
	fs.Parse(os.Args[1:])
	return o
}

func main() {
	logrusutil.ComponentInit()

	o := gatherOptions()
	if err := o.Validate(); err != nil {
		logrus.Fatalf("Invalid options: %v", err)
	}

	level, err := logrus.ParseLevel(o.logLevel)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to parse the log level.")
	}
	logrus.SetLevel(level)

	log := logrus.StandardLogger().WithField("plugin", pluginName)

	if err := secret.Add(o.webhookSecretFile); err != nil {
		log.WithError(err).Fatal("Error starting the secrets agent.")
	}

	githubClient, err := o.github.GitHubClient(o.dryRun)
	if err != nil {
		log.WithError(err).Fatal("Error getting a GitHub client.")
	}

	// Resolve the identity this plugin runs as, instead of hardcoding a bot
	// login: it decides which comments it may rewrite and which of its own
	// comments must not re-trigger a check.
	isSelf, err := githubClient.BotUserChecker()
	if err != nil {
		if !o.dryRun {
			log.WithError(err).Fatal("Error resolving the plugin identity.")
		}
		log.WithError(err).Warn("Cannot resolve the plugin identity in a dry run; no comment will be considered the plugin's own.")
		isSelf = func(string) bool { return false }
	}

	plugins, err := loadPluginConfig(o.pluginConfig)
	if err != nil {
		log.WithError(err).Fatal("Error loading the plugin config.")
	}

	configs, err := newConfigSource(o.config, log)
	if err != nil {
		log.WithError(err).Fatal("Error loading the protected-changes policy.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go configs.watch(ctx, o.reloadInterval)

	serv := &server{
		tokenGenerator: secret.GetTokenGenerator(o.webhookSecretFile),
		configs:        configs,
		handler:        newHandler(configs, githubClient, newCache(defaultCacheTTL, defaultCacheSize), o.dryRun, isSelf, plugins, log),
		log:            log,
	}

	health := pjutil.NewHealthOnPort(o.instrumentationOptions.HealthPort)
	health.ServeReady()

	mux := http.NewServeMux()
	mux.Handle("/", serv)
	externalplugins.ServeExternalPluginHelp(mux, log, helpProvider)

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())

	defer interrupts.WaitForGracefulShutdown()
	interrupts.ListenAndServe(&http.Server{Addr: ":" + strconv.Itoa(o.instrumentationOptions.MetricsPort), Handler: metricsMux}, 5*time.Second)
	interrupts.ListenAndServe(&http.Server{Addr: ":" + strconv.Itoa(o.port), Handler: mux}, 5*time.Second)
}
