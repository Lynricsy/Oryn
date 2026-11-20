/**
 * 项目表单：名称、仓库、图标、基线分支、默认 provider / orb 类型 / 浏览器 Profile、浅克隆、setup / resume 脚本、
 * 「仓库内脚本：有 / 无」、git 提交身份覆盖；编辑 GitHub 项目时还有推送 / 开 PR 门禁区（M15，`ProjectGates`）。
 * 新建发 POST，编辑发 PATCH（空串 = 清除；门禁只在改过时随 `githubGates` 发出）；响应体就是保存后的项目。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Project, type Schemas, unwrap } from "../../api/client";
import type { GitHubGates } from "../../api/github-queries";
import {
	qk,
	useBrowserProfiles,
	useProjects,
	useProviders,
} from "../../api/queries";
import { GlyphGrid } from "../../components/settings/GlyphGrid";
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
} from "../../components/ui/primitives";
import type { SetupRun } from "../../lib/env-banner";
import { PROJECT_GLYPHS } from "../../lib/glyphs";
import { SnapshotsToggle, WarmOrbsStepper } from "./ProjectAccel";
import { DEFAULT_GATES, normalizeGates, ProjectGates } from "./ProjectGates";
import { RepoPicker } from "./RepoPicker";

export type ProjectForm = Schemas["ProjectCreate"];

export const EMPTY_PROJECT: ProjectForm = {
	name: "",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 0,
};

export function formOf(p: Project): ProjectForm {
	return {
		name: p.name,
		repoUrl: p.repoUrl,
		baseBranch: p.baseBranch,
		defaultProvider: p.defaultProvider,
		defaultOrbType: p.defaultOrbType,
		setupScript: p.setupScript,
		resumeScript: p.resumeScript,
		shallowClone: p.shallowClone,
		color: p.color,
		icon: p.icon,
		gitAuthorName: p.gitAuthorName,
		gitAuthorEmail: p.gitAuthorEmail,
		defaultBrowserProfileId: p.defaultBrowserProfileId,
		snapshots: p.snapshots,
		warmOrbs: p.warmOrbs,
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

/** 项目图标：当前徽标 + 「自动」（清空，之后由首个线程标题卡的图标自动填入）+ 精选字形网格。 */
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
		<div className="flex flex-col gap-2 sm:col-span-2">
			<span className="font-medium text-muted text-xs">
				{t("projects.icon")}
			</span>
			<div className="flex flex-wrap items-center gap-3">
				<ProjectBadge name={name || "?"} icon={icon} size={32} />
				<Button
					size="sm"
					variant={icon ? "ghost" : "secondary"}
					aria-pressed={!icon}
					onClick={() => onChange(undefined)}
				>
					{t("projects.iconAuto")}
				</Button>
				<span className="min-w-0 flex-1 text-faint text-xs">
					{t("projects.iconAutoHintShort")}
				</span>
			</div>
			<GlyphGrid
				glyphs={PROJECT_GLYPHS}
				value={icon}
				label={t("projects.icon")}
				onPick={(c) => onChange(c)}
			/>
		</div>
	);
}

export function ProjectEditor({
	initial,
	id,
	lastSetup,
	onSaved,
	onCancel,
}: {
	initial: ProjectForm;
	/** 有则编辑（PATCH），无则新建（POST） */
	id?: string;
	lastSetup?: SetupRun;
	onSaved: (project: Project) => void;
	onCancel: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const providers = useProviders();
	const profiles = useBrowserProfiles();
	const [f, setF] = useState<ProjectForm>(initial);
	const save = useAsync();
	const set = <K extends keyof ProjectForm>(k: K, v: ProjectForm[K]) =>
		setF((prev) => ({ ...prev, [k]: v }));
	// 门禁只对已存在的 GitHub 项目开放；null = 没改过，保存时不发 `githubGates`
	const project = useProjects().data?.projects.find((p) => p.id === id);
	const [gates, setGates] = useState<GitHubGates | null>(null);
	const provider = providers.data?.providers.find(
		(p) => p.name === f.defaultProvider,
	);
	// 身份覆盖须两项同时填写或同时留空（服务端同样校验）。
	const gitPairOk = !f.gitAuthorName?.trim() === !f.gitAuthorEmail?.trim();
	return (
		<form
			className="grid gap-3 sm:grid-cols-2"
			onSubmit={async (e) => {
				e.preventDefault();
				let saved: Project | undefined;
				const ok = await save.run(async () => {
					if (id) {
						const patch: Schemas["ProjectPatch"] = {
							...f,
							repoUrl: f.repoUrl ?? "",
							baseBranch: f.baseBranch ?? "",
							defaultProvider: f.defaultProvider ?? "",
							defaultOrbType: f.defaultOrbType ?? "",
							setupScript: f.setupScript ?? "",
							resumeScript: f.resumeScript ?? "",
							color: f.color ?? "",
							// 空串 = 清除，回到自动采用
							icon: f.icon ?? "",
							gitAuthorName: f.gitAuthorName ?? "",
							gitAuthorEmail: f.gitAuthorEmail ?? "",
							// 空串 = 清除默认 Profile
							defaultBrowserProfileId: f.defaultBrowserProfileId ?? "",
							...(gates ? { githubGates: normalizeGates(gates) } : {}),
						};
						saved = await unwrap(
							api.PATCH("/api/v1/projects/{id}", {
								params: { path: { id } },
								body: patch,
							}),
						);
					} else
						saved = await unwrap(api.POST("/api/v1/projects", { body: f }));
					await qc.invalidateQueries({ queryKey: qk.projects });
				});
				if (ok && saved) onSaved(saved);
			}}
		>
			<Field label={t("projects.name")}>
				<TextInput
					required
					value={f.name}
					onChange={(e) => set("name", e.target.value)}
				/>
			</Field>
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
			<ProjectIconPicker
				name={f.name}
				icon={f.icon}
				onChange={(icon) => set("icon", icon)}
			/>
			<Field label={t("projects.baseBranch")}>
				<TextInput
					placeholder="main"
					value={f.baseBranch ?? ""}
					onChange={(e) => set("baseBranch", e.target.value || undefined)}
				/>
			</Field>
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
			{id && (
				<p className="text-muted text-xs sm:col-span-2">
					<RepoScriptText run={lastSetup} />
				</p>
			)}
			<div className="sm:col-span-2">
				<Field label={t("projects.resumeScript")}>
					<TextArea
						rows={3}
						value={f.resumeScript ?? ""}
						onChange={(e) => set("resumeScript", e.target.value || undefined)}
					/>
				</Field>
			</div>
			<Field label={t("projects.gitAuthorName")}>
				<TextInput
					autoComplete="off"
					value={f.gitAuthorName ?? ""}
					onChange={(e) => set("gitAuthorName", e.target.value || undefined)}
				/>
			</Field>
			<Field label={t("projects.gitAuthorEmail")}>
				<TextInput
					type="email"
					autoComplete="off"
					value={f.gitAuthorEmail ?? ""}
					onChange={(e) => set("gitAuthorEmail", e.target.value || undefined)}
				/>
			</Field>
			<p
				className={`text-xs sm:col-span-2 ${gitPairOk ? "text-muted" : "text-err"}`}
			>
				{gitPairOk ? t("projects.gitHint") : t("projects.gitPairError")}
			</p>
			{id && project?.github && (
				<div className="border-border border-t pt-3 sm:col-span-2">
					<ProjectGates
						value={gates ?? project.githubGates ?? DEFAULT_GATES}
						onChange={setGates}
					/>
				</div>
			)}
			<div className="flex gap-2 sm:col-span-2">
				<Button
					type="submit"
					variant="primary"
					disabled={save.busy || !f.name.trim() || !gitPairOk}
				>
					{t("common.save")}
				</Button>
				<Button onClick={onCancel}>{t("common.cancel")}</Button>
			</div>
			<ErrorText error={save.error} />
		</form>
	);
}
