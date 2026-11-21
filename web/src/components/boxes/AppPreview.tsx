/**
 * app 行的悬停实时预览（M23 步骤 7.4）：只在 `app.preview && app.url` 且指针设备能悬停时启用。
 * 触发器是包住 app 徽标的按钮（可聚焦）；指针停 300ms 或键盘聚焦时打开，指针离开触发器与卡片两者 150ms 后、
 * 焦点离开两者、或 Escape 时关闭（关闭即卸载 iframe）。浮层走 `Floating`，不做进场动画（悬停不加入场动画）。
 * 画框里先是放大的徽标，iframe `load` 之后直接换成 iframe（不淡入）。私有 app 经 `/__oryn/login` 走一次握手。
 */
import {
	type FocusEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { BoxApp } from "../../api/box-queries";
import { ProjectBadge } from "../ui/data";
import { Floating } from "../ui/Floating";

const OPEN_DELAY = 300;
const CLOSE_DELAY = 150;

/** 指针设备能悬停（触屏不启用预览）。 */
export function canHoverPreview(): boolean {
	return (
		typeof window !== "undefined" &&
		typeof window.matchMedia === "function" &&
		window.matchMedia("(hover: hover)").matches
	);
}

/** 预览地址：公开 app 直接打开根页面，私有 app 先经 `/__oryn/login` 换 Cookie 再跳回。 */
export function previewSrc(
	app: Pick<BoxApp, "url" | "public">,
	dark: boolean,
): string {
	const q = `/?oryn-preview=${dark ? "dark" : "light"}`;
	return app.public
		? `${app.url}${q}`
		: `${app.url}/__oryn/login?next=${encodeURIComponent(q)}`;
}

export function AppPreview({
	app,
	size = 28,
}: {
	app: BoxApp;
	size?: number;
}): ReactNode {
	const { t } = useTranslation();
	const anchor = useRef<HTMLButtonElement>(null);
	const card = useRef<HTMLDivElement>(null);
	const [open, setOpen] = useState(false);
	const [loaded, setLoaded] = useState(false);
	const timer = useRef<number | undefined>(undefined);
	const schedule = (next: boolean, ms: number) => {
		window.clearTimeout(timer.current);
		timer.current = window.setTimeout(() => {
			setOpen(next);
			if (!next) setLoaded(false);
		}, ms);
	};
	const hold = () => window.clearTimeout(timer.current);
	useEffect(() => () => window.clearTimeout(timer.current), []);
	useEffect(() => {
		if (!open) return;
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			window.clearTimeout(timer.current);
			setOpen(false);
			setLoaded(false);
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [open]);
	/** 焦点去了触发器与卡片之外才关。 */
	const onBlur = (e: FocusEvent) => {
		const next = e.relatedTarget;
		if (
			next instanceof Node &&
			(anchor.current?.contains(next) || card.current?.contains(next))
		)
			return;
		schedule(false, 0);
	};
	const dark = document.documentElement.dataset.theme === "dark";
	return (
		<>
			<button
				ref={anchor}
				type="button"
				aria-label={t("boxes.preview.label", { app: app.name })}
				aria-expanded={open}
				className="inline-flex shrink-0 rounded-sm"
				onPointerEnter={() => schedule(true, OPEN_DELAY)}
				onPointerLeave={() => schedule(false, CLOSE_DELAY)}
				onFocus={() => {
					hold();
					setOpen(true);
				}}
				onBlur={onBlur}
			>
				<ProjectBadge name={app.name} size={size} />
			</button>
			{open && (
				<Floating
					ref={card}
					anchor={anchor}
					side="bottom"
					align="start"
					gap={6}
					bare
					motion={false}
					className="w-96 max-w-[calc(100vw-1rem)] overflow-hidden p-1.5"
					onPointerEnter={hold}
					onPointerLeave={() => schedule(false, CLOSE_DELAY)}
					onBlur={onBlur}
				>
					<a
						href={app.url}
						target="_blank"
						rel="noreferrer"
						className="relative block aspect-[8/5] overflow-hidden rounded-md bg-inset"
					>
						{!loaded && (
							<span className="absolute inset-0 grid place-items-center">
								<ProjectBadge name={app.name} size={56} />
							</span>
						)}
						<iframe
							data-testid="app-preview-frame"
							title={app.name}
							src={previewSrc(app, dark)}
							sandbox="allow-scripts allow-same-origin"
							allow=""
							tabIndex={-1}
							onLoad={() => setLoaded(true)}
							className={`pointer-events-none absolute inset-0 h-full w-full border-0 ${loaded ? "" : "invisible"}`}
						/>
					</a>
				</Floating>
			)}
		</>
	);
}
