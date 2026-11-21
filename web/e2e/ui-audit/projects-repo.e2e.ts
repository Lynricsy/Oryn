/** 项目仓库浏览（M24 阶段 ②）：主页的仓库区块（就绪 / 克隆失败）、提交列表与详情、ref 选择菜单、分支、标签、目录与文件预览。 */
import { MOCK_REPO_HEAD_SHA } from "../../src/mock/repo";
import { lastOverlay, surface } from "./audit";

const PRJ = "prj_01JCZ0000000000000000ORYN";
const PAGE = `/projects/${PRJ}`;

surface({
	id: "projects.repo.home",
	path: PAGE,
	ready: "[data-testid=repo-commits-card] [data-testid=repo-commit-row]",
});
surface({
	id: "projects.repo.home-error",
	path: "/projects/prj_01JCZ00000000000000AGENT",
	ready: "[data-testid=repo-unavailable]",
});
surface({
	id: "projects.repo.commits",
	path: `${PAGE}/commits`,
	ready: "[data-testid=repo-commit-list] [data-testid=repo-commit-row]",
});
surface({
	id: "projects.repo.commit",
	path: `${PAGE}/commits/${MOCK_REPO_HEAD_SHA}`,
	ready: "[data-testid=repo-commit-detail] [data-testid=diff-rows]",
});
surface({
	id: "projects.repo.ref-picker",
	path: `${PAGE}/commits`,
	ready: "[data-testid=repo-commit-row]",
	open: async (page) => {
		await page.getByTestId("repo-ref-picker").click();
		return lastOverlay(page);
	},
});
surface({
	id: "projects.repo.branches",
	path: `${PAGE}/branches`,
	ready: "[data-testid=repo-ref-row]",
});
surface({
	id: "projects.repo.tags",
	path: `${PAGE}/tags`,
	ready: "[data-testid=repo-ref-row]",
});
surface({
	id: "projects.repo.files",
	path: `${PAGE}/files/docs`,
	ready: "[data-testid=repo-entry-list] [data-testid=repo-entry]",
});
surface({
	id: "projects.repo.file",
	path: `${PAGE}/files/README.md`,
	ready: "[data-testid=file-viewer]",
});
