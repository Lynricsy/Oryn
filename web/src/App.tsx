import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { fetchHealth } from "./health";

export function App() {
	const { t } = useTranslation();
	const health = useQuery({
		queryKey: ["healthz"],
		queryFn: ({ signal }) => fetchHealth(signal),
	});

	return (
		<main className="flex min-h-dvh flex-col items-center justify-center gap-2 bg-neutral-50 p-6 text-neutral-900 dark:bg-neutral-950 dark:text-neutral-100">
			<h1 className="font-semibold text-3xl tracking-tight">
				{health.data
					? `${t("app.name")} ${health.data.version}`
					: t("app.name")}
			</h1>
			<p className="text-neutral-500 text-sm">{t("app.tagline")}</p>
			<p className="text-neutral-500 text-sm" role="status">
				{health.isPending && t("health.loading")}
				{health.isError && t("health.error", { message: health.error.message })}
				{health.data && t("health.version", { version: health.data.version })}
			</p>
		</main>
	);
}
