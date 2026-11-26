/**
 * 设置 → 输入与快捷键（个人）：
 * 1. 新对话：默认对象（`NewThreadTargetCard`，在 IdentitySettings）。
 * 2. 输入框：发送键（本机偏好，`lib/composer` 的 `setSendKey`）；下一条消息预测两个开关（omp `config.yml`：
 *    `composer.predictions` 总开关，`composer.predictionAlternatives` 只在总开关打开时可改；关闭即删除该键，
 *    每次改动都在最新的 config.yml 上执行 `useOmpConfig().mutate`）。
 * 3. 选区快捷动作（`SelectionActionsSettings`）。
 * 4. 快捷键：只读一览，取自 `lib/shortcuts` 的清单；输入框按键跟随当前发送键。
 */
import { Fragment, type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useOmpConfig } from "../../api/agent-config-queries";
import { ApplyStatusBanner } from "../../components/settings/ApplyStatusBanner";
import { AgentErrorText } from "../../components/settings/agent-config-ui";
import SelectionActionsSettings from "../../components/settings/SelectionActionsSettings";
import { Callout } from "../../components/ui/data";
import {
	ErrorText,
	Kbd,
	Select,
	Spinner,
	Toggle,
} from "../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../components/ui/workbench";
import { type SendKey, setSendKey, useSendKey } from "../../lib/composer";
import {
	type ComposerPredictionSettings,
	composerPredictionSettings,
	setComposerPredictionSetting,
} from "../../lib/omp-config";
import {
	APP_SHORTCUTS,
	composerShortcuts,
	formatCombo,
	isApplePlatform,
	type KeyCombo,
} from "../../lib/shortcuts";
import { NewThreadTargetCard } from "./IdentitySettings";

export default function InputSettings(): ReactNode {
	return (
		<>
			<NewThreadTargetCard />
			<ComposerGroup />
			<SelectionActionsSettings />
			<ShortcutsGroup />
		</>
	);
}

const SEND_KEYS: readonly SendKey[] = ["enter", "mod"];

function ComposerGroup(): ReactNode {
	const { t } = useTranslation();
	const send = useSendKey();
	const mac = isApplePlatform();
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
	const omp = t("inputSettings.predictions.omp");
	return (
		<Group
			title={t("inputSettings.composer")}
			action={config.pending ? <Spinner /> : undefined}
		>
			<GroupList>
				<GroupRow
					icon="keyboard"
					title={t("inputSettings.sendKey.title")}
					meta={t("inputSettings.sendKey.hint")}
					trailing={
						<Select
							className="w-40"
							aria-label={t("inputSettings.sendKey.title")}
							value={send}
							options={SEND_KEYS.map((k) => ({
								value: k,
								label: t("inputSettings.sendKey.option", {
									key: formatCombo(
										{ key: "Enter", code: "Enter", mod: k === "mod" },
										mac,
									),
								}),
							}))}
							onChange={setSendKey}
						/>
					}
				/>
				<GroupRow
					icon="sparkle"
					title={t("inputSettings.predictions.title")}
					meta={`${t("inputSettings.predictions.hint")} ${omp}`}
					trailing={
						<Toggle
							hideLabel
							label={t("inputSettings.predictions.title")}
							checked={settings.predictions}
							disabled={!ready}
							onChange={(on) => set("predictions", on)}
						/>
					}
				/>
				<GroupRow
					icon="stack"
					title={t("inputSettings.predictions.alternatives")}
					meta={
						settings.predictions
							? `${t("inputSettings.predictions.alternativesHint")} ${omp}`
							: t("inputSettings.predictions.needsPredictions")
					}
					trailing={
						<Toggle
							hideLabel
							label={t("inputSettings.predictions.alternatives")}
							checked={settings.alternatives}
							disabled={!ready || !settings.predictions}
							onChange={(on) => set("alternatives", on)}
						/>
					}
				/>
			</GroupList>
			{invalid && (
				<Callout tone="warn" role="status">
					{t("agentPages.errors.configInvalid", {
						reason: config.doc?.errors[0]?.message ?? "",
					})}
				</Callout>
			)}
			<ErrorText error={config.error} />
			<AgentErrorText error={config.mutateError} />
			<ApplyStatusBanner />
		</Group>
	);
}

/** 一组组合键：`range` 显示成「首 – 尾」，`twice` 显示两遍（Esc Esc），其余逐个列出。 */
function Keys({
	combos,
	range,
	twice,
	mac,
}: {
	combos: readonly KeyCombo[];
	range?: boolean;
	twice?: boolean;
	mac: boolean;
}): ReactNode {
	const shown =
		range && combos.length > 1
			? [combos[0], combos.at(-1)].filter((c) => c !== undefined)
			: combos;
	return (
		<span className="inline-flex items-center gap-1 text-faint text-xs">
			{shown.map((c, i) => {
				const label = formatCombo(c, mac);
				return (
					<Fragment key={label}>
						{i > 0 && <span aria-hidden>{range ? "–" : "/"}</span>}
						<Kbd>{label}</Kbd>
						{twice && <Kbd>{label}</Kbd>}
					</Fragment>
				);
			})}
		</span>
	);
}

function ShortcutsGroup(): ReactNode {
	const { t } = useTranslation();
	const send = useSendKey();
	const mac = isApplePlatform();
	return (
		<Group title={t("inputSettings.shortcuts.title")}>
			<GroupList>
				{APP_SHORTCUTS.map((s) => (
					<GroupRow
						key={s.id}
						title={t(`inputSettings.shortcuts.${s.id}`)}
						meta={t(
							`inputSettings.shortcuts.scope.${s.scope === "thread" ? "thread" : s.notInInput ? "appNotInInput" : "app"}`,
						)}
						trailing={<Keys combos={s.combos} range={s.range} mac={mac} />}
					/>
				))}
				{composerShortcuts(send).map((s) => (
					<GroupRow
						key={s.id}
						title={t(`inputSettings.shortcuts.${s.id}`)}
						meta={t(
							`inputSettings.shortcuts.scope.${s.busy ? "composerBusy" : s.empty ? "composerEmpty" : "composer"}`,
						)}
						trailing={<Keys combos={s.combos} twice={s.twice} mac={mac} />}
					/>
				))}
			</GroupList>
		</Group>
	);
}
