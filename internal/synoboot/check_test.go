package synoboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// devNodes stands regular files in for /dev/synoboot<n>, which open for
// writing the way a writable partition node does.
//
// devNodes - /dev/synoboot<n> 대신 일반 파일을 둔다. 쓸 수 있는 파티션 노드처럼
// 쓰기로 열린다.
func devNodes(t *testing.T, parts ...int) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range parts {
		p := filepath.Join(dir, Name+string(rune('0'+n)))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckHealthyDisk(t *testing.T) {
	if got := Check(fixture(t), devNodes(t, 1, 2, 3), "sdb"); len(got) != 0 {
		t.Fatalf("got %+v, want no problems", got)
	}
}

func TestCheckMissingPartition(t *testing.T) {
	// sda has partitions 1 and 2 only.
	// sda 에는 파티션 1 과 2 만 있다.
	got := Check(fixture(t), devNodes(t, 1, 2), "sda")
	if len(got) != 1 || !strings.Contains(got[0].English, "no partition 3") {
		t.Fatalf("got %+v, want one missing-partition problem", got)
	}
}

func TestCheckReadOnlyDisk(t *testing.T) {
	sys := fixture(t)
	if err := os.WriteFile(filepath.Join(sys, "sdb", "ro"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The mark cannot be cleared here (no device node), so it stays, and the
	// read-only problem is the only one reported.
	//
	// 여기서는 표시를 지울 수 없으므로 (장치 노드가 없다) 남아 있고, 읽기 전용
	// 문제 하나만 알린다.
	got := Check(sys, devNodes(t, 1, 2, 3), "sdb")
	if len(got) != 1 || !strings.Contains(got[0].English, "read-only") {
		t.Fatalf("got %+v, want one read-only problem", got)
	}
}

func TestCheckNodeNotWritable(t *testing.T) {
	// Node 3 is missing from /dev although sysfs lists the partition.
	// sysfs 에는 파티션이 있는데 /dev 에 노드 3 이 없다.
	got := Check(fixture(t), devNodes(t, 1, 2), "sdb")
	if len(got) != 1 || !strings.Contains(got[0].English, "partition 3 cannot be opened") {
		t.Fatalf("got %+v, want one open-for-writing problem", got)
	}
}
