/** 设置页「密钥」（default export，由设置页接入）：写入值（代理 / 环境变量两种模式）、名称列表与删除。 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Schemas, unwrap } from "../../api/client";
import { qk, useSecrets } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { Badge, Card, ListRow } from "../../components/ui/data";
import {
	Button,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";
import { LeadIcon, RowList } from "./parts";

export default function SecretsSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const secrets = useSecrets();
	const [name, setName] = useState("");
	const [value, setValue] = useState("");
	const [mode, setMode] = useState<"proxy" | "env">("proxy");
	const [hosts, setHosts] = useState("");
	const save = useAsync();
	const del = useAsync();
	const envNameOk = mode !== "env" || /^[A-Z_][A-Z0-9_]*$/.test(name);
	return (
		<>
			<Card title={t("secrets.put")} icon="plus">
				<form
					className="grid gap-3 sm:grid-cols-2"
					onSubmit={async (e) => {
						e.preventDefault();
						const body: Schemas["SecretPut"] = {
							value,
							mode,
							...(hosts.trim()
								? {
										hosts: hosts
											.split(",")
											.map((h) => h.trim())
											.filter(Boolean),
									}
								: {}),
						};
						const ok = await save.run(() =>
							unwrap(
								api.PUT("/api/v1/secrets/{name}", {
									params: { path: { name: name.trim() } },
									body,
								}),
							),
						);
						if (ok) {
							setValue("");
							setName("");
							void qc.invalidateQueries({ queryKey: qk.secrets });
						}
					}}
				>
					<Field
						label={t("secrets.name")}
						hint={
							mode === "env" ? t("secrets.envNameHint") : t("secrets.nameHint")
						}
					>
						<TextInput
							required
							value={name}
							onChange={(e) => setName(e.target.value)}
						/>
					</Field>
					<Field label={t("secrets.mode")}>
						<Select
							value={mode}
							onChange={(e) => setMode(e.target.value as "proxy" | "env")}
						>
							<option value="proxy">{t("secrets.modeProxy")}</option>
							<option value="env">{t("secrets.modeEnv")}</option>
						</Select>
					</Field>
					<div className="sm:col-span-2">
						<Field label={t("secrets.value")}>
							<TextInput
								required
								type="password"
								autoComplete="off"
								value={value}
								onChange={(e) => setValue(e.target.value)}
							/>
						</Field>
					</div>
					{mode === "proxy" && (
						<div className="sm:col-span-2">
							<Field label={t("secrets.hosts")} hint={t("secrets.hostsHint")}>
								<TextInput
									value={hosts}
									onChange={(e) => setHosts(e.target.value)}
								/>
							</Field>
						</div>
					)}
					<div>
						<Button
							type="submit"
							variant="primary"
							disabled={save.busy || !name.trim() || !value || !envNameOk}
						>
							{t("common.save")}
						</Button>
					</div>
					<ErrorText error={save.error} />
				</form>
			</Card>
			<Card title={t("secrets.title")} icon="key">
				{secrets.isPending && <Spinner />}
				<ErrorText error={secrets.error ?? del.error} />
				<RowList>
					{secrets.data?.secrets.map((s) => (
						<li key={s.name}>
							<ListRow
								leading={<LeadIcon name="key" />}
								title={
									<>
										<span className="truncate font-mono">{s.name}</span>
										<Badge tone={s.mode === "env" ? "frost" : "neutral"}>
											{s.mode}
										</Badge>
									</>
								}
								meta={
									<>
										{s.hosts?.map((h) => (
											<Badge key={h} icon="globe">
												{h}
											</Badge>
										))}
										<span>{formatRelative(s.updatedAt)}</span>
									</>
								}
								trailing={
									<IconButton
										icon="trash"
										label={t("common.delete")}
										onClick={async () => {
											if (
												!(await confirmAction({
													title: t("secrets.deleteConfirm", { name: s.name }),
													action: t("common.delete"),
													danger: true,
												}))
											)
												return;
											void del
												.run(() =>
													unwrap(
														api.DELETE("/api/v1/secrets/{name}", {
															params: { path: { name: s.name } },
														}),
													),
												)
												.then(() =>
													qc.invalidateQueries({ queryKey: qk.secrets }),
												);
										}}
									/>
								}
							/>
						</li>
					))}
				</RowList>
				{secrets.data?.secrets.length === 0 && (
					<p className="text-faint text-sm">{t("secrets.empty")}</p>
				)}
			</Card>
		</>
	);
}
