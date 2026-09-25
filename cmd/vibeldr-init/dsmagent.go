package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The part of the loader that runs inside DSM.
//
// Everything else happens before DSM starts, and writing into a file DSM will
// read later is enough. The drive compatibility verdict is the one thing that
// does not work that way.
//
// DSM checks each drive against the list Synology sells and supports, and
// anything not on it is "unverified": storage pools and volumes turn orange and
// a system reliability warning appears. The drive itself is fine - this is a
// support policy, not a health check - but the UI does not explain that, and on
// a machine Synology does not sell every drive is caught here.
//
// This cannot be finished in advance. synostoraged judges after it starts,
// writes a per-drive result under /run, and keeps that result until the next
// boot. So the loader has to leave something behind: one binary and one service
// unit, run once after the storage service has come up.
//
// The paths below are DSM's own vocabulary. Nothing here was invented; it is
// what DSM was observed writing into these places:
//
//	/run/synostorage/disks/<dev>/compatibility        the verdict
//	                            /force_compatibility  a forced override
//	                            /compatibility_action what the UI suggests doing
//	                            /smart_*_ignore       suppress SMART warnings
//	                            /compatibility*.lock  the lock held while judging
//	/run/space/pool_compatibility*                    the pool's copy
//	/var/lib/disk-compatibility/*_v*.db               the drive list itself
//
// 로더 중 DSM 안에서 도는 파트.
//
// 나머지 일들은 전부 DSM 시작 전에 일어나고, DSM 이 나중에 읽을 파일에 미리
// 써두는 걸로 충분하다. 하지만 드라이브 호환성 판정은 그런 방식이 안 통한다.
//
// DSM 은 각 드라이브를 시놀로지가 판매·지원하는 목록에 대조하고, 목록에
// 없는 건 "미검증" 이다. 스토리지 풀과 볼륨이 주황색으로 뜨고 시스템 신뢰성
// 경고가 뜬다. 드라이브 자체는 멀쩡하지만 (건강 검진이 아니라 지원 정책일
// 뿐) UI 는 그렇게 설명해주지 않고, 시놀로지가 팔지 않는 머신에서는
// 모든 드라이브가 여기서 걸린다.
//
// 이건 사전에 못 끝낸다. synostoraged 가 시작한 뒤에 판정해서 /run 밑에
// 드라이브별 결과를 쓰고, 그 결과를 다음 부팅까지 유지하기 때문이다. 그래서
// 로더는 뭔가를 남겨야 한다: 바이너리 하나 + 서비스 유닛 하나, 스토리지
// 서비스가 뜬 뒤 한 번 실행.
//
// 아래 경로들은 DSM 자신이 쓰는 어휘 그대로다. 새로 지어낸 게 없고 DSM 이
// 이 자리들에 뭘 쓰는지 관찰해서 얻은 것이다:
//
//	/run/synostorage/disks/<dev>/compatibility        판정
//	                            /force_compatibility  강제 덮어쓰기
//	                            /compatibility_action UI 가 제안하는 조치
//	                            /smart_*_ignore       SMART 경고 억제
//	                            /compatibility*.lock  판정 중 잡는 락
//	/run/space/pool_compatibility*                    풀 쪽 사본
//	/var/lib/disk-compatibility/*_v*.db               드라이브 목록 자체

// Where the loader leaves its own copy inside DSM.
// DSM 안에서 로더가 자기 사본을 남기는 자리들.
const (
	dsmAgentName    = "/usr/sbin/vibeldr-agent"
	dsmServiceName  = "vibeldr.service"
	dsmServiceDir   = "/usr/lib/systemd/system"
	dsmWantsDir     = "/usr/lib/systemd/system/multi-user.target.wants"
	storageRunDir   = "/run/synostorage/disks"
	compatDBDir     = "/var/lib/disk-compatibility"
	compatDBKey     = "disk_compatbility_info" // Synology's own spelling, not a typo / Synology 표기 그대로 (오타 아님)
	agentWaitPeriod = 2 * time.Second
	agentWaitTries  = 30
)

// The pool verdict cache is never deleted.
//
// It is tempting to remove /run/space/pool_compatibility and
// /var/lib/space/pool_compatibility so the pool asks again instead of
// repeating an old answer. That does the opposite. DSM treats the
// /var/lib/space/ copy as the original and copies it to /run/space/, so
// removing the original makes the copy fail and the cache never appears
// again:
//
//	space_pool_disk_compat.c:314 Failed to copy '/var/lib/space/pool_compatibility'
//	                             to '/run/space/pool_compatibility'
//	disk_compatibility_cache_update.c:138 Failed to update cache
//
// Storage Manager then shows "unverified" for good. The way to get a fresh
// verdict is to ask synostgdisk for one (recheckDiskCompatibility in hddb.go).
//
// 풀 판정 캐시 파일은 지우지 않는다.
//
// /run/space/pool_compatibility 와 /var/lib/space/pool_compatibility 를
// 지우면 풀이 옛 판단을 되풀이하지 않고 새로 물을 것 같지만 반대다. DSM 은
// /var/lib/space/ 쪽을 원본으로 삼아 /run/space/ 로 복사하므로, 원본을
// 지우면 복사가 실패하고 캐시가 영영 안 생긴다 (위 로그). 그 상태로 저장소
// 관리자는 계속 "미검증" 을 보여준다. 다시 판정하게 하려면 지우는 게 아니라
// synostgdisk 에게 물어본다 (hddb.go 의 recheckDiskCompatibility).

// verdict is what DSM records once it has judged a drive.
// verdict - DSM 이 드라이브에 대해 판정을 내린 뒤 기록하는 값들.
type verdict struct {
	Compatibility           string `json:"compatibility"`
	NotYetRollingStatus     string `json:"not_yet_rolling_status"`
	FWDSMUpdateStatusNotify bool   `json:"fw_dsm_update_status_notify"`
	BareboneInstallable     bool   `json:"barebone_installable"`
	BareboneInstallableV2   string `json:"barebone_installable_v2"`
	SMARTTestIgnore         bool   `json:"smart_test_ignore"`
	SMARTAttrIgnore         bool   `json:"smart_attr_ignore"`
}

func supported() verdict {
	return verdict{
		Compatibility:           "support",
		NotYetRollingStatus:     "support",
		FWDSMUpdateStatusNotify: false,
		BareboneInstallable:     true,
		BareboneInstallableV2:   "auto",
		SMARTTestIgnore:         true,
		SMARTAttrIgnore:         true,
	}
}

// runAgent is the one resident job the loader leaves inside DSM. systemd starts
// it after the storage service has looked the disks over.
//
// runAgent - 로더가 DSM 안에 남기는 유일한 상주 작업. 스토리지 서비스가
// 디스크를 살펴본 뒤 systemd 가 시작한다.
func runAgent() error {
	// Check the MACs written in at the ramdisk stage came through unchanged. If
	// they match, nothing is done beyond a log line per card. When a card's driver
	// is reloaded during the boot the address reverts to the factory one, and that
	// is when this writes it again.
	//
	// 램디스크 단계에서 박아둔 MAC 이 그대로 넘어왔는지 확인한다. 같으면
	// 아무것도 안 하고 카드마다 한 줄만 남긴다. 부팅 도중 랜카드 드라이버가 다시
	// 로드되면 주소가 공장값으로 돌아가는데, 그때 여기서 다시 박힌다.
	applyHardwareMACs()

	drives := waitForDrives()
	if len(drives) == 0 {
		logf("agent: no drives appeared under %s", storageRunDir)
	}

	action, err := json.Marshal(supported())
	if err != nil {
		return err
	}

	var models []string
	for _, dir := range drives {
		model := readTrimmed(filepath.Join(dir, "model"))
		if model == "" {
			model = readTrimmed(filepath.Join(dir, "real_model"))
		}
		if model != "" {
			models = append(models, model)
		}
		markSupported(dir, string(action))
	}

	if n := patchDrivelists(models, supported()); n > 0 {
		logf("agent: %d drive list(s) updated for %s", n, strings.Join(models, ", "))
	}
	logf("agent: %d drive(s) marked supported", len(drives))

	// Fixing the database does not make DSM look again by itself. Its verdicts are
	// in a cache under /run/space/, and that cache has to be told to rebuild.
	// Without this, Storage Manager keeps showing "unverified" and "at risk".
	//
	// DB 를 고쳤다고 DSM 이 알아서 다시 보지는 않는다. 판정 결과는
	// /run/space/ 아래 캐시에 있고, 그 캐시를 다시 만들라고 시켜야 한다.
	// 안 하면 저장소 관리자는 계속 "미검증" 과 "위험" 을 보여준다.
	recheckDiskCompatibility()
	// If DSM fetches a new drive database, everything just added disappears with it.
	// DSM 이 드라이브 DB 를 새로 받아오면 방금 넣은 항목이 통째로 사라진다.
	blockDriveDBUpdate()

	if n := blockAutoUpdate(); n > 0 {
		logf("agent: blocked %d auto-update path(s)", n)
	}
	if n, _ := patchAppArmor(); n > 0 {
		logf("agent: refreshed %d AppArmor profile(s)", n)
	}

	// The next four are small, independent follow-ups. All of them have to leave
	// the boot running even on failure, so their return values are only logged.
	// They go from safest to most invasive: smart_test (one synoinfo line),
	// temperature (plants a link), acpid (replaces files, and only where DMI
	// matches), notify (an outbound call).
	//
	// 다음 넷은 각각 독립적인 소소한 사후 조치다. 모두 실패해도 부팅은
	// 계속되어야 하므로 반환값을 로그로만 소비한다. 순서는 안전 -> 침습 순이다:
	// smart_test (synoinfo 한 줄) -> temperature (링크만 심음)
	// -> acpid (DMI 감지 시에만 파일 교체) -> notify (외부 호출).
	if n := enableSmartTestIgnore(); n > 0 {
		logf("agent: set the smart_test skip flag")
	}
	if rc := fixTemperatureMapping(); rc != 0 {
		logf("agent: temperature mapping fix failed (rc=%d)", rc)
	}
	if n := patchACPI(); n > 0 {
		logf("agent: updated %d acpid power button debounce file(s)", n)
	}

	// The signal that DSM has got as far as opening storage, announced outward as
	// a dsm_up event. The boot has to carry on regardless, so the error is ignored.
	//
	// DSM 이 스토리지까지 열었다는 신호. dsm_up 이벤트로 밖에 알린다.
	// 실패해도 부팅은 계속되어야 하므로 오류는 무시한다.
	_ = notify("dsm_up", map[string]string{
		"drives": strings.Join(driveModelsOf(drives), ","),
	})
	return nil
}

// driveModelsOf pulls the model strings out of the drives directories as a
// slice. It is a helper for the notify payload. runAgent already walks these
// once, but rather than reuse that result this re-gathers from the files, to
// keep the flow of the code simple.
//
// driveModelsOf - drives 디렉터리들에서 model 문자열을 뽑아 슬라이스로 만든다.
// notify 페이로드에 넣기 위한 헬퍼다. runAgent 안에서 이미 한 번 훑고
// 있지만 여기선 그 결과를 다시 쓰지 않고 파일에서 재수집한다. 코드 흐름을
// 단순히 유지하기 위함이다.
func driveModelsOf(drives []string) []string {
	var out []string
	for _, dir := range drives {
		m := readTrimmed(filepath.Join(dir, "model"))
		if m == "" {
			m = readTrimmed(filepath.Join(dir, "real_model"))
		}
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

// updateHosts are the hosts DSM asks whether an update exists. Mapping them to
// 127.0.0.1 has DSM hear "no update" and go quiet. nsswitch consults the hosts
// file before DNS, so this is safe without touching the firewall or the routes.
//
// updateHosts - DSM 이 업데이트 존재 여부를 물어보는 호스트들. 이걸 127.0.0.1
// 로 매핑하면 DSM 이 "업데이트 없음" 을 받고 조용해진다. hosts 파일 자체는
// nsswitch 가 DNS 보다 먼저 참조하므로, 방화벽·라우팅을 손대지 않고 안전하다.
var updateHosts = []string{
	"update.synology.com",
	"update7.synology.com",
	"usg.synology.com",
	"myds.synology.com",
	"autoupdate.synology.com",
}

// autoUpdateFiles are what a DSM auto-update leaves behind to apply on the next
// boot: the downloaded archives, plus the updater's log. Left in place, the
// next reboot replaces the kernel with the stock one and the loader effectively
// disappears, which looks like a brick. They are cleared on every boot.
//
// autoUpdateFiles - DSM 자동 업데이트가 다음 부팅에 적용하려고 남겨 둔 것들.
// 받아 둔 아카이브들과 업데이터 로그다. 남아 있으면 다음 재부팅에서 stock 커널로 갈아치우고 우리 로더가 실질적으로
// 사라진다 (brick 처럼 보인다). 매 부팅마다 청소한다.
var autoUpdateFiles = []string{
	"/var/lib/AutoUpdate/DSM/updater/updater.log",
	"/var/lib/AutoUpdate/DSM/updater/update.pat",
	"/var/lib/AutoUpdate/DSM/updater/patch.tar.gz",
	"/tmp/autoupdate.pat",
}

// blockAutoUpdate stops DSM downloading an update and replacing itself. Two
// ways:
//
//  1. point the update hosts at 127.0.0.1 in /etc/hosts, so DSM's query for a
//     new version comes back with nothing.
//  2. delete any update archive already downloaded into the AutoUpdate spool.
//
// synoinfo.conf's rss_server is already redirected to 127.0.0.1 by
// loaderPolicy, but that is the package update channel. A DSM system update
// goes another way and has to be blocked through hosts.
//
// It returns how many paths were actually touched, for the log.
//
// blockAutoUpdate - DSM 이 자동으로 업데이트를 받아 자기 자신을 갈아치우는
// 걸 막는다. 두 갈래:
//
//  1. /etc/hosts 에서 업데이트 호스트를 127.0.0.1 로 돌려, DSM 의 새 버전
//     조회가 빈손으로 돌아오게 한다.
//  2. AutoUpdate 스풀에 이미 받아 둔 업데이트 아카이브를 지운다.
//
// synoinfo.conf 의 rss_server 를 이미 127.0.0.1 로 리다이렉트하지만
// (loaderPolicy), 그건 패키지 업데이트 채널이다. DSM 시스템 업데이트는 별도
// 경로라 hosts 로 막아야 한다.
//
// 반환값은 실제로 손댄 경로 개수 (로그용).
func blockAutoUpdate() int {
	var n int
	if ensureHostsBlocks("/etc/hosts") {
		n++
	}
	for _, p := range autoUpdateFiles {
		if err := os.Remove(p); err == nil {
			n++
		}
	}
	return n
}

// hostsMarker marks the start of the block inserted into the hosts file. On a
// rerun only that block is rewritten; the rest of the hosts file is untouched.
//
// hostsMarker - hosts 파일에 삽입한 블록의 시작 표시. 재실행 시 이 블록만
// 다시 쓴다 (기존 hosts 파일 나머지는 손대지 않는다).
const hostsMarker = "# vibeldr: block DSM auto-update"

// ensureHostsBlocks plants our block in the hosts file and does not add it
// twice if it is already there. It never creates the file - DSM always has one.
//
// ensureHostsBlocks - hosts 파일에 우리 블록을 심고, 이미 있으면 두 번 안
// 붙인다. 파일이 없어도 만들지 않는다 (DSM 은 항상 있다).
func ensureHostsBlocks(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	body := string(raw)
	if strings.Contains(body, hostsMarker) {
		return false // already there / 이미 있음
	}
	var b strings.Builder
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(hostsMarker + "\n")
	for _, h := range updateHosts {
		fmt.Fprintf(&b, "127.0.0.1\t%s\n", h)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		logf("agent: %s: %v", path, err)
		return false
	}
	return true
}

// waitForDrives waits for the storage service to create its per-drive state
// directories. Running before that writes into directories about to be
// replaced, and the result disappears.
//
// waitForDrives - 스토리지 서비스가 드라이브별 상태 디렉터리를 만들 때까지
// 기다린다. 미리 실행하면 곧 교체될 디렉터리에 쓰게 되어 결과가 사라진다.
func waitForDrives() []string {
	for i := 0; i < agentWaitTries; i++ {
		if dirs, _ := filepath.Glob(filepath.Join(storageRunDir, "*")); len(dirs) > 0 {
			return dirs
		}
		time.Sleep(agentWaitPeriod)
	}
	return nil
}

// markSupported writes the verdict for one drive.
//
// The lock file comes first. While the lock is held the storage service treats
// its own cached result as the authoritative answer, and a verdict written
// underneath it is read as stale.
//
// markSupported - 드라이브 하나에 대해 판정을 쓴다.
//
// 락 파일이 먼저다. 락이 잡혀 있는 동안 스토리지 서비스는 자기 캐시된
// 결과를 권위 있는 답으로 취급해서, 그 밑에 쓴 판정은 옛것으로 읽힌다.
func markSupported(dir, action string) {
	for _, l := range []string{"compatibility.lock", "compatibility_action.lock"} {
		_ = os.Remove(filepath.Join(dir, l))
	}
	write := func(name, value string) {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644)
	}
	write("compatibility", "support\n")
	write("force_compatibility", "support\n")
	write("smart_attr_ignore", "1\n")
	write("smart_test_ignore", "1\n")
	// With compatibility_action empty, the UI suggests "replace the drive"
	// whatever compatibility says. So it must not be left empty.
	//
	// compatibility_action 이 비어 있으면 compatibility 가 무엇이든 UI 는
	// "드라이브를 교체하세요" 를 제안한다. 그래서 비워두면 안 된다.
	write("compatibility_action", action)
}

// patchDrivelists adds this machine's drives to the list DSM consults.
//
// The files are JSON per enclosure, keyed by drive model. Adding an entry is
// less invasive than turning the check off: everything else DSM does with that
// answer keeps working, the answer is simply "yes" now.
//
// patchDrivelists - DSM 이 조회하는 목록 자체에 이 머신의 드라이브를 추가한다.
//
// 파일은 인클로저별 JSON 이고 드라이브 모델이 키다. 엔트리 추가가 검사 자체를
// 끄는 것보다 침습성이 적다. DSM 이 그 답으로 하는 다른 일들이 다 계속
// 동작하되, 이제 답이 "예" 로 나올 뿐이다.
func patchDrivelists(models []string, v verdict) int {
	if len(models) == 0 {
		return 0
	}
	return patchDrivelistsIn(compatDBDir, models, v)
}

// patchDrivelistsIn registers the drives in the compatibility database of a
// given directory.
//
// Right before the pivot the installed system is still under /tmpRoot and an
// absolute path does not reach it. So this is split out to take a directory.
//
// patchDrivelistsIn - 지정한 디렉터리의 호환성 DB 에 드라이브를 등록한다.
//
// pivot 직전에는 설치된 시스템이 아직 /tmpRoot 밑에 있어서 절대경로가
// 안 통한다. 그래서 디렉터리를 받는 형태로 나눠 둔다.
func patchDrivelistsIn(dir string, models []string, v verdict) int {
	files, _ := filepath.Glob(filepath.Join(dir, "*_v*.db"))
	var done int
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		info, ok := doc[compatDBKey].(map[string]any)
		if !ok {
			// Some of the files describe rules rather than drives.
			// 일부 파일은 드라이브가 아니라 규칙을 담는다.
			continue
		}
		for _, m := range models {
			info[m] = map[string]any{
				"default": map[string]any{"compatibility_interval": []any{v}},
			}
		}
		doc[compatDBKey] = info
		out, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		if err := os.WriteFile(f, out, 0o644); err != nil {
			logf("agent: %s: %v", f, err)
			continue
		}
		done++
	}
	return done
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// installAgent leaves the loader's resident part inside the installed system.
//
// That is a copy of this binary plus the unit telling systemd when to run it.
// The timing is the point: after the storage service, because that service is
// what makes the verdict this sets out to overturn.
//
// installAgent - 로더의 상주 파트를 설치된 시스템 안에 남긴다.
//
// 이 바이너리의 사본 + systemd 에게 언제 실행할지 알려주는 유닛이다. 타이밍이
// 핵심이다. 스토리지 서비스 뒤여야 한다. 우리가 뒤엎으려는 판정을 그 서비스가
// 내리기 때문이다.
func installAgent(root string) {
	self, err := os.ReadFile("/" + initSelfName)
	if err != nil {
		logf("agent: cannot read self: %v", err)
		return
	}
	if err := os.WriteFile(root+dsmAgentName, self, 0o755); err != nil {
		logf("agent: %s: %v", root+dsmAgentName, err)
		return
	}

	unit := fmt.Sprintf(`[Unit]
Description=vibeldr: tell DSM this machine's drives are usable
Wants=pkgctl-StorageManager.service synostoraged.service
After=pkgctl-StorageManager.service synostoraged.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=-%s -agent

[Install]
WantedBy=multi-user.target
`, dsmAgentName)

	if err := os.MkdirAll(root+dsmWantsDir, 0o755); err != nil {
		logf("agent: %v", err)
		return
	}
	unitPath := root + dsmServiceDir + "/" + dsmServiceName
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		logf("agent: %s: %v", unitPath, err)
		return
	}
	// systemd takes this symlink as the unit being enabled. Creating the link
	// directly lets the loader register the service for a system that is not
	// running right now.
	//
	// systemd 는 이 심볼릭 링크로 유닛을 enable 상태로 간주한다. 직접 링크를
	// 만들면, 지금 돌지 않는 시스템에 대해서도 로더가 서비스를 등록할 수 있다.
	link := root + dsmWantsDir + "/" + dsmServiceName
	_ = os.Remove(link)
	if err := os.Symlink(dsmServiceDir+"/"+dsmServiceName, link); err != nil {
		logf("agent: %s: %v", link, err)
		return
	}
	logf("agent: installed into the system on the disk")
	if ok, err := installStorageManagerDropin(root); err != nil {
		logf("agent: drop-in: %v", err)
	} else if ok {
		logf("agent: installed the storage-manager drop-in")
	}
	if err := backupToDSM(root); err != nil {
		logf("agent: backup: %v", err)
	}
}

// initSelfName is this binary's name at the ramdisk root.
// initSelfName - 램디스크 루트에서 이 바이너리의 이름.
const initSelfName = "vibeldr-init"
