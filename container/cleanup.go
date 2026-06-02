package container

import (
	"context"
	"fmt"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// CleanupOrphans 在守护进程启动前清理同名遗留容器。
func CleanupOrphans(ctx context.Context, regs []ContainerRegistration) error {
	// 关键决策：容器名称已与配置名绑定，daemon 冷启动前必须清理同名残留容器，
	// 否则 Docker 会因为 name conflict 拒绝创建新容器，影响整套测试拉起。
	if len(regs) == 0 {
		return nil
	}
	targets := make(map[string]struct{}, len(regs))
	for _, reg := range regs {
		if reg.Name == "" {
			continue
		}
		targets[reg.Name] = struct{}{}
	}
	if len(targets) == 0 {
		return nil
	}

	dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer dockerCli.Close()

	containers, err := dockerCli.ContainerList(ctx, dockercontainer.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, ctr := range containers {
		nameMatched := false
		for _, rawName := range ctr.Names {
			name := strings.TrimPrefix(rawName, "/")
			if _, ok := targets[name]; ok {
				nameMatched = true
				break
			}
		}
		if !nameMatched {
			continue
		}
		if err = dockerCli.ContainerRemove(ctx, ctr.ID, dockercontainer.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			return err
		}
		if err = waitContainerRemoved(ctx, dockerCli, ctr.ID, 30*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func waitContainerRemoved(ctx context.Context, dockerCli *client.Client, containerID string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		// 关键决策：ContainerRemove 返回成功不代表底层立即完成删除，
		// 这里轮询 Inspect 直到 NotFound，消除 create 与 remove 交错的竞态窗口。
		_, err := dockerCli.ContainerInspect(waitCtx, containerID)
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				return nil
			}
			if waitCtx.Err() != nil {
				return fmt.Errorf("wait remove container %s timeout", containerID)
			}
			return err
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait remove container %s timeout", containerID)
		case <-ticker.C:
		}
	}
}
