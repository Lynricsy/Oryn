/**
 * 设置 → Git 与 GitHub 的「提交者」分组：orb 内 git 提交默认使用的 user.name / user.email（全局默认值，项目设置里可覆盖），
 * 一块表单面板，可保存或清除。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Schemas, unwrap } from "../../api/client";
import { qk, useGitIdentity } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { Callout } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	ErrorText,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { FormField, Group, GroupPanel } from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";

function GitIdentityForm({
	initial,
}: {
	initial?: Schemas["GitIdentity"];
}): ReactNode {
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
			className="flex flex-col gap-3"
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
			<div className="grid gap-3 sm:grid-cols-2">
				<FormField label={t("gitIdentity.name")}>
					{(p) => (
						<TextInput
							{...p}
							required
							maxLength={200}
							autoComplete="name"
							value={name}
							onChange={(e) => {
								setName(e.target.value);
								setSaved(false);
							}}
						/>
					)}
				</FormField>
				<FormField label={t("gitIdentity.email")}>
					{(p) => (
						<TextInput
							{...p}
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
					)}
				</FormField>
			</div>
			<div className="flex flex-wrap items-center gap-2">
				<Button
					type="submit"
					variant="primary"
					disabled={save.busy || !dirty || !name.trim() || !email.includes("@")}
				>
					{t("common.save")}
				</Button>
				{initial && (
					<Button
						variant="ghost"
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

export default function GitIdentitySettings(): ReactNode {
	const { t } = useTranslation();
	const q = useGitIdentity();
	const id = q.data?.identity;
	return (
		<Group title={t("gitIdentity.title")}>
			<GroupPanel>
				{q.isPending && <Spinner />}
				<ErrorText error={q.error} />
				{q.data && (
					<>
						{/* 「当前：name <email>」须是同一个文本节点（测试与验收脚本按整句查找）；没设置时必须看到，用警示 */}
						{id ? (
							<p className="flex items-center gap-1.5 text-[13px] text-muted">
								<Icon name="user" size={13} aria-hidden className="shrink-0" />
								<span className="min-w-0 truncate">
									{t("gitIdentity.current", { name: id.name, email: id.email })}
								</span>
							</p>
						) : (
							<Callout tone="warn">{t("gitIdentity.notSet")}</Callout>
						)}
						<GitIdentityForm initial={id} />
					</>
				)}
				<p className="border-border border-t pt-2.5 text-[12.5px] text-muted">
					{t("gitIdentity.hint")}
				</p>
			</GroupPanel>
		</Group>
	);
}
