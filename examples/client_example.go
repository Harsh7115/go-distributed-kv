// Package main demonstrates usage of the go-distributed-kv HTTP client API.
//
// Run a 3-node cluster first (see README), then execute:
//
//	go run ./examples/client_example.go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"time"
)

// kvClient is a simple HTTP client for the distributed KV store.
// It retries failed requests against alternate nodes for basic fault tolerance.
type kvClient struct {
	nodes      []string
	httpClient *http.Client
}

// newKVClient creates a client that load-balances across the given node addresses.
func newKVClient(nodes []string) *kvClient {
	return &kvClient{
		nodes: nodes,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Put writes key=value to the cluster. Retries against other nodes on failure.
func (c *kvClient) Put(key, value string) error {
	body, _ := json.Marshal(map[string]string{"value": value})
	return c.doWithRetry("PUT", "/kv/"+key, bytes.NewReader(body))
}

// Get retrieves the value associated with key. Returns ("", false) if missing.
func (c *kvClient) Get(key string) (string, bool, error) {
	var result map[string]string
	err := c.doWithRetryAndDecode("GET", "/kv/"+key, nil, &result)
	if err != nil {
		return "", false, err
	}
	v, ok := result["value"]
	return v, ok, nil
}

// Delete removes key from the cluster. Returns true if the key existed.
func (c *kvClient) Delete(key string) error {
	return c.doWithRetry("DELETE", "/kv/"+key, nil)
}

func (c *kvClient) doWithRetry(method, path string, body io.Reader) error {
	return c.doWithRetryAndDecode(method, path, body, nil)
}

func (c *kvClient) doWithRetryAndDecode(method, path string, body io.Reader, out interface{}) error {
	// Shuffle nodes so load is spread across replicas.
	order := rand.Perm(len(c.nodes))
	var lastErr error
	for _, i := range order {
		url := "http://" + c.nodes[i] + path
		var bodyReader io.Reader
		if body != nil {
			// Body may only be read once; for retries the caller must pass a fresh reader.
			bodyReader = body
		}
		req, err := http.NewRequest(method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil // caller checks ok flag
		}
		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("node %s: HTTP %d", c.nodes[i], resp.StatusCode)
			continue
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("all nodes failed: %w", lastErr)
	}
	return errors.New("no nodes available")
}

// -----------------------------------------------------------------------
// Demo
// -----------------------------------------------------------------------

func main() {
	// Point at a 3-node cluster started locally (see README).
	client := newKVClient([]string{
		"localhost:8080",
		"localhost:8081",
		"localhost:8082",
	})

	log.Println("=== go-distributed-kv client example ===")

	// --- Basic put / get / delete ---
	log.Println("Writing keys...")
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key-%d", i)
		val := fmt.Sprintf("value-%d-%d", i, time.Now().UnixNano())
		if err := client.Put(key, val); err != nil {
			log.Fatalf("PUT %s: %v", key, err)
		}
		log.Printf("  PUT %s = %s", key, val)
	}

	log.Println("Reading keys back...")
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key-%d", i)
		val, ok, err := client.Get(key)
		if err != nil {
			log.Fatalf("GET %s: %v", key, err)
		}
		if !ok {
			log.Fatalf("GET %s: not found (unexpected)", key)
		}
		log.Printf("  GET %s = %s", key, val)
	}

	// --- Overwrite ---
	log.Println("Overwriting key-0...")
	if err := client.Put("key-0", "overwritten"); err != nil {
		log.Fatalf("PUT key-0: %v", err)
	}
	val, _, _ := client.Get("key-0")
	log.Printf("  GET key-0 = %s", val)

	// --- Delete ---
	log.Println("Deleting key-2...")
	if err := client.Delete("key-2"); err != nil {
		log.Fatalf("DELETE key-2: %v", err)
	}
	_, ok, _ := client.Get("key-2")
	log.Printf("  key-2 exists after delete: %v", ok)

	// --- Throughput micro-benchmark ---
	log.Println("Throughput benchmark (1000 writes)...")
	start := time.Now()
	const n = 1000
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("bench-%d", i)
		if err := client.Put(key, fmt.Sprintf("%d", i)); err != nil {
			log.Fatalf("bench PUT: %v", err)
		}
	}
	elapsed := time.Since(start)
	log.Printf("  %d writes in %v — %.0f ops/s", n, elapsed, float64(n)/elapsed.Seconds())

	log.Println("Done.")
}
