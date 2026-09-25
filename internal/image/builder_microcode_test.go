// builder_microcode_test.go is the integration check for WithMicrocode.
//
// With fixture .bin files in the microcode source directory, the build has to
// burn two files onto p3, intel-ucode.img and amd-ucode.img, and each has to be
// a valid newc cpio with the real data at the vendor's expected path.
//
// A source directory that is absent or empty is skipped quietly, with no
// warning: this path is opt-in, and "the option is on but the blobs have not
// been fetched yet" is a normal state.
//
// builder_microcode_test.go - WithMicrocode 통합 검증.
//
// 마이크로코드 소스 디렉터리에 fixture .bin 이 있으면 빌드가 p3 에
// intel-ucode.img / amd-ucode.img 두 파일을 굽고, 각각 유효한 newc cpio 인지
// (그리고 벤더별 예상 경로에 실 데이터가 들어 있는지) 확인한다.
//
// 소스 디렉터리 자체가 없거나 비어 있으면 warning 없이 조용히 스킵한다.
// 이 경로는 opt-in 이라 "옵션은 켰지만 아직 블롭을 안 받았다" 는 정상 상태다.
package image

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"vibeldr/internal/ramdisk"
)

// findP3File opens the p3 of an actually built image, finds the named file and
// returns it; (nil, false) when it is not there.
//
// findP3File - 실제로 빌드된 이미지 p3 을 열어 name 파일을 찾아 돌려준다.
// 없으면 (nil, false).
func findP3File(t *testing.T, path, name string) ([]byte, bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 partitions, got %d", len(parts))
	}
	p3 := openFAT(t, f, int64(parts[2].StartLBA)*SectorSize)
	e, ok := p3.find(p3.readDir(p3.rootClus), name)
	if !ok {
		return nil, false
	}
	return p3.readFile(e), true
}

func TestBuilderMicrocodePacksBothVendors(t *testing.T) {
	dir := t.TempDir()
	// The fixture plants a few real .bin files for each vendor. The contents are
	// arbitrary bytes - only the cpio magic and the payload match are checked, not
	// the CPU signatures.
	//
	// fixture: 두 벤더 각각에 실제 .bin 을 몇 개 심는다. 내용은 임의 바이트다.
	// cpio 매직과 페이로드 일치만 검사하고 CPU 시그니처는 안 본다.
	ucodeSrc := filepath.Join(dir, "microcode")
	writeFixture(t, filepath.Join(ucodeSrc, "intel-ucode", "06-00-00.bin"), 64, 0xA1)
	writeFixture(t, filepath.Join(ucodeSrc, "intel-ucode", "06-01-01.bin"), 32, 0xA2)
	writeFixture(t, filepath.Join(ucodeSrc, "amd-ucode", "microcode_amd.bin"), 48, 0xB1)
	writeFixture(t, filepath.Join(ucodeSrc, "amd-ucode", "microcode_amd_fam17h.bin"), 96, 0xB2)

	out := filepath.Join(dir, "loader.img")
	b := &Builder{
		TotalMB:            192,
		P1MB:               48,
		P2MB:               48,
		Labels:             [3]string{"LDR1", "LDR2", "LDR3"},
		VolumeIDSeed:       0x2000,
		WithMicrocode:      true,
		MicrocodeSourceDir: ucodeSrc,
	}
	res, err := b.Build(out, &Content{
		P3: []File{{Path: "initrd-dsm", Data: bytes.Repeat([]byte{0x11}, 1024)}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// With both vendors present no warning is expected; any that appear are
	// only logged, not failed.
	//
	// 두 벤더가 모두 있으면 warning 이 없어야 정상이다. 나오더라도 실패로
	// 치지 않고 로그만 남긴다.
	for _, w := range res.Warnings {
		t.Logf("warning: %s", w)
	}

	// Checking intel-ucode.img.
	// intel-ucode.img 검증.
	got, ok := findP3File(t, out, "intel-ucode.img")
	if !ok {
		t.Fatal("p3 is missing intel-ucode.img")
	}
	a, err := ramdisk.ReadCPIO(got)
	if err != nil {
		t.Fatalf("intel-ucode.img is not a valid cpio: %v", err)
	}
	e := singleRegularFile(t, a)
	if e.Name != "kernel/x86/microcode/GenuineIntel.bin" {
		t.Errorf("intel entry name = %q", e.Name)
	}
	wantIntel := append(bytes.Repeat([]byte{0xA1}, 64), bytes.Repeat([]byte{0xA2}, 32)...)
	if !bytes.Equal(e.Data, wantIntel) {
		t.Errorf("intel payload mismatch: len=%d want=%d", len(e.Data), len(wantIntel))
	}

	// Checking amd-ucode.img.
	// amd-ucode.img 검증.
	got, ok = findP3File(t, out, "amd-ucode.img")
	if !ok {
		t.Fatal("p3 is missing amd-ucode.img")
	}
	a, err = ramdisk.ReadCPIO(got)
	if err != nil {
		t.Fatalf("amd-ucode.img is not a valid cpio: %v", err)
	}
	e = singleRegularFile(t, a)
	if e.Name != "kernel/x86/microcode/AuthenticAMD.bin" {
		t.Errorf("amd entry name = %q", e.Name)
	}
	wantAMD := append(bytes.Repeat([]byte{0xB1}, 48), bytes.Repeat([]byte{0xB2}, 96)...)
	if !bytes.Equal(e.Data, wantAMD) {
		t.Errorf("amd payload mismatch: len=%d want=%d", len(e.Data), len(wantAMD))
	}

	// initrd-dsm has to still be there - the microcode must not push the real
	// ramdisk out.
	//
	// initrd-dsm 도 여전히 있어야 한다 (마이크로코드가 정규 램디스크를 밀어내면
	// 안 된다).
	if _, ok := findP3File(t, out, "initrd-dsm"); !ok {
		t.Error("p3 lost initrd-dsm after microcode was added")
	}
}

func TestBuilderMicrocodeSkipsWhenSourcesMissing(t *testing.T) {
	// The path where MicrocodeSourceDir does not exist at all: the build succeeds
	// and no ucode file appears.
	//
	// MicrocodeSourceDir 자체가 없는 경로. 빌드는 성공하고 ucode 파일은 안 생긴다.
	dir := t.TempDir()
	out := filepath.Join(dir, "loader.img")
	b := &Builder{
		TotalMB:            192,
		P1MB:               48,
		P2MB:               48,
		Labels:             [3]string{"LDR1", "LDR2", "LDR3"},
		WithMicrocode:      true,
		MicrocodeSourceDir: filepath.Join(dir, "no-such-microcode"),
	}
	if _, err := b.Build(out, nil); err != nil {
		t.Fatalf("Build should succeed when microcode dirs are missing: %v", err)
	}
	if _, ok := findP3File(t, out, "intel-ucode.img"); ok {
		t.Error("intel-ucode.img should not be present when source dir is missing")
	}
	if _, ok := findP3File(t, out, "amd-ucode.img"); ok {
		t.Error("amd-ucode.img should not be present when source dir is missing")
	}
}

func TestBuilderMicrocodeIntelOnlyLeavesAMDOut(t *testing.T) {
	// intel-ucode filled in and amd-ucode never created: only intel comes out and
	// amd falls away quietly, with no warning either - the user may simply not have
	// wanted amd.
	//
	// intel-ucode 는 채우고 amd-ucode 는 안 만든 경우. intel 만 나오고 amd 는
	// 조용히 빠진다. warning 도 없어야 한다 (사용자가 amd 를 원치 않은 것일 수도
	// 있다).
	dir := t.TempDir()
	ucodeSrc := filepath.Join(dir, "microcode")
	writeFixture(t, filepath.Join(ucodeSrc, "intel-ucode", "06-00-00.bin"), 16, 0xCC)

	out := filepath.Join(dir, "loader.img")
	b := &Builder{
		TotalMB:            192,
		P1MB:               48,
		P2MB:               48,
		Labels:             [3]string{"LDR1", "LDR2", "LDR3"},
		WithMicrocode:      true,
		MicrocodeSourceDir: ucodeSrc,
	}
	res, err := b.Build(out, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, w := range res.Warnings {
		t.Errorf("unexpected warning: %s", w)
	}
	if _, ok := findP3File(t, out, "intel-ucode.img"); !ok {
		t.Error("intel-ucode.img should be present")
	}
	if _, ok := findP3File(t, out, "amd-ucode.img"); ok {
		t.Error("amd-ucode.img should be absent when amd-ucode dir is missing")
	}
}
