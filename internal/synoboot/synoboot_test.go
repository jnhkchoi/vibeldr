package synoboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a sysfs block tree with two disks and one of everything that
// must be ignored.
//
// fixture - 디스크 둘과, 무시해야 할 종류를 하나씩 담은 sysfs 블록 트리를
// 만든다.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sda/dev", "8:0")
	write("sda/sda1/dev", "8:1")
	write("sda/sda1/partition", "1")
	write("sda/sda2/dev", "8:2")
	write("sda/sda2/partition", "2")

	write("sdb/dev", "8:16")
	// Out of order on disk, to prove the sort happens.
	// 정렬이 일어나는지 보려고 일부러 순서를 섞어 둔다.
	write("sdb/sdb3/dev", "8:19")
	write("sdb/sdb3/partition", "3")
	write("sdb/sdb1/dev", "8:17")
	write("sdb/sdb1/partition", "1")
	write("sdb/sdb2/dev", "8:18")
	write("sdb/sdb2/partition", "2")

	write("loop0/dev", "7:0")
	write("md0/dev", "9:0")
	write("sr0/dev", "11:0")
	return root
}

func TestPartitionsAreSortedByNumber(t *testing.T) {
	got, err := partitions(fixture(t), "sdb")
	if err != nil {
		t.Fatalf("partitions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d partitions, want 3", len(got))
	}
	for i, want := range []partition{{1, 8, 17}, {2, 8, 18}, {3, 8, 19}} {
		if got[i] != want {
			t.Errorf("partition %d = %+v, want %+v", i, got[i], want)
		}
	}
}

func TestReadDevNumber(t *testing.T) {
	maj, min, err := readDevNumber(filepath.Join(fixture(t), "sdb", "dev"))
	if err != nil {
		t.Fatalf("readDevNumber: %v", err)
	}
	if maj != 8 || min != 16 {
		t.Fatalf("got %d:%d, want 8:16", maj, min)
	}
}

func TestReadDevNumberRejectsRubbish(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dev")
	if err := os.WriteFile(p, []byte("not-a-device\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readDevNumber(p); err == nil {
		t.Error("expected an error for a malformed dev file")
	}
}

// A RAM disk, a loop device or a RAID array is never the loader, and looking
// at them wastes time and can block on an optical drive with no disc in it.
//
// RAM 디스크, loop 장치, RAID 어레이는 절대 로더가 아니다. 들여다보면 시간만
// 들고, 디스크가 없는 광학 드라이브에서는 멈춰 버릴 수도 있다.
func TestIsVirtual(t *testing.T) {
	for _, name := range []string{"loop0", "ram3", "md127", "dm-0", "sr0", "zram1"} {
		if !isVirtual(name) {
			t.Errorf("%q should be skipped", name)
		}
	}
	for _, name := range []string{"sda", "sdb", "nvme0n1", "vda"} {
		if isVirtual(name) {
			t.Errorf("%q should not be skipped", name)
		}
	}
}

// Nothing in the fixture carries the label, and the error has to name what was
// looked at - a boot log saying only "not found" would leave no way to tell a
// missing loader from a missing /dev.
//
// 픽스처의 어느 디스크에도 라벨이 없고, 오류는 무엇을 봤는지 밝혀야 한다.
// 부팅 로그에 "not found" 만 있으면 로더가 없는 건지 /dev 가 없는 건지 가릴
// 수 없다.
func TestFindLoaderDiskReportsWhatItSaw(t *testing.T) {
	_, err := findLoaderDisk(fixture(t), t.TempDir(), "VIBELDR1")
	if err == nil {
		t.Fatal("expected an error when no disk carries the label")
	}
	for _, want := range []string{"VIBELDR1", "sda", "sdb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "loop0") {
		t.Error("virtual devices should not appear in the error")
	}
}

// The encoding is not simply major<<8|minor; Linux widened both fields and put
// the extra bits further up. Small numbers come out the same either way, which
// is what makes getting it wrong easy to miss.
//
// 인코딩은 단순한 major<<8|minor 가 아니다. 리눅스가 두 필드를 넓히면서 늘어난
// 비트를 위쪽에 두었다. 작은 번호는 어느 쪽이든 같은 값이 나와서, 틀려도
// 놓치기 쉽다.
func TestMakedev(t *testing.T) {
	cases := []struct {
		major, minor uint32
		want         uint64
	}{
		{8, 0, 0x800},     // sda
		{8, 17, 0x811},    // sdb1
		{259, 0, 0x10300}, // nvme0n1
		{8, 300, 0x100800 | 0x2c},
	}
	for _, c := range cases {
		if got := makedev(c.major, c.minor); got != c.want {
			t.Errorf("makedev(%d, %d) = %#x, want %#x", c.major, c.minor, got, c.want)
		}
	}
}
