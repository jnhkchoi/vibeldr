package cmdline

import (
	"fmt"
	"testing"

	"vibeldr/internal/hwscan"
)

// TestPortMapMultiController - SataPortMap / DiskIdxMap generation across
// several controller layouts.
//
// The value is not a bay count: it is one digit per controller, in DSM's own
// enumeration order, and DiskIdxMap says where each controller's first port
// lands. The four cases below are the shapes that get this wrong most easily.
//
// TestPortMapMultiController - 컨트롤러 구성별 SataPortMap / DiskIdxMap 생성.
//
// 이 값은 베이 총수가 아니라 컨트롤러마다 한 자리씩, DSM 열거 순서대로
// 적는 것이고, DiskIdxMap 은 각 컨트롤러의 첫 포트가 몇 번 디스크인지를
// 적는다. 아래 네 경우가 가장 틀리기 쉬운 모양이다.
func TestPortMapMultiController(t *testing.T) {
	ports := func(first, n int) []hwscan.Port {
		out := make([]hwscan.Port, n)
		for i := range out {
			out[i] = hwscan.Port{Name: fmt.Sprintf("ata%d", first+i), Index: uint32(i)}
		}
		return out
	}
	// Two controllers: 1f.2 is ata1 to 6, 07.0 is ata7 to 12.
	// 두 컨트롤러: 1f.2 가 ata1~6, 07.0 이 ata7~12.
	late := hwscan.Controller{PCIeRoot: "00:07.0", Ports: ports(7, 6)}
	early := hwscan.Controller{PCIeRoot: "00:1f.2", Ports: ports(1, 6)}
	plan := []string{"00:07.0 0"}

	cases := []struct {
		name        string
		controllers []hwscan.Controller
		bayOrder    []string
		maxBays     int
		wantPortmap string
	}{
		// 4 bays: the first controller falls outside the bay range, and a
		// leading "0" panics the kernel, so it becomes a throwaway "1".
		// 4 베이: 첫 컨트롤러가 베이 범위 밖이고, 첫 자리 "0" 은 커널 패닉을
		// 부르므로 버리는 "1" 로 바뀐다.
		{"4 bays, disk on the second controller", []hwscan.Controller{late, early}, plan, 4, "14"},
		// 12 bays: both controllers fit, so both report all six ports.
		// 12 베이: 두 컨트롤러가 다 들어가므로 각각 6 포트를 그대로 적는다.
		{"12 bays, same layout", []hwscan.Controller{late, early}, plan, 12, "66"},
		// One disk per controller: each is capped by the next assigned one.
		// 컨트롤러마다 디스크 하나씩: 각각 다음 배정 컨트롤러에서 잘린다.
		{"four controllers, one bay each", []hwscan.Controller{
			{PCIeRoot: "00:01.0", Ports: ports(1, 6)},
			{PCIeRoot: "00:02.0", Ports: ports(7, 6)},
			{PCIeRoot: "00:03.0", Ports: ports(13, 6)},
			{PCIeRoot: "00:04.0", Ports: ports(19, 6)},
		}, []string{"00:01.0 0", "00:02.0 0", "00:03.0 0", "00:04.0 0"}, 4, "1111"},
		// No bay order at all: controllers are laid out in ata number order,
		// whatever order they were passed in.
		// 베이 지정이 없으면 넘겨받은 순서와 상관없이 ata 번호 순으로 놓는다.
		{"no bay order", []hwscan.Controller{late, early}, nil, 16, "66"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pm, idx := portMapFor(tc.controllers, tc.bayOrder, tc.maxBays)
			if pm != tc.wantPortmap {
				t.Errorf("SataPortMap = %q, want %q (DiskIdxMap %q)", pm, tc.wantPortmap, idx)
			}
			if len(idx) != 2*len(pm) {
				t.Errorf("DiskIdxMap %q 는 SataPortMap %q 의 자리마다 두 자리여야 한다", idx, pm)
			}
		})
	}
}
