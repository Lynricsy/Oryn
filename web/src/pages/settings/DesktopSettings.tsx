/**
 * 设置 → 桌面应用（M6 计划 6.3，仅桌面壳内可见），一个设置一行：
 * 1. 更新：版本与更新（检查 / 下载进度 / 重启以更新）、下载后自动安装。
 * 2. 窗口：关闭到托盘、全局快捷键（对话框里修改）。
 * 3. 安全：令牌存储位置（存在文件里时给警告）。
 * 更换服务器在「账户与设备」。数据全部来自壳（`host.desktop`）；壳推来 `update` 事件后重新取一次。
 */
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Callout } from "../../components/ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Kbd,
	Spinner,
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
import {
	type DesktopInfo,
	host,
	type DesktopSettings as Settings,
} from "../../lib/host";

/** 「版本与更新」一行：行说明是更新状态，行尾是当下能做的事（检查 / 重启以更新）。 */
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

	const meta =
		state === "available" || state === "downloading" ? (
			<span className="inline-flex items-center gap-1.5">
				<Spinner size={12} />
				{t(
					state === "available" ? "desktop.available" : "desktop.downloading",
					{ version: version ?? "" },
				)}
			</span>
		) : state === "error" && updateError ? (
			<span className="text-err">
				{t("desktop.updateError", { error: updateError })}
			</span>
		) : state === "none" && upToDate && !busy ? (
			t("desktop.upToDate")
		) : undefined;

	const trailing =
		state === "ready" ? (
			<Button
				size="sm"
				variant="primary"
				disabled={busy}
				onClick={() => void act(() => desktop.installUpdate())}
			>
				{busy ? (
					<Spinner size={12} />
				) : (
					t("desktop.restartToUpdate", { version: version ?? "" })
				)}
			</Button>
		) : state === "available" || state === "downloading" ? undefined : (
			<Button size="sm" disabled={busy} onClick={() => void check()}>
				{busy ? <Spinner size={12} /> : t("desktop.checkUpdate")}
			</Button>
		);

	return (
		<GroupRow
			icon="download"
			title={
				<>
					<span>{t("desktop.version")}</span>
					<span className="font-mono font-normal text-muted">
						{info.version}
					</span>
				</>
			}
			meta={meta}
			trailing={trailing}
		>
			{error ? <ErrorText error={error} /> : null}
		</GroupRow>
	);
}

/** 修改全局快捷键的对话框：去掉首尾空白后提交，留空 = 关闭。 */
function ShortcutDialog({
	saved,
	busy,
	onSave,
	onClose,
}: {
	saved: string;
	busy: boolean;
	onSave: (shortcut: string) => void;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const [draft, setDraft] = useState(saved);
	const submit = (e: FormEvent) => {
		e.preventDefault();
		onSave(draft.trim());
	};
	const formId = "desktop-shortcut-form";
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("desktop.shortcut")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						type="submit"
						form={formId}
						variant="primary"
						disabled={busy || draft.trim() === saved}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form id={formId} onSubmit={submit}>
				<FormField
					label={t("desktop.shortcut")}
					aside={t("desktop.shortcutAside")}
				>
					{(p) => (
						<TextInput
							{...p}
							autoFocus
							className="font-mono"
							spellCheck={false}
							autoCapitalize="off"
							placeholder="Control+Alt+Space"
							value={draft}
							onChange={(e) => setDraft(e.target.value)}
						/>
					)}
				</FormField>
			</form>
		</Dialog>
	);
}

function ShortcutRow({
	info,
	save,
	busy,
}: {
	info: DesktopInfo;
	save: (patch: Partial<Settings>) => Promise<boolean>;
	busy: boolean;
}) {
	const { t } = useTranslation();
	const [editing, setEditing] = useState(false);
	const saved = info.settings.shortcut;
	return (
		<GroupRow
			icon="keyboard"
			title={t("desktop.shortcut")}
			meta={t("desktop.shortcutHint")}
			trailing={
				<>
					{saved ? (
						<Kbd>{saved}</Kbd>
					) : (
						<span className="text-[12.5px] text-muted">
							{t("desktop.shortcutOff")}
						</span>
					)}
					<Button size="sm" onClick={() => setEditing(true)}>
						{t("desktop.shortcutEdit")}
					</Button>
					{editing && (
						<ShortcutDialog
							saved={saved}
							busy={busy}
							onClose={() => setEditing(false)}
							onSave={(shortcut) =>
								void save({ shortcut }).then((ok) => {
									if (ok) setEditing(false);
								})
							}
						/>
					)}
				</>
			}
		>
			{info.shortcutError && (
				<p role="alert" className="text-[12.5px] text-err">
					{t("desktop.shortcutError", { error: info.shortcutError })}
				</p>
			)}
		</GroupRow>
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
	if (!info)
		return (
			<GroupPanel>
				{error ? <ErrorText error={error} /> : <Spinner />}
			</GroupPanel>
		);

	const refresh = () => {
		void desktop.info().then(setInfo, setError);
	};

	/** 提交一项设置；返回是否成功（快捷键对话框据此关闭）。 */
	const save = async (patch: Partial<Settings>): Promise<boolean> => {
		setSaving(true);
		setError(null);
		try {
			setInfo(await desktop.setSettings(patch));
			return true;
		} catch (e) {
			setError(e);
			return false;
		} finally {
			setSaving(false);
		}
	};

	return (
		<>
			<Group title={t("desktop.groups.update")}>
				<GroupList>
					<UpdateRow info={info} onChanged={refresh} />
					<GroupRow
						icon="refresh"
						title={t("desktop.autoUpdate")}
						meta={t("desktop.autoUpdateHint")}
						trailing={
							<Toggle
								hideLabel
								checked={info.settings.autoUpdate}
								disabled={saving}
								onChange={(v) => void save({ autoUpdate: v })}
								label={t("desktop.autoUpdate")}
							/>
						}
					/>
				</GroupList>
			</Group>
			<Group title={t("desktop.groups.window")}>
				<GroupList>
					<GroupRow
						icon="tray"
						title={t("desktop.closeToTray")}
						meta={t("desktop.closeToTrayHint")}
						trailing={
							<Toggle
								hideLabel
								checked={info.settings.closeToTray}
								disabled={saving}
								onChange={(v) => void save({ closeToTray: v })}
								label={t("desktop.closeToTray")}
							/>
						}
					/>
					<ShortcutRow info={info} save={save} busy={saving} />
				</GroupList>
				<ErrorText error={error} />
			</Group>
			<Group title={t("desktop.groups.security")}>
				<GroupList>
					<GroupRow
						icon="lock"
						title={t("desktop.tokenStore")}
						meta={t("desktop.tokenStoreHint")}
						trailing={
							<span className="text-[12.5px] text-muted">
								{t(`desktop.tokenStoreKind.${info.tokenStore}`)}
							</span>
						}
					>
						{info.tokenStore === "file" && (
							<Callout tone="warn">{t("desktop.fileStore")}</Callout>
						)}
					</GroupRow>
				</GroupList>
			</Group>
		</>
	);
}
