/** 认证分区：登录、首次设置、配对（输入码前与查看配对详情后）。 */
import { surface } from "./audit";

surface({ id: "auth.login", path: "/login", ready: "button" });
surface({ id: "auth.setup", path: "/setup", ready: "button" });
surface({ id: "auth.pair", path: "/pair", ready: "button" });
surface({
	id: "auth.pair.review",
	path: "/pair",
	ready: "button",
	open: async (page) => {
		await page.getByLabel("配对码", { exact: true }).fill("ABCD-EFGH");
		await page.getByRole("button", { name: "查看", exact: true }).click();
		await page.getByRole("button", { name: "批准", exact: true }).waitFor();
		return undefined;
	},
	noReopen: true,
});
