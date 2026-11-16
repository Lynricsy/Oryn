// 一张幻灯片：按八种版式在固定尺寸的画板上排版（字体用系统无衬线栈，颜色只取调色板）。
// 编辑态下各文字区变成 `contentEditable="plaintext-only"`，失焦时把文字交给 `onText(相对路径, 文字)`。

import {
	type CSSProperties,
	type KeyboardEvent,
	type ReactNode,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import {
	type Bullet,
	type Column,
	DECK_PALETTES,
	type Deck,
	type Slide,
	slideSize,
} from "../../../src/lib/artifacts/slides";
import { resolveAsset } from "../shared/config";

export interface SlideEditing {
	/** 编辑态：文字区可编辑。 */
	on: boolean;
	onText(path: string, text: string): void;
}

const READ_ONLY: SlideEditing = { on: false, onText: () => {} };

type TextTag =
	| "h1"
	| "h2"
	| "h3"
	| "p"
	| "div"
	| "span"
	| "li"
	| "th"
	| "td"
	| "figcaption"
	| "blockquote";

/** 一处文字。编辑后按新值重建元素（`key`），避免 React 去对账被浏览器改过的文本节点。 */
function Text({
	as: Tag = "div",
	path,
	value,
	edit,
	className,
}: {
	as?: TextTag;
	path: string;
	value: string;
	edit: SlideEditing;
	className?: string;
}): ReactNode {
	if (!edit.on)
		return (
			<Tag className={className} data-path={path}>
				{value}
			</Tag>
		);
	const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
		if (e.key === "Escape") {
			e.preventDefault();
			e.currentTarget.blur();
		}
		// 编辑时方向键等只作用于文字，不翻页
		e.stopPropagation();
	};
	return (
		<Tag
			key={value}
			className={`${className ?? ""} oryn-editable`}
			data-path={path}
			contentEditable="plaintext-only"
			suppressContentEditableWarning
			spellCheck={false}
			onKeyDown={onKeyDown}
			onBlur={(e) => {
				const text = e.currentTarget.textContent ?? "";
				if (text !== value) edit.onText(path, text);
			}}
		>
			{value}
		</Tag>
	);
}

function BulletList({
	path,
	items,
	edit,
}: {
	path: string;
	items: readonly Bullet[];
	edit: SlideEditing;
}): ReactNode {
	return (
		<ul className="oryn-s-bullets">
			{items.map((b, i) => {
				const at = `${path}[${i}]`;
				if (typeof b === "string")
					// biome-ignore lint/suspicious/noArrayIndexKey: 要点按位置编辑，位置就是身份
					return <Text key={i} as="li" path={at} value={b} edit={edit} />;
				return (
					// biome-ignore lint/suspicious/noArrayIndexKey: 要点按位置编辑，位置就是身份
					<li key={i}>
						<Text as="span" path={`${at}.text`} value={b.text} edit={edit} />
						{b.bullets && (
							<BulletList
								path={`${at}.bullets`}
								items={b.bullets}
								edit={edit}
							/>
						)}
					</li>
				);
			})}
		</ul>
	);
}

function ColumnView({
	path,
	col,
	edit,
}: {
	path: string;
	col: Column;
	edit: SlideEditing;
}): ReactNode {
	return (
		<div className="oryn-s-column">
			{col.heading !== undefined && (
				<Text
					as="h3"
					path={`${path}.heading`}
					value={col.heading}
					edit={edit}
				/>
			)}
			{col.text !== undefined && (
				<Text as="p" path={`${path}.text`} value={col.text} edit={edit} />
			)}
			{col.bullets && (
				<BulletList path={`${path}.bullets`} items={col.bullets} edit={edit} />
			)}
		</div>
	);
}

function Title({
	value,
	edit,
}: {
	value: string | undefined;
	edit: SlideEditing;
}): ReactNode {
	if (value === undefined) return null;
	return (
		<Text
			as="h2"
			className="oryn-s-heading"
			path="title"
			value={value}
			edit={edit}
		/>
	);
}

function Body({
	slide,
	base,
	edit,
}: {
	slide: Slide;
	base: string;
	edit: SlideEditing;
}): ReactNode {
	switch (slide.layout) {
		case "title":
		case "section":
			return (
				<div className="oryn-s-cover">
					<span className="oryn-s-rule" />
					<Text
						as="h1"
						className="oryn-s-title"
						path="title"
						value={slide.title}
						edit={edit}
					/>
					{slide.subtitle !== undefined && (
						<Text
							as="p"
							className="oryn-s-subtitle"
							path="subtitle"
							value={slide.subtitle}
							edit={edit}
						/>
					)}
				</div>
			);
		case "bullets":
			return (
				<>
					<Title value={slide.title} edit={edit} />
					<div
						className={`oryn-s-split${slide.image ? " oryn-s-has-image" : ""}`}
					>
						<BulletList path="bullets" items={slide.bullets} edit={edit} />
						{slide.image && (
							<img
								className="oryn-s-side-image"
								src={resolveAsset(slide.image, base)}
								alt={slide.alt ?? ""}
							/>
						)}
					</div>
				</>
			);
		case "two-column":
			return (
				<>
					<Title value={slide.title} edit={edit} />
					<div className="oryn-s-columns">
						<ColumnView path="left" col={slide.left} edit={edit} />
						<ColumnView path="right" col={slide.right} edit={edit} />
					</div>
				</>
			);
		case "image":
			return (
				<>
					<Title value={slide.title} edit={edit} />
					<figure className="oryn-s-figure">
						<img src={resolveAsset(slide.image, base)} alt={slide.alt} />
						{slide.caption !== undefined && (
							<Text
								as="figcaption"
								path="caption"
								value={slide.caption}
								edit={edit}
							/>
						)}
					</figure>
				</>
			);
		case "quote":
			return (
				<div className="oryn-s-quote">
					<span className="oryn-s-quote-mark" aria-hidden="true">
						“
					</span>
					<Text as="blockquote" path="quote" value={slide.quote} edit={edit} />
					{slide.attribution !== undefined && (
						<p className="oryn-s-attribution">
							<span aria-hidden="true">— </span>
							<Text
								as="span"
								path="attribution"
								value={slide.attribution}
								edit={edit}
							/>
						</p>
					)}
				</div>
			);
		case "stats":
			return (
				<>
					<Title value={slide.title} edit={edit} />
					<div
						className="oryn-s-stats"
						style={{ "--oryn-s-count": slide.stats.length } as CSSProperties}
					>
						{slide.stats.map((s, i) => (
							// biome-ignore lint/suspicious/noArrayIndexKey: 按位置编辑，位置就是身份
							<div key={i} className="oryn-s-stat">
								<Text
									className="oryn-s-stat-value"
									path={`stats[${i}].value`}
									value={s.value}
									edit={edit}
								/>
								{s.label !== undefined && (
									<Text
										className="oryn-s-stat-label"
										path={`stats[${i}].label`}
										value={s.label}
										edit={edit}
									/>
								)}
							</div>
						))}
					</div>
				</>
			);
		case "table":
			return (
				<>
					<Title value={slide.title} edit={edit} />
					<table className="oryn-s-table">
						<thead>
							<tr>
								{slide.columns.map((c, i) => {
									const path = `columns[${i}]`;
									return (
										<Text
											key={path}
											as="th"
											path={path}
											value={c}
											edit={edit}
										/>
									);
								})}
							</tr>
						</thead>
						<tbody>
							{slide.rows.map((r, i) => (
								// biome-ignore lint/suspicious/noArrayIndexKey: 按位置编辑，位置就是身份
								<tr key={i}>
									{r.map((cell, j) => {
										const path = `rows[${i}][${j}]`;
										return (
											<Text
												key={path}
												as="td"
												path={path}
												value={cell}
												edit={edit}
											/>
										);
									})}
								</tr>
							))}
						</tbody>
					</table>
				</>
			);
	}
}

/** 固定尺寸（`slideSize`）的画板，不缩放。 */
export function SlideView({
	deck,
	slide,
	base,
	edit = READ_ONLY,
	label,
}: {
	deck: Deck;
	slide: Slide;
	base: string;
	edit?: SlideEditing;
	label?: string;
}): ReactNode {
	const { width, height } = slideSize(deck.aspect);
	const p = DECK_PALETTES[deck.theme ?? "light"];
	const style = {
		width,
		height,
		"--s-bg": p.background,
		"--s-text": p.text,
		"--s-muted": p.muted,
		"--s-accent": p.accent,
		"--s-surface": p.surface,
		"--s-line": p.line,
	} as CSSProperties;
	return (
		<section
			className={`oryn-slide oryn-slide-${slide.layout}`}
			style={style}
			aria-label={label}
			data-layout={slide.layout}
		>
			<Body slide={slide} base={base} edit={edit} />
		</section>
	);
}

/** 把画板用 `transform: scale` 缩放到容器里居中；没有 ResizeObserver（测试环境）时按原尺寸。 */
export function ScaledSlide({
	deck,
	children,
	className,
}: {
	deck: Deck;
	children: ReactNode;
	className?: string;
}): ReactNode {
	const ref = useRef<HTMLDivElement>(null);
	const [scale, setScale] = useState(1);
	const { width, height } = slideSize(deck.aspect);

	useLayoutEffect(() => {
		const el = ref.current;
		if (!el || typeof ResizeObserver === "undefined") return;
		const measure = () => {
			const w = el.clientWidth;
			const h = el.clientHeight;
			if (w > 0 && h > 0) setScale(Math.min(w / width, h / height));
		};
		measure();
		const ro = new ResizeObserver(measure);
		ro.observe(el);
		return () => ro.disconnect();
	}, [width, height]);

	return (
		<div ref={ref} className={`oryn-s-stage ${className ?? ""}`}>
			<div
				className="oryn-s-scaled"
				style={{
					width,
					height,
					transform: `translate(-50%, -50%) scale(${scale})`,
				}}
			>
				{children}
			</div>
		</div>
	);
}
