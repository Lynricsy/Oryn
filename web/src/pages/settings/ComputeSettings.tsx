/**
 * 设置 → 算力：Provider 与 Runner 同构——「新建」（分组右上）→ 对话框里填名字 → 同一对话框一次性显示令牌 → 列表。
 * - Provider：托管 orb 的算力节点；行 = 名字、驱动、能力（冻结 / 快照）、各 orb 类型规格 · 在线状态 · ⋯（删除）。
 *   分组右上另有「查看总览」进 /providers。
 * - Runner（M11b）：用户自己的机器以 `orbd run --runner --dir <目录>` 主动接入；行 = 名字、主机与系统、声明的目录、特性、
 *   orbd 版本 · 在线状态 · ⋯（删除）。令牌库里只存哈希，删除即吊销。
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type Provider, unwrap } from "../../api/client";
import {
	qk,
	type Runner,
	useCreateRunner,
	useDeleteRunner,
	useProviders,
	useRunnerOnline,
	useRunners,
} from "../../api/queries";
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge, Callout } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { DockerBadge, typeHasDocker } from "../../components/ui/OrbBadges";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupRow,
	StatusCell,
	StatusText,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import {
	formatOrbCpus,
	formatOrbMemory,
	formatRelative,
} from "../../lib/format";

/** 新 runner 的完整启动命令（在那台机器上以普通用户运行；`--dir` 可重复）。 */
export function runnerCommand(origin: string, token: string): string {
	return `ORYN_URL=${origin} ORYN_RUNNER_TOKEN=${token} orbd run --runner --dir <目录>`;
}

// ---------------------------------------------------------------- 新建 → 一次性令牌

interface Created {
	/** 令牌提示的标题（带名字） */
	title: string;
	/** 逐项显示、各带复制钮：令牌，以及 runner 的完整启动命令 */
	secrets: readonly { label: string; text: string }[];
	/** 令牌下方的用法说明 */
	note: string;
}

/** 一次性令牌：必须看到的提示，所以用警示 Callout；每项一段等宽文本 + 复制。 */
function OneTimeSecrets({ created }: { created: Created }): ReactNode {
	return (
		<div data-testid="compute-token">
			<Callout tone="warn" title={created.title}>
				<div className="mt-2 flex flex-col gap-2.5">
					{created.secrets.map((s) => (
						<div key={s.label} className="flex flex-col gap-1">
							<span className="text-muted text-xs">{s.label}</span>
							<div className="flex items-start gap-2">
								<code className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
									{s.text}
								</code>
								<CopyButton text={s.text} ariaLabel={`${s.label}：${s.text}`} />
							</div>
						</div>
					))}
					<p className="text-muted text-xs">{created.note}</p>
				</div>
			</Callout>
		</div>
	);
}

/** 新建对话框：先填名字，创建成功后原地换成一次性令牌，关掉就再也看不到。 */
function CreateDialog({
	title,
	intro,
	placeholder,
	create,
	onClose,
}: {
	title: string;
	intro?: string;
	placeholder: string;
	create: (name: string) => Promise<Created>;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const formId = useId();
	const [name, setName] = useState("");
	const [created, setCreated] = useState<Created | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const submit = async () => {
		const n = name.trim();
		if (!n || busy) return;
		setBusy(true);
		setError(null);
		try {
			setCreated(await create(n));
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={title}
			footer={
				created ? (
					<Button variant="primary" onClick={onClose}>
						{t("compute.done")}
					</Button>
				) : (
					<>
						<Button variant="ghost" onClick={onClose}>
							{t("common.cancel")}
						</Button>
						<Button
							type="submit"
							form={formId}
							variant="primary"
							disabled={busy || !name.trim()}
						>
							{busy && <Spinner size={13} />}
							{t("common.create")}
						</Button>
					</>
				)
			}
		>
			{created ? (
				<OneTimeSecrets created={created} />
			) : (
				<form
					id={formId}
					className="flex flex-col gap-4"
					onSubmit={(e) => {
						e.preventDefault();
						void submit();
					}}
				>
					{intro && <p className="text-[13px] text-muted">{intro}</p>}
					<FormField label={t("compute.name")}>
						{(p) => (
							<TextInput
								{...p}
								autoFocus
								autoComplete="off"
								spellCheck={false}
								placeholder={placeholder}
								value={name}
								onChange={(e) => setName(e.target.value)}
							/>
						)}
					</FormField>
					<ErrorText error={error} />
				</form>
			)}
		</Dialog>
	);
}

/** 行尾 ⋯：目前只有删除。 */
function RowMenu({
	name,
	disabled,
	onDelete,
}: {
	name: string;
	disabled: boolean;
	onDelete: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Menu
			trigger={(p) => (
				<IconButton
					icon="more"
					box="sm"
					label={t("compute.more", { name })}
					{...p}
				/>
			)}
			items={[
				{
					key: "delete",
					icon: "trash",
					label: t("common.delete"),
					danger: true,
					disabled,
					onSelect: onDelete,
				},
			]}
		/>
	);
}

function OnlineStatus({ online }: { online: boolean }): ReactNode {
	const { t } = useTranslation();
	return (
		<StatusCell>
			<StatusText tone={online ? "ok" : "neutral"}>
				{online ? t("compute.online") : t("compute.offline")}
			</StatusText>
		</StatusCell>
	);
}

// ---------------------------------------------------------------- Provider

/** provider 能力：支持为绿底勾、不支持为中性叉；读屏另读「支持 / 不支持」。 */
function Capability({ on, label }: { on: boolean; label: string }) {
	const { t } = useTranslation();
	const state = on ? t("providers.supported") : t("providers.unsupported");
	return (
		<Badge
			tone={on ? "ok" : "neutral"}
			icon={on ? "check" : "close"}
			title={state}
		>
			{label}
			<span className="sr-only">{`：${state}`}</span>
		</Badge>
	);
}

function ProviderRow({ provider: p }: { provider: Provider }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const del = useMutation({
		mutationFn: () =>
			unwrap(
				api.DELETE("/api/v1/providers/{id}", {
					params: { path: { id: p.id } },
				}),
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: qk.providers }),
	});
	return (
		<GroupRow
			icon="box"
			title={
				<>
					<span className="truncate">{p.name}</span>
					{p.driver && (
						<span className="shrink-0 font-medium text-faint text-xs">
							{p.driver}
						</span>
					)}
				</>
			}
			meta={
				p.capabilities && (
					<>
						<Capability
							on={p.capabilities.freeze}
							label={t("providers.freeze")}
						/>
						<Capability
							on={p.capabilities.snapshot}
							label={t("providers.snapshot")}
						/>
					</>
				)
			}
			trailing={
				<>
					<OnlineStatus online={p.online} />
					<RowMenu
						name={p.name}
						disabled={del.isPending}
						onDelete={async () => {
							if (
								await confirmAction({
									title: t("providers.deleteConfirm", { name: p.name }),
									action: t("common.delete"),
									danger: true,
								})
							)
								del.mutate();
						}}
					/>
				</>
			}
		>
			{(p.orbTypes.length > 0 || del.error) && (
				<div className="flex flex-col gap-1.5">
					{/* 每个 orb 类型一组：名称 + 规格；「默认」只是标注，用中性色 */}
					{p.orbTypes.length > 0 && (
						<div className="flex flex-wrap gap-2 text-xs">
							{p.orbTypes.map((ot) => (
								<span
									key={ot.name}
									className="inline-flex h-6 items-center gap-2 rounded-md bg-inset px-2 text-muted"
								>
									<span className="font-medium text-fg">{ot.name}</span>
									<span className="inline-flex items-center gap-1 tabular-nums">
										<Icon name="cpu" size={11} aria-hidden />
										{formatOrbCpus(ot.cpus)}
									</span>
									<span className="inline-flex items-center gap-1 tabular-nums">
										<Icon name="memory" size={11} aria-hidden />
										{formatOrbMemory(ot.memoryMB)}
									</span>
									{p.defaultType === ot.name && (
										<Badge>{t("providers.default")}</Badge>
									)}
									{typeHasDocker(ot) && <DockerBadge />}
								</span>
							))}
						</div>
					)}
					<ErrorText error={del.error} />
				</div>
			)}
		</GroupRow>
	);
}

function ProvidersGroup(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const providers = useProviders();
	const [creating, setCreating] = useState(false);
	const list = providers.data?.providers ?? [];
	const create = async (name: string): Promise<Created> => {
		const res = await unwrap(api.POST("/api/v1/providers", { body: { name } }));
		await qc.invalidateQueries({ queryKey: qk.providers });
		return {
			title: t("providers.tokenOnce", { name: res.provider.name }),
			secrets: [{ label: t("compute.token"), text: res.token }],
			note: t("providers.tokenHint"),
		};
	};
	return (
		<Group
			title={t("providers.title")}
			action={
				<>
					<Link
						to="/providers"
						className="inline-flex h-7 items-center gap-1 rounded-[10px] px-2 font-medium text-muted text-xs transition-colors hover:bg-hover hover:text-fg"
					>
						<Icon name="gauge" size={13} aria-hidden />
						{t("providers.openOverview")}
					</Link>
					<Button size="sm" icon="plus" onClick={() => setCreating(true)}>
						{t("compute.new")}
					</Button>
				</>
			}
		>
			{providers.isPending && <Spinner />}
			<ErrorText error={providers.error} />
			{providers.isSuccess && list.length === 0 && (
				<p className="px-1.5 text-faint text-sm">{t("providers.empty")}</p>
			)}
			{list.length > 0 && (
				<GroupList>
					{list.map((p) => (
						<ProviderRow key={p.id} provider={p} />
					))}
				</GroupList>
			)}
			{creating && (
				<CreateDialog
					title={t("providers.create")}
					placeholder={t("providers.namePlaceholder")}
					create={create}
					onClose={() => setCreating(false)}
				/>
			)}
		</Group>
	);
}

// ---------------------------------------------------------------- Runner

function RunnerRow({ runner }: { runner: Runner }): ReactNode {
	const { t } = useTranslation();
	const online = useRunnerOnline(runner);
	const del = useDeleteRunner();
	const system = [runner.os, runner.arch].filter(Boolean).join("/");
	return (
		<GroupRow
			icon="laptop"
			title={<span className="truncate">{runner.name}</span>}
			meta={
				<>
					{runner.hostname ? (
						<span className="inline-flex items-center gap-1">
							{runner.hostname}
							{system && <span className="text-faint">· {system}</span>}
						</span>
					) : (
						<span>{t("runner.neverConnected")}</span>
					)}
					{runner.orbdVersion && <span>· orbd {runner.orbdVersion}</span>}
					{!online && runner.lastSeenAt && (
						<span>
							·{" "}
							{t("runner.lastSeen", {
								when: formatRelative(runner.lastSeenAt),
							})}
						</span>
					)}
				</>
			}
			trailing={
				<>
					<OnlineStatus online={online} />
					<RowMenu
						name={runner.name}
						disabled={del.isPending}
						onDelete={async () => {
							if (
								await confirmAction({
									title: t("runner.deleteConfirm", { name: runner.name }),
									body: t("runner.deleteConfirmBody"),
									action: t("common.delete"),
									danger: true,
								})
							)
								del.mutate(runner.id);
						}}
					/>
				</>
			}
		>
			{(runner.dirs.length > 0 || runner.features.length > 0 || del.error) && (
				<div className="flex flex-col gap-1.5" data-testid="runner-details">
					{runner.dirs.length > 0 && (
						<div className="flex flex-wrap gap-1.5 text-xs">
							{runner.dirs.map((d) => (
								<span
									key={d.path}
									title={d.git ? t("runner.gitRepo") : undefined}
									className="inline-flex h-6 min-w-0 max-w-full items-center gap-1.5 rounded-sm bg-inset px-2 font-mono text-muted"
								>
									<Icon
										name={d.git ? "branch" : "folder"}
										size={11}
										aria-hidden
										className="shrink-0"
									/>
									<span className="min-w-0 truncate">{d.path}</span>
								</span>
							))}
						</div>
					)}
					{runner.features.length > 0 && (
						<div className="flex flex-wrap gap-1">
							{runner.features.map((f) => (
								<Badge key={f}>{f}</Badge>
							))}
						</div>
					)}
					<ErrorText error={del.error} />
				</div>
			)}
		</GroupRow>
	);
}

function RunnersGroup(): ReactNode {
	const { t } = useTranslation();
	const runners = useRunners();
	const createRunner = useCreateRunner();
	const [creating, setCreating] = useState(false);
	const list = runners.data?.runners ?? [];
	const create = async (name: string): Promise<Created> => {
		const res = await createRunner.mutateAsync(name);
		return {
			title: t("runner.tokenOnce", { name: res.runner.name }),
			secrets: [
				{ label: t("compute.token"), text: res.token },
				{
					label: t("runner.command"),
					text: runnerCommand(window.location.origin, res.token),
				},
			],
			note: t("runner.commandHint"),
		};
	};
	return (
		<Group
			title={t("runner.title")}
			action={
				<Button size="sm" icon="plus" onClick={() => setCreating(true)}>
					{t("compute.new")}
				</Button>
			}
		>
			{runners.isPending && <Spinner />}
			<ErrorText error={runners.error} />
			{runners.isSuccess && list.length === 0 && (
				<p className="px-1.5 text-faint text-sm">{t("runner.emptyHint")}</p>
			)}
			{list.length > 0 && (
				<GroupList>
					{list.map((r) => (
						<RunnerRow key={r.id} runner={r} />
					))}
				</GroupList>
			)}
			{creating && (
				<CreateDialog
					title={t("runner.create")}
					intro={t("runner.intro")}
					placeholder={t("runner.namePlaceholder")}
					create={create}
					onClose={() => setCreating(false)}
				/>
			)}
		</Group>
	);
}

export default function ComputeSettings(): ReactNode {
	return (
		<>
			<ProvidersGroup />
			<RunnersGroup />
		</>
	);
}
