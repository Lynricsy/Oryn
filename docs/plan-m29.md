# M29 — 画布升级为设计功能：实施计划

> 迁移号取 `0059`：`0058_push_gateway.sql` 已存在。行号只作提示，动手前按符号重读。标「未验证」的结论都由 Step 0 的探针兜底。
> 本文自包含；工作区由多个会话共用，提交时只带本步骤改的行。

## 已定决策（主人，2026-11-24）

- D1 画框源码：TSX + Tailwind v4 + npm；有 orb 时 vite dev server 经 Portal 实时预览，每回合 `vite build` 把源码与静态产物封存为一个版本；查看、发布、缩略图、渲染只用静态产物；没有 orb 的助手与 Runner 继续用 HTML 画框。
- D2 设计稿存放在 artifact 库（唯一事实源），orb 工作副本只是缓存；另加导出为工程、部署到 box。
- D3 npm 不额外限制（不加 `--ignore-scripts`），边界是 orb。
- D4 直接升级现有 `canvas` 类型，不新建 design 类型。
- D5 本期范围：品质、迭代、原型、交付；组件面板不做。
- D6 内置技能 `oryn-design` 由 Oryn 自写，只讲画布与工具契约；流程改写自 oil-ui（MIT），可计数硬规则取自 HyperSkills `frontend-design` 并下沉为探针，探针规则同时写回 HyperSkills 作为 evals 场景；流程由工具与 hook 硬保证；设计工具有闸门。
- D7 便签自动投递：设计画布默认开，去抖 30 s，可按 artifact 关闭。
- D8 设计检查不过：`session_stop` 每回合至多强制续一轮。
- D9 封存时 orbd 自动 `npm install`（含 postinstall），不加限制。
- D10 Vetta 探针规则：只取判据、独立实现，NOTICE 写「规则思路参考 Vetta」，不看其源码照搬。

## 探针结论

2026-11-24 实测（探针代码不入库，临时文件已删）。环境：vite 7.3.8、@vitejs/plugin-react 5、tailwindcss 4、react 19.1.1；控制面 Chromium `/usr/bin/chromium`；生产 orb 镜像 `oryn-orb:prod-91068c50`（node v26.13.1、`/usr/sbin/chromium`）；omp 19.1.1。

- **P1 通过**：两帧（login、signup）多入口产物建成画布 artifact，经真实帧服务（`NewHost` + `OpenFrame`，泛域名 `a-<令牌>.localhost`）打开。WebKit 与 Chromium、顶层直开与 `sandbox="allow-scripts allow-forms"` iframe 内嵌四种组合全部：渲染、Tailwind 自定义 token 生效（`text-brand` → `rgb(15,118,110)`）、点 `<a href="./signup.html">` 跳转成功、React 状态更新正常。控制台只有既有的 `Unrecognized CSP directive 'webrtc'` 与 iframe 宿主页的无关 404，无 CORS/CSP 拦截。不需要失败分支（`modulePreload` 保留）。
- **P2 通过，修正一处契约**：12 帧产物 30 个文件、304 KiB（React 共享 chunk 186 KiB，gzip 59 KiB）。踩到两点，写进 T3：
  1. vite 配置必须放在**引擎根**并从引擎根加载（`--config <engine>/vite.config.mjs`，设计目录经 `ORYN_DESIGN_DIR`）。放在设计目录时 vite 用 esbuild 打包配置，按设计目录解析插件，报 `Missing "./internal" specifier in "vite"`。
  2. Tailwind 入口 `@import "tailwindcss"` 必须在**引擎**的 `styles.css` 里（设计目录解析不到 `tailwindcss` 包）；设计侧 `src/theme.css` 只写 `@theme {…}`，由引擎插件（`enforce:"pre"`，**排在 `@tailwindcss/vite` 之前**）把占位符替换成 `@import "<work>/src/theme.css"; @source "<work>/src";`。排在之后时自定义 token 与类都不生成。
- **P3 通过（含 WebKit 条件）**：真实 portal 中间件 + 握手（`AuthHandler`、`Middleware`）+ TLS，upstream 指向真实 vite dev server（`server.allowedHosts:[".localhost"]`）。Chromium：主站页内 3 个 `PORTAL_SANDBOX` iframe 经 `/__oryn/login?next=` 全部登录、出图，改源码后 57 ms 内三帧同时热更，无报错。WebKit：iframe 内握手停在主站 `/login`——主站会话 Cookie 是 `SameSite=Lax`，跨站 iframe 子资源请求不带它，`/portal/auth` 视为未登录。**顶层打开一次该 portal 地址**（拿到 host-only 的 portal Cookie）后，iframe 内直接回跳、渲染，热更 46 ms。这是现有 `PortalPanel` 的同一限制，不是新问题。处理：T6 的 live 帧在收到 portal 主机的未登录跳转（帧 URL 落在主站 `/login`）时退回静态帧，并显示「在新窗口授权实时预览」按钮（`window.open(portalFrameSrc(...))`，授权后刷新 live 帧）；Chromium 系（含桌面壳与 Android WebView）无需此步。连接上限 `maxOrbConns = 256`，6 个 live 帧远低于此。
- **P4 通过，修正两处**：omp 19.1.1 RPC 模式（`--mode rpc -e <扩展>`），模型 `anthropic/claude-sonnet-5-5`：
  1. `input` 对 RPC 的 `prompt` 与 `follow_up` 都触发，`source:"rpc"`，`text` 为原文。
  2. `session_stop` 返回 `{continue:true, additionalContext}` 后：先发 `agent_end{willContinue:true}`，随后 `before_agent_start{reason:"prompt", prompt:<additionalContext>}`（**reason 不是 revive**），续跑结束时 `session_stop{stop_hook_active:true}`、终态 `agent_end`（无 `willContinue`）。所以 T5 不能凭 `before_agent_start` 开新回合：续跑也会触发它。
  3. 中止（RPC `abort`）的回合**仍发终态 `agent_end`**，但**不发 `session_stop`**。A1 成立；中止回合照常封存，只是没有「检查不过强制续一轮」。
  4. 事件顺序：三组实测里都是 `input` → `before_agent_start` → `agent_start`，即 `before_agent_start` **先于**本 run 的首个 `agent_start`。忙时 `follow_up`：先 `input`，再在当前 run 内触发 `before_agent_start{prompt:"<follow_up 原文>"}`（无新的 `agent_start`），之后一个 `session_stop` 与一个终态 `agent_end`。第一次实测在其后又出现过一组 `agent_start → session_stop → agent_end`，但那次 A 阶段只等到第一个 `agent_end` 就继续，强制续跑与之交错；第二次（等足 A 的两个 `agent_end`）没有出现。**不据此断言 follow_up 一定形成独立 run**，T5 的回合边界不依赖它。
- **P5 通过**：控制面——经真实帧服务（不透明源 CSP）用 `--dump-dom --virtual-time-budget=5000` 拿到 React 渲染后的探针 JSON（`text-clipped`，`detail:144`）；以 `nobody` 带沙箱运行也通过。现有渲染器用的就是 `--virtual-time-budget=5000`，直接复用；`--timeout=8000`（不带 virtual time）**拿不到**结果，弃用该分支。探针脚本须由产物 HTML 引用并与产物同源提供（漏放 `probe.js` 时 DOM 里没有结果节点，排查时注意）。orb 内——`/usr/sbin/chromium` 以 uid 1000 **不加 `--no-sandbox`** 即可，结果一致。
- **P6 失败，走失败分支**：`defaultInactive` 工具在回合中途经 `setActiveTools` 激活后，omp 以 `tool_addition` 系统块 + `defer_loading` 声明它；本机 Anthropic 兼容网关（codehub）对该请求回 400 `The request format is invalid or unsupported by the upstream provider`（去掉控制块重放后同一端点不再报格式错）。补测：改在**第二个回合开始时**（`before_agent_start`）激活，同样产生 `tool_addition` 块并 400——会话里只要工具集相对首个请求变化，omp 就走 tool_addition 通道，与是否在回合中途无关。Oryn Core 的 `oryn-tool-change-fallback.ts` 只匹配 `tool_addition|tool_removal|tool_reference_unresolved` 字样，这个网关的报错文本不含它们，降级不触发。**裁决**：T5 不用 `setActiveTools` 做闸门，设计工具在 orb 线程里始终注册且激活；闸门下沉到处理器（没有本线程工作副本时，除 `design_open` 外一律回 `no_design` 并提示先 `design_open`）与工具描述的反向触发段。Core 侧待办（不在本计划内）：降级判据改为「400 且请求体含控制块」；修好后再评估是否恢复按需激活。

## 目标

把 `canvas` 类型升级为可用 TSX + Tailwind v4 + npm 的设计画布（D1–D6）。有 orb 的线程实时预览并按 `file:line` 定位，每回合封存一个「源码 + 静态产物」版本。静态产物承担查看、发布、缩略图、渲染。在其上补齐品质（探针检查、方向发牌、token）、迭代（回合 = 版本、标题、便签投递）、原型（跳转、预览、设备外壳）、交付（样机导出、导出工程、部署 box）。

## 核心判断

1. **回合内的权威状态在 orb 工作副本，不在 artifact 库。** 现有「每次 `update_artifact` 一个版本」与「回合 = 版本」冲突，所以构建、封存、检查、截图都由 **orbd（Go）** 做，不放在扩展钩子里。omp 扩展处理器有 30 s 预算（`runner.ts:150`），`vite build` 可能超时。扩展只在 `input`、`before_agent_start`、`agent_end`、`session_stop` 四个点向 orbd.sock 发短请求。
2. **静态侧零改动。** 多入口构建，每帧一份 `dist/<id>.html`，`page.file` 仍是版本内的 `.html`（`doc.go` 的 `pageFile` 校验不变）。
3. **不用 react-router。** 帧间跳转是引擎提供的 `<Link>`，静态产物里渲染成同源相对链接 `./<id>.html`。现有运行时已放行同源链接（`start.ts:186`），这样绕开不透明源下 `history.pushState` 的不确定性。
4. **测量用 `--dump-dom`，不上 CDP 或 playwright。** 探针脚本把结果写进 DOM，Chromium `--headless=new --dump-dom` 把序列化 DOM 打到 stdout，Go 抽 JSON。同一份探针在 orb（工作副本）和控制面（已封存版本、HTML 画框）各有一个启动器，零新依赖。

## Step 0 风险探针

每个探针只回答一个问题，结论写进 `docs/plan-m29.md`「探针结论」后再开工。探针代码不入库，沿用 `internal/artifacts/browser_probe_test.go` 与 `bun run probe:artifact-browser` 的做法。

| # | 问题 | 最便宜的验证 | 通过判据 | 失败分支 |
|---|---|---|---|---|
| P1 | 不透明源沙箱能否运行 vite 多入口产物？ | 手工 `vite build`（`base:"./"`，两个入口，共享 chunk）；产物建成 canvas artifact，画布里打开并点帧间 `<a href="./b.html">` | 两帧渲染、跳转成功，控制台无 CORS/CSP 报错（`setFrameHeaders` 对 `Origin: null` 回 ACAO null，`policy.go:66-78`） | 关 `modulePreload`，构建改 `inlineDynamicImports` 单 chunk；仍失败则每帧单文件内联（`vite-plugin-singlefile`，引擎 devDependency） |
| P2 | `vite build` 能否稳定输出 `dist/<id>.html` + `dist/assets/**`？ | 引擎插件在 `<work>/.oryn-build/` 生成每帧入口 HTML，`root` 指向该目录，`outDir=<work>/dist` | 输出恰为该布局；12 帧产物不超过 40 个文件、8 MiB（上限 256 / 64 MiB，`limits.go`） | 构建后由 orbd 重命名、拍平；超量则关闭字体子集外的资产内联 |
| P3 | Portal 帧嵌在画布里时，HMR WebSocket 与 Partitioned Cookie 可用吗？ | orb 起 vite（`server.allowedHosts` 放行 `$PUBLIC_URL` 主机、`hmr.clientPort=443`），SPA 里 3 个 iframe 用 `PORTAL_SANDBOX`（`PortalPanel.tsx:41`），先到 `/__oryn/login?next=` | 三帧出图，改源码后 2 s 内热更，Safari 与 Chrome 都过，无 `portal_busy`（`conns.go` 的 `maxOrb`，值未读） | Cookie 被拦：live 模式只在 FocusedPage 单帧开；连接超限：live 帧数上限降到 4；WS 不通：`hmr:false`，由 `design-reload` 消息整帧刷新 |
| P4 | 钩子可靠吗？ | 临时扩展打日志：发 prompt / steer / follow_up、中止、带后台任务的回合 | `input` 的 `source==="rpc"` 拿到原文（`rpc-mode.ts:2664`）；无 `willContinue` 的 `agent_end` 每个 run 恰一次（`agent-session.ts:6411-6492`）；中止后仍有终态 `agent_end`（未验证）；`session_stop` 返回 `{continue:true, additionalContext}` 能续一轮（`shared-events.ts:530`） | 中止不发 `agent_end`：改由下一次 `before_agent_start` 与 orbd 观察到 agent 进程空闲时补封（与 Vetta 一致） |
| P5 | `--dump-dom` 在两处都可用吗？ | 控制面：`chromiumArgs` 把 `--screenshot` 换成 `--dump-dom`，取 stdout；orb：`chromium --headless=new --dump-dom http://127.0.0.1:<port>/frames/x?__oryn_probe=1` | DOM 里有 `<script id="oryn-probe" type="application/json">` 且含 React 渲染后的测量；orb 内不加 `--no-sandbox` 能跑 | orb 里起不来：按 `ORYN_NESTED_DOCKER` 同类条件加 `--no-sandbox`（orb 本身是隔离边界）；virtual-time 下 React 没渲染完：探针改为 `requestIdleCallback` 后写结果，并用 `--timeout=8000` |
| P6 | `defaultInactive` + `setActiveTools` 能在回合中途让新工具对下一次模型请求可见吗？ | 临时扩展注册 inactive 工具，在另一个工具的 `execute` 里激活 | 同一回合的后续请求能调用 | 实测失败（见探针结论）：工具始终激活，闸门放在处理器里 |

## 冻结契约

### C1 canvas.json（`internal/artifacts/canvas/doc.go` ↔ `web/src/lib/artifacts/canvas-doc.ts`）

- `format`：解析接受 `1` 和 `2`。文档带下列任一新字段时写出 `2`，否则写出 `1`，这样 HTML 画布逐字节不变、可回滚。
- 顶层新增可选 `design: {engine: 1, start: <pageId>|null}`。有它即「设计画布」；`start` 是原型起始帧。
- page 新增可选字段：
  - `source`：字符串，匹配 `^src/frames/[a-z0-9][a-z0-9-]{0,63}\.tsx$`。带 `source` 的 page，其 `file` 必须等于 `dist/<同名>.html`；`source` 只在 `design` 存在时允许。
  - `device`：`"none" | "phone" | "tablet" | "browser"`，缺省 `none`。
- `source` 与 `file` 的 set 被拒，只能由封存改；`device` 可 set。
- 测试向量追加到 `schema/testdata/canvas-ops.json`。

### C2 版本内文件布局（设计画布）

```
canvas.json
src/frames/<id>.tsx        默认导出组件；可选 export const frame = {name, width, height, device}
src/frames/_layout.tsx     可选共享外壳
src/components/**  src/theme.css  src/DESIGN.md
src/package.json  src/package-lock.json   设计级依赖（可缺）
dist/<id>.html  dist/assets/**  dist/frames.json
```

`dist/frames.json` = `[{id, name, width, height, device, links:[<id>…]}]`，由引擎构建生成。`src/**` 与 `dist/**` 只经封存写入；对设计画布，`update_artifact` 的 `files`/`edits` 命中这两个前缀时回 `invalid`，提示改工作副本。

### C3 orb 工作副本

```
~/designs/<artifactId>/
  .oryn-design.json   {artifactId, threadId, baseVersion, engine, sealedHash, service, port}
  src/…               与版本内 src/ 同构；src/node_modules 不同步
  dist/               构建输出
  .oryn-build/        引擎生成的入口，不同步
~/.oryn/design/engine/<engineVersion>/    orbd 写出的引擎源
/usr/local/share/oryn/design-engine/<engineVersion>/node_modules   镜像预装
```

- `sealedHash` = 对 `src/**`（排除任意层级的 `node_modules/`）按路径字节序排序，逐个拼接 `<path>\n<sha256(内容) 小写 hex>\n`（`path` 以 `src/` 开头、`/` 分隔），对整串取 sha256 小写 hex。只有一份实现：`internal/artifacts/design` 的 `SourceHash(files map[string][]byte) string`（T4 写），orbd 封存判脏与控制面 `check_json.fingerprint`、`stale` 判定都调它。
- 引擎的 `node_modules` 优先软链到镜像预装目录；版本不符时在引擎目录 `npm ci`（一次，约 1 分钟，对 agent 显示进度）。
- 引擎依赖：react 19、react-dom、lucide-react、vite 7、@vitejs/plugin-react 5、tailwindcss 4、@tailwindcss/vite、@iconify/tailwind4。去掉 react-router 与 html-to-image。
- 受管服务名 `dsn-<sha256(artifactId) 前 10 位 hex>`，`Portal: true`，命令 `node <engine>/node_modules/vite/bin/vite.js --config <engine>/vite.config.mjs`，环境 `ORYN_DESIGN_DIR`、`ORYN_DESIGN_SANDBOX`（含 `runtime.js`、`probe.js` 的目录，dev 以 `/frames/runtime.js`、`/frames/probe.js` 提供）、可选 `ORYN_DESIGN_ALLOWED_HOSTS`。
- `ORYN_DESIGN_SANDBOX` 的内容由 orbd 在 `design.open` 时经内部操作 `fetch_design_runtime` 从控制面取（控制面 `webui.Asset("sandbox/runtime.js")` 与 `sandbox/probe.js`，缺哪个就不写哪个），写到 `~/.oryn/design/sandbox/`。运行时协议面向父页，必须与控制面 Web 同版，所以不嵌进 orbd。
- Host 白名单（安全）：Portal 代理把公开 Host 原样转给 orb（`internal/portal/proxy.go:109`），dev server **不得**设 `allowedHosts: true`（会让任意 Host 读到设计源码）。引擎的放行集合 = `PUBLIC_URL` 的主机名（orbd 受管服务按 portal 模板注入，`services.go:1345` `urlForLocked`；算不出时为空，portal 配好后 `refreshPublicURLs` 自动重启服务补上）∪ `ORYN_DESIGN_ALLOWED_HOSTS`（逗号分隔，完整主机名或以 `.` 开头的后缀，留作手工调试）。两者都空时保持 vite 默认：放行 `localhost`、`*.localhost` 与任意 IP 字面量 Host。放行 IP 不算缺口：防的是 DNS 重绑定，攻击者域名的 Host 必是域名；IP 字面量 Host 只有直连才能发出，而 dev server 只监听 127.0.0.1，能直连的 orb 内进程本可伪造任意 Host；经 Portal 进来的 Host 一定是 `<label>.<portal_domain>`（`portalhost.Base.Match`）。因此不另做 Host 校验。用 `PUBLIC_URL` 而不是让 T4 在 env 里写主机名：动态服务的端口由 supervisor 分配，定义时算不出主机名。T3 验收包含「非白名单域名 Host 被拒」；T4 给引擎补 `PUBLIC_URL` 分支并更新 `Version`。

### C4 orbd.sock 新方法（`internal/orbsock`，runner.sock 不认）

| 方法 | 参数 → 结果 |
|---|---|
| `design.open` | `{threadId, id?, title?}` → `{id, dir, version, devUrl:"http://127.0.0.1:<port>/"}`。无 `id`：建带 `design` 的空设计画布；物化 head；起 dev 服务 |
| `design.turn` | `{threadId, turnId, title}` → `{}`。记下本回合；若有上一回合未封存的脏副本，先以旧 turn 封存 |
| `design.check` | `{threadId, id, frames?:[id], screenshot?:bool}` → `{status, issues:[…], images:[{frame, data}]}` |
| `design.seal` | `{threadId, turnId}` → `{sealed:[{id, version}], skipped:[id]}`。对本线程每个脏副本：依赖变了先装依赖 → `vite build` → ctl `artifact.call` `seal_design` |
| `design.status` | `{threadId}` → `{designs:[{id, dir, dirty, check}]}` |

超时：`seal` 5 分钟、`check` 90 秒。构建失败时不落版本，返回 `build_failed` 加 stderr 尾 4 KiB。

### C5 控制面操作与 agent 工具

- `artifact.call` 的 params 外壳增加可选 `turn: {id, title}`（`orbCallParams`，`agent.go:885`），进入 `Caller.Turn`。orb 扩展每次调用都带。
- 内部操作，不注册成工具：`seal_design {id, baseVersion, files:[{path, source}|{path, remove}], check?}`。控制面按 `dist/frames.json` 对齐 pages（多出的帧 add、自动排位；消失的帧 remove；`name/width/height/device` 只在新增时取用），整体走 `writeCanvas`（`live.go:479`），`canvas.json` 不参与冲突检查，`src/**`、`dist/**` 按 `baseVersion` 检查。
- agent 工具（`oryn-orb.ts`）：在 orb 线程里始终注册且激活（P6：任何会话中途的工具集变化在本机网关上 400）。除 `design_open` 外，没有本线程工作副本时处理器回 `no_design`，提示先 `design_open`；描述首句写用途，随后写「只用于 Oryn 画布 artifact；在项目仓库里写前端不要用」。

| 工具 | 参数 | 去向 |
|---|---|---|
| `design_open` | `{id?, title?}` | orbd `design.open`；建立或接管本线程的工作副本 |
| `design_check` | `{id, frames?, screenshot?}` | orbd `design.check` |
| `design_directions` | `{id, surface:"landing"\|"app", industry?, tone?, proposals?:[{name, palette, type, layout}]×3}` | `artifact.call`；landing 由库发 3 张，app 校验 agent 自拟的 3 张 |
| `design_direction` | `{id, pick}` | `artifact.call`，记录选定方向 |
| `design_notes` | `{id}` | `artifact.call`，列待处理便签（评论串 + 用户 sticky）+ 逐帧带编号截图 |
| `design_resolve_note` | `{id, ref, reply}` | `artifact.call`；`ref` 是 `act_…` 或 sticky id |

- 助手与 Runner 只得到 `design_directions`、`design_direction`、`design_notes`、`design_resolve_note`，外加 `check_artifact {id, path?}`（控制面探针）。它们没有工作副本。

### C6 迁移 `0059_artifact_design.sql`

```sql
ALTER TABLE artifact_versions ADD COLUMN turn_id TEXT;
ALTER TABLE artifact_versions ADD COLUMN title TEXT;
ALTER TABLE artifact_versions ADD COLUMN check_json TEXT;   -- {status, issues, fingerprint}
CREATE INDEX artifact_versions_turn ON artifact_versions (artifact_id, turn_id);
CREATE TABLE artifact_designs (
  artifact_id TEXT PRIMARY KEY REFERENCES artifacts (id) ON DELETE CASCADE,
  surface TEXT, direction TEXT, dealt TEXT NOT NULL DEFAULT '[]',
  auto_notes INTEGER NOT NULL DEFAULT 1, delivered TEXT NOT NULL DEFAULT '[]', dirty_at INTEGER
);
```

OpenAPI：`ArtifactVersion` 增加可选 `turnId`、`title`、`check{status, issues}`。新增 `GET /api/v1/artifacts/{id}/design`（`getArtifactDesign` → `{design:boolean, live:{threadId, port}|null, direction, autoNotes, check}`）与 `PATCH`（改 `autoNotes`）。`exportArtifact` 的 `format` 增加 `project`、`mockup-png`、`mockup-pdf`。

### C7 父子 postMessage（`web/sandbox/runtime/messages.ts`，`PROTOCOL_VERSION` 仍为 1，只加不改）

- `PageSelection` 增加可选 `loc: string|null`，格式 `src/frames/x.tsx:42:5`，取自 `data-oryn-loc`。
- 父 → 页：`select-loc {loc}`、`design-reload {}`、`navigate {frame}`。
- 页 → 父：`navigated {frame}`、`ready` 增加可选 `design: {frame, links:[id]}`。
- 评论锚点 `Anchor` 增加 `Loc *string`（`comments.go:41`，锚点存 JSON，不用迁移）。
- `data-oryn-loc` 在 dev 与静态构建里都打（见裁决 3）。
- dev 帧里的运行时由引擎入口加载同一份 `sandbox/runtime.js`，配置 `mark:""`、`runtime:true`。

### C8 探针（`web/sandbox/probe/main.ts` → `sandbox/probe.js`）

- 触发：`?__oryn_probe=1`。
- 输出：`<script id="oryn-probe" type="application/json">{v:1, frame, height, issues:[{rule, loc, text, detail}]}</script>`。
- 每条规则最多报 3 处。`status ∈ clean | issues | stale | unverified`；`stale` = `check_json.fingerprint` ≠ 当前 `sealedHash`。

| 规则 | 判据 / 阈值 | 来源 |
|---|---|---|
| `text-clipped` | `overflow` 非 visible 且 `scrollWidth>clientWidth+1` 或 `scrollHeight>clientHeight+1`，且有可见文本 | Vetta layout-probe |
| `unintended-wrap` | 按钮、标签、导航项的文本行数 > 1（`Range.getClientRects`） | Vetta |
| `icon-missing` | `[class*="icon-["]` 或 svg 盒为 0，或 mask-image 为 none | Vetta |
| `edge-misaligned` | 同一父容器内兄弟块左缘差 1–3 px | Vetta |
| `token-undefined` | 计算样式里有未解析的 `var(--…)` | Vetta bg-token-undefined |
| `contrast-low` | 文本对比 < 4.5（≥24 px 或粗体 ≥18.66 px 用 3） | frontend-design（WCAG 1.4.3） |
| `target-small` | 可点元素 < 24×24 CSS px | frontend-design（SC 2.5.8） |
| `dash-in-copy` | 可见文本含 U+2014 或 U+2013 | frontend-design |
| `eyebrow-excess` | 大写 + 字距 > 0.05em + 字号 ≤ 13 px 的标签数 > `ceil(sections/3)` | frontend-design |
| `accent-multiple` | 饱和度 > 0.5 的非中性色相簇（30° 一簇）> 1 | frontend-design |
| `radius-single` | 元素 ≥ 12 个且全部非 0 圆角取同一值 | frontend-design |
| `pure-black-white` | 面积 > 5% 的背景或文本为 `#000` / `#fff` | ai-tells |
| `split-repeat` | 连续 3 个 section 同为左右图文分栏 | frontend-design |
| `raw-color` | 元素内联 style 含颜色字面量 | frontend-design（语义 token） |

后 8 条属审美类，只在 `artifact_designs.direction` 非空时报告；阈值按 `surface` 取（landing 用上表，app 关闭 `eyebrow-excess` 与 `split-repeat`）。

### C9 内置技能 `oryn-design`（`internal/orbdrt/assets/oryn-design/SKILL.md` + `NOTICE.md`）

- frontmatter description：「在 Oryn 画布上做设计稿、原型、落地页或 App 界面稿（artifact），或处理画布上的便签时使用。在项目仓库里写前端代码不属于这里，那是 `oil-ui` / `frontend-design`。」
- 章节：
  1. 何时用 / 不用。触发边界：产物是 artifact 画框 → 本技能；产物是仓库里的文件 → 另两者。
  2. 流程：产品事实 → 每帧一个主角 / 主动作 → 灰度结构 → 一个推荐方向，用户明说才三方向并排。改写自 oil-ui，删去版本检查、截图脚本、`.oil-ui` 目录、生图积分。
  3. 画布契约：`design_open`、工作副本布局、`frames/<id>.tsx`、`export const frame`、`<Link to>`、`theme.css` token、`DESIGN.md`。
  4. 发牌：`design_directions` → `design_direction`，只能在发出的牌里挑。
  5. 检查：每次改完跑 `design_check`，规则表与修法；`issues` 不清零不结束回合。
  6. 便签：`design_notes` → 改 → `design_resolve_note`。
  7. 交付：导出工程、部署 box 转 `oryn-boxes`。
  8. 自动发生的事：回合封存、标题、依赖安装，不要手动调 `update_artifact` 写 `src/`。
- NOTICE：oil-ui（MIT，流程改写）、taste-skill（MIT，Leonxlnx，方向库数据）、HyperSkills frontend-design（MIT，规则阈值）、Vetta layout-probe（许可未验证，照搬前由主人确认；否则只取规则思路重写）。
- 下发：orb 写到 `$HOME/.agents/skills/oryn-design/`（`writeAgentAssets`，`selfsetup.go:288`）；Runner 写到 `$HOME/.oryn/omp/agent/skills/oryn-design/`；助手放 `internal/coord/skilldocs/design.md` 并改 `buildSkillFiles`。Runner 与助手版删去 3、8 两节，换成「用 HTML 画框」。

## 步骤

Accept 统一口径：除特别写明外，`go build ./...` 无输出，`go test <包>` 打印 `ok`，`cd web && bun run check` 退出码 0，`bun run test` 全绿。

### 主干 T

**T1 画布文档 format 2**
- Depends on：Step 0。
- Files：`internal/artifacts/canvas/{doc,ops}.go`、`doc_test.go`、`web/src/lib/artifacts/canvas-doc.ts`、`schema/testdata/canvas-ops.json`、`internal/artifacts/guide.go`（canvas 主题加「设计画布」一节）。
- Do：按 C1 实现。`docKeys` 加 `design`，`pageKeys` 加 `source`、`device`，`pageSettable` 加 `device`。`Doc.JSON()` 按是否含新字段决定 `format`。`links` 校验 `source` ↔ `file`、`start` 指向存在的 page。
- Accept：`go test ./internal/artifacts/canvas/` → `ok`；`cd web && bun run test -- canvas-doc` 全绿；向量文件里新增至少 6 例（含 `source` 无 `design` 报错、set `source` 被拒）。

**T2 迁移、版本列、Caller.Turn**
- Depends on：无；与 T1 并行。
- Files：`internal/store/migrations/0059_artifact_design.sql`、`internal/store/queries/artifacts.sql`、`internal/artifacts/{artifacts,write,agent}.go`、`schema/openapi.yaml`、`internal/api/artifacts.go`。
- Do：按 C6。`WriteParams`、`CreateParams` 加 `Turn struct{ID, Title string}`。`insertVersion` 写三列。`Title` 取首行，截到 `MaxNoteRunes`；以 `[Oryn ` 开头的自动消息不取（口径同 `transcript.IsAutomatedUserText`）。`agentVersions` 输出 `title`、`turnId`。
- Accept：`make gen && go build ./... && go test ./internal/store/... ./internal/artifacts/` → `ok`；`cd web && bun run check` 通过。

**T3 引擎模板与镜像预装**
- Depends on：P1、P2。
- Files：新包 `internal/designengine/`（`embed` 引擎源：`package.json`、`package-lock.json`、`vite.config.mjs`、`plugin-frames.mjs`、`babel-loc.mjs`、`entry.tsx`、`link.tsx`、`styles.css`，以及 `Version` 常量）、`images/orb/Dockerfile`（`npm ci` 到 `/usr/local/share/oryn/design-engine/<v>/`）。
- Do：
  - 引擎插件：dev 中间件为 `/frames/<id>` 生成入口；build 生成 `.oryn-build/<id>.html` 与 `dist/frames.json`。
  - `resolve.alias` 把 react / react-dom 钉到引擎副本。
  - 配置从引擎根加载（`--config <engine>/vite.config.mjs`，设计目录经 `ORYN_DESIGN_DIR`），插件按引擎 `node_modules` 解析（P2 实测：配置放设计目录会解析失败）。
  - Tailwind：引擎 `styles.css` 写 `@import "tailwindcss";` 与占位注释；引擎插件 `oryn-design-css`（`enforce:"pre"`，在 `plugins` 数组里排在 `@tailwindcss/vite` 之前）把占位替换为 `@import "<work>/src/theme.css"; @source "<work>/src";`。设计侧 `src/theme.css` 只写 `@theme {…}`，不写 `@import "tailwindcss"`。
  - `Link` 在 dev 输出 `/frames/<id>`，在 build 输出 `./<id>.html`。
  - babel 插件给 JSX 起始标签加 `data-oryn-loc`。
  - `entry.tsx` 在 `?__oryn_probe=1` 时加载 probe；dev 时加载运行时并回 `ready.design`。
- Accept：`go test ./internal/designengine/` → `ok`（校验嵌入文件齐全、lock 与 package 一致）；临时目录里 `npm ci && vite build` 两帧样例，`ls dist` 恰为 `a.html b.html assets frames.json`。

**T4 orbd 设计运行时**
- Depends on：T2、T3。
- Files：新 `internal/orbdrt/design.go`、`design_test.go`、`internal/orbsock/orbsock.go`、`internal/orbdrt/selfsetup.go`（`handleSock` 分支）、`internal/artifacts/agent.go`（`seal_design`）、`internal/artifacts/design.go`（对齐 pages）、`schema`（link 的 artifact.call 外壳加 `turn`）。
- Do：按 C3、C4、C5。
  - 物化：经 ctl `artifact.call read_artifact` 逐文件取 `src/**`（文本），二进制资产走新的内部操作 `fetch_design_files`，返回带 base64 的清单；单次不超过 `maxRequestBytes` 4 MiB，超过则分页。
  - 起服务：直接调 `r.svc.Start(ServiceStartParams{Name, Command, Cwd, Portal:true, ThreadID})`。
  - 封存：算 hash，未变则 skip；`src/package.json` 变了先在 `src/` 下 `npm install`（D3，不加 `--ignore-scripts`）。
  - 对账：`.oryn-design.json` 的 `baseVersion` 落后于 head 时，副本干净就重新物化，脏则保留；封存报 `conflict` 时原文回给 agent。
- Accept：`go test ./internal/orbdrt/ -run Design` → `ok`（用假 vite 脚本，覆盖 open / seal / skip / build_failed / conflict）；`go test ./internal/artifacts/ -run Seal` → `ok`（多帧、增删帧后 pages 对齐，`canvas.json` 不冲突）。

**T5 扩展：工具、闸门、钩子、技能下发**
- Depends on：T4、P4、P6。
- Files：`internal/orbdrt/assets/oryn-orb.ts`、`assets/oryn-design/{SKILL.md,NOTICE.md}`、`assets.go`、`selfsetup.go`、`runtime.go:446`（Runner 版）、`internal/coord/{skilldocs/design.md,skill.go,profile.go}`。
- Do：
  - 注册 C5 的工具。
  - `input`：`source==="rpc"` 且不以 `[Oryn ` 开头时记 `pendingTitle`（`prompt` 与 `follow_up` 都会触发）。
  - 设计回合以 **run** 为单位，跨 `willContinue` 保持：扩展维护 `turn: {id, title} | null`。`agent_start` 时若 `turn` 为空，开新回合（`turnId=crypto.randomUUID()`、`title=pendingTitle`、清空 `pendingTitle`、发 `design.turn`）；`agent_end{willContinue:true}` 不清 `turn`，后续的 `agent_start` 沿用；终态 `agent_end`（无 `willContinue`）封存后把 `turn` 置空。`before_agent_start` 只用来追加一行设计现状，不开回合（P4：它先于首个 `agent_start`，且续跑与 run 内吸收的 follow_up 也会触发它、`prompt` 是 `additionalContext` 或排队原文）。
  - 不调用 `setActiveTools`（P6 补测：回合开始时切换同样 400）。
  - `session_stop`：有脏副本且 `check.status` 为 `issues` 或 `unverified`、且 `!stop_hook_active` 时，返回 `{continue:true, additionalContext:"设计检查未通过：…"}`，每回合至多一次（P4 实测：续跑结束时 `stop_hook_active:true`）。中止的回合不发 `session_stop`，不强制续跑。
  - `agent_end` 且 `!willContinue`：`void` 发 `design.seal`，不 await。中止的回合同样有终态 `agent_end`，照常封存。
  - `ARTIFACT_GUIDE` 加一行，指向 `oryn-design`。
- Accept：`go test ./internal/orbdrt/` → `ok`（资产写出路径断言）；`cd /root/Projects/Ling/oryn-core && bunx tsc --noEmit -p <含该扩展的检查配置>` 无错。若该仓库没有现成配置，沿用 `orbdrt_test.go` 里对扩展的既有语法检查方式。

**T6 Web：设计画布与 live 帧**
- Depends on：T1、T2、T4，P3。
- Files：`web/src/components/artifacts/canvas/{CanvasEditor,frames,FocusedPage}.tsx`、新 `canvas/LiveFrame.tsx`、`frame-link.ts`、`web/sandbox/runtime/{messages,select,start}.ts`、`web/src/api/artifact-queries.ts`、`internal/api/artifacts.go`（`getArtifactDesign`）、`internal/artifacts/design.go`（由 `orb_services` 查 `dsn-…` 服务）、两份语言文件。
- Do：
  - `design.live` 非空、且查看的是最新版本时，离视口最近的至多 6 帧用 portal iframe：`sandbox=PORTAL_SANDBOX`，`src` 经 `portalFrameSrc(template, port, "/frames/<id>")`（`portal-url.ts:39`）。其余帧与旧版本用静态帧。
  - 顶栏显示「实时 / 静态」徽标与手动切换。
  - `FrameLink` 对 portal 帧 `postMessage` 时把 targetOrigin 写成 portal 源。
  - 选中结果带 `loc`，进入 `SelectionChip`、`anchorOf`、`handoffPrompt`。
  - 带 `source` 的帧禁用 EditPanel 直接编辑，提示「在对话里让 Agent 改」。
  - 收到新版本后，对 `hmr` 不可用的帧发 `design-reload`。
  - 带 `source` 的画框删除后撤销不了（重建会带 `source`，ops 拒绝；T1 实测），编辑器对这类画框禁用删除或明示不可撤销。
  - WebKit 授权兜底（P3 实测）：portal 握手在 WebKit iframe 里会被带到主站 `/login`（主站会话 Cookie 是 `SameSite=Lax`，生产环境主站页还带 `frame-ancestors 'none'`，帧直接空白）。父页读不到跨源帧的 URL，所以判据是「live 帧 5 s 内没有收到 dev 运行时的 `ready`」：该帧退回静态帧，顶栏出现「在新窗口授权实时预览」；点击 `window.open(portalFrameSrc(template, port, "/"))`，窗口回到焦点后重建 live 帧。Chromium 系实测不触发。
- Accept：`cd web && bun run check && bun run test -- canvas frames frame-link messages` 全绿；`bun run accept:m29 -- --only K1,K2,K3`（K1 设计画布静态帧可见；K2 live 徽标出现且改源码后帧内文本变化；K3 点元素后 chip 显示 `src/frames/…:行`）。

### 品质 Q（依赖 T4）

**Q1 探针与两处启动器**
- Depends on：T3、P5。
- Files：`web/sandbox/probe/{main,rules}.ts` 及测试、`web/vite.sandbox.config.ts`（mode `probe`）、`web/package.json`（build 链追加）、`internal/artifacts/render/{chromium,render}.go`（`Job.DumpDOM`，stdout 上限 4 MiB）、`internal/artifacts/site.go`（`ServeRender` 在 query 含 `__oryn_probe=1` 时注入）、`internal/artifacts/check.go`（`check_artifact`、解析、写 `check_json`）、`internal/orbdrt/design.go`（orb 启动器 + `--screenshot`）。
- Accept：`cd web && bun run test -- probe` 全绿（每条规则一正一反的 jsdom 例）；`go test ./internal/artifacts/render/ ./internal/artifacts/ -run 'Probe|Check'` → `ok`；`bun run accept:m29 -- --only K4`（故意做一个截断按钮，`design_check` 返回 `text-clipped` 和它的 `loc`）。

**Q2 方向发牌与设计系统**
- Depends on：T2。
- Files：`internal/artifacts/design/{deal.go,library.json,deal_test.go}`、`internal/artifacts/agent.go`、`internal/coord/profile.go`（助手工具定义）。
- Do：
  - `Deal(lib, surface, industry, tone, seed, avoid) [3]Direction` 是纯函数，`avoid` = `dealt` 的最近 9 项。
  - `library.json` 取自 taste-skill 的 `palettes.json`、`type.json`，裁到约 40 色板、20 字体对，写进 NOTICE。
  - app 面校验 `proposals`：3 个，色相两两相差 ≥ 30° 或字体族不同，否则 `invalid`。
  - `design_direction` 把选定方向写进 `artifact_designs.direction`，并回一段可粘进 `theme.css` 的 `@theme` 文本。
  - token 名（Q2 与 T3 共用，冻结）：`--color-{bg,surface,text,muted,accent,on-accent}`、`--font-display`，正文覆盖 `--font-sans`，等宽覆盖 `--font-mono`；`bg`、`text`、`accent` 必有。
- Accept：`go test ./internal/artifacts/design/` → `ok`（同种子同结果；连发三次不重复）。

### 迭代 I

**I1 版本历史按回合呈现**
- Depends on：T2。
- Files：`web/src/components/artifacts/VersionHistory.tsx` 及测试、语言文件。
- Do：同一 `turnId` 的连续版本折叠成一行（展开可见各版本）；主标题取 `title || note || author`；带 `check.status` 徽标。
- Accept：`bun run test -- VersionHistory` 全绿，含折叠用例。

**I2 旧版查看与恢复对账**
- Depends on：T4、T6。
- Files：`ArtifactViewer.tsx`（设计画布钉旧版时强制静态帧，横幅文案）、`internal/orbdrt/design.go`（对账已在 T4 实现，这里补测试）。
- Accept：`bun run accept:m29 -- --only K5`（钉 v1 → 静态帧显示旧内容 → 恢复 → 下一回合 agent 的工作副本是恢复后的源码）。

**I3 便签自动投递**
- Depends on：T2。
- Files：新 `internal/artifacts/notes.go` 及测试、`activity.go`（`turnObserver.StateChanged` 转 idle 时触发）、`comments.go`、`live.go`（`flushLive` 后标脏）、`internal/server/server.go`（注入 `threads.Deliver`）、`CommentsPanel.tsx`（自动投递开关 + 「已排队」状态）、`agent.go`（`design_notes`、`design_resolve_note`）。
- Do：
  - 待处理项 = 未解决且 `SentAt` 为零的评论串 + `by==nil && !done` 且 id 不在 `delivered` 里的 sticky。
  - 标脏后去抖 30 s；目标 = artifact 的创建线程（未归档且有 orb）；线程 idle 时调 `Deliver(AutoMessage{Text:"[Oryn 设计便签] …调用 design_notes…"})`。ack 成功后 `MarkSent` 并追加 `delivered`。
  - 忙时等 `StateChanged`。
  - `auto_notes=0` 或无目标线程时不投，保留手动按钮。
- Accept：`go test ./internal/artifacts/ -run Notes` → `ok`（假时钟：去抖、忙时延后、ack 失败不标记、重复不投）。

### 原型 R（依赖 T6）

**R1 预览模式与设备外壳**
- Files：新 `canvas/Prototype.tsx`、`canvas/DeviceShell.tsx`、`canvas.css`、`Toolbar.tsx`、`PropsPanel.tsx`（device 选择）、`Presenter.tsx`（不改，复用 `fitScale`）。
- Do：
  - 从 `design.start`（缺省第一帧）开单帧窗口。
  - 帧内同源跳转后，由运行时的 `navigated` 同步当前帧，并更新外壳与尺寸。
  - 画布上按 `dist/frames.json.links` 画帧间连线，只读，不落 canvas.json。
  - 外壳用令牌色与令牌圆角，390 宽可用。
- Accept：`bun run test -- Prototype DeviceShell` 全绿；`accept:m29 -- --only K6`（点帧内 Link，标题变成目标帧名）。

### 交付 D

**D1 样机导出**
- Depends on：Q1。
- Files：`web/sandbox/overview/main.ts`（`view=mockup`）、`internal/artifacts/{overview,export}.go`、`ArtifactMenu.tsx` + 新 `MockupDialog.tsx`。
- Do：服务端生成样机页，query 为 `frames,scale,radius,border,bg,shadow,shell,long`。`mockup-png` 经 `render.Job`；长图先用探针量 `height`，上限 8000。`mockup-pdf` 用 `PDF:true`。
- Accept：`go test ./internal/artifacts/ -run Mockup` → `ok`；`accept:m29 -- --only K7`（下载的 PNG 尺寸与选项相符）。

**D2 导出为工程**
- Depends on：T3。
- Files：`internal/artifacts/export.go`、`internal/designengine/eject/`（`index.html`、`main.tsx`、`vite.config.ts`、`server.mjs`、`.oryn/box.yaml`、`README.md`）。
- Do：`format=project` 的 zip = `src/**` + eject 文件 + 合并后的 `package.json`（引擎依赖 ∪ 设计依赖）。eject 版 `main.tsx` 用 hash 路由列出全部帧；`box.yaml` 为 `build: npm ci && npm run build`、`source:false`、`artifacts:[dist, server.mjs]`、`services.web.command: node server.mjs`。
- Accept：`go test ./internal/artifacts/ -run ExportProject` → `ok`（zip 清单断言）；手工解压后 `npm ci && npm run build` 退出码 0。

**D3 部署到 box**
- Depends on：D2。
- Files：`cmd/oryn/`（新子命令 `oryn design eject <id> --to <dir>`，经 orbd.sock 取 `format=project`）、`oryn-design/SKILL.md` 第 7 节、`ArtifactMenu.tsx`（「部署到 box」= `sendGeneratedPrompt` 给作者线程）。
- Accept：`go build ./cmd/oryn && go test ./cmd/oryn/ -run DesignEject` → `ok`；真机走查一次 `oryn design eject … && git init && git commit && oryn boxes deploy --create`，打开 app 地址看到起始帧（记入计划，不进 CI）。

**V 验收脚本与文档**
- Files：`web/scripts/m29-design-accept.ts`（K1–K7，写法照 `m14-artifacts-accept.ts`）、`web/package.json`（`accept:m29`）、`docs/features.md`、`docs/protocol.md`。

### 并行切片（至多 5 个子 agent）

- 波次 1：T1 ‖ T2 ‖ T3 ‖ Q2 的 `deal.go`。四片文件不相交，共享契约只有 C1 与 C6。
- 波次 2：T4 独占 `orbdrt/design.go`、`agent.go`、`orbsock.go`。并行 Q1 的 `web/sandbox/probe` 与 `render/`，以及 I1。
- 波次 3：T5（`oryn-orb.ts`、技能）‖ T6（web 画布）‖ I3（`notes.go`；它对 `agent.go` 的两个工具分支由 T5 的 agent 合入，`agent.go` 只有一个属主）‖ D2 ‖ Q1 的 orb 启动器（在 T4 的 `design.go` 上追加，排在 T4 合入之后）。
- 波次 4：R1 ‖ D1 ‖ D3 ‖ I2 ‖ V。
- 共享文件属主：`schema/openapi.yaml` 归 T2，其余切片把 schema 片段交给它；语言文件各片只追加自己的键，由 V 做最后合并检查。

## 关键文件

- `internal/artifacts/live.go` 的 `writeCanvas`、`agentCanvas`：封存必须走它，才能保持「canvas.json 由服务端合并」。
- `internal/orbdrt/services.go` 的 `Start`：orbd 内部直接起动态服务；控制面无法新建动态服务（`serviceapi.go:180` 不带 Command）。
- `oryn-core/.../session/agent-session.ts:6411-6492`：`agent_end` 的 per-loop 语义与 `willContinue`。
- `internal/artifacts/render/chromium.go` 的 `chromiumArgs`：`--dump-dom` 分支，参数逐项固定。
- `internal/portal/proxy.go` 的 `modifyResponse`：已把 `frame-ancestors` 改写为 SPA 源，live 帧可被嵌入。

## 验证（端到端）

前置：`make web && make build`；配置了 `portal_domain` 的本机 orynd；orb 镜像已含引擎。工作目录 `web/`。

1. `bun run accept:m29`，期望末行 `K1–K7 passed`。
2. 手工：新线程发「做一个登录页和注册页，能互相跳转」。期望：
   - 画布出现两帧，带「实时」徽标；
   - 回合结束后版本历史只多一行，标题就是这句话，徽标 `clean`；
   - 停掉 orb 后刷新，两帧仍显示，点「注册」能跳转；
   - 缩略图非空白。
3. `sqlite3 <data>/oryn.db "select n,turn_id,title from artifact_versions where artifact_id='<id>'"`：该回合恰一行，`turn_id` 非空。

## Rollback point

- 波次 1 之后可停：只多了解析能力、三列、嵌入包，行为不变。
- T4–T6 合入后回滚：`goose down` 到 0058（丢 `artifact_designs` 与三列）；已存在的 format 2 画布在旧版 orynd 上解析失败（`format 必须是 1`），需先导出。HTML 画布保持 format 1，不受影响。
- orb 镜像与 orbd 必须同版发布：新 orbd 遇旧镜像会走 `npm ci` 回退路径，可用但首次慢；旧 orbd 不认 `design.*`，扩展的 `design_open` 回 `method_not_found`，文本提示升级。

## Non-goals

- 组件面板、风格画廊界面。
- TSX 帧的元素直接编辑与调节参数（`data-oryn-params`）。
- 没有 orb 的助手、Runner 用 TSX。
- 查看器唤醒已停的 orb 来开 live。
- 发布页剥离 `data-oryn-loc`。
- Vetta 式「旧版写进工作区」的 peek。
- 多线程同时编辑同一设计的合并。
- 设计级依赖的缓存或镜像加速。

## Assumptions

- A1 被中止的回合仍会发终态 `agent_end`。P4 已实测成立（中止回合不发 `session_stop`）。
- A2 `canvas.Parse` 接受 `pages: []`，且 `validate` 允许只有 `canvas.json` 的版本（读到的代码路径上无最小画框数检查，未实测）。影响 T4 的 `design.open`；失效则建画布时带一帧占位 `dist/index.html`。
- A3 box 上有 `node`（未验证）。影响 D2、D3；失效则 `server.mjs` 换成 `python3 -m http.server "$PORT" -d dist`。
- A4 orb 内的 Chromium 可无头运行（桌面用的同一二进制，`Dockerfile:90`）。影响 Q1；失效走 P5 分支。
- A5 `portal_domain` 已配置；否则没有 live，功能退化为「回合结束才看到」，其余不变。影响 T6。
- A6 全局助手的 `Caller.Turn` 暂为空：`coord` 是否有可用的回合标识未读。影响 I1；助手产生的版本不折叠、标题回落到 `note`。
- A7 Vetta 探针规则的许可允许借鉴（未验证）。影响 Q1；失效则只按上表判据独立重写，不看其源码。

## 对 11 条疑点的裁决

1. **数据模型**：采纳 `page.file = dist/<id>.html`，另加 `source` 字段。否决「`route` 与 `file` 二选一」，那会让帧、渲染、发布、缩略图全部分叉。格式取「按内容写 1 或 2」；否决「无条件升 2」（HTML 画布不必失去可回滚性）和「format 1 加可选字段」（旧解析器会报成未知字段，而不是格式不符）。
2. **构建与同步**：引擎源由 orbd 嵌入，`node_modules` 镜像预装，版本不符时 `npm ci` 兜底。工作副本放 `~/designs/<id>/`：`~/.oryn/**` 被 fs API 判为只读（`localapi.go:248`），用户在文件面板看不了也改不了。触发点是 `agent_end`，执行者是 orbd；否决在扩展里 `vite build`（30 s 处理器预算）。`canvas.json` 不进工作副本，采纳。
3. **实时预览**：采纳。服务由 orbd 在 `design.open` 时自起。dev 帧复用同一份运行时，不另写。静态构建也打 `data-oryn-loc`，这样没有 orb 时评论与「交给 Agent」仍能精确定位；否决「仅 dev 打标」。
4. **渲染检查**：否决 a（playwright：新依赖、体积）和 b（手写 CDP：数百行协议代码加管道管理）。采纳 `--dump-dom` + 页内探针，orb 与控制面两个启动器共用一份规则。HTML 画框经 `check_artifact` 同样有检查。
5. **回合 = 版本**：设计画布真的延迟落版本（orb 侧封存）。HTML 画框不延迟，只用 `turn_id` 分组展示；否决对 HTML 也延迟（助手没有工作副本，延迟意味着在控制面再造一套暂存）。`turn_id` 由扩展生成；否决控制面推断（`threads` 没有回合标识，`TurnEvent` 只对带 Tag 的投递触发，`turns.go:27`）。
6. **标题**：取 orb 侧 `input` 事件的原文，随 `turn` 外壳上送。否决控制面查 entries：没有只取用户消息的查询，且排队合并后对不上回合。
7. **旧版查看**：采纳「画布级只读旧版 + 恢复」，现有 `pinnedVersion` 已具备；只补恢复后的工作副本对账。否决 Vetta 式 peek。
8. **便签**：不统一数据模型（sticky 在 canvas.json，评论在表里，锚点语义不同），只统一出口：一个待处理视图、一条自动消息、一对 agent 工具。否决合表迁移。
9. **原型**：采纳相对链接，由引擎 `<Link>` 生成；否决 react-router 和 `#/frame/<id>`。连线从 `frames.json.links` 推导，不进 canvas.json。
10. **交付**：样机走服务端样机页，一套实现出 PNG、长图、PDF；否决客户端 `compose-image` 扩展（出不了 PDF，长图受浏览器 canvas 上限限制）。box 部署走「eject → orb 内提交 → `oryn boxes deploy`」；否决控制面直推 dist，因为 box 管线以 git 提交和 orb 构建为前提（`boxes_deploy.go:176`），另开一条路相当于第二套部署。
11. **发牌**：做成工具（Go 纯函数 + 数据），技能只讲何时调；否决纯技能下发，那不可强制、不可测。taste 的一致性检查并入探针规则。

## 原「待主人决定」的裁决

见文首 D7–D10：便签默认开（a）、检查不过强制续一轮（a）、封存自动安装保持、探针规则只取判据独立实现。A7 随 D10 消解。

## 范围外

- `docs/plan-m28.md` 与简报都写「迁移取 0058」，但 0058 已被占用。本计划用 0059；若 M28 另有未落地的迁移，需要与其对齐编号。
- `thumbs.go` 对画布渲染的是概览页。设计画布帧多时缩略图会很小，可后续改成渲染 `design.start` 帧。