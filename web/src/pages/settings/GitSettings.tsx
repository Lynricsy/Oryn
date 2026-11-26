/**
 * 设置 → Git 与 GitHub：OAuth 回调结果（?connected= / ?error=）在最上面，其后「提交者」（orb 内 git 提交的默认身份），
 * 再是 GitHub 的 OAuth App、GitHub 账号（含自动化用哪个账号）、仓库 webhook、审计。
 */
import type { ReactNode } from "react";
import GitHubSettings, { GitHubCallbackNotice } from "./GitHubSettings";
import GitIdentitySettings from "./GitIdentitySettings";

export default function GitSettings(): ReactNode {
	return (
		<>
			<GitHubCallbackNotice />
			<GitIdentitySettings />
			<GitHubSettings />
		</>
	);
}
