//go:build linux

// hwmac_linux.go writes the chosen MACs onto the network cards for real.
//
// Why it is needed:
//
//	The mac1..mac4 kernel parameters are only DSM's "identity MACs". The
//	kernel keeps them in /proc/sys/kernel/syno_mac_address1 and uses them for
//	QuickConnect, licensing and Synology Assistant matching; it never touches
//	the address a card actually puts on the wire. So changing mac1 alone
//	leaves the factory MAC showing in the router's DHCP list and in
//	find.synology.
//
//	The point of setting a MAC by hand is usually to be seen as that MAC on
//	the network, so here the link is taken down for a moment, the hardware
//	address itself is overwritten with SIOCSIFHWADDR, and the link goes back
//	up. That is what `ip link set dev ethN address ...` does; it is done
//	through the ioctl directly so the ramdisk needs neither ip nor ifconfig.
//
// When it runs:
//
//	Right after all the drivers are up and before DSM brings the network up.
//	Written in at that point, the same kernel carries it across the pivot and
//	it survives into the installed DSM. In case a driver is reloaded in
//	between, the -agent stage checks once more.
//
// They are attached in order: mac1 -> eth0, mac2 -> eth1 and so on. The order
// the kernel registered the cards in is the number in the name, and DSM counts
// mac1..N in that same order.
//
// hwmac_linux.go - 지정한 MAC 을 랜카드에 실제로 박는다.
//
// 왜 필요한가:
//
//	커널 파라미터 mac1..mac4 는 DSM 의 "정체성 MAC" 일 뿐이다. 커널은 이를
//	/proc/sys/kernel/syno_mac_address1 등에 두고 QuickConnect, 라이선스,
//	Synology Assistant 매칭에 쓰며, 랜카드가 선로에 실제로 내보내는 주소는
//	건드리지 않는다. 그래서 mac1 만 바꾸면 공유기 DHCP 목록과
//	find.synology 에는 공장 MAC 이 그대로 보인다.
//
//	MAC 을 손으로 정하는 목적은 보통 네트워크에서 그 MAC 으로 보이는 것이라,
//	여기서 링크를 잠깐 내리고 SIOCSIFHWADDR 로 하드웨어 주소 자체를 덮어쓴
//	뒤 다시 올린다. `ip link set dev ethN address ...` 와 같은 일이고,
//	램디스크에 ip 도 ifconfig 도 필요 없게 ioctl 을 직접 부른다.
//
// 언제 도는가:
//
//	드라이버를 다 올린 직후, DSM 이 네트워크를 세우기 전이다. 이 시점에
//	박아두면 같은 커널이 그대로 pivot 을 넘어가므로 설치된 DSM 까지
//	유지된다. 도중에 드라이버가 다시 로드되는 경우를 대비해 -agent 단계에서
//	한 번 더 확인한다.
//
// mac1 -> eth0, mac2 -> eth1 … 순서로 붙인다. 커널이 카드를 등록한 순서가
// 곧 이름의 숫자이고, DSM 도 같은 순서로 mac1..N 을 센다.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// macCount is how many MAC slots the kernel command line carries (mac1..mac4).
// macCount - 커널 커맨드라인이 싣고 다니는 MAC 슬롯 개수 (mac1..mac4).
const macCount = 4

// ifreq is the interface request structure the ioctls take. Data is a union and
// is read differently per request: a sockaddr for SIOCxIFHWADDR, the first two
// bytes as flags for SIOCxIFFLAGS.
//
// ifreq - ioctl 이 주고받는 인터페이스 요청 구조체. Data 는 union 이라
// 요청마다 다르게 읽는다: SIOCxIFHWADDR 이면 sockaddr, SIOCxIFFLAGS 면
// 앞 2바이트가 플래그.
type ifreq struct {
	Name [16]byte
	Data [24]byte
}

func newIfreq(name string) *ifreq {
	var r ifreq
	copy(r.Name[:], name)
	return &r
}

func ifreqIoctl(fd int, req uintptr, r *ifreq) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(r)))
	if errno != 0 {
		return errno
	}
	return nil
}

// applyHardwareMACs writes the command line's mac1..N onto ethN in order.
//
// It never fails the boot. A missing slot, a missing card, or a driver refusing
// the address change is logged to the console and passed over - the machine
// still boots on its factory MACs.
//
// applyHardwareMACs - 커맨드라인의 mac1..N 을 순서대로 ethN 에 박는다.
//
// 부팅을 실패시키지 않는다. 슬롯이 없거나, 카드가 없거나, 드라이버가
// 주소 변경을 거부하면 콘솔에 남기고 그냥 넘어간다 - 그래도 공장 MAC 으로는
// 부팅되기 때문이다.
func applyHardwareMACs() {
	want := cmdlineMACs()
	if len(want) == 0 {
		return
	}
	ifaces := ethInterfaces()
	if len(ifaces) == 0 {
		logf("mac: no network card to apply it to")
		return
	}
	for i, mac := range want {
		if i >= len(ifaces) {
			// The command line carrying more MACs than there are cards is
			// normal - the model's default netif_num can be larger than what
			// is actually fitted. End quietly.
			//
			// 커맨드라인이 카드보다 많은 MAC 을 싣고 있는 건 정상이다
			// (모델 기본 netif_num 이 실제 장착 수보다 큰 경우). 조용히 끝낸다.
			break
		}
		name := ifaces[i]
		cur, err := getHWAddr(name)
		if err != nil {
			logf("mac: %s cannot read the current address (%v)", name, err)
			continue
		}
		if cur == mac {
			logf("mac: %s already %s", name, prettyHW(mac))
			continue
		}
		if err := setHWAddr(name, mac); err != nil {
			logf("mac: %s %s -> %s failed (%v)", name, prettyHW(cur), prettyHW(mac), err)
			continue
		}
		logf("mac: %s %s -> %s", name, prettyHW(cur), prettyHW(mac))
	}
}

// cmdlineMACs reads mac1..mac4 in slot order, stopping at the first gap. A
// layout with mac2 but no mac1 means nothing, and pushing it through anyway
// would put addresses on the wrong cards.
//
// cmdlineMACs - mac1..mac4 를 슬롯 순서대로 읽는다. 중간이 비면 거기서 끊는다.
// mac1 없이 mac2 만 있는 배치는 의미가 없고, 그 상태로 밀어 넣으면 엉뚱한
// 카드에 붙는다.
func cmdlineMACs() [][6]byte {
	var out [][6]byte
	for i := 1; i <= macCount; i++ {
		v := cmdlineValue(fmt.Sprintf("mac%d", i), "")
		if v == "" {
			break
		}
		mac, err := parseHW(v)
		if err != nil {
			logf("mac: cannot read mac%d=%q (%v)", i, v, err)
			break
		}
		out = append(out, mac)
	}
	return out
}

// ethInterfaces lists the interfaces with a real device behind them, in the
// order the kernel registered them.
//
// /sys/class/net also holds lo and the virtual interfaces, and those have no
// device symlink, so they fall out naturally. What is left is named ethN, and
// sorting on the trailing number gives the registration order - the same order
// DSM counts mac1..N in.
//
// ethInterfaces - 실제 장치가 붙어 있는 인터페이스를 커널 등록 순서로 준다.
//
// /sys/class/net 에는 lo 나 가상 인터페이스도 섞여 있는데 그것들은 device
// 심볼릭 링크가 없어서 자연스럽게 걸러진다. 남은 이름은 ethN 형태이므로
// 뒤 숫자로 정렬하면 등록 순서 (= DSM 이 mac1..N 을 세는 순서) 가 된다.
func ethInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if n == "lo" {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + n + "/device"); err != nil {
			continue
		}
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		ai, aok := ifIndexOf(names[i])
		bi, bok := ifIndexOf(names[j])
		if aok && bok && ai != bi {
			return ai < bi
		}
		return names[i] < names[j]
	})
	return names
}

// ifIndexOf takes the 3 out of "eth3". With no trailing number it reports false
// and the name falls back to alphabetical order.
//
// ifIndexOf - "eth3" 에서 3 을 뽑는다. 숫자 꼬리가 없으면 (false) 이름순으로
// 밀린다.
func ifIndexOf(name string) (int, bool) {
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	if i == len(name) {
		return 0, false
	}
	n, err := strconv.Atoi(name[i:])
	if err != nil {
		return 0, false
	}
	return n, true
}

func getHWAddr(name string) ([6]byte, error) {
	var mac [6]byte
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return mac, err
	}
	defer syscall.Close(fd)

	r := newIfreq(name)
	if err := ifreqIoctl(fd, syscall.SIOCGIFHWADDR, r); err != nil {
		return mac, err
	}
	// Read Data as a sockaddr: the first two bytes are sa_family, the rest
	// sa_data.
	//
	// Data 를 sockaddr 로 읽는다: 앞 2바이트가 sa_family, 그 뒤가 sa_data.
	copy(mac[:], r.Data[2:8])
	return mac, nil
}

// setHWAddr takes the link down, changes the address and brings it back up.
//
// A good many drivers refuse an address change while the interface is up,
// with EBUSY. Some, virtio_net among them, allow it, but there is no reason to
// treat those separately, so it always goes down and back up. One that was
// already down is not brought up - there is no reason to wake a card DSM has
// decided not to use yet.
//
// setHWAddr - 링크를 내리고 주소를 바꾼 뒤 다시 올린다.
//
// 상당수 드라이버가 인터페이스가 올라가 있는 동안의 주소 변경을 EBUSY 로
// 거절한다. virtio_net 처럼 허용하는 드라이버도 있지만 구분해서 다룰 이유가
// 없어 항상 내렸다 올린다. 원래 내려가 있었으면 올리지 않는다 - DSM 이
// 아직 쓰지 않기로 한 카드를 우리가 깨울 이유가 없다.
func setHWAddr(name string, mac [6]byte) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	wasUp, err := linkIsUp(fd, name)
	if err != nil {
		return fmt.Errorf("read flags: %w", err)
	}
	if wasUp {
		if err := setLink(fd, name, false); err != nil {
			return fmt.Errorf("link down: %w", err)
		}
	}

	r := newIfreq(name)
	binary.LittleEndian.PutUint16(r.Data[:2], syscall.ARPHRD_ETHER)
	copy(r.Data[2:8], mac[:])
	setErr := ifreqIoctl(fd, syscall.SIOCSIFHWADDR, r)

	if wasUp {
		// Even where the address change failed, the link is always put back
		// as it was. Stopping here would mean we cut the network ourselves.
		//
		// 주소 변경이 실패했더라도 링크는 반드시 원래대로 되돌린다.
		// 여기서 멈추면 우리가 네트워크를 끊어버린 꼴이 된다.
		if upErr := setLink(fd, name, true); upErr != nil && setErr == nil {
			return fmt.Errorf("link up: %w", upErr)
		}
	}
	return setErr
}

func linkIsUp(fd int, name string) (bool, error) {
	r := newIfreq(name)
	if err := ifreqIoctl(fd, syscall.SIOCGIFFLAGS, r); err != nil {
		return false, err
	}
	return binary.LittleEndian.Uint16(r.Data[:2])&syscall.IFF_UP != 0, nil
}

func setLink(fd int, name string, up bool) error {
	r := newIfreq(name)
	if err := ifreqIoctl(fd, syscall.SIOCGIFFLAGS, r); err != nil {
		return err
	}
	flags := binary.LittleEndian.Uint16(r.Data[:2])
	if up {
		flags |= syscall.IFF_UP | syscall.IFF_RUNNING
	} else {
		flags &^= syscall.IFF_UP
	}
	binary.LittleEndian.PutUint16(r.Data[:2], flags)
	return ifreqIoctl(fd, syscall.SIOCSIFFLAGS, r)
}

// parseHW takes a MAC as the command line writes it. Synology's form is twelve
// hex digits with no separator, but a human writing it with colons, dashes,
// dots or spaces is read too.
//
// parseHW - 커맨드라인의 MAC 표기를 받는다. 시놀로지 형식은 구분자 없는
// 12 자리 16진수지만, 사람이 콜론·하이픈·점·공백을 넣어 적어도 읽히게 한다.
func parseHW(s string) ([6]byte, error) {
	var mac [6]byte
	cleaned := strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(s))
	if len(cleaned) != 12 {
		return mac, fmt.Errorf("not 12 hex digits")
	}
	b, err := hex.DecodeString(cleaned)
	if err != nil {
		return mac, err
	}
	copy(mac[:], b)
	return mac, nil
}

func prettyHW(mac [6]byte) string {
	parts := make([]string, 6)
	for i, b := range mac {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
