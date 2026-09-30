//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Getting onto the network from nothing.
//
// The loader environment has none of a distribution's backbone: no udev to load
// a driver when a card appears, no dhclient, no ifupdown. What it has is the
// three things all of those sit on top of - the drivers already in the ramdisk,
// the ioctl that brings an interface up, and a protocol that fits on one page.
//
// DHCP is the interesting part. A machine with no address cannot open an
// ordinary socket to ask for one, so the request is bound to an interface
// rather than to an address. SO_BINDTODEVICE lets an address-less socket
// broadcast over one card, and DHCP was designed to crawl out through exactly
// that gap.
//
// 무에서부터 네트워크 붙기.
//
// 로더 환경엔 배포판 backbone 이 없다: 카드가 붙으면 드라이버를 로드해주는
// udev 도 없고, dhclient 도 없고, ifupdown 도 없다. 있는 건 저 도구들이
// 다 그 위에 얹혀 있는 세 가지 - 램디스크에 이미 있는 드라이버, 인터페이스를
// up 시키는 ioctl, 그리고 페이지 하나에 담기는 프로토콜.
//
// DHCP 가 흥미로운 부분이다. 주소 없는 머신은 주소를 요청할 일반 소켓을
// 못 여니, 요청이 주소가 아니라 인터페이스에 bind 돼서 나간다.
// SO_BINDTODEVICE 는 주소 없는 소켓이 한 카드 위에서 broadcast 할 수 있게
// 해주는데, DHCP 는 원래 이 틈으로 기어나오도록 설계됐다.

// The BOOTP and DHCP constants, from RFC 2131 and RFC 2132.
// BOOTP 와 DHCP 상수. RFC 2131 과 RFC 2132 의 값.
const (
	dhcpClientPort = 68
	dhcpServerPort = 67
	dhcpMagic      = 0x63825363

	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpAck      = 5

	optSubnetMask   = 1
	optRouter       = 3
	optDNS          = 6
	optRequestedIP  = 50
	optMessageType  = 53
	optServerID     = 54
	optParamRequest = 55
	optEnd          = 255
)

// Lease is what the network answered with.
// Lease - 네트워크가 돌려준 응답.
type Lease struct {
	Interface string
	Address   net.IP
	Mask      net.IPMask
	Router    net.IP
	DNS       []net.IP
}

// String is the one-line form written to the boot log.
// String - 부팅 로그에 찍는 한 줄 형태.
func (l Lease) String() string {
	ones, _ := l.Mask.Size()
	s := fmt.Sprintf("%s: %s/%d", l.Interface, l.Address, ones)
	if l.Router != nil {
		s += " via " + l.Router.String()
	}
	return s
}

// bringUpNetwork finds the cards, raises them and asks for an address.
//
// Every card is tried in order, not just the first. On a machine with several
// of them, the one with a cable in it is not necessarily the one the kernel
// numbered 0.
//
// bringUpNetwork - 카드를 찾아 up 시키고 주소를 요청한다.
//
// 첫 카드만이 아니라 모든 카드를 순서대로 시도한다. 카드가 여러 개인
// 머신에서 케이블이 꽂힌 카드가 반드시 커널이 0 번으로 매긴 카드는 아니다.
func bringUpNetwork(timeout time.Duration) (*Lease, error) {
	names, err := interfaces()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("no network interfaces; the driver for this card is not loaded")
	}

	var last error
	for _, name := range names {
		if err := setUp(name); err != nil {
			last = fmt.Errorf("%s: %w", name, err)
			continue
		}
		// A card that has just come up has not finished negotiating with the
		// switch, and a request sent into that gap is simply lost.
		//
		// 방금 올라온 카드는 스위치와의 협상이 아직 안 끝났고, 그 틈에
		// 보낸 요청은 그냥 사라진다.
		if !waitForCarrier(name, 8*time.Second) {
			last = fmt.Errorf("%s: no cable", name)
			continue
		}
		lease, err := dhcp(name, timeout)
		if err != nil {
			last = fmt.Errorf("%s: %w", name, err)
			continue
		}
		if err := configure(lease); err != nil {
			return nil, err
		}
		return lease, nil
	}
	return nil, last
}

// interfaces lists the real cards, loopback excluded, in kernel order.
// interfaces - 실제 카드 목록. loopback 은 빼고 커널 순서대로.
func interfaces() ([]string, error) {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Name() == "lo" {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// waitForCarrier waits for the card to report a cable, up to d.
// waitForCarrier - 카드가 케이블을 보고할 때까지 최대 d 만큼 기다린다.
func waitForCarrier(name string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile("/sys/class/net/" + name + "/carrier"); err == nil {
			if strings.TrimSpace(string(b)) == "1" {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// ifreq is the structure every network ioctl takes: a name and one union.
// ifreq - 모든 네트워크 ioctl 이 받는 구조체. 이름 하나와 공용체 하나.
type ifreq struct {
	Name [16]byte
	Data [24]byte
}

// ioctlIface makes one interface ioctl.
// ioctlIface - 인터페이스 ioctl 한 번.
func ioctlIface(fd int, req uintptr, r *ifreq) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(r)))
	if errno != 0 {
		return errno
	}
	return nil
}

// newIfreq is an ifreq with the interface name filled in.
// newIfreq - 인터페이스 이름을 채운 ifreq.
func newIfreq(name string) *ifreq {
	var r ifreq
	copy(r.Name[:], name)
	return &r
}

// setUp raises the interface, the way "ip link set dev X up" does.
// setUp - 인터페이스를 올린다. "ip link set dev X up" 과 같은 일.
func setUp(name string) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	r := newIfreq(name)
	if err := ioctlIface(fd, syscall.SIOCGIFFLAGS, r); err != nil {
		return err
	}
	flags := binary.LittleEndian.Uint16(r.Data[:2])
	binary.LittleEndian.PutUint16(r.Data[:2], flags|syscall.IFF_UP|syscall.IFF_RUNNING)
	return ioctlIface(fd, syscall.SIOCSIFFLAGS, r)
}

// configure puts the lease on the interface and adds the default route.
// configure - 받은 lease 를 인터페이스에 얹고 기본 경로를 더한다.
func configure(l *Lease) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	set := func(req uintptr, ip net.IP) error {
		r := newIfreq(l.Interface)
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&r.Data[0]))
		sa.Family = syscall.AF_INET
		copy(sa.Addr[:], ip.To4())
		return ioctlIface(fd, req, r)
	}
	if err := set(syscall.SIOCSIFADDR, l.Address); err != nil {
		return fmt.Errorf("set address: %w", err)
	}
	if err := set(syscall.SIOCSIFNETMASK, net.IP(l.Mask)); err != nil {
		return fmt.Errorf("set netmask: %w", err)
	}
	if l.Router != nil {
		if err := addDefaultRoute(fd, l.Router); err != nil {
			return fmt.Errorf("set route: %w", err)
		}
	}
	writeResolvConf(l.DNS)
	return nil
}

// rtentry is what SIOCADDRT takes.
// rtentry - SIOCADDRT 가 받는 구조체.
type rtentry struct {
	pad0    uint64
	dst     syscall.RawSockaddrInet4
	gateway syscall.RawSockaddrInet4
	genmask syscall.RawSockaddrInet4
	flags   uint16
	pad1    [6]byte
	pad2    uint64
	pad3    uint64
	pad4    uint8
	pad5    [3]byte
	dev     uintptr
	mtu     uint64
	window  uint64
	irtt    uint16
	pad6    [6]byte
}

// addDefaultRoute adds the 0.0.0.0/0 route through the gateway.
// addDefaultRoute - 게이트웨이를 통하는 0.0.0.0/0 경로를 더한다.
func addDefaultRoute(fd int, gw net.IP) error {
	const rtfUp, rtfGateway = 0x0001, 0x0002
	var r rtentry
	r.dst.Family = syscall.AF_INET
	r.genmask.Family = syscall.AF_INET
	r.gateway.Family = syscall.AF_INET
	copy(r.gateway.Addr[:], gw.To4())
	r.flags = rtfUp | rtfGateway
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCADDRT, uintptr(unsafe.Pointer(&r)))
	if errno != 0 {
		return errno
	}
	return nil
}

// writeResolvConf writes /etc/resolv.conf, since name resolution has to work
// before anything can be downloaded.
//
// writeResolvConf - /etc/resolv.conf 를 쓴다. 뭔가 받으려면 이름 해석부터
// 돼야 한다.
func writeResolvConf(servers []net.IP) {
	if len(servers) == 0 {
		return
	}
	var b strings.Builder
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	_ = os.MkdirAll("/etc", 0o755)
	_ = os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644)
}

// dhcp asks for an address on one interface and waits for an answer.
// dhcp - 인터페이스 하나 위에서 주소를 요청하고 응답을 기다린다.
func dhcp(name string, timeout time.Duration) (*Lease, error) {
	hw, err := hardwareAddr(name)
	if err != nil {
		return nil, err
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)

	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
		return nil, err
	}
	// The socket has no address yet, so it is tied to the card instead. This
	// is the part that makes asking possible before there is anything to ask
	// with.
	//
	// 소켓에는 아직 주소가 없으니 대신 카드에 묶는다. 요청할 수단이 생기기
	// 전에 요청을 가능하게 하는 게 바로 이 부분이다.
	if err := syscall.SetsockoptString(fd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, name); err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: dhcpClientPort}); err != nil {
		return nil, err
	}

	xid := rand.Uint32()
	deadline := time.Now().Add(timeout)

	offer, err := exchange(fd, hw, xid, dhcpDiscover, nil, nil, deadline)
	if err != nil {
		return nil, fmt.Errorf("no offer: %w", err)
	}
	ack, err := exchange(fd, hw, xid, dhcpRequest, offer.Address, offer.serverID, deadline)
	if err != nil {
		return nil, fmt.Errorf("offer not confirmed: %w", err)
	}
	ack.Interface = name
	if ack.Mask == nil {
		ack.Mask = net.CIDRMask(24, 32)
	}
	return &ack.Lease, nil
}

type reply struct {
	Lease
	serverID net.IP
}

// exchange sends one message and waits for the matching answer.
// exchange - 메시지 하나를 보내고 짝이 맞는 응답을 기다린다.
func exchange(fd int, hw net.HardwareAddr, xid uint32, kind byte, requested, serverID net.IP, deadline time.Time) (*reply, error) {
	msg := buildMessage(hw, xid, kind, requested, serverID)
	to := &syscall.SockaddrInet4{Port: dhcpServerPort, Addr: [4]byte{255, 255, 255, 255}}

	want := byte(dhcpOffer)
	if kind == dhcpRequest {
		want = dhcpAck
	}

	buf := make([]byte, 1500)
	for attempt := 0; attempt < 4 && time.Now().Before(deadline); attempt++ {
		if err := syscall.Sendto(fd, msg, 0, to); err != nil {
			return nil, err
		}
		wait := time.Duration(1<<attempt) * time.Second
		if err := setRecvTimeout(fd, wait); err != nil {
			return nil, err
		}
		for {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				break // timed out; send again / 시간 초과. 다시 보낸다
			}
			r, ok := parseReply(buf[:n], xid, want)
			if ok {
				return r, nil
			}
		}
	}
	return nil, errors.New("no answer")
}

// setRecvTimeout caps how long a receive blocks, so one lost packet does not
// hang the boot.
//
// setRecvTimeout - 수신이 멈춰 있을 시간에 상한을 둔다. 패킷 하나를
// 잃었다고 부팅이 멎지 않게 하려는 것이다.
func setRecvTimeout(fd int, d time.Duration) error {
	tv := syscall.NsecToTimeval(int64(d))
	return syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
}

// buildMessage writes a BOOTP packet with the DHCP options this needs.
// buildMessage - 여기 필요한 DHCP 옵션을 담은 BOOTP 패킷을 만든다.
func buildMessage(hw net.HardwareAddr, xid uint32, kind byte, requested, serverID net.IP) []byte {
	b := make([]byte, 240, 300)
	b[0] = 1 // request / 요청
	b[1] = 1 // ethernet / 이더넷
	b[2] = 6 // address length / 주소 길이
	binary.BigEndian.PutUint32(b[4:], xid)
	b[10] = 0x80 // answer by broadcast; no address to unicast to yet / 응답을 broadcast 로. unicast 로 받을 주소가 아직 없다
	copy(b[28:], hw)
	binary.BigEndian.PutUint32(b[236:], dhcpMagic)

	b = append(b, optMessageType, 1, kind)
	if requested != nil {
		b = append(b, optRequestedIP, 4)
		b = append(b, requested.To4()...)
	}
	if serverID != nil {
		b = append(b, optServerID, 4)
		b = append(b, serverID.To4()...)
	}
	b = append(b, optParamRequest, 3, optSubnetMask, optRouter, optDNS)
	b = append(b, optEnd)
	return b
}

// parseReply pulls the fields of interest out of an answer, returning false
// when it is not the answer this exchange was waiting for.
//
// parseReply - 응답에서 관심 필드를 뽑는다. 이 교환이 기다리던 응답이
// 아니면 false 를 돌려준다.
func parseReply(b []byte, xid uint32, want byte) (*reply, bool) {
	if len(b) < 240 || b[0] != 2 || binary.BigEndian.Uint32(b[4:]) != xid {
		return nil, false
	}
	if binary.BigEndian.Uint32(b[236:]) != dhcpMagic {
		return nil, false
	}

	r := &reply{}
	r.Address = net.IP(append([]byte(nil), b[16:20]...))

	var kind byte
	for i := 240; i < len(b); {
		opt := b[i]
		if opt == optEnd {
			break
		}
		if opt == 0 {
			i++
			continue
		}
		if i+1 >= len(b) {
			break
		}
		n := int(b[i+1])
		if i+2+n > len(b) {
			break
		}
		v := b[i+2 : i+2+n]
		switch opt {
		case optMessageType:
			kind = v[0]
		case optSubnetMask:
			if n == 4 {
				r.Mask = net.IPMask(append([]byte(nil), v...))
			}
		case optRouter:
			if n >= 4 {
				r.Router = net.IP(append([]byte(nil), v[:4]...))
			}
		case optDNS:
			for j := 0; j+4 <= n; j += 4 {
				r.DNS = append(r.DNS, net.IP(append([]byte(nil), v[j:j+4]...)))
			}
		case optServerID:
			if n == 4 {
				r.serverID = net.IP(append([]byte(nil), v...))
			}
		}
		i += 2 + n
	}
	if kind != want {
		return nil, false
	}
	return r, true
}

// hardwareAddr is the card's MAC address, which the DHCP request needs.
// hardwareAddr - 카드의 MAC 주소. DHCP 요청에 필요하다.
func hardwareAddr(name string) (net.HardwareAddr, error) {
	b, err := os.ReadFile("/sys/class/net/" + name + "/address")
	if err != nil {
		return nil, err
	}
	return net.ParseMAC(strings.TrimSpace(string(b)))
}
