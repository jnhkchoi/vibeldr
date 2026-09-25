//go:build linux

package main

import (
	"os"
	"syscall"
)

// Handing the machine over to the system on the disk.
//
// DSM's last ramdisk script ends with busybox switch_root, which does three
// things: move the installed filesystem onto /, chroot into it, exec its init.
// This helper does those three steps itself, because there is work to do in
// between and because busybox reports any failure as nothing more than
//
//	switch_root: error moving root
//
// The move works. A booted machine logs
//
//	vibeldr: pivot: /tmpRoot is the root now
//
// and that line comes out of the MS_MOVE branch alone. detachDev runs first
// and takes everything still mounted under the old root out of the way; a root
// cannot be moved while another filesystem sits on top of it. Whether the move
// would in fact fail without detachDev has never been tried.
//
// pivot_root and chroot sit below MS_MOVE as fallbacks and have not been
// needed. pivot_root cannot leave an initramfs and returns EINVAL, which is a
// mainline restriction rather than anything Synology added. chroot changes what
// / means for this process and everything it starts without touching a mount,
// so there is nothing for the kernel to refuse; the cost is that the ramdisk
// stays behind the new root as some tens of megabytes nothing reclaims.
//
// chroot is not only the fallback. It runs on every path, including after a
// successful MS_MOVE, because it is the second half of what switch_root does.
//
// 머신을 디스크의 시스템에게 넘긴다.
//
// DSM 램디스크 마지막 스크립트는 busybox switch_root 로 끝나고, 그건 세 가지를
// 한다: 설치된 파일시스템을 / 로 옮기고, 거기로 chroot 하고, 그쪽 init 을
// exec. 이 헬퍼가 세 단계를 직접 하는 이유는 중간에 할 일이 있어서이고,
// busybox 는 무엇이 실패하든 위 한 줄로만 알려주기 때문이다.
//
// 옮기기는 된다. 부팅한 기계의 로그에 위 두 번째 줄이 찍히고, 그 줄은 MS_MOVE
// 분기에서만 나온다. 앞서 detachDev 가 옛 루트 밑에 남은 마운트를 전부 떼어
// 낸다 - 루트는 다른 파일시스템이 얹힌 채로는 못 옮긴다. 다만 detachDev 가
// 없으면 정말 실패하는지는 시험해 본 적 없다.
//
// pivot_root 와 chroot 는 MS_MOVE 아래에 폴백으로 있고 아직 쓰인 적이 없다.
// pivot_root 는 initramfs 밖으로 못 나가 EINVAL 을 주는데 그건 시놀로지가
// 더한 게 아니라 메인라인 제약이다. chroot 는 마운트를 건드리지 않고 이
// 프로세스와 자식들 기준의 / 만 바꾸므로 커널이 거부할 대상 자체가 없다.
// 대가는 램디스크가 새 루트 뒤에 수십 MB 로 남아 회수되지 않는 것.
//
// chroot 는 폴백 전용이 아니다. MS_MOVE 가 성공한 뒤에도 항상 실행된다 -
// switch_root 가 하는 일의 나머지 절반이라서.

// pivotRoot hands the machine over to the installed system's init.
// pivotRoot - 머신을 설치된 시스템의 init 에게 넘겨준다.
func pivotRoot(newroot, init string) {
	announce()

	if err := os.Chdir(newroot); err != nil {
		logf("pivot: %s: %v", newroot, err)
		fallBackToSwitchRoot(newroot, init)
		return
	}

	// The device tree, the settings and the agent belong to the system on the
	// disk, and this is the last moment it can be touched from outside.
	//
	// device tree, 설정, 에이전트는 디스크의 시스템에 속하는 것들이고,
	// 지금이 바깥에서 그 시스템에 손댈 수 있는 마지막 순간이다.
	mapInstalledSystem(newroot)
	applyInstalledSettings(newroot)
	// The compatibility database is fixed now too. The agent does it again
	// later, but by then the storage service has already judged the drives and
	// it is too late.
	//
	// 호환성 DB 도 지금 고친다. 에이전트가 나중에 또 하지만, 그때는
	// 스토리지 서비스가 이미 드라이브를 판정한 뒤라 늦다.
	patchInstalledDrivelists(newroot)
	installAgent(newroot)
	installFirmware(firmwareListFile, "/lib/firmware", newroot)

	// Three ways to change the root, in order. MS_MOVE is the one that has
	// succeeded on every boot observed so far; the two below it are there for
	// a kernel that refuses it, and have not been reached.
	//
	// 루트를 바꾸는 세 방법을 순서대로. 지금까지 관측된 부팅에서는 전부
	// MS_MOVE 가 성공했다. 아래 둘은 그걸 거부하는 커널을 위한 자리이고
	// 아직 걸린 적이 없다.
	switch {
	case syscall.Mount(".", "/", "", syscall.MS_MOVE, "") == nil:
		logf("pivot: %s is the root now", newroot)
	case syscall.PivotRoot(".", "initrd") == nil:
		// DSM's own script creates /tmpRoot/initrd one line earlier. It is
		// the directory this pivot_root call requires and has no other use.
		//
		// DSM 자체 스크립트가 한 줄 앞에서 /tmpRoot/initrd 를 만들어 두는데,
		// 이 pivot_root 호출이 요구하는 디렉터리다 (다른 용도는 없다).
		logf("pivot: pivot_root succeeded")
	default:
		// Both ways of replacing the root are closed; the chroot just below
		// carries it alone.
		//
		// 루트 교체가 둘 다 막힌 경우. 바로 아래 chroot 만으로 넘어간다.
		logf("pivot: the kernel will not let the root be replaced; entering it instead")
	}

	if err := syscall.Chroot("."); err != nil {
		logf("pivot: chroot: %v (errno %d)", err, errnoOf(err))
		fallBackToSwitchRoot(newroot, init)
		return
	}
	if err := os.Chdir("/"); err != nil {
		logf("pivot: chdir /: %v", err)
	}

	// The console the boot has been writing to lives in the new root now.
	// 부팅이 써 오던 콘솔은 이제 새 루트 안에 있다.
	reopenConsole()

	logf("pivot: handing over to %s", init)
	if err := syscall.Exec(init, []string{init}, os.Environ()); err != nil {
		logf("pivot: %s: %v", init, err)
	}
}

// fallBackToSwitchRoot runs DSM's own pivot. If that fails it fails the way it
// always did, so nothing fails at a new point.
//
// fallBackToSwitchRoot - DSM 자체 pivot 을 실행한다. 실패해도 원래 실패하던
// 대로 실패시켜서 새 지점에서 실패하지 않게 한다.
func fallBackToSwitchRoot(newroot, init string) {
	const switchRoot = "/sbin/switch_root"
	argv := []string{switchRoot, "-c", "/dev/console", newroot, init}
	if err := syscall.Exec(switchRoot, argv, os.Environ()); err != nil {
		logf("pivot: %s: %v", switchRoot, err)
	}
}

// reopenConsole reconnects the standard streams to the new root's console, the
// same effect as switch_root -c. A boot that cannot say what it is doing is a
// boot nobody can fix.
//
// reopenConsole - 표준 스트림을 새 루트의 콘솔로 다시 연결한다 (switch_root -c
// 와 동일한 효과). 부팅이 자기가 뭘 하는지 말할 수 없으면 아무도 못 고친다.
func reopenConsole() {
	f, err := os.OpenFile("/dev/console", os.O_RDWR, 0)
	if err != nil {
		return
	}
	fd := int(f.Fd())
	for _, std := range []int{0, 1, 2} {
		_ = syscall.Dup2(fd, std)
	}
	if fd > 2 {
		_ = f.Close()
	}
}

// errnoOf pulls the raw error number out. "invalid argument" and "operation not
// permitted" point at different branches in the kernel, so the two have to be
// told apart.
//
// errnoOf - 원시 에러 번호를 뽑아낸다. "invalid argument" 와 "operation not
// permitted" 는 커널의 다른 갈래를 가리키므로 이 구분이 필요하다.
func errnoOf(err error) int {
	if e, ok := err.(syscall.Errno); ok {
		return int(e)
	}
	return -1
}
