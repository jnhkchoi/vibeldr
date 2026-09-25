package kmod

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// fakeKO builds the smallest ELF that ReadBytes accepts: a .modinfo section
// holding the given key=value fields, and the section name table.
//
// fakeKO - ReadBytes 가 받아들이는 가장 작은 ELF 를 만든다: 주어진 key=value
// 필드를 담은 .modinfo 섹션과 섹션 이름 표.
func fakeKO(fields ...string) []byte {
	modinfo := []byte(strings.Join(fields, "\x00") + "\x00")
	shstr := []byte("\x00.modinfo\x00.shstrtab\x00")
	le := binary.LittleEndian

	var b bytes.Buffer
	hdr := make([]byte, 64)
	copy(hdr, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	le.PutUint16(hdr[16:], 1)  // ET_REL / 재배치 파일
	le.PutUint16(hdr[18:], 62) // EM_X86_64 / x86-64
	le.PutUint32(hdr[20:], 1)
	shoff := 64 + len(modinfo) + len(shstr)
	shoff += -shoff & 7
	le.PutUint64(hdr[40:], uint64(shoff))
	le.PutUint16(hdr[52:], 64)
	le.PutUint16(hdr[58:], 64)
	le.PutUint16(hdr[60:], 3)
	le.PutUint16(hdr[62:], 2)
	b.Write(hdr)
	b.Write(modinfo)
	b.Write(shstr)
	for b.Len() < shoff {
		b.WriteByte(0)
	}
	section := func(name, typ uint32, off, size int) {
		sh := make([]byte, 64)
		le.PutUint32(sh[0:], name)
		le.PutUint32(sh[4:], typ)
		le.PutUint64(sh[24:], uint64(off))
		le.PutUint64(sh[32:], uint64(size))
		le.PutUint64(sh[48:], 1)
		b.Write(sh)
	}
	section(0, 0, 0, 0)
	section(1, 1, 64, len(modinfo))             // .modinfo, SHT_PROGBITS / .modinfo 섹션
	section(10, 3, 64+len(modinfo), len(shstr)) // .shstrtab, SHT_STRTAB / 섹션 이름 표
	return b.Bytes()
}

const vm44 = "vermagic=4.4.302+ SMP mod_unload "

func TestFakeKOReads(t *testing.T) {
	m, err := ReadBytes("e1000e.ko", fakeKO(vm44, "alias=pci:v00008086d*", "depends=ptp"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Vermagic != "4.4.302+ SMP mod_unload " || len(m.Aliases) != 1 || len(m.Depends) != 1 {
		t.Fatalf("got %+v", m)
	}
}

func TestMergeOriginals(t *testing.T) {
	ours := Pack{
		"e1000e.ko":     fakeKO(vm44, "alias=pci:v00008086d*", "version=ours"),
		"atlantic.ko":   fakeKO(vm44, "alias=pci:v00001D6Ad*", "version=ours"),
		"i2c-i801.ko":   fakeKO(vm44, "alias=pci:v00008086d0000A123*", "version=ours"),
		"virtio_net.ko": fakeKO(vm44, "alias=virtio:d00000001v*"),
	}
	orig := Pack{
		// A replacement, and what it depends on (no alias of its own).
		// 교체되는 것과, 그것이 의존하는 것 (자기 alias 는 없다).
		"e1000e.ko": fakeKO(vm44, "alias=pci:v00008086d*", "depends=ptp", "version=syno"),
		"ptp.ko":    fakeKO(vm44),
		// Kept ours on purpose.
		// 일부러 우리 것을 남긴다.
		"atlantic.ko": fakeKO(vm44, "alias=pci:v00001D6Ad*", "version=syno"),
		// A name spelled with "_" where ours has "-".
		// 우리 것은 "-", 원본은 "_" 로 쓴 이름.
		"i2c_i801.ko": fakeKO(vm44, "alias=pci:v00008086d0000A123*", "version=syno"),
		// A hardware driver we lack: added.
		// 우리에게 없는 하드웨어 드라이버: 추가.
		"r8168.ko": fakeKO(vm44, "alias=pci:v000010ECd00008168*"),
		// No device alias: left to DSM.
		// 장치 alias 가 없다: DSM 몫.
		"synobios.ko": fakeKO(vm44, "alias=char-major-201"),
		// Another kernel release: never used.
		// 다른 커널 release: 절대 안 쓴다.
		"wrongkern.ko": fakeKO("vermagic=5.10.55+ SMP mod_unload ", "alias=pci:v00001234d*"),
	}

	out, rep, err := MergeOriginals(ours, orig)
	if err != nil {
		t.Fatal(err)
	}
	same := func(name string, want []byte) {
		t.Helper()
		if !bytes.Equal(out[name], want) {
			t.Errorf("%s is not the expected copy", name)
		}
	}
	same("e1000e.ko", orig["e1000e.ko"])
	same("ptp.ko", orig["ptp.ko"])
	same("r8168.ko", orig["r8168.ko"])
	same("atlantic.ko", ours["atlantic.ko"])
	same("i2c-i801.ko", orig["i2c_i801.ko"])
	same("virtio_net.ko", ours["virtio_net.ko"])
	for _, gone := range []string{"synobios.ko", "wrongkern.ko", "i2c_i801.ko"} {
		if _, ok := out[gone]; ok {
			t.Errorf("%s should not be in the pack", gone)
		}
	}
	if got, want := string(out[OriginalsList]), "e1000e\ni2c_i801\nptp\nr8168\n"; got != want {
		t.Errorf("%s = %q, want %q", OriginalsList, got, want)
	}
	if strings.Join(rep.Kept, ",") != "atlantic" || strings.Join(rep.WrongRelease, ",") != "wrongkern" {
		t.Errorf("report %+v", rep)
	}
	if _, ok := ours[OriginalsList]; ok {
		t.Error("the pack passed in was changed")
	}

	// Merging again gives the same pack: a pack from partition 4 that already
	// has the originals comes out as it went in.
	//
	// 다시 합쳐도 같은 팩이다: 원본이 이미 든 파티션 4 의 팩은 들어간 그대로 나온다.
	again, _, err := MergeOriginals(out, orig)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(out) {
		t.Fatalf("second merge has %d entries, first %d", len(again), len(out))
	}
	for name, data := range out {
		if !bytes.Equal(again[name], data) {
			t.Errorf("second merge changed %s", name)
		}
	}
}

func TestMergeOriginalsNeedsAVermagic(t *testing.T) {
	if _, _, err := MergeOriginals(Pack{"x.ko": fakeKO("alias=pci:*")}, Pack{}); err == nil {
		t.Fatal("a pack with no vermagic was accepted")
	}
}
