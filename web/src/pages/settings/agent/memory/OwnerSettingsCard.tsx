/**
 * 「助手记忆」的设置（M17 R18）：助手记忆开关、主人的名字与别名（`OwnerSettingsCard`，放在「助手记忆」分组里），
 * 判断模型接口（`OwnerDecideCard`，单独一组）。
 * 开关即改即存（沿用已保存的名字）；名字与别名改完点「保存」。名字写入时一律归为「主人」，最多 16 个、每个不超过 64 字。
 * 判断模型（Google Decisions API，用于召回重排）的 key 只写不读：只显示是否已设置，留空保存 = 保留原 key，可单独清除；
 * 删除接口后召回改按 RRF 顺序给出并标注未经重排。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type OwnerMemoryDecideEndpoint,
	type OwnerMemorySettings,
	useDeleteOwnerMemoryDecideEndpoint,
	usePutOwnerMemoryDecideEndpoint,
	usePutOwnerMemorySettings,
} from "../../../../api/owner-memory-queries";
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import {
	Button,
	IconButton,
	TextInput,
	Toggle,
} from "../../../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
} from "../../../../components/ui/workbench";
import { MemoryError } from "./errors";

/** 与服务端 `normalizeNames` 一致。 */
export const OWNER_NAMES_MAX = 16;
export const OWNER_NAME_MAX_CHARS = 64;
/** 判断模型接口的默认值（服务端 `DefaultDecideBaseURL`、`DefaultDecideModel`）。 */
export const DEFAULT_DECIDE_BASE_URL =
	"https://generativelanguage.googleapis.com";
export const DEFAULT_DECIDE_MODEL = "gemini-decision-1";

/** 「助手记忆」分组的内容：开关一行 + 名字与别名面板。 */
export function OwnerSettingsCard({
	settings,
}: {
	settings: OwnerMemorySettings;
}): ReactNode {
	const { t } = useTranslation();
	const put = usePutOwnerMemorySettings();
	return (
		<>
			<GroupList>
				<GroupRow
					icon="memory"
					title={t("memoryPage.owner.settings.enabled")}
					meta={t("memoryPage.owner.settings.enabledHint")}
					trailing={
						<Toggle
							hideLabel
							checked={settings.enabled}
							disabled={put.isPending}
							onChange={(enabled) =>
								put.mutate({ enabled, ownerNames: settings.ownerNames })
							}
							label={t("memoryPage.owner.settings.enabled")}
						/>
					}
				/>
			</GroupList>
			<MemoryError step="ownerSaveSettings" error={put.error} />
			<NamesForm settings={settings} />
		</>
	);
}

/** 名字或别名的问题；没有问题返回 null。 */
function nameProblem(
	name: string,
	names: readonly string[],
): "empty" | "tooLong" | "duplicate" | "tooMany" | null {
	if (!name) return "empty";
	if ([...name].length > OWNER_NAME_MAX_CHARS) return "tooLong";
	if (names.includes(name)) return "duplicate";
	if (names.length >= OWNER_NAMES_MAX) return "tooMany";
	return null;
}

function NamesForm({ settings }: { settings: OwnerMemorySettings }): ReactNode {
	const { t } = useTranslation();
	const put = usePutOwnerMemorySettings();
	const [names, setNames] = useState<string[]>(settings.ownerNames);
	const [draft, setDraft] = useState("");
	useEffect(() => setNames(settings.ownerNames), [settings.ownerNames]);
	const candidate = draft.trim();
	const problem = draft === "" ? null : nameProblem(candidate, names);
	const dirty =
		names.length !== settings.ownerNames.length ||
		names.some((n, i) => n !== settings.ownerNames[i]);
	const add = () => {
		if (nameProblem(candidate, names)) return;
		setNames([...names, candidate]);
		setDraft("");
	};
	return (
		<GroupPanel>
			<FormField
				label={t("memoryPage.owner.settings.names")}
				aside={t("memoryPage.owner.settings.namesHint", {
					max: OWNER_NAMES_MAX,
					chars: OWNER_NAME_MAX_CHARS,
				})}
			>
				{(p) => (
					<div className="flex min-w-0 flex-wrap gap-2">
						<TextInput
							{...p}
							value={draft}
							onChange={(e) => setDraft(e.target.value.replace(/[\r\n]/g, ""))}
							onKeyDown={(e) => {
								if (e.key === "Enter" && !e.nativeEvent.isComposing) {
									e.preventDefault();
									add();
								}
							}}
							placeholder={t("memoryPage.owner.settings.namePlaceholder")}
							autoComplete="off"
							spellCheck={false}
							className="min-w-0 flex-1 basis-48"
						/>
						<Button
							icon="plus"
							disabled={candidate === "" || problem !== null}
							onClick={add}
						>
							{t("memoryPage.owner.settings.addName")}
						</Button>
					</div>
				)}
			</FormField>
			{problem && problem !== "empty" && (
				<p className="text-err text-xs" role="alert">
					{t(`memoryPage.owner.settings.nameProblem.${problem}`, {
						max: OWNER_NAMES_MAX,
						chars: OWNER_NAME_MAX_CHARS,
					})}
				</p>
			)}
			<div className="flex flex-wrap items-center gap-2">
				{names.length === 0 ? (
					<p className="text-[12.5px] text-muted">
						{t("memoryPage.owner.settings.noNames")}
					</p>
				) : (
					<ul
						className="flex min-w-0 flex-1 flex-wrap gap-1.5"
						aria-label={t("memoryPage.owner.settings.names")}
					>
						{names.map((n) => (
							<li
								key={n}
								className="inline-flex h-7 max-w-full items-center gap-0.5 rounded-[9px] bg-inset pr-0.5 pl-2 text-sm"
							>
								<span className="min-w-0 truncate">{n}</span>
								<IconButton
									icon="close"
									box="xs"
									size={12}
									label={t("memoryPage.owner.settings.removeName", {
										name: n,
									})}
									onClick={() => setNames(names.filter((x) => x !== n))}
								/>
							</li>
						))}
					</ul>
				)}
				<Button
					variant="primary"
					className="ml-auto"
					disabled={!dirty || put.isPending}
					onClick={() =>
						put.mutate({ enabled: settings.enabled, ownerNames: names })
					}
				>
					{t("memoryPage.owner.settings.saveNames")}
				</Button>
			</div>
			<MemoryError step="ownerSaveSettings" error={put.error} />
		</GroupPanel>
	);
}

/** 判断模型（召回重排）：一组，标题旁标「可选」。 */
export function OwnerDecideCard({
	decide,
}: {
	decide: OwnerMemoryDecideEndpoint | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const put = usePutOwnerMemoryDecideEndpoint();
	const del = useDeleteOwnerMemoryDecideEndpoint();
	const [baseUrl, setBaseUrl] = useState(
		decide?.baseUrl ?? DEFAULT_DECIDE_BASE_URL,
	);
	const [model, setModel] = useState(decide?.model ?? DEFAULT_DECIDE_MODEL);
	const [apiKey, setApiKey] = useState("");
	useEffect(() => {
		setBaseUrl(decide?.baseUrl ?? DEFAULT_DECIDE_BASE_URL);
		setModel(decide?.model ?? DEFAULT_DECIDE_MODEL);
		setApiKey("");
	}, [decide]);
	const busy = put.isPending || del.isPending;
	const dirty =
		decide === undefined ||
		baseUrl.trim() !== decide.baseUrl ||
		model.trim() !== decide.model ||
		apiKey !== "";
	const canSave = dirty && baseUrl.trim() !== "" && model.trim() !== "";
	const save = () =>
		put.mutate(
			{
				baseUrl: baseUrl.trim(),
				model: model.trim(),
				// 留空 = 保留原 key（省略字段）；空串会清除 key，只由「清除 key」发出
				...(apiKey ? { apiKey } : {}),
			},
			{ onSuccess: () => setApiKey("") },
		);
	return (
		<Group
			title={t("memoryPage.owner.decide.title")}
			meta={t("memoryPage.owner.decide.optional")}
		>
			<GroupPanel>
				<form
					className="flex flex-col gap-3"
					data-testid="owner-decide-form"
					onSubmit={(e) => {
						e.preventDefault();
						if (canSave && !busy) save();
					}}
				>
					<p className="text-[12.5px] text-muted leading-normal">
						{decide
							? t("memoryPage.owner.decide.configuredHint")
							: t("memoryPage.owner.decide.unsetHint")}
					</p>
					<FormField label={t("memoryPage.owner.decide.baseUrl")}>
						{(p) => (
							<TextInput
								{...p}
								value={baseUrl}
								onChange={(e) => setBaseUrl(e.target.value)}
								inputMode="url"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
							/>
						)}
					</FormField>
					<FormField label={t("memoryPage.owner.decide.model")}>
						{(p) => (
							<TextInput
								{...p}
								value={model}
								onChange={(e) => setModel(e.target.value)}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
							/>
						)}
					</FormField>
					<FormField
						label={t("memoryPage.owner.decide.apiKey")}
						aside={
							decide?.hasKey
								? t("memoryPage.owner.decide.keySet")
								: t("memoryPage.owner.decide.keyUnset")
						}
					>
						{(p) => (
							<TextInput
								{...p}
								type="password"
								value={apiKey}
								onChange={(e) => setApiKey(e.target.value)}
								autoComplete="new-password"
								placeholder={
									decide?.hasKey
										? t("memoryPage.owner.decide.keyKeep")
										: t("memoryPage.owner.decide.keyEmpty")
								}
							/>
						)}
					</FormField>
					<div className="flex flex-wrap items-center justify-end gap-2">
						{decide?.hasKey && (
							<ConfirmButton
								label={t("memoryPage.owner.decide.clearKey")}
								confirmLabel={t("memoryPage.owner.decide.clearKeyConfirm")}
								disabled={busy}
								onConfirm={() =>
									put.mutate({
										baseUrl: decide.baseUrl,
										model: decide.model,
										apiKey: "",
									})
								}
							/>
						)}
						{decide && (
							<ConfirmButton
								label={t("memoryPage.owner.decide.delete")}
								confirmLabel={t("memoryPage.owner.decide.deleteConfirm")}
								disabled={busy}
								onConfirm={() => del.mutate()}
							/>
						)}
						<Button type="submit" variant="primary" disabled={!canSave || busy}>
							{t("common.save")}
						</Button>
					</div>
					<MemoryError step="ownerSaveDecide" error={put.error} />
					<MemoryError step="ownerDeleteDecide" error={del.error} />
				</form>
			</GroupPanel>
		</Group>
	);
}
