package fixture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"testing"

	"github.com/McHarvvvy/mtkit/fixtured"
	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

type seqSource struct {
	mu sync.Mutex
	n  int64
}

func (s *seqSource) Next() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.n
	s.n++
	return v, nil
}

func TestBlockSourceFetchesOnlyWhenExhausted(t *testing.T) {
	blocks := [][2]int64{{0, 2}, {10, 12}}
	var calls int
	src := &blockSource{
		size: 2,
		kind: protocol.KindToken,
		fetch: func(kind string, size int64) (int64, int64, error) {
			if kind != protocol.KindToken || size != 2 {
				t.Fatalf("fetch args = %q/%d", kind, size)
			}
			b := blocks[calls]
			calls++
			return b[0], b[1], nil
		},
	}
	for i, want := range []int64{0, 1, 10, 11} {
		got, err := src.Next()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("Next[%d] = %d, want %d", i, got, want)
		}
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls)
	}
}

func TestHTTPClientFetchesFromFixtured(t *testing.T) {
	srv := fixtured.New()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux, nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(u.Host, "")
	scope := client.NewScope(t)

	first := scope.UniqueHex(16)
	second := scope.UniqueHex(16)
	if first == second {
		t.Fatalf("expected distinct values, got %q", first)
	}
}

func TestUniqueStringIsBijectionWithinSmallCapacity(t *testing.T) {
	src := &seqSource{}
	scope := newScopeWith(t, src)
	seen := make(map[string]bool, 256)
	for i := 0; i < 256; i++ {
		v := scope.UniqueString(2, Hex)
		if seen[v] {
			t.Fatalf("duplicate value %q at token %d", v, i)
		}
		seen[v] = true
	}
	if len(seen) != 256 {
		t.Fatalf("distinct count = %d, want 256", len(seen))
	}
}

func TestCaseScopeFormats(t *testing.T) {
	scope := newScopeWith(t, &seqSource{})

	uuid := scope.UUID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-8[0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(uuid) {
		t.Fatalf("invalid uuid: %s", uuid)
	}

	noDash := scope.UUIDNoDash()
	if !regexp.MustCompile(`^[0-9a-f]{12}4[0-9a-f]{3}8[0-9a-f]{15}$`).MatchString(noDash) {
		t.Fatalf("invalid uuid no dash: %s", noDash)
	}

	email := scope.EmailFrom("user@example.com")
	if !regexp.MustCompile(`^user\+[0-9A-Za-z]{16}@example\.com$`).MatchString(email) {
		t.Fatalf("invalid email: %s", email)
	}

	phone := scope.Phone("US")
	if !regexp.MustCompile(`^\+1[0-9]{10}$`).MatchString(phone) {
		t.Fatalf("invalid phone: %s", phone)
	}
}

func TestServerRejectsInvalidAllocSize(t *testing.T) {
	srv := fixtured.New()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux, nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body, err := json.Marshal(protocol.AllocReq{Kind: "token", Size: 0})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+protocol.PathAlloc, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var out protocol.ErrorResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code != protocol.ErrCodeInvalidSize {
		t.Fatalf("error code = %q, want %q", out.Code, protocol.ErrCodeInvalidSize)
	}
}
