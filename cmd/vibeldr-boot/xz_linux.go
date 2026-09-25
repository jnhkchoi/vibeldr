//go:build linux

// xz_linux.go recompresses the kernel as LZMA1 using the xz that came on the
// image.
//
// Why it is needed:
//
//	Turning off the DSM kernel's module signature enforcement means flipping one
//	byte inside the kernel image, and that byte sits inside an LZMA-compressed
//	blob. The only way to it is to decompress the whole thing, change it,
//	recompress and put it back in the same slot. The slot is a fixed size, so a
//	result even slightly larger than the original does not go back in. The
//	original was produced by liblzma, so the same liblzma (xz) is what produces
//	the same size.
//
// The options match the original stream's header: LZMA1 alone, preset 9 (lc=3
// lp=0 pb=2, 64 MiB dictionary). A different header and the kernel's decoder
// cannot read it.
//
// xz_linux.go - 이미지에 실려 온 xz 로 커널을 LZMA1 로 다시 압축한다.
//
// 왜 이게 필요한가:
//
//	DSM 커널의 모듈 서명 강제를 끄려면 커널 이미지 안의 한 바이트를 뒤집어야
//	한다. 그 바이트는 LZMA 로 압축된 덩어리 안에 있어서, 통째로 풀었다가
//	고치고 다시 압축해 원래 칸에 도로 넣는 수밖에 없다. 칸 크기가 고정이라
//	압축 결과가 원본보다 조금이라도 크면 못 넣는다. 원본을 만든 압축기가
//	liblzma 이므로 같은 liblzma (xz) 를 써야 같은 크기가 나온다.
//
// 옵션은 원본 스트림 헤더와 맞춘다: LZMA1 alone, preset 9 (lc=3 lp=0 pb=2,
// 사전 64 MiB). 헤더가 다르면 커널의 디코더가 읽지 못한다.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"vibeldr/internal/catalog"
)

// xzPath is where the xz binary sits on the image, and xzLibDir is where the
// libraries it needs were put.
//
// xzPath - 이미지에 실린 xz 실행 파일의 자리.
// xzLibDir - 그것이 필요로 하는 라이브러리들이 놓인 자리.
const (
	xzPath   = "/" + catalog.XZBinPath
	xzLibDir = "/usr/lib"
)

// compressKernelLZMA compresses vmlinux into an LZMA1 alone stream.
//
// Each variant produces different parameters. Rebuild takes the first result
// that fits the slot, so the earlier variants are the safe, proven ones.
//
//	variant 0: -9 (pb=2, lc=3, lp=0, the same props 0x5d as the original). Most
//	           models fit the slot here.
//	variant 1: pb=1 (props 0x30), which comes out smaller than -9 on this data.
//	           It is for a 4.4 kernel that overflows the slot at -9, such as
//	           DS3622xs+. The kernel's unlzma reads lc/lp/pb out of the header,
//	           so it decompresses as it stands.
//
// A variant past the end returns an error, which ends Rebuild's retry loop.
//
// compressKernelLZMA - vmlinux 를 LZMA1 alone 스트림으로 압축한다.
//
// variant 마다 다른 파라미터를 낸다. Rebuild 가 칸에 들어가는 첫 결과를 쓰므로,
// 앞 variant 일수록 "안전하고 검증된" 파라미터를 둔다. 두 variant 의 내용은
// 위 영문 목록과 같다.
//
// variant 가 범위를 넘으면 에러를 돌려 Rebuild 의 시도 루프를 끝낸다.
func compressKernelLZMA(vmlinux []byte, variant int) ([]byte, error) {
	if _, err := os.Stat(xzPath); err != nil {
		return nil, fmt.Errorf("xz 가 이미지에 없음 (%s): %w", xzPath, err)
	}
	var args []string
	switch variant {
	case 0:
		args = []string{"--format=lzma", "-9", "--stdout", "--quiet"}
	case 1:
		args = []string{"--format=lzma", "--lzma1=preset=9,pb=1", "--stdout", "--quiet"}
	default:
		return nil, fmt.Errorf("시도할 압축 파라미터 없음 (variant %d)", variant)
	}
	cmd := exec.Command(xzPath, args...)
	cmd.Stdin = bytes.NewReader(vmlinux)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+xzLibDir)

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("xz 실행 (%s): %w\n  stderr: %s",
			filepath.Base(xzPath), err, errBuf.String())
	}
	if out.Len() < 13 {
		return nil, errors.New("xz 결과가 LZMA 헤더보다 짧음")
	}
	return out.Bytes(), nil
}
