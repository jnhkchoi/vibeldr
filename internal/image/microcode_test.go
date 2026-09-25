// microcode_test.go checks the early-microcode initrd packing.
//
// The kernel does not have to parse real microcode here, so all this checks is
// that (1) the result is a valid newc cpio, (2) its only regular file is at
// exactly the right path, and (3) the content matches the source .bin files
// sorted and concatenated.
//
// microcode_test.go - 조기 마이크로코드 initrd 팩킹 검증.
//
// 커널이 실제 마이크로코드를 파싱할 필요는 없으니, 여기서는 (1) 결과가
// 유효한 newc cpio 이고 (2) 유일한 정규 파일이 정확한 경로에 있으며
// (3) 내용이 소스 .bin 들을 정렬-이어붙인 것과 일치하는지만 확인한다.
package image

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"vibeldr/internal/ramdisk"
)

func writeFixture(t *testing.T, path string, size int, fill byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := bytes.Repeat([]byte{fill}, size)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// singleRegularFile pulls the one regular file entry out of an archive. More
// than one regular file fails the test.
//
// singleRegularFile - 아카이브에서 유일한 정규 파일 항목을 뽑아낸다.
// 정규 파일이 하나가 아니면 테스트 실패다.
func singleRegularFile(t *testing.T, a *ramdisk.Archive) *ramdisk.Entry {
	t.Helper()
	var found *ramdisk.Entry
	for i := range a.Entries {
		e := &a.Entries[i]
		if !e.IsRegular() {
			continue
		}
		if found != nil {
			t.Fatalf("expected exactly one regular file, also found %q", e.Name)
		}
		found = e
	}
	if found == nil {
		t.Fatal("no regular file in archive")
	}
	return found
}

func TestPackIntelUcode(t *testing.T) {
	dir := t.TempDir()
	// Two files, 32 bytes and 16 bytes. The lexicographic sort order is the
	// concatenation order.
	//
	// 파일 두 개: 32 바이트, 16 바이트. 사전순 정렬 결과가 concat 순서다.
	writeFixture(t, filepath.Join(dir, "intel-ucode", "00-00-00.bin"), 32, 0xAA)
	writeFixture(t, filepath.Join(dir, "intel-ucode", "01-01-01.bin"), 16, 0xBB)

	got, err := PackIntelUcode(dir)
	if err != nil {
		t.Fatalf("PackIntelUcode: %v", err)
	}

	a, err := ramdisk.ReadCPIO(got)
	if err != nil {
		t.Fatalf("ReadCPIO: %v", err)
	}
	e := singleRegularFile(t, a)
	if e.Name != "kernel/x86/microcode/GenuineIntel.bin" {
		t.Errorf("name = %q, want kernel/x86/microcode/GenuineIntel.bin", e.Name)
	}
	want := append(bytes.Repeat([]byte{0xAA}, 32), bytes.Repeat([]byte{0xBB}, 16)...)
	if !bytes.Equal(e.Data, want) {
		t.Errorf("data mismatch: len=%d want=%d", len(e.Data), len(want))
	}
}

func TestPackAMDUcode(t *testing.T) {
	dir := t.TempDir()
	// AMD only picks up microcode_amd*.bin; an unrelated file has to be ignored.
	// AMD 는 microcode_amd*.bin 만 잡는다. 무관한 파일은 무시되어야 한다.
	writeFixture(t, filepath.Join(dir, "amd-ucode", "microcode_amd.bin"), 32, 0xCC)
	writeFixture(t, filepath.Join(dir, "amd-ucode", "microcode_amd_fam17h.bin"), 16, 0xDD)
	writeFixture(t, filepath.Join(dir, "amd-ucode", "README"), 8, 0x00)

	got, err := PackAMDUcode(dir)
	if err != nil {
		t.Fatalf("PackAMDUcode: %v", err)
	}

	a, err := ramdisk.ReadCPIO(got)
	if err != nil {
		t.Fatalf("ReadCPIO: %v", err)
	}
	e := singleRegularFile(t, a)
	if e.Name != "kernel/x86/microcode/AuthenticAMD.bin" {
		t.Errorf("name = %q, want kernel/x86/microcode/AuthenticAMD.bin", e.Name)
	}
	want := append(bytes.Repeat([]byte{0xCC}, 32), bytes.Repeat([]byte{0xDD}, 16)...)
	if !bytes.Equal(e.Data, want) {
		t.Errorf("data mismatch: len=%d want=%d", len(e.Data), len(want))
	}
}

func TestPackIntelUcodeMissing(t *testing.T) {
	// With no intel-ucode/ at all it has to error clearly, so the mistake is
	// noticed.
	//
	// intel-ucode/ 자체가 없으면 명확히 에러가 나야 실수를 감지할 수 있다.
	dir := t.TempDir()
	if _, err := PackIntelUcode(dir); err == nil {
		t.Fatal("expected error for missing intel-ucode/")
	}
}
