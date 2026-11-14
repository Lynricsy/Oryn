/**
 * 设备配对批准页：在手机浏览器（粗指针）里批准移动设备后给出 `oryn://open` 回到 App 的链接；
 * 批准桌面设备、或在细指针设备上批准时不显示。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../api/client";
import i18n from "../i18n";
import { PairApproval } from "./PairApproval";

type Kind = Schemas["DeviceKind"];

function stubServer(kind: Kind) {
	const pairing: Schemas["DevicePairing"] = {
		code: "ABCD-EFGH",
		name: "Pixel 9",
		kind,
		platform: kind === "mobile" ? "android" : "linux-x64",
		ip: "192.0.2.1",
		userAgent: "UA",
		createdAt: 0,
		expiresAt: 600_000,
	};
	const device: Schemas["Device"] = {
		id: "dev_1",
		name: pairing.name,
		kind,
		platform: pairing.platform,
		createdAt: 0,
	};
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			const path = new URL(req.url, "http://x").pathname;
			if (req.method === "GET" && path === "/api/v1/devices/pair/ABCD-EFGH")
				return Response.json(pairing);
			if (req.method === "POST" && path === "/api/v1/devices/pair/approve")
				return Response.json(device);
			return Response.json({ code: "not_found", message: "" }, { status: 404 });
		}),
	);
}

/** 假 matchMedia：只回答 `(pointer: coarse)`。 */
function pointer(coarse: boolean) {
	vi.stubGlobal("matchMedia", (q: string) => ({
		matches: coarse && q === "(pointer: coarse)",
		media: q,
		addEventListener: () => undefined,
		removeEventListener: () => undefined,
	}));
}

afterEach(() => {
	vi.unstubAllGlobals();
});

async function approve() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<PairApproval initialCode="abcdefgh" />
		</QueryClientProvider>,
	);
	await act(async () => {
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("pair.lookup") }),
		);
	});
	await act(async () => {
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("pair.approve") }),
		);
	});
	expect(screen.getByRole("status")).toHaveTextContent(i18n.t("pair.approved"));
}

describe("批准后回到 App", () => {
	it("移动设备 + 粗指针：显示 oryn://open 链接", async () => {
		stubServer("mobile");
		pointer(true);
		await approve();
		const link = screen.getByTestId("pair-back-to-app");
		expect(link).toHaveAttribute("href", "oryn://open");
		expect(link).toHaveTextContent(i18n.t("pair.backToApp"));
	});

	it("桌面设备：不显示", async () => {
		stubServer("desktop");
		pointer(true);
		await approve();
		expect(screen.queryByTestId("pair-back-to-app")).toBeNull();
	});

	it("移动设备但细指针（在电脑上批准）：不显示", async () => {
		stubServer("mobile");
		pointer(false);
		await approve();
		expect(screen.queryByTestId("pair-back-to-app")).toBeNull();
	});
});
