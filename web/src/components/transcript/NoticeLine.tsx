/**
 * 转录里的内联提示（`command_output`、通知、压缩 / 重试结果、扩展报错……，见 thread-reducer 的 `pushNotice`）。
 *
 * 形态与工具行一致：一行「图标方块 + 标签」；短正文接在标签后面，长正文 / 多行正文放进标签下方左缘对齐标签的卡片。
 * 正文默认按 Markdown 渲染（omp 的命令输出本来就是给 ACP 客户端看的 Markdown：`/changelog` 是标题 + 列表，
 * `/jobs`、`/usage` 把对齐的树 / 量表包在围栏代码块里）；没有包围栏、靠空格对齐的纯文本（`/context` 等）
 * 按等宽原样排（规则见 `noticeBodyFormat`）；`/mcp list`、`/ssh list` 的 ` | ` 分隔行渲染成表格（`lib/config-list.ts`）。
 * 超过 `max-h-80` 的正文折叠，底部「展开全部」；卡片右上角可复制全文。
 * 信息 / 警告 / 错误只给图标方块、标签和卡片描边上色，颜色来自令牌。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { type ConfigList, parseConfigList } from "../../lib/config-list";
import type { Notice } from "../../state/thread-reducer";
import { Markdown } from "../markdown/Markdown";
import { CopyButton } from "../ui/CopyButton";
import { Icon, type IconName } from "../ui/icons";
import { ConfigListTable } from "./ConfigListTable";
import { Foldable } from "./Foldable";

/** 正文的排法：`markdown` 交给 Markdown 组件；`plain` 是靠空格对齐的纯文本，等宽、不折行、横向滚动。 */
export type NoticeBodyFormat = "markdown" | "plain";

const FENCE_RE = /^ {0,3}(`{3,}|~{3,})/;
/** 制表符与框线 / 方块字符（树形导轨、ASCII 量表）：只有等宽才对得齐。 */
const DRAWING_RE = /[\t\u2500-\u259f]/;
/** 词与词之间连续两个以上空格 = 按列对齐的表格 / 键值。 */
const ALIGN_RE = /\S {2,}\S/;

/** 每一行是否在围栏代码块以外（围栏的开闭行本身也算围栏）。 */
function proseLines(lines: string[]): boolean[] {
	let fence: string | null = null;
	return lines.map((line) => {
		const m = FENCE_RE.exec(line)?.[1];
		if (fence) {
			if (m && m[0] === fence[0] && m.length >= fence.length) fence = null;
			return false;
		}
		if (m) fence = m;
		return !m;
	});
}

/**
 * 判定规则（只看围栏代码块以外的行，围栏里的内容 Markdown 本来就按等宽原样排）：
 * - 含制表符或框线 / 方块字符（U+2500–U+259F）→ `plain`；
 * - 至少两行在词与词之间有连续两个以上空格（不算以 `|` 开头的 Markdown 表格行）→ `plain`；
 * - 其余 → `markdown`（纯散文也走 Markdown：换行由 remark-breaks 保留，看起来与纯文本相同）。
 */
export function noticeBodyFormat(text: string): NoticeBodyFormat {
	const lines = text.split("\n");
	const prose = proseLines(lines);
	let aligned = 0;
	for (const [i, line] of lines.entries()) {
		if (!prose[i]) continue;
		if (DRAWING_RE.test(line)) return "plain";
		if (!line.trimStart().startsWith("|") && ALIGN_RE.test(line)) {
			aligned++;
			if (aligned >= 2) return "plain";
		}
	}
	return "markdown";
}

/**
 * 命令输出里的 `<id>`、`<path>` 是用法占位符，不是 HTML：Markdown 不渲染原始 HTML，会把它们整个吞掉
 * （「/jobs kill <id>」显示成「/jobs kill 」）。围栏与行内代码以外的 `<` 转义成字面量。
 */
export function escapeAngles(text: string): string {
	const lines = text.split("\n");
	const prose = proseLines(lines);
	return lines
		.map((line, i) =>
			prose[i]
				? line
						.split(/(`+[^`]*`+)/)
						.map((seg, j) => (j % 2 ? seg : seg.replaceAll("<", "\\<")))
						.join("")
				: line,
		)
		.join("\n");
}

/** 单行、不长、不需要等宽的正文接在标签后面；其余放进卡片。 */
const INLINE_MAX = 160;

const LEVEL_STYLE: Record<
	Notice["level"],
	{ icon: IconName; chip: string; label: string; card: string }
> = {
	info: {
		icon: "info",
		chip: "bg-inset text-muted",
		label: "text-muted",
		card: "border-border",
	},
	warning: {
		icon: "alert",
		chip: "bg-warn/15 text-warn-ink",
		label: "text-warn-ink",
		card: "border-warn/40",
	},
	error: {
		icon: "xCircle",
		chip: "bg-err/10 text-err",
		label: "text-err",
		card: "border-err/35",
	},
};

/**
 * 正文字号：转录正文是 14.5px，提示是「过程」，退一档到 13px（text-sm）。
 * markdown.css 不在 Tailwind 的层里，普通工具类压不过它的 `.md { font-size }`，所以带 `!`。
 */
export const MD_SIZE = "[&_.md]:text-sm! [&_.md]:leading-relaxed!";

export function NoticeLine({ notice }: { notice: Notice }): ReactNode {
	const { t } = useTranslation();
	const label = t(`notice.${notice.kind}`);
	const style = LEVEL_STYLE[notice.level];
	const icon: IconName =
		notice.kind === "command_output" && notice.level === "info"
			? "terminal"
			: style.icon;
	const url =
		notice.kind === "open_url" &&
		notice.detail &&
		typeof notice.detail === "object" &&
		"url" in notice.detail
			? String(notice.detail.url)
			: null;
	// 只去掉首尾空行：首行的缩进是对齐的一部分
	const text = notice.text.replace(/^(?:[ \t]*\n)+/, "").trimEnd();
	const format = noticeBodyFormat(text);
	// `/mcp list`、`/ssh list` 的固定格式行：渲染成表格
	const list = notice.kind === "command_output" ? parseConfigList(text) : null;
	const inline =
		!text ||
		(!list &&
			format === "markdown" &&
			text.length <= INLINE_MAX &&
			!text.includes("\n") &&
			!FENCE_RE.test(text));
	const copyLabel = t("notice.copy", { label });
	return (
		<div
			data-notice={notice.kind}
			data-level={notice.level}
			className="group/notice min-w-0 py-0.5"
		>
			<div className="flex min-h-6 min-w-0 items-start gap-2 text-xs">
				<span
					aria-hidden
					className={`mt-[3px] inline-grid size-[18px] shrink-0 place-items-center rounded ${style.chip}`}
				>
					<Icon name={icon} size={11} weight="bold" />
				</span>
				<span className={`shrink-0 font-medium leading-6 ${style.label}`}>
					{label}
				</span>
				{inline && text ? (
					<div className="min-w-0 flex-1 py-0.5 text-fg [&_.md]:text-xs! [&_.md]:leading-5!">
						<Markdown text={escapeAngles(text)} />
					</div>
				) : (
					<span className="flex-1" />
				)}
				{text && (
					<span
						className={
							inline
								? "-my-px shrink-0 opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/notice:opacity-100 pointer-coarse:opacity-100"
								: "-my-px shrink-0"
						}
					>
						<CopyButton
							text={notice.text}
							ariaLabel={copyLabel}
							testId="notice-copy"
						/>
					</span>
				)}
			</div>
			{!inline && (
				<NoticeBody
					text={text}
					format={format}
					list={list}
					cardTone={style.card}
					lines={text.split("\n").length}
				/>
			)}
			{url && (
				<a
					className="ml-[26px] block break-all text-xs text-accent-ink underline underline-offset-2"
					href={url}
					target="_blank"
					rel="noopener noreferrer"
				>
					{url}
				</a>
			)}
		</div>
	);
}

function NoticeBody({
	text,
	format,
	list,
	cardTone,
	lines,
}: {
	text: string;
	format: NoticeBodyFormat;
	list: ConfigList | null;
	cardTone: string;
	lines: number;
}): ReactNode {
	return (
		<Foldable
			lines={lines}
			testId="notice-body"
			className={`focus-inset mt-1 ml-[26px] overflow-hidden rounded-md border ${cardTone} ${format === "plain" && !list ? "bg-inset" : "bg-panel"}`}
		>
			{list ? (
				<div className="py-1">
					<ConfigListTable list={list} />
				</div>
			) : format === "plain" ? (
				<pre className="m-0 overflow-x-auto whitespace-pre px-3 py-2.5 font-mono text-xs leading-relaxed text-fg">
					{text}
				</pre>
			) : (
				<div className={`px-3.5 py-2.5 text-fg ${MD_SIZE}`}>
					<Markdown text={escapeAngles(text)} />
				</div>
			)}
		</Foldable>
	);
}
