package quota

import (
	"sync"
	"time"
)

// Cache stores live quota snapshots per credential with TTL freshness and
// bounded stale fallback. Snapshots are treated as immutable after Put.
type Cache struct {
	ttl      time.Duration
	staleMax time.Duration
	now      func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	snapshot *Snapshot
	storedAt time.Time
}

// NewCache builds a cache with the given fresh TTL and stale horizon.
// A non-positive staleMax disables stale fallback.
func NewCache(ttl, staleMax time.Duration) *Cache {
	return &Cache{
		ttl:      ttl,
		staleMax: staleMax,
		now:      time.Now,
		entries:  make(map[string]cacheEntry),
	}
}

// Get returns the snapshot and its freshness. fresh is true within TTL;
// stale is true past TTL but within the stale horizon (exclusive of fresh).
func (c *Cache) Get(key string) (snapshot *Snapshot, fresh, stale bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || entry.snapshot == nil {
		return nil, false, false
	}
	age := now.Sub(entry.storedAt)
	if age < 0 {
		age = 0
	}
	if age < c.ttl {
		return entry.snapshot, true, false
	}
	if c.staleMax > 0 && age < c.staleMax {
		return entry.snapshot, false, true
	}
	return nil, false, false
}

// Put stores a snapshot, stamping its observation time. A nil snapshot
// deletes the entry.
func (c *Cache) Put(key string, snapshot *Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if snapshot == nil {
		delete(c.entries, key)
		return
	}
	storedAt := c.now()
	snapshot.ObservedAt = storedAt
	c.entries[key] = cacheEntry{snapshot: snapshot, storedAt: storedAt}
}

// Invalidate drops one entry, returning whether it existed.
func (c *Cache) Invalidate(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		return false
	}
	delete(c.entries, key)
	return true
}

// Len reports the number of stored entries (including expired ones).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
