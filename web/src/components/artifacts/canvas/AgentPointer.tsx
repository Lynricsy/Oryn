/**
 * 画布上的 agent 在场（计划 M14 6.7）：各线程的指针、工作中的画框高亮与「跟随 {线程}」。
 * - 指针在线程 focus 画框的中心（同一画框上有几个线程就往下错开），文字「在看「…」/ 正在改「…」/ 正在画「…」/ 正在挪「…」」；
 *   只画当前画布页上的画框。改（write）与画（draw）时给那个画框加工作中的高亮框。
 * - 跟随：由查看器顶栏的在场头像发起（follow.ts）。镜头跟着被跟随线程的 focus 走（`reveal`：已经完整可见就不动，
 *   在别的画布页时先切过去），其它指针变淡，顶部一条「正在跟随…」带停止按钮。用户自己平移或缩放（镜头不是跟随设的）即停止；
 *   那个线程不在在场信息里了（回合结束）、编辑器不再跟随最新版本或卸载时也停止。
 * 编辑器只做挂接：调 `useAgentPresence`，把结果交给 `AgentPointers`（画布区里、覆盖层之上）与 `FollowBar`。
 */
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type {
	Box,
	CanvasDoc,
	CanvasPage,
} from "../../../lib/artifacts/canvas-doc";
import type { RealtimeClient } from "../../../realtime/socket";
import { Icon } from "../../ui/icons";
import { Button } from "../../ui/primitives";
import {
	presenceOrder,
	type ThreadActivity,
	ThreadFace,
	useThreadName,
} from "../PresenceFaces";
import { useArtifactActivity } from "../use-activity";
import { type Camera, reveal, toScreen } from "./camera";
import { setFollowing, useFollowing } from "./follow";

/** 同一画框上的几个指针依次往下错开的屏幕像素。 */
export const POINTER_STACK = 26;
/** 跟随切换画布页之后，这段时间内镜头的变化算编辑器自己的（切页时的适合全部），不算用户平移。 */
const ADOPT_MS = 300;

export interface PointerSpot {
	activity: ThreadActivity;
	page: CanvasPage;
	/** 指针尖的位置（画布区的屏幕坐标）。 */
	x: number;
	y: number;
}

/** 当前画布页上各线程指针的位置：focus 画框的中心，同一画框上的依次往下错开。正在改的在前。 */
export function pointerSpots(
	threads: readonly ThreadActivity[],
	doc: CanvasDoc | null,
	canvasKey: string,
	cam: Camera,
): PointerSpot[] {
	if (!doc) return [];
	const stacked = new Map<string, number>();
	const out: PointerSpot[] = [];
	for (const activity of presenceOrder(threads)) {
		const id = activity.focus?.page;
		const page = id
			? doc.pages.find((p) => p.id === id && p.canvas === canvasKey)
			: undefined;
		if (!page) continue;
		const n = stacked.get(page.id) ?? 0;
		stacked.set(page.id, n + 1);
		const c = toScreen(cam, page.x + page.width / 2, page.y + page.height / 2);
		out.push({ activity, page, x: c.x, y: c.y + n * POINTER_STACK });
	}
	return out;
}

/** 有线程在改或画的画框（当前画布页上），屏幕坐标。 */
export function workingBoxes(
	spots: readonly PointerSpot[],
	cam: Camera,
): { id: string; box: Box }[] {
	const seen = new Set<string>();
	const out: { id: string; box: Box }[] = [];
	for (const { activity, page } of spots) {
		if (activity.doing !== "write" && activity.doing !== "draw") continue;
		if (seen.has(page.id)) continue;
		seen.add(page.id);
		const s = toScreen(cam, page.x, page.y);
		out.push({
			id: page.id,
			box: {
				x: s.x,
				y: s.y,
				width: page.width * cam.zoom,
				height: page.height * cam.zoom,
			},
		});
	}
	return out;
}

export interface AgentPresenceInput {
	artifactId: string;
	/** 跟随最新版本（看旧版本时没有在场）。 */
	live: boolean;
	doc: CanvasDoc | null;
	canvasKey: string;
	cam: Camera;
	/** 画布区尺寸。 */
	size: { width: number; height: number };
	setCam(cam: Camera): void;
	/** 切到另一个画布页（跟随的线程在别的页上时）。 */
	switchCanvas(id: string): void;
	client?: Pick<RealtimeClient, "onArtifact" | "lastArtifactActivity">;
}

export interface AgentPresence {
	artifactId: string;
	spots: PointerSpot[];
	working: { id: string; box: Box }[];
	/** 正在跟随的线程。 */
	following: string | null;
	stop(): void;
}

export function useAgentPresence({
	artifactId,
	live,
	doc,
	canvasKey,
	cam,
	size,
	setCam,
	switchCanvas,
	client,
}: AgentPresenceInput): AgentPresence {
	const activity = useArtifactActivity(artifactId, client);
	const threads = live ? (activity?.threads ?? []) : [];
	const followingRaw = useFollowing(artifactId);
	const following = live ? followingRaw : null;
	const target = following
		? threads.find((a) => a.threadId === following)
		: undefined;
	const targetId = target?.focus?.page;
	const targetPage = targetId
		? doc?.pages.find((p) => p.id === targetId)
		: undefined;

	const camRef = useRef(cam);
	camRef.current = cam;
	const setCamRef = useRef(setCam);
	setCamRef.current = setCam;
	const switchRef = useRef(switchCanvas);
	switchRef.current = switchCanvas;
	// 跟随最近一次设的镜头；镜头变成别的值 = 用户自己动了
	const own = useRef<Camera | null>(null);
	const adoptUntil = useRef(0);

	// 不再跟随最新版本、线程离开（回合结束）时停止；卸载时停止
	const gone =
		!!followingRaw &&
		(!live ||
			(activity !== null &&
				!activity.threads.some((a) => a.threadId === followingRaw)));
	useEffect(() => {
		if (gone) setFollowing(artifactId, null);
	}, [gone, artifactId]);
	useEffect(() => () => setFollowing(artifactId, null), [artifactId]);

	const follow = () => {
		if (!following) return;
		if (own.current === null) own.current = camRef.current;
		if (!targetPage) return;
		if (targetPage.canvas !== canvasKey) {
			adoptUntil.current = performance.now() + ADOPT_MS;
			switchRef.current(targetPage.canvas);
			return;
		}
		if (size.width === 0 || size.height === 0) return;
		const next = reveal(camRef.current, targetPage, size.width, size.height);
		own.current = next;
		if (next !== camRef.current) setCamRef.current(next);
	};
	const followRef = useRef(follow);
	followRef.current = follow;

	// 镜头变了：跟随设的就不管；切画布页之后编辑器自己设的（适合全部）接过来再跟一次；其余是用户平移或缩放，停止跟随
	useEffect(() => {
		if (!following) {
			own.current = null;
			return;
		}
		if (own.current === null || cam === own.current) return;
		if (performance.now() < adoptUntil.current) {
			own.current = cam;
			followRef.current();
			return;
		}
		setFollowing(artifactId, null);
	}, [cam, following, artifactId]);

	// focus（或它的画框位置、画布页、画布区尺寸）变了：镜头跟过去
	// biome-ignore lint/correctness/useExhaustiveDependencies: 这些值变化时重新跟随；跟随本身读 followRef 里的最新值
	useEffect(() => {
		followRef.current();
	}, [
		following,
		targetPage?.id,
		targetPage?.canvas,
		targetPage?.x,
		targetPage?.y,
		targetPage?.width,
		targetPage?.height,
		canvasKey,
		size.width,
		size.height,
	]);

	const spots = pointerSpots(threads, doc, canvasKey, cam);
	return {
		artifactId,
		spots,
		working: workingBoxes(spots, cam),
		following,
		stop: () => setFollowing(artifactId, null),
	};
}

/** 一个线程的指针：箭头 + 「头像 正在改「画框」」。 */
export function AgentPointer({
	spot,
	dim = false,
}: {
	spot: PointerSpot;
	/** 在跟随别的线程：变淡。 */
	dim?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const { activity, page } = spot;
	const thread = useThreadName(activity.threadId);
	const text = t(`artifacts.presence.pointer.${activity.doing}`, {
		name: page.name,
	});
	return (
		<div
			data-testid="agent-pointer"
			data-thread={activity.threadId}
			data-doing={activity.doing}
			data-page={page.id}
			title={`${thread} · ${text}`}
			className="absolute flex items-start transition-[left,top,opacity] duration-300 ease-out"
			style={{ left: spot.x, top: spot.y, opacity: dim ? 0.35 : 1 }}
		>
			<Icon
				name="cursor"
				size={18}
				weight="fill"
				className="-mt-0.5 -ml-0.5 shrink-0 text-ink drop-shadow-sm"
			/>
			<span className="mt-3.5 -ml-1 inline-flex max-w-60 items-center gap-0.5 rounded-full bg-ink py-px pr-2 pl-px text-ink-fg text-xs shadow-md">
				<ThreadFace threadId={activity.threadId} size={14} />
				<span className="truncate">{text}</span>
			</span>
		</div>
	);
}

/** 画布区里的在场层：工作中的画框高亮与各线程的指针。不接管指针。 */
export function AgentPointers({
	presence,
}: {
	presence: AgentPresence;
}): ReactNode {
	const { spots, working, following } = presence;
	if (spots.length === 0) return null;
	return (
		<div
			data-testid="agent-presence"
			className="pointer-events-none absolute inset-0 overflow-hidden"
		>
			{working.map(({ id, box }) => (
				<div
					key={id}
					data-testid="agent-working"
					data-page={id}
					className="agent-ring-breathe absolute rounded-sm outline-2 outline-cat-edit outline-offset-2"
					style={{
						left: box.x,
						top: box.y,
						width: box.width,
						height: box.height,
					}}
				/>
			))}
			{spots.map((s) => (
				<AgentPointer
					key={s.activity.threadId}
					spot={s}
					dim={following !== null && s.activity.threadId !== following}
				/>
			))}
		</div>
	);
}

/** 「正在跟随 {线程}」与停止按钮（画布区顶部居中）。 */
export function FollowBar({
	presence,
}: {
	presence: AgentPresence;
}): ReactNode {
	const { t } = useTranslation();
	const name = useThreadName(presence.following ?? "");
	if (!presence.following) return null;
	return (
		<div className="pointer-events-none absolute inset-x-0 top-2 z-10 flex justify-center px-2">
			<div
				role="status"
				data-testid="canvas-following"
				className="pointer-events-auto flex min-w-0 items-center gap-2 rounded-full border border-border bg-panel py-0.5 pr-0.5 pl-1 text-sm shadow-sm"
			>
				<ThreadFace threadId={presence.following} size={16} />
				<span className="min-w-0 truncate">
					{t("artifacts.presence.following", { thread: name })}
				</span>
				<Button size="sm" variant="ghost" onClick={presence.stop}>
					{t("artifacts.presence.stopFollow")}
				</Button>
			</div>
		</div>
	);
}
