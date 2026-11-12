/** /healthz 的应答（与 schema/openapi.yaml 中的定义一致）。 */
export interface Health {
	ok: boolean;
	version: string;
}

export async function fetchHealth(signal?: AbortSignal): Promise<Health> {
	const res = await fetch("/healthz", { signal });
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	return (await res.json()) as Health;
}
