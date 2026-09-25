package ramdisk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"vibeldr/internal/lzma"
)

// The hook goes into DSM's boot script, one line above the disk check that
// ends the boot with "DISK NOT INSTALLED":
//
//	ProcDiskList=`/usr/syno/bin/synodiskport -installable_disk_list 2>/dev/null`
//	if [ "0" != "${MaxDisks}" ] && [ "" = "${ProcDiskList}" ]; then
//	        Exit 1 "DISK NOT INSTALLED"
//
// The position was chosen carefully. Any earlier and the storage drivers are
// not loaded yet, so sysfs has nothing to say about ATA ports. Here they are
// all up and nobody has looked at the device tree yet.
//
// 훅은 DSM 부팅 스크립트 안, 부팅을 "DISK NOT INSTALLED" 로 끝내는 디스크
// 검사 (위 조각) 바로 한 줄 위에 삽입한다.
//
// 위치는 신중하게 골랐다. 이보다 위에서는 스토리지 드라이버가 아직 로드
// 전이라 sysfs 가 ATA 포트에 대해 할 말이 없다. 여기서는 다 올라와 있고
// 아직 device tree 를 아무도 안 봤다.

const (
	// InitName is the helper's name in the archive. cpio names carry no
	// leading slash, so unpacked it becomes /vibeldr-init.
	//
	// InitName - 아카이브에서 헬퍼의 이름. cpio 이름엔 앞 슬래시가 없어서,
	// 언팩되면 /vibeldr-init 이 된다.
	InitName = "vibeldr-init"

	// SynoinfoName carries settings meant for the installed system. Editing
	// the ramdisk's own synoinfo.conf is not enough, because DSM lays down its
	// own copy from the .pat, so what is carried here has to be applied again
	// on the other side of the pivot.
	//
	// SynoinfoName - 설치된 시스템용 설정을 실어 나르는 파일. 램디스크의
	// synoinfo.conf 만 고쳐선 부족하다. DSM 은 .pat 에서 자기 사본을 깔기
	// 때문에, 여기 담긴 설정은 pivot 반대편에서 다시 적용해야 한다.
	SynoinfoName = "vibeldr-synoinfo"

	// BuildIDName holds the patch fingerprint, so that matching a boot log
	// against a build does not depend on remembering which image was uploaded.
	//
	// BuildIDName - 패치의 지문을 담는다. 부팅 로그와 특정 빌드를 대조할 때,
	// "어느 이미지를 올렸는지" 를 기억에 의존하지 않고 확인할 수 있게 한다.
	BuildIDName = "vibeldr-build"

	// linuxrcName and rcName are DSM's boot scripts. rcName takes over once
	// DSM has decided an install is needed.
	//
	// linuxrcName / rcName - DSM 의 부팅 스크립트. 인스톨이 필요하다고
	// 판정되면 rcName 이 이어받는다.
	linuxrcName = "linuxrc.syno.impl"
	rcName      = "etc/rc"

	// initPostName is the last script of the ramdisk stage: it mounts the
	// installed system and hands over to it.
	//
	// initPostName - 램디스크 단계의 마지막 스크립트. 설치된 시스템을
	// 마운트하고 넘겨준다.
	initPostName = "usr/sbin/init.post"

	// hookMarker identifies each patch and makes it repeatable.
	// hookMarker - 각 패치를 식별하고 반복 가능하게 만드는 표식.
	hookMarker = "# vibeldr:"
)

// hook is one insertion into one of DSM's scripts.
//
// Always exactly two lines: the marker and the actual command. Keeping to that
// convention means removing an earlier patch is just deleting the marker line
// and the one after it.
//
// hook - DSM 스크립트 중 한 곳에 넣는 한 개의 삽입.
//
// 정확히 두 줄 (marker + 실제 커맨드) 이다. 이전 패치를 제거할 때는 marker
// 줄과 그 다음 한 줄만 지우면 되도록 이 규약을 지킨다.
type hook struct {
	file string
	// anchor: the hook goes immediately above the first line starting with it.
	// anchor - 이 접두어로 시작하는 첫 줄 바로 위에 훅을 삽입한다.
	anchor string
	lines  [2]string
}

var hooks = []hook{
	// The disk mapping has to happen after the storage drivers are loaded and
	// before DSM looks for disks. That window is one line wide: above it sysfs
	// has nothing to say about ATA ports, below it the boot has already given
	// up with "DISK NOT INSTALLED".
	//
	// The anchor matches the assignment, not the command, because the same
	// command also appears earlier inside a shell function.
	//
	// 디스크 매핑은 스토리지 드라이버가 로드된 뒤, DSM 이 디스크를 찾기
	// 전이어야 한다. 그 창은 한 줄 넓이다. 이보다 위면 sysfs 가 ATA 포트에
	// 대해 할 말이 없고, 이보다 아래면 부팅이 "DISK NOT INSTALLED" 로 이미
	// 포기한 뒤다.
	//
	// anchor 는 커맨드가 아니라 대입문에 맞춘다. 같은 커맨드가 앞쪽에서도
	// 셸 함수 안에 등장하기 때문이다.
	{
		file:   linuxrcName,
		anchor: "ProcDiskList=",
		lines: [2]string{
			hookMarker + " point the device tree at this machine's own disk controllers",
			"if [ -x /" + InitName + " ]; then /" + InitName + "; fi",
		},
	},
	// The rescue shell goes somewhere else: at the end of the script that runs
	// once DSM has decided to show its installer. Everything left over from the
	// ramdisk stage is killed at that transition, so a shell started earlier
	// does not survive it - and while it holds the console, /dev stays busy and
	// the transition fails noisily.
	//
	// 구조용 셸은 다른 자리다. DSM 이 인스톨러를 띄우기로 결정한 뒤 도는
	// 스크립트의 끝에서 시작한다. 램디스크 단계에 남은 것들은 이 전환에서
	// 전부 죽으므로 앞에서 띄운 셸은 살아남지 못한다. 게다가 콘솔을 잡고
	// 있으면 /dev 가 busy 로 남아 이 전환이 시끄럽게 실패한다.
	{
		file:   rcName,
		anchor: `echo "============ Date ============"`,
		// Backgrounded: the console outlives the boot, and the boot must not
		// wait for the console. Written as an and-list rather than an if
		// block, because "cmd & fi" does not parse in some small shells and
		// that parse error goes by unnoticed.
		//
		// 백그라운드로 띄운다. 콘솔은 부팅보다 오래 살고, 부팅이 콘솔을
		// 기다리면 안 된다. if 블록이 아니라 and-list 로 쓰는 이유는
		// "cmd & fi" 가 작은 셸에서 파싱이 안 되는 경우가 있고, 그 파싱
		// 에러가 조용히 지나가기 때문이다.
		lines: [2]string{
			hookMarker + " guard the install, and serve a rescue console if asked",
			"[ -x /" + InitName + " ] && /" + InitName + " -stage2 &",
		},
	},
	// Without this line a freshly installed DSM never boots. The last thing the
	// ramdisk stage does is unmount /dev and pivot into the installed system,
	// and the unmount fails:
	//
	//	umount: can't unmount /dev: Device or resource busy
	//	switch_root: error moving root
	//
	// The machine then goes back to the installer, and the web assistant
	// politely asks whether to install over the system that is already on the
	// disk. Forever.
	//
	// What is holding /dev is synobios, DSM's own hardware monitoring module,
	// which the boot script always loads. It opens /dev/ttyS1 to talk to a
	// power-management microcontroller that exists on a real appliance, and
	// waits for an answer that never comes on a machine without one.
	//
	// Stopping it from opening the port looks like the fix and is not. Remove
	// the device node and the open fails, /dev unmounts cleanly - and the
	// module keeps hold of a file pointer it never got, hands it to the first
	// program that asks for a sensor reading, and the kernel dies:
	//
	//	BUG: sleeping function called from invalid context
	//	pid: 5347, name: syno_microp_con
	//
	// So the module is left to behave exactly as it does on real hardware, and
	// the unmount is what changes. A lazy detach takes the name out of the tree
	// immediately while any process still holding a file keeps its own copy.
	// synobios goes on waiting harmlessly and the pivot succeeds.
	//
	// 이 줄이 없으면 갓 설치된 DSM 은 절대 부팅되지 않는다. 램디스크 단계
	// 마지막 동작은 /dev 언마운트 + 설치된 시스템으로 pivot 인데, 언마운트가
	// 실패한다:
	//
	//	umount: can't unmount /dev: Device or resource busy
	//	switch_root: error moving root
	//
	// 그럼 머신이 인스톨러로 되돌아가고, 웹 어시스턴트가 이미 디스크에
	// 설치돼 있는 시스템을 다시 설치하겠냐고 정중하게 묻는다. 무한 반복이다.
	//
	// /dev 를 붙잡고 있는 건 DSM 자체 하드웨어 모니터링 모듈인 synobios 다.
	// 부팅 스크립트가 무조건 로드한다. /dev/ttyS1 을 열어 실기에서 그 포트에
	// 있는 전원관리 마이크로컨트롤러와 통신하려 하고, 그 컨트롤러가 없는
	// 기계에서는 끝내 오지 않을 응답을 기다린다.
	//
	// 포트를 못 열게 막는 게 얼핏 해법 같지만 틀린 해법이다. device node 를
	// 지우면 open 은 실패하고 /dev 는 깨끗이 언마운트되지만, 모듈은
	// 못 얻은 파일 포인터를 계속 들고 있다가, 다음에 센서 값을 요청하는
	// 첫 프로그램에게 그걸 넘겨줘 커널이 죽는다:
	//
	//	BUG: sleeping function called from invalid context
	//	pid: 5347, name: syno_microp_con
	//
	// 그래서 모듈은 실기에서 하듯 그대로 동작하게 두고, 언마운트만 바꾼다.
	// lazy detach 는 이름을 즉시 트리에서 빼내고, 아직 파일을 잡고 있는
	// 프로세스는 자기 몫을 계속 쓰게 한다. synobios 는 무해하게 계속
	// 기다리고, pivot 은 성공한다.
	{
		file: initPostName,
		// Above the script's own unmounts, not below them. Below, /proc is
		// already gone and the mount table with it - the helper cannot find
		// out what is in the way, because the only file that would have told
		// it has just been unmounted.
		//
		// 스크립트 자신의 언마운트보다 위, 아래가 아니다. 아래에서는 /proc
		// 이 이미 사라졌고 마운트 표도 함께 없다. 무엇이 막고 있는지 알려줄
		// 유일한 파일이 방금 언마운트됐으니 헬퍼가 알아낼 방법이 없다.
		anchor: "Umount /proc",
		// The helper does it, not a line of shell. Both can call the same
		// syscall; only one of them reports what happened on a path that has
		// been seen to reach the console.
		//
		// 셸 한 줄이 아니라 헬퍼가 한다. 같은 시스콜을 부를 수 있는 건
		// 마찬가지지만, 무슨 일이 있었는지를 콘솔까지 닿는 경로로 알려주는
		// 쪽은 하나뿐이다.
		lines: [2]string{
			hookMarker + " detach /dev so a module still holding it cannot block the pivot",
			"[ -x /" + InitName + " ] && /" + InitName + " -detach",
		},
	},
	// The helper does the pivot instead of busybox switch_root.
	//
	// It logs the errno the kernel returned, where busybox only says "error
	// moving root". On failure it execs the original switch_root, so a pivot
	// that fails still fails in the same place and the same way as it would
	// without the hook. The leading [ -x ... ] guard makes the hook a no-op on
	// a ramdisk with no helper: the original line below simply runs.
	//
	// busybox switch_root 대신 헬퍼가 pivot 을 수행한다.
	//
	// 커널이 돌려준 에러 번호를 로그로 남긴다 (busybox 는 "error moving root"
	// 만 뱉는다). 실패하면 원본 switch_root 를 exec 해서, 실패하는 pivot 은 훅이
	// 없을 때와 같은 자리에서 같은 방식으로 실패한다. 앞의 [ -x ... ] 가드 덕분에
	// 헬퍼가 없는 램디스크에서는 이 훅이 무효이고 아래 원본 라인이 그대로 돈다.
	{
		file:   initPostName,
		anchor: "exec /sbin/switch_root",
		lines: [2]string{
			hookMarker + " move the root ourselves, so a refusal comes with a reason",
			"[ -x /" + InitName + " ] && exec /" + InitName + " -pivot",
		},
	},
}

// Hooked is where one hook actually landed: which file, and which line.
// Hooked - 훅 하나가 실제로 어디 (파일 + 라인번호) 에 삽입됐는지.
type Hooked struct {
	File string
	Line int
}

// Report summarises what the patch did, for the build log.
// Report - 패치가 무엇을 했는지의 요약 (빌드 로그용).
type Report struct {
	// Entries is the number of files in the ramdisk.
	// Entries - 램디스크 안의 파일 수.
	Entries int
	// Hooks is where each hook landed, in order.
	// Hooks - 각 훅이 삽입된 자리, 순서대로.
	Hooks []Hooked
	// InitSize is the size of the helper that was added.
	// InitSize - 추가된 헬퍼의 크기.
	InitSize int
	// BuildID is this patch's fingerprint, printed to the console every boot.
	// BuildID - 이 패치의 고유 지문. 매 부팅마다 콘솔에 찍힌다.
	BuildID string
	// Decompressed and Repacked are the byte counts before and after.
	// Decompressed / Repacked - 처리 전/후 바이트 수.
	Decompressed int
	Repacked     int
	// LinuxrcLog is where the boot script's output was sent instead of
	// /dev/console, or "" if busybox was left alone - see console.go.
	//
	// LinuxrcLog - 부팅 스크립트 출력을 /dev/console 대신 보낸 곳. busybox 를
	// 건드리지 않았으면 "" (console.go 참고).
	LinuxrcLog string
}

func (r Report) String() string {
	var where []string
	for _, h := range r.Hooks {
		where = append(where, fmt.Sprintf("%s:%d", h.File, h.Line))
	}
	s := fmt.Sprintf("%d files, hooks at %s, %s is %d bytes, %d -> %d bytes uncompressed",
		r.Entries, strings.Join(where, " and "), InitName, r.InitSize, r.Decompressed, r.Repacked)
	if r.LinuxrcLog != "" {
		s += ", linuxrc output to " + r.LinuxrcLog
	}
	return s
}

// Patch turns Synology's rd.gz into a ramdisk that fits itself to the machine
// it booted on.
//
// The result is a plain uncompressed cpio archive. The kernel accepts that as
// it is, so there is no reason to LZMA-compress something the kernel is about
// to decompress again. It costs a little more space on the loader partition,
// and the ramdisk path needs no compressor at all.
//
// Patch - 시놀로지 rd.gz 를, 부팅한 머신에 스스로를 맞추는 램디스크로 변환.
//
// 결과는 압축 없는 평범한 cpio 아카이브다. 커널은 이걸 그대로 받아들이므로,
// 커널이 곧바로 되풀 것을 굳이 LZMA 로 압축할 이유가 없다. 로더 파티션의
// 디스크 공간을 조금 더 쓰는 대신, 램디스크 쪽에는 압축기가 아예 필요 없다.
func Patch(rdgz, initBinary []byte, extra ...File) ([]byte, Report, error) {
	cpio, err := lzma.Decode(rdgz)
	if err != nil {
		return nil, Report{}, fmt.Errorf("decompressing the ramdisk: %w", err)
	}
	return PatchCPIO(cpio, initBinary, extra...)
}

// PatchCPIO is Patch on an already decompressed archive.
// PatchCPIO - 이미 압축이 풀린 아카이브에 대한 Patch.
func PatchCPIO(cpio, initBinary []byte, extra ...File) ([]byte, Report, error) {
	rep := Report{Decompressed: len(cpio)}

	a, err := ReadCPIO(cpio)
	if err != nil {
		return nil, rep, fmt.Errorf("reading the ramdisk: %w", err)
	}
	rep.Entries = len(a.Entries)

	if err := a.AddOrReplace(InitName, 0o755, initBinary); err != nil {
		return nil, rep, err
	}
	rep.InitSize = len(initBinary)

	for _, f := range extra {
		if err := a.AddOrReplace(f.Name, f.Mode, f.Data); err != nil {
			return nil, rep, err
		}
	}

	// A short fingerprint of this patch, written where the helper can read it
	// and print it on every boot. Without it, "is this the image I just made?"
	// can only be answered from memory, and a fix that never reached the image
	// looks exactly like a fix that did not work.
	//
	// 이 패치의 짧은 지문. 헬퍼가 읽어서 매 부팅마다 찍는다. 이게 없으면
	// "지금 올린 이미지가 맞나" 를 기억에 의존해 답해야 하고, 이미지에 안
	// 실린 수정과 안 먹는 수정이 똑같아 보인다.
	rep.BuildID = buildID(initBinary, extra)
	if err := a.AddOrReplace(BuildIDName, 0o644, []byte(rep.BuildID+"\n")); err != nil {
		return nil, rep, err
	}

	for _, h := range hooks {
		if err := a.applyHook(h); err != nil {
			return nil, rep, err
		}
	}
	// Where each hook sits is read back from the finished archive rather than
	// remembered from the moment of insertion. With two hooks in one file, the
	// second landing above the first moves the first one's line number, and a
	// build log with a wrong line number is worse than one with none.
	//
	// 각 훅이 어디 앉았는지는 삽입 순간을 기억하지 말고 완성된 아카이브에서
	// 다시 읽는다. 같은 파일에 훅이 둘이면 두 번째가 위에 앉을 때 첫 번째의
	// 라인 번호가 이동하므로, 라인 번호를 잘못 적는 빌드 로그가 아예 안
	// 적는 것보다 나쁘다.
	for _, h := range hooks {
		rep.Hooks = append(rep.Hooks, Hooked{File: h.file, Line: a.lineOf(h.file, h.lines[0])})
	}
	// Without a console on a 5.x kernel, busybox would never start the boot
	// script at all - see console.go.
	//
	// 5.x 커널에 콘솔이 없으면 busybox 가 부팅 스크립트를 아예 시작하지 않는다
	// (console.go 참고).
	if a.freeLinuxrcFromConsole() {
		rep.LinuxrcLog = LinuxrcLog
	}
	// A sanity check on the insertions: the finished archive must contain
	// exactly as many of our markers as expected. If an anchor string has
	// disappeared in a DSM refactor, or a hook went in twice, this fails here
	// rather than becoming a silent failure later.
	//
	// 훅 삽입 sanity check. 완성된 아카이브 안에 우리 marker 개수가 정확히
	// 예상한 것과 맞는지 확인한다. anchor 문자열이 DSM 리팩터로 사라졌거나
	// 훅이 중복 삽입되면 여기서 실패시켜 silent-fail 로 이어지지 않게 한다.
	expected := map[string]int{}
	for _, h := range hooks {
		expected[h.file]++
	}
	for file, want := range expected {
		got := a.countHookMarkers(file)
		if got != want {
			return nil, rep, fmt.Errorf("ramdisk 훅 검증 실패: %s 에 marker %d 개 있어야 하는데 %d 개 있음", file, want, got)
		}
	}
	for _, h := range rep.Hooks {
		if h.Line == 0 {
			return nil, rep, fmt.Errorf("ramdisk 훅 검증 실패: %s 에 삽입한 훅의 marker 라인을 다시 찾을 수 없음", h.File)
		}
	}

	out, err := a.Bytes()
	if err != nil {
		return nil, rep, err
	}
	rep.Repacked = len(out)
	return out, rep, nil
}

// countHookMarkers counts the lines in a file starting with our hookMarker
// prefix, which is how the insertions are verified against what was expected.
//
// countHookMarkers - 파일 안에 hookMarker 접두사로 시작하는 줄이 몇 개인지.
// 훅 삽입이 정확히 예상된 만큼 이루어졌는지 검증하는 데 쓴다.
func (a *Archive) countHookMarkers(file string) int {
	e, ok := a.Get(file)
	if !ok {
		return 0
	}
	var n int
	for _, l := range strings.Split(string(e.Data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), hookMarker) {
			n++
		}
	}
	return n
}

// lineOf is the 1-based line the marker sits on, or 0 when it is not there.
// lineOf - marker 가 놓인 1-based 라인. 없으면 0.
func (a *Archive) lineOf(file, marker string) int {
	e, ok := a.Get(file)
	if !ok {
		return 0
	}
	for i, l := range strings.Split(string(e.Data), "\n") {
		if strings.TrimSpace(l) == marker {
			return i + 1
		}
	}
	return 0
}

// applyHook inserts one hook.
// applyHook - 훅 하나를 삽입한다.
func (a *Archive) applyHook(h hook) error {
	return a.Patch(h.file, func(script string) (string, error) {
		lines := strings.Split(script, "\n")

		// Re-patching a ramdisk that has already been patched must not stack
		// two hooks, so an existing one is removed first.
		//
		// 이미 패치된 램디스크를 다시 패치할 때 훅이 두 겹으로 쌓이면 안
		// 되므로, 기존 것을 먼저 제거한다.
		lines = withoutHook(lines, h.lines[0])

		for i, l := range lines {
			if !strings.HasPrefix(strings.TrimSpace(l), h.anchor) {
				continue
			}
			out := make([]string, 0, len(lines)+len(h.lines))
			out = append(out, lines[:i]...)
			out = append(out, h.lines[:]...)
			out = append(out, lines[i:]...)
			return strings.Join(out, "\n"), nil
		}
		return "", fmt.Errorf("%s has no line starting with %q, so this DSM version is laid out differently", h.file, h.anchor)
	})
}

// withoutHook drops one marker line and the command under it.
//
// It matches the whole marker, not just the prefix every hook shares. Two hooks
// can live in the same file - init.post gets one above its /proc unmount and
// another above the switch_root exec - and a prefix match would make applying
// the second one quietly delete the first.
//
// withoutHook - marker 줄 하나와 그 아래 커맨드를 지운다.
//
// 모든 훅이 공유하는 접두사가 아니라 marker 전체로 매칭한다. 한 파일에 훅이
// 둘 있을 수 있고 (init.post 는 /proc 언마운트 위와 switch_root exec 위에
// 하나씩 받는다), 접두사로 매칭하면 두 번째를 적용할 때 첫 번째가 조용히 지워진다.
func withoutHook(lines []string, marker string) []string {
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == marker {
			i++ // also skip the command that follows the marker / marker 다음 커맨드도 건너뛴다
			continue
		}
		out = append(out, lines[i])
	}
	return out
}

// AddOrReplace writes a file whether or not it is already there, which is what
// re-running a build needs.
//
// AddOrReplace - 파일이 이미 있든 없든 쓴다. 빌드를 다시 돌릴 때 필요한
// 동작이다.
func (a *Archive) AddOrReplace(name string, mode uint32, data []byte) error {
	if _, ok := a.Get(name); ok {
		return a.Replace(name, data)
	}
	return a.Add(name, mode, data)
}

// buildID fingerprints a patch: the helper that goes in, and every line this
// version of vibeldr inserts into DSM's scripts.
//
// buildID - 패치의 지문을 만든다. 들어가는 헬퍼와, 이 버전의 vibeldr 가
// DSM 스크립트에 삽입하는 모든 줄이 재료다.
func buildID(initBinary []byte, extra []File) string {
	h := sha256.New()
	h.Write(initBinary)
	for _, k := range hooks {
		fmt.Fprintf(h, "\n%s|%s|%s|%s", k.file, k.anchor, k.lines[0], k.lines[1])
	}
	for _, f := range extra {
		fmt.Fprintf(h, "\n%s|%o|", f.Name, f.Mode)
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// File is something else the loader wants carried inside the ramdisk.
//
// It goes in through the same door as the helper rather than being added
// afterwards, so that the build fingerprint covers it. A setting that changes
// without the fingerprint changing is exactly the trap this whole mechanism
// exists to close.
//
// File - 로더가 램디스크 안에 함께 실어 보내려는 그 밖의 것.
//
// 나중에 덧붙이지 않고 헬퍼와 같은 문으로 들어간다. 그래야 빌드 지문이
// 이것까지 덮는다. 지문은 그대로인데 설정만 바뀌는 상황이야말로 이 장치가
// 막으려는 함정이다.
type File struct {
	Name string
	Mode uint32
	Data []byte
}
