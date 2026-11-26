/**
 * 「连接」分组（R1–R3、R10）：全局总开关、Hindsight 地址、`.env` 的生成与输出语言（只读）。
 * 开关即改即存（沿用已保存的地址）；地址点「保存」才提交。远端地址不是 https 时服务端拒绝（`insecure_url`）。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemorySettings,
	useMemorySettings,
	usePutMemorySettings,
} from "../../../../api/memory-queries";
import { GroupNote } from "../../../../components/settings/agent-config-ui";
import {
	Button,
	Spinner,
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
import { formatRelative } from "../../../../lib/format";
import { EnvFileDialog } from "./EnvFileDialog";
import { MemoryError } from "./errors";
import { secToMs } from "./shared";

export function ConnectionCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemorySettings();
	return (
		<Group title={t("memoryPage.connection.title")}>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadSettings" error={q.error} />
			) : (
				<ConnectionForm settings={q.data} />
			)}
		</Group>
	);
}

function ConnectionForm({ settings }: { settings: MemorySettings }): ReactNode {
	const { t } = useTranslation();
	const put = usePutMemorySettings();
	const [url, setUrl] = useState(settings.hindsightUrl ?? "");
	const [envOpen, setEnvOpen] = useState(false);
	useEffect(() => setUrl(settings.hindsightUrl ?? ""), [settings.hindsightUrl]);
	const trimmed = url.trim();
	const dirty = trimmed !== (settings.hindsightUrl ?? "");
	const save = () =>
		put.mutate({
			enabled: settings.enabled,
			...(trimmed ? { hindsightUrl: trimmed } : {}),
		});
	return (
		<>
			<GroupList>
				<GroupRow
					icon="memory"
					title={t("memoryPage.connection.enabled")}
					trailing={
						<Toggle
							hideLabel
							checked={settings.enabled}
							disabled={put.isPending}
							onChange={(enabled) =>
								put.mutate({
									enabled,
									...(settings.hindsightUrl
										? { hindsightUrl: settings.hindsightUrl }
										: {}),
								})
							}
							label={t("memoryPage.connection.enabled")}
						/>
					}
				/>
				<GroupRow
					icon="key"
					title=".env"
					meta={
						settings.keysGenerated
							? t("memoryPage.connection.keysAt", {
									when: settings.keysGeneratedAt
										? formatRelative(secToMs(settings.keysGeneratedAt))
										: "—",
								})
							: t("memoryPage.connection.noKeys")
					}
					trailing={
						<Button size="sm" onClick={() => setEnvOpen(true)}>
							{settings.keysGenerated
								? t("memoryPage.env.regenerate")
								: t("memoryPage.env.generate")}
						</Button>
					}
				/>
			</GroupList>
			<GroupPanel>
				<form
					onSubmit={(e) => {
						e.preventDefault();
						if (dirty) save();
					}}
				>
					<FormField
						label={t("memoryPage.connection.url")}
						aside={t("memoryPage.connection.urlHint")}
					>
						{(p) => (
							<div className="flex min-w-0 flex-wrap gap-2">
								<TextInput
									{...p}
									value={url}
									onChange={(e) => setUrl(e.target.value)}
									placeholder="http://127.0.0.1:8888"
									inputMode="url"
									autoComplete="off"
									spellCheck={false}
									className="min-w-0 flex-1 basis-56 font-mono"
								/>
								<Button
									type="submit"
									variant="primary"
									disabled={!dirty || put.isPending}
								>
									{t("common.save")}
								</Button>
							</div>
						)}
					</FormField>
				</form>
			</GroupPanel>
			<MemoryError step="saveSettings" error={put.error} />
			<GroupNote>
				{t("memoryPage.connection.language", {
					language: settings.outputLanguage,
				})}
			</GroupNote>
			<EnvFileDialog
				open={envOpen}
				regenerate={settings.keysGenerated}
				onClose={() => setEnvOpen(false)}
			/>
		</>
	);
}
