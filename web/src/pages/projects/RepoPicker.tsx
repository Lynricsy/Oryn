/**
 * 项目表单里的「从 GitHub 选择」（M14）：我的账号经 App 可访问的仓库，可搜索（输入 200ms 防抖后作为 `q`）。
 * 只在「我的账号」已连接且令牌有效时出现。弹层按最近的定位祖先摆放：调用方把输入框与本按钮放进同一个 `relative` 行。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { useGitHubRepos, useGitHubStatus } from "../../api/github-queries";
import { Badge } from "../../components/ui/data";
import { Option, Popover } from "../../components/ui/PickerPopover";
import {
	Button,
	ErrorText,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";

export type GitHubRepo = Schemas["GitHubRepo"];

/** 搜索框输入后多久才发请求（毫秒）。 */
const DEBOUNCE_MS = 200;

function RepoList({
	onPick,
}: {
	onPick: (repo: GitHubRepo) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [text, setText] = useState("");
	const [q, setQ] = useState("");
	useEffect(() => {
		const timer = setTimeout(() => setQ(text.trim()), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	}, [text]);
	const repos = useGitHubRepos(q);
	const list = repos.data?.repos ?? [];
	return (
		<div className="flex flex-col gap-1" data-testid="repo-picker">
			<div className="p-1">
				<TextInput
					autoFocus
					type="search"
					aria-label={t("github.repoPicker.search")}
					placeholder={t("github.repoPicker.search")}
					value={text}
					onChange={(e) => setText(e.target.value)}
					onKeyDown={(e) => {
						// 表单里回车不提交整个项目
						if (e.key === "Enter") e.preventDefault();
					}}
				/>
			</div>
			{repos.isPending && (
				<div className="flex justify-center p-2">
					<Spinner size={13} />
				</div>
			)}
			<div className="px-2">
				<ErrorText error={repos.error} />
			</div>
			{repos.isSuccess && list.length === 0 && (
				<p className="px-2 pb-2 text-faint text-xs">
					{t("github.repoPicker.empty")}
				</p>
			)}
			{list.map((r) => (
				<Option
					key={r.id}
					selected={false}
					onClick={() => onPick(r)}
					hint={r.defaultBranch}
					trailing={
						r.private ? (
							<Badge icon="lock">{t("github.repoPicker.private")}</Badge>
						) : undefined
					}
				>
					{r.fullName}
				</Option>
			))}
		</div>
	);
}

export function RepoPicker({
	onPick,
}: {
	onPick: (repo: GitHubRepo) => void;
}): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const [open, setOpen] = useState(false);
	// 其它测试替身可能给出空对象：身份字段缺失按未连接处理
	if (status.data?.identities?.user?.status !== "ok") return null;
	return (
		<>
			<Button
				size="sm"
				variant="ghost"
				icon="github"
				aria-expanded={open}
				// 再点一次时弹层先在 mousedown 里关掉，这里重新打开：保持打开而不是闪一下
				onClick={() => setOpen(true)}
			>
				{t("github.repoPicker.button")}
			</Button>
			{open && (
				<Popover side="bottom" onClose={() => setOpen(false)}>
					<RepoList
						onPick={(r) => {
							setOpen(false);
							onPick(r);
						}}
					/>
				</Popover>
			)}
		</>
	);
}
