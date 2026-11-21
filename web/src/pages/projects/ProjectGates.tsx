/**
 * 推送 / 开 PR 门禁（M15，R19）：两个开关（推送门禁、PR 门禁）、修复 / 检查 / 测试命令、
 * PR 正文必需的二级标题（每行一个）与「须含 Fixes|Closes|Resolves #N」。门禁由出站代理强制执行，
 * 检查由 orb 内的 `github_prepare` 按这里的命令运行。`ProjectGates` 是受控的表单区；`ProjectGatesCard`
 * （M24，项目设置「GitHub」分组）把它放进卡片，改过才能保存，经 `PATCH projects/{id}` 的 `githubGates` 写入。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Project, unwrap } from "../../api/client";
import type { GitHubGates } from "../../api/github-queries";
import { qk } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { Card } from "../../components/ui/data";
import {
	Button,
	ErrorText,
	Field,
	TextArea,
	Toggle,
} from "../../components/ui/primitives";

/** 项目还没配置门禁时的值（默认关闭）。 */
export const DEFAULT_GATES: GitHubGates = {
	push: false,
	pr: false,
	prHeadings: [],
	requireClosingRef: false,
};

/** 保存前整理：命令去掉首尾空白、空命令省略；标题去掉 `## ` 前缀与空行。 */
export function normalizeGates(g: GitHubGates): GitHubGates {
	const cmd = (k: "fixCmd" | "checkCmd" | "testCmd") => {
		const v = g[k]?.trim();
		return v ? { [k]: v } : {};
	};
	return {
		push: g.push,
		pr: g.pr,
		...cmd("fixCmd"),
		...cmd("checkCmd"),
		...cmd("testCmd"),
		prHeadings: (g.prHeadings ?? [])
			.map((h) => h.trim().replace(/^#+\s*/, ""))
			.filter((h) => h !== ""),
		requireClosingRef: g.requireClosingRef ?? false,
	};
}

export function ProjectGates({
	value,
	onChange,
}: {
	value: GitHubGates;
	onChange: (next: GitHubGates) => void;
}): ReactNode {
	const { t } = useTranslation();
	const set = <K extends keyof GitHubGates>(k: K, v: GitHubGates[K]) =>
		onChange({ ...value, [k]: v });
	return (
		<fieldset
			data-testid="project-gates"
			className="m-0 flex min-w-0 flex-col gap-3 p-0"
		>
			<legend className="mb-1 p-0 font-medium text-sm">
				{t("github.gates.title")}
			</legend>
			<p className="text-faint text-xs">{t("github.gates.intro")}</p>
			<div className="flex flex-wrap gap-x-6 gap-y-2">
				<Toggle
					checked={value.push}
					onChange={(v) => set("push", v)}
					label={t("github.gates.push")}
				/>
				<Toggle
					checked={value.pr}
					onChange={(v) => set("pr", v)}
					label={t("github.gates.pr")}
				/>
			</div>
			<div className="grid gap-3 sm:grid-cols-2">
				<Field
					label={t("github.gates.fixCmd")}
					hint={t("github.gates.fixCmdHint")}
				>
					<TextArea
						rows={2}
						spellCheck={false}
						placeholder="bun run format"
						value={value.fixCmd ?? ""}
						onChange={(e) => set("fixCmd", e.target.value)}
					/>
				</Field>
				<Field
					label={t("github.gates.checkCmd")}
					hint={t("github.gates.checkCmdHint")}
				>
					<TextArea
						rows={2}
						spellCheck={false}
						placeholder="bun run check"
						value={value.checkCmd ?? ""}
						onChange={(e) => set("checkCmd", e.target.value)}
					/>
				</Field>
				<Field
					label={t("github.gates.testCmd")}
					hint={t("github.gates.testCmdHint")}
				>
					<TextArea
						rows={2}
						spellCheck={false}
						placeholder="bun run test"
						value={value.testCmd ?? ""}
						onChange={(e) => set("testCmd", e.target.value)}
					/>
				</Field>
				<Field
					label={t("github.gates.prHeadings")}
					hint={t("github.gates.prHeadingsHint")}
				>
					<TextArea
						rows={4}
						spellCheck={false}
						placeholder={"Repro\nCause\nFix\nVerification"}
						value={(value.prHeadings ?? []).join("\n")}
						onChange={(e) => set("prHeadings", e.target.value.split("\n"))}
					/>
				</Field>
			</div>
			<Toggle
				checked={value.requireClosingRef ?? false}
				onChange={(v) => set("requireClosingRef", v)}
				label={t("github.gates.requireClosingRef")}
			/>
		</fieldset>
	);
}

/** 门禁卡（M24）：只对绑定了 GitHub 的项目出现；没改过时「保存」不可用，「取消」交给父组件还原。 */
export function ProjectGatesCard({
	project,
	onCancel,
}: {
	project: Project;
	onCancel(): void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const save = useAsync();
	// null = 没改过
	const [gates, setGates] = useState<GitHubGates | null>(null);
	if (!project.github) return null;
	return (
		<Card>
			<div className="flex flex-col gap-3">
				<ProjectGates
					value={gates ?? project.githubGates ?? DEFAULT_GATES}
					onChange={setGates}
				/>
				<div className="flex gap-2">
					<Button
						variant="primary"
						disabled={gates === null || save.busy}
						onClick={() =>
							void save.run(async () => {
								if (!gates) return;
								await unwrap(
									api.PATCH("/api/v1/projects/{id}", {
										params: { path: { id: project.id } },
										body: { githubGates: normalizeGates(gates) },
									}),
								);
								await qc.invalidateQueries({ queryKey: qk.projects });
							})
						}
					>
						{t("common.save")}
					</Button>
					<Button onClick={onCancel}>{t("common.cancel")}</Button>
				</div>
				<ErrorText error={save.error} />
			</div>
		</Card>
	);
}
