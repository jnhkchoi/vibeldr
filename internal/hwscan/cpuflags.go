package hwscan

import (
	"io/fs"
	"os"
	"strings"
)

// CPUFlags are the feature flags /proc/cpuinfo lists for the first CPU, or nil
// when it cannot be read. Every CPU of a machine reports the same set, so the
// first flags line is enough.
//
// Inside a virtual machine this is the virtual CPU's set, which can leave out
// features the physical CPU has and still executes: a KVM guest with the
// default kvm64 model lists neither movbe nor bmi2, yet the DSM kernels that
// use them run there.
//
// CPUFlags - /proc/cpuinfo 가 첫 CPU 에 대해 적은 기능 플래그. 못 읽으면 nil.
// 한 기계의 CPU 는 모두 같은 집합을 알리므로 첫 flags 줄이면 된다.
//
// 가상 머신 안에서는 가상 CPU 의 집합이라, 실제 CPU 가 가졌고 실행도 하는 기능이
// 빠질 수 있다. 기본 kvm64 모델의 KVM 게스트는 movbe 도 bmi2 도 적지 않지만,
// 그것을 쓰는 DSM 커널이 거기서 돈다.
type CPUFlags map[string]bool

// ReadCPUFlags reads the flags from fsys (rooted at /).
// ReadCPUFlags - fsys(/ 기준)에서 플래그를 읽는다.
func ReadCPUFlags(fsys fs.FS) CPUFlags {
	b, err := fs.ReadFile(fsys, "proc/cpuinfo")
	if err != nil {
		return nil
	}
	return ParseCPUFlags(string(b))
}

// ParseCPUFlags takes the flags out of /proc/cpuinfo's text.
// ParseCPUFlags - /proc/cpuinfo 내용에서 플래그를 꺼낸다.
func ParseCPUFlags(cpuinfo string) CPUFlags {
	for _, line := range strings.Split(cpuinfo, "\n") {
		if !strings.HasPrefix(line, "flags") {
			continue
		}
		// The line looks like "flags\t\t: fpu vme ... hypervisor ...".
		// "flags\t\t: fpu vme ... hypervisor ..." 형식.
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out := CPUFlags{}
		for _, f := range strings.Fields(rest) {
			out[f] = true
		}
		return out
	}
	return nil
}

// RunningCPUFlags reads the running machine's flags from /proc/cpuinfo.
// RunningCPUFlags - 지금 도는 기계의 플래그를 /proc/cpuinfo 에서 읽는다.
func RunningCPUFlags() CPUFlags { return ReadCPUFlags(os.DirFS("/")) }

// Missing returns the flags of want this CPU does not list, in want's order.
// With no flags read at all it returns nil: nothing is known either way.
//
// Missing - want 중 이 CPU 가 적지 않은 플래그를 want 순서대로 돌려준다.
// 플래그를 전혀 못 읽었으면 nil 이다. 어느 쪽도 알 수 없다.
func (c CPUFlags) Missing(want []string) []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, f := range want {
		if !c[f] {
			out = append(out, f)
		}
	}
	return out
}
