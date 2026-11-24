// RFC 8291（Web Push 消息加密）的 `aes128gcm`（RFC 8188）单记录解密：插件（debug 探针自检）与
// Notification Service Extension 共用。报文体 = salt(16) | rs(4，大端) | idlen(1) = 65 | 发送端临时公钥(65，未压缩点)
// | 密文 + 16 字节 GCM 标签；与 Android 的 FcmKeys.open、服务端的 Web Push 加密逐字节对应，测试向量是 RFC 8291 附录 A。
// 只用 CryptoKit，不引用 Tauri 与 UIApplication（扩展里不可用）。
import CryptoKit
import Foundation

@available(iOS 14.0, *)
enum WebPushDecrypt {
  enum Failure: Error {
    /// 报文体长度、记录大小或公钥长度不对。
    case malformed
    /// 发送端公钥不是 P-256 曲线上的点。
    case senderKey
    /// AEAD 校验失败（密钥、auth 或密文不对）。
    case decrypt
    /// 明文末尾没有单记录的分隔符 0x02。
    case padding
  }

  private static let saltLength = 16
  private static let headerLength = 21
  private static let keyLength = 65
  private static let tagLength = 16

  /// 用接收端私钥与 16 字节 auth 解开一份完整报文体，返回去掉填充的明文。
  static func open(_ body: Data, privateKey: P256.KeyAgreement.PrivateKey, auth: Data) throws -> Data {
    let bytes = [UInt8](body)
    guard bytes.count >= headerLength else { throw Failure.malformed }
    let salt = Data(bytes[0..<saltLength])
    let rs = bytes[saltLength..<(saltLength + 4)].reduce(0) { ($0 << 8) | Int($1) }
    let idlen = Int(bytes[headerLength - 1])
    let cipherStart = headerLength + idlen
    // 单记录：整段密文不超过记录大小，且至少有标签加一个分隔符
    guard idlen == keyLength, rs >= tagLength + 2, bytes.count >= cipherStart + tagLength + 1,
      bytes.count - cipherStart <= rs
    else { throw Failure.malformed }
    let senderRaw = Data(bytes[headerLength..<cipherStart])
    let cipher = Data(bytes[cipherStart...])

    let sender: P256.KeyAgreement.PublicKey
    do {
      sender = try P256.KeyAgreement.PublicKey(x963Representation: senderRaw)
    } catch {
      throw Failure.senderKey
    }
    let ecdh = try privateKey.sharedSecretFromKeyAgreement(with: sender).withUnsafeBytes { Data($0) }
    let receiverRaw = privateKey.publicKey.x963Representation

    // RFC 8291 §3.3–3.4：IKM = HKDF(auth, ecdh, "WebPush: info" 0x00 ‖ ua_public ‖ as_public, 32)
    var keyInfo = Data("WebPush: info".utf8)
    keyInfo.append(0)
    keyInfo.append(receiverRaw)
    keyInfo.append(senderRaw)
    let ikm = expand(prk: hmac(key: auth, ecdh), info: keyInfo, length: 32)
    // RFC 8188 §2.2–2.3：CEK 与 NONCE 由 salt 和 IKM 导出
    let prk = hmac(key: salt, ikm)
    let cek = expand(prk: prk, info: label("Content-Encoding: aes128gcm"), length: 16)
    let nonce = expand(prk: prk, info: label("Content-Encoding: nonce"), length: 12)

    let padded: Data
    do {
      let box = try AES.GCM.SealedBox(
        nonce: AES.GCM.Nonce(data: nonce),
        ciphertext: cipher.prefix(cipher.count - tagLength),
        tag: cipher.suffix(tagLength))
      padded = try AES.GCM.open(box, using: SymmetricKey(data: cek))
    } catch {
      throw Failure.decrypt
    }
    // 去掉末尾的 0x00 填充，最后一个非零字节必须是最后一条记录的分隔符 0x02
    guard let last = padded.lastIndex(where: { $0 != 0 }), padded[last] == 2 else { throw Failure.padding }
    return Data(padded[padded.startIndex..<last])
  }

  private static func hmac(key: Data, _ data: Data) -> Data {
    Data(HMAC<SHA256>.authenticationCode(for: data, using: SymmetricKey(data: key)))
  }

  /// HKDF-Expand，输出不超过一个 SHA-256 块：T(1) = HMAC(PRK, info ‖ 0x01) 截短。
  private static func expand(prk: Data, info: Data, length: Int) -> Data {
    var input = info
    input.append(1)
    return hmac(key: prk, input).prefix(length)
  }

  /// RFC 8188 的 info：标签 ‖ 0x00。
  private static func label(_ text: String) -> Data {
    var data = Data(text.utf8)
    data.append(0)
    return data
  }
}
