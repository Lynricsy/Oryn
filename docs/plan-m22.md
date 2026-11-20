# M22 — 上下文菜单（右键 / 菜单键 / 长按）与选区旁路提问：实施计划

> 编号按现状取 M22：`docs/plan-m21.md` 已是「Runner 改用 Oryn Core」计划（`3e627da`），**不要碰那个文件**；它在范围外清单里把原生 Windows Runner「建议 M22」，那只是建议，没有占号。动手时若 `docs/plan-m22.md` 已被占用则顺延，并同步改本文里的 `accept:m22`、`m22-accept.ts`、`.vite-m22`、`.dev/m22/`。M21 也会新建 `web/scripts/m21-accept.ts` 并在 `web/package.json` 注册 `accept:m21`，本计划不动这两处。
> 本文自包含：执行者不需要读过产生它的对话。行号只作提示（取自 `1535a08`），动手前按符号重读。仓库里有其他会话在并行改动：写本文时工作区有 M21 未提交的 `schema/openapi.yaml`、`internal/apigen/apigen.gen.go`、`web/src/api/schema.d.ts`、`web/package.json`、`web/src/mock/server.ts`、`internal/threads/agent.go`，以及别人的 `web/src/components/ui/primitives.tsx` 等。提交时只带自己改的行。
> 计划经过一轮对照代码的只读评审，评审发现的问题已并入各步骤。

## Context

要求：接管右键菜单，并放进一批纯 TUI 做不了、放在右键菜单里合适的功能；代表功能是「解释」——选中一段文字让模型解释，像 btw 一样复用主会话的前缀缓存，并且模型要明确知道选中的是哪一段。

终态：网页侧（浏览器、PWA、桌面壳、移动壳共用同一份页面）有一个统一的上下文菜单——鼠标右键、键盘菜单键 / Shift+F10、触屏长按三种触发——覆盖转录、侧栏线程行、收件箱行、文件树、Artifact 卡、文件查看器 / 编辑器、改动列表、终端；选中文字时提供「解释 / 就此提问… / 翻译 / 自定义快捷动作」，结果显示在选区旁的悬浮解释卡里。

已定的取舍（实现时不再改）：

- 「解释」类动作复用现有 `btw` RPC，**不改 Core**。前端把选区原文、前后文、来源描述拼进 `question`；Core 的 btw 用主会话同一份 system prompt、tools 与历史消息，只在尾部追加问题，模型与思考档位沿用主线，所以前缀缓存原样命中。记录进 btw 历史。
- 模型上下文里没有消息 id，定位只能靠文字：选区原文 + 同一处前后各 120 个码点 + 来源描述（谁说的、几个用户回合之前、正文 / 思考 / 工具）；来源早于最近一次压缩时注明原文可能已不在上下文。
- 接管策略：有菜单项才接管；普通输入框（`input` / `textarea` / `select` / `contenteditable`）一律原生菜单；Shift+右键一律原生；没有菜单项时桌面壳屏蔽原生菜单、浏览器放行。
- 触屏：对象上长按 500ms 弹同一份菜单（文字选择仍交给系统）；手机宽度（compact）且由触屏触发时是底部动作面板，其余是浮动菜单；「解释」同时进现有的选区胶囊。
- 已有旁路提问在跑：是解释卡这类信封问题 → 取消它换新的；是用户在 btw 对话框里手动问的 → AI 菜单项置灰。关闭解释卡时仍在生成 → 取消生成。
- 自定义快捷动作存服务端（owner 表一列，GET / PUT 整体替换），各端同步。
- 线程行菜单是核心集（见步骤 4.1）；改动列表不做「丢弃改动」（后端没有接口）。
- 记为 M22，在 `main` 上按步骤原子提交。

## Approach

### 通用约定

- 工作目录 `/root/Projects/Ling/Oryn`，分支 `main`。每个步骤一个提交，格式 `<type>(<scope>): <gitmoji> <subject>`，带 `Co-authored-by: Wine Fox <fox@ling.plus>`。工作区是多会话共用的：提交只带本步骤自己改的行，不用 `git add -A` / `git commit -a`，也不动别人未提交的改动。
- 每个阶段结束用 `record-agent-log` 记一条（做了什么、为什么这样做）；阶段 0 那条带上本文「已定的取舍」。
- 每个步骤结束都要通过：`cd web && bun run check && bun run test`。步骤 7.1 另跑 `make gen`、`make lint-spec`、`go test ./internal/api/... ./internal/store/...`；`make check-gen` 放在该步骤提交**之后**跑（它是 `make gen` 加 `git diff --exit-code`，比的是工作区与暂存区，`Makefile:30-31`；生成物还没提交时必然报差异）。
- 动手前读 `skill://react`、`skill://typescript`；步骤 7.1 读 `skill://go`；步骤 8.1 读 `skill://web-testing`。
- 视觉沿用现有令牌，不开新方向：菜单与解释卡用 `.glass pop-in rounded-lg shadow-lg`，菜单项用 `rounded-md`，进入动画只有 `.pop-in`、退出不做动画（`docs/design/ui-redesign.md:19-22, 45-56, 77`）。
- 新文案同时加进 `web/src/i18n/locales/zh-CN.json` 与 `en.json`（`locales.test.ts` 要求 key 集合一致），全表见「文案」一节。新增三个顶层命名空间 `contextMenu`、`sideAsk`、`selectionActions`（已确认不存在）。
- 复制一律用 `copyToClipboard(text): Promise<boolean>`（`web/src/lib/clipboard.ts`）；返回 `false` 时 `showToast({ text: i18n.t("contextMenu.copyFailed"), tone: "err" })`，成功不提示。

### 阶段 0 — 入库

**步骤 0.1** 先确认 `docs/plan-m22.md` 不存在（存在则按文首说明顺延），再把本计划原文存为 `docs/plan-m22.md` 并提交（`docs(plan): 📝 M22 上下文菜单与选区旁路提问计划`）。写任何新文件前都先看目标路径是否已有内容，已有就停下来核对，不覆盖。

### 阶段 1 — 菜单基础设施

**步骤 1.1 — 类型、注册表、选区快照**

新建 `web/src/lib/context-menu/types.ts`，内容就是下面这些类型（后续步骤都从这里引用）：

```ts
import type { MenuItem } from "../../components/ui/primitives";

export type ContextTrigger = "mouse" | "keyboard" | "touch";
export interface Anchor { left: number; top: number; right: number; bottom: number }
export interface MenuGroup { key: string; items: MenuItem[] }

/** 触发那一刻的 DOM 选区快照（活的 Range 会被流式重渲染冲掉，不外传）。 */
export interface DomSelection {
	/** 选中的文字；KaTeX 公式换回 TeX 源码 */
	text: string;
	/** 包住整个选区的元素 */
	root: Element;
	startEl: Element;
	endEl: Element;
	/** 各行的视口矩形（宽度 > 0）；引擎没有 Range.getClientRects 时为空数组 */
	rects: DOMRect[];
	/** scope 内、选区之前 / 之后的文字，空白压成单个空格，取紧邻选区的 max 个码点 */
	before(scope: Element, max: number): string;
	after(scope: Element, max: number): string;
}

export type SelectionSource =
	| { kind: "message"; role: "assistant" | "user"; part: "text" | "thinking"; turnsAgo: number | null; inContext: boolean; live: boolean; spans: boolean }
	| { kind: "tool"; toolName: string; turnsAgo: number | null; inContext: boolean; spans: boolean }
	| { kind: "file"; path: string; startLine?: number; endLine?: number }
	| { kind: "diff"; path: string }
	| { kind: "terminal" }
	| { kind: "side" }
	| { kind: "other" };

export interface SelectionContext {
	/** 回答问题的线程；null = 这里不提供 AI 动作 */
	threadId: string | null;
	text: string;
	before: string;
	after: string;
	source: SelectionSource;
	/** 解释卡的锚点（视口坐标） */
	anchor: Anchor;
	/** 给出时菜单里有「引用回复」 */
	quote?: () => void;
	/** 给出时菜单里有「引用到输入框」，返回要插入草稿的文本 */
	insert?: () => string;
}

export interface ContextRequest {
	trigger: ContextTrigger;
	x: number;
	y: number;
	target: Element;
	selection: DomSelection | null;
	askThreadId: string | null;
}

export type ProviderResult = MenuGroup[] | "suppress" | null;

export interface ContextTargetSpec {
	/** 这个元素自己的菜单组；"suppress" = 不弹菜单也不放行原生菜单 */
	items?(req: ContextRequest): ProviderResult;
	/** 认领落在这个元素里的 DOM 选区；定义了它（即使返回 null）外层就不再认领 */
	selection?(sel: DomSelection, req: ContextRequest): SelectionContext | null;
	/** 这个子树里 AI 动作归哪个线程；null = 明确禁用 */
	askThreadId?: string | null;
}
```

新建 `web/src/lib/context-menu/selection.ts`：

- `selectionText(range: Range, within: Element, plain: () => string): string` —— 把 `quote-selection.tsx:57-86` 的 `selectedText` 整段搬过来：原来两处 `sel.toString()`（没有公式、克隆数对不上时的回退）换成 `plain()`，其余不变。调用方传 `() => sel.toString()`。**不能**换成 `range.toString()`：Markdown 开了 remark-breaks，软换行渲染成 `<br>`，`Range.toString()` 只拼文本节点，会把多行粘成一行；`Selection.toString()` 才按渲染结果换行（jsdom 里两者一样，测试看不出来）。`quote-selection.tsx` 改为从这里引入，删除原函数。
- `readDomSelection(): DomSelection | null` —— `sel = document.getSelection()` 为空、`rangeCount === 0`、`isCollapsed` 或文字全是空白时返回 null；`root` = `commonAncestorContainer` 提升到元素；`text = selectionText(range, root, () => sel.toString())`；`rects` 用 `typeof range.getClientRects === "function"` 守卫（jsdom 没有）；`before` / `after` 在调用时用新建 Range（`selectNodeContents(scope)` 后把终点 / 起点移到选区起点 / 终点）取 `toString()`，`replace(/\s+/g, " ").trim()` 后按码点截取。
- `hitsSelection(sel: DomSelection, x: number, y: number, target: Element): boolean` —— `rects` 非空时判断点是否落在任一矩形内（外扩 2px）；为空时退回 `sel.root.contains(target)`。
- `anchorOf(sel: DomSelection | null, x: number, y: number): Anchor` —— `rects` 非空取并集，否则 `{ left: x, top: y, right: x, bottom: y }`。

新建 `web/src/lib/context-menu/selection-groups.ts`，本步骤只做最小版：`selectionGroups(sel: SelectionContext): MenuGroup[]` 返回一个 `key: "selection"` 的组，含「引用回复」（`sel.quote` 存在时，文案 `transcript.quoteReply`，图标 `quote`）与「复制」（`common.copy`，图标 `copy`，复制 `sel.text`）。步骤 2.5 补全。

新建 `web/src/lib/context-menu/registry.ts`：

- 模块级 `WeakMap<Element, { current: ContextTargetSpec }>`。
- `useContextTarget<T extends Element = HTMLElement>(spec: ContextTargetSpec): (el: T | null) => void` —— 返回稳定的 ref 回调；spec 存在 `useRef` 里每次渲染更新（回调读到的永远是最新 props）；元素换了或卸载时删旧登记。元素已有别的 ref 时在调用处内联合并：`ref={(el) => { own.current = el; ctxRef(el); }}`。
- `resolveAskThread(from: Element | null): string | null` —— 从 `from` 向上找第一个 `askThreadId !== undefined` 的 spec，返回其值；找不到返回 null。
- `resolveContext(base: Omit<ContextRequest, "askThreadId">): { groups: MenuGroup[]; suppress: boolean }`：
  1. `askThreadId = resolveAskThread(base.target)`，拼出完整 `req`。
  2. 选区组：`req.selection` 非空时，从 `selection.root` 向上找第一个定义了 `selection` 的 spec，调用它；返回非 null → `selectionGroups(ctx)`；返回 null → 没有选区组。一路没有 spec 定义 `selection` → 用兜底上下文 `{ threadId: null, text, before: "", after: "", source: { kind: "other" }, anchor: anchorOf(...) }` 调 `selectionGroups`。
  3. 对象组：从 `req.target` 向上，每个有 `items` 的 spec 调一次，数组依次追加（内层在前）；任何一个返回 `"suppress"` → 结果 `{ groups: [], suppress: true }`。
  4. 第 2 步产出了选区组时，第 3 步的对象组只保留 key 为 `link`、`image` 的（用户此刻操作的是选区，不再把「复制消息 / fork」之类一起列出来）。选区组排在对象组前面；按 `group.key` 去重（先出现的留下）；丢掉 `items` 为空的组。

**步骤 1.2 — 菜单状态与宿主**

新建 `web/src/lib/context-menu/store.ts`：

```ts
export interface OpenContextMenu {
	groups: MenuGroup[];
	x: number;
	y: number;
	trigger: ContextTrigger;
	/** 键盘触发时关闭后把焦点还给它 */
	restoreFocus: HTMLElement | null;
}
export const contextMenuStore: StoreApi<{ menu: OpenContextMenu | null }>;
export function openContextMenu(menu: OpenContextMenu): void;   // 已有打开的直接替换
export function closeContextMenu(restoreFocus?: boolean): void; // restoreFocus 为真且 restoreFocus 元素仍在文档里时 focus({ preventScroll: true })
```

新建 `web/src/components/ui/ContextMenu.tsx`，导出 `ContextMenuHost(): ReactNode`，读 store，`createPortal` 到 `document.body`。两种形态：

- **浮动菜单**（`!(useBreakpoint() === "compact" && menu.trigger === "touch")`）：
  - 根：`<div role="menu" tabIndex={-1} data-testid="context-menu" data-quote-affordance="" className="glass pop-in fixed z-50 min-w-52 max-w-72 overflow-y-auto rounded-lg p-1 shadow-lg" style={{ left, top, maxHeight: "calc(100dvh - 16px)" }}>`。`data-quote-affordance` 是为了让 `useQuoteSelection` 的捕获阶段 `pointerdown` 忽略菜单内的按下（`quote-selection.tsx:228-238`）。
  - 定位：首帧按 `(x, y)` 渲染，`useLayoutEffect` 里量实际宽高后夹取：`left = clamp(x, 8, innerWidth - w - 8)`；`y + h > innerHeight - 8` 时 `top = max(8, y - h)`，否则 `top = y`。
  - 项的渲染与类名照抄 `Menu`（`primitives.tsx:338-391`：`leading` / `icon`、`checked`、`danger`、`disabled`、`hint`、`title`、`attrs`、`children` 箭头），每项多加 `data-menu-key={item.key}`；组与组之间 `<div role="separator" className="my-1 h-px bg-[var(--glass-line)]" />`。项的 `role` 必须与 `Menu` 相同（`checked === undefined` → `menuitem`，否则 `menuitemradio` + `aria-checked`）：现有验收脚本在文件树与 Artifact 卡上右键后按 `getByRole("menuitem", { name })` 点项（`web/scripts/m5-accept.ts:1638-1639`、`web/scripts/m14-artifacts-accept.ts:1040-1041, 2063-2064, 6119-6120`），迁移后要照常能点到。
  - 子菜单沿用 `Menu` 的原地换页（顶部带返回行，`primitives.tsx:328-337`），只支持一级。
  - 不移动 DOM 焦点到菜单项：用 `active` 下标 state 表示高亮项（类名 `bg-hover`，根上 `aria-activedescendant`）。鼠标触发时不改焦点（保住选区高亮）；键盘触发时 `focus({ preventScroll: true })` 到根，并把 `active` 设为第一个可用项。每个按钮 `onMouseDown={(e) => e.preventDefault()}`。
  - 打开期间在 `document` 捕获阶段监听 `keydown`：`ArrowDown` / `ArrowUp`（跳过 disabled，循环）、`Home` / `End`、`Enter` / `Space`（执行高亮项）、`ArrowRight`（进子菜单）、`ArrowLeft` / `Backspace`（子菜单里返回）、`Escape` 与 `Tab`（关闭，`closeContextMenu(true)`）——这些键都 `preventDefault()` + `stopPropagation()`（`Dialog` 在 window 上听 Esc，不拦会连对话框一起关）。带 `ctrlKey` / `metaKey` 的其他键：关闭菜单后放行。
  - 关闭时机：菜单外的 `pointerdown`（document 捕获）、菜单外的 `wheel` 与 `touchmove`（window 捕获）、`resize`、window `blur`。**不要**听 `scroll`：转录流式输出时每个增量都会 `el.scrollTop = el.scrollHeight`（`Transcript.tsx:736-738`），window 捕获到的 `scroll` 会把刚打开的菜单立刻关掉。
  - 选中项：有 `children` → 进子菜单；否则先 `closeContextMenu(menu.trigger === "keyboard")` 再调 `onSelect`。
- **底部动作面板**（compact 且触屏触发）：
  - `<div data-testid="context-sheet" className="fixed inset-0 z-50 flex items-end">`，里面一个 `bg-scrim` 的全屏关闭按钮和面板 `<div role="menu" className="pop-in relative max-h-[70dvh] w-full overflow-y-auto rounded-t-xl bg-panel p-2 pb-safe shadow-xl ring-1 ring-[var(--glass-line)]">`。
  - 用 `pushModal(host)`（`web/src/lib/modal.ts:25`）让背景 inert，Esc 关闭。浮动形态**不要**调 `pushModal`：inert 会把页面上的选区高亮清掉。
  - 项同上但行高 `min-h-11`、字号 `text-base`；子菜单同样原地换页。

在 `web/src/App.tsx:109-166` 把 `<ContextMenuHost />` 放进 `<ErrorBoundary>`、与 `<Suspense>` 并列（这样 `/s/:slug`、`/login` 等不经过 AppShell 的路由也有宿主）。宿主不依赖 Router。

**步骤 1.3 — 全局分发器**

新建 `web/src/lib/context-menu/dispatch.ts`，导出 `installContextMenu(): () => void`（返回卸载函数）；在 `web/src/main.tsx` 的 `installNoFocusZoom()` 之后调用。常量：

```ts
const PLAIN_EDITABLE = 'input, textarea, select, [contenteditable=""], [contenteditable="true"]';
const CUSTOM_SURFACE = ".cm-editor, .xterm";
const LONG_PRESS_MS = 500;
const LONG_PRESS_SLOP = 10;
const KEY_ECHO_MS = 500;
```

共用的收尾函数 `finish(e, base, restoreFocus)`：`const { groups, suppress } = resolveContext(base)`；`groups` 非空 → `e.preventDefault()` + `openContextMenu(...)`，返回 true；否则 `suppress` 为真、或（`host.desktop !== null` 且 `base.target` 不在 `.cm-editor` 里）→ 只 `e.preventDefault()`；其余情况什么都不做（放行原生菜单）。

- **鼠标**：`document` 上冒泡阶段的 `contextmenu`。依次：`e.defaultPrevented` → 返回；目标在菜单自己里面（`[data-testid="context-menu"], [data-testid="context-sheet"]`）→ `preventDefault()` 后返回；距上次键盘触发不足 `KEY_ECHO_MS` → `preventDefault()` 后返回（吞掉菜单键自己派生出的那次事件）；事件 `pointerType === "touch"`（Android 长按）→ 目标在 `[data-ctx-touch]` 里就 `preventDefault()`，然后返回（长按计时器负责开菜单）；`e.shiftKey` → 返回；目标不在 `CUSTOM_SURFACE` 里且在 `PLAIN_EDITABLE` 里 → 返回；否则 `sel = readDomSelection()`，`hitsSelection` 不成立就当没有选区，`finish(e, { trigger: "mouse", x: e.clientX, y: e.clientY, target, selection }, null)`。
- **键盘**：`document` 捕获阶段的 `keydown`，键是 `ContextMenu` 或带 Shift 的 `F10`。`active = document.activeElement ?? document.body`；`active` 在 `PLAIN_EDITABLE` 或 `.xterm` 里 → 返回（终端里 Shift+F10 是给 TUI 程序的）。`sel = readDomSelection()`，只在焦点与选区相关时才用它——`active === document.body`、`active.contains(sel.root)` 或 `sel.root.contains(active)`——否则当没有选区（先在转录里选过字、再把焦点移到文件树行或 Artifact 卡上按 Shift+F10，要出的是那一行自己的菜单）。用选区时 `target` 取 `sel.endEl`、坐标取最后一个矩形的右下角；不用选区时 `target = active`，坐标取其矩形的 `(min(left + 24, right), bottom)`。`finish(e, { trigger: "keyboard", ... }, active)`；返回 true 时记下时间戳供上面的回声判断。
- **触屏长按**：`document` 捕获阶段、passive 的 `pointerdown`，只认 `pointerType === "touch"` 且 `target.closest("[data-ctx-touch]")` 存在。起 `LONG_PRESS_MS` 计时；`pointermove` 位移超过 `LONG_PRESS_SLOP`、`pointerup`、`pointercancel` 都取消（不听 `scroll`，理由同步骤 1.2；手指开始滚动时浏览器自己会发 `pointercancel`）。到时：`resolveContext({ trigger: "touch", x, y, target, selection: null })`，有组就 `document.getSelection()?.removeAllRanges()`、`navigator.vibrate?.(8)`、`openContextMenu(...)`，并置一个「吞掉下一次点击」的标记——`document` 捕获阶段的 `click` 见到标记就 `preventDefault()` + `stopPropagation()` 并清标记；标记在下一次 `pointerdown` 或 700ms 后自动清掉。

在 `web/src/index.css` 的 `@layer components` 里加：

```css
[data-ctx-touch] { -webkit-touch-callout: none; }
@media (pointer: coarse) {
	[data-ctx-touch] { -webkit-user-select: none; user-select: none; }
}
```

`data-ctx-touch` 只打在成块的、不承载正文的对象上（各步骤点名）；正文里的行内链接不打（否则触屏上整段选不全）。

测试约定：需要菜单的组件测试在 `beforeEach` 里 `const off = installContextMenu()`，`afterEach` 里 `off(); closeContextMenu()`，渲染时把 `<ContextMenuHost />` 与被测组件并列；触发用 `fireEvent.contextMenu(el, { clientX, clientY })`。

**步骤 1.4 — 文件树迁到统一菜单**

`web/src/components/files/FileTree.tsx`：

- 删除：私有 `ContextMenu` 组件（`:442-530`）、`MenuState`、`menu` state 与 `openMenu`、`LONG_PRESS_MS` 与 `press` ref、行上的 `onContextMenu` / `onPointerDown` / `onPointerUp` / `onPointerMove`（`:339-352`）、根容器的 `onContextMenu`（`:414-418`）、`onRowKeyDown` 里 `ContextMenu` / `F10` 两个 case（`:220-229`）。
- 在根 `div[role="tree"]` 上用 `useContextTarget`，`items(req)`：`row = req.target.closest('[role="treeitem"][data-path]')`；有行就按 `data-path` 找到 `FileEntry`，没有行且 `req.target` 就是根容器本身则 `entry = null`，其余返回 null。`actions = actionsFor(entry, caps)`，为空返回 null；否则返回 `[{ key: "file", items }]`，每项 `{ key: action, icon: ACTION_ICON[action], label: <现有各动作的文案>, danger: action === "delete", onSelect: () => onAction(action, entry) }`。
- 行加 `data-ctx-touch=""`。
- 行尾「⋯」按钮（原 `openMenu(e, r.right, r.bottom)`）改为 `openContextMenu({ groups: <同上>, x: r.right, y: r.bottom, trigger: "keyboard", restoreFocus: <该行元素> })`。
- `FileTree.tsx` 文件头注释（`:2`）改成与现状相符：操作菜单由统一上下文菜单（`lib/context-menu`）提供，本文件只登记菜单项与行尾「⋯」。
- `FileTree.test.tsx`：`setup()` 把 `<ContextMenuHost />` 与 `FileTree` 并列渲染，`beforeEach` / `afterEach` 按「测试约定」装卸分发器。`:144` 的 Shift+F10 用例断言 `screen.getByTestId("context-menu")` 里有该行的动作。`:126` 的「⋯」用例里 `fireEvent.keyDown(window, { key: "Escape" })`（`:139`）改成 `fireEvent.keyDown(document.activeElement as Element, { key: "Escape" })`——新菜单在 `document` 捕获阶段听键，派到 `window` 上的事件不经过 `document`；其余断言（菜单含焦点、Esc 后焦点回到该行）不变。
- `FileBrowser.test.tsx`：辅助函数 `openMenu`（`:130-137`）点「⋯」后查 `getByRole("menu")`，四个用例靠它（`:206, 231, 533, 612`）。在该文件渲染 `FileBrowser` 的地方并列渲染 `<ContextMenuHost />`，`afterEach` 里 `closeContextMenu()`；`openMenu` 本身不用改（新宿主的根同样是 `role="menu"`）。

**步骤 1.5 — Artifact 卡迁到统一菜单**

- `web/src/components/artifacts/ArtifactMenu.tsx`：把 prop `openRef`（`:208, 220, 439`）换成 `itemsRef?: RefObject<(() => MenuItem[]) | null>`，每次渲染 `itemsRef.current = () => items`（`items` 就是它传给 `Menu` 的那份，`:276`）；`trigger` 里给 `openRef.current` 赋值的那行与它的注释一并删掉（`CardCell` 是 `openRef` 唯一的使用者，迁移后没人用了）。对话框 state 仍在 `ArtifactMenu` 里，`onSelect` 闭包照常工作。
- `web/src/pages/ArtifactsPage.tsx` 的 `CardCell`（`:187-240`）：`openMenu` ref（`:208`）换成 `const itemsRef = useRef<(() => MenuItem[]) | null>(null)`，传给 `ArtifactMenu` 的 `itemsRef`；删 `onContextMenu`（`:227-230`）与 `onKeyDown` 里 `ContextMenu` / Shift+F10 分支（`:220-226`，`onKeyDown` 只剩 `onKey(e, index)`）；在外层 `<li>` 上 `useContextTarget({ items: () => { const items = itemsRef.current?.() ?? []; return items.length ? [{ key: "artifact", items }] : null; } })`，并加 `data-ctx-touch=""`。右上角「更多」按钮保持不变。
- `web/src/components/artifacts/ArtifactCard.tsx`：删 `onContextMenu` prop（`:107, 119, 207`），唯一调用方是上面的 `CardCell`。

### 阶段 2 — 选区旁路提问

**步骤 2.1 — 提问信封**

新建 `web/src/lib/side-ask.ts`（纯函数，带单测 `side-ask.test.ts`）：

```ts
export type SideAction =
	| { kind: "explain" }
	| { kind: "translate" }
	| { kind: "ask"; question: string }
	| { kind: "custom"; name: string; prompt: string }
	| { kind: "tool" };
export interface ParsedSideQuestion {
	action: "explain" | "translate" | "ask" | "custom" | "tool";
	name?: string;      // custom
	selected: string;
	ask?: string;       // ask：用户自己的问题
}
export const SELECTION_MAX = 4000;
export const AROUND_MAX = 120;
export function describeSource(source: SelectionSource): string;
export function clipSelection(raw: string): string;
export function composeSideQuestion(action: SideAction, sel: Pick<SelectionContext, "text" | "before" | "after" | "source">, language: string): string;
export function parseSideQuestion(question: string): ParsedSideQuestion | null;
```

信封的字面格式（`composeSideQuestion` 的输出，逐字符照此）：

```
<oryn-selection action="{action}" name="{name}" source="{source}">
<before>{before}</before>
<selected>
{selected}
</selected>
<after>{after}</after>
</oryn-selection>

{instruction}
```

- `name="…"` 只在 `custom` 时出现；`<before>` / `<after>` 行在内容为空时整行省略。
- 属性值转义：`&`→`&amp;`、`"`→`&quot;`、`<`→`&lt;`、`>`→`&gt;`、换行→空格。
- 三段正文里的 `</selected>`、`</before>`、`</after>`、`</oryn-selection>` 把 `</` 换成 `<\/`；`parseSideQuestion` 还原。
- `{selected}` = `clipSelection(sel.text)`：`\r\n?`→`\n`；去每行行尾空白；去首尾空行；超过 `SELECTION_MAX` 个码点时取前 3000 + `\n…[{n} characters omitted]…\n` + 后 1000（`n` = 总数 − 4000）。
- `{before}` / `{after}` 已是单行（`DomSelection.before/after` 压过空白），截到 `AROUND_MAX`。
- `{source}` = `describeSource(sel.source)`，英文：
  - message + assistant + text：`the assistant's reply`；assistant + thinking：`the assistant's reasoning (thinking block)`；user：`the user's message`。
  - tool：`` the `{toolName}` tool call (its arguments or output) ``。
  - 上面两类后面接回合短语：`turnsAgo === 0` → ` in the current turn`；`1` → `, one user turn back`；`n` → `, {n} user turns back`；`null` → 不接。`live` → 再接 ` (still streaming)`。`spans` → 再接 ` (the selection continues into what follows)`。`inContext === false` → 再接 `; this part precedes the last compaction, so the original may no longer be in your context - rely on the excerpt`。
  - file：`file {path}`，有行号时接 `, lines {startLine}-{endLine}`，再接 `, as shown in the user's file viewer; it may not be in your context - rely on the excerpt`。
  - diff：`the git diff of {path} in the user's changes panel; it may not be in your context - rely on the excerpt`。
  - terminal：`output in the user's terminal panel; it may not be in your context - rely on the excerpt`。
  - side：`an earlier side answer shown to the user`。other：`text shown in the conversation UI`。
- `{instruction}`，其中 `lang` = `language` 以 `zh` 开头时 `Simplified Chinese`、否则 `English`，`alt` 取另一个；`LOCATE` = `<before> and <after> are the text right around it in the same place, given only to pinpoint which occurrence is meant; do not act on them.`：
  - explain：`Explain the passage inside <selected>, which the user highlighted in the conversation UI. {LOCATE} Say what it means and why it matters here, drawing on this conversation's context. Respond in {lang}.`
  - translate：`Translate the passage inside <selected>, which the user highlighted in the conversation UI, into {lang}; if it is already in {lang}, translate it into {alt}. {LOCATE} Keep code, identifiers, paths and formatting unchanged. Output only the translation.`
  - ask：`ASK_HEAD + question`，`ASK_HEAD` = `The user highlighted the passage inside <selected> in the conversation UI and asks about it. {LOCATE}\n\nQuestion: `
  - custom：`The user highlighted the passage inside <selected> in the conversation UI. {LOCATE} Apply this instruction to it:\n\n{prompt}`
  - tool：`Explain the tool call named inside <selected>: what it did, why it was made at that point, and what its result shows; if it failed, explain the failure and what would fix it. Respond in {lang}.`
- `parseSideQuestion` 用一条正则匹配上面的结构（`action` 限定五个值；`<selected>\n` 到 `\n</selected>\n` 惰性匹配），不匹配返回 null；`ask` 时把指令开头的 `ASK_HEAD` 去掉得到 `ask`。

单测覆盖：五种动作的 compose → parse 往返；选区里含 `</selected>` 与引号仍能往返；超长选区按 3000 + 1000 截断；普通问题（`"这是什么"`）解析为 null。

**步骤 2.2 — mock 服务器补 btw**

`web/src/mock/server.ts` 的 `handleCmd`（`:3136` 起）：新增实例字段 `btwRecords: Map<string, RpcBtwHistoryRecord[]>` 与每线程一个运行中的计时器句柄。

- `case "btw"`：`question` 取 trim；该线程已有 `status === "running"` 的最新一轮 → `respond(..., undefined, "A /btw question is still running; cancel it first")`。带 `recordId` 时在该记录的 `followUps` 末尾追加一轮，否则新建记录（`id` 用自增 `mock-btw-{n}`，`leafId: null`）。先 `this.frame(threadId, { type: "btw_record", record })`（running、空回答），再 `respond(..., { record })`，然后把回答切成 4 段，每隔 `Number(localStorage.getItem("oryn.mock.btw-delay") ?? 120)` 毫秒发一个 `btw_delta`，最后发 `status: "complete"` 的 `btw_record`。回答文本：`parseSideQuestion(question)` 非空时 `模拟回答（{action}）：{selected 的前 40 个字}`，否则 `模拟回答：{question}`。
- `case "btw_cancel"`：有运行中的 → 清计时器、该轮置 `cancelled`、发 `btw_record`、应答 `{ cancelled: true }`；否则 `{ cancelled: false }`。
- `case "btw_inject"`：应答 `{ sent: false, reason: "mock" }`。
- `case "get_btw_history"`（`:3241`）：返回该线程的记录，按 `createdAt` 降序。
- 调试句柄 `window.__orynMock`（`:792`）上能读到 `btwRecords`（验收脚本要读发出去的 `question`）。

**步骤 2.3 — 运行态与 btw 对话框**

`web/src/components/thread/BtwTopic.tsx`：

- 把 `BtwTopicDetail` 里「各轮问答 + inject 处理 + `injectNote` + `ErrorText`」那段（`:107-120` 的 `inject` 与 `:139-157` 的 JSX，连同 `error` / `injectNote` 两个 state）抽成导出的 `BtwTurns({ threadId, record }: { threadId: string; record: RpcBtwHistoryRecord })`；`BtwTopicDetail` 改为渲染它。
- 把贴底跟随那段抽成导出的 `useFollowTail(scrollRef: RefObject<HTMLElement | null>, turnCount: number, latestLength: number): (e: UIEvent<HTMLElement>) => void`：`stick` / `seenTurns` 两个 ref 与 `useLayoutEffect`（`:93-105`）搬进 hook；返回值是原来写在 `section` 上的 `onScroll` 处理函数（`:126-130`，按 `STICK_SLACK` 更新 `stick`），调用方把它挂到滚动容器的 `onScroll` 上。`BtwTopicDetail` 改用它，行为不变。
- 新增导出 `SideQuestionHead({ question, variant }: { question: string; variant: "list" | "title" })`：`parseSideQuestion(question)` 为 null 时原样输出 `question`；否则 `list` 形态输出一行 `{label}：{selected 压成单行}`，`title` 形态输出 `label` 一行加一个 `blockquote`（类名用 `MessageView.tsx` 的 `USER_QUOTE`，外加 `max-h-40 overflow-y-auto`）显示 `selected`。`label`：`explain` / `translate` / `tool` → `t("sideAsk.action.<action>")`；`custom` → `name`；`ask` → `ask` 文本。
- `MessageView.tsx:85` 的 `USER_QUOTE` 现在是文件内常量，改成 `export const`（上一条要引它）。
- `BtwTopicDetail` 的标题（`:134`）用 `<SideQuestionHead variant="title">`，`section` 的 `aria-label`（`:124`）用 `label` 加选区前 40 个字；根 `section` 上 `useContextTarget({ askThreadId: threadId, selection: (sel) => ({ threadId, text: sel.text, before: sel.before(root, AROUND_MAX), after: sel.after(root, AROUND_MAX), source: { kind: "side" }, anchor: anchorOf(sel, 0, 0) }) })`。

`web/src/components/thread/BtwDialog.tsx`：新增 prop `initialRecordId?: string`，`picked` 初值为它（`:79`）；给了它时窄屏 `pageState` 初值为 `"detail"`（`:84`）；另加一个 effect：`initialRecordId` 变成非空的新值时 `setPicked(initialRecordId)` 并把 `pageState` 置 `"detail"`（对话框已经开着时从叠在上面的解释卡点「在旁路提问里打开」，也要切到那条话题）。列表项（`:508`）用 `<SideQuestionHead variant="list">`。`web/src/state/ui.ts:26` 的 `DialogState` 改为 `{ kind: "btw"; recordId?: string }`；`Dialogs.tsx:1832` 传 `initialRecordId={dialog.recordId}`。

新建 `web/src/state/side-ask.ts`：

```ts
export interface SideAskCard {
	threadId: string;
	anchor: Anchor;
	/** 非空 = 卡片正在等用户输入问题（「就此提问…」），还没发出 */
	compose: SelectionContext | null;
	/** 已发出的信封问题 */
	question: string | null;
	/** Core 应答后的记录 id；命令在途时为 null */
	recordId: string | null;
	error: unknown;
	/** 触发时已有别的模态对话框开着（例如从 btw 对话框里发起）：解释卡改用 Dialog 外壳叠在它上面 */
	overModal: boolean;
}
export const sideAskStore: StoreApi<{ card: SideAskCard | null }>;
export function locateEntry(threadId: string, entryId: string | undefined): { turnsAgo: number | null; inContext: boolean };
export function sideAskBusy(threadId: string): boolean;
/** 同一判断的响应式版本（选区胶囊用）；threadId 为 null 时恒为 false */
export function useSideAskBusy(threadId: string | null): boolean;
export function runSideAsk(action: SideAction, sel: SelectionContext): Promise<void>;
export function openSideAskCompose(sel: SelectionContext): void;
export function followUpSideAsk(text: string): Promise<void>;
export function closeSideAsk(opts?: { keepRunning?: boolean }): void;
```

- `locateEntry`：`peekThreadStore(threadId)?.getState()` 为空 → `{ turnsAgo: null, inContext: true }`；`entryId` 为空（在途消息）→ `{ turnsAgo: 0, inContext: true }`。否则 `path = activeBranch(entries, leafEntryId).flat()`（`web/src/state/transcript.ts:155`），找到该条目的下标 `i`（找不到 → `{ null, true }`），`turnsAgo` = `path.slice(i + 1)` 里 role 为 `user` 的 message 条目数（role 的取法与 `transcript.ts` 里 `messageItem` 相同）；`ctx = contextPath(entries, leafEntryId)`（`web/src/lib/context-segments.ts:53`），`inContext = ctx === null || ctx.some((e) => e.entryId === entryId)`。
- 「运行中的 btw」= 线程 store 的 `btw` 里最新一轮（`r.followUps?.at(-1) ?? r`）`status === "running"` 的记录。这个取最新一轮的小函数在本文件里自己写一份，**不要**从 `BtwTopic.tsx` 引 `latestTurn`：`BtwTopic` → `registry` → `selection-groups` → `state/side-ask` 已是一条依赖链，反向再引就成环。`sideAskBusy` = 存在这样的记录且 `parseSideQuestion(r.question) === null`。
- `overForeignModal(): boolean`（文件内）= `modalHosts().some((h) => h.querySelector("[data-side-ask]") === null)`——模态栈里有不是解释卡自己的对话框。`web/src/lib/modal.ts` 为此加导出 `modalHosts(): readonly HTMLElement[]`（直接返回 `stack`）。
- `runSideAsk`（前置：`sel.threadId` 非空）：
  1. `overModal = overForeignModal()`（要在关旧卡之前取）；已有卡片 → `closeSideAsk()`。
  2. 有运行中的记录：是信封问题 → `await realtime.cmd(threadId, { type: "btw_cancel", recordId: r.id }).catch(() => undefined)`；不是 → `showToast({ text: i18n.t("sideAsk.busy"), tone: "err" })` 后返回。
  3. `question = composeSideQuestion(action, sel, i18n.language)`；置卡片 `{ threadId, anchor: sel.anchor, compose: null, question, recordId: null, error: null, overModal }`；模块级序号 `seq` 自增并记下本次的值。
  4. `await realtime.cmd(threadId, { type: "btw", question })`。成功：序号仍是本次 → 写入 `recordId`；已不是（卡片被关或被换）→ `btw_cancel` 这条记录。失败：错误消息含 `cancel it first` 时等 150ms 重试一次；仍失败且序号仍是本次 → 写入 `error`。
- `openSideAskCompose(sel)`：先取 `overModal = overForeignModal()`，`closeSideAsk()`，再置卡片 `{ threadId, anchor, compose: sel, question: null, recordId: null, error: null, overModal }`。
- `followUpSideAsk(text)`：`realtime.cmd(threadId, { type: "btw", question: text.trim(), recordId })`，失败写 `error`。
- `closeSideAsk`：`seq` 自增（让在途应答走取消分支）；非 `keepRunning` 且卡片有 `recordId`、该记录最新一轮在跑 → `btw_cancel`（吞掉错误）；卡片置 null。

`web/src/pages/ThreadPage.tsx:262-277` 那个 effect：`shownThread !== threadId` 分支里 `ui.close()` 之后加 `closeSideAsk()`；`queueMicrotask` 里 `shownCount === 0` 时也调一次。

**步骤 2.4 — 解释卡**

新建 `web/src/components/thread/SideAskCard.tsx`，导出 `SideAskHost({ threadId }: { threadId: string })`；挂在 `ThreadPage.tsx:691` 的 `<ThreadDialogs threadId={threadId} />` 旁边。`card?.threadId !== threadId` 时渲染 null。`record = useThread(threadId, (s) => (card.recordId ? s.btw[card.recordId] : undefined))`。

- 内容（两种外壳共用）：
  - 页头：动作标签（`compose` 态固定为 `t("sideAsk.action.ask")`，否则取自 `parseSideQuestion(card.question)`，规则同 `SideQuestionHead`）、选区摘要（单行截断，`title` 给全文）、关闭按钮。
  - `compose` 态：一个 `autoFocus` 的 `TextInput`（占位 `sideAsk.askPlaceholder`）加发送按钮；提交非空文本 → `runSideAsk({ kind: "ask", question }, card.compose)`。
  - 其余：`record` 还没到且无错误 → `Spinner`；有 `record` → `<BtwTurns threadId record />`，滚动区用 `useFollowTail`（返回值挂到滚动容器的 `onScroll`）；`card.error` → `<ErrorText error={card.error} />`。
  - 页脚（非 compose 态且有 `record`）：追问表单（占位 `btw.followUpPlaceholder`，运行中禁用，提交调 `followUpSideAsk`）；运行中显示「停止」（`sideAsk.stop`，发 `btw_cancel`）；「复制回答」（`btw.copyAnswer`，`copyableAnswer(record)`）；「在旁路提问里打开」（`sideAsk.openInBtw`：`uiStore.getState().open({ kind: "btw", recordId })` 后 `closeSideAsk({ keepRunning: true })`）。
- 外壳：
  - `useBreakpoint() === "compact" || card.overModal`：包进 `Dialog`（`primitives.tsx:140`）。窄屏本来就是底部抽屉；宽屏上从别的对话框里发起时也走这条——浮动卡是 `z-40`，会被那个对话框宿主的 `fixed inset-0 z-50`（`primitives.tsx:208`）盖住、点不到。`title` 用动作标签，`onClose={() => closeSideAsk()}`。
  - 其余：`createPortal` 到 body 的 `<div role="dialog" aria-label={label} data-testid="side-ask-card" data-quote-affordance="" className="glass pop-in fixed z-40 flex max-h-[min(60dvh,32rem)] w-[min(26rem,calc(100vw-2rem))] flex-col rounded-lg shadow-lg">`。位置在打开时与 window `resize` 时算，不跟随滚动：`below = innerHeight - anchor.bottom >= 280 || innerHeight - anchor.bottom >= anchor.top`；`below` 时 `top = anchor.bottom + 8`，否则 `bottom = innerHeight - anchor.top + 8`；`left = clamp((anchor.left + anchor.right) / 2 - width / 2, 8, innerWidth - width - 8)`。焦点在卡片内时 Esc 关闭；点卡片外不关闭。
  - 两种外壳里内容的根元素都带 `data-side-ask=""`（`overForeignModal` 靠它认出解释卡自己的 `Dialog`）。
- 卡片根上 `useContextTarget`，spec 与 `BtwTopicDetail` 的相同（`askThreadId: threadId`、`source: { kind: "side" }`）。

**步骤 2.5 — 选区菜单组、输入框插入、命令面板预填**

新建 `web/src/state/selection-actions.ts`：`export interface SelectionAction { id: string; name: string; prompt: string }`、`export const selectionActionsStore: StoreApi<{ actions: SelectionAction[] }>`（初值空数组；步骤 7.2 接上数据）。

补全 `selection-groups.ts` 的 `selectionGroups(sel)`，返回两组：

- `key: "selection.ai"`（仅 `sel.threadId` 非空）：
  - `explain`（图标 `sparkle`，`contextMenu.explain`）→ `void runSideAsk({ kind: "explain" }, sel)`
  - `ask`（`question`，`contextMenu.ask`）→ `openSideAskCompose(sel)`
  - `translate`（`globe`，`contextMenu.translate`）→ `void runSideAsk({ kind: "translate" }, sel)`
  - 自定义动作：`selectionActionsStore.getState().actions` 的前 3 个直接列出（key `custom:{id}`，图标 `bolt`，label 为 `name`，→ `runSideAsk({ kind: "custom", name, prompt }, sel)`）；第 4 个起收进一项 `more`（`contextMenu.moreActions`，`children`）。
  - `sideAskBusy(sel.threadId)` 为真时这一组每项 `disabled: true`、`title: t("sideAsk.busy")`。
- `key: "selection"`：`quote`（`sel.quote` 存在时）；`insert`（`sel.insert` 存在且 `sel.threadId` 非空时，图标 `message`，`contextMenu.insert` → `insertIntoComposer(sel.threadId, sel.insert())`）；`copy`；`search`（仅 `paletteAvailable()` 为真时；图标 `search`，`contextMenu.search` → `openPalette(searchQueryOf(sel.text))`，`searchQueryOf` 取第一行、压空白、截 200 字符）。

`web/src/state/composer-drafts.ts`：`ComposerDraft` 加 `insert: ComposerInsert | null`（`export interface ComposerInsert { text: string }`），`EMPTY.insert = null`；导出 `insertIntoComposer(threadId: string, text: string): void` = `patchComposerDraft(threadId, { insert: { text } })` 后 `uiStore.getState().setThreadTab("agent")`。`web/src/components/composer/Composer.tsx` 在引用回复那个 effect（`:190-198`）旁加同构的 effect：`insert` 对象身份变化时，`prev = getComposerDraft(threadId).text`，`next = prev.trim() ? prev.replace(/\s+$/, "") + "\n\n" + insert.text : insert.text`，`patchComposerDraft(threadId, { text: next, insert: null })`，聚焦 textarea 并把光标放到末尾。

`web/src/state/composer-drafts.test.ts:20-28` 用 `toEqual` 比对完整的空草稿对象：在那个对象里补上 `insert: null`。

`web/src/components/palette/palette-state.ts`：`PaletteState` 加 `initialQuery: string`；`openPalette(query = "")` 写入它（已打开时照旧直接返回），`closePalette` 清空。`CommandPalette.tsx:192` 改为 `useState(() => paletteStore.getState().initialQuery)`。`web/src/components/layout/Sidebar.tsx:85` 的 `onClick={openPalette}` 改为 `onClick={() => openPalette()}`（否则点击事件对象会被当成查询串）。同文件再加模块级计数与两个导出：`claimPalette(): () => void`（计数加一，返回减一的函数）、`paletteAvailable(): boolean`（计数 > 0）；`PaletteHost.tsx` 里 `useEffect(claimPalette, [])`（要放在 `if (!usePaletteOpen()) return null` 之前）。分享页、登录页没有 `PaletteHost`，那里的菜单因此没有「搜索」。搜索是命令面板现有的全局内容搜索，不限定当前线程（后端没有线程过滤参数）。

**步骤 2.6 — 接进转录**

- `web/src/pages/ThreadPage.tsx:443` 的根 `div`：`useContextTarget({ askThreadId: coordinator ? null : threadId, selection: (sel, req) => ({ threadId: req.askThreadId, text: sel.text, before: "", after: "", source: { kind: "other" }, anchor: anchorOf(sel, req.x, req.y) }) })`。协调对话（Space 聊天、助手对话）服务端拒绝 btw（`web/src/lib/slash.ts:495-497`），所以是 null。分享页与子 agent 对话框不登记，自然没有 AI 动作。
- `web/src/components/transcript/MessageView.tsx` 加定位用属性（只加属性，不改结构）：
  - `UserMessage`（`:473`）外层 `div.group/msg`（`:508`，`className="group/msg flex flex-col items-end gap-1"`）加 `data-ask-scope="user"`。
  - `ThinkingBlock` 展开体（`:599`）加 `data-ask-scope="thinking"`。
  - `AssistantMessage` 的块外层 div（`:905-913`）：`toolCall` 块加 `data-tool-name={b.name}`。
- 新建 `web/src/components/transcript/selection-context.ts`，导出 `transcriptSelection(sel: DomSelection, o: { storeThreadId: string; askThreadId: string | null; quote?: (text: string) => void; fallback: Anchor }): SelectionContext`：
  - `scopeOf(el)` = `el.closest("[data-quote-scope], [data-ask-scope]")`；`start = scopeOf(sel.startEl)`、`end = scopeOf(sel.endEl)`、`spans = start !== end`。
  - `row = sel.startEl.closest("[data-index]")`；`entryId = row?.getAttribute("data-entry-id") ?? undefined`；`role = row?.getAttribute("data-role")`；`live = row?.hasAttribute("data-live") ?? false`。
  - 来源：`tool = sel.startEl.closest("[data-tool-name]")` 存在 → `{ kind: "tool", toolName }`；否则在 `[data-ask-scope="thinking"]` 里 → message / assistant / thinking；否则 `role === "user"` → message / user / text；`role === "assistant"` → message / assistant / text；其余 → `{ kind: "other" }`。前两类带上 `locateEntry(o.storeThreadId, entryId)` 的结果与 `live`、`spans`。
  - `before = sel.before(start ?? sel.root, AROUND_MAX)`、`after = sel.after(end ?? sel.root, AROUND_MAX)`。
  - `quote`：`o.quote` 存在、`!spans`、且 `sel.startEl.closest("[data-quote-scope]")` 与 `sel.endEl.closest("[data-quote-scope]")` 是同一个非空元素时给出 `() => o.quote(normalizeQuote(sel.text))`（与现在「引用回复」的可用条件一致）。
  - `anchor = sel.rects.length ? anchorOf(sel, 0, 0) : o.fallback`；`threadId = o.askThreadId`。
- `web/src/components/transcript/Transcript.tsx`：滚动容器（`:1051`，与 `scrollRef` 合并 ref）上 `useContextTarget({ selection: (sel, req) => transcriptSelection(sel, { storeThreadId: threadId, askThreadId: req.askThreadId, quote: readOnly ? undefined : (text) => patchComposerDraft(threadId, { quote: { text } }), fallback: anchorOf(null, req.x, req.y) }) })`。
- 选区胶囊（`quote-selection.tsx`）：
  - `useQuoteSelection` 新增入参 `describe: (sel: DomSelection) => SelectionContext | null`，返回值新增 `explain(anchor: Anchor): void`；`SelectionQuote` 新增 `quotable: boolean` 与 `ask: SelectionContext | null`。
  - `readSelection` 改为：`dom = readDomSelection()` 为空 → none；选区起点不在 `root` 里 → other；`quotable` = 起止在同一个 `[data-quote-scope]` 里（原判据）；`ask = describe(dom)`，`ask?.threadId` 为空时置 null；既不 `quotable` 也没有 `ask` → other；文字取 `quotable ? selectionText(range, scope, () => sel.toString()) : dom.text`（`sel` / `range` 照原样从 `document.getSelection()` 取）。位置计算（`:111-135`）不变。
  - `take()` 只在 `quotable` 时有效。`explain(anchor)`：取 `captured.current?.ask`，为空返回；`dismissed.current = sig`、`setQuote(null)`；`void runSideAsk({ kind: "explain" }, { ...ask, anchor })`。
  - `QuoteChip` 改成与 `QuoteBar` 同构的胶囊容器（`div[data-quote-affordance]`）：`quotable` 时有「引用回复」按钮，`ask` 非空时有「解释」按钮（图标 `sparkle`，`contextMenu.explain`，`onClick={(e) => onExplain(rectOf(e.currentTarget))}`，`rectOf(el)` 取 `getBoundingClientRect()` 的 `{ left, top, right, bottom }`），两个都有时中间放 `QuoteBar` 里那种竖线。`QuoteBar` 新增 props `quotable: boolean`、`canExplain: boolean`、`onExplain(anchor: Anchor): void`：「引用回复」按钮只在 `quotable` 时渲染（`take()` 只对可引用的选区生效，不可引用时留着按钮就是点了没反应），「解释」按钮只在 `canExplain` 时渲染，关闭按钮照旧。`useSideAskBusy(ask?.threadId ?? null)` 为真时「解释」按钮 `disabled` 并带 `title`。
  - `onPointerUp`（`:239-245`）改成：`pointerDown` 为假 → 返回；先 `pointerDown = false`；再判 `fromAffordance(e)` → 返回；否则照旧记 `lastInput`、安排 `evaluate`。原因：右键菜单正好开在指针下（Linux 上 `contextmenu` 在按下时就触发），松开时 `pointerup` 落在带 `data-quote-affordance` 的菜单上；按现在的写法 `pointerDown` 会一直是 true，之后键盘扩选不再重新捕获。
  - `Transcript.tsx:751` 传 `describe: (dom) => transcriptSelection(dom, { storeThreadId: threadId, askThreadId: resolveAskThread(scrollRef.current), fallback: anchorOf(dom, 0, 0) })`（不传 `quote`，引用仍走原来的 `onQuote`）；`:1084-1095` 把 `selection.explain` 接给两个胶囊。
  - `QuoteReply.test.tsx` 现有用例保持通过（「引用回复」按钮的名字与行为不变）。

### 阶段 3 — 转录里的对象

**步骤 3.1 — 消息**

`MessageView.tsx`：把 `MessageMenu` 里构造菜单项的部分（`:292-315`）抽成文件内函数 `messageMenuItems(o: { entryId?: string; copyText: () => string; copyLabel: string; quoteText?: () => string; ctx: TranscriptContextValue; t: TFunction }): MenuItem[]`，`MessageMenu` 改用它（`itemCount` 用返回数组的长度）。项：

- `copy`（文案 `o.copyLabel`，图标 `copy`）→ 复制 `copyText()`。行内「⋯」菜单（`MessageMenu`）传 `t("common.copy")`——`Transcript.test.tsx:299` 按名字「复制」点它，回合末尾那个复制的是整回合、也不该叫「复制消息」；右键菜单传 `t("contextMenu.copyMessage")`。
- `quote`（`contextMenu.quoteMessage`，图标 `quote`）：`quoteText` 给出且 `entryId !== undefined && !ctx.readOnly` 时 → `patchComposerDraft(ctx.threadId, { quote: { text: normalizeQuote(quoteText()) } })`。
- `fork`、`label`：与现在相同（`entryId !== undefined && !ctx.readOnly`）。

`UserMessage` 外层 div 与 `AssistantMessage` 外层 div 各 `useContextTarget({ items: () => [{ key: "message", items: messageMenuItems(...) }] })`，`copyText` / `entryId` 与各自 `MessageMenu` 用的一致；`quoteText` 只有助手消息的右键菜单给（`() => textOfContent(content)`），`MessageMenu` 不传，行内菜单的项数与现在相同。回合末尾 `TurnMeta`（`:735`）的菜单（`:776`）就是一个 `MessageMenu`，随它一起改用 `messageMenuItems`。

**步骤 3.2 — Markdown：链接、图片、代码块、行内代码**

新建 `web/src/lib/context-menu/image-actions.ts`，导出 `imageGroup(src: string, askThreadId: string | null): MenuGroup`（key `image`）：

- `copyImage`（`contextMenu.copyImage`，图标 `image`）：仅当 `typeof ClipboardItem !== "undefined" && navigator.clipboard?.write` 时出现。`fetch(src, { credentials: "same-origin" })` → blob；类型不是 `image/png` 时经 `createImageBitmap` + canvas 转成 PNG；`navigator.clipboard.write([new ClipboardItem({ "image/png": png })])`；失败走「复制失败」toast。
- `download`（`contextMenu.downloadImage`，图标 `download`）：临时 `<a href={src} download="image-{Date.now()}.{ext}">` 点击（做法同 `FileBrowser.tsx:284-292`），`ext` 取自 blob 类型或 `data:` 头，缺省 `png`。
- `attach`（`contextMenu.insert`，图标 `attach`）：仅 `askThreadId` 非空时出现；`fetch` 成 `File` 后 `enqueueComposerFiles(askThreadId, [file])` 并 `uiStore.getState().setThreadTab("agent")`。

`composer-drafts.ts` 加一个独立的小 store 与三个导出：`enqueueComposerFiles(threadId: string, files: File[]): void`、`useComposerPendingFiles(threadId: string): File[]`、`takeComposerFiles(threadId: string): File[]`（取出并清空）；`resetComposerDrafts` 一并清空。`Composer.tsx` 加 effect：待加入列表非空时 `takeComposerFiles`，`canAttach` 为真就 `void att.addFiles(files)`（`:116-117`）。

`web/src/components/markdown/Markdown.tsx`：

- 根 `div.md`（`:357`）上 `useContextTarget`，`items(req)` 依次判断（都要求命中元素在这个根里）：
  - `req.target.closest("a[href]")` → 组 `link`：`open`（`contextMenu.openLink`，图标 `external`，`window.open(href, "_blank", "noopener")`）、`copyLink`（`contextMenu.copyLink`，图标 `link`，复制 `href`）。
  - `req.target.closest("img")` → `imageGroup(img.currentSrc || img.src, req.askThreadId)`。
  - `req.target.closest("code.md-inline-code")` → 组 `code`：`copy`（`common.copy`，复制其 `textContent`）。
- `CodeBlock`（`:247-271`）的 `div.md-code` 上 `useContextTarget`，`items(req)` 返回组 `code`：`copy`（`contextMenu.copyCode`，复制 `code`）；`explain`（`contextMenu.explainCode`，图标 `sparkle`，仅 `req.askThreadId` 非空；`sideAskBusy` 时置灰）→ `runSideAsk({ kind: "explain" }, { threadId: req.askThreadId, text: code, before: "", after: "", source: <见下>, anchor: <div.md-code 的矩形> })`。`source`：`row = el.closest("[data-index]")` 有 `data-role` 时为 `{ kind: "message", role, part: "text", ...locateEntry(req.askThreadId, row 的 data-entry-id), live: row.hasAttribute("data-live"), spans: false }`，否则 `{ kind: "other" }`。`.md-code-bar` 加 `data-ctx-touch=""`。
- `MessageView.tsx` 的 `Images`（`:129-143`）：每个图片按钮加 `data-ctx-touch=""`；`Images` 最外层 div（`:115`）上 `useContextTarget({ items: (req) => { const img = req.target.closest("img"); return img ? [imageGroup(img.currentSrc || img.src, req.askThreadId)] : null; } })`。

**步骤 3.3 — 工具卡**

- `web/src/tool-render/util.ts` 新增 `toolSubject(name: string, args: unknown): { kind: "command" | "path"; text: string } | null`：`args` 不是对象 → null；`name === "bash"` 且 `str(args.command)` 非空 → command；`str(args.path) ?? str(args.file_path)` 非空 → path（`read` 的路径去掉 `:` 之后的选择器部分，规则照 `tools/read.tsx:48-72` 的 `readArgs`）；其余 null。
- `web/src/tool-render/ToolView.tsx`：`ToolViewProps` 加 `rootRef?: (el: HTMLDivElement | null) => void`，接到根 `div.tv-card`（`:226`）；`button.tv-head`（`:229`）加 `data-ctx-touch=""`。
- `MessageView.tsx` 的 `ToolCard`（`:621-677`）：`const ctxRef = useContextTarget({ items })`，传 `rootRef={ctxRef}`。`items(req)` 返回组 `tool`（`subject = toolSubject(name, args)`，`output = resultTextOf(result)`）：
  - `copySubject`：`subject` 非空时，文案按 kind 取 `contextMenu.copyCommand` / `contextMenu.copyPath`。
  - `copyOutput`（`contextMenu.copyOutput`）：`output` 非空时。
  - `openFile`（`contextMenu.openInFiles`，图标 `folderOpen`）：`subject?.kind === "path"`、`req.askThreadId` 非空、且路径是绝对路径或能用线程的 `cwd` 拼成绝对路径（`useThread(ctx.threadId, (s) => s.thread?.cwd)`，`joinPath` 来自 `web/src/lib/file-source.ts:77`）时 → `uiStore.getState().openFile(ctx.threadId, abs)`（步骤 5.1 加这个方法；本步骤先不放这一项，5.1 里补上）。
  - `explain`（`contextMenu.explainTool`，图标 `sparkle`）：`req.askThreadId` 非空时；`sideAskBusy` 时置灰 → `runSideAsk({ kind: "tool" }, { threadId: req.askThreadId, text: subject ? name + ": " + subject.text : name + ": " + JSON.stringify(args ?? {}).slice(0, 300), before: "", after: "", source: { kind: "tool", toolName: name, ...locateEntry(ctx.threadId, <所在行的 data-entry-id>), spans: false }, anchor: <卡片根的矩形> })`。

### 阶段 4 — 侧栏线程行与收件箱行

**步骤 4.1 — 线程行菜单与对话框宿主**

导出现有私有符号：`web/src/components/thread/Dialogs.tsx` 的 `RenameDialog`（`:1414`）、`LabelsDialog`（`:1453`）、`ForkDialog`（`:360`）；`web/src/components/thread/ArchiveDialog.tsx` 的 `ArchiveDialog`（`:36`）；`web/src/components/thread/ThreadHeader.tsx` 的 `snoozeAt`（`:71`）。`useThreadActions` 的菜单项不动（它受 iOS 原生页头序列化约束，`ThreadHeader.test.tsx:208-243`）。

`RenameDialog` 新增可选 prop `submit?: (title: string) => Promise<unknown>`，缺省 `(title) => renameThread(threadId, title)`（现有行为，`:1428-1433`）；表单提交改调它。

新建 `web/src/components/thread/thread-row-menu.tsx`：

```ts
export function threadRowGroups(thread: Thread, o: { to: string; navigate: NavigateFunction; t: TFunction }): MenuGroup[];
export function ThreadRowDialogs(): ReactNode;
```

- 文件内 store：`{ dialog: { kind: "rename" | "labels" | "fork"; threadId: string } | { kind: "archive"; thread: Thread } | null }`。打开 rename / labels / fork 前先 `getThreadStore(thread.id).getState().apply([{ type: "thread", thread }])`（三个对话框的初值读线程 store，非当前线程的 store 是空的；`patchThread` 也是这样填它的，`web/src/state/actions.ts:355-370`）。
- 所有直接动作包一层：失败时 `showToast({ text: describeError(e, t), tone: "err" })`。
- `threadRowGroups` 的组与项（状态一律读传入的 `thread` 对象，不读线程 store）：
  - `thread.open`：`open`（`contextMenu.open`，图标 `message`，`navigate(o.to)`）；`openNewTab`（`contextMenu.openNewTab`，图标 `external`，仅 `host.kind === "web"`，`window.open(o.to, "_blank", "noopener")`——两种壳里 `window.open` 都会跳去系统浏览器，所以不给）。
  - `thread.state`：`unread`（`thread.unread` 为真时 `inbox.markRead`，否则 `contextMenu.markUnread`；`patchThread(id, { unread: !thread.unread })`）；`pin`（`thread.unpin` / `thread.pin`，`patchThread(id, { pinned: !thread.pinned })`）；`snooze`（`thread.snooze`，`children`：`1h` / `3h` / `tomorrow` / `week` → `patchThread(id, { snoozedUntil: snoozeAt(k) })`，`thread.snoozedUntil` 大于当前时间时多一项 `clear` → `snoozedUntil: 0`，文案同 `ThreadHeader.tsx` 现有的 `thread.snoozeFor.*` / `thread.snoozeClear`）；`archive`（已归档：`thread.unarchive`，`patchThread(id, { archived: false })`；未归档：`thread.archive`，`thread.worktree` 为空时直接 `patchThread(id, { archived: true })`，否则开 `{ kind: "archive", thread }`）。
  - `thread.edit`：`rename`、`labels`、`fork`（各开对应对话框）；`color`（`thread.color`，`children` 为 `THREAD_COLORS` 各色加 `none`，label 用 `t("color.<c>")` 纯文字，`patchThread(id, { color })`，`none` 传空串）；`link`（`thread.copyLink`，复制 `location.origin + "/t/" + id`）。
  - `thread.danger`：`delete`（`thread.delete`，`danger: true`）：`window.confirm(t("thread.deleteConfirm"))` → `deleteThread(id)`，之后若 `location.pathname === "/t/" + id` 则 `navigate("/")`。
- `ThreadRowDialogs`：读 store，按 `kind` 渲染。`RenameDialog` / `LabelsDialog` / `ForkDialog` 用 `lazy(() => import("./Dialogs").then((m) => ({ default: m.X })))` 加 `<Suspense fallback={null}>`（别把 1800 行的 `Dialogs.tsx` 拉进主包）；`archive` 渲染 `<ArchiveDialog key={thread.id} pending={{ thread, shipped: false }} onClose={close} />`。`onClose` 都是把 store 置 null。`RenameDialog` 在这里传 `submit={(title) => patchThread(threadId, { title })}`：`renameThread` 在 PATCH 之后还会 `await realtime.cmd(set_session_name)`（`web/src/state/actions.ts:422-428`），对 orb 已冻结的非当前线程，这条命令要等 orb 被唤醒才应答，对话框会一直不关；而 PATCH 改标题时控制面自己就会把 `set_session_name` 排给 agent（`internal/api/threads.go:235`、`internal/threads/agent.go:1761-1769`），前端不用再发一遍。
- 在 `web/src/components/layout/AppShell.tsx` 两个渲染分支里 `<PaletteHost />` 后面各加一个 `<ThreadRowDialogs />`（`:318`、`:377`）。

**步骤 4.2 — 接进两种行**

- `web/src/components/layout/ThreadsPane.tsx` 的 `ThreadRow`（`:~215-336`）：`const navigate = useNavigate(); const ctxRef = useContextTarget({ items: () => threadRowGroups(thread, { to: "/t/" + thread.id, navigate, t }) })`，把 `ref={ctxRef}` 与 `data-ctx-touch=""` 加在 `NavLink` 上。不往链接里加任何带文字的节点，也不包新元素（`ThreadsPane.tsx:9` 的 textContent 契约与 `ThreadsPane.test.tsx:62-95` 的断言）。
- `web/src/pages/InboxPage.tsx` 的 `InboxRow`（`:604-790`）：同样加在 `<Link data-inbox-row>` 上，`to` 同为 `/t/{id}`。离场中的行外层是 `inert`，事件到不了，不用另判。
- 助手分区的 `ConversationRow`（`AssistantPane.tsx:41`）不接：它的归档走另一套接口，且协调对话没有改名 / fork 语义。

### 阶段 5 — 文件、编辑器、改动

**步骤 5.1 — 从外部打开文件**

- `web/src/state/ui.ts`：`UiState` 加 `fileSel: Record<string, { path: string; n: number }>`（不持久化）、`openFile(threadId: string, path: string): void`、`clearFileSel(threadId: string): void`。`openFile` 仿 `openArtifact`（`:367-370`）：写 `fileSel[threadId] = { path, n: (旧 n ?? 0) + 1 }`，再 `setPanelTab("files")`、`setPanel(true)`。
- `web/src/components/files/FilesPanel.tsx:145`：读 `useUi((s) => s.fileSel[threadId])`，作为 `openPath` 传给 `FileBrowser`，并传 `onOpened={() => uiStore.getState().clearFileSel(threadId)}`。
- `web/src/components/files/FileBrowser.tsx`：新增两个 prop `openPath?: { path: string; n: number }` 与 `onOpened?(): void`。effect（依赖 `openPath?.n`、`allRoots` 与 `rootPath`；写在换根时重置展开状态的那个 effect `:253-256` 之后）：`openPath` 为空、`allRoots` 为空或这个 `n` 已处理过（ref 记着）→ 返回。先定根：`owner = allRoots.find((r) => isWithin(path, r.path))`（`Root` 见 `:62-67`，`allRoots` 见 `:189-192`）。找不到 → 记下 `n`、调 `onOpened?.()` 后返回（路径不在任何可浏览的根下，不动当前选择）。`owner.key !== root.key` → `persist({ root: owner.key, selected: path })` 后返回，**不**记这个 `n`——换根后 `rootPath` 变化，effect 重跑时走下面的分支。`owner` 就是当前根 → 记下 `n`，自 `rootPath` 起到 `parentPath(path)` 逐级 `expandTo(p)` + `await loadDir(p)`（写法照 `:382-391` 的 `chain` 循环），然后 `select(path)`（`:279`），最后调 `onOpened?.()`（清掉待打开项，免得面板重新挂载时又把用户后来选的文件换回去）。
- 回到步骤 3.3，把工具卡的 `openFile` 项补上。

**步骤 5.2 — 编辑器选区与文件查看器**

`web/src/components/editor/CodeEditor.tsx`：宿主 div（`data-testid="code-editor"`，与 `host` ref 合并）上 `useContextTarget`：

- `selection: () => null`（编辑器里的选区由下面的 `items` 自己处理，不让外层按 DOM 选区再出一份）。
- `items(req)`：`v = view.current`，`main = v?.state.selection.main`；没有或 `main.empty` → 返回 null（没有选区时放行原生菜单，保住粘贴）。否则 `text = v.state.sliceDoc(main.from, main.to)`，`startLine` / `endLine` 用 `v.state.doc.lineAt(pos).number`，`lang = languageFromPath(path) ?? ""`（`web/src/tool-render/util.ts:120`），`a = v.coordsAtPos(main.from)`、`b = v.coordsAtPos(main.to)`。返回：
  - `...selectionGroups({ threadId: req.askThreadId, text, before: v.state.sliceDoc(Math.max(0, main.from - AROUND_MAX), main.from), after: v.state.sliceDoc(main.to, main.to + AROUND_MAX), source: { kind: "file", path, startLine, endLine }, anchor: a && b ? { left: a.left, top: a.top, right: b.right, bottom: b.bottom } : anchorOf(null, req.x, req.y), insert: () => "`" + path + ":" + startLine + "-" + endLine + "`\n```" + lang + "\n" + text + "\n```" })`
  - 组 `edit`：`cut`（`contextMenu.cut`，仅 `!readOnly`：复制后 `v.dispatch(v.state.replaceSelection(""))`）；`paste`（`contextMenu.paste`，图标 `clipboard`，仅 `!readOnly && navigator.clipboard?.readText`：读到文本后 `v.dispatch(v.state.replaceSelection(text))`，失败静默）；`copyLocation`（`contextMenu.copyLocation`，复制 `path:startLine-endLine`）。「复制」已在 `selection` 组里。
- 这些都从现有的 `view` ref 与 props 现取，不改挂载 effect 的扩展数组。

`web/src/components/files/FileViewer.tsx`：最外层容器上 `useContextTarget({ selection: (sel, req) => ({ threadId: req.askThreadId, text: sel.text, before: sel.before(rootEl, AROUND_MAX), after: sel.after(rootEl, AROUND_MAX), source: { kind: "file", path }, anchor: anchorOf(sel, req.x, req.y) }) })`（Markdown 预览等非编辑器视图里的选区用它；编辑器里被内层的 `selection: () => null` 挡住）。

**步骤 5.3 — 改动列表与 diff**

- `web/src/components/changes/ChangesPanel.tsx`（组件是 `:269` 的默认导出）：文件行 `<li>`（`:634` 起）加 `data-change-path={f.path}` 与 `data-ctx-touch=""`（只加属性；按钮里三个直接子 span 被验收脚本依赖，不动）。`const ctxRef = useContextTarget({ items })` 写在组件顶部、两个提前返回（`:438-441`）之前，`ref={ctxRef}` 挂在文件列表的 `<ul aria-label={t("changes.files")}>`（`:624`）上。`items(req)` 里现取数据，不引用提前返回之后才声明的 `files` / `stageable`：`li = req.target.closest("li[data-change-path]")`，没有命中返回 null；`f = (status?.repo ? status.files : []).find((x) => x.path === li.getAttribute("data-change-path"))`，找不到返回 null；返回组 `change`：
  - `openFile`（`contextMenu.openInFiles`，图标 `folderOpen`）：`f.status !== "deleted"` 且 `status?.root` 存在 → `uiStore.getState().openFile(threadId, joinPath(status.root, f.path))`。
  - `copyPath`（`contextMenu.copyPath`）：复制 `f.path`。
  - `stage`：仅 `scope.scope !== "commit"`（与 `:443` 的 `stageable` 同一判断）；`f.staged === "full"` 时文案 `contextMenu.unstage`，否则 `contextMenu.stage` → `void stage([f], f.staged !== "full")`（`:420`）。
  - 不做「丢弃改动」：orbd 只有 diff / stage / unstage 接口。
- `web/src/components/changes/FileDiff.tsx`：在 `FileDiff`（`:28`）的滚动 div（`:120`，`ref={scrollRef}`）上登记，与 `scrollRef` 合并 ref：`useContextTarget({ selection: (sel, req) => ({ threadId: req.askThreadId, text: sel.text, before: "", after: "", source: { kind: "diff", path: file.path }, anchor: anchorOf(sel, req.x, req.y) }) })`。不要登记在 `VirtualPatch`（`:169`）里的 `div.tv-diff` 上——那里拿不到 `file`。

### 阶段 6 — 终端

**步骤 6.1**

- `web/src/state/terminals.ts` 的 `TermLike`（`:26-49`）加四个可选方法：`getSelection?(): string`、`selectAll?(): void`、`clear?(): void`、`mouseTracking?(): boolean`。`web/src/components/terminal/xterm.ts` 的 `XtermTerm` 实现：`this.term.getSelection()`、`this.term.selectAll()`、`this.term.clear()`、`this.term.modes.mouseTrackingMode !== "none"`。测试里的手写替身不用改（都是可选方法，调用处一律 `?.`）。
- 把 `TerminalKeyBar.tsx:~124-130` 的粘贴逻辑抽到新文件 `web/src/components/terminal/paste.ts`：`export async function pasteIntoTerminal(threadId: string, terminalId: string): Promise<void>`，`TerminalKeyBar` 改用它。
- `web/src/components/terminal/TerminalView.tsx`：容器 div（与现有 `ref` 合并）上 `useContextTarget`：
  - `selection: () => null`。
  - `items(req)`：`term = terminalHub.session(threadId, terminalId)?.term`，为空返回 null；`term.mouseTracking?.()` 为真 → `"suppress"`（鼠标交给终端里的程序）。`text = term.getSelection?.() ?? ""`。返回：`text` 非空时 `...selectionGroups({ threadId: req.askThreadId, text, before: "", after: "", source: { kind: "terminal" }, anchor: anchorOf(null, req.x, req.y), insert: () => "```\n" + text + "\n```" })`；再加组 `terminal`：`paste`（`contextMenu.paste`，图标 `clipboard`；`host.platform === "android"` 或没有 `navigator.clipboard?.readText` 时不出现；该终端 `status !== "live"` 时 `disabled`；→ `pasteIntoTerminal`）、`selectAll`（`contextMenu.selectAll`）、`clear`（`contextMenu.clearTerminal`）。「复制」已在 `selection` 组里。
- 终端不打 `data-ctx-touch`（触屏上长按留给 xterm 自己）。

### 阶段 7 — 自定义快捷动作

**步骤 7.1 — 后端**

- `schema/openapi.yaml`：仿 `/api/v1/settings/composer`（`:2284-2316`）新增 `/api/v1/settings/selection-actions`，`get` 的 `operationId: getSelectionActions`、`put` 的 `operationId: putSelectionActions`（同样的 `security` 与 409 说明）。schema 放在 `ComposerPrefs`（`:8178`）后面：

  ```yaml
  SelectionAction:
    type: object
    required: [id, name, prompt]
    properties:
      id: { type: string, pattern: '^[a-z0-9]{8}$' }
      name: { type: string, minLength: 1, maxLength: 24 }
      prompt: { type: string, minLength: 1, maxLength: 2000 }
  SelectionActions:
    type: object
    required: [actions]
    properties:
      actions:
        type: array
        maxItems: 12
        items: { $ref: '#/components/schemas/SelectionAction' }
  ```
- 迁移 `internal/store/migrations/0043_selection_actions.sql`（goose 格式同 `0024_agent_avatars.sql`）：Up `ALTER TABLE owner ADD COLUMN selection_actions TEXT NOT NULL DEFAULT '[]';`，Down `ALTER TABLE owner DROP COLUMN selection_actions;`。
- `internal/store/queries/avatar_identity.sql` 末尾加 `GetOwnerSelectionActions :one`（`SELECT selection_actions FROM owner WHERE id = 1;`）与 `SetOwnerSelectionActions :execrows`（`UPDATE owner SET selection_actions = @selection_actions WHERE id = 1;`）。
- `make gen`；`make lint-spec`。`make gen` 会把整份 `schema/openapi.yaml` 重新生成一遍，工作区里别的会话未提交的接口改动也会出现在生成物里：提交本步骤时，`internal/apigen/apigen.gen.go`、`internal/store/db/*.go`、`web/src/api/schema.d.ts` 只带内容匹配 `(?i)selection[-_]?actions?` 的改动块，`schema/openapi.yaml` 只带本步骤加的路径与两个 schema。
- 新建 `internal/api/selection_actions.go`，结构照 `internal/api/composer_prefs.go`：
  - `GetSelectionActions`：`sql.ErrNoRows` 或 JSON 解不出 → 返回 `{ "actions": [] }`；否则返回存的列表。空列表要序列化成 `[]` 而不是 `null`（切片用 `make(..., 0)` 起）。
  - `PutSelectionActions`：`decodeJSON`；校验——至多 12 条、`id` 匹配 `^[a-z0-9]{8}$` 且互不重复、`name` / `prompt` 去首尾空白后非空、按 rune 计 `name` ≤ 24、`prompt` ≤ 2000——任一不满足 `writeErr(w, r, errInvalid("…"))`；把去过空白的列表 `json.Marshal` 后 `SetOwnerSelectionActions`，`savedOwner(w, r, n, err)`，回写规整后的列表。
- 新建 `internal/api/selection_actions_test.go`，照 `composer_prefs_test.go` 的搭法：初始 GET 为空列表；PUT 两条后 GET 读回（含去空白）；13 条、重复 id、空名字各回 400。

**步骤 7.2 — 前端数据层**

- 新建 `web/src/api/selection-actions-queries.ts`：`selectionActionsKey = ["settings", "selection-actions"] as const`；`useSelectionActions(enabled = true)`；`saveSelectionActions(qc: QueryClient, value: Schemas["SelectionActions"]): Promise<void>`——取消在途读取、PUT、再取消、`setQueryData`，与 `saveComposerPrefs`（`web/src/api/identity-queries.ts:253-264`）同构。
- `web/src/state/selection-actions.ts` 加 `useSelectionActionsSync(enabled: boolean): void`：`useSelectionActions(enabled)` 的 `data` 变化时写进 `selectionActionsStore`。在 `AppShell.tsx` 里与其他 hook 一起、在提前返回之前调用，`enabled` 取「`me` 查询已成功」。
- `web/src/mock/server.ts`：实例字段 `selectionActions: Schemas["SelectionActions"] = { actions: [] }`，在 `settings/composer` 分支（`:1619-1630`）后加 `settings/selection-actions` 分支（PUT 做与后端相同的条数与长度校验，失败回 400 `invalid_request`）。
- `AppShell.test.tsx` 若因多出一个未桩的请求而报错，就在它的 `QueryClient` 里预置 `selectionActionsKey` 的数据。

**步骤 7.3 — 设置分区**

- 分区 id `selection-actions`：加进 `SettingsSection`（`web/src/pages/SettingsPage.tsx:104-131`）、`SETTINGS`（`web/src/App.tsx:78-105`，放在 `"agent-composer"` 后面）、`SettingsPage` 渲染表（`:1264-1284`）、`MORE_ENTRIES`（`web/src/components/layout/more-entries.ts`，`{ key: "selection-actions", group: "agent", icon: "bolt", labelKey: "settings.selection-actions", to: "/settings/selection-actions" }`，紧跟 `agent-composer` 那一项）。
- 新建 `web/src/components/settings/SelectionActionsSettings.tsx`：一个 `Card`（标题 `selectionActions.title`，图标 `bolt`），说明文字 `selectionActions.hint`；列表每行显示名称与提示词首行，行尾有上移、下移、编辑、删除四个 `IconButton`；空列表显示 `EmptyState`（`selectionActions.empty`）；「添加动作」按钮在满 12 条时禁用并显示 `selectionActions.limit`。新增 / 编辑用 `Dialog`：`Field` 名称（`TextInput maxLength={24}`）与 `Field` 提示词（`TextArea maxLength={2000} mono={false}`，hint 为 `selectionActions.promptHint`）。`TextArea`（`web/src/components/ui/primitives.tsx:410-416`）为此加 prop `mono?: boolean`，缺省 true，为 false 时不拼 `font-mono`（现有调用不受影响）。新条目的 `id` 用 `crypto.getRandomValues` 生成 8 位 `[a-z0-9]`。每次增删改排序后整体 `saveSelectionActions`，错误用 `ErrorText` 显示；删除前 `window.confirm(t("selectionActions.deleteConfirm"))`。

### 阶段 8 — 验收脚本

**步骤 8.1** 新建 `web/scripts/m22-accept.ts`，结构照 `web/scripts/m13-accept.ts` 的 mock 部分（playwright-core 驱动 chromium，自起 `VITE_MOCK=1` 的 vite，端口 `5195`，vite `cacheDir` 用 `node_modules/.vite-m22`，页面初始化脚本设 `localStorage["oryn.debug"]="1"`，每个场景一个新的浏览器上下文，输出 `R<n> PASS|FAIL`，截图存 `.dev/m22/`）；在 `web/package.json` 的 scripts 里注册 `"accept:m22": "bun scripts/m22-accept.ts"`。端口 `5195` 动手时先在 `web/scripts/*.ts` 里搜一遍，被占就取下一个没人用的奇数端口。场景清单见 Verification。

### 文案

| key | zh-CN | en |
|---|---|---|
| `contextMenu.explain` | 解释 | Explain |
| `contextMenu.ask` | 就此提问… | Ask about this… |
| `contextMenu.translate` | 翻译 | Translate |
| `contextMenu.moreActions` | 更多动作 | More actions |
| `contextMenu.insert` | 引用到输入框 | Add to composer |
| `contextMenu.search` | 搜索 | Search |
| `contextMenu.copyFailed` | 复制失败 | Couldn't copy |
| `contextMenu.copyMessage` | 复制消息 | Copy message |
| `contextMenu.quoteMessage` | 引用整条 | Quote message |
| `contextMenu.openLink` | 打开链接 | Open link |
| `contextMenu.copyLink` | 复制链接地址 | Copy link address |
| `contextMenu.copyImage` | 复制图片 | Copy image |
| `contextMenu.downloadImage` | 下载图片 | Download image |
| `contextMenu.copyCode` | 复制代码 | Copy code |
| `contextMenu.explainCode` | 解释这段代码 | Explain this code |
| `contextMenu.copyCommand` | 复制命令 | Copy command |
| `contextMenu.copyPath` | 复制路径 | Copy path |
| `contextMenu.copyOutput` | 复制输出 | Copy output |
| `contextMenu.openInFiles` | 在文件面板打开 | Open in Files |
| `contextMenu.explainTool` | 解释这次调用 | Explain this call |
| `contextMenu.open` | 打开 | Open |
| `contextMenu.openNewTab` | 在新标签页打开 | Open in new tab |
| `contextMenu.markUnread` | 标为未读 | Mark as unread |
| `contextMenu.stage` | 暂存 | Stage |
| `contextMenu.unstage` | 取消暂存 | Unstage |
| `contextMenu.cut` | 剪切 | Cut |
| `contextMenu.paste` | 粘贴 | Paste |
| `contextMenu.selectAll` | 全选 | Select all |
| `contextMenu.clearTerminal` | 清屏 | Clear |
| `contextMenu.copyLocation` | 复制路径与行号 | Copy path and lines |
| `sideAsk.action.explain` | 解释 | Explain |
| `sideAsk.action.translate` | 翻译 | Translate |
| `sideAsk.action.ask` | 就此提问 | Ask about this |
| `sideAsk.action.tool` | 解释调用 | Explain call |
| `sideAsk.busy` | 旁路提问进行中 | A side question is running |
| `sideAsk.askPlaceholder` | 想问什么？ | What do you want to ask? |
| `sideAsk.stop` | 停止 | Stop |
| `sideAsk.openInBtw` | 在旁路提问里打开 | Open in side questions |
| `settings.selection-actions` | 选区快捷动作 | Selection actions |
| `selectionActions.title` | 选区快捷动作 | Selection actions |
| `selectionActions.hint` | 选中文字后出现在右键菜单里，把你写的提示词连同选中的内容一起交给模型。 | Shown in the context menu when text is selected; your prompt is sent to the model together with the selection. |
| `selectionActions.add` | 添加动作 | Add action |
| `selectionActions.name` | 名称 | Name |
| `selectionActions.prompt` | 提示词 | Prompt |
| `selectionActions.promptHint` | 写成对选中内容的指令，例如「找出这段话里的漏洞」。 | Write it as an instruction about the selection, e.g. "Find the flaws in this passage". |
| `selectionActions.empty` | 还没有自定义动作 | No custom actions yet |
| `selectionActions.limit` | 最多 12 个 | Up to 12 actions |
| `selectionActions.deleteConfirm` | 删除这个动作？ | Delete this action? |
| `selectionActions.moveUp` | 上移 | Move up |
| `selectionActions.moveDown` | 下移 | Move down |

其余文案复用现有 key：`common.copy`、`common.close`、`common.cancel`、`common.save`、`common.edit`、`common.delete`、`transcript.quoteReply`、`transcript.forkHere`、`transcript.label`、`inbox.markRead`、`thread.pin` / `unpin` / `snooze` / `snoozeFor.*` / `snoozeClear` / `archive` / `unarchive` / `rename` / `labels` / `color` / `copyLink` / `fork` / `delete` / `deleteConfirm`、`color.*`、`btw.followUpPlaceholder`、`btw.copyAnswer`，以及文件树各动作现有的文案。

## Critical files & anchors

- `web/src/components/transcript/quote-selection.tsx` — `readSelection`（`:94-137`）、捕获阶段 `pointerdown` 清快照（`:230-238`）、`[data-quote-affordance]` 豁免：浮动菜单、解释卡、胶囊按钮都靠这个属性不被当成「点了别处」。
- `web/src/components/ui/primitives.tsx` — `MenuItem`（`:251-270`）是全计划唯一的菜单项类型；`Menu` 的项渲染（`:338-391`）是新宿主要照抄的样式；`Dialog`（`:140-248`）在 window 上听 Esc，菜单必须在捕获阶段拦。
- `web/src/pages/ThreadPage.tsx` — 根 div（`:443`）登记 `askThreadId`；`:262-277` 的进出线程 effect 负责关解释卡；`:691` 旁挂 `SideAskHost`；`coordinator`（`:211`）决定有没有 AI 动作。
- `web/src/mock/server.ts` — `handleCmd`（`:3136`）、`frame`（`:2881`）、`respond`（`:3118`）：不补 btw，解释卡在 `dev:mock` 与验收脚本里都跑不起来。
- `web/src/components/layout/ThreadsPane.tsx` — 链接 `textContent` 契约（`:9`）：线程行只能加属性与 ref。
- `web/src/lib/modal.ts` — `applyInert`（`:11-22`）只在压栈 / 出栈时执行：浮动菜单在 `Dialog` 之后才挂到 body，不会被设成 inert；解释卡靠新增的 `modalHosts()` 判断要不要换成 `Dialog` 外壳。
- `web/src/components/transcript/Transcript.tsx:736-738` — 贴底跟随每个增量都改 `scrollTop`：菜单与长按都不能拿 `scroll` 当关闭 / 取消信号。

## Verification

每步：`cd web && bun run check && bun run test` 通过。步骤 7.1：`make gen`、`make lint-spec` 通过；`go test ./internal/api/... ./internal/store/...` 通过（`internal/store` 里有把整条迁移链回滚一遍的测试，会走到新迁移的 Down）；提交后 `make check-gen` 无差异。工作区里若有别的会话未提交的接口改动，`make check-gen` 会因它们而非零退出，这时改用 `git diff -- internal/apigen internal/store/db web/src/api | grep -Eci 'selection[-_]?actions?'` 判定，输出 `0` 即本步骤的生成物已全部提交。

随步骤新增的单测（都是行为断言）：

- `web/src/lib/side-ask.test.ts`（步骤 2.1）：见该步骤末尾。
- `web/src/lib/context-menu/dispatch.test.tsx`（步骤 1.3）：登记了菜单项的元素上右键 → 事件 `defaultPrevented` 且出现 `context-menu`；`textarea` 上右键 → 不拦；按住 Shift → 不拦；没有任何登记、`host.desktop` 为 null → 不拦；同样情形把 `host.desktop` 改成非 null（改写方式同 `AppShell.test.tsx:385`）→ `defaultPrevented` 且没有菜单；spec 返回 `"suppress"` → `defaultPrevented` 且没有菜单；先有祖先处理器 `preventDefault` → 不开菜单；聚焦一个登记过的按钮后按 Shift+F10 → 出菜单，Esc 后焦点回到该按钮。
- `web/src/components/ui/ContextMenu.test.tsx`（步骤 1.2）：↓ ↑ Home End 跳过禁用项；Enter 执行并关闭；带 `children` 的项 → 进子菜单、← 返回；菜单外 `pointerdown` 关闭；打开着的 `Dialog` 不因菜单里的 Esc 而关闭。
- `web/src/state/side-ask.test.ts`（步骤 2.3）：运行中的记录是信封问题时 `runSideAsk` 先发 `btw_cancel` 再发 `btw`；是普通问题时不发 `btw`、`sideAskBusy` 为真；`closeSideAsk()` 在记录运行中时发 `btw_cancel`，`keepRunning` 时不发；应答晚于关闭时对新记录发 `btw_cancel`。`locateEntry`：分支上该条目之后有两条用户消息 → `turnsAgo: 2`；条目早于最近一次压缩的保留段 → `inContext: false`。
- `web/src/components/layout/ThreadsPane.test.tsx` 增一例（步骤 4.2）：右键线程行出现「置顶」等项，且所有链接的 `textContent` 仍只等于标题。
- `dispatch.test.tsx` 另加一例（步骤 1.3）：页面别处有选区、焦点在一个登记过的按钮上按 Shift+F10 → 菜单是按钮自己的项，没有选区组。
- 随步骤改动的现有测试：`FileTree.test.tsx` 与 `FileBrowser.test.tsx`（步骤 1.4，加宿主、改 Esc 的派发目标）、`composer-drafts.test.ts`（步骤 2.5，空草稿多一个 `insert: null`）。`QuoteReply.test.tsx`（步骤 2.6）与 `Transcript.test.tsx:294-301`（步骤 3.1，行内菜单的「复制」）不改，保持通过。

端到端（步骤 8.1 完成后）：`cd web && bun run accept:m22`，全部 `PASS`。除注明外视口 1280×800。各场景用哪条线程由脚本按夹具定：主线程是 `MAIN_THREAD`（`web/src/mock/fixtures.ts:120`），带图片的消息在 `fixtures.ts:2537` 所在的夹具线程，bash 工具卡见 `fixtures.ts:1574`；某类对象夹具里确实没有时，在 `fixtures.ts` 的主线程里补一条。触屏长按（R20）用 CDP 的 `Input.dispatchTouchEvent` 发 `touchStart`、等 600ms、再发 `touchEnd`。场景：

| # | 操作 | 期望 |
|---|---|---|
| R1 | 在一条助手消息里拖选一段字，在选区上右键 | 出现 `context-menu`，依次有 解释、就此提问…、翻译、引用回复、复制、搜索；该次 `contextmenu` 事件 `defaultPrevented === true` |
| R2 | 接 R1 点「解释」 | 出现 `side-ask-card`，文字逐段增长，最终含「模拟回答（explain）」；`window.__orynMock` 里这条记录的 `question` 以 `<oryn-selection action="explain" source="the assistant's reply` 开头，含 `<selected>\n{选中的原文}\n</selected>`，且含非空的 `<before>` 或 `<after>` |
| R3 | 在输入框 textarea 上右键；在助手消息上按住 Shift 右键 | 两次事件的 `defaultPrevented` 都是 `false`，没有 `context-menu` |
| R4 | `oryn.mock.btw-delay` 设为 2000，触发解释后立刻点卡片的关闭 | 卡片消失；mock 里该记录最新一轮 `status === "cancelled"` |
| R5 | 延迟 2000：输入 `/btw 慢问题` 发出并关掉对话框，再在选区上右键 | 解释 / 就此提问… / 翻译三项都是 `disabled` |
| R6 | 延迟 2000：触发解释，未结束时对另一段文字再点「解释」 | 第一条记录变 `cancelled`，第二条在跑，卡片显示第二条 |
| R7 | 解释完成后在卡片里追问「再短一点」 | 该记录 `followUps.length === 1`，卡片出现追问气泡与新回答 |
| R8 | 点卡片的「在旁路提问里打开」 | btw 对话框打开并选中这条话题；列表项文字以「解释：」开头，不含 `<oryn-selection` |
| R9 | 右键「就此提问…」，输入「这句对吗」发送 | 记录的 `question` 含 `action="ask"` 且以 `Question: 这句对吗` 结尾 |
| R10 | 右键一个代码块 | 有 复制代码、解释这段代码；点复制代码后剪贴板等于代码块源码 |
| R11 | 右键一张转录图片 | 有 复制图片、下载图片、引用到输入框；点最后一项后输入框出现一个附件条目 |
| R12 | 右键一张 bash 工具卡 | 有 复制命令、解释这次调用；点解释后记录的 `question` 含 `action="tool"` 与 `bash: ` |
| R13 | 在侧栏右键一个不是当前线程的线程行 → 置顶 | 该行出现在置顶分组；再右键 → 改名，对话框输入框初值是这条线程的标题，保存后侧栏该行标题更新、当前线程标题不变 |
| R14 | 收件箱页右键一行 → 标为未读 | 该行出现未读点 |
| R15 | 文件面板里右键一个文件 | 出现与迁移前相同的动作集（重命名、下载、复制路径、删除等） |
| R16 | 打开一个文本文件，选中两行，右键 → 引用到输入框 | 输入框文本含 `` `<绝对路径>:<起>-<止>` `` 与三反引号围起来的选中代码 |
| R17 | 改动面板右键一个文件 → 在文件面板打开 | 右面板切到文件标签，查看器显示该文件 |
| R18 | 终端面板里右键 | 有 粘贴、全选、清屏；先全选再右键 → 另有 解释、引用到输入框、复制 |
| R19 | 设置 → 选区快捷动作，添加「挑毛病 / 找出这段话里的漏洞」；回线程在选区上右键 → 挑毛病 | 菜单里有「挑毛病」；记录的 `question` 含 `action="custom" name="挑毛病"` 与 `找出这段话里的漏洞` |
| R20 | 视口 390×844、触摸模拟：长按侧栏线程行 600ms 后松开 | 出现 `context-sheet`；路由没有变化 |
| R21 | 打开分享页 `/s/{MOCK_SHARE_SLUGS.snapshot}`（`web/src/mock/server.ts:89-94`，值 `5a0c1e7f3b9d42a8c6e1f0b2d4a69c3e`），在助手消息选区上右键；再在选区外的同一条消息上右键 | 第一次菜单只有「复制」；第二次只有「复制消息」（都没有 AI 项、引用、搜索、fork、标签） |

人工看图（设计规范要求亲自看）：`cd web && bun run dev:mock`，在浏览器里对 R1、R2、R13、R20 各看一遍明暗两套主题——菜单贴边时不出屏、玻璃材质与圆角和现有菜单一致、解释卡不挡住选区那一行。

## Assumptions & contingencies

- 「翻译」的目标语言是界面语言（`i18n.language`）；原文已是该语言时在中文与英文之间互译。
- 自定义动作上限 12 个，名称 ≤ 24 字、提示词 ≤ 2000 字；菜单里直接列前 3 个，其余进「更多动作」。
- 「搜索」是全局内容搜索（命令面板预填），不限定当前线程。
- 解释卡定位在打开时算一次，不跟随滚动。从别的对话框（如 btw 对话框）里发起时，解释卡是叠在上面的对话框而不是浮动卡。「就此提问…」里已输入但没发出的文字，在窗口跨断点（宽 ↔ 窄）导致线程页重挂载时不保留。
- Space 聊天与助手对话、分享页、子 agent 对话框里没有 AI 动作；助手分区的对话行没有右键菜单。
- AI 动作的模型与思考档位就是主线当前的（btw 的行为）；不为解释单独降档，换档位会让消息缓存失效。
- 真实 Core 上的缓存命中没有单独验收，依赖 btw 既有机制。

执行中的预案：

- `0043` 已被别的迁移占用 → 用下一个空号，文件名其余部分不变。
- 编辑时工具报文件已被改动（别的会话刚改过同一个文件）→ 重读该文件，在最新内容上重做这一处改动；不还原、不覆盖对方的行。两边改到同一段代码且语义冲突时停下来问用户。
- 取消后紧接着的 `btw` 仍报 `cancel it first`（重试一次后依旧）→ 把重试等待从 150ms 提到 500ms；仍不行就让卡片显示这条错误，不再自动重试。
- `NavLink` / `Link` 不把 ref 回调传到 `<a>` 上 → 改为在链接上加 `data-ctx-thread={thread.id}`，由 `ThreadsPane` / `InboxPage` 的列表容器登记一个 spec，按 `req.target.closest("[data-ctx-thread]")` 从索引（`indexStore`）取线程对象。
- 目标 WebView 没有 `ClipboardItem` 或 `clipboard.write` → 「复制图片」不出现（步骤 3.2 已按能力判断），不另做降级。
- xterm 6 的类型里没有 `modes.mouseTrackingMode` → `mouseTracking()` 恒返回 `false`（右键始终出菜单）。
- iOS 真机上长按与系统选字抢手势（菜单出来的同时出现选字放大镜）→ 把 `LONG_PRESS_MS` 降到 400。
- `AppShell` 把 `Dialogs.tsx` 的懒加载对话框拉起来时测试环境报 Suspense 相关告警 → 在对应测试里 `await` 对话框出现（`findByRole("dialog")`）。
