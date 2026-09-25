//go:build linux

// rescue_linux.go is the "rescue mode" inside the boot TUI.
//
// A DSM volume is almost always btrfs. This gathers into one menu what can be
// done when a broken volume stops DSM coming up: scan, mount read-only, check,
// look at the scrub state, roll a snapshot back. The loader already has the
// kernel and the disk drivers, so no separate rescue OS has to be booted.
//
// Actually doing any of it needs the statically linked `btrfs.static` on this
// image. Without that file every item says btrfs-progs is not included and does
// nothing. The path is in the btrfsBinary constant.
//
// The destructive actions (chunk-recover, super-recover, zero-log, set-default)
// only go ahead after a second confirmation that takes exactly the string
// "YES", to stop an accident.
//
// rescue_linux.go - 부트 TUI 안의 "복구 모드".
//
// DSM 볼륨은 거의 항상 btrfs 다. 볼륨이 깨져 DSM 이 안 뜰 때 사용자가
// 할 수 있는 일 - 스캔, 읽기 전용 마운트, check, scrub 상태 확인,
// 스냅샷 롤백 - 을 메뉴로 묶어 둔다. 로더가 이미 커널과 디스크
// 드라이버를 들고 있으므로 별도의 복구 OS 를 띄울 필요가 없다.
//
// 실제 동작에는 이 이미지에 static 링크된 `btrfs.static` 이 필요하다.
// 파일이 없으면 각 항목은 "btrfs-progs 미포함" 을 표시하고 아무 일도
// 하지 않는다. 경로는 btrfsBinary 상수에 있다.
//
// destructive 액션 (chunk-recover, super-recover, zero-log, set-default) 은
// "YES" 문자열을 정확히 입력받는 이중 확인을 통과해야 진행한다. 실수 방지용.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// btrfsBinary is the statically linked btrfs meant to be embedded in the
	// image. Nothing is actually embedded yet; this code only gets ready for it.
	//
	// btrfsBinary - 이미지에 embed 될 static 링크된 btrfs.
	// 아직 실제 embed 는 안 돼 있다. 이 코드는 그 위에서 준비만 해 둔 것이다.
	btrfsBinary = "/usr/local/bin/btrfs.static"
	rescueMount = "/mnt/rescue"

	// rescueBanner is the yellow warning at the top of the screen: VT100 SGR
	// 1 (bold) plus 33 (yellow foreground) plus 0 (reset).
	//
	// rescueBanner - 화면 상단에 붙는 노란색 경고. VT100 SGR 1 (bold)
	// + 33 (yellow foreground) + 0 (reset).
	rescueBanner = "\x1b[1;33m*** 복구 모드 - 원본을 손상시킬 수 있음 ***\x1b[0m"
)

// rescueMode is the "rescue mode" submenu, looping until the user goes back
// with q.
//
// Each action pauses afterwards so there is time to read what it said. The
// shell REPL (7) is the exception, being a loop of its own.
//
// rescueMode - "복구 모드" 서브메뉴. 사용자가 q 로 뒤로 갈 때까지 돈다.
//
// 각 서브 액션은 실행 후 pause() 로 결과를 읽을 시간을 준다. shell REPL
// (7) 만은 자체 루프라 예외다.
func rescueMode() {
	for {
		screenClear()
		fmt.Println(rescueBanner)
		fmt.Println("복구 모드 (Rescue Mode)")
		fmt.Println("-----------------------")
		if !btrfsAvailable() {
			fmt.Println("  [경고] btrfs-progs 미포함 (" + btrfsBinary + " 없음).")
			fmt.Println("         스캔만 Go-native probe 로 제한 동작, 나머지는 no-op.")
		}
		if isMounted(rescueMount) {
			fmt.Println("  [상태] " + rescueMount + " 마운트됨")
		}
		fmt.Println()
		fmt.Println("  1) BTRFS 볼륨 스캔")
		fmt.Println("  2) BTRFS 마운트 (읽기전용)")
		fmt.Println("  3) BTRFS 복구 시도 (check + rescue)")
		fmt.Println("  4) BTRFS scrub 상태")
		fmt.Println("  5) subvolume / snapshot 나열")
		fmt.Println("  6) snapshot 롤백 (subvolume set-default, 매우 위험)")
		fmt.Println("  7) 확장 rescue REPL 진입")
		fmt.Println("  q) 뒤로")
		in := prompt("select: ")
		switch in {
		case "1":
			rescueBtrfsScan()
			pause("")
		case "2":
			rescueBtrfsMount()
			pause("")
		case "3":
			rescueBtrfsRepair()
			pause("")
		case "4":
			rescueBtrfsScrubStatus()
			pause("")
		case "5":
			rescueBtrfsListSubvolumes()
			pause("")
		case "6":
			rescueBtrfsSnapshotRollback()
			pause("")
		case "7":
			rescueLoop()
		case "q", "":
			return
		}
	}
}

// ---------------------------------------------------------------------------
// The BTRFS actions / BTRFS 서브액션들
// ---------------------------------------------------------------------------

// rescueBtrfsScan scans with btrfs.static when the image has it, and falls back
// to the Go-native superblock probe when it does not.
//
// rescueBtrfsScan - 이미지가 btrfs.static 을 갖고 있으면 그걸로 스캔하고,
// 없으면 Go 자체 superblock probe 로 폴백한다.
func rescueBtrfsScan() {
	fmt.Println()
	fmt.Println("[BTRFS 볼륨 스캔]")
	if btrfsAvailable() {
		out, err := runBtrfs(15*time.Second, "filesystem", "show")
		if err != nil {
			fmt.Println("btrfs filesystem show 실패:", err)
		}
		trim := strings.TrimSpace(out)
		if trim == "" {
			fmt.Println("발견 없음")
		} else {
			fmt.Println(trim)
		}
		return
	}
	// The fallback: probe each candidate device's superblock on its own.
	// 폴백: 후보 device 를 각자 superblock probe 한다.
	devs := candidateBTRFSDevs()
	var hits []*BTRFSSuper
	for _, d := range devs {
		s, err := probeBTRFSSuper(d)
		if err != nil || s == nil {
			continue
		}
		hits = append(hits, s)
	}
	if len(hits) == 0 {
		fmt.Printf("발견 없음 (Go-native probe, %d개 디바이스 검사)\n", len(devs))
		return
	}
	for _, s := range hits {
		fmt.Printf("%s  label=%q  generation=%d  uuid=%x\n",
			s.Device, s.Label, s.Generation, s.FSID)
	}
}

// rescueBtrfsMount mounts read-only at rescueMount. The recovery and
// skip_balance combination is the usual way to open a badly damaged filesystem.
//
// It goes through syscall.Mount, so it works without btrfs.static as long as
// the kernel has the btrfs module.
//
// rescueBtrfsMount - 읽기 전용으로 rescueMount 에 붙인다. recovery +
// skip_balance 조합은 심하게 손상된 fs 를 여는 데 흔히 쓰는 옵션이다.
//
// syscall.Mount 로 실행한다. btrfs.static 이 없어도 커널에 btrfs 모듈만
// 있으면 된다.
func rescueBtrfsMount() {
	fmt.Println()
	fmt.Println("[BTRFS 읽기전용 마운트]")
	if isMounted(rescueMount) {
		fmt.Println("이미 " + rescueMount + " 에 마운트되어 있음. umount 후 재시도:")
		if err := syscall.Unmount(rescueMount, 0); err != nil {
			fmt.Println("  umount 실패:", err)
			return
		}
		fmt.Println("  umount 완료.")
	}
	dev := strings.TrimSpace(prompt("device (예: /dev/sda3): "))
	if dev == "" {
		fmt.Println("device 미입력 - 취소.")
		return
	}
	if _, err := os.Stat(dev); err != nil {
		fmt.Println("device 없음:", err)
		return
	}
	if err := os.MkdirAll(rescueMount, 0o755); err != nil {
		fmt.Println("mkdir 실패:", err)
		return
	}
	// ro goes in as MS_RDONLY; the rest of the options go in the data string.
	// ro 는 MS_RDONLY 로 넣는다. 나머지 옵션은 data 문자열로 넣는다.
	opts := "recovery,skip_balance"
	if err := syscall.Mount(dev, rescueMount, "btrfs",
		syscall.MS_RDONLY, opts); err != nil {
		fmt.Printf("mount %s -> %s 실패: %v\n", dev, rescueMount, err)
		fmt.Println("  가능 원인:")
		fmt.Println("   * 커널에 btrfs 모듈이 없음")
		fmt.Println("   * 슈퍼블록 심각 손상 - 3) 복구 시도 필요")
		fmt.Println("   * 마운트 옵션 미지원 (구버전 커널) - 옵션 없이 다시")
		return
	}
	fmt.Printf("마운트됨: %s -> %s (ro,%s)\n", dev, rescueMount, opts)
}

// rescueBtrfsRepair diagnoses first with a read-only check, then runs whichever
// destructive rescue action the user picks. All of them need the "YES"
// confirmation.
//
// rescueBtrfsRepair - readonly check 로 먼저 진단하고, 그 다음 사용자가
// 고른 destructive rescue 액션을 실행한다. 전부 "YES" 이중 확인을 거쳐야
// 한다.
func rescueBtrfsRepair() {
	fmt.Println()
	fmt.Println("[BTRFS 복구 시도]")
	if !btrfsAvailable() {
		fmt.Println("btrfs-progs 미포함 - 이 액션은 사용 불가.")
		return
	}
	dev := strings.TrimSpace(prompt("검사할 device (예: /dev/sda3): "))
	if dev == "" {
		fmt.Println("device 미입력 - 취소.")
		return
	}
	// check --readonly changes nothing on the filesystem, though it can take a
	// long time.
	//
	// check --readonly 는 fs 를 변경하지 않는다. 시간은 오래 걸릴 수 있다.
	fmt.Println("btrfs check --readonly 실행 중... (수 분 걸릴 수 있음)")
	out, err := runBtrfs(10*time.Minute, "check", "--readonly", dev)
	fmt.Println(out)
	if err != nil {
		fmt.Println("check 결과: 오류 감지 -", err)
	} else {
		fmt.Println("check 결과: 오류 없음")
	}

	fmt.Println()
	fmt.Println("추가 액션 (destructive - 원본을 변경):")
	fmt.Println("  a) btrfs rescue super-recover  - 슈퍼블록 복구")
	fmt.Println("  b) btrfs rescue chunk-recover  - chunk tree 재생성")
	fmt.Println("  c) btrfs rescue zero-log       - log tree 초기화")
	fmt.Println("  q) 취소")
	in := strings.ToLower(strings.TrimSpace(prompt("select: ")))
	var args []string
	switch in {
	case "a":
		args = []string{"rescue", "super-recover", "-y", dev}
	case "b":
		args = []string{"rescue", "chunk-recover", "-y", dev}
	case "c":
		args = []string{"rescue", "zero-log", dev}
	default:
		fmt.Println("취소.")
		return
	}
	if !requireYES(fmt.Sprintf(
		"정말 실행? (btrfs %s) [정확히 YES 를 입력해야 진행]: ",
		strings.Join(args, " "))) {
		fmt.Println("취소.")
		return
	}
	out, err = runBtrfs(30*time.Minute, args...)
	fmt.Println(out)
	if err != nil {
		fmt.Println("실행 결과: 오류 -", err)
		return
	}
	fmt.Println("실행 완료.")
}

// rescueBtrfsScrubStatus is the scrub state of the already-mounted rescueMount.
// Starting a scrub is not offered here: it takes a very long time, and DSM runs
// one on its own schedule anyway, so there is little reason to start one at
// boot.
//
// rescueBtrfsScrubStatus - 이미 마운트된 rescueMount 의 scrub 상태.
// scrub 시작은 여기서 제공하지 않는다. 시간이 매우 오래 걸리고, DSM 이
// 원래 스케줄러로 돌리므로 부트 시점에 새로 걸 이유가 적다.
func rescueBtrfsScrubStatus() {
	fmt.Println()
	fmt.Println("[BTRFS scrub 상태]")
	if !btrfsAvailable() {
		fmt.Println("btrfs-progs 미포함 - 사용 불가.")
		return
	}
	if !isMounted(rescueMount) {
		fmt.Println(rescueMount + " 이 마운트되지 않았음. 먼저 2) 로 마운트.")
		return
	}
	out, err := runBtrfs(30*time.Second, "scrub", "status", rescueMount)
	fmt.Print(out)
	if err != nil {
		fmt.Println("scrub status 실패:", err)
	}
}

// rescueBtrfsListSubvolumes lists the subvolumes. Synology puts its Snapshot
// Replication snapshots in as subvolumes here, so a rollback target is chosen
// from this list.
//
// rescueBtrfsListSubvolumes - subvolume 을 나열한다. Synology 는 Snapshot
// Replication 스냅샷을 여기 subvolume 으로 심으므로, 롤백 대상 후보를
// 이 목록에서 골라오게 된다.
func rescueBtrfsListSubvolumes() {
	fmt.Println()
	fmt.Println("[BTRFS subvolume / snapshot 나열]")
	if !btrfsAvailable() {
		fmt.Println("btrfs-progs 미포함 - 사용 불가.")
		return
	}
	if !isMounted(rescueMount) {
		fmt.Println(rescueMount + " 이 마운트되지 않았음. 먼저 2) 로 마운트.")
		return
	}
	fmt.Println("--- snapshots (-s) ---")
	out, err := runBtrfs(30*time.Second, "subvolume", "list", "-a", "-s", rescueMount)
	if err != nil {
		fmt.Println("subvolume list -s 실패:", err)
	} else if strings.TrimSpace(out) == "" {
		fmt.Println("(없음)")
	} else {
		fmt.Print(out)
	}
	fmt.Println("--- all subvolumes ---")
	out2, err := runBtrfs(30*time.Second, "subvolume", "list", "-a", rescueMount)
	if err != nil {
		fmt.Println("subvolume list 실패:", err)
	} else {
		fmt.Print(out2)
	}
}

// rescueBtrfsSnapshotRollback changes the next mount's default subvolume with
// subvolume set-default. It is a rollback in all but name, and very destructive.
//
// The sequence:
//  1. check it is mounted
//  2. show the list again - the user may not have just looked at item 5
//  3. take an ID and check it is a number
//  4. the "YES" confirmation
//  5. try a read-write remount, then run set-default
//
// rescueBtrfsSnapshotRollback - subvolume set-default 로 다음 마운트의
// default subvolume 을 바꾼다. 사실상 롤백이고 매우 destructive 하다.
// 절차는 위 영문 목록과 같다.
func rescueBtrfsSnapshotRollback() {
	fmt.Println()
	fmt.Println("[BTRFS snapshot 롤백 - subvolume set-default]")
	if !btrfsAvailable() {
		fmt.Println("btrfs-progs 미포함 - 사용 불가.")
		return
	}
	if !isMounted(rescueMount) {
		fmt.Println(rescueMount + " 이 마운트되지 않았음. 먼저 2) 로 마운트.")
		return
	}
	fmt.Println("(주의) read-only 마운트라면 rw remount 후 실행 필요.")
	fmt.Println()
	out, err := runBtrfs(30*time.Second, "subvolume", "list", "-a", rescueMount)
	if err != nil {
		fmt.Println("subvolume list 실패:", err)
		return
	}
	fmt.Print(out)
	idStr := strings.TrimSpace(prompt("default 로 설정할 subvolume ID: "))
	if idStr == "" {
		fmt.Println("취소.")
		return
	}
	if !isAllDigits(idStr) {
		fmt.Println("숫자가 아님 - 취소.")
		return
	}
	if !requireYES(fmt.Sprintf(
		"정말 subvolume %s 를 default 로 설정?  이 액션은 자동 복구 안 됨. "+
			"[정확히 YES 를 입력해야 진행]: ", idStr)) {
		fmt.Println("취소.")
		return
	}
	// Try a read-write remount: a no-op if it is already rw, and a way past EROFS
	// if it is ro.
	//
	// rw remount 를 시도한다. 이미 rw 면 no-op 이고, ro 면 EROFS 를 우회한다.
	_ = syscall.Mount("", rescueMount, "btrfs", syscall.MS_REMOUNT, "")
	out, err = runBtrfs(30*time.Second, "subvolume", "set-default", idStr, rescueMount)
	fmt.Print(out)
	if err != nil {
		fmt.Println("실행 실패:", err)
		return
	}
	fmt.Printf("완료. 다음 mount 부터 subvolume %s 가 default.\n", idStr)
}

// ---------------------------------------------------------------------------
// The extra rescue REPL commands, dispatched from rescueLoop
// 확장 rescue REPL 명령들 (rescueLoop 에서 dispatch)
// ---------------------------------------------------------------------------

// cmdBtrfs passes `btrfs <sub-command> [args...]` straight to btrfs.static, for
// typing an arbitrary btrfs command outside the dedicated submenu.
//
// cmdBtrfs - `btrfs <sub-command> [args...]` 를 그대로 btrfs.static 에 넘긴다.
// 전용 서브메뉴 밖에서 사용자가 임의의 btrfs 명령을 직접 치고 싶을 때 쓴다.
func cmdBtrfs(args []string) {
	if !btrfsAvailable() {
		fmt.Println("btrfs-progs 미포함 (" + btrfsBinary + " 없음)")
		return
	}
	if len(args) == 0 {
		fmt.Println("usage: btrfs <sub-command> [args...]")
		fmt.Println("예: btrfs filesystem show")
		fmt.Println("    btrfs subvolume list /mnt/rescue")
		fmt.Println("    btrfs scrub status /mnt/rescue")
		return
	}
	out, err := runBtrfs(5*time.Minute, args...)
	fmt.Print(out)
	if err != nil {
		fmt.Println("btrfs:", err)
	}
}

// cmdMounts dumps /proc/mounts as it is: which filesystem is mounted where and
// with what.
//
// cmdMounts - /proc/mounts 를 그대로 덤프한다. 어느 fs 가 어디에 뭘로 붙었는지.
func cmdMounts() {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		fmt.Println("mount:", err)
		return
	}
	fmt.Print(string(b))
}

// cmdLsblk imitates lsblk by printing /proc/partitions as it is. The real lsblk
// walks sysfs, but coming out of one file is tidier here; cmdParts is the
// separate view that does walk sysfs.
//
// cmdLsblk - lsblk 흉내. /proc/partitions 를 그대로 찍는다. 실제 lsblk 는
// sysfs 를 훑는 게 정석이지만 파일 하나로 나오는 이쪽이 더 간결하다.
// cmdParts 가 sysfs 를 훑는 별도 view 다.
func cmdLsblk() {
	b, err := os.ReadFile("/proc/partitions")
	if err != nil {
		fmt.Println("lsblk:", err)
		return
	}
	fmt.Print(string(b))
}

// cmdNet summarises the interfaces and the routes, reusing interfaces() from
// net_linux.go.
//
// cmdNet - 인터페이스와 route 요약. net_linux.go 의 interfaces() 를 재사용한다.
func cmdNet() {
	// Each interface's mac and carrier, exactly as cmdIP shows them.
	// 인터페이스 각각의 mac / carrier - cmdIP 와 같다.
	cmdIP()
	fmt.Println()
	fmt.Println("routes (/proc/net/route):")
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		fmt.Println("  route:", err)
	} else {
		fmt.Print(string(b))
	}
	// The interface names once more; interfaces() comes from net_linux.go.
	// 인터페이스 이름만 다시 한 번 (interfaces() 는 net_linux.go 것).
	names, err := interfaces()
	if err != nil {
		return
	}
	fmt.Println()
	fmt.Println("interfaces:")
	for _, n := range names {
		fmt.Println("  " + n)
	}
}

// cmdJournal lists the files under /var/log with their sizes, and points at the
// existing `logs <path>` for tailing one of them.
//
// cmdJournal - /var/log 아래 파일 목록 (크기 포함). 각 파일의 tail 은 기존
// `logs <path>` 를 쓰도록 안내한다.
func cmdJournal() {
	entries, err := os.ReadDir("/var/log")
	if err != nil {
		fmt.Println("journal:", err)
		return
	}
	if len(entries) == 0 {
		fmt.Println("(비어있음)")
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		fmt.Printf("  %-40s %10d bytes\n", e.Name()+suffix, info.Size())
	}
	fmt.Println()
	fmt.Println("특정 파일 tail: `logs /var/log/<name>`")
}

// cmdShell spawns the first shell it finds, trying bash, sh and then ash, with
// stdio passed straight through. When the shell exits it comes back to the
// rescue REPL.
//
// cmdShell - bash / sh / ash 순서로 처음 발견되는 shell 을 띄운다. stdio 는
// 그대로 이어 붙인다. shell 이 끝나면 rescue REPL 로 돌아온다.
func cmdShell() {
	for _, path := range []string{"/bin/bash", "/bin/sh", "/bin/ash", "/usr/bin/bash"} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		c := exec.Command(path)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		fmt.Println("[spawning " + path + " - exit 로 복귀]")
		_ = c.Run()
		return
	}
	fmt.Println("shell 없음: /bin/bash, /bin/sh, /bin/ash 어느 것도 없음.")
}

// ---------------------------------------------------------------------------
// Helpers / 유틸
// ---------------------------------------------------------------------------

// btrfsAvailable reports whether btrfs.static is on the image. Every caller
// checks it each time; there is no cache, so swapping the loader image takes
// effect immediately.
//
// btrfsAvailable - btrfs.static 이 이미지에 들어 있는지. 호출부는 매번 이걸
// 확인한다. 로더 이미지를 갈아 끼우면 곧바로 반영되도록 캐시를 두지 않는다.
func btrfsAvailable() bool {
	_, err := os.Stat(btrfsBinary)
	return err == nil
}

// runBtrfs runs btrfs.static with args and returns the combined output. Past
// the timeout the process is killed by the context being cancelled.
//
// runBtrfs - btrfs.static 을 args 로 실행하고 합쳐진 출력을 돌려준다.
// timeout 을 넘기면 context cancel 로 프로세스가 종료된다.
func runBtrfs(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, btrfsBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// requireYES is true only when the user typed exactly "YES". It is case
// sensitive, and Enter alone is false. It is the last gate before a destructive
// action.
//
// requireYES - 사용자가 정확히 "YES" 를 입력한 경우에만 true. 대소문자를
// 구분하고, Enter 만 눌러도 false 다. destructive 액션의 마지막 관문.
func requireYES(msg string) bool {
	return strings.TrimSpace(prompt(msg)) == "YES"
}

// isMounted reports whether target appears as a mount point in /proc/mounts.
// isMounted - /proc/mounts 에 target 이 마운트 대상으로 존재하는지.
func isMounted(target string) bool {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == target {
			return true
		}
	}
	return false
}

// isAllDigits reports whether a string is non-empty and all [0-9].
// isAllDigits - 문자열이 비어 있지 않고 모두 [0-9] 인가.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// candidateBTRFSDevs are the devices worth probing for a btrfs superblock: the
// entries in /sys/class/block starting with sd, nvme, vd, mmcblk or hd, with
// loop, ram, dm and the like left out. Both partitions and whole disks are
// included, because btrfs can sit on a whole disk with no partition at all.
//
// candidateBTRFSDevs - btrfs superblock probe 대상 device 후보.
// /sys/class/block 에서 sd*, nvme*, vd*, mmcblk*, hd* 로 시작하는 것만 본다.
// loop / ram / dm 등은 뺀다. 파티션과 whole-disk 를 모두 포함한다 - btrfs 는
// 파티션 없이 whole-disk 로도 존재할 수 있다.
func candidateBTRFSDevs() []string {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		switch {
		case strings.HasPrefix(n, "sd"),
			strings.HasPrefix(n, "nvme"),
			strings.HasPrefix(n, "vd"),
			strings.HasPrefix(n, "mmcblk"),
			strings.HasPrefix(n, "hd"):
			out = append(out, "/dev/"+n)
		}
	}
	sort.Strings(out)
	return out
}
