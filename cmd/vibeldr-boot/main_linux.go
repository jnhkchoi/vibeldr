//go:build linux

// vibeldr-boot is the loader's own boot environment: the program that comes up
// when "configure and build" is chosen in GRUB instead of DSM.
//
// The kernel is Synology's, unchanged and already on the loader. The ramdisk is
// a single file - this binary - unpacked as /init. Linux runs /init as PID 1
// and asks for nothing else, so one static Go program is a complete operating
// environment.
//
// Everything a distribution would have done has to be done here: mount /proc,
// /sys and /dev, bring the network up, and never exit - PID 1 exiting is a
// kernel panic.
//
// vibeldr-boot - 로더 자체의 부팅 환경. GRUB 에서 DSM 대신 "configure
// and build" 를 선택했을 때 뜨는 프로그램.
//
// 커널은 시놀로지 것 그대로 (로더에 이미 있다). 램디스크는 파일 하나 -
// 이 바이너리 - 가 /init 으로 풀린다. 리눅스는 /init 을 PID 1 로 실행하고
// 다른 건 요구하지 않으므로, static Go 프로그램 하나면 완결된 운영 환경이다.
//
// 배포판이 대신 해주던 것들을 스스로 해야 한다: /proc, /sys, /dev 마운트,
// 네트워크 up, 그리고 절대 종료하지 않기 - PID 1 이 종료하면 커널 패닉.
package main

import (
	"fmt"
	"os"
	"os/signal"
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
	// Process 1 receives no signal it has not asked for and is killed by
	// nothing, so an ordinary crash would leave the machine hung with no
	// message. Catching the interrupt keys at least makes a wedged screen
	// recoverable without a power cycle.
	//
	// 프로세스 1 은 자기가 요청하지 않은 시그널을 받지 않고 무엇으로도
	// 죽지 않으므로, 평범한 크래시 하나가 아무 메시지 없이 기계를 멈춘
	// 상태로 남긴다. 인터럽트 키만이라도 받아 두면 화면이 물렸을 때
	// 전원을 끊지 않고 되돌릴 수 있다.
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
	}

	// The TUI returning normally would be a kernel panic; the reboot syscall
	// is made from inside it. Defensively: if it does return, park here.
	//
	// TUI 가 정상 반환하는 순간이 곧 커널 패닉이다. reboot syscall 은 그
	// 안에서 불린다. 방어적으로, 만약 반환하면 여기서 멈춰 세운다.
	runTUI()
	select {}
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
