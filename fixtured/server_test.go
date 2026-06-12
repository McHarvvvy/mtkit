package fixtured

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

func TestAllocatorConcurrentBlocksDoNotOverlap(t *testing.T) {
	var alloc Allocator
	const workers = 32
	const size = int64(128)

	type block struct {
		start int64
		end   int64
	}
	blocks := make(chan block, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start, end := alloc.Alloc("token", size)
			blocks <- block{start: start, end: end}
		}()
	}
	wg.Wait()
	close(blocks)

	seen := make(map[int64]bool, workers*int(size))
	for b := range blocks {
		if b.end-b.start != size {
			t.Fatalf("block size mismatch: [%d,%d)", b.start, b.end)
		}
		for v := b.start; v < b.end; v++ {
			if seen[v] {
				t.Fatalf("duplicate token %d", v)
			}
			seen[v] = true
		}
	}
	if got, want := len(seen), workers*int(size); got != want {
		t.Fatalf("token count = %d, want %d", got, want)
	}
}

func TestServerAlloc(t *testing.T) {
	srv := New()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(protocol.HeaderToken) != "secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body, err := json.Marshal(protocol.AllocReq{Kind: "token", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+protocol.PathAlloc, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(protocol.HeaderToken, "secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out protocol.AllocResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Start != 0 || out.End != 3 {
		t.Fatalf("alloc response = [%d,%d), want [0,3)", out.Start, out.End)
	}
}
