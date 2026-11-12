package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// AuthDeps 是 H2 领域（认证、设备、项目、provider、密钥、agent 文本配置）的依赖。
type AuthDeps struct {
	// Service 持有 WebAuthn 依赖方与进行中的仪式。
	Service *auth.Service
	// Tickets 与 /api/v1/ws 处理器共享同一实例。
	Tickets *auth.Tickets
	Vault   *secrets.Vault
	Home    *agentcfg.Home
}

// maxJSONBody 是 JSON 请求体上限。
const maxJSONBody = 1 << 20

// decodeJSON 解析 JSON 请求体；失败时已写出 400。
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON: "+err.Error())
		return false
	}
	return true
}

// statusError 是处理器内部（如事务回调中）返回的带状态错误。
type statusError struct {
	status    int
	code, msg string
}

func (e *statusError) Error() string { return e.msg }

func errInvalid(msg string) error  { return &statusError{http.StatusBadRequest, "invalid_request", msg} }
func errNotFound(msg string) error { return &statusError{http.StatusNotFound, "not_found", msg} }
func errConflict(msg string) error { return &statusError{http.StatusConflict, "conflict", msg} }

// writeErr 把领域错误映射为 {code, message}。
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *auth.Error
	var se *statusError
	switch {
	case errors.As(err, &se):
		WriteError(w, se.status, se.code, se.msg)
	case errors.As(err, &ae):
		WriteError(w, ae.Status, ae.Code, ae.Message)
	case errors.Is(err, fs.ErrNotExist):
		WriteError(w, http.StatusNotFound, "not_found", "不存在")
	case errors.Is(err, agentcfg.ErrInvalidPath), errors.Is(err, agentcfg.ErrExcludedPath), errors.Is(err, agentcfg.ErrInvalidContent):
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, agentcfg.ErrTooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request", err.Error())
	default:
		slog.Error("处理请求失败", "method", r.Method, "path", r.URL.Path, "err", err)
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// principal 返回中间件写入的主体；缺失时写出 401。
func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "未登录或凭据无效")
	}
	return p, ok
}

// isUniqueViolation 判断 SQLite 唯一约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// isForeignKeyViolation 判断 SQLite 外键约束冲突。
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// readBody 读取至多 limit 字节的请求体，超出返回 ErrTooLarge。
func readBody(r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w（%d MiB）", agentcfg.ErrTooLarge, limit>>20)
	}
	return b, nil
}

// optStr 把空串转为 nil。
func optStr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
