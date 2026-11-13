/** 按文件名（及可选 MIME）挑文件图标：文件树、文件查看器、上传队列、附件条与消息附件共用。 */
import type { IconName } from "../components/ui/icons";

const CODE_EXT =
	/\.(c|cc|cpp|cs|css|go|h|hpp|html|java|js|jsx|json|kt|lua|mjs|php|py|rb|rs|scss|sh|sql|svelte|swift|toml|ts|tsx|vue|xml|ya?ml|zig)$/i;
const IMAGE_EXT = /\.(avif|bmp|gif|heic|ico|jpe?g|png|svg|webp)$/i;
const TEXT_EXT = /\.(csv|log|md|markdown|mdx|rst|txt)$/i;

export function fileIcon(name: string, mime?: string): IconName {
	if (mime?.startsWith("image/") || IMAGE_EXT.test(name)) return "fileImage";
	if (CODE_EXT.test(name)) return "fileCode";
	if (mime?.startsWith("text/") || TEXT_EXT.test(name)) return "fileText";
	return "file";
}
