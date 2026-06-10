// Package fake 是测试侧向 faked control API 提交规则的客户端。
//
// faked 内嵌 daemon 后，control 面挂 daemon 主端口；本包只需 daemon 的 control
// 地址即可注册规则（地址由项目 bootstrap 从 mtkit.runtime.json 读出后传入），
// 不再有独立 faked 发现链 / 环境变量。本文件提供统一 Client 入口与 control API
// transport；grpc.go 与 http.go 在其上叠加协议级链式注册 API。client 只 import
// 两侧 protocol 子包，不拽服务端代码进测试 binary。
package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
)

// apiEnvelope 是 control API 响应包装；与服务端 control handler writeOK/writeError 对齐。
type apiEnvelope struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Client 是测试侧注册 fake rules 的统一 control API client。
//
// 它本质上始终是 HTTP control API 客户端；GRPC() / HTTP() 只是按被注册的
// fake 协议划分的 rule builder 命名空间。
type Client struct {
	ctrl *control

	grpcOnce sync.Once
	grpc     *grpcClient

	httpOnce sync.Once
	http     *httpClient
}

// New 用 daemon 的 control 地址与 token 创建 Client。
//
// controlAddr 即 mtkit.runtime.json 的 control_addr（faked rule 注册与 lease /
// fixtured 共用 daemon 主端口）；token 即 runtime 文件的 token，与 lease 共用同一鉴权。
// 二者均由项目 bootstrap 从 runtime 文件读出后传入，本包不做发现。token 为空时不带鉴权头
// （供进程内、control 未挂鉴权中间件的测试直连使用）。
func New(controlAddr, token string) *Client {
	return &Client{ctrl: newControl(controlAddr, token)}
}

// GRPC 返回 gRPC rule 注册命名空间。
func (c *Client) GRPC() *grpcClient {
	c.grpcOnce.Do(func() {
		c.grpc = newGRPC(c.ctrl)
	})
	return c.grpc
}

// HTTP 返回 HTTP rule 注册命名空间。
func (c *Client) HTTP() *httpClient {
	c.httpOnce.Do(func() {
		c.http = newHTTP(c.ctrl)
	})
	return c.http
}

// Addr 返回 control API 监听地址；测试可能需要直接对其做断言。
func (c *Client) Addr() string { return c.ctrl.addr }

// control 是测试 client 与 control API 之间的最薄一层 transport。
// 只关心：POST JSON → 解码统一错误体。不感知具体协议（gRPC / HTTP）。
type control struct {
	addr   string
	token  string
	client *http.Client
}

func newControl(addr, token string) *control {
	return &control{
		addr:   addr,
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// post 向 control API POST 一个 JSON DTO，解码统一错误体或调用方期望的 success 体。
//
// path 由协议 client 固化（"/v1/grpc/rules" / "/v1/http/rules"）。
// out 为 nil 时只校验响应是 ok=true，不解码 body 中其它字段。
func (c *control) post(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("faked client: marshal request: %w", err)
	}
	url := "http://" + c.addr + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("faked client: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set(mtconstant.HeaderToken, c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("faked client: post %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("faked client: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return decodeError(resp.StatusCode, raw)
	}

	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("faked client: decode envelope: %w", err)
	}
	if !env.OK {
		return &APIError{HTTPStatus: resp.StatusCode, Code: env.Error, Message: env.Message}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("faked client: decode body: %w", err)
		}
	}
	return nil
}

func helperCtx() context.Context { return context.Background() }

// APIError 是 control API 返回 ok=false 时的结构化错误。
// 测试用例可通过 errors.As 取出 code 做断言。
type APIError struct {
	HTTPStatus int
	Code       string
	Message    string
}

// Error 实现 error 接口。
func (e *APIError) Error() string {
	return fmt.Sprintf("faked control api: http=%d code=%s message=%s", e.HTTPStatus, e.Code, e.Message)
}

// decodeError 把 4xx/5xx 响应解码为 *APIError；envelope 解析失败时回退到原始正文。
func decodeError(status int, raw []byte) error {
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Error != "" {
		return &APIError{HTTPStatus: status, Code: env.Error, Message: env.Message}
	}
	return &APIError{HTTPStatus: status, Code: "", Message: string(raw)}
}
