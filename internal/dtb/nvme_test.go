package dtb

import "testing"

// NVMe controllers become nvme_slot nodes with a power limit each, replacing
// any the tree had, and survive a round trip through the binary form.
//
// NVMe 컨트롤러는 슬롯마다 전력 한도를 단 nvme_slot 노드가 되고, 트리에 있던
// 것을 대신하며, 바이너리 형식을 거쳐도 남는다.
func TestSetNVMeSlots(t *testing.T) {
	tree := &Tree{Root: &Node{Children: []*Node{{Name: "nvme_slot@1"}, {Name: "internal_slot@1"}}}, Version: 17, CompVersion: 16}
	if n := tree.SetNVMeSlots([]string{"0000:00:02.0", "0000:00:1c.0,00.0"}); n != 2 {
		t.Fatalf("wrote %d", n)
	}
	raw, err := tree.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := back.Root.GetString("power_limit"); got != "14.85,14.85" {
		t.Errorf("power_limit %q", got)
	}
	s := back.Root.Child("nvme_slot@2")
	if s == nil {
		t.Fatal("no nvme_slot@2")
	}
	if got, _ := s.GetString("pcie_root"); got != "0000:00:1c.0,00.0" {
		t.Errorf("pcie_root %q", got)
	}
	var slots int
	for _, c := range back.Root.Children {
		if len(c.Name) > 10 && c.Name[:10] == "nvme_slot@" {
			slots++
		}
	}
	if slots != 2 || back.Root.Child("internal_slot@1") == nil {
		t.Errorf("children %v", back.Root.Children)
	}
	if (&Tree{Root: &Node{}}).SetNVMeSlots(nil) != 0 {
		t.Error("wrote slots with no controllers")
	}
}
