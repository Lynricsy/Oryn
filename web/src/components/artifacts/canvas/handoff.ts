/**
 * 「交给 Agent」（计划 M14 6.6）：把选中的画框与其上的标注连同最多 4 张合成图发给一个线程。
 *
 * - 范围（`handOffSet`）：选中的画框；与这些画框区域重叠的标注；被选中的标注；被选中的标注压在下面的画框。
 * - 目标（`handOffTarget`）：在场信息里 `doing` 不是 `read` 的第一个线程 → 创建线程（未归档）→ 都没有就走「新线程」。
 * - 发送（`handOff`）：消息由 `handoffPrompt` 生成（来自页面与标注的文字只在两行 `mark` 之间）。渲染可用时前 4 个画框
 *   各取一张 PNG（宽超过 1600 时按比例请求 1600 宽），叠画标注后作为附件上传，再以 `prompt`（agent 忙时 `follow_up`）
 *   发出；渲染不可用时只发文字。「新线程」交给首屏输入框（`seedHome`），不带图。
 * 发送的各个外部动作经 `HandOffDeps` 注入（测试替换它们）；`useHandOff` 把真实实现与 toast 接到画布编辑器上。
 */
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	type ArtifactDetail,
	type ArtifactThreadActivity,
	fetchArtifactPng,
} from "../../../api/artifact-queries";
import type { Thread } from "../../../api/client";
import {
	type CanvasDoc,
	type CanvasGroup,
	type CanvasNote,
	type CanvasPage,
	noteBox,
} from "../../../lib/artifacts/canvas-doc";
import { handoffPrompt } from "../../../lib/artifacts/prompt";
import { uploadAttachment } from "../../../lib/attachments";
import type { OmpCommand, OrynPromptExtras } from "../../../omp/types";
import { realtime } from "../../../realtime/socket";
import { isThreadBusy, promptFrame } from "../../../state/actions";
import { getThreadStore, useIndex } from "../../../state/stores";
import { showToast } from "../../../state/toasts";
import { type HomeSeed, uiStore } from "../../../state/ui";
import { describeError } from "../../ui/primitives";
import { useArtifactActivity } from "../use-activity";
import { composeFrameImage, shotSize } from "./compose-image";
import { readingOrder } from "./selection";

/** 最多附几张画框图。 */
export const HANDOFF_MAX_IMAGES = 4;

export interface HandOffSet {
	/** 阅读顺序。 */
	pages: CanvasPage[];
	/** 绘制顺序。 */
	notes: CanvasNote[];
	/** 文档的方案组：画框行带上方案信息（M34 C7）。 */
	groups: CanvasGroup[];
}

function overlaps(
	a: CanvasPage,
	b: { x: number; y: number; width: number; height: number },
): boolean {
	return (
		a.x <= b.x + b.width &&
		a.x + a.width >= b.x &&
		a.y <= b.y + b.height &&
		a.y + a.height >= b.y
	);
}

/** 交给 Agent 的范围：`ids` 是当前画布页上选中的画框与标注。 */
export function handOffSet(
	doc: CanvasDoc,
	canvas: string,
	ids: readonly string[],
): HandOffSet {
	const chosen = new Set(ids);
	const onCanvas = doc.pages.filter((p) => p.canvas === canvas);
	const notes = doc.notes.filter((n) => n.canvas === canvas);
	const picked = onCanvas.filter((p) => chosen.has(p.id));
	const pickedNotes = notes.filter((n) => chosen.has(n.id));
	const pageIds = new Set(picked.map((p) => p.id));
	for (const n of pickedNotes) {
		const b = noteBox(n);
		for (const p of onCanvas) if (overlaps(p, b)) pageIds.add(p.id);
	}
	const noteIds = new Set(pickedNotes.map((n) => n.id));
	for (const n of notes) {
		const b = noteBox(n);
		if (picked.some((p) => overlaps(p, b))) noteIds.add(n.id);
	}
	return {
		pages: readingOrder(doc, canvas).filter((p) => pageIds.has(p.id)),
		notes: notes.filter((n) => noteIds.has(n.id)),
		groups: doc.groups ?? [],
	};
}

/**
 * 发给哪个线程：正在改这个 artifact 的第一个线程，其次是未归档的创建线程；`null` = 新线程。
 */
export function handOffTarget({
	activity,
	creator,
	detailThreads,
	index,
}: {
	activity: readonly ArtifactThreadActivity[];
	creator: string | undefined;
	detailThreads: ArtifactDetail["threads"];
	index: Readonly<Record<string, Thread>>;
}): string | null {
	const working = activity.find((a) => a.doing !== "read");
	if (working) return working.threadId;
	if (!creator) return null;
	const archived =
		detailThreads.find((d) => d.threadId === creator)?.archived === true ||
		index[creator]?.archivedAt !== undefined;
	return archived ? null : creator;
}

export interface HandOffInput {
	artifact: { id: string; title: string; projectId?: string };
	/** 消息里写的版本（最新版本）。 */
	version: number;
	set: HandOffSet;
	/** 整份文档的画框（吸附的线与箭头端点用）。 */
	allPages: readonly CanvasPage[];
	/** 目标线程；`null` = 新线程。 */
	target: string | null;
	/** 控制面渲染可用（`capabilities.render`）。 */
	render: boolean;
}

/** 发送用到的外部动作。 */
export interface HandOffDeps {
	t: TFunction;
	/** 一个画框的合成图（截图 + 标注）。 */
	image(
		page: CanvasPage,
		notes: readonly CanvasNote[],
		allPages: readonly CanvasPage[],
		artifactId: string,
	): Promise<File>;
	upload(file: File, threadId: string): Promise<{ id: string }>;
	busy(threadId: string): boolean;
	send(
		threadId: string,
		frame: OmpCommand & OrynPromptExtras,
	): Promise<unknown>;
	seedHome(seed: HomeSeed): void;
	navigate(to: string): void;
}

/** 交给 Agent。回 `sent`（发到了已有线程）或 `seeded`（交给了首屏输入框）。 */
export async function handOff(
	input: HandOffInput,
	deps: HandOffDeps,
): Promise<"sent" | "seeded"> {
	const { artifact, set, target } = input;
	const text = handoffPrompt(
		{ id: artifact.id, title: artifact.title },
		input.version,
		set.pages,
		set.notes,
		deps.t,
		set.groups,
	);
	if (target === null) {
		deps.seedHome(
			artifact.projectId ? { text, projectId: artifact.projectId } : { text },
		);
		deps.navigate("/");
		return "seeded";
	}
	const ids: string[] = [];
	if (input.render)
		for (const page of set.pages.slice(0, HANDOFF_MAX_IMAGES)) {
			const file = await deps.image(
				page,
				set.notes,
				input.allPages,
				artifact.id,
			);
			ids.push((await deps.upload(file, target)).id);
		}
	await deps.send(
		target,
		promptFrame(deps.busy(target) ? "follow_up" : "prompt", text, [], ids),
	);
	return "sent";
}

/** 一个画框的合成图：按 `shotSize` 请求 PNG，叠画标注。 */
async function frameImage(
	page: CanvasPage,
	notes: readonly CanvasNote[],
	allPages: readonly CanvasPage[],
	artifactId: string,
): Promise<File> {
	const { blob } = await fetchArtifactPng(artifactId, {
		path: page.file,
		...shotSize(page),
	});
	const png = await composeFrameImage(blob, page, notes, allPages);
	return new File([png], `${page.name}.png`, { type: "image/png" });
}

/** 画布编辑器的「交给 Agent」：`run(ids)` 把当前画布页上的这些对象交出去；`busy` 时按钮禁用。 */
export function useHandOff({
	artifactId,
	detail,
	render,
}: {
	artifactId: string;
	detail: ArtifactDetail | undefined;
	render: boolean;
}): {
	busy: boolean;
	run(doc: CanvasDoc, canvas: string, ids: readonly string[]): void;
} {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const activity = useArtifactActivity(artifactId);
	const index = useIndex((s) => s.threads);
	const [busy, setBusy] = useState(false);

	const run = (doc: CanvasDoc, canvas: string, ids: readonly string[]) => {
		if (busy || !detail) return;
		const set = handOffSet(doc, canvas, ids);
		if (set.pages.length === 0 && set.notes.length === 0) return;
		const target = handOffTarget({
			activity: activity?.threads ?? [],
			creator: detail.artifact.threadId,
			detailThreads: detail.threads,
			index,
		});
		const toast = `artifact-handoff:${artifactId}`;
		if (target !== null)
			showToast({
				id: toast,
				busy: true,
				text: t("artifacts.canvas.handOff.running"),
			});
		setBusy(true);
		handOff(
			{
				artifact: detail.artifact,
				version: detail.version,
				set,
				allPages: doc.pages,
				target,
				render,
			},
			{
				t,
				image: frameImage,
				upload: (file, threadId) => uploadAttachment(file, { threadId }),
				busy: (threadId) => isThreadBusy(getThreadStore(threadId).getState()),
				send: (threadId, frame) => realtime.cmd(threadId, frame),
				seedHome: (seed) => uiStore.getState().seedHome(seed),
				navigate,
			},
		)
			.then(
				(how) => {
					if (how === "sent")
						showToast({ id: toast, text: t("artifacts.canvas.handOff.done") });
				},
				(err: unknown) =>
					showToast({
						id: toast,
						tone: "err",
						text: t("artifacts.canvas.handOff.failed", {
							reason: describeError(err, t),
						}),
					}),
			)
			.finally(() => setBusy(false));
	};
	return { busy, run };
}
