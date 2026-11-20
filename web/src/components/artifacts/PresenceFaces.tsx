/**
 * 在场头像（计划 M14 6.7）：谁在改、谁在读这个 artifact，以及「在 N 处打开」。
 * - 正在改（写 / 画 / 挪）的线程在前、只是在读的在后；最多 3 个头像，更多显示「+N」；正在改的头像外圈转圈。
 * - 查看器顶栏用 `PresenceFaces`：点头像打开那个线程，并在右面板显示这个 artifact；画布（`canFollow`）上点头像弹菜单，
 *   多一项「跟随 {线程}」（镜头跟着它的 focus 走，见 canvas/AgentPointer.tsx）。观看者数大于 1 时显示「在 N 处打开」。
 * - 卡片用 `PresenceStack`：同样的头像排列，不可点（卡片本身是按钮）。
 * 在场信息只有正在观看这个 artifact 的连接收得到（`useArtifactActivity`）；没有时什么都不渲染。
 * 头像：全局助手对话用助手身份，其余用默认 Agent 身份（同转录的回落身份）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useAssistantIdentity, useIdentity } from "../../api/identity-queries";
import type {
	ArtifactActivityEvent,
	RealtimeClient,
} from "../../realtime/socket";
import { useIndex } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { AgentStatusRing } from "../ui/AgentStatusAvatar";
import { IdentityAvatar } from "../ui/IdentityAvatar";
import { Menu, type MenuItem } from "../ui/primitives";
import { setFollowing, useFollowing } from "./canvas/follow";
import { useArtifactActivity } from "./use-activity";

export type ThreadActivity = ArtifactActivityEvent["threads"][number];

/** 头像最多显示几个，其余并成「+N」。 */
export const MAX_FACES = 3;
/** 外圈与头像之间的留白（同 AgentStatusAvatar），有没有外圈布局都不动。 */
const RING_PAD = 3;

type ActivitySource = Pick<
	RealtimeClient,
	"onArtifact" | "lastArtifactActivity"
>;

/** 详情接口里与这个 artifact 有关的线程：索引里没有（别的项目、已删除）时用它的标题。 */
export type KnownThread = { threadId: string; title: string };

/** 正在改（不是只读）。 */
export function isWorking(a: ThreadActivity): boolean {
	return a.doing !== "read";
}

/** 每个线程一项（同一线程出现多次时取正在改的那项），正在改的在前、在读的在后，各自保持到达顺序。 */
export function presenceOrder(
	threads: readonly ThreadActivity[],
): ThreadActivity[] {
	const byThread = new Map<string, ThreadActivity>();
	for (const a of threads) {
		const cur = byThread.get(a.threadId);
		if (!cur || (!isWorking(cur) && isWorking(a))) byThread.set(a.threadId, a);
	}
	const all = [...byThread.values()];
	return [...all.filter(isWorking), ...all.filter((a) => !isWorking(a))];
}

/** 线程的显示名：索引里的标题 → 详情给的标题 → 「一个线程」。 */
export function useThreadName(
	threadId: string,
	known?: readonly KnownThread[],
): string {
	const { t } = useTranslation();
	const title = useIndex((s) => s.threads[threadId]?.title);
	return (
		title ||
		known?.find((x) => x.threadId === threadId)?.title ||
		t("artifacts.presence.unknownThread")
	);
}

/** 线程的头像；`working` 时外圈转圈（与子 agent 运行中的外圈同一套画法）。 */
export function ThreadFace({
	threadId,
	size = 20,
	working = false,
}: {
	threadId: string;
	size?: number;
	working?: boolean;
}): ReactNode {
	const assistant = useIndex((s) => s.threads[threadId]?.kind) === "assistant";
	const agent = useIdentity("agent", !assistant).data;
	const helper = useAssistantIdentity(assistant);
	const box = size + RING_PAD * 2;
	return (
		<span
			aria-hidden
			data-working={working ? "" : undefined}
			className="relative inline-grid shrink-0 place-items-center"
			style={{ width: box, height: box }}
		>
			{working && (
				<span className="absolute inset-0">
					<AgentStatusRing state="running" size={box} />
				</span>
			)}
			<IdentityAvatar
				identity={
					assistant
						? helper.avatar
						: { avatarIcon: agent?.avatarIcon, avatarImage: agent?.avatarImage }
				}
				of={assistant ? "assistant" : "agent"}
				size={size}
				shape="circle"
			/>
		</span>
	);
}

/** 头像旁的说明：「{线程}：正在改」。 */
function useFaceLabel(a: ThreadActivity, known?: readonly KnownThread[]) {
	const { t } = useTranslation();
	return t("artifacts.presence.face", {
		thread: useThreadName(a.threadId, known),
		doing: t(`artifacts.presence.doing.${a.doing}`),
	});
}

function MoreCount({ n }: { n: number }): ReactNode {
	const { t } = useTranslation();
	if (n <= 0) return null;
	return (
		<span
			data-testid="presence-more"
			title={t("artifacts.presence.moreTitle", { count: n })}
			className="ml-0.5 shrink-0 font-medium text-2xs text-muted tabular-nums"
		>
			+{n}
		</span>
	);
}

/** 不可点的一张头像（卡片）。 */
function StaticFace({
	activity,
	size,
}: {
	activity: ThreadActivity;
	size: number;
}): ReactNode {
	const label = useFaceLabel(activity);
	return (
		<span
			data-testid="presence-face"
			data-thread={activity.threadId}
			data-doing={activity.doing}
			title={label}
			className="-ml-1 inline-flex first:ml-0"
		>
			<ThreadFace
				threadId={activity.threadId}
				size={size}
				working={isWorking(activity)}
			/>
		</span>
	);
}

/** 卡片上的在场头像：不可点；没有在场信息时不渲染。 */
export function PresenceStack({
	artifactId,
	size = 16,
	client,
}: {
	artifactId: string;
	size?: number;
	client?: ActivitySource;
}): ReactNode {
	const activity = useArtifactActivity(artifactId, client);
	const order = presenceOrder(activity?.threads ?? []);
	if (order.length === 0) return null;
	return (
		<span
			data-testid="presence-stack"
			className="inline-flex shrink-0 items-center"
		>
			{order.slice(0, MAX_FACES).map((a) => (
				<StaticFace key={a.threadId} activity={a} size={size} />
			))}
			<MoreCount n={order.length - MAX_FACES} />
		</span>
	);
}

/** 打开线程并在右面板显示这个 artifact（同更多菜单的「打开线程」）。 */
function useOpenThread(artifactId: string) {
	const navigate = useNavigate();
	return (threadId: string) => {
		navigate(`/t/${threadId}`);
		uiStore.getState().openArtifact(threadId, artifactId);
	};
}

/** 查看器顶栏的一张头像：点击打开线程；可以跟随时弹菜单（跟随 / 停止跟随、打开线程）。 */
function ViewerFace({
	artifactId,
	activity,
	known,
	canFollow,
}: {
	artifactId: string;
	activity: ThreadActivity;
	known?: readonly KnownThread[];
	canFollow: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const label = useFaceLabel(activity, known);
	const name = useThreadName(activity.threadId, known);
	const open = useOpenThread(artifactId);
	const following = useFollowing(artifactId) === activity.threadId;
	const face = (
		<ThreadFace
			threadId={activity.threadId}
			size={20}
			working={isWorking(activity)}
		/>
	);
	// 头像相互压一点；悬停或聚焦的那张浮到最上
	const stacked = "-ml-1 first:ml-0 hover:z-10 focus-within:z-10";
	const btn = `relative inline-flex rounded-full ${following ? "ring-2 ring-border-strong" : ""}`;
	if (!canFollow)
		return (
			<button
				type="button"
				data-testid="presence-face"
				data-thread={activity.threadId}
				data-doing={activity.doing}
				aria-label={label}
				title={label}
				className={`${stacked} ${btn}`}
				onClick={() => open(activity.threadId)}
			>
				{face}
			</button>
		);
	const items: MenuItem[] = [
		following
			? {
					key: "follow",
					icon: "close",
					label: t("artifacts.presence.stopFollow"),
					onSelect: () => setFollowing(artifactId, null),
				}
			: {
					key: "follow",
					icon: "eye",
					label: t("artifacts.presence.follow", { thread: name }),
					onSelect: () => setFollowing(artifactId, activity.threadId),
				},
		{
			key: "open",
			icon: "message",
			label: t("artifacts.presence.openThread"),
			onSelect: () => open(activity.threadId),
		},
	];
	return (
		<Menu
			className={stacked}
			items={items}
			trigger={(p) => (
				<button
					type="button"
					data-testid="presence-face"
					data-thread={activity.threadId}
					data-doing={activity.doing}
					aria-label={label}
					title={label}
					className={btn}
					{...p}
				>
					{face}
				</button>
			)}
		/>
	);
}

/** 查看器顶栏的在场：头像、「+N」与「在 N 处打开」；什么都没有时不渲染。 */
export function PresenceFaces({
	artifactId,
	threads,
	canFollow = false,
	client,
}: {
	artifactId: string;
	/** 详情接口的线程（索引里查不到的线程用它的标题）。 */
	threads?: readonly KnownThread[];
	/** 画布编辑器在跟随最新版本：头像菜单里有「跟随」。 */
	canFollow?: boolean;
	client?: ActivitySource;
}): ReactNode {
	const { t } = useTranslation();
	const activity = useArtifactActivity(artifactId, client);
	const order = presenceOrder(activity?.threads ?? []);
	const viewers = activity?.viewers ?? 0;
	if (order.length === 0 && viewers <= 1) return null;
	return (
		<div
			data-testid="presence-faces"
			className="flex shrink-0 items-center gap-1.5 px-1"
		>
			{order.length > 0 && (
				<span className="inline-flex items-center">
					{order.slice(0, MAX_FACES).map((a) => (
						<ViewerFace
							key={a.threadId}
							artifactId={artifactId}
							activity={a}
							known={threads}
							canFollow={canFollow}
						/>
					))}
					<MoreCount n={order.length - MAX_FACES} />
				</span>
			)}
			{viewers > 1 && (
				<span
					data-testid="presence-viewers"
					className="whitespace-nowrap text-2xs text-muted"
				>
					{t("artifacts.presence.openIn", { count: viewers })}
				</span>
			)}
		</div>
	);
}
