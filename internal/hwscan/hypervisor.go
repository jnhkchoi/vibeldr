// hypervisor.go - works out which hypervisor this machine is running on, or
// whether it is physical, by reading DMI out of sysfs.
//
// It matters because on Hyper-V, Xen HVM and Parallels the loader does not
// hand over to the DSM kernel with kexec. Those hypervisors share pages with
// their guest - the VP assist page, the hypercall page, the TSC reference
// page - and a kernel replaced by kexec does not hand them back, so the new
// kernel can find the hypervisor still writing into memory it now owns. On
// these the loader boots directly instead: GRUB loads the DSM kernel with no
// kexec in between. Neither path has been run on these three hypervisors.
//
// The detection reads DMI from sysfs and runs no external binary, because the
// early boot environment has almost no tools in it.
//
// hypervisor.go - 이 머신이 어떤 하이퍼바이저 위에서 도는지 (혹은 물리
// 머신인지) 를 sysfs DMI 로 알아낸다.
//
// 이게 중요한 이유: Hyper-V, Xen HVM, Parallels 에서는 로더가 kexec 로 DSM
// 커널에 넘기지 않는다. 이 하이퍼바이저들은 게스트와 페이지를 공유하는데
// (VP assist page, hypercall page, TSC reference page), kexec 로 바뀐 커널은
// 그걸 돌려주지 않아서 새 커널이 자기 메모리에 하이퍼바이저가 계속 쓰는 걸
// 만날 수 있다. 그래서 여기서는 직접 부팅한다: GRUB 이 DSM 커널을 kexec 없이
// 바로 올린다. 이 세 하이퍼바이저에서는 두 경로 모두 돌려 본 적이 없다.
//
// 판별은 외부 바이너리 없이 sysfs 의 DMI 정보만 읽어서 한다. 부팅 초기
// 환경에는 실행할 수 있는 도구가 거의 없기 때문이다.
package hwscan

import (
	"io/fs"
	"os"
	"strings"
)

// Hypervisor is the detected hypervisor. The string goes straight into logs
// and the UI, so it is kept short and readable.
//
// Hypervisor - 감지된 하이퍼바이저 종류. 문자열은 로그와 UI 에 그대로
// 뜨므로 사람이 읽기 좋게 짧게 둔다.
type Hypervisor string

const (
	HypervisorBaremetal  Hypervisor = "baremetal"
	HypervisorKVM        Hypervisor = "kvm"
	HypervisorQEMU       Hypervisor = "qemu"
	HypervisorVMware     Hypervisor = "vmware"
	HypervisorHyperV     Hypervisor = "hyperv"
	HypervisorXen        Hypervisor = "xen"
	HypervisorParallels  Hypervisor = "parallels"
	HypervisorVirtualBox Hypervisor = "virtualbox"
	HypervisorUnknown    Hypervisor = "unknown"
)

// NeedsDirectBoot reports whether kexec cannot be trusted on this hypervisor.
// See the comment at the top of the file: Hyper-V, Xen and Parallels.
//
// NeedsDirectBoot - 이 하이퍼바이저에서 kexec 를 믿을 수 없는지. 파일
// 최상단 주석 참고: Hyper-V, Xen, Parallels.
func (h Hypervisor) NeedsDirectBoot() bool {
	switch h {
	case HypervisorHyperV, HypervisorXen, HypervisorParallels:
		return true
	}
	return false
}

// DetectHypervisorSysfs detects against the running machine. A convenience
// wrapper.
//
// DetectHypervisorSysfs - 지금 도는 머신에 대고 감지. 편의 함수.
func DetectHypervisorSysfs() Hypervisor { return DetectHypervisor(os.DirFS("/")) }

// DetectHypervisor reads sysfs and procfs through an fs.FS. It follows the
// same pattern as hwscan.Scan, so it is fully testable with fstest.MapFS.
//
// The order of detection:
//
//  1. DMI sys_vendor and product_name - the most reliable information.
//  2. /sys/hypervisor/type - Xen is sometimes only visible here.
//  3. The "hypervisor" flag in /proc/cpuinfo - it does not say which one, but
//     it does establish that this is virtualised at all.
//
// DetectHypervisor - fs.FS 를 통해 sysfs / procfs 를 읽어서 감지한다.
// hwscan.Scan 과 같은 패턴이라 fstest.MapFS 로 완전 테스트가 가능하다.
//
// 감지 순서:
//
//  1. DMI sys_vendor + product_name - 가장 확실한 정보.
//  2. /sys/hypervisor/type - Xen 이 종종 이쪽에서만 잡힌다.
//  3. /proc/cpuinfo 의 "hypervisor" flag - 어떤 하이퍼바이저인지 몰라도
//     최소한 "가상화됐다" 는 사실은 확보한다.
func DetectHypervisor(fsys fs.FS) Hypervisor {
	vendor := readDMI(fsys, "sys/class/dmi/id/sys_vendor")
	product := readDMI(fsys, "sys/class/dmi/id/product_name")

	if h := classifyDMI(vendor, product); h != "" {
		return h
	}

	// /sys/hypervisor/type reads "xen" on a Xen dom0 or domU. It catches the
	// cases where DMI did not say "Xen", such as Xen PV.
	//
	// /sys/hypervisor/type: Xen dom0/domU 에서 "xen" 이 뜬다. DMI 가 "Xen" 을
	// 못 준 경우 (Xen PV 등) 여기서 잡힌다.
	if t := readTrim(fsys, "sys/hypervisor/type"); strings.EqualFold(t, "xen") {
		return HypervisorXen
	}

	// The hypervisor flag in /proc/cpuinfo only says "there is something
	// underneath". Narrowing by CPU vendor does not help either, since KVM
	// passes "GenuineIntel" straight through. Getting this far means the exact
	// name cannot be pinned down, so HypervisorUnknown is returned and
	// NeedsDirectBoot stays false - better than forcing a direct boot on a
	// false positive.
	//
	// /proc/cpuinfo 의 hypervisor flag 는 "위에 뭔가 있다" 만 말해 준다.
	// CPU vendor 로 좁혀도 소용없다. KVM 은 "GenuineIntel" 을 그대로 넘겨주기
	// 때문이다. 여기까지 왔으면 정확한 이름을 특정할 수 없으므로
	// HypervisorUnknown 을 반환하고 NeedsDirectBoot 는 false 로 남긴다.
	// false positive 로 direct boot 를 강요하는 것보다 낫다.
	if hasHypervisorFlag(fsys) {
		return HypervisorUnknown
	}

	return HypervisorBaremetal
}

// classifyDMI decides the hypervisor from the two DMI strings. An empty string
// means nothing matched, which lets the caller move on to another signal.
//
// Match table (substring, case-insensitive):
//
//	sys_vendor  "Microsoft Corporation"           -> hyperv
//	sys_vendor  "VMware"                          -> vmware
//	sys_vendor  "innotek"                         -> virtualbox
//	product     "VirtualBox"                      -> virtualbox
//	sys_vendor  "Parallels"                       -> parallels
//	sys_vendor  "Xen"                             -> xen
//	product     "HVM domU" / "Xen"                -> xen
//	sys_vendor  "QEMU"                            -> qemu, or kvm when
//	                                                product has "KVM"
//	sys_vendor  "Red Hat" + product "KVM"         -> kvm
//	sys_vendor  "Google" + product "Google Comp." -> kvm (GCE)
//
// classifyDMI - DMI 문자열 두 개로 하이퍼바이저를 결정한다. 매칭에 못
// 잡히면 빈 문자열을 돌려주고, 호출자가 다른 signal 로 넘어갈 수 있게 한다.
//
// 매칭 표는 위와 같다 (부분 문자열, 대소문자 무시). sys_vendor 가 "QEMU" 면
// qemu 지만, product 에 "KVM" 이 있으면 kvm 으로 승격한다.
func classifyDMI(vendor, product string) Hypervisor {
	v := strings.ToLower(vendor)
	p := strings.ToLower(product)

	switch {
	case strings.Contains(v, "microsoft"):
		// A Hyper-V guest reads "Microsoft Corporation" for sys_vendor and
		// "Virtual Machine" for product_name.
		//
		// Hyper-V 게스트는 sys_vendor 가 "Microsoft Corporation",
		// product_name 이 "Virtual Machine" 으로 뜬다.
		return HypervisorHyperV
	case strings.Contains(v, "vmware"):
		return HypervisorVMware
	case strings.Contains(v, "innotek") || strings.Contains(v, "oracle") && strings.Contains(p, "virtualbox"):
		return HypervisorVirtualBox
	case strings.Contains(p, "virtualbox"):
		return HypervisorVirtualBox
	case strings.Contains(v, "parallels"):
		return HypervisorParallels
	case strings.Contains(v, "xen") || strings.Contains(p, "hvm domu") || strings.Contains(p, "xen"):
		return HypervisorXen
	case strings.Contains(v, "qemu"):
		// QEMU runs both as pure TCG and with KVM acceleration. "KVM" in
		// product_name promotes it to KVM.
		//
		// QEMU 는 순수 TCG 로도, KVM 가속으로도 돈다. product_name 에
		// "KVM" 이 들어가면 KVM 으로 승격한다.
		if strings.Contains(p, "kvm") {
			return HypervisorKVM
		}
		return HypervisorQEMU
	case strings.Contains(v, "red hat") && strings.Contains(p, "kvm"):
		return HypervisorKVM
	case strings.Contains(v, "google") && strings.Contains(p, "google"):
		// GCE runs on KVM.
		// GCE 는 KVM 위에서 돈다.
		return HypervisorKVM
	}
	return ""
}

// readDMI reads a DMI slot and trims the whitespace. Missing or unreadable
// gives "".
//
// readDMI - DMI 슬롯을 읽고 앞뒤 공백을 제거한다. 없거나 못 읽으면 "".
func readDMI(fsys fs.FS, name string) string { return readTrim(fsys, name) }

func readTrim(fsys fs.FS, name string) string {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// hasHypervisorFlag is true when "hypervisor" appears in any flags line of
// /proc/cpuinfo. It is only set inside a guest.
//
// hasHypervisorFlag - /proc/cpuinfo 의 flags 라인 안에 "hypervisor" 가
// 하나라도 있으면 참. 게스트에서만 켜진다.
func hasHypervisorFlag(fsys fs.FS) bool {
	return ReadCPUFlags(fsys)["hypervisor"]
}
