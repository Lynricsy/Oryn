/**
 * 设置页：设备（列表、吊销、批准配对）、provider（新建并一次性显示令牌、类型、在线状态）、runner（M11b：新建并一次性显示令牌与启动命令、列表、删除）、
 * 密钥（名称列表、写入值）、浏览器 Profile（M8）、Git 身份（orb 内提交身份的全局默认值）、agent 配置（与线程 Files 面板同一个文件浏览器 + CodeMirror 编辑器，不做表单化）、
 * 身份与头像（M9.6：Agent 身份、全局助手、角色头像、我的头像）、外观（明暗、主题色、壁纸）、通知（此设备、渠道、最近通知）、公开分享（M5）、
 * 桌面应用（M6，仅桌面壳内出现）、关于。分区导航在侧栏「更多」分区（`more-entries.ts`），页面本身只有页头与内容；
 * 项目设置在项目分区（`/projects/<id>`）。
 * 桌面壳里的会话是设备换来的 App 会话：不能批准配对（改为引导到浏览器）、不显示退出登录（由「更换服务器」代替）。
 * 移动壳（M7）：没有「桌面应用」分区，也不显示退出登录；关于页列出 App 版本、WebView、令牌存储与「更换服务器…」；
 * 设备页的「用手机扫码连接」二维码只在浏览器与桌面壳里出现（手机自己不用扫自己）。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type ChangeEvent,
	type CSSProperties,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useNavigate } from "react-router";
import { api, type Schemas, unwrap } from "../api/client";
import { identityKeys } from "../api/identity-queries";
import { invalidateModelCatalog } from "../api/model-catalog-queries";
import {
	qk,
	useDevices,
	useGitIdentity,
	useHealth,
	useMe,
	useProviders,
	useSecrets,
} from "../api/queries";
import { FileBrowser } from "../components/files/FileBrowser";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { PairApproval } from "../components/PairApproval";
import { radioKeys } from "../components/settings/GlyphGrid";
import { useAsync } from "../components/settings/useAsync";
import { CopyButton } from "../components/ui/CopyButton";
import {
	Badge,
	Callout,
	Card,
	KeyValue,
	ListRow,
	Sheet,
	StatusDot,
} from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import { DockerBadge, typeHasDocker } from "../components/ui/OrbBadges";
import {
	Button,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextInput,
} from "../components/ui/primitives";
import { agentConfigSource } from "../lib/file-source";
import { formatDateTime, formatMemoryMB, formatRelative } from "../lib/format";
import { host, type MobileInfo } from "../lib/host";
import { pairUri, qrModules } from "../lib/pair-qr";
import { useBreakpoint } from "../lib/viewport";
import { processWallpaper, readWallpaperImage } from "../lib/wallpaper";
import {
	ACCENTS,
	type AccentName,
	MODES,
	type ThemeMode,
	themeStore,
	useTheme,
	WALLPAPERS,
} from "../state/theme";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";
import AutomationsSettings from "./settings/AutomationsSettings";
import CollectionsPage from "./settings/agent/CollectionsPage";
import ExtensionsPage from "./settings/agent/ExtensionsPage";
import PromptsPage from "./settings/agent/PromptsPage";
import SkillsPage from "./settings/agent/SkillsPage";
import BrowserProfilesSettings from "./settings/BrowserProfilesSettings";
import { CoordinatorAbout } from "./settings/CoordinatorAbout";
import DesktopSettings from "./settings/DesktopSettings";
import { IdentitySettings } from "./settings/IdentitySettings";
import NotificationsSettings from "./settings/NotificationsSettings";
import RunnersSettings from "./settings/RunnersSettings";
import SharesSettings from "./settings/SharesSettings";

export type SettingsSection =
	| "devices"
	| "providers"
	| "runners"
	| "secrets"
	| "browser-profiles"
	| "git-identity"
	| "agent-skills"
	| "agent-prompts"
	| "agent-commands"
	| "agent-extensions"
	| "agent-config"
	| "identity"
	| "appearance"
	| "notifications"
	| "shares"
	| "automations"
	| "desktop"
	| "about";

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

// ---------------------------------------------------------------- 设备

/**
 * 「用手机扫码连接」：二维码内容 `oryn://pair?server=<本站 origin>`，手机 App 连接页扫码后只填服务器地址。
 * 二维码要高对比才扫得出：浅色主题用纸面底 + 正文色码，深色主题反过来（正文色作底、纸面色作码），始终浅底深码。
 */
function PairQrCard() {
	const { t } = useTranslation();
	const origin = window.location.origin;
	const uri = pairUri(origin);
	const qr = useMemo(() => qrModules(uri), [uri]);
	return (
		<Card title={t("devices.pairQr.title")} icon="qr">
			<div className="flex flex-col items-center gap-4 sm:flex-row sm:items-center">
				<div className="shrink-0 rounded-md bg-panel p-4 text-fg ring-1 ring-border [[data-theme=dark]_&]:bg-fg [[data-theme=dark]_&]:text-panel">
					<svg
						data-testid="pair-qr"
						data-qr-payload={uri}
						role="img"
						aria-label={t("devices.pairQr.label", { server: origin })}
						viewBox={`0 0 ${qr.size} ${qr.size}`}
						shapeRendering="crispEdges"
						className="block h-40 w-40"
					>
						{qr.runs.map((r) => (
							<rect
								key={`${r.x},${r.y}`}
								x={r.x}
								y={r.y}
								width={r.w}
								height={1}
								fill="currentColor"
							/>
						))}
					</svg>
				</div>
				<div className="flex min-w-0 flex-col gap-1.5">
					<p className="break-all font-mono text-sm">{origin}</p>
					<p className="text-muted text-xs">{t("devices.pairQr.hint")}</p>
				</div>
			</div>
		</Card>
	);
}

const DEVICE_ICON: Record<Schemas["Device"]["kind"], IconName> = {
	cli: "terminal",
	desktop: "monitor",
	mobile: "device",
};

function DevicesSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const devices = useDevices();
	const me = useMe();
	const revoke = useAsync();
	// App 会话（设备令牌换来的网页会话）不能批准配对：设备令牌不能再签发设备
	const appSession = me.data?.auth.kind === "web" && !!me.data.auth.deviceId;
	return (
		<>
			<Card title={t("devices.approveTitle")} icon="key">
				{appSession ? (
					<p className="text-muted text-xs">
						{t("devices.pairInBrowser")}{" "}
						<a
							href="/pair"
							target="_blank"
							rel="noopener noreferrer"
							className="text-accent-ink underline underline-offset-3"
						>
							{t("devices.pairInBrowserLink")}
						</a>
					</p>
				) : (
					<>
						<p className="mb-3 text-muted text-xs">{t("pair.hint")}</p>
						<PairApproval />
					</>
				)}
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
			{host.mobile === null && <PairQrCard />}
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
										{/* 每个 orb 类型一组：名称 + 规格，组间留出明显间隔；「默认」只是标注，用中性色 */}
										{p.orbTypes.length > 0 && (
											<span className="mt-1 flex basis-full flex-wrap gap-2">
												{p.orbTypes.map((ot) => (
													<span
														key={ot.name}
														className="inline-flex h-6 items-center gap-2 rounded-md bg-inset px-2 text-muted"
													>
														<span className="font-medium text-fg">
															{ot.name}
														</span>
														<span className="inline-flex items-center gap-1 tabular-nums">
															<Icon name="cpu" size={11} aria-hidden />
															{t("panel.cores", { n: ot.cpus })}
														</span>
														<span className="inline-flex items-center gap-1 tabular-nums">
															<Icon name="memory" size={11} aria-hidden />
															{formatMemoryMB(ot.memoryMB)}
														</span>
														{p.defaultType === ot.name && (
															<Badge>{t("providers.default")}</Badge>
														)}
														{typeHasDocker(ot) && <DockerBadge />}
													</span>
												))}
											</span>
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
					// 页面打开时发出的旧读取后到会把缓存还原，先取消再写回
					await qc.cancelQueries({ queryKey: qk.gitIdentity });
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
								await qc.cancelQueries({ queryKey: qk.gitIdentity });
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

/** omp 主配置文件：其中的 `cycleOrder` / `modelTags` 决定角色头像列表。 */
const ROLES_CONFIG = "config.yml";
/** 中央模型目录按这两个文件渲染：保存或删除后让目录重新读取（服务端按新配置重跑）。 */
const CATALOG_CONFIGS = new Set([ROLES_CONFIG, "models.yml"]);

function AgentConfigSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [result, setResult] = useState<
		Schemas["AgentConfigWriteResult"] | null
	>(null);
	// 来源须稳定：FileBrowser 以 sources 为依赖加载根。config.yml 决定 role 列表（身份与头像 → 角色头像），
	// 保存或删除它之后让那份列表重新取；models.yml / config.yml 变了让模型目录重新取
	const sources = useMemo(() => {
		const changed = (path: string) => {
			if (path === ROLES_CONFIG)
				void qc.invalidateQueries({ queryKey: identityKeys.roles });
			if (CATALOG_CONFIGS.has(path)) void invalidateModelCatalog(qc);
		};
		const base = agentConfigSource((r) => {
			setResult(r);
			changed(r.file.path);
		});
		return [
			{
				...base,
				async remove(path: string) {
					await base.remove(path);
					changed(path);
				},
			},
		];
	}, [qc]);
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

/**
 * 主题色色块：各预设 accent 在浅色 / 深色下的实际取值（与 index.css 的 `--accent-*` 一致），用 `light-dark()`
 * 随当前明暗显示。全站唯一允许的颜色字面量（另见 favicon）。
 */
const SWATCH: Record<AccentName, string> = {
	teal: "light-dark(oklch(0.53 0.105 205), oklch(0.78 0.105 205))",
	cobalt: "light-dark(oklch(0.53 0.17 258), oklch(0.78 0.17 258))",
	violet: "light-dark(oklch(0.53 0.17 295), oklch(0.78 0.17 295))",
	berry: "light-dark(oklch(0.53 0.17 358), oklch(0.78 0.17 358))",
	fox: "light-dark(oklch(0.55 0.16 48), oklch(0.78 0.16 48))",
	graphite: "light-dark(oklch(0.3 0.015 260), oklch(0.9 0.015 260))",
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
		<Card title={t("appearance.theme")} icon="palette">
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
						className="grid w-fit grid-cols-6 gap-x-1 gap-y-3 sm:gap-x-4"
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
									{/* 细内描边：色块和卡片底色接近时（深色下的石墨）也能一眼分辨边界 */}
									<span
										aria-hidden
										className={`grid h-7 w-7 place-items-center rounded-full text-ink-fg shadow-xs inset-ring-1 inset-ring-fg/20 ring-offset-2 ring-offset-panel transition-shadow duration-150 ${on ? "ring-2 ring-[var(--ring)]" : ""}`}
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
				<WallpaperPicker />
			</div>
		</Card>
	);
}

/**
 * 壁纸：内置三种 + 自定义图片。缩略卡挂 `.wp-<名>` 类，按当前主题色与明暗真实渲染；
 * 自定义卡的图片与纱厚度经内联变量传入（壁纸不是 custom 时 <html> 上没有这些变量）。
 * 还没有图片时「自定义」是上传入口而不是单选项，方向键不会停在它上面。
 */
function WallpaperPicker() {
	const { t } = useTranslation();
	const wallpaper = useTheme((s) => s.wallpaper);
	const tone = useTheme((s) => s.tone);
	const setWallpaper = useTheme((s) => s.setWallpaper);
	const [image, setImage] = useState(readWallpaperImage);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<Error | null>(null);
	const fileRef = useRef<HTMLInputElement>(null);
	const onFile = (e: ChangeEvent<HTMLInputElement>) => {
		const file = e.target.files?.[0];
		e.target.value = "";
		if (!file) return;
		setBusy(true);
		setError(null);
		// 代次记在 theme store 里：上传途中改选了别的壁纸，结果就作废；离开设置页（卸载）不影响
		const gen = themeStore.getState().beginWallpaperUpload();
		void processWallpaper(file)
			.then(({ url, tone }) => {
				// 存储失败时抛错，壁纸保持原样
				if (themeStore.getState().setCustomWallpaper(url, tone, gen))
					setImage(url);
			})
			.catch(() => {
				// 已被新的壁纸选择取代的上传，失败也不再提示
				if (themeStore.getState().wallpaperGen === gen)
					setError(new Error(t("appearance.wallpaperError")));
			})
			.finally(() => setBusy(false));
	};
	const thumb = (w: (typeof WALLPAPERS)[number], on: boolean) => (
		<span
			aria-hidden
			className={`wp-${w} block h-[60px] w-24 rounded-lg inset-ring-1 inset-ring-fg/15 ring-offset-2 ring-offset-panel transition-shadow duration-150 ${on ? "ring-2 ring-[var(--ring)]" : ""}`}
			style={
				w === "custom" && image
					? ({
							"--wallpaper-image": `url("${image}")`,
							"--wp-lo": tone?.[0] ?? 0.5,
							"--wp-hi": tone?.[1] ?? 0.5,
						} as CSSProperties)
					: undefined
			}
		/>
	);
	return (
		<div className="flex flex-col gap-2">
			<span aria-hidden className="font-medium text-muted text-xs">
				{t("appearance.wallpaper")}
			</span>
			<div
				role="radiogroup"
				aria-label={t("appearance.wallpaper")}
				onKeyDown={radioKeys}
				className="flex flex-wrap gap-x-3 gap-y-3"
			>
				{WALLPAPERS.map((w) => {
					const label = t(`appearance.wallpapers.${w}`);
					if (w === "custom" && !image)
						return (
							<button
								key={w}
								type="button"
								aria-label={`${label}：${t("appearance.wallpaperUpload")}`}
								disabled={busy}
								onClick={() => fileRef.current?.click()}
								className="flex flex-col items-center gap-1.5 rounded-md py-1 text-2xs text-muted transition-colors duration-150 hover:text-fg disabled:opacity-50"
							>
								<span
									aria-hidden
									className="grid h-[60px] w-24 place-items-center rounded-lg border border-border-strong border-dashed text-faint"
								>
									{busy ? <Spinner /> : <Icon name="upload" size={16} />}
								</span>
								<span aria-hidden>{label}</span>
							</button>
						);
					const on = wallpaper === w;
					return (
						// biome-ignore lint/a11y/useSemanticElements: 缩略卡外观的单选（WAI-ARIA radio），原生 radio 画不出
						<button
							key={w}
							type="button"
							role="radio"
							aria-checked={on}
							aria-label={label}
							tabIndex={on ? 0 : -1}
							onClick={() => setWallpaper(w)}
							className={`flex flex-col items-center gap-1.5 rounded-md py-1 text-2xs transition-colors duration-150 ${on ? "font-medium text-fg" : "text-muted hover:text-fg"}`}
						>
							{thumb(w, on)}
							<span aria-hidden>{label}</span>
						</button>
					);
				})}
			</div>
			{/* 没有图片时「自定义」磁贴就是上传入口；有图片后才出现「更换 / 移除」 */}
			<input
				ref={fileRef}
				type="file"
				accept="image/*"
				className="sr-only"
				tabIndex={-1}
				aria-hidden
				disabled={busy}
				onChange={onFile}
			/>
			{image && (
				<div className="flex flex-wrap items-center gap-2">
					<Button
						size="sm"
						icon="upload"
						disabled={busy}
						onClick={() => fileRef.current?.click()}
					>
						{t("appearance.wallpaperReplace")}
					</Button>
					<Button
						size="sm"
						variant="ghost"
						icon="trash"
						disabled={busy}
						onClick={() => {
							themeStore.getState().removeCustomWallpaper();
							setImage(null);
							setError(null);
						}}
					>
						{t("appearance.wallpaperRemove")}
					</Button>
				</div>
			)}
			<p className="text-faint text-xs">{t("appearance.wallpaperHint")}</p>
			<ErrorText error={error} />
		</div>
	);
}

// ---------------------------------------------------------------- 关于

function AboutSettings() {
	const { t, i18n } = useTranslation();
	const health = useHealth();
	const me = useMe();
	const navigate = useNavigate();
	const logout = useAsync();
	const mobile = host.mobile;
	const [mobileInfo, setMobileInfo] = useState<MobileInfo | null>(null);
	const [mobileError, setMobileError] = useState<unknown>(null);
	useEffect(() => {
		if (!mobile) return;
		let live = true;
		mobile.info().then(
			(i) => {
				if (live) setMobileInfo(i);
			},
			(e) => {
				if (live) setMobileError(e);
			},
		);
		return () => {
			live = false;
		};
		// 宿主在页面生命周期内不变
	}, []);
	const mobileRows = mobile
		? [
				{
					key: "appVersion",
					label: t("mobile.about.appVersion"),
					value: (
						<span className="font-mono">{mobileInfo?.version || "—"}</span>
					),
				},
				{
					key: "webview",
					label: t("mobile.about.webview"),
					value: (
						<span className="font-mono">{mobileInfo?.webview || "—"}</span>
					),
				},
				{
					key: "tokenStore",
					label: t("mobile.about.tokenStore"),
					value: mobileInfo
						? t(`mobile.about.tokenStoreKind.${mobileInfo.tokenStore}`)
						: "—",
				},
			]
		: [];
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
					...mobileRows,
				]}
			/>
			<CoordinatorAbout />
			{mobile && (
				<div className="mt-4 flex flex-col gap-1.5 border-border border-t pt-3">
					<ErrorText error={mobileError} />
					<div>
						<Button
							variant="danger"
							onClick={() => mobile.changeServer()}
							data-testid="mobile-change-server"
						>
							{t("desktop.changeServer")}
						</Button>
					</div>
					<p className="text-faint text-xs">{t("desktop.changeServerHint")}</p>
				</div>
			)}
			{/* 退出登录只给纯浏览器：两种壳里的会话由壳的设备令牌换来，退出后壳会立刻再换一个，改用「更换服务器」 */}
			{host.desktop === null && host.mobile === null && (
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
			)}
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
	const sidebarToggle = useSidebarToggleShown();
	// 桌面 App 无系统标题栏：页头可拖动，两端按需放窗口控件
	const chromeLeft = !useSidebarShown();
	// 「桌面应用」分区只在桌面壳里有：浏览器与移动壳都没有
	const desktop = host.desktop !== null;
	if (section === "desktop" && !desktop)
		return <Navigate to="/settings/devices" replace />;
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header
				data-window-drag=""
				className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
			>
				{chromeLeft && <ChromeLead />}
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
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
				<span className="min-w-0 flex-1" />
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div className="min-h-0 flex-1 overflow-y-auto p-3 pb-safe sm:p-5">
				<div className="mx-auto flex max-w-3xl flex-col gap-3">
					{section === "devices" && <DevicesSettings />}
					{section === "providers" && <ProvidersSettings />}
					{section === "runners" && <RunnersSettings />}
					{section === "secrets" && <SecretsSettings />}
					{section === "browser-profiles" && <BrowserProfilesSettings />}
					{section === "git-identity" && <GitIdentitySettings />}
					{section === "agent-skills" && <SkillsPage />}
					{section === "agent-prompts" && <PromptsPage />}
					{section === "agent-commands" && <CollectionsPage />}
					{section === "agent-extensions" && <ExtensionsPage />}
					{section === "agent-config" && <AgentConfigSettings />}
					{section === "identity" && <IdentitySettings />}
					{section === "appearance" && <ThemeSettings />}
					{section === "notifications" && <NotificationsSettings />}
					{section === "shares" && <SharesSettings />}
					{section === "automations" && <AutomationsSettings />}
					{section === "desktop" && <DesktopSettings />}
					{section === "about" && <AboutSettings />}
				</div>
			</div>
		</Sheet>
	);
}
