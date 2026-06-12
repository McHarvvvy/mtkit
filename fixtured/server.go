package fixtured

import (
	"encoding/json"
	"net/http"

	"github.com/McHarvvvy/mtkit/fixtured/protocol"
)

// maxAllocSize 是单次发号区块的上界。
// 正常客户端固定请求 65536；设上界是为了防止异常/恶意请求一次性把全局计数器推进巨量、吃掉 token 值空间。
// 取值远大于正常块（16×）以容纳未来更大的块，同时挡住明显异常的请求。
const maxAllocSize = int64(1 << 20)

// Server 把 fixture 协调端点挂在 mtkit daemon 主端口上（与 lease/faked 共用端口与鉴权）。
type Server struct {
	alloc *Allocator
}

// New 创建 fixture 协调服务。由 daemon 启动时无条件创建，无 enable flag。
func New() *Server {
	return &Server{alloc: &Allocator{}}
}

// RegisterRoutes 把 fixture 控制路由挂到 daemon mux。
// wrap 为 daemon 的 withAuth；测试可传 nil 跳过鉴权。
func (s *Server) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	handler := s.handleAlloc
	if wrap != nil {
		handler = wrap(handler)
	}
	mux.HandleFunc(protocol.PathAlloc, handler)
}

func (s *Server) handleAlloc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	var req protocol.AllocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	// v1 只有 token 一个 kind；非 token 一律拒绝，避免静默吞掉拼写错误的 kind。
	if req.Kind != protocol.KindToken {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeInvalidKind, "kind must be token")
		return
	}
	if req.Size <= 0 {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeInvalidSize, "size must be positive")
		return
	}
	if req.Size > maxAllocSize {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeInvalidSize, "size exceeds limit")
		return
	}
	start, end := s.alloc.Alloc(req.Kind, req.Size)
	writeJSON(w, http.StatusOK, protocol.AllocResp{Start: start, End: end})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.ErrorResp{Code: code, Message: message})
}
