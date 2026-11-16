/**
 * 首页 = 新建对话框（居中玻璃卡，按最新 Amp）：提示词 + 附件（图片另带内联副本）；底部胶囊——对象（role 胶囊，Ctrl+S：
 * 全局助手 / cycleOrder 里的 role / 指定模型，以及思考强度）、执行位置（Ctrl+M）、浏览器 Profile（Ctrl+B，M8；只在新 orb
 * 且至少有一个 Profile 时出现）、项目（Ctrl+J）、发送。初始对象取首屏偏好（`settings/composer` 的 `defaultTarget`）。
 * 选全局助手（M13 R14–R16）：执行位置、项目、Profile 三个胶囊收起（不在 DOM 里），附件传进助手文件库，发送即新建
 * 助手对话（首条消息、文件、图片、所选模型都带上）并跳到 `/assistant/:id`；托盘换成旋转流光描边、色场换成助手色相。
 * 普通对象：新线程以所选 role 开场（`role`），或用指定的模型（`model`）；省略 provider / 类型时由控制面依次取项目默认、
 * orb_defaults、provider 默认。
 * 选的 Profile 被另一台运行中的 orb 占用（409 `profile_in_use`）时弹选择对话框，选「只读使用」或「接管」后带 `profileMode` 重发。
 * 执行位置的「Runners」分组（M11b）：选 runner 的目录（可带子路径、可新建 worktree）；目录已被别的线程占用
 * （409 `dir_in_use`）时弹对话框，选「新建 worktree」或「仍然共用」后重发。
 * 托盘背后是一层光场（.home-field），玻璃居中时也有东西可模糊；发送后托盘经 DockTray 连续变形到线程页底部输入坞。
 * 所选项目已绑定 GitHub 时（M14），键入 `#` 弹出该仓库的 issue / PR 候选，选中即作为 Markdown 附件加入（全局助手时不启用）。
 */
import {
	type CSSProperties,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Schemas } from "../api/client";
import { useComposerPrefs } from "../api/identity-queries";
import {
	useBrowserProfiles,
	useOrbs,
	useProjects,
	useProviders,
} from "../api/queries";
import { createAssistantConversation } from "../api/space-queries";
import { RecentArtifacts } from "../components/artifacts/RecentArtifacts";
import {
	AttachButton,
	AttachmentStrip,
	useAttachments,
} from "../components/composer/attachments";
import { morphToThread } from "../components/composer/DockTray";
import { PillButton, type PillText } from "../components/composer/PillButton";
import { RefMenu, useRefMenu } from "../components/composer/RefMenu";
import {
	RoleCapsule,
	type Target,
	useRoleOptions,
} from "../components/composer/RoleCapsule";
import { FADE_MS, type Phase } from "../components/composer/RoleTrack";
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
import {
	type DirChoice,
	type DirConflict,
	DirInUseDialog,
	dirConflictOf,
} from "../components/runner/DirInUseDialog";
import {
	RunnerOptions,
	type RunnerPick,
	runnerExecutor,
	runnerPickDir,
	runnerPickError,
	worktreeSlug,
} from "../components/runner/RunnerPicker";
import { setPendingPrompt } from "../components/transcript/pending-prompt";
import { Badge, BrandMark, ProjectBadge, Sheet } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import { DockerBadge, typeHasDocker } from "../components/ui/OrbBadges";
import { OrbBead } from "../components/ui/OrbBead";
import { Option, Popover, PopoverLabel } from "../components/ui/PickerPopover";
import {
	describeError,
	IconButton,
	Spinner,
} from "../components/ui/primitives";
import { formatMemoryMB } from "../lib/format";
import { useBreakpoint } from "../lib/viewport";
import { createThread } from "../state/actions";
import { shareIntakeStore, useShareIntake } from "../state/share-intake";
import {
	uiStore,
	useSidebarShown,
	useSidebarToggleShown,
	useUi,
} from "../state/ui";

type Pill = "executor" | "profile" | "project" | "role";

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
	const [prompt, setPrompt] = useState("");
	const [pill, setPill] = useState<Pill | null>(null);
	const [executor, setExecutor] = useState<Executor>({ kind: "new_orb" });
	// 选了 runner 的目录（M11b）时优先于 `executor`；选回新 orb / 已有 orb 时清空
	const [runnerPick, setRunnerPick] = useState<RunnerPick | null>(null);
	const [projectId, setProjectId] = useState<string | undefined>(undefined);
	// 用户在 role 胶囊里选的对象；还没选时跟随首屏偏好（偏好没读到或读取失败时是 default role）
	const [picked, setPicked] = useState<Target | null>(null);
	const prefs = useComposerPrefs();
	const target: Target =
		picked ??
		(prefs.data?.defaultTarget === "assistant"
			? { kind: "assistant" }
			: { kind: "role", role: "default" });
	const assistantTarget = target.kind === "assistant";
	const assistantRef = useRef(assistantTarget);
	assistantRef.current = assistantTarget;
	// 用户切换过全局助手与普通对象之后，三个胶囊的收起 / 展开才播动画（偏好读到后的初始对象直接到位）
	const [pillsMotion, setPillsMotion] = useState(false);
	const choose = (next: Target) => {
		if ((next.kind === "assistant") !== assistantTarget) setPillsMotion(true);
		setPicked(next);
	};
	const att = useAttachments({ library: assistantTarget });
	const [profilePick, setProfilePick] = useState<ProfilePick>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [conflict, setConflict] = useState<ProfileConflict | null>(null);
	const [conflictBusy, setConflictBusy] = useState<ConflictChoice | null>(null);
	const [conflictError, setConflictError] = useState<unknown>(null);
	const [dirConflict, setDirConflict] = useState<DirConflict | null>(null);
	const [dirBusy, setDirBusy] = useState<DirChoice | null>(null);
	const [dirError, setDirError] = useState<unknown>(null);
	// 撞上冲突的那次请求（冲突重试只在它上面加使用方式，不再按当前胶囊重新构造）
	const conflictReq = useRef<{
		body: Schemas["CreateThreadRequest"];
		text: string;
	} | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);
	const trayRef = useRef<HTMLDivElement>(null);
	const [caret, setCaret] = useState(0);
	const refMenu = useRefMenu({
		projectId:
			!assistantTarget &&
			projects.data?.projects.find((p) => p.id === projectId)?.github?.bound
				? projectId
				: undefined,
		text: prompt,
		caret,
		setText: (next, c) => {
			setPrompt(next);
			setCaret(c);
			requestAnimationFrame(() => ref.current?.setSelectionRange(c, c));
		},
		onAttached: (a) => att.addUploaded([a]),
	});
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

	// 别处交给首屏的提示（Artifacts 空态磁贴、把评论发给新线程）：取走后写进输入框（与项目）并聚焦
	const homeSeed = useUi((s) => s.homeSeed);
	useEffect(() => {
		if (!homeSeed) return;
		const seed = uiStore.getState().takeHomeSeed();
		if (!seed) return;
		setPrompt(seed.text);
		if (seed.projectId) setProjectId(seed.projectId);
		requestAnimationFrame(() => {
			const el = ref.current;
			if (!el) return;
			el.focus();
			el.setSelectionRange(seed.text.length, seed.text.length);
		});
	}, [homeSeed]);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (!(e.ctrlKey || e.metaKey)) return;
			const k = e.key.toLowerCase();
			const map: Record<string, Pill> = {
				m: "executor",
				j: "project",
				s: "role",
				b: "profile",
			};
			const p = map[k];
			if (!p) return;
			// 全局助手：执行位置、项目、Profile 胶囊不在，它们的快捷键也不响应（Ctrl+B 照旧留给侧栏开关）
			if (p !== "role" && assistantRef.current) return;
			// Ctrl+B 只在输入框里切 Profile 弹层（且胶囊在时）；其余时候留给全局的侧栏开关
			if (p === "profile" && (!inEditable(e.target) || !showProfileRef.current))
				return;
			e.preventDefault();
			setPill((cur) => (cur === p ? null : p));
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const profileList = profiles.data?.profiles ?? [];
	// Profile 胶囊：新 orb 且至少有一个 Profile 时出现（挂到已有 orb 时沿用那台 orb 的 Profile）
	const showProfile =
		executor.kind === "new_orb" && !runnerPick && profileList.length > 0;
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
	// 协调 orb（M9，跑在控制面宿主上）不能挂工作线程，也不出现在执行位置里；
	// 预热池 orb（M11a）由控制面在新建线程时自动认领，不能手动挂（服务端回 409 orb_is_pooled）
	const liveOrbs = (orbs.data?.orbs ?? []).filter(
		(o) =>
			!o.coordinator &&
			// runner 的常驻 orb（M11b）在「Runners」分组里按目录选，不作为已有 orb 挂
			o.kind !== "runner" &&
			!o.pool &&
			o.state !== "deleted" &&
			o.state !== "deleting" &&
			o.state !== "failed",
	);
	// 胶囊的无障碍名保持原拼接字符串；视觉上把「·」后的部分拆成徽标
	const existingOrbLabel =
		executor.kind === "orb"
			? t("newThread.existingOrb", { id: executor.orbId.slice(0, 12) })
			: "";
	const executorText: PillText = runnerPick
		? {
				label: `${runnerPick.runnerName} · ${runnerPickDir(runnerPick)}${runnerPick.mode === "worktree" ? ` · ${t("runner.modeWorktree")}` : ""}`,
				main: `${runnerPick.runnerName} · ${runnerPickDir(runnerPick).split("/").pop()}`,
				badge:
					runnerPick.mode === "worktree"
						? t("runner.worktreeBadge")
						: undefined,
			}
		: executor.kind === "orb"
			? { label: existingOrbLabel, main: existingOrbLabel }
			: executor.kind === "new_orb" && executor.provider
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
	const roles = useRoleOptions();
	// 普通 role 的托盘光晕与色场用该 role 的色相（与 Ctrl+P 轨道同色）；指定模型与全局助手由 CSS 各自取色
	const trayHue =
		target.kind === "role"
			? roles.find((r) => r.role === target.role)?.hue
			: undefined;
	// 执行位置、项目、Profile 三个胶囊：hidden（不在 DOM 里）→ shown → leaving（FADE_MS 淡出后回到 hidden）
	const [pillsPhase, setPillsPhase] = useState<Phase>(
		assistantTarget ? "hidden" : "shown",
	);
	useLayoutEffect(() => {
		if (!assistantTarget) {
			setPillsPhase("shown");
			return;
		}
		if (!pillsMotion) {
			setPillsPhase("hidden");
			return;
		}
		setPillsPhase((p) => (p === "hidden" ? p : "leaving"));
		const id = window.setTimeout(() => setPillsPhase("hidden"), FADE_MS);
		return () => window.clearTimeout(id);
	}, [assistantTarget, pillsMotion]);
	// 切到助手的那一帧就开始淡出（不等 effect 把状态改成 leaving）；展开时新挂载的胶囊组播进场
	const pillsAnim =
		pillsPhase === "leaving" || assistantTarget
			? "home-pills-out"
			: pillsMotion
				? "home-pills-in"
				: "";
	const rise = (delay: string) =>
		intro
			? `transition-[opacity,translate] duration-300 ease-out starting:translate-y-2 starting:opacity-0 ${delay}`
			: "";

	/** 发送当前输入：全局助手新建助手对话；其余新建线程，Profile 被占用时记下这次请求并弹冲突对话框。 */
	const submit = async () => {
		const text = prompt.trim();
		if (!text && !att.hasFiles) return;
		if (att.uploading) {
			setError(t("attachments.waitUpload"));
			return;
		}
		if (target.kind === "assistant") {
			await sendAssistant(text, target);
			return;
		}
		const pickError = runnerPick && runnerPickError(runnerPick);
		if (pickError) {
			setPill("executor");
			setError(
				t(
					pickError === "subpath"
						? "runner.subpathInvalid"
						: "runner.worktreeNameInvalid",
				),
			);
			return;
		}
		await send(
			{
				prompt: text,
				executor: runnerPick ? runnerExecutor(runnerPick, text) : executor,
				...(projectId ? { projectId } : {}),
				...(att.contents.length > 0 ? { images: att.contents } : {}),
				...(att.attachmentIds.length > 0
					? { attachments: att.attachmentIds }
					: {}),
				...(target.kind === "role"
					? { role: target.role }
					: {
							model: { provider: target.model.provider, id: target.model.id },
						}),
				...(target.thinking ? { thinkingLevel: target.thinking } : {}),
				...(executor.kind === "new_orb" && !runnerPick && profilePick !== null
					? { browserProfileId: profilePick }
					: {}),
			},
			text,
		);
	};

	/** 全局助手：新建助手对话，首条消息、库文件、图片、所选模型与思考档位都带上，然后跳过去。 */
	const sendAssistant = async (
		text: string,
		to: Extract<Target, { kind: "assistant" }>,
	) => {
		setBusy(true);
		setError(null);
		try {
			const thread = await createAssistantConversation({
				...(text ? { prompt: text } : {}),
				...(att.fileIds.length > 0 ? { files: att.fileIds } : {}),
				...(att.contents.length > 0 ? { images: att.contents } : {}),
				...(to.model
					? { model: { provider: to.model.provider, id: to.model.id } }
					: {}),
				...(to.thinking ? { thinkingLevel: to.thinking } : {}),
			});
			if (window.matchMedia?.("(pointer: coarse)").matches) ref.current?.blur();
			setPrompt("");
			att.clear();
			navigate(`/assistant/${thread.id}`);
		} catch (e) {
			setError(e instanceof Error ? e.message : String(e));
		} finally {
			setBusy(false);
		}
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

	/**
	 * 目录冲突对话框里选了做法：在撞上冲突的那次请求上改执行位置后重发——
	 * 「新建 worktree」取提示词的 slug 作名字（冲突只出现在「当前目录」模式，没有填过名字），「仍然共用」带 `shareDir`。
	 */
	const resolveDirConflict = (choice: DirChoice) => {
		const req = conflictReq.current;
		if (req?.body.executor.kind !== "runner") return;
		const { worktree: _, shareDir: __, ...base } = req.body.executor;
		const name = worktreeSlug(req.text);
		void send(
			{
				...req.body,
				executor:
					choice === "worktree"
						? { ...base, worktree: { name } }
						: { ...base, shareDir: true },
			},
			req.text,
			undefined,
			choice,
		);
	};

	const send = async (
		body: Schemas["CreateThreadRequest"],
		text: string,
		mode?: ConflictChoice,
		dirChoice?: DirChoice,
	) => {
		setBusy(true);
		setError(null);
		if (mode) {
			setConflictBusy(mode);
			setConflictError(null);
		}
		if (dirChoice) {
			setDirBusy(dirChoice);
			setDirError(null);
		}
		try {
			const thread = await createThread(body);
			setConflict(null);
			setDirConflict(null);
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
			// 冲突的目录正是声明的 git 仓库根时才能在旁边新建 worktree（子目录不行）
			const d =
				body.executor.kind === "runner"
					? dirConflictOf(
							e,
							body.executor.dir,
							Boolean(
								runnerPick?.dir.git &&
									runnerPick.dir.path === body.executor.dir,
							),
						)
					: null;
			if (mode) setConflictError(e);
			else if (dirChoice) setDirError(e);
			else if (c) {
				conflictReq.current = { body, text };
				setConflict(c);
			} else if (d) {
				conflictReq.current = { body, text };
				setDirConflict(d);
			} else setError(e instanceof Error ? e.message : String(e));
		} finally {
			setBusy(false);
			setConflictBusy(null);
			setDirBusy(null);
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
					<div
						className={`home-tray relative isolate ${rise("delay-[120ms]")}`}
						data-target={target.kind}
						style={
							trayHue ? ({ "--role-h": trayHue } as CSSProperties) : undefined
						}
					>
						{/*
						 * 托盘背后的光场：玻璃底下有东西可模糊，居中态也看得出是玻璃。按对象换色（R15）：全局助手是助手色相的色场
						 * 加旋转流光描边（.home-tray::after），普通 role 是该 role 色相的静态光晕
						 */}
						<div aria-hidden className="home-field" />
						{refMenu.open && (
							<RefMenu
								entries={refMenu.entries}
								active={refMenu.active}
								pending={refMenu.pending}
								onPick={refMenu.pick}
							/>
						)}
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
								onChange={(e) => {
									setPrompt(e.target.value);
									setCaret(e.target.selectionStart);
								}}
								onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
								onKeyUp={(e) => setCaret(e.currentTarget.selectionStart)}
								onPaste={(e) => {
									const files = [...e.clipboardData.files];
									if (files.length > 0) {
										e.preventDefault();
										void att.addFiles(files);
									}
								}}
								onKeyDown={(e) => {
									if (refMenu.onKeyDown(e)) return;
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
							 * 工具行。宽屏一行：附件 + 分隔、选项胶囊（对象在最前，其余三个在选全局助手时收起）、发送钮。
							 * 窄屏两行：附件与发送钮在第一行两端；分隔线下选项单独占第二行——对象、orb 是小圆圈，项目名拿剩下的宽度。
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
									className={`flex min-h-8 min-w-0 items-center gap-1.5 ${compact ? "-mx-3 relative order-last w-[calc(100%+1.5rem)] border-[var(--glass-line)] border-t px-3 pt-2.5" : "flex-1"}`}
								>
									<RoleCapsule
										value={target}
										onChange={choose}
										allowAssistant
										open={pill === "role"}
										onOpenChange={(open) => setPill(open ? "role" : null)}
										compactPill={compact}
									/>
									{pillsPhase !== "hidden" && (
										<div
											data-testid="home-pills"
											className={`flex min-w-0 items-center gap-1.5 ${compact ? "flex-1" : ""} ${pillsAnim}`}
										>
											<PillButton
												icon={runnerPick ? "laptop" : "box"}
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
												icon="folder"
												text={projectText}
												shortcut="Ctrl+J"
												active={pill === "project"}
												onClick={() =>
													setPill(pill === "project" ? null : "project")
												}
												grow={compact}
											/>
										</div>
									)}
								</div>
								<button
									type="button"
									title={t("newThread.start")}
									aria-label={t("composer.send")}
									disabled={
										busy || att.uploading || (!prompt.trim() && !att.hasFiles)
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
								{pill === "executor" && !assistantTarget && (
									<Popover onClose={() => setPill(null)}>
										<PopoverLabel>{t("newThread.newOrb")}</PopoverLabel>
										<Option
											selected={
												!runnerPick &&
												executor.kind === "new_orb" &&
												!executor.provider
											}
											leading={<Icon name="box" size={14} />}
											onClick={() => {
												setExecutor({ kind: "new_orb" });
												setRunnerPick(null);
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
															!runnerPick &&
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
																{typeHasDocker(ot) && <DockerBadge />}
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
															setRunnerPick(null);
															setPill(null);
														}}
													>
														{p.name}
													</Option>
												)),
											)}
										<RunnerOptions
											pick={runnerPick}
											prompt={prompt}
											onPick={setRunnerPick}
										/>
										{liveOrbs.length > 0 && (
											<PopoverLabel>{t("newThread.existingOrbs")}</PopoverLabel>
										)}
										{liveOrbs.map((o) => (
											<Option
												key={o.id}
												selected={
													!runnerPick &&
													executor.kind === "orb" &&
													executor.orbId === o.id
												}
												leading={<OrbBead state={o.state} />}
												mono
												trailing={
													<>
														<Badge>{o.type}</Badge>
														{o.nestedDocker && <DockerBadge />}
													</>
												}
												onClick={() => {
													setExecutor({ kind: "orb", orbId: o.id });
													setRunnerPick(null);
													setPill(null);
												}}
											>
												{o.id.slice(0, 16)}
											</Option>
										))}
									</Popover>
								)}
								{pill === "profile" && showProfile && !assistantTarget && (
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
								{pill === "project" && !assistantTarget && (
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
							</div>
						</div>
					</div>
					{(error || att.error || refMenu.error != null) && (
						<p
							role="alert"
							className="mt-2 flex items-center justify-center gap-1.5 text-err text-sm"
						>
							<Icon name="alert" size={13} className="shrink-0" />
							{error ?? (att.error || describeError(refMenu.error, t))}
						</p>
					)}
					<RecentArtifacts />
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
			<DirInUseDialog
				conflict={dirConflict}
				busy={dirBusy}
				error={dirError}
				onChoose={resolveDirConflict}
				onClose={() => {
					if (!dirBusy) setDirConflict(null);
				}}
			/>
		</Sheet>
	);
}
