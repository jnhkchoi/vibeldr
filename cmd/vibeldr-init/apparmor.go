package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AME (Advanced Media Extensions, package name CodecPack) is refused the mount
// of /tmp/synoboot inside its AppArmor profile. On Synology hardware the boot
// media sits elsewhere and this mount is not needed, but under this loader
// CodecPack works only once /dev/synoboot* is mounted on /tmp/synoboot.
//
// The typical refusal in the log:
//
//	DENIED { mount } profile="/usr/syno/etc/CodecPack/CodecPack"
//	                 name="/tmp/synoboot"
//
// The fix adds one mount rule and read access to /dev/synoboot* to the
// profile. A DSM update that replaces the profile means adding it again, so
// this is idempotent.
//
// AME (Advanced Media Extensions, 패키지명 CodecPack) 은 AppArmor 프로파일
// 안에서 /tmp/synoboot 에 대한 mount 를 거부당한다. 시놀로지 하드웨어에서는
// 실제 부트 미디어 위치가 다르고 이 mount 가 필요없지만, 우리 로더 환경에서는
// /dev/synoboot* 을 /tmp/synoboot 로 mount 해야 CodecPack 이 동작한다.
//
// 로그에 남는 전형적인 거부:
//
//	DENIED { mount } profile="/usr/syno/etc/CodecPack/CodecPack"
//	                 name="/tmp/synoboot"
//
// 해결은 프로파일 안에 mount rule 하나 + /dev/synoboot* 읽기 권한을 추가.
// DSM 업데이트가 프로파일을 갈아치우면 다시 붙여야 하므로 idempotent.

// apparmorProfileDir is where the AppArmor profile files live.
// apparmorProfileDir - AppArmor 프로파일 파일들이 있는 표준 위치.
const apparmorProfileDir = "/etc/apparmor.d"

// apparmorMarker marks the block we put in. A profile that already contains
// this string is left alone.
//
// apparmorMarker - 우리가 심은 블록의 표시. 이 문자열이 프로파일 안에
// 이미 있으면 손대지 않는다.
const apparmorMarker = "# vibeldr: allow synoboot mount"

// apparmorRule is the rule inserted just before a profile's closing '}'.
// apparmorRule - 프로파일의 닫는 '}' 바로 앞에 삽입하는 규칙.
const apparmorRule = `# vibeldr: allow synoboot mount
mount -> /tmp/synoboot,
/dev/synoboot* r,
`

// patchAppArmor finds the AppArmor profiles for CodecPack and AME and adds the
// rule that allows the mount. It returns how many profiles were actually
// updated.
//
// An updated profile is reloaded with apparmor_parser -r. Where that binary is
// absent, as on some slim DSM builds, it only logs and leaves the kernel to
// read the new profiles on the next boot.
//
// patchAppArmor - CodecPack / AME 관련 AppArmor 프로파일을 찾아 mount
// 허용 규칙을 추가한다. 반환값은 실제로 갱신된 프로파일 개수다.
//
// 갱신한 프로파일은 apparmor_parser -r 로 재적재한다. 바이너리가 없으면
// (일부 슬림 DSM 빌드) 로그만 남기고 다음 부팅에서 커널이 새로 읽도록 둔다.
func patchAppArmor() (int, error) {
	entries, err := os.ReadDir(apparmorProfileDir)
	if err != nil {
		return 0, err
	}
	var n int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !apparmorProfileMatch(name) {
			continue
		}
		path := filepath.Join(apparmorProfileDir, name)
		ok, err := patchAppArmorFile(path)
		if err != nil {
			logf("agent: apparmor: %s: %v", path, err)
			continue
		}
		if ok {
			n++
			reloadAppArmor(path)
		}
	}
	return n, nil
}

// apparmorProfileMatch reports whether a file name contains CodecPack or ame,
// case insensitively. Those are the two spellings observed in how Synology
// names these profiles.
//
// apparmorProfileMatch - 파일명이 CodecPack 또는 ame 를 포함하는지. 대소문자는
// 무시한다. 시놀로지가 프로파일 이름을 어떻게 짓는지 관찰해서 얻은 두 표기다.
func apparmorProfileMatch(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "codecpack") || strings.Contains(lower, "ame")
}

// patchAppArmorFile puts the rule into one profile file, returning false when
// it is already there. The rule block goes in before the file's last '}', on
// the assumption that the file holds a single profile, as AppArmor convention
// has it.
//
// patchAppArmorFile - 한 프로파일 파일에 규칙을 넣는다. 이미 있으면 false.
// 프로파일의 마지막 '}' 앞에 규칙 블록을 끼워넣는다 (프로파일 하나짜리
// 파일이라는 가정 - AppArmor 관례).
func patchAppArmorFile(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	body := string(raw)
	if strings.Contains(body, apparmorMarker) {
		return false, nil
	}
	idx := strings.LastIndex(body, "}")
	if idx < 0 {
		// A file with no '}' is not a profile we understand.
		// '}' 가 없는 파일은 우리가 이해하는 프로파일이 아니다.
		return false, nil
	}
	patched := body[:idx] + apparmorRule + body[idx:]
	if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// reloadAppArmor reloads the profile at once when apparmor_parser is on PATH.
// When it is not, it logs one line and moves on - the kernel reads the new
// profile on the next boot.
//
// reloadAppArmor - apparmor_parser 가 PATH 에 있으면 프로파일을 즉시 재적재한다.
// 없으면 로그 한 줄만 남기고 넘어간다 - 커널이 다음 부팅에 새 프로파일을 읽는다.
func reloadAppArmor(path string) {
	bin, err := exec.LookPath("apparmor_parser")
	if err != nil {
		logf("agent: no apparmor_parser; %s takes effect on the next boot", path)
		return
	}
	if err := exec.Command(bin, "-r", path).Run(); err != nil {
		logf("agent: apparmor_parser -r %s: %v", path, err)
	}
}
