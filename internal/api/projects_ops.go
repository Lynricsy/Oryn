package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/coord"
)

// ProjectOps 把项目的增改删以 coord.Projects 的形态提供给全局助手（M19 第三层）：与 REST 同一套校验与副作用，
// 带状态的 api 错误转成同码的 *coord.Error。
type ProjectOps struct {
	S *Server
}

var _ coord.Projects = ProjectOps{}

// Create 新建项目。
func (o ProjectOps) Create(ctx context.Context, in apigen.ProjectCreate) (apigen.Project, error) {
	row, err := o.S.createProject(ctx, in)
	if err != nil {
		return apigen.Project{}, coordErr(err)
	}
	return o.S.projectView(ctx, row)
}

// Update 修改项目；p.GithubGates 非空时按 REST 的 githubGates 处理。
func (o ProjectOps) Update(ctx context.Context, id string, p apigen.ProjectPatch) (apigen.Project, error) {
	var gates json.RawMessage
	if p.GithubGates != nil {
		b, err := json.Marshal(p.GithubGates)
		if err != nil {
			return apigen.Project{}, err
		}
		gates = b
	}
	row, err := o.S.updateProject(ctx, id, p, gates)
	if err != nil {
		return apigen.Project{}, coordErr(err)
	}
	return o.S.projectView(ctx, row)
}

// Delete 删除项目。
func (o ProjectOps) Delete(ctx context.Context, id string) error {
	return coordErr(o.S.deleteProject(ctx, id))
}

// coordErr 把 statusError 转成同码的 *coord.Error；其余原样返回。
func coordErr(err error) error {
	var se *statusError
	if errors.As(err, &se) {
		return &coord.Error{Code: se.code, Message: se.msg}
	}
	return err
}
