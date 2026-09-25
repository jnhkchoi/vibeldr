package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The last thing the DSM installer does is flash the motherboard firmware. On a
// VM that kills the machine outright:
//
//	./H2OFFT-Lx64  - Insyde's BIOS flasher. Running it under KVM takes the
//	                 virtual CPU with it: no panic, no shutdown message, the
//	                 VM simply resets and the install is lost one step from
//	                 the end.
//
// It is blocked from user space alone, with no kernel module: the program on
// disk is replaced with a script that does nothing and succeeds.
//
// The timing is awkward. The installer verifies the checksums of the files it
// unpacked, and the flasher is among them:
//
//	2901234190 1339786 H2OFFT-Lx64 2277213 0
//
// Replacing it too early fails that checksum and the install stops with a
// corruption error. So the replacement happens after the installer has written
// "checksum passed" to its own log. From that line to the flasher running is
// about five seconds.
//
// DSM 인스톨러의 마지막 동작은 메인보드 펌웨어 플래시다. VM 에서는 그게
// 머신을 통째로 죽인다:
//
//	./H2OFFT-Lx64  - Insyde 의 BIOS 플래셔. KVM 에서 돌리면 가상 CPU 가
//	                 같이 죽는다. 패닉도 종료 메시지도 없이 VM 이 리셋되고,
//	                 끝 한 걸음 앞에서 설치를 잃는다.
//
// 커널 모듈 없이 유저스페이스만으로 차단한다. 그 프로그램을 디스크에서 아무
// 일도 안 하고 성공하는 스크립트로 교체한다.
//
// 시점이 까다롭다. 인스톨러는 자기가 푼 파일들의 체크섬을 검증하고, 플래셔도
// 그 안에 있다:
//
//	2901234190 1339786 H2OFFT-Lx64 2277213 0
//
// 너무 일찍 교체하면 체크섬 실패로 인스톨이 손상 에러로 멈춘다. 그래서
// 교체는 인스톨러가 자기 로그에 "체크섬 통과" 를 남긴 뒤에 이루어진다. 그 줄부터 플래셔 실행까지 약 5 초다.

// flashers are the programs that must never run, as filepath.Glob patterns.
// H2OFFT carries a version suffix that differs per DSM and per model - DS918+
// 7.4.1 has H2OFFT-Lx64-0815. Blocking by exact name would miss the real file
// under a different suffix, the BIOS flash would run (updater.c:1927
// "H2OFFT... failed! 166" -> updater.c:6927 errno=21) and the whole install
// would roll back. So they are caught with globs.
//
// flashers - 절대 실행되면 안 되는 프로그램들. filepath.Glob 패턴이다.
// H2OFFT 는 DSM/모델마다 버전 접미사가 붙는다 (예: DS918+ 7.4.1 은
// H2OFFT-Lx64-0815). 정확한 이름으로 막으면 접미사가 다른 실물을 놓쳐
// BIOS 플래시가 실행되고 (updater.c:1927 "H2OFFT... failed! 166" ->
// updater.c:6927 errno=21) 설치가 통째로 롤백된다. 그래서 글롭으로 잡는다.
var flashers = []string{
	"/tmpData/upd@te/H2OFFT*",
	"/tmpData/upd@te/sas_fw_upgrade_tool",
	"/tmpData/upd@te/uboot_do_upd.sh",
	"/usr/syno/bin/syno_pstore_collect",
	// The out-of-band management firmware upgrade. It assumes a real BMC, and
	// on a machine without one the upgrade stops right there, so it is blocked.
	//
	// 대역외 관리 펌웨어 업그레이드. 실기의 BMC 를 전제로 하고, 없는
	// 기계에서 돌면 업그레이드가 거기서 멈추므로 실행을 막는다.
	"/usr/syno/sbin/syno_oob_fw_upgrade",
}

// checksumPassed is the log string the installer leaves once it has verified
// the files it unpacked. After that line it is safe to touch them.
//
// checksumPassed - 인스톨러가 언팩한 파일들을 검증했다고 남기는 로그 문자열.
// 이 이후는 손대도 안전하다.
const checksumPassed = "Pass checksum of"

// stub does nothing and succeeds. To the installer it looks like a successful run.
// stub - 아무 일도 안 하고 성공한다. 인스톨러 눈에는 성공한 실행으로 보인다.
const stub = "#!/bin/sh\n# replaced by vibeldr: this program flashes firmware and hangs a VM\nexit 0\n"

const (
	guardPoll  = 100 * time.Millisecond
	guardBurst = 60 * time.Second
)

// guardFirmwareFlashers watches the installer's log and disarms the flashers
// every time a set of files passes its checksum.
//
// guardFirmwareFlashers - 인스톨러 로그를 지켜보다가 파일 집합이 체크섬을
// 통과할 때마다 플래셔들을 무력화한다.
func guardFirmwareFlashers(logPath string) {
	follow(logPath, func(line string) {
		if !strings.Contains(line, checksumPassed) {
			return
		}
		logf("guard: installer checksum passed; disarming firmware flashers")
		// A short burst rather than one attempt: the files are written over
		// several seconds, and a retry by the installer unpacks them again.
		//
		// 한 번이 아니라 짧게 반복한다 (burst). 파일들이 몇 초에 걸쳐 쓰이고,
		// 인스톨러가 재시도할 때 다시 언팩되기도 하기 때문이다.
		deadline := time.Now().Add(guardBurst)
		for time.Now().Before(deadline) {
			for _, pat := range flashers {
				// Find the real files with a glob, to cover the version suffix.
				// 글롭으로 실물 파일들을 찾는다 (버전 접미사 대응).
				matches, _ := filepath.Glob(pat)
				for _, p := range matches {
					if disarm(p) {
						logf("guard: disarmed %s", p)
					}
				}
			}
			time.Sleep(guardPoll)
		}
	})
}

// disarm replaces one program with the stub, returning whether a replacement
// was needed.
//
// The replacement is a rename, not a write. Linux refuses a write to a file
// currently being executed - the install log fills with "text file busy" - and
// the flasher survives untouched. A rename moving the name onto a new file has
// no such restriction: only what the name points at changes, and a process
// already running keeps using the copy it holds until it exits. What matters is
// that the next exec meets the stub.
//
// disarm - 프로그램 하나를 stub 으로 교체한다. 교체가 필요했는지 여부를
// 돌려준다.
//
// 교체는 write 가 아니라 rename 이다. 리눅스는 현재 실행 중인 파일에 쓰는 걸
// 거부하고 (install 로그에 "text file busy" 가 도배된다), 플래셔는 그대로
// 살아남는다. 반면 이름을 새 파일로 옮기는 rename 은 그런 제한이 없다.
// 이름이 가리키는 실체가 바뀔 뿐이고, 이미 실행 중인 프로세스는 자기가
// 잡고 있는 사본을 계속 쓰다가 종료된다. 중요한 건 다음 exec 이 stub 을
// 만난다는 것이다.
func disarm(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil || string(body) == stub {
		return false
	}
	// The temporary file has to be in the same directory: a rename cannot
	// cross filesystems and /tmpData is its own mount.
	//
	// 임시 파일은 같은 디렉터리에 있어야 한다. rename 은 파일시스템을
	// 못 넘고 /tmpData 는 자기 마운트다.
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".vibeldr")
	if err := os.WriteFile(tmp, []byte(stub), 0o755); err != nil {
		reportOnce(path, err)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		reportOnce(path, err)
		return false
	}
	return true
}

// reported and reportOnce log a failure the first time only. The guard retries
// ten times a second for a minute, so the same error every time would stack 600
// identical lines on the console and bury what the install is actually doing.
//
// reported / reportOnce - 실패를 처음 볼 때만 로그한다. 가드는 초당 10 번, 1 분간
// 재시도하므로, 매번 같은 오류가 나면 콘솔에 똑같은 줄이 600 개 쌓여서
// 인스톨의 진짜 진행 상황이 묻힌다.
var reported = map[string]bool{}

func reportOnce(path string, err error) {
	if reported[path] {
		return
	}
	reported[path] = true
	logf("guard: %s: %v", path, err)
}

// follow calls fn for each line appended to a file. A file that does not exist
// yet is followed from the moment it appears.
//
// follow - 파일에 append 되는 줄마다 fn 을 호출한다. 아직 존재하지 않는
// 파일도 나중에 생기면 그때부터 따라간다.
func follow(path string, fn func(line string)) {
	var offset int64
	var partial string
	for {
		time.Sleep(guardPoll)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		size, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			f.Close()
			continue
		}
		// A rotated or truncated log starts over.
		// 교체되거나 잘린 로그는 처음부터 다시 읽는다.
		if size < offset {
			offset, partial = 0, ""
		}
		if size > offset {
			if _, err := f.Seek(offset, io.SeekStart); err == nil {
				buf := make([]byte, size-offset)
				n, _ := io.ReadFull(f, buf)
				offset += int64(n)
				partial += string(buf[:n])
				lines := strings.Split(partial, "\n")
				// The last piece may be an unfinished line.
				// 마지막 조각은 아직 끝나지 않은 줄일 수 있다.
				partial = lines[len(lines)-1]
				for _, l := range lines[:len(lines)-1] {
					fn(l)
				}
			}
		}
		f.Close()
	}
}
