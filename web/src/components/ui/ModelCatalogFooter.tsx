/**
 * 模型列表的页脚（线程的模型对话框、首屏 role 胶囊的「指定模型…」）：目录是旧结果时给一行淡色提示，
 * 「刷新」让控制面强制重跑目录（`?refresh=true`），结果直接写回缓存。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type ModelCatalog,
	refreshModelCatalog,
} from "../../api/model-catalog-queries";
import { formatDateTime } from "../../lib/format";
import { Icon } from "./icons";
import { Button, describeError, Spinner } from "./primitives";

export function ModelCatalogFooter({
	catalog,
}: {
	catalog: ModelCatalog | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const refresh = async () => {
		setBusy(true);
		setError(null);
		try {
			await refreshModelCatalog(qc);
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	return (
		<div className="flex w-full min-w-0 items-center gap-2 text-xs">
			<span className="min-w-0 flex-1 truncate">
				{error ? (
					<span role="alert" className="text-err">
						{describeError(error, t)}
					</span>
				) : (
					catalog?.stale && (
						<span data-testid="catalog-stale" className="text-faint">
							{t("model.catalogStale", {
								time: formatDateTime(catalog.fetchedAt),
							})}
						</span>
					)
				)}
			</span>
			<Button
				size="sm"
				variant="ghost"
				disabled={busy}
				onClick={() => void refresh()}
			>
				{busy ? <Spinner size={12} /> : <Icon name="refresh" size={13} />}
				{t("model.refresh")}
			</Button>
		</div>
	);
}
