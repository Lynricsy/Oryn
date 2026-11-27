/** 声明式视图的静态块：标题、正文、徽标、提示、分隔线、读数、键值、表格、列表、进度、图片。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type {
	AlertBlock,
	BadgesBlock,
	HeadingBlock,
	ImageBlock,
	KeyValueBlock,
	ListBlock,
	ProgressBlock,
	StatsBlock,
	TableBlock,
	TableCell,
	TextBlock,
	Tone,
	Trend,
} from "../../lib/view/spec";
import { Markdown } from "../markdown/Markdown";
import {
	Badge,
	Callout,
	KeyValue,
	Meter,
	type Tone as UiTone,
} from "../ui/data";
import { Icon, type IconName } from "../ui/icons";

/** 视图的语气 → 界面元件的语气：info 用霜蓝，不占强调色（强调色只给「需要你」的东西）。 */
const TONE: Record<Tone, UiTone> = {
	neutral: "neutral",
	info: "frost",
	success: "ok",
	warning: "warn",
	danger: "err",
};

/** 按界面语言格式化数值；字符串原样显示。 */
function useFormatValue(): (v: string | number) => string {
	const { i18n } = useTranslation();
	const nf = new Intl.NumberFormat(i18n.language);
	return (v) => (typeof v === "number" ? nf.format(v) : v);
}

const HEADING_CLASS = {
	1: "font-semibold text-fg text-lg leading-7",
	2: "font-semibold text-base text-fg leading-6",
	3: "font-semibold text-fg text-sm",
} as const;

export function HeadingView({ block }: { block: HeadingBlock }): ReactNode {
	const cls = `m-0 text-balance ${HEADING_CLASS[block.level]}`;
	if (block.level === 1) return <h3 className={cls}>{block.text}</h3>;
	if (block.level === 2) return <h4 className={cls}>{block.text}</h4>;
	return <h5 className={cls}>{block.text}</h5>;
}

/** Markdown 正文：不加载图片（显示成链接），不渲染内嵌 HTML。 */
export function TextView({
	block,
	streaming,
}: {
	block: TextBlock;
	streaming: boolean;
}): ReactNode {
	return (
		<div className={block.muted ? "text-muted" : "text-fg"}>
			<Markdown text={block.text} streaming={streaming} images={false} />
		</div>
	);
}

export function BadgesView({ block }: { block: BadgesBlock }): ReactNode {
	return (
		<div className="flex flex-wrap gap-1.5">
			{block.items.map((it, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: 徽标可能重名，顺序即身份
				<Badge key={i} tone={TONE[it.tone]} title={it.label}>
					{it.label}
				</Badge>
			))}
		</div>
	);
}

export function AlertView({ block }: { block: AlertBlock }): ReactNode {
	return (
		<Callout tone={TONE[block.tone]} title={block.title}>
			<span className="whitespace-pre-wrap">{block.text}</span>
		</Callout>
	);
}

export function DividerView(): ReactNode {
	return <hr className="m-0 border-0 border-border border-t" />;
}

const TREND_ICON = {
	up: "trendUp",
	down: "trendDown",
	flat: "minus",
} as const satisfies Record<Trend, IconName>;

export function StatsView({ block }: { block: StatsBlock }): ReactNode {
	const { t } = useTranslation();
	const fmt = useFormatValue();
	return (
		<dl className="m-0 grid grid-cols-[repeat(auto-fit,minmax(8.5rem,1fr))] gap-2">
			{block.items.map((it, i) => (
				<div
					// biome-ignore lint/suspicious/noArrayIndexKey: 读数可能重名，顺序即身份
					key={i}
					className="flex min-w-0 flex-col gap-0.5 rounded-md border border-border px-3 py-2.5"
				>
					<dt className="truncate text-muted text-xs" title={it.label}>
						{it.label}
					</dt>
					<dd className="m-0 break-words font-semibold text-fg text-xl tabular-nums leading-7">
						{fmt(it.value)}
					</dd>
					{(it.delta || it.trend) && (
						<dd className="m-0 flex items-center gap-1 text-muted text-xs tabular-nums">
							{it.trend && (
								<Icon
									name={TREND_ICON[it.trend]}
									size={13}
									role="img"
									className="shrink-0"
									aria-label={t(`view.block.trend.${it.trend}`)}
								/>
							)}
							{it.delta && <span className="min-w-0 truncate">{it.delta}</span>}
						</dd>
					)}
					{it.hint && <dd className="m-0 text-faint text-xs">{it.hint}</dd>}
				</div>
			))}
		</dl>
	);
}

export function KeyValueView({ block }: { block: KeyValueBlock }): ReactNode {
	const fmt = useFormatValue();
	return (
		<KeyValue
			rows={block.items.map((it, i) => ({
				key: String(i),
				label: it.label,
				value: <span className="tabular-nums">{fmt(it.value)}</span>,
			}))}
		/>
	);
}

const ALIGN = {
	left: "text-left",
	right: "text-right",
	center: "text-center",
} as const;

function Cell({
	value,
	fmt,
}: {
	value: TableCell;
	fmt: (v: string | number) => string;
}): ReactNode {
	const { t } = useTranslation();
	if (value === null) return null;
	if (typeof value === "boolean")
		return (
			<Icon
				name={value ? "check" : "minus"}
				size={14}
				className={`inline-block ${value ? "text-fg" : "text-faint"}`}
				role="img"
				aria-label={t(value ? "view.block.yes" : "view.block.no")}
			/>
		);
	return fmt(value);
}

export function TableView({ block }: { block: TableBlock }): ReactNode {
	const { t } = useTranslation();
	const fmt = useFormatValue();
	return (
		<figure className="m-0 flex min-w-0 flex-col gap-1.5">
			{block.caption && (
				<figcaption className="font-medium text-muted text-xs">
					{block.caption}
				</figcaption>
			)}
			<div className="max-h-[28rem] overflow-auto rounded-md border border-border">
				<table className="w-full border-collapse text-sm">
					<thead className="sticky top-0 z-10 bg-raised">
						<tr>
							{block.columns.map((c) => (
								<th
									key={c.key}
									scope="col"
									className={`break-keep px-3 py-2 font-medium text-muted text-xs ${ALIGN[c.align]}`}
								>
									{c.label}
								</th>
							))}
						</tr>
					</thead>
					<tbody>
						{block.rows.length === 0 && (
							<tr className="border-border border-t">
								<td
									colSpan={block.columns.length}
									className="px-3 py-3 text-center text-faint text-xs"
								>
									{t("view.block.tableEmpty")}
								</td>
							</tr>
						)}
						{block.rows.map((row, ri) => (
							// biome-ignore lint/suspicious/noArrayIndexKey: 行没有键，顺序即身份
							<tr key={ri} className="border-border border-t">
								{block.columns.map((c) => (
									<td
										key={c.key}
										className={`break-keep px-3 py-2 align-top text-fg tabular-nums [overflow-wrap:anywhere] ${ALIGN[c.align]}`}
									>
										<Cell value={row[c.key] ?? null} fmt={fmt} />
									</td>
								))}
							</tr>
						))}
					</tbody>
				</table>
			</div>
		</figure>
	);
}

export function ListView({ block }: { block: ListBlock }): ReactNode {
	const items = block.items.map((it, i) => (
		// biome-ignore lint/suspicious/noArrayIndexKey: 列表项可能重名，顺序即身份
		<li key={i} className="flex gap-3 py-2 first:pt-0 last:pb-0">
			{block.ordered ? (
				<span className="w-5 shrink-0 pt-px text-right text-faint text-xs tabular-nums leading-5">
					{i + 1}.
				</span>
			) : (
				<span
					aria-hidden
					className="mt-2 h-1.5 w-1.5 shrink-0 rounded-full bg-ghost"
				/>
			)}
			<div className="min-w-0 flex-1">
				<div className="break-words font-medium text-fg text-sm">
					{it.title}
				</div>
				{it.description && (
					<p className="m-0 mt-0.5 break-words text-muted text-sm">
						{it.description}
					</p>
				)}
			</div>
			{it.meta && (
				<span className="max-w-[40%] shrink-0 truncate pt-px text-faint text-xs leading-5">
					{it.meta}
				</span>
			)}
		</li>
	));
	const cls = "m-0 flex list-none flex-col divide-y divide-border p-0";
	return block.ordered ? (
		<ol className={cls}>{items}</ol>
	) : (
		<ul className={cls}>{items}</ul>
	);
}

export function ProgressView({ block }: { block: ProgressBlock }): ReactNode {
	const { t } = useTranslation();
	const fmt = useFormatValue();
	const readout =
		block.max === 100
			? `${fmt(Math.round(block.value))}%`
			: `${fmt(block.value)} / ${fmt(block.max)}`;
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex items-baseline justify-between gap-3">
				<span className="min-w-0 truncate text-fg text-sm">{block.label}</span>
				<span className="shrink-0 text-muted text-xs tabular-nums">
					{readout}
				</span>
			</div>
			<Meter
				value={block.value}
				max={block.max}
				tone={TONE[block.tone]}
				label={block.label ?? t("view.block.progress")}
			/>
		</div>
	);
}

/** 图片：地址已由规格限定为 `https://` 或 `data:image/`；不带 Referer。 */
export function ImageView({ block }: { block: ImageBlock }): ReactNode {
	return (
		<figure className="m-0 flex flex-col items-start gap-1.5">
			<img
				src={block.src}
				alt={block.alt}
				loading="lazy"
				decoding="async"
				referrerPolicy="no-referrer"
				className="block h-auto max-h-[28rem] max-w-full rounded-md"
			/>
			{block.caption && (
				<figcaption className="text-faint text-xs">{block.caption}</figcaption>
			)}
		</figure>
	);
}
