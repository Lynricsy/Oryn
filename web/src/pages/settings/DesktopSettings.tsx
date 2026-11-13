/**
 * 设置 → 桌面应用（M6 计划 6.3，仅桌面壳内可见）：版本与更新、下载后自动安装、全局快捷键、关闭到托盘、
 * 令牌存储位置与更换服务器。数据全部来自壳（`host.desktop`）；壳推来 `update` 事件后重新取一次。
 */
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Callout, Card, KeyValue } from "../../components/ui/data";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import {
	type DesktopInfo,
	host,
	type DesktopSettings as Settings,
} from "../../lib/host";

function UpdateRow({
	info,
	onChanged,
}: {
	info: DesktopInfo;
	onChanged: () => void;
}) {
	const { t } = useTranslation();
	const desktop = host.desktop;
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	/** 最近一次手动检查的结论：没有新版本时显示「已是最新」。 */
	const [upToDate, setUpToDate] = useState(false);
	if (!desktop) return null;
	const { state, version, error: updateError } = info.update;

	const act = async (fn: () => Promise<unknown>) => {
		setBusy(true);
		setError(null);
		try {
			await fn();
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	const check = () =>
		act(async () => {
			const found = await desktop.checkUpdate();
			setUpToDate(!found);
			onChanged();
		});

	return (
		<div className="flex flex-col gap-1.5">
			{state === "ready" ? (
				<div>
					<Button
						variant="primary"
						disabled={busy}
						onClick={() => void act(() => desktop.installUpdate())}
					>
						{busy ? (
							<Spinner />
						) : (
							t("desktop.restartToUpdate", { version: version ?? "" })
						)}
					</Button>
				</div>
			) : state === "available" || state === "downloading" ? (
				<p className="inline-flex items-center gap-1.5 text-muted text-sm">
					<Spinner size={12} />
					{t(
						state === "available" ? "desktop.available" : "desktop.downloading",
						{ version: version ?? "" },
					)}
				</p>
			) : (
				<div className="flex flex-wrap items-center gap-2">
					<Button size="sm" disabled={busy} onClick={() => void check()}>
						{busy ? <Spinner size={12} /> : t("desktop.checkUpdate")}
					</Button>
					{state === "none" && upToDate && !busy && (
						<span className="text-faint text-xs">{t("desktop.upToDate")}</span>
					)}
				</div>
			)}
			{state === "error" && updateError && (
				<p className="text-err text-xs">
					{t("desktop.updateError", { error: updateError })}
				</p>
			)}
			<ErrorText error={error} />
		</div>
	);
}

function ShortcutField({
	info,
	save,
	busy,
}: {
	info: DesktopInfo;
	save: (patch: Partial<Settings>) => void;
	busy: boolean;
}) {
	const { t } = useTranslation();
	const saved = info.settings.shortcut;
	const [draft, setDraft] = useState(saved);
	useEffect(() => setDraft(saved), [saved]);
	const submit = (e: FormEvent) => {
		e.preventDefault();
		save({ shortcut: draft.trim() });
	};
	return (
		<form className="flex flex-col gap-1.5" onSubmit={submit}>
			<Field label={t("desktop.shortcut")} hint={t("desktop.shortcutHint")}>
				<div className="flex gap-2">
					<TextInput
						className="min-w-0 flex-1 font-mono"
						spellCheck={false}
						autoCapitalize="off"
						placeholder="Control+Alt+Space"
						value={draft}
						onChange={(e) => setDraft(e.target.value)}
					/>
					<Button type="submit" disabled={busy || draft.trim() === saved}>
						{t("common.save")}
					</Button>
				</div>
			</Field>
			{info.shortcutError && (
				<p role="alert" className="text-err text-xs">
					{t("desktop.shortcutError", { error: info.shortcutError })}
				</p>
			)}
		</form>
	);
}

export default function DesktopSettings(): ReactNode {
	const { t } = useTranslation();
	const desktop = host.desktop;
	const [info, setInfo] = useState<DesktopInfo | null>(null);
	const [error, setError] = useState<unknown>(null);
	const [saving, setSaving] = useState(false);

	useEffect(() => {
		const d = host.desktop;
		if (!d) return;
		let live = true;
		const refresh = () =>
			d.info().then(
				(r) => {
					if (live) setInfo(r);
				},
				(e) => {
					if (live) setError(e);
				},
			);
		void refresh();
		const off = host.onUpdate(() => void refresh());
		return () => {
			live = false;
			off();
		};
	}, []);

	if (!desktop) return null;

	const refresh = () => {
		void desktop.info().then(setInfo, setError);
	};

	const save = (patch: Partial<Settings>) => {
		setSaving(true);
		setError(null);
		desktop
			.setSettings(patch)
			.then(setInfo, setError)
			.finally(() => setSaving(false));
	};

	return (
		<Card title={t("desktop.title")} icon="monitor">
			{!info ? (
				error ? (
					<ErrorText error={error} />
				) : (
					<Spinner />
				)
			) : (
				<div className="flex flex-col gap-4">
					<KeyValue
						rows={[
							{
								key: "version",
								label: t("desktop.version"),
								value: <span className="font-mono">{info.version}</span>,
							},
							{
								key: "update",
								label: t("desktop.update"),
								value: <UpdateRow info={info} onChanged={refresh} />,
							},
							{
								key: "tokenStore",
								label: t("desktop.tokenStore"),
								value: t(`desktop.tokenStoreKind.${info.tokenStore}`),
							},
						]}
					/>
					{info.tokenStore === "file" && (
						<Callout tone="warn">{t("desktop.fileStore")}</Callout>
					)}
					<div className="flex flex-col gap-3 border-border border-t pt-4">
						<Toggle
							checked={info.settings.autoUpdate}
							disabled={saving}
							onChange={(v) => save({ autoUpdate: v })}
							label={t("desktop.autoUpdate")}
						/>
						<Toggle
							checked={info.settings.closeToTray}
							disabled={saving}
							onChange={(v) => save({ closeToTray: v })}
							label={t("desktop.closeToTray")}
						/>
						<ShortcutField info={info} save={save} busy={saving} />
						<ErrorText error={error} />
					</div>
					<div className="flex flex-col gap-1.5 border-border border-t pt-4">
						<div>
							<Button variant="danger" onClick={() => desktop.changeServer()}>
								{t("desktop.changeServer")}
							</Button>
						</div>
						<p className="text-faint text-xs">
							{t("desktop.changeServerHint")}
						</p>
					</div>
				</div>
			)}
		</Card>
	);
}
