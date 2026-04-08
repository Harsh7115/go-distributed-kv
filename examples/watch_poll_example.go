// examples/watch_poll_example.go
//
// Demonstrates a "watch" pattern on top of go-distributed-kv's HTTP API:
// a goroutine polls a key at a configurable interval and notifies a channel
// whenever the value changes.  A second goroutine periodically writes new
// values so you can see the watcher fire in real time.
//
// Usage:
//   go run examples/watch_poll_example.go -addr http://localhost:8080
//
// The program runs for 30 seconds then exits cleanly.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── client helpers ────────────────────────────────────────────────────────────

type KVClient struct {
	addr   string
	client *http.Client
}

func NewKVClient(addr string) *KVClient {
	return &KVClient{
		addr:   strings.TrimRight(addr, "/"),
		client: &http.Client{Timeout: 3 * time.Second},
	}
}

// Get returns (value, exists, error).
func (c *KVClient) Get(ctx context.Context, key string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/kv/%s", c.addr, key), nil)
	if err != nil {
		return "", false, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("GET %s: status %d", key, resp.StatusCode)
	}

	var result struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false, err
	}
	return result.Value, true, nil
}

// Put sets key=value.
func (c *KVClient) Put(ctx context.Context, key, value string) error {
	body := fmt.Sprintf(`{"value":%q}`, value)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("%s/kv/%s", c.addr, key),
		io.NopCloser(strings.NewReader(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT %s: status %d", key, resp.StatusCode)
	}
	return nil
}

// ── watcher ───────────────────────────────────────────────────────────────────

// WatchEvent is emitted whenever the watched key's value changes.
type WatchEvent struct {
	Key      string
	OldValue string // empty string when the key is first observed
	NewValue string
	At       time.Time
}

// WatchKey polls key every interval and sends a WatchEvent on ch whenever the
// value changes.  It stops when ctx is cancelled.
func WatchKey(ctx context.Context, client *KVClient, key string,
	interval time.Duration, ch chan<- WatchEvent) {

	var last string
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			val, exists, err := client.Get(ctx, key)
			if err != nil {
				log.Printf("[watcher] error polling %q: %v", key, err)
				continue
			}
			if !exists {
				continue
			}
			if val != last {
				ch <- WatchEvent{
					Key:      key,
					OldValue: last,
					NewValue: val,
					At:       time.Now(),
				}
				last = val
			}
		}
	}
}

// ── writer goroutine ──────────────────────────────────────────────────────────

// WriteLoop periodically updates key with a random value.
func WriteLoop(ctx context.Context, client *KVClient, key string,
	interval time.Duration, wg *sync.WaitGroup) {

	defer wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	adjectives := []string{"fast", "slow", "reliable", "partitioned", "healthy", "degraded"}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			val := adjectives[r.Intn(len(adjectives))]
			if err := client.Put(ctx, key, val); err != nil {
				log.Printf("[writer] PUT error: %v", err)
			} else {
				log.Printf("[writer] set %q = %q", key, val)
			}
		}
	}
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	addr := flag.String("addr", "http://localhost:8080", "cluster endpoint")
	watchKey := flag.String("key", "cluster-state", "key to watch")
	pollInterval := flag.Duration("poll", 500*time.Millisecond, "poll interval for watcher")
	writeInterval := flag.Duration("write", 2*time.Second, "interval for writer goroutine")
	duration := flag.Duration("dur", 30*time.Second, "how long to run")
	flag.Parse()

	client := NewKVClient(*addr)
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	// Seed the key so the watcher has something to observe immediately.
	if err := client.Put(ctx, *watchKey, "initial"); err != nil {
		log.Fatalf("failed to seed key %q: %v", *watchKey, err)
	}

	// Start watcher.
	events := make(chan WatchEvent, 32)
	go WatchKey(ctx, client, *watchKey, *pollInterval, events)

	// Start writer.
	var wg sync.WaitGroup
	wg.Add(1)
	go WriteLoop(ctx, client, *watchKey, *writeInterval, &wg)

	// Print events until the context expires.
	fmt.Printf("Watching key %q on %s (poll=%v, write=%v, duration=%v)\n\n",
		*watchKey, *addr, *pollInterval, *writeInterval, *duration)

	eventCount := 0
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case ev := <-events:
			eventCount++
			fmt.Printf("[%s] key=%q  %q → %q\n",
				ev.At.Format("15:04:05.000"), ev.Key, ev.OldValue, ev.NewValue)
		}
	}

	wg.Wait()
	fmt.Printf("\nDone. Observed %d change event(s) in %v.\n", eventCount, *duration)
}
