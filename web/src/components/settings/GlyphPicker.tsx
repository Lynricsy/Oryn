/**
 * 字形选择（项目图标与各种头像共用）：触发按钮 + Notion 式弹层——「图标 / 表情符号」两页、搜索框、分组网格、右上角移除。
 * 「常用」精选由调用方同步给出，打开即显示；完整目录（Nerd dev / linux / md 与全部 emoji）首次打开时按需加载，网格按行虚拟滚动。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import {
	type ReactNode,
	useDeferredValue,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type CatalogGlyph,
	type Glyph,
	type GlyphCatalog,
	loadGlyphCatalog,
	searchGlyphs,
} from "../../lib/glyphs";
import { isNerdGlyph } from "../../lib/title";
import { useBreakpoint } from "../../lib/viewport";
import { Popover } from "../ui/PickerPopover";
import { TextInput, tabClass } from "../ui/primitives";

export type GlyphTab = "icons" | "emoji";
const TABS: readonly GlyphTab[] = ["icons", "emoji"];

/** 每页的「常用」精选；传模块级常量，避免每次渲染重算网格。 */
export type FeaturedGlyphs = Readonly<Record<GlyphTab, readonly Glyph[]>>;

/** 一行 10 格；分组标题行与字形行同高（32px 方格 + 2px 行距），虚拟滚动不用逐行量高。 */
const COLS = 10;
const ROW = 34;

type Row =
	| { kind: "head"; key: string; label: string }
	| { kind: "cells"; key: string; glyphs: readonly (Glyph | CatalogGlyph)[] };

interface Section {
	id: string;
	/** 搜索结果不带标题 */
	label?: string;
	glyphs: readonly (Glyph | CatalogGlyph)[];
}

function rowsOf(sections: readonly Section[]): Row[] {
	const rows: Row[] = [];
	for (const s of sections) {
		if (s.glyphs.length === 0) continue;
		if (s.label) rows.push({ kind: "head", key: `h:${s.id}`, label: s.label });
		for (let i = 0; i < s.glyphs.length; i += COLS)
			rows.push({
				kind: "cells",
				key: `${s.id}:${i}`,
				glyphs: s.glyphs.slice(i, i + COLS),
			});
	}
	return rows;
}

export function GlyphPicker({
	value,
	featured,
	defaultTab = "icons",
	label,
	removeLabel,
	onRemove,
	onPick,
	disabled,
	triggerClassName = "",
	children,
}: {
	value?: string;
	featured: FeaturedGlyphs;
	/** 当前没有字形时先打开哪一页；有字形时打开它所在的页 */
	defaultTab?: GlyphTab;
	/** 触发按钮与弹层的无障碍名 */
	label: string;
	/** 弹层右上角的移除 / 自动按钮；不给 `onRemove` 就不显示 */
	removeLabel?: string;
	onRemove?: () => void;
	onPick: (char: string) => void;
	disabled?: boolean;
	triggerClassName?: string;
	/** 触发按钮的内容 */
	children: ReactNode;
}): ReactNode {
	const [open, setOpen] = useState(false);
	const trigger = useRef<HTMLButtonElement>(null);
	// 选中或移除后关闭，焦点回到触发按钮（按钮所在的行不会因为弹层卸载而丢焦点）
	const done = () => {
		setOpen(false);
		trigger.current?.focus();
	};
	return (
		<span className="relative inline-flex">
			<button
				ref={trigger}
				type="button"
				aria-label={label}
				title={label}
				aria-haspopup="dialog"
				aria-expanded={open}
				disabled={disabled}
				// 弹层开着时按下触发按钮：拦住冒泡，否则弹层的外点关闭先关掉它，随后的 click 又把它打开
				onMouseDown={(e) => {
					if (open) e.stopPropagation();
				}}
				onClick={() => setOpen((o) => !o)}
				className={triggerClassName}
			>
				{children}
			</button>
			{open && (
				<GlyphPanel
					value={value}
					featured={featured}
					defaultTab={defaultTab}
					label={label}
					removeLabel={removeLabel}
					onRemove={
						onRemove &&
						(() => {
							onRemove();
							done();
						})
					}
					onPick={(c) => {
						onPick(c);
						done();
					}}
					onClose={() => setOpen(false)}
				/>
			)}
		</span>
	);
}

function GlyphPanel({
	value,
	featured,
	defaultTab,
	label,
	removeLabel,
	onRemove,
	onPick,
	onClose,
}: {
	value?: string;
	featured: FeaturedGlyphs;
	defaultTab: GlyphTab;
	label: string;
	removeLabel?: string;
	onRemove?: () => void;
	onPick: (char: string) => void;
	onClose: () => void;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const [tab, setTab] = useState<GlyphTab>(() =>
		value ? (isNerdGlyph(value) ? "icons" : "emoji") : defaultTab,
	);
	const [q, setQ] = useState("");
	const query = useDeferredValue(q.trim());
	const [catalog, setCatalog] = useState<GlyphCatalog>();
	const [failed, setFailed] = useState(false);
	useEffect(() => {
		let live = true;
		loadGlyphCatalog().then(
			(c) => {
				if (live) setCatalog(c);
			},
			() => {
				if (live) setFailed(true);
			},
		);
		return () => {
			live = false;
		};
	}, []);

	const rows = useMemo(() => {
		// 精选换成目录里的同一字形：emoji 精选因此也有名字可搜、可悬停
		const pinned = featured[tab].map((g) => catalog?.byChar.get(g.char) ?? g);
		const sections = catalog?.[tab === "icons" ? "nerd" : "emoji"] ?? [];
		if (query) {
			const seen = new Set<string>();
			const all: (Glyph | CatalogGlyph)[] = [];
			for (const g of [...pinned, ...sections.flatMap((s) => s.glyphs)]) {
				if (seen.has(g.char)) continue;
				seen.add(g.char);
				all.push(g);
			}
			return rowsOf([{ id: "q", glyphs: searchGlyphs(all, query) }]);
		}
		return rowsOf([
			{
				id: "featured",
				label: t("glyphPicker.section.featured"),
				glyphs: pinned,
			},
			...sections.map((s) => ({
				id: s.id,
				label: t(`glyphPicker.section.${s.id}`),
				glyphs: s.glyphs,
			})),
		]);
	}, [featured, tab, catalog, query, t]);

	const scroller = useRef<HTMLDivElement>(null);
	const virtualizer = useVirtualizer({
		count: rows.length,
		getScrollElement: () => scroller.current,
		estimateSize: () => ROW,
		overscan: 6,
	});
	// 换页或改搜索词：回到顶部
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只按页与搜索词触发
	useEffect(() => {
		if (scroller.current) scroller.current.scrollTop = 0;
	}, [tab, query]);

	const zh = i18n.language.startsWith("zh");
	const first = rows.find((r) => r.kind === "cells")?.glyphs[0];
	const placeholder = t(
		tab === "icons" ? "glyphPicker.searchIcons" : "glyphPicker.searchEmoji",
	);

	return (
		<Popover
			side="bottom"
			onClose={onClose}
			bare
			className="w-[22rem]"
			maxHeight={440}
		>
			<div
				role="dialog"
				aria-label={label}
				className="flex min-h-0 flex-1 flex-col"
			>
				<div className="flex shrink-0 items-center gap-1 border-border border-b px-2 py-1.5">
					{/* biome-ignore lint/a11y/useSemanticElements: 分段按钮（aria-pressed），不是表单字段集 */}
					<div role="group" className="flex items-center gap-0.5">
						{TABS.map((k) => (
							<button
								key={k}
								type="button"
								aria-pressed={tab === k}
								onClick={() => setTab(k)}
								className={`${tabClass(tab === k)} ${tab === k ? "bg-hover" : ""}`}
							>
								{t(`glyphPicker.${k}`)}
							</button>
						))}
					</div>
					{onRemove && removeLabel && (
						<button
							type="button"
							onClick={onRemove}
							className={`${tabClass(false)} ml-auto hover:bg-hover`}
						>
							{removeLabel}
						</button>
					)}
				</div>
				<div className="shrink-0 px-2 pt-2 pb-1">
					{/* 桌面打开即可输入；手机上不自动聚焦，免得软键盘挡住网格 */}
					<TextInput
						type="search"
						autoFocus={!compact}
						aria-label={placeholder}
						placeholder={placeholder}
						value={q}
						onChange={(e) => setQ(e.target.value)}
						onKeyDown={(e) => {
							// 回车选第一个结果
							if (e.key !== "Enter" || !first) return;
							e.preventDefault();
							onPick(first.char);
						}}
					/>
				</div>
				<div
					ref={scroller}
					className="h-72 min-h-0 shrink overflow-y-auto overscroll-contain px-1.5 pb-1.5"
				>
					{rows.length === 0 && query ? (
						<p className="px-1 py-3 text-muted text-sm">
							{t("glyphPicker.noMatch", { q: query })}
						</p>
					) : (
						<div
							className="relative w-full"
							style={{ height: virtualizer.getTotalSize() }}
						>
							{virtualizer.getVirtualItems().map((v) => {
								const row = rows[v.index];
								if (!row) return null;
								return (
									<div
										key={row.key}
										className="absolute top-0 left-0 w-full"
										style={{
											height: ROW,
											transform: `translateY(${v.start}px)`,
										}}
									>
										{row.kind === "head" ? (
											<div className="flex h-full items-end px-1 pb-1 font-medium text-2xs text-faint">
												{row.label}
											</div>
										) : (
											<div className="grid grid-cols-10 gap-0.5">
												{row.glyphs.map((g) => {
													const on = g.char === value;
													const name = (zh && "zh" in g && g.zh) || g.label;
													return (
														<button
															key={g.char}
															type="button"
															title={name}
															aria-label={name}
															aria-pressed={on}
															onClick={() => onPick(g.char)}
															className={`grid h-8 place-items-center rounded-md border text-xl leading-none transition-colors duration-150 ${on ? "border-accent bg-accent-soft text-accent-ink" : "border-transparent text-muted hover:bg-hover hover:text-fg"}`}
														>
															{g.char}
														</button>
													);
												})}
											</div>
										)}
									</div>
								);
							})}
						</div>
					)}
					{!catalog && !query && (
						<p className="px-1 py-2 text-faint text-xs">
							{t(failed ? "glyphPicker.failed" : "glyphPicker.loading")}
						</p>
					)}
				</div>
			</div>
		</Popover>
	);
}
