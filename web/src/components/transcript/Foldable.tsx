/**
 * 转录里的长正文折叠（内联提示的卡片、自动消息的事件卡共用）：超过 `max-h-80` 时截断、底部渐隐，
 * 下方「展开全部（N 行）/ 收起」。是否被截按真实排版量（宽度变了会重新量），不按行数猜；
 * 收起后外框顶部若已滚出视口，拉回来，免得读者找不到刚才读到哪。
 */
import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../ui/icons";

export function Foldable({
	lines,
	className = "",
	toggleClassName = "px-2 pb-1.5",
	testId,
	children,
}: {
	/** 按钮上的行数（原文行数） */
	lines: number;
	/** 外框的类（描边、底色、外边距）；按钮在外框内、正文下方 */
	className?: string;
	/** 按钮行的内边距：让按钮里的箭头与正文左缘对齐 */
	toggleClassName?: string;
	/** 截断层的 data-testid */
	testId?: string;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const clipRef = useRef<HTMLDivElement>(null);
	const frameRef = useRef<HTMLDivElement>(null);
	const [expanded, setExpanded] = useState(false);
	const collapsing = useRef(false);
	const [clipped, setClipped] = useState(false);
	useLayoutEffect(() => {
		const el = clipRef.current;
		if (!el || expanded) return;
		if (collapsing.current) {
			collapsing.current = false;
			frameRef.current?.scrollIntoView?.({ block: "nearest" });
		}
		const measure = () => setClipped(el.scrollHeight > el.clientHeight + 1);
		measure();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(measure);
		ro.observe(el);
		if (el.firstElementChild) ro.observe(el.firstElementChild);
		return () => ro.disconnect();
	}, [expanded]);
	const folded = clipped && !expanded;
	return (
		<div ref={frameRef} className={`min-w-0 scroll-mt-16 ${className}`}>
			<div
				ref={clipRef}
				data-testid={testId}
				data-folded={folded || undefined}
				className={`${expanded ? "" : "max-h-80"} overflow-hidden ${folded ? "[mask-image:linear-gradient(to_bottom,black_calc(100%-56px),transparent)]" : ""}`}
			>
				{children}
			</div>
			{clipped && (
				<div className={`flex ${toggleClassName}`}>
					<button
						type="button"
						aria-expanded={expanded}
						className="inline-flex h-6 items-center gap-1 rounded-sm px-1.5 text-2xs text-faint transition-colors duration-150 hover:bg-hover hover:text-fg"
						onClick={() => {
							collapsing.current = expanded;
							setExpanded(!expanded);
						}}
					>
						<Icon
							name={expanded ? "chevronUp" : "chevronDown"}
							size={12}
							aria-hidden
						/>
						{expanded
							? t("notice.collapse")
							: t("notice.showAll", { count: lines })}
					</button>
				</div>
			)}
		</div>
	);
}
