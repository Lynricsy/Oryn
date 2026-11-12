package providerrt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/jobs"
)

// ErrUnauthorized 表示控制面拒绝了 provider 令牌（不可重试）。
var ErrUnauthorized = errors.New("providerrt: provider 令牌无效")

// errLeaseConflict 表示另一个实例持有租约。
var errLeaseConflict = errors.New("providerrt: 另一个 provider 实例持有租约")

// Runner 是 provider 运行时主循环。
type Runner struct {
	// URL 是控制面地址（provider 配置 url）。
	URL string
	// Token 是 provider 令牌 opv_…。
	Token string
	// OrbURL 覆盖 spawn 任务注入 orb 的 ORYN_URL（provider 配置 orb_url；空 = 不覆盖）。
	OrbURL string
	Driver Driver
	// Version 在 hello 中上报。
	Version string
	// AllowMultiple 声明允许多实例并存。
	AllowMultiple bool

	// Concurrency 是并发执行的任务数（默认 4）。
	Concurrency int
	// ClaimWait 是长轮询等待时长（默认 30s）。
	ClaimWait time.Duration
	// JobTimeout 是单个任务的超时（默认 5 分钟）。
	JobTimeout time.Duration
	// ShutdownGrace 是收到退出信号后给在途任务的宽限（默认 45s）。
	ShutdownGrace time.Duration
	// HeartbeatInterval 是重发 hello 续租的间隔（默认 15s）。
	HeartbeatInterval time.Duration
	HTTP              *http.Client

	instanceID string
	// helloMu 串行化 hello（租约冲突时多个 worker 同时重试）。
	helloMu sync.Mutex
}

func (r *Runner) defaults() {
	if r.Concurrency <= 0 {
		r.Concurrency = 4
	}
	if r.ClaimWait <= 0 {
		r.ClaimWait = 30 * time.Second
	}
	if r.JobTimeout <= 0 {
		r.JobTimeout = 5 * time.Minute
	}
	if r.ShutdownGrace <= 0 {
		r.ShutdownGrace = 45 * time.Second
	}
	if r.HeartbeatInterval <= 0 {
		r.HeartbeatInterval = 15 * time.Second
	}
	if r.HTTP == nil {
		r.HTTP = &http.Client{}
	}
	if r.instanceID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		r.instanceID = hex.EncodeToString(b)
	}
}

// InstanceID 返回本实例 id（Run 之后有效）。
func (r *Runner) InstanceID() string { return r.instanceID }

// Run 运行到 ctx 结束（SIGTERM）：停止认领，等待在途任务（至多 ShutdownGrace），然后 bye。
func (r *Runner) Run(ctx context.Context) error {
	r.defaults()
	if err := r.helloUntilOK(ctx); err != nil {
		return err
	}
	// 在途任务使用的上下文：退出信号后再宽限 ShutdownGrace 才取消。
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	stopGrace := context.AfterFunc(ctx, func() {
		t := time.NewTimer(r.ShutdownGrace)
		defer t.Stop()
		select {
		case <-t.C:
			cancelJobs()
		case <-jobCtx.Done():
		}
	})
	defer stopGrace()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.heartbeat(ctx)
	}()
	errCh := make(chan error, r.Concurrency)
	for range r.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.worker(ctx, jobCtx); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	byeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.post(byeCtx, "/provider/v1/bye", map[string]string{"instanceId": r.instanceID}, nil); err != nil {
		slog.Warn("provider bye 失败", "err", err)
	} else {
		slog.Info("provider 已下线", "instance", r.instanceID)
	}
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (r *Runner) hello(ctx context.Context) error {
	r.helloMu.Lock()
	defer r.helloMu.Unlock()
	info := r.Driver.Info()
	req := apigen.ProviderHelloRequest{
		InstanceID: r.instanceID, Driver: info.Driver, Version: r.Version, Capabilities: info.Capabilities,
		Types: info.Types, DefaultType: info.DefaultType, AllowMultiple: r.AllowMultiple,
	}
	var res apigen.ProviderHelloResponse
	return r.post(ctx, "/provider/v1/hello", req, &res)
}

// helloUntilOK 重试 hello 直到成功；令牌无效时立即返回错误。
func (r *Runner) helloUntilOK(ctx context.Context) error {
	backoff := time.Second
	for {
		err := r.hello(ctx)
		if err == nil {
			slog.Info("provider 已连接控制面", "url", r.URL, "instance", r.instanceID, "driver", r.Driver.Info().Driver)
			return nil
		}
		if errors.Is(err, ErrUnauthorized) {
			return err
		}
		slog.Warn("provider hello 失败，稍后重试", "err", err, "retryIn", backoff)
		if !sleepCtx(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func (r *Runner) heartbeat(ctx context.Context) {
	t := time.NewTicker(r.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.hello(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("provider 续租失败", "err", err)
			}
		}
	}
}

func (r *Runner) worker(ctx, jobCtx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		job, ok, err := r.claim(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrUnauthorized):
			return err
		case errors.Is(err, errLeaseConflict):
			slog.Warn("租约被其他实例持有，重新 hello")
			if !sleepCtx(ctx, 5*time.Second) {
				return nil
			}
			_ = r.hello(ctx)
			continue
		case err != nil:
			slog.Warn("认领任务失败", "err", err, "retryIn", backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, 15*time.Second)
			continue
		}
		backoff = time.Second
		if !ok {
			continue
		}
		r.execute(jobCtx, job)
	}
	return nil
}

func (r *Runner) claim(ctx context.Context) (apigen.ProviderJob, bool, error) {
	var res apigen.ProviderClaimResponse
	req := apigen.ProviderClaimRequest{InstanceID: r.instanceID, WaitSeconds: int(r.ClaimWait / time.Second)}
	c, cancel := context.WithTimeout(ctx, r.ClaimWait+15*time.Second)
	defer cancel()
	status, err := r.do(c, "/provider/v1/jobs/claim", req, &res)
	if err != nil {
		return res.Job, false, err
	}
	return res.Job, status == http.StatusOK, nil
}

// execute 执行一个任务并上报结果（上报失败时重试数次；最终失败由控制面的认领租约兜底重排）。
func (r *Runner) execute(ctx context.Context, pj apigen.ProviderJob) {
	start := time.Now()
	out := newTailBuffer(jobs.MaxOutputBytes)
	log := slog.With("job", pj.ID, "op", pj.Op, "orb", pj.OrbID, "attempt", pj.Attempt)
	log.Info("开始执行任务")
	j, err := ParseJob(pj)
	if err == nil {
		if j.Spawn != nil && r.OrbURL != "" {
			j.Spawn.Env.OrynURL = r.OrbURL
		}
		jctx, cancel := context.WithTimeout(ctx, r.JobTimeout)
		err = r.Driver.Exec(jctx, j, out)
		cancel()
	}
	req := apigen.ProviderCompleteRequest{Ok: err == nil, Output: out.String(), DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		msg := err.Error()
		req.Error = &msg
		log.Warn("任务失败", "err", err, "durationMs", req.DurationMs)
	} else {
		log.Info("任务完成", "durationMs", req.DurationMs)
	}
	for i := range 5 {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_, cerr := r.do(cctx, "/provider/v1/jobs/"+pj.ID+"/complete", req, nil)
		cancel()
		if cerr == nil {
			return
		}
		var he *httpError
		if errors.As(cerr, &he) && he.status < 500 {
			log.Warn("控制面拒绝任务结果", "err", cerr)
			return
		}
		log.Warn("上报任务结果失败", "err", cerr, "try", i+1)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
}

// httpError 是控制面返回的非 2xx 响应。
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, strings.TrimSpace(e.body))
}

func (r *Runner) post(ctx context.Context, path string, body, out any) error {
	_, err := r.do(ctx, path, body, out)
	return err
}

// do 发送 POST；返回 2xx 状态码。401 → ErrUnauthorized；409 provider_already_connected → errLeaseConflict。
func (r *Runner) do(ctx context.Context, path string, body, out any) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.URL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return resp.StatusCode, fmt.Errorf("解析 %s 应答: %w", path, err)
			}
		}
		return resp.StatusCode, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, ErrUnauthorized
	}
	var e apigen.Error
	if resp.StatusCode == http.StatusConflict && json.Unmarshal(raw, &e) == nil && e.Code == "provider_already_connected" {
		return resp.StatusCode, errLeaseConflict
	}
	return resp.StatusCode, &httpError{status: resp.StatusCode, body: string(raw)}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
