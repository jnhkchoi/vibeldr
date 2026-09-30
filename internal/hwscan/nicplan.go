package hwscan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The network card order DSM sees: which card is LAN 1 (eth0), LAN 2 (eth1)
// and so on.
//
// DSM names its cards in the order their drivers load in its ramdisk: its own
// drivers first, the driver pack's after them. On a machine that mixes vendors
// that order has nothing to do with where the cards sit, and it is not the
// order the install wizard sees either - it runs another kernel with other
// drivers. The wizard therefore records the order the user wants, by card, and
// the helper renames the interfaces to match before the MAC addresses go in
// and before DSM brings any of them up.
//
// A card is named by its permanent MAC address, which the kernel keeps apart
// from the current one: it is the same under both kernels, it survives the
// loader writing another MAC onto the card, and it moves with the card to
// another PCI slot or USB port. A driver that reports none leaves the card
// named by its PCI position instead (NICPosition), so for that card a move
// does change who it is. Either way a card that is no longer found only gives
// up its place: the boot goes on, and the cards left take the LAN numbers in
// order.
//
// DSM 이 보는 랜카드 순서. 어느 카드가 LAN 1(eth0), LAN 2(eth1) … 인가.
//
// DSM 은 자기 램디스크에서 드라이버가 올라온 순서로 카드 이름을 붙인다. 자기
// 드라이버가 먼저, 드라이버 팩 것이 그 뒤다. 제조사가 섞인 기계에서는 그 순서가
// 카드가 꽂힌 자리와 상관이 없고, 설치 마법사가 보는 순서와도 다르다 - 마법사는
// 다른 커널과 다른 드라이버로 돈다. 그래서 마법사가 사용자가 원하는 순서를
// 카드별로 적어 두고, 헬퍼가 MAC 을 넣기 전이자 DSM 이 인터페이스를 올리기 전에
// 그 순서대로 이름을 바꾼다.
//
// 카드는 영구 MAC 주소로 부른다. 커널은 이것을 지금 주소와 따로 들고 있어서, 두
// 커널에서 같고, 로더가 카드에 다른 MAC 을 써도 그대로이며, 카드를 다른 PCI
// 슬롯이나 USB 포트로 옮겨도 따라간다. 영구 주소를 알려 주지 않는 드라이버의
// 카드는 PCI 위치(NICPosition)로 부르므로, 그 카드는 옮기면 다른 카드가 된다.
// 어느 쪽이든 더는 찾을 수 없는 카드는 자리만 내놓는다. 부팅은 계속되고, 남은
// 카드들이 LAN 번호를 차례로 받는다.

// NICPlanName is the file the plan travels in, at the ramdisk root.
// NICPlanName - 계획이 실려 가는 파일. 램디스크 루트에 있다.
const NICPlanName = "vibeldr-nic-plan"

var (
	pciAddr = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	usbPort = regexp.MustCompile(`^[0-9]+-[0-9.]+$`)
)

// NICKey names the card behind a network interface: "mac:" and its permanent
// MAC when the driver reports one, its PCI position otherwise. sysfs is the
// sysfs root; the permanent address is only asked on the running system's own
// ("/sys"). An interface with no device (a bridge, a tunnel) has no key.
//
// NICKey - 네트워크 인터페이스 뒤의 카드를 부르는 이름. 드라이버가 영구 MAC 을
// 알려 주면 "mac:" 과 그 주소, 아니면 PCI 위치다. sysfs 는 sysfs 루트이고, 영구
// 주소는 도는 시스템 자신의 것("/sys")일 때만 묻는다. 장치가 없는
// 인터페이스(브리지, 터널)는 이름이 없다.
func NICKey(sysfs, iface string) string {
	pos := NICPosition(sysfs, iface)
	if pos == "" {
		return ""
	}
	if sysfs == DefaultSysfs {
		if mac := permAddr(iface); mac != "" {
			return "mac:" + mac
		}
	}
	return pos
}

// NICPosition is where the card behind a network interface sits: its PCI
// address, and the USB port after it for a USB card. Sorting by it gives the
// cards in slot order.
//
// NICPosition - 네트워크 인터페이스 뒤의 카드가 꽂힌 자리. PCI 주소이고, USB
// 카드면 그 뒤에 USB 포트가 붙는다. 이것으로 정렬하면 슬롯 순서가 된다.
func NICPosition(sysfs, iface string) string {
	p, err := filepath.EvalSymlinks(filepath.Join(sysfs, "class", "net", iface, "device"))
	if err != nil {
		return ""
	}
	return nicKeyFromPath(filepath.ToSlash(p))
}

// nicKeyFromPath takes the last PCI address in a device path, and after it the
// USB port the card hangs from, if any.
//
// nicKeyFromPath - 장치 경로의 마지막 PCI 주소와, 그 뒤에 카드가 매달린 USB
// 포트가 있으면 그것을 취한다.
func nicKeyFromPath(p string) string {
	parts := strings.Split(p, "/")
	last := -1
	for i, s := range parts {
		if pciAddr.MatchString(s) {
			last = i
		}
	}
	if last < 0 {
		return ""
	}
	key := parts[last]
	for _, s := range parts[last+1:] {
		if usbPort.MatchString(s) {
			key += "/" + s
		}
	}
	return key
}

// OrderNICs returns keys in LAN order: first those plan names, in its order,
// then the rest in the order given (the caller sorts them by slot), so a new
// card goes after the ones already placed. Plan entries for cards that are not
// here are dropped.
//
// OrderNICs - keys 를 LAN 순서로 돌려준다. 먼저 plan 에 적힌 것을 그 순서대로,
// 나머지는 받은 순서대로(호출자가 슬롯 순으로 정렬해 준다) 뒤에 둔다. 그래서 새
// 카드는 이미 자리 잡은 카드 뒤로 간다. 여기 없는 카드를 적은 plan 항목은 버린다.
func OrderNICs(keys, plan []string) []string {
	present := map[string]bool{}
	for _, k := range keys {
		present[k] = true
	}
	var out []string
	used := map[string]bool{}
	for _, k := range plan {
		if present[k] && !used[k] {
			out = append(out, k)
			used[k] = true
		}
	}
	var rest []string
	for _, k := range keys {
		if !used[k] {
			rest = append(rest, k)
			used[k] = true
		}
	}
	return append(out, rest...)
}

// RenderNICPlan writes the plan, one key per line in LAN order.
// RenderNICPlan - 계획을 LAN 순서로 한 줄에 키 하나씩 쓴다.
func RenderNICPlan(order []string) []byte {
	var b strings.Builder
	for _, k := range order {
		if k = strings.TrimSpace(k); k != "" {
			b.WriteString(k + "\n")
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return []byte(b.String())
}

// ParseNICPlan reads what RenderNICPlan wrote.
// ParseNICPlan - RenderNICPlan 이 쓴 것을 읽는다.
func ParseNICPlan(raw string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// NICRename is one interface rename.
// NICRename - 인터페이스 이름 바꾸기 하나.
type NICRename struct{ From, To string }

// NICRenames works out the renames that give the cards eth0, eth1, ... in plan
// order. current maps each ethN interface present to its card key; cards the
// plan does not name keep their relative order after the planned ones.
//
// Every rename goes through a temporary name first (vibeldrN), since the name
// a card wants can still belong to another card. Nothing is returned when the
// cards already have the names they should.
//
// NICRenames - 카드들이 plan 순서로 eth0, eth1, … 을 갖게 하는 이름 바꾸기를
// 구한다. current 는 지금 있는 ethN 인터페이스마다 카드 키를 준다. plan 에 없는
// 카드는 계획된 카드들 뒤에서 서로의 순서를 지킨다.
//
// 카드가 원하는 이름을 아직 다른 카드가 갖고 있을 수 있어서, 모든 바꾸기는 먼저
// 임시 이름(vibeldrN)을 거친다. 카드들이 이미 제 이름을 갖고 있으면 아무것도
// 돌려주지 않는다.
func NICRenames(current map[string]string, plan []string) []NICRename {
	names := make([]string, 0, len(current))
	for n := range current {
		names = append(names, n)
	}
	// Current order: by the number in ethN.
	// 지금 순서: ethN 의 번호 순.
	sort.Slice(names, func(i, j int) bool {
		a, b := ethNumber(names[i]), ethNumber(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	byKey := map[string]string{}
	for _, n := range names {
		if k := current[n]; k != "" {
			if _, dup := byKey[k]; !dup {
				byKey[k] = n
			}
		}
	}
	var final []string
	placed := map[string]bool{}
	for _, k := range plan {
		if n, ok := byKey[k]; ok && !placed[n] {
			final = append(final, n)
			placed[n] = true
		}
	}
	for _, n := range names {
		if !placed[n] {
			final = append(final, n)
		}
	}
	same := true
	for i, n := range final {
		if n != fmt.Sprintf("eth%d", i) {
			same = false
			break
		}
	}
	if same {
		return nil
	}
	var out []NICRename
	for i, n := range final {
		out = append(out, NICRename{From: n, To: fmt.Sprintf("vibeldr%d", i)})
	}
	for i := range final {
		out = append(out, NICRename{From: fmt.Sprintf("vibeldr%d", i), To: fmt.Sprintf("eth%d", i)})
	}
	return out
}

// ethNumber is the N of ethN, or a large number for any other name.
// ethNumber - ethN 의 N. 다른 이름이면 큰 수.
func ethNumber(name string) int {
	var n int
	if _, err := fmt.Sscanf(name, "eth%d", &n); err != nil || fmt.Sprintf("eth%d", n) != name {
		return 1 << 20
	}
	return n
}

// MACTargets says which interface each of n MAC addresses goes to. With a plan,
// address i belongs to the card the plan puts at position i - found through
// keys (interface -> card key) whatever the interface is called now - and is
// "" when that card is not here. Without a plan, address i goes to ifaces[i].
//
// MACTargets - n 개의 MAC 주소가 각각 어느 인터페이스로 갈지. 계획이 있으면 i 번
// 주소는 계획의 i 번째 카드 몫이고, 그 인터페이스 이름이 지금 무엇이든
// keys(인터페이스 -> 카드 키)로 찾는다. 그 카드가 없으면 "" 다. 계획이 없으면 i 번
// 주소는 ifaces[i] 로 간다.
func MACTargets(ifaces []string, keys map[string]string, plan []string, n int) []string {
	out := make([]string, n)
	if len(plan) == 0 {
		copy(out, ifaces)
		return out
	}
	byKey := map[string]string{}
	for _, name := range ifaces {
		if k := keys[name]; k != "" {
			byKey[k] = name
		}
	}
	for i := 0; i < n && i < len(plan); i++ {
		out[i] = byKey[plan[i]]
	}
	return out
}
