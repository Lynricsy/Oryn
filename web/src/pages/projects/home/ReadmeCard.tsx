/**
 * 项目主页的 README（M24 阶段 ②）：在根目录的文件里按 README.md > .markdown > .mdx > .rst > .txt > 无扩展名
 * （不分大小写）选一个，至多读 512 KB。Markdown 里的相对链接进仓库文件页、相对图片走 blob 地址；其它格式按原文显示。
 * 没有 README 时不渲染。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
	repoBlobUrl,
	repoKeys,
	repoRetry,
	useRepoTree,
} from "../../../api/repo-queries";
import {
	Markdown,
	type RelativeUrls,
} from "../../../components/markdown/Markdown";
import { Card } from "../../../components/ui/data";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { projectRepoSource } from "../../../lib/file-source";
import { repoFileHref, resolveRepoPath } from "../repo/paths";

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
	const relativeTo = useMemo<RelativeUrls>(
		() => ({
			link: (href) => {
				const r = resolveRepoPath("", href);
				return r && repoFileHref(projectId, r.path, refName) + r.hash;
			},
			image: (src) => {
				const r = resolveRepoPath("", src);
				return r && repoBlobUrl(projectId, refName, r.path);
			},
		}),
		[projectId, refName],
	);
	if (!readme) return null;
	return (
		<div data-testid="project-readme">
			<Card title={readme.name} icon="readme">
				{text.isPending ? (
					<div className="flex justify-center py-3 text-faint">
						<Spinner />
					</div>
				) : text.isError ? (
					<ErrorText error={text.error} />
				) : (
					<div className="flex flex-col gap-2">
						{MARKDOWN_RE.test(readme.name) ? (
							<Markdown text={text.data.text} relativeTo={relativeTo} />
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
			</Card>
		</div>
	);
}
