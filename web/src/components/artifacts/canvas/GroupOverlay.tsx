/**
 * 方案组（计划 M34 S9）：当前画布页上的方案组覆盖层与选定后的提示条。
 *
 * - `GroupOverlay`：组里位于当前画布页的画框用虚线外接框包住，框上是组标题「方案组 · 名字」；每个方案在它最上面的画框上方
 *   （画框名称行之上）有方案标签：方案标记、名字（`note` 作悬停提示）与动作。屏幕坐标、字号不随缩放，同 `FrameLabel`。
 *   未选定的组每个方案都有「选定此方案」；选定后选中方案显示「已选定」，其它方案改为「改选此方案」。只在给了 `commit`
 *   （可编辑）时有按钮。落选方案搬到「备选方案」页后，那一页上照样画出它们的组框与方案标签。
 * - 选定提交 `pickOps`（`expandPicks` 展开后的操作，含画框的搬移），走画布操作通道落成用户版本；不弹确认框，靠版本历史撤销。
 * - `PickNotice`：用户选定后的可关闭提示条（C5）。HTML 画框画布给「让 Agent 统一到此方案」，用 `pickPrompt` 发给
 *   `sendTargets` 排在最前的线程（没有就交给首屏输入框）；设计画布由便签投递自动通知 agent，只提示「下一回合统一主题」。
 */
import type { ReactNode } from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	useArtifact,
	useArtifactVersions,
} from "../../../api/artifact-queries";
import {
	type Box,
	type CanvasDoc,
	type CanvasGroup,
	type CanvasOp,
	type CanvasPage,
	type CanvasVariant,
	expandPicks,
} from "../../../lib/artifacts/canvas-doc";
import { pickPrompt } from "../../../lib/artifacts/prompt";
import { sendGeneratedPrompt } from "../../../state/actions";
import { useIndex } from "../../../state/stores";
import { showToast } from "../../../state/toasts";
import { uiStore } from "../../../state/ui";
import { Badge } from "../../ui/data";
import { Button, describeError, IconButton } from "../../ui/primitives";
import { sendTargets } from "../CommentsPanel";
import { useArtifactActivity } from "../use-activity";
import { type Camera, toScreen } from "./camera";
import { LABEL_GAP, LABEL_HEIGHT } from "./FrameLabel";

/** 方案标签行的高度、它与画框名称行的间距，外接框四周留白（屏幕像素）。 */
const ROW_HEIGHT = 28;
const ROW_GAP = 2;
const PAD = 12;
/** 方案标签至少这么宽，缩得很小时按钮也放得下。 */
const ROW_MIN_WIDTH = 176;

/** 用户选定 `group` 的 `variant`：提交的操作（含落选与改选画框的搬移，同服务端的 ExpandPicks）。 */
export function pickOps(
	doc: CanvasDoc,
	group: string,
	variant: string,
): CanvasOp[] {
	return expandPicks(doc, [
		{ op: "set", id: group, key: "picked", value: variant },
	]);
}

/** 一个方案在当前画布页上的落点：它最上面（再最左）的画框。 */
interface VariantSpot {
	variant: CanvasVariant;
	top: CanvasPage;
}

interface GroupSpot {
	group: CanvasGroup;
	/** 组里位于当前画布页的画框的外接框（世界坐标）。 */
	box: Box;
	variants: VariantSpot[];
}

/** 各方案组在 `canvas` 页上的落点；组里没有画框在这一页的不画。 */
export function groupSpots(doc: CanvasDoc, canvas: string): GroupSpot[] {
	const byId = new Map(doc.pages.map((p) => [p.id, p]));
	const out: GroupSpot[] = [];
	for (const group of doc.groups ?? []) {
		const variants: VariantSpot[] = [];
		let x1 = Number.POSITIVE_INFINITY;
		let y1 = Number.POSITIVE_INFINITY;
		let x2 = Number.NEGATIVE_INFINITY;
		let y2 = Number.NEGATIVE_INFINITY;
		for (const variant of group.variants) {
			let top: CanvasPage | null = null;
			for (const id of variant.pages) {
				const p = byId.get(id);
				if (!p || p.canvas !== canvas) continue;
				x1 = Math.min(x1, p.x);
				y1 = Math.min(y1, p.y);
				x2 = Math.max(x2, p.x + p.width);
				y2 = Math.max(y2, p.y + p.height);
				if (!top || p.y < top.y || (p.y === top.y && p.x < top.x)) top = p;
			}
			if (top) variants.push({ variant, top });
		}
		if (variants.length > 0)
			out.push({
				group,
				box: { x: x1, y: y1, width: x2 - x1, height: y2 - y1 },
				variants,
			});
	}
	return out;
}

/** 画框所属的方案（画框名称行的方案标记用）。 */
export function variantByPage(doc: CanvasDoc): Map<string, CanvasVariant> {
	const out = new Map<string, CanvasVariant>();
	for (const g of doc.groups ?? [])
		for (const v of g.variants) for (const p of v.pages) out.set(p, v);
	return out;
}

export interface GroupOverlayProps {
	doc: CanvasDoc;
	/** 当前画布页。 */
	canvas: string;
	cam: Camera;
	/** 可编辑时给：提交选定的操作，回是否已提交。不给时只显示，没有选定按钮。 */
	commit?(ops: CanvasOp[]): boolean;
	/** 选定已提交（提示条用）。 */
	onPicked?(group: string): void;
}

export function GroupOverlay({
	doc,
	canvas,
	cam,
	commit,
	onPicked,
}: GroupOverlayProps): ReactNode {
	const { t } = useTranslation();
	const spots = groupSpots(doc, canvas);
	if (spots.length === 0) return null;
	const z = cam.zoom;
	// 方案标签在画框名称行之上
	const above = LABEL_HEIGHT + LABEL_GAP + ROW_GAP + ROW_HEIGHT;
	const pick = (g: CanvasGroup, v: CanvasVariant) => {
		if (commit?.(pickOps(doc, g.id, v.id))) onPicked?.(g.id);
	};
	return (
		<div
			data-testid="canvas-groups"
			className="pointer-events-none absolute inset-0"
		>
			{spots.map(({ group: g, box, variants }) => {
				const s = toScreen(cam, box.x, box.y);
				const left = s.x - PAD;
				const top = s.y - above - PAD;
				return (
					<div key={g.id} data-group={g.id}>
						<div
							aria-hidden
							className="absolute rounded-xl border border-border-strong border-dashed"
							style={{
								left,
								top,
								width: box.width * z + PAD * 2,
								height: box.height * z + above + PAD * 2,
							}}
						/>
						<span
							data-testid="canvas-group-title"
							title={g.name}
							className="absolute max-w-80 truncate rounded-full border border-border bg-panel px-2 font-medium text-muted text-xs leading-5 shadow-xs"
							style={{ left: left + PAD, top: top - 11 }}
						>
							{t("artifacts.canvas.groups.title", { name: g.name })}
						</span>
						{variants.map(({ variant: v, top: page }) => {
							const p = toScreen(cam, page.x, page.y);
							return (
								<div
									key={v.id}
									data-variant={`${g.id}/${v.id}`}
									className="pointer-events-auto absolute flex items-center gap-1.5"
									style={{
										left: p.x,
										top: p.y - above,
										width: Math.max(page.width * z, ROW_MIN_WIDTH),
										height: ROW_HEIGHT,
									}}
								>
									<span className="inline-flex h-5 max-w-16 shrink-0 items-center truncate rounded bg-accent-soft px-1.5 font-semibold text-accent text-xs">
										{v.id.toUpperCase()}
									</span>
									<span
										title={v.note || undefined}
										className="min-w-0 truncate font-medium text-fg text-sm"
									>
										{v.name}
									</span>
									<span className="ml-auto flex shrink-0 items-center">
										{g.picked === v.id ? (
											<span data-testid="canvas-group-picked">
												<Badge tone="ok" icon="check">
													{t("artifacts.canvas.groups.picked")}
												</Badge>
											</span>
										) : commit ? (
											<Button
												size="sm"
												data-testid="canvas-group-pick"
												aria-label={t(
													g.picked === null
														? "artifacts.canvas.groups.pickLabel"
														: "artifacts.canvas.groups.repickLabel",
													{ name: v.name },
												)}
												onClick={() => pick(g, v)}
											>
												{t(
													g.picked === null
														? "artifacts.canvas.groups.pick"
														: "artifacts.canvas.groups.repick",
												)}
											</Button>
										) : null}
									</span>
								</div>
							);
						})}
					</div>
				);
			})}
		</div>
	);
}

/**
 * 用户选定方案后的提示条（C5）：`group` 还是已选定时显示，关掉或组不再选定时消失。
 * 固定在画布视口右下角、与底部工具条同一行（工具条居中，提示条最宽到工具条右侧为止）：这一行落在适合全部时的留白里，
 * 不压画框，也不压画框上方的名称行与方案标签。
 */
export function PickNotice({
	artifactId,
	doc,
	group,
	onClose,
}: {
	artifactId: string;
	doc: CanvasDoc;
	group: string;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const g = doc.groups?.find((x) => x.id === group);
	const v = g?.variants.find((x) => x.id === g.picked);
	if (!v) return null;
	const text = t(
		doc.design
			? "artifacts.canvas.groups.noticeDesign"
			: "artifacts.canvas.groups.notice",
		{ name: v.name },
	);
	return (
		<div
			role="status"
			data-testid="canvas-pick-notice"
			className="absolute right-2 bottom-2 z-10 flex max-w-[calc(50%-17.5rem)] items-center gap-1.5 overflow-hidden rounded-xl border border-border bg-panel p-1 pl-2.5 text-sm shadow-md"
		>
			<span title={text} className="min-w-0 truncate">
				{text}
			</span>
			{!doc.design && (
				<UnifyButton
					artifactId={artifactId}
					doc={doc}
					group={group}
					onSent={onClose}
				/>
			)}
			<IconButton
				icon="close"
				box="sm"
				size={14}
				label={t("artifacts.canvas.groups.dismiss")}
				data-testid="canvas-pick-dismiss"
				onClick={onClose}
			/>
		</div>
	);
}

/** 「让 Agent 统一到此方案」：发给评论面板同款排序里的第一个线程；没有可发的线程就交给首屏输入框。 */
function UnifyButton({
	artifactId,
	doc,
	group,
	onSent,
}: {
	artifactId: string;
	doc: CanvasDoc;
	group: string;
	onSent(): void;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const detail = useArtifact(artifactId).data;
	const versions = useArtifactVersions(artifactId).data;
	const activity = useArtifactActivity(artifactId);
	const index = useIndex((s) => s.threads);
	const [busy, setBusy] = useState(false);

	const send = async () => {
		if (busy || !detail) return;
		const { artifact } = detail;
		const text = pickPrompt(
			{ id: artifactId, title: artifact.title },
			doc,
			group,
			t,
		);
		const [to] = sendTargets({
			detailThreads: detail.threads,
			index,
			projectId: artifact.projectId,
			activity: activity?.threads ?? [],
			versions: versions ?? [],
		});
		if (!to) {
			uiStore
				.getState()
				.seedHome(
					artifact.projectId
						? { text, projectId: artifact.projectId }
						: { text },
				);
			navigate("/");
			return;
		}
		setBusy(true);
		try {
			await sendGeneratedPrompt(to.threadId, text);
			showToast({
				text: t("artifacts.canvas.groups.unifySent", {
					thread: to.title || t("thread.untitled"),
				}),
			});
			onSent();
		} catch (err) {
			showToast({
				tone: "err",
				text: t("artifacts.canvas.groups.unifyFailed", {
					reason: describeError(err, t),
				}),
			});
		} finally {
			setBusy(false);
		}
	};

	return (
		<Button
			size="sm"
			variant="primary"
			icon="sparkle"
			data-testid="canvas-pick-unify"
			disabled={busy || !detail}
			onClick={() => void send()}
		>
			{t("artifacts.canvas.groups.unify")}
		</Button>
	);
}
