/** 主机「测试」的结果：成功 / 失败、延迟、系统、指纹与一次状态快照（负载、CPU、内存、各磁盘）。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { SshTestResult } from "../../../../api/ssh-queries";
import { CopyButton } from "../../../../components/ui/CopyButton";
import { Meter, thresholdTone } from "../../../../components/ui/data";
import { Icon } from "../../../../components/ui/icons";
import { IconButton } from "../../../../components/ui/primitives";
import { formatBytes, formatPercent } from "../../../../lib/format";

function Usage({
	label,
	used,
	total,
}: {
	label: string;
	used: number;
	total: number;
}): ReactNode {
	const pct = total > 0 ? (used / total) * 100 : 0;
	return (
		<div className="flex flex-col gap-1">
			<div className="flex items-baseline justify-between gap-2 text-xs">
				<span className="min-w-0 truncate font-mono text-muted">{label}</span>
				<span className="shrink-0 text-faint tabular-nums">
					{formatBytes(used * 1024)} / {formatBytes(total * 1024)}
				</span>
			</div>
			<Meter value={pct} tone={thresholdTone(pct)} label={label} />
		</div>
	);
}

export function TestResultView({
	result,
	onClose,
}: {
	result: SshTestResult;
	onClose?: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const snap = result.snapshot;
	return (
		<div
			data-testid="ssh-test-result"
			// 量表轨道是内嵌井（bg-inset），外框不能再用 inset，否则轨道看不见
			className="flex flex-col gap-2 rounded-md border border-border bg-raised px-3 py-2 text-xs"
		>
			<div className="flex items-start gap-2">
				<Icon
					name={result.ok ? "checkCircle" : "xCircle"}
					size={14}
					aria-hidden
					className={`mt-px shrink-0 ${result.ok ? "text-ok" : "text-err"}`}
				/>
				<div className="min-w-0 flex-1">
					<p className="font-medium text-fg">
						{result.ok ? t("sshPage.test.ok") : t("sshPage.test.failed")}
						<span className="ml-2 font-normal text-faint tabular-nums">
							{t("sshPage.test.latency", { ms: result.latencyMs })}
							{result.os && ` · ${result.os}`}
						</span>
					</p>
					{result.error && (
						<p className="mt-0.5 break-words text-err">{result.error}</p>
					)}
				</div>
				{onClose && (
					<IconButton
						icon="close"
						size={12}
						box="xs"
						label={t("common.close")}
						onClick={onClose}
					/>
				)}
			</div>
			{result.hostKeyFingerprint && (
				<div className="flex min-w-0 items-center gap-1 text-muted">
					<span className="shrink-0">{t("sshPage.test.fingerprint")}</span>
					<code className="min-w-0 truncate font-mono text-fg">
						{result.hostKeyFingerprint}
					</code>
					<CopyButton text={result.hostKeyFingerprint} />
				</div>
			)}
			{snap && (
				<div className="grid gap-2 sm:grid-cols-2">
					{snap.load1 !== undefined && (
						<p className="text-muted sm:col-span-2">
							{t("sshPage.test.load")}{" "}
							<span className="text-fg tabular-nums">
								{snap.load1.toFixed(2)}
							</span>
						</p>
					)}
					{snap.cpuPct !== undefined && (
						<div className="flex flex-col gap-1">
							<div className="flex justify-between text-xs">
								<span className="text-muted">CPU</span>
								<span className="text-faint tabular-nums">
									{formatPercent(snap.cpuPct)}
								</span>
							</div>
							<Meter
								value={snap.cpuPct}
								tone={thresholdTone(snap.cpuPct)}
								label="CPU"
							/>
						</div>
					)}
					{snap.memTotalKb !== undefined && snap.memUsedKb !== undefined && (
						<Usage
							label={t("sshPage.test.memory")}
							used={snap.memUsedKb}
							total={snap.memTotalKb}
						/>
					)}
					{snap.disks.map((d) => (
						<Usage
							key={d.mount}
							label={d.mount}
							used={d.usedKb}
							total={d.totalKb}
						/>
					))}
				</div>
			)}
		</div>
	);
}
