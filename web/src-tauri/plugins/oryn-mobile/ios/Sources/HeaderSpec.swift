// 原生页头的数据形状（与插件 crate 的 `models.rs` 的 `Header*` 一一对应；字段名同 JSON）。
// 只做解码，校验在壳的 `mobile_logic::valid_header`。
import CoreGraphics
import Foundation

/// 控件在网页布局视口里的矩形（CSS px，= pt：页面缩放恒为 1）。
struct HeaderFrameSpec: Decodable, Equatable {
  let x: Double
  let y: Double
  let width: Double
  let height: Double
  var rect: CGRect { CGRect(x: x, y: y, width: width, height: height) }
}

/// 四选一：symbol / glyph（可带 bg、fg）/ dot / orb。颜色一律 `#rrggbb`。
struct HeaderIconSpec: Decodable, Equatable {
  let symbol: String?
  let glyph: String?
  let bg: String?
  let fg: String?
  let dot: String?
  /// `booting` / `ready` / `working` / `freezing` / `frozen` / `stopped` / `deleted` / `failed` / `err`。
  let orb: String?
}

/// 一段「图形 + 文字」。
struct HeaderSegSpec: Decodable, Equatable {
  let text: String
  let icon: HeaderIconSpec?
  /// `err` 或缺省。
  let tone: String?
}

struct HeaderMarkSpec: Decodable, Equatable {
  let symbol: String
  let dim: Bool?
}

struct HeaderItemSpec: Decodable, Equatable, Identifiable {
  let id: String
  let label: String
  let icon: HeaderIconSpec?
  let sub: HeaderSegSpec?
  let hint: String?
  /// `accent` / `warn`。
  let dot: String?
  let pulse: Bool?
  let checked: Bool?
  let danger: Bool?
  let disabled: Bool?
  let copy: String?
  let children: [HeaderItemSpec]?
}

struct HeaderMenuSpec: Decodable, Equatable {
  let label: String
  let dim: Bool
  let quick: [HeaderItemSpec]?
  let status: [HeaderItemSpec]?
  let groups: [[HeaderItemSpec]]
  /// 顶层全部项（找子页的父项用）。
  var tops: [HeaderItemSpec] { (quick ?? []) + (status ?? []) + groups.flatMap { $0 } }
}

struct HeaderPaletteSpec: Decodable, Equatable {
  let accent: String
  let brand: String
  let err: String
  let warn: String
  let good: String
  let frost: String
}

struct HeaderTuneSpec: Decodable, Equatable {
  let scrim: Double
  let tint: Double
}

struct HeaderTextSpec: Decodable, Equatable {
  let back: String
  let close: String
}

struct HeaderLeadSpec: Decodable, Equatable {
  let frame: HeaderFrameSpec
  let symbol: String
  let label: String
}

struct HeaderTitleSpec: Decodable, Equatable {
  let frame: HeaderFrameSpec
  let text: String
  let icon: HeaderIconSpec?
  let error: String?
  let meta: [HeaderSegSpec]
  let marks: [HeaderMarkSpec]
  let label: String
  let menu: HeaderMenuSpec
}

struct HeaderTrailSpec: Decodable, Equatable {
  let frame: HeaderFrameSpec
  let symbol: String
  let label: String
  let dot: String?
  let menu: HeaderMenuSpec
}

/// 原生页头的整份状态（`headerSet` 的 `spec`）。
struct HeaderSpec: Decodable, Equatable {
  /// 线程 id（只有侧栏钮的页是页名，如 `home`）：变了收起菜单；事件原样带回。
  let key: String
  let dark: Bool
  let palette: HeaderPaletteSpec
  let tune: HeaderTuneSpec
  let text: HeaderTextSpec
  let lead: HeaderLeadSpec
  /// 没有标题胶囊（首页：只有侧栏钮）时为 nil。
  let title: HeaderTitleSpec?
  let trail: HeaderTrailSpec?
}

/// `headerSet` 的载荷：`{"spec": <spec|null>}`。
struct HeaderSetArgs: Decodable {
  let spec: HeaderSpec?
}

/// `headerShow` 的载荷。
struct HeaderShowArgs: Decodable {
  let alpha: Double
  let dx: Double
  let passive: Bool
  let ms: Double?
  let delay: Double?
  let ease: [Double]?
  /// 起步的绝对时刻（Unix 毫秒）；有它时忽略 `delay`。
  let at: Double?
}

/// `headerMenu` 的载荷：`open` 为 `title` / `trail` / null；`page` = 子页的父项 id。
struct HeaderMenuArgs: Decodable {
  let open: String?
  let page: String?
}

/// `headerProbe` 的返回（调试）。
struct HeaderProbeOut: Encodable {
  let center: Bool
  let lead: Bool
  let title: Bool
  let trail: Bool
}
