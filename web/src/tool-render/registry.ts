/**
 * Tool renderer registry. Keys are current wire tool names.
 * `mcp__*` tools share the MCP renderer; unknown tools fall back to the generic JSON renderer.
 */
import { genericRenderer } from "./generic";
import { ARTIFACT_TOOLS, artifactRenderer } from "./tools/artifact";
import { askRenderer } from "./tools/ask";
import { astEditRenderer } from "./tools/ast-edit";
import { astGrepRenderer } from "./tools/ast-grep";
import { bashRenderer } from "./tools/bash";
import { browserRenderer } from "./tools/browser";
import { checkpointRenderer, rewindRenderer } from "./tools/checkpoint";
import { debugRenderer } from "./tools/debug";
import { desktopRecordRenderer } from "./tools/desktop-record";
import { editRenderer } from "./tools/edit";
import { evalRenderer } from "./tools/eval";
import { fetchRenderer } from "./tools/fetch";
import { generateImageRenderer } from "./tools/generate-image";
import { githubRenderer } from "./tools/github";
import { globRenderer } from "./tools/glob";
import { goalRenderer } from "./tools/goal";
import { grepRenderer } from "./tools/grep";
import { idaRenderer } from "./tools/ida";
import { lspRenderer } from "./tools/lsp";
import { mcpRenderer } from "./tools/mcp";
import { memoryEditRenderer } from "./tools/memory-edit";
import { recallRenderer } from "./tools/memory-recall";
import { reflectRenderer } from "./tools/memory-reflect";
import { retainRenderer } from "./tools/memory-retain";
import { ORYN_TOOLS, orynRenderer } from "./tools/oryn";
import { readRenderer } from "./tools/read";
import { reportToolIssueRenderer } from "./tools/report-tool-issue";
import { resolveRenderer } from "./tools/resolve";
import { securityScanRenderer } from "./tools/security-scan";
import { taskRenderer } from "./tools/task";
import { terminalRenderer } from "./tools/terminal";
import { todoRenderer } from "./tools/todo";
import { ttsRenderer } from "./tools/tts";
import { waitRenderer } from "./tools/wait";
import { webSearchRenderer } from "./tools/web-search";
import { writeRenderer } from "./tools/write";
import { isMcpToolName, XDEV_DEVICE_TOOL, XDEV_DOCS_TOOL } from "./tools/xdev";
import { xdevDeviceRenderer, xdevDocsRenderer } from "./tools/xdev-cards";
import { yieldRenderer } from "./tools/yield";
import type { ToolRenderer } from "./types";

const RENDERERS: Record<string, ToolRenderer> = {
	ask: askRenderer,
	ast_edit: astEditRenderer,
	ast_grep: astGrepRenderer,
	bash: bashRenderer,
	browser: browserRenderer,
	checkpoint: checkpointRenderer,
	rewind: rewindRenderer,
	puppeteer: browserRenderer,
	debug: debugRenderer,
	desktop_record: desktopRecordRenderer,
	edit: editRenderer,
	apply_patch: editRenderer,
	eval: evalRenderer,
	js: evalRenderer,
	python: evalRenderer,
	notebook: evalRenderer,
	fetch: fetchRenderer,
	glob: globRenderer,
	find: globRenderer,
	generate_image: generateImageRenderer,
	github: githubRenderer,
	goal: goalRenderer,
	ida: idaRenderer,
	wait: waitRenderer,
	lsp: lspRenderer,
	memory_edit: memoryEditRenderer,
	recall: recallRenderer,
	reflect: reflectRenderer,
	retain: retainRenderer,
	read: readRenderer,
	report_tool_issue: reportToolIssueRenderer,
	// `xd://report_issue` 设备与顶层 report_tool_issue 是同一件事
	report_issue: reportToolIssueRenderer,
	resolve: resolveRenderer,
	reject: resolveRenderer,
	propose: resolveRenderer,
	security_scan: securityScanRenderer,
	grep: grepRenderer,
	search: grepRenderer,
	task: taskRenderer,
	terminal: terminalRenderer,
	todo: todoRenderer,
	web_search: webSearchRenderer,
	tts: ttsRenderer,
	write: writeRenderer,
	yield: yieldRenderer,
	[XDEV_DOCS_TOOL]: xdevDocsRenderer,
	[XDEV_DEVICE_TOOL]: xdevDeviceRenderer,
};

// Oryn 宿主工具（M9）：只出现在协调对话里，与 omp 自带工具不重名
for (const name of ORYN_TOOLS) RENDERERS[name] = orynRenderer;
// Artifacts 工具（M14）：orb / Runner 的扩展工具与全局助手的宿主工具同名
for (const name of ARTIFACT_TOOLS) RENDERERS[name] = artifactRenderer;

/**
 * 按名字取查找表里的值，只认自有属性：工具名 / 设备名可能来自模型写的 `xd://<name>`，
 * `constructor`、`__proto__`、`toString` 之类不能命中 Object 原型上的成员。
 */
export function ownLookup<T>(
	table: Readonly<Record<string, T>>,
	name: string,
): T | undefined {
	return Object.hasOwn(table, name) ? table[name] : undefined;
}

/** 助手的电脑（M19）遥控的工具名前缀：`computer_bash` 等与 orb 里的同名内置工具共用卡片。 */
const COMPUTER_PREFIX = "computer_";
/** 带前缀但不是遥控工具、按自己的名字显示的宿主工具。 */
const COMPUTER_OWN_TOOLS: Record<string, true> = {
	computer_status: true,
	computer_upload: true,
	computer_download: true,
};

/** 是否是助手在自己的电脑上遥控执行的工具（`computer_<内置工具>`）。 */
export function isComputerTool(name: string): boolean {
	return (
		name.startsWith(COMPUTER_PREFIX) &&
		name.length > COMPUTER_PREFIX.length &&
		!Object.hasOwn(COMPUTER_OWN_TOOLS, name)
	);
}

/** 查渲染器、图标与类别用的名字：`computer_<x>` 去掉前缀成 `<x>`，其余原样。 */
export function baseToolName(name: string): string {
	return isComputerTool(name) ? name.slice(COMPUTER_PREFIX.length) : name;
}

export function resolveToolRenderer(name: string): ToolRenderer {
	const base = baseToolName(name);
	return (
		ownLookup(RENDERERS, base) ??
		(isMcpToolName(base) ? mcpRenderer : genericRenderer)
	);
}
