package dtb

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func sampleTree() *Tree {
	root := &Node{Name: ""}
	root.SetString("compatible", "Synology")
	root.SetString("model", "synology_test_unit")

	for i := 1; i <= 3; i++ {
		ahci := &Node{Name: "ahci"}
		ahci.SetString("pcie_root", "0000:80:08.2,00.0")
		ahci.SetUint32("ata_port", uint32(i-1))
		ahci.Props = append(ahci.Props, Property{Name: "internal_mode"})

		slot := &Node{Name: "internal_slot@" + string(rune('0'+i))}
		slot.SetString("protocol_type", "sata")
		slot.Children = append(slot.Children, ahci)
		root.Children = append(root.Children, slot)
	}
	return &Tree{Root: root, Version: 17, CompVersion: 16}
}

func TestRoundTrip(t *testing.T) {
	in := sampleTree()
	b, err := in.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	out, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(in.Root, out.Root) {
		t.Fatal("tree changed across a serialise/parse round trip")
	}
}

// TestRoundTripIsStable guards the serialiser against drift: writing a tree we
// just read must produce the same bytes every time, so an unchanged ramdisk
// repacks to an unchanged ramdisk.
//
// TestRoundTripIsStable - 직렬화기가 흔들리지 않는지 지킨다. 방금 읽은 트리를
// 쓰면 매번 같은 바이트가 나와야, 손대지 않은 램디스크가 그대로 다시 묶인다.
func TestRoundTripIsStable(t *testing.T) {
	b, err := sampleTree().Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	tr, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	again, err := tr.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(b, again) {
		t.Fatalf("re-serialising changed the tree: %d bytes then %d", len(b), len(again))
	}
}

func TestParseRejectsBadMagic(t *testing.T) {
	b, _ := sampleTree().Bytes()
	b[0] ^= 0xff
	if _, err := Parse(b); err == nil {
		t.Error("expected an error for a wrong magic number")
	}
}

func TestParseRejectsTruncated(t *testing.T) {
	b, _ := sampleTree().Bytes()
	if _, err := Parse(b[:len(b)/2]); err == nil {
		t.Error("expected an error for a truncated tree")
	}
}

func TestUint32Properties(t *testing.T) {
	n := &Node{Name: "ahci"}
	n.SetUint32("ata_port", 5)
	p, ok := n.Prop("ata_port")
	if !ok {
		t.Fatal("property was not added")
	}
	// Device tree cells are big endian regardless of the host.
	// device tree 셀은 호스트와 상관없이 빅엔디언이다.
	if !bytes.Equal(p.Value, []byte{0, 0, 0, 5}) {
		t.Fatalf("value = % x, want 00 00 00 05", p.Value)
	}
	if v, ok := n.GetUint32("ata_port"); !ok || v != 5 {
		t.Fatalf("GetUint32 = %d, %v", v, ok)
	}
	// Overwriting must replace, not append a second property.
	// 덮어쓰기는 두 번째 property 를 붙이지 말고 바꿔야 한다.
	n.SetUint32("ata_port", 6)
	if len(n.Props) != 1 {
		t.Fatalf("got %d properties after an overwrite, want 1", len(n.Props))
	}
}

func TestGetUint32RejectsWrongWidth(t *testing.T) {
	n := &Node{Name: "ahci"}
	n.SetString("pcie_root", "0000:00:1f.2")
	if _, ok := n.GetUint32("pcie_root"); ok {
		t.Error("a string was accepted as a single cell")
	}
}

func TestApplySATAPorts(t *testing.T) {
	tr := sampleTree()
	ports := []SATAPort{
		{PCIeRoot: "0000:00:1f.2", Port: 0},
		{PCIeRoot: "0000:00:1f.2", Port: 1},
		{PCIeRoot: "0000:00:1f.2", Port: 2},
	}
	r, err := tr.ApplySATAPorts(ports)
	if err != nil {
		t.Fatalf("ApplySATAPorts: %v", err)
	}
	if r.Assigned != 3 || len(r.Unassigned) != 0 || len(r.Surplus) != 0 {
		t.Fatalf("remap = %+v", r)
	}
	for i, s := range tr.InternalSlots() {
		got, ok := s.SATAPort()
		if !ok {
			t.Fatalf("slot %d lost its address", s.Index)
		}
		if got != ports[i] {
			t.Errorf("slot %d = %v, want %v", s.Index, got, ports[i])
		}
	}
}

// TestApplySATAPortsFewerPorts covers the ordinary case of running a twelve
// bay model on a machine with two disks: the surplus bays stay in the tree and
// simply read as empty.
//
// TestApplySATAPortsFewerPorts - 12 베이 모델을 디스크 두 개짜리 기계에서
// 돌리는 흔한 경우. 남는 베이는 트리에 그대로 남고 빈 베이로 읽힌다.
func TestApplySATAPortsFewerPorts(t *testing.T) {
	tr := sampleTree()
	r, err := tr.ApplySATAPorts([]SATAPort{{PCIeRoot: "0000:00:1f.2", Port: 0}})
	if err != nil {
		t.Fatalf("ApplySATAPorts: %v", err)
	}
	if r.Assigned != 1 {
		t.Errorf("Assigned = %d, want 1", r.Assigned)
	}
	if !reflect.DeepEqual(r.Unassigned, []int{2, 3}) {
		t.Errorf("Unassigned = %v, want [2 3]", r.Unassigned)
	}
	if len(tr.InternalSlots()) != 3 {
		t.Error("bays were removed; the model's bay count must not change")
	}
}

func TestApplySATAPortsMorePorts(t *testing.T) {
	tr := sampleTree()
	ports := make([]SATAPort, 5)
	for i := range ports {
		ports[i] = SATAPort{PCIeRoot: "0000:00:1f.2", Port: uint32(i)}
	}
	r, err := tr.ApplySATAPorts(ports)
	if err != nil {
		t.Fatalf("ApplySATAPorts: %v", err)
	}
	if r.Assigned != 3 || len(r.Surplus) != 2 {
		t.Fatalf("remap = %+v", r)
	}
}

func TestApplySATAPortsWithoutSlots(t *testing.T) {
	tr := &Tree{Root: &Node{}}
	if _, err := tr.ApplySATAPorts(nil); err == nil {
		t.Error("expected an error when the tree has no bays")
	}
}

// TestRealModelDTB works on Synology's own tree for SA6400. It is skipped when
// the work directory is not populated.
//
// TestRealModelDTB - 시놀로지가 낸 SA6400 트리 원본으로 확인한다. work
// 디렉터리가 채워져 있지 않으면 건너뛴다.
func TestRealModelDTB(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "work", "dsm-real", "model.dtb"))
	if err != nil {
		t.Skip("work/dsm-real/model.dtb is not present")
	}
	tr, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if model, _ := tr.Root.GetString("model"); model != "synology_epyc7002_sa6400" {
		t.Fatalf("model = %q", model)
	}

	slots := tr.InternalSlots()
	if len(slots) != 12 {
		t.Fatalf("got %d bays, want 12", len(slots))
	}
	// The first bay of the real appliance, as shipped.
	// 출하 상태 그대로인 실제 어플라이언스의 첫 베이.
	if got, ok := slots[0].SATAPort(); !ok || got.PCIeRoot != "0000:80:08.2,00.0" || got.Port != 0 {
		t.Fatalf("bay 1 = %v (ok=%v)", got, ok)
	}

	ports := make([]SATAPort, 12)
	for i := range ports {
		ports[i] = SATAPort{PCIeRoot: "0000:00:1f.2", Port: uint32(i)}
	}
	if _, err := tr.ApplySATAPorts(ports); err != nil {
		t.Fatalf("ApplySATAPorts: %v", err)
	}

	out, err := tr.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	for i, s := range back.InternalSlots() {
		got, _ := s.SATAPort()
		if got != ports[i] {
			t.Fatalf("after a round trip bay %d = %v, want %v", s.Index, got, ports[i])
		}
	}
	// Everything the model needs besides addresses must survive untouched.
	// 주소 말고 모델에 필요한 나머지는 전부 손대지 않은 채 남아야 한다.
	if cfg, _ := back.Root.GetString("syno_image_config"); cfg != "RACK_12_Bay" {
		t.Errorf("syno_image_config = %q, want RACK_12_Bay", cfg)
	}
	if back.Root.Child("E10M20-T1") == nil || back.Root.Child("RX1223rp") == nil {
		t.Error("expansion unit nodes were lost")
	}
}
