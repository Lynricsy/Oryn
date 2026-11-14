/**
 * Desktop 标签（M8 4.3）：orb 的 X11 桌面经 `/api/v1/orbs/<id>/desktop/ws`（noVNC）显示并可操作。
 *
 * 工具条（h-11，结构照 Portal）：Profile 菜单 + 连接状态点；弹性空白；可控制 / 仅观看开关；剪贴板；键盘（仅触屏）；
 * 更多（缩放「跟随面板 / 适应」、启动 / 关闭浏览器、重新连接）；全屏。
 * 工具条下按需一行 Profile 横幅：被占用（选只读或接管）、只读副本（含被接管）、装载失败（重试）、保存结果。
 *
 * 何时连接：面板挂载、orb `ready`、`desktop.displayReady`、页面在前台四者同时成立；页面转入后台满 60 秒断开，回到前台再连。
 * 意外断开（关闭码不是 4401 / 4409 / 4410 / 4429）且仍满足条件：按 1、2、5 秒重试三次，之后显示「重新连接」。
 *
 * 触屏：手势由 noVNC 自带（单指点 = 左键、单指拖 = 左键拖、长按 = 右键、双指拖 = 滚动、捏合 = Ctrl+滚轮即页面缩放）；
 * compact 默认「适应」（远端 1280x800 不变、整块桌面等比缩进面板，不需要本地平移）；也可切「跟随面板」让远端分辨率等于面板，
 * 网页按手机宽度排版、1:1 清晰；全屏与横屏能拿到更大的画面。软键盘打开时临时按「适应」缩放，远端分辨率不随键盘高度抖动。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Orb } from "../../api/client";
import { useOrbBrowser, useSaveOrbDesktopProfile } from "../../api/queries";
import { useCoarsePointer } from "../../lib/viewport";
import { wakeThread } from "../../state/actions";
import { uiStore, useDesktopScale, useUi } from "../../state/ui";
import { useOrbOf } from "../thread/ThreadHeader";
import { Callout, StatusDot, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Menu,
	type MenuItem,
	Spinner,
} from "../ui/primitives";
import { ClipboardPopover } from "./ClipboardPopover";
import { ProfileMenu, useOrbProfileChoice } from "./ProfileMenu";
import { profileErrorText, profileStatusText } from "./profile-status";
import { SoftKeyboard } from "./SoftKeyboard";
import {
	type DesktopSession,
	type DesktopStatus,
	openDesktopSession,
} from "./session";

/** 意外断开后的重试间隔（三次）。 */
export const RETRY_MS = [1000, 2000, 5000] as const;
/** 不重试的关闭码：吊销、旧 orbd、orb 已删除、观看者已满。 */
const FATAL = new Set([4401, 4409, 4410, 4429]);
/** 页面转入后台多久后断开。 */
export const AWAY_MS = 60_000;

const STATUS_TONE: Record<DesktopStatus, Tone> = {
	connecting: "warn",
	connected: "ok",
	closed: "neutral",
};

/** 页面在后台满 `ms` 后为真；回到前台立即为假。挂载时已在后台则直接为真。 */
function usePageAway(ms: number): boolean {
	const [away, setAway] = useState(
		() => document.visibilityState !== "visible",
	);
	useEffect(() => {
		let timer: ReturnType<typeof setTimeout> | undefined;
		const onChange = () => {
			clearTimeout(timer);
			if (document.visibilityState === "visible") setAway(false);
			else timer = setTimeout(() => setAway(true), ms);
		};
		document.addEventListener("visibilitychange", onChange);
		return () => {
			clearTimeout(timer);
			document.removeEventListener("visibilitychange", onChange);
		};
	}, [ms]);
	return away;
}

type KeyboardLock = { lock?(): Promise<void>; unlock?(): void };

function keyboardApi(): KeyboardLock | undefined {
	return (navigator as Navigator & { keyboard?: KeyboardLock }).keyboard;
}

/** 舞台遮罩：orb 不在 ready、桌面未就绪、连接失败或已满。 */
function StageCover({
	children,
	testId,
}: {
	children: ReactNode;
	testId: string;
}) {
	return (
		<div
			data-testid={testId}
			className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-3 rounded-lg bg-panel/85 p-6 text-center backdrop-blur-sm"
		>
			{children}
		</div>
	);
}

function OrbCover({
	threadId,
	orb,
}: {
	threadId: string;
	orb: Orb;
}): ReactNode {
	const { t } = useTranslation();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const state = orb.state;
	if (state === "frozen" || state === "stopped")
		return (
			<StageCover testId="desktop-asleep">
				<OrbBead state={state} size={40} />
				<div className="space-y-1">
					<p className="font-medium text-fg text-sm">{t("deskPanel.asleep")}</p>
					<p className="max-w-xs text-muted text-xs">
						{t("deskPanel.asleepHint")}
					</p>
				</div>
				<Button
					variant="accent"
					icon="play"
					disabled={busy}
					onClick={async () => {
						setBusy(true);
						setError(null);
						try {
							await wakeThread(threadId);
						} catch (e) {
							setError(e);
							setBusy(false);
						}
					}}
				>
					{t("deskPanel.wake")}
				</Button>
				<ErrorText error={error} />
			</StageCover>
		);
	if (state === "failed")
		return (
			<StageCover testId="desktop-failed">
				<OrbBead state={state} size={40} />
				<p className="max-w-xs text-muted text-sm">
					{t("deskPanel.orbFailed")}
				</p>
			</StageCover>
		);
	return (
		<StageCover testId="desktop-starting">
			<OrbBead state={state} size={40} />
			<p className="flex items-center gap-2 text-muted text-sm">
				{state === "ready" ? (
					<>
						<Spinner size={12} />
						{t("deskPanel.displayStarting")}
					</>
				) : (
					t("deskPanel.orbStarting", { state: t(`orbState.${state}`) })
				)}
			</p>
		</StageCover>
	);
}

/** 关闭码 → 遮罩的标题与下一步说明。 */
function closedText(
	code: number | undefined,
	t: TFunction,
): { title: string; hint: string } {
	switch (code) {
		case 4401:
			return {
				title: t("deskPanel.closed.revoked"),
				hint: t("deskPanel.closed.revokedHint"),
			};
		case 4409:
			return {
				title: t("deskPanel.closed.outdated"),
				hint: t("deskPanel.closed.outdatedHint"),
			};
		case 4410:
			return {
				title: t("deskPanel.closed.deleted"),
				hint: t("deskPanel.closed.deletedHint"),
			};
		case 4429:
			return {
				title: t("deskPanel.closed.full"),
				hint: t("deskPanel.closed.fullHint"),
			};
		case 4502:
			return {
				title: t("deskPanel.closed.lost"),
				hint: t("deskPanel.closed.unreachableHint"),
			};
		case 4503:
			return {
				title: t("deskPanel.closed.lost"),
				hint: t("deskPanel.closed.unavailableHint"),
			};
		default:
			return {
				title: t("deskPanel.closed.lost"),
				hint: t("deskPanel.closed.networkHint"),
			};
	}
}

function DesktopView({
	threadId,
	orb,
}: {
	threadId: string;
	orb: Orb & { desktop: NonNullable<Orb["desktop"]> };
}): ReactNode {
	const { t } = useTranslation();
	const desktop = orb.desktop;
	const profile = desktop.profile;
	const control = useUi((s) => s.desktopControl);
	const scale = useDesktopScale();
	const clipSync = useUi((s) => s.desktopClipSync);
	const coarse = useCoarsePointer();
	const away = usePageAway(AWAY_MS);
	const want = orb.state === "ready" && desktop.displayReady && !away;

	const stageRef = useRef<HTMLDivElement>(null);
	const screenRef = useRef<HTMLDivElement>(null);
	const sessionRef = useRef<DesktopSession | null>(null);
	const [status, setStatus] = useState<DesktopStatus>("closed");
	const [closeCode, setCloseCode] = useState<number | undefined>();
	const [nonce, setNonce] = useState(0);
	const [gaveUp, setGaveUp] = useState(false);
	const attemptRef = useRef(0);
	const [kbOpen, setKbOpen] = useState(false);
	const [remoteClip, setRemoteClip] = useState<string | null>(null);
	const lastSentClip = useRef<string | null>(null);
	const [notice, setNotice] = useState<string | null>(null);

	const browser = useOrbBrowser(orb.id);
	const save = useSaveOrbDesktopProfile(orb.id);
	const choice = useOrbProfileChoice(orb.id, profile?.mode === "readonly");

	// 会话里读最新偏好（会话只在连接条件变化时重建）
	const effectiveScale = kbOpen ? "fit" : scale;
	const prefs = useRef({ control, scale: effectiveScale, clipSync });
	prefs.current = { control, scale: effectiveScale, clipSync };

	// 离开可连接状态后重新计数，下次满足条件时重新给三次重试
	useEffect(() => {
		if (want) return;
		attemptRef.current = 0;
		setGaveUp(false);
		setStatus("closed");
		setCloseCode(undefined);
	}, [want]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: nonce 变化（重试 / 重新连接）时重建会话
	useEffect(() => {
		const el = screenRef.current;
		if (!want || gaveUp || !el) return;
		let alive = true;
		let timer: ReturnType<typeof setTimeout> | undefined;
		const s = openDesktopSession(el, orb.id, {
			onStatus: (st, code) => {
				if (!alive) return;
				setStatus(st);
				if (st === "connected") {
					attemptRef.current = 0;
					setCloseCode(undefined);
				} else if (st === "closed") {
					setCloseCode(code);
					if (code !== undefined && FATAL.has(code)) return;
					const n = attemptRef.current;
					const delay = RETRY_MS[n];
					if (delay === undefined) {
						setGaveUp(true);
						return;
					}
					attemptRef.current = n + 1;
					timer = setTimeout(() => setNonce((x) => x + 1), delay);
				}
			},
			onClipboard: (text) => {
				if (!alive) return;
				setRemoteClip(text);
				lastSentClip.current = text;
				if (prefs.current.clipSync && document.hasFocus())
					void navigator.clipboard?.writeText(text).catch(() => {});
			},
		});
		s.setViewOnly(!prefs.current.control);
		s.setScale(prefs.current.scale);
		sessionRef.current = s;
		return () => {
			alive = false;
			clearTimeout(timer);
			s.close();
			if (sessionRef.current === s) sessionRef.current = null;
		};
	}, [want, gaveUp, orb.id, nonce]);

	useEffect(() => {
		sessionRef.current?.setViewOnly(!control);
	}, [control]);
	useEffect(() => {
		sessionRef.current?.setScale(effectiveScale);
	}, [effectiveScale]);

	// 自动同步（本机 → 桌面）：舞台获得焦点与窗口获得焦点时读本机剪贴板，与上次不同才发送
	useEffect(() => {
		const stage = stageRef.current;
		if (!clipSync || !stage) return;
		const pull = () => {
			void navigator.clipboard
				?.readText()
				.then((text) => {
					if (!text || text === lastSentClip.current) return;
					lastSentClip.current = text;
					sessionRef.current?.paste(text);
				})
				.catch(() => {});
		};
		stage.addEventListener("focusin", pull);
		window.addEventListener("focus", pull);
		return () => {
			stage.removeEventListener("focusin", pull);
			window.removeEventListener("focus", pull);
		};
	}, [clipSync]);

	// 全屏结束时释放键盘锁
	useEffect(() => {
		const onChange = () => {
			if (!document.fullscreenElement) keyboardApi()?.unlock?.();
		};
		document.addEventListener("fullscreenchange", onChange);
		return () => document.removeEventListener("fullscreenchange", onChange);
	}, []);

	const fullscreen = async () => {
		const stage = stageRef.current;
		if (stage?.requestFullscreen && document.fullscreenEnabled !== false) {
			try {
				await stage.requestFullscreen();
				// 让 Esc、Ctrl+W 等进桌面（不支持时忽略）
				await keyboardApi()
					?.lock?.()
					.catch(() => {});
				return;
			} catch {
				// 被拒绝：退到面板最大化
			}
		}
		uiStore.getState().togglePanelMax();
	};

	const reconnect = () => {
		attemptRef.current = 0;
		setGaveUp(false);
		setCloseCode(undefined);
		setNonce((x) => x + 1);
	};

	// 保存结果提示 4 秒后收起
	useEffect(() => {
		if (!notice) return;
		const timer = setTimeout(() => setNotice(null), 4000);
		return () => clearTimeout(timer);
	}, [notice]);

	const runSave = async () => {
		setNotice(null);
		try {
			const p = await save.mutateAsync();
			setNotice(t("deskPanel.saved", { revision: p.revision }));
		} catch {
			// 错误由 save.error 显示
		}
	};

	const shownStatus: DesktopStatus = want ? status : "closed";
	const running = desktop.browser === "running";
	const moreItems: MenuItem[] = [
		{
			key: "follow",
			label: t("deskPanel.scale.follow"),
			checked: scale === "follow",
			onSelect: () => uiStore.getState().setDesktopScale("follow"),
		},
		{
			key: "fit",
			label: t("deskPanel.scale.fit"),
			checked: scale === "fit",
			onSelect: () => uiStore.getState().setDesktopScale("fit"),
		},
		running
			? {
					key: "browser",
					label: t("deskPanel.stopBrowser"),
					icon: "stop",
					disabled: browser.isPending || orb.state !== "ready",
					onSelect: () => browser.mutate(false),
				}
			: {
					key: "browser",
					label: t("deskPanel.startBrowser"),
					icon: "play",
					disabled:
						browser.isPending ||
						orb.state !== "ready" ||
						desktop.browser === "starting",
					onSelect: () => browser.mutate(true),
				},
		{
			key: "reconnect",
			label: t("deskPanel.reconnect"),
			icon: "refresh",
			disabled: !want,
			onSelect: reconnect,
		},
	];

	const closed = shownStatus === "closed" && want;
	const fatal = closeCode !== undefined && FATAL.has(closeCode);
	const showClosed = closed && (gaveUp || fatal);
	const text = closedText(closeCode, t);

	let banner: ReactNode = null;
	if (profile?.status === "in_use") {
		const heldBy = profile.heldBy;
		banner = (
			<Callout
				tone="warn"
				icon="browser"
				title={profileStatusText(profile, t).text}
				actions={
					<Button
						size="sm"
						onClick={() =>
							heldBy
								? choice.open({
										profileId: profile.id,
										profileName: profile.name,
										holder: heldBy,
									})
								: void choice.apply(profile.id)
						}
					>
						{t("deskPanel.banner.choose")}
					</Button>
				}
			>
				{t("deskPanel.banner.inUseHint", { name: profile.name })}
			</Callout>
		);
	} else if (profile?.status === "readonly") {
		banner = (
			<Callout
				tone="neutral"
				icon="eye"
				title={profileStatusText(profile, t).text}
				actions={
					<Button
						size="sm"
						disabled={choice.pending}
						onClick={() => {
							if (window.confirm(t("deskPanel.banner.writeBackConfirm")))
								void choice.apply(profile.id);
						}}
					>
						{t("deskPanel.banner.writeBack")}
					</Button>
				}
			>
				{profile.readonlyReason === "taken_over"
					? t("deskPanel.banner.takenOverHint")
					: t("deskPanel.banner.readonlyHint")}
			</Callout>
		);
	} else if (profile?.status === "error") {
		banner = (
			<Callout
				tone="err"
				icon="browser"
				title={t("deskPanel.banner.errorTitle", { name: profile.name })}
				actions={
					<Button
						size="sm"
						icon="refresh"
						disabled={choice.pending}
						onClick={() =>
							void choice.apply(
								profile.id,
								profile.mode === "readonly" ? "readonly" : undefined,
							)
						}
					>
						{t("deskPanel.banner.retry")}
					</Button>
				}
			>
				{profileErrorText(profile.error, t)}
			</Callout>
		);
	}

	return (
		<div
			className="flex h-full min-h-0 flex-col"
			data-testid="desktop-panel"
			style={{
				paddingBottom: "max(env(safe-area-inset-bottom), var(--kb-inset))",
			}}
		>
			<div
				role="toolbar"
				aria-label={t("deskPanel.toolbar")}
				className="flex h-11 shrink-0 items-center gap-0.5 border-border border-b px-2"
			>
				<ProfileMenu
					orbId={orb.id}
					current={profile}
					browserRunning={running}
					choice={choice}
					onSave={() => void runSave()}
					saving={save.isPending}
				/>
				<span
					data-testid="desktop-status"
					data-state={shownStatus}
					className="ml-1 inline-flex shrink-0"
				>
					<StatusDot
						tone={STATUS_TONE[shownStatus]}
						pulse={shownStatus === "connecting"}
						label={t(`deskPanel.status.${shownStatus}`)}
					/>
				</span>
				<span className="min-w-2 flex-1" />
				<IconButton
					data-testid="desktop-control"
					icon={control ? "cursor" : "eye"}
					box="sm"
					size={15}
					label={control ? t("deskPanel.toWatch") : t("deskPanel.toControl")}
					active={!control}
					onClick={() => uiStore.getState().setDesktopControl(!control)}
				/>
				<ClipboardPopover
					remoteText={remoteClip}
					disabled={shownStatus !== "connected"}
					onSend={(text) => {
						lastSentClip.current = text;
						sessionRef.current?.paste(text);
					}}
				/>
				{coarse && (
					<IconButton
						data-testid="desktop-keyboard"
						icon="keyboard"
						box="sm"
						size={15}
						label={kbOpen ? t("deskPanel.kb.hide") : t("deskPanel.kb.show")}
						active={kbOpen}
						disabled={!control}
						onClick={() => setKbOpen((v) => !v)}
					/>
				)}
				<Menu
					items={moreItems}
					trigger={(p) => (
						<IconButton
							icon="more"
							box="sm"
							size={15}
							label={t("deskPanel.more")}
							{...p}
						/>
					)}
				/>
				<IconButton
					data-testid="desktop-fullscreen"
					icon="maximize"
					box="sm"
					size={15}
					label={t("deskPanel.fullscreen")}
					onClick={() => void fullscreen()}
				/>
			</div>
			{(banner || notice || save.error || choice.error || browser.error) && (
				<div className="flex shrink-0 flex-col gap-1.5 border-border border-b px-2 py-2">
					{banner}
					{notice && (
						<Callout tone="ok" role="status">
							{notice}
						</Callout>
					)}
					<ErrorText error={save.error ?? choice.error ?? browser.error} />
				</div>
			)}
			<div className="min-h-0 flex-1 p-2">
				<div
					ref={stageRef}
					data-testid="desktop-stage"
					className="relative h-full overflow-hidden rounded-lg border border-border bg-inset"
				>
					<div ref={screenRef} className="absolute inset-0" />
					{!control && shownStatus === "connected" && (
						<span className="pointer-events-none absolute top-2 left-2 z-10 inline-flex items-center gap-1 rounded-full bg-panel/85 px-2 py-0.5 text-2xs text-muted shadow-xs">
							<Icon name="eye" size={11} aria-hidden />
							{t("deskPanel.watching")}
						</span>
					)}
					{orb.state !== "ready" || !desktop.displayReady ? (
						<OrbCover threadId={threadId} orb={orb} />
					) : showClosed ? (
						<StageCover testId="desktop-closed">
							<span className="grid h-11 w-11 place-items-center rounded-full bg-inset">
								<Icon name="monitor" size={20} className="text-faint" />
							</span>
							<div className="space-y-1">
								<p className="font-medium text-fg text-sm">{text.title}</p>
								<p className="max-w-xs text-muted text-xs">{text.hint}</p>
							</div>
							{closeCode !== 4409 &&
								closeCode !== 4410 &&
								closeCode !== 4401 && (
									<Button icon="refresh" onClick={reconnect}>
										{t("deskPanel.reconnect")}
									</Button>
								)}
						</StageCover>
					) : shownStatus === "connecting" ||
						(shownStatus === "closed" && want) ? (
						<StageCover testId="desktop-connecting">
							<p className="flex items-center gap-2 text-muted text-sm">
								<Spinner size={12} />
								{t("deskPanel.connecting")}
							</p>
						</StageCover>
					) : shownStatus === "connected" && !running ? (
						<div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center">
							{desktop.browser === "starting" || browser.isPending ? (
								<p className="flex items-center gap-2 rounded-full bg-panel/85 px-3 py-1.5 text-muted text-sm shadow-xs">
									<Spinner size={12} />
									{t("deskPanel.browserStarting")}
								</p>
							) : (
								<Button
									variant="ghost"
									icon="browser"
									data-testid="desktop-start-browser"
									className="pointer-events-auto bg-panel/85 shadow-xs"
									onClick={() => browser.mutate(true)}
								>
									{t("deskPanel.startBrowser")}
								</Button>
							)}
						</div>
					) : null}
				</div>
			</div>
			{kbOpen && control && (
				<SoftKeyboard
					onKey={(sym, down) => sessionRef.current?.sendKey(sym, down)}
				/>
			)}
			{choice.dialog}
		</div>
	);
}

export default function DesktopPanel({
	threadId,
}: {
	threadId: string;
	orbId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const orb = useOrbOf(threadId);
	if (!orb || orb.state === "deleted")
		return (
			<EmptyState icon="monitor" title={t("deskPanel.orbGone")}>
				{t("deskPanel.orbGoneHint")}
			</EmptyState>
		);
	if (!orb.desktop)
		return (
			<EmptyState icon="monitor" title={t("deskPanel.unsupported")}>
				{t("deskPanel.unsupportedHint")}
			</EmptyState>
		);
	return (
		// 换 orb 即重建：会话、重试计数与剪贴板都属于这台 orb
		<DesktopView
			key={orb.id}
			threadId={threadId}
			orb={orb as Orb & { desktop: NonNullable<Orb["desktop"]> }}
		/>
	);
}
