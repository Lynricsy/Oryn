/**
 * 桌面任务栏（M25）：舞台正下方居中的一条胶囊，按 `orb.desktop.apps` 的顺序放应用图标，
 * 有不属于任何应用的窗口时末尾再加一个「其它窗口」。旧 orbd 不上报 `apps`，此时不渲染（面板保留舞台中央的「启动浏览器」）。
 *
 * 图标取自 orb（`/api/v1/orbs/{id}/desktop/apps/{app}/icon`，同源 Cookie），加载失败换成占位图标。
 * 运行中的应用图标下有一个中性色圆点，`launching` 时叠一个转圈，拥有活动窗口的应用底色加深（比悬停与菜单展开深一档）。
 * 窗口菜单里活动窗口行首打勾；同名窗口在标题后加淡色序号以便区分。
 *
 * 点击：没在运行 → 启动；一个窗口 → 活动且没最小化就最小化，否则切到前台；多个窗口 → 弹出窗口菜单
 * （每行一个窗口：点击切到前台、行尾关闭；末行「新建窗口」）。右键、长按或 Shift+F10 在任意运行中的图标上打开同一个菜单；
 * 「其它窗口」点击一律弹菜单。agent 正在操作桌面（`controlLocked`）时，任何操作先确认接管、接管成功后再执行。
 *
 * 键盘：任务栏是 toolbar，只有一个格子在 Tab 序列里，左右方向键（Home / End）在格子间移动焦点。
 */
import {
	Fragment,
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Orb, Schemas } from "../../api/client";
import { useOrbDesktopApp, useOrbDesktopWindow } from "../../api/queries";
import { confirmAction } from "../../lib/confirm";
import { useBreakpoint } from "../../lib/viewport";
import { Floating } from "../ui/Floating";
import { Icon } from "../ui/icons";
import { IconButton, Spinner } from "../ui/primitives";

type App = Schemas["OrbDesktopApp"];
type Win = Schemas["OrbDesktopWindow"];
type WindowAction = Schemas["OrbDesktopWindowAction"];

/** 任务栏上的一格：一个应用，或末尾的「其它窗口」。 */
type Slot =
	| { kind: "app"; key: string; app: App; wins: Win[] }
	| { kind: "other"; key: string; wins: Win[] };

const OTHER_KEY = "other";
/** 触屏长按多久打开窗口菜单。 */
const LONG_PRESS_MS = 500;

function slotsOf(desktop: Orb["desktop"]): Slot[] {
	const apps = desktop?.apps ?? [];
	const windows = desktop?.windows ?? [];
	const known = new Set(apps.map((a) => a.id));
	const slots: Slot[] = apps.map((app) => ({
		kind: "app",
		key: app.id,
		app,
		wins: windows.filter((w) => w.appId === app.id),
	}));
	const others = windows.filter((w) => !w.appId || !known.has(w.appId));
	if (others.length > 0)
		slots.push({ kind: "other", key: OTHER_KEY, wins: others });
	return slots;
}

function DockButton({
	slot,
	orbId,
	size,
	focusable,
	expanded,
	onPress,
	onMenu,
	onFocus,
	buttonRef,
}: {
	slot: Slot;
	orbId: string;
	size: number;
	focusable: boolean;
	expanded: boolean;
	onPress: () => void;
	onMenu: () => void;
	onFocus: () => void;
	buttonRef: (el: HTMLButtonElement | null) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [broken, setBroken] = useState(false);
	// 长按：计时器与「已触发」标记（触发后吞掉随后的那次 click）
	const press = useRef<{
		timer?: ReturnType<typeof setTimeout>;
		fired: boolean;
	}>({ fired: false });
	useEffect(() => () => clearTimeout(press.current.timer), []);
	const cancelPress = () => clearTimeout(press.current.timer);

	const name = slot.kind === "app" ? slot.app.name : t("deskPanel.dock.others");
	const running = slot.kind === "other" || slot.app.running;
	const launching = slot.kind === "app" && slot.app.launching;
	const active = slot.wins.some((w) => w.active && !w.minimized);
	// 有窗口可列（运行中的应用、「其它窗口」）时才有菜单
	const menu = running || slot.wins.length > 0;

	return (
		<button
			ref={buttonRef}
			type="button"
			tabIndex={focusable ? 0 : -1}
			title={name}
			aria-label={running ? t("deskPanel.dock.runningName", { name }) : name}
			aria-haspopup={menu ? "menu" : undefined}
			aria-expanded={menu ? expanded : undefined}
			data-testid={`dock-${slot.key}`}
			data-active={active || undefined}
			className={`relative grid size-9 shrink-0 select-none place-items-center rounded-full transition-colors duration-150 ${active ? "bg-active" : expanded ? "bg-hover" : "hover:bg-hover"}`}
			onFocus={onFocus}
			onClick={() => {
				if (press.current.fired) {
					press.current.fired = false;
					return;
				}
				onPress();
			}}
			onContextMenu={(e) => {
				e.preventDefault();
				if (menu) onMenu();
			}}
			onPointerDown={(e) => {
				if (e.pointerType === "mouse") return;
				cancelPress();
				press.current.fired = false;
				press.current.timer = setTimeout(() => {
					press.current.fired = true;
					if (menu) onMenu();
				}, LONG_PRESS_MS);
			}}
			onPointerUp={cancelPress}
			onPointerLeave={cancelPress}
			onPointerCancel={cancelPress}
			onKeyDown={(e) => {
				if (e.key !== "F10" || !e.shiftKey) return;
				e.preventDefault();
				if (menu) onMenu();
			}}
		>
			<span
				className={`grid place-items-center ${launching ? "opacity-50" : ""}`}
			>
				{slot.kind === "app" && !broken ? (
					<img
						src={`/api/v1/orbs/${encodeURIComponent(orbId)}/desktop/apps/${encodeURIComponent(slot.app.id)}/icon`}
						alt={name}
						width={size}
						height={size}
						draggable={false}
						className="pointer-events-none [-webkit-touch-callout:none]"
						onError={() => setBroken(true)}
					/>
				) : (
					<Icon
						name="appWindow"
						size={size}
						className="text-muted"
						data-testid="dock-icon-fallback"
					/>
				)}
			</span>
			{launching && (
				<span className="absolute inset-0 grid place-items-center text-muted">
					<Spinner size={14} />
				</span>
			)}
			{running && (
				<span
					aria-hidden
					data-testid="dock-running-dot"
					className="absolute bottom-0.5 left-1/2 size-1 -translate-x-1/2 rounded-full bg-muted"
				/>
			)}
		</button>
	);
}

/** 窗口菜单：方向键在菜单项间移动，Esc 关闭并把焦点还给任务栏图标。 */
function onMenuKey(e: KeyboardEvent<HTMLDivElement>, close: () => void) {
	if (e.key === "Escape") {
		e.preventDefault();
		close();
		return;
	}
	if (e.key === "Tab") {
		close();
		return;
	}
	const items = [
		...e.currentTarget.querySelectorAll<HTMLElement>('[role="menuitem"]'),
	];
	const i = items.indexOf(document.activeElement as HTMLElement);
	const next =
		e.key === "ArrowDown"
			? (i + 1) % items.length
			: e.key === "ArrowUp"
				? (i - 1 + items.length) % items.length
				: e.key === "Home"
					? 0
					: e.key === "End"
						? items.length - 1
						: -1;
	if (next < 0) return;
	e.preventDefault();
	items[next]?.focus();
}

export function DesktopDock({
	orb,
	controlLocked,
	onTakeOver,
	onError,
}: {
	orb: Orb;
	/** agent 正在操作桌面（`control.state == agent`）：操作前先确认接管 */
	controlLocked: boolean;
	/** 接管桌面（面板横幅上的同一个动作）；失败时抛出，原操作不再执行 */
	onTakeOver: () => Promise<void>;
	/** 操作出错时给出错误（面板显示在横幅区），每次操作开始时以 `null` 清除 */
	onError: (error: unknown) => void;
}): ReactNode {
	const { t } = useTranslation();
	const iconSize = useBreakpoint() === "compact" ? 20 : 22;
	const launch = useOrbDesktopApp(orb.id);
	const operate = useOrbDesktopWindow(orb.id);
	const [menu, setMenu] = useState<string | null>(null);
	const [focusIdx, setFocusIdx] = useState(0);
	const buttons = useRef(new Map<string, HTMLButtonElement>());
	const anchor = useRef<HTMLButtonElement | null>(null);
	const menuRef = useRef<HTMLDivElement>(null);

	const slots = slotsOf(orb.desktop);
	const open = menu ? slots.find((s) => s.key === menu) : undefined;
	// 菜单所属的格子已消失（窗口全关了）：收起，免得它重新出现时菜单自己弹出来
	if (menu && !open) setMenu(null);

	useEffect(() => {
		if (!menu) return;
		menuRef.current
			?.querySelector<HTMLElement>('[role="menuitem"]')
			?.focus({ preventScroll: true });
		const onDown = (e: MouseEvent) => {
			const target = e.target as Node;
			// 触发钮自己的点击交给 onClick 开合
			if (menuRef.current?.contains(target) || anchor.current?.contains(target))
				return;
			setMenu(null);
		};
		document.addEventListener("mousedown", onDown);
		return () => document.removeEventListener("mousedown", onDown);
	}, [menu]);

	if (slots.length === 0) return null;

	const closeMenu = (refocus: boolean) => {
		setMenu(null);
		if (refocus) anchor.current?.focus({ preventScroll: true });
	};
	const openMenu = (key: string) => {
		anchor.current = buttons.current.get(key) ?? null;
		setMenu(key);
	};

	const run = async (op: () => Promise<unknown>) => {
		try {
			if (controlLocked) {
				const ok = await confirmAction({
					title: t("deskPanel.dock.takeConfirm"),
					body: t("deskPanel.dock.takeConfirmBody"),
					action: t("deskPanel.control.take"),
				});
				if (!ok) return;
				await onTakeOver();
			}
			onError(null);
			await op();
		} catch (e) {
			onError(e);
		}
	};
	const doLaunch = (appId: string) => run(() => launch.mutateAsync(appId));
	const doWindow = (windowId: string, action: WindowAction) =>
		run(() => operate.mutateAsync({ windowId, action }));

	const press = (slot: Slot) => {
		if (slot.kind === "other" || slot.wins.length > 1) {
			if (menu === slot.key) closeMenu(false);
			else openMenu(slot.key);
			return;
		}
		const [win] = slot.wins;
		if (win)
			void doWindow(
				win.id,
				win.active && !win.minimized ? "minimize" : "activate",
			);
		else if (!slot.app.launching) void doLaunch(slot.app.id);
	};

	const current = Math.min(focusIdx, slots.length - 1);
	const onToolbarKey = (e: KeyboardEvent<HTMLDivElement>) => {
		const n = slots.length;
		const next =
			e.key === "ArrowRight"
				? (current + 1) % n
				: e.key === "ArrowLeft"
					? (current - 1 + n) % n
					: e.key === "Home"
						? 0
						: e.key === "End"
							? n - 1
							: -1;
		if (next < 0) return;
		e.preventDefault();
		setFocusIdx(next);
		const slot = slots[next];
		if (slot) buttons.current.get(slot.key)?.focus();
	};

	const openName = open
		? open.kind === "app"
			? open.app.name
			: t("deskPanel.dock.others")
		: "";

	return (
		<div
			data-testid="desktop-dock"
			className="flex shrink-0 justify-center px-2 pb-2"
		>
			<div
				role="toolbar"
				aria-label={t("deskPanel.dock.label")}
				aria-orientation="horizontal"
				onKeyDown={onToolbarKey}
				className="focus-inset flex h-11 max-w-full items-center gap-1 overflow-x-auto rounded-full border border-border bg-panel px-1 shadow-xs [scrollbar-width:none]"
			>
				{slots.map((slot, i) => (
					<Fragment key={slot.key}>
						{slot.kind === "other" && (
							<span
								aria-hidden
								className="mx-0.5 h-5 w-px shrink-0 bg-border"
							/>
						)}
						<DockButton
							slot={slot}
							orbId={orb.id}
							size={iconSize}
							focusable={i === current}
							expanded={menu === slot.key}
							onPress={() => press(slot)}
							onMenu={() => openMenu(slot.key)}
							onFocus={() => setFocusIdx(i)}
							buttonRef={(el) => {
								if (el) buttons.current.set(slot.key, el);
								else buttons.current.delete(slot.key);
							}}
						/>
					</Fragment>
				))}
			</div>
			{open && (
				<Floating
					key={open.key}
					ref={menuRef}
					anchor={anchor}
					side="top"
					// 锚点是胶囊里的图标：再让出胶囊的内边距与描边，菜单与任务栏之间留 8px
					gap={12}
					role="menu"
					aria-label={openName}
					data-testid="desktop-dock-menu"
					className="w-max min-w-52 max-w-72"
					bodyClassName="p-1"
					onKeyDown={(e) => onMenuKey(e, () => closeMenu(true))}
				>
					{open.wins.map((w) => {
						const title = w.title || t("deskPanel.dock.untitled");
						const same = open.wins.filter(
							(x) => (x.title || t("deskPanel.dock.untitled")) === title,
						);
						const ordinal = same.length > 1 ? same.indexOf(w) + 1 : 0;
						const label = ordinal ? `${title} · ${ordinal}` : title;
						const current = w.active && !w.minimized;
						return (
							<div key={w.id} className="flex items-center gap-0.5">
								<button
									type="button"
									role="menuitem"
									aria-current={current || undefined}
									className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left text-fg text-sm hover:bg-hover"
									onClick={() => {
										closeMenu(true);
										void doWindow(w.id, "activate");
									}}
								>
									<span className="grid w-3.5 shrink-0 place-items-center">
										{current && (
											<Icon name="check" size={14} className="text-muted" />
										)}
									</span>
									<span className="min-w-0 flex-1 truncate">
										{title}
										{ordinal > 0 && (
											<span className="text-faint"> · {ordinal}</span>
										)}
									</span>
									{w.minimized && (
										<span className="shrink-0 text-faint text-xs">
											{t("deskPanel.dock.minimized")}
										</span>
									)}
								</button>
								<IconButton
									role="menuitem"
									icon="close"
									box="sm"
									size={13}
									label={t("deskPanel.dock.closeWindow", { title: label })}
									onClick={() => {
										closeMenu(true);
										void doWindow(w.id, "close");
									}}
								/>
							</div>
						);
					})}
					{open.kind === "app" && (
						<div
							className={
								open.wins.length > 0 ? "mt-1 border-border border-t pt-1" : ""
							}
						>
							<button
								type="button"
								role="menuitem"
								className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-fg text-sm hover:bg-hover"
								onClick={() => {
									closeMenu(true);
									void doLaunch(open.app.id);
								}}
							>
								<Icon name="plus" size={14} className="shrink-0 text-muted" />
								{t("deskPanel.dock.newWindow")}
							</button>
						</div>
					)}
				</Floating>
			)}
		</div>
	);
}
