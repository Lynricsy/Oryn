/**
 * 统一上下文菜单的宿主：读 `lib/context-menu/store`，渲染到 body。
 * - 浮动菜单（默认）：玻璃面板，按触发点定位并夹在视口内；不移动 DOM 焦点到菜单项（鼠标触发时保住选区高亮），
 *   用高亮下标 + `aria-activedescendant` 表示当前项，键盘在 document 捕获阶段处理。
 * - 底部动作面板（compact 屏且由触屏长按触发）：模态，背景 inert。
 * 子菜单沿用 `Menu` 的原地换页，只支持一级。
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
	const hostRef = useRef<HTMLDivElement>(null);
	/** 子菜单：所属父项在扁平列表里的下标 */
	const [sub, setSub] = useState<number | null>(null);
	const [active, setActive] = useState(() =>
		menu.trigger === "keyboard"
			? firstEnabled(menu.groups.flatMap((g) => g.items))
			: -1,
	);
	const [pos, setPos] = useState({ left: menu.x, top: menu.y });
	const flat = menu.groups.flatMap((g) =>
		g.items.map((item) => ({ item, key: `${g.key}/${item.key}` })),
	);
	const parent = sub === null ? undefined : flat[sub]?.item;
	const rows = parent?.children
		? parent.children.map((item) => ({ item, key: `sub/${item.key}` }))
		: flat;
	const items = rows.map((r) => r.item);

	const choose = (index: number) => {
		const item = items[index];
		if (!item || item.disabled) return;
		if (item.children && sub === null) {
			setSub(index);
			setActive(menu.trigger === "keyboard" ? firstEnabled(item.children) : -1);
			return;
		}
		closeContextMenu(menu.trigger === "keyboard");
		item.onSelect?.();
	};
	const back = () => {
		setActive(sub ?? -1);
		setSub(null);
	};
	const live = useRef({ items, active, sub, choose, back });
	live.current = { items, active, sub, choose, back };

	// 浮动菜单量过实际尺寸后夹进视口：放不下就翻到触发点上方。换页（`sub`）、挪位后重排（贴边时
	// 可用宽度变了）等尺寸变化都要重量，交给 ResizeObserver
	// biome-ignore lint/correctness/useExhaustiveDependencies: sub 只用作重量的触发
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
	}, [sheet, menu.x, menu.y, sub]);

	useLayoutEffect(() => {
		const host = hostRef.current;
		const release = sheet && host ? pushModal(host) : undefined;
		if (menu.trigger === "keyboard" || sheet)
			panelRef.current?.focus({ preventScroll: true });
		return release;
	}, [sheet, menu.trigger]);

	useEffect(() => {
		const inside = (e: Event) =>
			e.target instanceof Node && !!panelRef.current?.contains(e.target);
		const onDown = (e: Event) => {
			if (!inside(e)) closeContextMenu();
		};
		const onMove = (e: Event) => {
			if (!inside(e)) closeContextMenu();
		};
		const onClose = () => closeContextMenu();
		const onKey = (e: KeyboardEvent) => {
			const { items, active, sub, choose, back } = live.current;
			const nav = (next: number) => {
				e.preventDefault();
				e.stopPropagation();
				setActive(next);
			};
			switch (e.key) {
				case "ArrowDown":
					return nav(firstEnabled(items, active + 1));
				case "ArrowUp":
					return nav(
						firstEnabled(items, active < 0 ? items.length - 1 : active - 1, -1),
					);
				case "Home":
					return nav(firstEnabled(items));
				case "End":
					return nav(firstEnabled(items, items.length - 1, -1));
				case "Enter":
				case " ":
					e.preventDefault();
					e.stopPropagation();
					if (active >= 0) choose(active);
					return;
				case "ArrowRight":
					e.preventDefault();
					e.stopPropagation();
					if (sub === null && active >= 0 && items[active]?.children)
						choose(active);
					return;
				case "ArrowLeft":
				case "Backspace":
					e.preventDefault();
					e.stopPropagation();
					if (sub !== null) back();
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
	// 组与组之间画分隔线：记下每组第一项在扁平列表里的下标
	const groupStarts = new Set<number>();
	let offset = 0;
	for (const g of menu.groups) {
		if (offset > 0) groupStarts.add(offset);
		offset += g.items.length;
	}
	const list = (
		<>
			{parent && (
				<button
					type="button"
					className={`flex w-full items-center gap-2 rounded-md text-faint hover:bg-hover ${sheet ? "min-h-11 px-3 text-sm" : "px-2 py-1.5 text-xs"}`}
					onMouseDown={(e) => e.preventDefault()}
					onClick={back}
				>
					<Icon name="back" size={13} />
					{parent.label}
				</button>
			)}
			{rows.map(({ item, key }, i) => (
				<Fragment key={key}>
					{!parent && groupStarts.has(i) && (
						<hr className="my-1 h-px border-0 bg-[var(--glass-line)]" />
					)}
					<button
						id={`${id}-${i}`}
						type="button"
						{...item.attrs}
						data-menu-key={item.key}
						{...(item.checked === undefined
							? { role: "menuitem" }
							: { role: "menuitemradio", "aria-checked": item.checked })}
						disabled={item.disabled}
						title={item.title}
						tabIndex={-1}
						className={`flex w-full items-center gap-2 rounded-md text-left hover:bg-hover disabled:opacity-40 ${rowClass} ${i === active ? "bg-hover" : ""} ${item.danger ? "text-err" : "text-fg"} ${item.checked ? "font-medium" : ""}`}
						onMouseDown={(e) => e.preventDefault()}
						onMouseEnter={() => !item.disabled && setActive(i)}
						onClick={() => choose(i)}
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
								<Icon
									name={item.icon}
									size={14}
									className="shrink-0 text-muted"
								/>
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
						{item.hint && (
							<span className="text-faint text-xs">{item.hint}</span>
						)}
						{item.children && (
							<Icon name="chevronRight" size={13} className="text-faint" />
						)}
					</button>
				</Fragment>
			))}
		</>
	);
	const activeId = active >= 0 ? `${id}-${active}` : undefined;
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
					{list}
				</div>
			</div>
		);
	return (
		<div
			ref={panelRef}
			role="menu"
			tabIndex={-1}
			aria-activedescendant={activeId}
			data-testid="context-menu"
			data-quote-affordance=""
			className="glass pop-in fixed z-50 min-w-52 max-w-72 overflow-y-auto rounded-lg p-1 shadow-lg outline-none"
			style={{
				left: pos.left,
				top: pos.top,
				maxHeight: "calc(100dvh - 16px)",
			}}
		>
			{list}
		</div>
	);
}
