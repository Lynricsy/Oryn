/**
 * 仓库子页（提交 / 分支 / 标签 / 文件，M24 阶段 ②）的门：先取镜像状态，能读时把状态交给 children；
 * 加载中转圈，读状态失败给错误，镜像不能读（没有仓库、没有 git、克隆失败或进行中、空仓库）时只显示 `RepoUnavailable`。
 */
import type { ReactNode } from "react";
import type { Project } from "../../../api/client";
import { type RepoStatus, useRepoStatus } from "../../../api/repo-queries";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { RepoUnavailable } from "../home/RepoUnavailable";

/** 镜像能读：有仓库、控制面有 git、已就绪且不是空仓库。主页与各子页共用这条判断。 */
export function repoReadable(status: RepoStatus): boolean {
	return (
		status.kind !== "none" &&
		status.available &&
		status.state === "ready" &&
		!status.empty
	);
}

export function RepoGate({
	project,
	children,
}: {
	project: Project;
	children: (status: RepoStatus) => ReactNode;
}): ReactNode {
	const repo = useRepoStatus(project.id);
	const status = repo.data;
	if (!status)
		return repo.isError ? (
			<ErrorText error={repo.error} />
		) : (
			<div className="flex justify-center p-6 text-muted">
				<Spinner size={18} />
			</div>
		);
	if (!repoReadable(status))
		return <RepoUnavailable project={project} status={status} />;
	return children(status);
}
