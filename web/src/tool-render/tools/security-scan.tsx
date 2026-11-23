/**
 * `security_scan` — OMP 原生安全扫描与 Codex Security 云扫描（设备 `xd://security_scan`）。
 * 结果形状见 oryn-core `tools/security-scan.ts`：details 为 `{ action, plan?, operation?, cancelled?,
 * finding?, cloudConfigurations?, cloudStats?, cloudScan?, importedScan? }`，按 action 只带其中几项：
 * - preflight → `plan: { id, fingerprint }`
 * - start / status → `operation`（SecurityOperationSnapshot：operationId、planId、scanId、phase、
 *   createdAt、updatedAt、jobId?、sessionFile?、findingCount、error?）；cancel 另带 `cancelled`
 * - validate → `finding: { id, validationStatus }`
 * - cloud_scans → `cloudConfigurations[]`；cloud_start → `cloudScan: { id, repositoryUrl }`
 * - cloud_status → `cloudStats`（commit 计数、按严重度的发现数）；cloud_pull → `importedScan: { id, findingCount }`
 * 工具不发运行中的部分结果；出错（ToolError）时没有 details，只剩错误文本。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime } from "../../lib/format";
import {
	Badge,
	Badges,
	type CellTone,
	InvalidArg,
	Kv,
	KvGrid,
	Note,
	Output,
	PathText,
	ProgressCells,
	ResultText,
	Row,
	type Tone,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

const ACTIONS = [
	"preflight",
	"start",
	"status",
	"cancel",
	"validate",
	"cloud_scans",
	"cloud_start",
	"cloud_status",
	"cloud_pull",
] as const;
type Action = (typeof ACTIONS)[number];

/** 每个 action 在摘要里点名的关键参数（preflight 的目标另行拼）。 */
const KEY_ARG: Partial<Record<Action, string>> = {
	start: "plan_id",
	status: "operation_id",
	cancel: "operation_id",
	validate: "finding_id",
	cloud_start: "repository_url",
	cloud_status: "cloud_configuration_id",
	cloud_pull: "cloud_configuration_id",
};

/** operation 阶段的读数配色：终态才着色，进行中的阶段保持中性。 */
const PHASE_CLASS: Record<string, string> = {
	completed: "tv-meta-ok",
	partial: "tv-meta-warn",
	cancelled: "tv-meta-warn",
	failed: "tv-meta-err",
};

/** finding 校验状态的读数配色；unvalidated / rejected（判为误报）保持中性。 */
const VALIDATION_CLASS: Record<string, string> = {
	validated: "tv-meta-ok",
	partial: "tv-meta-warn",
	error: "tv-meta-err",
};

const SEVERITIES = [
	"critical",
	"high",
	"medium",
	"low",
	"informational",
] as const;

function actionOf(args: Record<string, unknown>): Action | null {
	const action = str(args.action);
	return ACTIONS.find((a) => a === action) ?? null;
}

function strList(value: unknown): string[] {
	return Array.isArray(value)
		? value.filter((v): v is string => typeof v === "string" && v !== "")
		: [];
}

function rec(value: unknown): Record<string, unknown> | null {
	return isRecord(value) ? value : null;
}

/** preflight 的扫描目标：类别（缺省 repository）+ 范围（ref 区间或包含路径）。 */
function PreflightTarget({ args }: { args: Record<string, unknown> }) {
	const kind = str(args.target_kind) ?? "repository";
	const base = str(args.base_revision);
	const head = str(args.head_revision);
	const scope =
		kind === "ref_diff" && (base || head)
			? `${base ?? "?"}..${head ?? "?"}`
			: strList(args.include_paths).join(", ");
	return (
		<>
			<span>{kind}</span>
			{scope && <span className="tv-path"> {truncate(scope, 90)}</span>}
		</>
	);
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const action = actionOf(args);
	if (!action) return <InvalidArg what="action" />;
	const keyArg = KEY_ARG[action];
	const key = keyArg ? str(args[keyArg]) : null;
	return (
		<>
			<Badge>{action}</Badge>{" "}
			{action === "preflight" ? (
				<PreflightTarget args={args} />
			) : keyArg ? (
				key ? (
					<span className="tv-path">{truncate(key, 96)}</span>
				) : (
					<InvalidArg what={keyArg} />
				)
			) : null}
		</>
	);
}

/** 云扫描各严重度的发现数之和；字段缺失时为 null。 */
function cloudFindingTotal(stats: Record<string, unknown>): number | null {
	const counts = rec(stats.findingCounts);
	if (!counts) return null;
	let total = 0;
	for (const s of SEVERITIES) total += num(counts[s]) ?? 0;
	return total;
}

function Findings({ count }: { count: number | null }): ReactNode {
	const { t } = useTranslation();
	if (count === null) return null;
	return (
		<span className={count > 0 ? "tv-meta-warn" : undefined}>
			{t("toolRender.securityScan.findings", { count })}
		</span>
	);
}

/** 右侧读数：operation 阶段与发现数、计划就绪、校验结论、云扫描步骤 / 配置数 / 导入数。 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { args, result } = props;
	const action = actionOf(args);
	if (!action || result?.isError) return null;
	const details = detailsRecord(result);
	if (action === "validate") {
		const finding = rec(details?.finding);
		// 结果给出最终状态；还没有结果时显示请求的状态
		const status =
			str(finding?.validationStatus) ?? str(args.validation_status);
		if (!status) return null;
		return <span className={VALIDATION_CLASS[status]}>{status}</span>;
	}
	if (!details) return null;
	const operation = rec(details.operation);
	const cancelled =
		typeof details.cancelled === "boolean" ? details.cancelled : null;
	const plan = rec(details.plan);
	const configs = Array.isArray(details.cloudConfigurations)
		? details.cloudConfigurations.length
		: null;
	const stats = rec(details.cloudStats);
	const imported = rec(details.importedScan);
	const phase = str(operation?.phase);
	const step = str(stats?.currentStep);
	return (
		<>
			{cancelled !== null && (
				<span className={cancelled ? "tv-meta-ok" : "tv-meta-warn"}>
					{t(
						cancelled
							? "toolRender.securityScan.cancelRequested"
							: "toolRender.securityScan.notRunning",
					)}
				</span>
			)}
			{phase && (
				<span className={PHASE_CLASS[phase]}>
					<PhaseLabel phase={phase} />
				</span>
			)}
			{operation && <Findings count={num(operation.findingCount)} />}
			{plan && str(plan.id) && (
				<span className="tv-meta-ok">
					{t("toolRender.securityScan.planReady")}
				</span>
			)}
			{configs !== null && (
				<span>{t("toolRender.securityScan.configs", { count: configs })}</span>
			)}
			{rec(details.cloudScan) && (
				<span className="tv-meta-ok">
					{t("toolRender.securityScan.started")}
				</span>
			)}
			{step && <span>{step}</span>}
			{stats && <Findings count={cloudFindingTotal(stats)} />}
			{imported && num(imported.findingCount) !== null && (
				<span>
					{t("toolRender.securityScan.imported", {
						count: num(imported.findingCount),
					})}
				</span>
			)}
		</>
	);
}

/** 参数值的展示：数组逐项用逗号连起，布尔与数字转文字。 */
function argText(value: unknown): string | null {
	if (typeof value === "string") return value || null;
	if (typeof value === "number" || typeof value === "boolean")
		return String(value);
	if (Array.isArray(value)) {
		const items = strList(value);
		return items.length > 0 ? items.join(", ") : null;
	}
	return null;
}

/**
 * 参数网格里不列的键：action 在头部徽章；长文本与证据另起一块；
 * 校验状态平时在读数里，出错时读数为空才回到网格。
 */
const ARG_SKIP: Record<string, true> = {
	action: true,
	validation_summary: true,
	validation_evidence: true,
};

function ArgsGrid({
	args,
	statusInMeta,
}: {
	args: Record<string, unknown>;
	statusInMeta: boolean;
}): ReactNode {
	const rows: ReactNode[] = [];
	for (const k in args) {
		if (ARG_SKIP[k] || (statusInMeta && k === "validation_status")) continue;
		// 头部摘要会被省略号截断：关键对象、preflight 的目标与 ref 区间也在网格里列全
		const text = argText(args[k]);
		if (text === null) continue;
		rows.push(
			<Kv key={k} k={k}>
				{k.endsWith("_paths") || k === "output_root" ? (
					<span className="tv-path">{text}</span>
				) : (
					text
				)}
			</Kv>,
		);
	}
	return rows.length > 0 ? <KvGrid>{rows}</KvGrid> : null;
}

function Evidence({ value }: { value: unknown }): ReactNode {
	if (!Array.isArray(value)) return null;
	const items = value.filter(isRecord);
	if (items.length === 0) return null;
	return (
		<div className="tv-list">
			{items.map((item, i) => (
				<Row key={i} k={str(item.label) ?? <InvalidArg what="label" />}>
					{str(item.explanation)}
				</Row>
			))}
		</div>
	);
}

/** ISO 时间按本地时间显示；解析不了时原样显示。 */
function when(value: unknown): string | null {
	const raw = str(value);
	const ts = Date.parse(raw ?? "");
	return Number.isFinite(ts) ? formatDateTime(ts) : raw;
}

/** operation 的全部阶段：前五个是本地扫描的工作阶段（顺序即进度），其后是 completed 之外的终态。 */
const PHASES = [
	"queued",
	"preparing",
	"reviewing",
	"publishing",
	"completed",
	"partial",
	"cancelled",
	"failed",
] as const;
type Phase = (typeof PHASES)[number];

function PhaseLabel({ phase }: { phase: string }): ReactNode {
	const { t } = useTranslation();
	const known = PHASES.find((p) => p === phase);
	return known ? t(`toolRender.securityScan.phase.${known}`) : phase;
}

type StepState = "done" | "now" | "todo" | "warn" | "err";

/**
 * operation 的阶段轨道：已过的阶段为实心，当前阶段用强调色，未到的为空心。
 * partial / cancelled / failed 只知道终态，不知道停在哪一步：最后一格写终态，之前的阶段按「部分完成」实心、其余空心。
 */
function PhaseTrack({ phase }: { phase: string }): ReactNode {
	const { t } = useTranslation();
	const known = PHASES.find((p) => p === phase);
	if (!known) return null;
	const at = PHASES.indexOf(known);
	const terminal = at > 4;
	const steps: Phase[] = terminal
		? [...PHASES.slice(0, 4), known]
		: [...PHASES.slice(0, 5)];
	return (
		<ol className="tv-scan-track">
			{steps.map((step, i) => {
				let state: StepState;
				if (terminal) {
					if (i === steps.length - 1)
						state = known === "failed" ? "err" : "warn";
					else state = known === "partial" ? "done" : "todo";
				} else if (known === "completed" || i < at) state = "done";
				else state = i === at ? "now" : "todo";
				return (
					<li
						key={step}
						className={`tv-scan-step tv-scan-step--${state}`}
						aria-current={state === "now" ? "step" : undefined}
					>
						<i aria-hidden="true" />
						<span>{t(`toolRender.securityScan.phase.${step}`)}</span>
					</li>
				);
			})}
		</ol>
	);
}

/** operation 快照里读数之外的部分：阶段轨道、各级 id、job、会话文件、时间、错误（查询的 operation id 已在头部）。 */
function OperationGrid({
	op,
	queried,
}: {
	op: Record<string, unknown>;
	queried: string | null;
}): ReactNode {
	const { t } = useTranslation();
	const sessionFile = str(op.sessionFile);
	const error = str(op.error);
	const operationId = str(op.operationId);
	const phase = str(op.phase);
	return (
		<>
			{phase && <PhaseTrack phase={phase} />}
			{error && <Note tone="err">{error}</Note>}
			<KvGrid>
				<Kv k={t("toolRender.securityScan.operation")}>
					{operationId !== queried && operationId}
				</Kv>
				<Kv k={t("toolRender.securityScan.scan")}>{str(op.scanId)}</Kv>
				<Kv k={t("toolRender.securityScan.plan")}>{str(op.planId)}</Kv>
				<Kv k={t("toolRender.securityScan.job")}>{str(op.jobId)}</Kv>
				<Kv k={t("toolRender.securityScan.sessionFile")}>
					{sessionFile && <PathText path={sessionFile} />}
				</Kv>
				<Kv k={t("toolRender.securityScan.created")}>{when(op.createdAt)}</Kv>
				<Kv k={t("toolRender.securityScan.updated")}>{when(op.updatedAt)}</Kv>
			</KvGrid>
		</>
	);
}

/** 提交进度的格子：总数很大时按比例缩到 120 格以内（非零的一类至少一格），交给 ProgressCells 聚合成分段条。 */
function commitTones(finished: number, failed: number, pending: number) {
	const total = finished + failed + pending;
	const scale = total > 120 ? 120 / total : 1;
	const n = (count: number) =>
		count > 0 ? Math.max(1, Math.round(count * scale)) : 0;
	return [
		...Array<CellTone>(n(finished)).fill("ok"),
		...Array<CellTone>(n(failed)).fill("err"),
		...Array<CellTone>(n(pending)).fill("idle"),
	];
}

/** 严重度的色调：critical / high 红，medium 黄，其余中性。 */
const SEVERITY_TONE: Partial<Record<(typeof SEVERITIES)[number], Tone>> = {
	critical: "err",
	high: "err",
	medium: "warn",
};

function SeverityChips({
	counts,
}: {
	counts: Record<string, unknown>;
}): ReactNode {
	const { t } = useTranslation();
	const chips = SEVERITIES.flatMap((s) => {
		const n = num(counts[s]);
		return n
			? [
					<Badge key={s} tone={SEVERITY_TONE[s]}>
						{t(`toolRender.securityScan.sev.${s}`)} {n}
					</Badge>,
				]
			: [];
	});
	return chips.length > 0 ? (
		<span className="tv-badges">{chips}</span>
	) : (
		<span className="tv-faint">{t("toolRender.securityScan.noFindings")}</span>
	);
}

function CloudConfigs({ items }: { items: unknown[] }): ReactNode {
	const { t } = useTranslation();
	const configs = items.filter(isRecord);
	if (configs.length === 0) return null;
	return (
		<div className="tv-list">
			{configs.map((c, i) => {
				const remaining = num(c.remainingScans);
				const total = num(c.totalScans);
				const env = str(c.environmentId);
				const repo = str(c.repositoryId);
				return (
					<Row key={str(c.id) ?? i} k={str(c.id)}>
						<span className="tv-path">{str(c.repositoryUrl)}</span>{" "}
						<Badges
							items={[
								str(c.currentStep),
								str(c.state),
								str(c.scanType),
								repo && `repo ${repo}`,
								env && `env ${env}`,
								remaining !== null &&
									t("toolRender.securityScan.remaining", {
										remaining,
										total: total ?? "?",
									}),
							]}
						/>
					</Row>
				);
			})}
		</div>
	);
}

function CloudStats({
	stats,
	args,
}: {
	stats: Record<string, unknown>;
	args: Record<string, unknown>;
}): ReactNode {
	const { t } = useTranslation();
	const finished = num(stats.finishedCommits);
	const pending = num(stats.pendingCommits);
	const failed = num(stats.failedCommits);
	const counts = rec(stats.findingCounts);
	const configId = str(stats.configurationId);
	return (
		<KvGrid>
			<Kv k={t("toolRender.securityScan.cloudConfig")}>
				{configId !== str(args.cloud_configuration_id) && configId}
			</Kv>
			<Kv k={t("toolRender.securityScan.sourceConfig")}>
				{str(stats.sourceConfigurationId)}
			</Kv>
			<Kv k={t("toolRender.securityScan.commits")}>
				{(finished !== null || pending !== null || failed !== null) && (
					<span className="tv-scan-commits">
						<ProgressCells
							tones={commitTones(finished ?? 0, failed ?? 0, pending ?? 0)}
							label={t("toolRender.securityScan.commits")}
						/>
						<Badges
							items={[
								finished !== null && (
									<Badge key="f" tone="ok">
										{t("toolRender.securityScan.finished", { count: finished })}
									</Badge>
								),
								pending !== null &&
									t("toolRender.securityScan.pending", { count: pending }),
								failed !== null && failed > 0 && (
									<Badge key="x" tone="err">
										{t("toolRender.securityScan.failed", { count: failed })}
									</Badge>
								),
							]}
						/>
					</span>
				)}
			</Kv>
			<Kv k={t("toolRender.securityScan.severity")}>
				{counts && <SeverityChips counts={counts} />}
			</Kv>
			<Kv k={t("toolRender.securityScan.lastCommit")}>
				{str(stats.lastScannedCommit)}
			</Kv>
			<Kv k={t("toolRender.securityScan.lastScanned")}>
				{when(stats.lastScannedAt)}
			</Kv>
			<Kv k={t("toolRender.securityScan.updated")}>{when(stats.updatedAt)}</Kv>
		</KvGrid>
	);
}

/** details 里有没有本卡能结构化展示的结果；没有时（出错、旧格式）退回结果文本。 */
function hasStructured(details: Record<string, unknown> | null): boolean {
	return (
		!!details &&
		(isRecord(details.plan) ||
			isRecord(details.operation) ||
			isRecord(details.finding) ||
			Array.isArray(details.cloudConfigurations) ||
			isRecord(details.cloudStats) ||
			isRecord(details.cloudScan) ||
			isRecord(details.importedScan))
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	const summary = str(args.validation_summary);
	return (
		<>
			<ArgsGrid args={args} statusInMeta={!result?.isError} />
			{summary && <div className="tv-prose">{normalizeWs(summary)}</div>}
			<Evidence value={args.validation_evidence} />
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(result);
	const plan = rec(details?.plan);
	const operation = rec(details?.operation);
	const configs = Array.isArray(details?.cloudConfigurations)
		? details.cloudConfigurations
		: null;
	const stats = rec(details?.cloudStats);
	const cloudScan = rec(details?.cloudScan);
	const imported = rec(details?.importedScan);
	const cloudRepo = cloudScan && str(cloudScan.repositoryUrl);
	return (
		<>
			{plan && (
				<KvGrid>
					<Kv k={t("toolRender.securityScan.plan")}>{str(plan.id)}</Kv>
					<Kv k={t("toolRender.securityScan.fingerprint")}>
						{str(plan.fingerprint)}
					</Kv>
				</KvGrid>
			)}
			{operation && (
				<OperationGrid op={operation} queried={str(args.operation_id)} />
			)}
			{configs && <CloudConfigs items={configs} />}
			{stats && <CloudStats stats={stats} args={args} />}
			{cloudScan && (
				<KvGrid>
					<Kv k={t("toolRender.securityScan.cloudScan")}>
						{str(cloudScan.id)}
					</Kv>
					<Kv k={t("toolRender.securityScan.repository")}>
						{cloudRepo !== str(args.repository_url) && cloudRepo}
					</Kv>
				</KvGrid>
			)}
			{imported && (
				<KvGrid>
					<Kv k={t("toolRender.securityScan.scan")}>{str(imported.id)}</Kv>
				</KvGrid>
			)}
			{result?.isError ? (
				<ErrorText text={resultTextOf(result)} />
			) : (
				!hasStructured(details) && <ResultText result={result} maxLines={8} />
			)}
		</>
	);
}

/** 出错：首行作醒目的错误提示，其余（堆栈、细节）收在下面的可折叠块里。 */
function ErrorText({ text }: { text: string }): ReactNode {
	const trimmed = text.trim();
	const nl = trimmed.indexOf("\n");
	return (
		<>
			<Note tone="err">{nl < 0 ? trimmed : trimmed.slice(0, nl)}</Note>
			{nl >= 0 && (
				<Output text={trimmed.slice(nl + 1)} variant="code" maxLines={8} />
			)}
		</>
	);
}

export const securityScanRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
};
