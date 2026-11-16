# Oryn「全局助手」现状盘点（只读调研，仓库 `/root/Projects/Ling/Oryn`）

> 约定：路径均相对仓库根；`文件:行` 为本次读到的实际位置。标 **[推断]** 的是读代码后的推论，未找到直接证据。
> 未读到的东西（例如 Android/iOS 客户端源码目录）不下结论。

---

## 0. 一句话结论

全局助手 = `threads` 表里一个 `kind=assistant` 的线程（`internal/threads/coordinator.go:19-23`）。它跑在**控制面宿主上的沙箱子进程**里。这个进程里的 omp 只有 `read`/`write` 两个内置工具，外加控制面应答的 25 个"宿主工具"（9 个协调类、3 个文件库类、13 个 artifact 类）。它没有 shell、没有网络、没有浏览器，也没有长期记忆；记忆接入在 M16 里被明确排除，近况/画像/Honcho/EverOS 只停在调研日志里，没落代码。

它做不成"核心 agent"的原因是**结构性的**：它被设计成"只会调度别的线程的无工作区协调者"，沙箱、工具、记忆、上下文四层都关死了。见第 7 节。

---

## 1. 运行架构

### 1.1 进程在哪里跑、怎么拉起

链路（自上而下）：

1. **库里的数据模型**：协调对话是普通线程行，`kind = space | assistant`（`internal/threads/coordinator.go:19-23`；`docs/protocol.md:777`）。所有协调对话**共用一台** `coordinator=1` 的 orb，挂在内置 provider `@host`（`driver=host`）下。`EnsureOrb` 单飞创建它（`internal/coord/coord.go` 的 `EnsureOrb`，约 :200-225）。
2. **orynd 进程内的 `coordhost.Host`**（`internal/coordhost/host.go:1-11, 560-700`）：注册 `@host` provider、每 15 秒续租、单 goroutine 认领任务；**同一时刻至多一个 `orynd coordinator-host` 子进程**（`docs/protocol.md:949`；`host.go:1-11`）。子进程经继承的 socketpair（fd 3 = link，fd 4 = 状态管道）与 orynd 通信，无 WebSocket、无令牌（`internal/coordhost/child.go:25-30`）。
3. **子进程 `orynd coordinator-host`**（`child.go:62-205`）。启动顺序固定：探测 → 回环 → 监听模型代理端口 → 取 link → 降权 → 施加沙箱 → 报告 → 自检或运行受限 orbd（`child.go:62-64`）。里面跑的是裁剪过的 orbd（`orbdrt`，`Coordinator:true`；`child.go:208-255`）。
4. **orbd 再拉起 `omp`**（Oryn Core），用 **`--mode rpc-ui`**（`internal/orbdrt/coordinator.go:92-94`），不是 print/interactive。

### 1.2 沙箱（Landlock + seccomp + 命名空间 + 降权）

| 项 | 内容 | 出处 |
|---|---|---|
| 文件 | Landlock 白名单：只有 `--home`（`<dir>/homes/<orbID>`）可写；可执行仅 omp；只读：`/usr/lib*`、`/lib*`、`/proc`、`/etc/{ld.so.cache,passwd,hosts,…}`、`/dev/urandom` 等 | `child.go:30-41, 158-170` |
| 网络 | 独立网络命名空间；没有 netns 时靠 Landlock TCP 端口规则（ABI ≥ 4）；**唯一放行的是回环上的模型代理端口**（默认 7811，由 orynd 在施沙箱前建好监听再交给 orbd，所以沙箱内不需要 bind 权限） | `child.go:113-119, 158-170`；`internal/config/config.go:67-80` |
| syscall | seccomp 黑名单；AF_UNIX 只留已连接的 stream socketpair；拒绝 truncate、UDP、TFO；能力集清空；`no_new_privs` | `internal/sandbox/sandbox_linux.go:3-4`；`internal/sandbox/check_linux.go:76,151-162,196` |
| 用户 | root 部署时降权到专用账号 `oryn-coord-<fnv32a>`（可配置 `coordinator.user`） | `internal/config/config.go:71-75` |
| 命名空间 | PID ns / net ns（用不了时降级到更高 Landlock ABI 门槛，不满足则 fail-closed 即"协调功能不可用"） | `docs/protocol.md:867`；`internal/coordhost/coordhost_test.go:568-670` |
| 自检 | `orynd coordinator-host --selftest` / `orynd coordinator check` 共用 `sandbox.Checks`，含"宿主上真实监听的白名单外 TCP 端口必须连不上" | `child.go:171-198`；`docs/protocol.md:867` |
| 状态可见性 | `GET /api/v1/coordinator` 返回 `CoordinatorStatus{available, reason, ompVersion, sandbox{landlockAbi, network, pidNamespace, ipcScope, uid, seccomp}}` | `docs/protocol.md:788, 804` |

### 1.3 omp 的启动参数与环境（协调档）

`internal/orbdrt/coordinator.go:61-110`：

```
omp --mode rpc-ui --cwd <home>/.oryn/coordinator/cwd --session-dir ~/.oryn/threads/<threadId>
    --no-tools --tools read,write
    --no-skills --no-extensions --no-rules --no-context-files --no-lsp
    --config <overlay.yml> --system-prompt <prompts/<threadId>.md>
```

- **系统提示词写成文件**，argv 只带路径，避免同 uid 进程用 `/proc/<pid>/cmdline` 读到（:85-88）。
- **环境变量从零构造**：`HOME/PATH=/usr/bin:/bin/LANG/TMPDIR/PI_RPC_EMIT_TITLE/PI_NOTIFICATIONS=off/OTEL_SDK_DISABLED/PI_FILE_LOCK_BACKEND=sidecar/ORYN_THREAD_ID`（:96-105）；`agent.start` 带 `env` 一律拒绝（:70-72）。
- **overlay（`--config`）强制关闭**（:22-35）：`autolearn`、`memory.backend: "off"`、`checkpoint`、`compaction.experimentalContextManagement`、`goal`、`plan`、`generate_image`、`lsp`、`mcp.enableProjectConfig`、`marketplace.autoUpdate`、`startup.checkUpdate`、`bash.direnv`。
- ctl 方法白名单（:37-42）：`setup.run / resume.run / agent.start / agent.stop / shutdown / omphome.apply / library.sync / library.fetch`，其余 `method_not_found`。hello 特性表恰为 `["coordinator","omphome-apply","library"]`（`docs/protocol.md:836`）。
- 协调宿主的 `setup.run` 只写 omp 文本配置：带 repo、脚本、git hosts、git 身份、env 任何一项都拒绝（`coordinator.go:50-59`）。

### 1.4 拉起 / 休眠 / 唤醒

- **休眠 = 停子进程**。`coordhost` 的 pause（freeze/stop）都结束子进程；会话文件留在 `<dir>/homes/<orbID>`，唤醒后接续（`host.go:1-6`）。
- **空闲策略复用 orb 状态机**：`EvaluateIdle` 对 ready → frozen（agent 与用户都空闲超时）→ stopped（冻结太久）（`internal/orbs/service.go:1439-1485`，`runner` 跳过，协调 orb 没有豁免 **[推断]**）。
- orynd 重启时：协调 orb 若是 ready 则 `Pause(stop)`（子进程已随上一个 orynd 消失），下一条消息经 resume 回来；connecting/setting_up/resuming 则直接重拉（`host.go` `recover`，约 :655-680）。
- 崩溃：`crashWindow=5min`、`crashLimit=3`（`host.go:56-57`）；`kill -9` 后经 stopping/stopped/waking 回到 ready（`internal/server/e2e_m9_test.go:479` 的注释）。
- **每次 agent.start 之前**（`internal/threads/agent.go:~290-312` 的 `prepareCoordinator`）：
  1. 以 `AgentProfile.Setup` 对协调宿主重跑一次 `setup.run`，把受管文件（`models.yml`、`config.yml`）还原，覆盖助手用 `write` 改过的内容；失败则线程标 failed，不带着可能被改过的配置启动；
  2. `library.SyncHost` 把文件库同步进宿主沙箱 `~/library/`（失败只记日志）。
- `RefreshAgent`：Profile 变化后等静止停 agent，下条命令用新系统提示词重启（`threads/coordinator.go:~180-215`）。

### 1.5 会话持久化

- **会话文件**：每个线程一个 `~/.oryn/threads/<threadId>`（`sessionDirOf`，`internal/threads/agent.go:~330`），在沙箱 HOME 里，omp 以 `open_session` 接续。
- **转录**：与工作线程相同，控制面把 omp 条目复制进库，WS 与前端共用（`docs/protocol.md:777` "转录、WS、条目复制、标题、未读全部复用"）。
- **首条消息**与线程行同一事务落库（`startup_frames`），保证"建对话+首问"原子（`threads/coordinator.go:~128-155`）。
- **orb 被删**后线程自动重新挂到新协调 orb：转录保留，模型上下文从新会话开始（`ensureCoordOrb`，`threads/coordinator.go:348-376`）。**这意味着助手的"记忆"只剩转录**，没有跨对话的任何东西。

### 1.6 与 Space 协调宿主 `@host` 的关系

**同一个宿主、同一台 orb、同一个子进程**；区别只在 `kind`、系统提示词与工具表：

| | Space 协调者 | 全局助手 |
|---|---|---|
| 提示词范围段 | `You coordinate the Space "<name>". Goal… Default project…` | `You are the owner's global assistant. You can see and direct every thread.`（`internal/coord/profile.go:37`） |
| 工具 | 9 个协调工具（`ToolDefs(inSpace=true)` 在 :208-250 后直接 return） | 协调 9 + 文件 3 + artifact 13（`profile.go:169-265`） |
| 内置 read/write | 禁用（`spaceFiles`：`You have no files`，:59） | 开（`~/library/…`） |
| 作用域 | `list_threads/list_schedules/get_stats` 默认只看本 Space；`create_thread` 默认挂 Space 与其项目 | 全量 |
| 授权 | `Kind==space` 必须能加载 Space，否则 **fail-closed** 拒绝一切工具（`internal/coord/tools.go:36-52`） | — |
| 首条消息 | `CreateCoordinator` 只允许助手带 prompt/images/files（`threads/coordinator.go:~113`） | 允许 |

所有协调对话共用一个沙箱，"所属对话"只是可见性边界，不是安全边界（`docs/plan-m13.md:76`）。

---

## 2. 完整工具清单

宿主工具由控制面应答：omp 发 `host_tool_call` → 控制面 `Coordinator.HostToolCall` → 回 `host_tool_result`（`internal/threads/coordinator.go:401-440`）。**单次调用超时 60s**（`hostToolTimeout`，`coordinator.go:29`）；omp 取消（`host_tool_cancel`）或 agent 退出则结果作废。分发在 `internal/coord/tools.go:34-107`，工具定义在 `internal/coord/profile.go:169-390`。

### 2.1 内置（omp）

| 工具 | 说明 |
|---|---|
| `read` | 沙箱内读文件。可读：`~/library/threads/<threadId>/…`（本对话）、`~/library/shared/…`；其余是 omp 自己的 HOME。提示词写明只读库文件（`profile.go:40-45`） |
| `write` | 沙箱内写。提示词规定**只准写 `~/library/out/`**（`profile.go:40-45`）。注意这是**提示词约束**，技术上整个 HOME 可写，下次 `agent.start` 前 `setup.run` 还原受管文件（`threads/agent.go` `prepareCoordinator`） |

### 2.2 协调类（Space 与助手共有；`profile.go:208-250`；实现 `tools.go`）

| 工具 | 参数要点 | 行为/限制 | 实现行 |
|---|---|---|---|
| `list_threads` | `scope`(space\|all)、`state`、`project`(名称)、`archived`、`query`、`limit`(默认30，最大100) | 只列 `kind=work`；结果走不可信包裹 `wrapUntrusted` | `tools.go:201` |
| `read_thread` | `threadId`、`tail`(默认6，最大30) | 读最近 user/assistant 消息；**不可信包裹**；目标必须是工作线程且非 runner | `tools.go:269` |
| `create_thread` | `prompt`(必填)、`project`、`title`、`model`(provider/id)、`thinkingLevel`、`orbType`、`files`(**仅助手**，文件库 id) | 首条提示词前加来源头行 `[Oryn message from assistant "" (<id>) — sent by an agent, not typed by the owner]`；**限速 10/10min、40/24h/对话**；写**回报登记**；`ParentThreadID=助手线程` | `tools.go:307` |
| `send_message` | `threadId`、`message`、`steer` | 登记回报后 `Deliver`（自动化身份）；`steer` 仅目标正在工作时生效；**限速 60/10min/对话** | `tools.go:417` |
| `archive_thread` | `threadId`、`outcome`(shipped\|archived) | `shipped` 记 `shippedAt` 并归档；不能删除线程 | `tools.go:474` |
| `set_schedule` | `action`(set\|pause\|resume\|delete)、`mode`(thread\|new_thread)、`threadId`(缺省 = 本对话，即"叫醒自己")、`project`、`name`、`kind`(cron\|every\|once)、`cron/every/at/timezone/endsAt`、`prompt`(占位符 `{{now}}{{date}}{{scheduled_at}}{{run_number}}{{last_run_at}}`)、`enabled`、`model/thinkingLevel/orbType/overlap/keepLast` | 限速 10/10min、40/24h；agent 定义的 `new_thread` 至少间隔 15 分钟、不重叠、`keepLast` 不可调低；`createdBy="agent"` | `tools.go:533, 632` |
| `list_schedules` | `scope` | Space 默认只看本 Space | `tools.go:710` |
| `list_projects` | — | `ID/Name/RepoURL/DefaultProvider/DefaultOrbType`（只列未归档项目） | `tools.go:740` |
| `get_stats` | `kind`(usage\|tools\|orbs\|output)、`groupBy`、`series`、`from/to/tz`、`project/space/thread/model`、`limit`(默认10，最大30) | 与 REST 同 JSON；Space 强制本 Space 且禁 `orbs` | `tools.go:776` |

线程回报：目标线程这次投递的回合到终态后，控制面自动向助手投递 `[Oryn thread report · automatic] <T-id> is now <state>.` + 不可信包裹 JSON(`title/outcome/reply(≤4000字)/error/question`)（`internal/coord/reports.go:~295-326`；`content.go:19` `maxReportRunes=4000`）；回报限速 60/小时（`limit.go:18`）。提示词明确"不要轮询"（`profile.go:16-35`）。

### 2.3 文件库类（只给助手；`profile.go:251-265`；实现 `internal/coord/library.go`）

| 工具 | 参数 | 行为 | 行 |
|---|---|---|---|
| `save_file` | `path`(必须 `~/library/out/…`)、`name`、`shared` | 经 `library.fetch` 流把沙箱文件取回存库；默认只属本对话 | `library.go:125` |
| `list_files` | `scope`(conversation\|shared) | 列可读库文件与沙箱路径；**不可信包裹**（文件名可来自 orb 内 `oryn library put`） | `library.go:160` |
| `share_file` | `id`(afl_…)、`shared`(bool) | 对话私有 ↔ 全局共享；路径随之变化 | `library.go:188` |

另：REST 管理 `GET/POST/PATCH/DELETE /api/v1/assistant/files…`（≤100MiB；`docs/protocol.md:819-826`）；WS 命令扩展字段 `orynFiles`（≤20）（`docs/protocol.md:828`）。

### 2.4 Artifact 类（只给助手，`ScopeAll`，13 个；`profile.go:268-390`；分发 `tools.go:80-91`；实现 `internal/coord/artifacts.go:25-75` → `internal/artifacts/agent.go`）

`create_artifact`（`title/type(document|slides)/html/files/capabilities(db|files|sample)/note/projectId`）、`read_artifact`（分页）、`update_artifact`（`baseVersion` 乐观并发 + `edits/files/canvas`）、`list_artifacts`、`delete_artifact`（软删 30 天）、`list_artifact_versions`、`restore_artifact_version`、`read_artifact_guide`（topic: files/runtime/…）、`read_artifact_comments`、`reply_artifact_comment`、`resolve_artifact_comment`、`read_artifact_data`、`write_artifact_data`。

- 助手**没有** `screenshot_artifact`（该操作存在于 `internal/artifacts/agent.go:34` 的 `OpScreenshot`，但不在助手工具表里）。
- `source` 只能是 `~/library/out/` 下的文件（`profile.go:47-57`；`coord/artifacts.go:18`）。
- 主人在 artifact 上的评论会**作为消息到达助手**（`artifacts.TurnObserver` 在 `server.go:293` 注册）。
- 结果里的页面文本/数据/评论走 `wrapUntrustedText`（`coord/artifacts.go:100`）。

### 2.5 orb 内 agent 反向调用的 ctl 方法（不是助手的工具，但属于同一协调层，`internal/coord/orbmethods.go:27-36`）

`thread.create / thread.send / thread.get / schedule.get / schedule.put / schedule.delete / schedule.enable / library.put`。限制是"亲属规则"：只能联系父子线程、只能改自己线程的定时（`coord.go:1-8` 文件头）。

---

## 3. 上下文与记忆

### 3.1 现在**实际注入**了什么

助手每次启动，上下文只来自：

1. **系统提示词**：`coord.SystemPrompt(space=nil, "", threadID)`（`internal/coord/profile.go:62-81`）= 通用模板（`:16-35`）+ `assistantScope`（:37）+ 文件段（:40-45）+ artifact 段（:47-57）。**纯静态文本，除 threadID 外没有任何运行时数据**（没有近况、主人偏好、项目清单、日期时区）。
2. **omp-home 的两个文件**：`models.yml`（provider/模型路由，地址改写到协调宿主代理端口）和 `config.yml`（`internal/orbs/setup.go:77-79` 的 `coordinatorFiles`）。其余（mcp.json、skills、agents、commands、AGENTS.md、扩展）一律不下发（`docs/plan-m13.md:73-76, R31`）。
3. **omp 的 `--no-context-files --no-rules --no-skills --no-extensions`**：AGENTS.md / 规则 / 技能 / 扩展全关（`orbdrt/coordinator.go:93`）。**所以"AGENTS 配置如何注入"的答案是：不注入。**
4. **对话转录**（omp 会话文件，沙箱 HOME 内）。
5. **文件库**：用 `read` 才看得到；附件在消息末尾追加 `<oryn-attachments>` 块列沙箱路径（`docs/protocol.md:828`）。

### 3.2 记忆：现状 = **无**

- **overlay 强制 `memory: { backend: "off" }` 与 `autolearn: { enabled: false }`**（`orbdrt/coordinator.go:22-24`）。
- **M16 内置 Hindsight 明确排除助手**：`threads/memory.go:12-14`（"协调对话不接入记忆"）、`threads/agent.go:~190`（`if prof == nil { mem = s.agentMemory(...) }`）、`FeatureMemory` 注释"协调宿主不上报"（`threads/memory.go:15-17`）、`docs/plan-m16.md:37`（Q5 "全局助手不接入"）、`docs/plan-m16.md:178`（非目标："全局助手（协调档）接入记忆：它的 overlay 强制 `memory.backend: off`，另有 EverOS / 近况路线"）、`.agent-logs/0149`（"全局助手仍不接入"）、`.agent-logs/0148`（"既有助手记忆路线（0064-0138：Honcho/EverOS/近况）未落代码"）。
- **M16 管理页文案**："作用于 orb 与 runner，不作用于全局助手"（`docs/plan-m16.md:485`）。
- **近况（recent activity）**：只有调研与原型，**无代码**。grep `近况|recent.?activity` 在 `internal/`、`web/src` 中的唯一命中是 M16 的文字 / 日志。设计见 `.agent-logs/0133`、`0134`、`0137`、`0138`：
  - 0133：近况单独成层，不塞进 EverOS 画像（画像要"当前值、不写会过期的细节"，会反复删近况）；近况按"事情"组织，每条 `title/dirs/goal/status/decisions(≤5)/open/last_active`；每天增量更新（旧近况 + 当天新小结 → 新近况）；超过约 3 周没动的移出，长期归属由画像接住。
  - 0134：v1 把"要求"写成"已完成"，v2 拆成 `requests / decisions / done` 后全量核对。
  - 0137：单用主人原话看不懂"好，这样改吧"；加助手语境做"去语境改写"，输出 `{text, scope: personal|project|once, kind: request|decision|question|confirm|info}`；风险：confirm 类会把被注入污染的助手方案写成"主人同意了"，要保留来源 id。
  - 0138（更正）：**近况的一行概览（title/goal/focus/last_active）在任何地方都注入，包括全局助手和其他项目**；scope 只限制"事实适用范围"，不决定能否被看见。
- **Honcho**：`.agent-logs/0067-0089` 调研与中文实测（Honcho 3.6.1，AGPL，原子结论易断章取义，0079 复盘转向"分层综合"）；`0112-0129` 换 EverOS 实测（画像精度好但太稀，值得借鉴 episode 层与画像排除规则/`forget`/`do_not_record`，0129）。**都停在 `/tmp` 评测，没有一行进入 `internal/`。**（`grep Honcho` 只在 `.agent-logs` 命中。）
- **安全结论已定、可复用**：
  - 0064：不宜开 omp 内置 mnemopi（沙箱禁 AF_UNIX、无外网下 onnx、数据锁在沙箱 HOME、auto-retain 会把不可信线程回报写进记忆）→ 推荐记忆做成**控制面能力**（`remember/recall/forget` 宿主工具 + `agent.start` 注入核心记忆快照）。
  - 0065（更正 0064）：**`OwnerTurn` 不能作授权边界**（`OwnerTurn = !t.autoActive`，主人发起的回合同样会读进不可信内容再被诱导调 `remember`）。模型发起的写入一律先进低信任候选；pinned/preference 需主人确认或 `ownerQuote` 在主人亲手输入的消息中逐字核验。`OwnerTurn` 继续只做审计字段（`internal/threads/coordinator.go:50-51`）。
- **Hindsight 对工作线程已落地的机制（对助手有直接借鉴价值）**：`agent.start` 带 `memory` 参数 → orbd 写 `--config` 覆盖（`memory.backend: hindsight`、`hindsight.globalScope: true`）+ 注入 `HINDSIGHT_API_URL=http://127.0.0.1:7811/memory/t/<threadId>`、`HINDSIGHT_BANK_ID=oryn`、`HINDSIGHT_SCOPING=per-project-tagged`（`internal/apigen/apigen.gen.go:5535-5538`）；orynd 网关按来源强制 bank/标签，key 不进 orb（`docs/plan-m16.md` Q3）；全局记忆打 `oryn:global` 标签，agent 写全局先进**待批准候选队列**（orynd 库内，批准才进 Hindsight；`migrations/0036_memory.sql:44`）；全局 `user-preferences` mental model 由 orynd 维护、只从已批准记忆提炼（U3）。

### 3.3 其他上下文来源

- **身份**：助手名字/头像独立于 Agent 身份（`GetOwnerAssistantIdentity`，`internal/api/avatar_identity.go:53-63`；`plan-m13.md:22`）。
- **模型**：见 5.3。
- **审计**：每次协调动作写 `coord_actions`（`internal/coord/coord.go` `audit`，约 :235-262），保留 90 天（`actionRetention`，:~170），`GET /api/v1/automation/actions`（`docs/protocol.md:789, 805`）。助手**读不到**这张表（没有工具）。

---

## 4. 入口与多端

### 4.1 入口

| 入口 | 行为 | 出处 |
|---|---|---|
| 首屏（HomePage） | role 胶囊里可选"全局助手"；选中后隐藏执行位置/项目/Profile；附件进助手文件库；发送 = `POST assistant/conversations`（带 prompt/files/images/model/thinkingLevel）并跳 `/assistant/:id`；**不启用 `#` issue/PR 引用** | `web/src/pages/HomePage.tsx:1-15`；`docs/plan-m13.md:46-50, 1049(m14)`；`.agent-logs/0081` |
| 助手页 | `/assistant` 跳最近未归档对话，没有就新建；`/assistant/<id>` = `ThreadScreen variant="coordinator"`（无右面板）；页头：圆形头像 + 标题 + 文件库按钮 + 菜单（新对话/归档）；`Ctrl/Cmd+/` 唤起；协调宿主不可用时显示"暂不可用 + 原因"，旧对话只读 | `web/src/pages/AssistantPage.tsx:1-254` |
| 侧栏 | 桌面底栏分区（线程/助手/项目/更多）；助手分区列会话（`AssistantPane`） | `web/src/components/layout/AssistantPane.tsx`；`.agent-logs/0046` |
| 文件库 | `LibraryPanel`（对话框） | `web/src/components/assistant/LibraryPanel.tsx` |
| 默认对象 | 服务端 owner 偏好 `defaultTarget = assistant\|role`（跨设备一致，入口在"身份"页） | `internal/api/composer_prefs.go:32`；`docs/plan-m13.md:51` |
| REST | `POST /assistant/conversations`、`GET /coordinator`、`GET /automation/actions`、文件库 5 个端点、`GET/PUT /settings/assistant-identity`、`/settings/composer` | `docs/protocol.md:803-826`；`internal/api/*.go` |
| `oryn` CLI | `cmd/oryn` 里没有 assistant 子命令（grep 仅 `attach.go` 里的 `[assistant]` 文本标记 / `library.go` 的 `oryn library put`）；**CLI 无法与助手对话 [推断，基于 grep 未命中]** | `cmd/oryn/library.go:44` |

### 4.2 命令白名单

客户端对协调线程只允许（`internal/threads/coordinator.go:326-346`）：`prompt/steer/follow_up/abort/abort_and_prompt/queue_*/promote_queued_message/set_model/oryn_select_model/set_thinking_level/set_fast_mode/set_slow_mode/switch_leaf/navigate_tree/abort_branch_summary/set_label/set_entry_label/compact/clear_context/retry/set_session_name` 与 `get_*`（拒绝 `get_git_changes/get_git_diff/get_git_status/get_ssh_hosts`）。**没有** `bash`、`orynAttachments`（→ `coordinator_restricted`）、fork、分享、挂到别的 orb；客户端不能冒充 `host_tool_result`（`threads/agent.go:1616-1620`）。

### 4.3 移动端

仓库里没找到独立的 `mobile/ android/ ios/` 目录（本次 grep 这些路径"Skipped missing paths"）。`docs`/日志提到"手机底部抽屉""iOS TestFlight"（`.agent-logs/0166`、`0147`），说明移动端存在，但**本次没读到其源码，无法给出助手在移动端的具体表现**。Web 端的助手页与首屏胶囊按响应式（`useBreakpoint`）适配手机（`AssistantPage.tsx`；`plan-m13.md` R13）。

### 4.4 推送 / Telegram 能否与助手双向对话

- **通知生成**：`notify.Service.StateChanged` 对**所有线程种类**生效（`internal/notify/notify.go:312-327`，没有 kind 过滤；grep `KindWork` 在 `notify/` 无命中）：`working→idle` 且无人聚焦 → 去抖后发 `done`；进入 `error` → `error`；`InputRequested` → `input`。所以**助手回合结束也会触发推送/Telegram "完成"通知** **[推断：依据是 kind 无过滤，未读到专门针对 assistant 的测试]**。
- **Telegram 入站**：只支持**回复某条通知消息**的方式（`internal/notify/telegram.go:473-560`）：按 `chat_id:reply_to_message_id` 查通知 → 取 `n.ThreadID` → 如果是未解决的 `input` 通知就按 `uiprompt.AnswerText` 作答，否则 `th.Command(prompt|steer)`。`coordinatorAllowed("prompt")` 为真（`threads/coordinator.go:326-340`），**因此回复助手的完成通知可以向助手发 `prompt`，形成双向对话 [推断：路径通，没有专门的测试]**。
- **限制**：非回复消息一律回 `replyHint`（"回复这条消息即可把文字发给该线程"，`telegram.go:507-509`、`text.go:11`）。**不能在 Telegram 里直接"新开一个助手对话"或"@助手 问问题"**；没有命令（`/start` 之外无任何 `/xxx`）。通知链接是 `PublicURL + "/t/" + 线程id`（`notify.go:53`、`deliver.go:26`），前端 `/t/<助手对话>` 会重定向到 `/assistant/<id>`（`web/src/components/layout/useSection.test.tsx:73`）。
- **推送（FCM/APNs）**：同一条 `Message` 管道，按钮仅用于 `input`（`deliver.go:168-190`）。
- **助手主动推送**：**没有**"助手给主人发通知"的工具。助手的输出只能落在对话转录里，间接触发"完成"通知；定时叫醒自己（`set_schedule mode=thread`）也一样。

---

## 5. 能力边界与硬限制

### 5.1 能做什么（有效权限面）

- 看**所有** `kind=work` 线程：列表 + 读最近 ≤30 条消息；项目清单；统计（usage/tools/orbs/output）；定时清单。
- 对工作线程：`create_thread`、`send_message`（含 steer）、`archive_thread`（含 shipped）。
- 管理定时：新建/改/暂停/恢复/删（含 `thread` 与 `new_thread`）。
- 文件库读写与 artifact 全套（含主人评论回复）。
- **没有闸门**：主人的决定是"协调者动任意工作线程不设闸门，靠不可信内容包裹、来源头行与审计记录兜底"（`internal/coord/coord.go:1-8`）。

### 5.2 不能做什么（硬限制）

| 限制 | 机制 | 出处 |
|---|---|---|
| **不能碰 Runner 线程** | `resolveTarget`：`t.Cwd != nil` 或 orb 是 runner → `runner_unsupported`。理由：协调者读到的 orb 内容可能带注入，不能经它变成用户自己机器上的操作（sec S6） | `internal/coord/tools.go:127-153` |
| **不能操作协调对话** | 目标必须 `kind=work`：助手↔Space↔助手不能互相发/读/归档（防互相唤醒成环，结构规则） | `tools.go:127-141` |
| 不能删线程/orb/项目、改密钥/provider/设备/分享/项目设置 | 没有这类工具；提示词写明"Say so if asked" | `profile.go:33-34` |
| **没有 shell / bash** | `--no-tools --tools read,write` | `orbdrt/coordinator.go:93` |
| **没有网络** | 网络命名空间/Landlock，仅回环模型代理端口；自检要求 `1.1.1.1:443` 连不上 | `child.go:158-170, 185` |
| **没有浏览器/搜索/抓取/代码执行/图像生成** | `generate_image` 关；无 web 工具；`--no-extensions`；提示词写 "you cannot run commands or browse" | `coordinator.go:29`；`profile.go:16` |
| **没有技能/子 agent/MCP/命令/规则/AGENTS.md/LSP/扩展** | 全部 `--no-*`；`mcp.enableProjectConfig` 关；plan 非目标明写 | `coordinator.go:25-33, 93`；`plan-m13.md:73-76` |
| **没有记忆、没有 autolearn、没有 compaction 实验特性** | overlay | `coordinator.go:22-27` |
| 不能写 `~/library/out/` 以外（提示词约束） | **非技术强制**：`write` 在整个沙箱 HOME 可写，靠"每次启动前 `setup.run` 还原受管文件"兜底 | `profile.go:42`；`threads/agent.go` `prepareCoordinator` |
| **助手对话之间无安全隔离** | 共用一个沙箱，"所属对话"只是可见性边界 | `plan-m13.md:76` |
| 工具调用限速 | `create_thread` 10/10min + 40/24h；`send_message` 60/10min；`set_schedule` 10/10min + 40/24h；回报 60/h；单次宿主工具 60s 超时 | `coord/limit.go:16-20`；`threads/coordinator.go:29` |
| `new_thread` 定时 | agent 定义的最小间隔 15 分钟，不重叠，`keepLast` 只增不减 | `profile.go:33`；`schedule/service.go:102` |

### 5.3 模型选择

- 默认模型：`coordinator.model`（`"provider/id"`，必须含 `/`，`internal/config/config.go:381`）；空 = omp 配置的默认模型（`config.go:80-81`）。`CreateConversation` 在请求没给模型时回落到它（`internal/coord/spaces.go:~255`）。
- 对话级：`CreateAssistantConversationRequest{model, thinkingLevel}`（`internal/api/coordinator.go:~111-125`）；客户端可发 `set_model / oryn_select_model / set_thinking_level / set_fast_mode / set_slow_mode`（`threads/coordinator.go:326-337`）。
- **模型目录来自 `models.yml`**，随 `omphome.apply` 改写成协调宿主代理端口；`config.yml` 也下发（所以 `modelRoles` 等 role 配置对助手生效；技能/agent/子 agent 页"不作用于全局助手"，`plan-m13.md:R31`）。
- 助手的 role：回合头**始终显示助手身份**，不参与 `resolveTurnRole`（`plan-m13.md:203`）。

### 5.4 并发

- 同一时刻**至多一个** `coordinator-host` 子进程（`docs/protocol.md:949`）；所有助手对话与所有 Space 聊天**共用这一个子进程**，其下每个线程一个 omp 进程（由 orbd 管，`agentCap` 没找到上限，`grep maxAgents|MaxAgent` 无命中 **[推断：无显式并发上限]**）。
- 助手可同时在多个工作线程上"扇出"，受 `create_thread` 限速约束；回报用 `repMu` 与"登记+投递"互斥（`coord.go` 注释；`reports.go:108-128`）。
- orynd 重启会让子进程消失；ready 的协调 orb 先 stop，下条消息再 resume（`host.go` `recover`）。
- **没有"助手主动运行"的并发**：没有后台常驻任务，只有"定时叫醒自己"与"线程回报"两种自动触发。

### 5.5 成本统计

- 助手的 token/成本被记进 `usage_records`（`internal/stats/*`，按线程归集；`threads` 的 usage 观察者 `AddObserver(s.Stats)`，`server.go:292`）。`get_stats` 可按 `model/thread/project/space/purpose` 聚合；Space 协调者强制 `space=本 Space`，**助手不限**（`tools.go:776-790`）。
- 没有发现"助手单独预算/上限/告警"。`KindBudget` 是全局预算告警（`notify/notify.go:88`），**不按助手归集 [推断]**。
- **记忆/Hindsight 的 LLM 用量单独统计**在管理页，不并入统计页（`plan-m16.md` T8），与助手无关。

---

## 6. 已规划但未做的相关条目

> 本节只列文档/日志里的**明示计划或设想**。"未做"依据是对应代码在仓库中不存在。

| 条目 | 状态 | 出处 |
|---|---|---|
| **助手接入记忆**（Hindsight） | **明确排除**（Q5 / 非目标），说"另有 EverOS / 近况路线" | `docs/plan-m16.md:37, 178`；`.agent-logs/0149` |
| **近况（recent activity）层**：按"事情"组织，每日增量更新，一行概览全局注入 | 仅原型（`/tmp/everos-eval`）；**无代码** | `.agent-logs/0133, 0134, 0137, 0138` |
| **画像（用户模型）+ 去语境改写 + scope** | 仅原型；**无代码** | `.agent-logs/0137`；`0129` |
| **Hindsight 的 mental model 承载"画像/近况文档"** | 纸面分析："三个候选里唯一原生同时覆盖助手语境+作用域+文档式画像/近况"，**未实测**；近况是否遵守"最近两周"时间窗**需实测** | `.agent-logs/0152`；`0153`（更正：Memory Defense 只是敏感信息正则脱敏，不是注入检测） |
| **控制面记忆宿主工具** `remember/recall/forget` + `agent.start` 注入核心快照 + 候选/ownerQuote 闸门 | 仅方案；**无代码**（grep 工具表无此名） | `.agent-logs/0064, 0065` |
| **后台整理（dreaming）** | "二期" | `.agent-logs/0064` |
| 助手使用技能/子 agent/命令/上下文文件/扩展 | M13 明确**非目标** | `docs/plan-m13.md:73-76` |
| MCP、通用设置表单、LSP、`secrets.yml`、插件市场 | M13 非目标，"留给后续里程碑" | `docs/plan-m13.md:72` |
| 助手对话之间的安全隔离 | M13 非目标 | `docs/plan-m13.md:76` |
| 普通线程附件进文件库 | M13 非目标 | `docs/plan-m13.md:77` |
| 项目记忆"提升为全局" | M16 未选 | `docs/plan-m16.md` 非目标 |
| GitHub 凭据给协调宿主 | M15 明确"runner 和协调宿主除外"：助手**不会有 GitHub 能力** | `docs/plan-m15.md:35, 107, 521, 528` |
| 助手 `screenshot_artifact` | 工具表没有（orb 里有） | `internal/artifacts/agent.go:34` vs `profile.go:268-390` |
| 助手在 Telegram 中直接发起/@ | 无；只能回复通知 | `telegram.go:507` |
| `.agent-logs/0169` 本次升级调研 | 进行中；融合对象为 Grok、Muse、ChatGPT App、Hermes Agent | `.agent-logs/0169` |

---

## 7. 结构性瓶颈：为何它只能当"附属助手"

以下是我读完代码后的判断，每条都能指到具体机制。

1. **"协调者"定位写进了沙箱与工具集**
   系统提示词第一句就是"You have no workspace: you cannot run commands or browse"（`profile.go:16`），技术上也是 `--no-tools --tools read,write`（`orbdrt/coordinator.go:93`）。助手没有任何"自己动手完成事情"的手，唯一的杠杆是**派生别的线程**（`create_thread`）。这让它天然是"调度员"，而不是"能完成任务的 agent"。要变成 superagent，需要至少一个能执行工作的通道：要么（a）给助手自己一个受控 orb/工作区，要么（b）把"搜索/抓取/浏览器/代码执行"做成控制面宿主工具。目前两者都没有。

2. **沙箱是"一台宿主 + 一个子进程 + 全体共享"，不是"每助手一个隔离环境"**
   所有助手对话与 Space 聊天共用一个沙箱、一个 HOME、一个降权账号（`docs/protocol.md:949`；`plan-m13.md:76`）。把 bash、网络、浏览器加进来，会直接击穿"对话间可见性不是安全边界"的前提，也与"沙箱 fail-closed、无外网"的威胁模型冲突。这与"每个线程独占一台隔离 orb"的 Oryn 主线原则（任务书）正好相反；协调宿主是一个为"只读不可信数据"特意压到最小的例外。

3. **没有记忆，且三条路线都没落地**
   overlay 强制关闭 omp 记忆，M16 排除助手，EverOS/近况/Honcho 只有原型。每次对话只有转录，orb 被删后连模型上下文都清空（`ensureCoordOrb`）。助手**不知道主人是谁、最近在做什么、偏好是什么**。这是它"不像核心助手"的最大单点。

4. **上下文注入通道只有一条静态系统提示词**
   `SystemPrompt(space, projectName, threadID)` 只拼静态模板（`profile.go:62`），没有日期/时区/项目清单/近况/偏好/活跃线程摘要。`agent.start` 的 `Profile` 只有 `SystemPrompt + Tools + Setup`（`threads/coordinator.go:55-62`），没有"动态上下文块"字段；`AGENTS.md`/`--no-context-files` 全关。要注入近况必须新增 Profile 字段或每次 `agent.start` 重渲染（`RefreshAgent` 已有"静止后重启"机制，可复用）。

5. **没有"主动性"的发起面**
   只有两种自动触发：`set_schedule mode=thread`（叫醒自己）与线程回报；**没有**"助手→主人"的主动通知工具、没有事件订阅（Git push、CI 失败、邮件）、没有后台巡检。通知引擎只在**线程状态变化**时发通知（`notify.go:312-327`），助手想主动打扰主人只能"在一次被唤醒的回合里说话，等去抖后发 done"。定时 `new_thread` 间隔 ≥15 分钟、`keepLast` 单向（`profile.go:33`），也是为"失控保护"压得很紧。

6. **没有外部连接器与扩展面**
   `--no-extensions --no-skills`、`mcp` 关、`generate_image` 关、`marketplace.autoUpdate: off`（`coordinator.go:22-35`）；`coordinatorMethods` 白名单只有 8 个方法（:37-42）。助手无法使用 Oryn 已有的 GitHub 集成（M15 凭据"协调宿主除外"）、浏览器 Profile、远程桌面。M13 的 Agent 配置 GUI 管理的 skills/agents/commands 对它**全部不生效**（R31）。

7. **权限模型是"工具即权限"，没有分级/确认**
   主人拍板"不设闸门"（`coord.go:1-8`）。这对"小助手"很方便，但一旦能力扩张（执行、联网、写外部系统），缺的是**按动作分级的确认/审批**。目前只有限速与审计；而审计表助手自己读不到，也没有"撤销"。`OwnerTurn` 已被 0065 否决作授权边界，所以需要另起一套"主人亲手确认"的机制（如 `ownerQuote` 逐字核验）。

8. **统计与成本无助手维度**
   用量归在线程上，助手没有"自己的预算/限额/超额降级"；`get_stats` 助手可读全局，但没有"助手本身花了多少"的专门视图或限额 **[推断]**。

9. **多端入口不对称**
   Web 首屏、助手页、文件库完备；Telegram 只能"回复通知"；CLI 无助手入口 **[推断]**；没有"在 Telegram 里直接问助手"的入口，也没有语音/截图/分享入口。助手当前是"一个 Web 页面里的线程"，而不是"随处可达的个人 agent"。

10. **单点与可用性**
    协调宿主依赖 Landlock ABI ≥ 2/4/6 与命名空间；条件不满足时 **fail-closed**，整个助手与 Space 都不可用（`docs/protocol.md:867`；`AssistantPage.tsx` 的 "暂不可用"分支）。助手会随 orynd 重启而丢子进程，要靠 resume 回来。对"核心"来说这是风险面：Landlock 缺失的环境（旧内核、某些容器）直接没有助手。

### 7.1 对后续设计最有用的"已就绪的积木"

- **宿主工具通道**（`threads.Coordinator` 接口：`EnsureOrb / AgentProfile / HostToolCall`，`threads/coordinator.go:75-82`）：新增能力只需在 `ToolDefs` + `HostToolCall` 的 `switch` 里加一项，控制面自己执行，**无需放宽沙箱**。60s 超时 + `wrapUntrusted` 不可信包裹 + 来源头行 + 审计 + 限速的"五件套"已就位。
- **回报机制**（`coord/reports.go`）：给助手一个"派工 → 回执"的异步模型，是实现"助手 = 总管"的基础。
- **文件库 + `library.sync/fetch`**：控制面与沙箱之间已有"推文件进去、取产物出来"的双向通道。
- **`RefreshAgent` + `AgentProfile.Setup`**：每次启动重渲染系统提示词、还原受管文件，是注入近况/偏好的现成位置。
- **Hindsight 网关与候选队列**（`internal/memory`、`migrations/0036_memory.sql`）：orynd 已有"按来源强制 bank/标签、key 不下发、agent 写全局先进待批准候选"的实现，助手记忆可以复用，只需新开一个"助手 bank"，并解决 `FeatureMemory` 与 `memory.backend: off` 的 overlay 冲突。
- **Artifacts**：助手已有 13 个工具，是现成的"交付物"通路（文档/幻灯片/页面 + 评论回路）。
- **定时 `thread` 模式叫醒自己**：已是"助手自驱"的最小实现。

### 7.2 可能的冲突/代价（给方案设计者）

- 任何"给助手执行能力"的方案都要回答：**在哪个隔离域里执行**？现有答案是"每个线程独占一个 orb"，所以最自然的路径是让助手**把活派给 orb**（已有），而不是自己跑；若要自己跑，应该是"助手专属 orb"，而不是放宽协调宿主沙箱。
- 记忆写入必须沿用 0065 的结论：**模型发起的写入先进候选；`OwnerTurn` 不是授权依据**。
- Hindsight 当前的 `HINDSIGHT_SCOPING=per-project-tagged` 与"无项目=只召回全局、关闭自动写入"（R5）对助手是否合适需重新评估（助手天然是"无项目"）。
- 近况注入要遵守 0138：**一行概览全局可见，细节按需展开**；否则会再次出现"时区被断章取义成主人个人属性"（0137）。
- `config.yml/models.yml` 目前"也下发给助手"，意味着助手的 role/模型改动面与普通线程共用一份；如果助手要用独立模型/role，需要拆分下发策略。
