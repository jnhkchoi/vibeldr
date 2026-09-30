package hwscan

import (
	"reflect"
	"testing"
	"testing/fstest"
)

// The first flags line is read, and Missing keeps the order it was asked in.
// 첫 flags 줄을 읽고, Missing 은 물어본 순서를 지킨다.
func TestReadCPUFlags(t *testing.T) {
	fsys := fstest.MapFS{"proc/cpuinfo": &fstest.MapFile{Data: []byte(
		"processor\t: 0\nflags\t\t: fpu sse2 movbe bmi1 hypervisor\n\nprocessor\t: 1\nflags\t\t: fpu\n")}}
	c := ReadCPUFlags(fsys)
	if !c["movbe"] || !c["hypervisor"] || c["bmi2"] {
		t.Fatalf("flags %v", c)
	}
	if got := c.Missing([]string{"bmi2", "movbe", "avx2"}); !reflect.DeepEqual(got, []string{"bmi2", "avx2"}) {
		t.Fatalf("missing %v", got)
	}
	if !hasHypervisorFlag(fsys) {
		t.Fatal("hypervisor flag not seen")
	}
}

// With no cpuinfo nothing is reported missing, since nothing is known.
// cpuinfo 가 없으면 아무것도 모르니 빠졌다고 알리지 않는다.
func TestReadCPUFlagsUnreadable(t *testing.T) {
	c := ReadCPUFlags(fstest.MapFS{})
	if c != nil || c.Missing([]string{"movbe"}) != nil {
		t.Fatalf("got %v", c)
	}
}
