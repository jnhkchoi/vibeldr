package hwscan

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The loader lives on a disk of its own, and that disk must not be offered to
// DSM as a bay. If it were, DSM would happily propose installing over the
// thing it just booted from.
//
// We do not hide anything: the device tree only lists the bays we put in it,
// so leaving the loader's port out of that list is enough. What we do need is
// to recognise the disk, and it recognises itself - the first partition
// carries a FAT volume label we chose when the image was built.
//
// Reading that label means reading two sectors. No mounting, no filesystem
// driver, no udev, which matters because this runs inside DSM's own ramdisk
// where none of those are available yet.
//
// 로더는 자기 디스크 위에 있고, 그 디스크를 DSM 에 베이로 내주면 안 된다.
// 내주면 DSM 이 방금 부팅한 그 물건 위에 설치하자고 제안하게 된다.
//
// 뭔가를 숨기지는 않는다. device tree 에는 우리가 넣은 베이만 들어 있으므로,
// 로더의 포트를 그 목록에서 빼는 것으로 충분하다. 필요한 것은 그 디스크를
// 알아보는 일뿐이고, 디스크는 스스로를 알린다. 첫 파티션이 이미지 빌드 때
// 우리가 정한 FAT 볼륨 라벨을 달고 있다.
//
// 그 라벨을 읽는 데는 섹터 두 개면 된다. 마운트도, 파일시스템 드라이버도,
// udev 도 필요 없다. 이게 중요한 이유는 이 코드가 DSM 자체 램디스크 안에서
// 도는데 거기엔 그 셋 다 아직 없기 때문이다.

const (
	sectorSize = 512

	mbrSignatureOffset = 510
	mbrPartitionTable  = 446
	mbrPartitionSize   = 16

	// FAT32 partition types, as written by the image builder.
	// 이미지 빌더가 쓰는 FAT32 파티션 타입.
	partTypeFAT32LBA = 0x0c
	partTypeFAT32CHS = 0x0b

	bpbLabelOffset  = 71 // BS_VolLab
	bpbLabelLength  = 11
	bpbFSTypeOffset = 82 // BS_FilSysType
)

// DefaultDev is where device nodes live on a booted system.
// DefaultDev - 부팅된 시스템에서 장치 노드가 있는 자리.
const DefaultDev = "/dev"

// FirstPartitionLabel reads the FAT volume label of a block device's first
// partition. It returns an empty string, and no error, for a device that
// simply is not laid out this way - an unpartitioned data disk, say.
//
// FirstPartitionLabel - 블록 장치 첫 파티션의 FAT 볼륨 라벨을 읽는다. 이런
// 배치가 아닌 장치 (파티션 없는 데이터 디스크 등) 는 오류가 아니라 빈
// 문자열을 돌려준다.
func FirstPartitionLabel(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	mbr := make([]byte, sectorSize)
	if _, err := f.ReadAt(mbr, 0); err != nil {
		return "", fmt.Errorf("hwscan: read MBR of %s: %w", path, err)
	}
	if mbr[mbrSignatureOffset] != 0x55 || mbr[mbrSignatureOffset+1] != 0xAA {
		return "", nil
	}

	entry := mbr[mbrPartitionTable : mbrPartitionTable+mbrPartitionSize]
	switch entry[4] {
	case partTypeFAT32LBA, partTypeFAT32CHS:
	default:
		return "", nil
	}
	start := int64(binary.LittleEndian.Uint32(entry[8:]))
	if start == 0 {
		return "", nil
	}

	bpb := make([]byte, sectorSize)
	if _, err := f.ReadAt(bpb, start*sectorSize); err != nil {
		return "", fmt.Errorf("hwscan: read boot sector of %s: %w", path, err)
	}
	if string(bpb[bpbFSTypeOffset:bpbFSTypeOffset+5]) != "FAT32" {
		return "", nil
	}
	return strings.TrimSpace(string(bpb[bpbLabelOffset : bpbLabelOffset+bpbLabelLength])), nil
}

// FindLoaderPort locates the port holding the loader's own disk, identified by
// the volume label of its first partition.
//
// It returns the controller and port index, or ok=false when the loader booted
// from something that is not a SATA disk at all, such as a USB stick. That is
// not a failure: there is simply nothing to leave out.
//
// FindLoaderPort - 로더 자신의 디스크가 붙은 포트를 찾는다. 식별 기준은 첫
// 파티션의 볼륨 라벨이다.
//
// 컨트롤러와 포트 번호를 돌려주고, 로더가 SATA 디스크가 아닌 것 (USB 스틱
// 등) 에서 부팅했으면 ok=false 다. 그건 실패가 아니라 뺄 것이 없다는 뜻이다.
func FindLoaderPort(cs []Controller, dev, label string) (ctrl string, port uint32, ok bool) {
	for _, c := range cs {
		for _, p := range c.Ports {
			if p.Block == "" {
				continue
			}
			got, err := FirstPartitionLabel(filepath.Join(dev, p.Block))
			if err != nil {
				continue
			}
			if strings.EqualFold(got, label) {
				return c.Address, p.Index, true
			}
		}
	}
	return "", 0, false
}

// ExcludePort returns the controllers with one port dropped.
//
// The surviving ports keep their original indices. An index is the port's real
// position on its controller, so closing the gap would make every later bay
// point at the wrong hardware. A controller left with no ports at all is
// dropped too, since it can no longer host a bay.
//
// ExcludePort - 포트 하나를 뺀 컨트롤러 목록을 돌려준다.
//
// 남은 포트는 원래 번호를 유지한다. 번호는 컨트롤러 위에서의 실제 위치라,
// 빈자리를 메우면 그 뒤의 베이가 전부 엉뚱한 하드웨어를 가리키게 된다.
// 포트가 하나도 안 남은 컨트롤러는 더 이상 베이를 담을 수 없으므로 함께
// 뺀다.
func ExcludePort(cs []Controller, ctrl string, port uint32) []Controller {
	out := make([]Controller, 0, len(cs))
	for _, c := range cs {
		if c.Address != ctrl {
			out = append(out, c)
			continue
		}
		kept := make([]Port, 0, len(c.Ports))
		for _, p := range c.Ports {
			if p.Index == port {
				continue
			}
			kept = append(kept, p)
		}
		c.Ports = kept
		if len(c.Ports) > 0 {
			out = append(out, c)
		}
	}
	return out
}
