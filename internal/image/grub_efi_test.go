package image

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteEFIToFATPlaceholder checks that writeEFIToFAT fails with a clear
// message while only the placeholder is present. In a repository where the real
// binary is committed this check simply passes, so it is skipped there.
//
// TestWriteEFIToFATPlaceholder - 자리표시자만 있는 상태에서 writeEFIToFAT
// 이 명확한 안내 메시지로 실패하는지 확인한다. 실제 바이너리가 커밋되어
// 있는 저장소에서는 이 검사가 그냥 통과하므로 여기서 스킵한다.
func TestWriteEFIToFATPlaceholder(t *testing.T) {
	if err := validateEFIBinary(grubEFIBinary); err == nil {
		t.Skip("real grub-efi binary is present; nothing to check for placeholder path")
	} else if err.Error() != missingEFIBinaryMsg {
		t.Fatalf("validateEFIBinary returned unexpected error: %v", err)
	}
}

// TestEmbeddedEFIBinaryLooksReal is a shallow check that the embedded binary is
// a real GRUB EFI image rather than the placeholder. Skipped in the placeholder
// state. The minimum size is 100KiB - a monolithic build with its modules
// embedded is usually 1MB or more, and this is the floor that catches a
// truncated file being committed - and it checks MZ, PE, PE32+ and the EFI
// subsystem.
//
// TestEmbeddedEFIBinaryLooksReal - embed 된 바이너리가 자리표시자가 아닌
// 실제 GRUB EFI 이미지인지 얕게 검증한다. 자리표시자 상태에서는 스킵한다.
// 최소 크기 100KiB (모듈이 embed 된 monolithic 은 보통 1MB+ 인데,
// 잘려서 커밋된 사고를 잡기 위한 하한선), MZ / PE / PE32+ / EFI
// 서브시스템까지 확인한다.
func TestEmbeddedEFIBinaryLooksReal(t *testing.T) {
	if err := validateEFIBinary(grubEFIBinary); err != nil {
		t.Skipf("placeholder in place: %v", err)
	}
	const minSize = 100 * 1024
	if len(grubEFIBinary) < minSize {
		t.Fatalf("embedded EFI binary is only %d bytes; expected >= %d", len(grubEFIBinary), minSize)
	}
	// validateEFIBinary already checks MZ, PE and PE32+. What is added here is
	// whether the PE Optional Header's Subsystem field is EFI_APPLICATION (10), so
	// that a Windows executable put in by mistake (Subsystem 2 or 3) is caught too.
	//
	// validateEFIBinary 는 MZ / PE / PE32+ 를 이미 확인한다. 여기서는
	// PE Optional Header 의 Subsystem 필드가 EFI_APPLICATION(10) 인지
	// 추가로 확인한다. Windows 실행 파일 (Subsystem 2/3) 을 실수로 넣은
	// 상황도 잡히도록 하려는 것이다.
	peOff := int(binary.LittleEndian.Uint32(grubEFIBinary[0x3C:0x40]))
	// The Optional Header starts at peOff+24, and the Subsystem field is at offset
	// 68 within it.
	//
	// Optional Header 시작은 peOff+24 이고, Subsystem 필드는 그 안의 오프셋 68.
	subOff := peOff + 24 + 68
	if subOff+2 > len(grubEFIBinary) {
		t.Fatalf("EFI binary too short to hold Subsystem field")
	}
	sub := binary.LittleEndian.Uint16(grubEFIBinary[subOff : subOff+2])
	// 10 = EFI_APPLICATION, 11 = EFI_BOOT_SERVICE_DRIVER, 12 = EFI_RUNTIME_DRIVER,
	// 13 = EFI_ROM. GRUB is 10, but any of the four counts as an EFI image.
	//
	// 10 = EFI_APPLICATION, 11 = EFI_BOOT_SERVICE_DRIVER, 12 = EFI_RUNTIME_DRIVER,
	// 13 = EFI_ROM. GRUB 은 10 이지만, 넷 중 하나면 EFI 이미지라고 보고 통과시킨다.
	if sub < 10 || sub > 13 {
		t.Fatalf("PE Subsystem = %d; expected an EFI subsystem (10..13)", sub)
	}
}

// TestBuildWithEFI is the integration test that only passes with a real GRUB
// EFI binary present; it is skipped in the placeholder state. It builds with
// Builder.WithEFI = true and verifies that EFI/BOOT/BOOTX64.EFI and
// EFI/BOOT/grub.cfg really exist on the first partition and are not empty.
//
// TestBuildWithEFI - 실제 GRUB EFI 바이너리가 있을 때만 통과하는 통합
// 테스트. 자리표시자 상태에서는 스킵한다. Builder.WithEFI = true 로 빌드해서
// 첫 파티션에 EFI/BOOT/BOOTX64.EFI 와 EFI/BOOT/grub.cfg 가 실제로
// 존재하고 비어 있지 않은지 검증한다.
func TestBuildWithEFI(t *testing.T) {
	if err := validateEFIBinary(grubEFIBinary); err != nil {
		t.Skipf("skipping EFI build test: %v", err)
	}

	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "loader.img")

	b := DefaultBuilder()
	b.WithEFI = true
	if _, err := b.Build(outPath, &Content{}); err != nil {
		t.Fatalf("Build with EFI: %v", err)
	}

	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer f.Close()

	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatalf("read MBR: %v", err)
	}
	fr, err := OpenFAT32(f, int64(parts[0].StartLBA)*SectorSize)
	if err != nil {
		t.Fatalf("open p1 FAT32: %v", err)
	}

	// Walk to check the two files really are in P1. FAT ignores case, so the names
	// are normalised to lower case to make the lookup easy.
	//
	// 필요한 두 파일이 실제로 P1 안에 있는지 walk 로 확인한다. 대소문자를
	// 무시하는 FAT 에서 조회 편의를 위해 소문자로 정규화한다.
	found := map[string]uint32{}
	err = fr.WalkAll(func(p string, e Entry) error {
		found[normalisePath(p)] = e.Size
		return nil
	})
	if err != nil {
		t.Fatalf("walk p1: %v", err)
	}
	for _, need := range []string{"efi/boot/bootx64.efi", "efi/boot/grub.cfg"} {
		size, ok := found[need]
		if !ok {
			t.Errorf("missing %s in p1 (have: %v)", need, keys(found))
			continue
		}
		if size == 0 {
			t.Errorf("%s exists but is empty", need)
		}
	}
}

// TestBuildWithoutEFIStillPlacesNothing: without WithEFI on, no EFI/ directory
// should be created on the partition. It is a sub-condition of the contract
// that an existing BIOS image stays byte for byte the same.
//
// TestBuildWithoutEFIStillPlacesNothing - WithEFI 를 켜지 않으면 EFI/
// 디렉터리가 파티션에 안 만들어져야 한다. 기존 BIOS 이미지가 바이트
// 단위로 그대로 유지된다는 계약의 하위 조건이다.
func TestBuildWithoutEFIStillPlacesNothing(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "loader.img")

	b := DefaultBuilder()
	// b.WithEFI stays at its default of false.
	// b.WithEFI 는 기본값 false 그대로 둔다.
	if _, err := b.Build(outPath, &Content{}); err != nil {
		t.Fatalf("Build without EFI: %v", err)
	}

	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer f.Close()

	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatalf("read MBR: %v", err)
	}
	fr, err := OpenFAT32(f, int64(parts[0].StartLBA)*SectorSize)
	if err != nil {
		t.Fatalf("open p1 FAT32: %v", err)
	}
	err = fr.WalkAll(func(p string, e Entry) error {
		if normalisePath(p) == "efi/boot/bootx64.efi" {
			t.Errorf("EFI/BOOT/BOOTX64.EFI should not appear when WithEFI is false")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk p1: %v", err)
	}
}

// normalisePath makes path comparison easy. FAT ignores case, so everything
// goes to lower case.
//
// normalisePath - 경로 비교 편의용. FAT 은 대소문자를 무시하므로 소문자로.
func normalisePath(p string) string {
	out := make([]byte, len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func keys(m map[string]uint32) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
