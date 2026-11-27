/**
 * 设置 → 全局助手的「后台研究」分组（M26）：总开关、四条管线（公网话题跟进、自动挖掘、内部关联、MCP 检查）、
 * 研究员的模型（空 = 与助手相同）、每日花费上限与今天已花、研究偏好（1500 字）。
 * 都整体 PUT 研究设置；请求进行中先显示要改成的值。上限与偏好是文本输入：上限在失焦或回车时保存，偏好点保存。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	DAILY_USD_MAX,
	DAILY_USD_MIN,
	PREFERENCES_LIMIT,
	type ResearchSettings,
	usePutResearchSettings,
	useResearchSettings,
} from "../../api/research-queries";
import { pickedModel } from "../../components/model-picker/ModelField";
import {
	ModelPicker,
	modelKey,
} from "../../components/model-picker/ModelPicker";
import { ModelPickerPopover } from "../../components/model-picker/ModelPickerPopover";
import { GroupNote } from "../../components/settings/agent-config-ui";
import { ModelLogo } from "../../components/ui/ModelLogo";
import {
	Button,
	ErrorText,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
} from "../../components/ui/workbench";
import { formatCost } from "../../lib/format";

/** 四条管线的开关，按显示顺序。 */
const PIPELINES = ["web", "mine", "internal", "mcp"] as const;

export function ResearchCard(): ReactNode {
	const { t } = useTranslation();
	const envelope = useResearchSettings();
	return (
		<Group title={t("settings.research.title")}>
			{envelope.isPending ? (
				<Spinner />
			) : envelope.isError ? (
				<ErrorText error={envelope.error} />
			) : (
				<ResearchBody
					saved={envelope.data.settings}
					spent={envelope.data.spentTodayUsd}
					capped={envelope.data.capped}
				/>
			)}
		</Group>
	);
}

function ResearchBody({
	saved,
	spent,
	capped,
}: {
	saved: ResearchSettings;
	spent: number;
	capped: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const put = usePutResearchSettings();
	// 请求进行中先显示要改成的值
	const current: ResearchSettings = (put.isPending && put.variables) || saved;
	const save = (patch: Partial<ResearchSettings>) =>
		put.mutate({ ...current, ...patch });
	// null = 没在编辑，显示当前值
	const [cap, setCap] = useState<string | null>(null);
	const [prefs, setPrefs] = useState<string | null>(null);
	const [picking, setPicking] = useState(false);
	const busy = put.isPending;
	const capText = cap ?? String(current.dailyUsd);
	const capValue = Number(capText);
	const capValid =
		capText.trim() !== "" &&
		Number.isFinite(capValue) &&
		capValue >= DAILY_USD_MIN &&
		capValue <= DAILY_USD_MAX;
	const commitCap = () => {
		if (cap === null) return;
		if (capValid && capValue !== current.dailyUsd) save({ dailyUsd: capValue });
		if (capValid) setCap(null);
	};
	const prefsText = prefs ?? current.preferences;
	const prefsCount = [...prefsText].length;
	const prefsOver = prefsCount > PREFERENCES_LIMIT;
	const model = current.model;
	return (
		<div data-testid="research-card" className="flex flex-col gap-1.5">
			<GroupList>
				<GroupRow
					icon="search"
					title={t("settings.research.enabled")}
					trailing={
						<Toggle
							hideLabel
							checked={current.enabled}
							disabled={busy}
							label={t("settings.research.enabled")}
							onChange={(enabled) => save({ enabled })}
						/>
					}
				/>
				{PIPELINES.map((key) => (
					<GroupRow
						key={key}
						title={
							<span className="font-normal">
								{t(`settings.research.pipeline.${key}`)}
							</span>
						}
						trailing={
							<Toggle
								hideLabel
								checked={current[key]}
								disabled={busy}
								label={t(`settings.research.pipeline.${key}`)}
								onChange={(on) => save({ [key]: on })}
							/>
						}
					/>
				))}
			</GroupList>
			<GroupNote>{t("settings.research.hint")}</GroupNote>
			<GroupList>
				<GroupRow
					icon="cpu"
					title={t("settings.research.model")}
					meta={
						model ? (
							<span
								data-testid="research-model"
								className="inline-flex min-w-0 items-center gap-1.5"
							>
								<ModelLogo
									model={model.id}
									provider={model.provider}
									size={12}
								/>
								<span className="truncate font-mono">{`${model.provider}/${model.id}`}</span>
							</span>
						) : (
							t("settings.research.modelDefault")
						)
					}
					trailing={
						<>
							{model && (
								<Button
									size="sm"
									variant="ghost"
									disabled={busy}
									onClick={() => {
										const { model: _drop, ...rest } = current;
										put.mutate(rest);
									}}
								>
									{t("settings.research.modelReset")}
								</Button>
							)}
							<span className="relative inline-flex">
								<Button
									size="sm"
									aria-expanded={picking}
									disabled={busy}
									onClick={() => setPicking((v) => !v)}
								>
									{t("settings.research.modelChange")}
								</Button>
								{picking && (
									<ModelPickerPopover
										title={t("settings.research.model")}
										onClose={() => setPicking(false)}
									>
										<ModelPicker
											free="model"
											selected={model ? modelKey(model) : undefined}
											onPick={(target) => {
												const picked = pickedModel(target);
												if (!picked) return;
												save({ model: picked });
												setPicking(false);
											}}
										/>
									</ModelPickerPopover>
								)}
							</span>
						</>
					}
				/>
				<GroupRow
					icon="coin"
					title={t("settings.research.dailyUsd")}
					meta={
						<>
							<span data-testid="research-spent" className="tabular-nums">
								{t("settings.research.spent", {
									spent: formatCost(spent),
									cap: formatCost(saved.dailyUsd),
								})}
							</span>
							{capped && (
								<span className="text-warn">
									{t("settings.research.capped")}
								</span>
							)}
							<span>{t("settings.research.dailyHint")}</span>
						</>
					}
					trailing={
						<TextInput
							type="number"
							inputMode="decimal"
							min={DAILY_USD_MIN}
							max={DAILY_USD_MAX}
							step="any"
							className="w-24"
							aria-label={t("settings.research.dailyUsd")}
							aria-invalid={!capValid || undefined}
							disabled={busy}
							value={capText}
							onChange={(e) => setCap(e.target.value)}
							onBlur={commitCap}
							onKeyDown={(e) => {
								if (e.key === "Enter") commitCap();
							}}
						/>
					}
				>
					{!capValid && (
						<p role="alert" className="text-err text-xs">
							{t("settings.research.dailyInvalid", {
								min: DAILY_USD_MIN,
								max: DAILY_USD_MAX,
							})}
						</p>
					)}
				</GroupRow>
			</GroupList>
			<GroupPanel>
				<FormField
					label={t("settings.research.preferences")}
					aside={t("settings.research.preferencesHint")}
				>
					{(p) => (
						<TextArea
							{...p}
							mono={false}
							rows={4}
							aria-invalid={prefsOver || undefined}
							value={prefsText}
							onChange={(e) => setPrefs(e.target.value)}
						/>
					)}
				</FormField>
				<div className="flex flex-wrap items-center justify-between gap-3 text-xs">
					<span role={prefsOver ? "alert" : undefined} className="text-err">
						{prefsOver
							? t("settings.research.tooLong", { limit: PREFERENCES_LIMIT })
							: ""}
					</span>
					<span className="ml-auto flex shrink-0 items-center gap-2">
						<span
							data-testid="research-preferences-count"
							className={`tabular-nums ${prefsOver ? "text-err" : "text-faint"}`}
						>
							{prefsCount} / {PREFERENCES_LIMIT}
						</span>
						<Button
							size="sm"
							disabled={prefs === null || prefsOver || busy}
							onClick={() =>
								put.mutate(
									{ ...current, preferences: prefsText },
									{ onSuccess: () => setPrefs(null) },
								)
							}
						>
							{t("settings.research.savePreferences")}
						</Button>
					</span>
				</div>
			</GroupPanel>
			<ErrorText error={put.error} />
		</div>
	);
}
