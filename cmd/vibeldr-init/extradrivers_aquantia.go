//go:build linux

package main

// Aquantia AQC107/113 10G / 2.5G NIC unlock.
//
// The problem: the atlantic driver in Synology's stock kernel keeps a SubID
// whitelist, so an AQC card without Synology branding matches by modalias and
// is still refused at probe. The card never comes up.
//
// The fix: the driver pack carries an atlantic.ko built from public source,
// and when the AQC vendor (0x1d6a) is present the pack's copy is loaded. It has
// no SubID check and takes every AQC card.
//
// Nothing is built here. The pack build (vibeldr-modules) makes that .ko for
// the kernel version and keeps it in place of Synology's original on purpose;
// this file only loads it.
//
// Aquantia AQC107/113 10G / 2.5G NIC unlock.
//
// 문제: Synology stock 커널의 atlantic 드라이버가 SubID 화이트리스트를
// 두고 있어서, 시놀로지 브랜드가 안 붙은 AQC 카드는 modalias 매칭이
// 되어도 probe 단계에서 거부된다. 결과적으로 카드가 안 잡힌다.
//
// 해법: 드라이버 팩에 공개 소스로 빌드한 atlantic.ko 가 들어 있고, AQC 벤더
// (0x1d6a) 가 보이면 팩본을 로드한다. 팩본은 SubID 체크가 없어서 모든 AQC
// 카드를 잡는다.
//
// 여기서 빌드하는 것은 없다. 팩 빌드 (vibeldr-modules) 가 그 커널 버전에 맞춰
// .ko 를 만들고 일부러 시놀 원본 대신 남겨 둔다. 이 파일은 로드만 한다.

import (
	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
)

// aquantiaVendor is the PCI vendor ID. Marvell bought them, but sysfs still
// reports the original Aquantia ID.
//
// aquantiaVendor - PCI vendor ID. Marvell 이 인수했지만 sysfs 는 여전히
// 원래 Aquantia ID 를 뱉는다.
const aquantiaVendor uint32 = 0x1d6a

// atlanticModule is the upstream driver's module name.
// atlanticModule - upstream 드라이버의 모듈명.
const atlanticModule = "atlantic"

// unlockAquantia loads the pack's atlantic when an AQC card is present.
//
// If an atlantic is already loaded (the stock one, say), it is left in place
// and nothing more is tried: loadModuleByName finds the name among the loaded
// modules and returns without a load. Where a real replacement is needed, the
// proper way is a blacklist kernel argument so the stock one is never loaded
// at all.
//
// unlockAquantia - AQC 카드가 있으면 팩본 atlantic 을 로드한다.
//
// atlantic 이 이미 로드돼 있으면 (stock 이든) 그대로 두고 더 시도하지 않는다.
// loadModuleByName 이 로드된 모듈 목록에서 이름을 찾고 로드 없이 돌아온다.
// 대체가 필요하면 blacklist 로 커널 인자를 넘겨 stock 을 애초에 안
// 로드시키는 게 정공법이다.
func unlockAquantia(index kmod.Index, images map[string][]byte, loaded map[string]bool) {
	devs := pciByVendor(scanPCIDevices(hwscan.DefaultSysfs), aquantiaVendor)
	if len(devs) == 0 {
		return
	}
	var addrs []string
	for _, d := range devs {
		addrs = append(addrs, d.sysfsAddress)
	}
	logf("aquantia: found %d card(s): %v", len(devs), addrs)

	if _, ok := index[atlanticModule]; !ok {
		logf("aquantia: pack has no %s.ko, skipping unlock", atlanticModule)
		return
	}
	ok, err := loadModuleByName(atlanticModule, index, images, loaded)
	switch {
	case err != nil:
		logf("aquantia: %s: %v", atlanticModule, err)
	case ok:
		logf("aquantia: loaded pack %s", atlanticModule)
	}
}
