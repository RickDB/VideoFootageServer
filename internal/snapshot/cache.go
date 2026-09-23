// Package snapshot caches camera preview images fetched from a recorder.
// Fetching a snapshot means talking to the NVR over ISAPI, which is slow
// enough that a form showing dozens of cameras needs both caching (so
// re-opening the same form doesn't re-fetch every image) and de-duplication
// of concurrent requests for the same camera (so several browser tabs, or
// the same page's own parallel thumbnail loads racing a cold cache, don't
// each trigger a separate hit on the recorder).
package snapshot

import (
	"context"
	"sync"
	"time"
)

// FetchFunc retrieves a fresh image for whatever a cache key represents.
type FetchFunc func(ctx context.Context) (data []byte, contentType string, err error)

type entry struct {
	data        []byte
	contentType string
	at          time.Time
}

// call represents one in-flight fetch that other callers for the same key
// wait on rather than starting their own request (a simple, dependency-free
// singleflight).
type call struct {
	done        chan struct{}
	data        []byte
	contentType string
	err         error
}

type Cache struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[int64]entry
	calls map[int64]*call
}

func New(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, items: map[int64]entry{}, calls: map[int64]*call{}}
}

// Get returns the cached image for key if it's still fresh, otherwise calls
// fetch exactly once even under concurrent callers for the same key, caches
// a successful result, and returns it. A failed fetch is never cached, so
// the next request (or the next parallel caller once the in-flight call
// completes) retries rather than being stuck with a stored error.
func (c *Cache) Get(ctx context.Context, key int64, fetch FetchFunc) ([]byte, string, error) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok && time.Since(e.at) < c.ttl {
		c.mu.Unlock()
		return e.data, e.contentType, nil
	}
	if existing, ok := c.calls[key]; ok {
		c.mu.Unlock()
		<-existing.done
		return existing.data, existing.contentType, existing.err
	}
	cl := &call{done: make(chan struct{})}
	c.calls[key] = cl
	c.mu.Unlock()

	data, contentType, err := fetch(ctx)
	cl.data, cl.contentType, cl.err = data, contentType, err
	close(cl.done)

	c.mu.Lock()
	delete(c.calls, key)
	if err == nil {
		c.items[key] = entry{data: data, contentType: contentType, at: time.Now()}
	}
	c.mu.Unlock()
	return data, contentType, err
}
