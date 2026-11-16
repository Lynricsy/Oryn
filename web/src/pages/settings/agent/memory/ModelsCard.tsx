/**
 * 记忆页「模型」卡（T1、U2、T3、R16）：五种用途各选一个模型，「默认」必选，其余第一项是「跟随默认」。
 * 候选是控制面给的 `options`（中央模型目录里的对话模型，协议类型由控制面判定）；不能用于 Hindsight 的置灰并写明原因。
 * 生效协议与 `.env` 里已部署的不同时，给出要改进 `.env` 的片段、「需要重启 Hindsight」与「我已重启」。
 */
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	MEMORY_PURPOSES,
	type MemoryModelOption,
	type MemoryModels,
	type MemoryPurpose,
	useConfirmMemoryModelsDeployed,
	useMemoryModels,
	usePutMemoryModels,
} from "../../../../api/memory-queries";
import { CopyButton } from "../../../../components/ui/CopyButton";
import { Badge, Callout, Card } from "../../../../components/ui/data";
import {
	Button,
	Field,
	Select,
	Spinner,
} from "../../../../components/ui/primitives";
import { MemoryError } from "./errors";

/** 选择器的值：空串 = 跟随默认（「默认」用途里表示尚未选择）。 */
type Draft = Record<MemoryPurpose, string>;

function optionKey(providerKey: string, modelId: string): string {
	return JSON.stringify([providerKey, modelId]);
}

function draftOf(models: MemoryModels): Draft {
	const d = {} as Draft;
	for (const p of MEMORY_PURPOSES) {
		const c = models.items.find((i) => i.purpose === p);
		d[p] =
			c && !c.followsDefault && c.providerKey && c.modelId
				? optionKey(c.providerKey, c.modelId)
				: "";
	}
	return d;
}

export function ModelsCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryModels();
	return (
		<Card title={t("memoryPage.models.title")} icon="sparkle">
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadModels" error={q.error} />
			) : q.data ? (
				<ModelsForm models={q.data} />
			) : null}
		</Card>
	);
}

function ModelsForm({ models }: { models: MemoryModels }): ReactNode {
	const { t } = useTranslation();
	const put = usePutMemoryModels();
	const confirm = useConfirmMemoryModelsDeployed();
	const saved = useMemo(() => draftOf(models), [models]);
	const [draft, setDraft] = useState<Draft>(saved);
	useEffect(() => setDraft(saved), [saved]);
	const byKey = useMemo(() => {
		const m = new Map<string, MemoryModelOption>();
		for (const o of models.options)
			m.set(optionKey(o.providerKey, o.modelId), o);
		return m;
	}, [models.options]);
	const dirty = MEMORY_PURPOSES.some((p) => draft[p] !== saved[p]);
	const unavailable = models.options.filter((o) => !o.family).length;
	const restart = models.items.filter((i) => i.needsRestart);
	const defaultName = byKey.get(draft.default)?.name;

	const save = () =>
		put.mutate({
			items: MEMORY_PURPOSES.map((purpose) => {
				const key = draft[purpose];
				if (!key) return { purpose };
				const [providerKey, modelId] = JSON.parse(key) as [string, string];
				return { purpose, providerKey, modelId };
			}),
		});

	return (
		<div className="flex flex-col gap-3">
			<p className="text-faint text-xs">{t("memoryPage.models.hint")}</p>
			<div className="grid gap-3 sm:grid-cols-2">
				{MEMORY_PURPOSES.map((purpose) => {
					const choice = models.items.find((i) => i.purpose === purpose);
					const value = draft[purpose];
					const known = value === "" || byKey.has(value);
					return (
						<Field
							key={purpose}
							label={
								<span className="flex flex-wrap items-center gap-1.5">
									{t(`memoryPage.purpose.${purpose}`)}
									{choice?.family && (
										<Badge>{t(`memoryPage.family.${choice.family}`)}</Badge>
									)}
									{choice?.needsRestart && (
										<Badge tone="warn" icon="alert">
											{t("memoryPage.models.needsRestartBadge")}
										</Badge>
									)}
								</span>
							}
							hint={t(`memoryPage.purposeHint.${purpose}`)}
						>
							<Select
								value={value}
								data-purpose={purpose}
								onChange={(e) =>
									setDraft((d) => ({ ...d, [purpose]: e.target.value }))
								}
							>
								{purpose === "default" ? (
									value === "" && (
										<option value="" disabled>
											{t("memoryPage.models.pick")}
										</option>
									)
								) : (
									<option value="">
										{defaultName
											? t("memoryPage.models.followNamed", {
													name: defaultName,
												})
											: t("memoryPage.models.follow")}
									</option>
								)}
								{!known && choice?.modelId && (
									<option value={value} disabled>
										{t("memoryPage.models.missing", { name: choice.modelId })}
									</option>
								)}
								{models.options.map((o) => (
									<option
										key={optionKey(o.providerKey, o.modelId)}
										value={optionKey(o.providerKey, o.modelId)}
										disabled={!o.family}
									>
										{o.family
											? t("memoryPage.models.option", {
													name: o.name,
													provider: o.providerKey,
													family: t(`memoryPage.family.${o.family}`),
												})
											: t("memoryPage.models.optionUnavailable", {
													name: o.name,
													provider: o.providerKey,
													reason:
														o.reason ?? t("memoryPage.models.unknownReason"),
												})}
									</option>
								))}
							</Select>
						</Field>
					);
				})}
			</div>
			{unavailable > 0 && (
				<p className="text-faint text-xs">
					{t("memoryPage.models.unavailable", { n: unavailable })}
				</p>
			)}
			<div className="flex flex-wrap items-center justify-end gap-2">
				{dirty && (
					<Button variant="ghost" onClick={() => setDraft(saved)}>
						{t("common.cancel")}
					</Button>
				)}
				<Button
					variant="primary"
					disabled={!dirty || draft.default === "" || put.isPending}
					onClick={save}
				>
					{t("common.save")}
				</Button>
			</div>
			<MemoryError step="saveModels" error={put.error} />
			{restart.length > 0 && (
				<Callout
					tone="warn"
					role="status"
					title={t("memoryPage.models.needsRestart")}
				>
					<div className="flex flex-col gap-2">
						<p>
							{t("memoryPage.models.needsRestartHint", {
								purposes: restart
									.map((i) => t(`memoryPage.purpose.${i.purpose}`))
									.join(t("memoryPage.listSep")),
							})}
						</p>
						{models.envSnippet && (
							<div className="flex flex-col gap-1 text-fg">
								<div className="flex justify-end">
									<CopyButton
										text={models.envSnippet}
										label={t("common.copy")}
									/>
								</div>
								<pre
									data-testid="memory-env-snippet"
									className="overflow-auto whitespace-pre rounded-md bg-inset px-3 py-2 font-mono text-xs"
								>
									{models.envSnippet}
								</pre>
							</div>
						)}
						<div>
							<Button
								size="sm"
								variant="primary"
								disabled={confirm.isPending}
								onClick={() => confirm.mutate()}
							>
								{t("memoryPage.models.restarted")}
							</Button>
						</div>
						<MemoryError step="confirmDeployed" error={confirm.error} />
					</div>
				</Callout>
			)}
		</div>
	);
}
