# M24 — Projects 重做：项目主页、仓库浏览、托管仓库、Git LFS、项目级密钥（实施计划）

> 编号取 M24：起草时 M22 最高，入库前 `docs/plan-m23.md` 已被 Boxes 计划占用，故顺延。迁移号仍从 0045 起（Boxes 计划写明被占用时顺延）。
> 本文自包含：执行者不需要读过产生它的对话。行号只作提示（取自 `b6ebe5b`），动手前按符号重读。工作区是多会话共用的，提交时只带自己改的行。
> 四个阶段的规格各自对照代码起草，合并时逐条核对过跨阶段的名字与接口。步骤编号是「阶段.序号」，例如步骤 2.4 是阶段 ② 的第 4 步。

## Context

要求：参考 Amp 的 Projects 界面（项目总览、项目主页、仓库浏览、线程列表、分栏设置）重做 Oryn 的「项目」分区。现在的项目页只是一张设置表单加一个「查看线程」的侧栏过滤按钮，回答不了「这个代码库现在怎么样了」；过滤功能直接取消。

终态：`/projects` 是项目总览（线程数、近 14 天活动曲线、最后活跃）；`/projects/<id>` 是项目主页（描述与仓库行、运行态状态条、最近线程、提交到基线、文件列表、README）；`…/threads`、`…/commits`、`…/branches`、`…/tags`、`…/files/*` 是子页；设置搬到 `…/settings/<分组>`，左导航分组。仓库内容来自控制面自己维护的裸仓库（外部仓库的镜像，或 Oryn 托管的正本）；Git LFS 在仓库浏览、orb 工作区、托管仓库三处都可用；项目可以有自己的密钥与环境变量。

已定的取舍（实现时不再改）：

- **仓库数据来源**：orynd 在 `<data_dir>/repos/<projectId>.git` 维护裸仓库并自己执行 `git`。外部仓库是完整镜像（全部 `refs/heads/*` 与 `refs/tags/*`，不含 PR 引用）：首次打开项目页时克隆；之后有读取且距上次拉取超过 60 秒时后台拉取（读取先返回旧数据）；GitHub 项目另订阅 `push` 事件；页面有手动刷新。控制面主机没装 `git`（或低于 2.31）时仓库区块显示原因，其余功能照常。单个镜像上限 5 GiB（可配），超过就删除镜像并提示。已归档的项目不自动克隆、不自动拉取，只能手动同步。拉取 GitHub 或有令牌的主机时沿用 orynd 进程的 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`（出站代理的上游与 GitHub 客户端本来就读这些变量）；其它主机经公网地址守卫直连。
- **项目主页**自上而下：描述与仓库行 → 状态条 → 最近线程 | 提交到基线 → 文件 → README → 服务。状态条 = 等你回复 / 工作中的线程数、打开的 PR 与失败的 CI、最近一次 setup 失败告警、近 7 天成本与回合（链到统计页）；服务块在有服务时出现，标题是「服务」，失败和已停止的也列出；不放定时任务摘要。
- **图标**：页头的复制链接 / Clone / 设置是纯图标按钮（`aria-label` + `title`），「新建线程」保留图标加文字；状态条每项、文件区的提交 / 分支 / 标签、仓库来源（GitHub / 其它 Git / Oryn 托管）带前置图标；线程行用现有状态图标。
- **总览**：一行一个项目（徽标、名称、描述与仓库、线程数、活动曲线、多久前活跃），按最近活动排序；页头 `⋯` 菜单里「显示已归档项目」；主按钮「新建项目」。`/projects` 不再跳到第一个项目；分区照旧记住上次停留的页面。
- **侧栏**：首行「总览」（行尾 `+` 新建项目），其后每个未归档项目一行（名称 + 线程数 · 多久前活跃），按名称排序，不显示定时任务数。行尾过滤按钮、「无项目的线程」行、线程分区的过滤签与 `projectFilter` 状态全部删除。
- **设置**：独立子页，纸面内左导航 + 右内容；窄屏导航是一页列表、点进去看内容。分组顺序：概览 / 环境 / Agent（含项目记忆开关）/ GitHub / 定时任务 / SSH 主机 / 密钥与环境变量（阶段 ④ 出现）。
- **线程子页**：搜索（标题与标签）+ 状态（全部 / 活跃 / 已归档 / 已交付，各档可以重叠）；行上显示状态、PR / CI、最后活动时间。不做消息数与用户过滤。
- **顺手补的字段与入口**：项目描述（一行，≤ 200 字）；归档 / 取消归档入口。已归档的项目不显示「新建线程」。
- **提交关联线程**：不改提交信息。出站代理在 orb 的 `git push` 经过时记下「哪个线程把哪个 ref 从哪个提交推到哪个提交」，加上现有 `thread_commits` 采样；GitHub 项目用「提交所属 PR → 线程」补 squash 合并。
- **托管仓库**：新建项目可选「空白仓库（Oryn 托管）」，创建时自带一个空树的初始提交（`Initial commit`，没有 README）；orb 经出站代理读写它；克隆地址 `https://<Oryn 地址>/git/<项目 id>.git`；`oryn clone <项目>` 登记 `oryn git-credential` 作为凭据助手（克隆地址的主机与 `oryn login` 的服务器不同时直接报错），控制面只在 `/git/` 路径接受「HTTP Basic + 设备令牌」；`oryn project create --push` 把本机仓库变成托管项目（默认加成 `origin`，已有指向别处的 `origin` 时报错并提示 `--remote`）。没有仓库的旧项目可在设置里「创建托管仓库」。基线分支不设保护。删除项目连仓库一起删（确认框写明）；归档后仓库只读。不设大小上限，也不额外安排清理。主人本机的推送不关联线程，只刷新页面并触发 Webhook。不支持托管与外部仓库互转。不做只读 Git 凭据与公开只读访问。
- **托管项目的交付**：改动面板出现「提交并推送到 <基线分支>」按钮（固定提示词）；线程把提交推上基线分支后自动标记「已交付」。GitHub 项目行为不变。
- **推送后 Webhook**：仅托管仓库；地址（不允许带用户名密码）+ 可选签名密钥（填了就带 `X-Oryn-Signature-256`，HMAC-SHA256）；每次推送成功后异步 POST 一次 JSON，不阻塞推送、不重试；设置里显示最近一次投递的时间与状态码；地址只能主人改，助手不能改。线程里 agent 的推送同样触发，载荷里 `pusher.kind` 为 `thread`，要不要据此部署由接收端把关。
- **Git LFS**：三处都支持。仓库浏览读到 LFS 指针时，用镜像拉取的同一份凭据向上游 LFS 接口按需取内容，放进有上限的本地缓存（默认 2 GiB，先清最久没用的），单个文件超过 100 MiB 只给下载，控制面不需要装 `git-lfs`；只用仓库主机自己的 LFS 接口，不读仓库里的 `.lfsconfig`（令牌只发给它所属的主机）。orb 工作区：出站代理放行 LFS 接口并注入凭据，上传算写操作（受只读评审线程与「无项目只读」规则约束）；令牌仍不进 orb：批量接口的请求与响应都由代理解析后重写，上游在响应里回显或新签发的凭据会被去掉，无法解析的响应不放行；LFS 对象取不下来时 setup 记为失败（工作区里的指针文件会让构建和测试悄悄出错）。托管仓库：控制面实现 LFS 服务端（批量接口 + 对象上传下载），主人本机与 orb 都能用，对象随仓库存放、随项目删除，不设上限，不做文件锁定。
- **正本与数据盘的保护**（方案评审后加的）：托管仓库目录里有标记文件，镜像的清理逻辑永远不删带标记的目录，找不到项目行的托管仓库移到 `orphaned/` 而不是删除；被删除或被强推覆盖的分支旧值在仓库里留存 30 天；orb 推上来的对象先过 fsck；数据盘可用空间低于 `repos.min_free_gb`（缺省 2 GiB）时不克隆、不拉取、不接受推送；git 进程被取消时先 SIGTERM，不留下锁文件；建仓与改基线分支不在数据库事务里执行 git。
- **密钥与环境变量**：项目设置里一张表，每条可勾「保密」（保存后不可再读；改成不保密必须重新输入值）或不勾（普通环境变量，值可见可改）；支持粘贴 `.env` 批量添加；全局密钥页不变。项目级的值只对本项目的 orb 生效。同名时的优先级：全局环境变量 < 项目条目 < 模型供应商密钥对应的变量。能按项目设的是环境变量与 Git 令牌（`git/<host>/token`）；模型供应商密钥只有全局的。项目里设的 `git/github.com/token` 只在 orb 不归 GitHub 集成管时生效。Runner 线程与协调宿主不下发项目环境变量；助手不能读写；改动使项目快照失效。已归档的项目仍可查看与修改条目。迁移回滚会丢掉全部项目级条目。
- **不做**：Amp 的只读 Git 凭据、公开代码访问、Change Owner、Satellites、Feedback Widget、Space 指令、提交的 Verified 徽标、LFS 文件锁定、任何多用户功能。
- 记为 M24，在 `main` 上按步骤原子提交；四个阶段按 ① → ② → ③ → ④ 顺序交付，每个阶段结束都可单独合入，前一段不依赖后一段。

## 假设

- GitHub 的 LFS batch 接口接受与 git 拉取相同的 `Authorization: Basic base64(x-access-token:<令牌>)`。依据：`gh` 自己的凭据助手对 OAuth 令牌就用这个用户名（`cli/cli` 的 `pkg/cmd/auth/gitcredential/helper.go:16`），git-lfs 经同一个助手取凭据。本仓库的测试只对假上游验证；「Verification」第 6 步的真机走查覆盖真实 GitHub，不成立时要改的是步骤 2.19（控制面取对象）与 2.21（出站代理注入）里生成 `Authorization` 的地方。
- GitHub 返回的 LFS 下载地址在别的主机上（如 `github-cloud.githubusercontent.com`），是自带签名的 URL。计划不写死主机名，也不把仓库令牌发给这些主机。
- orb 镜像里的 git-lfs 是 3.x（Arch 仓库现状），只会调用 batch 接口；镜像冒烟脚本里已有 `git lfs version`。

## 通用约定

- 工作目录 `/root/Projects/Ling/Oryn`，分支 `main`。每个步骤一个提交，格式 `<type>(<scope>): <gitmoji> <中文 subject>`，带 `Co-authored-by: Wine Fox <fox@ling.plus>`。提交只带本步骤自己改的行，不用 `git add -A` / `git commit -a`，不动别人未提交的改动。
- 每个阶段结束用 `record-agent-log` 记一条（做了什么、为什么这样做）。
- 每个改到 `web/` 的步骤结束都要通过 `cd web && bun run check && bun run test`；每个改到 Go 的步骤结束都要通过该步骤「验收」里列的 `go test`。改了 `schema/openapi.yaml` 或 `internal/store/{migrations,queries}` 的步骤先跑 `make gen` 与 `make lint-spec`；`make check-gen` 放在该步骤提交**之后**跑（它是 `make gen` 加 `git diff --exit-code`，生成物没提交时必然报差异）。
- 迁移编号：写本文时最高是 `0044_schedule_role.sql`；本计划依次用 0045（步骤 1.1）、0046（2.2）、0047（3.3）、0048（3.7）、0049（4.1），被占用时顺延并同步改后面的编号。迁移用 goose 格式且 Down 必须可执行；`internal/store/queries/*.sql` 里的注释只用英文。
- 动手前读 `skill://react`、`skill://typescript`；改 Go 的步骤读 `skill://go`；写迁移读 `skill://sqlite`；阶段 ② ③ ④ 里涉及凭据、对外拉取、认证的步骤读 `skill://security-review`；改 e2e 的步骤读 `skill://web-testing`。
- 界面遵守 `docs/design/web-guidelines.md`：颜色只用 `web/src/index.css` 的 token；圆角只用既有档位；最小字号 `text-2xs`；弹层一律走 `web/src/components/ui/Floating.tsx`；确认一律用 `confirmAction`（`web/src/lib/confirm.ts`）；断点用 `useBreakpoint()`；亮暗两套主题都要看。新文案同时加进 `web/src/i18n/locales/zh-CN.json` 与 `en.json`（`locales.test.ts` 要求 key 集合一致）。图标只用 `web/src/components/ui/icons.tsx` 里登记过的，缺的按步骤里写的 Phosphor 深导入补登记。
- 阶段 ① 与阶段 ② 的界面做完后各做一轮真实画面评审：`cd web && ORYN_SHOTS_DIR=/tmp/oryn-shots/m23 bun run shots:all --only projects` 出亮暗 × 390 / 1280 的截图，亲自看图，再按 `skill://oil-ui` 的「存量已有界面润色评审」派一位没有上下文的评审者只看截图与任务说明；评审指出的问题在本阶段内改完再进入下一阶段。
- 每个阶段的最后一步是文档（`docs/features.md`、`docs/deploy.md`、`docs/protocol.md`、`README.md`），与代码同一阶段合入。

## 阶段 0 — 入库

**步骤 0.1** 把本计划存为 `docs/plan-m23.md` 并提交（`docs(plan): 📝 M24 Projects 重做计划`）。

## 阶段 ① — 界面骨架

交付：`/projects` 变成项目总览（不再跳转）。每个项目有三个页面：主页（描述、仓库行、状态条、最近线程、服务）、线程子页（搜索和状态筛选，筛选写进 URL）、设置子页（左导航，6 组）。另有新建页、归档与取消归档、项目 `description` 字段。
侧栏首行是「总览」，行尾有 `+`；项目行显示线程数和活跃时间。线程分区的项目过滤功能整体删除，`ProjectsPage.tsx` 删除。
只用现有数据和新增的 `description` 字段，不依赖镜像。提交列、文件区块、README 由阶段 ② 填进本阶段留好的位置。

### 阶段 ① 的通用约定

- 每步结束必须通过 `cd web && bun run check && bun run test`。1.1 另跑 `make gen && make lint-spec && go test ./internal/api/... ./internal/orbs/...`；`make check-gen` 要在 1.1 提交之后再跑（它比较的是工作区与暂存区）。
- 新文案同时写进 `web/src/i18n/locales/zh-CN.json` 和 `en.json`（`locales.test.ts:12` 要求两边 key 集合完全一致）。zh 的复数 key 同样写 `_one` 和 `_other`，沿用 `sidebar.projectThreads_one/_other` 的做法（zh-CN.json:172-173）。
- 复制一律用 `copyToClipboard`（`lib/clipboard.ts:5`）。失败时 `showToast({ text, tone: "err" })`（`state/toasts.ts:55`）。成功时按钮图标换成 `check`，1200ms 后复原，沿用 `ChangesPanel.tsx:76-82` 的做法。`components/ui/CopyButton.tsx` 是 24px 的淡色行内按钮，页头的 32px 图标钮不能复用它。
- 确认一律用 `confirmAction`（`lib/confirm.ts:26`）。
- 弹层只用 `Menu`（`ui/primitives.tsx:294`，基于 Floating）。
- 分段按钮照抄 `InboxPage.tsx:324-341` 的写法：`role="group"`、`aria-pressed`、`tabClass(sel)`、`h-6`，选中项加 `bg-panel shadow-xs`。

### 步骤 1.1 — 项目描述字段（后端）
依赖：无，因为只是给 projects 表加一列。
文件：新建 `internal/store/migrations/0045_project_description.sql` / 修改 `internal/store/queries/projects.sql`、`schema/openapi.yaml`、`internal/api/projects.go`、`internal/orbs/setuprun.go` / 生成 `internal/store/db/*`、`internal/apigen/apigen.gen.go`、`web/src/api/schema.d.ts` / 测试 新建 `internal/api/projects_description_test.go`、修改 `internal/orbs/setuprun_test.go`

做什么：
1. 新建迁移 0045。目录里最高编号是 0044，写法照 `0044_schedule_role.sql`：
   ```sql
   -- +goose Up
   -- 项目的一行描述（≤200 字符；NULL = 未设置），项目总览与项目主页显示。
   ALTER TABLE projects ADD COLUMN description TEXT;
   -- +goose Down
   ALTER TABLE projects DROP COLUMN description;
   ```
2. 改 `projects.sql`：
   - `CreateProject` 的列表与 VALUES 各加一项：列 `description`、值 `@description`。
   - `UpdateProject` 的 SET 加 `description = @description`。
   - `GetProject` / `ListProjects` 是 `SELECT *`，不用改。查询文件里的注释只写英文。
3. 改 `openapi.yaml`：
   - `Project`（:7149）加 `description: { type: string, description: 一行描述；未设置时省略 }`。
   - `ProjectCreate`（:7202）加 `description: { type: string, maxLength: 200, description: 一行描述（≤200 字符）；空串等同省略 }`。
   - `ProjectPatch`（:7236）加 `description: { type: string, maxLength: 200, description: 空串表示清除 }`。
4. 改 `projects.go`。写法跟着可选字符串字段走（`optStr` 会 trim 并把空串转成 nil，见 `server_auth.go:124`；更新时用 `patchStr`，`projects.go:319-323`）：
   - 新增 `func projectDescription(v *string) (*string, error)`：先 `d := optStr(v)`；如果 `d != nil` 且 `utf8.RuneCountInString(*d) > 200`，返回 `errInvalid("description 至多 200 个字符")`（400 `invalid_request`）。服务端没有挂请求校验中间件（grep `OapiRequestValidator` 无命中），所以长度只能由这个函数检查。
   - `projectJSON`（:25）加 `Description: p.Description`。
   - `createProject`（:170）先调 `projectDescription(req.Description)`，再在 `CreateProjectParams` 里加 `Description: desc`。
   - `updateProject`（:256）在事务外先校验；在 `u` 的初始化里加 `Description: p.Description`；`if req.Description != nil { u.Description = desc }`。
   - description 不触发 `poolKick`，也不影响快照哈希。
5. 改 `setuprun.go:248`：`UpdateProjectParams` 整行覆盖（:247 的注释写明了），所以要补 `Description: prj.Description`。全部调用点用 `rg -n "UpdateProjectParams\{" internal cmd` 列出，共三处：`projects.go:296`、`setuprun.go:248`，以及只有测试用到的 `github/hooks_test.go:204`（不改）。
6. 跑 `make gen`。生成的字段与同表的可选字符串字段同形（`apigen.Project` 里的 `Icon *string`），预期是 `Description *string`；以生成结果为准。
7. 测试：
   - `projects_description_test.go` 写 `TestProjectDescriptionAPI`，写法照 `avatar_identity_test.go:15` 的 `newH2Env`、`h2DeviceToken`。
   - `setuprun_test.go:240` 的 `TestProjectScriptsSetKeepsOtherColumns`：建项目时加 `Description: str("keep me")`，最后断言它仍然是 `"keep me"`。
   - 边界：PATCH 恰好 200 个字符 → 200 并保存；201 个 → 400 `invalid_request`。

范围：CLI（`cmd/oryn/project.go`）和助手工具（`internal/coord`）都不改。

验收：
- `make gen && go test ./internal/api/... ./internal/orbs/...` → `ok`。
- 具体行为（POST 指 `POST /api/v1/projects`，PATCH 指该项目的 PATCH）：
  - POST `{"name":"p","description":"  一行说明  "}` → 201，body 里 `"description":"一行说明"`。
  - POST 带 201 个字符的 description → 400。
  - PATCH `{"color":"red"}` → description 不变。
  - PATCH `{"description":""}` → 200，响应里没有 `"description"`，库里 `GetProject` 的 `Description == nil`。

### 步骤 1.2 — mock 与夹具
依赖：1.1（类型里有 `description`）。
文件：修改 `web/src/mock/fixtures.ts`、`web/src/mock/server.ts`

做什么：
1. `fixtures.ts:19-59`：
   - Oryn 加 `description: "自托管的 agent 工作台：控制面、orb 运行时与网页端"`。
   - oryn-core 加 `description: "Oryn 的 agent 内核（omp 分支）"`。
   - 末尾追加第三个项目（放在下标 2，不影响 `projects[0]`、`projects[1]` 的所有使用点）：`{ id: "prj_01JCZ000000000000000LEGACY", name: "legacy-site", description: "旧官网，已迁到 docs-site", shallowClone: false, snapshots: true, warmOrbs: 0, createdAt: NOW - 120 * 24 * HOUR, updatedAt: NOW - 10 * 24 * HOUR, archivedAt: NOW - 10 * 24 * HOUR }`。
   - 记忆 mock 已经过滤了归档项目（`mock/memory.ts:183`），不用改。
2. `server.ts:1509` 的 `GET projects` 改成 `const archived = url.searchParams.get("archived") === "true"; projects: this.projects.filter((x) => Boolean(x.archivedAt) === archived)`，与 `ListProjects` 一致（`projects.go:119-123`）。
3. `server.ts:1965` 的 `GET threads` 增加 `project` 参数：值是 `"none"` 时只留 `!t.projectId`；其它值只留 `t.projectId === project`；不给则不过滤。这与契约一致（`schema.d.ts:11645`）。

验收：
- `cd web && bun run test` → 全绿（`ProjectsPane.test.tsx:45` 只取未归档项目）。
- `new MockServer().handleFetch(new Request("http://x/api/v1/projects?archived=true"))` → 只返回 `legacy-site`。

### 步骤 1.3 — 删除「按项目过滤线程」功能
依赖：无，因为功能自成一体。
文件：修改 `web/src/state/ui.ts`、`web/src/components/layout/ThreadsPane.tsx`、`ProjectsPane.tsx`、`web/src/pages/ProjectsPage.tsx`、两个语言文件、`web/scripts/m96-accept.ts`、`web/scripts/m9-accept.ts` / 测试 `ThreadsPane.test.ts`、`ThreadsPane.test.tsx`、`ProjectsPane.test.tsx`、`SectionPanes.test.tsx`、`useSection.test.tsx`、`ProjectsPage.test.tsx`

做什么（行号都按本次读取重新核对过）：
1. `ui.ts:187-189` 删掉 `projectFilter` 的注释、字段和 `setProjectFilter`；`:278-279` 删掉对应实现。
2. `ThreadsPane.tsx`：
   - :2 的头注释删去「项目过滤签、」。
   - :15 的 import 去掉 `Project`（只剩被删的签在用）。
   - 删 :67-74 的 `filterByProject`。
   - 删 :528-565 的 `ProjectFilterChip`。
   - :569 的注释删去「不受项目过滤影响；」。
   - 删 :593 的 `projectFilter`。
   - :616-620 改成 `useMemo(() => groupThreads(Object.values(threads), now), [threads, now])`。
   - 删 :650-655 的渲染。
3. `ProjectsPane.tsx`：
   - :1-3 的头注释改为「首行新建项目，其后每个未归档项目一行」。
   - 删 :48-51 的 `filterThreads`、:84-91 行尾的 `<IconButton icon="chats" …/>`（连同包着它的 `div`，`NavLink` 保留为列表的直接子元素）、:94-107「无项目的线程」按钮。
   - 删 :29 的 `switchSection`；import 去掉 `uiStore`、`useSwitchSection`、`IconButton`、`CountPill`。
4. `ProjectsPage.tsx`：:5 的注释删去「查看线程」那句；删 :206 的 `const switchSection` 与 :226-239 的「查看线程」按钮；import 去掉 `useSwitchSection`。
5. 两个语言文件删除：`sidebar.projectFilter`、`sidebar.clearProjectFilter`（各 :169-170）、整个 `projectsPane` 对象（:4338-4341）、`projectsPage.viewThreads`（:4343）。`sidebar.noProject` 保留，`ThreadsPane.tsx:632` 还在用。`AutomationsSettings.tsx:56` 的 `?project=` 是另一套参数，不动。
6. 测试：
   - `ThreadsPane.test.ts`：:4 的 import 去掉 `filterByProject`；删 `it("项目过滤：…")` 整块（约 :137-147，以 :139-145 的断言为准）。
   - `ThreadsPane.test.tsx`：:2 的注释删去「，项目过滤签仍在搜索框下」；:26 的 import 去掉 `projects`；:175 删 `projectFilter: undefined,`；:183 删整句；删从 :274 开始的两个 `it`（「项目过滤签…」「「无项目」过滤签」，到 :301）。
   - `ProjectsPane.test.tsx`：:63 和 :69 删 `projectFilter` 相关；删 :95-114 两个 `it`；:1-3 的注释同步改。
   - `SectionPanes.test.tsx:90` 改成 `uiStore.setState({ sectionPaths: {} })`。
   - `useSection.test.tsx:122` 删 `projectFilter: undefined,`。
   - `ProjectsPage.test.tsx`：:3 的注释删去「与「查看线程」」；:241 删；:423-428 的「查看线程」用例删；随之没人用的 `uiStore` import（:18）与只为该用例服务的 `<Route path="/" element={<p>THREADS</p>} />`（:221）一并删。
7. 验收脚本：
   - `m96-accept.ts:3311-3326`（点「查看线程」、等过滤签、清除）整段删掉，后面的 `/settings/projects` 检查（:3327）保留。
   - `m9-accept.ts:3211-3222` 换成两句：先 `await projs.locator('a[href^="/projects/prj_"]').first().waitFor({ timeout: 5_000 }).catch(() => {})`（项目行是异步加载的，`count()` 不等待），再 `check((await projs.locator('a[href^="/projects/prj_"]').count()) > 0, "项目分区列出项目行（链到项目页）")`。不点进去，保持下沉侧栏开着，这样 :3223 之后点「更多」仍然成立。

验收：
- `cd web && bun run check && bun run test` → 绿。
- 下面这条命令输出为空：`rg -n "projectFilter|setProjectFilter|filterByProject|ProjectFilterChip|project-filter|clearProjectFilter|filterThreads|noProjectThreads|viewThreads" web/src web/scripts web/e2e -g '!**/AutomationsSettings*'`。
- mock 下打开线程分区，侧栏没有过滤签。

### 步骤 1.4 — 公共小件
依赖：无，因为都是独立的新导出。
文件：新建 `web/src/lib/use-debounced.ts`、`web/src/lib/project-activity.ts`、`web/src/pages/projects/repo-label.ts` / 修改 `web/src/components/composer/CommandMenu.tsx`、`web/src/components/palette/CommandPalette.tsx`、`web/src/components/stats/statsParams.ts`、`web/src/api/queries.ts`、`web/src/components/github/PrChip.tsx`、`web/src/components/ui/icons.tsx` / 测试 新建 `lib/use-debounced.test.ts`、`lib/project-activity.test.ts`、`pages/projects/repo-label.test.ts`，修改 `components/stats/statsParams.test.ts`

做什么：
1. 防抖：新增 `export function useDebounced<T>(value: T, ms: number): T`，函数体就是 `CommandMenu.tsx:48-55` 的原文。删掉 `CommandMenu.tsx:48-55` 和 `CommandPalette.tsx:134-142` 两份私有副本，两处（:72、:431）改为从 lib 引入。仓库里只有这两份，都是私有的。
2. 项目活动：
   - 新增 `export interface ProjectActivity { threads: number; lastActiveAt?: number }`。
   - 新增 `export function projectActivity(threads: readonly Thread[]): Map<string, ProjectActivity>`：只统计有 `projectId`、`!archivedAt`、`!isCoordinatorThread` 的线程。`lastActiveAt` 取 `activityOf`（`lib/inbox.ts:23`）的最大值。
   - 新增 `export function lastActive(p: Project, a: ProjectActivity | undefined): number`，返回 `a?.lastActiveAt ?? p.createdAt`。
   - 新增 `export function byActivity(map: ReadonlyMap<string, ProjectActivity>): (a: Project, b: Project) => number`：最近活动降序，同值按 `name.localeCompare`。
3. `repo-label.ts`：
   - `export function repoLabel(url: string): string`：`new URL(url)` 成功且 `host` 非空时返回 `host + pathname`，去掉结尾的 `.git` 和 `/`；解析失败（如 `git@host:o/r`）或 `host` 为空（如 `github.com:o/r`，会被解析成 scheme）时原样返回。
   - `export function isGitHubRepo(url?: string): boolean`，判断 host 是不是 `github.com`。
4. `statsParams.ts` 新增 `export function daysWindow(days: number, now: number, tz = browserTimeZone()): StatsWindow`：起点是 `addDays(today, -(days - 1))`，终点是 `addDays(today, 1)`，`grain = grainFor(days)`，复用同文件的私有 `addDays`。结果按天取整，查询 key 当天稳定。
5. `queries.ts`：
   - `qk` 增加 `project: (id: string) => ["projects", id] as const` 和 `archivedProjects: ["projects", { archived: true }] as const`。两者都以 `qk.projects` 开头，现有的 `invalidateQueries({ queryKey: qk.projects })` 会连带失效它们。
   - 新增 `useArchivedProjects(enabled: boolean)`：`GET /projects?archived=true`。
   - `useArchivedThreads` 的签名改成 `(enabled: boolean, projectId?: string)`。给了 projectId 时，key 为 `["threads", { archived: true, project: projectId }]`，query 带 `project`；唯一的现有调用点 `ThreadsPane.tsx:367` 不用改。
6. `PrChip.tsx:21-24` 的 `CHIP` 和 `CHECK_TONE` 加上 `export`。
7. `icons.tsx` 加 `import { Terminal } from "@phosphor-icons/react/dist/csr/Terminal";`（`node_modules` 里已确认有这个文件），映射写 `/** 复制 git clone 命令（项目页头）；终端面板用 terminal */ clone: Terminal`。

验收：
- `cd web && bunx vitest run src/lib src/pages/projects/repo-label.test.ts src/components/stats/statsParams.test.ts` → 绿。
- 具体行为：
  - `repoLabel("https://github.com/acme/w.git")` → `"github.com/acme/w"`。
  - `daysWindow(14, now)` 的 `to - from` 等于 14 个本地日，`grain` 是 `"day"`。
  - 线程 `[{projectId:"p", lastMessageAt:5}, {projectId:"p", kind:"space", lastMessageAt:9}, {projectId:"p", archivedAt:1, lastMessageAt:8}]` → `get("p")` 是 `{threads:1, lastActiveAt:5}`。

### 步骤 1.5 — 页面外框 `ProjectShell` 与 `useProject`
依赖：1.4（`qk.project`）。
文件：新建 `web/src/pages/projects/ProjectShell.tsx`、`useProject.ts` / 修改 `web/src/pages/ProjectsPage.tsx`、语言文件 / 测试 新建 `pages/projects/ProjectShell.test.tsx`

做什么：
1. `useProject.ts`：`export type ProjectLookup = { status: "loading" } | { status: "missing"; error: unknown } | { status: "ok"; project: Project }`，以及 `export function useProject(id: string | undefined): ProjectLookup`。
   - 先查 `useProjects()` 的列表。列表里只有未归档项目（`projects.go:119-123`）。
   - 找不到且列表已经成功返回时，再发 `useQuery({ queryKey: qk.project(id), retry: false, queryFn: GET /api/v1/projects/{id} })`。归档项目就是这样按 id 拿到的，`GetProject` 不按归档过滤（`projects.go:224`）。
   - 返回 404 时是 `missing`，error 为 null；其它错误是 `missing` 且带上 error；列表本身出错也是 `missing` 并带 error。
2. `ProjectShell.tsx`：
   - `export interface Crumb { label: string; to?: string }`。
   - `export function ProjectShell({ project, crumbs = [], actions, wide = false, children }: { project?: Project; crumbs?: readonly Crumb[]; actions?: ReactNode; wide?: boolean; children: ReactNode }): ReactNode`。外框、页头、拖动区、`ChromeLead`、侧栏开关、`WindowControls` 原样搬自 `ProjectsPage.tsx:72-110`（`ProjectSheet`）。
   - 标题区是 `<nav aria-label={t("projectShell.breadcrumb")}>`。根节点「项目」（`projects.title`）是链到 `/projects` 的链接，compact 时 `hidden sm:inline-flex`。有 project 时接 `<ProjectBadge size={18}/> name`，有 crumbs 时它是链到 `/projects/<id>` 的链接，否则它就是 `<h1>`。各 crumb 之间用 `aria-hidden` 的 `/` 分隔，最后一个 crumb 是 `<h1>`，其余是链接。没有 project 也没有 crumbs 时显示 `folder` 图标加 `<h1>项目</h1>`，与今天的 `PlainTitle` 相同。
   - 正文容器：`mx-auto flex flex-col gap-3 ${wide ? "max-w-5xl" : "max-w-3xl"}`。
   - `project?.archivedAt` 时在正文最前面渲染 `<Callout tone="neutral" icon="archive" role="status" data-testid="project-archived" title=… actions=…>`。正文是 `projectShell.archivedBody`；动作按钮「取消归档」用 `useAsync` 发 PATCH `{archived:false}`，然后失效 `qk.projects`，出错时显示 `ErrorText`。
   - `export function ProjectGate({ children }: { children: (p: Project) => ReactNode }): ReactNode`：从 `useParams().projectId` 调 `useProject`。loading 时是 `<ProjectShell>` 加 Spinner（今天的 :181-184）；missing 时是今天 :186-196 的 markup，`data-testid="project-not-found"`，内容依次为 `ErrorText`、`projectsPage.notFound`、链回 `/projects` 的链接；ok 时 `<Fragment key={p.id}>{children(p)}</Fragment>`。
3. `ProjectsPage.tsx` 删掉 `ProjectSheet`、`PlainTitle`：
   - `ProjectsIndexPage` 改用 `<ProjectShell>`。
   - create 分支用 `<ProjectShell crumbs={[{ label: t("projects.new") }]}>`。
   - 详情页改为 `<ProjectGate>{(p) => <ProjectDetail project={p} />}</ProjectGate>`，`ProjectDetail` 外层改用 `<ProjectShell project={project} actions={…}>`。
   - 这样 `ProjectsPage.test.tsx` 里的 `heading level 1 name "oryn"` 不变。
4. 新文案：

| key | zh-CN | en |
|---|---|---|
| `projectShell.breadcrumb` | 位置 | Breadcrumb |
| `projectShell.archivedTitle` | 这个项目已归档 | This project is archived |
| `projectShell.archivedBody` | 它不在侧栏里，GitHub 仓库钩子已释放，预热 orb 已清零，定时任务与触发规则不再运行。线程都还在。 | It's hidden from the sidebar, its GitHub repository hook is released, warm orbs are at zero, and its schedules and triggers no longer run. Its threads are kept. |
| `projectShell.unarchive` | 取消归档 | Unarchive |

验收：
- `cd web && bunx vitest run src/pages/projects/ProjectShell.test.tsx src/pages/ProjectsPage.test.tsx` → 绿。
- 列表为空、`GET /projects/prj_old` 返回 `{…archivedAt:5}` 时：渲染 `project-archived`；点「取消归档」后记录到 `PATCH /api/v1/projects/prj_old {"archived":false}`。
- `GET` 返回 404 时：出现 `project-not-found`，里面不显示错误文字。

### 步骤 1.6 — 侧栏 `ProjectsPane`
依赖：1.3、1.4。
文件：修改 `web/src/components/layout/ProjectsPane.tsx`、语言文件 / 测试 `ProjectsPane.test.tsx`

做什么：
1. 重写组件：
   - 首行 `<div className="flex items-center gap-1 pr-1">` 里放 `<NavLink to="/projects" end onClick={closeOnCompact} className={LIST_LINK}>`，内容是 22px 槽位里的 `<GroupIcon name="folder"/>` 加 `projectsPane.overview`。
   - 首行行尾放 `<IconButton icon="plus" size={15} label={t("projects.new")} onClick={() => { navigate("/projects/new"); closeOnCompact(); }}/>`。
   - 其后每个未归档项目一个 `<NavLink to={`/projects/${p.id}`}>`，不加 `end`。NavLink 默认匹配子路径，所以在 `/projects/<id>/threads`、`/settings/*` 下仍然选中。
   - 行内容是 `ProjectBadge` 22、名称，副标题 `t("projectsPane.meta", { count, ago: formatRelative(lastActive(p, a), now) })`。数据来自 `projectActivity(Object.values(threads))` 和 `useNow()`。
   - 行顺序保持服务端的名称序（`projects.sql` 的 `ORDER BY name`），活动变化时行不跳。
2. 定时数不再显示：删掉 `useSchedules` 和 `scheduleCounts`，两个语言文件删除 `sidebar.projectMeta`（只有这里在用）。定时任务移到项目设置里。
3. 新文案：

| key | zh-CN | en |
|---|---|---|
| `projectsPane.overview` | 总览 | Overview |
| `projectsPane.meta_one` | {{count}} 个线程 · {{ago}}活跃 | {{count}} thread · active {{ago}} |
| `projectsPane.meta_other` | {{count}} 个线程 · {{ago}}活跃 | {{count}} threads · active {{ago}} |

4. `ProjectsPane.test.tsx`（MockServer）改写为：
   - 「首行是总览，链到 /projects 且只在 /projects 选中」。
   - 「行尾 + 去 /projects/new」。
   - 「未归档项目各一行，legacy-site 不在；在 /projects/<oryn>/settings/overview 下 Oryn 行 aria-current=page」。
   - 「副标题含『个线程 · 』与『活跃』」。

验收：
- `cd web && bunx vitest run src/components/layout/ProjectsPane.test.tsx` → 绿。
- mock 下停在 `/projects/<Oryn>/threads` 时 Oryn 行高亮。

### 步骤 1.7 — 总览页与新建页
依赖：1.2、1.4、1.5、1.6。
文件：新建 `web/src/pages/projects/ProjectsOverviewPage.tsx`、`ProjectCreatePage.tsx`、`projects-test-kit.tsx` / 修改 `web/src/App.tsx`、`web/src/pages/ProjectsPage.tsx`、语言文件、`web/e2e/ui-audit/projects.e2e.ts`、`docs/ui-audit/areas/sections.md`、`web/scripts/m14-accept.ts` / 测试 新建 `ProjectsOverviewPage.test.tsx`、`ProjectCreatePage.test.tsx`，修改 `ProjectsPage.test.tsx`、`App.test.tsx`

做什么：
1. 总览页 `export function ProjectsOverviewPage(): ReactNode`：
   - 外框：`<ProjectShell wide actions={…}>`。页头动作是 `Menu`（trigger 为 `<IconButton icon="more" label={t("projectsOverview.menu")}/>`，唯一一项 `{ key: "archived", label: t("projectsOverview.showArchived"), checked: showArchived, onSelect: toggle }`）和主按钮 `<Button variant="primary" size="sm" icon="plus" aria-label={t("projects.new")}>`，文字包在 `<span className="hidden sm:inline">` 里，照 `ProjectsPage.tsx:229-238` 的写法。
   - 「显示已归档」的状态放在 URL 的 `?archived=1`，用 `useSearchParams` 加 `setSp(…, { replace: true })`。理由：与 `ArtifactsPage.tsx:459` 的 URL 视图状态一致，分区会记住 `pathname + search`（`useSection.ts:95-96`），刷新和切分区回来都保持。不用 localStorage（`InboxPage` 的分组偏好是跨页面的长期偏好，与这里不同）。
   - 数据：`useProjects()`；`useArchivedProjects(showArchived)`；`projectActivity(Object.values(useIndex(s => s.threads)))`；用量用 `useUsageStats({ ...pick(daysWindow(14, Date.now()), "from", "to", "tz"), groupBy: "project", series: "day", limit: 500 })`（`stats-queries.ts:66`）。
   - 曲线：`values = series.map(pt => pt.groups.find(g => g.key === p.id)?.turns ?? 0)`。
   - 隐藏曲线的情况：没有 `series`（请求出错或未完成）；或顶层 `groups` 里没有 `p.id` 但有 `__other__`（被折叠了，数值不可信）。全零时画平线，意思是「安静」。
   - 行 `<Link to={`/projects/${p.id}`} data-testid="project-row" data-project={p.id} className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-md px-2 py-2.5 transition-colors duration-150 hover:bg-hover sm:grid-cols-[auto_minmax(0,1fr)_6rem_auto]">`：
     - `ProjectBadge` 28；
     - 名称（`font-medium truncate`），下一行 `text-faint text-xs truncate` 显示 `[p.description, p.repoUrl && repoLabel(p.repoUrl)].filter(Boolean).join(" · ") || t("projects.noRepo")`；
     - `<Sparkline className="hidden text-muted sm:block" values label={t("projectsOverview.activity", { name })}/>`；
     - 右列上下两行：`t("sidebar.projectThreads", { count })`（这个 key 已存在但还没有使用者，zh-CN.json:172）和 `t("projectsOverview.activeAgo", { ago })`。
   - 活跃项目放在一个无标题 `Card` 的 `ul.-mx-2` 里，按 `byActivity` 排序。归档区单独一张 `Card title={t("sidebar.archived")}`，行上加 `<Badge icon="archive">{t("sidebar.archived")}</Badge>`；归档列表为空时显示 `projectsOverview.noArchived`。
   - 没有活跃项目时：`EmptyState`，`data-testid="projects-empty"`，正文 `projectsPage.empty`，加主按钮「新建项目」。打开了归档开关时，归档卡照常显示在下面。
2. 新建页 `export function ProjectCreatePage(): ReactNode`：`<ProjectShell crumbs={[{ label: t("projects.new") }]}>` 里放 `<Card><ProjectEditor initial={EMPTY_PROJECT} onSaved={(p) => navigate(`/projects/${p.id}`, { replace: true })} onCancel={() => navigate("/projects")}/></Card>`。本步沿用现有的整张表单，1.9 改成精简表单。
3. 改 `App.tsx`：
   - `projects` → `ProjectsOverviewPage`，`projects/new` → `ProjectCreatePage`，两者都用 `lazy` 单独导入。
   - 删掉 `ProjectsIndexPage` 的 lazy 声明和 `ProjectPage create` 那条路由。
   - `ProjectsPage.tsx` 删掉 `ProjectsIndexPage` 和 `create` 分支。
   - `settings/projects` 的重定向（:150-153）保留，现在落到总览页。
4. `lib/sections.ts` 不用改：`sectionOf` 按前缀匹配，`:47` 的 `under(p, "/projects")` 覆盖所有 `/projects/...` 深路径；`sectionTarget`（:76-88）返回记住的 `pathname + search`，例如 `/projects/<id>/threads?status=archived`，否则回到 `defaultSectionPath("projects") = "/projects"`（:60-61），现在就是总览。
5. 测试：
   - 新建 `projects-test-kit.tsx`：把 `ProjectsPage.test.tsx` 里的 `ORYN`、`CORE`、`SETUP`、`orb()`、`handle()`、`Where`、`mount()` 搬过来并导出，原文件改为引入。`handle` 增加四类路由：`GET /projects?archived=true`（读 `archivedList`）；`GET /api/v1/threads`（按 `archived` 和 `project` 读 `threadList`）；`GET /stats/usage`（返回可写的 `usageStats`）；`GET` 和 `PUT /memory/projects*`。`mount` 的路由表与 App 里的项目路由同步。
   - 搬移用例（编号对应下方 1.9 第 5 条的映射表）：#1 删除；#2 移到 Overview；#5、#6、#23 移到 Create。
   - 总览页新增用例：
     - 按活动排序：CORE 有更新的线程 → CORE 行在前。
     - 请求参数：用量请求带 `groupBy=project&series=day&limit=500`，且 `to - from` 是 14 天。
     - 折叠：用量组里只有 `__other__` → 没有 `role=img` 名为「oryn 近 14 天每天的回合数」的曲线。
     - 归档开关：⋯ → 显示已归档项目 → `where` 是 `/projects?archived=1`，出现 legacy 行和「已归档」徽标；再点一次回到 `/projects`。
   - `App.test.tsx`：`vi.mock` 改成分别 mock `./pages/projects/ProjectsOverviewPage`（INDEX）和 `ProjectCreatePage`（CREATE）。`/projects/<id>` 这一条暂时仍 mock `./pages/ProjectsPage`。
6. `m14-accept.ts` 不用改：本步新建页仍是「保存」按钮（:605、:1977）。
7. ui-audit：`projects.e2e.ts` 增加 `projects.list.menu`（`/projects`，点「更多操作」后返回 `lastOverlay`）和 `projects.list.archived`（`/projects?archived=1`）；`sections.md` 的清单表在 `projects.list` 下面加两行，其中 `projects.list` 改为「项目总览」。
8. 新文案：

| key | zh-CN | en |
|---|---|---|
| `projectsOverview.menu` | 更多操作 | More actions |
| `projectsOverview.showArchived` | 显示已归档项目 | Show archived projects |
| `projectsOverview.activeAgo` | {{ago}}活跃 | Active {{ago}} |
| `projectsOverview.activity` | {{name}} 近 14 天每天的回合数 | Turns per day in {{name}} over the last 14 days |
| `projectsOverview.noArchived` | 没有已归档的项目。 | No archived projects. |

验收：
- `cd web && bunx vitest run src/pages src/App.test.tsx` → 绿。
- mock 下打开 `/projects`，看到 Oryn、oryn-core 两行，各带曲线和「N 个线程」，不再跳到第一个项目。

### 步骤 1.8 — 线程子页
依赖：1.5、1.7（测试工具）。
文件：新建 `web/src/pages/projects/ProjectThreadsPage.tsx`、`project-threads.ts`、`home/ProjectThreadRow.tsx` / 修改 `App.tsx`、语言文件、`projects.e2e.ts`、`sections.md` / 测试 新建 `ProjectThreadsPage.test.tsx`、`project-threads.test.ts`，修改 `App.test.tsx`

做什么：
1. `project-threads.ts`：
   - `export function mergeProjectThreads(index: Readonly<Record<string, Thread>>, archived: readonly Thread[] | undefined, projectId: string): Thread[]`：只要 `projectId` 相符且 `!isCoordinatorThread`；同一 id 以索引为准（实时）；按 `activityOf` 降序。
   - `export function useProjectThreads(projectId: string)`：返回 `{ threads, archivedPending: boolean, archivedError: unknown }`，内部是 `useIndex(s => s.threads)` 加 `useArchivedThreads(true, projectId)`。只拉本项目的归档线程（`GET /threads?archived=true&project=<id>`，见 `schema.d.ts:11645-11648`），不在客户端过滤全量归档列表。
   - `export type ThreadStatusFilter = "all" | "active" | "archived" | "shipped"`。
   - `export const STATUS_FILTERS: Record<ThreadStatusFilter, (t: Thread) => boolean> = { all: () => true, active: (t) => t.archivedAt === undefined, archived: (t) => t.archivedAt !== undefined, shipped: (t) => t.shippedAt !== undefined }`。各档允许重叠，已交付的线程同时出现在「活跃」或「已归档」里。
   - `export function matchesQuery(t: Thread, q: string): boolean`：按空白切词，每个词（转小写）都要命中 `parseTitleCard(t.title).text` 或 `t.labels` 中的某一项。
2. `ProjectThreadRow({ thread, now }: { thread: Thread; now: number })`：新组件。侧栏的 `ThreadRow`（`ThreadsPane.tsx:208`）不复用：它绑着侧栏的 `--sb-*` 样式变量、`closeOnCompact`、NavLink 选中态和 fork 子树递归，链接的 textContent 也必须只等于标题（:8）；而这里是纸面上的行，还要显示交付和归档徽标。
   - 行是 `<li><Link to={`/t/${id}`} data-testid="project-thread-row" data-thread={id} className="flex min-h-10 items-center gap-3 rounded-md px-2 py-1.5 text-sm transition-colors duration-150 hover:bg-hover">`。
   - 行内依次为：`ThreadStateIcon state size=14`；标题（`parseTitleCard(...).text || t("thread.untitled")`，`min-w-0 flex-1 truncate`）。
   - PR 摘要：取 `pulls` 里第一个 open 的，没有就取第一个。显示 `Icon prIcon(pull)` 12、`#number`；有 `checks` 时加 `<StatusDot tone={CHECK_TONE[checks]} label={t(`thread.pr.checks.${checks}`)}/>`；`title={prStatusText(pull, t)}`（`PrChip.tsx:36`）。
   - 徽标：`shippedAt` 时 `<Badge tone="ok" icon="checkCircle">{t("projectHome.shipped")}</Badge>`；`archivedAt` 时 `<Badge icon="archive">{t("sidebar.archived")}</Badge>`。
   - 最后是 `text-2xs text-faint tabular-nums` 的 `formatRelative(activityOf(thread), now)`。
3. `ProjectThreadsPage`：`<ProjectGate>{(p) => <ProjectShell project={p} crumbs={[{ label: t("projectThreads.title") }]} actions={<NewThreadButton project={p}/>}>…}`。`NewThreadButton` 在本步先建在 `home/HeaderActions.tsx`，定义见 1.9。
   - 工具行：`<TextInput type="search" aria-label={t("projectThreads.search")} placeholder=…>`，本地 state 初值取 URL 的 `q`；`useDebounced(text, 200)` 之后写回 URL（`replace`，空串时删掉参数）；URL 被外部改变时同步回本地 state。旁边是分段按钮（`aria-label={t("projectThreads.statusLabel")}`），值写进 `?status=`，`all` 时删掉参数，非法值按 `all` 处理。
   - 列表：`threads.filter(STATUS_FILTERS[status]).filter(t => matchesQuery(t, q))`，放在无标题 `Card` 的 `ul.-mx-2` 里，`useNow()` 作基准时刻。
   - 状态：
     - 项目一个线程都没有：`EmptyState icon="chats" title={t("projectHome.noThreads")}`。
     - 有线程但被筛空：`EmptyState icon="search" title={t("projectThreads.noMatch")}`，加按钮「清除筛选」，把 URL 恢复成不带 search。
     - 归档列表还在加载且状态不是 active：列表下方显示 `Spinner`。
     - `archivedError`：显示 `ErrorText`，索引里的线程照常列出。
4. `App.tsx` 加 `projects/:projectId/threads`，`App.test` 加 mock 和对应用例。
5. ui-audit 加 `projects.threads`（`/projects/<PRJ>/threads`）和 `projects.threads.archived`（`?status=archived`），`sections.md` 同步两行。
6. 新文案：

| key | zh-CN | en |
|---|---|---|
| `projectThreads.title` | 线程 | Threads |
| `projectThreads.search` | 搜索标题与标签 | Search titles and labels |
| `projectThreads.statusLabel` | 状态 | Status |
| `projectThreads.status.all` | 全部 | All |
| `projectThreads.status.active` | 活跃 | Active |
| `projectThreads.status.archived` | 已归档 | Archived |
| `projectThreads.status.shipped` | 已交付 | Shipped |
| `projectThreads.noMatch` | 没有符合条件的线程。 | No threads match. |
| `projectThreads.clear` | 清除筛选 | Clear filters |
| `projectHome.noThreads` | 这个项目还没有线程。 | No threads in this project yet. |
| `projectHome.shipped` | 已交付 | Shipped |
| `sidebar.newThread`（复用） | 新建线程 | New thread |

验收：
- `cd web && bunx vitest run src/pages/projects` → 绿。
- 输入数据：索引里有 T1（ORYN，标签 `bug`）、T2（ORYN，`shippedAt`）、一个协调线程（ORYN）、一个 CORE 线程；`/threads?archived=true&project=prj_a` 返回 T3。
  - 打开 `/projects/prj_a/threads` → 3 行。
  - 点「已归档」→ URL 是 `?status=archived`，只剩 T3。
  - 输入 `bug` 并推进 200ms → URL 是 `?status=archived&q=bug`，显示 noMatch；点「清除筛选」→ 3 行。

### 步骤 1.9 — 项目主页与设置子页切换，删除 `ProjectsPage.tsx`
依赖：1.4–1.8。
文件：
- 新建 `pages/projects/ProjectHomePage.tsx`、`home/ProjectIntro.tsx`、`home/StatusStrip.tsx`、`home/RecentThreadsCard.tsx`、`home/ServicesCard.tsx`、`project-orbs.ts`、`settings/ProjectSettingsPage.tsx`、`settings/OverviewTab.tsx`、`EnvironmentTab.tsx`、`AgentTab.tsx`、`GitHubTab.tsx`、`SchedulesTab.tsx`、`SshTab.tsx`
- 修改 `home/HeaderActions.tsx`、`ProjectEditor.tsx`、`ProjectGates.tsx`、`pages/settings/agent/memory/ToggleCard.tsx`、`ProjectCreatePage.tsx`、`App.tsx`、`useSection.ts`（:89 注释）、语言文件、`projects.e2e.ts`、`sections.md`、`web/scripts/screenshots.ts`、`m96-accept.ts`、`m9-accept.ts`、`m14-accept.ts`、`m122-accept.ts`
- 删除 `web/src/pages/ProjectsPage.tsx`、`ProjectsPage.test.tsx`
- 测试 新建 `ProjectHomePage.test.tsx`、`settings/ProjectSettingsPage.test.tsx`；修改 `ProjectGates.test.tsx`、`RepoPicker.test.tsx`、`SettingsM9.test.tsx`、`App.test.tsx`

做什么：

**1. `project-orbs.ts`**：把 `latestSetupOrb`（`ProjectsPage.tsx:295-306`）和 orb 的 `activityOf`（:435-441，改名 `orbActivity`）原样搬过来并导出；新增 `export type SetupOrb = Orb & { lastSetup: SetupRun }`。

**2. 页头动作（`home/HeaderActions.tsx`）**，每个组件都是 `({ project }: { project: Project })`：
- `CopyLinkButton`：`IconButton icon={copied ? "check" : "link"} label={t("projectShell.copyLink")}`，复制 `${location.origin}/projects/${id}`（与 `ThreadHeader.tsx:445` 拼链接的方式相同）。
- `CloneButton`：`!project.repoUrl` 时返回 null；否则 `IconButton icon={copied ? "check" : "clone"} label={t("projectShell.copyClone")}`，复制 `git clone ${repoUrl}`。
- `SettingsButton`：`IconButton icon="settings" label={t("projectShell.settings")}`，点击 `navigate(`/projects/${id}/settings`)`。宽屏会再重定向到 overview，compact 进入列表页。
- `NewThreadButton`：`project.archivedAt` 时返回 null；否则 `<Button variant="primary" size="sm" icon="plus" aria-label={t("sidebar.newThread")}>`，文字只在 `sm:` 以上显示；点击时 `uiStore.getState().seedHome({ text: "", projectId: id }); navigate("/")`。空正文不需要改首页：`HomePage.tsx:283-294` 只判断 seed 对象是否存在，然后 `setPrompt("")`、`setProjectId`；首页的 `prompt` 不持久化（`:201`）。
- 复制失败一律 `showToast({ text: t("projectShell.copyFailed", { text }), tone: "err" })`。

**3. 主页 `ProjectHomePage`**：`<ProjectGate>{(p) => <ProjectShell project={p} wide actions={<>CopyLink Clone Settings NewThread</>}><div data-testid="project-home" className="flex flex-col gap-3">…`，子元素依次如下。

- **`ProjectIntro`**：有描述时显示 `<p className="text-muted text-sm">{description}</p>`。
  - 仓库行 `<p data-testid="project-repo" className="flex flex-wrap items-center gap-x-3 gap-y-1 text-faint text-xs">`：图标在 `isGitHubRepo` 时用 `github`，否则 `code`；地址是 `font-mono` 的 `repoUrl` 经 `repoLabel` 处理后的结果；有 `baseBranch` 时再加 `branch` 图标和分支名，`title={t("projects.baseBranch")}`。
  - 没有仓库时显示 `folder` 图标加 `projects.noRepo`（这个 key 已存在但还没有使用者）。
  - 阶段 ② 把同步状态追加为这个 `<p>` 的最后一个子元素。

- **`StatusStrip`**（`data-testid="project-status"`，`role="group" aria-label={t("projectHome.status")}`）：
  - 统计范围是索引里本项目、非协调、未归档的线程。
  - 每个胶囊是 `<Link className={`${CHIP} border-border bg-panel/60 text-fg hover:bg-hover`} data-chip=…>`。
  - `awaiting`：`state === "awaiting_input"`，图标 `ThreadStateIcon state="awaiting_input" size={12}`。
  - `working`：`state === "working" || "provisioning"`，口径与收件箱的 running 一致（`lib/inbox.ts` 的 `inboxStateOf`），图标 `ThreadStateIcon state="working" size={12}`。
  - `prs`：按 `${repo}#${number}` 去重后 `state === "open"` 的 PR，图标 `pullRequest`；其中 `checks === "failure"` 的数量大于 0 时，在同一个胶囊里追加 `text-err` 的「· N 个 CI 失败」。
  - 上面三种都链到 `/projects/<id>/threads?status=active`。
  - `setup`：对本项目、未删除的 orb 取 `latestSetupOrb`，`lastSetup.ok === false` 时显示，图标 `xCircle`，文字 `text-err`；有 `threadIds[0]` 时链到 `/t/<它>`，否则链到 `settings/environment`。
  - `usage`：`useUsageStats({ ...statsWindow({ range: "7d" }, Date.now()) 的 from/to/tz, groupBy: "total", project: id })`，用 `total.costUsd` 加 `formatCost`、`total.turns`，图标 `chart`（不加美元图标），链到 `/stats/usage?${statsSearch({ ...DEFAULT_PARAMS, range: "7d", project: id })}`，也就是 `range=7d&project=<id>`（`statsParams.ts` 的 `statsSearch`）。请求出错、未完成，或成本和回合都是 0 时不显示。
  - 数值为 0 的胶囊不显示。全部隐藏时整个 strip 不渲染、不占位，下一块直接上移。

- **列容器**：`<div data-testid="project-home-columns" className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,24rem),1fr))] items-start gap-3">`，本阶段只有 `RecentThreadsCard` 一个子元素。`auto-fit` 会折叠空轨道，所以单个子元素占满整行。阶段 ② 只需加第二个兄弟元素 `RecentCommitsCard`：容器宽度 ≥ 2×24rem+gap 时自动排成两列，窄时堆叠，类名不改。

- **`RecentThreadsCard`**：`<Card title={t("projectHome.recentThreads")} icon="chats" action={<Link to="threads">{t("projectHome.viewAll")}</Link>}>`，`data-testid="project-recent"`。取 `useProjectThreads(id).threads.slice(0, 5)`，含归档线程，所以安静的项目不会空着。没有线程时显示 `projectHome.noThreads` 和一个小号的 `NewThreadButton`。

- **`ServicesCard`**：今天 `ProjectsPage.tsx:378-432` 的逻辑原样搬过来，数据是 `useOrbs()` 里 `projectId` 相符的 orb。`rows.length === 0` 时返回 null，删掉 `project-services-empty` 和 `projectsPage.noServices`。外层 Card 加 `data-testid="project-services"`，标题沿用 `projectsPage.services`。

- 阶段 ② 把「文件」区块和 README 卡插在列容器之后、`ServicesCard` 之前。

**4. `ProjectEditor` 拆分**（`pages/projects/ProjectEditor.tsx`）：
- 签名：`export type ProjectSection = "create" | "overview" | "environment"`；`ProjectEditor({ section, initial, id, lastSetup, onSaved, onCancel }: { section: ProjectSection; initial: ProjectForm; id?: string; lastSetup?: SetupRun; onSaved(p: Project): void; onCancel(): void })`。
- 各 section 的字段：
  - create：name、icon、description、repoUrl + `RepoPicker`、baseBranch。
  - overview：create 的全部字段，加 git 作者名与邮箱和 `gitHint`/`gitPairError`。
  - environment：defaultProvider、defaultOrbType、默认浏览器 Profile、shallowClone、`SnapshotsToggle`、`WarmOrbsStepper`、setupScript、`RepoScriptText`、resumeScript。
- description 用 `<Field label={t("projects.description")} hint={t("projects.descriptionHint")}><TextInput maxLength={200} …/></Field>`，占 `sm:col-span-2`。
- 提交：
  - create：`POST {name, shallowClone: false, ...非空的(icon, description, repoUrl, baseBranch)}`。生成类型要求 `shallowClone`（`schema.d.ts:5607`）。省略的字段由服务端补默认值：shallowClone=false（`projects.go:199`）、snapshots 打开、warmOrbs=0（`projects.sql` 的 COALESCE）、provider、类型、脚本、Profile 为 NULL（继承全局），图标留空，之后由首个线程标题卡自动填入（`0008` 迁移）。
  - overview：`PATCH {name, description, repoUrl, baseBranch, icon, gitAuthorName, gitAuthorEmail}`，未填一律发 `""`（清除）。
  - environment：`PATCH {defaultProvider, defaultOrbType, defaultBrowserProfileId, setupScript, resumeScript}` 未填发 `""`，另带 `shallowClone`、`snapshots: f.snapshots ?? true`、`warmOrbs: f.warmOrbs ?? 0`。
  - 服务端只改出现的字段，snapshots 和 warmOrbs 为 nil 时保持不变（`projects.go:332`、`projects.sql` 的 COALESCE），所以拆开保存是安全的。`color` 不再发送。
- `EMPTY_PROJECT` 改为 `{ name: "", shallowClone: false }`；`formOf` 增加 `description: p.description`。
- 提交按钮：create 显示「创建」（`common.create`），其它显示「保存」。
- 删掉门禁区和 `useProjects` 的查找，`ProjectGates` 不再从这里引入。
- 重置方式沿用今天的写法：父组件给每张表单 `key={`${project.updatedAt}:${rev}`}`，取消时 `rev + 1`（`ProjectsPage.tsx:211-212,272-279`）。`ProjectCreatePage` 改为 `section="create"`。

**5. 门禁**：在 `ProjectGates.tsx` 新增 `export function ProjectGatesCard({ project, onCancel }: { project: Project; onCancel(): void })`。`!project.github` 时返回 null；否则是 Card 里的 `ProjectGates`，值取 `gates ?? project.githubGates ?? DEFAULT_GATES`。「保存」在 `gates === null` 或 busy 时禁用，提交 `PATCH {githubGates: normalizeGates(gates)}` 后失效 `qk.projects`；「取消」调用 `onCancel`。

**6. 设置页 `ProjectSettingsPage`**：
- `export const SETTINGS_TABS = ["overview", "environment", "agent", "github", "schedules", "ssh"] as const`，`export type ProjectSettingsTab = (typeof SETTINGS_TABS)[number]`。阶段 ④ 追加 `secrets`。
- 页面：`<ProjectGate>` 包 `<ProjectShell project wide crumbs={[{ label: t("projectSettings.title"), to: `/projects/${id}/settings` }, …(tab ? [{ label: t(`projectSettings.tabs.${tab}`) }] : [])]}>`。
- 版式规则：`const compact = useBreakpoint() === "compact"`（`lib/viewport.ts:28`）。
  - 没有 tab 且 `!compact`：`<Navigate to={`/projects/${id}/settings/overview`} replace/>`。
  - tab 不合法：重定向到 overview。
  - `compact` 且没有 tab：列表页 `<Card data-testid="project-settings-list">`，每组一行 `Link`（`flex min-h-11 items-center gap-3 rounded-md px-2 text-sm hover:bg-hover`），左边图标，右边 `chevronRight`。
  - `compact` 且有 tab：只显示该组内容，靠面包屑里的「设置」返回列表。
  - medium 和 expanded：`<div data-testid="project-settings" className="grid grid-cols-[11rem_minmax(0,1fr)] items-start gap-5">`，左边是 `<nav data-testid="project-settings-nav" aria-label={t("projectSettings.nav")}>` 和若干 `NavLink`（`flex h-8 items-center gap-2 rounded-md px-2 text-sm ${isActive ? "bg-active font-medium text-fg" : "text-muted hover:bg-hover hover:text-fg"}`），右边是 `flex min-w-0 flex-col gap-3` 的内容区。
- 导航图标：概览 `info`、环境 `wrench`、Agent `robot`、GitHub `github`、定时任务 `clock`、SSH 主机 `hardDrives`。后两个与 `more-entries.ts` 里的 automations（:23）、agent-ssh（:103）一致。

**7. 各分组内容**（每个 Tab 组件都是 `({ project }: { project: Project })`）：
- **Overview**：`Card title={t("projectSettings.general")} icon="settings"` 里放 `ProjectEditor section="overview"`。
  - 下面是 `Card title={t("projectSettings.danger")} icon="alert" data-testid="project-danger"`，有两行。
  - 第一行：归档。未归档时是 `Button icon="archive"`「归档」（`common.archive`）：先 `confirmAction({ title: t("projectSettings.archiveConfirm", { name }), body: t("projectSettings.archiveConfirmBody"), action: t("common.archive") })`，确认后 `PATCH {archived:true}`。已归档时这一行显示 `unarchiveHint` 和「取消归档」按钮，不需要确认，因为操作可逆。
  - 第二行：删除，走现有流程（`projects.deleteConfirm`，`danger`），新增 `body: t("projectSettings.deleteHint")`。删除后失效 `qk.projects` 并 `navigate("/projects", { replace: true })`。
  - 删除说明的依据：定时任务随项目级联删除（`0015_spaces_schedules.sql:30`）；项目配置和记忆被清理（`projects.go:454-465`）。
- **Environment**：`Card title={t("projectSettings.envForm")} icon="settings"` 里放 `ProjectEditor section="environment" lastSetup={setupOrb?.lastSetup}`；下面是 `EnvironmentCard`（从 `ProjectsPage.tsx:308-372` 原样搬进本文件）和 `AccelCard`。
- **Agent**：`ProjectAgentConfigCard`；下面是 `Card title={t("projectSettings.memory.title")} icon="brain"`。
  - 数据用 `useMemoryProjects`、`usePutMemoryProject`。
  - 加载中显示 `Spinner`；出错显示 `<MemoryError step="loadProjects"/>`。
  - 列表里找不到本项目：只有归档项目会这样，因为列表里是每个未归档项目（`ToggleCard.tsx:2`），此时显示 `projectSettings.memory.archived`。
  - 找到时显示 `memoryPage.projects.hint` 和 `<ul className="-mx-2"><MemoryProjectRow …/></ul>`。`ToggleCard.tsx` 里的 `ProjectRow` 改名为 `MemoryProjectRow` 并导出，原调用点（:40）同步改。
- **GitHub**：`!project.github && !project.repoUrl?.startsWith("https://github.com/")` 时，显示 `EmptyState icon="github" title={t("projectSettings.github.noneTitle")}`，正文 `noneBody`，按钮 `editRepo` 去 overview。这个条件与 `ProjectGitHub.tsx:149` 让卡片返回 null 的条件相同。否则显示 `ProjectGitHubCard`，下面是带 key 重置的 `ProjectGatesCard`。
- **Schedules**：`<Card><ProjectSchedules project/></Card>`，与今天 `ProjectsPage.tsx:285-287` 相同。
- **SSH**：`ProjectSshCard`。

**8. 路由与注释**：
- `App.tsx` 加 `projects/:projectId` → Home、`projects/:projectId/settings` 和 `projects/:projectId/settings/:tab` → Settings，都是 lazy；删掉 `./pages/ProjectsPage` 的引用。
- `useSection.ts:89` 的注释，把「`/projects` → `/projects/<id>`」改为「`/projects/<id>/settings` → `…/settings/overview`」。
- 命令面板不用改：项目候选本来就跳 `/projects/${p.id}`（`CommandPalette.tsx:283`），现在就是主页。

**9. 测试搬移**（`ProjectsPage.test.tsx` 实际是 23 个用例，不是 24；本步删除整个文件）：

| # | 原用例 | 去向 |
|---|---|---|
| 1 | 有未归档项目跳到第一个 | 1.7 删除（重定向已取消） |
| 2 | 没有项目的空状态 | 1.7 → Overview |
| 3 | 编辑保存的 PATCH 体 | → Settings/overview，期望体为 `{name:"oryn2",description:"",repoUrl:"https://github.com/x/oryn",baseBranch:"",icon:"\u{f07d3}",gitAuthorName:"",gitAuthorEmail:""}`，面包屑里的项目链接文字变为 oryn2 |
| 4 | 编辑后取消还原 | → Settings/overview |
| 5、6、23 | 新建跳转、新建取消、新建页无 Agent 卡 | 1.7 → Create。本步 #5 的 POST 体改为 `{name:"fresh",shallowClone:false}`；#23 另断言没有 `project-gates` |
| 7 | 字形与「自动」 | → Settings/overview |
| 8 | 浏览器 Profile | → Settings/environment |
| 9、10 | 删除与取消删除 | → Settings/overview 危险区，确认后 `where` 为 `/projects` |
| 11 | 不存在的 id | → Home（`/projects/prj_gone` 返回 404） |
| 12 | 查看线程 | 1.3 已删 |
| 13 | 定时任务卡 | → Settings/schedules |
| 14 | 环境与服务都为空 | 拆开：环境空 → Settings/environment；`project-services` 不存在 → Home |
| 15 | setup 与服务行 | 拆开：环境 → Settings/environment；服务行 → Home |
| 16–21 | 快照与预热 6 例 | → Settings/environment |
| 22 | Agent 配置 | → Settings/agent |

**10. 其它测试改动**：
- 新增 Home 用例：
  - Clone 复制 `git clone https://github.com/x/oryn`；没有 repoUrl 时没有 Clone 按钮。
  - 索引里 awaiting、working、provisioning 各一个，外加一个 open 且 checks 为 failure 的 PR → 胶囊文字分别是「1 个等你回复」「2 个工作中」「1 个打开的 PR · 1 个 CI 失败」。
  - 用量返回 `costUsd 1.5`、`turns 12` → 文字「近 7 天 $1.50 · 12 个回合」，链接是 `/stats/usage?range=7d&project=prj_a`。
  - 全部为 0 → 没有 `project-status`。
  - 7 个线程 → 5 行，其中含归档线程。
  - 「新建线程」→ `uiStore.getState().homeSeed` 等于 `{text:"", projectId:"prj_a"}`，`where` 是 `/`。
  - 归档项目 → 有 banner，没有「新建线程」。
- 新增 Settings 用例：
  - `innerWidth=1280` 打开 `/settings` → 落到 `/settings/overview`。
  - `innerWidth=390` → 显示 6 行的列表，点「环境」进入。
  - `/settings/bogus` → 落到 overview。
  - 归档确认框标题是「归档项目 oryn？」，确认后 `PATCH {archived:true}`，取消则不发请求。
  - CORE（没有仓库）的 GitHub 分组显示空态。
  - 记忆开关 → `PUT /memory/projects/prj_a {enabled:true}`。
- `ProjectGates.test.tsx` 改为渲染 `ProjectGatesCard`：
  - 「没改过时保存不可用」替换原 :89-98；
  - :100-147 的两例照旧，比较的是 PATCH 体里的 `githubGates`；
  - 「非 GitHub 项目没有门禁区」保留；
  - 「新建项目（无 id）」删除，新建页不含门禁这件事已由 #23 覆盖。
- `RepoPicker.test.tsx:82` 加 `section="create"`，:73-75 的 projects 桩和注释删掉。
- `SettingsM9.test.tsx:11,55-57` 改为 `ProjectSettingsPage`，路由 `/projects/:projectId/settings/:tab`，初始地址 `/projects/${oryn.id}/settings/schedules`，:2 的注释同步改。
- `App.test.tsx`：
  - mock 换成五个新模块；Home 的 mock 在 `projectId === "boom"` 时抛 `TypeError`，保住 :140 以后的错误边界用例。
  - 用例覆盖：`/projects/prj_1` → HOME；`/projects/prj_1/settings` 和 `/projects/prj_1/settings/github` → SETTINGS，并带上 tab。

**11. 文案删除**：两个语言文件删除 `projectsPage.settings`、`projectsPage.noServices`。

**12. ui-audit 与截图**：
- `projects.e2e.ts` 删除这些 surface：`projects.page`、`projects.page.settings-bottom`、`.agent-config`、`.github`、`.ssh`、`.accel`、`.schedules`、`.repo-picker`、`.trigger-dialog`、`.trigger-dialog.model`、`.schedule-menu`、`.schedule-new`、`.schedule-edit`。
- 新增 surface：`projects.home`、`projects.home.bottom`（滚到底）、`projects.archived`（`/projects/prj_01JCZ000000000000000LEGACY`）、`projects.settings.nav`（`only: "mobile"`）、`projects.settings.overview`、`projects.settings.overview.danger`（`cardAt("危险操作")`）、`projects.settings.repo-picker`、`projects.settings.environment`、`projects.settings.accel`、`projects.settings.agent`、`projects.settings.github`、`projects.settings.trigger-dialog`、`projects.settings.trigger-dialog.model`、`projects.settings.schedules`、`projects.settings.schedule-menu`、`projects.settings.schedule-new`、`projects.settings.schedule-edit`、`projects.settings.ssh`。
- 新 surface 的 `open` 和 `ready` 沿用旧逻辑，只把 path 换成对应分组的地址。
- `sections.md` 清单表的 `projects.*` 行按上面替换。表下的「问题记录」是历史记录，不改。
- `screenshots.ts`：
  - :516 改为 `/settings/schedules`；
  - :600、:606 改为 `/settings/github`；
  - :1858、:1870 改为 `/settings/environment`；
  - 在 :1245 之后新增 `projects-overview`、`projects-home`、`projects-threads`、`projects-settings` 四个条目，就绪标记分别是 `[data-testid=projects-overview]`、`project-home`、`project-threads`、`project-settings`（390 宽时等 `project-settings-list`）。

**13. 验收脚本**：
- `m96-accept.ts:3271-3290` 改为：点侧栏项目行 → `waitPath(/projects/${id})` → 等 `project-home` 和文字 `zh.projectHome.recentThreads` → 点 `zh.projectShell.settings` → `waitPath(…/settings/overview)` → 点链接 `zh.projectSettings.tabs.environment` → `waitPath(…/settings/environment)`。:3291 以后的填写与 GET 校验不变。
- `m9-accept.ts:2996` 改为 `/projects/${pid}/settings/schedules`。
- `m14-accept.ts`：
  - :543 改为 `/settings/github`；
  - :605 和 :1977 的「保存」改为「创建」；
  - :618 前插入 ``await goto(p, MOCK_BASE, `${new URL(p.url()).pathname}/settings/github`)``；
  - :1984 前插入 ``await goto(p, REAL_UI, `/projects/${projectId}/settings/github`)``；
  - :703 改为 `/settings/github`。
- `m122-accept.ts:1145` 改为 `/projects/${a.id}/settings/agent`。

**14. 新文案**：

| key | zh-CN | en |
|---|---|---|
| `projects.description` | 描述 | Description |
| `projects.descriptionHint` | 一行说明，显示在项目总览和项目主页上（最多 200 字）。 | One line shown on the projects overview and project home (up to 200 characters). |
| `projectShell.copyLink` | 复制项目链接 | Copy project link |
| `projectShell.copyClone` | 复制 git clone 命令 | Copy git clone command |
| `projectShell.copyFailed` | 没能复制到剪贴板（浏览器不允许访问剪贴板）：{{text}} | Couldn't copy to the clipboard (the browser blocked access): {{text}} |
| `projectShell.settings` | 项目设置 | Project settings |
| `projectHome.status` | 项目状态 | Project status |
| `projectHome.recentThreads` | 最近线程 | Recent threads |
| `projectHome.viewAll` | 查看全部 | View all |
| `projectHome.chip.awaiting_one/_other` | {{count}} 个等你回复 | {{count}} waiting for you |
| `projectHome.chip.working_one/_other` | {{count}} 个工作中 | {{count}} working |
| `projectHome.chip.prs_one` / `_other` | {{count}} 个打开的 PR | {{count}} open PR / {{count}} open PRs |
| `projectHome.chip.ciFailing_one/_other` | {{count}} 个 CI 失败 | {{count}} failing CI |
| `projectHome.chip.setupFailed` | 上次环境配置失败 | Last setup failed |
| `projectHome.chip.usage_one` / `_other` | 近 7 天 {{cost}} · {{count}} 个回合 | Last 7 days {{cost}} · {{count}} turn / turns |
| `projectSettings.title` | 设置 | Settings |
| `projectSettings.nav` | 设置分组 | Settings sections |
| `projectSettings.tabs.overview` / `environment` / `agent` / `github` / `schedules` / `ssh` | 概览 / 环境 / Agent / GitHub / 定时任务 / SSH 主机 | Overview / Environment / Agent / GitHub / Schedules / SSH hosts |
| `projectSettings.general` | 基本信息 | General |
| `projectSettings.envForm` | 默认环境与脚本 | Defaults and scripts |
| `projectSettings.danger` | 危险操作 | Danger zone |
| `projectSettings.archive` | 归档项目 | Archive project |
| `projectSettings.archiveHint` | 从侧栏收起，停用它的 GitHub 钩子、预热 orb、定时任务与触发规则；线程保留，随时可以取消归档。 | Hide it from the sidebar and stop its GitHub hook, warm orbs, schedules and triggers. Threads are kept; unarchive any time. |
| `projectSettings.archiveConfirm` | 归档项目 {{name}}？ | Archive project {{name}}? |
| `projectSettings.archiveConfirmBody` | GitHub 仓库钩子会被释放，预热 orb 清零，定时任务与触发规则停止运行；线程不受影响，仍可在这个项目里新建线程。 | Its GitHub hook is released, warm orbs go to zero, and schedules and triggers stop. Threads are untouched and you can still start threads in it. |
| `projectSettings.unarchiveHint` | 项目回到侧栏，定时任务与触发规则重新生效。 | The project returns to the sidebar and its schedules and triggers take effect again. |
| `projectSettings.delete` | 删除项目 | Delete project |
| `projectSettings.deleteHint` | 线程保留但不再属于任何项目；定时任务、Agent 配置与项目记忆一并删除，不可恢复。 | Threads are kept but leave the project; its schedules, agent config and project memory are deleted for good. |
| `projectSettings.memory.title` | 项目记忆 | Project memory |
| `projectSettings.memory.archived` | 已归档的项目不在记忆的项目列表里，取消归档后才能设置。 | Archived projects aren't in memory's project list; unarchive to change this. |
| `projectSettings.github.noneTitle` | 仓库不在 GitHub 上 | Not a GitHub repository |
| `projectSettings.github.noneBody` | 把仓库地址改成 https://github.com/<owner>/<repo>，这里就能设置事件回流、触发规则与推送门禁。 | Set the repository URL to https://github.com/<owner>/<repo> to configure event forwarding, triggers and push gates here. |
| `projectSettings.github.editRepo` | 改仓库地址 | Change repository URL |

验收：
- `cd web && bun run check && bun run test` → 绿。
- 下面这条命令输出为空：`rg -n "pages/ProjectsPage|ProjectsIndexPage|ProjectDetail|projectsPage\.(settings|noServices)|projects\.page\b" web/src web/e2e web/scripts docs/ui-audit/areas`。

### 步骤 1.10 — 文档
依赖：1.9。
文件：修改 `docs/features.md`、`docs/design/ui-redesign.md`、`README.md`

做什么：
1. `docs/features.md`：在「工作台」一章（命令面板所在的那一章）加「### 项目」小节，写清：
   - 总览：每个项目的线程数、近 14 天活动曲线、最后活跃时间；`⋯` 菜单里显示已归档项目。
   - 项目主页：描述与仓库行、状态条每一项的含义与点击去向、最近线程、服务块。
   - 线程子页：搜索范围（标题与标签）与四档状态。
   - 设置的六个分组各管什么。
   - 归档与取消归档的效果（侧栏、GitHub 钩子、预热 orb、定时任务与触发规则、线程）。
2. `docs/features.md` 里「项目级中央配置」一节的「在项目页的『Agent 配置』卡片里编辑」改为「在项目设置的『Agent』分组里编辑」；`rg -n '项目页' docs/features.md README.md` 的其它命中逐条按新位置改写。
3. `docs/design/ui-redesign.md`：功能落位表里描述项目行过滤按钮与齿轮的条目（`rg -n '过滤|查看线程' docs/design/ui-redesign.md`）改成新的侧栏与页面结构。
4. `README.md`：功能列表里关于项目的描述与新界面一致。

验收：`rg -n '查看线程|按项目过滤|无项目的线程' docs/features.md docs/design README.md` 输出为空。

## 阶段 ① 验收

命令：
1. `make gen && go test ./internal/api/... ./internal/orbs/... ./internal/store/...` → 全部 `ok`；提交后跑 `make check-gen` → 无差异。
2. `cd web && bun run check && bun run test` → 绿。
3. `cd web && npx playwright test e2e/ui-audit --grep projects` → 新登记的 surface 全部通过。
4. `cd web && ORYN_SHOTS_DIR=/tmp/oryn-shots/projects-p1 bun run shots:all --only projects` → 生成 1280 和 390 宽、明暗两种主题的截图。
5. `rg` 检查：1.3 与 1.9 的两条命令都输出为空。

在 `cd web && bun run dev:mock` 下手动点一遍：
1. 底栏或侧栏「项目」→ 停在 `/projects`。Oryn 和 oryn-core 两行，带描述、`github.com/...`、「N 个线程」、14 天曲线和「…活跃」。侧栏首行「总览」高亮，没有过滤按钮和「无项目的线程」。
2. ⋯ → 显示已归档项目 → URL 变成 `?archived=1`，出现「已归档」区和 legacy-site 行。切到线程分区再切回来，归档区仍在。
3. 点 Oryn → 主页依次是描述、仓库行（GitHub 图标、`github.com/oryn-demo/webapp`、`main`）、状态胶囊、「最近线程」（5 行，占满整行）、服务卡。点 Clone 图标，图标变成 ✓，剪贴板里是 `git clone https://github.com/oryn-demo/webapp`。
4. 点「新建线程」→ 跳到 `/`，首屏的项目胶囊是 Oryn，输入框为空。
5. 「查看全部」→ `/projects/<Oryn>/threads`。点「已归档」，搜索框输入一个标题词 → URL 带上 `?status=archived&q=…`。切到别的分区再切回来，筛选保持；侧栏 Oryn 行仍然高亮。
6. 齿轮 → 1280 宽落到 `/settings/overview`，左边是 6 组导航。在「环境」改 setup 脚本并保存，只发 PATCH 环境字段。改窗口到 390 宽，打开 `/settings` → 列表页，点进「GitHub」能看到绑定卡和门禁卡。
7. 在「概览」→ 危险操作 → 归档，确认框标题是「归档项目 Oryn？」。确认后出现归档 banner，侧栏 Oryn 行消失，页头没有「新建线程」。点 banner 上的「取消归档」→ Oryn 回到侧栏。
8. 直接打开 `/projects/prj_01JCZ000000000000000LEGACY` → 归档项目主页带 banner；打开 `/projects/prj_nope` → 显示「项目不存在或已删除。」并给出回到项目列表的链接。

## 阶段 ② — 仓库镜像与仓库浏览

> 摘要：orynd 在 `<data_dir>/repos/<projectId>.git` 为每个有 https 仓库地址的项目维护完整裸镜像（新包 `internal/repos`，git 由控制面执行，凭据只进环境变量，未担保主机经回环公网守卫）。
> 镜像提供只读 REST（状态 / 同步 / 分支标签 / 提交 / 提交详情与 diff / 目录 / 文件），出站代理记录每次 `git push` 并展开成「提交 → 线程」，GitHub 项目再按「提交 → PR → 线程」补齐。
> 前端在项目主页加「提交到 <基线>」「文件」「README」，新增 `提交 / 分支 / 标签 / 文件` 子页；git 缺失或项目无仓库时只显示一条说明，其余功能照常。

前提：阶段 ① 已合入，文件与组件名以本文阶段 ① 的规格为准。行号取自写本文时的工作区，动手前按符号重读。每个步骤后都要通过 `go test ./...` 与 `cd web && bun run check && bun run test`。改了 `schema/openapi.yaml` 或 SQL 的步骤先 `make gen`，提交后再跑 `make check-gen`（`Makefile:30-31` 比的是工作区，生成物未提交时必报差异）。动手前读 `skill://go`；2.10 起读 `skill://react`、`skill://typescript`；2.16 读 `skill://web-testing`。

### 阶段 ② 的通用约定

- **git 执行**（只在 `internal/repos/git.go`）：
  - argv 固定为 `[<git>, "--git-dir", <dir>, "-c","core.quotepath=off", "-c","color.ui=false", "-c","diff.noprefix=false", "-c","diff.mnemonicPrefix=false", "-c","protocol.allow=never", "-c","protocol.https.allow=always", "-c","protocol.http.allow=always", "-c","core.hooksPath=/dev/null", <子命令与参数…>]`。读命令（log、diff、ls-tree、cat-file 等）另加 `"-c","core.bigFileThreshold=16m"`：超过 16 MiB 的文件按二进制处理，diff 的内存有上界。
  - `git init` 一律带 `--template=`：不把主机上系统模板里的 hooks 与配置带进仓库。
  - 修订与路径参数一律放在 `--end-of-options` 之后；路径一律用 `:(literal)<path>` pathspec（与 `orbdrt/gitapi.go` 的 `literal()` 同语义）。
  - 环境从空白开始，只放：`PATH`（取 orynd 进程的 PATH）、`HOME=<data_dir>/repos/.home`、`XDG_CONFIG_HOME=<data_dir>/repos/.home/.config`、`GIT_CONFIG_NOSYSTEM=1`、`GIT_TERMINAL_PROMPT=0`、`LC_ALL=C`、`GIT_HTTP_LOW_SPEED_LIMIT=1000`、`GIT_HTTP_LOW_SPEED_TIME=60`，读操作另加 `GIT_OPTIONAL_LOCKS=0`。
  - 代理变量：担保主机（github.com 或有令牌的主机，见 2.4）把 orynd 进程的 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`（及小写形式）原样传给 git。出站代理的上游与 GitHub 客户端都用 `http.DefaultTransport`（`egress.go:262`、`github/github.go:203`），本来就读这些变量；镜像不读的话，必须经代理出网的主机上 GitHub 集成能用而镜像拉不动。未担保主机不传这些变量，只经 2.3 的守卫直连。
  - 超时：读 20 s（同 `orbdrt` 的 `gitTimeout`）；`ls-remote` 60 s；首次克隆的 fetch 15 min；增量 fetch 5 min。
  - stdout 按调用给上限，超限即杀进程并置 `truncated`（同 `orbdrt/gitapi.go:58-110` 的 `git()` 写法）；stderr 只留末尾 4 KiB。
  - 取消与超时不直接 SIGKILL。git 靠信号处理清理 `*.lock`，被 SIGKILL 会把锁文件永久留在仓库里。执行器统一这样建进程：
    ```go
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
    cmd.WaitDelay = 10 * time.Second // 到点还没退出，再对整个进程组 SIGKILL
    ```
  - **删除仓库目录**只经 `removeRepoDir(dir string, hostedOK bool) error`（`sync.go`）：目录里有标记文件 `oryn-hosted`（阶段 ③ 的托管正本）且 `hostedOK == false` 时不删，记 `slog.Error` 并返回错误。本阶段的调用点（超过上限、改地址、启动清扫）都传 `false`；只有删除项目的 `Remove` 传 `true`。这样库被恢复到旧备份、或阶段 ③ 的迁移被回滚时，镜像逻辑删不掉唯一的正本。
  - **磁盘保护**：`Config.MinFreeBytes`（缺省 2 GiB）。克隆与拉取开始前 `statfs(Dir)`，可用空间低于它就不执行，记 `no_space`；进行中每 5 s 复查一次可用空间与 `dirSize`，可用空间不足或镜像已超过 `MaxBytes` 就终止 git（前者记 `no_space`，后者记 `too_large`）。数据盘与 `oryn.db` 同盘，写满会让整个控制面停摆。
- **最低 git 版本 2.31**：`GIT_CONFIG_COUNT` 2.31 起可用，`--no-write-fetch-head` 2.29 起，`--end-of-options` 2.24 起。
- **校验规则**：
  - ref：`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`（照搬 `GitBaseQuery`，`schema/openapi.yaml:6595-6599`），并拒绝含 `..`、`//`、以 `/` 或 `.lock` 结尾的值。实现为导出函数 `repos.ValidRef(ref string) bool`（`repos.go`），阶段 ③ 复用。
  - 提交 sha：`^[0-9a-f]{7,40}$`（照搬 `GitCommitQuery`，`:6590-6594`）。
  - 仓库内路径：拒绝 NUL、开头 `/`、`.` / `..` / 空段，长度 ≤ 4096；`""` 表示根（tree 专用）。
  - 任何以 `-` 开头的 ref 或路径都返回 400 `invalid_request`。
- **新增错误码**（加进 `Error.code` 的描述，`schema/openapi.yaml:6762` 起）：
  - 503 `repo_unavailable`：控制面没有可用的 git。
  - 409 `no_repo`：项目没有仓库地址。
  - 409 `repo_not_ready`：镜像不存在、克隆中，或克隆失败。
  - 404 `ref_not_found`、`commit_not_found`、`path_not_found`。
  - 409 `not_a_file`、`not_a_directory`。
  - 413 `too_large`（沿用 M3 的码）。
- **文案**：全部新文案放在新顶层命名空间 `projectRepo`（写本文时两个语言文件里都没有），只有 `files.roots.repo` 例外；两种语言同步（`locales.test.ts` 比 key 集合）；计数用 `_one` / `_other`（同 `zh-CN.json:173-174` 的写法）。
- **复制**：一律 `copyToClipboard`（`web/src/lib/clipboard.ts:5`）。返回 false 时 `showToast({ text: t("projectRepo.copyFailed"), tone: "err" })`（`state/toasts.ts:55`），成功不提示。

---

### 步骤 2.1 — 把 git diff 解析抽成 `internal/gitdiff`

依赖：无，因为这是纯重构，只为 2.8 复用。

文件：
- 新建 `internal/gitdiff/gitdiff.go`
- 修改 `internal/orbdrt/gitapi.go`
- 测试 `internal/gitdiff/gitdiff_test.go`

做什么：

1. 新建包 `gitdiff`，放这些纯函数（不执行 git）：
   - `func NULFields(b []byte) []string`：照搬 `orbdrt/gitapi.go:408-414`。
   - `func Status(code byte) apigen.GitFileChangeStatus`：取代 `nameStatusMap`（`:416-424`）；未知码返回 `modified`。
   - `func ParseChanges(nameStatus, numstat []byte) (byPath map[string]*apigen.GitFileChange, order []string)`：照搬 `changedFiles` 里解析 `--name-status -z` 与 `--numstat -z` 的两段（`:478-536`）。`Staged` 恒为 `none`。
   - `func IsBinaryPatch(p []byte) bool`：照搬 `:834-846`。
   - `func CutTruncated(out []byte) []byte`：截到最后一个换行；没有换行返回 nil（照搬 `handleGitDiff` 的 `:795-801`）。
2. `orbdrt/gitapi.go`：`changedFiles` 改为调用 `gitdiff.ParseChanges` 后再做 worktree 合并；`mergeIndexState` 里的 `nameStatusMap[code]` 改为 `gitdiff.Status(code)`；`handleGitDiff` 改用 `gitdiff.CutTruncated` 与 `gitdiff.IsBinaryPatch`。删除本地的 `nulFields`、`nameStatusMap`、`isBinaryPatch`。全部调用点用 `rg -n 'nulFields\(|nameStatusMap|isBinaryPatch\(' internal/orbdrt` 找，必须清零。
3. 为什么只抽解析、不抽执行层：orbd 的执行层（`r.bins.command`、`r.baseEnv()`）在 orb 里跑，控制面需要的是隔离的环境与凭据注入，两边只有解析是同一份逻辑。

验收：
- `go test ./internal/gitdiff/... ./internal/orbdrt/...` 通过。
- 用例：`ParseChanges("R100\x00a.go\x00b.go\x00", "3\t1\t\x00a.go\x00b.go\x00")` 得到 `b.go`：`status=renamed`、`oldPath=a.go`、`+3 −1`；`IsBinaryPatch([]byte("Binary files a and b differ\n"))` 为真。

---

### 步骤 2.2 — 配置键、迁移与查询

依赖：无，因为只加配置与表，没有使用方。

文件：
- 修改 `internal/config/config.go`
- 新建 `internal/store/migrations/0046_project_repos.sql`（阶段 ① 的 `0045_project_description.sql` 之后；编号被占用时顺延）。
- 新建 `internal/store/queries/repos.sql`
- 修改 `internal/store/queries/github.sql`
- 测试 `internal/store/store_test.go`、`internal/config/config_test.go`

做什么：

1. config：
   - 新类型：
     ```go
     // Repos 是项目仓库的控制面裸仓库（阶段 ②）。
     type Repos struct {
         // Git 是 git 可执行文件；空 = 在 PATH 里找 git（不带斜杠的名字也按 PATH 查找）。
         Git string `yaml:"git"`
         // MaxMirrorGB 是单个镜像的大小上限；0（缺省）= 5。拉取后超出即删除镜像并记 too_large。
         MaxMirrorGB int `yaml:"max_mirror_gb"`
         // MinFreeGB 是数据盘要保留的可用空间：低于它时不克隆、不拉取、不接受推送；0（缺省）= 2。
         MinFreeGB int `yaml:"min_free_gb"`
     }
     ```
   - `Orynd` 加字段 `Repos Repos yaml:"repos"`（`:244-261`）。
   - `LoadOrynd` 仿 chromium（`:341-346`）：值含 `/` 时转绝对路径。
   - `Validate` 加两条：`max_mirror_gb` < 0 → `repos.max_mirror_gb 不能为负`；`min_free_gb` < 0 → `repos.min_free_gb 不能为负`。
   - 加 `func (c Orynd) ReposDir() string { return filepath.Join(c.DataDir, "repos") }`。
2. 迁移（Up / Down 都写全；SQL 注释可用中文，查询文件注释只用英文）：
   ```sql
   -- +goose Up
   -- 阶段 ②：项目仓库的控制面裸仓库与提交↔线程关联。kind=hosted 留给阶段 ③（本阶段只写 mirror）。
   CREATE TABLE project_repos (
       project_id     TEXT    PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
       kind           TEXT    NOT NULL CHECK (kind IN ('mirror','hosted')),
       remote_url     TEXT,              -- mirror：最近一次拉取时项目的 repo_url 原文
       github_repo_id INTEGER,           -- 同上时刻项目绑定的 GitHub 仓库 id（改名跟随判定）
       default_branch TEXT,
       synced_at      INTEGER,           -- 最近一次成功拉取；NULL = 从未成功
       size_bytes     INTEGER NOT NULL DEFAULT 0,
       error_code     TEXT,
       error_message  TEXT,              -- 已脱敏，≤500 字符
       error_at       INTEGER,
       created_at     INTEGER NOT NULL,
       updated_at     INTEGER NOT NULL
   );
   CREATE TABLE repo_pushes (
       id          INTEGER PRIMARY KEY AUTOINCREMENT,
       project_id  TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
       thread_id   TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
       orb_id      TEXT    NOT NULL,
       ref         TEXT    NOT NULL,     -- refs/heads/<分支>
       old_sha     TEXT    NOT NULL,
       new_sha     TEXT    NOT NULL,
       pushed_at   INTEGER NOT NULL,
       expanded_at INTEGER,              -- NULL = 待展开
       expand_note TEXT                  -- NULL | truncated | missing
   );
   CREATE INDEX repo_pushes_pending ON repo_pushes (project_id, expanded_at);
   CREATE TABLE repo_commit_threads (
       project_id TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
       sha        TEXT    NOT NULL,
       thread_id  TEXT    REFERENCES threads (id) ON DELETE CASCADE,  -- NULL = GitHub 反查过、没有线程
       source     TEXT    NOT NULL CHECK (source IN ('push','pull','none')),
       linked_at  INTEGER NOT NULL,
       PRIMARY KEY (project_id, sha)
   );
   -- +goose Down
   DROP TABLE repo_commit_threads;
   DROP INDEX repo_pushes_pending;
   DROP TABLE repo_pushes;
   DROP TABLE project_repos;
   ```
3. `repos.sql`（sqlc 名与语义）：
   - `GetProjectRepo :one`、`ListProjectRepos :many`、`DeleteProjectRepo :exec`。
   - `UpsertProjectRepo :exec`：全部列，`ON CONFLICT (project_id) DO UPDATE`。
   - `InsertRepoPush :exec`。
   - `ListPendingRepoPushes :many`：`project_id = @project_id AND expanded_at IS NULL ORDER BY id`。
   - `FinishRepoPush :exec`：`expanded_at` + `sqlc.narg(expand_note)`。
   - `ExpireRepoPushes :exec`：`SET expanded_at=@now, expand_note='missing' WHERE expanded_at IS NULL AND pushed_at < @before`。
   - `DeleteRepoPushesBefore :exec`：已展开且 `expanded_at < @before`。
   - `LinkRepoCommitThread :exec`：`INSERT … ON CONFLICT (project_id, sha) DO UPDATE SET thread_id=excluded.thread_id, source=excluded.source, linked_at=excluded.linked_at WHERE repo_commit_threads.source = 'none'`。正结果不互相覆盖：先到的 push 胜出。
   - `MarkRepoCommitNoThread :exec`：`INSERT OR REPLACE … source='none', thread_id=NULL`，前提是当前行不存在或本身就是 `none`，条件写进 `WHERE NOT EXISTS (… source <> 'none')`。
   - `ListRepoCommitThreads :many`：`sha IN (sqlc.slice('shas'))`，`LEFT JOIN threads` 取 `title`。
   - `ListSampledCommitThreads :many`：`FROM thread_commits tc JOIN threads t … WHERE t.project_id=@project_id AND tc.sha IN (sqlc.slice('shas')) ORDER BY COALESCE(t.last_message_at, t.created_at) DESC`，取 `sha, thread_id, title`。
4. `github.sql` 加 `FirstPullThread :one`：`SELECT thread_id FROM thread_pulls WHERE repo_id=@repo_id AND number=@number AND unlinked=0 ORDER BY linked_at LIMIT 1`。不排除已归档线程：历史提交照样属于它。
5. `store_test.go:35-47` 的 `m2Tables` 按字典序插入 `project_repos`（在 `project_snapshots` 前）以及 `repo_commit_threads`、`repo_pushes`（在 `recovery_codes` 后、`role_avatars` 前）；`:15-34` 的注释补「阶段 ②：project_repos、repo_pushes、repo_commit_threads」。

验收：
- `make gen && go test ./internal/store/... ./internal/config/...` 通过。
- 用例：`repos: {git: "./bin/git"}` 加载后 `Repos.Git` 是绝对路径；`max_mirror_gb: -1` 时校验报错。

---

### 步骤 2.3 — 回环公网守卫 `egress.PublicProxy`

依赖：无，因为它只用 egress 已有的拨号函数。

文件：
- 新建 `internal/egress/publicproxy.go`
- 测试 `internal/egress/publicproxy_test.go`

做什么：

1. 新类型：
   ```go
   // PublicProxy 是控制面自己的 git 连未担保主机时用的回环 CONNECT 代理（阶段 ②）：只放行 443 端口，
   // 拨号沿用 dialWeb（空 WebPolicy = 一律 publicOnly），在连接时检查实际 IP，DNS 重绑定无效。
   type PublicProxy struct{ ln net.Listener; srv *http.Server }
   func ListenPublicProxy() (*PublicProxy, error) // 监听 127.0.0.1:0
   func (p *PublicProxy) URL() string             // "http://127.0.0.1:<port>"
   func (p *PublicProxy) Close() error
   ```
2. 处理逻辑：
   - 只接受 `CONNECT`，其它方法回 405。
   - `webTarget(r.Host)`（`web.go:181-191`）解析失败 → 400；端口不是 443 → 403 `oryn repos: only port 443`。
   - `dialWeb(ctx, &WebPolicy{}, host, port)`（`:151-157`）：`errNotPublic` → 403（正文 `errNotPublic.Error()`），其它错误 → 502。
   - 成功后与 `serveConnect`（`:207-243`）相同：Hijack、清期限、回 200、`go tunnel(...)`（`:247-255`）。
   - `srv.ReadHeaderTimeout = 10s`。
3. 为什么另开监听：git 子进程只能经 `http.proxy` 交出连接，出站代理的 `Handler` 绑定 orb 策略，不适合控制面。

验收：
- `go test ./internal/egress/...` 通过。
- 用例：
  - `CONNECT 127.0.0.1:443` → 403，正文 `destination is not a public address`。
  - `CONNECT example.com:22` → 403。
  - `GET /` → 405。

---

### 步骤 2.4 — `internal/repos`：裸仓库存储与同步

依赖：2.2（表与配置）、2.3（守卫）。

文件：
- 新建 `internal/repos/repos.go`、`git.go`、`sync.go`
- 测试 `internal/repos/testutil_test.go`、`sync_test.go`、`git_test.go`

做什么：

1. 公开 API（`repos.go`）：
   ```go
   // Kind 是项目仓库的来源；阶段 ③ 加 KindHosted。
   type Kind string
   const ( KindMirror Kind = "mirror"; KindNone Kind = "none" )
   type State string
   const ( StateAbsent State = "absent"; StateCloning State = "cloning"; StateReady State = "ready"; StateError State = "error" )

   type Config struct {
       Dir          string           // cfg.ReposDir()
       Git          string           // cfg.Repos.Git
       GitHubWebURL string           // cfg.GitHub.WebURL：github.com 仓库实际拉取的源
       MaxBytes     int64            // MaxMirrorGB<<30；0 = 5<<30
       MinFreeBytes int64            // MinFreeGB<<30；0 = 2<<30
       StaleAfter   time.Duration    // 0 = 60s
       Now          func() time.Time // nil = time.Now
       ProxyEnv     []string         // 担保主机拉取时追加的代理变量（KEY=VALUE）；nil = 不追加
       Changed      func(projectID string) // 状态变化后广播；nil = 不广播
   }
   // GitHub 是 github.com 拉取凭据（*github.Service 实现，2.5）。
   type GitHub interface{ UserToken(ctx context.Context) (string, error) }
   // Secrets 是其它主机的 git 令牌（*secrets.Vault 实现）。
   type Secrets interface{ GitToken(host string) (string, bool) }
   // Error 是领域错误，api 按 Status / Code 原样输出。
   type Error struct{ Status int; Code, Message string }

   func New(cfg Config, st *store.Store, sec Secrets, gh GitHub) (*Service, error)
   func (s *Service) Run(ctx context.Context)
   func (s *Service) Close()
   func (s *Service) Available() (ok bool, reason string) // reason: "" | "git_missing" | "git_too_old"
   func (s *Service) Status(ctx context.Context, projectID string) (apigen.ProjectRepoStatus, error)
   func (s *Service) Sync(ctx context.Context, projectID string) (apigen.ProjectRepoStatus, error)
   func (s *Service) Kick(projectID string)
   func (s *Service) Remove(projectID string)
   func (s *Service) ProjectChanged(ctx context.Context, projectID string)
   ```
   `apigen.ProjectRepoStatus` 在 2.5 生成，所以 2.4 先在包内定义同形的 `status` 结构，2.5 改为直接返回 apigen 类型并删除本地结构。
2. 定位 git（`git.go`）：
   - `New` 里执行 `exec.LookPath(cmp.Or(cfg.Git, "git"))`，再跑 `git version`（5 s 超时），按 `git version X.Y` 解析。
   - 找不到 → `git_missing`；低于 2.31 → `git_too_old`。用 `slog.Warn("控制面没有可用的 git，仓库浏览停用", "reason", r)` 记一次，此后不再探测，重启生效。
   - git 可用时 `egress.ListenPublicProxy()`，并 `MkdirAll(Dir, 0o700)` 与 `.home`。
3. 执行器：
   ```go
   type gitRun struct{ dir string; env []string; stdin io.Reader; limit int; timeout time.Duration }
   func (s *Service) git(ctx context.Context, o gitRun, args ...string) (gitResult, error)
   ```
   argv 与环境见「通用约定」；`gitResult` 字段同 `orbdrt` 的（out、truncated、code、stderr）。测试钩子 `s.trace func(argv, env []string)` 只在 `_test.go` 里赋值。
4. 凭据与守卫（`remote(ctx, prj db.Project) (remote, error)`）：
   - 用 `orbs.HTTPSRepo(*prj.RepoUrl)`（`orbs/setup.go:623-640`，规则合适：只收 https，拒绝端口、userinfo、query、`.`/`..` 段）。不合法 → `unsupported_url`。
   - 拉取地址：host 是 `github.com` 且 `GitHubWebURL != "https://github.com"` 时为 `GitHubWebURL + "/" + path + ".git"`，否则就是 `*prj.RepoUrl` 原文。
   - 令牌：github.com 用 `gh.UserToken(ctx)`，出错就匿名；其它主机用 `sec.GitToken(host)`。
   - 担保主机 = github.com，或令牌存在的主机。担保主机的环境再追加 `Config.ProxyEnv`（`repos.Config` 的字段 `ProxyEnv []string`，`KEY=VALUE` 形式；见「通用约定 · 代理变量」）；未担保主机不追加。
   - 有令牌时追加环境：`GIT_CONFIG_COUNT=2`、`GIT_CONFIG_KEY_0=http.<拉取地址的 scheme://host/>.extraHeader`、`GIT_CONFIG_VALUE_0=Authorization: Basic <base64("x-access-token:"+tok)>`（与出站代理 `resolveGit` 的头相同，`egress.go:511`），以及 `GIT_CONFIG_KEY_1=http.followRedirects`、`GIT_CONFIG_VALUE_1=false`。
   - 未担保主机追加 `http.proxy=<PublicProxy.URL()>`（同样用 `GIT_CONFIG_KEY_n`）。
   - 令牌永远不进 argv、不进磁盘配置。所有 stderr 写库或写日志前先经 `redactSecrets(s string, vals ...string) string`，把 tok 与它的 base64 Basic 串替换成 `oryn-redacted`。`egress` 的 `redact` 是未导出的成对替换，这里不导出它，另写 5 行。
5. 同步（`sync.go`）：
   - 单飞：`flights map[string]*flight`（`flight{ctx cancel; done chan struct{}; again bool}`）。进行中再 `Kick` 只置 `again`，跑完再来一轮。全局信号量 2：同时最多两个克隆或拉取。
   - 克隆流程：
     1. `git init --bare -q --template= <id>.git.tmp-<rand>`，然后写配置：`remote.origin.url=<拉取地址>`、`remote.origin.fetch` 两条（`+refs/heads/*:refs/heads/*`、`+refs/tags/*:refs/tags/*`）、`remote.origin.tagOpt=--no-tags`、`gc.autoDetach=false`。
     2. `git ls-remote --symref origin HEAD` 取默认分支；取到的名字不满足 `ValidRef` 时当作没取到。
     3. `git fetch --prune --quiet --no-write-fetch-head origin`。
     4. 默认分支存在时 `git symbolic-ref HEAD refs/heads/<默认>`。
     5. `os.Rename` 到 `<id>.git`。
   - 拉取流程：先把 `remote.origin.url` 设成当前算出的地址，再做 2–4 步。
   - ls-remote 失败而 fetch 成功时，默认分支按顺序取：旧值、`main`、`master`、第一个分支。
   - 每次成功后：用 `dirSize(dir string) (int64, error)`（`sync.go` 里的包内函数，`filepath.WalkDir` 求和；阶段 ③ 复用）算出大小，超过 `MaxBytes` 就 `removeRepoDir(dir, false)` 并记 `too_large`。否则 `UpsertProjectRepo`（`synced_at=now`，清掉 error），最后 `Changed(id)`。2.7 会在 `Changed` 之前加一行 `expandPushes`，本步不写。
   - 失败：
     - 没有镜像（首次克隆）→ 删除 tmp，行 `synced_at=NULL` 并记 error，状态 `error`。
     - 已有镜像 → 保留，只记 error，状态仍 `ready`。
   - 错误分类（stderr，`LC_ALL=C`）：

     | stderr 特征 | 错误码 |
     |---|---|
     | `Received HTTP code 403 from proxy after CONNECT` | `blocked_address` |
     | `terminal prompts disabled` / `Authentication failed` / `could not read Username` | 无令牌 `auth_required`，有令牌 `auth_failed` |
     | `Repository not found` / `returned error: 404` | `not_found` |
     | `Could not resolve host` / `Failed to connect` / `Connection refused` | `unreachable` |
     | context 超时 | `timeout` |
     | 磁盘保护触发（见「通用约定 · 磁盘保护」） | `no_space` |
     | 其它 | `fetch_failed` |

     message = 脱敏后的 stderr 末尾，截到 500 字符。
   - 状态计算（`Status`）：
     - 无 repoUrl → `kind=none, state=absent`。
     - 有飞行且无镜像 → `cloning`。
     - 有镜像 → `ready`（`syncing` = 有飞行）。
     - 无镜像但有 error → `error`。
     - 其余 → `absent`。
   - 懒触发（只在项目未归档、git 可用时）：`Status` 与 2.8 的读接口经 `ensure(ctx, prj, auto=prj.ArchivedAt==nil)` 进入：无镜像且无 error → 后台克隆（有 error 时不自动重试，等手动同步或推送事件）；ready 且 `now-synced_at > StaleAfter` → 后台拉取。都不阻塞请求。
   - 归档项目不做任何自动网络动作，`Sync` 照常可用。
   - 改地址（`ProjectChanged` 与 `ensure` 都会检查）：比较 `HTTPSRepo` 得到的 `host/小写 path`。行里的 `github_repo_id` 与项目当前的非空且相同（GitHub 改名跟随）→ 只更新 `remote_url`；否则删除镜像（取消飞行、`DeleteProjectRepo`、`removeRepoDir(dir, false)`，保留项目），然后 `Changed`。
   - `Remove`：取消飞行并等 ≤10 s；`DeleteProjectRepo`；`removeRepoDir(dir, true)`。
   - `removeRepoDir` 通过标记检查后，把目录改名为 `<id>.git.trash-<rand>`，在 goroutine 里 `os.RemoveAll`，失败只记日志。
   - `Run` 启动时：
     - 删除 `Dir` 下全部 `*.tmp-*` / `*.trash-*`。
     - `GetProject` 为 `sql.ErrNoRows` 的 `prj_*.git`：没有标记文件的直接删；带 `oryn-hosted` 标记的改名到 `<Dir>/orphaned/<id>.git-<Unix 秒>` 并记 `slog.Error`，不删。
     - 对留下的每个仓库，删除 mtime 早于 10 分钟的 `HEAD.lock`、`packed-refs.lock` 与 `refs/**/*.lock`（断电、OOM 后的残留）。
   - `Run` 之后每小时 `ExpireRepoPushes(before=now-24h)`、`DeleteRepoPushesBefore(now-30d)`（这两句在 2.7 加上）。
6. 测试：
   - `testutil_test.go`：
     - `requireGit(t)`：`exec.LookPath("git")` 失败时，`ORYN_REQUIRE_GIT=1` 则 `t.Fatal`，否则 `t.Skip`。
     - 上游用 `githubtest.New()` 加 `GitRoot=t.TempDir()`、`InitRepo("acme/widgets")`（`githubtest/gitstate.go`、`extra.go:42-111`），`Config.GitHubWebURL=fake.URL()`。
     - `commitTo(t, bare, branch, files map[string]string, msg)`：在临时工作区 clone → 写入 → commit → push。
   - `sync_test.go` 覆盖：
     - 首次 `Status` → `cloning`，等到 `ready`，`defaultBranch=main`、`sizeBytes>0`。
     - 上游新增分支 `b` 后 `Sync` → 镜像有 `refs/heads/b`；上游删除 `b` 后 `Sync` → prune 掉。
     - `StaleAfter` 内重复 `Status` 不触发拉取（数 fake 的 `CallsTo("GET","/acme/widgets.git/info/refs")`）。
     - 归档项目不自动克隆。
     - 改成另一个仓库地址 → 镜像被删。
     - `Remove` → 目录与行都没了。
     - `MaxBytes=1` → `too_large`，镜像被删。
     - `MinFreeBytes` 设成大于磁盘可用空间 → `Sync` 后 `lastError.code=no_space`，没有发起 fetch（fake 的 `info/refs` 调用数为 0）。
     - 目录里放了 `oryn-hosted` 标记：`removeRepoDir(dir, false)` 返回错误且目录还在；`Run` 对没有项目行的带标记目录改名到 `orphaned/`。
     - 仓库里留一个 mtime 为 1 小时前的 `refs/heads/x.lock`，`Run` 启动后它被删；mtime 为现在的不删。
     - git 路径指向不存在文件 → `Available()==(false,"git_missing")`。
   - `git_test.go`：
     - 带令牌拉取时 `trace` 收到的 argv 不含令牌；env 含 `GIT_CONFIG_VALUE_0=Authorization: Basic …`。
     - fake 记到的 `Auth` 解码为 `x-access-token:<tok>`。
     - `redactSecrets("fatal: tok dG9r", "tok")` 不含 `tok`。
     - 守卫：`repoUrl=https://127.0.0.1/acme/x` 且无密钥 → `lastError.code=blocked_address`；同地址但 `Secrets` 返回 `git/127.0.0.1/token` → `unreachable`（直连被拒），证明担保主机绕过了守卫。
     - 代理变量：`Config.ProxyEnv=["HTTPS_PROXY=http://p:1"]`，带令牌的主机 → `trace` 的 env 含这一项；无令牌的非 GitHub 主机 → 不含，且 env 里没有任何 `*_PROXY` 变量。测试替身的 `Config.ProxyEnv` 缺省为空，不受跑测试的机器环境影响。

验收：
- `go test ./internal/repos/...` 通过；没有 git 时整个包 Skip，`ORYN_REQUIRE_GIT=1` 时失败。
- 用例：`repoUrl=https://127.0.0.1/acme/x` → `Status` 最终 `{state:"error", lastError:{code:"blocked_address"}}`。

---

### 步骤 2.5 — 状态与同步接口、实时消息、装配

依赖：2.4。

文件：
- 修改 `schema/openapi.yaml`、`internal/realtime/public.go`、`internal/server/server.go`、`internal/api/area.go`、`internal/api/projects.go`、`internal/github/identity.go`、`.github/workflows/ci.yml`
- 新建 `internal/api/projectrepo.go`
- 测试 `internal/api/projectrepo_test.go`

做什么：

1. OpenAPI：
   - 路径：
     - `/api/v1/projects/{id}/repo`：`get`，`operationId: getProjectRepo`，200 返回 `ProjectRepoStatus`。
     - `/api/v1/projects/{id}/repo/sync`：`post`，`operationId: syncProjectRepo`，security 同 `updateProject`（`:428-432`），202 返回 `ProjectRepoStatus`。
   - schema：
     ```yaml
     ProjectRepoStatus:
       type: object
       required: [kind, state, available, syncing, empty]
       properties:
         kind: { type: string, enum: [mirror, none], description: '`none` = 项目没有仓库地址；阶段 ③ 加 `hosted`' }
         state: { type: string, enum: [absent, cloning, ready, error] }
         available: { type: boolean, description: 控制面有可用的 git（≥2.31） }
         unavailableReason: { type: string, enum: [git_missing, git_too_old] }
         syncing: { type: boolean, description: state=ready 时后台拉取进行中 }
         empty: { type: boolean, description: 镜像就绪但没有任何分支 }
         cloneUrl: { type: string, description: 用户 `git clone` 用的地址（mirror = 项目 repoUrl） }
         defaultBranch: { type: string }
         baseBranch: { type: string, description: 项目 baseBranch 在镜像里存在时取它，否则 defaultBranch }
         syncedAt: { $ref: '#/components/schemas/TimestampMs' }
         sizeBytes: { type: integer, format: int64 }
         lastError: { $ref: '#/components/schemas/ProjectRepoError' }
     ProjectRepoError:
       type: object
       required: [code, message, at]
       properties:
         code: { type: string, enum: [unsupported_url, auth_required, auth_failed, not_found, blocked_address, unreachable, timeout, too_large, no_space, fetch_failed] }
         message: { type: string, description: 已脱敏的 git 输出末尾（≤500 字符） }
         at: { $ref: '#/components/schemas/TimestampMs' }
     WsServerProjectRepoChanged:
       type: object
       description: 项目仓库镜像状态变了（开始 / 结束克隆或拉取、出错、被删除）；发给全部私有连接。客户端让该项目的仓库查询失效，WS 每次重新 open 后对全部项目失效一次
       required: [type, projectId]
       properties:
         type: { type: string, enum: [project_repo.changed], x-enum-varnames: [WsServerProjectRepoChangedTypeProjectRepoChanged] }
         projectId: { $ref: '#/components/schemas/ProjectId' }
     ```
     把 `WsServerProjectRepoChanged` 加进 `WsServerMessage` 的 `oneOf` 与 `mapping`（`:12435-12490`），并在 `Error.code` 描述里补上本阶段的新码。
2. `public.go` 加：
   ```go
   // ProjectRepoChanged 实现 repos.Config.Changed：项目仓库镜像状态变了，广播给全部私有连接（阶段 ②）。
   func (h *Hub) ProjectRepoChanged(projectID string)
   ```
   写法同 `SshChanged`（`:405-407`）。
3. github 加 `func (s *Service) UserToken(ctx context.Context) (string, error) { return s.tokenFor(ctx, slotUser) }`（`identity.go:152`）。
4. `projectrepo.go`：
   - `type ReposDeps struct{ Service *repos.Service }`；`area.go` 加 `Repos *ReposDeps`，nil 时回 501，写法同 `sshSvc`（`ssh.go:18-24`）。
   - `writeRepoErr`：`*repos.Error` → `WriteError(e.Status, e.Code, e.Message)`，其余交给 `writeErr`。
   - `GetProjectRepo` / `SyncProjectRepo` 调 `Status` / `Sync`。项目不存在 → 404 `not_found`；git 不可用时 `Sync` → 503 `repo_unavailable`；`kind=none` 时 `Sync` → 409 `no_repo`。
5. 装配（`server.go`，放在 `s.Stats` 之后，`:330` 一带）：
   ```go
   s.Repos, err = repos.New(repos.Config{Dir: cfg.ReposDir(), Git: cfg.Repos.Git, GitHubWebURL: cfg.GitHub.WebURL,
       MaxBytes: int64(cmp.Or(cfg.Repos.MaxMirrorGB, 5)) << 30, MinFreeBytes: int64(cmp.Or(cfg.Repos.MinFreeGB, 2)) << 30,
       ProxyEnv: proxyEnv(os.Environ()),
       Changed: s.Hub.ProjectRepoChanged}, s.Store, s.Vault, s.GitHub)
   ```
   - `proxyEnv(environ []string) []string`（`server.go` 包内函数）：只留键为 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` 及其小写形式的项。
   - `Server` 结构体加 `Repos *repos.Service`（与 `GitHub *github.Service`，`:109`，同列）。
   - `s.closers = append(s.closers, s.Repos.Close)`；`go s.Repos.Run(runCtx)`（与 `:368-387` 并列）；`s.API.Repos = &api.ReposDeps{Service: s.Repos}`。
6. `projects.go` 钩子：
   - `deleteProject` 在 `ProjectHomes.Remove`（`:453-457`）之后加：
     ```go
     // 项目仓库镜像随项目删除（行由外键级联）；失败只记日志
     if s.Repos != nil && s.Repos.Service != nil { s.Repos.Service.Remove(id) }
     ```
   - `updateProject` 在 `BindProject` 那段（`:386-393`）之后：`repoChanged` 时调 `s.Repos.Service.ProjectChanged(ctx, id)`。
   - 改为归档不动镜像。
7. `ci.yml` 的 go job 加 `ORYN_REQUIRE_GIT: "1"`（与 `ORYN_REQUIRE_SANDBOX` 并列，`:20-22`；ubuntu-24.04 自带 git）。
8. 测试 `projectrepo_test.go`：
   - 新建 `newRepoEnv(t)`：照 `newGitHubEnvWith`（`github_test.go:35-60`）装配 store、vault、`githubtest`（带 GitRoot）、`repos.New`，再挂 `s.Repos`；请求用 `h2Env.do`（`auth_test.go:111-144`）与 `webSession()`。
   - 用例：无 repoUrl → `{kind:"none",state:"absent"}`；有 repoUrl → 先 `cloning`、后 `ready`；`POST sync` 不带 CSRF → 403；删除项目 → `<dir>/repos/<id>.git` 不存在。

验收：
- `make gen && make lint-spec && go test ./internal/api/... ./internal/realtime/...` 通过。
- 用例：`GET /api/v1/projects/<id>/repo` → `{"kind":"mirror","state":"ready","available":true,"syncing":false,"empty":false,"defaultBranch":"main","baseBranch":"main",…}`。

---

### 步骤 2.6 — GitHub `push` 事件触发拉取

依赖：2.5。

文件：
- 修改 `internal/github/hooks.go`、`internal/github/events.go`、`internal/github/github.go`、`internal/server/server.go`
- 新建 `internal/github/push.go`
- 测试 `internal/github/push_test.go`

做什么：

1. `hookEvents`（`hooks.go:23-26`）末尾加 `"push"`。已有的 hook 怎么拿到新事件：`ensureHook` 每次都带 `Events: hookEvents` 整体 PATCH（`:122-142`），而 `Run` 每 `hookSyncInterval`（10 min，`github.go:29`）调用一次 `ResyncHooks`（`events.go:271-276`）。所以升级后 ≤10 min 内全部 hook 都会订阅 push。`hooks_test.go:70,128` 比较的就是变量本身，不用改。
2. 新增：
   ```go
   // RepoSync 是项目仓库镜像的拉取入口（*repos.Service 实现，阶段 ②）。
   type RepoSync interface{ Kick(projectID string) }
   func (s *Service) SetRepoSync(r RepoSync)
   ```
   在 `push.go` 的 `init` 里 `on("push", (*Service).onPush)`：取载荷 `repository.id`；`ListProjectsByGithubRepo`（只含未归档项目，`github.sql:93-97`）逐个 `Kick`；返回 `handled`，没有项目时返回 `ignored:no_project`；`repoSync` 为 nil 时返回 `ignored:no_repo_sync`。
3. 装配：`s.GitHub.SetRepoSync(s.Repos)`。
4. 测试：投递 `push` → stub 收到项目 id；已归档项目不 Kick。

验收：
- `go test ./internal/github/...` 通过。
- 用例：`deliver("push", {"ref":"refs/heads/main","repository":{"id":42}})` → outcome `handled`，stub 记到 `[prj_…]`。

---

### 步骤 2.7 — 在出站代理记录推送，并展开成提交

依赖：2.4（镜像与表）。

文件：
- 修改 `internal/egress/egress.go`、`internal/orbs/egress.go`、`internal/orbs/service.go`、`internal/server/server.go`、`internal/repos/sync.go`
- 新建 `internal/repos/links.go`
- 测试 `internal/egress/push_test.go`、`internal/orbs/push_egress_test.go`、`internal/repos/links_test.go`

做什么：

1. egress：
   - `target` 加 `push bool`、`gitHost string`。`resolveGit` 里 `push = strings.HasSuffix(rest, "/git-receive-pack")`、`gitHost = host`，对所有主机都设（`receivePack` 仍只给 github.com，`:520-523`）。
   - 从 `receivePackWrite`（`:848-862`）抽出 `func readPushCommands(r *http.Request) (cmds []RefUpdate, caps []string, ok bool)`（编码检查、`ParseReceivePack`、还原请求体），`receivePackWrite` 改为调用它。
   - 可选能力：
     ```go
     // GitPush 是上游以 200 应答的一次 git-receive-pack（阶段 ②）。
     type GitPush struct{ Host, Repo string; Refs []RefUpdate }
     // PushRecorder 是 Resolver 的可选能力：上游以 200 应答推送后调用（不实现即不记录）。
     type PushRecorder interface{ GitPushed(p GitPush) }
     ```
     用可选接口，不加进 `Resolver`，免得 7 个只读实现（`agentcfg_test`、`artifacts/sample`、`relayroute`、`modelcat`、`orbdrt` 测试等）各加一个空方法。
   - `ServeHTTP` 在 `beginGitHubWrites` 之后，若 `t.push && r.Method == POST`：`aud` 里有已解析的 PUSH 写操作就复用它的 `Refs`，否则调 `readPushCommands`。然后在 `ModifyResponse` 开头加：`resp.StatusCode == 200 && len(refs) > 0` 且 `h.R` 实现了 `PushRecorder` → `GitPushed(GitPush{t.gitHost, t.repo, refs})`。
   - 成功的判定：只看 HTTP 200，不解析 report-status（它在 side-band 里、随响应体流式到达，晚于 `ModifyResponse`）。被 `ng` 拒绝的 ref 不会误记：展开时要求 `new_sha` 出现在之后拉取的镜像里；即使它因为别的 ref 存在，这些提交也确实出自该 orb。
2. orbs：
   - 从 `threadGates`（`egress.go:176-195`）抽出 `func (s *Service) workThread(ctx context.Context, orbID string) (string, error)`（唯一的未归档工作线程，否则返回 `""`），`threadGates` 改为调用它。
   - 新增：
     ```go
     // PushSink 接收出站代理观察到的成功推送（阶段 ②；*repos.Service 实现）。
     type PushSink interface{ GitPushed(ctx context.Context, p PushedRefs) }
     type PushedRefs struct{ OrbID, ProjectID, ThreadID, Host, Repo string; Refs []egress.RefUpdate }
     func (s *Service) SetPushSink(p PushSink)
     ```
     `Service` 加字段 `pushes PushSink`（`service.go:164-174`）。
   - `egressResolver.GitPushed`：10 s ctx；`GetOrb` 的 `ProjectID` 为 nil、`workThread` 为空或 `pushes` 为 nil 时不记。
   - 装配：`s.Orbs.SetPushSink(s.Repos)`。
3. repos（`links.go`）：
   - `GitPushed`：
     - 读项目，要求 `HTTPSRepo` 得到的 `host + "/" + 小写 path` == `p.Host + "/" + p.Repo`（egress 的 `NormalizeRepo` 已经小写并去掉 `.git`）。
     - 只记 `refs/heads/` 开头且 `New` 不全为 0 的 ref，逐个 `InsertRepoPush`，然后 `Kick(project)`。
   - `expandPushes(ctx, prj)`：在每次成功拉取之后、`Changed` 之前调用（`sync.go` 加这一行）。对每个 `ListPendingRepoPushes`：
     1. `git cat-file -e --end-of-options <new>^{commit}` 失败 → 跳过（24 h 后由 `ExpireRepoPushes` 记 `missing`）。
     2. 选范围：
        - old 不全为 0 且 old 存在 → `rev-list --max-count=501 --end-of-options <new> ^<old>`。
        - 否则（新分支或 old 不在镜像里）：ref 就是默认分支或没有默认分支时 → `rev-list --max-count=501 --end-of-options <new>`；其余 → `… <new> ^refs/heads/<默认分支>`。
     3. 取前 500 个，超出记 `truncated`；在一个事务里逐个 `LinkRepoCommitThread(source='push')`，再 `FinishRepoPush`。
   - `Run` 加上 2.4 留下的两句定时清理。
4. 测试：
   - `egress/push_test.go`：用 `fakeR` 包一层实现 `PushRecorder` 的类型；非 GitHub 主机推送且上游 200 → 收到 `refs/heads/x old new`；上游 403 → 不调用；门禁拒绝 → 不调用。
   - `orbs/push_egress_test.go`：沿用 `memory_egress_test.go` 的环境（`:110` 一带），orb 有项目和唯一工作线程 → sink 收到正确的 project / thread。
   - `links_test.go`：
     - 仓库不符 → 不入库。
     - 推送后 `commitTo` 再 `Sync` → 新提交在 `repo_commit_threads` 里，`source=push`。
     - 新分支的情况只包含分支独有的提交。
     - 镜像里没有 new，24 h 后 → `missing`（注入 `Now`）。

验收：
- `go test ./internal/egress/... ./internal/orbs/... ./internal/repos/...` 通过。
- 用例：线程 T 推送 `refs/heads/feat 000…0 → <c2>`，c1、c2 不在 main 上 → 拉取后 `repo_commit_threads` 恰好两行 `(c1,T,push)`、`(c2,T,push)`。

---

### 步骤 2.8 — 读接口与提交↔线程查找

依赖：2.5、2.7。

文件：
- 修改 `schema/openapi.yaml`、`internal/api/projectrepo.go`、`internal/repos/links.go`、`internal/github/pulls.go`、`internal/server/server.go`
- 新建 `internal/repos/read.go`
- 测试 `internal/repos/read_test.go`、`internal/api/projectrepo_test.go`、`internal/github/pulls_test.go`

做什么：

1. OpenAPI：全部 `tags: [projects]`，GET 用缺省鉴权。
   - 新参数：
     - `RepoRefQuery`（`ref`，pattern 同 `GitBaseQuery`）。
     - `RepoPathQuery`（`path`，maxLength 4096，描述写明「仓库内相对路径；空 = 根」）。
     - `RepoShaPath`（`sha`，in: path，pattern `^[0-9a-f]{7,40}$`）。
     - `RepoCursorQuery`（`cursor`，maxLength 200）。
   - 路径：
     - `/repo/refs`（`listProjectRepoRefs`）：`kind`（必填，`branch|tag`）、`cursor`、`limit`（1–200，缺省 50）→ `ProjectRepoRefList`。
     - `/repo/commits`（`listProjectRepoCommits`）：`ref`、`path`、`cursor`、`limit`（1–100，缺省 30）→ `ProjectRepoCommitList`。
     - `/repo/commits/{sha}`（`getProjectRepoCommit`）→ `ProjectRepoCommitDetail`。
     - `/repo/commits/{sha}/diff`（`getProjectRepoCommitDiff`）：复用 `GitDiffPathQuery`、`GitDiffOldPathQuery`（`:6600-6610`）→ 现成的 `GitFileDiff`（`:11561-11576`）。
     - `/repo/tree`（`getProjectRepoTree`）：`ref`、`path`、`lastCommits`（boolean，缺省 true）→ `ProjectRepoTree`。
     - `/repo/blob`（`getProjectRepoBlob`）：`ref`、`path`（必填）、`If-None-Match` 头 → 200 `application/octet-stream`（带 `ETag`、`Content-Disposition`），或 304。
   - schema：
     ```yaml
     ProjectRepoCommitSummary: { required: [sha, shortSha, subject, authorName, authorEmail, ts], 字段同 GitCommit }
     ProjectRepoThreadLink:
       required: [id, title, source]
       properties: { id: ThreadId, title: string, source: { enum: [push, sampled, pull] } }
     ProjectRepoCommit:
       allOf: [ProjectRepoCommitSummary]
       required: [parents]
       properties: { parents: string[], thread: ProjectRepoThreadLink }
     ProjectRepoCommitList: { required: [ref, commit, commits], properties: { ref: string, commit: string, commits: { maxItems: 100, items: ProjectRepoCommit }, nextCursor: string } }
     ProjectRepoCommitDetail:
       allOf: [ProjectRepoCommit]
       required: [body, committerName, committedAt, files, additions, deletions, truncated]
       files: { maxItems: 2000, items: GitFileChange }   # staged 恒 none；合并提交相对第一个父提交，根提交相对空树
       body: 去掉 subject 行后的正文，≤64KiB
     ProjectRepoRef:
       required: [name, kind, commit, default]
       properties: { name, kind: [branch, tag], commit: ProjectRepoCommitSummary, default: boolean, annotated: boolean }
     ProjectRepoRefList: { required: [refs], properties: { refs: { items: ProjectRepoRef }, nextCursor: string } }
     ProjectRepoTreeEntry:
       required: [name, path, type, mode, size, oid]
       properties:
         name, path: 仓库内相对路径（根为 ""）
         type: [dir, file, symlink, submodule]
         mode: Unix 权限位（100644→420，100755→493，040000→493，120000→511，160000→0）
         size: int64（仅 file / symlink）
         oid: blob / tree / 子模块提交的 sha
         target: 符号链接目标（≤4KiB）
         lastCommit: ProjectRepoCommitSummary
         mime: 仅 self 且 type=file
         text: 仅 self 且 type=file：前 8KiB 无 NUL 且是合法 UTF-8
     ProjectRepoTree:
       required: [ref, commit, path, self, entries, truncated, lastCommitsComplete]
       entries: { maxItems: 5000, description: self 是目录时为直接子项（目录在前、按名不分大小写排序），否则为空 }
     ```
2. 实现（`read.go`）。每个方法先走 `ensure(ctx, prj, auto)`：
   - kind=none → 409 `no_repo`；git 不可用 → 503 `repo_unavailable`；镜像不在 → 409 `repo_not_ready`。
   - ref 解析：空值 = 状态里的 `baseBranch`。依次尝试 `rev-parse --verify -q --end-of-options refs/heads/<ref>^{commit}`、`refs/tags/<ref>^{commit}`，最后在 ref 形如 sha 时直接试 `<ref>^{commit}`。都失败 → 404 `ref_not_found`。
   - refs：`for-each-ref --sort=-committerdate`（标签用 `-creatordate`），`--format` 用 `%1f` / `%1e` 分隔，取 `refname`、`objectname`、`*objectname`、`subject`、`*subject`、`authorname`、`*authorname`、`authoremail`、`*authoremail`、`authordate:unix`、`*authordate:unix`、`objecttype`，读上限 10000 条，stdout ≤16 MiB。默认分支排第一。cursor = 十进制 offset。
   - commits：`log --format=%H%x1f%h%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%s%x1e --skip=<off> -n <limit+1> --end-of-options <tip> -- [:(literal)<path>]`。cursor = `<tip>:<off>`，校验 `^[0-9a-f]{40}([0-9a-f]{24})?:[0-9]{1,7}$`，翻页时用 cursor 里的 tip，分支移动后页面仍稳定。
   - commit：
     - `show -s --format=…%cn%x1f%ce%x1f%ct%x1f%B --end-of-options <sha>^{commit}`；body ≤64 KiB。
     - 文件 = `gitdiff.ParseChanges(diff --no-ext-diff -M --name-status -z <base> <sha>, … --numstat -z …)`；base = 第一个父提交，没有就用空树 `4b825dc642cb6eb9a060e54bf8d69288fbee4904`。
     - 按路径排序，取前 2000 个；additions / deletions 是非二进制文件的和。
     - sha 不存在 → 404 `commit_not_found`。
   - diff：`diff --no-ext-diff -M -U3 <base> <sha> -- :(literal)path [:(literal)oldPath]`，上限 1 MiB，再用 `gitdiff.CutTruncated` / `IsBinaryPatch`，规则同 `handleGitDiff`（`orbdrt/gitapi.go:708-808`）。
   - tree：
     1. `rev-parse --verify -q <commit>:<path>` 取 oid，失败 → 404 `path_not_found`。
     2. `cat-file -t` 判断类型。
     3. 目录：`ls-tree -z --long <treeoid>`，最多 5000 条，超出置 `truncated`。
     4. 文件：self 带 `mime`（`mime.TypeByExtension`，退回 `http.DetectContentType(前 512 B)`）和 `text`（用 `cat-file blob` 读 8 KiB 后杀进程判断）。
     5. 最后提交（`lastCommits=true` 且是目录时）：流式读 `log --no-renames --name-only --format=%x1e%H%x1f%at%x1f%s --end-of-options <commit> -- [:(literal)<dir>/]`；每个记录把改动路径映射到直接子项名（`"` 开头的名字用 `strconv.Unquote`），首次见到就记下。子项全部有值、或已读 2000 个提交、或耗时 ≥3 s 时取消进程。没拿到的子项省略 `lastCommit`，并置 `lastCommitsComplete=false`。
     6. 缓存：内存 `map[commit+"\x00"+dir]`，超过 256 项就整个清空（提交 sha 不变，结果可以一直复用；仓库里没有 LRU 工具，这样最简单也有界）。
   - blob：
     - 解析 oid；是目录 → 409 `not_a_file`；`cat-file -s` 超过 64 MiB（与 web 的 `MAX_PREVIEW_BLOB_BYTES` 一致）→ 413 `too_large`。
     - 响应头：
       - `ETag: "<oid>"`；`If-None-Match` 相同 → 304。
       - `Content-Type` 同 tree 的 mime 规则，但 `text/html` 一律改为 `text/plain; charset=utf-8`。
       - `share.InlineImage` 为真的栅格图片用 `Content-Disposition: inline`，其余一律 `attachment; filename=<名>`（`mime.FormatMediaType`）。
       - `X-Content-Type-Options: nosniff`、`Content-Security-Policy: sandbox`，同 `blobs.go:35-46`。
       - ref 是完整 sha 时 `Cache-Control: private, max-age=31536000, immutable`，否则 `private, no-cache`。
     - 正文流式输出 `cat-file blob <oid>`。忽略 `Range`：web 的 `readTextRange` 已经兼容 200（`file-source.ts:330-345`）。
3. 线程查找（`links.go`）：
   ```go
   func (s *Service) threadsFor(ctx context.Context, prj db.Project, shas []string) map[string]apigen.ProjectRepoThreadLink
   ```
   每页批量两次查询：先 `ListRepoCommitThreads`（只要 thread 非空的行，`source` 原样），未命中的再 `ListSampledCommitThreads`（每个 sha 取第一行，`source=sampled`）。
   提交详情（`Commit`）额外一步，需要同时满足：项目 `GithubRepoID != nil`、前两步都未命中、库里没有 24 h 内的 `none` 行。满足时调用：
   ```go
   type CommitThreads interface{ CommitThread(ctx context.Context, projectID, sha string) (threadID string, ok bool, err error) }
   ```
   `ok` → `LinkRepoCommitThread(source='pull')`；`!ok && err==nil` → `MarkRepoCommitNoThread`；`err != nil` 不缓存（比如身份未连接）。列表页从不调 GitHub，避免一页 30 个提交扇出 30 次请求。
   `repos.Config` 不加这个依赖，改为在 `GitHub` 接口里并上 `CommitThreads`。
4. github（`pulls.go`）：`func (s *Service) CommitThread(ctx, projectID, sha string) (string, bool, error)`：`projectRepo(prj)`，未绑定 → `("", false, nil)`；以 `slotUser` 请求 `GET /repos/{full}/commits/{sha}/pulls?per_page=5`（与 `:269` 同形）；按顺序取 `merged`、再 `open` 的 PR，用 `FirstPullThread(repoID, number)` 找线程。
5. handlers：8 个方法分别调用 `Service.Refs` / `Commits` / `Commit` / `CommitDiff` / `Tree` / `Blob`。参数结构体的类型名取 `make gen` 之后 `internal/apigen/apigen.gen.go` 里 `ServerInterface` 的签名；`GetProjectRepoBlob` 自己写响应头并 `io.Copy`。
6. 测试：
   - `read_test.go`：
     - refs 翻页：60 个分支、limit 50 → 第二页 10 个。
     - commits 翻页与 path 过滤；上游动了 main 之后，第二页 cursor 仍落在原 tip 上。
     - commit 详情的文件数与 `+/-`；根提交相对空树。
     - diff 截断到 1 MiB；二进制 → `kind=binary`。
     - tree 根目录子项的 `lastCommit.subject`；`Config.treeWalkMax=1`（测试用字段）→ `lastCommitsComplete=false`。
     - blob 内容、ETag 与 304；目录 → 409 `not_a_file`；`Config.maxBlobBytes=4` → 413。
     - 坏输入：ref `-x` → 400；path `../a` → 400；sha `zz` → 400；未知 ref → 404 `ref_not_found`。
     - 线程查找：push 优先于 sampled；GitHub stub 只调一次，第二次命中缓存；负结果 24 h 内不再调用。
   - `pulls_test.go`：fake 的 `commits/{sha}/pulls`（`githubtest/issues.go:177`）返回一个已合并 PR，且 `thread_pulls` 有关联 → 返回该线程。

验收：
- `make gen && make lint-spec && go test ./internal/repos/... ./internal/api/... ./internal/github/...` 通过。
- 用例：`GET …/repo/tree` → `entries` 里有 `{"name":"README.md","type":"file","lastCommit":{"subject":"initial commit",…}}`；`GET …/repo/blob?path=README.md` → `# acme/widgets\n`，响应头带 `ETag: "<oid>"`。

---

### 步骤 2.9 — 端到端测试

依赖：2.6、2.8。

文件：新建 `internal/server/e2e_repo_test.go`

做什么：复用 `newM15Env` 与它的前三步 `stepOAuthApp` / `stepIdentities` / `stepProject`（`e2e_m15_test.go:122-138,213-275`；项目 `acme/widgets` 已绑定且 hook 为 active）。之后依次：

1. 轮询 `GET /api/v1/projects/<prj>/repo` ≤30 s，直到 `state=="ready"`。
2. `GET repo/commits` 的 `commits[0].subject=="initial commit"`。
3. tree / blob 同 2.8 的验收用例。
4. 在 `fake.BarePath(m15Repo)` 上 `commitTo(…, "second")`。
5. `e.deliver(t, "push", map[string]any{"ref":"refs/heads/main","repository":map[string]any{"id":m15RepoID}})`，`e.outcome(...)=="handled"`（`:827-848`）。
6. 轮询 commits，直到 `commits[0].subject=="second"`。
7. `fake.CallsTo("GET","/acme/widgets.git/info/refs")` 里每条的 `Auth` 都以 `Basic ` 开头，解码后以 `x-access-token:` 开头。
8. `DELETE /api/v1/projects/<prj>` 之后，`filepath.Join(e.cfg.DataDir,"repos",prj+".git")` 不存在。

验收：`ORYN_REQUIRE_GIT=1 go test ./internal/server/ -run TestE2ERepo -count=1` 通过。

---

### 步骤 2.10 — 前端数据层：图标、实时、查询、mock 仓库

依赖：2.8（生成的 `schema.d.ts`）。

文件：
- 修改 `web/src/components/ui/icons.tsx`、`web/src/realtime/socket.ts`、`web/src/components/layout/AppShell.tsx`、`web/src/mock/server.ts`、两个语言文件
- 新建 `web/src/api/repo-queries.ts`、`web/src/mock/repo.ts`
- 测试 `web/src/api/repo-queries.test.tsx`
- 另需修改所有给 `RealtimeClient` 做桩的测试：`App.test.tsx:16`、`AppShell.test.tsx:34`、`useSection.test.tsx:32` 都加 `onProjectRepoChanged: vi.fn(() => () => undefined)`

做什么：

1. 图标：`commit: GitCommit`、`tag: Tag`、`readme: BookOpenText`，分别深导入 `@phosphor-icons/react/dist/csr/GitCommit`、`/Tag`、`/BookOpenText`（已确认 node_modules 里有）。
2. socket：
   - 加 `onProjectRepoChanged(cb: (projectId: string | null) => void): () => void` 与对应的 emit，写法同 `sshListeners`（`:169-181`）。
   - `case "project_repo.changed": this.emitProjectRepoChanged(msg.projectId)`。
   - `onopen`（`:456-460`）里 `this.emitProjectRepoChanged(null)`。
3. `repo-queries.ts`：
   - 类型：`RepoStatus`、`RepoRef`、`RepoCommit`、`RepoCommitDetail`、`RepoTree`、`RepoTreeEntry`（全部取自 `Schemas[...]`）。
   - `repoKeys`：
     ```ts
     all: (id) => ["projectRepo", id],
     status: (id) => [...all(id), "status"],
     refs: (id, kind) => [...all(id), "refs", kind],
     commits: (id, ref, path) => [...all(id), "commits", ref ?? "", path ?? ""],
     commit: (id, sha) => [...all(id), "commit", sha],
     diff: (id, sha, path, oldPath) => [...all(id), "diff", sha, path, oldPath ?? ""],
     tree: (id, ref, path, lastCommits) => [...all(id), "tree", ref ?? "", path, lastCommits],
     ```
   - `useRealtimeRepoSync(client = realtime)`：projectId 非空 → `invalidateQueries({queryKey: repoKeys.all(id)})`；为 null → `{queryKey: ["projectRepo"]}`。在 `AppShell.tsx:216` 旁调用。
   - `repoRetry = (n, e) => !(e instanceof ApiError && [400, 404, 409, 413, 503].includes(e.status)) && n < 2`。
   - hooks：
     - `useRepoStatus(projectId, enabled)`：`staleTime 10_000`；`state` 为 `cloning` 或 `syncing` 时 `refetchInterval 5_000`（WS 丢消息时的兜底）。
     - `useRepoRefs(projectId, kind)`：infinite，`nextCursor`。
     - `useRepoCommits(projectId, {ref?, path?})`：infinite，`limit 30`，`staleTime 30_000`。
     - `useRepoCommit(projectId, sha)`：`staleTime 60_000`。
     - `useRepoCommitDiff(projectId, sha, file, enabled)`：`staleTime Infinity`。
     - `useRepoTree(projectId, ref, path)`：`staleTime 30_000`。
     - `useSyncRepo(projectId)`：mutation，成功后 `setQueryData(status)`。
   - `repoBlobUrl(projectId, ref, path): string` → `/api/v1/projects/${encodePathParam(id)}/repo/blob?ref=…&path=…`，ref 为空时省略该参数。
4. mock（`mock/repo.ts`，`MockRepo.handle(p, m, url): Promise<Response | null>`，在 `server.ts:1775` 的 `githubRes` 之前调用；构造参数 `{ changed: (projectId) => this.broadcast({type:"project_repo.changed", projectId}), projects, threads }`）：
   - Oryn 项目（`fixtures.ts:20`）：`ready`，`defaultBranch "main"`，`sizeBytes 3_145_728`，`syncedAt NOW-4min`。
   - oryn-core：`state "error"`，`lastError {code:"auth_failed", message:"fatal: Authentication failed for 'https://github.com/Lynricsy/oryn-core.git/'", at: NOW-1h}`。
   - 其它项目：按有无 repoUrl 分别返回 `mirror/absent` 或 `none`。
   - 假仓库：分支 `main`、`feature/repo-browser`，附注标签 `v0.1.0`，8 个提交（真实格式的 40 位 sha，subject / author / ts 各不相同，其中 2 个 `thread` 指向 `threads.find(t => t.projectId === projects[0].id)`）。
   - 文件：`README.md`（含 `[指南](docs/guide.md)` 与 `![架构图](docs/diagram.svg)`）、`docs/guide.md`、`docs/diagram.svg`（120×60，`fill="gray"`，这是数据不是组件）、`src/main.go`、`src/lib/util.go`、`.gitignore`。
   - 每个提交带文件列表与真实 unified patch。`POST repo/sync` → `syncing:true`，600 ms 后 `syncedAt=Date.now()` 并广播。不存在的路径一律回与服务端同码的错误体。
5. 文案：`files.roots.repo`「仓库」/"Repository"；`projectRepo.copyFailed`「复制失败」/"Copy failed"。

验收：
- `cd web && bun run check && bunx vitest run src/api/repo-queries.test.tsx` 通过。
- 用例：桩 client 触发 `cb("prj_x")` → `["projectRepo","prj_x","status"]` 失效，`["projectRepo","prj_y"]` 不受影响；`cb(null)` → 全部失效。

---

### 步骤 2.11 — Markdown 支持相对链接与相对图片

依赖：无，因为是独立的组件 API。

文件：
- 修改 `web/src/components/markdown/Markdown.tsx`
- 测试 `web/src/components/markdown/Markdown.test.tsx`

做什么：

1. 导出：
   ```ts
   /** 相对地址的解析（README 等仓库文档）：不带 scheme、不以 `#` 或 `//` 开头的 href / src 交给它；返回 null 时原样输出 */
   export interface RelativeUrls {
       /** 返回站内路由（以 `/` 开头），用 react-router 的 Link 打开，不新开窗口 */
       link(href: string): string | null;
       /** 返回可直接加载的图片地址 */
       image(src: string): string | null;
   }
   ```
   `MarkdownProps`（`:409-413`）加 `relativeTo?: RelativeUrls`。
2. 实现：`const RelativeUrlContext = createContext<RelativeUrls | null>(null)`；`Markdown` 用 Provider 包住 `ReactMarkdown`；`makeComponents`（`:349-405`）里的 `a`、`img` 改成读 context 的小组件。命中且返回非 null 时：链接渲染 `<Link to={…}>`，图片 `src` 换成返回值。其余行为不变：外链照旧 `target="_blank"`。判定函数 `isRelativeUrl(u)`：`!/^[a-z][a-z0-9+.-]*:/i.test(u) && !u.startsWith("#") && !u.startsWith("//")`。`rehype-sanitize` 的缺省 schema 放行相对地址，不用改 `schema`。
3. 测试：给 `relativeTo={{link: h => "/x/"+h, image: s => "/img/"+s}}` → `[a](docs/a.md)` 渲染出 `href="/x/docs/a.md"` 且没有 `target`；`![i](p.png)` 的 src 是 `/img/p.png`；`https://e.com` 不变；不给 `relativeTo` 时输出与现在一致。

验收：`cd web && bunx vitest run src/components/markdown/Markdown.test.tsx` 通过。

---

### 步骤 2.12 — FileDiff 与 GitTarget 解耦

依赖：无。

文件：修改 `web/src/components/changes/FileDiff.tsx`

做什么：

1. 导出：
   ```ts
   /** diff 主体：错误 / 加载 / 二进制 / 过大 / 空 / 虚拟 patch + 截断说明；scrollRef 是外层滚动容器（虚拟列表用） */
   export function PatchView({ diff, error, scrollRef }: { diff: GitFileDiff | null; error: unknown; scrollRef: RefObject<HTMLDivElement | null> }): ReactNode
   /** 文件路径（目录淡色、文件名加粗、重命名带旧路径）与 +x −y */
   export function FileChangeLabel({ file }: { file: GitFileChange }): ReactNode
   ```
   直接从 `FileDiff` 的 JSX（`:128-182` 与 `:113-127`）原样抽出。`FileDiff` 改为组合这两个组件，对外的 props（`target`、`file`、`refresh`、`onBack`）不变，`VirtualPatch` 也不动。
2. 没有新的调用方要迁移：`FileDiff` 现有用法不变。

验收：
- `cd web && bun run check && bunx vitest run src/components/changes` 通过。
- 改动面板截图与改前一致：`ORYN_SHOTS_DIR=/tmp/oryn-shots/p2-diff bun run shots:all --only changes`。

---

### 步骤 2.13 — 只读来源 `projectRepoSource`

依赖：2.10。

文件：
- 修改 `web/src/lib/file-source.ts`
- 测试 `web/src/lib/file-source.test.ts`

做什么：

1. 新函数：
   ```ts
   /** 项目仓库镜像（阶段 ②）：只读；路径是仓库内相对路径，根为 ""；ref 为空 = 服务端的基线 */
   export function projectRepoSource(projectId: string, ref: string): FileSource
   ```
   - `id` 为 `repo:${projectId}:${ref}`；`caps` 只有 `download: true`，其余全为 false。
   - `roots()` 返回 `[{ label: "files.roots.repo", path: "" }]`。
   - `list(path)` 请求 tree（`lastCommits: false`），把 entries 映射成 `FileEntry`：
     - `type`：submodule 映射为 `"other"`。
     - `mtime`：`lastCommit?.ts ?? 0`。
     - `etag`：`"${oid}"`。
     - `target` 原样带上。
   - `stat(path)` 用同一个请求的 `self`，同样映射，并带上 `mime` / `text`。
   - `readText` 用 `readTextRange(repoBlobUrl(…), max)`；`readBlob` 用 `cappedBlob(await rawFetch(url))`；`downloadUrl` 返回 `repoBlobUrl`。
   - `write` / `mkdir` / `rename` / `remove` 一律 reject `new ApiError(409, "read_only", "repository files are read-only")`（同 `orbFileSource` 停机预览的写法，`:452-462`）。
2. 测试：桩 fetch 后验证 `list("")` 的映射、`stat("README.md").mime`、写操作被拒。

验收：`cd web && bunx vitest run src/lib/file-source.test.ts` 通过。

---

### 步骤 2.14 — 项目主页区块与设置「仓库」卡

依赖：2.10、2.11、2.13。

文件：
- 新建 `web/src/pages/projects/home/RepoSyncStatus.tsx`、`RecentCommitsCard.tsx`、`FilesSection.tsx`、`RepoEntryList.tsx`、`ReadmeCard.tsx`、`RepoUnavailable.tsx`、`web/src/pages/projects/repo/paths.ts`
- 修改 `web/src/pages/projects/ProjectHomePage.tsx`、`web/src/pages/projects/home/ProjectIntro.tsx`、`web/src/pages/projects/settings/OverviewTab.tsx`、两个语言文件
- 测试 `web/src/pages/projects/home/RepoBlocks.test.tsx`

做什么：

1. `paths.ts`：
   - `resolveRepoPath(baseDir: string, rel: string): { path: string; hash: string } | null`：先拆掉 `?query` 与 `#hash`；`/` 开头时相对仓库根（GitHub 的语义）；归一化 `.` 与 `..`；越过根 → null。
   - `repoFileHref(projectId, path, ref)` → `/projects/${id}/files/${encodeURI(path)}${ref ? `?ref=${encodeURIComponent(ref)}` : ""}`。
2. `RepoSyncStatus({ project })`：作为 `ProjectIntro` 里 `<p data-testid="project-repo">` 的最后一个子元素（阶段 ① 留的位置），前置图标 `branch`。没有 `repoUrl` 时返回 null（阶段 ① 已在这一行显示 `projects.noRepo`）。各状态：

   | 状态 | 显示 |
   |---|---|
   | `available=false` | `gitMissing` 或 `gitTooOld` |
   | absent | `absent` |
   | cloning | `Spinner` + `cloning` |
   | ready + syncing | `Spinner` + `syncing` |
   | ready | `synced`（`{{ago}}` = `formatRelative(syncedAt)`）；有 `lastError` 时文字后加 `alert` 图标，`title` 为 `staleFailed` |
   | error | `failed`（`{{reason}}` = `t("projectRepo.errors."+code)`） |

   有 repoUrl 且 git 可用时，右侧放 `IconButton icon="refresh" label={t("projectRepo.sync")}`，调用 `useSyncRepo`，飞行中 disabled。
3. `RecentCommitsCard({ projectId, branch })`：
   - 标题 `projectRepo.commits.title`；`useRepoCommits` 第一页取前 5 条。
   - 每行用 `ListRow`（`ui/data.tsx:315-356`）：
     - leading：`commit` 图标。
     - title：subject。
     - meta：等宽 shortSha、关联线程的 `Link to={"/t/"+id}`（`message` 图标 + 标题）、`formatRelative(ts)`。
     - 整行跳 `/projects/:id/commits/:sha`。
   - 右上 `Link` 文案 `projectRepo.commits.viewAll`；空列表用 `projectRepo.commits.none`。
4. `FilesSection({ projectId, status })`：
   - 标题 `projectRepo.files.title`。
   - 子导航三个 `Link`（图标 `commit` / `branch` / `tag`；文案 `projectRepo.nav.commits` / `branches` / `tags`）。
   - 最新提交条：根目录 tree 的 `commit` 去 `useRepoCommit` 取 subject，加 shortSha 与相对时间。
   - 下面是 `RepoEntryList`。
5. `RepoEntryList({ projectId, ref, tree })`：
   - 每行：`folder` / `file` / `link`（符号链接）/ `box`（子模块）图标、名字、淡色截断的 `lastCommit.subject`、相对时间。
   - 点击跳 `repoFileHref`；子模块不可点。
   - `truncated` 时显示 `projectRepo.files.truncated`；`lastCommitsComplete=false` 时缺值的格子显示「—」。
6. `ReadmeCard({ projectId, ref, entries })`：
   - 在根目录的文件里按不分大小写的顺序选：`README.md` > `README.markdown` > `README.mdx` > `README.rst` > `README.txt` > `README`。
   - 用 `projectRepoSource(projectId, ref).readText(path, 512*1024)` 读取。`.md` / `.markdown` / `.mdx` 用 `<Markdown text relativeTo={{ link: h => resolveRepoPath("", h) && repoFileHref(…), image: s => resolveRepoPath("", s) && repoBlobUrl(…) }} />`；其它格式用 `<pre className="whitespace-pre-wrap font-mono text-xs">`。
   - 截断时末尾加 `projectRepo.readme.truncated`；没有 README 时整张卡不渲染。
7. `RepoUnavailable({ project, status })`：只用一个 `EmptyState`（`primitives.tsx:697-715`）：

   | 情况 | 图标 | 标题 | 正文 | 操作 |
   |---|---|---|---|---|
   | 没有 repoUrl | `branch` | `projectRepo.unavailable.noRepoTitle` | `noRepoBody` | `Link` → `/projects/:id/settings/overview`，文案 `openSettings` |
   | git 不可用 | `alert` | `status.gitMissing` / `gitTooOld` | `gitBody` | — |
   | error | `alert` | `errorTitle` | 错误原因 | 「立即同步」 |
   | absent 且已归档 | — | `absentTitle` | `absentBody` | 「立即同步」 |
   | cloning | Spinner | `status.cloning` | `cloningBody` | — |
   | ready 且 empty | — | `status.empty` | `emptyBody` | — |

8. `ProjectHomePage`（在阶段 ① 的结构上加，不改类名）：
   - `RecentCommitsCard` 作为 `data-testid="project-home-columns"` 容器的第二个子元素，排在 `RecentThreadsCard` 之后；容器的 `auto-fit` 网格在够宽时自动成两列，窄时堆叠。
   - 列容器之后、`ServicesCard` 之前依次插入 `FilesSection`、`ReadmeCard`。
   - `status.state !== "ready" || status.empty || !status.available || status.kind === "none"` 时，这三块都不渲染（列容器里只剩 `RecentThreadsCard`，占满整行），在列容器之后、`ServicesCard` 之前渲染一个 `RepoUnavailable`。
   - ref 一律用 `status.baseBranch`。
9. `OverviewTab`：在「基本信息」卡与「危险操作」卡之间加 `Card title={t("projectRepo.settings.title")} icon="branch" data-testid="project-repo-mirror"`，里面是 `RepoMirrorRow`：`state === "ready"` 时显示 `projectRepo.settings.mirror`（`{{ago}}` = `formatRelative(syncedAt)`，`{{size}}` = `formatBytes(sizeBytes)`，`lib/format.ts:56`），其它状态显示 `RepoSyncStatus` 的文字；右侧按钮 `projectRepo.sync`。没有 `repoUrl` 时整张卡不渲染。
10. 新文案（zh-CN / en）：

    | key | zh-CN | en |
    |---|---|---|
    | `projectRepo.status.gitMissing` | 控制面未安装 git | git is not installed on the control plane |
    | `projectRepo.status.gitTooOld` | 控制面的 git 版本过旧（需要 2.31 或更高） | git on the control plane is too old (2.31 or newer required) |
    | `projectRepo.status.absent` | 未克隆 | Not cloned |
    | `projectRepo.status.cloning` | 克隆中… | Cloning… |
    | `projectRepo.status.syncing` | 同步中… | Syncing… |
    | `projectRepo.status.synced` | 已同步 {{ago}} | Synced {{ago}} |
    | `projectRepo.status.failed` | 同步失败：{{reason}} | Sync failed: {{reason}} |
    | `projectRepo.status.staleFailed` | 最近一次同步失败：{{reason}} | Last sync failed: {{reason}} |
    | `projectRepo.status.empty` | 仓库是空的 | The repository is empty |
    | `projectRepo.sync` | 立即同步 | Sync now |
    | `projectRepo.errors.unsupported_url` | 只支持 https:// 仓库地址 | Only https:// repository URLs are supported |
    | `projectRepo.errors.auth_required` | 需要凭据：连接 GitHub 账号或添加 git/<主机>/token 密钥 | Credentials required: connect your GitHub account or add a git/<host>/token secret |
    | `projectRepo.errors.auth_failed` | 凭据被拒绝 | Credentials were rejected |
    | `projectRepo.errors.not_found` | 找不到仓库 | Repository not found |
    | `projectRepo.errors.blocked_address` | 仓库主机解析到内网地址，已拦截 | The repository host resolves to a private address and was blocked |
    | `projectRepo.errors.unreachable` | 连不上仓库主机 | Cannot reach the repository host |
    | `projectRepo.errors.timeout` | 拉取超时 | Fetch timed out |
    | `projectRepo.errors.too_large` | 仓库超过镜像大小上限 | The repository exceeds the mirror size limit |
    | `projectRepo.errors.no_space` | 控制面磁盘空间不足 | The control plane is low on disk space |
    | `projectRepo.errors.fetch_failed` | 拉取失败 | Fetch failed |
    | `projectRepo.unavailable.noRepoTitle` | 还没有关联仓库 | No repository yet |
    | `projectRepo.unavailable.noRepoBody` | 在项目设置里填写仓库地址后，这里会显示提交、文件和 README。 | Add a repository URL in project settings to see commits, files and the README here. |
    | `projectRepo.unavailable.openSettings` | 打开项目设置 | Open project settings |
    | `projectRepo.unavailable.gitBody` | 仓库浏览需要控制面主机安装 git 2.31 或更高版本；线程与其它功能不受影响。 | Repository browsing needs git 2.31+ on the control-plane host; threads and everything else keep working. |
    | `projectRepo.unavailable.errorTitle` | 仓库镜像不可用 | Repository mirror unavailable |
    | `projectRepo.unavailable.absentTitle` | 仓库尚未克隆 | Repository not cloned yet |
    | `projectRepo.unavailable.absentBody` | 已归档项目不会自动克隆，点「立即同步」手动克隆。 | Archived projects are not cloned automatically. Use "Sync now" to clone it. |
    | `projectRepo.unavailable.cloningBody` | 首次克隆可能需要几分钟。 | The first clone can take a few minutes. |
    | `projectRepo.unavailable.emptyBody` | 推送第一个提交后这里会显示内容。 | Push a first commit to see it here. |
    | `projectRepo.commits.title` | 提交到 {{branch}} | Commits to {{branch}} |
    | `projectRepo.commits.viewAll` | 查看全部 | View all |
    | `projectRepo.commits.none` | 还没有提交 | No commits yet |
    | `projectRepo.files.title` | 文件 | Files |
    | `projectRepo.files.truncated` | 条目过多，只显示前 5000 项 | Too many entries; showing the first 5,000 |
    | `projectRepo.nav.commits` | 提交 | Commits |
    | `projectRepo.nav.branches` | 分支 | Branches |
    | `projectRepo.nav.tags` | 标签 | Tags |
    | `projectRepo.readme.truncated` | README 过长，只显示前 512 KB | README is too long; showing the first 512 KB |
    | `projectRepo.settings.mirror` | 镜像 {{size}} · 同步于 {{ago}} | Mirror {{size}} · synced {{ago}} |
    | `projectRepo.settings.title` | 仓库镜像 | Repository mirror |

11. 测试（桩 fetch + `MemoryRouter`，写法同阶段 ① 的 `web/src/pages/projects/ProjectHomePage.test.tsx`）：
    - error 状态显示「同步失败：凭据被拒绝」；`available:false` 只出现一个 EmptyState，而且没有「提交到」。
    - README 里的 `docs/guide.md` 链接指向 `/projects/<id>/files/docs/guide.md?ref=main`；图片 src 是 `…/repo/blob?ref=main&path=docs%2Fdiagram.svg`。
    - 点刷新按钮发出 `POST …/repo/sync`。

验收：
- `cd web && bun run check && bunx vitest run src/pages/projects/home` 通过。
- 用例：mock 下打开 Oryn 项目主页 → 看到「提交到 main」5 行、「文件」6 行、README 里的架构图。

---

### 步骤 2.15 — 仓库子页面与路由

依赖：2.12、2.14。

文件：
- 新建 `web/src/pages/projects/repo/RepoNav.tsx`、`RefPicker.tsx`、`CommitsPage.tsx`、`CommitDetail.tsx`、`BranchesPage.tsx`、`TagsPage.tsx`、`FilesPage.tsx`
- 修改 `web/src/App.tsx`、两个语言文件
- 测试 `web/src/pages/projects/repo/RepoPages.test.tsx`

做什么：

1. 路由：在 `App.tsx` 里紧跟阶段 ① 的 `projects/:projectId/threads` 加平级路由，都用 `lazy`。每个页面自己套 `<ProjectGate>{(p) => <ProjectShell project={p} wide crumbs={[…]}>…}`（步骤 1.5），面包屑分别是「提交」「分支」「标签」「文件」（文案复用 `projectRepo.nav.*` 与 `projectRepo.files.title`）：
   - `projects/:projectId/commits`、`projects/:projectId/commits/:sha` → `CommitsPage`
   - `projects/:projectId/branches` → `BranchesPage`
   - `projects/:projectId/tags` → `TagsPage`
   - `projects/:projectId/files/*` → `FilesPage`

   四个页面都先调 `useRepoStatus`，不可用时渲染 `RepoUnavailable`。
2. `RepoNav`：页头下的子导航，三个 `NavLink`，样式用 `tabClass(selected)`（`primitives.tsx:582`），图标 `commit` / `branch` / `tag`，文案复用 `projectRepo.nav.*`。
3. `RefPicker({ projectId, value, onChange })`：`Menu`（`primitives.tsx:294-313`），两个子菜单「分支」「标签」（`children`），当前项 `checked`；触发钮是 `branch` 图标加 ref 名。
4. `CommitsPage`：
   - 查询参数 `?ref=`（缺省 = 基线）；`useRepoCommits` 的「加载更早的提交」按钮调 `fetchNextPage`（仓库没有通用分页组件，做法同 `useSshAudit`）。
   - expanded（`useBreakpoint()`，`lib/viewport.ts:28`）：左列表右详情；没有 `:sha` 时右侧显示 `projectRepo.commits.selectHint`。
   - 其它宽度：没有 `:sha` 时只显示列表，有 `:sha` 时只显示详情，详情页头有返回 `IconButton icon="back"`。
5. `CommitDetail({ projectId, sha })`：
   - 内容：subject（标题）、body（`pre-wrap`）、作者与 `formatDateTime`、关联线程卡（`ListRow`，`message` 图标 → `/t/:id`，meta 是来源文案 `projectRepo.commit.source.<push|sampled|pull>`）、父提交（`Link` 到各自的短 sha）、`IconButton icon="copy" label={t("projectRepo.commit.copySha")}`、`projectRepo.commit.filesChanged`（`{{count}}`）加 `+x −y`。
   - 文件列表：每个文件一行折叠按钮（`chevronRight` / `chevronDown` + `FileChangeLabel`）；`files.length <= 5` 时默认全部展开。展开后放一个自带滚动的盒子 `max-h-[70dvh] overflow-auto`，里面是 `PatchView`（`diff` 来自 `useRepoCommitDiff`，`scrollRef` 指向这个盒子）。
   - `truncated` 时加 `projectRepo.commit.truncated`。
6. `BranchesPage` / `TagsPage`：`useRepoRefs`，加载更多。每行：名称、`Badge`（`projectRepo.refs.default`，只给默认分支）、头提交（subject、shortSha、相对时间）、「文件」`Link` → `/projects/:id/files?ref=<name>`，文案 `projectRepo.refs.files`。
7. `FilesPage`：
   - 路径取 splat，ref 取 `?ref=`，`useRepoTree(…, lastCommits=true)`。
   - 面包屑：根名用项目名，各段都是 `Link`；`RefPicker` 改的是 `?ref=`。
   - `self.type === "dir"` → `RepoEntryList`。
   - `"file"` / `"symlink"` → `<FileViewer source={useMemo(() => projectRepoSource(id, ref), [id, ref])} path={path} readOnly />`（`FileViewer.tsx:171-187`）。
   - 不用 `FileBrowser`：它把选中项存在 localStorage，不进 URL，也没有「最后提交」列，深链接做不了。
8. 新文案：

   | key | zh-CN | en |
   |---|---|---|
   | `projectRepo.commits.loadMore` | 加载更早的提交 | Load older commits |
   | `projectRepo.commits.selectHint` | 选择一个提交查看改动 | Select a commit to see its changes |
   | `projectRepo.commit.copySha` | 复制 SHA | Copy SHA |
   | `projectRepo.commit.parents` | 父提交 | Parents |
   | `projectRepo.commit.filesChanged_one` | {{count}} 个文件变更 | {{count}} file changed |
   | `projectRepo.commit.filesChanged_other` | {{count}} 个文件变更 | {{count}} files changed |
   | `projectRepo.commit.truncated` | 改动文件超过 2000 个，只列出前 2000 个 | More than 2,000 files changed; showing the first 2,000 |
   | `projectRepo.commit.thread` | 关联线程 | Linked thread |
   | `projectRepo.commit.source.push` | 由该线程推送 | Pushed from this thread |
   | `projectRepo.commit.source.sampled` | 线程里出现过该提交 | Seen in this thread |
   | `projectRepo.commit.source.pull` | 经 PR 合并 | Merged via PR |
   | `projectRepo.refs.default` | 默认 | Default |
   | `projectRepo.refs.files` | 文件 | Files |
   | `projectRepo.refs.loadMore` | 加载更多 | Load more |
   | `projectRepo.refs.none` | 没有标签 | No tags |
   | `projectRepo.refPicker.branches` | 分支 | Branches |
   | `projectRepo.refPicker.tags` | 标签 | Tags |

9. 测试：
   - `/projects/P/commits/<sha>` 在 expanded 下同时有列表和详情，compact 下只有详情。
   - 详情里点开一个文件 → 渲染出 `data-testid="diff-rows"`。
   - `/projects/P/files/docs?ref=main` 列出 `guide.md`；`/files/README.md` 渲染 `FileViewer`，并且没有保存按钮。

验收：
- `cd web && bun run check && bun run test` 通过。
- 用例：mock 下 `/projects/prj_01JCZ0000000000000000ORYN/branches` 显示 `main`（带「默认」徽标）和 `feature/repo-browser`。

---

### 步骤 2.16 — UI 审计面与截图登记

依赖：2.15。

文件：
- 新建 `web/e2e/ui-audit/projects-repo.e2e.ts`
- 修改 `docs/ui-audit/areas/sections.md`、`web/scripts/screenshots.ts`

做什么：

1. 用 `surface({ id, path, ready?, open? })`（`audit.ts:288-300`）登记：

   | id | path | 补充 |
   |---|---|---|
   | `projects.repo.home` | `/projects/prj_01JCZ0000000000000000ORYN` | |
   | `projects.repo.home-error` | oryn-core 的主页 | |
   | `projects.repo.commits` | `…/commits` | |
   | `projects.repo.commit` | `…/commits/<mock 第一个 sha>` | |
   | `projects.repo.ref-picker` | | `open` 点 RefPicker，返回 `lastOverlay(page)` |
   | `projects.repo.branches` | `…/branches` | |
   | `projects.repo.tags` | `…/tags` | |
   | `projects.repo.files` | `…/files/docs` | |
   | `projects.repo.file` | `…/files/README.md` | |

   以上都是 mock 里真实存在的状态。
2. `sections.md`：开头的 spec 列表加上 `projects-repo`；清单表格在阶段 ① 的 `projects.settings.*` 各行之后追加上面 9 行（列：id、界面、打开路径、形态）。
3. `screenshots.ts` 的 `SCREENS`（`:1077` 起）加入 `projects-repo-home`、`projects-repo-commit`、`projects-repo-files`、`projects-repo-file`，写法同 `:1858-1870`：`open(page, url, "<就绪选择器>")`。就绪选择器用组件上的 `data-testid`：`repo-commits-card`、`repo-commit-detail`、`repo-entry-list`、`file-viewer`（新组件的根元素上加这些属性）。

验收：
- `cd web && npx playwright test e2e/ui-audit/projects-repo.e2e.ts` 通过。
- `ORYN_SHOTS_DIR=/tmp/oryn-shots/p2 bun run shots:all --only projects-repo` 生成 1280 与 390 宽、明暗两套共 16 张；390 和 360 宽下目视无溢出。

---

---

### Git LFS（阶段 ② 的补充步骤）

> 摘要：读接口遇到合规的 LFS 指针时，控制面用纯 Go 的 batch 客户端按需取真实对象，取对象用与镜像拉取相同的凭据和传输方式。镜像项目的对象放进有界 LRU 缓存 `<data_dir>/repos/.lfs-cache/`（缺省 2 GiB），超过 100 MiB 的对象只能下载。控制面主机不需要 `git-lfs`。
> 出站代理放行 `<repo>.git/info/lfs/…`：先按 batch 请求的 `operation` 判断读写，写操作与 `git-receive-pack` 遵守同一套规则，再注入令牌。与上游同源的传输地址改写成 orb 侧的 `http://` 地址，凭以短期门票换取注入；其它主机的地址原样交给 orb 直连。令牌不进 orb。
> orb 里 clone 与 repo.update 都不在检出时下载 LFS 对象，之后统一 `git lfs pull`。失败会写进 setup 日志与 `setupError`，线程里显示「环境 setup 失败」。网页给 LFS 文件加标记，并显示真实大小、大文件仅下载和取对象失败几种状态。

依据（协议）：
- 指针格式：https://github.com/git-lfs/git-lfs/blob/main/docs/spec.md
- Batch API：https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md
- Basic 传输：https://github.com/git-lfs/git-lfs/blob/main/docs/api/basic-transfers.md
- 服务器发现：https://github.com/git-lfs/git-lfs/blob/main/docs/api/server-discovery.md
- 认证：https://github.com/git-lfs/git-lfs/blob/main/docs/api/authentication.md
- 文件锁：https://github.com/git-lfs/git-lfs/blob/main/docs/api/locking.md

已核实的 git-lfs 客户端行为（main 分支源码）：
- `lfsapi/endpoint_finder.go`：endpoint = remote URL 套用 `url.*.insteadOf` 之后，加 `.git/info/lfs`（remote 已以 `.git` 结尾时只加 `/info/lfs`）。
- `lfshttp/proxy.go`：按请求 URL 读 `http.<url>.proxy`。
- `tq/adapterbase.go`：
  - `lfs.transfer.enablehrefrewrite` 缺省为 false，action href 原样使用；
  - action 的 `header` 原样设到请求上；
  - `authenticated:true` 时不再找凭据。
- 文件锁校验：
  - `locking/locks.go:272`：`locks/verify` 返回 404 / 501 时报 NotImplemented；
  - `commands/lockverifier.go`：收到 NotImplemented 就静默写 `lfs.<url>.locksverify=false`，推送继续；返回 403 时只警告（状态为 unknown 时）；
  - `commands/uploader.go:321-385`：只有 `https://github.com` 默认开启锁校验（要求 scheme 一致）。
- `commands/command_filter_process.go:42,63`：先 `installHooks(false)`，再判断 `GIT_LFS_SKIP_SMUDGE`，所以跳过 smudge 也照样装 pre-push 钩子。
- `lfshttp/errors.go`：错误体是 JSON 时，显示其中的 `message`。

上游会把请求的 Authorization 原样回显进 action header：
- Gitea：`services/lfs/server.go:508,516,522`；
- GitLab：`app/controllers/repositories/lfs_api_controller.rb:182`。

这是 2.21 必须改写同源 href 的原因。

### 步骤 2.17 — `internal/lfs`：指针、Batch API 类型与错误体

依赖：无。这一步只有纯函数与类型。

文件：新建 `internal/lfs/lfs.go`；测试 `internal/lfs/lfs_test.go`

做什么：
1. 包注释：`// Package lfs 是 Git LFS 指针与 HTTP API 的数据结构（M24）：控制面镜像客户端、托管仓库 LFS 服务端与出站代理共用。`
2. 常量与校验：
   ```go
   const (
       MediaType       = "application/vnd.git-lfs+json"
       PointerMax      = 1024    // 指针文件必须小于 1024 字节（spec.md）
       BatchBodyMax    = 1 << 20 // 批量请求体读取上限
       BatchMaxObjects = 1000
   )
   func ValidOID(s string) bool            // ^[0-9a-f]{64}$
   type Pointer struct{ OID string; Size int64 }
   func ParsePointer(b []byte) (Pointer, bool)
   func (p Pointer) Encode() []byte        // 规范三行形式，测试与 mock 用
   func ObjectPath(root, oid string) string // root/oid[0:2]/oid[2:4]/oid（与 git-lfs 的 .git/lfs/objects 布局相同）
   ```
3. `ParsePointer` 的规则：只认规范的三行指针，其余一律返回 false。
   - 长度：`0 < len(b) < PointerMax`。
   - 内容：`utf8.Valid(b)`，最后一个字节是 `\n`。
   - 去掉末尾换行后按 `\n` 切分，恰好 3 行：
     - 第 1 行是 `version https://git-lfs.github.com/spec/v1` 或 `version https://hawser.github.com/spec/v1`；
     - 第 2 行是 `oid sha256:` 加上满足 `ValidOID` 的串；
     - 第 3 行是 `size ` 加上匹配 `^(0|[1-9][0-9]{0,18})$` 且 `strconv.ParseInt` 成功的数。
   - 带扩展键（`ext-0-…`）或其它键的指针不识别，按普通文件显示。扩展指针要经扩展程序才能还原内容，原样提供对象会给错内容。
   - 不看 `.gitattributes`：控制面 git 最低 2.31，读不到裸仓库在某个提交上的属性（`check-attr --source` 2.40 才有）。
4. Batch 类型，json 名照 batch.md：
   - `Ref{Name}`
   - `BatchRequest{Operation, Transfers []string, Ref *Ref, Objects []ObjectSpec, HashAlgo}`：`Ref` 与 `HashAlgo` 带 `omitempty`，其余字段总是输出。出站代理按这个结构重新序列化请求体（2.21），输出的字段顺序就是声明顺序。
   - `ObjectSpec{OID "oid", Size "size"}`
   - `BatchResponse{Transfer, Objects []ObjectResult, HashAlgo, Message, RequestID, DocumentationURL}`
   - `ObjectResult{OID, Size, Authenticated bool omitempty, Actions map[string]*Action omitempty, Error *ObjectError omitempty}`
   - `Action{Href, Header map[string]string omitempty, ExpiresIn int64 omitempty, ExpiresAt string omitempty}`
   - `ObjectError{Code int, Message}`
5. 另外两个函数：
   ```go
   // Expiry：expires_in>0 优先；否则解析 expires_at（RFC 3339）；都没有 ok=false。
   func (a *Action) Expiry(now time.Time) (time.Time, bool)
   // WriteError 写 LFS 形式的错误：Content-Type MediaType，正文 {"message": msg}。git-lfs 会显示 message。
   func WriteError(w http.ResponseWriter, status int, msg string)
   // ErrorBody 是 WriteError 的正文字节（出站代理在 ModifyResponse 里替换响应体时用）。
   func ErrorBody(msg string) []byte
   ```
6. 测试（表驱动）：
   - 规范指针 → ok；
   - `hawser` 版本 → ok；
   - 缺末尾换行、`\r\n`、大写 oid、`size 01`、多一行 `ext-0-foo x`、1024 字节、空内容 → 全部 false；
   - `Encode` 与 `ParsePointer` 互逆；
   - `ObjectPath("/r","4d7a…")` 为 `/r/4d/7a/4d7a…`。

验收：
- `go test ./internal/lfs/...` 通过。
- 用例：`ParsePointer([]byte("version https://git-lfs.github.com/spec/v1\noid sha256:"+64×"a"+"\nsize 12345\n"))` 返回 `{OID: 64×"a", Size: 12345}, true`。

---

### 步骤 2.18 — 配置键与 LFS 缓存

依赖：2.2、2.4、2.17。

文件：
- 修改 `internal/config/config.go`、`internal/repos/repos.go`、`internal/repos/sync.go`、`internal/server/server.go`
- 新建 `internal/repos/lfscache.go`
- 测试 `internal/config/config_test.go`、`internal/repos/lfscache_test.go`

做什么：
1. config 的 `Repos` 加一个字段：
   ```go
   // MaxLFSCacheGB 是镜像项目 Git LFS 对象缓存的总上限（所有项目合计，按最近使用淘汰）；0（缺省）= 2。
   MaxLFSCacheGB int `yaml:"max_lfs_cache_gb"`
   ```
   `Validate` 加一条：值 < 0 时报 `repos.max_lfs_cache_gb 不能为负`。
2. `repos.Config` 加 `MaxLFSCacheBytes int64 // MaxLFSCacheGB<<30；0 = 2<<30`。`server.go` 的 `repos.Config{…}` 加 `MaxLFSCacheBytes: int64(cmp.Or(cfg.Repos.MaxLFSCacheGB, 2)) << 30`。
3. 缓存（`lfscache.go`）：
   ```go
   // lfsCache 是镜像项目的 LFS 对象缓存 <Dir>/.lfs-cache/<projectID>/<oid[0:2]>/<oid[2:4]>/<oid>（阶段 ② 补充）。
   // 所有项目合计不超过 max（软上限：并发下载期间最多再超出 lfsConcurrency×lfsInlineMax），淘汰最久未用的；
   // 命中时更新文件 mtime，重启后按 mtime 恢复顺序。托管仓库的对象不在这里（步骤 3.13）。
   type lfsCache struct {
       dir   string
       max   int64
       mu    sync.Mutex
       total int64
       lru   *list.List               // *lfsEntry{key string; size int64}，表头 = 最近使用
       byKey map[string]*list.Element // projectID + "/" + oid
   }
   func openLFSCache(dir string, max int64) (*lfsCache, error)
   func (c *lfsCache) path(projectID, oid string) string
   func (c *lfsCache) get(projectID, oid string) (string, bool)
   func (c *lfsCache) reserve(size int64)
   func (c *lfsCache) add(projectID, oid string, size int64)
   func (c *lfsCache) drop(projectID, oid string)
   func (c *lfsCache) removeProject(projectID string)
   func (c *lfsCache) prune(keep func(projectID string) bool)
   ```
   各方法的行为：
   - `openLFSCache`：`MkdirAll(dir, 0o700)`；删掉 `*.trash-*` 目录，以及各项目目录下的 `tmp-*`；扫描其余文件，名字须满足 `lfs.ValidOID`，按 mtime 从新到旧入表；超出上限就淘汰。
   - `get`：命中时移到表头，并 `os.Chtimes(now)`（失败忽略）。
   - `reserve`：从表尾逐个 `os.Remove`，直到 `total+size ≤ max`。
   - `drop`：用于文件被外部删掉、`os.Open` 返回 ENOENT 的时候。
   - `removeProject`：去掉该项目的全部条目；目录改名为 `<pid>.trash-<rand>`，由 goroutine `RemoveAll`。
   - `prune`：`keep` 为假的项目目录整个删掉。

   淘汰正在被读的文件没有问题：读取方先 `os.Open` 再服务，Linux 上已打开的文件删除后仍可读完。
4. 接入：
   - `repos.New` 在 git 可用时调用 `openLFSCache(filepath.Join(cfg.Dir, ".lfs-cache"), cfg.MaxLFSCacheBytes)`，放进 `Service.lfsCache`。
   - 2.4 的 `Remove`，以及「改地址 → 删除镜像」用的同一个删除逻辑，末尾都加 `s.lfsCache.removeProject(id)`。
   - `Run` 启动清理加一句 `s.lfsCache.prune(…)`：`GetProject` 返回 `sql.ErrNoRows` 的项目不保留。
   - `dirSize` 不变：缓存在 `<id>.git` 之外，不计入镜像的 `size_bytes`，也不参与 `MaxBytes`（5 GiB）判定。镜像因 `too_large` 被删时，经同一删除逻辑一并清掉缓存。
5. 测试：
   - config：`max_lfs_cache_gb: -1` → 报错。
   - 缓存：
     - `max=10`，先后 `add` 4、4 字节，`get` 第一个，再 `reserve(4)`+`add` → 第二个被删；
     - 重启（重新 `openLFSCache`）后 `total=8`，残留的 `tmp-x` 被删；
     - `removeProject` → 目录不在；
     - `prune` 删掉不存在项目的目录。

验收：
- `go test ./internal/config/... ./internal/repos/ -run LFSCache` 通过。
- 用例：`repos: {max_lfs_cache_gb: 3}` → `MaxLFSCacheBytes == 3<<30`。

---

### 步骤 2.19 — 控制面 LFS 客户端（镜像项目按需取对象）

依赖：2.18。

文件：
- 新建 `internal/repos/lfsfetch.go`、`internal/github/githubtest/lfs.go`
- 修改 `internal/repos/repos.go`（字段与错误）、`internal/repos/git.go`（`remote` 结构体）
- 测试 `internal/repos/lfsfetch_test.go`

做什么：
1. 常量与错误：
   ```go
   const (
       lfsInlineMax      = 100 << 20        // 超过它的对象不内联预览、不进缓存，只能 download=1 流式下载
       lfsBatchTimeout   = 30 * time.Second
       lfsLowSpeedBytes  = 60_000           // 60 s 窗口内少于这么多字节即中止（同 GIT_HTTP_LOW_SPEED_*）
       lfsLowSpeedWindow = 60 * time.Second
       lfsConcurrency    = 4                // 同时向上游取对象的个数
       lfsPerProject     = lfsConcurrency / 2 // 同一项目同时占用的名额上限
   )
   var (
       errLFSMissing     = &Error{Status: 404, Code: "lfs_missing", Message: "找不到这个 LFS 对象"}
       errLFSAuth        = &Error{Status: 409, Code: "lfs_auth_failed", Message: "上游拒绝了读取 LFS 对象的凭据"}
       errLFSUnreachable = &Error{Status: 502, Code: "lfs_unreachable", Message: "连不上存放 LFS 对象的主机"}
       errLFSCorrupt     = &Error{Status: 502, Code: "lfs_corrupt", Message: "上游返回的内容与 LFS 指针不符"}
       errLFSNoSpace     = &Error{Status: 507, Code: "lfs_no_space", Message: "控制面磁盘空间不足，暂时不能读取 LFS 对象"}
   )
   func lfsErr(base *Error, detail string) *Error // 复制 base，Message 追加「：<detail>」；detail 先过 redactSecrets
   ```
   - `repos.Config` 加 `LFSFlightTimeout time.Duration // 一次取对象（batch、下载、落盘）的总时限；0 = 15 分钟`。`server.go` 不赋值，只有测试改它。
2. 凭据。LFS 只用 2.4 的 `remote(ctx, prj)`，取其中四项：拉取地址、主机、令牌、是否担保主机。2.4 实现里字段名不同就按实际名字用，缺哪项就在 `remote()` 里补上。
   - 阶段 ④ 4.4 只改 `remote()` 内部的取令牌顺序，本步不用再动。
   - 托管项目（阶段 ③）不调 `remote()`，见步骤 3.13。
3. 传输。在 `New` 里、git 可用时建两个 `*http.Transport`：
   - `s.lfsVouched`：`Proxy` 由 `Config.ProxyEnv` 构造 `golang.org/x/net/http/httpproxy.Config`（`HTTPS_PROXY`/`https_proxy`、`HTTP_PROXY`/`http_proxy`、`NO_PROXY`/`no_proxy`），再取 `.ProxyFunc()(req.URL)`。`x/net` 已是直接依赖，见 `go.mod:24`。语义与 git 子进程拿到 `ProxyEnv` 时相同。
   - `s.lfsGuarded`：`Proxy: http.ProxyURL(<PublicProxy.URL()>)`。2.3 的守卫只接受 CONNECT 443，只连公网地址，并在连接时检查实际 IP。
   - 两者都设 `DialContext` 超时 15 s、`TLSHandshakeTimeout` 15 s、`ResponseHeaderTimeout` 30 s、`MaxIdleConnsPerHost` 4。

   每次取对象用一个 `http.Client`，Transport 为 `lfsRouter{endpointHost, vouched}`，逐跳选择：
   - `req.URL.Host` 与 endpoint 主机相同（不区分大小写，按 scheme 补缺省端口后比较）：担保主机用 `lfsVouched`，未担保主机用 `lfsGuarded`；
   - 其它主机：一律走 `lfsGuarded`，所以上游给出的内网 href 会被守卫拒绝。
4. 批量请求（`lfsDownloadAction(ctx, rm remote, p lfs.Pointer) (lfs.Action, error)`）：
   - endpoint = 拉取地址去掉末尾 `/`，不以 `.git` 结尾时补 `.git`，再加 `/info/lfs`。github.com 用 2.4 的 `GitHubWebURL` 形式。仓库里的 `.lfsconfig`（`lfs.url`）不读，令牌只发给它所属的主机。
   - 请求：`POST <endpoint>/objects/batch`。
     - 头：`Accept`、`Content-Type` 都是 `lfs.MediaType`；`User-Agent: oryn-repos`；有令牌时加 `Authorization: Basic base64("x-access-token:"+tok)`，与 2.4 的 git 拉取相同。
     - 体：`{"operation":"download","transfers":["basic"],"objects":[{"oid":…,"size":…}],"hash_algo":"sha256"}`。
     - 超时 `lfsBatchTimeout`；`CheckRedirect` 返回 `http.ErrUseLastResponse`，与 git 的 `http.followRedirects=false` 一致。
   - 结果映射：

     | 上游 | 结果 |
     |---|---|
     | 传输错误、超时、3xx、429、5xx、守卫回 403 | `errLFSUnreachable` |
     | 401 / 403 | `errLFSAuth` |
     | 404 | `errLFSMissing`（上游没有 LFS 服务或看不到仓库） |
     | 其它 4xx | `errLFSCorrupt` |
     | 200 但体超过 1 MiB、不是 JSON、`transfer` 非空且不是 `basic`、找不到同 oid 的对象、缺 `actions.download` | `errLFSCorrupt` |
     | 对象 `error.code` 404 / 410 | `errLFSMissing` |
     | 对象 `error.code` 401 / 403 | `errLFSAuth` |
     | 对象其它 `error.code` | `errLFSCorrupt` |
5. 下载（`lfsGet(ctx, rm, p, a lfs.Action) (io.ReadCloser, error)`）：
   - href 必须是绝对地址、不带 userinfo。scheme 须为 `https`，或者与 endpoint 同源（只有测试用的 http 假上游会这样）。否则返回 `lfsErr(errLFSUnreachable, "传输地址不是 https")`。
   - 请求头只放 `a.Header`，Oryn 不另加凭据。href 主机与 endpoint 主机不同时，值里含令牌或它的 Basic 串的头一律丢掉，这样回显型上游把令牌指向别的主机也带不出去。
   - `CheckRedirect`：最多 5 跳；拒绝 https 降级为 http；换主机时清空全部请求头。
   - 响应 200 才算成功；404 / 410 → missing；401 / 403 → auth；其余 → unreachable。
   - 低速保护：每 `lfsLowSpeedWindow` 检查一次，窗口内读到的字节不足 `lfsLowSpeedBytes` 就取消请求 ctx，结果为 `lfsErr(errLFSUnreachable, "下载过慢")`。
   - 返回的读取器边读边算 sha256 与字节数：超过 `p.Size` 立即报错；到 EOF 时字节数不等于 `p.Size` 或摘要不等于 oid，`Read` 返回 `errLFSCorrupt`。
6. 取对象：
   ```go
   // lfsObject 打开项目的 LFS 对象（调用方保证 p.Size ≤ lfsInlineMax，或项目是托管项目）：镜像项目先查缓存，
   // 未命中时向上游取并写入缓存；同一 projectID+oid 单飞。调用方关闭文件。
   func (s *Service) lfsObject(ctx context.Context, prj db.Project, p lfs.Pointer) (*os.File, error)
   // lfsOpenStream 不经缓存从上游流式读取（> lfsInlineMax 的 download=1）；校验规则同 lfsGet。
   func (s *Service) lfsOpenStream(ctx context.Context, prj db.Project, p lfs.Pointer) (io.ReadCloser, error)
   ```
   - `p.Size == 0`：oid 必须等于空内容的 sha256（`e3b0c442…b855`），否则 `errLFSCorrupt`；返回空文件（`os.Open(os.DevNull)`），不访问上游。
   - 单飞：
     - `Service` 加 `lfsMu sync.Mutex`、`lfsFlights map[string]*lfsFlight`（`{done chan struct{}; err error}`）、`lfsSem chan struct{}`（容量 `lfsConcurrency`）、`lfsProj map[string]chan struct{}`（每个项目一个容量 `lfsPerProject` 的信号量，按需创建，`Remove` 时删掉），以及在 `New` 里建、`Close` 时取消的 `lfsCtx`。
     - 飞行在 `lfsCtx` 下运行，不随某个浏览器请求取消；等待方等 `done` 或自己的 ctx。
     - 每次飞行从占到名额起有 `Config.LFSFlightTimeout` 的总时限，到时结果为 `lfsErr(errLFSUnreachable, "下载超时")`。只靠低速阈值不够：上游以略高于 1 KB/s 的速度喂一个 100 MiB 的对象，能占一个名额约 29 小时。
   - 一次飞行的步骤：
     1. 占名额：先占本项目的信号量，再占全局的 `lfsSem`。一个项目的慢上游最多占一半名额，别的项目的 LFS 预览不会跟着挂起；
     2. 磁盘检查：`statfs(Dir)` 的可用空间减去 `p.Size` 后低于 `Config.MinFreeBytes`（阶段 ② 的 `repos.min_free_gb`）→ `errLFSNoSpace`，不访问上游。写入量以 `p.Size`（不超过 100 MiB）为上界，所以不用中途复查；
     3. `lfsDownloadAction` → `lfsGet`；
     4. 写到 `<cache>/<pid>/tmp-<oid>-<rand>`（0600）；
     5. 校验通过后 `Sync`、`reserve(size)`，再 `os.Rename` 到 `path(pid, oid)`；
     6. `add`。
   - 任一步失败就删掉 tmp，错误交给全部等待方。错误不缓存，下次请求重新尝试。
   - 命中缓存但 `os.Open` 返回 ENOENT 时先 `drop`，再按未命中处理。
7. 假上游（`githubtest/lfs.go`），供本包与 2.20 测试用：
   ```go
   func (s *Server) PutLFS(oid string, content []byte) // oid 由调用方给出，可以故意不符（测损坏）
   // LFSHrefOrigin 非空时 download href 用它作 origin（模拟对象存储在别的主机）；空 = 自己的 URL。
   // LFSEchoAuth 为真时 action.header 回显请求的 Authorization（Gitea / GitLab 的做法）。
   LFSHrefOrigin string
   LFSEchoAuth   bool
   LFSStall      bool // 为真时对象请求写完响应头后挂起，直到请求被取消（测总时限与名额）
   ```
   - 路由：`POST /{owner}/{repo}.git/info/lfs/objects/batch` 按 batch.md 应答，未存的对象回对象级 404；`GET /_lfs/objects/{oid}` 返回内容。
   - 两者都记进 `Calls`。在 `extra.go:20` 的 `gitPathRe` 匹配之前分流。
8. 测试（`requireGit`；镜像用 `githubtest` 的 `acme/widgets`，`commitTo` 写入 `lfs.Pointer{oid, n}.Encode()` 作为 `big.bin`）：
   - 首次 `lfsObject` → 内容正确。fake 记到的 batch 请求带 `Authorization: Basic …`（解码为 `x-access-token:<tok>`），`/_lfs/objects/…` 请求不带 `Authorization`。
   - 第二次 → batch 调用数不变（命中缓存）。并发 5 个 → batch 只调用 1 次。
   - `PutLFS(oid, 别的内容)` → `lfs_corrupt`，缓存里没有该文件。未 `PutLFS` → `lfs_missing`。
   - `LFSHrefOrigin = <另一个 httptest TLS 服务器的 URL>`（127.0.0.1，非 443 端口）→ `lfs_unreachable`，而且那台服务器收到 0 个请求（守卫拦截）。
   - `LFSEchoAuth=true` 且同源 → 下载请求带回显的头，成功。
   - 头过滤单测：href 在别的主机、header 里含令牌 → 该头被丢弃。
   - `MaxLFSCacheBytes = 对象大小` → 再取第二个对象后，第一个被淘汰。
   - `Remove` → `.lfs-cache/<pid>` 不在。
   - `LFSStall=true` 加 `Config.LFSFlightTimeout=200ms` → `lfs_unreachable`，`.lfs-cache/<pid>` 里没有 tmp 文件。
   - `LFSStall=true` 时对同一项目并发取 4 个不同对象 → fake 同时在途的对象请求是 2 个；这时另一个项目的 `lfsObject` 仍能发出自己的对象请求。
   - `MinFreeBytes` 大于磁盘可用空间 → `lfs_no_space`，fake 没有收到 batch 请求。

验收：
- `go test ./internal/repos/ -run LFS` 通过。
- 用例：fake 存 `hello lfs\n`、指针 `size 10` → `lfsObject` 读出 `hello lfs\n`，`<data_dir>/repos/.lfs-cache/<pid>/<oid[0:2]>/<oid[2:4]>/<oid>` 存在。

---

### 步骤 2.20 — 读接口识别 LFS 指针

依赖：2.8、2.19。

文件：
- 修改 `schema/openapi.yaml`、`internal/repos/read.go`、`internal/api/projectrepo.go`
- 测试 `internal/repos/read_test.go`、`internal/api/projectrepo_test.go`

做什么：
1. OpenAPI：
   ```yaml
   ProjectRepoLfs:
     type: object
     description: 文件是 Git LFS 指针（阶段 ② 补充）；此时条目的 size 是对象的真实大小，oid 仍是指针 blob 的 sha
     required: [oid]
     properties:
       oid: { type: string, pattern: '^[0-9a-f]{64}$', description: LFS 对象的 sha256 }
       error:
         type: string
         enum: [lfs_missing, lfs_auth_failed, lfs_unreachable, lfs_corrupt, lfs_no_space]
         description: 只出现在 self 上：取对象失败的原因（对象 ≤ 100 MiB 时 tree 先取对象）
   RepoDownloadQuery:
     name: download
     in: query
     required: false
     schema: { type: boolean, default: false }
     description: 为 true 时一律 Content-Disposition attachment，并允许下载超过 100 MiB 的 Git LFS 对象
   ```
   - `ProjectRepoTreeEntry.properties` 加 `lfs: { $ref: '#/components/schemas/ProjectRepoLfs' }`。
   - `getProjectRepoBlob` 的参数加 `$ref: '#/components/parameters/RepoDownloadQuery'`。描述补一句：「Git LFS 指针返回对象内容；对象是本地文件时支持 Range」。
   - `Error.code` 描述补上 `lfs_missing`（404）、`lfs_auth_failed`（409）、`lfs_unreachable`（502）、`lfs_corrupt`（502）、`lfs_no_space`（507）。
2. Tree：
   - 目录条目里 `type=file`、`0 < size < lfs.PointerMax` 的条目，用一次 `git cat-file --batch` 读出内容：stdin 是这些 oid，每行一个；stdout 上限 `n*(lfs.PointerMax+64)`。逐条 `ParsePointer`，命中的条目 `size` 改为对象大小，并带上 `lfs.oid`。目录条目从不取对象。
   - self 是文件且命中指针（2.8 本来就读前 8 KiB）：`size` 与 `lfs.oid` 同上。
     - `size ≤ lfsInlineMax` 或项目是托管项目时，调 `lfsObject`，按对象内容决定 `mime`（扩展名优先，退回前 512 B 的 `http.DetectContentType`）与 `text`（前 8 KiB 无 NUL 且是合法 UTF-8）。
     - 失败时 `lfs.error = (*Error).Code`，省略 `text`，`mime` 只按扩展名。
     - `> lfsInlineMax` 时不取对象，`mime` 只按扩展名，省略 `text`。
3. Blob。2.8 的 `Blob` 改为返回下面的结构，handler 随之改；2.8 已有的结果字段并进来：
   ```go
   // BlobContent 是 Blob 的结果：File 与 Body 恰有一个非 nil，调用方关闭。
   type BlobContent struct {
       OID, Name, ContentType string
       Size                   int64     // LFS 时为对象大小
       Inline, Immutable      bool
       File                   *os.File  // 本地文件（镜像缓存、托管库对象）：handler 用 http.ServeContent，支持 Range
       Body                   io.ReadCloser // git cat-file 或上游流：handler io.Copy，出错即 panic(http.ErrAbortHandler)
   }
   func (s *Service) Blob(ctx context.Context, projectID, ref, path string, download bool) (*BlobContent, error)
   ```
   - 解析 oid 与 304 判定照 2.8，在取对象之前做。
   - 先看 `cat-file -s`：小于 `PointerMax` 时读出内容并 `ParsePointer`。不是指针就走 2.8 原逻辑（64 MiB 上限不变）。
   - 是指针时：
     - `size > lfsInlineMax && !download` → 413 `too_large`，消息「Git LFS 对象超过 100 MiB，只能下载（download=1）」；
     - 否则镜像项目在 `size ≤ lfsInlineMax` 时用 `lfsObject`（`File`），超过时用 `lfsOpenStream`（`Body`，`Content-Length` = 对象大小，校验失败即中止连接，浏览器把下载标为失败）。
   - `ContentType` 与 `Inline` 照 2.8 规则，但作用于对象内容；`download=1` 时一律 `attachment`，这条也适用于普通 blob。
   - ETag 仍是指针 blob 的 oid：指针唯一决定对象。
4. 提交 diff 照旧显示指针文本的改动，不取对象。
5. 测试：
   - `read_test.go`：
     - `docs/big.bin` 是 LFS → 目录条目 `size` 是真实大小，带 `lfs.oid`，且没有 batch 调用；
     - self → `text=true`、`mime` 正确；
     - Blob → 内容正确，二次请求带 `Range: bytes=0-3` → 206；
     - 未存对象 → self 的 `lfs.error=lfs_missing`，Blob 404 `lfs_missing`；
     - 测试字段 `lfsInlineMaxTest=4` → 不带 download 时 413，`download=1` → 200 流式，内容正确；
     - 损坏对象 + `download=1` → `io.Copy` 报错；
     - 普通 blob + `download=1` → `attachment`。
   - `projectrepo_test.go`：`GET …/repo/blob?path=docs/big.bin` 的 `Content-Length` 等于对象大小。

验收：
- `make gen && make lint-spec && go test ./internal/repos/... ./internal/api/...` 通过。
- 用例：`GET …/repo/tree?path=docs` → `{"name":"big.bin","type":"file","size":10,"lfs":{"oid":"<64 hex>"},…}`。

---

### 步骤 2.21 — 出站代理放行 Git LFS

依赖：2.7（`target.gitHost`）、2.17。

文件：
- 新建 `internal/egress/lfs.go`
- 修改 `internal/egress/egress.go`
- 测试 `internal/egress/lfs_test.go`

做什么：
1. 端点分类（`lfs.go`），取代 `resolveGit` 里对 `gitEndpoint` 的直接调用：
   ```go
   const (
       lfsBatchRespMax = 4 << 20 // batch 响应的读取上限；超出回 502（失败关闭）
       lfsTicketMax    = 4096    // 每台 orb 的门票上限
       lfsTicketTTL    = time.Hour
   )
   // gitReq 是 classifyGit 认出的 git 请求（smart-HTTP 或 Git LFS）。
   type gitReq struct {
       repo     string // NormalizeRepo 形式
       write    bool
       endpoint string // 相对 <repo>.git/ 的端点：info/refs、git-upload-pack、git-receive-pack、info/lfs/objects/batch、info/lfs/locks…
       lfs      bool   // info/lfs/ 端点：拒绝时回 LFS JSON 错误体
       lfsBatch bool   // objects/batch：请求体已由 lfsBatchRequest 规范化，响应经 lfsRewrite
       reject   int    // 非 0：batch 请求体不可接受，直接以这个状态码（413 / 415 / 422）回 LFS 错误体
       rejectMsg string
   }
   func classifyGit(r *http.Request, rest string) (gitReq, bool)
   // lfsBatchRequest 解析 batch 请求体，并把 r.Body 换成对解析结果的重新序列化（同时重设 ContentLength 与 Content-Length 头）：
   // 上游收到的字节就是代理据以判断读写的那一份，两边不会对同一个请求体读出不同的 operation。
   // status 为 0 表示可以转发；否则是应当回给 orb 的状态码，msg 是说明。
   func lfsBatchRequest(r *http.Request) (req lfs.BatchRequest, status int, msg string)
   ```
   - `lfsBatchRequest` 的规则（`classifyGit` 认出 batch 端点后调用，不可接受时记进 `gitReq.reject` / `rejectMsg`）：
     - 带 `Content-Encoding`（identity 除外）→ 415 `oryn: 批量请求不能压缩`；
     - 请求体超过 `lfs.BatchBodyMax`，或 `objects` 多于 `lfs.BatchMaxObjects` → 413 `oryn: 批量请求过大`；
     - 不是合法 JSON、`operation` 不是 `download` / `upload`、`hash_algo` 不是空或 `sha256`、某个对象的 oid 不满足 `lfs.ValidOID` 或 `size < 0` → 422 `oryn: 批量请求无效`；
     - 通过后重新序列化：`transfers` 固定为 `["basic"]`，`objects` 为 nil 时换成空切片，只保留 `operation`、`ref.name`、`objects[].oid/size`、`hash_algo`，其余字段丢弃。
     - 为什么重写而不是原样转发：Go 的 `encoding/json` 匹配字段名不分大小写、重复键取后一个，上游的解析器未必如此。`{"operation":"upload","OPERATION":"download"}` 原样转发时，代理读到 download 而上游可能读到 upload，只读的 orb 就拿到了上传地址。oid 在这里校验，是因为有的上游（GitLab）把请求里的 oid 直接拼进 href。
   - smart-HTTP 端点：照搬 `gitEndpoint`（`egress.go:545-566`）。
   - LFS 端点：`rest` 里含 `.git/info/lfs/`，前缀经 `NormalizeRepo` 后至少含一个 `/`。git-lfs 的 endpoint 一定是 `<remote>.git/info/lfs`（server-discovery.md），所以要求有 `.git`。

     | 后缀 | 方法 | 读写 |
     |---|---|---|
     | `info/lfs/objects/batch` | POST | `lfsBatchRequest` 得到 `download` 是读，`upload` 是写；请求体不可接受时不转发（`reject`） |
     | `info/lfs/locks` | GET / POST | GET 读，POST（建锁）写 |
     | `info/lfs/locks/verify` | POST | 写（locking.md：要求推送权限） |
     | `info/lfs/locks/<id>/unlock`（`id` 匹配 `^[A-Za-z0-9._-]{1,128}$`） | POST | 写 |

     其它方法或路径都不认。git-lfs 3 已删掉 v1 的非 batch 接口，对 remote 主机不会再调别的端点。镜像里的 git-lfs 来自 Arch 仓库（3.x），2.22 的冒烟测试断言主版本号。
2. `resolve`（`egress.go:367-461`）：
   - 代理形式分支（`:370-379`）在 `canonicalPath` 检查之后直接 `return h.resolveGit(…)`。
   - 删掉这里的 `gitEndpoint` 预检，挪进 `resolveGit`。门票也在 `resolveGit` 里查，排在端点分类之后（见第 3 条）。
3. `resolveGit`（`:478-530`）：
   ```go
   req, ok := classifyGit(r, rest)
   if !ok {
       if r.URL.Host == "" { return target{}, http.StatusNotFound }
       // 只有 classifyGit 不认的路径才查门票：门票不能遮蔽已识别的 git / LFS 端点。
       if t, st, ok := h.lfsTicketTarget(r, pol, host); ok { return t, st }
       return target{deny: denyProxyNotGit}, http.StatusForbidden
   }
   if req.reject != 0 { return target{deny: req.rejectMsg, lfs: true}, req.reject }
   ```
   - `classifyGit` 认得而 `gitAllowed` 拒绝的请求直接拒绝，不回落到门票。上游若给出形如 `…/other/repo.git/git-receive-pack` 的同源 href，orb 按它发出的请求会被当作普通推送来检查（仓库限制、只读评审、推送门禁），而不是凭门票放行。
   - 把 `:497-507` 的仓库限制与 ReviewOnly 判断抽成 `func (h *Handler) gitAllowed(pol Policy, host, repo string, write, receivePackPost bool) (deny string, status int)`（`status == 0` 表示放行），行为不变：
     - PAT 主机：有 `GitRepos` 时只放行项目仓库；没有时写操作一律 403。
     - github.com 开放时：写操作且不是 receive-pack POST，并且 `ReviewOnly` → `denyReviewOnly`。LFS 上传、建锁、解锁、`locks/verify` 都属于这一类。
   - 拒绝时 `target{deny: deny, lfs: req.lfs}`。
   - 放行的 target 加 `lfs: req.lfs, lfsBatch: req.lfsBatch, stripEncoding: req.lfsBatch`：上游 gzip 时由 Transport 解压，改写读到的是明文。
   - 令牌：每个请求都调 `h.R.GitToken(host)`，注入与脱敏照旧（`:508-523`）。阶段 ④ 的「项目 → 全局」在 `egressResolver.GitToken` 里完成，这里不用改。
4. `target` 加字段 `lfs, lfsBatch bool`。`ServeHTTP` 的拒绝分支在 `t.lfs` 时改用 `lfs.WriteError(w, status, msg)`，让 git-lfs 显示 `oryn: <原因>`（`lfshttp/errors.go`）。`denyProxyNotGit`（`:463`）的文案改为：「代理形式的请求只接受 git smart-HTTP（info/refs、git-upload-pack、git-receive-pack）、Git LFS 接口（<仓库>.git/info/lfs/…）与 LFS 批量接口给出的传输地址」。
5. 改写 batch 响应（失败关闭）。有两件事使它必须做：
   - 回显型上游（Gitea `server.go:508`）把注入的 Authorization 放进 action header；脱敏后 orb 拿到的是 `Basic oryn-redacted`，直连 https 必然 401，又不能把令牌交给 orb。
   - 有的上游会新签发凭据。GitLab 用 PAT 鉴权时，在 action header 里放一份新签的 `Basic base64(用户名:LfsToken)`（`lfs_api_controller.rb` 的 `lfs_auth_header`），有效 2 小时，可以用来 git 推送。它不在 `t.redact` 里，流式脱敏认不出；原样转发的话，只读的 orb 拿它就能直连上游推送。

   所以 batch 的成功响应一律解析后重写，认不出的内容不放行：
   ```go
   // lfsRewrite（M24）把上游对 objects/batch 的 200 响应换成代理重新序列化的 lfs.BatchResponse：
   // 与上游同主机的 action 去掉凭据头并改写成经代理的地址（登记门票），不能改写的删掉；其它主机的 action 只去掉带已知令牌的头。
   // 响应无法解析时把它换成 502 的 LFS 错误体。
   func (h *Handler) lfsRewrite(resp *http.Response, t target)
   ```
   - 调用位置：`ModifyResponse`（`:272-301`）在 `mcpRedirect` 之后、脱敏之前。`t.lfsBatch` 时：
     - 200 → `h.lfsRewrite(resp, t)`；
     - 其它 2xx → 换成 502（batch.md 只定义 200）；
     - 非 2xx 原样转发（错误体里没有 action）。

     之后的流式脱敏照常运行。
   - 换成 502 的做法：关闭原来的体；`StatusCode=502`、`Status="502 Bad Gateway"`；头只留 `Content-Type: lfs.MediaType` 与 `Content-Length`；体为 `lfs.ErrorBody("oryn: 上游的 LFS 批量响应无法解析")`。
   - 解析：`Content-Encoding` 不是空或 identity、读到 `lfsBatchRespMax` 还没结束、不是合法 JSON → 502。
   - 逐个 action（`download` / `upload` / `verify`）：

     | href | 处理 |
     |---|---|
     | 不是绝对的 http / https 地址，或带 userinfo | 删掉这个 action |
     | 主机名与上游（`t.url`）相同，scheme 与端口也相同，并且解码后的路径规范（`canonicalPath`） | `header` 只保留 `Accept`、`Content-Type`、`Transfer-Encoding`，其余全删（凭据由代理注入）；href 改写为 `http://<t.gitHost><EscapedPath>[?RawQuery]` 并登记门票 |
     | 主机名与上游相同，但 scheme 或端口不同，或路径不规范 | 删掉这个 action |
     | 其它主机 | href 不动；删掉带已知令牌的头：值本身、或它按 `Basic <base64>` 解出的明文，含 `t.redact` 任一项 |

   - 这三种 action 里有被删掉的，该对象的 `actions` 清空，并设 `error = {code: 422, message: "oryn: 上游给出的传输地址不能经代理访问"}`，让 git-lfs 报错而不是当作无需传输。其它名字的 action 直接丢掉（没有对应的请求方法），不影响同对象的其余 action。
   - 其它主机的 action 里，上游给对象存储签发的头（预签名、短期令牌）仍会交给 orb：这是「其它主机的地址由 orb 直连」这一取舍的一部分。
   - 门票：
     - 键：`<方法> http://<gitHost><EscapedPath>[?RawQuery]`，方法是 download→GET、upload→PUT、verify→POST。
     - 值：`lfsTicket{upstream *url.URL; host, repo string; write bool; expires time.Time}`，其中 `write` = action 不是 download。
     - 有效期取 `min(now+lfsTicketTTL, Action.Expiry)`。已过期的同主机 action 按「删掉这个 action」处理。
     - 存储：`Handler` 加 `lfsMu sync.Mutex`、`lfsTickets map[string]lfsTicket`、`now func() time.Time`（nil 时用 `time.Now`，只有测试赋值）。Handler 每台 orb 一个、长期存在（`orbs/egress.go:351`）。超过 `lfsTicketMax` 时先删过期的，再删最早到期的。
   - 重新序列化后设 `ContentLength` 与 `Content-Length`，删掉 `Content-Encoding` 与 `ETag`。未知字段会丢，git-lfs 不读它们。
6. 使用门票（`lfsTicketTarget(r, pol, host) (target, int, bool)`）：
   - 只在 `classifyGit` 不认该路径时被调用（第 3 条）。未命中或过期 → `ok=false`，`resolveGit` 回 `denyProxyNotGit`。
   - 命中：
     - 按当前策略重查 `gitAllowed(pol, tk.host, tk.repo, tk.write, false)`，策略收窄、ReviewOnly 变化都会生效；
     - 重新取 `GitToken(tk.host)`，拿不到回 404；
     - target 为 `{url: tk.upstream, auth, redact, gitHost: host, lfs: true}`；github.com 开放时再加 `ghToken/ghAccess`，401 时照常调 `GitHubTokenRejected`。
   - 查询串由键保证与上游一致，`Rewrite` 沿用入站的 RawQuery。
7. 其余不变的地方：
   - 审计：git 路由本来就没有逐请求审计，LFS 也没有。github.com 的 LFS 上传不登记 `GitHubWrite`：防回流对应的是 webhook，LFS 上传不改 ref、不产生事件。
   - 门禁：推送门禁与 prepare 通过记录只对 ref 更新求值（`gate.go:266-296`）。对象要被 ref 引用，必须经随后的 `git-receive-pack`，那一步照常过门禁。
   - `PushRecorder`（2.7）不涉及。
   - M15 之前的 orbd（路径形式 `/git/<host>/…`）：batch 与锁接口能用，同主机传输地址用不了（改写后的 `http://` 地址不经它的代理）。
8. Resolver 与策略的接触点：
   - 用到：`Policy()` 的 `NoGit`、`GitRepos`、`GitHub`；`GitToken`；`ThreadGates`（经 `h.threadGates()`）；`GitHubTokenRejected`。
   - 不用：`SecretByID`、`GitHubWriteBegin/End`、`PushRecorder`、`HostedGit`（步骤 3.14 才用）。
   - 不加 Resolver 方法，所以没有实现方要改。
9. 测试（`lfs_test.go`）：
   - 转发类用例：github.com，`GitHub{GitOrigin: up.URL}`，`up` 是假上游，写法同 `egress_test.go:276`。
     - 代理形式 `POST http://github.com/acme/w.git/info/lfs/objects/batch`（download）→ 上游收到 `Authorization: Basic x-access-token:tok`。上游回显该头，href 为 `up.URL+"/media/<oid>"` → orb 收到的 href 是 `http://github.com/media/<oid>`，action header 里没有 Authorization，整个响应体里没有 `tok` 及其 base64。
     - 随后 `GET http://github.com/media/<oid>` → 上游收到注入的 Authorization，返回 200 内容。同一 URL 用 PUT → 403 `denyProxyNotGit`。把 `h.now` 推到 61 分钟后 → 403。
     - 另一主机的 href（`https://objects.example/x?sig=1`）原样保留：它的 header 里 `X-Amz-Date` 还在，值为 `Basic base64("x-access-token:tok")` 的头被删。
     - 上游在同主机 action 的 header 里放新签发的凭据 `Authorization: Basic base64("u:fresh")`（GitLab 的做法）→ orb 收到的响应体里没有 `fresh` 及其 base64；随后经门票的 GET 仍成功。
     - 失败关闭：上游回 200 加正文 `not json`、回 4 MiB+1 的 JSON、回 `Content-Encoding: br`、回 201 → orb 一律收到 502，`Content-Type` 是 `lfs.MediaType`，正文里没有上游的内容。
     - 同主机但端口不同的 href、路径含 `/../` 的 href → 该对象没有 `actions`，`error.code` 是 422。名字是 `sync` 的多余 action 被丢掉，同对象的 `download` 不受影响。
     - 请求体 `{"operation":"upload","OPERATION":"download","transfers":["tus"],"objects":[],"x":1}` → 上游收到的请求体恰好是 `{"operation":"download","transfers":["basic"],"objects":[]}`，`Content-Length` 与它相符。
     - ReviewOnly：upload batch → 403，JSON `message` 为 `oryn: ` 加 `denyReviewOnly`；download → 200。
   - 分类类用例：直接调 `h.resolve`，PAT 主机 `git.example`。
     - 无项目：download → 200；upload → 403；请求体 `{`、`"operation":"verify"`、oid 为 `../x`、`"hash_algo":"sha512"` → 422；1 MiB+1 的请求体、1001 个对象 → 413；`Content-Encoding: gzip` → 415；`GET …/info/lfs/locks` → 200；`POST …/locks/verify` → 403。
     - 有项目仓库：别的仓库 → 403。
     - 门票不遮蔽端点：往 `h.lfsTickets` 里放键 `POST http://git.example/other/repo.git/git-receive-pack`（门票的 `repo` 是项目仓库）→ `h.resolve` 仍按普通推送判定，回 403（不是项目仓库），返回的不是门票的 target。
     - `…/info/lfs/objects/batch` 用 GET → 403 `denyProxyNotGit`；路径形式 → 404。

验收：
- `go test ./internal/egress/...` 通过。
- 用例：无项目 orb 发 `POST http://git.example/a/b.git/info/lfs/objects/batch`，请求体 `{"operation":"upload","objects":[]}` → 403，`Content-Type: application/vnd.git-lfs+json`。

---

### 步骤 2.22 — orb：检出不被 LFS 卡住，失败时有提示

依赖：无，测试独立；端到端可用要等 2.21。

文件：
- 修改 `internal/orbdrt/setup.go`、`schema/openapi.yaml`、`images/orb/smoke-test.sh`、`internal/orbdrt/assets/oryn-setup/SKILL.md`、`.github/workflows/ci.yml`
- 测试 `internal/orbdrt/lfs_test.go`

做什么：
1. 现状：
   - 镜像已执行 `git lfs install --system --skip-repo`（`images/orb/Dockerfile:168`），`/etc/gitconfig` 里有 `filter.lfs.{clean,smudge,process}` 与 `required=true`。
   - orbd 子进程不设 `GIT_CONFIG_NOSYSTEM`（`runtime.go:1124`），所以 clone 时会 smudge。
   - 在 2.21 之前，batch 一律 403（`egress.go:375-377`）。由于 `required=true`，`git clone` 以「Clone succeeded, but checkout failed」退出非 0，setup 直接失败（`setup.go:290-298`）。之后 `hasCheckout` 为真，不再重新克隆，工作区停在半检出状态。

   镜像本身不用改。
2. `setup.go`：
   - 加 `func skipSmudge(env map[string]string) []string`：复制 env，设 `GIT_LFS_SKIP_SMUDGE=1`，再 `envList`。
     - clone（`:296`）改用 `skipSmudge(env)`；
     - `r.updateRepo(…)` 的 env 参数也改用它，`merge --ff-only` / `reset --hard` 因此同样不 smudge。
     - 跳过 smudge 照样会装 pre-push 钩子（`command_filter_process.go:42` 先于 `:63`），orb 内推送时 LFS 对象会上传。
     - agent 自己的 git 不受影响：仍按系统配置 smudge，失败会大声报错。
   - 新函数：
     ```go
     // lfsPull 在检出之后取 Git LFS 对象（M24）；仓库没有 filter=lfs 的文件时什么都不做。返回值并入 SetupError。
     func (r *Runtime) lfsPull(ctx context.Context, lg *setupLogger, workdir string, env []string) error
     ```
     1. `git -C <wd> ls-files -z -- ':(attr:filter=lfs)'` 得到 n 个文件（attr pathspec，git 2.13 起），`n == 0` 返回 nil。
     2. `git lfs version` 失败时：`lg.system(apigen.Git, "warning: the repository has <n> Git LFS file(s) but git-lfs is not installed; they stay pointer files")`，返回 `git-lfs is not installed; <n> LFS file(s) are pointer files`。
     3. `lg.run(ctx(setupTimeout), apigen.Git, wd, env, "git", "-C", wd, "lfs", "pull")`：
        - 失败：`lg.system(apigen.Git, "warning: git lfs pull failed; files that could not be downloaded are still pointer files. Fix the cause, then run `git lfs pull` in <wd>")`，返回 `fmt.Errorf("git lfs pull: %v", err)`；
        - 成功：记 `git lfs pull: <n> LFS file(s) checked out`。
   - 调用：在 3b（repo.update）之后、第 4 步之前：`if p.Repo != nil && p.Repo.URL != "" && hasCheckout(workdir) { lfsErr = r.lfsPull(ctx, lg, workdir, envList(env)) }`。每次 full setup 都跑，对象齐全时 `git lfs pull` 只是空转。
   - 在 `sres.Ok, sres.Error = …`（`:328`）之前：`lfsErr != nil` 时 `SetupError` = 原值 + `"; "` + `lfsErr.Error()`；原值为空时只放 `lfsErr.Error()`。
3. 失败时 agent 看到什么：
   - setup.log 里有上面的 warning 行；
   - 线程里出现「环境 setup 失败」提示（`link.go:574-585`；预热池 orb 照现有规则回收，并退避 1 小时）；
   - 没取到的文件仍是指针文本；
   - agent 自己运行 `git lfs pull` 时，看到的是出站代理给出的 JSON `message`（2.21）。
4. `schema/openapi.yaml:13852` 的 `setupError` 描述改为：「setup 失败原因：setup 脚本失败，或 Git LFS 对象没能取下（`git lfs pull: …`）；ok=false 时给出；完整输出见 setup.log」。
5. 镜像冒烟（`images/orb/smoke-test.sh:267` 之后）加三行：
   ```bash
   [[ "$(git config --system --get filter.lfs.required)" == true ]]
   [[ "$(git config --system --get filter.lfs.process)" == "git-lfs filter-process" ]]
   git lfs version | grep -q '^git-lfs/3\.'
   ```
6. `SKILL.md`「orb 是什么样的」末尾加一条：「仓库里的 Git LFS 文件由 setup 用 `git lfs pull` 取下；取不下时 setup 记为失败，文件仍是指针，修好原因后在仓库根运行 `git lfs pull`。」
7. `ci.yml` 的 go job：跑测试前加一步 `git lfs version || (sudo apt-get update && sudo apt-get install -y git-lfs)`，并在 env 里加 `ORYN_REQUIRE_GIT_LFS: "1"`（与阶段 ② 的 `ORYN_REQUIRE_GIT` 并列）。
8. 测试（`requireGitLFS(t)`：找不到 `git-lfs` 时，`ORYN_REQUIRE_GIT_LFS=1` 则 Fatal，否则 Skip）：
   - 准备：测试 HOME 里先执行 `git lfs install --skip-repo`。上游是 `file://` 裸仓库，它的 LFS 对象用 `git lfs push` 写入（standalone file 传输，见 https://github.com/git-lfs/git-lfs/blob/main/docs/man/git-lfs-standalone-file.adoc）。
   - 对象存在 → 工作区是真实内容，`res.Ok`，日志含 `1 LFS file(s) checked out`。
   - 删掉上游 `lfs/objects` → clone 成功（`hasCheckout`），文件是指针，`res.Ok == false`，`*res.SetupError` 含 `git lfs pull`。
   - 没有 LFS 文件 → 日志里没有 `git lfs`。
   - env 的 PATH 指向只有 git 的目录 → `SetupError` 含 `git-lfs is not installed`。

验收：
- `make gen && ORYN_REQUIRE_GIT_LFS=1 go test ./internal/orbdrt/ -run LFS` 通过。
- 用例：上游缺对象 → `setup.run` 结果 `{"ok":false,"setupError":"git lfs pull: exit status 2"}`。退出码以实际为准：`commands/command_pull.go:113` 的 `Exit`。

---

### 步骤 2.23 — 前端：LFS 标记、预览与下载

依赖：2.15、2.20（生成的 `schema.d.ts`）。

文件：
- 新建 `web/src/pages/projects/repo/LfsFileState.tsx`
- 修改：
  - `web/src/api/repo-queries.ts`、`web/src/lib/file-source.ts`
  - `web/src/pages/projects/home/RepoEntryList.tsx`、`web/src/pages/projects/repo/FilesPage.tsx`
  - `web/src/mock/repo.ts`、两个语言文件
- 测试 `web/src/pages/projects/repo/RepoPages.test.tsx`、`web/src/pages/projects/home/RepoBlocks.test.tsx`、`web/src/lib/file-source.test.ts`

做什么：
1. `repo-queries.ts`：
   - 加 `export const LFS_PREVIEW_MAX_BYTES = 100 * 1024 * 1024; // 与服务端 lfsInlineMax 一致`。
   - `repoBlobUrl(projectId, ref, path, opts?: { download?: boolean })`：`download` 为真时追加 `&download=1`。
2. `projectRepoSource.downloadUrl` 改为 `repoBlobUrl(…, { download: true })`。预览读取（`readText` / `readBlob`）不带这个参数。
3. `RepoEntryList`：`entry.lfs` 时在名字后放 `<Badge title={t("projectRepo.lfs.badgeTitle")}>{t("projectRepo.lfs.badge")}</Badge>`（`ui/data.tsx:17`）。
4. `FilesPage`，self 是文件时：
   - `self.lfs?.error` → `<LfsFileState kind="error" …>`；
   - 否则 `self.lfs && self.size > LFS_PREVIEW_MAX_BYTES` → `<LfsFileState kind="large" …>`；
   - 两种情况都不渲染 `FileViewer`：媒体类型会拿 `downloadUrl` 内联播放（`FileViewer.tsx:244-246`）。
   - 其余情况照旧渲染 `FileViewer`。`self.lfs` 时在它上方加 `<p data-testid="repo-lfs-note" className="flex items-center gap-2 text-faint text-xs">`，内容是 Badge 加 `t("projectRepo.lfs.stored", { size: formatBytes(self.size) })`。
5. `LfsFileState({ kind, projectId, refName, path, entry, onRetry })` 基于 `EmptyState`（`primitives.tsx:697`）：
   - `error`：`data-testid="repo-lfs-error"`、图标 `alert`、标题 `lfs.errorTitle`、正文 `lfs.errors.<code>`、`Button icon="refresh"`（`lfs.retry`）→ `onRetry`（tree 查询的 `refetch`）。
   - `large`：`data-testid="repo-lfs-large"`、图标 `download`、标题 `lfs.largeTitle`、正文 `lfs.largeBody`（`{{max}}` = `formatBytes(LFS_PREVIEW_MAX_BYTES)`），再加一个下载按钮：`Button icon="download"`，文字 `lfs.download`（`{{size}}`），点击调 `downloadUrl(repoBlobUrl(…, { download: true }), name)`（`lib/artifacts/download.ts:7`）。
6. README 不用改：LFS 的 README 经 blob 接口取到内容；README 里引用的 LFS 图片 ≤ 100 MiB 时照常显示，超过时服务端回 413，图片显示为坏图。
7. mock（`mock/repo.ts`）在 `docs/media/` 下加三个 LFS 文件，根目录仍是 6 项：

   | 路径 | 目录项 | self | blob |
   |---|---|---|---|
   | `banner.svg` | `size`=SVG 字节数、`lfs.oid` | `mime:"image/svg+xml"`、`text:true` | 480×120 的 SVG（`fill="gray"`，与 `diagram.svg` 同类） |
   | `walkthrough.mp4` | `size:157_286_400`、`lfs.oid` | `mime:"video/mp4"` | 不带 `download=1` 回 413 `too_large`；带了回 16 字节 octet-stream |
   | `missing.psd` | `size:8_388_608`、`lfs.oid` | `lfs.error:"lfs_missing"` | 404 `{code:"lfs_missing"}` |

   oid 是固定的 64 位十六进制串。
8. 新文案：

   | key | zh-CN | en |
   |---|---|---|
   | `projectRepo.lfs.badge` | LFS | LFS |
   | `projectRepo.lfs.badgeTitle` | 由 Git LFS 存储 | Stored with Git LFS |
   | `projectRepo.lfs.stored` | 由 Git LFS 存储 · {{size}} | Stored with Git LFS · {{size}} |
   | `projectRepo.lfs.largeTitle` | 文件过大，不在浏览器中预览 | Too large to preview in the browser |
   | `projectRepo.lfs.largeBody` | 超过 {{max}} 的 Git LFS 文件只能下载。 | Git LFS files larger than {{max}} can only be downloaded. |
   | `projectRepo.lfs.download` | 下载（{{size}}） | Download ({{size}}) |
   | `projectRepo.lfs.errorTitle` | 无法读取这个 Git LFS 文件 | Can't load this Git LFS file |
   | `projectRepo.lfs.retry` | 重试 | Retry |
   | `projectRepo.lfs.errors.lfs_missing` | 找不到这个 LFS 对象：推送时可能没有上传，或已被删除。 | This LFS object can't be found: it may never have been uploaded, or it was deleted. |
   | `projectRepo.lfs.errors.lfs_auth_failed` | 上游拒绝了读取 LFS 对象的凭据。 | The upstream rejected the credentials for reading LFS objects. |
   | `projectRepo.lfs.errors.lfs_unreachable` | 连不上存放 LFS 对象的主机，或该地址不是公网 https 地址、已被拦截。 | Can't reach the host that stores the LFS object, or its address isn't a public https address and was blocked. |
   | `projectRepo.lfs.errors.lfs_corrupt` | 上游返回的内容与 LFS 指针不符。 | The content from the upstream doesn't match the LFS pointer. |
   | `projectRepo.lfs.errors.lfs_no_space` | 控制面磁盘空间不足，暂时不能读取这个 LFS 对象。 | The control plane is low on disk space, so this LFS object can't be loaded right now. |

9. 测试：
   - `RepoBlocks`：带 `lfs` 的条目出现「LFS」徽标，`title` 为「由 Git LFS 存储」。
   - `RepoPages`：
     - `/files/docs/media/banner.svg` → 出现 `repo-lfs-note`（「由 Git LFS 存储 · …」）和 `file-viewer`；
     - `walkthrough.mp4` → 出现 `repo-lfs-large`，没有 `file-viewer`；点下载后，桩的 `downloadUrl` 收到以 `download=1` 结尾的地址；
     - `missing.psd` → `repo-lfs-error` 含「找不到这个 LFS 对象」，点「重试」后 tree 再请求一次；
     - `/files/docs` 列出 `media`。
   - `file-source`：`downloadUrl("a.bin")` 以 `&download=1` 结尾。

验收：
- `cd web && bun run check && bunx vitest run src/pages/projects src/lib/file-source.test.ts` 通过。
- 用例：mock 下打开 `/projects/prj_01JCZ0000000000000000ORYN/files/docs/media/walkthrough.mp4` → 显示「文件过大，不在浏览器中预览」和「下载（150 MB）」。

---

### 步骤 2.24 — UI 审计面

依赖：2.23。

文件：修改 `web/e2e/ui-audit/projects-repo.e2e.ts`、`docs/ui-audit/areas/sections.md`

做什么：
1. 新增三个面：

   | id | path |
   |---|---|
   | `projects.repo.file-lfs` | `…/files/docs/media/banner.svg` |
   | `projects.repo.file-lfs-large` | `…/files/docs/media/walkthrough.mp4` |
   | `projects.repo.file-lfs-error` | `…/files/docs/media/missing.psd` |

2. `sections.md` 在 `projects.repo.file` 之后追加这三行，界面分别是「仓库文件 · LFS 预览 / 仅下载 / 取对象失败」，形态都是「页面」。
3. 截图清单不变：2.16 的 `projects-repo-*` 张数不改。

验收：`cd web && npx playwright test e2e/ui-audit/projects-repo.e2e.ts` 通过，新增的三个面都有截图。

---

---

### 步骤 2.25 — 文档

依赖：本阶段其余步骤。

文件：修改 `docs/features.md`、`docs/deploy.md`、`docs/protocol.md`

做什么：

1. `docs/features.md` 的「项目」小节加「仓库浏览」：
   - 镜像从哪来、何时同步（首次打开克隆、读取时超过 60 秒后台拉取、GitHub `push` 事件、手动同步）。
   - 主页的「提交到 <基线>」「文件」「README」与 `提交 / 分支 / 标签 / 文件` 子页。
   - 提交关联线程的三种来源（线程推送、线程里出现过、经 PR 合并）。
   - 仓库区块不可用时各种说明的含义（没有仓库地址、控制面没有 git、凭据被拒、超过上限、磁盘空间不足等）。
   - Git LFS：文件页显示 LFS 文件的真实大小与 LFS 标记；控制面按需从上游取对象，不需要装 git-lfs；超过 100 MiB 只能下载；只用仓库主机自己的 LFS 接口，不读仓库里的 `.lfsconfig`。
   - 「Git 访问范围」一段补一句：Git LFS 与 git 用同一套规则，上传算写操作。
   - orb 环境一段补一句：仓库里的 LFS 文件由 setup 用 `git lfs pull` 取下，取不下时 setup 记为失败。
2. `docs/deploy.md`：
   - 「1. 安装二进制与镜像」写明控制面主机需要 git 2.31 或更高；没有时仓库浏览停用，其余功能照常。
   - 「2. 控制面配置」的 yaml 示例加 `repos: { git: "", max_mirror_gb: 5, max_lfs_cache_gb: 2, min_free_gb: 2 }`，逐项说明缺省值与作用。
   - 「7. 安全」加「项目仓库镜像」小节：`<data_dir>/repos` 里放什么；凭据只经环境变量传给 git、不进 argv 与磁盘配置；未担保主机经公网地址守卫；担保主机沿用进程的代理变量；数据盘可用空间低于 `min_free_gb` 时不克隆、不拉取。
   - 「git 出站策略」小节加 Git LFS：放行 `<仓库>.git/info/lfs/` 下的 batch 与锁接口；batch 的请求体由代理解析后重新序列化再转发，按其中的 `operation` 判断读写（不可解析的不转发），上传、`locks/verify`、建锁、解锁算写；batch 的成功响应同样解析后重写：与上游同主机的传输地址改成经代理访问并去掉其中的凭据头（包括上游新签发的），其它主机的地址由 orb 直连，无法解析的响应回 502。
   - 同一小节加一条：orb 内 clone / repo.update 不在检出时下载 LFS 对象，之后统一 `git lfs pull`，失败计入 setup 失败。
3. `docs/protocol.md`：按该文件里各里程碑的写法加 `## M24：项目仓库` 一节：
   - `project_repos` / `repo_pushes` / `repo_commit_threads` 三张表。
   - `/api/v1/projects/{id}/repo*` 接口、`ProjectRepoLfs` 与 `download` 参数、全部新错误码（含五个 `lfs_*`）。
   - WS `project_repo.changed`。
   - 出站代理的推送记录、LFS 端点表，以及 LFS 拒绝时回 `application/vnd.git-lfs+json` 的 `{"message"}`。
   - `setupError` 的新含义。

验收：`rg -n 'max_mirror_gb|max_lfs_cache_gb|min_free_gb' docs/deploy.md` 三个键都有命中；`rg -n 'project_repo.changed' docs/protocol.md` 有命中。

## 阶段 ② 验收

1. Go：`go test ./internal/gitdiff/... ./internal/repos/... ./internal/api/... ./internal/egress/... ./internal/orbs/... ./internal/github/... ./internal/store/... ./internal/config/...` 全部通过，有 git 的机器上没有 Skip。
2. 端到端（真 git + 进程内假 GitHub `internal/github/githubtest`，GitRoot 为临时目录，`github.web_url` 指向它；选它而不是本地 HTTPS 裸仓库，是因为它同时覆盖了 OAuth 凭据注入、仓库 hook 投递和 smart-HTTP）：`ORYN_REQUIRE_GIT=1 go test ./internal/server/ -run TestE2ERepo -count=1 -v`。各步期望：
   - `GET /api/v1/projects/<prj>/repo` 30 s 内 → `{"kind":"mirror","state":"ready","defaultBranch":"main","available":true}`。
   - `GET …/repo/commits` → `commits[0].subject=="initial commit"`。
   - 假 GitHub 上新增提交 `second` 并投递 `push` → outcome `handled`，随后 `commits[0].subject=="second"`。
   - `…/repo/blob?path=README.md` → `# acme/widgets\n`；带上返回的 `ETag` 再请求 → 304。
   - 删除项目后 `<data_dir>/repos/<prj>.git` 不存在。
3. 生成物与规格：`make gen && make lint-spec`；提交之后 `make check-gen` 无差异。
4. 前端：`cd web && bun run check && bun run test`；`cd web && npx playwright test e2e/ui-audit --grep "projects\.repo\."` 通过。
5. 手工（mock）：`cd web && bun run dev:mock`。
   - 打开 Oryn 项目：仓库行显示「已同步 4分钟前」与刷新钮；「提交到 main」5 行，其中带线程链接的点进去是 `/t/<id>`；README 里的「指南」进入 `/projects/…/files/docs/guide.md?ref=main`，架构图可见。
   - 打开 oryn-core：只有一条「仓库镜像不可用 · 凭据被拒绝」说明，「最近线程」照常显示。

Git LFS 部分另外核对：

1. `ORYN_REQUIRE_GIT=1 ORYN_REQUIRE_GIT_LFS=1 go test ./internal/lfs/... ./internal/repos/... ./internal/egress/... ./internal/orbdrt/... ./internal/config/...` 全部通过，没有 Skip。
2. 用例：
   - 镜像仓库 `docs/big.bin` 是指向 `hello lfs\n` 的指针 → `GET …/repo/tree?path=docs` 的条目 `size:10`，带 `lfs.oid`；
   - `GET …/repo/blob?path=docs/big.bin` 返回 `hello lfs\n`，第二次请求时假上游的 batch 调用数不变；
   - 对象缺失 → 404 `lfs_missing`。
3. 手工，mock 下打开 `docs/media/` 的三个文件：
   - `banner.svg` 显示 LFS 说明与 SVG 预览；
   - `walkthrough.mp4` 只有「下载（150 MB）」；
   - `missing.psd` 显示「找不到这个 LFS 对象」与「重试」。
4. 手工，真机（GitHub 私有仓库，含 LFS 文件）：
   - 新线程里 `git lfs ls-files` 的状态列为 `*`，文件是真实内容；
   - agent 修改 LFS 文件、提交并推送 → GitHub 上能下载新对象；
   - 无项目 orb 对 PAT 主机运行 `git lfs push` → 失败，输出含 `oryn: `。

## 阶段 ③ — 托管仓库

> 摘要：项目可以选「空白仓库（Oryn 托管）」：orynd 在 `<data_dir>/repos/<projectId>.git` 建正本裸仓库（带一个初始提交），自己用 `git upload-pack / receive-pack --stateless-rpc` 提供 smart-HTTP。
> 主人本机经 `/git/<项目 id>.git`（HTTP Basic / Bearer + 设备令牌，`oryn clone` 登记凭据助手）访问，orb 经出站代理在本地服务自己项目的仓库；推送后记推送、展开提交 → 线程、广播，线程推上基线后自动「已交付」，再异步投递 Webhook。
> 前端：新建页的仓库来源选择、各处「Oryn 托管」标记、Clone 复制 `oryn clone`、设置概览「托管仓库」卡（克隆地址、Webhook、创建托管仓库）、改动面板「提交并推送到 <基线>」。

前提：阶段 ① ② 已合入，名字以本文阶段 ① ② 的规格为准。行号取自写本文时的工作区，动手前按符号重读。改 Go 的步骤读 `skill://go` 与 `skill://security-review`；3.10 起读 `skill://react`、`skill://typescript`；3.12 读 `skill://web-testing`。

### 阶段 ③ 的通用约定

- **托管标记**：`projects.hosted`（BOOLEAN）是唯一权威；`repo_url` 对托管项目恒为 NULL。`project_repos` 行（阶段 ② 已预留 `kind='hosted'`）只存 `default_branch` 与 `size_bytes`。
- **克隆地址**：`orbs.HostedCloneURL(publicURL, projectID string) string` = `strings.TrimSuffix(publicURL, "/") + "/git/" + projectID + ".git"`。函数放在 `internal/orbs/setup.go`、紧挨 `HTTPSRepo`（`:620-640`）。理由：阶段 ② 的 `internal/repos` 已经 import `orbs`（用 `orbs.HTTPSRepo`），反过来 import 会成环。
- **git 执行**一律走阶段 ② 的 `internal/repos/git.go` 约定（argv 前缀、空白环境、`GIT_CONFIG_NOSYSTEM=1`、`GIT_TERMINAL_PROMPT=0`、ctx 取消即杀进程）；最低 git 2.31 沿用阶段 ②。
- **托管仓库不受镜像逻辑约束**：不 fetch、不 prune、不按 `MaxBytes` 删除、不因 repoUrl 比较而删除。
- **新增错误码**（加进 `Error.code` 描述，`schema/openapi.yaml:6762` 起）：
  - 409 `repo_hosted`：托管项目上改 `repoUrl`，或对它同步。
  - 409 `repo_exists`：项目已有仓库时再创建托管仓库。
  - 409 `project_archived`：已归档项目上创建托管仓库。
  - 409 `repo_not_hosted`：非托管项目上设置 Webhook。
  - git 不可用沿用阶段 ② 的 503 `repo_unavailable`。
- **文案**：新顶层命名空间 `hostedRepo`（两个语言文件里都没有；`rg '"hostedRepo"' web/src/i18n` 为空），另有 4 个 key 挂在既有命名空间下，见各步骤。
- **迁移号**：阶段 ① 用 0045、阶段 ② 用 0046；本阶段是 `0047_project_hosted.sql`（3.3）与 `0048_project_webhooks.sql`（3.7）。被占用时顺延。

---

### 步骤 3.1 — `internal/repos`：托管仓库初始化

依赖：步骤 2.4（`repos.Service`、`git()`）。
文件：新建 `internal/repos/hosted.go` / 修改 `internal/repos/repos.go` / 测试 `internal/repos/hosted_test.go`

做什么：
1. `repos.go`：
   - `Kind` 加 `KindHosted Kind = "hosted"`。
   - ref 校验用阶段 ② 的 `repos.ValidRef(ref string) bool`（`repos.go`，规则见阶段 ②「通用约定 · 校验规则 · ref」）。
2. `hosted.go`：
   ```go
   // Identity 是托管仓库初始提交的作者与提交者。
   type Identity struct{ Name, Email string }

   // ErrRepoExists 表示目标目录已存在，而且不是托管正本（没有 oryn-hosted 标记）。
   var ErrRepoExists = &Error{Status: http.StatusConflict, Code: "repo_exists", Message: "项目已有仓库"}

   // InitHosted 建托管裸仓库 <Dir>/<projectID>.git：HEAD → refs/heads/<base>；initialCommit 为真时在 base 上建一个空树提交。
   // 目录已存在且带 oryn-hosted 标记时认领它（adopted=true），不重建也不动内容。只动文件系统与 git，不读写数据库。
   func (s *Service) InitHosted(ctx context.Context, projectID, base string, initialCommit bool, id Identity) (adopted bool, err error)

   // SetHostedHead 把托管仓库 HEAD 指到 refs/heads/<base>（改基线分支时）。只动 git，不读写数据库。
   func (s *Service) SetHostedHead(ctx context.Context, projectID, base string) error

   // RecordHosted 写 project_repos 行（kind=hosted、default_branch=base、size_bytes=目录大小）。只能在事务外调用。
   func (s *Service) RecordHosted(ctx context.Context, projectID, base string) error
   ```
3. `InitHosted` 的流程（git 不可用 → 503 `repo_unavailable`，同 `Sync`）：
   1. 目录已存在：里面有标记文件 `oryn-hosted` → 认领，返回 `(true, nil)`（上次创建崩在中间，或库被恢复到了旧备份）；没有标记 → `ErrRepoExists`。
   2. `git init --bare -q --template= <id>.git.tmp-<rand>`，再 `symbolic-ref HEAD refs/heads/<base>`。
   3. 写配置 `core.logAllRefUpdates=true`。基线分支不设保护，允许强推与删分支；不设 `receive.denyNonFastForwards` / `denyDeletes`，缺省就是放行。被删或被强推覆盖的旧值由 3.5 留存到 `refs/oryn/saved/`。
   4. `initialCommit` 时依次执行（不需要工作区）：
      - `hash-object -t tree -w --stdin`，stdin 为空，得到 `4b825dc642cb6eb9a060e54bf8d69288fbee4904`；
      - `commit-tree <tree> -m "Initial commit"`，环境加 `GIT_AUTHOR_NAME/EMAIL`、`GIT_COMMITTER_NAME/EMAIL` = `id`；
      - `update-ref refs/heads/<base> <commit> 0000000000000000000000000000000000000000`。
   5. 在 tmp 目录里写标记文件 `oryn-hosted`（内容是项目 id 加换行）。阶段 ② 的 `removeRepoDir` 靠它拒绝误删正本。然后 `os.Rename` 成 `<id>.git`。
   6. 任一步失败：删掉 tmp 目录，返回错误。
4. 三个函数的分工：
   - `InitHosted` 与 `SetHostedHead` 不碰库。库只有一条连接（`internal/store/store.go:38-45`），在 `Store.Tx` 的回调里再用 `s.st.Q` 会等不到连接，整个控制面卡死；所以它们一律在事务外调用，`project_repos` 行由 `RecordHosted` 另写。
   - `SetHostedHead`：`symbolic-ref HEAD refs/heads/<base>`。分支可以还不存在。
   - `RecordHosted`：`UpsertProjectRepo{kind:"hosted", default_branch: base, size_bytes: dirSize(dir)}`，`dirSize` 是步骤 2.4 在 `sync.go` 里的包内函数。
5. 阶段 ② 的入口对托管项目短路（`prj.Hosted` 为真时）：
   - `ensure`：不克隆、不拉取、不比较地址；目录在 → 可读，否则 409 `repo_not_ready`。
   - `ProjectChanged` 直接返回。
   - `Kick` 不排队。
   - `Status`：
     - 目录在 → `{kind:"hosted", state:"ready", available, syncing:false, empty:<refs/heads 为空>, cloneUrl: orbs.HostedCloneURL(cfg.PublicURL, id), defaultBranch, baseBranch:<项目 baseBranch>, sizeBytes}`；
     - 目录不在 → `state:"absent"`；
     - 不带 `syncedAt` / `lastError`。
     - 自愈：目录在，而 `project_repos` 行缺失或它的 `default_branch` 不等于项目的 `baseBranch`（创建或改基线时崩在两步之间）→ 先补跑 `SetHostedHead` 与 `RecordHosted`，再返回状态。`ServeHostedGit` 入口做同样的检查。
   - `Sync` → `&Error{409, "repo_hosted", "托管仓库不需要同步"}`。
   - `Config` 加 `PublicURL string`（`cfg.PublicURL`）。
   - `Remove` 不改：删项目时托管仓库同样删除（确认框写明，见 3.10）。
6. 测试（`requireGit`）：
   - `InitHosted(..., "main", true, {"A","a@x"})` → `rev-parse refs/heads/main` 有值，`log --format=%an%x00%s` 是 `A\x00Initial commit`，`ls-tree` 为空。
   - `initialCommit=false` → 没有任何 ref，`symbolic-ref HEAD` = `refs/heads/main`。
   - 再调一次 → `(true, nil)`，仓库内容不变（认领）；目录存在但没有 `oryn-hosted` → `ErrRepoExists`。
   - 建好的目录里有 `oryn-hosted`；`removeRepoDir(dir, false)` 拒绝删它。
   - `InitHosted` 后不调 `RecordHosted`，直接 `Status` → 行被补上，`kind=hosted`。
   - `SetHostedHead("dev")` → HEAD 变成 `refs/heads/dev`。
   - 托管项目上 `Sync` → 409 `repo_hosted`；`Status` 的 `kind=hosted`。
   - `MaxBytes=1` 时托管仓库不被删。

验收：
- `go test ./internal/repos/...` 通过。
- 用例：`InitHosted(ctx,"prj_A","main",true,id)` 之后 `git --git-dir <dir> rev-list --count main` 输出 `1`。

---

### 步骤 3.2 — smart-HTTP 处理器

依赖：3.1。
文件：新建 `internal/repos/smarthttp.go` / 修改 `internal/repos/git.go` / 测试 `internal/repos/smarthttp_test.go`

做什么：
1. **方式**：直接执行 `git upload-pack|receive-pack --stateless-rpc`，不经 `git http-backend` + `net/http/cgi`。
   - 理由：upload-pack / receive-pack 是 git 本地与 ssh 传输必需的内置命令，装了 git 就一定有；http-backend 在 libexec 里，部分发行版单独打包（Alpine 的 `git-daemon` 包里才有 `/usr/libexec/git-core/git-http-backend`，见 https://pkgs.alpinelinux.org/contents?arch=x86_64&branch=v3.23&name=git-daemon&repo=main）。
   - 直接执行还能沿用阶段 ② 的受控环境，而 CGI 会继承 orynd 的环境。
   - `githubtest/extra.go:58-67` 的 http-backend 写法只用于测试夹具，不要照搬。
2. `git.go` 加流式执行（同一 argv 前缀与环境，没有超时、没有 stdout 上限）：
   ```go
   // gitStream 执行 git 并把 stdout 流式写入 w（每次写后 Flush），stdin 原样喂入。取消方式同阶段 ② 的执行器
   // （向进程组发 SIGTERM，10 s 后才 SIGKILL）。返回 stderr 末尾 4 KiB。
   func (s *Service) gitStream(ctx context.Context, o gitRun, w io.Writer, args ...string) (stderrTail string, err error)
   ```
3. `smarthttp.go`：
   ```go
   type Endpoint string
   const ( EndpointInfoRefs Endpoint = "info/refs"; EndpointUploadPack Endpoint = "git-upload-pack"; EndpointReceivePack Endpoint = "git-receive-pack" )
   // IsPush 报告请求是否属于推送：info/refs?service=git-receive-pack 或 POST git-receive-pack。
   func IsPush(r *http.Request, ep Endpoint) bool
   // serveSmartHTTP 在裸仓库 dir 上服务一个已鉴权、已授权的 smart-HTTP 请求。fromOrb = 请求来自 orb（经出站代理）。
   func (s *Service) serveSmartHTTP(w http.ResponseWriter, r *http.Request, dir string, ep Endpoint, fromOrb bool)
   ```
4. 行为：
   - **info/refs**：
     - 只接受 GET（其它方法 405，带 `Allow: GET`）；
     - `service` 必须是 `git-upload-pack` 或 `git-receive-pack`，否则 403，正文 `oryn: 只支持 smart HTTP`；
     - 响应头 `Content-Type: application/x-git-<svc>-advertisement`；
     - 协议不是 v2、或服务是 receive-pack 时，先写 pkt-line `# service=git-<svc>\n` 和 `0000`，再执行 `<svc> --stateless-rpc --advertise-refs <dir>`。
   - **git-upload-pack / git-receive-pack**：
     - 只接受 POST（否则 405）；
     - 请求 `Content-Type` 必须是 `application/x-git-<svc>-request`（否则 415）；
     - `Content-Encoding: gzip` 或 `x-gzip` 时用 `gzip.NewReader` 解压（坏流 → 400），其它非 identity 编码 → 415；
     - 响应 `Content-Type: application/x-git-<svc>-result`；执行 `<svc> --stateless-rpc <dir>`，请求体作 stdin。
   - **协议 v2**：只对 upload-pack，把 `Git-Protocol` 头作为环境 `GIT_PROTOCOL` 传入。头值须匹配 `^[A-Za-z0-9=:._-]{1,256}$`，否则 400。按 `:` 切分后含 `version=2` 即视为 v2，info/refs 不写 service 行（与 `http-backend.c` 的 `determine_protocol_version_server() != protocol_v2` 一致）。receive-pack 忽略这个头。
   - 所有响应加：`Cache-Control: no-cache, max-age=0, must-revalidate`、`Pragma: no-cache`、`Expires: Fri, 01 Jan 1980 00:00:00 GMT`（同 http-backend 的 `hdr_nocache`）。
   - **出错**：进程启动失败且还没写头 → 500 `oryn: git 启动失败`；写头之后失败只记 `slog.Warn`（带 stderr 末尾）。
   - **取消**：
     - upload-pack：客户端断开即 ctx 取消，按阶段 ② 的执行器约定收尾。
     - receive-pack：用 `context.WithoutCancel(r.Context())` 启动。客户端断开只让 stdin 读到 EOF，git 自己退出并清理锁文件；被强杀的 receive-pack 会在正本里留下永久的 `*.lock`。请求体 120 s 没有新数据就关闭 stdin。
     - 残留的 `objects/incoming-*` 由 git 自己的 gc 清理，不另处理。
   - **额外的 git 配置**（argv 前缀之后追加）：
     - 两个服务都加 `-c transfer.hideRefs=refs/oryn`：3.5 留存的旧 ref 对客户端不可见、不可改。
     - `fromOrb` 的 receive-pack 加 `-c receive.fsckObjects=true`：agent 推上来的对象先过 fsck，带 `.git/` 路径项或恶意 `.gitmodules` 的树进不来，主人之后在本机克隆不会中招。主人本机的推送不加，免得历史仓库因为旧瑕疵被拒。
   - **请求体不设上限，磁盘另有保护**：
     - receive-pack 开始前 `statfs(Dir)`，可用空间低于 `Config.MinFreeBytes`（阶段 ② 的 `repos.min_free_gb`，缺省 2 GiB）→ 503 `oryn: 控制面磁盘空间不足`；进行中每 5 s 复查，不足即终止 git。
     - 自带 openresty 的 `location /git/` 不限请求体（步骤 3.13 加的 `client_max_body_size 0`），其余路径仍是 `600m`。orb 的推送走 link，本来就不经 openresty。
   - **并发上限**：托管 git 进程全局信号量 8，每台 orb 2；超出回 503 加 `Retry-After: 5`，正文 `oryn: 仓库正忙，请稍后重试`。
5. 测试：
   - `httptest.NewServer` 把 `/r.git/<ep>` 交给 `serveSmartHTTP(dir)`，用真 git：
     - `git clone <url>/r.git` 与 `git -c protocol.version=0 clone` 都成功；
     - 新提交后 `git push` → 裸仓库 ref 前进；
     - `git push --force` 与 `git push origin :b` 都成功。
   - 手工请求：
     - `DELETE …/info/refs` → 405；
     - 没有 service → 403；
     - POST 错 Content-Type → 415；
     - gzip 压缩的 `0000` 体发给 upload-pack → 200，Content-Type 为 `-result`；
     - `Git-Protocol: version=2;rm -rf` → 400。
     - `fromOrb=true` 时推一个树里含 `.git` 路径项的提交 → 被拒（`receive.fsckObjects`）；`fromOrb=false` 时同一次推送成功。
     - receive-pack 进行中客户端断开 → 仓库里没有残留的 `*.lock`，下一次推送成功。
     - `MinFreeBytes` 设成大于磁盘可用空间 → receive-pack 回 503，正文含 `磁盘空间不足`；upload-pack 不受影响。

验收：
- `go test ./internal/repos/ -run SmartHTTP` 通过。
- 用例：GET `…/info/refs?service=git-upload-pack`，带 `Git-Protocol: version=0` → 正文以 `001e# service=git-upload-pack\n0000` 开头。

---

### 步骤 3.3 — 数据模型与项目 API

依赖：3.1。
文件：
- 新建 `internal/store/migrations/0047_project_hosted.sql`、`internal/api/projecthosted.go`
- 修改 `internal/store/queries/projects.sql`、`schema/openapi.yaml`、`internal/api/projects.go`、`internal/api/projectrepo.go`、`internal/orbs/projsnap.go`、`internal/stats/commits.go`、`internal/server/server.go`、`internal/coord/admintools.go`
- 生成 `internal/store/db/*`、`internal/apigen/apigen.gen.go`、`web/src/api/schema.d.ts`
- 测试 `internal/api/projecthosted_test.go`、`internal/orbs/projsnap_test.go`、`internal/stats/commits_test.go`

做什么：
1. 迁移：
   ```sql
   -- +goose Up
   -- 项目仓库由 Oryn 托管（阶段 ③）：1 时 <data_dir>/repos/<id>.git 是正本，repo_url 恒为 NULL。
   ALTER TABLE projects ADD COLUMN hosted BOOLEAN NOT NULL DEFAULT 0;
   -- +goose Down
   ALTER TABLE projects DROP COLUMN hosted;
   ```
2. `projects.sql`：
   - `CreateProject` 的列与值各加 `hosted` / `@hosted`。
   - 新增：
     ```sql
     -- name: SetProjectHosted :execrows
     UPDATE projects SET hosted = 1, base_branch = @base_branch, updated_at = @updated_at
     WHERE id = @id AND hosted = 0 AND (repo_url IS NULL OR repo_url = '') AND archived_at IS NULL;

     -- name: UnsetProjectHosted :execrows
     UPDATE projects SET hosted = 0, updated_at = @updated_at WHERE id = @id AND hosted = 1;
     ```
     （查询文件里的注释只写英文。）
   - `UpdateProject` 不改：`hosted` 不能经 PATCH 修改。
3. OpenAPI：
   - `Project` 加：
     - `hosted: { type: boolean, description: 仅托管项目为 true；其余省略 }`；
     - `cloneUrl: { type: string, description: 托管仓库的克隆地址 https://<Oryn 地址>/git/<id>.git；非托管项目省略 }`。
     - 用可选字段：web 里有 `Project[]` 字面量（`mock/fixtures.ts:19`），必填会让它们全部报错。
   - `ProjectCreate` 加：
     - `hosted: { type: boolean, default: false, description: 新建 Oryn 托管仓库；与 repoUrl 互斥 }`；
     - `emptyRepo: { type: boolean, default: false, description: 只与 hosted 同用：不建初始提交（oryn project create --push 用，本机已有的历史才能推上来） }`。
   - 新路径 `/api/v1/projects/{id}/repo/hosted`：`post`，`operationId: createProjectHostedRepo`，security 同 `updateProject`，无请求体；201 返回 `Project`，另有 404 / 409 / 503。
   - `ProjectRepoStatus.kind` 的 enum 加 `hosted`，描述改为「`hosted` = Oryn 托管的正本」。
4. `projects.go`：
   - **`projectJSON`** 改为方法 `func (s *Server) projectJSON(p db.Project, hook *db.GithubHook) apigen.Project`（调用点 `:62`、`:150`）：`p.Hosted` 时 `out.Hosted = new(true)`，`out.CloneURL = new(orbs.HostedCloneURL(s.Config.PublicURL, p.ID))`。
   - **`createProject`** 校验：
     - `req.EmptyRepo` 为真而 `req.Hosted` 不为真 → 400 `emptyRepo 只能与 hosted 同用`；
     - `hosted` 且 `optStr(req.RepoURL) != nil` → 400 `hosted 与 repoUrl 只能二选一`；
     - `hosted` 时 `base := cmp.Or(strings.TrimSpace(*req.BaseBranch), "main")`，`!repos.ValidRef(base)` → 400 `baseBranch 不是合法的分支名`；
     - `s.Repos == nil` 或 `Available()` 为假 → 503 `repo_unavailable`。
   - **`createProject`** 建仓：三步都在事务外、依次执行：插入行（`Hosted: true, BaseBranch: &base`）→ `s.Repos.Service.InitHosted(ctx, id, base, !emptyRepo, s.hostedIdentity(ctx, row))` → `RecordHosted(ctx, id, base)`。后两步任一失败：`DeleteProject(context.WithoutCancel(ctx), id)` 撤掉行，`Remove(id)` 清掉可能已建的目录，再返回错误（`*repos.Error` 原样输出，其余 500），保证不出现「有行没仓库」。托管项目不调 `BindProject`。
   - **`updateProject`** 事务内取到 `p` 之后：
     - `p.Hosted && req.RepoURL != nil && strings.TrimSpace(*req.RepoURL) != ""` → `&statusError{409, "repo_hosted", "托管仓库的项目不能改用外部仓库地址"}`。空串照常放行：`ProjectEditor` 每次都发 `repoUrl: ""`（`pages/projects/ProjectEditor.tsx:153-155`）。
     - `p.Hosted && req.BaseBranch != nil`：空串 → 400 `托管仓库的基线分支不能清空`；`!ValidRef` → 400。值变了：**事务提交之后**调 `SetHostedHead` 再 `RecordHosted`，不放进事务闭包（见 3.1 第 4 条）。这两步失败只记 `slog.Error`，由 3.1 第 5 条的自愈在下次 `Status` / `ServeHostedGit` 时补上。
   - `deleteProject` 不改：步骤 2.5 加的 `s.Repos.Service.Remove(id)` 同样删托管仓库。
5. `projecthosted.go`：
   - `func (s *Server) hostedIdentity(ctx context.Context, p db.Project) repos.Identity`，优先级照 `orbs/setup.go:325-334`：
     1. `gitIdentityJSON(p.GitAuthorName, p.GitAuthorEmail)`（`settings.go:15`）；
     2. 否则 `GetOwnerGitIdentity` 再过一次 `gitIdentityJSON`；
     3. 都没有时用 `{Name:"Oryn", Email:"oryn@"+<public_url 的 Hostname()>}`。
   - `CreateProjectHostedRepo(w, r, id)`：
     1. 项目不存在 → 404 `not_found`；
     2. 已归档 → 409 `project_archived`；
     3. repoUrl 非空 → 409 `repo_exists`；`Hosted` 且目录存在 → 409 `repo_exists`；`Hosted` 而目录不存在（上次崩在中间）→ 跳过第 5 步里的 `SetProjectHosted`，直接补建；
     4. `base := cmp.Or(baseBranch, "main")`，不合法 → 400；git 不可用 → 503；
     5. 事务外依次执行：`SetProjectHosted`（单条语句，影响 0 行 → 409 `repo_exists`，防并发）→ `InitHosted(..., true, ...)` → `RecordHosted`。后两步失败时用 `context.WithoutCancel(ctx)` 补偿：`UnsetProjectHosted`；本次新建的目录（`adopted == false`）用 `Remove` 清掉，认领来的目录不删；然后返回错误；
     6. 事务之后 `s.Orbs.Service.InvalidateProjectPolicies(ctx, id)`（`orbs/gates.go:136`）和 `s.poolKick(id)`，返回 201 项目视图。
     - 已在运行的 orb 不受影响：工作目录在建 orb 时就定了；新 orb 会克隆它。
6. `projectrepo.go`：
   - `SyncProjectRepo` 把 `*repos.Error` 原样写出，于是得到 409 `repo_hosted`；`GetProjectRepo` 返回 `kind:"hosted"`。
   - `POST /api/v1/projects/{id}/repo/hosted` 不写进阶段 ② 的 handler，单独放在 `projecthosted.go`。
7. `projsnap.go:91-114`：匿名结构加 `Hosted bool json:"hosted,omitempty"`，值取 `prj.Hosted`。用 `omitempty`，`V` 仍为 2：非托管项目的哈希不变，现有快照不会全部作废。
8. `stats/commits.go:156`：条件改为 `(p.RepoUrl == nil || *p.RepoUrl == "") && !p.Hosted`。不改的话托管项目的线程永远不采样提交。
9. `server.go`：阶段 ② 的 `repos.Config{…}` 加 `PublicURL: cfg.PublicURL`。
10. 助手：`coord` 的 `projectArgs`（`internal/coord/admintools.go:190-203`）里没有 `hosted`，助手只能建外部仓库或不带仓库的项目；它的 `update_project` 走同一个 `updateProject`，同样拿到 409。助手删除项目的确认卡（`admintools.go:314-319`）要写明托管仓库：项目 `Hosted` 时 Title 用 `Delete project %q and its hosted repository?`，Summary 末尾追加 `Its Oryn-hosted git repository is the only copy and will be permanently deleted.`。被注入的助手发起删除、主人只看卡片点确认时，卡片上必须看得到正本会被删。
11. 测试（`projecthosted_test.go`，用阶段 ② 的 `newRepoEnv`）：
    - POST `{"name":"h","hosted":true}` → 201，`hosted:true`、`baseBranch:"main"`、`cloneUrl:"https://oryn.test/git/<id>.git"`；裸仓库 1 个提交，作者回落到 `Oryn <oryn@oryn.test>`；设了全局身份时用全局身份。
    - `{"hosted":true,"repoUrl":"https://x/a/b"}` → 400；`{"emptyRepo":true}` → 400；`{"hosted":true,"emptyRepo":true,"baseBranch":"trunk"}` → 没有 ref、HEAD → `refs/heads/trunk`；`baseBranch:"-x"` → 400。
    - PATCH `{"repoUrl":"https://x/a/b"}` → 409 `repo_hosted`；`{"repoUrl":""}` → 200；`{"baseBranch":"dev"}` → HEAD 改变；`{"baseBranch":""}` → 400。
    - 无仓库项目上 POST `repo/hosted` → 201，再来一次 → 409 `repo_exists`；外部仓库项目 → 409 `repo_exists`；归档项目 → 409 `project_archived`。
    - `GET repo` → `kind:"hosted"`；`POST repo/sync` → 409 `repo_hosted`；DELETE 项目之后目录不存在。
    - 建仓失败的补偿：把 git 路径换成一个恒失败的脚本后 POST `{"name":"h2","hosted":true}` → 回错误，`GET /api/v1/projects` 里没有 `h2`，`repos/` 下没有它的目录。
    - 认领：先在 `repos/<id>.git` 放一个带 `oryn-hosted` 标记的裸仓库（含 2 个提交），再对无仓库项目 `<id>` POST `repo/hosted` → 201，仓库里仍是那 2 个提交。
    - 改基线分支的请求与一个并发的 `GET /api/v1/projects` 都在 2 s 内返回（不在事务里碰库，不会卡住唯一的连接）。
    - 助手的删除确认：托管项目的确认卡 Title 含 `and its hosted repository`，Summary 含 `the only copy`；非托管项目的文案不变。
    - `projsnap_test.go`：非托管项目的哈希与改前的常量相同；`Hosted=true` 的哈希不同。
    - `commits_test.go`：托管项目的线程会走到 `/git/status` 请求。

验收：
- `make gen && make lint-spec && go test ./internal/api/... ./internal/repos/... ./internal/orbs/... ./internal/stats/...` 通过；提交后 `make check-gen` 无差异。
- 用例：`POST /api/v1/projects {"name":"demo","hosted":true}` → `201 {"id":"prj_…","name":"demo","hosted":true,"cloneUrl":"…/git/prj_….git","baseBranch":"main",…}`。

---

### 步骤 3.4 — 主人入口 `/git/<项目 id>.git/…`

依赖：3.2、3.3。
文件：
- 新建 `internal/api/gitserve.go`、`internal/egress/hosted.go`（本步只放 `HostedPusher` 类型）
- 修改 `internal/auth/auth.go`、`internal/repos/hosted.go`、`internal/server/server.go`
- 测试 `internal/auth/auth_test.go`、`internal/api/gitserve_test.go`

做什么：
1. `auth.go` 加：
   ```go
   // GitCredential 校验 /git/ 路径的凭据（阶段 ③）：`Authorization: Basic <任意用户名>:<odt_…>` 或 `Bearer odt_…`，
   // 只认设备令牌（DeviceByToken：存在且未吊销），从不读 Cookie。
   func GitCredential(ctx context.Context, st *store.Store, r *http.Request) (Principal, bool)
   ```
   实现：先试 `r.BasicAuth()`，取 password；否则试 `strings.CutPrefix(h, "Bearer ")`；交给 `DeviceByToken`（`:165-180`）。
2. `hosted.go` 加（实现 3.6 的 `egress.HostedGitServer`，3.4 先只给主人入口用）：
   ```go
   // ServeHostedGit 服务托管仓库的一个 smart-HTTP 请求（调用方已鉴权）。pusher.OrbID 为空 = 主人本机。
   func (s *Service) ServeHostedGit(w http.ResponseWriter, r *http.Request, projectID, endpoint string, pusher egress.HostedPusher)
   ```
   `egress.HostedPusher` 在 3.6 定义；3.4 先在 `internal/egress/hosted.go` 加上这个类型（`type HostedPusher struct{ OrbID, ThreadID string }`）。
   - git 不可用 → 503 `oryn: 控制面没有可用的 git`。
   - 项目不存在或 `!Hosted` → 404 `oryn: 仓库不存在`。
   - `IsPush` 且已归档 → 403 `oryn: 项目已归档，仓库只读`。正文都是 `text/plain`：git 会把 text/plain 正文显示成 `remote: …`（`remote-curl.c` 的 `show_http_message`）。
   - 目录不在 → 404。其余交给 `serveSmartHTTP(w, r, dir, ep, pusher.OrbID != "")`。
   - POST receive-pack 持有每项目的推送锁：`pushLocks sync.Map`（projectID → 容量 1 的 `chan struct{}`）。等锁用 `select`，同时等 `ctx.Done()`；等满 60 s 回 503 `oryn: 仓库正忙，请稍后重试`。不用 `sync.Mutex`：它不响应取消，一个不发请求体的推送能把后面的推送全卡住。3.5 在锁内做 ref 快照。
3. `gitserve.go`：
   ```go
   // GitHandler 服务 /git/<项目 id>.git/<端点>：不经 /api/v1 鉴权组，自己按设备令牌鉴权。
   func (s *Server) GitHandler() http.Handler
   ```
   1. `r.URL.RawPath != ""`（有转义字符）或路径不匹配 `^/git/(prj_[0-9A-HJKMNP-TV-Z]{26})\.git/(info/refs|git-upload-pack|git-receive-pack)$` → 404。这样 `..`、`%2F`、多余的段都进不来。
   2. `auth.GitCredential` 失败 → 401，头 `WWW-Authenticate: Basic realm="Oryn"`，正文 `oryn: 需要设备令牌（oryn login 后用 oryn clone 克隆）`。请求带 `Sec-Fetch-Site` 头（来自浏览器）时不回 `WWW-Authenticate`，免得弹出原生口令框。先鉴权再查项目，项目是否存在不向匿名者泄露。
   3. `s.Repos == nil` → 503。
   4. 否则 `s.Repos.Service.ServeHostedGit(w, r, id, ep, egress.HostedPusher{})`。
4. 挂载（`server.go`，在 `mounts[api.AppSessionPath] = …` 旁边）：`mounts["/git/*"] = s.API.GitHandler()`。
   - mounts 在 `NewRouter` 里先于鉴权组注册（`internal/api/router.go:65-67`），所以不经过 Cookie / Bearer 中间件。
   - **不**把 `/git` 加进 `serverPrefixes`（`router.go:101`）：那会在鉴权组里再注册一个 `/git/*`，与 mount 冲突；未知子路径由 `GitHandler` 自己回 404。
   - 裸的 `/git` 落到 SPA，无害。
   - portal 主机在 `s.Portal` 中间件里就被分流，到不了这里。
5. 测试：
   - `auth_test.go`：Basic `x:odt_…` → ok；Bearer → ok；吊销（`RevokeDevice`）之后 → 失败；只带会话 Cookie → 失败；Basic 里的密码不是 odt → 失败。
   - `gitserve_test.go`（`newRepoEnv` 加挂 `/git/*`）：建托管项目后用真 git：
     - `git -c credential.helper= -c "credential.helper=!f(){ echo username=x; echo password=$TOK; }; f" clone <srv>/git/<id>.git` 成功，`log` 是 `Initial commit`；
     - commit 后 push 成功；
     - 错令牌：git 退出码 128，stderr 含 `Authentication failed`，且第一次 401 响应带 `WWW-Authenticate`；
     - 吊销后 → 401；
     - 归档后 clone 成功、push 失败，stderr 含 `项目已归档，仓库只读`；
     - 非托管项目 / 不存在的 id → 404；
     - `/git/prj_%2e%2e.git/info/refs` → 404；
     - 只带 Cookie → 401。
     - 带 `Sec-Fetch-Site: cross-site` 的匿名请求 → 401，没有 `WWW-Authenticate` 头。
     - 一个 receive-pack 占着推送锁不发请求体时，第二个推送在取消自己的 ctx 后立即返回，不会一直挂着。

验收：
- `go test ./internal/auth/... ./internal/api/ -run 'Git|Hosted'` 通过。
- 用例：不带凭据 `GET /git/<id>.git/info/refs?service=git-upload-pack` → 401，响应头 `WWW-Authenticate: Basic realm="Oryn"`。

---

### 步骤 3.5 — 推送后的记账：推送记录、提交 → 线程、广播、自动交付

依赖：3.4、步骤 2.7（`repo_pushes`、`expandPushes`）。
文件：新建 `internal/repos/hostedpush.go` / 修改 `internal/repos/hosted.go`、`internal/repos/repos.go`、`internal/server/server.go` / 测试 `internal/repos/hostedpush_test.go`

做什么：
1. **ref 变化**：用 `for-each-ref` 在锁内前后各取一次快照求差，不解析 report-status。
   - report-status 在 side-band 里、随响应流式输出，要拆包还要处理部分 ref 被拒；快照求差在推送锁内是精确的。
   - `refsSnapshot(ctx, dir) (map[string]string, error)` = `for-each-ref --format=%(objectname) %(refname)`，在 Go 里丢掉 `refs/oryn/` 开头的行。
   - `diffRefs(before, after) []egress.RefUpdate` 按 ref 名排序；新建的 Old、删除的 New 为 40 个 `0`（init 不指定 object-format，恒为 SHA-1）。
2. **`ServeHostedGit` 的 POST receive-pack 分支**：
   1. 等推送锁（3.4），`before` 快照，`serveSmartHTTP`，`after` 快照，留存旧值（下一条），解锁。`after` 快照与留存用 `context.WithoutCancel(r.Context())` 加 20 s 超时：客户端先断开时 ref 可能已经更新，记账不能跟着丢。
   2. 留存旧值：对 `diffRefs` 里的删除（New 全 0）与非快进（`merge-base --is-ancestor <old> <new>` 退出码非 0）执行 `update-ref refs/oryn/saved/<推送时刻的毫秒数>/<原 ref 去掉 refs/ 前缀> <old>`。基线分支不设保护，agent 可以删掉或强推任何分支；删 ref 时它的 reflog 一起消失，所以不靠 reflog 补救。`Run` 每小时删掉 30 天前的 `refs/oryn/saved/*`（按路径里的时间戳判断）。
   3. 有变化时调 `s.afterHostedPush(context.WithoutCancel(r.Context()), prj, pusher, updates, now)`，同步执行、30 s 超时；
   4. 这时响应体已经写完，记账的任何失败只记 `slog.Warn`，不影响推送结果。
3. **`afterHostedPush` 依次做**：
   1. **推送记录**：只在 `pusher.OrbID != "" && pusher.ThreadID != ""` 时，对每个 `refs/heads/` 开头、New 不为全 0 的 ref 调 `InsertRepoPush`（与阶段 ② `GitPushed` 的过滤一致）。
      - 主人本机的推送不写 `repo_pushes`：该表的 `thread_id`、`orb_id` 是 NOT NULL（步骤 2.2），而且没有线程可关联。
      - orb 推送但解析不出唯一线程（`workThread` 为空）时同样不写，与阶段 ② 一致。
   2. **展开**：立即调 `expandPushes(ctx, prj)`（本地仓库，不 fetch）。默认分支取 `project_repos.default_branch`，即 3.1 写入的基线。
   3. **大小**：重新 `dirSize`，`UpsertProjectRepo` 更新 `size_bytes`。
   4. **广播**：`s.cfg.Changed(prj.ID)` → `project_repo.changed`。
   5. **自动交付**：`pusher.ThreadID != ""`，且某个 update 满足 `Ref == "refs/heads/"+*prj.BaseBranch && New` 不为全 0 → `s.cfg.Ships.MarkShipped(ctx, threadID, now.UnixMilli())`。
      - `MarkShipped`（`internal/threads/threads.go:686-702`）已交付过就不改动，并会 `Publish`。
      - 不发「合并」通知（`github/pulls.go:89-108` 的 `onMerged` 发通知，是因为 PR 合并发生在线程之外；推送本身就是线程刚做的事）。
   6. **Webhook**：3.7 在这里加一行入队。
4. **`repos.Config`** 加：
   ```go
   // Ships 是自动交付入口（*threads.Service 实现）；nil = 不自动交付。
   Ships interface{ MarkShipped(ctx context.Context, id string, at int64) error }
   ```
   `server.go` 装配时传 `Ships: s.Threads`。threads 不 import repos，不会成环。
5. **测试**：
   - httptest 服务端固定 `pusher={OrbID:"orb-1",ThreadID:"T-1"}`，用真 git：
     - push main → `repo_pushes` 有 1 行，`repo_commit_threads` 里新提交指向 `T-1`（`source=push`），stub `MarkShipped("T-1")` 被调用一次，`Changed` 收到 id；
     - 只推 `feat` → 不交付；
     - 强推 main → 交付（幂等）；删 `feat` → 有 update 但不写 push 行。
   - 主人入口（pusher 为零值）push main → 不写 push 行、不交付，`Changed` 照常。
   - 一次推两个 ref → 两条 update，按名排序。
   - 删除分支 `feat` → `refs/oryn/saved/<毫秒>/heads/feat` 指向它原来的提交；强推 main → 旧提交同样被留存；`git ls-remote` 看不到 `refs/oryn/…`，`git push origin :refs/oryn/saved/x` 被拒。
   - 推送成功但客户端在响应前断开 → `Changed` 仍被调用，线程仍被标为已交付。

验收：
- `go test ./internal/repos/ -run HostedPush` 通过。
- 用例：线程 T-1 经托管入口推 `refs/heads/main <c0> → <c1>` → `GET /api/v1/threads/T-1` 的 `shippedAt` 非空。

---

### 步骤 3.6 — orb 经出站代理读写自己项目的仓库

依赖：3.5、步骤 2.7（`workThread`）。
文件：
- 修改 `internal/egress/hosted.go`（3.4 已建，本步补齐其余类型）
- 修改 `internal/egress/egress.go`、`internal/orbs/setup.go`、`internal/orbs/egress.go`、`internal/orbs/service.go`、`internal/server/server.go`，以及全部 `Resolver` 实现（下列）
- 测试 `internal/egress/hosted_test.go`、`internal/orbs/setup_git_test.go`、`internal/orbs/hosted_egress_test.go`、`internal/repos/hosted_egress_test.go`

做什么：
1. **`egress/hosted.go`**：
   ```go
   // HostedGitRoute 是 orb 所属项目的托管仓库：git 主机等于 Host 的请求只放行 Repo，在本地服务，不转发、不注入令牌。
   type HostedGitRoute struct {
       Host      string // public_url 的 host[:port]，小写
       Repo      string // NormalizeRepo 形式，如 "git/prj_01jx…"（public_url 带路径前缀时含前缀）
       ProjectID string
   }
   type HostedPusher struct{ OrbID, ThreadID string } // 3.4 已加
   // HostedGitServer 在本地服务托管仓库（*repos.Service 实现）。
   type HostedGitServer interface {
       ServeHostedGit(w http.ResponseWriter, r *http.Request, projectID, endpoint string, pusher HostedPusher)
   }
   // HostedGitLookup 是 Resolver 的托管仓库通道；srv 为 nil = 没有通道（请求回 403），err 非 nil 回 503。
   type HostedGitLookup interface{ HostedGit() (srv HostedGitServer, pusher HostedPusher, err error) }
   type NoHostedGit struct{}
   func (NoHostedGit) HostedGit() (HostedGitServer, HostedPusher, error) { return nil, HostedPusher{}, nil }
   const denyHostedOther = "这个 orb 只能访问本项目的托管仓库"
   ```
   - `Policy`（`egress.go:54-77`）加 `HostedGit *HostedGitRoute`；`Resolver`（`:122-149`）嵌入 `HostedGitLookup`。
   - 用嵌入而不是阶段 ② `PushRecorder` 那样的可选接口：这条通道决定放行，缺了要失败关闭（403），不能静默不做。
2. **补齐 `Resolver` 实现**：`rg -n 'egress\.NoMemory$|^\s+NoMemory$' internal` 命中的每个类型都嵌入 `egress.NoHostedGit`：
   - `agentcfg/agentcfg_test.go:279`
   - `artifacts/sample/sample.go:210`
   - `github/selfwrite_test.go:287`
   - `memory/relayroute/relayroute.go:227`
   - `modelcat/egress.go:38`
   - `orbdrt/gh_remote_test.go:34`
   - 另外直接实现的有 `egress/egress_test.go:22` 的 `fakeR`（加字段 `hosted *hostedFake`）和 `orbs/egress.go` 的 `egressResolver`（见第 5 条）。
3. **`resolveGit`**（`egress.go:478-529`）：在 `classifyGit` 解析成功之后（步骤 2.21 用它取代了 `gitEndpoint`，得到 `req`；`repo`、`write` 即 `req.repo`、`req.write`）、`githubOpen` 之前插入：
   ```go
   if hg := pol.HostedGit; hg != nil && host == hg.Host {
       if repo != hg.Repo { return target{deny: denyHostedOther}, http.StatusForbidden }
       if write && h.threadGates().ReviewOnly { return target{deny: denyReviewOnly}, http.StatusForbidden }
       return target{hostedProject: hg.ProjectID, hostedEndpoint: req.endpoint}, http.StatusOK
   }
   ```
   - `target` 加 `hostedProject, hostedEndpoint string`。
   - 端点名用 `req.endpoint`（相对 `<repo>.git/` 的路径），不另写取端点名的函数。
   - 前面的 `NoGit` 检查保持不动：协调宿主仍一律 403；runner 在到达这里之前已被 `runnerEgressHandler`（`orbs/egress.go:375-384`）拦住。
4. **`ServeHTTP`**（`:222`）：`resolve` 返回 200 后、`substitute` 之前：
   - `t.hostedProject != ""` 时调 `h.R.HostedGit()`：
     - err → 503 `oryn egress: 读取 orb 的线程失败`；
     - srv 为 nil → 403 `oryn egress: 未知或不允许的路由`；
     - 否则 `r.Header.Del("Authorization")`，调 `srv.ServeHostedGit(w, r, t.hostedProject, t.hostedEndpoint, pusher)`，然后 return。
   - 因为提前返回，阶段 ② 记录上游推送的逻辑（ReverseProxy 的 `ModifyResponse`）不会碰到托管推送，托管推送由 3.5 记录。
5. **orbs**：
   - `Service` 加字段 `hostedGit egress.HostedGitServer` 和 `func (s *Service) SetHostedGit(h egress.HostedGitServer)`。写法同 `SetMemory`（`orbs/egress.go:113`），须在接受 link 之前调用。
   - `egressResolver.HostedGit()`：`hostedGit` 为 nil → `(nil, {}, nil)`；否则 10 s ctx 调 `workThread(ctx, orbID)`（步骤 2.7 抽出的函数），返回 `(s.hostedGit, HostedPusher{OrbID, ThreadID}, err)`。
6. **setup**（`orbs/setup.go`）：
   - `DefaultSetup` 加 `PublicURL string`；`server.go:188-193` 传 `PublicURL: cfg.PublicURL`。
   - `plan()` 在 `prj != nil && prj.Hosted && d.PublicURL != ""` 时：
     - `clone := HostedCloneURL(d.PublicURL, prj.ID)`；
     - `p.Repo = &apigen.LinkCtlSetupRepo{URL: clone, Shallow: prj.ShallowClone}`。**不**设 `Branch`：服务端 HEAD 就指向基线；`--push` 失败留下的空仓库配了 `--branch` 会克隆失败（`orbdrt/setup.go:286-299`），不配则可以克隆空仓库；`repo.update` 缺省取远端 HEAD（`:449-453`）。
     - `entry, route, ok := hostedGitEntry(clone, prj.ID)`；不 ok（例如 public_url 的路径段含 git 配置键不接受的字符）→ `slog.Warn` 一次，不下发路由，orb 会直连并因没有令牌而失败。
     - ok 时：在 `withGitHubHost` 之后（阶段 ④ 改动之后同样放在最后）`p.GitHosts = append(p.GitHosts, entry)` 并 `sort.Strings`，`plan.Egress.HostedGit = &route`。
     - 托管项目没有 repoUrl，所以 `GitRepos` 为空（不收窄），其它主机保持只读规则不变。
   - `func hostedGitEntry(cloneURL, projectID string) (entry string, route egress.HostedGitRoute, ok bool)`：
     - scheme 须为 http 或 https；
     - `entry = strings.ToLower(u.Host) + u.Path`（`u.Path` 以 `/` 开头，所以就是 `host[:port]/<前缀>/git/<id>.git`）；
     - 每个路径段须匹配 `repoSegRe`（`setup.go:642`），与 orbd 的 `gitSegRe`（`orbdrt/setup.go:877`）同规则；
     - `route = {Host: strings.ToLower(u.Host), Repo: egress.NormalizeRepo(u.Path), ProjectID: projectID}`。
7. **orb 内的远端地址规则**：`git remote -v` 显示的就是 `Project.cloneUrl`，主人本机也能用这个地址。orbd 无需改代码，它对每个 GitHosts 条目写两项配置（`orbdrt/setup.go:250-266`）：
   - `http.http://<host>/.proxy = <egress>`；
   - `url.http://<entry>.insteadOf = https://<entry>`。

   按 public_url 的形态：
   - `https://h`：克隆地址改写成 `http://h/git/<id>.git`，经代理以代理形式（`GET http://h/…`）到达 egress，`proxyGitHost` 取 `h`。
   - `https://h:8443`：主机键 `h:8443`，`gitHostRe` 接受端口；`HTTPSRepo` 拒绝端口，但这里不用它。
   - `http://localhost:8080`（开发）：insteadOf 不会命中，`http.http://localhost:8080/.proxy` 直接接住。orb 的 `NO_PROXY` 覆盖该主机时，orbd 已有的告警会说明并让它直连（`:233-248`）。
   - M15 之前的 orbd 用 origin-form `/git/<entry>/…`，`resolve` 按 `parts[1]` 取主机后进入同一分支，同样能用。所以不存在 orbd 过旧的问题。
8. **装配**（`server.go`，阶段 ② 构造 `s.Repos` 之后）：`s.Orbs.SetHostedGit(s.Repos)`。
9. **测试**：
   - `egress/hosted_test.go`：`fakeR{pol: Policy{HostedGit: &HostedGitRoute{Host:"oryn.test", Repo:"git/prj_a", ProjectID:"prj_A"}}}` 加记录型 `hostedFake`：
     - `http://oryn.test/git/prj_A.git/info/refs?service=git-upload-pack` → 本地收到 `("prj_A","info/refs")`，请求里没有 `Authorization`；
     - `…/git/prj_B.git/git-upload-pack` → 403，正文含 `denyHostedOther`；
     - `gl` 为 ReviewOnly 时 receive-pack → 403 `denyReviewOnly`，upload-pack → 200；
     - `NoGit` → 403；srv 为 nil → 403；err → 503。
   - `setup_git_test.go` 新增 `TestPlanHostedRepo`：
     - `PublicURL:"https://oryn.example.com"` → `Repo.URL` 是克隆地址、`Branch` 为 nil，`GitHosts` 含 `oryn.example.com/git/<id>.git`（vault 没有该主机的令牌也一样），`Egress.HostedGit` 等于预期；
     - `http://localhost:8080` → 条目 `localhost:8080/git/<id>.git`；
     - `https://h.example/oryn/` → 条目 `h.example/oryn/git/<id>.git`，`Repo` 为 `oryn/git/<lower id>`；
     - 非托管项目 → `HostedGit` 为 nil，现有断言不变。
   - `orbs/hosted_egress_test.go`（环境照 `memory_egress_test.go:63-110`）：orb 有项目和唯一工作线程 → 假 srv 收到 `pusher{orb, thread}`。
   - `repos/hosted_egress_test.go`：真 git 走 `git -c http.proxy=<httptest(egress.NewHandler(…))> clone http://oryn.test/git/<id>.git`。resolver 是测试类型：`Policy.HostedGit` 指向该项目，`HostedGit()` 返回真 `*repos.Service` 和固定线程 → clone 与 push 都成功，`repo_pushes` 有该线程的行；对另一个托管项目克隆 → 失败，stderr 含 `这个 orb 只能访问本项目的托管仓库`。

验收：
- `go test ./internal/egress/... ./internal/orbs/... ./internal/repos/... ./internal/agentcfg/... ./internal/modelcat/... ./internal/memory/... ./internal/artifacts/... ./internal/github/... ./internal/orbdrt/...` 通过。
- 用例：经代理 `git push http://oryn.test/git/<id>.git HEAD:main` → 退出码 0，线程被标为已交付。

---

### 步骤 3.7 — 推送后 Webhook

依赖：3.5。
文件：
- 新建 `internal/store/migrations/0048_project_webhooks.sql`、`internal/store/queries/project_webhooks.sql`、`internal/repos/webhook.go`
- 修改 `schema/openapi.yaml`、`internal/api/projecthosted.go`、`internal/api/projects.go`、`internal/repos/hostedpush.go`、`internal/repos/repos.go`、`internal/server/server.go`
- 生成同 3.3
- 测试 `internal/store/store_test.go`、`internal/repos/webhook_test.go`、`internal/api/projecthosted_test.go`

做什么：
1. 迁移：
   ```sql
   -- +goose Up
   -- 托管仓库的推送后 Webhook（阶段 ③），每项目至多一条。secret_enc = secrets.Box.Seal("project-webhook:" || project_id, 密钥)；NULL = 不签名。
   CREATE TABLE project_webhooks (
       project_id       TEXT    PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
       url              TEXT    NOT NULL,
       secret_enc       BLOB,
       last_delivery_at INTEGER,
       last_status      INTEGER,   -- HTTP 状态码；0 = 没拿到响应（连接失败、超时）
       last_error       TEXT,      -- 非 2xx 或传输错误的简短原因（≤500 字符）；成功为 NULL
       created_at       INTEGER NOT NULL,
       updated_at       INTEGER NOT NULL
   );
   -- +goose Down
   DROP TABLE project_webhooks;
   ```
   `store_test.go` 的表清单按字典序加入 `project_webhooks`。
2. 查询：
   - `GetProjectWebhook :one`、`ListProjectWebhooks :many`；
   - `UpsertProjectWebhook :exec`：`ON CONFLICT (project_id) DO UPDATE SET url, secret_enc, updated_at`；
   - `DeleteProjectWebhook :execrows`；
   - `SetProjectWebhookDelivery :exec`：只更新 `last_*`。
3. OpenAPI：
   - `ProjectWebhook`：必填 `url`、`hasSecret`；可选 `lastDeliveryAt`（TimestampMs）、`lastStatus`（integer）、`lastError`（string）。
   - `ProjectWebhookInput`：必填 `url`（`maxLength: 2048`）；可选 `secret`（`maxLength: 256`，描述「省略 = 不改；空串 = 清除」）。
   - `Project.webhook`：`allOf: [ProjectWebhook]`，描述「仅托管项目且已设置时出现；从不返回密钥」。
   - 路径 `/api/v1/projects/{id}/webhook`：
     - `put`（`operationId: putProjectWebhook`）：200 返回 `Project`；400；404；409 `repo_not_hosted`。
     - `delete`（`deleteProjectWebhook`）：204，幂等；项目不存在 → 404。
   - security 同 `updateProject`。
4. 只有主人能改，助手改不了：
   - 它不在 `ProjectPatch` 里；
   - 助手的项目入口只有 `coord.Projects` 的 Create / Update / Delete（`internal/coord/admintools.go:41-45`），参数是固定的 `projectArgs`（`:190-203`）；
   - 也没有任何工具调用这个 REST 端点，只能用主人的会话或设备令牌调用。
5. Handlers：
   - **URL 校验**：`url.Parse` 成功，scheme 为 `http` 或 `https`，`Host` 非空，`User == nil`（地址会原样回显在设置里，凭据不能放在地址里），长度 ≤ 2048；否则 400 `invalid_request`。
   - **内网地址照常允许**：只有主人能设，等同主人自己发请求。
   - **密钥**：`secret` 省略时保留旧的 `secret_enc`；`""` → NULL；非空 → `box.Seal(webhookAAD(id), …)`，其中 `func webhookAAD(projectID string) string { return "project-webhook:" + projectID }`。AAD 绑定项目，同 `secrets.go:70-86` 防止密文在记录间挪用。分隔符用 `:`：全局密钥名的字符集不含它（`internal/secrets/secrets.go:94-95`），所以不会与某个全局密钥的 AAD 相同。
   - **视图**：`projectView` 与 `ListProjects` 读 webhook 行（列表用一次 `ListProjectWebhooks` 建 map，写法同 hook 行，`projects.go:126-151`），`projectJSON` 加一个 `*db.ProjectWebhook` 参数填 `Webhook`（`hasSecret = len(secret_enc) > 0`）。
6. **投递**（`repos/webhook.go`；`repos.Config` 加 `Box *secrets.Box`、`WebhookTimeout time.Duration`，0 = 10 s）：
   - `afterHostedPush` 最后：`GetProjectWebhook` 存在就入队。每个项目一个容量 8 的队列和一个投递 goroutine，同一时刻只投一个；队列满时丢弃这次事件，并 `SetProjectWebhookDelivery(status=0, error="skipped: delivery queue full")`。投递脱离推送请求，用 `context.Background()` 加 `WebhookTimeout`。
   - 载荷（`deliveryId = ids.New("whd")`）：
     ```json
     {"event":"push","deliveryId":"whd_01J…","pushedAt":1795000000000,
      "project":{"id":"prj_…","name":"demo","cloneUrl":"https://oryn.example.com/git/prj_….git"},
      "pusher":{"kind":"thread","threadId":"T-…"},
      "refs":[{"ref":"refs/heads/main","oldSha":"<40 hex>","newSha":"<40 hex>"}]}
     ```
     - 主人本机推送：`pusher` 为 `{"kind":"owner"}`。
     - orb 推送但没有唯一线程：`{"kind":"thread"}`，不带 `threadId`。
     - `refs` 是全部变化的 ref，包括删除（`newSha` 全 0）。
   - 请求头：`Content-Type: application/json`、`User-Agent: Oryn-Webhook/<version.Version>`、`X-Oryn-Event: push`、`X-Oryn-Delivery: <deliveryId>`；有密钥时再加 `X-Oryn-Signature-256: sha256=<hex(HMAC-SHA256(密钥, 原始请求体))>`。
   - 客户端：
     ```go
     &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
     ```
     不跟随重定向，3xx 算失败；Transport 用 `http.DefaultTransport`。响应体读至多 64 KiB 后丢弃。不重试。
   - 结果：`SetProjectWebhookDelivery(at=now, status, error)`：
     - 2xx → `error = NULL`；
     - 其它状态 → `HTTP <code>`；
     - 传输错误 → `status = 0`，`error = err.Error()` 截到 500 字符；
     - 解密密钥失败 → 不发请求，`status = 0`，`error = 签名密钥无法解密`。
     - 然后 `s.cfg.Changed(prj.ID)`，让打开着的设置页刷新（见 3.10）。
7. **装配**：`repos.Config{…, Box: box}`。
8. **测试**：
   - `webhook_test.go`：本地 `httptest` 接收端：
     - 有密钥 `s3cret` → 恰好收到一次 POST，`hmac.Equal` 校验签名，载荷字段齐全，`pusher.kind=thread`；
     - 没有密钥 → 没有签名头；
     - 接收端回 500 → `last_status=500`、`last_error="HTTP 500"`；
     - 回 302 → 不跟随，`last_status=302`；
     - `WebhookTimeout=200ms` 而接收端睡 1 s → `last_status=0`；
     - 接收端每次睡 300 ms，连续触发 12 次推送事件 → 同一时刻只有 1 个请求在途，收到的不超过 9 次，库里出现过 `last_error="skipped: delivery queue full"`；
     - 用 `require.Eventually` 轮询库，不加测试钩子。
   - `projecthosted_test.go`：
     - 非托管项目 PUT → 409 `repo_not_hosted`；
     - `ftp://x`、`https://u:p@x`、2049 字符的地址 → 400；
     - PUT `{"url":…,"secret":"s"}` → `webhook.hasSecret=true`，响应体里搜不到 `"s"` 这个值；
     - 再 PUT 不带 secret → 仍为 true；`"secret":""` → false；
     - DELETE → 项目里没有 `webhook`。

验收：
- `make gen && make lint-spec && go test ./internal/repos/... ./internal/api/... ./internal/store/...` 通过；提交后 `make check-gen` 无差异。
- 用例：设了密钥 `s3cret` 的项目推送后，接收端的 `X-Oryn-Signature-256` 等于 `"sha256="+hex(hmac(sha256,"s3cret",body))`。

---

### 步骤 3.8 — CLI：`oryn clone`、`oryn git-credential`、`oryn project create --hosted / --push`

依赖：3.3（生成的 `apigen.Project.Hosted/CloneURL`、`ProjectCreate.Hosted/EmptyRepo`）。
文件：
- 新建 `cmd/oryn/clone.go`、`cmd/oryn/gitcredential.go`、`cmd/oryn/project_push.go`
- 修改 `cmd/oryn/project.go`
- 测试 `cmd/oryn/clone_test.go`、`gitcredential_test.go`、`project_push_test.go`

做什么：
1. **`clone.go`**：
   - `findProject(ctx, c, name) (apigen.Project, error)`：依次 `GET /api/v1/projects` 与 `?archived=true`，按名称或 id 匹配。`resolveProject`（`thread.go:210`）只看未归档项目，不改它。
   - `func orynHelper() string`：`exec.LookPath("oryn")` 与 `os.Executable()` 经 `os.SameFile` 判定为同一文件时返回 `!oryn git-credential`（升级后路径变化也不坏）；否则返回 `"!" + shellQuote(exe) + " git-credential"`，`shellQuote` 用单引号包住并把 `'` 写成 `'\''`。
   - `func credentialOrigin(cloneURL string) (string, bool)`：http / https 时返回 `scheme://host[:port]`。
   - `func cloneArgs(cloneURL, dir, helper string) []string`：
     ```go
     []string{"clone", "-c", "credential." + origin + ".helper=", "-c", "credential." + origin + ".helper=" + helper, "--", cloneURL, dir}
     ```
     - `git clone -c` 写进新仓库的配置，并且在 fetch 之前生效，所以克隆过程本身就用这个助手，之后也一直保留。
     - 先写一个空值，按主机清空助手列表（gitcredentials：「configured to the empty string, this resets the helper list」），令牌不会被系统钥匙串之类的助手 `store` 走；其它主机的助手不受影响。
   - 命令 `oryn clone <项目名|id> [目录]`（`cobra.RangeArgs(1, 2)`）：
     1. `findProject`。
     2. 非托管：有 repoUrl 时 `usageErr("项目 %s 使用外部仓库，请直接运行：git clone %s", name, repoUrl)`；没有时 `usageErr("项目 %s 没有仓库", name)`。退出码 2。
     3. 克隆地址的 origin 与 `credentials.json` 的 `url` origin（`loadCredentials`，`client.go:36-58`）不同（主机或协议）→ 报错 `克隆地址 %s 与登录的服务器 %s 不同，凭据助手不会应答：用 oryn login --url <克隆地址的主机> 重新登录`，不执行 git。
     4. 目录缺省是项目名。名字为 `.` / `..`、含 `/`、`\`、NUL 或以 `-` 开头时，`usageErr("项目名 %q 不能直接作目录名：oryn clone %s <目录>")`。
     5. `exec.CommandContext(ctx, "git", cloneArgs(...)...)`，stdio 直通，`Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")`。git 非 0 → `&exitCodeError{code, …}`（`orb_thread.go:33-37`）。目录已存在由 git 自己报错。
2. **`gitcredential.go`**：命令 `oryn git-credential <操作>`（`cobra.ExactArgs(1)`，Short「git 凭据助手（由 oryn clone 登记）」）。
   ```go
   func gitCredential(op string, in io.Reader, out io.Writer, cred credentials, credErr error) error
   ```
   - `get`：读 `key=value` 行到空行或 EOF。`credErr` 为 nil、令牌非空，且 `protocol` 等于登录 URL 的 scheme、`host` 等于登录 URL 的 `Host`（git 的 host 含端口，大小写不敏感）时，写 `username=oryn\npassword=<token>\n`。其余一律什么都不写，退出码 0，让 git 继续问别的助手或报认证失败。别的主机永远拿不到令牌。
   - `store` / `erase` 与未知操作：读完 stdin，不输出，返回 nil。
3. **`project.go`**：
   - `projectCreateOpts` 加 `hosted, push bool; remote string`，flags：
     - `--hosted`「新建 Oryn 托管的空白仓库（带初始提交）」；
     - `--push`「把当前目录的 git 仓库建成托管项目并推上去（基线 = 当前分支）」；
     - `--remote`「--push 时加的远端名（缺省 origin）」。
   - 互斥（`usageErr`，退出码 2）：
     - `--hosted` 或 `--push` 与 `--repo` → `--hosted / --push 与 --repo 只能二选一`；
     - `--push` 与 `--branch` → `--push 以当前分支作基线，不能再给 --branch`；
     - `--remote` 没有 `--push` → `--remote 只能与 --push 同用`。
   - `--hosted`：`req.Hosted = new(true)`，其余与现在一样，输出 id。
   - `--push`：交给 `projectPush`。
4. **`project_push.go`**：
   ```go
   func projectPush(ctx context.Context, c *apiClient, credURL, dir string, req apigen.ProjectCreate, remote string, remoteGiven bool, stdout, stderr io.Writer) (apigen.Project, error)
   ```
   先检查，有一项不过就不建项目：
   1. `git -C dir rev-parse --is-inside-work-tree` 不是 `true` → `当前目录不是 git 工作区：在本机仓库里运行 --push`。
   2. `symbolic-ref --quiet --short HEAD` 失败 → `HEAD 处于分离状态：先 git switch 到要作为基线的分支`。
   3. `rev-parse --verify --quiet HEAD` 失败 → `分支 <b> 还没有提交：先提交一次`。
   4. `config --get remote.<remote>.url` 成功（远端已存在）：
      - 没给 `--remote` → `远端 origin 已指向 <url>：用 --remote <名字> 换个远端名（如 --remote oryn）`；
      - 给了 → `远端 <name> 已存在（<url>）`。
      - 读的是配置原值，不用 `get-url`：后者会套用 insteadOf。

   然后：

   5. POST `{…req, hosted:true, emptyRepo:true, baseBranch:<b>}`。409（同名）→ 原样返回 `已有同名项目 (409 conflict)`，本机什么都没改。
   6. 克隆地址不是 http / https，或它的 origin 与 `credURL` 不同（规则同 clone）→ 打印克隆地址与要手工执行的命令，返回错误，项目保留。不是 http / https 的地址绝不交给 git：`ext::…` 这类地址等于在本机执行任意命令。
   7. `git remote add <remote> <cloneUrl>`；是 http / https 时：
      - `git config --local --unset-all credential.<origin>.helper`（退出码 5 表示没有，忽略）；
      - `--add credential.<origin>.helper ""`；
      - `--add credential.<origin>.helper <orynHelper()>`。
   8. `git push -u <remote> <b>`，stdio 直通，`GIT_TERMINAL_PROMPT=0`。失败 → `推送失败；项目 <name>（<id>）已建好，仓库是空的。排查后重跑：git push -u <remote> <b>`，退出码 1。
      - 项目保留：远端与助手已经配好，重推是幂等的；删掉反而要主人重新来一遍。
   9. 成功时输出 id（`--json` 时输出项目 JSON）。
5. **测试**：
   - `gitcredential_test.go` 表驱动：
     - 登录 `https://oryn.example.com` + `protocol=https\nhost=oryn.example.com\n\n` → `username=oryn\npassword=odt_x\n`；
     - `host=ORYN.example.com` → 命中；
     - `host=oryn.example.com:8443` → 空；`protocol=http` → 空；`host=github.com` → 空；
     - 登录 `https://h:8443` + `host=h:8443` → 命中；
     - `credErr` 非 nil → 空；`store` / `erase` → 空。
   - `clone_test.go`：
     - `cloneArgs("https://h/git/p.git","demo","!oryn git-credential")` 等于 `[clone -c credential.https://h.helper= -c credential.https://h.helper=!oryn git-credential -- https://h/git/p.git demo]`；
     - `shellQuote("/a b/o'ryn")` 是 `'/a b/o'\''ryn'`；
     - 用 `newFakeAPI`（`thread_test.go:38-57`）：外部项目 → 报错并给出 `git clone https://github.com/…`；名字 `a/b` → usageErr；登录主机不同 → 报错。
   - `project_push_test.go`（真 git，`requireGit` 写法同阶段 ②）。克隆地址由 `githubtest` 的 HTTP git 服务提供：`githubtest.New()` 加 `GitRoot`、`InitRepo`，并在裸仓库里 `git config http.receivepack true`（http-backend 缺省不接受匿名推送）；fake API 返回的 `cloneUrl` 指向它，`credURL` 取同一个 origin，所以 origin 检查与助手配置都会真实执行。用例：
     - fake API 返回 `cloneUrl: "ext::sh -c id"` → 报错，没有执行 `git remote add`；
     - 普通目录 → 报错，fake 没收到 POST；
     - 分离 HEAD → 报错；
     - 已有 origin → 报错并提示 `--remote`；`--remote oryn` → 成功，裸仓库有 `refs/heads/<b>`，POST 体里 `emptyRepo:true`、`baseBranch:<b>`；
     - fake 回 409 → 报错，本机没有新远端；
     - `cloneUrl` 指向不存在的路径 → 报错文本含「已建好」，远端已加上。

验收：
- `go test ./cmd/oryn/...` 通过。
- 用例：`printf 'protocol=https\nhost=oryn.example.com\n\n' | ORYN_API_URL=https://oryn.example.com ORYN_API_TOKEN=odt_x oryn git-credential get` → 输出 `username=oryn` 与 `password=odt_x` 两行。

---

### 步骤 3.9 — 端到端测试

依赖：3.4–3.7。
文件：新建 `internal/server/e2e_hosted_test.go`

做什么：
1. `newStack(t, config.Idle{…})`（`internal/server/e2e_test.go:105-149`）：完整的 orynd 与设备令牌 `s.token`；`PublicURL` 是 `http://localhost`，所以 git 一律走 `s.http.URL`。
2. 起一个本地 `httptest` 接收端，把收到的请求体与头放进 channel。
3. 步骤：
   1. `rest("POST","/api/v1/projects",{"name":"demo","hosted":true},&p,201)`，断言 `p.Hosted` 与 `p.CloneURL == "http://localhost/git/"+p.ID+".git"`。
   2. `rest("PUT","/api/v1/projects/"+p.ID+"/webhook",{"url":recv.URL,"secret":"s3cret"},&p,200)`。
   3. 在临时目录执行 `git -c credential.helper= -c "credential.helper=!f(){ echo username=x; echo password=<tok>; }; f" clone <s.http.URL>/git/<id>.git w`，等价于 `oryn clone` 写下的配置。
   4. `w` 里写 `a.txt`，`commit -m "hello hosted"`，`push origin main`。
   5. `GET …/repo/commits` 的 `commits[0].subject == "hello hosted"`，`commits[1].subject == "Initial commit"`。
   6. 5 s 内接收端恰好收到 1 次 POST：`X-Oryn-Event: push`，签名校验通过，`refs[0]` 是 `{"ref":"refs/heads/main","oldSha":<初始提交>,"newSha":<新提交>}`，`pusher.kind == "owner"`。
   7. `GET /api/v1/projects/<id>` 的 `webhook.lastStatus == 200`。
   8. PATCH `{"archived":true}` 之后再 push → 失败，stderr 含 `仓库只读`；clone 仍然成功。
   9. DELETE 项目 → `<data_dir>/repos/<id>.git` 不存在。

验收：`ORYN_REQUIRE_GIT=1 go test ./internal/server/ -run TestE2EHosted -count=1 -v` 通过。

---

### 步骤 3.10 — 前端：新建页来源、托管标记、Clone、设置「托管仓库」卡、mock

依赖：3.3、3.7（生成的 `schema.d.ts`），步骤 1.9、步骤 2.14。
文件：
- 新建 `web/src/pages/projects/settings/HostedRepoCard.tsx`、`web/src/lib/shell-quote.ts`
- 修改：
  - `web/src/components/ui/icons.tsx`
  - `web/src/pages/projects/ProjectEditor.tsx`
  - `web/src/pages/projects/home/HeaderActions.tsx`、`home/ProjectIntro.tsx`、`home/RepoSyncStatus.tsx`
  - `web/src/pages/projects/ProjectsOverviewPage.tsx`、`settings/OverviewTab.tsx`、`ProjectShell.tsx`
  - `web/src/pages/HomePage.tsx`、`web/src/lib/env-banner.ts`、`web/src/api/repo-queries.ts`
  - `web/src/mock/fixtures.ts`、`web/src/mock/server.ts`、`web/src/mock/repo.ts`
  - 两个语言文件
- 测试 `settings/HostedRepoCard.test.tsx`、`ProjectCreatePage.test.tsx`、`ProjectHomePage.test.tsx`、`lib/shell-quote.test.ts`、`lib/env-banner.test.ts`

做什么：
1. **图标**：`import { HardDrive } from "@phosphor-icons/react/dist/csr/HardDrive"`（已确认 `node_modules/@phosphor-icons/react/dist/csr/HardDrive.es.js` 存在），映射 `/** 仓库来源：Oryn 托管（阶段 ③）；GitHub 用 github，其它 Git 用 code */ hostedRepo: HardDrive`。
2. **`shell-quote.ts`**：`export function shellArg(s: string): string`。匹配 `^[A-Za-z0-9._@%+=:,/-]+$` 时原样返回，否则用单引号包住、内部的 `'` 写成 `'\''`。没有现成的同类函数（`rg -n "shellQuote|shellArg" web/src` 为空）。
3. **`ProjectEditor`**：
   - `section="create"`：在名称下方加分段按钮（写法照阶段 ① 通用约定的 `InboxPage.tsx:324-341`：`role="group"`、`aria-pressed`、`tabClass`），`aria-label={t("hostedRepo.source.label")}`。三项：「已有仓库」（缺省，就是现在的流程）/「空白仓库（Oryn 托管）」/「不带仓库」，状态 `source: "existing" | "hosted" | "none"` 放在组件里。
     - `hosted`：隐藏 repoUrl 与 `RepoPicker`，显示 `hostedRepo.source.hostedHint`，baseBranch 的 placeholder 是 `main`；POST 体加 `hosted: true`，不带 `repoUrl`。
     - `none`：repoUrl 与 baseBranch 都隐藏也都不发。
     - 切换时清掉表单里的 `repoUrl`。
   - `section="overview"` 且 `project.hosted`：不渲染 repoUrl 字段，PATCH 体里不带 `repoUrl`；baseBranch 照常可改，清空时服务端会回 400。阶段 ② 的「仓库镜像」卡只在有 `repoUrl` 时渲染，托管项目上本来就不出现。
4. **托管标记**：
   - `ProjectIntro` 仓库行：`project.hosted` 时显示 `Icon hostedRepo` + `t("hostedRepo.label")` + `branch` 图标与基线分支名，放在 `projects.noRepo` 分支之前。
   - 阶段 ② 的 `RepoSyncStatus`：`status.kind === "hosted"` 时返回 null（没有同步的概念）。
   - 总览行的副标题：把 `p.repoUrl && repoLabel(p.repoUrl)` 换成 `p.hosted ? t("hostedRepo.label") : p.repoUrl && repoLabel(p.repoUrl)`。
   - `HomePage.tsx:911` 的项目选项 hint：`p.hosted ? t("hostedRepo.label") : p.repoUrl?.replace(...)`。
   - `env-banner.ts:70` 的条件改为 `(project?.repoUrl || project?.hosted) &&`。
5. **`CloneButton`**（`home/HeaderActions.tsx`）：
   - `project.hosted` 时复制 `` `oryn clone ${shellArg(project.name)}` ``，`label={t("projectShell.copyOrynClone")}`；
   - 否则维持阶段 ① 的 `git clone ${repoUrl}`；两者都没有时返回 null。
6. **`HostedRepoCard({ project })`**（`OverviewTab` 里放在「基本信息」卡之后、「危险操作」卡之前，与阶段 ② 的「仓库镜像」卡同一位置；两者互斥：有 `repoUrl` 时是镜像卡，托管或没有仓库时是本卡）：
   - **`project.hosted`**：`Card title={t("hostedRepo.title")} icon="hostedRepo" data-testid="hosted-repo-card"`：
     - 「克隆地址」：`TextInput readOnly className="font-mono"`，值为 `cloneUrl`，旁边 `IconButton icon="copy" label={t("hostedRepo.copyCloneUrl")}`，复制规则同阶段 ①；下面是 `hostedRepo.cloneHint`（`{{cmd}}` = `oryn clone <名>`）。
     - Webhook 小节（标题 `hostedRepo.webhook.title`，说明 `hint`）：
       - 地址 `TextInput type="url" maxLength={2048}`；
       - 密钥 `TextInput type="password" autoComplete="new-password"`，`hasSecret` 时 placeholder 是 `secretKept`，下面 `secretHint`；
       - `hasSecret` 时有「清除密钥」按钮，把本地的 `clearSecret` 置真；
       - 「保存 Webhook」→ `PUT /webhook`，体 `{url, ...(secret ? {secret} : clearSecret ? {secret: ""} : {})}`，成功后失效 `qk.projects`，出错用 `ErrorText`；
       - 已有 webhook 时有「移除 Webhook」→ `confirmAction({ title: t("hostedRepo.webhook.removeConfirm", { name }), body: t("hostedRepo.webhook.removeConfirmBody"), action: t("hostedRepo.webhook.remove"), danger: true })` → `DELETE`。
       - 最近投递一行（`text-faint text-xs`）：没有 `lastDeliveryAt` → `never`；`lastError` 为空 → `lastOk`；否则 `lastFailed`；`{{ago}}` 用 `formatRelative`。
   - **非托管且没有 repoUrl**：同一张卡，正文 `hostedRepo.createBody`，`Button icon="hostedRepo"`「创建托管仓库」。点击 → `confirmAction({ title: t("hostedRepo.createConfirm", { name }), body: t("hostedRepo.createConfirmBody", { base: project.baseBranch || "main" }), action: t("hostedRepo.createAction") })` → `POST /repo/hosted` → 失效 `qk.projects` 与 `repoKeys.all(id)`。归档项目上按钮 disabled，旁边显示 `createArchived`（服务端也会回 409）。
   - **外部仓库**：不渲染。
7. **删除、归档文案**：
   - `OverviewTab` 的删除确认：`body: t(project.hosted ? "projectSettings.deleteHintHosted" : "projectSettings.deleteHint")`。
   - 归档确认：`body` 同样按 `project.hosted` 取 `archiveConfirmBodyHosted`。
   - `ProjectShell` 的归档 banner：正文按 `project.hosted` 取 `projectShell.archivedBodyHosted`。
8. **实时**（`repo-queries.ts` 的 `useRealtimeRepoSync`）：`projectId` 非空，且 `qc.getQueryData(qk.projects)` 里该项目 `hosted` 时，再失效 `qk.projects`。Webhook 的最近投递在 `Project` 里，只对托管项目多一次列表请求，不让镜像项目每次同步都拉列表。
9. **mock**：
   - `fixtures.ts` 末尾（下标 3）加托管项目：
     ```ts
     { id: "prj_01JCZ000000000000000HOSTED", name: "notes", description: "Oryn 托管的笔记仓库",
       hosted: true, cloneUrl: "https://oryn.example.com/git/prj_01JCZ000000000000000HOSTED.git", baseBranch: "main",
       shallowClone: false, snapshots: true, warmOrbs: 0,
       webhook: { url: "https://ci.example.com/hooks/oryn", hasSecret: true, lastDeliveryAt: NOW - 25 * MIN, lastStatus: 200 },
       createdAt: NOW - 3 * 24 * HOUR, updatedAt: NOW - 25 * MIN }
     ```
     id 是 `prj_` 加 26 位：`01JCZ` + 15 个 `0` + `HOSTED`。
   - `server.ts` 的项目路由（步骤 1.2 之后的位置）：
     - POST：`hosted && repoUrl` → 400；`hosted` 时补 `hosted: true`、`cloneUrl: https://oryn.example.com/git/<id>.git`、`baseBranch: b.baseBranch || "main"`，并删掉 `emptyRepo` 字段。
     - PATCH：托管项目且 `repoUrl` 非空 → 409 `repo_hosted`。
     - 新路由 `projects/:id/repo/hosted`（POST：201，或 409 `repo_exists` / `project_archived`）和 `projects/:id/webhook`（PUT：校验 http/https，非托管 409 `repo_not_hosted`，`hasSecret` 规则同服务端；DELETE：204）。
   - `mock/repo.ts`：托管项目状态 `{kind:"hosted", state:"ready", available:true, syncing:false, empty:false, cloneUrl, defaultBranch:"main", baseBranch:"main", sizeBytes: 524_288}`，读接口复用 Oryn 的假仓库数据；`POST repo/sync` → 409 `repo_hosted`。
10. **新文案**：

| key | zh-CN | en |
|---|---|---|
| `hostedRepo.label` | Oryn 托管 | Hosted by Oryn |
| `hostedRepo.source.label` | 仓库 | Repository |
| `hostedRepo.source.existing` | 已有仓库 | Existing repository |
| `hostedRepo.source.hosted` | 空白仓库（Oryn 托管） | Blank repository (hosted by Oryn) |
| `hostedRepo.source.none` | 不带仓库 | No repository |
| `hostedRepo.source.hostedHint` | Oryn 会建一个只有初始提交的仓库；orb 和本机的 oryn clone 都从这里克隆、推送。 | Oryn creates a repository with one initial commit; orbs and oryn clone on your machine clone from it and push to it. |
| `hostedRepo.title` | 托管仓库 | Hosted repository |
| `hostedRepo.cloneUrl` | 克隆地址 | Clone URL |
| `hostedRepo.copyCloneUrl` | 复制克隆地址 | Copy clone URL |
| `hostedRepo.cloneHint` | 在本机运行 {{cmd}}，它用 oryn login 的设备令牌认证。 | Run {{cmd}} on your machine; it authenticates with your oryn login device token. |
| `hostedRepo.webhook.title` | 推送后 Webhook | Push webhook |
| `hostedRepo.webhook.hint` | 每次推送成功后向这个地址 POST 一次 JSON，不重试。线程里 agent 的推送也会触发（载荷的 pusher.kind 为 thread），要不要据此部署由接收端决定。 | After each successful push, Oryn POSTs one JSON event to this URL. No retries. Pushes made by agents in threads trigger it too (pusher.kind is "thread"); the receiver decides whether to act on them. |
| `hostedRepo.webhook.url` | 地址 | URL |
| `hostedRepo.webhook.secret` | 签名密钥（可选） | Signing secret (optional) |
| `hostedRepo.webhook.secretKept` | 已设置；留空则不改 | Set — leave blank to keep it |
| `hostedRepo.webhook.secretHint` | 填了就在 X-Oryn-Signature-256 头里带 HMAC-SHA256 签名。 | When set, requests carry an HMAC-SHA256 signature in the X-Oryn-Signature-256 header. |
| `hostedRepo.webhook.clearSecret` | 清除密钥 | Clear secret |
| `hostedRepo.webhook.save` | 保存 Webhook | Save webhook |
| `hostedRepo.webhook.remove` | 移除 Webhook | Remove webhook |
| `hostedRepo.webhook.removeConfirm` | 移除 {{name}} 的推送 Webhook？ | Remove the push webhook of {{name}}? |
| `hostedRepo.webhook.removeConfirmBody` | 之后的推送不再通知这个地址。 | Later pushes will no longer notify this URL. |
| `hostedRepo.webhook.never` | 还没有投递过 | No deliveries yet |
| `hostedRepo.webhook.lastOk` | 最近一次投递：{{ago}} · HTTP {{status}} | Last delivery {{ago}} · HTTP {{status}} |
| `hostedRepo.webhook.lastFailed` | 最近一次投递失败：{{ago}} · {{error}} | Last delivery failed {{ago}} · {{error}} |
| `hostedRepo.create` | 创建托管仓库 | Create hosted repository |
| `hostedRepo.createBody` | 这个项目还没有仓库。可以让 Oryn 托管一个带初始提交的空白仓库；建好后不能再改成外部仓库。 | This project has no repository. Oryn can host a blank one with an initial commit; it can't be switched to an external repository later. |
| `hostedRepo.createConfirm` | 为 {{name}} 创建托管仓库？ | Create a hosted repository for {{name}}? |
| `hostedRepo.createConfirmBody` | 基线分支是 {{base}}。之后新建的 orb 会克隆它，正在运行的 orb 不受影响。 | The base branch is {{base}}. New orbs will clone it; running orbs are not affected. |
| `hostedRepo.createAction` | 创建 | Create |
| `hostedRepo.createArchived` | 已归档的项目不能创建仓库，先取消归档。 | Unarchive the project to create a repository. |
| `projectShell.copyOrynClone` | 复制 oryn clone 命令 | Copy oryn clone command |
| `projectShell.archivedBodyHosted` | 它不在侧栏里，预热 orb 已清零，定时任务与触发规则不再运行；托管仓库只读（能克隆，不能推送）。线程都还在。 | It's hidden from the sidebar, warm orbs are at zero, and its schedules and triggers no longer run; its hosted repository is read-only (clone works, push doesn't). Its threads are kept. |
| `projectSettings.archiveConfirmBodyHosted` | 预热 orb 清零，定时任务与触发规则停止运行，托管仓库变为只读；线程不受影响。 | Warm orbs go to zero, schedules and triggers stop, and the hosted repository becomes read-only. Threads are untouched. |
| `projectSettings.deleteHintHosted` | 线程保留但不再属于任何项目；托管仓库（全部分支、标签与提交）、Webhook、定时任务、Agent 配置与项目记忆一并删除，不可恢复。 | Threads are kept but leave the project; the hosted repository (all branches, tags and commits), its webhook, schedules, agent config and project memory are deleted for good. |

11. **测试**：
    - `ProjectCreatePage.test.tsx`：选「空白仓库（Oryn 托管）」后没有仓库地址输入框，POST 体是 `{name:"n",shallowClone:false,hosted:true}`；选「不带仓库」→ 体里没有 `repoUrl` 和 `baseBranch`。
    - `ProjectHomePage.test.tsx`：托管项目的仓库行显示「Oryn 托管」与 `main`；点 Clone → 剪贴板是 `oryn clone notes`；名字 `my notes` → `oryn clone 'my notes'`。
    - `HostedRepoCard.test.tsx`：
      - 托管项目显示克隆地址与「最近一次投递：…· HTTP 200」；
      - 只改地址后保存 → PUT 体是 `{url}`（不带 secret）；
      - 「清除密钥」再保存 → `{url, secret:""}`；
      - 移除要确认；
      - 无仓库项目 →「创建托管仓库」，确认框标题「为 x 创建托管仓库？」，确认后 `POST …/repo/hosted`；
      - 归档无仓库项目 → 按钮 disabled；外部仓库项目 → 不渲染。
    - `env-banner.test.ts`：`hosted:true` 且没有仓库内脚本 → 出现 `missing_config`。
    - `shell-quote.test.ts`：`notes` 原样；`it's` → `'it'\''s'`。

验收：
- `cd web && bun run check && bun run test` 通过。
- 用例：mock 下打开 `/projects/prj_01JCZ000000000000000HOSTED/settings/overview` → 「托管仓库」卡里有 `https://oryn.example.com/git/prj_01JCZ000000000000000HOSTED.git` 和「最近一次投递：25分钟前 · HTTP 200」。

---

### 步骤 3.11 — 改动面板「提交并推送到 <基线>」

依赖：3.10（类型与 mock）。
文件：修改 `web/src/components/changes/ShipButton.tsx`、`web/src/lib/ship-prompt.ts`、两个语言文件 / 测试 `web/src/components/changes/ShipButton.test.tsx`

做什么：
1. `ship-prompt.ts` 加（文件头注释补一句「托管项目见 buildHostedShipPrompt（阶段 ③）」）：
   ```ts
   /** 托管项目（阶段 ③）：直接推上基线分支；推上基线后服务端自动把线程标为已交付。 */
   export function buildHostedShipPrompt(base: string): string {
   	return [
   		`Ship the current work to \`${base}\`:`,
   		"1. Commit any uncommitted changes that belong to this task, with a clear message.",
   		`2. Run \`git fetch origin\` and rebase your commits onto \`origin/${base}\`, resolving any conflicts.`,
   		`3. Push with \`git push origin HEAD:${base}\`. If it is rejected because \`${base}\` moved, repeat step 2 and push again; do not force-push.`,
   		"4. Reply with the commits that were pushed (short SHA and subject).",
   	].join("\n");
   }
   ```
2. `ShipButton.tsx`：
   - `const hosted = project?.hosted === true`；返回 null 的条件改为 `(!hosted && !project?.github?.bound) || orb?.kind === "runner"`（`:40`）。
   - `hosted` 时：`const target = project.baseBranch || "main"`（基线由项目决定，不用 orb 的 `status.base`；自动交付认的也是项目基线）；文案 `t("changes.ship.hosted", { base: target })`；图标 `done ? "check" : "upload"`；发送 `buildHostedShipPrompt(target)`。
   - 非托管的行为不变。头注释补上托管分支的说明。
3. 新文案：`changes.ship.hosted` — zh-CN「提交并推送到 {{base}}」/ en "Commit & push to {{base}}"。
4. 测试：
   - `buildHostedShipPrompt("main")` 以 ``Ship the current work to `main`:\n1. `` 开头，含 `git push origin HEAD:main` 与 `do not force-push`；
   - 托管项目（`hosted:true, baseBranch:"trunk"`，没有 `github`）→ 按钮文字「提交并推送到 trunk」，点击后 `frame.message` 含 `HEAD:trunk`；
   - runner orb → 不渲染；
   - 既不托管也未绑定 → 不渲染（原用例保留）。

验收：
- `cd web && bunx vitest run src/components/changes/ShipButton.test.tsx` 通过。
- 用例：托管项目的线程打开改动面板 → 按钮「提交并推送到 main」，点击后线程收到上面这段提示词。

---

### 步骤 3.12 — UI 审计面与截图登记

依赖：3.10。
文件：修改 `web/e2e/ui-audit/projects.e2e.ts`、`docs/ui-audit/areas/sections.md`、`web/scripts/screenshots.ts`

做什么：
1. `projects.e2e.ts` 用 `surface({ id, path, open? })`（`audit.ts:288-300`）加四个面：

   | id | path | open |
   |---|---|---|
   | `projects.new.hosted` | `/projects/new` | 点 `getByRole("button", { name: "空白仓库（Oryn 托管）" })` |
   | `projects.hosted.home` | `/projects/prj_01JCZ000000000000000HOSTED` | — |
   | `projects.settings.overview.hosted` | `/projects/prj_01JCZ000000000000000HOSTED/settings/overview` | `cardAt("托管仓库")` |
   | `projects.settings.overview.create-hosted` | `/projects/prj_01JCZ000000000000000LEGACY/settings/overview` | `cardAt("托管仓库")` |

   改动面板的按钮只做单元测试（3.11）：mock 里没有托管项目的 orb 工作区。
2. `sections.md` 清单表在 `projects.settings.*` 各行之后追加这四行（列：id、界面、打开路径、形态）：
   - 新建项目 ·「空白仓库」来源 / 新建页点「空白仓库（Oryn 托管）」/ 页面；
   - 托管项目主页 / 项目分区点 notes / 页面；
   - 设置概览 ·「托管仓库」卡（克隆地址、Webhook）/ 齿轮 → 概览 / 页面；
   - 设置概览 ·「创建托管仓库」（已归档、按钮禁用）/ 打开 legacy-site 的设置 / 页面。
3. `screenshots.ts` 的 `SCREENS` 在阶段 ② 的 `projects-repo-*` 之后加：
   - `projects-hosted-home`（`open(page, "/projects/<HOSTED>", "[data-testid=project-home]")`）；
   - `projects-hosted-settings`（`…/settings/overview`，就绪选择器 `[data-testid=hosted-repo-card]`，再 `scrollWithin(page, "[data-testid=hosted-repo-card]", "start")`）；
   - `projects-new-hosted`（`/projects/new`，点分段按钮后截图）。
   - 写法同 `:1858-1870`。

验收：
- `cd web && npx playwright test e2e/ui-audit --grep "projects\.(new\.hosted|hosted|settings\.overview\.(hosted|create-hosted))"` 通过。
- `ORYN_SHOTS_DIR=/tmp/oryn-shots/p3 bun run shots:all --only projects-hosted` 生成 1280 / 390 宽、明暗两套共 8 张；390 和 360 宽目视无溢出（克隆地址输入框可横向滚动，不撑破卡片）。

---

---

### Git LFS（阶段 ③ 的补充步骤）

### 步骤 3.13 — 托管仓库的 LFS 服务端（主人入口与读接口）

依赖：步骤 3.4、步骤 2.20。

文件：
- 新建 `internal/repos/lfsserver.go`
- 修改：
  - `internal/repos/hosted.go`（`ServeHostedGit`）、`internal/repos/lfsfetch.go`（托管分支）、`internal/repos/sync.go`（`Run`）
  - `internal/api/gitserve.go`、`deploy/openresty/oryn.conf`
- 测试 `internal/repos/lfsserver_test.go`、`internal/repos/read_test.go`、`internal/api/gitserve_test.go`

做什么：
1. 存储：
   - 对象：`<data_dir>/repos/<id>.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>`（`lfs.ObjectPath`）；上传临时文件：`<id>.git/lfs/tmp/`。
   - 随项目删除：3.4 的 `Remove` 删掉整个 `<id>.git`。
   - 计入 `size_bytes`：3.1 / 3.5 的 `dirSize(<id>.git)` 包含 `lfs/`，在下一次 receive-pack 记账时更新。git-lfs 的 pre-push 先传对象再推 ref；单独 `git lfs push` 的对象要等下一次推送才计入。
   - 不设大小上限。
   - `Run` 启动时，对每个 `prj_*.git` 执行 `os.RemoveAll(<dir>/lfs/tmp)`。
2. `ServeHostedGit`（3.4）：
   - endpoint 以 `info/lfs/` 开头时：
     - 前置错误（git 不可用 503、项目不存在 404、目录不在 404）改用 `lfs.WriteError`；
     - 不做 `IsPush` 的归档判断，由 `serveLFS` 自己判断；
     - 最后调 `s.serveLFS(w, r, prj, dir, endpoint, pusher)`。
   - LFS 请求不拿推送锁，也不做 ref 快照。
   - 对象传输（`info/lfs/objects/<oid>` 的 GET 与 PUT）有自己的并发名额：全局 16、每台 orb 8。没有名额时排队，等 30 s 仍没有就回 503 加 `Retry-After: 5`，正文 `oryn: 仓库正忙，请稍后重试`。不与 git 进程共用 3.2 的名额：git-lfs 缺省 8 路并发传输，共用的话一次 LFS 推送就会占满每台 orb 2 个的 git 名额。batch 请求不占名额。
3. `lfsserver.go`：
   ```go
   // serveLFS 服务托管仓库的 Git LFS 端点：batch + basic 传输；不实现文件锁（一律 404，git-lfs 据此静默关闭推送前的锁校验，locks.go:272）。
   func (s *Service) serveLFS(w http.ResponseWriter, r *http.Request, prj db.Project, dir, endpoint string, pusher egress.HostedPusher)
   // lfsHrefBase：pusher.OrbID 为空（主人本机）时是 orbs.HostedCloneURL(PublicURL, id)；否则把它的 scheme 换成 http，
   // 这样 orb 内 git-lfs 的传输请求经 http.http://<host>/.proxy 回到出站代理的托管通道（步骤 3.6）。
   func (s *Service) lfsHrefBase(projectID string, pusher egress.HostedPusher) string
   ```
   `info/lfs/objects/batch`：
   - 只接受 POST，否则 405 + `Allow: POST`。
   - `Content-Type` 的媒体类型必须是 `lfs.MediaType` 或 `application/json`，否则 415。
   - 请求体经 `http.MaxBytesReader(lfs.BatchBodyMax)` 读取：超限 413；不是合法 JSON → 422 `oryn: 请求体不是合法的批量请求`。
   - 请求级检查：
     - `operation` 不是 `download` / `upload` → 422；
     - `transfers` 非空且不含 `basic` → 422 `oryn: 只支持 basic 传输`；
     - `objects` 为空 → 422；超过 `lfs.BatchMaxObjects` → 413；
     - `upload` 且项目已归档 → 403 `oryn: 项目已归档，仓库只读`。
   - 逐个对象：
     - `hash_algo` 非空且不是 `sha256` → 对象错误 409；
     - oid 不合法或 `size < 0` → 对象错误 422 `oryn: oid 或 size 无效`；
     - download：不存在 → 对象错误 404 `Object does not exist`；存在但文件大小与请求不符 → 对象错误 422 `oryn: 对象大小与请求不符`；否则 → `actions.download`；
     - upload：已存在且大小相符 → 省略 `actions`（batch.md 的约定）；不存在，或已存在但大小不符（上次写坏了）→ `actions.upload`，PUT 校验通过后覆盖；不给 `verify`，PUT 当场校验。
   - action 的形式：`{href: base+"/info/lfs/objects/"+oid, header: {"Authorization": <回显值>}（请求没带凭据就省略）, expires_in: 3600}`，对象带 `authenticated: true`。
     - 回显值不照抄请求头：从请求的 `Authorization` 取出令牌（Basic 的密码位或 Bearer 的值，取法同 `gitserve.go` 的鉴权），重新拼成 `Basic base64("oryn:"+<令牌>)`。照抄的话，一个带超长用户名的请求乘以 1000 个对象，能把响应放大到几百 MB。
     - 回显的是主人自己的设备令牌，走同一条 TLS 连接回给他，这是 Gitea 的做法，git-lfs 已经支持。经出站代理的请求，Authorization 已被删掉（3.6），不会回显。
   - 响应 200，`Content-Type: lfs.MediaType`，`Cache-Control: no-store`（响应体里有设备令牌，不能被中间层缓存），体为 `{"transfer":"basic","objects":[…],"hash_algo":"sha256"}`。

   `info/lfs/objects/<oid>`（`serveLFS` 自己先用 `lfs.ValidOID` 校验 oid，不合法回 404，不依赖调用方的路径正则）：
   - GET：
     - 不存在 → 404；
     - 存在 → `Content-Type: application/octet-stream`、`X-Content-Type-Options: nosniff`、`Content-Disposition: attachment`、`ETag: "<oid>"`，再 `http.ServeContent`（支持 Range）。对象内容是 agent 能上传的任意字节，不能让浏览器在 Oryn 的源下把它当页面打开。
   - PUT：
     - 归档 → 403；
     - 带压缩编码 → 415；
     - 没有 `Content-Length`（`r.ContentLength < 0`）→ 411 `oryn: 上传需要 Content-Length`。git-lfs 的 basic 上传总是带它；
     - 磁盘保护：开始前 `statfs(Dir)`，可用空间减去 `Content-Length` 后低于 `Config.MinFreeBytes` → 507 `oryn: 磁盘空间不足`；写入中每写 64 MiB 复查一次（并发上传会一起消耗空间），不足即中止并回 507；写入时遇到 `ENOSPC` 同样回 507；
     - 在 `lfs/tmp` 里 `os.CreateTemp(oid+"-*")`，紧接着 `defer os.Remove(tmp)`（改名成功后它是空操作）：所有失败路径，包括客户端中途断开，都不留临时文件；
     - 用 `io.MultiWriter(file, sha256)` 从 `io.LimitReader(body, n+1)` 流式写入（`n` 是 `Content-Length`）；请求体 120 s 没有新数据就中止（同 3.2 的 receive-pack）；
     - 读到的字节数不等于 `n` → 400 `oryn: 请求体长度与 Content-Length 不符`；
     - 摘要不等于 oid → 422 `oryn: 内容的 sha256 与 oid 不符`；
     - 通过后 `Sync`、`Close`、`MkdirAll`，再 `os.Rename` 覆盖目标（同文件系统内是原子的；目标已存在也覆盖，写坏的对象因此能被修复），最后 fsync 目标所在的目录；
     - 回 200，空正文。
   - 其它方法 → 405。

   `info/lfs/locks…`：任何方法都回 404，正文 `{"message":"oryn: 不支持 Git LFS 文件锁"}`。locking.md：「404 … will not halt any Git pushes」。
4. 读接口：步骤 2.19 的 `lfsObject` 在 `prj.Hosted` 时打开 `ObjectPath(<dir>/lfs/objects, oid)`：
   - ENOENT → `errLFSMissing`；
   - 文件大小不等于 `p.Size` → `errLFSCorrupt`；
   - 不经缓存、不访问网络，对任何大小都返回 `File`。3.20 的规则不变：超过 100 MiB 的对象不带 `download=1` 照样回 413。
5. `gitserve.go`（3.4）：
   - 路径正则改为：
     ```
     ^/git/(prj_[0-9A-HJKMNP-TV-Z]{26})\.git/(info/refs|git-upload-pack|git-receive-pack|info/lfs/objects/batch|info/lfs/objects/[0-9a-f]{64}|info/lfs/locks(?:/verify|/[A-Za-z0-9._-]{1,128}/unlock)?)$
     ```
   - `info/lfs/` 路径鉴权失败时：保留 `WWW-Authenticate`，另加 `LFS-Authenticate: Basic realm="Oryn"`，正文用 `lfs.WriteError(w, 401, "oryn: 需要设备令牌（oryn login 后用 oryn clone 克隆）")`。git-lfs 随后经 `git credential fill` 向 `oryn git-credential` 要凭据（authentication.md）。
6. `deploy/openresty/oryn.conf` 主域名 server 加一个 location，落实「托管仓库不设大小上限」：
   ```nginx
   # 托管仓库（阶段 ③）：git 推送与 Git LFS 上传不限请求体
   location /git/ {
       client_max_body_size 0;
       # 以下与 location / 逐行相同（proxy_pass … proxy_request_buffering off）
   }
   ```
   其它路径仍是 `600m`（`:18`）。orb 的上传走 link，不经 openresty。
7. 测试：
   - `lfsserver_test.go`：httptest 把 `/r.git/<ep>` 交给 `serveLFS`。
     - upload batch → 带 `actions.upload`，`authenticated:true`；PUT 正确内容 → 200，文件存在；PUT 错误内容 → 422，没有文件；
     - 再发 upload batch → 没有 `actions`；
     - 把已存对象截断成 1 字节后再发 upload batch → 又带 `actions.upload`；PUT 正确内容 → 200，文件恢复成正确内容；
     - PUT 不带 `Content-Length`（chunked）→ 411，`lfs/tmp` 是空的；
     - 用裸 TCP 连接发 `Content-Length: 10` 加 5 个字节后关闭写端 → 400；发 5 个字节后直接断开 → 处理函数返回后 `lfs/tmp` 是空的；
     - `MinFreeBytes` 大于磁盘可用空间 → PUT 回 507；
     - batch 请求的 `Authorization` 是 `Basic base64("<2000 个 a>:tok")` → 响应里回显的是 `Basic base64("oryn:tok")`，响应头有 `Cache-Control: no-store`；
     - GET 的响应头有 `X-Content-Type-Options: nosniff` 与 `Content-Disposition: attachment`；
     - 直接调 `serveLFS`，endpoint 为 `info/lfs/objects/../../config` → 404；
     - download batch → href 是 `https://oryn.test/git/<id>.git/info/lfs/objects/<oid>`，pusher 有 OrbID 时是 `http://…`；
     - GET 带 `Range: bytes=0-3` → 206；
     - `POST locks/verify` → 404 JSON；
     - 归档后 upload → 403；
     - `hash_algo: sha512` → 对象错误 409；
     - 1001 个对象 → 413；
     - 不存在对象的 download → 对象错误 404。
   - `read_test.go`：托管项目提交指针、放入对象 → Blob 返回内容；删掉对象 → `lfs_missing`。
   - `gitserve_test.go`（`requireGit` + `requireGitLFS`）：
     - git 配置用 `-c url.<srv.URL>/.insteadOf=https://oryn.test/ -c lfs.transfer.enablehrefrewrite=true`，再加 3.4 的凭据助手；
     - clone → `git lfs track "*.bin"`，提交 1 MiB 的 `a.bin` 后 push → `<dir>/lfs/objects/…` 存在；
     - 新 clone → `a.bin` 是真实内容；
     - push 时没有锁相关的告警，本地配置里出现 `lfs.<url>.locksverify=false`；
     - 不带凭据的 batch → 401，带 `LFS-Authenticate`；
     - 归档后推新的 LFS 文件 → 失败，stderr 含 `仓库只读`。

验收：
- `ORYN_REQUIRE_GIT=1 ORYN_REQUIRE_GIT_LFS=1 go test ./internal/repos/... ./internal/api/ -run 'LFS|Git|Hosted'` 通过。
- 用例：不带凭据 `POST /git/<id>.git/info/lfs/objects/batch` → 401，头 `LFS-Authenticate: Basic realm="Oryn"`，正文 `{"message":"oryn: 需要设备令牌（oryn login 后用 oryn clone 克隆）"}`。

---

### 步骤 3.14 — orb 经出站代理用托管 LFS

依赖：3.6、3.13、步骤 2.21。

文件：
- 修改 `internal/egress/lfs.go`、`internal/egress/egress.go`
- 测试 `internal/egress/hosted_test.go`、`internal/repos/hosted_egress_test.go`

做什么：
1. 签名改为 `classifyGit(r *http.Request, rest string, hosted bool) (gitReq, bool)`。`hosted` 为真时多认一个端点：`info/lfs/objects/<oid>`（oid 满足 `lfs.ValidOID`），GET 是读，PUT 是写。这些是托管服务端自己给出的 href；外部上游的传输地址只走门票。
2. `resolveGit`：先算 `hosted := pol.HostedGit != nil && host == pol.HostedGit.Host`，再 `classifyGit(r, rest, hosted)`。3.6 的托管分支用 `req.repo`、`req.write`，以及 `hostedEndpoint: req.endpoint`。写操作（upload batch、PUT、锁的写操作）在 ReviewOnly 时回 `denyReviewOnly`。托管拒绝时 `target` 带上 `lfs: req.lfs`，错误体是 JSON。
3. `ServeHTTP` 的托管提前返回（3.6）不变：先 `Del("Authorization")`，再 `ServeHostedGit(…, req.endpoint, pusher)`。托管路径不经过 `lfsRewrite`，也不登记门票。
4. 测试：
   - `hosted_test.go`：
     - `PUT http://oryn.test/git/prj_A.git/info/lfs/objects/<oid>` → 本地收到 `("prj_A","info/lfs/objects/<oid>")`，请求里没有 Authorization；
     - ReviewOnly 下 PUT → 403，JSON 的 `message` 为 `oryn: ` 加 `denyReviewOnly`；GET → 200；
     - `prj_B` → 403 `denyHostedOther`；
     - 非托管主机上的 `…/info/lfs/objects/<oid>` → 403 `denyProxyNotGit`。
   - `hosted_egress_test.go`（加 `requireGitLFS`）：用 3.6 的 egress 测试环境，在克隆的本地配置里写 `http.proxy=<egress>`。
     - `git lfs track` 加提交 `a.bin` 后 push → `<dir>/lfs/objects/…` 存在，`repo_pushes` 有该线程的行；
     - 另起一个克隆 → `a.bin` 是真实内容。

验收：
- `ORYN_REQUIRE_GIT_LFS=1 go test ./internal/egress/... ./internal/repos/ -run 'Hosted'` 通过。
- 用例：经代理 `git push http://oryn.test/git/<id>.git HEAD:main`，其中带 1 个 LFS 文件 → 退出码 0，托管库里有该对象。

---

### 步骤 3.15 — CLI：`--push` 上传 LFS 对象

依赖：3.8、3.13。

文件：修改 `cmd/oryn/project_push.go`、`internal/github/githubtest/lfs.go` / 测试 `cmd/oryn/project_push_test.go`

做什么：
1. 3.8 第 4 条的检查 3 之后加检查 3a：`git -C dir ls-files -z -- ':(attr:filter=lfs)'` 非空，并且 `git lfs version` 失败时，报错 `仓库里有 Git LFS 文件，但本机没有 git-lfs：先安装 git-lfs 再运行 --push`。这时不建项目。
2. 第 7 步与第 8 步之间：仓库用了 LFS 时，执行 `git -C dir lfs push --all <remote> <b>`，stdio 直通，`GIT_TERMINAL_PROMPT=0`。`--all` 上传 `<b>` 全部历史引用的对象，见 https://github.com/git-lfs/git-lfs/blob/main/docs/man/git-lfs-push.adoc。
   - 失败时报错 `LFS 对象上传失败；项目 <name>（<id>）已建好，仓库是空的。排查后重跑：git lfs push --all <remote> <b> && git push -u <remote> <b>`，退出码 1，项目保留，理由同第 8 步。
   - 为什么显式执行：pre-push 钩子只在 `git lfs install` 或 filter 运行过的仓库里才有，用 `core.hooksPath` 的仓库会被跳过；缺了对象，托管库就只剩指针。重复上传没有代价：batch 对已有对象不给 actions。
3. `oryn clone` 不用改：
   - `git clone -c credential.<origin>.helper=…` 在检出之前写进新仓库配置（3.8）；
   - 检出时 git-lfs 收到 401 加 `LFS-Authenticate`，于是向 `oryn git-credential` 要凭据；
   - 传输用回显的头（3.13）。
   本机没装 git-lfs 时文件仍是指针，这与任何 git 主机的情况相同。
4. 测试（`requireGitLFS`）。克隆地址沿用 3.8 测试里 `githubtest` 的 HTTP git 服务（`--push` 只接受 http / https 的克隆地址）。步骤 2.19 的假 LFS 上游（`githubtest/lfs.go`）在本步补上上传：batch 的 `upload` 对未存的对象给出 `actions.upload`（`PUT /_lfs/objects/{oid}`，把请求体存下），已存的对象不给 actions。
   - 有 LFS 文件 → fake 存下了该对象，而且它的 `Calls` 里上传请求排在 `git-receive-pack` 之前；
   - PATH 里没有 git-lfs → 报错，fake 没收到 POST；
   - 没有 LFS 文件 → 不调用 `git lfs`。

验收：
- `ORYN_REQUIRE_GIT_LFS=1 go test ./cmd/oryn/ -run Push` 通过。
- 用例：在带 LFS 文件的本机仓库里运行 `oryn project create demo4 --push --remote oryn` → 输出 `prj_…`；项目主页打开该文件，显示「由 Git LFS 存储 · …」。

前端：托管项目沿用阶段 ② 的 LFS 展示；mock 的托管项目复用 Oryn 的假仓库数据（3.10），里面已有 `docs/media/`，不用另加步骤。

---

---

### 步骤 3.16 — 文档

依赖：本阶段其余步骤。

文件：修改 `docs/features.md`、`docs/deploy.md`、`docs/protocol.md`、`README.md`

做什么：

1. `docs/features.md` 的「项目」小节加「托管仓库」：
   - 新建项目的三种仓库来源；没有仓库的旧项目怎样创建托管仓库。
   - 本机用法：`oryn clone <项目>`、`oryn project create <名> --hosted`、`oryn project create <名> --push [--remote <名>]`。
   - 交付：改动面板的「提交并推送到 <基线>」，推上基线后线程自动标记已交付。
   - 推送后 Webhook：载荷字段、请求头、签名校验的示例（HMAC-SHA256，`X-Oryn-Signature-256: sha256=<hex>`），不重试；写明线程里 agent 的推送也会触发（`pusher.kind` 为 `thread`），要不要据此部署由接收端把关。
   - 基线分支不设保护；被删除或被强推覆盖的分支旧值保留 30 天（`refs/oryn/saved/…`），需要时在控制面主机上用 git 取回。
   - 归档后只读、删除项目连仓库一起删、托管与外部仓库不能互转。
   - Git LFS：托管仓库内置 LFS 服务（batch + basic），不支持文件锁，对象随项目删除、不设大小上限。
2. `docs/deploy.md`：
   - 「3. 反代」：自带配置新增 `location /git/ { client_max_body_size 0; … }` 及其原因（git 推送与 LFS 上传不限请求体，其余路径仍是 600m）。
   - 「7. 安全」加 `/git/` 入口：只认设备令牌（HTTP Basic 的密码位或 Bearer），不读 Cookie；orb 只能访问本项目的托管仓库；orb 的推送会过 fsck；数据盘可用空间低于 `min_free_gb` 时拒绝推送。
   - 「2. 控制面配置」里 `data_dir` 的说明加两句：`<data_dir>/repos` 里的托管仓库是正本，备份 `data_dir` 时必须包含；把库恢复到旧备份后，找不到项目行的托管仓库会被移到 `<data_dir>/repos/orphaned/` 而不是删除。
3. `docs/protocol.md` 的 M24 一节补：`projects.hosted`、`project_webhooks` 表、`/git/<id>.git/*`（smart-HTTP 与 LFS 的路由、JSON 形状、401 加 `LFS-Authenticate`、锁接口一律 404）、`POST /api/v1/projects/{id}/repo/hosted`、`PUT|DELETE /api/v1/projects/{id}/webhook`、新错误码、`Policy.HostedGit`。
4. `README.md`：CLI 示例块加 `oryn clone`、`oryn project create --hosted`、`oryn project create --push`。

验收：`rg -n 'oryn clone' README.md docs/features.md` 与 `rg -n 'X-Oryn-Signature-256' docs/features.md` 都有命中；`rg -n 'location /git/' docs/deploy.md deploy/openresty/oryn.conf` 两个文件都有命中。

## 阶段 ③ 验收

1. Go：`ORYN_REQUIRE_GIT=1 go test ./internal/repos/... ./internal/api/... ./internal/auth/... ./internal/egress/... ./internal/orbs/... ./internal/stats/... ./internal/store/... ./cmd/oryn/...` 全部通过，没有 Skip。
2. 端到端：`ORYN_REQUIRE_GIT=1 go test ./internal/server/ -run TestE2EHosted -count=1 -v`，各步期望：
   - `POST /api/v1/projects {"name":"demo","hosted":true}` → `201`，`"hosted":true`、`"cloneUrl":"http://localhost/git/prj_….git"`、`"baseBranch":"main"`。
   - 带设备令牌的 `git clone <srv>/git/<id>.git w` → 退出码 0；`git -C w log --format=%s` 输出 `Initial commit`。
   - commit `hello hosted` 之后 `git push origin main` → 退出码 0。
   - `GET …/repo/commits` → `commits[0].subject=="hello hosted"`，`commits[1].subject=="Initial commit"`。
   - 本地接收端恰好收到 1 次 `POST`：`X-Oryn-Event: push`，`X-Oryn-Signature-256 == "sha256="+hex(HMAC-SHA256("s3cret", body))`，`refs[0].ref=="refs/heads/main"`，`pusher.kind=="owner"`；`GET` 项目 → `webhook.lastStatus==200`。
   - 归档后 push → 退出码非 0，stderr 含 `项目已归档，仓库只读`；clone 仍然成功。
   - 删除项目后 `<data_dir>/repos/<id>.git` 不存在。
3. orb 路径：`go test ./internal/repos/ -run HostedEgress -v` → 经出站代理 clone 与 push 都成功，`repo_pushes` 有该线程的行；对别的托管项目 → `这个 orb 只能访问本项目的托管仓库`。
4. 生成物：`make gen && make lint-spec`；提交之后 `make check-gen` 无差异。
5. 前端：`cd web && bun run check && bun run test`；3.12 的 playwright 命令通过。
6. 手工（真机 orynd，`public_url` 为 `https://oryn.example.com`）：
   1. `oryn project create demo2 --hosted` 输出 `prj_…`。
   2. `oryn clone demo2` → 生成 `demo2/`；`git -C demo2 config --get-all credential.https://oryn.example.com.helper` 输出一个空行和 `!oryn git-credential`。
   3. 在本机已有仓库里 `oryn project create demo3 --push`：已有 origin 时报错并提示 `--remote`；`--remote oryn` → 推送成功，项目主页显示本机历史。
   4. 在 demo2 里新建线程，让 agent 改文件后点「提交并推送到 main」→ 线程变为「已交付」，提交列表出现新提交并关联到该线程。

Git LFS 部分另外核对：

1. `ORYN_REQUIRE_GIT=1 ORYN_REQUIRE_GIT_LFS=1 go test ./internal/repos/... ./internal/api/... ./internal/egress/... ./cmd/oryn/...` 全部通过。
2. 手工：
   - `oryn clone demo2` 后 `git lfs track "*.bin"`，提交一个 700 MiB 的文件并推送 → 经 openresty 成功；
   - 项目主页的该文件只能下载；
   - 新线程的 orb 工作区里该文件是真实内容；
   - 归档后推送新 LFS 文件 → stderr 含 `项目已归档，仓库只读`。

## 阶段 ④ — 项目级密钥与环境变量

- 每个项目一张「密钥与环境变量」表：条目是环境变量（可勾「保密」，不保密的值可读可改）或 Git 令牌 `git/<host>/token`（总是保密）；只注入本项目的 orb，与全局同名时以项目为准；模型供应商密钥（`omp/models/…`）仍只在全局。
- 存储沿用 `secrets` 表（重建，加 `project_id` 与 `secret`），`secrets.Vault` 加项目级方法；setup 计划、出站代理 git 令牌、项目快照哈希、阶段 ② 的镜像拉取都按「项目 → 全局」解析；Runner 线程、协调宿主、助手碰不到。
- 新接口 `/api/v1/projects/{id}/secrets[/{name}]`、`oryn secrets set|ls|rm --project`、项目设置新分组 `secrets`（含粘贴 `.env` 批量添加）；全局「密钥」设置页不变。

### 阶段 ④ 的通用约定

- 依赖阶段 ①（项目设置子页 `web/src/pages/projects/settings/ProjectSettingsPage.tsx` 的分组导航）与阶段 ②（`internal/repos` 镜像拉取）；不依赖阶段 ③。
- 迁移号：阶段 ① 用 0045、阶段 ② 用 0046、阶段 ③ 用 0047 与 0048，本阶段是 `0049_project_secrets.sql`。被占用时顺延，下文与测试里的版本号（49、前一版本 48）同步改。
- 每步结束 `make test` 与 `cd web && bun run check && bun run test` 通过；改 `schema/openapi.yaml` 或 `internal/store/queries/*.sql` 的步骤先 `make gen`，提交后再 `make check-gen`（它比较工作区，`Makefile:30-31`）。
- 新文案同时写进 `web/src/i18n/locales/zh-CN.json` 与 `en.json`（`web/src/i18n/locales.test.ts` 校验 key 一致）。新顶层命名空间 `projectSecrets`（已确认两个文件里都没有）；复数用 `_one` / `_other`，与现有写法一致（`en.json:173`、`zh-CN.json:173-174`）。
- 服务端错误码沿用 `invalid_request` / `not_found`（`internal/api/server_auth.go:55-56`），message 用中文。

### 已定的取舍（实现时不再改）

1. **AAD**：全局行仍是 `name`（`internal/secrets/secrets.go:70-86`，不重新封存）；项目行是 `project:<projectId>:<name>`，不保密的再加 `:plain`。全局名的字符集（`secrets.go:94-95`）不含 `:`，两种 AAD 不会撞；密文挪到别的项目或别的名字、或者翻转保密位，都解不开。
2. **唯一性**：两条部分唯一索引，照 `0042_ssh.sql:40-41`：`(name) WHERE project_id IS NULL` 与 `(project_id, name) WHERE project_id IS NOT NULL`。
3. **写入**不用 `ON CONFLICT`（部分索引的 upsert 冲突目标需带 WHERE），在事务里先查再 Insert / Update，覆盖时保留 id（与 `vault.go:63-100` 现在「覆盖保留 id」一致）。
4. **占位符 `oryn-ph-<id>`、`Get`、`Lookup` 只解析全局行**：项目条目要么以明文进 orb 环境，要么由 `GitToken` 注入，从不下发占位符。
5. **已知密钥集合 `Vault.All`** = 所有 `secret = 1` 的行（全局与项目）。它同时用于分享扫描（`internal/share/scan.go:63`）、runner 无密钥校验（`internal/server/server.go:214-216`、`adapters.go:86-113`）、主人记忆脱敏（经 `share.RedactText`，`internal/ownermem/service.go:27-28`）。项目行的键是 `<项目名>:<NAME>`：项目名唯一（`0001_init.sql:67`），全局名不含 `:`，所以不会与全局键冲突；它作为扫描命中的 `secretName` 显示（`scan.go:217-218`）。不保密的值不进这个集合。
6. **环境变量合并顺序**：全局 env 密钥 → 项目条目 → omp-home 降级成环境变量的模型密钥（保留 `internal/orbs/setup.go:379` 的现有规则；模型密钥只在全局，项目条目不能顶掉它）。
7. **出站 git 令牌**：github.com 且 GitHub 集成管着这台 orb 时，OAuth 路径原样不动（`internal/orbs/egress.go:48-61`）；其余情况先查项目的 `git/<host>/token`，再查全局。读 orb 行失败时拒绝（不回落到可能更宽的全局令牌）。
8. **不作废出站策略缓存**：`egress.Policy` 里没有由密钥派生的内容，令牌在每个 git 请求时经 `Resolver.GitToken` 现取（`internal/egress/egress.go:507`），所以不调 `InvalidateProjectPolicies`（`internal/orbs/gates.go:136`）。写入或删除后调 `s.poolKick(id)`（`internal/api/projects.go:470`），让预热池按新的配置哈希对账，同 `updateProject` 对 setup 脚本改动的处理（`projects.go:403-406`）。
9. **已归档的项目照常可读写**（同项目级 Agent 配置 `internal/api/projectagentconfig.go:16-35`，不看 `archived_at`）；归档不删条目，删除项目经外键级联删除。

### 步骤 4.1 — 存储：secrets 表加项目作用域与保密位

依赖：无，因为只改表结构与查询，全局行为不变。

文件：新建 `internal/store/migrations/0049_project_secrets.sql` / 修改 `internal/store/queries/blobs_secrets.sql`、`internal/secrets/vault.go`、`internal/orbs/setup.go`、`internal/orbs/projsnap.go` / 生成 `internal/store/db/*` / 测试 `internal/store/project_secrets_migration_test.go`、`internal/orbs/m11a_fixes_test.go`、`internal/orbs/projsnap_test.go`

做什么：

1. 迁移按 `0033_github_oauth.sql:159-160` 的写法：库以 `foreign_keys = ON` 打开、迁移包在事务里，所以先搬进无约束的暂存表，不用 `RENAME TO`。`secrets` 没有子表引用。迁移全文：
   ```sql
   -- +goose Up
   -- 阶段 ④：项目级密钥与环境变量。project_id NULL = 全局（原有行全部是全局）；secret = 0 只允许项目级 env 条目（值可读）。
   -- SQLite 去不掉列级 UNIQUE(name)，只能重建：照 0018 / 0033 先搬进无约束的暂存表，不用 RENAME TO。id 原样搬回，
   -- 已下发的占位符 oryn-ph-<id> 继续有效；全局行的 AAD 仍是名称，不重新封存。
   CREATE TABLE secrets_keep (
       id TEXT, name TEXT, mode TEXT, value_enc BLOB, hosts TEXT, created_at INTEGER, updated_at INTEGER
   );
   INSERT INTO secrets_keep (id, name, mode, value_enc, hosts, created_at, updated_at)
   SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets;
   DROP TABLE secrets;
   CREATE TABLE secrets (
       id         TEXT    PRIMARY KEY,                    -- sec_<ULID>
       name       TEXT    NOT NULL,
       mode       TEXT    NOT NULL CHECK (mode IN ('proxy', 'env')),
       value_enc  BLOB    NOT NULL,                       -- AAD：全局 = name；项目 = "project:<project_id>:<name>"，不保密的再加 ":plain"
       hosts      TEXT    NOT NULL DEFAULT '[]',
       created_at INTEGER NOT NULL,
       updated_at INTEGER NOT NULL,
       project_id TEXT    REFERENCES projects (id) ON DELETE CASCADE,   -- NULL = 全局
       secret     BOOLEAN NOT NULL DEFAULT 1,             -- 0 = 普通环境变量（值可读），只用于项目级 env 条目
       CHECK (secret = 1 OR (project_id IS NOT NULL AND mode = 'env'))
   );
   CREATE UNIQUE INDEX secrets_global_name  ON secrets (name) WHERE project_id IS NULL;
   CREATE UNIQUE INDEX secrets_project_name ON secrets (project_id, name) WHERE project_id IS NOT NULL;
   INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
   SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets_keep;
   DROP TABLE secrets_keep;

   -- +goose Down
   -- 回到 0001 的表结构：项目级条目（project_id 非空）无法表示，直接丢弃；全局行的 id 与密文原样保留。
   CREATE TABLE secrets_keep (
       id TEXT, name TEXT, mode TEXT, value_enc BLOB, hosts TEXT, created_at INTEGER, updated_at INTEGER
   );
   INSERT INTO secrets_keep (id, name, mode, value_enc, hosts, created_at, updated_at)
   SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets WHERE project_id IS NULL;
   DROP TABLE secrets;
   CREATE TABLE secrets (
       id         TEXT    PRIMARY KEY,
       name       TEXT    NOT NULL UNIQUE,
       mode       TEXT    NOT NULL CHECK (mode IN ('proxy', 'env')),
       value_enc  BLOB    NOT NULL,
       hosts      TEXT    NOT NULL DEFAULT '[]',
       created_at INTEGER NOT NULL,
       updated_at INTEGER NOT NULL
   );
   INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
   SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets_keep;
   DROP TABLE secrets_keep;
   ```
   新列放在末尾，生成的 `db.Secret` 原有字段顺序不变，只多 `ProjectID *string` 与 `Secret bool`（`sqlc.yaml` 设置了 `emit_pointers_for_null_types`）。
2. 用下面这一段替换 `blobs_secrets.sql:10-33` 的 secrets 部分（注释只用英文；`UpsertSecret` 删除）：
   ```sql
   -- name: InsertSecret :exec
   INSERT INTO secrets (id, project_id, name, mode, secret, value_enc, hosts, created_at, updated_at)
   VALUES (@id, sqlc.narg(project_id), @name, @mode, @secret, @value_enc, @hosts, @created_at, @updated_at);

   -- Overwrite keeps the id (placeholders already delivered stay valid).
   -- name: UpdateSecret :exec
   UPDATE secrets SET mode = @mode, secret = @secret, value_enc = @value_enc, hosts = @hosts, updated_at = @updated_at
   WHERE id = @id;

   -- Global rows only.
   -- name: GetSecretByName :one
   SELECT * FROM secrets WHERE project_id IS NULL AND name = @name;

   -- Placeholder resolution (oryn-ph-<id>) only ever resolves global rows.
   -- name: GetSecret :one
   SELECT * FROM secrets WHERE id = @id AND project_id IS NULL;

   -- Listing never returns ciphertext. Global rows only.
   -- name: ListSecrets :many
   SELECT id, name, mode, hosts, created_at, updated_at FROM secrets WHERE project_id IS NULL ORDER BY name;

   -- name: DeleteSecretByName :execrows
   DELETE FROM secrets WHERE project_id IS NULL AND name = @name;

   -- name: GetProjectSecret :one
   SELECT * FROM secrets WHERE project_id = CAST(@project_id AS TEXT) AND name = @name;

   -- Project rows with ciphertext; the caller opens only what it needs.
   -- name: ListProjectSecrets :many
   SELECT * FROM secrets WHERE project_id = CAST(@project_id AS TEXT) ORDER BY name;

   -- name: DeleteProjectSecret :execrows
   DELETE FROM secrets WHERE project_id = CAST(@project_id AS TEXT) AND name = @name;

   -- Every secret-flagged row (global and project) with its project name, for the known-secret set
   -- (share scan, runner screening, owner-memory redaction). Plain project values are not secrets.
   -- name: ListSealedSecrets :many
   SELECT s.id, s.project_id, s.name, s.value_enc, p.name AS project_name
   FROM secrets s LEFT JOIN projects p ON p.id = s.project_id
   WHERE s.secret = 1
   ORDER BY s.project_id IS NOT NULL, p.name, s.name;

   -- Names and last change of every secret whose value can reach an orb of the given project, for the project
   -- snapshot config hash (M11a; never the values): global env-mode secrets, global model keys
   -- (omp/models/<key>/apiKey, the omp-home render may downgrade them to environment variables), and that project's
   -- env entries, prefixed with "project:" so they never collide with a global name. Pass '' for no project.
   -- name: ListConfigSecretStamps :many
   SELECT CAST(CASE WHEN project_id IS NULL THEN name ELSE 'project:' || name END AS TEXT) AS name, updated_at
   FROM secrets
   WHERE (project_id IS NULL AND (mode = 'env' OR name LIKE 'omp/models/%/apiKey'))
      OR (project_id = CAST(@project_id AS TEXT) AND mode = 'env')
   ORDER BY 1;
   ```
   生成的 `ListConfigSecretStampsRow` 仍是 `{Name string; UpdatedAt int64}`（`CAST … AS TEXT` 的写法同 `owner_memory.sql:204`）。`sameStamps` 的 `slices.Equal`（`projsnap.go:116`）照常可比，没有项目条目的项目哈希与升级前相同。
3. `vault.go` 的全局路径改用新查询，签名都不变：
   - `Put`（`vault.go:63`）：事务内 `GetSecretByName` 命中就 `UpdateSecret`（id 不变；`hosts == nil` 时保留旧值，逻辑照旧），未命中就 `InsertSecret{ProjectID: nil, Secret: true}`，再读回。
   - `metaOf`（`:102`）不变。`Get` / `List` / `Delete` / `Lookup` / `SecretByID` / `GitToken` / `GitHosts` / `EnvSecrets` 不改代码，靠查询里的 `project_id IS NULL` 收窄。
4. 调用点改成传项目：
   - `setup.go:271,279` 改为 `d.Store.Q.ListConfigSecretStamps(ctx, pid)`，`pid` 在 `prj != nil` 时是 `prj.ID`，否则 `""`。
   - `projsnap.go:81` 改为 `q.ListConfigSecretStamps(ctx, prj.ID)`。
   - `projsnap_test.go:36` 改为 `ps.st.Q.ListConfigSecretStamps(ctx, prj.ID)`。
   - `m11a_fixes_test.go:40-45` 的 `putSecret` 改调 `InsertSecret`（`ProjectID: nil, Secret: true`）。
   - 用 `rg -n 'UpsertSecret|ListConfigSecretStamps\(ctx\)' internal` 确认没有遗漏。
5. 新测试 `TestProjectSecretsMigration`，照 `internal/store/m19_test.go` 的写法（`openTemp`、`goose.NewProvider`、`downTo`、`queryInt`）：
   - 在前一版本（48）插入两条全局行，`Up` 后 id 与 `value_enc` 不变、`secret = 1`。
   - 同名全局行插入失败；同项目同名失败；两个项目同名、以及项目与全局同名都成功。
   - 插入 `secret = 0, project_id NULL` 的行违反 CHECK。
   - 删除项目后它的行被级联删掉。
   - `downTo(48)` 后项目行消失、全局行还在，同名插入因 `UNIQUE(name)` 失败；再 `Up` 成功。

验收：`go test ./internal/store/... ./internal/secrets/... ./internal/orbs/...` 通过；`make check-gen` 无差异。具体：在版本 48 插入 `('sec_A','FOO','env',x'00','[]',1,1)`，`Up` 后 `SELECT id, secret FROM secrets` 返回 `sec_A|1`；回滚后 `SELECT COUNT(*) FROM secrets WHERE name='FOO'` 等于全局行数。

### 步骤 4.2 — Vault 的项目级方法与已知密钥集合

依赖：4.1。

文件：修改 `internal/secrets/secrets.go`、`internal/secrets/vault.go` / 测试 `internal/secrets/vault_test.go`、`internal/secrets/secrets_test.go`（两个测试文件都已存在，往里加用例）

做什么：

1. `secrets.go` 新增：
   ```go
   // ErrNoProject 表示项目不存在（含写入期间被删除）。
   var ErrNoProject = errors.New("项目不存在")
   var gitTokenNameRe = regexp.MustCompile(`^git/[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/token$`)
   // ProjectAAD 是项目级条目的附加数据：project:<projectID>:<name>，不保密的再加 ":plain"。
   func ProjectAAD(projectID, name string, secret bool) string
   // ValidProjectEntry 校验项目级条目的名称、模式与保密位。
   func ValidProjectEntry(name, mode string, secret bool) error
   ```
   `ValidProjectEntry` 按下面的顺序判断，返回的错误文本即 400 的 message：
   - `mode` 不是 `env` / `proxy`：`未知模式 %q`。
   - `env` 且名称不匹配 `envNameRe`：`env 模式的密钥名必须匹配 [A-Z_][A-Z0-9_]*：%q`（与 `ValidName` 同文，`secrets.go:99-113`）。
   - `proxy` 且名称以 `omp/models/` 开头：`模型供应商密钥只能在全局密钥里设置：%q`。
   - `proxy` 且不匹配 `gitTokenNameRe`：`项目级 proxy 条目只能是 git/<主机>/token（主机名小写）：%q`。
   - `proxy` 且 `!secret`：`Git 令牌只能保密保存`。
2. `Meta`（`vault.go:19`）加 `ProjectID string`（全局为空串）与 `Secret bool`。新增类型：
   ```go
   // ProjectEntry 是项目级条目；Value 只对不保密的条目填写（解密失败时为 nil 并记日志）。
   type ProjectEntry struct {
   	Meta
   	Value *string
   }
   ```
3. 新增私有函数 `aadOf(row db.Secret) string`：`row.ProjectID == nil` 时是 `row.Name`，否则 `ProjectAAD(*row.ProjectID, row.Name, row.Secret)`。`open`（`:106`）改用它。再抽出 `gitHostOf(name string) (string, bool)`，即 `GitHosts`（`:208-224`）里的解析，两处共用。
4. 新增方法：
   ```go
   func (v *Vault) PutProject(ctx context.Context, projectID, name, mode, value string, secret bool) (Meta, error)
   func (v *Vault) ListProject(ctx context.Context, projectID string) ([]ProjectEntry, error)
   func (v *Vault) DeleteProject(ctx context.Context, projectID, name string) (bool, error)
   func (v *Vault) ProjectEnv(projectID string) (map[string]string, error)
   func (v *Vault) ProjectGitToken(projectID, host string) (tok string, found bool, err error)
   func (v *Vault) ProjectGitHosts(projectID string) ([]string, error)
   ```
   - `PutProject` 先 `ValidProjectEntry`，`value == ""` 时返回 `值不能为空`。然后用 `ProjectAAD` 封存。事务里先 `GetProject`，`sql.ErrNoRows` 时返回 `ErrNoProject`。接着 `GetProjectSecret`，命中就 `UpdateSecret`（保留 id），未命中就 `InsertSecret{ID: ids.New("sec"), ProjectID: &projectID, Secret: secret, Hosts: "[]"}`，最后读回。
   - `ListProject` 用 `ListProjectSecrets`，只解密 `Secret == false` 的行。
   - `ProjectEnv` 取 `mode = env` 的行逐个解密，解密失败返回错误（与 `EnvSecrets` 一致）。
   - `ProjectGitToken` 用 `GetProjectSecret(projectID, GitTokenName(host))`：不存在返回 `("", false, nil)`；读库或解密失败返回 `err`。调用方遇到 `err` 一律拒绝，不回落到全局令牌：项目配的令牌通常比全局的窄，失败时改用更宽的全局令牌正好与「项目优先」相反。
   - `ProjectGitHosts` 对 `ListProjectSecrets` 的名称逐个做 `gitHostOf`。
5. `All`（`:144-166`）改为一次 `ListSealedSecrets`（同时去掉现在的 N+1 读）：全局行的键是 `Name`；项目行的键是 `*ProjectName + ":" + Name`（`ProjectName` 为 nil 时用项目 id）；AAD 用 `ProjectAAD(…, true)`。解密失败照旧跳过并记日志。同时更新 `Vault` 的文档注释（`:28-30`），写明两种 AAD。

验收：`go test ./internal/secrets/...`。新测试：
- `TestVaultProjectScope`（带 `store.Store` 的夹具，用 `st.Q.CreateProject` 建项目 A、B）：
  - `PutProject(A,"FOO","env","bar",false)` 后，`ListProject(A)` 得到 `[{FOO, Secret:false, Value:"bar"}]`，`ListProject(B)` 为空，`List` 不含 FOO，`Lookup("FOO")` 为 `false`，`SecretByID(那条的 id)` 为 `false`。
  - 再写一次保留 id。
  - 删除项目 A 后 `ListProject(A)` 为空。
- `TestVaultProjectAAD`：用 SQL 把 A 的行 `UPDATE secrets SET project_id = B` 后，`ProjectEnv(B)` 返回错误；把 `secret` 改成 0 后同样解不开。
- `TestVaultAllKnownSet`：全局 `X=sk-global-123456`、A 的保密 `S=sk-proj-123456`、A 的不保密 `P=plain-value-123`，`All()` 等于 `{"X":…, "<A 的名字>:S":…}`，不含 `plain-value-123`。
- `TestValidProjectEntry` 表驱动，覆盖上面五条消息，以及 `git/gitlab.com/token` 通过、`git/GitLab.com/token` 与 `git/a/b/token` 被拒。

### 步骤 4.3 — 解析调用点：setup 计划、出站代理、快照哈希

依赖：4.2。

文件：修改 `internal/orbs/setup.go`、`internal/orbs/egress.go` / 测试 `internal/orbs/setup_git_test.go`、`internal/orbs/apply_test.go`、`internal/orbs/coordinator_test.go`、`internal/orbs/m11a_fixes_test.go`、新建 `internal/orbs/project_env_test.go`

做什么：

1. `orbs.Vault` 接口（`setup.go:44-53`）加两个方法：
   ```go
   // ProjectEnv 返回该项目的 env 条目（保密与不保密都含）。
   ProjectEnv(projectID string) (map[string]string, error)
   // ProjectGitHosts 返回该项目有 git/<host>/token 条目的主机。
   ProjectGitHosts(projectID string) ([]string, error)
   ```
   `EgressSecrets`（`egress.go:20-23`）加 `ProjectGitToken(projectID, host string) (tok string, found bool, err error)`。测试替身同步实现：
   - `gitVault`（`setup_git_test.go:23-28`）加字段 `project map[string]string`（键 `<pid>/<host>`）与 `projectErr error`（非 nil 时 `ProjectGitToken` 返回它），`ProjectEnv` 返回空表，`ProjectGitHosts` 返回 nil。
   - `modelVault`（`apply_test.go:48-57`）、`hookVault`（`m11a_fixes_test.go:28-38`）返回空值。
   - `coordVault`（`coordinator_test.go:104-118`）的 `ProjectEnv` 返回 `{"PRJ_ONLY":"x"}`。
   - 用 `rg -n 'GitHosts\(\) \(\[\]string, error\)' internal/orbs` 列出全部替身。
2. `plan()`（`setup.go:295`）在 `GitHosts()` 之后（`:346` 附近）、`prj != nil` 时依次做：
   - `penv, err := d.Vault.ProjectEnv(prj.ID)`，出错返回 `读取项目环境变量: %w`，然后 `for k, v := range penv { p.Env[k] = v }`（项目条目覆盖同名全局 env 密钥）。
   - `phosts, err := d.Vault.ProjectGitHosts(prj.ID)`，出错返回 `读取项目 git 主机: %w`，然后 `hosts = unionHosts(hosts, phosts)`。
   - 新增 `func unionHosts(global, project []string) []string`：全局在前，项目主机只在全局里没有（`strings.EqualFold`）时追加。不用 `withGitHubHost`（`:610`）那种按 github.com 的特判；`gitInsteadOf`（`:592`）的排序不变。
   - `:379` 的注释改为「环境变量降级的模型密钥优先于同名的 env 密钥与项目环境变量」，代码不动。
3. `planRunner`（`:494-536`）与 `planCoordinator`（`:102-176`）不改。证据：`Plan`（`:251-261`）对协调 orb 和 runner 在读项目之前就分流；`planRunner` 不调用任何 env / git 主机方法；`planCoordinator` 只经 `coordinatorMCP`（`:207`）调全局的 `EnvSecrets()` 来展开 MCP 头。项目层的 runner 交付 `RunnerProjectConfigOf`（`:422`）不碰密钥库。runner 的出站通道本来就拒绝 `/git/`（`runnerEgressHandler`），协调宿主则是 `NoGit`。
4. `egress.go` 新增：
   ```go
   // orbProjectID 返回 orb 所属项目 id；orb 不存在或不属于项目时为空串；读库失败返回错误。
   func (s *Service) orbProjectID(ctx context.Context, orbID string) (string, error)
   ```
   照 `memoryCaller`（`egress.go:133-156`）用 `s.st.Q.GetOrb`，`sql.ErrNoRows` 时返回 `""`。`egressResolver.GitToken`（`:48-61`）中，github.com 分支原样保留；在 `r.s.secrets == nil` 的判断之后插入：取 10s 超时的 ctx 调 `orbProjectID`，出错记 `slog.Warn("读取 orb 项目失败，拒绝 git 令牌", …)` 并返回 `"", false`。`pid != ""` 时调 `r.s.secrets.ProjectGitToken(pid, host)`：`err != nil` → 记 `slog.Warn("读取项目 git 令牌失败，拒绝", …)` 并返回 `"", false`；`found` → 返回它；只有 `!found && err == nil` 才走原来的 `r.s.secrets.GitToken(host)`。同时更新注释。
5. `TestEgressResolverGitHubToken`（`setup_git_test.go:401-426`）的 `Service` 改为带 `st`（`store.Open(ctx, t.TempDir())`），否则非 github.com 分支会对 nil store 调用 `GetOrb`；断言不变（orb 不存在 → 无项目 → 全局）。

验收：`go test ./internal/orbs/...`。新测试：
- `project_env_test.go` 的 `TestPlanProjectEnvPrecedence`：真实 `store` + `secrets.NewVault(st, secrets.New(make([]byte,32)))`。全局 `Put("FOO","env","baz")`，A 上 `PutProject("FOO","env","bar",false)` 与 `PutProject("git/gitlab.example.com/token","proxy","t",true)`。`DefaultSetup{Store, Vault}.Plan`：
  - `db.Orb{ProjectID:&A}` 的 `Params.Env["FOO"] == "bar"`，`Params.GitHosts` 含 `gitlab.example.com`；
  - `db.Orb{ProjectID:&B}` 的值是 `"baz"`，GitHosts 不含该主机；
  - `db.Orb{Kind: new(KindRunner)}` 的 Env 为空；
  - `db.Orb{Coordinator:true}` 的 Env 不含 `FOO`。
- `TestPlanCoordinator`（`coordinator_test.go`）加断言：Env 不含 `PRJ_ONLY`。
- `m11a_fixes_test.go` 新增 `TestConfigHashProjectEnvStamps`（写在 `TestConfigHashModelKeyStamps` 旁，用新辅助函数 `putProjectSecret(prj, name, mode, updatedAt)` 调 `InsertSecret`）：项目 a 的 env 条目改变 a 的 `currentHash`，不改变 b 的；a 的 `git/x.example/token` 不改变哈希。
- `TestEgressResolverProjectGitToken`：真实 store 与 vault，`CreateProvider` + 两台 `CreateOrb`（写法照 `memory_egress_test.go:63-70`）。A 有项目 gitlab.com 令牌 `ptok`，全局是 `gtok`：
  - 项目令牌读取出错（替身的 `projectErr` 非 nil）时，orbA 的 `GitToken("gitlab.com")` 返回 `"", false`，不回落到 `gtok`；
  - orbA 的 `GitToken("gitlab.com")` 返回 `ptok`，orbB 返回 `gtok`；
  - `stubGitHub{state: GitHubTokenOK}` 下 orbA 的 `GitToken("github.com")` 返回 OAuth 令牌，即使 A 设了 `git/github.com/token`；
  - `GitHubTokenUnmanaged` 下返回项目的 github.com 令牌。

### 步骤 4.4 — 镜像拉取用项目令牌（阶段 ②）

依赖：4.3，以及阶段 ② 的 `internal/repos`。

文件：修改 `internal/repos/repos.go`（`Secrets` 接口）、`internal/repos/git.go`（`remote()`）/ 测试 `internal/repos/git_test.go`、`internal/repos/testutil_test.go`（密钥替身）

做什么：

1. `repos.Secrets` 接口（步骤 2.4：`type Secrets interface{ GitToken(host string) (string, bool) }`）多一个 `ProjectGitToken(projectID, host string) (tok string, found bool, err error)`（`*secrets.Vault` 已实现）；同包测试替身同步实现。
2. 非 github.com 主机：先 `ProjectGitToken(projectID, host)`。`err != nil` → 这次拉取不带令牌也不回落，直接记 `auth_failed`（message 为「读取项目 Git 令牌失败」）；`found` → 用它；`!found && err == nil` 才用阶段 ② 原来的全局 `GitToken(host)`。github.com：保持阶段 ② 的顺序，只把其中查 PAT 的那一步改为同样的「先项目、后全局」。与 4.3 的出站代理同一条优先级。
3. 拉取失败的文案与状态沿用阶段 ②。镜像目录 `<data_dir>/repos/<projectId>.git` 不动。托管仓库（阶段 ③，`kind = hosted`）没有上游，不调 `remote()`，不受本步影响。

验收：`go test ./internal/repos/...`。新增用例：项目 `git/<测试主机>/token = p1`、全局 `= g1`，拉取凭据解析得到 `p1`；删掉项目条目后得到 `g1`；另一个项目得到 `g1`。

### 步骤 4.5 — REST 接口

依赖：4.2。

文件：修改 `schema/openapi.yaml` / 新建 `internal/api/projectsecrets.go` / 生成 `internal/apigen/apigen.gen.go`、`web/src/api/schema.d.ts` / 测试新建 `internal/api/projectsecrets_test.go`

做什么：

1. 在 `openapi.yaml` 的 `/api/v1/projects/{id}/agent-config/import` 之后（`:551-575` 段末）加：
   ```yaml
   /api/v1/projects/{id}/secrets:
     parameters:
       - { $ref: '#/components/parameters/ProjectIdPath' }
     get:
       tags: [projects]
       operationId: listProjectSecrets
       summary: 项目级环境变量与 Git 令牌（阶段 ④；保密条目不含值）
       description: 项目不存在 → 404 `not_found`。已归档的项目照常可读写。只对本项目的 orb 生效，与全局同名时以项目为准；不下发给 Runner 线程与协调宿主。
       responses:
         '200':
           description: 按名称排序
           content:
             application/json:
               schema: { $ref: '#/components/schemas/ProjectSecretList' }
         default: { $ref: '#/components/responses/Error' }
   /api/v1/projects/{id}/secrets/{name}:
     parameters:
       - { $ref: '#/components/parameters/ProjectIdPath' }
       - { $ref: '#/components/parameters/SecretNamePath' }
     put:
       tags: [projects]
       operationId: putProjectSecret
       summary: 写入或覆盖项目级条目（覆盖保留原 id）
       description: |
         `mode:"env"`：名称须匹配 `[A-Z_][A-Z0-9_]*`；`secret` 缺省 true，`secret:false` 的值之后可经列表读回。
         `mode:"proxy"`：名称只能是 `git/<主机>/token`（主机小写），且必须保密；模型供应商密钥（`omp/models/…`）只能在全局 `/secrets` 设置。
         校验失败或值为空 → 400 `invalid_request`；项目不存在（含写入期间被删除）→ 404 `not_found`。
       security:
         - cookieAuth: []
           csrfHeader: []
         - deviceBearer: []
       requestBody:
         required: true
         content:
           application/json:
             schema: { $ref: '#/components/schemas/ProjectSecretPut' }
       responses:
         '200':
           description: 写入后的条目
           content:
             application/json:
               schema: { $ref: '#/components/schemas/ProjectSecret' }
         default: { $ref: '#/components/responses/Error' }
     delete:
       tags: [projects]
       operationId: deleteProjectSecret
       summary: 删除项目级条目
       description: 项目或条目不存在 → 404 `not_found`。
       security:
         - cookieAuth: []
           csrfHeader: []
         - deviceBearer: []
       responses:
         '204': { description: 已删除 }
         default: { $ref: '#/components/responses/Error' }
   ```
   在 `SecretPut`（`:10928`）之后加 schemas：
   ```yaml
   ProjectSecret:
     type: object
     required: [name, mode, secret, createdAt, updatedAt]
     properties:
       name: { type: string }
       mode: { $ref: '#/components/schemas/SecretMode' }
       secret: { type: boolean, description: 'true = 保存后不可读（不给 `value`）；false = 普通环境变量' }
       value: { type: string, description: '只在 `secret:false` 时给出' }
       createdAt: { $ref: '#/components/schemas/TimestampMs' }
       updatedAt: { $ref: '#/components/schemas/TimestampMs' }
   ProjectSecretList:
     type: object
     required: [secrets]
     properties:
       secrets:
         type: array
         items: { $ref: '#/components/schemas/ProjectSecret' }
   ProjectSecretPut:
     type: object
     required: [value, mode]
     properties:
       value: { type: string }
       mode: { $ref: '#/components/schemas/SecretMode' }
       secret: { type: boolean, default: true }
   ```
2. `projectsecrets.go`：
   - `projectSecretJSON(e secrets.ProjectEntry) apigen.ProjectSecret`，`Value` 只在 `!e.Secret` 时赋值。
   - `ListProjectSecrets(w, r, id apigen.ProjectIDPath)`：先 `s.Store.Q.GetProject`，`ErrNoRows` 时回 404 `not_found`「项目不存在」（同 `projectagentconfig.go:17-20`）。
   - `PutProjectSecret(w, r, id apigen.ProjectIDPath, name apigen.SecretNamePath)`：`decodeJSON`；`!req.Mode.Valid()` 时回 400「mode 必须是 proxy 或 env」（同 `secrets.go` 的 `PutSecret`）；`secret := req.Secret == nil || *req.Secret`；`ValidProjectEntry` 出错回 400，message 为 `err.Error()`；`req.Value == ""` 时回 400「值不能为空」；`Vault.PutProject` 返回 `errors.Is(err, secrets.ErrNoProject)` 时回 404「项目不存在」，其余错误交 `writeErr`。成功后 `s.poolKick(id)`，回 200 并带 `secret` 与 `value`（不保密时）。
   - `DeleteProjectSecret`：项目不存在回 404「项目不存在」；`DeleteProject` 返回 false 时回 404「条目不存在」；成功回 204 并 `s.poolKick(id)`。
3. 助手与协调者碰不到这些接口：`internal/coord/admintools.go:23-36` 的工具里没有密钥相关的，`profile.go:52,55` 的提示词也写明不能改密钥；本阶段不加工具。

验收：`make gen && go test ./internal/api/...`，提交后 `make check-gen`。`TestProjectSecretsAPI`（`newH2Env` + `h2DeviceToken`，写法照 `h2_resources_test.go:114-135`）：
- `GET /api/v1/projects/prj_nope/secrets` 回 404。
- `PUT …/secrets/lower {value:"v",mode:"env"}` 回 400。
- `PUT …/secrets/omp%2Fmodels%2Fx%2FapiKey {mode:"proxy"}` 回 400，body 含「只能在全局」。
- `PUT …/secrets/git%2Fgitlab.com%2Ftoken {value:"t",mode:"proxy",secret:false}` 回 400。
- `PUT …/secrets/FOO {value:"bar",mode:"env",secret:false}` 回 200，body 含 `"value":"bar"`。
- `PUT …/secrets/TOK {value:"sk-x",mode:"env"}` 回 200，body 不含 `sk-x`，`GET` 列表也不含。
- 全局 `GET /api/v1/secrets` 不含 `FOO`。
- 把项目 `PATCH {archived:true}` 后 PUT 仍回 200。
- `DELETE …/secrets/FOO` 回 204，再删一次回 404。

### 步骤 4.6 — CLI：`oryn secrets … --project`

依赖：4.5。

文件：修改 `cmd/oryn/secrets.go` / 测试新建 `cmd/oryn/secrets_test.go`

做什么：

1. 照 `ssh.go:25-52` 把包级变量（`secretsCmd`、`secretsSetOpts`、`secretsLsJSON` 等，`secrets.go:18-105`）改为 `func newSecretsCmd(client func() (*apiClient, error)) *cobra.Command`，`init` 里改成 `root.AddCommand(newSecretsCmd(newClient))`。输出改用 `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`，标准输入改用 `cmd.InOrStdin()`，JSON 输出用 `printJSONTo(cmd.OutOrStdout(), …)`（`client.go:196`）。
2. 三个子命令都加 `--project <名称或 id>`，用 `resolveProject`（`thread.go:210`，与 `config push --project`（`config.go:128`）同一个函数）解析。它只列未归档项目，所以已归档的项目要在网页上改。
3. `set` 加 `--plain`：
   - 不带 `--project` 时报 `usageErr("--plain 只能与 --project 一起使用")`；带 `--project` 又给了 `--hosts` 时报 `usageErr("--hosts 不能与 --project 一起使用：项目级 Git 令牌只注入它自己的主机")`。
   - 带 `--project` 且没给 `--mode`（`Flags().Changed("mode")` 为假）时，名称以 `git/` 开头就用 `proxy`，否则用 `env`。
   - 发 `PUT /api/v1/projects/<id>/secrets/<escapePathParam(name)>`，body 是 `ProjectSecretPut{Value, Mode, Secret: new(!plain)}`。
   - 成功后 stderr 输出 `已写入项目 <项目名> 的 <name>（<mode>，保密|不保密）`；全局写入的输出照旧（`已写入 %s（%s）`）。
   - 更新 `Long` 说明，写清 `--project` 与 `--plain`。
4. `ls --project`：表头 `NAME\tKIND\tSECRET\tVALUE\tUPDATED`。`KIND` 取 `env`，`mode = proxy` 时为 `git`；`SECRET` 为 `yes` / `no`；`VALUE` 是不保密条目的值（含 `\t` `\n` `\r` 时用 `strconv.Quote`），保密条目写 `-`。`--json` 输出 `ProjectSecretList`。不带 `--project` 时输出不变。
5. `rm --project` 发 `DELETE /api/v1/projects/<id>/secrets/<name>`。

验收：`go test ./cmd/oryn/...`。用 `newFakeAPI`（`thread_test.go:36`）+ `runSecrets`（写法照 `ssh_test.go:20-31`）：
- `set FOO bar --project demo --plain` 发 `PUT /api/v1/projects/prj_A/secrets/FOO`，body 为 `{"value":"bar","mode":"env","secret":false}`。
- `set git/gitlab.com/token t --project demo` 的 body 为 `mode: "proxy"`、`secret: true`。
- `set X v --plain` 返回用法错误且不发请求。
- `ls --project demo` 的输出含 `FOO  env  no  bar`。
- `rm FOO --project demo` 发 `DELETE`。

### 步骤 4.7 — 网页：项目设置「密钥与环境变量」分组

依赖：4.5，以及阶段 ① 的设置子页。

文件：新建 `web/src/api/project-secrets-queries.ts`、`web/src/lib/dotenv.ts`、`web/src/pages/projects/settings/SecretsTab.tsx` / 修改 `web/src/pages/projects/settings/ProjectSettingsPage.tsx`（`SETTINGS_TABS` 与分组渲染）、`web/src/pages/projects/settings/ProjectSettingsPage.test.tsx`、`web/src/mock/server.ts`、`web/src/mock/fixtures.ts`、两个语言文件 / 测试新建 `web/src/lib/dotenv.test.ts`、`web/src/pages/projects/settings/SecretsTab.test.tsx`

做什么：

1. `project-secrets-queries.ts`。按功能分文件，同 `ssh-queries.ts`、阶段 ② 的 `repo-queries.ts`：
   ```ts
   export type ProjectSecret = Schemas["ProjectSecret"];
   export type ProjectSecretPut = Schemas["ProjectSecretPut"];
   export const projectSecretKeys = { list: (projectId: string) => ["projects", projectId, "secrets"] as const };
   export function useProjectSecrets(projectId: string)   // GET，queryKey = list(projectId)
   export function putProjectSecret(projectId: string, name: string, body: ProjectSecretPut): Promise<ProjectSecret>
   export function deleteProjectSecret(projectId: string, name: string): Promise<void>
   ```
   键挂在 `["projects", id]` 下，同 `qk.projectSnapshots`（`queries.ts:18`）。写入沿用 `SecretsSettings` 的 `useAsync` + `unwrap(api.…)` 写法（`SettingsPage.tsx:483-640`），不用 `ssh-queries` 的 `useMutation`；成功后 `invalidateQueries({queryKey: projectSecretKeys.list(id)})`。没有 WS 推送，别的设备在下次重新取数时看到。
2. `lib/dotenv.ts`（仓库里没有 .env 解析器；`lib/highlight.ts:96` 只是高亮语言名）：
   ```ts
   export const ENV_NAME_RE = /^[A-Z_][A-Z0-9_]*$/;
   export interface DotenvEntry { line: number; name: string; value: string }
   export type DotenvIssueReason = "noEquals" | "badName" | "emptyValue" | "unterminatedQuote" | "trailingText" | "duplicate";
   export interface DotenvIssue { line: number; reason: DotenvIssueReason; other?: number }
   export function parseDotenv(text: string): { entries: DotenvEntry[]; issues: DotenvIssue[] }
   ```
   规则：
   - 按 `\r?\n` 分行，行号从 1 开始；trim 后为空或以 `#` 开头的行跳过。
   - 去掉开头的 `export` 加至少一个空白。
   - 没有 `=` 记 `noEquals`；`=` 前 trim 后的名称不匹配 `ENV_NAME_RE` 记 `badName`（不自动转大写）。
   - 值取 `=` 之后、去掉前导空白的部分：
     - 以 `"` 开头：在同一行找第一个未转义的 `"`，找不到记 `unterminatedQuote`（不支持跨行）；引号内 `\n` 转成换行，`\"` 转成 `"`，`\\` 转成 `\`，其它 `\x` 原样保留。
     - 以 `'` 开头：找下一个 `'`，不处理转义。
     - 两种引号闭合后，剩余部分 trim 后必须为空或以 `#` 开头，否则记 `trailingText`。
     - 不带引号：从第一个「空白 + `#`」起截掉（行内注释），再去尾部空白；`a#b` 原样保留。
   - 结果为空串时记 `emptyValue`（服务端拒绝空值）。
   - 同名出现多次时以后者为准，前面那行记 `duplicate`，`other` 填后者的行号。
   - `SecretsTab` 也从这里取 `ENV_NAME_RE`，全前端只有这一份定义。
3. `SecretsTab.tsx` 导出 `export function SecretsTab({ project }: { project: Project })`，与阶段 ① 的其它 `*Tab.tsx` 同形；下文与测试里的 `projectId` 一律取 `project.id`。根元素是 `<div data-testid="project-secrets" className="flex flex-col gap-4">`，自上而下：
   - **说明**：`<p className="text-muted text-sm">{t("projectSecrets.intro")}</p>`。
   - **添加**：`Card`（`title=projectSecrets.addTitle`、`icon="plus"`）里放 `EntryForm`。
     - 字段：`Field` + `Select`「类型」（env / git）；env 时是 `TextInput`「变量名」（提示 `nameHint`），git 时是 `TextInput`「主机」（提示 `hostHint`，提交时 trim 并转小写，名称拼成 `git/<host>/token`）；`TextInput`「值」，保密时 `type="password" autoComplete="off"`；`Checkbox`「保密」默认勾选，git 时勾选并禁用、下方显示 `gitAlwaysSecret`，env 时下方显示 `secretHint`。
     - 保存按钮 `variant="primary"`，以下情况禁用：值为空；env 名称不匹配 `ENV_NAME_RE`；git 主机不匹配 `/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/`；正在保存。
     - 失败用 `ErrorText` 显示服务端 message；成功清空表单并让列表失效。
   - **列表**：`Card`（`title=projectSecrets.listTitle`、`icon="key"`、`action` = `Button variant="secondary" size="sm" icon="upload"`「批量添加」）。
     - 每行一个 `ListRow`（`components/ui/data.tsx:315`），外层是 `<ul className="-mx-2 flex flex-col">`，同 `SettingsPage` 的 `RowList`（`:136-138`，那是页内私有组件，全局页不动，所以不抽出共享）。每个 `li` 带 `data-testid="project-secret-row" data-name={name}`。
     - `title` = 名称（`font-mono truncate`；git 条目显示主机）+ `Badge`：env 用 `tone="frost"` 写「环境变量」，git 用 `icon="branch"` 写「Git 令牌」。
     - `meta`：不保密时显示值（`font-mono truncate`，`title` 放完整值）；保密时显示 `••••••••`（`aria-label=projectSecrets.hidden`）加 `Badge icon="lock"`「保密」；最后是 `formatRelative(updatedAt)`。
     - `trailing`：`IconButton icon="edit" label=editLabel` 打开编辑对话框；`IconButton icon="trash" label=deleteLabel` 先走 `confirmAction({title: deleteConfirm, body: deleteBody, action: t("common.delete"), danger: true})`（`lib/confirm.ts`，不用 `window.confirm`），确认后删除。
     - 读取中显示 `Spinner`；空列表显示 `EmptyState icon="key" title=empty` 加 `emptyBody`。
   - **编辑**：`Dialog`（`components/ui/primitives.tsx:141`，标题 `editTitle`）里放同一个 `EntryForm`，传入 `existing`：类型与名称（主机）只读；不保密时值预填 `existing.value`；保密时值为空、placeholder 为 `newValue`。在对话框里把「保密」改成不保密也必须重新输入值。保存后关闭。
   - **批量添加**：`Dialog`（`data-testid="project-secrets-bulk"`、标题 `bulkTitle`），弹层统一走 `Dialog`，不手写定位。
     - 内容：提示 `bulkHint`；`TextArea`（mono，`rows={8}`，placeholder 为 `FOO=bar\nexport TOKEN="…"`）；`useMemo(() => parseDotenv(text))`。
     - 预览：标题 `bulkPreview`（计数）；每项一行：名称、值（勾了保密就显示点，否则截断显示）、`Checkbox`「保密」默认勾选；与现有条目同名的加 `Badge` `bulkOverwrite`。
     - 问题：有问题时显示 `bulkIssues`，逐条写 `bulkLine` + `issue.<reason>`。
     - `footer`：取消 + `Button variant="primary"`（`bulkImport`，计数为 0 或进行中时禁用；进行中文字换成 `bulkProgress`）。
     - 导入：逐个 `await putProjectSecret(…, {value, mode: "env", secret})`，按顺序发；全部成功后关闭对话框；有失败时对话框留着，预览只剩失败的项，每项下方用 `ErrorText` 显示服务端 message，顶部显示 `bulkFailed`。
     - 结束时不论成败都让列表失效。
4. 登记分组（`ProjectSettingsPage.tsx`）：`SETTINGS_TABS` 末尾追加 `"secrets"`；导航图标 `key`；文案走阶段 ① 的 `t(`projectSettings.tabs.${tab}`)`，所以新 key 是 `projectSettings.tabs.secrets`；内容 `<SecretsTab project={project} />`。`ProjectSettingsPage.test.tsx` 里「390 宽显示 6 行的列表」改为 7 行。
5. 文案，全部放在 `projectSecrets` 下：

   | key | zh-CN | en |
   |---|---|---|
   | `projectSettings.tabs.secrets`（完整 key，不在 `projectSecrets` 下） | 密钥与环境变量 | Secrets & env vars |
   | `intro` | 这里的值只注入本项目的 orb（setup 脚本、agent、受管服务），与全局密钥同名时以这里为准；不会下发给 Runner 线程。改动在 orb 下次启动时生效，并使项目快照失效，新 orb 会重新跑 setup。 | Values here are injected only into this project's orbs (setup script, agent, managed services) and override a global secret with the same name. They are never sent to Runner threads. Changes apply the next time an orb starts and invalidate the project snapshot, so new orbs run setup again. |
   | `addTitle` | 添加 | Add |
   | `listTitle` | 本项目的变量与令牌 | Variables and tokens |
   | `kind` | 类型 | Type |
   | `kindEnv` | 环境变量 | Env var |
   | `kindGit` | Git 令牌 | Git token |
   | `name` | 变量名 | Name |
   | `nameHint` | 大写字母、数字和 _，不能以数字开头 | Uppercase letters, digits and _, not starting with a digit |
   | `host` | 主机 | Host |
   | `hostHint` | 如 gitlab.com；本项目的克隆、推送与镜像拉取都用它 | e.g. gitlab.com. Used for this project's clone, push and mirror fetch |
   | `value` | 值 | Value |
   | `newValue` | 输入新值 | Enter a new value |
   | `secret` | 保密 | Secret |
   | `secretHint` | 保存后不能再查看，只能覆盖 | Can't be viewed after saving, only replaced |
   | `gitAlwaysSecret` | Git 令牌总是保密保存 | Git tokens are always stored as secrets |
   | `hidden` | 已隐藏 | Hidden |
   | `editLabel` / `editTitle` | 编辑 {{name}} | Edit {{name}} |
   | `deleteLabel` | 删除 {{name}} | Delete {{name}} |
   | `deleteConfirm` | 删除 {{name}}？ | Delete {{name}}? |
   | `deleteBody` | 本项目的 orb 下次启动时不再有这个值；同名的全局密钥（如有）会重新生效。 | This project's orbs won't get this value from their next start; a global secret with the same name, if any, applies again. |
   | `empty` | 还没有项目级的变量或令牌 | No project variables or tokens yet |
   | `emptyBody` | 添加一项，或用「批量添加」粘贴 .env 内容。 | Add one, or paste a .env file with Bulk add. |
   | `bulk` | 批量添加 | Bulk add |
   | `bulkTitle` | 批量添加环境变量 | Bulk add env vars |
   | `bulkHint` | 粘贴 .env 内容：每行一个 KEY=value，支持 # 注释、export 前缀与引号。 | Paste .env content: one KEY=value per line; # comments, export prefixes and quotes are supported. |
   | `bulkPreview_one` / `_other` | 将导入 {{count}} 项 | {{count}} entry to import / {{count}} entries to import |
   | `bulkOverwrite` | 覆盖现有 | Replaces existing |
   | `bulkIssues` | 以下行不会导入 | These lines won't be imported |
   | `bulkLine` | 第 {{line}} 行 | Line {{line}} |
   | `issue.noEquals` | 缺少 = | Missing = |
   | `issue.badName` | 变量名须为大写字母、数字和 _，不能以数字开头 | Name must be uppercase letters, digits and _, not starting with a digit |
   | `issue.emptyValue` | 值为空 | Empty value |
   | `issue.unterminatedQuote` | 引号没有闭合（不支持跨行的值） | Unclosed quote (multi-line values aren't supported) |
   | `issue.trailingText` | 引号后还有多余内容 | Extra text after the closing quote |
   | `issue.duplicate` | 与第 {{other}} 行重名，以后者为准 | Same name as line {{other}}; the later one wins |
   | `bulkImport_one` / `_other` | 导入 {{count}} 项 | Import {{count}} entry / Import {{count}} entries |
   | `bulkProgress` | 正在导入 {{done}}/{{total}} | Importing {{done}}/{{total}} |
   | `bulkFailed_one` / `_other` | {{count}} 项没有导入，原因见下 | {{count}} entry wasn't imported; see below / {{count}} entries weren't imported; see below |

   保存、取消、删除按钮用现有的 `common.save` / `common.cancel` / `common.delete`（`zh-CN.json:80-92`）。
6. mock 服务端：
   - `fixtures.ts` 导出 `projectSecrets: Record<string, Schemas["ProjectSecret"][]>`（`Schemas` 已在 `fixtures.ts:7` 导入）。给 `projects[0]`（`prj_01JCZ0000000000000000ORYN`）三条：`NODE_ENV`（env、不保密、值 `development`、2 小时前更新）、`SENTRY_DSN`（env、保密、26 小时前）、`git/gitlab.com/token`（proxy、保密、3 天前）。
   - `server.ts` 照 `snapshots`（`:528-533`）复制成实例字段 `readonly projectSecrets = new Map(…)`。在 `prjCfg` 段（`:1560-1615`）之后加路由 `/^projects\/([^/]+)\/secrets(?:\/(.+))?$/`：
     - 项目不存在回 404 `{code:"not_found",message:"项目不存在"}`；
     - GET 回 `{secrets}`（保密条目去掉 `value`）；
     - PUT 时名称用 `decodeURIComponent`，按 4.2 的五条规则与空值校验，回 400 并带与服务端相同的 message；通过后 upsert（不保密才存 `value`，`updatedAt: Date.now()`），回 200；
     - DELETE 时不存在回 404「条目不存在」，否则回 204。

验收：`cd web && bun run check && bunx vitest run src/lib/dotenv.test.ts src/pages/projects/settings/SecretsTab.test.tsx src/i18n/locales.test.ts`。
- `dotenv.test.ts` 用表驱动用例（`it.each`）：
  - `"FOO=bar"` 得到 `[{line:1,name:"FOO",value:"bar"}]`；
  - `"export TOKEN='a b'"` 得到 `a b`；
  - `'X="l1\\nl2" # c'` 的值是 `"l1\nl2"`；
  - `"URL=a#b"` 得到 `a#b`；`"URL=a #b"` 得到 `a`；
  - `"# c\n\nFOO=1"` 的 entries 行号是 3；
  - `"foo=1"` 记 `badName@1`；`"NOEQ"` 记 `noEquals`；`'A="x'` 记 `unterminatedQuote`；`'A="x" y'` 记 `trailingText`；`"A="` 记 `emptyValue`；
  - `"A=1\nA=2"` 的 entries 为 `[A=2@2]`，issues 为 `[duplicate@1 other:2]`。
- `SecretsTab.test.tsx` 用 `MockServer` + `vi.stubGlobal("fetch")`，写法照 `components/layout/ProjectsPane.test.tsx:47-60`，另外记下请求：
  - `NODE_ENV` 行显示 `development`，`SENTRY_DSN` 行不显示值、有「保密」徽标；
  - 选 env、填 `FOO`/`bar`、取消「保密」后保存，发出 `PUT …/secrets/FOO`，body 为 `{"value":"bar","mode":"env","secret":false}`；
  - 切到 Git 令牌后「保密」被勾选且禁用；
  - 删除 `NODE_ENV` 后调 `settleConfirm(true)`，发出 `DELETE`；
  - 批量添加粘贴 `"A=1\nfoo=2\nFAIL_ME=3"`，显示「将导入 2 项」和「第 2 行」，导入后两个 PUT 按顺序发出；fetch 替身对 `FAIL_ME` 回 500，对话框留下只剩 `FAIL_ME` 的预览。

### 步骤 4.8 — ui-audit 与截图登记

依赖：4.7。

文件：修改 `web/e2e/ui-audit/projects.e2e.ts`、`docs/ui-audit/areas/sections.md`、`web/scripts/screenshots.ts`

做什么：

1. `projects.e2e.ts` 里加 `const SECRETS = \`/projects/${PRJ}/settings/secrets\``，再加三个 `surface`（id 前缀与阶段 ① 的 `projects.settings.*` 一致）：
   - `projects.settings.secrets`：`path: SECRETS`，`ready: "[data-testid=project-secrets] [data-testid=project-secret-row]"`；
   - `projects.settings.secrets.bulk`：点「批量添加」，往 `TextArea` 填 `"NODE_ENV=production\nexport API_URL=\"https://api.example.com\"\nbad line"`，返回 `page.getByRole("dialog")`；
   - `projects.settings.secrets.edit`：点「编辑 NODE_ENV」，返回 `page.getByRole("dialog")`。
2. `sections.md` 的清单表加三行：
   - `| \`projects.settings.secrets\` | 项目设置「密钥与环境变量」 | 项目页设置 → 左导航「密钥与环境变量」 | 页面 |`
   - `| \`projects.settings.secrets.bulk\` | 批量添加（粘贴 .env 与预览） | 该分组「批量添加」→ 粘贴文本 | 弹窗（手机为抽屉） |`
   - `| \`projects.settings.secrets.edit\` | 编辑条目 | 某行「编辑」 | 弹窗（手机为抽屉） |`
3. `screenshots.ts` 的 `SCREENS`（`:1077`）加两项：
   - `{ name: "project-secrets", prepare: ({page}) => open(page, \`/projects/${projects[0]?.id}/settings/secrets\`, "[data-testid=project-secret-row]") }`；
   - `project-secrets-bulk`：同一路径，点「批量添加」，填上面那段文本，等 `[data-testid=project-secrets-bulk]` 出现后 `settle(page, 400)`。

验收：`cd web && npx playwright test e2e/ui-audit/projects.e2e.ts` 三个新 surface 在全部视口通过；`cd web && ORYN_SHOTS_DIR=/tmp/oryn-shots/phase4 bun run shots:all --only project-secrets` 产出明暗两套主题、1280 与 390 宽的截图，并按 `docs/design/web-guidelines.md` 第 3 节亲自看图（390 与 360 宽下行内值截断不折行）。

### 步骤 4.9 — 文档

依赖：4.1–4.8。

文件：修改 `docs/features.md`、`docs/protocol.md`、`README.md`

做什么：

1. `docs/features.md`：密钥相关的段落后加「项目级密钥与环境变量」：能设什么（环境变量、`git/<host>/token`）、不能设什么（模型供应商密钥）、同名时的优先级、保密与不保密的区别、粘贴 `.env` 批量添加、谁拿不到（Runner 线程、协调宿主、助手）、改动使项目快照失效。「项目」小节的设置分组清单加「密钥与环境变量」。
2. `docs/protocol.md`：按该文件里各里程碑的写法，在 M24 一节补 `secrets` 表的新列（`project_id`、`secret`）与两条部分唯一索引、两种 AAD、`/api/v1/projects/{id}/secrets[/{name}]` 的请求与响应、解析优先级（setup 计划、出站代理 Git 令牌、镜像拉取）。
3. `README.md`：CLI 示例块里 `oryn secrets set …` 附近加一行 `oryn secrets set FOO bar --project <项目> --plain`。

验收：`rg -n 'secrets set .*--project' README.md docs/features.md` 至少各有一处命中。

## 阶段 ④ 验收

- `make gen && make check-gen`：无差异。
- `make test`：通过，其中重点：
  - `go test ./internal/orbs/ -run 'TestPlanProjectEnvPrecedence|TestConfigHashProjectEnvStamps|TestEgressResolverProjectGitToken|TestPlanCoordinator'`：A 项目 `FOO=bar`（不保密）+ 全局 `FOO=baz`（保密）时，A 的 orb 是 `FOO=bar`，B 的 orb 是 `FOO=baz`，runner 与协调 orb 没有 `FOO`；A 的 env 条目只改变 A 的配置哈希；gitlab.com 上 A 的 orb 用项目令牌，B 的用全局令牌；GitHub 管着的 github.com 仍走 OAuth。
  - `go test ./internal/secrets/ -run 'TestVaultProjectScope|TestVaultProjectAAD|TestVaultAllKnownSet|TestValidProjectEntry'`：密文换项目或翻转保密位后解不开；`All()` 含 `<项目名>:S`，不含不保密的值。
  - `go test ./internal/store/ -run TestProjectSecretsMigration`、`go test ./internal/api/ -run TestProjectSecretsAPI`、`go test ./cmd/oryn/ -run TestSecrets`、`go test ./internal/repos/...`（4.4）。
- `cd web && bun run check && bun run test`：通过。
- `cd web && npx playwright test e2e/ui-audit/projects.e2e.ts`：通过。
- 手工检查（`bun run dev:mock` 或真实 orynd）：
  - `oryn secrets set FOO bar --project <A> --plain` 后，`oryn secrets ls --project <A>` 显示 `FOO env no bar`；`oryn secrets ls`（全局）没有 FOO。
  - 在 A 的设置里粘贴 `.env` 批量导入三项后，列表出现三行，保密的只显示点。
  - 在 A 里新建线程，orb 启动后 agent 里 `echo $FOO` 输出 `bar`；在 B 里输出全局值。
  - 对一个公开分享的线程，转录里出现 A 的保密值时，扫描命中显示 `<A 的名字>:<NAME>`；出现不保密的值时不命中。

## Verification

四个阶段各自的「验收」一节是该阶段的合入门槛。全部完成后再整体过一遍：

1. 生成物与规格：`make gen && make lint-spec`，提交后 `make check-gen` 无差异。
2. Go：`ORYN_REQUIRE_GIT=1 ORYN_REQUIRE_GIT_LFS=1 make test`。带上这两个变量后，主机缺 `git` 或 `git-lfs` 时相关用例直接失败而不是跳过（CI 的 go job 同样这样设）。
3. 前端：`cd web && bun run check && bun run test && bun run build`。
4. 界面审计：`cd web && npx playwright test e2e/ui-audit --grep projects` 全绿。
5. 截图与评审：`cd web && ORYN_SHOTS_DIR=/tmp/oryn-shots/m23 bun run shots:all --only project`，亮暗 × 390 / 1280 逐张看过。
6. 真机走查（dev 环境的 orynd 加一台 orb）：
   - 外部 GitHub 项目：打开项目主页，镜像克隆完成后出现「提交到 main」「文件」「README」；在线程里让 agent 推一个提交，提交列表里该提交关联到这条线程。
   - 带 LFS 的仓库：文件页里 LFS 图片正常显示；orb 里 `git lfs ls-files` 有内容，工作区文件不是指针；agent 新增一个 LFS 文件并推送成功。
   - 托管项目：`oryn project create demo --hosted` → `oryn clone demo` → 本机推送 → Webhook 收到一次带签名的 POST；线程里点「提交并推送到 main」后线程变为已交付。
   - 项目密钥：`oryn secrets set FOO bar --project demo --plain` 后，新线程的 orb 里 `echo $FOO` 输出 `bar`，别的项目不受影响。
7. 文档：`docs/features.md`、`docs/deploy.md`、`docs/protocol.md`、`README.md` 与实现一致。

## Critical Files

阶段 ①：

- `web/src/pages/ProjectsPage.tsx`：要搬走的卡片、`latestSetupOrb`、`ServicesCard`、`EnvironmentCard` 的来源，步骤 1.9 删除。
- `web/src/pages/projects/ProjectShell.tsx`、`ProjectHomePage.tsx`、`settings/ProjectSettingsPage.tsx`：页面外框、主页、设置分组；阶段 ② ③ ④ 都往这三处加内容。
- `web/src/pages/projects/ProjectEditor.tsx`：按 `section` 拆分，加描述字段，移出门禁。
- `web/src/components/layout/ProjectsPane.tsx`、`ThreadsPane.tsx`、`web/src/state/ui.ts`：侧栏重写与过滤功能删除。
- `internal/api/projects.go`：描述字段的校验与映射；部分 PATCH 的语义依赖这里。

阶段 ②：

- `internal/repos/`（`repos.go`、`git.go`、`sync.go`、读接口与提交 → 线程查找）：裸镜像的存储、同步、读取，git 的受控执行环境；`removeRepoDir` 的正本保护与磁盘空间保护也在这里。
- `internal/egress/publicproxy.go`、`internal/egress/egress.go`、`internal/orbs/egress.go`：公网地址守卫、推送记录、LFS 接口放行。
- `internal/api/projectrepo.go`、`schema/openapi.yaml`：状态、同步与只读接口。
- `web/src/api/repo-queries.ts`、`web/src/pages/projects/home/`、`web/src/pages/projects/repo/`：查询、主页区块、仓库子页。
- `internal/lfs/lfs.go`、`internal/repos/lfsfetch.go`、`lfscache.go`：LFS 指针解析、batch 客户端、按主机选传输（守卫）、单飞、校验、LRU 缓存。
- `internal/egress/lfs.go`：LFS 接口的读写分类与放行、同源传输地址的改写与门票。
- `internal/orbdrt/setup.go`：clone / update 带 `GIT_LFS_SKIP_SMUDGE=1`，之后 `git lfs pull`，失败并入 `setupError`。

阶段 ③：

- `internal/repos/hosted.go`、`smarthttp.go`、`hostedpush.go`、`webhook.go`：建裸仓库、smart-HTTP、推送锁、推送后的记账与自动交付、Webhook 投递。
- `internal/egress/egress.go`、`internal/egress/hosted.go`：`resolveGit` 的托管分支与 `ServeHTTP` 里的本地提前返回；所有 `Resolver` 实现都要嵌入 `NoHostedGit`。
- `internal/orbs/setup.go`：`HostedCloneURL`、`hostedGitEntry`，以及 `plan()` 里托管项目的 `Repo` / `GitHosts` / `Policy.HostedGit`。
- `internal/api/projects.go`、`internal/api/gitserve.go`、`internal/auth/auth.go`：托管项目的创建与修改规则、`/git/` 路由、只认设备令牌的鉴权。
- `cmd/oryn/clone.go`、`gitcredential.go`、`project_push.go`：`oryn clone`、凭据助手、`--push`。
- `internal/repos/lfsserver.go`：托管仓库的 LFS 服务端（batch + 对象上传下载）、路径规则、401 的 `LFS-Authenticate` 头。

阶段 ④：

- `internal/store/migrations/0049_project_secrets.sql`、`internal/store/queries/blobs_secrets.sql`：表重建、部分唯一索引、带作用域的查询与快照戳。
- `internal/secrets/vault.go`、`secrets.go`：两种 AAD、项目级方法、`All` 已知密钥集合、`ValidProjectEntry`。
- `internal/orbs/setup.go`、`internal/orbs/egress.go`：`plan()` 的环境变量合并与 Git 主机并集、`GitToken` 的「项目 → 全局」优先级。
- `web/src/pages/projects/settings/SecretsTab.tsx`、`web/src/lib/dotenv.ts`：分组页、编辑与批量粘贴对话框、`.env` 解析。
