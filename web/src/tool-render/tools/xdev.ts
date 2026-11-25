/**
 * `xd://` 设备调用的解包：唯一一处把外层 `write xd://<device>` / `read xd://<device>`
 * 换成「真实工具名 + 真实参数 + 内层 details」，渲染器只拿真实工具的 args / result / partial。
 *
 * Core 语义（oryn-core `internal-urls/xd-protocol.ts`、`tools/xdev.ts`）：
 * - `write xd://<tool>` 的 content 是 JSON 参数对象；派发成功（及参数校验通过后内层工具抛错）时
 *   `details.xdev = { tool, mode: "execute", args, inner }`，运行中的部分结果同样包在 `details.xdev.inner`。
 * - content 为空、`?` 或 `help` 时是 help 模式：`details.xdev = { tool, mode: "help" }`，结果文本是设备文档。
 * - 设备不存在、JSON 无效、参数校验失败时 `isError`，`details.xdev` 不带 args（或整个 details 缺失）。
 * - `resolve` / `reject` / `propose` / `report_issue` 的 content 是纯文本，不是 JSON。
 * - `read xd://` 是设备目录，`read xd://<tool>` 是设备文档，`read xd://<tool>/<topic>` 是文档专题。
 */
import type { ToolResultLike } from "../types";
import { isRecord, str } from "../util";

/** 设备文档卡（help 模式与 `read xd://…`）的渲染器键。 */
export const XDEV_DOCS_TOOL = "xd:docs";
/** 未能派发的设备写入（派发前失败、content 不是参数对象）的渲染器键。 */
export const XDEV_DEVICE_TOOL = "xd:device";

const XD_PREFIX = "xd://";
/** Core `HELP_CONTENT_RE`：空、`?` 或 `help` 请求文档而非执行。 */
const HELP_CONTENT_RE = /^\s*(\?|help)?\s*$/i;
/** omp 注册 MCP 工具的名字前缀：`mcp__<server>_<tool>`。 */
const MCP_PREFIX = "mcp__";

export interface XdevCard {
	/** 卡片头显示名：`xd://<device>`。 */
	label: string;
	/** 选渲染器、图标与类别用的名字：真实工具名，或 XDEV_DOCS_TOOL / XDEV_DEVICE_TOOL。 */
	tool: string;
	/** 交给渲染器的参数（真实工具的参数，或伪工具的 `{ device, topic }` / `{ device, payload }`）。 */
	args: Record<string, unknown>;
	/** 设备参数里习惯性带的 `i`（外层调用没有意图时用）。 */
	intent?: string;
	/** 为真时结果与部分结果的 details 换成派发的内层 details（{@link unwrapXdevResult}）。 */
	unwrap: boolean;
}

interface Dispatch {
	tool: string;
	mode: "help" | "execute";
	args: Record<string, unknown> | null;
}

/** `xd://<name>`：根返回 `name: null`；含 `/?#` 或不是 xd:// 返回 null（同 Core `parseXdUrl`）。 */
function parseXdUrl(input: string): { name: string | null } | null {
	const trimmed = input.trim();
	if (!trimmed.toLowerCase().startsWith(XD_PREFIX)) return null;
	const name = trimmed.slice(XD_PREFIX.length);
	if (name.length === 0) return { name: null };
	if (/[/?#]/.test(name)) return null;
	return { name };
}

/** `xd://<tool>/<topic>` 文档专题（同 Core `parseXdTopicUrl`）。 */
function parseXdTopicUrl(
	input: string,
): { name: string; topic: string } | null {
	const match = /^xd:\/\/([^/?#]+)\/([^/?#]+)$/i.exec(input.trim());
	return match?.[1] && match[2] ? { name: match[1], topic: match[2] } : null;
}

function dispatchOf(details: unknown): Dispatch | null {
	if (!isRecord(details) || !isRecord(details.xdev)) return null;
	const { tool, mode, args } = details.xdev;
	if (typeof tool !== "string" || (mode !== "help" && mode !== "execute"))
		return null;
	return { tool, mode, args: isRecord(args) ? args : null };
}

/** 设备名是 MCP 工具（`mcp__<server>_<tool>`）。 */
export function isMcpToolName(name: string): boolean {
	return name.startsWith(MCP_PREFIX);
}

/**
 * MCP 工具的 server / tool：结果 details 的 `serverName` / `mcpToolName` 为准（服务名本身可能含 `_`），
 * 没有结果时按 Core `parseMCPToolName` 拆名字（服务名到第一个 `_` 为止）。不是 MCP 名字时为 null。
 */
export function mcpOrigin(
	name: string,
	details: unknown,
): { server: string; tool: string } | null {
	if (!isMcpToolName(name)) return null;
	const rest = name.slice(MCP_PREFIX.length);
	const at = rest.indexOf("_");
	const server = isRecord(details) ? str(details.serverName) : null;
	const tool = isRecord(details) ? str(details.mcpToolName) : null;
	if (server && tool) return { server, tool };
	if (at <= 0 || at === rest.length - 1) return null;
	return { server: rest.slice(0, at), tool: rest.slice(at + 1) };
}

/** `report_issue` 的纯文本：首行工具名 + 正文，或 `<tool>: <描述>`（同 Core `parseReportIssueBody`）。 */
function reportIssueArgs(text: string): Record<string, unknown> {
	const body = text.trim();
	const nl = body.indexOf("\n");
	if (nl >= 0) {
		const tool = body.slice(0, nl).trim();
		const report = body.slice(nl + 1).trim();
		if (tool && report) return { tool, report };
	}
	const colon = body.search(/:\s/);
	if (colon > 0) {
		const tool = body.slice(0, colon).trim();
		const report = body.slice(colon + 1).trim();
		if (tool && report) return { tool, report };
	}
	return { report: body };
}

/** 纯文本设备的真实参数；不是纯文本设备时为 null。 */
function textDeviceArgs(
	device: string,
	text: string,
): Record<string, unknown> | null {
	switch (device) {
		case "resolve":
		case "reject":
			return { reason: text.trim() };
		case "propose":
			return { title: text.trim() };
		case "report_issue":
			return reportIssueArgs(text);
		default:
			return null;
	}
}

function parseArgsObject(content: string): Record<string, unknown> | null {
	try {
		const parsed: unknown = JSON.parse(content);
		return isRecord(parsed) ? parsed : null;
	} catch {
		return null;
	}
}

function docsCard(device: string | null, topic: string | null): XdevCard {
	return {
		label: `${XD_PREFIX}${device ?? ""}`,
		tool: XDEV_DOCS_TOOL,
		args: { device, topic },
		unwrap: false,
	};
}

function executeCard(
	device: string,
	rawArgs: Record<string, unknown>,
	unwrap: boolean,
	label = `${XD_PREFIX}${device}`,
): XdevCard {
	let args = rawArgs;
	let intent: string | undefined;
	if (typeof rawArgs.i === "string") {
		intent = rawArgs.i.trim() || undefined;
		args = { ...rawArgs };
		delete args.i;
	}
	// report_issue 派发时只记录整段 `{ report }`；拆成真实工具 report_tool_issue 的 `{ tool, report }`
	if (device === "report_issue" && typeof args.tool !== "string") {
		const report = str(args.report);
		if (report) args = reportIssueArgs(report);
	}
	return { label, tool: device, args, intent, unwrap };
}

function deviceCard(device: string, payload: string | null): XdevCard {
	return {
		label: `${XD_PREFIX}${device}`,
		tool: XDEV_DEVICE_TOOL,
		args: { device, payload },
		unwrap: false,
	};
}

/** 从 Core 19.1+ bash 工具结果的 `details.toolCommands` 中识别单工具命令派发。 */
function singleToolCommandOf(
	details: unknown,
): (Dispatch & { inner?: unknown }) | null {
	if (!isRecord(details) || !Array.isArray(details.toolCommands)) return null;
	const list = details.toolCommands.filter(isRecord);
	if (list.length !== 1) return null;
	const item = list[0];
	if (!item) return null;
	const tool = str(item.tool);
	const mode =
		item.mode === "help" || item.mode === "execute" ? item.mode : null;
	if (!tool || !mode) return null;
	return {
		tool,
		mode,
		args: isRecord(item.args) ? item.args : null,
		inner: item.inner,
	};
}

/**
 * 把一次 `write` / `read` 调用识别为 `xd://` 设备卡片，或把 Core 19.1+ 单条 `bash` 工具命令
 * （`details.toolCommands`）识别为真实工具卡片；不是派发调用时返回 null。
 * `args` 是外层调用已去掉意图字段的参数。
 */
export function xdevCardOf(input: {
	name: string;
	args: Record<string, unknown>;
	result?: ToolResultLike;
	partialResult?: ToolResultLike;
}): XdevCard | null {
	const { name, args, result } = input;
	if (name === "bash") {
		const tc = singleToolCommandOf(
			result ? result.details : input.partialResult?.details,
		);
		if (!tc) return null;
		if (tc.mode === "help") {
			return {
				label: `${tc.tool} --help`,
				tool: XDEV_DOCS_TOOL,
				args: { device: tc.tool, topic: null },
				unwrap: false,
			};
		}
		if (tc.args) return executeCard(tc.tool, tc.args, true, tc.tool);
		return null;
	}
	const path = str(args.path);
	if (name === "read") {
		if (!path) return null;
		const topic = parseXdTopicUrl(path);
		if (topic) return docsCard(topic.name, topic.topic);
		const target = parseXdUrl(path);
		return target ? docsCard(target.name, null) : null;
	}
	if (name !== "write") return null;
	const target = path ? parseXdUrl(path) : null;
	// 完成后看最终结果，运行中看部分结果（Core 把真实工具的进度同样包在 details.xdev 里）
	const dispatch = result
		? dispatchOf(result.details)
		: dispatchOf(input.partialResult?.details);
	if (!target && !dispatch) return null;
	const device = dispatch?.tool ?? target?.name ?? "";

	if (dispatch?.mode === "help") return docsCard(device, null);
	if (dispatch?.args) return executeCard(device, dispatch.args, true);

	// 尚未派发（调用刚发出、设备不发进度）或派发前失败：从外层参数推断
	const content = typeof args.content === "string" ? args.content : null;
	if (device && content !== null) {
		if (HELP_CONTENT_RE.test(content)) return docsCard(device, null);
		const text = textDeviceArgs(device, content);
		if (text) return executeCard(device, text, false);
		// 派发失败时参数没通过校验（或设备不存在）：不交给真实渲染器，显示错误与原始内容
		const parsed = result?.isError ? null : parseArgsObject(content);
		if (parsed) return executeCard(device, parsed, false);
	}
	return deviceCard(device, content);
}

/** 派发卡片的结果：外层 write/bash 的 details 换成内层工具的 details（没有派发时为 undefined）。 */
export function unwrapXdevResult(
	result: ToolResultLike | undefined,
): ToolResultLike | undefined {
	if (!result) return undefined;
	const xdev =
		isRecord(result.details) && isRecord(result.details.xdev)
			? result.details.xdev
			: null;
	const inner = xdev ? xdev.inner : singleToolCommandOf(result.details)?.inner;
	return {
		content: result.content,
		details: inner,
		isError: result.isError,
	};
}
