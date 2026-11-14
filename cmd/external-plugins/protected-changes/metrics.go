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

import "github.com/prometheus/client_golang/prometheus"

var (
	webhookEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "protected_changes_webhook_events_total",
		Help: "Webhook events received, by event type and whether they were evaluated.",
	}, []string{"event_type", "outcome"})

	evaluations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "protected_changes_evaluations_total",
		Help: "Pull request evaluations, by result.",
	}, []string{"result"})

	evaluationDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "protected_changes_evaluation_duration_seconds",
		Help:    "How long one pull request evaluation takes.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 40},
	})

	githubErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "protected_changes_github_errors_total",
		Help: "GitHub calls that failed, by operation.",
	}, []string{"operation"})

	matchCacheHits = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "protected_changes_match_cache_hits_total",
		Help: "Evaluations that reused the matched rules of a previous run.",
	})

	reportedStatuses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "protected_changes_status_total",
		Help: "Statuses posted to GitHub, by state.",
	}, []string{"state"})
)

func init() {
	prometheus.MustRegister(webhookEvents)
	prometheus.MustRegister(evaluations)
	prometheus.MustRegister(evaluationDuration)
	prometheus.MustRegister(githubErrors)
	prometheus.MustRegister(matchCacheHits)
	prometheus.MustRegister(reportedStatuses)
}
