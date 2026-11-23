/**
 * 角色胶囊轨（纯展示）：每格是角色头像 + 角色名，当前格由一块角色色滑块高亮，到位后名字轻微回弹；
 * `flash` 每递增一次就浮出（或保持）并重置计时，4 秒没有再递增就收起。
 * 线程页（RoleTrack：Ctrl+P 切换线程的角色模型）与首页（Ctrl+P 轮换新对话的 role）共用。
 * 轨道绝对定位在 `anchorRef` 的定位祖先上方（`anchorRef` 自身的 offsetTop 一并抬过去），横向铺满它再居中：
 * 不超过输入框宽度时全部显示，超出才在轨道内横向滚动（不显示滚动条）。
 */
import {
	type CSSProperties,
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { IdentityAvatar } from "../ui/IdentityAvatar";

const SHOW_MS = 4000;
/** 浮层淡出时长；首屏胶囊的收起 / 展开同用这个时长。 */
export const FADE_MS = 180;

/** 浮出状态：`hidden` 不在 DOM 里，`leaving` 播完收起动画（FADE_MS）后回到 `hidden`。 */
export type Phase = "hidden" | "shown" | "leaving";

/** 轨道里的一格。 */
export interface RailSlot {
	id: string;
	label: string;
	/** CSS 色相值（写进 `--role-h`） */
	hue: string;
	/** 角色头像：设了自定义头像用它，内置 role 没设时用默认字形，其余回落为默认 Agent 身份 */
	avatar: {
		identity: Schemas["AvatarIdentity"] | undefined;
		of: "role" | "agent";
		glyph?: string;
	};
	/** 可选：不可选的格置灰、点击无效 */
	ok: boolean;
	/** 悬停说明 */
	title?: string;
}

export function RoleRail({
	slots,
	activeId,
	flash,
	anchorRef,
	loading = false,
	message,
	onPick,
}: {
	slots: readonly RailSlot[];
	activeId?: string;
	/** 每次切换请求（或提示）递增：驱动浮出 */
	flash: number;
	anchorRef: RefObject<HTMLElement | null>;
	/** 数据还在取：没有格时显示占位 */
	loading?: boolean;
	message?: { tone: "info" | "error"; text: string };
	/** 点了一个可选、且不是当前的格 */
	onPick: (id: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [phase, setPhase] = useState<Phase>("hidden");
	const [above, setAbove] = useState(0);
	const [bump, setBump] = useState(0);
	const timer = useRef<number | undefined>(undefined);
	const hovering = useRef(false);
	const seenFlash = useRef(flash);

	const scheduleHide = () => {
		window.clearTimeout(timer.current);
		if (hovering.current) return;
		timer.current = window.setTimeout(() => {
			setPhase("leaving");
			timer.current = window.setTimeout(() => setPhase("hidden"), FADE_MS);
		}, SHOW_MS);
	};

	// 每次切换请求：浮出（或保持）轨道、重置计时
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只由 flash 计数驱动
	useEffect(() => {
		if (flash === seenFlash.current) return;
		seenFlash.current = flash;
		setAbove(anchorRef.current?.offsetTop ?? 0);
		setPhase("shown");
		scheduleHide();
	}, [flash]);

	useEffect(() => () => clearTimeout(timer.current), []);

	// 滑块：读当前格的 offsetLeft/offsetWidth；刚浮出时直接到位，之后的切换才滑动
	const listRef = useRef<HTMLDivElement>(null);
	const plateRef = useRef<HTMLSpanElement>(null);
	const placedFor = useRef<string | undefined>(undefined);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 轨道浮出（phase）与格数变化后按钮才挂载 / 移位，需要重新定位
	useLayoutEffect(() => {
		const list = listRef.current;
		const plate = plateRef.current;
		if (!list || !plate) {
			placedFor.current = undefined;
			return;
		}
		const el = list.querySelector<HTMLElement>('[aria-checked="true"]');
		if (!el) {
			plate.style.opacity = "0";
			return;
		}
		const animate =
			placedFor.current !== undefined && placedFor.current !== activeId;
		plate.style.transitionDuration = animate ? "" : "0ms";
		plate.style.left = `${el.offsetLeft}px`;
		plate.style.width = `${el.offsetWidth}px`;
		plate.style.opacity = "1";
		if (animate) {
			el.scrollIntoView?.({ block: "nearest", inline: "nearest" });
			setBump((b) => b + 1);
		}
		placedFor.current = activeId;
	}, [activeId, slots.length, phase]);

	if (phase === "hidden") return null;
	const activeHue = slots.find((s) => s.id === activeId)?.hue;
	return (
		<div
			className="pointer-events-none absolute inset-x-0 z-30 flex justify-center"
			style={{ bottom: `calc(100% + ${above + 10}px)` }}
		>
			{/* 收起只缩小下沉、不淡出：玻璃在 opacity < 1 时模糊会被截断，读起来像模糊忽有忽无 */}
			{/* biome-ignore lint/a11y/noStaticElementInteractions: 悬停/聚焦时暂停自动淡出 */}
			<div
				className={`glass pop-in pointer-events-auto flex min-w-0 max-w-full items-center rounded-full p-1 transition-[translate,scale] duration-[180ms] ease-out ${phase === "leaving" ? "translate-y-1 scale-95" : ""}`}
				onPointerEnter={() => {
					hovering.current = true;
					clearTimeout(timer.current);
					setPhase("shown");
				}}
				onPointerLeave={() => {
					hovering.current = false;
					scheduleHide();
				}}
				onFocus={() => {
					hovering.current = true;
					clearTimeout(timer.current);
					setPhase("shown");
				}}
				onBlur={() => {
					hovering.current = false;
					scheduleHide();
				}}
			>
				<div
					ref={listRef}
					role="radiogroup"
					aria-label={t("roles.track")}
					className="focus-inset relative flex min-w-0 items-center overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
				>
					<span
						ref={plateRef}
						aria-hidden
						className="role-tone absolute top-0 h-7 rounded-full bg-[var(--role-fill)] opacity-0 ring-1 ring-[var(--role-line)] ring-inset transition-[left,width,background-color,box-shadow] duration-[220ms] ease-out"
						style={{ "--role-h": activeHue } as CSSProperties}
					/>
					{slots.map((slot) => {
						const on = slot.id === activeId;
						return (
							// biome-ignore lint/a11y/useSemanticElements: 胶囊里要放头像和滑块，原生 radio 画不出来；键盘与读屏语义由 role/aria-checked 提供
							<button
								key={slot.id}
								type="button"
								role="radio"
								aria-checked={on}
								aria-disabled={!slot.ok || undefined}
								data-role={slot.id}
								title={slot.title}
								className={`role-tone relative z-10 inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full pr-2.5 pl-1.5 font-medium text-xs transition-colors duration-150 ${
									on
										? "text-[var(--role-ink)]"
										: slot.ok
											? "text-muted hover:text-fg"
											: "cursor-not-allowed text-ghost"
								}`}
								style={{ "--role-h": slot.hue } as CSSProperties}
								onClick={() => {
									if (slot.ok && !on) onPick(slot.id);
								}}
							>
								<span
									className={`inline-flex shrink-0 ${slot.ok ? "" : "opacity-50"}`}
								>
									<IdentityAvatar
										identity={slot.avatar.identity}
										of={slot.avatar.of}
										glyph={slot.avatar.glyph}
										size={18}
										shape="circle"
									/>
								</span>
								<span
									key={on ? `on-${bump}` : "off"}
									className={on ? "role-pop font-semibold" : ""}
								>
									{slot.label}
								</span>
							</button>
						);
					})}
					{loading && slots.length === 0 && (
						<span className="px-3 text-2xs text-faint">…</span>
					)}
				</div>
				{message && (
					<span
						className={`shrink-0 px-2 text-2xs ${message.tone === "error" ? "text-err" : "text-faint"}`}
					>
						{message.text}
					</span>
				)}
			</div>
		</div>
	);
}
