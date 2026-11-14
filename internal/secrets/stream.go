package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// 流式加密格式（M8 浏览器 Profile 归档等大文件）：
//
//	"OSS1"（4B）‖ salt（16B 随机）‖ 若干帧
//	帧 = uint32 大端密文长度 ‖ 密文
//
// 子密钥 = HKDF-SHA256(主密钥, salt, info="oryn stream v1") 取 32 字节，AES-256-GCM；
// 明文每帧 65536 字节（末帧 0–65536 字节）；nonce = 11 字节大端帧序号 ‖ 1 字节标志
// （0x00 中间帧、0x01 末帧）；AAD = name。一条流恰好以一个末帧结束，末帧之后还有数据即报错。
const (
	streamMagic     = "OSS1"
	streamSaltLen   = 16
	streamChunk     = 64 << 10
	streamInfo      = "oryn stream v1"
	streamFlagMid   = 0x00
	streamFlagFinal = 0x01
)

var (
	// ErrStreamCorrupt 表示密文流被截断、篡改、重排，或 name 不符。
	ErrStreamCorrupt = errors.New("secrets: 加密流损坏或不完整")
	errStreamClosed  = errors.New("secrets: 加密流已关闭")
)

// streamAEAD 由主密钥与 salt 派生本条流的 AES-256-GCM。
func (b *Box) streamAEAD(salt []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, b.key[:], salt, streamInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// streamNonce 写入第 seq 帧的 nonce（11 字节大端序号 ‖ 1 字节标志）。
func streamNonce(nonce []byte, seq uint64, flag byte) {
	clear(nonce[:3])
	binary.BigEndian.PutUint64(nonce[3:11], seq)
	nonce[11] = flag
}

// SealStream 返回把写入内容分块加密到 dst 的写入器；必须 Close（写出末块），否则密文不完整、解密必然失败。
// 文件头在本调用内写出；Close 不关闭 dst。
func (b *Box) SealStream(dst io.Writer, name string) (io.WriteCloser, error) {
	salt := make([]byte, streamSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := b.streamAEAD(salt)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 0, len(streamMagic)+streamSaltLen)
	head = append(append(head, streamMagic...), salt...)
	if _, err := dst.Write(head); err != nil {
		return nil, err
	}
	return &sealWriter{
		dst:   dst,
		aead:  aead,
		aad:   []byte(name),
		buf:   make([]byte, 0, streamChunk),
		out:   make([]byte, 4, 4+streamChunk+aead.Overhead()),
		nonce: make([]byte, aead.NonceSize()),
	}, nil
}

type sealWriter struct {
	dst    io.Writer
	aead   cipher.AEAD
	aad    []byte
	buf    []byte // 未加密的明文，至多 streamChunk 字节
	out    []byte // 帧缓冲：4 字节长度 + 密文
	nonce  []byte
	seq    uint64
	err    error // 粘滞错误
	closed bool
}

// Write 缓冲明文；缓冲满且还有后续数据时才写出中间帧（满缓冲可能就是末帧）。
func (w *sealWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errStreamClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	n := 0
	for len(p) > 0 {
		if len(w.buf) == streamChunk {
			if err := w.flush(streamFlagMid); err != nil {
				return n, err
			}
		}
		c := copy(w.buf[len(w.buf):streamChunk], p)
		w.buf = w.buf[:len(w.buf)+c]
		p = p[c:]
		n += c
	}
	return n, nil
}

// Close 写出末帧；重复调用无操作。
func (w *sealWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	return w.flush(streamFlagFinal)
}

func (w *sealWriter) flush(flag byte) error {
	if w.seq == 1<<64-1 {
		w.err = errors.New("secrets: 加密流帧数溢出")
		return w.err
	}
	streamNonce(w.nonce, w.seq, flag)
	frame := w.aead.Seal(w.out[:4], w.nonce, w.buf, w.aad)
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)-4))
	if _, err := w.dst.Write(frame); err != nil {
		w.err = err
		return err
	}
	w.seq++
	w.buf = w.buf[:0]
	return nil
}

// OpenStream 返回解密读取器；截断、篡改、块重排或 name 不符时 Read 返回错误。调用方必须读到 io.EOF 才能认为明文完整。
// 文件头在本调用内读取并校验。
func (b *Box) OpenStream(src io.Reader, name string) (io.Reader, error) {
	head := make([]byte, len(streamMagic)+streamSaltLen)
	if _, err := io.ReadFull(src, head); err != nil {
		return nil, fmt.Errorf("%w: 读取文件头: %w", ErrStreamCorrupt, err)
	}
	if string(head[:len(streamMagic)]) != streamMagic {
		return nil, fmt.Errorf("%w: 文件头不符", ErrStreamCorrupt)
	}
	aead, err := b.streamAEAD(head[len(streamMagic):])
	if err != nil {
		return nil, err
	}
	return &openReader{
		src:   src,
		aead:  aead,
		aad:   []byte(name),
		frame: make([]byte, streamChunk+aead.Overhead()),
		out:   make([]byte, streamChunk),
		nonce: make([]byte, aead.NonceSize()),
	}, nil
}

type openReader struct {
	src   io.Reader
	aead  cipher.AEAD
	aad   []byte
	frame []byte // 密文缓冲
	out   []byte // 明文缓冲（与密文分开：第一次按中间帧解失败后密文仍完整，可再按末帧解）
	plain []byte // 尚未交给调用方的明文（out 的切片）
	nonce []byte
	seq   uint64
	final bool  // 已解出末帧
	err   error // 粘滞错误（含 io.EOF）
}

func (r *openReader) Read(p []byte) (int, error) {
	for len(r.plain) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		r.err = r.next()
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

// next 读入并解密下一帧；末帧之后确认流已结束并返回 io.EOF。
func (r *openReader) next() error {
	if r.final {
		var one [1]byte
		switch n, err := io.ReadFull(r.src, one[:]); {
		case n > 0:
			return fmt.Errorf("%w: 末帧之后还有数据", ErrStreamCorrupt)
		case errors.Is(err, io.EOF):
			return io.EOF
		default:
			return err
		}
	}
	var hdr [4]byte
	if _, err := io.ReadFull(r.src, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: 缺少末帧", ErrStreamCorrupt)
		}
		return err
	}
	size := int(binary.BigEndian.Uint32(hdr[:]))
	if size < r.aead.Overhead() || size > len(r.frame) {
		return fmt.Errorf("%w: 帧长度 %d 非法", ErrStreamCorrupt, size)
	}
	ct := r.frame[:size]
	if _, err := io.ReadFull(r.src, ct); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: 帧被截断", ErrStreamCorrupt)
		}
		return err
	}
	// 先按中间帧解；不成再按末帧解。
	streamNonce(r.nonce, r.seq, streamFlagMid)
	plain, err := r.aead.Open(r.out[:0], r.nonce, ct, r.aad)
	if err == nil && len(plain) != streamChunk {
		return fmt.Errorf("%w: 中间帧长度不符", ErrStreamCorrupt)
	}
	if err != nil {
		streamNonce(r.nonce, r.seq, streamFlagFinal)
		if plain, err = r.aead.Open(r.out[:0], r.nonce, ct, r.aad); err != nil {
			return fmt.Errorf("%w: 第 %d 帧校验失败", ErrStreamCorrupt, r.seq)
		}
		r.final = true
	}
	r.seq++
	r.plain = plain
	return nil
}
