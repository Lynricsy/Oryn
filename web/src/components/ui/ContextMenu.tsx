/**
 * 统一上下文菜单的宿主：读 `lib/context-menu/store`，渲染到 body。
 * - 浮动菜单（默认）：玻璃面板，按触发点定位并夹在视口内；不移动 DOM 焦点到菜单项（鼠标触发时保住选区高亮），
 *   用高亮下标 + `aria-activedescendant` 表示当前项，键盘在 document 捕获阶段处理。
 * - 底部动作面板（compact 屏且由触屏长按触发）：模态，背景 inert。
 * 子菜单只支持一级：浮动菜单里悬停父项（或点击、→、Enter）即在父项旁弹出子面板，在父项之间移动时稍作停顿再切换，
 * 让指针能斜着移进子面板；底部动作面板没有悬停，沿用原地换页（顶部「返回」）。
 */
import {
	Fragment,
	type ReactNode,
	useEffect,
	useId,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useStore } from "zustand";
import {
	closeContextMenu,
	contextMenuStore,
	type OpenContextMenu,
} from "../../lib/context-menu/store";
import { pushModal } from "../../lib/modal";
import { useBreakpoint } from "../../lib/viewport";
import { Icon } from "./icons";
import type { MenuItem } from "./primitives";

const EDGE = 8;
/** 子菜单已开着时指针移到别的项：等这么久再切换 / 收起，斜着移向子面板途经别的项不至于把它换掉 */
const SUB_SWITCH_MS = 150;

export function ContextMenuHost(): ReactNode {
	const menu = useStore(contextMenuStore, (s) => s.menu);
	const compact = useBreakpoint() === "compact";
	// 每次打开（含替换）都换 key：子菜单、高亮项、位置从头算
	const seq = useRef<{ menu: OpenContextMenu | null; n: number }>({
		menu: null,
		n: 0,
	});
	if (seq.current.menu !== menu) seq.current = { menu, n: seq.current.n + 1 };
	if (!menu) return null;
	return createPortal(
		<MenuBody
			key={seq.current.n}
			menu={menu}
			sheet={compact && menu.trigger === "touch"}
		/>,
		document.body,
	);
}

function firstEnabled(items: MenuItem[], from = 0, step = 1): number {
	const n = items.length;
	for (let k = 0; k < n; k++) {
		const i = (((from + k * step) % n) + n) % n;
		if (!items[i]?.disabled) return i;
	}
	return -1;
}

interface Row {
	item: MenuItem;
	key: string;
}

function MenuBody({
	menu,
	sheet,
}: {
	menu: OpenContextMenu;
	sheet: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const id = useId();
	const panelRef = useRef<HTMLDivElement>(null);
	const subRef = useRef<HTMLDivElement>(null);
	const hostRef = useRef<HTMLDivElement>(null);
	const switchTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
	/** 打开的子菜单：所属父项在扁平列表里的下标 */
	const [sub, setSub] = useState<number | null>(null);
	/** 键盘导航是否落在子菜单里（底部面板原地换页，打开子菜单即落在里面） */
	const [inSub, setInSub] = useState(false);
	const [active, setActive] = useState(() =>
		menu.trigger === "keyboard"
			? firstEnabled(menu.groups.flatMap((g) => g.items))
			: -1,
	);
	const [subActive, setSubActive] = useState(-1);
	const [pos, setPos] = useState({ left: menu.x, top: menu.y });
	const [subPos, setSubPos] = useState<{ left: number; top: number } | null>(
		null,
	);
	const flat: Row[] = menu.groups.flatMap((g) =>
		g.items.map((item) => ({ item, key: `${g.key}/${item.key}` })),
	);
	const items = flat.map((r) => r.item);
	const parent = sub === null ? undefined : items[sub];
	const subRows: Row[] =
		parent?.children?.map((item) => ({ item, key: `sub/${item.key}` })) ?? [];
	const subItems = subRows.map((r) => r.item);

	const cancelSwitch = () => clearTimeout(switchTimer.current);
	const openSub = (index: number, viaKey: boolean) => {
		cancelSwitch();
		const children = items[index]?.children;
		if (!children) return;
		setActive(index);
		setSub(index);
		setInSub(viaKey || sheet);
		setSubActive(viaKey ? firstEnabled(children) : -1);
	};
	const closeSub = () => {
		cancelSwitch();
		setSub(null);
		setInSub(false);
		setSubPos(null);
	};
	const back = () => {
		if (sub !== null) setActive(sub);
		closeSub();
	};
	const select = (item: MenuItem | undefined) => {
		if (!item || item.disabled) return;
		closeContextMenu(menu.trigger === "keyboard");
		item.onSelect?.();
	};
	const choose = (index: number, viaKey: boolean) => {
		const item = items[index];
		if (!item || item.disabled) return;
		if (item.children) openSub(index, viaKey);
		else select(item);
	};
	/** 浮动菜单里指针进入主菜单的一项：父项直接弹出子菜单；子菜单已开着时停顿后再切换或收起 */
	const hoverMain = (index: number) => {
		const item = items[index];
		if (!item) return;
		setInSub(false);
		if (!item.disabled) setActive(index);
		const target = item.children && !item.disabled ? index : null;
		cancelSwitch();
		if (target === sub) return;
		if (sub === null) {
			if (target !== null) openSub(target, false);
			return;
		}
		switchTimer.current = setTimeout(() => {
			if (target === null) closeSub();
			else openSub(target, false);
		}, SUB_SWITCH_MS);
	};
	const enterSub = () => {
		cancelSwitch();
		setInSub(true);
		if (sub !== null) setActive(sub);
	};
	const live = useRef({
		items,
		subItems,
		active,
		subActive,
		sub,
		inSub,
		choose,
		select,
		back,
		openSub,
		closeSub,
	});
	live.current = {
		items,
		subItems,
		active,
		subActive,
		sub,
		inSub,
		choose,
		select,
		back,
		openSub,
		closeSub,
	};

	// 浮动菜单量过实际尺寸后夹进视口：放不下就翻到触发点上方。挪位后重排（贴边时可用宽度变了）
	// 等尺寸变化都要重量，交给 ResizeObserver
	useLayoutEffect(() => {
		const el = panelRef.current;
		if (sheet || !el) return;
		const place = () => {
			const w = el.offsetWidth;
			const h = el.offsetHeight;
			const left = Math.min(
				Math.max(menu.x, EDGE),
				Math.max(EDGE, window.innerWidth - w - EDGE),
			);
			// 放不下就翻到触发点上方；触发点本身贴着底边时再整体上提，下沿也留出 EDGE
			const below = menu.y + h <= window.innerHeight - EDGE;
			const top = below
				? menu.y
				: Math.max(EDGE, Math.min(menu.y - h, window.innerHeight - h - EDGE));
			setPos((p) => (p.left === left && p.top === top ? p : { left, top }));
		};
		place();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(place);
		ro.observe(el);
		return () => ro.disconnect();
	}, [sheet, menu.x, menu.y]);

	// 子面板贴在主菜单右侧、首项与父项对齐（减去内边距）；右边放不下翻到左侧，上下夹进视口。
	// 主菜单挪位（`pos`）后跟着重算
	// biome-ignore lint/correctness/useExhaustiveDependencies: pos 只用作重量的触发
	useLayoutEffect(() => {
		const el = subRef.current;
		const main = panelRef.current;
		if (sheet || sub === null || !el || !main) return;
		const place = () => {
			const box = main.getBoundingClientRect();
			const anchor = document
				.getElementById(`${id}-${sub}`)
				?.getBoundingClientRect();
			const w = el.offsetWidth;
			const h = el.offsetHeight;
			const left =
				box.right - 2 + w <= window.innerWidth - EDGE
					? box.right - 2
					: Math.max(EDGE, box.left - w + 2);
			const top = Math.max(
				EDGE,
				Math.min((anchor?.top ?? box.top) - 4, window.innerHeight - h - EDGE),
			);
			setSubPos((p) =>
				p && p.left === left && p.top === top ? p : { left, top },
			);
		};
		place();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(place);
		ro.observe(el);
		return () => ro.disconnect();
	}, [sheet, sub, id, pos]);

	useLayoutEffect(() => {
		const host = hostRef.current;
		const release = sheet && host ? pushModal(host) : undefined;
		if (menu.trigger === "keyboard" || sheet)
			panelRef.current?.focus({ preventScroll: true });
		return release;
	}, [sheet, menu.trigger]);

	useEffect(() => {
		const inside = (e: Event) =>
			e.target instanceof Node &&
			[panelRef, subRef].some((r) => r.current?.contains(e.target as Node));
		const onDown = (e: Event) => {
			if (!inside(e)) closeContextMenu();
		};
		const onMove = (e: Event) => {
			if (!inside(e)) closeContextMenu();
		};
		const onClose = () => closeContextMenu();
		const onKey = (e: KeyboardEvent) => {
			const {
				items,
				subItems,
				active,
				subActive,
				sub,
				inSub,
				choose,
				select,
				back,
				openSub,
				closeSub,
			} = live.current;
			const list = inSub ? subItems : items;
			const cur = inSub ? subActive : active;
			const nav = (next: number) => {
				e.preventDefault();
				e.stopPropagation();
				if (inSub) setSubActive(next);
				else {
					// 键盘在主菜单里移开父项：悬停弹出的子菜单随之收起
					if (sub !== null && next !== sub) closeSub();
					setActive(next);
				}
			};
			switch (e.key) {
				case "ArrowDown":
					return nav(firstEnabled(list, cur + 1));
				case "ArrowUp":
					return nav(
						firstEnabled(list, cur < 0 ? list.length - 1 : cur - 1, -1),
					);
				case "Home":
					return nav(firstEnabled(list));
				case "End":
					return nav(firstEnabled(list, list.length - 1, -1));
				case "Enter":
				case " ":
					e.preventDefault();
					e.stopPropagation();
					if (cur < 0) return;
					if (inSub) select(list[cur]);
					else choose(cur, true);
					return;
				case "ArrowRight":
					e.preventDefault();
					e.stopPropagation();
					if (!inSub && active >= 0 && items[active]?.children)
						openSub(active, true);
					return;
				case "ArrowLeft":
				case "Backspace":
					e.preventDefault();
					e.stopPropagation();
					if (inSub) back();
					return;
				case "Escape":
				case "Tab":
					e.preventDefault();
					e.stopPropagation();
					closeContextMenu(true);
					return;
				default:
					if (e.ctrlKey || e.metaKey) closeContextMenu();
			}
		};
		document.addEventListener("pointerdown", onDown, true);
		window.addEventListener("wheel", onMove, true);
		window.addEventListener("touchmove", onMove, true);
		window.addEventListener("resize", onClose);
		window.addEventListener("blur", onClose);
		document.addEventListener("keydown", onKey, true);
		return () => {
			clearTimeout(switchTimer.current);
			document.removeEventListener("pointerdown", onDown, true);
			window.removeEventListener("wheel", onMove, true);
			window.removeEventListener("touchmove", onMove, true);
			window.removeEventListener("resize", onClose);
			window.removeEventListener("blur", onClose);
			document.removeEventListener("keydown", onKey, true);
		};
	}, []);

	const rowClass = sheet
		? "min-h-11 px-3 py-2 text-base"
		: "px-2 py-1.5 text-sm";
	const renderRow = (
		item: MenuItem,
		domId: string,
		lit: boolean,
		onEnter: () => void,
		onClick: () => void,
	) => (
		<button
			id={domId}
			type="button"
			{...item.attrs}
			data-menu-key={item.key}
			{...(item.checked === undefined
				? { role: "menuitem" }
				: { role: "menuitemradio", "aria-checked": item.checked })}
			{...(item.children && {
				"aria-haspopup": "menu" as const,
				"aria-expanded": parent === item,
			})}
			disabled={item.disabled}
			title={item.title}
			tabIndex={-1}
			className={`flex w-full items-center gap-2 rounded-md text-left hover:bg-hover disabled:opacity-40 ${rowClass} ${lit ? "bg-hover" : ""} ${item.danger ? "text-err" : "text-fg"} ${item.checked ? "font-medium" : ""}`}
			onMouseDown={(e) => e.preventDefault()}
			onMouseEnter={onEnter}
			onClick={onClick}
		>
			{item.leading ? (
				<span
					aria-hidden
					className="inline-flex w-[14px] shrink-0 justify-center text-muted"
				>
					{item.leading}
				</span>
			) : (
				item.icon && (
					<Icon name={item.icon} size={14} className="shrink-0 text-muted" />
				)
			)}
			<span className="min-w-0 flex-1 truncate">{item.label}</span>
			{item.checked && (
				<Icon
					name="check"
					size={13}
					weight="bold"
					className="shrink-0 text-accent-ink"
				/>
			)}
			{item.hint && <span className="text-faint text-xs">{item.hint}</span>}
			{item.children && (
				<Icon name="chevronRight" size={13} className="text-faint" />
			)}
		</button>
	);
	// 组与组之间画分隔线：记下每组第一项在扁平列表里的下标
	const groupStarts = new Set<number>();
	let offset = 0;
	for (const g of menu.groups) {
		if (offset > 0) groupStarts.add(offset);
		offset += g.items.length;
	}
	const mainList = flat.map(({ item, key }, i) => (
		<Fragment key={key}>
			{groupStarts.has(i) && (
				<hr className="my-1 h-px border-0 bg-[var(--glass-line)]" />
			)}
			{renderRow(
				item,
				`${id}-${i}`,
				i === active || i === sub,
				sheet ? () => !item.disabled && setActive(i) : () => hoverMain(i),
				() => choose(i, false),
			)}
		</Fragment>
	));
	const subList = subRows.map(({ item, key }, j) => (
		<Fragment key={key}>
			{renderRow(
				item,
				`${id}-s${j}`,
				j === subActive,
				() => !item.disabled && setSubActive(j),
				() => select(item),
			)}
		</Fragment>
	));
	const activeId = inSub
		? subActive >= 0
			? `${id}-s${subActive}`
			: undefined
		: active >= 0
			? `${id}-${active}`
			: undefined;
	if (sheet)
		return (
			<div
				ref={hostRef}
				data-testid="context-sheet"
				data-quote-affordance=""
				className="fixed inset-0 z-50 flex items-end"
			>
				<button
					type="button"
					tabIndex={-1}
					aria-label={t("common.close")}
					className="absolute inset-0 bg-scrim"
					onClick={() => closeContextMenu()}
				/>
				<div
					ref={panelRef}
					role="menu"
					tabIndex={-1}
					aria-activedescendant={activeId}
					className="pop-in relative max-h-[70dvh] w-full overflow-y-auto rounded-t-xl bg-panel p-2 pb-safe shadow-xl outline-none ring-1 ring-[var(--glass-line)]"
				>
					{parent ? (
						<>
							<button
								type="button"
								className="flex min-h-11 w-full items-center gap-2 rounded-md px-3 text-faint text-sm hover:bg-hover"
								onMouseDown={(e) => e.preventDefault()}
								onClick={back}
							>
								<Icon name="back" size={13} />
								{parent.label}
							</button>
							{subList}
						</>
					) : (
						mainList
					)}
				</div>
			</div>
		);
	return (
		<>
			<div
				ref={panelRef}
				role="menu"
				tabIndex={-1}
				aria-activedescendant={activeId}
				data-testid="context-menu"
				data-quote-affordance=""
				// 玻璃底画在不滚动的外壳上、列表在内层滚动：外壳本身滚动时 ::before 的模糊底会跟着滚走
				className="glass pop-in fixed z-50 flex min-w-52 max-w-72 flex-col rounded-lg shadow-lg outline-none"
				style={{
					left: pos.left,
					top: pos.top,
					maxHeight: "calc(100dvh - 16px)",
				}}
			>
				<div className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1">
					{mainList}
				</div>
			</div>
			{parent?.children && (
				<div
					key={sub}
					ref={subRef}
					role="menu"
					aria-labelledby={`${id}-${sub}`}
					data-testid="context-submenu"
					data-quote-affordance=""
					className="glass pop-in fixed z-50 flex min-w-44 max-w-72 flex-col rounded-lg shadow-lg outline-none"
					style={{
						left: subPos?.left ?? 0,
						top: subPos?.top ?? 0,
						visibility: subPos ? undefined : "hidden",
						maxHeight: "calc(100dvh - 16px)",
					}}
					onMouseEnter={enterSub}
				>
					<div className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1">
						{subList}
					</div>
				</div>
			)}
		</>
	);
}
