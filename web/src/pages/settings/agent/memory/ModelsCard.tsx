/**
 * 「模型」分组（T1、U2、T3、R16）：五种用途各选一个模型，「默认」必选，其余对象区有「跟随默认」。
 * 每个用途是一个模型字段（`ModelField`），点开统一选择器；候选是控制面给的 `options`（中央模型目录里的对话模型，
 * 协议类型由控制面判定，写在行尾）；不能用于 Hindsight 的置灰并写明原因；已不在目录里的旧值是一条不可选的对象行。
 * 生效协议与 `.env` 里已部署的不同时，给出要改进 `.env` 的片段、「需要重启 Hindsight」与「我已重启」。
 */
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	MEMORY_PURPOSES,
	type MemoryModels,
	type MemoryPurpose,
	useConfirmMemoryModelsDeployed,
	useMemoryModels,
	usePutMemoryModels,
} from "../../../../api/memory-queries";
import { ModelField } from "../../../../components/model-picker/ModelField";
import {
	entryKey,
	ModelPicker,
	modelKey,
	type PickerEntry,
} from "../../../../components/model-picker/ModelPicker";
import { GroupNote } from "../../../../components/settings/agent-config-ui";
import { CopyButton } from "../../../../components/ui/CopyButton";
import { Badge, Callout } from "../../../../components/ui/data";
import { Icon } from "../../../../components/ui/icons";
import { Button, Spinner } from "../../../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupPanel,
} from "../../../../components/ui/workbench";
import type { PickerModel } from "../../../../lib/model-picker";
import { MemoryError } from "./errors";

/** 选择器的值：空串 = 跟随默认（「默认」用途里表示尚未选择）。 */
type Draft = Record<MemoryPurpose, string>;
/** 选择器对象区：「跟随默认」与已不在目录里的旧值。 */
const FOLLOW = "follow";
const MISSING = "missing";

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
		<Group title={t("memoryPage.models.title")}>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadModels" error={q.error} />
			) : (
				<ModelsForm models={q.data} />
			)}
		</Group>
	);
}

function ModelsForm({ models }: { models: MemoryModels }): ReactNode {
	const { t } = useTranslation();
	const put = usePutMemoryModels();
	const confirm = useConfirmMemoryModelsDeployed();
	const saved = useMemo(() => draftOf(models), [models]);
	const [draft, setDraft] = useState<Draft>(saved);
	useEffect(() => setDraft(saved), [saved]);
	// 控制面的候选 → 选择器的模型行。候选来自目录的对话模型，provider/id 本应唯一；万一重复只留第一条，
	// 免得选择器里出现两行同键的模型
	const byKey = useMemo(() => {
		const m = new Map<string, PickerModel>();
		for (const o of models.options) {
			const key = optionKey(o.providerKey, o.modelId);
			if (m.has(key)) continue;
			m.set(key, {
				provider: o.providerKey,
				id: o.modelId,
				name: o.name,
				selector: `${o.providerKey}/${o.modelId}`,
				unavailable: o.family
					? undefined
					: (o.reason ?? t("memoryPage.models.unknownReason")),
				note: o.family ? t(`memoryPage.family.${o.family}`) : undefined,
			});
		}
		return m;
	}, [models.options, t]);
	const pickerModels = useMemo(() => [...byKey.values()], [byKey]);
	const dirty = MEMORY_PURPOSES.some((p) => draft[p] !== saved[p]);
	const unavailable = models.options.filter((o) => !o.family).length;
	const restart = models.items.filter((i) => i.needsRestart);
	const defaultName = byKey.get(draft.default)?.name;
	const followName = defaultName
		? t("memoryPage.models.followNamed", { name: defaultName })
		: t("memoryPage.models.follow");

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
		<>
			<GroupPanel>
				<div className="grid gap-3 sm:grid-cols-2">
					{MEMORY_PURPOSES.map((purpose) => {
						const choice = models.items.find((i) => i.purpose === purpose);
						const value = draft[purpose];
						const model = value ? byKey.get(value) : undefined;
						const follows = value === "" && purpose !== "default";
						// 草稿里的旧值已不在候选里（模型从目录消失）
						const missingId =
							value !== "" && !model ? choice?.modelId : undefined;
						const missing = missingId
							? t("memoryPage.models.missing", { name: missingId })
							: undefined;
						const entries: PickerEntry[] = [
							...(purpose === "default"
								? []
								: [
										{
											key: FOLLOW,
											name: followName,
											face: <Icon name="link" size={14} />,
										},
									]),
							...(missingId
								? [
										{
											key: MISSING,
											name: missingId,
											face: <Icon name="alert" size={14} />,
											problem: missing,
										},
									]
								: []),
						];
						const selected = follows
							? entryKey(FOLLOW)
							: model
								? modelKey(model)
								: missing
									? entryKey(MISSING)
									: undefined;
						return (
							<div key={purpose} data-purpose={purpose}>
								<FormField
									label={
										<span className="inline-flex flex-wrap items-center gap-1.5">
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
									aside={t(`memoryPage.purposeHint.${purpose}`)}
								>
									{(p) => (
										<ModelField
											{...p}
											title={t(`memoryPage.purpose.${purpose}`)}
											value={model?.selector ?? ""}
											models={pickerModels}
											display={follows ? followName : missing}
											placeholder={t("memoryPage.models.pick")}
										>
											{(close) => (
												<ModelPicker
													entries={entries}
													models={pickerModels}
													selected={selected}
													onPick={(target) => {
														const next =
															target.kind === "model"
																? optionKey(
																		target.model.provider,
																		target.model.id,
																	)
																: target.kind === "entry" &&
																		target.entry.key === FOLLOW
																	? ""
																	: undefined;
														if (next === undefined) return;
														setDraft((d) => ({ ...d, [purpose]: next }));
														close();
													}}
												/>
											)}
										</ModelField>
									)}
								</FormField>
							</div>
						);
					})}
				</div>
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
			</GroupPanel>
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
									className="overflow-auto whitespace-pre rounded-[11px] bg-inset px-3 py-2 font-mono text-xs"
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
			<GroupNote>
				{t("memoryPage.models.hint")}
				{unavailable > 0 &&
					` ${t("memoryPage.models.unavailable", { n: unavailable })}`}
			</GroupNote>
		</>
	);
}
