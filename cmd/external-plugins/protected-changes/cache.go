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
	"hash/fnv"
	"sync"
	"time"

	"sigs.k8s.io/prow/cmd/external-plugins/protected-changes/rules"
)

const lockStripes = 64

// cacheKey identifies one evaluation. A new commit or a policy change produces a
// new key, so a stale result can never be reused for different content.
type cacheKey struct {
	repo       string
	number     int
	sha        string
	configHash string
}

type cacheEntry struct {
	matches  []rules.Match
	storedAt time.Time
}

// cache remembers the rules a pull request commit matched, so an approval
// comment only has to recompute the approval half of the verdict.
//
// Striped locks serialize the handlers for the same pull request: a webhook
// retry or a comment arriving while a synchronize is in flight must not post two
// statuses or two advisory comments.
type cache struct {
	ttl        time.Duration
	maxEntries int

	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
	locks   [lockStripes]sync.Mutex
}

func newCache(ttl time.Duration, maxEntries int) *cache {
	return &cache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    map[cacheKey]cacheEntry{},
	}
}

// lock serializes work for one pull request and returns the unlock function.
func (c *cache) lock(repo string, number int) func() {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(repo))
	stripe := int(hasher.Sum32()+uint32(number)) % lockStripes

	c.locks[stripe].Lock()
	return c.locks[stripe].Unlock
}

func (c *cache) matches(key cacheKey) ([]rules.Match, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.storedAt) > c.ttl {
		delete(c.entries, key)
		return nil, false
	}

	return entry.matches, true
}

func (c *cache) storeMatches(key cacheKey, matches []rules.Match) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}

	c.entries[key] = cacheEntry{matches: matches, storedAt: time.Now()}
}

func (c *cache) evictLocked() {
	now := time.Now()
	for key, entry := range c.entries {
		if now.Sub(entry.storedAt) > c.ttl {
			delete(c.entries, key)
		}
	}

	for key := range c.entries {
		if len(c.entries) < c.maxEntries {
			return
		}
		delete(c.entries, key)
	}
}
