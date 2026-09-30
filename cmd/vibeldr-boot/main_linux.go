//go:build linux

// vibeldr-boot is the loader's own boot environment: the program that comes up
// when "configure and install" or "reconfigure" is chosen in GRUB instead of
// DSM.
//
// The kernel is a generic Alpine LTS kernel on partition 1 (vmlinuz). The
// ramdisk (initrd-vibeldr) holds this binary as /init, next to the kernel
// modules, a CA bundle and the Synology tools it runs (scemd, xz). Linux runs
// /init as PID 1 and asks for nothing else, so this one static Go program is
// the whole operating environment.
//
// Everything a distribution would have done has to be done here: mount /proc,
// /sys and /dev, bring the network up, and never exit - PID 1 exiting is a
// kernel panic.
//
// vibeldr-boot - 로더 자체의 부팅 환경. GRUB 에서 DSM 대신 "configure and
// install" 이나 "reconfigure" 를 선택했을 때 뜨는 프로그램.
//
// 커널은 파티션 1 의 범용 Alpine LTS 커널 (vmlinuz) 이다. 램디스크
// (initrd-vibeldr) 에는 이 바이너리가 /init 으로 들어 있고, 옆에 커널 모듈, CA
// 번들, 이 프로그램이 돌리는 시놀로지 도구 (scemd, xz) 가 함께 있다. 리눅스는
// /init 을 PID 1 로 실행하고 다른 건 요구하지 않으므로, static Go 프로그램 하나가
// 운영 환경 전체다.
//
// 배포판이 대신 해주던 것들을 스스로 해야 한다: /proc, /sys, /dev 마운트,
// 네트워크 up, 그리고 절대 종료하지 않기 - PID 1 이 종료하면 커널 패닉.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ipWaitTimeout is how long to wait for a DHCP answer. At the start of the TUI
// there may be no card or no cable, so it is kept short and the boot carries on
// quietly either way.
//
// ipWaitTimeout - DHCP 응답을 기다릴 시간. TUI 초입에서 카드가 없거나 케이블이
// 안 꽂혀있을 수 있으므로 짧게 잡고 조용히 계속 진행한다.
const ipWaitTimeout = 8 * time.Second

// main brings the environment up step by step and hands over to the TUI.
// main - 환경을 단계별로 세우고 TUI 에 넘긴다.
func main() {
	// Go's runtime answers SIGINT and SIGTERM by exiting, and process 1
	// exiting is a kernel panic. Registered here, both go to a channel nobody
	// reads instead, so nothing acts on them.
	//
	// Go 런타임은 SIGINT 와 SIGTERM 을 받으면 종료하고, 프로세스 1 이 종료하면
	// 커널 패닉이다. 여기서 등록하면 둘 다 아무도 읽지 않는 채널로 가므로 아무 일도
	// 일어나지 않는다.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGTERM)

	say("vibeldr-boot: starting as pid %d", os.Getpid())

	for _, m := range []struct{ source, target, fstype string }{
		{"proc", "/proc", "proc"},
		{"sysfs", "/sys", "sysfs"},
		{"devtmpfs", "/dev", "devtmpfs"},
	} {
		if err := os.MkdirAll(m.target, 0o755); err != nil {
			say("vibeldr-boot: %s: %v", m.target, err)
			continue
		}
		if err := syscall.Mount(m.source, m.target, m.fstype, 0, ""); err != nil {
			say("vibeldr-boot: mount %s: %v", m.target, err)
		}
	}

	mountWorkFS()

	say("vibeldr-boot: %s", kernelVersion())
	say("vibeldr-boot: block devices: %s", blockDevices())

	// Load drivers, bring the network up, run the TUI. A failed step does not
	// stop the next one. The TUI falls back to the rescue prompt even with no
	// loader partition and no catalog.
	//
	// 드라이버 로드 -> 네트워크 up -> TUI. 각 단계는 실패해도 다음 단계로
	// 간다. TUI 는 로더 파티션이나 catalog 가 안 붙어도 rescue 프롬프트로
	// 폴백한다.
	if n, err := loadDrivers(); err != nil {
		say("vibeldr-boot: driver load: %v", err)
	} else {
		say("vibeldr-boot: loaded %d driver(s)", n)
	}
	if lease, err := bringUpNetwork(ipWaitTimeout); err != nil {
		say("vibeldr-boot: network: %v (continuing offline)", err)
	} else {
		say("vibeldr-boot: %s", lease)
		// Before any HTTPS download: a clock years behind fails every
		// certificate (clock_linux.go).
		//
		// HTTPS 로 받기 전에: 몇 년 늦은 시계는 모든 인증서를 실패시킨다
		// (clock_linux.go).
		if msg, err := syncClock(); err != nil {
			say("vibeldr-boot: clock: %v (keeping %s)", err, time.Now().UTC().Format(time.RFC3339))
		} else {
			say("vibeldr-boot: %s", msg)
		}
	}

	// The reboot syscall is made from inside the TUI. If it returns, park
	// here: process 1 returning from main is a kernel panic.
	//
	// reboot syscall 은 TUI 안에서 불린다. TUI 가 반환하면 여기서 멈춰 세운다.
	// 프로세스 1 이 main 에서 반환하면 커널 패닉이다.
	runTUI()
	select {}
}

// textMenuArg on the kernel command line skips the graphical wizard for the
// text menu, for a screen the wizard cannot draw on. The boot menu has an
// entry that sets it (internal/image/grub_bootstrap.go, grub_rewrite_linux.go).
//
// textMenuArg - 커널 커맨드라인에 있으면 그래픽 마법사를 건너뛰고 글자 메뉴로
// 간다. 마법사가 그리지 못하는 화면을 위한 것이다. 부팅 메뉴에 이것을 넣는
// 항목이 있다 (internal/image/grub_bootstrap.go, grub_rewrite_linux.go).
const textMenuArg = "vibeldr_text"

// bootArg reports whether the kernel command line has name as a word of its
// own.
//
// bootArg - 커널 커맨드라인에 name 이 한 단어로 있는지.
func bootArg(name string) bool {
	raw, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(raw)) {
		if f == name {
			return true
		}
	}
	return false
}

// consoleQuiet says whether to stop writing to the console directly.
//
// It is on while the graphical installer screen is up. The console uses the
// same framebuffer, so one line printed from here lands on top of the screen
// being drawn.
//
// consoleQuiet - 콘솔에 직접 쓰는 것을 멈출지.
//
// 그래픽 설치 화면이 떠 있는 동안에는 켜 둔다. 같은 프레임버퍼를 콘솔도
// 쓰고 있어서, 여기서 한 줄 찍으면 그리던 화면 위에 글자가 얹힌다.
var consoleQuiet atomic.Bool

// setConsoleQuiet stops direct console output, or brings it back.
// setConsoleQuiet - 콘솔 직접 출력을 멈추거나 되살린다.
func setConsoleQuiet(v bool) { consoleQuiet.Store(v) }

// say writes to the console directly as well as to stdout, because process 1
// starts with no controlling terminal and stdout may go nowhere.
//
// say - 표준 출력만이 아니라 콘솔에도 직접 쓴다. 프로세스 1 은 제어 터미널
// 없이 시작하므로 표준 출력이 아무 데도 안 갈 수 있다.
func say(format string, args ...any) {
	line := fmt.Sprintf("vibeldr-boot: "+format+"\n", args...)
	fmt.Print(line)
	if consoleQuiet.Load() {
		return
	}
	if f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
}

// kernelVersion is the running kernel's name and release, for the boot log.
// kernelVersion - 지금 돌고 있는 커널의 이름과 릴리스. 부팅 로그용.
func kernelVersion() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "uname failed"
	}
	return charsToString(u.Sysname[:]) + " " + charsToString(u.Release[:])
}

// charsToString turns a NUL-terminated C char array into a Go string.
// charsToString - NUL 로 끝나는 C 문자 배열을 Go 문자열로.
func charsToString(c []int8) string {
	b := make([]byte, 0, len(c))
	for _, v := range c {
		if v == 0 {
			break
		}
		b = append(b, byte(v))
	}
	return string(b)
}

// blockDevices lists what is in /sys/block, for the boot log. It is the first
// clue about whether the disk drivers came up.
//
// blockDevices - /sys/block 에 있는 것을 나열한다. 부팅 로그용이고, 디스크
// 드라이버가 올라왔는지 보는 첫 단서다.
func blockDevices() string {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return "unreadable: " + err.Error()
	}
	var out string
	for _, e := range entries {
		out += e.Name() + " "
	}
	if out == "" {
		return "(none)"
	}
	return out
}
