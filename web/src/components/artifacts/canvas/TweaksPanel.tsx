/**
 * 画框的「调节」面板（计划 M14 6.5）：列出选中画框声明的调节参数，按类型给颜色、文本、滑块、开关、下拉。
 *
 * - 参数声明来自画框页面运行时的 `ready.params`（`useFrameParams` 在每个画框的通道上收集，再用 `parseParamsDecl`
 *   校验一遍）；只有跟随最新版本、已加载好的画框有。
 * - 改动立即经帧通道发 `params {values}` 预览；停手 1000 ms 后 `PUT …/params` 保存，落成一个作者为「你」的版本。
 *   保存串行排队，`baseVersion` 取「画框回报声明时的版本」与「上一次保存返回的版本」中较大的。
 * - 换画框或面板关掉时把还没保存的改动立即保存。保存失败时把预览还原成画框里的值。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { ParentMessageBody } from "../../../../sandbox/runtime/messages";
import { setArtifactParams } from "../../../api/artifact-queries";
import type { CanvasPage } from "../../../lib/artifacts/canvas-doc";
import {
	coerceParamValue,
	type PageParam,
	type ParamValue,
	parseParamsDecl,
} from "../../../lib/artifacts/params";
import {
	describeError,
	Field,
	Select,
	Spinner,
	TextInput,
	Toggle,
} from "../../ui/primitives";
import type { FrameLink } from "../frame-link";

/** 停手多久后保存。 */
export const TWEAK_SAVE_DELAY_MS = 1000;

/** 一个画框回报的参数声明，以及收到时 artifact 的最新版本（保存时的 `baseVersion`）。 */
export interface FrameParams {
	params: PageParam[];
	version: number;
}

export interface FrameParamsHub {
	/** 画框通道建立 / 拆除时调用（接在 `CanvasEditor` 的 `onLink` 上）。 */
	track(pageId: string, link: FrameLink | null): void;
	/** 已回报声明的画框。 */
	byPage: Readonly<Record<string, FrameParams>>;
	/** 画框声明了至少一个参数。 */
	declared(pageId: string): boolean;
	/** 发给某个画框里的页面；画框没加载时丢弃。 */
	post(pageId: string, body: ParentMessageBody): void;
}

/** 收集每个画框 `ready` 消息里的参数声明。`head` 是 artifact 的最新版本。 */
export function useFrameParams(head: number): FrameParamsHub {
	const [byPage, setByPage] = useState<Record<string, FrameParams>>({});
	const links = useRef(new Map<string, FrameLink>());
	const offs = useRef(new Map<string, () => void>());
	const headRef = useRef(head);
	headRef.current = head;

	const track = useCallback((pageId: string, link: FrameLink | null) => {
		offs.current.get(pageId)?.();
		offs.current.delete(pageId);
		if (!link) {
			links.current.delete(pageId);
			setByPage((cur) => {
				if (!(pageId in cur)) return cur;
				const { [pageId]: _, ...rest } = cur;
				return rest;
			});
			return;
		}
		links.current.set(pageId, link);
		offs.current.set(
			pageId,
			link.on("ready", (m) => {
				const params = parseParamsDecl(m.params);
				const version = headRef.current;
				setByPage((cur) => ({ ...cur, [pageId]: { params, version } }));
			}),
		);
	}, []);

	return useMemo(
		() => ({
			track,
			byPage,
			declared: (pageId: string) => (byPage[pageId]?.params.length ?? 0) > 0,
			post: (pageId: string, body: ParentMessageBody) =>
				links.current.get(pageId)?.post(body),
		}),
		[track, byPage],
	);
}

type SaveStatus =
	| { kind: "idle" }
	| { kind: "saving" }
	| { kind: "saved"; version: number }
	| { kind: "failed"; reason: string };

export interface TweaksPanelProps {
	artifactId: string;
	page: CanvasPage;
	/** 画框回报的声明；还没加载好时为空。 */
	frame: FrameParams | undefined;
	post(body: ParentMessageBody): void;
}

/** 一个画框的调节面板；换画框 = 换 key 重建（卸载时把没保存的改动立即保存）。 */
export function TweaksPanel({
	artifactId,
	page,
	frame,
	post,
}: TweaksPanelProps): ReactNode {
	const { t } = useTranslation();
	// 用户改过、画框还没以新值回报的参数；ref 与 state 同步写，事件与异步回调里读 ref
	const [draft, setDraftState] = useState<Record<string, ParamValue>>({});
	const draftRef = useRef(draft);
	const setDraft = useRef((next: Record<string, ParamValue>) => {
		draftRef.current = next;
		setDraftState(next);
	}).current;
	const [status, setStatus] = useState<SaveStatus>({ kind: "idle" });
	const pending = useRef<Record<string, ParamValue>>({});
	const timer = useRef<number | undefined>(undefined);
	const queue = useRef<Promise<void>>(Promise.resolve());
	const lastSaved = useRef(0);
	const inflight = useRef(0);
	const live = useRef({ artifactId, path: page.file, frame, post, t });
	live.current = { artifactId, path: page.file, frame, post, t };

	const save = useRef((values: Record<string, ParamValue>) => {
		inflight.current++;
		setStatus({ kind: "saving" });
		queue.current = queue.current.then(async () => {
			const cur = live.current;
			let next: SaveStatus;
			try {
				const version = await setArtifactParams(cur.artifactId, {
					baseVersion: Math.max(cur.frame?.version ?? 0, lastSaved.current),
					path: cur.path,
					values,
				});
				lastSaved.current = Math.max(lastSaved.current, version);
				next = { kind: "saved", version };
			} catch (e) {
				next = { kind: "failed", reason: describeError(e, cur.t) };
				// 没有被更新的改动盖过的项还原成画框里的值：预览与输入框都回到保存前
				const failed = Object.keys(values).filter(
					(name) => draftRef.current[name] === values[name],
				);
				const restore: Record<string, ParamValue> = {};
				for (const p of live.current.frame?.params ?? [])
					if (failed.includes(p.name)) restore[p.name] = p.value;
				if (Object.keys(restore).length > 0)
					live.current.post({ kind: "params", values: restore });
				const out = { ...draftRef.current };
				for (const name of failed) delete out[name];
				setDraft(out);
			} finally {
				inflight.current--;
			}
			setStatus(inflight.current > 0 ? { kind: "saving" } : next);
		});
	});

	const flush = useRef(() => {
		clearTimeout(timer.current);
		timer.current = undefined;
		const values = pending.current;
		if (Object.keys(values).length === 0) return;
		pending.current = {};
		save.current(values);
	});

	// 卸载（换画框、关面板）时立即保存没保存的改动
	useEffect(() => {
		const f = flush.current;
		return () => f();
	}, []);

	// 画框重新回报声明（保存出的新版本到了、或页面重载）：已经一致的草稿清掉，其余重新预览
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只在画框回报新声明时重发；post 每次渲染都是新函数
	useEffect(() => {
		if (!frame) return;
		const out: Record<string, ParamValue> = {};
		for (const p of frame.params) {
			const v = draftRef.current[p.name];
			if (v !== undefined && v !== p.value) out[p.name] = v;
		}
		setDraft(out);
		if (Object.keys(out).length > 0) post({ kind: "params", values: out });
	}, [frame]);

	const change = (p: PageParam, raw: unknown) => {
		const v = coerceParamValue(p, raw);
		if (v === null) return;
		setDraft({ ...draftRef.current, [p.name]: v });
		pending.current = { ...pending.current, [p.name]: v };
		post({ kind: "params", values: { [p.name]: v } });
		clearTimeout(timer.current);
		timer.current = window.setTimeout(
			() => flush.current(),
			TWEAK_SAVE_DELAY_MS,
		);
	};

	if (!frame)
		return (
			<p className="px-3 py-4 text-faint text-sm">
				{t("artifacts.canvas.props.tweaksWaiting")}
			</p>
		);
	if (frame.params.length === 0)
		return (
			<p className="px-3 py-4 text-faint text-sm">
				{t("artifacts.canvas.props.tweaksEmpty")}
			</p>
		);

	return (
		<div data-testid="tweaks-panel" className="flex flex-col">
			<div className="flex flex-col gap-3 px-3 py-3">
				{frame.params.map((p) => (
					<ParamField
						key={p.name}
						param={p}
						value={draft[p.name] ?? p.value}
						onChange={(v) => change(p, v)}
					/>
				))}
			</div>
			<div
				role="status"
				aria-live="polite"
				data-testid="tweaks-status"
				className="flex min-h-9 items-center gap-2 border-border border-t px-3 text-muted text-xs"
			>
				{status.kind === "saving" && (
					<>
						<Spinner size={12} />
						{t("artifacts.canvas.props.tweaksSaving")}
					</>
				)}
				{status.kind === "saved" &&
					t("artifacts.canvas.props.tweaksSaved", { n: status.version })}
				{status.kind === "failed" && (
					<span className="text-err">
						{t("artifacts.canvas.props.tweaksFailed", {
							reason: status.reason,
						})}
					</span>
				)}
			</div>
		</div>
	);
}

/** `<input type="color">` 只认 #rrggbb：#rgb / #rgba 展开、去掉透明度。 */
function pickerColor(v: string): string {
	const hex = v.slice(1);
	const six =
		hex.length <= 4
			? Array.from(hex.slice(0, 3), (c) => c + c).join("")
			: hex.slice(0, 6);
	return `#${six.toLowerCase()}`;
}

function ParamField({
	param,
	value,
	onChange,
}: {
	param: PageParam;
	value: ParamValue;
	onChange(v: unknown): void;
}): ReactNode {
	const { t } = useTranslation();
	const label = param.label || param.name;
	switch (param.type) {
		case "color":
			return (
				<TextLikeField
					label={label}
					param={param}
					value={String(value)}
					onChange={onChange}
					picker={
						<input
							type="color"
							aria-label={t("artifacts.canvas.props.tweaksPicker", { label })}
							className="h-8 w-10 shrink-0 cursor-pointer rounded-md border border-border bg-transparent p-0.5"
							value={pickerColor(String(value))}
							onChange={(e) => onChange(e.target.value)}
						/>
					}
				/>
			);
		case "text":
			return (
				<TextLikeField
					label={label}
					param={param}
					value={String(value)}
					onChange={onChange}
				/>
			);
		case "number":
			return (
				<Field
					label={
						<span className="flex justify-between gap-2">
							<span>{label}</span>
							<span className="tabular-nums">
								{String(value)}
								{param.unit}
							</span>
						</span>
					}
				>
					<input
						type="range"
						className="w-full accent-[var(--accent)]"
						min={param.min}
						max={param.max}
						step={param.step}
						value={Number(value)}
						onChange={(e) => onChange(Number(e.target.value))}
					/>
				</Field>
			);
		case "boolean":
			return (
				<Toggle checked={value === true} onChange={onChange} label={label} />
			);
		case "choice":
			return (
				<Field label={label}>
					<Select
						value={String(value)}
						onChange={(e) => onChange(e.target.value)}
					>
						{param.options.map((o) => (
							<option key={o} value={o}>
								{o}
							</option>
						))}
					</Select>
				</Field>
			);
	}
}

/**
 * 颜色（带取色器）与文本：输入框里不合规的值（颜色写法不对、文本超长）不套用，标出来。
 * 不在输入时跟着外面的值走（取色器改了、画框回报了新值）。
 */
function TextLikeField({
	label,
	param,
	value,
	picker,
	onChange,
}: {
	label: string;
	param: PageParam;
	value: string;
	picker?: ReactNode;
	onChange(v: unknown): void;
}): ReactNode {
	const { t } = useTranslation();
	const [raw, setRaw] = useState(value);
	const [focused, setFocused] = useState(false);
	const shown = focused ? raw : value;
	const color = param.type === "color";
	const valid = coerceParamValue(param, color ? shown.trim() : shown) !== null;
	return (
		<Field
			label={label}
			hint={valid ? undefined : t("artifacts.canvas.props.tweaksInvalid")}
		>
			<span className="flex items-center gap-2">
				{picker}
				<TextInput
					className={color ? "font-mono" : ""}
					value={shown}
					aria-invalid={!valid}
					onFocus={() => {
						setRaw(value);
						setFocused(true);
					}}
					onBlur={() => setFocused(false)}
					onChange={(e) => {
						setRaw(e.target.value);
						onChange(color ? e.target.value.trim() : e.target.value);
					}}
				/>
			</span>
		</Field>
	);
}
