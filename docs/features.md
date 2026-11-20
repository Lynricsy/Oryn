# Oryn 功能详解

README 只列每项能力的一句话概要，这里写完整的行为、边界与约束。部署与配置项见 [`deploy.md`](deploy.md)，协议见 [`protocol.md`](protocol.md)。

## 目录

- [执行环境](#执行环境)
- [工作台](#工作台)
- [Agent 配置](#agent-配置)
- [全局助手与 Space](#全局助手与-space)
- [记忆、GitHub 与 SSH](#记忆github-与-ssh)
- [Artifacts](#artifacts)
- [客户端](#客户端)
- [安全边界](#安全边界)

## 执行环境

Oryn 以 [Oryn Core](https://github.com/Lynricsy/oryn-core)（fork 自 [`can1357/oh-my-pi`](https://github.com/can1357/oh-my-pi)，MIT）为 Agent 内核，每个线程运行在自己的执行环境（orb）里。

### 四种执行后端

同一控制面可以同时接入以下后端，按项目或线程选用：

| 后端 | 隔离方式 | 说明 |
|---|---|---|
| Docker 容器 | 普通容器 | 默认剥离 `NET_RAW` 与 `NET_ADMIN` |
| Sysbox 嵌套容器 | 非特权 UID 映射 | 可在容器内运行内层 `dockerd`，无需 `--privileged` |
| Firecracker microVM | 独立 Linux 内核 | `/30` 点对点 tap 网络与 jailer 槽位隔离 |
| 本机 Runner | 个人机器上的普通用户进程 | 以 `orbd run --runner` 直连指定目录 |

本机 Runner 的目录已有活动线程时会报错，可在界面上选择新建 worktree，或用 `--worktree <名>` 在仓库旁新建独立的 `git worktree`。

### 项目快照与预热池

项目首次完成 `.oryn/setup` 后自动生成干净的磁盘快照（Docker 提交镜像，Firecracker 用 `reflink` 复制稀疏盘），之后的新线程跳过构建，直接运行 `.oryn/resume`。高频项目可配置 1–3 台常驻预热池，新建线程直接认领。

### 冻结、唤醒与停机

Agent 与用户空闲达到阈值后，orb 自动冻结（暂停容器或 vCPU），打开线程或访问受管服务时自动唤醒。冻结超时后转为停机，磁盘状态完整保留。停机的 orb 唤醒要冷启动，所以打开线程时不会自动唤醒它，只有发消息或手动唤醒才会。

停机期间文件面板仍可浏览和预览 orb 里的文件，且不会唤醒它：docker provider 从已停止的容器里读取，firecracker provider 在用户态解析 VM 磁盘（不挂载）。需要编辑时一键唤醒。

### orb 跟随线程

orb 是线程的执行环境，不是要用户挑选或清理的资源：

- 新建线程总是得到一台新 orb（或认领预热池）。界面上不列出已有 orb、不显示 orb 编号（信息面板悬停可见，供排查）。
- 线程全部归档后，orb 在下一轮空闲评估（≤30 秒）就冻结。归档线程的 orb 只冻结 / 停机、不删除，取消归档后可继续。
- 最后一个引用它的线程（含已归档的）删除时，orb 一并删除。「无引用」判定与进入删除在同一事务；挂载线程的写入事务会复核 orb 没有在删除，二者串行。
- 例外是定时任务的 `keepLast`：自动归档的旧运行只回收它独占的 orb。别的线程（哪怕已归档，例如从它「共用 orb」分叉出的线程）还在用就保留，同样在一个事务里判定。
- 命令行 `oryn thread new --orb` 与 API `executor.kind = "orb"` 仍可挂到已有 orb，留给脚本与高级用法。

### 环境自配置

项目的开发环境由生命周期脚本声明：

| 脚本 | 何时执行 |
|---|---|
| `.oryn/setup`（兼容 `.agents/setup`） | 新 orb 首次创建时执行一次 |
| `.oryn/resume` | 每次冻结后唤醒时执行，最长等待 10 秒 |

不便入库的脚本也可以保存在控制面的项目设置里。脚本退出非零不会阻塞 orb 就绪：工作台顶部显示诊断条，可以查看日志，或交给当前线程的 Agent（内置 `oryn-setup` 技能）修复。

### 开发镜像

`images/orb/` 基于滚动更新的 Arch Linux 构建：x64 用官方 [`archlinux:latest`](https://hub.docker.com/_/archlinux)，ARM64 用每日构建的 [`menci/archlinuxarm:latest`](https://github.com/Menci/docker-archlinuxarm)。默认非 root 用户、UTF-8、`UTC` 时区，`~/.local/bin`、`~/.cargo/bin`、`~/go/bin` 与 npm 全局目录已在 `PATH` 里。

| 类别 | 预装工具链 |
|---|---|
| 语言与构建 | C/C++ (GCC/Clang)、Make、CMake、Ninja、Autotools、Go、Rust (`rustfmt`/`clippy`/`rust-src`)、Python (`pip`/`uv`/`pipx`/`ruff`)、Node.js (`npm`/`pnpm`/Bun)、Ruby/Bundler、PHP/Composer、Lua |
| 语言服务器 | 按 Oryn Core 内置 LSP 配置预装：`clangd`、`rust-analyzer`、`gopls`、`typescript-language-server`、`pyright`、`ruff`、`ruby-lsp`、`phpactor`、`lua-language-server`、`bash-language-server`、YAML / JSON / HTML / CSS / Dockerfile 语言服务器、`marksman` |
| 代码与协作 | Git、Git LFS、`gh`、OpenSSH、`rsync`、`ripgrep`、`fd`、`jq`、`yq` v4、`patch`/`diff`、ShellCheck、`shfmt`、常见归档解压工具（含 `unrar`/`cabextract`） |
| 排障与数据 | `ip`/`ss`/`dig`/`nc`/`socat`/`whois`/`nmap`/`iperf3`、`lsof`/`strace`/`ltrace`/`gdb`、`htop`/`ncdu`、SQLite / PostgreSQL / MariaDB / Valkey(Redis) 客户端、DuckDB、Miller、HTTPie、`aria2`、`yt-dlp` |
| 文档与媒体 | Pandoc、LibreOffice（`soffice --headless`）、Poppler/`qpdf`/Ghostscript/MuPDF/`img2pdf`、Tesseract OCR（英文、简繁中文）、ImageMagick、librsvg、WebP/AVIF/JPEG XL/HEIF 编解码、`optipng`/`pngquant`/`jpegoptim`/`gifsicle`、ExifTool、Graphviz、`qrencode`/`zbar`、FFmpeg、SoX、MediaInfo |
| Python 库 | 系统 Python 可直接 `import`：numpy、pandas、scipy、matplotlib、Pillow、requests、BeautifulSoup、lxml、PyYAML、openpyxl、XlsxWriter、python-docx、pypdf、ReportLab |
| 图形桌面 | TigerVNC (`Xvnc`)、Openbox、受管策略加固的 Chromium、中英文与彩色 Emoji 字体 |

系统软件包记录在 `/usr/local/share/oryn/packages.txt`。ShellCheck、Pandoc、rust-analyzer、marksman、phpactor 从上游 GitHub 发行版安装（ARM 仓库缺包，双架构统一来源），版本记录在 `/usr/local/share/oryn/upstream-versions.txt`。在项目 `setup` 脚本里增装系统包用 `sudo pacman -Syu --needed --noconfirm <包名>`。镜像已启用 [BlackArch](https://blackarch.org/) 源（双架构）：只导入经钉定签名主密钥校验的 keyring 与镜像列表，不预装安全工具；该源排在官方源之后，同名包仍取官方版本，其中的工具同样用上面的命令安装。跨架构构建 ARM64 镜像见 [`development.md`](development.md#跨架构构建-arm64-镜像)。

## 工作台

### 转录与工具卡

- **工具卡**：头部显示这次调用的意图，收起、展开都一样；没有意图时才显示参数摘要。展开体上半是输入，下半是输出井，用底色区分，不再写「输入 / 输出」标题。
- **当前一批默认展开**：线程工作中时，最后一条助手消息里的工具调用（并行调用算一批）默认展开；模型开始下一批或回合结束时自动收起。手动开合过的卡片以用户为准。
- **语法高亮**：约 60 种语言；diff 按文件语言高亮代码部分，grep / ast_grep 结果按文件逐段高亮，命令行统一按 bash 高亮。
- **bash 的 Pseudo 视图**：bash 卡默认显示 Oryn Core 生成的 Pseudo 视图（令牌着色、失败标 ×、跳过的步骤弱化、变量终值），可切回原始命令。运行中随执行轨迹更新，正在执行的行首标 `›`，其余行弱化。Runner 线程只有最终结果带 Pseudo 视图。
- **思考档位**：转录回合头把模型放进胶囊，右下角用图标标出该回合的思考档位。首屏 role 弹层直接选中实际生效的档位，不再有「默认」项：显式指定的档位优先，其次是模型自带的默认档位，再次是会话起始档位（default role 的显式档位或 `config.yml` 的 `defaultThinkingLevel`），最后按模型能力收窄。推不出实际值时只列可选档位、不选中。

### 工程面板

工作台右侧内置以下面板：

| 面板 | 内容 |
|---|---|
| 信息 | CPU / 内存采样曲线、10 段彩色上下文构成、可交互待办、子 Agent 派发树 |
| 改动 | 实时 Diff、暂存与取消暂存、推送并开 PR |
| 文件 | 文件树、编辑器与浏览器内预览（见下） |
| 终端 | Web 终端，256K 字符快照续传，流控背压恢复 |
| Portal | 经泛域名 `https://t-<短码>-p<端口>.<portal_domain>/` 暴露开发服务器，支持限时公开预览 |
| 桌面 | 远程桌面（TigerVNC + Openbox + Chromium），跨线程用读写锁复用登录态 Profile |
| Artifacts | 本线程产出的 artifact |

### 文件预览

文件面板按格式自动选择预览，无需下载：

| 类别 | 支持 |
|---|---|
| 代码与文本 | 语法高亮编辑 |
| 可渲染的文本 | Markdown、CSV / TSV、HTML、Jupyter Notebook、mermaid 与 SVG，可在「预览 / 源码」间切换，预览跟随未保存的草稿 |
| 文档 | PDF（pdf.js 懒渲染、可缩放、可选中文字）；Word、Excel / ODS / Numbers 与 PowerPoint 在本地解析渲染，不经第三方服务 |
| 图片 | 常见位图可缩放并显示透明底；HEIC/HEIF、TIFF（多页）与 PSD 在本地解码 |
| 其它 | 音视频按 Range 流式播放；字体显示样张与字形；zip / tar / tar.gz 列出目录；STL / OBJ / PLY / glTF / GLB 3D 模型可交互查看 |

HTML 类内容放进不执行脚本、不发网络请求的沙箱帧。

### 收件箱与命令面板

- **侧栏「需要你」**：汇总等待输入或出错的线程，子线程状态上卷到父行。
- **`/inbox` 分诊台**：顶部是大号待处理数与状态筛选胶囊，分节按紧急程度排列：等你处理 → 出错 → 已完成未读 → 运行中。每行写明在等你什么：未作答的问题原文、完成摘要、出错原因或当前意图。
- **一触作答**：可一触作答的提问在行下方直接列出选项按钮，点选即作答；已在别处作答时提示并撤回。
- **出错说人话**：出错原因先归类成一句人话（镜像拉取被拒、找不到镜像、容器命令非零退出、连接超时等）并带上镜像名、退出码等小签，原文降为一行等宽小字。
- **命令面板 `Ctrl/⌘ K`**：跳转线程、项目、Space、Artifacts、设置与常用操作，并对全部对话正文做全文搜索（SQLite FTS5 trigram 索引，中文无需分词；fork 拷贝的同一条消息只计一次，命中后直接定位到该条消息）。

### 输入框

- **引用回复**：在 agent 回复里选中一段文字即可「引用回复」，引用以 Markdown 引用块随消息发出（运行中自动走 steer）。有选区时转录暂停自动贴底，流式输出不会把选区冲走。
- **`#` 引用**：键入 `#` 可以把 GitHub issue / PR 连同评论作为附件引用。
- **下一条消息预测**：在 设置 › Agent › 输入框 打开后，每轮正常完成都会用当前模型猜你接下来要发的消息，以灰色文字显示在空输入框里（omp `composer.predictions`，Oryn Core 把它接进了 RPC 模式）。按 Tab 或 → 插入，不会发送；触屏上点一下灰色文字也能插入。打开「多个候选」后最多给出 4 条，插入一条后再按 Tab 换下一条。草稿只要还是某条预测的开头，提示就一直跟着显示；新回合开始、清空上下文或跳转会话树时，旧预测自动撤回。默认关闭：每完成一轮都会多发一次计费请求，请求带着整段对话。

### 远程桌面与人机控制

远程桌面有统一的控制状态（空闲 / Agent 操作中 / 用户已接管 / 等你操作），由 orbd 权威维护，所有设备一致。Agent 回合中面板默认只读观看；用户一键接管后，Agent 的浏览器 CDP 命令被挂起并经 steer 告知，交还或超时自动恢复。遇到登录、验证码、二次验证时，Agent 调用 `desktop_ask_user` 阻塞等待，桌面面板横幅、线程卡片、推送与 Telegram 都能点「完成 / 跳过」作答。

## Agent 配置

### 图形化配置，保存即生效

设置页「Agent」分组把技能、子 Agent、模型角色、提示词与上下文、命令 / 规则 / 提示片段、扩展 / hook / 工具做成表单。编辑 `config.yml` 时保留原有注释，启停沿用 Oryn Core 的 `disabledExtensions` 与 `task.disabledAgents`；原始文件浏览器保留为「高级」入口。

保存后数秒内下发到所有运行中的 orb。技能、命令、规则、上下文文件与 MCP 在会话空闲时热重载；hook、扩展模块、自定义工具等需要重启的改动，页面顶部列出受影响的会话，可一键安排空闲时懒重启。

### 项目级中央配置

每个项目在控制面另有一层 Agent 配置，内容可以是规则、技能、子 Agent、命令、提示片段、扩展、hook、工具、`mcp.json`、`config.yml` 与 `AGENTS.md`。在项目页的「Agent 配置」卡片里编辑，或用 `oryn config push|pull --project <项目>` 同步。

- 每次 setup / resume 时，这一层原样镜像到 orb 的 `~/.oryn/project-config/.omp/`，不写进仓库工作区，`git status` 保持干净。
- 同名条目的优先级是「项目层 > 仓库 `.omp/` > 全局」；`AGENTS.md` 与仓库里的并存。
- `/omfg` 生成规则后可以选「本项目」或「全局」，规则写回中央，由新建或唤醒的 orb 拿到。

约束：

- 全局与项目两层合计的下发编码不能超过 88 MiB，超过时写入返回 413。
- `models.yml` 只能放在全局层。
- 规则、`.md`、`.yml`、`.json` 都必须是 UTF-8 文本。
- 为防止不可信仓库借符号链接把本机文件（如 `~/.ssh`）传上去，`config push --project` 拒绝以下情况：推送目录本身是符号链接，或其中有指向目录外、无法解析、指向被排除路径的链接。全局推送仍照常跟随 `~/.omp/agent` 里指向别处的链接。两种推送都只收普通文件，`--exclude` 与 `.orynignore` 同时按链接名和真实路径匹配。
- 同一线程同时只能有一个 `/omfg`。
- `/omfg` 写回中央的规则必须与确认时的预览一致；中央已有同名但内容不同的规则时不覆盖，需要到 Agent 配置里手工合并。

### 中央模型目录

控制面用钉定版本的 Oryn Core 一次性运行 `omp models` 生成模型列表（`models.yml` 自定义 provider 与有密钥的内置 provider，附各自的思考档位与模型自带的默认档位）。设置页、首屏与线程内三处共用，无需打开线程或唤醒 orb；配置变化后自动重算，24 小时过期，也可手动刷新。

### 头像

默认 Agent 身份、全局助手、每个 role（含内置 role 与不在轮换里的 role）和每个子 Agent 都能设头像，内置 role / Agent 自带默认 Nerd 字形。子 Agent 总览、详情与 task 卡片用头像加光环动效区分运行、排队、完成、失败与仅存转录；公开分享页只带出分享范围内实际用到的 Agent 头像。

## 全局助手与 Space

控制面内置 `@host` 协调宿主，按需在独立系统账号、Landlock LSM、seccomp-bpf 与 Linux 命名空间围成的严格沙箱中拉起协调进程；沙箱里唯一能连的是回环上通往 orynd 的出站代理端口。

### Space 协调者

只开放内置 `read` / `write`，负责拆解阶段目标、并发创建与检视工作线程，并在需要人工决策时汇总提醒。它不能联网、没有 MCP，也不能读写或操控 Runner 线程。

### 全局助手

按三层执行，能自己做的就不派线程：

1. **自己动手**：omp 自带的 `web_search`、用 `read` 读网页、`generate_image` 生图，全局 `mcp.json` 里的 HTTP / SSE MCP 服务器，以及 artifact（含截图、导出进文件库、导入、从回收站恢复）。
2. **遥控「助手的电脑」**（见下）。
3. **派出与管理 agent**，操作面接近主人：在项目、Space 或 Runner 目录里开线程（可指定 provider、类型、已有 orb、role）；给工作线程或 Space 的协调对话发话并收回报；改标题与模型、中止、fork、唤醒；读写线程 orb 里的文件并查看 git 改动；新建和修改项目、Space、项目 Agent 配置、定时与 GitHub 触发规则；暂停和唤醒 orb；管理分享。

**联网的边界**：沙箱本身仍连不到外网。助手的请求只经 orynd 的正向代理出去，代理在连接时按实际 IP 判定，只连公网地址；MCP 的鉴权头由 orynd 注入，不进沙箱。「设置 → 全局助手」有联网总开关，MCP 服务器可以逐个关掉。

**确认卡**：删除（线程、项目、Space、定时、触发规则、orb）和让未登录的人能访问的分享（线程分享链接、Portal 公开预览、以访问码或公开方式发布 artifact）不会直接执行。对话里先出现确认卡，同时推送带「是 / 否」的通知；主人点了「是」才由 orynd 执行，结果以自动消息回到对话，24 小时没人答就过期。新建、修改、归档、发消息不需要确认。密钥、provider、设备与登录相关设置不对助手开放。

### 首屏直达

首屏输入框的 role 胶囊可选全局助手、轮换里的 role，或直接指定模型与思考档位；胶囊直接显示当前生效的选择，档位以右下角图标标出。选全局助手时执行位置与项目胶囊收起、输入框换上流光描边，发送即新建助手对话并带上首条消息、附件与所选模型。「新对话默认对象」存在服务端，各设备一致。

### 主人记忆

全局助手有一份只关于主人的记忆（bank `oryn-owner`，与工程记忆同一个 Hindsight）。

- **写入**：助手对话与工作线程空闲 30 分钟、归档或交付后，主人的话连同助手的最后一条回复按回合写入。写入前用与分享相同的密钥扫描规则和密钥库值脱敏；工具调用、thinking 与长代码块不写。
- **整理**：每天整理出主人的长期主题（固定的「主人画像」加至多 12 个动态主题），在助手启动时附在系统提示词末尾。
- **工具**：`recall_memory`（判断模型重排，带出处与可信度分档）、`search_history`（原文全文搜索）、`recall_engineering`（只读工程记忆）、`remember`（主人原话直接生效，否则待确认）。
- **管理**：「更多 → Agent → 记忆 → 助手记忆」可查看注入预览、主题、事实与出处，让事实失效、按来源遗忘、排除对话或项目，审核明确记忆。

### 文件库

以下文件统一进入文件库：

- 助手对话里的附件；
- 助手写到 `~/library/out/` 的文件和生图结果（`~/tmp/`），用 `save_file` 存入；
- 从工作线程的 orb（`fetch_thread_file`）或助手的电脑（`computer_download`）取回的文件；
- `export_artifact` 的导出物；
- orb 内 Agent 用 `oryn library put` 存入的文件。

文件库同步进协调宿主沙箱，供 omp 自带的 `read` 读取（PDF、Office、图片、压缩包、SQLite 等）。助手开新线程时可以附带库文件，也能用 `put_thread_file` / `computer_upload` 把库文件放进线程的 orb 或自己的电脑。全局助手页的文件库面板可以预览、下载、删除、切换共享并查看来源。

### 主线

助手分区最上面置顶一条永久对话，显示助手的名字和头像。它不能归档或删除，不进对话列表；打开助手分区时仍落在最近的普通对话。主线里的消息有三个来源：勾了「交给助手」的定时（`new_thread`）与 GitHub 触发规则的运行结果、主人在主线里让助手派出的线程的回报、助手在主线里给自己设的提醒。

- **交给助手**：每次运行的结果在 15 秒内合并成一条自动消息进主线，该运行不再单独推送完成通知（出错、等输入照常通知）。助手结合记忆判断值不值得说；不值得就调用 `stay_silent`，这一轮不置未读、不推送。
- **提醒**：主线可以有多条线程内定时（按名字管理，至多 50 条）。
- **分段运行**：为了让每次输入的上下文保持很短，主线一段一段地跑。每段开头清空上下文，只带一份备忘和最近 12 条消息（主人的话与助手说出口的话，沉默的回合不算）。备忘由助手用 `update_main_notes` 整体改写，1500 字以内，主人可以在页头查看和编辑。主线静止 10 分钟或满 8 个回合后，下一条消息开始新的一段。
- **页面**：只加载最近 600 条，可以向前翻页。自动来料和沉默的回合折叠显示；每条助手消息下有「在新对话中继续」，会把那段话作为引用带进一个新对话。

### 助手的电脑

全库只有一台属于助手的普通 orb，主线与所有助手对话共用。第一次用到时创建，空闲时照常冻结，调用前自动唤醒。

助手进程仍留在协调宿主沙箱里，经 `computer_bash`、`computer_read` / `computer_write` / `computer_edit`、`computer_grep` / `computer_glob` / `computer_find`、`computer_ast_grep` / `computer_ast_edit`、`computer_eval`、`computer_lsp`、`computer_wait` 遥控 orb 里一个不调模型的 omp 工具执行器（Oryn Core 的 `oryn_tool_*` RPC）。输出流式回传，图片原样到达，取消会传到 orb 里；`computer_upload` / `computer_download` 在电脑与文件库之间搬文件。这些工具的卡片与对应的普通工具相同。

`/assistant/computer` 页面可以查看状态，唤醒、暂停或重置（删除后下次用到时重建），浏览文件，查看远程桌面并接管。provider 与类型在「设置 → 全局助手」里选。

### 定时任务

| 模式 | 命令 | 行为 |
|---|---|---|
| 线程内定时 | `oryn schedule set` | 在单个已有线程内按 Cron 或固定间隔触发巡检提示词 |
| 项目级定时 | `oryn schedule add` | 每次拉起全新隔离线程，按 `keepLast` 自动归档历史运行 |

### 成本与产出统计

控制面直接从会话条目入库模型调用、工具执行与生命周期事件（自动剔除分支 fork 造成的历史重复计费），按模型、项目、Space 或用途穿透统计成本、缓存命中率、TTFT 与交付提交数，支持 CSV 导出与日 / 月预算阈值系统通知。

## 记忆、GitHub 与 SSH

### Hindsight 记忆

orb 与 Runner 里的 agent 共用一个自托管的 [Hindsight](https://github.com/vectorize-io/hindsight) 记忆库，回合结束自动写入，下一轮自动召回。

- **隔离**：控制面网关替 agent 填写项目标签，同一项目的各个线程共享记忆，不同项目互不可见。
- **全局层**：写进全局层的内容（个人偏好、跨项目经验）先进候选队列，主人批准后才对所有项目生效。
- **模型中转**：Hindsight 自己调用的模型、嵌入与重排全部经控制面中转，可在同一协议内随时换模型。Gemini Decisions 重排使用随项目构建的 `gemini-rerank` 适配器。
- **管理**：「更多 → Agent → 记忆」可以浏览、搜索、按线程遗忘、审核候选、刷新 mental model、查看用量和导出；删除项目时，它的记忆一并删除。

部署见 [`deploy.md` 第 14 节](deploy.md#14-hindsight-记忆服务m16)。

### GitHub

部署见 [`deploy.md` 第 13 节](deploy.md#13-github-集成m15)。

- **OAuth App 授权**：在设置页填入自己注册的 GitHub OAuth App，连接「我的账号」与「Agent 账号」。触发规则、项目级定时与发布哨兵建的全自动线程默认用 Agent 账号。
- **orb 内免登录**：账号连接后，任何工作 orb 里的 git 与 `gh` 都可以直接使用（runner 与协调宿主除外）。真实令牌由出站代理注入，不进 orb。
- **PR 与回流**：项目按仓库地址自动绑定。线程推送的提交成为 PR 的头提交后，线程页头显示 PR 状态与 CI 结果，合并即标记已交付。评审、评论与 CI 失败会回流进线程，agent 自己的写操作不会回流。
- **事件来源**：有 admin 权限的仓库走仓库 webhook，并轮询投递日志补漏；其余仓库走 REST 轮询。
- **robomp 预设**：「分诊」「PR 评审」「发布哨兵」三个触发规则预设。分诊会登记 issue 状态，并支持实现授权、任务时限、完成提醒与问题自动关闭；PR 评审只发 COMMENT。
- **门禁**：项目可开启推送 / 开 PR 门禁，由出站代理强制执行：开启后推送或开 PR 前须先用 `github_prepare` 跑过检查。
- **发布哨兵**：发布提交的 CI 被阻塞时开线程修复，至多 5 轮，直到版本变为 green 或 failed。
- **审计**：agent 的每个 GitHub 写操作都记审计，保留 30 天。

### SSH 主机

在「更多 → Agent → SSH 主机」登记目标机（全局，或只对某个项目可见）与钥匙，orb 里的 agent 直接用主机名访问：`ssh://<名>/<路径>` 读写文件、终端工具里 `ssh <名>`、`ssh_exec_many` 一次在多台机器上并发执行。

- 连接都由控制面网关代拨（支持跳板链、首次连接固定指纹），私钥不进 orb，每条命令记审计。
- 全自动线程只能用明确放行的主机。
- 设置页可以测试连接、查看负载 / CPU / 内存 / 磁盘快照，直接打开网页终端或浏览远端文件。

部署见 [`deploy.md` 第 15 节](deploy.md#15-ssh-主机与控制面网关m18)。

## Artifacts

orb 线程、Runner 线程与全局助手的 Agent 都能用 `create_artifact` / `update_artifact` 等工具做出页面、画布、文档（Markdown）与幻灯片。

- **版本**：每次写入生成一个新版本，冲突按文件粒度检查；可以恢复旧版本，删除的进「最近删除」保留 30 天。
- **隔离渲染**：每个 artifact 在不透明源的沙箱帧里显示（配置了 `portal_domain` 时用 `a-<令牌>` 泛域名，否则用主域的 `/_af/` 路径），只能加载自身文件与白名单 CDN。控制面主机上带沙箱的 headless Chromium 负责渲染缩略图、PNG 与 PDF。
- **入口**：「更多」分区的 `/artifacts` 总览页（大卡片预览、类型标签、搜索、筛选、导入、回收站）；线程右面板的 Artifacts 标签；转录里的 artifact 卡片；命令面板。
- **查看器** `/artifacts/<id>`：版本历史、评论（可发给线程）、元素直接编辑、导出（HTML / zip、Word、Markdown、PowerPoint、PDF）和分享。
- **画布编辑器**：工具条、图层与属性面板；画框可声明调节参数，在「调节」面板里即时预览并写回；选中的画框与标注可连同合成图「交给 Agent」。
- **演示与聚焦**：页面与画布可铺满窗口逐页演示，单个画框可全尺寸聚焦查看；手机上画布只读，演示可左右轻扫翻页。
- **在场**：查看器与卡片显示正在读、正在改的线程头像；画布上显示 agent 指针，可以「跟随」它的镜头。
- **页面能力**：页面可通过 `oryn.use("db" | "files" | "sample")` 存数据、只读项目文件、向用户的模型提问；后两项首次使用需要用户同意，可在 设置 › Artifacts 收回。
- **发布**：可以发布成私有、访问码或公开的链接；发布访问码与公开链接前会做密钥扫描。

## 客户端

### 原生壳

Windows / macOS / Linux 桌面端与 Android、iOS 客户端共用一套 Tauri 2 工程（`web/src-tauri`）。客户端本身只携带极简的配对连接页，配对后加载所连服务器自身的 Web UI，服务端升级即全端同步。设备令牌只驻留在 Rust 进程与系统凭据库（macOS 钥匙串、Windows 凭据管理器、Linux Secret Service、Android Keystore），网页脚本无法读取。

### 桌面

- 无系统标题栏的同源设计：macOS 保留原生红绿灯，与侧栏品牌行同一行，启动时实测校准位置；macOS 26 起挂 unified 工具栏，卡片按窗口外框做同心圆角。Windows / Linux 自绘窗口按钮，Linux 遵循 GTK 标题栏布局。
- 系统托盘、全局唤醒快捷键与 `oryn://` 深链接。

细节见 [`desktop.md`](desktop.md)。

### 移动

- `<600dp` 窄屏：屏幕圆角同心的下沉式侧栏、悬浮毛玻璃底栏、右上角快捷视图菜单、系统分享接收。
- 推送：RFC 8291 端到端加密的 **UnifiedPush**（零 Google 依赖）与加密 **FCM** 双通道。工具审批、确认这类可一触作答的提问直接在通知上带「允许 / 拒绝」等按钮（Android 需解锁后点按，浏览器 Web Push 同样支持），后台作答不拉起界面；请求在别处作答后旧按钮自动撤回。
- iOS 26：原生 Liquid Glass 线程页头与系统 `UITabBar` 底栏，随下沉侧栏的卡片开合；App 图标可在「设置 → 外观」换成品牌色版本。iOS 经 TestFlight 分发，APNs 推送尚未完成。

细节见 [`mobile.md`](mobile.md)。

### 终端

- `oryn thread attach <线程>`：在终端内流式查看转录，支持实时输入引导（steer）、作答与计划审批。
- `oryn sync <线程>`：把远端 orb 内的提交与未提交改动实时镜像到本机干净检出（`Ctrl-C` 自动还原，`--apply` 一次性落盘）。

## 安全边界

完整威胁模型与加固清单见 [`deploy.md` 第 7 节](deploy.md#7-安全)。

### 出站代理凭据隔离

默认 `proxy` 模式下，导入的 `models.yml` 明文 API Key 与 `git/*` 令牌存入控制面主密钥加密库，下发到 orb 的配置只含 `oryn-ph-…` 占位符。请求经 `orbd` 本地端口 `127.0.0.1:7811` 回到 `orynd`，按绑定的上游主机注入真实值，并把响应中回显的凭据脱敏回占位符。只有显式设为 `env` 模式的自定义密钥，或未绑定上游 `hosts` 的模型 Key，才会降级为环境变量下发。

### Git 访问范围

- **github.com**：用 OAuth 账号令牌注入，能读写哪些仓库取决于账号自身权限，不按项目仓库限制。项目开启推送 / 开 PR 门禁时，出站代理要求先用 `github_prepare` 跑过检查；只读评审线程在推送协商阶段即被拒绝。
- **其它 Git 主机（PAT）**：绑定了 `https://` 仓库地址的项目 orb 只允许对该仓库执行读写（`git-upload-pack` / `git-receive-pack`），同主机其它仓库一律 `403`；未绑定项目的 orb 只放行只读克隆，拒绝一切推送。

### 令牌轮换与单向信任

- **orb 令牌**：`oot_…` 由 `HMAC(key, orbID : 代数)` 派生，每次创建、唤醒及拍摄快照后立即轮换，快照磁盘内不留有效令牌（Firecracker guest 令牌只驻留内存 `tmpfs`）。
- **Runner 单向边界**：控制面永不向 Runner 下发模型配置或环境变量密钥，只开一条仅通往记忆网关 `/memory/` 的专用通道（其它出站路径一律 403，Hindsight 的 key 不进 Runner）。
- **谁能操控 Runner**：只有主人与全局助手。全局助手可以像主人一样在 Runner 上新建线程、发消息、读取、归档、读写线程目录里的文件，删除要主人点确认卡；Space 协调者与 orb 内 Agent 禁止读写或操控任何 Runner 线程，普通 orb 里产出的内容不能经它们变成你机器上的指令。
- **协调沙箱**：全局助手所在的沙箱只有一个出口，即回环上的出站代理端口；联网与 MCP 都经 `orynd` 的正向代理（只连公网地址，MCP 鉴权头由 `orynd` 注入）。网页、搜索结果与 MCP 的返回都作为不可信数据交给助手。
