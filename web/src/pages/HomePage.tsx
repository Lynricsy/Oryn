/**
 * 首页 = 新建线程框（居中玻璃卡，按最新 Amp）：提示词 + 附件（图片另带内联副本）；底部胶囊——执行位置（Ctrl+M）、项目（Ctrl+J）、
 * 模型与思考级别（Ctrl+S）、发送。省略 provider / 类型时由控制面依次取项目默认、orb_defaults、provider 默认。
 * 托盘背后是一层主题色光场（.home-field），玻璃居中时也有东西可模糊；发送后托盘经 DockTray 连续变形到线程页底部输入坞。
 */
import {
	type ReactElement,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Schemas } from "../api/client";
import { useOrbs, useProjects, useProviders } from "../api/queries";
import {
	AttachmentStrip,
	useAttachments,
} from "../components/composer/attachments";
import { morphToThread } from "../components/composer/DockTray";
import { setPendingPrompt } from "../components/transcript/pending-prompt";
import { Badge, BrandMark, ProjectBadge, Sheet } from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import { ModelLogo } from "../components/ui/ModelLogo";
import { OrbBead } from "../components/ui/OrbBead";
import { IconButton, Spinner, TextInput } from "../components/ui/primitives";
import { ThinkingDial } from "../components/ui/ThinkingDial";
import { formatMemoryMB } from "../lib/format";
import { modelBrand, shortModelName } from "../lib/model-brand";
import { useBreakpoint } from "../lib/viewport";
import { createThread } from "../state/actions";
import { useIndex } from "../state/stores";
import { uiStore, useSidebarToggleShown } from "../state/ui";

type Pill = "executor" | "project" | "model";

const THINKING_LEVELS = [
	"auto",
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;

type Executor = Schemas["ThreadExecutor"];

/** 首次出场动效只在本次页面会话里第一次挂载首页时播放 */
let introPlayed = false;

/** 胶囊文案：主文字 + 可选徽标；`label` 是整体无障碍名（保持原拼接字符串）。 */
interface PillText {
	label: string;
	main: string;
	badge?: string;
	/** 主文字后的图标（模型胶囊的思考级别圆圈）；圆形按钮里缩成右下角角标 */
	trail?: ReactNode;
}

/**
 * 底部选项按钮；快捷键只放进悬停提示，输入框里不显示按键提示。
 * `pill`：图标 + 名字，放不下时文字截断；`circle`：窄屏上 orb、模型（以及以后的浏览器 profile）
 * 只留一个 32px 小圆圈，名字靠 aria-label 与悬停提示，把横向空间让给项目名。
 */
function PillButton({
	icon,
	text,
	shortcut,
	onClick,
	active,
	shape = "pill",
}: {
	/** 图标名，或现成的装饰节点（如模型 logo） */
	icon: IconName | ReactElement;
	text: PillText;
	shortcut: string;
	onClick: () => void;
	active: boolean;
	shape?: "pill" | "circle";
}) {
	const circle = shape === "circle";
	return (
		<button
			type="button"
			onClick={onClick}
			aria-expanded={active}
			aria-label={text.label}
			title={`${text.label} (${shortcut})`}
			className={`relative inline-flex h-8 items-center rounded-full border text-xs shadow-xs transition-colors duration-150 ${circle ? "w-8 shrink-0 justify-center" : "min-w-0 max-w-60 shrink gap-1.5 px-3"} ${active ? "border-border-strong bg-hover text-fg" : "border-border bg-panel/70 text-muted hover:bg-hover hover:text-fg"}`}
		>
			<span className="inline-flex shrink-0">
				{typeof icon === "string" ? (
					<Icon name={icon} size={circle ? 15 : 13} />
				) : (
					icon
				)}
			</span>
			{!circle && <span className="min-w-0 truncate">{text.main}</span>}
			{!circle && text.badge && (
				<span className="inline-flex shrink-0">
					<Badge>{text.badge}</Badge>
				</span>
			)}
			{text.trail &&
				(circle ? (
					<span className="-right-0.5 -bottom-0.5 absolute inline-flex rounded-full bg-panel p-px">
						{text.trail}
					</span>
				) : (
					text.trail
				))}
		</button>
	);
}

function Popover({
	children,
	onClose,
}: {
	children: ReactNode;
	onClose: () => void;
}) {
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node)) onClose();
		};
		document.addEventListener("mousedown", onDown);
		return () => document.removeEventListener("mousedown", onDown);
	}, [onClose]);
	return (
		<div
			ref={ref}
			className="glass pop-in absolute bottom-full left-0 z-30 mb-2 max-h-80 w-80 max-w-[calc(100vw-2rem)] overflow-y-auto rounded-lg p-1"
		>
			{children}
		</div>
	);
}

function PopoverLabel({ children }: { children: ReactNode }) {
	return (
		<div className="px-2 pt-2 pb-1 font-medium text-2xs text-faint first:pt-1">
			{children}
		</div>
	);
}

/**
 * 弹层选项：主文字紧跟 hint 文字、同在一个按钮里（项目选项的无障碍名 = 项目名 + 仓库地址，验收脚本按此匹配）。
 * `leading` 只能放无文字的图标，一律 aria-hidden；`trailing` 放徽标组。
 */
function Option({
	selected,
	onClick,
	children,
	leading,
	hint,
	trailing,
	mono,
}: {
	selected: boolean;
	onClick: () => void;
	children: ReactNode;
	leading?: ReactNode;
	hint?: ReactNode;
	trailing?: ReactNode;
	mono?: boolean;
}) {
	return (
		<button
			type="button"
			onClick={onClick}
			className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover ${selected ? "bg-hover text-fg" : "text-fg"}`}
		>
			{leading && (
				<span
					aria-hidden
					className="inline-flex w-[18px] shrink-0 justify-center text-faint"
				>
					{leading}
				</span>
			)}
			<span className="flex min-w-0 flex-1 items-baseline gap-2">
				<span
					className={`min-w-0 shrink truncate ${mono ? "font-mono text-xs" : ""}`}
				>
					{children}
				</span>
				{hint && (
					<span className="min-w-0 shrink-[2] truncate text-faint text-xs">
						{hint}
					</span>
				)}
			</span>
			{trailing && (
				<span className="flex shrink-0 items-center gap-1">{trailing}</span>
			)}
			{selected && (
				<Icon name="check" size={13} className="shrink-0 text-muted" />
			)}
		</button>
	);
}

export function HomePage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const compact = bp === "compact";
	const sidebarToggle = useSidebarToggleShown();
	const providers = useProviders();
	const projects = useProjects();
	const orbs = useOrbs();
	const threads = useIndex((s) => s.threads);
	const att = useAttachments();
	const [prompt, setPrompt] = useState("");
	const [pill, setPill] = useState<Pill | null>(null);
	const [executor, setExecutor] = useState<Executor>({ kind: "new_orb" });
	const [projectId, setProjectId] = useState<string | undefined>(undefined);
	const [model, setModel] = useState("");
	const [thinking, setThinking] = useState<string>("");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);
	const trayRef = useRef<HTMLDivElement>(null);
	// 首次出场：品牌标、标题、输入卡依次上移淡入（仅本次页面会话里第一次挂载）
	const [intro] = useState(() => !introPlayed);

	useEffect(() => {
		introPlayed = true;
		ref.current?.focus();
	}, []);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (!(e.ctrlKey || e.metaKey)) return;
			const k = e.key.toLowerCase();
			const map: Record<string, Pill> = {
				m: "executor",
				j: "project",
				s: "model",
			};
			const p = map[k];
			if (p) {
				e.preventDefault();
				setPill((cur) => (cur === p ? null : p));
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const recentModels = useMemo(() => {
		const set = new Set<string>();
		for (const th of Object.values(threads))
			if (th.model) set.add(`${th.model.provider}/${th.model.id}`);
		return [...set].slice(0, 8);
	}, [threads]);

	const liveOrbs = (orbs.data?.orbs ?? []).filter(
		(o) =>
			o.state !== "deleted" && o.state !== "deleting" && o.state !== "failed",
	);
	// 胶囊的无障碍名保持原拼接字符串；视觉上把「·」后的部分拆成徽标
	const existingOrbLabel =
		executor.kind === "orb"
			? t("newThread.existingOrb", { id: executor.orbId.slice(0, 12) })
			: "";
	const executorText: PillText =
		executor.kind === "orb"
			? { label: existingOrbLabel, main: existingOrbLabel }
			: executor.provider
				? {
						label: `${executor.provider}${executor.type ? ` · ${executor.type}` : ""}`,
						main: executor.provider,
						badge: executor.type,
					}
				: {
						label: t("newThread.newOrbDefault"),
						main: t("newThread.newOrbDefault"),
					};
	const projectName = projectId
		? (projects.data?.projects.find((p) => p.id === projectId)?.name ??
			projectId)
		: t("newThread.noProject");
	const projectText: PillText = { label: projectName, main: projectName };
	// 与线程输入坞的 ModelChip 同一套省空间规则：认得出品牌时去掉系列名（logo 已说明），思考级别画成圆圈；
	// 胶囊的无障碍名与悬停提示仍是全名加级别
	const modelId = model.trim().split("/").pop()?.replace(/:.*$/, "") || model;
	const modelText: PillText = {
		label: model
			? `${model}${thinking ? ` · ${thinking}` : ""}`
			: thinking
				? `${t("newThread.defaultModel")} · ${thinking}`
				: t("newThread.defaultModel"),
		main: model
			? shortModelName(modelId, modelBrand(model))
			: t("newThread.defaultModel"),
		trail: thinking ? (
			<ThinkingDial level={thinking} className="text-muted" />
		) : undefined,
	};
	const rise = (delay: string) =>
		intro
			? `transition-[opacity,translate] duration-300 ease-out starting:translate-y-2 starting:opacity-0 ${delay}`
			: "";

	const submit = async () => {
		const text = prompt.trim();
		if (!text && att.attachmentIds.length === 0) return;
		if (att.uploading) {
			setError(t("attachments.waitUpload"));
			return;
		}
		setBusy(true);
		setError(null);
		try {
			const slash = model.indexOf("/");
			const body: Schemas["CreateThreadRequest"] = {
				prompt: text,
				executor,
				...(projectId ? { projectId } : {}),
				...(att.contents.length > 0 ? { images: att.contents } : {}),
				...(att.attachmentIds.length > 0
					? { attachments: att.attachmentIds }
					: {}),
				...(slash > 0
					? {
							model: {
								provider: model.slice(0, slash),
								id: model.slice(slash + 1),
							},
						}
					: {}),
				...(thinking ? { thinkingLevel: thinking } : {}),
			};
			const thread = await createThread(body);
			setPendingPrompt(thread.id, text);
			// 触屏：发送即收起软键盘，线程页也不再抢焦点（否则键盘会先收后弹闪一下）
			if (window.matchMedia?.("(pointer: coarse)").matches) ref.current?.blur();
			// 托盘连续变形到线程页输入坞；旧快照里仍是刚发出的文字，清空放到导航那一刻
			morphToThread(trayRef.current, () => {
				setPrompt("");
				att.clear();
				navigate(`/t/${thread.id}`);
			});
		} catch (e) {
			setError(e instanceof Error ? e.message : String(e));
		} finally {
			setBusy(false);
		}
	};

	return (
		<Sheet inset={bp === "expanded"} className="flex h-full flex-col">
			{sidebarToggle && (
				<header className="flex h-12 shrink-0 items-center gap-1.5 px-2">
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
					{/* 与桌面侧栏顶部同一套品牌：品牌标 + 衬线字标，按 48px 顶栏缩到 20px */}
					<span className="flex items-center gap-1.5">
						<BrandMark size={20} />
						<span className="wordmark text-[20px] text-fg leading-none">
							{t("app.name")}
						</span>
					</span>
				</header>
			)}
			<div className="flex flex-1 items-start justify-center overflow-y-auto overflow-x-hidden px-3 pt-[10vh] pb-keyboard sm:items-center sm:pt-0">
				<div className="w-full max-w-2xl">
					<div className={`mb-3 flex justify-center ${rise("")}`}>
						<BrandMark size={32} blink />
					</div>
					<h1
						className={`mb-5 text-center font-semibold text-fg text-xl tracking-tight ${rise("delay-[60ms]")}`}
					>
						{t("newThread.heading")}
					</h1>
					{/* 与线程页输入坞同一块 D 稿托盘：同一材质（glass-dock）、同一团主题色晕（dock-glow）、同样的分行；首页大一档圆角（18px） */}
					<div
						className={`dock-glow relative isolate ${rise("delay-[120ms]")}`}
					>
						{/* 托盘背后的光场（一圈宽而淡的光晕 + 两团晕）：玻璃底下有东西可模糊，居中态也看得出是玻璃 */}
						<div aria-hidden className="home-field" />
						<div
							ref={trayRef}
							className="glass glass-dock rounded-2xl transition-[border-color] duration-150 has-[textarea:focus]:border-[var(--ring)]"
						>
							<AttachmentStrip items={att.items} onRemove={att.remove} />
							<textarea
								onDragOver={(e) => {
									if (e.dataTransfer.types.includes("Files"))
										e.preventDefault();
								}}
								onDrop={(e) => {
									if (e.dataTransfer.files.length === 0) return;
									e.preventDefault();
									void att.addFiles(e.dataTransfer.files);
								}}
								ref={ref}
								rows={4}
								value={prompt}
								aria-label={t("newThread.prompt")}
								placeholder={t("newThread.placeholder")}
								className="block w-full resize-none bg-transparent px-4 pt-[13px] pb-1.5 text-[14px] leading-5 outline-none placeholder:text-faint focus-visible:outline-none"
								onChange={(e) => setPrompt(e.target.value)}
								onPaste={(e) => {
									const files = [...e.clipboardData.files];
									if (files.length > 0) {
										e.preventDefault();
										void att.addFiles(files);
									}
								}}
								onKeyDown={(e) => {
									if (
										e.key === "Enter" &&
										!e.shiftKey &&
										!e.nativeEvent.isComposing
									) {
										e.preventDefault();
										void submit();
									}
								}}
							/>
							{/*
							 * 工具行。宽屏一行：附件 + 分隔、选项胶囊、发送钮。
							 * 窄屏两行：附件与发送钮在第一行两端；选项单独占第二行
							 * （orb、模型是小圆圈，项目名占剩下的宽度），互不挤占。
							 */}
							<div
								className={`relative flex px-2 pb-2 ${compact ? "flex-wrap items-center gap-y-1.5" : "items-start gap-0.5"}`}
							>
								<div className="flex h-8 shrink-0 items-center">
									<label className="inline-flex h-[30px] w-[30px] shrink-0 cursor-pointer items-center justify-center rounded-md text-faint transition-colors duration-150 hover:bg-hover hover:text-fg">
										<Icon name="attach" size={15} />
										<span className="sr-only">{t("attachments.attach")}</span>
										<input
											type="file"
											multiple
											className="hidden"
											onChange={(e) => {
												if (e.target.files) void att.addFiles(e.target.files);
												e.target.value = "";
											}}
										/>
									</label>
									{!compact && (
										<span
											aria-hidden
											className="mx-1.5 h-[18px] w-px shrink-0 bg-[var(--glass-line)]"
										/>
									)}
								</div>
								<div
									className={`flex min-h-8 min-w-0 items-center gap-1.5 ${compact ? "order-last w-full" : "flex-1"}`}
								>
									<PillButton
										icon="box"
										text={executorText}
										shortcut="Ctrl+M"
										active={pill === "executor"}
										onClick={() =>
											setPill(pill === "executor" ? null : "executor")
										}
										shape={compact ? "circle" : "pill"}
									/>
									<PillButton
										icon="folder"
										text={projectText}
										shortcut="Ctrl+J"
										active={pill === "project"}
										onClick={() =>
											setPill(pill === "project" ? null : "project")
										}
									/>
									<PillButton
										icon={<ModelLogo model={model} size={compact ? 16 : 13} />}
										text={modelText}
										shortcut="Ctrl+S"
										active={pill === "model"}
										onClick={() => setPill(pill === "model" ? null : "model")}
										shape={compact ? "circle" : "pill"}
									/>
								</div>
								<button
									type="button"
									title={t("newThread.start")}
									aria-label={t("composer.send")}
									disabled={
										busy ||
										att.uploading ||
										(!prompt.trim() && att.attachmentIds.length === 0)
									}
									onClick={() => void submit()}
									// 与线程页发送钮相同：强调色实心圆角方块，空态只降不透明度
									className={`${compact ? "ml-auto" : "ml-1.5"} inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent text-accent-fg shadow-xs transition-[scale,filter,opacity] duration-150 ease-out hover:brightness-110 active:scale-95 disabled:pointer-events-none disabled:opacity-45 disabled:shadow-none`}
								>
									{busy ? (
										<Spinner size={14} />
									) : (
										<Icon name="send" size={16} weight="bold" />
									)}
								</button>
								{pill === "executor" && (
									<Popover onClose={() => setPill(null)}>
										<PopoverLabel>{t("newThread.newOrb")}</PopoverLabel>
										<Option
											selected={
												executor.kind === "new_orb" && !executor.provider
											}
											leading={<Icon name="box" size={14} />}
											onClick={() => {
												setExecutor({ kind: "new_orb" });
												setPill(null);
											}}
										>
											{t("newThread.newOrbDefault")}
										</Option>
										{(providers.data?.providers ?? []).map((p) =>
											p.orbTypes.map((ot) => (
												<Option
													key={`${p.id}-${ot.name}`}
													selected={
														executor.kind === "new_orb" &&
														executor.provider === p.name &&
														executor.type === ot.name
													}
													leading={<Icon name="cpu" size={14} />}
													hint={ot.name}
													trailing={
														<>
															<Badge>{t("panel.cores", { n: ot.cpus })}</Badge>
															<Badge>{formatMemoryMB(ot.memoryMB)}</Badge>
															{!p.online && (
																<Badge tone="warn">
																	{t("providers.offline")}
																</Badge>
															)}
														</>
													}
													onClick={() => {
														setExecutor({
															kind: "new_orb",
															provider: p.name,
															type: ot.name,
														});
														setPill(null);
													}}
												>
													{p.name}
												</Option>
											)),
										)}
										{liveOrbs.length > 0 && (
											<PopoverLabel>{t("newThread.existingOrbs")}</PopoverLabel>
										)}
										{liveOrbs.map((o) => (
											<Option
												key={o.id}
												selected={
													executor.kind === "orb" && executor.orbId === o.id
												}
												leading={<OrbBead state={o.state} />}
												mono
												trailing={<Badge>{o.type}</Badge>}
												onClick={() => {
													setExecutor({ kind: "orb", orbId: o.id });
													setPill(null);
												}}
											>
												{o.id.slice(0, 16)}
											</Option>
										))}
									</Popover>
								)}
								{pill === "project" && (
									<Popover onClose={() => setPill(null)}>
										<Option
											selected={!projectId}
											leading={<Icon name="folder" size={14} />}
											onClick={() => {
												setProjectId(undefined);
												setPill(null);
											}}
										>
											{t("newThread.noProject")}
										</Option>
										{(projects.data?.projects ?? [])
											.filter((p) => !p.archivedAt)
											.map((p) => (
												<Option
													key={p.id}
													selected={projectId === p.id}
													leading={<ProjectBadge name={p.name} icon={p.icon} />}
													hint={p.repoUrl?.replace(/^https?:\/\//, "")}
													onClick={() => {
														setProjectId(p.id);
														setPill(null);
													}}
												>
													{p.name}
												</Option>
											))}
									</Popover>
								)}
								{pill === "model" && (
									<Popover onClose={() => setPill(null)}>
										<div className="p-1">
											<TextInput
												autoFocus
												placeholder={t("newThread.modelPlaceholder")}
												value={model}
												onChange={(e) => setModel(e.target.value)}
											/>
										</div>
										<Option
											selected={!model}
											leading={<Icon name="sparkle" size={14} />}
											onClick={() => setModel("")}
										>
											{t("newThread.defaultModel")}
										</Option>
										{recentModels.map((m) => (
											<Option
												key={m}
												selected={model === m}
												leading={<ModelLogo model={m} />}
												onClick={() => setModel(m)}
											>
												{m}
											</Option>
										))}
										<PopoverLabel>{t("model.thinking")}</PopoverLabel>
										<div className="flex flex-wrap gap-1 px-2 pb-2">
											{["", ...THINKING_LEVELS].map((l) => (
												<button
													key={l || "default"}
													type="button"
													aria-pressed={thinking === l}
													onClick={() => setThinking(l)}
													className={`h-6 rounded-md border px-2 text-xs transition-colors duration-150 ${thinking === l ? "border-border-strong bg-hover text-fg" : "border-border text-muted hover:bg-hover hover:text-fg"}`}
												>
													{l || t("newThread.defaultThinking")}
												</button>
											))}
										</div>
									</Popover>
								)}
							</div>
						</div>
						{(error || att.error) && (
							<p
								role="alert"
								className="mt-2 flex items-center justify-center gap-1.5 text-err text-sm"
							>
								<Icon name="alert" size={13} className="shrink-0" />
								{error ?? att.error}
							</p>
						)}
					</div>
				</div>
			</div>
		</Sheet>
	);
}
