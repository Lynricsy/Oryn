/**
 * 信息面板「SSH 主机」卡（M18），只对工作线程渲染；默认收起，展开才查询。
 * - orb 线程：这台 orb 经控制面网关能用的主机（`threads/{id}/ssh-hosts`）。全局与本项目都没有主机时整张卡不渲染；
 *   全自动 orb 上未放开的主机置灰并标「全自动不可用」。每行可复制 `ssh <名>`、测试；底部链到设置页。
 * - runner 线程：那台机器自己的 SSH 配置（omp 的 `get_ssh_hosts` / `ssh_manage` / `ssh_test`），不经 Oryn 网关；
 *   可增删改（仅 `editable` 的项）与测试。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	sshTarget,
	useSshHosts,
	useTestSshHost,
	useThreadSshHosts,
} from "../../api/ssh-queries";
import type {
	RpcSshHostInfo,
	RpcSshHostInput,
	RpcSshTestResult,
} from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { agentQueryable, useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { HostStatusDot } from "../ssh/HostStatus";
import { CopyButton } from "../ui/CopyButton";
import { Badge, Callout, Card, ListRow, StatusDot } from "../ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextInput,
} from "../ui/primitives";
import { useOrbOf } from "./ThreadHeader";

/** 本卡默认收起（展开才发查询）：`collapsedCards` 里记的是「已展开」标记。 */
const EXPANDED_KEY = "ssh:expanded";

function useExpanded() {
	const expanded = useUi((s) => !!s.collapsedCards[EXPANDED_KEY]);
	return {
		collapsible: true,
		collapsed: !expanded,
		onToggle: () => uiStore.getState().toggleCard(EXPANDED_KEY),
		expanded,
	};
}

function ManageLink(): ReactNode {
	const { t } = useTranslation();
	return (
		<Link
			to="/settings/agent-ssh"
			className="mt-2 inline-block text-accent text-xs hover:underline"
		>
			{t("sshPanel.manage")}
		</Link>
	);
}

function ManagedSsh({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const projectId = useThread(threadId, (s) => s.thread?.projectId);
	const all = useSshHosts();
	const { expanded, ...fold } = useExpanded();
	const scoped = (all.data?.hosts ?? []).filter(
		(h) => h.projectId === undefined || h.projectId === projectId,
	);
	const list = useThreadSshHosts(threadId, expanded && scoped.length > 0);
	const test = useTestSshHost();
	if (scoped.length === 0) return null;
	if (list.data && (!list.data.managed || list.data.hosts.length === 0))
		return null;
	const hosts = list.data?.hosts ?? [];
	return (
		<Card
			title={t("panel.ssh")}
			icon="hardDrives"
			{...fold}
			summary={
				list.data && (
					<span className="truncate text-faint">
						{t("sshPanel.summary", { count: hosts.length })}
					</span>
				)
			}
		>
			{list.isPending && <Spinner size={12} />}
			<ErrorText error={list.error ?? test.error} />
			{hosts.length > 0 && (
				<ul className="-mx-1.5 flex flex-col gap-px" data-testid="ssh-panel">
					{hosts.map(({ host, usable, blocked }) => (
						<li key={host.id} className={usable ? undefined : "opacity-50"}>
							<ListRow
								leading={<HostStatusDot test={host.lastTest} />}
								title={
									<>
										<span className="min-w-0 truncate font-medium font-mono">
											{host.name}
										</span>
										{!usable && blocked === "automation" && (
											<Badge>{t("sshPanel.automationBlocked")}</Badge>
										)}
									</>
								}
								meta={
									<span className="min-w-0 truncate font-mono">
										{sshTarget(host)}
									</span>
								}
								trailing={
									<>
										<CopyButton
											text={`ssh ${host.name}`}
											ariaLabel={t("sshPanel.copyCommand", { name: host.name })}
										/>
										<IconButton
											icon="play"
											size={13}
											box="xs"
											disabled={test.isPending && test.variables === host.id}
											label={t("sshPanel.test", { name: host.name })}
											onClick={() => test.mutate(host.id)}
										/>
									</>
								}
							/>
						</li>
					))}
				</ul>
			)}
			<ManageLink />
		</Card>
	);
}

type RunnerScope = "user" | "project";

interface RunnerForm {
	name: string;
	host: string;
	username: string;
	port: string;
	keyPath: string;
	description: string;
	scope: RunnerScope;
}

const EMPTY_FORM: RunnerForm = {
	name: "",
	host: "",
	username: "",
	port: "",
	keyPath: "",
	description: "",
	scope: "user",
};

/** runner 那台机器的主机：新建或编辑（编辑时带上原名与原作用域，改名 / 换作用域由 omp 处理）。 */
function RunnerHostDialog({
	threadId,
	editing,
	onClose,
	onDone,
}: {
	threadId: string;
	editing?: RpcSshHostInfo;
	onClose: () => void;
	onDone: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const [form, setForm] = useState<RunnerForm>(
		editing
			? {
					name: editing.name,
					host: editing.host,
					username: editing.username ?? "",
					port: editing.port ? String(editing.port) : "",
					keyPath: editing.keyPath ?? "",
					description: editing.description ?? "",
					scope: editing.scope === "project" ? "project" : "user",
				}
			: EMPTY_FORM,
	);
	const save = useMutation({
		mutationFn: () => {
			const host: RpcSshHostInput = {
				host: form.host.trim(),
				...(form.username.trim() ? { username: form.username.trim() } : {}),
				...(Number(form.port) ? { port: Number(form.port) } : {}),
				...(form.keyPath.trim() ? { keyPath: form.keyPath.trim() } : {}),
				...(form.description.trim()
					? { description: form.description.trim() }
					: {}),
			};
			return realtime.cmd(threadId, {
				type: "ssh_manage",
				action: editing ? "update" : "create",
				scope: form.scope,
				name: form.name.trim(),
				host,
				...(editing
					? {
							previousName: editing.name,
							previousScope: editing.scope === "project" ? "project" : "user",
						}
					: {}),
			});
		},
		onSuccess: () => {
			onDone();
			onClose();
		},
	});
	const set =
		(k: keyof RunnerForm) =>
		(e: { target: { value: string } }): void =>
			setForm((f) => ({ ...f, [k]: e.target.value }));
	return (
		<Dialog
			open
			onClose={onClose}
			title={
				editing
					? t("sshPanel.runner.editTitle", { name: editing.name })
					: t("sshPanel.runner.addTitle")
			}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						type="submit"
						form="ssh-runner-form"
						disabled={!form.name.trim() || !form.host.trim() || save.isPending}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				id="ssh-runner-form"
				className="grid gap-3 sm:grid-cols-2"
				onSubmit={(e) => {
					e.preventDefault();
					save.mutate();
				}}
			>
				<Field label={t("sshPage.hostDialog.name")}>
					<TextInput
						value={form.name}
						onChange={set("name")}
						spellCheck={false}
						autoFocus
					/>
				</Field>
				<Field label={t("sshPanel.runner.host")}>
					<TextInput
						value={form.host}
						onChange={set("host")}
						spellCheck={false}
					/>
				</Field>
				<Field label={t("sshPage.hostDialog.username")}>
					<TextInput
						value={form.username}
						onChange={set("username")}
						spellCheck={false}
					/>
				</Field>
				<Field label={t("sshPage.hostDialog.port")}>
					<TextInput
						type="number"
						min={1}
						max={65535}
						value={form.port}
						onChange={set("port")}
						placeholder="22"
					/>
				</Field>
				<Field label={t("sshPanel.runner.keyPath")}>
					<TextInput
						value={form.keyPath}
						onChange={set("keyPath")}
						spellCheck={false}
						placeholder="~/.ssh/id_ed25519"
					/>
				</Field>
				<Field label={t("sshPanel.runner.scope")}>
					<Select value={form.scope} onChange={set("scope")}>
						<option value="user">user</option>
						<option value="project">project</option>
					</Select>
				</Field>
				<div className="sm:col-span-2">
					<Field label={t("sshPage.hostDialog.description")}>
						<TextInput value={form.description} onChange={set("description")} />
					</Field>
				</div>
				<div className="sm:col-span-2">
					<ErrorText error={save.error} />
				</div>
			</form>
		</Dialog>
	);
}

function RunnerSsh({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const queryable = useThread(threadId, agentQueryable);
	const { expanded, ...fold } = useExpanded();
	const queryKey = ["rpc", threadId, "ssh"];
	const hosts = useQuery({
		queryKey,
		enabled: queryable && expanded,
		staleTime: 60_000,
		retry: false,
		queryFn: () => realtime.cmd(threadId, { type: "get_ssh_hosts" }),
	});
	const refresh = () => void qc.invalidateQueries({ queryKey });
	const [results, setResults] = useState<Record<string, RpcSshTestResult>>({});
	const [dialog, setDialog] = useState<
		{ kind: "add" } | { kind: "edit"; host: RpcSshHostInfo } | null
	>(null);
	const test = useMutation({
		mutationFn: (h: RpcSshHostInfo) =>
			// 只带主机定义的字段（RpcSshHostInput），不带 scope / editable / os 等列表附加项
			realtime.cmd(threadId, {
				type: "ssh_test",
				host: {
					name: h.name,
					host: h.host,
					username: h.username,
					port: h.port,
					keyPath: h.keyPath,
					description: h.description,
					compat: h.compat,
				},
			}),
		onSuccess: (r) => setResults((s) => ({ ...s, [r.name]: r })),
	});
	const remove = useMutation({
		mutationFn: (h: RpcSshHostInfo) =>
			realtime.cmd(threadId, {
				type: "ssh_manage",
				action: "delete",
				scope: h.scope === "project" ? "project" : "user",
				name: h.name,
			}),
		onSuccess: refresh,
	});
	if (!queryable) return null;
	const data = hosts.data;
	const list = data?.hosts ?? [];
	return (
		<Card
			title={t("panel.ssh")}
			icon="hardDrives"
			{...fold}
			summary={
				data && (
					<span className="truncate text-faint">
						{t("sshPanel.summary", { count: list.length })}
					</span>
				)
			}
			action={
				expanded && (
					<IconButton
						icon="plus"
						size={13}
						box="xs"
						className="-mr-1.5"
						label={t("sshPanel.runner.add")}
						onClick={() => setDialog({ kind: "add" })}
					/>
				)
			}
		>
			<p className="mb-2 text-faint text-xs">{t("sshPanel.runner.hint")}</p>
			{hosts.isPending && hosts.fetchStatus !== "idle" && <Spinner size={12} />}
			<ErrorText error={hosts.error ?? test.error ?? remove.error} />
			{data && !data.openSshAvailable && (
				<Callout tone="warn">{t("sshPanel.runner.noSsh")}</Callout>
			)}
			{data?.warnings.map((w) => (
				<Callout key={w} tone="warn">
					{w}
				</Callout>
			))}
			{data && list.length === 0 && (
				<p className="text-faint text-xs">{t("sshPanel.runner.empty")}</p>
			)}
			{list.length > 0 && (
				<ul className="-mx-1.5 flex flex-col gap-px" data-testid="ssh-runner">
					{list.map((h) => {
						const r = results[h.name];
						return (
							<li key={`${h.scope}:${h.name}`}>
								<ListRow
									leading={
										r ? (
											<span
												title={r.error ?? r.checkedAt}
												className="inline-flex"
											>
												<StatusDot
													tone={r.ok ? "ok" : "err"}
													label={r.ok ? t("sshPage.test.ok") : (r.error ?? "")}
												/>
											</span>
										) : (
											<StatusDot tone="neutral" />
										)
									}
									title={
										<>
											<span className="min-w-0 truncate font-medium font-mono">
												{h.name}
											</span>
											<Badge>{h.scope}</Badge>
											{(r?.os ?? h.os) && <Badge>{r?.os ?? h.os}</Badge>}
										</>
									}
									meta={
										<span className="min-w-0 truncate font-mono">
											{h.username ? `${h.username}@` : ""}
											{h.host}
											{h.port ? `:${h.port}` : ""}
										</span>
									}
									trailing={
										<>
											<IconButton
												icon="play"
												size={13}
												box="xs"
												disabled={test.isPending && test.variables === h}
												label={t("sshPanel.test", { name: h.name })}
												onClick={() => test.mutate(h)}
											/>
											{h.editable && (
												<>
													<IconButton
														icon="edit"
														size={13}
														box="xs"
														label={t("sshPanel.runner.edit", { name: h.name })}
														onClick={() => setDialog({ kind: "edit", host: h })}
													/>
													<IconButton
														icon="trash"
														size={13}
														box="xs"
														disabled={remove.isPending}
														label={t("sshPanel.runner.delete", {
															name: h.name,
														})}
														onClick={() => remove.mutate(h)}
													/>
												</>
											)}
										</>
									}
								/>
							</li>
						);
					})}
				</ul>
			)}
			{dialog && (
				<RunnerHostDialog
					threadId={threadId}
					editing={dialog.kind === "edit" ? dialog.host : undefined}
					onClose={() => setDialog(null)}
					onDone={refresh}
				/>
			)}
		</Card>
	);
}

export function SshSection({ threadId }: { threadId: string }): ReactNode {
	const work = useThread(
		threadId,
		(s) => (s.thread?.kind ?? "work") === "work",
	);
	const orb = useOrbOf(threadId);
	if (!work) return null;
	return orb?.kind === "runner" ? (
		<RunnerSsh threadId={threadId} />
	) : (
		<ManagedSsh threadId={threadId} />
	);
}
