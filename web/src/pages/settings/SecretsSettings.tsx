/**
 * 设置 → 密钥：全局密钥的名称列表（值只写不读）。
 * 分组右上「写入」打开对话框：名称、注入方式（proxy 经控制面代理注入，不进 orb / env 作为环境变量注入 agent）、值、
 * proxy 模式下允许注入的主机。行 = 名称与方式 · 允许的主机、更新时间 · ⋯（更新值 = 同名重写；删除，先确认）。
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Schemas, unwrap } from "../../api/client";
import { qk, useSecrets } from "../../api/queries";
import { Badge } from "../../components/ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Select,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupRow,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";

type Secret = Schemas["SecretMeta"];
type Mode = "proxy" | "env";

/** 写入对话框；`initial` 给出时是更新已有密钥（名称只读，值重新填）。 */
function SecretDialog({
	initial,
	onClose,
}: {
	initial?: Secret;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const formId = useId();
	const [name, setName] = useState(initial?.name ?? "");
	const [value, setValue] = useState("");
	const [mode, setMode] = useState<Mode>(initial?.mode ?? "proxy");
	const [hosts, setHosts] = useState(initial?.hosts?.join(", ") ?? "");
	const save = useMutation({
		mutationFn: (body: Schemas["SecretPut"]) =>
			unwrap(
				api.PUT("/api/v1/secrets/{name}", {
					params: { path: { name: name.trim() } },
					body,
				}),
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: qk.secrets }),
	});
	const envNameOk = mode !== "env" || /^[A-Z_][A-Z0-9_]*$/.test(name.trim());
	const valid = name.trim() !== "" && value !== "" && envNameOk;
	return (
		<Dialog
			open
			onClose={onClose}
			title={
				initial
					? t("secrets.updateTitle", { name: initial.name })
					: t("secrets.put")
			}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						type="submit"
						form={formId}
						variant="primary"
						disabled={!valid || save.isPending}
					>
						{save.isPending && <Spinner size={13} />}
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				id={formId}
				className="flex flex-col gap-4"
				onSubmit={(e) => {
					e.preventDefault();
					if (!valid || save.isPending) return;
					save.mutate(
						{
							value,
							mode,
							...(mode === "proxy" && hosts.trim()
								? {
										hosts: hosts
											.split(",")
											.map((h) => h.trim())
											.filter(Boolean),
									}
								: {}),
						},
						{ onSuccess: onClose },
					);
				}}
			>
				<FormField
					label={t("secrets.name")}
					aside={
						mode === "env" ? t("secrets.envNameHint") : t("secrets.nameHint")
					}
				>
					{(p) => (
						<TextInput
							{...p}
							required
							readOnly={!!initial}
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							value={name}
							onChange={(e) => setName(e.target.value)}
						/>
					)}
				</FormField>
				<FormField label={t("secrets.mode")}>
					{(p) => (
						<Select<Mode>
							{...p}
							value={mode}
							onChange={setMode}
							options={[
								{ value: "proxy", label: t("secrets.modeProxy") },
								{ value: "env", label: t("secrets.modeEnv") },
							]}
						/>
					)}
				</FormField>
				<FormField label={t("secrets.value")}>
					{(p) => (
						<TextInput
							{...p}
							required
							type="password"
							autoComplete="off"
							value={value}
							onChange={(e) => setValue(e.target.value)}
						/>
					)}
				</FormField>
				{mode === "proxy" && (
					<FormField
						label={t("secrets.hosts")}
						optional={t("secrets.optional")}
						aside={t("secrets.hostsHint")}
					>
						{(p) => (
							<TextInput
								{...p}
								spellCheck={false}
								value={hosts}
								onChange={(e) => setHosts(e.target.value)}
							/>
						)}
					</FormField>
				)}
				<ErrorText error={save.error} />
			</form>
		</Dialog>
	);
}

function SecretRow({
	secret: s,
	onUpdate,
}: {
	secret: Secret;
	onUpdate: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const del = useMutation({
		mutationFn: () =>
			unwrap(
				api.DELETE("/api/v1/secrets/{name}", {
					params: { path: { name: s.name } },
				}),
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: qk.secrets }),
	});
	return (
		<GroupRow
			icon="key"
			title={
				<>
					<span className="truncate font-mono text-[13px]">{s.name}</span>
					<Badge tone={s.mode === "env" ? "frost" : "neutral"}>{s.mode}</Badge>
				</>
			}
			meta={
				<>
					{s.hosts?.map((h) => (
						<span key={h} className="font-mono text-xs">
							{h}
						</span>
					))}
					<span>{formatRelative(s.updatedAt)}</span>
				</>
			}
			trailing={
				<Menu
					trigger={(p) => (
						<IconButton
							icon="more"
							box="sm"
							label={t("secrets.more", { name: s.name })}
							{...p}
						/>
					)}
					items={[
						{
							key: "update",
							icon: "edit",
							label: t("secrets.update"),
							onSelect: onUpdate,
						},
						{
							key: "delete",
							icon: "trash",
							label: t("common.delete"),
							danger: true,
							disabled: del.isPending,
							onSelect: async () => {
								if (
									await confirmAction({
										title: t("secrets.deleteConfirm", { name: s.name }),
										action: t("common.delete"),
										danger: true,
									})
								)
									del.mutate();
							},
						},
					]}
				/>
			}
		>
			{del.error ? <ErrorText error={del.error} /> : undefined}
		</GroupRow>
	);
}

export default function SecretsSettings(): ReactNode {
	const { t } = useTranslation();
	const secrets = useSecrets();
	const [editing, setEditing] = useState<Secret | "new" | null>(null);
	const list = secrets.data?.secrets ?? [];
	return (
		<Group
			title={t("secrets.title")}
			action={
				<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
					{t("secrets.add")}
				</Button>
			}
		>
			{secrets.isPending && <Spinner />}
			<ErrorText error={secrets.error} />
			{secrets.isSuccess && list.length === 0 && (
				<p className="px-1.5 text-faint text-sm">{t("secrets.empty")}</p>
			)}
			{list.length > 0 && (
				<GroupList>
					{list.map((s) => (
						<SecretRow key={s.name} secret={s} onUpdate={() => setEditing(s)} />
					))}
				</GroupList>
			)}
			{editing && (
				<SecretDialog
					initial={editing === "new" ? undefined : editing}
					onClose={() => setEditing(null)}
				/>
			)}
		</Group>
	);
}
