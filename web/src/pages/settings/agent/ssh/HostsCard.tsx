/**
 * SSH 主机分组：不传 `projectId` = 全局主机（设置页），传了 = 该项目的主机（项目页复用，标题与说明由调用方给）。
 * 每行：主机名 + 徽标（停用、全自动可用、端口转发、标签）· `用户@地址:端口`（经跳板时接跳板名）与描述 ·
 * 「测试」（悬停出现）· 最近一次测试的状态（定宽状态列，悬停看时间与错误）· ⋯（编辑、终端、文件、重置指纹、启用 / 停用、删除）。
 * 测试结果与操作错误在行下展开。新建成功后自动跑一次测试。删除先确认，服务端拒绝（如被当作跳板）的原因显示在行下。
 * 终端与文件浏览在大对话框里打开（终端懒加载 xterm）。
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
import { Badge } from "../../../../components/ui/data";
import { Icon } from "../../../../components/ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Spinner,
} from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	Reveal,
	StatusCell,
	StatusText,
} from "../../../../components/ui/workbench";
import { confirmAction } from "../../../../lib/confirm";
import { sshHostFileSource } from "../../../../lib/file-source";
import { formatDateTime } from "../../../../lib/format";
import { HostDialog } from "./HostDialog";
import { TestResultView } from "./TestResultView";

const HostTerminal = lazy(
	() => import("../../../../components/ssh/HostTerminal"),
);

type HostAction =
	| { kind: "create" }
	| { kind: "edit"; host: SshHost }
	| { kind: "terminal"; host: SshHost }
	| { kind: "files"; host: SshHost };

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

/** 最近一次测试：短状态 + 悬停给时间与错误。 */
function LastTest({ test }: { test: SshHost["lastTest"] }): ReactNode {
	const { t } = useTranslation();
	const detail = !test
		? t("sshPage.status.untested")
		: test.ok
			? t("sshPage.status.ok", { at: formatDateTime(test.at) })
			: t("sshPage.status.failed", {
					at: formatDateTime(test.at),
					error: test.error ?? "",
				});
	return (
		<span title={detail} className="flex min-w-0">
			<StatusText tone={!test ? "neutral" : test.ok ? "ok" : "err"}>
				{!test
					? t("sshPage.status.short.untested")
					: test.ok
						? t("sshPage.status.short.ok")
						: t("sshPage.status.short.failed")}
			</StatusText>
		</span>
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
	const del = useDeleteSshHost();
	const error = update.error ?? reset.error ?? del.error;
	return (
		<GroupRow
			icon="hardDrives"
			title={
				<span
					data-testid="ssh-host-row"
					className="flex min-w-0 items-center gap-1.5"
				>
					<span className="max-w-full shrink-0 truncate font-mono">
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
				</span>
			}
			meta={
				<>
					<span className="font-mono text-xs">{sshTarget(host)}</span>
					{jumpName && <span>{t("sshPage.row.via", { name: jumpName })}</span>}
					{host.description && (
						<span className="min-w-0 truncate">{host.description}</span>
					)}
				</>
			}
			trailing={
				<>
					<Reveal>
						<Button size="sm" disabled={testing} onClick={onTest}>
							{testing && <Spinner size={12} />}
							{t("sshPage.row.test")}
						</Button>
					</Reveal>
					<StatusCell>
						<LastTest test={host.lastTest} />
					</StatusCell>
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
								disabled: del.isPending,
								onSelect: async () => {
									if (
										await confirmAction({
											title: t("sshPage.hosts.deleteTitle", {
												name: host.name,
											}),
											body: t("sshPage.hosts.deleteHint"),
											action: t("common.delete"),
											danger: true,
										})
									)
										del.mutate(host.id);
								},
							},
						]}
						trigger={(p) => (
							<IconButton
								{...p}
								icon="more"
								box="sm"
								label={t("sshPage.row.actions", { name: host.name })}
							/>
						)}
					/>
				</>
			}
		>
			{(error || result) && (
				<div className="flex flex-col gap-2">
					<ErrorText error={error} />
					{result && <TestResultView result={result} onClose={onCloseResult} />}
				</div>
			)}
		</GroupRow>
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
	/** 分组下方的说明 */
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
		<div data-testid="ssh-hosts" className="contents">
			<Group
				title={title}
				action={
					<Button
						size="sm"
						icon="plus"
						onClick={() => setAction({ kind: "create" })}
					>
						{t("sshPage.hosts.add")}
					</Button>
				}
			>
				{tags.length > 0 && (
					// 标签是筛选按钮：前置漏斗图标，免得一排 ghost 按钮读起来像普通文字
					<div className="flex flex-wrap items-center gap-1 px-1">
						<Icon name="funnel" size={13} aria-hidden className="text-faint" />
						{tags.map((tag) => (
							<Button
								key={tag}
								size="sm"
								variant={tagFilter === tag ? "secondary" : "ghost"}
								aria-pressed={tagFilter === tag}
								onClick={() =>
									setTagFilter((cur) => (cur === tag ? null : tag))
								}
							>
								{tag}
							</Button>
						))}
					</div>
				)}
				{hosts.isPending && <Spinner size={12} />}
				<ErrorText error={hosts.error ?? test.error} />
				{hosts.isSuccess && mine.length === 0 && (
					<p className="px-1.5 text-faint text-sm">
						{t("sshPage.hosts.empty")}
					</p>
				)}
				{shown.length > 0 && (
					<GroupList>
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
					</GroupList>
				)}
				{intro && <p className="px-1.5 text-faint text-xs">{intro}</p>}
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
			</Group>
		</div>
	);
}
