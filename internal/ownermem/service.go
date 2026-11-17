package ownermem

// service.go：主人记忆服务的骨架。连接、key 与 M16 共用（memory.Service），数据写在 bank `oryn-owner`。
// 任何查询出错都按「未启用」处理（计划「全局约束」）。

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// BankID 是主人记忆的 bank，只由 orynd 访问（M16 网关对 orb 与 Runner 一律改写为 `oryn`）。
const BankID = "oryn-owner"

// Config 是主人记忆服务的依赖。
type Config struct {
	Q      *db.Queries
	Box    *secrets.Box
	Memory *memory.Service
	// Redact 对写入与工具输出的文本做密钥脱敏（share.(*Service).RedactText）；nil 时不脱敏。
	Redact func(ctx context.Context, text string) (string, int)
	// HTTP 访问判断模型接口；nil 时用带超时的默认客户端。
	HTTP *http.Client
	Now  func() time.Time
}

// Service 是全局助手的主人记忆。
type Service struct {
	cfg Config
	// bankMu 串行化 bank 配置的检查与写入（配置哈希一致时只读一次库）。
	bankMu sync.Mutex
}

// New 构造服务。
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{cfg: cfg}
}

// Error 是带 HTTP 状态与码的领域错误（与 memory.Error 同形，API 层按它写响应）。
type Error = memory.Error

func errInvalid(msg string) error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: msg}
}

func errNotFound(msg string) error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

// errUnavailable 是助手记忆没有运行（M16 记忆未启用或未配置、助手记忆关闭）时的错误。
var errUnavailable = &Error{Status: http.StatusConflict, Code: "owner_memory_disabled", Message: "助手记忆未开启"}

// loadSettings 读设置；还没有行时返回零值（未开启）。
func (s *Service) loadSettings(ctx context.Context) (db.OwnerMemorySetting, error) {
	st, err := s.cfg.Q.GetOwnerMemorySettings(ctx)
	if errors.Is(err, errNoRows) {
		return db.OwnerMemorySetting{OwnerNames: "[]"}, nil
	}
	return st, err
}

// Available 是 M16 记忆已启用且已配置（助手记忆运行的前提）。
func (s *Service) Available(ctx context.Context) bool {
	return s.cfg.Memory != nil && s.cfg.Memory.Enabled(ctx)
}

// Enabled 是助手记忆正在运行：M16 记忆可用且助手记忆开关开。任何错误按未启用。
func (s *Service) Enabled(ctx context.Context) bool {
	if !s.Available(ctx) {
		return false
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		slog.Warn("读取助手记忆设置失败，按未启用处理", "err", err)
		return false
	}
	return st.Enabled != 0
}

// bank 返回 `oryn-owner` 的句柄，并保证 bank 存在、配置（mission 等）与当前设置一致。未运行时返回 errUnavailable。
func (s *Service) bank(ctx context.Context) (*memory.Bank, error) {
	if !s.Enabled(ctx) {
		return nil, errUnavailable
	}
	b, err := s.cfg.Memory.Bank(ctx, BankID)
	if err != nil {
		return nil, err
	}
	s.bankMu.Lock()
	defer s.bankMu.Unlock()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	cfg, hash := bankConfig(ownerNames(st))
	if st.ConfigHash != nil && *st.ConfigHash == hash {
		return b, nil
	}
	if err := b.Ensure(ctx, cfg); err != nil {
		return nil, upstream("配置 bank "+BankID, err)
	}
	if err := s.ensureSeed(ctx, b); err != nil {
		return nil, err
	}
	if err := s.cfg.Q.SetOwnerMemoryConfigHash(ctx, &hash); err != nil {
		return nil, err
	}
	return b, nil
}

// ensureSeed 创建固定种子主题 `owner-profile`（R8）：本地没有记录时在 bank 里建 mental model（已存在按成功），再记入本地。
func (s *Service) ensureSeed(ctx context.Context, b *memory.Bank) error {
	if _, err := s.cfg.Q.GetOwnerMemoryTopic(ctx, ProfileTopicID); err == nil {
		return nil
	} else if !errors.Is(err, errNoRows) {
		return err
	}
	err := b.Call(ctx, http.MethodPost, "/mental-models", topicSpec(ProfileTopicID, profileName, profileScope), nil)
	if err != nil && hsStatus(err) != http.StatusConflict {
		return upstream("创建主题 "+ProfileTopicID, err)
	}
	return s.cfg.Q.UpsertOwnerMemoryTopic(ctx, db.UpsertOwnerMemoryTopicParams{
		ModelID: ProfileTopicID, Name: profileName, Scope: profileScope, Seed: 1, Status: "active",
		Now: s.cfg.Now().UnixMilli(),
	})
}

// hsStatus 是 Hindsight 应答的 HTTP 状态；不是应答错误时为 0。
func hsStatus(err error) int {
	var he *memory.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// upstream 把访问 Hindsight 的失败包装成 502；领域错误原样返回。
func upstream(what string, err error) error {
	var me *Error
	if errors.As(err, &me) {
		return err
	}
	return &Error{Status: http.StatusBadGateway, Code: "memory_upstream", Message: what + "失败：" + err.Error()}
}

// redact 按配置脱敏。
func (s *Service) redact(ctx context.Context) func(string) string {
	if s.cfg.Redact == nil {
		return func(t string) string { return t }
	}
	return func(t string) string {
		out, _ := s.cfg.Redact(ctx, t)
		return out
	}
}
