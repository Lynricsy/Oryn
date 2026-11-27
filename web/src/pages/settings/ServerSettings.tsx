/**
 * 设置 → 控制面：控制面的运维参数（空闲回收、数据保留、磁盘上限），改动压在 orynd.yaml 之上，重启后仍在。
 * - 显示的都是生效值；数字框失焦或回车提交，每次只发改动的那一个字段。值没变、输入不合法都不发请求。
 * - 在这里改过的键：说明后追加配置文件里的原值，行尾多一个「恢复」（发 null，回落 orynd.yaml）。
 * - 保留期变短（0 = 永久，按无穷大比较）先二次确认：清理会永久删除超出保留期的旧记录。
 * - 同一时间只发一个请求，进行中禁用全部控件；服务端拒绝时错误显示在该分组下，输入框还原成生效值。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type OpsSettings,
	type OpsSettingsPatch,
	type OpsSettingsState,
	patchOpsSettings,
	useOpsSettings,
} from "../../api/ops-settings-queries";
import { useAsync } from "../../components/settings/useAsync";
import type { IconName } from "../../components/ui/icons";
import {
	Button,
	ErrorText,
	Spinner,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";

type GroupId = "idle" | "retention" | "disk";
type Unit = "minutes" | "hours" | "days" | "gb";

/** 每个单位对应的原始值：时长是毫秒，容量本身就是 GB。 */
const UNIT_SIZE: Record<Unit, number> = {
	minutes: 60_000,
	hours: 3_600_000,
	days: 86_400_000,
	gb: 1,
};

type NumberName =
	| "agentIdleMs"
	| "userIdleMs"
	| "stopAfterFrozenMs"
	| "samplesMs"
	| "eventsMs"
	| "usageMs"
	| "maxMirrorGb"
	| "minFreeGb"
	| "maxLfsCacheGb"
	| "maxTotalGb"
	| "maxSnapshotGb";

interface NumberSpec {
	/** JSON 字段名，也是文案键 */
	name: NumberName;
	/** YAML 点路径，对应 `overridden` 里的元素 */
	key: string;
	unit: Unit;
	icon: IconName;
	get: (s: OpsSettings) => number;
	patch: (v: number | null) => OpsSettingsPatch;
}

const IDLE: readonly NumberSpec[] = [
	{
		name: "agentIdleMs",
		key: "idle.agent_idle",
		unit: "minutes",
		icon: "robot",
		get: (s) => s.idle.agentIdleMs,
		patch: (v) => ({ idle: { agentIdleMs: v } }),
	},
	{
		name: "userIdleMs",
		key: "idle.user_idle",
		unit: "minutes",
		icon: "user",
		get: (s) => s.idle.userIdleMs,
		patch: (v) => ({ idle: { userIdleMs: v } }),
	},
	{
		name: "stopAfterFrozenMs",
		key: "idle.stop_after_frozen",
		unit: "hours",
		icon: "timer",
		get: (s) => s.idle.stopAfterFrozenMs,
		patch: (v) => ({ idle: { stopAfterFrozenMs: v } }),
	},
];

const RETENTION: readonly NumberSpec[] = [
	{
		name: "samplesMs",
		key: "stats.retention.samples",
		unit: "days",
		icon: "chart",
		get: (s) => s.statsRetention.samplesMs,
		patch: (v) => ({ statsRetention: { samplesMs: v } }),
	},
	{
		name: "eventsMs",
		key: "stats.retention.events",
		unit: "days",
		icon: "history",
		get: (s) => s.statsRetention.eventsMs,
		patch: (v) => ({ statsRetention: { eventsMs: v } }),
	},
	{
		name: "usageMs",
		key: "stats.retention.usage",
		unit: "days",
		icon: "coin",
		get: (s) => s.statsRetention.usageMs,
		patch: (v) => ({ statsRetention: { usageMs: v } }),
	},
];

const DISK: readonly NumberSpec[] = [
	{
		name: "maxMirrorGb",
		key: "repos.max_mirror_gb",
		unit: "gb",
		icon: "hostedRepo",
		get: (s) => s.repos.maxMirrorGb,
		patch: (v) => ({ repos: { maxMirrorGb: v } }),
	},
	{
		name: "minFreeGb",
		key: "repos.min_free_gb",
		unit: "gb",
		icon: "hardDrives",
		get: (s) => s.repos.minFreeGb,
		patch: (v) => ({ repos: { minFreeGb: v } }),
	},
	{
		name: "maxLfsCacheGb",
		key: "repos.max_lfs_cache_gb",
		unit: "gb",
		icon: "database",
		get: (s) => s.repos.maxLfsCacheGb,
		patch: (v) => ({ repos: { maxLfsCacheGb: v } }),
	},
	{
		name: "maxTotalGb",
		key: "snapshots.max_total_gb",
		unit: "gb",
		icon: "stack",
		get: (s) => s.snapshots.maxTotalGb,
		patch: (v) => ({ snapshots: { maxTotalGb: v } }),
	},
	{
		name: "maxSnapshotGb",
		key: "snapshots.max_snapshot_gb",
		unit: "gb",
		icon: "snapshot",
		get: (s) => s.snapshots.maxSnapshotGb,
		patch: (v) => ({ snapshots: { maxSnapshotGb: v } }),
	},
];

const WAKE_KEY = "idle.wake_on_focus";

/** 原始值 → 按单位显示的数（时长可以是小数，如 90 秒显示 1.5 分钟）。 */
function shown(raw: number, unit: Unit): string {
	return String(Math.round((raw / UNIT_SIZE[unit]) * 1000) / 1000);
}

/** 保留期 a 是否比 b 短；0 = 永久。 */
function shorter(a: number, b: number): boolean {
	return (a === 0 ? Infinity : a) < (b === 0 ? Infinity : b);
}

/** 一行数字参数：数字框 + 单位，改过时追加配置文件里的原值与「恢复」。 */
function NumberRow({
	spec,
	state,
	busy,
	onSubmit,
}: {
	spec: NumberSpec;
	state: OpsSettingsState;
	busy: boolean;
	/** 提交新值（null = 恢复）；返回是否已生效，未生效时输入框还原 */
	onSubmit: (spec: NumberSpec, v: number | null) => Promise<boolean>;
}): ReactNode {
	const { t } = useTranslation();
	const title = t(`serverSettings.fields.${spec.name}.title`);
	const unitLabel = t(`serverSettings.units.${spec.unit}`);
	const current = spec.get(state.effective);
	const display = shown(current, spec.unit);
	const [text, setText] = useState(display);
	const [base, setBase] = useState(display);
	const [invalid, setInvalid] = useState(false);
	// 生效值变了（本行或别处的提交、重新拉取）→ 输入框跟上
	if (base !== display) {
		setBase(display);
		setText(display);
		setInvalid(false);
	}
	const overridden = state.overridden.includes(spec.key);
	// 回车提交后弹出确认会让输入框失焦，失焦又会提交一次；提交未结束前忽略后来的
	const submitting = useRef(false);

	const commit = async () => {
		if (busy || submitting.current) return;
		if (text === display) {
			setInvalid(false);
			return;
		}
		const v = Number(text);
		if (
			text.trim() === "" ||
			!Number.isFinite(v) ||
			v < 0 ||
			(spec.unit === "gb" && !Number.isInteger(v))
		) {
			setInvalid(true);
			return;
		}
		setInvalid(false);
		const next = Math.round(v * UNIT_SIZE[spec.unit]);
		if (next === current) {
			setText(display);
			return;
		}
		submitting.current = true;
		try {
			if (!(await onSubmit(spec, next))) setText(display);
		} finally {
			submitting.current = false;
		}
	};

	return (
		<GroupRow
			icon={spec.icon}
			title={title}
			meta={
				<>
					<span>{t(`serverSettings.fields.${spec.name}.hint`)}</span>
					{overridden && (
						<span>
							·{" "}
							{t("serverSettings.fromYaml", {
								value: `${shown(spec.get(state.yaml), spec.unit)} ${unitLabel}`,
							})}
						</span>
					)}
				</>
			}
			trailing={
				<>
					{overridden && (
						<Button
							size="sm"
							variant="ghost"
							disabled={busy}
							aria-label={t("serverSettings.restoreLabel", { name: title })}
							onClick={() => void onSubmit(spec, null)}
						>
							{t("serverSettings.restore")}
						</Button>
					)}
					<TextInput
						type="number"
						inputMode="decimal"
						min={0}
						step="any"
						className="w-24"
						aria-label={title}
						aria-invalid={invalid || undefined}
						disabled={busy}
						value={text}
						onChange={(e) => setText(e.target.value)}
						onBlur={() => void commit()}
						onKeyDown={(e) => {
							if (e.key !== "Enter") return;
							// 阻止后续的 keypress：否则确认框弹出后焦点落在「取消」上，这次回车会把它点掉
							e.preventDefault();
							void commit();
						}}
					/>
					<span className="w-9 text-muted text-xs">{unitLabel}</span>
				</>
			}
		>
			{invalid && (
				<p role="alert" className="text-err text-xs">
					{spec.unit === "gb"
						? t("serverSettings.invalidInteger")
						: t("serverSettings.invalidNumber")}
				</p>
			)}
		</GroupRow>
	);
}

export default function ServerSettings(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const ops = useOpsSettings();
	const op = useAsync();
	const [errAt, setErrAt] = useState<GroupId | null>(null);

	if (ops.isPending) return <Spinner />;
	if (!ops.data) return <ErrorText error={ops.error} />;
	const state = ops.data;

	const send = (group: GroupId, patch: OpsSettingsPatch) => {
		setErrAt(group);
		return op.run(() => patchOpsSettings(qc, patch));
	};

	const submit =
		(group: GroupId) =>
		async (spec: NumberSpec, v: number | null): Promise<boolean> => {
			if (spec.unit === "days") {
				const next = v ?? spec.get(state.yaml);
				if (
					shorter(next, spec.get(state.effective)) &&
					!(await confirmAction({
						title: t("serverSettings.shortenTitle", {
							name: t(`serverSettings.fields.${spec.name}.title`),
						}),
						body: t("serverSettings.shortenBody"),
						action: t("serverSettings.shorten"),
						danger: true,
					}))
				)
					return false;
			}
			return send(group, spec.patch(v));
		};

	const groupError = (group: GroupId) =>
		errAt === group && <ErrorText error={op.error} />;

	const rows = (group: GroupId, specs: readonly NumberSpec[]) =>
		specs.map((spec) => (
			<NumberRow
				key={spec.key}
				spec={spec}
				state={state}
				busy={op.busy}
				onSubmit={submit(group)}
			/>
		));

	const wake = state.effective.idle.wakeOnFocus;
	const wakeTitle = t("serverSettings.fields.wakeOnFocus.title");
	const wakeOverridden = state.overridden.includes(WAKE_KEY);

	return (
		<>
			<Group title={t("serverSettings.groups.idle")}>
				<GroupList>
					{rows("idle", IDLE)}
					<GroupRow
						icon="bolt"
						title={wakeTitle}
						meta={
							<>
								<span>{t("serverSettings.fields.wakeOnFocus.hint")}</span>
								{wakeOverridden && (
									<span>
										·{" "}
										{t("serverSettings.fromYaml", {
											value: state.yaml.idle.wakeOnFocus
												? t("serverSettings.on")
												: t("serverSettings.off"),
										})}
									</span>
								)}
							</>
						}
						trailing={
							<>
								{wakeOverridden && (
									<Button
										size="sm"
										variant="ghost"
										disabled={op.busy}
										aria-label={t("serverSettings.restoreLabel", {
											name: wakeTitle,
										})}
										onClick={() =>
											void send("idle", { idle: { wakeOnFocus: null } })
										}
									>
										{t("serverSettings.restore")}
									</Button>
								)}
								<Toggle
									checked={wake}
									disabled={op.busy}
									label={wakeTitle}
									hideLabel
									onChange={(v) =>
										void send("idle", { idle: { wakeOnFocus: v } })
									}
								/>
							</>
						}
					/>
				</GroupList>
				{groupError("idle")}
			</Group>
			<Group title={t("serverSettings.groups.retention")}>
				<GroupList>{rows("retention", RETENTION)}</GroupList>
				{groupError("retention")}
			</Group>
			<Group title={t("serverSettings.groups.disk")}>
				<GroupList>{rows("disk", DISK)}</GroupList>
				{groupError("disk")}
			</Group>
		</>
	);
}
