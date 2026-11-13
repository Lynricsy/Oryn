/** 附件的公开发布控件（附件对话框每行）：未发布 →「发布」；已发布 →「撤回」（点一次变「确认撤回」）。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import {
	type PublishedFile,
	usePublishAttachment,
	useRevokePublishedFile,
} from "../../api/share-queries";
import { Button } from "../ui/primitives";
import { ConfirmButton } from "./ConfirmButton";

export function AttachmentPublishButton({
	attachment,
	published,
	onError,
}: {
	attachment: Schemas["Attachment"];
	/** 设置页总览里对应的发布记录（撤回要用它的 id）；总览未加载完时为空。 */
	published: PublishedFile | undefined;
	/** 失败时交给对话框统一显示 */
	onError: (e: unknown) => void;
}): ReactNode {
	const { t } = useTranslation();
	const publish = usePublishAttachment();
	const revoke = useRevokePublishedFile();
	if (!attachment.publicUrl)
		return (
			<Button
				size="sm"
				variant="ghost"
				icon="globe"
				disabled={publish.isPending}
				onClick={() => publish.mutate(attachment.id, { onError })}
			>
				{t("publish.publish")}
			</Button>
		);
	return (
		<ConfirmButton
			label={t("publish.unpublish")}
			confirmLabel={t("publish.unpublishConfirm")}
			disabled={revoke.isPending || !published}
			onConfirm={() => {
				if (published) revoke.mutate(published.id, { onError });
			}}
		/>
	);
}
