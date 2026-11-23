/**
 * 项目表单（M24 起按 `section` 拆开）：
 * - `create`（新建页）：名称、仓库来源（已有仓库 / 空白仓库（Oryn 托管）/ 不带仓库）、图标、描述、仓库地址（可从 GitHub
 *   选择）、基线分支；发 POST，只带填了的字段，其余由服务端补默认值；
 * - `overview`（设置「概览」）：同上（没有来源选择；托管项目不显示仓库地址），另有 git 提交身份覆盖；
 * - `environment`（设置「环境」）：默认 provider / orb 类型 / 浏览器 Profile、浅克隆、快照与预热、setup / resume 脚本与「仓库内脚本：有 / 无」。
 * 编辑发 PATCH，只带本分组的字段（空串 = 清除）；服务端只改出现的字段，所以两组分开保存互不影响。响应体就是保存后的项目。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Project, type Schemas, unwrap } from "../../api/client";
import { qk, useBrowserProfiles, useProviders } from "../../api/queries";
import {
	type FeaturedGlyphs,
	GlyphPicker,
} from "../../components/settings/GlyphPicker";
import { useAsync } from "../../components/settings/useAsync";
import { ProjectBadge } from "../../components/ui/data";
import { typeHasDocker } from "../../components/ui/OrbBadges";
import {
	Button,
	ErrorText,
	Field,
	Select,
	TextArea,
	TextInput,
	Toggle,
	tabClass,
} from "../../components/ui/primitives";
import type { SetupRun } from "../../lib/env-banner";
import { PROJECT_GLYPHS } from "../../lib/glyphs";
import { SnapshotsToggle, WarmOrbsStepper } from "./ProjectAccel";
import { RepoPicker } from "./RepoPicker";

export type ProjectForm = Schemas["ProjectCreate"];

/** 表单分组：新建页、设置「概览」、设置「环境」。 */
export type ProjectSection = "create" | "overview" | "environment";

export const EMPTY_PROJECT: ProjectForm = { name: "", shallowClone: false };

/** 新建页的仓库来源（M24 阶段 ③）：已有仓库（填地址）/ 空白仓库（Oryn 托管）/ 不带仓库。 */
export type RepoSource = "existing" | "hosted" | "none";
const REPO_SOURCES: readonly RepoSource[] = ["existing", "hosted", "none"];

export function formOf(p: Project): ProjectForm {
	return {
		name: p.name,
		description: p.description,
		repoUrl: p.repoUrl,
		baseBranch: p.baseBranch,
		defaultProvider: p.defaultProvider,
		defaultOrbType: p.defaultOrbType,
		setupScript: p.setupScript,
		resumeScript: p.resumeScript,
		shallowClone: p.shallowClone,
		icon: p.icon,
		gitAuthorName: p.gitAuthorName,
		gitAuthorEmail: p.gitAuthorEmail,
		defaultBrowserProfileId: p.defaultBrowserProfileId,
		snapshots: p.snapshots,
		warmOrbs: p.warmOrbs,
		hosted: p.hosted,
	};
}

/** 去掉空白后为空的字段视为未填。 */
const filled = (v: string | undefined): v is string => !!v?.trim();

/**
 * 新建：名称与浅克隆必填，其余只带填了的（服务端补默认值，图标留空以后自动采用）。
 * 托管来源加 `hosted: true` 且不带仓库地址；不带仓库时仓库地址与基线分支都不发。
 */
function createBody(f: ProjectForm, source: RepoSource): ProjectForm {
	const body: ProjectForm = { name: f.name, shallowClone: false };
	if (filled(f.icon)) body.icon = f.icon;
	if (filled(f.description)) body.description = f.description;
	if (source === "hosted") body.hosted = true;
	if (source === "existing" && filled(f.repoUrl)) body.repoUrl = f.repoUrl;
	if (source !== "none" && filled(f.baseBranch)) body.baseBranch = f.baseBranch;
	return body;
}

/** 编辑：只发本分组的字段，未填一律发空串（清除）。 */
function patchBody(
	section: Exclude<ProjectSection, "create">,
	f: ProjectForm,
): Schemas["ProjectPatch"] {
	if (section === "overview")
		return {
			name: f.name,
			description: f.description ?? "",
			// 托管项目不能改用外部仓库地址：不发 repoUrl
			...(f.hosted ? {} : { repoUrl: f.repoUrl ?? "" }),
			baseBranch: f.baseBranch ?? "",
			// 空串 = 清除，回到自动采用
			icon: f.icon ?? "",
			gitAuthorName: f.gitAuthorName ?? "",
			gitAuthorEmail: f.gitAuthorEmail ?? "",
		};
	return {
		defaultProvider: f.defaultProvider ?? "",
		defaultOrbType: f.defaultOrbType ?? "",
		// 空串 = 清除默认 Profile
		defaultBrowserProfileId: f.defaultBrowserProfileId ?? "",
		setupScript: f.setupScript ?? "",
		resumeScript: f.resumeScript ?? "",
		shallowClone: f.shallowClone,
		snapshots: f.snapshots ?? true,
		warmOrbs: f.warmOrbs ?? 0,
	};
}

/** 「仓库内脚本：有 / 无」：取自该项目最近一次 setup 运行；无记录时为「未知」。 */
export function RepoScriptText({
	run,
}: {
	run: SetupRun | undefined;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{t("env.repoScript", {
				value: run
					? t(run.hasRepoScript ? "env.repoScriptYes" : "env.repoScriptNo")
					: t("env.repoScriptUnknown"),
			})}
		</>
	);
}

const PROJECT_FEATURED: FeaturedGlyphs = { icons: PROJECT_GLYPHS, emoji: [] };

/**
 * 项目图标：点徽标打开字形弹层（常用精选 + 完整目录，可模糊搜索）；弹层里的「自动」清空图标，
 * 之后由首个线程标题卡的图标自动填入。
 */
function ProjectIconPicker({
	name,
	icon,
	onChange,
}: {
	name: string;
	icon?: string;
	onChange: (icon: string | undefined) => void;
}) {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-1.5 sm:col-span-2">
			<span className="font-medium text-muted text-xs">
				{t("projects.icon")}
			</span>
			<div className="flex items-center gap-3">
				<GlyphPicker
					value={icon}
					featured={PROJECT_FEATURED}
					label={t("projects.icon")}
					removeLabel={t("projects.iconAuto")}
					onRemove={() => onChange(undefined)}
					onPick={(c) => onChange(c)}
					triggerClassName="rounded-md p-1 transition-colors duration-150 hover:bg-hover aria-expanded:bg-hover"
				>
					<ProjectBadge name={name || "?"} icon={icon} size={40} />
				</GlyphPicker>
				{!icon && (
					<span className="min-w-0 flex-1 text-faint text-xs">
						{t("projects.iconAutoHint")}
					</span>
				)}
			</div>
		</div>
	);
}

type SetField = <K extends keyof ProjectForm>(k: K, v: ProjectForm[K]) => void;

/** 新建页的仓库来源分段按钮（M24 阶段 ③）；切换时由调用方清掉已填的仓库地址。 */
function RepoSourcePicker({
	value,
	onChange,
}: {
	value: RepoSource;
	onChange: (s: RepoSource) => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-1.5 sm:col-span-2">
			<span className="font-medium text-muted text-xs">
				{t("hostedRepo.source.label")}
			</span>
			{/* biome-ignore lint/a11y/useSemanticElements: 分段按钮（aria-pressed），不是表单字段集 */}
			<div
				role="group"
				aria-label={t("hostedRepo.source.label")}
				data-testid="repo-source"
				className="flex w-fit max-w-full flex-wrap items-center gap-0.5 rounded-md bg-inset p-0.5"
			>
				{REPO_SOURCES.map((s) => (
					<button
						key={s}
						type="button"
						data-source={s}
						aria-pressed={value === s}
						onClick={() => onChange(s)}
						className={`${tabClass(value === s)} h-6 ${value === s ? "bg-panel shadow-xs" : ""}`}
					>
						{t(`hostedRepo.source.${s}`)}
					</button>
				))}
			</div>
			{value === "hosted" && (
				<p className="text-faint text-xs">
					{t("hostedRepo.source.hostedHint")}
				</p>
			)}
		</div>
	);
}

/**
 * 新建与「概览」共用的字段：名称、描述、图标，然后是仓库一组：（新建页）仓库来源、仓库地址（独占一行，行尾「从 GitHub 选择」）、
 * 紧随其后的基线分支。托管来源 / 托管项目不显示仓库地址；「不带仓库」连基线分支也不显示。
 */
function GeneralFields({
	f,
	set,
	source,
	onSource,
}: {
	f: ProjectForm;
	set: SetField;
	/** 只有新建页给：仓库来源；缺省按项目是否托管判断 */
	source?: RepoSource;
	onSource?: (s: RepoSource) => void;
}): ReactNode {
	const { t } = useTranslation();
	const src: RepoSource = source ?? (f.hosted ? "hosted" : "existing");
	return (
		<>
			<div className="sm:col-span-2">
				<Field label={t("projects.name")}>
					<TextInput
						required
						value={f.name}
						onChange={(e) => set("name", e.target.value)}
					/>
				</Field>
			</div>
			<div className="sm:col-span-2">
				<Field
					label={t("projects.description")}
					hint={t("projects.descriptionHint")}
				>
					<TextInput
						maxLength={200}
						value={f.description ?? ""}
						onChange={(e) => set("description", e.target.value || undefined)}
					/>
				</Field>
			</div>
			<ProjectIconPicker
				name={f.name}
				icon={f.icon}
				onChange={(icon) => set("icon", icon)}
			/>
			{source && onSource && (
				<RepoSourcePicker value={source} onChange={onSource} />
			)}
			{src === "existing" && (
				<div className="sm:col-span-2">
					<Field label={t("projects.repoUrl")}>
						<div className="relative flex items-center gap-2">
							<TextInput
								className="min-w-0 flex-1"
								// 同一个 label 里还有「从 GitHub 选择」按钮：名字只取字段名
								aria-label={t("projects.repoUrl")}
								placeholder="https://github.com/…"
								value={f.repoUrl ?? ""}
								onChange={(e) => set("repoUrl", e.target.value || undefined)}
							/>
							<RepoPicker
								onPick={(r) => {
									set("repoUrl", `https://github.com/${r.fullName}`);
									if (!f.baseBranch) set("baseBranch", r.defaultBranch);
								}}
							/>
						</div>
					</Field>
				</div>
			)}
			{src !== "none" && (
				<Field label={t("projects.baseBranch")}>
					<TextInput
						placeholder="main"
						value={f.baseBranch ?? ""}
						onChange={(e) => set("baseBranch", e.target.value || undefined)}
					/>
				</Field>
			)}
		</>
	);
}

/** 「概览」另有的 git 提交身份覆盖（须两项同时填写或同时留空）。 */
function GitIdentityFields({
	f,
	set,
	pairOk,
}: {
	f: ProjectForm;
	set: SetField;
	pairOk: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{/* 名字与邮箱另起一行并排（基线分支只占左半行） */}
			<div className="sm:col-start-1">
				<Field label={t("projects.gitAuthorName")}>
					<TextInput
						autoComplete="off"
						value={f.gitAuthorName ?? ""}
						onChange={(e) => set("gitAuthorName", e.target.value || undefined)}
					/>
				</Field>
			</div>
			<Field label={t("projects.gitAuthorEmail")}>
				<TextInput
					type="email"
					autoComplete="off"
					value={f.gitAuthorEmail ?? ""}
					onChange={(e) => set("gitAuthorEmail", e.target.value || undefined)}
				/>
			</Field>
			<p
				className={`text-xs sm:col-span-2 ${pairOk ? "text-muted" : "text-err"}`}
			>
				{pairOk ? t("projects.gitHint") : t("projects.gitPairError")}
			</p>
		</>
	);
}

/** 「环境」：默认 provider / orb 类型 / 浏览器 Profile、浅克隆、快照与预热、脚本。 */
function EnvironmentFields({
	f,
	set,
	lastSetup,
}: {
	f: ProjectForm;
	set: SetField;
	lastSetup?: SetupRun;
}): ReactNode {
	const { t } = useTranslation();
	const providers = useProviders();
	const profiles = useBrowserProfiles();
	const provider = providers.data?.providers.find(
		(p) => p.name === f.defaultProvider,
	);
	return (
		<>
			<Field label={t("projects.defaultProvider")}>
				<Select
					value={f.defaultProvider ?? ""}
					onChange={(e) => set("defaultProvider", e.target.value || undefined)}
				>
					<option value="">{t("projects.inherit")}</option>
					{providers.data?.providers.map((p) => (
						<option key={p.id} value={p.name}>
							{p.name}
						</option>
					))}
				</Select>
			</Field>
			<Field label={t("projects.defaultOrbType")}>
				<Select
					value={f.defaultOrbType ?? ""}
					onChange={(e) => set("defaultOrbType", e.target.value || undefined)}
				>
					<option value="">{t("projects.inherit")}</option>
					{(provider?.orbTypes ?? []).map((ot) => (
						<option key={ot.name} value={ot.name}>
							{typeHasDocker(ot)
								? `${ot.name} · ${t("accel.docker")}`
								: ot.name}
						</option>
					))}
				</Select>
			</Field>
			<Field
				label={t("projects.defaultBrowserProfile")}
				hint={t("projects.defaultBrowserProfileHint")}
			>
				<Select
					value={f.defaultBrowserProfileId ?? ""}
					onChange={(e) =>
						set("defaultBrowserProfileId", e.target.value || undefined)
					}
				>
					<option value="">{t("common.none")}</option>
					{profiles.data?.profiles.map((p) => (
						<option key={p.id} value={p.id}>
							{p.name}
						</option>
					))}
				</Select>
			</Field>
			<div className="flex items-end">
				<Toggle
					checked={f.shallowClone}
					onChange={(v) => set("shallowClone", v)}
					label={t("projects.shallowClone")}
				/>
			</div>
			<div className="sm:col-span-2">
				<SnapshotsToggle
					checked={f.snapshots ?? true}
					onChange={(v) => set("snapshots", v)}
				/>
			</div>
			<div className="sm:col-span-2">
				<WarmOrbsStepper
					value={f.warmOrbs ?? 0}
					onChange={(v) => set("warmOrbs", v)}
				/>
			</div>
			<div className="sm:col-span-2">
				<Field label={t("projects.setupScript")} hint={t("projects.setupHint")}>
					<TextArea
						rows={4}
						value={f.setupScript ?? ""}
						onChange={(e) => set("setupScript", e.target.value || undefined)}
					/>
				</Field>
			</div>
			<p className="text-muted text-xs sm:col-span-2">
				<RepoScriptText run={lastSetup} />
			</p>
			<div className="sm:col-span-2">
				<Field label={t("projects.resumeScript")}>
					<TextArea
						rows={3}
						value={f.resumeScript ?? ""}
						onChange={(e) => set("resumeScript", e.target.value || undefined)}
					/>
				</Field>
			</div>
		</>
	);
}

export function ProjectEditor({
	section,
	initial,
	id,
	lastSetup,
	onSaved,
	onCancel,
}: {
	section: ProjectSection;
	initial: ProjectForm;
	/** 编辑的项目（`create` 以外必填） */
	id?: string;
	/** 「环境」分组的「仓库内脚本：有 / 无」 */
	lastSetup?: SetupRun;
	onSaved: (project: Project) => void;
	onCancel: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [f, setF] = useState<ProjectForm>(initial);
	const save = useAsync();
	const set: SetField = (k, v) => setF((prev) => ({ ...prev, [k]: v }));
	// 新建页的仓库来源；切换时清掉已填的仓库地址
	const [source, setSource] = useState<RepoSource>("existing");
	const pickSource = (s: RepoSource) => {
		setSource(s);
		set("repoUrl", undefined);
	};
	// 身份覆盖须两项同时填写或同时留空（服务端同样校验）。
	const gitPairOk =
		section !== "overview" ||
		!f.gitAuthorName?.trim() === !f.gitAuthorEmail?.trim();
	const nameOk = section === "environment" || !!f.name.trim();
	return (
		<form
			className="grid gap-3 sm:grid-cols-2"
			onSubmit={async (e) => {
				e.preventDefault();
				let saved: Project | undefined;
				const ok = await save.run(async () => {
					if (section === "create")
						saved = await unwrap(
							api.POST("/api/v1/projects", { body: createBody(f, source) }),
						);
					else {
						if (!id) throw new Error("ProjectEditor: 编辑需要项目 id");
						saved = await unwrap(
							api.PATCH("/api/v1/projects/{id}", {
								params: { path: { id } },
								body: patchBody(section, f),
							}),
						);
					}
					await qc.invalidateQueries({ queryKey: qk.projects });
				});
				if (ok && saved) onSaved(saved);
			}}
		>
			{section === "environment" ? (
				<EnvironmentFields f={f} set={set} lastSetup={lastSetup} />
			) : (
				<GeneralFields
					f={f}
					set={set}
					{...(section === "create" ? { source, onSource: pickSource } : {})}
				/>
			)}
			{section === "overview" && (
				<GitIdentityFields f={f} set={set} pairOk={gitPairOk} />
			)}
			<div className="flex gap-2 sm:col-span-2">
				<Button
					type="submit"
					variant="primary"
					disabled={save.busy || !nameOk || !gitPairOk}
				>
					{section === "create" ? t("common.create") : t("common.save")}
				</Button>
				<Button onClick={onCancel}>{t("common.cancel")}</Button>
			</div>
			<ErrorText error={save.error} />
		</form>
	);
}
