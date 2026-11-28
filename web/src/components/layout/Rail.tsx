/**
 * iPhone Duo 竖栏（`lib/rail.ts`，壳报告系统把工具栏竖排在一侧时由 AppShell 渲染）：固定在壳给的竖栏矩形里，不随侧栏开合、
 * 卡片移动。上面一根玻璃胶囊是页头的按钮：开关侧栏（取代各页页头里带 `data-sidebar-toggle` 的那个，竖栏在时它们被隐藏）
 * 与当前页头登记的操作（`useRailActions`，如线程页的树、信息面板、更多，手机线程页的视图菜单）；下面是竖排的分区。
 * 页头里的侧栏开关一隐藏，原生页头量不到侧栏钮就不会接管（`useSlotFrame` 为 null），页头整条退回网页画。
 * 原生竖栏（`lib/native-rail.ts`，壳 bridge ≥ 13、iOS 27.1+）：每次渲染合成一份 spec 交给控制器，原生用 Liquid Glass 画、
 * 自己按相机中线摆；原生可用时这里只留一块透明占位（遮挡探测：网页的对话框、遮罩盖到这一列时原生隐藏；接管后带
 * `data-native`），点按经 `host.onRailTap` 回来走与网页按钮同一套回调。没有原生竖栏、或 `set` 被拒时画网页版本。
 */
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useLocation } from "react-router";
import { host } from "../../lib/host";
import { findMenuItem, useHeaderTheme } from "../../lib/native-header";
import {
	buildRailSpec,
	nativeRail,
	useNativeRailAvailable,
	useNativeRailEngaged,
} from "../../lib/native-rail";
import { buildTabItems, COVER_POLL_MS } from "../../lib/native-tab-bar";
import {
	type RailAction,
	railAction,
	useRail,
	useRailActionList,
} from "../../lib/rail";
import { SECTION_ORDER } from "../../lib/sections";
import { uiStore, useSidebarShown } from "../../state/ui";
import { Icon } from "../ui/icons";
import { Menu, type MenuItem } from "../ui/primitives";
import {
	SectionTabBar,
	useAvatarPng,
	useSectionTabs,
	useSectionTap,
} from "./SectionTabBar";

export function Rail(): ReactNode {
	const { t } = useTranslation();
	const actions = useRailActionList();
	const available = useNativeRailAvailable();
	const engaged = useNativeRailEngaged();
	const sidebarShown = useSidebarShown();
	const { pathname } = useLocation();
	const tabs = useSectionTabs();
	const tap = useSectionTap(true);
	const probeRef = useRef<HTMLSpanElement>(null);
	const slotRef = useRef<HTMLDivElement>(null);
	const theme = useHeaderTheme(probeRef);
	const avatarPng = useAvatarPng(available ? tabs.avatarImage : undefined);
	const covered = useRailCover(slotRef, engaged);
	const keyRef = useRef(pathname);
	keyRef.current = pathname;

	// 每次渲染交一份（内容没变控制器不重发）；调色板还没读到时先不发
	useEffect(() => {
		if (!available || !theme) return;
		nativeRail.setSpec(
			buildRailSpec({
				key: pathname,
				dark: theme.dark,
				palette: theme.palette,
				covered,
				lead: { label: t("nav.sidebar"), active: sidebarShown },
				actions,
				tabs: {
					label: t("tabBar.label"),
					items: buildTabItems({
						labels: tabs.labels,
						assistant: {
							image: avatarPng,
							glyph: tabs.avatarIcon || null,
							unread: tabs.assistantUnread,
						},
						conn: tabs.conn === "open" ? null : tabs.connTone,
					}),
					selected: tabs.section,
				},
			}),
		);
	});
	// 不竖排了（展开竖放、离开 App 外壳）：撤掉原生竖栏
	useEffect(() => () => nativeRail.setSpec(null), []);

	// 原生点按：与网页按钮同一套回调；换页前的晚到点按（key 不同）丢弃
	useEffect(
		() =>
			host.onRailTap((e) => {
				if (e.key !== keyRef.current) return;
				if (e.part === "lead") uiStore.getState().toggleSidebar();
				else if (e.part === "action") railAction(e.id)?.onClick?.();
				else if (e.part === "item")
					findMenuItem(railAction(e.id)?.menu ?? [], e.item)?.onSelect?.();
				else {
					const s = SECTION_ORDER.find((k) => k === e.id);
					if (s) tap(s);
				}
			}),
		[tap],
	);

	return (
		<div
			data-testid="rail"
			data-native={engaged ? "" : undefined}
			className="pointer-events-none fixed top-[var(--rail-y)] left-[var(--rail-x)] z-40 flex h-[var(--rail-h)] w-[var(--rail-w)] flex-col items-center gap-2 overflow-y-auto pt-1"
		>
			{/* 原生竖栏的调色板：读这几个元素的计算颜色（useHeaderTheme） */}
			<span ref={probeRef} hidden>
				<i data-k="accent" className="text-accent-ink" />
				<i data-k="brand" style={{ color: "var(--brand)" }} />
				<i data-k="err" className="text-err" />
				<i data-k="warn" className="text-warn" />
				<i data-k="good" style={{ color: "var(--good)" }} />
				<i data-k="frost" style={{ color: "var(--frost)" }} />
			</span>
			{available ? (
				// 原生可用：网页不画（竖栏只在 iOS 27.1+ 出现，那里原生一定在，先画网页版只会闪一下）。原生被拒后 available
				// 变假，退回网页竖栏。透明占位接住按钮之间的点按（什么也不做），也是遮挡探测的对象
				<div
					ref={slotRef}
					aria-hidden
					data-testid="rail-slot"
					className="pointer-events-auto absolute inset-0"
				/>
			) : (
				<>
					<div className="glass pointer-events-auto flex w-[52px] shrink-0 flex-col items-center gap-1 rounded-full p-1">
						<button
							type="button"
							data-testid="rail-sidebar"
							aria-label={t("nav.sidebar")}
							title={t("nav.sidebar")}
							aria-pressed={sidebarShown}
							className={`inline-flex h-11 w-11 items-center justify-center rounded-full transition-colors duration-150 hover:bg-hover hover:text-fg ${sidebarShown ? "text-accent-ink" : "text-muted"}`}
							onClick={() => uiStore.getState().toggleSidebar()}
						>
							<Icon name="sidebar" size={19} />
						</button>
						{actions.length > 0 && (
							<div
								data-testid="rail-actions"
								className="flex flex-col items-center gap-1"
							>
								{actions.map((a) => (
									<RailButton key={a.id} action={a} />
								))}
							</div>
						)}
					</div>
					<SectionTabBar tone="rail" />
				</>
			)}
		</div>
	);
}

/** 菜单项的点按都转到最新登记的那份（竖栏的操作只在形状变了时换新，回调可能是旧线程的）。 */
function liveItems(
	id: string,
	items: readonly MenuItem[],
	prefix = "",
): MenuItem[] {
	return items.map((it) => {
		const path = prefix + it.key;
		return {
			...it,
			onSelect: () =>
				findMenuItem(railAction(id)?.menu ?? [], path)?.onSelect?.(),
			children: it.children && liveItems(id, it.children, `${path}/`),
		};
	});
}

type TriggerProps = Parameters<Parameters<typeof Menu>[0]["trigger"]>[0];

/** 网页竖栏里的一个页头操作：有菜单的是菜单钮（朝屏幕内侧展开），否则普通钮；回调取最新登记的那份。 */
function RailButton({ action: a }: { action: RailAction }): ReactNode {
	const side = useRail()?.side;
	const tone = a.active ? "text-accent-ink" : "text-muted";
	const button = (p?: TriggerProps) => (
		<button
			type="button"
			data-rail-action={a.id}
			aria-label={a.label}
			title={a.label}
			aria-pressed={a.menu ? undefined : (a.active ?? undefined)}
			className={`relative inline-flex h-11 w-11 items-center justify-center rounded-full transition-colors duration-150 hover:bg-hover hover:text-fg ${tone}`}
			onClick={a.menu ? undefined : () => railAction(a.id)?.onClick?.()}
			{...p}
		>
			<Icon name={a.icon} size={19} />
			{a.dot && (
				<span
					aria-hidden
					className={`absolute top-2 right-2 h-2 w-2 rounded-full ring-2 ring-[var(--panel)] ${a.dot === "warn" ? "bg-warn" : "bg-accent"}`}
				/>
			)}
		</button>
	);
	return a.menu ? (
		<Menu
			items={liveItems(a.id, a.menu)}
			align={side === "left" ? "left" : "right"}
			trigger={(p) => button(p)}
		/>
	) : (
		button()
	);
}

/**
 * 原生接管时探测占位有没有被网页内容盖住（对话框、命令面板、遮罩）：竖栏一列上中下三点最上面的元素不是占位就算盖住。
 * 时机同原生页头：生效时一次；`document.body` 的直接子节点增删、抬手、松键之后下一帧；另每 COVER_POLL_MS 兜底。
 */
function useRailCover(
	slot: RefObject<HTMLElement | null>,
	on: boolean,
): boolean {
	const [covered, setCovered] = useState(false);
	useLayoutEffect(() => {
		if (!on) {
			setCovered(false);
			return;
		}
		let frame = 0;
		const check = () => {
			const el = slot.current;
			if (!el) return;
			const r = el.getBoundingClientRect();
			if (r.width <= 0 || r.height <= 0) return;
			const x = r.left + r.width / 2;
			const hidden = [0.1, 0.4, 0.7].some((f) => {
				const hit = document.elementFromPoint(x, r.top + r.height * f);
				return !hit || !el.contains(hit);
			});
			setCovered(hidden);
		};
		const soon = () => {
			if (frame) return;
			frame = requestAnimationFrame(() => {
				frame = 0;
				check();
			});
		};
		check();
		const poll = window.setInterval(() => {
			if (document.visibilityState !== "hidden") check();
		}, COVER_POLL_MS);
		const mo = new MutationObserver(soon);
		mo.observe(document.body, { childList: true });
		document.addEventListener("pointerup", soon, true);
		document.addEventListener("keyup", soon, true);
		return () => {
			window.clearInterval(poll);
			cancelAnimationFrame(frame);
			mo.disconnect();
			document.removeEventListener("pointerup", soon, true);
			document.removeEventListener("keyup", soon, true);
		};
	}, [slot, on]);
	return covered;
}
