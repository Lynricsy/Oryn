#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["python-docx", "openpyxl", "python-pptx", "reportlab", "pillow"]
# ///
"""生成 Office 查看器的样本与人工对照基准图（M31 步骤 5.1）。

用法（仓库根目录）：uv run web/scripts/office-fixtures.py
产物：web/sandbox/viewers/testdata/office/sample.{docx,xlsx,pptx,pdf}，以及 soffice 转 PDF 后
pdftoppm -r 50 得到的 <名>.ref-<页>.png（查看器截图的人工对照基准，不参与自动比较）。
每个样本都带有计划 5.1 清单里「必须可见」的内容；查看器选型（5.5）按截图逐项核对。
"""

from __future__ import annotations

import io
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path

from docx import Document
from docx.enum.text import WD_BREAK
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from docx.shared import Pt, RGBColor
from docx.shared import Cm
from openpyxl import Workbook
from openpyxl.chart import BarChart, Reference
from openpyxl.styles import Alignment, Font, PatternFill
from PIL import Image, ImageDraw
from pptx import Presentation
from pptx.chart.data import CategoryChartData
from pptx.dml.color import RGBColor as PptxRGB
from pptx.enum.chart import XL_CHART_TYPE
from pptx.enum.shapes import MSO_SHAPE
from pptx.util import Emu, Inches
from pptx.util import Pt as PptxPt
from reportlab.lib.pagesizes import A4
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.cidfonts import UnicodeCIDFont
from reportlab.pdfgen import canvas

OUT = Path(__file__).resolve().parent.parent / "sandbox/viewers/testdata/office"


def png_bytes() -> bytes:
    """一张 240×160 的示意图：蓝底、橙色圆、白色对角线，截图里一眼能认出来。"""
    img = Image.new("RGB", (240, 160), (37, 99, 235))
    d = ImageDraw.Draw(img)
    d.ellipse((70, 30, 170, 130), fill=(249, 115, 22))
    d.line((0, 0, 240, 160), fill=(255, 255, 255), width=6)
    buf = io.BytesIO()
    img.save(buf, "PNG")
    return buf.getvalue()


def set_cjk_font(run, name: str = "Noto Sans CJK SC") -> None:
    run.font.name = name
    rpr = run._element.get_or_add_rPr()
    fonts = rpr.find(qn("w:rFonts"))
    if fonts is None:
        fonts = OxmlElement("w:rFonts")
        rpr.append(fonts)
    fonts.set(qn("w:eastAsia"), name)


def make_docx(path: Path) -> None:
    doc = Document()
    h = doc.add_heading(level=0)
    set_cjk_font(h.add_run("季度报告 Q3"))
    p = doc.add_paragraph()
    set_cjk_font(
        p.add_run(
            "本季度营收稳步增长，主要来自华东与华南两个区域的新客户。"
            "研发投入保持在营收的百分之十五左右，交付周期较上季度缩短了两周。"
        )
    )
    p = doc.add_paragraph()
    r = p.add_run("加粗的结论")
    r.bold = True
    set_cjk_font(r)
    set_cjk_font(p.add_run("，以及"))
    r = p.add_run("红色的风险提示")
    r.font.color.rgb = RGBColor(0xDC, 0x26, 0x26)
    set_cjk_font(r)
    set_cjk_font(p.add_run("。"))

    table = doc.add_table(rows=3, cols=3)
    table.style = "Table Grid"
    rows = [("区域", "营收", "增长"), ("华东", "1,280", "12%"), ("华南", "960", "8%")]
    for i, row in enumerate(rows):
        for j, text in enumerate(row):
            cell = table.cell(i, j)
            cell.text = ""
            set_cjk_font(cell.paragraphs[0].add_run(text))

    doc.add_paragraph()
    doc.add_picture(io.BytesIO(png_bytes()), width=Cm(6))

    brk = doc.add_paragraph().add_run()
    brk.add_break(WD_BREAK.PAGE)
    h = doc.add_heading(level=1)
    set_cjk_font(h.add_run("附录"))
    p = doc.add_paragraph()
    set_cjk_font(p.add_run("数据来源：内部财务系统，截至九月三十日。"))
    doc.styles["Normal"].font.size = Pt(11)
    doc.save(path)


def make_xlsx(path: Path) -> None:
    wb = Workbook()
    ws = wb.active
    ws.title = "汇总"
    ws.merge_cells("A1:D1")
    ws["A1"] = "季度营收汇总"
    ws["A1"].font = Font(size=16, bold=True)
    ws["A1"].alignment = Alignment(horizontal="center")
    ws.row_dimensions[1].height = 24
    head = PatternFill("solid", fgColor="FDE68A")
    for col, text in zip("ABCD", ("区域", "营收", "占比", "增长")):
        c = ws[f"{col}2"]
        c.value = text
        c.fill = head
        c.font = Font(bold=True)
    data = [("华东", 1280, 0.12), ("华南", 960, 0.08), ("华北", 720, 0.05)]
    for i, (region, revenue, growth) in enumerate(data, start=3):
        ws[f"A{i}"] = region
        ws[f"B{i}"] = revenue
        ws[f"B{i}"].number_format = '"¥"#,##0.00'
        ws[f"C{i}"] = f"=B{i}/$B$6"
        ws[f"C{i}"].number_format = "0.0%"
        ws[f"D{i}"] = growth
        ws[f"D{i}"].number_format = "0%"
    ws["A6"] = "合计"
    ws["A6"].font = Font(bold=True)
    ws["B6"] = "=SUM(B3:B5)"
    ws["B6"].number_format = '"¥"#,##0.00'
    for col, width in zip("ABCD", (12, 16, 10, 10)):
        ws.column_dimensions[col].width = width

    chart = BarChart()
    chart.title = "各区域营收"
    chart.add_data(Reference(ws, min_col=2, min_row=2, max_row=5), titles_from_data=True)
    chart.set_categories(Reference(ws, min_col=1, min_row=3, max_row=5))
    chart.height, chart.width = 7, 12
    ws.add_chart(chart, "F2")

    detail = wb.create_sheet("明细")
    detail.append(["日期", "区域", "金额"])
    for i in range(1, 11):
        detail.append([f"2026-09-{i:02d}", ("华东", "华南", "华北")[i % 3], 100 * i])
    wb.save(path)
    cache_formula_values(path, data)


def cache_formula_values(path: Path, data: list[tuple[str, int, float]]) -> None:
    """openpyxl 只写公式不写缓存值；查看器不重算，所以按 Excel 的做法把算好的结果写进 <v>。"""
    total = sum(r for _, r, _ in data)
    values = {"B6": str(total)}
    for i, (_, revenue, _) in enumerate(data, start=3):
        values[f"C{i}"] = repr(revenue / total)
    src = path.read_bytes()
    out = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(src)) as zin, zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            body = zin.read(item.filename)
            if item.filename == "xl/worksheets/sheet1.xml":
                xml = body.decode()
                for ref, value in values.items():
                    xml, n = re.subn(
                        rf'(<c r="{ref}"[^>]*>)(<f>[^<]*</f>)(<v\s*/>|<v></v>)?',
                        rf"\1\2<v>{value}</v>",
                        xml,
                    )
                    if n != 1:
                        raise SystemExit(f"没找到公式单元格 {ref}")
                body = xml.encode()
            zout.writestr(item, body)
    path.write_bytes(out.getvalue())


def make_pptx(path: Path) -> None:
    prs = Presentation()
    prs.slide_width, prs.slide_height = Inches(13.333), Inches(7.5)

    s = prs.slides.add_slide(prs.slide_layouts[0])
    s.shapes.title.text = "季度经营回顾"
    s.placeholders[1].text = "2026 年第三季度"

    s = prs.slides.add_slide(prs.slide_layouts[5])
    s.shapes.title.text = "本季要点"
    box = s.shapes.add_textbox(Inches(0.8), Inches(1.8), Inches(6), Inches(3.5))
    tf = box.text_frame
    for i, text in enumerate(("营收同比增长 12%", "新增客户 48 家", "交付周期缩短两周")):
        para = tf.paragraphs[0] if i == 0 else tf.add_paragraph()
        para.text = "• " + text
        para.font.size = PptxPt(24)
    shape = s.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, Inches(7.4), Inches(1.8), Inches(2.6), Inches(1.6))
    shape.fill.solid()
    shape.fill.fore_color.rgb = PptxRGB(0x10, 0xB9, 0x81)
    shape.text_frame.text = "目标达成"
    s.shapes.add_picture(io.BytesIO(png_bytes()), Inches(7.4), Inches(3.8), width=Inches(3.6))

    s = prs.slides.add_slide(prs.slide_layouts[5])
    s.shapes.title.text = "区域数据"
    rows = [("区域", "营收", "增长"), ("华东", "1,280", "12%"), ("华南", "960", "8%")]
    table = s.shapes.add_table(3, 3, Inches(0.8), Inches(1.8), Inches(5.5), Inches(1.8)).table
    for i, row in enumerate(rows):
        for j, text in enumerate(row):
            table.cell(i, j).text = text
    cd = CategoryChartData()
    cd.categories = ["华东", "华南", "华北"]
    cd.add_series("营收", (1280, 960, 720))
    s.shapes.add_chart(
        XL_CHART_TYPE.COLUMN_CLUSTERED, Inches(6.8), Inches(1.8), Inches(5.8), Inches(4.5), cd
    )
    prs.save(path)


def make_pdf(path: Path) -> None:
    pdfmetrics.registerFont(UnicodeCIDFont("STSong-Light"))
    c = canvas.Canvas(str(path), pagesize=A4)
    w, h = A4
    c.setFont("STSong-Light", 24)
    c.drawString(72, h - 96, "季度报告 PDF 样本")
    c.setFont("STSong-Light", 12)
    c.drawString(72, h - 132, "第一页：营收稳步增长，华东与华南贡献主要增量。")
    c.setFillColorRGB(0.15, 0.39, 0.92)
    c.rect(72, h - 260, 200, 90, fill=1, stroke=0)
    c.showPage()
    c.setFont("STSong-Light", 24)
    c.drawString(72, h - 96, "第二页 附录")
    c.setFont("STSong-Light", 12)
    c.drawString(72, h - 132, "数据来源：内部财务系统。")
    c.showPage()
    c.save()


def render_refs(sample: Path) -> None:
    """soffice 转 PDF（PDF 样本直接用）后 pdftoppm -r 50 出每页的对照图。"""
    for old in OUT.glob(f"{sample.name}.ref-*.png"):
        old.unlink()
    with tempfile.TemporaryDirectory() as tmp:
        pdf = sample
        if sample.suffix != ".pdf":
            subprocess.run(
                ["soffice", "--headless", "--convert-to", "pdf", "--outdir", tmp, str(sample)],
                check=True,
                capture_output=True,
            )
            pdf = Path(tmp) / (sample.stem + ".pdf")
        prefix = Path(tmp) / "ref"
        subprocess.run(["pdftoppm", "-png", "-r", "50", str(pdf), str(prefix)], check=True)
        for png in sorted(Path(tmp).glob("ref-*.png")):
            page = int(png.stem.split("-")[-1])
            shutil.copy(png, OUT / f"{sample.name}.ref-{page}.png")


def main() -> int:
    OUT.mkdir(parents=True, exist_ok=True)
    makers = {
        "sample.docx": make_docx,
        "sample.xlsx": make_xlsx,
        "sample.pptx": make_pptx,
        "sample.pdf": make_pdf,
    }
    for name, make in makers.items():
        make(OUT / name)
        render_refs(OUT / name)
        print(f"wrote {OUT / name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
