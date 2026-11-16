package render

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"log/slog"
	"strings"

	"github.com/Lynricsy/Oryn/internal/sandbox"
)

// preflightPage 是自检作业的固定页面：整页纯绿，截图后核对中心像素。
var preflightPage = []byte(`<!doctype html><html><head><meta charset="utf-8"></head>` +
	`<body style="margin:0"><div style="width:100vw;height:100vh;background:#00a000"></div></body></html>`)

const preflightWidth, preflightHeight = 320, 200

// Preflight 跑一次内置的最小截图作业（代理直接回一段固定的 HTML，不涉及任何 artifact），据此设置 Status，
// 并记一行日志。orynd 启动时调用一次。
func (r *Renderer) Preflight(ctx context.Context) {
	if r.setup != nil {
		// 配置本身就不能渲染：原因在 New 里已经写进 Status
		slog.Warn("artifacts: 渲染已停用，缩略图、PNG 与 PDF 不可用", "reason", r.setup.Error())
		return
	}
	err := r.preflight(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		reason := strings.TrimPrefix(err.Error(), "render: ")
		r.setStatus(false, reason)
		slog.Warn("artifacts: 渲染自检未通过，缩略图、PNG 与 PDF 不可用", "reason", reason)
		return
	}
	r.setStatus(true, "")
	slog.Info("artifacts: 渲染自检通过", "chromium", r.chromium, "uid", r.uid,
		"chromium_sandbox", !r.cfg.NoSandbox, "landlock_net", landlockNote(), "proxy_port", r.proxy.port())
}

func (r *Renderer) preflight(ctx context.Context) error {
	out, err := r.run(ctx, Job{Width: preflightWidth, Height: preflightHeight}, preflightPage)
	if err != nil {
		return err
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		return fmt.Errorf("自检截图不是 PNG：%w", err)
	}
	if b := img.Bounds(); b.Dx() != preflightWidth || b.Dy() != preflightHeight {
		return fmt.Errorf("自检截图尺寸是 %d×%d，应为 %d×%d", b.Dx(), b.Dy(), preflightWidth, preflightHeight)
	}
	cr, cg, cb, _ := img.At(preflightWidth/2, preflightHeight/2).RGBA()
	if cr>>8 > 0x20 || cg>>8 < 0x80 || cb>>8 > 0x20 {
		return fmt.Errorf("自检截图的内容不对（中心像素 #%02x%02x%02x），Chromium 没有加载到自检页面", cr>>8, cg>>8, cb>>8)
	}
	return nil
}

// landlockNote 说明 Landlock 网络规则（第 3 层）在本机是否生效。
func landlockNote() string {
	abi, err := sandbox.Probe()
	switch {
	case err != nil:
		return "跳过：" + err.Error()
	case abi < 4:
		return fmt.Sprintf("跳过：内核 Landlock ABI %d < 4", abi)
	}
	return fmt.Sprintf("生效（ABI %d）", abi)
}
