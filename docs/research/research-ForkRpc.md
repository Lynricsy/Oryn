# 调研报告:nornzach/oh-my-pi(omp fork,GUI 扩展 RPC)

调研时间 2026-11-12(UTC),fork 与上游 can1357/oh-my-pi 均为实时状态。以下均为亲自读到的内容,推测处标 `[推测]`。

## 0. 结论速览

1. **fork 对 agent 核心的改动不大,主要是加 RPC 层。** 新增约 40 个 `modes/rpc/rpc-*.ts` 文件,并让 `rpc-mode.ts` 的 `handleCommand` 巨型 switch 多出几十个 case(该文件 +2869/−344)。
   - 少量核心改动:`Agent` 队列稳定 id、`chat` 会话种类、`terminal` 工具、提示词里的 `{{#if panel}}` 分支、`rpc-ui` 自动取标题。
2. **扩展命令的协议类型只在手写的 `rpc-types.ts` 里。** `wire/commands.ts`、`rpc-wire.schema.json` 和 `sdk/go/omp-rpc/wire.go` 只多了 3 项:`discover_provider_models`、`set_api_key`、`set_ask_dialog.form`(外加 `promptCache`、Form UI 帧)。
   - 我 grep 过 fork 的 schema:`terminal_*`、`get_transcript`、`worktree_*`、`get_queue`、`plan_approval`、`switch_leaf`、`collab_*`、`get_usage`、`get_settings`、`get_jobs` 均 0 命中,Go 的 `wire.go` 里也没有 `terminal_open`、`worktree_create`、`get_transcript`。
   - 所以 Go 后端要么用 `Client.Call` 手写扩展命令的类型,要么先把这些命令补进 `wire/commands.ts` 再 `bun run gen:rpc`。
3. **在无头 Linux 容器里用 `--mode rpc-ui` 的 JSONL 驱动基本没障碍。** 需注意的点见 §4。
4. **这是个"领先 166、落后 359"的深度分叉。** 同步用 `git merge upstream/main`,冲突集中在 `rpc-mode.ts`、`rpc-types.ts`、`agent-session.ts`。再 fork 一层是可行的,但要接受长期的合并成本。

---

## 1. fork 概况

| 项 | 值 | 来源 |
|---|---|---|
| 默认分支 | `main`(另有 `gui-v0.3.1-runtime`) | https://api.github.com/repos/nornzach/oh-my-pi , `/branches` |
| HEAD | `4b67dcb3174b412f2c236b7c993e3ad244520654`,2026-11-12T05:46:32Z,"feat(rpc): let hosts sign a provider in with a pasted API key" | `/commits` |
| 创建/最近 push | 2026-08-05 / 2026-11-12T05:47:22Z | API |
| 许可证 | MIT(上游同) | API `license` |
| 对应上游版本 | `packages/coding-agent/package.json` version = **18.19.0** | fork raw package.json |
| 相对上游 main | `status: diverged`,**ahead 166 / behind 359** | https://api.github.com/repos/nornzach/oh-my-pi/compare/can1357:main...nornzach:main |
| merge base | `4ad629ea`(can1357,2026-11-12T04:35:18Z,`--account` flag) | 同上 `merge_base_commit` |
| 上游当前 HEAD | `52ff19a2`(2026-11-12T11:04:55Z) | 同上 `base_commit` |
| GUI 固定 commit | `sidecar-source.json` = `{"repo":"nornzach/oh-my-pi","commit":"4b67dcb3174b412f2c236b7c993e3ad244520654"}`,恰好是 fork HEAD | https://raw.githubusercontent.com/nornzach/oh-my-pi-gui/main/sidecar-source.json |

- **本机上游**:`/root/Projects/GitHub/oh-my-pi` 是 18.15.0。fork 基于的是 18.19.0,比本机新 4 个小版本。本机没有 `debug`、`collab_*` 等命令,没法拿它判断哪些命令是 fork 新增的。我另外 grep 了 can1357 的 `4ad629ea` 版 `rpc-types.ts`,确认那里也没有。
- **旁支分支** `gui-v0.3.1-runtime` 是 2026-08-05 的两个旧提交,落后 21093 个提交,已无用。
- **GUI 仓库**:`nornzach/oh-my-pi-gui`,**Electron**(不是 Tauri),版本 `0.9.42`,MIT,155 星(https://api.github.com/repos/nornzach/oh-my-pi-gui , `package.json`)。

### 同步方式

- 脚本是 `scripts/sync-upstream.sh`:`git fetch upstream` → `git merge upstream/main --no-edit`(**merge,不是 rebase**)→ `bun install` → natives 重新准备 → `gen:stats` → `build:omp` → `$GUI/resources/omp --smoke-test` → GUI build/check:types/test → 把 monorepo commit 写进 `sidecar-source.json`。
  - 冲突时手工解决、提交,再用 `SKIP_MERGE=1` 续跑。
  - 来源:https://raw.githubusercontent.com/nornzach/oh-my-pi-gui/main/scripts/sync-upstream.sh
- fork README 横幅写的是 `git fetch upstream && git merge upstream/main   # or rebase the fork commits onto it`。
- 实际历史:最近页可见 `6e5fe6a4 "Merge remote-tracking branch 'upstream/main'"`(2026-11-12T05:04:15Z),也就是每次发版前同步。GUI AGENTS.md 规定"Every release starts with a sync"。
- 未读全历史,**同步频率的精确值未找到**。
- 仓库布局很特殊:GUI 仓库嵌套在 monorepo 的 `packages/gui/`,有独立 `.git`,monorepo 视角下它是 untracked。fork 的 AGENTS.md 新增了整节"omp GUI Sub-Repository"(+45 行)讲三仓关系:`origin`=fork,`upstream`=can1357(绝不 push)。

---

## 2. fork 改动的文件清单

数据来自 compare API,三点比较(merge base `4ad629ea` 到 `4b67dcb`),所以所列全是 fork 自己的改动。**API 只返回前 300 个文件,字母序在 `packages/stats/...` 处被截断**,因此 `sdk/`、`crates/`(除 1 个)、`packages/tui` 等之后的目录看不到。靠后的文件 additions/deletions 显示为 0,不可信。

### 2.1 `packages/coding-agent/src/modes/rpc/`(核心)

**纯新增(status=added)**,行数为 additions:

| 文件 | 行数 | 内容(据文件头注释) |
|---|---|---|
| `rpc-terminal.ts` | 665 | `RpcTerminalHost`:PTY 托管 |
| `rpc-terminal-tool.ts` | 475 | agent 的 `terminal` 工具 |
| `rpc-terminal-session.ts` | 411 | agent 驱动的 shell 会话(本机/SSH) |
| `rpc-mcp-extra.ts` | 907 | MCP 管理 |
| `rpc-modes.ts` | 614 | vibe / goal / loop 控制器 |
| `rpc-pr.ts` | 365 | PR(`gh` CLI) |
| `rpc-plan.ts` | 344 | `RpcPlanApprovalController` |
| `rpc-operations.ts` | 360 | |
| `rpc-domains.ts` | 379 | skills/hooks/plugins/themes 读取 |
| `rpc-collab.ts` | 240 | 协作 |
| `rpc-plugins.ts` | 235 | |
| `rpc-marketplace.ts` | 217 | |
| `rpc-actions.ts` | 228 | |
| `rpc-host-dictation.ts` | 211 | |
| `rpc-extensions.ts` | 220 | |
| `rpc-worktree.ts` | 167 | git status / worktree |
| `rpc-workspace.ts` | 169 | 工作区目录 |
| `rpc-subagent-restore.ts` | 263 | |
| `rpc-session-actions.ts` | 155 | |
| `rpc-custom-ui.ts` | 158 | |
| `rpc-reports.ts` | 131 | context / tools / share / jobs 报告 |
| `rpc-completions.ts` | 123 | |
| `rpc-agents.ts` | 125 | |
| `rpc-session-tree.ts` | 105 | |
| `rpc-git-diff.ts` | 94 | |
| `rpc-foreign.ts` | 91 | |
| `rpc-queue.ts` | 80 | |
| `rpc-memory.ts` | 102 | |
| `rpc-model-roles.ts` | 85 | |
| `rpc-skills.ts` | 69 | |
| `rpc-browser-host.ts` | 58 | 浏览器面板中继 |
| `rpc-session-extra.ts` | 70 | |
| `rpc-paste.ts` | 45 | |
| `rpc-omfg.ts` | 41 | |
| `rpc-tan.ts` | 27 | |
| `prompts/pr-draft-{system,user}.md` | 11 / 14 | |

**改动上游文件**:

| 文件 | 改动 |
|---|---|
| `rpc-mode.ts` | +2869/−344,巨型 switch,是主冲突点 |
| `rpc-types.ts` | +3390/−169 |
| `rpc-client.ts` | +157/−27 |
| `rpc-btw.ts` | +165/−20 |
| `rpc-subagents.ts` | +154/−6 |
| `rpc-messages.ts` | +41 |
| `rpc-input.ts` | 仅给 `readRpcInputFrames` 加 `signal?: AbortSignal` |
| `wire/commands.ts` | +37/−1 |
| `wire/frames.ts` | +64/−2 |
| `wire/state.ts` | +44 |
| `wire/rpc-wire.generated.ts` | +193/−2 |
| `wire/rpc-wire.schema.json` | +669 |

### 2.2 其它改动

- **会话**:
  - 新增:`session/chat-session.ts`、`session/chat-settings.ts`(chat 种类,无工具)、`session/ask-reanswer.ts`。
  - 修改:`session/agent-session.ts`(+818/−192)、`session-manager.ts`(+145/−12)、`session-loader.ts`、`session-entries.ts`(+7)、`async-job-views.ts`、`tool-choice-queue.ts` 等。
- **`packages/agent/src/agent.ts`**:+117/−13。给队列加稳定 id(`s1`/`f1`,WeakMap),新增 `removeQueuedMessage`、`moveQueuedMessage`、`queueEntryId`,并把 `onQueueChange` 挪位。这是真正的核心改动。
- **其它核心文件**:`task/executor.ts` +158、`main.ts` +88/−40、`memories/index.ts` +390、`memories/storage.ts` +261、`memories/workbench.ts` 新增 423、`modes/settings.ts` +33、`config/settings.ts` +28、`sdk.ts`、`system-prompt.ts` +3 等。
  - 具体内容我只读了 `main.ts`、`agent.ts`、`system-prompt.ts` 三处 patch。
- **提示词**:
  - 新增 `prompts/chat-mode.md`、`prompts/tools/terminal.md`。
  - 修改 `tools/browser.md`(`{{#if panel}}` 块)、`memories/read-path.md`(`{{#if notes}}` 项目笔记)、`system/date-cwd-reminder.md`(`cwd` 可省)。
- **其它新增文件**:`utils/git-status.ts`、`plan-mode/plan-save.ts`、`advisor-config-overlay.ts`、几十个 `test/rpc-*.test.ts` 和 `fixtures/`。
- **仓库杂项**(与 agent 无关的噪声):
  - 根目录 `index.js`(+39263 行,疑似误提交)。
  - `design-qa.md`、`.codegraph/.gitignore`。
  - `.devin/skills/{gui-release,upstream-sync}/SKILL.md`。
  - `packages/coding-agent/CHANGELOG.md` +4512 行(`[推测]`:fork 里 changelog 与上游分叉)。
  - `bun.lock` +1884/−447。
- **领域归类**:
  - 终端:`rpc-terminal*.ts`
  - 浏览器 host:`rpc-browser-host.ts`
  - git/worktree/PR:`rpc-worktree.ts`、`rpc-git-diff.ts`、`rpc-pr.ts`
  - 设置:`modes/settings.ts`、`rpc-mode.ts`(`get_settings*` 在 switch 里)
  - 会话树:`rpc-session-tree.ts`、`rpc-session-extra.ts`
  - 队列:`rpc-queue.ts` + `agent.ts`
  - plan/goal/vibe/loop:`rpc-plan.ts`、`rpc-modes.ts`
  - 子 agent:`rpc-agents.ts`、`rpc-subagents.ts`、`rpc-subagent-restore.ts`
  - 协作:`rpc-collab.ts`(复用上游 `collab/host`、`collab/guest`)
  - 语音:`rpc-host-dictation.ts`
  - MCP/插件/技能:`rpc-mcp-extra.ts`、`rpc-marketplace.ts`、`rpc-plugins.ts`、`rpc-skills.ts`
  - 安全扫描:命令在 `rpc-types.ts`,实现文件未在可见列表中(`secrets/settings.ts` 仅 +1)

---

## 3. 扩展 RPC 命令表

### 3.0 通用约定

- **传输**:stdin/stdout JSONL。请求 `{id?, type, ...}`,响应 `{id?, type:"response", command, success:true, data?}` 或 `{..., success:false, error, code?}`。来源 `rpc-types.ts` RpcResponse 联合,`@4b67dcb` L2380–L3750 区间,错误响应在 L3750 附近。
- **握手**:`ready` 帧宣告 `protocolVersion:1`、`supportedProtocolVersions:[1,2]`、`maxFrameBytes`、`maxReassembledFrameBytes`(`rpc-mode.ts` L5464)。随后 `negotiate_protocol` 只接受 2,响应 `{protocolVersion:2}`(L2746–2749)。
- **串行队列**:非后台命令串行执行。`BACKGROUND_COMMANDS`(`rpc-mode.ts` L628–L720)里的命令绕过队列。
  - 含:`bash`、`eval`、`collab_*`、`debug`、`get_git_changes`、`get_git_diff`、`get_omp_update`、`get_usage`、`import_foreign_session`、`list_foreign_sessions`、`live_*`、`login`、`marketplace_action`、`mcp_*`、`pr_*`、`worktree_*`、`security_start`、`ssh_test`、`set_api_key`、`share_session`、`transcribe_audio`、`synthesize_speech`、`terminal_*`(含 `terminal_input`)、`browser_host_*`、`tui_open`、`host_dictation_*`、`abort_branch_summary`、`btw_cancel` 等。
  - `compact`/`handoff`/`omfg`(以及带 compact 的 `plan_approval`)是"串行起、不阻后续"。`switch_leaf` 保持串行。
- **协作只读**:guest 在只读协作里只允许 `get_*`、`list_*`、`preview_*`、`terminal_*` 和少数控制命令(`rpc-collab.ts` L10–24)。
- **busy 保护**:`get_transcript_page` 在 streaming/compacting 时返回 `code:"session_busy"`(`rpc-mode.ts` L4411–4413)。`clear_context` 同样返回 `busy`。
- **源码锚点**:命令定义在 `rpc-types.ts` RpcCommand 联合(L86–L613),响应在 RpcResponse 联合。下表里的行号是 `rpc-types.ts@4b67dcb` 的命令定义行。

### 3.1 终端 / 浏览器 host(重点)

| 命令 | 参数 | 返回 data | 相关帧/说明 |
|---|---|---|---|
| `terminal_open` | `cols?, rows?, cwd?` | `RpcTerminalInfo` | 开一个登录 shell(L180) |
| `terminal_input` | `terminalId, data` | 无 | 退出的终端静默丢弃输入;虚拟终端转 `onInput`(L185) |
| `terminal_resize` | `terminalId, cols, rows` | 无 | cols 钳制到 2–1000,rows 到 1–500(L186) |
| `terminal_close` | `terminalId` | 无 | 运行中则杀进程,退出后 `terminal_closed`;已退出则直接忘掉(L187) |
| `terminal_list` | — | `{terminals: RpcTerminalInfo[]}` | L188 |
| `terminal_snapshot` | `terminalId` | `{terminal, data, seq}` | L189;先 flush 待发数据 |
| `terminal_configure` | `interactiveCommands: bool, agentTools?: bool` | `{interactiveCommands, agentTools}` | L195;见下 |
| `terminal_run_cli` | `subcommand: "git"\|"cleanse", args?` | `RpcTerminalInfo` | 在 revealed 终端里跑 omp 子命令(L182) |
| `tui_open` | `surface: "advisor_config"` | 无 | 把 TUI-only 界面挂到终端视图(L184) |
| `browser_host_start` / `browser_host_stop` | — | `{extensionUrl}` / 无 | 见下 |

- **`RpcTerminalInfo`**(L1041):`terminalId`(形如 `term-<8hex>-<n>`,每进程随机前缀)、`kind: shell|command|background|session`、`title`、`cwd`、`running`、`exitCode?`、`pid?`、`cols`、`rows`、`host?`、`state?: starting|idle|busy|input`、`root?`。
- **出站帧**(unsolicited):
  - `terminal_opened {terminal, reveal?}`
  - `terminal_output {terminalId, data, seq}`
  - `terminal_exit {terminalId, exitCode?, cancelled, timedOut}`
  - `terminal_closed {terminalId}`

**`seq` 语义**(`rpc-terminal.ts` `#append`/`#flush`/`snapshot`):
1. `seq` = 该终端迄今**已通过 `terminal_output` 发出的字符数**(JS 字符串 length,即 UTF-16 码元),**在本帧 `data` 末尾处**的累计值。
2. 输出按 8ms 窗口合并,待发缓冲 ≥32K 字符时立刻 flush。
3. `snapshot` 先 flush,所以此前写出的每个 `terminal_output` 都满足 `seq <= snapshot.seq`,且其内容已包含在 `snapshot.data` 里;之后的帧 `seq` 更大。
4. 客户端重连/重挂载的做法:拿 snapshot 全量重绘,再丢弃 `seq <= snapshot.seq` 的帧。
5. 每终端滚动缓冲最多 256K 字符,超出从最近换行处裁剪。所以 `snapshot.data` 不一定从 0 开始,`seq` 是累计量而非 `data` 下标。
6. 进程退出后终端保留滚动缓冲和退出状态,直到 `terminal_close`。已退出的非 shell 终端最多保留 5 个,更早的会自动 `terminal_closed`。

**`pty:true` 命令怎么路由**:
- `rpc-ui` 模式在 `main.ts` 设置 `PI_NO_PTY=1`,所以默认 `pty:true` 的 bash 不走终端。
- `terminal_configure {interactiveCommands:true}` 把 `rpcUiContext.interactiveTerminal = terminalHost.asInteractiveTerminal()`(`rpc-mode.ts` L3333)。之后 `pty:true` 的 `bash` 调用改在 host 终端里跑:发 `terminal_opened {reveal:true}`,`kind:"command"`,agent 等它退出。终端保持打开且只读,直到 host 关闭。
- `terminal_configure {agentTools:true}` 注册 agent 的 **`terminal` 工具**(`TerminalTool`)。工具 `loadMode="essential"`,参数 `action: list|read|run|stop|open|exec|send|wait`,另有 `id, lines, command, host, label, cwd, text, key, wait`(`rpc-terminal-tool.ts`)。
  - `run` 开 `background` 终端。
  - `open/exec/send/wait` 驱动 `session` 终端,可 SSH。
  - `read` 通过 xterm 回放 scrollback 给纯文本。
  - `run/open/exec/send` 沿用 `bash` 的审批规则。
  - 密码、passphrase、host-key 提问由用户在终端里答,agent 不得 `send`。
  - 最多 8 个活动 session。
  - 提示词在 `prompts/tools/terminal.md`。
- 终端是 **per-process** 的,不是 per-session。登录 shell 取 `$SHELL`(绝对路径且可执行),否则用 agent 配置的 shell,POSIX 加 `-l`。

**`browser_host_start`**(`rpc-browser-host.ts`):在 loopback 起一个私有浏览器 relay,返回 `extensionUrl = ws://127.0.0.1:<port>/ext?token=…`。**host 必须以"浏览器扩展"身份拨入**,用自己的 webview 当 tab。之后 agent 的 `browser` 工具开页面就走这个 relay 而不是 Chromium。每条腿都有 secret。`browser_host_stop` 恢复原行为。提示词的 `{{#if panel}}` 块就是给这个模式用的。

### 3.2 计划 / 会话模式

| 命令 | 参数 | 返回 | 帧 |
|---|---|---|---|
| `set_plan_mode` | `enabled` | `RpcPlanModeState {enabled, planFilePath?}` | 启用时 `planFilePath:"local://PLAN.md"`;`session.restrictToolNames`(chat 会话)下启用返回 `code:"mode_unavailable_in_chat"`(`rpc-mode.ts` L4739–4764) |
| `get_plan_mode` | — | `RpcPlanModeState` | |
| `plan_approval` | `approved, option?: execute\|compact\|keep_context\|save, feedback?, savePath?` | `{approved, dispatched, reason?, savedPath?, freshSessionStarted?}` | L355–361 |
| `get_vibe_mode` / `set_vibe_mode` | `enabled` | `{enabled, killedWorkers?}` | |
| `get_goal` / `set_goal` | `objective?, tokenBudget?: number\|null, action?: pause\|resume\|drop` | `RpcGoalState` | 另有上游的 `goal` 命令 |
| `guided_goal` | `initial?` | `{started:true}` | 注入访谈 prompt |
| `get_loop_mode` / `set_loop_mode` | `enabled, args?`(原始 `/loop` 参数串) | `{enabled, state: off\|waiting\|running\|paused, prompt?, limit?}` | `loop_mode_update {state}` |
| `set_agents_paused` | `enabled` | `{paused, pausedAt?, heldMs?}` | |
| `btw_branch`、`tan`、`omfg` | | `{cancelled, sessionFile?}` / `{jobId}` / `{state: saved\|rejected\|aborted, savedPath?}` | |

**plan 帧**:`plan_proposal {planFilePath, title, suggestedFileName, planContent, options}`(`RpcPlanProposalFrame`,L1463)。
- agent 在 plan mode 下 `write` 到 `xd://propose` 时发出。
- `options` 恒为 `["execute","compact","keep_context","save","refine"]`。其中 `refine` 不走 `option`,而是 `plan_approval {approved:false, feedback}`。
- 机制:`set_plan_mode` 调 `planApprovalController.syncArmed()`,安装自己的 `PlanProposalHandler`,并让提案后 abort,避免模型在 host 决定前循环重提。
- `execute` 会清空上下文开新会话,`compact` 先压缩,`keep_context` 在原 transcript 上执行,`save` 只写文件。

### 3.3 transcript / 会话树 / 队列

| 命令 | 参数 | 返回 | 说明 |
|---|---|---|---|
| `get_transcript` | — | `{messages: AgentMessage[]}` | 活动分支的**完整展示历史**,不是 LLM 上下文(`rpc-mode.ts` L3998) |
| `get_transcript_page` | `cursor?, limit?` | `RpcMessagesPage` | 同 `get_messages_page` 的分页;busy 时报 `session_busy` |
| `get_session_tree` | — | `{tree: RpcSessionTreeNode[], activeLeafId}` | 节点:`entryId, parentId, role: user\|assistant\|system, textPreview(~200 字), timestamp, label?, onActiveBranch, isLeaf`(L2036) |
| `switch_leaf` | `entryId, summarize?, customInstructions?` | `{cancelled, aborted?, reopenAsk?{toolCallId,html}, editorText?, editorImages?, activeLeafId?, askReanswerCommitted?}`(L805) | 同文件内移动叶子(`session.navigateTree`) |
| `fork_from` | `entryId` | `{sessionPath, sessionId}` | 写一个**独立新会话文件**(根到 entryId 的路径),**不切换当前会话** |
| `resume_after_ask_reanswer` | — | 无 | `askReanswerCommitted` 后,在重建 transcript 之后调用 |
| `get_queue` | — | `{steering: RpcQueuedMessage[], followUp: [...]}` | `RpcQueuedMessage {id, text, images?, editable, timestamp}`,id 形如 `s1`/`f1`(L246、L1247) |
| `queue_edit` | `queueId, text` | `{updated:true}` | 保留附件/投递元数据 |
| `queue_remove` | `queueId` | `{removed:true}` | |
| `queue_move` | `queueId, toIndex, toLane?` | `{lane, index}` | `toIndex` 钳制,`toLane` 可换道且 id 不变 |
| `queue_clear` | `lane?` | `{removed:n}` | 只清用户可恢复项;advisor 卡和内部 steer 保留 |

- 上游已有的相关项(非 fork 新增):`queue_update {steering[], followUp[], liveSteered}` 事件(上游 `agent-session-events.ts:109`)、`remove_queued_message`、`promote_queued_message`、`dequeue`、`get_tree`、`get_entries`、`navigate_tree`、`branch`、`fork`。
- **注意**:`queueId` 不能放在帧信封的 `id` 位,它是请求关联 id。
- 其他:`set_label`、`set_entry_label`、`get_branch_messages`、`set_session_name {name, sessionPath?}`、`move_session {path}`、`list_foreign_sessions`、`import_foreign_session {source: claude|codex, foreignId}`(返回 `{sessionPath, sessionId}`)、`write_local_paste {content}`(返回 `{name,url}`,计数器由 agent 分配)、`clear_context`(返回 `{cleared, droppedCount?}`)、`retry`(返回 `{retried}`)。

### 3.4 git / worktree / PR

| 命令 | 参数 | 返回 |
|---|---|---|
| `get_git_status` | — | `{isRepo, branch\|null, staged, unstaged, untracked}` |
| `get_git_changes` | — | `{isRepo, root, base, files:[{path, oldPath?, status}], truncated}` |
| `get_git_diff` | `path` | `{path, diff, kind: text\|binary\|large\|symlink\|directory, truncated}` |
| `worktree_create` | `name, baseCwd?, baseRef?: "HEAD"\|"default"` | `{path, branch, baseCwd}` |
| `worktree_remove` | `path, force?` | `{removed:true}` |
| `pr_repo` | — | `{available:true, repo, defaultBranch}` 或 `{available:false, reason: gh_missing\|not_a_repo\|no_github_remote}` |
| `pr_list` | `state?, limit?` | `RpcPrListItem[]` |
| `pr_get` | `number` | `RpcPrDetail` |
| `pr_diff` | `number, path` | `{diff}` |
| `pr_draft` | `base?, head?` | `{title, body}`(会调一次模型) |
| `pr_create` | `title, body, base?, head?, draft?` | `{url, number}` |
| `pr_checkout` | `number` | `{path, branch}` |

- `worktree_create` 新建分支 `omp/gui/<name>`,路径 `~/.omp/wt/gui-<name>-<hash7(primaryRoot)>`,重名自动加后缀 `-2..-100`。
  - 名称限 `[a-z0-9-]{1,41}`。
  - 错误码:`not_a_repo|invalid_name|worktree_create_failed|worktree_dirty|not_a_worktree|worktree_remove_failed`。
  - 带 repo lock;remove 对脏 worktree 报错除非 `force`,且不删分支。
  - 来源 `rpc-worktree.ts` 头部。
- `get_git_status` 用 `@oh-my-pi/pi-natives/vcs`(原生),不依赖 `git` CLI。`pr_*` 依赖 `gh` CLI。

### 3.5 设置 / 用量 / 报告 / 供应商

| 命令 | 参数 | 返回 |
|---|---|---|
| `get_settings_schema` | — | `{entries: RpcSettingEntry[], tabs}` |
| `get_settings` | `paths?` | `{values, provenance?, advisorEnabled, advisorActive}` |
| `set_setting` | `path, value` | `{path, value, savedValue?, provenance?, advisorEnabled?, advisorActive?}` |
| `get_usage` | — | `{reports: RpcUsageReport[], session: RpcUsageSessionStats}` |
| `get_context_report` | — | `{breakdown, contextWindow, model}` |
| `get_active_tools` | — | `{tools:[{name, description?, source: builtin\|mcp\|extension\|plugin}]}` |
| `get_jobs` | — | `{jobs: AsyncJobSnapshotItem[], tree?: RpcJobTreeRow[]}` |
| `preview_share_session` / `share_session` | `snapshotId?` | `{snapshotId, preview, serverUrl, store, redactionEnabled, truncated?}` / `{url, truncated?}` |
| `get_providers`、`discover_provider_models`、`set_api_key`、`get_logout_accounts`、`logout` | | 见 schema |
| `get_model_roles`、`set_model_role`、`get_model_role_metadata` | | |
| `get_chat_config`、`set_chat_config` | | chat 会话的系统提示/历史策略 |
| `get_themes`、`get_theme_colors`、`get_gui_themes` | | |

- `RpcSettingEntry`(L1348 附近)含 `path, type, value, default, label, description, tab, group, options, secret, advanced, condition, ordered, tuiOnly, restartRequired`。
  - `tuiOnly` 和 `restartRequired` 供客户端打标。
  - `get_settings` 的 `values` 是路径到值的扁平 map。
  - 设置写入后对运行中会话**即时生效**(fork 的 `applyRuntimeSetting`,见 commit `872c6fe1`)。
- `RpcUsageLimit`:`id, label, usedFraction?, used?, limit?, unit?, remainingFraction?, windowLabel?, resetsAt?, status?, notes?`。

### 3.6 子 agent / 技能 / 插件 / MCP / 安全 / SSH

| 命令 | 参数 | 返回 |
|---|---|---|
| `abort_subagent` / `revive_subagent` | `agentId` | `{ok, reason?}` |
| `get_agent_definitions` | — | `RpcAgentDefinitionsResult` |
| `get_skills`、`get_skill_detail {name}`、`set_skill_enabled`、`manage_skill {action: create\|update\|delete, name, description?, body?}` | | |
| `get_hooks`、`set_hook_enabled {hookId, enabled}` | | |
| `get_plugins`、`set_plugin_enabled`、`get_plugin_detail`、`set_plugin_features`、`set_plugin_setting`、`delete_plugin_setting`、`reload_plugins` | | |
| `get_marketplaces`、`marketplace_action {action: add\|remove\|update\|install\|uninstall\|upgrade\|list_available, marketplace?, plugin?, source?}` | | |
| `get_mcp_servers`、`mcp_action {name, action: enable\|disable\|reconnect\|remove, scope?}`、`mcp_add {name, config, scope?}`、`mcp_test {name?\|config?}`、`mcp_reauth`、`mcp_reauth_cancel` | | |
| `security_start {target}`、`security_cancel {operationId}`、`security_validate`、`security_set_disposition`、`get_security_dashboard`、`get_security_scan {scanId}` | | |
| `get_ssh_hosts`、`ssh_manage`、`ssh_test` | | |
| `get_memory_report`、`get_memory_overview/activity`、`edit_memory`、`update_memory` | | |
| `get_directories`、`add_directory`、`remove_directory` | | |
| `get_omp_update` | — | `{latestVersion, updateAvailable, checkedAt, distribution:"bundled", installStrategy:"gui-update"}` |
| `get_prompt_templates`、`get_config_warnings`、`get_advisor_status`、`list_reset_credits`、`redeem_reset_credit`、`get_last_assistant_text`、`get_copy_targets` | | |
| `set_prewalk`、`fresh`、`shake_context {mode: elide\|images\|thinking}`、`set_force_tool`、`get_force_tool` | | |
| `get_command_arg_completions {command, prefix}` | | 返回 `{items:[{value,label?,description?,hint?}]}` |
| `debug {params}` | | `{content, details?}` |

- `abort_subagent` 的 `reason`:`advisor_readonly|not_found|not_parked` 一类(来自 `plan/16` 的设计文字,实际取值以 `rpc-agents.ts` 为准,**该文件未读**)。
- 上游已有 `cancel_subagent {subagentId}` 和 `get_subagents`,`abort_subagent` 是按 registry 行 `agentId` 的另一套。

### 3.7 协作 / 语音

| 命令 | 参数 | 返回 |
|---|---|---|
| `collab_start` | `relayUrl?, view?` | `RpcCollabState` |
| `collab_join` | `link` | `RpcCollabState` |
| `collab_leave` | — | `RpcCollabState` |
| `get_collab_state` | — | `RpcCollabState` |
| `host_dictation_start/audio/stop/cancel` | `audio: {pcm16Base64}` | `{state}`;帧 `host_dictation_state/text/notice/download` |
| `transcribe_audio` | `audioBase64`(WAV PCM16 mono 16kHz), `mimeType` | `{text}` |
| `synthesize_speech` | `text` | `{audioBase64, mimeType}` |

- `RpcCollabState`(L826):`role: host|guest|null, readOnly, link?, viewLink?, webLink?, webViewLink?, participants[{name, role, readOnly?}]`。
  - "秘密写链接不进日志"。
  - `collab_start`/`collab_join` 是后台命令。
- 上游已有 `live_*`、`dictation_*`、`predict_word*`、`btw*`。

### 3.8 命令总数

`plan/16`(2026-08-05 的旧状态)写的是"现有 82 → 104"。现在 `RpcCommand` 联合明显更大,我没有逐条计数,**精确数量未统计**。

---

## 4. fork 是否改了 agent 核心行为?依赖什么?无头 Linux 能用吗?

### 4.1 改了什么

- **主要是 RPC 层**(见 §2.1)。
- **核心行为的改动**:
  1. `Agent` 队列稳定 id、`removeQueuedMessage`、`moveQueuedMessage`、`queueEntryId`、`onQueueChange` 位置调整(`packages/agent/src/agent.ts`)。
  2. 会话头里新增 `kind:"chat"`:`--chat` flag、无工具、`chatSystemPrompt` 整个替换系统提示。`createSessionManager` 在 chat 时不自动续会话。
  3. `rpc-ui` 模式 `autoTitle` 打开(原版只有交互模式有)。同一处 diff 把 `rpc-ui` 从 `PI_NO_TITLE=1` 条件里去掉了。
  4. 启动警告收集:RPC/rpc-ui 把启动期警告攒进 `session.configWarnings`,由 `get_config_warnings` 返回。
  5. rpc 模式在没有 model 时也能启动(`mode !== "rpc" && mode !== "rpc-ui" && !session.model` 才报错)。
  6. 设置即时生效:`applyRuntimeSetting`;`plan.defaultOnStartup` 在 RPC 启动路径生效;`todo_auto_clear` 事件接线。
  7. 提示词:`{{#if panel}}`(浏览器面板)、`{{#if notes}}`(项目笔记)、cwd 可省略。
  8. 新工具 `terminal`:**默认不加载**,仅 `terminal_configure {agentTools:true}` 时注册。
- **会话文件格式**:`session-entries.ts` +7、`session-manager.ts` +145、`session-loader.ts` +24,实际改动内容我**没读**;`kind:"chat"` 进了会话 header。`[推测]` 对旧版 omp 读这些文件可能有影响。
- **不是**重写工具集或系统提示主体。

### 4.2 依赖的模式 / 环境变量 / GUI 假设

- **`--mode rpc-ui` 是上游已有模式**(`cli/args.ts` 的 `Mode` 含 `"rpc-ui"`),不是 fork 新加的。它让 `ask` 等工具 UI 走协议;`--no-ui` 可关闭扩展 UI。
- 扩展命令**不单独要求 `rpc-ui`**:`terminal_configure`、`get_*` 等在 `--mode rpc` 下也可调用。`[推测]` `ask` 表单、`extension_ui_request` 需要 `rpc-ui`。
- **环境变量**:
  - `PI_RPC_EMIT_TITLE=1`:是否发标题帧(`rpc-mode.ts` L1340)。
  - `PI_NO_PTY=1`:rpc-ui 自动设。
  - `PI_NOTIFICATIONS=off`:rpc 模式自己设。
  - `PI_NO_TITLE`:见上。
  - GUI 的 smoke 脚本传 `PI_RPC_EMIT_TITLE=1 PI_NO_PTY=1 PI_NOTIFICATIONS=off`。
- **GUI/本机假设(会在容器里碰到)**:
  - `get_omp_update` 写死 `distribution:"bundled"`、`installStrategy:"gui-update"`。
  - `get_gui_themes`、`worktree_create` 的分支前缀 `omp/gui/` 和路径 `~/.omp/wt/`。
  - `browser_host_start` 需要有"浏览器扩展端"拨入 loopback WebSocket,服务端不会自己起浏览器。
  - `host_dictation_*`、`live_*` 需要 host 提供麦克风。
  - 登录流 `open_url` 要由 host 打开浏览器;用 `set_api_key` 可绕开。
  - `pr_*` 需要 `gh` CLI 且已登录。
  - `terminal_*` 需要 `pi_natives` 的 `PtySession`(Linux 有 `linux-x64`/`linux-arm64` leaf 包)。
  - 没发现 Electron 专属的 API 调用;GUI 的 `ELECTRON_RUN_AS_NODE` 只是 GUI 自己的注意事项。

### 4.3 无头 Linux 容器里 JSONL 驱动

- **没有硬障碍**。`smoke-sidecar.mjs` 就是通过 stdio 以 `--mode rpc-ui` 驱动二进制(`get_state` → `get_available_models` → `get_state` → `set_thinking_level` → `get_settings`/`set_setting` 往返)。
- 注意事项:
  1. 要处理 `ready` 帧并 `negotiate_protocol {protocolVersion:2}`,启用 `rpc_chunk` 分帧(1 MiB 帧,64 MiB 重组)。
  2. 终端可用,但容器里要有可执行的 `$SHELL`(或 omp 配置的 shell)。
  3. 浏览器面板用不了(没有 host webview);agent 自己的无头 Chromium/WebKit 仍可用。
  4. 登录和 `open_url`、`ask`、`select/confirm/input` 等 `extension_ui_request` 需要客户端回 `extension_ui_response`。
  5. `get_available_models` 曾导致串行队列卡死(smoke 脚本的存在理由),当前版本据说已有界;客户端仍应对此命令设超时。
  6. 命令基本串行,别假设并行。

---

## 5. 协议类型的权威来源

- **上游的权威来源**是 `wire/commands.ts` + `frames.ts` + `state.ts`(TS DSL)。生成物有 `rpc-wire.schema.json`、`rpc-wire.generated.ts`、`sdk/go/omp-rpc/wire.go`,以及 Python、Rust SDK。`bun run gen:rpc` 重新生成。
  - 来源:本机 `sdk/go/omp-rpc/README.md`:"`wire.go` — **generated** … Regenerate … `bun run gen:rpc`"。
- **fork 对它的维护**:fork 提交信息明确写"Regenerated the wire schema and the Go, Python and Rust SDK bindings"(`4b67dcb`)。
  - 我读到的 fork `wire.go` 有 `DiscoverProviderModels`、`SetAPIKey`、`FormUiRequest`。
  - 所以**schema 管到的那几项是同步的**。
- **fork 的缺口**:所有"GUI 扩展命令"(终端、worktree、git、PR、队列、plan、会话树、设置、用量、协作、MCP/插件/技能管理等)**不在 schema、不在 Go SDK**,只在手写的 `modes/rpc/rpc-types.ts`(`RpcCommand`/`RpcResponse` 联合 + 帧接口)。
  - GUI 自己用 `src/shared/rpc-types.ts` 手工镜像,再用 `scripts/gen-types.ts --check` 做"命令名覆盖 + `Rpc*` 接口字段名集合"的漂移检查,不检查类型。
  - `scripts/check-protocol.ts` 用 `tsc -p scripts/protocol/tsconfig.protocol.json` 对 GUI 和 agent 源做类型检查,还核对 stats 路由。
- **对 Go 的含义**,三条路:
  1. 先在 fork 的 `wire/commands.ts`、`frames.ts` 里补全这些命令的 DSL,再 `bun run gen:rpc` 得到 `wire.go`。
     - 工作量大,但长期最干净。
     - 补写时可直接以 `rpc-types.ts` 为蓝本。
  2. 用上游 Go SDK 的 `Client.Call(ctx, command, params, timeout)`(返回 `json.RawMessage`,`client.go:281`)手写扩展命令的结构体。
     - 未知帧会解码成 `UnknownNotification`,不会崩。
  3. 从 `rpc-types.ts` 用 TS→Go 的第三方工具生成。
     - 联合类型和 `AgentMessage`/`Model` 等跨包类型会很吃力,`[推测]` 不推荐。

---

## 6. sidecar 构建、版本校验、冒烟测试

- **构建入口**:`bun scripts/build-bundled-omp.ts [--target bun-<os>-<arch>[-variant]] [--out path]`,位于 GUI 仓库(`package.json` 脚本 `build:omp`、`build:omp:x64`、`build:omp:win`)。
- **没有 Linux 的 package 脚本**。`package.json` 里 `build:omp` 用宿主平台。Linux 由 CI `release-linux.yml` 构建,**该 workflow 文件未读**。
  - GUI AGENTS.md 说:"Linux is built by CI … reproduces the sidecar from `sidecar-source.json`'s monorepo pin, smoke-tests it"。
- **构建流程**:
  1. 要求 monorepo 邻居存在,并校验 Bun 版本(`packageManager` 字段)。
  2. 跑 `check:protocol`。
  3. `stageNativeAddon(target)`:若 `packages/natives/native/` 里的 `.node` 版本戳与包版本不符,就用 `bun add --no-cache --registry=https://registry.npmjs.org --os=… --cpu=… @oh-my-pi/pi-natives-<platformTag>@<ver>` 下载 leaf 包。
     - x64 要找 `pi_natives.<tag>-modern.node` / `-baseline.node`;arm64 只有 `pi_natives.<tag>.node`。
     - 构建后恢复原状。
  4. 跑 `gen:stats` 和 `collab-web gen:export-viewer`。
  5. 调 `compileCodingAgent`(上游 `packages/coding-agent/scripts/compile-binary.ts`),`entrypoint = packages/coding-agent/src/cli.ts`。
- **`compileCodingAgent` 里的 `Bun.build` 参数**(读自本机 18.15 的该文件,18.19 可能略有差异):
  - `format:"esm"`、`bytecode:true`、`splitting:true`、`minify:{identifiers:false, keepNames:true}`。
  - `define`:`process.env.PI_COMPILED="true"`、`PI_DOCS_EMBED` 等。
  - `external`:`fastembed`、`onnxruntime-node`。
  - `files`:`embeddedAddonFiles(native)`,把 `.node` 作为 asset 嵌入。
  - `compile`:`{target?, outfile, execArgv:["--user-agent=…"], autoloadBunfig/Dotenv/Tsconfig/PackageJson:false}`。
  - 另有两个插件:JSON 解析和 legacy-pi 虚拟模块。
- **版本差异警告**:GUI 的 `build-bundled-omp.ts` 调用 `compileCodingAgent` 时**没传** `transformersVersion`,而本机 18.15 的该函数签名要求它。说明 GUI 脚本对接的是 18.19 的新签名。
- **目标与大小**:
  - 上游 `build-binary.ts` 的 linux 目标:`linux-arm64 → bun-linux-arm64`,`linux-x64 → bun-linux-x64-baseline`。
  - GUI 脚本接受 `bun-(darwin|linux|win32|windows)-(arm64|x64)(-…)?`,输出 `resources/omp`(arm64 或原生)、`omp.x64`、`omp.exe`。
  - 大小:GUI AGENTS.md 写"~120 MB each"(`resources/omp*`)。这是 macOS 的数字,**Linux 的实际大小未找到**。
  - 上游注释说 bytecode 让二进制 +52 MB。
- **版本校验**:
  - natives 用 `version-sentinel.js` 校验 `.node` 与包版本一致(`containsVersionStamp`、`containsLegacyVersionSentinel`)。
  - 构建后 `$GUI/resources/omp --smoke-test`(`sync-upstream.sh` 步骤 6)。AGENTS.md 另说 `--version` 应显示 `omp/X.Y.Z`。
  - 上游 `omp --smoke-test`(`cli.ts` `runSmokeTest`)依次探测:sync worker、stats activity worker、stats server、tiny title worker、STT、JS eval worker、computer worker、daemon broker、LSP mux、IDA host、blob broker、terminal-output worker、text predict daemon 等。
- **GUI 冒烟脚本**:`scripts/smoke-sidecar.mjs <binary>`。以 `--mode rpc-ui` 启动,断言:READY、`negotiate_protocol`、`get_state`、`get_available_models`(≤6000ms)、`get_state`、`set_thinking_level`、`get_settings`、`set_setting` 往返。输出 VERDICT PASS/FAIL,硬超时 40s。
- 发布流程里的 Windows 构建用 `bun-windows-x64-baseline`,在 CI 里跑。

---

## 7. 维护风险与分支策略

### 7.1 事实

- **冲突热点**(按改动量):
  - `modes/rpc/rpc-mode.ts`(+2869/−344,单文件 switch,上游也在改)
  - `rpc-types.ts`(+3390/−169)
  - `session/agent-session.ts`(+818/−192)
  - `main.ts`(+88/−40,**含大量格式化噪声**:大量只是换行风格的 diff,说明 fork 用 oxfmt/biome 格式化过。GUI AGENTS.md 明说"a Biome `--write` on merge-resolved files once added ~4k lines of formatting churn")
  - `session-manager.ts`、`task/executor.ts`、`memories/{index,storage}.ts`
  - 生成物:`wire/rpc-wire.generated.ts`、`rpc-wire.schema.json`、`sdk/go/omp-rpc/wire.go`(要重新生成而不是手工合)
  - `CHANGELOG.md`、`bun.lock`
- **fork 作者的约束**:
  - GUI AGENTS.md:
    - 不得往 `can1357` push。
    - monorepo 工作在根目录提交,GUI 工作在 `packages/gui/` 提交。
    - 同步必须用脚本,冲突解决后用 `SKIP_MERGE=1` 续跑。
    - monorepo 文件用 monorepo 自己的 `oxfmt`/`oxlint`,不用 GUI 的 Biome。
    - 提交信息不带 AI 署名。
  - `plan/16`(2026-08-05):
    - "新命令按三种范式选一种,不要发明第四种":read builder / apply action / stateful controller。
    - 新增 RPC 命令必须同步更新 `RpcCommand` 联合、`RpcResponse` 联合、`rpc-mode.ts` switch、GUI 镜像、preload。
    - **"文件落点…四个新文件 + `rpc-actions.ts` 追加;`rpc-mode.ts` 只加 case 与 import。upstream 合并时冲突只落在 switch 与 import 两处"**,即"改动尽量放新文件"的原话出处。
    - 现实偏离:`rpc-mode.ts` 已 +2869 行,这个约束没有守住。
  - `plan/16` 是旧状态("A1 已交付,A2/C1 未实施"),现在已落后于源码。
- **发布时的陷阱**(GUI release skill):Intel/Windows 的 natives 取包有 TLS 拦截问题;`latest-mac.yml` 要覆盖两种架构。与 Oryn 无直接关系。
- **上游自身速度**:落后 359 个提交只是约 6.5 小时的上游进度(`4ad629ea` 04:35 → `52ff19a2` 11:04),PR merge 会放大提交数;上游极活跃,星数 38783。
- **仓库洁净度**:fork 里混有 `index.js`(39263 行)、`design-qa.md`、`.devin/skills/` 等非 agent 文件,再 fork 时应清理。

### 7.2 分支策略选项

假设远端为 `upstream`(can1357)、`nornzach`(nornzach/oh-my-pi)、`origin`(Oryn 自己的 fork)。

| 选项 | 做法 | 好处 | 代价 |
|---|---|---|---|
| **A. 以 nornzach 为基线,单分支 merge 上游** | `origin/main` = nornzach/main + Oryn 补丁;定期 `merge upstream/main`,同时 `merge nornzach/main`;与 nornzach 现有做法一致 | 最省事;能继续吃 nornzach 的新 RPC 命令 | 三方合并;`rpc-mode.ts` 双方都改时冲突翻倍;合并提交图复杂 |
| **B. 一次性切断 nornzach,把其 RPC 层当"本仓代码"接管** | 以当前 HEAD `4b67dcb` 为起点,之后只跟 `upstream`,不再 merge `nornzach`,需要的 nornzach 变更手工 cherry-pick | 只剩一条上游同步线,冲突来源单一 | 失去 nornzach 的后续修复;GUI 专属行为(`omp/gui/` 前缀、`get_omp_update`)要自己清理 |
| **C. 三层分支:`upstream-mirror` / `rpc-layer` / `oryn`** | `upstream-mirror` 只 fast-forward 上游;`rpc-layer` 是 nornzach 补丁 rebase 到 mirror 上(用 `git rebase --onto` 或 `git cherry`);`oryn` = mirror + rpc-layer + Oryn 补丁(merge 或 rebase) | 补丁边界清晰;可向 nornzach 回馈 | 需要持续 rebase 约 166 个提交,冲突要反复解;rebase 重写历史,对下游不友好 |
| **D. 补丁队列(patch series)** | 用 `git format-patch` / quilt / `git-branchless` 把 nornzach 的差异存成补丁序列,上游每次更新就重放 | 与上游版本解耦,升级可自动化 | 补丁失败即阻塞;166 提交且大文件,实际维护重 |
| **E. 重构以减小面** | 把 `rpc-mode.ts` 里的 case 抽成"命令注册表",各 `rpc-*.ts` 自注册,`rpc-mode.ts` 只留一行 hook;再提给上游或自己维持 | 长期冲突面最小 | 前期要大改 `rpc-mode.ts`,而这恰是最大冲突文件;短期收益为负 |

- **推荐顺序 `[推测]`**:先用 **A** 起步(与现有流程一致、成本最低),同时做 **E** 的一小步,即把 Oryn 自己新增的命令放进独立文件,`rpc-mode.ts` 只加一行 import 和 case。如果日后 nornzach 停更或方向与 Oryn 偏离,转 **B**。
- 无论选哪个:
  - 生成物(`rpc-wire.*`、`wire.go`)冲突时**不要手工解**,解完源文件后重新 `bun run gen:rpc`。
  - 格式化只用 monorepo 工具链。
  - 合并后跑 `--smoke-test` 与 `smoke-sidecar.mjs`。
  - Oryn 要用的扩展命令最好补进 `wire/commands.ts` 的 DSL(见 §5),这样 Go 类型能随 `gen:rpc` 自动更新。

---

## 对 Oryn 的启示

- 容器内 agent 直接用 `omp --mode rpc-ui` + JSONL,先 `ready` → `negotiate_protocol 2`,可复用 smoke-sidecar 的握手序列。
- Go 后端用上游 `sdk/go/omp-rpc` 的 `Client.Call` 驱动扩展命令,不要期待 `wire.go` 里有 `terminal_*`、`worktree_*`、`get_queue`、`plan_approval` 等。
- 想要 Go 类型随版本自动更新,就把用到的扩展命令补进 fork 的 `wire/commands.ts` DSL,再 `bun run gen:rpc`;这是最干净的长期方案。
- 终端:前端 xterm 用 `terminal_snapshot` 的 `seq` 去重(丢弃 `seq <= snapshot.seq` 的帧),后端 Go 可原样转发帧。
- 远程 orb 里需 `terminal_configure {interactiveCommands:true, agentTools:true}` 才有 `pty:true` 终端和 agent `terminal` 工具;终端属于进程而非会话,断线重连要靠 `terminal_list` + `snapshot`。
- `browser_host_start` 需要 host 侧有 webview 拨入 relay,Tauri 移动端基本不适用;orb 里应让 agent 用自带无头浏览器,不调这个命令。
- `get_omp_update`、`worktree_*` 的 `omp/gui/` 前缀等有 GUI 痕迹,Oryn 应改成自己的命名或忽略这些命令。
- 公开分享/只读链接:fork 的 `collab_start {view:true}` 与 `webViewLink` 是现成的只读分享机制,值得评估能否直接作为"只读线程链接"的后端;而 `preview_share_session`/`share_session` 是把快照上传到 blob/gist。
- 队列、会话树、`get_transcript(_page)` 都是纯读/轻写命令,适合直接映射成 Go API 与手机端的"看对话/改排队"功能。
- 设置:`get_settings_schema` 提供 `tuiOnly`/`restartRequired` 元数据,可直接渲染 Oryn 的设置页并标注哪些需要重启 orb 里的 agent。
- 同步策略先走"nornzach 基线 + 周期 merge 上游",Oryn 自己的新命令写在新文件里,`rpc-mode.ts` 只加 case。
- 构建 linux-x64/arm64 sidecar 需要对应的 pi-natives leaf 包(版本戳必须匹配),以及对 18.19 `compileCodingAgent` 新签名的适配;要在容器里放 `omp --smoke-test` 做门禁。
- 再 fork 时清理 `index.js`(39K 行)、`.devin/`、`design-qa.md`,并把 CHANGELOG 分叉的 4512 行处理掉,否则每次合并都带噪声。
- `negotiate_protocol` 目前只接受 v2,Go 客户端要实现 `rpc_chunk` 重组(上游 SDK 已实现)。

---

## 实际读过的文件/页面清单

**GitHub API / 页面**
- https://api.github.com/repos/nornzach/oh-my-pi
- https://api.github.com/repos/nornzach/oh-my-pi/branches
- https://api.github.com/repos/nornzach/oh-my-pi/commits?per_page=60&sha=main(仅前 300 行 + 全文 grep 提交信息)
- https://api.github.com/repos/nornzach/oh-my-pi/compare/can1357:main...nornzach:main(文件列表至 300 条上限;读了 `main.ts`、`agent.ts`、`AGENTS.md`、`README.md`、`docs/rpc.md`、`wire/*`、`rpc-input.ts`、`chat-mode.md`、`terminal.md`、`browser.md`、`system-prompt.ts` 等 patch)
- https://api.github.com/repos/nornzach/oh-my-pi/compare/main...gui-v0.3.1-runtime(前 300 行)
- https://api.github.com/repos/nornzach/oh-my-pi-gui 、`/contents/scripts`

**fork 源码(raw, @4b67dcb)**
- `packages/coding-agent/src/modes/rpc/rpc-types.ts`(L1–764、L800–860、L1035–1075、L1236–1260、L1280–1400、L1400–1530、L2030–2060、L2150–2340、L2380–3900 及多处 grep)
- `rpc-mode.ts`(L615–725,及对 `terminal_*`、`set_plan_mode`、`get_transcript*`、`get_usage`、`BACKGROUND_COMMANDS`、`ready` 等 grep)
- `rpc-terminal.ts`(全文)、`rpc-terminal-tool.ts`(L1–140)、`rpc-browser-host.ts`(全文)、`rpc-worktree.ts`(L1–110)、`rpc-plan.ts`(L1–120)、`rpc-collab.ts`(L1–80)
- `prompts/tools/terminal.md`、`packages/coding-agent/package.json`(grep)、`sdk/go/omp-rpc/wire.go`(grep)、`wire/rpc-wire.schema.json`(grep)

**GUI 仓库(raw, main)**
- `sidecar-source.json`、`AGENTS.md`、`package.json`
- `plan/16-agent-rpc-extensions.md`、`plan/01-architecture.md`
- `scripts/sync-upstream.sh`、`scripts/build-bundled-omp.ts`(全文)、`scripts/check-protocol.ts`、`scripts/smoke-sidecar.mjs`、`scripts/gen-types.ts`(L1–80)

**本机上游(18.15.0)**
- `packages/coding-agent/src/modes/rpc/rpc-types.ts`(命令联合 grep)、`wire/commands.ts`(L1–300)、`wire/rpc-wire.schema.json`(grep)
- `packages/coding-agent/scripts/compile-binary.ts`、`scripts/build-binary.ts`(grep)
- `src/main.ts`、`cli/args.ts`、`cli.ts`、`session/agent-session*.ts`(grep)
- `sdk/go/omp-rpc/README.md`、`client.go`、`wire.go`(grep)、`.github/workflows/ci.yml`、`nix.yml`(grep)

**未读到 / 未找到**
- GUI 仓库的 `src/shared/rpc-types.ts`(未读)、`plan/18–21`、`.github/workflows/release-linux.yml`、`release-windows.yml`。
- fork 各 `rpc-*.ts` 的完整实现(`rpc-queue.ts`、`rpc-session-tree.ts`、`rpc-agents.ts`、`rpc-mcp-extra.ts`、`rpc-marketplace.ts`、`rpc-plugins.ts`、`rpc-memory.ts` 等只靠 compare 的行数与类型定义判断)。
- compare 文件列表被 API 截断在 300 条,`sdk/go`、`sdk/python`、`sdk/rust`、`crates/` 的 fork 侧改动、`packages/stats` 之后的字母序文件看不到;靠后文件的行数统计显示为 0,不可信。
- 同步的完整历史与频率(只看了最近一页提交)。
- Linux sidecar 的实际体积、`release-linux.yml` 的具体 `--target` 参数。
- `RpcCommand` 联合的精确命令总数(未逐条计数)。
- `abort_subagent`/`revive_subagent` 的 `reason` 取值以源码为准(`rpc-agents.ts` 未读,文中取值来自 `plan/16`)。

### Critical Files for Implementation
- `packages/coding-agent/src/modes/rpc/rpc-types.ts` — 扩展命令/响应/帧的唯一权威类型(nornzach/oh-my-pi@4b67dcb)
- `packages/coding-agent/src/modes/rpc/rpc-mode.ts` — switch 分发、`BACKGROUND_COMMANDS`、`terminal_configure`、plan 控制器接线,也是最大冲突点
- `packages/coding-agent/src/modes/rpc/wire/commands.ts` — 想让 `gen:rpc` 覆盖扩展命令时要补的 DSL 入口
- `packages/coding-agent/src/modes/rpc/rpc-terminal.ts` — `RpcTerminalHost`(`seq`/snapshot/scrollback 语义)
- `sdk/go/omp-rpc/client.go` — Go 侧 `Client.Call` 通用入口,手写扩展命令类型时的落点
