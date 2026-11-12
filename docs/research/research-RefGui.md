# oh-my-pi-gui（nornzach）调研报告：对 Oryn 的启示

范围说明：所有结论来自对 `raw.githubusercontent.com/nornzach/oh-my-pi-gui/main` 的只读抓取，以及对本机上游源码的只读 grep。不确定处标 `[推测]`。

两个先决事实：
- 本机上游 `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/package.json:3` 是 **18.15.0**，而 GUI 绑定的 sidecar 是 monorepo `4b67dcb3`（上游 18.19.0，见 `README.md` 与 `sidecar-source.json`）。下文「fork 独有」是相对 **18.15.0** 判断的，18.16–18.19 里可能已有部分命令，未核实。
- 本机上游 `rpc-ui`、`set_event_filter`（含 `messageUpdates:"delta"`）、`set_ask_dialog`、`negotiate_protocol` v2 chunk、`subagent_*` 帧、`live_*`、`dictation_*`、`btw_*` 都已存在（`packages/coding-agent/src/modes/rpc/wire/commands.ts`、`docs/rpc.md:1-123`）。

---

## 1. 技术栈与目录结构

**来源：** `package.json`、`AGENTS.md`、`plan/README.md`、仓库根目录树。

- **版本与壳**
  - 包名 `@oh-my-pi/omp-gui` v0.9.42，`"main": "./out/main/index.js"`。
  - `devDependencies.electron` 是 **36.9.5**，README 徽章仍写 Electron 35，二者不一致。
  - 构建用 `electron-vite ^3`，打包用 `electron-builder ^26`，更新用 `electron-updater`。
  - 日志与存储：`electron-log`、`electron-store`（prefs）。
- **渲染栈**
  - React 19、Tailwind 4（`@tailwindcss/vite`）。
  - 状态管理用 **zustand 5**，按 tab 作用域化（见下）。
  - `react-markdown` + `remark-gfm` + `remark-math` + `rehype-katex` + `rehype-raw` + `rehype-sanitize`，加自写的 remark/rehype 插件。
  - 高亮用 `highlight.js`；图表用 `mermaid`（动态 import）和 `chart.js` + `react-chartjs-2`。
  - 列表虚拟化用 `@tanstack/react-virtual`；拖拽用 `@dnd-kit`；表单用 `react-hook-form` + `zod`。
- **终端**：`@xterm/xterm ^5.5` + `@xterm/addon-fit`。依赖里**没有 node-pty**。
- **编辑器 / diff**
  - 编辑器是 CodeMirror 6（`@codemirror/view`、`commands`、`lang-json`、`lang-markdown`），只用于设置里的小编辑器。
  - **没有 Monaco**。diff 用 `diff ^9` 加自写渲染 `src/renderer/lib/diff.tsx`（19.6KB）。
- **测试**
  - 单测用 vitest 3，渲染测试用 **linkedom** 做 DOM 垫片，不用 jsdom（`AGENTS.md` 的 Code Conventions，范例是 `ThinkingBlock.test.tsx`）。
  - e2e 用 Playwright，位于 `e2e/`：`desktop.e2e.ts`、`deep-audit.e2e.ts`、`real-core.e2e.ts`、`runtime.e2e.ts`、`performance.e2e.ts`、`auto-follow.e2e.ts`，以及 `sidecar-fixture.ts`（假 sidecar）。
  - 校验脚本：`check:types`、`check:protocol`（`scripts/check-protocol.ts`）、`gen:types`（实为**检查器**，不是生成器，见 `PLAN.md` 审计注记）、`scripts/lint-surfaces.mjs`。
  - 测试约束：zustand store 在 `afterEach` 用 `reset()` 复位，禁用 `mock.module()`。
- **目录职责**（`src/main/` 的完整清单见目录页）
  - `src/main/`（约 80 文件）：
    - `index.ts`：应用生命周期、`resolveBundledOmp`。
    - `sidecar.ts`：单个 sidecar 生命周期。
    - `sidecar-pool.ts`：每 tab 一个 sidecar 的池，加事件路由。
    - `rpc-bridge.ts`：NDJSON + v2 chunk 重组。
    - `event-batcher.ts`：16ms 批处理。
    - `ipc.ts`（59KB）：全部 IPC 处理器。
    - `browser-host.ts`（39KB）：内置浏览器。
    - `session-index.ts`、`conversation-index.ts`：会话索引与 FTS 搜索。
    - `scheduler/*`：定时任务。
    - `stats-server.ts`、`stats-client.ts`。
    - `tab-layout.ts`、`window.ts`、`tray.ts`、`updater.ts`、`menu.ts`、`deep-link.ts`、`shell-env.ts`、`proxy-env.ts`、`custom-themes.ts`、`models-config.ts`、`plugin-catalog.ts`。
  - `src/preload/index.ts`（23KB）：`contextBridge.exposeInMainWorld("omp", …)`，暴露类型化 `OmpApi`；所有命令走 `ipcRenderer.invoke(IPC_COMMANDS.RPC_COMMAND, {command, timeoutMs})`。
  - `src/shared/`：
    - `rpc-types.ts`（88KB）：手写的线协议类型镜像，注释写明不依赖 `@oh-my-pi/*`。
    - `ipc-types.ts`（67KB）：`IPC_EVENTS` / `IPC_COMMANDS` 通道表与 `OmpApi`。
    - `rpc-client.ts`：preload 与渲染端共用的会话 RPC 适配器。
    - `launch-profile.ts`、`schedule.ts`、`scheduler-types.ts`、`stats-types.ts`、`chord.ts`、`hotkeys.ts`。
  - `src/renderer/`：
    - `components/{agents,chat,common,dialogs,layout,memory,panels,settings,stats,tasks,tools}`。
    - `stores/`：zustand store，约 50 个文件。
    - `hooks/`、`lib/`（含 `themes.ts` 84KB）、`locales/{en,zh,ru}.ts`。
- **安全基线**（`src/main/window.ts:142-148`，`index.html` CSP）
  - `contextIsolation:true`、`nodeIntegration:false`、`sandbox:true`。
  - CSP：`script-src 'self'; connect-src 'self'; …`，渲染端不能直连网络。
  - 所有 FS、网络、子进程都在主进程。

---

## 2. 进程模型

**核心文件：** `src/main/sidecar.ts`（862 行）、`src/main/sidecar-pool.ts`、`src/main/rpc-bridge.ts`、`src/shared/rpc-client.ts`。

- **启动命令**（`sidecar.ts` `#spawn`）：
  - 固定参数 `["--mode","rpc-ui"]`。
  - 条件参数：
    - `--session <path>`（恢复会话）。
    - `--no-auto-resume`（全新 tab）。
    - `--model @default`（保存的模型无法恢复时的回退）。
    - `--chat`（无工具纯对话会话）。
    - 定时任务的 `runPolicy.flags`。
    - 用户 `extraFlags` 加 launch profile，经 `stripDenylistedFlags` 过滤，不能覆盖代码控制的参数。
  - 环境变量：`PI_RPC_EMIT_TITLE=1`、`PI_NO_PTY=1`、`PI_NOTIFICATIONS=off`，另外合并登录 shell 的 PATH（`shell-env.ts`）和代理环境（`proxy-env.ts`）。
  - 二进制来源：`index.ts` 的 `resolveBundledOmp()` 只接受内置二进制 `resources/omp`（`bun build --compile` 产物，约 120MB），**不回退到系统 omp**。
  - 开发覆盖：`OMP_BUNDLED_OMP` 指定二进制；`OMP_SIDECAR=source` 加 `OMP_SIDECAR_CLI` 用 bun 跑源码。
- **每 tab 一个 sidecar**：`SidecarPool` 里 `PoolEntry{sidecar, tabId, win, kind, worktree, status, running, sessionFile…}`。
  - 没有 tab 数量上限；只有 tab 被显示时才 spawn，初始状态是 `asleep`。
  - 完整事件流只转发给每窗口至多 2 个**可见** tab（分屏）；所有 tab 都推轻量 `TAB_STATUS`。
  - `#sessionOwners`（F-OWN）：防止同一 session 文件被双重 attach；`spawnTabForWindow` 返回 `refusal: "owned" | "kind-mismatch"`。
  - `#requestOwners`（F-UI-ORIGIN）：`extension_ui` / `host_tool` / `host_uri` 请求的响应，路由回**发起请求的 sidecar**，即使用户已切到别的 tab。
  - 暖备用（gui#29）：每个 workspace 预启动一个空闲 sidecar，新 tab 用 `switch_session` 接管，冷启动需要数秒，接管不到 1 秒。
- **JSONL 协议客户端**：
  - 分帧与 chunk 重组在 `src/main/rpc-bridge.ts`（`attachNdjsonParser`、`ChunkReassembler`）。
  - 限制：`RPC_MAX_FRAME_BYTES=1048576`，`RPC_MAX_REASSEMBLED_BYTES=67108864`，chunk 载荷 262144。
  - 只有 ready 帧通告 `supportedProtocolVersions∋2` 且上述两个限制完全相等，才发 `negotiate_protocol` v2。
  - 命令 id 关联与超时在 `src/main/rpc-client.ts`（`RpcClient`），超时表在 `src/shared/rpc-client.ts`：`bash/eval/compact/handoff/login/plan_approval/switch_leaf = 660s`，`export_html = 120s`，默认约 8s。
  - 帧路由在 `SidecarManager.#routeFrame`：
    - `ready`、`response`。
    - `extension_ui_request`、`host_tool_call`、`host_uri_request`。
    - `subagent_*`、`available_commands_update`、`model_catalog_update`、`config_update`。
    - `prompt_result`、`session_settled`、`command_output`、`session_info_update`、`extension_error`。
    - `LIVE_FRAME_TYPES`、`DICTATION_FRAME_TYPES`、`TERMINAL_FRAME_TYPES`、`BTW_FRAME_TYPES`。
    - `AGENT_EVENT_TYPES` 里的事件进 `EventBatcher`。
  - ready 后自动发三条命令：
    - `set_event_filter{events:[…],messageUpdates:"delta"}`。
    - `set_ask_dialog{enabled:true,form:true}`。
    - `terminal_configure{interactiveCommands:true,agentTools:true}`。
- **崩溃、重启、恢复**：
  - `MAX_RESTART_ATTEMPTS=3`，`RESTART_DELAYS=[1000,2000,4000]`ms；`exit 0` 视为正常关闭，不重启。
  - spawn 周期用 `#generation` 计数器防止「已死 child」误报 `ready`（见 `sidecar.ts` 注释）。
  - 每次 spawn 保留 stderr 尾部 20 行，`reportFailure` 写崩溃报告。
  - 保存的模型无法恢复时（`parseModelRestoreFailure`），额外用 `--model @default` 重启一次，不计入崩溃预算。
  - 重启时用 `#resumeSessionPath` 经 `--session` 恢复原会话。
  - `restart(cwd?, resumeSessionPath?, {fresh})` 用于切换项目。
  - 状态机：`asleep → starting → ready → restarting → error | exited`（`SidecarStatus`）。
  - 另有 15s 心跳探活（`use-rpc-events.ts` 的 `heartbeatMs`）。
- **相关子进程**：
  - 统计服务：同一内置二进制 `omp stats --host 127.0.0.1 --port 0 --no-open`（`stats-server.ts`），带自己的重启预算。
  - 定时任务：每次运行起一个无头 sidecar，用 `worktree_create` 建 worktree 后 `sidecar.restart(created.path)`（`scheduler/runner.ts`）。
- **串行约束**（`plan/01-architecture.md`）：RPC 命令按序执行，只有 `bash` 和侧通道帧（`extension_ui_response`、`host_tool_*`、`host_uri_result`）绕过队列。

---

## 3. 对 fork 的依赖

**依据：** `src/shared/rpc-types.ts:21-290` 的 `RpcCommand` union，对照本机上游 18.15.0 的 `wire/commands.ts` 与 `rpc-mode.ts`。

**上游已有、GUI 直接用：**
- `rpc-ui` 模式、`negotiate_protocol`、`set_event_filter`（含 delta）。
- `set_ask_dialog`、`prompt`、`abort`、`switch_session`、`branch`、`fork`、`set_model`。
- `set_subagent_subscription`、`get_subagents`、`get_subagent_messages`、`set_host_tools`、`set_host_uri_schemes`。
- `live_*`、`dictation_*`、`btw*`、`get_messages_page`、`get_entries`。

**上游 18.15.0 里没有找到、GUI 在用的命令（fork 独有，或在 18.16–18.19 新增）。** 这是 `RpcCommand` union 与上游 grep 的差集；`set_settings` 一类未逐个核实。

| 类别 | 命令 / 事件 | 用途 |
|---|---|---|
| 终端 | `terminal_open/_input/_resize/_close/_list/_snapshot/_configure`、`terminal_run_cli`、`tui_open`；帧 `terminal_opened/_output/_exit/_closed` | sidecar 内 PTY 托管，GUI 的 xterm 只负责显示 |
| 浏览器 | `browser_host_start/_stop` | sidecar 起私有 relay，返回 `extensionUrl`（`RpcBrowserHostInfo`） |
| Git / worktree / PR | `get_git_status/_changes/_diff`、`worktree_create/_remove`、`pr_repo/_list/_get/_diff/_draft/_create/_checkout` | worktree 默认分支 `omp/gui/<name>`，位于 `~/.omp/wt/gui-<name>-<hash7>`（`rpc-types.ts` 注释） |
| 设置 / 目录 | `get_settings_schema`、`get_settings`、`set_setting`、`get_providers`、`get_usage` | 通用设置读写 |
| 会话 | `get_transcript(_page)`、`get_session_tree`、`switch_leaf`、`fork_from`、`set_session_name`、`set_session_pinned`、`set_entry_label`、`move_session`、`get_directories/add_directory/remove_directory`、`export_html`、`share_session`、`preview_share_session` | 对应 `plan/16` 里的 A 批命令 |
| 队列 | `get_queue`、`queue_edit/_remove/_move/_clear`、`dequeue` | 稳定 id 的队列管理 |
| 子 agent | `abort_subagent`、`revive_subagent` | Agent Hub 动作 |
| 域管理 | `get_skills/_skill_detail`、`manage_skill`、`get_hooks`、`set_hook_enabled`、`get_mcp_servers`、`mcp_action/_add/_test/_reauth`、`get_plugins`、`set_plugin_enabled/_features/_setting`、`marketplace_action`、`get_gui_themes`、`get_prompt_templates`、`get_memory_*`、`edit_memory` | 管理面 |
| 安全 / SSH | `security_start/_cancel/_validate/_set_disposition`、`ssh_manage`、`ssh_test` | |
| 模式 | `set_plan_mode`、`plan_approval`、`set_goal`、`set_vibe_mode`、`set_loop_mode`、`tan`、`omfg`、`set_prewalk`、`fresh`、`shake_context`、`reload_plugins`、`set_force_tool`、`retry`、`clear_context`、`write_local_paste` | |
| 协作 | `collab_start/_join/_leave`、`get_collab_state`；事件 `collab_state` | relay 协作 |
| 其他 | `host_dictation_start/_audio/_stop/_cancel`、`synthesize_speech`、`get_command_arg_completions`、`list/import_foreign_session`、`debug`、`get_login_providers`、`set_api_key`、`get_omp_update`、`get_context_report`、`get_active_tools`、`get_jobs` | |

- `set_ask_dialog` 的 `form?: true` 参数上游 18.15.0 没有（上游参数仅 `{enabled}`）。
- 事件侧：`AGENT_EVENT_TYPES` 里出现的 `ttsr_triggered`、`todo_reminder`、`todo_auto_clear`、`irc_message`、`plan_proposal`、`queue_update`、`loop_mode_update`、`goal_updated`、`collab_state`、`retry_fallback_*`、`notice` 等，未逐个与上游事件表核对。
- 能否只用上游 omp 工作？**不能完整工作。** 终端面板、内置浏览器、worktree / PR、设置页、会话树、队列、插件市场、协作都依赖上述命令。
  - 降级行为：个别特性做了「命令不存在则报错」式探测（`plan/16` §1.2：`SessionTreeDialog` 有 feature-detect）。`browser-host.ts:980` 明确容忍旧 sidecar：「An older sidecar without the command simply runs its own browser」。
  - 协议对账：`rpc-types.ts` 手写镜像，`check:protocol` 和 `gen:types --check` 做结构检查；wire 类型改动要在 monorepo（`rpc-types.ts`）与 GUI 两侧**手改两遍**（`PLAN.md` 仓库与发布流程表）。
  - 推论 `[推测]`：上游 18.15.0 + 上游 `rpc-ui` 可以跑出「聊天 + 工具渲染 + 子 agent + 询问对话框」的最小子集。
- 同步流程：`scripts/sync-upstream.sh`，依次执行 fetch upstream、merge、`bun install`、pi_natives 重新提供、`gen:stats`、`build:omp`、GUI build + tests（`AGENTS.md`）。fork 改动尽量落在新文件（`rpc-session-extra.ts`、`rpc-foreign.ts`、`rpc-agents.ts`、`rpc-paste.ts`，见 `plan/16` §5），以减少合并冲突。

---

## 4. 事件 → UI

**事件入口（renderer）：**
- `src/renderer/hooks/use-rpc-events.ts`（37KB）。订阅 `window.omp.events.onTabBatch(reduceEvents)`，在 `withSessionRuntime(tabId, …)` 内先调
  - `useMessagesStore.getState().applyEvents(events)`
  - `useToolsStore.getState().applyEvents(events)`
- 再对每个事件做 switch，覆盖 `agent_start/_end`、`collab_state`、`goal_updated`、`queue_update`、`plan_proposal`、`notice` 等，更新 session、model、todo、queue、plan-approval、subagents、btw 等 store。
- 丢弃规则：`switchPending` 期间丢事件；`isTabClosed(tabId)` 的迟到帧丢弃；`agent_end.isTerminal === false` 的中间收尾不触发通知。
- 主进程批处理：`EventBatcher` 按 16ms 批量发送；不丢 `message_update`，但在积压 > 1000 时丢中间的 `tool_execution_update`（`plan/01-architecture.md`）。

**消息与流式状态：** `src/renderer/stores/messages.ts`
- 状态字段：`messages`（已提交）、`liveMessages`（本地回显加已送达但未提交的）、`streamingMessage`、`streamingText`、`streamingThinking`、`totalMessages`、`nextCursor`、`needsTranscriptSync`。
- 事件处理：
  - `message_start`：只有 assistant 消息流式。
  - `message_update`：按 `assistantMessageEvent` 的 `text_delta` / `thinking_delta` 累加字符串。
  - `message_end`：并入 `liveMessages`。
  - `agent_end`：用带 `entryId` 的消息 upsert 提交，未覆盖的 live 行保留并置 `needsTranscriptSync`。
- 去重：用 `messageIdentity` / `sameMessageContent` 对本地回显（`optimistic`）和落盘消息做匹配。
- 水合：`session-hydration.ts` 用 `get_transcript_page`（分页 256 条，失败回退 `get_transcript`），结果经 `mergeFetchedTranscript` 合并。
- store 以 `createScopedStoreHook` 按 tab 作用域化（`session-runtime-context.tsx`、`tab-runtime.ts`），每个 tab 一套 store。

**工具状态：** `src/renderer/stores/tools.ts`
- `ToolEntry{toolName,args,status:"pending|running|done|error|aborted",partialResult,streamingArgs,result,isError,startTime,endTime}`。
- 提供商 tool call id 只保证消息内唯一，所以对重复 id 生成「出现次数」键（`allocateEntryKey`）。delta 模式下帧不带快照，所以未命名的流式调用先按流位置键存，`toolcall_end` 时再改名。

**工具渲染器注册表**（`src/renderer/components/tools/index.tsx` 的 `REGISTRY`，未命中回退 `GenericRenderer`）：

| 工具名 | 渲染器 |
|---|---|
| `read` | `ReadRenderer`、`ReadGroupCard`（连续 read 合并） |
| `edit` / `apply_patch` | `EditRenderer`（diff 视图，用 `lib/diff.tsx`） |
| `ast_edit` / `ast_grep` | `AstEditRenderer` / `AstGrepRenderer` |
| `write` | `WriteRendererOrDevice`（`write xd://<tool>` 设备调用按内部工具渲染，见 `lib/xdev.ts`） |
| `bash` | `BashRenderer`（ANSI 走 `lib/ansi.tsx`） |
| `grep` / `find` / `glob` | `GrepRenderer` / `FindRenderer` / `GlobRenderer` |
| `task`（子 agent） | `TaskRenderer`（31KB） |
| `todo` / `todowrite` / `todo_write` / `set_todos` | `TodoRenderer` |
| `eval` / `browser` / `debug` / `lsp` | `EvalRenderer` / `BrowserRenderer` / `DebugRenderer` / `LspRenderer` |
| `github` / `gh` | `GithubRenderer` |
| `hub`（旧版）/ `wait` | `HubRenderer` / `WaitRenderer`（在 `CoordinationRenderer.tsx`） |
| `ask` | `AskRenderer` + `ask-form.ts` |
| `computer` / `generate_image` | `ComputerRenderer` / `ImageRenderer` |
| `web_search` | `WebSearchRenderer` |
| `goal` | `GoalRenderer` |
| `vibe_*` | `VibeRenderer.tsx` |
| `retain` / `recall` / `reflect` / `memory_edit` | `MemoryRenderer` |
| `resolve` / `reject`（plan 解析） | `ResolveRenderer` |
| `checkpoint` / `rewind` / `context_notes` / `new_context` / `learn` / `manage_skill` / `security_scan` / `ida` | `OpsRenderers.tsx` |
| `schedule_task` / `manage_scheduled_tasks` | `ScheduleTaskRenderer` / `ManageTasksRenderer` |

- 头部摘要由同文件 `SUMMARIES` 表计算，只依赖调用参数（结果可能尚未到达）。预览高度统一走 `lib/preview.ts` 的分档，不另写 `max-h-*`（`AGENTS.md`）。
- **`agent://`、`proc://`**：这两个 URI 没有找到专门渲染器文件。README 称「review `agent://` messages and process controls in compact structured cards」，对应卡片在 `CoordinationRenderer.tsx` 的 `WaitRenderer` 与 `HubRenderer`。具体 `agent://` 解析代码未逐行读。
- **plan**：`PlanApprovalDialog`（`dialogs/`）+ `stores/plan-approval.ts` + `plan_approval` RPC；选项 `execute | compact | keep_context | save`。`fs:read-plan` IPC 读取计划文档。

**Thinking / 流式 markdown / mermaid / 图片 / 子 agent：**
- Thinking：`chat/ThinkingBlock.tsx`
  - 默认折叠，带字数 / 行数、时长徽章、思考等级强调色（`--omp-thinking-*`）。
  - 实时 tok/s 速度计（`SpeedTracker`）。
  - 偏好 `hideThinkingBlock` / `proseOnlyThinking`（后者把围栏代码替换成 `...`，对齐 TUI）。
  - 订阅 `streamingThinking`，已完成的块不订阅实时缓冲，避免每个 delta 重渲染所有历史块。
- 流式 markdown：`lib/streaming-markdown.ts`（11KB）+ `components/chat/StreamingText.tsx` + `hooks/use-throttled-text.ts`。
- Markdown：`lib/markdown.tsx` 是**唯一**路径，rehype-sanitize schema 在此；禁止对模型文本用 `dangerouslySetInnerHTML`（`AGENTS.md`）。
- Mermaid：`chat/MermaidBlock.tsx`
  - 动态 import；`securityLevel:"strict"`。
  - 渲染后再做一次 SVG 清理（去 `script/iframe/object/embed/link/meta`、内联事件、`javascript:`）。
  - 按 `${theme}:${fnv(source)}` 缓存 SVG（上限 50 条）；解析失败回退 `CodeBlock`。
- 图片：`ImageRenderer`、`ResultImages.tsx`、`common/ImageViewer`（`ZoomableImage`）；本地图片经 `fs:read-image` 以 data URL 提供（带 MIME 嗅探和大小上限）。
- 子 agent 嵌套：
  - 行内：`TaskRenderer` 渲染 live 树（每 agent 统计、当前工具、重试徽章、嵌套 task 树，`MAX_NESTED_DEPTH`、yields 与 review findings）。
  - Agents 抽屉：`stores/subagents.ts` 消费 `subagent_lifecycle/progress/event` 帧；订阅级别固定 `"progress"`，因为 `"events"` 级别每个 token 一个快照，会淹没 stdout（gui#19）。`refresh` 用 `get_subagents` **合并**而不是替换，避免完成态消失。
  - DAG / 转录：`panels/SubagentDag.tsx`、`subagent-graph.ts`、`SubagentTranscript.tsx`、`AgentHubWindow.tsx`；转录通过 `get_subagent_messages` 分页获取（`use-subagent-stream-poll.ts`）。

---

## 5. 终端与浏览器

### 终端：不用 node-pty，PTY 在 sidecar 里

- **证据：**
  - `package.json` 无 `node-pty`。
  - `src/renderer/stores/terminals.ts` 头注释：「Sidecar-hosted terminals, per tab」，只创建 xterm 视图。
  - 命令全是 RPC：`terminal_open{cols,rows,cwd}`、`terminal_input{terminalId,data}`、`terminal_resize`、`terminal_close`、`terminal_list`、`terminal_snapshot`。
- 输出帧：`terminal_opened{terminal:RpcTerminalInfo, reveal?}`、`terminal_output{terminalId,data,seq}`、`terminal_exit{exitCode?,cancelled}`、`terminal_closed`。
  - 这些帧在 `SidecarManager` 中**对所有 tab（含后台）**转发，输出不能丢。
  - `seq` 是该终端累计字符数。重载的窗口先拉 `terminal_snapshot`，再用 `seq` 去重衔接实时帧（`terminals.ts` 头注释）。
- `RpcTerminalInfo.kind`：`shell`（用户开的登录 shell）或 `command`（`pty:true` 的 bash 调用）。`state` 含 `starting | busy | input`；还有 `root`、`host`（ssh）、`cwd`、`title` 等字段（`TerminalPanel.tsx` 的 `TerminalChip`）。
- **`pty:true` 路由到 GUI**：
  - 上游行为：`rpc-ui` 模式会设 `PI_NO_PTY=1`（`main.ts:2010-2012`），`bash-pty-selection.ts` 据此关闭 PTY。
  - GUI 做法：ready 后发 `terminal_configure{interactiveCommands:true, agentTools:true}`。
    - `interactiveCommands`：等待输入的 agent 命令在面板终端里运行，用户可作答；`terminal_opened.reveal=true` 让 GUI 自动展开面板（`terminals.ts` 的 `reveal` 字段）。
    - `agentTools`：向 agent 暴露 `terminal` 工具，agent 可读终端内容，并把长驻命令（dev server）开到独立终端。
  - 这是 fork 对 sidecar 的改动，在上游不存在 `[推测]`（未读 fork 源码，只读了 GUI 侧）。
- xterm：scrollback 10000、`FitAddon`、主题取自 CSS 变量（`--omp-code-bg`、`--omp-ansi-*` 等）；视图实例放 store 而不是组件里，关抽屉不丢滚动历史。

### 内置浏览器：WebContentsView + 反向 relay 通道，CDP 经 `webContents.debugger`

- **文件：** `src/main/browser-host.ts`（39KB）、`browser-relay-protocol.ts`、`browser-keys.ts`、`src/renderer/components/panels/BrowserPanel.tsx`、`stores/browser.ts`、`shared/browser-address.ts`。
- 机制：
  1. GUI 对每个 tab 的 sidecar 发 `browser_host_start`，sidecar 起一个私有 **browser relay**（WebSocket），返回 `RpcBrowserHostInfo{extensionUrl}`。
  2. 主进程 `TabBrowser.link(extensionUrl)` 用 `new WebSocket(extensionUrl)` **拨出**，扮演 relay 的「浏览器扩展」角色。
  3. 握手发 `{t:"hello", userAgent, browserVersion, tabs, attachedTabIds, discardedTabsProtocol, instanceId}`。
  4. relay 发 `{t:"rpc", id, op:"attach|detach|send|createTab|removeTab|activateTab|group|ungroup"}`，`send` 带 `sessionId?`、`method`、`params`；GUI 用页面的 `webContents.debugger` 转发 **CDP**，回 `rpcResult`、`cdpEvent`、`detached`、`tabCreated/Updated/Removed`、`ping`。
  5. 断线指数重连：`RECONNECT_MIN_MS=250` → `RECONNECT_MAX_MS=5000`；`PING_INTERVAL_MS=20000`。
- 协议镜像自上游 `packages/coding-agent/src/tools/browser/relay/protocol.ts`（文件头注释）。本机上游 `tools/browser/relay/{bridge,daemon,extension-files,kind,probe,protocol,server}.ts` 和 `extension-assets/` 确实存在，说明上游已有「Chrome 扩展 + relay」方案，GUI 把 Electron 当成这个扩展。
- 页面模型：每 tab 一组 `WebContentsView`（`openedBy: "agent"|"user"`），全局共享 `persist:omp-browser` session。
  - 页面睡眠策略：非当前页屏外、后台节流；agent 操作期间页面保持唤醒（`AGENT_IDLE_MS=30000`），否则停在窗口右下角 1px 处；最小尺寸 `1280×800`。
  - 权限默认全拒，仅 `GRANTED_PERMISSIONS={clipboard-sanitized-write}`；UA 换成纯 Chrome UA。
- **登录态存储：** Electron `session.fromPartition("persist:omp-browser")`，即 Electron `userData` 目录下的持久分区（macOS 为 `~/Library/Application Support/omp/Partitions/omp-browser`，路径为按 Electron 惯例推断 `[推测]`）。所有 tab 共享，README 明确警告「the agent's pages act as you」。
- agent 仍可能用别的浏览器：调用里指定 `app:"relay"`、CDP URL / 可执行文件、`"own"/"webkit"/"chromium"`，或配置了 relay / `browser.cdpUrl` 时（README 的 Terminal & browser 行）。

---

## 6. 各功能的实现入口

| 功能 | 入口 |
|---|---|
| 文件 / 工作区面板 | `panels/FilesPanel.tsx`：**主进程 `node:fs`**，通道 `fs:list`（≤2000 文件、深度 8）/ `fs:read`（200KB 预览上限）/ `fs:read-image`，不经 sidecar（头注释）。`@mention` 通过 `window` 事件 `omp:insert-mention`。另有 `DiffPanel.tsx`、`RepositoryChanges.tsx`（`get_git_changes/_diff`）、`workspace-ui.tsx`、`dialogs/WorkspaceDialog.tsx`、`WorkspaceDirsDialog.tsx`（`add_directory/remove_directory`）。 |
| 附件 | `layout/InputArea.tsx`：拖拽图片 → `fileToImage` → `ImageContent[]`；非图片拖拽 → `window.omp.app.pathForFile(file)`（preload 的 `webUtils.getPathForFile`）→ `droppedFileMention` 生成 @路径；粘贴图片同路径。大段粘贴：`lib/paste-blobs.ts`（>10 行或 >1000 字符折叠成 `[Paste #N, +L lines]`，提交时展开；阈值 `paste.largeMenuThreshold` 默认 100 行出菜单；「存为文件」走 `write_local_paste` → `local://paste-N.md`）。 |
| 会话库（搜索 / 树 / 分叉） | `main/session-index.ts`（chokidar 监听 `~/.omp/agent/sessions/**.jsonl`，解析 256 字节标题槽 + 头部 + 32KB 尾部，LRU 4096）；`main/conversation-index.ts`（`node:sqlite` **FTS5 trigram** 派生缓存，逐消息命中并打开到该消息）；`dialogs/ConversationSearchDialog.tsx`、`SessionPickerDialog.tsx`、`SessionTreeDialog.tsx`（38KB）+ `lib/session-tree.ts`；`switch_leaf`/`branch`/`fork_from`；`ImportForeignDialog.tsx`（Claude/Codex 复制导入）。 |
| worktree tab | `main/tab-spawn.ts` + `sidecar-pool.ts` 的 `PoolEntry.worktree`（`IpcTabWorktree`）；`worktree_create/_remove` RPC；`dialogs/WorktreeDialog.tsx`、`WorktreeCloseDialog.tsx`；设计文档 `plan/20-git-worktrees.md`；PR 中心：`panels/pr/`、`PrCenterWindow.tsx`、`stores/pr-center.ts`。 |
| 分屏 | `layout/SplitWorkspace.tsx`，仅**两窗格**（`SplitAxis`/`SplitPlacement`：上下左右投放区）；`main/tab-layout.ts` 持久化；`IpcTabGroup` 在池中表示分屏对。设计文档 `plan/14-parallel-sessions.md`。 |
| 定时任务 | `main/scheduler/{host,scheduler,runner,policy,guards,store,notices,schedule-tool,manage-tasks-tool}.ts`；渲染端 `components/tasks/`、`stores/scheduler.ts`；`shared/schedule.ts`、`scheduler-types.ts`；`schedule_task` / `manage_scheduled_tasks` 工具由 host tool 注入。休眠错过的槽位只记「missed」，不补跑（README）。设计文档 `plan/23-scheduled-tasks.md`。 |
| Agent Hub | `panels/AgentHubWindow.tsx`（36KB）、`agent-hub-settings.ts`、`components/agents/`；`stores/agent-pages.ts`；设计文档 `plan/24-agents-view.md`。 |
| 设置 / Provider / 模型 | `settings/SettingsWindow.tsx`（65KB，设置表单由 `get_settings_schema` 驱动，`SchemaSettingRow.tsx`，附 `schema-zh.ts`、`schema-ru.ts` 本地化）；`ProvidersWindow.tsx`、`ProviderConfigDialog.tsx`（自定义 provider、拉取模型列表）；`ModelRolesWindow.tsx`；`dialogs/ModelPicker.tsx`；`main/models-config.ts`；`ApiKeyDialog.tsx`；`login`/`set_api_key` RPC。 |
| MCP | `panels/mcp/`、`ExtensionsPanel.tsx`；`mcp_action/_add/_test/_reauth`。 |
| 插件 / marketplace | `panels/plugins/`、`PluginsWindow.tsx`、`main/plugin-catalog.ts`、`shared/plugin-*.ts`、`stores/plugin-activation.ts`；见第 7 节。 |
| 主题 | `renderer/lib/themes.ts`（84KB，内置：`dark light titanium nord solarized paper dawn latte gruvbox frost matcha rosePine`）、`theme-generator.ts`、`custom-themes.ts`；用户主题 JSON 位于 **`~/.omp/gui/themes/*.json`**（`{name,description,extends,scheme,tokens}`，token 键可带或不带 `--omp-` 前缀，完整列表在 `THEME_TOKEN_KEYS`）；`main/custom-themes.ts` 监听文件变化；`ThemeEditorDialog.tsx`；「Follow the current task's terminal palette」选项让 agent 主题叠加覆盖对话区。 |
| i18n | `renderer/lib/i18n.tsx`：`useT()`、`translate()`；语言 `en | zh | ru`，存 `localStorage["omp.lang"]`，缺 key 回退 `en` 再回退 key 名；`locales/{en,zh,ru}.ts`，`locales.test.ts` 强制 en/zh key 一致；主进程另有 `main/i18n.ts`、`native-ru.ts`；设置 schema 另有按语言的整表翻译。 |
| 语音 | 听写：`lib/dictation.ts`、`layout/use-dictation.ts`、`DictationStrip.tsx` → `host_dictation_start/_audio{pcm16Base64}/_stop`（PCM16 mono **16kHz** little-endian，在渲染进程采集），回 `host_dictation_state/_text/_notice` 帧；实时语音：`dialogs/LiveVoiceDialog.tsx`、`lib/voice.ts`、`live-state.ts`、`live_start/_stop/_mute`；TTS：`synthesize_speech`。 |
| collab / relay | `dialogs/CollabDialog.tsx`：`collab_start{relayUrl?,view?}`、`collab_join{link}`、`collab_leave`、`get_collab_state`；`RpcCollabState{role,readOnly,participants}`；`collab_state` 事件带 `restored` 时重新水合；需要已配置的 relay 服务。relay 服务端实现不在 GUI 仓库里。 |
| 其他 | 命令面板 `dialogs/CommandPalette.tsx` + `lib/command-registry.ts`（73KB）；快捷键 `lib/keymap.ts`、`shared/chord.ts`、`hotkeys.ts`；托盘 `main/tray.ts`；统计 `components/stats/`、`stats-client.ts`；深链 `omp://` `main/deep-link.ts`；btw 侧问 `panels/BtwPanel.tsx`、`stores/btw.ts`；队列 `panels/QueuePanel.tsx`、`stores/queue.ts`；调试台 `dialogs/DebugConsoleDialog.tsx`（`debug` RPC）。 |

---

## 7. PLAN.md 与 AGENTS.md 要点

### PLAN.md

内容是「GUI 插件市场」开发计划，不是全局路线图。

- **目标：** 打开 → 浏览 → 选择 → 安装（信任确认）→ 自动激活（空闲重启）→ 立即可用，外加声明式 GUI 主题 token 插件。
- **约束：** 不引入第二套插件运行时。GUI 保持静态安全宿主（`contextIsolation` + `sandbox` + `script-src 'self'`）；插件逻辑全部在 sidecar，从 `~/.omp/plugins` 和项目插件根动态加载。
- **设计决策：**
  - D1：比较变更前后**实际会加载**的插件集合，任一侧含 `extensions/tools/hooks` → `restart-required`。
  - D2：重启绑定发起 mutation 的 tab，校验走 tab 定向 RPC。
  - D3：插件主题复用 agent-theme overlay，仅允许 `TRANSCRIPT_OVERLAY_VARS` 子集，冲突时 agent 优先。
  - D4：可执行插件的禁用和卸载同样要重启。
- **里程碑：**
  - M1 元数据 + 激活协议：P1 catalog 元数据透传、P2 插件卡片增强、P3 激活检测（`activation` 字段，`live | restart-required`）、P4 安装信任确认（实测修正为**所有安装都确认**，因为 catalog wire 不带 manifest）。
  - M2：P6 空闲自动重启（`pendingByTab` 按 tab + session 排队）、P7 重启后用 `get_plugins` 校验。
  - M3：P8 `get_gui_themes` + `PluginManifest.gui.theme`、P9 `validatePluginThemeTokens`（token 键子集 + 值形状正则 + `CSS.supports`）。
  - M4：P11 作者文档，位于 monorepo `docs/marketplace.md`。
- **v1 不做：** 官方预置 catalog、回滚、任意前端插件宿主（iframe / React）、npm catalog、细粒度权限、评分、多窗口同步重启、跨市场搜索、过期版本提醒。
- **风险与缓解：** 重启杀掉进行中回合 → busy 守卫 + `agent_end` 延迟激活；多窗口插件状态漂移 → v1 文档化提示，pool 级 restart 进 Backlog；插件主题伪装 UI → 仅 transcript token。
- **测试规范：** 契约测试打在 resolver / RPC 层，临时 HOME fixture；GUI 用 linkedom；禁止 source-grep 测试。

### AGENTS.md

- **三个仓库，禁止混淆：**
  - `nornzach/oh-my-pi-gui`：GUI 产品和发布，可 push。
  - `nornzach/oh-my-pi`：monorepo fork，agent / RPC 改动在此，可 push。
  - `can1357/oh-my-pi`：upstream，**永不 push**，只经 `sync-upstream.sh` 拉取。
  - GUI 仓库以嵌套 checkout 形式位于 `oh-my-pi/packages/gui/`，`scripts/build-bundled-omp.ts` 用相对路径 `../../coding-agent` 构建 sidecar；GUI 路径对 monorepo 是有意未跟踪的。
- **sidecar 与打包规则：**
  - `resources/omp*` 是 gitignored 构建产物（约 120MB），不手改。
  - 构建脚本：`build:omp`（arm64）、`build:omp:x64`、`build:omp:win`（`bun-windows-x64-baseline`）。
  - 脚本自动处理 `pi_natives` 原生插件；打包读取 `extraResources`，x64 必须用 `electron-builder.x64.yml`。
  - 打包后的 GUI **永不使用系统 omp**，缺失即报可操作错误。
- **发布手册（Phase 0–7）：** 每次发布必先上游同步，然后版本号 / CHANGELOG / README 中英同步，提交 `release(gui): vX.Y.Z`，打 tag `vX.Y.Z`，构建三平台 sidecar 并冒烟（`scripts/smoke-sidecar.mjs`），打包安装器，发布。
  - `latest-mac.yml` 必须覆盖两种架构，否则更新检查失效。
  - macOS 应用与 sidecar 只做 ad-hoc 签名、未公证；Windows 未签名。
  - Windows 与 Linux 由 CI 构建（`.github/workflows/release-windows.yml` / `release-linux.yml`，按 `sidecar-source.json` 的 monorepo pin 复现 sidecar）。
  - 站点 `site/` 通过 releases API 动态取版本，发布时无需改。
- **代码约定：**
  - 继承 monorepo TS 规范（无 `any`、无内联 import、`#private`、优先 Bun API）。
  - 所有用户可见字符串走 `useT()`，且 `en.ts` / `zh.ts` key 必须一致。
  - 模型文本只经 `MarkdownRenderer`，不使用 `dangerouslySetInnerHTML`。
  - 工具文本用 `lib/format.ts` 的清理助手（对齐 TUI 的 tab / 截断 / 路径缩短）。
  - 子进程输出必须过 `AnsiText`。
  - 无 AI 署名尾注。
  - 格式化：GUI 用 Biome；monorepo 文件用 `oxfmt` / `oxlint`（曾因 Biome `--write` 造成约 4k 行格式化噪音）。
- **已知限制**（`plan/README.md` 的 v1 Limitations，这是早期审计结论，现状很可能已被 fork 命令补上 `[推测]`）：Plan / Goal / Vibe / Loop 模式 handleTui-only；无通用 settings get/set RPC；setTheme 在 RPC 下报错；无 retry RPC；rpc 下无富 askDialog。
  - 当前已见 `set_plan_mode`、`get_settings`、`set_setting`、`retry`、`set_ask_dialog{form}` 等命令，说明这些限制已被 fork 解除。
- **README 声明的功能边界：**
  - SSH：`/ssh list|add|remove` 管理命令禁用。
  - 审批和扫描「不是 OS 沙箱」。
  - 分屏只有两窗格。
  - 定时任务只在 omp 运行时执行。
  - 协作需要 relay 服务。

---

## 8. Electron 强绑定部分，及可复用的架构思路

**Electron 强绑定：**
- `browser-host.ts`：`WebContentsView`、`webContents.debugger`、`session.fromPartition`、`desktopCapturer`；整套「屏外 / 唤醒」机制都是 Electron 特有的。
- `window.ts` / `menu.ts` / `tray.ts` / `updater.ts`（electron-updater）/ `shortcuts.ts`（全局快捷键）/ `deep-link.ts` / `app-quit.ts` / `quit-guard.ts` / `gpu-fallback.ts`、`titleBarOverlay`。
- preload `contextBridge` + `ipcRenderer.invoke`，以及 `webUtils.getPathForFile` 拿拖拽文件的本机路径。
- 主进程直读本机文件系统（`fs:*`、`session-index.ts`、`conversation-index.ts` 读 `~/.omp/agent/sessions`、`node:sqlite`）：这在「orb 在远端」时全部失效，必须改成经 orb 的文件 / 会话 API。
- 子进程由 GUI 主进程 spawn（`child_process.spawn`、`shell-env.ts`、`proxy-env.ts`）：与「每个线程一台云端机器」架构相反，进程管理要移到 orb 内。
- 渲染端的 `window.omp.*` API 与 `IPC_EVENTS` 通道是 Electron 绑定的，但它们**已按 `OmpApi` 接口抽象**，可替换成 WebSocket / HTTP 实现。

**与 Electron 无关、可整体或按思路复用：**
- **协议层：** 手写的 `RpcCommand` / 帧类型、v2 chunk 重组、`id` 关联与按命令的超时表。这是纯 TS，可直接跑在浏览器或 RN。
- **事件 → 状态 reducer：** `stores/messages.ts`、`tools.ts`、`subagents.ts`、`session-hydration.ts`，以及乐观回显去重、delta 模式下的 tool-call 键处理。它们不依赖 Electron，可移植到 Web / 移动。
- **工具渲染器注册表**（`components/tools/*`）：纯 React；移动端需要换 DOM / CSS 实现。
- **按 tab 作用域化的 store 运行时**（`session-runtime-context.tsx`）：多线程并行的核心抽象。
- **重连与恢复策略：** 水合 + 分页 transcript + 增量事件补偿，`terminal_snapshot` + `seq` 去重。这套模式天然适合「断线重连 / 多端同步」。
- **终端模型：** PTY 托管在 agent 侧、客户端只是 xterm 显示器，加快照 + `seq`，这与 orb 终端 / Portal 的需求一致。
- **浏览器 relay 协议**（`browser-relay-protocol.ts`）：协议本身与 Electron 无关；orb 里可以由真正的无头 Chromium 实现 relay 的另一端。
- **定时任务、worktree、PR 中心的 RPC 驱动方式**：逻辑在 sidecar 命令里，GUI 只是控制台。
- **安全默认：** 渲染端 CSP 禁止外连、模型文本强制 sanitize、mermaid SVG 二次清理。

---

## 对 Oryn 的启示

**值得复刻：**
- 把「一个 tab = 一个 agent 进程 = 一套 store」做成硬边界，orb 与线程一一对应时直接映射 `PoolEntry`（`sidecar-pool.ts`）。
- 后台 tab 只推轻量 `TAB_STATUS`，完整事件流只给可见 tab，对多端 / 移动端省流量很关键。
- PTY 放在 agent 侧（`terminal_*` RPC），客户端仅用 xterm 显示；`terminal_snapshot` + `seq` 去重是断线重连的正确做法。
- `set_event_filter{messageUpdates:"delta"}` 加 EventBatcher：避免全量快照的二次方流量，上游已支持，可直接用。
- 子 agent 订阅固定在 `"progress"` 级别，转录按需分页，防止淹没共享管道（gui#19）。
- 乐观回显与落盘消息用 `entryId` 加 identity 做合并，`needsTranscriptSync` 标记兜底：多端同步要有同类机制。
- `get_transcript_page` 分页水合，失败回退全量接口：两端协议升级可以平滑。
- 发起方路由（F-UI-ORIGIN）：审批 / 询问 / host tool 的响应要回到发起它的 agent，而不是当前活跃的那个；Oryn 里 orb 切换更频繁，必须有这层。
- session 文件所有权守卫（F-OWN）：防止同一线程被两个端同时附着，Oryn 多端场景更需要，最好升级成租约。
- 浏览器 relay 协议（`hello` / `rpc` / `cdpEvent`）：orb 内用无头 Chromium 实现 relay 的另一端，用户端只做观看和接管。
- 内置二进制 + 严格版本固定（`sidecar-source.json` pin monorepo commit）；协议类型手写镜像加结构检查脚本，避免漂移。
- 暖备用 sidecar（`adopt()`）：orb 冷启动更慢，预热池的收益更大。
- 主题 token 校验（只允许白名单变量 + `CSS.supports`）、插件「所有安装都确认」、可执行插件变更要求重启。
- 工具卡片的「头部摘要只依赖调用参数」设计：流式阶段结果未到也能渲染。
- i18n key 一致性由测试强制。

**容易踩的坑：**
- 手写协议镜像要双边手改，漏改不会报错（`success()` 的 `as RpcResponse` 强转曾掩盖缺失的 `fork` variant，见 `plan/16` §1.1），需要自动生成 + 校验。本机上游已有 `wire/rpc-wire.schema.json` 和 `rpc-wire.generated.ts`，Oryn 应直接基于它生成，不要再手写。
- fork 依赖重：GUI 约 100+ 命令里大半是 fork 独有，每次上游同步都要重新合并和重编，Oryn 应把这些能力做成**独立协议层**，而不是改 agent 核心。
- 命令串行队列（只有 `bash` 和侧通道帧绕过）：长命令会阻塞后续命令，所以超时表按命令设置；远端 orb 的 RTT 会放大这个问题，需要明确哪些命令可并行。
- `rpc-ui` 自动设 `PI_NO_PTY=1`，`pty:true` 的 bash 默认不工作，需要显式路由到 GUI 终端。
- 浏览器登录态是所有 tab 共享的全局分区，agent 以用户身份操作；Oryn 的「浏览器 Profile」需要按 orb / Space 隔离并明确授权。
- 渲染端 CSP 禁止外连，所以所有 I/O 都绕主进程；迁到 Web 后要重新设计网络边界与鉴权（relay 是 localhost 上的无鉴权 WebSocket `[推测]`）。
- 本地文件假设渗透很深（`session-index` 读磁盘、`fs:list` 用 `node:fs`、`pathForFile`、worktree 位于 `~/.omp/wt`），迁到远端 orb 需要整层 FS 抽象。
- 会话列表与全文检索（FTS5）建在本地 jsonl 上，多端需要服务端索引。
- 分屏只做了两窗格，tab 布局持久化是按窗口的本机状态，多端同步布局需要另设计。
- 定时任务运行在本机主进程，休眠即错过；云端 orb 方案应该把调度放服务端。
- 重启 sidecar 会杀掉进行中回合，插件变更要靠 busy 守卫延后；orb 内同理。
- 事件乱序与迟到帧（关闭的 tab、`switchPending`、`isTerminal:false` 的中间 `agent_end`）需要大量防御代码，复刻时先把这些边界列成测试清单。
- README 声称审批和扫描「不是 OS 沙箱」：Orb 才是真正的隔离边界，不要照搬这条说明。
- 文档与代码有漂移（Electron 35 / 36.9.5；`plan/` 里「42 个命令」与现在 100+），引用 `plan/` 时要对照代码。

---

## 实际读过的文件清单

**oh-my-pi-gui（经 raw.githubusercontent.com）：**
- `AGENTS.md`、`PLAN.md`、`README.md`（前约 300 行，共 818 行；根目录页还附带 README 全文，读到 300 行止）、`package.json`、`sidecar-source.json`
- `plan/16-agent-rpc-extensions.md`、`plan/01-architecture.md`、`plan/README.md`（目录页内嵌）
- `src/main/`：`sidecar.ts`（全文 862 行）、`sidecar-pool.ts`（1–200 行）、`rpc-bridge.ts`、`browser-relay-protocol.ts`、`browser-host.ts`（1–170 行及 grep）、`tab-spawn.ts`、`index.ts`（1–140 行）、`session-index.ts`（头部）、`conversation-index.ts`（头部）、`stats-server.ts`（grep）、`window.ts`（grep）、`ipc.ts`（grep）、`scheduler/runner.ts`（grep）
- `src/preload/index.ts`（1–120 行）
- `src/shared/rpc-client.ts`（1–140 行）、`rpc-types.ts`（1–420 行及 grep）、`ipc-types.ts`（grep）
- `src/renderer/`：`index.html`、`hooks/use-rpc-events.ts`（1–330 行）、`hooks/session-hydration.ts`（1–80 行）、`stores/messages.ts`（1–400 行）、`stores/tools.ts`（1–120 行）、`stores/subagents.ts`（1–90 行）、`stores/terminals.ts`（1–140 行）、`components/tools/index.tsx`（全文）、`components/tools/TaskRenderer.tsx`（1–90 行）、`components/chat/ThinkingBlock.tsx`、`components/chat/MermaidBlock.tsx`（头部）、`components/panels/TerminalPanel.tsx`（1–80 行）、`components/panels/FilesPanel.tsx`（1–70 行）、`components/dialogs/CollabDialog.tsx`（1–60 行）、`components/layout/SplitWorkspace.tsx`（1–40 行）、`components/layout/InputArea.tsx`（grep）、`lib/composer-submit.ts`（1–80 行）、`lib/paste-blobs.ts`（1–60 行）、`lib/i18n.tsx`（1–50 行）
- 目录列表页：`src`、`src/main`、`src/main/scheduler`、`src/preload`、`src/shared`、`src/renderer`、`renderer/{stores,lib,hooks}`、`components/{tools,chat,panels,layout,dialogs,settings}`、`plan/`、`docs/`、`e2e/`、仓库根

**本机上游 `/root/Projects/GitHub/oh-my-pi`（只读）：**
- `packages/coding-agent/package.json`（版本 18.15.0）
- `packages/coding-agent/src/main.ts`（grep `rpc-ui`、`PI_NO_PTY`）
- `packages/coding-agent/src/modes/rpc/{rpc-mode.ts, rpc-types.ts, rpc-client.ts, rpc-session-events.ts, rpc-subagents.ts, wire/commands.ts, wire/frames.ts, wire/rpc-wire.generated.ts}`（grep）
- `packages/coding-agent/src/tools/{bash-pty-selection.ts, browser/relay/*}`（glob / grep）
- `docs/rpc.md`（1–123 行）、`docs/cli-reference.md`（grep）

**未读：** fork 源码（`nornzach/oh-my-pi`）、`plan/07`、`plan/14`、`plan/23`、`plan/24` 全文，以及大部分 `src/main/ipc.ts`、`browser-host.ts` 后半。凡涉及 fork 实现细节的结论均来自 GUI 侧类型和调用点，已标 `[推测]`。

### Critical Files for Implementation

- `src/main/sidecar.ts` + `src/main/sidecar-pool.ts`：sidecar 生命周期、重启策略、按 tab 事件路由、F-OWN / F-UI-ORIGIN；Oryn 的「orb 连接管理」直接对标这两个。
- `src/shared/rpc-types.ts` + `src/shared/rpc-client.ts`：线协议类型和命令超时表；Oryn 的 SDK 层可参考，但应改为基于上游 `wire/*.schema.json` 生成。
- `src/renderer/stores/messages.ts` + `tools.ts` + `hooks/use-rpc-events.ts`：事件 → 视图状态的 reducer 与乐观回显去重，是「完整展示细节」的核心。
- `src/renderer/components/tools/index.tsx`（及同目录渲染器）：工具渲染器注册表，是对话视图的组件清单。
- `src/main/browser-host.ts` + `browser-relay-protocol.ts` + `src/renderer/stores/terminals.ts`：浏览器 relay 与终端托管的两套「agent 侧托管、客户端显示」模式，直接对应 Portal / Browser Profile / Desktop 需求。
