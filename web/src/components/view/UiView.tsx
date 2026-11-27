/**
 * 声明式视图（`render_ui`，docs/plan-m33.md 的 C2 / C10）：直出在转录里，不套工具卡外壳。
 * 容器（card / columns / tabs）与块分发在这里，叶子块各自一个组件；块之间共享的作答状态走 `ViewContext`。
 */
import {
	type CSSProperties,
	type KeyboardEvent,
	type ReactNode,
	useId,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { ViewAnswer } from "../../lib/view/answer";
import type {
	CardBlock,
	ColumnsBlock,
	TabsBlock,
	UiViewSpec,
	ViewBlock,
} from "../../lib/view/spec";
import { tabClass } from "../ui/primitives";
import { ActionsView } from "./ActionsView";
import {
	AlertView,
	BadgesView,
	DividerView,
	HeadingView,
	ImageView,
	KeyValueView,
	ListView,
	ProgressView,
	StatsView,
	TableView,
	TextView,
} from "./blocks";
import { ChartView } from "./ChartView";
import { useView, ViewContext, type ViewContextValue } from "./context";
import { FormView } from "./FormView";
import { SvgView } from "./SvgView";

export interface UiViewProps {
	spec: UiViewSpec;
	toolCallId: string;
	/** 已作答（含在途提交）的块：块路径 → 作答。已作答的块一律锁定并显示作答内容。 */
	answers: ReadonlyMap<string, ViewAnswer>;
	/** 控件可用；false 时全部禁用（参数流式中 / 尚未开始执行且没有挂起的 await / 只读页 / callId 不可作答）。 */
	interactive: boolean;
	/** 参数仍在流式：chart 与 svg 只显示骨架。 */
	streaming?: boolean;
	/** 提交一个块。text 是作答正文（表单用 formatFormAnswer，按钮用其 prompt）。reject 时该块解锁并显示错误。只读页不给。 */
	onAnswer?: (answer: ViewAnswer, text: string) => Promise<void>;
}

function CardView({ block }: { block: CardBlock }): ReactNode {
	return (
		<section className="flex min-w-0 flex-col gap-3 rounded-md bg-raised p-3">
			{(block.title || block.description) && (
				<header className="flex flex-col gap-0.5">
					{block.title && (
						<h4 className="m-0 font-semibold text-fg text-sm">{block.title}</h4>
					)}
					{block.description && (
						<p className="m-0 text-muted text-xs">{block.description}</p>
					)}
				</header>
			)}
			<BlockList blocks={block.blocks} />
		</section>
	);
}

/** 分栏：容器够宽才按 weights 并排，窄了上下堆叠（按自身宽度判断，嵌套在窄栏里同样堆叠）。 */
function ColumnsView({ block }: { block: ColumnsBlock }): ReactNode {
	const weights = block.weights ?? block.columns.map(() => 1);
	const style = {
		"--view-cols": weights.map((w) => `minmax(0,${w}fr)`).join(" "),
	} as CSSProperties;
	return (
		<div className="@container min-w-0">
			<div
				style={style}
				className={`grid grid-cols-1 gap-3 ${block.columns.length > 2 ? "@xl:grid-cols-[var(--view-cols)]" : "@lg:grid-cols-[var(--view-cols)]"}`}
			>
				{block.columns.map((c, i) => (
					// biome-ignore lint/suspicious/noArrayIndexKey: 栏的下标即块路径的一段
					<div key={i} className="flex min-w-0 flex-col gap-3">
						<BlockList blocks={c.blocks} />
					</div>
				))}
			</div>
		</div>
	);
}

/** 标签页：各页都保持挂载（表单草稿、图表不因切换丢失），只显示当前页。←/→、Home/End 切换。 */
function TabsView({ block }: { block: TabsBlock }): ReactNode {
	const base = useId();
	const [active, setActive] = useState(0);
	const current = Math.min(active, block.tabs.length - 1);
	const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
		const n = block.tabs.length;
		const next =
			e.key === "ArrowRight"
				? (current + 1) % n
				: e.key === "ArrowLeft"
					? (current - 1 + n) % n
					: e.key === "Home"
						? 0
						: e.key === "End"
							? n - 1
							: null;
		if (next === null) return;
		e.preventDefault();
		setActive(next);
		document.getElementById(`${base}-tab${next}`)?.focus();
	};
	return (
		<div className="flex min-w-0 flex-col gap-3">
			<div
				role="tablist"
				className="flex flex-wrap gap-1"
				onKeyDown={onKeyDown}
			>
				{block.tabs.map((tab, i) => (
					<button
						// biome-ignore lint/suspicious/noArrayIndexKey: 页的下标即块路径的一段
						key={i}
						type="button"
						role="tab"
						id={`${base}-tab${i}`}
						aria-selected={i === current}
						aria-controls={`${base}-panel${i}`}
						tabIndex={i === current ? 0 : -1}
						onClick={() => setActive(i)}
						className={`${tabClass(i === current)} max-w-full ${i === current ? "bg-hover" : ""}`}
					>
						<span className="min-w-0 truncate">{tab.label}</span>
					</button>
				))}
			</div>
			{block.tabs.map((tab, i) => (
				<div
					// biome-ignore lint/suspicious/noArrayIndexKey: 页的下标即块路径的一段
					key={i}
					role="tabpanel"
					id={`${base}-panel${i}`}
					aria-labelledby={`${base}-tab${i}`}
					hidden={i !== current}
					className="flex min-w-0 flex-col gap-3"
				>
					<BlockList blocks={tab.blocks} />
				</div>
			))}
		</div>
	);
}

function Block({ block }: { block: ViewBlock }): ReactNode {
	const { streaming } = useView();
	switch (block.type) {
		case "heading":
			return <HeadingView block={block} />;
		case "text":
			return <TextView block={block} streaming={streaming} />;
		case "badges":
			return <BadgesView block={block} />;
		case "alert":
			return <AlertView block={block} />;
		case "divider":
			return <DividerView />;
		case "stats":
			return <StatsView block={block} />;
		case "keyValue":
			return <KeyValueView block={block} />;
		case "table":
			return <TableView block={block} />;
		case "list":
			return <ListView block={block} />;
		case "progress":
			return <ProgressView block={block} />;
		case "image":
			return <ImageView block={block} />;
		case "chart":
			return <ChartView block={block} streaming={streaming} />;
		case "svg":
			return <SvgView block={block} streaming={streaming} />;
		case "form":
			return <FormView block={block} />;
		case "actions":
			return <ActionsView block={block} />;
		case "card":
			return <CardView block={block} />;
		case "columns":
			return <ColumnsView block={block} />;
		case "tabs":
			return <TabsView block={block} />;
	}
}

function BlockList({ blocks }: { blocks: readonly ViewBlock[] }): ReactNode {
	return blocks.map((b) => <Block key={b.path} block={b} />);
}

export function UiView({
	spec,
	toolCallId,
	answers,
	interactive,
	streaming = false,
	onAnswer,
}: UiViewProps): ReactNode {
	const { t } = useTranslation();
	const ctx = useMemo<ViewContextValue>(
		() => ({
			toolCallId,
			answers,
			interactive: interactive && !!onAnswer,
			streaming,
			onAnswer,
		}),
		[toolCallId, answers, interactive, streaming, onAnswer],
	);
	return (
		<ViewContext.Provider value={ctx}>
			<section
				aria-label={spec.title ?? t("toolRender.renderUi.title")}
				data-view-width={spec.width}
				className={`@container flex w-full min-w-0 flex-col gap-3 rounded-lg border border-border p-4 ${spec.width === "wide" ? "" : "max-w-[40rem]"}`}
			>
				{(spec.title || spec.description) && (
					<header className="flex flex-col gap-1">
						{spec.title && (
							<h3 className="m-0 text-balance font-semibold text-base text-fg leading-6">
								{spec.title}
							</h3>
						)}
						{spec.description && (
							<p className="m-0 text-muted text-sm">{spec.description}</p>
						)}
					</header>
				)}
				<BlockList blocks={spec.blocks} />
			</section>
		</ViewContext.Provider>
	);
}
