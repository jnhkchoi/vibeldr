//go:build !linux

package hwscan

// permAddr has no way to ask on other systems; cards are then named by their
// PCI position.
//
// permAddr - 다른 시스템에서는 물을 방법이 없다. 그때 카드는 PCI 위치로 부른다.
func permAddr(iface string) string { return "" }
