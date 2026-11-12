# Amp「Orb 环境系统」调研报告

调研日期 2026-11-12，只读。除特别标注外，所有结论都来自我亲自读到的页面，页面末尾的 `lastModified` 为 2026-08 至 2026-11。文中 `docs/markdown/X` 与 `docs/X` 是同一页面的 Markdown 版与 HTML 版。

**范围提醒：**
- 文档中没有「Amp Net」「exit node」的专门页面，只有 Orb Network Routing（`amp net route`）。
- 文档中没有 `amp orb system-metrics` 命令，也没有 CPU、内存、磁盘实时利用率的观测面。详见「补充：统计与可观测性」。

---

## 0. 页面清单

**已读到（均为 `ampcode.com` 下）：**
- `/docs/orbs`
- `/docs/markdown/orbs/getting-started`
- `/docs/markdown/orbs/customizing`
- `/docs/markdown/orbs/optimizing`
- `/docs/markdown/orbs/shipping`
- `/docs/markdown/orbs/sizes-and-costs`
- `/docs/markdown/orbs/handling-secrets`
- `/docs/markdown/orbs/files-and-attachments`
- `/docs/markdown/orbs/portals`（含 Desktop）
- `/docs/markdown/orbs/mac`
- `/docs/markdown/orbs/profiles`
- `/docs/markdown/orbs/agent-to-agent`
- `/docs/markdown/orbs/automations`
- `/docs/markdown/orbs/self-hosted`
- `/docs/markdown/orbs/self-hosted/kubernetes`
- `/docs/markdown/boxes`
- `/docs/markdown/cli/runners`
- `/docs/markdown/cli/spawning-orbs`
- `/docs/markdown/threads`
- `/docs/markdown/projects`
- `/docs/markdown/collaborate/multiplayer`
- `/docs/markdown/pricing`
- `/api/external`（External API v2，用了 usage 相关端点）
- `/notes/orbs-explained`
- `/notes/putting-an-agent-in-an-orb`
- `/llms.txt`
- `/sitemap.xml`

**页面清单依据：**
- `llms.txt`（Orbs 章节共 15 页）和 `sitemap.xml` 一致。
- `/docs/orbs/*` 实际有 15 页，比任务给的已知 5 页多：customizing、optimizing、shipping、handling-secrets、files-and-attachments、mac、profiles、self-hosted、self-hosted/kubernetes。
- `/docs/orbs/portals` 同时包含 Desktop。

**未读到或只读了一部分：**
- `/docs/orbs/self-hosted/bundle.zip`（Helm 安装包，内含 `self-hosted.md`、`changelog.md`，我没有下载，属于只读限制）。
- 大文件的末尾：threads（读到 300/314 行）、projects（300/313 行，后面是 Permissions 节）、external API（11331 行，只 grep 了 usage 相关部分）。
- 未读：`/docs/apps/mac`（Share Network 开关）、`/docs/customize/mcp`（`${amp:id-token}`）、`/docs/cli/settings`、`/docs/integrations/*`、`/docs/egress-ips`、`/news/*`、`/what-are-orbs`、`/manual/orbs`（`putting-an-agent-in-an-orb` 引用了它，我没有读）、`/settings/*`（需登录）。

---

## 1. Orb 是什么：隔离、镜像、生命周期、快照

### 定义
- Orb 是「Amp 为每个线程创建的云端沙箱/远程机器，含代码、插件、开发工具和线程上下文」。来源：<https://ampcode.com/docs/orbs>、<https://ampcode.com/docs/markdown/orbs/getting-started>。
- 官方自述：「Yes, they are VMs, or sandboxes」。来源：<https://ampcode.com/notes/orbs-explained>。
- 隔离技术没有统一说法，按托管方式不同：
  - Amp 托管 orb 的底层 provider：只在 Tailscale 一节提到「E2B microVM orbs 可能只暴露 link-local IPv4」，和「Amp's default E2B orbs 目前跑在 GCP us-west1」。来源：<https://ampcode.com/docs/markdown/orbs/customizing>。所以 Amp 托管 orb 底层很可能是 E2B（Firecracker microVM）。
  - 自托管 K8s：gVisor 沙箱，运行时是开源的 Substrate（`github.com/agent-substrate/substrate`）。来源：<https://ampcode.com/docs/markdown/orbs/self-hosted/kubernetes>。
- 规格：
  - 所有当前规格磁盘均为 60 GB。
  - 规格表：`a1.tiny` 1C/2G；`a1.small` 2C/4G；`a1.medium` 4C/8G；`a1.large` 8C/16G；`a1.xxlarge` 16C/32G；`a1.3xlarge` 16C/44G（仅 Gigawatt/Enterprise）。
  - 项目默认 `a1.medium`。
  - 来源：<https://ampcode.com/docs/markdown/orbs/sizes-and-costs>。
- 基础镜像和预装工具：
  - Debian 12 或 13，脚本应读 `/etc/os-release` 而不是写死版本。
  - 预装：`amp`、`gh`（激活后已认证）、`glab`、Git、SSH、tmux、Bun/Node/npm/pnpm/Yarn、Python/pip/uv、Nix、Docker Engine（daemon 默认不启动，需 `sudo systemctl start docker`；Compose/Buildx 未装）、`agent-browser` 和 Chrome for Testing、ffmpeg、ImageMagick、jq、fzf、ripgrep、vim、unzip、zstd、lsof、websocat。
  - 来源：<https://ampcode.com/docs/markdown/orbs/customizing>。
  - 另一篇旧文提到预装 PostgreSQL 和 Redis（2026-08 的说法），我没在 customizing 页的当前清单里看到：<https://ampcode.com/notes/putting-an-agent-in-an-orb>。
- 环境标记：
  - 所有 orb 内进程都有 `AMP_ORB=1`。
  - 命令执行时有 `AGENT=amp`（来源：optimizing 页）。
  - 另有 `AMP_WORKSPACE_ID`、`AMP_PROJECT_ID`、`AMP_THREAD_ID`、`AMP_USER_EMAIL`、`PORT`、`PUBLIC_URL`。
- 目录约定：
  - 仓库路径 `/home/user/workspace/repo`。
  - 多仓库项目的额外仓库在 `../repos/`。
  - 预拉取脚本从 `/home/user/workspace` 运行。
  - 用户是 `user`，有免密 sudo。
  - 日志在 `/home/user/.cache/amp/logs/setup.log` 和 `resume.log`。
  - orb 专属 AGENTS.md 在 `~/.config/amp/AGENTS.md`（镜像默认自带，追加不要覆盖）。
  - agent 的制品放 `.amp/in/artifacts/`（旧文约定）。
  - 来源：customizing、optimizing、`putting-an-agent-in-an-orb`。

### 生命周期（线程级）

按文档拼出的状态流：

1. **创建**
   - 新建线程时 Amp 创建 orb。
   - 限速：每用户突发 20 个计量 orb，之后每 5 分钟新启 1 个。超限不会失败，而是排队，线程里显示预计等待时间。来源：<https://ampcode.com/docs/orbs>。
2. **准备（项目快照流程）**，见下节。
3. **激活**
   - 应用线程环境变量和 workload identity 凭据。
   - 运行 `.agents/resume`，最多等 10 秒，之后 agent 继续、hook 继续在后台跑。
   - 然后启动 agent。
4. **运行**：只要 agent 有活，orb 就不暂停。
5. **暂停**（两个条件同时满足）
   - agent 已空闲 5 分钟，且用户 20 分钟无交互。
   - 实际等于「agent 结束后 5 分钟」和「最后交互后 20 分钟」两者中较晚者。
   - 保活的工作包括：其他 agent 回传的结果、automation/webhook 唤醒、插件 `amp.system.executor.keepAlive()` 租约、排队消息。
   - agent 留下的 dev server 不会让 orb 保持唤醒。
   - 算作用户交互的：发消息，使用 Files/Terminal/Desktop 面板，对线程 portal 的任何已授权 HTTP 请求。
   - 来自 Puck 或 automation 的消息算 agent 工作，不是交互，所以只在工作结束后 5 分钟暂停。
   - 归档线程会立刻暂停。
   - 来源：<https://ampcode.com/docs/markdown/orbs/sizes-and-costs>。
6. **唤醒**
   - 触发条件：打开未归档的 orb 线程、打开 portal、发新消息、webhook、定时任务。
   - 设置里的 Orb Saver Mode 可让「仅打开线程」不再唤醒 orb。
   - 唤醒时恢复 executor 并再次运行 `.agents/resume`。
7. **销毁**：线程被删除时销毁；归档只暂停。来源：self-hosted 页 "Archiving never sends `delete`"。

### 暂停 / 唤醒如何保存状态

- Amp 托管 orb：暂停时「保存磁盘和内存，唤醒后进程继续运行」。来源：self-hosted 页 Caution 一节。
- 具体机制（内存快照还是 VM 暂停）文档未写，只说效果。
- Amp 托管 orb 上，portal 背后的服务、Terminal 会话和 Desktop 应用在暂停后都能保持。
- 自托管后端的保持能力因后端而异：

| 基础设施 | 内存 | 磁盘 | 项目快照 |
|---|---|---|---|
| Amp 托管 | 是 | 是 | 是 |
| Docker | 否 | 是 | 仅同一台主机 |
| Apple containers | 否 | 是 | 仅同一台 Mac（需 `container` ≥ 1.6.0） |
| K8s Helm chart | 否 | 是 | 是 |
| E2B | 是 | 是 | 示例里没有 |
| Modal / Vercel / Daytona | 否 | 是 | 示例里没有 |
| exe.dev | 是 | 是 | 示例里没有 |
| Boat | 不保证 | 部分（14 天） | 示例里没有 |

- 不保存内存的后端，唤醒后 portal 背后的服务要在有人再次打开 portal 时重启，Terminal 会话和 Desktop 应用丢失。
- 因此文档建议在这类后端的 `.agents/resume` 里重启本地数据库。来源：optimizing 页。

### 项目快照（project snapshot）的生成与复用

来源：<https://ampcode.com/docs/markdown/orbs/customizing> "The Orb Lifecycle"。

快照是「已准备好的 orb 的保存副本，包含仓库和 `.agents/setup` 的变更」。启动项目 orb 时：

1. 检查该项目、该 orb 规格是否有快照（每个规格各一份）。
2. 若快照命中，直接恢复并跳过 `.agents/setup`。命中条件：
   - 快照对应的 commit 等于主仓库基线分支和各附加仓库默认分支的最新 commit。
   - 项目的共享配置没变。共享配置指：workspace 与 project 的环境变量/secrets、pre-clone 脚本、pre-setup 脚本。
   - Amp 能核对 commit 的情形：GitHub.com、GitLab、使用默认分支的 Amp 托管仓库。
   - 无法核对时，恢复快照后继续走第 3 步。
3. 不命中时，从基础镜像或旧快照出发，依次执行：pre-clone 脚本 → clone/更新仓库 → pre-setup 脚本 → `.agents/setup`（在仓库根目录）。
4. setup 成功后，保存为新的项目快照。
5. 应用线程环境和 workload identity 凭据。
6. 运行 `.agents/resume`，然后启动 agent。

补充规则：
- **刷新**：基线分支有新 commit 或共享配置变化后，快照过期。Amp 恢复旧快照，在其上更新仓库并重跑 pre-setup 和 setup，保存为新快照。工作区项目常在 push 后后台刷新。其他分支的 commit 不触发刷新，所以 `.agents/setup` 的改动要合到基线分支才生效。
- **有效期**：基于同一镜像构建的快照最多复用 72 小时，期间的刷新不重置计时。之后从基础镜像重新开始，这也是新镜像版本到达项目的时机。（旧文写的是 24 小时，当前文档为准。）
- **按成员单独快照**：以下情形每个项目成员有独立快照，setup 在恢复快照后执行，并带该成员自己的个人环境变量/secrets，新 orb 启动更慢。
  - 其他 Git 主机的仓库；
  - Amp 托管仓库且设了 Base Branch；
  - Amp 读不到最新基线 commit。
- **管理**：`amp projects snapshots list <project>`；`amp projects snapshots delete <project> [--resource a1.small]`（不带 `--resource` 则删该项目全部快照）。agent 用 `manage_amp` 的 topic=`projects`、operation=`delete-snapshots`。
- **限制**：
  - setup 20 分钟超时（含 pre-setup 耗时），超时或失败则不发布新快照。
  - clone 超过 15 分钟，orb 启动失败。
  - 缓解：项目设置里开 **Shallow Clone**（Orb > Advanced）。
  - 克隆会跳过 >1 MB 的文件版本，需要时再取。
  - setup 结束时 Amp 会杀掉脚本留下的所有进程（包括 `&`、`nohup`、`setsid` 和工具自起的守护进程）。长驻进程要走 `.amp/services.yaml`，或 `sudo systemctl enable --now <unit>`（systemd 管理，会进入快照）。
  - setup 期间 orb 里有 `/run/amp/workload-identity-request-token`，setup 结束、存快照前被移除。

### 项目级三个脚本入口

- **`.agents/setup`**：可执行，需幂等，放仓库根。
- **`.agents/resume`**：见第 2 节。
- **`.agents/desktop/resume`**：Desktop 启动脚本，优先于项目设置中的 Desktop resume script。
- **项目设置里**（存在 Amp 侧而不是仓库里）：Pre-clone script、Pre-setup script。
  - CLI：`amp projects update <ns/name> --pre-setup-script-file ./pre-setup.sh`；
  - Desktop 脚本：`amp projects create|update --desktop-resume-script-file`。
- **工作区级**：Workspace Resume Script（实验性，Orbs › Advanced）在每次运行 `.agents/resume` 之前运行。
- 来源：customizing 页、portals 页。

---

## 2. Agent 与 Orb 的关系

### agent 循环跑在哪里

- Amp 服务器端跑 agent 循环和模型推理；orb 只跑 agent 的工具（读写文件、执行命令等）。
- 证据：self-hosted 页 "What Leaves Your Network"：「Amp runs the agent loop and model inference on its own servers. Your orb runs the agent's tools, such as reading and editing files and running commands.」
- 同页说明 orb 会把工具产出回传 Amp 并随线程存储：agent 读到的文件内容、命令输出、Changes 的 diff、线程附件和截图。
- 关于「thread actor」：我没有在官方文档里直接找到该名词。`putting-an-agent-in-an-orb` 的 `AGENTS.md` 目录里有 `lib/thread-protocol`、`lib/thread-client`，dev server 的环境变量里有 `THREAD_ACTORS_SERVICE_SECRET`、`THREAD_ACTORS_WEBSOCKET_JWT_SECRET`。[推测] 服务器端有每线程一个 actor、orb 通过 WebSocket 连回该线程。

### `amp orb bootstrap` 与 `ORB_TOKEN`

- `amp orb bootstrap` 在 orb 内运行（以镜像默认用户 `user`、后台分离运行），出站连接 Amp，使 Amp 能给 orb 做环境准备并执行 agent 的工具。来源：self-hosted 页。
- `ORB_TOKEN` 是「短期凭据，授权该 orb 连接其线程」，只在 spawn 任务中提供，由 dispatch 脚本传进 orb，不能当作长期密钥。
- bootstrap 之后的步骤由 Amp 驱动：clone 仓库 → pre-setup → `.agents/setup` → 启动 agent。来源：self-hosted 页 Start Your First Thread。
- orb 内的 `amp` 二进制会被 Amp 在启动时替换成当前版本。来源：self-hosted 页 Bring Your Own Image。

### orb 内常驻进程（文档中提到的）

没有单独的进程清单页，下面是零散提到的：

- executor：Amp 连接 orb 的执行进程。唤醒时「Amp restores its executor」，`amp orb restart-processes` 会重启 executor 和受管服务。
- bootstrap 进程：见上。
- 受管服务 supervisor：`amp orb services`、`amp orb service ...`，进程不会因 Amp 更新而被杀。
- tmux 共享会话：Terminal 在共享 tmux 会话中，agent 可读它的输出。
- Amp Tunnel：连接 `tunnel.ampcode.com`（WSS 443），承载 portal、Terminal、Desktop。
- Desktop 栈：systemd 单元 `amp-desktop`（改脚本后 `sudo systemctl restart amp-desktop`）。
  - 技术栈：labwc（无头 Wayland 合成器，1280x720）+ Waymote（`waymote-gateway`、`waymote-streamd`）+ Xwayland + PulseAudio + ffmpeg，`/opt/amp-wayland`。
  - 来源：runners 页（说明与 orb Desktop 同一套栈）、self-hosted 页的镜像依赖表。
- systemd：自托管 K8s 会在启动时起 Docker daemon；其他自托管后端可能没有 systemd。

### `.agents/resume` 的语义

- 在应用当前线程环境和 workload identity 之后运行，首次激活和每次唤醒各一次。
- agent 最多等它 10 秒，仍在跑则放行。
- 不要在里面装依赖，必须幂等。
- 日志在 `/home/user/.cache/amp/logs/resume.log`。

---

## 3. 自托管 Orb Provider（完整协议）

来源：<https://ampcode.com/docs/markdown/orbs/self-hosted>、<https://ampcode.com/docs/markdown/orbs/self-hosted/kubernetes>。

### 总体模型

```
amp orb provider ./dispatch.sh      # 在你的网络内长驻
  ↑ 出站 HTTPS 长连接 → ampcode.com（Amp 从不入站连接）
  ← Amp 下发 job：spawn / pause / delete
  → 每个 job 运行你的 dispatch 程序一次（无参数，参数通过环境变量传）
dispatch 在 spawn 时在 orb 内启动 `amp orb bootstrap`
orb 出站连回 Amp 的线程
```

- 只需要出站 HTTPS，Amp 无需云凭据或集群访问。也因此，自托管时不需要放行 Amp 的服务器 IP。
- 代理必须允许长连接。

### Token 与运行

- 在 Settings → Orbs（工作区）点 **Add Orb Provider**，起名（如 `our-k8s-orbs`），复制 `aop_live_…` token，只显示一次。个人也可在个人设置里加只有自己能用的 provider。
- 运行：
  ```sh
  chmod +x dispatch.sh
  export AMP_ORB_PROVIDER_TOKEN="<copied token>"
  amp orb provider /opt/orbs/dispatch.sh
  ```
  程序要用绝对路径，并建议放在会自动重启的 service manager 下。
- **每个 token 只能跑一个 `amp orb provider`**：
  - 两个同时在线会各自抢一部分 job，各自起 orb，导致线程的 orb 在一台机器上、下一个 job 却发到另一台。
  - Provider 启停时会通知 Amp，被 kill 的也会被 Amp 发现。
  - 启动时若发现另一个 provider 仍连着，有终端就请求确认；无终端就记录日志并等待对方停止。
- **`--allow-multiple`**：仅当你的 dispatch 程序在所有 provider 之间共享状态时才使用。
- 默认 provider：工作区设置里选，或 `amp orb provider set-default <name> --workspace`（需管理员）；不带 `--workspace` 则设置个人默认。
- **Where Orbs Run**（工作区管理员设置）：
  - Built-in and Self-Hosted（默认）；
  - Self-Hosted Only：新线程使用工作区 Default Orb，且必须是 provider；成员不能在内置 orb 上启动或继续线程；agent 不能起 Mac satellite；
  - Built-in Only：provider 不再收到 job，provider 和 token 保留。
- **Personal Orb Providers**：独立开关，控制成员能否在个人 provider 上跑工作区线程。
- 线程使用：新线程里点 **Orb**，在类型下拉的 **Self-Hosted** 下选 provider。CLI：`amp -ox "..." --orb-provider our-k8s-orbs [--orb-size <orb type>]`；`--orb-provider builtin` 表示 Amp 自己的 orb。
- 删除 provider 会立即吊销 token，使用它的线程的 orb 停止工作。

### Dispatch 契约

程序每个 job 被运行一次，无参数，输入全部是环境变量：

| 变量 | 值 |
|---|---|
| `ORB_OPERATION` | `spawn` / `pause` / `delete` |
| `ORB_ID` | 如 `amp-<thread id>`，用作机器名，使重复 job 找到同一台机器 |
| `ORB_IMAGE` | 仅 spawn。要启动的镜像：provider 环境里设置的 `ORB_IMAGE` 覆盖值，或 Amp 默认 |
| `ORB_SNAPSHOT_ID` | spawn 时表示从哪个项目快照启动，pause 时表示要存成哪个快照；没有则不设置 |
| `ORB_TYPE` | 仅 spawn。线程选择的 orb type 名；provider 没配 type 时不设置 |
| `ORB_TOKEN` | 仅 spawn。短期凭据，必须传进 orb |
| `AMP_URL` | 仅 spawn。非 ampcode.com 时需传进 orb |

stdin 的使用没有文档说明；输出约定：程序输出进 provider 日志，spawn 的输出也显示在线程的 setup log 里。成功与否看退出码（示例里 `set -e`，Apple 示例未知 operation 时 `exit 1`），文档没有专门描述退出码语义。

三个操作的要求：

| 操作 | 要求 |
|---|---|
| `spawn` | 让 orb 跑起来。已存在则重启；否则有 `ORB_SNAPSHOT_ID` 对应快照就从快照创建，否则从 `ORB_IMAGE` 创建。然后以镜像默认用户、分离模式在其中启动 `amp orb bootstrap`，带上 `ORB_TOKEN` 和 `AMP_URL` |
| `pause` | 停止 orb 并保留磁盘；若有 `ORB_SNAPSHOT_ID`，同时以该 ID 存一份磁盘副本供后续 spawn 使用。平台保不住磁盘就直接删 orb，下次 spawn 起新的，Amp 会重新搭建仓库。平台若还能存内存，进程在下次 spawn 后继续跑，否则随 orb 停止 |
| `delete` | 删除 orb 和磁盘。线程被删时发，归档不发 |

- 要求每个操作幂等，且在 5 分钟内完成。
- 之所以快照由 `pause` 触发：Amp 发一个带 `ORB_SNAPSHOT_ID` 的 pause，再发一个 spawn，由此保存项目快照。dispatch 忽略 `ORB_SNAPSHOT_ID` 时，每个线程都从镜像启动并跑 `.agents/setup`。

### 示例脚本原文（文档中的 Docker 版，最小可用）

```bash
#!/usr/bin/env bash
# dispatch.sh
set -euo pipefail

# "$ORB_IMAGE" is the Amp orb image.

case "$ORB_OPERATION" in
	spawn)
		image="$ORB_IMAGE"
		docker image inspect "${ORB_SNAPSHOT_ID-}" >/dev/null 2>&1 && image="$ORB_SNAPSHOT_ID"
		docker start "$ORB_ID" 2>/dev/null ||
			docker run --init --platform linux/amd64 -d --name "$ORB_ID" \
				"$image" sleep infinity
		docker exec -d --env ORB_TOKEN "$ORB_ID" amp orb bootstrap
		;;
	pause)
		docker stop "$ORB_ID"
		[ -z "${ORB_SNAPSHOT_ID-}" ] || docker commit "$ORB_ID" "$ORB_SNAPSHOT_ID"
		;;
	delete)
		docker rm -f "$ORB_ID"
		;;
esac
```

文档还给出 E2B、Apple containers、Modal、Vercel、Daytona、exe.dev、Boat 的完整示例，共性是：
- 用 `ORB_ID` 当机器名；
- spawn 用 `exec`/`run command` 以后台方式启动 `amp orb bootstrap`；
- 把 `ORB_TOKEN`（和非空时的 `AMP_URL`）作为环境变量传入；
- Modal 示例里需要显式设 `HOME=/home/user`、`USER=user`，因为它的 exec 以 root 运行。

Apple containers 示例中值得注意的细节：
- 快照会把 `sleep` 存为 entrypoint，所以 `run` 时也要显式 `--entrypoint sleep ... infinity`；
- 裸 `--env NAME` 会从宿主复制值。

### 镜像

| 镜像 | 平台 |
|---|---|
| `us-central1-docker.pkg.dev/amp-prod-0/amp-orb/amp-orb:latest` | Linux x64（默认） |
| `us-central1-docker.pkg.dev/amp-prod-0/amp-orb/amp-orb-arm64:latest` | Linux ARM64 |

- ARM64 主机需在 provider 环境里设 `ORB_IMAGE` 为 arm64 镜像。
- Docker 示例要把 `--platform` 改成 `linux/arm64`，并加 `--shm-size=1g`，因为 orb 里的浏览器需要超过 Docker 默认 64 MB 的共享内存。
- 修改 `ORB_IMAGE` 只影响新 orb，已有 orb 和快照保留旧镜像。

**自带镜像（Bring Your Own Image）的要求：**
- Linux x64 或 ARM64，glibc（Alpine 不行）。
- 需要 glibc loader：x64 为 `/lib64/ld-linux-x86-64.so.2`，ARM64 为 `/lib/ld-linux-aarch64.so.1`。
- 全部 orb 都需要：Bash、coreutils、util-linux、CA 证书；Git（`/usr/bin/git` 或 `/opt/git/bin/git`，签名提交需 ≥ 2.34）；`/usr/local/bin/amp`；默认用户 `user`（用户和组，可写 `/home/user`）；`user` 免密 sudo（`user ALL=(ALL) NOPASSWD: ALL`）。
- 按功能附加：Portals 需 Node.js ≥ 18；Terminal 需 tmux ≥ 2.9；CLI 更新需 curl；Desktop 需 `/opt/amp-wayland`（取自 Amp orb 镜像）、xwayland、dbus、gsettings-desktop-schemas、xsettingsd、wlr-randr、pulseaudio、ffmpeg、fonts-dejavu-core；建议装 ripgrep、gh、openssh-client。
- Amp 用 sudo 来配置系统 Git、准备运行时文件，以及在 systemd 镜像上管理 Amp 服务。

### Orb Types（相当于托管 orb 的 size）

- 在 provider 的 Edit 对话框的 Orb types 里添加名字（如 `small`、`large`、`gpu`），并选一个默认。
- 名字即身份，保存后不能改；描述可改。
- Amp 把线程选的类型作为 `ORB_TYPE` 传给每个 spawn；怎么映射到 CPU/内存/GPU/镜像由 dispatch 决定。
- 线程创建时就固定类型，终身不变；删除类型会让使用它的线程显示「orb type 已被移除」而不启动 orb。
- 改名流程：先加新名、改 dispatch，等没有线程使用旧名后再删。
- CLI：`--orb-size <orb type>`。

### 其他 provider 选项

- **Public Portals**：默认关闭，因为公开 portal 等于进入你内网的匿名隧道。可在 provider 的 Edit 里打开，关闭时立刻终止该 provider 下所有 orb 的公开访问。仍受工作区「Allow Users to Share Threads and Portals Outside the Workspace」约束。
- **Repository Setup**（可关）：关闭后 Amp 不 clone 仓库、不改 Git credential helper 和 `gh` 登录、不为 orb 取 GitHub token，但仍设置提交作者、跑 pre-setup 和 `.agents/setup` 并启动 agent。此时镜像必须自带 `/home/user/workspace/repo` 的 Git 检出，且设置好 `origin` 和 `origin/HEAD`；对 Amp 托管仓库不适用。

### 网络要求（出站）

| 来源 | 目的 | 用途 |
|---|---|---|
| provider | `ampcode.com` 443 | 收 job、回报结果 |
| provider 与 orb | `static.ampcode.com` 443 | CLI 更新 |
| orb | `ampcode.com` HTTPS 和 WSS 443 | bootstrap 与工具连接 |
| orb | `tunnel.ampcode.com` WSS 443 | Amp Tunnel（portal/Terminal/Desktop） |
| orb | 你的 Git 主机 443 | clone/push |
| orb | `storage.googleapis.com` 443 | 镜像里没有 `rg` 时下载 ripgrep |
| orb | `*.nscluster.cloud` WSS 443 | SSH 到 macOS satellite |

- K8s 额外：provider 到 `registry.npmjs.org`（或私有 npm 镜像）；节点到 `us-central1-docker.pkg.dev`、`registry-1.docker.io`、`storage.googleapis.com`。

### Kubernetes Helm chart

- 一个 pod 两个容器：`spawner`（跑 `amp orb provider`）和 `provider`（向 Substrate 申请 sandbox，暂停时把磁盘存到存储桶）。
- 存储：GKE→Cloud Storage，EKS→S3（IRSA），AKS→Azure Blob。
- 安装：
  ```sh
  helm install orb-provider oci://us-central1-docker.pkg.dev/amp-prod-0/orb-provider/chart \
    -n amp-orbs --set cloud=gke --set amp.url=https://ampcode.com \
    --set snapshots.location=gs://<bucket>/amp-orbs/ ...
  ```
- token 放在 Secret `amp-orb-spawner`（key=`AMP_ORB_PROVIDER_TOKEN`）里，不进 Helm values。
- `workers.types: [{name, cpu, memoryGi, min, warm}]` 要与 Amp 侧 orb types 同名；`workers.default`；`min:0, warm:0, workers.autoFill: true` 可缩到零。
- 一个 worker 需要 `memoryGi + 1 GiB` 的节点内存。
- 升级：`helm upgrade --reset-then-reuse-values`；运行中的 orb 留在旧 worker 代，在下次暂停时迁到新代。

### 数据外流（要点）

- 仓库检出、构建产物和保存的 orb 磁盘留在你的基础设施里；orb 把工具产出（文件内容、命令输出、diff、附件）发给 Amp。
- Portal/Terminal/Desktop 都走 Amp Tunnel；自托管 orb 的 portal 默认只有登录 Amp 且有线程权限的用户能打开。

### 计费（一两句）

自托管 orb 在 Hobby/Individual/Teams 免费，Enterprise 按 Amp 托管标准价计费（见第 12 节）；你另付自己基础设施的费用。

---

## 4. Portal

来源：<https://ampcode.com/docs/markdown/orbs/portals>。

- **定义**：Portal 是把 orb 内 Web 服务暴露出来的已认证 URL，在线程的 **Portal** 标签里打开。
- **URL 形态**
  - 默认是 `onamp.dev` 的生成子域，形如 `https://t-…-p3000.onamp.dev`，结构是 `t-<thread>-p<port>`。文档里 `p<number>` 表示 Amp Portal 代理的标识，**不一定等于应用内部端口**。
  - 受管主机名：`<name>--<namespace>.onamp.dev`。
  - 工作区默认域名下形如 `t-<thread>-p<port>.portal.example.com`。
  - 规则：不要硬编码或自己构造域名，用 `amp orb portal <port>` 或 `amp orb services ensure` 打印的 URL，并在运行时读 `PUBLIC_URL`。
- **访问控制**
  - 默认：能查看线程的人才能访问其 portal；多人线程参与者在其访问期内可用。
  - 公开：Portal Options（⋯）→ Access → **Make Public For** 1 小时 / 3 小时 / 1 天 / 7 天。
    - 无需登录 Amp，仅限该端口，不授予线程或评审工具的访问。
    - 期间可 Keep Public For 续期，或 End Public Access。
  - agent 也能改：`manage_amp` 的 topic=`portals`、operation=`set-public-access`，需用户弹窗批准。`amp` CLI 不能改访问级别；`amp orb portal list` 能列出哪些 portal 公开及到期时间。
  - 以下情形 Access 不可用：自托管 provider 关了 Public Portals，或工作区关了「Allow Users to Share Threads and Portals Outside the Workspace」。
  - `.amp/services.yaml` 里不支持 `access` 字段（文档明确）。
  - 转发给应用的请求头：
    - `X-Amp-Authenticated`：逗号分隔的 `amp-user=yes|no, workspace-member=yes|no, collaborator=yes|no`。多数情况检查包含 `workspace-member=yes` 即可。
    - 当用户是工作区成员或协作者时，另有 `X-Amp-User-ID`、`X-Amp-User-Display-Name`、`X-Amp-User-Email`、`X-Amp-User-URL`。非可见 ASCII 值整体按 UTF-8 百分号编码，用 `decodeURIComponent` 还原。
- **登录/鉴权**：外部访问者需 Amp 登录（除非公开）；orb 内部请求线程的 portal URL 会直接到本地服务，不需要第二次 portal 登录（只绕过 Amp 的 portal 认证，不绕过应用自己的认证）。文档中**未找到** `portal login-url` 命令。
- **自定义 hostname/域名**
  - `amp orb portal <port> --hostname <hostname>`。主机名可以是受管的 `<name>--<namespace>.onamp.dev`，或你在个人/工作区 **Domains** 设置里添加并通过 DNS 校验（状态 Active）的自有域名。
  - 只有工作区可以把域名设为 portal 默认（Make Default for Portals）；个人域名只用于 `--hostname` 指定的主机名。
  - 归档线程时自定义 hostname 仍保持分配。
  - `--take-over` 把主机名从另一个线程的 portal 转到当前 portal。
  - 也可以迁到 box 应用：`amp boxes deploy --domain <hostname> --take-over`；portal 无法从 box 应用夺回主机名。
- **Manifest：`.amp/portals/<name>.json`**
  - 由 `amp orb services ensure` 或 `amp orb service start --portal` 生成，属于内部实现，应在 Git 忽略（`.amp/portals/`）。
  - 要自定义链接，手写单独的 manifest。完整示例：
    ```json
    {
      "links": [
        { "label": "App", "url": "https://t-…-p3000.onamp.dev", "note": "Use the seeded test account." },
        { "folder": "Admin", "links": [
          { "label": "Users", "url": "https://t-…-p3000.onamp.dev/admin/users" },
          { "label": "Settings", "url": "https://t-…-p3000.onamp.dev/admin/settings" } ] }
      ]
    }
    ```
  - 字段：`links[]`，每项要么是 `{label, url, note?}`，要么是 `{folder, links[]}`。`note` 显示为信息气泡；文件夹只嵌套一层，且同一服务内文件夹名唯一。
- **请求唤醒 orb**：任何已授权的 portal HTTP 请求都会唤醒暂停的 orb，并开启新的 20 分钟交互窗口。唤醒期间恢复计费，空闲后自动暂停；Portal 标签打开一个尚未启动的已声明 portal 时会自动运行 `ensure`。
- **WebSocket/HTTP**
  - 支持 HTTP，WebSocket 也能通过（比如 dev server 热更新）。
  - 每条 WebSocket 经过一台 Amp 服务器，Amp 部署时（常常一小时多次）会替换服务器，连接无 close frame 断开，客户端表现为 code 1006，必须自行重连。
  - 不要用 portal 做必须长期保持的连接（例如数据库隧道）；文档建议用 Tailscale。
  - 浏览器在两个 portal host 之间的请求不支持（CORS 预检不带 portal 认证）。方案是让前端 dev server 代理 API。
  - 应用收到的 `Host` 是公开域名，并带标准 forwarded HTTPS 头。
- **注入与评审组件**
  - 评审小部件默认注入页面：选中元素后向线程发评论，带选中文本、页面位置，应用能提供时还带源码位置。
  - 关闭方式：service 的 `review: false`，或单次响应头 `x-amp-review-widget: off`。
  - `target="_amp"` 链接可从 portal 面板跳回 Amp 页面。
- **框架**
  - Vite：Amp 启动 portal 服务时设置 `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS`（如 `.onamp.dev`），Vite ≥ 6.1 自动放行。
  - Laravel：在 services.yaml 里 `APP_URL="$PUBLIC_URL" ASSET_URL="$PUBLIC_URL"`，用标准 trusted proxy。
- **Dev Magic Link 约定**：开发专用路由 `GET /__dev/log-me-in/<email>?returnTo=<path>`，用 `$AMP_USER_EMAIL` 占位生成主 portal 链接。
- **Runner 上的 Portal**：`amp --no-tui --remote-control-portals`；runner portal 不能公开，页面也没有评审按钮。来源：runners 页。

---

## 5. Services（`.amp/services.yaml`）

来源：portals 页。

### 完整 schema

```yaml
services:                      # 必填，非空映射；名称 [a-z0-9-]，以字母/数字开头，≤32 字符
  web:
    command: pnpm dev -- --host 0.0.0.0 --port "$PORT"   # 必填，经 login shell 运行
    cwd: app                   # 相对仓库根，默认仓库根
    port: 3000                 # 1–65535 固定端口；省略则 Amp 选一个跨重启保持不变的空闲端口
    env: { API_MODE: development }   # 字符串；可引用 ${services.web.publicURL}
    health: /healthz           # 端口开放后 GET 此路径，2xx/3xx 表示就绪；省略则只检查端口连通
    review: true               # 默认 true，是否注入评审小部件
    platforms: [linux]         # linux / darwin，非空列表；省略表示全平台
    portal:                    # true → url:/、title=服务名；或 {url,title,description}
      url: /
      title: App
      description: Use the seeded development account.
    portals:                   # 额外链接，每项需 url+title；也可用 folder + links 分组
      - url: /admin
        title: Admin
      - folder: Marketing site
        links: [ {url: /home, title: Homepage} ]
```

### 规则与细节

- `PORT`、`PUBLIC_URL`、`AMP_THREAD_ID` 由 Amp 管理，不得在 `env` 里设置。所有服务都收到 `PORT` 和 `AMP_THREAD_ID`，至少有一个 portal 链接的服务还收到 `PUBLIC_URL`。
- portal 条目里唯一支持的占位符是 `$AMP_USER_EMAIL`，会替换进 `url`（做 URL 编码）、`title`、`description`。
- `url` 可以是以 `/` 开头的路径，或绝对 http(s) URL。
- 服务间引用：`${services.web.publicURL}`。Amp 会先启动被引用的服务；被引用服务必须有 portal；引用缺失或循环依赖会被拒绝。
- `platforms` 不匹配当前 executor 的服务会被跳过、不显示链接；若没有匹配的服务，ensure 成功但什么也不启动；被引用的服务也必须匹配平台；空列表无效。

### supervisor 命令

```
amp orb services ensure [--json]   # 读 services.yaml，检查每个服务，补启动缺失的，等就绪，写 Portal 标签状态，打印 URL
amp orb service list | status <n> | logs <n> | restart <n> | stop <n>
amp orb service start <name> --command '...' [--portal --title --description --cwd --port]  # 一次性服务，等端口最长 60 秒
amp orb portal <port> --title 'App' [--description] [--hostname] [--take-over]   # 给已在监听的服务建 portal，不负责拉起或监督
amp orb portal list
```

- `restart` 会重新读取最新的 `services.yaml`，保留端口和 portal 配置。
- `start` 会拒绝已在 services.yaml 里声明的名字。
- **监督行为**
  - 服务被 supervisor 管理，能在 Amp 更新和 orb 恢复后存活，也能跨暂停保持（Amp 托管 orb）。
  - 不要用 `nohup`、`setsid`、`&`、tmux 来保活 portal 服务，否则可能在 Amp 更新或 orb 恢复时被杀。
  - 打印出 URL 只说明路由已配置，不代表服务通过了就绪检查。
- **日志**：`amp orb service logs <name>`；setup/resume 日志见第 1 节。
- **与 portal 的关系**：service 可以没有 portal；有 portal 才会收到 `PUBLIC_URL` 并出现在 Portal 标签。
- **不要**在 `.agents/setup` 里启动 dev server 或运行 `ensure`。

---

## 6. Desktop、Chrome、Profiles、iOS Simulator

### Desktop（来源：portals 页 "Desktop"）

- orb 线程的 **Desktop** 标签：orb 里的高分辨率交互式 Linux 桌面。
- agent 做网页任务（填表、购物）时驱动的就是这里的 Chrome 窗口；需要用户接管（登录、验证码）时会请求。Amp 会录屏并用截图汇报。
- 默认启动 Chrome；可用 Desktop resume script 替换。
  - 脚本优先级：仓库内 `.agents/desktop/resume` 高于项目设置。
  - 每次 Desktop 会话启动时在仓库根运行，需自己判断 app 是否已运行。
  - 脚本里启动的子进程在脚本退出后继续运行。
  - 改脚本后需 `sudo systemctl restart amp-desktop`。
- 内置 app 安装：`sudo -n /opt/amp-desktop/install-app <name>`，名称为 `chrome`、`firefox`、`code`（VS Code）、`blender`、`terminal`（Foot）。首次打开才安装。
- 可用 `apt-get` 装其他图形应用（要进快照请写到 setup）。
- 限制：orb 没有硬件虚拟化，不能跑 Android 模拟器；orb 的 computer use 标注为「早期、不受支持」。
- 想用自己的家庭网络访问封锁云 IP 的站点，见第 8 节。
- 实验开关：Settings › Experimental Features 的 Desktop Fun Games（TuxRacer 等）。

### Chrome Profiles（来源：<https://ampcode.com/docs/markdown/orbs/profiles>）

- **功能**：保存在 orb Desktop Chrome 里的登录态、保存的密码和站点数据（如 localStorage）。
- **开启**：Settings › Experimental Features 里打开 Profiles。
- **命名**：3–32 个字母/数字/连字符。
- **选择**
  - 新线程 composer：⋮ › Profile。
  - CLI：`amp -ox "..." --profile <name|ID>`，要求该 profile 打开了 **Allow Agent to Select**。
  - Desktop 标签工具栏的菜单：切换时若 Chrome 已开则带新标签重启。
  - 线程菜单：••• › Orb › Profile。
- **权限**
  - 一个 orb 只能读写其线程所选的那个 profile。
  - 默认只有用户自己选。打开 Settings › Profiles 的 **Allow Agent to Select** 后，orb 里任何以用户凭据运行的东西都可以选。
  - agent 在 orb 内用 `amp profile use <name>`（Chrome 重启后才返回）、`amp profile none` 恢复干净 profile；在 orb 外（如 Puck）用 `manage_amp` 的 topic=`profiles`、operation=`use`；`amp profile list` 列出。
  - agent 自己建的 profile 默认对 agent 开放，且不用于多人线程。
- **同步**
  - 登录态和 localStorage 即时同步：一个 orb 登录/登出或 localStorage 变化，几秒内其他用同一 profile 的 orb 就能收到，无需重启 Chrome。
  - Chrome 其余数据（其他站点数据、保存的密码）每 2 分钟保存一次，切换 profile 时也保存（仅当自上次保存后有变化）。
  - 冲突：同一站点登录态并发修改，先到 Amp 的赢，但登出永远赢；localStorage 并发修改两边都保留，除非改了同一条目，则先到先赢；一个 orb 只保存自己改过的部分；保存的密码两边都保留。已打开的 Chrome 在重启前保持原状。
  - 标签页留在打开它的线程，每个线程的 Chrome 以新标签页开始，重启后恢复本线程自己的标签。
- **平台**：仅 Linux orb；macOS orb 不提供。
- **多人线程**：用了 profile 的线程不能转多人（需先选 No Profile，等几秒让 orb 清掉）；要在多人里用需打开该 profile 的 **Allow in Multiplayer**，关闭时 Chrome 会用干净 profile 重启。
- **安全**：静态加密，只有你自己的 orb 线程能读；profile 永远不保存你的 Amp 登录或 orb portal 登录；删除即抹除全部内容。
- **Passkeys**：orb 的 Chrome 没有蓝牙/USB，需在 Mac App 打开该线程 Desktop 标签，点 Allow 后 App 最多 2 分钟使用蓝牙，扫码用手机确认。仅线程所有者可做。

### iOS Simulator

两条路径：

1. **macOS Satellite**（来源：<https://ampcode.com/docs/markdown/orbs/mac>，beta，需候补名单）
   - orb 仍跑在 Linux 上；需要 macOS 时 Amp 约 30 秒启动一台带 Xcode、iOS 模拟器和 Homebrew 的 Mac。
   - 每条命令执行前，orb 会把变更同步到 Mac，Git 忽略的文件（如 `.env`）留在 orb。截图、日志、构建产物回传线程。若应用依赖 orb 上的后端，Amp 会把 Mac 连到它。
   - 线程有 **Mac** 标签，在 Desktop 标签旁，可看屏并操作。
   - 镜像：`macos-27`（Xcode 26.6 与 27.0–27.2，默认 27.2；iOS 27.0–27.2，默认镜像）、`macos-26`（Xcode 16.4 与 26.0–26.3，默认 26.1.1）、`macos-15`（Xcode 15.4 与 16.0–16.4，默认 16.0）。项目设置 Settings → Satellites 选 Mac Image；项目固定 Xcode（如 `.xcode-version`）时 Amp 自动选匹配镜像。
   - 规格：`mac.medium` 6C/14G、`mac.large` 12C/28G。
   - 空闲 20 分钟删除（含其上所有内容），归档线程、orb 休眠或要求停止时立即停止。
   - 代码签名：Settings → Integrations → App Store Connect；或环境变量 `ASC_KEY_ID`、`ASC_ISSUER_ID`、`ASC_PRIVATE_KEY_B64`（或 `ASC_PRIVATE_KEY`），三者都非空时优先于已连接的 key 且不再询问。Mac 上的签名凭据随 Mac 停止而删除。
   - Mac 是 VM，无嵌套虚拟化，不能跑 Android 模拟器。
2. **Runner 的 Desktop**（<https://ampcode.com/docs/markdown/cli/runners>）：`amp --no-tui --desktop`，在 macOS 上共享 Mac 显示器，agent 可驱动 iPhone Simulator。
   - 需要 Apple Silicon、macOS 15+、arm64 Amp CLI。
   - 通过 `Amp Desktop Helper.app` 申请 Screen Recording 和 Accessibility。
   - Linux 上 runner 自起 labwc 无头桌面（1280x720），用 Waymote 推流，并导出 `WAYLAND_DISPLAY`、`DISPLAY`、`PULSE_SERVER`。
3. **服务侧**：services.yaml 的 `platforms: [darwin]` 示例里 `command: ./scripts/serve-ios-simulator`。文档中**未找到**专门的 "iOS Simulator serve" 页面或命令，该示例就是文档所给。

---

## 7. Secrets & Env Vars、redact、id-token

来源：<https://ampcode.com/docs/markdown/orbs/handling-secrets>。

### 层级

- 三层设置页：**Workspace**（`/workspace/environment-variables`，作用于该工作区的所有 orb，包括个人项目）、**Project**（项目设置）、**Personal**（`/settings/environment-variables`）。
- 覆盖顺序：personal > project > workspace，同名后者被前者覆盖。
- **box 层**：`amp secrets set STRIPE_KEY --box alice/workbench --deployment bug-tracker --secret --data-file ./key.txt`，每个应用有自己的 secrets，部署后才生效（<https://ampcode.com/docs/markdown/boxes>）。
- runner 层：加 `--amp-env`（或设置 `amp.runner.env.enabled=true`）后，每次线程启动时 runner 向 Amp 拉取变量，优先级 personal > project > workspace > runner 进程环境。注入到 shell 命令和 stdio MCP 服务器，按目录生效，变量改了无需重启 runner。共享 runner 只加 workspace 和 project 的，不加个人的。

### 注入方式

- 新 orb 启动时拿当前值；运行中的 orb 用 `amp orb restart-processes` 刷新（等当前命令完成，重载值并重启 executor 与受管服务）。
- 环境变量值可以为空（如 `GIT_CONFIG_VALUE_0=`）；secret 必须有值。
- 标记为 secret 后，保存后界面隐藏值。
- 管理方式：设置页、`amp secrets --help`、`amp secrets history`（谁创建/最后修改，不显示值）、agent 的 `manage_amp`（topic `secrets` 或 `environment_variables`）。Amp 需要你已有的密钥（如 API key）时，会让你在私有对话框里输入，值不进线程；Amp 自己生成的随机值（如 webhook 签名密钥）直接存入。
- 保留变量名：`AMP_API_KEY` 保留，不能当 secret 存；Amp 正在从 orb 环境中移除你账号的 `AMP_API_KEY`，orb 内的 `amp` CLI 使用线程身份（短期、自动轮换的凭据）；Amp 托管仓库的 Git 操作用只限 Git 的 token。
- setup 阶段只收到 workspace 和 project 级变量，不收到个人变量（按成员快照路径除外）。

### redact-output

- Amp 隐藏 orb shell 输出里**所有出现的** secret 值。
- 因此不要把短值或常见词（`file`、`true`）存成 secret，否则每次出现都被隐藏；设置页会在保存这类值时警告。

### OIDC / id-token

- 命令：`amp orb id-token --audience <aud> [--ttl-seconds N] [--subject-scope thread|user|project|workspace]`，stdout 只输出 token。默认有效期 10 分钟，可设 60 秒到 1 小时。
- 算法 RS256。端点：
  - Issuer：`https://ampcode.com/api/workload-identity`
  - Discovery：`…/.well-known/openid-configuration`
  - JWKS：`…/jwks.json`
- 标准声明 `iss`、`aud`、`sub`、`iat`、`exp`、`jti`；Amp 声明：`workspace_id`（个人线程无）、`project_id`（无项目则无）、`user_id`、`thread_id`、`email`、`email_verified`（`user_id`/`thread_id`/`email*` 在项目 setup 阶段缺失）、`token_use`（`amp orb id-token` 签发的为 `exchanged`）。
- `sub` 按 scope 逐层去掉右边：
  - `thread`：`workspace:<ws>:project:<proj>:user:<user>:thread:<thread>`
  - `user`：`workspace:<ws>:project:<proj>:user:<user>`
  - `project`：`workspace:<ws>:project:<proj>`（个人项目为 `user:<owner>:project:<proj>`）
  - `workspace`：`workspace:<ws>`
- 默认 scope 是 `thread`。每次签发 project scope 都会检查当前项目访问权；项目换所有者会改变 subject。
- setup 阶段（pre-clone、pre-setup、`.agents/setup`）只能签发 project 或 workspace scope 的 token（`workspace` 需要工作区拥有的项目），请求凭据位于 `/run/amp/workload-identity-request-token`，setup 后被移除；resume 时重新写入新的运行时凭据。
- 信任策略建议：每个服务使用不同 audience；按 `workspace_id`、`project_id`、`user_id`、`thread_id` 限制；有 custom claim 条件时要求 `token_use == exchanged`；用不可变 ID 而不是 `email`。
- 文档给出了 GCP Workload Identity Federation、AWS IAM OIDC（AWS 忽略自定义声明，只能靠 `aud` 和 `sub`）和 Tailscale 的完整示例。
- MCP：HTTP MCP 服务器的 header 里写 `${amp:id-token}` 可以带上针对其 origin 的 ID token（详见 customize/mcp，我没读那页）。

---

## 8. Amp Net：Orb Network Routing / 共享网络 / Tailscale

文档里没有叫 "exit node" 的 Amp 功能，相关的有三种：

1. **Orb Network Routing**（来源：<https://ampcode.com/docs/markdown/cli/runners>）
   - 在你的机器上运行 `amp --no-tui --runner-id home-mac --share-network`（Mac App 有开关）。
   - 在 orb 线程的 ••• › Orb › Network Routing 里选 `home-mac`，或在 orb 内运行 `amp net route home-mac`。
   - 可在 AGENTS.md 写 "Before using the web in an orb, run `amp net route home-mac`."。
   - 需保持 runner 运行且电脑不睡眠。
   - 管理员可在工作区设置 Orbs › Orb Network Routing 关闭；关闭时 orb 不再通过 runner 路由、runner 也不再共享网络。
   - 用途：很多网站封锁云 IP，让 orb 通过你自己的网络上网。
   - 底层协议和流量路径文档**未找到**。
2. **Tailscale exit node**
   - customizing 页的 Cookbook 说可以「通过 Tailscale 并使用 exit node」路由住宅/商用网络流量。
   - 同页详细的 Tailscale 说明又明确警告**不要加 `--exit-node`**：exit node 会把 orb 的全部出站流量和 DNS 都送进 tailnet，包括到 Amp 的连接、包下载和 CLI 更新器；exit node 不可达或唤醒后尚未重连时，orb 连不上 Amp，线程会一直等一个永远不会连上的 executor。
   - 同页又说 Amp 自己主机到 orb 的流量不走 tailnet，所以 route 或 exit node 不会切断 Amp 对 orb 的控制。两处说法在这点上互相矛盾，Oryn 若实现应走「控制流量绕过」的安全设计。
3. **私网访问**
   - 加 `--accept-routes`，只通告 orb 需要的子网，由 subnet router 或 app connector 提供。
   - 完整示例：用 OIDC ID token 向 Tailscale 换取临时 auth key（`tailscale up --client-id="${TAILSCALE_CLIENT_ID}?ephemeral=true&preauthorized=true" --id-token="file:$token_file" --advertise-tags=tag:amp-orb`）。
   - 安装时用 `policy-rc.d` 阻止包安装后自启 `tailscaled`，setup 里禁用服务，避免机器身份进快照；`.agents/resume` 里才启动和登录。
   - 在 E2B microVM 里需设置 `TS_ASSUME_NETWORK_UP_FOR_TEST=true`。

**共享网络（runner 范围）**：`--share` 是把 runner 共享给工作区成员（见 runners 页），与上面的 `--share-network` 不同；`--share` 的成员线程在你的机器上以你的 OS 用户运行代码。

---

## 9. Files / Terminal / Desktop / Changes 面板能力

来源：<https://ampcode.com/docs/orbs>、<https://ampcode.com/docs/markdown/threads>、<https://ampcode.com/docs/markdown/orbs/files-and-attachments>。

- **Changes**
  - 列出已提交和未提交变更，commit 选择器可看全部变更、仅未提交、单个 commit、与另一分支对比。
  - 智能排序：Amp 读 diff，把最能解释变更的文件放前面；超过 500 个文件或线程对你只读时按字母序。
  - 操作：**Ship**（按项目 Ship Behavior）、**Review**（逐步走读 diff）、**Sync**、**Restack**（把中间 commit 整理成更干净的一组，不 push）。
- **Files**
  - 浏览并打开 orb 中任意文件，无需先下载仓库。
  - 上传：拖拽到 Files 面板（拖到目录则放入该目录，其他位置放入仓库根）；工具栏上传按钮；目录右键 Upload Files…。单文件上限 512 MB。
- **Terminal**
  - 在 orb 内开一个 shell，使用共享 **tmux** 会话。终端和 agent 使用同一文件系统与工作副本，改动互相立即可见。
  - Amp 能检查终端输出并配合长命令；Amp 也能在回复里内嵌可输入的实时终端。
  - 粘贴图片或拖入文件到终端：Amp 上传到 orb 并把绝对路径插入光标处（只有仅含文件而无文本的剪贴板才会上传）。
- **Desktop / Portal / Mac / Huddle**：见前面各节。
- **附件（线程级）**
  - composer 附件按钮、拖拽、粘贴；默认单文件上限 100 MB，有更高上传额度的用户可到 512 MB。
  - 附件保存在线程里，不会自动拷进仓库；Amp 能直接看图片/PDF/音频/视频，其他类型需要本地工具时才下载进 orb。
  - 线程菜单 Attachments 列出附件和 agent 上传的文件；标记 **Public**（已发布）和 **Temporary**（几小时后过期）。能删线程的人可删附件。
  - 附件独立于 orb 的 Files 面板，删附件不改 orb。
  - 发布：让 agent 把文件发布成公开 URL。
- **`amp sync`**：`amp sync <thread>` 在本地仓库检出（需无未提交改动）里镜像 orb 的变更并持续更新，停止时恢复原状；`amp sync --apply <thread>` 一次性应用并退出（可加 `--checkout` / `--skip-checkout`）。未推送的 commit 直接从 orb 取。以你的身份登录（`amp login`，或 CI 里设置 `AMP_API_KEY` 为 `sgamp_` 开头的访问令牌）。
- **TUI**：Ctrl+O 命令面板 → "thread: new in orb"；`amp --executor orb` 打开选了新 orb 的 TUI。
- **多人**：开启多人后，团队成员可以使用 orb 的 portal、终端和文件浏览器；见第 11 节。
- **Runner 上的对应关系**（对比用）：Changes/Files 在 runner 目录是 Git/Jujutsu 检出时自动可用；Terminal 需 `--remote-control-terminal`；Portal 需 `--remote-control-portals`；Desktop 需 `--desktop`；Desktop 和 Terminal 通过 WebRTC 连接 runner，直连或经 TURN，5 秒内连不上则回退到 Amp 中继。

---

## 10. Boxes

来源：<https://ampcode.com/docs/markdown/boxes>。

- **是什么**：Box 是常开的计算机，用来跑你用 Amp 构建的东西（网站、仪表盘、工具、长驻服务）。每个应用有自己的 URL，如 `https://bug-tracker--acme.onamp.dev`。
- **机器**：Ubuntu 24.04，20 GB 磁盘，文件在部署和重启之间保留；box 没有公网 IP，不能 SSH。
- **与 Orb 的区别**（原表）：

| | Orb | Box |
|---|---|---|
| 用途 | agent 在线程里改代码 | 需要持续运行的应用 |
| 运行 | 线程空闲就暂停 | 常开，或按每日时间表 |
| 数量 | 每线程一台 | 一台机器跑多个应用和多个线程 |
| URL | portal 预览 dev server | 每个应用一个 URL |
| 价格 | 按分钟，运行时计费 | 每 30 天起 $18 |

- 典型流程：在 orb 里构建和评审，再部署到 box。box 自己从不构建代码；box 上的线程跑在 box 的 Amp runner 上，直接改线上应用。
- **规格**：`b1.tiny` 0.5 vCPU（可突发到 2）/2 GB，$18/30 天；`b1.small` 1 vCPU（突发到 2）/4 GB，$30（默认）；`b1.medium` 2C/8G，$50；`b1.large` 4C/16G，$100。
  - 创建后不可改规格；换规格要新建 box 再 `amp boxes cp` 迁数据。
  - Amp 备有 `b1.tiny` 和 `b1.small` 的热机，`b1.medium` 和 `b1.large` 新建约等待 6 分钟。
  - 每 box 每月 100 GB 网络流量，80 GB 邮件提醒，到上限就停机到下月（UTC）。
- **部署模型**
  - 命令：`amp boxes deploy [--on alice/workbench --name bug-tracker --create --size ... --private|--public --widget|--no-widget --domain <host> --no-domain --take-over --move]`。
  - 目标记录在 `.amp/box-deploy.json`（可多个目标）。
  - 构建在 orb（或 runner 机器）里跑，不在 box 上：Amp 在已 push 的分支上运行 `build`；如果检出位于已 push 的 commit、没有未提交或未跟踪文件，并且配置里列了 `artifacts`，则在该检出里直接 build（复用已装依赖和构建缓存），否则在全新 clone 里构建。
  - 把 `artifacts` 打包发到 box 成为新 release，box 切换到新 release 并重启该应用的服务。Amp 等应用监听端口，不监听则回滚到上一个 release 并使部署失败。
  - 保留当前 release 加前 2 个 release。
  - 部署时应用的环境变量：`PORT`、`AMP_DEPLOYMENT_URL`、`AMP_DEPLOYMENT_NAME`、`AMP_DEPLOYMENT_BOX`、`AMP_DEPLOYMENT_DATA_DIRECTORY`（构建只看到前三个）。
  - 同 box 多应用：每应用独立 Linux 用户 + systemd 沙箱，无 sudo，只能写自己的数据目录和 home；**不是**应用间的安全边界（共享 CPU/内存/磁盘/网络，可互连端口）。
  - 一个应用绑定其首次部署的仓库，换仓库要加 `--move`。
- **`.amp/box.yaml`**（文档只给了这些字段；完整 schema 我**未找到**）：
  ```yaml
  build: pnpm install --frozen-lockfile && pnpm build
  source: false          # false 则 release 只含 artifacts，且各 service 的 cwd 必须在 artifacts 内；省略则带源码和 Git 历史，线程可改运行中的代码
  artifacts: [build, package.json]
  include: [../packages/ui]   # 单仓库多应用时，相对应用目录追加需要的目录
  services:
    web:
      command: node build
  ```
  - 单仓库多应用：每个应用目录里各一个 `.amp/box.yaml`，应用的 `build`、service 的 `cwd` 和 `artifacts` 都相对该目录。
- **访问控制**
  - 应用默认公开，`--private` 后访问者需登录 Amp（只有 box 所有者或所属工作区成员），私有应用无法接收 webhook。Amp 不会把用户身份传给应用。
  - 个人 box（`alice/…`）只有本人可见；工作区 box（`acme/…`）全员可见/部署/开线程，仅创建者和管理员可删除、启停、改时间表、看成本。
  - 工作区 box 需 Enterprise，或带支付方式的 Teams。
  - CI 可用服务账号 API key 作为 `AMP_API_KEY` 部署。
  - 多人线程（包括 Space 启动的线程）可以操作本工作区 box；agent 创建 box、改计划、启停、设置 secrets 等需线程所有者或管理员在弹窗中批准。
  - 工作区管理员可关闭 **Allow Users to Create Boxes** 和 **Allow Public Box Apps**。
- **运行时间表**：每日一个运行窗口，`amp boxes schedule alice/workbench --on 05:00 --off 23:00 --tz Europe/Berlin`（`--clear` 清除）；计划停机时最多等 30 分钟让运行中的线程和部署结束。
- **其他特性**：自定义域名（`--domain`，需先在 Domains 设置里加域名并 DNS 校验）；反馈小部件（`--widget`）；导航栏预览 `<meta name="amp-preview" content="v1; enabled; iframe">`（Amp 用 384×240 的 iframe，查询参数 `amp-preview=light|dark`）；滥用举报；被封禁的 box 磁盘和数据保留。
- **命令**：`amp boxes deploy | deployments | undeploy | cancel-deploy | create | list | show | exec | cp | schedule | start | stop | rename | delete`。
- **计费**：按运行分钟数；停止的 box 免费；创建需至少 $2 信用额度；付款方用完额度后 24 小时 box 停机，磁盘数据保留。

---

## 11. Automations、Webhooks、Agent-to-Agent、多人

### Automations（来源：<https://ampcode.com/docs/markdown/orbs/automations>）

- 用自然语言说明时间，Amp 在当前线程保存一个**调度（schedule）**加一个**提示（prompt）**。到点时 Amp 用一条简短消息唤醒 agent，agent 用 `get_schedule` 工具读取保存的提示并执行，保留线程上下文。
- **每个线程最多一个调度**；可单次或重复。重复调度直到你暂停/删除、达到配置的结束时间，或 Amp 在满足你给的完成条件后清除它。
- 单次调度运行一次即完成；失败时 Amp 显示错误；临时投递错误可让重复调度保持启用；若 Amp 暂停了调度，需修复后恢复。
- 管理：线程里的 automation 指示器（标题、时间表、下次运行、保存的提示，开关暂停/恢复，垃圾桶删除），或直接对 agent 说。
- **贪睡（snooze）线程**：调度运行和事件订阅投递（如 GitHub CI 结果）继续唤醒 agent，但不把线程带回侧边栏、不发通知，除非 agent 在提示里被告知要 unsnooze。
- 调度和 webhook 对应的工作算 agent 工作，会让 orb 保持运行。
- 周期性示例：「每个工作日早上 9:00 …」「每十分钟检查 …」。

### Webhooks

- 由**插件**创建的公开 HTTP 端点，存事件并唤醒其 orb；仅限在 Amp 托管 orb 里运行的插件。
- API：
  ```ts
  const { url } = await amp.createWebhook({
    key: 'github-events',
    headers: ['x-hub-signature-256'],
    handler: async (event, ctx) => { /* event.id, event.body, event.headers; ctx.signal */ },
  })
  ```
- 返回的 `url` 接受 `POST`。同一 key 重复注册，URL 在插件重载和 orb 重启后保持不变；在项目里，注册归属于「用户 + 项目 + 插件 + key」，因此检入的插件在同项目其他线程里得到同一 URL；首个注册的线程是**所有者线程**，负责处理事件；无项目的线程每个单独注册。
- **投递保证**：请求先存储，再返回 HTTP 200（只表示已入队），然后唤醒所属 orb 并调用 handler。**至少一次**投递，需按 `event.id` 去重。handler 有 30 秒；抛错则只延迟该事件，5 秒后重试，逐次翻倍直到 5 分钟，持续被拒 1 小时则丢弃并记录日志；因此不保证严格顺序。待处理事件让 orb 保持运行。
- **限速**：每端点突发 10 个新事件，每分钟补充 10 个；超出或积压达 100 个返回 HTTP 429，带 `Retry-After`；请求体最大 1 MB；发送方重试时建议带稳定的 `Idempotency-Key`。
- **安全**：URL 视同密码（谁有谁能无登录投递）；Amp 不校验外部服务的签名，需把签名头放进 `headers`，把密钥存在项目 secrets 里并自行验证；插件未列出的请求头会被丢弃；归档所有者线程后 URL 返回 404、投递暂停，恢复线程后继续。
- 触发设置里可暂停（保留 URL 但拒收）或删除（吊销 URL 并清空队列）。
- 测试：`curl -X POST -H 'Idempotency-Key: ...' --data '{...}' "$AMP_WEBHOOK_URL"`。
- 保持线程活跃的规则：不要归档拥有 CI/webhook 的线程；CLI 执行模式加 `--no-archive-after-execute`，SDK 设 `noArchiveAfterExecute` / `no_archive_after_execute`。
- 其他触发：项目设置的 Post-receive Webhook（Amp 托管仓库每次 push 后发 JSON POST）、GitHub 监听器（news/github-listeners，**未读**）。

### Agent to Agent（来源：<https://ampcode.com/docs/markdown/orbs/agent-to-agent>）

- agent 可以启动其他 agent、发指令、交换文件、把结果带回当前线程。
- 新线程有自己的对话、上下文窗口、工作副本和 orb，**不自动共享未提交文件**；需要明确的文件传输（消息本身不传文件或 commit）。
- 用法：side quest（并行处理无关 bug）、fan-out（并行多个线程，选小规格 orb 减成本）、交换文件、继续旧工作（找到旧线程并把文件拉进当前工作区）、跨项目或跨机器（另一个项目、orb，或装了 runner 的机器，如 `cloud-dev-box` runner）。
- 线程可通过 `create_thread` 工具启动，并带 `executor: "runner"`、`runner_id`、`worktree`；父线程归档不会归档子线程。线程页有 "Archive N Threads" 一并归档。
- 子线程的 `parentThreadID` 在 External API 里可见。
- 底层的 agent 间消息协议文档**未找到**。

### 多人（来源：<https://ampcode.com/docs/markdown/collaborate/multiplayer>）

- 在线程上打开多人模式（默认 7 天，可用 `amp threads share multiplayer ttl <thread> --hours 3` 设 5 分钟到 7 天）后，工作区同事能在同一线程里提示 agent，并使用该 orb 或共享 runner 的 portal、终端和文件浏览器。费用全部由线程所有者承担。
- 安全告示：所有贡献者能访问线程、orb、secrets、文件和终端。
- 多人线程里 agent 拒绝修改账户设置/secrets；box 相关的修改要所有者或管理员批准；MCP 服务器与集成（Google、个人 Linear、App Store Connect、Slack 私有频道）默认在多人线程里禁用，需要逐项允许。
- 用 `@` 提及同事即自动邀请并开启多人。
- Huddle：线程内语音/视频/共享屏幕通话，不依赖多人。

### 附：Space

Spaces 页（`/docs/spaces`）**未读**；`boxes` 页提到「Space 启动的线程都是多人线程」。

---

## 补充：统计与可观测性（用户新增范围）

### 先说结论

我在已读页面里**没有找到** `amp orb system-metrics` 命令，也没有任何文档说明 orb 内的 CPU、内存、磁盘实时利用率、指标项或采样粒度。网页检索同样没有找到。官方暴露的是下面这些**分配与计费口径**的数据。Oryn 若要做运行时利用率，这部分需要自己设计。

### 已确认的数据面

1. **线程菜单（More Actions）**：显示线程累计成本；对 orb 线程显示 orb 当前是运行还是已暂停、使用的规格，并重复说明暂停超时（threads 页、sizes-and-costs 页）。
2. **线程侧栏的 `$` 价格**：点击可看该线程的详细费用（pricing 页）。Amp 也可以对话式「explain usage」，解释某线程为何多用了 token、额度如何被消耗（<https://ampcode.com/news/explain-usage>，我只从 pricing 页看到链接，**未读**）。
3. **`amp usage`** 命令和设置页：查看余额。设置页入口：`/settings`、`/workspace`；Satellites 用量在 `/settings/usage/satellites`，可看 Mac 运行了多久、花了多少。这些设置页要登录，我无法读取内容，具体的列和图表**未验证**。
4. **项目设置**：企业工作区的 orb 价格显示在项目设置里（sizes-and-costs 页）。
5. **Box 页**：About 面板显示是否运行、时间表、规格、（有管理权限时）本月已花费和月底预计；还有线程数与可用时间（uptime）图表（例如 "40 threads and 75% uptime over 21 days"）；Overview 显示所有 box 最近的部署和线程（boxes 页）。
6. **自托管**：线程用量显示每个 orb 的运行时长和规格，即使费用为 $0；按 orb 上报的 CPU 和内存按分钟计费，只计线程的 orb，不计其 agent 工具沙箱（self-hosted 页 Pricing）。
7. **External API v2**（`https://ampcode.com/api/v2`，来源：<https://ampcode.com/api/external>）
   - 认证：工作区的 M2M 应用（`https://ampcode.com/workspace/applications` 创建），Bearer token；数据范围限定在该工作区。
   - `GET /threads/{threadID}/usage`：线程用量，**仅保留 180 天（Enterprise）或 90 天（其他）内创建的线程**。响应中有按规格聚合的 orb 条目，**按用量从大到小**排序：
     - `size`（如 `"a1.medium"`，CPU 和内存不匹配任何当前规格时为 `null`）；
     - `cpuCount`（number|null）；
     - `memoryMB`（number|null）；
     - `runtimeMs`（number，运行时长毫秒）；
     - `usage`（number，费用，已包含在外层 usage 总额里）。
   - 用户维度的聚合同样带上「按 orb 规格的工作区归属 orb 运行时长与用量」数组，字段相同（API 文件约第 10950 行附近，我只确认了字段，没读完整个端点的路径）。
   - `GET /threads` 列表字段含 `agentState`（`working | idle | awaiting_approval | error`，可能比 agent 实际滞后约 10 秒）、`createdAt`、`updatedAt`、`firstSyncedAt`、`creatorUserID`、`parentThreadID`、`mainThreadID`、`repositories`、`labels`；可按 `project`、`visibility`、`userID`/`userEmail`、`updatedAfter` 等过滤。
8. **Activity 页（`/feed`）**：能按时间、状态、作者、项目过滤线程，也可用与 agent 查找线程相同的查询语法搜索；过滤条件存在 URL 里，可分享。
9. **日志**：orb 内 `setup.log`、`resume.log`（每次运行覆盖）；`amp orb service logs <name>`；provider 日志和线程的 setup log（自托管排障）；webhook 丢弃事件写入 orb 的 Amp 日志。线程在最近几次 setup 耗时达到或超过 2 分钟时显示警告（optimizing 页）。
10. **orb 暂停/运行状态**：线程菜单展示；`runtimeMs` 只算运行时间。暂停时长没有单独的指标面。

### 计费规则（一两句）

按分钟计费，暂停的 orb 免费；各规格单价、自托管计费与套餐包含的额度见 sizes-and-costs 与 pricing 页。Oryn 不做计费，只需保留「规格 × 运行时长」这类原始用量记录。

---

## 12. 对 Oryn 的启示

**架构**
- 值得复刻：agent 循环和模型推理放在服务器侧（每线程一个 actor），orb 只做「工具执行 + 文件系统 + 终端」，orb 通过一条出站长连接回连线程。这样 orb 暂停时 agent 状态不丢，自托管也只需要出站网络。
- 值得复刻：provider 协议极简：三个操作（spawn/pause/delete）加环境变量，每个操作幂等、5 分钟内完成，由外部脚本实现。Oryn 的自有 orb provider 直接沿用这个形状，调度、计量和重试都放到服务端。
- 值得复刻：`ORB_ID` 是确定性的机器名（`amp-<threadId>`），重复 job 靠它找到同一台机器，这样 provider 可以无状态。
- 值得复刻：orb type 由 provider 声明、线程创建时固定、终身不变；删除或改名要有迁移步骤和引用计数提示。
- 坑：一个 token 同时只允许一个 provider 在线，否则 orb 和 job 会错配；要在服务端做「单 provider 连接」检测。
- 坑：不同后端的暂停保真度差别很大（有无内存、有无快照）。要把「内存是否保留」「是否支持快照」作为 provider 的能力声明，唤醒后据此决定是否重启服务和重跑 resume。

**生命周期与快照**
- 值得复刻：setup/resume 分离。setup 在项目维度做并缓存成快照，resume 在每次唤醒时做认证和修复，且只给 10 秒。
- 值得复刻：快照失效判断用「基线分支 commit + 共享配置哈希」，并设最长复用期（72 小时）。要提供手动删除快照的入口。
- 坑：快照里不能带身份。setup 只能拿项目/工作区级身份，个人凭据在 resume 注入；setup 后要移除请求凭据。
- 坑：setup 结束要清理它留下的所有进程，长驻服务必须走受管服务或 systemd，否则不进快照。
- 坑：仓库不能留默认全量克隆。超大仓库要提供 shallow clone 选项，并给克隆设超时。
- 坑：空闲暂停规则要区分「agent 工作」和「用户交互」，并明确哪些事件续命。portal 请求算交互；webhook 和 automation 只算 agent 工作。

**Portal / Services**
- 值得复刻：portal URL 编码线程和端口（`t-<thread>-p<port>`），默认只有能看线程的人能访问，公开是限时的、按端口的、需要用户批准，并向应用注入 `X-Amp-Authenticated` 与 `X-Amp-User-*` 头。
- 值得复刻：`.amp/services.yaml` 由受管 supervisor 管理（`$PORT`、`PUBLIC_URL`、health、服务间 `${services.x.publicURL}` 引用、`platforms`），portal 请求能按需拉起服务，打开 portal 即触发 ensure。
- 坑：WebSocket 会因服务器滚动发布而断开且没有 close frame，客户端必须重连；这类代理层最好支持优雅迁移，别照抄这个限制。
- 坑：两个 portal 子域之间的 CORS 预检不带认证，要么走同源代理，要么在设计里解决。
- 坑：自定义域名需要 DNS 校验和 take-over 语义（归档线程仍占用主机名）。

**Secrets / 身份**
- 值得复刻：工作区 / 项目 / 个人三级覆盖，输出里自动遮蔽 secret，OIDC id-token 带 `workspace_id`、`project_id`、`user_id`、`thread_id` 并支持 `sub` 分级。
- 坑：遮蔽按字符串匹配，短值和常见词会误伤输出，需要保存时拦截或警告。
- 坑：AWS IAM 忽略自定义声明，只能依赖 `aud` 和 `sub`，所以 `sub` 的分级设计很关键。
- 坑：orb 内的 `amp` 用线程身份而不是账户 API key，避免长期密钥落在 orb 里。

**浏览器 / Desktop**
- 值得复刻：profile 按线程选择、加密保存，登录态和 localStorage 即时同步，其余数据定时保存，冲突规则明确（登出永远赢）。
- 坑：多人线程里使用 profile 等于所有人共享登录态，要默认禁止。passkey 需要客户端桥接蓝牙，无法在纯云端完成。
- 坑：orb 不能跑 Android 模拟器；iOS 要有独立的 macOS 机器，并同步文件到那台机器。

**网络**
- 值得复刻：让 orb 通过用户自己的 runner 出网（`amp net route`），并由管理员总开关控制。
- 坑：Tailscale exit node 会切断 orb 与控制面的连接。Oryn 必须保证控制/隧道流量始终绕过自定义路由，且唤醒后允许路由未就绪。

**统计与分析（Oryn 自己要补的部分）**
- 官方只提供「规格 × 运行时长 × 用量」和线程 `agentState`，没有运行时利用率。Oryn 要自己在 orb 内常驻采集（CPU、内存、磁盘、进程、网络）、按线程和项目聚合，并记录生命周期事件（创建、setup 耗时、暂停、唤醒、快照命中/未命中）。
- 值得复刻：用量数据按「规格」聚合并保留 `cpuCount`、`memoryMB`、`runtimeMs`，规格不再匹配任何当前档位时标记为 `null`，同时给出 API 导出，数据保留期可配置（Amp 是 90 天 / 180 天）。
- 值得复刻：setup 耗时过长（≥ 2 分钟）时在线程上警告，并保留 setup/resume 日志文件。

**面板与同步**
- 值得复刻：Files、Terminal、Changes、Portal、Desktop 作为线程右侧面板，Terminal 用共享 tmux，agent 可读输出，终端和 agent 共享文件系统。Terminal、Desktop 在直连失败时回退到中继。
- 值得复刻：`amp sync` 的做法是「检出 orb 的 commit + 复制未提交改动 + 持续镜像，退出时恢复」，未 push 的提交直接从 orb 取。
- 坑：上传分两类（线程附件和直接写入 orb 文件系统），限额不同，附件与 orb 文件互相独立，不要混淆。

**自动化 / webhook / agent 间协作**
- 值得复刻：webhook 先入队再返回 200，至少一次投递，指数退避（5 秒起、封顶 5 分钟、1 小时丢弃），按 `event.id` 去重；URL 视同密码，签名由插件自己校验。
- 坑：webhook 的所有者线程一归档，URL 就 404、投递暂停，要提示用户不要归档承载自动化的线程。
- 值得复刻：agent 间通信用显式的文件传输，线程之间不共享工作区；父子关系用 `parentThreadID` 记录。

**Boxes（可选，不属于 Oryn 首批范围）**
- 构建在 orb 里完成，box 只接收 release，部署失败自动回滚；多个应用靠 Linux 用户 + systemd 沙箱隔离，但文档明确这不是安全边界。

**待确认项（建议后续补读）**
- `amp orb bootstrap` 的内部协议和 orb 与线程之间的消息格式（文档未公开，只能从行为推测）。
- 暂停/唤醒的内存保存机制，以及 Amp 托管 orb 的真实隔离技术（只能从 E2B 的间接提示推断）。
- `.amp/box.yaml` 的完整 schema。
- 设置页里的用量图表（需要登录）。
- Space（`/docs/spaces`）、`/docs/apps/mac`、`/docs/customize/mcp`、`/docs/cli/settings`。

### Critical Files for Implementation
本任务是只读调研，没有本地代码文件。以下是对 Oryn 实现最关键的外部参考页：
- `https://ampcode.com/docs/markdown/orbs/self-hosted` — provider 协议、dispatch 契约、示例脚本、自带镜像要求。
- `https://ampcode.com/docs/markdown/orbs/customizing` — 生命周期、快照、setup/resume、日志。
- `https://ampcode.com/docs/markdown/orbs/portals` — services.yaml schema、portal 访问控制、manifest、Desktop。
- `https://ampcode.com/docs/markdown/orbs/handling-secrets` — 三级变量、id-token 和 `sub` 分级。
- `https://ampcode.com/api/external` — 线程与 orb 用量的 API 字段（`runtimeMs`、`cpuCount`、`memoryMB`）。
