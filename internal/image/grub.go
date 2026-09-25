package image

import (
	_ "embed"
	"fmt"
	"io"
)

// GRUB, taken from the GNU release directly rather than torn out of somebody
// else's loader image.
//
// A BIOS machine reads and runs the first sector of the disk. That sector is
// boot.img, and its only job is to load the next thing - core.img - out of the
// gap between the partition table and the first partition. core.img holds the
// real GRUB, with the modules this loader needs already built into it, so GRUB
// can read a filesystem before it has looked for anything on the disk.
//
// The awkward part of a bootloader is that first hop: boot.img has 446 bytes to
// work in and no filesystem, so all it can know is core.img's sector number.
// That value is written into boot.img while the image is assembled.
//
// core.img carries its own configuration and runs before anything else on the
// disk:
//
//	search --no-floppy --set=root --label VIBELDR1
//	set prefix=($root)/boot/grub
//	configfile $prefix/grub.cfg
//
// It finds the volume by label rather than by disk number, so the same image
// boots identically from USB, SATA or NVMe - machines number them differently.
//
// The two files are GNU GRUB 2.12 as built by Debian. GRUB is not rebuilt here:
// building it needs a toolchain, and a loader that only builds for people who
// have that toolchain is a loader most people cannot build.
//
// GRUB - 남의 로더 이미지에서 뜯어온 게 아니라 GNU 릴리즈를 직접 쓴다.
//
// BIOS 머신은 디스크의 첫 섹터를 읽어 실행한다. 그 섹터가 boot.img 이고, 그
// 유일한 역할은 다음 것 (core.img) 을 파티션 테이블과 첫 파티션 사이
// 갭에서 로드하는 것이다. core.img 에 진짜 GRUB 이 들어있고, 이 로더가
// 필요한 모듈들이 이미 그 안에 빌드돼 있어서, 디스크에서 뭘 찾기 전에
// GRUB 이 이미 파일시스템을 읽을 수 있다.
//
// 부트로더의 껄끄러운 부분이 그 첫 홉이다. boot.img 는 446 바이트 안에서
// 움직여야 하고 파일시스템도 없으니, core.img 의 섹터 번호만 알고 있어야
// 한다. 그 값은 이미지 조립할 때 boot.img 에 써 넣는다.
//
// core.img 는 자기 설정을 담고 있고 디스크의 어떤 것보다 먼저 실행된다
// (위 세 줄).
//
// 디스크 번호가 아니라 라벨로 찾으므로, 같은 이미지가 USB, SATA, NVMe
// 어디에 붙어도 동일하게 부팅된다. 머신마다 번호를 다르게 매길 수 있으니까.
//
// 두 파일은 Debian 이 빌드한 GNU GRUB 2.12 다. 여기서 GRUB 을 새로 빌드하지
// 않고 커밋된 파일을 쓴다. GRUB 빌드에는 툴체인이 필요한데, 그런 툴체인이
// 있어야만 빌드되는 로더는 대부분의 사람이 못 빌드하는 로더이기 때문이다.

// GRUBVersion is the upstream version of the two embedded stages, for the build
// log and for anyone wondering what is booting their machine.
//
// GRUBVersion - embed 된 두 스테이지의 원본 버전. 빌드 로그와, 지금 자기
// 머신을 뭐가 부팅시키고 있는지 궁금한 사람을 위한 값이다.
const GRUBVersion = "2.12"

//go:embed grub/boot.img
var grubBootImg []byte

//go:embed grub/core.img
var grubCoreImg []byte

const (
	// mbrSize is the sector boot.img occupies.
	// mbrSize - boot.img 가 차지하는 섹터 크기.
	mbrSize = 512
	// coreStartLBA is where core.img goes: immediately after the MBR, in the
	// gap that exists because the first partition starts at LBA 2048.
	//
	// coreStartLBA - core.img 가 놓이는 자리. MBR 바로 뒤, 첫 파티션이
	// LBA 2048 에서 시작하기 때문에 생기는 갭이다.
	coreStartLBA = 1
	// bootImgCoreLBAOffset is where core.img's sector number is stored inside
	// boot.img. GRUB's own installer patches the same place.
	//
	// bootImgCoreLBAOffset - boot.img 안에서 core.img 의 섹터 번호가 저장되는
	// 위치. GRUB 자체 인스톨러도 여기를 패치한다.
	bootImgCoreLBAOffset = 0x5c
)

// writeGRUB places both GRUB stages into the image and returns the MBR boot
// code, which the caller writes back out along with the partition table.
//
// writeGRUB - GRUB 두 스테이지를 이미지에 얹고 MBR boot code 를 반환한다.
// 호출자가 이 코드를 파티션 테이블과 함께 다시 쓴다.
func writeGRUB(dst io.WriterAt) ([]byte, error) {
	if len(grubBootImg) != mbrSize {
		return nil, fmt.Errorf("grub boot.img is %d bytes, expected %d", len(grubBootImg), mbrSize)
	}

	// core.img has to fit between the MBR and the first partition. Otherwise
	// the partition overwrites the tail of the bootloader and the result hangs
	// with no message at all.
	//
	// core.img 는 MBR 과 첫 파티션 사이에 들어가야 한다. 안 그러면 파티션이
	// 부트로더 뒷부분을 덮어써서 결과가 아무 메시지 없이 hang 한다.
	available := int64(FirstPartitionLBA-coreStartLBA) * int64(SectorSize)
	if int64(len(grubCoreImg)) > available {
		return nil, fmt.Errorf("grub core.img is %d bytes, and only %d fit before the first partition",
			len(grubCoreImg), available)
	}
	if _, err := dst.WriteAt(grubCoreImg, coreStartLBA*SectorSize); err != nil {
		return nil, fmt.Errorf("write grub core: %w", err)
	}

	// Tell the first stage where the second one is. Without this, boot.img
	// reads the sector that was baked in when it was built - which is where
	// GRUB happened to be installed on the machine that built it.
	//
	// 첫 스테이지에게 두 번째 스테이지 위치를 알려준다. 이걸 안 하면
	// boot.img 가 빌드될 때 박힌 섹터를 읽는데, 그건 boot.img 를 만든
	// 머신에 GRUB 이 설치돼 있던 위치다.
	boot := make([]byte, mbrSize)
	copy(boot, grubBootImg)
	putUint64(boot[bootImgCoreLBAOffset:], coreStartLBA)

	code := make([]byte, mbrBootCodeSize)
	copy(code, boot)
	return code, nil
}

func putUint64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}
