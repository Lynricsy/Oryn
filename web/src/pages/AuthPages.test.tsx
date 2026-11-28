import { browserSupportsWebAuthn } from "@simplewebauthn/browser";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ParseKeys } from "i18next";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import {
	fullPage,
	goAfterLogin,
	LoginPage,
	SetupPage,
	setupTokenFromHash,
} from "./AuthPages";

vi.mock("@simplewebauthn/browser", () => ({
	browserSupportsWebAuthn: vi.fn(() => true),
	startAuthentication: vi.fn(async () => ({ id: "assertion" })),
	startRegistration: vi.fn(async () => ({ id: "attestation" })),
}));

describe("初始化令牌走 URL 片段（L3）", () => {
	afterEach(() => window.history.replaceState(null, "", "/"));

	it("从 #token= 读取（URL 转义的值也能还原）", () => {
		expect(setupTokenFromHash("#token=ost_abc-_9")).toBe("ost_abc-_9");
		expect(setupTokenFromHash("#token=ost_a%2Bb")).toBe("ost_a+b");
		expect(setupTokenFromHash("")).toBeUndefined();
		expect(setupTokenFromHash("#other=1")).toBeUndefined();
	});

	it("SetupPage 读到令牌后从地址栏抹掉片段，查询串里的 token 不再被采用", () => {
		window.history.replaceState(null, "", "/setup?x=1#token=ost_secret");
		render(
			<QueryClientProvider client={new QueryClient()}>
				<MemoryRouter initialEntries={["/setup"]}>
					<SetupPage />
				</MemoryRouter>
			</QueryClientProvider>,
		);
		expect(
			screen.getByRole("heading", { name: i18n.t("auth.setupTitle") }),
		).toBeInTheDocument();
		expect(window.location.hash).toBe("");
		expect(window.location.search).toBe("?x=1");
	});
});

describe("登录后的去处（M4 4.3）", () => {
	afterEach(() => vi.restoreAllMocks());

	it("next 是 portal 授权端点：整页跳转到服务端（location.replace），不走路由", () => {
		const replace = vi.spyOn(fullPage, "replace").mockImplementation(() => {});
		const navigate = vi.fn();
		const next = "/portal/auth?t=3f9a1c0b7e2d&p=5173&next=%2Fa%3Fb%3D1";
		goAfterLogin(next, navigate);
		expect(replace).toHaveBeenCalledWith(next);
		expect(navigate).not.toHaveBeenCalled();
	});

	it("站内路径走路由内跳转，非站内地址回首页", () => {
		const replace = vi.spyOn(fullPage, "replace").mockImplementation(() => {});
		const navigate = vi.fn();
		goAfterLogin("/t/T-1", navigate);
		goAfterLogin("https://evil.example/portal/auth?t=1", navigate);
		// 只认 `/portal/auth?` 前缀：别的 portal 开头的站内路径照常走路由
		goAfterLogin("/portal/authx", navigate);
		expect(navigate.mock.calls).toEqual([
			["/t/T-1", { replace: true }],
			["/", { replace: true }],
			["/portal/authx", { replace: true }],
		]);
		expect(replace).not.toHaveBeenCalled();
	});
});

/** 密码 + TOTP：fetch 换成按路径记账的桩；`replies` 按路径给响应，缺省 404。 */
describe("密码 + 验证器", () => {
	const ENROLLMENT = {
		enrollmentId: "enr-1",
		secret: "JBSWY3DPEHPK3PXP",
		otpauthUri: "otpauth://totp/Oryn:owner?secret=JBSWY3DPEHPK3PXP&issuer=Oryn",
	};
	let calls: { path: string; body: unknown }[];
	let replies: Record<string, () => Response>;

	beforeEach(() => {
		calls = [];
		replies = {};
		vi.mocked(browserSupportsWebAuthn).mockReturnValue(true);
		vi.stubGlobal(
			"fetch",
			async (input: RequestInfo | URL, init?: RequestInit) => {
				const req =
					input instanceof Request
						? input
						: new Request(new URL(String(input), "http://x"), init);
				const path = new URL(req.url, "http://x").pathname;
				if (req.method !== "GET") {
					const text = await req.text();
					calls.push({ path, body: text ? JSON.parse(text) : undefined });
				}
				const reply = replies[path];
				return reply
					? reply()
					: Response.json(
							{ code: "not_found", message: path },
							{ status: 404 },
						);
			},
		);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		window.history.replaceState(null, "", "/");
	});

	function mount(entry: string, page: ReactNode) {
		render(
			<QueryClientProvider
				client={
					new QueryClient({ defaultOptions: { queries: { retry: false } } })
				}
			>
				<MemoryRouter initialEntries={[entry]}>
					<Routes>
						<Route path="/login" element={page} />
						<Route path="/setup" element={page} />
						<Route path="/" element={<p>home</p>} />
						<Route path="/t/:id" element={<p>thread</p>} />
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	const t = (key: ParseKeys): string => i18n.t(key);
	const typeInto = (label: string | RegExp, value: string) =>
		fireEvent.change(screen.getByLabelText(label), { target: { value } });

	it("登录：展开密码表单，验证码去掉空格，成功后去 next", async () => {
		replies["/api/v1/auth/password/login"] = () =>
			Response.json({
				displayName: "owner",
				createdAt: 0,
				auth: { kind: "web" },
			});
		mount("/login?next=/t/T-1", <LoginPage />);
		expect(screen.queryByLabelText(t("auth.password"))).not.toBeInTheDocument();
		fireEvent.click(
			screen.getByRole("button", { name: t("auth.usePassword") }),
		);
		const password = screen.getByLabelText(t("auth.password"));
		const code = screen.getByLabelText(t("auth.totpCode"));
		expect(password).toHaveAttribute("autocomplete", "current-password");
		expect(code).toHaveAttribute("autocomplete", "one-time-code");
		expect(code).toHaveAttribute("inputmode", "numeric");
		typeInto(t("auth.password"), "hunter22");
		typeInto(t("auth.totpCode"), "123 456");
		expect(code).toHaveValue("123456");
		fireEvent.click(
			screen.getByRole("button", { name: t("auth.loginWithPassword") }),
		);
		expect(await screen.findByText("thread")).toBeInTheDocument();
		expect(calls).toEqual([
			{
				path: "/api/v1/auth/password/login",
				body: { password: "hunter22", code: "123456" },
			},
		]);
	});

	it("不支持 WebAuthn：默认展开密码表单、不给 Passkey 按钮；失败显示错误并清空验证码", async () => {
		vi.mocked(browserSupportsWebAuthn).mockReturnValue(false);
		replies["/api/v1/auth/password/login"] = () =>
			Response.json(
				{ code: "unauthorized", message: "密码或验证码不对" },
				{ status: 401 },
			);
		mount("/login", <LoginPage />);
		expect(
			screen.queryByRole("button", { name: t("auth.loginWithPasskey") }),
		).not.toBeInTheDocument();
		typeInto(t("auth.password"), "wrong-password");
		typeInto(t("auth.totpCode"), "000000");
		fireEvent.click(
			screen.getByRole("button", { name: t("auth.loginWithPassword") }),
		);
		expect(await screen.findByRole("alert")).toBeInTheDocument();
		expect(screen.getByLabelText(t("auth.totpCode"))).toHaveValue("");
		expect(screen.queryByText("home")).not.toBeInTheDocument();
	});

	it("初始化：改用密码 → 带令牌 begin → 扫码、两次密码一致、验证码 → finish 后展示恢复码", async () => {
		window.history.replaceState(null, "", "/setup#token=ost_secret");
		replies["/api/v1/auth/password/setup/begin"] = () =>
			Response.json(ENROLLMENT);
		replies["/api/v1/auth/password/setup/finish"] = () =>
			Response.json({
				me: { displayName: "owner", createdAt: 0, auth: { kind: "web" } },
				recoveryCodes: ["CODE-A", "CODE-B"],
			});
		mount("/setup", <SetupPage />);
		fireEvent.click(
			screen.getByRole("button", { name: t("auth.usePasswordInstead") }),
		);
		const qr = await screen.findByRole("img", { name: t("auth.totpQrLabel") });
		expect(qr).toHaveAttribute("data-qr-payload", ENROLLMENT.otpauthUri);
		expect(qr.querySelectorAll("rect").length).toBeGreaterThan(0);
		expect(screen.getByText(ENROLLMENT.secret)).toBeInTheDocument();
		expect(calls).toEqual([
			{
				path: "/api/v1/auth/password/setup/begin",
				body: { token: "ost_secret" },
			},
		]);

		const finish = screen.getByRole("button", { name: t("auth.finishSetup") });
		typeInto(/^密码/, "short");
		expect(screen.getByText(t("auth.passwordTooShort"))).toBeInTheDocument();
		typeInto(/^密码/, "correct horse");
		typeInto(/^确认密码/, "correct hose");
		typeInto(t("auth.totpCode"), "654321");
		expect(screen.getByText(t("auth.passwordMismatch"))).toBeInTheDocument();
		expect(finish).toBeDisabled();
		typeInto(/^确认密码/, "correct horse");
		expect(finish).toBeEnabled();
		fireEvent.click(finish);

		expect(await screen.findByText("CODE-A")).toBeInTheDocument();
		expect(calls[1]).toEqual({
			path: "/api/v1/auth/password/setup/finish",
			body: {
				enrollmentId: "enr-1",
				password: "correct horse",
				code: "654321",
			},
		});
	});

	it("恢复流程且不支持 WebAuthn：直接是密码路径，begin 不带令牌，完成后回首页", async () => {
		vi.mocked(browserSupportsWebAuthn).mockReturnValue(false);
		replies["/api/v1/auth/password/setup/begin"] = () =>
			Response.json(ENROLLMENT);
		replies["/api/v1/auth/password/setup/finish"] = () =>
			Response.json({
				me: { displayName: "owner", createdAt: 0, auth: { kind: "web" } },
			});
		mount("/setup", <SetupPage />);
		expect(
			screen.getByRole("heading", { name: t("auth.rePasswordTitle") }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: t("auth.usePasskeyInstead") }),
		).not.toBeInTheDocument();
		fireEvent.click(
			screen.getByRole("button", { name: t("auth.startPasswordSetup") }),
		);
		await screen.findByRole("img", { name: t("auth.totpQrLabel") });
		typeInto(/^密码/, "correct horse");
		typeInto(/^确认密码/, "correct horse");
		typeInto(t("auth.totpCode"), "111222");
		fireEvent.click(
			screen.getByRole("button", { name: t("auth.finishSetup") }),
		);
		expect(await screen.findByText("home")).toBeInTheDocument();
		await waitFor(() =>
			expect(calls.map((c) => c.path)).toEqual([
				"/api/v1/auth/password/setup/begin",
				"/api/v1/auth/password/setup/finish",
			]),
		);
		expect(calls[0]?.body).toEqual({});
	});
});
