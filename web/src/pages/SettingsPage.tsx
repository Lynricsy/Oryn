/**
 * 设置页：设备（列表、吊销、批准配对）、provider（新建并一次性显示令牌、类型、在线状态）、项目（表单）、
 * 密钥（名称列表、写入值）、Git 身份（orb 内提交身份的全局默认值）、agent 配置（omp-home 文件列表 + 纯文本编辑器，不做表单化）、关于。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, useNavigate } from "react-router";
import {
	api,
	encodePathParam,
	type Project,
	rawFetch,
	type Schemas,
	unwrap,
} from "../api/client";
import {
	qk,
	useAgentFile,
	useAgentFiles,
	useDevices,
	useGitIdentity,
	useHealth,
	useMe,
	useOrbs,
	useProjects,
	useProviders,
	useSecrets,
} from "../api/queries";
import { PairApproval } from "../components/PairApproval";
import { CopyButton } from "../components/ui/CopyButton";
import { Icon, type IconName } from "../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../components/ui/primitives";
import { latestSetupByProject, type SetupRun } from "../lib/env-banner";
import { formatDateTime, formatRelative } from "../lib/format";
import { formatBytes } from "../lib/images";
import { useBreakpoint } from "../lib/viewport";
import { uiStore } from "../state/ui";

export type SettingsSection =
	| "devices"
	| "providers"
	| "projects"
	| "secrets"
	| "git-identity"
	| "agent-config"
	| "about";

const SECTIONS: { key: SettingsSection; icon: IconName }[] = [
	{ key: "devices", icon: "device" },
	{ key: "providers", icon: "box" },
	{ key: "projects", icon: "folder" },
	{ key: "secrets", icon: "key" },
	{ key: "git-identity", icon: "fork" },
	{ key: "agent-config", icon: "file" },
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

function Card({
	title,
	children,
	action,
}: {
	title: ReactNode;
	children: ReactNode;
	action?: ReactNode;
}) {
	return (
		<section className="mb-6 rounded-xl border border-border bg-panel">
			<div className="flex items-center justify-between gap-2 border-border border-b px-4 py-2.5">
				<h2 className="font-medium text-sm">{title}</h2>
				{action}
			</div>
			<div className="p-4">{children}</div>
		</section>
	);
}

// ---------------------------------------------------------------- 设备

function DevicesSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const devices = useDevices();
	const revoke = useAsync();
	return (
		<>
			<Card title={t("devices.approveTitle")}>
				<p className="mb-3 text-muted text-xs">{t("pair.hint")}</p>
				<PairApproval />
			</Card>
			<Card title={t("devices.title")}>
				{devices.isPending && <Spinner />}
				<ErrorText error={devices.error ?? revoke.error} />
				<ul className="divide-y divide-border">
					{devices.data?.devices.map((d) => (
						<li key={d.id} className="flex items-center gap-3 py-2">
							<Icon name="device" size={16} className="text-muted" />
							<div className="min-w-0 flex-1">
								<div className="flex items-center gap-2 text-sm">
									<span className="truncate font-medium">{d.name}</span>
									{d.current && (
										<span className="rounded bg-accent/15 px-1 text-[10px] text-accent">
											{t("devices.current")}
										</span>
									)}
									{d.revokedAt && (
										<span className="rounded bg-err/10 px-1 text-[10px] text-err">
											{t("devices.revoked")}
										</span>
									)}
								</div>
								<div className="text-faint text-xs">
									{[
										t(`devices.kind.${d.kind}`),
										d.platform,
										t("devices.lastSeen", {
											when: d.lastSeenAt ? formatRelative(d.lastSeenAt) : "—",
										}),
									]
										.filter(Boolean)
										.join(" · ")}
								</div>
							</div>
							{!d.revokedAt && (
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
							)}
						</li>
					))}
				</ul>
				{devices.data?.devices.length === 0 && (
					<p className="text-faint text-sm">{t("devices.empty")}</p>
				)}
			</Card>
		</>
	);
}

// ---------------------------------------------------------------- provider

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
			<Card title={t("providers.create")}>
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
					<div className="mt-3 rounded-lg border border-warn/40 bg-warn/10 p-3">
						<p className="mb-2 text-sm">{t("providers.tokenOnce")}</p>
						<div className="flex items-center gap-2">
							<code className="min-w-0 flex-1 break-all rounded bg-inset px-2 py-1 font-mono text-xs">
								{token}
							</code>
							<CopyButton text={token} />
						</div>
					</div>
				)}
				<ErrorText error={create.error} />
			</Card>
			<Card title={t("providers.title")}>
				{providers.isPending && <Spinner />}
				<ErrorText error={providers.error ?? del.error} />
				<ul className="divide-y divide-border">
					{providers.data?.providers.map((p) => (
						<li key={p.id} className="py-2.5">
							<div className="flex items-center gap-2">
								<span
									className={`h-2 w-2 rounded-full ${p.online ? "bg-ok" : "bg-faint"}`}
								/>
								<span className="font-medium text-sm">{p.name}</span>
								<span className="text-faint text-xs">
									{p.online ? t("providers.online") : t("providers.offline")}
								</span>
								{p.driver && (
									<span className="rounded bg-inset px-1 text-[10px] text-muted">
										{p.driver}
									</span>
								)}
								<div className="flex-1" />
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
							</div>
							{p.orbTypes.length > 0 && (
								<div className="mt-1 flex flex-wrap gap-1.5 pl-4">
									{p.orbTypes.map((ot) => (
										<span
											key={ot.name}
											className="rounded border border-border px-1.5 py-0.5 text-muted text-xs"
										>
											{`${ot.name} · ${ot.cpus}C/${Math.round(ot.memoryMB / 1024)}G`}
											{p.defaultType === ot.name
												? ` · ${t("providers.default")}`
												: ""}
										</span>
									))}
								</div>
							)}
							{p.capabilities && (
								<div className="mt-1 pl-4 text-faint text-xs">
									{t("providers.capabilities", {
										freeze: p.capabilities.freeze ? "✓" : "✗",
										snapshot: p.capabilities.snapshot ? "✓" : "✗",
									})}
								</div>
							)}
						</li>
					))}
				</ul>
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
			action={
				editing === null && (
					<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
						{t("projects.new")}
					</Button>
				)
			}
		>
			{editing === "new" && (
				<div className="mb-4 rounded-lg border border-border p-3">
					<ProjectEditor
						initial={EMPTY_PROJECT}
						onDone={() => setEditing(null)}
					/>
				</div>
			)}
			{projects.isPending && <Spinner />}
			<ErrorText error={projects.error ?? del.error} />
			<ul className="divide-y divide-border">
				{projects.data?.projects.map((p) => (
					<li key={p.id} className="py-2.5">
						{editing === p.id ? (
							<ProjectEditor
								initial={formOf(p)}
								id={p.id}
								lastSetup={lastSetup.get(p.id)}
								onDone={() => setEditing(null)}
							/>
						) : (
							<div className="flex items-center gap-2">
								<Icon name="folder" size={15} className="text-muted" />
								<div className="min-w-0 flex-1">
									<div className="font-medium text-sm">{p.name}</div>
									<div className="truncate text-faint text-xs">
										{p.repoUrl ?? t("projects.noRepo")}
									</div>
									{p.repoUrl && (
										<div className="text-faint text-xs">
											<RepoScriptText run={lastSetup.get(p.id)} />
										</div>
									)}
								</div>
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
							</div>
						)}
					</li>
				))}
			</ul>
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
			<Card title={t("secrets.put")}>
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
			<Card title={t("secrets.title")}>
				{secrets.isPending && <Spinner />}
				<ErrorText error={secrets.error ?? del.error} />
				<ul className="divide-y divide-border">
					{secrets.data?.secrets.map((s) => (
						<li key={s.name} className="flex items-center gap-2 py-2">
							<Icon name="key" size={14} className="text-muted" />
							<span className="min-w-0 flex-1 truncate font-mono text-sm">
								{s.name}
							</span>
							<span className="rounded bg-inset px-1 text-[10px] text-muted">
								{s.mode}
							</span>
							{s.hosts && s.hosts.length > 0 && (
								<span className="hidden text-faint text-xs sm:inline">
									{s.hosts.join(", ")}
								</span>
							)}
							<span className="hidden text-faint text-xs sm:inline">
								{formatRelative(s.updatedAt)}
							</span>
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
										.then(() => qc.invalidateQueries({ queryKey: qk.secrets }));
								}}
							/>
						</li>
					))}
				</ul>
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
					<span className="text-muted text-xs">{t("gitIdentity.saved")}</span>
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
		<Card title={t("gitIdentity.title")}>
			<p className="mb-3 text-muted text-xs">{t("gitIdentity.hint")}</p>
			{q.isPending && <Spinner />}
			<ErrorText error={q.error} />
			{q.data && (
				<>
					<p className="mb-3 text-sm">
						{id
							? t("gitIdentity.current", { name: id.name, email: id.email })
							: t("gitIdentity.notSet")}
					</p>
					<GitIdentityForm initial={id} />
				</>
			)}
		</Card>
	);
}

// ---------------------------------------------------------------- agent 配置

function AgentConfigEditor({
	path,
	onDeleted,
}: {
	path: string;
	onDeleted: () => void;
}) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const file = useAgentFile(path);
	const [text, setText] = useState<string | null>(null);
	const [result, setResult] = useState<
		Schemas["AgentConfigWriteResult"] | null
	>(null);
	const save = useAsync();
	const value = text ?? file.data ?? "";
	return (
		<div className="flex min-h-0 flex-1 flex-col gap-2">
			<div className="flex items-center gap-2">
				<span className="min-w-0 flex-1 truncate font-mono text-sm">
					{path}
				</span>
				<Button
					size="sm"
					variant="danger"
					onClick={() => {
						if (!window.confirm(t("agentConfig.deleteConfirm", { path })))
							return;
						void save
							.run(() =>
								rawFetch(
									`/api/v1/agent-config/files/${encodePathParam(path)}`,
									{ method: "DELETE" },
								),
							)
							.then(async (ok) => {
								if (!ok) return;
								await qc.invalidateQueries({ queryKey: qk.agentFiles });
								onDeleted();
							});
					}}
				>
					{t("common.delete")}
				</Button>
				<Button
					size="sm"
					variant="primary"
					disabled={save.busy || text === null}
					onClick={() =>
						void save.run(async () => {
							const res = await rawFetch(
								`/api/v1/agent-config/files/${encodePathParam(path)}`,
								{
									method: "PUT",
									headers: { "Content-Type": "application/octet-stream" },
									body: value,
								},
							);
							setResult(
								(await res.json()) as Schemas["AgentConfigWriteResult"],
							);
							setText(null);
							await qc.invalidateQueries({ queryKey: qk.agentFile(path) });
							await qc.invalidateQueries({ queryKey: qk.agentFiles });
						})
					}
				>
					{t("common.save")}
				</Button>
			</div>
			{file.isPending ? (
				<Spinner />
			) : (
				<TextArea
					aria-label={path}
					spellCheck={false}
					className="min-h-96 flex-1 text-xs leading-relaxed"
					value={value}
					onChange={(e) => setText(e.target.value)}
				/>
			)}
			<ErrorText error={file.error ?? save.error} />
			{result && (
				<div className="text-xs">
					{result.extractedSecrets.length > 0 && (
						<p className="text-ok">
							{t("agentConfig.extracted", {
								names: result.extractedSecrets.join(", "),
							})}
						</p>
					)}
					{result.warnings.map((w) => (
						<p key={`${w.path}:${w.line ?? 0}:${w.kind}`} className="text-warn">
							{`${w.path}${w.line ? `:${w.line}` : ""} — ${w.message}`}
						</p>
					))}
				</div>
			)}
		</div>
	);
}

function AgentConfigSettings() {
	const { t } = useTranslation();
	const files = useAgentFiles();
	const [selected, setSelected] = useState<string | null>(null);
	const [newPath, setNewPath] = useState("");
	const create = useAsync();
	const qc = useQueryClient();
	return (
		<Card title={t("agentConfig.title")}>
			<p className="mb-3 text-muted text-xs">{t("agentConfig.hint")}</p>
			<div className="flex flex-col gap-4 md:flex-row">
				<div className="md:w-64 md:shrink-0">
					{files.isPending && <Spinner />}
					<ErrorText error={files.error} />
					<ul className="flex max-h-[28rem] flex-col overflow-y-auto">
						{files.data?.files.map((f) => (
							<li key={f.path}>
								<button
									type="button"
									onClick={() => setSelected(f.path)}
									className={`flex w-full items-center gap-2 rounded px-2 py-1 text-left text-sm hover:bg-hover ${selected === f.path ? "bg-hover" : ""}`}
								>
									<span className="min-w-0 flex-1 truncate font-mono text-xs">
										{f.path}
									</span>
									<span className="shrink-0 text-[10px] text-faint">
										{formatBytes(f.size)}
									</span>
								</button>
							</li>
						))}
					</ul>
					{files.data?.files.length === 0 && (
						<EmptyState icon="file" title={t("agentConfig.empty")}>
							{t("agentConfig.pushHint")}
						</EmptyState>
					)}
					<form
						className="mt-2 flex gap-1"
						onSubmit={async (e) => {
							e.preventDefault();
							const p = newPath.trim().replace(/^\/+/, "");
							if (!p || p.includes("..")) return;
							const ok = await create.run(() =>
								rawFetch(`/api/v1/agent-config/files/${encodePathParam(p)}`, {
									method: "PUT",
									headers: { "Content-Type": "application/octet-stream" },
									body: "",
								}),
							);
							if (ok) {
								await qc.invalidateQueries({ queryKey: qk.agentFiles });
								setSelected(p);
								setNewPath("");
							}
						}}
					>
						<TextInput
							className="h-7 py-0 font-mono text-xs"
							placeholder={t("agentConfig.newPath")}
							value={newPath}
							onChange={(e) => setNewPath(e.target.value)}
						/>
						<Button size="sm" type="submit" disabled={!newPath.trim()}>
							{t("common.create")}
						</Button>
					</form>
					<ErrorText error={create.error} />
				</div>
				<div className="flex min-w-0 flex-1 flex-col">
					{selected ? (
						<AgentConfigEditor
							key={selected}
							path={selected}
							onDeleted={() => setSelected(null)}
						/>
					) : (
						<p className="text-faint text-sm">{t("agentConfig.pick")}</p>
					)}
				</div>
			</div>
		</Card>
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
		<Card title={t("about.title")}>
			<dl className="grid grid-cols-[8rem_1fr] gap-y-2 text-sm">
				<dt className="text-muted">{t("about.version")}</dt>
				<dd className="font-mono">{health.data?.version ?? "—"}</dd>
				<dt className="text-muted">{t("about.owner")}</dt>
				<dd>{me.data?.displayName ?? "—"}</dd>
				<dt className="text-muted">{t("about.since")}</dt>
				<dd>{me.data ? formatDateTime(me.data.createdAt) : "—"}</dd>
				<dt className="text-muted">{t("about.session")}</dt>
				<dd>{me.data ? t(`about.authKind.${me.data.auth.kind}`) : "—"}</dd>
				<dt className="text-muted">{t("about.language")}</dt>
				<dd>
					<Select
						className="max-w-40"
						value={i18n.language}
						onChange={(e) => void i18n.changeLanguage(e.target.value)}
					>
						<option value="zh-CN">简体中文</option>
						<option value="en">English</option>
					</Select>
				</dd>
			</dl>
			<div className="mt-4">
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
		<div className="flex h-full min-h-0 flex-col">
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
				<h1 className="font-medium text-sm">{t("nav.settings")}</h1>
			</header>
			<div className="flex min-h-0 flex-1 flex-col md:flex-row">
				<nav className="flex shrink-0 gap-1 overflow-x-auto border-border border-b p-2 md:w-52 md:flex-col md:border-r md:border-b-0">
					{SECTIONS.map((s) => (
						<NavLink
							key={s.key}
							to={`/settings/${s.key}`}
							className={({ isActive }) =>
								`inline-flex shrink-0 items-center gap-2 rounded-md px-2.5 py-1.5 text-sm ${isActive ? "bg-hover text-fg" : "text-muted hover:text-fg"}`
							}
						>
							<Icon name={s.icon} size={14} />
							{t(`settings.${s.key}`)}
						</NavLink>
					))}
				</nav>
				<div className="min-h-0 flex-1 overflow-y-auto p-4 pb-safe">
					<div className="mx-auto max-w-3xl">
						{section === "devices" && <DevicesSettings />}
						{section === "providers" && <ProvidersSettings />}
						{section === "projects" && <ProjectsSettings />}
						{section === "secrets" && <SecretsSettings />}
						{section === "git-identity" && <GitIdentitySettings />}
						{section === "agent-config" && <AgentConfigSettings />}
						{section === "about" && <AboutSettings />}
					</div>
				</div>
			</div>
		</div>
	);
}
