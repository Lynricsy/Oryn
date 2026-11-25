/**
 * 线程输入区：发送 / steer / 排队 / 中断按键（`lib/composer.ts`）、`/` 命令菜单、`!`/`!!` bash、文件附件。
 * 是输入坞玻璃托盘里的一节（透明、无边框）：输入框、工具行（附件、模式、模型、发送）、页脚行（模式状态；上下文、成本、速度读数）。
 * `coordinator`（协调对话，M9）：没有模式条；`!` 前缀按普通文本发送；命令菜单与斜杠命令按服务端白名单过滤。
 * 协调对话里只有全局助手对话（M13）有附件：文件传进助手文件库，以 `orynFiles` 携带；Space 聊天没有附件。
 * 线程的项目已绑定 GitHub 时（M14），键入 `#` 弹出该仓库的 issue / PR 候选（`RefMenu`），选中即作为 Markdown 附件加入；
 * `/` 命令菜单打开时（整段以 `/` 开头）与协调对话里不启用。
 * omp 推来下一条消息预测时（`composerPrediction`，规则见 `lib/composer-prediction.ts`）在草稿后叠一层灰色幽灵字：
 * Tab / → 或点一下插入（从不发送），有多个候选时插入后再按 Tab 轮换。
 * 输入命令参数时（`/btw `），光标后同一层灰字给出参数提示（`inlineArgHint`，同 omp TUI），只是提示，不可插入。
 */
import {
	memo,
	type ReactNode,
	useEffect,
	useId,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { chatModels, fetchModelCatalog } from "../../api/model-catalog-queries";
import { selectionPrompt } from "../../lib/artifacts/prompt";
import { composerKeyAction, parseComposerInput } from "../../lib/composer";
import { predictionView } from "../../lib/composer-prediction";
import { selectModel } from "../../lib/model-select";
import { composeQuoted } from "../../lib/quote";
import {
	carriesAttachments,
	coordinatorAllows,
	matchModelSelector,
	type SlashAction,
} from "../../lib/slash";
import type { OmpCommand, SessionState } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import {
	commandTimeout,
	describeSlashResult,
	isModeCommand,
	isThreadBusy,
	LONG_COMMAND_TIMEOUT_MS,
	promptFrame,
	pushLocalNotice,
	resolveSlashText,
	runModeCommand,
	type SendMode,
	slashPromptFrame,
} from "../../state/actions";
import {
	claimComposer,
	composerMounted,
	getComposerDraft,
	type InFlightDraft,
	isInFlightDraft,
	patchComposerDraft,
	takeComposerFiles,
	takeComposerRestores,
	useComposerDraft,
	useComposerPendingFiles,
	useComposerPendingRestores,
} from "../../state/composer-drafts";
import {
	agentQueryable,
	getThreadStore,
	useIndex,
	useThread,
} from "../../state/stores";
import { subagentFiles } from "../../state/subagent-entries";
import { uiStore } from "../../state/ui";
import { SelectionChip } from "../artifacts/SelectionChip";
import { openPalette } from "../palette/palette-state";
import { ModeStatus, ModeStrip } from "../thread/ModeBar";
import { useThreadProject } from "../thread/ThreadHeader";
import {
	addPendingPrompt,
	removePendingPrompts,
} from "../transcript/pending-prompt";
import { Icon } from "../ui/icons";
import { describeError, IconButton, Spinner } from "../ui/primitives";
import { AttachButton, AttachmentStrip, useAttachments } from "./attachments";
import { CommandMenu, type MenuEntry, useCommandMenu } from "./CommandMenu";
import { ModelChip, SessionReadout } from "./ComposerMeta";
import { FileDropOverlay, useFileDrop } from "./FileDrop";
import { RefMenu, useRefMenu } from "./RefMenu";
import { RoleTrack, useRoleShortcut } from "./RoleTrack";

/** memo：线程页因页头、面板、侧栏开合重渲染时输入区不跟着重算（props 都是原始值）。 */
export const Composer = memo(function Composer({
	threadId,
	disabled,
	coordinator = false,
	dropHandledByParent = false,
}: {
	threadId: string;
	disabled?: boolean;
	coordinator?: boolean;
	/** 外层容器（如线程页整张对话纸面）已接管文件拖拽投放与遮罩时为真，避免双层遮罩与重复加入。 */
	dropHandledByParent?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const busy = useThread(threadId, isThreadBusy);
	const commands = useThread(threadId, (s) => s.commands);
	const editorText = useThread(threadId, (s) => s.editorText);
	const prediction = useThread(threadId, (s) => s.composerPrediction);
	// 草稿与错误行存在组件外（按线程）：跨 compact 断点、切线程重新挂载时不丢
	const text = useComposerDraft(threadId, (d) => d.text);
	const error = useComposerDraft(threadId, (d) => d.error);
	const quote = useComposerDraft(threadId, (d) => d.quote);
	const insert = useComposerDraft(threadId, (d) => d.insert);
	const inFlight = useComposerDraft(threadId, (d) => d.inFlight);
	const setText = (next: string) =>
		patchComposerDraft(threadId, { text: next });
	const setError = (next: unknown) =>
		patchComposerDraft(threadId, { error: next });
	const clearQuote = () => patchComposerDraft(threadId, { quote: null });
	const artifactSelection = useComposerDraft(
		threadId,
		(d) => d.artifactSelection,
	);
	const clearSelection = () =>
		patchComposerDraft(threadId, { artifactSelection: null });
	const [bashRunning, setBashRunning] = useState(false);
	const [active, setActive] = useState(0);
	const [menuDismissed, setMenuDismissed] = useState(false);
	/** 上一条斜杠命令没有带走附件（附件留在输入区）时，那条命令的名字。 */
	const [keptFor, setKeptFor] = useState<string | null>(null);
	const escAt = useRef<number | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);
	const rootRef = useRef<HTMLDivElement>(null);
	useRoleShortcut(threadId);
	const assistant = useIndex((s) => s.threads[threadId]?.kind) === "assistant";
	const canAttach = !coordinator || assistant;
	const att = useAttachments({ threadId, library: assistant });
	const fileDrop = useFileDrop({
		enabled: canAttach && !disabled && !dropHandledByParent,
		onFiles: (files) => void att.addFiles(files),
	});
	// 右键菜单「引用到输入框」送来的图片：挂上后加入附件列表
	const pendingFiles = useComposerPendingFiles(threadId);
	const addFiles = att.addFiles;
	useEffect(() => {
		if (pendingFiles.length === 0) return;
		const files = takeComposerFiles(threadId);
		if (canAttach) void addFiles(files);
	}, [pendingFiles, threadId, canAttach, addFiles]);
	// 回退（rewind）带回 / 撤销时去掉的附件
	const pendingRestores = useComposerPendingRestores(threadId);
	const applyRestore = att.applyRestore;
	useEffect(() => {
		if (pendingRestores.length === 0) return;
		for (const op of takeComposerRestores(threadId)) applyRestore(op);
	}, [pendingRestores, threadId, applyRestore]);
	const menu = useCommandMenu(threadId, text, commands, coordinator);
	const menuOpen = menu !== null && !menuDismissed;
	const entries = menu?.entries ?? [];
	const project = useThreadProject(
		useIndex((s) => s.threads[threadId]?.projectId),
	);
	const [caret, setCaret] = useState(0);
	const refMenu = useRefMenu({
		projectId:
			!coordinator && project?.github?.bound && !text.startsWith("/")
				? project.id
				: undefined,
		text,
		caret,
		setText: (next, c) => {
			setText(next);
			setCaret(c);
			requestAnimationFrame(() => ref.current?.setSelectionRange(c, c));
		},
		onAttached: (a) => att.addUploaded([a]),
	});
	const [composing, setComposing] = useState(false);
	/** 最近一次从预测插入的文本（轮换用）；之后的任何其他编辑都清掉它。 */
	const [inserted, setInserted] = useState<string | null>(null);
	const ghostRef = useRef<HTMLDivElement>(null);
	const ghostHintId = useId();
	const predicted = predictionView({
		choices: prediction,
		draft: text,
		caret,
		// 菜单、输入法组字、agent 工作中、发送途中都不显示
		enabled:
			!disabled &&
			!busy &&
			!inFlight &&
			!composing &&
			!menuOpen &&
			!refMenu.open,
		inserted,
	});
	const ghost = predicted.ghost;
	/** 命令参数灰字：单行、光标在末尾、非输入法组字时显示在草稿后；不可点、不随 Tab 插入。预测灰字优先。 */
	const argHint =
		!ghost &&
		!disabled &&
		!composing &&
		caret >= text.length &&
		!text.includes("\n")
			? menu?.inlineHint
			: undefined;

	/** 接受预测：草稿换成完整候选、光标放到末尾；从不发送。 */
	const acceptPrediction = (choice: string) => {
		// 同步提交新草稿，再同步放光标：同一次按键里 React 的 onSelect 读到的已是新值的末尾，
		// 不会把 caret 改回旧位置（否则轮换序号与下一次 Tab 会失效）
		flushSync(() => {
			setText(choice);
			setInserted(choice);
			setCaret(choice.length);
			setMenuDismissed(false);
		});
		const el = ref.current;
		if (!el) return;
		el.focus({ preventScroll: true });
		el.setSelectionRange(choice.length, choice.length);
	};

	// omp 的 set_editor_text / 会话树跳转带回的草稿；同一份只并入一次（重新挂载时 store 里仍是它）
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只由新的 editorText 触发
	useEffect(() => {
		if (!editorText || getComposerDraft(threadId).editorText === editorText)
			return;
		const prev = getComposerDraft(threadId).text;
		patchComposerDraft(threadId, {
			editorText,
			text: editorText.prepend ? `${editorText.text}${prev}` : editorText.text,
		});
		ref.current?.focus();
	}, [editorText]);

	// 新的引用回复：光标放到回复处（正文末尾）。重新挂载时 store 里仍是同一份引用，不重复抢焦点
	const quoteSeen = useRef(quote);
	useEffect(() => {
		if (!quote || quote === quoteSeen.current) return;
		quoteSeen.current = quote;
		const el = ref.current;
		if (!el) return;
		el.focus({ preventScroll: true });
		el.setSelectionRange(el.value.length, el.value.length);
	}, [quote]);

	// 「引用到输入框」：接到正文末尾（与原文空一行），光标放到最后
	useEffect(() => {
		if (!insert) return;
		const prev = getComposerDraft(threadId).text;
		const next = prev.trim()
			? `${prev.replace(/\s+$/, "")}\n\n${insert.text}`
			: insert.text;
		patchComposerDraft(threadId, { text: next, insert: null });
		requestAnimationFrame(() => {
			const el = ref.current;
			if (!el) return;
			el.focus({ preventScroll: true });
			el.setSelectionRange(el.value.length, el.value.length);
		});
	}, [insert, threadId]);

	// 重新挂载后尽力恢复卸载时的光标与焦点；卸载时（DOM 还在）记下它们
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const sel = getComposerDraft(threadId).selection;
		if (sel) {
			const idle =
				document.activeElement === null ||
				document.activeElement === document.body;
			if (sel.focused && idle) el.focus();
			el.setSelectionRange(sel.start, sel.end);
		}
		return () => {
			patchComposerDraft(threadId, {
				selection: {
					start: el.selectionStart,
					end: el.selectionEnd,
					focused: document.activeElement === el,
				},
			});
		};
	}, [threadId]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: 文本或幽灵字变化后按内容高度自适应
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		el.style.height = "auto";
		// 幽灵字可能比已输入的文字多占几行：取两者较高者
		const content = Math.max(
			el.scrollHeight,
			ghostRef.current?.scrollHeight ?? 0,
		);
		el.style.height = `${Math.min(content, Math.round(window.innerHeight * 0.4))}px`;
	}, [text, ghost?.suffix, argHint]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: 候选数变化时重置高亮项
	useEffect(() => {
		setActive(0);
	}, [entries.length]);

	// 提交时的草稿快照：应答回来时只有草稿仍是那一份才清空（等待期间的新输入保留）。
	// 文字以草稿 store 为准：应答可能在输入区重新挂载之后才到
	const draftRef = useRef({
		text,
		items: att.items,
		quote,
		artifactSelection,
	});
	draftRef.current = { text, items: att.items, quote, artifactSelection };
	// 异步命令应答落地时用户可能已切到别的线程（切线程会卸载本输入区）：对话框是全局单值、由当前页面消费，
	// 旧线程的应答不得在新线程打开 / 覆盖对话框。按线程登记而非按实例：跨断点重新挂载仍算留在本线程
	useEffect(() => claimComposer(threadId), [threadId]);

	/**
	 * 发出的正文：用户原文后接选中的元素（`selectionPrompt`），引用回复的引用块放在最前。
	 * 原文为空（只发附件）时不留开头的空行。
	 */
	const outgoing = (body: string, sent: typeof draftRef.current) => {
		const sel = sent.artifactSelection;
		const withSel = sel
			? body
				? selectionPrompt(body, sel, t)
				: selectionPrompt("", sel, t).trimStart()
			: body;
		return sent.quote ? composeQuoted(sent.quote.text, withSel) : withSel;
	};

	/**
	 * 清空输入区；给了 `sent`（发出的那份草稿）时一并清掉随它发出的引用与选中的元素——等待期间换了一段引用
	 * 或另选了元素的，新的留着。不给时两者都留着（bash、斜杠命令不带走它们）。
	 */
	const reset = (sent?: typeof draftRef.current) => {
		setText("");
		att.clear();
		setMenuDismissed(false);
		if (!sent) return;
		const now = getComposerDraft(threadId);
		patchComposerDraft(threadId, {
			quote: now.quote === sent.quote ? null : now.quote,
			artifactSelection:
				now.artifactSelection === sent.artifactSelection
					? null
					: now.artifactSelection,
		});
	};

	const resetIfUnchanged = (
		sent: typeof draftRef.current,
		withRefs: boolean,
	) => {
		const now = getComposerDraft(threadId);
		if (now.text === sent.text && draftRef.current.items === sent.items)
			reset(withRefs ? sent : undefined);
	};

	/** 发送失败：把发出的那份草稿放回输入区（等待期间又写了新草稿的接在后面），引用、选中的元素、附件空着才放回。 */
	const restoreDraft = (sent: typeof draftRef.current) => {
		const now = getComposerDraft(threadId);
		patchComposerDraft(threadId, {
			text: now.text.trim() ? `${sent.text}\n\n${now.text}` : sent.text,
			quote: now.quote ?? sent.quote,
			artifactSelection: now.artifactSelection ?? sent.artifactSelection,
		});
		att.restore(sent.items);
	};

	/** 命令没有带走附件：只清掉命令文本，附件留在输入区。 */
	const clearTextIfUnchanged = (sent: typeof draftRef.current) => {
		if (getComposerDraft(threadId).text !== sent.text) return;
		setText("");
		setMenuDismissed(false);
	};

	const notifyResult = (frame: OmpCommand, data: unknown) => {
		const r = describeSlashResult(frame, data);
		if (r)
			pushLocalNotice(
				threadId,
				r.level,
				t(r.key, {
					...r.params,
					why: r.whyKey ? t(r.whyKey) : undefined,
				}),
			);
	};

	const runSlash = async (
		action: SlashAction,
		mode: SendMode,
		name: string,
	) => {
		if (coordinator && !coordinatorAllows(action))
			throw new Error(t("composer.coordinatorUnavailable", { name }));
		const ui = uiStore.getState();
		switch (action.kind) {
			case "rpc": {
				const data = isModeCommand(action.frame)
					? await runModeCommand(threadId, action.frame)
					: await realtime.cmd(
							threadId,
							action.frame,
							commandTimeout(action.frame),
						);
				notifyResult(action.frame, data);
				return;
			}
			case "mode_prompt":
				// 与 TUI 一致：先进入模式，再把 prompt（连同附件）作为该模式的第一条消息发出
				if (action.frame) await runModeCommand(threadId, action.frame);
				await realtime.cmd(
					threadId,
					slashPromptFrame(
						mode,
						action.prompt,
						att.contents,
						att.attachmentIds,
						att.fileIds,
					),
				);
				return;
			case "queue":
				await realtime.cmd(
					threadId,
					promptFrame(
						"follow_up",
						action.message,
						att.contents,
						att.attachmentIds,
						att.fileIds,
					),
				);
				return;
			case "select_model": {
				// 中央模型目录里的对话模型（不问线程的 agent）；切换命令照旧发给线程
				const models = chatModels(await fetchModelCatalog());
				const model = matchModelSelector(models, action.selector);
				if (!model) {
					if (composerMounted(threadId))
						ui.open({ kind: "model", query: action.selector });
					return;
				}
				await selectModel(threadId, model);
				const state = (await realtime.cmd(threadId, {
					type: "get_state",
				})) as SessionState;
				getThreadStore(threadId)
					.getState()
					.apply([{ type: "state", state }]);
				pushLocalNotice(
					threadId,
					"info",
					t("slash.result.model", { model: model.name || model.id }),
				);
				return;
			}
			case "open_tree":
				ui.open({ kind: "tree" });
				return;
			case "open_rewind":
				ui.open({ kind: "rewind" });
				return;
			case "open_fork":
				ui.open({ kind: "fork" });
				return;
			case "open_model":
				ui.open({ kind: "model" });
				return;
			case "open_goal":
				ui.open({ kind: "goal" });
				return;
			case "open_btw":
				ui.open({ kind: "btw" });
				return;
			case "ask_btw":
				// Core 先写入运行中的记录再应答；成功后打开对话框看流式回答（已有运行中的会被拒，错误留在输入区）
				await realtime.cmd(threadId, {
					type: "btw",
					question: action.question,
				});
				if (composerMounted(threadId)) ui.open({ kind: "btw" });
				return;
			case "open_changes":
				ui.setPanelTab("changes");
				ui.setPanel(true);
				return;
			case "open_subagents": {
				ui.setPanelTab("info");
				ui.setPanel(true);
				if (ui.collapsedCards.subagents) ui.toggleCard("subagents");
				// 子 agent 卡没有内容时不显示：说明一下，免得看起来没反应
				const s = getThreadStore(threadId).getState();
				if (
					Object.keys(s.subagents).length === 0 &&
					subagentFiles(s.entries).length === 0
				)
					pushLocalNotice(threadId, "info", t("slash.result.noSubagents"));
				return;
			}
			case "review_plan":
				if (!getThreadStore(threadId).getState().planProposal)
					throw new Error(t("slash.noPlan"));
				ui.reviewPlan();
				return;
			case "report":
				ui.open({ kind: "report", report: action.report });
				return;
			case "new_thread":
				navigate("/");
				return;
			case "thread_search":
				// 线程搜索在命令面板里（侧栏的搜索入口同样打开它）
				openPalette();
				return;
			case "needs_args":
				throw new Error(
					t("slash.needsArgs", { name: action.name, usage: action.usage }),
				);
			case "bad_args":
				throw new Error(
					t("slash.badArgs", {
						name: action.name,
						arg: action.arg,
						usage: action.usage,
					}),
				);
			case "unavailable":
				throw new Error(
					t(`slash.unavailable.${action.reason}`, { name: action.name }),
				);
			case "prompt":
				await realtime.cmd(
					threadId,
					action.attach
						? slashPromptFrame(
								mode,
								action.text,
								att.contents,
								att.attachmentIds,
								att.fileIds,
							)
						: slashPromptFrame(mode, action.text),
					LONG_COMMAND_TIMEOUT_MS,
				);
				return;
		}
	};

	const submit = async (mode: SendMode) => {
		const sent = draftRef.current;
		// 同一份草稿已在发送中（含重新挂载前由旧输入区发出、尚未应答的）：忽略重复提交
		const keys = sent.items.map((i) => i.key);
		// 选中的元素按内容计入（发出的正文里的边界是随机的，不能拿来比）
		const markerText = [
			sent.quote ? composeQuoted(sent.quote.text, sent.text) : sent.text,
			...(sent.artifactSelection
				? [JSON.stringify(sent.artifactSelection)]
				: []),
		].join("\n");
		if (isInFlightDraft(getComposerDraft(threadId).inFlight, markerText, keys))
			return;
		const parsed = parseComposerInput(sent.text);
		// 协调对话没有工作区：`!` 前缀不是 bash，按原文发送
		const asText =
			coordinator && parsed.kind === "bash"
				? { kind: "text" as const, text: sent.text.trim() }
				: parsed;
		setError(null);
		setKeptFor(null);
		if (att.uploading && parsed.kind !== "bash") {
			setError(new Error(t("attachments.waitUpload")));
			return;
		}
		const marker: InFlightDraft = { text: markerText, keys };
		patchComposerDraft(threadId, { inFlight: marker });
		try {
			if (parsed.kind === "empty") {
				if (!att.hasFiles) return;
				await realtime.cmd(
					threadId,
					promptFrame(
						mode,
						outgoing("", sent),
						att.contents,
						att.attachmentIds,
						att.fileIds,
					),
				);
				resetIfUnchanged(sent, true);
				return;
			}
			if (asText.kind === "bash") {
				reset();
				setBashRunning(true);
				try {
					await realtime.cmd(
						threadId,
						asText.excludeFromContext
							? {
									type: "bash",
									command: asText.command,
									excludeFromContext: true,
								}
							: { type: "bash", command: asText.command },
						3_600_000,
					);
				} finally {
					setBashRunning(false);
				}
				return;
			}
			if (asText.kind === "slash") {
				const action = resolveSlashText(threadId, asText.text);
				if (action) {
					const name = asText.text.trim().split(/\s/)[0] ?? asText.text;
					await runSlash(action, mode, name);
					if (sent.items.length > 0 && !carriesAttachments(action)) {
						clearTextIfUnchanged(sent);
						setKeptFor(name);
					} else resetIfUnchanged(sent, false);
					return;
				}
			}
			const body = asText.kind === "text" ? asText.text : sent.text.trim();
			const frame = promptFrame(
				mode,
				outgoing(body, sent),
				att.contents,
				att.attachmentIds,
				att.fileIds,
			);
			// agent 未就绪（orb 冻结 / 唤醒中、agent 启动中）时命令在服务端排队，agent 接下才应答：消息先以「发送中」气泡
			// 进转录、输入区立即清空，对应的 user 条目到达时气泡由转录清除。就绪的 agent 几毫秒内应答，不闪气泡，
			// 仍是应答后清空。未识别的斜杠命令照原文交给 agent，可能不产生 user 条目，不走气泡
			const ts = getThreadStore(threadId).getState();
			const bubble =
				asText.kind === "text" && !agentQueryable(ts)
					? addPendingPrompt(threadId, body, ts.lastSeq)
					: undefined;
			if (bubble === undefined) {
				await realtime.cmd(threadId, frame);
				resetIfUnchanged(sent, true);
				return;
			}
			reset(sent);
			// 草稿已离开输入区：在途标记不再拦重复提交（再输入同样的文字是新的一条）
			if (getComposerDraft(threadId).inFlight === marker)
				patchComposerDraft(threadId, { inFlight: null });
			try {
				await realtime.cmd(threadId, frame);
			} catch (e) {
				removePendingPrompts(threadId, [bubble]);
				restoreDraft(sent);
				throw e;
			}
		} catch (e) {
			setError(e);
		} finally {
			if (getComposerDraft(threadId).inFlight === marker)
				patchComposerDraft(threadId, { inFlight: null });
		}
	};

	const pick = (entry: MenuEntry) => {
		setText(entry.insert);
		if (entry.submit) {
			setText(entry.insert.trim());
			setMenuDismissed(true);
		}
		ref.current?.focus();
	};

	const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
		if (refMenu.onKeyDown(e)) return;
		if (menuOpen && entries.length > 0) {
			if (e.key === "ArrowDown") {
				e.preventDefault();
				setActive((a) => (a + 1) % entries.length);
				return;
			}
			if (e.key === "ArrowUp") {
				e.preventDefault();
				setActive((a) => (a - 1 + entries.length) % entries.length);
				return;
			}
			if (
				e.key === "Tab" ||
				(e.key === "Enter" &&
					!e.shiftKey &&
					entries[active] &&
					text.trim() !== entries[active]?.insert.trim())
			) {
				e.preventDefault();
				const entry = entries[active];
				if (entry) pick(entry);
				return;
			}
		}
		if (menuOpen && e.key === "Escape") {
			e.preventDefault();
			setMenuDismissed(true);
			return;
		}
		// 空输入框里在开头按退格：先移除引用，其次是选中的元素（与删掉一个字符同一个手势）
		if (
			(quote || artifactSelection) &&
			e.key === "Backspace" &&
			text === "" &&
			!e.nativeEvent.isComposing
		) {
			e.preventDefault();
			if (quote) clearQuote();
			else clearSelection();
			return;
		}
		// 下一条消息预测：在菜单之后处理（菜单先吃掉 Tab），只认无修饰键的 Tab / →，从不发送。
		// 按键时再以输入框为准确认光标折叠在末尾（caret 状态在 keyup 后才更新，且不含选区）。
		const el = e.currentTarget;
		if (
			!e.nativeEvent.isComposing &&
			!e.shiftKey &&
			!e.altKey &&
			!e.ctrlKey &&
			!e.metaKey &&
			el.selectionStart === el.selectionEnd &&
			el.selectionEnd >= el.value.length
		) {
			if (e.key === "Tab" && predicted.cycle) {
				e.preventDefault();
				acceptPrediction(predicted.cycle.next);
				return;
			}
			if ((e.key === "Tab" || e.key === "ArrowRight") && ghost) {
				e.preventDefault();
				acceptPrediction(ghost.choice);
				return;
			}
		}
		const decision = composerKeyAction(
			e.nativeEvent,
			busy,
			escAt.current,
			Date.now(),
			text.trim() === "",
		);
		escAt.current = decision.escAt;
		switch (decision.action) {
			case "abort":
				e.preventDefault();
				void realtime.cmd(threadId, { type: "abort" }).catch(() => undefined);
				return;
			case "rewind":
				// 离散输入的更新同步提交、effect 也同步执行：对话框在这次 keydown 冒泡到 window 之前就挂上了
				// 自己的 Esc 监听，不拦住冒泡会被同一个 Esc 立刻关掉
				e.preventDefault();
				e.stopPropagation();
				uiStore.getState().open({ kind: "rewind" });
				return;
			case "prompt":
			case "steer":
			case "follow_up":
			case "abort_and_prompt":
				e.preventDefault();
				void submit(decision.action);
				return;
			default:
				return;
		}
	};

	// 上传未完成时不能发送（否则附件会被悄悄漏掉）；出错的文件不随消息发送
	const canSend =
		!disabled && !att.uploading && (text.trim() !== "" || att.hasFiles);
	return (
		<div
			ref={rootRef}
			className="relative"
			{...(dropHandledByParent ? {} : fileDrop.bind)}
		>
			{!dropHandledByParent && (
				<FileDropOverlay active={fileDrop.dragging} compact />
			)}
			<RoleTrack threadId={threadId} anchorRef={rootRef} />
			{menuOpen && (
				<CommandMenu
					entries={entries}
					usage={menu.usage}
					active={active}
					onPick={pick}
				/>
			)}
			{refMenu.open && (
				<RefMenu
					entries={refMenu.entries}
					active={refMenu.active}
					pending={refMenu.pending}
					onPick={refMenu.pick}
				/>
			)}
			{quote && (
				<QuoteStrip
					text={quote.text}
					onRemove={() => {
						clearQuote();
						ref.current?.focus({ preventScroll: true });
					}}
				/>
			)}
			{artifactSelection && (
				<SelectionChip
					selection={artifactSelection}
					onRemove={() => {
						clearSelection();
						ref.current?.focus({ preventScroll: true });
					}}
				/>
			)}
			{canAttach && <AttachmentStrip items={att.items} onRemove={att.remove} />}
			{/* 幽灵字叠在输入框上方，排版与输入框一致；只有灰色部分接收点击（触屏没有 Tab，点一下插入） */}
			<div className="relative">
				<textarea
					ref={ref}
					rows={1}
					value={text}
					disabled={disabled}
					aria-label={t("composer.label")}
					aria-describedby={ghost ? ghostHintId : undefined}
					placeholder={
						ghost && text === ""
							? ""
							: busy
								? t("composer.placeholderBusy")
								: t("composer.placeholder")
					}
					// 与首页托盘同一套内边距：文字离边 20px（32px 大圆角下不贴弧线），工具行图标中心、页脚读数都对齐这条线
					className="block max-h-[40vh] min-h-[52px] w-full resize-none bg-transparent px-5 pt-4 pb-1.5 text-[14px] leading-5 outline-none placeholder:text-faint focus-visible:outline-none"
					onChange={(e) => {
						setText(e.target.value);
						setCaret(e.target.selectionStart);
						setMenuDismissed(false);
						setInserted(null);
					}}
					onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
					onKeyUp={(e) => setCaret(e.currentTarget.selectionStart)}
					onKeyDown={onKeyDown}
					onCompositionStart={() => setComposing(true)}
					onCompositionEnd={() => setComposing(false)}
					onScroll={(e) => {
						if (ghostRef.current)
							ghostRef.current.scrollTop = e.currentTarget.scrollTop;
					}}
					onPaste={(e) => {
						const files = canAttach ? [...e.clipboardData.files] : [];
						if (files.length > 0) {
							e.preventDefault();
							void att.addFiles(files);
						}
					}}
				/>
				{(ghost || argHint) && (
					<div
						ref={ghostRef}
						aria-hidden
						data-testid={ghost ? "composer-ghost" : "composer-arg-hint"}
						className="pointer-events-none absolute inset-0 overflow-hidden whitespace-pre-wrap break-words px-5 pt-4 pb-1.5 text-[14px] leading-5"
					>
						<span className="invisible">{text}</span>
						{ghost ? (
							// biome-ignore lint/a11y/useKeyWithClickEvents: 键盘用 Tab / → 接受，这里只给指针与触屏
							// biome-ignore lint/a11y/noStaticElementInteractions: 同上，幽灵字本身对辅助技术隐藏，说明见 aria-describedby
							<span
								className="pointer-events-auto cursor-text text-faint"
								// 不抢输入框焦点
								onMouseDown={(e) => e.preventDefault()}
								onClick={() => acceptPrediction(ghost.choice)}
							>
								{ghost.suffix}
							</span>
						) : (
							<span className="text-faint">{argHint}</span>
						)}
					</div>
				)}
				{ghost && (
					<span id={ghostHintId} className="sr-only">
						{t("composer.prediction", { text: ghost.choice })}
					</span>
				)}
			</div>
			{/*
			 * 工具行：附件 + 分隔、可换行的选项（模式、模型）、停止 + 发送。停止与发送始终在第一行最右；
			 * ModeStrip 是 flex-1（基准 0）先让出空间、自己收进「更多」，仍放不下的选项才在容器里折到下一行。
			 */}
			<div className="flex items-start gap-0.5 px-3 pb-2">
				{canAttach && (
					<div className="flex h-8 shrink-0 items-center">
						<AttachButton onFiles={(files) => void att.addFiles(files)} />
						<span
							aria-hidden
							className="mx-1.5 h-[18px] w-px shrink-0 bg-[var(--glass-line)]"
						/>
					</div>
				)}
				<div
					className={`flex min-h-8 min-w-0 flex-1 flex-wrap items-center gap-0.5 ${canAttach ? "" : "pl-2"}`}
				>
					{!coordinator && <ModeStrip threadId={threadId} />}
					{bashRunning && (
						<button
							type="button"
							className="inline-flex h-[30px] shrink-0 items-center gap-1.5 rounded-md px-2 text-muted text-xs hover:bg-hover"
							onClick={() =>
								void realtime
									.cmd(threadId, { type: "abort_bash" })
									.catch(() => undefined)
							}
						>
							<Spinner size={11} />
							{t("composer.abortBash")}
						</button>
					)}
					{/* 手机上模型名多截一点：agent 工作时多出停止钮，仍能一行放下，输入坞不随每轮工作忽高忽低 */}
					<span className="contents max-sm:[&>button]:max-w-32">
						<ModelChip threadId={threadId} />
					</span>
				</div>
				{busy && (
					<IconButton
						icon="stop"
						label={t("composer.abort")}
						onClick={() =>
							void realtime
								.cmd(threadId, { type: "abort" })
								.catch(() => undefined)
						}
					/>
				)}
				<button
					type="button"
					aria-label={busy ? t("composer.steer") : t("composer.send")}
					disabled={!canSend}
					onClick={() => void submit(busy ? "steer" : "prompt")}
					// 空态只降不透明度，仍是强调色实心方块（D 稿）
					className="ml-1.5 inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent text-accent-fg shadow-xs transition-[scale,filter,opacity] duration-150 ease-out hover:brightness-110 active:scale-95 disabled:pointer-events-none disabled:opacity-60 disabled:shadow-none"
				>
					<Icon name="send" size={16} weight="bold" />
				</button>
			</div>
			{(error || att.error || refMenu.error != null) && (
				<p role="alert" className="px-5 pb-2 text-err text-xs">
					{error
						? describeError(error, t)
						: att.error || describeError(refMenu.error, t)}
				</p>
			)}
			{keptFor && att.items.length > 0 && !error && (
				<p
					role="status"
					className="flex items-center gap-1.5 px-5 pb-2 text-muted text-xs"
				>
					<Icon name="info" size={13} aria-hidden className="shrink-0" />
					{t("slash.attachmentsKept", { name: keptFor })}
				</p>
			)}
			<div className="flex min-h-8 items-center gap-3.5 border-[var(--glass-line)] border-t px-5 py-[7px] text-2xs text-faint leading-4">
				{!coordinator && <ModeStatus threadId={threadId} />}
				<span className="flex-1" />
				{predicted.cycle && (
					<span
						role="status"
						className="shrink-0 tabular-nums"
						title={t("composer.predictionCycle")}
					>
						<span aria-hidden>⇥ </span>
						{predicted.cycle.index + 1}/{predicted.cycle.total}
						<span className="sr-only"> {t("composer.predictionCycle")}</span>
					</span>
				)}
				<SessionReadout threadId={threadId} />
			</div>
		</div>
	);
});

/** 输入区上方的引用条：引用原文前两行 + 移除钮；发出时拼成引用块放在正文前面。 */
function QuoteStrip({
	text,
	onRemove,
}: {
	text: string;
	onRemove: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="px-3 pt-3" data-testid="composer-quote">
			<div className="flex items-start gap-2 rounded-lg border border-border bg-panel py-1.5 pr-1.5 pl-3 shadow-xs">
				<Icon
					name="quote"
					size={14}
					aria-hidden
					className="mt-[3px] shrink-0 text-faint"
				/>
				<p
					className="line-clamp-2 min-w-0 flex-1 whitespace-pre-wrap break-words text-muted text-xs leading-5"
					title={text}
				>
					<span className="sr-only">{t("composer.quoting")}</span>
					{text}
				</p>
				<button
					type="button"
					aria-label={t("composer.removeQuote")}
					title={t("composer.removeQuote")}
					className="inline-grid h-6 w-6 shrink-0 place-items-center rounded-md text-faint transition-colors hover:bg-hover hover:text-fg"
					onClick={onRemove}
				>
					<Icon name="close" size={12} />
				</button>
			</div>
		</div>
	);
}
