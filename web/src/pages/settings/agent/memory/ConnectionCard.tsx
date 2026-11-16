/**
 * 记忆页「连接」卡（R1–R3、R10）：Hindsight 地址、全局总开关、输出语言（只读）与 `.env` 的生成。
 * 开关即改即存（沿用已保存的地址）；地址点「保存」才提交。远端地址不是 https 时服务端拒绝（`insecure_url`）。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemorySettings,
	useMemorySettings,
	usePutMemorySettings,
} from "../../../../api/memory-queries";
import { Card } from "../../../../components/ui/data";
import {
	Button,
	Field,
	Spinner,
	TextInput,
	Toggle,
} from "../../../../components/ui/primitives";
import { formatRelative } from "../../../../lib/format";
import { EnvFileDialog } from "./EnvFileDialog";
import { MemoryError } from "./errors";
import { secToMs } from "./shared";

export function ConnectionCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemorySettings();
	return (
		<Card title={t("memoryPage.connection.title")} icon="link">
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadSettings" error={q.error} />
			) : q.data ? (
				<ConnectionForm settings={q.data} />
			) : null}
		</Card>
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
		<div className="flex flex-col gap-3">
			<Toggle
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
			<form
				className="flex flex-col gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					if (dirty) save();
				}}
			>
				<Field
					label={t("memoryPage.connection.url")}
					hint={t("memoryPage.connection.urlHint")}
				>
					<div className="flex min-w-0 flex-wrap gap-2">
						<TextInput
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
				</Field>
			</form>
			<MemoryError step="saveSettings" error={put.error} />
			<p className="text-faint text-xs">
				{t("memoryPage.connection.language", {
					language: settings.outputLanguage,
				})}
			</p>
			<div className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-inset px-3 py-2">
				<span className="min-w-0 text-muted text-xs">
					{settings.keysGenerated
						? t("memoryPage.connection.keysAt", {
								when: settings.keysGeneratedAt
									? formatRelative(secToMs(settings.keysGeneratedAt))
									: "—",
							})
						: t("memoryPage.connection.noKeys")}
				</span>
				<Button size="sm" icon="key" onClick={() => setEnvOpen(true)}>
					{settings.keysGenerated
						? t("memoryPage.env.regenerate")
						: t("memoryPage.env.generate")}
				</Button>
			</div>
			<EnvFileDialog
				open={envOpen}
				regenerate={settings.keysGenerated}
				onClose={() => setEnvOpen(false)}
			/>
		</div>
	);
}
