// 原生底栏的数据形状（与插件 crate 的 `models.rs` 的 `TabBar*` 一一对应；字段名同 JSON）。
// 只做解码，校验在壳的 `mobile_logic::valid_tab_bar`。
import CoreGraphics
import Foundation

/// `{symbol}` / `{glyph}` / `{image}` 三选一（外部标记的单键对象）。
struct TabBarIconSpec: Decodable, Equatable {
  let symbol: String?
  let glyph: String?
  let image: String?
}

struct TabBarItemSpec: Decodable, Equatable {
  let id: String
  let label: String
  let icon: TabBarIconSpec
  /// `accent` / `warn` / `err`。
  let badge: String?
}

/// 网页胶囊在布局视口里的矩形（CSS px，= pt：页面缩放恒为 1）。
struct TabBarFrameSpec: Decodable, Equatable {
  let x: Double
  let y: Double
  let width: Double
  let height: Double
  var rect: CGRect { CGRect(x: x, y: y, width: width, height: height) }
}

struct TabBarSpec: Decodable, Equatable {
  let items: [TabBarItemSpec]
  let selected: String
  let frame: TabBarFrameSpec
  let dark: Bool
  /// `#rrggbb`；nil = 系统强调色。
  let accent: String?
}

/// `tabBarSet` 的载荷：`{"spec": <spec|null>}`。
struct TabBarSetArgs: Decodable {
  let spec: TabBarSpec?
}

/// `tabBarReveal` 的载荷：卡片左缘（视口 x，底栏右缘裁到这里）、网页内容是否压在底栏位置上，
/// 以及过渡：`ms` 为 0 立即就位，否则从绝对时刻 `at`（Unix 毫秒，缺省 = 收到即起步）按 `ease` 插值。
struct TabBarRevealArgs: Decodable {
  let x: Double
  let covered: Bool
  let ms: Double
  let ease: [Double]?
  let at: Double?
}
