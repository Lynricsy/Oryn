// Package research 是全局助手的后台研究（M26）：公网话题跟进、内部数据关联与 MCP 检查。
//
// 研究服务自带节拍，驱动协调宿主里两个隐藏的研究员对话：公网研究员（只有搜索与读公网网址）与私有研究员（只有只读
// 宿主工具与白名单里的 MCP 工具）。研究结果经 Host.PostFinding 进主线收件，由主线助手决定说或沉默。本包不 import
// internal/coord：宿主能力经 Host 接口注入。
package research

import (
	"context"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 研究员角色。
const (
	RoleWeb     = "web"
	RolePrivate = "private"
)

// 管线。
const (
	PipelineWeb      = "web"
	PipelineMine     = "mine"
	PipelineInternal = "internal"
	PipelineMCP      = "mcp"
)

// 启动变体（与 apigen.LinkCtlAgentStartParamsVariant 同值）。
const (
	VariantResearchWeb     = "research_web"
	VariantResearchPrivate = "research_private"
	VariantMainRestricted  = "main_restricted"
)

// 引擎。
const (
	tickEvery       = time.Minute
	researcherRetry = 5 * time.Second
	runTimeout      = 15 * time.Minute
	runsKeep        = 30 * 24 * time.Hour
	// RunTagPrefix 是研究运行的投递标签前缀（运行 id 即标签）。
	RunTagPrefix = "rsr_"
)

// 话题（长度均按 Unicode 字符计）。
const (
	titleMax        = 80
	questionMax     = 600
	baselineMax     = 2000
	summaryMax      = 2000
	reportMax       = 4000
	sourcesMax      = 10
	sourceURLMax    = 500
	sourceTitleMax  = 200
	maxActiveTopics = 50
)

// 公网话题跟进。
const (
	webFirstDelay   = 24 * time.Hour
	webBaseInterval = 24 * time.Hour
	webMaxInterval  = 7 * 24 * time.Hour
	questionTTL     = 30 * 24 * time.Hour
)

// 自动挖掘。
const (
	mineEvery        = 5 * time.Minute
	mineIdleAfter    = 30 * time.Minute
	mineBatch        = 5
	mineMaxPerThread = 3
	// ExcerptMax / ExcerptEach 是挖掘摘录的总长与单条上限（字符数；摘录由 Host.Excerpt 生成）。
	ExcerptMax  = 12000
	ExcerptEach = 1500
)

// 内部关联与 MCP 检查。
const (
	internalEvery       = 2 * time.Hour
	internalMaxFindings = 3
	mcpEvery            = 60 * time.Minute
	mcpSince            = 24 * time.Hour
	mcpMaxFindings      = 5
)

// 设置。
const (
	prefsMax          = 1500
	defaultDailyUSD   = 20.0
	dailyUSDMin       = 0.1
	dailyUSDMax       = 1000.0
	mcpToolsPerServer = 100
	mcpToolNameMax    = 200
)

// Host 是研究服务对协调层的依赖（*coord.Service 实现）。
type Host interface {
	// ResearcherCold 报告角色 role 的研究员 agent 是否已经停了（对话不存在也算）：为真时下一次投递启动新的一代。
	ResearcherCold(ctx context.Context, role string) bool
	// DeliverResearch 把一次研究运行的提示词投递给研究员（标签 = runID）；必要时先建研究员对话。
	DeliverResearch(ctx context.Context, role, runID, prompt string) error
	// PrepareMCP 在 MCP 运行投递前启动研究员并等 servers 在它的 omp 里连上且有工具；时限内没就绪时返回错误。
	PrepareMCP(ctx context.Context, role string, servers []string) error
	// StopResearcher 中止研究员的回合并在静止后停掉它的 agent。
	StopResearcher(ctx context.Context, role string)
	// LastReply 返回线程最后一条助手文本。
	LastReply(ctx context.Context, threadID string) (string, error)
	// ResearcherThreads 返回已存在的研究员对话 id。
	ResearcherThreads(ctx context.Context) ([]string, error)
	// Excerpt 返回助手对话 afterSeq 之后的摘录（主人消息与每个回合最后的助手文本）。
	Excerpt(ctx context.Context, threadID string, afterSeq int64) (Excerpt, error)
	// PostFinding 把一条发现写进主线收件。
	PostFinding(ctx context.Context, f Finding)
	// RefreshPolicy 以当前设置刷新协调宿主的出站策略（研究可用的 MCP 工具变化时）。
	RefreshPolicy(ctx context.Context) error
	// SetResearcherModel 改研究员用的模型（"provider/id"；空 = 协调对话的缺省模型）。
	SetResearcherModel(ctx context.Context, model string) error
}

// Notifier 发系统通知（*notify.Service 实现）。
type Notifier interface {
	PostSystem(ctx context.Context, kind, title, body, link string) error
}

// Excerpt 是一段对话摘录。
type Excerpt struct {
	Text          string
	LastSeq       int64
	OwnerMessages int
}

// Finding 是一条研究发现（以 JSON 写进主线收件的 body）。
type Finding struct {
	RunID    string                  `json:"runId"`
	Pipeline string                  `json:"pipeline"`
	TopicID  string                  `json:"topicId,omitempty"`
	Kind     string                  `json:"kind,omitempty"`
	Title    string                  `json:"title"`
	Question string                  `json:"question,omitempty"`
	Baseline string                  `json:"baseline,omitempty"`
	Summary  string                  `json:"summary"`
	Sources  []apigen.ResearchSource `json:"sources,omitempty"`
	// ThreadID 是来源对话或相关线程；Threads 是内部关联涉及的线程。
	ThreadID string   `json:"threadId,omitempty"`
	Threads  []string `json:"threads,omitempty"`
	// Restricted 为真表示发现来自外部内容（网页、邮件、日历）：只投给受限的主线 agent。
	Restricted bool `json:"-"`
}

// TopicInput 是新建话题的参数。Kind 空 = question；Origin 空 = manual。
type TopicInput struct {
	Kind, Title, Question, Baseline, Origin, SourceThreadID string
}

// 错误码。
const (
	CodeInvalid  = "invalid_request"
	CodeNotFound = "not_found"
)

// Error 是研究服务的业务错误。
type Error struct {
	Code, Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errInvalid(msg string) *Error { return &Error{Code: CodeInvalid, Message: msg} }
