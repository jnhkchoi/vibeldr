// Package lzma decodes the LZMA1 streams inside a Synology .pat, and encodes
// the one it has to write back.
//
// Both the zImage payload and rd.gz are in "LZMA alone" format: a 13 byte
// header followed by raw LZMA1 data. It predates .xz and the Go standard
// library does not have it.
//
// The decoder is here and the encoder is in encoder.go. A repacked rd.gz is
// written as plain cpio, because the kernel accepts an uncompressed initramfs,
// so the encoder is only needed when vmlinux is recompressed to rebuild a
// bzImage.
//
// One deliberate difference from a strict decoder: a stream whose input simply
// runs out after the declared number of bytes is accepted. rd.gz ends that way,
// with no end-of-stream marker, and the "alone" readers in circulation
// misjudge it as truncated.
//
// Package lzma - 시놀로지 .pat 안의 LZMA1 스트림 디코더. 다시 써야 하는
// 스트림은 인코딩도 한다.
//
// zImage 페이로드도 rd.gz 도 "LZMA alone" 포맷이다. 13 바이트 헤더 뒤에
// raw LZMA1 데이터가 온다. .xz 이전 포맷이고 Go 표준 라이브러리엔 없다.
//
// 디코더는 여기, 인코더는 encoder.go 다. 램디스크는 커널이 비압축 initramfs 도
// 받으므로 재패킹된 rd.gz 는 그냥 cpio 로 쓴다. 인코더가 필요한 건 vmlinux 를
// 다시 압축해서 bzImage 를 재조립할 때뿐이다.
//
// 엄격 디코더와 한 가지 다르다. 선언된 바이트를 다 뽑고 나서 입력이 그냥
// 떨어지는 스트림도 받아들인다. rd.gz 가 end-of-stream 마커 없이 그렇게
// 끝나서, 시중의 "alone" 리더들이 이걸 잘림으로 오판한다.
package lzma

import (
	"errors"
	"fmt"
)

// HeaderSize is the size of an "alone" header: the properties byte, the
// dictionary size and the uncompressed size.
//
// HeaderSize - "alone" 헤더 크기: properties 바이트 + dictionary size +
// uncompressed size.
const HeaderSize = 13

// SizeUnknown is the uncompressed-size field of a stream whose length was not
// known when it was written. Such a stream must carry an end marker.
//
// SizeUnknown - 쓸 때 길이를 몰랐던 스트림의 uncompressed-size 필드 값.
// 이런 스트림은 반드시 end marker 를 담고 있어야 한다.
const SizeUnknown = ^uint64(0)

// Header describes an LZMA1 stream.
// Header - LZMA1 스트림의 서술.
type Header struct {
	LC        int // literal context bits / 리터럴 컨텍스트 비트
	LP        int // literal position bits / 리터럴 위치 비트
	PB        int // position bits / 위치 비트
	DictSize  uint32
	Size      uint64
	SizeKnown bool
}

// ParseHeader reads the 13 byte "alone" header.
// ParseHeader - 13 바이트 "alone" 헤더 읽기.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("lzma: need %d header bytes, have %d", HeaderSize, len(b))
	}
	props := int(b[0])
	// props packs three values: (pb*5 + lp)*9 + lc.
	// props 는 세 값을 (pb*5 + lp)*9 + lc 로 묶은 것이다.
	if props >= 9*5*5 {
		return Header{}, fmt.Errorf("lzma: properties byte 0x%02x is out of range", b[0])
	}
	h := Header{
		LC:       props % 9,
		LP:       (props / 9) % 5,
		PB:       props / 45,
		DictSize: uint32(b[1]) | uint32(b[2])<<8 | uint32(b[3])<<16 | uint32(b[4])<<24,
	}
	var size uint64
	for i := 0; i < 8; i++ {
		size |= uint64(b[5+i]) << (8 * i)
	}
	h.Size = size
	h.SizeKnown = size != SizeUnknown
	return h, nil
}

// ErrCorrupt means the stream will not decode.
// ErrCorrupt - 디코딩되지 않는 스트림.
var ErrCorrupt = errors.New("lzma: corrupt stream")

// Decode decompresses an "alone" stream: header plus data.
// Decode - "alone" 스트림 (헤더 + 데이터) 복호.
func Decode(src []byte) ([]byte, error) {
	if len(src) < HeaderSize {
		return nil, fmt.Errorf("lzma: stream is only %d bytes", len(src))
	}
	h, err := ParseHeader(src)
	if err != nil {
		return nil, err
	}
	return DecodeRaw(h, src[HeaderSize:])
}

// DecodeRaw decompresses LZMA1 data whose parameters are already known. It is
// the entry point for a stream whose header lives elsewhere, such as the kernel
// payload inside a bzImage.
//
// DecodeRaw - 파라미터가 이미 알려진 LZMA1 데이터를 복호한다. bzImage 안의
// 커널 페이로드처럼 헤더가 다른 곳에 있는 스트림의 진입점이다.
func DecodeRaw(h Header, src []byte) ([]byte, error) {
	if h.LC+h.LP > 12 {
		return nil, fmt.Errorf("lzma: lc+lp=%d is more than the format allows", h.LC+h.LP)
	}
	d := &decoder{h: h}
	if err := d.run(src); err != nil {
		return nil, err
	}
	if h.SizeKnown && uint64(len(d.out)) != h.Size {
		return d.out, fmt.Errorf("lzma: got %d bytes, header declared %d: %w", len(d.out), h.Size, ErrCorrupt)
	}
	return d.out, nil
}
