# M33 — 视图（原生生成式 UI）：实施计划

> 状态：调研与四轮决策确认已完成，主人已确认共识。当前处于计划模式，仓库只读；开工的第一步（S0）把本文件原样落到 `docs/plan-m33.md`。
> 调研底稿：`agent://ScoutAgentTools`、`agent://ScoutTranscriptUI`、`agent://ScoutSandboxRender`、`agent://ScoutSettings`、`agent://ScoutAnchorsAgent`、`agent://ScoutAnchorsWeb`（均带 `path:line`）。

## Context

主人要求：参考 Vetta 的生成式 UI 插件，给 Oryn 做原生的生成式 UI——agent 在对话里直接给出可交互的界面（表单、选项、图表、仪表盘、小工具），而不是只有文字。
终态：agent 调用 `render_ui`（声明式组件）或 `render_html`（沙箱 HTML），界面直接出现在转录里；主人在上面的操作回到 agent，可以是下一条消息，也可以是这次调用的结果。产品里这个东西叫**视图**。

参考实现：`openvetta/vetta-official-marketplace` 的 `abilities/plugins/vetta-generative-ui`（v0.1.1，不在 `open-vetta` 主仓）。组件词表、提示词与沙箱策略以它为蓝本；宿主机制（工具注册、回传、持久化、帧）全部换成 Oryn 自己的。

现状为什么不够（均已核对，行号为写作时）：

- 转录里的工具卡只有「头部 + 可展开体」（`web/src/tool-render/ToolView.tsx`），没有内联提交的先例；`ask` 的表单停靠在输入坞，答完即消失，不留在对话里。
- Web 的 Markdown 只渲染 mermaid 与 KaTeX；没有图表库（`web/package.json` 无 `chart.js`）。
- 主站 CSP 是 `script-src 'self' 'wasm-unsafe-eval'`（`internal/webui/webui.go`），`srcdoc` 子文档继承它，**参考实现的 `srcdoc` 沙箱在 Oryn 跑不了内联脚本**；可运行脚本的不透明源帧只有 artifact 帧（`internal/artifacts/frames.go`、`policy.go`、`host.go`），且硬绑定 artifact。
- 客户端向线程送消息只有 WS `cmd` 的 `prompt / steer / follow_up`；回合内等待用户只有 `extension_ui_request` 的预定义方法（`internal/threads/live.go:20`）。

## 已定决策（主人逐项确认）

- D1 定位：三者并存。`ask` = 回合内必须马上拿到的简单答案；**视图** = 转录里的一次性可交互回答，随对话走，不进 Artifacts 列表；Artifacts = 要留存、迭代、发布的成品。
- D2 两种形态都做：声明式 `render_ui` 为主，沙箱 `render_html` 兜底。
- D3 回传两条通道都支持，由模型每次调用选：非阻塞 = 操作作为主人的下一条消息；阻塞 = 调用挂起，操作作为该调用的工具结果。
- D4 范围：工作线程主 agent（orb 与本机 Runner）+ 全局助手。子 agent、Space 协调者、研究员、受限主线不提供。
- D5 开关：默认开启；设置里一个用户级总开关，关闭后不注册工具、不注入指引。已有的视图照常显示。
- D6 静态可视化：`render_ui` 加 `svg` 块覆盖自由绘图；Core 的 Visuals 指引不动，不在本期。
- D7 组件表：Vetta 的 17 种 + `svg`，共 18 种；`text` 块改为 Markdown。
- D8 图表：引入 Chart.js（懒加载），`chart` 块收标准 Chart.js `data` / `options`，8 种图型。
- D9 HTML 网络策略照 Vetta：任意 `https:` 的脚本 / 样式 / 字体 / 图片 / 媒体可加载；`connect-src 'none'`、`form-action 'none'`。主人已知的暴露见「风险」。
- D10 呈现：直出、常驻可见，不套工具卡外壳、不随批次折叠。流式期间声明式视图逐块出现，控件在参数通过校验、调用开始执行之前禁用；HTML 视图先显示骨架，调用开始执行后挂帧（`await` 的调用在等待作答期间帧已经可用）。
- D11 阻塞选择：两个工具各有可选参数 `await`，默认 `false`。
- D12 命名：界面与文档叫「视图」（View），设置项叫「生成式 UI」；工具名 `render_ui` / `render_html`（Core 与 Oryn 均无同名工具）。
- D13 阻塞时限：等 10 分钟；到时调用照常返回并转为非阻塞，视图仍可操作，之后的操作作为消息到达。等待期间线程是 `awaiting_input`（进「等你处理」、发推送、orb 不空闲冻结）。主人改为直接打字时，挂起的调用立刻以「改用文字回复」结束。
- D14 全局助手的阻塞：同样 10 分钟、同样降级；尽力而为——在途调用只在 orynd 内存里，orynd 重启后不保证还在。
- D15 HTML 内置库：`libs` 可选 `chartjs`、`tailwind`，离线内联注入。
- D16 HTML 回传：`window.oryn.sendPrompt(text)` 直接发送，不弹确认条，不要求用户手势。
- D17 状态：用过即锁。表单提交、按钮点击后该块锁定并显示已提交的内容；HTML 视图每次调用只回传一次。状态从转录推导，跨设备一致。
- D18 公开分享页：声明式视图照常渲染（控件禁用）；HTML 视图照常运行，回传无效。
- D19 积极度：积极——内容有结构就倾向用视图（见 C11）。

## 冻结契约

以下字面量在各步骤间共享，实现时原样使用。

### C1 工具

两个工具，工作线程由 orb 扩展注册，全局助手由宿主工具表下发；名字、描述、参数 schema 同一份（C6）。`loadMode: "essential"`，`approval: "read"`。

| 工具 | 参数 |
|---|---|
| `render_ui` | `title?`（≤200）、`description?`（≤600）、`width?: "normal" \| "wide"`、`blocks`（1–40 个块，见 C2）、`await?: boolean`、`endTurn?: boolean` |
| `render_html` | `title?`（≤200）、`html`（1–200 000 字符；完整文档或 body 片段，可带内联 `<style>` / `<script>`）、`libs?: ("tailwind" \| "chartjs")[]`、`height?`（≥120 的最小高度，省略则随内容）、`await?: boolean`、`endTurn?: boolean` |

- 参数校验由 Core 在 `execute` 之前按 JSON Schema 完成（`agent-loop.ts:3259` → `validateToolArguments`）；失败时模型收到 `Validation failed for tool …` 并自行重试，`execute` 不被调用。对象一律写 `additionalProperties: false`：Core 遇到多余的键是删掉而不是判错（`validation.ts:1722-1735`）。
- `render_ui` 不含 `form` / `actions` 时 `await` 被忽略。
- `endTurn`（两个工具都有，默认 `false`）：视图是本回合最后一句。结果 `details` 加 `orynEndTurn: true`，Core 把它当终止型结果，这批工具跑完即结束回合、不再请求模型。真正挂起等作答（`await` 且有交互）时忽略。

### C2 组件词表（`render_ui.blocks`）

`tone` = `neutral | info | success | warning | danger`；未注明的字符串上限 200。

| `type` | 字段 |
|---|---|
| `heading` | `text`，`level?: 1 \| 2 \| 3`（默认 2） |
| `text` | `text`（1–4000，**Markdown**；不加载图片、不渲染内嵌 HTML），`muted?` |
| `badges` | `items[1–20]{label, tone?}` |
| `alert` | `tone?`，`title?`，`text`（1–2000） |
| `divider` | — |
| `stats` | `items[1–8]{label, value: string \| number, delta?(≤40), trend?: up \| down \| flat, hint?(≤120)}` |
| `keyValue` | `items[1–40]{label, value: string \| number}` |
| `table` | `caption?`，`columns[1–12]{key(1–64), label, align?: left \| right \| center}`，`rows[≤200]`：以列 `key` 为键的对象，单元格 `string \| number \| boolean \| null` |
| `list` | `ordered?`，`items[1–50]{title, description?(≤400), meta?(≤80)}` |
| `progress` | `label?`，`value`，`max?`（>0，默认 100），`tone?` |
| `image` | `src`（`https://` 或 `data:image/`），`alt`，`caption?` |
| `chart` | `chartType: bar \| line \| pie \| doughnut \| radar \| polarArea \| scatter \| bubble`，`title?`，`data{labels?, datasets[1–12]{label?, data[], …}}`（标准 Chart.js，其余键透传），`options?`（标准 Chart.js），`height?`（160–560，默认 280） |
| `svg` | `svg`（1–50 000，内联 `<svg>` 标记），`caption?` |
| `form` | `title?`，`description?(≤600)`，`columns?: 1 \| 2 \| 3`，`fields[1–30]`，`submitLabel?(≤40)` |
| `actions` | `buttons[1–6]{label, prompt(1–2000), variant?: primary \| secondary}` |
| `card` | `title?`，`description?(≤600)`，`blocks[1–20]` |
| `columns` | `weights?`：2–4 个 1–6 的数，`columns[2–4]{blocks[1–20]}` |
| `tabs` | `tabs[2–6]{label, blocks[1–20]}` |

- `form.fields[]`：`name`（`^[A-Za-z][\w-]{0,63}$`，表单内唯一）、`label`、`kind`（`text | textarea | number | email | url | date | time | select | multiselect | radio | checkbox | switch | slider`）、`placeholder?`、`description?(≤400)`、`required?`、`defaultValue?: string | number | boolean | string[]`、`span?: "full"`、`options?[1–50]{label, value(1–200)}`（`select / multiselect / radio` 需要）、`min?`、`max?`、`step?(>0)`。表单只校验必填与数值范围，语义由 agent 复核。
- 容器（`card / columns / tabs`）最多嵌套 3 层。schema 用 `$defs` + `$ref` **按层展开、不递归**：`leaf`（15 种非容器块）、`block1 = leaf ∪ 容器(leaf)`、`block2 = leaf ∪ 容器(block1)`、顶层 `block = leaf ∪ 容器(block2)`。联合用 `anyOf`，以 `type` 的 `const` 判别。
- **块路径**（作答与锁定用）：从顶层 `blocks` 起的下标串，用 `.` 连接；进入 `columns` 的第 i 列写 `c<i>`，进入 `tabs` 的第 i 页写 `t<i>`。例：`2`、`0.1`、`3.c0.2`、`1.t1.0`。HTML 视图固定为 `html`。

### C3 工具结果

`content` 是一段英文文本（给模型），`details.view` 给 Web。`details.view` 除下表的字段外一律带 `spec`：`render_ui` 是 `execute` 实际收到的参数（去掉 `await` 与 `endTurn`），`render_html` 是 `{ libs, height }`（不重复存 `html`）。`endTurn` 生效时 `details` 另带 `orynEndTurn: true`（与 `view` 同级）。

为什么要 `spec`：Core 校验时会修复参数（字符串转数字、单值转数组、解开被二次编码的键，`agent-loop.ts:3255-3270`），修复后的值只进 `execute` 与 `tool_execution_start`；转录里的 `toolCall.arguments` 仍是模型的原始输出。回放以 `spec` 为准（C10）。

| 情形 | 文本 | `details.view` |
|---|---|---|
| 无交互块，或 HTML 且非 `await` | `Rendered.` | `{outcome:"rendered"}` |
| 有交互块、非 `await` | `Rendered. The user's input will arrive as their next message; end this turn now.` | `{outcome:"rendered"}` |
| `await`，已作答 | `The user answered in the view:\n<作答正文，见 C4>` | `{outcome:"answered", answer}` |
| `await`，10 分钟无人作答 | `No response within 10 minutes. The view stays usable: a later answer arrives as a user message. Continue without it or end the turn.` | `{outcome:"timeout"}` |
| `await`，主人改为打字 | `The user chose to reply in text instead; their message follows.` | `{outcome:"dismissed"}` |
| `await`，回合被中止 | `Aborted.`（`isError: true`） | `{outcome:"aborted"}` |

Web 的判据：结果 `isError` 且没有 `details.view`（参数没通过校验、会话中断补的合成结果）→ 不显示视图，只留一行安静的提示；其余情形都显示视图。

### C4 作答的线上格式

```ts
type ViewAnswer =
  | { block: string; kind: "form"; values: Record<string, string | number | boolean | string[] | null> }
  | { block: string; kind: "action"; choice: number }   // buttons 的下标
  | { block: "html"; kind: "prompt" };                  // 正文即 sendPrompt 的文本（≤4000）
```

**作答正文**（给模型读，也是转录里显示的内容），只由 Web 生成（`formatViewAnswer`）：

- 表单：首行是表单 `title`（没有则省略），随后每个字段一行 `- <label>: <显示值>`（选项显示其 `label`，多选用 `, ` 连接，布尔 `✓` / `✗`，空值 `—`），空一行，再接一个 ```` ```json ```` 围栏，内容是 `{ <name>: <原始值> }`。
- 按钮：该按钮的 `prompt` 原文。
- HTML：页面传来的文本。

**非阻塞回执**（作为主人的下一条消息发出；首行**不得**以 `[Oryn ` 开头，那是自动消息的判据，见 `web/src/lib/oryn-message.ts:386`）：

```
<oryn-view-answer call="<toolCallId>" block="<块路径>"[ choice="<n>"]>
<作答正文>
</oryn-view-answer>
```

- `choice` 只在按钮作答时出现。
- 解析：`^<oryn-view-answer call="([^"\s<>]{1,256})" block="([0-9a-z.]{1,40})"(?: choice="(\d)")?>\n([\s\S]*)\n<\/oryn-view-answer>\s*$`。表单的 `values` 取正文里最后一个 ```` ```json ```` 围栏。
- 发送：`sendGeneratedPrompt(threadId, text)`（`web/src/state/actions.ts`；agent 忙时 `follow_up`，闲时 `prompt`）。
- `toolCallId` 含 `"`、空白、`<`、`>` 或超过 256 字符时，该视图不可交互。

**阻塞应答**：`extension_ui_response` 的 `value` 是 `JSON.stringify({ v: 1, answer: ViewAnswer, text: <作答正文> })`。扩展与 orynd 只转交：`text` 进工具结果文本，`answer` 进 `details.view.answer`。`value` 不是这种 JSON 时（旧客户端在输入坞里手打的回答）整段当作 `text`，`answer` 省略。

### C5 阻塞通道

- 工作线程：扩展在 `execute` 里调 `ctx.ui.input(title, placeholder, { signal, timeout: 600_000, onTimeout })`（`timeout` 单位毫秒；超时、取消、abort 都 resolve 为 `undefined`，`rpc-mode.ts:1778-1850`）。
  - `placeholder` = `oryn-view:<toolCallId>`——这是标记，Web 按它识别（`method === "input"` 且 `placeholder` 以 `oryn-view:` 开头）。
  - `title` 给人看（收件箱与推送正文取它，`internal/notify/notify.go:563`）：视图的 `title`，没有时按 `ORYN_LANGUAGE` 取「有一个视图等你操作」/ `A view is waiting for your input`。
  - 结果判定：有值 → 已作答；`undefined` 且 `onTimeout` 触发过 → 超时；`undefined` 且 `signal.aborted` → 中止；其余 `undefined` → 主人改为打字。
- 全局助手：宿主工具处理函数调 `threads.RequestUI`（`internal/threads/hostui.go`，现在只收 `confirm` + `cfm_` 前缀；放宽为另收 `input` + `vwa_` 前缀），帧同上（`id = ids.New("vwa")`），然后等 `Coordinator.UIAnswered`、10 分钟定时器或 `ctx` 取消；后两种先 `WithdrawUI`。`HostToolTimeout` 对这两个工具返回 11 分钟。
- Web 侧：带标记的请求不进输入坞，交给转录里对应 `toolCallId` 的视图作答（`answerUi`）。主人在输入框发消息前，先对本线程所有带标记的请求应答 `{cancelled: true}`。

### C6 单一真源与下发

- `schema/view-tools.json`（随 `schema/embed.go` 嵌入）：`{ "guide": string, "tools": [{ "name", "label", "description", "parameters" }, …] }`。工具描述、参数 schema、系统提示词指引都只在这里。
- 全局助手：`internal/coord` 启动时解析一次，`viewToolDefs()` 产出带 `loadMode: "essential"` 的宿主工具定义，`guide` 追加到系统提示词。
- 工作线程：orbd 把同一份字节写到 `$HOME/.oryn/core/view-tools.json`（Runner 写到 Runner 的扩展目录旁）；开关开启时设环境变量 `ORYN_VIEW_TOOLS=<该文件的绝对路径>`。扩展读到这个变量才注册工具，`parameters` 直接用文件里的 JSON Schema（Core 接受纯 JSON Schema 对象，`ai/src/utils/schema/wire.ts:598`）。
- 控制面 → orbd：`LinkCtlAgentStartParams` 加 `viewTools: boolean`（`schema/openapi.yaml`，`language` 之后）。orbd 先 `delete(env, "ORYN_VIEW_TOOLS")` 再按参数写入，写法同 `ORYN_LANGUAGE`（`internal/orbdrt/agent.go:717-724`）。只在工作线程分支设置，Space / 助手的协调宿主不设（`internal/server/e2e_m9_test.go:313` 的环境变量白名单不变）。
- 生效时机：工作线程在下次 agent 启动时生效（运行中的 agent 不因设置变化重启）；助手对话经 `refreshConversations`（`internal/coord/settings.go:296`）在静止后重启。
- 子 agent：Core 会把扩展重新绑定到每个子会话（task 工具、eval 的 `agent()`、`/tan` 克隆），用 `ctx.agent.kind` 区分（`"main"` / `"sub"`；不能看 `depth`，`/tan` 克隆是深度 0 的子 agent，`extensions/types.ts:648-667`）。扩展在 `session_start` / `session_switch` 里，`ctx.agent.kind !== "main"` 时把两个工具从激活列表里去掉（`pi.setActiveTools(pi.getActiveTools().filter(…))`，注册在 `registerRoleTools` 之后）；`execute` 开头再判一次，不是主 agent 直接返回错误。指引只在主 agent 且 `pi.getActiveTools()` 含 `render_ui` 时追加（角色白名单 `ORYN_ROLE_TOOLS` 没列它时同样不追加）。

### C7 设置

- 迁移 `internal/store/migrations/<下一个空闲编号>_generative_ui.sql`（写作时最新是 `0071`）：

```sql
-- +goose Up
-- 生成式 UI 总开关：agent 是否可以用 render_ui / render_html 在对话里给出视图（缺省开启）。
ALTER TABLE owner ADD COLUMN generative_ui INTEGER NOT NULL DEFAULT 1 CHECK (generative_ui IN (0, 1));

-- +goose Down
ALTER TABLE owner DROP COLUMN generative_ui;
```

- 查询 `internal/store/queries/generative_ui.sql`：`GetOwnerGenerativeUI :one`、`SetOwnerGenerativeUI :execrows`（写法同 `artifact_publish.sql:38-42`）。
- API：`GET / PUT /api/v1/settings/generative-ui`，`operationId` 为 `getGenerativeUISettings` / `putGenerativeUISettings`，body `GenerativeUISettings { enabled: boolean }`；处理函数照 `internal/api/artifact_publish.go` 的 `GetArtifactPrefs` / `PutArtifactPrefs`，值变化时调 `Coord.Service.OnGenerativeUIChanged`（= `refreshConversations`）。两个 `operationId` 登记进 `internal/coord/apipolicy.go`（读 / 写）。
- 设置页：「助手与 Agent → 扩展与工具」（`web/src/pages/settings/agent/ExtensionsPage.tsx`）页首加一组，一行 `Toggle`。

### C8 视图帧（HTML 视图的宿主）

复用 artifact 帧的寻址与令牌（`a-<令牌>.<portal_domain>` 或 `<public_url>/_af/<令牌>/`），内容与策略独立。

- `internal/artifacts`：`frame` 加 `view func(context.Context) (ViewDoc, error)` 与 `public bool`；`ViewDoc{HTML string; Libs []string}`。

```go
// OpenViewFrame 申请一个视图帧；load 在每次取帧根时调用。public 的帧计入匿名配额。
func (h *Host) OpenViewFrame(load func(context.Context) (ViewDoc, error), origin string, public bool) (token, url string)
```

- `Host.serve` 在 `h.use(token)` 之后分支：`f.view != nil` → `serveView`。只服务帧根的 `GET` / `HEAD`，其余 404。
- 配额：与 artifact 帧共用 `maxFrames = 256` 与空闲过期；`public` 的帧另有上限 64，超出时只淘汰最旧的 `public` 帧（匿名访客挤不掉主人的帧）。
- 策略 `viewPolicy(ancestors)`，经 `setFrameHeaders` 发出：

```
sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' https:; style-src 'unsafe-inline' https:;
img-src data: blob: https:; font-src data: https:; media-src data: blob: https:; connect-src 'none';
form-action 'none'; base-uri 'none'; webrtc 'block'; frame-ancestors <ancestors>
```

- 文档拼装（Go，`viewDocument`）：在 `<head>` 开头注入 `<meta charset>`、`<meta viewport>`、内联的视图运行时 `sandbox/view.js`（自带基础样式），`libs` 含 `tailwind` 时再注入主题 `<style type="text/tailwindcss">` 与 `sandbox/lib/tailwind.js`，含 `chartjs` 时注入 `sandbox/lib/chart.js` 与默认配色脚本。`html` 有 `<head>` 就插在它后面，只有 `<html>` 就补一个 `<head>`，都没有就整体包成文档。内联脚本里的 `</script` 转义（`inlineScript`，`site.go:53`）。
- 资源由 `webui.Asset` 在 `NewHost` 读一次：`sandbox/view.js`、`sandbox/lib/chart.js`、`sandbox/lib/tailwind.js`。
- 端点（改 `schema/openapi.yaml` → `make gen`）：

| 端点 | 说明 |
|---|---|
| `POST /api/v1/threads/{id}/view-frames`（`openViewFrame`） | body `{ toolCallId }`；201 `ViewFrame { token, url }`。在该线程主会话的条目里找这次调用（`ListEntrySeqsContaining` 取候选，再解析 assistant 消息里 `id` 相同、`name` 为 `render_html` 的 `toolCall`）；找不到 404。`html` 取 `arguments.html`（字符串，参数修复不改它），`libs` 兼容单个字符串与数组。`load` 闭包按 `(threadId, seq)` 直接取行。apipolicy 的登记同 `openArtifactFrame`。 |
| `POST /api/v1/public/shares/{slug}/view-frames`（`openPublicShareViewFrame`） | 匿名；body 同样只有 `{ toolCallId }`，内容一律从服务端条目取，访客不提交 HTML。只在分享范围内找、用清洗后的公开条目（`internal/share` 的 `scope`），且只服务范围内已有成功结果（非 `isError`）的调用。帧令牌绑定这个分享：`load` 闭包每次重新走分享校验，撤销后帧即 404（与 `share.go` 里「撤销后页面、接口、图片一律 404」一致）。apipolicy 的登记同 `getPublicShare`。 |
| `DELETE /api/v1/artifact-frames/{token}` | 关帧，沿用现有端点。 |

### C9 帧内协议

- 信封 `{ v: 1, channel }`，通道与握手同 artifact 帧（父页面在帧 `load` 后发 `hello` 定通道；只认 `event.source === iframe.contentWindow` 且通道一致的消息；单条 ≤64 KiB）。类型与解析放 `web/sandbox/view/messages.ts`，不并入 artifact 的 `PageMessage`。
- 父 → 页：`hello`；`theme { scheme: "light" | "dark", vars: Record<string, string> }`。
- 页 → 父：`ready`；`resize { height }`；`prompt { text }`（≤4000）。
- 首帧主题走 URL 片段：`<url>#theme=<encodeURIComponent(JSON{scheme, vars})>`，运行时在 `<head>` 里同步读出，不闪烁；之后主题变化（`oryn:themechange`）走 `theme` 消息。值里的 `< > ; { }` 一律去掉。
- 主题变量（与 Core 的 SVG 约定、`AskFormView.tsx:56` 的映射同名）：`--fg --muted --border --surface --accent --success --warning --error --c1…--c6`，另加 `--bg --font --mono --radius`。父页面用 `getComputedStyle` 取解析后的值。
- 页面 API：`window.oryn.sendPrompt(text)`、`window.oryn.palette`（`--c1…--c6` 的值）。
- 高度：`height` 是最小高度；帧跟随 `resize` 长高，没有上限，内容不在帧内滚动。页面高度随帧高变化（`100vh`、`height: 100%` 一类）时，运行时连续 3 次看到「帧变高多少内容就跟着变高多少」即认定耦合，报一次 480 px；之后看超出视口的部分 `h - v`，只有它比见过的最小值多出 16 px 才上报（比增量而不是绝对值：`100vh` 加 24 px 页边距每轮都超出视口 24 px，按绝对值判会一路撑高），内容缩到比视口矮也上报。Web 按 `toolCallId` 记住上次高度，重新挂载时不跳动。参数流到第一个块之前（声明式）或帧挂载之前（HTML）显示 `ViewComposing` 占位。

### C10 Web 渲染契约

- `ToolRenderer` 加 `Inline?: ComponentType<ToolInlineProps>`（`web/src/tool-render/types.ts`）；`ToolInlineProps = ToolRenderProps & { toolCallId: string; streaming?: boolean; rootRef?: (el: HTMLDivElement | null) => void }`。`ToolView` 拆成外层（解析渲染器、构造 props）与现有卡片；渲染器有 `Inline` 时外层直接渲染它，不出卡片外壳（保留右键菜单的 `rootRef`）。
- `ToolRenderHost` 加 `view?`：

```ts
export interface ViewHost {
	/** 这次调用在转录里已有的作答：块路径 → 作答。 */
	answers(toolCallId: string): ReadonlyMap<string, ViewAnswer>;
	/** 这次调用是否正挂起等待作答（await）。 */
	awaiting(toolCallId: string): boolean;
	/** 提交作答：挂起时作为工具结果返回，否则作为下一条消息。只读页不提供。 */
	submit?(toolCallId: string, answer: ViewAnswer, text: string): Promise<void>;
	/** 申请 HTML 视图帧，返回帧根地址。 */
	openFrame?(toolCallId: string): Promise<string>;
}
```

- `TranscriptModel` 加 `viewAnswers: Map<toolCallId, Map<块路径, ViewAnswer>>`（`web/src/state/transcript.ts`），来源两处：主人消息里的回执（C4；不看 `parentIrc`、`synthetic` 与 `[Oryn ` 开头的消息）与工具结果的 `details.view.answer`。同一块有多条时取最早的。
- 在途提交（已发出、条目还没回来）记在一个按 `threadId:toolCallId:块路径` 为键的小 store 里，发送失败时清掉。
- 一次调用的四个状态，Web 按它决定显示与可用性：
  - 参数流式中（`streamingArgs`）：`render_ui` 逐块显示、控件禁用；`render_html` 显示骨架。
  - 参数流完、尚未开始执行（没有 `tool_execution_start`，也没有结果）：同上。
  - 执行中（收到 `tool_execution_start`，没有结果）：参数已通过校验。`render_html` 此时就挂帧；有带标记的挂起请求（C5）时控件可用、作答走阻塞通道，否则禁用。
  - 已结束（有结果）：按 C3 的判据显示；作答走非阻塞通道。
- 规格来源的优先级：结果的 `details.view.spec` → `tool_execution_start` 的 `args`（`liveTools[toolCallId].args`，已修复；重连后由快照恢复）→ 流式中的 `toolCall.arguments`。`MessageView.ToolCard` 现在是 `args ?? live?.args`（原始参数优先，`MessageView.tsx:1099`），对有 `Inline` 的渲染器要把三者都传下去。
- 控件可用性：只读页 → 全禁用；块已作答或在途 → 锁定并显示作答；其余按上面的状态。
- 流式：`args` 是逐 delta 补全括号解析出的对象（`message-builder.ts` 的 `parsePartialJson`），末尾的块可能残缺。归一化函数对每个块做最低要求检查，不满足的跳过；`chart` 与 `svg` 在参数流完之前只显示骨架。
- `chart.options` / `data` 进 Chart.js 前深度去掉 `__proto__`、`constructor`、`prototype` 键。没有给颜色的数据集按 `--viz-1…8` 上色，主题切换后重取。
- `svg` 经 `sanitizeSvgMarkup`（`web/src/lib/ask-form.ts:425`）清洗，容器带 `SVG_THEME_VARS` 的映射。
- `width`：`normal` = 最宽 40rem；`wide` = 占满消息列。

### C11 系统提示词指引（`view-tools.json` 的 `guide`，静态文本）

```
# Views (render_ui / render_html)

A view is an interactive answer rendered inline in the conversation. Reach for one whenever the answer has structure:
- comparing options across several dimensions
- numbers, trends, rankings or distributions: stats, a chart, a table
- plans, timelines, checklists, status overviews
- collecting several inputs at once, or offering clear choices to click
- something to play with: a calculator, a what-if slider, a simulator

Plain text is still right for short factual answers, casual chat, code and file changes, prose explanations, or when the user asks for text.

How to do it well:
- Prefer render_ui. Use render_html only when its components cannot express the layout or behaviour.
- Lay it out deliberately: cards for groups, a stats row for KPIs, columns to put a chart beside its table. Use the user's language inside the view.
- One line of lead-in, render once, then at most a short takeaway. Never repeat the view's content in prose.
- A view lives in this conversation. Work the user will keep, iterate on or publish belongs in an artifact.

Input from a view:
- By default a submitted form or a clicked button arrives as the user's next message, wrapped in <oryn-view-answer call="…" block="…">. After rendering a form or buttons, end your turn and wait; never guess the answer.
- Set await: true only when you cannot continue this turn without the answer. The call then returns the user's input as its result. After 10 minutes without input it returns anyway and the view stays usable.
```

工具描述以 Vetta 的两段为底稿，改动：`vetta` → `oryn`；`text` 说明改为 Markdown；补 `svg` 块；HTML 的 CSS 变量换成 C9 的名字；删掉「发送前用户确认」，换成 `await` 的说明。

## 实施步骤

每步一个或几个原子提交，提交信息 `feat(views): ✨ …`。S1 之后 S2 / S3 / S4（Go 与扩展）和 S5 / S6（Web）可以并行，同时在跑的子 agent 不超过 5 个。

### S0 落盘

- 把本文件写到 `docs/plan-m33.md` 并提交（`docs(views): 📝 …`）。

### S1 契约文件

- 新建 `schema/view-tools.json`（C1、C2、C11），在 `schema/embed.go` 导出 `ViewTools []byte`。
- Go 测试：能解析；两个工具名正确；所有 `$ref` 都指向存在的 `$defs`；没有递归引用；文件 ≤16 KiB。
- 验收：测试通过；把两份 schema 与指引合计的 token 数记进提交说明（估计 3–4k，每个请求都带，走提示缓存）。

### S2 设置

- 迁移、查询、`make gen`；`schema/openapi.yaml` 加端点与 `GenerativeUISettings`、`LinkCtlAgentStartParams.viewTools`；`internal/api/generative_ui.go`；apipolicy；`coord.OnGenerativeUIChanged`。
- `internal/threads/agent.go`：加 `ownerGenerativeUI`（读不到时为真，写法同 `ownerLanguage`），在 `prof == nil` 分支设 `params.ViewTools`。
- 测试：API 读写与未初始化所有者的缺省值；`make check-gen`、`make lint-spec`。

### S3 工作线程的工具通道

- `internal/orbdrt`：把 `schema.ViewTools` 加进 orb 与 Runner 的受管文件表（`selfsetup.go:328`、`runtime.go:472` 一带）；`agent.go` 加 `envViewTools` 与写入 / 清除。
- `internal/orbdrt/assets/oryn-orb.ts`：`registerViews(pi)`——读 `ORYN_VIEW_TOOLS` 指向的文件，注册两个工具（C3、C5、C6 的子 agent 处理）；在 orb 与 Runner 两个分支都调用，两处 `before_agent_start` 按 C6 的条件追加 `guide`。
- 测试：`agent_test.go` 仿 `TestAgentStartLanguageEnv`（参数为真时有变量；`p.Env` 里伪造的被清掉）；新增 `testdata/view_tools.ts` + Go 包装（仿 `sandbox_tools.ts`），用假 `pi` 与假 `ctx.ui.input` 覆盖 C3 的六种结果与 `details.view.spec`、`ctx.agent.kind === "sub"` 时的工具隐藏与拒绝、指引的追加条件。

### S4 全局助手的工具通道

- `internal/coord/profile.go`：`ToolRenderUI` / `ToolRenderHTML` 常量；`ToolSet.Views`；`ToolDefs` 末尾追加 `viewToolDefs()`；`AgentProfile` 里 `views := space == nil && !restricted && 开关开启`，为真时追加 `guide`。
- `internal/coord/views.go`：处理函数（只给 `KindAssistant`，其余 `unknown_tool`）。最低检查：`blocks` 是 1–40 个元素的数组 / `html` 长度 1–200 000。非 `await` 立即返回；`await` 走 C5。等待表是 `map[requestID]chan json.RawMessage`，`UIAnswered` 开头按 `vwa_` 前缀分流。结果的 `Details` 按 C3 带 `view.spec`，取自 `HostToolCall.Args`（见 U2）。
- `internal/coord/filetools.go` 的 `HostToolTimeout`；`internal/threads/hostui.go` 的 `RequestUI` 放宽。
- 测试：更新 `threadtools_test.go:224-250` 的工具名单；`await` 的三条路径（作答、超时——超时时长做成包内变量供测试缩短、主人改为打字）；Space / 研究员 / 受限主线拿不到工具。

### S5 Web 骨架

- `web/src/lib/view/spec.ts`（类型、宽容归一化）、`answer.ts`（C4 的生成与解析、块路径）。
- `tool-render`：C10 的类型扩展、`ToolView` 拆分、`tools/render-view.tsx` 两个渲染器并登记进 `registry.ts`，`TOOL_ICON` / `TOOL_CAT` 加项。
- 状态：`transcript.ts` 的 `viewAnswers`；`thread-reducer.ts` 加选择器 `dockPendingUi`（去掉带标记的请求）与 `viewAwaitOf`；`ThreadPage.tsx`（`firstUi` 与 `queued`）、`DockPill.tsx:86`、`RightPanel.tsx:318` 改用 `dockPendingUi`；`actions.ts` 加 `submitViewAnswer` 与 `dismissViewAwaits`，`Composer.submit` 在发送前调后者。
- `TranscriptContext` 加 `view`，`MessageView.ToolCard` 据此构造 `host.view`（只读页不给 `submit`）。
- 回执：`UserMessage`、`Transcript.tsx` 的 `PendingPrompt`、`lib/queue-message.ts` 都先过 `parseViewAnswer`——显示成普通用户气泡加一个「来自视图」的小标，正文去掉末尾的 JSON 围栏；回执不可「回到这里编辑」（`rewind-model.ts` 的 `isRealUserMessage` 同步）。
- 测试：归一化（含残缺的流式输入）、回执的生成与解析往返、`viewAnswers` 的两个来源与「取最早」、`dockPendingUi`、发消息时撤销挂起请求。

### S6 声明式视图

- `web/src/components/view/`：`UiView` 与 18 个块。能用现成控件的直接用（`components/ui/primitives.tsx` 的 `Button / TextInput / TextArea / Select / Toggle / Checkbox / Radio`，`data.tsx` 的 `Badge / Meter / Callout / KeyValue`）；`stats` 大数字、数据表格、标签页、滑块需要新写。颜色只用令牌，圆角只用 `rounded-md / lg`，最小字号 `text-2xs`（`docs/design/web-guidelines.md`）。
- `web/package.json` 加依赖 `chart.js`；`ChartBlock` 用 `import("chart.js/auto")` 懒加载。
- 文案进 `zh-CN.json` 与 `en.json`（`toolRender.renderUi.*`、`view.*`）。
- `web/src/mock/fixtures.ts` 的 `TOOL_ZOO_THREAD` 加样例：仪表盘（stats + chart + table + columns）、未作答的表单、已作答的表单（带回执）、按钮组、`svg`、参数校验失败。
- 测试：每类块的渲染；表单的必填与范围校验、提交后的锁定；按钮组点一次后锁定；流式时控件禁用；只读时全禁用。
- 验收：`bun run shots:all --only tools` 的截图里视图在浅色 / 深色、宽 / 窄下都正常。

### S7 HTML 视图

- Go：C8 的全部（`internal/artifacts` 的 `view.go` 与 `policy.go`，`internal/transcript` 加按 `toolCallId` 取工具调用参数的函数，`internal/share` 加公开查找，`internal/api` 两个端点）。
- 沙箱运行时：`web/sandbox/view/{main.ts,messages.ts,base.css}`；`web/vite.sandbox.config.ts` 加 `view` 入口，并在该模式下把 `chart.js` 的 UMD 产物与 `@tailwindcss/browser` 的全局产物原样输出到 `sandbox/lib/`（做法同 `vite.config.ts` 里 `@silurus/ooxml` 的 `emitFile`）；`web/package.json` 的 `build` 链追加一步，`@tailwindcss/browser` 进 devDependencies。
- Web：`components/view/HtmlView.tsx` 与 `view-link.ts`。调用进入 C10 的「执行中」后申请帧（此时条目可能还没落库，开帧 404 时退避重试 3 次）；`<iframe sandbox="allow-scripts" referrerPolicy="no-referrer">`；8 秒没收到 `ready` 就重新申请一次（orynd 重启或帧被淘汰），再失败显示可重试的错误态。帧地址按 `toolCallId` 缓存在线程页的生命周期里，离开线程时统一关帧。`prompt` 消息按 C4 走 `submit`，每次调用只收第一条。
- 测试：`viewPolicy` 的字面量；`viewDocument` 的三种插入位置与 `</script` 转义；开帧的命中 / 未命中 / 非 `render_html`；公开端点的范围校验与撤销后 404；匿名配额不淘汰主人的帧；`view-link` 只认自己帧与通道的消息。
- mock 模式（`VITE_MOCK=1`）没有帧服务：mock 的 `view-frames` 返回 501，样例线程里放一条 `render_html` 用来截错误态；真实运行由验收脚本覆盖。

### S8 公开分享页

- `web/src/pages/SharePage.tsx` 的 `ctx` 加 `view`：`answers` 取模型里的 `viewAnswers`，`openFrame` 调公开端点，没有 `submit`。
- 测试：分享页里表单显示且禁用、已作答的显示作答；HTML 视图申请的是公开端点。

### S9 设置界面

- `web/src/api/generative-ui-queries.ts`（写法同 `language-queries.ts`）；`ExtensionsPage` 的开关行；`web/src/mock/server.ts` 的路由；文案。
- 测试：开关的读写与乐观更新。

### S10 文档与验收脚本

- `docs/features.md` 加「视图」一节；`docs/protocol.md` 补工具表、`ORYN_VIEW_TOOLS`、`viewTools`、C4 与 C5 的格式、两个端点；`README.md` 功能段加一条。
- `web/scripts/m33-accept.ts` + `package.json` 的 `accept:m33`（结构同 `m3-accept.ts`），`docs/development.md` 加一节。

## 验证

- Go：`go test ./schema/... ./internal/coord/... ./internal/orbdrt/... ./internal/threads/... ./internal/artifacts/... ./internal/share/... ./internal/api/...`，`make check-gen`，`make lint-spec`。
- Web：`bun run check`，`bun run test`，`bun run shots:all --only tools`。
- 真机验收 `bun run accept:m33`（运行中的 orynd + docker provider，前置同 `accept:m3`），逐项打印 `T<n> PASS|FAIL`：
  - T1 工作线程：`render_ui` 出表单，提交后 agent 收到 `<oryn-view-answer>` 消息并继续；刷新页面后表单仍是已提交。
  - T2 `await: true`（`render_ui` 与 `render_html` 各一次）：视图已经可操作（HTML 的帧已挂载）而此时还没有工具结果，线程是 `awaiting_input`；提交之后才出现工具结果，同一回合继续，结果含作答。
  - T3 `await` 期间在输入框发消息：调用以 `dismissed` 结束，消息送达。
  - T4 `render_html` + `libs: ["chartjs"]`：帧里图表画出；`sendPrompt` 送达；第二次调用被忽略。
  - T5 帧的 CSP：`fetch` 被拒；`https` 图片能加载；帧读不到父页面。
  - T6 关掉开关后新启动的 agent 没有这两个工具，旧视图仍显示。
  - T7 公开分享页：声明式视图禁用；HTML 视图能运行；撤销分享后帧 404。
  - T8 全局助手：非阻塞与 `await` 各走一遍。
  - T9 没有工具白名单的默认子 agent（task 工具派出）与 `/tan` 克隆：工具列表里都没有 `render_ui` / `render_html`，系统提示词里没有视图指引。
  - T10 第二台设备打开同一线程，看到同样的锁定状态。
  - T11 一次被 Core 修复过参数的调用（`blocks` 被二次编码成字符串）：实时显示与刷新后的显示一致。

## 未验证项与兜底

实现到对应步骤时先验证，结论写进提交说明。

- U1 `session_start` 的处理函数按注册顺序执行、子会话里 `setActiveTools` 生效——没有实跑；`ctx.agent.kind` 的语义来自 `extensions/types.ts:648-667` 的文档注释。兜底已在 C6：`execute` 开头的硬拒绝不依赖顺序。
- U2 Core 对宿主工具（全局助手）也按 schema 校验参数，且 `host_tool_call` 帧里的参数是修复后的——推断（宿主工具同样经 agent 循环），没有实跑。兜底已在 S4：Go 侧的最低检查，加上 Web 的宽容归一化。
- U3 线上配置的各 provider 都接受带 `$defs` / `$ref` 的工具 schema。Core 对多数 provider 原样保留 `$ref`，只有个别路径会内联（`normalize.ts:1319`）；按层展开的 schema 内联后会膨胀到几十倍。S1 完成后用实际配置的每个 provider 各发一次带这两个工具的请求。若有 provider 不接受：把容器的子块改成 Vetta 的做法（子块只描述为「任意上面列出的块」），嵌套块的校验退到 Web 的归一化。
- U4 iOS WKWebView 里转录内 iframe 的触摸与滚动冲突，T4 需要在 iOS 壳里再走一遍。
- U5 `chart.js` 的 UMD 文件名与 `@tailwindcss/browser` 的全局产物路径，以安装后的 `dist/` 为准。
- U6 orynd 重启后，orbd 在新流上重放在途的宿主工具调用（`hostToolCallLocked` 的去重注释），助手的 `await` 会重新发起请求、重新计时——推断，没有实跑；D14 已声明不保证。

## 风险（主人已知或需知）

- 视图帧可以向模型指定的任意 `https` 主机发 GET（脚本、样式、图片），对方能看到 IP 与 UA，URL 里可以带模型写入的数据。orb 内 agent 本身可联网，这不是新的外泄能力；但全局助手关闭联网时，HTML 视图仍是一条出站路径。
- 视图里的 `<img>` 也能指向 Oryn 自己的地址。会话 Cookie 是 `SameSite=Lax`、帧在不透明源里，不会带上登录态；这依赖 GET 接口没有副作用。
- D16 不要求用户手势：模型写的页面可以在加载时就调用 `sendPrompt`。每次调用只收一条（D17）限制了次数，但两台设备同时打开时可能各发一条。指引要求只在用户操作时调用。
- 两台设备同时提交同一个块会产生两条消息，锁定显示取最早的一条；不做服务端去重。
- 两个工具的 schema 与指引每个请求都带（估计 3–4k token，走提示缓存）。关掉开关即不带。
- 视图没有独立存储：内容就是转录里的工具调用参数，单个字符串超过 500 000 字符会被 Core 截断（`html` 上限 200 000，在此之内）。

## 不做

- Core 的 Visuals 指引（```` ```svg ```` 围栏、表格自动成图）接入 Web。
- 视图转存为 artifact、视图的导出与打印。
- HTML 视图的全屏 / 弹出窗口。
- Space 协调者、子 agent、研究员里的视图。
- 服务端对作答去重或校验。
