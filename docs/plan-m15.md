# M15 — GitHub 改用 OAuth App，并补齐 robomp 等价能力：实施计划

## 修订记录

- **v1（2026-11-16）**：初稿。
- **v2（2026-11-16）**：按 oracle 评审修订，完整意见见 `.agent-logs` 第 0121 条。
  - 契约步骤改为只做加法，破坏性删除后移到 1.5 和 8.1。原因：v1 从 0.3 起有十几步无法构建。
  - 事件入口（hook、验签、投递轮询）提前到删除 App 代码之前。原因：v1 中阶段 2 结束时没有任何事件来源。
  - 防回流改为请求侧预登记，`/gh/` 路由去掉 `Accept-Encoding`。原因：gh 的 mutation 响应不带对象 id，生产环境的响应是 gzip。
  - 补上 OAuth App 的组织访问限制与 SAML SSO、`canDrive` 改用 `author_association`、保留 `GitRepos`、保护 Oryn 自己的 hook、门禁覆盖表、失败即拒绝、修正发布推送命令、仓库级 watch、跨仓库 PR 关联防劫持、0.3 一次列全并写清 Down、投递载荷保留、`uploads.github.com`、审计的三处缺口、R24 的挂载点、hook 细节、robomp 的 `reopened`/`ready_for_review`/评审机器人白名单/`reviewing` 状态、验收命令修正。
  - 新增主人决定：「任何人」档规则和外部 PR 评审在 Agent 账号不可用时不回退到我的账号。
  - 简化：无条件删除刷新链路，GraphQL 不缓冲响应，任务时限用列加扫描实现。
- **v3（2026-11-16，执行中修订）**：
  - 0.2 探查得出投递日志接口单独计额、304 不计数，hook 的 `secret` 被打码，据此修改了 1.3 的轮询和 1.2 的同步方式。
  - 0.4 的 OpenAPI 改为与 0.3 并行起草（只改 `schema/openapi.yaml`，等 0.3 结束再统一 `make gen`）。
  - 7.3 的提示词文件提前完成（`internal/github/presets/*.md`）。由此带来三处修订：
    - 触发提示词新增模板变量 `{{autoclose_hours}}`（4.1）；
    - `github_release_status` 必须返回 `tagOid`（6.1）；
    - `presets.go` 加载文件时去掉开头的 `<!-- … -->` 出处注释（7.3）。
  - R25 的完成提醒改为只对分类为 `bug`、`documentation` 或尚未分类的线程发送（预设作者发现：`question` 等分类停在 `new` 是正常收尾）。

## Context

主人原话：「直接 GitHub 整个改成用 OAuth App 授权吧，不再依赖安装仓库级 app。对外发言不需要人工确认。关闭或合并别人的 PR、打标签、提交 review、改仓库设置等都要支持。凡是 agent 工作中可能需要的操作都要支持。至少覆盖 robo.omp（`can1357/oh-my-pi` 的 `python/robomp`）的全功能。」

确认后的决定（2026-11-16，见 `.agent-logs` 第 0118 条与本文修订记录）：

- **Q1 安全模型**：出站代理不按仓库拦截 GitHub。真实令牌仍然不进 orb；最小权限靠账号区分，全自动线程默认用 Agent 账号。agent 的发言不追加任何标记。
- **Q2 凭据范围**：所有工作 orb 都有 GitHub 凭据，包括没有项目的线程；runner 和协调宿主除外。
- **Q3 OAuth scope**：`repo workflow read:org admin:repo_hook gist write:packages user:email`。
- **Q4 驱动者档位**：`writers`、`writers_and_author`（默认）、`anyone`（按人限流）。
- **Q5 门禁**：项目级配置，由出站代理强制执行。默认关闭，robomp 预设中打开。
- **Q6 回退**：「任何人」档规则和外部 PR 评审规则在 Agent 账号不可用时不回退，事件记为 `ignored:agent_required`，同时通知主人。其余全自动线程回退到我的账号，并在线程中写一条提示。

### 现状

已核对代码。行号只作提示，动手前按符号重读。

- **M14 的 App 耦合点**：
  - ① manifest 与安装同步：`app.go`、`installations.go`。
  - ② 安装令牌：`client.go` 的 `installToken`，调用方是 `bind.go`、`gate.go`、`checks.go`、`refs.go`、`pulls.go`、`triggers.go`。
  - ③ 收窄令牌：`orbs.go` 的 `issueScoped`。
  - ④ App webhook 验签与 `/app/hook/deliveries` 轮询：`events.go`、`poll.go`。
  - 「已绑定」的判定条件 `github_installation_id IS NOT NULL` 出现在 `queries/github.sql` 的 6 处、`bind.go` 的 `ProjectGitHubAPI`、`orbs.go` 的 `orbManaged`。
- **保留不动的部分**：身份槽位、OAuth 网页流、触发规则、PR 关联与合并标记、回流 outbox、CI 汇总、`#` 引用、`gh.sock` 与占位令牌 `oryn-proxied`、`GH_CONFIG_DIR`、git `insteadOf`。
- **`egress.AgentMarker` 的使用点**：`egress.go` 的 `markRequest`、`gate.go` 的 `isSelf`、`triggers.go` 的 `announce`。
- **可以复用的现有机制**：
  - `threads.CreateParams` 已有 `Model`、`ThinkingLevel`；
  - `threads.Command` 有 `abort`、`set_model` 帧（`threads/coordinator.go`）；
  - `orbs.Pause(ctx, orbID, mode)`（`orbs/service.go`）；
  - orb 内工具的范例：`oryn-orb.ts` → orbd → ctl `artifact.call`；
  - 迁移重建 CHECK 约束的先例：`0018_analytics.sql` 的 `_keep` 搬表。
- **迁移与已知缺陷**：
  - 最新迁移是 `0032_artifact_publish.sql`；
  - `github_deliveries.source` 的 CHECK 只允许 `webhook`、`poll`；
  - `FinishGithubDelivery` 会把 `payload` 置为 NULL；
  - orbd 的 gh 代理只把 `api.github.com` 转给 egress。

### 外部事实

带「实测」的由步骤 0.2 确认。

- **GitHub App 走不通**：GitHub App 用户令牌对没装 App 的仓库只有隐式读权限，所以 App 路线无法写入非自有仓库。
- **OAuth App 的基本限制**：令牌不能按仓库收窄、不过期、没有刷新令牌；没有 App 级 webhook 和投递日志；只能在网页上手动注册。
- **组织访问限制**：新建组织默认开启 OAuth App 访问限制。未经组织批准的 OAuth App：
  - 读不到该组织的私有资源；
  - 对公开资源也没有 privileged 的创建、更新、删除操作；
  - 收不到私有仓库的 hook 投递。

  开启 SAML SSO 的组织要求令牌另行做 SSO 授权，否则返回 403，并带 `X-GitHub-SSO` 响应头。
- **仓库 hook**：`repo`、`admin:repo_hook` 两个 scope 都允许管理仓库 hook，以及 `GET /repos/{o}/{r}/hooks/{id}/deliveries[/{id}]`。投递请求带 `X-GitHub-Hook-ID` 和 `X-GitHub-Hook-Installation-Target-ID`。仓库 hook 只投递 `check_suite` 的 `completed`。
- **gh 的 GraphQL 形态**（cli/cli 源码）：
  - `gh pr create`：`createPullRequest`，随后调 `updatePullRequest` 与 `requestReviews*`；
  - `gh pr review`：`addPullRequestReview`，只选 `clientMutationId`；
  - `gh pr|issue comment`：`addComment`，只选 URL。
- **权限查询接口**：`GET …/collaborators/{login}/permission` 要求调用者有 push 权限，否则返回 403。

### 需求（每条可由第三方判真假）

**A. OAuth 连接**

- **R1** 设置页「GitHub」能保存 OAuth App 的 Client ID 和 Client Secret，页面显示需要登记的回调地址 `<public_url>/api/v1/public/github/oauth/callback`，以及可修改的 webhook 基址（默认等于 `public_url`）。删除配置时，先删除 Oryn 建的全部仓库 hook，再断开两个身份。页面上不再出现 App 的创建、安装、同步。
- **R2** 「我的账号」和「Agent 账号」各自通过 OAuth 网页流连接，授权请求的 `scope` 恰好是 Q3 的七项。连接后显示：
  - 登录名、头像，以及实际授予的 scope（取自 `X-OAuth-Scopes`）；
  - 缺少 scope 时显示「需要重新授权」。

  两个槽位不能是同一账号。任何 GitHub 调用返回 401 时，该槽位置为 `reauth`。
- **R3** 升级后删除旧 App 数据，两个槽位都要重新连接。项目的 `github_repo_id`、触发规则、PR 关联、`thread_issues` 保留。
- **R4** 组织限制可见：
  - 绑定或建 hook 时遇到 OAuth App 组织限制（403，message 含 `OAuth App access restrictions`），项目或 hook 状态为 `org_restricted`，界面给出申请批准的链接 `<web_url>/settings/connections/applications/<client_id>`；
  - 遇到 SSO（403 且带 `X-GitHub-SSO`），状态为 `sso_required`，界面给出响应头里的授权链接。

**B. orb 凭据**

- **R5** 只要线程身份对应的槽位是 `ok`，任何工作 orb（有没有项目都一样）满足：
  - `gh api user` 返回该槽位的登录名；
  - 走 `api.github.com` 与 `uploads.github.com` 的 gh 子命令都可用（包括 `gh release upload`）；
  - `git clone/fetch/push https://github.com/<任意>/<任意>` 由出站代理注入令牌，能做什么取决于账号自身的权限；
  - 其它 git 主机（PAT）的规则不变：有项目时只放行项目仓库，无项目时任意仓库只读；
  - orb 的环境变量、磁盘、出站代理的响应里都没有真实令牌；
  - runner 与协调宿主不下发 GitHub 凭据。
- **R6** 身份选择：
  - 触发规则、项目级定时、发布哨兵创建的线程（全自动线程）用 Agent 账号；
  - Agent 账号不可用时按 Q6 处理；
  - 其余线程用我的账号；
  - orb 内派生的子线程沿用父线程的身份；
  - 断开 Agent 账号不会把「自动化任务的 GitHub 身份」设置改回 `user`；该设置默认值为 `agent`。
- **R7** 防回流：出站代理不改写任何请求正文。agent 经出站代理发起的写操作（评论、review、行内评论、issue、PR、标签、状态变化），对应事件不回流到任何线程，也不触发规则。Agent 账号发出的事件一律不回流。

**C. 绑定与事件**

- **R8** 项目 `repoUrl` 为 `https://github.com/<owner>/<repo>`，且我的账号能读取该仓库时，项目自动绑定（记录 `github_repo_id`）；读不到时显示「未绑定」及原因（`not_found`、`org_restricted`、`sso_required`）。改名和转移后自动跟随（现有行为）。
- **R9** 仓库 hook：
  - 已绑定仓库上，如果我的账号是 admin，Oryn 自动创建或校正一个仓库 hook，参数为 `content_type=json`、secret 每个 hook 独立、事件取全局约束里的 `hookEvents`；
  - GitHub 能访问 webhook 地址时实时处理，否则轮询该 hook 的投递日志补漏；同一投递只处理一次；
  - 最后一个绑定该仓库的项目解绑或删除时，删除 hook；
  - 设置页按仓库列出 hook 状态：`active`、`no_admin`、`org_restricted`、`sso_required`、`error`；
  - agent 经出站代理修改或删除 Oryn 自己的 hook 时，请求被拒绝。
- **R10** 没有 `active` hook 的已绑定仓库：
  - 只要该仓库有启用的触发规则，控制面就每 60 秒轮询仓库级的新 issue、新评论、新 PR、`workflow_run`；
  - 线程关联的 issue 和 PR，只要所在仓库没有 `active` hook（例如向上游开的 PR），就轮询该对象本身（PR 状态、评论、review、CI）；
  - 轮询都带 ETag，结果合成为与 webhook 结构相同的内部事件，后续行为与 webhook 路径一致。
- **R11** M14 的以下行为不变：PR 关联、合并后设置 `shippedAt`、回流合并窗口、CI 失败回流（最多 5 轮）、`#` 引用、「推送并开 PR」按钮。

  PR 关联不再要求 PR 与项目在同一仓库，但跨仓库关联只有两种情况会建立：出站代理观察到本线程创建了该 PR；或 PR 作者是已连接的两个身份之一，且 head 提交是本线程推送的。

**D. robomp 等价能力**

- **R12** 触发事件：
  - 新增 `issue_reopened`；
  - 新增 `pull_request_opened`：匹配 `opened`、`reopened`、`ready_for_review`，只处理非草稿、`open` 状态、作者非 bot 的 PR；
  - 新增 `workflow_run_completed`；
  - 保留 `issue_opened`、`issue_labeled`、`mention`。
- **R13** 驱动者档位：
  - `writers`：`author_association` 为 OWNER、MEMBER 或 COLLABORATOR，或者我的账号在该仓库有 push 权限且权限接口返回 write/admin；
  - `writers_and_author`：在 `writers` 之外，再加 issue 或 PR 的作者；
  - `anyone`：任何人，但按发送者限流——OWNER/MEMBER/COLLABORATOR 不限，CONTRIBUTOR 每小时 10 次，其余每小时 3 次，超限记为 `ignored:sender_rate_limited`；
  - 规则可以配置「评审机器人」登录名列表，列表中的 bot 发言视为维护者指令，不需要 @；
  - 档位同样作用于已关联线程的跟进评论；
  - `issues.labeled` 事件的 sender 判定：等于仓库 owner、在规则维护者列表中，或在有 push 权限时查权限接口。
- **R14** 模型与思考级别：
  - 每条规则可以配置模型池（随机取一个）和思考级别；
  - 触发正文或跟进评论中单独成行的 `/model <别名>`、`/thinking <级别>` 用于覆盖，只认维护者（OWNER、MEMBER、COLLABORATOR 或规则维护者列表）；
  - 别名匹配顺序：完整 id，然后斜杠后的短名，最后子串；
  - 思考级别别名：`off|none|no`、`lo|low`、`med|medium`、`hi|high`、`xhi|xhigh`、`max`。
- **R15** issue 线程状态机：
  - 状态集合：`new`、`reproducing`、`fixing`、`reviewing`、`opened`、`needs_info`、`merged`、`closed`、`abandoned`，线程页头可见；
  - `issues.closed` 或 PR 合并时，状态变为 `closed` 或 `merged`，orb 冻结，线程不删除；
  - 处于终态时，非指令评论只会收到一条固定回帖；
  - 维护者的 `@<Agent 账号>` 指令会重开该线程。
- **R16** 实现授权：规则开启「实现需授权」后，只有以下两种情况，出站代理才放行本线程为该仓库创建 PR，否则拒绝并说明原因：
  - 本线程的分类是 `bug` 或 `documentation`；
  - OWNER 或规则维护者已打上 `accepted` 标签，或评论里含 `go ahead`（不区分大小写）。
- **R17** orb 内 omp 工具：`github_classify_issue`、`github_task_state`、`github_prepare`、`github_autoclose`、`github_release_status`、`github_abort_task`，语义见步骤 5.2 和 6.1。
- **R18** 问题自动关闭：
  - agent 调用 `github_autoclose` 时，记录本线程身份在该 issue 上的最近一条评论；N 小时后（默认 4 小时，规则可配）只检查这条评论上 issue 作者的 👎；
  - 有 👎 就取消；没有就以 `state_reason=completed` 关闭 issue；
  - 期间任何人发新评论，或 issue 被外部关闭，都同步取消；
  - 每条排期只执行一次。
- **R19** 门禁（项目级，默认关闭），覆盖范围见 5.4 的覆盖表：
  - **推送门禁**：经出站代理推送到 github.com 的每个 `refs/heads/*` 新 SHA，都必须有本 orb 的 `push` 级以上 `github_prepare` 通过记录。拒绝时 git 客户端显示 `oryn: <原因>`。
  - **PR 门禁**：创建 PR 时，head SHA 必须有 `pr` 级通过记录，正文满足项目配置的必需二级标题和 `Fixes|Closes|Resolves #N` 规则。
  - **改 ref 的等价 API**：门禁开启时，`contents`、`git/refs`、`merges` 等能改 ref 的 API 按覆盖表检查或拒绝。
  - **跳过**：`skip_checks=true` 产生带 `skipped` 标记的记录，同样放行，但计入审计。
  - **失败即拒绝**：门禁开启时，写请求体超过上限、无法解析或带压缩编码，一律拒绝。
- **R20** `github_prepare` 在线程工作目录里按顺序执行，结果上报控制面：
  1. HEAD 不是 detached；
  2. 当前分支不是默认分支（发布线程例外）；
  3. `origin/<默认分支>..HEAD` 非空；
  4. 把提交信息中的字面量 `\n` 改写为真换行（只改信息）；
  5. 运行修复命令，并把产生的 diff 并入 HEAD 提交；
  6. 工作树干净；
  7. 区间内每个提交的作者等于线程身份的提交身份；
  8. 运行检查命令（600 秒）；
  9. `level=pr` 时再运行测试命令（3600 秒）。

  任一步失败都返回详情，不产生通过记录。
- **R21** 外部 PR 评审线程是只读工作区：出站代理拒绝它的推送、创建 PR、合并、改 ref 的 API。规则选项「只发 COMMENT」开启时（预设默认开启），`APPROVE` 和 `REQUEST_CHANGES` 两种 review 被拒绝。
- **R22** 发布哨兵（`workflow_run_completed` 规则，默认关闭）：
  - 只处理状态为 `completed`、head 提交信息以规则前缀开头的 run（默认前缀 `chore: bump version to `）；
  - 远端 tag `v<version>` 不再指向该 run 的 SHA 时记为 `stale`；
  - 阻塞结论：`failure`、`timed_out`、`startup_failure`、`action_required`；
  - 状态：`awaiting_ci`、`fixing`、`green`、`failed`、`superseded`；
  - 遇到阻塞结论时开启或续跑同一线程；全部 run 完成且无阻塞、并且存在非草稿 Release 时置为 `green`；超过最大轮数（5）或默认分支被别人推进时置为 `failed`；同仓库出现新版本时，旧版本置为 `superseded`；
  - 终态不在 GitHub 上发言；
  - 修复推送用 `git push --atomic --force-with-lease=refs/tags/v<version>:<事件中的 tag oid> origin HEAD:refs/heads/<默认分支> refs/tags/v<version>`：分支只能快进，tag 只在仍指向旧值时才移动。
- **R23** 审计：出站代理为每个 orb 的 GitHub 写操作记一行，包括：
  - 非 GET 的 REST 请求；
  - 解析出的 GraphQL mutation（query 不记）；
  - `git-receive-pack`（每个 ref 一行）。

  每行字段：时间、线程、身份、方法、路径模板或 mutation 名、仓库（取路径，或响应中的 `url`/`resourcePath`，取不到时留空并把 node id 记入 `op`）、状态码、门禁结论、正文 SHA-256。保留 30 天。设置页、API 和 CLI 都可以按线程或仓库查询。
- **R24** CLI `oryn github`：
  - `triage <owner>/<repo>#<n> [--rule]`：合成 `issues.opened`，打印线程 id；
  - `replay <guid>`：重放 7 天内已结束的投递，载荷保留到清理时；
  - `status`：显示 issue 线程状态、发布状态、hook 状态；
  - `cleanup <owner>/<repo>#<n>`：置为 `abandoned` 并冻结 orb；
  - `audit [--thread] [--repo]`。
- **R25** 任务时限与完成提醒：
  - 规则可配置任务时限，存为 `thread_issues.deadline_at`；到点时中止当前回合并发通知；
  - 规则开启「完成提醒」后，agent 回合结束时，若状态仍是 `new`、`reproducing` 或 `fixing`，且分类为 `bug`、`documentation` 或尚未分类，最多补发 2 次提醒。
- **R26** 预设：规则编辑器提供「robomp 分诊」「robomp PR 评审」「robomp 发布哨兵」三个预设。提示词移植自 robomp 的 `prompts/`，以 MIT 许可注明出处。行内评审评论使用 `gh api repos/{o}/{r}/pulls/{n}/reviews` 一次提交。

### 非目标

- GitHub Enterprise Server 的专门验收。
- GitLab。
- 回写 Check run。
- `delete_repo`、`admin:org`。
- 人工确认。
- 出站代理按仓库收窄 GitHub 访问。
- robomp 的本地 issue 索引、HMAC 旁车、原生构建缓存。
- **包仓库推送**：ghcr.io、npm.pkg.github.com 等需要以真实主机名做 TLS 的包仓库，经占位令牌注入的推送不在本期范围。`write:packages` 只服务于经 `gh api` 调用的 Packages REST API。deploy.md 中写明这一点。
- gist 的 git 克隆（`gist.github.com`）。`gh gist` 走 API，可用。

### 假设（每条注明失效时受影响的步骤）

- **A1** ✅ 已实测成立（附录 B-5）：gh 发起写操作时请求里的正文，与 webhook 载荷中的 `body` 逐字节相同，CRLF 和首尾空白都原样保留。2.3 仍按规范化（统一为 `\n` 换行、去掉首尾空白）后的结果比较，作为保险。
  - 失效影响：步骤 2.3 的正文哈希改为只比较前 512 字符，并扩大时间窗。
- **A2** ✅ 已实测成立（附录 B-7）：`POST git-receive-pack` 返回 200，并带 side-band 包裹的 `ng <ref> <原因>` 时，git 显示 `! [remote rejected] HEAD -> feature (<原因>)`。
  - 失效影响：5.4 改为在 `info/refs?service=git-receive-pack` 阶段预判（只能用于 `ReviewOnly`），推送门禁的拒绝原因改由 `github_prepare` 的工具输出提示。
- **A3** ⚠️ 未实测（手头没有开启 OAuth App 限制的组织）：组织访问限制下，非成员在公开仓库发评论或开 PR 不受影响。
  - 失效影响：R4 的提示文案和 deploy.md 的风险说明。

## 全局约束

- **语言与提交**：
  - 代码注释、文档、提交信息用简体中文；
  - 每个步骤一个原子提交，格式 `<type>(<scope>): <gitmoji> <subject>`，带 `Co-authored-by: Wine Fox <fox@ling.plus>`；
  - 用 eval 的 `commit` 命名空间提交；
  - 子 agent 同时不超过 5 个；
  - 每个阶段完成后用 `record-agent-log` 记录。
- **并行改动**：工作区中有他人的改动。每步动手前重读目标文件，按符号定位，不回退别人的改动。
- **每步提交前必须通过** `go build ./... && go vet ./...`。改动 `web/` 或 OpenAPI 的步骤，还要通过 `cd web && bun run check`。新的 OpenAPI 操作在实现之前由 `apigen.Unimplemented` 返回 501。
- **依赖方向**：
  - 包依赖：`github → threads → orbs`、`api → github`；`orbs`、`threads`、`egress` 不导入 `github`。
  - 不引入 GitHub SDK。
  - 新增 `github.com/vektah/gqlparser/v2`，只用于解析 GraphQL 文档、展开 fragment、按 `operationName` 选取 operation。
- **请求头与基址**：发往 GitHub 的请求带 `Accept: application/vnd.github+json`、`X-GitHub-Api-Version: 2022-11-28`、`User-Agent: oryn/<version.Version>`。基址取 `cfg.GitHub.APIURL` / `WebURL`。
- **加密存储**：敏感值用 `secrets.Box.Seal` 加密，AAD 分别为：
  - `github/oauth/client-secret`
  - `github/identity/<slot>/access`
  - `github/hook/<repo_id>/secret`
- **固定字面量**：
  - `oauthScopes = "repo workflow read:org admin:repo_hook gist write:packages user:email"`。
  - `hookEvents = issues, issue_comment, pull_request, pull_request_review, pull_request_review_comment, check_suite, status, workflow_run, repository`。
  - 仓库 hook 的创建参数：`{name:"web", active:true, events:hookEvents, config:{url:<webhookBase>/api/v1/public/github/webhook, content_type:"json", secret, insecure_ssl:"0"}}`。
  - 投递来源：`webhook`、`poll`、`rest`。
  - hook 状态：`active`、`no_admin`、`org_restricted`、`sso_required`、`error`。
  - 驱动者档位：见 R13。
  - issue 状态：见 R15。
  - 发布状态：见 R22。
  - 门禁级别：`push`、`pr`。
  - 新增 ctl 方法：`github.call`、`github.prepare.report`，均为 orbd → 控制面；runner link 拒绝这两个方法。
  - 发布哨兵自动消息的首行：`[Oryn release sentinel on <owner>/<repo> v<version> — started automatically, not typed by the owner]`。
- **常量**（`internal/github/github.go`）：

  | 常量 | 值 |
  |---|---|
  | `restPollInterval` | 60s |
  | `hookPollInterval` | 30s；最近 10 分钟收到过 webhook 投递时放宽到 5 分钟 |
  | `hookSyncInterval` | 10m |
  | `scanInterval` | 60s（自动关闭、任务时限共用） |
  | `defaultAutocloseHours` | 4 |
  | `selfWriteWindow` | 120s（无正文的动作） |
  | `selfWriteRetention` | 7 * 24h |
  | `auditRetention` | 30 * 24h |
  | `anyoneMaxPerHour` | 3 |
  | `contributorMaxPerHour` | 10 |
  | `maxReleaseRounds` | 5 |
  | `maxCompletionReminders` | 2 |
  | `rateLimitFloor` | 500（剩余额度低于它时，轮询间隔加倍） |

  删除 `installTokenTTL`、`scopedTokenTTL`、`refreshSkew`。

  出站代理的常量：`ghWriteBodyMax = 1 << 20`、`receivePackHeadMax = 64 << 10`。
- **「已绑定」的定义**：`projects.github_repo_id IS NOT NULL`。
- **验收命令**：在仓库根目录执行。`make check-gen` 在提交之后运行（它是 `git diff --exit-code`）。

## 方案与被否决的选项

- **凭据**：采用 OAuth 用户令牌 + 出站代理注入 + 不按仓库拦截（Q1）。
  - 否决：受控动作白名单（主人要求支持全部操作）。
  - 否决：出站代理按仓库收窄（GraphQL 要反查 node id，容易误拦；主人未选）。
  - 否决：`mode=env` 全局令牌（令牌会进入所有 orb）。
- **防回流**：采用请求侧预登记 `(槽位, 动作类别, 目标, 正文哈希)`，REST 响应的 id 作为补充键。
  - 否决：正文标记（主人不要）。
  - 否决：只从响应里取 id（gh 的 mutation 不选 id；响应可能是 gzip；webhook 可能比响应先到）。
  - 否决：按登录名一刀切（会吞掉我本人的评论）。
- **事件**：采用「admin 仓库建 hook，并轮询 hook 的投递日志；其余用仓库级与对象级 REST 轮询」。
  - 否决：`/repos/{o}/{r}/events`（官方说明延迟可达 30 秒到 6 小时）。
  - 否决：`/notifications`（需要额外 scope，语义也不同）。
- **门禁**：采用「出站代理强制执行 + orbd 执行检查 + 覆盖表 + 解析失败即拒绝」。
  - 否决：只写进提示词。
  - 否决：在控制面执行检查（代码在 orb 里）。
- **robomp 能力的落地方式**：通用化为触发规则、omp 工具、预设和后台任务。凡是 `gh` 能直接完成的写操作，不另做工具。

**回滚点**：步骤 1.5（破坏性迁移）之前都可以回退到 M14。1.5 之后只能向前。从阶段 2 起，每个阶段结束时系统都可用。

## 阶段 0：契约与风险前置（只加不删）

### 步骤 0.1：计划入库
Depends on：无。
Files：`docs/plan-m15.md`。
Accept：`git log -1 --name-only` 的输出包含 `docs/plan-m15.md`。

### 步骤 0.2：探查（不产出功能）
Depends on：无。
Do：

1. 按 orb 镜像里 gh 的版本固定 cli/cli 的 commit。从 `api/queries_pr.go`、`queries_pr_review.go`、`queries_comments.go`、`queries_issue.go` 摘出各子命令的 mutation 名、input 字段和 selection，写入附录 A。
2. 用本机 gh 的 OAuth 令牌（GitHub CLI 是一个 OAuth App，带 `repo` scope）对真实 github.com 做一次性探查，**需要主人先指定测试仓库**。探查清单：
   - 仓库 hook 的增删查，投递列表与详情，`X-GitHub-Hook-ID`、`X-GitHub-Hook-Installation-Target-ID`，投递列表是否支持 ETag/304；
   - `X-OAuth-Scopes` 的格式；
   - 没有 push 权限时，`collaborators/{login}/permission` 返回的状态码与 message；
   - `/gh/` 路由上游是否返回 gzip；
   - 评论正文在请求与 webhook 载荷里是否一致（A1）；
   - `POST git-receive-pack` 返回 200 + `ng` 时，客户端显示什么（A2，用本地 bare 仓库加最小 HTTP 服务即可测，不需要 GitHub）；
   - R22 的原子推送命令：分支已被别人推进时是否整体失败（本地 bare 仓库即可测）。

   结论写进附录 B，并更新 A1–A3。
Accept：附录 A 不少于 10 行，附录 B 逐项写明「成立 / 不成立 + 证据」。

### 步骤 0.3：加法迁移 `0033_github_oauth.sql`
Depends on：0.2（附录 A 决定 self-write 键的形态）。
Files：新建 `internal/store/migrations/0033_github_oauth.sql`（先用 `ls … | tail -1` 确认编号）；改 `internal/store/queries/github.sql`；改 `internal/store/github_migration_test.go`。
Do：

**Up**：

- 新建表：
  - `github_oauth_app(id INTEGER PRIMARY KEY CHECK(id=1), client_id, client_secret_enc, webhook_base, created_at)`。
  - `github_hooks(repo_id PK, full_name, hook_id, secret_enc, status, error, delivery_cursor, deliveries_etag, last_delivery_at, last_delivery_src, last_webhook_at, updated_at)`。
  - `github_repo_watches(repo_id PK, full_name, etag_issues, etag_comments, etag_pulls, etag_runs, since_at, updated_at)`。
  - `github_watches(kind CHECK(pull|issue), repo_id, full_name, number, thread_id, slot, etag_issue, etag_comments, etag_reviews, etag_review_comments, etag_checks, last_seen_at, done_at, PK(kind, full_name, number))`。
  - `github_self_writes(id INTEGER PK, slot, category, target, body_sha256, created_at)`，加索引 `(target, category)`；补充键表 `github_self_keys(key PK, created_at)`。
  - `github_audit(id INTEGER PK, at, thread_id, orb_id, slot, method, op, repo, status, gate, body_sha256)`，加索引 `(thread_id, at)`、`(repo, at)`。
  - `github_prepare(sha, orb_id, level CHECK(push|pr), skipped, reason, at, PK(sha, orb_id, level))`。
  - `github_autoclose(repo_id, number, thread_id, comment_id, author, due_at, state CHECK(pending|claimed|done|cancelled), outcome, PK(repo_id, number))`。
  - `github_releases(repo_id, version, sha, tag_oid, thread_id, state, rounds, updated_at, PK(repo_id, version))`。
  - `github_sender_runs(rule_id, sender, at)`，加索引 `(rule_id, sender, at)`。
- 新增列：
  - `github_identities.scopes TEXT NOT NULL DEFAULT ''`；
  - `projects.github_bind_status TEXT`（`not_found|org_restricted|sso_required|NULL`）、`projects.github_gates TEXT`（JSON：`{push, pr, fixCmd, checkCmd, testCmd, prHeadings[], requireClosingRef}`）；
  - `thread_issues` 增加：`state DEFAULT 'new'`、`classification`、`impl_authorized BOOLEAN DEFAULT 0`、`review_only BOOLEAN DEFAULT 0`、`rule_id`、`deadline_at`、`reminders INTEGER DEFAULT 0`；
  - `thread_pulls.created_via_egress BOOLEAN DEFAULT 0`。
- 重建 CHECK（照 `0018_analytics.sql` 的 `_keep` 搬表方式，不用 `RENAME TO`）：
  - `github_deliveries.source` 增加 `rest`；
  - `github_triggers.event` 增加三个新事件，同时加列：`drivers DEFAULT 'writers_and_author'`、`maintainers`、`reviewer_bots`、`models`、`thinking`、`impl_auth`、`comment_only_reviews`、`autoclose_hours`、`max_duration_sec`、`completion_reminders`、`release_prefix`。
- `owner.github_automation_identity`：先 `DROP COLUMN`，再 `ADD COLUMN … DEFAULT 'agent' CHECK(IN('user','agent'))`。
- **本步不删除 App 的表和列。**

**Down**：

- 逆序删除新表、新列；
- 把两张重建 CHECK 的表恢复成 0028 的原形（先删掉 `rest` 来源的行、新事件的行）；
- `owner` 列恢复为原来的默认值。

**查询**：

- `FinishGithubDelivery` 不再把 `payload` 置为 NULL（R24 的 replay 需要载荷）；
- 为新表补齐增删改查。

Accept：`make gen && go build ./... && go vet ./... && go test ./internal/store/... -run 'Migration|Github'` 输出 `ok`，迁移用例包含 `DownTo(27)` 再 `Up`。

### 步骤 0.4：加法 OpenAPI 与 ctl 契约
Depends on：0.3。
Files：`schema/openapi.yaml`；`make gen` 的生成物。
Do：

- **新增 REST 操作**：
  - `PUT|DELETE /api/v1/github/oauth-app`（请求体 `GitHubOAuthAppInput{clientId, clientSecret, webhookBase?}`）；
  - `GET /api/v1/github/hooks`；
  - `GET /api/v1/github/audit?thread=&repo=&before=&limit≤200`；
  - `GET /api/v1/github/tasks`；
  - `POST /api/v1/github/triage`；
  - `POST /api/v1/github/deliveries/{guid}/replay`；
  - `POST /api/v1/github/tasks/cleanup`；
  - `GET /api/v1/github/presets`。
- **新增字段**：
  - `GitHubStatus` 加 `oauthApp?`、`webhookBase`；旧的 `app`、`installations` 先保留；
  - `GitHubIdentity` 加 `scopes`、`missingScopes`；
  - `ProjectGitHub` 加 `bindStatus`；
  - `GitHubTrigger*` 加 0.3 的新字段；
  - `Project` / `ProjectPatch` 加 `githubGates`；
  - `Thread` 加 `githubTask?{repo, number, state}`；
  - `GitHubTriggerEvent` 加新事件值。
- **ctl**：
  - `LinkCtlMethod` 加 `github.call`、`github.prepare.report`，以及它们的 Params/Result；
  - `LinkCtlSetupRunParams` 加 `githubGates?`；
  - `LinkCtlSetupGh` 加 `uploads: boolean`。
- **本步不删除任何操作。** 新操作由 `apigen.Unimplemented` 返回 501。

Accept：`make gen && make lint-spec && go build ./... && go vet ./... && (cd web && bun run check)` 全部退出 0。

## 阶段 1：OAuth 核心、事件入口、清除 App

### 步骤 1.1：OAuth App 凭据、授权流、`tokenFor`
Depends on：0.4。
Files：新建 `internal/github/oauthapp.go`；改 `identity.go`、`client.go`、`api/github.go`（`PUT|DELETE oauth-app`）；测试 `oauthapp_test.go`、`identity_test.go`；`githubtest/server.go`（`/login/oauth/access_token` 回显 scope，`/user` 返回 `X-OAuth-Scopes`）。
Produces：

- `(*Service).SetOAuthApp(ctx, clientID, secret, webhookBase string) error`
- `(*Service).DeleteOAuthApp(ctx) error`：先调用 `dropAllHooks`（1.2 提供，本步先留空实现），再清空身份
- `(*Service).oauthClient(ctx) (id, secret string, err error)`：未配置时返回 `github_not_configured`（409）
- `(*Service).tokenFor(ctx, slot string) (string, error)`：直接解密
- `(*Service).call`：遇到 401 时 `markReauth(slot)`，遇到 403 时按 R4 分类为 `org_restricted` / `sso_required`，返回类型化错误 `*ghError{Kind}`

Do：

- `BeginAuthorize` 在请求里加上 `scope=<oauthScopes>`，client 取自 `oauthClient`；
- `FinishAuthorize` 写入 `scopes`；
- 本步保留 App 路径，`oauthClient` 优先读 `github_oauth_app`；
- 删除刷新链路（`refreshSkew`、刷新调用）。

Accept：`go build ./... && go test ./internal/github/... ./internal/api/... -run 'OAuthApp|Authorize|Identity'` 输出 `ok`。新增用例断言：授权 URL 的 `scope` 与常量一致；缺少 scope 时 `missingScopes` 非空；401 后槽位为 `reauth`；带 `X-GitHub-SSO` 的 403 被分类为 `sso_required`。

### 步骤 1.2：仓库 hook 管理
Depends on：1.1。
Files：新建 `internal/github/hooks.go`、`hooks_test.go`；改 `bind.go`（绑定和解绑时调用）、`events.go`（`Run` 中加入 `hookSyncInterval` 循环）、`oauthapp.go`（`DeleteOAuthApp` 调用 `dropAllHooks`）、`identity.go`（`Disconnect(user)` 先调用 `dropAllHooks`）；在 `githubtest` 中加 hooks 路由。
Produces：

- `ensureHook(ctx, repoID int64, full string) error`
- `dropHook(ctx, repoID int64) error`：只有在没有其他未归档项目绑定该 `repo_id` 时才删
- `dropAllHooks(ctx) error`
- `isOrynHook(repo string, hookID int64) bool`

Do：

- 判定条件：`GET /repos/{full}` 的 `permissions.admin` 为真才创建 hook，否则记为 `no_admin`；403 按 R4 分类处理。
- 创建参数使用全局约束里的固定字面量。已存在同 URL 的 hook 时，`PATCH` 改写 config。
- 每轮同步都重写 config（url、secret、content_type），所以 webhook 基址变更后会全量更新。

Accept：`go build ./... && go test ./internal/github/... -run Hook` 输出 `ok`。覆盖：创建、幂等、`no_admin`、`org_restricted`、多项目共享时解绑不删除、删除 OAuth 配置时全部删除、基址变更时改写。

### 步骤 1.3：webhook 验签与投递日志轮询
Depends on：1.2。
Files：`events.go`（`HandleWebhook`、`ingest`）、重写 `poll.go`；测试 `events_test.go`、`poll_test.go`、`api/github_events_test.go`。
Do：

- **验签**：`HandleWebhook` 按 `X-GitHub-Hook-ID` 查到 `github_hooks` 行，用该行的 secret 验 `X-Hub-Signature-256`，再检查 `X-GitHub-Hook-Installation-Target-ID` 等于该行的 `repo_id`。不满足任一条件返回 401。验签成功后写 `last_webhook_at`。
- **轮询**：`poll.go` 按 `hookPollInterval` 对每个 `active` 的 hook 读取 `GET /repos/{full}/hooks/{id}/deliveries?per_page=100`：
  - 请求带 `If-None-Match: <deliveries_etag>`。304 不消耗额度。该接口单独计数，资源名 `webhook_deliveries`，上限 500 次/小时（附录 B-3）。
  - 只为 `delivery_cursor` 之后、且 guid 不在 `github_deliveries` 里的投递取详情。
  - 第一次只建立基线；结果进入 `ingest(…, "poll")`。
  - `X-RateLimit-Remaining` 低于 50 时，暂停到 `X-RateLimit-Reset`。
- `TouchGithubDelivery` 改为写 `github_hooks`。
- 本步同时移除 App 的验签和 App 投递轮询（新入口已经可用）。

Accept：`go build ./... && go test ./internal/github/... ./internal/api/... -run 'Webhook|Poll|Events'` 输出 `ok`。覆盖：hook id 错误返回 401、target id 不符返回 401、轮询补漏、webhook 与轮询重复时只处理一次。

### 步骤 1.4：令牌调用方迁移，删除 App 代码
Depends on：1.3。
Files：`bind.go`、`gate.go`、`checks.go`、`refs.go`、`pulls.go`、`triggers.go`、`repos.go`、`client.go`、`orbs.go`、`events.go`；删除 `installations.go`、`app.go`；以及对应测试；`githubtest` 删除 App 路由，增加 `/user/repos`。
Do：

- 安装令牌的调用方统一改为 `tokenFor`：交互路径用 `user`，自动化路径用规则对应的槽位。
- `bind`：
  - `GET /repos/{full}` 成功时写入 `github_repo_id`，并清空 `github_bind_status`；
  - 返回 404 或 R4 的 403 时，清空 repo id 并写入状态；
  - 成功后调用 `ensureHook`。
- `canDrive` 改用新签名 `canDrive(ctx, repo, sender, author, assoc, drivers string) (bool, string)`：
  - 先看 `author_association`；
  - 只有在我的账号 `permissions.push` 为真时，才调权限接口，接口返回 403 视为非写权限者，结果缓存 `permTTL`；
  - 本步内 `drivers` 恒为 `writers`，4.1 再开放档位。
- `ListRepos` 改用 `GET /user/repos?affiliation=owner,collaborator,organization_member&sort=pushed&per_page=100`。
- 删除：`appJWT`、`appAuth`、`installToken`、`issueScoped`、`scoped` 缓存，以及 `installation*` 和 `github_app_authorization` 两类事件的处理器。
- `orbManaged` 与「已绑定」的判定改为只看 `github_repo_id`；6 处 SQL 条件同步修改。

Accept：`go build ./... && go vet ./... && go test ./internal/github/... ./internal/api/... ./internal/orbs/...` 输出 `ok`；`grep -rn "installToken\|token/scoped\|appJWT\|GetGithubApp" internal --include=*.go` 无输出。

### 步骤 1.5：破坏性迁移 `0034_github_drop_app.sql`
Depends on：1.4。
Files：新建迁移；`queries/github.sql`（删除 App、安装相关查询）；`github_migration_test.go`。
Do：

- **Up**：
  - `DROP TABLE github_installations`、`DROP TABLE github_app`；
  - `DELETE FROM github_identities`（R3）；
  - 删除 `github_identities` 的 `access_expires_at`、`refresh_token_enc`、`refresh_expires_at`；
  - 删除 `projects.github_installation_id`、`projects.github_owner_id`；
  - 重建 `github_oauth_states`：去掉 manifest 相关列，CHECK 只保留 `identity`。
- **Down**：
  - 以空表重建 `github_app`、`github_installations`；
  - 加回 `projects` 的两列和 `github_identities` 的三列；
  - `github_oauth_states` 恢复原形；
  - 在注释中写明数据不可恢复。

Accept：`make gen && go build ./... && go test ./internal/store/... ./internal/github/...` 输出 `ok`，`DownTo(27)` 再 `Up` 的用例通过。

### 步骤 1.6：身份规则
Depends on：1.4。
Files：`internal/github/identity.go`（`Disconnect` 不再改写自动化身份；`SetAutomationIdentity` 去掉 `github_agent_required` 校验）、`internal/threads/threads.go`（`AutomationIdentity` 的选择）、`internal/schedule/loop.go`；以及对应测试。
Produces：`github.(*Service).ResolveAutomationSlot(ctx, untrusted bool) (slot string, err error)`。

- `untrusted=true`，且 Agent 槽位不是 `ok` 时，返回 `errAgentRequired`；
- 否则 Agent 不可用就返回 `user`，并附带回退提示。

Accept：`go build ./... && go test ./internal/threads/... ./internal/schedule/... ./internal/github/... -run 'Identity|Automation'` 输出 `ok`。覆盖：回退、`untrusted` 时拒绝、断开 Agent 后设置值不变。

## 阶段 2：orb 凭据、去标记、防回流、审计

### 步骤 2.1：所有工作 orb 下发凭据；`uploads.github.com`
Depends on：1.4。
Files：

- `internal/orbs/setup.go`：`DefaultSetup.plan` 的 GitHub 分支；
- `internal/github/orbs.go`：`orbManaged`、`OrbSetup`、`OrbToken`；
- `internal/orbdrt/gh.go`：`ghProxy` 把 `uploads.github.com` 也转发到 `/gh/uploads.github.com/…`；
- `internal/egress/egress.go`：`gh` 分支允许 `api.github.com`、`uploads.github.com`，upload 主机的上游为 `https://uploads.github.com`，流式转发；git 分支在 `GitRepos` 检查之前，`host=="github.com" && pol.GitHub != nil` 时放行读写；
- 测试：`setup_git_test.go`、`orbs_test.go`、`gh_test.go`、`egress_test.go`。

Do：

- `orbManaged` 的条件：不是 runner、不是协调宿主、该槽位为 `ok`。
- 所有工作 orb 都下发 `Gh{Login, uploads:true}`、`Egress.GitHub`，以及覆盖整个 `github.com` 主机的 `insteadOf`。
- 有项目时，`GitRepos[projectRepo.host]` 照旧写入（不改 `setup.go` 现有的这几行）。

Accept：`go build ./... && go test ./internal/orbs/... ./internal/github/... ./internal/orbdrt/... ./internal/egress/... -run 'Setup|Orb|Gh|Upload|Git'` 输出 `ok`。新增用例：

- 无项目 orb 的 plan 含 `Gh` 与整主机 `insteadOf`；
- runner 和协调宿主的 plan 不含；
- 有 github.com 项目的 orb 不能读其它 PAT 主机上的非项目仓库；
- `uploads.github.com` 的请求被注入令牌。

### 步骤 2.2：去掉标记
Depends on：2.1。
Files：`internal/egress/egress.go`、`egress_test.go`、`internal/github/gate.go`、`internal/github/triggers.go`。
Do：

- 删除 `AgentMarker`、`markRequest`、`markAgentBodies`、`markField`、`setRaw`、`encodeJSON`、`markBodyMax`、`target.mark`；
- `isSelf` 暂时只保留按 Agent 登录名判断（2.3 补全）；
- `announce` 回帖不再带标记，并通过 2.3 的预登记接口登记（本步先把回帖返回的评论 id 写进 `github_self_keys`）。

Accept：`go build ./... && go test ./internal/egress/... ./internal/github/...` 输出 `ok`；`grep -rn "AgentMarker\|oryn-agent" internal web/src` 无输出。

### 步骤 2.3：写操作识别、防回流、审计
Depends on：2.2、0.2（附录 A、B）。
Files：

- 新建 `internal/egress/ghwrite.go`、`ghwrite_test.go`：识别器，输出 `{category, target, bodySHA, op, repo}`；GraphQL 用 gqlparser 展开 fragment、按 `operationName` 选 operation、代入变量；
- 改 `egress.go`：
  - `/gh/` 的 Rewrite 删除 `Accept-Encoding`；
  - 写请求在转发前读入请求体，上限 `ghWriteBodyMax`；超限时，门禁关闭则只审计，门禁开启则拒绝；
  - 读取后用 `bytes.Reader` 复原请求体；
  - 转发前调用 `Resolver.GitHubWriteBegin`，响应后调用 `GitHubWriteEnd`；
  - git 分支对 `git-receive-pack` 解析 pkt-line 命令段（规则同 5.4），每个 ref 记一行审计；
- `internal/orbs/egress.go`：实现新增方法，转交给 `orbs.GitHub`；
- 新建 `internal/github/selfwrite.go`；
- 改 `gate.go`；
- `githubtest/issues.go`：补 `createPullRequest`、`addComment`、`addPullRequestReview`、`submitPullRequestReview`、`mergePullRequest` 这些 mutation，以及 `POST /repos/{o}/{r}/forks`。

Produces：

- `egress.Resolver` 增加 `GitHubWriteBegin(w GitHubWrite) (gate string)`、`GitHubWriteEnd(w GitHubWrite, status int, respKeys []string)`；
- `type GitHubWrite struct{ Method, Category, Target, Op, Repo, BodySHA256 string; Refs []RefUpdate }`；
- `github.(*Service).isSelfEvent(ctx, ev selfProbe) bool`。

Do：

- **预登记**：写入 `github_self_writes(slot, category, target, body_sha256)`。target 的取法：
  - REST 取规范化路径；
  - GraphQL 取 input 中的 `subjectId`、`pullRequestId`、`issueId`、`repositoryId`。
- **补充键**：只读 REST 写操作的 JSON 响应（不读 GraphQL 响应），取 `id`、`node_id`，以及 `html_url` 片段里的 `#issuecomment-`、`#pullrequestreview-`、`#discussion_r`。
- **`isSelf` 的判定**：满足以下任一即为自身写入：
  - sender 是 Agent 槽位的登录名；
  - 命中补充键；
  - sender 等于某个槽位的登录名，目标（事件里的 `issue.node_id`、`pull_request.node_id`，或路径）匹配，并且正文哈希相等；无正文的动作在 `selfWriteWindow` 内匹配。

  sender 是槽位登录名但还没命中时，延后 5 秒复查一次。
- **审计分类**：GraphQL 的 query 不审计；`repo` 字段的取值按 R23。
- **清理**：每小时清理一次，self-write 保留 `selfWriteRetention`，审计保留 `auditRetention`。

Accept：`go build ./... && go test ./internal/egress/... -run 'GitHubWrite|ReceivePack'` 输出 `ok`，覆盖：

- REST 评论；
- GraphQL `addComment`，包括 fragment、别名、多个 operation；
- `createPullRequest`、`addPullRequestReview`；
- gzip 上游；
- 超限请求体。

`go test ./internal/github/... -run SelfWrite` 输出 `ok`，覆盖：

- webhook 先于响应到达时仍判为自身写入；
- 主人本人手写的同目标、不同正文的评论正常回流。

## 阶段 3：事件补全

### 步骤 3.1：REST 轮询（仓库级 + 对象级）
Depends on：2.3。
Files：

- 新建 `internal/github/watch.go`、`watch_test.go`；
- 改 `listen.go`、`pulls.go`、`checks.go`：准入条件改为「仓库已绑定，或 `(repo_id, number)` 有未取消的 `thread_pulls`/`thread_issues`」。无项目线程的三类回流开关都取默认开启，令牌取线程 orb 所用的槽位；
- 改 `triggers.go`：仓库级 watch 的登记与注销跟随规则的启停。

Do：

- **仓库级 watch**：已绑定、hook 不是 `active`、且有启用规则的仓库，按 `restPollInterval` 轮询：
  - `issues?since=&state=all&sort=updated`
  - `issues/comments?since=`
  - `pulls?state=open&sort=updated&direction=desc`
  - `actions/runs?event=push&status=completed`
- **对象级 watch**：线程关联的对象，所在仓库 hook 不是 `active` 时，轮询：
  - 本体
  - `issues/{n}/comments?since=`
  - `pulls/{n}/reviews`
  - `pulls/{n}/comments?since=`
  - head 的 `check-runs` 和 `status`
- 请求都带 `If-None-Match`。
- **合成事件**：从变化中合成内部事件，guid 为 `rest:<kind>:<full>:<对象 id>:<updated_at>`，进入 `ingest(…, "rest")`。合成事件清单写进本文附录 C，列出事件、action、处理器读取的字段、sender 的来源（`user` 字段或 `issues/{n}/events`）。
- **退场与限流**：线程归档，或对象进入终态满 24 小时后，watch 退场；剩余额度低于 `rateLimitFloor` 时，轮询间隔加倍。

Accept：`go build ./... && go test ./internal/github/... -run Watch` 输出 `ok`，覆盖：

- 304 不产生事件；
- 新评论回流一次；
- 上游 PR 合并后设置 `shippedAt`；
- hook 为 `active` 时不轮询；
- 无项目线程的上游 PR 能回流。

### 步骤 3.2：跨仓库 PR 关联
Depends on：3.1。
Files：`pulls.go`、`pulls_test.go`。
Do：

- `GitHubWriteEnd` 识别到成功的 PR 创建（REST 或 `createPullRequest`）时，补一次 `GET /repos/{o}/{r}/pulls/{n}`（REST 响应已带 number 时直接用；GraphQL 只返回 URL，从 URL 解析 owner/repo/number），取到 head sha 后：
  - 建立 `thread_pulls`，`created_via_egress=1`；
  - 登记对象级 watch。
- `linkSampled` 对不同仓库的 PR，只在 PR 的 `user.login` 是已连接身份之一时才关联。

Accept：`go build ./... && go test ./internal/github/... -run 'Pull'` 输出 `ok`，新增两类用例：出站代理创建的跨仓库 PR 被关联；陌生人从 fork 发起、head 相同的 PR 不被关联。

## 阶段 4：触发规则扩展与 issue 状态

### 步骤 4.1：新事件、驱动者档位、按人限流、评审机器人
Depends on：1.6、3.1。
Files：`triggers.go`、`gate.go`、`listen.go`，以及对应测试。
Do：

- 落实 R12、R13、Q6：
  - `pull_request_opened` 规则建立的线程设置 `review_only=1`；
  - `anyone` 档规则和 `pull_request_opened` 规则调用 `ResolveAutomationSlot(untrusted=true)`，返回 `errAgentRequired` 时记 `ignored:agent_required` 并通知；
  - 跟进评论按线程来源规则的 `drivers` 判定；
  - `workflow_run` 交给 6.1，本步只做路由。
- `issue_reopened` 与 `issue_opened` 共用同一个限流预算。
- `triggerPrompt` 新增模板变量 `{{autoclose_hours}}`，取值为规则的 `autoclose_hours`，未配置时取 `defaultAutocloseHours`。

Accept：`go build ./... && go test ./internal/github/... -run 'Trigger|Gate|Listen'` 输出 `ok`。覆盖：三档 × 发送者类别（OWNER / CONTRIBUTOR / NONE / 作者 / 评审 bot）的矩阵，`anyone` 档第 4 次被拒，Agent 不可用时 `untrusted` 规则被忽略。

### 步骤 4.2：模型池、思考级别、指令
Depends on：4.1。
Files：新建 `internal/github/pragmas.go`、`pragmas_test.go`；改 `triggers.go`、`listen.go`、`github.go`（`Threads` 接口）。
Produces：`github.Threads` 接口新增 `SetModel(ctx, threadID string, m apigen.ModelRef, thinking string) error`，由 `threads` 用现有的 `set_model` 帧实现。模型目录的只读查询以 `ModelCatalog` 接口注入，`github` 不导入 `modelcat`。
Do：落实 R14。

Accept：`go build ./... && go test ./internal/github/... -run Pragma` 输出 `ok`。

### 步骤 4.3：状态机、终态应答、实现授权、时限与提醒
Depends on：4.1。
Files：新建 `internal/github/tasks.go`、`tasks_test.go`；改 `triggers.go`、`listen.go`、`pulls.go`、`github.go`（`Threads` 接口新增 `Abort(ctx, threadID) error`，用 `abort` 帧实现；新增回合结束回调 `OnTurnEnded(threadID string)`，由 `threads` 的 `Observer.TurnEnded` 驱动；`Orbs` 接口新增 `Freeze(ctx, orbID) error`，用 `orbs.Pause(ctx, id, freeze)` 实现）。
Produces：

- `(*Service).SetTaskState(ctx, threadID, state string, note string) error`
- `(*Service).CleanupTask(ctx, repo string, number int) error`

Do：

- 落实 R15、R16、R25；
- 终态回帖的正文移植自 robomp 的 `finalized_issue_comment.md`；
- 时限和提醒放在同一个 `scanInterval` 循环里扫描；
- 提醒的发送条件：状态为 `new`、`reproducing` 或 `fixing`，且分类为 `bug`、`documentation` 或尚未分类。

Accept：`go build ./... && go test ./internal/github/... -run 'Task|Finalized|ImplAuth|Deadline|Reminder'` 输出 `ok`。

## 阶段 5：orb 工具与门禁

### 步骤 5.1：ctl 方法与 orbd 接线
Depends on：0.4、4.3。
Files：

- 新建 `internal/orbdrt/github.go`；
- 改 `localapi.go`：本机路由 `/github/call`、`/github/prepare`；
- 改 `internal/orbs/service.go`：分发 ctl；
- 改 `internal/orbs/github.go`：接口加 `Call`、`PrepareReport`；
- 新建 `internal/github/tools.go`：`Call` 先分发到已实现的工具，未实现的返回 `not_implemented`；`PrepareReport` 完整实现，写 `github_prepare`；
- 改 runner link 的允许清单：不加入这两个方法。

Produces：

- `orbs.GitHub.Call(ctx, orbID, threadID string, p apigen.LinkCtlGithubCallParams) (apigen.LinkCtlGithubCallResult, error)`
- `PrepareReport(ctx, orbID string, p apigen.LinkCtlGithubPrepareReportParams) error`

Accept：`go build ./... && go test ./internal/orbdrt/... ./internal/orbs/... ./internal/github/... -run 'GithubCall|PrepareReport|Runner'` 输出 `ok`。

### 步骤 5.2：omp 工具（发布工具除外）
Depends on：5.1。
Files：`internal/orbdrt/assets/oryn-orb.ts`、`internal/github/tools.go`、`tools_test.go`、`internal/orbdrt/selfsetup_test.go`。
Do：

- **`github_classify_issue`**：
  - 参数：`primary`（8 类）、`priority?`、`functional[]`、`provider?`、`platform?`、`rationale`、`branch_slug?`；
  - 行为：写入 `thread_issues.classification`；打标签（标签名移植自 robomp）；`bug` 必须带 `priority`；带 `branch_slug` 时返回建议分支 `oryn/<线程短 id>/<slug>`；
  - 返回：`implAuthorized`。
- **`github_task_state`**：
  - 参数：`state`、`note?`、`repro?{title, command, output, exit_code, reproduced}`；
  - 行为：`needs_info` 时自动添加 `needs-info` 标签；`repro` 写成线程条目。
- **`github_prepare`**：
  - 参数：`level`、`skip_checks?`、`reason?`；
  - 行为：交给 orbd 执行（5.3）。
- **`github_autoclose`**：
  - 参数：`number?`、`hours?`；
  - 行为：记录本线程身份在该 issue 上最近一条评论的 id。
- **`github_abort_task`**：
  - 参数：`reason`；
  - 行为：状态置为 `abandoned`，写入审计，不对外发言。

Accept：`go build ./... && go test ./internal/github/... -run Tool && go test ./internal/orbdrt/... -run AgentAssets` 输出 `ok`。断言扩展文本中包含这五个工具名。

### 步骤 5.3：`github_prepare` 的 orbd 执行器
Depends on：5.1。
Files：

- 新建 `internal/orbdrt/prepare.go`、`prepare_test.go`；
- 改 `internal/orbdrt/setup.go`：接收 `githubGates`；
- 改 `internal/orbs/setup.go`：由项目的 `github_gates` 填写 `githubGates`。

Do：

- 按 R20 的顺序执行。
- 提交信息的修复：对 `origin/<默认分支>..HEAD` 中的提交，逐个用 `git commit-tree` 重建，保留 tree、作者、日期。
- 发布线程不检查「当前分支不是默认分支」。
- 通过后调用 `github.prepare.report`。

Accept：`go build ./... && go test ./internal/orbdrt/... ./internal/orbs/... -run Prepare` 输出 `ok`。覆盖：

- detached HEAD；
- 当前在默认分支；
- 区间为空；
- 工作树不干净；
- 身份不符；
- 检查失败；
- `\n` 修复后 tree 不变；
- 通过后上报。

### 步骤 5.4：出站代理门禁
Depends on：5.3、2.3、4.1、4.3。
Files：

- 新建 `internal/egress/gate.go`、`gate_test.go`；
- `egress.Policy.GitHub` 增加 `Gates *GateConfig`、`ReviewOnly`、`CommentOnlyReviews`、`ImplAuth`；
- `Resolver` 增加：
  - `PrepareOK(sha, level string) (ok, skipped bool)`
  - `ImplAuthorized() bool`
  - `HeadSHA(repo, ref string) (string, error)`：用该 orb 自己槽位的令牌查询
  - `IsOrynHook(repo string, id int64) bool`
- `internal/orbs/setup.go`：按项目和线程填写上述策略。

Do：按下面的覆盖表执行。REST 的前缀 `/repos/{o}/{r}` 与 `/repositories/{id}` 等价，匹配前先解码路径并去掉结尾斜杠。

| 类别 | REST | GraphQL mutation | 处理 |
|---|---|---|---|
| 创建 PR | `POST /pulls` | `createPullRequest` | 检查 PR 门禁、`ImplAuth`；`ReviewOnly` 时拒绝 |
| 改 ref（可知 SHA） | `POST /git/refs`、`PATCH /git/refs/*` | `createRef`、`updateRef`、`updateRefs` | 推送门禁开启时查 `PrepareOK(sha,"push")`；`ReviewOnly` 时拒绝 |
| 改 ref（不可知 SHA） | `PUT\|DELETE /contents/*`、`POST /merges`、`POST /merge-upstream`、`PUT /pulls/{n}/update-branch` | `createCommitOnBranch`、`mergeBranch`、`updatePullRequestBranch` | 推送门禁开启或 `ReviewOnly` 时拒绝 |
| 合并 | `PUT /pulls/{n}/merge` | `mergePullRequest`、`enablePullRequestAutoMerge`、`enqueuePullRequest` | `ReviewOnly` 时拒绝 |
| review 事件 | `POST /pulls/{n}/reviews`（`event`）、`POST /pulls/{n}/reviews/{id}/events` | `addPullRequestReview`、`submitPullRequestReview` | `CommentOnlyReviews` 时只放行 `COMMENT`，以及没有 event 的待提交 review |
| Oryn 的 hook | `PATCH\|DELETE /hooks/{id}`、`PATCH /hooks/{id}/config` | 无 | `IsOrynHook` 为真时始终拒绝 |
| git 推送 | `git-receive-pack` | — | 只检查 `refs/heads/*`；`ReviewOnly` 时在 `info/refs?service=git-receive-pack` 就返回 403 |

pkt-line 解析规则：

- 只读命令段，最多 `receivePackHeadMax`；
- 空命令段放行；
- 跳过 `shallow` 行；
- 去掉 NUL 之后的能力串；
- 遇到 `push-cert` 拒绝；
- 解析完用 `io.MultiReader` 还原请求体。

拒绝推送时：读完并丢弃请求体，返回 `200 application/x-git-receive-pack-result`，内容为 `unpack ok` 加每个 ref 的 `ng <ref> oryn: <原因>`；客户端协商了 side-band-64k 时放在 band 1 里。A2 不成立时改为 403。

GraphQL 中受管控的 mutation，如果参数无法静态求值，一律拒绝。

Accept：`go build ./... && go test ./internal/egress/... -run Gate` 输出 `ok`，覆盖表中每一行的放行和拒绝、别名或 fragment 绕过、超限请求体、`ng` 响应格式。

### 步骤 5.5：问题自动关闭
Depends on：5.2、3.1。
Files：新建 `internal/github/autoclose.go`、`autoclose_test.go`；改 `listen.go`、`triggers.go`。
Do：

- 落实 R18：
  - 在 `scanInterval` 循环里用 `UPDATE … SET state='claimed' WHERE state='pending' AND due_at<=?` 原子认领到期项；
  - 查询 `GET /repos/{o}/{r}/issues/comments/{comment_id}/reactions?content=-1`，只认 issue 作者；
  - 关闭请求为 `PATCH /issues/{n}`，参数 `{state:"closed", state_reason:"completed"}`，令牌用线程身份；
  - 返回 404 时记为 `already_closed`。
- 关闭操作本身也走预登记，防止回流。

Accept：`go build ./... && go test ./internal/github/... -run Autoclose` 输出 `ok`。覆盖：作者 👎、非作者 👎、新评论取消、外部关闭取消、并发只关闭一次。

## 阶段 6：发布哨兵

### 步骤 6.1：`workflow_run` 状态机与 `github_release_status`
Depends on：4.1、5.2、5.4。
Files：新建 `internal/github/release.go`、`release_test.go`；改 `tools.go`、`oryn-orb.ts`（注册 `github_release_status`）；`githubtest` 增加 `actions/runs`、`runs/{id}/jobs`、`jobs/{id}/logs`、`git/ref/tags/{tag}`、`releases/tags/{tag}`。
Do：

- 落实 R22：
  - 每个仓库串行处理，串行键为 `<repo>#release`；
  - 失败报告最多 5 个 job，每个附日志最后 120 行，超出部分列入 overflow；
  - 线程身份用 `ResolveAutomationSlot(untrusted=false)`。
- `github_release_status` 返回：runs、阻塞的 job、当前轮数、`version`、`tagOid`（事件中的 tag 对象 id，供推送命令的 lease 使用）。
- 发布线程开启门禁时，项目门禁中推送门禁的 `level` 取 `push`。

Accept：`go build ./... && go test ./internal/github/... -run Release && go test ./internal/orbdrt/... -run AgentAssets` 输出 `ok`，覆盖：`stale`、阻塞与非阻塞、green 需要非草稿 Release、轮数上限、默认分支被推进、`superseded`。

## 阶段 7：API、CLI、预设

### 步骤 7.1：审计、任务、hook、triage、replay、cleanup、presets 端点
Depends on：2.3、4.3、6.1、7.3（presets）。
Files：`api/github.go`、`api/github_test.go`。
Do：实现 0.4 中仍返回 501 的操作。`replay` 只接受 7 天内 `outcome` 已结束的投递。

Accept：`go build ./... && go test ./internal/api/... -run 'GitHub(Audit|Tasks|Hooks|Triage|Replay|Cleanup|Presets|OAuthApp)'` 输出 `ok`。

### 步骤 7.2：CLI `oryn github`
Depends on：7.1。
Files：新建 `cmd/oryn/github.go`、`github_test.go`。
Do：实现 R24 的五个子命令，输出表格，支持 `--json`。

Accept：`go build ./... && go test ./cmd/oryn/... -run Github` 输出 `ok`。

### 步骤 7.3：robomp 预设
Depends on：4.2、4.3、5.2、5.4、6.1。
Files：新建 `internal/github/presets/`，内含 `triage.md`、`review.md`、`release.md`、`finalized.md`、`autoclose_suffix.md`，每个文件头注明「移植自 can1357/oh-my-pi python/robomp，MIT」；新建 `presets.go`、`presets_test.go`。
Do：`presets.go` 用 `embed` 加载 `presets/*.md`，加载时去掉开头的 `<!-- … -->` 出处注释（`finalized.md` 的正文会原样发到 GitHub）。提示词文件已提前写好，本步只需核对，并补上加载代码和测试。提示词的内容是：把 robomp 的以下内容改写为使用 Oryn 工具名与 `gh` 的提示词：

- 分类体系、bug 价值门、重复/已修复检查；
- 四段式 PR 正文；
- question 的 👎 提示后缀；
- 评审只发 COMMENT，行内评论用 `gh api …/reviews` 一次提交；
- 发布修复流程，使用 R22 的推送命令。

Accept：`go build ./... && go test ./internal/github/... -run Preset` 输出 `ok`，断言预设字段与 R26 一致，引用的工具名都已注册。

## 阶段 8：Web 与删除旧契约

### 步骤 8.1：设置页与删除旧 OpenAPI 操作
Depends on：0.4、7.1。
Files：

- `schema/openapi.yaml`：删除 `beginGitHubAppManifest`、`deleteGitHubApp`、`syncGitHubInstallations`、`startGitHubManifest`、`finishGitHubManifest`、`GitHubApp`、`GitHubInstallation`、`GitHubManifestRequest`，以及 `GitHubStatus` 的旧字段；
- `internal/api/github.go`：删除对应处理器；
- `web/src/pages/settings/GitHubSettings.tsx`；
- `web/src/pages/projects/ProjectGitHub.tsx`：未绑定原因、hook 状态；
- `web/src/api/github-queries.ts`；
- `web/src/mock/github.ts`；
- i18n；
- 测试：`GitHubSettings.test.tsx`、`ProjectGitHub.test.tsx`、`RepoPicker.test.tsx`。

Do：设置页包含以下卡片：OAuth App（Client ID、Secret、回调地址带复制按钮、webhook 基址、删除）；身份（scope 与缺失项）；hook 列表；审计列表（分页、过滤）。

Accept：`make gen && go build ./... && (cd web && bun run check && bun run test -- GitHubSettings ProjectGitHub RepoPicker)` 全部通过。

### 步骤 8.2：规则编辑器、项目门禁、线程任务状态
Depends on：8.1、7.3。
Files：

- `web/src/pages/projects/GitHubTriggers.tsx`；
- `ProjectEditor.tsx`：门禁区；
- `ThreadHeader.tsx`、`CompactThreadHeader.tsx`：状态徽标；
- i18n；
- 测试：`GitHubTriggers.test.tsx`，新增 `ProjectGates.test.tsx`。

Do：规则表单加入 0.3 的全部字段和三个预设按钮；在项目编辑器里加门禁配置区；线程页头显示 issue 状态或发布状态。

Accept：`cd web && bun run check && bun run test -- GitHubTriggers ProjectGates` 通过。

## 阶段 9：文档与端到端验收

### 步骤 9.1：文档
Depends on：阶段 1–8。
Files：

- `docs/deploy.md` §13，重写，包括：
  - 注册 OAuth App 的步骤与回调地址；
  - scope 说明；
  - 组织批准与 SSO；
  - 身份规则与 Q6；
  - 事件来源与额度；
  - 门禁、发布哨兵；
  - 风险说明：令牌对账号可见的全部仓库有效，自动化应使用只被邀请到必要仓库的 Agent 账号；
  - 包仓库推送不在本期范围。
- `docs/protocol.md`：把 M14 的 GitHub 一节改写为 M15。
- `README.md`：GitHub 段。

Accept：`grep -n "GitHub App\|manifest\|installation\|oryn-agent" docs/deploy.md docs/protocol.md README.md` 只在标明「M14 历史」的段落里出现。

### 步骤 9.2：端到端验收
Depends on：9.1。
Files：新建 `internal/server/e2e_m15_test.go`、`web/scripts/m15-accept.ts`；`web/package.json` 加 `accept:m15`；`web/scripts/m14-accept.ts` 中的 App 段改为 OAuth。
Do：

- **Go 端到端**：用 `githubtest` 和附录 A 的请求形态，直接对 egress handler 发请求，覆盖：
  - 配置 OAuth App；
  - 连接两个身份；
  - 无项目线程 fork、push、开上游 PR，并关联；
  - 上游 PR 合并后线程标记为已交付；
  - robomp 分诊预设建线程；
  - `github_prepare` 未通过时拒绝开 PR，通过后放行；
  - 自动关闭；
  - 发布哨兵到达 green；
  - 审计记录完整；
  - 自身写入不回流。
- **真机部分**：`m15-accept.ts` 用真实 gh 在 orb 中执行 `gh api user`、`gh pr create`，走 fakegithub 透传。

Accept：`go test ./internal/server/... -run E2EM15` 输出 `ok`；`cd web && bun run accept:m15` 退出 0；`make test` 通过。

## 附录 A：gh 的 mutation 形态（步骤 0.2 填写）

gh 版本固定为 `v2.102.1`（本机版本；orb 镜像的 Arch `github-cli` 包跟随最新版，若版本不同，2.3 实现前用同样的方法复核）。来源：cli/cli 对应 tag 下的 `api/queries_*.go`、`pkg/cmd/**/http.go`。

| gh 子命令 | 请求 | 操作名 / mutation 字段 | input 中的目标字段 | 响应 selection |
|---|---|---|---|---|
| `pr create` | GraphQL | `PullRequestCreate` / `createPullRequest` | `repositoryId`、`baseRefName`、`headRefName`（可带 `owner:`）、`headRepositoryId?` | `pullRequest{id url}` |
| `pr create`（元数据） | GraphQL | `PullRequestCreateMetadata` / `updatePullRequest`；`PullRequestCreateRequestReviews` / `requestReviews` | `pullRequestId` | 仅 id/url |
| `pr review` | GraphQL | `addPullRequestReview` | `pullRequestId`、`event`（COMMENT / APPROVE / REQUEST_CHANGES）、`body` | `clientMutationId` |
| `pr edit --add-reviewer` | GraphQL | `requestReviewsByLogin` | `pullRequestId` | — |
| `pr comment` / `issue comment` | GraphQL | `CommentCreate` / `addComment` | `subjectId`（issue 或 PR 的 node id）、`body` | `commentEdge.node.url`（`…#issuecomment-<id>`） |
| 编辑、删除评论 | GraphQL | `CommentUpdate` / `updateIssueComment`；`CommentDelete` / `deleteIssueComment` | `id` | url / `clientMutationId` |
| `issue create` | GraphQL | `IssueCreate` / `createIssue` | `repositoryId` | issue url |
| `issue close` | GraphQL | `closeIssue` | `issueId`、`stateReason?` | — |
| `pr close` / `pr reopen` / `pr ready` / `pr ready --undo` | GraphQL | `closePullRequest` / `reopenPullRequest` / `markPullRequestReadyForReview` / `convertPullRequestToDraft` | `pullRequestId` | — |
| `pr merge` | GraphQL | `mergePullRequest`、`enablePullRequestAutoMerge`、`disablePullRequestAutoMerge` | `pullRequestId` | — |
| `pr update-branch` | GraphQL | `updatePullRequestBranch` | `pullRequestId` | — |
| `pr revert` | GraphQL | `revertPullRequest` | `pullRequestId` | — |
| `issue/pr edit --add-label` | GraphQL | `addLabelsToLabelable` / `removeLabelsFromLabelable` | `labelableId` | — |
| `issue/pr edit`（标题、正文） | GraphQL | `updateIssue` / `updatePullRequest` | `id` / `pullRequestId` | — |
| `pr edit`（指派） | GraphQL | `replaceActorsForAssignable` | `assignableId` | — |
| 子 issue 与依赖 | GraphQL | `addSubIssue`、`removeSubIssue`、`addBlockedBy`、`removeBlockedBy`、`updateIssueIssueType` | `issueId` | — |
| `repo fork` | REST | `POST /repos/{o}/{r}/forks` | — | repo JSON（含 `node_id`） |
| `release create/upload` | REST | `POST /repos/{o}/{r}/releases`；上传走 `uploads.github.com` 的 `upload_url` | — | release JSON |
| `api -X POST …/comments` | REST | 路径即操作 | 路径 | 评论 JSON（`id`、`node_id`、`html_url`） |

结论：gh 的 mutation 响应**不含**新对象的数值 id，只有 `addComment`、`createPullRequest`、`createIssue` 返回 URL。所以防回流以「请求侧预登记目标 + 正文哈希」为主（2.3），URL 锚点和 REST 响应 id 只作为补充键。

## 附录 B：真实 GitHub 探查结论（步骤 0.2 填写）

探查日期：2026-11-16。令牌：本机 gh 的 GitHub CLI OAuth 令牌（`gho_`，scope 为 `gist, read:org, repo, user, workflow`），等价于 OAuth App 的用户令牌。临时仓库：`Lynricsy/oryn-m15-probe`（私有，id 1459651303；令牌没有 `delete_repo`，**需主人手动删除**）。

1. ✅ `X-OAuth-Scopes` 的格式是逗号加空格，例如 `gist, read:org, repo, user, workflow`。
2. ✅ 只有 `repo` scope、没有 `admin:repo_hook` 时，也能创建和删除仓库 hook、读取投递日志。`GET` hook 返回的 `config.secret` 被打码成 `********`，所以 1.2 每轮同步都要连同 secret 一起 `PATCH`，不能先读后写。
3. ✅ 投递日志接口 `GET /repos/{o}/{r}/hooks/{id}/deliveries` 单独计数：`X-RateLimit-Resource: webhook_deliveries`，上限 500 次/小时；列表和详情各计 1 次；带 `If-None-Match` 返回 304 时不计数（`X-RateLimit-Used` 不变）。
4. ✅ 投递请求头带 `X-GitHub-Hook-ID`、`X-GitHub-Hook-Installation-Target-ID`（等于仓库数值 id）、`X-GitHub-Hook-Installation-Target-Type: repository`、`X-GitHub-Delivery`、`X-GitHub-Event`。即使目标地址返回 502，投递也会进日志，详情里有完整的请求头和载荷。
5. ✅ 用 GraphQL `addComment` 发出的正文 `"  第一行 line1\r\nline2 with trailing   \r\n\r\n```\ncode\n```\n  "`，与 webhook 载荷 `comment.body` 逐字节相同。载荷里有 `comment.id`、`comment.node_id`、`issue.node_id`、`sender.login`、`comment.author_association`。
6. ✅ 带 `Accept-Encoding: gzip` 请求 `api.github.com/user`，上游返回 `content-encoding: gzip`。2.3 删除 `Accept-Encoding` 是必要的。
7. ✅ 本地实测 git 2.x：对 `POST git-receive-pack` 回 200，side-band-64k 的 band 1 里放 `unpack ok` 加 `ng refs/heads/feature oryn: <原因>`，客户端显示 `! [remote rejected] HEAD -> feature (oryn: <原因>)`，退出码为 1。
8. ✅ 本地实测 R22 的原子推送命令：默认分支已被别人推进时，整体失败，分支和 tag 都不变（`atomic push failed … (fetch first)`）；没被推进时，分支快进，tag 强制移动。
9. ✅ 对没有 push 权限的仓库（`cli/cli`）调用 `collaborators/{login}/permission`：返回 403，message 为 `Must have push access to view collaborator permission.`
10. ⚠️ 未测：OAuth App 的组织访问限制（A3）、`check_suite` 的仓库 hook 投递（按文档只投递 `completed`）。

## 附录 C：REST 轮询的合成事件表（步骤 3.1 填写）

（待填）
