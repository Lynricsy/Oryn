/** 范围选择器：全部改动（相对基线）| 仅未提交 | 单个 commit。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatRelative } from "../../lib/format";
import { Select } from "../ui/primitives";
import type { ChangeScope, GitCommit } from "./git";

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
	baseName,
	commits,
	truncated,
}: {
	value: ChangeScope;
	onChange(s: ChangeScope): void;
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
			onChange={(e) => onChange(decode(e.target.value))}
		>
			<option value="all">
				{baseName
					? t("changes.scope.allBase", { base: baseName })
					: t("changes.scope.all")}
			</option>
			<option value="uncommitted">{t("changes.scope.uncommitted")}</option>
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
