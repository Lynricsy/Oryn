package boxes

import (
	"bufio"
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
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 密钥、exec、cp 与日志的限额。
const (
	maxSecrets      = 64
	maxSecretValue  = 64 << 10
	maxSecretsTotal = 1 << 20
	execDefaultSec  = 600
	execMaxSec      = 3600
	copyTimeout     = 30 * time.Minute
	logDefaultTail  = 65536
	logMaxTail      = 1 << 20
)

var secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// reservedEnv 是控制面与 orbd 自己设置的环境变量，不能作为密钥名。
var reservedEnv = map[string]bool{
	"PORT": true, "ORYN_DEPLOYMENT_URL": true, "ORYN_DEPLOYMENT_NAME": true, "ORYN_DEPLOYMENT_BOX": true,
	"ORYN_DEPLOYMENT_DATA_DIRECTORY": true,
}

// secretKey 是密钥加密时绑定的名字。
func secretKey(boxID, app, name string) string { return "box/" + boxID + "/" + app + "/" + name }

// openSecret 解密一条密钥。
func (s *Service) openSecret(boxID, app string, r db.BoxSecret) (string, error) {
	if s.cfg.Box == nil {
		return "", errors.New("boxes: secrets box not configured")
	}
	v, err := s.cfg.Box.Open(secretKey(boxID, app, r.Name), r.ValueEnc)
	if err != nil {
		return "", err
	}
	return string(v), nil
}

// appOf 取 box 上的 app 行；没有 → app_not_found。
func (s *Service) appOf(ctx context.Context, q *db.Queries, b db.Box, app string) (db.BoxApp, error) {
	a, err := q.GetBoxApp(ctx, db.GetBoxAppParams{BoxID: b.ID, Name: app})
	if errors.Is(err, sql.ErrNoRows) {
		return db.BoxApp{}, errf(CodeAppNotFound, "app %s not found on box %s", app, b.Name)
	}
	return a, err
}

// noOpenDeploy 在 app 有未结束的部署时返回 deploy_in_progress。
func (s *Service) noOpenDeploy(ctx context.Context, q *db.Queries, b db.Box, app string) error {
	_, err := q.GetOpenBoxDeploy(ctx, db.GetOpenBoxDeployParams{BoxID: b.ID, App: app})
	if err == nil {
		return inProgress(b.Name, app)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// appViewByName 重读 app 行并转成 API 形态（含 deploying 与服务镜像）。
func (s *Service) appViewByName(ctx context.Context, b db.Box, app string) (apigen.BoxApp, error) {
	q := s.st.Q
	a, err := s.appOf(ctx, q, b, app)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	deploying := true
	if err := s.noOpenDeploy(ctx, q, b, app); err == nil {
		deploying = false
	} else if codeOf(err) != CodeDeployInProgress {
		return apigen.BoxApp{}, err
	}
	svcs, _, err := orbs.ServicesView(ctx, q, b.OrbID, s.now().UnixMilli())
	if err != nil {
		return apigen.BoxApp{}, err
	}
	return s.appView(ctx, q, a, deploying, svcs)
}

// orbResultErr 把 orbd 的业务失败（ok = false）转成错误。
func orbResultErr(res apigen.OrbBoxResult) error {
	code := deref(res.Code)
	if code == "" {
		code = "internal"
	}
	msg := deref(res.Message)
	if l := deref(res.Log); l != "" {
		msg += "\n" + l
	}
	return &Error{Code: code, Message: msg}
}

// SetPublic 设置 app 的公开性；改成私有时在提交之后断开它的匿名连接。
func (s *Service) SetPublic(ctx context.Context, ref, app string, public bool, by Actor) (apigen.BoxApp, error) {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	defer s.lockApp(app)()
	changed := false
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		a, err := s.appOf(ctx, q, b, app)
		if err != nil || a.Public == public {
			return err
		}
		if _, err := q.SetBoxAppPublic(ctx, db.SetBoxAppPublicParams{Public: public, UpdatedAt: s.now().UnixMilli(), ID: a.ID}); err != nil {
			return err
		}
		changed = true
		return s.record(ctx, q, b.ID, string(apigen.BoxActivityVisibility), by, app, activity{Public: &public})
	})
	if err != nil {
		return apigen.BoxApp{}, err
	}
	if changed {
		if !public && s.cfg.DropApp != nil {
			s.cfg.DropApp(app)
		}
		s.changed()
	}
	return s.appViewByName(ctx, b, app)
}

// Undeploy 下线 app：orbd 停掉服务并删 release（purge 连数据目录），然后删 app 行。box 停着时不做半截下线。
// 密钥按 (box, app 名) 保留。
func (s *Service) Undeploy(ctx context.Context, ref, app string, purge bool, by Actor) error {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return err
	}
	defer s.lockApp(app)()
	a, err := s.appOf(ctx, s.st.Q, b, app)
	if err != nil {
		return err
	}
	if err := s.noOpenDeploy(ctx, s.st.Q, b, app); err != nil {
		return err
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return err
	}
	p := "0"
	if purge {
		p = "1"
	}
	if err := s.orbJSON(ctx, o.ID, http.MethodDelete, "/box/apps/"+app+"?purge="+p, nil, nil); err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.DeleteBoxApp(ctx, a.ID); err != nil {
			return err
		}
		return s.record(ctx, q, b.ID, string(apigen.BoxActivityUndeployed), by, app, activity{})
	})
	if err != nil {
		return err
	}
	slog.Info("boxes: app 已下线", "box", b.Name, "app", app, "purge", purge)
	if s.cfg.DropApp != nil {
		s.cfg.DropApp(app)
	}
	s.changed()
	return nil
}

// Restart 以当前的密钥重启 app 的全部服务并等就绪；不回滚。
func (s *Service) Restart(ctx context.Context, ref, app string, by Actor) (apigen.BoxApp, error) {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	defer s.lockApp(app)()
	a, err := s.appOf(ctx, s.st.Q, b, app)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	if err := s.noOpenDeploy(ctx, s.st.Q, b, app); err != nil {
		return apigen.BoxApp{}, err
	}
	if a.LiveReleaseID == nil {
		return apigen.BoxApp{}, errf(CodeAppNotDeployed, "app %s has no live release yet", app)
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	env, err := s.appEnv(ctx, s.st.Q, b, app)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	var res apigen.OrbBoxResult
	if err := s.orbJSON(ctx, o.ID, http.MethodPost, "/box/apps/"+app+"/restart",
		apigen.OrbBoxRestartRequest{Env: env, ReadyTimeoutSec: readyTimeoutSec}, &res); err != nil {
		return apigen.BoxApp{}, err
	}
	if !res.Ok {
		return apigen.BoxApp{}, orbResultErr(res)
	}
	if err := s.record(ctx, s.st.Q, b.ID, string(apigen.BoxActivityRestarted), by, app, activity{}); err != nil {
		return apigen.BoxApp{}, err
	}
	s.changed()
	return s.appViewByName(ctx, b, app)
}

// Rollback 把 app 切回一个保留着的 release（releaseID 为空 = 最新的 kept）。repo / root / source 随之回到那个 release 的。
// orbd 对「目标已经是 live」直接回 ok，所以上一次的应答丢了时再回滚一次就把库补齐。
func (s *Service) Rollback(ctx context.Context, ref, app, releaseID string, by Actor) (apigen.BoxApp, error) {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	defer s.lockApp(app)()
	a, err := s.appOf(ctx, s.st.Q, b, app)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	if err := s.noOpenDeploy(ctx, s.st.Q, b, app); err != nil {
		return apigen.BoxApp{}, err
	}
	rels, err := s.st.Q.ListBoxReleases(ctx, a.ID)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	var target *db.BoxRelease
	for i := range rels {
		r := &rels[i]
		if r.State == string(apigen.BoxReleaseKept) && (releaseID == "" || r.ID == releaseID) {
			target = r
			break
		}
	}
	if target == nil {
		if releaseID == "" {
			return apigen.BoxApp{}, errf(CodeReleaseNotFound, "app %s has no earlier release to roll back to", app)
		}
		return apigen.BoxApp{}, errf(CodeReleaseNotFound, "release %s is not a kept release of %s", releaseID, app)
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	var res apigen.OrbBoxResult
	if err := s.orbJSON(ctx, o.ID, http.MethodPost, "/box/apps/"+app+"/rollback",
		apigen.OrbBoxRollbackRequest{ReleaseID: target.ID, ReadyTimeoutSec: readyTimeoutSec}, &res); err != nil {
		return apigen.BoxApp{}, err
	}
	if !res.Ok {
		return apigen.BoxApp{}, orbResultErr(res)
	}
	tctx, cancel := txCtx(ctx)
	defer cancel()
	err = s.st.Tx(tctx, func(q *db.Queries) error {
		now := s.now().UnixMilli()
		cur, err := s.appOf(tctx, q, b, app)
		if err != nil {
			return err
		}
		if cur.LiveReleaseID != nil && *cur.LiveReleaseID != target.ID {
			if _, err := q.SetBoxReleaseState(tctx, db.SetBoxReleaseStateParams{State: string(apigen.BoxReleaseKept), ID: *cur.LiveReleaseID}); err != nil {
				return err
			}
		}
		if _, err := q.SetBoxReleaseActivated(tctx, db.SetBoxReleaseActivatedParams{State: string(apigen.BoxReleaseLive), ActivatedAt: now, ID: target.ID}); err != nil {
			return err
		}
		if _, err := q.SetBoxAppLive(tctx, db.SetBoxAppLiveParams{
			LiveReleaseID: &target.ID, Repo: target.Repo, Root: target.Root, Source: target.Source, UpdatedAt: now, ID: cur.ID,
		}); err != nil {
			return err
		}
		return s.record(tctx, q, b.ID, string(apigen.BoxActivityRolledBack), by, app,
			activity{ReleaseID: target.ID, Commit: target.CommitSha})
	})
	if err != nil {
		return apigen.BoxApp{}, err
	}
	s.changed()
	s.released(b.ID, app)
	return s.appViewByName(ctx, b, app)
}

// Releases 列出 app 的 release（新到旧，含已裁剪的）。
func (s *Service) Releases(ctx context.Context, ref, app string) ([]apigen.BoxRelease, error) {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return nil, err
	}
	a, err := s.appOf(ctx, s.st.Q, b, app)
	if err != nil {
		return nil, err
	}
	rels, err := s.st.Q.ListBoxReleases(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.BoxRelease, 0, len(rels))
	for _, r := range rels {
		out = append(out, releaseView(r))
	}
	return out, nil
}

// secretTarget 校验 box 与 app 名（密钥按 app 名存，首次部署之前就能设）。
func (s *Service) secretTarget(ctx context.Context, ref, app string) (db.Box, error) {
	if !portalhost.ValidAppName(app) {
		return db.Box{}, errf(CodeInvalid, "invalid app name %q", app)
	}
	b, _, err := s.live(ctx, ref)
	return b, err
}

// Secrets 列出 app 的密钥；明文环境变量（secret = false）带值。
func (s *Service) Secrets(ctx context.Context, ref, app string) ([]apigen.BoxSecret, error) {
	b, err := s.secretTarget(ctx, ref, app)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListBoxSecrets(ctx, db.ListBoxSecretsParams{BoxID: b.ID, App: app})
	if err != nil {
		return nil, err
	}
	out := make([]apigen.BoxSecret, 0, len(rows))
	for _, r := range rows {
		v := apigen.BoxSecret{Name: r.Name, Secret: r.Secret, UpdatedAt: r.UpdatedAt}
		if !r.Secret {
			plain, err := s.openSecret(b.ID, app, r)
			if err != nil {
				return nil, err
			}
			v.Value = &plain
		}
		out = append(out, v)
	}
	return out, nil
}

// PutSecret 设置一条密钥（secret = false 为明文环境变量）。改动在下一次部署或 Restart 之后才进到进程里。
func (s *Service) PutSecret(ctx context.Context, ref, app, name, value string, secret bool) error {
	if !secretNameRe.MatchString(name) {
		return errf(CodeInvalid, "invalid secret name %q: letters, digits and underscores, not starting with a digit", name)
	}
	if reservedEnv[name] {
		return errf(CodeInvalid, "%s is set by Oryn and cannot be a secret", name)
	}
	if len(value) > maxSecretValue {
		return errf(CodeInvalid, "a secret value is at most %d bytes", maxSecretValue)
	}
	if s.cfg.Box == nil {
		return errors.New("boxes: secrets box not configured")
	}
	b, err := s.secretTarget(ctx, ref, app)
	if err != nil {
		return err
	}
	enc, err := s.cfg.Box.Seal(secretKey(b.ID, app, name), []byte(value))
	if err != nil {
		return err
	}
	size := int64(len(name) + len(value))
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		rows, err := q.ListBoxSecrets(ctx, db.ListBoxSecretsParams{BoxID: b.ID, App: app})
		if err != nil {
			return err
		}
		exists := false
		for _, r := range rows {
			exists = exists || r.Name == name
		}
		if !exists && len(rows) >= maxSecrets {
			return errf(CodeInvalid, "an app has at most %d secrets", maxSecrets)
		}
		others, err := q.SumBoxSecretSize(ctx, db.SumBoxSecretSizeParams{BoxID: b.ID, App: app, Name: name})
		if err != nil {
			return err
		}
		if others+size > maxSecretsTotal {
			return errf(CodeInvalid, "an app's secrets total at most %d bytes (names plus values)", maxSecretsTotal)
		}
		return q.PutBoxSecret(ctx, db.PutBoxSecretParams{
			BoxID: b.ID, App: app, Name: name, ValueEnc: enc, Size: size, Secret: secret, UpdatedAt: s.now().UnixMilli(),
		})
	})
	if err != nil {
		return err
	}
	s.changed()
	return nil
}

// DeleteSecret 删除一条密钥（不存在时无操作）。
func (s *Service) DeleteSecret(ctx context.Context, ref, app, name string) error {
	b, err := s.secretTarget(ctx, ref, app)
	if err != nil {
		return err
	}
	n, err := s.st.Q.DeleteBoxSecret(ctx, db.DeleteBoxSecretParams{BoxID: b.ID, App: app, Name: name})
	if err != nil {
		return err
	}
	if n > 0 {
		s.changed()
	}
	return nil
}

// Exec 在 box 上跑一条命令（cwd = `~`，给了 app 则是它的线上目录），把 orbd 的 NDJSON 原样写给 w：
// `{"s":"o"|"e","d":…}`…`{"exit":N}`。ctx 结束时请求随之取消，orbd 杀掉进程组。出错时 w 还没写过任何东西。
func (s *Service) Exec(ctx context.Context, ref string, req apigen.BoxExecRequest, w io.Writer) error {
	if strings.TrimSpace(req.Command) == "" {
		return errf(CodeInvalid, "command is required")
	}
	timeout := execDefaultSec
	if req.TimeoutSec != nil {
		timeout = *req.TimeoutSec
	}
	if timeout < 1 || timeout > execMaxSec {
		return errf(CodeInvalid, "timeoutSec must be 1–%d", execMaxSec)
	}
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return err
	}
	cwd := "~"
	if app := deref(req.App); app != "" {
		a, err := s.appOf(ctx, s.st.Q, b, app)
		if err != nil {
			return err
		}
		if a.LiveReleaseID == nil {
			return errf(CodeAppNotDeployed, "app %s has no live release yet", app)
		}
		cwd = "~/apps/" + app + "/current"
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(apigen.OrbBoxExecRequest{Command: req.Command, Cwd: cwd, TimeoutSec: timeout})
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/box/exec", strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := s.orbDo(ctx, o.ID, hreq, "internal")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	if err != nil && ctx.Err() == nil {
		slog.Warn("boxes: exec 输出中断", "box", b.Name, "err", err)
	}
	return nil
}

// execResult 是 box.call exec 的结果：两路输出各截到 execCap。
type execResult struct {
	Exit      int    `json:"exit"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

const execCap = 256 << 10

// collectExec 把 orbd 的 NDJSON 收成 execResult。
func collectExec(r io.Reader) (execResult, error) {
	var res execResult
	var out, errb strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	gotExit := false
	for sc.Scan() {
		var line struct {
			S    string `json:"s"`
			D    string `json:"d"`
			Exit *int   `json:"exit"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Exit != nil {
			res.Exit, gotExit = *line.Exit, true
			continue
		}
		dst := &out
		if line.S == "e" {
			dst = &errb
		}
		if room := execCap - dst.Len(); room < len(line.D) {
			dst.WriteString(line.D[:max(room, 0)])
			res.Truncated = true
		} else {
			dst.WriteString(line.D)
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	if !gotExit {
		return res, errf(CodeBoxNotReady, "the box closed the command's output before it finished")
	}
	res.Stdout, res.Stderr = out.String(), errb.String()
	return res, nil
}

// Copy 在调用方的 orb 与 box 之间复制一个文件（只给 box.call 用）：boxPath 相对 `~/apps/<app>/data/`（给了 app）或 `~/`；
// orbPath 是绝对路径或 `~/…`。目标的父目录必须已存在。返回字节数。
func (s *Service) Copy(ctx context.Context, srcOrbID, ref, app, boxPath, orbPath string, toBox bool) (int64, error) {
	clean := path.Clean(boxPath)
	if boxPath == "" || path.IsAbs(boxPath) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return 0, errf(CodeInvalid, "box path must be relative and stay inside the target directory")
	}
	if !path.IsAbs(orbPath) && !strings.HasPrefix(orbPath, "~/") {
		return 0, errf(CodeInvalid, "the local path must be absolute")
	}
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return 0, err
	}
	full := "~/" + clean
	if app != "" {
		if _, err := s.appOf(ctx, s.st.Q, b, app); err != nil {
			return 0, err
		}
		full = "~/apps/" + app + "/data/" + clean
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	from, fromPath, to, toPath := srcOrbID, orbPath, o.ID, full
	if !toBox {
		from, fromPath, to, toPath = o.ID, full, srcOrbID, orbPath
	}
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, "/fs/file?"+url.Values{"path": {fromPath}}.Encode(), nil)
	if err != nil {
		return 0, err
	}
	src, err := s.orbDo(ctx, from, get, "not_found")
	if err != nil {
		return 0, err
	}
	defer src.Body.Close()
	cr := &countReader{r: src.Body}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"/fs/file?"+url.Values{"path": {toPath}, "overwrite": {"true"}}.Encode(), cr)
	if err != nil {
		return 0, err
	}
	put.ContentLength = src.ContentLength
	put.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.orbDo(ctx, to, put, "internal")
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return cr.n, nil
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Logs 读 app 一个服务日志的末尾 tail 字节（service 为空 = 该 app live release 的第一个服务；只给 box.call 用）。
func (s *Service) Logs(ctx context.Context, ref, app, service string, tail int) (string, error) {
	if tail <= 0 {
		tail = logDefaultTail
	}
	tail = min(tail, logMaxTail)
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return "", err
	}
	a, err := s.appOf(ctx, s.st.Q, b, app)
	if err != nil {
		return "", err
	}
	if service == "" {
		if a.LiveReleaseID == nil {
			return "", errf(CodeAppNotDeployed, "app %s has no live release yet", app)
		}
		r, err := s.st.Q.GetBoxRelease(ctx, *a.LiveReleaseID)
		if err != nil {
			return "", err
		}
		var svcs []apigen.OrbBoxService
		if err := json.Unmarshal([]byte(r.Services), &svcs); err != nil || len(svcs) == 0 {
			return "", errf(CodeAppNotDeployed, "app %s has no services", app)
		}
		service = svcs[0].Name
	}
	o, err := s.usable(ctx, b)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"/services/"+url.PathEscape(app+"--"+service)+"/log?tail="+strconv.Itoa(tail), nil)
	if err != nil {
		return "", err
	}
	resp, err := s.orbDo(ctx, o.ID, req, "not_found")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(io.LimitReader(resp.Body, int64(tail)+64<<10))
	if err != nil {
		return "", unreachable{errf(CodeBoxNotReady, "box unreachable: %v", err)}
	}
	return string(text), nil
}
