/**
 * Tool renderer registry. Keys are current wire tool names.
 * `mcp__*` tools share the MCP renderer; unknown tools fall back to the generic JSON renderer.
 */
import { genericRenderer } from "./generic";
import { askRenderer } from "./tools/ask";
import { astEditRenderer } from "./tools/ast-edit";
import { astGrepRenderer } from "./tools/ast-grep";
import { bashRenderer } from "./tools/bash";
import { browserRenderer } from "./tools/browser";
import { checkpointRenderer, rewindRenderer } from "./tools/checkpoint";
import { debugRenderer } from "./tools/debug";
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

export function resolveToolRenderer(name: string): ToolRenderer {
	return (
		ownLookup(RENDERERS, name) ??
		(isMcpToolName(name) ? mcpRenderer : genericRenderer)
	);
}
