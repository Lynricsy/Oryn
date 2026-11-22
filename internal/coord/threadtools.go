package coord

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 第三层的线程工具（M19 步骤 2.2，只给全局助手）：中止当前一轮与切换模型（oryn 技能的原生操作），直接执行并写审计。
// 其余线程操作（改名、fork、唤醒、删除…）是 oryn 的 REST 操作。

// 工具名。
const (
	ToolAbortThread    = "abort_thread"
	ToolSetThreadModel = "set_thread_model"
)

// threadToolDefs 是全局助手线程操作的定义（oryn 技能原生操作）。
func threadToolDefs() []apigen.OmpFrame {
	id := str("Thread id (T-…).")
	return []apigen.OmpFrame{
		tool(ToolAbortThread, "Stop the current turn of a work thread.", obj{"threadId": id}, "threadId"),
		tool(ToolSetThreadModel, "Change the model of a work thread; it applies from its next turn.", obj{
			"threadId":      id,
			"model":         str("Model as provider/id."),
			"thinkingLevel": str("Thinking level."),
		}, "threadId", "model"),
	}
}

// Boxes 是 box 线程的目标解析（*boxes.Service 实现）：create_thread 的 box 参数经它找到 box 的 orb 与 app 目录。
type Boxes interface {
	ThreadTarget(ctx context.Context, boxRef, app string) (orbID, cwd string, err error)
}

// SetBoxes 设置 box 入口；须在服务对外可用之前调用。未设置时 create_thread 的 box 参数回 coordinator_unavailable。
func (s *Service) SetBoxes(b Boxes) { s.boxes = b }

func (s *Service) boxesOr() (Boxes, error) {
	if s.boxes == nil {
		return nil, errf(CodeCoordinatorUnavailable, "boxes are not available")
	}
	return s.boxes, nil
}

// assistantCreateArgs 是全局助手 create_thread 的额外参数（M19）。
type assistantCreateArgs struct {
	Space    string   `json:"space"`
	Provider string   `json:"provider"`
	OrbID    string   `json:"orbId"`
	Runner   string   `json:"runner"`
	Dir      string   `json:"dir"`
	Worktree string   `json:"worktree"`
	ShareDir bool     `json:"shareDir"`
	Role     string   `json:"role"`
	Labels   []string `json:"labels"`
	// Box / App（M23）：线程开在这台 box 上（App 非空时在该 app 的线上目录里），与 orbId / runner / provider 互斥。
	Box string `json:"box"`
	App string `json:"app"`
}

// assistantCreateProps 是 assistantCreateArgs 的参数定义（并入助手的 create_thread）。
func assistantCreateProps() obj {
	return obj{
		"space":    str("Space name: the thread joins it (its project is the default)."),
		"provider": str("Orb provider name (default: the project's)."),
		"orbId":    str("Run in this existing orb (orb_…) instead of a new one."),
		"runner":   str("Run on this runner (name, see the oryn operation listRunners) instead of an orb; needs dir."),
		"dir":      str("runner: working directory, inside one the runner declares."),
		"worktree": str("runner: create a git worktree with this name next to dir and work there."),
		"shareDir": boolean("runner: allow sharing dir with other threads."),
		"role":     str("Start with this model role."),
		"labels":   obj{"type": "array", "items": obj{"type": "string"}, "description": "Labels for the thread."},
		"box":      str("Run on this box (name, see the oryn operation listBoxes) instead of an orb: the thread works directly on the live machine and its apps' data. To deploy, start a normal thread (no box) that runs `oryn boxes deploy` in its repository."),
		"app":      str("box: work in this app's live directory instead of the machine's home."),
	}
}

// applyAssistantCreate 把助手的额外参数映射到 threads.CreateParams；互斥关系由 threads.Create 校验。
func (s *Service) applyAssistantCreate(ctx context.Context, a assistantCreateArgs, p *threads.CreateParams, projectName *string) error {
	if a.Space != "" {
		sp, err := s.spaceByName(ctx, a.Space)
		if err != nil {
			return err
		}
		p.SpaceID = sp.ID
		if p.ProjectID == "" && sp.ProjectID != nil {
			p.ProjectID = *sp.ProjectID
			if names, err := s.projectNames(ctx); err == nil {
				*projectName = names[p.ProjectID]
			}
		}
	}
	p.Provider, p.OrbID, p.Role, p.Labels = a.Provider, a.OrbID, a.Role, a.Labels
	if a.Box != "" || a.App != "" {
		if a.Box == "" {
			return errf(CodeInvalid, "app needs box")
		}
		if a.OrbID != "" || a.Runner != "" || a.Provider != "" {
			return errf(CodeInvalid, "box cannot be combined with orbId, runner or provider")
		}
		b, err := s.boxesOr()
		if err != nil {
			return err
		}
		orbID, cwd, err := b.ThreadTarget(ctx, a.Box, a.App)
		if err != nil {
			return err
		}
		p.OrbID, p.Cwd = orbID, cwd
	}
	if a.Runner == "" {
		if a.Dir != "" || a.Worktree != "" || a.ShareDir {
			return errf(CodeInvalid, "dir, worktree and shareDir need runner")
		}
		return nil
	}
	if a.Dir == "" {
		return errf(CodeInvalid, "a runner thread needs dir")
	}
	m, err := s.machinesOr()
	if err != nil {
		return err
	}
	list, err := m.ListRunners(ctx)
	if err != nil {
		return err
	}
	for _, r := range list {
		if r.Name == a.Runner {
			p.RunnerID, p.Dir, p.Worktree, p.ShareDir = r.ID, a.Dir, a.Worktree, a.ShareDir
			return nil
		}
	}
	return errf(CodeNotFound, "runner %q not found", a.Runner)
}

// spaceByName 按名字取 Space；不存在 → space_not_found。
func (s *Service) spaceByName(ctx context.Context, name string) (db.Space, error) {
	sp, err := s.st.Q.GetSpaceByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Space{}, errf(CodeSpaceNotFound, "space %q not found", name)
	}
	return sp, err
}

// allowMutate 按 mutateRules 给全局助手的写类工具与确认请求计数（每个对话合计）。
func (s *Service) allowMutate(c toolCtx) error {
	if !s.lim.allow(c.ThreadID+"|mutate", s.cfg.Now(), mutateRules) {
		return errf(CodeRateLimited, "change limit reached (30 per 10 minutes, 200 per 24 hours per conversation)")
	}
	return nil
}

// threadOpArgs 是 abort_thread / set_thread_model 的参数。
type threadOpArgs struct {
	ThreadID string `json:"threadId"`
	// set_thread_model
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
}

// threadTool 执行线程操作；无论成败都写审计（动作名 = 工具名，目标 = 该线程）。
func (s *Service) threadTool(ctx context.Context, c toolCtx) (any, error) {
	var a threadOpArgs
	err := decodeArgs(c.Args, &a)
	summary := ""
	defer func() {
		s.audit(ctx, c.Kind, c.ThreadID, c.OwnerTurn, c.Name, a.ThreadID, "", summary, err)
	}()
	if err != nil {
		return nil, err
	}
	var t db.Thread
	if t, err = s.resolveActTarget(ctx, c, a.ThreadID, false); err != nil {
		return nil, err
	}
	// 参数先校验，再计数
	var model *apigen.ModelRef
	if c.Name == ToolSetThreadModel {
		m, ok := parseModelRef(a.Model)
		if !ok {
			err = errf(CodeInvalid, "model must be provider/id")
			return nil, err
		}
		model = m
		summary = a.Model
		if a.ThinkingLevel != "" {
			summary += " · " + a.ThinkingLevel
		}
	}
	if err = s.allowMutate(c); err != nil {
		return nil, err
	}
	switch c.Name {
	case ToolAbortThread:
		err = s.th.Abort(ctx, t.ID)
	case ToolSetThreadModel:
		err = s.th.SetModel(ctx, t.ID, *model, a.ThinkingLevel)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "threadIds": []string{t.ID}}, nil
}
