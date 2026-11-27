// 幻灯片 artifact 的查看器（计划 M14 5.3）：一次一张，键盘与底部浮条翻页；演示态请父页面把帧铺满窗口；
// `content` 能力可用且看的是最新版本时可以就地改字并保存成新版本。
// `?view=print` 把全部幻灯片逐页排开（PDF 导出用），`?view=thumbnail` 只画第一张。
// 评论（M31 7.4）：评论模式下点幻灯片报出张号、标题与归一化坐标；当前这张的图钉画在幻灯片上，定位时翻到那一张。

import { type ReactNode, useEffect, useMemo, useState } from "react";
import {
	NO_PINS,
	pickPoint,
	pointAnchor,
	textAtPoint,
} from "../../../src/lib/artifacts/office/anchor";
import { PinLayer } from "../../../src/lib/artifacts/office/pins";
import {
	type Deck,
	parseDeck,
	slideSize,
	withText,
} from "../../../src/lib/artifacts/slides";
import type { ViewerComments } from "../shared/bridge";
import type { ViewMode } from "../shared/config";
import type { ViewerContent } from "../shared/content";
import { EditBar, SaveStatusBar } from "../shared/EditBar";
import type { ViewerStrings } from "../shared/strings";
import { ScaledSlide, SlideView } from "./SlideView";

/** 运行时挂在页面上的本地函数：请父页面进入 / 退出演示。没有运行时（在浏览器中单独打开）时不存在。 */
function requestPresent(on: boolean): void {
	const fn = (window as Window & { __orynPresent?: (on: boolean) => void })
		.__orynPresent;
	if (typeof fn === "function") fn(on);
}

function isTyping(target: EventTarget | null): boolean {
	const el = target as HTMLElement | null;
	if (!el || typeof el.closest !== "function") return false;
	return !!el.closest("input, textarea, select, [contenteditable]");
}

export function SlidesViewer({
	content,
	view,
	strings,
	comments,
}: {
	content: ViewerContent;
	view: ViewMode;
	strings: ViewerStrings;
	/** 帧运行时的评论桥；没有时不画图钉、不响应点选。 */
	comments?: ViewerComments;
}): ReactNode {
	const { doc } = content;
	const parsed = useMemo(() => (doc ? parseDeck(doc.text) : null), [doc]);
	const [draft, setDraft] = useState<Deck | null>(null);
	const [index, setIndex] = useState(0);
	const [presenting, setPresenting] = useState(false);

	const deck = draft ?? (parsed?.ok ? parsed.deck : null);
	const count = deck?.slides.length ?? 0;
	const current = Math.min(index, Math.max(0, count - 1));
	const interactive = view === null && deck !== null;

	useEffect(() => {
		if (!interactive) return;
		const go = (to: number) => setIndex(Math.max(0, Math.min(count - 1, to)));
		const onKey = (e: KeyboardEvent) => {
			if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey) return;
			if (isTyping(e.target)) return;
			switch (e.key) {
				case "ArrowRight":
				case "ArrowDown":
				case "PageDown":
					go(current + 1);
					break;
				case "ArrowLeft":
				case "ArrowUp":
				case "PageUp":
					go(current - 1);
					break;
				case " ":
					go(e.shiftKey ? current - 1 : current + 1);
					break;
				case "Home":
					go(0);
					break;
				case "End":
					go(count - 1);
					break;
				case "Escape":
					if (!presenting) return;
					setPresenting(false);
					requestPresent(false);
					break;
				default:
					return;
			}
			e.preventDefault();
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [interactive, count, current, presenting]);

	// 定位评论：翻到锚点所在的那一张
	const locate = view === null ? (comments?.locate ?? null) : null;
	useEffect(() => {
		const n = locate?.page?.number;
		if (n !== undefined) setIndex(n - 1);
	}, [locate]);

	if (content.state === "loading")
		return <p className="oryn-placeholder">{strings.loading}</p>;
	if (content.state === "error" || !doc || !parsed)
		return (
			<p className="oryn-placeholder" role="alert">
				{strings.loadFailed}
			</p>
		);
	if (!deck)
		return (
			<DeckProblems
				problems={parsed.ok ? [] : parsed.problems.map(strings.problem)}
				title={strings.badDeck}
				print={view === "print"}
			/>
		);

	if (view === "print") return <PrintDeck deck={deck} base={doc.base} />;
	const first = deck.slides[0];
	if (view === "thumbnail")
		return first ? (
			<ScaledSlide deck={deck} className="oryn-s-thumbnail">
				<SlideView deck={deck} slide={first} base={doc.base} />
			</ScaledSlide>
		) : null;

	const slide = deck.slides[current];
	const editing = draft !== null;
	// 演示与改字时不评论、不画图钉
	const pick =
		!presenting && !editing && comments?.mode === "comment"
			? comments.pick
			: null;
	const pins =
		presenting || editing
			? NO_PINS
			: (comments?.pins ?? NO_PINS).filter(
					(p) => p.anchor.page?.number === current + 1,
				);
	const overlay =
		slide && (pick || pins.length > 0) ? (
			<>
				{pick ? (
					<button
						type="button"
						className="oryn-pick"
						aria-label={strings.commentSlide(current + 1)}
						onClick={(e) => {
							const { point, at } = pickPoint(e);
							const name = "title" in slide ? (slide.title ?? "") : "";
							// 点中的最内层文字元素；幻灯片画板与根节点本身不算（那是整张的文字）
							const text = textAtPoint(
								at,
								(el) =>
									el.closest(".oryn-s-scaled") !== null &&
									!el.matches(".oryn-s-scaled, .oryn-s-scaled > *"),
							);
							pick(pointAnchor({ number: current + 1, name }, point, text), at);
						}}
					/>
				) : null}
				<PinLayer pins={pins} />
			</>
		) : undefined;
	const startPresenting = () => {
		setPresenting(true);
		requestPresent(true);
	};
	const save = async () => {
		if (!draft) return;
		const outcome = await content.save(`${JSON.stringify(draft, null, 2)}\n`);
		if (outcome !== "error") setDraft(null);
	};

	return (
		<div
			className={`oryn-slides${presenting ? " oryn-slides-presenting" : ""}`}
		>
			{!presenting && (
				<SaveStatusBar
					status={content.status}
					strings={strings}
					onDismiss={content.dismiss}
				/>
			)}
			{!presenting && content.editable && !editing && !pick && (
				<button
					type="button"
					className="oryn-btn oryn-s-edit"
					onClick={() => {
						content.dismiss();
						setDraft(deck);
					}}
				>
					{strings.edit}
				</button>
			)}
			{slide && (
				<ScaledSlide deck={deck} overlay={overlay}>
					<SlideView
						key={current}
						deck={deck}
						slide={slide}
						base={doc.base}
						label={strings.slideLabel(current + 1)}
						edit={
							editing && !presenting
								? {
										on: true,
										onText: (path, text) =>
											setDraft((d) =>
												d ? withText(d, `slides[${current}].${path}`, text) : d,
											),
									}
								: undefined
						}
					/>
				</ScaledSlide>
			)}
			{presenting ? (
				<p className="oryn-s-exit-hint">{strings.exitHint}</p>
			) : (
				<>
					{editing && (
						<EditBar
							hint={strings.slidesHint}
							nextVersion={doc.head + 1}
							dirty={
								JSON.stringify(draft) !==
								JSON.stringify(parsed.ok ? parsed.deck : null)
							}
							saving={content.status.kind === "saving"}
							strings={strings}
							onDiscard={() => setDraft(null)}
							onSave={() => void save()}
						/>
					)}
					<nav className="oryn-s-nav" aria-label={strings.slidesNav}>
						<button
							type="button"
							className="oryn-btn oryn-s-nav-btn"
							aria-label={strings.prev}
							title={strings.prev}
							disabled={current === 0}
							onClick={() => setIndex(current - 1)}
						>
							‹
						</button>
						<span className="oryn-s-counter" data-testid="slide-counter">
							{strings.counter(current + 1, count)}
						</span>
						<button
							type="button"
							className="oryn-btn oryn-s-nav-btn"
							aria-label={strings.next}
							title={strings.next}
							disabled={current >= count - 1}
							onClick={() => setIndex(current + 1)}
						>
							›
						</button>
						<button
							type="button"
							className="oryn-btn"
							onClick={startPresenting}
						>
							{strings.present}
						</button>
					</nav>
				</>
			)}
		</div>
	);
}

/** 打印视图：全部幻灯片各占一页；图片解码完后标记 `data-ready`，渲染器据此截取。 */
function PrintDeck({ deck, base }: { deck: Deck; base: string }): ReactNode {
	const { width, height } = slideSize(deck.aspect);
	useEffect(() => {
		let live = true;
		const images = [...document.images].map((img) =>
			typeof img.decode === "function" ? img.decode().catch(() => {}) : null,
		);
		void Promise.all(images).then(() => {
			if (live) document.documentElement.dataset.ready = "true";
		});
		return () => {
			live = false;
		};
	}, []);
	return (
		<div className="oryn-s-print">
			<style>{`@page { size: ${width}px ${height}px; margin: 0 }`}</style>
			{deck.slides.map((s, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: 打印视图只读，位置就是身份
				<div key={i} className="oryn-s-print-page">
					<SlideView deck={deck} slide={s} base={base} />
				</div>
			))}
		</div>
	);
}

function DeckProblems({
	title,
	problems,
	print,
}: {
	title: string;
	problems: string[];
	print: boolean;
}): ReactNode {
	useEffect(() => {
		// 打印视图也要结束等待，免得渲染器一直等
		if (print) document.documentElement.dataset.ready = "true";
	}, [print]);
	return (
		<div className="oryn-placeholder oryn-s-problems" role="alert">
			<p>{title}</p>
			<ul>
				{problems.map((p) => (
					<li key={p}>{p}</li>
				))}
			</ul>
		</div>
	);
}
