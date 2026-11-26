/**
 * 项目设置「密钥与环境变量」（M24 阶段 ④）：本项目的环境变量（可勾「保密」）与 Git 令牌 `git/<主机>/token`（总是保密）。
 * 自上而下：说明、添加表单、条目列表（编辑 / 删除）；列表卡右上角「批量添加」打开粘贴 `.env` 的对话框。
 * 保密条目保存后读不回值：编辑时值留空、必须重新输入；不保密的条目值可见可改。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type FormEvent,
	type ReactNode,
	useId,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Project } from "../../../api/client";
import {
	deleteProjectSecret,
	type ProjectSecret,
	type ProjectSecretPut,
	projectSecretKeys,
	putProjectSecret,
	useProjectSecrets,
} from "../../../api/project-secrets-queries";
import { useAsync } from "../../../components/settings/useAsync";
import { Badge, Card, ListRow } from "../../../components/ui/data";
import {
	Button,
	Checkbox,
	Dialog,
	EmptyState,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
} from "../../../components/ui/primitives";
import { confirmAction } from "../../../lib/confirm";
import { ENV_NAME_RE, parseDotenv } from "../../../lib/dotenv";
import { formatRelative } from "../../../lib/format";

type Kind = "env" | "git";

/** Git 令牌的主机名（与服务端 `git/<host>/token` 的规则一致：小写）。 */
const HOST_RE = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
const MASK = "••••••••";

/** `git/<host>/token` 条目的主机；不是 Git 令牌时为 undefined。 */
function gitHost(s: ProjectSecret): string | undefined {
	const m = /^git\/(.+)\/token$/.exec(s.name);
	return s.mode === "proxy" && m ? m[1] : undefined;
}

interface Draft {
	kind: Kind;
	name: string;
	host: string;
	value: string;
	secret: boolean;
}

function draftOf(existing?: ProjectSecret, kind: Kind = "env"): Draft {
	if (!existing) return { kind, name: "", host: "", value: "", secret: true };
	const host = gitHost(existing);
	return {
		kind: host === undefined ? "env" : "git",
		name: existing.name,
		host: host ?? "",
		value: existing.secret ? "" : (existing.value ?? ""),
		secret: existing.secret,
	};
}

const draftHost = (d: Draft) => d.host.trim().toLowerCase();
const draftName = (d: Draft) =>
	d.kind === "git" ? `git/${draftHost(d)}/token` : d.name.trim();
const draftSecret = (d: Draft) => d.kind === "git" || d.secret;
const draftReady = (d: Draft) =>
	d.value !== "" &&
	(d.kind === "git"
		? HOST_RE.test(draftHost(d))
		: ENV_NAME_RE.test(d.name.trim()));
const draftBody = (d: Draft): ProjectSecretPut => ({
	value: d.value,
	mode: d.kind === "git" ? "proxy" : "env",
	secret: draftSecret(d),
});

/** 条目表单的字段；新增与编辑共用，编辑时类型与名称只读。 */
function EntryFields({
	draft,
	onChange,
	existing,
}: {
	draft: Draft;
	onChange: (d: Draft) => void;
	existing?: ProjectSecret;
}): ReactNode {
	const { t } = useTranslation();
	const set = (p: Partial<Draft>) => onChange({ ...draft, ...p });
	const secret = draftSecret(draft);
	const locked = existing !== undefined;
	return (
		<div className="grid gap-3 sm:grid-cols-2">
			<Field label={t("projectSecrets.kind")}>
				<Select
					value={draft.kind}
					disabled={locked}
					onChange={(v) => set({ kind: v })}
					options={[
						{ value: "env", label: t("projectSecrets.kindEnv") },
						{ value: "git", label: t("projectSecrets.kindGit") },
					]}
				/>
			</Field>
			{draft.kind === "git" ? (
				<Field
					label={t("projectSecrets.host")}
					hint={locked ? undefined : t("projectSecrets.hostHint")}
				>
					<TextInput
						className="font-mono"
						value={draft.host}
						readOnly={locked}
						autoComplete="off"
						spellCheck={false}
						placeholder="gitlab.com"
						onChange={(e) => set({ host: e.target.value })}
					/>
				</Field>
			) : (
				<Field
					label={t("projectSecrets.name")}
					hint={locked ? undefined : t("projectSecrets.nameHint")}
				>
					<TextInput
						className="font-mono"
						value={draft.name}
						readOnly={locked}
						autoComplete="off"
						spellCheck={false}
						onChange={(e) => set({ name: e.target.value })}
					/>
				</Field>
			)}
			<div className="sm:col-span-2">
				<Field label={t("projectSecrets.value")}>
					<TextInput
						className="font-mono"
						type={secret ? "password" : "text"}
						autoComplete="off"
						spellCheck={false}
						value={draft.value}
						placeholder={
							existing?.secret ? t("projectSecrets.newValue") : undefined
						}
						onChange={(e) => set({ value: e.target.value })}
					/>
				</Field>
			</div>
			<div className="flex flex-col gap-1 sm:col-span-2">
				{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
				<label className="inline-flex items-center gap-2 self-start text-sm">
					<Checkbox
						checked={secret}
						disabled={draft.kind === "git"}
						onChange={(e) => set({ secret: e.target.checked })}
					/>
					{t("projectSecrets.secret")}
				</label>
				<span className="text-faint text-xs">
					{draft.kind === "git"
						? t("projectSecrets.gitAlwaysSecret")
						: t("projectSecrets.secretHint")}
				</span>
			</div>
		</div>
	);
}

function AddCard({ projectId }: { projectId: string }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [draft, setDraft] = useState(() => draftOf());
	const save = useAsync();
	const submit = async (e: FormEvent) => {
		e.preventDefault();
		const ok = await save.run(() =>
			putProjectSecret(projectId, draftName(draft), draftBody(draft)),
		);
		if (!ok) return;
		setDraft(draftOf(undefined, draft.kind));
		void qc.invalidateQueries({
			queryKey: projectSecretKeys.list(projectId),
		});
	};
	return (
		<Card title={t("projectSecrets.addTitle")} icon="plus">
			<form className="flex flex-col gap-3" onSubmit={submit}>
				<EntryFields draft={draft} onChange={setDraft} />
				<div>
					<Button
						type="submit"
						variant="primary"
						disabled={save.busy || !draftReady(draft)}
					>
						{t("common.save")}
					</Button>
				</div>
				<ErrorText error={save.error} />
			</form>
		</Card>
	);
}

function EditDialog({
	projectId,
	existing,
	onClose,
}: {
	projectId: string;
	existing: ProjectSecret;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const formId = useId();
	const [draft, setDraft] = useState(() => draftOf(existing));
	const save = useAsync();
	const submit = async (e: FormEvent) => {
		e.preventDefault();
		const ok = await save.run(() =>
			putProjectSecret(projectId, existing.name, draftBody(draft)),
		);
		if (!ok) return;
		void qc.invalidateQueries({
			queryKey: projectSecretKeys.list(projectId),
		});
		onClose();
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("projectSecrets.editTitle", { name: existing.name })}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						type="submit"
						form={formId}
						variant="primary"
						disabled={save.busy || !draftReady(draft)}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form id={formId} className="flex flex-col gap-3" onSubmit={submit}>
				<EntryFields draft={draft} onChange={setDraft} existing={existing} />
				<ErrorText error={save.error} />
			</form>
		</Dialog>
	);
}

function BulkDialog({
	projectId,
	existing,
	onClose,
}: {
	projectId: string;
	existing: ReadonlySet<string>;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [text, setText] = useState("");
	/** 每项的保密位；没记过的按默认勾选 */
	const [plain, setPlain] = useState<ReadonlySet<string>>(new Set());
	const [progress, setProgress] = useState<{
		done: number;
		total: number;
	} | null>(null);
	/** 上一轮没导入的项与原因；null = 还没导入过（或之后改了文本） */
	const [failed, setFailed] = useState<Record<string, unknown> | null>(null);
	const parsed = useMemo(() => parseDotenv(text), [text]);
	const items = failed
		? parsed.entries.filter((e) => e.name in failed)
		: parsed.entries;
	const busy = progress !== null;

	const run = async () => {
		const total = items.length;
		const errs: Record<string, unknown> = {};
		setProgress({ done: 0, total });
		for (const [i, e] of items.entries()) {
			try {
				await putProjectSecret(projectId, e.name, {
					value: e.value,
					mode: "env",
					secret: !plain.has(e.name),
				});
			} catch (err) {
				errs[e.name] = err;
			}
			setProgress({ done: i + 1, total });
		}
		setProgress(null);
		void qc.invalidateQueries({
			queryKey: projectSecretKeys.list(projectId),
		});
		if (Object.keys(errs).length === 0) onClose();
		else setFailed(errs);
	};

	const failedCount = failed ? Object.keys(failed).length : 0;
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("projectSecrets.bulkTitle")}
			bodyTestId="project-secrets-bulk"
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={busy || items.length === 0}
						onClick={() => void run()}
					>
						{progress
							? t("projectSecrets.bulkProgress", progress)
							: t("projectSecrets.bulkImport", { count: items.length })}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				<p className="text-muted text-sm">{t("projectSecrets.bulkHint")}</p>
				<TextArea
					aria-label={t("projectSecrets.bulkTitle")}
					rows={8}
					spellCheck={false}
					placeholder={'FOO=bar\nexport TOKEN="…"'}
					value={text}
					disabled={busy}
					onChange={(e) => {
						setText(e.target.value);
						setFailed(null);
					}}
				/>
				{failedCount > 0 && (
					<p role="alert" className="text-err text-sm">
						{t("projectSecrets.bulkFailed", { count: failedCount })}
					</p>
				)}
				{items.length > 0 && (
					<section className="flex flex-col gap-1">
						<h3 className="font-medium text-muted text-xs">
							{t("projectSecrets.bulkPreview", { count: items.length })}
						</h3>
						<ul className="flex flex-col">
							{items.map((e) => {
								const secret = !plain.has(e.name);
								return (
									<li
										key={e.name}
										data-testid="bulk-entry"
										data-name={e.name}
										className="flex min-w-0 flex-col gap-0.5 py-1.5"
									>
										<div className="flex min-w-0 items-center gap-2">
											<span className="min-w-0 truncate font-mono text-sm">
												{e.name}
											</span>
											{existing.has(e.name) && (
												<Badge>{t("projectSecrets.bulkOverwrite")}</Badge>
											)}
											{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
											<label className="ml-auto inline-flex shrink-0 items-center gap-1.5 text-muted text-xs">
												<Checkbox
													checked={secret}
													disabled={busy}
													onChange={(ev) =>
														setPlain((s) => {
															const next = new Set(s);
															if (ev.target.checked) next.delete(e.name);
															else next.add(e.name);
															return next;
														})
													}
												/>
												{t("projectSecrets.secret")}
											</label>
										</div>
										<span
											className="truncate font-mono text-faint text-xs"
											title={secret ? undefined : e.value}
										>
											{secret ? MASK : e.value}
										</span>
										{failed?.[e.name] !== undefined && (
											<ErrorText error={failed[e.name]} />
										)}
									</li>
								);
							})}
						</ul>
					</section>
				)}
				{parsed.issues.length > 0 && (
					<section className="flex flex-col gap-1">
						<h3 className="font-medium text-muted text-xs">
							{t("projectSecrets.bulkIssues")}
						</h3>
						<ul className="flex flex-col gap-0.5">
							{parsed.issues.map((is) => (
								<li
									key={`${is.line}:${is.reason}`}
									className="flex gap-2 text-xs"
								>
									<span className="shrink-0 text-muted tabular-nums">
										{t("projectSecrets.bulkLine", { line: is.line })}
									</span>
									<span className="min-w-0 text-faint">
										{t(`projectSecrets.issue.${is.reason}`, {
											other: is.other,
										})}
									</span>
								</li>
							))}
						</ul>
					</section>
				)}
			</div>
		</Dialog>
	);
}

function SecretRow({
	secret: s,
	onEdit,
	onDelete,
}: {
	secret: ProjectSecret;
	onEdit: () => void;
	onDelete: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const host = gitHost(s);
	return (
		<li data-testid="project-secret-row" data-name={s.name}>
			<ListRow
				title={
					<>
						<span className="min-w-0 truncate font-mono">{host ?? s.name}</span>
						{host === undefined ? (
							<Badge tone="frost">{t("projectSecrets.kindEnv")}</Badge>
						) : (
							<Badge icon="branch">{t("projectSecrets.kindGit")}</Badge>
						)}
					</>
				}
				meta={
					<>
						{s.secret ? (
							<>
								<span title={t("projectSecrets.hidden")}>
									<span aria-hidden>{MASK}</span>
									<span className="sr-only">{t("projectSecrets.hidden")}</span>
								</span>
								<Badge icon="lock">{t("projectSecrets.secret")}</Badge>
							</>
						) : (
							<span
								className="min-w-0 max-w-full truncate font-mono"
								title={s.value}
							>
								{s.value}
							</span>
						)}
						<span>{formatRelative(s.updatedAt)}</span>
					</>
				}
				trailing={
					<>
						<IconButton
							icon="edit"
							label={t("projectSecrets.editLabel", { name: s.name })}
							onClick={onEdit}
						/>
						<IconButton
							icon="trash"
							label={t("projectSecrets.deleteLabel", { name: s.name })}
							onClick={onDelete}
						/>
					</>
				}
			/>
		</li>
	);
}

export function SecretsTab({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const list = useProjectSecrets(project.id);
	const del = useAsync();
	const [editing, setEditing] = useState<ProjectSecret | null>(null);
	const [bulk, setBulk] = useState(false);
	const secrets = list.data?.secrets;
	const names = useMemo(() => new Set(secrets?.map((s) => s.name)), [secrets]);

	const remove = async (s: ProjectSecret) => {
		const ok = await confirmAction({
			title: t("projectSecrets.deleteConfirm", { name: s.name }),
			body: t("projectSecrets.deleteBody"),
			action: t("common.delete"),
			danger: true,
		});
		if (!ok) return;
		await del.run(() => deleteProjectSecret(project.id, s.name));
		void qc.invalidateQueries({
			queryKey: projectSecretKeys.list(project.id),
		});
	};

	return (
		<div data-testid="project-secrets" className="flex flex-col gap-4">
			<p className="text-muted text-sm">{t("projectSecrets.intro")}</p>
			<AddCard projectId={project.id} />
			<Card
				title={t("projectSecrets.listTitle")}
				icon="key"
				action={
					<Button
						variant="secondary"
						size="sm"
						icon="upload"
						onClick={() => setBulk(true)}
					>
						{t("projectSecrets.bulk")}
					</Button>
				}
			>
				<div className="flex flex-col gap-2">
					{list.isPending && <Spinner />}
					<ErrorText error={list.error ?? del.error} />
					{secrets && secrets.length > 0 && (
						<ul className="-mx-2 flex flex-col">
							{secrets.map((s) => (
								<SecretRow
									key={s.name}
									secret={s}
									onEdit={() => setEditing(s)}
									onDelete={() => void remove(s)}
								/>
							))}
						</ul>
					)}
					{secrets?.length === 0 && (
						<EmptyState icon="key" title={t("projectSecrets.empty")}>
							{t("projectSecrets.emptyBody")}
						</EmptyState>
					)}
				</div>
			</Card>
			{editing && (
				<EditDialog
					projectId={project.id}
					existing={editing}
					onClose={() => setEditing(null)}
				/>
			)}
			{bulk && (
				<BulkDialog
					projectId={project.id}
					existing={names}
					onClose={() => setBulk(false)}
				/>
			)}
		</div>
	);
}
