package boxes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// orbExecMaxSec 是 orb 内 exec 的上限（ctl 的调用方放弃等待不会结束控制面上的处理，命令跑到结束或超时）。
const orbExecMaxSec = 1800

// callArgs 是各 op 共用的参数（按 op 取用其中几项）。
type callArgs struct {
	Box        string `json:"box"`
	App        string `json:"app"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Type       string `json:"type"`
	On         string `json:"on"`
	Off        string `json:"off"`
	Tz         string `json:"tz"`
	Clear      bool   `json:"clear"`
	Public     *bool  `json:"public"`
	ReleaseID  string `json:"releaseId"`
	Value      string `json:"value"`
	Secret     *bool  `json:"secret"`
	Command    string `json:"command"`
	TimeoutSec *int   `json:"timeoutSec"`
	BoxPath    string `json:"boxPath"`
	OrbPath    string `json:"orbPath"`
	ToBox      bool   `json:"toBox"`
	Service    string `json:"service"`
	Tail       int    `json:"tail"`
}

// OnCall 处理 orb 内 `oryn boxes …` 经 orbd 转来的 box.call（ctl 请求）。调用方须是这台 orb 上未归档的工作线程，且 orb
// 不是全自动的；发起者记为 {agent, 线程}。结果一律是对象，包进 LinkCtlBoxCallResult。
func (s *Service) OnCall(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlBoxCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: CodeInvalid, Message: err.Error()}
	}
	if err := s.checkCaller(ctx, orbID, p.ThreadID); err != nil {
		return nil, ctlErr(err)
	}
	raw, err := json.Marshal(p.Params)
	if err != nil {
		return nil, ctlErr(err)
	}
	res, err := s.dispatch(ctx, orbID, p.Op, raw, Actor{Kind: ActorAgent, ThreadID: p.ThreadID})
	if err != nil {
		return nil, ctlErr(err)
	}
	m, err := toObject(res)
	if err != nil {
		return nil, ctlErr(err)
	}
	return apigen.LinkCtlBoxCallResult{Result: m}, nil
}

// checkCaller 校验调用方线程：存在、是工作线程、未归档、属于发起 link 的 orb；全自动的 orb 不能用 box。
func (s *Service) checkCaller(ctx context.Context, orbID, threadID string) error {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return errf(CodeForbidden, "calling thread not found")
	}
	if err != nil {
		return err
	}
	if t.Kind != "work" || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != orbID {
		return errf(CodeForbidden, "calling thread does not belong to this orb")
	}
	o, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return err
	}
	if o.Automation {
		return errf(CodeForbidden, "automation threads cannot use boxes")
	}
	return nil
}

// dispatch 按 op 执行。
func (s *Service) dispatch(ctx context.Context, orbID, op string, raw []byte, by Actor) (any, error) {
	decode := func(v any) error {
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(v); err != nil {
			return errf(CodeInvalid, "invalid params for %s: %v", op, err)
		}
		return nil
	}
	var a callArgs
	switch op {
	case "deploy.prepare", "deploy.commit":
	default:
		if err := decode(&a); err != nil {
			return nil, err
		}
	}
	empty := map[string]any{}
	switch op {
	case "list":
		l, err := s.List(ctx)
		return apigen.BoxList{Boxes: l}, err
	case "show":
		return s.Get(ctx, a.Box)
	case "create":
		return s.Create(ctx, a.Name, a.Provider, a.Type, by)
	case "start":
		return s.Start(ctx, a.Box, by)
	case "stop":
		return s.Stop(ctx, a.Box, by)
	case "rename":
		return s.Rename(ctx, a.Box, a.Name, by)
	case "schedule":
		if a.Clear {
			return s.SetSchedule(ctx, a.Box, nil, by)
		}
		return s.SetSchedule(ctx, a.Box, &apigen.BoxSchedule{On: a.On, Off: a.Off, Tz: a.Tz}, by)
	case "deploy.prepare":
		var pp PrepareParams
		if err := decode(&pp); err != nil {
			return nil, err
		}
		return s.PrepareDeploy(ctx, orbID, pp, by)
	case "deploy.commit":
		var cp CommitParams
		if err := decode(&cp); err != nil {
			return nil, err
		}
		return s.CommitDeploy(ctx, orbID, cp)
	case "deploy.cancel":
		return empty, s.CancelDeploy(ctx, a.Box, a.App)
	case "visibility":
		if a.Public == nil {
			return nil, errf(CodeInvalid, "public is required")
		}
		return s.SetPublic(ctx, a.Box, a.App, *a.Public, by)
	case "undeploy":
		return empty, s.Undeploy(ctx, a.Box, a.App, false, by)
	case "restart":
		return s.Restart(ctx, a.Box, a.App, by)
	case "rollback":
		return s.Rollback(ctx, a.Box, a.App, a.ReleaseID, by)
	case "releases":
		r, err := s.Releases(ctx, a.Box, a.App)
		return apigen.BoxReleaseList{Releases: r}, err
	case "secrets.list":
		l, err := s.Secrets(ctx, a.Box, a.App)
		return apigen.BoxSecretList{Secrets: l}, err
	case "secrets.set":
		secret := a.Secret == nil || *a.Secret
		return empty, s.PutSecret(ctx, a.Box, a.App, a.Name, a.Value, secret)
	case "secrets.unset":
		return empty, s.DeleteSecret(ctx, a.Box, a.App, a.Name)
	case "exec":
		return s.callExec(ctx, a)
	case "cp":
		n, err := s.Copy(ctx, orbID, a.Box, a.App, a.BoxPath, a.OrbPath, a.ToBox)
		return map[string]any{"bytes": n}, err
	case "logs":
		text, err := s.Logs(ctx, a.Box, a.App, a.Service, a.Tail)
		return map[string]any{"text": text}, err
	}
	return nil, errf(CodeForbidden, "deleting a box needs the owner: use the Boxes page or `oryn boxes delete` on a signed-in machine")
}

// callExec 跑命令并等它结束，两路输出各截到 256 KiB。不能中途取消：ctl 调用方放弃等待不影响这里。
func (s *Service) callExec(ctx context.Context, a callArgs) (execResult, error) {
	if a.TimeoutSec != nil && *a.TimeoutSec > orbExecMaxSec {
		return execResult{}, errf(CodeInvalid, "timeoutSec is at most %d inside a thread's environment", orbExecMaxSec)
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := s.Exec(context.WithoutCancel(ctx), a.Box, apigen.BoxExecRequest{Command: a.Command, App: nonEmpty(a.App), TimeoutSec: a.TimeoutSec}, pw)
		_ = pw.CloseWithError(err)
		errc <- err
	}()
	res, cerr := collectExec(pr)
	_ = pr.Close()
	if err := <-errc; err != nil {
		return execResult{}, err
	}
	return res, cerr
}

// ctlErr 把错误转成 ctl 错误（*Error 保留码）。
func ctlErr(err error) error {
	var ce *link.CtlError
	if errors.As(err, &ce) {
		return ce
	}
	var be *Error
	if errors.As(err, &be) {
		return &link.CtlError{Code: be.Code, Message: be.Message}
	}
	slog.Warn("boxes: box.call 失败", "err", err)
	return &link.CtlError{Code: "internal", Message: err.Error()}
}

// toObject 把结果转成 JSON 对象（map）。
func toObject(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
