/**
 * 主线页头的「跟进」（M26）：后台研究替主人跟进的话题。顶部是新建表单（种类、标题、要查什么）；
 * 每行显示标题、种类（问题跟进 / 长期关注）、来源（自动 / 手动 / 线程）、下次检查时间与上次发现的开头，
 * 行内有暂停开关与菜单（立即检查、编辑、删除；删除先确认，再 `DELETE …/topics/{id}`，服务端置为 `dismissed`）。
 * 后台研究总开关关着时顶部提示并链到设置。没有 WS 推送，开着时 30 秒轮询一次列表。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type ResearchTopic,
	useCreateResearchTopic,
	useDeleteResearchTopic,
	useResearchSettings,
	useResearchTopics,
	useRunResearchTopic,
	useUpdateResearchTopic,
} from "../../api/research-queries";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";
import { Badge, Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	Field,
	Menu,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../ui/primitives";

/** 标题与问题的长度上限（Unicode 字符，同服务端）。 */
const TITLE_MAX = 80;
const QUESTION_MAX = 600;
/** 行内显示上次发现的前多少个字。 */
const REPORT_PREVIEW = 200;

type TopicDraft = Pick<ResearchTopic, "kind" | "title" | "question">;

/** 新建与编辑共用的表单：种类、标题、要查什么。 */
function TopicForm({
	initial,
	submitLabel,
	pending,
	error,
	onSubmit,
	onCancel,
}: {
	initial: TopicDraft;
	submitLabel: string;
	pending: boolean;
	error: unknown;
	onSubmit: (draft: TopicDraft, reset: () => void) => void;
	onCancel?: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const [draft, setDraft] = useState(initial);
	const ready = draft.title.trim() !== "" && draft.question.trim() !== "";
	return (
		<form
			className="flex flex-col gap-2"
			onSubmit={(e) => {
				e.preventDefault();
				if (ready && !pending) onSubmit(draft, () => setDraft(initial));
			}}
		>
			<div className="grid gap-2 sm:grid-cols-[10rem_1fr]">
				<Field label={t("mainline.followUps.kindLabel")}>
					<Select
						value={draft.kind}
						onChange={(kind) => setDraft({ ...draft, kind })}
						options={(["question", "interest"] as const).map((v) => ({
							value: v,
							label: t(`mainline.followUps.kind.${v}`),
						}))}
					/>
				</Field>
				<Field label={t("mainline.followUps.titleLabel")}>
					<TextInput
						value={draft.title}
						maxLength={TITLE_MAX}
						onChange={(e) => setDraft({ ...draft, title: e.target.value })}
					/>
				</Field>
			</div>
			<Field
				label={t("mainline.followUps.questionLabel")}
				hint={t("mainline.followUps.questionHint")}
			>
				<TextArea
					mono={false}
					rows={2}
					value={draft.question}
					maxLength={QUESTION_MAX}
					onChange={(e) => setDraft({ ...draft, question: e.target.value })}
				/>
			</Field>
			<ErrorText error={error} />
			<div className="flex justify-end gap-2">
				{onCancel && (
					<Button size="sm" onClick={onCancel}>
						{t("common.cancel")}
					</Button>
				)}
				<Button
					type="submit"
					size="sm"
					variant="primary"
					disabled={!ready || pending}
				>
					{submitLabel}
				</Button>
			</div>
		</form>
	);
}

function TopicRow({
	topic,
	onEdit,
}: {
	topic: ResearchTopic;
	onEdit: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateResearchTopic();
	const run = useRunResearchTopic();
	const del = useDeleteResearchTopic();
	const error = update.error ?? run.error ?? del.error;
	const active = topic.state === "active";
	const report = [...topic.lastReport];
	return (
		<li
			data-topic={topic.id}
			className="flex min-h-11 items-start gap-3 rounded-md px-2 py-1.5"
		>
			<span className="flex min-w-0 flex-1 flex-col gap-0.5">
				<span className="flex min-w-0 items-center gap-1.5 text-sm">
					<Icon
						name={topic.kind === "question" ? "search" : "eye"}
						size={14}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<button
						type="button"
						onClick={onEdit}
						className="min-w-0 truncate text-left font-medium hover:underline"
					>
						{topic.title}
					</button>
				</span>
				<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-faint text-xs">
					<Badge tone={topic.kind === "question" ? "accent" : "neutral"}>
						{t(`mainline.followUps.kind.${topic.kind}`)}
					</Badge>
					<span>{t(`mainline.followUps.origin.${topic.origin}`)}</span>
					<span>
						{topic.state === "done"
							? t("mainline.followUps.done")
							: topic.state === "paused" || topic.nextRunAt === undefined
								? t("mainline.followUps.paused")
								: t("mainline.followUps.nextRun", {
										when: formatRelative(topic.nextRunAt),
									})}
					</span>
					{error ? (
						<span className="text-err">{describeError(error, t)}</span>
					) : null}
				</span>
				{report.length > 0 && (
					<p
						data-testid="topic-report"
						className="min-w-0 whitespace-pre-wrap break-words text-muted text-xs"
					>
						{report.length > REPORT_PREVIEW
							? `${report.slice(0, REPORT_PREVIEW).join("")}…`
							: topic.lastReport}
					</p>
				)}
			</span>
			<Toggle
				checked={active}
				disabled={update.isPending || topic.state === "done"}
				onChange={(on) =>
					update.mutate({
						id: topic.id,
						patch: { state: on ? "active" : "paused" },
					})
				}
				label={
					<span className="sr-only">
						{t("mainline.followUps.toggle", { title: topic.title })}
					</span>
				}
			/>
			<Menu
				items={[
					{
						key: "run",
						icon: "play",
						label: t("mainline.followUps.runNow"),
						disabled: !active || run.isPending,
						onSelect: () => run.mutate(topic.id),
					},
					{
						key: "edit",
						icon: "edit",
						label: t("common.edit"),
						onSelect: onEdit,
					},
					{
						key: "delete",
						icon: "trash",
						danger: true,
						label: t("common.delete"),
						onSelect: async () => {
							if (
								await confirmAction({
									title: t("mainline.followUps.deleteConfirm", {
										title: topic.title,
									}),
									body: t("mainline.followUps.deleteConfirmBody"),
									action: t("common.delete"),
									danger: true,
								})
							)
								del.mutate(topic.id);
						},
					},
				]}
				trigger={(p) => (
					<button
						type="button"
						aria-label={t("mainline.followUps.menu", { title: topic.title })}
						className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted transition-colors duration-150 hover:bg-hover hover:text-fg"
						{...p}
					>
						<Icon name="more" size={16} />
					</button>
				)}
			/>
		</li>
	);
}

/** 编辑一行：表单就地替换该行，保存后收起。 */
function TopicEditor({
	topic,
	onDone,
}: {
	topic: ResearchTopic;
	onDone: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateResearchTopic();
	return (
		<li className="rounded-md border border-border px-3 py-2">
			<TopicForm
				initial={{
					kind: topic.kind,
					title: topic.title,
					question: topic.question,
				}}
				submitLabel={t("common.save")}
				pending={update.isPending}
				error={update.error}
				onCancel={onDone}
				onSubmit={(d) =>
					update.mutate(
						{
							id: topic.id,
							patch: {
								kind: d.kind,
								title: d.title.trim(),
								question: d.question.trim(),
							},
						},
						{ onSuccess: onDone },
					)
				}
			/>
		</li>
	);
}

export function FollowUpsDialog({
	onClose,
}: {
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const topics = useResearchTopics(undefined, true);
	const settings = useResearchSettings();
	const create = useCreateResearchTopic();
	const [editing, setEditing] = useState<string | null>(null);
	const list = topics.data?.topics ?? [];
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("mainline.followUps.title")}
			footer={<Button onClick={onClose}>{t("common.close")}</Button>}
		>
			<div data-testid="main-follow-ups" className="flex flex-col gap-3">
				<p className="text-muted text-xs">{t("mainline.followUps.hint")}</p>
				{settings.data && !settings.data.settings.enabled && (
					<Callout
						tone="warn"
						actions={
							<Link
								to="/settings/assistant"
								onClick={onClose}
								className="font-medium text-accent-ink hover:underline"
							>
								{t("mainline.followUps.openSettings")}
							</Link>
						}
					>
						{t("mainline.followUps.disabled")}
					</Callout>
				)}
				<section className="rounded-md border border-border px-3 py-2">
					<h3 className="mb-1.5 font-medium text-sm">
						{t("mainline.followUps.new")}
					</h3>
					<TopicForm
						initial={{ kind: "question", title: "", question: "" }}
						submitLabel={t("mainline.followUps.create")}
						pending={create.isPending}
						error={create.error}
						onSubmit={(d, reset) =>
							create.mutate(
								{
									kind: d.kind,
									title: d.title.trim(),
									question: d.question.trim(),
								},
								{ onSuccess: reset },
							)
						}
					/>
				</section>
				{topics.isPending && <Spinner size={14} />}
				<ErrorText error={topics.error} />
				{!topics.isPending && list.length === 0 && (
					<p className="text-faint text-sm">{t("mainline.followUps.empty")}</p>
				)}
				{list.length > 0 && (
					<ul className="-mx-2 flex flex-col">
						{list.map((topic) =>
							editing === topic.id ? (
								<TopicEditor
									key={topic.id}
									topic={topic}
									onDone={() => setEditing(null)}
								/>
							) : (
								<TopicRow
									key={topic.id}
									topic={topic}
									onEdit={() => setEditing(topic.id)}
								/>
							),
						)}
					</ul>
				)}
			</div>
		</Dialog>
	);
}
