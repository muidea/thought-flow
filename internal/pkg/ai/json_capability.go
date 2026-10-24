package ai

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"

	"thoughtflow/internal/pkg/appconfig"
)

// Module-specific provider instances share capability negotiation for the same
// endpoint, credential and model. Keep this process-local so restarting probes
// again after an upstream capability change. Never store or log a raw API key.
var jsonFormatCapabilities = newJSONCapabilityCache(256)

// Providers retain only their key. Each operation leases the canonical state,
// so evicting an idle entry cannot split active providers into independent probes.
// No TTL: a hot identity keeps its unsupported decision until configuration
// changes or process restart. Capacity pressure can discard only idle identities.
type jsonCapabilityEntry struct {
	state   *jsonFormatCapability
	users   int
	touched time.Time
}
type jsonCapabilityCache struct {
	mu      sync.Mutex
	entries map[jsonFormatCapabilityKey]*jsonCapabilityEntry
	limit   int
	changed chan struct{}
}

func newJSONCapabilityCache(limit int) *jsonCapabilityCache {
	return &jsonCapabilityCache{entries: make(map[jsonFormatCapabilityKey]*jsonCapabilityEntry), limit: max(1, limit), changed: make(chan struct{})}
}
func (c *jsonCapabilityCache) lease(ctx context.Context, key jsonFormatCapabilityKey) (*jsonFormatCapability, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		c.mu.Lock()
		entry := c.entries[key]
		if entry == nil && len(c.entries) >= c.limit {
			var victim jsonFormatCapabilityKey
			var oldest *jsonCapabilityEntry
			for candidate, e := range c.entries {
				if e.users == 0 && (oldest == nil || e.touched.Before(oldest.touched)) {
					victim, oldest = candidate, e
				}
			}
			if oldest != nil {
				delete(c.entries, victim)
			} else {
				wait := c.changed
				c.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-wait:
					continue
				}
			}
		}
		if entry == nil {
			entry = &jsonCapabilityEntry{state: &jsonFormatCapability{}}
			c.entries[key] = entry
		}
		entry.users++
		entry.touched = time.Now()
		c.mu.Unlock()
		var once sync.Once
		return entry.state, func() {
			once.Do(func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				entry.users--
				entry.touched = time.Now()
				close(c.changed)
				c.changed = make(chan struct{})
			})
		}, nil
	}
}

type jsonFormatCapabilityKey struct {
	endpoint   string
	model      string
	credential [sha256.Size]byte
}

type jsonFormatCapability struct {
	mu          sync.Mutex
	known       bool
	unsupported bool
	probing     chan struct{}
}

func jsonFormatKey(cfg appconfig.LLMConfig, model string) jsonFormatCapabilityKey {
	return jsonFormatCapabilityKey{endpoint: openAICompatibleBaseURL(cfg.BaseURL), model: model,
		credential: sha256.Sum256([]byte(strings.TrimSpace(cfg.APIKey)))}
}

// acquire coordinates only the initial probe. Once support is known, requests
// run concurrently. The probe owner must call complete before starting fallback
// so waiters can proceed without waiting for its generated content.
func (c *jsonFormatCapability) acquire(ctx context.Context) (skipFormat bool, complete func(error), err error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		c.mu.Lock()
		if c.unsupported {
			c.mu.Unlock()
			return true, nil, nil
		}
		if pending := c.probing; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return false, nil, ctx.Err()
			case <-pending:
				continue
			}
		}
		var probe chan struct{}
		if !c.known {
			probe = make(chan struct{})
			c.probing = probe
		}
		c.mu.Unlock()
		return false, func(err error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			var failure ProviderError
			if errors.As(err, &failure) && failure.onlyResponseFormatUnsupported() {
				c.known = true
				c.unsupported = true
			} else if err == nil {
				c.known = true
			}
			if probe != nil {
				c.probing = nil
				close(probe)
			}
		}, nil
	}
}
