package dtb

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// How a DSM device tree describes a disk bay. The shape is the same across
// every model's model.dtb:
//
//	internal_slot@2 {
//	    protocol_type = "sata";
//	    led_type      = "atmega1608";
//	    ahci {
//	        pcie_root     = "0000:80:08.2,00.0";  // NUL-terminated string
//	        ata_port      = <0x00000001>;         // big-endian cell, from 0
//	        internal_mode;                        // presence only, no value
//	    };
//	    led_green  { led_name = "syno_led2"; };
//	    led_orange { led_name = "syno_led3"; };
//	};
//
// pcie_root names a device on the root bus, plus one hop per bridge below it.
// "0000:80:08.2,00.0" means device 00.0 behind bridge 0000:80:08.2. ata_port
// is the port index within that controller.
//
// A multi-bay appliance scatters its bays across several controllers in no
// particular order, so the slot number on its own says nothing about the
// topology. That is why the addresses have to be rewritten rather than guessed.
//
// DSM device tree 가 디스크 베이를 어떻게 서술하는지 (위 구조가 여러 모델의
// model.dtb 에 공통이다).
//
// pcie_root 는 루트 버스의 장치를 지정하고, 필요하면 아래 각 브리지마다
// hop 한 개씩 붙인다. "0000:80:08.2,00.0" 은 브리지 0000:80:08.2 뒤의 00.0
// 장치다. ata_port 는 그 컨트롤러 안의 포트 인덱스다.
//
// 다중 베이 어플라이언스는 여러 컨트롤러에 베이를 순서 없이 분산 배치하니,
// 슬롯 번호만 봐서는 토폴로지에 대해 아무것도 알 수 없다. 그래서 주소를
// 추측이 아니라 다시 써야 한다.

const (
	slotPrefix   = "internal_slot@"
	ahciNode     = "ahci"
	propPCIeRoot = "pcie_root"
	propATAPort  = "ata_port"
)

// Uint32 reads a single-cell value.
// Uint32 - single-cell 값 읽기.
func (p Property) Uint32() (uint32, bool) {
	if len(p.Value) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(p.Value), true
}

// GetUint32 reads a single-cell property.
// GetUint32 - single-cell property 읽기.
func (n *Node) GetUint32(name string) (uint32, bool) {
	p, ok := n.Prop(name)
	if !ok {
		return 0, false
	}
	return p.Uint32()
}

// SetUint32 writes a single-cell property, adding it when it is not there.
// SetUint32 - single-cell property 쓰기 (없으면 추가).
func (n *Node) SetUint32(name string, v uint32) {
	val := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	if p, ok := n.Prop(name); ok {
		p.Value = val
		return
	}
	n.Props = append(n.Props, Property{Name: name, Value: val})
}

// SATAPort identifies one port on an AHCI controller.
// SATAPort - AHCI 컨트롤러 위의 포트 하나 식별자.
type SATAPort struct {
	// PCIeRoot is in the device tree's own notation, e.g. "0000:00:1f.2".
	// PCIeRoot - device tree 자체 표기 ("0000:00:1f.2" 등).
	PCIeRoot string
	// Port is the controller-relative port index, counted from zero.
	// Port - 컨트롤러 안에서의 포트 번호. 0 부터 센다.
	Port uint32
}

func (s SATAPort) String() string { return fmt.Sprintf("%s port %d", s.PCIeRoot, s.Port) }

// Slot is one disk bay in the tree.
// Slot - 트리 안의 디스크 베이 하나.
type Slot struct {
	Index int // the N in internal_slot@N / internal_slot@N 의 N
	Node  *Node
}

// InternalSlots returns the disk bays in number order. A device tree stores
// them in file order, which is usually the same but not always.
//
// InternalSlots - 디스크 베이를 번호 순으로 돌려준다. device tree 는 파일
// 순서로 저장하는데, 대개 같지만 항상 그렇지는 않다.
func (t *Tree) InternalSlots() []Slot {
	if t.Root == nil {
		return nil
	}
	var out []Slot
	for _, n := range t.Root.ChildrenWithPrefix(slotPrefix) {
		idx, err := strconv.Atoi(strings.TrimPrefix(n.Name, slotPrefix))
		if err != nil {
			continue
		}
		out = append(out, Slot{Index: idx, Node: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// SATAPort reads the address this slot currently points at.
// SATAPort - 슬롯이 현재 가리키는 주소를 읽는다.
func (s Slot) SATAPort() (SATAPort, bool) {
	ahci := s.Node.Child(ahciNode)
	if ahci == nil {
		return SATAPort{}, false
	}
	root, ok := ahci.GetString(propPCIeRoot)
	if !ok {
		return SATAPort{}, false
	}
	port, ok := ahci.GetUint32(propATAPort)
	if !ok {
		return SATAPort{}, false
	}
	return SATAPort{PCIeRoot: root, Port: port}, true
}

// SetSATAPort points the slot at a different controller and port.
// SetSATAPort - 슬롯이 다른 컨트롤러/포트를 가리키게 쓴다.
func (s Slot) SetSATAPort(p SATAPort) error {
	ahci := s.Node.Child(ahciNode)
	if ahci == nil {
		return fmt.Errorf("dtb: %s has no %s node, so it is not a SATA bay", s.Node.Name, ahciNode)
	}
	ahci.SetString(propPCIeRoot, p.PCIeRoot)
	ahci.SetUint32(propATAPort, p.Port)
	return nil
}

// Remap is the result of rewriting the tree's bay addresses.
// Remap - 트리의 베이 주소 재작성 결과.
type Remap struct {
	// Assigned is how many bays now point at real hardware.
	// Assigned - 이제 실제 하드웨어를 가리키게 된 베이 수.
	Assigned int
	// Unassigned are the bays left pointing at the original appliance address,
	// because the machine has fewer ports than the model has bays. DSM shows
	// them as empty, which is the truth.
	//
	// Unassigned - 원본 어플라이언스 주소를 그대로 가리키게 남은 베이들
	// (머신의 포트 수가 모델의 베이 수보다 적어서). DSM 에는 빈 베이로
	// 보이는데, 그게 사실이다.
	Unassigned []int
	// Surplus are the machine's leftover ports, beyond what the model can show.
	// Surplus - 모델이 표시할 수 있는 베이 수를 넘는, 머신에 남은 포트들.
	Surplus []SATAPort
	// Skipped are the names of bays with no ahci node, such as an NVMe-wired
	// bay on a mixed SATA/NVMe model.
	//
	// Skipped - ahci 노드가 없는 베이 이름들 (SATA/NVMe 혼합 모델에서
	// NVMe 로 배선된 베이 등).
	Skipped []string
}

// Summary renders a remap as a single log line.
// Summary - remap 을 한 줄 로그 형태로 렌더.
func (r Remap) Summary() string {
	s := fmt.Sprintf("%d bay(s) mapped", r.Assigned)
	if n := len(r.Unassigned); n > 0 {
		s += fmt.Sprintf(", %d left empty", n)
	}
	if n := len(r.Surplus); n > 0 {
		s += fmt.Sprintf(", %d port(s) beyond the model's bay count", n)
	}
	if n := len(r.Skipped); n > 0 {
		s += fmt.Sprintf(", %d non-SATA bay(s) untouched", n)
	}
	return s
}

// ApplySATAPorts rewrites every SATA bay to point at the given ports, in order.
// The tree's own bay count, LED wiring and expansion units are left alone -
// only the addresses change.
//
// ports must already be in the order the bays should appear in. That order is
// what the user sees in Storage Manager.
//
// ApplySATAPorts - 모든 SATA 베이가 주어진 포트를 순서대로 가리키게 재작성.
// 트리 자체의 베이 수, LED 배선, 확장 유닛은 그대로 유지하고 주소만 바꾼다.
//
// ports 는 이미 베이가 나타나야 할 순서로 정렬돼 있어야 한다. 그 순서가
// 스토리지 매니저에서 사용자에게 보이는 순서다.
func (t *Tree) ApplySATAPorts(ports []SATAPort) (Remap, error) {
	slots := t.InternalSlots()
	if len(slots) == 0 {
		return Remap{}, fmt.Errorf("dtb: tree has no %sN nodes, so it does not describe SATA bays", slotPrefix)
	}

	var r Remap
	next := 0
	for _, s := range slots {
		if s.Node.Child(ahciNode) == nil {
			r.Skipped = append(r.Skipped, s.Node.Name)
			continue
		}
		if next >= len(ports) {
			r.Unassigned = append(r.Unassigned, s.Index)
			continue
		}
		if err := s.SetSATAPort(ports[next]); err != nil {
			return r, err
		}
		next++
		r.Assigned++
	}
	if next < len(ports) {
		r.Surplus = append(r.Surplus, ports[next:]...)
	}
	return r, nil
}
