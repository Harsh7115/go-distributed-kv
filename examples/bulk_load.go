// examples/bulk_load.go
//
// Bulk-load stress test for go-distributed-kv.
//
// Spawns N worker goroutines that each write M key-value pairs to the cluster,
// then reads them back and verifies correctness. Prints a latency histogram and
// throughput summary at the end.
//
// Usage:
//   go run ./examples/bulk_load.go \
//     --addr http://localhost:8080 \
//     --workers 8 \
//     --keys-per-worker 500 \
//     --value-size 128
//
// Start a local 3-node cluster first (see README).

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ─── CLI flags ────────────────────────────────────────────────────────────────

var (
	addr          = flag.String("addr", "http://localhost:8080", "Leader HTTP address")
	workers       = flag.Int("workers", 8, "Number of concurrent writer goroutines")
	keysPerWorker = flag.Int("keys-per-worker", 500, "Keys each worker writes")
	valueSize     = flag.Int("value-size", 128, "Bytes per value")
	readBack      = flag.Bool("read-back", true, "Verify every key after writing")
	timeout       = flag.Duration("timeout", 5*time.Second, "Per-request HTTP timeout")
)

// ─── HTTP client helpers ──────────────────────────────────────────────────────

type kvClient struct {
	base   string
	client *http.Client
}

func newKVClient(addr string, t time.Duration) *kvClient {
	return &kvClient{
		base:   addr,
		client: &http.Client{Timeout: t},
	}
}

type putRequest struct {
	Value string `json:"value"`
}

type getResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (c *kvClient) put(key, value string) (time.Duration, error) {
	body, _ := json.Marshal(putRequest{Value: value})
	start := time.Now()
	resp, err := c.client.Do(mustReq("PUT", c.base+"/kv/"+key, bytes.NewReader(body)))
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, fmt.Errorf("PUT %s: %w", key, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return elapsed, fmt.Errorf("PUT %s: unexpected status %d", key, resp.StatusCode)
	}
	return elapsed, nil
}

func (c *kvClient) get(key string) (string, time.Duration, error) {
	start := time.Now()
	resp, err := c.client.Get(c.base + "/kv/" + key)
	elapsed := time.Since(start)
	if err != nil {
		return "", elapsed, fmt.Errorf("GET %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", elapsed, fmt.Errorf("GET %s: not found", key)
	}
	if resp.StatusCode != http.StatusOK {
		return "", elapsed, fmt.Errorf("GET %s: status %d", key, resp.StatusCode)
	}
	var gr getResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return "", elapsed, fmt.Errorf("GET %s: decode: %w", key, err)
	}
	return gr.Value, elapsed, nil
}

func mustReq(method, url string, body io.Reader) *http.Request {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ─── Worker ───────────────────────────────────────────────────────────────────

type result struct {
	latency time.Duration
	isRead  bool
	err     error
}

func worker(id int, client *kvClient, keys int, valSize int, doRead bool, out chan<- result, wg *sync.WaitGroup) {
	defer wg.Done()
	value := string(bytes.Repeat([]byte("x"), valSize))

	for i := 0; i < keys; i++ {
		key := fmt.Sprintf("worker%d-key%06d", id, i)

		// Write
		lat, err := client.put(key, value)
		out <- result{latency: lat, isRead: false, err: err}
		if err != nil {
			continue
		}

		// Optionally read back and verify
		if doRead {
			got, lat2, err2 := client.get(key)
			out <- result{latency: lat2, isRead: true, err: err2}
			if err2 == nil && got != value {
				out <- result{err: fmt.Errorf("key %s: value mismatch (got %d bytes, want %d)", key, len(got), len(value))}
			}
		}
	}
}

// ─── Stats ────────────────────────────────────────────────────────────────────

type stats struct {
	latencies []time.Duration
	errors    int64
	count     int64
}

func (s *stats) add(r result) {
	atomic.AddInt64(&s.count, 1)
	if r.err != nil {
		atomic.AddInt64(&s.errors, 1)
		return
	}
	s.latencies = append(s.latencies, r.latency)
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printStats(label string, s *stats, elapsed time.Duration) {
	sort.Slice(s.latencies, func(i, j int) bool { return s.latencies[i] < s.latencies[j] })
	n := len(s.latencies)
	if n == 0 {
		fmt.Printf("\n%s: no successful operations\n", label)
		return
	}

	var total time.Duration
	for _, l := range s.latencies {
		total += l
	}
	mean := total / time.Duration(n)
	ops := float64(n) / elapsed.Seconds()

	fmt.Printf("\n%s (%d ops, %d errors)\n", label, n, s.errors)
	fmt.Printf("  Throughput : %.0f ops/sec\n", ops)
	fmt.Printf("  Mean       : %v\n", mean.Round(time.Microsecond))
	fmt.Printf("  p50        : %v\n", percentile(s.latencies, 50).Round(time.Microsecond))
	fmt.Printf("  p95        : %v\n", percentile(s.latencies, 95).Round(time.Microsecond))
	fmt.Printf("  p99        : %v\n", percentile(s.latencies, 99).Round(time.Microsecond))
	fmt.Printf("  Max        : %v\n", s.latencies[n-1].Round(time.Microsecond))
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	totalKeys := *workers * *keysPerWorker
	opLabel := "write-only"
	if *readBack {
		opLabel = "write+read-verify"
	}
	fmt.Printf("go-distributed-kv bulk loader\n")
	fmt.Printf("  Leader   : %s\n", *addr)
	fmt.Printf("  Workers  : %d\n", *workers)
	fmt.Printf("  Keys     : %d total (%d/worker)\n", totalKeys, *keysPerWorker)
	fmt.Printf("  Value    : %d bytes\n", *valueSize)
	fmt.Printf("  Mode     : %s\n", opLabel)
	fmt.Println()

	client := newKVClient(*addr, *timeout)

	results := make(chan result, *workers**keysPerWorker*2)
	var wg sync.WaitGroup

	start := time.Now()
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(i, client, *keysPerWorker, *valueSize, *readBack, results, &wg)
	}

	// Close channel when all workers finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	writeStats := &stats{}
	readStats := &stats{}
	var errMsgs []string

	for r := range results {
		if r.err != nil && !r.isRead {
			if len(errMsgs) < 10 {
				errMsgs = append(errMsgs, r.err.Error())
			}
		}
		if r.isRead {
			readStats.add(r)
		} else {
			writeStats.add(r)
		}
	}

	elapsed := time.Since(start)

	fmt.Printf("Total wall time: %v\n", elapsed.Round(time.Millisecond))
	printStats("WRITES", writeStats, elapsed)
	if *readBack {
		printStats("READS (read-your-writes)", readStats, elapsed)
	}

	if len(errMsgs) > 0 {
		log.Printf("\nFirst %d errors:", len(errMsgs))
		for _, e := range errMsgs {
			log.Printf("  %s", e)
		}
	}

	totalOps := writeStats.count
	if *readBack {
		totalOps += readStats.count
	}
	fmt.Printf("\nOverall: %.0f ops/sec (%d ops in %v)\n",
		float64(totalOps)/elapsed.Seconds(), totalOps, elapsed.Round(time.Millisecond))
}
