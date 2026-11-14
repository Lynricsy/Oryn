/**
 * 头像裁剪对话框：整张图按 contain 放进舞台，上面叠一个可拖动、可缩放的正方形取景框（图不动框动）。
 * 拖动框 = 平移；四角手柄 = 以对角为不动点缩放；滚轮、双指、滑杆 = 缩放；框聚焦时方向键平移（Shift 加大步长），
 * `+` / `-` 缩放，`0` 复位。框内叠两条显示轮廓：圆角矩形实线（回合头的方块头像）与内切圆虚线（底栏等处的圆形头像）。
 * 预览行用同一张 `<img>` 加 transform 即时显示两种形状，不重绘 canvas；确认时才按取景框编码。
 */
import {
	type CSSProperties,
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useEffect,
	useId,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type Corner,
	type Crop,
	cropAtZoom,
	type Dim,
	initialCrop,
	MAX_ZOOM,
	moveCrop,
	resizeFromCorner,
	zoomCrop,
	zoomOf,
} from "../../lib/avatar-crop";
import {
	type AvatarSource,
	AvatarTooLargeError,
	renderAvatar,
} from "../../lib/avatar-image";
import { Callout } from "../ui/data";
import { Button, Dialog, describeError } from "../ui/primitives";

/** 回合头头像的边长（px）：圆角矩形轮廓按「半径 / 边长」等比画到取景框上。 */
const TILE_SIZE = 28;
/** 读不到 `rounded-md` 的像素半径时的取值。 */
const FALLBACK_RADIUS = 6;
/** 预览方块的边长（px）。 */
const PREVIEW_SIZE = 40;
/** 舞台的最长边：桌面 320px，窄屏留出对话框内边距与手柄的命中区。 */
const STAGE_BASE = "min(320px, 100vw - 64px)";
/** 滚轮一格、`+` / `-` 一次的缩放倍数。 */
const ZOOM_STEP = 0.9;

type Gesture =
	| { kind: "move"; from: Crop; x0: number; y0: number }
	| { kind: "resize"; corner: Corner }
	| { kind: "pinch"; dist: number };

const CORNERS: { corner: Corner; place: string; cursor: string }[] = [
	{ corner: "nw", place: "-top-3 -left-3", cursor: "cursor-nwse-resize" },
	{ corner: "ne", place: "-top-3 -right-3", cursor: "cursor-nesw-resize" },
	{ corner: "sw", place: "-bottom-3 -left-3", cursor: "cursor-nesw-resize" },
	{ corner: "se", place: "-right-3 -bottom-3", cursor: "cursor-nwse-resize" },
];

const pct = (v: number) => `${v * 100}%`;

function frameStyle(c: Crop, d: Dim): CSSProperties {
	return {
		left: pct(c.x / d.w),
		top: pct(c.y / d.h),
		width: pct(c.size / d.w),
		height: pct(c.size / d.h),
	};
}

export function AvatarCropDialog({
	source,
	onCancel,
	onConfirm,
}: {
	source: AvatarSource;
	onCancel: () => void;
	onConfirm: (dataUrl: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const d: Dim = { w: source.width, h: source.height };
	const [crop, setCrop] = useState(() => initialCrop(d));
	// 指针与滚轮回调在一帧里可能连发多次：以 ref 为准，state 只负责重绘
	const cropRef = useRef(crop);
	const update = (next: Crop) => {
		cropRef.current = next;
		setCrop(next);
	};
	const [error, setError] = useState<string | null>(null);
	const [radius, setRadius] = useState(FALLBACK_RADIUS);
	const hintId = useId();
	const stageRef = useRef<HTMLDivElement>(null);
	const frameRef = useRef<HTMLDivElement>(null);
	const probeRef = useRef<HTMLSpanElement>(null);
	const pointers = useRef(new Map<number, { x: number; y: number }>());
	const gesture = useRef<Gesture | null>(null);

	useLayoutEffect(() => {
		const probe = probeRef.current;
		if (!probe) return;
		const r = Number.parseFloat(getComputedStyle(probe).borderTopLeftRadius);
		if (r > 0) setRadius(r);
	}, []);

	/** 视口坐标 → 源图坐标；舞台还没有尺寸时为 null。 */
	const toSource = (clientX: number, clientY: number) => {
		const rect = stageRef.current?.getBoundingClientRect();
		if (!rect?.width || !rect.height) return null;
		return {
			x: ((clientX - rect.left) * source.width) / rect.width,
			y: ((clientY - rect.top) * source.height) / rect.height,
		};
	};
	const toSourceRef = useRef(toSource);
	toSourceRef.current = toSource;

	// 滚轮要 preventDefault（否则对话框正文跟着滚），React 的 onWheel 是被动监听，只能挂原生的
	useEffect(() => {
		const stage = stageRef.current;
		if (!stage) return;
		const dim = { w: source.width, h: source.height };
		const onWheel = (e: WheelEvent) => {
			e.preventDefault();
			const c = cropRef.current;
			const p = toSourceRef.current(e.clientX, e.clientY) ?? {
				x: c.x + c.size / 2,
				y: c.y + c.size / 2,
			};
			const next = zoomCrop(
				c,
				e.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP,
				p.x,
				p.y,
				dim,
			);
			cropRef.current = next;
			setCrop(next);
		};
		stage.addEventListener("wheel", onWheel, { passive: false });
		return () => stage.removeEventListener("wheel", onWheel);
	}, [source]);

	const pinchDistance = () => {
		const [a, b] = [...pointers.current.values()];
		return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : 0;
	};

	const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
		e.currentTarget.setPointerCapture?.(e.pointerId);
		pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
		if (pointers.current.size === 2) {
			gesture.current = { kind: "pinch", dist: pinchDistance() };
			return;
		}
		if (pointers.current.size > 2) return;
		const target = e.target as HTMLElement;
		const corner = target.closest<HTMLElement>("[data-corner]")?.dataset
			.corner as Corner | undefined;
		if (corner) gesture.current = { kind: "resize", corner };
		else if (frameRef.current?.contains(target))
			gesture.current = {
				kind: "move",
				from: cropRef.current,
				x0: e.clientX,
				y0: e.clientY,
			};
		else gesture.current = null;
	};

	const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
		if (!pointers.current.has(e.pointerId)) return;
		pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
		const g = gesture.current;
		if (!g) return;
		if (g.kind === "pinch") {
			const [a, b] = [...pointers.current.values()];
			const dist = pinchDistance();
			const mid = a && b && toSource((a.x + b.x) / 2, (a.y + b.y) / 2);
			if (mid && g.dist > 0 && dist > 0)
				update(zoomCrop(cropRef.current, g.dist / dist, mid.x, mid.y, d));
			g.dist = dist;
			return;
		}
		if (g.kind === "move") {
			const rect = stageRef.current?.getBoundingClientRect();
			if (!rect?.width) return;
			const scale = source.width / rect.width;
			update(
				moveCrop(
					g.from,
					(e.clientX - g.x0) * scale,
					(e.clientY - g.y0) * scale,
					d,
				),
			);
			return;
		}
		const p = toSource(e.clientX, e.clientY);
		if (p) update(resizeFromCorner(cropRef.current, g.corner, p.x, p.y, d));
	};

	const onPointerEnd = (e: PointerEvent<HTMLDivElement>) => {
		pointers.current.delete(e.pointerId);
		// 双指松开一指时不接着拖动，免得框跳到剩下那根手指的位置
		if (pointers.current.size === 0 || gesture.current?.kind === "pinch")
			gesture.current = null;
	};

	const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
		const c = cropRef.current;
		const step = Math.min(d.w, d.h) * (e.shiftKey ? 0.1 : 0.01);
		const cx = c.x + c.size / 2;
		const cy = c.y + c.size / 2;
		const moves: Record<string, [number, number]> = {
			ArrowLeft: [-step, 0],
			ArrowRight: [step, 0],
			ArrowUp: [0, -step],
			ArrowDown: [0, step],
		};
		const delta = moves[e.key];
		if (delta) update(moveCrop(c, delta[0], delta[1], d));
		else if (e.key === "+" || e.key === "=")
			update(zoomCrop(c, ZOOM_STEP, cx, cy, d));
		else if (e.key === "-" || e.key === "_")
			update(zoomCrop(c, 1 / ZOOM_STEP, cx, cy, d));
		else if (e.key === "0") update(initialCrop(d));
		else return;
		e.preventDefault();
	};

	const cancel = () => {
		source.release();
		onCancel();
	};

	const use = () => {
		let url: string;
		try {
			url = renderAvatar(source, cropRef.current);
		} catch (err) {
			setError(
				err instanceof AvatarTooLargeError
					? t("appearance.avatarTooLarge")
					: describeError(err, t),
			);
			return;
		}
		source.release();
		onConfirm(url);
	};

	const portrait = d.h > d.w;
	const box = frameStyle(crop, d);
	return (
		<Dialog
			open
			onClose={cancel}
			title={t("avatarCrop.title")}
			footer={
				<>
					<Button
						variant="ghost"
						icon="refresh"
						className="mr-auto"
						onClick={() => update(initialCrop(d))}
					>
						{t("avatarCrop.reset")}
					</Button>
					<Button onClick={cancel}>{t("avatarCrop.cancel")}</Button>
					<Button variant="primary" onClick={use}>
						{t("avatarCrop.use")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col items-center gap-3">
				<span
					ref={probeRef}
					aria-hidden
					className="pointer-events-none invisible absolute h-0 w-0 rounded-md"
				/>
				<div className="p-3">
					<div
						ref={stageRef}
						data-testid="crop-stage"
						className="relative touch-none select-none"
						style={{
							width: portrait
								? `calc(${STAGE_BASE} * ${d.w / d.h})`
								: STAGE_BASE,
							aspectRatio: `${d.w} / ${d.h}`,
						}}
						onPointerDown={onPointerDown}
						onPointerMove={onPointerMove}
						onPointerUp={onPointerEnd}
						onPointerCancel={onPointerEnd}
					>
						{/* 图与框外压暗在裁剪层里；取景框与手柄在外层，手柄命中区可越出图边 */}
						<div className="absolute inset-0 overflow-hidden rounded-xs bg-inset">
							<img
								src={source.url}
								alt=""
								draggable={false}
								className="absolute inset-0 h-full w-full"
							/>
							<div
								aria-hidden
								className="absolute shadow-[0_0_0_100vmax_var(--scrim)]"
								style={box}
							/>
						</div>
						{/* 取景框是自定义的二维控件（方向键平移、+ / - 缩放），没有对应的原生元素 */}
						{/* biome-ignore lint/a11y/useSemanticElements: 不是 fieldset，是可聚焦的键盘控件 */}
						<div
							ref={frameRef}
							data-testid="crop-frame"
							data-crop={`${Math.round(crop.x)},${Math.round(crop.y)},${Math.round(crop.size)}`}
							role="group"
							// biome-ignore lint/a11y/noNoninteractiveTabindex: 取景框要能聚焦才能用键盘操作
							tabIndex={0}
							aria-label={t("avatarCrop.frame")}
							aria-describedby={hintId}
							className="absolute cursor-move outline-offset-2"
							style={box}
							onKeyDown={onKeyDown}
						>
							<span
								aria-hidden
								className="pointer-events-none absolute inset-0 border-2 border-panel [filter:drop-shadow(0_0_1px_var(--text))]"
								style={{ borderRadius: pct(radius / TILE_SIZE) }}
							/>
							<span
								aria-hidden
								className="pointer-events-none absolute inset-0 rounded-full border-2 border-panel border-dashed [filter:drop-shadow(0_0_1px_var(--text))]"
							/>
							{CORNERS.map((h) => (
								<span
									key={h.corner}
									data-corner={h.corner}
									title={t("avatarCrop.handle")}
									className={`absolute grid h-6 w-6 place-items-center ${h.place} ${h.cursor}`}
								>
									<span className="pointer-events-none h-2.5 w-2.5 rounded-xs bg-panel ring-1 ring-fg" />
								</span>
							))}
						</div>
					</div>
				</div>
				<input
					type="range"
					min={1}
					max={MAX_ZOOM}
					step={0.01}
					value={zoomOf(crop, d)}
					aria-label={t("avatarCrop.zoom")}
					className="w-full max-w-80 accent-accent"
					onChange={(e) =>
						update(cropAtZoom(cropRef.current, Number(e.target.value), d))
					}
				/>
				<div className="flex items-start justify-center gap-6">
					<Preview
						source={source}
						crop={crop}
						shape="tile"
						label={t("avatarCrop.previewTile")}
					/>
					<Preview
						source={source}
						crop={crop}
						shape="circle"
						label={t("avatarCrop.previewCircle")}
					/>
				</div>
				<div className="flex flex-col gap-1 text-center text-faint text-xs">
					<p id={hintId}>{t("avatarCrop.hint")}</p>
					<p>{t("avatarCrop.outlines")}</p>
				</div>
				{error && (
					<div className="w-full">
						<Callout tone="err" role="alert">
							{error}
						</Callout>
					</div>
				)}
			</div>
		</Dialog>
	);
}

function Preview({
	source,
	crop,
	shape,
	label,
}: {
	source: AvatarSource;
	crop: Crop;
	shape: "tile" | "circle";
	label: string;
}): ReactNode {
	const scale = PREVIEW_SIZE / crop.size;
	return (
		<div
			data-testid={`crop-preview-${shape}`}
			className="flex flex-col items-center gap-1"
		>
			<span
				className={`relative block overflow-hidden ring-1 ring-border ${shape === "circle" ? "rounded-full" : "rounded-md"}`}
				style={{ width: PREVIEW_SIZE, height: PREVIEW_SIZE }}
			>
				<img
					src={source.url}
					alt=""
					draggable={false}
					className="absolute top-0 left-0 max-w-none origin-top-left"
					style={{
						width: source.width * scale,
						height: source.height * scale,
						transform: `translate(${-crop.x * scale}px, ${-crop.y * scale}px)`,
					}}
				/>
			</span>
			<span className="text-2xs text-faint">{label}</span>
		</div>
	);
}
