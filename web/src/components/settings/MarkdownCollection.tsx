/**
 * 一类 Markdown 条目（技能、命令、规则、提示片段、instruction）的列表与编辑：
 * - 布局：`dir-skill` 是 `<dir>/<名称>/SKILL.md`（删除时连同目录下其他文件）；`flat-md` 是 `<dir>/<名称>.<扩展名>`。
 * - 每行显示名称与描述（frontmatter `description`，没有时取正文首个非空行，与 Core 斜杠命令一致），
 *   开关写 config.yml 的 `disabledExtensions`（`toggleDisabled(doc, keyOf(name), enabled)`）。
 * - 名称与 Core 一致：技能取 frontmatter `name`（去空白）否则目录名（`discovery/helpers.ts` 的 `scanSkillsFromDir`）；
 *   其余取文件名去掉扩展名（`nameFromFile` 可覆盖，如 instruction 另去掉 `.instructions`）。
 * - 新建 / 编辑对话框：名称（只在新建时可填）、`fields` 列出的 frontmatter 字段、正文编辑器；
 *   未列出的 frontmatter 键原样保留。frontmatter 无法解析时退回整份原文编辑。
 * - 删除前确认；被停用的条目删除后顺带把它的键从 `disabledExtensions` 里移除。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type AgentFile,
	useAgentFileActions,
	useAgentFiles,
	useAgentTexts,
	useOmpConfig,
} from "../../api/agent-config-queries";
import { parseFrontmatter, stringifyFrontmatter } from "../../lib/frontmatter";
import { toggleDisabled } from "../../lib/omp-config";
import { Badge, Callout, Card, ListRow } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	Field,
	IconButton,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../ui/primitives";
import {
	AgentErrorText,
	CodeField,
	ConfirmDeleteDialog,
} from "./agent-config-ui";

export type FieldKind = "text" | "textarea" | "list" | "bool";

/** 一个 frontmatter 字段。`list` 在表单里是逗号分隔的文本，写成 YAML 列表。 */
export interface FieldSpec {
	key: string;
	kind: FieldKind;
	label: string;
	hint?: string;
	required?: boolean;
}

export type CollectionLayout = "dir-skill" | "flat-md";

export interface MarkdownCollectionProps {
	/** 相对 omp-home 的目录，如 `skills`、`commands` */
	dir: string;
	layout: CollectionLayout;
	/** `flat-md` 认的扩展名（不带点）；第一个用于新建。缺省 `["md"]` */
	extensions?: readonly string[];
	/** 名称 → `disabledExtensions` 里的键 */
	keyOf(name: string): string;
	fields: readonly FieldSpec[];
	title: string;
	icon: IconName;
	/** 卡片顶部的一行说明 */
	hint?: ReactNode;
	emptyText: string;
	/** `flat-md`：文件名 → 条目名；缺省去掉扩展名 */
	nameFromFile?(file: string): string;
	/** 新建时名称的格式（缺省：字母数字开头，字母数字 `.` `_` `-`，最长 64） */
	namePattern?: RegExp;
	nameHint?: string;
	/** Core 跳过没有描述的条目（技能）：行上标出，表单里描述必填 */
	requireDescription?: boolean;
}

const DEFAULT_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const DEFAULT_EXTENSIONS: readonly string[] = ["md"];

interface Entry {
	/** 主文件（SKILL.md 或 `<dir>/<file>`） */
	path: string;
	/** 删除时要删的全部文件 */
	paths: string[];
	/** 目录名或文件名去扩展名：新建时与名称相同 */
	stem: string;
	ext: string;
}

function extOf(file: string): string {
	const dot = file.lastIndexOf(".");
	return dot > 0 ? file.slice(dot + 1) : "";
}

function stripExt(file: string): string {
	const dot = file.lastIndexOf(".");
	return dot > 0 ? file.slice(0, dot) : file;
}

/** 由扁平文件列表找出本目录的条目（跳过 `.` 开头的项，同 Core）。 */
export function collectionEntries(
	files: readonly AgentFile[],
	dir: string,
	layout: CollectionLayout,
	extensions: readonly string[] = ["md"],
): Entry[] {
	const prefix = `${dir}/`;
	const out: Entry[] = [];
	if (layout === "dir-skill") {
		const skillFiles = new Map<string, string[]>();
		for (const f of files) {
			if (!f.path.startsWith(prefix)) continue;
			const [folder, ...rest] = f.path.slice(prefix.length).split("/");
			if (!folder || folder.startsWith(".") || rest.length === 0) continue;
			const list = skillFiles.get(folder) ?? [];
			list.push(f.path);
			skillFiles.set(folder, list);
		}
		for (const [folder, paths] of skillFiles) {
			const path = `${prefix}${folder}/SKILL.md`;
			if (paths.includes(path))
				out.push({ path, paths: paths.sort(), stem: folder, ext: "md" });
		}
	} else {
		for (const f of files) {
			if (!f.path.startsWith(prefix)) continue;
			const file = f.path.slice(prefix.length);
			if (file.includes("/") || file.startsWith(".")) continue;
			const ext = extOf(file);
			if (!extensions.includes(ext)) continue;
			out.push({ path: f.path, paths: [f.path], stem: stripExt(file), ext });
		}
	}
	return out.sort((a, b) => a.path.localeCompare(b.path));
}

interface Parsed {
	name: string;
	description: string;
	data: Record<string, unknown>;
	body: string;
	error?: string;
}

function parseEntry(
	entry: Entry,
	text: string,
	layout: CollectionLayout,
	nameFromFile: (file: string) => string,
): Parsed {
	const { data, body, error } = parseFrontmatter(text);
	const rawName = data.name;
	const name =
		layout === "dir-skill"
			? (typeof rawName === "string" && rawName.trim()) || entry.stem
			: nameFromFile(`${entry.stem}.${entry.ext}`);
	const desc =
		typeof data.description === "string" ? data.description.trim() : "";
	const firstLine = body
		.split("\n")
		.map((l) => l.trim())
		.find(Boolean);
	return {
		name,
		description: desc || firstLine || "",
		data,
		body,
		error,
	};
}

export function MarkdownCollection(props: MarkdownCollectionProps) {
	const {
		dir,
		layout,
		extensions = DEFAULT_EXTENSIONS,
		keyOf,
		title,
		icon,
		hint,
		emptyText,
		requireDescription,
	} = props;
	const nameFromFile = props.nameFromFile ?? stripExt;
	const { t } = useTranslation();
	const files = useAgentFiles();
	const config = useOmpConfig();
	const entries = useMemo(
		() => collectionEntries(files.data ?? [], dir, layout, extensions),
		[files.data, dir, layout, extensions],
	);
	const paths = useMemo(() => entries.map((e) => e.path), [entries]);
	const { texts, error: textError } = useAgentTexts(paths);
	const [editing, setEditing] = useState<Entry | "new" | null>(null);
	const [deleting, setDeleting] = useState<{
		entry: Entry;
		name: string;
	} | null>(null);

	const rows = entries.map((entry) => {
		const text = texts.get(entry.path);
		const parsed =
			typeof text === "string"
				? parseEntry(entry, text, layout, nameFromFile)
				: undefined;
		return { entry, parsed };
	});
	const names = new Set(
		rows
			.map((r) => r.parsed?.name ?? r.entry.stem)
			.concat(entries.map((e) => e.stem)),
	);

	const onDeleted = (name: string) => {
		const key = keyOf(name);
		if (config.disabled.has(key))
			void config.mutate((doc) => toggleDisabled(doc, key, true));
	};

	return (
		<Card
			title={title}
			icon={icon}
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
			{hint && <p className="mb-2 text-faint text-xs">{hint}</p>}
			{files.error ? (
				<ErrorText error={files.error} />
			) : files.data === undefined ? (
				<div className="flex justify-center py-6">
					<Spinner />
				</div>
			) : entries.length === 0 ? (
				<EmptyState icon={icon} title={emptyText} />
			) : (
				<div className="-mx-2 flex flex-col">
					{rows.map(({ entry, parsed }) => {
						const name = parsed?.name ?? entry.stem;
						const key = keyOf(name);
						const enabled = !config.disabled.has(key);
						return (
							<ListRow
								key={entry.path}
								title={
									<>
										<span className="min-w-0 truncate font-medium">{name}</span>
										{entry.ext !== "md" && <Badge>.{entry.ext}</Badge>}
										{requireDescription &&
											parsed &&
											!(
												typeof parsed.data.description === "string" &&
												parsed.data.description.trim()
											) && (
												<Badge tone="warn" icon="alert">
													{t("agentPages.missingDescription")}
												</Badge>
											)}
										{parsed?.error && (
											<Badge tone="warn" icon="alert">
												{t("agentPages.frontmatterBroken")}
											</Badge>
										)}
									</>
								}
								meta={
									parsed?.description ? (
										<span className="line-clamp-2 min-w-0">
											{parsed.description}
										</span>
									) : (
										<span className="font-mono">{entry.path}</span>
									)
								}
								trailing={
									<>
										<Toggle
											checked={enabled}
											disabled={config.doc === undefined}
											onChange={(on) =>
												void config.mutate((doc) =>
													toggleDisabled(doc, key, on),
												)
											}
											label={
												<span className="sr-only">
													{t("agentPages.enable", { name })}
												</span>
											}
										/>
										<IconButton
											icon="edit"
											box="sm"
											size={14}
											label={t("agentPages.edit", { name })}
											disabled={parsed === undefined}
											onClick={() => setEditing(entry)}
										/>
										<IconButton
											icon="trash"
											box="sm"
											size={14}
											label={t("agentPages.delete", { name })}
											onClick={() => setDeleting({ entry, name })}
										/>
									</>
								}
							/>
						);
					})}
				</div>
			)}
			<div className="mt-1 flex flex-col gap-1 empty:hidden">
				{textError ? <ErrorText error={textError} /> : null}
				<AgentErrorText error={config.mutateError} />
			</div>
			{editing && (
				<EntryDialog
					props={props}
					entry={editing === "new" ? null : editing}
					text={editing === "new" ? null : (texts.get(editing.path) ?? null)}
					taken={names}
					onClose={() => setEditing(null)}
				/>
			)}
			{deleting && (
				<ConfirmDeleteDialog
					name={deleting.name}
					paths={deleting.entry.paths}
					onClose={() => setDeleting(null)}
					onDeleted={() => onDeleted(deleting.name)}
				/>
			)}
		</Card>
	);
}

type FormValues = Record<string, string | boolean>;

function formValue(spec: FieldSpec, v: unknown): string | boolean {
	if (spec.kind === "bool") return v === true;
	if (spec.kind === "list") {
		if (Array.isArray(v)) return v.map(String).join(", ");
		return typeof v === "string" ? v : "";
	}
	return typeof v === "string" ? v : v == null ? "" : String(v);
}

/** 表单值写回 frontmatter：空值删除键，`list` 按逗号拆成列表，`bool` 只在为真时写 `true`。 */
function applyFields(
	data: Record<string, unknown>,
	fields: readonly FieldSpec[],
	values: FormValues,
): Record<string, unknown> {
	const out = { ...data };
	for (const f of fields) {
		const v = values[f.key];
		if (f.kind === "bool") {
			if (v === true) out[f.key] = true;
			else delete out[f.key];
			continue;
		}
		const s = typeof v === "string" ? v.trim() : "";
		if (!s) {
			delete out[f.key];
			continue;
		}
		out[f.key] =
			f.kind === "list"
				? s
						.split(",")
						.map((x) => x.trim())
						.filter(Boolean)
				: s;
	}
	return out;
}

function EntryDialog({
	props,
	entry,
	text,
	taken,
	onClose,
}: {
	props: MarkdownCollectionProps;
	/** null = 新建 */
	entry: Entry | null;
	text: string | null;
	taken: ReadonlySet<string>;
	onClose(): void;
}) {
	const { dir, layout, fields, requireDescription } = props;
	const extensions = props.extensions ?? DEFAULT_EXTENSIONS;
	const nameFromFile = props.nameFromFile ?? stripExt;
	const namePattern = props.namePattern ?? DEFAULT_NAME;
	const { t } = useTranslation();
	const actions = useAgentFileActions();
	const original = useMemo(
		() =>
			entry && text !== null
				? parseEntry(entry, text, layout, nameFromFile)
				: null,
		[entry, text, layout, nameFromFile],
	);
	const [name, setName] = useState(original?.name ?? "");
	const [values, setValues] = useState<FormValues>(() =>
		Object.fromEntries(
			fields.map((f) => [f.key, formValue(f, original?.data[f.key])]),
		),
	);
	const [body, setBody] = useState(original?.body ?? "");
	/** frontmatter 坏了：整份原文编辑 */
	const [raw, setRaw] = useState(original?.error ? (text ?? "") : null);
	const [showErrors, setShowErrors] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);

	const creating = entry === null;
	const trimmed = name.trim();
	const nameError = !creating
		? null
		: !trimmed
			? t("agentPages.errors.nameRequired")
			: !namePattern.test(trimmed)
				? t("agentPages.errors.nameInvalid")
				: taken.has(trimmed)
					? t("agentPages.errors.nameTaken")
					: null;
	const missing = fields.filter(
		(f) =>
			(f.required || (requireDescription && f.key === "description")) &&
			f.kind !== "bool" &&
			!String(values[f.key] ?? "").trim(),
	);
	const invalid = raw === null && (nameError !== null || missing.length > 0);

	const path = entry
		? entry.path
		: layout === "dir-skill"
			? `${dir}/${trimmed}/SKILL.md`
			: `${dir}/${trimmed}.${extensions[0] ?? "md"}`;

	const save = async () => {
		setShowErrors(true);
		if (invalid || busy) return;
		setBusy(true);
		setError(null);
		let out: string;
		if (raw !== null) out = raw;
		else {
			// 新建技能时把名称写进 frontmatter（Core 以它为准），放在最前
			const head: Record<string, unknown> =
				creating && layout === "dir-skill" ? { name: trimmed } : {};
			out = stringifyFrontmatter(
				applyFields({ ...head, ...(original?.data ?? {}) }, fields, values),
				body,
			);
		}
		try {
			await actions.write(path, out, creating ? null : text);
			onClose();
		} catch (e) {
			setError(e);
			setBusy(false);
		}
	};

	const set = (key: string, v: string | boolean) =>
		setValues((prev) => ({ ...prev, [key]: v }));

	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={
				creating
					? t("agentPages.newTitle", { kind: props.title })
					: t("agentPages.edit", { name: original?.name ?? entry?.stem ?? "" })
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
							hint={
								creating
									? (props.nameHint ?? t("agentPages.nameHint"))
									: undefined
							}
						>
							<TextInput
								value={name}
								readOnly={!creating}
								disabled={!creating}
								autoFocus={creating}
								aria-invalid={showErrors && nameError !== null}
								onChange={(e) => setName(e.target.value)}
							/>
						</Field>
						{showErrors && nameError && (
							<p className="-mt-2 text-err text-xs">{nameError}</p>
						)}
						{fields.map((f) => {
							const v = values[f.key];
							if (f.kind === "bool")
								return (
									<div key={f.key} className="flex flex-col gap-1">
										<Toggle
											checked={v === true}
											onChange={(on) => set(f.key, on)}
											label={f.label}
										/>
										{f.hint && (
											<span className="text-faint text-xs">{f.hint}</span>
										)}
									</div>
								);
							const isMissing = showErrors && missing.includes(f);
							return (
								<Field key={f.key} label={f.label} hint={f.hint}>
									{f.kind === "textarea" ? (
										<TextArea
											rows={2}
											className="font-sans"
											value={String(v ?? "")}
											aria-invalid={isMissing}
											onChange={(e) => set(f.key, e.target.value)}
										/>
									) : (
										<TextInput
											value={String(v ?? "")}
											aria-invalid={isMissing}
											onChange={(e) => set(f.key, e.target.value)}
										/>
									)}
									{isMissing && (
										<span className="text-err text-xs">
											{t("agentPages.errors.required")}
										</span>
									)}
								</Field>
							);
						})}
						<div className="flex flex-col gap-1">
							<span className="flex items-center gap-1.5 font-medium text-muted text-xs">
								<Icon name="fileText" size={12} aria-hidden />
								{t("agentPages.body")}
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
				<AgentErrorText error={error} />
			</form>
		</Dialog>
	);
}
