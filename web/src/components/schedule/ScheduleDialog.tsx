/**
 * 定时对话框（M9）：两种模式共用。
 * - 公共：三段切换（重复 cron / 间隔 / 一次）、cron 预设、时区、截止时间、提示词（列出占位符）、防抖预览接下来 5 次、
 *   启用开关、「立即运行」、「删除」、最近 20 次运行（`new_thread` 的每行带所建线程的链接；失败行显示 `error`）。
 * - `new_thread` 专有：名称（必填）、项目（含「无项目」；`lockProject` 或编辑时只读——目标不可改）、模型与思考级别、
 *   orb 类型（不含 `@host`）、重叠策略、保留数（1–50）。
 * 保存：新建走 `POST schedules`（线程内定时走 `PUT threads/{id}/schedule`），编辑走 `PUT schedules/{id}`；
 * 409 `schedule_name_taken` 标在名称框上。
 *
 * M9 审查修复：
 * - 版本条件（W1）：打开时记下定义版本 `baseRev`，保存带 `ifRev`（线程内定时此前没有时为 0）。编辑期间查询刷新带来
 *   新版本、或保存回 409 `schedule_conflict` → 表单上方出冲突条，草稿不动、保存禁用；「重新载入」以最新定义重建表单，
 *   「仍然保存」以最新版本为基线重发。
 * - 读取失败（W2）：GET 失败且手里没有数据时只给错误与重试，不落到新建表单（否则保存会覆盖现有定义）；
 *   指定了 id 的 404 显示「定时不存在」。线程内定时的 404 是「还没有」，照常进新建表单。
 * - 间隔支持秒（W3）；`at` / `endsAt` 明确是浏览器本地时间，「一次」不给时区选择、请求里的时区取浏览器时区（W5）。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ApiError, type Schemas } from "../../api/client";
import { useProjects, useProviders } from "../../api/queries";
import {
	previewSchedule,
	type Schedule,
	type ScheduleInput,
	type ScheduleRun,
	scheduleKeys,
	useCreateSchedule,
	useDeleteSchedule,
	usePutSchedule,
	usePutThreadSchedule,
	useRunSchedule,
	useSchedule,
	useScheduleRuns,
	useThreadSchedule,
} from "../../api/schedule-queries";
import { useSpaces } from "../../api/space-queries";
import { formatDateTime } from "../../lib/format";
import {
	browserZone,
	CRON_PRESETS,
	type EveryUnit,
	joinEvery,
	parseGoDuration,
	SCHEDULE_PLACEHOLDERS,
	splitEvery,
} from "../../lib/schedule";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { Badge, Callout, type Tone } from "../ui/data";
import { ModelSelect } from "../ui/ModelSelect";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	Field,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
	tabClass,
} from "../ui/primitives";

export type ScheduleTarget =
	| { mode: "thread"; threadId: string }
	| {
			mode: "new_thread";
			/** 新建时预填的项目；`null` = 无项目 */
			projectId?: string | null;
			/** 新建时固定的 Space（Space 页「定时」小节） */
			spaceId?: string;
			lockProject?: boolean;
	  };

const KINDS: Schemas["ScheduleKind"][] = ["cron", "every", "once"];
const EVERY_UNITS: EveryUnit[] = ["s", "m", "h", "d"];
const UNIT_LABEL = {
	s: "schedule.unit.seconds",
	m: "schedule.unit.m",
	h: "schedule.unit.h",
	d: "schedule.unit.d",
} as const satisfies Record<EveryUnit, string>;
const PREVIEW_DEBOUNCE_MS = 400;

/** 时区列表（`Intl.supportedValuesOf`），保证含 UTC、浏览器时区与当前值。 */
function zoneList(current: string): string[] {
	let all: string[] = [];
	try {
		all = Intl.supportedValuesOf("timeZone");
	} catch {
		// 旧浏览器：只给三个
	}
	return [...new Set(["UTC", browserZone(), current, ...all])];
}

/** 毫秒 → `<input type="datetime-local">` 的值（浏览器本地时间，到分钟）。 */
function toLocalInput(ms: number | undefined): string {
	if (ms === undefined) return "";
	const d = new Date(ms);
	const pad = (n: number) => String(n).padStart(2, "0");
	return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** `datetime-local` 的值按浏览器本地时间解释（字段标签写明了这一点）。 */
function fromLocalInput(v: string): number | undefined {
	if (!v) return undefined;
	const ms = new Date(v).getTime();
	return Number.isFinite(ms) ? ms : undefined;
}

interface FormState {
	kind: Schemas["ScheduleKind"];
	cron: string;
	everyValue: number;
	everyUnit: EveryUnit;
	at: string;
	timezone: string;
	endsAt: string;
	prompt: string;
	enabled: boolean;
	name: string;
	projectId: string;
	model: string;
	thinking: string;
	/** `provider/type`；空串 = 按建线程的回退链 */
	executor: string;
	overlap: Schemas["ScheduleOverlap"];
	keepLast: number;
}

function initialState(
	existing: Schedule | undefined,
	target: ScheduleTarget,
): FormState {
	const every = splitEvery(parseGoDuration(existing?.every) ?? 1800);
	const inHour = Math.ceil((Date.now() + 3600_000) / 300_000) * 300_000;
	return {
		kind: existing?.kind ?? "cron",
		cron: existing?.cron ?? CRON_PRESETS[0].cron,
		everyValue: every.value,
		everyUnit: every.unit,
		at: toLocalInput(existing?.at ?? inHour),
		timezone: existing?.timezone ?? browserZone(),
		endsAt: toLocalInput(existing?.endsAt),
		prompt: existing?.prompt ?? "",
		enabled: existing?.enabled ?? true,
		name: existing?.name ?? "",
		projectId:
			existing?.projectId ??
			(target.mode === "new_thread" && !existing
				? (target.projectId ?? "")
				: ""),
		model: existing?.model
			? `${existing.model.provider}/${existing.model.id}`
			: "",
		thinking: existing?.thinkingLevel ?? "",
		executor:
			existing?.provider && existing.orbType
				? `${existing.provider}/${existing.orbType}`
				: (existing?.provider ?? ""),
		overlap: existing?.overlap ?? "skip",
		keepLast: existing?.keepLast ?? 5,
	};
}

/**
 * 只含时间字段的定义（预览用）。`at` / `endsAt` 是浏览器本地时间；「一次」的时区取浏览器时区——
 * 绝对时间与时区无关，带上浏览器时区只是让服务端的显示与表单一致。
 */
function timing(s: FormState): ScheduleInput {
	const endsAt = fromLocalInput(s.endsAt);
	return {
		kind: s.kind,
		...(s.kind === "cron" ? { cron: s.cron.trim() } : {}),
		...(s.kind === "every"
			? { every: joinEvery(s.everyValue, s.everyUnit) }
			: {}),
		...(s.kind === "once" ? { at: fromLocalInput(s.at) } : {}),
		timezone: s.kind === "once" ? browserZone() : s.timezone,
		...(endsAt !== undefined && s.kind !== "once" ? { endsAt } : {}),
		prompt: "",
	};
}

/** 表单 → 请求体。线程内定时不带 mode / threadId（`PUT threads/{id}/schedule` 隐含）；编辑时目标字段与现值相同。 */
export function scheduleBody(
	s: FormState,
	target: ScheduleTarget,
	existing: Schedule | undefined,
): ScheduleInput {
	const base: ScheduleInput = {
		...timing(s),
		prompt: s.prompt,
		enabled: s.enabled,
		...(s.name.trim() ? { name: s.name.trim() } : {}),
	};
	if (target.mode === "thread") return base;
	const slash = s.model.indexOf("/");
	const exec = s.executor.indexOf("/");
	const spaceId = existing?.spaceId ?? target.spaceId;
	return {
		...base,
		mode: "new_thread",
		name: s.name.trim(),
		...(s.projectId ? { projectId: s.projectId } : {}),
		...(spaceId ? { spaceId } : {}),
		...(slash > 0
			? {
					model: {
						provider: s.model.slice(0, slash),
						id: s.model.slice(slash + 1),
					},
				}
			: {}),
		...(s.thinking ? { thinkingLevel: s.thinking } : {}),
		...(s.executor
			? exec > 0
				? {
						provider: s.executor.slice(0, exec),
						orbType: s.executor.slice(exec + 1),
					}
				: { provider: s.executor }
			: {}),
		overlap: s.overlap,
		keepLast: Math.min(50, Math.max(1, Math.round(s.keepLast))),
	};
}

const RUN_TONE: Record<Schemas["ScheduleRunStatus"], Tone> = {
	pending: "neutral",
	running: "accent",
	needs_input: "accent",
	completed: "ok",
	failed: "err",
	skipped: "neutral",
};

function RunRow({
	run,
	newThread,
}: {
	run: ScheduleRun;
	newThread: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const thread = useIndex((s) =>
		run.threadId ? s.threads[run.threadId] : undefined,
	);
	const title = thread
		? parseTitleCard(thread.title).text || t("thread.untitled")
		: run.threadId;
	return (
		<li
			data-run={run.id}
			data-status={run.status}
			className="flex flex-col gap-0.5 border-border border-b py-1.5 text-xs last:border-b-0"
		>
			<span className="flex min-w-0 items-center gap-2">
				<Badge tone={RUN_TONE[run.status]}>
					{t(`schedule.runStatus.${run.status}`)}
				</Badge>
				<span className="text-faint">
					{t(`schedule.trigger.${run.trigger}`)}
				</span>
				<span className="ml-auto shrink-0 text-muted tabular-nums">
					{formatDateTime(run.scheduledFor)}
				</span>
			</span>
			{newThread && run.threadId && (
				<Link
					to={`/t/${run.threadId}`}
					className="min-w-0 truncate text-accent-ink hover:underline"
				>
					{title}
				</Link>
			)}
			{run.missed > 0 && (
				<span className="text-faint">
					{t("schedule.missed", { n: run.missed })}
				</span>
			)}
			{run.error && <span className="break-words text-err">{run.error}</span>}
		</li>
	);
}

export function ScheduleDialog({
	scheduleId,
	target,
	onClose,
}: {
	scheduleId?: string;
	target: ScheduleTarget;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const threadMode = target.mode === "thread";
	const threadSchedule = useThreadSchedule(
		threadMode ? target.threadId : "",
		threadMode,
	);
	const one = useSchedule(threadMode ? undefined : scheduleId);
	// 当前用到的那个查询（新建「每次新建线程」定时时没有）
	const q = threadMode
		? {
				pending: threadSchedule.isPending,
				failed: threadSchedule.isError && threadSchedule.data === undefined,
				error: threadSchedule.error,
				fetching: threadSchedule.isFetching,
				refetch: threadSchedule.refetch,
			}
		: scheduleId
			? {
					pending: one.isPending,
					failed: one.isError && one.data === undefined,
					error: one.error,
					fetching: one.isFetching,
					refetch: one.refetch,
				}
			: null;
	if (q?.pending)
		return (
			<Dialog open onClose={onClose} title={t("schedule.title")}>
				<div className="flex justify-center py-6 text-muted">
					<Spinner />
				</div>
			</Dialog>
		);
	if (q?.failed) {
		// 读取失败不能当作「没有定时」：那会显示默认值的新建表单，保存时覆盖现有定义（W2）
		const notFound =
			!threadMode && q.error instanceof ApiError && q.error.status === 404;
		return (
			<Dialog
				open
				onClose={onClose}
				title={t("schedule.title")}
				footer={
					<>
						<Button onClick={onClose}>{t("common.close")}</Button>
						{!notFound && (
							<Button
								variant="primary"
								disabled={q.fetching}
								onClick={() => void q.refetch()}
							>
								{t("schedule.retry")}
							</Button>
						)}
					</>
				}
			>
				<div data-testid="schedule-load-error">
					<Callout
						tone={notFound ? "neutral" : "err"}
						role="alert"
						title={notFound ? t("schedule.notFound") : t("schedule.loadError")}
					>
						{notFound ? null : describeError(q.error, t)}
					</Callout>
				</div>
			</Dialog>
		);
	}
	const existing = threadMode ? (threadSchedule.data ?? undefined) : one.data;
	return (
		<ScheduleForm
			target={target}
			existing={existing}
			refreshing={q?.fetching ?? false}
			onClose={onClose}
		/>
	);
}

function ScheduleForm({
	target,
	existing,
	refreshing,
	onClose,
}: {
	target: ScheduleTarget;
	/** 最新的定义（查询刷新后随之更新；表单草稿不跟着变） */
	existing: Schedule | undefined;
	/** 定义查询正在重新读取 */
	refreshing: boolean;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [s, setS] = useState<FormState>(() => initialState(existing, target));
	const set = useCallback(
		<K extends keyof FormState>(k: K, v: FormState[K]) =>
			setS((prev) => ({ ...prev, [k]: v })),
		[],
	);
	const onThinking = useCallback((v: string) => set("thinking", v), [set]);
	const newThread = target.mode === "new_thread";
	// 版本：线程内定时「还没有」记作 0（`ifRev: 0` = 只在线程没有定时时新建）；新建「每次新建线程」定时不做版本检查
	const latestRev = existing?.rev ?? (newThread ? undefined : 0);
	const [baseRev, setBaseRev] = useState(latestRev);
	// 保存回了 409 schedule_conflict
	const [rejected, setRejected] = useState(false);
	// 保存已发出、还没失败：期间自己的保存带来的刷新不算冲突
	const [submitting, setSubmitting] = useState(false);
	const conflict =
		!submitting &&
		(rejected || (baseRev !== undefined && latestRev !== baseRev));
	const projects = useProjects();
	const providers = useProviders();
	const create = useCreateSchedule();
	const putOne = usePutSchedule(existing?.id ?? "");
	const putThread = usePutThreadSchedule(
		target.mode === "thread" ? target.threadId : "",
	);
	const del = useDeleteSchedule();
	const runNow = useRunSchedule();
	const runs = useScheduleRuns(existing?.id, (existing?.openRuns ?? 0) > 0);
	const saving = create.isPending || putOne.isPending || putThread.isPending;
	const saveError = create.error ?? putOne.error ?? putThread.error;
	const nameTaken =
		saveError instanceof ApiError && saveError.code === "schedule_name_taken";
	const conflictError =
		saveError instanceof ApiError && saveError.code === "schedule_conflict";
	const spaces = useSpaces();
	const spaceId =
		existing?.spaceId ??
		(target.mode === "new_thread" ? target.spaceId : undefined);
	const projectLocked =
		existing !== undefined ||
		(target.mode === "new_thread" && target.lockProject);
	const zone = browserZone();

	// 防抖预览：只看时间字段，变化后 400ms 才请求，前一个在途请求作废
	const timingKey = JSON.stringify(timing(s));
	const [preview, setPreview] = useState<
		{ next: number[] } | { error: unknown } | null
	>(null);
	const seq = useRef(0);
	useEffect(() => {
		const id = ++seq.current;
		const ctrl = new AbortController();
		const timer = setTimeout(() => {
			previewSchedule(JSON.parse(timingKey) as ScheduleInput, ctrl.signal).then(
				(r) => {
					if (seq.current === id) setPreview({ next: r.next });
				},
				(e: unknown) => {
					if (seq.current === id && !ctrl.signal.aborted)
						setPreview({ error: e });
				},
			);
		}, PREVIEW_DEBOUNCE_MS);
		return () => {
			clearTimeout(timer);
			ctrl.abort();
		};
	}, [timingKey]);

	const executors = useMemo(() => {
		const list = (providers.data?.providers ?? [])
			.filter((p) => p.name !== "@host")
			.flatMap((p) =>
				p.orbTypes.map((ot) => ({
					value: `${p.name}/${ot.name}`,
					label: `${p.name} · ${ot.name}`,
				})),
			);
		if (s.executor && !list.some((x) => x.value === s.executor))
			list.push({ value: s.executor, label: s.executor.replace("/", " · ") });
		return list;
	}, [providers.data, s.executor]);

	const save = async (ifRev: number | undefined) => {
		const body: ScheduleInput = {
			...scheduleBody(s, target, existing),
			...(ifRev !== undefined ? { ifRev } : {}),
		};
		setSubmitting(true);
		try {
			if (target.mode === "thread") await putThread.mutateAsync(body);
			else if (existing) await putOne.mutateAsync(body);
			else await create.mutateAsync(body);
		} catch (e) {
			setSubmitting(false);
			if (e instanceof ApiError && e.code === "schedule_conflict") {
				setRejected(true);
				// 取回最新定义：冲突条的两个动作都以它为准
				void qc.invalidateQueries({ queryKey: scheduleKeys.all });
			}
			return;
		}
		onClose();
	};
	/** 丢弃草稿，以最新定义重建表单。 */
	const reload = () => {
		setS(initialState(existing, target));
		setBaseRev(latestRev);
		setRejected(false);
		create.reset();
		putOne.reset();
		putThread.reset();
	};
	/** 以最新版本为基线重发草稿（覆盖别处的修改）。 */
	const overwrite = () => {
		setBaseRev(latestRev);
		setRejected(false);
		void save(latestRev);
	};
	const canSave =
		!saving &&
		!conflict &&
		s.prompt.trim() !== "" &&
		(!newThread || s.name.trim() !== "") &&
		(s.kind !== "cron" || s.cron.trim() !== "") &&
		(s.kind !== "once" || fromLocalInput(s.at) !== undefined);

	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={
				existing
					? t("schedule.editTitle")
					: newThread
						? t("schedule.newProjectTitle")
						: t("schedule.title")
			}
			footer={
				<>
					{existing && (
						<Button
							variant="danger"
							className="mr-auto"
							disabled={del.isPending}
							onClick={() => {
								if (!window.confirm(t("schedule.deleteConfirm"))) return;
								void del
									.mutateAsync(existing.id)
									.then(onClose, () => undefined);
							}}
						>
							{t("common.delete")}
						</Button>
					)}
					{existing && (
						<Button
							icon="play"
							disabled={runNow.isPending}
							onClick={() =>
								void runNow.mutateAsync(existing.id).catch(() => undefined)
							}
						>
							{t("schedule.runNow")}
						</Button>
					)}
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={!canSave}
						onClick={() => void save(baseRev)}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					if (canSave) void save(baseRev);
				}}
			>
				{conflict && (
					<div data-testid="schedule-conflict">
						<Callout
							tone="warn"
							role="alert"
							title={t("schedule.conflict.title")}
						>
							<span className="mt-1.5 flex flex-wrap gap-1.5">
								<Button size="sm" disabled={refreshing} onClick={reload}>
									{t("schedule.conflict.reload")}
								</Button>
								<Button
									size="sm"
									disabled={refreshing || saving}
									onClick={overwrite}
								>
									{t("schedule.conflict.overwrite")}
								</Button>
							</span>
						</Callout>
					</div>
				)}
				{existing?.pausedReason === "failures" && (
					<Callout tone="warn" title={t("schedule.autoPaused")}>
						{t("schedule.autoPausedHint", { n: existing.failures })}
					</Callout>
				)}
				{existing?.pausedReason === "archived" && (
					<Callout tone="neutral">{t("schedule.pausedArchived")}</Callout>
				)}
				<Field
					label={newThread ? t("schedule.name") : t("schedule.nameOptional")}
					hint={
						nameTaken
							? undefined
							: newThread
								? t("schedule.nameHint")
								: t("schedule.nameOptionalHint")
					}
				>
					<TextInput
						value={s.name}
						maxLength={80}
						aria-invalid={nameTaken || undefined}
						onChange={(e) => set("name", e.target.value)}
					/>
				</Field>
				{nameTaken && (
					<p role="alert" className="-mt-2 text-err text-xs">
						{t("schedule.nameTaken")}
					</p>
				)}
				{newThread && (
					// 新建线程模式：目标（项目、Space）紧跟名称，放在时间字段之前
					<div className="grid gap-3 sm:grid-cols-2">
						<Field label={t("schedule.project")}>
							<Select
								value={s.projectId}
								disabled={projectLocked}
								onChange={(e) => set("projectId", e.target.value)}
							>
								<option value="">{t("sidebar.noProject")}</option>
								{(projects.data?.projects ?? [])
									.filter((p) => !p.archivedAt || p.id === s.projectId)
									.map((p) => (
										<option key={p.id} value={p.id}>
											{p.name}
										</option>
									))}
							</Select>
						</Field>
						{spaceId && (
							<Field label={t("schedule.space")}>
								<TextInput
									readOnly
									value={
										spaces.data?.spaces?.find((x) => x.id === spaceId)?.name ??
										""
									}
								/>
							</Field>
						)}
					</div>
				)}
				<div
					role="radiogroup"
					aria-label={t("schedule.kindLabel")}
					className="flex gap-1 self-start rounded-lg bg-inset p-0.5"
				>
					{KINDS.map((k) => (
						// biome-ignore lint/a11y/useSemanticElements: 分段按钮（WAI-ARIA radio），原生 radio 画不出这种外观
						<button
							key={k}
							type="button"
							role="radio"
							aria-checked={s.kind === k}
							className={`${tabClass(s.kind === k)} ${s.kind === k ? "bg-panel shadow-xs" : ""}`}
							onClick={() => set("kind", k)}
						>
							{t(`schedule.kind.${k}`)}
						</button>
					))}
				</div>
				{s.kind === "cron" && (
					<div className="flex flex-col gap-2">
						<div className="flex flex-wrap gap-1.5">
							{CRON_PRESETS.map((p) => (
								<Button
									key={p.key}
									size="sm"
									variant={s.cron.trim() === p.cron ? "primary" : "secondary"}
									onClick={() => set("cron", p.cron)}
								>
									{t(`schedule.preset.${p.key}`)}
								</Button>
							))}
						</div>
						<Field label={t("schedule.cron")} hint={t("schedule.cronHint")}>
							<TextInput
								className="font-mono"
								value={s.cron}
								onChange={(e) => set("cron", e.target.value)}
							/>
						</Field>
					</div>
				)}
				{s.kind === "every" && (
					<Field label={t("schedule.every")}>
						<span className="flex gap-2">
							<TextInput
								type="number"
								min={1}
								className="w-24"
								aria-label={t("schedule.everyValue")}
								value={s.everyValue}
								onChange={(e) => set("everyValue", Number(e.target.value) || 1)}
							/>
							<Select
								wrapperClassName="w-32"
								aria-label={t("schedule.everyUnit")}
								value={s.everyUnit}
								onChange={(e) => set("everyUnit", e.target.value as EveryUnit)}
							>
								{EVERY_UNITS.map((u) => (
									<option key={u} value={u}>
										{t(UNIT_LABEL[u])}
									</option>
								))}
							</Select>
						</span>
					</Field>
				)}
				{s.kind === "once" && (
					<Field
						label={`${t("schedule.at")}${t("schedule.localTime", { zone })}`}
					>
						<TextInput
							type="datetime-local"
							value={s.at}
							onChange={(e) => set("at", e.target.value)}
						/>
					</Field>
				)}
				{s.kind !== "once" && (
					// 「一次」是绝对时间，时区不参与解释，不给选择
					<div className="grid gap-3 sm:grid-cols-2">
						<Field label={t("schedule.timezone")}>
							<Select
								value={s.timezone}
								onChange={(e) => set("timezone", e.target.value)}
							>
								{zoneList(s.timezone).map((z) => (
									<option key={z} value={z}>
										{z}
									</option>
								))}
							</Select>
						</Field>
						<Field
							label={`${t("schedule.endsAt")}${t("schedule.localTime", { zone })}`}
							hint={t("schedule.endsAtHint")}
						>
							<TextInput
								type="datetime-local"
								value={s.endsAt}
								onChange={(e) => set("endsAt", e.target.value)}
							/>
						</Field>
					</div>
				)}
				<div
					data-testid="schedule-preview"
					className="rounded-md bg-inset px-3 py-2 text-xs"
				>
					<div className="mb-1 font-medium text-muted">
						{t("schedule.preview")}
						<span className="font-normal text-faint">
							{t("schedule.localTime", { zone })}
						</span>
					</div>
					{preview === null ? (
						<Spinner size={12} />
					) : "error" in preview ? (
						<ErrorText error={preview.error} />
					) : preview.next.length === 0 ? (
						<span className="text-faint">{t("schedule.previewNone")}</span>
					) : (
						<ol className="flex flex-col gap-0.5 text-fg tabular-nums">
							{preview.next.map((ms) => (
								<li key={ms}>{formatDateTime(ms)}</li>
							))}
						</ol>
					)}
				</div>
				<Field
					label={t("schedule.prompt")}
					hint={
						<span>
							{t("schedule.placeholders")}{" "}
							{SCHEDULE_PLACEHOLDERS.map((p) => (
								<code key={p} className="mr-1 font-mono">
									{p}
								</code>
							))}
						</span>
					}
				>
					<TextArea
						rows={4}
						className="font-sans"
						value={s.prompt}
						onChange={(e) => set("prompt", e.target.value)}
					/>
				</Field>
				{newThread && (
					<>
						<div className="grid gap-3 sm:grid-cols-2">
							<Field label={t("schedule.orbType")}>
								<Select
									value={s.executor}
									onChange={(e) => set("executor", e.target.value)}
								>
									<option value="">{t("newThread.newOrbDefault")}</option>
									{executors.map((x) => (
										<option key={x.value} value={x.value}>
											{x.label}
										</option>
									))}
								</Select>
							</Field>
						</div>
						<div className="flex flex-col gap-1 text-sm">
							<span className="font-medium text-muted text-xs">
								{t("schedule.modelLabel")}
							</span>
							<ModelSelect
								model={s.model}
								thinking={s.thinking}
								onModel={(m) => set("model", m)}
								onThinking={onThinking}
							/>
						</div>
						<div className="grid gap-3 sm:grid-cols-2">
							<Field label={t("schedule.overlapLabel")}>
								<Select
									value={s.overlap}
									onChange={(e) =>
										set("overlap", e.target.value as Schemas["ScheduleOverlap"])
									}
								>
									<option value="skip">{t("schedule.overlap.skip")}</option>
									<option value="allow">{t("schedule.overlap.allow")}</option>
								</Select>
							</Field>
							<Field
								label={t("schedule.keepLast")}
								hint={t("schedule.keepLastHint")}
							>
								<TextInput
									type="number"
									min={1}
									max={50}
									value={s.keepLast}
									onChange={(e) => set("keepLast", Number(e.target.value) || 1)}
								/>
							</Field>
						</div>
					</>
				)}
				<Toggle
					checked={s.enabled}
					onChange={(v) => set("enabled", v)}
					label={t("schedule.enabled")}
				/>
				{!nameTaken && !conflictError && <ErrorText error={saveError} />}
				<ErrorText error={runNow.error ?? del.error} />
				{existing && (
					<section className="flex flex-col gap-1">
						<h3 className="font-medium text-muted text-xs">
							{t("schedule.runs")}
						</h3>
						{runs.isPending ? (
							<Spinner size={12} />
						) : (runs.data?.runs ?? []).length === 0 ? (
							<p className="text-faint text-xs">{t("schedule.noRuns")}</p>
						) : (
							<ul data-testid="schedule-runs" className="flex flex-col">
								{(runs.data?.runs ?? []).slice(0, 20).map((r) => (
									<RunRow key={r.id} run={r} newThread={newThread} />
								))}
							</ul>
						)}
					</section>
				)}
			</form>
		</Dialog>
	);
}
