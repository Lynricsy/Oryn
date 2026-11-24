// 推送共享代码的单元测试（Notification Service Extension 与插件共用的 Shared/*.swift；ci/ios-push-unit.sh 驱动）。
// 模拟器的 simctl push 不调起 NSE（M28 P3），这里把 NSE 的整条处理链逐段跑一遍：
//   RFC 8291 附录 A 向量 → WebPushDecrypt 解出原文；
//   ci/ios-push/notify.apns 的密文（加密给附录 A 的接收端）→ 解密 → PushContent.parse → build 的标题、正文、归组与点击信息；
//   篡改密文、错的 auth 一律失败（NSE 据此交付占位）；PushKeyStore 在钥匙串里写入、读回、删除一套密钥。
import CryptoKit
import UserNotifications
import XCTest

@testable import PushUnitHost

final class PushSharedTests: XCTestCase {
  // RFC 8291 附录 A
  private static let receiverPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
  private static let auth = "BTBZMqHH6r4Tts7J_aSIgg"
  private static let vectorBody =
    "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6Tlz"
    + "AC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"

  private func data(_ base64url: String) throws -> Data {
    try XCTUnwrap(PushKeyStore.fromBase64url(base64url), "base64url 解不开：\(base64url.prefix(16))…")
  }

  private func receiver() throws -> P256.KeyAgreement.PrivateKey {
    try P256.KeyAgreement.PrivateKey(rawRepresentation: data(Self.receiverPrivate))
  }

  /// ci/ios-push/notify.apns 的 `e`（与 CI 推给 NSE 的是同一份载荷）。
  private func notifyCipher() throws -> Data {
    let url = try XCTUnwrap(
      Bundle(for: Self.self).url(forResource: "notify", withExtension: "apns"), "测试包里没有 notify.apns")
    let apns = try XCTUnwrap(
      JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any], "notify.apns 不是 JSON 对象")
    XCTAssertEqual(apns["v"] as? String, "2")
    let aps = try XCTUnwrap(apns["aps"] as? [String: Any])
    XCTAssertEqual(aps["mutable-content"] as? Int, 1, "网关通知必须带 mutable-content 才会调起 NSE")
    return try data(XCTUnwrap(apns["e"] as? String))
  }

  func testAppendixAVector() throws {
    let plain = try WebPushDecrypt.open(data(Self.vectorBody), privateKey: receiver(), auth: data(Self.auth))
    XCTAssertEqual(String(decoding: plain, as: UTF8.self), "When I grow up, I want to be a watermelon")
  }

  func testNotifyPayloadBuildsNotification() throws {
    let plain = try WebPushDecrypt.open(notifyCipher(), privateKey: receiver(), auth: data(Self.auth))
    let message = try XCTUnwrap(PushContent.parse(plain), "解出的明文不是合法的通知载荷")
    XCTAssertEqual(message.id, "ntf_probe")
    XCTAssertEqual(message.path, "/t/T-0193a1b2-0000-7000-8000-00000000a001")
    XCTAssertEqual(message.tag, "oryn-00000000a001")
    XCTAssertEqual(message.avatar?.kind, "thread")
    XCTAssertEqual(message.avatar?.color, "blue")

    let content = PushContent.build(message)
    XCTAssertEqual(content.title, "Push probe")
    XCTAssertEqual(content.body, "Decrypted by the notification service extension")
    XCTAssertEqual(content.threadIdentifier, "oryn-00000000a001")
    XCTAssertEqual(content.userInfo["id"] as? String, "ntf_probe")
    XCTAssertEqual(content.userInfo["path"] as? String, "/t/T-0193a1b2-0000-7000-8000-00000000a001")
  }

  func testTamperedCipherFails() throws {
    var body = try notifyCipher()
    body[body.count - 1] ^= 0x01
    XCTAssertThrowsError(try WebPushDecrypt.open(body, privateKey: receiver(), auth: data(Self.auth)))
    var wrongAuth = try data(Self.auth)
    wrongAuth[0] ^= 0x01
    XCTAssertThrowsError(try WebPushDecrypt.open(notifyCipher(), privateKey: receiver(), auth: wrongAuth))
    XCTAssertThrowsError(try WebPushDecrypt.open(Data(count: 20), privateKey: receiver(), auth: data(Self.auth)))
  }

  /// 撤回消息与非站内路径都不展示（NSE 交付占位）。
  func testParseRejectsNonNotifications() {
    XCTAssertNil(PushContent.parse(Data(#"{"v":1,"kind":"withdraw","id":"ntf_1","tag":"oryn-x"}"#.utf8)))
    XCTAssertNil(PushContent.parse(Data(#"{"title":"t","url":"https://evil.example/"}"#.utf8)))
    XCTAssertNil(PushContent.parse(Data("not json".utf8)))
  }

  func testKeyStoreRoundTrip() throws {
    PushKeyStore.clear()
    defer { PushKeyStore.clear() }
    XCTAssertNil(PushKeyStore.load())
    let key = try receiver()
    let auth = try data(Self.auth)
    XCTAssertTrue(PushKeyStore.save(privateKey: key, auth: auth), "钥匙串写入失败")
    let loaded = try XCTUnwrap(PushKeyStore.load(), "钥匙串读不回刚写入的密钥")
    XCTAssertEqual(loaded.privateKey.rawRepresentation, key.rawRepresentation)
    XCTAssertEqual(loaded.auth, auth)
    PushKeyStore.clear()
    XCTAssertNil(PushKeyStore.load())
  }
}
