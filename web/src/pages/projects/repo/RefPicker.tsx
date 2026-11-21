/**
 * 选 ref 的下拉（M24 阶段 ②）：两个子菜单「分支」「标签」，当前项打勾；触发钮是分支图标加 ref 名。
 * 每类列出最近活动的前一页（默认分支总在第一）；更多的在分支 / 标签子页里。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { type RepoRefKind, useRepoRefs } from "../../../api/repo-queries";
import { Icon } from "../../../components/ui/icons";
import { Button, Menu, type MenuItem } from "../../../components/ui/primitives";

function useRefItems(
	projectId: string,
	kind: RepoRefKind,
	value: string,
	onChange: (ref: string) => void,
	empty: string,
): MenuItem[] {
	const q = useRepoRefs(projectId, kind);
	const refs = q.data?.pages.flatMap((p) => p.refs) ?? [];
	if (refs.length === 0) return [{ key: "none", label: empty, disabled: true }];
	return refs.map((r) => ({
		key: r.name,
		label: <span className="font-mono text-xs">{r.name}</span>,
		native: { label: r.name },
		icon: kind === "branch" ? "branch" : "tag",
		checked: r.name === value,
		onSelect: () => onChange(r.name),
	}));
}

export function RefPicker({
	projectId,
	value,
	onChange,
}: {
	projectId: string;
	value: string;
	onChange: (ref: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const branches = useRefItems(projectId, "branch", value, onChange, "—");
	const tags = useRefItems(
		projectId,
		"tag",
		value,
		onChange,
		t("projectRepo.refs.none"),
	);
	return (
		<Menu
			align="left"
			items={[
				{
					key: "branches",
					label: t("projectRepo.refPicker.branches"),
					icon: "branch",
					children: branches,
				},
				{
					key: "tags",
					label: t("projectRepo.refPicker.tags"),
					icon: "tag",
					children: tags,
				},
			]}
			trigger={(props) => (
				<Button
					{...props}
					size="sm"
					icon="branch"
					data-testid="repo-ref-picker"
					aria-label={t("projectRepo.refPicker.label", { ref: value })}
					className="max-w-60"
				>
					<span className="min-w-0 truncate font-mono">{value}</span>
					<Icon
						name="chevronDown"
						size={12}
						aria-hidden
						className="shrink-0 text-faint"
					/>
				</Button>
			)}
		/>
	);
}
