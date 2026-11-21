/**
 * Boxes（M23）界面的纯逻辑：名字校验、状态色调、时段文案、box 线程的标签、首屏执行位置胶囊的三种状态。
 * 都不读 React 状态，测试直接调。
 */
import type { TFunction } from "i18next";
import type { Box } from "../../api/box-queries";
import type { Orb, Thread } from "../../api/client";
import type { PillText } from "../composer/PillButton";
import type { Tone } from "../ui/data";

/** 首屏执行位置选中的 box：只存 id，名字在渲染时从 box 列表里查；`app` 省略 = 机器。 */
export interface BoxPick {
	boxId: string;
	app?: string;
}

/** box 名：小写字母、数字、连字符，至多 30 个字符，首尾是字母或数字，不含 `--`（同服务端）。 */
export function validBoxName(name: string): boolean {
	return (
		/^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$/.test(name) && !name.includes("--")
	);
}

export const BOX_STATE_TONE: Record<Box["state"], Tone> = {
	starting: "warn",
	running: "ok",
	stopping: "warn",
	stopped: "neutral",
	failed: "err",
};

/** 状态文案：被时段停下的写「按时段停机」。 */
export function boxStateText(box: Box, t: TFunction): string {
	return box.state === "stopped" && box.stoppedBy === "schedule"
		? t("boxes.state.stoppedBySchedule")
		: t(`boxes.state.${box.state}`);
}

/** 时段文案：`05:00–23:00 Europe/Berlin`，没有时段是「一直运行」。 */
export function scheduleText(schedule: Box["schedule"], t: TFunction): string {
	return schedule
		? `${schedule.on}–${schedule.off} ${schedule.tz}`
		: t("boxes.about.alwaysOn");
}

/** 线程 cwd 落在哪个 app 的目录（`~/apps/<名>/…`）；机器上的线程为 undefined。 */
export function appOfCwd(cwd: string | undefined): string | undefined {
	return /^~\/apps\/([^/]+)(?:\/|$)/.exec(cwd ?? "")?.[1];
}

/**
 * box 线程的标签 `<box 名> · <app 或 机器>`；不是 box 的 orb、或 box 列表还没到（找不到这台 orb 的 box）时为 null——
 * 调用方这时保持原来的 orb 文案与图标。
 */
export function boxThreadLabel(
	orb: Pick<Orb, "id" | "box"> | undefined,
	thread: Pick<Thread, "cwd"> | undefined,
	boxes: readonly Box[] | undefined,
	t: TFunction,
): string | null {
	if (!orb?.box) return null;
	const box = boxes?.find((b) => b.orbId === orb.id);
	if (!box) return null;
	return `${box.name} · ${appOfCwd(thread?.cwd) ?? t("boxes.picker.machine")}`;
}

/** 首屏执行位置胶囊的状态：列表没到、可以开线程、或这台 box / app 现在不能开线程（警示态）。 */
export type BoxLabel =
	| { state: "loading"; text: PillText }
	| { state: "ok"; text: PillText }
	| { state: "gone"; text: PillText };

/**
 * 首屏执行位置胶囊的文案。可选与否只看 `desired`（与服务端 `ThreadTarget` 一致），app 还要有 live。
 */
export function boxLabel(
	pick: BoxPick,
	boxes: readonly Box[] | undefined,
	t: TFunction,
): BoxLabel {
	if (!boxes) return { state: "loading", text: { label: "…", main: "…" } };
	const box = boxes.find((b) => b.id === pick.boxId || b.name === pick.boxId);
	const app = pick.app ? box?.apps.find((a) => a.name === pick.app) : undefined;
	if (!box || box.desired === "stopped" || (pick.app && !app?.live)) {
		const gone = t("boxes.picker.gone");
		return { state: "gone", text: { label: gone, main: gone, warn: true } };
	}
	const badge = pick.app ?? t("boxes.picker.machine");
	return {
		state: "ok",
		text: { label: `${box.name} · ${badge}`, main: box.name, badge },
	};
}

/** box 上最近一次部署的 app（总览行的元信息用）。 */
export function lastDeployedApp(box: Box): Box["apps"][number] | undefined {
	let best: Box["apps"][number] | undefined;
	for (const a of box.apps)
		if (a.live && (!best?.live || a.live.activatedAt > best.live.activatedAt))
			best = a;
	return best;
}
