import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { realtime } from "../realtime/socket";
import { connectionStore } from "../state/stores";
import { ServerErrors } from "./ServerErrors";

const TID = "T-0193a1b2-0000-7000-8000-0000000000f1";

describe("全局服务端错误提示（W14）", () => {
	afterEach(() => {
		connectionStore.setState({ errors: [] });
		vi.restoreAllMocks();
	});

	it("非命令错误可见；带线程时可重新同步并关闭", () => {
		const resync = vi.spyOn(realtime, "resync").mockImplementation(() => {});
		connectionStore.setState({
			errors: [
				{ key: 1, code: "invalid_frame", message: "bad", threadId: TID },
				{ key: 2, code: "not_found", message: "thread gone" },
			],
		});
		render(
			<MemoryRouter>
				<ServerErrors />
			</MemoryRouter>,
		);
		const alerts = screen.getAllByRole("alert");
		expect(alerts).toHaveLength(2);
		expect(alerts[0]).toHaveTextContent(i18n.t("errors.invalid_frame"));
		expect(alerts[1]).toHaveTextContent("thread gone");
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("alerts.resync") }),
		);
		expect(resync).toHaveBeenCalledWith(TID);
		expect(connectionStore.getState().errors.map((e) => e.key)).toEqual([2]);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("common.dismiss") }),
		);
		expect(connectionStore.getState().errors).toEqual([]);
	});
});
