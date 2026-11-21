/**
 * 设置页「Runners」（M11b，default export，由设置页接入）：顶部新建（名字）→ 一次性显示令牌与完整启动命令；
 * 列表每行 = 在线状态、名字、主机名与系统、声明的目录（git 仓库带分支图标）、特性、orbd 版本、最近在线；删除带确认。
 * runner 是用户自己的机器以 `orbd run --runner --dir <目录>` 主动接入的执行位置；令牌库里只存哈希，删除即吊销。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type Runner,
	useCreateRunner,
	useDeleteRunner,
	useRunnerOnline,
	useRunners,
} from "../../api/queries";
import { CopyButton } from "../../components/ui/CopyButton";
import {
	Badge,
	Callout,
	Card,
	ListRow,
	StatusDot,
} from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";

/** 新 runner 的完整启动命令（在那台机器上以普通用户运行；`--dir` 可重复）。 */
export function runnerCommand(origin: string, token: string): string {
	return `ORYN_URL=${origin} ORYN_RUNNER_TOKEN=${token} orbd run --runner --dir <目录>`;
}

function Secret({ label, text }: { label: string; text: string }): ReactNode {
	return (
		<div className="flex flex-col gap-1">
			<span className="text-muted text-xs">{label}</span>
			<div className="flex items-start gap-2">
				<code className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
					{text}
				</code>
				<CopyButton text={text} ariaLabel={`${label}：${text}`} />
			</div>
		</div>
	);
}

function RunnerRow({ runner }: { runner: Runner }): ReactNode {
	const { t } = useTranslation();
	const online = useRunnerOnline(runner);
	const del = useDeleteRunner();
	const system = [runner.os, runner.arch].filter(Boolean).join("/");
	return (
		<li data-testid="runner-settings-row">
			<ListRow
				leading={
					<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
						<Icon name="laptop" size={15} />
					</span>
				}
				title={
					<>
						<span className="truncate font-medium">{runner.name}</span>
						<span className="inline-flex items-center gap-1.5 text-muted text-xs">
							<StatusDot tone={online ? "ok" : "neutral"} />
							{online ? t("runner.online") : t("runner.offline")}
						</span>
					</>
				}
				meta={
					<>
						{runner.hostname ? (
							<span className="inline-flex items-center gap-1">
								<Icon name="monitor" size={11} aria-hidden />
								{runner.hostname}
								{system && <span className="text-faint">· {system}</span>}
							</span>
						) : (
							<span>{t("runner.neverConnected")}</span>
						)}
						{runner.orbdVersion && <span>orbd {runner.orbdVersion}</span>}
						{!online && runner.lastSeenAt && (
							<span>
								{t("runner.lastSeen", {
									when: formatRelative(runner.lastSeenAt),
								})}
							</span>
						)}
						{runner.dirs.length > 0 && (
							<span className="mt-1 flex basis-full flex-wrap gap-1.5">
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
							</span>
						)}
						{runner.features.length > 0 && (
							<span className="flex basis-full flex-wrap gap-1">
								{runner.features.map((f) => (
									<Badge key={f}>{f}</Badge>
								))}
							</span>
						)}
						<ErrorText error={del.error} />
					</>
				}
				trailing={
					<IconButton
						icon="trash"
						label={t("runner.delete", { name: runner.name })}
						disabled={del.isPending}
						onClick={async () => {
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
				}
			/>
		</li>
	);
}

export default function RunnersSettings(): ReactNode {
	const { t } = useTranslation();
	const runners = useRunners();
	const create = useCreateRunner();
	const [name, setName] = useState("");
	const [created, setCreated] = useState<{
		name: string;
		token: string;
	} | null>(null);
	const origin = window.location.origin;
	const list = runners.data?.runners ?? [];
	return (
		<>
			<Card title={t("runner.create")} icon="plus">
				<p className="mb-2.5 text-muted text-xs">{t("runner.intro")}</p>
				<form
					className="flex flex-wrap gap-2"
					onSubmit={(e) => {
						e.preventDefault();
						const n = name.trim();
						if (!n) return;
						create.mutate(n, {
							onSuccess: (res) => {
								setCreated({ name: res.runner.name, token: res.token });
								setName("");
							},
						});
					}}
				>
					<TextInput
						className="max-w-60"
						aria-label={t("runner.name")}
						placeholder={t("runner.namePlaceholder")}
						value={name}
						onChange={(e) => setName(e.target.value)}
					/>
					<Button
						type="submit"
						variant="primary"
						disabled={create.isPending || !name.trim()}
					>
						{create.isPending && <Spinner size={13} />}
						{t("common.create")}
					</Button>
				</form>
				<ErrorText error={create.error} />
				{created && (
					<div className="mt-3" data-testid="runner-created">
						<Callout
							tone="warn"
							title={t("runner.tokenOnce", { name: created.name })}
						>
							<div className="mt-2 flex flex-col gap-2.5">
								<Secret label={t("runner.token")} text={created.token} />
								<Secret
									label={t("runner.command")}
									text={runnerCommand(origin, created.token)}
								/>
								<p className="text-muted text-xs">{t("runner.commandHint")}</p>
							</div>
						</Callout>
					</div>
				)}
			</Card>
			<Card title={t("runner.title")} icon="laptop">
				{runners.isPending && <Spinner />}
				<ErrorText error={runners.error} />
				{list.length > 0 && (
					<ul className="-mx-2 flex flex-col">
						{list.map((r) => (
							<RunnerRow key={r.id} runner={r} />
						))}
					</ul>
				)}
				{runners.isSuccess && list.length === 0 && (
					<EmptyState icon="laptop" title={t("runner.empty")}>
						{t("runner.emptyHint")}
					</EmptyState>
				)}
			</Card>
		</>
	);
}
