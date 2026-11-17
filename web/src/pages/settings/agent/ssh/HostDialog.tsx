/**
 * 新建 / 编辑 SSH 主机。项目卡片里打开时固定 `projectId`（作用域建后不可改）。
 * 跳板候选按服务端规则：全局主机只能经全局主机，项目主机可经全局或同项目的主机；排除自己。
 * 服务端 400 / 409 的文案原样显示在表单底部。编辑时密码留空 = 沿用原密码。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type SshHost,
	type SshHostCreate,
	useCreateSshHost,
	useSshKeys,
	useUpdateSshHost,
} from "../../../../api/ssh-queries";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	Select,
	Spinner,
	TextInput,
	Toggle,
} from "../../../../components/ui/primitives";
import { KeyDialog } from "./KeysCard";

type Compat = "default" | "on" | "off";

export function HostDialog({
	host,
	projectId,
	hosts,
	onClose,
	onSaved,
}: {
	/** 缺省 = 新建 */
	host?: SshHost;
	/** 新建时的作用域；编辑时取 `host.projectId` */
	projectId?: string;
	/** 全部主机（选跳板用） */
	hosts: SshHost[];
	onClose: () => void;
	onSaved: (host: SshHost, created: boolean) => void;
}): ReactNode {
	const { t } = useTranslation();
	const keys = useSshKeys();
	const create = useCreateSshHost();
	const update = useUpdateSshHost();
	const scope = host ? host.projectId : projectId;
	const [name, setName] = useState(host?.name ?? "");
	const [address, setAddress] = useState(host?.address ?? "");
	const [port, setPort] = useState(String(host?.port ?? 22));
	const [username, setUsername] = useState(host?.username ?? "");
	const [authType, setAuthType] = useState(host?.authType ?? "key");
	const [keyId, setKeyId] = useState(host?.keyId ?? "");
	const [password, setPassword] = useState("");
	const [jumpHostId, setJumpHostId] = useState(host?.jumpHostId ?? "");
	const [description, setDescription] = useState(host?.description ?? "");
	const [tags, setTags] = useState(host?.tags.join(", ") ?? "");
	const [enabled, setEnabled] = useState(host?.enabled ?? true);
	const [allowAutomation, setAllowAutomation] = useState(
		host?.allowAutomation ?? false,
	);
	const [allowForward, setAllowForward] = useState(host?.allowForward ?? false);
	const [compat, setCompat] = useState<Compat>(
		host?.compat === undefined ? "default" : host.compat ? "on" : "off",
	);
	const [newKey, setNewKey] = useState(false);
	const pending = create.isPending || update.isPending;
	const error = create.error ?? update.error;
	const jumps = hosts.filter(
		(h) =>
			h.id !== host?.id &&
			(h.projectId === undefined || (scope && h.projectId === scope)),
	);
	const keyList = keys.data?.keys ?? [];

	const submit = () => {
		const tagList = tags
			.split(/[,，]/)
			.map((s) => s.trim())
			.filter(Boolean);
		const common = {
			name: name.trim(),
			address: address.trim(),
			port: Number(port) || 22,
			username: username.trim(),
			authType,
			...(authType === "key" && keyId ? { keyId } : {}),
			...(authType === "password" && password ? { password } : {}),
			description: description.trim(),
			tags: tagList,
			enabled,
			allowAutomation,
			allowForward,
		};
		if (host) {
			update.mutate(
				{
					id: host.id,
					patch: {
						...common,
						jumpHostId: jumpHostId || null,
						compat: compat === "default" ? null : compat === "on",
					},
				},
				{ onSuccess: (h) => onSaved(h, false) },
			);
			return;
		}
		const body: SshHostCreate = {
			...common,
			...(scope ? { projectId: scope } : {}),
			...(jumpHostId ? { jumpHostId } : {}),
			...(compat === "default" ? {} : { compat: compat === "on" }),
		};
		create.mutate(body, { onSuccess: (h) => onSaved(h, true) });
	};

	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={
				host
					? t("sshPage.hostDialog.editTitle", { name: host.name })
					: t("sshPage.hostDialog.createTitle")
			}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						type="submit"
						form="ssh-host-form"
						disabled={pending}
					>
						{pending && <Spinner size={12} />}
						{host ? t("common.save") : t("sshPage.hostDialog.create")}
					</Button>
				</>
			}
		>
			<form
				id="ssh-host-form"
				className="grid gap-3 sm:grid-cols-2"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<Field
					label={t("sshPage.hostDialog.name")}
					hint={t("sshPage.hostDialog.nameHint")}
				>
					<TextInput
						value={name}
						onChange={(e) => setName(e.target.value)}
						spellCheck={false}
						autoFocus
					/>
				</Field>
				<Field label={t("sshPage.hostDialog.username")}>
					<TextInput
						value={username}
						onChange={(e) => setUsername(e.target.value)}
						spellCheck={false}
						autoComplete="off"
					/>
				</Field>
				<Field label={t("sshPage.hostDialog.address")}>
					<TextInput
						value={address}
						onChange={(e) => setAddress(e.target.value)}
						spellCheck={false}
						placeholder="10.0.0.12"
					/>
				</Field>
				<Field label={t("sshPage.hostDialog.port")}>
					<TextInput
						type="number"
						min={1}
						max={65535}
						value={port}
						onChange={(e) => setPort(e.target.value)}
					/>
				</Field>
				<Field label={t("sshPage.hostDialog.authType")}>
					<Select
						value={authType}
						onChange={(e) => setAuthType(e.target.value as "key" | "password")}
					>
						<option value="key">{t("sshPage.hostDialog.authKey")}</option>
						<option value="password">
							{t("sshPage.hostDialog.authPassword")}
						</option>
					</Select>
				</Field>
				{authType === "key" ? (
					// 「新建钥匙」放在 label 外：label 只关联下拉框
					<div className="flex items-end gap-2">
						<div className="min-w-0 flex-1">
							<Field label={t("sshPage.hostDialog.key")}>
								<Select
									value={keyId}
									onChange={(e) => setKeyId(e.target.value)}
								>
									<option value="">{t("sshPage.hostDialog.pickKey")}</option>
									{keyList.map((k) => (
										<option key={k.id} value={k.id}>
											{k.name}
										</option>
									))}
								</Select>
							</Field>
						</div>
						<Button icon="plus" onClick={() => setNewKey(true)}>
							{t("sshPage.hostDialog.newKey")}
						</Button>
					</div>
				) : (
					<Field label={t("sshPage.hostDialog.password")}>
						<TextInput
							type="password"
							autoComplete="new-password"
							value={password}
							onChange={(e) => setPassword(e.target.value)}
							placeholder={
								host?.hasPassword
									? t("sshPage.hostDialog.passwordKeep")
									: undefined
							}
						/>
					</Field>
				)}
				<Field
					label={t("sshPage.hostDialog.jump")}
					hint={t("sshPage.hostDialog.jumpHint")}
				>
					<Select
						value={jumpHostId}
						onChange={(e) => setJumpHostId(e.target.value)}
					>
						<option value="">{t("common.none")}</option>
						{jumps.map((h) => (
							<option key={h.id} value={h.id}>
								{h.name}
								{h.enabled ? "" : ` (${t("sshPage.row.disabled")})`}
							</option>
						))}
					</Select>
				</Field>
				<Field
					label={t("sshPage.hostDialog.compat")}
					hint={t("sshPage.hostDialog.compatHint")}
				>
					<Select
						value={compat}
						onChange={(e) => setCompat(e.target.value as Compat)}
					>
						<option value="default">
							{t("sshPage.hostDialog.compatDefault")}
						</option>
						<option value="on">{t("sshPage.hostDialog.compatOn")}</option>
						<option value="off">{t("sshPage.hostDialog.compatOff")}</option>
					</Select>
				</Field>
				<div className="sm:col-span-2">
					<Field label={t("sshPage.hostDialog.description")}>
						<TextInput
							value={description}
							onChange={(e) => setDescription(e.target.value)}
							maxLength={500}
						/>
					</Field>
				</div>
				<div className="sm:col-span-2">
					<Field
						label={t("sshPage.hostDialog.tags")}
						hint={t("sshPage.hostDialog.tagsHint")}
					>
						<TextInput
							value={tags}
							onChange={(e) => setTags(e.target.value)}
							spellCheck={false}
						/>
					</Field>
				</div>
				<div className="flex flex-col gap-2 sm:col-span-2">
					<Toggle
						checked={enabled}
						onChange={setEnabled}
						label={t("sshPage.hostDialog.enabled")}
					/>
					<Toggle
						checked={allowAutomation}
						onChange={setAllowAutomation}
						label={t("sshPage.hostDialog.allowAutomation")}
					/>
					<Toggle
						checked={allowForward}
						onChange={setAllowForward}
						label={t("sshPage.hostDialog.allowForward")}
					/>
				</div>
				<div className="sm:col-span-2">
					<ErrorText error={error} />
				</div>
			</form>
			{newKey && (
				<KeyDialog
					mode="generate"
					onClose={() => setNewKey(false)}
					onCreated={(k) => setKeyId(k.id)}
				/>
			)}
		</Dialog>
	);
}
