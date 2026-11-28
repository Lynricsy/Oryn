/**
 * 回退（rewind，对应 omp 的 Esc Esc、`/branch`、`/rewind`）：在原会话里把叶子移回所选用户消息之前（`switch_leaf`，
 * 原来的路径保留为分支，会话树里仍可切回），再把那条消息的原文与附件放回输入区。提示条带「撤销」：叶子回到回退前的
 * 位置，输入区回到回退前的草稿。会话树跳转带回的草稿也经这里放回输入区。
 */
import i18n from "../../i18n";
import {
	type Attachment,
	listThreadAttachments,
	parseAttachmentBlock,
} from "../../lib/attachments";
import { parseDraftEditsBlock } from "../../lib/draft-edits";
import type { ImageContent } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import {
	enqueueComposerRestore,
	getComposerDraft,
	type RestoredAttachment,
} from "../../state/composer-drafts";
import { getThreadStore } from "../../state/stores";
import { showToast } from "../../state/toasts";
import { describeError } from "../ui/primitives";

/** `switch_leaf` 应答里带回的草稿（目标是用户消息时才有）。 */
export interface LeafSwitchDraft {
	editorText?: string;
	editorImages?: ImageContent[];
}

let seq = 0;

const IMAGE_EXT: Record<string, string> = {
	"image/png": "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif": "gif",
};

/**
 * 把带回的草稿拆成输入区的正文与附件：去掉控制面追加的 `<oryn-attachments>` 块与写作块改稿块（`<oryn-draft-edits>`，
 * 改稿仍在服务端，不放回输入区），块里的附件按 id 对上线程附件后
 * 原样复用。发送时图片既上传为线程附件、又按条目顺序附一份内联副本，所以两边的图片个数相等时按顺序配回同一项；
 * 对不上（有的图片超了内联预算、只作为文件发送）时无法判断对应关系，图片只作为文件放回。没有线程附件的图片
 * （全局助手对话的图片只随 `images` 发送）作为内联图片放回。
 */
export function draftFromLeafSwitch(
	draft: LeafSwitchDraft,
	threadAttachments: readonly Attachment[],
): { text: string; items: RestoredAttachment[] } {
	const parsed = parseAttachmentBlock(draft.editorText ?? "");
	const { attachments } = parsed;
	const { text } = parseDraftEditsBlock(parsed.text);
	const images = draft.editorImages ?? [];
	const byId = new Map(threadAttachments.map((a) => [a.id, a]));
	const files = attachments.flatMap((m) => {
		const a = m.id ? byId.get(m.id) : undefined;
		return a ? [a] : [];
	});
	const imageFiles = files.filter((a) => a.mime.startsWith("image/")).length;
	const paired = imageFiles === images.length;
	let next = 0;
	const items: RestoredAttachment[] = files.map((a) => {
		const image =
			paired && a.mime.startsWith("image/") ? images[next++] : undefined;
		return {
			name: a.name,
			size: a.size,
			attachment: a,
			...(image ? { image } : {}),
		};
	});
	if (imageFiles === 0)
		images.forEach((image, i) => {
			items.push({
				name: `image-${i + 1}.${IMAGE_EXT[image.mimeType] ?? "img"}`,
				size: Math.floor((image.data.length * 3) / 4),
				image,
			});
		});
	return { text, items };
}

function setComposerText(threadId: string, text: string): void {
	const store = getThreadStore(threadId);
	store.getState().patchLocal({
		editorText: {
			text,
			prepend: false,
			nonce: (store.getState().editorText?.nonce ?? 0) + 1,
		},
	});
}

/**
 * 把 `switch_leaf` 带回的草稿放进输入区（替换正文，附件追加为一批）。没有草稿时什么也不做。
 * 返回这批附件的标记（撤销时据此去掉）；线程附件列表取不到时只放回正文与内联图片，并提示附件没能放回。
 */
export async function applyLeafSwitchDraft(
	threadId: string,
	draft: LeafSwitchDraft,
): Promise<string | null> {
	if (!draft.editorText && !draft.editorImages?.length) return null;
	const hasIds = parseAttachmentBlock(draft.editorText ?? "").attachments.some(
		(a) => a.id,
	);
	let threadAttachments: Attachment[] = [];
	if (hasIds)
		try {
			threadAttachments = await listThreadAttachments(threadId);
		} catch (e) {
			showToast({
				tone: "err",
				text: i18n.t("rewind.attachmentsFailed", {
					error: describeError(e, i18n.t),
				}),
			});
		}
	const { text, items } = draftFromLeafSwitch(draft, threadAttachments);
	setComposerText(threadId, text);
	const tag = `rewind-${++seq}`;
	if (items.length > 0)
		enqueueComposerRestore(threadId, { kind: "add", tag, items });
	return tag;
}

/**
 * 回退到 `entryId`：
 * - 用户消息 / 可编辑自定义消息：叶子移到该条目之前，原文与附件放回输入区（即使已是当前叶子也会移动）；
 * - 助手消息 / 工具调用等非用户消息：叶子落在该条目上，保留它及之前内容、裁掉之后内容，不改动输入区草稿；
 *   若它已经是当前叶子（无后续内容可裁），仅提示「已经在这个位置」而不发无效切换。
 * `switch_leaf` 失败（例如回合还在进行）时抛出，叶子不变。
 */
export async function rewindTo(
	threadId: string,
	entryId: string,
	opts?: { rewindsBefore?: boolean },
): Promise<"rewound" | "noop" | "cancelled"> {
	const prevLeaf = getThreadStore(threadId).getState().leafEntryId;
	const rewindsBefore = opts?.rewindsBefore ?? true;
	if (!rewindsBefore && prevLeaf === entryId) {
		showToast({ text: i18n.t("rewind.alreadyHere") });
		return "noop";
	}
	const prevText = getComposerDraft(threadId).text;
	const res = await realtime.cmd(threadId, { type: "switch_leaf", entryId });
	if (res.cancelled || res.aborted) return "cancelled";
	const hasDraft = Boolean(res.editorText || res.editorImages?.length);
	const tag = hasDraft ? await applyLeafSwitchDraft(threadId, res) : null;
	showToast({
		text: hasDraft ? i18n.t("rewind.done") : i18n.t("rewind.donePoint"),
		...(prevLeaf
			? {
					action: {
						label: i18n.t("rewind.undo"),
						run: () =>
							void undoRewind(
								threadId,
								prevLeaf,
								hasDraft ? prevText : null,
								tag,
							),
					},
				}
			: {}),
	});
	return "rewound";
}

/** 撤销一次回退：叶子回到 `leaf`（回退时那条路径留成了分支）；回退时改过输入区草稿的才还原草稿与附件。 */
async function undoRewind(
	threadId: string,
	leaf: string,
	text: string | null,
	tag: string | null,
): Promise<void> {
	try {
		const res = await realtime.cmd(threadId, {
			type: "switch_leaf",
			entryId: leaf,
		});
		if (res.cancelled || res.aborted) return;
	} catch (e) {
		showToast({ tone: "err", text: describeError(e, i18n.t) });
		return;
	}
	if (text !== null) setComposerText(threadId, text);
	if (tag) enqueueComposerRestore(threadId, { kind: "drop", tag });
	showToast({ text: i18n.t("rewind.undone") });
}
/** 消息菜单的「回到这里编辑」：失败时以提示条报错。 */
export function rewindWithToast(threadId: string, entryId: string): void {
	void rewindTo(threadId, entryId).catch((e: unknown) =>
		showToast({ tone: "err", text: describeError(e, i18n.t) }),
	);
}
