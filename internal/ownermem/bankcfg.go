package ownermem

// bankcfg.go：bank `oryn-owner` 的配置（R7）。抽取口径来自设计文档 §3.2 与 §7.1、§7.2 的真实负例：归属、完成状态、
// 场景、指代词。配置随主人的名字与别名变化，按哈希判断是否需要重写。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// retainMissionBody 是写入时的抽取口径（不含主人名字那一段）。
const retainMissionBody = `这个记忆库只记录"关于主人"的信息，服务对象是主人的全局助手。输入是主人与全局助手或编码 agent 的对话：每行以「主人：」「助手：」「agent：」或「自动消息：」开头；「助手（上一条）：」是主人这句话所回应的那条助手消息。

要记录：
- 主人是谁：身份、背景、所在组织、设备与环境、长期兴趣；
- 主人在做什么：正在推进的项目与目标、当前进度与状态、计划、尚未解决的问题；
- 主人的决定与取舍：选了什么、放弃了什么、为什么；
- 主人的偏好与习惯：沟通方式、工作方式、明确说过的喜好与禁忌；
- 主人想了解什么：最近反复问的主题。

粒度：到"项目 / 功能 / 决定"为止。主人在意的具体参数（型号、版本、规格、数量）可以保留。不要记录代码级细节：文件路径、函数名、行号、堆栈、命令输出、diff、具体实现步骤。

来源规则（必须遵守）：
- 以主人的话为事实依据。「主人：」开头的内容可以直接成为关于主人的事实。
- 助手 / agent 说的话只用于理解上下文。它提出的方案、建议、推测不能写成"主人决定 / 主人采用 / 主人是"，除非主人明确接受（例如"好""就这样""按这个做"）；主人接受时，要写清楚接受的是哪个方案。
- 主人拒绝的方案记为"主人拒绝了某方案及原因"，不能写成主人的计划。
- 「自动消息：」是系统通知（线程回报、定时运行、GitHub 事件），不是主人说的话。

写法：
- 统一称"主人"，不要用"用户"。
- 每条事实必须自成一体：指代词（"该任务""这个项目""它""这里"）一律换成具体名称；不知道指的是什么就不写这条。
- 归属：只有主人本人的属性（身份、所在地、时区、设备、偏好）才能写成"主人……"；属于某个项目、任务、服务、agent 的设置（例如某个定时任务的时间基准、某台服务器的配置）必须写明属于谁，例如"NotionAgent 论文任务的时间基准：新加坡时间"。
- 场景：主人在某个具体场景里的决定只在那个场景成立（例如"GitHub 上 agent 发评论不需要人工确认"），要写明场景，不能写成一般习惯。
- 时间敏感的状态（"打算""正在""已完成"）要带上发生的日期。
- 不记录账号、邮箱、密钥、令牌，以及凭据所在的文件位置。`

// reflectMission 是主题整理、主题刷新与 reflect 的口径。
const reflectMission = `你在为主人的全局助手整理"关于主人"的背景资料。只依据记忆库里的事实；助手或 agent 提出而主人未接受的方案不算主人的事实。新的状态覆盖旧的状态（"计划做 X"之后若有"X 已完成"，只写已完成并带日期）。用简体中文。
归属：只有主人本人的属性（身份、所在地、时区、设备、偏好）才能写成"主人……"；属于某个项目、任务、服务、agent 的设置必须写明它属于谁，不能写成主人的个人属性。
完成状态：只有出现明确完成的事实（"已通过""已发布""已完成"）才能写"已完成"。"准备""计划""打算""正在"类事实如果之后没有完成的事实，要写成"（截至 YYYY-MM-DD）准备中 / 后续未知"；一件事的某个阶段完成不代表后续阶段也完成。
场景：主人在某个具体场景里做的决定只在那个场景成立，写进画像或其他主题时必须带上场景，不能泛化成一般习惯或全局偏好。`

// topicFormat 是每个主题 source_query 末尾的格式要求（设计文档 §4.1）。
const topicFormat = `输出格式要求：高密度条目，不写叙述句；按子主题分段，每段一行以"- 子主题："开头，条目之间用分号分隔；日期写在条目里（YYYY-MM-DD）；只写当前状态，过时的状态删掉或标"（已完成 / 已放弃，日期）"；不写"主人曾与助手讨论了……"这类句子；没有记录的子主题直接省略，不写"未记录"；不确定的写"（待确认）"。`

// retainMission 拼出带主人名字与别名的抽取口径。
func retainMission(names []string) string {
	if len(names) == 0 {
		return retainMissionBody
	}
	return retainMissionBody + "\n\n主人的名字与别名：" + strings.Join(names, "、") +
		`。对话中出现这些名字指的都是主人本人，一律写成"主人"，不要当成另一个人。`
}

// bankConfig 是 bank 应有的配置及其哈希：Hindsight 自带重排关闭（orynd 用判断模型重排，计划 D8）。
func bankConfig(names []string) (map[string]any, string) {
	cfg := map[string]any{
		"retain_mission":      retainMission(names),
		"reflect_mission":     reflectMission,
		"enable_observations": true,
		"enable_reranking":    false,
	}
	raw, _ := json.Marshal(cfg) // map 的键按字典序输出，哈希稳定
	sum := sha256.Sum256(raw)
	return cfg, hex.EncodeToString(sum[:])
}

const (
	// ProfileTopicID 是固定种子主题（D4）；动态主题 id 以 TopicPrefix 开头。
	ProfileTopicID = "owner-profile"
	TopicPrefix    = "owner-topic-"
	// topicMaxTokens 是每个主题的生成上限（设计文档 §4.1：800 时单个主题写到上千字）。
	topicMaxTokens = 400
	profileName    = "主人画像"
	profileScope   = "主人的身份、背景、所在组织、设备与环境、长期兴趣、长期偏好与沟通方式"
)

// sourceQuery 是主题的 source_query：范围说明 + 三条约束 + 格式要求。
func sourceQuery(name, scope string) string {
	return "关于主题「" + name + "」（" + scope + "），主人的当前状态是什么？\n" + reflectMission + "\n" + topicFormat
}

// topicSpec 是创建主题 mental model 的请求体。刷新由 orynd 触发（R9），关闭 Hindsight 的整理后自动刷新。
// exclude_mental_models：reflect 不读任何 mental model（含本主题旧内容），只从事实与 observation 整理。
// 否则 reflect 会反复 search_mental_models、照抄自己的旧内容：首次生成时事实还不全写出的占位内容会一直延续，
// 别的主题的内容也会串进来（真实数据验收时观察到：主人画像 9 次只搜 mental model，全部写成「待确认」）。
func topicSpec(id, name, scope string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "source_query": sourceQuery(name, scope), "max_tokens": topicMaxTokens,
		"trigger": map[string]any{"mode": "delta", "refresh_after_consolidation": false, "exclude_mental_models": true},
	}
}
