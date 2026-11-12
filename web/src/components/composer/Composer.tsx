/**
 * 线程输入区：发送 / steer / 排队 / 中断按键（`lib/composer.ts`）、`/` 命令菜单、`!`/`!!` bash、图片附件。
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
	promptFrame,
	resolveSlashText,
	type SendMode,
} from "../../state/actions";
import { useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Icon } from "../ui/icons";
import { describeError, IconButton, Kbd, Spinner } from "../ui/primitives";
import { AttachmentStrip, useImageAttachments } from "./attachments";
import { CommandMenu, type MenuEntry, useCommandMenu } from "./CommandMenu";

export function Composer({
	threadId,
	disabled,
}: {
	threadId: string;
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const busy = useThread(
		threadId,
		(s) => s.running || s.thread?.state === "working",
	);
	const commands = useThread(threadId, (s) => s.commands);
	const editorText = useThread(threadId, (s) => s.editorText);
	const [text, setText] = useState("");
	const [error, setError] = useState<unknown>(null);
	const [bashRunning, setBashRunning] = useState(false);
	const [active, setActive] = useState(0);
	const [menuDismissed, setMenuDismissed] = useState(false);
	const escAt = useRef<number | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);
	const att = useImageAttachments();
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

	const reset = () => {
		setText("");
		att.clear();
		setMenuDismissed(false);
	};

	const runSlash = async (action: SlashAction, mode: SendMode) => {
		switch (action.kind) {
			case "rpc":
				await realtime.cmd(threadId, action.frame);
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
		const parsed = parseComposerInput(text);
		setError(null);
		try {
			if (parsed.kind === "empty") {
				if (att.contents.length === 0) return;
				await realtime.cmd(threadId, promptFrame(mode, "", att.contents));
				reset();
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
					reset();
					return;
				}
			}
			await realtime.cmd(
				threadId,
				promptFrame(
					mode,
					parsed.kind === "text" ? parsed.text : text.trim(),
					att.contents,
				),
			);
			reset();
		} catch (e) {
			setError(e);
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

	const canSend = !disabled && (text.trim() !== "" || att.images.length > 0);
	return (
		<div className="relative mx-auto w-full max-w-3xl">
			{menuOpen && (
				<CommandMenu
					entries={menu}
					active={active}
					onPick={pick}
					hint={menu.length > 0 ? t("composer.menuHint") : undefined}
				/>
			)}
			<div className="rounded-xl border border-border bg-panel shadow-sm focus-within:border-accent/60">
				<AttachmentStrip images={att.images} onRemove={att.remove} />
				<textarea
					onDragOver={(e) => e.preventDefault()}
					onDrop={(e) => {
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
					className="block max-h-[40vh] w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-sm outline-none placeholder:text-faint"
					onChange={(e) => {
						setText(e.target.value);
						setMenuDismissed(false);
					}}
					onKeyDown={onKeyDown}
					onPaste={(e) => {
						const files = [...e.clipboardData.files].filter((f) =>
							f.type.startsWith("image/"),
						);
						if (files.length > 0) {
							e.preventDefault();
							void att.addFiles(files);
						}
					}}
				/>
				<div className="flex items-center gap-1 px-2 pb-2">
					<label className="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg">
						<Icon name="image" size={16} />
						<span className="sr-only">{t("composer.attach")}</span>
						<input
							type="file"
							accept="image/*"
							multiple
							className="hidden"
							onChange={(e) => {
								if (e.target.files) void att.addFiles(e.target.files);
								e.target.value = "";
							}}
						/>
					</label>
					{bashRunning && (
						<button
							type="button"
							className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-muted text-xs hover:bg-hover"
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
					<div className="hidden min-w-0 flex-1 items-center gap-2 truncate text-[11px] text-faint sm:flex">
						{busy ? (
							<>
								<span>
									<Kbd>Enter</Kbd> {t("composer.keySteer")}
								</span>
								<span>
									<Kbd>Ctrl+Enter</Kbd> {t("composer.keyQueue")}
								</span>
								<span>
									<Kbd>Alt+Enter</Kbd> {t("composer.keyInterrupt")}
								</span>
								<span>
									<Kbd>Esc Esc</Kbd> {t("composer.keyAbort")}
								</span>
							</>
						) : (
							<>
								<span>
									<Kbd>/</Kbd> {t("composer.keyCommands")}
								</span>
								<span>
									<Kbd>!</Kbd> {t("composer.keyBash")}
								</span>
							</>
						)}
					</div>
					<div className="flex-1 sm:hidden" />
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
						className="inline-flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-fg disabled:opacity-40"
					>
						<Icon name="send" size={16} />
					</button>
				</div>
			</div>
			{(error || att.error) && (
				<p role="alert" className="mt-1 px-1 text-err text-xs">
					{error ? describeError(error, t) : att.error}
				</p>
			)}
		</div>
	);
}
