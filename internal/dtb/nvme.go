package dtb

import (
	"strconv"
	"strings"
)

// M.2 slots on the board.
//
// A device-tree model finds its M.2 SSDs through nvme_slot nodes at the root:
//
//	nvme_slot@1 {
//	    pcie_root = "0000:00:02.0";   // the NVMe controller, as in internal_slot
//	    port_type = "ssdcache";
//	};
//
// and reads the root's power_limit, one value per slot. SA6400's own tree has
// no board slots, only those of its add-in cards (m2_card@N/nvme, with
// "ssdcache" and a power limit of 14.85 per slot), so an NVMe SSD on the
// machine is not given a place: DSM reports it cannot find its location and
// the SSD cannot be used as a cache.
//
// 보드의 M.2 슬롯.
//
// device tree 모델은 루트의 nvme_slot 노드로 M.2 SSD 를 찾고 (위 모양), 루트의
// power_limit 을 슬롯마다 값 하나씩 읽는다. SA6400 자신의 트리에는 보드 슬롯이
// 없고 확장 카드의 것(m2_card@N/nvme, "ssdcache", 슬롯마다 전력 한도 14.85)만
// 있어서, 이 기계의 NVMe SSD 는 자리를 받지 못한다. DSM 은 위치를 찾을 수
// 없다고 하고 그 SSD 를 캐시로 쓸 수 없다.

const (
	nvmeSlotPrefix = "nvme_slot@"
	propPortType   = "port_type"
	propPowerLimit = "power_limit"
	// nvmePowerLimit is the per-slot value SA6400's add-in card slots carry.
	// nvmePowerLimit - SA6400 확장 카드 슬롯에 적힌 슬롯당 값.
	nvmePowerLimit = "14.85"
)

// SetNVMeSlots replaces the tree's board M.2 slots with one per NVMe
// controller in roots, in order, as SSD cache slots, and sets power_limit to
// match. With no roots the tree is left as it is. It returns the number of
// slots written.
//
// SetNVMeSlots - 트리의 보드 M.2 슬롯을 roots 의 NVMe 컨트롤러마다 하나씩, 순서대로
// SSD 캐시 슬롯으로 바꾸고 power_limit 을 거기 맞춘다. roots 가 없으면 트리를
// 그대로 둔다. 쓴 슬롯 수를 돌려준다.
func (t *Tree) SetNVMeSlots(roots []string) int {
	if len(roots) == 0 {
		return 0
	}
	kept := t.Root.Children[:0]
	for _, c := range t.Root.Children {
		if !strings.HasPrefix(c.Name, nvmeSlotPrefix) {
			kept = append(kept, c)
		}
	}
	t.Root.Children = kept
	limits := make([]string, len(roots))
	for i, r := range roots {
		n := &Node{Name: nvmeSlotPrefix + strconv.Itoa(i+1)}
		n.SetString(propPCIeRoot, r)
		n.SetString(propPortType, "ssdcache")
		t.Root.Children = append(t.Root.Children, n)
		limits[i] = nvmePowerLimit
	}
	t.Root.SetString(propPowerLimit, strings.Join(limits, ","))
	return len(roots)
}
