package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func ReadyProcessStable(window time.Duration) ReadyFunc {
	return func(ctx context.Context, waitCh <-chan error, _ time.Duration) error {
		if window <= 0 {
			window = 400 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-waitCh:
			if !ok || err == nil {
				return errors.New("process exited before startup completed")
			}
			return fmt.Errorf("process exited before startup completed: %w", err)
		case <-time.After(window):
			return nil
		}
	}
}

func ReadyTCP(addrs []string) ReadyFunc {
	return func(ctx context.Context, waitCh <-chan error, timeout time.Duration) error {
		return waitTCP(ctx, waitCh, normalizeProbeAddrs(addrs), timeout)
	}
}

func ReadyFileThenTCP(path string, extractAddr func([]byte) (string, error)) ReadyFunc {
	return func(ctx context.Context, waitCh <-chan error, timeout time.Duration) error {
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		deadline := time.Now().Add(timeout)
		var lastErr error
		for {
			raw, err := os.ReadFile(path)
			if err == nil {
				addr, parseErr := extractAddr(raw)
				if parseErr != nil {
					lastErr = parseErr
				} else {
					return waitTCP(ctx, waitCh, normalizeProbeAddrs([]string{addr}), time.Until(deadline))
				}
			} else {
				lastErr = err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case werr, ok := <-waitCh:
				if !ok || werr == nil {
					return errors.New("process exited before ready file")
				}
				return fmt.Errorf("process exited before ready file: %w", werr)
			default:
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("wait ready file timeout: %s: %v", path, lastErr)
			}
			time.Sleep(120 * time.Millisecond)
		}
	}
}

func ExtractJSONAddr(field string) func([]byte) (string, error) {
	return func(raw []byte) (string, error) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", err
		}
		var addr string
		if err := json.Unmarshal(obj[field], &addr); err != nil {
			return "", err
		}
		if strings.TrimSpace(addr) == "" {
			return "", fmt.Errorf("missing %s", field)
		}
		return addr, nil
	}
}

func waitTCP(ctx context.Context, waitCh <-chan error, addrs []string, timeout time.Duration) error {
	if len(addrs) == 0 {
		return ReadyProcessStable(400*time.Millisecond)(ctx, waitCh, timeout)
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		failed := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
			if err != nil {
				failed = append(failed, addr)
				continue
			}
			_ = conn.Close()
		}
		if len(failed) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case werr, ok := <-waitCh:
			if !ok || werr == nil {
				return errors.New("process exited before ready")
			}
			return fmt.Errorf("process exited before ready: %w", werr)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait tcp ready timeout, failed addrs: %s", strings.Join(failed, ","))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func normalizeProbeAddrs(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, raw := range addrs {
		addr := strings.TrimSpace(raw)
		if addr == "" {
			continue
		}
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		if strings.TrimSpace(host) == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			continue
		}
		out = append(out, net.JoinHostPort(host, port))
	}
	return out
}
