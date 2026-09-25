// Package image assembles a bootable loader disk image entirely from
// userspace.
//
// Both the partition table and the filesystems are written byte by byte, so an
// unprivileged user can build an image on any OS, the build is reproducible,
// and it can be tested without a VM.
//
// Package image - 부팅 가능한 로더 디스크 이미지를 유저스페이스에서만
// 조립한다.
//
// 파티션 테이블도 파일시스템도 전부 바이트 단위로 쓴다. 어떤 OS 에서든
// 비특권 유저가 이미지를 만들 수 있고, 빌드가 재현 가능하며, VM 없이
// 테스트도 된다.
package image

import (
	"encoding/binary"
	"fmt"
	"io"
)

// SectorSize is the only sector size this builder emits. Every consumer of a
// loader image (GRUB, the DSM kernel, the virtualization layer) handles 512
// byte sectors, and supporting 4Kn would double the geometry code for no
// benefit here.
//
// SectorSize - 이 빌더가 내는 유일한 섹터 크기. 로더 이미지를 읽는 쪽
// (GRUB, DSM 커널, 가상화 계층) 이 전부 512 바이트 섹터를 다루고, 4Kn 을
// 지원하면 기하 계산 코드가 두 배가 되면서 얻는 건 없다.
const SectorSize = 512

// PartitionType is the MBR partition type byte.
// PartitionType - MBR 파티션 타입 바이트.
type PartitionType byte

const (
	// TypeFAT32LBA is what every partition in a vibeldr image uses.
	// TypeFAT32LBA - vibeldr 이미지의 모든 파티션이 쓰는 타입.
	TypeFAT32LBA PartitionType = 0x0c
	// TypeFAT16LBA is recognised when reading a donor image.
	// TypeFAT16LBA - donor 이미지를 읽을 때 인식하는 타입.
	TypeFAT16LBA PartitionType = 0x0e
	// TypeLinux is used for the raw driver-pack partition (no filesystem).
	// TypeLinux - 파일시스템 없는 raw 드라이버 팩 파티션에 쓴다.
	TypeLinux PartitionType = 0x83
)

func (t PartitionType) String() string {
	switch t {
	case TypeFAT32LBA:
		return "FAT32 (LBA)"
	case TypeFAT16LBA:
		return "FAT16 (LBA)"
	case TypeLinux:
		return "Linux"
	case 0:
		return "empty"
	default:
		return fmt.Sprintf("0x%02x", byte(t))
	}
}

// Partition is one MBR primary partition.
// Partition - MBR 주 파티션 하나.
type Partition struct {
	Bootable bool
	Type     PartitionType
	StartLBA uint32
	Sectors  uint32
}

// End returns the LBA one past the last sector of the partition.
// End - 파티션 마지막 섹터의 다음 LBA.
func (p Partition) End() uint32 { return p.StartLBA + p.Sectors }

// Bytes returns the partition size in bytes.
// Bytes - 파티션 크기 (바이트).
func (p Partition) Bytes() int64 { return int64(p.Sectors) * SectorSize }

const (
	// mbrBootCodeSize is 440, not 446: bytes 440-443 hold the disk signature
	// and 444-445 are reserved. Treating the boot code as 446 bytes means the
	// signature write silently corrupts the last four bytes of a donor's
	// bootloader.
	//
	// mbrBootCodeSize 는 446 이 아니라 440 이다. 440-443 바이트는 디스크
	// 서명이고 444-445 는 예약이다. 부트 코드를 446 바이트로 다루면 서명을
	// 쓸 때 donor 부트로더의 마지막 네 바이트가 조용히 망가진다.
	mbrBootCodeSize         = 440
	mbrDiskSigOffset        = 440
	mbrPartitionTableOffset = 446
	mbrPartitionEntrySize   = 16
	mbrSignatureOffset      = 510
	maxPrimaryPartitions    = 4
)

// chs encodes an LBA as a legacy cylinder/head/sector triple.
//
// Nothing in the boot path actually uses these values - GRUB and the
// kernel both read the LBA fields - but partitioning tools warn loudly when
// they are absent or inconsistent, so they are filled in properly.
//
// chs - LBA 를 옛 cylinder/head/sector 삼중값으로 인코딩한다.
//
// 부팅 경로에서 이 값을 실제로 쓰는 건 없다 (GRUB 도 커널도 LBA 필드를
// 읽는다). 다만 파티션 도구들이 값이 없거나 앞뒤가 안 맞으면 시끄럽게
// 경고하므로 제대로 채워 둔다.
func chs(lba uint32) [3]byte {
	const (
		headsPerCylinder   = 255
		sectorsPerTrack    = 63
		sectorsPerCylinder = headsPerCylinder * sectorsPerTrack
	)

	cylinder := lba / sectorsPerCylinder
	head := (lba / sectorsPerTrack) % headsPerCylinder
	sector := (lba % sectorsPerTrack) + 1

	// Beyond the CHS addressing limit everything is pinned to the maximum,
	// which is the convention every partitioning tool follows.
	//
	// CHS 주소 한계를 넘으면 전부 최대값으로 고정한다. 모든 파티션 도구가
	// 따르는 관례다.
	if cylinder > 1023 {
		cylinder, head, sector = 1023, headsPerCylinder-1, sectorsPerTrack
	}

	return [3]byte{
		byte(head),
		byte(sector) | byte((cylinder>>2)&0xc0),
		byte(cylinder & 0xff),
	}
}

// WriteMBR writes the master boot record.
//
// bootCode may be nil. When supplied it is at most the first 440 bytes of the
// MBR (mbrBootCodeSize) - GRUB's stage 1, either our own boot.img from
// writeGRUB or the one lifted from a donor image. The disk signature and the
// partition table are always written by us.
//
// WriteMBR - 마스터 부트 레코드를 쓴다.
//
// bootCode 는 nil 이어도 된다. 주어지면 MBR 의 앞 440 바이트 이하
// (mbrBootCodeSize) 이고, GRUB 1 단계다. writeGRUB 가 내놓는 우리 boot.img
// 이거나 donor 이미지에서 가져온 것이다. 디스크 서명과 파티션 표는 어느
// 쪽이든 항상 우리가 쓴다.
func WriteMBR(w io.WriterAt, parts []Partition, bootCode []byte, diskSig uint32) error {
	if len(parts) > maxPrimaryPartitions {
		return fmt.Errorf("MBR holds at most %d primary partitions, got %d", maxPrimaryPartitions, len(parts))
	}

	sector := make([]byte, SectorSize)

	if len(bootCode) > mbrBootCodeSize {
		return fmt.Errorf("boot code is %d bytes, the MBR has room for %d", len(bootCode), mbrBootCodeSize)
	}
	copy(sector, bootCode)

	// Disk signature. Windows and some boot managers key off it, and a zero
	// signature makes some disk-import tools treat the disk as unformatted.
	//
	// 디스크 서명. 윈도우와 일부 부트 매니저가 이 값을 기준으로 삼고, 서명이
	// 0 이면 일부 디스크 임포트 도구가 포맷 안 된 디스크로 취급한다.
	binary.LittleEndian.PutUint32(sector[mbrDiskSigOffset:mbrDiskSigOffset+4], diskSig)

	for i, p := range parts {
		off := mbrPartitionTableOffset + i*mbrPartitionEntrySize
		entry := sector[off : off+mbrPartitionEntrySize]

		if p.Bootable {
			entry[0] = 0x80
		}
		first := chs(p.StartLBA)
		copy(entry[1:4], first[:])
		entry[4] = byte(p.Type)
		last := chs(p.End() - 1)
		copy(entry[5:8], last[:])
		binary.LittleEndian.PutUint32(entry[8:12], p.StartLBA)
		binary.LittleEndian.PutUint32(entry[12:16], p.Sectors)
	}

	sector[mbrSignatureOffset] = 0x55
	sector[mbrSignatureOffset+1] = 0xaa

	if _, err := w.WriteAt(sector, 0); err != nil {
		return fmt.Errorf("write MBR: %w", err)
	}
	return nil
}

// ReadMBR parses the partition table of an existing image, which is how a donor
// image is inspected before its boot code is reused.
//
// ReadMBR - 기존 이미지의 파티션 표를 파싱한다. donor 이미지의 부트 코드를
// 재사용하기 전에 그 이미지를 들여다보는 방법이다.
func ReadMBR(r io.ReaderAt) ([]Partition, error) {
	sector := make([]byte, SectorSize)
	if _, err := r.ReadAt(sector, 0); err != nil {
		return nil, fmt.Errorf("read MBR: %w", err)
	}
	if sector[mbrSignatureOffset] != 0x55 || sector[mbrSignatureOffset+1] != 0xaa {
		return nil, fmt.Errorf("not an MBR: missing 0x55AA signature")
	}

	var parts []Partition
	for i := 0; i < maxPrimaryPartitions; i++ {
		off := mbrPartitionTableOffset + i*mbrPartitionEntrySize
		entry := sector[off : off+mbrPartitionEntrySize]

		start := binary.LittleEndian.Uint32(entry[8:12])
		count := binary.LittleEndian.Uint32(entry[12:16])
		if entry[4] == 0 || count == 0 {
			continue
		}
		parts = append(parts, Partition{
			Bootable: entry[0] == 0x80,
			Type:     PartitionType(entry[4]),
			StartLBA: start,
			Sectors:  count,
		})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("no partitions found in MBR")
	}
	return parts, nil
}

// ReadBootCode returns the MBR boot code: the first 440 bytes, stopping short
// of the disk signature so the result can be handed straight back to WriteMBR.
//
// ReadBootCode - MBR 부트 코드를 돌려준다. 앞 440 바이트까지만 읽어 디스크
// 서명 앞에서 멈추므로, 결과를 그대로 WriteMBR 에 넘길 수 있다.
func ReadBootCode(r io.ReaderAt) ([]byte, error) {
	buf := make([]byte, mbrBootCodeSize)
	if _, err := r.ReadAt(buf, 0); err != nil {
		return nil, fmt.Errorf("read boot code: %w", err)
	}
	return buf, nil
}
