/**
 * 转录里资源（图片 blob、附件）的地址：站内走私有接口；匿名分享页（`/s/<slug>`）用 Provider 换成公开接口，
 * 同一套转录渲染因此能原样跑在不登录的页面上。
 */
import { createContext, useContext } from "react";
import { blobUrl } from "../api/client";
import { attachmentContentUrl } from "./attachments";

export interface AssetUrls {
	/** 转录图片 `blob:sha256:<hex>` 的读取地址。 */
	blob(sha256: string): string;
	/** 附件地址；`inline` 为 true 时请求栅格图内联预览，否则下载；null = 不可访问（芯片只显示名称）。 */
	attachment(id: string, inline?: boolean): string | null;
}

/** 站内（已登录）地址。 */
export const PRIVATE_ASSET_URLS: AssetUrls = {
	blob: blobUrl,
	attachment: (id, inline) => attachmentContentUrl(id, inline),
};

export const AssetUrlContext = createContext<AssetUrls>(PRIVATE_ASSET_URLS);

export function useAssetUrls(): AssetUrls {
	return useContext(AssetUrlContext);
}
