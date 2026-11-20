/** `browser` — drive a Chromium tab: open/close named tabs, run puppeteer scripts. */
import type { ReactNode } from "react";
import {
	Badge,
	Badges,
	CodeBlock,
	ResultImages,
	ResultText,
	type Tone,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	num,
	shortenPath,
	str,
	truncate,
} from "../util";

interface BrowserDetails {
	action: string | null;
	name: string | null;
	url: string | null;
	browser: string | null;
}

function detailsOf(result: ToolRenderProps["result"]): BrowserDetails {
	const d = detailsRecord(result);
	return {
		action: d ? str(d.action) : null,
		name: d ? str(d.name) : null,
		url: d ? str(d.url) : null,
		browser: d ? str(d.browser) : null,
	};
}

interface AppArg {
	path: string | null;
	cdpUrl: string | null;
	target: string | null;
}

function appOf(args: Record<string, unknown>): AppArg | null {
	if (!isRecord(args.app)) return null;
	return {
		path: str(args.app.path),
		cdpUrl: str(args.app.cdp_url),
		target: str(args.app.target),
	};
}

function actionTone(action: string): Tone | undefined {
	switch (action) {
		case "open":
			return "ok";
		case "run":
			return "accent";
		case "close":
			return "warn";
		default:
			return undefined;
	}
}

/** Mirrors the TUI's `describeBrowser` for explicit app args; reported mode (details.browser) is the fallback. */
function describeApp(app: AppArg | null): string | null {
	if (app?.cdpUrl) return `connected ${app.cdpUrl}`;
	if (app?.path) return `spawned ${shortenPath(app.path)}`;
	return null;
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsOf(result);
	const action = str(args.action) ?? details.action ?? "?";
	const closeAll =
		action === "close" &&
		(args.all === true || (str(args.name) === null && details.name === null));
	const tab = details.name ?? str(args.name) ?? "main";
	const url = details.url ?? str(args.url);
	return (
		<>
			<Badge tone={actionTone(action)}>{action}</Badge>
			<span>{closeAll ? "all tabs" : tab}</span>
			{args.kill === true && <Badge tone="err">kill</Badge>}
			{url && (
				<span className="tv-faint">{truncate(shortenPath(url), 72)}</span>
			)}
		</>
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	// 旧调用可能没写 action，回落到结果报告的动作，脚本照样在输入段给出
	const action = str(args.action) ?? detailsOf(result).action;
	const app = appOf(args);
	const tab = str(args.name);
	const url = str(args.url);
	const viewport = isRecord(args.viewport) ? args.viewport : null;
	const vpWidth = viewport ? num(viewport.width) : null;
	const vpHeight = viewport ? num(viewport.height) : null;
	const vpScale = viewport ? num(viewport.scale) : null;
	const code = str(args.code);
	return (
		<>
			<Badges
				items={[
					tab !== null && <Badge>tab {tab}</Badge>,
					url && <Badge tone="accent">{truncate(shortenPath(url), 120)}</Badge>,
					describeApp(app),
					app?.target && `target ${app.target}`,
					args.all === true && <Badge tone="warn">all</Badge>,
					args.kill === true && <Badge tone="err">kill</Badge>,
					vpWidth !== null &&
						vpHeight !== null &&
						`${vpWidth}×${vpHeight}${vpScale !== null ? `@${vpScale}x` : ""}`,
				]}
			/>
			{action === "run" && code !== null && (
				<CodeBlock code={code.replace(/\s+$/, "")} lang="javascript" />
			)}
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsOf(result);
	// 结果报告的标签页、地址、浏览器只在与参数不同（或参数未给）时列出
	const tab = details.name !== str(args.name) ? details.name : null;
	const url = details.url !== str(args.url) ? details.url : null;
	// 显式 app 参数优先于结果报告的浏览器模式
	const browser = describeApp(appOf(args)) ? null : details.browser;
	return (
		<>
			<Badges
				items={[
					tab !== null && <Badge>tab {tab}</Badge>,
					url && <Badge tone="accent">{truncate(shortenPath(url), 120)}</Badge>,
					browser,
				]}
			/>
			<ResultImages result={result} />
			<ResultText result={result} maxLines={10} />
		</>
	);
}

export const browserRenderer: ToolRenderer = { Summary, Input, Result };
