/**
 * 底栏（M9.6，全端同一个）：线程 / 助手 / 项目 / 更多。点一项 = 切分区（侧栏内容与主区路由一起换，导航到该分区记住的路径）；
 * 手机下沉侧栏里点后侧栏保持打开，再点一次已选中的分区收起侧栏。选中项是强调色文字 + 一块滑动底板（四等分，按序号定位，
 * 200ms `--ease`，与分区轨道同步；减少动效时由全局规则压掉过渡）。
 * 助手项显示全局助手的生效名字与圆形头像（都没设时「全局助手」+ sparkle）；任一助手对话未读时带未读点。
 * 「更多」项在连接不是 open 时带状态点。
 * 底栏悬浮在分区内容之上（绝对定位贴侧栏底边）：列表照常滚到它下面、从玻璃胶囊后面透出来，不再被一条实底截断；
 * 各分区滚动区底部按 `--tabbar-h`（`tabBarHeight`，由侧栏外壳设置）留白，最后一行能滚到胶囊上方。
 * 胶囊两种场合都是玻璃（`.glass`，自带模糊）：`tone="glass"` 是手机下沉侧栏层，底部让出安全区；`inset` 是列侧栏 / 双段抽屉，
 * `safeBottom` 时再让出底部安全区。外层不接收指针（胶囊两侧露出的列表仍可点）。底栏不带 `data-window-drag`（不能拖动窗口）。
 * iOS 原生底栏（`lib/native-tab-bar.ts`，只在 `tone="glass"`）：量好胶囊的视口矩形，连同四项、选中项、深浅、强调色、
 * 头像与角标整份交给控制器（`setSpec`，内容变了才真正发）；原生浮在 WebView 之上（玻璃折射下面的侧栏列表），
 * 原生看得见时网页胶囊换成 `opacity-0` 并对读屏与 Tab 隐藏，但仍可命中（遮挡探测靠它）；被菜单、弹层盖住时原生让位、
 * 网页胶囊顶上。原生点击经 `host.onTabBar` 回来，走与网页按钮同一个 `switchSection`；卸载时撤掉原生栏。
 * `tone="rail"`：iPhone Duo 竖栏（`Rail.tsx`）里的版本：同样四项竖着排成一根玻璃胶囊，跟在竖栏的页头按钮下面；
 * 此时侧栏里不再放底栏，原生底栏随之撤掉（竖栏是网页画的）。底栏原本是「换侧栏里的列表」：手机下沉侧栏收着时
 * 点竖栏的一项 = 先切到该分区（主区照常跳到它上次停留的页面），再展开侧栏露出那个分区的列表；侧栏开着时与侧栏里的底栏
 * 一样（换分区，再点当前分区收起）。列侧栏常驻（内屏横放）时侧栏本来就看得见，也与侧栏里的底栏一样。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useAssistantIdentity } from "../../api/identity-queries";
import { host, type NativeTabBarSpec } from "../../lib/host";
import { toHexColor } from "../../lib/host-chrome";
import {
	buildTabBarSpec,
	nativeTabBar,
	rasterizeAvatar,
	useNativeTabBarShown,
} from "../../lib/native-tab-bar";
import { SECTION_ORDER, type SectionId } from "../../lib/sections";
import { useConnection, useIndex } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { StatusDot } from "../ui/data";
import { AssistantAvatar } from "../ui/IdentityAvatar";
import { Icon, type IconName } from "../ui/icons";
import { isSinkLayout, useSection, useSwitchSection } from "./useSection";

const ICONS = {
	threads: "chats",
	assistant: "sparkle",
	projects: "folder",
	more: "more",
} as const satisfies Record<SectionId, IconName>;

/** 胶囊高度与上下外边距：与下面 `outer` 的类名一致，供侧栏外壳设 `--tabbar-h`。 */
const PILL = "52px";
export function tabBarHeight(
	tone: "glass" | "inset",
	safeBottom: boolean,
): string {
	const bottom =
		tone === "glass"
			? "max(var(--safe-bottom), 0.5rem)"
			: safeBottom
				? "calc(0.5rem + var(--safe-bottom))"
				: "0.5rem";
	return `calc(${PILL} + 0.375rem + ${bottom})`;
}

/** 快捷键提示：macOS / iOS 用 ⌘n，其余 Ctrl+n（侧栏的命令面板按钮同用）。 */
export const MAC =
	typeof navigator !== "undefined" &&
	/^(Mac|iPhone|iPad|iPod)/.test(navigator.platform);

type Frame = NativeTabBarSpec["frame"];

/** 保留两位小数：亚像素抖动不算矩形变了。 */
const round2 = (v: number) => Math.round(v * 100) / 100;

export function SectionTabBar({
	tone,
	safeBottom = false,
}: {
	tone: "glass" | "inset" | "rail";
	safeBottom?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const section = useSection();
	const switchSection = useSwitchSection();
	const conn = useConnection((s) => s.status);
	const assistant = useAssistantIdentity();
	const assistantUnread = useIndex((s) =>
		Object.values(s.threads).some(
			(th) => th.kind === "assistant" && th.unread && !th.archivedAt,
		),
	);
	const index = SECTION_ORDER.indexOf(section);
	const avatarIcon = assistant.avatar.avatarIcon;
	const avatarImage = assistant.avatar.avatarImage;
	const hasAvatar = Boolean(avatarIcon || avatarImage);
	const connTone = conn === "unauthorized" ? "err" : "warn";
	const labels = useMemo<Record<SectionId, string>>(
		() => ({
			threads: t("tabBar.threads"),
			assistant: assistant.name ?? t("tabBar.assistant"),
			projects: t("tabBar.projects"),
			more: t("tabBar.more"),
		}),
		[t, assistant.name],
	);

	// ── iOS 原生底栏（只在手机下沉侧栏层）：钩子无条件调用，副作用里判断 sink ──
	const sink = tone === "glass";
	const shown = useNativeTabBarShown();
	const hidden = sink && shown;
	const slotRef = useRef<HTMLDivElement>(null);
	const inkRef = useRef<HTMLSpanElement>(null);
	const [frame, setFrame] = useState<Frame | null>(null);
	const [paint, setPaint] = useState<{
		dark: boolean;
		accent: string | null;
	} | null>(null);
	const [raster, setRaster] = useState<{
		src: string;
		png: string | null;
	} | null>(null);

	// 胶囊矩形：侧栏层不随卡片移动，与开合无关；视口或胶囊尺寸变了重量，值没变不 set
	useLayoutEffect(() => {
		const el = slotRef.current;
		if (!sink || !el) return;
		const measure = () => {
			const r = el.getBoundingClientRect();
			const next = {
				x: round2(r.left),
				y: round2(r.top),
				width: round2(r.width),
				height: round2(r.height),
			};
			setFrame((prev) =>
				prev &&
				prev.x === next.x &&
				prev.y === next.y &&
				prev.width === next.width &&
				prev.height === next.height
					? prev
					: next,
			);
		};
		measure();
		window.addEventListener("resize", measure);
		const ro =
			typeof ResizeObserver === "undefined"
				? null
				: new ResizeObserver(measure);
		ro?.observe(el);
		return () => {
			window.removeEventListener("resize", measure);
			ro?.disconnect();
		};
	}, [sink]);

	// 深浅与强调色：跟随 <html> 的属性（主题、强调色变量）与系统明暗。强调色读不带过渡的隐藏探针，
	// 不读选中按钮（它有 150ms 颜色过渡，换主题的那一刻读到的是旧色）
	useLayoutEffect(() => {
		if (!sink) return;
		const root = document.documentElement;
		const read = () => {
			const dark = root.dataset.theme === "dark";
			const ink = inkRef.current;
			const accent = ink ? toHexColor(getComputedStyle(ink).color) : null;
			setPaint((p) =>
				p && p.dark === dark && p.accent === accent ? p : { dark, accent },
			);
		};
		read();
		const mo = new MutationObserver(read);
		mo.observe(root, { attributes: true });
		const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
		mq?.addEventListener("change", read);
		return () => {
			mo.disconnect();
			mq?.removeEventListener("change", read);
		};
	}, [sink]);

	// 图片头像栅格化成 PNG（异步）；出图前先按字形 / sparkles 发
	const rasterSrc = sink ? avatarImage : undefined;
	useEffect(() => {
		if (!rasterSrc) return;
		let live = true;
		void rasterizeAvatar(rasterSrc).then((png) => {
			if (live) setRaster({ src: rasterSrc, png });
		});
		return () => {
			live = false;
		};
	}, [rasterSrc]);
	const avatarPng = raster && raster.src === rasterSrc ? raster.png : null;

	useEffect(() => {
		if (!sink || !frame || !paint || frame.width <= 0 || frame.height <= 0)
			return;
		nativeTabBar.setSpec(
			buildTabBarSpec({
				labels,
				selected: section,
				frame,
				dark: paint.dark,
				accent: paint.accent,
				assistant: {
					image: avatarPng,
					glyph: avatarIcon || null,
					unread: assistantUnread,
				},
				conn: conn === "open" ? null : connTone,
			}),
		);
	}, [
		sink,
		frame,
		paint,
		labels,
		section,
		avatarPng,
		avatarIcon,
		assistantUnread,
		conn,
		connTone,
	]);

	// 离开手机布局、壳的加载 / 错误页：撤掉原生栏
	useEffect(
		() => (sink ? () => nativeTabBar.setSpec(null) : undefined),
		[sink],
	);

	// 原生点击：与网页按钮同一调用（点已选中项在下沉侧栏里收起）
	useEffect(
		() =>
			sink
				? host.onTabBar((id) => {
						const s = SECTION_ORDER.find((k) => k === id);
						if (s) switchSection(s, { toggleSink: true });
					})
				: undefined,
		[sink, switchSection],
	);

	const rail = tone === "rail";
	const outer = sink
		? "px-3 pt-1.5 pb-[max(var(--safe-bottom),0.5rem)]"
		: `px-2 pt-1.5 ${safeBottom ? "pb-[calc(0.5rem+var(--safe-bottom))]" : "pb-2"}`;
	return (
		<nav
			aria-label={t("tabBar.label")}
			data-testid="section-tabbar"
			data-tone={tone}
			className={
				rail
					? "pointer-events-none flex shrink-0 justify-center"
					: `pointer-events-none absolute inset-x-0 bottom-0 z-10 ${outer}`
			}
		>
			{sink && <span ref={inkRef} hidden className="text-accent-ink" />}
			<div
				ref={slotRef}
				data-tabbar-slot
				aria-hidden={hidden || undefined}
				className={`${hidden ? "opacity-0" : "glass"} pointer-events-auto rounded-full p-1 ${rail ? "h-[232px] w-[52px]" : "h-[52px]"}`}
			>
				<div
					className={`relative grid h-full ${rail ? "grid-rows-4" : "grid-cols-4"}`}
				>
					<span
						aria-hidden
						data-testid="section-tabbar-plate"
						className={`absolute rounded-full bg-[var(--sb-sel)] duration-200 ease-[var(--ease)] ${rail ? "inset-x-0 transition-[top]" : "inset-y-0 transition-[left]"}`}
						style={
							rail
								? { top: `${index * 25}%`, height: "25%" }
								: { left: `${index * 25}%`, width: "25%" }
						}
					/>
					{SECTION_ORDER.map((key, i) => {
						const on = key === section;
						const label = labels[key];
						const keys = MAC ? `⌘${i + 1}` : `Ctrl+${i + 1}`;
						return (
							<button
								key={key}
								type="button"
								data-tab={key}
								tabIndex={hidden ? -1 : undefined}
								aria-current={on ? "true" : undefined}
								title={t("sections.tabTitle", { label, keys })}
								onClick={() => switchSection(key, { toggleSink: true })}
								className={`relative z-10 flex min-h-11 min-w-0 flex-col items-center justify-center gap-0.5 rounded-full text-2xs leading-none transition-colors duration-150 ${on ? "font-semibold text-accent-ink" : "font-medium text-muted hover:text-fg"}`}
							>
								<span className="relative inline-flex">
									{key === "assistant" && hasAvatar ? (
										<AssistantAvatar size={20} shape="circle" />
									) : (
										<Icon
											name={ICONS[key]}
											size={20}
											weight={on ? "fill" : "regular"}
											aria-hidden
										/>
									)}
									{key === "assistant" && assistantUnread && (
										<span
											role="img"
											data-testid="tabbar-unread"
											aria-label={t("sidebar.unread")}
											className="absolute -top-0.5 -right-1 h-[7px] w-[7px] rounded-full bg-accent ring-2 ring-[var(--panel)]"
										/>
									)}
									{key === "more" && conn !== "open" && (
										<span
											data-testid="tabbar-conn"
											className="absolute -top-0.5 -right-1 inline-flex"
										>
											<StatusDot
												tone={connTone}
												label={t(`connection.${conn}`)}
											/>
										</span>
									)}
								</span>
								<span className="max-w-full truncate px-1">{label}</span>
							</button>
						);
					})}
				</div>
			</div>
		</nav>
	);
}
