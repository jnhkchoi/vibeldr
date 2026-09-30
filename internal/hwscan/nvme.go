package hwscan

import (
	"io/fs"
	"os"
	"path"
	"sort"
)

// classNVMe is the PCI class of an NVMe controller (mass storage, NVM).
// classNVMe - NVMe 컨트롤러의 PCI class (대용량 저장장치, NVM).
const classNVMe = 0x0108

// NVMeRoots lists the NVMe controllers under a sysfs tree in PCI address
// order, each as the device tree writes its position (pcieRoot).
//
// NVMeRoots - sysfs 트리 아래 NVMe 컨트롤러를 PCI 주소 순으로, 각각 device tree
// 가 위치를 적는 형식(pcieRoot)으로 나열한다.
func NVMeRoots(fsys fs.FS) []string {
	hosts, _ := fs.Glob(fsys, "devices/pci*")
	type found struct{ addr, root string }
	var out []found
	var walk func(host, dir string)
	walk = func(host, dir string) {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || !isPCIAddress(e.Name()) {
				continue
			}
			child := path.Join(dir, e.Name())
			if class, ok := readClass(fsys, child); ok && class>>8 == classNVMe {
				out = append(out, found{e.Name(), pcieRoot(host, child)})
				continue
			}
			walk(host, child)
		}
	}
	for _, h := range hosts {
		walk(h, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].addr < out[j].addr })
	roots := make([]string, len(out))
	for i, f := range out {
		roots[i] = f.root
	}
	return roots
}

// NVMeRootsSysfs is NVMeRoots on the running system.
// NVMeRootsSysfs - 도는 시스템의 NVMeRoots.
func NVMeRootsSysfs() []string { return NVMeRoots(os.DirFS(DefaultSysfs)) }
