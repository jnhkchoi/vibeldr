package main

import (
	"os"
	"path/filepath"
	"strings"

	"vibeldr/internal/dsmconf"
	"vibeldr/internal/hwscan"
)

// Fitting the installed system's early module loading to this CPU.
//
// DSM lists modules for systemd-modules-load in /usr/lib/modules-load.d, and
// 70-crypto-kernel.conf names CPU-specific ones: crc32c-intel on DS918+ and
// DS3622xs+, which needs SSE4.2, and aesni-intel on SA6400, which needs AES-NI.
// On a CPU without them - a VM with the default kvm64 model among others - the
// module refuses to load and systemd-modules-load fails. syno-kernel-modules-
// load.service is Requisite= on it, so it is skipped too, and with it usbhid,
// hid, usblp, aesni-intel and syno_hddmon. The journal then shows
// "Dependency failed for Load Kernel Modules in Kernel Project".
//
// Two changes, both made again on every boot because a DSM update puts the
// files back:
//
//   - a module whose CPU feature is missing is commented out of
//     70-crypto-kernel.conf, and without AES-NI support_aesni_intel is set to
//     "no", so syno-kernel-modules-load.sh does not probe aesni-intel either.
//   - a drop-in gives systemd-modules-load an ExecStart with a leading "-",
//     so a module that still fails to load does not fail the unit and take
//     syno-kernel-modules-load down with it.
//
// 설치된 시스템의 초기 모듈 로드를 이 CPU 에 맞춘다.
//
// DSM 은 systemd-modules-load 가 올릴 모듈을 /usr/lib/modules-load.d 에 적고,
// 70-crypto-kernel.conf 에 CPU 에 따른 모듈을 넣는다. DS918+·DS3622xs+ 는
// SSE4.2 가 필요한 crc32c-intel, SA6400 은 AES-NI 가 필요한 aesni-intel 이다. 그
// 기능이 없는 CPU (기본 kvm64 모델 VM 등) 에서는 모듈이 올라가지 않고
// systemd-modules-load 가 실패한다. syno-kernel-modules-load.service 가 그것에
// Requisite= 로 걸려 있어 함께 건너뛰고, usbhid, hid, usblp, aesni-intel,
// syno_hddmon 도 같이 빠진다. 그러면 저널에 "Dependency failed for Load Kernel
// Modules in Kernel Project" 가 남는다.
//
// 두 가지를 바꾸고, DSM 업데이트가 파일을 되돌리므로 매 부팅마다 다시 한다.
//
//   - CPU 기능이 없는 모듈은 70-crypto-kernel.conf 에서 주석 처리하고, AES-NI 가
//     없으면 support_aesni_intel 을 "no" 로 둬 syno-kernel-modules-load.sh 도
//     aesni-intel 을 찾지 않게 한다.
//   - 설정 조각으로 systemd-modules-load 의 ExecStart 앞에 "-" 를 붙여, 그래도
//     못 올라가는 모듈이 유닛을 실패시키고 syno-kernel-modules-load 까지
//     끌어내리지 않게 한다.

const (
	cryptoModulesConf = "/usr/lib/modules-load.d/70-crypto-kernel.conf"
	modulesLoadDropIn = "/etc/systemd/system/systemd-modules-load.service.d/vibeldr.conf"
)

// cpuModules maps a module in 70-crypto-kernel.conf to the CPU flag it needs.
// cpuModules - 70-crypto-kernel.conf 의 모듈과 그것이 필요로 하는 CPU 플래그.
var cpuModules = map[string]string{
	"crc32c-intel": "sse4_2",
	"aesni-intel":  "aes",
}

// modulesLoadDropInBody clears the unit's ExecStart and sets it again with
// "-", which tells systemd to ignore the command's exit status.
//
// modulesLoadDropInBody - 유닛의 ExecStart 를 비우고 "-" 를 붙여 다시 정한다.
// "-" 는 systemd 에게 명령의 종료 상태를 무시하라는 뜻이다.
const modulesLoadDropInBody = "[Service]\nExecStart=\nExecStart=-/usr/lib/systemd/systemd-modules-load\n"

// fitModuleLoading makes both changes under root for the CPU cpu describes and
// returns what it changed, for the log. With no CPU flags read it only writes
// the drop-in, since which modules would fail is unknown.
//
// fitModuleLoading - root 아래에 cpu 가 말하는 CPU 에 맞춰 두 가지를 바꾸고,
// 바꾼 것을 로그용으로 돌려준다. CPU 플래그를 못 읽었으면 어느 모듈이 실패할지
// 모르므로 설정 조각만 쓴다.
func fitModuleLoading(root string, cpu hwscan.CPUFlags) []string {
	var done []string
	if cpu != nil {
		var off []string
		path := root + cryptoModulesConf
		if raw, err := os.ReadFile(path); err == nil {
			lines := strings.Split(string(raw), "\n")
			for i, l := range lines {
				name := strings.TrimSpace(l)
				flag, ok := cpuModules[name]
				if ok && !cpu[flag] {
					lines[i] = "# " + name + " - vibeldr: this CPU has no " + flag
					off = append(off, name)
				}
			}
			if len(off) > 0 {
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
					logf("module loading: %s: %v", path, err)
				} else {
					done = append(done, "off: "+strings.Join(off, " "))
				}
			}
		}
		if !cpu["aes"] {
			for _, rel := range synoinfoFiles {
				p := root + rel
				raw, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				out := dsmconf.Set(string(raw), "support_aesni_intel", "no")
				if out != string(raw) {
					if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
						logf("module loading: %s: %v", p, err)
					}
				}
			}
		}
	}
	p := root + modulesLoadDropIn
	if cur, err := os.ReadFile(p); err != nil || string(cur) != modulesLoadDropInBody {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			logf("module loading: %v", err)
			return done
		}
		if err := os.WriteFile(p, []byte(modulesLoadDropInBody), 0o644); err != nil {
			logf("module loading: %s: %v", p, err)
			return done
		}
		done = append(done, "systemd-modules-load failures ignored")
	}
	return done
}
