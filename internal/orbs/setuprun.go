package orbs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// SetupLogTail 是每次 setup / resume 运行保留的日志尾部上限（orb_setup_runs.log_tail）。
const SetupLogTail = 256 << 10

// CodeNoProject 是 orb 不属于任何项目时 project.scripts.* 的 ctl 错误码。
const CodeNoProject = "no_project"

// CodeTooLarge 是 project.scripts.set 的脚本超过 MaxProjectScriptBytes 时的 ctl 错误码。
const CodeTooLarge = "too_large"

// MaxProjectScriptBytes 是 orb 经 project.scripts.set 写入的单个脚本上限。
const MaxProjectScriptBytes = 256 << 10

// MaxSetupRunsPerOrb 是每个 orb 保留的 setup / resume 运行记录条数（更早的在写入新记录时删除）。
const MaxSetupRunsPerOrb = 50

// setupLogs 是一个 orb 尚未归属到运行记录的 setup.log 输出（按阶段分开，各自只保留尾部）。
type setupLogs struct {
	setup, resume []byte
}

// appendTail 追加 data 并把缓冲控制在 2×SetupLogTail 以内（摊还 O(1)；取出时再截到 SetupLogTail）。
func appendTail(buf []byte, data string) []byte {
	buf = append(buf, data...)
	if len(buf) > 2*SetupLogTail {
		buf = append([]byte(nil), buf[len(buf)-SetupLogTail:]...)
	}
	return buf
}

// logTail 返回 buf 的末尾至多 SetupLogTail 字节，并跳过截断处残缺的 UTF-8 字符。
func logTail(buf []byte) []byte {
	if len(buf) <= SetupLogTail {
		return buf
	}
	buf = buf[len(buf)-SetupLogTail:]
	for i := 0; i < utf8.UTFMax && len(buf) > 0 && !utf8.RuneStart(buf[0]); i++ {
		buf = buf[1:]
	}
	return buf
}

// setupLogPhase 把 setup.log 的 phase 归到 setup 或 resume 缓冲（omp_home / git / clone / setup → setup）。
func setupLogPhase(p apigen.LinkCtlSetupLogParamsPhase) (apigen.SetupPhase, bool) {
	switch p {
	case apigen.OmpHome, apigen.Git, apigen.Clone, apigen.Setup:
		return apigen.SetupPhaseSetup, true
	case apigen.Resume:
		return apigen.SetupPhaseResume, true
	}
	return "", false
}

func (s *Service) onSetupLog(orbID string, params json.RawMessage) {
	var p apigen.LinkCtlSetupLogParams
	if err := json.Unmarshal(params, &p); err != nil {
		slog.Warn("setup.log 参数无效", "orb", orbID, "err", err)
		return
	}
	phase, ok := setupLogPhase(p.Phase)
	if !ok {
		slog.Warn("setup.log 阶段未知", "orb", orbID, "phase", p.Phase)
		return
	}
	s.mu.Lock()
	b := s.setupLogs[orbID]
	if b == nil {
		b = &setupLogs{}
		s.setupLogs[orbID] = b
	}
	if phase == apigen.SetupPhaseSetup {
		b.setup = appendTail(b.setup, p.Data)
	} else {
		b.resume = appendTail(b.resume, p.Data)
	}
	s.mu.Unlock()
}

// takeSetupLog 取出并清空 orb 某阶段的日志缓冲。
func (s *Service) takeSetupLog(orbID string, phase apigen.SetupPhase) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.setupLogs[orbID]
	if b == nil {
		return []byte{}
	}
	var out []byte
	if phase == apigen.SetupPhaseSetup {
		out, b.setup = b.setup, nil
	} else {
		out, b.resume = b.resume, nil
	}
	if b.setup == nil && b.resume == nil {
		delete(s.setupLogs, orbID)
	}
	out = logTail(out)
	if out == nil {
		out = []byte{}
	}
	return out
}

// onSetupResult 把一次 setup / resume 运行写入 orb_setup_runs（log_tail 取自该阶段缓冲），并推送 orb 更新。
func (s *Service) onSetupResult(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlSetupResultParams
	if err := json.Unmarshal(params, &p); err != nil {
		slog.Warn("setup.result 参数无效", "orb", orbID, "err", err)
		return
	}
	if (p.Phase != apigen.SetupPhaseSetup && p.Phase != apigen.SetupPhaseResume) ||
		(p.Trigger != apigen.SetupTriggerAuto && p.Trigger != apigen.SetupTriggerManual) {
		slog.Warn("setup.result 阶段或触发方式未知", "orb", orbID, "phase", p.Phase, "trigger", p.Trigger)
		return
	}
	row := db.InsertOrbSetupRunParams{
		ID: ids.New("osr"), OrbID: orbID, Phase: string(p.Phase), Trigger: string(p.Trigger),
		StartedAt: p.StartedAt, FinishedAt: s.nowMs(), Ok: b2i(p.Ok), SetupRan: b2i(p.SetupRan), Error: p.Error,
		DurationMs: p.DurationMs, HasRepoScript: b2i(p.HasRepoScript), HasProjectScript: b2i(p.HasProjectScript),
		LogTail: s.takeSetupLog(orbID, p.Phase),
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.InsertOrbSetupRun(ctx, row); err != nil {
			return err
		}
		_, err := q.PruneOrbSetupRuns(ctx, db.PruneOrbSetupRunsParams{OrbID: orbID, Keep: MaxSetupRunsPerOrb})
		return err
	})
	if err != nil {
		slog.Warn("写入 setup 运行记录失败", "orb", orbID, "err", err)
		return
	}
	slog.Info("orb setup 运行结束", "orb", orbID, "phase", p.Phase, "trigger", p.Trigger, "ok", p.Ok,
		"setupRan", p.SetupRan, "durationMs", p.DurationMs, "logBytes", len(row.LogTail))
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return
	}
	s.emitInfo(ctx, EventSetup, orb)
}

func (s *Service) dropSetupLogs(orbID string) {
	s.mu.Lock()
	delete(s.setupLogs, orbID)
	s.mu.Unlock()
}

// scriptSHA 返回脚本内容的 SHA-256 十六进制（未设置时为空串）。
func scriptSHA(v *string) string {
	if v == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(*v))
	return hex.EncodeToString(sum[:])
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ---- project.scripts.get / set：只作用于发起 link 的 orb 所属项目 ----

func errNoProject() error {
	return &link.CtlError{Code: CodeNoProject, Message: "orb does not belong to a project"}
}

// orbProject 读取 orb 所属项目；无项目（或项目已删除）时返回 no_project。
func orbProject(ctx context.Context, q *db.Queries, orbID string) (db.Project, error) {
	orb, err := q.GetOrb(ctx, orbID)
	if err != nil {
		return db.Project{}, err
	}
	if orb.ProjectID == nil || *orb.ProjectID == "" {
		return db.Project{}, errNoProject()
	}
	prj, err := q.GetProject(ctx, *orb.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Project{}, errNoProject()
	}
	return prj, err
}

func projectScripts(p db.Project) apigen.LinkCtlProjectScripts {
	return apigen.LinkCtlProjectScripts{ProjectID: p.ID, ProjectName: p.Name, Setup: nonEmpty(p.SetupScript), Resume: nonEmpty(p.ResumeScript)}
}

func (s *Service) onProjectScriptsGet(ctx context.Context, orbID string, _ json.RawMessage) (any, error) {
	prj, err := orbProject(ctx, s.st.Q, orbID)
	if err != nil {
		return nil, err
	}
	return projectScripts(prj), nil
}

func (s *Service) onProjectScriptsSet(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlProjectScriptsSetParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &link.CtlError{Code: "invalid_params", Message: err.Error()}
		}
	}
	for _, v := range []*string{p.Setup, p.Resume} {
		if v != nil && len(*v) > MaxProjectScriptBytes {
			return nil, &link.CtlError{Code: CodeTooLarge, Message: fmt.Sprintf("script exceeds %d bytes", MaxProjectScriptBytes)}
		}
	}
	var out db.Project
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		prj, err := orbProject(ctx, q, orbID)
		if err != nil {
			return err
		}
		orb, err := q.GetOrb(ctx, orbID)
		if err != nil {
			return err
		}
		// 省略 = 不变；空串 = 清除（存 NULL）
		if p.Setup != nil {
			prj.SetupScript = nonEmpty(p.Setup)
		}
		if p.Resume != nil {
			prj.ResumeScript = nonEmpty(p.Resume)
		}
		prj.UpdatedAt = s.nowMs()
		if _, err := q.UpdateProject(ctx, db.UpdateProjectParams{
			Name: prj.Name, RepoUrl: prj.RepoUrl, BaseBranch: prj.BaseBranch, DefaultProvider: prj.DefaultProvider,
			DefaultOrbType: prj.DefaultOrbType, SetupScript: prj.SetupScript, ResumeScript: prj.ResumeScript,
			ShallowClone: prj.ShallowClone, Color: prj.Color, ArchivedAt: prj.ArchivedAt, UpdatedAt: prj.UpdatedAt, ID: prj.ID,
		}); err != nil {
			return err
		}
		// orb 内写入的脚本会在该项目以后的每个 orb 里执行：留下可审计的记录（脚本内容的哈希）。
		detail, _ := json.Marshal(map[string]any{
			"event": "project_scripts_set", "projectId": prj.ID,
			"setupSha256": scriptSHA(prj.SetupScript), "resumeSha256": scriptSHA(prj.ResumeScript),
		})
		if _, err := q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{
			OrbID: orbID, Ts: prj.UpdatedAt, FromState: orb.State, ToState: orb.State, Detail: string(detail),
		}); err != nil {
			return err
		}
		out = prj
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.Info("orb 更新了项目脚本", "orb", orbID, "project", out.ID, "setup", p.Setup != nil, "resume", p.Resume != nil)
	return projectScripts(out), nil
}
