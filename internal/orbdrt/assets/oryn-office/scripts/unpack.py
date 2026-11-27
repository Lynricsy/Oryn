"""把 Office 文件（docx / xlsx / pptx 及启用宏的变体）解包成目录，便于直接改 XML。

用法：python unpack.py <文件> <目录>

目录必须不存在或为空。每个条目按原路径写出，条目顺序记在目录里的 .oryn-order（每行一个条目名），
pack.py 回包时按它排序。XML 原样写出、不重新排版（重新排版会改动 xml:space="preserve" 里的空白）。
"""

import pathlib
import sys
import zipfile

ORDER_FILE = ".oryn-order"


def safe_target(root: pathlib.Path, name: str) -> pathlib.Path:
    """返回条目在 root 下的路径；绝对路径、带 .. 或反斜杠的条目名直接拒绝。"""
    parts = name.split("/")
    if name.startswith("/") or "\\" in name or ".." in parts or ORDER_FILE in parts:
        raise ValueError(f"不安全的条目名：{name!r}")
    return root.joinpath(*parts)


def main() -> int:
    if len(sys.argv) != 3:
        print("用法：python unpack.py <文件> <目录>", file=sys.stderr)
        return 2
    src, root = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    if root.exists() and (not root.is_dir() or any(root.iterdir())):
        print(f"{root} 已存在且不是空目录", file=sys.stderr)
        return 2
    try:
        zf = zipfile.ZipFile(src)
    except (OSError, zipfile.BadZipFile) as err:
        print(f"{src} 不是 zip 包（旧版 .doc/.xls/.ppt 或加密文件先用 soffice 转换）：{err}", file=sys.stderr)
        return 2
    order = []
    with zf:
        for info in zf.infolist():
            if info.is_dir():
                continue
            try:
                target = safe_target(root, info.filename)
            except ValueError as err:
                print(err, file=sys.stderr)
                return 2
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(zf.read(info))
            order.append(info.filename)
    (root / ORDER_FILE).write_text("\n".join(order) + "\n", encoding="utf-8")
    print(f"已解包 {len(order)} 个条目到 {root}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
