/**
 * 项目 GitHub 卡片里的「触发规则」（M14 / M15）：GitHub 事件 → 新建线程。
 * 列表每行 = 名称、事件、已触发次数、最近触发、启停、编辑、删除；没有规则时一行说明。
 * 对话框（新建与编辑共用）分四段：
 * 1. 基本：预设按钮（新建时，`GET github/presets` 的 robomp 预设一键填满表单，带门禁的预设可顺手应用到本项目）、名称、事件、
 *    标签（仅「打标签」）、回帖线程链接、提示词（可套用本地提示词预设）；
 * 2. 谁能驱动：驱动者档位（R13）、维护者与评审机器人登录名；
 * 3. 模型：模型池（随机取一个，空 = 项目 / 全局默认）与思考级别；
 * 4. 任务：实现需授权、只发 COMMENT（仅外部 PR）、自动关闭等待时长、任务时限（分钟，存秒）、完成提醒、发布前缀（仅发布哨兵）。
 * 「@Agent 账号」要求 Agent 账号已连接：未连接时对话框里给出警告并禁用保存（服务端同样 409 `github_agent_required`）。
 */
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Project, Schemas } from "../../api/client";
import {
	type GitHubGates,
	type GitHubTrigger,
	type GitHubTriggerInput,
	useCreateGitHubTrigger,
	useDeleteGitHubTrigger,
	useGitHubPresets,
	useGitHubStatus,
	useGitHubTriggers,
	useSetProjectGates,
	useUpdateGitHubTrigger,
} from "../../api/github-queries";
import { ModelPickerBody } from "../../components/composer/ModelPickerBody";
import { Badge, Callout, ListRow } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { ModelLogo } from "../../components/ui/ModelLogo";
import { Popover } from "../../components/ui/PickerPopover";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Radio,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import { formatRelative } from "../../lib/format";
import { TRIGGER_PRESETS } from "../../lib/trigger-presets";

type TriggerEvent = Schemas["GitHubTriggerEvent"];
type Drivers = Schemas["GitHubTriggerDrivers"];
type ModelRef = Schemas["ModelRef"];

const EVENTS: readonly TriggerEvent[] = [
	"issue_opened",
	"issue_reopened",
	"issue_labeled",
	"mention",
	"pull_request_opened",
	"workflow_run_completed",
];

const DRIVERS: readonly Drivers[] = ["writers", "writers_and_author", "anyone"];

/** 规则可选的思考级别（空串 = 跟随默认）。 */
export const TRIGGER_THINKING = [
	"off",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;

const DEFAULT_AUTOCLOSE_HOURS = 4;
const DEFAULT_RELEASE_PREFIX = "chore: bump version to ";

/** 逗号或空白分隔的 GitHub 登录名（去掉 `@`、去重、保持顺序）。 */
export function parseLogins(text: string): string[] {
	return [
		...new Set(
			text
				.split(/[\s,，]+/)
				.map((s) => s.replace(/^@/, "").trim())
				.filter((s) => s !== ""),
		),
	];
}

/** 对话框的表单状态：数字与列表按输入框里的原文保存，保存时再换算。 */
interface TriggerForm {
	name: string;
	event: TriggerEvent;
	label: string;
	announce: boolean;
	prompt: string;
	drivers: Drivers;
	maintainers: string;
	reviewerBots: string;
	models: ModelRef[];
	thinking: string;
	implAuth: boolean;
	commentOnlyReviews: boolean;
	autocloseHours: string;
	maxDurationMin: string;
	completionReminders: boolean;
	releasePrefix: string;
	/** 运行结果交给助手（M19）：进主线，完成时不单独通知 */
	handToAssistant: boolean;
}

/** 已有规则或预设 → 表单；都没有时是新建规则的默认值。 */
function formOf(src?: GitHubTriggerInput): TriggerForm {
	return {
		name: src?.name ?? "",
		event: src?.event ?? "issue_labeled",
		label: src?.label ?? "",
		announce: src?.announce ?? true,
		prompt: src?.prompt ?? "",
		drivers: src?.drivers ?? "writers_and_author",
		maintainers: (src?.maintainers ?? []).join(", "),
		reviewerBots: (src?.reviewerBots ?? []).join(", "),
		models: (src?.models ?? []).map(({ provider, id }) => ({ provider, id })),
		thinking: src?.thinking ?? "",
		implAuth: src?.implAuth ?? false,
		commentOnlyReviews: src?.commentOnlyReviews ?? false,
		autocloseHours: String(src?.autocloseHours ?? DEFAULT_AUTOCLOSE_HOURS),
		maxDurationMin: src?.maxDurationSec ? String(src.maxDurationSec / 60) : "",
		completionReminders: src?.completionReminders ?? false,
		releasePrefix: src?.releasePrefix ?? DEFAULT_RELEASE_PREFIX,
		handToAssistant: src?.handToAssistant ?? false,
	};
}

/** 表单 → 请求体（新建与编辑共用）。任务时限留空 = 0（不限时）。 */
function bodyOf(f: TriggerForm): GitHubTriggerInput {
	const minutes = Number(f.maxDurationMin);
	return {
		name: f.name.trim(),
		event: f.event,
		...(f.event === "issue_labeled" ? { label: f.label.trim() } : {}),
		prompt: f.prompt,
		announce: f.announce,
		drivers: f.drivers,
		maintainers: parseLogins(f.maintainers),
		reviewerBots: parseLogins(f.reviewerBots),
		models: f.models,
		thinking: f.thinking,
		implAuth: f.implAuth,
		commentOnlyReviews: f.commentOnlyReviews,
		autocloseHours: Number(f.autocloseHours),
		maxDurationSec: f.maxDurationMin.trim() ? Math.round(minutes * 60) : 0,
		completionReminders: f.completionReminders,
		handToAssistant: f.handToAssistant,
		...(f.event === "workflow_run_completed"
			? { releasePrefix: f.releasePrefix }
			: {}),
	};
}

function formValid(f: TriggerForm): boolean {
	const hours = Number(f.autocloseHours);
	const minutes = f.maxDurationMin.trim() ? Number(f.maxDurationMin) : 0;
	return (
		f.name.trim().length > 0 &&
		f.prompt.trim().length > 0 &&
		(f.event !== "issue_labeled" || f.label.trim().length > 0) &&
		(f.event !== "workflow_run_completed" ||
			f.releasePrefix.trim().length > 0) &&
		Number.isFinite(hours) &&
		hours > 0 &&
		Number.isFinite(minutes) &&
		minutes >= 0
	);
}

/** 规则行里的事件文案。 */
function EventText({ trigger }: { trigger: GitHubTrigger }): ReactNode {
	const { t } = useTranslation();
	if (trigger.event === "issue_labeled")
		return (
			<>
				{t("github.triggers.event.labeledWith")}{" "}
				<code className="font-mono">{trigger.label}</code>
			</>
		);
	return t(`github.triggers.event.${trigger.event}`);
}

function TriggerRow({
	trigger,
	projectId,
	onEdit,
}: {
	trigger: GitHubTrigger;
	projectId: string;
	onEdit: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateGitHubTrigger(projectId);
	const del = useDeleteGitHubTrigger(projectId);
	const enabled = update.isPending
		? (update.variables.patch.enabled ?? trigger.enabled)
		: trigger.enabled;
	return (
		<li data-testid="github-trigger-row">
			<ListRow
				leading={
					<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
						<Icon name="bolt" size={15} />
					</span>
				}
				title={
					<>
						<span
							className={`truncate font-medium ${enabled ? "" : "text-faint"}`}
						>
							{trigger.name}
						</span>
						{!enabled && <Badge>{t("github.triggers.paused")}</Badge>}
					</>
				}
				meta={
					<>
						<span>
							<EventText trigger={trigger} />
						</span>
						<span>
							{t("github.triggers.runCount", { n: trigger.runCount })}
						</span>
						{trigger.lastRunAt && (
							<span>
								{t("github.triggers.lastRun", {
									when: formatRelative(trigger.lastRunAt),
								})}
							</span>
						)}
						<ErrorText error={update.error ?? del.error} />
					</>
				}
				trailing={
					<>
						<Toggle
							checked={enabled}
							disabled={update.isPending}
							onChange={(v) =>
								update.mutate({ id: trigger.id, patch: { enabled: v } })
							}
							label={
								<span className="sr-only">
									{t("github.triggers.enable", { name: trigger.name })}
								</span>
							}
						/>
						<IconButton
							icon="edit"
							label={t("github.triggers.edit", { name: trigger.name })}
							onClick={onEdit}
						/>
						<IconButton
							icon="trash"
							label={t("github.triggers.delete", { name: trigger.name })}
							disabled={del.isPending}
							onClick={() => {
								if (
									window.confirm(
										t("github.triggers.deleteConfirm", { name: trigger.name }),
									)
								)
									del.mutate(trigger.id);
							}}
						/>
					</>
				}
			/>
		</li>
	);
}

/** 模型池：已选模型的胶囊（可移除）+「添加模型」弹层（中央目录，也可直接输入 `provider/id`）。 */
function ModelPool({
	value,
	onChange,
}: {
	value: ModelRef[];
	onChange: (next: ModelRef[]) => void;
}): ReactNode {
	const { t } = useTranslation();
	const labelId = useId();
	const [open, setOpen] = useState(false);
	return (
		<div className="flex flex-col gap-1 text-sm">
			<span id={labelId} className="font-medium text-muted text-xs">
				{t("github.triggers.models")}
			</span>
			<div className="flex flex-wrap items-center gap-1.5">
				{value.length > 0 && (
					<ul
						aria-labelledby={labelId}
						className="m-0 flex list-none flex-wrap gap-1.5 p-0"
					>
						{value.map((m) => {
							const name = `${m.provider}/${m.id}`;
							return (
								<li
									key={name}
									data-testid="trigger-model"
									className="inline-flex h-7 items-center gap-1 rounded-full border border-border bg-inset pr-0.5 pl-2.5 text-xs"
								>
									<ModelLogo model={m.id} provider={m.provider} size={12} />
									<span className="font-mono">{name}</span>
									<IconButton
										icon="close"
										box="xs"
										size={12}
										label={t("github.triggers.removeModel", { model: name })}
										onClick={() =>
											onChange(
												value.filter(
													(x) => x.provider !== m.provider || x.id !== m.id,
												),
											)
										}
									/>
								</li>
							);
						})}
					</ul>
				)}
				<span className="relative inline-flex">
					<Button
						size="sm"
						icon="plus"
						aria-expanded={open}
						onClick={() => setOpen((v) => !v)}
					>
						{t("github.triggers.addModel")}
					</Button>
					{open && (
						<Popover side="bottom" onClose={() => setOpen(false)}>
							<div className="p-1">
								<ModelPickerBody
									free
									onPick={({ provider, id }) => {
										if (
											!value.some((x) => x.provider === provider && x.id === id)
										)
											onChange([...value, { provider, id }]);
										setOpen(false);
									}}
								/>
							</div>
						</Popover>
					)}
				</span>
			</div>
			<span className="text-faint text-xs">
				{value.length === 0
					? t("github.triggers.modelsDefault")
					: t("github.triggers.modelsHint")}
			</span>
		</div>
	);
}

/** 对话框里的分段小标题。 */
function Section({ children }: { children: ReactNode }): ReactNode {
	return (
		<h3 className="mt-2 border-border border-t pt-3 font-medium text-sm">
			{children}
		</h3>
	);
}

/** 预设按钮（新建时）：点一下用预设填满表单；预设带门禁时提示可应用到本项目。 */
function PresetBar({
	projectId,
	onApply,
}: {
	projectId: string;
	onApply: (trigger: GitHubTriggerInput) => void;
}): ReactNode {
	const { t } = useTranslation();
	const presets = useGitHubPresets();
	const setGates = useSetProjectGates(projectId);
	const [gates, setPresetGates] = useState<GitHubGates | null>(null);
	const list = presets.data?.presets ?? [];
	if (presets.isPending) return <Spinner size={12} />;
	if (presets.isError) return <ErrorText error={presets.error} />;
	if (list.length === 0) return null;
	return (
		<div className="flex flex-col gap-2" data-testid="trigger-presets">
			<div className="flex flex-wrap items-center gap-2">
				<span className="font-medium text-muted text-xs">
					{t("github.triggers.startFrom")}
				</span>
				{list.map((p) => (
					<Button
						key={p.id}
						size="sm"
						icon="sparkle"
						onClick={() => {
							onApply(p.trigger);
							setGates.reset();
							setPresetGates(p.gates ?? null);
						}}
					>
						{p.name}
					</Button>
				))}
			</div>
			{gates && (
				<Callout
					tone="neutral"
					icon="lock"
					actions={
						setGates.isSuccess ? (
							<span role="status" className="text-ok text-xs">
								{t("github.triggers.presetGatesApplied")}
							</span>
						) : (
							<Button
								size="sm"
								disabled={setGates.isPending}
								onClick={() => setGates.mutate(gates)}
							>
								{setGates.isPending && <Spinner size={12} />}
								{t("github.triggers.presetGatesApply")}
							</Button>
						)
					}
				>
					{t("github.triggers.presetGates")}
					<ErrorText error={setGates.error} />
				</Callout>
			)}
		</div>
	);
}

export function TriggerDialog({
	projectId,
	trigger,
	onClose,
}: {
	projectId: string;
	/** 有则编辑，无则新建 */
	trigger?: GitHubTrigger;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const create = useCreateGitHubTrigger(projectId);
	const update = useUpdateGitHubTrigger(projectId);
	const [f, setF] = useState<TriggerForm>(() => formOf(trigger));
	const set = <K extends keyof TriggerForm>(k: K, v: TriggerForm[K]) =>
		setF((prev) => ({ ...prev, [k]: v }));
	const agentMissing = f.event === "mention" && !status.data?.identities?.agent;
	const valid = formValid(f);
	const pending = create.isPending || update.isPending;
	const save = () => {
		const body = bodyOf(f);
		const done = { onSuccess: onClose };
		if (trigger) update.mutate({ id: trigger.id, patch: body }, done);
		else create.mutate(body, done);
	};
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={
				trigger ? t("github.triggers.editTitle") : t("github.triggers.newTitle")
			}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={!valid || agentMissing || pending}
						onClick={save}
					>
						{pending && <Spinner size={13} />}
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				data-testid="github-trigger-dialog"
				onSubmit={(e) => {
					e.preventDefault();
					if (valid && !agentMissing && !pending) save();
				}}
			>
				{!trigger && (
					<PresetBar projectId={projectId} onApply={(tr) => setF(formOf(tr))} />
				)}
				<Field label={t("github.triggers.name")}>
					<TextInput
						required
						maxLength={100}
						value={f.name}
						onChange={(e) => set("name", e.target.value)}
					/>
				</Field>
				<Field label={t("github.triggers.eventLabel")}>
					<Select
						value={f.event}
						onChange={(e) => set("event", e.target.value as TriggerEvent)}
					>
						{EVENTS.map((ev) => (
							<option key={ev} value={ev}>
								{t(`github.triggers.event.${ev}`)}
							</option>
						))}
					</Select>
				</Field>
				{f.event === "issue_labeled" && (
					<Field label={t("github.triggers.label")}>
						<TextInput
							required
							maxLength={100}
							placeholder="oryn"
							value={f.label}
							onChange={(e) => set("label", e.target.value)}
						/>
					</Field>
				)}
				{agentMissing && (
					<Callout tone="warn" role="alert">
						{t("github.triggers.agentMissing")}
					</Callout>
				)}
				<Toggle
					checked={f.announce}
					onChange={(v) => set("announce", v)}
					label={t("github.triggers.announce")}
				/>
				<Field label={t("github.triggers.preset")}>
					<Select
						value=""
						onChange={(e) => {
							const preset = TRIGGER_PRESETS.find(
								(p) => p.key === e.target.value,
							);
							if (preset) set("prompt", preset.prompt);
						}}
					>
						<option value="">{t("github.triggers.presetNone")}</option>
						{TRIGGER_PRESETS.map((p) => (
							<option key={p.key} value={p.key}>
								{t(p.labelKey)}
							</option>
						))}
					</Select>
				</Field>
				<Field
					label={t("github.triggers.prompt")}
					hint={t("github.triggers.promptHint", {
						vars: "{{repo}} {{number}} {{kind}} {{url}}",
					})}
				>
					<TextArea
						required
						rows={8}
						maxLength={20_000}
						value={f.prompt}
						onChange={(e) => set("prompt", e.target.value)}
					/>
				</Field>

				<Section>{t("github.triggers.sectionDrivers")}</Section>
				<fieldset className="m-0 flex flex-col gap-2 border-0 p-0">
					<legend className="mb-1 font-medium text-muted text-xs">
						{t("github.triggers.drivers.label")}
					</legend>
					{DRIVERS.map((d) => (
						// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input）
						<label key={d} className="flex cursor-pointer items-start gap-2">
							<span className="mt-0.5 inline-flex">
								<Radio
									name="github-trigger-drivers"
									value={d}
									checked={f.drivers === d}
									onChange={() => set("drivers", d)}
								/>
							</span>
							<span className="flex min-w-0 flex-col">
								<span className="text-sm">
									{t(`github.triggers.drivers.${d}`)}
								</span>
								<span className="text-faint text-xs">
									{t(`github.triggers.drivers.${d}Hint`)}
								</span>
							</span>
						</label>
					))}
				</fieldset>
				<div className="grid gap-3 sm:grid-cols-2">
					<Field
						label={t("github.triggers.maintainers")}
						hint={t("github.triggers.maintainersHint")}
					>
						<TextInput
							spellCheck={false}
							autoComplete="off"
							placeholder="octocat, hubot"
							value={f.maintainers}
							onChange={(e) => set("maintainers", e.target.value)}
						/>
					</Field>
					<Field
						label={t("github.triggers.reviewerBots")}
						hint={t("github.triggers.reviewerBotsHint")}
					>
						<TextInput
							spellCheck={false}
							autoComplete="off"
							placeholder="coderabbitai[bot]"
							value={f.reviewerBots}
							onChange={(e) => set("reviewerBots", e.target.value)}
						/>
					</Field>
				</div>

				<Section>{t("github.triggers.sectionModel")}</Section>
				<ModelPool value={f.models} onChange={(v) => set("models", v)} />
				<Field label={t("github.triggers.thinking")}>
					<Select
						wrapperClassName="w-full max-w-60"
						value={f.thinking}
						onChange={(e) => set("thinking", e.target.value)}
					>
						<option value="">{t("github.triggers.thinkingDefault")}</option>
						{TRIGGER_THINKING.map((l) => (
							<option key={l} value={l}>
								{t(`model.levels.${l}`)}
							</option>
						))}
					</Select>
				</Field>

				<Section>{t("github.triggers.sectionTask")}</Section>
				<Toggle
					checked={f.implAuth}
					onChange={(v) => set("implAuth", v)}
					label={t("github.triggers.implAuth")}
				/>
				<p className="-mt-2 text-faint text-xs">
					{t("github.triggers.implAuthHint")}
				</p>
				{f.event === "pull_request_opened" && (
					<>
						<Toggle
							checked={f.commentOnlyReviews}
							onChange={(v) => set("commentOnlyReviews", v)}
							label={t("github.triggers.commentOnlyReviews")}
						/>
						<p className="-mt-2 text-faint text-xs">
							{t("github.triggers.commentOnlyReviewsHint")}
						</p>
					</>
				)}
				<Toggle
					checked={f.completionReminders}
					onChange={(v) => set("completionReminders", v)}
					label={t("github.triggers.completionReminders")}
				/>
				<p className="-mt-2 text-faint text-xs">
					{t("github.triggers.completionRemindersHint")}
				</p>
				<Toggle
					checked={f.handToAssistant}
					onChange={(v) => set("handToAssistant", v)}
					label={t("github.triggers.handToAssistant")}
				/>
				<p className="-mt-2 text-faint text-xs">
					{t("github.triggers.handToAssistantHint")}
				</p>
				<div className="grid gap-3 sm:grid-cols-2">
					<Field
						label={t("github.triggers.autocloseHours")}
						hint={t("github.triggers.autocloseHoursHint")}
					>
						<TextInput
							type="number"
							inputMode="numeric"
							min={1}
							step={1}
							value={f.autocloseHours}
							onChange={(e) => set("autocloseHours", e.target.value)}
						/>
					</Field>
					<Field
						label={t("github.triggers.maxDuration")}
						hint={t("github.triggers.maxDurationHint")}
					>
						<TextInput
							type="number"
							inputMode="numeric"
							min={0}
							step={1}
							placeholder={t("github.triggers.maxDurationNone")}
							value={f.maxDurationMin}
							onChange={(e) => set("maxDurationMin", e.target.value)}
						/>
					</Field>
				</div>
				{f.event === "workflow_run_completed" && (
					<Field
						label={t("github.triggers.releasePrefix")}
						hint={t("github.triggers.releasePrefixHint")}
					>
						<TextInput
							required
							spellCheck={false}
							value={f.releasePrefix}
							onChange={(e) => set("releasePrefix", e.target.value)}
						/>
					</Field>
				)}
				<ErrorText error={create.error ?? update.error} />
			</form>
		</Dialog>
	);
}

export function GitHubTriggers({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const triggers = useGitHubTriggers(project.id);
	const [editing, setEditing] = useState<GitHubTrigger | "new" | null>(null);
	const list = triggers.data?.triggers ?? [];
	return (
		<div
			data-testid={`github-triggers-${project.id}`}
			className="flex flex-col gap-2"
		>
			<div className="flex items-center gap-2">
				<h4 className="font-medium text-muted text-xs">
					{t("github.triggers.title")}
				</h4>
				<span className="min-w-0 flex-1" />
				<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
					{t("github.triggers.new")}
				</Button>
			</div>
			{triggers.isPending && <Spinner size={12} />}
			<ErrorText error={triggers.error} />
			{triggers.isSuccess && list.length === 0 && (
				<p className="text-faint text-xs">{t("github.triggers.empty")}</p>
			)}
			{list.length > 0 && (
				<ul className="-mx-2 flex flex-col">
					{list.map((tr) => (
						<TriggerRow
							key={tr.id}
							trigger={tr}
							projectId={project.id}
							onEdit={() => setEditing(tr)}
						/>
					))}
				</ul>
			)}
			{editing && (
				<TriggerDialog
					projectId={project.id}
					trigger={editing === "new" ? undefined : editing}
					onClose={() => setEditing(null)}
				/>
			)}
		</div>
	);
}
