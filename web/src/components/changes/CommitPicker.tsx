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
	return (
		<Select
			aria-label={t("changes.scope.label")}
			wrapperClassName="min-w-0 flex-1"
			className="h-7 py-0 text-xs"
			value={encode(value)}
			onChange={(e) => {
				// 受控：不调用 onChange 时，下拉框回到当前范围
				if (e.target.value === COMPARE) onCompareBranch();
				else onChange(decode(e.target.value));
			}}
		>
			<option value="all">
				{baseName
					? t("changes.scope.allBase", { base: baseName })
					: t("changes.scope.all")}
			</option>
			<option value="uncommitted">{t("changes.scope.uncommitted")}</option>
			<option value={COMPARE}>{t("changes.scope.compareBranch")}</option>
			{(commits.length > 0 || !known) && (
				<optgroup label={t("changes.scope.commits")}>
					{!known && selectedSha && (
						<option value={`commit:${selectedSha}`}>
							{selectedSha.slice(0, 7)}
						</option>
					)}
					{commits.map((c) => (
						<option key={c.sha} value={`commit:${c.sha}`}>
							{`${c.shortSha} ${c.subject} · ${c.authorName} · ${formatRelative(c.ts)}`}
						</option>
					))}
					{truncated && (
						<option disabled value="">
							{t("changes.scope.moreCommits")}
						</option>
					)}
				</optgroup>
			)}
		</Select>
	);
}
