/**
 * box 页的对话框（M23 步骤 7.4）：改名、运行时段、app 的密钥。失败都留在对话框里用 `describeError` 显示，
 * 成功才关闭。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type Box,
	type BoxApp,
	useBoxSecrets,
	useDeleteBoxSecret,
	usePutBoxSecret,
	useRestartBoxApp,
	useUpdateBox,
} from "../../api/box-queries";
import { formatRelative } from "../../lib/format";
import { Badge } from "../ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../ui/primitives";
import { validBoxName } from "./box-model";

export function RenameBoxDialog({
	box,
	onClose,
}: {
	box: Box;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateBox();
	const [name, setName] = useState(box.name);
	const valid = validBoxName(name);
	const submit = () => {
		if (!valid || name === box.name) return onClose();
		update.mutate({ id: box.id, patch: { name } }, { onSuccess: onClose });
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("boxes.rename.title", { name: box.name })}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={!valid || update.isPending}
						onClick={submit}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<Field
					label={t("boxes.new.name")}
					hint={valid ? t("boxes.new.nameHint") : t("boxes.new.invalidName")}
				>
					<TextInput
						autoFocus
						value={name}
						maxLength={30}
						spellCheck={false}
						aria-invalid={!valid || undefined}
						onChange={(e) => setName(e.target.value.trim())}
					/>
				</Field>
				<ErrorText error={update.error} />
			</form>
		</Dialog>
	);
}

function timeZones(current: string): string[] {
	const intl = Intl as { supportedValuesOf?: (k: string) => string[] };
	const all = intl.supportedValuesOf?.("timeZone") ?? ["UTC"];
	return all.includes(current) ? all : [current, ...all];
}

export function ScheduleDialog({
	box,
	onClose,
}: {
	box: Box;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const update = useUpdateBox();
	const browserTz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
	const [on, setOn] = useState(box.schedule?.on ?? "08:00");
	const [off, setOff] = useState(box.schedule?.off ?? "20:00");
	const [tz, setTz] = useState(box.schedule?.tz ?? browserTz);
	const valid = /^\d\d:\d\d$/.test(on) && /^\d\d:\d\d$/.test(off) && on !== off;
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("boxes.schedule.title")}
			footer={
				<>
					{box.schedule && (
						<Button
							variant="ghost"
							className="mr-auto"
							disabled={update.isPending}
							onClick={() =>
								update.mutate(
									{ id: box.id, patch: { clearSchedule: true } },
									{ onSuccess: onClose },
								)
							}
						>
							{t("boxes.schedule.clear")}
						</Button>
					)}
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={!valid || update.isPending}
						onClick={() =>
							update.mutate(
								{ id: box.id, patch: { schedule: { on, off, tz } } },
								{ onSuccess: onClose },
							)
						}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				<p className="text-muted text-sm">{t("boxes.schedule.hint")}</p>
				<div className="grid grid-cols-2 gap-3">
					<Field label={t("boxes.schedule.on")}>
						<TextInput
							type="time"
							value={on}
							onChange={(e) => setOn(e.target.value)}
						/>
					</Field>
					<Field label={t("boxes.schedule.off")}>
						<TextInput
							type="time"
							value={off}
							onChange={(e) => setOff(e.target.value)}
						/>
					</Field>
				</div>
				<Field label={t("boxes.schedule.tz")}>
					<Select value={tz} onChange={(e) => setTz(e.target.value)}>
						{timeZones(tz).map((z) => (
							<option key={z} value={z}>
								{z}
							</option>
						))}
					</Select>
				</Field>
				<ErrorText error={update.error} />
			</div>
		</Dialog>
	);
}

export function SecretsDialog({
	box,
	app,
	onClose,
}: {
	box: Box;
	app: BoxApp;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const list = useBoxSecrets(box.id, app.name);
	const put = usePutBoxSecret();
	const del = useDeleteBoxSecret();
	const restart = useRestartBoxApp();
	const [name, setName] = useState("");
	const [value, setValue] = useState("");
	const [plain, setPlain] = useState(false);
	const nameOk = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name);
	const add = () =>
		put.mutate(
			{ box: box.id, app: app.name, name, value, secret: !plain },
			{
				onSuccess: () => {
					setName("");
					setValue("");
					setPlain(false);
				},
			},
		);
	const secrets = list.data?.secrets ?? [];
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("boxes.secrets.title", { app: app.name })}
			footer={
				<>
					<span className="mr-auto self-center text-faint text-xs">
						{t("boxes.secrets.applyHint")}
					</span>
					<Button
						disabled={!app.live || restart.isPending}
						onClick={() => restart.mutate({ box: box.id, app: app.name })}
					>
						{t("boxes.secrets.restartNow")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-4">
				{list.isPending ? (
					<Spinner />
				) : secrets.length === 0 ? (
					<p className="text-faint text-sm">{t("boxes.secrets.empty")}</p>
				) : (
					<ul data-testid="box-secrets" className="flex flex-col">
						{secrets.map((s) => (
							<li
								key={s.name}
								className="flex min-h-9 items-center gap-2 rounded-md px-2 py-1 text-sm"
							>
								<span className="font-mono text-fg">{s.name}</span>
								{s.secret ? (
									<Badge icon="lock">{t("boxes.secrets.hidden")}</Badge>
								) : (
									<span className="min-w-0 flex-1 truncate font-mono text-muted text-xs">
										{s.value}
									</span>
								)}
								<span className="ml-auto shrink-0 text-faint text-xs">
									{formatRelative(s.updatedAt)}
								</span>
								<IconButton
									icon="trash"
									box="sm"
									label={t("boxes.secrets.remove", { name: s.name })}
									disabled={del.isPending}
									onClick={() =>
										del.mutate({ box: box.id, app: app.name, name: s.name })
									}
								/>
							</li>
						))}
					</ul>
				)}
				<form
					className="flex flex-col gap-3 rounded-md bg-inset p-3"
					onSubmit={(e) => {
						e.preventDefault();
						if (nameOk) add();
					}}
				>
					<Field label={t("boxes.secrets.name")}>
						<TextInput
							value={name}
							spellCheck={false}
							placeholder="DATABASE_URL"
							className="font-mono"
							onChange={(e) => setName(e.target.value.trim())}
						/>
					</Field>
					<Field label={t("boxes.secrets.value")}>
						<TextArea
							rows={3}
							value={value}
							onChange={(e) => setValue(e.target.value)}
						/>
					</Field>
					<div className="flex flex-wrap items-center gap-3">
						<Toggle
							checked={plain}
							onChange={setPlain}
							label={t("boxes.secrets.plain")}
						/>
						<Button
							type="submit"
							variant="primary"
							size="sm"
							className="ml-auto"
							disabled={!nameOk || put.isPending}
						>
							{t("boxes.secrets.add")}
						</Button>
					</div>
				</form>
				<ErrorText error={put.error ?? del.error ?? restart.error} />
			</div>
		</Dialog>
	);
}
