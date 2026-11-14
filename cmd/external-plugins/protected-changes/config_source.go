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
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"sigs.k8s.io/prow/cmd/external-plugins/protected-changes/rules"
)

// configSource serves the policy and reloads it when the file changes.
//
// The file is usually a ConfigMap volume: Kubernetes replaces the whole mount,
// so a stat-based watcher can miss an update. Re-reading this small document and
// comparing its digest is both simpler and reliable.
type configSource struct {
	path string
	log  *logrus.Entry

	mu     sync.RWMutex
	config *rules.Config
}

func newConfigSource(path string, log *logrus.Entry) (*configSource, error) {
	source := &configSource{path: path, log: log}
	if err := source.reload(); err != nil {
		return nil, err
	}
	return source, nil
}

// Current returns the policy in force. The returned document is never mutated.
func (s *configSource) Current() *rules.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// reload swaps in the policy when its content changed. A broken file keeps the
// previous policy in force instead of disabling the check.
func (s *configSource) reload() error {
	config, err := rules.LoadConfig(s.path)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config != nil && s.config.Hash() == config.Hash() {
		return nil
	}
	s.config = config
	s.log.WithField("config-hash", config.Hash()).Info("Loaded protected-changes policy.")
	return nil
}

// watch reloads the policy every interval until ctx is done.
func (s *configSource) watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.reload(); err != nil {
				s.log.WithError(err).Error("Failed to reload the protected-changes policy; keeping the previous one.")
			}
		}
	}
}
