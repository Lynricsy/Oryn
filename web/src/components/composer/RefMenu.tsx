/**
 * 输入框里的 `#` 引用（M14）：已绑定 GitHub 的项目里，光标前是 `#查询`（`#` 在行首或空白之后）时弹出该仓库的
 * issue / PR 候选；选中后把 `#查询` 片段从文本里删掉，并把该 issue / PR 渲染成 Markdown 附件加入本条消息。
 * 键盘：↑ / ↓ 循环移动，Enter（无 Shift）或 Tab 选中，Esc 只关菜单（文本再变化后重新允许打开）。
 * 面板与列表项的样式与 `/` 命令菜单一致。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { attachGitHubRef, useGitHubRefs } from "../../api/github-queries";
import type { Attachment } from "../../lib/attachments";
import { Icon } from "../ui/icons";

type GitHubRef = Schemas["GitHubRef"];

const TOKEN_RE = /(^|\s)#([^\s#]{0,50})$/;
const DEBOUNCE_MS = 200;

/** 光标前的 `#查询` 片段：`#` 须在行首或空白之后；没有则返回 null。 */
export function refToken(
	text: string,
	caret: number,
): { start: number; query: string } | null {
	const m = TOKEN_RE.exec(text.slice(0, caret));
	if (!m) return null;
	const query = m[2] ?? "";
	return { start: caret - query.length - 1, query };
}

export interface RefMenuModel {
	open: boolean;
	entries: GitHubRef[];
	active: number;
	pending: boolean;
	error: unknown;
	/** 菜单处理了这次按键时返回 true（已 `preventDefault`），调用方随即返回 */
	onKeyDown: (e: React.KeyboardEvent<HTMLTextAreaElement>) => boolean;
	pick: (r: GitHubRef) => void;
	dismiss: () => void;
}

export function useRefMenu(o: {
	/** 未绑定项目传 undefined（不启用） */
	projectId: string | undefined;
	text: string;
	caret: number;
	setText: (next: string, caret: number) => void;
	onAttached: (a: Attachment) => void;
}): RefMenuModel {
	const token = o.projectId ? refToken(o.text, o.caret) : null;
	// Esc 关掉时记下当时的文本：文本一变就重新允许打开
	const [dismissedAt, setDismissedAt] = useState<string | null>(null);
	const open = token !== null && dismissedAt !== o.text;
	const [query, setQuery] = useState(token?.query ?? "");
	const raw = token?.query ?? "";
	useEffect(() => {
		const id = setTimeout(() => setQuery(raw), DEBOUNCE_MS);
		return () => clearTimeout(id);
	}, [raw]);
	const refs = useGitHubRefs(o.projectId ?? "", query, open);
	const entries = open ? (refs.data?.refs ?? []) : [];
	// 高亮项按查询词记：查询变了回到第一项（渲染期派生，不用 effect）
	const [cursor, setCursor] = useState({ query, index: 0 });
	const active =
		cursor.query === query && cursor.index < entries.length ? cursor.index : 0;
	const [error, setError] = useState<unknown>(null);

	const pick = (r: GitHubRef) => {
		if (!token || !o.projectId) return;
		o.setText(
			o.text.slice(0, token.start) + o.text.slice(o.caret),
			token.start,
		);
		setError(null);
		attachGitHubRef(o.projectId, r.number).then(o.onAttached, setError);
	};
	const dismiss = () => setDismissedAt(o.text);

	const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
		if (!open || e.nativeEvent.isComposing) return false;
		if (e.key === "Escape") {
			e.preventDefault();
			dismiss();
			return true;
		}
		if (entries.length === 0) return false;
		const move = (d: number) => {
			e.preventDefault();
			setCursor({
				query,
				index: (active + d + entries.length) % entries.length,
			});
			return true;
		};
		if (e.key === "ArrowDown") return move(1);
		if (e.key === "ArrowUp") return move(-1);
		if ((e.key === "Enter" && !e.shiftKey) || e.key === "Tab") {
			e.preventDefault();
			const r = entries[active];
			if (r) pick(r);
			return true;
		}
		return false;
	};

	return {
		open,
		entries,
		active,
		pending: open && (refs.isPending || query !== raw),
		error,
		onKeyDown,
		pick,
		dismiss,
	};
}

export function RefMenu({
	entries,
	active,
	pending,
	onPick,
}: {
	entries: GitHubRef[];
	active: number;
	pending: boolean;
	onPick: (r: GitHubRef) => void;
}): ReactNode {
	const { t } = useTranslation();
	const listRef = useRef<HTMLDivElement>(null);
	// biome-ignore lint/correctness/useExhaustiveDependencies: active 变化即需要滚动
	useEffect(() => {
		listRef.current
			?.querySelector<HTMLElement>("[aria-selected=true]")
			?.scrollIntoView?.({ block: "nearest" });
	}, [active]);
	return (
		<div
			data-testid="ref-menu"
			className="glass pop-in absolute right-0 bottom-full left-0 z-30 mb-2 overflow-hidden rounded-xl"
		>
			<div className="border-[var(--glass-line)] border-b px-3 py-1 text-2xs text-faint">
				{t("composer.refs.hint")}
			</div>
			<div
				ref={listRef}
				role="listbox"
				aria-label={t("composer.refs.label")}
				className="max-h-[calc(9*1.75rem+0.5rem)] overflow-y-auto scroll-py-1 p-1"
			>
				{entries.length === 0 && (
					<div className="flex h-7 items-center px-2 text-faint text-sm">
						{pending ? t("composer.refs.loading") : t("composer.refs.empty")}
					</div>
				)}
				{entries.map((r, i) => {
					const selected = i === active;
					return (
						<button
							key={r.number}
							type="button"
							role="option"
							aria-selected={selected}
							data-ref={r.number}
							title={r.title}
							className={`flex h-7 w-full items-center gap-2 rounded-sm px-2 text-left text-xs transition-colors duration-150 hover:bg-hover ${selected ? "bg-hover" : ""}`}
							onMouseDown={(ev) => {
								ev.preventDefault();
								onPick(r);
							}}
						>
							<Icon
								name={r.kind === "pull" ? "pullRequest" : "circle"}
								size={13}
								aria-hidden
								className={`shrink-0 ${r.state === "open" ? "text-ok" : "text-faint"}`}
							/>
							<span className="shrink-0 font-medium font-mono text-fg tabular-nums">
								{`#${r.number}`}
							</span>
							<span className="min-w-0 flex-1 truncate text-muted">
								{r.title}
							</span>
							<span className="shrink-0 text-2xs text-faint">
								{t(`composer.refs.state.${r.state}`)}
							</span>
						</button>
					);
				})}
			</div>
		</div>
	);
}
