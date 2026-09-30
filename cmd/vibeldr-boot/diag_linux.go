//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"vibeldr/internal/synoboot"
)

// Gathering what it takes to look into a failed install or boot, into one
// file on the loader's partition 1 (vibeldr-diag-<time>.tar.gz). Taking the
// stick to another computer is then enough to hand it on, with no network and
// no serial port.
//
// In it:
//
//	loader-log.txt  the helper's log from the last DSM boot, which it keeps in
//	                the gap behind the loader disk's MBR (cmd/vibeldr-init)
//	dmesg.txt       this kernel's log
//	system.txt      CPU, memory, PCI devices, disks and network cards
//	loader.yaml     the settings, with the serial number, MAC addresses and
//	                notification addresses and passwords blanked out
//	dsm/            an installed DSM's logs, from the first data disk that
//	                has one: the installer's .log.junior and /var/log
//	pstore/         what the firmware kept of an earlier kernel crash
//
// Each file is cut to its last diagMaxFile bytes.
//
// 설치나 부팅이 실패했을 때 살펴볼 것을 로더 파티션 1 의 파일 하나
// (vibeldr-diag-<시각>.tar.gz) 로 모은다. 네트워크도 시리얼 포트도 없이, USB 를
// 다른 컴퓨터에 꽂기만 하면 넘겨줄 수 있다.
//
// 담기는 것:
//
//	loader-log.txt  지난 DSM 부팅의 헬퍼 로그. 헬퍼가 로더 디스크의 MBR 뒤 빈
//	                공간에 남긴다 (cmd/vibeldr-init)
//	dmesg.txt       이 커널의 로그
//	system.txt      CPU, 메모리, PCI 장치, 디스크, 랜카드
//	loader.yaml     설정. 시리얼 번호, MAC 주소, 알림 주소와 비밀번호는 지운다
//	dsm/            설치된 DSM 의 로그. 그것이 있는 첫 데이터 디스크에서
//	                설치기의 .log.junior 와 /var/log
//	pstore/         펌웨어가 남긴 이전 커널 크래시 기록
//
// 파일마다 마지막 diagMaxFile 바이트만 남긴다.

// diagMaxFile caps each file in the bundle.
// diagMaxFile - 묶음 안 파일 하나의 상한.
const diagMaxFile = 4 << 20

// Where the helper keeps its log on the loader disk, as cmd/vibeldr-init's
// logfile_linux.go writes it.
//
// 헬퍼가 로더 디스크에 로그를 두는 자리. cmd/vibeldr-init 의 logfile_linux.go
// 가 쓰는 대로다.
const (
	loaderLogOffset = 1024 * 512
	loaderLogBytes  = 512 * 1024
	loaderLogMagic  = "=== VIBELDR BOOT LOG ==="
)

// saveDiagnostics writes the bundle into dir and returns its name and size.
// loaderDisk is the loader's disk ("sda"), empty when it was not found.
//
// saveDiagnostics - 묶음을 dir 에 쓰고 이름과 크기를 돌려준다. loaderDisk 는
// 로더의 디스크("sda")이고, 찾지 못했으면 비어 있다.
func saveDiagnostics(loaderDisk, dir string) (string, int, error) {
	if !isMounted(dir) {
		return "", 0, fmt.Errorf("%s is not mounted", dir)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	now := time.Now()
	add := func(name string, data []byte) {
		if len(data) > diagMaxFile {
			data = data[len(data)-diagMaxFile:]
		}
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: now})
		_, _ = tw.Write(data)
	}

	if log := readLoaderLog("/dev/" + synoboot.Name); log != nil {
		add("loader-log.txt", log)
	}
	if k := kernelLog(); k != nil {
		add("dmesg.txt", k)
	}
	add("system.txt", []byte(systemSummary()))
	if raw, err := os.ReadFile(configPath); err == nil {
		add("loader.yaml", maskConfig(raw))
	}
	for _, f := range dsmLogs(loaderDisk) {
		add(f.name, f.data)
	}
	for _, f := range pstoreFiles() {
		add(f.name, f.data)
	}

	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	name := "vibeldr-diag-" + now.UTC().Format("20060102-150405") + ".tar.gz"
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		return "", 0, err
	}
	return name, buf.Len(), nil
}

// readLoaderLog reads the helper's log off the loader disk's whole-disk node,
// nil when there is none.
//
// readLoaderLog - 로더 디스크의 전체 디스크 노드에서 헬퍼의 로그를 읽는다.
// 없으면 nil.
func readLoaderLog(node string) []byte {
	f, err := os.Open(node)
	if err != nil {
		return nil
	}
	defer f.Close()
	b := make([]byte, loaderLogBytes)
	n, _ := f.ReadAt(b, loaderLogOffset)
	b = b[:n]
	if !bytes.HasPrefix(b, []byte(loaderLogMagic)) {
		return nil
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return b
}

// kernelLog is the kernel's ring buffer.
// kernelLog - 커널 링 버퍼.
func kernelLog() []byte {
	size, err := syscall.Klogctl(10, nil) // SYSLOG_ACTION_SIZE_BUFFER
	if err != nil || size <= 0 {
		size = 1 << 20
	}
	buf := make([]byte, size)
	n, err := syscall.Klogctl(3, buf) // SYSLOG_ACTION_READ_ALL
	if err != nil {
		return nil
	}
	return buf[:n]
}

// systemSummary lists the machine's hardware as sysfs and /proc report it.
// systemSummary - sysfs 와 /proc 가 알리는 대로 이 기계의 하드웨어를 적는다.
func systemSummary() string {
	var b strings.Builder
	var uts syscall.Utsname
	if syscall.Uname(&uts) == nil {
		fmt.Fprintf(&b, "kernel: %s\n", utsString(uts.Release[:]))
	}
	fmt.Fprintf(&b, "cpu: %s\n", procField("/proc/cpuinfo", "model name"))
	fmt.Fprintf(&b, "memory: %s\n", procField("/proc/meminfo", "MemTotal"))
	if raw, err := os.ReadFile("/proc/filesystems"); err == nil {
		fmt.Fprintf(&b, "filesystems: %s\n", strings.Join(strings.Fields(strings.ReplaceAll(string(raw), "nodev", "")), " "))
	}
	if raw, err := os.ReadFile("/proc/modules"); err == nil {
		var mods []string
		for _, l := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(l); len(f) > 0 {
				mods = append(mods, f[0])
			}
		}
		sort.Strings(mods)
		fmt.Fprintf(&b, "modules: %s\n", strings.Join(mods, " "))
	}

	b.WriteString("\npci:\n")
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	sort.Strings(devs)
	for _, d := range devs {
		drv := ""
		if p, err := os.Readlink(filepath.Join(d, "driver")); err == nil {
			drv = filepath.Base(p)
		}
		fmt.Fprintf(&b, "  %s %s:%s class %s %s\n", filepath.Base(d),
			sysValue(d, "vendor"), sysValue(d, "device"), sysValue(d, "class"), drv)
	}

	b.WriteString("\ndisks:\n")
	disks, _ := os.ReadDir("/sys/block")
	for _, e := range disks {
		d := filepath.Join("/sys/block", e.Name())
		fmt.Fprintf(&b, "  %s %s sectors, %s\n", e.Name(), sysValue(d, "size"), sysValue(d, "device/model"))
	}

	b.WriteString("\nnetwork:\n")
	nets, _ := os.ReadDir("/sys/class/net")
	for _, e := range nets {
		d := filepath.Join("/sys/class/net", e.Name())
		drv := ""
		if p, err := os.Readlink(filepath.Join(d, "device", "driver")); err == nil {
			drv = filepath.Base(p)
		}
		fmt.Fprintf(&b, "  %s %s %s\n", e.Name(), sysValue(d, "operstate"), drv)
	}
	return b.String()
}

// sysValue is one sysfs file's contents, trimmed.
// sysValue - sysfs 파일 하나의 내용, 앞뒤 공백 없이.
func sysValue(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "-"
	}
	return strings.TrimSpace(string(raw))
}

// procField is the value after "name:" on the first such line of a /proc
// file.
//
// procField - /proc 파일에서 "name:" 으로 시작하는 첫 줄의 값.
func procField(path, name string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "-"
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == name {
			return strings.TrimSpace(v)
		}
	}
	return "-"
}

// utsString turns a NUL-padded utsname field into a string.
// utsString - NUL 로 채운 utsname 칸을 문자열로.
func utsString[T int8 | uint8](f []T) string {
	b := make([]byte, 0, len(f))
	for _, c := range f {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// diagFile is one file for the bundle.
// diagFile - 묶음에 들어갈 파일 하나.
type diagFile struct {
	name string
	data []byte
}

// dsmLogs reads an installed DSM's logs from the first data disk that has
// them, mounting its system partition read-only as dsmbackup_linux.go does,
// and says in dsm-scan.txt what each disk gave.
//
// dsmLogs - 설치된 DSM 의 로그를 그것이 있는 첫 데이터 디스크에서 읽는다.
// dsmbackup_linux.go 처럼 시스템 파티션을 읽기 전용으로 붙이고, 디스크마다 무엇을
// 얻었는지 dsm-scan.txt 에 적는다.
func dsmLogs(loader string) []diagFile {
	var out []diagFile
	var scan strings.Builder
	for _, p := range dsmPartitions(loader) {
		part := p.Dev
		if len(out) > 0 {
			fmt.Fprintf(&scan, "%s: skipped, logs already taken\n", part)
			continue
		}
		err := withDSMSystem(part, func(root string) error {
			out = readDSMLogs(root, "dsm/"+p.Disk+"/")
			if len(out) == 0 {
				return fmt.Errorf("no DSM logs on it")
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(&scan, "%s: %v\n", part, err)
		} else {
			fmt.Fprintf(&scan, "%s: %d file(s)\n", part, len(out))
		}
	}
	return append(out, diagFile{"dsm-scan.txt", []byte(scan.String())})
}

// readDSMLogs takes the logs from a mounted system partition.
// readDSMLogs - 붙여 둔 시스템 파티션에서 로그를 가져온다.
func readDSMLogs(root, prefix string) []diagFile {
	var out []diagFile
	for _, pattern := range []string{".log.junior/*", "var/log/messages", "var/log/*.log"} {
		matches, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, m := range matches {
			if fi, err := os.Stat(m); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			rel, _ := filepath.Rel(root, m)
			out = append(out, diagFile{prefix + filepath.ToSlash(rel), data})
		}
	}
	return out
}

// pstoreFiles are the records in the pstore file system, mounted here if it
// is not yet.
//
// pstoreFiles - pstore 파일시스템의 기록. 아직 붙어 있지 않으면 여기서 붙인다.
func pstoreFiles() []diagFile {
	const at = "/sys/fs/pstore"
	entries, err := os.ReadDir(at)
	if err != nil {
		return nil
	}
	if len(entries) == 0 {
		if syscall.Mount("pstore", at, "pstore", syscall.MS_RDONLY, "") == nil {
			defer syscall.Unmount(at, 0)
			entries, _ = os.ReadDir(at)
		}
	}
	var out []diagFile
	for _, e := range entries {
		if data, err := os.ReadFile(filepath.Join(at, e.Name())); err == nil {
			out = append(out, diagFile{"pstore/" + e.Name(), data})
		}
	}
	return out
}
