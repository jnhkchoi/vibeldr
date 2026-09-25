package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"vibeldr/internal/dsmconf"
)

// DSM's nightly refresh and its system updates replace
// /var/lib/disk-compatibility/*.db wholesale. dsmagent already puts our drives
// into the list with patchDrivelists at boot, but that happens once, at boot -
// when the database is refreshed after that, our entries disappear and the DSM
// UI goes orange again.
//
// The lasting answer: run our agent once more every time the storage manager
// service restarts. One systemd drop-in is enough, and since it does not touch
// the existing unit, a DSM update overwriting that unit leaves our drop-in
// alive.
//
// DSM 의 nightly 리프레시와 시스템 업데이트는 /var/lib/disk-compatibility/*.db
// 를 통째로 갈아치운다. 부팅 시점의 dsmagent 가 이미 patchDrivelists 로
// 우리 드라이브를 목록에 넣지만, 그건 부팅 때 한 번뿐이다. 그 뒤에 DB 가
// 갱신되면 우리 엔트리가 사라지고, DSM UI 는 다시 주황색으로 돌아온다.
//
// 지속적인 해결책: 스토리지 매니저 서비스가 재기동될 때마다 우리 에이전트를
// 한 번 더 태운다. systemd drop-in 하나면 충분하고, 기존 유닛을 손대지 않아
// DSM 업데이트가 유닛 본체를 덮어써도 우리 drop-in 은 살아남는다.

// storageDropinDirs are where the storage manager service's drop-in can go.
//
// DSM 7.4 keeps that unit in /usr/local/lib/systemd/system. systemd looks for
// <unit>.d/ under every standard unit path, so putting it under /lib usually
// works too, but the same place as the unit is the sure one. Both are used: a
// missing path is created, and a spare drop-in does no harm.
//
// storageDropinDirs - 스토리지 매니저 서비스의 drop-in 을 놓을 자리들.
//
// DSM 7.4 는 이 유닛을 /usr/local/lib/systemd/system 에 둔다. systemd 는
// 표준 유닛 경로 전부에서 <unit>.d/ 를 찾으므로 /lib 쪽에 놓아도 대개
// 먹지만, 유닛과 같은 자리에 두는 쪽이 확실하다. 둘 다 쓴다 - 없는
// 경로는 만들어지고, 여분의 drop-in 은 해가 없다.
var storageDropinDirs = []string{
	"/usr/local/lib/systemd/system/pkgctl-StorageManager.service.d",
	"/lib/systemd/system/pkgctl-StorageManager.service.d",
}

// storageDropinName is the drop-in's file name. It starts with vibeldr so it
// stands out among drop-ins from other vendors.
//
// storageDropinName - drop-in 파일명. vibeldr 로 시작해 여러 벤더 drop-in
// 사이에서도 알아볼 수 있게 한다.
const storageDropinName = "vibeldr.conf"

// storageDropinBody is the drop-in's content. The '-' in front of ExecStartPost
// says to keep the service itself successful even when vibeldr-agent fails,
// which avoids the storage service failing to come up because of us.
//
// storageDropinBody - drop-in 내용. ExecStartPost 앞의 '-' 는 vibeldr-agent
// 가 실패해도 서비스 자체는 성공으로 남기라는 표시다. 스토리지 서비스가
// 우리 때문에 안 뜨는 사태를 피한다.
const storageDropinBody = `[Service]
ExecStartPost=-/usr/sbin/vibeldr-agent -agent
`

// installStorageManagerDropin puts the drop-in under the target root. It
// returns whether a file was actually written or updated; identical content
// already there gives false.
//
// root is the same prefix installAgent works with, the installed system's mount
// point (/tmpRoot during the pivot).
//
// installStorageManagerDropin - 설치 대상 root 밑에 drop-in 을 심는다.
// 반환값은 실제로 파일을 새로 쓰거나 갱신했는지다. 이미 같은 내용이면 false.
//
// root 는 installAgent 가 다루는 것과 같은 접두사 (설치된 시스템의 마운트
// 지점, pivot 중에는 /tmpRoot).
func installStorageManagerDropin(root string) (bool, error) {
	want := []byte(storageDropinBody)
	var wrote bool
	var firstErr error
	for _, d := range storageDropinDirs {
		dir := root + d
		path := filepath.Join(dir, storageDropinName)

		// An existing file with the same content means nothing to do.
		// 기존 파일이 같은 내용이면 아무것도 안 한다.
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, want) {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		wrote = true
	}
	if !wrote && firstErr != nil {
		return false, firstErr
	}
	return wrote, nil
}

// Why the compatibility database is fixed right before the pivot.
//
// The agent (-agent) does the same job, but it runs when the storage manager
// service comes up, and by then DSM's storage subsystem has already judged the
// drives. However well the database is fixed it is one beat late, and Storage
// Manager is left showing the pool as "at risk" and the drives as "unverified".
//
// So it is done once more in the same place as the settings (synoinfo): the
// moment the ramdisk is holding the installed system at /tmpRoot and DSM's init
// has not started yet. Fixed there, the storage service reads a correct
// database from the beginning.
//
// 호환성 DB 를 pivot 직전에 고치는 이유.
//
// 에이전트(-agent) 도 같은 일을 하지만 그건 스토리지 매니저 서비스가 뜰 때
// 도는 것이고, 그때는 DSM 의 스토리지 서브시스템이 드라이브를 이미 판정한
// 뒤다. DB 를 아무리 고쳐도 한 박자 늦어서, 저장소 관리자에 스토리지 풀이
// "위험", 드라이브가 "미검증" 으로 남는다.
//
// 그래서 설정(synoinfo) 과 같은 자리에서 한 번 더 한다: 램디스크가 설치된
// 시스템을 /tmpRoot 에 들고 있고 DSM 의 init 은 아직 시작되지 않은 순간.
// 그때 고쳐두면 스토리지 서비스는 처음부터 올바른 DB 를 읽는다.

// drivesFile is where the drive model names read in the early stage are carried
// over to the pivot stage.
//
// By the time of the pivot, /sys is already unmounted and sysfs cannot be read
// again. So it is read once right after the drivers load, while /sys is still
// alive, written here, and at the pivot only this file is read. It is the same
// arrangement as vibeldr-ports, which carries the bay order across.
//
// drivesFile - 초기 단계에서 읽은 드라이브 모델명을 pivot 단계로 옮기는 자리.
//
// pivot 시점에는 /sys 가 이미 언마운트돼 있어서 sysfs 를 다시 읽을 수 없다.
// 그래서 드라이버를 올린 직후(아직 /sys 가 살아 있을 때) 한 번 읽어 여기
// 적어두고, pivot 때는 이 파일만 읽는다. 베이 순서를 옮기는 vibeldr-ports
// 와 같은 방식이다.
const drivesFile = "/vibeldr-drives"

// saveDriveModels is called in the early stage: it reads sysfs and leaves the
// result in a file.
//
// saveDriveModels - 초기 단계에서 호출한다. sysfs 를 읽어 파일로 남긴다.
func saveDriveModels() {
	models := driveModelsFromSysfs()
	if len(models) == 0 {
		return
	}
	if err := os.WriteFile(drivesFile, []byte(strings.Join(models, "\n")+"\n"), 0o644); err != nil {
		logf("hddb: cannot record drive models: %v", err)
		return
	}
	logf("hddb: recorded drive model(s): %s", strings.Join(models, ", "))
}

// loadDriveModels is called at the pivot stage. With no file it tries sysfs one
// last time.
//
// loadDriveModels - pivot 단계에서 호출한다. 파일이 없으면 sysfs 를 마지막으로
// 한 번 시도한다.
func loadDriveModels() []string {
	raw, err := os.ReadFile(drivesFile)
	if err != nil {
		return driveModelsFromSysfs()
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// patchInstalledDrivelists registers this machine's drives in the installed
// system's compatibility database. root is the installed system's mount point.
//
// patchInstalledDrivelists - 설치된 시스템의 호환성 DB 에 이 기계의
// 드라이브를 등록한다. root 는 설치된 시스템의 마운트 지점이다.
func patchInstalledDrivelists(root string) {
	models := loadDriveModels()
	if len(models) == 0 {
		logf("hddb: no drive models known; compatibility DB left alone")
		return
	}
	n := patchDrivelistsIn(root+compatDBDir, models, supported())
	if n > 0 {
		logf("hddb: %d drive list(s) updated for %s", n, strings.Join(models, ", "))
	}
}

// driveModelsFromSysfs gathers the model names of the attached block devices.
//
// The name DSM looks up in its database is not always the model sysfs reports.
// A QEMU disk appears in sysfs as "QEMU HARDDISK" while DSM's log says
// "HARDDISK". Since either could be the one used, both go in - one extra entry
// the database never looks at does no harm.
//
// driveModelsFromSysfs - 붙어 있는 블록 장치의 모델명을 모은다.
//
// DSM 이 DB 에서 찾을 때 쓰는 이름이 sysfs 의 model 과 항상 같지는 않다.
// QEMU 디스크는 sysfs 에 "QEMU HARDDISK" 로 보이는데 DSM 로그에는
// "HARDDISK" 로 나온다. 어느 쪽이 쓰일지 모르니 둘 다 넣는다 - DB 에
// 안 쓰이는 항목이 하나 더 있어도 해가 없다.
func driveModelsFromSysfs() []string {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join("/sys/block", e.Name(), "device", "model"))
		if err != nil {
			continue
		}
		m := strings.TrimSpace(string(raw))
		add(m)
		if i := strings.LastIndexByte(m, ' '); i >= 0 {
			add(m[i+1:])
		}
	}
	return out
}

// recheckDiskCompatibility tells DSM to judge disk compatibility again.
//
// Fixing the compatibility database alone does not change the screen. DSM
// keeps its verdicts in a cache under /run/space/, and Storage Manager reads
// that cache. Until the cache is refreshed the drives stay "unverified" and
// the pool "at risk". The real log shows that failure as it is:
//
//	disk_compatibility_cache_update.c:138 Failed to update cache
//	space_pool_disk_compat.c:314 Failed to copy '/var/lib/space/pool_compatibility'
//	                             to '/run/space/pool_compatibility'
//
// synostgdisk is DSM's own tool, and this one call rebuilds the cache.
//
// recheckDiskCompatibility - DSM 에게 디스크 호환성을 다시 판정하라고 시킨다.
//
// 호환성 DB 를 고치는 것만으로는 화면이 안 바뀐다. DSM 은 판정 결과를
// /run/space/ 아래 캐시에 두고 저장소 관리자는 그 캐시를 읽는다. 캐시가
// 갱신되지 않으면 드라이브는 "미검증", 스토리지 풀은 "위험" 으로 남는다.
// 실제 로그가 그 실패를 그대로 보여준다:
//
//	disk_compatibility_cache_update.c:138 Failed to update cache
//	space_pool_disk_compat.c:314 Failed to copy '/var/lib/space/pool_compatibility'
//	                             to '/run/space/pool_compatibility'
//
// synostgdisk 는 DSM 자신의 도구이고, 이 한 줄이 캐시를 다시 만든다.
func recheckDiskCompatibility() {
	const tool = "/usr/syno/sbin/synostgdisk"
	if _, err := os.Stat(tool); err != nil {
		return
	}
	// Without /run/space the cache copy fails outright, so it is created first.
	// /run/space 가 없으면 캐시 복사 자체가 실패한다. 먼저 만들어 둔다.
	if err := os.MkdirAll("/run/space", 0o755); err != nil {
		logf("hddb: cannot create /run/space: %v", err)
	}
	out, err := exec.Command(tool, "--check-all-disks-compatibility").CombinedOutput()
	if err != nil {
		logf("hddb: recheck failed: %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	logf("hddb: asked DSM to re-check disk compatibility")
}

// onlinePackINFO are the candidate metadata files of the package DSM fetches
// its drive database from.
//
// onlinePackINFO - DSM 이 드라이브 DB 를 받아오는 패키지의 메타파일 후보들.
var onlinePackINFO = []string{
	"/var/packages/SynoOnlinePack_v3/INFO",
	"/var/packages/SynoOnlinePack_v2/INFO",
	"/var/packages/SynoOnlinePack/INFO",
}

// blockDriveDBUpdate stops the drive database updating itself.
//
// DSM fetches a new drive database periodically and replaces
// /var/lib/disk-compatibility wholesale. The drives just added disappear with
// it, and days later they suddenly read "unverified" again.
//
// The way to stop it is to make the version start at 9999. DSM then sees its
// own version as already higher than the server's and downloads nothing. That
// is easier to undo than deleting files or turning the service off, and the
// package keeps behaving as it did.
//
// blockDriveDBUpdate - 드라이브 DB 자동 갱신을 막는다.
//
// DSM 은 주기적으로 새 드라이브 DB 를 받아 /var/lib/disk-compatibility 를
// 통째로 갈아치운다. 그러면 방금 넣은 우리 드라이브가 사라지고 며칠 뒤
// 갑자기 "미검증" 으로 돌아온다.
//
// 막는 방법은 버전을 9999 로 시작하게 만드는 것이다. DSM 은 자기 버전이 이미
// 서버 것보다 높다고 보고 내려받지 않는다. 파일을 지우거나 서비스를 끄는
// 것보다 되돌리기 쉽고, 패키지 동작 자체는 그대로다.
func blockDriveDBUpdate() {
	for _, p := range onlinePackINFO {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		body := string(raw)
		v, ok := dsmconf.Get(body, "version")
		if !ok || strings.HasPrefix(v, "9999") {
			return
		}
		out := dsmconf.Set(body, "version", "9999"+v)
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			logf("hddb: cannot pin %s: %v", p, err)
			return
		}
		logf("hddb: drive db auto-update disabled (%s)", filepath.Base(filepath.Dir(p)))
		return
	}
}
