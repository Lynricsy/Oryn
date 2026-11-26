/** 新建 Box 对话框（M23）：非法名字不发请求；停用的 provider 不在规格里；重名时留在对话框里显示人话错误；「交给 Agent」写入首屏提示并跳过去。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Provider } from "../../api/client";
import i18n from "../../i18n";
import { uiStore } from "../../state/ui";
import { NewBoxDialog } from "./NewBoxDialog";

const PROVIDERS: Provider[] = [
	{
		id: "prov_1",
		name: "local",
		driver: "docker",
		orbTypes: [{ name: "medium", cpus: 4, memoryMB: 8192 }],
		online: true,
		createdAt: 1,
	},
	{
		id: "prov_2",
		name: "retired",
		driver: "docker",
		orbTypes: [{ name: "small", cpus: 2, memoryMB: 4096 }],
		online: true,
		createdAt: 1,
		disabledAt: 2,
	},
];

let posts: unknown[];
let createReply: () => Response;

beforeEach(() => {
	posts = [];
	createReply = () =>
		Response.json(
			{ code: "box_exists", message: "box workbench already exists" },
			{ status: 409 },
		);
	vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
		const req = input instanceof Request ? input : new Request(String(input));
		const url = new URL(req.url);
		if (url.pathname === "/api/v1/providers")
			return Response.json({ providers: PROVIDERS });
		if (url.pathname === "/api/v1/boxes" && req.method === "POST") {
			posts.push(await req.json());
			return createReply();
		}
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	uiStore.getState().takeHomeSeed();
});

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount() {
	const onClose = vi.fn();
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/boxes"]}>
				<NewBoxDialog onClose={onClose} />
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
	return onClose;
}

// 名字输入框的标签里还有一行提示，按前缀匹配
const nameInput = () =>
	screen.getByRole("textbox", {
		name: new RegExp(`^${i18n.t("boxes.new.name")}`),
	});
const createButton = () =>
	screen.getByRole("button", { name: i18n.t("boxes.new.create") });

describe("新建 Box", () => {
	it("非法名字不发请求，就地提示", async () => {
		mount();
		fireEvent.change(nameInput(), { target: { value: "Bad_Name" } });
		fireEvent.click(createButton());
		expect(
			await screen.findByText(i18n.t("boxes.new.invalidName")),
		).toBeVisible();
		expect(posts).toHaveLength(0);
	});

	it("停用的 provider 不在规格里", async () => {
		mount();
		const select = screen.getByRole("combobox", {
			name: i18n.t("boxes.new.size"),
		});
		fireEvent.click(select);
		await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
		const list = screen.getByRole("listbox");
		expect(list).toHaveTextContent("local");
		expect(list).not.toHaveTextContent("retired");
	});

	it("服务端回 box_exists：对话框还开着、输入还在、看得到人话错误", async () => {
		const onClose = mount();
		fireEvent.change(nameInput(), { target: { value: "workbench" } });
		fireEvent.click(createButton());
		expect(await screen.findByText(i18n.t("errors.box_exists"))).toBeVisible();
		expect(posts).toEqual([{ name: "workbench" }]);
		expect(nameInput()).toHaveValue("workbench");
		expect(onClose).not.toHaveBeenCalled();
		expect(screen.getByTestId("where")).toHaveTextContent("/boxes");
	});

	it("创建成功后进入 box 页；选了规格就带上 provider 与 type", async () => {
		createReply = () =>
			Response.json(
				{
					id: "box_new",
					name: "lab",
					orbId: "orb-1",
					state: "starting",
					desired: "running",
					provider: "local",
					type: "medium",
					cpus: 4,
					memoryMB: 8192,
					createdAt: 1,
					apps: [],
				},
				{ status: 201 },
			);
		mount();
		const select = screen.getByRole("combobox", {
			name: i18n.t("boxes.new.size"),
		});
		fireEvent.click(select);
		await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
		fireEvent.click(screen.getAllByRole("option")[1] as HTMLElement);
		fireEvent.change(nameInput(), { target: { value: "lab" } });
		fireEvent.click(createButton());
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/boxes/box_new"),
		);
		expect(posts).toEqual([{ name: "lab", provider: "local", type: "medium" }]);
	});

	it("交给 Agent：写入首屏提示并跳到首屏", () => {
		const onClose = mount();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("boxes.new.handOff") }),
		);
		expect(onClose).toHaveBeenCalled();
		expect(screen.getByTestId("where")).toHaveTextContent(/^\/$/);
		expect(uiStore.getState().takeHomeSeed()).toEqual({
			text: i18n.t("boxes.new.seedPrompt"),
		});
	});
});
