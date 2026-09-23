package snapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentGetsFetchOnce(t *testing.T) {
	c := New(time.Hour)
	var calls int32
	fetch := func(ctx context.Context) ([]byte, string, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		return []byte("image"), "image/jpeg", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, ct, err := c.Get(context.Background(), 1, fetch)
			if err != nil || string(data) != "image" || ct != "image/jpeg" {
				t.Errorf("unexpected result: %q %q %v", data, ct, err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected fetch to run exactly once for concurrent callers, ran %d times", got)
	}
}

func TestCacheHitAvoidsRefetch(t *testing.T) {
	c := New(time.Hour)
	var calls int32
	fetch := func(ctx context.Context) ([]byte, string, error) {
		atomic.AddInt32(&calls, 1)
		return []byte("v1"), "image/jpeg", nil
	}
	if _, _, err := c.Get(context.Background(), 1, fetch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(context.Background(), 1, fetch); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 fetch for two calls within TTL, got %d", got)
	}
}

func TestExpiredEntryIsRefetched(t *testing.T) {
	c := New(10 * time.Millisecond)
	var calls int32
	fetch := func(ctx context.Context) ([]byte, string, error) {
		atomic.AddInt32(&calls, 1)
		return []byte("v"), "image/jpeg", nil
	}
	if _, _, err := c.Get(context.Background(), 1, fetch); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, err := c.Get(context.Background(), 1, fetch); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected a refetch after TTL expiry, got %d calls", got)
	}
}

func TestFailedFetchIsNotCached(t *testing.T) {
	c := New(time.Hour)
	var calls int32
	failing := errors.New("recorder unreachable")
	fetch := func(ctx context.Context) ([]byte, string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, "", failing
	}
	if _, _, err := c.Get(context.Background(), 1, fetch); !errors.Is(err, failing) {
		t.Fatalf("expected failing error, got %v", err)
	}
	if _, _, err := c.Get(context.Background(), 1, fetch); !errors.Is(err, failing) {
		t.Fatalf("expected failing error on retry, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected a fresh fetch attempt each time since failures aren't cached, got %d calls", got)
	}
}
