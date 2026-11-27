/**
 * WebAuthn 仪式的小工具：登录页、初始化页与设置页共用。
 */

/**
 * begin 返回的是 `publicKey` 内层 JSON（WebAuthn L3 JSON 形态，由 @simplewebauthn/browser 解码，
 * 等价于 `PublicKeyCredential.parseCreationOptionsFromJSON / parseRequestOptionsFromJSON`）；
 * 若服务端仍包了一层 `{publicKey: …}` 也兼容。
 */
export function unwrapOptions<T>(options: Record<string, unknown>): T {
	const inner = options.publicKey;
	return (inner && typeof inner === "object" ? inner : options) as T;
}
