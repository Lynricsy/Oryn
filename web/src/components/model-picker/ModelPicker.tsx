/**
 * 统一模型选择器（三栏选择台）：首页对象胶囊、线程模型对话框、各设置页的模型选择都用它，不再各写一套。
 *
 * 桌面：左侧分组竖轨（常用 / 对象 / 各厂商，点一下滚到该组，指示条跟随列表滚动）、中间搜索 + 列表、右侧常驻检视栏
 * （高亮项的完整 id、能力、思考级别）、底部页脚（目录过期提示与刷新、键位）。窄屏（compact）：只有搜索与列表，
 * 思考级别收成列表下方的一排分段按钮。
 *
 * 列表依次是：调用方给的「对象」（role、全局助手、跟随默认……，`more` 的收进可展开的「更多角色」）、常用（本机收藏
 * 与最近使用，筛选时隐藏）、按厂商分组的模型。`free` 时输入框里的值目录里没有也能直接用（多出一行「使用 …」）。
 * 键盘：↑↓ 移动高亮，↵ 选高亮项，⌘/Ctrl+1–9 直接选第 n 行；鼠标移动也会移动高亮（检视栏跟着高亮走）。
 *
 * 思考级别（`thinking`）：检视栏按高亮项的模型能力列出级别。点的是已选中项的级别 = 就地改级别；点别的项的级别 =
 * 连同该级别一起选中该项（由调用方的 `onChange` 决定怎么落地）。不给 `thinking` 时检视栏不出级别。
 *
 * DOM 约定（测试与验收脚本依赖）：对象行 `button[data-entry]`（外加调用方给的 `data-*`，如 `data-role`），模型行
 * `button[data-model="provider/id"]`（选中时 `aria-current` + `font-medium`），常用区的同一模型用 `data-model-shortcut`
 * 不重复 `data-model`，自由输入行 `button[data-model-custom]`，思考级别 `radiogroup「思考级别」`。
 */
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	chatModels,
	type ModelCatalog,
	useModelCatalog,
} from "../../api/model-catalog-queries";
import { formatTokens } from "../../lib/format";
import {
	groupByProvider,
	matchesQuery,
	modelHaystack,
	modelSelector,
	modelShortName,
	type PickerModel,
	parseModelInput,
	providerLabel,
} from "../../lib/model-picker";
import {
	noteRecentModel,
	toggleFavoriteModel,
	useModelPrefs,
} from "../../lib/model-prefs";
import type { ModelRef } from "../../lib/model-roles";
import { formatCombo, isApplePlatform } from "../../lib/shortcuts";
import { useBreakpoint } from "../../lib/viewport";
import { Icon } from "../ui/icons";
import { ModelCatalogFooter } from "../ui/ModelCatalogFooter";
import { ModelLogo } from "../ui/ModelLogo";
import { ErrorText, Kbd, Spinner } from "../ui/primitives";
import { LevelGlyph } from "../ui/ThinkingDial";

/** 列表顶部「对象」区的一项：role、全局助手、跟随默认等，由调用方组装。 */
export interface PickerEntry {
	key: string;
	name: string;
	/** 头像 / 图标（装饰，读屏名由 `name` 承担） */
	face: ReactNode;
	/** 解析出的模型：行尾显示它的 logo 与短名，检视栏据此列能力与思考级别 */
	model?: ModelRef;
	/** 行尾的思考级别图标（预设或实际生效的级别） */
	level?: string;
	/** 没有模型时行尾的说明（如「新建助手对话」「未配置」） */
	hint?: string;
	/** 名字后的小徽标（如「默认」） */
	badge?: string;
	/**
	 * 特殊对象的强调标记（全局助手）：主题色的闪光小徽标。`label` 作悬停提示；
	 * `text` 是徽标里的字，名字本身已说明身份时省略（只剩图标，免得重复）
	 */
	mark?: { label: string; text?: string };
	/** 不能选的原因：行置灰、悬停提示 */
	problem?: string;
	/** 收进「更多角色」 */
	more?: boolean;
	/** 额外的 data 属性（键不带 `data-` 前缀），如 `{ role: "Mare" }` */
	data?: Record<string, string>;
}

export type PickTarget =
	| { kind: "entry"; entry: PickerEntry }
	| { kind: "model"; model: PickerModel }
	| { kind: "custom"; value: string };

export interface PickerThinking {
	/** 某项当前（或选中后）生效的级别 */
	value(target: PickTarget): string | undefined;
	/** 某项的预设级别（检视栏标「预设」） */
	preset?(target: PickTarget): string | undefined;
	onChange(target: PickTarget, level: string): void;
}

export const entryKey = (key: string): string => `entry:${key}`;
export const modelKey = (m: ModelRef): string => `model:${modelSelector(m)}`;
export const customKey = (value: string): string => `custom:${value}`;

export function targetKey(t: PickTarget): string {
	if (t.kind === "entry") return entryKey(t.entry.key);
	if (t.kind === "model") return modelKey(t.model);
	return customKey(t.value);
}

/** 对象行与检视栏名字后的强调标记（见 `PickerEntry.mark`）；装饰，读屏名由行名与 `title` 承担。 */
function EntryMark({
	mark,
}: {
	mark: NonNullable<PickerEntry["mark"]>;
}): ReactNode {
	return (
		<span
			aria-hidden
			title={mark.label}
			data-entry-mark=""
			className="inline-flex h-4 shrink-0 items-center gap-0.5 rounded-xs bg-accent-soft px-1 text-2xs text-accent-ink leading-4"
		>
			<Icon name="sparkle" size={10} weight="fill" />
			{mark.text}
		</span>
	);
}

/** 目录里的对话模型 → 选择器的模型行。 */
export function catalogPickerModels(
	catalog: ModelCatalog | undefined,
): PickerModel[] {
	return chatModels(catalog).map((m) => ({
		provider: m.provider,
		id: m.id,
		name: m.name || m.id,
		selector: `${m.provider}/${m.id}`,
		contextWindow: m.contextWindow,
		reasoning: m.reasoning,
		thinking: m.thinking,
		input: m.input,
	}));
}

interface Row {
	key: string;
	target: PickTarget;
	/** 常用区的模型行（与厂商组里的同一模型区分开） */
	shortcut?: boolean;
}

interface Section {
	id: string;
	label: string;
	/** 竖轨上的图标 */
	rail: ReactNode;
	/** 厂商组标题前的 logo */
	logo?: ReactNode;
	rows: Row[];
}

const FREQUENT_LIMIT = 8;

export function ModelPicker({
	entries = [],
	entriesLabel,
	selected,
	models: given,
	free,
	thinking,
	onPick,
	pickLabel,
	initialQuery,
	error,
}: {
	entries?: readonly PickerEntry[];
	/** 对象区的标题；缺省「对象」 */
	entriesLabel?: string;
	/** 选中项的键（`entryKey` / `modelKey` / `customKey`） */
	selected?: string;
	/** 候选模型；缺省 = 中央目录里的对话模型（此时页脚带目录刷新） */
	models?: readonly PickerModel[];
	/** 自由输入：`model` 只认 `provider/id`；`pattern` 认任何非空文本（omp 的模型模式） */
	free?: "model" | "pattern";
	thinking?: PickerThinking;
	onPick(target: PickTarget): void;
	/** 检视栏 ↵ 后面的说明；缺省「选择」 */
	pickLabel?(target: PickTarget): string;
	initialQuery?: string;
	/** 调用方的错误（如选择命令失败），与目录读取失败一起显示 */
	error?: unknown;
}): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const catalog = useModelCatalog(!given);
	const models = useMemo(
		() => given ?? catalogPickerModels(catalog.data),
		[given, catalog.data],
	);
	const prefs = useModelPrefs();
	const [q, setQ] = useState(initialQuery ?? "");
	const [hi, setHi] = useState<string | null>(null);
	const [moreOpen, setMoreOpen] = useState(false);
	const [active, setActive] = useState<string | null>(null);
	const listRef = useRef<HTMLDivElement>(null);
	const rootRef = useRef<HTMLDivElement>(null);
	// 窄屏：打开时把焦点放在选择器本身而不是搜索框，免得软键盘弹起挤掉列表（抽屉对话框见到焦点已在内容里就不再移动）
	useLayoutEffect(() => {
		if (compact) rootRef.current?.focus();
	}, [compact]);
	const sectionRefs = useRef(new Map<string, HTMLElement>());
	const query = q.trim();

	const bySelector = useMemo(
		() => new Map(models.map((m) => [m.selector, m])),
		[models],
	);
	const shown = useMemo(
		() => models.filter((m) => matchesQuery(modelHaystack(m), query)),
		[models, query],
	);
	const visibleEntries = entries.filter((e) =>
		matchesQuery(
			`${e.name} ${e.model ? `${modelSelector(e.model)} ${e.model.name ?? ""}` : ""}`,
			query,
		),
	);
	const mainEntries = visibleEntries.filter((e) => !e.more);
	const moreEntries = visibleEntries.filter((e) => e.more);
	const moreShown =
		moreOpen ||
		!!query ||
		moreEntries.some((e) => entryKey(e.key) === selected);

	// 自由输入行：输入的值目录里没有、也不是某个对象时多出「使用 …」
	let custom: string | undefined;
	if (free === "model") {
		const typed = parseModelInput(query);
		if (typed && !bySelector.has(modelSelector(typed)))
			custom = modelSelector(typed);
	} else if (free === "pattern" && query) {
		if (
			!bySelector.has(query) &&
			!entries.some((e) => e.key === query || e.name === query)
		)
			custom = query;
	}

	const sections: Section[] = [];
	if (custom !== undefined)
		sections.push({
			id: "custom",
			label: t("modelPicker.use", { model: custom }),
			rail: <Icon name="edit" size={16} />,
			rows: [
				{ key: customKey(custom), target: { kind: "custom", value: custom } },
			],
		});
	const entryRows: Row[] = [
		...mainEntries,
		...(moreShown ? moreEntries : []),
	].map((entry) => ({
		key: entryKey(entry.key),
		target: { kind: "entry", entry },
	}));
	if (mainEntries.length + moreEntries.length > 0)
		sections.push({
			id: "entries",
			label: entriesLabel ?? t("modelPicker.entries"),
			rail: <Icon name="user" size={16} />,
			rows: entryRows,
		});
	if (!query) {
		const frequent = [
			...prefs.favorites,
			...prefs.recent.filter((s) => !prefs.favorites.includes(s)),
		]
			.map((s) => bySelector.get(s))
			.filter((m): m is PickerModel => !!m && !m.unavailable)
			.slice(0, FREQUENT_LIMIT);
		if (frequent.length > 0)
			sections.push({
				id: "frequent",
				label: t("modelPicker.frequent"),
				rail: <Icon name="star" size={16} weight="fill" />,
				rows: frequent.map((model) => ({
					key: `shortcut:${model.selector}`,
					target: { kind: "model", model },
					shortcut: true,
				})),
			});
	}
	for (const g of groupByProvider(shown)) {
		const first = g.models[0];
		const logo = (
			<ModelLogo
				provider={g.provider}
				model={first?.id}
				size={16}
				className="text-muted"
			/>
		);
		sections.push({
			id: `provider:${g.provider}`,
			label: providerLabel(g.provider),
			rail: logo,
			logo: (
				<ModelLogo
					provider={g.provider}
					model={first?.id}
					size={13}
					className="text-muted"
				/>
			),
			rows: g.models.map((model) => ({
				key: modelKey(model),
				target: { kind: "model", model },
			})),
		});
	}
	const rows = sections.flatMap((s) => s.rows);
	const pickable = (r: Row) =>
		!(r.target.kind === "entry" && r.target.entry.problem) &&
		!(r.target.kind === "model" && r.target.model.unavailable);
	// 高亮：手动移过的那一行；它不在了（筛选）就落到选中项，再不行落到第一行
	const current =
		rows.find((r) => r.key === hi) ??
		(query ? undefined : rows.find((r) => r.key === selected && !r.shortcut)) ??
		rows[0];

	const pick = (row: Row) => {
		if (!pickable(row)) return;
		if (row.target.kind === "model") noteRecentModel(row.target.model.selector);
		onPick(row.target);
	};

	// 高亮行滚进视野（键盘移动、筛选后）；jsdom 没有 scrollIntoView
	const currentKey = current?.key;
	useEffect(() => {
		if (!currentKey) return;
		const el = listRef.current?.querySelector<HTMLElement>(
			`[data-row="${CSS.escape(currentKey)}"]`,
		);
		el?.scrollIntoView?.({ block: "nearest" });
	}, [currentKey]);

	const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
		if (e.nativeEvent.isComposing) return;
		const at = current ? rows.indexOf(current) : -1;
		if (e.key === "ArrowDown" || e.key === "ArrowUp") {
			e.preventDefault();
			if (rows.length === 0) return;
			const step = e.key === "ArrowDown" ? 1 : -1;
			const next = rows[(at + step + rows.length) % rows.length];
			if (next) setHi(next.key);
		} else if (e.key === "Enter") {
			e.preventDefault();
			if (current) pick(current);
		} else if ((e.metaKey || e.ctrlKey) && !e.altKey) {
			// 数字键按物理键位判（与应用其它快捷键一致，不受键盘布局影响）
			const n = /^Digit([1-9])$/.exec(e.code)?.[1];
			const row = n ? rows[Number(n) - 1] : undefined;
			if (row) {
				e.preventDefault();
				pick(row);
			}
		}
	};

	// 竖轨的指示条跟着列表滚动：最后一个标题已滚到顶部附近的组
	const onScroll = () => {
		const list = listRef.current;
		if (!list) return;
		let id: string | null = null;
		for (const s of sections) {
			const el = sectionRefs.current.get(s.id);
			if (el && el.offsetTop - list.offsetTop <= list.scrollTop + 8) id = s.id;
		}
		setActive(id);
	};
	const jump = (s: Section) => {
		sectionRefs.current.get(s.id)?.scrollIntoView?.({ block: "start" });
		const first = s.rows[0];
		if (first) setHi(first.key);
		setActive(s.id);
	};
	const activeId =
		active ??
		sections.find((s) => s.rows.some((r) => r.key === current?.key))?.id;

	const placeholder =
		free === "pattern"
			? t("modelPicker.searchPattern")
			: free === "model"
				? t("modelPicker.searchFree")
				: t("model.search");

	const resolveModel = (target: PickTarget): PickerModel | undefined => {
		if (target.kind === "model") return target.model;
		const ref =
			target.kind === "entry"
				? target.entry.model
				: parseModelInput(target.value);
		return ref ? bySelector.get(modelSelector(ref)) : undefined;
	};

	const levelsFor = (
		target: PickTarget,
	): { levels: string[]; known: boolean } | null => {
		const value = thinking?.value(target);
		const m = resolveModel(target);
		if (!m) return value ? { levels: [value], known: false } : null;
		if (!m.thinking || m.thinking.length === 0) return null;
		const list = ["auto", ...m.thinking.filter((l) => l !== "auto")];
		if (value && !list.includes(value)) list.unshift(value);
		return { levels: list, known: true };
	};

	let index = 0;
	const renderRow = (row: Row) => {
		const i = index++;
		const isCurrent = row.key === current?.key;
		const isSelected = !row.shortcut && row.key === selected;
		const common = {
			"data-row": row.key,
			"data-highlighted": isCurrent || undefined,
			onMouseMove: () => {
				if (hi !== row.key) setHi(row.key);
			},
		};
		// ⌘/Ctrl+1–9 直接选第 n 行：只在高亮行上画键帽，不让九个键帽挤满列表
		const kbd =
			!compact && isCurrent && i < 9 ? (
				// 键帽不进行按钮的读屏名
				<span aria-hidden className="flex shrink-0">
					<Kbd>
						{formatCombo(
							{ key: String(i + 1), code: `Digit${i + 1}`, mod: true },
							isApplePlatform(),
						)}
					</Kbd>
				</span>
			) : undefined;
		const rowClass = `flex h-8 w-full min-w-0 items-center gap-2 rounded-sm px-2 text-left text-sm transition-colors duration-100 ${isCurrent ? "bg-hover" : ""}`;
		if (row.target.kind === "custom")
			return (
				<li key={row.key} {...common}>
					<button
						type="button"
						data-model-custom={row.target.value}
						className={rowClass}
						onClick={() => pick(row)}
					>
						<Icon name="edit" size={14} className="shrink-0 text-muted" />
						<span className="min-w-0 flex-1 truncate">
							{t("modelPicker.use", { model: row.target.value })}
						</span>
						{kbd}
					</button>
				</li>
			);
		if (row.target.kind === "entry") {
			const e = row.target.entry;
			const data = Object.fromEntries(
				Object.entries(e.data ?? {}).map(([k, v]) => [`data-${k}`, v]),
			);
			return (
				<li key={row.key} {...common}>
					<button
						type="button"
						data-entry={e.key}
						{...data}
						aria-current={isSelected || undefined}
						aria-disabled={e.problem ? true : undefined}
						title={e.problem}
						className={`${rowClass} ${e.problem ? "cursor-not-allowed" : ""}`}
						onClick={() => pick(row)}
					>
						<span
							aria-hidden
							className={`inline-flex w-5 shrink-0 justify-center ${e.problem ? "opacity-40" : ""}`}
						>
							{e.face}
						</span>
						<span
							className={`min-w-0 shrink truncate ${isSelected ? "font-medium" : ""} ${e.problem ? "text-ghost" : ""}`}
						>
							{e.name}
						</span>
						{e.mark && <EntryMark mark={e.mark} />}
						{e.badge && (
							<span className="shrink-0 rounded-xs bg-inset px-1 text-2xs text-muted leading-4">
								{e.badge}
							</span>
						)}
						<span className="min-w-0 flex-1" />
						{e.model ? (
							<span className="flex min-w-0 shrink items-center gap-1 text-faint text-xs">
								<ModelLogo
									model={e.model.id}
									provider={e.model.provider}
									size={12}
								/>
								<span className="min-w-0 truncate">
									{modelShortName(e.model)}
								</span>
							</span>
						) : (
							(e.problem || e.hint) && (
								<span className="min-w-0 shrink truncate text-faint text-xs">
									{e.problem ?? e.hint}
								</span>
							)
						)}
						{/* 思考级别只画图标（读屏名在图标上）；没有级别的行也占位，各行的列对齐 */}
						<span className="inline-flex w-3.5 shrink-0 justify-center text-muted">
							{e.level && <LevelGlyph level={e.level} size={13} />}
						</span>
						{kbd}
						<Icon
							name="check"
							size={13}
							weight="bold"
							aria-hidden
							className={`shrink-0 ${isSelected ? "text-fg" : "invisible"}`}
						/>
					</button>
				</li>
			);
		}
		const m = row.target.model;
		const fav = prefs.favorites.includes(m.selector);
		const dataAttr = row.shortcut
			? { "data-model-shortcut": m.selector }
			: { "data-model": m.selector };
		return (
			<li key={row.key} {...common} className="group relative">
				<button
					type="button"
					{...dataAttr}
					aria-current={isSelected || undefined}
					aria-disabled={m.unavailable ? true : undefined}
					title={m.unavailable}
					className={`${rowClass} pr-8 ${isSelected ? "font-medium" : ""} ${m.unavailable ? "cursor-not-allowed text-ghost" : ""}`}
					onClick={() => pick(row)}
				>
					<ModelLogo
						model={m.id}
						provider={m.provider}
						size={14}
						className={`shrink-0 ${m.unavailable ? "opacity-40" : ""}`}
					/>
					<span className="min-w-0 shrink truncate">{m.name}</span>
					{row.shortcut && (
						<span className="shrink-0 truncate text-faint text-xs">
							{providerLabel(m.provider)}
						</span>
					)}
					<span className="min-w-0 flex-1" />
					{(m.unavailable || m.note) && (
						<span className="min-w-0 shrink truncate text-faint text-xs">
							{m.unavailable ?? m.note}
						</span>
					)}
					{kbd}
					<Icon
						name="check"
						size={13}
						weight="bold"
						aria-hidden
						className={`shrink-0 ${isSelected ? "text-fg" : "invisible"}`}
					/>
				</button>
				{/* 收藏：高亮或已收藏时显示；独立按钮（不嵌在行按钮里） */}
				<button
					type="button"
					aria-label={t(
						fav ? "modelPicker.unfavorite" : "modelPicker.favorite",
						{ model: m.name },
					)}
					aria-pressed={fav}
					tabIndex={-1}
					onClick={() => toggleFavoriteModel(m.selector)}
					className={`absolute top-1 right-1 grid h-6 w-6 place-items-center rounded-xs transition-colors duration-100 hover:bg-active ${fav ? "text-warn" : "text-faint hover:text-fg"} ${fav || isCurrent ? "" : "opacity-0 focus-visible:opacity-100 group-hover:opacity-100"}`}
				>
					<Icon name="star" size={13} weight={fav ? "fill" : "regular"} />
				</button>
			</li>
		);
	};

	const loading = !given && catalog.isPending;
	// 空态只看模型：目录里一个模型都没有时总要说（对象行照常列在上面）；有模型但筛选后整张列表空了才说「没有匹配」
	const empty =
		loading || !(given || catalog.isSuccess)
			? undefined
			: models.length === 0
				? t("model.noModels")
				: rows.length === 0
					? t("model.noMatch", { q: query })
					: undefined;

	const list = (
		<div
			ref={listRef}
			onScroll={onScroll}
			className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain px-1.5 pb-2"
		>
			<ErrorText error={error ?? (given ? undefined : catalog.error)} />
			{loading && (
				<div className="flex items-center gap-2 px-2 py-3 text-muted text-sm">
					<Spinner />
					{t("model.loading")}
				</div>
			)}
			{sections.map((s) => (
				<section
					key={s.id}
					aria-label={s.label}
					ref={(el) => {
						if (el) sectionRefs.current.set(s.id, el);
						else sectionRefs.current.delete(s.id);
					}}
				>
					{s.id !== "custom" && (
						// 吸顶标题：半透明 + 自身模糊，滚过去的行在下面糊开，不叠出清晰重影；底层仍是弹层的毛玻璃
						<h3 className="sticky top-0 z-10 flex h-7 items-center gap-1.5 bg-[color-mix(in_oklab,var(--panel)_70%,transparent)] px-2 font-medium text-2xs text-faint backdrop-blur-md">
							{s.logo}
							<span className="truncate">{s.label}</span>
							{s.id.startsWith("provider:") && (
								<span className="tabular-nums text-ghost">{s.rows.length}</span>
							)}
						</h3>
					)}
					<ul className="flex flex-col gap-px">{s.rows.map(renderRow)}</ul>
					{s.id === "entries" && moreEntries.length > 0 && !query && (
						<button
							type="button"
							aria-expanded={moreShown}
							onClick={() => setMoreOpen((v) => !v)}
							className="mt-px flex h-7 w-full items-center gap-1 rounded-sm px-2 text-2xs text-faint transition-colors duration-100 hover:bg-hover hover:text-fg"
						>
							<Icon
								name="chevronRight"
								size={11}
								className={`transition-transform duration-150 ${moreShown ? "rotate-90" : ""}`}
							/>
							{t("modelPicker.moreRoles", { n: moreEntries.length })}
						</button>
					)}
				</section>
			))}
			{empty && <p className="px-2 py-3 text-muted text-sm">{empty}</p>}
		</div>
	);

	const search = (
		<div className="flex h-11 shrink-0 items-center gap-2 border-border border-b px-3">
			<Icon name="search" size={15} className="shrink-0 text-faint" />
			<input
				// biome-ignore lint/a11y/noAutofocus: 桌面打开即可输入筛选；手机上不自动聚焦，免得软键盘挤掉列表
				autoFocus={!compact}
				aria-label={placeholder}
				placeholder={placeholder}
				value={q}
				onChange={(e) => {
					setQ(e.target.value);
					setHi(null);
					setActive(null);
				}}
				onKeyDown={onKeyDown}
				className="h-full min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-ghost"
			/>
		</div>
	);

	const levelChoice = current ? levelsFor(current.target) : null;
	const levelValue = current ? thinking?.value(current.target) : undefined;
	const levelPreset = current ? thinking?.preset?.(current.target) : undefined;
	const levels =
		thinking && current && levelChoice ? (
			<div
				role="radiogroup"
				aria-label={t("model.thinking")}
				title={levelChoice.known ? undefined : t("modelPicker.levelsUnknown")}
				className={
					compact
						? "flex flex-wrap gap-1 border-border border-t px-2 py-2"
						: "flex flex-col gap-px"
				}
			>
				{levelChoice.levels.map((l) => {
					const on = levelValue === l;
					return (
						// biome-ignore lint/a11y/useSemanticElements: 带图形的级别行，原生 radio 画不出来；读屏语义由 role/aria-checked 提供
						<button
							key={l}
							type="button"
							role="radio"
							aria-checked={on}
							// 「预设」只作描述：读屏名保持级别名本身
							title={levelPreset === l ? t("modelPicker.preset") : undefined}
							onClick={() => thinking.onChange(current.target, l)}
							className={
								compact
									? `inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors duration-150 ${on ? "border-border-strong bg-hover text-fg" : "border-border text-muted hover:bg-hover hover:text-fg"}`
									: `flex h-7 items-center gap-2 rounded-sm px-2 text-left text-sm transition-colors duration-100 ${on ? "bg-accent-soft text-accent-ink" : "text-fg hover:bg-hover"}`
							}
						>
							<span aria-hidden className="flex">
								<LevelGlyph level={l} size={14} />
							</span>
							<span className="min-w-0 flex-1 truncate">
								{t(`model.levels.${l as "auto"}`, { defaultValue: l })}
							</span>
							{!compact && levelPreset === l && (
								<span aria-hidden className="shrink-0 text-2xs text-faint">
									{t("modelPicker.preset")}
								</span>
							)}
							{!compact && on && (
								<Icon
									name="check"
									size={12}
									weight="bold"
									className="shrink-0"
								/>
							)}
						</button>
					);
				})}
			</div>
		) : null;

	if (compact)
		return (
			<div
				ref={rootRef}
				tabIndex={-1}
				data-model-picker=""
				className="flex h-full min-h-0 w-full flex-col outline-none"
			>
				{search}
				{list}
				{levels}
				{!given && (
					<div className="shrink-0 border-border border-t px-2 py-1">
						<ModelCatalogFooter catalog={catalog.data} />
					</div>
				)}
			</div>
		);

	return (
		<div data-model-picker="" className="flex h-full min-h-0 w-full">
			<nav
				aria-label={t("modelPicker.groups")}
				className="flex w-[52px] shrink-0 flex-col items-center gap-1 overflow-y-auto border-border border-r bg-[color-mix(in_oklab,var(--inset)_45%,transparent)] py-2 [scrollbar-width:none]"
			>
				{sections
					.filter((s) => s.id !== "custom")
					.map((s, i, all) => (
						<span key={s.id} className="contents">
							{/* 对象 / 常用 与厂商之间一道分隔 */}
							{s.id.startsWith("provider:") &&
								i > 0 &&
								!all[i - 1]?.id.startsWith("provider:") && (
									<span
										aria-hidden
										className="my-1 h-px w-6 shrink-0 bg-border"
									/>
								)}
							<button
								type="button"
								title={s.label}
								aria-label={s.label}
								aria-current={activeId === s.id || undefined}
								// 竖轨是鼠标的快捷跳转；键盘在搜索框里用 ↑↓ / ⌘1–9，不让竖轨抢走对话框的初始焦点
								tabIndex={-1}
								onClick={() => jump(s)}
								className={`relative grid h-8 w-8 shrink-0 place-items-center rounded-sm transition-colors duration-100 ${activeId === s.id ? "bg-panel text-fg shadow-xs" : "text-muted hover:bg-hover hover:text-fg"}`}
							>
								{s.rail}
								{activeId === s.id && (
									<span
										aria-hidden
										className="absolute top-1.5 -right-[10px] h-5 w-[3px] rounded-full bg-accent"
									/>
								)}
							</button>
						</span>
					))}
			</nav>
			<div className="flex min-w-0 flex-1 flex-col">
				<div className="flex min-h-0 flex-1">
					<div className="flex min-w-0 flex-1 flex-col">
						{search}
						{list}
					</div>
					<aside className="flex w-[220px] shrink-0 flex-col gap-3 overflow-y-auto border-border border-l bg-[color-mix(in_oklab,var(--raised)_40%,transparent)] p-3">
						{current && (
							<Inspector
								target={current.target}
								model={resolveModel(current.target)}
							/>
						)}
						{levels && (
							<div className="flex flex-col gap-1 border-border border-t pt-3">
								<h4 className="px-2 font-medium text-2xs text-faint">
									{t("model.thinking")}
								</h4>
								{levels}
							</div>
						)}
						{thinking &&
							current &&
							!levelChoice &&
							current.target.kind !== "custom" &&
							resolveModel(current.target) && (
								<p className="border-border border-t pt-3 text-faint text-xs">
									{t("modelPicker.noThinking")}
								</p>
							)}
						{current && pickable(current) && (
							<p className="mt-auto flex items-center gap-1.5 border-border border-t pt-3 text-muted text-xs">
								<Kbd>↵</Kbd>
								<span className="min-w-0 truncate">
									{pickLabel?.(current.target) ?? t("modelPicker.pick")}
								</span>
							</p>
						)}
					</aside>
				</div>
				<footer className="flex h-8 shrink-0 items-center gap-3 border-border border-t px-2 text-faint text-xs">
					<span className="min-w-0 flex-1">
						{!given && <ModelCatalogFooter catalog={catalog.data} />}
					</span>
					<span className="flex shrink-0 items-center gap-1">
						<Kbd>↑↓</Kbd>
						{t("modelPicker.keyMove")}
					</span>
					<span className="flex shrink-0 items-center gap-1">
						<Kbd>Esc</Kbd>
						{t("modelPicker.keyClose")}
					</span>
				</footer>
			</div>
		</div>
	);
}

/** 检视栏上半部：高亮项是谁、用哪个模型、模型能做什么。 */
function Inspector({
	target,
	model,
}: {
	target: PickTarget;
	model: PickerModel | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const ref: ModelRef | undefined =
		target.kind === "model"
			? target.model
			: target.kind === "entry"
				? target.entry.model
				: parseModelInput(target.value);
	const caps: string[] = [];
	if (model?.reasoning || (model?.thinking?.length ?? 0) > 0)
		caps.push(t("modelPicker.capThinking"));
	if (model?.input?.includes("image")) caps.push(t("modelPicker.capImage"));
	if (model?.contextWindow)
		caps.push(
			t("modelPicker.capContext", {
				n: formatTokens(model.contextWindow).toUpperCase(),
			}),
		);
	return (
		<div className="flex min-w-0 flex-col gap-1.5">
			{target.kind === "entry" ? (
				<div className="flex min-w-0 items-center gap-2">
					<span aria-hidden className="inline-flex shrink-0">
						{target.entry.face}
					</span>
					<span className="min-w-0 truncate font-semibold text-sm">
						{target.entry.name}
					</span>
					{target.entry.mark && <EntryMark mark={target.entry.mark} />}
				</div>
			) : (
				<div className="flex min-w-0 items-center gap-2">
					{ref ? (
						<ModelLogo model={ref.id} provider={ref.provider} size={18} />
					) : (
						<Icon name="edit" size={16} className="text-muted" />
					)}
					<span className="min-w-0 break-words font-semibold text-sm">
						{target.kind === "model" ? target.model.name : target.value}
					</span>
				</div>
			)}
			{target.kind === "entry" && ref && (
				<div className="flex min-w-0 items-center gap-1.5 text-muted text-xs">
					<ModelLogo model={ref.id} provider={ref.provider} size={12} />
					<span className="min-w-0 truncate">
						{model?.name ?? ref.name ?? ref.id}
					</span>
				</div>
			)}
			{ref && (
				<code className="break-all font-mono text-2xs text-faint leading-4">
					{modelSelector(ref)}
				</code>
			)}
			{target.kind === "entry" &&
				!ref &&
				(target.entry.problem || target.entry.hint) && (
					<p
						className={`text-xs ${target.entry.problem ? "text-err" : "text-faint"}`}
					>
						{target.entry.problem ?? target.entry.hint}
					</p>
				)}
			{model?.unavailable && (
				<p className="text-err text-xs">{model.unavailable}</p>
			)}
			{model?.note && !model.unavailable && (
				<p className="text-faint text-xs">{model.note}</p>
			)}
			{caps.length > 0 && (
				<ul className="flex flex-wrap gap-1">
					{caps.map((c) => (
						<li
							key={c}
							className="rounded-xs border border-border bg-panel px-1.5 text-2xs text-muted leading-[18px]"
						>
							{c}
						</li>
					))}
				</ul>
			)}
		</div>
	);
}
