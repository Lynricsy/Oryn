/** 设置 → Git 与 GitHub：orb 内提交身份的全局默认值、GitHub OAuth 与身份。 */
import type { ReactNode } from "react";
import GitHubSettings from "./GitHubSettings";
import GitIdentitySettings from "./GitIdentitySettings";

export default function GitSettings(): ReactNode {
	return (
		<>
			<GitIdentitySettings />
			<GitHubSettings />
		</>
	);
}
