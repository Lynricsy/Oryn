package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn boxes …` 的传输层（M23）：orb 内经 orbd.sock 的 box.call（ctl 同名方法）由控制面执行；
// orb 外用设备令牌的 REST。每个子命令只写一次，按 inOrb() 选实现。

// boxAPI 是 boxes 子命令依赖的传输。op 名与参数照 box.call 的 op 表；结果一律是对象。
type boxAPI interface {
	// inOrb 报告是否走 orb 内的 box.call。
	inOrb() bool
	// call 执行一个 op，把结果对象解到 out（out 为 nil 时丢弃）。
	call(ctx context.Context, op string, params map[string]any, out any) error
	// exec 在 box 上执行命令，输出写到 stdout / stderr，返回远端退出码。
	exec(ctx context.Context, p boxExecParams, stdout, stderr io.Writer) (int, error)
	// copy 在 box 与本地（orb 或本机）之间复制一个文件，返回字节数。
	copy(ctx context.Context, p boxCopyParams) (int64, error)
}

type boxExecParams struct {
	Box, App, Command string
	TimeoutSec        int
}

// boxCopyParams：BoxPath 相对 `~/apps/<app>/data/`（给了 App）或 `~/`；LocalPath 是绝对路径。
type boxCopyParams struct {
	Box, App, BoxPath, LocalPath string
	ToBox                        bool
}

// deploy.prepare / deploy.commit 的线路形状（与控制面 internal/boxes 的 PrepareParams 等字段名一致）。
type boxPrepareParams struct {
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
	Public   *bool  `json:"public,omitempty"`
}

type boxPrepareResult struct {
	DeployID    string            `json:"deployId"`
	ReleaseID   string            `json:"releaseId"`
	Box         string            `json:"box"`
	App         string            `json:"app"`
	URL         string            `json:"url"`
	ArchivePath string            `json:"archivePath"`
	MaxBytes    int64             `json:"maxBytes"`
	Env         map[string]string `json:"env"`
}

type boxCommitParams struct {
	DeployID          string                 `json:"deployId"`
	SHA256            string                 `json:"sha256"`
	SizeBytes         int64                  `json:"sizeBytes"`
	Services          []apigen.OrbBoxService `json:"services"`
	DiscardBoxChanges bool                   `json:"discardBoxChanges,omitempty"`
}

type boxCommitResult struct {
	OK        bool   `json:"ok"`
	URL       string `json:"url"`
	ReleaseID string `json:"releaseId"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Log       string `json:"log"`
}

// boxExecResult 是 orb 内 exec op 的结果（整段输出，两路各至多 256KiB）。
type boxExecResult struct {
	Exit      int    `json:"exit"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

// boxErr 是 box op 的错误（控制面的 {code, message}）。
type boxErr struct{ Code, Message string }

func (e *boxErr) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// toMap 把结构体经 JSON 转成参数表。
func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// newBoxAPI 按运行位置选传输。
func newBoxAPI() (boxAPI, error) {
	if inOrb() {
		thread, err := callerThread()
		if err != nil {
			return nil, err
		}
		return &orbBoxAPI{thread: thread}, nil
	}
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return &restBoxAPI{c: c}, nil
}

// ---------------------------------------------------------------- orb 内：box.call

type orbBoxAPI struct{ thread string }

func (a *orbBoxAPI) inOrb() bool { return true }

func (a *orbBoxAPI) call(ctx context.Context, op string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	var res apigen.LinkCtlBoxCallResult
	err := sockCall(ctx, orbsock.MethodBoxCall, apigen.LinkCtlBoxCallParams{ThreadID: a.thread, Op: op, Params: params}, &res, nil)
	if err != nil {
		var se *orbsock.Error
		if errors.As(err, &se) {
			return &boxErr{Code: se.Code, Message: se.Message}
		}
		return err
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(res.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (a *orbBoxAPI) exec(ctx context.Context, p boxExecParams, stdout, stderr io.Writer) (int, error) {
	params := map[string]any{"box": p.Box, "command": p.Command}
	if p.App != "" {
		params["app"] = p.App
	}
	if p.TimeoutSec > 0 {
		params["timeoutSec"] = p.TimeoutSec
	}
	var res boxExecResult
	if err := a.call(ctx, "exec", params, &res); err != nil {
		return 0, err
	}
	_, _ = io.WriteString(stdout, res.Stdout)
	_, _ = io.WriteString(stderr, res.Stderr)
	if res.Truncated {
		fmt.Fprintln(stderr, "oryn: output truncated to 256 KiB per stream")
	}
	return res.Exit, nil
}

func (a *orbBoxAPI) copy(ctx context.Context, p boxCopyParams) (int64, error) {
	params := map[string]any{"box": p.Box, "boxPath": p.BoxPath, "orbPath": p.LocalPath, "toBox": p.ToBox}
	if p.App != "" {
		params["app"] = p.App
	}
	var res struct {
		Bytes int64 `json:"bytes"`
	}
	if err := a.call(ctx, "cp", params, &res); err != nil {
		return 0, err
	}
	return res.Bytes, nil
}

// ---------------------------------------------------------------- orb 外：REST

type restBoxAPI struct{ c *apiClient }

func (a *restBoxAPI) inOrb() bool { return false }

const boxesPath = "/api/v1/boxes"

func boxPath(box, suffix string) string { return boxesPath + "/" + escapePathParam(box) + suffix }

func boxAppPath(box, app, suffix string) string {
	return boxPath(box, "/apps/"+escapePathParam(app)+suffix)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func (a *restBoxAPI) call(ctx context.Context, op string, p map[string]any, out any) error {
	err := a.callREST(ctx, op, p, out)
	var ae *apiError
	if errors.As(err, &ae) {
		return &boxErr{Code: ae.Code, Message: ae.Message}
	}
	return err
}

func (a *restBoxAPI) callREST(ctx context.Context, op string, p map[string]any, out any) error {
	box, app := str(p, "box"), str(p, "app")
	switch op {
	case "list":
		return a.c.do(ctx, http.MethodGet, boxesPath, nil, out)
	case "show":
		return a.c.do(ctx, http.MethodGet, boxPath(box, ""), nil, out)
	case "create":
		body := apigen.CreateBoxRequest{Name: str(p, "name")}
		if v := str(p, "provider"); v != "" {
			body.Provider = &v
		}
		if v := str(p, "type"); v != "" {
			body.Type = &v
		}
		return a.c.do(ctx, http.MethodPost, boxesPath, body, out)
	case "start", "stop":
		return a.c.do(ctx, http.MethodPost, boxPath(box, "/"+op), nil, out)
	case "rename":
		name := str(p, "name")
		return a.c.do(ctx, http.MethodPatch, boxPath(box, ""), apigen.UpdateBoxRequest{Name: &name}, out)
	case "schedule":
		var body apigen.UpdateBoxRequest
		if clear, _ := p["clear"].(bool); clear {
			body.ClearSchedule = new(true)
		} else {
			body.Schedule = &apigen.BoxSchedule{On: str(p, "on"), Off: str(p, "off"), Tz: str(p, "tz")}
		}
		return a.c.do(ctx, http.MethodPatch, boxPath(box, ""), body, out)
	case "delete":
		return a.c.do(ctx, http.MethodDelete, boxPath(box, ""), nil, nil)
	case "deploy.cancel":
		return a.c.do(ctx, http.MethodPost, boxAppPath(box, app, "/cancel-deploy"), nil, nil)
	case "visibility":
		pub, _ := p["public"].(bool)
		return a.c.do(ctx, http.MethodPatch, boxAppPath(box, app, ""), apigen.UpdateBoxAppRequest{Public: pub}, out)
	case "undeploy":
		q := ""
		if purge, _ := p["purgeData"].(bool); purge {
			q = "?purgeData=true"
		}
		return a.c.do(ctx, http.MethodDelete, boxAppPath(box, app, q), nil, nil)
	case "restart":
		return a.c.do(ctx, http.MethodPost, boxAppPath(box, app, "/restart"), nil, out)
	case "rollback":
		var body apigen.BoxRollbackRequest
		if v := str(p, "releaseId"); v != "" {
			body.ReleaseID = &v
		}
		return a.c.do(ctx, http.MethodPost, boxAppPath(box, app, "/rollback"), body, out)
	case "releases":
		return a.c.do(ctx, http.MethodGet, boxAppPath(box, app, "/releases"), nil, out)
	case "secrets.list":
		return a.c.do(ctx, http.MethodGet, boxAppPath(box, app, "/secrets"), nil, out)
	case "secrets.set":
		body := apigen.PutBoxSecretRequest{Value: str(p, "value")}
		if s, ok := p["secret"].(bool); ok {
			body.Secret = &s
		}
		return a.c.do(ctx, http.MethodPut, boxAppPath(box, app, "/secrets/"+escapePathParam(str(p, "name"))), body, nil)
	case "secrets.unset":
		return a.c.do(ctx, http.MethodDelete, boxAppPath(box, app, "/secrets/"+escapePathParam(str(p, "name"))), nil, nil)
	case "logs":
		return a.logs(ctx, box, app, str(p, "service"), p["tail"], out)
	}
	return usageErr("`%s` is only available inside a thread's environment", op)
}

// logs 经现有的服务日志接口取 `<app>--<svc>` 的日志末尾。
func (a *restBoxAPI) logs(ctx context.Context, box, app, svc string, tail any, out any) error {
	var b apigen.Box
	if err := a.c.do(ctx, http.MethodGet, boxPath(box, ""), nil, &b); err != nil {
		return err
	}
	name, err := appServiceName(b, app, svc)
	if err != nil {
		return err
	}
	q := url.Values{}
	if n, ok := tail.(int); ok && n > 0 {
		q.Set("tail", strconv.Itoa(n))
	}
	p := "/api/v1/orbs/" + escapePathParam(b.OrbID) + "/services/" + escapePathParam(name) + "/log"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	rc, err := a.c.getRaw(ctx, p)
	if err != nil {
		return err
	}
	defer rc.Close()
	text, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	if out != nil {
		raw, _ := json.Marshal(map[string]string{"text": string(text)})
		return json.Unmarshal(raw, out)
	}
	return nil
}

// appServiceName 返回 app 的服务在监督器里的完整名字：给了 svc 就是 `<app>--<svc>`，否则取第一个服务。
func appServiceName(b apigen.Box, app, svc string) (string, error) {
	if svc != "" {
		return app + "--" + svc, nil
	}
	for _, a := range b.Apps {
		if a.Name == app {
			if len(a.Services) == 0 {
				return "", fmt.Errorf("app %s has no running services", app)
			}
			return a.Services[0].Name, nil
		}
	}
	return "", &boxErr{Code: "app_not_found", Message: fmt.Sprintf("no app %s on box %s", app, b.Name)}
}

func (a *restBoxAPI) exec(ctx context.Context, p boxExecParams, stdout, stderr io.Writer) (int, error) {
	body := apigen.BoxExecRequest{Command: p.Command}
	if p.App != "" {
		body.App = &p.App
	}
	if p.TimeoutSec > 0 {
		body.TimeoutSec = &p.TimeoutSec
	}
	rc, err := a.c.postStream(ctx, boxPath(p.Box, "/exec"), body)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			return 0, &boxErr{Code: ae.Code, Message: ae.Message}
		}
		return 0, err
	}
	defer rc.Close()
	return decodeExecStream(rc, stdout, stderr)
}

// decodeExecStream 逐行解 NDJSON：`{"s":"o"|"e","d":…}` 写到对应输出，`{"exit":n}` 结束。
func decodeExecStream(r io.Reader, stdout, stderr io.Writer) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var line struct {
			S    string `json:"s"`
			D    string `json:"d"`
			Exit *int   `json:"exit"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return 0, fmt.Errorf("bad exec stream line: %w", err)
		}
		if line.Exit != nil {
			return *line.Exit, nil
		}
		switch line.S {
		case "o":
			_, _ = io.WriteString(stdout, line.D)
		case "e":
			_, _ = io.WriteString(stderr, line.D)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("exec stream ended without an exit code")
}

// boxAbsPath 把 box 上的相对路径换成 orb 文件接口认的 `~/…`。
func boxAbsPath(app, rel string) string {
	if app != "" {
		return path.Join("~/apps", app, "data", rel)
	}
	return path.Join("~", rel)
}

func (a *restBoxAPI) copy(ctx context.Context, p boxCopyParams) (int64, error) {
	var b apigen.Box
	if err := a.c.do(ctx, http.MethodGet, boxPath(p.Box, ""), nil, &b); err != nil {
		return 0, a.wrap(err)
	}
	q := url.Values{"path": {boxAbsPath(p.App, p.BoxPath)}}
	fsPath := "/api/v1/orbs/" + escapePathParam(b.OrbID) + "/fs/file"
	if p.ToBox {
		f, err := os.Open(p.LocalPath)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return 0, err
		}
		q.Set("overwrite", "true")
		if err := a.c.do(ctx, http.MethodPut, fsPath+"?"+q.Encode(), f, nil); err != nil {
			return 0, a.wrap(err)
		}
		return st.Size(), nil
	}
	rc, err := a.c.getRaw(ctx, fsPath+"?"+q.Encode())
	if err != nil {
		return 0, a.wrap(err)
	}
	defer rc.Close()
	f, err := os.Create(p.LocalPath)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func (a *restBoxAPI) wrap(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return &boxErr{Code: ae.Code, Message: ae.Message}
	}
	return err
}
