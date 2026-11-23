/**
 * 项目主页的 README（M24 阶段 ②，仿 GitHub 仓库首页的 README 框）：在根目录的文件里按 README.md > .markdown > .mdx >
 * .rst > .txt > 无扩展名（不分大小写）选一个，至多读 512 KB。框头是带下划线的文件名标签，正文宽留白；Markdown 按文档渲染
 * （内嵌 HTML、单换行不断行），相对链接进仓库文件页、相对图片走 blob 地址；其它格式按原文显示。没有 README 时不渲染。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { repoKeys, repoRetry, useRepoTree } from "../../../api/repo-queries";
import { Markdown } from "../../../components/markdown/Markdown";
import { Icon } from "../../../components/ui/icons";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { projectRepoSource } from "../../../lib/file-source";
import { repoRelativeUrls } from "../repo/paths";

const README_NAMES = [
	"readme.md",
	"readme.markdown",
	"readme.mdx",
	"readme.rst",
	"readme.txt",
	"readme",
];
const MARKDOWN_RE = /\.(md|markdown|mdx)$/i;
const README_MAX = 512 * 1024;

export function ReadmeCard({
	projectId,
	refName,
}: {
	projectId: string;
	refName: string;
}): ReactNode {
	const { t } = useTranslation();
	// 与「文件」区块同一个查询（同 key），不多发请求
	const tree = useRepoTree(projectId, refName, "");
	const files = tree.data?.entries.filter((e) => e.type === "file") ?? [];
	const readme = README_NAMES.map((n) =>
		files.find((e) => e.name.toLowerCase() === n),
	).find((e) => e !== undefined);
	const path = readme?.path ?? "";
	const text = useQuery({
		queryKey: [...repoKeys.all(projectId), "readme", refName, path],
		enabled: !!readme,
		queryFn: () =>
			projectRepoSource(projectId, refName).readText(path, README_MAX),
		staleTime: 30_000,
		retry: repoRetry,
	});
	// README 在仓库根目录
	const relativeTo = useMemo(
		() => repoRelativeUrls(projectId, refName, ""),
		[projectId, refName],
	);
	if (!readme) return null;
	return (
		<section
			data-testid="project-readme"
			className="overflow-hidden rounded-lg border border-border bg-panel"
		>
			<header className="flex h-11 items-stretch border-border border-b px-2">
				<h3 className="relative inline-flex items-center gap-1.5 px-2 font-medium text-fg text-sm after:absolute after:inset-x-1 after:bottom-0 after:h-0.5 after:rounded-full after:bg-[var(--brand)]">
					<Icon name="readme" size={16} aria-hidden className="text-faint" />
					{readme.name}
				</h3>
			</header>
			<div className="px-4 py-5 sm:px-8 sm:py-6">
				{text.isPending ? (
					<div className="flex justify-center py-3 text-faint">
						<Spinner />
					</div>
				) : text.isError ? (
					<ErrorText error={text.error} />
				) : (
					<div className="flex flex-col gap-2">
						{MARKDOWN_RE.test(readme.name) ? (
							<Markdown
								text={text.data.text}
								relativeTo={relativeTo}
								document
							/>
						) : (
							<pre className="whitespace-pre-wrap break-words font-mono text-xs">
								{text.data.text}
							</pre>
						)}
						{text.data.truncated && (
							<p className="text-faint text-xs">
								{t("projectRepo.readme.truncated")}
							</p>
						)}
					</div>
				)}
			</div>
		</section>
	);
}
