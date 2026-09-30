//go:build linux

package main

import (
	"os"
	"sort"
	"strings"
	"syscall"
)

// detachDev clears away every mount left on the old root so that the pivot
// succeeds.
//
// The ramdisk stage ends by unmounting /dev and moving to the installed root.
// In a VM the unmount fails:
//
//	umount: can't unmount /dev: Device or resource busy
//	switch_root: error moving root
//
// When that happens the boot falls back to the installer, which offers to
// install the already installed system again.
//
// What holds /dev is synobios (DSM's own hardware monitor). It opens
// /dev/ttyS1 to talk to a power management microcontroller that real hardware
// has and a VM does not, and keeps waiting for a reply that never comes.
//
// /dev alone is not enough. With /sys and the debugfs under it still there the
// pivot fails as well - a root cannot be moved while other filesystems sit on
// top of it. So everything except the new root and what is inside it is taken
// off, deepest path first. What DSM needs is mounted again on the other side.
//
// All of it is lazy (MNT_DETACH). A plain umount has already failed; a detach
// takes the mount out of the tree at once while letting processes that still
// hold files keep their share. synobios goes on waiting for its
// microcontroller, harmlessly, inside a filesystem nothing can reach any more.
//
// detachDev 는 이전 루트에 남아있는 모든 마운트를 정리해 pivot 이 성공하게 한다.
//
// 램디스크 단계 마지막은 /dev 언마운트 + 설치된 루트로 이동. VM 에서는
// 언마운트가 실패:
//
//	umount: can't unmount /dev: Device or resource busy
//	switch_root: error moving root
//
// 이러면 이미 설치된 시스템을 다시 설치하겠냐는 인스톨러로 되돌아옴.
//
// /dev 를 붙잡고 있는 건 synobios (DSM 자체 하드웨어 모니터). 실기에는
// 있지만 VM 에는 없는 전원관리 마이크로컨트롤러와 통신하려고 /dev/ttyS1 을
// 열고 오지 않는 응답을 계속 기다린다.
//
// /dev 하나로는 부족하다. /sys 와 그 아래 debugfs 가 남아 있어도 pivot 은
// 실패한다 - 루트는 다른 파일시스템이 얹혀 있는 상태로는 이동할 수 없다.
// 그래서 새 루트와 그 안의 것을 제외한 모든 걸 깊은 경로부터 떼어낸다.
// DSM 이 필요한 것들은 반대편에서 다시 마운트한다.
//
// 전부 lazy (MNT_DETACH). 일반 umount 는 이미 실패했고, detach 는 마운트를
// 트리에서 즉시 빼내면서, 아직 파일을 잡고 있는 프로세스는 자기 몫을 그대로
// 유지하게 한다. synobios 는 이제 도달 불가능한 파일시스템 안에서 자기
// 마이크로컨트롤러를 무해하게 계속 기다린다.
func detachDev() {
	var done, failed int
	// Deeper paths first: /sys/kernel/debug has to go before /sys.
	// 깊은 경로 먼저. /sys/kernel/debug 가 /sys 보다 먼저 가야 한다.
	for _, mp := range mountPoints() {
		// /proc goes last. It is where the mount table is read from, so
		// unmounting it first would leave the remaining mounts unreadable and
		// the whole thing would end having detached nothing.
		//
		// /proc 는 마지막. 마운트 테이블을 읽는 자리이니, 먼저 언마운트하면
		// 남은 마운트 목록을 못 읽어서 아무것도 안 뗀 채 끝나버린다.
		if keepMounted(mp) || mp == procMount {
			continue
		}
		if err := syscall.Unmount(mp, syscall.MNT_DETACH); err != nil {
			logf("detach: %s: %v", mp, err)
			failed++
			continue
		}
		logf("detach: took %s out of the way", mp)
		done++
	}

	// What is left is what decides whether the pivot can work, so it is worth
	// the handful of lines once per boot - read while /proc still answers.
	//
	// 남은 것이 pivot 성패를 가르니, 부팅마다 몇 줄 찍을 가치가 있다 -
	// /proc 가 아직 응답할 때 읽는다.
	for _, mp := range mountPoints() {
		if mp != procMount {
			logf("detach: still mounted: %s", mp)
		}
	}
	if err := syscall.Unmount(procMount, syscall.MNT_DETACH); err != nil {
		logf("detach: %s: %v", procMount, err)
		failed++
	} else {
		done++
	}
	logf("detach: %d mount(s) taken out of the way, %d refused", done, failed)
}

// procMount is both something to clear away and the only window onto the mount
// list, which is why it is handled last.
//
// procMount - 정리 대상이면서 동시에 마운트 목록을 읽는 유일한 창구.
// 마지막에 처리하는 이유다.
const procMount = "/proc"

// keepMounted reports whether this mount has to stay for the pivot.
// keepMounted - 이 마운트가 pivot 을 위해 남아야 하는지.
func keepMounted(mp string) bool {
	return mp == "/" || mp == newRoot || strings.HasPrefix(mp, newRoot+"/")
}

// mountPoints is the list of current mount points, longest path first.
// mountPoints - 현재 마운트된 지점 목록. 긴 경로 먼저.
func mountPoints() []string {
	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		logf("detach: /proc/mounts: %v", err)
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(l); len(f) >= 2 {
			out = append(out, f[1])
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}
