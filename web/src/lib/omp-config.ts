/**
 * omp `config.yml` 的结构化编辑（设置页的启停、角色模型、子 agent 等表单都经这里改文件）。
 *
 * 用 `yaml` 的 Document API 原地改节点：未触及的注释、空行、引号风格、行内 / 块式风格都原样保留。
 * 约定：
 * - 值设为 `null` 即删除该键；删除后沿路径向上清掉变空的映射 / 列表（根节点除外）。
 *   被删项上方以空行隔开的注释段（如文件头注释）不属于该项，留给下一项。
 * - 路径中间遇到缺失或不是映射的节点时换成新映射，与 Core `setByPath` 一致（`config/settings.ts`）。
 * - `disabledExtensions`、`task.disabledAgents` 保持原顺序：新条目追加到末尾，不产生重复。
 *
 * 写回文件用 `writeConfigDoc`；`doc.errors` 非空（原文不是合法 YAML）时 `yaml` 拒绝序列化，调用方应先检查。
 */
import {
	type Document,
	isCollection,
	isMap,
	isNode,
	isPair,
	isScalar,
	isSeq,
	type Node,
	parseDocument,
	type YAMLMap,
	type YAMLSeq,
} from "yaml";

type Path = readonly string[];
type ScalarValue = string | number | boolean;

/** Core 设置项路径（id 按 `.` 拆段，见 `config/registry.ts` 的 `segments`）。 */
const DISABLED_EXTENSIONS: Path = ["disabledExtensions"];
const MODEL_ROLES: Path = ["modelRoles"];
const CYCLE_ORDER: Path = ["cycleOrder"];
const MODEL_TAGS: Path = ["modelTags"];
const TASK_DISABLED_AGENTS: Path = ["task", "disabledAgents"];
const TASK_AGENT_MODEL_OVERRIDES: Path = ["task", "agentModelOverrides"];
const COMPOSER_PREDICTIONS: Path = ["composer", "predictions"];
const COMPOSER_PREDICTION_ALTERNATIVES: Path = [
	"composer",
	"predictionAlternatives",
];
const DEFAULT_THINKING_LEVEL: Path = ["defaultThinkingLevel"];

/**
 * omp 的思考档位（`ThinkingLevel` 去掉 inherit，加 auto），与后端 `internal/agentcfg/roles.go` 的 `thinkingLevels`
 * 同一份取值；按 auto → off → … → max 排列，供各处档位下拉共用。
 */
export const THINKING_LEVELS = [
	"auto",
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;
export type ThinkingLevel = (typeof THINKING_LEVELS)[number];

/** omp `defaultThinkingLevel` 没写或不合法时的缺省值。 */
export const FALLBACK_THINKING_LEVEL: ThinkingLevel = "high";

export function isThinkingLevel(v: unknown): v is ThinkingLevel {
	return (THINKING_LEVELS as readonly unknown[]).includes(v);
}

/** 解析 config.yml；空文本得到空文档。 */
export function readConfigDoc(text: string): Document {
	return parseDocument(text);
}

/** 序列化回文本；行内列表写成 `[a, b]`（`yaml` 默认 `[ a, b ]`，会改动手写的行内列表）。 */
export function writeConfigDoc(doc: Document): string {
	return doc.toString({ flowCollectionPadding: false });
}

/**
 * 删除集合的第 index 项。`yaml` 把紧贴的整块注释都挂在该项上，其中以空行隔开的前几段（如文件头注释）不属于它：
 * 移给下一项，没有下一项时挂到集合末尾。删的是首项时，下一项沿用它的前置空行设定，避免开头多出空行。
 */
function removeItem(
	doc: Document,
	coll: YAMLMap | YAMLSeq,
	index: number,
): void {
	const items = coll.items as unknown[];
	const [removed] = items.splice(index, 1);
	const node = isPair(removed) ? removed.key : removed;
	const following = items[index];
	const next = isPair(following) ? following.key : following;
	if (!isNode(node)) return;
	if (index === 0 && isNode(next)) next.spaceBefore = node.spaceBefore;
	const comment = node.commentBefore ?? "";
	const cut = comment.lastIndexOf("\n\n");
	if (cut < 0) return;
	const detached = comment.slice(0, cut);
	if (isNode(next)) {
		next.commentBefore = `${detached}\n\n${next.commentBefore ?? ""}`.replace(
			/\n+$/,
			"\n",
		);
	} else if (coll === doc.contents) {
		// 根映射删空时注释放到文档开头（`{}` 之前），否则接在文档末尾
		const key = index === 0 ? "commentBefore" : "comment";
		doc[key] = doc[key] ? `${doc[key]}\n${detached}` : detached;
	} else {
		coll.comment = coll.comment ? `${coll.comment}\n${detached}` : detached;
	}
}

function removeKey(doc: Document, map: YAMLMap, key: string): void {
	const index = map.items.findIndex((p) =>
		isScalar(p.key) ? p.key.value === key : p.key === key,
	);
	if (index >= 0) removeItem(doc, map, index);
}

/** 根映射；根为空或不是映射时换成新的空映射。 */
function rootMap(doc: Document): YAMLMap {
	if (!isMap(doc.contents)) doc.contents = doc.createNode({}) as YAMLMap;
	return doc.contents as YAMLMap;
}

/** 沿 path 取映射，缺失或不是映射的位置换成新映射。 */
function ensureMap(doc: Document, path: Path): YAMLMap {
	let map = rootMap(doc);
	for (const key of path) {
		const existing: unknown = map.get(key, true);
		if (isMap(existing)) {
			map = existing;
			continue;
		}
		const next = doc.createNode({}) as YAMLMap;
		map.set(key, next);
		map = next;
	}
	return map;
}

/** 沿 path 取已有的映射；中途缺失或不是映射时返回 undefined。 */
function findMap(doc: Document, path: Path): YAMLMap | undefined {
	let node: unknown = doc.contents;
	for (const key of path) {
		if (!isMap(node)) return undefined;
		node = node.get(key, true);
	}
	return isMap(node) ? node : undefined;
}

/** 删除 path 上的键，再向上清掉变空的容器（根映射保留）。 */
function removePath(doc: Document, path: Path): void {
	const parentPath = path.slice(0, -1);
	const parent = findMap(doc, parentPath);
	const key = path[path.length - 1];
	if (!parent || key === undefined || !parent.has(key)) return;
	removeKey(doc, parent, key);
	for (let depth = parentPath.length; depth > 0; depth--) {
		const at = path.slice(0, depth);
		const map = findMap(doc, at);
		if (!map || map.items.length > 0) break;
		const owner = findMap(doc, at.slice(0, -1));
		if (owner) removeKey(doc, owner, at[at.length - 1] ?? "");
	}
}

/** 写入 path；标量覆盖标量时只改值（保留该标量的注释与引号风格），同类容器替换时沿用原来的行内 / 块式风格与注释。 */
function setPath(
	doc: Document,
	path: Path,
	value: ScalarValue | readonly ScalarValue[],
): void {
	const parent = ensureMap(doc, path.slice(0, -1));
	const key = path[path.length - 1] ?? "";
	const prev = parent.get(key, true);
	if (isScalar(prev) && !Array.isArray(value)) {
		prev.value = value;
		return;
	}
	const next = doc.createNode(value) as Node;
	if (isCollection(prev) && isCollection(next) && isSeq(prev) === isSeq(next)) {
		next.flow = prev.flow;
		next.commentBefore = prev.commentBefore;
		next.comment = prev.comment;
		next.spaceBefore = prev.spaceBefore;
	}
	parent.set(key, next);
}

/**
 * 字符串列表里加入 / 移除一项：加入时追加到末尾（已存在则不动），移除时去掉所有同值项，列表变空则删除该键。
 * 返回是否改动了文档。
 */
function setListMember(
	doc: Document,
	path: Path,
	id: string,
	present: boolean,
): boolean {
	const node = doc.getIn(path, true);
	const seq = isSeq(node) ? (node as YAMLSeq) : undefined;
	const has = seq?.items.some((it) => isScalar(it) && it.value === id) ?? false;
	if (present === has) return false;
	if (present) {
		if (seq) seq.add(doc.createNode(id));
		else setPath(doc, path, [id]);
		return true;
	}
	if (!seq) return false;
	for (let i = seq.items.length - 1; i >= 0; i--) {
		const it = seq.items[i];
		if (isScalar(it) && it.value === id) removeItem(doc, seq, i);
	}
	if (seq.items.length === 0) removePath(doc, path);
	return true;
}

/**
 * 启停一个扩展条目（`disabledExtensions`，键由下方 `*Key` 构造）。语义同 Core
 * `persistDisabledExtensionsToggle`（`modes/rpc/rpc-actions.ts`）：已是目标状态时不改动，返回 false。
 */
export function toggleDisabled(
	doc: Document,
	id: string,
	enabled: boolean,
): boolean {
	return setListMember(doc, DISABLED_EXTENSIONS, id, !enabled);
}

/** 停用 / 恢复一个子 agent（`task.disabledAgents`）；返回是否改动。 */
export function setDisabledAgent(
	doc: Document,
	name: string,
	disabled: boolean,
): boolean {
	return setListMember(doc, TASK_DISABLED_AGENTS, name, disabled);
}

/** 设置角色模型（`modelRoles.<role>`）；null 删除。 */
export function setModelRole(
	doc: Document,
	role: string,
	selector: string | null,
): void {
	const path = [...MODEL_ROLES, role];
	if (selector === null) removePath(doc, path);
	else setPath(doc, path, selector);
}

/** 设置模型切换顺序（`cycleOrder`）；null 删除（回到 Core 默认 smol → default → slow）。 */
export function setCycleOrder(
	doc: Document,
	roles: readonly string[] | null,
): void {
	if (roles === null) removePath(doc, CYCLE_ORDER);
	else setPath(doc, CYCLE_ORDER, roles);
}

/** 设置角色标签的显示名（`modelTags.<role>.name`）；null 删除，标签只剩空映射时一并删除。 */
export function setModelTagName(
	doc: Document,
	role: string,
	name: string | null,
): void {
	const path = [...MODEL_TAGS, role, "name"];
	if (name === null) removePath(doc, path);
	else setPath(doc, path, name);
}

/**
 * 设置子 agent 的模型覆盖（`task.agentModelOverrides.<agent>`，Core 类型 `string | string[]`，
 * 见 `task/settings.ts`）；null 删除。
 */
export function setAgentModelOverride(
	doc: Document,
	agent: string,
	selector: string | readonly string[] | null,
): void {
	const path = [...TASK_AGENT_MODEL_OVERRIDES, agent];
	if (selector === null) removePath(doc, path);
	else setPath(doc, path, selector);
}

/** 输入框预测的两个布尔设置（`composer.predictions`、`composer.predictionAlternatives`）。 */
export interface ComposerPredictionSettings {
	predictions: boolean;
	alternatives: boolean;
}

/** Core 的默认值：Core 19 起预测默认开启，多候选仍默认关闭。 */
const COMPOSER_PREDICTION_DEFAULTS: ComposerPredictionSettings = {
	predictions: true,
	alternatives: false,
};

const COMPOSER_PREDICTION_PATHS: Record<
	keyof ComposerPredictionSettings,
	Path
> = {
	predictions: COMPOSER_PREDICTIONS,
	alternatives: COMPOSER_PREDICTION_ALTERNATIVES,
};

/**
 * 同 Core 设置注册表的 `accepts` / `compute`：只认 YAML 布尔值，其余（含缺失、字符串 "false"）按默认值
 * （Core 记一条 invalid 警告后用默认值）。
 */
function flagAt(
	doc: Document | undefined,
	key: keyof ComposerPredictionSettings,
): boolean {
	const v: unknown = doc?.getIn(COMPOSER_PREDICTION_PATHS[key]);
	return typeof v === "boolean" ? v : COMPOSER_PREDICTION_DEFAULTS[key];
}

export function composerPredictionSettings(
	doc: Document | undefined,
): ComposerPredictionSettings {
	return {
		predictions: flagAt(doc, "predictions"),
		alternatives: flagAt(doc, "alternatives"),
	};
}

/** 开关输入框预测设置项；与 Core 默认值相同时删除该键，`composer` 变空时一并删除。 */
export function setComposerPredictionSetting(
	doc: Document,
	key: keyof ComposerPredictionSettings,
	on: boolean,
): void {
	const path = COMPOSER_PREDICTION_PATHS[key];
	if (on === COMPOSER_PREDICTION_DEFAULTS[key]) removePath(doc, path);
	else setPath(doc, path, on);
}

/** `defaultThinkingLevel`：没写或取值不合法时按 omp 缺省值（high），与后端 `ParseDefaultThinkingLevel` 一致。 */
export function defaultThinkingLevel(doc: Document | undefined): ThinkingLevel {
	const v: unknown = doc?.getIn(DEFAULT_THINKING_LEVEL);
	return isThinkingLevel(v) ? v : FALLBACK_THINKING_LEVEL;
}

/** 设置新会话的默认思考档位；null 删除该键（回到 omp 缺省值）。 */
export function setDefaultThinkingLevel(
	doc: Document,
	level: ThinkingLevel | null,
): void {
	if (level === null) removePath(doc, DEFAULT_THINKING_LEVEL);
	else setPath(doc, DEFAULT_THINKING_LEVEL, level);
}

// ── 启停键：与 Core 各 capability 的 `toExtensionId` 一致（`packages/coding-agent/src/capability/*.ts`） ──

/** `capability/skill.ts`：`skill:<name>`。 */
export function skillKey(name: string): string {
	return `skill:${name}`;
}

export type HookType = "pre" | "post";

/** `capability/hook.ts`：`hook:<type>:<tool>:<name>`。 */
export function hookKey(type: HookType, tool: string, name: string): string {
	return `hook:${type}:${tool}:${name}`;
}

/**
 * 从配置目录（omp home 的 agent 目录，或项目的 `.omp/`）下的相对路径推导 hook 启停键；不是 hook 文件时返回 undefined。
 *
 * 对应 Core `discovery/builtin.ts` 的 `loadHooks`：
 * - 只扫 `hooks/pre/`、`hooks/post/` 的直接子项（`readDirEntries` 不递归），type 取目录名；
 * - 跳过 `.` 开头的文件；
 * - name 是**完整文件名**（含扩展名）；
 * - tool 是去掉最后一个扩展名的文件名（`*.ts` → `*`，表示所有工具）。
 * 键格式见 `capability/hook.ts` 的 `toExtensionId`。例：`hooks/pre/bash.ts` → `hook:pre:bash:bash.ts`。
 */
export function hookKeyFromPath(relPath: string): string | undefined {
	const parts = relPath.split("/");
	if (parts.length !== 3 || parts[0] !== "hooks") return undefined;
	const [, type, name = ""] = parts;
	if (type !== "pre" && type !== "post") return undefined;
	if (name === "" || name.startsWith(".")) return undefined;
	const dot = name.lastIndexOf(".");
	const tool = dot >= 0 ? name.slice(0, dot) : name;
	return hookKey(type, tool, name);
}

/** `capability/extension-module.ts`：`extension-module:<name>`。 */
export function extensionModuleKey(name: string): string {
	return `extension-module:${name}`;
}

/** `capability/tool.ts`：`tool:<name>`。 */
export function toolKey(name: string): string {
	return `tool:${name}`;
}

/** `capability/rule.ts`：`rule:<name>`。 */
export function ruleKey(name: string): string {
	return `rule:${name}`;
}

/** `capability/prompt.ts`：`prompt:<name>`。 */
export function promptKey(name: string): string {
	return `prompt:${name}`;
}

/** `capability/instruction.ts`：`instruction:<name>`。 */
export function instructionKey(name: string): string {
	return `instruction:${name}`;
}

/** `capability/slash-command.ts`：`slash-command:<name>`。 */
export function slashCommandKey(name: string): string {
	return `slash-command:${name}`;
}

/** `capability/context-file.ts`：`context-file:<level>:<basename>`。 */
export function contextFileKey(
	level: "user" | "project",
	basename: string,
): string {
	return `context-file:${level}:${basename}`;
}
