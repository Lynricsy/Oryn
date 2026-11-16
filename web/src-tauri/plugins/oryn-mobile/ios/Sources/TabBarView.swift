// 底栏的 SwiftUI 胶囊：四等分，20pt 图标 + 10pt 文字，选中项强调色 + 滑动底板；材质是 Liquid Glass（iOS 26）。
// 选中项只听网页的（当前分区 = 路由），点击只上报、不自己改选中。
import SwiftUI
import UIKit

/// `{symbol}` / `{glyph}` / `{image}` 三选一（外部标记的单键对象）。
struct TabBarIconSpec: Decodable {
  let symbol: String?
  let glyph: String?
  let image: String?
}

struct TabBarItemSpec: Decodable, Identifiable {
  let id: String
  let label: String
  let icon: TabBarIconSpec
  /// `accent` / `warn` / `err`。
  let badge: String?
}

/// 胶囊在网页布局视口里的矩形（CSS px，= pt：页面缩放恒为 1）。
struct TabBarFrameSpec: Decodable {
  let x: Double
  let y: Double
  let width: Double
  let height: Double
}

struct TabBarSpec: Decodable {
  let items: [TabBarItemSpec]
  let selected: String
  let frame: TabBarFrameSpec
  let dark: Bool
  /// `#rrggbb`；nil = 系统强调色。
  let accent: String?
}

final class TabBarModel: ObservableObject {
  @Published var items: [TabBarItemSpec] = []
  @Published var selected = ""
  @Published var accent: Color = .accentColor
  /// 已解码的头像：项 id → (base64, 图)；base64 没变就不重新解码。
  private var decoded: [String: (String, UIImage)] = [:]
  var onTap: (String) -> Void = { _ in }

  func update(_ spec: TabBarSpec) {
    for item in spec.items {
      guard let b64 = item.icon.image, decoded[item.id]?.0 != b64 else { continue }
      if let data = Data(base64Encoded: b64), let image = UIImage(data: data) {
        decoded[item.id] = (b64, image)
      }
    }
    items = spec.items
    selected = spec.selected
    accent = hexColor(spec.accent) ?? .accentColor
  }

  func image(_ item: TabBarItemSpec) -> UIImage? {
    guard let b64 = item.icon.image, let hit = decoded[item.id], hit.0 == b64 else { return nil }
    return hit.1
  }
}

@available(iOS 26.0, *)
struct TabBarView: View {
  @ObservedObject var model: TabBarModel
  @Namespace private var plate

  var body: some View {
    HStack(spacing: 0) {
      ForEach(model.items) { item in
        let on = item.id == model.selected
        Button {
          model.onTap(item.id)
        } label: {
          VStack(spacing: 2) {
            icon(item, on: on)
              .frame(width: 20, height: 20)
              .overlay(alignment: .topTrailing) { badge(item.badge) }
            Text(item.label)
              .font(.system(size: 10, weight: on ? .semibold : .medium))
              .lineLimit(1)
          }
          .padding(.horizontal, 4)
          .frame(maxWidth: .infinity, maxHeight: .infinity)
          .foregroundStyle(on ? model.accent : Color.secondary)
          .background {
            if on {
              Capsule()
                .fill(Color.primary.opacity(0.08))
                .matchedGeometryEffect(id: "plate", in: plate)
            }
          }
          .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(item.label))
        .accessibilityAddTraits(on ? .isSelected : [])
      }
    }
    .padding(4)
    .frame(maxWidth: .infinity, maxHeight: .infinity)
    .glassEffect(.regular.interactive(), in: Capsule())
    .animation(.easeOut(duration: 0.2), value: model.selected)
  }

  @ViewBuilder
  private func icon(_ item: TabBarItemSpec, on: Bool) -> some View {
    if let image = model.image(item) {
      Image(uiImage: image).resizable().scaledToFill().clipShape(Circle())
    } else if let glyph = item.icon.glyph {
      Text(glyph).font(.system(size: 13))
        .frame(width: 20, height: 20)
        .background(model.accent.opacity(0.14), in: Circle())
    } else {
      Image(systemName: item.icon.symbol ?? "circle")
        .symbolVariant(on ? .fill : .none)
        .font(.system(size: 17))
    }
  }

  @ViewBuilder
  private func badge(_ kind: String?) -> some View {
    if let kind = kind {
      Circle()
        .fill(kind == "err" ? Color.red : kind == "warn" ? Color.orange : model.accent)
        .frame(width: 7, height: 7)
        .offset(x: 4, y: -2)
    }
  }
}
