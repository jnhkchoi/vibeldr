package ramdisk

// stock.go edits a few of DSM's own files in the ramdisk, for the stage before
// the pivot - the installer (junior) and the boot script - which the helper
// cannot reach in time.
//
//   - etc/synoinfo.conf gets the settings the build carries (SynoinfoName),
//     except the update servers (dsmconf.InstalledOnly), which the installer
//     needs for its online install.
//     The installer's /etc/rc reads several of them from here before anything
//     of ours runs: the network card order, the Broadwell-DE and SAS probes,
//     the DS918+ drive power monitor. The installed system has its own copy,
//     which the helper writes before the pivot (cmd/vibeldr-init/settings.go).
//
//   - usr/syno/web/webman/get_state.cgi fills disabled_port_disks from
//     `synodiskport -portthawlist`, and the web installer shows a disk listed
//     there as a drive error on a disabled SATA port, asking for it to be
//     replaced. The ports here are laid out by the loader (the port map or the
//     device tree), not by the board synodiskport was written for, so its
//     verdict is not used and the list is emptied.
//
//   - linuxrc.syno.impl runs `broadcom_update.sh <root> update` from the
//     installed system on a normal boot, and reboots at once when it returns
//     1. It is Synology's firmware updater for the Broadcom network cards it
//     ships. On other hardware that is the same kind of risk as the BIOS
//     flasher the helper guards against (guard.go), so the boot script is
//     pointed at a name that does not exist and skips the block.
//
//   - etc/sysconfig/network-scripts gets the ifcfg-ethN files it lacks up to
//     eth7, so the installer brings up every network card (addIfcfgs).
//
//   - With the release the build used (dsmconf.ReleaseName), the ramdisk gets
//     a release list of its own, and its synoinfo.conf points rss_server_ssl
//     at the helper that serves it, so the web installer's online install
//     offers that release (dsmconf/rss.go, addInstallRSS).
//
// Each edit is a fixed-string replacement of a line that is the same in the
// three DSM 7.4.1 ramdisks. A line that is not found is left alone and
// reported, so a DSM that changed it shows up in the build log instead of
// breaking the build.
//
// stock.go - 램디스크 안의 DSM 자신의 파일 몇 개를 고친다. pivot 전 단계, 곧
// 설치기(junior)와 부팅 스크립트를 위한 것으로, 헬퍼는 제때 거기 닿지 못한다.
//
//   - etc/synoinfo.conf 에 빌드가 실어 온 설정(SynoinfoName)을 쓴다. 업데이트
//     서버(dsmconf.InstalledOnly)는 빼는데, 설치기가 온라인 설치에 쓰기
//     때문이다. 설치기의 /etc/rc 는 우리 것이 돌기 전에 그중 몇 개를 여기서
//     읽는다. 랜카드 순서, Broadwell-DE 와 SAS 검사, DS918+ 드라이브 전원
//     감시. 설치된 시스템에는 따로 사본이 있고, 헬퍼가 pivot 전에 쓴다
//     (cmd/vibeldr-init/settings.go).
//
//   - usr/syno/web/webman/get_state.cgi 는 disabled_port_disks 를
//     `synodiskport -portthawlist` 로 채우고, 웹 설치기는 거기 적힌 디스크를
//     꺼진 SATA 포트의 드라이브 오류로 보여주며 교체하라고 한다. 이 기계의
//     포트는 synodiskport 가 전제한 보드가 아니라 로더(포트맵이나 device
//     tree)가 배치하므로, 그 판정은 쓰지 않고 목록을 비운다.
//
//   - linuxrc.syno.impl 은 일반 부팅 때 설치된 시스템의
//     `broadcom_update.sh <root> update` 를 돌리고, 1 을 돌려주면 바로
//     재부팅한다. 시놀로지가 싣는 브로드컴 랜카드용 펌웨어 갱신기다. 다른
//     하드웨어에서는 헬퍼가 막는 BIOS 플래셔(guard.go)와 같은 종류의 위험이라,
//     부팅 스크립트가 없는 이름을 보게 해 그 블록을 건너뛰게 한다.
//
//   - etc/sysconfig/network-scripts 에 없는 ifcfg-ethN 을 eth7 까지 채워, 설치기가
//     모든 랜카드를 올리게 한다 (addIfcfgs).
//
//   - 빌드가 쓴 릴리스(dsmconf.ReleaseName)가 있으면 램디스크에 자기 릴리스
//     목록을 넣고, 그 synoinfo.conf 의 rss_server_ssl 을 목록을 주는 헬퍼로
//     돌려, 웹 설치기의 온라인 설치가 그 릴리스를 내놓게 한다 (dsmconf/rss.go,
//     addInstallRSS).
//
// 수정마다 세 DSM 7.4.1 램디스크에서 똑같은 줄을 고정 문자열로 바꾼다. 줄을
// 못 찾으면 건드리지 않고 보고만 해서, 그 줄을 바꾼 DSM 은 빌드를 깨는 대신
// 빌드 로그에 드러난다.

import (
	"fmt"
	"strings"

	"vibeldr/internal/dsmconf"
)

const (
	synoinfoConf = "etc/synoinfo.conf"
	versionFile  = "etc/VERSION"
	getStateCGI  = "usr/syno/web/webman/get_state.cgi"
)

// stockEdit is one fixed-string replacement in one file.
// stockEdit - 한 파일에서 고정 문자열 하나를 바꾸는 수정.
type stockEdit struct {
	name, file, from, to string
}

var stockEdits = []stockEdit{
	{
		name: "disabled port disks",
		file: getStateCGI,
		from: `DisabledPortDisks="$(/usr/syno/bin/synodiskport -portthawlist)"`,
		to:   `DisabledPortDisks=""`,
	},
	{
		name: "broadcom_update.sh",
		file: linuxrcName,
		from: `if [ -f "$Mnt"/usr/syno/sbin/broadcom_update.sh ]; then`,
		to:   `if [ -f "$Mnt"/usr/syno/sbin/broadcom_update.sh.vibeldr-skip ]; then`,
	},
}

// editStock makes the edits above, and writes settings into the ramdisk's
// synoinfo.conf when the build carries any, and the release list when it
// carries its release. It returns what was changed and what could not be
// found.
//
// etc.defaults is a symlink to etc in the DSM 7.4.1 ramdisks, so the one
// regular file covers both names; a separate etc.defaults/synoinfo.conf is
// edited too if a ramdisk has one.
//
// editStock - 위 수정을 하고, 빌드가 설정을 실어 왔으면 램디스크의
// synoinfo.conf 에, 릴리스를 실어 왔으면 릴리스 목록을 쓴다. 바꾼 것과 못 찾은
// 것을 돌려준다.
//
// DSM 7.4.1 램디스크에서 etc.defaults 는 etc 로의 심볼릭 링크라, 일반 파일
// 하나가 두 이름을 다 맡는다. 따로 된 etc.defaults/synoinfo.conf 가 있는
// 램디스크라면 그것도 고친다.
func (a *Archive) editStock(settings, release []byte) (done, missing []string) {
	var kv [][2]string
	for _, e := range dsmconf.ParseList(string(settings)) {
		if !dsmconf.InstalledOnly[e[0]] {
			kv = append(kv, e)
		}
	}
	if len(kv) > 0 {
		if a.setSynoinfo(kv) {
			done = append(done, "synoinfo.conf")
		} else {
			missing = append(missing, "synoinfo.conf")
		}
	}
	if url, md5 := dsmconf.ParseRelease(string(release)); url != "" && md5 != "" {
		if err := a.addInstallRSS(url, md5); err != nil {
			missing = append(missing, "install RSS ("+err.Error()+")")
		} else {
			done = append(done, "install RSS")
		}
	}
	for _, s := range stockEdits {
		e, ok := a.Get(s.file)
		if !ok || !e.IsRegular() || !strings.Contains(string(e.Data), s.from) {
			missing = append(missing, s.name)
			continue
		}
		e.Data = []byte(strings.Replace(string(e.Data), s.from, s.to, 1))
		done = append(done, s.name)
	}
	if added := a.addIfcfgs(); len(added) > 0 {
		done = append(done, "ifcfg "+strings.Join(added, " "))
	}
	return done, missing
}

// setSynoinfo writes kv into the ramdisk's synoinfo.conf and reports whether
// there was one to write into.
//
// setSynoinfo - kv 를 램디스크의 synoinfo.conf 에 쓰고, 쓸 파일이 있었는지
// 알린다.
func (a *Archive) setSynoinfo(kv [][2]string) bool {
	var n int
	for _, name := range []string{synoinfoConf, "etc.defaults/synoinfo.conf"} {
		e, ok := a.Get(name)
		if !ok || !e.IsRegular() {
			continue
		}
		e.Data = []byte(dsmconf.SetAll(string(e.Data), kv))
		n++
	}
	return n > 0
}

// addInstallRSS writes the release list for this ramdisk's model and version
// and points rss_server_ssl at the helper that serves it.
//
// addInstallRSS - 이 램디스크의 모델·버전으로 릴리스 목록을 쓰고
// rss_server_ssl 을 그것을 주는 헬퍼로 돌린다.
func (a *Archive) addInstallRSS(url, md5 string) error {
	ver, ok := a.Get(versionFile)
	info, ok2 := a.Get(synoinfoConf)
	if !ok || !ok2 || !ver.IsRegular() || !info.IsRegular() {
		return fmt.Errorf("no %s or %s", versionFile, synoinfoConf)
	}
	list, err := dsmconf.InstallRSS(string(ver.Data), string(info.Data), url, md5)
	if err != nil {
		return err
	}
	if err := a.AddOrReplace(dsmconf.InstallRSSName, 0o644, list); err != nil {
		return err
	}
	a.setSynoinfo([][2]string{{"rss_server_ssl", dsmconf.InstallRSSURL}})
	return nil
}

// ifcfgDir holds the installer's per-interface network settings.
// ifcfgDir - 설치기의 인터페이스별 네트워크 설정이 있는 곳.
const ifcfgDir = "etc/sysconfig/network-scripts"

// ifcfgCount is how many ethN interfaces the installer should bring up: the
// eight the loader lets DSM use.
//
// ifcfgCount - 설치기가 올려야 할 ethN 인터페이스 수. 로더가 DSM 에 허용하는
// 여덟 개다.
const ifcfgCount = 8

// addIfcfgs gives the installer a DHCP ifcfg file for each of eth0..eth7 that
// the ramdisk lacks, in the form the stock files have, and returns the ones it
// added.
//
// The installer's rc.network brings up only the interfaces that have a file
// (it lists ifcfg-*). The DS918+ ramdisk has eth0 and eth1 alone, so with the
// cable in a third card the installer never gets an address and cannot be
// found. The DS3622xs+ and SA6400 ramdisks already have eth0-eth8 and
// eth0-eth13, whether or not the cards exist.
//
// addIfcfgs - 램디스크에 없는 eth0..eth7 마다 기본 파일과 같은 형식의 DHCP
// ifcfg 파일을 설치기에 주고, 더한 것을 돌려준다.
//
// 설치기의 rc.network 는 파일이 있는 인터페이스만 올린다 (ifcfg-* 를 나열).
// DS918+ 램디스크에는 eth0 과 eth1 뿐이라, 세 번째 카드에 케이블을 꽂으면
// 설치기가 주소를 못 받아 찾을 수 없다. DS3622xs+ 와 SA6400 램디스크는 카드가
// 있든 없든 eth0-eth8, eth0-eth13 을 이미 갖고 있다.
func (a *Archive) addIfcfgs() []string {
	var added []string
	for i := 0; i < ifcfgCount; i++ {
		dev := fmt.Sprintf("eth%d", i)
		name := ifcfgDir + "/ifcfg-" + dev
		if _, ok := a.Get(name); ok {
			continue
		}
		body := "DEVICE=" + dev + "\nBOOTPROTO=dhcp\nONBOOT=yes\nIPV6INIT=off\n"
		if err := a.AddOrReplace(name, 0o644, []byte(body)); err != nil {
			continue
		}
		added = append(added, dev)
	}
	return added
}
