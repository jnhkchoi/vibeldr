package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// M.2 slots on a model without a device tree.
//
// A device-tree model finds its M.2 slots in model.dtb (dtb/nvme.go). The
// others use libsynonvme, which knows each model's board slots by the PCI
// address of the root port an M.2 SSD sits behind - for the DS918+ 0000:00:13.1
// and 0000:00:13.2. An NVMe SSD anywhere else has no slot, and DSM does not
// offer it as a cache.
//
// So on the DS918+ the helper rewrites those two addresses in the installed
// system's libsynonvme to the root-bus addresses of this machine's first two
// NVMe controllers. The addresses have the same length, so nothing else in the
// file moves. The untouched library is kept once (nvmeLibOrig) and every boot
// starts again from it, so a changed set of SSDs is followed and a machine
// with none gets the library back as it was.
//
// device tree 없는 모델의 M.2 슬롯.
//
// device tree 모델은 model.dtb 에서 M.2 슬롯을 찾는다 (dtb/nvme.go). 나머지는
// libsynonvme 를 쓰는데, 이것은 모델마다 보드 슬롯을 M.2 SSD 가 뒤에 붙는 루트
// 포트의 PCI 주소로 안다 - DS918+ 는 0000:00:13.1 과 0000:00:13.2. 다른 곳의 NVMe
// SSD 는 슬롯이 없고, DSM 은 그것을 캐시로 내놓지 않는다.
//
// 그래서 DS918+ 에서는 헬퍼가 설치된 시스템의 libsynonvme 에서 그 두 주소를 이
// 기계의 앞 NVMe 컨트롤러 둘의 루트 버스 주소로 바꾼다. 주소 길이가 같아서 파일의
// 다른 곳은 움직이지 않는다. 손대지 않은 라이브러리는 한 번 보관하고
// (nvmeLibOrig) 부팅마다 거기서 다시 시작하므로, SSD 구성이 바뀌면 따라가고
// SSD 가 없는 기계는 원래 라이브러리를 되찾는다.

const (
	// nvmeLib is the library inside the installed system.
	// nvmeLib - 설치된 시스템 안의 라이브러리.
	nvmeLib = "/usr/lib/libsynonvme.so.1"
	// nvmeLibOrig is the untouched copy.
	// nvmeLibOrig - 손대지 않은 사본.
	nvmeLibOrig = notifyDSMDir + "/libsynonvme.so.1.orig"
)

// boardM2Ports are the DS918+'s M.2 root ports as libsynonvme lists them.
// boardM2Ports - libsynonvme 가 적어 둔 DS918+ 의 M.2 루트 포트.
var boardM2Ports = []string{"0000:00:13.1", "0000:00:13.2"}

// fitNVMeLibrary rewrites libsynonvme under root for the NVMe controllers in
// nvme (dtb pcie_root strings) and says what it did, "" when nothing.
//
// fitNVMeLibrary - root 아래 libsynonvme 를 nvme 의 NVMe 컨트롤러(dtb pcie_root
// 문자열)에 맞게 고치고 한 일을 말한다. 한 일이 없으면 "".
func fitNVMeLibrary(root string, nvme []string) string {
	lib, orig := root+nvmeLib, root+nvmeLibOrig
	src, err := os.ReadFile(orig)
	if err != nil {
		src, err = os.ReadFile(lib)
		if err != nil || !bytes.Contains(src, []byte(boardM2Ports[0])) {
			return ""
		}
		if len(nvme) == 0 {
			return ""
		}
		if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
			return ""
		}
		if err := os.WriteFile(orig, src, 0o644); err != nil {
			return ""
		}
	}
	out := src
	var used []string
	for i, r := range nvme {
		if i >= len(boardM2Ports) {
			break
		}
		port, _, _ := strings.Cut(r, ",")
		if len(port) != len(boardM2Ports[i]) {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(boardM2Ports[i]), []byte(port))
		used = append(used, port)
	}
	if cur, err := os.ReadFile(lib); err == nil && bytes.Equal(cur, out) {
		return ""
	}
	if err := os.WriteFile(lib, out, 0o644); err != nil {
		return ""
	}
	if len(used) == 0 {
		return "libsynonvme put back as it was"
	}
	return "libsynonvme M.2 slots at " + strings.Join(used, " ")
}
