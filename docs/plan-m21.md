# M21 — Runner 改用 Oryn Core 与控制面下发的配置：实施计划

> 编号按现状取 M21（`docs/plan-m20.md` 是最后一份）。动手时若 `docs/plan-m21.md` 已被占用则顺延，并同步改本文里的 `accept:m21`、`m21-accept.ts`。
> 本文自包含：执行者不需要读过产生它的对话。行号只作提示，动手前按符号重读。

Goal：Runner 上的线程与 orb 里的线程体验一致：同一个 Oryn Core、同一份控制面下发的 omp 配置、同一套模型中转与 SSH 网关；与那台机器上用户自己的 omp、`~/.omp`、`~/.agents` 等用户级配置完全独立。
Approach：orbd 按编进自身的 `core/source.json` 下载并校验钉住的 Core；agent 以 `PI_CONFIG_DIR=.oryn/omp` 运行（omp 的整个配置根移到 `~/.oryn/omp`，agent 目录为 `~/.oryn/omp/agent`），并由 Core 的新开关屏蔽其它用户级发现源；控制面经现有的 `omphome.apply` 写入配置；Runner 的出站通道常驻并放行模型中转与记忆；SSH 网关照 orb 接入。密钥一律留在 orynd：配置里只有 `oryn-ph-…` 占位符。
Rollback point：步骤 1–2（Core 加 macOS 构建与隔离开关、钉新版本）对现有 orb 无行为变化（开关默认关），可单独保留。步骤 3–9 合入前在分支上，丢弃即回到现状；合入后回退 = revert，Runner 上留下的 `~/.oryn/core`、`~/.oryn/omp` 可手动删除，用户的 `~/.omp` 自始至终不被读写。没有数据库迁移（`runners.features` 是自由文本）。

## Context

主人原话：

> 对了，为什么我们现在runner没有使用oryn core🤔那还怎么使用那么多自定义rpc
> 但是我希望能有尽量相同的体验🤔不能和本机omp什么的完全独立吗？毕竟我们密钥是通过网关填入的，下发不含密钥的配置也没什么风险

### 现状（2026-11-20 核对）

- Runner 启动本机 PATH 里的 `omp`（`ORYN_AGENT_BIN` 可覆盖，`internal/orbdrt/runner.go` `RunnerConfigFromEnv`），读本机 `~/.omp/agent`。
- orbd 要求 `negotiate_protocol` 与每个 initFrame 成功（`internal/orbdrt/agent.go` `initialize`）。实测官方 omp 19.0.1：`terminal_configure`、`oryn_select_model` 回 `Unknown command`；Core 18.19.0-15 全部成功。所以带 `--terminal` 的 Runner 用官方 omp 时 agent 初始化失败、重启 3 次后放弃——现状并不是「缺功能但能用」。
- orb 的配置链路：orynd `orbs.DefaultSetup.plan` → `agentcfg.Render`（`models.yml` 的 key 换成 `oryn-ph-<id>`，baseUrl 换成 `http://127.0.0.1:7811/llm/<route>…`）→ `setup.run` / `resume.run` / `omphome.apply` → orbd `writeOmpConfigAt` 写 `~/.omp/agent` 并按 `~/.oryn/omp-home.manifest.json` 清理。
- Runner 被关掉的点：`runnerUnsupported`（含 `omphome.apply`）、hello 不报 `omphome-apply` / `ssh`、出站通道只按需开且 orynd 侧 `memoryOnlyHandler` 只放 `/memory/`、`sshgw/visible.go` 对 runner 返回 nil、`writeAgentAssets` 不对 runner 执行。
- 平台：`GOOS=darwin go build ./cmd/orbd` 通过；`GOOS=windows` 编不过（`syscall.Kill`、`Setpgid` 等，`internal/orbdrt/agent.go`、`computer.go`、`desktop.go`）。Core 只发布 linux-x64 / linux-arm64；`scripts/oryn/build-agent.ts` 已认 `bun-darwin-(arm64|x64)`。

### 已确认的决定（表单，2026-11-20）

| 编号 | 决定 | 被否的做法与理由 |
|---|---|---|
| D1 | Runner 只用 Core，移除「本机 omp + 本机配置」模式；`ORYN_AGENT_BIN` 只留给开发与测试（fakeomp） | 保留 `--local-omp`：两套行为都要测，且本机 omp 在 initFrame 上本来就会失败 |
| D2 | Core 由 orbd 直接从 GitHub Release 下载，环境变量可指向镜像 | 经 orynd 转发：orynd 要拉取并缓存全部平台的产物，多一条分发链路 |
| D3 | 这一轮对齐：模型（占位符 + 中转）、omp 配置、项目层配置、SSH 网关主机、Oryn 自带技能 | GitHub 凭据与 git 主机代理：主人未选，Runner 继续用本机 git 凭据 |
| D4 | 平台：Linux 与 macOS 本计划完成 | Windows 见「待确认」 |

### 待确认

- **Windows Runner**：Runner 进程就是 orbd（它连 orynd、启动并监督 omp、给面板提供文件 / 终端 / git、跑出站通道），只有 Core 无法接入。orbd 在 Windows 上编译不过（进程组、信号、unix socket、pty 都是 Unix 实现）。建议 Windows 机器经 WSL2 跑 Linux 版 orbd 与 Core（无需移植，步骤 9 写进文档）；原生 Windows 另立 M22。主人确认前本计划不做原生 Windows。

## Global constraints

- 密钥不进 Runner：下发给 Runner 的文件里不得出现 vault 明文；`agentcfg.Render` 的「无 baseUrl 的 provider 以 `ORYN_MODEL_KEY_<NAME>` 环境变量降级下发真实 key」分支对 Runner 必须关闭（这类 provider 在 Runner 上不可用，模型目录照常显示）。
- 不读写用户自己的 `~/.omp`、`~/.agents`、`~/.claude` 等用户级配置、`~/.ssh`、git 配置。
- Runner 的出站通道只放行 origin-form 的 `/llm/` 与 `/memory/`；`/git/`、`/gh/`、`/mcp/`、absolute-form 正向代理一律 403。
- oryn-core 只新增文件；对现有文件的改动最少几行并记入 `ORYN.md` 例外清单。

## Non-goals

- 原生 Windows Runner（建议 M22；WSL2 可用）。
- GitHub 凭据、git 主机代理、`url.insteadOf`（D3）。
- Runner 上的 Portal、受管服务、Desktop、Docker、快照、冻结 / 删除（与 M11b 相同，机器是用户的）。
- MCP 网关路由：omp-home 里的 `mcp.json` 原样下发（与 orb 相同）；需要密钥的 MCP 若在 orb 上靠 `/mcp/` 路由，本计划不给 Runner 开。

## Assumptions

- A1（步骤 0 已验证，见结论）`PI_CONFIG_DIR=.oryn/omp` 把 omp 的配置根整体移到 `~/.oryn/omp`（agent 目录、plugins、logs 都在其下），项目级 `.omp/` 不受影响。
- A2（步骤 0 已验证）用户级发现源里，`.agents` 默认开启，`~/.claude`、`~/.codex`、`~/.cursor` 等外部工具默认关闭但可被 `enabledProviders` 打开；仓库内的同类文件照常生效。Runner 需要 Core 开关把非 native 的用户级源全部关掉。
- A3 bun 在 macOS 上 `--compile` 出的二进制可直接运行（arm64 需要 ad-hoc 签名）。若不成立：步骤 1 增加 `codesign -s -` 一步。
- A4 现在的 Runner 在 macOS 上能跑（orbd 能编译；`loginEnv`、pty、`runner.sock` 在 mac 上未实测）。若不成立：macOS 部分退到 M22 一起做。
- A5 每个系统用户只跑一个 Runner（现有约束），常驻 `127.0.0.1:7811` 不会与第二个 Runner 冲突；端口被占用时 orbd 启动失败并给出 `ORYN_EGRESS_ADDR` 提示。

## Step 0 — 摸清 Core 在 Runner 上会读哪些用户级目录（调研）✅

见文末「步骤 0 结论」。

## Step 1 — oryn-core：macOS 构建与用户级发现隔离，发布新版

Depends on：步骤 0（已完成）。
Files：modify `.github/workflows/oryn-release.yml`（matrix 加 `macos-15` / `bun-darwin-arm64` / `omp-darwin-arm64` 与 `macos-15-intel` / `bun-darwin-x64` / `omp-darwin-x64`，构建前分别断言 `uname -m` 为 `arm64` / `x86_64`；mac 上的 natives 依赖用 brew 装 cmake、ninja。修订：初稿写的 `macos-14` / `macos-13` 已退役，见 actions/runner-images README）；create `packages/coding-agent/src/capability/oryn-user-isolation.ts`、`packages/coding-agent/test/oryn-user-isolation.test.ts`；modify `packages/coding-agent/src/capability/index.ts`（`isUserSourceEnabled` 开头 1 行调用）；modify `ORYN.md`（例外清单与「发布」节的产物列表）。
Produces：Release 资产 `omp-linux-x64`、`omp-linux-arm64`、`omp-darwin-arm64`、`omp-darwin-x64`、`omp-rpc-types.d.ts`、`SHA256SUMS`；环境变量 `ORYN_ISOLATE_USER_SOURCES=1`：`isUserSourceEnabled` 对一切非 `omp` / `native` 的源返回 false（项目级不受影响），并核对直接用 `os.homedir()` 的 provider（`discovery/*.ts`）是否都经过该判断，没有经过的在新文件里列出并一并屏蔽。
Do：mac 作业与 linux 一样跑 `omp --smoke-test` 与 `scripts/oryn/smoke-rpc.mjs`；arm64 产物不能运行时加 `codesign -s - <产物>`（A3）。推送 `oryn`，打 `oryn-v18.19.0-16`。
Accept：`bun test packages/coding-agent/test/oryn-user-isolation.test.ts` 通过；用步骤 0 的探针（真实 HOME 含 `~/.agents/skills`、仓库含 `.claude/skills`）以 `PI_CONFIG_DIR=.oryn/omp ORYN_ISOLATE_USER_SOURCES=1` 运行，`get_skills` 只列出仓库内技能；`gh release view oryn-v18.19.0-16 --repo Lynricsy/oryn-core --json assets -q '.assets[].name'` → 恰好上述 6 个名字；四个构建作业的 smoke 步骤都输出 `VERDICT PASS`。

## Step 2 — Oryn：钉新 Core，`core` 包导出下载所需的来源

Depends on：步骤 1（需要新 tag 的 sha256）。
Files：modify `core/source.json`（tag、commit、`sha256` 加 `darwin-arm64`、`darwin-x64`）、`core/embed.go`、`scripts/fetch-core.sh`（接受 `darwin-arm64|darwin-x64`）；test `core/embed_test.go`。
Produces：`core.Source() core.Release`，`type Release struct { Repo, Tag string; SHA256 map[string]string }`；平台键格式 `<goos>-<arch>`，arch 为 `x64` / `arm64`（与资产名 `omp-<键>` 一致）。`core.Pin()` 保持现有签名与语义。
Accept：`go test ./core/...` 通过；`scripts/fetch-core.sh x64` 输出「已下载 …@oryn-v18.19.0-16」且 sha 校验通过；`web/src/omp/rpc-types.d.ts` 与 Release 的 `omp-rpc-types.d.ts` sha 一致（不一致则用 Release 版覆盖）。

## Step 3 — orbd Runner：下载、校验、缓存 Core

Depends on：步骤 2（`core.Source()`）。
Files：create `internal/orbdrt/corefetch.go`、`internal/orbdrt/corefetch_test.go`；modify `internal/orbdrt/runner.go`（`RunnerConfigFromEnv`）、`internal/orbdrt/runtime.go`（runner 启动时确定 `AgentBin`）。
Consumes：`core.Source()`。
Produces：`func ensureCore(ctx context.Context, home string, rel core.Release, client *http.Client) (string, error)`，返回 `~/.oryn/core/<tag>/omp` 的绝对路径。
Do：平台键取 `runtime.GOOS` + (`amd64`→`x64`)；不在 `rel.SHA256` 里的平台立即报错「此平台没有 Oryn Core 构建」。URL = `${ORYN_CORE_BASE_URL:-https://github.com/<Repo>/releases/download}/<Tag>/omp-<键>`。下载到同目录临时文件、校验 sha256、`chmod 0755`、rename；已存在且 sha 一致直接返回；成功后删除 `~/.oryn/core/` 下其它 tag 目录。下载失败时 orbd 启动失败并打印 URL 与 `ORYN_CORE_BASE_URL` 提示（不回退到 PATH 的 omp，D1）。`ORYN_AGENT_BIN` 非空时跳过下载（开发 / 测试）。删除 runner 分支里「保留 PATH 中 `$HOME` 下目录以便找到 `~/.bun/bin/omp`」的特例（`binHome`），Runner 与 orb 一样只信任 `$HOME` 之外的 PATH 项。
Accept：`go test ./internal/orbdrt/ -run 'CoreFetch'` 通过，用 `httptest` 覆盖：首次下载、sha 不符拒绝且不留文件、已缓存不再请求、换 tag 后清理旧目录、未知平台报错、`ORYN_CORE_BASE_URL` 生效。

## Step 4 — orbd Runner：独立 omp 配置根与配置写入

Depends on：步骤 1（隔离开关），步骤 3（agent 是 Core）。
Files：modify `internal/orbdrt/runner.go`（`runnerUnsupported` 去掉 `MethodOmpHomeApply`；hello 特性加 `omphome-apply`）、`internal/orbdrt/setup.go`（`writeOmpConfigAt` / `writeOmpHome` / `pruneOmpHome` 的 agent 目录与 manifest 路径参数化）、`internal/orbdrt/agent.go`（runner 的 agent env 加 `PI_CONFIG_DIR=.oryn/omp`、`ORYN_ISOLATE_USER_SOURCES=1`）、`internal/orbdrt/runtime.go`、`internal/orbdrt/selfsetup.go`（runner 把 `oryn-library` 技能写到 `~/.oryn/omp/agent/skills/oryn-library`，manifest 不收录、prune 不删）；test `internal/orbdrt/runner_test.go`。
Produces：Runner 的 omp 配置根 `$HOME/.oryn/omp`，agent 目录 `$HOME/.oryn/omp/agent`，manifest `$HOME/.oryn/omp-home.manifest.json`（与 orb 同名，Runner 上此前不存在），项目层仍是 `$HOME/.oryn/project-config/.omp`；SSH 配置写 `$HOME/.oryn/omp/agent/ssh.json`。
Do：orb 路径与行为不变（目录仍是 `~/.omp/agent`）。Runner 在收到首个 `omphome.apply` 之前拒绝 `agent.start`（ctl 错误 `config_pending`，消息「等待控制面下发 omp 配置」），由 orynd 步骤 5 保证先发配置。
Accept：`go test ./internal/orbdrt/ -run 'Runner'` 通过，覆盖：`omphome.apply` 写进 `~/.oryn/omp/agent` 且不触碰临时 HOME 下预先放好的 `~/.omp/agent/config.yml`（内容与 mtime 不变）；prune 只删上次下发的文件；首个 apply 前 `agent.start` 回 `config_pending`；agent 进程环境含 `PI_CONFIG_DIR=.oryn/omp` 与 `ORYN_ISOLATE_USER_SOURCES=1`。

## Step 5 — orynd：为 Runner 生成并下发配置、放行模型中转与 SSH

Depends on：步骤 4（Runner 接受 `omphome.apply`、上报特性）。
Files：modify `internal/orbs/setup.go`（`plan` 的 runner 分支：omp-home + 项目层 + SSH，不带 GitHub / git / env / scripts）、`internal/agentcfg/agentcfg.go`（`Render` 增加选项关闭 `ORYN_MODEL_KEY_<NAME>` 降级下发）、`internal/orbs/apply.go`（`applyTargets` 包含在线 Runner）、`internal/orbs/runner.go` / `link.go`（`runnerHello` 先同步 `omphome.apply` 并 `setPolicy`，成功后才进 ready；失败时停在 connecting 并记录错误）、`internal/orbs/egress.go`（`memoryOnlyHandler` 改为 `runnerEgressHandler`：只放 origin-form 的 `/llm/` 与 `/memory/`）、`internal/sshgw/visible.go`（Runner 按线程项目返回可见主机，规则与 orb 相同，含「全自动线程只用允许的主机」）；test 对应的 `_test.go`。
Consumes：Runner hello 特性 `omphome-apply`、`ssh`。
Do：配置变更走现有 `ApplyOmpHome` → `threads/apply.go` 的 reload / 重启判定，Runner 与 orb 一视同仁。
Accept：`go test ./internal/orbs/... ./internal/agentcfg/... ./internal/sshgw/...` 通过，新增用例覆盖：Runner 的 plan 里 `models.yml` 只有 `oryn-ph-` 与 `http://127.0.0.1:7811/llm/`、没有任何 vault 明文、没有 `ORYN_MODEL_KEY_`；Runner 经 egress 请求 `/git/…`、`/gh/…`、absolute-form 均 403，`/llm/<route>/…` 被注入密钥后转发；hello 后 ready 之前已收到 `omphome.apply`。

## Step 6 — orbd Runner：常驻出站通道与 SSH 网关

Depends on：步骤 5（orynd 放行 `/llm/`、下发 SSH 配置）。
Files：modify `internal/orbdrt/runtime.go`（runner 启动即监听 `cfg.EgressAddr`，不再等首个带记忆的 `agent.start`；监听 SSH `127.0.0.1:7822` 并上报 `ssh` 特性）、`internal/orbdrt/memory.go`（删除按需开通道的逻辑）、`internal/orbdrt/agent.go`（runner 的 agent env 加 `ORYN_SSH=1`）；test `internal/orbdrt/runner_test.go`。
Do：端口占用时启动失败并提示 `ORYN_EGRESS_ADDR`（A5）。
Accept：`go test ./internal/orbdrt/ -run 'Runner|Egress|SSH'` 通过，覆盖：启动后 `127.0.0.1:<端口>` 可连且请求经 link 的 egress 流转发；hello 特性含 `ssh`、`omphome-apply`、`memory`。

## Step 7 — 扩展与指引

Depends on：步骤 4。
Files：modify `internal/orbdrt/assets/oryn-orb.ts`（`ORYN_RUNNER=1` 分支的指引补上「模型与 SSH 主机由 Oryn 下发」，其余 orb 专有指引——服务、Portal、Docker、setup——仍不注册）。
Accept：`go test ./internal/orbdrt/` 通过（扩展资产测试）；`bun build --no-bundle internal/orbdrt/assets/oryn-orb.ts` 无语法错误。

## Step 8 — 端到端验收脚本

Depends on：步骤 3–7。
Files：create `web/scripts/m21-accept.ts`；modify `web/package.json`（`"accept:m21": "bun scripts/m21-accept.ts"`）。
Do：在临时 HOME 下起开发版 orynd 与 `orbd run --runner --dir <临时仓库>`（`ORYN_CORE_BASE_URL` 指向本地静态服务，提供 `build/core/x64/omp`），中央配置放一个指向本地假模型服务的 provider；预先写一份带哨兵内容的 `~/.omp/agent/config.yml` 与 `~/.agents/skills/sentinel/SKILL.md`。经 API 建 Runner 线程发一条提示词。
Accept：`cd web && bun run accept:m21` 末行 `VERDICT PASS`，脚本断言：agent 进程的可执行文件是 `~/.oryn/core/oryn-v18.19.0-16/omp`；`~/.oryn/omp/agent/models.yml` 只含占位符；假模型服务收到的请求带真实 key（由 orynd 注入）；哨兵 `~/.omp/agent/config.yml` 内容与 mtime 未变，`sentinel` 技能不在线程的技能列表里；带 `--terminal` 时 agent 初始化成功。

## Step 9 — 文档

Depends on：步骤 3–8。
Files：modify `docs/deploy.md` 第 11 节（安全边界改写：控制面下发无密钥配置、Runner 可经中转消耗中央模型额度但拿不到密钥；启动不再要求 omp 在 PATH；`ORYN_CORE_BASE_URL`、`ORYN_EGRESS_ADDR`；macOS 支持；Windows 经 WSL2）、`docs/protocol.md`（M11b 节、`omphome.apply` 节、Runner egress 放行路径、`config_pending`）、`README.md` 中 Runner 相关段落。
Accept：`rg -n '确保 .omp. 与 .git. 在 PATH|那台机器自己的 omp 配置|runner 不上报且回 .not_supported.' docs README.md` 无输出；`make lint-spec` 通过（若改了 schema）。

## 并行性

- 步骤 1（oryn-core）与步骤 3 的代码编写可并行：二者只经步骤 2 的 `core.Release` 结构衔接，该结构本文已冻结；步骤 3 的测试不依赖真实 Release。
- 步骤 4 与步骤 5 不并行：5 依赖 4 上报的特性与 `config_pending` 语义。
- 其余顺序执行。

## 步骤 0 结论（2026-11-20）

探针：本机 Core 18.19.0-15（`build/core/x64/omp`）以 `--mode rpc` 启动，真实 HOME（含 `~/.agents/skills`、`~/.claude/skills`、`~/.claude/CLAUDE.md`、`~/.codex/AGENTS.md`、`~/.omp/plugins`），cwd 为含 `.claude/skills/repo-skill` 与 `AGENTS.md` 的临时仓库；读 `get_skills`、`get_context_report`、`get_agent_definitions`、`get_prompt_templates`、`get_mcp_servers`、`get_hooks` 里出现的路径。

| 发现源 | 只设 `PI_CODING_AGENT_DIR` | 设 `PI_CONFIG_DIR=<隔离目录>` | 原因 / 手段 |
|---|---|---|---|
| `~/.omp/agent` | 不读 | 不读 | agent 目录被覆盖 |
| `~/.omp/plugins`（runpod 插件的 8 个技能） | **读** | 不读 | 插件目录取配置根（`getPluginsDir` → `~/.omp/plugins`），与 agent 目录无关；`PI_CONFIG_DIR` 移走整个配置根 |
| `~/.agents/skills` | **读** | **读** | `.agents` 用户级源默认开启（`capability/index.ts` `isUserSourceEnabled`）→ 需要 Core 开关 |
| `~/.claude/skills`、`~/.claude/CLAUDE.md`、`~/.codex/AGENTS.md` | 不读 | 不读 | 外部工具的用户级源默认关闭（`FOREIGN_USER_PROVIDERS`），但中央 `config.yml` 的 `enabledProviders` 可打开 → 同样由 Core 开关兜住 |
| 仓库内 `.claude/skills`、`AGENTS.md` | 读 | 读 | 项目级，应保留（`instructionFilesTokens` 只有仓库那份的大小） |

修订（相对初稿）：A1 由「`PI_CODING_AGENT_DIR` 足够」改为「用 `PI_CONFIG_DIR=.oryn/omp`」——前者漏掉 `~/.omp/plugins`；Runner 的 omp 目录因此从 `~/.oryn/omp-agent` 改为 `~/.oryn/omp/agent`（步骤 4）；步骤 1 的隔离开关由「按需」改为必做，名称定为 `ORYN_ISOLATE_USER_SOURCES`。

## 执行中修订（2026-11-20）

- 步骤 1：mac 作业改用 `macos-15`（arm64）与 `macos-15-intel`（x64）并断言 `uname -m`——`macos-14` 已于 2026-11-02 退役、`macos-13` 更早下线。
- 步骤 4 / 5 / D3：`oryn-library` 不能原样复制到 Runner（它要求运行 `oryn library put`，Runner 上没有 `oryn` CLI、不监听 `orbd.sock`，orynd 也不放行 `library.put`）。保留需求，按最小开放实现：Runner 扩展注册工具 `oryn_library_put{path, name?}` → `runner.sock` 只多认 `library.put`（路径须在线程工作目录内、是普通文件，用 `stageLibraryPaths` 只登记该文件，结束后撤销）→ ctl `library.put`；orynd 的 `runnerRequests` 只加 `library.put`，`library.fetch` 仍只服务显式登记的路径，其它文件库操作不开放。Runner 写 runner 版技能 `~/.oryn/omp/agent/skills/oryn-library/SKILL.md`（存入方式写成调用该工具；不进 manifest）。`oryn-setup` 是 orb setup 专用，不写。验收：步骤 8 增加「Runner 线程经 `oryn_library_put` 存入文件，文件库里出现该文件，来源为该线程」。
- 步骤 1 增项：隔离开关打开时，Core 发起的 ssh 加 `-o UserKnownHostsFile=<配置根>/ssh/known_hosts`，不写用户的 `~/.ssh/known_hosts`（Core 默认 `StrictHostKeyChecking=accept-new` 会追加该文件）。
- 步骤 4 / 5 / D3 项目层：Runner 的常驻 orb 行不带 project_id，项目在线程行上，一台 Runner 同时服务多个项目，不能用一份 omp-home 表达。改为按线程交付：`LinkCtlAgentStartParams` 增加可选 `projectId`、`projectConfig`（复用 `LinkCtlProjectConfig`，改 schema 并重新生成），orynd 只对属于有项目层的项目的 Runner 线程填写；orbd 写到按内容寻址的 `$HOME/.oryn/project-config/<projectId>-<sha12>/.omp`（原子写入、复用，无运行中 agent 引用的旧版本清理），该 agent 的 `ORYN_CENTRAL_CONFIG` / `ORYN_PROJECT_CONFIG_ROOT` / `--config` 照 orb 的 `applyProjectConfig`。项目层变更沿用 `threads/apply.go` 的生效判定。初稿「项目层仍是 `$HOME/.oryn/project-config/.omp`、经 omphome.apply」作废。
- 项目级 SSH 主机（主人决定本轮补上，2026-11-20）：网关此前只知道连接来自哪台 Runner（流头不带线程，服务端 `NoClientAuth`），一台 Runner 服务多个项目时无法按项目放行。方案——每线程一把密钥，私钥不离开 Runner，不新增 ctl 方法：
  - orbd 在 Runner 上为每个线程的**监督器**生成一次 ed25519 密钥对（不是每次 `agent.start`）：私钥写 `$HOME/.oryn/threads/<线程>/ssh/id_ed25519`（0600，目录 0700），agent env 设 `ORYN_SSH_KEY=<该路径>`；公钥（authorized_keys 格式）随 `LinkCtlAgentStartResult` 新增的可选字段 `sshPublicKey` 回给 orynd。已有活动监督器时 `agent.start` 幂等返回原公钥；崩溃后监督器以旧 env 自重启，沿用同一把密钥；监督器彻底结束才删私钥。
  - orbd 把 Core 用的 `<配置根>/ssh/config`（Core 隔离模式的 `-F`）与 bash 用的 `~/.oryn/ssh/config` 写成同一份内容：每个网关主机的 `Host` 块，以及一个 `Host 127.0.0.1` 块（Core 以 ssh.json 的显式 host / port / user 连接时按它匹配），都带 `IdentityFile ${ORYN_SSH_KEY}`、`IdentitiesOnly yes`、`UserKnownHostsFile <配置根>/ssh/known_hosts`（OpenSSH ≥ 8.4 展开 `${ENV}`）。
  - orynd 记录「线程 → 公钥」（内存表；`agent.start` 成功时登记或同值刷新；`agent.status exited` 且 `RestartsLeft > 0` 时保留，`RestartsLeft == 0`、线程停止 / 归档、Runner 离线定时器到期时注销——link 在宽限期内重连不清表）；网关对 Runner 的流改为必须公钥认证，`PublicKeyCallback` 按公钥找到线程、核对线程的 `orb_id` 等于这条流的 orb，再按该线程的项目与自动化标记套用与 orb 相同的可见性规则（全局 + 本项目主机；全自动线程只用允许的主机）。orb 的流仍 `NoClientAuth`，行为不变。
  - 局限：同一系统用户下的进程都能读到别的线程的私钥文件。它防的是 agent 误用别的项目的主机，不防恶意本机进程——与「Runner 完全信任控制面、机器归用户」的边界一致，文档写明。
  - Core 连接复用：同一 Runner 的各线程共用配置根，Core 的 ssh master（`getSshControlDir()` 下的 `%C.sock`，`%C` 不含 IdentityFile）会被别的线程经 `ssh -O check` 复用，网关收不到第二把公钥、权限与审计归错线程。隔离开关打开且设了 `ORYN_SSH_KEY` 时，Core 的控制目录按密钥分开（`<控制目录>/<sha256(ORYN_SSH_KEY) 前 12 位>`）。
  - 测试：两个项目的 Runner 线程各自只能连本项目与全局主机；无公钥、未登记公钥、属于别的 Runner 的线程的公钥一律拒绝；全自动线程只用允许的主机；orb 不受影响；agent 崩溃自重启后、幂等 `agent.start` 后、Runner link 宽限期内重连后 SSH 仍能连；`RestartsLeft == 0` 后公钥失效；旧监督器的结束晚于新 `agent.start` 到达时新公钥仍有效。步骤 8 端到端：两个线程各起一个真实 Core 同连一个全局主机，网关审计分别记到两个线程。
- 发布（主人决定，2026-11-20）：oryn-core 本地已被另一会话合并到 omp 19.0.1，等该会话完成 19 的同步验证后再发布 Core 并钉版本（tag 将是 `oryn-v19.0.1-<序号>`，不是上文的 `-16`）。在此之前步骤 8 的端到端验收以 `ORYN_AGENT_BIN` 指向本地源码的 Core 运行（下载、校验、缓存由步骤 3 的单测覆盖），发布后再用真实下载补跑一次。
- 无密钥投递校验（审查发现，2026-11-20）：`planRunner` 的 `NoEnvFallback` 只管 `models.yml`，其余文件逐字复制，`agentcfg.Import` 对疑似明文只告警——「密钥不进 Runner」此前只对密钥库注入值成立。补一道 Runner 专用校验：下发前对全局层与按线程交付的项目层逐个文本文件跑 `ScanSecretLike`，并比对分享前扫描所用的已知密钥明文（密钥库全部值 + SSH 密码登录主机的密码）；命中的文件不下发，全局层写 orb 事件备注、项目层写线程事件，说明哪个文件未下发。orb 的下发不变。测试：中央 / 项目配置里的测试凭据不出现在 Runner 的 `omphome.apply` / `agent.start` 中。
