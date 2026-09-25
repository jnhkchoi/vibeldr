package hwscan

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// dmiFS is a minimal sysfs fixture holding only the given sys_vendor and
// product_name. DetectHypervisor has to be able to decide from DMI alone, with
// no other file present.
//
// dmiFS - 지정된 sys_vendor / product_name 만 담은 최소 sysfs 픽스처.
// 다른 파일이 없어도 DetectHypervisor 는 DMI 하나로 판정할 수 있어야 한다.
func dmiFS(vendor, product string) fstest.MapFS {
	m := fstest.MapFS{}
	if vendor != "" {
		m["sys/class/dmi/id/sys_vendor"] = &fstest.MapFile{Data: []byte(vendor + "\n")}
	}
	if product != "" {
		m["sys/class/dmi/id/product_name"] = &fstest.MapFile{Data: []byte(product + "\n")}
	}
	return m
}

func TestDetectHypervisorFromDMI(t *testing.T) {
	cases := []struct {
		name, vendor, product string
		want                  Hypervisor
	}{
		{"HyperV", "Microsoft Corporation", "Virtual Machine", HypervisorHyperV},
		{"VMware", "VMware, Inc.", "VMware Virtual Platform", HypervisorVMware},
		{"VMware7", "VMware, Inc.", "VMware7,1", HypervisorVMware},
		{"VirtualBoxByVendor", "innotek GmbH", "VirtualBox", HypervisorVirtualBox},
		{"VirtualBoxByProduct", "Oracle Corporation", "VirtualBox", HypervisorVirtualBox},
		{"ParallelsIntl", "Parallels International GmbH.", "Parallels ARM Virtual Machine", HypervisorParallels},
		{"ParallelsSoftware", "Parallels Software International Inc.", "Parallels Virtual Platform", HypervisorParallels},
		{"XenByVendor", "Xen", "HVM domU", HypervisorXen},
		{"XenByProduct", "Bochs", "HVM domU", HypervisorXen},
		{"KVMByRedHat", "Red Hat", "KVM", HypervisorKVM},
		{"KVMByGoogle", "Google", "Google Compute Engine", HypervisorKVM},
		{"KVMByQEMUProduct", "QEMU", "Standard PC (Q35 + ICH9, 2009) KVM", HypervisorKVM},
		{"QEMUPlain", "QEMU", "Standard PC (i440FX + PIIX, 1996)", HypervisorQEMU},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectHypervisor(dmiFS(tc.vendor, tc.product))
			if got != tc.want {
				t.Errorf("DetectHypervisor(vendor=%q, product=%q) = %q, want %q",
					tc.vendor, tc.product, got, tc.want)
			}
		})
	}
}

// TestDetectHypervisorFallsBackToSysHypervisor: the Xen PV case, where DMI says
// nothing and only /sys/hypervisor/type is there.
//
// TestDetectHypervisorFallsBackToSysHypervisor - DMI 는 아무것도 안
// 알려주는데 /sys/hypervisor/type 만 뜨는 Xen PV 케이스.
func TestDetectHypervisorFallsBackToSysHypervisor(t *testing.T) {
	m := fstest.MapFS{
		"sys/hypervisor/type": &fstest.MapFile{Data: []byte("xen\n")},
	}
	if got := DetectHypervisor(m); got != HypervisorXen {
		t.Errorf("got %q, want %q", got, HypervisorXen)
	}
}

// TestDetectHypervisorFallsBackToCpuinfo: neither DMI nor sys/hypervisor, only
// the hypervisor flag in cpuinfo. The name cannot be pinned down, so it has to
// come back Unknown with NeedsDirectBoot left false.
//
// TestDetectHypervisorFallsBackToCpuinfo - DMI 도 sys/hypervisor 도
// 없는데 cpuinfo 에 hypervisor flag 만 있는 경우. 이름을 특정 못 하니
// Unknown 이고, NeedsDirectBoot 는 false 로 남아야 한다.
func TestDetectHypervisorFallsBackToCpuinfo(t *testing.T) {
	m := fstest.MapFS{
		"proc/cpuinfo": &fstest.MapFile{Data: []byte(
			"processor\t: 0\n" +
				"vendor_id\t: GenuineIntel\n" +
				"flags\t\t: fpu vme de pse tsc hypervisor lm\n")},
	}
	got := DetectHypervisor(m)
	if got != HypervisorUnknown {
		t.Errorf("got %q, want %q", got, HypervisorUnknown)
	}
	if got.NeedsDirectBoot() {
		t.Errorf("%q.NeedsDirectBoot() = true, want false (모르는 상태에서 강요 금지)", got)
	}
}

// TestDetectHypervisorBaremetal: with no signal at all, it is bare metal.
// TestDetectHypervisorBaremetal - 아무 signal 도 없을 때는 baremetal.
func TestDetectHypervisorBaremetal(t *testing.T) {
	m := fstest.MapFS{
		"sys/class/dmi/id/sys_vendor":   &fstest.MapFile{Data: []byte("ASUSTeK COMPUTER INC.\n")},
		"sys/class/dmi/id/product_name": &fstest.MapFile{Data: []byte("PRIME B550-PLUS\n")},
		"proc/cpuinfo": &fstest.MapFile{Data: []byte(
			"processor\t: 0\n" +
				"vendor_id\t: AuthenticAMD\n" +
				"flags\t\t: fpu vme de pse tsc msr\n")},
	}
	if got := DetectHypervisor(m); got != HypervisorBaremetal {
		t.Errorf("got %q, want %q", got, HypervisorBaremetal)
	}
}

// TestDetectHypervisorEmptyFS: even a completely empty FS gives bare metal
// without crashing.
//
// TestDetectHypervisorEmptyFS - 완전 빈 FS 에서도 crash 없이 baremetal.
func TestDetectHypervisorEmptyFS(t *testing.T) {
	if got := DetectHypervisor(fstest.MapFS{}); got != HypervisorBaremetal {
		t.Errorf("got %q, want %q", got, HypervisorBaremetal)
	}
}

func TestNeedsDirectBoot(t *testing.T) {
	needs := map[Hypervisor]bool{
		HypervisorHyperV:     true,
		HypervisorXen:        true,
		HypervisorParallels:  true,
		HypervisorKVM:        false,
		HypervisorQEMU:       false,
		HypervisorVMware:     false,
		HypervisorVirtualBox: false,
		HypervisorBaremetal:  false,
		HypervisorUnknown:    false,
	}
	for h, want := range needs {
		if got := h.NeedsDirectBoot(); got != want {
			t.Errorf("%q.NeedsDirectBoot() = %v, want %v", h, got, want)
		}
	}
}

// This documents that everything here unifies behind the fs.FS interface, and
// keeps the import from being flagged as unused.
//
// 여기 있는 것이 전부 fs.FS 인터페이스로 통일된다는 점을 문서화하고, import 가
// 쓰이지 않는다는 경고를 막는다.
var _ fs.FS = fstest.MapFS{}
