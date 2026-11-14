/**
 * Desktop 工具条的剪贴板弹层（M8 4.4）：
 * - 桌面 → 本机：显示最近一次桌面剪贴板内容 +「复制到本机」；只有点这个按钮才写本机剪贴板。
 * - 本机 → 桌面：文本框 +「发送到桌面」（之后在桌面里 Ctrl+V 粘贴）；舞台上直接 Ctrl/⌘+V 也会发送（见 DesktopPanel）。
 * - 没有自动同步（安全审查 F3）：自动读本机剪贴板会把刚复制的密码送进 agent 可读的桌面，
 *   自动写本机剪贴板会让 orb 里的进程悄悄替换用户随后要粘贴到本地终端的内容。底部一句话说明这一点。
 * 开合照 Portal 的访问弹层：宽屏是非模态弹层（外点 / Esc 关闭），compact 用 Dialog。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { focusablesIn } from "../../lib/modal";
import { useBreakpoint } from "../../lib/viewport";
import { CopyButton } from "../ui/CopyButton";
import { Button, Dialog, IconButton, TextArea } from "../ui/primitives";

export function ClipboardBody({
	remoteText,
	onSend,
	disabled = false,
}: {
	remoteText: string | null;
	onSend: (text: string) => void;
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [draft, setDraft] = useState("");
	const [sent, setSent] = useState(false);
	return (
		<div className="flex flex-col gap-3" data-testid="desktop-clipboard">
			<section>
				<div className="mb-1 flex items-center justify-between gap-2">
					<h3 className="font-medium text-2xs text-faint">
						{t("deskPanel.clip.fromDesktop")}
					</h3>
					{remoteText && (
						<CopyButton
							text={remoteText}
							label={t("deskPanel.clip.copyLocal")}
							testId="desktop-clip-copy"
						/>
					)}
				</div>
				{remoteText ? (
					<pre
						data-testid="desktop-clip-remote"
						className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded-md bg-inset px-2.5 py-2 font-mono text-fg text-xs"
					>
						{remoteText}
					</pre>
				) : (
					<p className="text-faint text-xs">{t("deskPanel.clip.empty")}</p>
				)}
			</section>
			<section>
				<h3 className="mb-1 font-medium text-2xs text-faint">
					{t("deskPanel.clip.toDesktop")}
				</h3>
				<TextArea
					rows={3}
					aria-label={t("deskPanel.clip.toDesktop")}
					value={draft}
					onChange={(e) => {
						setDraft(e.target.value);
						setSent(false);
					}}
				/>
				<div className="mt-1.5 flex items-center justify-between gap-2">
					<span className="text-faint text-xs" role="status">
						{sent ? t("deskPanel.clip.sent") : t("deskPanel.clip.pasteHint")}
					</span>
					<Button
						size="sm"
						variant="primary"
						icon="send"
						disabled={disabled || !draft}
						onClick={() => {
							onSend(draft);
							setSent(true);
						}}
					>
						{t("deskPanel.clip.send")}
					</Button>
				</div>
			</section>
			<p className="border-border border-t pt-3 text-faint text-xs">
				{t("deskPanel.clip.manualNote")}
			</p>
		</div>
	);
}

/** 剪贴板按钮 + 弹层。 */
export function ClipboardPopover(props: {
	remoteText: string | null;
	onSend: (text: string) => void;
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const [open, setOpen] = useState(false);
	const ref = useRef<HTMLDivElement>(null);
	const triggerRef = useRef<HTMLButtonElement>(null);
	const popRef = useRef<HTMLDivElement>(null);
	useLayoutEffect(() => {
		const pop = popRef.current;
		if (open && !compact && pop) (focusablesIn(pop)[0] ?? pop).focus();
	}, [open, compact]);
	useEffect(() => {
		if (!open || compact) return;
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node))
				setOpen(false);
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			setOpen(false);
			triggerRef.current?.focus();
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [open, compact]);
	const title = t("deskPanel.clip.title");
	return (
		<div ref={ref} className="relative">
			<IconButton
				ref={triggerRef}
				icon="clipboard"
				box="sm"
				size={15}
				label={title}
				aria-expanded={open}
				aria-haspopup="dialog"
				className={open ? "bg-active text-fg" : ""}
				onClick={() => setOpen((v) => !v)}
			/>
			{open &&
				(compact ? (
					<Dialog open title={title} onClose={() => setOpen(false)}>
						<ClipboardBody {...props} />
					</Dialog>
				) : (
					<div
						ref={popRef}
						role="dialog"
						aria-label={title}
						tabIndex={-1}
						className="glass pop-in absolute top-full right-0 z-40 mt-1 w-80 max-w-[calc(100vw-1rem)] rounded-lg p-3 outline-none"
					>
						<h2 className="mb-2.5 font-medium text-faint text-xs">{title}</h2>
						<ClipboardBody {...props} />
					</div>
				))}
		</div>
	);
}
