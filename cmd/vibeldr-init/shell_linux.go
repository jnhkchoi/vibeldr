//go:build linux

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// The rescue console exists because a boot that goes wrong inside DSM's
// ramdisk is otherwise unreadable. DSM starts its own login on ttyS0 and
// ttyS2, synobios holds ttyS1, and none of those will let anyone in without a
// password nobody has.
//
// It is deliberately not a real shell on a real terminal. Handing a serial
// port to /bin/sh means depending on terminal modes, session leadership and a
// controlling tty, and when any of that goes wrong the symptom is a port that
// accepts keystrokes and answers nothing. Reading lines and running them is a
// dozen lines of code with nothing to go wrong, and the kernel's line
// discipline still provides echo and backspace.
//
// 구조용 콘솔이 있는 이유는, DSM 램디스크 안에서 잘못된 부팅은 달리 읽을
// 방법이 없기 때문이다. DSM 은 ttyS0 와 ttyS2 에 자기 로그인을 띄우고,
// synobios 가 ttyS1 을 잡고 있으며, 그 어느 것도 아무도 모르는 비밀번호
// 없이는 들여보내 주지 않는다.
//
// 일부러 진짜 터미널 위의 진짜 셸로 만들지 않는다. 시리얼 포트를 /bin/sh 에
// 넘기면 터미널 모드, 세션 리더, 제어 tty 에 기대야 하고, 그중 하나라도
// 어긋나면 키 입력은 받는데 아무 대답이 없는 포트가 된다. 줄을 읽어 실행하는
// 것은 잘못될 게 없는 열두 줄 남짓한 코드이고, 에코와 백스페이스는 여전히
// 커널의 line discipline 이 해 준다.

// consoleCandidates is where the rescue console will sit, in order of
// preference. ttyS3 is the first serial port nothing in DSM claims; ttyS0 is
// the fallback for a machine that only has one, where it shares the port with
// DSM's own login and the two will split keystrokes between them.
// /dev/console is the last resort.
//
// consoleCandidates - 구조용 콘솔이 자리 잡을 곳, 선호 순. ttyS3 은 DSM 이
// 아무것도 잡지 않는 첫 시리얼 포트다. ttyS0 은 포트가 하나뿐인 기계를 위한
// 대비책으로, DSM 자체 로그인과 포트를 나눠 써서 키 입력이 둘 사이에 갈린다.
// /dev/console 은 마지막 수단이다.
var consoleCandidates = []string{"/dev/ttyS3", consoleTTY, "/dev/console"}

// systemLog is where DSM's installer reports what it is doing.
// systemLog - DSM 인스톨러가 자기가 하는 일을 적는 곳.
const systemLog = "/var/log/messages"

const (
	prompt = "vibeldr# "
	// logPrefix marks lines relayed to the boot console.
	// logPrefix - 부트 콘솔로 옮겨 찍는 줄의 표시.
	logPrefix = "synolog| "
)

// spawnStage2 starts the long-running part (-daemon) in a session of its own
// and returns.
//
// The session matters. A process started with & from a shell script is in a
// background process group, and reading a terminal from one - once the group
// is orphaned, as it is here the moment the boot script exits - fails with
// EIO. Nothing is typed wrong and nothing is misconfigured; the read simply
// refuses, and the port looks dead.
//
// setsid cannot fix that in place, because a backgrounded process is already
// its own process-group leader and setsid refuses to run for one. So the work
// is handed to a child, which is not a leader and can therefore lead a new
// session, and which then becomes the owner of the port by opening it.
//
// spawnStage2 는 장기 실행 파트 (-daemon) 를 자기 세션에서 띄우고 돌아온다.
//
// 세션이 중요하다. 셸 스크립트에서 & 로 띄운 프로세스는 백그라운드 프로세스
// 그룹에 있고, 그 그룹이 고아가 되면 (여기서는 부팅 스크립트가 끝나는 순간)
// 터미널 읽기가 EIO 로 실패한다. 잘못 친 것도 잘못 설정한 것도 없이 읽기가
// 그냥 거부되고, 포트는 죽은 것처럼 보인다.
//
// setsid 로 제자리에서 고칠 수는 없다. 백그라운드 프로세스는 이미 자기
// 프로세스 그룹의 리더이고, setsid 는 리더에게는 거부한다. 그래서 일을 자식
// 프로세스에게 넘긴다. 자식은 리더가 아니라 새 세션을 이끌 수 있고, 포트를
// 열어서 그 주인이 된다.
func spawnStage2() error {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	cmd := exec.Command(exe, "-daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Its standard input must not be a terminal, or that terminal would
	// become the controlling one instead of the port it is meant to serve.
	//
	// 표준 입력이 터미널이면 안 된다. 그러면 그 터미널이, 섬기려는 포트 대신
	// 제어 터미널이 되어 버린다.
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer null.Close()
		cmd.Stdin = null
	}
	// Output stays on the boot console so a failure to open the port is
	// visible in the boot log rather than nowhere.
	//
	// 출력은 부트 콘솔에 그대로 둔다. 포트를 못 연 실패가 어디에도 안 남는
	// 대신 부팅 로그에 보이게 하려는 것이다.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Start()
}

// runDaemon is the long-running part. It never returns.
//
// It always remakes the boot device node, starts the firmware flasher guard
// and announces the machine's IP on the console. Only with vibeldr_shell on
// the command line does it go further: DSM's log is streamed to the boot
// console, and a command prompt is offered on a spare serial port if one can
// be opened.
//
// In that branch the streaming is the part that matters and the part that
// always works. Writing to a terminal from a background process is
// unrestricted; only reading is, which is why the prompt is the optional half.
// The answer to "why did the install fail" goes down the boot log whether or
// not anyone can type.
//
// runDaemon 은 장기 실행 파트다. 돌아오지 않는다.
//
// 항상 부트 장치 노드를 다시 만들고, 펌웨어 플래셔 가드를 띄우고, 콘솔에
// 이 머신의 IP 를 알린다. 커맨드라인에 vibeldr_shell 이 있을 때만 더 나아간다:
// DSM 로그를 부트 콘솔로 흘리고, 남는 시리얼 포트를 열 수 있으면 명령
// 프롬프트를 띄운다.
//
// 그 분기에서 중요한 것, 그리고 늘 되는 것은 로그 흘리기다. 백그라운드
// 프로세스가 터미널에 쓰는 것은 제한이 없고 읽기만 제한된다. 그래서 프롬프트가
// 선택적인 절반이다. "설치가 왜 실패했나" 의 답은 누가 입력할 수 있든 없든
// 부팅 로그로 나간다.
func runDaemon() error {
	// /dev was remounted on the way into this stage, so the boot device node
	// is made again here.
	//
	// 이 단계로 들어오면서 /dev 가 다시 마운트됐으므로 부트 장치 노드를 여기서
	// 다시 만든다.
	if err := makeBootDevice(); err != nil {
		logf("boot device: %v", err)
	}

	// Always on, whether or not anyone is watching: without it the firmware
	// flash at the end of the install can take the machine down and lose the
	// install (see guard.go).
	//
	// 누가 보든 말든 항상 켠다. 이게 없으면 설치 끝의 펌웨어 플래시가 머신을
	// 쓰러뜨리고 설치를 잃을 수 있다 (guard.go 참고).
	go guardFirmwareFlashers(systemLog)

	// Keep announcing the IP on the serial console, for a headless install.
	// Getting an address over DHCP takes a while, and before the DSM web UI is
	// up all the user needs to know is which IP was picked up. One line on the
	// console every 30 seconds; it flows without an interactive shell being
	// opened.
	//
	// 헤드리스 설치를 위해 시리얼 콘솔에 IP 를 계속 알린다. DHCP 로 주소를
	// 받는 데 시간이 걸리고, DSM 웹 UI 가 뜨기 전에 사용자는 "IP 뭐 잡혔는지"
	// 만 알면 된다. 30 초 간격으로 콘솔에 한 줄. 인터랙티브 셸을 안 열어도 흐른다.
	go announceLoop()

	if !hasCmdlineFlag(shellFlag) {
		select {}
	}

	snapshot(os.Stdout)
	go tail(os.Stdout, systemLog, logPrefix)

	tty, err := openConsole()
	if err != nil {
		// No prompt, but the log still flows. Keep running.
		// 프롬프트는 없어도 로그는 계속 흐른다. 계속 돈다.
		logf("rescue console: no interactive port (%v); streaming the log instead", err)
		select {}
	}
	defer tty.Close()

	// The banner below promises the kernel messages are silenced, so silence
	// them. Only in this branch: the caller asked for an interactive console
	// with vibeldr_shell, and without this the prompt is buried. dmesg still
	// has everything.
	//
	// 아래 배너가 커널 메시지를 껐다고 알리므로 실제로 끈다. 이 분기에서만
	// 한다. 사용자가 vibeldr_shell 로 대화형 콘솔을 요청한 경우이고, 이게
	// 없으면 프롬프트가 묻힌다. 내용은 dmesg 에 그대로 남는다.
	quietenConsole()

	fmt.Fprintf(tty, "\r\n\r\n=== vibeldr rescue console ===\r\n")
	fmt.Fprintf(tty, "type a command and press enter; kernel messages are silenced here\r\n\r\n")
	snapshot(tty)
	go tail(tty, systemLog, "")

	fmt.Fprint(tty, prompt)
	in := bufio.NewScanner(tty)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line != "" {
			out, err := exec.Command("/bin/sh", "-c", line).CombinedOutput()
			write(tty, string(out))
			if err != nil {
				fmt.Fprintf(tty, "[%v]\r\n", err)
			}
		}
		fmt.Fprint(tty, prompt)
	}
	// Losing the prompt is not a reason to stop streaming the log.
	// 프롬프트를 잃었다고 로그 흘리기를 멈출 이유는 없다.
	logf("rescue console: prompt ended (%v); still streaming the log", in.Err())
	select {}
}

// snapshot writes the handful of facts that explain most failed installs.
// snapshot - 실패한 설치 대부분을 설명하는 몇 가지 사실을 찍는다.
func snapshot(w io.Writer) {
	for _, cmd := range []string{"df -h", "free -m", "ls -la /tmp"} {
		fmt.Fprintf(w, "--- %s\r\n", cmd)
		out, _ := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
		write(w, string(out))
	}
}

// tail follows a log file, including one that does not exist yet. When prefix
// is given, each line carries it so that the stream can be picked out of a
// boot log afterwards.
//
// tail 은 로그 파일을 따라간다. 아직 없는 파일도 된다. prefix 가 주어지면
// 줄마다 붙여서, 나중에 부팅 로그에서 이 흐름을 골라낼 수 있게 한다.
func tail(w io.Writer, path, prefix string) {
	var offset int64
	for {
		time.Sleep(2 * time.Second)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		size, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			f.Close()
			continue
		}
		// A log that shrank (rotated or truncated) is read again from its
		// start.
		//
		// 줄어든 로그 (교체되거나 잘린 것) 는 처음부터 다시 읽는다.
		if size < offset {
			offset = 0
		}
		if size > offset {
			if _, err := f.Seek(offset, io.SeekStart); err == nil {
				buf := make([]byte, size-offset)
				n, _ := io.ReadFull(f, buf)
				write(w, addPrefix(string(buf[:n]), prefix))
				offset += int64(n)
			}
		}
		f.Close()
	}
}

// addPrefix marks every line, so a grep over the boot log picks the log
// stream out from everything else on the same wire.
//
// addPrefix 는 모든 줄에 표시를 붙인다. 부팅 로그를 grep 하면 같은 선로의
// 다른 모든 것 사이에서 이 로그 흐름만 골라낼 수 있다.
func addPrefix(s, prefix string) string {
	if prefix == "" {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

// write fixes up line endings, because a serial terminal needs a carriage
// return and the programs we run only emit newlines.
//
// write 는 줄 끝을 고친다. 시리얼 터미널은 캐리지 리턴이 필요한데, 우리가
// 돌리는 프로그램은 개행만 내보낸다.
func write(w io.Writer, s string) {
	fmt.Fprint(w, strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n"))
}

// quietenConsole stops kernel messages from reaching the console.
//
// Without this the console is unusable. DSM's drivers poll hardware the
// machine does not have - synobios looks for an i2c bus and a buzzer several
// times a second - and each failure is printed, so anything typed is buried
// before it can be read. The messages still go to the ring buffer, so dmesg
// shows them when they are wanted.
//
// The four values are console, default, minimum and boot-time log levels; 1
// leaves only KERN_EMERG on the console.
//
// quietenConsole 은 커널 메시지가 콘솔에 닿지 않게 한다.
//
// 이게 없으면 콘솔을 쓸 수 없다. DSM 드라이버들은 이 기계에 없는 하드웨어를
// 폴링하고 (synobios 는 i2c 버스와 부저를 초당 여러 번 찾는다) 실패할 때마다
// 찍어서, 입력한 것이 읽기도 전에 묻힌다. 메시지는 링 버퍼에는 그대로 가므로
// 필요할 때 dmesg 로 볼 수 있다.
//
// 네 값은 console, default, minimum, boot-time 로그 레벨이다. 1 이면 콘솔에는
// KERN_EMERG 만 남는다.
func quietenConsole() {
	_ = os.WriteFile("/proc/sys/kernel/printk", []byte("1 4 1 7\n"), 0o644)
}

// openConsole finds somewhere to put the console, honouring an explicit choice
// from the kernel command line.
//
// openConsole 은 콘솔을 둘 곳을 찾는다. 커널 커맨드라인에 명시한 선택이 있으면
// 그것을 따른다.
func openConsole() (*os.File, error) {
	candidates := consoleCandidates
	if chosen := cmdlineValue(shellFlag, ""); chosen != "" {
		candidates = []string{chosen}
	}

	var lastErr error
	for _, path := range candidates {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			logf("rescue console on %s - press enter", path)
			return f, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no usable console among %v: %w", candidates, lastErr)
}

// announceLoop prints this machine's reachable IPs to the serial console every
// 30 seconds, so that during a headless install the user knows which address to
// carry on to the web UI at.
//
// announceLoop - 이 머신의 접근 가능한 IP 들을 30 초마다 시리얼 콘솔에 찍는다.
// 헤드리스로 설치할 때 사용자가 웹 UI 로 이어갈 주소를 알 수 있게 하려는 것이다.
func announceLoop() {
	for {
		if ips := listIPv4(); len(ips) > 0 {
			logf("access DSM at: %s", strings.Join(ips, " "))
		}
		time.Sleep(30 * time.Second)
	}
}

// listIPv4 is this machine's IPv4 addresses with loopback and link-local left
// out. They come from net.InterfaceAddrs, filtered down to what is of use to
// someone working headless.
//
// listIPv4 - 이 머신의 IPv4 주소 중 loopback 과 link-local 을 뺀 것들.
// net.InterfaceAddrs 로 뽑되, 헤드리스 사용자에게 쓸모없는 항목은 걸러낸다.
func listIPv4() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}
