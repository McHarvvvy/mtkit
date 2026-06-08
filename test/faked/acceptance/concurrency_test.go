package acceptance

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/McHarvvvy/mtkit/test/faked/internal/testendpoint"
	"github.com/McHarvvvy/mtkit/test/faked/internal/testpb"
)

// TestConcurrency_SequenceHits 是 TC-09：单条 sequence rule 被 M 个 goroutine 并发命中 M 次。
// 期望：成功响应集合 == 注册的 N 份（去重后），失败数 == M - N（耗尽返回 FailedPrecondition）。
// 不断言顺序——并发本来不保证。
func TestConcurrency_SequenceHits(t *testing.T) {
	t.Parallel()
	h := sharedHarness(t)
	key := caseKey(t)

	const (
		seqLen     = 10
		goroutines = 50
	)
	resps := make([]*testpb.PingRes, seqLen)
	for i := 0; i < seqLen; i++ {
		// 用 Code 字段做唯一性标记——并发完成后用集合验证"全部不同响应都至少出现 1 次"。
		resps[i] = &testpb.PingRes{Code: int32(100 + i), Msg: "seq"}
	}

	seq := h.client.GRPC().
		On(testendpoint.EndpointEcho, testpb.Echo_Ping_FullMethodName).
		RouteKey(key).
		Sequence()
	for _, r := range resps {
		seq.Add(r)
	}
	seq.ErrorOnExhausted(codes.FailedPrecondition, "sequence exhausted").Apply(t)

	echo := h.dialEcho(t)

	type result struct {
		code int32
		err  error
	}
	results := make(chan result, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := echo.Ping(ctx, &testpb.PingReq{Key: key})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{code: got.GetCode()}
		}()
	}
	wg.Wait()
	close(results)

	successCodes := make(map[int32]int)
	failCount := 0
	for r := range results {
		if r.err != nil {
			if c := status.Code(r.err); c != codes.FailedPrecondition {
				t.Errorf("unexpected error code: want FailedPrecondition, got %s (err=%v)", c, r.err)
			}
			failCount++
			continue
		}
		successCodes[r.code]++
	}

	// 成功数 == seqLen；失败数 == goroutines - seqLen。
	if total := len(successCodes); total != seqLen {
		t.Errorf("distinct success codes: want %d, got %d (codes=%v)", seqLen, total, successCodes)
	}
	successCount := 0
	for _, v := range successCodes {
		successCount += v
	}
	if successCount != seqLen {
		t.Errorf("success count: want %d, got %d", seqLen, successCount)
	}
	if failCount != goroutines-seqLen {
		t.Errorf("fail count: want %d, got %d", goroutines-seqLen, failCount)
	}
	// 验证每个 code 都恰好出现 1 次（不丢不重）。
	for i := 0; i < seqLen; i++ {
		code := int32(100 + i)
		if successCodes[code] != 1 {
			t.Errorf("code %d: want exactly 1 hit, got %d", code, successCodes[code])
		}
	}
}
