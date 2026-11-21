package boxes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 部署的限额与时限（计划「名字与限额」）。
const (
	// MaxReleaseBytes 是 release 压缩包的上限。
	MaxReleaseBytes = 512 << 20
	// keepReleases 是 orbd 保留的 release 数（live + 前 2 个）。
	keepReleases = 3
	// readyTimeoutSec 是激活 / 回滚 / 重启时等端口监听的秒数。
	readyTimeoutSec = 60
	// transferTimeout / activateTimeout 是一次部署传输与激活的上限。
	transferTimeout = 30 * time.Minute
	activateTimeout = 10 * time.Minute
	// prepareWait 是 prepare 等 box 就绪的上限（新建的 box 要等镜像拉起），preparePoll 是复查间隔。
	prepareWait = 10 * time.Minute
	preparePoll = 500 * time.Millisecond
	// deployExpire 是 prepared 的部署等 commit 的上限。
	deployExpire = 60 * time.Minute
	// finishTimeout 是部署收尾事务的上限（收尾往往因为原 ctx 已取消才发生，事务用脱离取消的 ctx）。
	finishTimeout = 15 * time.Second
	// appPortMin / appPortMax 是 app 端口段（与监督器的自动端口 20000–29999 不重叠）。
	appPortMin = 30000
	appPortMax = 30999
)

// 部署行的状态。
const (
	deployPrepared     = "prepared"
	deployTransferring = "transferring"
	deployActivating   = "activating"
	deploySucceeded    = "succeeded"
	deployFailed       = "failed"
	deployCanceled     = "canceled"
)

// 部署结果里 orbd 之外的错误码。
const (
	codeCanceled        = "canceled"
	codeActivateUnknown = "activate_unknown"
	codeTransferFailed  = "transfer_failed"
	codeArchiveMissing  = "archive_missing"
	codeSizeMismatch    = "size_mismatch"
)

// PrepareParams 是 deploy.prepare 的参数（JSON 字段与 CLI 的线路形状一致）。
type PrepareParams struct {
	Box      string `json:"box"`
	App      string `json:"app"`
	Create   bool   `json:"create,omitempty"`
	Provider string `json:"provider,omitempty"`
	Type     string `json:"type,omitempty"`
	Repo     string `json:"repo"`
	Root     string `json:"root"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Source   bool   `json:"source"`
	Move     bool   `json:"move,omitempty"`
	// Public 为 nil 时新 app 取 true、已有 app 不变。
	Public *bool `json:"public,omitempty"`
}

// PrepareResult 是 deploy.prepare 的结果：ArchivePath 是源 orb 里打包的落点，Env 是构建可见的三项 ORYN_DEPLOYMENT_*。
type PrepareResult struct {
	DeployID    string            `json:"deployId"`
	ReleaseID   string            `json:"releaseId"`
	Box         string            `json:"box"`
	App         string            `json:"app"`
	URL         string            `json:"url"`
	ArchivePath string            `json:"archivePath"`
	MaxBytes    int64             `json:"maxBytes"`
	Env         map[string]string `json:"env"`
}

// CommitParams 是 deploy.commit 的参数：Services 的 cwd 已换算成相对 release 根。
type CommitParams struct {
	DeployID          string                 `json:"deployId"`
	SHA256            string                 `json:"sha256"`
	SizeBytes         int64                  `json:"sizeBytes"`
	Services          []apigen.OrbBoxService `json:"services"`
	DiscardBoxChanges bool                   `json:"discardBoxChanges,omitempty"`
}

// CommitResult 是 deploy.commit 的结果；OK 为假时 Code / Message / Log 说明原因。
type CommitResult struct {
	OK        bool   `json:"ok"`
	URL       string `json:"url"`
	ReleaseID string `json:"releaseId"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Log       string `json:"log"`
}

// flight 是一次在途的提交：cancelTransfer 只掐断传输（CancelDeploy），abort 连激活一起中止（删 box、进程退出）。
type flight struct {
	boxID          string
	cancelTransfer context.CancelFunc
	abort          context.CancelFunc
}

// unreachable 标记调用 box orbd 时连接层面的失败：结果未知（请求可能已被执行）。
type unreachable struct{ e *Error }

func (u unreachable) Error() string { return u.e.Message }
func (u unreachable) Unwrap() error { return u.e }

func isUnreachable(err error) bool {
	var u unreachable
	return errors.As(err, &u)
}

// txCtx 是收尾事务用的 ctx：脱离调用方的取消，另加 finishTimeout。
func txCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
}

// appURL 是 app 的地址（不带结尾 `/`；未启用 portal 时为空串）。
func (s *Service) appURL(app string) string {
	return strings.TrimSuffix(s.cfg.Base.AppURL(app), "/")
}

// validRoot 报告 root 是不是仓库里的相对目录（"" = 仓库根）。
func validRoot(root string) bool {
	if root == "" {
		return true
	}
	return !path.IsAbs(root) && path.Clean(root) == root && root != "." && root != ".." && !strings.HasPrefix(root, "../")
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---- 调用 box 的 orbd ----

// usable 是调用 box 的 orbd 之前的统一判定：期望停机 → box_stopped；orb 不是 ready 或没有 link → box_not_ready；
// orbd 不认 box 路由 → box_unsupported。
func (s *Service) usable(ctx context.Context, b db.Box) (db.Orb, error) {
	if b.Desired == desiredStopped {
		return db.Orb{}, errf(CodeBoxStopped, "box %s is stopped; start it first", b.Name)
	}
	o, err := s.orbs.Get(ctx, b.OrbID)
	if errors.Is(err, orbs.ErrNotFound) || (err == nil && gone(o)) {
		return db.Orb{}, errf(CodeBoxNotFound, "box %s not found", b.Name)
	}
	if err != nil {
		return db.Orb{}, err
	}
	if o.State != orbs.StateReady || !s.orbs.Connected(o.ID) {
		return db.Orb{}, errf(CodeBoxNotReady, "box %s is not running yet (%s)", b.Name, o.State)
	}
	if !s.orbs.HasFeature(o.ID, orbs.FeatureBox) {
		return db.Orb{}, errf(CodeBoxUnsupported, "this box runs an orbd without box support")
	}
	return o, nil
}

// orbDo 把请求发给 orb 的 orbd：连接失败包成 unreachable；非 2xx 时带上 orbd 的 {code,message}（缺省 fallback）。
// 2xx 时返回响应（调用方关闭）。
func (s *Service) orbDo(ctx context.Context, orbID string, req *http.Request, fallback string) (*http.Response, error) {
	resp, err := s.orbs.Do(ctx, orbID, req.WithContext(ctx))
	if err != nil {
		return nil, unreachable{errf(CodeBoxNotReady, "box unreachable: %v", err)}
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct{ Code, Message string }
	if json.Unmarshal(b, &e) == nil && e.Code != "" {
		return nil, &Error{Code: e.Code, Message: e.Message}
	}
	return nil, errf(fallback, "orbd returned %d", resp.StatusCode)
}

// orbJSON 发 JSON 请求（body 为 nil 时不带体），把 2xx 的 JSON 应答解到 out（out 为 nil 时丢弃；应答读到 1 MiB）。
func (s *Service) orbJSON(ctx context.Context, orbID, method, target string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.orbDo(ctx, orbID, req, codeTransferFailed)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return unreachable{errf(CodeBoxNotReady, "box unreachable: %v", err)}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return unreachable{errf(CodeBoxNotReady, "bad answer from the box: %v", err)}
	}
	return nil
}

// appsState 读 orbd 的 `GET /box/apps`。
func (s *Service) appsState(ctx context.Context, orbID string) (apigen.OrbBoxAppsState, error) {
	var st apigen.OrbBoxAppsState
	err := s.orbJSON(ctx, orbID, http.MethodGet, "/box/apps", nil, &st)
	return st, err
}

// appEnv 组 app 进程的环境：全部密钥（解密）加四个 ORYN_DEPLOYMENT_*。
func (s *Service) appEnv(ctx context.Context, q *db.Queries, b db.Box, app string) (map[string]string, error) {
	rows, err := q.ListBoxSecrets(ctx, db.ListBoxSecretsParams{BoxID: b.ID, App: app})
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(rows)+4)
	for _, r := range rows {
		v, err := s.openSecret(b.ID, app, r)
		if err != nil {
			return nil, err
		}
		env[r.Name] = v
	}
	env["ORYN_DEPLOYMENT_URL"] = s.appURL(app)
	env["ORYN_DEPLOYMENT_NAME"] = app
	env["ORYN_DEPLOYMENT_BOX"] = b.Name
	env["ORYN_DEPLOYMENT_DATA_DIRECTORY"] = "~/apps/" + app + "/data"
	return env, nil
}

// ---- 在途登记 ----

// track 原子地为部署 id 占一个在途槽：已被占时不覆盖、返回 false；占到时返回只注销这一项的 release。
func (s *Service) track(id string, f *flight) (release func(), ok bool) {
	s.flMu.Lock()
	defer s.flMu.Unlock()
	if _, busy := s.inflight[id]; busy {
		return nil, false
	}
	s.inflight[id] = f
	return func() {
		s.flMu.Lock()
		if s.inflight[id] == f {
			delete(s.inflight, id)
		}
		s.flMu.Unlock()
	}, true
}

func (s *Service) flying(id string) *flight {
	s.flMu.Lock()
	defer s.flMu.Unlock()
	return s.inflight[id]
}

// abortDeploys 中止这台 box 上全部在途的部署（删 box 之前；行随后被级联删掉）。
func (s *Service) abortDeploys(boxID string) {
	s.flMu.Lock()
	defer s.flMu.Unlock()
	for _, f := range s.inflight {
		if f.boxID == boxID {
			f.abort()
		}
	}
}

// ---- 收尾（部署的所有结束路径只经这两处） ----

// finishDeployTx 把部署行收成 failed / canceled：行已被别人收尾时返回 false。首次部署的临时 app 行随之删除（名字与端口
// 让出来）；failed 记活动 deploy_failed。
func (s *Service) finishDeployTx(ctx context.Context, q *db.Queries, row db.BoxDeploy, state, errText string) (bool, error) {
	now := s.now().UnixMilli()
	p := db.FinishBoxDeployParams{State: state, Now: &now, ID: row.ID}
	if errText != "" {
		p.Error = &errText
	}
	n, err := q.FinishBoxDeploy(ctx, p)
	if err != nil || n == 0 {
		return false, err
	}
	if row.First {
		if _, err := q.DeleteProvisionalBoxApp(ctx, db.DeleteProvisionalBoxAppParams{BoxID: row.BoxID, Name: row.App}); err != nil {
			return false, err
		}
	}
	if state == deployFailed {
		if err := s.record(ctx, q, row.BoxID, string(apigen.BoxActivityDeployFailed), deployActor(row), row.App,
			activity{Error: errText, Commit: row.CommitSha}); err != nil {
			return false, err
		}
	}
	return true, nil
}

// completeDeployTx 把部署行收成 succeeded：新 release 成为 live、原 live → kept、pruned 里的 → pruned，app 的 repo / root /
// source 换成这次部署的；set_public 非空时一并生效。行已被别人收尾时 done = false；private 报告这次把 app 改成了私有。
func (s *Service) completeDeployTx(ctx context.Context, q *db.Queries, row db.BoxDeploy, pruned []string) (done, private bool, err error) {
	now := s.now().UnixMilli()
	n, err := q.FinishBoxDeploy(ctx, db.FinishBoxDeployParams{State: deploySucceeded, Now: &now, ID: row.ID})
	if err != nil || n == 0 {
		return false, false, err
	}
	app, err := q.GetBoxApp(ctx, db.GetBoxAppParams{BoxID: row.BoxID, Name: row.App})
	if err != nil {
		return false, false, err
	}
	if app.LiveReleaseID != nil {
		if _, err := q.SetBoxReleaseState(ctx, db.SetBoxReleaseStateParams{State: string(apigen.BoxReleaseKept), ID: *app.LiveReleaseID}); err != nil {
			return false, false, err
		}
	}
	if err := q.CreateBoxRelease(ctx, db.CreateBoxReleaseParams{
		ID: row.ReleaseID, AppID: app.ID, Repo: row.Repo, CommitSha: row.CommitSha, Branch: row.Branch, Root: row.Root,
		Source: row.Source, SizeBytes: row.SizeBytes, Sha256: row.Sha256, Services: row.Services, ThreadID: row.ThreadID,
		State: string(apigen.BoxReleaseLive), CreatedAt: row.CreatedAt, ActivatedAt: now,
	}); err != nil {
		return false, false, err
	}
	rels, err := q.ListBoxReleases(ctx, app.ID)
	if err != nil {
		return false, false, err
	}
	for _, r := range rels {
		if r.ID != row.ReleaseID && r.State != string(apigen.BoxReleasePruned) && slices.Contains(pruned, r.ID) {
			if _, err := q.SetBoxReleaseState(ctx, db.SetBoxReleaseStateParams{State: string(apigen.BoxReleasePruned), ID: r.ID}); err != nil {
				return false, false, err
			}
		}
	}
	if _, err := q.SetBoxAppLive(ctx, db.SetBoxAppLiveParams{
		LiveReleaseID: &row.ReleaseID, Repo: row.Repo, Root: row.Root, Source: row.Source, UpdatedAt: now, ID: app.ID,
	}); err != nil {
		return false, false, err
	}
	by := deployActor(row)
	if row.SetPublic != nil {
		public := *row.SetPublic == 1
		if _, err := q.SetBoxAppPublic(ctx, db.SetBoxAppPublicParams{Public: public, UpdatedAt: now, ID: app.ID}); err != nil {
			return false, false, err
		}
		if err := s.record(ctx, q, row.BoxID, string(apigen.BoxActivityVisibility), by, row.App, activity{Public: &public}); err != nil {
			return false, false, err
		}
		private = !public && app.Public
	}
	if err := s.record(ctx, q, row.BoxID, string(apigen.BoxActivityDeployed), by, row.App,
		activity{Commit: row.CommitSha, ReleaseID: row.ReleaseID}); err != nil {
		return false, false, err
	}
	return true, private, nil
}

// deployActor 是部署行上记的发起者。
func deployActor(row db.BoxDeploy) Actor {
	by := Actor{Kind: row.Actor}
	if row.ThreadID != nil {
		by.ThreadID = *row.ThreadID
	}
	return by
}

// finish 在独立事务里调 finishDeployTx，并通知变化。
func (s *Service) finish(ctx context.Context, row db.BoxDeploy, state, errText string) (bool, error) {
	tctx, cancel := txCtx(ctx)
	defer cancel()
	var done bool
	err := s.st.Tx(tctx, func(q *db.Queries) error {
		var err error
		done, err = s.finishDeployTx(tctx, q, row, state, errText)
		return err
	})
	if err != nil {
		return false, err
	}
	if done {
		slog.Info("boxes: 部署结束", "deploy", row.ID, "app", row.App, "state", state, "error", errText)
		s.changed()
	}
	return done, nil
}

// complete 在独立事务里调 completeDeployTx；成功之后断开改成私有的 app 的匿名连接、通知变化、触发 released。
func (s *Service) complete(ctx context.Context, row db.BoxDeploy, pruned []string) (bool, error) {
	tctx, cancel := txCtx(ctx)
	defer cancel()
	var done, private bool
	err := s.st.Tx(tctx, func(q *db.Queries) error {
		var err error
		done, private, err = s.completeDeployTx(tctx, q, row, pruned)
		return err
	})
	if err != nil || !done {
		return false, err
	}
	slog.Info("boxes: 部署成功", "deploy", row.ID, "app", row.App, "release", row.ReleaseID)
	if private && s.cfg.DropApp != nil {
		s.cfg.DropApp(row.App)
	}
	s.changed()
	s.released(row.BoxID, row.App)
	return true, nil
}

// released 在 app 换了 live release（部署成功或回滚成功）之后调用。步骤 4.2 在这里起后台的预览探测。
func (s *Service) released(boxID, app string) {}

// resolveActivating 处理一行停在 activating、结果未知的部署：按 orbd 的状态补完或作废。返回最终状态（succeeded /
// failed）；box 连不上或 orbd 还在试运行时返回空串（留给下一轮对账）。
func (s *Service) resolveActivating(ctx context.Context, row db.BoxDeploy) (string, error) {
	b, err := s.st.Q.GetBox(ctx, row.BoxID)
	if errors.Is(err, sql.ErrNoRows) {
		// box 已删除：部署行随它级联删除
		return "", nil
	}
	if err != nil {
		return "", err
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return "", nil
	}
	st, err := s.appsState(ctx, o.ID)
	if err != nil {
		return "", nil
	}
	var cur *apigen.OrbBoxAppState
	for i := range st.Apps {
		if st.Apps[i].Name == row.App {
			cur = &st.Apps[i]
		}
	}
	switch {
	case cur != nil && cur.Activating != nil && *cur.Activating == row.ReleaseID:
		return "", nil
	case cur != nil && cur.Live == row.ReleaseID:
		pruned, err := s.prunedSince(ctx, row, cur.Releases)
		if err != nil {
			return "", err
		}
		done, err := s.complete(ctx, row, pruned)
		if err != nil {
			return "", err
		}
		if !done {
			return s.rowState(ctx, row.ID)
		}
		return deploySucceeded, nil
	}
	done, err := s.finish(ctx, row, deployFailed, "activation did not complete")
	if err != nil {
		return "", err
	}
	if !done {
		return s.rowState(ctx, row.ID)
	}
	return deployFailed, nil
}

// prunedSince 是控制面里该 app 的 live / kept release 中 orbd 已经不保留的那些。
func (s *Service) prunedSince(ctx context.Context, row db.BoxDeploy, kept []string) ([]string, error) {
	app, err := s.st.Q.GetBoxApp(ctx, db.GetBoxAppParams{BoxID: row.BoxID, Name: row.App})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rels, err := s.st.Q.ListBoxReleases(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rels {
		if r.State != string(apigen.BoxReleasePruned) && !slices.Contains(kept, r.ID) {
			out = append(out, r.ID)
		}
	}
	return out, nil
}

// rowState 读部署行当前的状态。
func (s *Service) rowState(ctx context.Context, id string) (string, error) {
	r, err := s.st.Q.GetBoxDeploy(ctx, id)
	if err != nil {
		return "", err
	}
	return r.State, nil
}

// sweepDeploys 收拾不在本进程里的未结束部署：transferring → failed（restarted 时写「控制面重启」），activating → 按 orbd
// 的状态补完或作废。prepared 不动（CLI 的 commit 仍然有效），只受 deployExpire 约束。
func (s *Service) sweepDeploys(ctx context.Context, restarted bool) {
	reason := "interrupted"
	if restarted {
		reason = "control plane restarted"
	}
	for _, state := range []string{deployTransferring, deployActivating} {
		rows, err := s.st.Q.ListBoxDeploysByState(ctx, state)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("boxes: 读取未结束的部署失败", "err", err)
			}
			return
		}
		for _, row := range rows {
			if s.flying(row.ID) != nil {
				continue
			}
			if state == deployTransferring {
				_, err = s.finish(ctx, row, deployFailed, reason)
			} else {
				_, err = s.resolveActivating(ctx, row)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("boxes: 收拾部署失败", "deploy", row.ID, "err", err)
			}
		}
	}
}

// ---- prepare ----

// PrepareDeploy 为一次部署占位：校验 box 与 app、必要时建 box 与临时 app 行、写一行 prepared 的部署。
// 意图（repo、root、source、公开性）只记在部署行上，激活成功才写到 app。
func (s *Service) PrepareDeploy(ctx context.Context, srcOrbID string, p PrepareParams, by Actor) (PrepareResult, error) {
	if !portalhost.ValidAppName(p.App) {
		return PrepareResult{}, errf(CodeInvalid, "invalid app name %q: use lowercase letters, digits and hyphens (up to 20 characters)", p.App)
	}
	if p.Repo == "" || p.Commit == "" || p.Branch == "" {
		return PrepareResult{}, errf(CodeInvalid, "repo, branch and commit are required")
	}
	if !validRoot(p.Root) {
		return PrepareResult{}, errf(CodeInvalid, "invalid root %q", p.Root)
	}
	b, _, err := s.live(ctx, p.Box)
	if codeOf(err) == CodeBoxNotFound && p.Create {
		var v apigen.Box
		if v, err = s.Create(ctx, p.Box, p.Provider, p.Type, by); err == nil {
			b, _, err = s.live(ctx, v.ID)
		}
	}
	if err != nil {
		return PrepareResult{}, err
	}
	if b.Desired == desiredStopped {
		return PrepareResult{}, errf(CodeBoxStopped, "box %s is stopped; start it first", b.Name)
	}
	if err := s.awaitBox(ctx, b); err != nil {
		return PrepareResult{}, err
	}
	// 查未结束的部署到插入部署行整段持有 app 锁：与 Undeploy 等交错时不会留下没有 app 行的部署
	defer s.lockApp(p.App)()

	now := s.now().UnixMilli()
	row := db.CreateBoxDeployParams{
		ID: ids.New("bxd"), BoxID: b.ID, App: p.App, ReleaseID: ids.New("bxr"), Actor: by.Kind, SrcOrbID: srcOrbID,
		Repo: p.Repo, Root: p.Root, Source: p.Source, CommitSha: p.Commit, Branch: p.Branch, CreatedAt: now,
	}
	if by.ThreadID != "" {
		row.ThreadID = &by.ThreadID
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		expired, err := q.ListExpiredBoxDeploys(ctx, s.now().Add(-deployExpire).UnixMilli())
		if err != nil {
			return err
		}
		for _, d := range expired {
			if _, err := s.finishDeployTx(ctx, q, d, deployCanceled, "expired"); err != nil {
				return err
			}
		}
		if _, err := q.GetOpenBoxDeploy(ctx, db.GetOpenBoxDeployParams{BoxID: b.ID, App: p.App}); err == nil {
			return inProgress(b.Name, p.App)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		app, err := q.GetBoxAppByName(ctx, p.App)
		switch {
		case err == nil && app.BoxID != b.ID:
			return errf(CodeAppExistsElsewhere, "app %s already runs on another box; app names are unique across boxes", p.App)
		case err == nil:
			if (app.Repo != p.Repo || app.Root != p.Root) && !p.Move {
				return errf(CodeAppRepoMismatch, "app %s is bound to repo %s (root %q); deploy from there, or pass --move to rebind it",
					p.App, app.Repo, app.Root)
			}
			if p.Public != nil && *p.Public != app.Public {
				v := int64(0)
				if *p.Public {
					v = 1
				}
				row.SetPublic = &v
			}
		case errors.Is(err, sql.ErrNoRows):
			n, err := q.CountBoxApps(ctx, b.ID)
			if err != nil {
				return err
			}
			if n >= MaxAppsPerBox {
				return errf(CodeAppLimit, "at most %d apps per box", MaxAppsPerBox)
			}
			port, err := freePort(ctx, q, b.ID)
			if err != nil {
				return err
			}
			public := p.Public == nil || *p.Public
			if err := q.CreateBoxApp(ctx, db.CreateBoxAppParams{
				ID: ids.New("bxa"), BoxID: b.ID, Name: p.App, Port: port, Public: public, Repo: p.Repo, Root: p.Root,
				Source: p.Source, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
			row.First = true
		default:
			return err
		}
		return q.CreateBoxDeploy(ctx, row)
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: box_deploys") {
			return PrepareResult{}, inProgress(b.Name, p.App)
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed: box_apps.name") {
			return PrepareResult{}, errf(CodeAppExistsElsewhere, "app %s already runs on another box; app names are unique across boxes", p.App)
		}
		return PrepareResult{}, err
	}
	s.changed()
	u := s.appURL(p.App)
	return PrepareResult{
		DeployID: row.ID, ReleaseID: row.ReleaseID, Box: b.Name, App: p.App, URL: u,
		ArchivePath: "~/.cache/oryn/box-releases/" + row.ID + ".tar.gz", MaxBytes: MaxReleaseBytes,
		Env: map[string]string{"ORYN_DEPLOYMENT_URL": u, "ORYN_DEPLOYMENT_NAME": p.App, "ORYN_DEPLOYMENT_BOX": b.Name},
	}, nil
}

func inProgress(box, app string) *Error {
	return errf(CodeDeployInProgress, "a deploy of %s to %s is already in progress; run `oryn boxes cancel-deploy %s --app %s` if it is stuck",
		app, box, box, app)
}

// awaitBox 等 box 可以接收部署：ready、有 link、有 box 特性。有 link 却没有 box 特性、或 orb failed 时立即返回。
func (s *Service) awaitBox(ctx context.Context, b db.Box) error {
	deadline := s.now().Add(prepareWait)
	for {
		o, err := s.orbs.Get(ctx, b.OrbID)
		if errors.Is(err, orbs.ErrNotFound) || (err == nil && gone(o)) {
			return errf(CodeBoxNotFound, "box %s not found", b.Name)
		}
		if err != nil {
			return err
		}
		if o.State == orbs.StateFailed {
			reason, _ := orbs.FailureReason(ctx, s.st.Q, o.ID)
			return errf(CodeBoxNotReady, "box %s failed to start: %s", b.Name, reason)
		}
		if s.orbs.Connected(o.ID) {
			if !s.orbs.HasFeature(o.ID, orbs.FeatureBox) {
				return errf(CodeBoxUnsupported, "this box runs an orbd without box support")
			}
			if o.State == orbs.StateReady {
				return nil
			}
		}
		if !s.now().Before(deadline) {
			return errf(CodeBoxNotReady, "box %s did not become ready in time (%s)", b.Name, o.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(preparePoll):
		}
	}
}

// freePort 取这台 box 上 appPortMin–appPortMax 里未用的最小端口。
func freePort(ctx context.Context, q *db.Queries, boxID string) (int64, error) {
	used, err := q.ListBoxAppPorts(ctx, boxID)
	if err != nil {
		return 0, err
	}
	for p := int64(appPortMin); p <= appPortMax; p++ {
		if !slices.Contains(used, p) {
			return p, nil
		}
	}
	return 0, errf(CodeAppLimit, "no free app port on this box")
}

// ---- commit ----

// CommitDeploy 认领 prepare 出的部署（只认它的源 orb），把源 orb 里的包传给 box，再激活。业务失败在 CommitResult 里
// （OK = false），不是 error。
func (s *Service) CommitDeploy(ctx context.Context, srcOrbID string, p CommitParams) (CommitResult, error) {
	if p.SizeBytes > MaxReleaseBytes {
		return CommitResult{}, errf(CodeReleaseTooLarge, "the release is %d bytes; at most %d (use source: false with artifacts)", p.SizeBytes, MaxReleaseBytes)
	}
	if p.SizeBytes <= 0 || !sha256Re.MatchString(p.SHA256) {
		return CommitResult{}, errf(CodeInvalid, "sizeBytes and a lowercase hex sha256 are required")
	}
	if len(p.Services) == 0 || len(p.Services) > 4 {
		return CommitResult{}, errf(CodeInvalid, "an app has 1 to 4 services")
	}
	svcs, err := json.Marshal(p.Services)
	if err != nil {
		return CommitResult{}, err
	}
	abortCtx, abort := context.WithCancel(s.root)
	defer abort()
	xferCtx, cancelX := context.WithTimeout(abortCtx, transferTimeout)
	defer cancelX()
	row, err := s.st.Q.GetBoxDeploy(ctx, p.DeployID)
	if errors.Is(err, sql.ErrNoRows) {
		return CommitResult{}, errf(CodeDeployNotFound, "deploy %s not found", p.DeployID)
	}
	if err != nil {
		return CommitResult{}, err
	}
	if row.SrcOrbID != srcOrbID || row.State != deployPrepared {
		return CommitResult{}, errf(CodeDeployNotFound, "deploy %s is not waiting for this environment's commit", p.DeployID)
	}
	// 先占槽后认领：对账恰好扫到这一行时不会把正常的部署当成孤儿。槽已被占（同一部署的另一个 commit 正在进行）时
	// 回 deploy_not_found——与认领输掉的结果一致：这次部署已被别的 commit 认领
	release, ok := s.track(row.ID, &flight{boxID: row.BoxID, cancelTransfer: cancelX, abort: abort})
	if !ok {
		return CommitResult{}, errf(CodeDeployNotFound, "deploy %s is already being committed", p.DeployID)
	}
	defer release()
	n, err := s.st.Q.ClaimBoxDeploy(ctx, db.ClaimBoxDeployParams{
		SizeBytes: p.SizeBytes, Sha256: p.SHA256, Services: string(svcs), ID: row.ID, SrcOrbID: srcOrbID,
	})
	if err != nil {
		return CommitResult{}, err
	}
	if n == 0 {
		return CommitResult{}, errf(CodeDeployNotFound, "deploy %s is not waiting for this environment's commit", p.DeployID)
	}
	row.State, row.SizeBytes, row.Sha256, row.Services = deployTransferring, p.SizeBytes, p.SHA256, string(svcs)
	s.changed()

	b, err := s.st.Q.GetBox(ctx, row.BoxID)
	if err != nil {
		return CommitResult{}, err
	}
	boxOrb, err := s.transfer(xferCtx, b, row)
	if err != nil {
		code, msg := errParts(err)
		done, ferr := s.finish(ctx, row, deployFailed, code+": "+msg)
		if ferr != nil {
			return CommitResult{}, ferr
		}
		if !done {
			return CommitResult{Code: codeCanceled, Message: "the deploy was canceled"}, nil
		}
		return CommitResult{Code: code, Message: msg}, nil
	}
	if n, err := s.st.Q.SetBoxDeployActivating(ctx, row.ID); err != nil {
		return CommitResult{}, err
	} else if n == 0 {
		return CommitResult{Code: codeCanceled, Message: "the deploy was canceled"}, nil
	}
	row.State = deployActivating
	s.changed()

	// 激活：CancelDeploy 影响不到（换链接换到一半不能打断），删 box 与进程退出照样中止
	actCtx, cancelA := context.WithTimeout(abortCtx, activateTimeout)
	defer cancelA()
	return s.activate(actCtx, b, boxOrb, row, p.DiscardBoxChanges)
}

// transfer 把源 orb 的包（GET /fs/file）直接接到 box 的 PUT /box/incoming/<releaseId>，不落控制面磁盘。返回 box 的 orb。
func (s *Service) transfer(ctx context.Context, b db.Box, row db.BoxDeploy) (db.Orb, error) {
	o, err := s.usable(ctx, b)
	if err != nil {
		return db.Orb{}, err
	}
	get, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"/fs/file?"+url.Values{"path": {"~/.cache/oryn/box-releases/" + row.ID + ".tar.gz"}}.Encode(), nil)
	if err != nil {
		return db.Orb{}, err
	}
	src, err := s.orbs.Do(ctx, row.SrcOrbID, get)
	if err != nil {
		return db.Orb{}, errf(codeTransferFailed, "cannot read the release from the thread's environment: %v", err)
	}
	defer src.Body.Close()
	if src.StatusCode != http.StatusOK {
		return db.Orb{}, errf(codeArchiveMissing, "the release archive is missing in the thread's environment (%d)", src.StatusCode)
	}
	if src.ContentLength >= 0 && src.ContentLength != row.SizeBytes {
		return db.Orb{}, errf(codeSizeMismatch, "the archive is %d bytes, expected %d", src.ContentLength, row.SizeBytes)
	}
	q := url.Values{"size": {strconv.FormatInt(row.SizeBytes, 10)}, "sha256": {row.Sha256}}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, "/box/incoming/"+row.ReleaseID+"?"+q.Encode(), src.Body)
	if err != nil {
		return db.Orb{}, err
	}
	put.ContentLength = row.SizeBytes
	put.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.orbDo(ctx, o.ID, put, codeTransferFailed)
	if err != nil {
		if isUnreachable(err) {
			return db.Orb{}, errf(codeTransferFailed, "upload to the box failed: %v", err)
		}
		return db.Orb{}, err
	}
	_ = resp.Body.Close()
	return o, nil
}

// activate 调 orbd 的 activate 并按结果收尾；调用本身失败（结果未知）时按 orbd 的状态补完，仍定不下来就回 activate_unknown。
func (s *Service) activate(ctx context.Context, b db.Box, o db.Orb, row db.BoxDeploy, discard bool) (CommitResult, error) {
	app, err := s.st.Q.GetBoxApp(ctx, db.GetBoxAppParams{BoxID: b.ID, Name: row.App})
	if err != nil {
		return CommitResult{}, err
	}
	env, err := s.appEnv(ctx, s.st.Q, b, row.App)
	if err != nil {
		return CommitResult{}, err
	}
	var svcs []apigen.OrbBoxService
	if err := json.Unmarshal([]byte(row.Services), &svcs); err != nil {
		return CommitResult{}, err
	}
	req := apigen.OrbBoxActivateRequest{
		ReleaseID: row.ReleaseID, Port: int(app.Port), Services: svcs, Env: env, Source: row.Source,
		Keep: keepReleases, ReadyTimeoutSec: readyTimeoutSec,
	}
	if discard {
		req.DiscardBoxChanges = new(true)
	}
	var res apigen.OrbBoxResult
	err = s.orbJSON(ctx, o.ID, http.MethodPost, "/box/apps/"+row.App+"/activate", req, &res)
	if err != nil && !isUnreachable(err) {
		code, msg := errParts(err)
		if _, ferr := s.finish(ctx, row, deployFailed, code+": "+msg); ferr != nil {
			return CommitResult{}, ferr
		}
		return CommitResult{Code: code, Message: msg}, nil
	}
	if err != nil {
		state, rerr := s.resolveActivating(context.WithoutCancel(ctx), row)
		switch {
		case rerr != nil:
			return CommitResult{}, rerr
		case state == deploySucceeded:
			return CommitResult{OK: true, URL: s.appURL(row.App), ReleaseID: row.ReleaseID}, nil
		case state == deployFailed:
			return CommitResult{Code: "activation_failed", Message: "the release was not activated; the box kept the previous one"}, nil
		}
		return CommitResult{Code: codeActivateUnknown, Message: "the box did not answer; the deploy finishes or rolls back on its own"}, nil
	}
	if !res.Ok {
		code, msg, log := deref(res.Code), deref(res.Message), deref(res.Log)
		if code == "" {
			code = "activation_failed"
		}
		if _, ferr := s.finish(ctx, row, deployFailed, code+": "+msg); ferr != nil {
			return CommitResult{}, ferr
		}
		return CommitResult{Code: code, Message: msg, Log: log}, nil
	}
	done, err := s.complete(ctx, row, res.Pruned)
	if err != nil {
		return CommitResult{}, err
	}
	if !done {
		return CommitResult{Code: codeCanceled, Message: "the deploy was finished elsewhere"}, nil
	}
	return CommitResult{OK: true, URL: s.appURL(row.App), ReleaseID: row.ReleaseID}, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// errParts 取错误的码与文案（不是 *Error 时码为 internal）。
func errParts(err error) (string, string) {
	var be *Error
	if errors.As(err, &be) {
		return be.Code, be.Message
	}
	return "internal", err.Error()
}

// codeOf 是错误的码（不是 *Error 时为空串）。
func codeOf(err error) string {
	var be *Error
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}

// CancelDeploy 取消 app 未结束的部署：prepared / transferring 的直接作废（掐断在途传输）；已经在激活的不能打断
// （deploy_in_progress）。
func (s *Service) CancelDeploy(ctx context.Context, ref, app string) error {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return err
	}
	row, err := s.st.Q.GetOpenBoxDeploy(ctx, db.GetOpenBoxDeployParams{BoxID: b.ID, App: app})
	if errors.Is(err, sql.ErrNoRows) {
		return errf(CodeDeployNotFound, "no deploy of %s is in progress on %s", app, b.Name)
	}
	if err != nil {
		return err
	}
	tctx, cancel := txCtx(ctx)
	defer cancel()
	canceled := false
	err = s.st.Tx(tctx, func(q *db.Queries) error {
		now := s.now().UnixMilli()
		n, err := q.CancelBoxDeploy(tctx, db.CancelBoxDeployParams{Now: &now, ID: row.ID})
		if err != nil || n == 0 {
			return err
		}
		canceled = true
		if row.First {
			_, err = q.DeleteProvisionalBoxApp(tctx, db.DeleteProvisionalBoxAppParams{BoxID: row.BoxID, Name: row.App})
		}
		return err
	})
	if err != nil {
		return err
	}
	if !canceled {
		return errf(CodeDeployInProgress, "the release is being activated; it finishes or rolls back within a few minutes")
	}
	if f := s.flying(row.ID); f != nil {
		f.cancelTransfer()
	}
	slog.Info("boxes: 部署已取消", "deploy", row.ID, "app", row.App)
	s.changed()
	return nil
}
