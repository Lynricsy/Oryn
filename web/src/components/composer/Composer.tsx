/**
 * 线程输入区：发送 / steer / 排队 / 中断按键（`lib/composer.ts`）、`/` 命令菜单、`!`/`!!` bash、文件附件。
 * 是输入坞玻璃托盘里的一节（透明、无边框）：输入框、工具行（附件、模式、模型、上下文、发送）、页脚行（快捷键、模式状态、会话读数）。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { composerKeyAction, parseComposerInput } from "../../lib/composer";
import type { SlashAction } from "../../lib/slash";
import { realtime } from "../../realtime/socket";
import {
	isModeCommand,
	isThreadBusy,
	promptFrame,
	resolveSlashText,
	runModeCommand,
	type SendMode,
} from "../../state/actions";
import { useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { ModeStatus, ModeStrip } from "../thread/ModeBar";
import { Icon } from "../ui/icons";
import { describeError, IconButton, Kbd, Spinner } from "../ui/primitives";
import { AttachmentStrip, useAttachments } from "./attachments";
import { CommandMenu, type MenuEntry, useCommandMenu } from "./CommandMenu";
import { ContextRing, ModelChip, SessionReadout } from "./ComposerMeta";
import { RoleTrack, useRoleShortcut } from "./RoleTrack";

export function Composer({
	threadId,
	disabled,
}: {
	threadId: string;
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const busy = useThread(threadId, isThreadBusy);
	const commands = useThread(threadId, (s) => s.commands);
	const editorText = useThread(threadId, (s) => s.editorText);
	const [text, setText] = useState("");
	const [error, setError] = useState<unknown>(null);
	const [bashRunning, setBashRunning] = useState(false);
	const [active, setActive] = useState(0);
	const [menuDismissed, setMenuDismissed] = useState(false);
	const escAt = useRef<number | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);
	const rootRef = useRef<HTMLDivElement>(null);
	useRoleShortcut(threadId);
	const att = useAttachments({ threadId });
	const menu = useCommandMenu(threadId, text, commands);
	const menuOpen = menu !== null && !menuDismissed;

	// omp 的 set_editor_text / 会话树跳转带回的草稿
	useEffect(() => {
		if (!editorText) return;
		setText((prev) =>
			editorText.prepend ? `${editorText.text}${prev}` : editorText.text,
		);
		ref.current?.focus();
	}, [editorText]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: 文本变化后按内容高度自适应
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		el.style.height = "auto";
		el.style.height = `${Math.min(el.scrollHeight, Math.round(window.innerHeight * 0.4))}px`;
	}, [text]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: 候选数变化时重置高亮项
	useEffect(() => {
		setActive(0);
	}, [menu?.length]);

	// 提交时的草稿快照：应答回来时只有草稿仍是那一份才清空（等待期间的新输入保留）
	const draftRef = useRef({ text, items: att.items });
	draftRef.current = { text, items: att.items };
	const inFlight = useRef<typeof draftRef.current | null>(null);

	const reset = () => {
		setText("");
		att.clear();
		setMenuDismissed(false);
	};

	const resetIfUnchanged = (sent: typeof draftRef.current) => {
		const cur = draftRef.current;
		if (cur.text === sent.text && cur.items === sent.items) reset();
	};

	const runSlash = async (action: SlashAction, mode: SendMode) => {
		switch (action.kind) {
			case "rpc":
				if (isModeCommand(action.frame))
					await runModeCommand(threadId, action.frame);
				else await realtime.cmd(threadId, action.frame);
				return;
			case "open_tree":
				uiStore.getState().open({ kind: "tree" });
				return;
			case "open_fork":
				uiStore.getState().open({ kind: "fork" });
				return;
			case "open_model":
				uiStore.getState().open({ kind: "model" });
				return;
			case "open_goal":
				uiStore.getState().open({ kind: "goal" });
				return;
			case "open_btw":
				uiStore.getState().open({ kind: "btw" });
				return;
			case "report":
				uiStore.getState().open({ kind: "report", report: action.report });
				return;
			case "new_thread":
				navigate("/");
				return;
			case "thread_search":
				uiStore.getState().focusSearch();
				return;
			case "needs_args":
				throw new Error(t("composer.needsArgs", { name: action.name }));
			case "unavailable":
				throw new Error(t("composer.unavailable", { name: action.name }));
			case "prompt":
				await realtime.cmd(threadId, promptFrame(mode, action.text, []));
				return;
		}
	};

	const submit = async (mode: SendMode) => {
		const sent = draftRef.current;
		// 同一份草稿已在发送中：忽略重复提交
		const pending = inFlight.current;
		if (pending && pending.text === sent.text && pending.items === sent.items)
			return;
		const parsed = parseComposerInput(sent.text);
		setError(null);
		if (att.uploading && parsed.kind !== "bash") {
			setError(new Error(t("attachments.waitUpload")));
			return;
		}
		inFlight.current = sent;
		try {
			if (parsed.kind === "empty") {
				if (att.attachmentIds.length === 0) return;
				await realtime.cmd(
					threadId,
					promptFrame(mode, "", att.contents, att.attachmentIds),
				);
				resetIfUnchanged(sent);
				return;
			}
			if (parsed.kind === "bash") {
				reset();
				setBashRunning(true);
				try {
					await realtime.cmd(
						threadId,
						parsed.excludeFromContext
							? {
									type: "bash",
									command: parsed.command,
									excludeFromContext: true,
								}
							: { type: "bash", command: parsed.command },
						3_600_000,
					);
				} finally {
					setBashRunning(false);
				}
				return;
			}
			if (parsed.kind === "slash") {
				const action = resolveSlashText(threadId, parsed.text);
				if (action) {
					await runSlash(action, mode);
					resetIfUnchanged(sent);
					return;
				}
			}
			await realtime.cmd(
				threadId,
				promptFrame(
					mode,
					parsed.kind === "text" ? parsed.text : sent.text.trim(),
					att.contents,
					att.attachmentIds,
				),
			);
			resetIfUnchanged(sent);
		} catch (e) {
			setError(e);
		} finally {
			if (inFlight.current === sent) inFlight.current = null;
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
		if (menuOpen && menu.length > 0) {
			if (e.key === "ArrowDown") {
				e.preventDefault();
				setActive((a) => (a + 1) % menu.length);
				return;
			}
			if (e.key === "ArrowUp") {
				e.preventDefault();
				setActive((a) => (a - 1 + menu.length) % menu.length);
				return;
			}
			if (
				e.key === "Tab" ||
				(e.key === "Enter" &&
					!e.shiftKey &&
					menu[active] &&
					text.trim() !== menu[active]?.insert.trim())
			) {
				e.preventDefault();
				const entry = menu[active];
				if (entry) pick(entry);
				return;
			}
		}
		if (menuOpen && e.key === "Escape") {
			e.preventDefault();
			setMenuDismissed(true);
			return;
		}
		const decision = composerKeyAction(
			e.nativeEvent,
			busy,
			escAt.current,
			Date.now(),
		);
		escAt.current = decision.escAt;
		switch (decision.action) {
			case "abort":
				e.preventDefault();
				void realtime.cmd(threadId, { type: "abort" }).catch(() => undefined);
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
		!disabled &&
		!att.uploading &&
		(text.trim() !== "" || att.attachmentIds.length > 0);
	return (
		<div ref={rootRef} className="relative">
			<RoleTrack threadId={threadId} anchorRef={rootRef} />
			{menuOpen && (
				<CommandMenu
					entries={menu}
					active={active}
					onPick={pick}
					hint={menu.length > 0 ? t("composer.menuHint") : undefined}
				/>
			)}
			<AttachmentStrip items={att.items} onRemove={att.remove} />
			<textarea
				onDragOver={(e) => {
					if (e.dataTransfer.types.includes("Files")) e.preventDefault();
				}}
				onDrop={(e) => {
					if (e.dataTransfer.files.length === 0) return;
					e.preventDefault();
					void att.addFiles(e.dataTransfer.files);
				}}
				ref={ref}
				rows={1}
				value={text}
				disabled={disabled}
				aria-label={t("composer.label")}
				placeholder={
					busy ? t("composer.placeholderBusy") : t("composer.placeholder")
				}
				className="block max-h-[40vh] min-h-[52px] w-full resize-none bg-transparent px-4 pt-[13px] pb-1.5 text-[14px] leading-5 outline-none placeholder:text-faint focus-visible:outline-none"
				onChange={(e) => {
					setText(e.target.value);
					setMenuDismissed(false);
				}}
				onKeyDown={onKeyDown}
				onPaste={(e) => {
					const files = [...e.clipboardData.files];
					if (files.length > 0) {
						e.preventDefault();
						void att.addFiles(files);
					}
				}}
			/>
			<div className="flex items-center gap-0.5 px-2 pb-2">
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
				<span
					aria-hidden
					className="mx-1.5 h-[18px] w-px shrink-0 bg-[var(--glass-line)]"
				/>
				<ModeStrip threadId={threadId} />
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
				<ModelChip threadId={threadId} />
				<ContextRing threadId={threadId} />
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
			{(error || att.error) && (
				<p role="alert" className="px-4 pb-2 text-err text-xs">
					{error ? describeError(error, t) : att.error}
				</p>
			)}
			<div className="@container/foot flex min-h-8 items-center gap-3.5 border-[var(--glass-line)] border-t px-3.5 py-[7px] text-2xs text-faint leading-4">
				{/* 按键提示照 D 稿：引导 · 排队 · 中断 · 命令；托盘窄时按 中断 → 排队 的顺序整段收起，手机与触屏不显示 */}
				<div className="hidden min-w-0 items-center gap-3.5 overflow-hidden whitespace-nowrap sm:pointer-fine:flex">
					<span className="inline-flex items-center gap-[5px]">
						<Kbd>Enter</Kbd>
						{t("composer.keySteer")}
					</span>
					<span className="hidden items-center gap-[5px] @md/foot:inline-flex">
						<Kbd>Ctrl+Enter</Kbd>
						{t("composer.keyQueue")}
					</span>
					<span className="hidden items-center gap-[5px] @lg/foot:inline-flex">
						<Kbd>Esc Esc</Kbd>
						{t("composer.keyAbort")}
					</span>
					<span className="inline-flex items-center gap-[5px]">
						<Kbd>/</Kbd>
						{t("composer.keyCommands")}
					</span>
				</div>
				<ModeStatus threadId={threadId} />
				<span className="flex-1" />
				<SessionReadout threadId={threadId} />
			</div>
		</div>
	);
}
