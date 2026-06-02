// Package client 是 orchestrator 在 client 模式（re-exec）下与 daemon 通信的内部实现。
//
// 注意：它与顶层 `mtkit/client`（面向测试作者的消费端 API）性质完全不同——
// 本包是 re-exec 进程管理的一部分（发现 daemon、持租约、心跳、自启），只供 orchestrator 分流使用。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	mtconstant "github.com/McHarvvvy/mtkit/constant"
	"github.com/McHarvvvy/mtkit/orchestrator/protocol"
	mtruntime "github.com/McHarvvvy/mtkit/runtime"
)

// Config 是客户端配置。
type Config struct {
	// Project 指定项目隔离标识，用于定位 runtime 文件并区分多项目并行测试。
	Project string
	// RuntimePath 指定 daemon 发现文件路径，空值时按 Project 推导默认路径。
	RuntimePath string
	// HTTPTimeout 指定客户端请求 daemon 的超时时间。
	HTTPTimeout time.Duration
}

// Client 用于与 mtkit daemon 通信。
type Client struct {
	cfg        Config
	baseURL    string
	token      string
	httpClient *http.Client
}

// LeaseInfo 是 Acquire 的返回值，仅包含租约信息。
type LeaseInfo struct {
	LeaseID    string
	AcquiredAt time.Time
}

// New 创建客户端并连接 daemon。
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Project == "" {
		cfg.Project = "default"
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 5 * time.Second
	}
	if cfg.RuntimePath == "" {
		cfg.RuntimePath = mtruntime.DefaultRuntimePath(cfg.Project)
	}

	c := &Client{cfg: cfg, httpClient: &http.Client{Timeout: cfg.HTTPTimeout}}
	if err := c.connectOrStart(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Acquire 向 daemon 申请租约。
func (c *Client) Acquire(ctx context.Context) (LeaseInfo, error) {
	req := protocol.AcquireReq{
		Project: c.cfg.Project,
		PID:     os.Getpid(),
	}
	var resp protocol.AcquireResp
	if err := c.post(ctx, protocol.PathAcquire, req, &resp); err != nil {
		return LeaseInfo{}, err
	}
	return LeaseInfo{LeaseID: resp.LeaseID, AcquiredAt: resp.AcquiredAt}, nil
}

// Heartbeat 发送租约心跳。
func (c *Client) Heartbeat(ctx context.Context, leaseID string) error {
	return c.post(ctx, protocol.PathHeartbeat, protocol.HeartbeatReq{LeaseID: leaseID}, &protocol.OKResp{})
}

// Release 释放租约。
func (c *Client) Release(ctx context.Context, leaseID string, exitCode int) error {
	return c.post(ctx, protocol.PathRelease, protocol.ReleaseReq{LeaseID: leaseID, ExitCode: exitCode}, &protocol.OKResp{})
}

// State 查询 daemon 状态。
func (c *Client) State(ctx context.Context) (protocol.StateResp, error) {
	var resp protocol.StateResp
	err := c.get(ctx, protocol.PathState, &resp)
	return resp, err
}

func (c *Client) connectOrStart(ctx context.Context) error {
	for attempt := 0; attempt < 2; attempt++ {
		// 关键决策：优先复用已有 daemon，只有 runtime 不可达时才触发自启动。
		if info, err := mtruntime.Read(c.cfg.RuntimePath); err == nil {
			if c.runtimeAlive(ctx, info) {
				c.baseURL = "http://" + info.Addr
				c.token = info.Token
				return nil
			}
			// 关键决策：runtime 文件存在但 daemon 已不可达时，主动删除陈旧 runtime，
			// 避免并发进程错误复用旧地址导致 connect refused。
			_ = os.Remove(c.cfg.RuntimePath)
		}

		err := c.autoStartDaemon(ctx)
		if err != nil {
			// 关键决策：Start 失败不一定代表最终失败。
			// 并发场景下可能是其它进程已经拉起 daemon，因此再等待一次 runtime 作为兜底收敛。
			if _, waitErr := mtruntime.Wait(c.cfg.RuntimePath, 3*time.Second); waitErr != nil {
				if attempt == 1 {
					return err
				}
				continue
			}
		}

		// 关键决策：启动后等待 runtime 就绪，再做一次端口探活，避免半启动状态被误用。
		info, waitErr := mtruntime.Wait(c.cfg.RuntimePath, 12*time.Second)
		if waitErr != nil {
			if attempt == 1 {
				return waitErr
			}
			continue
		}
		if c.runtimeAlive(ctx, info) {
			c.baseURL = "http://" + info.Addr
			c.token = info.Token
			return nil
		}
		_ = os.Remove(c.cfg.RuntimePath)
	}

	return fmt.Errorf("connect daemon failed after runtime refresh")
}

func (c *Client) runtimeAlive(ctx context.Context, info mtruntime.RuntimeInfo) bool {
	_ = ctx
	if strings.TrimSpace(info.Addr) == "" {
		return false
	}
	conn, err := net.DialTimeout(mtconstant.ProtocolTCP, info.Addr, 1200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (c *Client) post(ctx context.Context, path string, reqData interface{}, respData interface{}) error {
	b, err := json.Marshal(reqData)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.HeaderToken, c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e protocol.ErrorResp
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("daemon error: %s %s", e.Code, e.Message)
	}
	return json.NewDecoder(resp.Body).Decode(respData)
}

func (c *Client) get(ctx context.Context, path string, respData interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(protocol.HeaderToken, c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e protocol.ErrorResp
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("daemon error: %s %s", e.Code, e.Message)
	}
	return json.NewDecoder(resp.Body).Decode(respData)
}
