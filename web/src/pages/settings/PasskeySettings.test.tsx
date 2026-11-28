/**
 * 设置 → 账户与设备里的 Passkey、密码登录与恢复码：列表与「当前」、敏感操作前的重新验证（只有 Passkey 时先确认再发起系统提示，
 * 已启用密码登录时弹密码 + 验证码对话框、可改用 Passkey，已验证则直接操作）、改名不验证、重新生成后一次性展示新码、
 * 没有 WebAuthn 时隐藏 Passkey 操作并引导到浏览器（启用了密码登录则其余操作照常）、没启用密码时只剩一把不能删、
 * 设置 / 关闭密码登录。fetch 换成按路径记账的桩，@simplewebauthn/browser 换成桩。
 */
import {
	browserSupportsWebAuthn,
	startAuthentication,
} from "@simplewebauthn/browser";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type Schemas, UNAUTHORIZED_EVENT } from "../../api/client";
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
let passwordEnabled: boolean;
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
			passwordEnabled,
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
	if (path === "/api/v1/auth/password/reauth") {
		// 与真实服务端一致：复核凭据不对是 403 reauth_failed，不是 401
		const ok =
			body !== null &&
			typeof body === "object" &&
			"password" in body &&
			body.password === "correct horse";
		if (!ok)
			return Response.json(
				{ code: "reauth_failed", message: "密码或验证码错误" },
				{ status: 403 },
			);
		const at = Date.now();
		reauthValidUntil = at + 300_000;
		return Response.json({ verifiedAt: at, validUntil: reauthValidUntil });
	}
	if (path === "/api/v1/auth/password/begin")
		return Response.json({
			enrollmentId: "enr-1",
			secret: "JBSWY3DPEHPK3PXP",
			otpauthUri: "otpauth://totp/Oryn:owner?secret=JBSWY3DPEHPK3PXP",
		});
	if (path === "/api/v1/auth/password/finish") {
		passwordEnabled = true;
		return new Response(null, { status: 204 });
	}
	if (path === "/api/v1/auth/password" && m === "DELETE") {
		if (items.length === 0)
			return Response.json(
				{ code: "last_login_method", message: "至少保留一种登录方式" },
				{ status: 409 },
			);
		passwordEnabled = false;
		return new Response(null, { status: 204 });
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
	passwordEnabled = false;
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

	it("只剩一把且没启用密码登录时没有删除按钮", async () => {
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

	it("已启用密码登录时最后一把也能删", async () => {
		items = [passkey("pk-mac", "MacBook", true)];
		passwordEnabled = true;
		reauthValidUntil = Date.now() + 300_000;
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除「MacBook」" }),
		);
		await answerConfirm(true);
		await waitFor(() =>
			expect(calls).toEqual(["DELETE /api/v1/auth/passkeys/pk-mac"]),
		);
		expect(await screen.findByText("还没有 Passkey")).toBeInTheDocument();
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

describe("已启用密码登录时的重新验证", () => {
	beforeEach(() => {
		passwordEnabled = true;
	});

	async function openVerify() {
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除「YubiKey」" }),
		);
		await answerConfirm(true);
		return screen.findByRole("button", { name: "验证" });
	}

	it("用密码 + 验证码：password/reauth 后才 DELETE，不发起系统提示", async () => {
		const verify = await openVerify();
		expect(verify).toBeDisabled();
		fireEvent.change(screen.getByLabelText("密码"), {
			target: { value: "correct horse" },
		});
		fireEvent.change(screen.getByLabelText("验证码"), {
			target: { value: "123456" },
		});
		fireEvent.click(verify);
		await waitFor(() =>
			expect(calls).toEqual([
				"POST /api/v1/auth/password/reauth",
				"DELETE /api/v1/auth/passkeys/pk-yubi",
			]),
		);
		expect(bodies[0]).toEqual({ password: "correct horse", code: "123456" });
		expect(startAuthentication).not.toHaveBeenCalled();
		expect(confirmStore.getState().pending).toBeNull();
	});

	it("对话框里改用 Passkey", async () => {
		await openVerify();
		fireEvent.click(screen.getByRole("button", { name: "用 Passkey 验证" }));
		await waitFor(() =>
			expect(calls).toEqual([
				"POST /api/v1/auth/reauth/begin",
				"POST /api/v1/auth/reauth/finish",
				"DELETE /api/v1/auth/passkeys/pk-yubi",
			]),
		);
		expect(startAuthentication).toHaveBeenCalledTimes(1);
	});

	it("密码或验证码错：留在对话框显示错误、清空验证码，不当成会话失效，重试成功后才 DELETE", async () => {
		const lost = vi.fn();
		window.addEventListener(UNAUTHORIZED_EVENT, lost);
		try {
			const verify = await openVerify();
			fireEvent.change(screen.getByLabelText("密码"), {
				target: { value: "wrong horse" },
			});
			fireEvent.change(screen.getByLabelText("验证码"), {
				target: { value: "123456" },
			});
			fireEvent.click(verify);
			expect(await screen.findByRole("alert")).toHaveTextContent(
				"密码或验证码错误",
			);
			expect(screen.getByLabelText("验证码")).toHaveValue("");
			expect(lost).not.toHaveBeenCalled();
			expect(calls).toEqual(["POST /api/v1/auth/password/reauth"]);

			fireEvent.change(screen.getByLabelText("密码"), {
				target: { value: "correct horse" },
			});
			fireEvent.change(screen.getByLabelText("验证码"), {
				target: { value: "654321" },
			});
			fireEvent.click(screen.getByRole("button", { name: "验证" }));
			await waitFor(() =>
				expect(calls).toEqual([
					"POST /api/v1/auth/password/reauth",
					"POST /api/v1/auth/password/reauth",
					"DELETE /api/v1/auth/passkeys/pk-yubi",
				]),
			);
			expect(lost).not.toHaveBeenCalled();
		} finally {
			window.removeEventListener(UNAUTHORIZED_EVENT, lost);
		}
	});

	it("取消对话框：不验证、不删除、不显示错误", async () => {
		await openVerify();
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "删除「YubiKey」" }),
			).toBeEnabled(),
		);
		expect(calls).toEqual([]);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});
});

describe("密码登录", () => {
	it("设置：验证后 begin → 扫码、填密码与验证码 → finish，之后显示已启用", async () => {
		mount();
		expect(await screen.findByText("未启用")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "设置" }));
		const verify = await answerConfirm(true);
		expect(verify.action).toBe("用 Passkey 验证");
		const qr = await screen.findByRole("img", { name: "验证器二维码" });
		expect(qr).toHaveAttribute(
			"data-qr-payload",
			"otpauth://totp/Oryn:owner?secret=JBSWY3DPEHPK3PXP",
		);
		expect(screen.getByText("JBSWY3DPEHPK3PXP")).toBeInTheDocument();
		const done = screen.getByRole("button", { name: "完成" });
		fireEvent.change(screen.getByLabelText(/^密码/, { selector: "input" }), {
			target: { value: "correct horse" },
		});
		fireEvent.change(screen.getByLabelText(/^确认密码/), {
			target: { value: "correct horse" },
		});
		expect(done).toBeDisabled();
		fireEvent.change(screen.getByLabelText("验证码"), {
			target: { value: "654321" },
		});
		fireEvent.click(done);
		expect(await screen.findByText(/^已启用/)).toBeInTheDocument();
		expect(calls).toEqual([
			"POST /api/v1/auth/reauth/begin",
			"POST /api/v1/auth/reauth/finish",
			"POST /api/v1/auth/password/begin",
			"POST /api/v1/auth/password/finish",
		]);
		expect(bodies[3]).toEqual({
			enrollmentId: "enr-1",
			password: "correct horse",
			code: "654321",
		});
		expect(screen.getByRole("button", { name: "重设" })).toBeInTheDocument();
	});

	it("关闭：没有 Passkey 时显示 last_login_method", async () => {
		items = [];
		passwordEnabled = true;
		reauthValidUntil = Date.now() + 300_000;
		mount();
		fireEvent.click(await screen.findByRole("button", { name: "关闭" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("关闭密码登录？");
		expect(req.danger).toBe(true);
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"这是唯一的登录方式，不能关闭。请先添加一把 Passkey",
		);
		expect(calls).toEqual(["DELETE /api/v1/auth/password"]);
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
		// 没有可用的验证方式：也不能设置密码登录
		expect(
			screen.queryByRole("button", { name: "设置" }),
		).not.toBeInTheDocument();
	});

	it("已启用密码登录：只有添加仍要去浏览器，删除与重新生成用密码验证", async () => {
		vi.mocked(browserSupportsWebAuthn).mockReturnValue(false);
		passwordEnabled = true;
		mount();
		await screen.findByText("MacBook");
		expect(
			screen.queryByRole("button", { name: "添加" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByText("新增 Passkey 需要在浏览器里操作"),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: "重新生成" }),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "删除「YubiKey」" }));
		await answerConfirm(true);
		await screen.findByRole("button", { name: "验证" });
		// 对话框里不给 Passkey 选项
		expect(
			screen.queryByRole("button", { name: "用 Passkey 验证" }),
		).not.toBeInTheDocument();
	});
});
