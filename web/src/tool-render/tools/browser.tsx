/**
 * `browser` / `puppeteer` — 驱动浏览器标签页（oryn-core `tools/browser.ts`）：open / close / run / call / tabs / targets。
 * 头部：动作 + 标签页名 + 地址；读数给视口、截图张数或标签页数。
 * 输入段按动作给原生形态：open 是地址栏样式的网址与打开选项，run 是脚本，call 是函数与参数或逐步的调用链。
 * 输出段：带窗口外框的结果（地址栏 + 截图），截图另列保存位置；tabs / targets 列出页面；脚本输出与返回值按 JSON / 文本展示。
 * details 为 `{action, name, url?, browser?, viewport?, screenshots?, value?}`；`app` 新版是字符串，旧调用是对象，两种都认。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { formatBytes } from "../../lib/format";
import { ArgsTree, SmartText } from "../generic";
import {
	Badge,
	CodeBlock,
	EmptyNote,
	ErrorBlock,
	InvalidArg,
	JsonTree,
	PathText,
	ResultImages,
	SiteMark,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	hostOf,
	isRecord,
	num,
	resultImagesOf,
	resultTextOf,
	shortenPath,
	str,
	truncate,
} from "../util";

const ACTIONS = ["open", "close", "run", "call", "tabs", "targets"] as const;
type BrowserAction = (typeof ACTIONS)[number];

function asAction(value: string | null): BrowserAction | null {
	return value && (ACTIONS as readonly string[]).includes(value)
		? (value as BrowserAction)
		: null;
}

interface BrowserDetails {
	action: string | null;
	name: string | null;
	url: string | null;
	browser: string | null;
	viewport: { width: number; height: number } | null;
	screenshots: { dest: string; bytes: number | null; size: string | null }[];
	value: unknown;
}

function viewportOf(v: unknown): { width: number; height: number } | null {
	if (!isRecord(v)) return null;
	const width = num(v.width);
	const height = num(v.height);
	return width !== null && height !== null ? { width, height } : null;
}

function detailsOf(result: ToolRenderProps["result"]): BrowserDetails {
	const d = detailsRecord(result);
	const screenshots: BrowserDetails["screenshots"] = [];
	if (d && Array.isArray(d.screenshots))
		for (const s of d.screenshots) {
			const dest = isRecord(s) ? str(s.dest) : null;
			if (!dest || !isRecord(s)) continue;
			const w = num(s.width);
			const h = num(s.height);
			screenshots.push({
				dest,
				bytes: num(s.bytes),
				size: w !== null && h !== null ? `${w}×${h}` : null,
			});
		}
	return {
		action: d ? str(d.action) : null,
		name: d ? str(d.name) : null,
		url: d ? str(d.url) : null,
		browser: d ? str(d.browser) : null,
		viewport: d ? viewportOf(d.viewport) : null,
		screenshots,
		value: d?.value,
	};
}

/** 浏览器来源：新版 `app` 是字符串（关键字、CDP 地址或可执行文件），旧调用是 `{path, cdp_url, target}`。 */
function appText(args: Record<string, unknown>): string | null {
	const app = args.app;
	if (typeof app === "string")
		return app.includes("/") ? shortenPath(app) : app;
	if (!isRecord(app)) return null;
	const cdp = str(app.cdp_url);
	const path = str(app.path);
	return cdp ?? (path ? shortenPath(path) : null);
}

function actionOf(props: ToolRenderProps): BrowserAction | null {
	return asAction(str(props.args.action) ?? detailsOf(props.result).action);
}

/** 地址的展示：去掉协议，路径弱化。 */
function UrlText({ url }: { url: string }): ReactNode {
	const m = /^([a-z][\w+.-]*:\/\/)?([^/?#]*)(.*)$/i.exec(url);
	const host = m?.[2] ?? url;
	const rest = m?.[3] ?? "";
	return (
		<span className="tv-br-url">
			<span className="tv-br-url-host">{host}</span>
			{rest && <span className="tv-br-url-rest">{rest}</span>}
		</span>
	);
}

/** 地址栏：锁（https）/ 地球 + 站点标记 + 网址；可点时新窗口打开。 */
function AddressBar({
	url,
	trailing,
}: {
	url: string;
	trailing?: ReactNode;
}): ReactNode {
	const host = hostOf(url);
	const secure = url.startsWith("https://");
	const web = /^https?:\/\//.test(url);
	return (
		<div className="tv-br-bar">
			<span className="tv-br-bar-icon" aria-hidden="true">
				<Icon name={secure ? "lock" : "globe"} size={11} />
			</span>
			{host && <SiteMark host={host} />}
			{web ? (
				<a
					className="tv-br-bar-link"
					href={url}
					target="_blank"
					rel="noreferrer noopener"
				>
					<UrlText url={url} />
				</a>
			) : (
				<UrlText url={url} />
			)}
			{trailing && <span className="tv-br-bar-trail">{trailing}</span>}
		</div>
	);
}

function Summary(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { args, result } = props;
	const details = detailsOf(result);
	const raw = str(args.action) ?? details.action;
	const action = asAction(raw);
	const closeAll =
		action === "close" &&
		(args.all === true || (str(args.name) === null && details.name === null));
	const tab = details.name ?? str(args.name) ?? "main";
	const url = details.url ?? str(args.url);
	const listing = action === "tabs" || action === "targets";
	return (
		<>
			<Badge>
				{action ? t(`toolRender.browser.action.${action}`) : (raw ?? "?")}
			</Badge>
			{!listing && (
				<span className="tv-br-tab">
					{closeAll ? t("toolRender.browser.allTabs") : tab}
				</span>
			)}
			{args.kill === true && <Badge tone="err">kill</Badge>}
			{url && !listing && (
				<span className="tv-faint">
					{truncate(url.replace(/^https?:\/\//, ""), 72)}
				</span>
			)}
		</>
	);
}

/** 返回值里的页面列表（tabs 的 `ManagedTabInfo[]`、targets 的页面）。 */
function pagesOf(value: unknown): Record<string, unknown>[] | null {
	if (!Array.isArray(value)) return null;
	const pages = value.filter(isRecord);
	return pages.length === value.length ? pages : null;
}

/** 右侧读数：视口尺寸、截图张数、标签页数。 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result || result.isError) return null;
	const action = actionOf(props);
	const details = detailsOf(result);
	if (action === "tabs" || action === "targets") {
		const pages = pagesOf(details.value);
		return pages ? (
			<span>{t("toolRender.browser.pages", { count: pages.length })}</span>
		) : null;
	}
	const shots = Math.max(
		resultImagesOf(result).length,
		details.screenshots.length,
	);
	const vp = details.viewport ?? viewportOf(props.args.viewport);
	return (
		<>
			{shots > 0 && (
				<span>{t("toolRender.browser.shots", { count: shots })}</span>
			)}
			{action === "open" && vp && (
				<span>
					{vp.width}×{vp.height}
				</span>
			)}
		</>
	);
}

/** 调用链的一步：`method(args…)`，参数写成紧凑 JSON。 */
function ChainStep({
	step,
	index,
}: {
	step: unknown;
	index: number;
}): ReactNode {
	const method = isRecord(step) ? str(step.method) : null;
	const stepArgs = isRecord(step) && Array.isArray(step.args) ? step.args : [];
	let argText = "";
	try {
		argText = stepArgs.map((a) => JSON.stringify(a) ?? "").join(", ");
	} catch {
		argText = "…";
	}
	return (
		<li className="tv-br-step">
			<span className="tv-br-step-n">{index + 1}</span>
			<code className="tv-br-step-code">
				<span className="hljs-title">{method ?? "?"}</span>
				<span className="tv-faint">(</span>
				<span className="tv-muted">{truncate(argText, 160)}</span>
				<span className="tv-faint">)</span>
			</code>
		</li>
	);
}

/** 输入段里已专门排版（地址栏、徽标、脚本、调用链）的参数；其余按 JSON 树列出。 */
const SHOWN_KEYS = [
	"action",
	"url",
	"code",
	"fn",
	"args",
	"chain",
	"name",
	"app",
	"target",
	"viewport",
	"all",
	"kill",
];

function Input(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { args } = props;
	const action = actionOf(props);
	const url = str(args.url);
	const code = str(args.code);
	const fn = str(args.fn);
	const chain = Array.isArray(args.chain) ? args.chain : null;
	const fnArgs = Array.isArray(args.args) ? args.args : null;
	const vp = viewportOf(args.viewport);
	const scale = isRecord(args.viewport) ? num(args.viewport.scale) : null;
	const app = appText(args);
	const target =
		str(args.target) ?? (isRecord(args.app) ? str(args.app.target) : null);
	// 已专门排版的参数之外的选项（wait_until、headed、persist……）按 JSON 树列出
	const rest: Record<string, unknown> = {};
	for (const key in args) if (!SHOWN_KEYS.includes(key)) rest[key] = args[key];
	const chips = [
		str(args.name) !== null && (
			<Badge key="tab">
				{t("toolRender.browser.tab", { name: str(args.name) })}
			</Badge>
		),
		app && <Badge key="app">{app}</Badge>,
		target && (
			<Badge key="target">{t("toolRender.browser.target", { target })}</Badge>
		),
		vp && (
			<Badge key="vp">
				{vp.width}×{vp.height}
				{scale !== null && `@${scale}x`}
			</Badge>
		),
		args.all === true && (
			<Badge key="all" tone="warn">
				{t("toolRender.browser.allTabs")}
			</Badge>
		),
		args.kill === true && (
			<Badge key="kill" tone="err">
				kill
			</Badge>
		),
	].filter(Boolean);
	return (
		<>
			{url && <AddressBar url={url} />}
			{chips.length > 0 && <span className="tv-badges">{chips}</span>}
			{code !== null && (
				<CodeBlock code={code.replace(/\s+$/, "")} lang="javascript" />
			)}
			{fn !== null && (
				<CodeBlock code={fn.replace(/\s+$/, "")} lang="javascript" />
			)}
			{fnArgs && fnArgs.length > 0 && <JsonTree value={fnArgs} />}
			{chain && chain.length > 0 && (
				<ol className="tv-br-chain">
					{chain.map((step, i) => (
						<ChainStep key={i} step={step} index={i} />
					))}
				</ol>
			)}
			{Object.keys(rest).length > 0 && <ArgsTree args={rest} />}
			{action === null && args.action !== undefined && (
				<InvalidArg what="action" />
			)}
		</>
	);
}

/** tabs / targets 的页面列表：站点标记 + 标题 + 地址，标签页名与种类作徽标。 */
function PageList({ pages }: { pages: Record<string, unknown>[] }): ReactNode {
	const { t } = useTranslation();
	if (pages.length === 0)
		return <EmptyNote>{t("toolRender.browser.noPages")}</EmptyNote>;
	return (
		<ul className="tv-br-pages">
			{pages.map((p, i) => {
				const url = str(p.url) ?? "";
				const title = str(p.title)?.trim() || url || "?";
				const name = str(p.name);
				const kind = str(p.kind) ?? str(p.type);
				return (
					<li key={str(p.targetId) ?? name ?? i} className="tv-br-page">
						<SiteMark host={hostOf(url) || title} />
						<div className="tv-br-page-main">
							<div className="tv-br-page-title">{title}</div>
							{url && url !== title && (
								<div className="tv-br-page-url">{truncate(url, 140)}</div>
							)}
						</div>
						{name && <Badge>{name}</Badge>}
						{kind && <Badge>{kind}</Badge>}
						{p.persist === true && <Badge>persist</Badge>}
					</li>
				);
			})}
		</ul>
	);
}

/** 窗口外框：标题栏三个圆点 + 地址栏，内容是截图。 */
function BrowserFrame({
	url,
	browser,
	children,
}: {
	url: string | null;
	browser: string | null;
	children?: ReactNode;
}): ReactNode {
	return (
		<div className="tv-br-frame">
			<div className="tv-br-chrome">
				<span className="tv-br-lights" aria-hidden="true">
					<i />
					<i />
					<i />
				</span>
				{url ? (
					<AddressBar url={url} trailing={browser} />
				) : (
					browser && <span className="tv-faint">{browser}</span>
				)}
			</div>
			{children && <div className="tv-br-view">{children}</div>}
		</div>
	);
}

function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError)
		return <ErrorBlock text={text || t("toolRender.generic.failed")} />;
	const action = actionOf(props);
	const details = detailsOf(result);
	const images = resultImagesOf(result).length > 0;
	const pages =
		action === "tabs" || action === "targets" ? pagesOf(details.value) : null;
	// 有截图或拿到了页面地址：带窗口外框；地址与参数相同且没有截图时外框只是重复，省掉
	const frameUrl =
		details.url && (images || details.url !== str(props.args.url))
			? details.url
			: null;
	const showFrame = images || frameUrl !== null;
	return (
		<>
			{showFrame && (
				<BrowserFrame url={frameUrl ?? details.url} browser={details.browser}>
					{images && <ResultImages result={result} />}
				</BrowserFrame>
			)}
			{details.screenshots.length > 0 && (
				<ul className="tv-br-shots">
					{details.screenshots.map((s) => (
						<li key={s.dest}>
							<Icon name="camera" size={11} />
							<PathText path={s.dest} />
							<span className="tv-faint">
								{[s.size, s.bytes !== null ? formatBytes(s.bytes) : null]
									.filter(Boolean)
									.join(" · ")}
							</span>
						</li>
					))}
				</ul>
			)}
			{pages && <PageList pages={pages} />}
			{text && !(pages && pages.length > 0 && text.startsWith("[")) && (
				<SmartText text={text} />
			)}
			{!text && !images && !pages && !showFrame && (
				<EmptyNote>
					{action === "close"
						? t("toolRender.browser.closed")
						: t("toolRender.generic.empty")}
				</EmptyNote>
			)}
		</>
	);
}

export const browserRenderer: ToolRenderer = { Summary, Meta, Input, Result };
