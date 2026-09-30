package image

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/ramdisk"
)

// TestBuiltImageCarriesThePatchedRamdisk opens the image that was actually
// written and follows it all the way down: FAT32 on partition 3, the cpio
// inside it, the helper inside that. It is skipped when no image has been
// built.
//
// TestBuiltImageCarriesThePatchedRamdisk 는 실제로 쓰인 이미지를 열어 끝까지
// 따라 내려간다. 파티션 3 의 FAT32, 그 안의 cpio, 다시 그 안의 헬퍼 순이다.
// 빌드된 이미지가 없으면 건너뛴다.
func TestBuiltImageCarriesThePatchedRamdisk(t *testing.T) {
	path := filepath.Join("..", "..", "work", "out", "loader.img")
	f, err := os.Open(path)
	if err != nil {
		t.Skip("work/out/loader.img has not been built")
	}
	defer f.Close()

	parts, err := ReadMBR(f)
	if err != nil {
		t.Fatalf("ReadMBR: %v", err)
	}
	// Three filesystems, and a fourth partition when the image carries a
	// driver pack - that one holds a raw archive rather than a filesystem.
	//
	// 파일시스템 셋, 그리고 드라이버 팩을 실으면 네 번째 파티션이 붙는다. 그
	// 파티션은 파일시스템이 아니라 raw 아카이브를 담는다.
	if len(parts) < 3 || len(parts) > 4 {
		t.Fatalf("got %d partitions, want 3 or 4", len(parts))
	}

	p3 := parts[2]
	fs, err := OpenFAT32(f, int64(p3.StartLBA)*SectorSize)
	if err != nil {
		t.Fatalf("OpenFAT32 on p3: %v", err)
	}
	root, err := fs.Root()
	if err != nil {
		t.Fatalf("reading the root directory: %v", err)
	}
	byName := map[string]Entry{}
	for _, e := range root {
		byName[e.Name] = e
	}
	for _, want := range []string{"zImage-dsm", "initrd-dsm"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("%s is not on partition 3; root holds %v", want, root)
		}
	}
	initrd, err := fs.ReadFile(byName["initrd-dsm"])
	if err != nil {
		t.Fatalf("reading initrd-dsm: %v", err)
	}

	a, err := ramdisk.ReadCPIO(initrd)
	if err != nil {
		t.Fatalf("the ramdisk on the image is not a readable cpio: %v", err)
	}
	helper, ok := a.Get(ramdisk.InitName)
	if !ok {
		t.Fatal("the helper is not on the image")
	}
	if !bytes.HasPrefix(helper.Data, []byte{0x7f, 'E', 'L', 'F'}) {
		t.Fatal("the helper on the image is not an ELF binary")
	}
	if helper.Mode&0o111 == 0 {
		t.Fatalf("the helper is not executable (mode %o)", helper.Mode&0o777)
	}

	script, ok := a.Get("linuxrc.syno.impl")
	if !ok {
		t.Fatal("linuxrc.syno.impl is missing")
	}
	if !strings.Contains(string(script.Data), "/"+ramdisk.InitName) {
		t.Fatal("the boot script does not call the helper")
	}
	t.Logf("p3 holds a %d byte ramdisk with %d files; helper is %d bytes",
		len(initrd), len(a.Entries), len(helper.Data))
}
