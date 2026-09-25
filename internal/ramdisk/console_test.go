package ramdisk

import (
	"bytes"
	"testing"
)

// consoleArchive builds a ramdisk with a busybox carrying the linuxrc line and
// one module of the given kernel version.
//
// consoleArchive - linuxrc 줄을 담은 busybox 와, 주어진 커널 버전의 모듈 하나를
// 가진 램디스크를 만든다.
func consoleArchive(vermagic string) *Archive {
	a := &Archive{index: map[string]int{}}
	a.Entries = []Entry{
		{Name: busyboxName, Mode: ModeRegular | 0o755, Data: append([]byte("head\x00"), append(linuxrcToConsole, []byte("\x00/dev/console\x00tail")...)...)},
		{Name: "lib/modules/e1000e.ko", Mode: ModeRegular | 0o644, Data: []byte("x\x00vermagic=" + vermagic + " SMP mod_unload \x00")},
	}
	for i, e := range a.Entries {
		a.index[e.Name] = i
	}
	return a
}

// On a 5.x ramdisk only the linuxrc line changes, and the binary keeps its
// length. The other /dev/console in busybox is left as it was.
//
// 5.x 램디스크에서는 linuxrc 줄만 바뀌고 바이너리 길이는 그대로다. busybox 의
// 다른 /dev/console 은 원래대로 둔다.
func TestFreeLinuxrcFromConsole5(t *testing.T) {
	a := consoleArchive("5.10.55+")
	before := len(a.Entries[0].Data)
	if !a.freeLinuxrcFromConsole() {
		t.Fatal("not changed on a 5.x ramdisk")
	}
	got := a.Entries[0].Data
	if len(got) != before {
		t.Fatalf("busybox length %d, want %d", len(got), before)
	}
	if !bytes.Contains(got, linuxrcToFile) || bytes.Contains(got, linuxrcToConsole) {
		t.Fatalf("linuxrc line not replaced: %q", got)
	}
	if !bytes.Contains(got, []byte("\x00/dev/console\x00")) {
		t.Fatal("the other /dev/console was touched")
	}
}

// A 4.4 ramdisk always has a console, so it is left alone.
// 4.4 램디스크는 콘솔이 항상 있으므로 건드리지 않는다.
func TestFreeLinuxrcFromConsole44(t *testing.T) {
	a := consoleArchive("4.4.302+")
	if a.freeLinuxrcFromConsole() {
		t.Fatal("changed on a 4.4 ramdisk")
	}
	if !bytes.Contains(a.Entries[0].Data, linuxrcToConsole) {
		t.Fatal("busybox was modified")
	}
}
