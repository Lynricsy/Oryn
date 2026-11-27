/**
 * 设置 → 账户与设备里的 Passkey 与恢复码：列表与「当前」、敏感操作前的重新验证（先确认再发起系统提示，
 * 已验证则直接操作）、改名不验证、重新生成后一次性展示新码、没有 WebAuthn 时隐藏操作并引导到浏览器、只剩一把不能删。
 * fetch 换成按路径记账的桩，@simplewebauthn/browser 换成桩。
 */
import {
	browserSupportsWebAuthn,
	startAuthentication,
} from "@simplewebauthn/browser";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import { confirmStore } from "../../lib/confirm";
import { answerConfirm } from "../../test/confirm";
import PasskeySettings from "./PasskeySettings";

vi.mock("@simplewebauthn/browser", () => ({
	browserSupportsWebAuthn: vi.fn(() => true),
	startAuthentication: vi.fn(async () => ({ id: "assertion" })),
	startRegistration: vi.fn(async () => ({ id: "attestation" })),
}));

const NOW = Date.UTC(2026, 10, 27);

function passkey(
	id: string,
	name: string,
	current = false,
): Schemas["Passkey"] {
	return {
		id,
		name,
		createdAt: NOW - 30 * 86_400_000,
		transports: ["internal"],
		backupEligible: true,
		backedUp: current,
		current,
		...(current ? { lastUsedAt: NOW - 3_600_000 } : {}),
	};
}

const CODES = Array.from({ length: 10 }, (_, i) => `CODE-${i}`);

let items: Schemas["Passkey"][];
let reauthValidUntil: number | null;
/** 非 GET 请求，按发出顺序：`方法 路径` */
let calls: string[];
let bodies: unknown[];

async function handle(req: Request): Promise<Response> {
	const path = new URL(req.url, "http://x").pathname;
	const m = req.method;
	const text = m === "GET" ? "" : await req.text();
	const body: unknown = text ? JSON.parse(text) : undefined;
	if (m !== "GET") {
		calls.push(`${m} ${path}`);
		bodies.push(body);
	}
	if (path === "/api/v1/me")
		return Response.json({
			displayName: "owner",
			createdAt: NOW,
			auth: { kind: "web" },
			passkeyCount: items.length,
		});
	if (path === "/api/v1/auth/passkeys")
		return Response.json({ items, reauthValidUntil });
	if (path === "/api/v1/auth/reauth/begin")
		return Response.json({ ceremonyId: "c1", options: { challenge: "x" } });
	if (path === "/api/v1/auth/reauth/finish") {
		const at = Date.now();
		reauthValidUntil = at + 300_000;
		return Response.json({ verifiedAt: at, validUntil: reauthValidUntil });
	}
	const one = /^\/api\/v1\/auth\/passkeys\/([^/]+)$/.exec(path);
	if (one) {
		const id = decodeURIComponent(one[1] ?? "");
		if (m === "DELETE") {
			items = items.filter((x) => x.id !== id);
			return new Response(null, { status: 204 });
		}
		const pk = items.find((x) => x.id === id);
		if (m === "PATCH" && pk) {
			if (body && typeof body === "object" && "name" in body)
				pk.name = String(body.name);
			return Response.json(pk);
		}
	}
	if (path === "/api/v1/auth/recovery-codes") {
		if (m === "POST") return Response.json({ recoveryCodes: CODES });
		return Response.json({ total: 10, remaining: 8 });
	}
	return Response.json({ code: "not_found", message: path }, { status: 404 });
}

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<PasskeySettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const reauthCalls = () => calls.filter((c) => c.includes("/reauth/"));

beforeEach(() => {
	items = [passkey("pk-mac", "MacBook", true), passkey("pk-yubi", "YubiKey")];
	reauthValidUntil = null;
	calls = [];
	bodies = [];
	vi.mocked(browserSupportsWebAuthn).mockReturnValue(true);
	vi.mocked(startAuthentication).mockClear();
	vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
		handle(
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init),
		),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("Passkey 列表", () => {
	it("显示每把的名称，当前那把带「当前」", async () => {
		mount();
		expect(await screen.findByText("MacBook")).toBeInTheDocument();
		expect(screen.getByText("YubiKey")).toBeInTheDocument();
		const current = screen.getByText("当前");
		expect(current.closest("li")).toHaveTextContent("MacBook");
		expect(await screen.findByText(/还剩 8 \/ 10 个/)).toBeInTheDocument();
	});

	it("只剩一把时没有删除按钮", async () => {
		items = [passkey("pk-mac", "MacBook", true)];
		mount();
		await screen.findByText("MacBook");
		expect(
			screen.queryByRole("button", { name: "删除「MacBook」" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "给「MacBook」改名" }),
		).toBeInTheDocument();
	});
});

describe("删除", () => {
	it("未验证：确认删除 → 验证身份 → reauth 后才 DELETE", async () => {
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除「YubiKey」" }),
		);
		const del = await answerConfirm(true);
		expect(del.title).toBe("删除「YubiKey」？");
		expect(del.danger).toBe(true);
		const verify = await answerConfirm(true);
		expect(verify.title).toBe("验证身份");
		expect(verify.action).toBe("用 Passkey 验证");
		await waitFor(() =>
			expect(calls).toEqual([
				"POST /api/v1/auth/reauth/begin",
				"POST /api/v1/auth/reauth/finish",
				"DELETE /api/v1/auth/passkeys/pk-yubi",
			]),
		);
		expect(startAuthentication).toHaveBeenCalledTimes(1);
		await waitFor(() =>
			expect(screen.queryByText("YubiKey")).not.toBeInTheDocument(),
		);
	});

	it("已验证：确认后直接 DELETE，不再 reauth", async () => {
		reauthValidUntil = Date.now() + 300_000;
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除「YubiKey」" }),
		);
		await answerConfirm(true);
		await waitFor(() =>
			expect(calls).toEqual(["DELETE /api/v1/auth/passkeys/pk-yubi"]),
		);
		expect(confirmStore.getState().pending).toBeNull();
		expect(reauthCalls()).toEqual([]);
		expect(startAuthentication).not.toHaveBeenCalled();
	});

	it("取消验证：不发起系统提示，也不删除", async () => {
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除「YubiKey」" }),
		);
		await answerConfirm(true);
		await answerConfirm(false);
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "删除「YubiKey」" }),
			).toBeEnabled(),
		);
		expect(calls).toEqual([]);
		expect(startAuthentication).not.toHaveBeenCalled();
	});

	it("在系统提示里取消（NotAllowedError）不显示错误", async () => {
		vi.mocked(startAuthentication).mockRejectedValueOnce(
			Object.assign(new Error("cancelled"), { name: "NotAllowedError" }),
		);
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除「YubiKey」" }),
		);
		await answerConfirm(true);
		await answerConfirm(true);
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "删除「YubiKey」" }),
			).toBeEnabled(),
		);
		expect(calls).toEqual(["POST /api/v1/auth/reauth/begin"]);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});
});

describe("恢复码与改名", () => {
	it("重新生成：确认后展示 10 个新码，关闭后不再出现", async () => {
		reauthValidUntil = Date.now() + 300_000;
		mount();
		fireEvent.click(await screen.findByRole("button", { name: "重新生成" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("重新生成恢复码？");
		for (const c of CODES)
			expect(await screen.findByText(c)).toBeInTheDocument();
		expect(calls).toEqual(["POST /api/v1/auth/recovery-codes"]);
		fireEvent.click(screen.getByRole("button", { name: "我已保存" }));
		await waitFor(() =>
			expect(screen.queryByText("CODE-0")).not.toBeInTheDocument(),
		);
	});

	it("改名：PATCH 体是 { name }，不要求验证", async () => {
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "给「YubiKey」改名" }),
		);
		const input = await screen.findByLabelText("名称");
		fireEvent.change(input, { target: { value: "  备用钥匙 " } });
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(calls).toEqual(["PATCH /api/v1/auth/passkeys/pk-yubi"]),
		);
		expect(bodies).toEqual([{ name: "备用钥匙" }]);
		expect(reauthCalls()).toEqual([]);
		expect(await screen.findByText("备用钥匙")).toBeInTheDocument();
	});
});

describe("没有 WebAuthn 的环境", () => {
	it("隐藏添加、删除、重新生成，给出在浏览器中打开的链接", async () => {
		vi.mocked(browserSupportsWebAuthn).mockReturnValue(false);
		mount();
		await screen.findByText("MacBook");
		await screen.findByText(/还剩 8 \/ 10 个/);
		expect(
			screen.queryByRole("button", { name: "添加" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "删除「YubiKey」" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "重新生成" }),
		).not.toBeInTheDocument();
		const link = screen.getByRole("link", { name: "在浏览器中打开" });
		expect(link.getAttribute("href")).toMatch(/\/settings\/account$/);
		expect(link).toHaveAttribute("target", "_blank");
		// 改名不需要 Passkey，照常可用
		expect(
			screen.getByRole("button", { name: "给「YubiKey」改名" }),
		).toBeInTheDocument();
	});
});
