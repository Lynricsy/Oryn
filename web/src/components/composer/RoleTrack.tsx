/**
 * 角色模型轮换：Ctrl+P / Shift+Ctrl+P（mac ⌘P）按 omp 的 cycleOrder 切换角色模型，切换时在输入坞上方浮出角色轨道。
 * 解析规则在 `lib/model-roles.ts`；这里负责取数（get_settings / get_model_roles 走线程，React Query 缓存；
 * 解析别名与回退链用的模型列表取中央模型目录 `useModelCatalog`）、
 * 切换（`lib/model-select.ts`：新 Core 发 oryn_select_model，旧 Core 回退 set_model + set_thinking_level；按序合并连按）、
 * 轨道动画与读屏播报。当前格：线程里有来源记录时只认记录（`recordedRoleIndex`，不读写本机记忆）；
 * 没有记录（旧 Core）时沿用本机记忆 + 现有的匹配逻辑。模型对话框里的角色列表（RolePicker.tsx）走同一套取数与切换。
 */
import {
	type QueryClient,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import {
	modelCatalogQuery,
	useModelCatalog,
} from "../../api/model-catalog-queries";
import i18n from "../../i18n";
import {
	BUILTIN_ROLE_NAMES,
	type BuiltinRole,
	buildRoleSlots,
	type CurrentModel,
	currentRoleIndex,
	lastModelChangeRole,
	type ModelRef,
	parseCycleOrder,
	type RoleEntry,
	type RoleMemory,
	type RoleSlot,
	recordedRoleIndex,
	roleHue,
	slotSelector,
	stepRole,
} from "../../lib/model-roles";
import { selectRole } from "../../lib/model-select";
import { type SourceTrail, trailOf } from "../../lib/turn-role";
import type { OmpCommand, SessionState } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { type AccentName, useTheme } from "../../state/theme";
import { activeBranch } from "../../state/transcript";
import { uiStore } from "../../state/ui";
import { ModelLogo } from "../ui/ModelLogo";
import { describeError } from "../ui/primitives";

// ---------------------------------------------------------------- 取数

const SETTINGS_FRAME: OmpCommand = {
	type: "get_settings",
	paths: ["cycleOrder"],
};
const ROLES_FRAME: OmpCommand = { type: "get_model_roles" };
/** 角色配置很少变：30 秒内复用缓存，过期后下次按键或打开对话框时重取。 */
const STALE_MS = 30_000;

/** 与 `Dialogs.tsx` 的 `useRpc` 同一种 key，两边共用缓存。 */
const rpcKey = (threadId: string, frame: OmpCommand) =>
	["rpc", threadId, frame] as const;

interface SettingsData {
	values?: Record<string, unknown>;
}
interface RolesData {
	roles?: RoleEntry[];
}

function rpcQuery<T>(threadId: string, frame: OmpCommand) {
	return {
		queryKey: rpcKey(threadId, frame),
		queryFn: async () => ((await realtime.cmd(threadId, frame)) ?? {}) as T,
		staleTime: STALE_MS,
		retry: false,
	};
}

/** 有配置值却没有 `selected` 的角色（别名、回退链、不在候选里）需要模型目录来解析。 */
const needsModels = (entries: readonly RoleEntry[]) =>
	entries.some((e) => !e.selected && !!e.model);

interface RoleData {
	order: string[];
	entries: RoleEntry[];
	available: ModelRef[];
}

/** 按键时取一次数据（缓存未过期就直接用）。旧版 omp 没有 get_settings 时用默认顺序。 */
async function fetchRoleData(
	qc: QueryClient,
	threadId: string,
): Promise<RoleData> {
	const [settings, roles] = await Promise.all([
		qc
			.fetchQuery(rpcQuery<SettingsData>(threadId, SETTINGS_FRAME))
			.catch(() => undefined),
		qc.fetchQuery(rpcQuery<RolesData>(threadId, ROLES_FRAME)),
	]);
	const entries = roles.roles ?? [];
	// 目录含全部种类（tiny 等角色可能指向非 chat 模型）；读不到时只靠 `selected` 解析
	const available = needsModels(entries)
		? ((await qc.fetchQuery(modelCatalogQuery()).catch(() => undefined))
				?.models ?? [])
		: [];
	return {
		order: parseCycleOrder(settings?.values?.cycleOrder),
		entries,
		available,
	};
}

function currentOf(
	state: SessionState | null | undefined,
): CurrentModel | undefined {
	const m = state?.model;
	if (!m) return undefined;
	return {
		provider: m.provider,
		id: m.id,
		name: m.name,
		thinkingLevel: state.thinkingConfigured ?? state.thinkingLevel,
	};
}

/** 不是给人手动切换用的内置角色（审阅、子任务、提交信息、标题、记忆、任务小组件）：对话框里收进「更多」。 */
const SYSTEM_ROLES: Record<string, true> = {
	advisor: true,
	task: true,
	commit: true,
	tiny: true,
	memory: true,
	widget: true,
};

interface RoleGroups {
	cycle: RoleSlot[];
	others: RoleSlot[];
	more: RoleSlot[];
}

/** 轮换轨道（cycleOrder）+ 对话框额外列出的可见 chat 角色。 */
function groupSlots(
	data: RoleData,
	cur: CurrentModel | undefined,
	withExtras: boolean,
): RoleGroups {
	const inOrder = new Set(data.order);
	const extras = withExtras
		? data.entries.filter(
				(e) => e.section === "chat" && !e.hidden && !inOrder.has(e.id),
			)
		: [];
	const othersIds = extras
		.filter((e) => !Object.hasOwn(SYSTEM_ROLES, e.id))
		.map((e) => e.id);
	const moreIds = extras
		.filter((e) => Object.hasOwn(SYSTEM_ROLES, e.id))
		.map((e) => e.id);
	const all = buildRoleSlots(
		[...data.order, ...othersIds, ...moreIds],
		data.entries,
		data.available,
		cur,
	);
	const n = all.length - othersIds.length - moreIds.length;
	return {
		cycle: all.slice(0, n),
		others: all.slice(n, n + othersIds.length),
		more: all.slice(n + othersIds.length),
	};
}

// ---------------------------------------------------------------- 切换状态（按线程）

interface RoleThreadState {
	/** 已请求、尚未生效的目标角色：轨道立刻滑到它。 */
	pending?: string;
	/** 每次切换请求（或「只有一个角色」的提示）递增，驱动轨道浮出与托盘闪色。 */
	flash: number;
	announce: string;
	message?: { tone: "info" | "error"; text: string };
}

interface RoleStoreState {
	threads: Record<string, RoleThreadState>;
	/** 本地记住的上一次选择（同时写 localStorage，按线程）。 */
	memory: Record<string, RoleMemory | null>;
}

const roleStore = createStore<RoleStoreState>(() => ({
	threads: {},
	memory: {},
}));

const EMPTY_THREAD: RoleThreadState = { flash: 0, announce: "" };

function patchThread(
	threadId: string,
	patch: (s: RoleThreadState) => Partial<RoleThreadState>,
) {
	roleStore.setState((st) => {
		const cur = st.threads[threadId] ?? EMPTY_THREAD;
		return {
			threads: { ...st.threads, [threadId]: { ...cur, ...patch(cur) } },
		};
	});
}

const MEMORY_KEY = (threadId: string) => `oryn.role.${threadId}`;

function readMemory(threadId: string): RoleMemory | undefined {
	const st = roleStore.getState().memory;
	if (Object.hasOwn(st, threadId)) return st[threadId] ?? undefined;
	let mem: RoleMemory | null = null;
	try {
		const raw = globalThis.localStorage?.getItem(MEMORY_KEY(threadId));
		const v = raw ? (JSON.parse(raw) as Partial<RoleMemory>) : null;
		if (
			v &&
			typeof v.role === "string" &&
			typeof v.provider === "string" &&
			typeof v.id === "string"
		)
			mem = {
				role: v.role,
				provider: v.provider,
				id: v.id,
				thinkingLevel:
					typeof v.thinkingLevel === "string" ? v.thinkingLevel : undefined,
			};
	} catch {
		// 隐私模式或内容损坏：当作没有记忆
	}
	roleStore.setState((s) => ({ memory: { ...s.memory, [threadId]: mem } }));
	return mem ?? undefined;
}

function writeMemory(threadId: string, mem: RoleMemory) {
	roleStore.setState((s) => ({ memory: { ...s.memory, [threadId]: mem } }));
	try {
		globalThis.localStorage?.setItem(MEMORY_KEY(threadId), JSON.stringify(mem));
	} catch {
		// 忽略
	}
}

/** 测试用：清空切换状态与记忆。 */
export function resetRoleState() {
	roleStore.setState({ threads: {}, memory: {} });
}

/** 角色显示名：内置角色且没被用户改名时本地化，其余（自定义角色、改过名的）原样显示。 */
export function roleLabel(slot: Pick<RoleSlot, "id" | "name">): string {
	return Object.hasOwn(BUILTIN_ROLE_NAMES, slot.id) &&
		BUILTIN_ROLE_NAMES[slot.id as BuiltinRole] === slot.name
		? i18n.t(`roles.names.${slot.id as BuiltinRole}`)
		: slot.name;
}

/** 禁用格的悬停说明。 */
export function slotProblem(slot: RoleSlot): string | undefined {
	if (slot.status === "unresolved")
		return i18n.t("roles.unresolved", { raw: slot.raw ?? "" });
	if (slot.status === "unconfigured") return i18n.t("roles.unconfigured");
	return undefined;
}

async function refreshState(threadId: string) {
	const state = (await realtime.cmd(threadId, {
		type: "get_state",
	})) as SessionState;
	getThreadStore(threadId)
		.getState()
		.apply([{ type: "state", state }]);
}

/** 新 Core 切换后等来源记录入库的上限：记录到了轨道才从「在途」落到「当前」，不闪一下空白。 */
const RECORD_WAIT_MS = 3000;

/** 等线程活动分支末端的来源记录指向 `role`（最多 RECORD_WAIT_MS，超时照常结束）。 */
function waitForRecord(threadId: string, role: string): Promise<void> {
	const store = getThreadStore(threadId);
	const hit = () => {
		const s = store.getState();
		return (
			trailOf(activeBranch(s.entries, s.leafEntryId)).record?.role === role
		);
	};
	if (hit()) return Promise.resolve();
	return new Promise((resolve) => {
		const done = () => {
			clearTimeout(timer);
			unsubscribe();
			resolve();
		};
		const timer = setTimeout(done, RECORD_WAIT_MS);
		const unsubscribe = store.subscribe(() => {
			if (hit()) done();
		});
	});
}

/**
 * 当前角色在 `slots` 里的下标：有来源记录的线程只认记录（不读本机记忆）；
 * 没有记录（旧 Core）的线程沿用本机记忆 + 最后一条 model_change 的 role + 模型匹配。
 */
function roleIndex(
	slots: readonly RoleSlot[],
	cur: CurrentModel | undefined,
	threadId: string,
	trail: SourceTrail,
	lastRole: string | undefined,
): number {
	return trail.tracked
		? recordedRoleIndex(slots, cur, trail)
		: currentRoleIndex(slots, cur, readMemory(threadId), lastRole);
}

/**
 * 每个线程一个串行的切换工作者：连按时只保留最新目标，在途的一次做完后再切到它（不乱序、不堆积）。
 * 刷新状态也在循环里：刷新期间排入的新目标在刷新返回后接着处理，队列确实为空才结束并清 `pending`。
 * 新 Core 路径在清 `pending` 之前等来源记录入库；旧 Core 路径没有记录，改写本机记忆。
 */
const workers = new Map<string, { want?: RoleSlot; running: boolean }>();

async function drain(threadId: string) {
	const w = workers.get(threadId);
	if (!w || w.running) return;
	w.running = true;
	let last: RoleSlot | undefined;
	let lastLegacy = false;
	try {
		for (;;) {
			const slot = w.want;
			if (slot) {
				w.want = undefined;
				const model = slot.model;
				if (!model) continue;
				const { legacy } = await selectRole(threadId, slot);
				if (legacy)
					writeMemory(threadId, {
						role: slot.id,
						provider: model.provider,
						id: model.id,
						thinkingLevel: slot.thinkingLevel,
					});
				last = slot;
				lastLegacy = legacy;
				continue;
			}
			if (!last) break;
			await refreshState(threadId);
			// 刷新期间又排入了新目标：先切过去，最后再刷新一次
			if (w.want) continue;
			if (!lastLegacy) {
				await waitForRecord(threadId, last.id);
				if (w.want) continue;
			}
			const role = roleLabel(last);
			const model = last.model?.name || last.model?.id || "";
			patchThread(threadId, () => ({
				announce: i18n.t("roles.switched", { role, model }),
			}));
			break;
		}
	} catch (e) {
		w.want = undefined;
		patchThread(threadId, () => ({
			message: { tone: "error", text: describeError(e, i18n.t) },
		}));
	} finally {
		w.running = false;
		patchThread(threadId, () => ({ pending: undefined }));
	}
}

/** 切到某个角色：轨道立刻滑过去，命令按序发出。Ctrl+P、轨道点击与模型对话框共用。 */
export function requestRole(threadId: string, slot: RoleSlot) {
	if (slot.status !== "ok") return;
	patchThread(threadId, (s) => ({
		pending: slot.id,
		flash: s.flash + 1,
		message: undefined,
	}));
	let w = workers.get(threadId);
	if (!w) {
		w = { running: false };
		workers.set(threadId, w);
	}
	w.want = slot;
	void drain(threadId);
}

/** 按方向切到下一个可用角色（omp `cycleRoleModels`）。 */
export async function cycleRole(
	qc: QueryClient,
	threadId: string,
	dir: 1 | -1,
) {
	let data: RoleData;
	try {
		data = await fetchRoleData(qc, threadId);
	} catch (e) {
		patchThread(threadId, (s) => ({
			flash: s.flash + 1,
			message: { tone: "error", text: describeError(e, i18n.t) },
		}));
		return;
	}
	const ts = getThreadStore(threadId).getState();
	const cur = currentOf(ts.state);
	const { cycle } = groupSlots(data, cur, false);
	const pending = roleStore.getState().threads[threadId]?.pending;
	const from = pending
		? cycle.findIndex((s) => s.id === pending)
		: roleIndex(
				cycle,
				cur,
				threadId,
				trailOf(activeBranch(ts.entries, ts.leafEntryId)),
				lastModelChangeRole(ts.entries),
			);
	const next = stepRole(cycle, from, dir);
	const slot = next === undefined ? undefined : cycle[next];
	if (!slot) {
		patchThread(threadId, (s) => ({
			flash: s.flash + 1,
			message: { tone: "info", text: i18n.t("roles.onlyOne") },
		}));
		return;
	}
	requestRole(threadId, slot);
}

// ---------------------------------------------------------------- 视图数据

export interface RoleView extends RoleGroups {
	isPending: boolean;
	error: unknown;
	/** 当前角色（cycleOrder 内外都算，cycleOrder 优先命中）。 */
	currentId?: string;
	/** 轨道里当前高亮的格：在途目标优先，否则当前角色。 */
	trackId?: string;
	/** 当前实际生效的思考级别（与模型对话框的思考级别下拉框同源），可能被手动改得和角色预设不同。 */
	currentLevel?: string;
	/** 当前主题色：轨道色板据此跳过和强调色相近的色相。 */
	accent: AccentName;
}

/** 角色视图：`withExtras` 时额外列出 cycleOrder 以外的可见 chat 角色（模型对话框用）。 */
export function useRoleView(
	threadId: string,
	enabled: boolean,
	withExtras = false,
): RoleView {
	const settings = useQuery({
		...rpcQuery<SettingsData>(threadId, SETTINGS_FRAME),
		enabled,
	});
	const roles = useQuery({
		...rpcQuery<RolesData>(threadId, ROLES_FRAME),
		enabled,
	});
	const entries = roles.data?.roles;
	const wantModels = !!entries && needsModels(entries);
	const models = useModelCatalog(enabled && wantModels);
	const state = useThread(threadId, (s) => s.state);
	const threadEntries = useThread(threadId, (s) => s.entries);
	const leaf = useThread(threadId, (s) => s.leafEntryId);
	const pending = useStore(roleStore, (s) => s.threads[threadId]?.pending);
	useStore(roleStore, (s) => s.memory[threadId]);
	const lastRole = useMemo(
		() => lastModelChangeRole(threadEntries),
		[threadEntries],
	);
	const trail = useMemo(
		() => trailOf(activeBranch(threadEntries, leaf)),
		[threadEntries, leaf],
	);
	const settingsDone = !settings.isPending || settings.isError;
	const modelsDone = !wantModels || !models.isPending || models.isError;
	const ready = !!entries && settingsDone && modelsDone;
	const groups = useMemo<RoleGroups>(() => {
		if (!ready || !entries) return { cycle: [], others: [], more: [] };
		return groupSlots(
			{
				order: parseCycleOrder(settings.data?.values?.cycleOrder),
				entries,
				available: models.data?.models ?? [],
			},
			currentOf(state),
			withExtras,
		);
	}, [ready, entries, settings.data, models.data, state, withExtras]);
	const cur = currentOf(state);
	const accent = useTheme((s) => s.accent);
	const all = [...groups.cycle, ...groups.others, ...groups.more];
	const currentId = all[roleIndex(all, cur, threadId, trail, lastRole)]?.id;
	const cycleCurrent =
		groups.cycle[roleIndex(groups.cycle, cur, threadId, trail, lastRole)]?.id;
	return {
		...groups,
		isPending: enabled && !ready && !roles.isError,
		error: roles.error,
		currentId,
		trackId: pending ?? cycleCurrent,
		currentLevel: cur?.thinkingLevel,
		accent,
	};
}

/** 一格的角色色：轨道里按 cycleOrder 位置取色板（跳过和当前强调色相近的色相），其余用固定或哈希色相。 */
export function slotHue(
	slot: RoleSlot,
	view: Pick<RoleView, "cycle" | "accent">,
): string {
	return roleHue(slot.id, view.cycle.indexOf(slot), view.accent);
}

// ---------------------------------------------------------------- 快捷键与轨道

/** Ctrl+P / Shift+Ctrl+P（mac ⌘P）：线程页循环切换角色模型，并屏蔽浏览器的打印。 */
export function useRoleShortcut(threadId: string) {
	const qc = useQueryClient();
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
			if (e.key.toLowerCase() !== "p" && e.code !== "KeyP") return;
			e.preventDefault();
			// 对话框打开时只屏蔽打印，不切换
			if (uiStore.getState().dialog) return;
			void cycleRole(qc, threadId, e.shiftKey ? -1 : 1);
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [qc, threadId]);
}

const SHOW_MS = 4000;
const FADE_MS = 180;

type Phase = "hidden" | "shown" | "leaving";

/**
 * 切换时浮在输入坞托盘上方的角色胶囊轨：每格是模型 logo + 角色名，当前格由一块角色色滑块高亮，
 * 到位后名字轻微回弹；4 秒没有再切换就淡出。同时托盘描边闪一下角色色。
 * `anchorRef` 是托盘里 Composer 的根节点：轨道定位到整个托盘上方，闪色加在托盘上。
 */
export function RoleTrack({
	threadId,
	anchorRef,
}: {
	threadId: string;
	anchorRef: RefObject<HTMLDivElement | null>;
}): ReactNode {
	const { t } = useTranslation();
	const thread = useStore(
		roleStore,
		(s) => s.threads[threadId] ?? EMPTY_THREAD,
	);
	const [phase, setPhase] = useState<Phase>("hidden");
	const [above, setAbove] = useState(0);
	const [bump, setBump] = useState(0);
	const view = useRoleView(threadId, phase !== "hidden");
	const timer = useRef<number | undefined>(undefined);
	const hovering = useRef(false);
	const seenFlash = useRef(thread.flash);

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
		if (thread.flash === seenFlash.current) return;
		seenFlash.current = thread.flash;
		setAbove(anchorRef.current?.offsetTop ?? 0);
		setPhase("shown");
		scheduleHide();
	}, [thread.flash]);

	useEffect(() => () => clearTimeout(timer.current), []);

	const target = view.cycle.find((s) => s.id === thread.pending);
	const targetHue = target ? slotHue(target, view) : undefined;

	// 托盘描边闪一下目标角色色（CSS 动画结束即回到默认描边，不留痕）
	// biome-ignore lint/correctness/useExhaustiveDependencies: 每次切换请求都重播
	useEffect(() => {
		const tray =
			anchorRef.current?.parentElement?.closest<HTMLElement>(".glass");
		if (!tray || !targetHue) return;
		tray.style.setProperty("--role-h", targetHue);
		tray.removeAttribute("data-role-flash");
		void tray.offsetWidth;
		tray.setAttribute("data-role-flash", "");
	}, [thread.flash, targetHue]);

	useEffect(() => {
		const tray =
			anchorRef.current?.parentElement?.closest<HTMLElement>(".glass");
		if (!tray) return;
		const onEnd = (e: AnimationEvent) => {
			if (e.target === tray && e.animationName === "role-flash")
				tray.removeAttribute("data-role-flash");
		};
		tray.addEventListener("animationend", onEnd);
		return () => {
			tray.removeEventListener("animationend", onEnd);
			tray.removeAttribute("data-role-flash");
			tray.style.removeProperty("--role-h");
		};
	}, [anchorRef]);

	// 滑块：读当前格的 offsetLeft/offsetWidth；刚浮出时直接到位，之后的切换才滑动
	const listRef = useRef<HTMLDivElement>(null);
	const plateRef = useRef<HTMLSpanElement>(null);
	const placedFor = useRef<string | undefined>(undefined);
	const active = view.trackId;
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
			placedFor.current !== undefined && placedFor.current !== active;
		plate.style.transitionDuration = animate ? "" : "0ms";
		plate.style.left = `${el.offsetLeft}px`;
		plate.style.width = `${el.offsetWidth}px`;
		plate.style.opacity = "1";
		if (animate) {
			el.scrollIntoView?.({ block: "nearest", inline: "nearest" });
			setBump((b) => b + 1);
		}
		placedFor.current = active;
	}, [active, view.cycle.length, phase]);

	const activeSlot = view.cycle.find((s) => s.id === active);
	const live = (
		<p className="sr-only" aria-live="polite">
			{thread.announce}
		</p>
	);
	if (phase === "hidden") return live;
	return (
		<>
			{live}
			<div
				className="pointer-events-none absolute left-1/2 z-30 flex max-w-[calc(100vw-1.5rem)] -translate-x-1/2 justify-center"
				style={{ bottom: `calc(100% + ${above + 10}px)` }}
			>
				{/* biome-ignore lint/a11y/noStaticElementInteractions: 悬停/聚焦时暂停自动淡出 */}
				<div
					className={`glass pop-in pointer-events-auto flex min-w-0 items-center rounded-full p-1 transition-opacity duration-[180ms] ease-out ${phase === "leaving" ? "opacity-0" : ""}`}
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
						className="focus-inset relative flex min-w-0 items-center overflow-x-auto"
					>
						<span
							ref={plateRef}
							aria-hidden
							className="role-tone absolute top-0 h-7 rounded-full bg-[var(--role-fill)] opacity-0 ring-1 ring-[var(--role-line)] ring-inset transition-[left,width,background-color,box-shadow] duration-[220ms] ease-out"
							style={
								{
									"--role-h": activeSlot
										? slotHue(activeSlot, view)
										: undefined,
								} as React.CSSProperties
							}
						/>
						{view.cycle.map((slot) => {
							const on = slot.id === active;
							const ok = slot.status === "ok";
							const label = roleLabel(slot);
							return (
								// biome-ignore lint/a11y/useSemanticElements: 胶囊里要放 logo 和滑块，原生 radio 画不出来；键盘与读屏语义由 role/aria-checked 提供
								<button
									key={slot.id}
									type="button"
									role="radio"
									aria-checked={on}
									aria-disabled={!ok || undefined}
									data-role={slot.id}
									title={ok ? slotSelector(slot) : slotProblem(slot)}
									className={`role-tone relative z-10 inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full px-2.5 font-medium text-xs transition-colors duration-150 ${
										on
											? "text-[var(--role-ink)]"
											: ok
												? "text-muted hover:text-fg"
												: "cursor-not-allowed text-ghost"
									}`}
									style={
										{
											"--role-h": slotHue(slot, view),
										} as React.CSSProperties
									}
									onClick={() => {
										if (ok && !on) requestRole(threadId, slot);
									}}
								>
									<ModelLogo
										provider={slot.model?.provider}
										model={slot.model?.id}
										size={13}
										className={ok ? "" : "opacity-50"}
									/>
									<span
										key={on ? `on-${bump}` : "off"}
										className={on ? "role-pop font-semibold" : ""}
									>
										{label}
									</span>
								</button>
							);
						})}
						{view.isPending && view.cycle.length === 0 && (
							<span className="px-3 text-2xs text-faint">…</span>
						)}
					</div>
					{thread.message && (
						<span
							className={`shrink-0 px-2 text-2xs ${thread.message.tone === "error" ? "text-err" : "text-faint"}`}
						>
							{thread.message.text}
						</span>
					)}
				</div>
			</div>
		</>
	);
}
