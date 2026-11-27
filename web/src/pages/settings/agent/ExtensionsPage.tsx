/**
 * 设置 → 扩展与工具：列出 omp-home 下 `extensions/`、`hooks/pre|post/`、`tools/` 的条目（每类一组），
 * 提供源码编辑（行内对话框；「配置文件」的文件浏览器没有按路径直达的入口）、新建、删除与启停。这些改动要重启会话才生效。
 * 目录式条目（扩展子目录、工具子目录）打开文件管理：逐个编辑、新建、上传、删除其中的文件；
 * 扩展与工具另可整目录导入（文件夹或 zip），也可新建以 index.ts 为入口的目录。
 *
 * 名称与启停键对照 Core：
 * - 扩展模块（`discoverExtensionModulePaths` + `getExtensionNameFromPath`，键 `extension-module:<名称>`）：
 *   顶层 `*.ts|*.js` 取文件名去扩展名；子目录有 `package.json` 且 `omp`（或 `pi`）字段声明了 `extensions` 时逐个取声明的路径
 *   （指向目录则用其 `index.ts|js`，名称为目录名），否则子目录有 `index.ts|js` 时名称为目录名；都没有的子目录不会加载。
 * - hook（`loadHooks`）：`hooks/<pre|post>/<文件>`，键见 `hookKeyFromPath`。
 * - 自定义工具（`loadTools`，键 `tool:<名称>`）：顶层 `json` 取其 `name`、`md` 取 frontmatter `name`，否则文件名去扩展名；
 *   `ts|js|sh|bash|py` 取文件名去扩展名；子目录有 `index.ts` 时名称为目录名。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	useAgentFiles,
	useAgentTexts,
	useOmpConfig,
} from "../../../api/agent-config-queries";
import {
	useGenerativeUiSettings,
	useSetGenerativeUi,
} from "../../../api/generative-ui-queries";
import {
	AgentErrorText,
	AgentPageHead,
	ConfirmDeleteDialog,
	EmptyPanel,
	GroupNote,
	TextFileDialog,
} from "../../../components/settings/agent-config-ui";
import { EntryFilesDialog } from "../../../components/settings/EntryFilesDialog";
import {
	EntryImportDialog,
	type ImportKind,
} from "../../../components/settings/EntryImportDialog";
import { Badge, Callout } from "../../../components/ui/data";
import type { IconName } from "../../../components/ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextInput,
	Toggle,
} from "../../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupRow,
	Reveal,
} from "../../../components/ui/workbench";
import { parseFrontmatter } from "../../../lib/frontmatter";
import {
	extensionModuleKey,
	hookKeyFromPath,
	toggleDisabled,
	toolKey,
} from "../../../lib/omp-config";

/** 一行：一个可启停的条目（或不会被加载、没有键的目录）。 */
export interface CodeEntry {
	id: string;
	name: string;
	/** `disabledExtensions` 的键；undefined = Core 不会加载它 */
	key?: string;
	/** 行上显示的位置 */
	location: string;
	/** 条目的全部文件（删除时全删，编辑时可切换） */
	paths: string[];
	/** 默认打开的文件 */
	main: string;
}

type Section = "extensions" | "hooks" | "tools";

const TOOL_EXTENSIONS = ["json", "md", "ts", "js", "sh", "bash", "py"];

function stripExt(file: string): string {
	const dot = file.lastIndexOf(".");
	return dot > 0 ? file.slice(0, dot) : file;
}

/** 去掉 `.` 段、展开 `..` 段（`path.resolve` 的相对版）。 */
function normalize(path: string): string {
	const out: string[] = [];
	for (const seg of path.split("/")) {
		if (seg === "" || seg === ".") continue;
		if (seg === "..") out.pop();
		else out.push(seg);
	}
	return out.join("/");
}

/** 按顶层名字分组：文件 → 自身，子目录 → 其下全部文件。跳过 `.` 开头的项。 */
function topLevel(paths: readonly string[], dir: string) {
	const prefix = `${dir}/`;
	const files: string[] = [];
	const dirs = new Map<string, string[]>();
	for (const p of paths) {
		if (!p.startsWith(prefix)) continue;
		const rest = p.slice(prefix.length);
		const slash = rest.indexOf("/");
		const head = slash < 0 ? rest : rest.slice(0, slash);
		if (!head || head.startsWith(".")) continue;
		if (slash < 0) files.push(p);
		else dirs.set(head, [...(dirs.get(head) ?? []), p]);
	}
	return { files, dirs };
}

/** 子目录 `package.json` 里声明的扩展入口（`omp.extensions`，退到 `pi.extensions`）。 */
function declaredExtensions(pkg: string | null | undefined): string[] {
	if (!pkg) return [];
	try {
		const parsed: unknown = JSON.parse(pkg);
		if (!parsed || typeof parsed !== "object") return [];
		const manifest =
			"omp" in parsed && parsed.omp != null
				? parsed.omp
				: "pi" in parsed
					? parsed.pi
					: undefined;
		const list =
			manifest && typeof manifest === "object" && "extensions" in manifest
				? manifest.extensions
				: undefined;
		return Array.isArray(list)
			? list.filter((x): x is string => typeof x === "string")
			: [];
	} catch {
		return [];
	}
}

export function extensionEntries(
	paths: readonly string[],
	texts: ReadonlyMap<string, string | null>,
): CodeEntry[] {
	const { files, dirs } = topLevel(paths, "extensions");
	const out: CodeEntry[] = [];
	for (const p of files) {
		if (!/\.(ts|js)$/.test(p)) continue;
		const name = stripExt(p.slice("extensions/".length));
		out.push({
			id: p,
			name,
			key: extensionModuleKey(name),
			location: p,
			paths: [p],
			main: p,
		});
	}
	for (const [dir, list] of dirs) {
		const base = `extensions/${dir}`;
		const has = (p: string) => list.includes(p);
		const index = [`${base}/index.ts`, `${base}/index.js`].find(has);
		const declared = declaredExtensions(texts.get(`${base}/package.json`));
		let names: string[];
		if (declared.length > 0)
			names = declared.flatMap((e) => {
				const resolved = normalize(`${base}/${e}`);
				if (has(`${resolved}/index.ts`) || has(`${resolved}/index.js`))
					return [resolved.slice(resolved.lastIndexOf("/") + 1)];
				return has(resolved)
					? [stripExt(resolved.slice(resolved.lastIndexOf("/") + 1))]
					: [];
			});
		else names = index ? [dir] : [];
		const main =
			index ??
			(has(`${base}/package.json`) ? `${base}/package.json` : list[0]) ??
			base;
		const entry = { location: `${base}/`, paths: list.sort(), main };
		if (names.length === 0) out.push({ id: base, name: dir, ...entry });
		for (const name of names)
			out.push({
				id: `${base}#${name}`,
				name,
				key: extensionModuleKey(name),
				...entry,
			});
	}
	return out.sort((a, b) => a.id.localeCompare(b.id));
}

export function hookEntries(paths: readonly string[]): CodeEntry[] {
	return paths
		.map((p) => ({ p, key: hookKeyFromPath(p) }))
		.filter((x): x is { p: string; key: string } => x.key !== undefined)
		.map(({ p, key }) => ({
			id: p,
			name: p.slice(p.lastIndexOf("/") + 1),
			key,
			location: p,
			paths: [p],
			main: p,
		}))
		.sort((a, b) => a.id.localeCompare(b.id));
}

export function toolEntries(
	paths: readonly string[],
	texts: ReadonlyMap<string, string | null>,
): CodeEntry[] {
	const { files, dirs } = topLevel(paths, "tools");
	const out: CodeEntry[] = [];
	for (const p of files) {
		const file = p.slice("tools/".length);
		const ext = file.slice(file.lastIndexOf(".") + 1);
		if (!file.includes(".") || !TOOL_EXTENSIONS.includes(ext)) continue;
		let name = stripExt(file);
		const text = texts.get(p);
		if (text && ext === "json") {
			try {
				const data: unknown = JSON.parse(text);
				if (
					data &&
					typeof data === "object" &&
					"name" in data &&
					typeof data.name === "string" &&
					data.name
				)
					name = data.name;
			} catch {
				// 不合法的 JSON：Core 同样退回文件名
			}
		} else if (text && ext === "md") {
			const declared = parseFrontmatter(text).data.name;
			if (typeof declared === "string" && declared) name = declared;
		}
		out.push({
			id: p,
			name,
			key: toolKey(name),
			location: p,
			paths: [p],
			main: p,
		});
	}
	for (const [dir, list] of dirs) {
		const base = `tools/${dir}`;
		const index = `${base}/index.ts`;
		const loaded = list.includes(index);
		out.push({
			id: base,
			name: dir,
			key: loaded ? toolKey(dir) : undefined,
			location: `${base}/`,
			paths: list.sort(),
			main: loaded ? index : (list[0] ?? base),
		});
	}
	return out.sort((a, b) => a.id.localeCompare(b.id));
}

const NEW_FILE_PATTERN: Record<Section, RegExp> = {
	extensions: /^[A-Za-z0-9][A-Za-z0-9._-]*\.(ts|js)$/,
	hooks: /^(\*|[A-Za-z0-9][A-Za-z0-9_-]*)\.[A-Za-z0-9]+$/,
	tools: /^[A-Za-z0-9][A-Za-z0-9._-]*\.(json|md|ts|js|sh|bash|py)$/,
};

/** 目录式扩展 / 工具的目录名。 */
const DIR_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/** 可以整目录导入的分组（hook 只认单文件）。 */
const IMPORT_KIND: Partial<Record<Section, ImportKind>> = {
	extensions: "extension",
	tools: "tool",
};

export default function ExtensionsPage() {
	const { t } = useTranslation();
	const files = useAgentFiles();
	const config = useOmpConfig();
	const paths = useMemo(
		() => (files.data ?? []).map((f) => f.path),
		[files.data],
	);
	// 名字要从内容里取的文件：扩展子目录的 package.json、工具的 json / md
	const needText = useMemo(
		() =>
			paths.filter(
				(p) =>
					/^extensions\/[^/.][^/]*\/package\.json$/.test(p) ||
					/^tools\/[^/.][^/]*\.(json|md)$/.test(p),
			),
		[paths],
	);
	const { texts, error: textError } = useAgentTexts(needText);
	const [editing, setEditing] = useState<CodeEntry | null>(null);
	const [creating, setCreating] = useState<Section | null>(null);
	const [newPath, setNewPath] = useState<string | null>(null);
	const [deleting, setDeleting] = useState<CodeEntry | null>(null);
	const [importing, setImporting] = useState<Section | null>(null);

	const sections: {
		id: Section;
		icon: IconName;
		entries: CodeEntry[];
	}[] = [
		{
			id: "extensions",
			icon: "plugs",
			entries: extensionEntries(paths, texts),
		},
		{ id: "hooks", icon: "webhook", entries: hookEntries(paths) },
		{ id: "tools", icon: "wrench", entries: toolEntries(paths, texts) },
	];

	const row = (e: CodeEntry, icon: IconName): ReactNode => {
		const key = e.key;
		return (
			<GroupRow
				key={e.id}
				icon={icon}
				title={
					<>
						<span className="min-w-0 truncate">{e.name}</span>
						{key === undefined && (
							<Badge tone="warn" icon="alert">
								{t("agentPages.code.notLoaded")}
							</Badge>
						)}
					</>
				}
				meta={<span className="truncate font-mono">{e.location}</span>}
				trailing={
					<>
						<Reveal>
							<IconButton
								icon="code"
								box="sm"
								size={14}
								label={t("agentPages.code.open", { name: e.name })}
								onClick={() => setEditing(e)}
							/>
							<IconButton
								icon="trash"
								box="sm"
								size={14}
								label={t("agentPages.delete", { name: e.name })}
								onClick={() => setDeleting(e)}
							/>
						</Reveal>
						{key !== undefined && (
							<Toggle
								hideLabel
								checked={!config.disabled.has(key)}
								disabled={config.doc === undefined}
								onChange={(on) =>
									void config.mutate((doc) => toggleDisabled(doc, key, on))
								}
								label={t("agentPages.enable", { name: e.name })}
							/>
						)}
					</>
				}
			/>
		);
	};

	return (
		<>
			<AgentPageHead />
			<GenerativeUiGroup />
			<Callout tone="warn" icon="restart">
				{t("agentPages.code.restart")}
			</Callout>
			{sections.map((s) => (
				<Group
					key={s.id}
					title={t(`agentPages.code.${s.id}.title`)}
					action={
						<div className="flex items-center gap-1.5">
							{IMPORT_KIND[s.id] && (
								<Button
									size="sm"
									icon="upload"
									aria-label={t("agentPages.import.titleShort", {
										section: t(`agentPages.code.${s.id}.title`),
									})}
									onClick={() => setImporting(s.id)}
								>
									{t("agentPages.import.action")}
								</Button>
							)}
							<Button
								size="sm"
								icon="plus"
								aria-label={t("agentPages.code.newIn", {
									section: t(`agentPages.code.${s.id}.title`),
								})}
								onClick={() => setCreating(s.id)}
							>
								{t("agentPages.new")}
							</Button>
						</div>
					}
				>
					{files.error ? (
						<ErrorText error={files.error} />
					) : files.data === undefined ? (
						<Spinner />
					) : s.entries.length === 0 ? (
						<EmptyPanel>{t(`agentPages.code.${s.id}.empty`)}</EmptyPanel>
					) : (
						<GroupList>{s.entries.map((e) => row(e, s.icon))}</GroupList>
					)}
					<GroupNote>{t(`agentPages.code.${s.id}.hint`)}</GroupNote>
				</Group>
			))}
			{textError ? <ErrorText error={textError} /> : null}
			<AgentErrorText error={config.mutateError} />
			{editing &&
				(editing.location.endsWith("/") ? (
					<EntryFilesDialog
						title={editing.name}
						root={editing.location.slice(0, -1)}
						main={editing.main}
						locked={editing.key === undefined ? undefined : editing.main}
						onClose={() => setEditing(null)}
					/>
				) : (
					<TextFileDialog
						path={editing.main}
						title={editing.name}
						onClose={() => setEditing(null)}
					/>
				))}
			{importing && IMPORT_KIND[importing] && (
				<EntryImportDialog
					kind={IMPORT_KIND[importing]}
					sectionTitle={t(`agentPages.code.${importing}.title`)}
					nameHint={t("agentPages.code.dirNameHint")}
					onClose={() => setImporting(null)}
				/>
			)}
			{newPath && (
				<TextFileDialog
					path={newPath}
					title={newPath}
					onClose={() => setNewPath(null)}
				/>
			)}
			{creating && (
				<NewFileDialog
					section={creating}
					taken={paths}
					onClose={() => setCreating(null)}
					onPick={(p) => {
						setCreating(null);
						setNewPath(p);
					}}
				/>
			)}
			{deleting && (
				<ConfirmDeleteDialog
					name={deleting.name}
					paths={deleting.paths}
					onClose={() => setDeleting(null)}
					onDeleted={() => {
						const key = deleting.key;
						if (key && config.disabled.has(key))
							void config.mutate((doc) => toggleDisabled(doc, key, true));
					}}
				/>
			)}
		</>
	);
}

/** 生成式 UI 总开关（M33）：agent 能否用 render_ui / render_html 在对话里给出视图。下次启动 agent 时生效。 */
function GenerativeUiGroup(): ReactNode {
	const { t } = useTranslation();
	const settings = useGenerativeUiSettings();
	const set = useSetGenerativeUi();
	return (
		<Group title={t("agentPages.code.views.title")}>
			<GroupList>
				<GroupRow
					icon="views"
					title={t("agentPages.code.views.label")}
					meta={t("agentPages.code.views.hint")}
					trailing={
						settings.isPending ? (
							<Spinner />
						) : settings.isError ? undefined : (
							<Toggle
								hideLabel
								checked={settings.data.enabled}
								onChange={(enabled) => set.mutate(enabled)}
								label={t("agentPages.code.views.label")}
							/>
						)
					}
				>
					{settings.error || set.error ? (
						<ErrorText error={settings.error ?? set.error} />
					) : undefined}
				</GroupRow>
			</GroupList>
		</Group>
	);
}

/** 新建：先定文件名（hook 另选 pre / post；扩展与工具可选建目录，入口为 index.ts），再进编辑器。 */
function NewFileDialog({
	section,
	taken,
	onClose,
	onPick,
}: {
	section: Section;
	taken: readonly string[];
	onClose(): void;
	onPick(path: string): void;
}) {
	const { t } = useTranslation();
	const [name, setName] = useState("");
	const [hookType, setHookType] = useState<"pre" | "post">("pre");
	const [layout, setLayout] = useState<"file" | "dir">("file");
	const [touched, setTouched] = useState(false);
	const trimmed = name.trim();
	const asDir = section !== "hooks" && layout === "dir";
	const path =
		section === "hooks"
			? `hooks/${hookType}/${trimmed}`
			: asDir
				? `${section}/${trimmed}/index.ts`
				: `${section}/${trimmed}`;
	const dirPath = `${section}/${trimmed}`;
	const error = !(asDir ? DIR_NAME : NEW_FILE_PATTERN[section]).test(trimmed)
		? t(
				asDir
					? "agentPages.code.dirNameHint"
					: `agentPages.code.${section}.nameHint`,
			)
		: (
					asDir
						? taken.some((p) => p === dirPath || p.startsWith(`${dirPath}/`))
						: taken.includes(path)
				)
			? t("agentPages.errors.nameTaken")
			: null;
	const submit = () => {
		setTouched(true);
		if (!error) onPick(path);
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("agentPages.code.newIn", {
				section: t(`agentPages.code.${section}.title`),
			})}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={touched && error !== null}
						onClick={submit}
					>
						{t("agentPages.code.continue")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				{section !== "hooks" && (
					<FormField label={t("agentPages.code.layout")}>
						{(p) => (
							<Select<"file" | "dir">
								{...p}
								value={layout}
								options={[
									{ value: "file", label: t("agentPages.code.layoutFile") },
									{ value: "dir", label: t("agentPages.code.layoutDir") },
								]}
								onChange={setLayout}
							/>
						)}
					</FormField>
				)}
				{section === "hooks" && (
					<FormField label={t("agentPages.code.hookType")}>
						{(p) => (
							<Select<"pre" | "post">
								{...p}
								value={hookType}
								options={[
									{ value: "pre", label: t("agentPages.code.hookPre") },
									{ value: "post", label: t("agentPages.code.hookPost") },
								]}
								onChange={setHookType}
							/>
						)}
					</FormField>
				)}
				<Field
					label={t(
						asDir ? "agentPages.code.dirName" : "agentPages.code.fileName",
					)}
					hint={t(
						asDir
							? "agentPages.code.dirNameHint"
							: `agentPages.code.${section}.nameHint`,
					)}
				>
					<TextInput
						value={name}
						autoFocus
						aria-invalid={touched && error !== null}
						onChange={(e) => setName(e.target.value)}
					/>
				</Field>
				{touched && error && <p className="text-err text-xs">{error}</p>}
			</form>
		</Dialog>
	);
}
