package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Emulating an N100 as an SA6400 puts DSM's temperature readings out. The
// SA6400 is AMD EPYC while the real machine is an Intel Alder Lake-N, so the
// hwmon slot DSM expects holds no real CPU temperature source (coretemp), and
// when a stand-in such as the ACPI thermal zone (acpitz) takes the front
// position, the CPU and GPU temperatures read the same value or come out as 0.
//
// The same symptom reproduces across recent Intel and AMD parts alike - Alder
// Lake N-series, Raptor Lake, AMD Ryzen APUs, EPYC Embedded. The way to fix it
// from user space, with no kernel module, would be to put the hwmon slots back
// in order so the real coretemp or k10temp node lands where DSM reads.
//
// What this file does is narrower. The hwmon numbering in sysfs cannot be
// rearranged from here, so it finds the real CPU sensor, makes a stable
// symlink to it at /run/vibeldr/hwmon/cpu and logs the mapping. Nothing in the
// loader or in DSM reads that link, so DSM's own readings are not changed by
// it.
//
// It runs from the -agent run, which systemd starts on every boot and again
// whenever the storage manager restarts (the drop-in in hddb.go). The link is
// under /run, so every run makes it afresh. maintainCPUTempLink re-checks
// every five seconds, but only while the process lives, and the -agent run
// exits right after its work.
//
// N100 을 SA6400 로 에뮬레이션하면 DSM 의 온도 조회가 어긋난다. SA6400
// 은 AMD EPYC 이지만 실기는 Intel Alder Lake-N 이라, DSM 이 기대하는
// hwmon 슬롯에 실제 CPU 온도 소스 (coretemp) 가 없고, ACPI thermal zone
// (acpitz) 같은 대체값이 앞자리를 차지하면 CPU/GPU 온도가 같은 값으로
// 뜨거나 아예 0 으로 뜬다.
//
// 최신 Intel/AMD 전체에서 같은 증상이 재현된다 (Alder Lake N-series, Raptor
// Lake, AMD Ryzen APU, EPYC Embedded 등). LKM 없이 유저스페이스에서
// 고치려면 hwmon 슬롯의 순서를 바로잡아 실제 coretemp/k10temp 노드가
// DSM 이 읽는 자리에 오게 해야 한다.
//
// 이 파일이 하는 일은 그보다 좁다. 여기서는 sysfs 의 hwmon 번호를 바꿀 수
// 없으므로, 실제 CPU 센서를 찾아 /run/vibeldr/hwmon/cpu 에 안정된 심볼릭
// 링크를 만들고 매핑을 로그에 남긴다. 그 링크를 읽는 것은 로더에도 DSM 에도
// 없어서, DSM 자신의 온도 값은 이것으로 바뀌지 않는다.
//
// -agent 실행에서 돈다. systemd 가 매 부팅마다, 그리고 스토리지 매니저가
// 재시작할 때마다 (hddb.go 의 drop-in) 띄운다. 링크는 /run 밑이라 실행마다
// 새로 만든다. maintainCPUTempLink 는 5 초마다 다시 확인하지만 프로세스가
// 살아 있는 동안뿐이고, -agent 실행은 일을 마치면 바로 끝난다.

// vibeldrTempDir is where the mapping symlink lives. It is under /run, so a
// reboot clears it and remaking it is the re-verification.
//
// vibeldrTempDir - 매핑 심볼릭 링크가 사는 자리. /run 밑이라 재부팅되면
// 자동 소거되고, 다시 만드는 게 곧 재검증이다.
const vibeldrTempDir = "/run/vibeldr/hwmon"

// cpuTempPollInterval is how often the link is kept up to date. Too short costs
// load, too long misses a coretemp that loaded late. Five seconds is the
// practical value between them.
//
// cpuTempPollInterval - 링크 유지 주기. 너무 짧으면 부하고, 너무 길면
// 늦게 로드된 coretemp 를 못 잡는다. 5 초가 그 사이의 실용값이다.
const cpuTempPollInterval = 5 * time.Second

// knownCPUHwmonNames are the hwmon names accepted as a real CPU temperature
// source, in order of preference, first preferred. acpitz is only a fallback;
// coretemp or k10temp wins where either is present.
//
// knownCPUHwmonNames - 실제 CPU 온도 소스로 인정하는 hwmon 이름들.
// 우선순위 순서 (앞이 우선). acpitz 는 fallback 일 뿐이고, coretemp/k10temp
// 가 있으면 그걸 우선한다.
var knownCPUHwmonNames = []string{
	"coretemp",  // Intel (Core / Xeon / Alder Lake N / Raptor Lake / etc.)
	"k10temp",   // AMD (Zen / Zen+ / Zen2 / Zen3 / Zen4 / EPYC Embedded)
	"zenpower",  // AMD Zen custom driver, more accurate than k10temp where present / AMD (Zen 계열 커스텀 드라이버; 있으면 k10temp 보다 정확)
	"drivetemp", // drive SMART temperature, fallback only - not the CPU, but a last resort / 드라이브 SMART 온도 (fallback 만; CPU 아니지만 마지막 보루)
	"acpitz",    // last fallback: the ACPI thermal zone the BIOS exposes / 마지막 fallback: BIOS 가 노출하는 ACPI thermal zone
}

// fixTemperatureMapping finds the real CPU temperature source and exposes it
// through a stable link. It returns 0 on success, which includes having done
// nothing, and 1 on failure.
//
// fixTemperatureMapping - 실제 CPU 온도 소스를 찾아 안정 링크로 노출한다.
// 성공 (아무것도 안 한 경우 포함) 시 0, 실패 시 1.
func fixTemperatureMapping() int {
	// Log the vendor. The mapping itself is decided from the hwmon name, but
	// having the vendor makes a diagnosis such as "this is an AMD machine and
	// there is no k10temp" possible from the console alone.
	//
	// vendor 로그. 실제 매핑 판단은 hwmon 이름으로 하지만, "이 머신은
	// AMD 인데 k10temp 가 없다" 같은 진단을 콘솔만 봐도 하려면 벤더가
	// 있어야 쓸모 있다.
	vendor := cpuVendor()
	brand := cpuBrand()
	if brand != "" {
		logf("temperature: cpu=%s vendor=%s", brand, vendor)
	}

	// Off Linux there is no /sys and no hwmon, so this helper is simply a
	// no-op on other platforms.
	//
	// Linux 가 아니면 /sys 도 없고 hwmon 도 없다. 리눅스 외 플랫폼에서는
	// 이 헬퍼가 그냥 no-op 이다.
	if runtime.GOOS != "linux" {
		return 0
	}

	hwmons, err := scanHwmons()
	if err != nil {
		logf("temperature: hwmon scan failed: %v", err)
		return 1
	}
	if len(hwmons) == 0 {
		logf("temperature: no hwmon nodes - coretemp/k10temp not loaded")
		return 0
	}

	best := pickBestHwmon(hwmons)
	if best.path == "" {
		logf("temperature: no usable CPU temperature source (%d hwmon node(s) present)", len(hwmons))
		return 0
	}
	logf("temperature: CPU temperature source = %s (%s)", best.name, best.path)

	if err := ensureCPUTempLink(best.path); err != nil {
		logf("temperature: cannot create the link: %v", err)
		return 1
	}

	// Keep the link up to date in the background, for as long as this process
	// runs, to catch a coretemp that loads late. After a reboot the next
	// -agent run puts the link back in the same place.
	//
	// 이 프로세스가 도는 동안 백그라운드로 링크를 유지해, 늦게 로드되는
	// coretemp 를 잡는다. 재부팅 뒤에는 다음 -agent 실행이 같은 자리에 링크를
	// 다시 건다.
	go maintainCPUTempLink()
	return 0
}

type hwmonEntry struct {
	path string // the actual path of /sys/class/hwmon/hwmonN / /sys/class/hwmon/hwmonN 의 실제 경로
	name string // the string in the name file / name 파일에 든 문자열
}

// scanHwmons enumerates /sys/class/hwmon/hwmon* and reads each node's name.
// scanHwmons - /sys/class/hwmon/hwmon* 를 열거해 각 노드의 이름을 읽는다.
func scanHwmons() ([]hwmonEntry, error) {
	dirs, err := filepath.Glob("/sys/class/hwmon/hwmon*")
	if err != nil {
		return nil, err
	}
	var out []hwmonEntry
	for _, d := range dirs {
		n := readTrimmed(filepath.Join(d, "name"))
		if n == "" {
			continue
		}
		out = append(out, hwmonEntry{path: d, name: n})
	}
	return out, nil
}

// pickBestHwmon returns the first node matching knownCPUHwmonNames in order of
// preference, or an empty entry when none match.
//
// pickBestHwmon - knownCPUHwmonNames 우선순위대로 매칭되는 첫 노드를 돌려준다.
// 하나도 매칭 안 되면 빈 entry 다.
func pickBestHwmon(entries []hwmonEntry) hwmonEntry {
	for _, want := range knownCPUHwmonNames {
		for _, e := range entries {
			if e.name == want {
				return e
			}
		}
	}
	return hwmonEntry{}
}

// ensureCPUTempLink links /run/vibeldr/hwmon/cpu to the hwmon node in question,
// doing nothing when it already points there.
//
// ensureCPUTempLink - /run/vibeldr/hwmon/cpu 를 해당 hwmon 노드로 링크한다.
// 이미 같은 대상이면 아무것도 안 한다.
func ensureCPUTempLink(target string) error {
	if err := os.MkdirAll(vibeldrTempDir, 0o755); err != nil {
		return err
	}
	link := filepath.Join(vibeldrTempDir, "cpu")
	if existing, err := os.Readlink(link); err == nil && existing == target {
		return nil
	}
	_ = os.Remove(link)
	return os.Symlink(target, link)
}

// maintainCPUTempLink re-verifies the link every five seconds, following along
// by itself when the coretemp module loads late and the index changes. It
// lasts only as long as the process that started it.
//
// maintainCPUTempLink - 5 초마다 링크를 재검증한다. coretemp 모듈이 뒤늦게
// 로드되어 index 가 바뀌면 자동으로 따라간다. 띄운 프로세스가 살아 있는
// 동안만 돈다.
func maintainCPUTempLink() {
	for {
		time.Sleep(cpuTempPollInterval)
		if runtime.GOOS != "linux" {
			return
		}
		hwmons, err := scanHwmons()
		if err != nil {
			continue
		}
		best := pickBestHwmon(hwmons)
		if best.path == "" {
			continue
		}
		_ = ensureCPUTempLink(best.path)
	}
}

// cpuVendor is the vendor_id field from /proc/cpuinfo: "GenuineIntel",
// "AuthenticAMD" and so on. Unreadable comes back empty.
//
// cpuVendor - /proc/cpuinfo 의 vendor_id 필드. "GenuineIntel", "AuthenticAMD"
// 등. 못 읽으면 빈 문자열.
func cpuVendor() string {
	return firstFieldValue("/proc/cpuinfo", "vendor_id")
}

// cpuBrand is the model name from /proc/cpuinfo, for the log.
// cpuBrand - /proc/cpuinfo 의 model name. 로그용.
func cpuBrand() string {
	return firstFieldValue("/proc/cpuinfo", "model name")
}

// firstFieldValue is the value part of the first "key : value" line.
// firstFieldValue - "key : value" 라인들 중 첫 번째의 value 부분.
func firstFieldValue(path, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		if strings.TrimSpace(line[:i]) == key {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}
