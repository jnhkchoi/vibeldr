//go:build linux

package main

// logfile_linux.go keeps a copy of the boot log on the loader's own disk.
//
// Everything logf prints goes to /dev/console, and on a Synology kernel the only
// console that really exists is a serial port: the kernel is built with
// CONFIG_VGA_CONSOLE and CONFIG_FRAMEBUFFER_CONSOLE off and CONFIG_DUMMY_CONSOLE
// on, so tty0 swallows whatever is written to it. On a machine with no serial
// port the whole boot log therefore disappears, and a boot that fails there
// leaves nothing to look at - not on the screen, not anywhere.
//
// It is written straight onto the block device, with no filesystem. A FAT
// partition next to the kernel would be tidier, but Synology's kernel refuses
// every vfat mount during the ramdisk stage: EINVAL, no message in the log,
// while the same image mounts perfectly on an ordinary Linux machine. The
// driver pack is on a raw partition for that same reason.
//
// The gap between the MBR and the first partition is used. Partition 1 starts
// at LBA 2048 and GRUB's core image sits right after the MBR, well under a
// hundred kilobytes, so the second half of that gap is free. It is picked over
// the tail of the pack partition because the whole-disk node exists as soon as
// the loader's disk has been named, which is far earlier in the boot than the
// point where the pack is read - and a boot that stops early is exactly the one
// worth a log.
//
// logfile_linux.go - 부팅 로그 사본을 로더 자신의 디스크에 남긴다.
//
// logf 가 찍는 것은 전부 /dev/console 로 가는데, 시놀로지 커널에서 실제로
// 존재하는 콘솔은 시리얼뿐이다. CONFIG_VGA_CONSOLE 과
// CONFIG_FRAMEBUFFER_CONSOLE 이 꺼져 있고 CONFIG_DUMMY_CONSOLE 이 켜져 있어서
// tty0 에 쓴 것은 그대로 삼켜진다. 그래서 시리얼 포트가 없는 기계에서는 부팅
// 로그가 통째로 사라지고, 거기서 부팅이 실패하면 볼 것이 아무것도 안 남는다.
//
// 파일시스템 없이 블록 장치에 직접 쓴다. 커널 옆 FAT 파티션에 두면 깔끔하지만,
// 시놀로지 커널은 램디스크 단계에서 모든 vfat 마운트를 거부한다. EINVAL 이고
// 로그에 메시지도 없다. 같은 이미지가 일반 리눅스에서는 멀쩡히 마운트된다.
// 드라이버 팩이 raw 파티션에 있는 것도 같은 이유다.
//
// MBR 과 첫 파티션 사이의 빈 공간을 쓴다. 파티션 1 은 LBA 2048 에서
// 시작하고 GRUB core 이미지는 MBR 바로 뒤 100KB 도 안 되는 자리에 있으므로,
// 그 간격의 뒷절반은 비어 있다. 팩 파티션 끝 대신 여기를 고른 이유는,
// 디스크 전체 노드가 로더 디스크에 이름이 붙는 순간부터 존재하기 때문이다.
// 팩을 읽는 시점보다 훨씬 이르고, 일찍 멈추는 부팅이야말로 로그가 필요하다.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/synoboot"
)

// loaderDisk is the whole-disk node makeBootDevice creates for the loader.
// loaderDisk - makeBootDevice 가 로더에 만들어 주는 디스크 전체 노드.
var loaderDisk = hwscan.DefaultDev + "/" + synoboot.Name

const (
	// logAreaLBA is the sector the log starts at, and logAreaBytes how much of
	// the gap is set aside. Partition 1 starts at LBA 2048.
	//
	// logAreaLBA - 로그가 시작하는 섹터. logAreaBytes - 떼어 두는 크기.
	// 파티션 1 은 LBA 2048 에서 시작한다.
	logAreaLBA   = 1024
	logAreaBytes = 512 * 1024
	// logMagic marks the start so that a reader can find it without knowing
	// the exact offset, with `strings` or a search for this word.
	//
	// logMagic - 시작 표식. 읽는 쪽이 정확한 오프셋을 몰라도
	// `strings` 나 이 단어 검색으로 찾을 수 있게 한다.
	logMagic = "=== VIBELDR BOOT LOG ==="
	// logKeptMax caps what is held in memory. This runs before anything has
	// judged how much memory the machine has, and a boot looping on an error
	// could otherwise print until it fills it. The oldest lines go first:
	// the failure is at the end.
	//
	// logKeptMax - 메모리에 들고 있는 양의 상한. 이 단계는 이 기계의 메모리가
	// 얼마인지 아무도 판단하기 전이고, 오류로 루프에 빠진 부팅이라면 메모리를
	// 채울 때까지 찍을 수 있다. 오래된 줄부터 버린다. 실패는 끝에 있다.
	logKeptMax = 2000
)

var (
	logMu   sync.Mutex
	logKept []string
)

// keepLine stores one already-formatted line.
// keepLine - 이미 형식이 갖춰진 줄 하나를 보관한다.
func keepLine(s string) {
	logMu.Lock()
	defer logMu.Unlock()
	logKept = append(logKept, s)
	if len(logKept) > logKeptMax {
		logKept = logKept[len(logKept)-logKeptMax:]
	}
}

// logDevice opens the loader's own disk for the log write.
//
// /dev/synoboot is tried first: makeBootDevice puts that name on the loader's
// disk and it is the same device. When it is not there the disk is found in
// sysfs by the label on its first partition - the same lookup makeBootDevice
// does - and opened under its kernel name. The fallback is the point of this:
// a boot that stops before makeBootDevice succeeds is exactly the one worth a
// log, and depending on its node would lose that boot.
//
// logDevice - 로그를 쓸 로더 자신의 디스크를 연다.
//
// /dev/synoboot 을 먼저 본다. makeBootDevice 가 로더 디스크에 붙여 주는
// 이름이고 같은 장치다. 그게 없으면 첫 파티션의 라벨로 sysfs 에서 디스크를
// 찾아(makeBootDevice 와 같은 조회) 커널 이름으로 연다. 이 대비책이 핵심이다.
// makeBootDevice 가 성공하기 전에 멈추는 부팅이야말로 로그가 필요한 부팅인데,
// 그 노드에만 기대면 바로 그 부팅을 놓친다.
func logDevice() (*os.File, error) {
	f, err := os.OpenFile(loaderDisk, os.O_RDWR, 0)
	if err == nil {
		return f, nil
	}
	first := err
	name, err := synoboot.FindDisk(synoboot.DefaultSysBlock, hwscan.DefaultDev, loaderLabel())
	if err != nil {
		return nil, fmt.Errorf("%v; %v", first, err)
	}
	return os.OpenFile(filepath.Join(hwscan.DefaultDev, name), os.O_RDWR, 0)
}

// flushLog writes what has been gathered into the gap behind the MBR.
//
// A failure here never stops the boot, but it is printed. Staying silent about
// it means a machine with no serial port leaves neither a console log nor a
// disk log, with no way to tell which of the two broke - and that is the very
// situation this file exists for. The print goes straight out rather than
// through logf, which would feed the line back into the buffer being written.
//
// flushLog - 여태 모은 것을 MBR 뒤 빈 공간에 쓴다.
//
// 여기서 실패해도 부팅을 멈추지 않지만, 실패는 찍는다. 조용히 넘어가면
// 시리얼 포트가 없는 기계에서 콘솔 로그도 디스크 로그도 없이 끝나고, 둘 중
// 무엇이 깨졌는지조차 알 수 없다. 이 파일이 있는 이유가 바로 그 상황이다.
// 찍는 것은 logf 가 아니라 바로 내보낸다. logf 로 찍으면 지금 쓰고 있는
// 버퍼에 그 줄이 다시 들어간다.
func flushLog(tag string) {
	logMu.Lock()
	lines := make([]string, len(logKept))
	copy(lines, logKept)
	logMu.Unlock()
	if len(lines) == 0 {
		return
	}

	f, err := logDevice()
	if err != nil {
		fmt.Printf("vibeldr: log(%s): no disk to write to: %v\n", tag, err)
		return
	}
	defer f.Close()

	if _, err := f.Seek(logAreaLBA*512, 0); err != nil {
		fmt.Printf("vibeldr: log(%s): seek: %v\n", tag, err)
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s  %s\n", logMagic, tag, time.Now().UTC().Format(time.RFC3339))
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	out := []byte(b.String())
	if len(out) > logAreaBytes {
		out = out[len(out)-logAreaBytes:]
	}
	if _, err := f.Write(out); err != nil {
		fmt.Printf("vibeldr: log(%s): write: %v\n", tag, err)
		return
	}
	if err := f.Sync(); err != nil {
		fmt.Printf("vibeldr: log(%s): sync: %v\n", tag, err)
		return
	}
	fmt.Printf("vibeldr: log(%s): %d bytes on %s at LBA %d\n", tag, len(out), f.Name(), logAreaLBA)
}
