//go:build linux

package main

import (
	"os"
	"strings"
	"syscall"

	"vibeldr/internal/hwscan"
)

// applyNICPlan renames the network interfaces into the order the wizard
// settled (hwscan/nicplan.go), so DSM's LAN 1, LAN 2 ... are the cards the
// user picked. It runs once every card's driver has loaded, before the MAC
// addresses go in and before DSM brings any interface up. An interface has to
// be down to be renamed, so if any card is up already the plan is not applied.
// A rename that fails undoes the ones before it.
//
// With no plan in the ramdisk nothing changes, and DSM keeps its own
// driver-load order.
//
// applyNICPlan - 네트워크 인터페이스 이름을 마법사가 정한 순서(hwscan/nicplan.go)로
// 바꿔, DSM 의 LAN 1, LAN 2 … 가 사용자가 고른 카드가 되게 한다. 모든 카드의
// 드라이버가 올라온 뒤, MAC 주소를 넣기 전이자 DSM 이 인터페이스를 올리기 전에
// 돈다. 인터페이스는 내려가 있어야 이름을 바꿀 수 있어서, 이미 올라간 카드가
// 하나라도 있으면 계획을 적용하지 않는다. 바꾸기가 실패하면 앞서 한 것을
// 되돌린다.
//
// 램디스크에 계획이 없으면 아무것도 바꾸지 않고, DSM 자신의 드라이버 로드
// 순서가 남는다.
func applyNICPlan() {
	plan := nicPlan()
	if len(plan) == 0 {
		return
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		logf("nic order: %v", err)
		return
	}
	defer syscall.Close(fd)

	current := map[string]string{}
	for _, name := range ethInterfaces() {
		if !strings.HasPrefix(name, "eth") {
			continue
		}
		if up, err := linkIsUp(fd, name); err != nil || up {
			logf("nic order: %s is up, the planned order is not applied", name)
			return
		}
		current[name] = hwscan.NICKey(hwscan.DefaultSysfs, name)
	}
	renames := hwscan.NICRenames(current, plan)
	if len(renames) == 0 {
		logf("nic order: cards already in the planned order")
		return
	}
	for i, r := range renames {
		if err := renameInterface(fd, r.From, r.To); err != nil {
			logf("nic order: %s -> %s failed (%v), undoing", r.From, r.To, err)
			for j := i - 1; j >= 0; j-- {
				_ = renameInterface(fd, renames[j].To, renames[j].From)
			}
			return
		}
	}
	var got []string
	for _, name := range ethInterfaces() {
		got = append(got, name+"="+hwscan.NICKey(hwscan.DefaultSysfs, name))
	}
	logf("nic order: %s", strings.Join(got, " "))
}

// renameInterface gives an interface a new name with SIOCSIFNAME. The ifreq
// carries the new name where an address would otherwise go.
//
// renameInterface - SIOCSIFNAME 으로 인터페이스에 새 이름을 준다. ifreq 는 주소가
// 들어갈 자리에 새 이름을 싣는다.
func renameInterface(fd int, from, to string) error {
	r := newIfreq(from)
	copy(r.Data[:16], to)
	return ifreqIoctl(fd, syscall.SIOCSIFNAME, r)
}

// dsmNICPlan is where installAgent leaves a copy of the plan inside DSM, for
// the agent: the ramdisk and its copy are gone after the pivot.
//
// dsmNICPlan - installAgent 가 DSM 안에 계획 사본을 두는 곳. 에이전트용이다.
// pivot 뒤에는 램디스크와 그 사본이 없다.
const dsmNICPlan = "/etc/" + hwscan.NICPlanName

// nicPlan reads the network card plan: the ramdisk's copy, or inside DSM the
// one installAgent left. No plan comes back empty.
//
// nicPlan - 랜카드 계획을 읽는다. 램디스크의 것, DSM 안이면 installAgent 가 남긴
// 것이다. 계획이 없으면 빈 값이다.
func nicPlan() []string {
	for _, p := range []string{"/" + hwscan.NICPlanName, dsmNICPlan} {
		if raw, err := os.ReadFile(p); err == nil {
			return hwscan.ParseNICPlan(string(raw))
		}
	}
	return nil
}

// copyNICPlan leaves the ramdisk's plan inside the installed system, where
// the agent reads it (nicPlan). Without a plan any old copy is removed, so a
// rebuilt loader with no plan is not steered by a stale one.
//
// copyNICPlan - 램디스크의 계획을 설치된 시스템 안에 남긴다. 에이전트가 거기서
// 읽는다 (nicPlan). 계획이 없으면 옛 사본을 지워, 계획 없이 다시 만든 로더가 낡은
// 사본에 끌려가지 않게 한다.
func copyNICPlan(root string) {
	raw, err := os.ReadFile("/" + hwscan.NICPlanName)
	if err != nil {
		_ = os.Remove(root + dsmNICPlan)
		return
	}
	if err := os.WriteFile(root+dsmNICPlan, raw, 0o644); err != nil {
		logf("nic order: %s: %v", root+dsmNICPlan, err)
	}
}
