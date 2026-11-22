/**
 * 设置 → 全局助手的「后台研究」卡片（M26）：总开关、四条管线（公网话题跟进、自动挖掘、内部关联、MCP 检查）、
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
import { ModelPickerBody } from "../../components/composer/ModelPickerBody";
import { Badge, Card } from "../../components/ui/data";
import { ModelLogo } from "../../components/ui/ModelLogo";
import { Popover } from "../../components/ui/PickerPopover";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import { formatCost } from "../../lib/format";

/** 四条管线的开关，按卡片里的顺序。 */
const PIPELINES = ["web", "mine", "internal", "mcp"] as const;

export function ResearchCard(): ReactNode {
	const { t } = useTranslation();
	const envelope = useResearchSettings();
	const put = usePutResearchSettings();
	// 请求进行中先显示要改成的值
	const current: ResearchSettings | undefined = put.isPending
		? put.variables
		: envelope.data?.settings;
	const save = (patch: Partial<ResearchSettings>) => {
		if (current) put.mutate({ ...current, ...patch });
	};
	// null = 没在编辑，显示当前值
	const [cap, setCap] = useState<string | null>(null);
	const [prefs, setPrefs] = useState<string | null>(null);
	const [picking, setPicking] = useState(false);
	const busy = put.isPending || !current;
	const capText = cap ?? String(current?.dailyUsd ?? "");
	const capValue = Number(capText);
	const capValid =
		capText.trim() !== "" &&
		Number.isFinite(capValue) &&
		capValue >= DAILY_USD_MIN &&
		capValue <= DAILY_USD_MAX;
	const commitCap = () => {
		if (cap === null) return;
		if (capValid && capValue !== current?.dailyUsd)
			save({ dailyUsd: capValue });
		if (capValid) setCap(null);
	};
	const prefsText = prefs ?? current?.preferences ?? "";
	const prefsCount = [...prefsText].length;
	const prefsOver = prefsCount > PREFERENCES_LIMIT;
	const model = current?.model;
	return (
		<Card title={t("settings.research.title")} icon="search">
			<p className="text-faint text-xs">{t("settings.research.hint")}</p>
			{envelope.isPending ? (
				<Spinner />
			) : envelope.isError ? (
				<ErrorText error={envelope.error} />
			) : (
				<div data-testid="research-card" className="mt-2 flex flex-col gap-4">
					<Toggle
						checked={current?.enabled ?? false}
						disabled={busy}
						label={t("settings.research.enabled")}
						onChange={(enabled) => save({ enabled })}
					/>
					<fieldset className="flex flex-col gap-2">
						<legend className="mb-1.5 font-medium text-muted text-xs">
							{t("settings.research.pipelines")}
						</legend>
						{PIPELINES.map((key) => (
							<Toggle
								key={key}
								checked={current?.[key] ?? false}
								disabled={busy}
								label={t(`settings.research.pipeline.${key}`)}
								onChange={(on) => save({ [key]: on })}
							/>
						))}
					</fieldset>
					<div className="flex flex-col gap-1 text-sm">
						<span className="font-medium text-muted text-xs">
							{t("settings.research.model")}
						</span>
						<div className="flex flex-wrap items-center gap-2">
							{model ? (
								<span
									data-testid="research-model"
									className="inline-flex h-7 items-center gap-1 rounded-full border border-border bg-inset px-2.5 text-xs"
								>
									<ModelLogo
										model={model.id}
										provider={model.provider}
										size={12}
									/>
									<span className="font-mono">{`${model.provider}/${model.id}`}</span>
								</span>
							) : (
								<span className="text-muted text-xs">
									{t("settings.research.modelDefault")}
								</span>
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
									<Popover side="bottom" onClose={() => setPicking(false)}>
										<div className="p-1">
											<ModelPickerBody
												free
												current={model}
												onPick={({ provider, id }) => {
													save({ model: { provider, id } });
													setPicking(false);
												}}
											/>
										</div>
									</Popover>
								)}
							</span>
							{model && (
								<Button
									size="sm"
									variant="ghost"
									disabled={busy}
									onClick={() => {
										if (!current) return;
										const { model: _drop, ...rest } = current;
										put.mutate(rest);
									}}
								>
									{t("settings.research.modelReset")}
								</Button>
							)}
						</div>
					</div>
					<Field
						label={t("settings.research.dailyUsd")}
						hint={t("settings.research.dailyHint")}
					>
						<span className="flex flex-wrap items-center gap-2">
							<TextInput
								type="number"
								inputMode="decimal"
								min={DAILY_USD_MIN}
								max={DAILY_USD_MAX}
								step="any"
								className="w-28"
								aria-invalid={!capValid || undefined}
								disabled={busy}
								value={capText}
								onChange={(e) => setCap(e.target.value)}
								onBlur={commitCap}
								onKeyDown={(e) => {
									if (e.key === "Enter") commitCap();
								}}
							/>
							{envelope.data && (
								<span
									data-testid="research-spent"
									className="text-faint text-xs tabular-nums"
								>
									{t("settings.research.spent", {
										spent: formatCost(envelope.data.spentTodayUsd),
										cap: formatCost(envelope.data.settings.dailyUsd),
									})}
								</span>
							)}
							{envelope.data?.capped && (
								<Badge tone="warn">{t("settings.research.capped")}</Badge>
							)}
							{!capValid && (
								<span role="alert" className="text-err text-xs">
									{t("settings.research.dailyInvalid", {
										min: DAILY_USD_MIN,
										max: DAILY_USD_MAX,
									})}
								</span>
							)}
						</span>
					</Field>
					<div className="flex flex-col gap-1">
						<Field
							label={t("settings.research.preferences")}
							hint={t("settings.research.preferencesHint")}
						>
							<TextArea
								mono={false}
								rows={4}
								aria-invalid={prefsOver || undefined}
								value={prefsText}
								onChange={(e) => setPrefs(e.target.value)}
							/>
						</Field>
						<div className="flex items-center justify-between gap-3 text-xs">
							<span role={prefsOver ? "alert" : undefined} className="text-err">
								{prefsOver
									? t("settings.research.tooLong", {
											limit: PREFERENCES_LIMIT,
										})
									: ""}
							</span>
							<span className="flex shrink-0 items-center gap-2">
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
										current &&
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
					</div>
				</div>
			)}
			<ErrorText error={put.error} />
		</Card>
	);
}
