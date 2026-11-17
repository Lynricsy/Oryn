/**
 * 设置 → Agent → 输入框：omp `config.yml` 里与输入框相关的设置（同 omp TUI 设置的 interaction › Input 组）。
 * 目前是下一条消息预测：`composer.predictions` 总开关，`composer.predictionAlternatives` 只在总开关打开时可改。
 * 关闭即删除该键（Core 默认 false）；每次改动都在最新的 config.yml 上执行（`useOmpConfig().mutate`）。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useOmpConfig } from "../../../api/agent-config-queries";
import { ApplyStatusBanner } from "../../../components/settings/ApplyStatusBanner";
import {
	AgentErrorText,
	AgentScopeNote,
} from "../../../components/settings/agent-config-ui";
import { Callout, Card } from "../../../components/ui/data";
import { ErrorText, Spinner, Toggle } from "../../../components/ui/primitives";
import {
	type ComposerPredictionSettings,
	composerPredictionSettings,
	setComposerPredictionSetting,
} from "../../../lib/omp-config";

export default function ComposerPage(): ReactNode {
	const { t } = useTranslation();
	const config = useOmpConfig();
	const settings = useMemo(
		() => composerPredictionSettings(config.doc),
		[config.doc],
	);
	const invalid = config.doc !== undefined && config.doc.errors.length > 0;
	const ready = config.doc !== undefined && !invalid;
	const set = (key: keyof ComposerPredictionSettings, on: boolean) =>
		void config.mutate((doc) => {
			if (composerPredictionSettings(doc)[key] === on) return false;
			setComposerPredictionSetting(doc, key, on);
			return true;
		});

	return (
		<>
			<ApplyStatusBanner />
			<AgentScopeNote reachesAssistant />
			{invalid && (
				<Callout tone="warn" role="status">
					{t("agentPages.errors.configInvalid", {
						reason: config.doc?.errors[0]?.message ?? "",
					})}
				</Callout>
			)}
			<Card
				title={t("agentPages.composer.title")}
				icon="keyboard"
				action={config.pending ? <Spinner /> : undefined}
			>
				{config.error ? (
					<ErrorText error={config.error} />
				) : config.doc === undefined ? (
					<div className="flex justify-center py-6">
						<Spinner />
					</div>
				) : (
					<ul className="-mx-2 flex flex-col">
						<SettingRow
							label={t("agentPages.composer.predictions")}
							hint={t("agentPages.composer.predictionsHint")}
							checked={settings.predictions}
							disabled={!ready}
							onChange={(on) => set("predictions", on)}
						/>
						<SettingRow
							label={t("agentPages.composer.alternatives")}
							hint={
								settings.predictions
									? t("agentPages.composer.alternativesHint")
									: `${t("agentPages.composer.alternativesHint")} ${t("agentPages.composer.alternativesNeedsPredictions")}`
							}
							checked={settings.alternatives}
							disabled={!ready || !settings.predictions}
							onChange={(on) => set("alternatives", on)}
						/>
					</ul>
				)}
				<AgentErrorText error={config.mutateError} />
			</Card>
		</>
	);
}

/** 一行设置：标题 + 说明在左，开关在右。 */
function SettingRow({
	label,
	hint,
	checked,
	disabled,
	onChange,
}: {
	label: string;
	hint: string;
	checked: boolean;
	disabled: boolean;
	onChange: (on: boolean) => void;
}): ReactNode {
	return (
		<li className="flex items-start gap-3 rounded-md px-2 py-2">
			<span className="flex min-w-0 flex-1 flex-col gap-0.5">
				<span className={`font-medium text-sm ${disabled ? "text-muted" : ""}`}>
					{label}
				</span>
				<span className="text-faint text-xs">{hint}</span>
			</span>
			<span className="shrink-0 pt-0.5">
				<Toggle
					checked={checked}
					disabled={disabled}
					onChange={onChange}
					label={<span className="sr-only">{label}</span>}
				/>
			</span>
		</li>
	);
}
