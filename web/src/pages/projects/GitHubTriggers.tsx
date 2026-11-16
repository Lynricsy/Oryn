/**
 * 项目 GitHub 卡片里的「触发规则」（M14）：新 issue / 打指定标签 / @Agent 账号 → 新建线程。
 * 列表每行 = 名称、事件、已触发次数、最近触发、启停、编辑、删除；没有规则时一行说明。
 * 对话框（新建与编辑共用）：名称、事件、标签（仅「打标签」）、回帖线程链接、提示词（可套用预设）。
 * 「@Agent 账号」要求 Agent 账号已连接：未连接时对话框里给出警告并禁用保存（服务端同样 409 `github_agent_required`）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Project, Schemas } from "../../api/client";
import {
	type GitHubTrigger,
	useCreateGitHubTrigger,
	useDeleteGitHubTrigger,
	useGitHubStatus,
	useGitHubTriggers,
	useUpdateGitHubTrigger,
} from "../../api/github-queries";
import { Badge, Callout, ListRow } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import { formatRelative } from "../../lib/format";
import { TRIGGER_PRESETS } from "../../lib/trigger-presets";

type TriggerEvent = Schemas["GitHubTriggerEvent"];

const EVENTS: readonly TriggerEvent[] = [
	"issue_opened",
	"issue_labeled",
	"mention",
];

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
	const [name, setName] = useState(trigger?.name ?? "");
	const [event, setEvent] = useState<TriggerEvent>(
		trigger?.event ?? "issue_labeled",
	);
	const [label, setLabel] = useState(trigger?.label ?? "");
	const [announce, setAnnounce] = useState(trigger?.announce ?? true);
	const [prompt, setPrompt] = useState(trigger?.prompt ?? "");
	const agentMissing = event === "mention" && !status.data?.identities?.agent;
	const labeled = event === "issue_labeled";
	const valid =
		name.trim().length > 0 &&
		prompt.trim().length > 0 &&
		(!labeled || label.trim().length > 0);
	const pending = create.isPending || update.isPending;
	const save = () => {
		const body = {
			name: name.trim(),
			event,
			...(labeled ? { label: label.trim() } : {}),
			prompt,
			announce,
		};
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
				<Field label={t("github.triggers.name")}>
					<TextInput
						required
						maxLength={100}
						value={name}
						onChange={(e) => setName(e.target.value)}
					/>
				</Field>
				<Field label={t("github.triggers.eventLabel")}>
					<Select
						value={event}
						onChange={(e) => setEvent(e.target.value as TriggerEvent)}
					>
						{EVENTS.map((ev) => (
							<option key={ev} value={ev}>
								{t(`github.triggers.event.${ev}`)}
							</option>
						))}
					</Select>
				</Field>
				{labeled && (
					<Field label={t("github.triggers.label")}>
						<TextInput
							required
							maxLength={100}
							placeholder="oryn"
							value={label}
							onChange={(e) => setLabel(e.target.value)}
						/>
					</Field>
				)}
				{agentMissing && (
					<Callout tone="warn" role="alert">
						{t("github.triggers.agentMissing")}
					</Callout>
				)}
				<Toggle
					checked={announce}
					onChange={setAnnounce}
					label={t("github.triggers.announce")}
				/>
				<Field label={t("github.triggers.preset")}>
					<Select
						value=""
						onChange={(e) => {
							const preset = TRIGGER_PRESETS.find(
								(p) => p.key === e.target.value,
							);
							if (preset) setPrompt(preset.prompt);
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
						value={prompt}
						onChange={(e) => setPrompt(e.target.value)}
					/>
				</Field>
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
