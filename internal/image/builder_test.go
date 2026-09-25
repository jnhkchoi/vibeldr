package image

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCHSEncoding(t *testing.T) {
	// LBA 0 is cylinder 0, head 0, sector 1.
	// LBA 0 은 실린더 0, 헤드 0, 섹터 1 이다.
	if got := chs(0); got != [3]byte{0, 1, 0} {
		t.Errorf("chs(0) = %v, want [0 1 0]", got)
	}
	// Anything past the CHS addressing limit is pinned to the maximum triple.
	// CHS 주소 한계를 넘는 값은 최대 세 값으로 고정된다.
	got := chs(1 << 30)
	if got != [3]byte{254, 63 | 0xc0, 255} {
		t.Errorf("chs(1<<30) = %v, want the saturated maximum", got)
	}
}

func TestMBRRoundTrip(t *testing.T) {
	dev := newMemDevice(SectorSize)
	parts := []Partition{
		{Bootable: true, Type: TypeFAT32LBA, StartLBA: 2048, Sectors: 98304},
		{Type: TypeFAT32LBA, StartLBA: 100352, Sectors: 98304},
		{Type: TypeFAT32LBA, StartLBA: 198656, Sectors: 128000},
	}
	if err := WriteMBR(dev, parts, nil, 0xdeadbeef); err != nil {
		t.Fatalf("WriteMBR: %v", err)
	}

	got, err := ReadMBR(dev)
	if err != nil {
		t.Fatalf("ReadMBR: %v", err)
	}
	if len(got) != len(parts) {
		t.Fatalf("read %d partitions, want %d", len(got), len(parts))
	}
	for i := range parts {
		if got[i] != parts[i] {
			t.Errorf("partition %d = %+v, want %+v", i+1, got[i], parts[i])
		}
	}
	if !got[0].Bootable {
		t.Error("partition 1 should be marked bootable")
	}
}

func TestMBRPreservesBootCode(t *testing.T) {
	dev := newMemDevice(SectorSize)
	// 440 bytes, not 446: the four bytes after it are the disk signature, which
	// WriteMBR owns.
	//
	// 446 이 아니라 440 바이트다. 그 뒤 4 바이트는 디스크 서명이고 WriteMBR 가 쓴다.
	code := bytes.Repeat([]byte{0x90}, mbrBootCodeSize) // NOPs stand in for GRUB / GRUB 대신 NOP
	parts := []Partition{{Type: TypeFAT32LBA, StartLBA: 2048, Sectors: 1024}}

	if err := WriteMBR(dev, parts, code, 1); err != nil {
		t.Fatal(err)
	}
	back, err := ReadBootCode(dev)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, code) {
		t.Error("boot code was not preserved through WriteMBR")
	}
}

func TestReadMBRRejectsGarbage(t *testing.T) {
	dev := newMemDevice(SectorSize)
	if _, err := ReadMBR(dev); err == nil {
		t.Error("an all-zero sector is not an MBR and should be rejected")
	}
}

func TestBuilderValidation(t *testing.T) {
	cases := map[string]*Builder{
		"total too small": {TotalMB: 64, P1MB: 48, P2MB: 48},
		"p1 too small":    {TotalMB: 512, P1MB: 8, P2MB: 48},
		"p2 too small":    {TotalMB: 512, P1MB: 48, P2MB: 8},
	}
	for name, b := range cases {
		if _, err := b.Build(filepath.Join(t.TempDir(), "x.img"), nil); err == nil {
			t.Errorf("%s: should have been rejected", name)
		}
	}
}

func TestBuilderPartitionsDoNotFit(t *testing.T) {
	b := &Builder{TotalMB: 256, P1MB: 128, P2MB: 128}
	if _, err := b.Build(filepath.Join(t.TempDir(), "x.img"), nil); err == nil {
		t.Error("partitions that exactly consume the image leave nothing for p3 and should fail")
	}
}

// The end-to-end check: build a real image file, then read its partition table
// and all three filesystems back with an independent reader.
//
// 끝에서 끝까지 확인한다. 실제 이미지 파일을 만든 뒤 파티션 테이블과 세
// 파일시스템을 별도의 리더로 다시 읽는다.
func TestBuilderEndToEnd(t *testing.T) {
	out := filepath.Join(t.TempDir(), "loader.img")

	b := &Builder{
		TotalMB:      192,
		P1MB:         48,
		P2MB:         48,
		Labels:       [3]string{"LDR1", "LDR2", "LDR3"},
		VolumeIDSeed: 0x1000,
	}

	grubCfg := []byte("set timeout=5\nmenuentry 'DSM' { linux /bzImage-dsm }\n")
	zImage := bytes.Repeat([]byte{0x11}, 300000)
	payload := bytes.Repeat([]byte{0x22}, 500000)

	content := &Content{
		P1: []File{
			{Path: "boot/grub/grub.cfg", Data: grubCfg},
			{Path: "loader.yaml", Data: []byte("model: SA6400\n")},
		},
		P2: []File{{Path: "zImage", Data: zImage}},
		P3: []File{{Path: "initrd-dsm", Data: payload}},
	}

	res, err := b.Build(out, content)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Partitions) != 3 {
		t.Fatalf("got %d partitions, want 3", len(res.Partitions))
	}

	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != res.SizeBytes {
		t.Errorf("file is %d bytes, result claims %d", st.Size(), res.SizeBytes)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatalf("ReadMBR: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("MBR reports %d partitions, want 3", len(parts))
	}
	if parts[0].StartLBA != FirstPartitionLBA {
		t.Errorf("partition 1 starts at %d, want %d", parts[0].StartLBA, FirstPartitionLBA)
	}
	// Partitions must tile the disk without gaps or overlap.
	// 파티션은 틈도 겹침도 없이 디스크를 이어서 채워야 한다.
	for i := 1; i < len(parts); i++ {
		if parts[i].StartLBA != parts[i-1].End() {
			t.Errorf("partition %d starts at %d but partition %d ends at %d",
				i+1, parts[i].StartLBA, i, parts[i-1].End())
		}
	}

	// Partition 1: nested directory and a file in the root.
	// 파티션 1: 중첩 디렉터리와 루트의 파일.
	p1 := openFAT(t, f, int64(parts[0].StartLBA)*SectorSize)
	if p1.label != "LDR1" {
		t.Errorf("p1 label = %q, want LDR1", p1.label)
	}
	root1 := p1.readDir(p1.rootClus)
	boot, ok := p1.find(root1, "boot")
	if !ok {
		t.Fatalf("p1 is missing the boot directory: %+v", root1)
	}
	grubDir, ok := p1.find(p1.readDir(boot.Cluster), "grub")
	if !ok {
		t.Fatal("p1 is missing boot/grub")
	}
	cfg, ok := p1.find(p1.readDir(grubDir.Cluster), "grub.cfg")
	if !ok {
		t.Fatal("p1 is missing boot/grub/grub.cfg")
	}
	if got := p1.readFile(cfg); !bytes.Equal(got, grubCfg) {
		t.Error("grub.cfg content mismatch")
	}
	if _, ok := p1.find(root1, "loader.yaml"); !ok {
		t.Error("p1 is missing loader.yaml")
	}

	// Partition 2: the untouched DSM kernel.
	// 파티션 2: 손대지 않은 DSM 커널.
	p2 := openFAT(t, f, int64(parts[1].StartLBA)*SectorSize)
	e, ok := p2.find(p2.readDir(p2.rootClus), "zImage")
	if !ok {
		t.Fatal("p2 is missing zImage")
	}
	if got := p2.readFile(e); !bytes.Equal(got, zImage) {
		t.Error("zImage content mismatch")
	}

	// Partition 3: the payload.
	// 파티션 3: 페이로드.
	p3 := openFAT(t, f, int64(parts[2].StartLBA)*SectorSize)
	e, ok = p3.find(p3.readDir(p3.rootClus), "initrd-dsm")
	if !ok {
		t.Fatal("p3 is missing initrd-dsm")
	}
	if got := p3.readFile(e); !bytes.Equal(got, payload) {
		t.Error("initrd-dsm content mismatch")
	}
}

func TestBuilderFileFromSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.bin")
	want := bytes.Repeat([]byte{0x7f}, 12345)
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "loader.img")
	b := DefaultBuilder()
	b.TotalMB = 192
	b.P1MB = 48
	b.P2MB = 48
	if _, err := b.Build(out, &Content{P3: []File{{Path: "payload.bin", Source: src}}}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parts, _ := ReadMBR(f)
	p3 := openFAT(t, f, int64(parts[2].StartLBA)*SectorSize)
	e, ok := p3.find(p3.readDir(p3.rootClus), "payload.bin")
	if !ok {
		t.Fatal("payload.bin missing")
	}
	if got := p3.readFile(e); !bytes.Equal(got, want) {
		t.Error("file streamed from disk does not match")
	}
}

func TestBuilderDonorBootArea(t *testing.T) {
	dir := t.TempDir()
	donorPath := filepath.Join(dir, "donor.img")

	// A donor with a plausible boot area: recognisable MBR code plus a marker
	// in the BIOS boot gap where GRUB's core image would live.
	//
	// 그럴듯한 부트 영역을 가진 donor. 알아볼 수 있는 MBR 코드와, GRUB core
	// 이미지가 놓일 BIOS 부트 틈의 표식을 넣는다.
	donorSize := int64(FirstPartitionLBA+1024) * SectorSize
	donor := make([]byte, donorSize)
	copy(donor, bytes.Repeat([]byte{0xeb}, 446))
	copy(donor[SectorSize:], []byte("GRUB-CORE-IMAGE-MARKER"))
	donor[510], donor[511] = 0x55, 0xaa
	// One partition entry so ReadMBR accepts it.
	// ReadMBR 가 받아들이도록 파티션 항목 하나를 넣는다.
	donor[446+4] = byte(TypeFAT32LBA)
	donor[446+8] = 0x00
	donor[446+9] = 0x08 // LBA 2048, little endian / LBA 2048, 리틀 엔디언
	donor[446+12] = 0x00
	donor[446+13] = 0x04 // 1024 sectors / 1024 섹터
	if err := os.WriteFile(donorPath, donor, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "loader.img")
	b := &Builder{TotalMB: 192, P1MB: 48, P2MB: 48, DonorPath: donorPath}
	res, err := b.Build(out, nil)
	if err != nil {
		t.Fatalf("Build with donor: %v", err)
	}
	// This donor has no readable filesystem on partition 1, so the build must
	// warn that GRUB's modules could not be collected rather than pretending
	// the image is complete.
	//
	// 이 donor 는 파티션 1 에 읽을 수 있는 파일시스템이 없다. 그러니 빌드는
	// 이미지가 완전한 척하지 말고 GRUB 모듈을 모으지 못했다고 경고해야 한다.
	if len(res.Warnings) == 0 {
		t.Error("a donor with an unreadable partition 1 should produce a warning")
	}

	built, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(built[:mbrBootCodeSize], bytes.Repeat([]byte{0xeb}, mbrBootCodeSize)) {
		t.Error("donor MBR boot code was not carried over")
	}
	if !bytes.Contains(built[SectorSize:FirstPartitionLBA*SectorSize], []byte("GRUB-CORE-IMAGE-MARKER")) {
		t.Error("donor BIOS boot gap was not carried over")
	}
	// Our own partition table must still win over the donor's.
	// 그래도 파티션 테이블은 donor 것이 아니라 우리 것이어야 한다.
	parts, err := ReadMBR(bytes.NewReader(built))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Errorf("built image has %d partitions, want our 3", len(parts))
	}
}

// makeDonor builds a realistic donor: an MBR with boot code, a marker in the
// BIOS boot gap, and a real FAT32 partition 1 holding GRUB's modules.
//
// makeDonor 는 실제와 비슷한 donor 를 만든다. 부트 코드가 든 MBR, BIOS 부트
// 틈의 표식, GRUB 모듈을 담은 진짜 FAT32 파티션 1 로 이뤄진다.
func makeDonor(t *testing.T, path string) {
	t.Helper()
	const p1Sectors = 48 * (1 << 20) / SectorSize
	total := FirstPartitionLBA + p1Sectors
	dev := newMemDevice(int(total) * SectorSize)

	parts := []Partition{{Bootable: true, Type: TypeFAT32LBA, StartLBA: FirstPartitionLBA, Sectors: p1Sectors}}
	if err := WriteMBR(dev, parts, bytes.Repeat([]byte{0xeb}, mbrBootCodeSize), 0xabcd1234); err != nil {
		t.Fatal(err)
	}
	copy(dev.data[SectorSize:], []byte("GRUB-CORE-IMAGE-MARKER"))

	fs, err := FormatFAT32(dev, int64(FirstPartitionLBA)*SectorSize, p1Sectors, "DONOR", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFileBytes("boot/grub/i386-pc/normal.mod", bytes.Repeat([]byte{0x11}, 5000)); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFileBytes("boot/grub/i386-pc/fat.mod", bytes.Repeat([]byte{0x22}, 3000)); err != nil {
		t.Fatal(err)
	}
	// The donor's own grub.cfg must NOT survive into the built image.
	// donor 자신의 grub.cfg 는 빌드된 이미지에 남으면 안 된다.
	if err := fs.AddFileBytes("boot/grub/grub.cfg", []byte("DONOR CONFIG - must be replaced")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, dev.data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Copying a donor's boot sectors without its GRUB modules lands the boot in
// `grub rescue>`. This test covers that failure.
//
// donor 의 부트 섹터만 복사하고 GRUB 모듈을 빼면 부팅이 `grub rescue>` 에
// 떨어진다. 이 테스트는 그 실패를 막는다.
func TestBuilderCopiesDonorGrubModules(t *testing.T) {
	dir := t.TempDir()
	donorPath := filepath.Join(dir, "donor.img")
	makeDonor(t, donorPath)

	ourCfg := []byte("set timeout=1\nmenuentry 'ours' {}\n")
	out := filepath.Join(dir, "loader.img")
	b := &Builder{TotalMB: 192, P1MB: 48, P2MB: 48, DonorPath: donorPath, Labels: [3]string{"L1", "L2", "L3"}}
	res, err := b.Build(out, &Content{
		P1: []File{{Path: "boot/grub/grub.cfg", Data: ourCfg}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("a readable donor should produce no warnings, got %v", res.Warnings)
	}
	if res.DonatedFiles != 2 {
		t.Errorf("expected 2 donated files (the two .mod), got %d", res.DonatedFiles)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatal(err)
	}

	fr, err := OpenFAT32(f, int64(parts[0].StartLBA)*SectorSize)
	if err != nil {
		t.Fatalf("read built p1: %v", err)
	}
	got := map[string][]byte{}
	if err := fr.WalkAll(func(p string, e Entry) error {
		data, err := fr.ReadFile(e)
		if err != nil {
			return err
		}
		got[p] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got["boot/grub/i386-pc/normal.mod"]) != 5000 {
		t.Errorf("normal.mod did not travel with the boot code (got %d bytes)", len(got["boot/grub/i386-pc/normal.mod"]))
	}
	if len(got["boot/grub/i386-pc/fat.mod"]) != 3000 {
		t.Errorf("fat.mod missing (got %d bytes)", len(got["boot/grub/i386-pc/fat.mod"]))
	}
	if !bytes.Equal(got["boot/grub/grub.cfg"], ourCfg) {
		t.Errorf("our grub.cfg should win over the donor's, got %q", got["boot/grub/grub.cfg"])
	}
}

func TestBuilderRejectsMissingDonor(t *testing.T) {
	b := &Builder{TotalMB: 192, P1MB: 48, P2MB: 48, DonorPath: filepath.Join(t.TempDir(), "nope.img")}
	_, err := b.Build(filepath.Join(t.TempDir(), "out.img"), nil)
	if err == nil {
		t.Fatal("a missing donor should fail the build")
	}
	if !strings.Contains(err.Error(), "donor") {
		t.Errorf("error should mention the donor, got: %v", err)
	}
}

// A failed build must not leave a half-written image that looks usable.
// 실패한 빌드는 쓸 만해 보이는 반쯤 쓴 이미지를 남기면 안 된다.
func TestBuilderCleansUpOnFailure(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "broken.img")
	b := &Builder{TotalMB: 192, P1MB: 48, P2MB: 48, DonorPath: filepath.Join(dir, "missing.img")}
	if _, err := b.Build(out, nil); err == nil {
		t.Fatal("expected the build to fail")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("the partial image should have been removed")
	}
}

// TestBootstrapBuild covers bootstrap mode: only P1 is filled and P2 and P3 are
// empty FAT. vibeldr-boot fills P2 and P3 at boot time in that scenario, so at
// this stage the only thing of interest is whether the three files GRUB loads -
// grub.cfg, vmlinuz, initrd-vibeldr - really exist on P1.
//
// TestBootstrapBuild - 부트스트랩 모드: P1 만 채우고 P2/P3 은 빈 FAT.
// vibeldr-boot 이 부팅 시점에 P2/P3 을 채우는 시나리오라, 이 단계에서는
// GRUB 이 로드할 세 파일 (grub.cfg / vmlinuz / initrd-vibeldr) 이 P1 에
// 실제 존재하는지가 유일한 관심사다.
func TestBootstrapBuild(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bootstrap.img")

	kernel := bytes.Repeat([]byte{0xA5}, 200000)
	initrd := bytes.Repeat([]byte{0x5A}, 40000)
	grubCfg := BootstrapGrubConfig()

	content := &Content{
		P1: []File{
			{Path: "boot/grub/grub.cfg", Data: grubCfg},
			{Path: BootstrapKernelName, Data: kernel},
			{Path: BootstrapInitrdName, Data: initrd},
		},
	}
	b := &Builder{
		TotalMB: 192, P1MB: 48, P2MB: 48,
		Labels:    [3]string{"BS1", "BS2", "BS3"},
		Bootstrap: true,
	}
	res, err := b.Build(out, content)
	if err != nil {
		t.Fatalf("Bootstrap Build: %v", err)
	}
	if len(res.Partitions) != 3 {
		t.Fatalf("파티션 %d 개, want 3", len(res.Partitions))
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatal(err)
	}

	// Checking the three files are in P1.
	// P1 안에 세 파일 존재 확인.
	p1 := openFAT(t, f, int64(parts[0].StartLBA)*SectorSize)
	if p1.label != "BS1" {
		t.Errorf("p1 label = %q, want BS1", p1.label)
	}
	root := p1.readDir(p1.rootClus)
	if e, ok := p1.find(root, BootstrapKernelName); !ok {
		t.Errorf("p1 에 %s 없음", BootstrapKernelName)
	} else if got := p1.readFile(e); !bytes.Equal(got, kernel) {
		t.Errorf("커널 바이트 불일치")
	}
	if e, ok := p1.find(root, BootstrapInitrdName); !ok {
		t.Errorf("p1 에 %s 없음", BootstrapInitrdName)
	} else if got := p1.readFile(e); !bytes.Equal(got, initrd) {
		t.Errorf("initrd 바이트 불일치")
	}
	boot, ok := p1.find(root, "boot")
	if !ok {
		t.Fatal("p1 에 boot 디렉터리 없음")
	}
	grubDir, ok := p1.find(p1.readDir(boot.Cluster), "grub")
	if !ok {
		t.Fatal("p1 에 boot/grub 없음")
	}
	cfg, ok := p1.find(p1.readDir(grubDir.Cluster), "grub.cfg")
	if !ok {
		t.Fatal("p1 에 boot/grub/grub.cfg 없음")
	}
	if got := p1.readFile(cfg); !bytes.Equal(got, grubCfg) {
		t.Error("grub.cfg 내용 불일치")
	}

	// P2 and P3 have to open as valid FAT32 with an empty root.
	// P2, P3 는 유효한 FAT32 로 열려야 하고, root 는 비어 있어야 한다.
	for i, p := range parts[1:3] {
		fs := openFAT(t, f, int64(p.StartLBA)*SectorSize)
		entries := fs.readDir(fs.rootClus)
		if len(entries) != 0 {
			t.Errorf("p%d 가 비어있지 않음: %d 개 항목", i+2, len(entries))
		}
	}

	// P4, the driver pack, must not be created in bootstrap mode.
	// P4 (드라이버 팩) 는 부트스트랩 모드에서 만들지 않아야 한다.
	if len(res.Partitions) != 3 {
		t.Errorf("부트스트랩에 P4 가 붙음")
	}
}

// Two builds of the same inputs have to produce the same bytes. Without that a
// checksum cannot answer the only question it is ever asked: is the image on
// the server the image that was just built? Chasing a fix that was never
// deployed costs an hour and looks exactly like a fix that does not work.
//
// 같은 입력으로 두 번 빌드하면 같은 바이트가 나와야 한다. 그렇지 않으면
// 체크섬이 유일하게 받는 질문, 곧 서버에 있는 이미지가 방금 만든 이미지인지에
// 답하지 못한다. 배포되지 않은 수정을 쫓으면 한 시간이 들고, 그 모습은 효과
// 없는 수정과 똑같다.
func TestBuildIsReproducible(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "donated.bin")
	if err := os.WriteFile(src, bytes.Repeat([]byte{0x5a}, 40000), 0o644); err != nil {
		t.Fatal(err)
	}
	content := &Content{
		P1: []File{
			{Path: "boot/grub/grub.cfg", Data: []byte("set timeout=1\n")},
			{Path: "boot/grub/i386-pc/normal.mod", Source: src},
		},
		P2: []File{{Path: "zImage", Data: bytes.Repeat([]byte{0x11}, 200000)}},
		P3: []File{{Path: "initrd-dsm", Data: bytes.Repeat([]byte{0x22}, 300000)}},
	}

	build := func(name string) []byte {
		b := &Builder{TotalMB: 192, P1MB: 48, P2MB: 48,
			Labels: [3]string{"LDR1", "LDR2", "LDR3"}}
		out := filepath.Join(dir, name)
		if _, err := b.Build(out, content); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	first, second := build("a.img"), build("b.img")
	if !bytes.Equal(first, second) {
		var n, at int
		for i := range first {
			if first[i] != second[i] {
				if n == 0 {
					at = i
				}
				n++
			}
		}
		t.Fatalf("two identical builds differ in %d bytes, first at offset %d", n, at)
	}

	// Different content (here, a file one byte longer) must still give
	// different volume serials, or the serial stops telling two volumes apart.
	//
	// 내용이 다르면 (여기서는 파일이 1 바이트 더 길면) 볼륨 시리얼도 달라야
	// 한다. 아니면 시리얼로 두 볼륨을 구분할 수 없다.
	other := *content
	other.P2 = []File{{Path: "zImage", Data: bytes.Repeat([]byte{0x11}, 200001)}}
	b := &Builder{TotalMB: 192, P1MB: 48, P2MB: 48, Labels: [3]string{"LDR1", "LDR2", "LDR3"}}
	if contentSeed(b, content) == contentSeed(b, &other) {
		t.Error("two different images were given the same volume serial")
	}
}
