// 自绘菜单面板，版式照搬网页（CompactThreadHeader 的 TitleMenu 与共享 Menu），材质换成 Liquid Glass。
// wide = 标题菜单（圆角 22、内边距 6、三宫格 + 状态块 + 分组）；否则是视图菜单（圆角 18、内边距 4、单列）。
// 子页在面板里原地换页：顶部一行「← 父项名」，下面是子项；内容比上限高时滚动。
import SwiftUI
import UIKit

@available(iOS 26.0, *)
struct MenuPanel: View {
  @ObservedObject var model: HeaderModel
  let menu: HeaderMenuSpec
  let wide: Bool
  let spec: HeaderSpec
  let palette: HeaderPalette
  let maxHeight: CGFloat

  var body: some View {
    // 内容放得下就不滚动；放不下才套 ScrollView
    ViewThatFits(in: .vertical) {
      content
      ScrollView { content }.scrollBounceBehavior(.basedOnSize)
    }
    .frame(maxHeight: maxHeight)
    .glassEffect(
      .regular.tint(Color(uiColor: .systemBackground).opacity(spec.tune.tint)),
      in: RoundedRectangle(cornerRadius: wide ? 22 : 18, style: .continuous)
    )
    .accessibilityElement(children: .contain)
    .accessibilityLabel(Text(menu.label))
  }

  /// 主页的分组前是否要一条细线：标题菜单在宫格 / 状态块之后才要（第一组之前）。
  private var ruleBeforeFirstGroup: Bool {
    wide && (!(menu.quick ?? []).isEmpty || !(menu.status ?? []).isEmpty)
  }

  private var content: some View {
    VStack(spacing: 0) {
      if let parent = model.pageItem {
        Button {
          _ = model.back()
        } label: {
          HStack(spacing: 8) {
            Image(systemName: "arrow.left").font(.system(size: 14))
            Text(parent.label).lineLimit(1)
            Spacer(minLength: 0)
          }
          .font(.system(size: 12))
          .foregroundStyle(.secondary)
          .padding(.horizontal, 10)
          .padding(.vertical, 6)
        }
        .buttonStyle(MenuRowStyle(radius: 12))
        .accessibilityLabel(Text("\(spec.text.back), \(parent.label)"))
        rule
        ForEach(parent.children ?? []) { row($0) }
      } else {
        if let quick = menu.quick, !quick.isEmpty {
          HStack(spacing: 2) { ForEach(quick) { quickCell($0) } }.padding(.bottom, 4)
        }
        if let status = menu.status, !status.isEmpty {
          VStack(spacing: 0) { ForEach(status) { row($0) } }
            .background(
              Color.primary.opacity(0.04),
              in: RoundedRectangle(cornerRadius: 14, style: .continuous)
            )
            .padding(.vertical, 4)
        }
        ForEach(Array(menu.groups.enumerated()), id: \.offset) { index, group in
          // 宫格 / 状态块之后、以及组与组之间各一条细线
          if index > 0 || ruleBeforeFirstGroup { rule }
          ForEach(group) { row($0) }
        }
      }
    }
    .padding(wide ? 6 : 4)
    .id(model.page ?? "")
    .transition(.opacity)
  }

  /// 细线：1 高，左右 10、上下 4，文字色 10%。
  private var rule: some View {
    Rectangle().fill(Color.primary.opacity(0.1)).frame(height: 1)
      .padding(.horizontal, 10).padding(.vertical, 4)
      .accessibilityHidden(true)
  }

  private func tint(_ item: HeaderItemSpec) -> Color {
    item.danger == true ? palette.err : Color.primary
  }

  /// 三宫格的一格：图标 18、字 11.5、上 8 下 6 左右 4、圆角 14。
  private func quickCell(_ item: HeaderItemSpec) -> some View {
    Button {
      model.choose(item)
    } label: {
      VStack(spacing: 4) {
        if let icon = item.icon { HeaderIconView(icon: icon, box: 18, palette: palette) }
        Text(item.label).font(.system(size: 11.5)).lineLimit(1)
      }
      .foregroundStyle(tint(item))
      .padding(.horizontal, 4)
      .padding(.top, 8)
      .padding(.bottom, 6)
      .frame(maxWidth: .infinity)
    }
    .buttonStyle(MenuRowStyle(radius: 14))
    .disabled(item.disabled == true)
    .opacity(item.disabled == true ? 0.4 : 1)
    .accessibilityElement(children: .ignore)
    .accessibilityLabel(Text(item.label))
    .accessibilityAddTraits(.isButton)
  }

  private func row(_ item: HeaderItemSpec) -> some View {
    let hasKids = !(item.children ?? []).isEmpty
    let checked = item.checked == true
    return Button {
      model.choose(item)
    } label: {
      HStack(spacing: wide ? 10 : 8) {
        // 标题菜单的图标占 16pt 一格（没有图标也留着，各行文字对齐）；视图菜单 14pt、没有就不占
        if let icon = item.icon {
          HeaderIconView(icon: icon, box: wide ? 16 : 14, palette: palette)
            .foregroundStyle(item.danger == true ? palette.err : Color.secondary)
        } else if wide {
          Color.clear.frame(width: 16, height: 16)
        }
        VStack(alignment: .leading, spacing: 0) {
          HStack(spacing: 8) {
            Text(item.label)
              .font(.system(size: 14, weight: checked ? .medium : .regular))
              .lineLimit(1)
            if let dot = palette.dot(item.dot) { PulseDot(color: dot, pulse: item.pulse == true) }
          }
          if let sub = item.sub {
            SegView(seg: sub, palette: palette, box: 11)
              .font(.system(size: 11))
              .frame(height: 16)
          }
        }
        Spacer(minLength: 0)
        if let hint = item.hint {
          Text(hint).font(.system(size: 12).monospacedDigit()).foregroundStyle(.secondary)
        }
        if hasKids {
          Image(systemName: "chevron.right").font(.system(size: 13)).foregroundStyle(.tertiary)
        }
        if checked {
          Image(systemName: "checkmark")
            .font(.system(size: wide ? 14 : 13, weight: .bold))
            .foregroundStyle(palette.accent)
        }
      }
      .foregroundStyle(tint(item))
      .padding(.horizontal, wide ? 10 : 8)
      .padding(.vertical, 6)
      .frame(minHeight: wide ? 40 : 32)
    }
    .buttonStyle(MenuRowStyle(radius: 12))
    .disabled(item.disabled == true)
    .opacity(item.disabled == true ? 0.4 : 1)
    .accessibilityElement(children: .ignore)
    .accessibilityLabel(Text(item.label))
    .accessibilityValue(Text([item.sub?.text, item.hint].compactMap { $0 }.joined(separator: ", ")))
    .accessibilityAddTraits(checked ? [.isButton, .isSelected] : [.isButton])
  }
}

/// 行的按压底色（= 网页的 active:bg-active）。
@available(iOS 26.0, *)
struct MenuRowStyle: ButtonStyle {
  let radius: CGFloat

  func makeBody(configuration: Configuration) -> some View {
    configuration.label
      .contentShape(RoundedRectangle(cornerRadius: radius, style: .continuous))
      .background(
        Color.primary.opacity(configuration.isPressed ? 0.1 : 0),
        in: RoundedRectangle(cornerRadius: radius, style: .continuous))
  }
}

/// 6pt 提醒点；pulse 时明暗来回（= 网页的 animate-pulse，2s 一个来回），减弱动态效果时静止。
@available(iOS 26.0, *)
struct PulseDot: View {
  let color: Color
  let pulse: Bool
  @Environment(\.accessibilityReduceMotion) private var reduce

  var body: some View {
    if pulse && !reduce {
      TimelineView(.animation) { context in
        let t = context.date.timeIntervalSinceReferenceDate
        Circle().fill(color).frame(width: 6, height: 6).opacity(0.75 + 0.25 * cos(2 * .pi * t / 2))
      }
    } else {
      Circle().fill(color).frame(width: 6, height: 6)
    }
  }
}
