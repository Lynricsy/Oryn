/**
 * 设置 → Agent → 记忆（M16）：内置 Hindsight 记忆的管理页。同一项目的线程共享该项目的记忆，全局记忆所有项目都能召回，
 * 项目之间互相看不到；隔离由控制面的记忆网关强制。作用于 orb 与 runner，不作用于全局助手。
 * 卡片顺序：状态 → 连接与 .env → 项目开关 → 模型 → 嵌入与重排 → 浏览 → 全局记忆 → 待批准 → mental models → 用量 → 导出。
 * 之后是「助手记忆」分区（M17）：全局助手关于主人的记忆（bank `oryn-owner`），依赖上面的工程记忆已启用并配置；
 * 未满足时分区只显示一条提示。分区内：设置 → 状态 → 主题 → 事实 → 来源与排除 → 明确记忆。
 * 页顶有吸顶的页内目录：卡片按主题分 6 节，点哪节滚到哪节，滚动时高亮当前所在的节。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOwnerMemorySettings } from "../../../api/owner-memory-queries";
import { ApplyStatusBanner } from "../../../components/settings/ApplyStatusBanner";
import { Callout } from "../../../components/ui/data";
import { Spinner, tabClass } from "../../../components/ui/primitives";
import { BrowseCard } from "./memory/BrowseCard";
import { CandidatesCard } from "./memory/CandidatesCard";
import { ConnectionCard } from "./memory/ConnectionCard";
import { EndpointsCard } from "./memory/EndpointsCard";
import { ExportCard } from "./memory/ExportCard";
import { MemoryError } from "./memory/errors";
import { GlobalCard } from "./memory/GlobalCard";
import { MentalModelsCard } from "./memory/MentalModelsCard";
import { ModelsCard } from "./memory/ModelsCard";
import { OwnerExplicitCard } from "./memory/OwnerExplicitCard";
import { OwnerFactsCard } from "./memory/OwnerFactsCard";
import { OwnerSettingsCard } from "./memory/OwnerSettingsCard";
import { OwnerSourcesCard } from "./memory/OwnerSourcesCard";
import { OwnerStatusCard } from "./memory/OwnerStatusCard";
import { OwnerTopicsCard } from "./memory/OwnerTopicsCard";
import { StatusCard } from "./memory/StatusCard";
import { ToggleCard } from "./memory/ToggleCard";
import { UsageCard } from "./memory/UsageCard";

/** 目录的节：卡片按主题分组，节的锚点挂在分组外层上。 */
const SECTIONS = [
	"status",
	"models",
	"browse",
	"global",
	"usage",
	"owner",
] as const;
type SectionKey = (typeof SECTIONS)[number];
const anchorId = (k: SectionKey) => `memory-${k}`;

export default function MemoryPage(): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<ApplyStatusBanner />
			<MemoryToc />
			<div id={anchorId("status")} className="flex flex-col gap-3">
				<Callout tone="neutral">{t("memoryPage.scope")}</Callout>
				<StatusCard />
				<ConnectionCard />
				<ToggleCard />
			</div>
			<div id={anchorId("models")} className="flex flex-col gap-3">
				<ModelsCard />
				<EndpointsCard />
			</div>
			<div id={anchorId("browse")} className="flex flex-col gap-3">
				<BrowseCard />
			</div>
			<div id={anchorId("global")} className="flex flex-col gap-3">
				<GlobalCard />
				<CandidatesCard />
				<MentalModelsCard />
			</div>
			<div id={anchorId("usage")} className="flex flex-col gap-3">
				<UsageCard />
				<ExportCard />
			</div>
			<OwnerMemorySection />
		</>
	);
}

/** 最近的纵向滚动祖先（设置页的内容滚动区）。 */
function scrollParent(el: HTMLElement): HTMLElement | null {
	for (let p = el.parentElement; p; p = p.parentElement)
		if (/(auto|scroll)/.test(getComputedStyle(p).overflowY)) return p;
	return null;
}

/**
 * 吸顶的页内目录。底色不透明（半透明会叠出重影，模糊在吸顶条上也会随祖先变化时有时无）。
 * 吸顶位置取负的滚动区内边距（`-top-3` / `sm:-top-5`）：sticky 按内边距内缘停靠，`top-0` 会在上方留一道缝透出滚过的内容。
 * 滚动区的 `scroll-padding-top` 跟着目录高度走：点目录、Tab 聚焦都停在目录下方，不被它压住。
 */
function MemoryToc(): ReactNode {
	const { t } = useTranslation();
	const navRef = useRef<HTMLElement>(null);
	const [active, setActive] = useState<SectionKey>("status");
	useEffect(() => {
		const nav = navRef.current;
		const scroller = nav && scrollParent(nav);
		if (!nav || !scroller) return;
		const pad = () => {
			scroller.style.scrollPaddingTop = `${nav.offsetHeight + 8}px`;
		};
		let frame = 0;
		const update = () => {
			frame = 0;
			// 当前节 = 顶边已越过目录下沿的最后一节；滚到底时最后一节可能够不到这条线，认最后一节
			const line = nav.getBoundingClientRect().bottom + 8;
			let current: SectionKey = SECTIONS[0];
			for (const k of SECTIONS) {
				const el = document.getElementById(anchorId(k));
				if (el && el.getBoundingClientRect().top <= line) current = k;
			}
			if (
				scroller.scrollTop > 0 &&
				scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2
			)
				current = SECTIONS[SECTIONS.length - 1] ?? current;
			setActive(current);
		};
		const onScroll = () => {
			if (!frame) frame = requestAnimationFrame(update);
		};
		pad();
		update();
		scroller.addEventListener("scroll", onScroll, { passive: true });
		// 窄屏换行时目录变高：跟着改 scroll-padding（jsdom 没有 ResizeObserver）
		const ro =
			typeof ResizeObserver === "undefined" ? null : new ResizeObserver(pad);
		ro?.observe(nav);
		return () => {
			scroller.removeEventListener("scroll", onScroll);
			cancelAnimationFrame(frame);
			ro?.disconnect();
			scroller.style.scrollPaddingTop = "";
		};
	}, []);
	// 只滚设置页的内容区：scrollIntoView 会连带滚动所有可滚祖先（含文档），把整个外壳拉走
	const jump = (k: SectionKey) => {
		const nav = navRef.current;
		const el = document.getElementById(anchorId(k));
		const scroller = nav && scrollParent(nav);
		if (!nav || !el || !scroller) return;
		const reduce = window.matchMedia?.(
			"(prefers-reduced-motion: reduce)",
		).matches;
		const top =
			scroller.scrollTop +
			el.getBoundingClientRect().top -
			scroller.getBoundingClientRect().top -
			nav.offsetHeight -
			8;
		scroller.scrollTo({ top, behavior: reduce ? "auto" : "smooth" });
	};
	return (
		<nav
			ref={navRef}
			aria-label={t("memoryPage.toc.label")}
			data-testid="memory-toc"
			className="sticky -top-3 z-10 flex flex-wrap gap-1 border-border border-b bg-panel py-2 sm:-top-5"
		>
			{SECTIONS.map((k) => (
				<button
					key={k}
					type="button"
					aria-current={active === k ? "location" : undefined}
					className={`${tabClass(active === k)} ${active === k ? "bg-hover" : ""}`}
					onClick={() => jump(k)}
				>
					{k === "owner"
						? t("memoryPage.owner.title")
						: t(`memoryPage.toc.${k}`)}
				</button>
			))}
		</nav>
	);
}

/** 「助手记忆」分区：工程记忆未启用或未配置（`available` 为假）时只显示提示。 */
function OwnerMemorySection(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemorySettings();
	return (
		<section
			id={anchorId("owner")}
			className="flex flex-col gap-3"
			aria-labelledby="owner-memory-heading"
			data-testid="owner-memory-section"
		>
			<h2
				id="owner-memory-heading"
				className="mt-4 font-semibold text-sm tracking-tight"
			>
				{t("memoryPage.owner.title")}
			</h2>
			<p className="text-faint text-xs">{t("memoryPage.owner.scope")}</p>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadSettings" error={q.error} />
			) : q.data && !q.data.available ? (
				<Callout tone="neutral" role="status">
					{t("memoryPage.owner.unavailable")}
				</Callout>
			) : q.data ? (
				<>
					<OwnerSettingsCard settings={q.data} />
					<OwnerStatusCard />
					<OwnerTopicsCard />
					<OwnerFactsCard enabled={q.data.enabled} />
					<OwnerSourcesCard />
					<OwnerExplicitCard />
				</>
			) : null}
		</section>
	);
}
