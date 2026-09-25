package image

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
)

// UEFI - the second boot path, for modern hardware with CSM turned off.
//
// grub.go covers the BIOS side: boot.img in the first sector calls core.img in
// the gap, and the whole of GRUB is in there. UEFI is an entirely different
// path. The firmware looks in the partition table for an EFI System Partition
// (or whatever FAT is in that place) and runs \EFI\BOOT\BOOTX64.EFI directly.
// So all that happens here is putting that file on the first FAT partition.
//
// The partition type is not changed to 0xEF and the disk is not rewritten as
// GPT, because the UEFI specification's "Removable Media Boot Behavior"
// requires booting from this path on any FAT partition. Leaving it alone means
// the same partition also keeps the /boot/grub the BIOS-side GRUB reads, so one
// image supports both ways of booting.
//
// The stub grub.cfg does the minimum: find root by label and hand over to the
// real menu in /boot/grub/grub.cfg with configfile. From there the flow is the
// same as what core.img does on the BIOS path.
//
// UEFI - CSM 이 꺼진 요즘 하드웨어를 위한 두 번째 부트 경로.
//
// BIOS 쪽은 grub.go 가 담당한다. 첫 섹터의 boot.img 가 갭에 있는 core.img 를
// 부르고, 거기 GRUB 전체가 들어있다. UEFI 는 완전히 다른 경로다. 펌웨어가
// 파티션 테이블에서 EFI System Partition (또는 그 자리에 있는 FAT) 을
// 찾아 \EFI\BOOT\BOOTX64.EFI 를 그대로 실행한다. 그래서 여기서 하는 일은
// 첫 번째 FAT 파티션에 그 파일을 얹어두는 것뿐이다.
//
// 파티션 타입을 0xEF 로 바꾸거나 GPT 로 재작성하지 않는 이유: UEFI 사양의
// "Removable Media Boot Behavior" 는 아무 FAT 파티션에서든 이 경로를
// 찾으면 부팅하도록 규정하고 있고, 그렇게 하면 같은 파티션이 BIOS 쪽
// GRUB 이 읽는 /boot/grub 도 계속 담을 수 있다. 즉 이미지 하나로 두
// 부팅 방식을 모두 지원한다.
//
// stub grub.cfg 가 하는 일은 최소다. 라벨로 root 를 잡고, 진짜 메뉴가
// 들어있는 /boot/grub/grub.cfg 를 configfile 로 넘긴다. 그 뒤부터는 BIOS
// 경로에서 core.img 가 하던 것과 동일한 흐름이다.

//go:generate bash grub/fetch-efi.sh

//go:embed grub/BOOTX64.EFI
var grubEFIBinary []byte

// efiStubGrubCfg is the minimal configuration placed in the ESP: three lines
// that search for root by label, set prefix, and hand over to the real menu.
//
// efiStubGrubCfg - ESP 안에 얹는 최소 설정. 세 줄이다. root 라벨 검색,
// prefix 지정, 진짜 메뉴 파일로 넘기기.
const efiStubGrubCfg = "search --no-floppy --set=root --label VIBELDR1\n" +
	"set prefix=($root)/boot/grub\n" +
	"configfile /boot/grub/grub.cfg\n"

// missingEFIBinaryMsg is the error when only the placeholder is there and the
// real binary is not. The text names the instructions file, so grepping for it
// from anywhere leads back there.
//
// missingEFIBinaryMsg - 자리표시자만 있고 진짜 바이너리가 없을 때의 에러.
// 문구가 안내문 파일 이름을 그대로 담고 있어서, 어디서 grep 해도 그리로
// 이어진다.
const missingEFIBinaryMsg = "grub-efi 2.14 binary not embedded at internal/image/grub/BOOTX64.EFI; run: go generate ./internal/image (see grub/README-EFI.md)"

// validateEFIBinary checks that the embedded bytes are at least in PE32+ EFI
// image form. It catches a zero-byte placeholder and a 32 bit (PE32) binary put
// there by mistake. Failing here beats a large file with a wrong header that
// turns into a failed boot.
//
// validateEFIBinary - embed 된 바이트가 최소한 PE32+ EFI 이미지 형식인지
// 확인한다. 0 바이트 자리표시자와, 실수로 32 비트 (PE32) 바이너리를 넣은
// 경우를 걸러낸다. 파일 크기가 커도 헤더가 이상하면 부팅 실패로 이어지니
// 여기서 빨리 실패시킨다.
func validateEFIBinary(b []byte) error {
	if len(b) == 0 {
		return errors.New(missingEFIBinaryMsg)
	}
	// The MZ (DOS stub) header.
	// MZ (DOS stub) 헤더.
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return errors.New(missingEFIBinaryMsg)
	}
	// e_lfanew points at the PE signature.
	// e_lfanew 가 PE 서명 위치를 가리킨다.
	peOff := int(binary.LittleEndian.Uint32(b[0x3C:0x40]))
	// There must be room for the PE signature (4), the COFF header (20) and
	// the optional header magic (2).
	//
	// PE 서명 (4) + COFF 헤더 (20) + Optional header magic (2) 까지 읽을 수
	// 있어야 한다.
	if peOff < 0 || peOff+26 > len(b) {
		return errors.New(missingEFIBinaryMsg)
	}
	if string(b[peOff:peOff+4]) != "PE\x00\x00" {
		return errors.New(missingEFIBinaryMsg)
	}
	// The optional header magic: 0x20B means PE32+, that is 64 bit, which is
	// what UEFI x64 needs. 0x10B (PE32) is 32 bit and x86_64 firmware will not
	// load it.
	//
	// Optional header 의 magic: 0x20B 이면 PE32+, 즉 64 비트다. UEFI x64 에
	// 필요한 값이다. 0x10B (PE32) 은 32 비트라서 x86_64 펌웨어가 안 로드한다.
	magic := binary.LittleEndian.Uint16(b[peOff+24 : peOff+26])
	if magic != 0x20B {
		return errors.New(missingEFIBinaryMsg)
	}
	return nil
}

// writeEFIToFAT writes the three files EFI boot needs into an already formatted
// FAT32 partition. The caller passes partition 1 in, and closing the filesystem
// remains the caller's job.
//
// The three files:
//
//   - EFI/BOOT/BOOTX64.EFI  : the removable-media path, where most firmware
//     looks.
//   - EFI/BOOT/grubx64.efi  : the same bytes under a lower-case name, for
//     firmware that insists on it.
//   - EFI/BOOT/grub.cfg     : the three-line stub GRUB reads from next to
//     itself.
//
// SynoBootLoader.* is written by writeSynoBootLoaderStub instead, because it is
// for the installer rather than for booting and has nothing to do with whether
// EFI is supported.
//
// writeEFIToFAT - 이미 포맷된 FAT32 파티션에 EFI 부팅에 필요한 세 파일을
// 쓴다. 호출자는 파티션 1 을 이 함수에 넘긴다. 파일시스템 닫기 (Close) 는
// 여전히 호출자 몫이다.
//
// 파일 세 개:
//   - EFI/BOOT/BOOTX64.EFI  : Removable Media 경로. 대부분의 펌웨어가 여기 옴.
//   - EFI/BOOT/grubx64.efi  : 소문자 이름을 요구하는 일부 펌웨어 대응. 같은 바이트.
//   - EFI/BOOT/grub.cfg     : GRUB 이 자기 옆에서 찾아 읽는 3 줄짜리 스텁.
//
// SynoBootLoader.* 는 여기가 아니라 writeSynoBootLoaderStub 에서 쓴다.
// 그건 부팅이 아니라 인스톨러용이라 EFI 지원 여부와 무관하기 때문이다.
func writeEFIToFAT(fs *FAT32) error {
	if err := validateEFIBinary(grubEFIBinary); err != nil {
		return err
	}
	if err := fs.AddFileBytes("EFI/BOOT/BOOTX64.EFI", grubEFIBinary); err != nil {
		return fmt.Errorf("write EFI/BOOT/BOOTX64.EFI: %w", err)
	}
	// The same bytes again under a lower-case name. FAT is case-insensitive,
	// so one would do, but some firmware has been reported to recognise only
	// one of the two names anyway, so both are written.
	//
	// 같은 바이트를 소문자 이름으로 한 번 더 쓴다. FAT 자체는 대소문자
	// 무시라 사실 하나만 있어도 되지만, 어떤 펌웨어는 그럼에도 두 이름 중
	// 한쪽만 인식하는 사례가 보고돼서 둘 다 둔다.
	if err := fs.AddFileBytes("EFI/BOOT/grubx64.efi", grubEFIBinary); err != nil {
		return fmt.Errorf("write EFI/BOOT/grubx64.efi: %w", err)
	}
	if err := fs.AddFileBytes("EFI/BOOT/grub.cfg", []byte(efiStubGrubCfg)); err != nil {
		return fmt.Errorf("write EFI/BOOT/grub.cfg: %w", err)
	}
	return nil
}

// writeSynoBootLoaderStub writes the placeholder the DSM installer wants onto
// P1.
//
// It is always written, whether or not EFI boot is supported. These files are
// never used to boot - only the installer opens them. Tying them to WithEFI
// would drop them entirely from a build with no EFI binary, and the install
// would then stop at 99%.
//
// writeSynoBootLoaderStub - DSM 인스톨러용 자리표시자를 P1 에 쓴다.
//
// UEFI 부팅 지원과 무관하게 항상 쓴다. 이 파일들은 부팅에 쓰이지 않고
// 인스톨러만 연다. WithEFI 에 묶어두면 EFI 바이너리가 없는 빌드에서
// 통째로 빠지고, 그러면 설치가 99% 에서 멈춘다.
func writeSynoBootLoaderStub(fs *FAT32) error {
	if err := fs.AddFileBytes("EFI/BOOT/SynoBootLoader.conf", []byte(synoBootLoaderConf)); err != nil {
		return fmt.Errorf("write EFI/BOOT/SynoBootLoader.conf: %w", err)
	}
	// The content may be empty. The updater only reads the .conf; the .efi just
	// has to exist alongside it.
	//
	// 내용은 비어도 된다. 업데이터는 .conf 만 읽고, .efi 는 짝으로 존재하기만
	// 하면 된다.
	if err := fs.AddFileBytes("EFI/BOOT/SynoBootLoader.efi", nil); err != nil {
		return fmt.Errorf("write EFI/BOOT/SynoBootLoader.efi: %w", err)
	}
	return nil
}

// synoBootLoaderConf is the configuration file the DSM updater looks for on the
// loader's boot partition.
//
// We never use it. Booting is GRUB 2 and the menu is boot/grub/grub.cfg. This
// exists purely for the DSM installer.
//
// The installer's last step is "update the factory partition": it copies the
// new zImage and rd.gz onto our boot partition and then opens this path to
// change a KPTI setting. Without it, that is where it ends - the installer's
// own log, with its updater.c line numbers:
//
//	updater.c:928  open /tmp/bootmnt/EFI/boot//SynoBootLoader.conf failed
//	updater.c:1206 Failed to update KPTI config
//	updater.c:5678 failed to update factory partition, retry 0
//
// After that there is no retry and no reboot. The web UI sits at 99%.
//
// The content is GRUB 0.97 syntax. It is never used to boot, so the paths in it
// do not have to match our layout - the updater only has to be able to open and
// parse it.
//
// synoBootLoaderConf - DSM 업데이터가 로더 부트 파티션에서 찾는 설정 파일.
//
// 우리는 이 파일을 쓰지 않는다. 부팅은 GRUB 2 이고 메뉴는
// boot/grub/grub.cfg 다. 이 파일은 오직 DSM 인스톨러를 위해 있다.
//
// 인스톨러의 마지막 단계는 "factory partition 갱신" 이다. 새 zImage 와
// rd.gz 를 우리 부트 파티션에 복사한 뒤, KPTI 설정을 바꾸려고 이 경로를
// 연다. 파일이 없으면 거기서 끝난다. updater.c 줄 번호가 찍힌 인스톨러
// 자체 로그는 위와 같다.
//
// 그 뒤로 재시도도 없고 재부팅도 없다. 웹 UI 는 99% 에서 멈춘 채로 남는다.
//
// 내용은 GRUB 0.97 문법이다. 실제로 부팅에 쓰이지 않으므로 여기 적힌 경로가
// 우리 배치와 달라도 된다. 업데이터가 열어서 파싱할 수 있으면 그만이다.
const synoBootLoaderConf = `serial --unit=1 --speed=115200
terminal serial
default 1
timeout 3
verbose
hiddenmenu
fallback 0

title SYNOLOGY_1
        root (hd0,0)
        kernel /zImage root=/dev/md0
        initrd /rd.gz

title SYNOLOGY_2
        root (hd0,1)
        cksum /grub_cksum.syno
        vender /vender show
        kernel /zImage root=/dev/md0
        initrd /rd.gz
`
