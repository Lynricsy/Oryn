/** 收件箱分区：按状态 / 按项目两种视图与滚到底。 */
import { settle, surface } from "./audit";

surface({ id: "inbox.page", path: "/inbox" });
surface({
	id: "inbox.by-project",
	path: "/inbox",
	open: async (page) => {
		await page.getByRole("button", { name: "按项目" }).click();
		await settle(page);
		return undefined;
	},
});
surface({
	id: "inbox.bottom",
	path: "/inbox",
	open: async (page) => {
		await page
			.locator("main .overflow-y-auto")
			.first()
			.evaluate((e) => e.scrollTo({ top: e.scrollHeight }));
		await settle(page);
		return undefined;
	},
});
