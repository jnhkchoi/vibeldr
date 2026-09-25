//go:build linux

package main

// Mellanox ConnectX-4/5 (ConnectX-Dx included) unlock + SR-IOV VF recognition.
//
// The problem: the mlx5_core / mlx5_ib in DSM's kernel are old, so (a) newer
// cards fail to probe, and (b) even when the PF is claimed, turning SR-IOV on
// gives VFs that announce no modalias.
//
// The approach: when the Mellanox vendor (0x15b3) is present, the pack's
// mlx_compat, ib_core, mlx5_core and mlx5_ib are loaded in place of the stock
// ones. The stock modules already loaded are unloaded first; one that refuses
// stays, and the pack's copy of it is not attempted. The unlock expects newer
// builds (OFED or upstream) of these modules in the pack. The pack build does
// not make them: it keeps Synology's own copies of this stack
// (vibeldr-modules tools/merge_originals.py), so with that pack the modules
// loaded here are Synology's.
//
// The VF count comes from the loader's kernel command line as
// vibeldr_mlx_vfs=N; when it is set, each PF gets that number written to
// /sys/bus/pci/devices/<addr>/sriov_numvfs. The command line is the only
// input here - there is no loader.yaml field for it, and adding one would
// only give the user a nicer way to set this same parameter.
//
// Mellanox ConnectX-4/5 (ConnectX-Dx 도 포함) unlock + SR-IOV VF 인식.
//
// 문제: DSM 커널의 mlx5_core / mlx5_ib 가 오래된 버전이라 (a) 새 카드는
// probe 를 못하고, (b) PF 는 잡혀도 SR-IOV 를 켜면 VF 가 modalias 를 안
// 뿜는다.
//
// 방법: Mellanox 벤더 (0x15b3) 가 보이면 팩의 mlx_compat, ib_core,
// mlx5_core, mlx5_ib 를 stock 대신 로드한다. 이미 로드된 stock 은 먼저
// rmmod 해 보고, 거부한 것은 남겨 두며 그 모듈의 팩본은 시도하지 않는다.
// 이 unlock 은 팩에 OFED 나 upstream 의 새 빌드가 있다고 전제한다. 팩 빌드는
// 그것을 만들지 않고 이 스택은 시놀 원본을 유지한다 (vibeldr-modules
// tools/merge_originals.py). 그래서 그 팩으로는 여기서 올리는 모듈이 시놀 것이다.
//
// SR-IOV VF 개수는 로더 커널 커맨드라인 vibeldr_mlx_vfs=N 으로 지정한다.
// 값이 있으면 각 PF 의 /sys/bus/pci/devices/<addr>/sriov_numvfs 에 그 수를
// 써서 활성화한다. 입력 경로는 커맨드라인 하나뿐이다. loader.yaml 에는
// 대응 필드가 없고, 생기더라도 결국 이 파라미터를 채우는 창구가 된다.

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
)

// mellanoxVendor is Mellanox Technologies' PCI vendor ID.
// mellanoxVendor - Mellanox Technologies PCI vendor ID.
const mellanoxVendor uint32 = 0x15b3

// mellanoxSRIOVKey is the GRUB command-line key; its value is an integer N.
// mellanoxSRIOVKey - GRUB 커맨드라인 키. 정수 N 을 값으로 받는다.
const mellanoxSRIOVKey = "vibeldr_mlx_vfs"

// mellanoxModules is the load order: compat -> ib_core -> mlx5_core -> mlx5_ib.
// Out of that order the upper modules are refused with unresolved symbols.
// Following the depends table gives this order anyway, but writing it out is
// the safer choice.
//
// mellanoxModules - 로드 순서. compat -> ib_core -> mlx5_core -> mlx5_ib.
// 이 순서를 지키지 않으면 상위 모듈이 심볼 미해결로 거부된다. depends 표를
// 따라가면 이 순서가 자연히 나오지만, 명시적으로 두는 게 안전하다.
var mellanoxModules = []string{"mlx_compat", "ib_core", "mlx5_core", "mlx5_ib"}

// unlockMellanox replaces the stock modules with the pack's and turns SR-IOV on
// when a Mellanox card is present.
//
// unlockMellanox - Mellanox 카드가 있으면 팩본으로 대체하고 SR-IOV 를 켠다.
func unlockMellanox(index kmod.Index, images map[string][]byte, loaded map[string]bool) {
	devs := pciByVendor(scanPCIDevices(hwscan.DefaultSysfs), mellanoxVendor)
	if len(devs) == 0 {
		return
	}
	var addrs []string
	for _, d := range devs {
		addrs = append(addrs, d.sysfsAddress)
	}
	logf("mellanox: found %d device(s): %v", len(devs), addrs)

	// Try to unload the stock modules already loaded. A failure does not stop
	// anything: that module stays in loaded, so loadModuleByName skips the
	// pack's copy of it. The worst case is the stock modules staying, and even
	// then the boot carries on.
	//
	// 이미 로드된 stock 모듈을 unload 해 본다. 실패해도 계속 간다. 그 모듈은
	// loaded 에 남아 있으므로 loadModuleByName 이 팩본을 건너뛴다. 최악의 경우
	// stock 이 남는 것이고, 그때도 부팅은 계속된다.
	for i := len(mellanoxModules) - 1; i >= 0; i-- {
		name := mellanoxModules[i]
		if !loaded[name] {
			continue
		}
		if err := deleteModule(name); err != nil {
			logf("mellanox: rmmod %s: %v (continuing)", name, err)
			continue
		}
		delete(loaded, name)
		logf("mellanox: unloaded stock %s", name)
	}

	// Load the pack's modules in order.
	// 팩본을 순서대로 로드한다.
	var loadedNames []string
	for _, name := range mellanoxModules {
		if _, ok := index[name]; !ok {
			continue // a module not in the pack is skipped quietly / 팩에 없는 모듈은 조용히 스킵
		}
		ok, err := loadModuleByName(name, index, images, loaded)
		switch {
		case err != nil:
			logf("mellanox: %s: %v", name, err)
		case ok:
			loadedNames = append(loadedNames, name)
		}
	}
	if len(loadedNames) > 0 {
		logf("mellanox: loaded %v", loadedNames)
	}

	// Turn the SR-IOV VFs on. With no value on the cmdline, nothing is touched
	// at all - turning VFs on without the user asking has large side effects.
	//
	// SR-IOV VF 활성화. cmdline 에 값이 없으면 아예 안 건드린다 - 사용자가
	// 명시하지 않았는데 임의로 VF 를 켜는 건 사이드 이펙트가 크다.
	vfs := cmdlineValue(mellanoxSRIOVKey, "")
	if vfs == "" {
		return
	}
	n, err := strconv.Atoi(vfs)
	if err != nil || n < 0 {
		logf("mellanox: %s=%q is not a non-negative integer", mellanoxSRIOVKey, vfs)
		return
	}
	for _, d := range devs {
		if err := enableSRIOV(d.path, n); err != nil {
			logf("mellanox: sriov %s -> %d: %v", d.sysfsAddress, n, err)
			continue
		}
		logf("mellanox: sriov %s -> %d vfs", d.sysfsAddress, n)
	}
}

// enableSRIOV writes the value into the PF's sriov_numvfs file.
//
// A missing file means this device is a VF rather than a PF, or a card with no
// SR-IOV support. Either way it returns an error and the caller logs it.
//
// enableSRIOV - PF 의 sriov_numvfs 파일에 값을 쓴다.
//
// 파일이 없으면 이 장치는 PF 가 아니라 VF 이거나 SR-IOV 지원이 없는
// 카드다. 어느 쪽이든 에러를 돌려주고 호출자가 로그를 남긴다.
func enableSRIOV(pciPath string, n int) error {
	path := pciPath + "/sriov_numvfs"
	if _, err := os.Stat(path); err != nil {
		return err
	}
	// Already on at some other count, the kernel answers EBUSY. Write 0 first
	// to reset, then set the value wanted.
	//
	// 이미 다른 수로 켜져 있으면 커널이 EBUSY 를 낸다. 0 을 먼저 써서
	// 리셋한 뒤 목표 값으로 다시 설정한다.
	_ = os.WriteFile(path, []byte("0"), 0o644)
	return os.WriteFile(path, []byte(strconv.Itoa(n)), 0o644)
}

// sysDeleteModule is the delete_module(2) syscall number on linux/amd64.
//
// The kmod package offers loading only, with no unload. This is a narrow need
// that arises here alone, so it stays in this file rather than changing that
// package.
//
// sysDeleteModule - delete_module(2) 시스콜 번호 (linux/amd64).
//
// kmod 패키지는 로드만 제공하고 unload 는 없다. 여기서만 필요한 좁은
// 용도라 그 패키지를 건드리지 않고 이 파일에 그대로 둔다.
const sysDeleteModule = 176

// deleteModule is rmmod. With flags=O_NONBLOCK, a remaining refcount comes back
// as EWOULDBLOCK at once and is treated as a failure.
//
// deleteModule - rmmod. flags=O_NONBLOCK 이라 refcount 가 남아 있으면
// 즉시 EWOULDBLOCK 을 받아 실패로 처리한다.
func deleteModule(name string) error {
	arg, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(sysDeleteModule,
		uintptr(unsafe.Pointer(arg)), uintptr(syscall.O_NONBLOCK), 0)
	if errno != 0 {
		return fmt.Errorf("delete_module %s: %w", name, errno)
	}
	return nil
}
