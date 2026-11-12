# Amp 执行环境(Orb 体系)调研报告

状态标记含义:`[现行]` 最新版仍存在;`[已移除]`;`[已被取代→新形态]`。依据日期取页面 `lastModified` 或公告日期。当前日期为 2026-11-12,下文"现行"均指截至此日可读到的版本。亲自读到的才算事实,推测标 `[推测]`,读不到的写「未找到」。

---

## 0. 大改版时间线

| 日期 | 事件 | 改了什么 / 删了什么 | 来源 |
|---|---|---|---|
| 2026-01-08~15 | 清理"编辑器时代"功能 | TODOs 删除(01-12:Opus 4.5 不需要)。Fork 命令删除(01-13,改用线程提及)。Amp Tab 删除(01-15,1 月底停服)。01-13 另有「Handoff, Please」:handoff 改成对 agent 说一句话触发。 | https://ampcode.com/news/todos-are-done · /news/stick-a-fork-in-it · /news/tab-tab-dead · /news/ask-to-handoff |
| 2026-01-29 | 自定义命令删除 | 全部迁到 skills,文件放 `.agents/skills/<name>/SKILL.md`。 | https://ampcode.com/news/slashing-custom-commands |
| 2026-02-19 | 「The Coding Agent Is Dead」 | 宣布杀掉 VS Code/Cursor 扩展(03-05 20:00 PT 自毁)。理由:模型已不需要 agent 外壳,瓶颈是代码库组织和团队流程。CLI 暂留,称其为"梯子"。 | https://ampcode.com/news/the-coding-agent-is-dead |
| 2026-05-06 | **Neo 重写**(CLI 全部重建) | 新增:可远程控制、compaction 优先(上下文 90% 时自动压缩)、Plugin API 公开、消息默认排队并可 steer、权限默认关闭。删除:handoff、编辑/恢复消息时的文件回滚、skill 管理子命令、用户可调用 skill、主题、`$`/`$$` 手动 bash。性能:CPU 少 79%,内存少 70%。 | https://ampcode.com/news/neo |
| 2026-05-14 / 05-27 | npm 包改名、「Drop the Neo」 | 包名由 `@sourcegraph/amp` 改为 `@ampcode/cli`。Neo 即 Amp,对所有人开放。05-27 同日上线 Proof of Human(passkey)。 | /news/npm-package-changes · /news/drop-the-neo · /news/proof-of-human(后两篇仅见于公告列表标题;Neo 页内有 passkey 更新备注) |
| 2026-06-02 / 06-04 | 公开线程终结、「Agents, Everywhere」 | 公开线程结束,保留 workspace 内共享。web/CLI/mobile 都能看和驱动 agent。 | 公告列表(正文本轮未读;早先已读 end-of-public-threads、agents-everywhere) |
| **2026-06-30** | **Agents in Orbs**(orb 体系诞生) | 每个新线程一台云端机器,按分钟计费,闲置自动睡眠。同时出现 `amp sync`、`amp -ox`、Files/Terminal/Changes 面板。 | https://ampcode.com/news/agents-in-orbs |
| 2026-07-02 / 07-03 | read_thread 重写、More Orb Sizes | `read_thread` 改为子 agent。orb 磁盘 20GB→40GB,可在项目设置选 CPU/内存。 | /news/read-bigger-threads · /news/more-orb-sizes |
| 2026-07-08 / 07-09 | Agents Anywhere(runner)、**The Dial** | 四档 low/medium/high/ultra 取代旧模式 smart/deep/rush/large(旧名标记弃用)。 | 公告列表标题;早先已读 agents-anywhere、the-dial |
| 2026-07-14 | **Secrets of the Orb**(OIDC) | orb 可用 OIDC 证明身份,不必注入长期密钥。 | https://ampcode.com/news/secrets-of-the-orb |
| 2026-07-20~23 | Puck、Slack、Schedule、Multiplayer、**Event Driven Orbs** | 元 agent Puck;自我唤醒;共享 orb;webhook 唤醒 orb。 | https://ampcode.com/news/event-driven-orbs(其余早先已读) |
| 2026-07-27 | orb 价格降 20% | 来自 Amp 官方推文,见 size-the-orbs 页引用。 | https://ampcode.com/news/size-the-orbs-of-production |
| **2026-08-06** | **Portals**(`.amp/services.yaml`) | 无需 VPN 和端口转发,直接访问 orb 内的 HTTP 服务。 | https://ampcode.com/news/portals |
| **2026-08-07** | **Size the Orbs of Production** | 尺寸体系 a0→a1;自动暂停 15 分钟→5 分钟;启动提速;可按线程选尺寸。 | https://ampcode.com/news/size-the-orbs-of-production |
| 2026-08-19~28 | MCP in Orbs、Explain Usage、Friendly URLs、Setup Without a Commit、Multi-repo、No Mailmap、iOS/macOS 应用 | 见第 2 节。 | 见第 2 节 |
| 2026-08-27 | 删除 TUI 侧边栏 | 早先已读 so-long-tui-sidebar。 | 公告列表 |
| 2026-08-31~09-04 | Space、**Desktop** | orb 内有可交互的 Linux 桌面。 | 早先已读 |
| 2026-09-08~24 | Steer Don't Queue;runner 系列(一个 runner 多目录、worktree、Mac 应用即 runner、共享 runner) | **推翻**了 Neo 的"默认排队":现在发送即 steer。 | 早先已读 |
| 2026-10-22 | **Self-Hosted Orbs** | 自备算力,`amp orb provider` 派发 spawn/pause/delete。 | https://ampcode.com/news/self-hosted-orbs |
| 2026-10-28 | Personal self-hosted | 个人无需 workspace 即可添加 provider。 | https://ampcode.com/news/personal-self-hosted-orbs |
| 2026-10-29 | **macOS Satellites** | **推翻 Morbs**:不再让整台 orb 跑在 Mac 上,改为 Linux orb 加一台按需启动的 Mac。 | https://ampcode.com/news/macos-satellites |
| 2026-11-02~06 | Artifacts、Service Accounts、GitLab、**Spaces**、Close the Loop | Spaces 是更高层的多 agent/多人工作方式。 | /news/service-accounts · /news/gitlab(其余早先已读) |
| 2026-11-09 | Runner Portals | portal 扩展到本地 runner。 | 早先已读 |
| **2026-11-10** | **Boxes** | 永久在线的小型 VM,每个应用独立 `onamp.dev` URL,自带 runner。 | https://ampcode.com/news/boxes |

---

## 0.1 「老版功能,不要复刻」清单

| 老功能 | 状态与依据 |
|---|---|
| Handoff / 压缩命令 | `[已移除]` 2026-05-06,compaction 取代。一个月前(01-13)刚加的"对话触发 handoff"也被一并废除。https://ampcode.com/news/neo |
| Fork 线程 | `[已移除]` 2026-01-13,改为"新线程加 URL 提及"。https://ampcode.com/news/stick-a-fork-in-it |
| TODO 列表 | `[已移除]` 2026-01-12。https://ampcode.com/news/todos-are-done |
| Amp Tab(补全)、VS Code/Cursor 扩展 | `[已移除]` 2026-01-15 / 2026-03-05。 |
| 自定义命令 | `[已被取代→skills]` 2026-01-29。 |
| 旧权限系统(弹窗审批) | `[已被取代→内置插件]` 2026-05-06。默认全放行;只有配置过 `amp.permissions`/`amp.dangerouslyAllowAll:false`/`amp.guardedFiles.allowlist` 才加载。 |
| 编辑或恢复消息时回滚文件 | `[已移除]` 2026-05-06。 |
| CLI 主题、`$`/`$$` 手动 bash、skill 管理子命令、用户可调用 skill | `[已移除]` 2026-05-06。 |
| TUI 侧边栏 | `[已移除]` 2026-08-27。 |
| 旧模式名 smart/deep/rush/large | `[已被取代→Dial]` 2026-07-09。 |
| 公开线程 | `[已移除]` 2026-06-02。 |
| `@sourcegraph/amp` 包名 | `[已被取代→@ampcode/cli]` 2026-05-14。 |
| Morbs(整台 orb 跑在 Mac 上) | `[已被取代→macOS Satellites]` 2026-10-29。 |
| 旧 orb 尺寸 `a0.*`、`manual/orbs#pricing` 旧链接 | `[已被取代→a1.*]` 2026-08-07。 |
| orb 内的 `AMP_API_KEY` | `[已被取代→线程身份]` 文档称"Amp is removing"(2026-10-16)。 |
| 15 分钟空闲暂停 | `[已被取代]` 现为 5 分钟 + 20 分钟(见 3.1)。 |
| 手动 @ 文件提及 | `[推测:新 UI 去除]` 依据:Thorsten 讲座称"web/macOS/iOS 应用已不再有 @ 提及"。https://ampcode.com/what-are-orbs |
| 消息默认排队 | `[已被取代→steer]` 2026-09-08 取代 Neo 的默认排队。 |

**新旧冲突点(以最新为准):**
1. 安全页写"Amp doesn't offer self-hosted deployments",而 2026-10-22 起有自托管 orb。该句应是过时措辞。以 self-hosted 文档(2026-11-11)为准。https://ampcode.com/security
2. 自托管定价:公告(10-22)写"Enterprise 自 10-29 起收费",文档(2026-11-11)写"所有 workspace 免费至 11 月 13 日,之后 Enterprise 付 100% 标准价"。以文档为准。
3. orb 磁盘:07-03 公告写 40GB,sizes 文档(11-03)写所有当前尺寸 60GB。以文档为准。
4. 默认尺寸:sizes 文档写项目默认 `a1.medium`,Megawatt 订阅者的个人项目默认 `a1.small`。8-07 公告也称 a1.medium 是多数项目的更好选择。

**截图和视频:** 本轮读到的文档页大多附带明/暗双版图(`-light`/`-dark`),都是最新 UI。以下三类素材为旧版,不能作规格:
- Neo 公告(2026-05)里的 TUI 视频。
- `agents-in-orbs`(06-30)的演示视频——orb 初代 UI。
- 含 TUI 侧边栏的任何图。

文档页里同一批 `agents-in-orbs-demo-*.mp4` 仍在 `docs/orbs` 中引用。[推测] 它们只是展示流程,界面细节以 `-light/-dark.avif` 新图为准。

---

## 1. Orb 体系演进(含被推翻的设计与理由)

1. **2026-06-30 初代 orb** `[现行,细节已多次改]`
   - 每线程一台机器,按分钟计费,"start up quick and go to sleep"。
   - 进程、存储、限制:见 `agents-in-orbs` 一节。
   - 设计理由:"我们把模型限制在单机上就是在拖它们后腿"。https://ampcode.com/news/agents-in-orbs
2. **07-03** 开放选尺寸,磁盘 20GB→40GB。 https://ampcode.com/news/more-orb-sizes
3. **07-14** 引入 OIDC,理由是不注入长期密钥即可做运维任务。 https://ampcode.com/news/secrets-of-the-orb
4. **07-23** 事件驱动:webhook 唤醒休眠 orb。 https://ampcode.com/news/event-driven-orbs
5. **08-07** 定价和行为调整:
   - 新增 `a1.medium`(4C/8G),比 `a0.medium` 便宜 50%。
   - 自动暂停 15→5 分钟("闲置不收钱")。
   - 启动加速,尤其是"同项目中别人刚建过 orb"时(对应项目快照共享)。
   - 可按线程和子 agent 选尺寸。
   - 7-27 全局降价 20%。
   - 理由:订阅额度要覆盖"几乎所有人整月的 orb 用量"。https://ampcode.com/news/size-the-orbs-of-production
6. **08-06 Portals**,**08-23 友好 URL** `[现行]` https://ampcode.com/news/portals
7. **08-25 Setup without a commit**:pre-clone/pre-setup 脚本存在项目设置里,不进仓库。 `[现行]`(文档 customizing 2026-11-10)
8. **08-27 多仓库项目**:最多 20 个附加仓库,检出到相邻目录,只跑主仓库的 `.agents/setup`。 https://ampcode.com/news/multi-repo-projects(文档现写 `../repos/`)
9. **09-04 Desktop** `[现行]`
10. **10-22 自托管**,**10-28 个人 provider** `[现行]`
11. **10-29 macOS Satellites** `[现行,beta]`
    - **被推翻的设计 Morbs:** Docker 和后端在 Mac 上要几分钟才起来,而且每线程保留一块 Mac 磁盘太贵。
    - **新形态:** 线程仍在 Linux orb 上,Mac 是挂在 orb 旁的第二台机器,"围绕 orb 公转"。https://ampcode.com/news/macos-satellites
12. **11-10 Boxes**:开发在 orb、部署到 box。 https://ampcode.com/news/boxes

**行为变化小结(同一机制的新旧对照):**

| 项 | 旧 | 新 |
|---|---|---|
| 空闲暂停 | 15 分钟 | "agent 空闲 5 分钟 AND 用户 20 分钟无交互,取较晚者" |
| 磁盘 | 20GB→40GB | 60GB |
| orb 内凭据 | 账户 `AMP_API_KEY` | 线程身份(短期自动轮换) |
| 消息 | 排队 | steer |

---

## 2. 逐篇要点(本轮新读)

### agents-in-orbs(2026-06-30)`[现行]`
- 功能:启动远程 agent。orb 内含代码、插件和工具,闲置自动睡眠,按分钟计费。
- 用户可见:可看改动、浏览文件、开终端;`amp sync <thread>` 同步到本地;`amp -ox "..."` 在 orb 里起线程;TUI 里可新建 orb 线程。
- 理由:无本地资源冲突,可以并发起很多个 agent。
- 坑:未写。
- 来源:https://ampcode.com/news/agents-in-orbs

### secrets-of-the-orb(2026-07-14)`[现行]`
- 功能:`amp orb id-token --audience <aud>` 铸造 OIDC JWT(RS256)。
- 声明:除 `iss/aud/sub/exp/iat/jti` 外,有 `workspace_id`、`project_id`、`user_id`、`thread_id`、`email`、`email_verified`、`token_use=exchanged`。
- 发行方:`https://ampcode.com/api/workload-identity`;`sub` 形如 `workspace:<id>:project:<id>:user:<id>:thread:<id>`。
- 用法示例:
  - GCP WIF 读生产日志。
  - Tailscale trust credentials。
  - SOPS + KMS 解密开发密钥。
  - PAM 提权。
  - oauth2-proxy。
- 审计:操作可审计(含用户 ID 和线程 ID),令牌短期且范围窄。
- 来源:https://ampcode.com/news/secrets-of-the-orb

### more-orb-sizes(2026-07-03)`[已被取代]`
- 可在项目设置里选 CPU 和内存。
- 磁盘 20→40GB,免费。
- 价格页链到旧 `manual/orbs#pricing`。
- 来源:https://ampcode.com/news/more-orb-sizes

### size-the-orbs-of-production(2026-08-07)`[现行]`
- 尺寸表:

| 尺寸 | CPU | 内存 | 价格 |
|---|---|---|---|
| `a1.tiny` | 1 | 2GB | $0.08/h |
| `a1.small` | 2 | 4GB | $0.17/h |
| `a1.medium` | 4 | 8GB | $0.33/h |
| `a1.large` | 8 | 16GB | $0.66/h |
| `a1.xxlarge` | 16 | 32GB | $1.32/h |

- 按线程选尺寸;`amp -ox --orb-size <size>`;子 agent 派发时可指定更小尺寸做 fan-out。
- 项目自动升级到等价的新尺寸。
- 来源:https://ampcode.com/news/size-the-orbs-of-production

### portals(2026-08-06)`[现行]`
- 功能:访问 orb 内任何监听端口并说 HTTP 的服务。
- 触发:对 Amp 说 "show me in a portal"。agent 创建或查找 `.amp/services.yaml`,然后运行 `amp orb services <ensure|start>`;应用必须尊重 `PORT` 和 `PUBLIC_URL`。
- 访问:有线程访问权的人都能开;随 orb 睡眠和唤醒;地址栏齿轮可改主机名或看谁有权访问。线程转 multiplayer 后,队友的修改也实时可见。
- 登录:推荐 dev 魔法链接 `__dev/log-me-in/{email}?returnTo=`。
- 来源:https://ampcode.com/news/portals · 现行细则见 docs/orbs/portals(2026-11-10)

### event-driven-orbs(2026-07-23)`[现行]`
- 机制:通过 Plugin API `amp.createWebhook` 为线程注册持久端点;请求到达时 Amp 先存储事件再唤醒 orb。
- 处理器:可以追加到所属线程 `ctx.thread`,也可以 `amp.getBuiltinAgent(...).createThread({executor:'orb'})` 起新 orb 线程。
- 安全:Amp 验证 GitHub 签名并按投递去重,生成的线程默认只读,事件元数据可信、issue 正文不可信。webhook URL 本身就是凭据。
- 来源:https://ampcode.com/news/event-driven-orbs

### macos-satellites(2026-10-29)`[现行,beta + 等候名单]`
- 机制:服务端按需启动全新 Mac(约 30 秒),带 Xcode 和模拟器。orb 把改动同步过去;Mac 只负责构建、运行、测试。Git 忽略的文件(如 `.env`)留在 orb。Mac 上的 app 通过 portals 访问 orb 上的后端。
- 生命周期:每次都是全新 Mac,不保存任何东西。用完保留 20 分钟(复用构建缓存),之后删除;归档线程或让 Amp 停止则立即删除。
- 规格和价格:`mac.medium` 6C/14G $2/h;`mac.large` 12C/28G $4/h,从同一份 orb 额度扣。
- 镜像:`macos-27`/`macos-26`/`macos-15` 带 Xcode 和模拟器版本;`.xcode-version` 可钉版本。
- 限制:Mac 是无嵌套虚拟化的 VM,不能跑 Android 模拟器。
- 签名:App Store Connect API key,安装前询问,Mac 停止后凭据删除。
- 用量:Settings → Usage → Satellites。
- 来源:https://ampcode.com/news/macos-satellites · https://ampcode.com/docs/markdown/orbs/mac(2026-11-09)

### self-hosted-orbs(2026-10-22)`[现行]`
- 核心:`amp orb provider ./dispatch.sh` 向外连接 ampcode.com,收到 spawn/pause/delete 作业后调用你的程序。**无需入站端口。**
- 要求的底层能力:快速启动、同样快速休眠和恢复、长期保留、可缩到零。
- 官方给出的示例:Docker、E2B、K8s(Helm)、Apple containers、Modal、Vercel、Daytona、exe.dev。
- 官方态度:"You probably shouldn't self-host orbs"——只为受监管、需要内网或需要定制硬件的团队而做。
- 定价:见上方冲突点 2。
- 来源:https://ampcode.com/news/self-hosted-orbs · 契约见 docs/orbs/self-hosted(2026-11-11)

### personal-self-hosted-orbs(2026-10-28)`[现行]`
- 个人在 Settings → Orbs 加 provider,无需 workspace 和管理员。
- 线程创建时显示在 Self-Hosted 下并标记 Personal。
- 来源:https://ampcode.com/news/personal-self-hosted-orbs

### mcp-in-orbs(2026-08-19)`[现行]`
- 在 ampcode.com/settings/mcp-servers 连接远程 MCP,orb、TUI、runner 和 Puck 都可使用。
- 仅支持 Streamable HTTP,认证用 OAuth 或 Bearer。
- 个人和 workspace 两级配置。
- **不支持** MCP Apps、Resources、Prompts。
- 来源:https://ampcode.com/news/mcp-in-orbs

### explain-usage(2026-08-21)`[现行]`
- 可问 Puck:哪些线程消耗最多 token / orb 时间;最贵线程用了什么模型;能否缩小 orb 尺寸;"精确到毫秒的 orb 时间"。
- 数据来源:个人用量页 `/settings/usage` 和按线程的用量;页面上有 **Explain Usage** 按钮。
- CLI:`amp usage --details`、`amp threads usage <thread-id> --details`;可以在 portal 里做可视化。
- 来源:https://ampcode.com/news/explain-usage

### multi-repo-projects(2026-08-27)`[现行]`
- 项目最多带 20 个附加仓库,检出到相邻目录并告知 agent。
- Changes 面板显示跨仓库 diff。
- 坑:只有主仓库的 `.agents/setup` 自动执行,附加仓库的初始化要写进主仓库的 setup。
- 文档(2026-11-10)写路径为 `../repos/`。
- 来源:https://ampcode.com/news/multi-repo-projects

### no-mailmap-required(2026-08-28)`[现行]`
- Git 身份与 Amp 账户邮箱分离,在 Signing Keys → Git identities 管理。
- 启动 orb 时设置 `GIT_AUTHOR_*` 和 `GIT_COMMITTER_*`;开启提交签名时,只为已验证邮箱签名。
- workspace 管理员三选一:**Amp** / **Amp Account** / **User Choice**。个人默认值可被项目覆盖。
- 邮箱验证通过 Puck 发链接完成。
- 来源:https://ampcode.com/news/no-mailmap-required

### service-accounts(2026-11-03)`[现行]`
- workspace 所有、不绑定个人的账户,有自己的 API key 和第三方凭据。
- 其线程对 workspace 成员可见且是 multiplayer。
- 坑:**只给它分配你愿意授予所有成员的凭据**。
- 用法:`AMP_API_KEY=<SA key> amp -ox ...`(CI 里起线程);Puck 在用户说"workspace 所有"时自动选用。
- 来源:https://ampcode.com/news/service-accounts

### boxes(2026-11-10)`[现行]`
- 概念:永久在线的 VM,可多次部署多个应用,每个应用有 `onamp.dev` URL;**无容器或其他抽象**。
- 访问:可设为私有或要求 Sign In With Amp;自带 runner,可在 box 上直接起线程操作线上数据;可与 workspace 共享。
- 规格和价格:

| 规格 | 配置 | 价格 |
|---|---|---|
| `b1.tiny` | 0.5 vCPU(可突发到 2)/2GB | $0.025/h,约 $18/30 天 |
| `b1.small`(默认) | 1 vCPU(可突发到 2)/4GB | $0.042/h |
| `b1.medium` | 2C/8G | $0.069/h |
| `b1.large` | 4C/16G | $0.139/h |

- 按分钟计费,可设定定时开关机。
- 来源:https://ampcode.com/news/boxes

### gitlab(2026-11-05)`[现行]`
- 支持 gitlab.com 和自托管实例。orb 以你的身份 clone 和 push,`glab` 已登录。
- 订阅 MR 评论和 CI;可为每个新 MR 或 issue 创建 **orb spawner**。
- 在 Settings → Integrations & MCP → GitLab → Event Subscriptions 管理;workspace admin 可用 service account 跑 spawner。
- Push Updates 由 group Owner 一次性安装。
- 来源:https://ampcode.com/news/gitlab

### read-bigger-threads(2026-07-02)`[现行]`
- `read_thread` 改为子 agent,先搜索、再读、再核对是否被后续修改撤销("工具调用记录的是尝试,不是结果")。
- 背景:最长线程已被压缩 68 次以上,否则超过 2100 万 token。
- 模型从 Gemini 3.5 Flash 换成 GLM 5.2。
- 来源:https://ampcode.com/news/read-bigger-threads

### the-coding-agent-is-dead(2026-02-19)`[现行,战略背景]`
- 理由:新模型让 agent 外壳不再是瓶颈,"如何组织代码库、组织如何使用"才是。
- 要点:杀编辑器扩展,CLI 是"梯子","Amp 不再是一个点,而是一支箭"。
- 来源:https://ampcode.com/news/the-coding-agent-is-dead

### neo(2026-05-06)`[现行]`
- 内容:见时间线;另有 Plugin API 能力清单。
- 新旧对照:此文 Update 注明 `ask_user_choice` 在 2026-09 变成内置工具。
- 来源:https://ampcode.com/news/neo

### ask-to-handoff / stick-a-fork-in-it / slashing-custom-commands / todos-are-done / tab-tab-dead
- 这些都是"已移除"项的演进说明,见 0.1 清单。
- `stick-a-fork-in-it` 页面的 Update 行日期写作 "2025-05-06"([推测] 应为 2026-05-06,页面原文疑似笔误):handoff 在四个月后被删除。
- 来源见 0.1。

### 笔记类
**what-are-orbs(营销页,2026-10-26)** `[现行]`
- 六个卖点:一个 prompt 一个 orb;orb 无限、从 tiny 到 xxlarge;agent 间互相通信;orb "会变形"(只有 diff 离开);富 diff 审查、实时 portal、multiplayer;Amp 负责 spawn/pause/wake/计费("暂停的 orb 不花钱")。
- 内含 Thorsten 的 34 分钟 time capsule(2026-10),要点:全员 all-in orbs、"no local dev"、push to main 无 PR、让 agent 给出"irrefutable proof"、codebase 要对 agent 可读。
- 他们的代码库是 2M+ 行 TypeScript 单仓库,15 个 AGENTS.md。
- 他提到 2026 年初"遇到 Cloudflare 和 Durable Objects 的问题,切到别的平台后重发",是对重写原因的第一手说明。
- 用 orbs 评估 agent 友好度:起 5~10 个 orb 做对照实验。
- 来源:https://ampcode.com/what-are-orbs

**what-i-want-to-tell-you-about-orbs(2026-08-04)**
- 叙事文,无技术细节。
- 论点:摩擦消失 → 起更多 agent;让 agent 跑更久并给出证据("100% proof")。
- 例子:在无头沙箱里运行 Rust 桌面应用并返回演示视频。
- 来源:https://ampcode.com/notes/what-i-want-to-tell-you-about-orbs

**pave-the-road(2026-08-10)**
- 数据:orb 上线一个月后提交速度 +65%,85% 以上的提交来自 orbs。
- 论点:把 SDLC 从"预防错误"转为"错误便宜"——隔离优于审批、可回退优于细审、并行优于排队。
- 来源:https://ampcode.com/notes/pave-the-road

**thats-not-soc-2-compliant(2026-08-13)**
- SOC 2 不要求 PR。他们的控制措施:
  1. 限制 push 权限。
  2. 签名提交,GitHub 强制 main 上必须验证签名。
  3. 自动化 CI,坏改动阻断 main。
  4. 审计轨迹:提交链接回产生它的 Amp 线程。
- 不要求"第二个人看 diff"。
- 来源:https://ampcode.com/notes/thats-not-soc-2-compliant

**security 页(`lastModified 2026-11-10`)** `[现行]`
- 详见第 5 节。
- 来源:https://ampcode.com/security

### 其他读到的页面
- **docs/markdown/egress-ips**(2026-11-04):16 个 IPv4 出口地址;列出 Amp 服务器何时会主动连你的服务,以及何时不会(见 5.4)。
- **morbs 页**(2026-10-05):仅为 Mac satellites beta 的落地页,无技术内容;标题 "It's Morbin' Time" 与 Morbs 已被取代的事实并存,以 macos-satellites 公告为准。

---

## 3. orb 启动速度相关(完整汇总)

### 3.1 当前机制 `[现行]`(customizing 2026-11-10、optimizing 2026-11-09)
- **项目快照**:已准备好的 orb 的保存副本,内含仓库和 `.agents/setup` 的结果。每个项目、每个尺寸一份。
- **启动流程:**
  1. 查找该尺寸的项目快照。
  2. 若快照与主仓库 base 分支最新提交、附加仓库默认分支最新提交以及项目共享配置都匹配,则直接恢复并**跳过 `.agents/setup`**。"共享配置"指 workspace 和项目的环境变量与密钥、pre-clone 和 pre-setup 脚本。
  3. 无匹配快照:从基础或旧快照起,执行 pre-clone → clone 或更新 → pre-setup → `.agents/setup`。
  4. setup 成功后保存为新的项目快照。
  5. 应用线程环境和 workload identity。
  6. 执行 `.agents/resume` 后启动 agent。
- **何时过期:** base 分支出现新提交,或共享配置变了。刷新时先恢复旧快照,再在其上重跑 setup,所以 **setup 必须幂等**。
- **后台刷新:** workspace 项目通常在 push 后于后台刷新。
- **72 小时上限:** 建立在同一基础镜像上的快照最多复用 72 小时,之后重新从基础镜像开始(新 orb 镜像版本也在此时到达项目)。刷新不重置计时。安全页同样写"至多 72 小时"。
- **共享快照例外:** 非 GitHub/GitLab 的仓库、设了 Base Branch 的 Amp 托管仓库,或无法读取最新提交时,**每个成员各有快照且每次都要跑 setup**——启动更慢,并且 setup 用成员自己的环境变量和密钥。
- **手动清除:** `amp projects snapshots list <project>`、`amp projects snapshots delete <project> --resource a1.small`。
- **预热(prewarm):** 文档只在 Git History 一节顺带提到:"Project snapshots and prewarmed orbs switch to the new setting the next time Amp refreshes them"。**没有任何独立的预热池页面,其容量和策略「未找到」**。启动提速的内部机制(8-07 公告)也没有细节。
- **浅克隆:** 完整历史是默认,`git log/blame/merge-base` 开箱即用。跳过大于 1MB 的文件版本,命令需要时才拉取。**clone 超过 15 分钟 orb 启动失败。** 大仓库可在项目设置 Orb → Advanced 打开 **Shallow Clone**(只克隆最近一次提交)。
- **setup 耗时:**
  - 硬上限 20 分钟(含 pre-setup);超时仍启动 orb,但不发布新快照。
  - 失败同样不发布快照,导致后续新 orb 每次重跑 setup。
  - 建议控制在约 2 分钟内;近几次 setup 达到 2 分钟以上时,**线程里会显示警告**。
  - 建议并行化独立步骤、打印每步耗时,日志在 `/home/user/.cache/amp/logs/setup.log`。
- **setup 里不要留进程:** Amp 会杀掉脚本退出后残留的所有进程(含 nohup/setsid/守护进程)。数据库用 `sudo systemctl enable --now <unit>`,让 systemd 持有;dev server 放 `.amp/services.yaml`。
- **`.agents/resume`:** orb 首次激活和每次唤醒都跑,Amp 最多等 10 秒,之后让 agent 继续;日志 `resume.log`。
- **唤醒:** Amp-hosted orb 暂停时保存磁盘和**内存**,进程继续运行。
- **orb spawn pacing**(docs/orbs,2026-10-27):每个用户可一次起 20 个计费 orb 的突发;之后每 5 分钟起 1 个。后续启动**排队等待而不是失败**,线程显示预计等待;需要更高限额要联系支持。
- **镜像:** Debian 12/13。预装:amp、gh、glab、Git、tmux、Bun/Node/pnpm/Yarn、Python/uv、Nix、Docker Engine(daemon 默认不启动)、agent-browser + Chrome for Testing、ffmpeg、ImageMagick 等。
- **Amp 托管 orb 的底层:** 安全页列出 e2b 为 "ephemeral compute instances for Amp orbs",看得到代码数据;Archil 说明页写"默认 E2B orbs 运行在 GCP us-west1"。
- **自托管侧(对应做法):**
  - `ORB_SNAPSHOT_ID` 约定:在 `pause` 作业时保存、在 `spawn` 时用来启动。
  - 忽略它则每个线程都从镜像起并跑 setup。
  - Docker、Apple container、K8s Helm 支持项目快照;E2B、Modal 等示例未接。
  - K8s chart 的 `workers.types[].warm` 就是"预热空闲 worker 数",`min` 为常驻数,使下一个 orb 无需等待 pod。
- **macOS Satellite 启动:** 约 30 秒;空闲保留 20 分钟复用构建缓存。
- 来源:https://ampcode.com/docs/markdown/orbs/customizing · https://ampcode.com/docs/markdown/orbs/optimizing · https://ampcode.com/docs/markdown/orbs · https://ampcode.com/docs/markdown/orbs/self-hosted · https://ampcode.com/docs/markdown/orbs/self-hosted/kubernetes · https://ampcode.com/security

### 3.2 历史
- 初版:15 分钟空闲暂停。2026-08-07 起改 5 分钟,并"大幅提速启动,尤其同项目他人刚创建过 orb 时"(即共享项目快照)。
- 磁盘 20GB→40GB→60GB。

---

## 4. orb 可观测性与用量统计

| 项 | 内容 | 来源 / 日期 |
|---|---|---|
| 计费模型 | 按分钟计费;暂停不收费;唤醒时到再次暂停之间计费。 | sizes-and-costs 2026-11-03 |
| 暂停语义 | "agent 空闲 5 分钟 **且** 用户交互后 20 分钟,取较晚者"。交互指发消息、使用 Files/Terminal/Desktop,以及**任何通过授权的 portal HTTP 请求**。Puck 或 automation 发来的消息算 agent 工作,不开启 20 分钟窗口。 | 同上 |
| 保活 | 其他 agent 的回传、automation 和 webhook、插件 `amp.system.executor.keepAlive()` 租约、排队中的消息。**Dev server 本身不保活。** | 同上 |
| 省钱开关 | **Orb Saver Mode**(Settings → Orbs):仅打开线程不唤醒 orb。 | 同上 |
| 线程菜单 | 显示 orb 运行或暂停,并复述超时规则。 | 同上 |
| 个人用量 | `/settings/usage`、按线程用量;**Explain Usage** 按钮;Puck 回答。 | explain-usage 2026-08-21 |
| CLI | `amp usage --details`、`amp threads usage <id> --details`(含毫秒级 orb 时间)。 | 同上 |
| Satellite 用量 | Settings → Usage → Satellites,显示 Mac 运行时长与费用。 | mac 2026-11-09 |
| 自托管用量 | 线程用量显示每个 orb 的运行时长和尺寸,即使费用是 $0。按其上报的 CPU 和内存按分钟计,只计线程的 orb。自定义尺寸价格:CPU×$0.0504/h + GB×$0.0162/h。 | self-hosted 2026-11-11 |
| 内存告警 | Thorsten 的案例:收到"orb 内存不足"报告后,让 agent 实现"报告占内存前 5 的进程并在警告里展示",并读 cgroup。**该功能是否已发布未确认。** | what-are-orbs 讲座文字稿 |
| 项目配置 | `amp projects` 子命令可改尺寸、Ship 行为等。 | size-the-orbs / shipping |
| 日志 | hook 日志:`~/.cache/amp/logs/{setup,resume}.log`;服务日志 `amp orb service logs <name>`;自托管看 provider 日志和线程 setup log;K8s 看 provider 和 spawner 两个容器的日志。 | customizing / k8s |
| Workspace 分析 | 安全页提到"数据管理 API:workspace 分析和线程数据"。具体接口「未找到」(docs/api/external 本轮未读)。 | security |

**「未找到」:** 系统级指标(CPU、内存、磁盘曲线)的 UI,以及按 orb 的使用统计导出。

---

## 5. 安全模型

### 5.1 orb 内凭据与线程身份 `[现行]`
- orb 里的 `amp` CLI 用**线程身份**登录:短期凭据、自动轮换;`amp threads`、`amp -x`、`amp orb id-token` 无需配置。
- Amp 托管仓库的 Git 操作用只写 Git 的令牌。
- 账户 `AMP_API_KEY` 正从 orb 环境中移除。需要时自己放到个人密钥里(另起名字),`AMP_API_KEY` 本身是保留名。
- GitHub/GitLab 仓库无需在 orb 里配置 Git:Amp 用你的连接 clone、fetch、push,`gh`/`glab` 已登录。
- 其他 HTTPS Git 主机:用 `GIT_CONFIG_COUNT/KEY_0/VALUE_0` 的 `url.<…>.insteadOf` 技巧把令牌放进项目密钥,KEY_0 标记为 secret。
- **工作流身份:** pre-clone、pre-setup 和 setup 阶段**没有**个人 GitHub 凭据,也没有用户或线程的 workload identity;只能铸造 project 或 workspace 范围的 OIDC。请求凭据放在 `/run/amp/workload-identity-request-token`,保存快照前删除;`.agents/resume` 阶段才拿到新的运行时凭据。
- **脚本是受信任代码:** Amp 删除请求凭据,但不会找出脚本复制到别处的令牌,所以不要把令牌或登录缓存写进快照。
- 来源:https://ampcode.com/docs/markdown/orbs/handling-secrets(2026-10-16) · customizing(2026-11-10)

### 5.2 OIDC 细则
- 发行方 `https://ampcode.com/api/workload-identity`,另有 discovery 和 JWKS 端点。
- 默认 TTL 10 分钟,可设 60 秒到 1 小时。
- `--subject-scope` 取 `thread`(默认)/`user`/`project`/`workspace`,范围越大,`sub` 里越窄的层越被去掉。
- 信任策略应匹配**不可变 ID**(不要用 email);推荐同时要求 `token_use=exchanged`。
- AWS IAM 忽略自定义 claim,只能靠 `aud` 和 `sub` 限制。
- 项目所有权转移会改变 `sub`;每次铸造 project 范围令牌时 Amp 会检查当前访问权。
- 来源:handling-secrets(同上)

### 5.3 密钥与遮蔽
- 三级:workspace、项目、个人;优先级个人 > 项目 > workspace。
- 标记为 secret 后设置页隐藏其值;**orb shell 输出里每次出现该值都会被遮蔽**。
- 短值或常见词(`file`、`true`)会导致大面积误遮蔽,设置页保存时会警告。
- 密钥历史:`amp secrets history` 显示谁创建和修改,不显示值。
- Amp 需要用户已有的密钥时,弹私密对话框,值**不进入线程**;Amp 自己生成的随机值(如 webhook 签名密钥)直接存储。
- 刷新运行中的 orb 用 `amp orb restart-processes`。
- **全局遮蔽(安全页,Secret Redaction):** 在最底层检测并替换为 `[REDACTED:amp]`,覆盖 AWS/GCP/Azure、GitHub/GitLab/Amp 令牌、LLM key、Stripe/Slack/npm 等。
  - 局限:"尽力而为",对非标准格式和编码的密钥无效。
  - 补救:编辑前一条消息重发(覆盖 ampcode.com 上的内容);标私密只阻止共享;建议轮换。
- 泄露的 Amp 令牌可用 `POST /api/revoke` 撤销。
- 来源:handling-secrets · https://ampcode.com/security

### 5.4 portal 与 webhook 认证
- **默认访问:** 只有能看到该线程的人能开该线程的 portal。multiplayer 参与者在其访问有效期内可用。
- **公开访问:** 线程所有者可设 1 小时/3 小时/1 天/7 天;只对该端口生效,不授予线程访问权。
  - agent 通过 `manage_amp`(topic `portals`)操作时,公开或延长公开要弹窗批准。
  - CLI **不能**改 portal 访问;`amp orb portal list` 可看哪些已公开。
  - 工作区开关"Allow Users to Share Threads and Portals Outside the Workspace"优先。
- **自托管 portal:** 默认只对已登录且有线程权限的 Amp 用户开放;"公开 portal 相当于通向你内网的匿名隧道",所以需 provider 单独打开 **Public Portals**,关闭则一次终止该 provider 全部公开。
- **身份头(转发给应用):** `X-Amp-Authenticated: amp-user=yes|no, workspace-member=yes|no, collaborator=yes|no`,以及 `X-Amp-User-ID`、`X-Amp-User-Display-Name`、`X-Amp-User-Email`、`X-Amp-User-URL`(非 ASCII 值做 percent-encoding)。
- **owning orb 内**访问自己的 portal URL 绕过 Amp 的 portal 认证(但不绕过应用自己的认证)。
- **WebSocket 限制:** 经过 Amp 服务器,部署时会断开(close code 1006),需重连。**不能用于必须长连的场景(如数据库隧道)**,私网请用 Tailscale。
- **CORS 限制:** 两个 portal host 之间的浏览器请求不受支持(预检请求不带认证)。
- **自定义域名:** 个人和 workspace 的 Domains 页添加域名、按提示配 DNS、等待 Active。workspace 可设"Make Default for Portals"(形如 `t-<thread>-p<port>.portal.example.com`)。`amp orb portal <port> --hostname <h>` 和 `--take-over`。
- **Boxes 域名:** `amp boxes deploy --domain <hostname> --take-over`,portal 不能从 box 抢回主机名。
- **Review widget:** 默认开启,`review: false` 或响应头 `x-amp-review-widget: off` 关闭;评论携带选中文本、页面位置和(可得时)源码位置。runner portal 没有 review 按钮且不能公开。
- **Webhook:** URL 即密码;Amp **不**校验第三方签名,要在 `headers` 里声明需要的头并由 handler 验签;未声明的头被丢弃;至少一次投递,需按 `event.id` 去重;处理器 30 秒;失败退避 5 秒起翻倍至 5 分钟;持续失败 1 小时后丢弃;限速为突发 10 个、每分钟补 10 个,队列满 100 返回 429;请求体最大 1MB。归档拥有者线程会使 URL 404。
- 来源:https://ampcode.com/docs/markdown/orbs/portals(2026-11-10) · automations(2026-11-02) · self-hosted(2026-11-11)

### 5.5 签名提交与审计
- Git 身份与 Amp 账户解耦(见 no-mailmap-required),只为已验证邮箱签名。
- 自托管镜像:Git ≥ 2.34 才能签名。
- Amp 自己的做法(SOC 2 页):commits 链接到产生它们的线程。
- 安全页:线程存储保留 prompt、响应、工具调用、结果和附件,作为提示注入的审计轨迹。
- 应用审计日志对 Enterprise 按需提供,至少保留 30 天。

### 5.6 profiles(浏览器配置)与 passkey `[现行,需在实验功能中开启]`(profiles 2026-11-06)
- **保存内容:** orb Desktop 中 Chrome 的登录态、保存的密码、站点数据(local storage 等),可有多个命名 profile(3~32 位字母、数字、连字符)。每个 orb 只能读写自己线程所选的 profile。
- **同步:** 登录和 local storage 几秒内同步到其他使用同一 profile 的 orb;其余数据每 2 分钟和切换时保存。
- **冲突规则:** 同一站点的登录冲突先到先得,但**退出登录总是赢**;local storage 冲突除同一条目外都保留。
- **选择与限制:**
  - 新线程可选 profile;`amp -ox --profile`;`amp profile use <name>` / `amp profile none`。
  - agent 自主选择须在 Settings → Profiles 里打开 "Allow Agent to Select"。
  - 只支持 Linux orb,macOS 不支持。
  - 使用了 profile 的线程**不能**设为 multiplayer(任何能用该线程的人都能控制 Chrome 并使用已登录账号);要允许需开 "Allow in Multiplayer"。
- **加密与例外:** 静态加密,只有你自己的 orb 线程可读;**不保存 Amp 登录或 portal 登录**;删除 profile 即清除全部数据。
- **passkey:** orb 内 Chrome 无蓝牙或 USB。需要在 **macOS 应用**里打开该线程的 Desktop 标签,弹窗确认后临时使用蓝牙(最长 2 分钟)中转手机 passkey;仅线程所有者可做。
- **Proof of Human(账户级 passkey):** 要求有效的 passkey 会话才能远程控制线程;个人可开,Enterprise workspace 管理员可强制。管理员以管理员权限查看非自己有权看的线程时**始终要求** passkey。来源:security 页。
- 来源:https://ampcode.com/docs/markdown/orbs/profiles

### 5.7 自托管时的数据外流 `[现行]`(self-hosted 2026-11-11)
- 代理循环和模型推理在 Amp 服务器;orb 只执行 agent 的工具。
- **留在你基础设施内:** 仓库检出、构建产物、保存的 orb 磁盘。
- **会发往 Amp 并随线程存储的内容:** agent 读取的文件内容、命令输出、Changes 标签里的 diff、截图和线程附件。
- Amp **从不连入**你的网络,不需要云凭据或集群权限,也不需要放行 Amp 出口 IP。
- **orb 需要出站放行**(代理需允许长连接):

| 目的地 | 用途 |
|---|---|
| `ampcode.com` HTTPS 443 | 接收作业和上报结果 |
| `static.ampcode.com` | CLI 更新 |
| `ampcode.com` HTTPS + WSS | 引导和工具连接 |
| `tunnel.ampcode.com` WSS | portal、Terminal、Desktop |
| 你的 Git 主机 | clone 和 push |
| `storage.googleapis.com` | 镜像无 rg 时下载 ripgrep |
| `*.nscluster.cloud` WSS | SSH 到 macOS satellite |

- **Self-Hosted Only 模式:** workspace 管理员可把 "Where Orbs Run" 设为仅自托管:不再使用内置 orb(含自定义 agent 的服务端工具沙箱),不能启动 Amp 托管的 Mac satellite。另有 "Personal Orb Providers" 单独开关。
- **BYO 仓库检出:** 关闭 Repository Setup 后,Amp 不 clone、不改 Git 凭据助手和 `gh`、也不为 orb 获取 GitHub 令牌。镜像须在 `/home/user/workspace/repo` 有检出。
- **provider 约束:** 每个令牌只能运行一个 `amp orb provider`,否则两个实例各自认领作业会导致线程的 orb 分裂;需 `--allow-multiple` 才并存。provider 令牌形如 `aop_live_…` 且只显示一次。
- **删除线程语义:** 归档只会发 `pause`,**不会**发 `delete`,所以归档线程的 orb 和磁盘仍留在你的平台上。
- **权限要求:** 镜像需要 `user` 用户的无密码 sudo(用于配置系统 Git 设置、准备运行时文件和管理服务)。
- **托管侧对照(安全页):**
  - 暂停 orb 的文件系统和内存静态加密保存,直到线程被删除。
  - 删除线程即删除 orb(通常数秒)。
  - 项目快照最多复用 72 小时;被替换的快照要等所有由它创建的 orb 都删除后才能真正删除。
  - Amp 不提供独立于线程的 orb 保留期。
  - admin 可关闭 workspace 的 orb 线程创建。
  - 传输 TLS 1.2+,静态 AES-256。
- **Amp 托管 orb 的网络:** 出站 IP 会变,无法加入白名单;需要访问内网或限 IP 的 API,用 **Tailscale**(OIDC trust credentials,`tailscale up --client-id --id-token file:`,`tag:amp-orb`),或 **Orb Network Routing**(`amp --no-tui --share-network` 的 runner,`amp net route <runner-id>`)。
- **Tailscale 坑:** 不要用 `--exit-node`,出口节点不可达会让 orb 与 Amp 失联,线程会一直等待永不连接的 executor;E2B microVM 上需设 `TS_ASSUME_NETWORK_UP_FOR_TEST`;不要在 setup 中登录,否则机器身份会进入快照。
- 来源:https://ampcode.com/docs/markdown/orbs/self-hosted · https://ampcode.com/docs/markdown/egress-ips · https://ampcode.com/security

---

## 6. 其他现行功能要点(必答点 2 补充)

### Services(`.amp/services.yaml`)`[现行]`(portals 2026-11-10)
- 字段:`command`(必填)、`cwd`、`port`、`env`、`health`(HTTP 2xx/3xx 就绪)、`portal`、`portals`(可含 folder)、`review`、`platforms`(`linux`/`darwin`)。
- 服务名限制:小写字母、数字、连字符,最长 32 字符。
- 变量:Amp 注入 `PORT`、`AMP_THREAD_ID`;有 portal 的服务另有 `PUBLIC_URL`;`$AMP_USER_EMAIL` 是 portal 条目里唯一支持的占位符。
- 服务间引用:`${services.web.publicURL}`,会拒绝缺失服务和循环依赖。
- 命令:`amp orb services ensure [--json]`、`amp orb service list|status|logs|restart|stop|start`、`amp orb portal <port>`。
- 生命周期:Amp 作为监督者管理(跨 Amp 更新存活),Amp-hosted orb 的服务在暂停期间保持运行。**不要用 nohup/setsid/&/tmux 保活**。
- `.amp/portals/*.json` 是生成物,应加入 `.gitignore`。
- 来源:https://ampcode.com/docs/markdown/orbs/portals

### Desktop / Chrome / computer use `[现行]`
- 高分辨率可交互 Linux 桌面;agent 操作网站时用同一个 Chrome 窗口,需要登录或验证码时让用户接管。
- orb 里的 computer use **"early and unsupported"**。
- 可通过 `.agents/desktop/resume`(优先)或项目设置写启动脚本;`sudo -n /opt/amp-desktop/install-app <chrome|firefox|code|blender|terminal>`;调试时 `sudo systemctl restart amp-desktop`。
- 不能运行 Android 模拟器(无硬件虚拟化),要用带模拟器的 runner。
- runner 桌面:Linux 为无头 labwc(1280x720)+ Waymote;macOS 需要 Apple Silicon、macOS 15+、Screen Recording 和 Accessibility 授权;Desktop/Terminal 经 WebRTC,5 秒内连不上则退回 Amp 中继。
- 来源:portals 文档的 Desktop 一节 · https://ampcode.com/docs/markdown/cli/runners

### Terminal 与 Files 与 `amp sync` `[现行]`(docs/orbs 2026-10-27)
- Terminal 是共享 tmux 会话,agent 可读其输出,也可把终端嵌入回复。
- 上传:Files 面板拖拽(单文件最大 512MB)、终端粘贴图片或拖文件(上传并插入绝对路径)、线程附件(默认 100MB,高上限用户 512MB)。
- 附件对线程私有,不会自动进入仓库;"Temporary" 附件几小时过期;"Public" 是 Amp 发布的文件,须谨慎。
- `amp sync <thread>`:本地检出必须干净;检出 orb 的提交并复制未提交改动,orb 未 push 的提交直接从 orb 取;停止时重置回原状态,期间不要本地编辑。
- `amp sync --apply`:一次性拷贝,可加 `--checkout` 或 `--skip-checkout`;在 linked worktree 里若分支已被占用则游离检出。
- 登录:`amp login` 或 `AMP_API_KEY`;只读取已 push 的 Amp 托管项目可用只读 Git 凭据。
- 来源:https://ampcode.com/docs/markdown/orbs · /files-and-attachments

### Ship 与 Restack `[现行]`(shipping 2026-11-05)
- Ship 行为三选一:**Ship**(默认,trunk-based:commit → rebase → 跑完整测试 → push 基线分支,冲突在 orb 内解决)、**Push to Branch**、**Custom Ship**(≤10,000 字符纯文本,无模板变量)。
- 从 Puck 或另一个线程触发 ship 时发送同一提示;只有说 "ship" 才使用 Ship 提示。
- Restack:把中间提交整理为更干净的提交集,保留最终文件内容,**不 push**。
- `amp projects update … --ship-behavior custom --custom-ship-prompt-file`;提示由 Amp 存一份副本,不会自动读取仓库文件。
- 来源:https://ampcode.com/docs/markdown/orbs/shipping

### Automations 与 Schedules `[现行]`(2026-11-02)
- 每线程**一个**调度;保存一个提示,到点时用短消息唤醒 agent,agent 通过 `get_schedule` 读完整提示。
- 一次性调度运行一次后结束;重复调度到暂停、删除、到期或满足完成条件为止。
- **snooze:** 线程可被"Snooze Until I'm Needed",调度和事件订阅仍会唤醒 agent 但不弹侧栏和通知;只有回复里出现新信息(有人回复、失败、命中监控条件等)时才解除 snooze,"没有变化"类回复保持静默。
- 来源:https://ampcode.com/docs/markdown/orbs/automations

### GitHub listeners / Assign It To Amp / Restack / Setup Without a Commit / Close the Loop / Runner Desktop
- 早先已读,未重复核对(参见已读清单)。

---

## 7. 对 Oryn 的启示

1. orb 的核心契约就是 `spawn / pause / delete` 三作业加 `ORB_SNAPSHOT_ID`,Oryn 自有 provider 可直接复刻这个最小接口,先别发明别的。
2. 暂停语义要区分"agent 空闲"和"用户交互"两个计时器(5 分钟 + 20 分钟取较晚者),portal 请求也算交互。
3. 快照键应是 `(项目, 尺寸)`,命中条件是 base 分支 HEAD + 共享配置哈希;过期后"恢复旧快照再增量重跑 setup",并强制 setup 幂等。
4. 快照需要最大年龄(Amp 为 72 小时),以便基础镜像更新能传播;删除快照要受"仍有 orb 引用"约束。
5. 对无法读取 base 分支 HEAD 的主机,回退为"每成员一份快照 + 每次跑 setup",并在 UI 提示会更慢。
6. setup 阶段的凭据要和运行期凭据严格分层:setup 只拿 project/workspace 身份,个人和线程身份只在 resume 阶段注入,且保存快照前清理请求凭据。
7. 一个有价值的低成本设计是:setup 超过 2 分钟在线程里显示警告,超过 20 分钟强制截断但不发布快照。
8. clone 失败阈值(15 分钟)和 Shallow Clone 开关要做成项目级设置,并默认拉全历史以保证 `git blame` 可用。
9. 排队生成 orb:用户级令牌桶(突发 20,之后每 5 分钟 1 个),超限排队并展示预计等待,不报错。
10. 生命周期脚本三分:`setup`(幂等、可缓存)、`resume`(≤10 秒、每次唤醒)、`services`(受监督常驻);脚本退出后残留进程要被清理,避免快照污染。
11. 服务层建议照搬 `services.yaml` 的 `PORT`/`PUBLIC_URL`/`health` 约定,并让 portal 请求触发 `ensure`。
12. portal 默认"线程可见者可访问",公开是限时、按端口、需所有者批准的特例;自托管侧默认关闭公开。
13. 向被代理应用注入 `X-Amp-Authenticated` 与用户身份头,让应用自己做授权,而不是在代理层做细粒度鉴权。
14. OIDC 是比"注入密钥"更好的默认:自建 issuer,claims 含 workspace/project/user/thread,`sub` 分四级范围,TTL 默认 10 分钟。
15. 密钥遮蔽要在 orb shell 输出层面做"值全局替换",并在保存时拒绝过短或常见词的值,防止误伤。
16. 向用户要密钥走私密对话框,不进线程;agent 自己生成的值直接存储。
17. 自托管边界要说清:代理循环和推理在服务端,工具执行在 orb,所以文件内容、命令输出、diff、截图仍会外流;Oryn 若要做"数据不出域"需改变这条边界。
18. 自托管 provider 必须"只出站"(provider 主动长连服务端),避免要求用户开入站端口。
19. 同一令牌只允许一个 provider 实例在线,并检测冲突,否则线程的 orb 会分裂。
20. 对 K8s 路线,"warm 空闲 worker 数 + 快照落对象存储 + 可在任意节点恢复"是可参照的预热加迁移设计;官方没有独立的托管侧预热池文档。
21. 使用统计应以"orb 运行毫秒 + 尺寸 + 线程"为基本记录,并同时提供 Web 页和 CLI(`usage --details`)两个出口,再让 agent 自己解释;Oryn 不做计费也应保留这套明细。
22. macOS 的教训:不要让 Mac 承载整个线程;采用"Linux 主 orb + 按需子机"的拆分,并给子机独立租约(用完保留 20 分钟复用缓存)。
23. Profiles 的冲突规则("退出登录总是赢"、同条目先到先得)值得照搬;multiplayer 与 profile 互斥是必要的安全约束。
24. webhook 必须"先落库再 200",再唤醒;至少一次投递加幂等键加指数退避加 1 小时丢弃;URL 视为凭据;不替第三方验签。
25. service account 的威胁模型是"能驱动线程的人都继承其凭据",UI 上必须用显眼文案提醒。
26. 不复刻清单中的 handoff、fork、TODO、旧弹窗式权限、TUI 侧边栏、编辑器扩展、旧模式名;权限应作为插件级可选能力,而非核心默认。

---

## 8. 已读页面清单

**公告(本轮读到正文):**
- https://ampcode.com/news/agents-in-orbs
- https://ampcode.com/news/secrets-of-the-orb
- https://ampcode.com/news/more-orb-sizes
- https://ampcode.com/news/size-the-orbs-of-production
- https://ampcode.com/news/portals
- https://ampcode.com/news/event-driven-orbs
- https://ampcode.com/news/macos-satellites
- https://ampcode.com/news/self-hosted-orbs
- https://ampcode.com/news/personal-self-hosted-orbs
- https://ampcode.com/news/mcp-in-orbs
- https://ampcode.com/news/explain-usage
- https://ampcode.com/news/multi-repo-projects
- https://ampcode.com/news/no-mailmap-required
- https://ampcode.com/news/service-accounts
- https://ampcode.com/news/boxes
- https://ampcode.com/news/gitlab
- https://ampcode.com/news/read-bigger-threads
- https://ampcode.com/news/the-coding-agent-is-dead
- https://ampcode.com/news/neo
- https://ampcode.com/news/ask-to-handoff
- https://ampcode.com/news/stick-a-fork-in-it
- https://ampcode.com/news/slashing-custom-commands
- https://ampcode.com/news/todos-are-done
- https://ampcode.com/news/tab-tab-dead

**笔记与站点页:**
- https://ampcode.com/what-are-orbs(含 Thorsten 讲座文字稿)
- https://ampcode.com/notes/what-i-want-to-tell-you-about-orbs
- https://ampcode.com/notes/thats-not-soc-2-compliant
- https://ampcode.com/notes/pave-the-road
- https://ampcode.com/security(前 300 行)
- https://ampcode.com/morbs

**索引:**
- https://ampcode.com/chronicle(含 News/Notes 全部标题和日期)
- https://ampcode.com/news
- https://ampcode.com/sitemap.xml

**现行文档(用于判断最新状态):**
- https://ampcode.com/docs/markdown/orbs
- https://ampcode.com/docs/markdown/orbs/customizing(全文)
- https://ampcode.com/docs/markdown/orbs/optimizing
- https://ampcode.com/docs/markdown/orbs/sizes-and-costs
- https://ampcode.com/docs/markdown/orbs/portals(全文)
- https://ampcode.com/docs/markdown/orbs/handling-secrets(大部分,GCP/AWS/Tailscale 示例已读)
- https://ampcode.com/docs/markdown/orbs/files-and-attachments
- https://ampcode.com/docs/markdown/orbs/shipping
- https://ampcode.com/docs/markdown/orbs/mac
- https://ampcode.com/docs/markdown/orbs/profiles
- https://ampcode.com/docs/markdown/orbs/automations
- https://ampcode.com/docs/markdown/orbs/self-hosted(Dispatch 示例 Docker/E2B/Apple 已读,Modal/Vercel/Daytona/exe.dev/Boat 的代码细节仅在公告中读到)
- https://ampcode.com/docs/markdown/orbs/self-hosted/kubernetes
- https://ampcode.com/docs/markdown/egress-ips
- https://ampcode.com/docs/markdown/cli/runners(前 300 行)

**早先已读(第一波,本轮未重复核对):** spaces、space、agents-anywhere、amp-on-ios-and-macos、amp-on-android、steer-dont-queue、multiplayer、so-long-tui-sidebar、artifacts、less-noise、shared-runners、the-mac-app-is-your-runner、one-runner-many-worktrees、one-runner-is-now-enough、runner-desktop、runner-portals、attach-anything、agents-everywhere、meet-puck、many-many-pucks、from-agent-to-agent、end-of-public-threads、plugins-everywhere、pass-the-orb-to-the-left-hand-side、talk-to-puck、desktop、diffs、visualize-this、proof-of-human、schedule、take-puck-with-you、free-agent、close-the-loop、intelligently-ordered-diffs、friendly-urls-for-sharing-orbs、agents-panel、thread-labels、thread-map、notes/orbs-explained、notes/putting-an-agent-in-an-orb,以及 docs 的 boxes、apps/*、threads、spaces、puck、workspaces 等。

## 9. 未读清单

**列表中有、与执行环境相关、本轮仍未读:**
- 公告:assign-it-to-amp、github-listeners、setup-without-a-commit、restack-your-changes、friendly-urls-for-sharing-orbs、runner-desktop。早先已读,本轮只是未重读,所以第 6 节没有展开它们。
- 公告:first-things-first-faster(Jev 文件排序)、drop-the-neo、npm-package-changes、subscriptions、use-your-claude-plan、build-your-own-dial、a-dial-for-you、amp-labs-westpac、plaid-mode 等模型和商务类。
- docs:orbs/getting-started、orbs/agent-to-agent、apps/*、docs/boxes、docs/feedback-widget、integrations/github|gitlab|google、customize/mcp、customize/plugins、plugin-api、enterprise/*(含 minimal-data-retention)、api/external、docs/cli/clone、docs/cli/spawning-orbs、`docs/orbs/self-hosted/bundle.zip`。
- 其他:time-capsules/1~6(除 6 的文字稿经 what-are-orbs 间接读到)、podcast、YouTube 视频、notes/orbs-explained(早先已读)。
- security 页第 301~375 行(漏洞赏金)。

**确认「未找到」的信息:**
- prewarm 池的独立说明和托管侧容量策略。
- 系统级资源指标 UI(CPU、内存、磁盘曲线)。
- 工作区分析 API 的具体接口。
- "orb 内存告警"功能的发布状态。
- passkey 保护之外的 orb 级审计日志暴露给 Workspace Admin 的方式(安全页写应用审计日志"目前不向 Workspace Admin 开放")。

### Critical Files for Implementation
本报告是只读调研,不涉及仓库文件修改。供后续设计时对照的最关键来源页是:
- `https://ampcode.com/docs/markdown/orbs/customizing` — orb 生命周期、项目快照、setup/resume 契约与时限。
- `https://ampcode.com/docs/markdown/orbs/self-hosted` — dispatch 契约、数据外流边界、provider 约束、Public Portals。
- `https://ampcode.com/docs/markdown/orbs/handling-secrets` — OIDC、线程身份、密钥分层、遮蔽规则。
- `https://ampcode.com/docs/markdown/orbs/portals` — services.yaml、portal 认证头、自定义域名、公开访问。
- `https://ampcode.com/docs/markdown/orbs/sizes-and-costs` — 尺寸表、暂停语义、计费与保活规则。
# Amp 执行环境调研报告(修订版:单用户范围)

**修订说明:**
- 团队协作类只用一两行带过,标 `[团队功能·暂不做]`。
- 篇幅集中在单用户能力。
- 「只看最新版、标注现行/已移除」继续有效。
- 本版是对上一版的**压缩与重排**,没有新增读页。事实依据仍是上一版列出的已读页面。
- 状态标记:`[现行]`、`[已移除]`、`[已被取代→新形态]`。

---

## 0. 大改版时间线(单用户视角)

| 日期 | 事件 | 改了什么 / 删了什么 | 来源 |
|---|---|---|---|
| 2026-01-12~15 | 清理编辑器时代功能 | TODOs 删(01-12)。Fork 删(01-13)。Amp Tab 删(01-15,1 月底停服)。01-13 "Handoff, Please"让 handoff 变成对话触发。 | https://ampcode.com/news/todos-are-done · /news/stick-a-fork-in-it · /news/tab-tab-dead · /news/ask-to-handoff |
| 2026-01-29 | 自定义命令删 | 迁到 `.agents/skills/<name>/SKILL.md`。 | https://ampcode.com/news/slashing-custom-commands |
| 2026-02-19 | 编辑器扩展宣布死亡 | 2026-03-05 20:00 PT 自毁。理由:瓶颈已是代码库组织和流程,不是 agent 外壳。 | https://ampcode.com/news/the-coding-agent-is-dead |
| 2026-05-06 | **Neo 重写** | 新增:远程控制、自动 compaction(上下文 90% 触发)、Plugin API、默认排队与 steer、权限默认关。删除:handoff、编辑/恢复消息时的文件回滚、skill 管理子命令、用户可调用 skill、主题、`$`/`$$` 手动 bash。CPU -79%,内存 -70%。 | https://ampcode.com/news/neo |
| 2026-05-27 | Drop the Neo、Proof of Human | Neo 即 Amp。账户可要求 passkey 才能远程控制。 | 公告列表标题(正文本轮未读)、Neo 页 Update 行 |
| 2026-05-14 | npm 包改名 | `@sourcegraph/amp` → `@ampcode/cli`。 | 公告列表 |
| **2026-06-30** | **orb 诞生** | 每线程一台云机器,按分钟计费,闲置睡眠。同时有 `amp sync`、`amp -ox`、Files/Terminal/Changes。 | https://ampcode.com/news/agents-in-orbs |
| 2026-07-03 | 可选尺寸 | 磁盘 20GB→40GB。 | https://ampcode.com/news/more-orb-sizes |
| 2026-07-09 | The Dial | low/medium/high/ultra 取代 smart/deep/rush/large。 | 公告列表;早先已读 |
| 2026-07-14 | OIDC | orb 可用 OIDC 证明身份,不注入长期密钥。 | https://ampcode.com/news/secrets-of-the-orb |
| 2026-07-20~23 | Puck、Schedule、事件驱动 orb | webhook 唤醒休眠 orb。 | https://ampcode.com/news/event-driven-orbs |
| **2026-08-06** | Portals | `.amp/services.yaml` 加 portal。 | https://ampcode.com/news/portals |
| **2026-08-07** | 尺寸与暂停重做 | a0→a1;自动暂停 15→5 分钟;启动加速。 | https://ampcode.com/news/size-the-orbs-of-production |
| 2026-08-19~28 | MCP in orbs、Explain Usage、Setup Without a Commit、多仓库、Git 身份 | 见第 2 节。 | 见第 2 节 |
| 2026-08-27 | TUI 侧边栏删除 | — | 公告列表 |
| 2026-09-04 | Desktop | orb 内可交互 Linux 桌面。 | 早先已读 |
| 2026-09-08 | Steer, Don't Queue | **推翻 Neo 的默认排队**,发送即 steer。 | 早先已读 |
| 2026-09 | runner 系列 | 一个 runner 多目录、worktree、Mac 应用即 runner。 | 早先已读 |
| 2026-10-22 / 10-28 | 自托管 orb / 个人 provider | `amp orb provider` 派发 spawn/pause/delete。 | https://ampcode.com/news/self-hosted-orbs · /news/personal-self-hosted-orbs |
| **2026-10-29** | **macOS Satellites** | **推翻 Morbs**:Docker 和后端要几分钟起,每线程一块 Mac 磁盘太贵 → Linux orb + 按需 Mac。 | https://ampcode.com/news/macos-satellites |
| 2026-11-06 | Spaces | — | 早先已读 |
| 2026-11-09 | Runner Portals | portal 扩到本地 runner。 | 早先已读 |
| **2026-11-10** | Boxes | 永久在线 VM,每应用一个 `onamp.dev` URL。 | https://ampcode.com/news/boxes |

## 0.1 「老版功能,不要复刻」

| 老功能 | 状态与依据 |
|---|---|
| Handoff | `[已移除]` 2026-05-06,compaction 取代。01-13 刚加的"对话触发 handoff"也被废。 |
| Fork | `[已移除]` 2026-01-13,改为"新线程 + URL 提及"。 |
| TODO 列表 | `[已移除]` 2026-01-12。 |
| Amp Tab、VS Code/Cursor 扩展 | `[已移除]` 01-15 / 03-05。 |
| 自定义命令 | `[已被取代→skills]` 2026-01-29。 |
| 旧弹窗式权限 | `[已被取代→内置插件]` 默认全放行;配置了 `amp.permissions` 等才加载。 |
| 编辑或恢复消息时文件回滚 | `[已移除]` 2026-05-06。 |
| CLI 主题、`$`/`$$`、skill 管理命令、用户可调 skill | `[已移除]` 2026-05-06。 |
| TUI 侧边栏 | `[已移除]` 2026-08-27。 |
| 旧模式名 smart/deep/rush/large | `[已被取代→Dial]` 2026-07-09。 |
| 公开线程 | `[已移除]` 2026-06-02。 |
| 消息默认排队 | `[已被取代→steer]` 2026-09-08。 |
| Morbs | `[已被取代→macOS Satellites]` 2026-10-29。 |
| `a0.*` 尺寸 | `[已被取代→a1.*]` 2026-08-07。 |
| 15 分钟空闲暂停 | `[已被取代]` 现为 5 + 20 分钟规则。 |
| orb 内账户 `AMP_API_KEY` | `[已被取代→线程身份]` 文档称 Amp is removing。 |
| 手动 @ 文件提及 | `[推测:新 UI 已去除]` 依据 Thorsten 讲座称新应用"不再有 @ 提及"。 |

**新旧冲突(以最新为准):**
1. 安全页仍有"Amp doesn't offer self-hosted deployments",而 2026-10-22 起有自托管 orb,以 self-hosted 文档(2026-11-11)为准。
2. 自托管定价:公告说 Enterprise 自 10-29 收费,文档说免费至 11-13。
3. orb 磁盘:07-03 公告 40GB,sizes 文档(11-03)写 60GB。
4. 项目默认尺寸 `a1.medium`;Megawatt 订阅者的个人项目默认 `a1.small`。

**素材规则:** 文档页的 `-light/-dark.avif` 图为最新 UI。Neo 公告的 TUI 视频、`agents-in-orbs` 初代演示视频、任何带 TUI 侧边栏的图均为旧版。

---

## 1. orb 体系演进(被推翻的设计与理由)

1. 初代 orb(06-30)。理由:把模型限制在单机上就是拖后腿。
2. 尺寸与磁盘:20GB→40GB→60GB。
3. 08-07:价格降低;自动暂停 15→5 分钟;共享项目快照提速。
4. OIDC 取代长期密钥;orb 用线程身份登录 Amp。
5. Morbs → Satellites:把 Linux 能做的留在 orb(启动快),Mac 只做需要 macOS 的事。
6. 自托管:Amp 不接入你的网络,只做出站连接。官方明确劝退多数人:"You probably shouldn't self-host orbs"。
7. Boxes:orb 里开发,box 里常驻,二者分离。

---

## 2. 单用户核心能力(逐项要点)

### 2.1 orb 生命周期、尺寸与计费 `[现行]`
- **尺寸:**

| 尺寸 | CPU | 内存 | 价格 |
|---|---|---|---|
| `a1.tiny` | 1 | 2GB | $0.08/h |
| `a1.small` | 2 | 4GB | $0.17/h |
| `a1.medium` | 4 | 8GB | $0.33/h |
| `a1.large` | 8 | 16GB | $0.66/h |
| `a1.xxlarge` | 16 | 32GB | $1.32/h |
| `a1.3xlarge` | 16 | 44GB | $2.13/h |

  - 全部带 60GB 磁盘。`a1.3xlarge` 仅限 Gigawatt 订阅者和 Enterprise。
  - 自定义尺寸价格:CPU×$0.0504/h + GB×$0.0162/h。
- **选择尺寸:** 项目默认,或在新线程对话框每线程选;`amp -ox --orb-size`;让 agent 起子线程时指定更小尺寸做 fan-out。
- **暂停规则:** agent 空闲 5 分钟**且**用户无交互 20 分钟,取较晚者。
  - 交互 = 发消息、用 Files/Terminal/Desktop、任何授权的 portal HTTP 请求。
  - Puck 或 automation 消息算 agent 工作,不开 20 分钟窗口。
  - 保活:回传结果、automation/webhook、插件 `amp.system.executor.keepAlive()` 租约、排队消息。**Dev server 本身不保活。**
- **Orb Saver Mode**(Settings → Orbs):仅打开线程不唤醒 orb。
- **spawn pacing:** 每用户突发 20 个,之后每 5 分钟 1 个;超限排队等待而非失败。
- 来源:https://ampcode.com/docs/markdown/orbs/sizes-and-costs · /orbs

### 2.2 启动速度 `[现行]`
- **项目快照:** 每项目每尺寸一份。命中条件 = base 分支 HEAD(及附加仓库默认分支 HEAD)+ 共享配置(workspace/项目环境变量与密钥、pre-clone、pre-setup)。
  - 命中则跳过 `.agents/setup`。
  - 过期后恢复旧快照,再增量重跑 setup(所以必须幂等)。
  - 同一基础镜像上的快照最多复用 **72 小时**,之后从基础镜像重来。
  - 可手动清除:`amp projects snapshots list|delete <project> [--resource a1.small]`。
- **共享快照例外:** 仓库不在 GitHub/GitLab,或无法读到 base HEAD 时,**每成员各有快照且每次都跑 setup**,启动更慢。
- **setup:** 硬上限 20 分钟,超时仍启动 orb 但不发布快照;近几次 ≥2 分钟时线程里显示警告;日志 `/home/user/.cache/amp/logs/setup.log`。脚本退出后残留进程会被杀,数据库用 `sudo systemctl enable --now`。
- **resume:** 首次激活和每次唤醒都跑,最多等 10 秒。
- **克隆:** 默认完整历史(跳过 >1MB 的文件版本,命令需要时才拉);clone 超过 15 分钟 orb 启动失败;可开 **Shallow Clone**。
- **预热(prewarm):** 文档只在 Git History 一节顺带提到"prewarmed orbs";**无独立页面,容量与策略「未找到」**。
- **进程续命:** Amp 托管 orb 暂停时保存磁盘和内存,进程继续运行。
- **镜像:** Debian 12/13,预装 amp、gh、glab、Git、tmux、Bun/Node/pnpm/Yarn、Python/uv、Nix、Docker Engine(daemon 默认不启动)、Chrome for Testing、ffmpeg 等。
- **旧机制:** 08-07 前 15 分钟暂停;启动提速在 08-07 公告中只有定性描述。
- 来源:https://ampcode.com/docs/markdown/orbs/customizing · /optimizing

### 2.3 Services 与 Portals `[现行]`
- `.amp/services.yaml`:`command`(必填)、`cwd`、`port`、`env`、`health`(2xx/3xx 就绪)、`portal`/`portals`(可含 folder)、`review`、`platforms`。服务名小写加连字符,最长 32 字符。
- Amp 注入 `PORT`、`AMP_THREAD_ID`(有 portal 的另有 `PUBLIC_URL`);`$AMP_USER_EMAIL` 是 portal 条目里唯一支持的占位符。服务间用 `${services.web.publicURL}` 引用,拒绝缺失和循环依赖。
- 命令:`amp orb services ensure [--json]`、`amp orb service list|status|logs|restart|stop|start`、`amp orb portal <port> [--hostname]`。服务由 Amp 监督(跨 Amp 更新存活),暂停时保持;**不要用 nohup/setsid/&/tmux 保活**。
- 生成物 `.amp/portals/*.json` 要加入 `.gitignore`。
- **访问:** 默认只有该线程的可见者可开。公开访问为限时(1h/3h/1d/7d)、按端口、需用户批准;`amp orb portal list` 可看公开状态,CLI 不能改访问。
- **应用侧身份头:** `X-Amp-Authenticated: amp-user=…, workspace-member=…, collaborator=…`,以及 `X-Amp-User-ID/Display-Name/Email/URL`。
- **限制:** WebSocket 穿过 Amp 服务器,部署时会断(1006),不能用于数据库隧道。两个 portal 主机间的浏览器请求不受支持(CORS 预检不带认证)。
- **自定义域名:** 个人 Domains 页添加,配 DNS 到 Active,`amp orb portal <port> --hostname`,`--take-over` 迁移。
- **Review widget:** 默认开启,可 `review: false` 或响应头 `x-amp-review-widget: off` 关闭。评论携带选中文本、页面位置和(可得时)源码位置。
- **dev 登录:** 推荐 `__dev/log-me-in/{email}?returnTo=` 形式的路由,仅限开发。
- 来源:https://ampcode.com/docs/markdown/orbs/portals · https://ampcode.com/news/portals

### 2.4 Files、Terminal、`amp sync`、附件 `[现行]`
- **Terminal:** 共享 tmux 会话,agent 可读输出,也可嵌入回复。
- **上传:** Files 面板拖拽(单文件最大 512MB);终端粘贴图片或拖文件(上传并插入绝对路径);线程附件默认 100MB(高上限 512MB)。
- **附件:** 对线程私有、不进仓库;"Temporary" 数小时过期;"Public" 是 Amp 发布的文件,需谨慎。
- **`amp sync <thread>`:** 本地必须是干净检出;检出 orb 提交并复制未提交改动,停止时重置回原状,期间不要本地编辑。
- **`--apply`:** 一次性拷贝,可加 `--checkout` 或 `--skip-checkout`;linked worktree 中分支被占用则游离检出。
- **认证:** `amp login` 或 `AMP_API_KEY`;只拉已 push 的 Amp 托管项目可用只读 Git 凭据。
- 来源:https://ampcode.com/docs/markdown/orbs · /files-and-attachments

### 2.5 Desktop、Chrome、computer use `[现行]`
- Linux 桌面,agent 操作网站时共用同一个 Chrome 窗口;遇到登录或验证码,请用户接管。
- orb 里的 computer use **"early and unsupported"**。
- `.agents/desktop/resume`(优先)或项目设置写启动脚本;`sudo -n /opt/amp-desktop/install-app <chrome|firefox|code|blender|terminal>`;调试时 `sudo systemctl restart amp-desktop`。
- 不能跑 Android 模拟器(无硬件虚拟化),要用带模拟器的 runner。
- **runner 桌面:** Linux 为无头 labwc(1280×720)+ Waymote;macOS 需要 Apple Silicon、macOS 15+、授权 Screen Recording 和 Accessibility;Desktop/Terminal 经 WebRTC,5 秒连不上退回 Amp 中继。
- 来源:portals 文档 Desktop 一节 · https://ampcode.com/docs/markdown/cli/runners

### 2.6 浏览器 Profile 与 passkey `[现行,需在实验功能开启]`
- **保存内容:** 登录态、保存的密码、站点数据(local storage 等);多个命名 profile(3~32 位字母、数字、连字符)。
- **选择:** 新线程选择;`amp -ox --profile`;orb 内 `amp profile use <name>` / `amp profile none`。
  - agent 自主选择须先在 Settings → Profiles 打开 "Allow Agent to Select"。
  - 只能读写自己线程所选的 profile。
- **同步:** 登录和 local storage 几秒内同步到同 profile 的其他 orb;其余每 2 分钟和切换时保存。
- **冲突:** 同站点登录先到先得,但**退出登录总是赢**;local storage 冲突除同一条目外都保留。
- **范围:** 只支持 Linux orb。静态加密;**不保存 Amp 登录或 portal 登录**;删除 profile 即清空。
- **passkey 中转:** 在 **macOS 应用**里打开该线程 Desktop 标签,弹窗允许后临时用蓝牙(最长 2 分钟)中转手机 passkey,仅线程所有者可做。
- 与 multiplayer 互斥 `[团队功能·暂不做]`。
- 来源:https://ampcode.com/docs/markdown/orbs/profiles(2026-11-06)

### 2.7 macOS Satellites `[现行,beta + 等候名单]`
- **机制:** 服务端按需起一台全新 Mac(约 30 秒),带 Xcode 和模拟器。orb 把改动同步过去,Mac 只构建、运行、测试;Git 忽略的文件(如 `.env`)留在 orb。Mac 上的 app 通过 portals 访问 orb 后端。
- **生命周期:** 每次全新、不保存任何东西;用完保留 20 分钟(复用构建缓存),之后删除。
- **规格和价格:** `mac.medium` 6C/14G $2/h;`mac.large` 12C/28G $4/h,从同一份 orb 额度扣。
- **镜像:** `macos-27`/`macos-26`/`macos-15`,带不同 Xcode 和模拟器版本;`.xcode-version` 钉版本。
- **限制:** 不能跑 Android 模拟器。
- **签名:** App Store Connect API key,安装前询问,Mac 停止后凭据删除。
- **用量:** Settings → Usage → Satellites。
- 来源:https://ampcode.com/news/macos-satellites · https://ampcode.com/docs/markdown/orbs/mac

### 2.8 自动化、Schedule、webhook、事件 `[现行]`
- 每线程**一个**调度;保存一个提示,到点用短消息唤醒 agent,agent 用 `get_schedule` 读完整提示。一次性调度运行一次后结束;重复调度到暂停、删除、到期或满足完成条件为止。
- **snooze:** "Snooze Until I'm Needed" 让调度和事件订阅仍然唤醒 agent,但不弹侧栏和通知;只有回复含新信息(有人回复、失败、命中监控条件、问你问题)才解除,"无变化"类回复保持静默。
- **webhook(Plugin API `amp.createWebhook`):**
  - 先落库再返回 200;至少一次投递,需按 `event.id` 去重。
  - 处理器 30 秒超时;失败退避 5 秒起翻倍至 5 分钟;持续失败 1 小时后丢弃。
  - 限速:突发 10、每分钟补 10;队列满 100 返回 429;请求体最大 1MB。
  - **URL 即密码**;Amp 不替第三方验签,要在 `headers` 声明并自己验证;未声明的头被丢弃。
  - 拥有者线程归档时 URL 返回 404,事件投递暂停。
- 事件驱动 orb 示例:CI 失败、新 issue、监控告警、Discord 事件。
- 来源:https://ampcode.com/docs/markdown/orbs/automations · https://ampcode.com/news/event-driven-orbs

### 2.9 Ship、Restack `[现行]`
- Ship 三选一:默认 **Ship**(trunk-based:commit → rebase → 全量测试 → push base 分支,冲突在 orb 内解决)、**Push to Branch**、**Custom Ship**(≤10,000 字符纯文本,无模板变量)。
- `amp projects update … --ship-behavior custom --custom-ship-prompt-file`;提示由 Amp 存副本,不会自动读仓库文件。
- Restack:把中间提交整理成更干净的提交集,保留最终文件内容,**不 push**。
- 来源:https://ampcode.com/docs/markdown/orbs/shipping

### 2.10 个人密钥、OIDC、身份 `[现行]`
- **层级:** workspace/项目/个人三级,个人 > 项目 > workspace。personal 级只作用于自己的 orb。
- **遮蔽:** secret 保存后设置页隐藏;**orb shell 输出里每处出现都会被遮蔽**。短值或常见词会误遮蔽,保存时有警告。
- **私密输入:** 用户已有的密钥通过私密对话框输入,**不进入线程**;Amp 自己生成的随机值直接存储。
- **历史:** `amp secrets history` 显示谁创建与修改,不显示值。
- **刷新:** 运行中的 orb 用 `amp orb restart-processes`。
- **线程身份:** orb 里的 `amp` CLI 用短期自动轮换的线程身份;Amp 托管仓库的 Git 操作用只写 Git 的令牌。
- **`AMP_API_KEY`:** 被移除出 orb 环境;需要时另存个人密钥。
- **OIDC:**
  - `amp orb id-token --audience <aud> [--ttl-seconds N] [--subject-scope thread|user|project|workspace]`。
  - 发行方 `https://ampcode.com/api/workload-identity`;RS256;默认 10 分钟,可设 60 秒到 1 小时。
  - claims:`workspace_id`(个人线程缺省)、`project_id`、`user_id`、`thread_id`、`email`、`email_verified`、`token_use=exchanged`。
  - 策略应匹配不可变 ID,不要用 email。AWS 忽略自定义 claim,只能靠 `aud` 与 `sub`。
- **工作流身份分层:** pre-clone/pre-setup/setup 阶段只能铸 project 或 workspace 范围令牌(无用户或线程身份);请求凭据在 `/run/amp/workload-identity-request-token`,保存快照前删除;`.agents/resume` 才拿到新的运行时凭据。**脚本是受信代码**,Amp 不会清理脚本自己复制出去的令牌。
- 示例:GCP WIF 读日志、AWS IAM OIDC、Tailscale trust credentials。
- 来源:https://ampcode.com/docs/markdown/orbs/handling-secrets

### 2.11 Git 身份与签名 `[现行]`(2026-08-28)
- Git 身份与 Amp 账户邮箱解耦,在 Signing Keys → Git identities 管理;个人默认可被项目覆盖。
- 启动 orb 时设置 `GIT_AUTHOR_*` 和 `GIT_COMMITTER_*`;开启签名时只为**已验证**邮箱签名。Puck 可发验证链接。
- workspace 管理员规则(Amp / Amp Account / User Choice):`[团队功能·暂不做]`。
- 来源:https://ampcode.com/news/no-mailmap-required

### 2.12 Git 主机、GitLab、仓库配置 `[现行]`
- GitHub 仓库 orb 里无需配置;GitLab 连接后 orb 以你的账户 clone 与 push,`glab` 已登录。
- 其他 HTTPS 主机(Bitbucket 等)用 `GIT_CONFIG_COUNT/KEY_0/VALUE_0` 的 `url.<…>.insteadOf` 技巧放令牌,KEY_0 标记为 secret;令牌要 percent-encode。
- 不要把 GitLab 令牌放进项目密钥,否则会覆盖集成。
- 多仓库项目:最多 20 个附加仓库,检出到 `../repos/`,只自动跑主仓库的 `.agents/setup`;Changes 面板跨仓库显示 diff。
- **GitLab 为每个新 MR 起 orb 的 spawner** 与服务账户用法:`[团队功能·暂不做]`(仅个人订阅 MR 评论和 CI 可参考)。
- 来源:https://ampcode.com/news/gitlab · https://ampcode.com/docs/markdown/orbs/customizing

### 2.13 MCP in orbs `[现行]`
- 在 ampcode.com/settings/mcp-servers 连接远程 MCP,orb、TUI、runner、Puck 共用。
- 仅 Streamable HTTP(OAuth 或 Bearer);**不支持** MCP Apps/Resources/Prompts。
- 来源:https://ampcode.com/news/mcp-in-orbs

### 2.14 用量与统计视图 `[现行]`
| 项 | 内容 | 来源 |
|---|---|---|
| 个人用量页 | `/settings/usage` 和按线程用量,均有 **Explain Usage** 按钮 | explain-usage 2026-08-21 |
| Puck 问答 | 哪些线程用 token 最多;最贵线程的模型;能否缩小 orb 尺寸;毫秒级 orb 时间 | 同上 |
| CLI | `amp usage --details`、`amp threads usage <id> --details` | 同上 |
| Satellite | Settings → Usage → Satellites(Mac 运行时长和费用) | mac 文档 |
| 自托管 | 线程用量显示每个 orb 的运行时长和尺寸(即使 $0) | self-hosted 文档 |
| 日志 | `~/.cache/amp/logs/{setup,resume}.log`、`amp orb service logs <name>` | customizing / portals |
| 状态 | 线程动作菜单显示 orb 运行或暂停,并复述超时规则 | sizes-and-costs |

**「未找到」:** 系统级资源指标(CPU/内存/磁盘曲线)的 UI;按 orb 的使用统计导出;"orb 内存告警(top-5 进程)"功能是否已发布(Thorsten 案例仅提到正在开发)。

### 2.15 自托管 orb(单用户视角)`[现行]`
- `amp orb provider ./dispatch.sh`:provider 向外连接 ampcode.com;**无需入站端口**,Amp 从不连入你的网络,也不需要放行 Amp 出口 IP。
- 个人在 Settings → Orbs 添加 provider(令牌 `aop_live_…`,只显示一次),线程创建时选 Self-Hosted,标 Personal。
- **派发契约:** 环境变量 `ORB_OPERATION` ∈ {spawn, pause, delete}、`ORB_ID`、`ORB_IMAGE`(spawn)、`ORB_SNAPSHOT_ID`、`ORB_TYPE`、`ORB_TOKEN`(spawn,短期凭据)、`AMP_URL`。
  - `spawn`:启动或恢复机器,并以镜像默认用户在其中后台运行 `amp orb bootstrap`。
  - `pause`:停止并保留磁盘;若有 `ORB_SNAPSHOT_ID` 则另存副本;平台存不住磁盘则直接删除,下次重新 setup。
  - `delete`:仅在线程删除时发送,**归档只发 pause**。
  - 每次作业必须幂等,且 5 分钟内完成。
- **能力对照:**

| 平台 | 内存 | 磁盘 | 项目快照 |
|---|---|---|---|
| Amp 托管 | 是 | 是 | 是 |
| Docker | 否 | 是 | 同一主机 |
| Apple containers | 否 | 是 | 同一台 Mac,需 `container` ≥1.6.0 |
| K8s(Helm) | 否 | 是 | 是 |
| E2B | 是 | 是 | 示例未接 |
| Modal / Vercel / Daytona | 否 | 是 | 示例未接 |
| exe.dev | 是 | 是 | 示例未接 |
| Boat | 无保证 | 部分 | 示例未接 |

- **K8s Helm chart:** 在 GKE/EKS/AKS 上用 gVisor 沙箱 + [Substrate](https://github.com/agent-substrate/substrate);暂停的 orb 磁盘存到对象存储,可在任意 worker 恢复;`workers.types[].min/warm` 控制常驻与预热 worker 数。
- **镜像要求:** glibc 的 Linux x64/ARM64(Alpine 不行);`user` 用户加无密码 sudo;Git ≥2.34(签名);Node ≥18(portal)、tmux ≥2.9(Terminal)、桌面组件;官方镜像在 `us-central1-docker.pkg.dev/amp-prod-0/amp-orb/` 下有 x64 和 arm64 两个。
- **orb types:** provider 可定义类型名;线程一生保持其类型,删除类型会使这些线程无法启动;名称不可改。
- **数据外流边界:** 代理循环和推理在 Amp 服务器;orb 只执行工具。仓库检出、构建产物和保存的磁盘留在你的基础设施。会发往 Amp 并随线程存储的:agent 读取的文件内容、命令输出、Changes 里的 diff、截图和附件。
- **出站放行:** 见下表。

| 目的地 | 用途 |
|---|---|
| `ampcode.com` HTTPS 443 | provider 作业和结果 |
| `static.ampcode.com` | CLI 更新 |
| `ampcode.com` HTTPS + WSS | orb 引导和工具连接 |
| `tunnel.ampcode.com` WSS | portal、Terminal、Desktop |
| 你的 Git 主机 | clone 与 push |
| `storage.googleapis.com` | ripgrep 下载 |
| `*.nscluster.cloud` WSS | 到 macOS satellite 的 SSH |

- **Public Portals:** 默认关(公开 portal 等于通向内网的匿名隧道);在 provider 编辑对话框中单独打开。
- **Repository Setup 开关:** 可关闭,由镜像自带仓库检出(`/home/user/workspace/repo`);此时 Amp 不 clone、不改凭据助手、不取 GitHub 令牌。
- **provider 约束:** 每个令牌只能运行一个 `amp orb provider`,否则两个实例各自认领作业会使线程的 orb 分裂;需要多实例须传 `--allow-multiple` 并共享状态。
- 来源:https://ampcode.com/docs/markdown/orbs/self-hosted · /self-hosted/kubernetes · https://ampcode.com/news/self-hosted-orbs

### 2.16 Orb Network Routing 与 Tailscale `[现行]`
- Amp 托管 orb 的出站 IP 会变,无法加入白名单。
- **网络路由:** `amp --no-tui --runner-id home-mac --share-network` 让 orb 经你自己的网络出网;orb 线程菜单 Orb › Network Routing 选择,或 `amp net route home-mac`。
- **Tailscale:** OIDC trust credential + `tailscale up --client-id=…?ephemeral=true&preauthorized=true --id-token file:… --advertise-tags=tag:amp-orb`。setup 中只安装并禁用 `tailscaled`;**登录必须放在 resume**,否则机器身份会进入快照。
- **坑:**
  - 不要用 `--exit-node`,出口节点不可达会让 orb 与 Amp 失联,线程会一直等待永不连接的 executor。
  - E2B microVM 上需要 `TS_ASSUME_NETWORK_UP_FOR_TEST`。
- 来源:https://ampcode.com/docs/markdown/cli/runners · customizing 文档

### 2.17 Runners(个人形态)`[现行]`
- `amp --no-tui --runner-id <id>` 起一个只等远程线程的实例;`--dir`/`--discover-dirs`/`--discover-depth`/`--discover-exclude`/`--no-serve-cwd` 控制服务的目录(自动发现至多 500 个检出);`amp runner dirs add|list|remove` 动态调整。
- `--remote-control-terminal` 开终端;`--remote-control-portals` 开 portal;`--desktop` 开桌面。
- `amp -x … --executor runner:<id>` 从 CLI 起 runner 线程。
- 共享 runner `[团队功能·暂不做]`。
- 来源:https://ampcode.com/docs/markdown/cli/runners

### 2.18 Boxes(个人形态)`[现行]`
- 永久在线 VM,**无容器**;可多次从多个 orb 部署;每个应用有自己的 `onamp.dev` URL(可私有或要求 Sign In With Amp)。
- 自带 runner,可在 box 上直接起线程操作线上数据。
- 规格:`b1.tiny`(0.5 vCPU 可突发到 2、2GB,$0.025/h,约 $18/30 天)、`b1.small`(默认,1 vCPU 可突发到 2、4GB,$0.042/h)、`b1.medium`(2C/8G,$0.069/h)、`b1.large`(4C/16G,$0.139/h);按分钟计,可定时开关机。
- workspace 共享 box `[团队功能·暂不做]`。
- 来源:https://ampcode.com/news/boxes

### 2.19 单用户相关的其他页
- **Plugin API(Neo 2026-05-06):** `amp.on`、`amp.registerTool`、`amp.registerCommand`、`ctx.ui.*`、`amp.ai.ask`;`ask_user_choice` 已于 2026-09 内置。
- **read_thread(2026-07-02):** 子 agent,会核对后续是否撤销;已用 GLM 5.2;最长线程已压缩 68 次以上。
- **安全页(2026-11-10)中的个人相关项:**
  - 模型训练默认关闭,个人设置里可开(Enterprise 永不可开)。
  - 凭据存储在 `~/.local/share/amp/secrets.json`。
  - 客户端尽力避免读取 `.env`。
  - 个人线程可标私密。
  - 泄露令牌用 `POST /api/revoke`。
  - Minimal Data Retention 对所有用户可用。
  - 个人 passkey 开关 "Use Passkey Authentication":远程控制前要求有效的 passkey 会话。

---

## 3. 安全模型(单用户视角精简)

1. **凭据:** 线程身份(短期、自动轮换)取代账户 API key;OIDC 取代长期密钥;三级密钥个人最高优先;shell 输出全局遮蔽;私密对话框输入不进线程。
2. **阶段隔离:** setup 阶段无个人和线程身份,只有 project/workspace 身份;resume 阶段才注入运行时凭据;快照保存前清理请求凭据。
3. **提交签名:** 只为已验证邮箱签名;Git 身份与 Amp 账户邮箱解耦。
4. **Portal:** 默认仅线程可见者;公开为限时、按端口、需批准;应用侧靠 `X-Amp-*` 头做授权;owning orb 内访问自己的 portal 绕过 Amp 认证(不绕过应用认证)。
5. **Profiles:** 静态加密,只有自己的 orb 线程可读,不保存 Amp 或 portal 登录。
6. **Webhook:** URL 即凭据;自己验签;事件正文是不可信输入。
7. **托管侧数据:** 暂停 orb 的文件系统与内存静态加密保存,直到线程删除;删除线程即删除 orb(通常数秒);项目快照至多复用 72 小时,被替换的快照要等所有派生 orb 删除后才真正删除;传输 TLS 1.2+,静态 AES-256;e2b 为 Amp 托管 orb 提供算力并看得到代码数据。
8. **自托管数据外流:** 见 2.15。
9. **全局密钥遮蔽(安全页 Secret Redaction):** 在最底层检测并替换为 `[REDACTED:amp]`,尽力而为,对非标准或编码的密钥无效。
10. **`[团队功能·暂不做]`:** SSO/SCIM、审计日志、SOC 2 控制措施(Amp 自己 push-to-main + 强制签名 + CI + 提交链回线程)、admin 强制 passkey、Self-Hosted Only 模式、service account、workspace 级 Git 身份规则。

---

## 4. 团队功能一览 `[团队功能·暂不做]`
- Workspace、Groups、Multiplayer、线程可见性与对他人分享、Huddle、Space 的多人形态、Service Accounts、共享 runner、共享 box、Slack 团队集成。
- 企业管理(SSO/SCIM/审计日志/数据保留/Workspace Entitlements)。
- workspace 默认 portal 域名、Workspace Resume Script、Orb Network Routing 的 admin 开关、Self-Hosted Only 与 Personal Orb Providers 开关、Allow Users to Share Threads and Portals Outside the Workspace。
- GitLab spawner 的 service account 用法。
- 这些功能的本轮已读事实见上一版报告,此处不再展开。

---

## 5. 对 Oryn 的启示(单用户)

1. orb 契约取最小三作业 `spawn/pause/delete` + `ORB_SNAPSHOT_ID` + 短期 `ORB_TOKEN`;provider 只出站连接。
2. 暂停用两个计时器(agent 空闲 5 分钟、用户交互 20 分钟取较晚者),portal 请求计入交互。
3. 快照键 `(项目, 尺寸)`,命中条件 = base HEAD + 共享配置哈希;过期则"旧快照上增量重跑 setup";设最大年龄(Amp 为 72 小时)让基础镜像更新传播。
4. 对无法读 base HEAD 的情形回退为每成员(单用户即每人)快照 + 每次 setup,并提示会变慢。
5. setup 超 2 分钟线程内警告,超 20 分钟截断且不发布快照;setup 失败也不发布快照。
6. 生命周期脚本三分:`setup`(幂等可缓存)、`resume`(≤10 秒,每次唤醒)、`services`(受监督常驻);脚本退出后清理残留进程。
7. 克隆超时(15 分钟)和 Shallow Clone 做成项目级设置,默认全历史以保证 `git blame` 可用。
8. spawn 令牌桶(突发 20,之后每 5 分钟 1 个),超限排队并显示预计等待,不报错。
9. 凭据分层:setup 仅 project 级身份,个人和线程身份只在 resume 注入;保存快照前清理请求凭据。
10. 自建 OIDC issuer,claims 含 workspace/project/user/thread,`sub` 四级范围;TTL 默认 10 分钟。
11. 密钥在 orb shell 输出层面做全局值替换,保存时拒绝过短或常见词;向用户要密钥走私密对话框,不进线程。
12. services 层照搬 `PORT`/`PUBLIC_URL`/`health` 约定,portal 请求触发 `ensure`;WebSocket 经代理时要设计重连,不要承诺长连。
13. portal 默认仅所有者可见,公开是限时、按端口、需批准的特例;向应用注入身份头由应用自行授权。
14. Profiles 的冲突规则(退出登录总赢、同条目先到先得)值得照搬;passkey 中转靠桌面客户端的 Bluetooth。
15. webhook:先落库再 200 → 至少一次投递 → 幂等键 → 指数退避 → 1 小时丢弃;URL 视为凭据;不替第三方验签。
16. 自动化:一线程一个调度 + snooze 静默规则("无变化"不打扰)适合单用户通知设计。
17. 用量记录以"orb 运行毫秒 + 尺寸 + 线程"为基本粒度,同时提供 Web 页和 CLI(`usage --details`),并让 agent 能解释;Oryn 不做计费也应保留明细。
18. macOS 采用"Linux 主 orb + 按需子机"拆分,子机独立租约(用完保留 20 分钟复用缓存)。
19. 自托管边界必须说清:推理在服务端 → 文件内容、命令输出、diff、截图仍外流;Oryn 若自己做推理,这条边界反而可以收回。
20. 不复刻:handoff、fork、TODO、旧弹窗权限、TUI 侧边栏、编辑器扩展、旧模式名、Morbs。

---

## 6. 已读/未读(沿用上一版清单,摘要)
- **本轮读到正文的公告:** agents-in-orbs、secrets-of-the-orb、more-orb-sizes、size-the-orbs-of-production、portals、event-driven-orbs、macos-satellites、self-hosted-orbs、personal-self-hosted-orbs、mcp-in-orbs、explain-usage、multi-repo-projects、no-mailmap-required、service-accounts、boxes、gitlab、read-bigger-threads、the-coding-agent-is-dead、neo、ask-to-handoff、stick-a-fork-in-it、slashing-custom-commands、todos-are-done、tab-tab-dead。
- **笔记与站点页:** what-are-orbs、notes/what-i-want-to-tell-you-about-orbs、thats-not-soc-2-compliant、pave-the-road、security(前 300 行)、morbs。
- **索引:** chronicle、news、sitemap.xml。
- **现行文档:** orbs、orbs/customizing(全)、optimizing、sizes-and-costs、portals(全)、handling-secrets(大部分)、files-and-attachments、shipping、mac、profiles、automations、self-hosted、self-hosted/kubernetes、egress-ips、cli/runners(前 300 行)。
- **早先已读(本轮未重读):** 见上一版清单。
- **未读:** orbs/getting-started、orbs/agent-to-agent、docs/boxes、apps/*、integrations/github|gitlab|google、customize/mcp|plugins、plugin-api、api/external、cli/clone、cli/spawning-orbs、enterprise/*(团队功能,按约束不深挖)、time-capsules/1~6、security 页后 75 行。
- **「未找到」:** prewarm 池独立说明与容量策略;系统级资源指标 UI;workspace 分析 API 细节;"orb 内存告警"功能发布状态。

### Critical Files for Implementation
本任务只读调研,不涉及仓库文件修改。设计 Oryn 时最关键的对照来源页:
- `https://ampcode.com/docs/markdown/orbs/customizing` — 生命周期、项目快照、setup/resume 契约与时限。
- `https://ampcode.com/docs/markdown/orbs/self-hosted` — dispatch 契约、数据外流边界、provider 约束、Public Portals。
- `https://ampcode.com/docs/markdown/orbs/handling-secrets` — OIDC、线程身份、密钥分层、遮蔽。
- `https://ampcode.com/docs/markdown/orbs/portals` — services.yaml、portal 认证头、域名、Desktop。
- `https://ampcode.com/docs/markdown/orbs/sizes-and-costs` — 尺寸、暂停语义、计费与保活。
# 公开分享与公开入口专题(单用户范围补充)

本节是对前两版的**增补**。前两版的单用户内容仍然有效,团队协作类仍只带过。状态标记沿用 `[现行]`/`[已移除]`/`[已被取代→新形态]`,依据日期取页面 `lastModified` 或公告日期。**亲自读到的才算事实,推测标 `[推测]`,读不到写「未找到」。**

## 0. 结论速览

| 公开入口 | 状态 | 一句话 |
|---|---|---|
| 线程 **Unlisted** 链接 | `[现行]` | 拿到链接的任何人可打开;不可被搜索发现;公开可发现线程已被删除。 |
| 线程 `.md` 导出 | `[现行]` | URL 加 `.md`;Export 菜单另有 JSON(仅创建者)。 |
| Portal 公开访问 | `[现行]` | 限时(1h/3h/1d/7d)、按端口;可续期或终止;未登录者可访问。 |
| 自定义/友好主机名 | `[现行]` | `amp orb portal <port> --hostname`;域名在 Domains 页验证。 |
| 附件/文件"发布"为公开 URL | `[现行]` | agent 可发布文件;带 **Public** 徽章;普通附件仍受访问检查保护。 |
| Artifact "Anyone with the Link" | `[现行]` | 未登录可只读;可导出 Markdown/HTML/PDF。 |
| 公开只读代码访问(Amp 托管仓库) | `[现行]` | 任何人可 clone 和读取文件,但看不到也不能发起线程。 |
| 只读 Git 凭据 | `[现行]` | 给机器用的只读 clone 凭据,可设过期,可吊销。 |
| Box 公开应用 | `[现行]` | 应用 URL 默认公开;`--private` 切换;自定义域名;滥用可被封。 |
| 公开用户资料页 `ampcode.com/@name` | `[已被取代]` | 仍显示活动,但**不再显示线程**。 |
| 公开可发现线程 / Social Coding | `[已移除]` | 2026-06-02 删除。 |
| "Amp Store"/公开项目市场 | **未找到** | 本轮在文档和搜索中未发现任何官方入口。 |

---

## 1. 线程公开分享

### 1.1 概念与时间线
- **2025-11-04 Social Coding:** 上线公开资料页和公开线程。
- **2026-06-02 The End of Public Threads `[已移除]`:**
  - 删除"公开、全网可发现"的线程分享。
  - 保留 workspace 内共享,以及 `Unlisted`(靠不可猜 URL 分享给任何人)。
  - 已存在的 `Public (Discoverable)` 线程自动变成 `Unlisted`,所以博客里的线程链接不会失效。
  - 公开资料页保留活动,但不再展示线程。
  - **设计理由(官方原话要点):** 越来越难审查线程里是否夹带敏感文件片段;每次模型更新,agent 会把更多文件读入上下文;"这个决定是主动的,不是被某次事件触发"。
  - 来源:https://ampcode.com/news/end-of-public-threads · https://ampcode.com/news/social-coding
- **现行文档(threads,2026-11-10):** 可见性共四档。

| 档位 | 谁能打开 |
|---|---|
| **Private** | 你自己(workspace 管理员可经审计访问)`[团队功能·暂不做]` |
| **Workspace** | 工作区全体成员 `[团队功能·暂不做]` |
| **Group** | 指定组成员,仅 Enterprise `[团队功能·暂不做]` |
| **Unlisted** | 互联网上拿到链接的任何人 |

  来源:https://ampcode.com/docs/markdown/threads

### 1.2 URL 形态与设置入口
- 线程 URL:`https://ampcode.com/threads/T-…`(`T-` 后接 UUID,如 `T-6600f272-b20f-4652-bb6a-3a3c73099d31`,见 OIDC 样例)。
- **UI:** 标题栏 **Share** → **General Access**,选择"仅列出的人/组"、"整个 workspace"或"Anyone with the link";也可从命令面板 `Share Thread...`。
- **CLI:** `amp threads share T-… --visibility unlisted`;TUI 里 `Ctrl+O` → `set visibility`。
- **没有 workspace 时:** 新线程默认私有;分享对话框提供 **Create Workspace**。有 workspace 时:workspace 所有的项目里线程默认共享给 workspace,个人项目或无项目的线程默认私有。
- **与 multiplayer 互斥:** 线程设为"任何有链接者可看"时 multiplayer 自动关闭,并且在此状态下无法开启 `[团队功能·暂不做]`。

### 1.3 未登录访客看到什么(能确认和不能确认的)
- **能确认:**
  - 隐私政策写明 "shared thread and gift code pages" 属于 URL 里含私有标识符的页面,站点分析不记录该标识符。https://ampcode.com/privacy-policy(2026-11-03)
  - 线程内容包含"提示词、回复、**每一次工具调用**和改过的文件"(threads 文档对线程的定义),因此可见内容的范围在设计上包括工具调用。
- **未找到(本轮读不到,不能凭推测断言):**
  - 未登录访客在 Unlisted 页上具体看到哪些面板(Changes、Files、Terminal 是否对访客开放)。
  - 页面是否只读(默认应只读;文档只明确说"Anyone with the link: 可以打开"和"该状态下 multiplayer 不可用")。
  - 页面是否对未登录访客**实时更新**。
  - 线程页是否带 `noindex`。`robots.txt` 只 Disallow `/feed`、`/api/`、`/settings`、`/workspace` 等,**没有 Disallow `/threads`**。所以挡住搜索引擎收录很可能依赖页面 meta 或"不可发现"(没有任何公开列表链接),本轮无法确认。https://ampcode.com/robots.txt
  - 撤销分享(改回 Private/Workspace)后旧链接的具体表现。[推测] 应变为需要登录或拒绝访问,与"Artifact 公开链接在管理员关闭开关后立即失效"的行为一致(见 3.2),但线程本身未见官方明确描述。
- **Admin 的访问是审计式访问:** 管理员打开非本人可见的线程需要再次确认身份,页面显示审计提示并记录审计日志,artifact 此时只读 `[团队功能·暂不做]`。

### 1.4 `.md` 与 JSON 导出
- 在线程 URL 后加 `.md`(`https://ampcode.com/threads/T-….md`)读取 Markdown。
- 线程 More Actions → **Export**:Markdown;**完整 JSON 仅创建者**可导出。
- CLI:`amp threads markdown T-…`、`amp threads export T-…`。
- 文档没有说明 `.md` 对 Unlisted 访客是否同样可访问。[推测] 取决于该线程的可见性设置。

### 1.5 搜索与发现
- `visibility:private|workspace|group|unlisted` 搜索过滤器只缩小**你本来就能找到**的线程集。
- **"Unlisted 线程若不在你的账户和工作区内,则始终不可被发现"**——这是 Unlisted 的核心安全语义:靠不可猜 URL,而不是靠访问控制名单。https://ampcode.com/docs/markdown/prompting
- 线程内容被引用:粘贴 URL 或 `@T-…`,agent 会读取该线程(`read_thread` 子 agent)。引用别人的 Unlisted 线程是否可行,文档未写。

### 1.6 提交里的溯源链接
- agent 的提交带 `Amp-Thread-ID` 尾注,`git log` 可以回到对话。
- PR 描述里放线程链接,让评审者看到上下文。
- **启示:** 如果你把 PR 公开,而线程是 Private,外部读者点进去会被挡住;因此"发布 PR 前先把线程设为 Unlisted"是一个实际的操作决策点。

---

## 2. Portal 公开访问 `[现行]`(portals 文档 2026-11-10;docs/orbs/self-hosted 2026-11-11)

### 2.1 开启与时长
- 入口:portal 工具栏 **⋯ → Portal Options → Access → Make Public For**,可选 **1 小时 / 3 小时 / 1 天 / 7 天**。
- 公开期间,任何有链接的人**无需登录 Amp**即可打开。
- **作用域:** 只对**该端口**生效;公开链接**不**授予线程访问权,也不暴露线程的 review 工具。
- **续期/终止:** 公开期间 Access 面板显示截止时间;可在 **Keep Public For** 选新的时长,或选 **End Public Access**。
- **agent 路径:** `manage_amp` 工具,topic `portals`,operation `set-public-access`(以及 `list`);**公开或延长公开前必须弹窗征得用户批准**。CLI 无法修改访问,`amp orb portal list` 可看哪些 portal 已公开及到期时间。

### 2.2 URL 与主机名
- 默认 URL:`https://t-<thread>-p<port>.onamp.dev`(生成子域名)。
- **友好主机名:** `amp orb portal <port> --hostname <name>--<namespace>.onamp.dev`(托管域名),2026-08-23 公告"Friendly URLs for Sharing Orbs"引入(早先已读,具体措辞本轮未重读)。
- **自定义域名:**
  - 在个人或 workspace 的 **Domains** 设置页添加域名,按页面提示创建 DNS 记录,等待状态变 Active。
  - 之后可 `amp orb portal <port> --hostname <host>`。主机名**仅对该端口**有效。
  - 域名也可被设为 workspace 默认(形如 `t-<thread>-p<port>.portal.example.com`),个人域名只用于手动分配的主机名 `[团队功能·暂不做]` 的默认部分。
  - 自定义主机名在线程归档后保持分配;换线程要用 `--take-over`。
  - 要用同一个主机名服务 box 上的应用,用 `amp boxes deploy --domain <host> --take-over`;portal 不能反向抢回 box 的主机名。

### 2.3 公开 portal 的安全措施
- **时限与端口隔离:** 见 2.1,这是最关键的一层。
- **用户确认:** agent 要公开或延长必须经用户批准对话框。
- **一键终止:** 随时可 End Public Access。
- **runner portal 不能公开,也没有 review 按钮。** https://ampcode.com/docs/markdown/cli/runners
- **自托管 orb 默认关闭公开 portal。** 原因(官方原话要点):公开 portal 是一条"通向你网络的匿名隧道"。需在 provider 编辑对话框里单独打开 **Public Portals**,关闭则一次终止该 provider 所有公开访问。
- **workspace 开关** "Allow Users to Share Threads and Portals Outside the Workspace" 优先级更高 `[团队功能·暂不做]`,个人场景可忽略。
- **应用侧身份头 `X-Amp-Authenticated`:** 对未登录访客取值 `amp-user=no, workspace-member=no, collaborator=no`。**应用必须自己决定未登录用户能做什么**——Amp 的 portal 访问控制和应用自己的登录是分开的两层。
- **Review widget 在公开 portal 上的行为:** 官方文档只说 review widget 默认启用,可用 `review: false` 或响应头 `x-amp-review-widget: off` 关闭。**未写**匿名访客是否能看到并使用它。[推测] 评论需要登录 Amp 才能发送。
- **WebSocket/长连限制:** 经 Amp 服务器代理,部署时断开(code 1006);不适合作为长期公共服务,长期服务应放 box。

### 2.4 访问与计费的关系
- 任何授权的 portal HTTP 请求(包括公开访客的请求)会**唤醒暂停的 orb** 并开启新的 20 分钟交互窗口(sizes-and-costs 2026-11-03)。
- **启示:** 公开 portal 等同于允许陌生人让你的 orb 持续运行并计费。限时设计既是安全措施,也是成本控制。

---

## 3. 文件、附件、Artifact 的公开形态

### 3.1 附件与发布文件 `[现行]`(files-and-attachments 2026-10-30)
- **默认私有:** 附件"对线程私有,不会自动复制进仓库"。
- **三种形态:**
  - 普通附件:受 Amp 访问检查保护。
  - **Public** 徽章:表示该文件由 Amp **发布**过,任何有 URL 的人可访问。
  - **Temporary** 徽章:几小时后过期。
- **如何发布:** 让 agent 做,如 "Publish the finished demo video so I can share it outside this thread."
- **官方警告:** "只在文件对任何持 URL 的人都安全时才发布。"
- **管理:** 线程菜单 **Attachments** 列出线程里所有文件;能删除线程的人可以逐个或全部删除附件(附件是副本,删除不影响 orb 文件系统)。
- **用途示例(projects 文档):** 在 AGENTS.md 里写"用 `gh pr create` 创建 PR,在描述里嵌入 screenshot/video 的 **public artifact URL**",用于 PR 里的截图和视频。
- **未找到:** 公开 URL 的具体形态、是否带不可猜令牌、是否有过期设置(除 Temporary 外)。

### 3.2 Artifact 的 "Anyone with the Link" `[现行]`(threads 文档,2026-11-10;公告 2026-11-02)
- **分享入口:** Artifact 的 **Share → General Access**,三档:
  - **Same Access as Thread**(默认):与线程可见性联动,后续变化会跟随。
  - **Workspace Members** `[团队功能·暂不做]`。
  - **Anyone with the Link**:任何人,**无需登录**,只读。
- **编辑权限:** 只有能打开线程且属于该 workspace 的人,或个人线程的所有者,可以编辑。
- **关闭公开:** workspace 管理员关闭 *Allow Users to Share Threads and Portals Outside the Workspace* 或 *Allow Public Artifact Uploads* 后,**已有公开链接立即失效** `[团队功能·暂不做]`(但说明了撤销语义:失效是即时的)。
- **导出:** Export 标签可下载 Markdown/HTML,或打印 PDF。
- **限制:** 单个 artifact 最大 2 MB;每次保存产生新修订;嵌入的图片**保存在线程里,只有能打开线程的人才看得到**——通过公开或 workspace 链接访问的读者会看到**破图**。这是一个会影响"公开分享体验"的重要坑。
- **删除:** 删除 artifact 会保留其图片直到线程被删除;删除线程即删除其 artifacts。

---

## 4. 公开只读代码访问与只读 Git 凭据 `[现行]`(projects 2026-11-05)

- **Public Read-Only Code Access:** Amp 托管仓库(`Start From Scratch` 创建的项目)的项目设置里有此开关。开启后"互联网上的任何人都可以 clone 仓库并读取其文件;**线程保持私有**"。权限说明:"公开只读访问让他人能 clone 和读代码,但不能发起线程,也不能看到线程。"
- **URL 形态:** `https://ampcode.com/@owner/project-name`(git clone 时形如 `https://amp:<credential>@ampcode.com/@owner/project-name`)。
- **只读 Git 凭据**(用于构建服务器、CI、GPU 机器):
  - 项目设置 → **Read-only Git Credentials** → **Create Credential**,命名,选择有效期 **7/30/90/180 天或 Never**。
  - **只显示一次**的 "Authenticated Clone URL";HTTP Basic 认证,用户名被忽略,密码是凭据。
  - 只能读该项目仓库,不能 push;在同一处删除即吊销。
  - 把个人项目转入 workspace 会删除其全部凭据。
  - **坑:** Git 会把含凭据的 URL 存进 `.git/config`。
  - 只能取到**已 push**的提交;要拿 orb 里还没 push 的工作,用 `amp sync --apply`。
- **`amp clone owner/project-name`** 用于克隆 Amp 托管项目(docs/cli/clone 本轮未读)。
- **Amp 托管仓库的 Post-receive Webhook:** 每次成功 push 向你给定的 URL 发 JSON POST。

---

## 5. Box 公开应用与预览 `[现行]`(boxes 文档 2026-11-11)

### 5.1 访问模型
- **每个应用一个 URL:** `https://<app>--<namespace>.onamp.dev`(例如 `bug-tracker--acme.onamp.dev`)。
- **默认公开:** "an app's URL is public by default, so anyone with the URL can open it"。
- **`--private`:** 限制为 box 所有者(或所属 workspace 成员),访客先在浏览器登录 Amp;**没有该登录的请求(如 webhook)到不了私有应用**。后续部署保留该设置,并写入 `.amp/box-deploy.json` 里的 target。`--public` 可改回。
- **Amp 不告诉应用访问者是谁:** 需要用户身份或应用是公开的,应用得自己处理登录(需要时可用 "Sign in with Amp" OAuth client,在自定义域名下要补 redirect URL)。
- **workspace 开关** Allow Public Box Apps / Allow Users to Create Boxes `[团队功能·暂不做]`。

### 5.2 自定义域名
- 在 Domains 页添加域名、配置 DNS、等待 Active;`amp boxes deploy --domain portal.example.com`(可以是域名本身或其直接子域)。
- 原 `onamp.dev` URL 会**重定向**到新主机名;应用在 `AMP_DEPLOYMENT_URL` 里看到新主机名;`--no-domain` 恢复。
- 从 portal 迁移主机名:先不带 `--domain` 部署并验证,再 `--take-over`;切换在部署开始时发生,"让持有链接的人永远不会落到空应用上"。

### 5.3 公开预览(Previews in the Rail)
- 应用被固定到导航栏后,悬停图标可显示实时预览(触屏不显示)。
- 契约:页面 `<head>` 里放 `<meta name="amp-preview" content="v1; enabled; iframe" />`;Amp 用**不带 cookie** 的服务端请求探测该标签,每隔几分钟重查一次。
- 预览以 `amp-preview=light|dark` 查询参数加载,在 **384×240** 的 iframe 里;**不可交互**;沙箱化(不能弹窗、提交表单、导航 Amp、用摄像头麦克风);feedback widget 在预览里被排除。
- 私有应用的预览用查看者的 box 登录打开;应用自己的 cookie 默认带不进 iframe(除非 `SameSite=None` 且分区)。
- 每次悬停都会加载,要求请求快速且无副作用。

### 5.4 Feedback Widget 在公开站点上的暴露面
- `amp boxes deploy --widget` 为应用加入 widget;**公开应用上 widget 默认隐藏**,按 `Cmd+.`(Ctrl+.)或在手机上快速点三下才显示;发送评论**始终需要**以 box 所有者(或 workspace 成员)身份登录 Amp。
- 普通站点用脚本标签:`<script src="https://ampcode.com/w/client_….js" async>`(个人项目是以 `pw_` 开头的 widget key);用 `?amp` 打开页面激活。
- **Allowed Origins:** 必须 `https://`(本地开发允许 `http://localhost`、`127.0.0.1`、`[::1]`);拒绝危险通配符(`*.co.uk`、`*.github.io`、`*.onamp.dev`、已激活的自定义 portal 域名下的通配等),并依据 Public Suffix List 判断;CSP 需允许 `https://ampcode.com` 的 `script-src` 和 `frame-src`。
- **安全告警(原文要点):** 若攻击者控制了你的站点,可以往成员发送的评论里塞隐藏指令;这些评论会以发送者的名义启动线程,而该线程的 agent 可用项目的仓库和密钥。只在你控制且保持更新的站点上加脚本;怀疑被入侵就点 **Turn Off**。个人项目关闭后要重新 Enable 并替换脚本标签才会恢复。
- **限制:** 每次发送新开一个线程;agent 在 orb 里改代码,看不到线上数据;widget 只在所有外层页面都在允许来源上才会打开(被他站 iframe 嵌入时不打开)。
- 来源:https://ampcode.com/docs/markdown/feedback-widget(2026-11-11)

---

## 6. 公开入口的安全措施汇总

| 措施 | 适用入口 | 依据 |
|---|---|---|
| **不可猜测 URL**(能力 URL) | Unlisted 线程、artifact 链接、webhook URL、portal 默认主机名 | "share ... by unguessable URL"(end-of-public-threads);"Unlisted threads outside your account and workspaces remain undiscoverable"(prompting) |
| **不可发现** | Unlisted 线程 | 搜索只在你已有权限的范围内;公开可发现线程被删除 |
| **限时** | Portal 公开(1h/3h/1d/7d);Temporary 附件;只读 Git 凭据(7~180 天或 Never) | portals / files / projects |
| **端口/范围隔离** | Portal 公开只覆盖一个端口,不授予线程或 review 工具 | portals |
| **用户批准** | agent 公开 portal、延长公开、添加 widget 允许来源、关闭 widget | portals / feedback-widget |
| **即时撤销** | End Public Access;删除凭据;关闭 widget 立即删除 OAuth app;管理员关闭开关后公开 artifact 链接立即失效 | 同上 |
| **默认关闭的高风险面** | 自托管 orb 的 Public Portals;公开 portal 的 widget 隐藏;box 应用默认公开但可 `--private` | self-hosted / boxes |
| **审计** | 管理员访问私有内容记审计日志 `[团队功能·暂不做]`;线程本身保留提示、工具调用、附件作为提示注入审计轨迹 | threads / security |
| **滥用处理** | box 应用必须遵守 AUP;报告 `security@ampcode.com`;Amp 可**封禁 box**——所有 URL(含私有应用和自定义域名)显示"应用不可用"页,磁盘和数据保留,不能被所有者启动;邮件通知所有者并要求回复修复后解封 | boxes / security(security 页写明"向 security@ampcode.com 报告 onamp.dev 上的钓鱼或恶意应用") |
| **AUP 禁止内容** | 滥用/欺诈、CSAM、非法内容、性露骨、暴力仇恨威胁、对法律受保护事项的自动判定等 | https://ampcode.com/terms/aup(2026-09-10) |
| **内容审查/举报** | 对**线程/artifact/附件**这类公开内容的举报入口:**未找到**。仅 box 与 `onamp.dev` 应用有明确举报渠道。 | — |
| **隐私** | 站点分析不记录分享线程页 URL 中的私有标识符 | privacy-policy |
| **工具链** | 提示词/输出中的密钥自动遮蔽为 `[REDACTED:amp]`,避免进入线程和公开页面;但此为"尽力而为",泄露后建议轮换 | security |

**一个关键约束的原因:** 官方撤销"公开可发现线程"的理由是"无法可靠审查线程里是否夹带敏感内容"。因此 Oryn 设计公开分享时,应当把"分享前的敏感信息审查"当作一等问题,而不是事后补充。

---

## 7. 单用户视角的整合与对 Oryn 的启示

1. 公开分享的**主模型是"能力 URL"**:`Unlisted` = 不可猜 URL + 不可被发现,没有名单型 ACL。Oryn 可以用高熵 ID(如 UUID/随机令牌)加"不进任何索引/搜索/资料页"来复刻,**不要**做"公开可发现"目录。
2. 线程可见性三态(Private/Unlisted,个人版省去 Workspace 与 Group)就够用;切到 Unlisted 时必须同步处理"协作写入"(Amp 里与 multiplayer 互斥)。
3. 分享对话框的 **General Access** 应同时用于线程和 artifact:artifact 默认"跟随线程",也允许独立设置公开。
4. 线程要有 `.md` 导出和 CLI 导出(`markdown`/`export`);完整 JSON 只给所有者,公开读者只拿渲染后的文本。
5. 访客页必须明确**只读、不显示 Terminal/Files**。Amp 对此没有文档,Oryn 需要自行定义:建议默认只展示消息、工具调用摘要、diff,不开放执行环境。
6. 公开链接撤销应**立即生效**:参照 Amp artifact 的行为,撤销后旧链接马上失效,而不是等缓存过期。
7. **Portal 公开用"限时 + 单端口 + 需批准"**:时长档位 1h/3h/1d/7d 值得照搬;终止和续期放在同一个 Access 面板;agent 请求公开时必须由用户确认。
8. 公开 portal 请求会唤醒 orb 并计入交互窗口:Oryn 需要限制公开 portal 的唤醒频率,否则等于允许陌生人让你的机器持续运行。
9. 自托管/本地 provider 默认**关闭**公开 portal,因为那是通向本地网络的匿名隧道;需要时按 provider 单独开启。
10. 应用侧要拿到"是不是登录用户"的信号:`X-Amp-Authenticated` 风格的头,未登录时显式给出 `no`;应用自己负责授权,代理层不替它做细粒度控制。
11. **发布文件**要区分三类:私有附件、Temporary(自动过期)、Public(显式发布并带徽章);发布动作应由 agent 发起但让用户可见,且每个文件都能在"附件"列表里单独撤销。
12. Artifact 的图片存储与访问策略要和 artifact 一致,否则公开读者会看到破图(Amp 的已知坑)。
13. 公开只读代码访问和线程可见性应当**相互独立**:能 clone 不等于能看线程;给机器用的只读凭据用"一次性显示 + 可选过期 + 可吊销 + 仅读",并避免把凭据落进 `.git/config` 之外的更多位置。
14. Box 的公开应用:默认公开可能出人意料,Oryn 应考虑默认私有、公开需显式 `--public`;私有模式要明确说明 webhook 等无登录请求无法到达;自定义域名切换要保证旧 URL 重定向,且切换瞬间无空窗。
15. 预览契约(`<meta name="amp-preview">`)是一个小而完整的设计:不带 cookie 的探测、固定尺寸 iframe、沙箱、不可交互、与 widget 互斥。若 Oryn 要在侧栏显示应用预览,可以直接参照。
16. Feedback widget 的攻击面(被入侵站点向 agent 注入指令)要在产品里说明,并提供一键关闭;widget 在公开页面上默认隐藏、需快捷键才显示,是一个值得照搬的低成本防护。
17. 滥用处理需要一个"封禁但保留数据"的状态:对外统一显示不可用页,所有者不可自行重启,解封需沟通;并为所有公开面(不仅 box)提供举报邮箱或入口——Amp 对线程/artifact 的举报路径**未找到**,Oryn 可以补上。
18. 公开前的敏感信息检查是官方撤销"公开可发现线程"的核心理由:Oryn 在切换到 Unlisted 前,可以提示"此线程曾读取过以下类型的文件/包含已遮蔽的密钥",而不是直接放行。
19. 公开资料页与"社交编码"已被撤销:不要复刻"公开用户主页展示线程"。
20. "Amp Store/公开项目市场"本轮**未找到**任何官方入口,不要把它列入复刻范围,除非另行获得证据。

---

## 8. 已读与未读(本轮新增)

**本轮新读到正文:**
- https://ampcode.com/docs/markdown/threads(Sharing、Export、Artifacts 到 Share an Artifact)
- https://ampcode.com/news/end-of-public-threads
- https://ampcode.com/news/social-coding
- https://ampcode.com/news/artifacts
- https://ampcode.com/docs/markdown/boxes(全文,含 Public and Private Apps、Custom Domains、Previews in the Rail、Reporting Abuse)
- https://ampcode.com/docs/markdown/projects(含 Public Read-Only Code Access、Read-only Git Credentials、Permissions)
- https://ampcode.com/docs/markdown/spaces(Space 的可见性仅为 workspace/Only You;无公开形态)
- https://ampcode.com/docs/markdown/feedback-widget(全文)
- https://ampcode.com/docs/markdown/enterprise/workspace-thread-visibility-controls
- https://ampcode.com/terms/aup
- https://ampcode.com/privacy-policy
- https://ampcode.com/security(第 300~375 行,漏洞赏金)
- https://ampcode.com/robots.txt
- grep:docs/tools、docs/prompting、docs/orbs/getting-started、docs/cli、docs/puck 关于 public/publish/unlisted 的片段;一次网络搜索确认没有官方 "Amp Store"。

**仍未读/未找到:**
- 未登录访客在 Unlisted 线程页上的具体可见面板、是否只读、是否实时更新、是否 `noindex`、撤销后旧链接的具体表现——**未找到**官方文字。
- 已发布文件的公开 URL 具体形态与过期设置——**未找到**。
- 线程/artifact/附件的内容举报入口——**未找到**。
- 2026-08-23 `friendly-urls-for-sharing-orbs` 的具体措辞(早先已读,本轮未重读)。
- `docs/cli/clone`、`docs/collaborate/workspaces`、`docs/enterprise/*` 等。
- Space 的"公开"形态:Spaces 文档只提供 `Only You` 与 workspace/groups 两种共享范围,**没有公开链接形态**。

### Critical Files for Implementation
本任务是只读调研,不涉及仓库文件修改。设计公开分享时最关键的对照来源:
- `https://ampcode.com/docs/markdown/threads` — 线程可见性、Unlisted、导出、Artifact 公开分享与图片坑。
- `https://ampcode.com/docs/markdown/orbs/portals` — Portal 公开时长、端口范围、身份头、域名、agent 批准流程。
- `https://ampcode.com/docs/markdown/boxes` — 应用公开/私有、自定义域名、预览契约、封禁机制。
- `https://ampcode.com/news/end-of-public-threads` — 撤销公开可发现线程的理由与迁移方式。
- `https://ampcode.com/docs/markdown/feedback-widget` — 公开站点上 widget 的来源限制与攻击面。
