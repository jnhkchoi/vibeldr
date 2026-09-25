//go:build linux

// kexec_linux.go - jumping into the DSM kernel that was just built.
//
// What it buys is a reboot. The patched kernel and rd.gz are sitting in the
// work directory already, so there is no reason to go back out through the
// firmware and GRUB to reach them.
//
// It also arranges rd.gz as a real initrd rather than as an initramfs, which is
// what DSM's own switch_root was written against. Whether that arrangement is
// needed is not settled: the MS_MOVE in cmd/vibeldr-init/pivot_linux.go also
// succeeds on the plain reboot path. So this is the quicker route, not the only
// working one, and bootIntoDSM falls back to a reboot when kexec is refused.
//
// The call is kexec_file_load(2) directly. Handing the kernel and initrd over
// as open fds leaves the parsing to the kernel, so no external kexec-tools and
// no parser of our own. Once loaded, reboot(LINUX_REBOOT_CMD_KEXEC) jumps.
//
// kexec_linux.go - 방금 만든 DSM 커널로 점프한다.
//
// 얻는 것은 재부팅 한 번이다. 패치된 커널과 rd.gz 가 이미 작업 디렉터리에
// 있는데 굳이 펌웨어와 GRUB 을 다시 거쳐 갈 이유가 없다.
//
// 덤으로 rd.gz 가 initramfs 가 아니라 진짜 initrd 로 실린다. DSM 의
// switch_root 가 상정한 모양이 그쪽이다. 다만 그게 꼭 필요한지는 확정되지
// 않았다 - cmd/vibeldr-init/pivot_linux.go 의 MS_MOVE 는 평범한 재부팅
// 경로에서도 성공한다. 그러니 이건 빠른 길이지 유일한 길은 아니고, kexec 가
// 거부되면 bootIntoDSM 이 재부팅으로 폴백한다.
//
// 구현은 kexec_file_load(2) 직접 호출. 커널 이미지와 initrd 를 열린 fd 로
// 넘기면 파싱까지 커널이 해주므로 외부 kexec-tools 도, 자체 파서도 필요
// 없다. 적재가 끝나면 reboot(LINUX_REBOOT_CMD_KEXEC) 로 점프한다.
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The x86-64 syscall number; sys_kexec_file_load is 320.
// x86-64 syscall 번호. sys_kexec_file_load 는 320.
const sysKexecFileLoad = 320

// kexecIntoDSM jumps into the installed DSM kernel.
//
// Returning means it failed; on success nothing after this function runs. On
// failure it returns the error and the caller falls back to the ordinary route,
// a reboot into GRUB.
//
// kexecIntoDSM - 설치된 DSM 커널로 점프한다.
//
// 돌아오면 실패다. 성공하면 이 함수 뒤 코드는 실행되지 않는다. 실패 시
// 에러를 돌려주고, 호출자는 기존 방식(재부팅 -> GRUB) 으로 폴백한다.
func kexecIntoDSM(kernelPath, initrdPath, cmdline string) error {
	kfd, err := os.Open(kernelPath)
	if err != nil {
		return fmt.Errorf("커널 열기 (%s): %w", kernelPath, err)
	}
	defer kfd.Close()

	ifd, err := os.Open(initrdPath)
	if err != nil {
		return fmt.Errorf("initrd 열기 (%s): %w", initrdPath, err)
	}
	defer ifd.Close()

	// The command line goes over as a NUL-terminated string, and the length
	// counts the NUL.
	//
	// cmdline 은 NUL 종결 문자열로 넘긴다. 길이에 NUL 을 포함한다.
	cl := append([]byte(cmdline), 0)

	if err := kexecFileLoad(kfd.Fd(), ifd.Fd(), cl, 0); err != nil {
		return fmt.Errorf("kexec_file_load: %w", err)
	}

	syncAll()
	// Jump into the loaded kernel. On success this does not return.
	// 적재된 커널로 점프. 성공하면 돌아오지 않는다.
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_KEXEC); err != nil {
		return fmt.Errorf("reboot(KEXEC): %w", err)
	}
	return fmt.Errorf("kexec reboot 이 돌아옴")
}

// kexecFileLoad is the raw sys_kexec_file_load call:
//
//	long kexec_file_load(int kernel_fd, int initrd_fd,
//	                     unsigned long cmdline_len, const char *cmdline,
//	                     unsigned long flags);
//
// kexecFileLoad - sys_kexec_file_load 원시 호출. 시그니처는 위와 같다.
func kexecFileLoad(kernelFd, initrdFd uintptr, cmdline []byte, flags uintptr) error {
	var clPtr unsafe.Pointer
	if len(cmdline) > 0 {
		clPtr = unsafe.Pointer(&cmdline[0])
	}
	_, _, errno := syscall.Syscall6(
		sysKexecFileLoad,
		kernelFd,
		initrdFd,
		uintptr(len(cmdline)),
		uintptr(clPtr),
		flags,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
