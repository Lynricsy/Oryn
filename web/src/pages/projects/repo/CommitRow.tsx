/**
 * 一个提交的行（M24 阶段 ②）：gitmoji（没有时为提交图标）、subject、短 sha、关联的线程与多久前；整行进 `to`（提交详情）。
 * 整行链接用伸展的 `::after`，线程链接排在它上面单独可点，不嵌套交互元素。主页「提交到 <基线>」与提交子页的列表共用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { RepoCommit } from "../../../api/repo-queries";
import { ListRow, TitleGlyph } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import { formatRelative } from "../../../lib/format";
import { parseCommitSubject } from "../../../lib/gitmoji";
import { parseTitleCard } from "../../../lib/title";

export function CommitRow({
	commit: c,
	to,
	now,
	selected = false,
	author = false,
}: {
	commit: RepoCommit;
	to: string;
	now: number;
	/** 提交子页里当前打开的那个 */
	selected?: boolean;
	/** 在 meta 里加作者名（提交子页） */
	author?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const subject = parseCommitSubject(c.subject);
	return (
		<li
			data-testid="repo-commit-row"
			data-sha={c.sha}
			className={`relative rounded-md transition-colors duration-150 ${selected ? "bg-active" : "hover:bg-hover"}`}
		>
			<ListRow
				leading={<CommitGlyph glyph={subject.glyph} />}
				title={
					<Link
						to={to}
						aria-current={selected ? "page" : undefined}
						className="min-w-0 truncate after:absolute after:inset-0 after:rounded-md"
					>
						{subject.text}
					</Link>
				}
				meta={
					<>
						<span className="font-mono">{c.shortSha}</span>
						{author && (
							<span className="min-w-0 max-w-40 truncate">{c.authorName}</span>
						)}
						{c.thread && (
							<Link
								to={`/t/${c.thread.id}`}
								className="relative inline-flex min-w-0 items-center gap-1 text-accent-ink hover:underline"
							>
								<Icon
									name="message"
									size={11}
									aria-hidden
									className="shrink-0"
								/>
								{/* 不另设宽度上限：放不下时整个链接换到下一行再截断，宽屏能显示完整标题 */}
								<span className="min-w-0 truncate">
									{parseTitleCard(c.thread.title).text || t("thread.untitled")}
								</span>
							</Link>
						)}
						<span className="tabular-nums">{formatRelative(c.ts, now)}</span>
					</>
				}
			/>
		</li>
	);
}

/** 提交行的前导图标：gitmoji 用彩色小瓷片，否则是提交图标；两者占同样的 16px 宽，列表里标题对齐。 */
export function CommitGlyph({ glyph }: { glyph?: string }): ReactNode {
	return (
		<span className="inline-flex w-4 shrink-0 justify-center">
			{glyph ? (
				<TitleGlyph icon={glyph} colored />
			) : (
				<Icon name="commit" size={14} aria-hidden className="text-faint" />
			)}
		</span>
	);
}
