# omp 用户可见功能全集:Oryn 客户端功能对等(parity)基线

## 0. 版本与方法说明(请先读)

- **源码版本变化**:调研中途 `/root/Projects/GitHub/oh-my-pi` 被整体替换。我先读到的是旧快照,疑似 18.15.0(用户给定)。随后目录只剩 `.git` 骨架,又被重新检出。
  - 现在的工作树:`packages/coding-agent/package.json:3` 写 `18.19.0`,`.git/refs/heads/main` = `52ff19a2dbc00853ff24c6e76ef7aecea88a722c`。
  - `CHANGELOG.md:3-96` 有 `[Unreleased]` 段,所以这是 18.19.0 之后的 main,不是 18.19.0 发布版。
  - **本报告所有行号均取自这棵新树。** 旧快照只用来给出 §10 的差异证据。
- **未运行 `omp --help`**:本角色没有执行类工具。CLI 信息来自 `omp://cli-reference.md` 和 `src/commands/*.ts`。
- **未读 fork**:`nornzach/oh-my-pi` 没有读。凡写「RPC 无」,均指**上游**;fork 加了哪些命令,标「未找到」。
- **内置文档与源码一致**:`omp://` 文档(18.19)与这棵新树基本一致,下文以源码为准,文档作旁证。

---

## 1. TUI 全部 slash 命令(内置共 89 个主名)

**注册机制**
- 总表在 `slash-commands/builtin-registry.ts:43-51`,把 7 个文件拼起来。别名通过 `:53-59` 建索引。
- 每条 spec 有两个可选处理器:`handle`(文本模式,RPC/ACP/TUI 共用)和 `handleTui`(仅 TUI,需要 `InteractiveModeContext`)。定义在 `types.ts:164-208`。
- **RPC 可用 ⇔ spec 有 `handle`**:
  - `available-commands.ts:46-47`:`if (!command.handle) continue`。
  - `acp-builtins.ts:70-79`:`executeAcpBuiltinSlashCommand` 查不到 `handle` 就返回 `false`。
  - `rpc-mode.ts:1943-2000`:RPC 对 `prompt` 命令先调它,返回 `false` 才走 `session.prompt()`。
- 我对 47 个带 `handle` 的命令逐一核对,与你给的 18.19 实测列表完全一致(含 `unpin`)。
- ⚠️ **RPC 陷阱**:TUI 专属命令(`/plan`、`/tree`、`/fork`、`/vibe`、`/loop` 等)经 RPC `prompt` 发出时**不会报错**。
  - 它们会被当作普通文本发给模型(`omp://slash-command-internals.md` §7「Unknown `/...` behavior」)。
  - 客户端必须自己拦截这些命令,映射到专用 RPC 命令或本地 UI。
- 内置命令解析按首个空白或冒号切分,所以 `/model:opus` 也是内置调用(同文档 §5)。
- 以下表格行号均为 `slash-commands/` 目录下的文件。**RPC** 列:✅ 表示有 `handle`,输出以 `command_output` 文本帧到达(`rpc-mode.ts:1948`),不是结构化数据;「仅TUI」表示只有 `handleTui`。

### 1.1 `builtin-modes.ts`(模式与模型)

| 命令 | 作用 | 参数 | 终端专属UI | 行 | RPC |
|---|---|---|---|---|---|
| `/security` | OMP 原生安全扫描 | `plan\|scan\|status\|cancel\|scans\|show\|import\|export\|validate\|compare\|disposition` | 否 | 237 | ✅ |
| `/settings` | 打开设置菜单 | 无 | 设置选择器 | 258 | 仅TUI |
| `/setup`(别名 `/providers`) | 提供商/登录/搜索配置向导 | `[providers]` | 向导 | 267 | 仅TUI |
| `/plan` | 切换计划模式 | `[prompt]` | 审批覆盖层 | 285 | 仅TUI |
| `/plan-review` | 重开最近计划的审批 | 无 | 覆盖层 | 306 | 仅TUI |
| `/vibe` | 导演模式(只读工具集 + 持久 worker) | `[prompt]` | 否(TV-wall 渲染) | 317 | 仅TUI |
| `/goal` | 目标模式 | `[objective]` 或 `set\|show\|pause\|resume\|drop\|budget <N\|off>` | 否 | 335 | 仅TUI(RPC 另有 `goal` 命令) |
| `/guided-goal` | agent 先访谈再设 goal | `[rough objective]` | 否 | 361 | 仅TUI |
| `/loop` | 循环模式 | `[count\|duration] [--while\|--until '<cmd>'] [prompt]` | 状态栏/暂停态 | 373 | 仅TUI |
| `/queue` | 排入「agent 让出后」队列 | `<message>`(也认 `->`/`=>` 前缀与枚举列表) | 否 | 400 | 仅TUI(RPC 用 `follow_up`) |
| `/model`(别名 `/models`) | 切换模型 | TUI 无参开选择器;RPC 版带参数即切换 | 模型选择器 | 413 | ✅ |
| `/switch` | 模糊切换模型,临时、不改角色 | `[model]`,接受 `provider/id`、`@role`、`:level` | 无参开选择器 | 457 | ✅ |
| `/fast` | 快速服务层 | `on\|ultra\|off\|status` | 否 | 508 | ✅ |
| `/slow` | 慢速/flex 层 | `on\|off\|status` | 否 | 536 | ✅ |
| `/skillful` | 系统提示是否列出 skills(仅本会话) | `on\|off\|status` | 否 | 564 | ✅ |
| `/extended-context` | 扩展上下文窗口 | `on\|off\|status` | 否 | 620 | ✅ |
| `/computer` | computer-use eval prelude 开关 | `on\|off\|status` | 否 | 647 | ✅ |
| `/ratchet` | 建/复用 eval,无人值守爬山 | `[flow and goal]` | 否 | 692 | ✅(返回 `{prompt}` 继续送模型) |
| `/prewalk` | 一次性模型交接 | `[into]`、`restart [from] [into]`、`off`(用法见 `acpInputHint`) | 否 | 726 | ✅(仅 `handle`,无 `handleTui`) |
| `/modelpreset` | 保存/切换模型预设(角色模型 + 思考级别) | `list\|save\|switch\|delete [name]` | TUI 有向导 | 801 | ✅ |
| `/effort` | 设思考级别 | `[level]` | 否 | 851 | ✅ |

### 1.2 `builtin-collaboration.ts`(导出/共享/协作)

| 命令 | 作用 | 参数 | 终端UI | 行 | RPC |
|---|---|---|---|---|---|
| `/advisor` | advisor 开关 | `on\|off\|status\|dump [raw]\|configure`(`configure` 仅 TUI)、`toggle` | `configure` 编辑器 | 63 | ✅ |
| `/export` | 导出 HTML | `[--themes] [path]` | TUI 会开浏览器 | 180 | ✅(另有 RPC `export_html`) |
| `/trace` | 在 stats 仪表盘看 trace | 无 | 浏览器仪表盘 | 204 | ✅ |
| `/dump` | 转录文本 + LLM 请求 JSON | `[all\|anon]` | TUI 写剪贴板 | 231 | ✅ |
| `/share` | 加密链接分享 | 无 | 否 | 293 | ✅(TUI 可走自定义 `share.ts`) |
| `/collab` | 通过 relay 实时共享 | `[start\|view\|list\|stop\|status] [relayUrl]` | 是 | 319 | 仅TUI |
| `/join` | 加入共享会话 | `<link>` | 是 | 448 | 仅TUI |
| `/leave` | 离开 collab | 无 | 是 | 480 | 仅TUI |
| `/browser` | 浏览器 eval-prelude 无头/可见 | `headless\|visible` | 否 | 505 | ✅ |
| `/copy` | 从对话挑文本/代码块复制 | 参数可选 | 复制选择器 | 575 | 仅TUI |
| `/open` | 在浏览器打开对话里最后一个链接 | `[link\|url]` | 是 | 627 | 仅TUI |

### 1.3 `builtin-session.ts`(会话信息/导航/管理)

| 命令 | 作用 | 参数 | 终端UI | 行 | RPC |
|---|---|---|---|---|---|
| `/todo` | 查看/改 todo | `edit\|copy\|expand\|collapse\|export\|import\|append\|start\|done\|drop\|rm`(见 `:213-229`) | `edit` 开 `$EDITOR` | 208 | ✅(`handleTodoAcp`) |
| `/session` | 会话信息/删除/钉账号 | `info\|delete\|pin [account]` | TUI `delete` 带确认 | 246 | ✅ |
| `/jobs` | 后台作业快照 | `full\|kill <id>\|all` | 否 | 329 | ✅ |
| `/usage` | 供应商用量/限额 | `show\|reset [provider/credential]` | TUI 有仪表盘 | 405 | ✅ |
| `/stats` | 启动本地 stats 仪表盘 | `[--port <p>] [--host <h>]` | 浏览器 | 452 | ✅ |
| `/changelog` | 更新日志 | `full\|last [N]` | 否 | 490 | ✅ |
| `/hotkeys` | 快捷键列表 | 无 | 是 | 519 | 仅TUI |
| `/tools` | 当前对 agent 可见的工具 | 无 | 否 | 528 | ✅ |
| `/context` | 上下文用量与内容浏览 | 无 | TUI 为 Context Viewer 覆盖层;RPC 为文本报告 | 557 | ✅ |
| `/extensions`(别名 `/status`) | 扩展控制中心 | 无 | 仪表盘 | 576 | 仅TUI |
| `/agents` | agent 定义/模型/prewalk/advisor 设置 | 无 | 仪表盘 | 586 | 仅TUI |
| `/git` | git UI(分屏 diff/暂存/提交) | `[revision]` | 全屏 | 595 | 仅TUI |
| `/local` | 在 `$EDITOR` 里开 `local://` 文件 | `[name]` | 选择器 | 606 | 仅TUI |
| `/hub`(别名 `/subagents`) | Agent Hub | 无 | 全屏 | 617 | 仅TUI |
| `/branch`(别名 `/rewind`) | 回退到某条消息,旧路径保留为分支 | 无 | 全屏转录选择器 | 627 | 仅TUI |
| `/fork` | 复制会话 | `[pane\|window\|tab]` | 多路复用器 | 637 | 仅TUI |
| `/tree` | 会话树导航 | 无 | 树选择器 | 677 | 仅TUI |
| `/login` | OAuth 登录 | `[provider\|redirect URL]` | 选择器/手动回调输入 | 686 | 仅TUI(RPC 用 `login`) |
| `/logout` | OAuth 登出 | `[provider]` | 选择器 | 739 | 仅TUI(RPC 用 `logout`) |
| `/mcp` | MCP 管理 | `add\|list\|remove\|test\|reauth\|unauth\|enable\|disable\|reconnect\|reload\|resources\|prompts\|notifications\|help`(`:762-786`) | `add` 向导等 | 762 | ✅(`handleMcpAcp`) |

### 1.4 `builtin-lifecycle.ts`(生命周期/会话层)

| 命令 | 作用 | 参数 | 终端UI | 行 | RPC |
|---|---|---|---|---|---|
| `/ssh` | SSH 主机管理 | `add\|list\|remove\|help` | 否 | 154 | ✅ |
| `/new` | 新会话 | 无 | 否 | 177 | 仅TUI(RPC 用 `new_session`) |
| `/fresh` | 重置 provider 流状态,不动转录 | 无;流式中不可用 | 否 | 186 | ✅ |
| `/clear` | 原地清上下文,会话保留 | 无;追加 `reset_boundary` | 否 | 208 | 仅TUI |
| `/delete` | 删当前会话并新建 | 无 | 否 | 219 | 仅TUI(RPC 用 `/session delete`) |
| `/compact` | 手动压缩 | `[soft\|remote\|snapcompact] [focus]`;`snapcompact` 不接受 focus(见 `session/compact-modes.ts:41-56`) | 否 | 228 | ✅ |
| `/shake` | 机械丢弃重内容 | `elide\|images\|thinking` | 否 | 322 | ✅ |
| `/handoff` | 总结成交接文档并原地压缩 | `[focus instructions]` | 否 | 351 | ✅ |
| `/seance` | 咨询某历史会话而不恢复 | `[id\|path] [--model selector]` | 选择器 | 423(`seance.ts:151`) | 仅TUI |
| `/resume` | 恢复其他会话 | `[session id\|@claude\|@codex]` | 会话选择器 | 425 | 仅TUI |
| `/pin` | 钉/取消钉(toggle) | `[session id]` | 否 | 461 | ✅ |
| `/unpin` | 取消钉 | `[session id]` | 否 | 476 | ✅ |
| `/btw` | 旁路提问 / 浏览 BTW 历史 | `[question]` | 面板/历史 | 491 | 仅TUI(RPC 用 `btw`) |
| `/tan` | 后台跑一个完整 agent 做旁支工作 | `<work>` | 否 | 507 | 仅TUI |
| `/omfg` | 把抱怨锻造成 TTSR 规则 | `<complaint>` | 否 | 520 | 仅TUI |
| `/cleanse` | 并行子 agent 修诊断 | `[request] [--all]` | 否 | 533 | 仅TUI |
| `/retry` | 重试上次失败轮 | 无 | 否 | 546 | ✅ |
| `/debug` | 调试工具选择器 | 无 | 全屏 | 581 | 仅TUI |
| `/memory` | 记忆维护 | `view\|stats\|diagnose\|queue\|sync\|clear\|reset\|enqueue\|rebuild\|mm list\|mm show\|mm refresh\|mm history\|mm seed\|mm delete\|mm reload`(`:595-614`) | 否 | 590 | ✅ |
| `/rename` | 重命名,省略则生成标题 | `[title]` | 否 | 680 | ✅ |
| `/move` | 把会话搬到别的目录 | `<path>` | 否 | 758 | ✅ |
| `/wt`(别名 `/worktree`) | 把会话(含改动)搬进新 worktree | `[<branch>]` | 否 | 788 | ✅ |
| `/add-dir` | 加多根目录 | `<path>` | 否 | 823 | ✅ |
| `/remove-dir` | 去多根目录 | `<path>` | 否 | 855 | ✅ |
| `/dirs` | 列工作区目录 | 无 | 否 | 884 | ✅ |
| `/exit` | 退出 | 无 | 否 | 893 | 仅TUI |
| `/restart` | 重启 omp,恢复本会话及运行中子 agent | 无 | 否 | 898 | 仅TUI |

### 1.5 `builtin-marketplace.ts`、`builtin-skills.ts`、`builtin-control.ts`

| 命令 | 作用 | 参数 | 终端UI | 行 | RPC |
|---|---|---|---|---|---|
| `/marketplace`(`builtin-marketplace.ts`) | 市场源与插件管理 | `add\|remove\|update\|list\|discover\|install\|uninstall\|installed\|upgrade\|help` | TUI 有交互浏览器 | 46 | ✅ |
| `/plugins`(别名 `/plugin`) | 已装插件 | `list\|enable\|disable <plugin>` | 否 | 426 | ✅ |
| `/reload-plugins` | 重载 skills/commands/hooks/tools/agents/MCP | 无 | 否 | 490 | ✅ |
| `/skills`(`builtin-skills.ts`) | skills.omp.sh 注册表搜索/安装/更新 | `search\|install\|installed\|update\|help` | 否 | 66 | 仅TUI |
| `/force`(别名 `/force:`)(`builtin-control.ts`) | 强制下一轮用指定工具 | `<tool-name> [prompt]` | 否 | 44 | ✅(`{prompt}` 继续) |
| `/live` | Codex 实时语音模式 | 无 | 语音 UI | 94 | 仅TUI(RPC 用 `live_*`) |
| `/record` | 录屏为可回放文件(`omp play`) | 无 | 是 | 103 | 仅TUI |
| `/pause` | 冻结全部 agent 至恢复 | 无 | 暂停屏幕 | 112 | 仅TUI |
| `/forget-approvals` | 清掉「询问并记住」的授权 | 无 | 否 | 122 | ✅ |
| `/quit`(别名 `/q`) | 退出 | 无 | 否 | 132 | 仅TUI |

### 1.6 不在内置表里、用户也会遇到的 slash 命令

- **捆绑自定义命令**(`extensibility/custom-commands/loader.ts:154-175`):
  - `/green`(`bundled/ci-green`):生成「迭代 CI 直到绿」的提示。
  - `/review`(`bundled/review/index.ts:619`):构造 prompt,让主会话调 `task` 的 `reviewer`。
  - `/annotate`(`bundled/annotate/index.ts:345`):给 diff/文本加批注。
  - 这三个走 `session.customCommands`,会出现在 RPC 的 `available_commands_update` 中(`available-commands.ts:90-100`)。覆盖层 UI 仅 TUI。
- **兜底模板** `/init`(`omp://slash-command-internals.md` §3)。
- **`/skill:<name>`**:`skills.enableSkillCommands` 开启时存在;RPC 由 `dispatchRpcSkillPrompt` 处理(`rpc-mode.ts:1927-1940`)。
- **`/autoresearch`**:扩展注册(`autoresearch/index.ts:125`)。
- **文件命令**:`.omp/commands/*.md`、Claude/Codex/OpenCode 命令目录(优先级见 `omp://slash-command-internals.md` §1)。frontmatter `side: true` 的命令在 TUI 里当 `/btw` 处理,RPC 当普通 prompt。
- **扩展注册命令**:通过 `getRegisteredCommands` 并入列表(`interactive-mode.ts:2967`)。

---

## 2. 计划模式

### 2.1 进入/退出(TUI)
- 入口:`/plan [prompt]`(`builtin-modes.ts:285-303`)、快捷键 `app.plan.toggle` = `Alt+Shift+P`(`omp://keybindings.md`)、启动设置 `plan.defaultOnStartup`(`plan-mode/settings.ts`)、CLI `--plan-yolo`。
- `/plan` 是**三态切换**(`interactive-mode.ts:7006-7045`):
  1. 未开 → `#enterPlanMode`(`:5993`)。
  2. 已开 → 若计划文件已有草稿,先确认「Exit plan mode?」,再 `#exitPlanMode({paused:true, interruptActiveTurn:true})`,进入 **paused** 态。
  3. paused 且无参数再按 → `#disablePausedPlanMode`(`:6998`)彻底关闭。
- 互斥:goal / vibe 活动时拒绝进入,提示「Exit goal mode first」「Exit vibe mode first」(`:7010-7017`)。
- `plan.enabled` 为 false 时拒绝(`:7040`)。
- 带参数 `/plan <prompt>`:进入后把 prompt 作为第一个计划轮提交(`:7044-7068`)。

### 2.2 进入时发生的事(`#enterPlanMode`,`:5993-6075`)
- 工具集:
  - 若内置 `write` 存在则保留(用于写 `local://` 计划文件和提交 `xd://propose`)。
  - 若 `write` 被扩展工具顶替,则警告并丢弃。
  - 保存 `#planModePreviousToolPresentation`,退出时恢复。
- 状态:`session.setPlanModeState({enabled, planFilePath, workflow, reentry})`(`plan-mode/state.ts`)。
- 模型:`#applyPlanModeModel` 切到 `plan` 角色模型。
- 处理器:`session.setPlanProposalHandler(title => session.preparePlanForReview(title))`(`:6067`)。
- 若正在流式,还要 `sendPlanModeContext({deliverAs:"steer"})`。
- 并向 session journal 追加 `mode_change` 条目,恢复会话时靠它重入(`:5972-5976`)。

### 2.3 提示词契约(`prompts/system/plan-mode-active.md`)
- 工作区只读。`local://` 是会话本地产物目录,可写。
- 规范计划文件:`local://<slug>-plan.md`,slug 只含字母/数字/下划线/连字符,**批准时不改名**。
- 提交审批**只能**用 `write` 工具把 `<slug>`(纯文本)写到 `xd://propose`。**禁止**在散文里请求批准,也**禁止**用 `ask` 请求批准。
- 只允许 `ask` 或写 `xd://propose` 结束一个计划轮(`:119-122`;`agent-session.ts:13159` 的 `#isPlanDecisionTool`)。
- 计划模式下 `ask.timeout` 被禁用(`omp://tools/ask.md` Flow 4)。

### 2.4 只读约束如何实施
- **工具层守卫**(不是提示词):
  - `tools/plan-mode-guard.ts` 的 `enforcePlanModeWrite`(约 `:94-116`)。
  - 工作树写入抛 `Plan mode: the working tree is read-only. Write your plan to a local://<slug>-plan.md file instead.`
  - 重命名 / 删除直接禁止。
  - 目标落在 `local://` 沙盒内才放行(`targetsLocalSandbox`)。
- `proc://` 的 stdin 与 `/mode` 写入在计划模式下被阻止(`omp://tools/write.md`)。
- task 子 agent 在计划模式只拿到 `read/grep/glob/web_search/ast_grep`,并禁用 LSP/IRC(`omp://tools/task.md` 第 8 条)。
- 计划文件的 `read` 结果受压缩保护(`plan-mode/plan-protection.ts`)。

### 2.5 `xd://propose` 如何触发审批
- `tools/resolve.ts:285-301`:`xd://propose` 把写入内容(标题)派发给 plan-proposal handler;计划模式未开时抛「No plan is awaiting approval」。
- `session.preparePlanForReview`(`agent-session.ts:2129-2146`):
  - 用 `resolveApprovedPlan` 校验计划文件(按 `extra.title`、状态里的路径、最新的 `*-plan.md` 兜底)。
  - 返回 `details: {planFilePath, title, planExists}`。
- TUI 在 `event-controller.ts:2360-2395` 的 `tool_execution_end` 里读 write 派发元数据 `dispatch.inner`,**不阻塞**事件链地调用 `ctx.handlePlanApproval(...)`。
- `handlePlanApproval`(`interactive-mode.ts:7721` 起)先**静默 abort 当前轮**(`#abortPlanApprovalTurnSilently`),避免模型循环重提。
- 随后读计划、必要时把审批路径提升进 `planModeState`,弹出审批覆盖层。

### 2.6 审批选项及语义(`:7766-7772`)

| id | 标签 | 语义(见 `#approvePlan`,`:6784-6990`) |
|---|---|---|
| `execute` | **Approve and execute** | 退出计划模式 → `handleClearCommand()` 清上下文(新 `local://` 根,拷贝旧产物,写入计划)→ 发送合成的「plan-approved」prompt 开始执行 |
| `compact` | **Approve and compact context** | 退出计划模式(延迟恢复模型)→ 先设计划引用路径 → `handleCompactCommand` 用 `internalGuidance`(计划蒸馏提示,**不走**扩展 hook 的 `customInstructions`)压缩 → 再切到执行模型 → 发合成 prompt。压缩被取消则不派发执行 |
| `keep` | **Approve and keep context (~tokens/window)** | 退出计划模式,保留全部探索历史直接执行。上下文占比 ≥95% 时禁用(`PLAN_KEEP_CONTEXT_DISABLE_THRESHOLD_PERCENT=95`,`:642`) |
| `refine` | **Refine plan** | 不退出计划模式,把覆盖层里的批注/反馈作为下一条用户 prompt 发出;反馈为空则提示「enter a follow-up prompt」 |
| `save` | **Save and quit** | 弹路径输入框(`PlanSaveOverlay`,标题由模型生成建议文件名)→ 写到所选路径 → 退出计划模式 → `/clear` 新会话(`#savePlanAndQuit`,`:6665`) |

- **执行层级滑块**:若解析出 ≥2 个角色模型,覆盖层带「continue with」滑块,默认落在 `default` 层,选中的角色模型在**退出计划模式之后**才应用(`:7794-7820`、`:7932-7960`)。
- 覆盖层内可编辑计划(删除章节、批注)、外部编辑器打开(`#openPlanInExternalEditor`)、复制到剪贴板。编辑内容在批准时落盘。
- 批准后:
  - 记录 `appendModeChange("none", {approvedPlanFilePath})`。
  - 若 `plan.autosave` 则存到 `plan.autosaveDir`(默认 `<project>/.omp/plans/`)。
  - 用计划标题给无名会话起自动名。

### 2.7 `--plan-yolo`
- 解析:`cli/args.ts:383`、`flag-tables.ts:177`。`--plan-yolo-into` 默认 `@smol`。`--goal` 不能与 `--plan-yolo` 同用(`args.ts:195`)。
- 语义:强制只读计划模式启动 → 模型第一次 `xd://propose` 时**自动批准**(不弹覆盖层)→ 切到 `--plan-yolo-into` 模型执行。
- 实现:`session/prewalk.ts:504-611`(`armPlanYoloIfNeeded`、`#finalizePlanYoloProposal`);`sdk.ts:699` 的 `planYolo` 选项。
- `print-mode` 里 `plan.defaultOnStartup` 被忽略,`--plan-yolo` 是官方的无头计划流程(`print-mode.ts:215-230`)。

### 2.8 其他入口:扩展与 ACP 已有半套接口
- 扩展:
  - `ctx.sessionModes.set({mode:"plan", action:"enter"|"exit"})`(`extensibility/extensions/types.ts:1502-1537`)。
  - 事件 `plan_review_requested`(带 `reviewId, sessionId, title, planFilePath, planContent, options, tiers`)和 `plan_review_resolved`(`:1451-1490`)。
  - `ctx.resolvePlanReview(reviewId, choice, {feedback, role})`、`ctx.getPendingPlanReview()`、`ctx.openPlanReview()`(`:837-846`)。
  - `mode_changed` 事件(`:1540`)。
  - 这些**只在交互模式有效**(`PlanReviewRequestedEvent` 注释)。
- ACP:`acp-agent.ts:2547-2570` 有简化版(切 mode 即 `setPlanModeState` + `setPlanProposalHandler`),`#handleAcpPlanProposal`(`:2587`)用 elicitation 做二选一。
  - 它**没有**工具集收窄、模型切换、四种批准语义、`local://` 产物拷贝。可作参考,不可直接当 RPC 实现。
- **RPC 当前完全没有**:`rpc-mode.ts` 里与 plan 相关的只有 `rpc-goal.ts` 对 goal 的互斥检查(`:235`、`:408`)。`get_state` 也没有 plan 字段(`wire/state.ts:104-145`)。

### 2.9 要在 RPC 下支持计划模式,需要补的接口
1. 进入/暂停/退出命令,如 `set_plan_mode{action:"enter"|"pause"|"exit", prompt?}`。
   - 目前模式状态机在 `InteractiveMode` 私有方法里(`#enterPlanMode`、`#exitPlanMode`、`#tearDownPlanMode`),需要下沉到 `AgentSession` 或 RPC host。
   - 工具集保存/恢复、`plan` 角色模型切换/恢复、`mode_change` 落盘都要一并带过去。
2. `get_state` 增加 `planMode{status:"inactive"|"active"|"paused", planFilePath, workflow}`(可借用 `SessionModeState` 形状,`types.ts:1510`),并加 `mode_changed` 事件帧。
3. 审批帧与应答:
   - 服务端帧 `plan_review_requested`(复用 `PlanReviewRequestedEvent`,含 `options` 与 `tiers`)。
   - 命令 `resolve_plan_review{reviewId, choice: execute|compact|keep|refine|save, feedback?, role?, savePath?}`。
   - 帧 `plan_review_resolved`;`open_plan_review` 命令对应 `/plan-review`;`get_pending_plan_review`。
4. 计划内容读取:
   - 事件里已带 `planContent`。
   - 客户端要编辑/批注,需要写回,可走 host URI 方案(`set_host_uri_schemes`)或新增 `get_plan`/`put_plan`。
5. 触发点:RPC 的 `tool_execution_end` 已带 write 派发 `inner`。服务端需在 RPC 模式下装一个 handler,做「静默 abort 当前轮 + 发 `plan_review_requested`」,等价于 TUI 的 `handlePlanApproval`。
6. 四种批准的后续动作(清上下文、压缩、模型切换、合成 prompt)都要在服务端完成。`compact` 需要 `internalGuidance` 通道。
7. `--plan-yolo` 能力:RPC 以进程参数启动即可(`sdk.ts:699` 选项已存在),不需要新命令。若想运行期开启,需新增命令 [推测]。
8. 无头默认值:`plan.enabled` 设置、`ask` 不再超时。

---

## 3. 会话层操作

TUI 入口 → 语义 → 上游 RPC:

| 操作 | TUI 入口 | 语义 | 上游 RPC |
|---|---|---|---|
| new | `/new`(`lifecycle:177`) | 新 identity;发 `session_before_switch`(可被 hook 取消),保留模型/思考级别,清队列/todo/checkpoint;vibe 模式下拒绝(`omp://session-operations-...` New and delete) | `new_session{parentSession?}` → `{cancelled}`(`rpc-types.ts:61`) |
| resume | `/resume [id\|@claude\|@codex]`(`:425`)、CLI `--resume` | 切换到另一个 jsonl;先跟随 `continuationOf` 找最新副本;跨项目会 `applyCwdChange`;模型恢复失败则整个切换失败(RPC/headless)或警告后沿用当前模型(TUI) | `switch_session{sessionPath, provider?, modelId?}`(`:132`)、`open_session{sessionDir,...}`(`:62`) |
| continue | 仅 CLI `--continue` / `autoResume` 设置;**没有** `/continue` | 取终端 breadcrumb 或最近有内容的会话 | `open_session` 近似(按目录续最新会话) |
| restore | CLI `--restore`、`/restart`(`:898`) | 恢复上次进程中断的子 agent 与主轮(重试同 `/retry`) | 无 |
| 列出会话 | `/resume` 选择器 | `SessionManager.listForPicker/listAllForPicker`,置顶 pin 的会话 | **无列表命令**。`omp session lease` 只做 lease 探测(`commands/session.ts:26`,动作仅 `pack/unpack/moved/lease`)。RPC 有 `get_session_lease`(`:130`) |
| fork | `/fork`(`session:637`) | 整会话复制成新 jsonl,`parentSession` 指向旧会话,复制产物目录;流式中拒绝;vibe 下拒绝 | `fork{entryId?}`(`:134`);带 `entryId` 时从某条 message 之后切出(自动补齐 tool-call 批次);忙时 `code:"session_busy"` |
| fork pane/window/tab | `/fork pane\|window\|tab` | 在 tmux/Zellij/Herdr/Orca/CMUX 新 pane/窗口里起子 omp(`--fork`) | 仅TUI |
| branch | `/branch`、`/rewind`(`session:627`),默认双击 Esc(`doubleEscapeAction=rewind`) | 全屏转录选择器,**同文件内**移动 leaf,恢复用户草稿(`omp://tree.md`);程序化 `AgentSession.branch()` 则创建独立分支会话 | `branch{entryId}` → `{text, cancelled}`(`:133`)、`get_branch_messages`(`:138`)。⚠️ RPC `branch` 对应的是**程序化版本**(新会话),TUI `/branch` 等价于 `navigate_tree{summarize:false}` |
| tree 导航 | `/tree`(`session:677`) | 树选择器,过滤器 `default/no-tools/user-only/labeled-only/all`,搜索,`Shift+L` 编辑 label,`Shift+Enter` 直接摘要;选 user 消息则 leaf 移到其 parent 并预填输入;选 `ask` 结果则重开表单作为兄弟分支 | `get_tree`(`:78`)、`navigate_tree{entryId, summarize?, customInstructions?}`(`:135`)→ `{cancelled, aborted?, editorText?}`、`abort_branch_summary`(`:136`)、`set_label`(`:137`)、`get_entries{since?}`(`:77`)。`ask_reanswer_unsupported`:选到可重答的 `ask` 结果时 RPC 拒绝 |
| 分支摘要 | `/tree` 里选「Summarize / custom prompt」,`branchSummary.enabled`(默认 false) | 放弃分支被摘要,`branch_summary` 条目挂在目标处 | `navigate_tree.summarize`;中途可 `abort_branch_summary` |
| handoff | `/handoff [focus]`(`lifecycle:351`) | 用当前上下文旁路生成交接文档,作为一条 `CompactionEntry` 原地提交;session id/转录/缓存键不变;流式中拒绝 | `handoff{customInstructions?}` → `{savedPath?}\|null`(`:141`) |
| compact(手动) | `/compact [soft\|remote\|snapcompact] [focus]`(`lifecycle:228`) | 模式见 §1.4;`snapcompact` 把历史渲染成位图帧,无 LLM 调用,要求视觉模型 | `compact{customInstructions?}`(`:114`)→ `CompactionResult`;事件 `manual_compaction_start/end{mode?}`。RPC 命令**无 mode 参数**,要选模式需通过 `prompt "/compact snapcompact"` |
| compact(自动) | 设置 `compaction.*`(`methodOrder` 等) | 溢出恢复 / 不完整输出恢复 / 阈值 / 中途 / 空闲 | `set_auto_compaction{enabled}`(`:115`);事件 `auto_compaction_start{reason,action}` / `auto_compaction_end`(`agent-session-events.ts:32-49`) |
| shake | `/shake [elide\|images\|thinking]` | 把工具结果与大块替换为可恢复的 `artifact://` 引用 | 经 `prompt "/shake"`(✅) |
| snapcompact | `compaction.methodOrder` 含 `snapcompact` 或 `/compact snapcompact` | 见 `omp://compaction.md` | 同上 |
| fresh | `/fresh` | 只重置 provider 侧流状态/缓存 id | 经 `prompt "/fresh"` |
| clear | `/clear` | 原地清上下文,追加 `reset_boundary` | 仅TUI |
| rename | `/rename [title]` | 空标题则让模型生成 | `set_session_name{name}`(`:140`);也可 `/rename`;`session_info_update` 事件(`rpc-mode.ts:1970`) |
| pin / unpin | `/pin`、`/unpin`、`Alt+K`/`Alt+Shift+K` | 钉在 resume 列表顶部 | 经 `prompt "/pin"`、`"/unpin"` |
| label | `/tree` 里 `Shift+L` | 追加式 `label` 条目 | `set_label{entryId,label?}` |
| move | `/move <path>` | 重定位会话到新目录,拒绝流式中与 BTW 运行中 | 经 `prompt "/move"` |
| worktree | `/wt [branch]` | 新建 worktree 并带改动搬迁 | 经 `prompt "/wt"` |
| export html | `/export [--themes] [path]` | 嵌入会话/子会话/trace | `export_html{outputPath?}`(`:131`)→ `{path}` |
| share | `/share` | AES-256-GCM 加密快照 → 分享服务器或 secret gist,密钥在 URL fragment | 经 `prompt "/share"`(`command_output` 文本) |
| dump | `/dump [all\|anon]` | 转录 + LLM 请求 JSON(+ zip) | 经 `prompt "/dump"`;`get_state.systemPrompt/dumpTools` 提供原料 |
| import Claude/Codex | `/resume @claude\|@codex` | 转换并持久化为新 OMP 会话 | 无 |
| delete | `/delete`、`/session delete` | 删当前 jsonl+产物并新建 lazy 会话 | `/session delete` 经 `prompt`(18.18.x 起,见 `CHANGELOG.md:176`) |
| checkpoint / rewind | **模型工具**(`checkpoint.enabled` 默认 false) | `checkpoint{goal}` 标记当前状态;`rewind{report}` 在 `turn_end` 把探索路径裁成 `branch_summary` + `rewind-report` | 无命令;仅有工具事件。恢复靠 `get_entries` 里的 `branch_summary` 条目 |
| seance | `/seance` | 把历史会话 fork 成只读顾问子 agent | 无(但有 `task` 工具的 `seance` agent) |
| 消息读取 | — | — | `get_messages`(`:144`)、`get_messages_page{cursor?,limit?}`(`:145`)、`get_last_assistant_text`(`:139`)、`get_session_stats`(`:129`) |

---

## 4. 输入区能力

- **提交语义**(`input-controller.ts:1100-1520`):
  - 空闲时 Enter 发新轮。
  - 流式中 Enter = **steer**(`streamingBehavior:"steer"`)。
  - `Ctrl+Enter` / `Ctrl+Q`(`app.message.followUp`)= **follow-up**,命中 `handleFollowUp`(`:770`)。
  - `/queue`、`->`、`=>` 前缀排入「agent 让出后」队列,并能把枚举列表拆成多条(`tui/prompt/queue-input.ts:124-139`)。
  - 压缩进行中,自由文本排进 `compactionQueuedMessages` 待压缩结束回放(`:1357-1365`)。
- **队列模式**:`steeringMode`/`followUpMode` ∈ `all|one-at-a-time`(默认 one-at-a-time),`interruptMode` ∈ `immediate|wait`(`modes/settings.ts:907-945`)。RPC:`set_steering_mode`、`set_follow_up_mode`、`set_interrupt_mode`(`rpc-types.ts:109-111`)。
- **队列管理**:
  - `Alt+Up` / `Shift+Up`(`app.message.dequeue`)把最后一条排队消息取回编辑器。
  - RPC:`remove_queued_message{message,queue}`、`promote_queued_message{message}`(follow-up 提升为 steer)、`abort_and_restore_queue`(等价 TUI 的 Esc,原子撤回并中断)。
  - `queue_update{steering,followUp,liveSteered}` 事件给出可渲染的队列 chip 文本(`agent-session-events.ts:109`)。`get_state.queuedMessages` 同形。
- **中断语义(Esc)**(`input-controller.ts:526-700`):
  - `mcp test` 运行中先取消测试。
  - 流式 → 中断当前轮。
  - 压缩中 → 取消压缩。
  - BTW 运行中 → 取消侧问。
  - 空编辑器 + 非流式 → 双击 Esc 触发 `doubleEscapeAction`(`rewind` 默认 / `tree` / `none`)。
  - 聚焦子 agent 视图时 Esc 回主会话,**不**中断子 agent(`omp://agent-hub.md`)。
  - `/loop` 中 Esc 暂停循环。
  - RPC:`abort`、`abort_and_prompt`、`abort_and_restore_queue`、`abort_retry`、`abort_bash`。
- **`@` 文件提及 / 模型提及 `^model`**:提及自动补全在 `tui/prompt/*`(`prompt-action-autocomplete`、`model-mention-autocomplete`,`input-controller.ts:41-48`);`^model` 提及与 magic keywords 在 `session.prompt` 里展开(`omp://slash-command-internals.md` §5)。**属于客户端补全 + 服务端展开**,RPC 无补全接口。
- **图片**:
  - 粘贴(`app.clipboard.pasteImage`:Linux `Ctrl+V`、macOS `Ctrl+V/Cmd+V`、Windows `Ctrl+V/Alt+V`)。
  - OSC 5522 增强粘贴。
  - 粘贴单个图片文件路径会自动加载。视频路径会生成联系表 PNG(`input-controller.ts:81-83`)。
  - 以 `[Image #N]` 占位符 chip 表示,提交时压缩编号(`#compactDraftImages`,`:1057`)。
  - RPC:`prompt.images: ImageContent[]`;文本模型可走 vision-description(20s 上限)。
- **大段粘贴折叠**:`[Paste #N, +30 lines]` chip(`omp://keybindings.md` Vim 节提到)。`Ctrl+Shift+V` / `Alt+Shift+V` 原样粘贴(`app.clipboard.pasteTextRaw`)。折叠为**客户端行为**;`wrapPasteInAttachmentBlock` 会把内容包进 `<attachment>` 标签(`input-controller.ts:253`)。
- **`!` / `!!` bash**(`:1315-1330`):`!cmd` 执行并入上下文,`!!cmd` 排除上下文。已有 bash 在跑时拒绝并回填文本。RPC:`bash{command, excludeFromContext?}`,`ready.supportsBashExcludeFromContext` 为 true 才可发后者;`abort_bash`。
- **`$` / `$$` Python**(`:225-253`):`$ code` 入上下文,`$$ code` 排除。**`$` 后必须跟空白才算 Python**,否则 `$HOME` 之类当作文本。`parsePythonCommandInput` 还会识别粘贴来的 shell 提示符并放弃。**RPC 无对应命令**。
- **历史搜索**:`Ctrl+R`(`app.history.search`);历史存本地 `history.db`;被 `Ctrl+C` 清掉的草稿可用 Up 找回(保留 100 条,`composer.recallClearedDrafts` 默认开)。无 RPC。
- **补全/幽灵文本(predict)**:设置 `spelling.autocomplete`(默认 `auto`)、`composer.predictions`。RPC `predict_word{text,cursor}` → `{suffix|null}`,`predict_word_feedback{accepted}`;首次请求可能耗时数秒到约两分钟,失败应静默(`omp://rpc.md` Word prediction)。
- **听写**:`stt.enabled`;`Space` 按住说话(`app.stt.pushToTalk`)、`app.stt.toggle` 未绑定。RPC:`dictation_start{captureId}`、`dictation_stop`,帧 `dictation_state/transcript/end`。`/live` 实时语音:RPC `live_start/connect/stop/mute` + `live_phase/levels/transcript/end` 帧(`wire/commands.ts:188-215`)。
- **magic keywords**(`omp://magic-keywords.md`):`ultrathink`、`chillax`、`orchestrate`、`workflowz`、`jevify`,须精确小写、独立成词,忽略代码块/行内代码/HTML。作用是**附加隐藏的、用户署名的通知消息**(`ultrathink`/`chillax` 还会改本轮思考级别)。匹配发生在服务端展开之后,因此 RPC 的 `prompt` 同样生效。TUI 额外有编辑器内动画高亮(客户端)。开关 `magicKeywords.enabled` 及各关键词的 `magicKeywords.*`。
- **多行编辑**:`Shift+Enter` 换行(见 `/annotate` 说明里的编辑器键)。Vim 模式 `tui.vimMode`,Normal/Insert/Visual,状态栏带 `vim` 段。纯客户端。
- **外部编辑器**:`Ctrl+G` 用 `$VISUAL`/`$EDITOR` 编辑草稿(`app.editor.external`);`Alt+G`/`/local` 编辑 `local://` 文件。客户端。
- **其他**:
  - `Alt+O` 链接提示(`app.links.hint`)。
  - `F5`/`Alt+R` 重试(`app.retry`)。
  - `Ctrl+O` 展开工具输出,`Ctrl+Shift+O` 隐藏工具活动。
  - `Ctrl+T` 思考块显隐,`Shift+Tab` 循环思考级别。
  - `Ctrl+P` / `Shift+Ctrl+P` / `Alt+P` / `Alt+M` 模型循环/选择。
  - `Ctrl+L` 实时语音。
  - `Alt+A` Agent Hub。
  - `Alt+I` 把 BTW 的 `<inject>` 发到主会话。
  - `Alt+H` 隐藏待答 `ask`,`app.approval.approve` 批准工具调用。

---

## 5. 运行时状态与指示

- **状态栏段**(`tui/src/status-line/schema.ts:2-30`):`pi`、`status`、`model`、`mode`、`path`、`git`、`pr`、`subagents`、`token_in`、`token_out`、`token_total`、`token_rate`、`cost`、`context_pct`、`context_total`、`time_spent`、`time`、`session`、`hostname`、`cache_read`、`cache_write`、`cache_hit`、`cache`、`session_name`、`usage`、`collab`、`stream`、`vim`。
  - 预设:`default/minimal/compact/full/nerd/ascii/custom`(`presets.ts`)。
  - 分隔符:`powerline/powerline-thin/slash/pipe/block/none/ascii`。
  - `SegmentContext`(`types.ts:96-230`)还暴露:
    - 计划/prewalk/loop(`waiting|running|paused`+限额+条件)/goal/vibe 状态;
    - 投机压缩态(`idle|running|armed`)、自动压缩开关;
    - 子 agent 数与树成本、`activeMs`、`turnElapsedMs`;
    - git 分支与 staged/unstaged/untracked、PR;
    - 用量窗口(五小时/日/七日/月)与重置积分;
    - 提示缓存状态;录制中;collab 角色与人数。
- **RPC 能给出的状态数据**(`wire/state.ts:104-145` 的 `SessionState`,即 `get_state`):
  - 模型、思考级别、`isStreaming`、`isCompacting`、三个队列模式、`sessionFile/Id/Name`。
  - `autoCompactionEnabled`、`fastModeEnabled/Active`、`slowModeSupported/Enabled/Scope`、`usageLimit`、`tokensPerSecond`。
  - `messageCount`、`queuedMessageCount`、`hasPendingAsyncWork`、`isSettled`、`queuedMessages`。
  - `todoPhases`、`systemPrompt`、`dumpTools`、`contextUsage{tokens,contextWindow,percent}`、`goal`。
  - **没有**:cwd、git 分支、plan/vibe/loop/prewalk/advisor/审批模式、MCP 状态、后台作业列表、订阅者数。
  - 成本/token 明细来自 `get_session_stats`(`SessionStats`,含 `cost`、`tokens`、`credits`、`routedModels`、`editRejections`)。
- **通知与提示事件**(`agent-session-events.ts`):
  - `notice{level,message,source}`。
  - 重试:`auto_retry_start{attempt,maxAttempts,delayMs}` / `auto_retry_end{success,finalError}`;回退:`retry_fallback_applied{from,to,role,reason}` / `retry_fallback_succeeded`。
  - cache warming:`cache_warming_start/end`;开关 RPC `set_cache_warming{off|streaming|idle}`(默认 `idle`)。
  - `model_changed`、`thinking_level_changed`、`config_warnings_changed`。
  - advisor:`advisor_cost_changed`、`advisor_yielded`(以及消息流里的 advisor 卡片)。
  - TTSR:`ttsr_triggered{rules}`。
  - todo:`todo_reminder{todos,attempt,maxAttempts}`、`todo_auto_clear`。
  - IRC:`irc_message{message}`;`goal_updated{goal,state}`;`queue_update`。
  - 另有 RPC 层帧 `prompt_result`、`session_settled`、`command_output`、`session_info_update`、`config_update`、`available_commands_update`。
  - 基础 `AgentEvent`:`agent_start/end`、`turn_start/end`、`message_start/update/end`、`tool_execution_start/update/end`、`tool_stream_update`(`packages/agent/src/types.ts:1328-1355`)。
- **「让出」vs「安定」**:`prompt_result` 报告 agent yield;`session_settled`/`isSettled` 才表示没有任何后台工作能再唤醒会话(`omp://rpc.md` Yield vs settled)。
- **后台作业**:
  - `/jobs` 快照,`/jobs kill <id>|all`。
  - 内部 URL `proc://`(作业/服务/子 agent 状态与输出;`write proc://<id>/kill` 取消)。
  - TUI 状态栏与 job-widget。
  - **RPC 没有结构化 jobs 接口**,只有 `/jobs` 的文本输出。
- **子 agent 树 / Agent Hub**(`omp://agent-hub.md`):
  - 名册行含状态(`running/idle/parked/aborted`)、父级、未读 IRC 数、角色模型、年龄、任务、成本、请求数、工具调用数、token、当前工具与意图、重试状态、上下文占用、输出/补丁路径、worktree 分支。
  - 键位:`t` 树/平铺,`/` 过滤,`1/2` 切名册/活动日志,`r` revive,`x` kill,`Tab` 切检视器;活动日志有 `f` 过滤、`s` 范围、搜索、`Space` 跟随。
  - 聚焦子 agent 时编辑器/状态栏/转录都属于它;可在其中 steer/换模型。
  - 持久化 agent 会被扫描为 `parked`,advisor 转录是只读行(不能聊天/revive/kill)。
  - 置顶「Subagents」跳转列表由 `display.pinnedAgents` 控制(`off|collapsed|full`),`display.subagentLivePreview` 加实时预览行,`tui.mouse` 开启后可点击聚焦。
- **IRC**:agent 间消息,`IrcMessage{id,from,to,body,ts,replyTo,wakeRelay,broadcast,progress}`,投递结果 `injected|woken|revived|failed`(`tui/tools/irc.ts:1-43`)。
  - 发送通过 `write agent://<id>`,`agent://all` 广播仅主 agent。
  - TUI 里显示为 `irc_message` 事件和名册未读计数。
- **阻塞状态**:等待审批/计划审查时 `holdBlockedStatus({state:"blocked", kind:"permission"})`(`interactive-mode.ts:7915`),可供外部终端标题/通知消费。

---

## 6. 工具清单

**内置工具**(`coding-agent/src/tools/index.ts:598-629`,工厂映射 `BUILTIN_TOOLS`,共 30 个):
`read`、`security_scan`、`bash`、`edit`、`ast_grep`、`ast_edit`、`ask`、`debug`、`ida`、`eval`、`github`、`glob`、`grep`、`find`、`lsp`、`checkpoint`、`rewind`、`context_notes`、`new_context`、`task`、`wait`、`todo`、`web_search`、`write`、`memory_edit`、`retain`、`recall`、`reflect`、`learn`、`manage_skill`。

**隐藏工具**(`:631-635`):`think`、`yield`、`goal`。

**设备(xd://)与协议**:
- `xd://propose`(提交计划)、`xd://resolve` / `xd://reject`(预览的批准/丢弃)定义在 `tools/resolve.ts:15-16`、`:61`。
- `xd://<tool>` 设备让 `write` 派发被「挂载」的内置/MCP 工具(`omp://rpc.md` get_state 节)。
- `out://`(写入 Markdown 作为不终止回合的回复,默认仅交互 TUI 开启)、`proc://`、`agent://`、`history://`、`local://`、`cfg://`、`artifact://` 等是内部 URL,不是工具名。

**vibe 工具**:`vibe_spawn/send/wait/kill/list`(渲染器在 `tui/src/tools/index.ts:55-59`)。

**未在 `BUILTIN_TOOLS` 中的文档**:`omp://tools/tts.md`、`generate_image.md` 存在,但这两个工具名不在上述表里,注册处未核实。

**浏览器 / computer 不是独立工具**:
- 它们是 `eval` 的 prelude(`/browser`、`/computer` 的描述都是 "eval prelude")。
- 浏览器可用 Chromium、WebKit、Tern、cmux、relay 等后端(`omp://tools/browser.md`)。

**TUI 渲染器与 details 类型**(`packages/tui/src/tools/index.ts:34-70` 的 `toolRenderers`):

| 工具 | 渲染器文件 | details 类型 |
|---|---|---|
| ask | `ask.ts` | `AskToolDetails`(`answers[]`, notes) |
| ast_grep | `ast-grep.ts` | `AstGrepToolDetails` |
| ast_edit | `ast-edit.ts` | `AstEditToolDetails` |
| bash | `bash.ts`(+`bash-interactive.ts`、`shell-viz*.ts`、`terminal-output.ts`) | `BashToolDetails` |
| debug | `debug.ts` | `DebugToolDetails` |
| eval | `eval.ts` + `eval-format/{javascript,python}.ts` | `EvalToolDetails` |
| edit / apply_patch | `edit.ts` | `EditToolDetails`(diff、失败类别) |
| find | `find.ts` | `FindToolDetails` |
| glob | `glob.ts` | `GlobToolDetails` |
| grep | `grep.ts` | `GrepToolDetails` |
| lsp | `lsp.ts` | `LspToolDetails` |
| wait | `wait.ts` | `CoordinationDetails` |
| read | `read.ts`(+`proc-render.ts`、`cfg-render.ts`、`fetch.ts`) | `ReadToolDetails`(含 `proc?: ProcReadDetails`、`cfg?: CfgReadDetails`)、`ReadUrlToolDetails` |
| resolve / reject | `resolve.ts` | `ResolveDetails` |
| retain / recall / reflect | `memory.ts` | `MemoryRetainDetails` 等 |
| task | `task.ts`(+`agent-tree.ts`、`task-yield-assembly.ts`) | `TaskToolDetails`、`FindingDetails`、`SubmitReviewDetails` |
| think | `think.ts` | — |
| todo | `todo.ts` | `TodoToolDetails` |
| github | `github.ts`(+`gh-format.ts`) | `GhToolDetails`、`GhRunWatchViewDetails` 等 |
| goal | `goal.ts` | `GoalToolDetails`(`op: create\|get\|complete\|resume\|drop`) |
| web_search | `web-search.ts` | `SearchRenderDetails` |
| vibe_* | `vibe.ts` | `VibeToolDetails` |
| write | `write.ts`(+`xdev.ts`、`xd-url.ts`) | `WriteToolDetails` |
| yield | `yield.ts` | — |
| MCP 工具 | `mcp.ts` | `MCPToolDetails` |
| 其余(checkpoint、rewind、context_notes、new_context、security_scan、ida、memory_edit、learn、manage_skill) | **无专属渲染器** → `default-renderer.ts` | 通用 |

- 渲染器契约:`renderCall`/`renderResult`、可选 `describeCall`/`describeResult`(Tern 语义视图 `NativeToolView`)、`figure`、`activitySummary`、`mergeCallAndResult`、`inline`、各类重绘提示(`tools/renderer.ts`)。
- `RenderResultOptions` 含 `expanded`、`isPartial`、`spinnerFrame`、`argsComplete`、`executionStarted`、`elapsedMs`、`cancelled` 与 shell trace 字段。
- 另有 `autoresearch.ts`(`RunDetails`/`LogDetails`/`InitExperimentDetails`/`UpdateNotesDetails`)、`job-widget.ts`、`daemon.ts`、`irc.ts`、`todo` HUD 等周边渲染文件。

**带交互的工具**:
- **ask**(`omp://tools/ask.md`):
  - 现为**一个 HTML `<form>`**(控件:radio/checkbox/select/text/textarea/range/date 等,可带图片与 SVG)。
  - TUI 用 `HtmlFormDialog`;Tern 按 `<fieldset>` 分页并加 Review 页(`Alt+H` 隐藏)。
  - 每个 radio/checkbox 组都带自由文本「Other」。`ask.timeout` 默认 0(不超时),计划模式下强制不超时。
  - **RPC**:宿主没有 `formDialog` 时,服务端逐字段走 `select`/`input`/`editor` 三种 `extension_ui_request`。RPC 另有 `ask` 方法(`questions[]`),但只给 `ui.askDialog`,且需先 `set_ask_dialog{enabled:true}`(`rpc-mode.ts:1527-1535`、`:1727`)。
  - Unreleased 里 RPC `select` 请求新增 `initialIndex`、`selectionMarker`、`checkedIndices`、`markableCount`(`CHANGELOG.md:71`)。
- **工具审批**(`omp://approval-mode.md`):
  - 层级 `read/write/exec`,模式 `always-ask|always-ask-cached|write|yolo`(默认 `yolo`,`tools/settings.ts:314-318`)。
  - TUI 在编辑器上方停靠提示:Approve / Approve `<tool>` for this session / Deny,`Ctrl+Enter` 或 `Ctrl+Q` 直接批准,`Esc` 拒绝。
  - `always-ask-cached` 还有「批准目录/文件」及「Remember across sessions」开关;`/forget-approvals` 清空。
  - **RPC/ACP 无专用对话框,走带同样选项的 `select`**。
  - 另有 `tools.restrictedFolders`(任何模式下强制询问)、bash 的 `bash.patterns`(allow/prompt/deny)。
  - 子 agent 以 `yolo` 无头运行,遇到 prompt 直接拒绝。
  - 扩展事件 `tool_approval_requested/resolved`(`extensions/types.ts:1432`)。
  - **RPC 没有 `get_pending_approval` 之类的结构化接口**。
- **browser**:
  - `/browser visible|headless`(18.19 起会立即显隐 WebKit 标签窗口,`CHANGELOG.md:39`)。
  - 浏览器中继扩展(`tools/browser/relay/*`)可由用户接管标签。
  - Chromium 标签仍等下次 open 才切模式。
- **computer**(`omp://tools/computer.md`):
  - `computer.backgroundFallback`:`takeover|refuse`。
  - `desktop.control.acquire` 需要真人确认,无 UI 时返回 `no-ui`;只有 `tools.approval["computer.control"]` 能无人值守放行。
  - 供应商触发的 `pendingSafetyChecks` 一律强制人工提示。
- **debug**:
  - 工具(DAP)走常规审批。
  - `/debug` 是 TUI 专属选择器(性能 profile、日志查看、raw SSE、报告打包)。
- **bash PTY 覆盖层**:`Console` 覆盖层 + `Esc` 杀会话(`omp://tools/bash.md`)。`rpc-ui` 模式设置 `PI_NO_PTY=1`(`omp://rpc.md` Startup)。

---

## 7. 模式与开关

| 模式/开关 | 是什么(依据) | RPC 可控? |
|---|---|---|
| **goal** | 持久自主目标。`/goal [obj]`、`set/show/pause/resume/drop/budget`;`guided-goal` 先访谈;`goal.enabled`、`goal.continuationModes`;与计划/vibe 互斥;完成后移除 `goal` 工具 | ✅ `goal{op:get\|create\|resume\|pause\|drop, objective?, token_budget?}`(`rpc-types.ts:68-74`)+ `goal_updated` 事件 + `get_state.goal`。RPC 默认**不自动续跑**,除非 `goal.continuationModes` 含 `"rpc"`(`omp://rpc.md` goal payload) |
| **plan** | §2 | ❌ |
| **vibe** | 顶层变导演,工具只剩 `read`/可选 `todo`/五个 worker 工具;`fast`=`sonic`(`@smol`)、`good`=`task`(`@task`);与 plan/goal 互斥;开着时拒绝 new/delete/fork/move/wt/handoff(`omp://vibe-mode.md`) | ❌ |
| **loop** | `/loop [count\|duration] [--while\|--until '<cmd>']`,按命令退出码判定;`Esc` 暂停(`builtin-modes.ts:373-377`) | ❌ |
| **prewalk** | 一次性模型交接。门:有 `todo` 工具时第一次成功 `todo` 调用才开门;在第一个符合条件的 edit/write 回合边界切换。`prewalk.enabled/into/from`、`prewalk.auto: off\|suggest\|on`;CLI `--prewalk/--no-prewalk/--prewalk-into` | 间接:`prompt "/prewalk ..."`(✅) |
| **plan-yolo** | §2.7 | 仅启动参数 |
| **advisor** | 第二模型审阅每轮并注入建议;`WATCHDOG.yml` 花名册;**RPC/ACP 协议默认关闭 advisor**(`omp://advisor-watchdog.md`) | `prompt "/advisor on\|off\|status\|dump"`;事件 `advisor_cost_changed`/`advisor_yielded` |
| **fast / slow** | 服务层:OpenAI `service_tier=priority\|ultrafast`、Anthropic `speed=fast`、Google priority;slow = flex 或 Anthropic 低优先级 | ✅ `set_fast_mode`/`set_slow_mode`;`get_state` 带 `fastModeEnabled/Active`、`slowModeSupported/Enabled/Scope` |
| **extended-context** | 扩展上下文窗口开关 | `prompt "/extended-context"` |
| **skillful** | 系统提示是否列 skills(仅会话) | `prompt "/skillful"` |
| **ratchet** | 建 eval 并无人值守爬山 | `prompt "/ratchet"` |
| **force tool** | `/force <tool> [prompt]` 强制下一轮用某工具 | `prompt "/force ..."` |
| **approval mode** | `tools.approvalMode` 或 `--approval-mode`/`--yolo`/`--auto-approve` | ❌(仅设置/启动参数;`set_ask_dialog` 只管 ask 的 UI) |
| **cache warming** | `providers.cacheWarming: off\|streaming\|idle` | ✅ `set_cache_warming` |
| **auto retry / compaction** | `retry.*`、`compaction.enabled` | ✅ `set_auto_retry`/`abort_retry`、`set_auto_compaction` |
| **thinking level** | `ThinkingLevel`: `inherit/off/minimal/low/medium/high/xhigh/max`(`auto` 仅会话选择器) | ✅ `set_thinking_level`/`cycle_thinking_level`/`get_available_thinking_levels` |
| **btw** | 旁路提问,不入主转录;`btw.inject` 允许发回主会话 | ✅ `btw`、`btw_cancel`、`get_btw_history` + `btw_delta`/`btw_record` 帧 |
| **pause** | 冻结所有 agent | ❌ |
| **browser/computer** | 见 §6 | `prompt "/browser"`、`"/computer"` |

---

## 8. 管理类功能

- **运行期会话操作(客户端应支持)**:
  - 模型:`set_model`、`cycle_model`、`get_available_models`(会等待模型发现)。角色模型/预设:`/modelpreset` 经 `prompt`。
  - 登录:`get_login_providers`、`login{providerId}`(流程以 `open_url` 与 `input` 的 `extension_ui_request` 到达;**`secret:true` 的输入一律拒绝**,须在终端 UI 完成)、`get_logout_accounts`、`logout{providerId,credentialId}`(`rpc-types.ts:147-151`)。
  - MCP:`/mcp` 全部子命令经 `prompt`。`reauth` 需要宿主有 `mcpManager`(`rpc-mode.ts:1952-1962`)。**无结构化的连接状态/重连事件**。
  - skills:`/skill:<name>`;`/skills` 搜索安装为 TUI 专属(`manage_skill` 工具另可由模型调用)。
  - 插件/市场:`/marketplace`、`/plugins`、`/reload-plugins` 均 ✅。
  - hooks/扩展:`extension_ui_request`(`select/confirm/input/editor/ask/notify/setStatus/setWidget/setTitle/set_editor_text/open_url`)与 `extension_ui_response`,另有 `host_tool_*`、`host_uri_*`。`custom()` 组件**在 RPC 下不挂载**(`omp://tui.md`)。`/extensions` 仪表盘仅 TUI。
  - memory:`/memory ...` ✅。ssh:`/ssh ...` ✅。
  - worktree/目录:`/wt`、`/move`、`/add-dir`、`/remove-dir`、`/dirs` ✅。
  - 报告类:`/usage`、`/stats`、`/context`、`/tools`、`/jobs`、`/changelog`、`/session info` 均 ✅,但输出是**纯文本**。结构化数据请用 `get_session_stats` 与 `get_state`。
  - `set_todos{phases}`:客户端可直接改 todo(`:79`)。
  - 宿主工具 / URI:`set_host_tools`、`set_host_uri_schemes`。
  - 事件过滤:`set_event_filter{events|null, messageUpdates:"full"|"delta"}`。
- **编辑配置**(按需求用文本配置,不做 GUI):
  - `~/.omp/agent/config.yml`、项目 `.omp/config.yml`、`keybindings.yml`、`WATCHDOG.yml`、`models.yml`。
  - `/settings`、`/setup`、`/agents`(agent 定义)、`/extensions` 仪表盘本质都是配置编辑器。
  - **RPC 锁定的默认值**:RPC/ACP 对声明了 `protocolDefault` 的设置(任务隔离/执行、memory、advisor 等)固定中立默认,显式配置才覆盖;RPC 另固定异步作业与 bash/eval 自动后台默认(`omp://rpc.md` Startup)。
  - `cfg://` 内部 URL 允许 agent 读写设置(`CfgReadDetails`/`CfgWriteDetails`)。
- **CLI 子命令**(`src/commands/*.ts` 文件名):`acp, agents, anonymize, auth-broker, auth-gateway, auth, bench, browser-relay, cleanse, clip, collab, commit, complete, completions, compress, config, dry-balance, find, gallery, gc, git, grep, grievances, if-bench, images, install, join, launch, login, models, native-shell, play, plugin, predict, ps, read, render, say, session, settings, setup, share, shell, skill, ssh, stats, stream, tiny-models, token, toks, ttsr, update, usage, web-search, worktree`。完整用法需运行 `omp <cmd> --help`(本次未执行)。

---

## 9. 功能对等矩阵

依据缩写:`R:`=`modes/rpc/rpc-types.ts` 行号;`W:`=`modes/rpc/wire/commands.ts`;`E:`=`session/agent-session-events.ts`;`reg:`=`slash-commands/` 下注册行;`doc:`=`omp://` 文档。

### 会话

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| 新会话 | `/new`(reg lifecycle:177) | `new_session`(R:61) | |
| 恢复会话 | `/resume`(lifecycle:425) | `switch_session`(R:132) / `open_session`(R:62) | 模型恢复失败即失败(doc session-operations) |
| 列会话 | `/resume` 选择器 | **无** | 客户端需自己扫 `~/.omp/agent/sessions/<encoded-cwd>/*.jsonl`(doc session-switching),`get_session_lease` 只判占用(R:130) |
| continue / restore | CLI `--continue`/`--restore`/`/restart` | 无 | `/restart` 仅 TUI(lifecycle:898) |
| fork(整会话/到某条) | `/fork`(session:637) | `fork{entryId?}`(R:134) | 忙时 `session_busy` |
| fork 到 pane/窗口 | `/fork pane\|window\|tab` | 无 | 依赖多路复用器 |
| branch / rewind | `/branch`(session:627)、双击 Esc | `navigate_tree{summarize:false}`(R:135);`branch`(R:133) 是另一语义 | doc tree.md「vs adjacent operations」 |
| 会话树 | `/tree`(session:677) | `get_tree`(R:78)、`navigate_tree`、`abort_branch_summary`(R:136)、`set_label`(R:137)、`get_entries`(R:77) | 过滤器/搜索为客户端 |
| 分支摘要 | `/tree` 摘要选项 | `navigate_tree.summarize/customInstructions` | `branchSummary.enabled` 默认 false |
| handoff | `/handoff`(lifecycle:351) | `handoff`(R:141) | 流式中拒绝 |
| compact | `/compact [mode] [focus]`(lifecycle:228) | `compact`(R:114)、`set_auto_compaction`(R:115)、事件 `manual_/auto_compaction_*`(E:32-65) | RPC 命令无 mode,模式用 `prompt "/compact snapcompact"` |
| shake / snapcompact / fresh | `/shake`、`/compact snapcompact`、`/fresh` | 经 `prompt` | 均 ✅ |
| clear / delete | `/clear`、`/delete` | 仅 `/session delete` 经 `prompt` | `/clear` 仅 TUI |
| rename | `/rename`(lifecycle:680) | `set_session_name`(R:140) + `session_info_update` | |
| pin / unpin | `/pin`、`/unpin`、`Alt+K` | 经 `prompt` | 18.19 新增 `/unpin`(§10) |
| move / wt / dirs | `/move`、`/wt`、`/add-dir`… | 经 `prompt` | 流式中拒绝 |
| export html | `/export` | `export_html`(R:131) | |
| share | `/share` | 经 `prompt` | 文本输出 |
| dump | `/dump` | 经 `prompt` + `get_state.systemPrompt/dumpTools` | |
| import Claude/Codex | `/resume @claude\|@codex` | 无 | |
| checkpoint/rewind | 模型工具 | 仅工具事件 | `checkpoint.enabled` 默认 false(doc tools/checkpoint.md) |
| retry | `/retry`、`F5` | 经 `prompt "/retry"`;`set_auto_retry`/`abort_retry`(R:121-122) | |
| 会话统计 | `/session info` | `get_session_stats`(R:129) | |
| 消息读取 | — | `get_messages`/`get_messages_page`/`get_last_assistant_text` | 分页防超帧(doc rpc) |

### 输入

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| 发送 / steer / follow-up | Enter / `Ctrl+Enter` | `prompt{streamingBehavior}`、`steer`、`follow_up`(R:53-55) | 确认语义见 doc rpc prompt payload |
| 队列管理 | `Alt+Up`、chip | `remove_queued_message`、`promote_queued_message`、`abort_and_restore_queue`(R:56-60) | 老运行时不支持,不得回退到 `steer` |
| 队列模式 | 设置 | `set_steering_mode`/`set_follow_up_mode`/`set_interrupt_mode`(R:109-111) | |
| 中断 | Esc | `abort`(R:58)、`abort_and_prompt`(R:59) | |
| bash `!`/`!!` | 编辑器前缀 | `bash{excludeFromContext?}`、`abort_bash`(R:125-126) | 先检查 `ready.supportsBashExcludeFromContext` |
| python `$`/`$$` | 编辑器前缀 | **无** | input-controller:225-253 |
| `@` 提及 / 补全 | 编辑器 | 无 | 客户端自行实现 |
| 图片 / 视频 | 粘贴 / 拖放 | `prompt.images` | 视频→联系表仅 TUI |
| 大段粘贴折叠 | 编辑器 | 无(客户端) | |
| 历史搜索 | `Ctrl+R` | 无(客户端) | `history.db` 本地 |
| 幽灵文本 | `spelling.autocomplete` | `predict_word`/`predict_word_feedback`(R:154) | |
| 听写 / 实时语音 | `Space`、`/live` | `dictation_*`、`live_*`(R:89-96) | |
| magic keywords | 自由文本 | 无需命令,服务端处理 | 客户端可做高亮 |
| `/queue`、`->`、`=>` | 前缀 | 无(用 `follow_up`) | |
| 外部编辑器 / Vim | `Ctrl+G` | 无(客户端) | |

### 展示

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| 状态栏数据 | 状态栏 | `get_state`(R:65)、`get_session_stats`(R:129) | git/cwd/MCP/后台作业**无** |
| 上下文占用 | `context_pct` 段 | `get_state.contextUsage` | |
| tokens/s | `token_rate` 段 | `get_state.tokensPerSecond` | |
| 成本 | `cost` 段 | `get_session_stats.cost` | 含子 agent 成本需自行聚合 |
| 重试/回退提示 | 通知 | 事件 `auto_retry_*`、`retry_fallback_*`(E:65-83) | |
| cache warming | 状态栏 `cache` | 事件 `cache_warming_*`;`set_cache_warming`(R:118) | |
| TTSR | 通知 | 事件 `ttsr_triggered`(E:88) | |
| todo 提醒 / HUD | todo 面板 | 事件 `todo_reminder`、`todo_auto_clear`;`get_state.todoPhases`;`set_todos` | `blocked` 状态在 Unreleased 被移除(CHANGELOG:31) |
| 通知 | 状态消息 | `notice` 事件(E:92) | |
| IRC | 名册未读 | `irc_message` 事件(E:91) | |
| 命令输出 | 状态行 | `command_output` 帧 | |
| 快捷键列表 | `/hotkeys` | 无 | |
| 主题 | `omp://theme.md` | 无(文本配置) | |
| 转录压缩分隔线 | 显示转录 | 数据在 `get_entries` 里的 `compaction` 条目 | `buildSessionContext({transcript:true})` 语义(doc compaction「Display transcript」) |

### 工具

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| 工具调用渲染 | 转录 | `tool_execution_start/update/end`、`tool_stream_update`(`agent/src/types.ts:1352-1355`) | 渲染器与 details 见 §6 |
| ask 表单 | 对话框 | `extension_ui_request` `select/input/editor`,或 `ask`(需 `set_ask_dialog`) | 18.18.3 起为 HTML 表单 |
| 工具审批 | 停靠提示 | 带同样选项的 `select`(doc approval-mode) | 无结构化接口 |
| 计划审批 | 覆盖层 | **无**(§2.9) | |
| bash PTY | `Console` 覆盖层 | 无(`rpc-ui` 设 `PI_NO_PTY=1`) | |
| 宿主工具 | — | `set_host_tools`(R:80)、`host_tool_*` 帧 | |
| 宿主 URI | — | `set_host_uri_schemes`(R:81)、`host_uri_*` 帧 | |

### 模式

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| plan | `/plan`、`Alt+Shift+P` | **无** | §2 |
| goal | `/goal` | `goal`(R:68-74)、`goal_updated` | |
| vibe | `/vibe` | **无** | |
| loop | `/loop` | **无** | |
| prewalk | `/prewalk` | 经 `prompt` | |
| advisor | `/advisor` | 经 `prompt`;RPC 默认关 | |
| fast / slow | `/fast`、`/slow` | `set_fast_mode`、`set_slow_mode` | |
| thinking | `Shift+Tab`、`/effort` | `set_thinking_level`、`cycle_thinking_level` | |
| force tool | `/force` | 经 `prompt` | |
| approval mode | 设置 | **无** | |
| pause | `/pause` | **无** | |

### 子 agent

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| 子 agent 订阅 | Agent Hub | `set_subagent_subscription{off\|progress\|events}`(R:82) + 帧 `subagent_lifecycle/progress/event`(R:703-716) | |
| 名册 | Agent Hub(`Alt+A`、`/hub`) | `get_subagents`(R:84) → `SubagentSnapshot` | |
| 读转录 | Hub 聚焦 / 查看器 | `get_subagent_messages{subagentId\|sessionFile, fromByte}`(R:85) | 支持增量 tail |
| 对话(steer) | Hub 聚焦后输入 | `steer_subagent`(R:87) | 对 parked/killed 是否复活,RPC 侧[推测:同 `agent://` 写入语义] |
| kill | Hub `x` | `cancel_subagent`(R:86) | 硬杀 |
| revive | Hub `r` | **无独立命令** | |
| /tan 后台 agent、/seance | 仅 TUI | 无 | |
| /btw | `/btw` | `btw`、`btw_cancel`、`get_btw_history` | |

### 管理

| 功能 | TUI 入口 | 上游 RPC | 备注 / 依据 |
|---|---|---|---|
| 模型切换 | `/model`、`Alt+P/M`、`Ctrl+P` | `set_model`、`cycle_model`、`get_available_models` | |
| 登录 / 登出 | `/login`、`/logout` | `get_login_providers`、`login`、`get_logout_accounts`、`logout`(R:147-151) | `secret` 输入拒绝 |
| MCP | `/mcp` | 经 `prompt`(文本) | 无结构化状态 |
| skills | `/skill:*`、`/skills` | `/skill:*` 可用;`/skills` 无 | |
| plugins/marketplace | `/marketplace`、`/plugins` | 经 `prompt` | |
| hooks / 扩展 | `/extensions` | `extension_ui_request/response`、`extension_error` | |
| memory / ssh / worktree | `/memory`、`/ssh`、`/wt` | 经 `prompt` | |
| usage / stats / context | `/usage`、`/stats`、`/context` | 经 `prompt`(文本);结构化走 `get_session_stats` | |
| 设置 / 键位 / 主题 | `/settings` | 无(文本配置) | |

---

## 10. 18.15.0(旧快照)与 18.19.0(现树/文档)不一致之处

**直接观察到的差异**(同一路径先后两次读取):

| 项 | 旧快照(18.15.0 迹象) | 现在(18.19.0 + Unreleased) | 证据 |
|---|---|---|---|
| `/unpin` | 无,只有 `/pin` 一个 toggle | 有,`builtin-lifecycle.ts:476`;`pin` 的 `handle` 里多了 `runtime.notifySessionPinChanged?.()` | 我两次 grep 的对比;RPC 实测列表含 `unpin` |
| RPC `bash` | `{command}` | `{command, excludeFromContext?}`,`ready.supportsBashExcludeFromContext`(`rpc-types.ts:125`、`:383`) | `CHANGELOG.md:440`(18.18.1 段) |
| `/restart` | 描述为「resuming this session」 | 「…and its running subagents」(`lifecycle:900`) | `CHANGELOG.md:465` |
| `ask` | 问答式 | HTML `<form>`;RPC 逐字段 `select/input/editor` | `CHANGELOG.md:36`、`:326-327`(18.18.3 段) |
| 注册文件行号整体平移 | `lifecycle` 的 `ssh` 在 137、`pin` 在 445 | 154、461(+16/+17) | 两次读取对比;`session`、`control` 也有 +1~+5 位移 |
| `/session delete` 在 RPC/ACP | — | 先切换活动会话再删文件 | `CHANGELOG.md:176` |
| 其余 | | | |

**18.19.0 之后、尚未发布(现树独有,本机 18.19.0 二进制没有)**,均来自 `CHANGELOG.md:3-96` 的 `[Unreleased]`:
- todo 的 `blocked` 状态与 `blocker` 备注被删除,同时影响 RPC 的 `TodoStatus/TodoItem` 线类型(`:31`)。
- 移除 task 工具的 `taskFile`/`task.file`(`:30`)。
- RPC `select` 请求新增 `initialIndex/selectionMarker/checkedIndices/markableCount`(`:71`)。
- 仅 TUI:`/browser visible|headless` 立即显隐 WebKit 窗口(`:39`)。

**文档与源码的已知偏差 / 需留意**:
- `omp://slash-command-internals.md` 与 `omp://tree.md` 把 `/tree`、`/branch` 的实现文件写成 `builtin-session.ts`,与源码一致(`session:627`、`:677`)。
- `omp://tree.md` 说 `/branch` 在**同一文件内**移动 leaf,而 RPC 的 `branch` 命令对应的是「程序化 `AgentSession.branch()`」,会创建独立分支会话。两者不是一回事。
- `omp://session-operations-...md` 称 `/fork` 在流式中被拒绝,但 RPC `fork` 额外以 `requireIdle:true` 在**任何**忙状态(含 bash/eval/compaction/handoff/retry)下返回 `session_busy`。交互式 `/fork` 不带这个检查,可能把运行中的 bash 带进新会话。
- 本机 `omp` 二进制是 18.19.0,而参考源码已含 Unreleased 变更;对照时以「发布版文档 + 实测 `get_available_commands`」为准。

---

## 11. 未解事项与对 Oryn 的建议

**未找到 / 未验证**
- fork(`nornzach/oh-my-pi`)新增的 RPC 命令:**未读**。
- `tts`、`generate_image` 工具的注册位置:**未找到**(只有文档)。
- `omp --help` 输出:未运行。
- RPC 下 `steer_subagent` 对 parked/killed 子 agent 是否会自动复活:源码未逐行确认,[推测] 与 `agent://` 写入一致。

**建议的客户端分层**
1. **RPC 已覆盖的会话/队列/模型/goal/子 agent/btw/语音/听写**:直接映射到对应命令与事件。
2. **只有 `handle` 的 47 个命令**:统一经 `prompt "/xxx"` 发送,只能得到 `command_output` 纯文本。需要结构化展示的(jobs、usage、context、MCP 状态)应请 fork 补结构化 RPC。
3. **TUI 专属命令**(`/plan`、`/vibe`、`/loop`、`/tree`、`/branch`、`/fork` 子形态、`/clear`、`/new`、`/resume`、`/btw`、`/tan`、`/seance`、`/pause`、`/skills`、`/collab` 系列等):客户端必须拦截,不能当文本发出去。其中能映射的:`/tree`→`get_tree`+`navigate_tree`;`/new`→`new_session`;`/resume`→`switch_session`;`/btw`→`btw`;`/login`→`login`。其余需要 fork 或上游新增 RPC。
4. **计划模式**是最大缺口:需要 §2.9 的整套接口。`PlanReviewRequestedEvent` 和 `ExtensionSessionModes` 已有可复用的类型。
5. **客户端自实现**:输入区补全、粘贴折叠、历史、Vim、状态栏的 git/cwd 段、转录渲染(按 §6 的 details 类型)。

### Critical Files for Implementation

- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/slash-commands/builtin-registry.ts`:命令总表与 RPC/TUI 分流的判定依据(`:43-59`),配合 `acp-builtins.ts`、`available-commands.ts`。
- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/modes/rpc/rpc-mode.ts`:RPC 命令分发,`prompt` 里内置命令派发在 `:1943-2000`,命令 `switch` 从约 `:2063` 起;配套 `rpc-types.ts` 与 `wire/{commands,frames,state}.ts`。
- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/modes/interactive-mode.ts`:计划模式全部状态机(`#enterPlanMode` `:5993`、`#tearDownPlanMode` `:6144`、`#approvePlan` `:6784`、`handlePlanApproval` `:7721`、`openPlanReview` `:7658`);需下沉到服务端才能支持 RPC。
- `/root/Projects/GitHub/oh-my-pi/packages/coding-agent/src/session/agent-session.ts`(`preparePlanForReview` `:2129`、`setPlanProposalHandler` `:3454`)与 `tools/resolve.ts`(`xd://propose` 派发 `:285-301`)、`tools/plan-mode-guard.ts`(只读守卫)。
- `/root/Projects/GitHub/oh-my-pi/packages/tui/src/tools/index.ts`(工具渲染器注册 `:34-70`)与 `tools/renderer.ts`(渲染契约),以及 `status-line/{schema,types,presets}.ts`(状态栏段与 `SegmentContext`):客户端展示层的对照基线。
