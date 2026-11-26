/** 设置页「Git 身份」（default export，由设置页接入）：orb 内提交身份的全局默认值，可保存或清除。 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Schemas, unwrap } from "../../api/client";
import { qk, useGitIdentity } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { Callout, Card } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";

function GitIdentityForm({ initial }: { initial?: Schemas["GitIdentity"] }) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [name, setName] = useState(initial?.name ?? "");
	const [email, setEmail] = useState(initial?.email ?? "");
	const [saved, setSaved] = useState(false);
	const save = useAsync();
	const clear = useAsync();
	const dirty =
		name.trim() !== (initial?.name ?? "") ||
		email.trim() !== (initial?.email ?? "");
	return (
		<form
			className="grid gap-3 sm:grid-cols-2"
			onSubmit={async (e) => {
				e.preventDefault();
				setSaved(false);
				await save.run(async () => {
					const res = await unwrap(
						api.PUT("/api/v1/settings/git-identity", {
							body: { name: name.trim(), email: email.trim() },
						}),
					);
					// 页面打开时发出的旧读取后到会把缓存还原，先取消再写回
					await qc.cancelQueries({ queryKey: qk.gitIdentity });
					qc.setQueryData(qk.gitIdentity, res);
					setName(res.identity?.name ?? "");
					setEmail(res.identity?.email ?? "");
					setSaved(true);
				});
			}}
		>
			<Field label={t("gitIdentity.name")}>
				<TextInput
					required
					maxLength={200}
					autoComplete="name"
					value={name}
					onChange={(e) => {
						setName(e.target.value);
						setSaved(false);
					}}
				/>
			</Field>
			<Field label={t("gitIdentity.email")}>
				<TextInput
					required
					type="email"
					maxLength={320}
					autoComplete="email"
					value={email}
					onChange={(e) => {
						setEmail(e.target.value);
						setSaved(false);
					}}
				/>
			</Field>
			<div className="flex items-center gap-2 sm:col-span-2">
				<Button
					type="submit"
					variant="primary"
					disabled={save.busy || !dirty || !name.trim() || !email.includes("@")}
				>
					{t("common.save")}
				</Button>
				{initial && (
					<Button
						disabled={clear.busy}
						onClick={async () => {
							if (
								!(await confirmAction({
									title: t("gitIdentity.clearConfirm"),
									body: t("gitIdentity.clearConfirmBody"),
									action: t("common.clear"),
									danger: true,
								}))
							)
								return;
							setSaved(false);
							void clear.run(async () => {
								await unwrap(api.DELETE("/api/v1/settings/git-identity"));
								await qc.cancelQueries({ queryKey: qk.gitIdentity });
								qc.setQueryData(qk.gitIdentity, {});
								setName("");
								setEmail("");
							});
						}}
					>
						{t("common.clear")}
					</Button>
				)}
				{saved && !dirty && (
					<span className="inline-flex items-center gap-1 text-ok text-xs">
						<Icon name="check" size={13} weight="bold" />
						<span>{t("gitIdentity.saved")}</span>
					</span>
				)}
			</div>
			<ErrorText error={save.error ?? clear.error} />
		</form>
	);
}

export default function GitIdentitySettings() {
	const { t } = useTranslation();
	const q = useGitIdentity();
	const id = q.data?.identity;
	return (
		<Card title={t("gitIdentity.title")} icon="fork">
			<p className="mb-3 text-muted text-xs">{t("gitIdentity.hint")}</p>
			{q.isPending && <Spinner />}
			<ErrorText error={q.error} />
			{q.data && (
				<>
					<div className="mb-3">
						{/* 「当前：name <email>」须是同一个文本节点（测试与验收脚本按整句查找） */}
						<Callout
							tone={id ? "neutral" : "warn"}
							icon={id ? "user" : undefined}
						>
							{id
								? t("gitIdentity.current", { name: id.name, email: id.email })
								: t("gitIdentity.notSet")}
						</Callout>
					</div>
					<GitIdentityForm initial={id} />
				</>
			)}
		</Card>
	);
}
