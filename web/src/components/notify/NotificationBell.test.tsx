import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	type AppNotification,
	notificationsStore,
} from "../../state/notifications";
import { NotificationBell } from "./NotificationBell";

const TID = "T-0193a1b2-0000-7000-8000-0000000000b1";

function note(p: Partial<AppNotification>): AppNotification {
	return {
		id: "ntf_1",
		kind: "done",
		threadId: TID,
		title: "重构重连",
		body: "测试全部通过",
		url: `/t/${TID}`,
		createdAt: Date.now() - 60_000,
		deliveries: [],
		...p,
	};
}

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

let posts: unknown[];

beforeEach(() => {
	posts = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			posts.push(await req.json());
			return new Response(null, { status: 204 });
		}),
	);
	notificationsStore.setState({
		items: [
			note({ id: "a" }),
			note({
				id: "b",
				kind: "input",
				threadId: "T-x",
				url: "/t/T-x",
				title: "待答",
				body: "",
			}),
			note({
				id: "c",
				kind: "error",
				readAt: 1,
				title: "旧的出错",
				body: "exit 1",
			}),
		],
		unread: 2,
		toasts: [],
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function mount() {
	render(
		<MemoryRouter initialEntries={["/"]}>
			<NotificationBell />
			<Routes>
				<Route path="*" element={<Where />} />
			</Routes>
		</MemoryRouter>,
	);
}

describe("NotificationBell", () => {
	it("未读计数胶囊；无未读时不显示", async () => {
		mount();
		expect(
			screen.getByRole("button", { name: "通知（2 条未读）" }),
		).toBeInTheDocument();
		expect(screen.getByText("2")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "通知（2 条未读）" }));
		fireEvent.click(screen.getByRole("button", { name: "全部已读" }));
		expect(screen.getByRole("button", { name: "通知" })).toBeInTheDocument();
		expect(screen.queryByText("2")).toBeNull();
		await vi.waitFor(() => expect(posts).toEqual([{}]));
	});

	it("打开列表：图标、标题、正文（空正文用缺省文案）；点一条跳到线程并标该线程已读", async () => {
		mount();
		fireEvent.click(screen.getByRole("button", { name: /^通知/ }));
		const dialog = screen.getByRole("dialog", { name: "通知" });
		expect(within(dialog).getByText("测试全部通过")).toBeInTheDocument();
		expect(within(dialog).getByText("等待你的回答")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("需要输入")).toBeInTheDocument();
		expect(within(dialog).getAllByLabelText("未读")).toHaveLength(2);
		fireEvent.click(within(dialog).getByText("重构重连"));
		expect(screen.getByTestId("where")).toHaveTextContent(`/t/${TID}`);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(notificationsStore.getState().unread).toBe(1);
		await vi.waitFor(() => expect(posts).toEqual([{ threadId: TID }]));
	});

	it("点已读的通知只跳转，不再发已读请求", () => {
		mount();
		fireEvent.click(screen.getByRole("button", { name: /^通知/ }));
		fireEvent.click(screen.getByText("旧的出错"));
		expect(screen.getByTestId("where")).toHaveTextContent(`/t/${TID}`);
		expect(posts).toEqual([]);
	});

	it("没有通知时显示空态", () => {
		notificationsStore.setState({ items: [], unread: 0 });
		mount();
		fireEvent.click(screen.getByRole("button", { name: "通知" }));
		expect(screen.getByText("还没有通知")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "全部已读" })).toBeDisabled();
	});
});
