package modelcat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/core"
	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

const bundledAgentsTimeout = 15 * time.Second

// BundledAgents 解包钉住 Core（omp）自带的内置子代理定义（`omp agents unpack --dir <dir> --json`），
// 并按 Core 钉与二进制元信息在内存缓存结果。未配置 OmpPath 且未注入 AgentsRun 时返回 nil, nil。
func (c *Catalog) BundledAgents(ctx context.Context) ([]agentcfg.AgentSpec, error) {
	if c.cfg.AgentsRun == nil && c.cfg.OmpPath == "" {
		return nil, nil
	}
	key := c.bundledAgentsKey()
	c.agentsMu.Lock()
	if c.agentsKey == key && c.agents != nil {
		out := cloneAgentSpecs(c.agents)
		c.agentsMu.Unlock()
		return out, nil
	}
	c.agentsMu.Unlock()

	dir, err := os.MkdirTemp("", "oryn-bundled-agents-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	rctx, cancel := context.WithTimeout(ctx, bundledAgentsTimeout)
	defer cancel()
	run := c.cfg.AgentsRun
	if run == nil {
		run = c.unpackBundledAgents
	}
	if err := run(rctx, dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取解包目录: %w", err)
	}
	specs := make([]agentcfg.AgentSpec, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		spec, err := agentcfg.ParseAgentSpec(b)
		if err != nil || spec.Name == "" {
			continue
		}
		spec.Source = "bundled"
		specs = append(specs, spec)
	}
	slices.SortFunc(specs, func(a, b agentcfg.AgentSpec) int { return strings.Compare(a.Name, b.Name) })

	c.agentsMu.Lock()
	c.agentsKey = key
	c.agents = cloneAgentSpecs(specs)
	c.agentsMu.Unlock()
	return specs, nil
}

func (c *Catalog) bundledAgentsKey() string {
	if c.cfg.AgentsRun != nil {
		return "custom"
	}
	pin := core.Pin()[pinKey()]
	fi, err := os.Stat(c.cfg.OmpPath)
	if err != nil {
		return pin + "|" + c.cfg.OmpPath
	}
	return fmt.Sprintf("%s|%s|%d|%d", pin, c.cfg.OmpPath, fi.Size(), fi.ModTime().UnixNano())
}

func (c *Catalog) unpackBundledAgents(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, c.cfg.OmpPath, "agents", "unpack", "--dir", dir, "--json")
	cmd.Dir = dir
	cmd.Env = []string{
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"OTEL_SDK_DISABLED=true",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > maxErrDetail {
			detail = detail[len(detail)-maxErrDetail:]
		}
		if detail != "" {
			return fmt.Errorf("omp agents unpack: %w: %s", err, detail)
		}
		return fmt.Errorf("omp agents unpack: %w", err)
	}
	return nil
}

func cloneAgentSpecs(in []agentcfg.AgentSpec) []agentcfg.AgentSpec {
	out := make([]agentcfg.AgentSpec, len(in))
	for i, s := range in {
		s.Model = slices.Clone(s.Model)
		s.Tools = slices.Clone(s.Tools)
		out[i] = s
	}
	return out
}
