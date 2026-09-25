package ramdisk

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/lzma"
)

func buildArchive(t *testing.T, entries []Entry) []byte {
	t.Helper()
	a := &Archive{index: map[string]int{}}
	for _, e := range entries {
		if e.Mode == 0 {
			e.Mode = ModeRegular | 0o644
		}
		a.index[e.Name] = len(a.Entries)
		a.Entries = append(a.Entries, e)
	}
	b, err := a.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return b
}

func TestCPIORoundTrip(t *testing.T) {
	want := []Entry{
		{Name: "etc", Mode: ModeDirectory | 0o755},
		{Name: "etc/model.dtb", Data: bytes.Repeat([]byte{0xab}, 300)},
		{Name: "linuxrc.syno.impl", Mode: ModeRegular | 0o755, Data: []byte("#!/bin/sh\n")},
		{Name: "empty", Data: nil},
	}
	a, err := ReadCPIO(buildArchive(t, want))
	if err != nil {
		t.Fatalf("ReadCPIO: %v", err)
	}
	if len(a.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(a.Entries), len(want))
	}
	for i, e := range a.Entries {
		if e.Name != want[i].Name {
			t.Errorf("entry %d is %q, want %q", i, e.Name, want[i].Name)
		}
		if !bytes.Equal(e.Data, want[i].Data) {
			t.Errorf("%s: contents differ", e.Name)
		}
	}
	// Order matters: a file cannot be unpacked before its directory.
	// 순서가 중요하다. 파일은 자기 디렉터리보다 먼저 풀 수 없다.
	if a.Entries[0].Name != "etc" {
		t.Error("archive order was not preserved")
	}
	if !a.Entries[0].IsDir() || !a.Entries[1].IsRegular() {
		t.Error("file types were not preserved")
	}
}

// The kernel's initramfs unpacker does not create parent directories and
// quietly drops a file it cannot open. So adding a nested path has to emit a
// directory entry for each step before the file itself.
//
// 커널 initramfs 언패커는 상위 디렉터리를 만들어 주지 않고, 못 여는 파일은
// 조용히 버린다. 그래서 중첩 경로를 Add 하면 각 단계가 디렉터리 엔트리로
// 파일보다 먼저 나와야 한다.
func TestAddCreatesParentDirs(t *testing.T) {
	a := NewArchive()
	if err := a.Add("init", ModeRegular|0o755, []byte("x")); err != nil {
		t.Fatalf("Add init: %v", err)
	}
	if err := a.Add("lib/modules/6.6/kernel/virtio.ko", ModeRegular|0o644, []byte("y")); err != nil {
		t.Fatalf("Add module: %v", err)
	}
	// Adding into the same tree once more must not duplicate the directories.
	// 같은 트리에 한 번 더 넣어도 디렉터리가 중복되지 않아야 한다.
	if err := a.Add("lib/modules/6.6/modules.dep", ModeRegular|0o644, []byte("z")); err != nil {
		t.Fatalf("Add modules.dep: %v", err)
	}

	var got []string
	for _, e := range a.Entries {
		got = append(got, e.Name)
	}
	want := []string{
		"init",
		"lib", "lib/modules", "lib/modules/6.6", "lib/modules/6.6/kernel",
		"lib/modules/6.6/kernel/virtio.ko",
		"lib/modules/6.6/modules.dep",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries:\ngot:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, e := range a.Entries {
		if strings.HasSuffix(e.Name, ".ko") || strings.Contains(e.Name, ".dep") || e.Name == "init" {
			continue
		}
		if !e.IsDir() {
			t.Errorf("%s should be a directory entry", e.Name)
		}
		if e.Nlink < 2 {
			t.Errorf("%s has nlink %d, directories need at least 2", e.Name, e.Nlink)
		}
	}
}

func TestReadCPIORejectsGarbage(t *testing.T) {
	if _, err := ReadCPIO([]byte("not a cpio archive at all")); err == nil {
		t.Error("expected an error for input that is not an archive")
	}
}

func TestReadCPIORequiresTrailer(t *testing.T) {
	b := buildArchive(t, []Entry{{Name: "a", Data: []byte("x")}})
	// Cut the trailer record off the end.
	// 끝의 트레일러 레코드를 잘라 낸다.
	if _, err := ReadCPIO(b[:110]); err == nil {
		t.Error("expected an error for an archive with no trailer")
	}
}

const fakeLinuxrc = `#!/bin/sh
echo "START /linuxrc.syno.impl"

AddDeviceBackToSwapRaid()
{
	for dev in $(/usr/syno/bin/synodiskport -installable_disk_list); do
		echo "$dev"
	done
}

# insert synobios
SYNOLoadModules synobios
/bin/mknod /dev/synobios c 201 0

#
# check if disk is installed
#
ProcDiskList=` + "`" + `/usr/syno/bin/synodiskport -installable_disk_list 2>/dev/null` + "`" + `

if [ "0" != "${MaxDisks}" ] && [ "" = "${ProcDiskList}" ]; then
	Exit 1 "DISK NOT INSTALLED"
fi
`

const fakeRC = `#!/bin/sh
RCMsg "Starting /etc/rc"
mount -t devtmpfs none /dev

StartServices

echo "============ Date ============"
date
echo "=============================="

exit 0
`

const fakeInitPost = `#!/bin/sh
/bin/echo "Post init"

Mount -t proc proc /proc
Mount -t devtmpfs none /dev

Mount "$(GetRootMountOpt)" "$(GetRootMountPath)" /tmpRoot

Umount /proc >/dev/null 2>&1
Umount /dev
exec /sbin/switch_root -c /dev/console /tmpRoot /sbin/init
`

func fakeRamdisk(t *testing.T) []byte {
	t.Helper()
	return buildArchive(t, []Entry{
		{Name: "etc", Mode: ModeDirectory | 0o755},
		{Name: "etc/model.dtb", Data: []byte("tree")},
		{Name: "etc/rc", Mode: ModeRegular | 0o755, Data: []byte(fakeRC)},
		{Name: "linuxrc.syno.impl", Mode: ModeRegular | 0o755, Data: []byte(fakeLinuxrc)},
		{Name: "usr/sbin/init.post", Mode: ModeRegular | 0o755, Data: []byte(fakeInitPost)},
	})
}

// synobios keeps /dev/ttyS1 open waiting for a microcontroller that only the
// real appliance has, so the plain unmount in init.post fails and the pivot
// into the installed system never happens. The detach has to land before
// switch_root runs; after it there is no "after".
//
// synobios 는 실제 기기에만 있는 마이크로컨트롤러를 기다리며 /dev/ttyS1 을
// 열어 둔다. 그래서 init.post 의 평범한 언마운트가 실패하고 설치된 시스템으로
// pivot 하지 못한다. detach 는 switch_root 가 돌기 전에 들어가야 한다. 그 뒤에는
// "뒤" 가 없다.
func TestPatchDetachesDevBeforeThePivot(t *testing.T) {
	out, rep, err := PatchCPIO(fakeRamdisk(t), []byte("ELF"))
	if err != nil {
		t.Fatalf("PatchCPIO: %v", err)
	}
	a, err := ReadCPIO(out)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := a.Get(initPostName)
	if !ok {
		t.Fatal("usr/sbin/init.post is missing")
	}
	body := string(e.Data)
	lazy := strings.Index(body, "/"+InitName+" -detach")
	pivot := strings.Index(body, "exec /sbin/switch_root")
	if lazy < 0 {
		t.Fatal("init.post does not detach /dev")
	}
	if pivot < 0 || lazy > pivot {
		t.Fatalf("the detach is at %d, after the pivot at %d", lazy, pivot)
	}
	var found bool
	for _, h := range rep.Hooks {
		if h.File == initPostName {
			found = true
		}
	}
	if !found {
		t.Errorf("the report does not mention %s: %+v", initPostName, rep.Hooks)
	}
}

// hooksIn counts how many hooks target one file.
// hooksIn 은 한 파일을 겨냥하는 훅이 몇 개인지 센다.
func hooksIn(file string) int {
	var n int
	for _, h := range hooks {
		if h.file == file {
			n++
		}
	}
	return n
}

// hookFor finds a hook by what it anchors to, so that adding a hook elsewhere
// in the list does not break every test that looks at one.
//
// hookFor 는 훅을 anchor 로 찾는다. 그래서 목록의 다른 자리에 훅을 더해도
// 훅 하나를 보는 테스트들이 전부 깨지지 않는다.
func hookFor(t *testing.T, rep Report, anchor string) Hooked {
	t.Helper()
	for i, h := range hooks {
		if h.anchor != anchor {
			continue
		}
		if i >= len(rep.Hooks) {
			break
		}
		return rep.Hooks[i]
	}
	t.Fatalf("no hook anchored at %q in %+v", anchor, rep.Hooks)
	return Hooked{}
}

func linuxrcOf(t *testing.T, cpio []byte) string {
	t.Helper()
	a, err := ReadCPIO(cpio)
	if err != nil {
		t.Fatalf("ReadCPIO: %v", err)
	}
	e, ok := a.Get(linuxrcName)
	if !ok {
		t.Fatal("linuxrc is missing from the patched archive")
	}
	return string(e.Data)
}

func TestPatchInsertsHookAndHelper(t *testing.T) {
	init := []byte("\x7fELF pretend binary")
	out, rep, err := PatchCPIO(fakeRamdisk(t), init)
	if err != nil {
		t.Fatalf("PatchCPIO: %v", err)
	}

	a, err := ReadCPIO(out)
	if err != nil {
		t.Fatalf("ReadCPIO: %v", err)
	}
	e, ok := a.Get(InitName)
	if !ok {
		t.Fatal("the helper was not added")
	}
	if !bytes.Equal(e.Data, init) {
		t.Error("the helper's contents changed")
	}
	if e.Mode&0o777 != 0o755 {
		t.Errorf("helper mode is %o, want 755; it has to be executable", e.Mode&0o777)
	}

	if len(rep.Hooks) != len(hooks) {
		t.Fatalf("got %d hooks, want %d", len(rep.Hooks), len(hooks))
	}

	at := hookFor(t, rep, "ProcDiskList=").Line
	script := linuxrcOf(t, out)
	lines := strings.Split(script, "\n")
	if at < 1 || at+1 >= len(lines) {
		t.Fatalf("hook line %d is out of range", at)
	}
	// The hook must sit immediately above the disk check, not anywhere else:
	// earlier in the script the storage drivers are not loaded yet.
	//
	// 훅은 다른 곳이 아니라 디스크 검사 바로 위에 있어야 한다. 스크립트의 더
	// 앞쪽에서는 스토리지 드라이버가 아직 로드되지 않았다.
	if !strings.HasPrefix(strings.TrimSpace(lines[at-1]), hookMarker) {
		t.Fatalf("line %d is %q, expected the hook", at, lines[at-1])
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[at+1]), "ProcDiskList=") {
		t.Fatalf("line after the hook is %q, expected the disk check", lines[at+1])
	}
}

// The rescue shell has to be started from /etc/rc, not from the ramdisk boot
// script: everything from that stage is killed when DSM moves on to showing
// its installer, so a shell started earlier is gone by the time it is needed.
//
// 구조용 셸은 램디스크 부팅 스크립트가 아니라 /etc/rc 에서 시작해야 한다. DSM
// 이 인스톨러를 띄우는 단계로 넘어갈 때 그 단계의 것들은 전부 죽으므로, 앞에서
// 띄운 셸은 필요할 때쯤 이미 없다.
func TestPatchHooksTheInstallerStageToo(t *testing.T) {
	out, rep, err := PatchCPIO(fakeRamdisk(t), []byte("\x7fELF"))
	if err != nil {
		t.Fatalf("PatchCPIO: %v", err)
	}
	a, err := ReadCPIO(out)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := a.Get(rcName)
	if !ok {
		t.Fatal("etc/rc is missing")
	}
	body := string(rc.Data)
	if !strings.Contains(body, "/"+InitName+" -stage2") {
		t.Fatal("etc/rc does not start the rescue shell")
	}
	// And it must land before the script exits, or it never runs.
	// 그리고 스크립트가 끝나기 전에 들어가야 한다. 아니면 절대 돌지 않는다.
	if strings.Index(body, InitName) > strings.LastIndex(body, "exit 0") {
		t.Fatal("the hook is after the script exits")
	}
	if h := hookFor(t, rep, `echo "============ Date ============"`); h.File != rcName {
		t.Fatalf("hooks = %+v", rep.Hooks)
	}
}

// The call inside the shell function uses the same command, and patching above
// it would run the scan before the drivers exist.
//
// 셸 함수 안의 호출도 같은 커맨드를 쓴다. 그 위에 패치하면 드라이버가 생기기
// 전에 스캔이 돈다.
func TestPatchIgnoresTheCallInsideAFunction(t *testing.T) {
	out, rep, err := PatchCPIO(fakeRamdisk(t), []byte("\x7fELF"))
	if err != nil {
		t.Fatalf("PatchCPIO: %v", err)
	}
	before := strings.Split(linuxrcOf(t, out), "\n")[:rep.Hooks[0].Line-1]
	if strings.Contains(strings.Join(before, "\n"), "AddDeviceBackToSwapRaid()") == false {
		t.Error("the hook landed above the helper function, which is too early")
	}
}

func TestPatchIsRepeatable(t *testing.T) {
	first, _, err := PatchCPIO(fakeRamdisk(t), []byte("\x7fELF one"))
	if err != nil {
		t.Fatalf("first patch: %v", err)
	}
	second, _, err := PatchCPIO(first, []byte("\x7fELF one"))
	if err != nil {
		t.Fatalf("second patch: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("patching an already patched ramdisk changed it")
	}
	if n, want := strings.Count(linuxrcOf(t, second), hookMarker), hooksIn(linuxrcName); n != want {
		t.Fatalf("found %d hooks in the boot script, want %d", n, want)
	}
}

func TestPatchWithoutAnchor(t *testing.T) {
	cpio := buildArchive(t, []Entry{
		{Name: linuxrcName, Data: []byte("#!/bin/sh\necho hello\n")},
	})
	_, _, err := PatchCPIO(cpio, []byte("\x7fELF"))
	if err == nil {
		t.Fatal("expected an error when the disk check cannot be found")
	}
	if !strings.Contains(err.Error(), hooks[0].anchor) {
		t.Errorf("error should name what it looked for, got: %v", err)
	}
}

// A ramdisk whose installer script is laid out differently must fail loudly at
// build time rather than produce an image with no way in.
//
// 인스톨러 스크립트 배치가 다른 램디스크는, 들어갈 길이 없는 이미지를 만들지
// 말고 빌드 시점에 요란하게 실패해야 한다.
func TestPatchWithoutRC(t *testing.T) {
	cpio := buildArchive(t, []Entry{
		{Name: linuxrcName, Data: []byte(fakeLinuxrc)},
		{Name: rcName, Data: []byte("#!/bin/sh\nexit 0\n")},
	})
	_, _, err := PatchCPIO(cpio, []byte("\x7fELF"))
	if err == nil {
		t.Fatal("expected an error when etc/rc has no anchor")
	}
	if !strings.Contains(err.Error(), rcName) {
		t.Errorf("error should name the file, got: %v", err)
	}
}

func TestPatchWithoutLinuxrc(t *testing.T) {
	cpio := buildArchive(t, []Entry{{Name: "etc", Mode: ModeDirectory | 0o755}})
	if _, _, err := PatchCPIO(cpio, []byte("\x7fELF")); err == nil {
		t.Fatal("expected an error when the boot script is missing")
	}
}

// TestPatchRealRamdisk runs the whole thing against Synology's own rd.gz.
// TestPatchRealRamdisk 는 시놀로지 원본 rd.gz 로 전체 과정을 돌린다.
func TestPatchRealRamdisk(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "work", "dsm-real", "rd.gz"))
	if err != nil {
		t.Skip("work/dsm-real/rd.gz is not present")
	}
	init := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{0}, 1024)...)

	out, rep, err := Patch(raw, init)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if rep.Entries < 600 {
		t.Errorf("only %d entries; the real ramdisk has around 689", rep.Entries)
	}
	t.Logf("%s", rep)

	a, err := ReadCPIO(out)
	if err != nil {
		t.Fatalf("ReadCPIO: %v", err)
	}
	// The device tree has to survive untouched: the helper rewrites it at
	// boot, from the model's own copy.
	//
	// device tree 는 손대지 않은 채 남아야 한다. 헬퍼가 부팅 때 모델 자신의
	// 사본으로 다시 쓴다.
	dtb, ok := a.Get("etc/model.dtb")
	if !ok || len(dtb.Data) != 16446 {
		t.Fatalf("etc/model.dtb is %d bytes (present=%v), want 16446", len(dtb.Data), ok)
	}
	if _, ok := a.Get(InitName); !ok {
		t.Error("the helper is missing")
	}

	script := linuxrcOf(t, out)
	if n, want := strings.Count(script, hookMarker), hooksIn(linuxrcName); n != want {
		t.Errorf("found %d hooks in the boot script, want %d", n, want)
	}
	// Everything the original script did must still be there.
	// 원본 스크립트가 하던 것은 전부 그대로 남아 있어야 한다.
	original, err := lzma.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := ReadCPIO(original)
	orig, _ := before.Get(linuxrcName)
	if len(script) <= len(orig.Data) {
		t.Error("the patched script is not longer than the original")
	}
	for _, line := range strings.Split(string(orig.Data), "\n") {
		if !strings.Contains(script, line) {
			t.Fatalf("a line of the original script was lost: %q", line)
		}
	}
}
