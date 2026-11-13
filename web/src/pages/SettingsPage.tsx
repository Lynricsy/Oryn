/**
 * 设置页：设备（列表、吊销、批准配对）、provider（新建并一次性显示令牌、类型、在线状态）、项目（表单与图标）、
 * 密钥（名称列表、写入值）、Git 身份（orb 内提交身份的全局默认值）、agent 配置（与线程 Files 面板同一个文件浏览器 + CodeMirror 编辑器，不做表单化）、
 * 外观（明暗、主题色、agent 头像）、关于。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type ChangeEvent,
	type KeyboardEvent,
	type ReactNode,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import { api, type Project, type Schemas, unwrap } from "../api/client";
import {
	qk,
	useAgentIdentity,
	useDevices,
	useGitIdentity,
	useHealth,
	useMe,
	useOrbs,
	useProjects,
	useProviders,
	useSecrets,
} from "../api/queries";
import { FileBrowser } from "../components/files/FileBrowser";
import { PairApproval } from "../components/PairApproval";
import { AgentAvatar } from "../components/ui/AgentAvatar";
import { CopyButton } from "../components/ui/CopyButton";
import {
	Badge,
	Callout,
	Card,
	KeyValue,
	ListRow,
	ProjectBadge,
	Sheet,
	StatusDot,
} from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import {
	Button,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../components/ui/primitives";
import { AvatarTooLargeError, avatarDataUrl } from "../lib/avatar-image";
import { latestSetupByProject, type SetupRun } from "../lib/env-banner";
import { agentConfigSource } from "../lib/file-source";
import { formatDateTime, formatMemoryMB, formatRelative } from "../lib/format";
import {
	AVATAR_GLYPHS,
	type Glyph,
	isSingleGlyph,
	PROJECT_GLYPHS,
} from "../lib/glyphs";
import { useBreakpoint } from "../lib/viewport";
import {
	ACCENTS,
	type AccentName,
	MODES,
	type ThemeMode,
	useTheme,
} from "../state/theme";
import { uiStore } from "../state/ui";

export type SettingsSection =
	| "devices"
	| "providers"
	| "projects"
	| "secrets"
	| "git-identity"
	| "agent-config"
	| "appearance"
	| "about";

const SECTIONS: { key: SettingsSection; icon: IconName }[] = [
	{ key: "devices", icon: "device" },
	{ key: "providers", icon: "box" },
	{ key: "projects", icon: "folder" },
	{ key: "secrets", icon: "key" },
	{ key: "git-identity", icon: "fork" },
	{ key: "agent-config", icon: "file" },
	{ key: "appearance", icon: "palette" },
	{ key: "about", icon: "info" },
];

function useAsync() {
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const run = async (fn: () => Promise<unknown>) => {
		setBusy(true);
		setError(null);
		try {
			await fn();
			return true;
		} catch (e) {
			setError(e);
			return false;
		} finally {
			setBusy(false);
		}
	};
	return { busy, error, run };
}

/** 卡片内的列表：行自带内边距，向两侧各收 2 个单位与卡片标题对齐。 */
function RowList({ children }: { children: ReactNode }) {
	return <ul className="-mx-2 flex flex-col">{children}</ul>;
}

/** 列表行前置的细描边图标方块。 */
function LeadIcon({ name }: { name: IconName }) {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name={name} size={15} />
		</span>
	);
}

/** 单选组的方向键：在组内的 `role=radio` 之间移动焦点并选中。 */
function radioKeys(e: KeyboardEvent<HTMLElement>) {
	const step: Record<string, number> = {
		ArrowRight: 1,
		ArrowDown: 1,
		ArrowLeft: -1,
		ArrowUp: -1,
	};
	const d = step[e.key];
	if (!d) return;
	const radios = Array.from(
		e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]'),
	);
	const i = radios.indexOf(document.activeElement as HTMLButtonElement);
	if (i < 0) return;
	e.preventDefault();
	const next = radios[(i + d + radios.length) % radios.length];
	next?.focus();
	next?.click();
}

/** 字形选择网格：项目图标与 agent 头像共用。 */
function GlyphGrid({
	glyphs,
	value,
	label,
	disabled,
	onPick,
}: {
	glyphs: readonly Glyph[];
	value?: string;
	label: string;
	disabled?: boolean;
	onPick: (char: string) => void;
}) {
	return (
		<fieldset
			aria-label={label}
			className="m-0 grid min-w-0 grid-cols-[repeat(auto-fill,minmax(2rem,1fr))] gap-1 border-0 p-0"
		>
			{glyphs.map((g) => {
				const on = g.char === value;
				return (
					<button
						key={g.char}
						type="button"
						title={g.label}
						aria-label={g.label}
						aria-pressed={on}
						disabled={disabled}
						onClick={() => onPick(g.char)}
						className={`grid h-8 place-items-center rounded-md border text-lg leading-none transition-colors duration-150 disabled:opacity-50 ${on ? "border-accent bg-accent-soft text-accent-ink" : "border-transparent text-muted hover:bg-hover hover:text-fg"}`}
					>
						{g.char}
					</button>
				);
			})}
		</fieldset>
	);
}

// ---------------------------------------------------------------- 设备

const DEVICE_ICON: Record<Schemas["Device"]["kind"], IconName> = {
	cli: "terminal",
	desktop: "monitor",
	mobile: "device",
};

function DevicesSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const devices = useDevices();
	const revoke = useAsync();
	return (
		<>
			<Card title={t("devices.approveTitle")} icon="key">
				<p className="mb-3 text-muted text-xs">{t("pair.hint")}</p>
				<PairApproval />
			</Card>
			<Card title={t("devices.title")} icon="device">
				{devices.isPending && <Spinner />}
				<ErrorText error={devices.error ?? revoke.error} />
				<RowList>
					{devices.data?.devices.map((d) => (
						<li key={d.id}>
							<ListRow
								leading={<LeadIcon name={DEVICE_ICON[d.kind] ?? "device"} />}
								title={
									<>
										<span className="truncate font-medium">{d.name}</span>
										{d.current && (
											<Badge tone="accent">{t("devices.current")}</Badge>
										)}
										{d.revokedAt && (
											<Badge tone="err">{t("devices.revoked")}</Badge>
										)}
									</>
								}
								meta={
									<>
										<Badge>{t(`devices.kind.${d.kind}`)}</Badge>
										{d.platform && <Badge>{d.platform}</Badge>}
										<span>
											{t("devices.lastSeen", {
												when: d.lastSeenAt ? formatRelative(d.lastSeenAt) : "—",
											})}
										</span>
									</>
								}
								trailing={
									!d.revokedAt && (
										<Button
											size="sm"
											variant="danger"
											disabled={revoke.busy}
											onClick={() => {
												if (
													!window.confirm(
														t("devices.revokeConfirm", { name: d.name }),
													)
												)
													return;
												void revoke
													.run(() =>
														unwrap(
															api.DELETE("/api/v1/devices/{id}", {
																params: { path: { id: d.id } },
															}),
														),
													)
													.then(() =>
														qc.invalidateQueries({ queryKey: qk.devices }),
													);
											}}
										>
											{t("devices.revoke")}
										</Button>
									)
								}
							/>
						</li>
					))}
				</RowList>
				{devices.data?.devices.length === 0 && (
					<p className="text-faint text-sm">{t("devices.empty")}</p>
				)}
			</Card>
		</>
	);
}

// ---------------------------------------------------------------- provider

/** provider 能力：支持为绿底勾、不支持为中性叉；读屏另读「支持 / 不支持」。 */
function Capability({ on, label }: { on: boolean; label: string }) {
	const { t } = useTranslation();
	const state = on ? t("providers.supported") : t("providers.unsupported");
	return (
		<Badge
			tone={on ? "ok" : "neutral"}
			icon={on ? "check" : "close"}
			title={state}
		>
			{label}
			<span className="sr-only">{`：${state}`}</span>
		</Badge>
	);
}

function ProvidersSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const providers = useProviders();
	const [name, setName] = useState("");
	const [token, setToken] = useState<string | null>(null);
	const create = useAsync();
	const del = useAsync();
	return (
		<>
			<Card title={t("providers.create")} icon="plus">
				<form
					className="flex flex-wrap gap-2"
					onSubmit={async (e) => {
						e.preventDefault();
						await create.run(async () => {
							const res = await unwrap(
								api.POST("/api/v1/providers", { body: { name: name.trim() } }),
							);
							setToken(res.token);
							setName("");
							await qc.invalidateQueries({ queryKey: qk.providers });
						});
					}}
				>
					<TextInput
						className="max-w-60"
						placeholder={t("providers.namePlaceholder")}
						value={name}
						onChange={(e) => setName(e.target.value)}
					/>
					<Button
						type="submit"
						variant="primary"
						disabled={create.busy || !name.trim()}
					>
						{t("common.create")}
					</Button>
				</form>
				{token && (
					<div className="mt-3">
						<Callout tone="warn" title={t("providers.tokenOnce")}>
							<div className="mt-1.5 flex items-center gap-2">
								<code className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
									{token}
								</code>
								<CopyButton text={token} />
							</div>
						</Callout>
					</div>
				)}
				<ErrorText error={create.error} />
			</Card>
			<Card title={t("providers.title")} icon="box">
				{providers.isPending && <Spinner />}
				<ErrorText error={providers.error ?? del.error} />
				<RowList>
					{providers.data?.providers.map((p) => (
						<li key={p.id}>
							<ListRow
								leading={
									<span className="grid h-8 w-8 place-items-center">
										<StatusDot tone={p.online ? "ok" : "neutral"} />
									</span>
								}
								title={
									<>
										<span className="truncate font-medium">{p.name}</span>
										{p.driver && <Badge icon="box">{p.driver}</Badge>}
									</>
								}
								meta={
									<>
										<span>
											{p.online
												? t("providers.online")
												: t("providers.offline")}
										</span>
										{p.orbTypes.map((ot) => {
											const isDefault = p.defaultType === ot.name;
											return (
												<span
													key={ot.name}
													className="inline-flex items-center gap-0.5"
												>
													<Badge tone={isDefault ? "accent" : "neutral"}>
														{ot.name}
													</Badge>
													<Badge icon="cpu">
														{t("panel.cores", { n: ot.cpus })}
													</Badge>
													<Badge icon="memory">
														{formatMemoryMB(ot.memoryMB)}
													</Badge>
													{isDefault && (
														<Badge tone="accent">
															{t("providers.default")}
														</Badge>
													)}
												</span>
											);
										})}
										{p.capabilities && (
											<>
												<Capability
													on={p.capabilities.freeze}
													label={t("providers.freeze")}
												/>
												<Capability
													on={p.capabilities.snapshot}
													label={t("providers.snapshot")}
												/>
											</>
										)}
									</>
								}
								trailing={
									<IconButton
										icon="trash"
										label={t("common.delete")}
										onClick={() => {
											if (
												!window.confirm(
													t("providers.deleteConfirm", { name: p.name }),
												)
											)
												return;
											void del
												.run(() =>
													unwrap(
														api.DELETE("/api/v1/providers/{id}", {
															params: { path: { id: p.id } },
														}),
													),
												)
												.then(() =>
													qc.invalidateQueries({ queryKey: qk.providers }),
												);
										}}
									/>
								}
							/>
						</li>
					))}
				</RowList>
				{providers.data?.providers.length === 0 && (
					<p className="text-faint text-sm">{t("providers.empty")}</p>
				)}
			</Card>
		</>
	);
}

// ---------------------------------------------------------------- 项目

type ProjectForm = Schemas["ProjectCreate"];

const EMPTY_PROJECT: ProjectForm = { name: "", shallowClone: false };

function formOf(p: Project): ProjectForm {
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
	};
}

/** 「仓库内脚本：有 / 无」：取自该项目最近一次 setup 运行；无记录时为「未知」。 */
function RepoScriptText({ run }: { run: SetupRun | undefined }) {
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
					{t("projects.iconAutoHint")}
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

function ProjectEditor({
	initial,
	id,
	lastSetup,
	onDone,
}: {
	initial: ProjectForm;
	id?: string;
	lastSetup?: SetupRun;
	onDone: () => void;
}) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const providers = useProviders();
	const [f, setF] = useState<ProjectForm>(initial);
	const save = useAsync();
	const set = <K extends keyof ProjectForm>(k: K, v: ProjectForm[K]) =>
		setF((prev) => ({ ...prev, [k]: v }));
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
						};
						await unwrap(
							api.PATCH("/api/v1/projects/{id}", {
								params: { path: { id } },
								body: patch,
							}),
						);
					} else await unwrap(api.POST("/api/v1/projects", { body: f }));
					await qc.invalidateQueries({ queryKey: qk.projects });
				});
				if (ok) onDone();
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
				<TextInput
					placeholder="https://github.com/…"
					value={f.repoUrl ?? ""}
					onChange={(e) => set("repoUrl", e.target.value || undefined)}
				/>
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
							{ot.name}
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
			<div className="flex gap-2 sm:col-span-2">
				<Button
					type="submit"
					variant="primary"
					disabled={save.busy || !f.name.trim() || !gitPairOk}
				>
					{t("common.save")}
				</Button>
				<Button onClick={onDone}>{t("common.cancel")}</Button>
			</div>
			<ErrorText error={save.error} />
		</form>
	);
}

function ProjectsSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const projects = useProjects();
	const orbs = useOrbs();
	const lastSetup = latestSetupByProject(orbs.data?.orbs ?? []);
	const [editing, setEditing] = useState<string | "new" | null>(null);
	const del = useAsync();
	return (
		<Card
			title={t("projects.title")}
			icon="folder"
			action={
				editing === null && (
					<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
						{t("projects.new")}
					</Button>
				)
			}
		>
			{editing === "new" && (
				<div className="mb-3 rounded-lg border border-border bg-raised p-3">
					<ProjectEditor
						initial={EMPTY_PROJECT}
						onDone={() => setEditing(null)}
					/>
				</div>
			)}
			{projects.isPending && <Spinner />}
			<ErrorText error={projects.error ?? del.error} />
			<RowList>
				{projects.data?.projects.map((p) => (
					<li key={p.id}>
						{editing === p.id ? (
							<div className="my-1 rounded-lg border border-border bg-raised p-3">
								<ProjectEditor
									initial={formOf(p)}
									id={p.id}
									lastSetup={lastSetup.get(p.id)}
									onDone={() => setEditing(null)}
								/>
							</div>
						) : (
							<ListRow
								leading={<ProjectBadge name={p.name} icon={p.icon} size={28} />}
								title={<span className="truncate font-medium">{p.name}</span>}
								meta={
									<>
										<span className="min-w-0 max-w-full truncate">
											{p.repoUrl ?? t("projects.noRepo")}
										</span>
										{p.repoUrl && (
											<span>
												<RepoScriptText run={lastSetup.get(p.id)} />
											</span>
										)}
									</>
								}
								trailing={
									<>
										<IconButton
											icon="edit"
											label={t("common.edit")}
											onClick={() => setEditing(p.id)}
										/>
										<IconButton
											icon="trash"
											label={t("common.delete")}
											onClick={() => {
												if (
													!window.confirm(
														t("projects.deleteConfirm", { name: p.name }),
													)
												)
													return;
												void del
													.run(() =>
														unwrap(
															api.DELETE("/api/v1/projects/{id}", {
																params: { path: { id: p.id } },
															}),
														),
													)
													.then(() =>
														qc.invalidateQueries({ queryKey: qk.projects }),
													);
											}}
										/>
									</>
								}
							/>
						)}
					</li>
				))}
			</RowList>
			{projects.data?.projects.length === 0 && editing !== "new" && (
				<p className="text-faint text-sm">{t("projects.empty")}</p>
			)}
		</Card>
	);
}

// ---------------------------------------------------------------- 密钥

function SecretsSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const secrets = useSecrets();
	const [name, setName] = useState("");
	const [value, setValue] = useState("");
	const [mode, setMode] = useState<"proxy" | "env">("proxy");
	const [hosts, setHosts] = useState("");
	const save = useAsync();
	const del = useAsync();
	const envNameOk = mode !== "env" || /^[A-Z_][A-Z0-9_]*$/.test(name);
	return (
		<>
			<Card title={t("secrets.put")} icon="plus">
				<form
					className="grid gap-3 sm:grid-cols-2"
					onSubmit={async (e) => {
						e.preventDefault();
						const body: Schemas["SecretPut"] = {
							value,
							mode,
							...(hosts.trim()
								? {
										hosts: hosts
											.split(",")
											.map((h) => h.trim())
											.filter(Boolean),
									}
								: {}),
						};
						const ok = await save.run(() =>
							unwrap(
								api.PUT("/api/v1/secrets/{name}", {
									params: { path: { name: name.trim() } },
									body,
								}),
							),
						);
						if (ok) {
							setValue("");
							setName("");
							void qc.invalidateQueries({ queryKey: qk.secrets });
						}
					}}
				>
					<Field
						label={t("secrets.name")}
						hint={
							mode === "env" ? t("secrets.envNameHint") : t("secrets.nameHint")
						}
					>
						<TextInput
							required
							value={name}
							onChange={(e) => setName(e.target.value)}
						/>
					</Field>
					<Field label={t("secrets.mode")}>
						<Select
							value={mode}
							onChange={(e) => setMode(e.target.value as "proxy" | "env")}
						>
							<option value="proxy">{t("secrets.modeProxy")}</option>
							<option value="env">{t("secrets.modeEnv")}</option>
						</Select>
					</Field>
					<div className="sm:col-span-2">
						<Field label={t("secrets.value")}>
							<TextInput
								required
								type="password"
								autoComplete="off"
								value={value}
								onChange={(e) => setValue(e.target.value)}
							/>
						</Field>
					</div>
					{mode === "proxy" && (
						<div className="sm:col-span-2">
							<Field label={t("secrets.hosts")} hint={t("secrets.hostsHint")}>
								<TextInput
									value={hosts}
									onChange={(e) => setHosts(e.target.value)}
								/>
							</Field>
						</div>
					)}
					<div>
						<Button
							type="submit"
							variant="primary"
							disabled={save.busy || !name.trim() || !value || !envNameOk}
						>
							{t("common.save")}
						</Button>
					</div>
					<ErrorText error={save.error} />
				</form>
			</Card>
			<Card title={t("secrets.title")} icon="key">
				{secrets.isPending && <Spinner />}
				<ErrorText error={secrets.error ?? del.error} />
				<RowList>
					{secrets.data?.secrets.map((s) => (
						<li key={s.name}>
							<ListRow
								leading={<LeadIcon name="key" />}
								title={
									<>
										<span className="truncate font-mono">{s.name}</span>
										<Badge tone={s.mode === "env" ? "frost" : "neutral"}>
											{s.mode}
										</Badge>
									</>
								}
								meta={
									<>
										{s.hosts?.map((h) => (
											<Badge key={h} icon="globe">
												{h}
											</Badge>
										))}
										<span>{formatRelative(s.updatedAt)}</span>
									</>
								}
								trailing={
									<IconButton
										icon="trash"
										label={t("common.delete")}
										onClick={() => {
											if (
												!window.confirm(
													t("secrets.deleteConfirm", { name: s.name }),
												)
											)
												return;
											void del
												.run(() =>
													unwrap(
														api.DELETE("/api/v1/secrets/{name}", {
															params: { path: { name: s.name } },
														}),
													),
												)
												.then(() =>
													qc.invalidateQueries({ queryKey: qk.secrets }),
												);
										}}
									/>
								}
							/>
						</li>
					))}
				</RowList>
				{secrets.data?.secrets.length === 0 && (
					<p className="text-faint text-sm">{t("secrets.empty")}</p>
				)}
			</Card>
		</>
	);
}

// ---------------------------------------------------------------- Git 身份

function GitIdentityForm({ initial }: { initial?: Schemas["GitIdentity"] }) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [name, setName] = useState(initial?.name ?? "");
	const [email, setEmail] = useState(initial?.email ?? "");
	const [saved, setSaved] = useState(false);
	const save = useAsync();
	const clear = useAsync();
	const dirty =
		name.trim() !== (initial?.name ?? "") ||
		email.trim() !== (initial?.email ?? "");
	return (
		<form
			className="grid gap-3 sm:grid-cols-2"
			onSubmit={async (e) => {
				e.preventDefault();
				setSaved(false);
				await save.run(async () => {
					const res = await unwrap(
						api.PUT("/api/v1/settings/git-identity", {
							body: { name: name.trim(), email: email.trim() },
						}),
					);
					qc.setQueryData(qk.gitIdentity, res);
					setName(res.identity?.name ?? "");
					setEmail(res.identity?.email ?? "");
					setSaved(true);
				});
			}}
		>
			<Field label={t("gitIdentity.name")}>
				<TextInput
					required
					maxLength={200}
					autoComplete="name"
					value={name}
					onChange={(e) => {
						setName(e.target.value);
						setSaved(false);
					}}
				/>
			</Field>
			<Field label={t("gitIdentity.email")}>
				<TextInput
					required
					type="email"
					maxLength={320}
					autoComplete="email"
					value={email}
					onChange={(e) => {
						setEmail(e.target.value);
						setSaved(false);
					}}
				/>
			</Field>
			<div className="flex items-center gap-2 sm:col-span-2">
				<Button
					type="submit"
					variant="primary"
					disabled={save.busy || !dirty || !name.trim() || !email.includes("@")}
				>
					{t("common.save")}
				</Button>
				{initial && (
					<Button
						disabled={clear.busy}
						onClick={() => {
							if (!window.confirm(t("gitIdentity.clearConfirm"))) return;
							setSaved(false);
							void clear.run(async () => {
								await unwrap(api.DELETE("/api/v1/settings/git-identity"));
								qc.setQueryData(qk.gitIdentity, {});
								setName("");
								setEmail("");
							});
						}}
					>
						{t("common.clear")}
					</Button>
				)}
				{saved && !dirty && (
					<span className="inline-flex items-center gap-1 text-ok text-xs">
						<Icon name="check" size={13} weight="bold" />
						<span>{t("gitIdentity.saved")}</span>
					</span>
				)}
			</div>
			<ErrorText error={save.error ?? clear.error} />
		</form>
	);
}

function GitIdentitySettings() {
	const { t } = useTranslation();
	const q = useGitIdentity();
	const id = q.data?.identity;
	return (
		<Card title={t("gitIdentity.title")} icon="fork">
			<p className="mb-3 text-muted text-xs">{t("gitIdentity.hint")}</p>
			{q.isPending && <Spinner />}
			<ErrorText error={q.error} />
			{q.data && (
				<>
					<div className="mb-3">
						{/* 「当前：name <email>」须是同一个文本节点（测试与验收脚本按整句查找） */}
						<Callout
							tone={id ? "neutral" : "warn"}
							icon={id ? "user" : undefined}
						>
							{id
								? t("gitIdentity.current", { name: id.name, email: id.email })
								: t("gitIdentity.notSet")}
						</Callout>
					</div>
					<GitIdentityForm initial={id} />
				</>
			)}
		</Card>
	);
}

// ---------------------------------------------------------------- agent 配置

function AgentConfigSettings() {
	const { t } = useTranslation();
	const [result, setResult] = useState<
		Schemas["AgentConfigWriteResult"] | null
	>(null);
	// 来源须稳定：FileBrowser 以 sources 为依赖加载根
	const sources = useMemo(() => [agentConfigSource(setResult)], []);
	return (
		<Card title={t("agentConfig.title")} icon="file">
			<p className="mb-3 text-muted text-xs">
				{t("agentConfig.hint")} {t("agentConfig.pushHint")}
			</p>
			<div className="h-[32rem] overflow-hidden rounded-lg border border-border">
				<FileBrowser sources={sources} storageKey="settings.agent-config" />
			</div>
			{result && (
				<div className="mt-2 flex flex-col gap-1.5">
					{result.extractedSecrets.length > 0 && (
						<Callout tone="ok">
							{t("agentConfig.extracted", {
								names: result.extractedSecrets.join(", "),
							})}
						</Callout>
					)}
					{result.warnings.map((w) => (
						<Callout key={`${w.path}:${w.line ?? 0}:${w.kind}`} tone="warn">
							{`${w.path}${w.line ? `:${w.line}` : ""} — ${w.message}`}
						</Callout>
					))}
				</div>
			)}
		</Card>
	);
}

// ---------------------------------------------------------------- 外观

/** 主题色色块：各预设浅色 accent 的固定值（全站唯一允许的颜色字面量，另见 favicon）。 */
const SWATCH: Record<AccentName, string> = {
	teal: "oklch(0.53 0.105 205)",
	cobalt: "oklch(0.53 0.17 258)",
	violet: "oklch(0.53 0.17 295)",
	berry: "oklch(0.53 0.17 358)",
	fox: "oklch(0.55 0.16 48)",
	graphite: "oklch(0.3 0.015 260)",
};

const MODE_ICON: Record<ThemeMode, IconName> = {
	system: "monitor",
	light: "sun",
	dark: "moon",
};

function ThemeSettings() {
	const { t } = useTranslation();
	const mode = useTheme((s) => s.mode);
	const accent = useTheme((s) => s.accent);
	const setMode = useTheme((s) => s.setMode);
	const setAccent = useTheme((s) => s.setAccent);
	return (
		<Card title={t("settings.appearance")} icon="palette">
			<div className="flex flex-col gap-5">
				<div className="flex flex-col gap-2">
					<span aria-hidden className="font-medium text-muted text-xs">
						{t("appearance.mode")}
					</span>
					<div
						role="radiogroup"
						aria-label={t("appearance.mode")}
						onKeyDown={radioKeys}
						className="inline-flex w-fit max-w-full rounded-lg border border-border bg-inset p-0.5"
					>
						{MODES.map((m) => {
							const on = mode === m;
							return (
								// biome-ignore lint/a11y/useSemanticElements: 分段按钮外观的单选（WAI-ARIA radio），原生 radio 画不出
								<button
									key={m}
									type="button"
									role="radio"
									aria-checked={on}
									tabIndex={on ? 0 : -1}
									onClick={() => setMode(m)}
									className={`inline-flex h-7 items-center gap-1.5 rounded-md px-3 font-medium text-xs transition-[background-color,color,box-shadow] duration-150 ${on ? "bg-panel text-fg shadow-xs ring-1 ring-border" : "text-muted hover:text-fg"}`}
								>
									<Icon name={MODE_ICON[m]} size={14} />
									{t(`appearance.modes.${m}`)}
								</button>
							);
						})}
					</div>
				</div>
				<div className="flex flex-col gap-2">
					<span aria-hidden className="font-medium text-muted text-xs">
						{t("appearance.accent")}
					</span>
					<div
						role="radiogroup"
						aria-label={t("appearance.accent")}
						onKeyDown={radioKeys}
						className="flex flex-wrap gap-x-4 gap-y-3"
					>
						{ACCENTS.map((a) => {
							const on = accent === a;
							const label = t(`appearance.accents.${a}`);
							return (
								// biome-ignore lint/a11y/useSemanticElements: 色块外观的单选（WAI-ARIA radio），原生 radio 画不出
								<button
									key={a}
									type="button"
									role="radio"
									aria-checked={on}
									aria-label={label}
									tabIndex={on ? 0 : -1}
									onClick={() => setAccent(a)}
									className={`flex w-12 flex-col items-center gap-1.5 rounded-md py-1 text-2xs transition-colors duration-150 ${on ? "font-medium text-fg" : "text-muted hover:text-fg"}`}
								>
									<span
										aria-hidden
										className={`grid h-7 w-7 place-items-center rounded-full text-white shadow-xs ring-offset-2 ring-offset-panel transition-shadow duration-150 ${on ? "ring-2 ring-[var(--ring)]" : ""}`}
										style={{ background: SWATCH[a] }}
									>
										{on && <Icon name="check" size={14} weight="bold" />}
									</span>
									<span aria-hidden>{label}</span>
								</button>
							);
						})}
					</div>
				</div>
				<p className="text-faint text-xs">{t("appearance.desc")}</p>
			</div>
		</Card>
	);
}

function AgentAvatarSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useAgentIdentity();
	const save = useAsync();
	const [custom, setCustom] = useState("");
	const cur = q.data;
	const kind = cur?.avatarImage
		? "image"
		: cur?.avatarIcon
			? "icon"
			: "default";
	// PUT 整体替换：头像图片与图标字形互斥，`{}` = 恢复默认
	const put = (body: Schemas["AgentIdentity"]) =>
		save.run(async () => {
			const res = await unwrap(
				api.PUT("/api/v1/settings/agent-identity", { body }),
			);
			qc.setQueryData(qk.agentIdentity, res);
		});
	const onFile = (e: ChangeEvent<HTMLInputElement>) => {
		const file = e.target.files?.[0];
		e.target.value = "";
		if (!file) return;
		void save.run(async () => {
			let avatarImage: string;
			try {
				avatarImage = await avatarDataUrl(file);
			} catch (err) {
				throw new Error(
					err instanceof AvatarTooLargeError
						? t("appearance.avatarTooLarge")
						: t("appearance.avatarDecodeFailed"),
				);
			}
			const res = await unwrap(
				api.PUT("/api/v1/settings/agent-identity", { body: { avatarImage } }),
			);
			qc.setQueryData(qk.agentIdentity, res);
		});
	};
	return (
		<Card title={t("appearance.avatar")} icon="robot">
			<div className="flex items-center gap-3">
				<AgentAvatar size={40} />
				<div className="min-w-0 flex-1">
					<p className="text-sm">{t(`appearance.avatarKind.${kind}`)}</p>
					<p className="text-faint text-xs">{t("appearance.avatarHint")}</p>
				</div>
				{(save.busy || q.isPending) && <Spinner />}
			</div>
			<div className="mt-3 flex flex-wrap items-center gap-2">
				<label className="inline-flex h-7 cursor-pointer select-none items-center gap-1 rounded-md border border-border bg-panel px-2.5 font-medium text-fg text-xs shadow-xs transition-colors duration-150 hover:bg-hover has-[:disabled]:pointer-events-none has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-[var(--ring)] has-[:focus-visible]:outline-offset-2 has-[:disabled]:opacity-50">
					<Icon name="upload" size={13} />
					{t("appearance.avatarUpload")}
					<input
						type="file"
						accept="image/png,image/webp,image/jpeg"
						className="sr-only"
						disabled={save.busy}
						onChange={onFile}
					/>
				</label>
				<Button
					size="sm"
					variant="ghost"
					icon="refresh"
					disabled={kind === "default" || save.busy}
					onClick={() => void put({})}
				>
					{t("appearance.avatarReset")}
				</Button>
			</div>
			<div className="mt-4 flex flex-col gap-2">
				<span aria-hidden className="font-medium text-muted text-xs">
					{t("appearance.avatarGlyphs")}
				</span>
				<GlyphGrid
					glyphs={AVATAR_GLYPHS}
					value={cur?.avatarIcon}
					label={t("appearance.avatarGlyphs")}
					disabled={save.busy}
					onPick={(avatarIcon) => void put({ avatarIcon })}
				/>
			</div>
			<form
				className="mt-3 flex items-end gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					const avatarIcon = custom.trim();
					if (!isSingleGlyph(avatarIcon)) return;
					void put({ avatarIcon }).then((ok) => {
						if (ok) setCustom("");
					});
				}}
			>
				<Field label={t("appearance.avatarCustom")}>
					<TextInput
						className="w-28 text-center"
						maxLength={16}
						placeholder="🦊"
						value={custom}
						onChange={(e) => setCustom(e.target.value)}
					/>
				</Field>
				<Button type="submit" disabled={save.busy || !isSingleGlyph(custom)}>
					{t("appearance.avatarUse")}
				</Button>
			</form>
			<div className="mt-2">
				<ErrorText error={save.error ?? q.error} />
			</div>
		</Card>
	);
}

function AppearanceSettings() {
	return (
		<>
			<ThemeSettings />
			<AgentAvatarSettings />
		</>
	);
}

// ---------------------------------------------------------------- 关于

function AboutSettings() {
	const { t, i18n } = useTranslation();
	const health = useHealth();
	const me = useMe();
	const navigate = useNavigate();
	const logout = useAsync();
	return (
		<Card title={t("about.title")} icon="info">
			<KeyValue
				rows={[
					{
						key: "version",
						label: t("about.version"),
						value: (
							<span className="font-mono">{health.data?.version ?? "—"}</span>
						),
					},
					{
						key: "owner",
						label: t("about.owner"),
						value: me.data?.displayName ?? "—",
					},
					{
						key: "since",
						label: t("about.since"),
						value: me.data ? formatDateTime(me.data.createdAt) : "—",
					},
					{
						key: "session",
						label: t("about.session"),
						value: me.data ? (
							<Badge>{t(`about.authKind.${me.data.auth.kind}`)}</Badge>
						) : (
							"—"
						),
					},
					{
						key: "language",
						label: t("about.language"),
						value: (
							<Select
								wrapperClassName="max-w-40"
								aria-label={t("about.language")}
								value={i18n.language}
								onChange={(e) => void i18n.changeLanguage(e.target.value)}
							>
								<option value="zh-CN">简体中文</option>
								<option value="en">English</option>
							</Select>
						),
					},
				]}
			/>
			<div className="mt-4 border-border border-t pt-3">
				<Button
					variant="danger"
					disabled={logout.busy}
					onClick={() =>
						void logout
							.run(() => unwrap(api.POST("/api/v1/auth/logout")))
							.then((ok) => {
								if (ok) navigate("/login");
							})
					}
				>
					{t("about.logout")}
				</Button>
				<ErrorText error={logout.error} />
			</div>
		</Card>
	);
}

export function SettingsPage({
	section,
}: {
	section: SettingsSection;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3">
				{bp !== "expanded" && (
					<IconButton
						icon="menu"
						label={t("nav.sidebar")}
						onClick={() =>
							uiStore.getState().setSidebar(!uiStore.getState().sidebarOpen)
						}
					/>
				)}
				<Icon name="settings" size={16} className="text-faint" />
				<h1 className="font-semibold text-sm">{t("nav.settings")}</h1>
				<span aria-hidden className="text-ghost text-sm">
					/
				</span>
				<span className="truncate text-muted text-sm">
					{t(`settings.${section}`)}
				</span>
			</header>
			<div className="flex min-h-0 flex-1 flex-col md:flex-row">
				<nav className="focus-inset flex shrink-0 gap-1 overflow-x-auto border-border border-b p-2 md:w-52 md:flex-col md:border-r md:border-b-0">
					{SECTIONS.map((s) => (
						<NavLink
							key={s.key}
							to={`/settings/${s.key}`}
							className={({ isActive }) =>
								`inline-flex h-8 shrink-0 items-center gap-2 rounded-md px-2.5 text-sm transition-colors duration-150 ${isActive ? "bg-hover text-fg ring-1 ring-border" : "text-muted hover:bg-hover/60 hover:text-fg"}`
							}
						>
							<Icon name={s.icon} size={15} />
							{t(`settings.${s.key}`)}
						</NavLink>
					))}
				</nav>
				<div className="min-h-0 flex-1 overflow-y-auto p-3 pb-safe sm:p-5">
					<div className="mx-auto flex max-w-3xl flex-col gap-3">
						{section === "devices" && <DevicesSettings />}
						{section === "providers" && <ProvidersSettings />}
						{section === "projects" && <ProjectsSettings />}
						{section === "secrets" && <SecretsSettings />}
						{section === "git-identity" && <GitIdentitySettings />}
						{section === "agent-config" && <AgentConfigSettings />}
						{section === "appearance" && <AppearanceSettings />}
						{section === "about" && <AboutSettings />}
					</div>
				</div>
			</div>
		</Sheet>
	);
}
