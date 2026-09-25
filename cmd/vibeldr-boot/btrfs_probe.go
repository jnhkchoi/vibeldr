//go:build linux

// btrfs_probe.go is a minimal parser for the BTRFS superblock.
//
// Rescue mode has to start by detecting volumes, and that has to work even when
// `btrfs-progs` (btrfs.static) is not on the image.
//
// All this does is read the one primary superblock (offset 0x10000, 65536), check
// the magic `_BHRfS_M` and take the label, generation and fsid out of it. This
// parser never mounts read-write and never repairs anything - that is
// btrfs.static's job. What it returns is diagnostic only: there is a btrfs
// volume here, this is its label, this is roughly its generation.
//
// For the on-disk layout see `struct btrfs_super_block` in
// `linux/fs/btrfs/ctree.h`: csum(32) + fsid(16) + bytenr(8) + flags(8) +
// magic(8) + generation(8) + ... + dev_item(98) + label(256). Each offset is
// fixed in the btrfs* constants below.
//
// btrfs_probe.go - BTRFS 슈퍼블록의 최소 파서.
//
// `btrfs-progs` (btrfs.static) 이 이미지에 안 들어있는 상황에서도, 볼륨
// 감지 정도는 로더가 자체적으로 해줘야 rescue mode 의 첫 걸음이 성립한다.
//
// 여기서 하는 건 primary superblock (offset 0x10000, 65536 바이트) 한 개
// 읽고 magic `_BHRfS_M` 을 확인한 뒤 label / generation / fsid 만 뽑는
// 것이다. RW 마운트나 복구 로직은 이 파서로는 절대 안 한다 - 그건
// btrfs.static 의 몫이다. 이 파서는 "여기 btrfs 볼륨이 있고, 라벨은 이거고,
// generation 은 이 정도" 라는 진단 정보만 돌려준다.
//
// on-disk 레이아웃은 위 영문에 적은 `struct btrfs_super_block` 을 참고.
// 각 오프셋 상수는 아래 btrfs* 로 시작하는 const 로 고정돼 있다.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// btrfsSuperOffset is where the primary superblock sits. The secondary is at
// 64 MiB (0x4000000) and the tertiary at 256 GiB (0x4000000000); only the
// primary is looked at. Once even the primary is damaged a native probe has
// nothing left to do and the job belongs to `btrfs rescue super-recover`.
//
// btrfsSuperOffset - primary superblock 위치.
// secondary 는 64 MiB (0x4000000), tertiary 는 256 GiB (0x4000000000).
// 우리는 primary 만 본다 - primary 마저 깨지면 native probe 로는 도리 없고
// `btrfs rescue super-recover` 로 넘겨야 한다.
const (
	btrfsSuperOffset = 0x10000
	btrfsSuperSize   = 4096
	btrfsMagic       = "_BHRfS_M"

	// Field offsets within btrfs_super_block, from the start of the block.
	// btrfs_super_block 필드 오프셋 (super block 시작 기준).
	btrfsFsidOff       = 0x20  // 16 bytes / 16바이트
	btrfsMagicOff      = 0x40  // 8 bytes / 8바이트
	btrfsGenerationOff = 0x48  // 8 bytes LE / 8바이트 리틀엔디언
	btrfsLabelOff      = 0x12b // 256 bytes null-terminated / 256바이트, NUL 로 끝남
	btrfsLabelSize     = 256
)

// BTRFSSuper holds the fields of interest from a parsed superblock.
// BTRFSSuper - 파싱된 슈퍼블록의 관심 필드.
type BTRFSSuper struct {
	Device     string
	Label      string
	Generation uint64
	FSID       [16]byte
}

// probeBTRFSSuper reads and parses a device's primary superblock.
//
// What it returns:
//   - (nil, nil): the file was read but the magic does not match - not a btrfs
//     volume.
//   - (nil, err): the open or the read failed; the caller just moves on to the
//     next device.
//   - (super, nil): detected.
//
// probeBTRFSSuper - device 의 primary superblock 을 읽어 파싱한다.
// 반환 규칙은 위 영문 목록과 같다.
func probeBTRFSSuper(device string) (*BTRFSSuper, error) {
	f, err := os.Open(device)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, btrfsSuperSize)
	n, err := f.ReadAt(buf, btrfsSuperOffset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n < btrfsLabelOff+btrfsLabelSize {
		// The file does not even reach the end of the superblock - not btrfs.
		// 파일이 슈퍼블록 뒤끝까지 없음 - btrfs 아님.
		return nil, nil
	}
	return parseBTRFSSuper(device, buf), nil
}

// parseBTRFSSuper parses a buffer at least as big as the superblock, returning
// nil when the magic does not match. The device argument only labels the result;
// the parsing itself uses nothing but buf, which makes it easy to test.
//
// parseBTRFSSuper - buf 는 슈퍼블록 크기 이상이어야 한다. magic 이 안 맞으면
// nil 을 돌려준다. device 인자는 결과에 이름을 붙이는 용도이고, 파싱 자체는
// buf 만 쓴다 (테스트 편의).
func parseBTRFSSuper(device string, buf []byte) *BTRFSSuper {
	if len(buf) < btrfsLabelOff+btrfsLabelSize {
		return nil
	}
	if string(buf[btrfsMagicOff:btrfsMagicOff+8]) != btrfsMagic {
		return nil
	}
	s := &BTRFSSuper{Device: device}
	copy(s.FSID[:], buf[btrfsFsidOff:btrfsFsidOff+16])
	s.Generation = binary.LittleEndian.Uint64(buf[btrfsGenerationOff : btrfsGenerationOff+8])
	label := buf[btrfsLabelOff : btrfsLabelOff+btrfsLabelSize]
	if i := bytes.IndexByte(label, 0); i >= 0 {
		label = label[:i]
	}
	s.Label = string(label)
	return s
}
