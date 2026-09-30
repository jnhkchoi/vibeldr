package main

// The PCI vendor scanning helpers, shared by the three per-vendor unlocks
// (Aquantia, Mellanox, NVIDIA).
//
// It is pure file IO, so a fake sysfs tree in a temporary directory stands in
// for the real one and no Linux build tag is needed - which is what makes the
// sysfs path parsing unit-testable. The actual loading is each vendor's own
// file.
//
// PCI 벤더 스캔 헬퍼. 세 벤더별 unlock (Aquantia / Mellanox / NVIDIA) 이
// 공통으로 쓴다. 순수 파일 IO 로만 되어 있어 임시 디렉터리의 가짜 sysfs
// 트리로 대신할 수 있고, Linux 빌드 태그도 필요 없다. 그래서 sysfs 경로 파싱을 단위테스트로 잡을
// 수 있다. 실제 로드는 각 벤더 파일이 담당한다.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pciDevice is one PCI device as seen through sysfs.
//
// path is that device's sysfs directory, for example
// /sys/bus/pci/devices/0000:03:00.0. Reading an SR-IOV setting or a modalias
// below it starts from this path. vendor and device are sysfs's hex notation
// ("0x1d6a") parsed into a uint32.
//
// pciDevice - sysfs 에서 본 하나의 PCI 장치.
//
// path 는 그 장치의 sysfs 디렉터리 (예: /sys/bus/pci/devices/0000:03:00.0).
// SR-IOV 세팅이나 modalias 를 그 밑에서 읽을 때 이 경로가 기준이 된다.
// vendor/device 는 sysfs 의 hex 표기 ("0x1d6a") 를 uint32 로 파싱한 값이다.
type pciDevice struct {
	path         string
	vendor       uint32
	device       uint32
	sysfsAddress string
}

// parseHexID turns the "0x1d6a\n" sysfs writes into a uint32.
//
// There is no need to be generous about it: a Synology kernel always writes
// four lower-case hex digits into these files. Upper case, surrounding
// whitespace and a missing 0x are allowed; anything that is not hex is an
// error.
//
// parseHexID - sysfs 가 뱉는 "0x1d6a\n" 형식을 uint32 로 바꾼다.
//
// 지나치게 관대할 필요는 없다. 시놀로지 커널은 이 파일들에 항상 소문자
// 4자리 hex 를 쓴다. 대문자, 앞뒤 공백, 0x 없는 형태만 허용하고, hex 가
// 아니면 에러다.
func parseHexID(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if s == "" {
		return 0, fmt.Errorf("empty id")
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// scanPCIDevices enumerates every PCI device under sysfsRoot.
//
// sysfsRoot is usually "/sys" (hwscan.DefaultSysfs). For each device it reads
// the vendor and device files into a pciDevice. An entry that fails to read is
// skipped quietly - in a race such as a PCI hot-remove there really is a moment
// where the link is alive and the files are gone.
//
// scanPCIDevices - sysfsRoot 아래 모든 PCI 장치를 열거한다.
//
// sysfsRoot 는 보통 "/sys" (hwscan.DefaultSysfs). 매 장치별로 vendor+device
// 파일을 읽어 pciDevice 로 만든다. 읽기에 실패한 항목은 조용히 건너뛴다.
// PCI hot-remove 같은 경합에서 링크는 살아 있고 파일은 사라진 순간이 실재한다.
func scanPCIDevices(sysfsRoot string) []pciDevice {
	dir := filepath.Join(sysfsRoot, "bus", "pci", "devices")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []pciDevice
	for _, e := range entries {
		devPath := filepath.Join(dir, e.Name())
		vendor, err := readHexIDFile(filepath.Join(devPath, "vendor"))
		if err != nil {
			continue
		}
		device, err := readHexIDFile(filepath.Join(devPath, "device"))
		if err != nil {
			continue
		}
		out = append(out, pciDevice{
			path:         devPath,
			vendor:       vendor,
			device:       device,
			sysfsAddress: e.Name(),
		})
	}
	return out
}

// readHexIDFile reads one hex id out of one file.
// readHexIDFile - 한 파일에서 hex id 하나를 읽는다.
func readHexIDFile(path string) (uint32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return parseHexID(string(b))
}

// pciByVendor filters by vendor ID.
// pciByVendor - 벤더 ID 로 필터한다.
func pciByVendor(devs []pciDevice, vendor uint32) []pciDevice {
	var out []pciDevice
	for _, d := range devs {
		if d.vendor == vendor {
			out = append(out, d)
		}
	}
	return out
}
