package kpatch

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"vibeldr/internal/lzma"
)

// BzImage is a parsed bzImage container. It holds every offset and size needed
// to rebuild it, along with the original bytes.
//
// BzImage 는 bzImage 컨테이너를 파싱한 결과다. 재조립을 위해 필요한 위치와
// 크기 정보를 다 담고 있고, 원본 바이트도 갖고 있다.
type BzImage struct {
	// Raw is the original bytes as they are; the rebuild happens on top of it.
	// Raw 는 원본 바이트 그대로. 재조립도 이 위에서 이루어진다.
	Raw []byte

	// SetupSects is the setup code's size in 512 byte sectors; 0 means 4.
	// SetupSects 는 setup 코드의 512 바이트 섹터 수. 0 이면 4 로 취급한다.
	SetupSects byte

	// PayloadOff and PayloadLen are the file offset and length of the
	// compressed payload (the "piggy") within the whole bzImage file.
	//
	// PayloadOff, PayloadLen 은 bzImage 전체(=파일) 안에서 압축된
	// 페이로드(=piggy) 가 차지하는 파일 오프셋과 길이.
	PayloadOff int
	PayloadLen int

	// CompOff and CompLen are the file offset and length of the LZMA stream
	// itself. Some kernels put a few bytes of head in front of the payload, so
	// these are tracked separately.
	//
	// CompOff, CompLen 은 LZMA 스트림 자체의 파일 오프셋과 길이.
	// 커널마다 페이로드 앞에 몇 바이트 head 가 붙는 경우가 있어서 별도로 잡는다.
	CompOff int
	CompLen int
}

// ParseBzImage parses a bzImage and locates its compressed stream.
// ParseBzImage 는 bzImage 를 파싱하고 압축 스트림 위치를 잡는다.
func ParseBzImage(data []byte) (*BzImage, error) {
	if len(data) < 0x300 {
		return nil, fmt.Errorf("bzImage too short (%d bytes)", len(data))
	}
	// bzImage header: byte 0x1F1 is setup_sects, and boot_flag (0xAA55) is at
	// 0x1FE.
	//
	// bzImage 헤더: byte 0x1F1 = setup_sects, boot_flag(0xAA55) 는 0x1FE 에 있다.
	if data[0x1fe] != 0x55 || data[0x1ff] != 0xaa {
		return nil, fmt.Errorf("not a bzImage (missing 0x55 0xAA at 0x1fe)")
	}
	setupSects := data[0x1f1]
	if setupSects == 0 {
		setupSects = 4
	}
	payloadOff := (int(setupSects) + 1) * 512

	// header.payload_offset is at 0x248 and payload_length at 0x24c.
	// header.payload_offset 은 0x248, payload_length 는 0x24c.
	payoff := binary.LittleEndian.Uint32(data[0x248:0x24c])
	paylen := binary.LittleEndian.Uint32(data[0x24c:0x250])
	compOff := payloadOff + int(payoff)
	compLen := int(paylen)

	if compOff+compLen > len(data) {
		return nil, fmt.Errorf("payload runs past end (comp end 0x%x, file 0x%x)", compOff+compLen, len(data))
	}
	// The LZMA "alone" signature is 0x5D in the first byte. Any other
	// compression is outside what this handles, so say so clearly and stop.
	//
	// LZMA "alone" 시그니처는 첫 바이트 0x5D 다. 다른 압축이면 우리 소관이
	// 아니라 명확하게 알려주고 종료한다.
	if data[compOff] != 0x5d {
		return nil, fmt.Errorf("payload at 0x%x is not LZMA (first byte 0x%02x); kernel was built with a different decompressor", compOff, data[compOff])
	}

	return &BzImage{
		Raw:        data,
		SetupSects: setupSects,
		PayloadOff: payloadOff,
		PayloadLen: int(paylen),
		CompOff:    compOff,
		CompLen:    compLen,
	}, nil
}

// ExtractVMLinux decompresses the payload and returns the raw vmlinux ELF
// bytes, using the LZMA decoder built into this tree.
//
// ExtractVMLinux 는 압축 페이로드를 풀어서 raw vmlinux (ELF) 바이트를
// 돌려준다. 내장 LZMA 디코더를 그대로 쓴다.
func (b *BzImage) ExtractVMLinux() ([]byte, error) {
	comp := b.Raw[b.CompOff : b.CompOff+b.CompLen]
	// lzma.Decode takes an alone stream with its header. The kernel payload is
	// exactly that format.
	//
	// lzma.Decode 는 헤더 붙은 alone 스트림을 받는다. 커널 페이로드는 정확히
	// 그 포맷이다.
	return lzma.Decode(comp)
}

// Rebuild recompresses a patched vmlinux and puts it back into the same slot,
// producing a new bzImage.
//
// The result must fit inside the original slot (CompLen); growing the file
// breaks the boot, as the comment in the body explains. compress produces
// different LZMA parameters per variant, and the first result that fits is
// used.
//
// Rebuild 는 패치한 vmlinux 를 다시 압축해 원래 칸에 도로 넣은 bzImage 를
// 만든다.
//
// 반드시 원래 칸(CompLen) 안에 들어가야 한다 (파일을 키우면 부팅이 깨진다.
// Rebuild 본문 주석 참고). compress 가 variant 별로 다른 LZMA 파라미터를 내고,
// 그 중 칸에 들어가는 첫 결과를 쓴다.
func (b *BzImage) Rebuild(vmlinux []byte, compress func(vmlinux []byte, variant int) ([]byte, error)) ([]byte, error) {
	// The compressed result has to fit inside the original slot (CompLen). In a
	// bzImage the decoder code follows immediately after the payload, and the
	// head code in front jumps to that decoder relatively. Growing the payload
	// pushes everything after it and throws that jump off, so the kernel dies
	// just before decompression. Rather than grow the file, a compressed form
	// that fits is found.
	//
	// compress produces different LZMA parameters per variant number. Variant 0
	// is the same parameters as the original (pb=2) and later ones are
	// parameters that come out smaller (pb=1 and so on). The kernel's unlzma
	// reads lc/lp/pb from the stream header, so different parameters still
	// decompress. The first result that fits is used.
	//
	// 압축 결과가 반드시 원래 칸(CompLen) 안에 들어가야 한다. bzImage 는
	// 페이로드 바로 뒤에 디코더 코드가 이어지고, 앞쪽 head 코드가 그 디코더로
	// 상대점프한다. 페이로드를 키워 뒤를 밀면 그 점프가 어긋나 커널이 압축
	// 해제 직전에 죽는다. 그래서 파일을 키우지 않고, 칸에 들어가는 압축본을
	// 찾는다.
	//
	// compress 는 variant 번호로 서로 다른 LZMA 파라미터를 낸다. 0 번은 원본과
	// 같은 파라미터(pb=2), 뒤 번호는 더 작게 나오는 파라미터(pb=1 등)다. 커널
	// unlzma 는 스트림 헤더에서 lc/lp/pb 를 읽으므로 파라미터가 달라도 그대로
	// 해제한다. 앞에서부터 칸에 들어가는 첫 결과를 쓴다.
	var lastErr error
	for variant := 0; ; variant++ {
		newComp, err := compress(vmlinux, variant)
		if err != nil {
			if variant == 0 {
				return nil, fmt.Errorf("recompress: %w", err)
			}
			// compress returns an error when there are no variants left.
			// 더 시도할 variant 가 없으면 compress 가 에러를 준다.
			break
		}
		if len(newComp) < 13 {
			lastErr = fmt.Errorf("recompress: 결과가 LZMA 헤더보다 짧음")
			continue
		}
		// dict_size (bytes 1..5) has to match the original, because the kernel
		// allocates its buffer to that size. props (byte 0) and uncomp_size
		// (bytes 5..13) are read from the header, so they may differ.
		//
		// dict_size(1..5바이트)는 커널이 이 크기로 버퍼를 잡으므로 원본과
		// 같아야 안전하다. props(0번째)와 uncomp_size(5..13)는 헤더에서 읽어
		// 처리하므로 달라도 된다.
		if !bytes.Equal(newComp[1:5], b.Raw[b.CompOff+1:b.CompOff+5]) {
			lastErr = fmt.Errorf("recompress: dict_size 가 원본과 다름 (%x vs %x)",
				newComp[1:5], b.Raw[b.CompOff+1:b.CompOff+5])
			continue
		}
		if len(newComp) > b.CompLen {
			lastErr = fmt.Errorf("recompress: 칸을 넘침 (%d > %d)", len(newComp), b.CompLen)
			continue
		}
		// Write over the slot and zero whatever is left. The kernel's LZMA
		// decoder stops at the end-of-stream marker, so the trailing zeros are
		// ignored.
		//
		// 자리에 그대로 덮어쓰고 남는 곳은 0 으로 채운다. 커널 LZMA 디코더는
		// end-of-stream 마커에서 멈추므로 뒤의 0 은 무시된다.
		out := make([]byte, len(b.Raw))
		copy(out, b.Raw)
		copy(out[b.CompOff:], newComp)
		for i := b.CompOff + len(newComp); i < b.CompOff+b.CompLen; i++ {
			out[i] = 0
		}
		return out, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("맞는 압축 파라미터를 못 찾음")
	}
	return nil, lastErr
}
