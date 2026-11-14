/**
 * 首页 = 新建线程框（居中玻璃卡，按最新 Amp）：提示词 + 附件（图片另带内联副本）；底部胶囊——执行位置（Ctrl+M）、
 * 浏览器 Profile（Ctrl+B，M8；只在新 orb 且至少有一个 Profile 时出现）、项目（Ctrl+J）、模型与思考级别（Ctrl+S）、发送。
 * 省略 provider / 类型时由控制面依次取项目默认、orb_defaults、provider 默认。
 * 选的 Profile 被另一台运行中的 orb 占用（409 `profile_in_use`）时弹选择对话框，选「只读使用」或「接管」后带 `profileMode` 重发。
 * 托盘背后是一层主题色光场（.home-field），玻璃居中时也有东西可模糊；发送后托盘经 DockTray 连续变形到线程页底部输入坞。
 */
import {
	type ReactElement,
	type ReactNode,
	type Ref,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Schemas } from "../api/client";
import {
	useBrowserProfiles,
	useOrbs,
	useProjects,
	useProviders,
} from "../api/queries";
import {
	AttachButton,
	AttachmentStrip,
	useAttachments,
} from "../components/composer/attachments";
import { morphToThread } from "../components/composer/DockTray";
import {
	type ConflictChoice,
	ProfileConflictDialog,
} from "../components/desktop/ProfileConflictDialog";
import {
	type ProfileConflict,
	profileConflictOf,
} from "../components/desktop/profile-status";
import {
	ChromeLead,
	useChromeSides,
	WindowControls,
} from "../components/layout/WindowControls";
import { setPendingPrompt } from "../components/transcript/pending-prompt";
import { Badge, BrandMark, ProjectBadge, Sheet } from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import { ModelLogo } from "../components/ui/ModelLogo";
import {
	ModelOptions,
	ModelPill,
	modelPillText,
	newThreadThinkingOptions,
} from "../components/ui/ModelSelect";
import { OrbBead } from "../components/ui/OrbBead";
import { Option, Popover, PopoverLabel } from "../components/ui/PickerPopover";
import { IconButton, Spinner } from "../components/ui/primitives";
import { formatMemoryMB } from "../lib/format";
import { useBreakpoint } from "../lib/viewport";
import { createThread } from "../state/actions";
import { useModelCaps } from "../state/model-caps";
import { shareIntakeStore, useShareIntake } from "../state/share-intake";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

type Pill = "executor" | "profile" | "project" | "model";

type Executor = Schemas["ThreadExecutor"];

/** 新线程的浏览器 Profile：`null` = 跟随项目默认（不带字段），`""` = 不使用，`bpf_…` = 指定。 */
type ProfilePick = string | null;

/** 焦点在输入框里（Ctrl+B 在这里切 Profile 弹层；别处由全局快捷键开关侧栏）。 */
function inEditable(target: EventTarget | null): boolean {
	return (
		target instanceof HTMLElement &&
		!!target.closest("input, textarea, select, [contenteditable]")
	);
}

/** 首次出场动效只在本次页面会话里第一次挂载首页时播放 */
let introPlayed = false;

/** 胶囊文案：主文字 + 可选徽标；`label` 是整体无障碍名（保持原拼接字符串）。 */
interface PillText {
	label: string;
	main: string;
	badge?: string;
}

/**
 * 底部选项按钮；快捷键只放进悬停提示，输入框里不显示按键提示。
 * `pill`：图标 + 名字，放不下时文字截断；`circle`：窄屏上 orb（以及以后的浏览器 profile）只留一个
 * 32px 小圆圈，模型在空间不够时也缩成圆圈；名字靠 aria-label 与悬停提示，把横向空间让给项目名。
 */
function PillButton({
	ref,
	icon,
	text,
	shortcut,
	onClick,
	active,
	shape = "pill",
	grow = false,
	reserveEnd = false,
}: {
	ref?: Ref<HTMLButtonElement>;
	/** 图标名，或现成的装饰节点（如模型 logo） */
	icon: IconName | ReactElement;
	text: PillText;
	shortcut: string;
	onClick: () => void;
	active: boolean;
	shape?: "pill" | "circle";
	/** 不设 240px 上限，有多少剩余宽度用多少（窄屏的项目名） */
	grow?: boolean;
	/** 右端给叠在胶囊里的思考级别按钮留位置（ModelPill） */
	reserveEnd?: boolean;
}) {
	const circle = shape === "circle";
	return (
		<button
			ref={ref}
			type="button"
			onClick={onClick}
			aria-expanded={active}
			aria-label={text.label}
			title={`${text.label} (${shortcut})`}
			className={`inline-flex h-8 items-center rounded-full border text-xs shadow-xs transition-colors duration-150 ${circle ? "w-8 shrink-0 justify-center" : `min-w-0 shrink gap-1.5 pl-3 ${reserveEnd ? "pr-8" : "pr-3"} ${grow ? "max-w-none" : "max-w-60"}`} ${active ? "border-border-strong bg-hover text-fg" : "border-border bg-panel/70 text-muted hover:bg-hover hover:text-fg"}`}
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
		</button>
	);
}

export function HomePage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const compact = bp === "compact";
	const sidebarToggle = useSidebarToggleShown();
	// 桌面 App 无系统标题栏：首页是最右侧（侧栏没展开时也是最左侧）的表面，页头承载窗口控件
	const chrome = useChromeSides();
	const chromeLeft = !useSidebarShown() && chrome.left !== null;
	const header = sidebarToggle || chromeLeft || chrome.right !== null;
	const providers = useProviders();
	const projects = useProjects();
	const orbs = useOrbs();
	const profiles = useBrowserProfiles();
	const att = useAttachments();
	const [prompt, setPrompt] = useState("");
	const [pill, setPill] = useState<Pill | null>(null);
	const [executor, setExecutor] = useState<Executor>({ kind: "new_orb" });
	const [projectId, setProjectId] = useState<string | undefined>(undefined);
	const [model, setModel] = useState("");
	const [thinking, setThinking] = useState<string>("");
	const [profilePick, setProfilePick] = useState<ProfilePick>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [conflict, setConflict] = useState<ProfileConflict | null>(null);
	const [conflictBusy, setConflictBusy] = useState<ConflictChoice | null>(null);
	const [conflictError, setConflictError] = useState<unknown>(null);
	// 撞上冲突的那次请求（冲突重试只在它上面加使用方式，不再按当前胶囊重新构造）
	const conflictReq = useRef<{
		body: Schemas["CreateThreadRequest"];
		text: string;
	} | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);
	const trayRef = useRef<HTMLDivElement>(null);
	// 首次出场：品牌标、标题、输入卡依次上移淡入（仅本次页面会话里第一次挂载）
	const [intro] = useState(() => !introPlayed);

	useEffect(() => {
		introPlayed = true;
		ref.current?.focus();
	}, []);

	// 系统分享进来的内容（M7 计划 4.6）：先取走（StrictMode 重跑 effect 时不会重复预填），
	// 文字追加进提示词、已上传的附件加入条目，导入失败的文件给出提示
	const pendingShare = useShareIntake((s) => s.pending);
	const addUploaded = att.addUploaded;
	useEffect(() => {
		if (!pendingShare) return;
		const share = shareIntakeStore.getState().take();
		if (!share) return;
		const text = share.text;
		if (text) setPrompt((p) => (p ? `${p}\n${text}` : text));
		addUploaded(share.attachments);
		if (share.failed.length > 0)
			setError(t("mobile.shareFailed", { count: share.failed.length }));
	}, [pendingShare, addUploaded, t]);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (!(e.ctrlKey || e.metaKey)) return;
			const k = e.key.toLowerCase();
			const map: Record<string, Pill> = {
				m: "executor",
				j: "project",
				s: "model",
				b: "profile",
			};
			const p = map[k];
			// Ctrl+B 只在输入框里切 Profile 弹层（且胶囊在时）；其余时候留给全局的侧栏开关
			if (p === "profile" && (!inEditable(e.target) || !showProfileRef.current))
				return;
			if (p) {
				e.preventDefault();
				setPill((cur) => (cur === p ? null : p));
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const profileList = profiles.data?.profiles ?? [];
	// Profile 胶囊：新 orb 且至少有一个 Profile 时出现（挂到已有 orb 时沿用那台 orb 的 Profile）
	const showProfile = executor.kind === "new_orb" && profileList.length > 0;
	const showProfileRef = useRef(showProfile);
	showProfileRef.current = showProfile;
	const defaultProfileId = projects.data?.projects.find(
		(p) => p.id === projectId,
	)?.defaultBrowserProfileId;
	const defaultProfileName = defaultProfileId
		? (profileList.find((p) => p.id === defaultProfileId)?.name ??
			defaultProfileId)
		: undefined;
	const pickedProfileName =
		profilePick === null
			? defaultProfileName
			: profilePick === ""
				? undefined
				: (profileList.find((p) => p.id === profilePick)?.name ?? profilePick);
	const profileText: PillText =
		profilePick === null
			? {
					label: t("newThread.profileDefaultLabel", {
						name: defaultProfileName ?? t("common.none"),
					}),
					main: defaultProfileName ?? t("newThread.profileNone"),
				}
			: {
					label: t("newThread.profileLabel", {
						name: pickedProfileName ?? t("newThread.profileNone"),
					}),
					main: pickedProfileName ?? t("newThread.profileNone"),
				};
	// 协调 orb（M9，跑在控制面宿主上）不能挂工作线程，也不出现在执行位置里
	const liveOrbs = (orbs.data?.orbs ?? []).filter(
		(o) =>
			!o.coordinator &&
			o.state !== "deleted" &&
			o.state !== "deleting" &&
			o.state !== "failed",
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
	// 与线程输入坞的 ModelChip 同一套省空间规则；思考级别是胶囊旁的独立圆圈按钮（ModelPill）
	const modelText: PillText = modelPillText(model, t("newThread.defaultModel"));
	const modelCaps = useModelCaps(model);
	const thinkingOptions = newThreadThinkingOptions(modelCaps);
	// 换了模型、原来选的级别新模型不支持（或不推理）时退回「跟随默认」
	useEffect(() => {
		if (thinking && !thinkingOptions?.includes(thinking)) setThinking("");
	}, [thinking, thinkingOptions]);
	// 窄屏选项行：项目名先拿够自然宽度，剩下的还放得下模型名才展开模型，否则模型只留圆圈。
	// 模型名的自然宽度量一个不可见的同款胶囊（ghost），避免展开/收起来回抖动。
	const pillsRef = useRef<HTMLDivElement>(null);
	const profilePillRef = useRef<HTMLButtonElement>(null);
	const projectPillRef = useRef<HTMLButtonElement>(null);
	const modelGhostRef = useRef<HTMLSpanElement>(null);
	const [modelFolded, setModelFolded] = useState(true);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 项目名或模型名变了要重新测量（ResizeObserver 不报文字截断量变化）
	useLayoutEffect(() => {
		const row = pillsRef.current;
		const proj = projectPillRef.current;
		const ghost = modelGhostRef.current;
		if (!compact || !row || !proj || !ghost) return;
		const measure = () => {
			const text = proj.querySelector<HTMLElement>(".truncate");
			if (!text) return;
			const projNatural =
				proj.offsetWidth - text.clientWidth + text.scrollWidth;
			const cs = getComputedStyle(row);
			const gap = Number.parseFloat(cs.columnGap) || 0;
			// clientWidth 含左右内边距，子元素实际能用的是内容盒宽度
			const avail =
				row.clientWidth -
				(Number.parseFloat(cs.paddingLeft) || 0) -
				(Number.parseFloat(cs.paddingRight) || 0);
			const orb = row.firstElementChild as HTMLElement | null;
			// Profile 胶囊（窄屏是小圆圈）在时也占一格
			const profile = profilePillRef.current;
			const need =
				(orb?.offsetWidth ?? 0) +
				gap +
				(profile ? profile.offsetWidth + gap : 0) +
				projNatural +
				gap +
				ghost.offsetWidth;
			setModelFolded(need > avail);
		};
		measure();
		const ro = new ResizeObserver(measure);
		ro.observe(row);
		return () => ro.disconnect();
	}, [compact, projectText.main, modelText.main, showProfile]);
	const rise = (delay: string) =>
		intro
			? `transition-[opacity,translate] duration-300 ease-out starting:translate-y-2 starting:opacity-0 ${delay}`
			: "";

	/** 发送当前输入；Profile 被占用时记下这次请求并弹冲突对话框。 */
	const submit = async () => {
		const text = prompt.trim();
		if (!text && att.attachmentIds.length === 0) return;
		if (att.uploading) {
			setError(t("attachments.waitUpload"));
			return;
		}
		const slash = model.indexOf("/");
		await send(
			{
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
				...(executor.kind === "new_orb" && profilePick !== null
					? { browserProfileId: profilePick }
					: {}),
			},
			text,
		);
	};

	/**
	 * 冲突对话框里选了使用方式：重发撞上冲突的那次请求，并钉住对话框展示的 Profile。
	 * 「跟随项目默认」的请求不带 id，若照旧重发，服务端会按当时的项目默认重新解析；
	 * 期间默认被另一台设备改掉的话，接管的就成了用户没看到的另一个 Profile。
	 */
	const resolveConflict = (mode: ConflictChoice) => {
		const req = conflictReq.current;
		if (!conflict || !req) return;
		void send(
			{ ...req.body, browserProfileId: conflict.profileId, profileMode: mode },
			req.text,
			mode,
		);
	};

	const send = async (
		body: Schemas["CreateThreadRequest"],
		text: string,
		mode?: ConflictChoice,
	) => {
		setBusy(true);
		setError(null);
		if (mode) {
			setConflictBusy(mode);
			setConflictError(null);
		}
		try {
			const thread = await createThread(body);
			setConflict(null);
			conflictReq.current = null;
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
			const c = profileConflictOf(e);
			if (mode) setConflictError(e);
			else if (c) {
				conflictReq.current = { body, text };
				setConflict(c);
			} else setError(e instanceof Error ? e.message : String(e));
		} finally {
			setBusy(false);
			setConflictBusy(null);
		}
	};

	return (
		<Sheet inset={bp === "expanded"} className="flex h-full flex-col">
			{header && (
				<header
					data-window-drag=""
					className="flex h-12 shrink-0 items-center gap-1.5 px-2"
				>
					{chromeLeft && <ChromeLead />}
					{sidebarToggle && (
						<>
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
						</>
					)}
					<span className="min-w-0 flex-1" />
					<WindowControls side="right" />
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
					{/* 与线程页输入坞同一块 D 稿托盘：同一材质（glass-dock）、同样的分行；首页大一档圆角（18px） */}
					<div className={`relative isolate ${rise("delay-[120ms]")}`}>
						{/* 托盘背后的光场（一圈宽而淡的光晕 + 两团晕）：玻璃底下有东西可模糊，居中态也看得出是玻璃 */}
						<div aria-hidden className="home-field" />
						<div ref={trayRef} className="glass glass-dock rounded-2xl">
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
								// 托盘是 36px 大圆角：文字离边 20px，才不显得贴着弧线
								className="block w-full resize-none bg-transparent px-5 pt-4 pb-1.5 text-[14px] leading-5 outline-none placeholder:text-faint focus-visible:outline-none"
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
							 * 窄屏两行：附件与发送钮在第一行两端；分隔线下选项单独占第二行——orb 是小圆圈，
							 * 项目名先拿够宽度，剩余空间放得下才显示模型名，否则模型也缩成圆圈；思考级别始终是模型图标右下角的角标。
							 */}
							<div
								className={`relative flex px-3 pb-3 ${compact ? "flex-wrap items-center gap-y-2" : "items-start gap-0.5"}`}
							>
								<div className="flex h-8 shrink-0 items-center">
									<AttachButton onFiles={(files) => void att.addFiles(files)} />
									{!compact && (
										<span
											aria-hidden
											className="mx-1.5 h-[18px] w-px shrink-0 bg-[var(--glass-line)]"
										/>
									)}
								</div>
								<div
									ref={pillsRef}
									className={`flex min-h-8 min-w-0 items-center gap-1.5 ${compact ? "-mx-3 relative order-last w-[calc(100%+1.5rem)] border-[var(--glass-line)] border-t px-3 pt-2.5" : "flex-1"}`}
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
									{showProfile && (
										<PillButton
											ref={profilePillRef}
											icon="browser"
											text={profileText}
											shortcut="Ctrl+B"
											active={pill === "profile"}
											onClick={() =>
												setPill(pill === "profile" ? null : "profile")
											}
											shape={compact ? "circle" : "pill"}
										/>
									)}
									<PillButton
										ref={projectPillRef}
										icon="folder"
										text={projectText}
										shortcut="Ctrl+J"
										active={pill === "project"}
										onClick={() =>
											setPill(pill === "project" ? null : "project")
										}
										grow={compact}
									/>
									{(() => {
										const modelPill = (
											<PillButton
												icon={
													<ModelLogo
														model={model}
														size={compact && modelFolded ? 16 : 13}
													/>
												}
												text={modelText}
												shortcut="Ctrl+S"
												active={pill === "model"}
												onClick={() =>
													setPill(pill === "model" ? null : "model")
												}
												shape={compact && modelFolded ? "circle" : "pill"}
												reserveEnd={!compact && thinkingOptions !== null}
											/>
										);
										// 模型不推理：只有胶囊，没有思考级别圆圈
										return thinkingOptions ? (
											<ModelPill
												corner={compact}
												thinking={thinking}
												options={thinkingOptions}
												known={modelCaps !== undefined}
												onThinking={setThinking}
												pill={modelPill}
											/>
										) : (
											modelPill
										);
									})()}
									{compact && (
										// 只用来量模型名胶囊的自然宽度，与展开态同款内边距与间距
										<span
											ref={modelGhostRef}
											aria-hidden
											className="pointer-events-none invisible absolute top-0 left-0 inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-full border px-3 text-xs"
										>
											<ModelLogo model={model} size={13} />
											{modelText.main}
										</span>
									)}
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
										{(providers.data?.providers ?? [])
											.filter((p) => p.name !== "@host")
											.map((p) =>
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
																<Badge>
																	{t("panel.cores", { n: ot.cpus })}
																</Badge>
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
								{pill === "profile" && showProfile && (
									<Popover onClose={() => setPill(null)}>
										<PopoverLabel>{t("newThread.profileTitle")}</PopoverLabel>
										<Option
											selected={profilePick === null}
											leading={<Icon name="folder" size={14} />}
											hint={defaultProfileName ?? t("common.none")}
											onClick={() => {
												setProfilePick(null);
												setPill(null);
											}}
										>
											{t("newThread.profileFollow")}
										</Option>
										<Option
											selected={profilePick === ""}
											leading={<Icon name="minusCircle" size={14} />}
											onClick={() => {
												setProfilePick("");
												setPill(null);
											}}
										>
											{t("newThread.profileNone")}
										</Option>
										{profileList.map((p) => (
											<Option
												key={p.id}
												selected={profilePick === p.id}
												leading={<Icon name="browser" size={14} />}
												trailing={
													p.holder && (
														<Badge tone="warn">
															{t("newThread.profileInUse")}
														</Badge>
													)
												}
												onClick={() => {
													setProfilePick(p.id);
													setPill(null);
												}}
											>
												{p.name}
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
										<ModelOptions model={model} onModel={setModel} />
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
			<ProfileConflictDialog
				conflict={conflict}
				busy={conflictBusy}
				error={conflictError}
				onChoose={resolveConflict}
				onClose={() => {
					if (!conflictBusy) setConflict(null);
				}}
			/>
		</Sheet>
	);
}
