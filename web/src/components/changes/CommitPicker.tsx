/** 范围选择器：全部改动（相对基线）| 仅未提交 | 与分支比较… | 单个 commit。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatRelative } from "../../lib/format";
import { Select } from "../ui/primitives";
import type { ChangeScope, GitCommit } from "./git";

/** 「与分支比较…」项的值：只触发分支名输入，不是一个范围。 */
const COMPARE = "base:pick";

function encode(s: ChangeScope): string {
	return s.scope === "commit" ? `commit:${s.sha}` : s.scope;
}

function decode(v: string): ChangeScope {
	if (v.startsWith("commit:")) return { scope: "commit", sha: v.slice(7) };
	return { scope: v === "uncommitted" ? "uncommitted" : "all" };
}

export function CommitPicker({
	value,
	onChange,
	onCompareBranch,
	baseName,
	commits,
	truncated,
}: {
	value: ChangeScope;
	onChange(s: ChangeScope): void;
	/** 选中「与分支比较…」：由面板弹出分支名输入；选择器保持原范围 */
	onCompareBranch(): void;
	/** 基线分支名；无基线时「全部改动」等同「仅未提交」 */
	baseName?: string;
	commits: GitCommit[];
	truncated: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const selectedSha = value.scope === "commit" ? value.sha : null;
	const known =
		selectedSha === null || commits.some((c) => c.sha === selectedSha);
	// 原 optgroup「提交」展平：组名作提交项的 hint
	const group = t("changes.scope.commits");
	return (
		<Select
			aria-label={t("changes.scope.label")}
			size="sm"
			className="min-w-0 flex-1"
			value={encode(value)}
			onChange={(v) => {
				// 受控：不调用 onChange 时，下拉框回到当前范围
				if (v === COMPARE) onCompareBranch();
				else onChange(decode(v));
			}}
			options={[
				{
					value: "all",
					label: baseName
						? t("changes.scope.allBase", { base: baseName })
						: t("changes.scope.all"),
				},
				{ value: "uncommitted", label: t("changes.scope.uncommitted") },
				{ value: COMPARE, label: t("changes.scope.compareBranch") },
				...(!known && selectedSha
					? [
							{
								value: `commit:${selectedSha}`,
								label: selectedSha.slice(0, 7),
								hint: group,
							},
						]
					: []),
				...commits.map((c) => ({
					value: `commit:${c.sha}`,
					label: `${c.shortSha} ${c.subject} · ${c.authorName} · ${formatRelative(c.ts)}`,
					hint: group,
				})),
				...(truncated
					? [
							{
								value: "",
								label: t("changes.scope.moreCommits"),
								hint: group,
								disabled: true,
							},
						]
					: []),
			]}
		/>
	);
}
