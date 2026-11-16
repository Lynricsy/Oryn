/**
 * 项目编辑器里的「推送 / 开 PR 门禁」区（M15，R19）：两个开关（推送门禁、PR 门禁）、修复 / 检查 / 测试命令、
 * PR 正文必需的二级标题（每行一个）与「须含 Fixes|Closes|Resolves #N」。门禁由出站代理强制执行，
 * 检查由 orb 内的 `github_prepare` 按这里的命令运行。受控组件：值随项目表单一起经 `PATCH projects/{id}` 的 `githubGates` 保存。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { GitHubGates } from "../../api/github-queries";
import { Field, TextArea, Toggle } from "../../components/ui/primitives";

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
