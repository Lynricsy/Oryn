# omp 外部集成面调研报告（面向 Oryn）

## 0. 范围与方法

- **没有 shell 工具。** 本次只能读文件和搜索，不能执行 `omp --help`、`omp acp --help` 等命令。所有 CLI 事实来自源码与文档（`cli-reference.md`、`cli-commands.ts`、`commands/*.ts`）。
- **本机安装的是 17.4.1，不是 18.19.0。**
  - `/root/.bun/install/global/node_modules/@oh-my-pi/pi-coding-agent/package.json:4` 和 `pi-wire/package.json:4` 都是 `17.4.1`。
  - 安装包里没有 `docs/` 目录。源码仓库 HEAD 是 `packages/coding-agent/package.json:3` 的 `18.15.0`。
  - **18.19.0 的源码本机不存在，无法核对。** 下文一律以 18.15.0 源码为准，差异见 §0.1。
- 标注 `[推测]` 的是我读代码后的推断，没有直接证据。

### 0.1 安装包 17.4.1 与源码 18.15.0 的差异（已核实）

| 项目 | 安装包 17.4.1 | 源码 18.15.0 |
|---|---|---|
| `src/modes/rpc/` | 只有 `rpc-client / rpc-frame / rpc-input / rpc-messages / rpc-mode / rpc-subagents / rpc-types / host-tools / host-uris` | 另有 `wire/`（commands/events/frames/state）、`rpc-btw / rpc-dictation / rpc-live / rpc-goal / rpc-output / rpc-prompt-results / rpc-session-events / rpc-session-settle / rpc-compat` |
| CLI 命令 | 没有 `session.ts`、`stream.ts`、`auth.ts`、`collab.ts`；有 `ps.ts` | 以上均有 |
| `src/collab/` | 有 `display-name / guest / host / protocol / relay-client / replication-shrink / crypto` | 另有 `controller.ts`（autoStart 与房间轮换）、`registry.ts`（本机 host 发现）、`settings.ts` |
| `pi-wire` | 只有 `src/index.ts` | 另有 `stream.ts`、`tsp.ts`、`skillshare.ts` |
| `packages/` | 安装目录中有 omp-stats、snapcompact、pi-utils、pi-mnemopi、pi-catalog、omptype、hashline 等 | 另有 `collab-web`、`browser-relay`、`metaharness`、`stats` |

因此，以下能力在 17.4.1 上**不存在或形态不同**，Oryn 若依赖就必须锁定新版本：
- `omp session pack/unpack/moved/lease`
- RPC `open_session`、`get_session_lease`、`set_event_filter`、`prompt_result` / `session_settled`、v2 分块协议
- `omp auth export/import`
- `omp collab list/link`

---

## 1. `--mode rpc` 与 `--mode rpc-ui`

### 1.1 传输与启动

- **协议**：stdin/stdout 的换行分隔 JSON（JSONL），**不是 JSON-RPC 2.0**。（`docs/rpc.md` 开头）
- **入站**：`RpcCommand`、`extension_ui_response`、`host_tool_update/result`、`host_uri_result`。
- **出站**：`ready`、`response`、会话事件、`extension_ui_request`、`host_tool_call/cancel`、`host_uri_request/cancel` 等。
- **`@file` 参数**在 RPC 模式下被拒绝（`main.ts:1869`）。
- **stdin 在扩展发现之前就被占用**（`main.ts:1880`，`claimRpcInput()`）。
- **畸形 JSON** 产生 `command:"parse"` 的失败响应，循环不终止。
- **启动顺序**：先写 `ready` 帧，再执行扩展 `session_start`。启动期间只处理 `extension_ui_response` 与 `set_ask_dialog`，其他帧排队等待。
- **退出**：stdin EOF 后，未完成的 UI / host 请求被 reject，已接受命令排空，会话 dispose，退出码 0。会话持久化失败仍未恢复时退出码为 1，并先发出 `notice` 帧。
- **关键约束**：客户端关闭 stdin 后仍必须继续读 stdout，否则进程可能无限期无法退出。

`ready` 帧示例：
```json
{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1,2],"maxFrameBytes":1048576,"maxReassembledFrameBytes":67108864}
```

**帧大小与 v2 分块**
- v1：单帧上限 1 MiB。超限时响应变成 `success:false, error:"RPC response exceeded the transport limit"`，事件的字符串、数组或对象字段被截断。
- v2：客户端发 `{"type":"negotiate_protocol","protocolVersion":2}` 后，超限对象改用 `rpc_chunk` 帧。
  - 字段：`chunkId / index / count / byteLength / data(base64)`。
  - 重组上限 64 MiB。
  - 参考实现 `RpcFrameDecoder` 从 `@oh-my-pi/pi-coding-agent/modes/rpc/rpc-frame` 导出。
- 超大的 `agent_end` 会丢掉已通过 `message_end` 送达的前缀消息，并带 `messageCount`。**宿主必须自己累积 `message_end`，不能依赖 `agent_end.messages`。**
- stdout 背压时，服务端把待发字节溢出到私有临时文件，保持帧顺序，按 64 KiB 块排空。

### 1.2 `--no-ui`、`rpc` 与 `rpc-ui` 的区别

依据：`main.ts:306,1986,2010,2360,2796-2805`、`rpc-mode.ts:1716-1720,1790`、`rpc.md` Extension UI 一节。

| | `--mode rpc` | `--mode rpc-ui` |
|---|---|---|
| 工具 UI 上下文 | 不设置。`hasUI` 对工具为 false（`main.ts:2360`：`hasUI = isInteractive \|\| mode==="rpc-ui"`） | `setToolUIContext(rpcUiContext,true)`，`ask` 等工具 UI 走 `extension_ui_request` |
| `PI_NO_PTY` | 不强制 | 强制 `=1`（`main.ts:2010`） |
| 共同点 | 都设 `PI_NO_TITLE=1`；`applyProtocolDefaults("rpc")`（任务隔离、记忆、advisor 等取中性默认）；监听 config 文件变更 | |

- **`--no-ui`**（`main.ts:306` 要求只能与 rpc/rpc-ui 同用）：扩展运行器无 UI（`ctx.hasUI=false`）。
  - 对话框直接取默认值，不发帧。
  - `notify / setStatus / setWidget / setTitle / set_editor_text` 被丢弃。
  - `rpc-ui --no-ui` 仍会发工具 UI 请求（如 `ask`）。
  - 审批提示走扩展运行器，所以 `--no-ui` 下需要审批的工具直接 fail-closed（报"无交互 UI"错误）。
- 推荐组合：
  - 无 UI 的宿主：`rpc --no-ui`。
  - 只答工具对话框：`rpc-ui --no-ui`。
  - 全 UI：`rpc-ui`。
- **类型定义位置**：
  - `packages/coding-agent/src/modes/rpc/rpc-types.ts` 是 canonical。
  - 另有 `modes/rpc/wire/{commands,events,frames,state,content}.ts`（omptype 模式）。
  - 生成物：`wire/rpc-wire.schema.json`、`wire/rpc-wire.generated.ts`。

### 1.3 全部命令（`wire/commands.ts`，括号内为参数）

- **Prompt / 队列**：
  - `prompt`（message, images?, streamingBehavior?: steer|followUp）
  - `steer`、`follow_up`（message, images?）
  - `remove_queued_message`（message, queue）→ `{removed, images?, imagesDropped?}`
  - `promote_queued_message`（message）
  - `abort`
  - `abort_and_prompt`
  - `abort_and_restore_queue` → `{steering[], followUp[], imagesDropped?, truncated?}`
- **会话 / 协议**：
  - `negotiate_protocol`
  - `new_session`（parentSession?）
  - `open_session`（sessionDir, provider?, modelId?）→ `{cancelled, resumed, sessionId, sessionFile}`
  - `switch_session`（sessionPath, provider?, modelId?）
  - `branch`（entryId）、`fork`（entryId?）
  - `navigate_tree`（entryId, summarize?, customInstructions?）→ `{cancelled, aborted?, editorText?}`
  - `abort_branch_summary`
  - `set_label`、`set_session_name`
  - `handoff`、`export_html`
  - `get_session_lease`（sessionIds）→ `{held[], unknown[]}`
- **状态 / 查询**：
  - `get_state`、`get_session_stats`
  - `get_entries`（since?）→ `{entries, leafId}`；未知 since 返回 `code:"unknown_since"`
  - `get_tree` → `{tree, leafId}`
  - `get_messages`、`get_messages_page`（cursor?, limit 1..256；错误码 `session_busy / stale_cursor`）
  - `get_branch_messages`、`get_last_assistant_text`
  - `get_available_commands`
  - `set_todos`（phases）
  - `goal`（op: get|create|resume|pause|drop, objective?, token_budget?）
- **模型 / 思考**：
  - `set_model`、`cycle_model`、`get_available_models`
  - `set_thinking_level`、`cycle_thinking_level`、`get_available_thinking_levels`
  - `set_fast_mode`、`set_slow_mode`、`set_cache_warming`
- **队列模式与重试**：
  - `set_steering_mode`、`set_follow_up_mode`、`set_interrupt_mode`
  - `compact`（customInstructions?）、`set_auto_compaction`
  - `set_auto_retry`、`abort_retry`
- **宿主扩展**：`set_host_tools`、`set_host_uri_schemes`、`set_ask_dialog`
- **子 agent**：
  - `set_subagent_subscription`（off|progress|events）
  - `get_subagents`、`get_subagent_messages`（subagentId?|sessionFile?, fromByte?）
  - `cancel_subagent`、`steer_subagent`
- **事件控制**：`set_event_filter`（events|null, messageUpdates: full|delta）
- **Bash**：`bash`（命令并发执行，响应乱序，须按 `id` 匹配）、`abort_bash`
- **登录**：`get_login_providers`、`login`、`get_logout_accounts`、`logout`
- **其他**：
  - `predict_word`、`predict_word_feedback`（输入框幽灵文本）
  - `btw`、`btw_cancel`、`get_btw_history`（旁路提问）
  - 语音：`live_start / live_connect / live_stop / live_mute`
  - 听写：`dictation_start / dictation_stop`

响应格式：
- 成功：`{id?, type:"response", command, success:true, data?}`
- 失败：`{..., success:false, error, code?}`

**`prompt` 的完成语义**
- `prompt` 在消息被接纳后立即确认，不等 agent 跑完。
- 之后要么响应里 `data.agentInvoked:false`（本地完成，比如内置 slash 命令），要么稍后收到唯一一个 `prompt_result{id, agentInvoked, status: completed|aborted|error, error?, sessionSettled}`。
- `error` 含 `{message, provider?, model?, httpStatus?, retryable, code?}`。
- `code:"cost_cap"` 表示达到 `--max-cost` / `session.maxCost`。
- **"让出（yielded）"与"会话静止（settled）"是两回事。**
  - `agent_end.yielded=true` 表示 agent 本轮结束。
  - `session_settled` 帧或 `get_state.isSettled` 才表示再没有后台任务能唤醒它。
  - **暂停或回收沙箱前应等 `session_settled`。**

### 1.4 出站事件与帧

**会话事件**（`wire/events.ts`，这 33 种就是 `RpcAgentEvent`）：

| 事件 | 关键字段 |
|---|---|
| `agent_start` | `resumedToolCallIds?` |
| `agent_end` | `messages`, `messageCount?`, `isTerminal?`, `yielded?`, `awaitingAsyncWork?`, `telemetry?`, `coverage?` |
| `turn_start` / `turn_end` | `turn_end`: `message`, `toolResults[]` |
| `message_start` / `message_update` / `message_end` | `message`, `messageId`（进程内唯一，贯穿一条消息）；`message_update` 另有 `assistantMessageEvent` |
| `tool_execution_start` | `toolCallId, toolName, args?, intent?` |
| `tool_execution_update` | `args?, partialResult?` |
| `tool_stream_update` | `update?`（工具流式预览，例如参数增量） |
| `tool_execution_end` | `result?, isError?` |
| `auto_compaction_start/end` | start: `reason` (threshold\|overflow\|idle\|incomplete)、`action` (context-full\|remote\|handoff\|shake\|snapcompact)；end: `result?, aborted, willRetry, errorMessage?, skipped?` |
| `manual_compaction_start/end` | `mode?`；`result? / aborted / skipped? / errorMessage?` |
| `auto_retry_start/end` | `attempt, maxAttempts, delayMs, errorMessage, errorId?`；`success, finalError?, retryErrors` |
| `cache_warming_start/end` | `phase, provider, model`；`outcome` (hit\|miss\|error\|aborted)、`usage?`、`warmingStopReason?` |
| `retry_fallback_applied/succeeded` | `from,to,role,reason?`；`model,role` |
| `model_changed`、`config_warnings_changed`、`advisor_cost_changed`、`advisor_yielded` | 无负载，客户端需重新 `get_state` |
| `ttsr_triggered` | `rules[]`（`name,path,content`） |
| `todo_reminder` | `todos, attempt, maxAttempts` |
| `todo_auto_clear` | 无负载 |
| `irc_message` | `message: CustomMessage` |
| `notice` | `level: info\|warning\|error`, `message`, `source?` |
| `thinking_level_changed` | `thinkingLevel?, configured?, resolved?` |
| `goal_updated` | `goal, state?` |
| `queue_update` | `steering[], followUp[], liveSteered`（排队 chip 的权威快照） |

**`assistantMessageEvent` 子类型**（`packages/ai/src/types.ts:1680-1702`）：
`start / text_start / text_delta / text_end / thinking_start / thinking_delta / thinking_end / image_end / toolcall_start / toolcall_delta / toolcall_end / done / error`。
- 带 `partial: AssistantMessage` 完整快照；`toolcall_end` 带 `toolCall`。
- `set_event_filter{messageUpdates:"delta"}` 会去掉 `partial` 以省带宽。`*_start` 事件带 `block` 壳，客户端据此重建。

**非会话帧**（`wire/frames.ts`）：
- `ready`、`prompt_result`、`session_settled`、`extension_error`
- `available_commands_update`、`rpc_frame_error`
- `subagent_lifecycle`（`id, agent, agentSource, description?, status: started|completed|failed|aborted, sessionFile?, parentToolCallId?, index, detached?`）
- `subagent_progress`（`index, agent, task, parentToolCallId?, assignment?, progress{…}, sessionFile?`）
- `subagent_event`（`{id, event: RpcAgentEvent}`；这三种仅在订阅后发送）
- `btw_delta`、`btw_record`
- `command_output`（本地 slash 命令输出）
- `session_info_update`（`title?, sessionId`）、`config_update`（`model?, thinkingLevel?`）
- `live_phase / live_levels / live_transcript / live_end`
- `dictation_state / dictation_transcript / dictation_end`
- `host_tool_call / host_tool_cancel`、`host_uri_request / host_uri_cancel`
- `notice` 另有 `source:"session-persistence"` 与 `source:"btw-history"` 变体，不受 `set_event_filter` 过滤。

### 1.5 Extension UI 子协议

出站 `extension_ui_request`，按 `method` 区分：
- `select`（`options[]`, `optionDetails?[]`, `timeout?`）
- `confirm`（`title`, `message`）
- `input`（`placeholder?`, `secret?`）
- `editor`（`prefill?`, `promptStyle?`）
- `ask`（仅在 `set_ask_dialog{enabled:true}` 后发送，见下）
- `cancel`（`targetId`）
- `notify`（`message`, `notifyType?`）
- `setStatus`、`setWidget`（`widgetLines?`, `widgetPlacement`）
- `setTitle`（默认被抑制，设 `PI_RPC_EMIT_TITLE=1` 开启）
- `set_editor_text`
- `open_url`（`url`, `launchUrl?`, `instructions?`；登录流程用）

入站 `extension_ui_response` 四种形态：`{id, value}`、`{id, confirmed}`、`{id, cancelled:true, timedOut?}`、`{id, answers:[{id, selectedOptions[], customInput?}]}`。

`ask` 请求体：`questions:[{id, question, header?, options:[{label,description?,preview?}], multi?, recommended?}]` 加 `timeout?`。
- 超时或 `timedOut` 时，每题取 `recommended`，没有则取第一项。
- 选项里**不会**包含"Other"，宿主应总是提供自由文本。

**审批请求**（`tools/approval.ts:~840-905`）：RPC / ACP / 扩展宿主没有专用审批对话框，走 `ui.select(完整提示文本, approvalChoiceLabels(...))`。
- 选项标签：`Approve` / `Approve Once`、`Approve <tool> for this session`、`Approve File` / `Always Approve File`、`Deny`。
- 标签与答案的映射函数是 `approvalAnswerForChoice`。
- **宿主必须原样回传所选标签字符串**，不是结构化答案。

**不支持的终端专属 UI**：`custom()`、自定义 header/footer/editor、主题切换、工具展开。

### 1.6 宿主工具与宿主 URI

- `set_host_tools`（`name/label/description/parameters + hidden?/loadMode?/readsSkillUris?`）后，agent 调用时发 `host_tool_call{id, toolCallId, toolName, arguments}`。
  - 宿主可发 `host_tool_update{partialResult}`，最后发 `host_tool_result{result, isError?}`。
  - 被中止时发 `host_tool_cancel`。
- `set_host_uri_schemes`（scheme 小写，`writable`, `immutable`）后，对 `<scheme>://` 的读写以 `host_uri_request{operation: read|write, url, content?}` 回调宿主。
  - 结果是 `host_uri_result{content?, contentType?, notes?, immutable?, isError?, error?}`。
  - 内置 scheme 保留，宿主不能覆盖。
  - `edit` 工具不能作用于宿主 URI，只能用 `write`。
- **这是 Oryn 把"orb 外资源"（云盘、Portal 列表、Space 文件）暴露给 agent 的现成机制。**

---

## 2. 在 GUI 中复现 TUI 的全部内容：事件与渲染对照

### 2.1 TUI 的事件分发总表

`packages/coding-agent/src/modes/controllers/event-controller.ts:392-473` 定义 `AgentSessionEventHandlers`，类型为 `[E in AgentSessionEventKind]`，**覆盖每一种事件类型**。GUI 客户端应以同样的"每种事件必须有处理器"为设计约束。

渲染代码主要在 `packages/tui/src/`（不在 `coding-agent/src/modes/components`，那里已基本掏空，只剩 `extensions/` 四个文件）：
- `packages/tui/src/chat/*` 是消息组件。
- `packages/tui/src/tools/*` 是各工具渲染器。
- 注册表在 `packages/tui/src/tools/index.ts`（`toolRenderers`）。

### 2.2 对照表

| 内容 | 数据来源（事件 / 字段） | TUI 渲染位置 |
|---|---|---|
| 助手文本流 | `message_update.assistantMessageEvent` 的 `text_*`；`message_start/end` 用 `messageId` 配对 | `tui/src/chat/assistant-message.ts`；`event-controller.ts:#handleMessageStart/Update/End`（约 1222、1571、1760）；流式揭示节奏见 `controllers/streaming-reveal.ts` |
| 思考 | `thinking_start/delta/end`；`content[].type:"thinking"` 与 `redactedThinking`；`thinking_level_changed` | `tui/src/chat/thinking-display.ts`、`assistant-message.ts`；`--hide-thinking` 对应 `setHideThinkingBlock`。叙述型 thinking 在 `thinking_end` 后会被当作回复 |
| 工具调用生命周期 | `toolcall_start/delta/end`（参数流式）→ `tool_stream_update`（预览）→ `tool_execution_start{toolCallId,toolName,args,intent}` → `tool_execution_update{partialResult}` → `tool_execution_end{result,isError}`；最终以 `toolResult` 消息落盘（`role:"toolResult"`, `toolCallId`, `toolName`, `content[]`, `details`, `isError`） | `tui/src/chat/tool-execution.ts`（`ToolExecutionComponent`）；`controllers/tool-call-block.ts`、`tool-args-reveal.ts`；`event-controller.ts:#handleToolExecutionStart/End`（约 1970、2189）。`read` 工具有分组：`chat/read-tool-group.ts` |
| 各工具 `details` | 见 §2.3 | `tui/src/tools/<tool>.ts`；React 参照实现见 `collab-web/src/tool-render/tools/*.tsx` |
| 子 agent / `task` | `task` 工具卡片里的 `details`；`subagent_lifecycle/progress/event`（需订阅）；`get_subagents`、`get_subagent_messages`（按 `fromByte` 增量读转录，返回 `nextByte, reset`） | `tui/src/tools/task.ts`、`agent-tree.ts`；Agent Hub 见 `docs/agent-hub.md`；`modes/agent-hub-runtime.ts` |
| todo | `get_state.todoPhases`；`todo` 工具结果；`todo_reminder`、`todo_auto_clear`；`set_todos`。结构 `TodoPhase{name,tasks[{content,status,blocker?}]}`，status ∈ pending / in_progress / completed / abandoned / blocked | `tui/src/tools/todo.ts`、`tui/src/chat/todo-reminder.ts`；`controllers/todo-command-controller.ts` |
| plan mode / propose | 模型通过 `write xd://propose`（正文为 plan slug）提交计划，由 `setPlanProposalHandler` 处理。**RPC 里该 handler 只出现在 interactive-mode.ts:5831、acp-agent.ts:2355、prewalk.ts:531，未找到 RPC 侧安装**（见 §2.4） | `tui/src/tools/resolve.ts`（`PROPOSE_DEVICE_NAME`）；`plan-mode/*`；ACP 实现可参考 `acp-agent.ts:#handleAcpPlanProposal`（"Approve and execute" / "Refine plan"） |
| ask 表单 | `extension_ui_request{method:"ask"}`（需先 `set_ask_dialog`）；`ask` 工具结果 `details`（`QuestionResult{id,question,options,…}`）；无 `set_ask_dialog` 时退化为逐题 `select` + `editor` | `tui/src/tools/ask.ts`；`tools/ask.ts`（`recoverAskQuestions`、`OTHER_OPTION`） |
| approval | `select` 型 `extension_ui_request`，标签集合见 §1.5 | TUI 是停靠在输入框上方的专用对话框，`tools/approval.ts:requestApproval` |
| compaction | `auto_/manual_compaction_start/end`；`CompactionResult{summary, firstKeptEntryId, tokensBefore, details}`；落盘 `compaction` 条目（含 `shortSummary`, `tokensAfter`, `method`） | `tui/src/chat/compaction-summary-message.ts`；`event-controller.ts:#handleAutoCompactionStart/End`（约 2697） |
| 模型 / 用量 / 成本 / 速度 | 每条 `AssistantMessage.usage`、`.model`、`.provider`、`.duration`、`.ttft`、`.serviceTier`、`.stopReason`；`get_state.contextUsage / tokensPerSecond / fastMode* / slowMode* / usageLimit`；`get_session_stats`；`model_changed`、`retry_fallback_*`、`advisor_cost_changed` | `tui/src/overlays/usage-row.ts`（`createUsageRowBlock`, `TurnUsageTally`）；状态栏 `statusLine`；`chat/served-model-marker.ts` |
| 图片 | 助手 `image_end{content: ImageContent}`；用户 `prompt.images`；工具结果 `content[].type:"image"`；落盘为 `blob:sha256:<hash>`，加载时回填 base64 | `tui/src/chat/image-loading.ts`、`tools/*`；`eval` 的 `details.images` |
| artifact / blob | 被截断的工具输出，文本里带 `artifact://<id>`；文件在 `<会话文件去 .jsonl>/<id>.<tool>.log` | 读取只在 agent 的 `read` 工具内通过 internal URL 解析（`internal-urls/artifact-protocol.ts`）。**RPC 没有读取 artifact 的命令**，GUI 需直接读 orb 文件系统，或用 host URI |
| 通知 | `notice{level,message,source?}`；`extension_ui_request{notify,setStatus,setWidget}`；`ttsr_triggered`；`config_warnings_changed`；`session_info_update` | `event-controller.ts:#handleNotice`（约 1526）；`tui/src/chat/ttsr-notification.ts` |
| queue / steer | `queue_update`、`get_state.queuedMessages`；命令 `steer / follow_up / remove_queued_message / promote_queued_message / abort_and_restore_queue` | pending-messages bar；`controllers/input-controller.ts` |
| 其他消息类 | `custom_message`（`display`, `details`, `attribution`）、`bashExecution`、`pythonExecution`、`fileMention`、`branchSummary`、`irc_message`、advisor 卡片 | `tui/src/chat/{custom-message,bash-execution,eval-execution,advisor-message,skill-message,hook-message,late-diagnostics-message,recap-notice,cache-invalidation-marker,collab-prompt-message}.ts` |

### 2.3 各工具 `details` 结构位置

没有统一的 schema，每个工具的类型都在对应渲染器文件里：
- `tui/src/tools/bash.ts`：`BashToolDetails{meta?, timeoutSeconds?, requestedTimeoutSeconds?, exitCode?, timedOut?, service{name,state,ready,timedOut,pid}, …}`
- `edit.ts`：`EditToolDetails{diff, errorKind?, perFile…}`
- `grep.ts / glob.ts / find.ts / ast-grep.ts / ast-edit.ts / lsp.ts / debug.ts / eval.ts / github.ts / goal.ts / mcp.ts / memory.ts / web-search.ts / fetch.ts`
- 以及 `ask.ts`、`task.ts`、`todo.ts`、`write.ts`、`yield.ts`

**关键特性**
- `collab-web` 的渲染器契约明确要求容忍部分或畸形的 `args` / `details`（`collab-web/src/tool-render/types.ts`），因为它们作为纯 JSON 到达。
- `tool_execution_update.partialResult` 与最终 `result` 都是 `{content[], details?}`。
- 参数里的 `i` 字段是意图（intent）字段，渲染前应剥掉（`ToolRenderProps.args` 注释；`wire` 里 `INTENT_FIELD="i"`）。
- 工具名不是闭集。每个工具渲染器都可以回退到 `renderDefaultToolExecution`；MCP 工具等也走默认渲染。

### 2.4 RPC 下的缺口

- **plan mode**：`/plan` 等 slash 命令能否进入 plan mode 取决于内置命令；**RPC 下没有 `setPlanProposalHandler` 实现**，`xd://propose` 在 RPC 里能否被审批 [推测：会报错或无响应]。Oryn 若要 plan 审批 UI，需要自行验证或通过 `extension_ui_request` 另行实现。
- **子 agent revive**：RPC 只有 `cancel_subagent` / `steer_subagent`，未找到 revive 命令。文档称对已停放或已终止的 agent `write agent://<id>` 会唤醒它，那是 agent 工具层的操作。
- **ACP 与 RPC 的映射差异**：ACP 的事件映射器（`acp-event-mapper.ts`）只转换 `message_update / message_end / tool_execution_* / todo_reminder / todo_auto_clear`，**没有子 agent、compaction、queue 等事件**（compaction 通过 `_omp/compaction/start|end` 扩展通知，见 `docs/compaction.md` ACP notifications）。因此"完整展示"必须走 RPC。

---

## 3. SDK 与 ACP

### 3.1 SDK

依据：`docs/sdk.md`、`sdk/` 目录、`packages/coding-agent/package.json`。

- **进程内嵌入，仅 TypeScript / Bun**：
  - `bun add @oh-my-pi/pi-coding-agent`，要求 Bun ≥ 1.4.4（`package.json:567-568`）。
  - 入口 `createAgentSession()`、`SessionManager`、`Settings`、`AuthStorage`、`ModelRegistry`、`AgentRegistry`。
  - 子路径 `./sdk` 更窄（不含 `SessionManager / AuthStorage / ModelRegistry`）。
  - 文档明说："需要跨语言 / 进程隔离请用 RPC"。
- **多语言 = 进程外 RPC 客户端**：`sdk/python/omp-rpc`、`sdk/rust/omp-rpc`、`sdk/go/omp-rpc` 三个包。
  - 类型均由 `bun run gen:rpc` 从 `rpc-wire.schema.json` 生成。
  - 它们封装 v2 协商、分块重组、消息分页、`promptAndWait`、host tools / URI、扩展 UI。
  - Python 另有 `RpcClient`（默认拉起 `omp --mode rpc`）。
  - `packages/coding-agent/src/modes/rpc/rpc-client.ts` 是 TS 版客户端，**只是便利封装，不是协议定义**。
- 没有官方 JS 的浏览器 / Node SDK。Web 端要么自己实现 JSONL 客户端，要么经 Oryn 后端中转。
- `docs/rpc.md` 末尾提到 `packages/coding-agent/test/rpc-wire` 会校验生成物与 `rpc-types.ts` 一致，漂移会让 `bun check` 失败。**协议兼容性有测试保障。**

### 3.2 ACP（`omp acp`，等价于 `--mode acp`）

依据：`modes/acp/acp-agent.ts:836-890,2354`，`docs/approval-mode.md` ACP sessions。

- **能力声明**：`loadSession:true`；`mcpCapabilities{http,sse}`；`promptCapabilities{embeddedContext, image}`；`sessionCapabilities{list,fork,resume,close}`；顶层 `_meta.steering.supported:true`。
- 认证方式：`agent`（用本机已有凭据），客户端声明 `auth.terminal` 时多一个 `terminal`（拉起 omp TUI 配置）。
- 扩展方法：
  - `_session/steering`（可用 `_meta.steering.queue:"followUp"` 路由到 follow-up 队列）
  - `speech.models.list`
  - `_omp/compaction/start|end` 通知
- 审批：使用 `session/request_permission` 与 form elicitation。
- 支持客户端的 terminal 能力（`clientBridge.capabilities.terminal`），bash 可在客户端编辑器终端中运行（`bash-tool-runtime.md` §5）。
- `session/new` 无审批策略字段，**按进程配置审批**（`--yolo` / `--approval-mode` / `--config`）。
- **局限**：
  - 事件映射窄（见 §2.4）。
  - 没有子 agent 面板、队列管理、会话树、todo 之外的 UI。
  - 状态是"IDE 内嵌聊天"级别，不够 TUI 级。
  - 与 Zed 有已知时序竞态，已有 50 ms 防护（`ACP_BOOTSTRAP_RACE_GUARD_MS`）。

---

## 4. Collab

依据：`docs/collab.md`、`packages/wire/src/index.ts`、`packages/coding-agent/src/collab/*`、`packages/collab-web/README.md`、`collab-web/scripts/local-relay.ts`。

### 4.1 架构

- **星型拓扑，host 权威，guest 之间不互通。**
  - host 是运行 TUI 的 omp 进程，持有 agent 与所有工具。
  - relay 是内容盲的 WebSocket 转发服务，不保存状态。
  - guest 是另一个 omp（`omp join`）或浏览器（collab-web）。
- **host 只能是交互式 TUI。** `collab/controller.ts:16,45,67` 的构造参数是 `InteractiveModeContext`，因此 **headless 的 `--mode rpc` 进程不能直接成为 collab host** [推测，依据是类型依赖，未找到 RPC 侧启动 collab 的代码]。
- 房间随会话轮换：`/new`、`/resume`、`/fork` 会关房并以新 generation 重建。
- `collab.autoStart = view|control` 可让每个交互会话自动开房；本机可用 `omp collab list/link` 查询，注册信息在 `~/.omp/run/collab-hosts`（Unix socket / named pipe，owner-only 权限）。

### 4.2 协议

- **传输**：WebSocket `wss://<relay>/r/<roomId>?role=host|guest`（`relay-client.ts:~540`；`local-relay.ts`）。
- **信封**：`[4B uint32 BE peerId][AES-GCM 密文]`，`ENVELOPE_HEADER_LENGTH=4`。
  - peerId=0 表示 host 广播。
  - 非 0 表示发给指定 guest。
  - guest 发出的帧由 relay 重写 peerId 后转给 host。
- **密封**：AES-256-GCM，布局 `[12B IV][ciphertext+tag]`（`collab/crypto.ts`）。
- **relay 控制消息（明文 TEXT JSON）**：`{t:"peer-joined"|"peer-left", peer}`（发给 host）、`{t:"room-closed"}`（发给 guest）。
- **关闭码**：4009 表示重复 host；4004 表示房间不存在；4001 表示房间已关。
- **版本**：`COLLAB_PROTO = 3`（`pi-wire/src/index.ts`）。
- **HostFrame**：
  - `welcome{proto, header, state, agents, entryCount, readOnly?}`
  - `snapshot-chunk{entries, final}`
  - `entry{entry}`：持久化条目，**在图片 blob 外置之前广播**，所以图片是内联的
  - `event{event}`：实时 agent 事件
  - `state`：防抖的 footer 快照（模型、thinking、contextUsage、participants）
  - `bus{channel, data}`（仅 `task:subagent:progress|lifecycle`）
  - `agents`：agent 注册表快照
  - `ui-request / ui-request-end`
  - `transcript{reqId, text, newSize, error?}`
  - `bye`、`error`
- **GuestFrame**：`hello{proto, name, writeToken?}`、`prompt{text, images?}`、`ui-response`、`abort`、`agent-cmd{chat|kill|revive}`、`fetch-transcript{reqId, agentId, fromByte}`。
- 转录读取单次 ≤ 4 MiB，并在完整 JSONL 行处截断。
- 超大条目会被 `replication-shrink.ts` 缩减，或用 `collab-entry-too-large` 占位。
- 注意：**collab 的 `AgentEvent` 类型是 RPC 事件的子集**（`pi-wire` 仅处理约 15 种）。

### 4.3 链接与权限

- **链接格式**：`<roomId>.<key>`（默认 relay `wss://my.omp.sh`）。
  - 另有 `host[:port]/r/<roomId>.<key>`、`https://…/r/…`、`wss://…`、`ws://localhost:7475/r/…`。
  - 浏览器深链是 `https://<web>/#<link>`；房间密钥在 URL 片段里，不会发到服务器。
  - 旧格式 `#<key>` 仍可解析。
- **密钥**：
  - 完整链接 = 48 字节（32B 房间密钥 + 16B 写令牌）。
  - 只读（view）链接 = 32B 裸密钥。
- **权限**（由链接本身决定，host 在 join 时校验写令牌）：
  - 完整链接可以：读全部、prompt、abort、Agent Hub 操作（chat / kill / revive）、回答 host 的 `select` / `editor` 请求。
  - 只读链接只能读。
  - `/model`、`/compact`、bash 等仍然只有 host 能做。
  - advisor 记录不对 guest 可见。

### 4.4 `packages/collab-web`

- **是什么**：纯静态 SPA 的浏览器 guest 客户端。
  - 技术栈：React（`.tsx`）+ Bun HTML dev server / bundler。
  - 接口：`GuestClient` + `useSyncExternalStore` 快照；`socket.ts` 可重连。
  - 依赖 WebCrypto，需要 HTTPS 或 localhost。
  - `bun run build` 产出 `dist/`，可部署到任意静态站点。
  - 与 `@oh-my-pi/pi-wire` 共享类型，但**不依赖 coding-agent 运行时**。
  - 自带 `src/lib/{codec,link,socket,client}.ts`、`components/{transcript,agents,shell}`、`src/tool-render/`（每个内置工具一个 React 渲染器，也打包进 HTML 会话导出的 `<omp-tool-view>`）、`trace-render/`。
- **能渲染的内容**：流式文本、thinking、工具卡片（约 30 个工具）、子 agent 面板与按需转录、composer（prompt / 中断 / hub 动作）。
  - 条目类型只覆盖 pi-wire 子集：`message / custom_message / compaction / branch_summary / model_change / thinking_level_change`。
- **relay 服务端**：
  - 生产 relay 是一个小型 Go 服务，**源码与二进制均未发布，不可自托管**（`collab.md` "Self-hosting the relay"）。
  - 仓库里只有 WebSocket-only 的开发替身 `packages/collab-web/scripts/local-relay.ts`（默认 7466）：实现 `/r/<roomId>`，但**不托管浏览器客户端、`/share` blob、`/healthz`**。
  - 生产 relay 端点：`GET /`、`GET /r/<roomId>?role=…`、`POST /s`、`GET /s/<id>`、`GET /s/<id>/raw`、`GET /healthz`。
- **能否作为多端实时同步的基础**：
  - 可复用：`pi-wire` 的类型、`collab-web` 的渲染器与 codec、`local-relay.ts` 的信封与房间语义。
  - 不适合直接用：它依赖 host 是交互式 TUI；只有 prompt / abort 两种写操作；事件子集有限；密钥模型是"持链接即授权"，没有账号体系。
  - 因此**应作为参考设计，不作为运行依赖**。自建 relay 的最小规格就是 `local-relay.ts` 的 100 行契约。

---

## 5. `omp stream`、live.omp.sh、`omp clip`、`/record`

依据：`docs/stream.md`、`packages/wire/src/stream.ts`。

- **`omp stream`**：广播渲染后的屏幕行（不是会话数据）到 `live.omp.sh/<username>`。
  - 需要 stencil.so 账号（`/login` → Stencil，或 `STENCIL_API_KEY`）。
  - 与 collab 无关。
  - 机制：会话进程取 TUI 刚绘制的行，只保留 SGR 与 OSC 8，图片变成 `[image]`，做不可逆脱敏，计算与上次视口的差异，通过本机私有 Unix socket（0600）发给 `omp stream` 进程，再经 WSS 发往服务器。服务器是 Go 服务，内存里保存每个 pane 的视口加最近 2000 行（`STREAM_HISTORY_LIMIT`）。
  - 帧类型：`pane-open / pane-close / resize / history / viewport / patch / reset / paused`；主机帧 `hello / title / chat`。
  - 观众只读，聊天另走消息通道。
- **`/record` 与 `omp play`、`omp clip`**：
  - `/record` 用同一条管线，把脱敏后的行写入本地 `<tmpdir>/omp-recordings/<utc>-<session>.ompcast`。
  - 格式：JSONL。首行 `{"ompcast":1,"cols","rows","title","createdAt"}`，随后每行 `[ms, frame]`，帧类型为 `reset / history / resize / viewport / patch`。
  - `omp play` 在终端回放。
  - `omp clip` 上传到 live.omp.sh 并给出 `live.omp.sh/c/<id>`。
- **可复用性**：
  - `.ompcast` 是简单的终端帧录像，可作为 Oryn 的"终端录屏"格式参考。
  - 但它描述的是屏幕行，不是语义事件，**不能用于结构化的对话视图**。
  - live.omp.sh 服务端不可自托管（`stencil apps/live`）。
  - Oryn 的实时同步更应走 RPC 事件流。

---

## 6. 会话存储

依据：`docs/session.md`、`session/session-entries.ts`、`session/session-transfer.ts`、`commands/session.ts`。

### 6.1 路径与文件格式

- 会话文件：`~/.omp/agent/sessions/<encoded-cwd>/<timestamp>_<sessionId>.jsonl`。
  - 编码规则：home 下为 `-<相对路径>`，临时目录下为 `-tmp-…`，其他为 `--<绝对路径>--`。
  - 命名 profile 时位于 `~/.omp/profiles/<name>/agent/…`。
  - 会话 ID 是 UUIDv7。
- Blob：`~/.omp/agent/blobs/<sha256>`（全局内容寻址）。
- Artifact：`<会话文件去 .jsonl>/`（`<id>.<tool>.log`、`<outputId>.md`、`<outputId>.jsonl` 子 agent 转录、`.../__advisor*.jsonl`）。
- 其他：`terminal-sessions/<terminal-id>`（面包屑）、`history.db`（提示历史、`session_titles`、`session_recaps`，带 FTS5）、`agent.db`（凭据 SQLite）、`~/.omp/run/session-owners/<session-id>`（所有权租约）。
- **文件结构**：
  - 首行固定 256 字节的 `type:"title"` 槽（便于改名不重写）。
  - 然后是 header（`version:3`, `id`, `timestamp`, `cwd`, `title`, `titleSource`, `additionalDirectories`, `previousSessionFiles`, `providerPromptCacheKey`, `taskPromptCacheKey`, `parentSession`）。
  - 然后是条目，基础字段 `{type, id(8位hex), parentId|null, timestamp}`。
- **条目类型（16 种）**：
  - `message`（role camelCase：`user / developer / assistant / toolResult / bashExecution / pythonExecution / hookMessage / fileMention`）
  - `model_usage`
  - `thinking_level_change`、`model_change`（`model:"provider/id"`, `role?`）、`service_tier_change`
  - `compaction`、`branch_summary`、`reset_boundary`
  - `custom`（核心保留的 `customType`：`tool_execution_start / session_exit / user_todo_edit / vibe-session-lifecycle / autoresearch-control / system-prompt-digest`）
  - `custom_message`、`label`、`title_change`、`ttsr_injection`、`credential_pin`
  - `session_init`（也是子 agent 冷启动契约）、`mode_change`
- **树与分叉**：
  - 追加式树（`id/parentId`）+ 内存里的 `leafId`；**叶子不持久化**，加载时取最后一条物理条目。
  - `branch` 只移动指针；`navigate_tree` 支持带摘要的分支跳转；`createBranchedSession` 复制根到叶的路径成新文件；`fork` 新文件加 `parentSession`。
  - 可选 `copyArtifacts`，后台拷贝 artifacts。
- **写入**：
  - 新会话在出现第一条助手消息前只在内存里（`ensureOnDisk()` 可强制落盘）。
  - 之后增量追加，无 `fsync`。
  - 完整重写用原子替换，并有字节大小前置校验（`SessionWriteConflictError`）。
  - 超过 500,000 字符的字符串会被截断。
  - ≥1024 字符的 base64 图片外置为 `blob:sha256:<hash>`。

### 6.2 所有权与跨机器迁移

- **所有权租约**：写入的进程持有 `session-owners/<session-id>`（Linux 用 abstract socket，其他 Unix 用 flock，Windows 用命名 mutex）。
  - **另一进程发现租约被占时，不会写同一文件，而是 fork 出一个兄弟文件继续写**（`parentSession` 指向原会话）。
  - `omp session lease --json -- <id>…` 与 RPC `get_session_lease` 无副作用地探测，返回 `{held, unknown}`。
  - 注释写明这是为 `omp-web` 这类 JSON 消费者设计的（`commands/session.ts:46-50`）。**该 omp-web 不在仓库内，未找到。**
- **`omp session pack <session> <out.tar>`**：生成纯 tar。
  - `manifest.json {version:1, id, cwd, omp}`
  - `session.jsonl`（原样字节）
  - `artifacts/<path>`
  - `blobs/<sha256>`（缺失的 blob 会警告）
- **`omp session unpack <archive.tar> --cwd <dir>`**：落到目标 cwd 的会话目录，stdout 只打印会话文件路径。
  - 模式三种：`created`（无本地副本）、`replaced`（本地是前缀，直接替换）、`forked`（已分叉，生成新 id 的 fork）。
  - 并记录一条 `custom_message`（`customType:"session-transfer"`, `details.kind:"arrived"`）。
- **`omp session moved <session> --to <label>`**：给本地副本打上"已离开"标记。之后 resume 这个副本会**自动 fork 而不是续写**（`forkIfMovedAway`）。标记也是 `session-transfer` 条目，`kind:"departed"`。
- **`--restore [id]`**：像 `--resume` 一样打开，然后在 TUI 起来后接着做上次进程崩溃时没做完的事（先恢复被打断的子 agent，再恢复会话自身被打断的那一轮）。
  - 仅交互式，不带 prompt，不能与 `--continue / --fork / 导入 / --goal / --no-session` 同用。
  - 依据：`session-operations-export-share-fork-resume.md` §CLI `--restore`，约 477-506 行。
- **resume 时的中断恢复**：若最新 `session_exit` 之后是非终态尾部，初始化会追加一条 `stopReason:"aborted"` 的合成助手消息。
- **其他**：`--export <jsonl>` 导出 HTML；`/share` 端到端加密分享（gzip + AES-GCM，上限 1 MB，链接 `<base>/<id>#<key>`）；`omp gc` 做 blob 垃圾回收、归档与压缩。

---

## 7. 远程运行 omp

### 7.1 发行形态

- **npm 包 + Bun**：`bin.omp = src/cli.ts`（`coding-agent/package.json:27-28`），源码直接发布，`engines.bun >=1.4.4`。
- **预编译单文件二进制**：`scripts/install.sh --binary` 从 `https://build.stencil.so/api/products/omp/latest/<target>?channel=stable` 下载并校验 sha256。
  - 目标：`linux-x86_64 / linux-arm64 / linux-musl-* / macos-*`。
  - 默认装到 `~/.local/bin`（`PI_INSTALL_DIR` 可改），可用 `--ref v<version>` 固定版本，默认模式是 Bun 源码安装。
  - 二进制内嵌 natives（zstd 压缩），首次运行解压到 `~/.omp/natives/<version>`（`PI_NATIVES_DIR` 可改）。
- **Docker**：根 `Dockerfile` 的 `pi-base` 是 python3.12 + bun + rustup 启动器 + 预构建 `pi_natives.linux-*.node` + `omp_rpc` wheel。`/usr/local/bin/omp` 是一个 shim：`exec bun $PI_ROOT/packages/coding-agent/src/cli.ts`。
  - **官方 Docker 镜像不是单文件二进制，而是 Bun + 源码。**
  - `pi-runtime` 目标把源码烘焙进镜像，入口是 `tini → omp`。
- **natives**：
  - Rust N-API 插件 `pi_natives.<tag>.node`，x64 有 modern / baseline 两种变体。
  - 按文档 `natives-addon-loader-runtime.md` 的顺序加载；版本由链接后写入的 64 字节戳校验。
  - 它提供 shell（brush）、PTY（portable_pty）、grep/glob、AST、diff、voice、desktop 控制等。
  - 缺失或版本不符会报 "Failed to load pi_natives"。
- **无头运行**：`-p`（print）、`--mode json`、`--mode rpc|rpc-ui|acp`。
  - 非 TTY 的 stdin 在 print 模式下被读作 prompt。
  - `--max-time`、`--max-cost`（超出时退出码 3）、`--wait-async`。

### 7.2 凭据

- **`omp auth export` / `import <file|->`**（`commands/auth.ts`）：
  - 导出 `{version:1, credentials:[{provider, credential}]}`，包含 API key 与 OAuth（含 refresh token）。
  - 导入时合并：同账号 OAuth 或同 provider 的 API key 被替换，其余保留。
  - 警告：轮换 refresh token 的 provider 只接受最新的一个，导入后原机器可能被登出。
  - 若配置了 auth-broker，则命令拒绝并报错（凭据在 broker 里）。
  - 支持 `omp auth export | ssh host omp auth import -`。
  - 17.4.1 无此命令。
- **auth-broker**（`docs/auth-broker-gateway.md`）：
  - 凭据 vault 与 OAuth 刷新器，`omp auth-broker serve` 默认 `127.0.0.1:8765`，SQLite 存储，token 在 `<config>/auth-broker.token`（0600）。
  - HTTP API：`/v1/healthz`、`/v1/snapshot`（refresh token 替换为 `REMOTE_REFRESH_SENTINEL`）、`/v1/snapshot/stream`（SSE）、`/v1/credential[/:id/{refresh,disable,block}]`、`/v1/credentials/disabled`、`/v1/usage`、`/v1/usage/history`、`/v1/usage/observed`、`/v1/usage/clients`、`/v1/usage/stale`。
  - 客户端设 `OMP_AUTH_BROKER_URL` / `OMP_AUTH_BROKER_TOKEN`，与本地缓存 `~/.omp/cache/auth-broker-snapshot.enc`（AES-GCM，密钥由 token 派生）搭配。
  - 可用 `OMP_AUTH_BROKER_ACCOUNT_POOL_FILE` 限制客户端可见账号。
  - **直接 broker 客户端拿到的是 access token 和 API key，不拿 refresh token。**
- **auth-gateway**：
  - `omp auth-gateway serve` 是前向代理，默认 `127.0.0.1:4000`，自己也是 broker 客户端。
  - 接受 OpenAI Chat、Anthropic Messages、OpenAI Responses、`/v1/pi/stream`、图像、语音、转写、embedding、rerank、视频等请求。
  - 客户端永远看不到真实 access token。
  - `--clients=<file>` 提供按客户端的模型与账号授权（token 以 SHA-256 摘要保存）。
  - `omp auth-gateway stdio` 以 JSON 行服务父进程。
- **在 orb 里的用法**（metaharness / robomp 已这样做）：orb 内的 `models.yml` 把各 provider 的 `baseUrl` 指到 gateway，凭据只在宿主侧解析（`metaharness/README.md` "Auth never enters containers"；robomp README 的 `models.container.yml`）。
- `omp token`、`omp login`、`secrets.yml`（可逆脱敏，见 `docs/secrets.md`）是相关能力。

### 7.3 配置与覆盖（只说明文本配置，不涉及 GUI）

依据：`docs/config-usage.md`、`docs/environment-variables.md`。

- **格式**：YAML（`.yml/.yaml`），也接受 `.json/.jsonc`。
- **用户级**：`~/.omp/agent/config.yml`；**项目级**：`<cwd>/.omp/config.yml`。
- **叠加顺序（高→低）**：
  1. 设置定义上声明的环境变量
  2. 运行期 override（不持久化）
  3. 配置覆盖层：`PI_CONFIG_FILES`（Unix 用 `:` 分隔），然后依次是重复的 `--config <file>`，**后者覆盖前者**
  4. 项目设置
  5. 全局 `config.yml`
  6. 默认值
- **RPC / ACP 额外**：带 `protocolDefault` 的设置会被钉成中性默认值，除非已有显式配置。长期运行的 RPC 进程会监听配置文件变更。
- **profile**：`--profile <name>` / `OMP_PROFILE`（遗留 `PI_PROFILE`）把用户根重定向到 `~/.omp/profiles/<name>/agent/`，覆盖设置、会话、blob、`agent.db`；键位例外。
- **目录环境变量**：`PI_CONFIG_DIR`、`PI_CODING_AGENT_DIR`（仅默认 profile）、`PI_CODING_AGENT_SESSION_DIR`、`XDG_*_HOME`（仅当对应 `omp` 目录已存在时才生效）、`OMP_WORKTREE_DIR`。
- **下发到远程环境的做法**：
  - 把 `config.yml` 挂到 `$HOME/.omp/agent/`。
  - 或用 `PI_CONFIG_FILES` / `--config` 指向只读叠加层。
  - 或每个 orb 用独立 `--profile`。
  - robomp 的做法：宿主挂只读 `/srv/agent-home-stage`，入口脚本复制到 `/srv/agent-home`，agent 以 `HOME=/srv/agent-home` 运行（`Dockerfile.robomp`）。
- **`--cwd <dir>`**：指定启动目录。**`--add-dir <dir>`**（可重复）：增加工作区目录，记入会话 header 的 `additionalDirectories`。
- **其他关键启动旗标**：
  - `--session-dir`、`--no-session`
  - `--approval-mode always-ask|always-ask-cached|write|yolo`、`--yolo`
  - `--tools`、`--no-tools`
  - `--provider-session-id`、`--prompt-cache-key`
  - `--max-time`、`--warn-cost`、`--max-cost`
  - `--no-title`、`--no-pty`、`--no-lsp`、`--no-extensions`、`--no-skills`、`--no-rules`、`--no-context-files`

### 7.4 审批（对无头 orb 很关键）

`tools.approvalMode` 默认 `yolo`。`always-ask-cached` 会在 `approval-cache.db`（agent 目录）里记忆授权。

`tools.restrictedFolders` 里的目录**无论模式都要显式批准**，无 UI 时直接拒绝。子 agent 固定以 yolo 无头运行，任何需要 prompt 的调用都会被拒绝。`computer.control` 只有 `tools.approval["computer.control"]` 能无人值守放行。

---

## 8. PTY / 终端与 `omp ps`

依据：`docs/bash-tool-runtime.md` §5、§Managed service、§Interactive PTY；`docs/natives-shell-pty-process.md`。

### 8.1 bash 工具 `pty:true` 的实现

- 底层是 Rust `PtySession`（portable_pty）：
  - `start(options,onChunk,onStart)` 经 shell 运行。
  - `startArgv` 直接执行。
  - `write / resize / kill`。
  - 默认 120×40，限制 cols 20..400、rows 5..200。
  - Windows 用 ConPTY。
- 交互式路径 `runInteractiveBashPty`（`tools/bash-interactive.ts`）在 TUI 里打开一个**覆盖层控制台**，转发键盘输入，Esc 杀死，尺寸变化同步给 PTY。环境用真实 `TERM=xterm-256color`，去掉非交互加固变量。
- **启用条件**（`tools/bash-pty-selection.ts` 的 `canUseInteractiveBashPty`）：`pty===true` 且 `PI_NO_PTY!=="1"` 且工具上下文有 UI（`hasUI`）。
  - 条件不满足时降级到非 PTY，并在结果后附 "pty requested but unavailable" 提示。

### 8.2 RPC 下的终端

- **RPC 没有把交互式 PTY 交给客户端的机制。**
  - `rpc-ui` 强制 `PI_NO_PTY=1`（`main.ts:2010`）。
  - `rpc` 下 `hasUI=false`，同样不可用。
  - 文档表格明确："RPC tool call → Usually no UI → non-PTY"（`bash-tool-runtime.md` §Mode-specific）。
- 替代路径一：**客户端终端桥**（`clientBridge.capabilities.terminal` + `createTerminal`）仅 ACP 提供，bash 命令在客户端的编辑器终端里远程运行，流式更新 `terminalId`，超时杀死，信号退出码 137。
- 替代路径二：**命名服务**（`bash` 的 `name` + `ready` 参数，`launch.enabled` 默认 true）由 broker 持有 PTY，不需要 TUI，`pty` 默认 true。
  - 通过内部 URL 控制：`read proc://<name>`（状态与日志）、`write proc://<name>`（输入，空内容等于回车）、`write proc://<name>/kill`、`/mode`（persist | session | detached）。
  - 这是 **agent 工具层**的操作，不是 RPC 命令。
- RPC 的 `bash` 命令是一次性非 PTY 执行（`session.executeBash`），返回 `BashResult`。
- 结论：**GUI 的"用户终端 / Desktop 终端"要由 Oryn 自己在 orb 里开 PTY（独立于 omp）**，omp 只提供 agent 侧的命令执行。

### 8.3 `omp ps`

依据：`commands/ps.ts`、`cli/ps-cli.ts`。

- 管理 **daemon broker 监督的后台进程**：`omp ps [list|info|logs|stop|kill|restart] [name]`。
  - 旗标：`--all`、`--json`、`--plain`、`--dir <project>`、`--global <scope>`（如 `browser-relay`）、`-f/--follow`、`--head`、`-n/--lines`（最多 1000）、`--grep`、`--timeout`（stop 的宽限秒数）。
  - 在 TTY 上无参数时打开交互式 `ps-top` 监视器。
- broker 按项目目录或全局范围各有一个。最后一个 omp 客户端断开后等待 `OMP_DAEMON_IDLE_GRACE_MS`（默认 3000 ms）才关闭，并停止未 detached 的服务。
- 被监督的东西包括命名服务、共享浏览器 daemon、browser-relay、IDA 主机（`docs/tools/ida.md`）。
- metaharness 把该值设为 24 小时，使 agent 启动的服务在 omp 退出后继续存活到验证结束。
- **启示**：`omp ps --json` 是读 orb 内"agent 启动了哪些服务"的现成数据源；`OMP_DAEMON_IDLE_GRACE_MS` 决定服务是否随 RPC 进程退出而被杀。

---

## 9. browser 与 computer

### 9.1 browser 工具

依据：`docs/tools/browser.md`、`tools/browser/settings.ts`、`packages/browser-relay/README.md`。

它是 Eval 里的 `browser` 前导对象（不是独立 AgentTool），`browser.enabled` 默认 true。**所有 browser 宿主操作都是 exec 级审批。**

**选择顺序**：
1. `browser.open` 的 `app.cdp_url`
2. `app.path`
3. `app.relay:true`
4. `app.tern:true`
5. 否则按配置：`browser.relay` → `browser.cdpUrl` → 自动 Tern → cmux → **项目共享的托管 Chromium**（headless，首次使用自动安装，带 stealth 补丁）

`app.relay:false` / `app.tern:false` 可分别跳过。环境变量 `PI_BROWSER_RELAY=0|1`、`PI_BROWSER_TERN` 覆盖。

**各后端**：
- **托管 Chromium**：omp 自己的页面。`browser.headless` 默认 true。回合结束时冻结（`browser.freezeOnTurnEnd`），空闲 1800 秒后关闭（`browser.idleCloseSec`）。
- **spawned（`app.path`）**：启动带 CDP 的浏览器或 Electron。
- **connected（`app.cdp_url` / 设置 `browser.cdpUrl`）**：连接已有的 HTTP CDP 发现端点。
- **relay**：驱动用户真实 Chrome（含登录态）。
- **Tern / cmux**：原生 WKWebView，能力受限。

**browser-relay 包**：一个 Chrome 扩展（`omp browser-relay install` 写入 `~/.omp/browser-relay/extension`，到 `chrome://extensions` 加载未打包扩展）。
- 配套的 relay 服务器在 omp CLI 里（`omp browser-relay`，源码在 `tools/browser/relay/`），它伪装成 Chrome 的 CDP 发现端点，把多个 puppeteer 连接复用到 `chrome.debugger` 的单一附加上。
- 默认端点 `http://127.0.0.1:9224`（`browser.relayUrl`），只绑回环。可用 `--token` 防本机其他进程，自动在全局 daemon broker 下启动。
- 被驱动的标签页会归入一个名为 "omp" 的标签组。

**在 orb 里的含义**：
- 默认路径（托管 Chromium headless）无需任何配置。它是 omp 进程的子进程，需要能下载 Chromium（首次使用）。
- `browser.cdpUrl` 可指向 Oryn 自己管理的浏览器 profile 容器（"浏览器 Profile"功能的天然接口）。
- relay 面向本机真实 Chrome，对云端 orb 意义不大 [推测]。

### 9.2 computer 工具（computer use）

依据：`docs/computer-use.md`。

- **默认关闭**：`computer.enabled:false`。配置项还有 `computer.display`（active / all / 原生 ID）、`maxWidth` 3840、`maxHeight` 2400、`backgroundFallback`（takeover | refuse）。
- 是 Eval 里的 `computer` 前导对象：窗口与显示器枚举、截图、原生输入、AX 无障碍树、剪贴板、菜单。
- **平台与环境要求**：
  - macOS：授予屏幕录制与辅助功能权限。
  - Linux X11：可读 DISPLAY、RandR / XTEST；后台输入需要可写的 `/dev/uinput`、udev / libinput 热插拔和兼容的工具包 / 窗口管理器；AT-SPI 无障碍。
  - Linux Wayland：RemoteDesktop portal 或 `LIBEI_SOCKET` 输入；**官方发布的二进制不带 `wayland-pipewire`，截图能力为 false**。
  - Windows：原生捕获、Win32 输入、UIA。
- 审批：直接检查类 helper 是 `read` 级，输入与变更是 `exec` 级；`computer.run(…, {read_only:true})` 为 `read` 级。
- **在无头 orb 里要用 computer use，需要虚拟显示（如 Xvfb）加 AT-SPI 加 uinput [推测]，且应使用 X11 而非 Wayland**。安全上文档建议用专用账号或 VM。

---

## 10. infra/、Dockerfile.robomp、packages/metaharness

**结论：没有现成的"托管运行 omp 给终端用户"的产品化部件，但有三个可借鉴的参考实现。**

- **`infra/`**：只是 CI 基础设施文档（`infra/docs/*`）和 `vmon/omp_ci_template.py`。内容是 GitHub Actions 的自托管 runner（vibemon / vmon，每个 job 一个 KVM microVM，由 `omp-ci` 模板快照 fork）。与运行 omp 无关。
- **`Dockerfile.robomp` + `python/robomp`**（"roboomp"，GitHub 分诊与修复机器人）是最接近的现成参考：
  - 在 `pi-base` 镜像上加 Python 包与 Vite 构建的 SolidJS 面板。
  - FastAPI + sqlite 事件队列 + `WorkerPool`，每个 issue 一个 git worktree，**以 `omp --mode rpc` 子进程方式驱动**，`cwd=worktree`，持久化 `session_dir`，后续事件用 `--continue` 续接同一会话。
  - 崩溃恢复：启动时把 `running` 行改回 `queued`。
  - 安全姿态：双容器同一信任边界；PAT 只在 `gh-proxy` 容器；编排器到 proxy 用 HMAC-SHA256 签名（±30 s）；agent 子进程环境清洗（`worker._SCRUBBED_ENV_KEYS`）；写 GitHub 只能通过 `host_tools.py` 里的宿主工具，所有调用审计到 `tool_calls` 表；模型通过 `models.container.yml` 走宿主代理。
  - 沙箱：`sandbox.py`（克隆池 + worktree 生命周期）、slot 用户、共享 natives 缓存。
  - 这证明了"orb = 容器 + RPC 子进程 + 持久 session_dir + host tools + 凭据代理"的模式可行。
- **`packages/metaharness`**：基准测试管理器（Harbor / TypeScript edit / SnapCompact），含 REST + SSE API 与 SQLite 存储。
  - 其中 **`bench:tb` 通过 `vmon exec --pipe` 在 KVM microVM 里流式驱动 `omp --mode rpc`**，并反向隧道到宿主 auth gateway，凭据不进 VM。
  - 它是"远端 microVM 上跑 omp RPC"的另一份参考。
  - 与托管产品无关的部分：基准调度与评分。
- 官方 Docker 镜像：`bun run pi:image` 构建 `oh-my-pi/pi:dev`，`pi-base` 可作为派生镜像的基座。

---

## 11. SSH 支持

依据：`commands/ssh.ts`、`ssh/*`、`internal-urls/ssh-protocol.ts`、`docs/tools/read.md`（约 273 行）、`glob/grep/find.md`。

- **`omp ssh [add|remove|list]`** 管理 SSH 主机配置。
  - 旗标：`--host / --user / --port / --key / --desc / --compat / --scope project|user / --json`。
  - 配置文件是 `ssh.json` 的 `{hosts:{name:{host, username?, port?, keyPath?, description?, compat?}}}`（`ssh/config-writer.ts`），分用户级与项目级。精确路径我只读到格式，未核对具体文件名与目录。
  - 另有 `ssh-json` 发现提供者，优先级 5（`config-usage.md`）。
- **`ssh://host/<path>`** 是一个 internal URL，由 agent 的 `read / write / grep / glob / find / ast_grep` 使用。
  - `ssh://` 裸 URL 列出已配置主机。
  - 目标可以是预配置主机，也可以是 OpenSSH 能自行解析的目标（如 `~/.ssh/config` 别名）。
  - 读取远端 UTF-8 文本文件（≤ 1 MiB）或一级目录列表。二进制、非 UTF-8 或过大的文件会报错。
  - `ssh://host/~/path` 按远端 `HOME` 展开。
  - 不支持 `?` 查询串（需 `%3F` 转义），但 read 的 JSON 查询是例外（`ssh://HOST/tmp/data.json?q=.items`，在本地渲染）。
  - 目录和 glob 搜索通过 URL 文件系统遍历，无需本地挂载。
  - 需要远端有经验证可运行 POSIX 传输片段（`head/cat/mv/test/ls`）的 shell（`transferShell` 探测）。
  - **审批级别提升为 `exec`**。
- **连接**：`ssh/connection-manager.ts` 用 OpenSSH `ControlMaster` 复用连接（POSIX 平台），control 目录受 `sun_path` 长度限制，必要时回退到 `/tmp/omp-<hash>`。Windows 不支持 ControlMaster。
- `ssh/sshfs-mount.ts` 提供可选的 sshfs 挂载（`getRemoteDir()`），`file-transfer.ts` 做远端读写。
- 未找到独立的 "ssh 工具" 文档；SSH 能力主要通过 `ssh://` URL 与 `omp ssh` 配置管理体现，bash 工具仍在本机执行。

---

## 12. 统计与分析（用户新增范围；不做计费，但要有统计）

### 12.1 数据来源与记录字段

- **每条助手消息**（`packages/ai/src/types.ts:1283-1371`）：
  - `usage`：`input / output / cacheRead / cacheWrite / totalTokens / contextTokens? / orchestration{input,cacheRead,output}? / premiumRequests? / reasoningTokens? / cttl{ephemeral5m,ephemeral1h}? / server{webSearch,webFetch}? / credits{cost,committedCost,acuCost}?`，加 `cost{input,output,cacheRead,cacheWrite,total}`（定义见 `packages/catalog/src/types.ts:210+`）。
  - 另有 `provider / model / api / serviceTier / upstreamProvider / upstreamModel / stopReason / errorMessage / errorStatus / errorId / credentialId`。
  - 时序字段：`duration`（请求耗时 ms）、`ttft`（首 token 延迟 ms）、`timestamp`、`completedAt`。
- **`model_usage` 条目**（`session.md`）：记录对话之外的模型调用（`purpose`, `role?`, `api`, `provider`, `model`, `usage`, `stopReason`, `errorMessage?`），例如缓存预热 `purpose:"cache-warm"`。
- **会话累计**：`UsageStatistics{input, output, cacheRead, cacheWrite, totalTokens, orchestration*, premiumRequests, cost, subagentCost}`（`session/session-entries.ts:~409`）。子 agent 成本由 `task` 工具结果 `details.usage` 汇总（`session-stats.ts:505`）。

### 12.2 RPC 能否取到指标：能

- `get_session_stats` → `SessionStats{sessionFile?, sessionId, userMessages, assistantMessages, toolCalls, toolResults, totalMessages, tokens{input,output,reasoning,cacheRead,cacheWrite,total}, premiumRequests, cost, credits?{cost,committedCost,acuCost}, routedModels?, editRejections?, contextUsage?}`（`wire/state.ts:~219-243`）。
- `get_state`：`contextUsage{tokens, contextWindow, percent}`、`tokensPerSecond`（输出吞吐，可为 null）、`fastModeEnabled/Active`、`slowMode*`、`usageLimit{stage, resetsAtSec, allowanceLeftPercent?}`。
- 每条 `message_end.message.usage / duration / ttft`，可由客户端自行累加。
- `cache_warming_end.usage`、`advisor_cost_changed` 事件、`prompt_result.error.code:"cost_cap"`、`--max-cost` / `--warn-cost` 都能用来做预算告警。
- `get_subagents` / `subagent_progress.progress.{tokens, cost, requests, toolCount, durationMs, contextTokens, contextWindow, resolvedModel}`（`AgentProgress`，`pi-wire/src/index.ts`）提供子 agent 的实时成本。
- **ACP** 里有 `usage_update{size, used, cost{amount,currency}}`（`acp-agent.ts:~2676`）。

### 12.3 `omp stats` 与 `packages/stats`（`@oh-my-pi/omp-stats`）

- **命令**：`omp stats`，旗标 `-p/--port`（默认 3847）、`--host`（默认 127.0.0.1）、`-j/--json`、`-s/--summary`（`commands/stats.ts`）。
- **数据来源**：解析 `~/.omp/agent/sessions/` 下的 JSONL，增量同步。
  - 文件有 `file_offsets` 游标。
  - 带 watcher，会话写入时自动重新同步。
  - 解析时只抽取统计，丢弃消息正文。
- **存储**：SQLite `~/.omp/stats.db`（路径由 `getStatsDbPath()` 给出，`stats/src/db.ts:135`；README 同）。
  - 表：`messages`（逐次模型调用：folder, model, provider, api, timestamp, duration, ttft, stop_reason, error_message, input/output/cache_read/cache_write/total_tokens, premium_requests, cost_input/output/cache_read/cache_write/total, cost_no_cache_input, cost_unpriced, agent_type, service_tier）。
  - 表：`file_offsets`、`user_messages`（字数、大小写、脏话等"挫败感"特征）、`frustration_verdicts`、`tool_calls`（tool_name, args_chars, result_chars, is_error, calls_in_turn）、`meta`。
  - 另有按小时的 `message_rollup / tool_rollup / session_rollup`，由触发器标脏并后台重算，范围查询毫秒级。
- **指标**：tokens/s、缓存命中率、缓存节省、错误率、API 等价费用估算（订阅型模型用公开 API 价格估算，没有公开价格则记 N/A）、平均延迟、TTFT。
- **HTTP API**：`GET /api/stats`、`/api/stats/models`、`/api/stats/folders`、`/api/stats/timeseries`、`/api/events`（SSE：同步进度）、`/api/status`、`POST /api/sync`。面板页面有 Overview / Models / Providers / Costs / Requests / Errors / Traces / Tools / Frustration / Projects / Gain。
- **可编程**：`import { getDashboardStats, syncAllSessions } from "@oh-my-pi/omp-stats"`。
- **复用方式**：
  - 数据源就是会话 JSONL。Oryn 在 orb 外只需汇集会话文件（或消费 RPC 的 `message_end`）。
  - 可直接复用 `stats` 的解析器与聚合器库，或参照其 schema 建自己的汇总表。
  - `packages/coding-agent` 的 `trace` 视图（`/trace`）和 HTML 导出共用同一套 span 组装。

### 12.4 `omp usage`（供应商额度，不是会话统计）

- `omp usage [invalidate|clients|accounts|reset]`，旗标 `-j/--json`、`-p/--provider`、`-r/--redact`、`--history`、`-d/--days`（默认 7）、`-e/--extension`、`--no-extensions`。
- 作用：显示每个已认证账号的**供应商用量限额与重置窗口**。
  - `--history` 看每小时快照趋势。
  - `usage clients --days N` 按客户端（机器 / 应用）显示 token 消耗，数据来自 broker 的 `/v1/usage/clients`（由 `x-omp-install-id / x-omp-hostname / x-omp-app` 归因）。
  - `usage accounts` 列 OAuth 账号 identityKey。
  - `usage reset` 管理 Codex / Claude 已保存的重置。
- broker / gateway 的 `/v1/usage*`：服务端缓存 5 分钟（±25% 抖动），客户端 15 秒单飞缓存；最近一次成功值保留 24 小时。
- **结论**：用 gateway 的 `x-omp-*` 归因头是实现"按 orb / 用户 / 线程统计 token"最省力的路径 [推测：需自行在 orb 内给请求加这些头，当前源码里归因头来自客户端配置，未核对具体设置名]。

---

## 对 Oryn 的启示

**值得复刻**
1. 每个线程一个 orb 内的 `omp --mode rpc-ui --no-ui`，Oryn 后端做 JSONL 到 WebSocket/SSE 的桥接，客户端消费完整事件流。这是仓库自己（robomp、metaharness）验证过的形态。
2. 先发 `negotiate_protocol` v2，用 `set_event_filter{messageUpdates:"delta"}` 降带宽，并在服务端累积 `message_end`，不要依赖 `agent_end.messages`。
3. 用 `open_session{sessionDir}` 做"预热进程池 + 线程绑定"，每个线程一个固定的 `--session-dir`。
4. 用 `prompt_result` 判断本轮结束，用 `session_settled` / `get_state.isSettled` 判断可以暂停 / 回收 orb。
5. 渲染覆盖面以 `event-controller.ts` 的"每种事件必有处理器"和 `collab-web/src/tool-render` 的 React 渲染器为蓝本，渲染器必须容忍部分或畸形 `details`。
6. 用 `set_ask_dialog{true}` 获得结构化 `ask` 表单；审批用 `select` 回传标签字符串；`ask` 超时时取 `recommended`。
7. 用 `set_host_tools` / `set_host_uri_schemes` 把 Portal、Space 文件、浏览器 Profile 等 orb 外能力暴露给 agent。
8. 会话迁移：`omp session pack/unpack/moved/lease` 是官方的跨机器迁移原语（含 artifacts 与 blobs）；用 `lease` 在迁移前防止双写。
9. 凭据：orb 内不放任何真实密钥，`models.yml` 的 `baseUrl` 指向 auth-gateway，用 `--clients` 做按 orb 的模型与账号授权。
10. 统计：直接复用 `omp-stats` 的 SQLite schema 思路（`messages / tool_calls` 加小时 rollup），数据源取会话 JSONL 或 RPC 的 `message_end`；实时用量取 `get_session_stats` 和 `subagent_progress`。
11. 实时多端同步：自建 relay，复用 `pi-wire` 的信封与房间语义和 `local-relay.ts` 的 100 行契约（加账号体系），事件来源改为 RPC。

**容易踩的坑**
1. 17.4.1 与 18.15.0 差异大，本机安装版缺少 `open_session`、`get_session_lease`、`session` 子命令、v2 分块等。必须锁定并显式校验 `ready.supportedProtocolVersions`。
2. 关闭 stdin 后不继续读 stdout 会让进程无法退出；背压会让服务端写临时文件，需要监控磁盘。
3. v1 下超大帧被截断，`agent_end.messages` 不可信。
4. RPC 下没有交互式 PTY（`rpc-ui` 强制 `PI_NO_PTY=1`），用户终端必须自建。`bash` 命令是一次性非 PTY；命名服务的 PTY 属于 broker 而不是 RPC。
5. RPC 没有 plan 审批 handler、artifact 读取命令、子 agent revive 命令，需要自己补，或通过 host URI 与文件系统旁路。
6. ACP 的事件映射太窄，不能当作"完整展示"的传输。
7. collab 的 host 只能是交互式 TUI，生产 relay 不可自托管，持链接即授权，不适合做多租户鉴权基础。
8. `omp auth export/import` 会让轮换 refresh token 的 provider 在原机器被登出，且 broker 模式下被拒绝；优先用 broker + gateway。
9. `--no-ui` 下需要审批的工具会 fail-closed；子 agent 固定以 yolo 无头运行，但遇到 `prompt` 策略会被拒绝。
10. `OMP_DAEMON_IDLE_GRACE_MS` 默认 3 秒，agent 启动的命名服务会随 RPC 进程退出而被杀；需要常驻时要调大或用 detached 模式。
11. 官方 Docker 基座是 Bun + 源码而非单文件二进制；单文件二进制需从 `build.stencil.so` 取，natives 首次运行解压到 `~/.omp/natives/<version>`，要预热或挂载以免冷启动变慢。
12. XDG 路径只有在对应 `omp` 目录已存在时才生效；profile 与 `PI_CODING_AGENT_DIR` 的交互也有细节（命名 profile 会忽略后者），orb 内配置路径要先固定。
13. 多个 omp 进程写同一会话时，后来者会静默 fork 出新文件（有 `notice` 提示），线程到会话的映射不能假设一对一；迁移前先 `lease`。
14. 17.4.1 的安装包里没有 docs 目录，文档以源码仓库为准。

---

### Critical Files for Implementation

- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/modes/rpc/wire/commands.ts` — 全部 RPC 命令的参数与结果；配合同目录 `events.ts`、`frames.ts`、`state.ts` 是协议权威。
- `/root/Projects/GitHub/oh-my-pi/docs/rpc.md` — 传输、分块、`prompt_result` 与 `session_settled` 语义、扩展 UI、宿主工具与 URI、客户端库的权威说明。
- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/modes/controllers/event-controller.ts` — TUI 对每种事件的处理表（`:392-473`），GUI 的"完整性清单"。
- `/root/Projects/GitHub/oh-my-pi/packages/wire/src/index.ts` — collab 帧、信封、链接常量、`AgentProgress` 与会话条目类型，自建 relay 与同步的蓝本；配合 `packages/collab-web/scripts/local-relay.ts`。
- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/session/session-transfer.ts` — 会话打包、解包、`moved` 标记与租约的实现，跨机器迁移的依据。

（另：托管运行参考见 `/root/Projects/GitHub/oh-my-pi/python/robomp/README.md` 和 `Dockerfile.robomp`；统计参考见 `/root/Projects/GitHub/oh-my-pi/packages/stats/src/db.ts`。）
