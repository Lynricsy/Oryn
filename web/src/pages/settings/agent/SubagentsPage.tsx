/**
 * 设置 → Agent → 子 agent（M13 R27 / R28）：
 * - 内置 agent 只读展示名字、简介与头像（简介取头像接口的 description，没有时用本地化说明）；
 * - 自定义 agent 是 `agents/*.md`：名字以 frontmatter `name` 为准，Core 只加载同时有 `name` 与 `description` 的定义
 *   （`discovery/helpers.ts` 的 `parseAgentFields`）。表单字段对应 Core 认的 frontmatter，未列出的键原样保留；
 * - 每行都能启停（`task.disabledAgents`）、设模型覆盖（`task.agentModelOverrides.<agent>`）与改头像。
 * 编辑框的保存基线在打开时冻结（同 MarkdownCollection 的 EntryDialog）：之后文件在别处被改，保存一律冲突，
 * 只能「载入最新内容」后重做，不会静默覆盖。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Document } from "yaml";
import {
	isModifiedError,
	useAgentFileActions,
	useAgentFiles,
	useAgentTexts,
	useOmpConfig,
} from "../../../api/agent-config-queries";
import type { Schemas } from "../../../api/client";
import {
	agentFace,
	saveAgentAvatar,
	useAgentAvatars,
	useRoleIdentities,
} from "../../../api/identity-queries";
import { AvatarEditor } from "../../../components/settings/AvatarEditor";
import {
	AgentErrorText,
	AgentScopeNote,
	CodeField,
	ConfirmDeleteDialog,
	EditConflict,
} from "../../../components/settings/agent-config-ui";
import { collectionEntries } from "../../../components/settings/MarkdownCollection";
import { ModelPatternList } from "../../../components/settings/ModelPatternInput";
import { useAsync } from "../../../components/settings/useAsync";
import { Badge, Callout, Card, ListRow } from "../../../components/ui/data";
import { IdentityAvatar } from "../../../components/ui/IdentityAvatar";
import { Icon } from "../../../components/ui/icons";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../../components/ui/primitives";
import i18n from "../../../i18n";
import {
	parseFrontmatter,
	stringifyFrontmatter,
} from "../../../lib/frontmatter";
import {
	setAgentModelOverride,
	setDisabledAgent,
} from "../../../lib/omp-config";

type AvatarIdentity = Schemas["AvatarIdentity"];

const AGENTS_DIR = "agents";
/** 新建时的名字格式（同其他 Agent 子页）。 */
const AGENT_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
/** Core 的保留名：主会话与无名子会话的 agentName（`MAIN_AGENT_RULE_NAME` / `SUB_AGENT_RULE_NAME`）。 */
const RESERVED_NAMES: Record<string, true> = { main: true, sub: true };
/** 子 agent 可设的思考档位（Core `parseConfiguredThinkingLevel`）；空串 = 不设置。 */
const THINKING_LEVELS = [
	"",
	"auto",
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;

// ---------------------------------------------------------------- config.yml 的 task 设置

interface TaskSettings {
	disabled: ReadonlySet<string>;
	/** agent → 模型覆盖（按顺序回退） */
	overrides: ReadonlyMap<string, string[]>;
}

/** 字符串或字符串列表 → 去空白、丢空项的列表；字符串按逗号拆。 */
function stringList(v: unknown): string[] {
	const items = Array.isArray(v)
		? v.filter((x) => typeof x === "string" || typeof x === "number")
		: typeof v === "string"
			? v.split(",")
			: [];
	return items.map((x) => String(x).trim()).filter(Boolean);
}

/** `task.disabledAgents` 与 `task.agentModelOverrides`；文档缺失或不合法时为空。 */
export function taskSettings(doc: Document | undefined): TaskSettings {
	const out = { disabled: new Set<string>(), overrides: new Map() };
	if (!doc || doc.errors.length > 0) return out;
	const task: unknown = doc.toJS()?.task;
	if (!task || typeof task !== "object") return out;
	const t = task as Record<string, unknown>;
	for (const n of stringList(t.disabledAgents)) out.disabled.add(n);
	const ov = t.agentModelOverrides;
	if (ov && typeof ov === "object" && !Array.isArray(ov))
		for (const [k, v] of Object.entries(ov)) {
			const list = stringList(v);
			if (list.length > 0) out.overrides.set(k, list);
		}
	return out;
}

/** 模型列表写回：空删除，一项写字符串，多项写列表（Core 类型 `string | string[]`）。 */
function modelValue(list: readonly string[]): string | string[] | null {
	if (list.length === 0) return null;
	return list.length === 1 ? (list[0] ?? null) : [...list];
}

// ---------------------------------------------------------------- frontmatter 表单

/** `advisor` / `prewalk`：不启用、默认目标（`true`）、指定模型模式。 */
type OptionalTarget = { mode: "off" | "default" | "custom"; pattern: string };

export interface AgentForm {
	description: string;
	model: string[];
	thinkingLevel: string;
	/** 逗号分隔 */
	tools: string;
	/** `*` 或逗号分隔 */
	spawns: string;
	blocking: boolean;
	/** 逗号分隔 */
	autoloadSkills: string;
	advisor: OptionalTarget;
	prewalk: OptionalTarget;
}

/** 同 Core `parseBoolean`：布尔值，或去空白、不分大小写的 "true" / "false"；其余为 undefined。 */
function parseBoolean(v: unknown): boolean | undefined {
	if (typeof v === "boolean") return v;
	if (typeof v !== "string") return undefined;
	const s = v.trim().toLowerCase();
	return s === "true" ? true : s === "false" ? false : undefined;
}

function csv(v: unknown): string {
	return stringList(v).join(", ");
}

/** 同 Core：布尔 true → 默认目标；非布尔的非空字符串 → 指定模式。 */
function optionalTarget(v: unknown): OptionalTarget {
	const b = parseBoolean(v);
	if (b === true) return { mode: "default", pattern: "" };
	if (b === undefined && typeof v === "string" && v.trim())
		return { mode: "custom", pattern: v.trim() };
	return { mode: "off", pattern: "" };
}

/** frontmatter → 表单（同 Core `parseAgentFields` 的取值规则：`thinkingLevel` 优先于旧键 `thinking`）。 */
export function agentForm(data: Record<string, unknown>): AgentForm {
	const thinking =
		typeof data.thinkingLevel === "string"
			? data.thinkingLevel
			: typeof data.thinking === "string"
				? data.thinking
				: "";
	const spawns =
		typeof data.spawns === "string" && data.spawns.trim() === "*"
			? "*"
			: csv(data.spawns);
	return {
		description:
			typeof data.description === "string" ? data.description.trim() : "",
		model: stringList(data.model),
		thinkingLevel: thinking.trim(),
		tools: csv(data.tools),
		spawns,
		blocking: parseBoolean(data.blocking) === true,
		autoloadSkills: csv(data.autoloadSkills),
		advisor: optionalTarget(data.advisor),
		prewalk: optionalTarget(data.prewalk),
	};
}

function setOrDelete(
	out: Record<string, unknown>,
	key: string,
	v: unknown,
): void {
	if (v === undefined || v === null || v === "") delete out[key];
	else out[key] = v;
}

function listOf(s: string): string[] | undefined {
	const list = s
		.split(",")
		.map((x) => x.trim())
		.filter(Boolean);
	return list.length > 0 ? list : undefined;
}

function targetValue(v: OptionalTarget): true | string | undefined {
	if (v.mode === "off") return undefined;
	return (v.mode === "custom" && v.pattern.trim()) || true;
}

/**
 * 表单 → frontmatter：在原 frontmatter 上改，未列出的键原样保留；空值删除键。
 * 新建时 `name` 放在最前。思考档位写回原来用的键（只有旧键 `thinking` 时沿用它）。
 */
export function applyAgentForm(
	data: Record<string, unknown>,
	form: AgentForm,
	name?: string,
): Record<string, unknown> {
	const out: Record<string, unknown> = name ? { name, ...data } : { ...data };
	if (name) out.name = name;
	setOrDelete(out, "description", form.description.trim());
	setOrDelete(out, "model", modelValue(form.model));
	const thinkingKey =
		"thinking" in data && !("thinkingLevel" in data)
			? "thinking"
			: "thinkingLevel";
	setOrDelete(out, thinkingKey, form.thinkingLevel);
	setOrDelete(out, "tools", listOf(form.tools));
	setOrDelete(
		out,
		"spawns",
		form.spawns.trim() === "*" ? "*" : listOf(form.spawns),
	);
	setOrDelete(out, "blocking", form.blocking ? true : undefined);
	setOrDelete(out, "autoloadSkills", listOf(form.autoloadSkills));
	setOrDelete(out, "advisor", targetValue(form.advisor));
	setOrDelete(out, "prewalk", targetValue(form.prewalk));
	return out;
}

// ---------------------------------------------------------------- 自定义 agent 条目

interface CustomAgent {
	path: string;
	stem: string;
	/** frontmatter `name`（去空白）；缺失时为空串 */
	name: string;
	description: string;
	data: Record<string, unknown>;
	body: string;
	error?: string;
}

function parseAgent(path: string, stem: string, text: string): CustomAgent {
	const { data, body, error } = parseFrontmatter(text);
	const name = typeof data.name === "string" ? data.name.trim() : "";
	const description =
		typeof data.description === "string" ? data.description.trim() : "";
	return { path, stem, name, description, data, body, error };
}

/** 内置 agent 的简介：接口给的 description，否则本地化说明（没有则不显示）。 */
function bundledDescription(name: string, given?: string): string | undefined {
	if (given?.trim()) return given.trim();
	const key = `agentPages.subagents.bundledDescriptions.${name}`;
	return i18n.exists(key)
		? i18n.t(key as "agentPages.subagents.bundledDescriptions.task")
		: undefined;
}

// ---------------------------------------------------------------- 页面

export default function SubagentsPage(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const config = useOmpConfig();
	const avatars = useAgentAvatars();
	const roleList = useRoleIdentities();
	const files = useAgentFiles();
	const entries = useMemo(
		() => collectionEntries(files.data ?? [], AGENTS_DIR, "flat-md"),
		[files.data],
	);
	const paths = useMemo(() => entries.map((e) => e.path), [entries]);
	const { texts, error: textError } = useAgentTexts(paths);
	const task = useMemo(() => taskSettings(config.doc), [config.doc]);
	const roles = useMemo(
		() => roleList.data?.roles.map((r) => r.role) ?? [],
		[roleList.data],
	);
	const avatarSave = useAsync();
	const [open, setOpen] = useState<string | null>(null);
	const [editing, setEditing] = useState<CustomAgent | "new" | null>(null);
	/** 保存冲突后「载入最新内容」：换 key 让编辑框以最新原文重新打开。 */
	const [editKey, setEditKey] = useState(0);
	const [deleting, setDeleting] = useState<CustomAgent | null>(null);

	const list = avatars.data;
	const bundled = list?.agents.filter((a) => a.source === "bundled") ?? [];
	const orphans =
		list?.agents.filter(
			(a) => a.source === "orphan" && (a.avatarIcon || a.avatarImage),
		) ?? [];
	const custom = entries.map((e) => {
		const text = texts.get(e.path);
		return typeof text === "string" ? parseAgent(e.path, e.stem, text) : null;
	});
	const taken = new Set<string>([
		...bundled.map((a) => a.name),
		...entries.map((e) => e.stem),
		...custom.flatMap((c) => (c?.name ? [c.name] : [])),
	]);

	const configReady =
		config.doc !== undefined && config.doc.errors.length === 0;
	const toggle = (name: string, on: boolean) =>
		void config.mutate((doc) => setDisabledAgent(doc, name, !on));
	const setOverride = (name: string, next: readonly string[]) =>
		void config.mutate((doc) => {
			setAgentModelOverride(doc, name, modelValue(next));
			return true;
		});
	const putAvatar = (name: string, avatar: AvatarIdentity) =>
		avatarSave.run(() => saveAgentAvatar(qc, name, avatar));
	const onDeleted = (name: string) => {
		if (!name || !(task.disabled.has(name) || task.overrides.has(name))) return;
		void config.mutate((doc) => {
			setDisabledAgent(doc, name, false);
			setAgentModelOverride(doc, name, null);
			return true;
		});
	};

	const row = (
		name: string,
		props: {
			title?: ReactNode;
			description?: string;
			actions?: ReactNode;
			/** 名字缺失（不会加载）的条目不给启停与头像 */
			inert?: boolean;
			/** 列表 key；缺省为名字 */
			rowKey?: string;
		},
	) => {
		const { face, glyph } = agentFace(list, name);
		const override = task.overrides.get(name) ?? [];
		const expanded = open === name;
		return (
			<li
				key={props.rowKey ?? name}
				data-agent={name}
				className="flex flex-col"
			>
				<ListRow
					leading={
						<IdentityAvatar
							identity={face}
							of="agent"
							glyph={glyph}
							size={32}
						/>
					}
					title={
						props.title ?? (
							<span className="min-w-0 truncate font-medium">{name}</span>
						)
					}
					meta={
						<>
							{props.description && (
								<span className="line-clamp-2 min-w-0">
									{props.description}
								</span>
							)}
							{override.length > 0 && (
								<span className="min-w-0 truncate font-mono">
									{t("agentPages.subagents.overrideMeta", {
										models: override.join(", "),
									})}
								</span>
							)}
						</>
					}
					trailing={
						<>
							{!props.inert && (
								<Toggle
									checked={!task.disabled.has(name)}
									disabled={!configReady}
									onChange={(on) => toggle(name, on)}
									label={
										<span className="sr-only">
											{t("agentPages.enable", { name })}
										</span>
									}
								/>
							)}
							{props.actions}
							{!props.inert && (
								<Button
									size="sm"
									variant={expanded ? "secondary" : "ghost"}
									aria-expanded={expanded}
									aria-label={t("agentPages.subagents.configure", { name })}
									onClick={() => setOpen(expanded ? null : name)}
								>
									{t("agentPages.subagents.configureShort")}
								</Button>
							)}
						</>
					}
				/>
				{expanded && (
					<div className="mx-2 mb-2 flex flex-col gap-4 rounded-lg border border-border bg-raised p-3">
						<Field
							label={t("agentPages.subagents.override")}
							hint={t("agentPages.subagents.overrideHint")}
						>
							<ModelPatternList
								value={override}
								roles={roles}
								disabled={!configReady}
								label={t("agentPages.subagents.overrideInput", { name })}
								placeholder={t("agentPages.models.placeholder")}
								onChange={(next) => setOverride(name, next)}
							/>
						</Field>
						<div className="flex flex-col gap-2">
							<span className="font-medium text-muted text-xs">
								{t("agentPages.avatar")}
							</span>
							<AvatarEditor
								value={face}
								busy={avatarSave.busy}
								resetLabel={t("agentPages.removeAvatar")}
								onChange={(avatar) => putAvatar(name, avatar)}
							/>
						</div>
					</div>
				)}
			</li>
		);
	};

	return (
		<>
			{/* ApplyStatusBanner（R29 / R30）的位置：下发状态与「需重启」提示放在页首 */}
			<AgentScopeNote />
			<Card
				title={t("agentPages.subagents.bundledTitle")}
				icon="tree"
				action={avatars.isPending && <Spinner />}
			>
				<p className="mb-2 text-faint text-xs">
					{t("agentPages.subagents.bundledHint")}
				</p>
				{avatars.error ? (
					<ErrorText error={avatars.error} />
				) : (
					<ul className="-mx-2 flex flex-col">
						{bundled.map((a) =>
							row(a.name, {
								description: bundledDescription(a.name, a.description),
							}),
						)}
					</ul>
				)}
			</Card>
			<Card
				title={t("agentPages.subagents.customTitle")}
				icon="robot"
				action={
					<Button
						size="sm"
						icon="plus"
						className="-mr-1.5"
						onClick={() => setEditing("new")}
					>
						{t("agentPages.new")}
					</Button>
				}
			>
				<p className="mb-2 text-faint text-xs">
					{t("agentPages.subagents.customHint")}
				</p>
				{files.error ? (
					<ErrorText error={files.error} />
				) : files.data === undefined ? (
					<div className="flex justify-center py-6">
						<Spinner />
					</div>
				) : entries.length === 0 ? (
					<EmptyState icon="robot" title={t("agentPages.subagents.empty")} />
				) : (
					<ul className="-mx-2 flex flex-col">
						{entries.map((e, i) => {
							const a = custom[i] ?? null;
							const name = a?.name || e.stem;
							const actions = (
								<>
									<IconButton
										icon="edit"
										box="sm"
										size={14}
										label={t("agentPages.edit", { name })}
										disabled={a === null}
										onClick={() => a && setEditing(a)}
									/>
									<IconButton
										icon="trash"
										box="sm"
										size={14}
										label={t("agentPages.delete", { name })}
										disabled={a === null}
										onClick={() => a && setDeleting(a)}
									/>
								</>
							);
							return row(name, {
								rowKey: e.path,
								inert: a !== null && !a.name,
								description: a?.description || undefined,
								actions,
								title: (
									<>
										<span className="min-w-0 truncate font-medium">{name}</span>
										{a?.error ? (
											<Badge tone="warn" icon="alert">
												{t("agentPages.frontmatterBroken")}
											</Badge>
										) : a && !a.name ? (
											<Badge tone="warn" icon="alert">
												{t("agentPages.subagents.missingName")}
											</Badge>
										) : a && !a.description ? (
											<Badge tone="warn" icon="alert">
												{t("agentPages.missingDescription")}
											</Badge>
										) : null}
									</>
								),
							});
						})}
					</ul>
				)}
			</Card>
			{orphans.length > 0 && (
				<Card title={t("agentPages.subagents.orphansTitle")} icon="user">
					<p className="mb-2 text-faint text-xs">
						{t("agentPages.subagents.orphansHint")}
					</p>
					<ul className="-mx-2 flex flex-col">
						{orphans.map((a) => (
							<li key={a.name} data-agent={a.name}>
								<ListRow
									leading={
										<IdentityAvatar
											identity={a}
											of="agent"
											glyph={agentFace(list, a.name).glyph}
											size={32}
										/>
									}
									title={
										<span className="min-w-0 truncate font-medium">
											{a.name}
										</span>
									}
									trailing={
										<Button
											size="sm"
											variant="ghost"
											icon="trash"
											disabled={avatarSave.busy}
											onClick={() => void putAvatar(a.name, {})}
										>
											{t("agentPages.removeAvatar")}
										</Button>
									}
								/>
							</li>
						))}
					</ul>
				</Card>
			)}
			<div className="flex flex-col gap-1 empty:hidden">
				{textError ? <ErrorText error={textError} /> : null}
				{avatarSave.error ? <ErrorText error={avatarSave.error} /> : null}
				<AgentErrorText error={config.mutateError} />
			</div>
			{editing && (
				<AgentDialog
					key={editKey}
					agent={editing === "new" ? null : editing}
					text={editing === "new" ? null : (texts.get(editing.path) ?? null)}
					taken={taken}
					roles={roles}
					onReload={() => setEditKey((k) => k + 1)}
					onClose={() => setEditing(null)}
				/>
			)}
			{deleting && (
				<ConfirmDeleteDialog
					name={deleting.name || deleting.stem}
					paths={[deleting.path]}
					onClose={() => setDeleting(null)}
					onDeleted={() => onDeleted(deleting.name)}
				/>
			)}
		</>
	);
}

// ---------------------------------------------------------------- 编辑框

function TargetField({
	label,
	hint,
	value,
	roles,
	onChange,
}: {
	label: string;
	hint: string;
	value: OptionalTarget;
	roles: readonly string[];
	onChange(v: OptionalTarget): void;
}): ReactNode {
	const { t } = useTranslation();
	const listId = useId();
	return (
		<Field label={label} hint={hint}>
			<div className="flex flex-col gap-1.5 sm:flex-row">
				<Select
					wrapperClassName="sm:w-44 w-full shrink-0"
					value={value.mode}
					onChange={(e) =>
						onChange({
							...value,
							mode: e.target.value as OptionalTarget["mode"],
						})
					}
				>
					<option value="off">{t("agentPages.subagents.targetOff")}</option>
					<option value="default">
						{t("agentPages.subagents.targetDefault")}
					</option>
					<option value="custom">
						{t("agentPages.subagents.targetCustom")}
					</option>
				</Select>
				{value.mode === "custom" && (
					<TextInput
						aria-label={t("agentPages.subagents.targetPattern", { label })}
						className="font-mono"
						list={listId}
						placeholder={t("agentPages.models.placeholder")}
						value={value.pattern}
						onChange={(e) => onChange({ ...value, pattern: e.target.value })}
					/>
				)}
				<datalist id={listId}>
					{roles.map((r) => (
						<option key={r} value={`@${r}`} />
					))}
				</datalist>
			</div>
		</Field>
	);
}

function AgentDialog({
	agent,
	text,
	taken,
	roles,
	onReload,
	onClose,
}: {
	/** null = 新建 */
	agent: CustomAgent | null;
	/** 当前原文；只在打开时读一次作基线，之后的刷新不影响表单与保存基线。 */
	text: string | null;
	taken: ReadonlySet<string>;
	roles: readonly string[];
	onReload(): void;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const actions = useAgentFileActions();
	// 基线与表单一起冻结：保存只在文件仍是打开时的原文时写入，冲突后再点保存仍会冲突，不会静默覆盖。
	const [base] = useState(text);
	const original = useMemo(
		() =>
			agent && base !== null ? parseAgent(agent.path, agent.stem, base) : null,
		[agent, base],
	);
	const [name, setName] = useState(original?.name ?? "");
	const [form, setForm] = useState<AgentForm>(() =>
		agentForm(original?.data ?? {}),
	);
	const [body, setBody] = useState(original?.body ?? "");
	/** frontmatter 坏了：整份原文编辑 */
	const [raw, setRaw] = useState(original?.error ? (base ?? "") : null);
	const [showErrors, setShowErrors] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);

	const creating = agent === null;
	const trimmed = name.trim();
	const nameError = !creating
		? !trimmed
			? t("agentPages.errors.nameRequired")
			: null
		: !trimmed
			? t("agentPages.errors.nameRequired")
			: !AGENT_NAME.test(trimmed)
				? t("agentPages.errors.nameInvalid")
				: Object.hasOwn(RESERVED_NAMES, trimmed.toLowerCase())
					? t("agentPages.subagents.nameReserved")
					: taken.has(trimmed)
						? t("agentPages.errors.nameTaken")
						: null;
	const descMissing = !form.description.trim();
	const invalid = raw === null && (nameError !== null || descMissing);
	const path = agent ? agent.path : `${AGENTS_DIR}/${trimmed}.md`;
	const levels: readonly string[] =
		form.thinkingLevel &&
		!(THINKING_LEVELS as readonly string[]).includes(form.thinkingLevel)
			? [...THINKING_LEVELS, form.thinkingLevel]
			: THINKING_LEVELS;

	const set = <K extends keyof AgentForm>(key: K, v: AgentForm[K]) =>
		setForm((prev) => ({ ...prev, [key]: v }));

	const save = async () => {
		setShowErrors(true);
		if (invalid || busy) return;
		setBusy(true);
		setError(null);
		const out =
			raw ??
			stringifyFrontmatter(
				// 已有文件缺 name 时把表单里填的名字补进去
				applyAgentForm(
					original?.data ?? {},
					form,
					creating || !original?.name ? trimmed : undefined,
				),
				body,
			);
		try {
			await actions.write(path, out, creating ? null : base);
			onClose();
		} catch (e) {
			setError(e);
			setBusy(false);
		}
	};

	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={
				creating
					? t("agentPages.subagents.newTitle")
					: t("agentPages.edit", { name: original?.name || agent?.stem })
			}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={busy || (showErrors && invalid)}
						onClick={() => void save()}
					>
						{busy && <Spinner size={12} />}
						{creating ? t("common.create") : t("common.save")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					void save();
				}}
			>
				{raw !== null ? (
					<>
						<Callout tone="warn">
							{t("agentPages.frontmatterRaw", {
								reason: original?.error ?? "",
							})}
						</Callout>
						<CodeField tall value={raw} path={path} onChange={setRaw} />
					</>
				) : (
					<>
						<Field
							label={t("agentPages.name")}
							hint={creating ? t("agentPages.nameHint") : undefined}
						>
							<TextInput
								value={name}
								readOnly={!creating && Boolean(original?.name)}
								disabled={!creating && Boolean(original?.name)}
								autoFocus={creating}
								aria-invalid={showErrors && nameError !== null}
								onChange={(e) => setName(e.target.value)}
							/>
						</Field>
						{showErrors && nameError && (
							<p className="-mt-2 text-err text-xs">{nameError}</p>
						)}
						<Field
							label={t("agentPages.fields.description")}
							hint={t("agentPages.subagents.descriptionHint")}
						>
							<TextArea
								rows={2}
								className="font-sans"
								value={form.description}
								aria-invalid={showErrors && descMissing}
								onChange={(e) => set("description", e.target.value)}
							/>
							{showErrors && descMissing && (
								<span className="text-err text-xs">
									{t("agentPages.errors.required")}
								</span>
							)}
						</Field>
						<Field
							label={t("agentPages.subagents.model")}
							hint={t("agentPages.subagents.modelHint")}
						>
							<ModelPatternList
								value={form.model}
								roles={roles}
								label={t("agentPages.subagents.modelInput")}
								placeholder={t("agentPages.models.placeholder")}
								onChange={(next) => set("model", next)}
							/>
						</Field>
						<Field label={t("agentPages.subagents.thinking")}>
							<Select
								value={form.thinkingLevel}
								onChange={(e) => set("thinkingLevel", e.target.value)}
							>
								{levels.map((l) => (
									<option key={l} value={l}>
										{l === ""
											? t("agentPages.subagents.thinkingDefault")
											: i18n.exists(`model.levels.${l}`)
												? t(`model.levels.${l}` as "model.levels.auto")
												: l}
									</option>
								))}
							</Select>
						</Field>
						<Field
							label={t("agentPages.subagents.tools")}
							hint={t("agentPages.subagents.toolsHint")}
						>
							<TextInput
								className="font-mono"
								value={form.tools}
								onChange={(e) => set("tools", e.target.value)}
							/>
						</Field>
						<Field
							label={t("agentPages.subagents.spawns")}
							hint={t("agentPages.subagents.spawnsHint")}
						>
							<TextInput
								className="font-mono"
								value={form.spawns}
								onChange={(e) => set("spawns", e.target.value)}
							/>
						</Field>
						<Field
							label={t("agentPages.subagents.autoloadSkills")}
							hint={t("agentPages.subagents.autoloadSkillsHint")}
						>
							<TextInput
								className="font-mono"
								value={form.autoloadSkills}
								onChange={(e) => set("autoloadSkills", e.target.value)}
							/>
						</Field>
						<div className="flex flex-col gap-1">
							<Toggle
								checked={form.blocking}
								onChange={(on) => set("blocking", on)}
								label={t("agentPages.subagents.blocking")}
							/>
							<span className="text-faint text-xs">
								{t("agentPages.subagents.blockingHint")}
							</span>
						</div>
						<TargetField
							label={t("agentPages.subagents.advisor")}
							hint={t("agentPages.subagents.advisorHint")}
							value={form.advisor}
							roles={roles}
							onChange={(v) => set("advisor", v)}
						/>
						<TargetField
							label={t("agentPages.subagents.prewalk")}
							hint={t("agentPages.subagents.prewalkHint")}
							value={form.prewalk}
							roles={roles}
							onChange={(v) => set("prewalk", v)}
						/>
						<div className="flex flex-col gap-1">
							<span className="flex items-center gap-1.5 font-medium text-muted text-xs">
								<Icon name="fileText" size={12} aria-hidden />
								{t("agentPages.subagents.body")}
							</span>
							<CodeField
								value={body}
								path={path}
								onChange={setBody}
								onSave={() => void save()}
							/>
						</div>
					</>
				)}
				{isModifiedError(error) ? (
					<EditConflict onReload={onReload} />
				) : (
					<AgentErrorText error={error} />
				)}
			</form>
		</Dialog>
	);
}
