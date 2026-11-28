// 原生竖栏的数据形状（与插件 crate 的 `models.rs` 的 `Rail*` 一一对应；字段名同 JSON）。
// 只做解码，校验在壳的 `mobile_logic::valid_rail`。分区项与原生底栏同一类型，菜单与调色板与原生页头同一类型。
import Foundation

/// 侧栏开关；`active` = 侧栏开着。
struct RailLeadSpec: Decodable, Equatable {
  let label: String
  let active: Bool
}

/// 页头操作：有 `menu` 的是系统菜单钮，没有的是普通钮。
struct RailActionSpec: Decodable, Equatable, Identifiable {
  let id: String
  let symbol: String
  let label: String
  let active: Bool?
  /// `accent` / `warn`。
  let dot: String?
  let menu: HeaderMenuSpec?
}

/// 竖排的分区。`selected` 是某一项的 id。
struct RailTabsSpec: Decodable, Equatable {
  let label: String
  let items: [TabBarItemSpec]
  let selected: String
}

/// 原生竖栏的整份内容（`railSet` 的 `spec`）。矩形不在这里：原生 `RailProbe` 自己算。
struct RailSpec: Decodable, Equatable {
  /// 页面键：变了收起开着的菜单；事件原样带回。
  let key: String
  let dark: Bool
  let palette: HeaderPaletteSpec
  /// 网页内容压在竖栏上：隐藏且不接触摸。
  let covered: Bool
  let lead: RailLeadSpec
  let actions: [RailActionSpec]
  let tabs: RailTabsSpec
}

/// `railSet` 的载荷：`{"spec": <spec|null>}`。
struct RailSetArgs: Decodable {
  let spec: RailSpec?
}
