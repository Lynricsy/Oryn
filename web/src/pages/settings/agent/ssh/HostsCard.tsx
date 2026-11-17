/**
 * SSH 主机列表卡片：不传 `projectId` = 全局主机（设置页），传了 = 该项目的主机（项目页复用，标题与说明由调用方给）。
 * 每行：主机名 + 徽标（停用、全自动可用、端口转发、标签）、`用户@地址:端口`（经跳板时接跳板名）与描述、最近一次测试的状态点；
 * 尾部「测试」与菜单（编辑、终端、文件、重置指纹、启用 / 停用、删除）。测试结果在行下展开。
 * 新建成功后自动跑一次测试。终端与文件浏览在大对话框里打开（终端懒加载 xterm）。
 */
import { lazy, type ReactNode, Suspense, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type SshHost,
	type SshTestResult,
	sshTarget,
	useDeleteSshHost,
	useResetSshHostKey,
	useSshHosts,
	useTestSshHost,
	useUpdateSshHost,
} from "../../../../api/ssh-queries";
import { FileBrowser } from "../../../../components/files/FileBrowser";
import { HostStatusDot } from "../../../../components/ssh/HostStatus";
import { Badge, Card, ListRow } from "../../../../components/ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Spinner,
} from "../../../../components/ui/primitives";
import { sshHostFileSource } from "../../../../lib/file-source";
import { HostDialog } from "./HostDialog";
import { TestResultView } from "./TestResultView";

const HostTerminal = lazy(
	() => import("../../../../components/ssh/HostTerminal"),
);

type HostAction =
	| { kind: "create" }
	| { kind: "edit"; host: SshHost }
	| { kind: "delete"; host: SshHost }
	| { kind: "terminal"; host: SshHost }
	| { kind: "files"; host: SshHost };

function DeleteHostDialog({
	host,
	onClose,
}: {
	host: SshHost;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteSshHost();
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("sshPage.hosts.deleteTitle", { name: host.name })}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="danger"
						disabled={del.isPending}
						onClick={() => del.mutate(host.id, { onSuccess: onClose })}
					>
						{t("common.delete")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-2">
				<p className="text-muted text-sm">{t("sshPage.hosts.deleteHint")}</p>
				<ErrorText error={del.error} />
			</div>
		</Dialog>
	);
}

function FilesDialog({
	host,
	onClose,
}: {
	host: SshHost;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const sources = useMemo(() => [sshHostFileSource(host.id)], [host.id]);
	return (
		<Dialog
			open
			size="xl"
			onClose={onClose}
			title={t("sshPage.hosts.filesTitle", { name: host.name })}
		>
			<div className="h-[32rem] overflow-hidden rounded-lg border border-border">
				<FileBrowser sources={sources} storageKey={`ssh.${host.id}`} />
			</div>
		</Dialog>
	);
}

function HostRow({
	host,
	jumpName,
	result,
	onTest,
	testing,
	onCloseResult,
	onAction,
}: {
	host: SshHost;
	jumpName?: string;
	result?: SshTestResult;
	onTest: () => void;
	testing: boolean;
	onCloseResult: () => void;
	onAction: (a: HostAction) => void;
}): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateSshHost();
	const reset = useResetSshHostKey();
	return (
		<li
			data-testid="ssh-host-row"
			className={host.enabled ? undefined : "opacity-60"}
		>
			<ListRow
				leading={<HostStatusDot test={host.lastTest} />}
				title={
					<>
						<span className="max-w-full shrink-0 truncate font-medium font-mono">
							{host.name}
						</span>
						{/* 窄屏时裁掉徽标，不裁主机名 */}
						<span className="flex min-w-0 gap-1.5 overflow-hidden">
							{!host.enabled && <Badge>{t("sshPage.row.disabled")}</Badge>}
							{host.allowAutomation && (
								<Badge icon="robot">{t("sshPage.row.automation")}</Badge>
							)}
							{host.allowForward && (
								<Badge icon="link">{t("sshPage.row.forward")}</Badge>
							)}
							{host.tags.map((tag) => (
								<Badge key={tag}>{tag}</Badge>
							))}
						</span>
					</>
				}
				meta={
					<>
						<span className="font-mono">{sshTarget(host)}</span>
						{jumpName && (
							<span>{t("sshPage.row.via", { name: jumpName })}</span>
						)}
						{host.description && (
							<span className="min-w-0 truncate">{host.description}</span>
						)}
					</>
				}
				trailing={
					<>
						<Button
							size="sm"
							variant="ghost"
							disabled={testing}
							onClick={onTest}
						>
							{testing && <Spinner size={12} />}
							{t("sshPage.row.test")}
						</Button>
						<Menu
							items={[
								{
									key: "edit",
									icon: "edit",
									label: t("common.edit"),
									onSelect: () => onAction({ kind: "edit", host }),
								},
								{
									key: "terminal",
									icon: "terminal",
									label: t("sshPage.row.terminal"),
									disabled: !host.enabled,
									onSelect: () => onAction({ kind: "terminal", host }),
								},
								{
									key: "files",
									icon: "folder",
									label: t("sshPage.row.files"),
									disabled: !host.enabled,
									onSelect: () => onAction({ kind: "files", host }),
								},
								{
									key: "reset",
									icon: "key",
									label: t("sshPage.row.resetHostKey"),
									disabled: !host.hostKey,
									title: host.hostKey
										? `${host.hostKey.type} ${host.hostKey.fingerprint}`
										: t("sshPage.row.noHostKey"),
									onSelect: () => reset.mutate(host.id),
								},
								{
									key: "toggle",
									icon: host.enabled ? "pause" : "play",
									label: host.enabled
										? t("sshPage.row.disable")
										: t("sshPage.row.enable"),
									onSelect: () =>
										update.mutate({
											id: host.id,
											patch: { enabled: !host.enabled },
										}),
								},
								{
									key: "delete",
									icon: "trash",
									danger: true,
									label: t("common.delete"),
									onSelect: () => onAction({ kind: "delete", host }),
								},
							]}
							trigger={(p) => (
								<IconButton
									{...p}
									icon="more"
									box="sm"
									size={14}
									label={t("sshPage.row.actions", { name: host.name })}
								/>
							)}
						/>
					</>
				}
			/>
			<div className="px-2">
				<ErrorText error={update.error ?? reset.error} />
			</div>
			{result && (
				<div className="px-2 pb-2">
					<TestResultView result={result} onClose={onCloseResult} />
				</div>
			)}
		</li>
	);
}

export function HostsCard({
	projectId,
	title,
	intro,
}: {
	/** 缺省 = 全局主机 */
	projectId?: string;
	title: ReactNode;
	intro?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const hosts = useSshHosts();
	const test = useTestSshHost();
	const [action, setAction] = useState<HostAction | null>(null);
	const [results, setResults] = useState<Record<string, SshTestResult>>({});
	const [testing, setTesting] = useState<Record<string, boolean>>({});
	const [tagFilter, setTagFilter] = useState<string | null>(null);
	const all = hosts.data?.hosts ?? [];
	const mine = all.filter((h) => h.projectId === projectId);
	const names = new Map(all.map((h) => [h.id, h.name]));
	const tags = [...new Set(mine.flatMap((h) => h.tags))].sort();
	const shown =
		tagFilter && tags.includes(tagFilter)
			? mine.filter((h) => h.tags.includes(tagFilter))
			: mine;
	const runTest = (id: string) => {
		setTesting((s) => ({ ...s, [id]: true }));
		test.mutate(id, {
			onSuccess: (r) => setResults((s) => ({ ...s, [id]: r })),
			onSettled: () =>
				setTesting((s) => {
					const { [id]: _, ...rest } = s;
					return rest;
				}),
		});
	};
	const close = () => setAction(null);
	return (
		<Card
			title={title}
			icon="hardDrives"
			action={
				<Button
					size="sm"
					variant="ghost"
					icon="plus"
					onClick={() => setAction({ kind: "create" })}
				>
					{t("sshPage.hosts.add")}
				</Button>
			}
		>
			{intro && <p className="mb-2 text-faint text-xs">{intro}</p>}
			{tags.length > 0 && (
				<div className="mb-2 flex flex-wrap gap-1">
					{tags.map((tag) => (
						<Button
							key={tag}
							size="sm"
							variant={tagFilter === tag ? "secondary" : "ghost"}
							aria-pressed={tagFilter === tag}
							onClick={() => setTagFilter((cur) => (cur === tag ? null : tag))}
						>
							{tag}
						</Button>
					))}
				</div>
			)}
			{hosts.isPending && <Spinner size={12} />}
			<ErrorText error={hosts.error ?? test.error} />
			{hosts.isSuccess && mine.length === 0 && (
				<p className="text-faint text-xs">{t("sshPage.hosts.empty")}</p>
			)}
			{shown.length > 0 && (
				<ul className="-mx-2 flex flex-col" data-testid="ssh-hosts">
					{shown.map((h) => (
						<HostRow
							key={h.id}
							host={h}
							jumpName={h.jumpHostId ? names.get(h.jumpHostId) : undefined}
							result={results[h.id]}
							testing={!!testing[h.id]}
							onTest={() => runTest(h.id)}
							onCloseResult={() =>
								setResults((s) => {
									const { [h.id]: _, ...rest } = s;
									return rest;
								})
							}
							onAction={setAction}
						/>
					))}
				</ul>
			)}
			{(action?.kind === "create" || action?.kind === "edit") && (
				<HostDialog
					host={action.kind === "edit" ? action.host : undefined}
					projectId={projectId}
					hosts={all}
					onClose={close}
					onSaved={(h, created) => {
						close();
						if (created) runTest(h.id);
					}}
				/>
			)}
			{action?.kind === "delete" && (
				<DeleteHostDialog host={action.host} onClose={close} />
			)}
			{action?.kind === "files" && (
				<FilesDialog host={action.host} onClose={close} />
			)}
			{action?.kind === "terminal" && (
				<Dialog
					open
					size="xl"
					onClose={close}
					title={t("sshPage.terminal.title", { name: action.host.name })}
				>
					<Suspense fallback={<Spinner />}>
						<HostTerminal host={action.host} />
					</Suspense>
				</Dialog>
			)}
		</Card>
	);
}
