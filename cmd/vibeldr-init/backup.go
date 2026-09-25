package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vibeldr/internal/ramdisk"
)

// The loader settings' backup of themselves.
//
// The loader partition goes away on one mistake: the USB gets reformatted, a
// `vibeldr image` is interrupted, a fresh loader is burned to reuse the
// machine. But DSM stores its own identity (serial number, MAC, model), and a
// loader booted with values different from those is not recognised as the same
// DSM installation. Losing the loader settings means losing the continuity of
// the install.
//
// The safety net: a small copy of the loader's settings is left in a hidden
// directory inside the installed DSM. Starting again with a new loader, the
// identity.cfg kept here is copied onto its partition 1 by hand to restore the
// identity. Nothing reads this backup automatically.
//
// Why /root/.vibeldr/backup:
//   - /root sits on the root fs (/dev/md0), unaffected by a volume being
//     rebuilt or moved.
//   - DSM itself never touches /root; root's home belongs to the system.
//   - being hidden, it rarely shows up in a file manager.
//
// 로더 설정의 자가 백업.
//
// 로더 파티션은 실수 하나로 사라진다: 사용자가 USB 를 다시 포맷하거나,
// `vibeldr image` 가 중간에 끊기거나, 머신을 재활용하려고 새 로더를
// 굽는다. 하지만 DSM 은 자기 정체성 (SN, MAC, 모델) 을 스스로 저장하고
// 있어, 이 값들과 다르게 부팅된 로더는 같은 DSM 설치로 인식되지 않는다.
// 즉 로더 설정을 잃으면 설치의 연속성을 잃는다.
//
// 안전망: 로더의 작은 설정 사본을 설치된 DSM 안의 숨김 디렉터리에 남긴다.
// 새 로더로 다시 시작할 때 여기 있는 identity.cfg 를 손으로 그 로더의
// 파티션 1 에 복사하면 정체성이 복원된다. 이 백업을 자동으로 읽는 코드는 없다.
//
// /root/.vibeldr/backup 을 고른 이유:
//   - /root 는 루트 fs (/dev/md0) 에 있어, 볼륨을 다시 만들거나 옮겨도
//     영향이 없다.
//   - DSM 자체는 /root 를 건드리지 않는다. root 홈은 시스템 몫이다.
//   - 숨김 디렉터리라 파일 관리자에 잘 드러나지 않는다.

// backupDirRel is the backup's path inside the installed DSM, under root's home.
// backupDirRel - 설치된 DSM 안의 백업 경로. root 홈 밑.
const backupDirRel = "/root/.vibeldr/backup"

// The backup file names. There are only three and all of them are small.
// 백업 파일 이름들. 세 개뿐이고 전부 작다.
const (
	backupIdentityName = "identity.cfg"
	backupSynoinfoName = "synoinfo.conf"
	backupManifestName = "manifest.json"
)

// backupIdentityKeys are the command-line keys to consult when reassembling
// identity.cfg. It is the same list as identityKeys in the original
// (cmd/vibeldr), and these are the values that decide which machine this is.
//
// backupIdentityKeys - identity.cfg 를 재조립할 때 참조할 커맨드라인 키.
// 원본 (cmd/vibeldr) 의 identityKeys 와 같은 목록이며, 이 머신이 "어느
// 머신인지" 를 결정하는 값들이다.
var backupIdentityKeys = []string{
	"syno_hw_version",
	"sn",
	"netif_num",
	"mac1", "mac2", "mac3", "mac4",
	"vid", "pid",
}

// Manifest is the metadata left so that it can be told which loader a backup
// came from. No random field goes in, so the result stays reproducible.
// Timestamp is included deliberately.
//
// Manifest - 백업이 어느 로더에서 나온 것인지 알아볼 수 있게 남기는 메타.
// 재현 가능성을 위해 랜덤 필드는 넣지 않는다. Timestamp 는 의도적으로 포함한다.
type Manifest struct {
	Model     string   `json:"model"`
	Version   string   `json:"version"`
	BuildID   string   `json:"build_id"`
	Timestamp string   `json:"timestamp"`
	Serial    string   `json:"serial"`
	MACs      []string `json:"macs"`
}

// backupToDSM leaves a small copy of the loader's settings under
// /root/.vibeldr/backup in the installed DSM. It is called at the end of
// installAgent, right before the pivot.
//
// Three files: identity.cfg (for recovering the serial number and MACs),
// synoinfo.conf (the carried copy as it is) and manifest.json (model, DSM
// version, build id, timestamp).
//
// Every write is atomic, a tempfile plus a rename, leaving no trace of a
// partial write.
//
// backupToDSM - 설치된 DSM 의 /root/.vibeldr/backup 아래에 로더의 작은
// 설정 사본을 남긴다. pivot 직전 installAgent 마지막에서 호출된다.
//
// 세 파일: identity.cfg (SN + MAC 복구용), synoinfo.conf (실어 온 사본
// 그대로), manifest.json (모델/DSM 버전/빌드ID/타임스탬프).
//
// 모든 쓰기는 tempfile + rename 으로 원자적이다. 부분 쓰기 흔적을 남기지 않는다.
func backupToDSM(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(backupDirRel))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	values := backupCmdlineValues()

	// identity.cfg: the identity this boot used, reassembled into a form GRUB
	// can read again. With this file as it is, the machine boots with the same
	// serial number and MAC.
	//
	// identity.cfg: 지금 부팅한 정체성을 GRUB 이 다시 읽을 수 있는 형태로
	// 재조립한다. 이 파일 그대로면 같은 SN/MAC 로 다시 부팅된다.
	if id := buildIdentityCfg(values); len(id) > 0 {
		if err := writeAtomic(filepath.Join(dir, backupIdentityName), id, 0o600); err != nil {
			return fmt.Errorf("identity: %w", err)
		}
	}

	// synoinfo.conf: the copy the image builder put in the ramdisk, copied as
	// it is. That copy is the original applied to the installed system on every
	// boot, so backing it up is the only way to come back to the same state.
	//
	// synoinfo.conf: 이미지 빌더가 램디스크에 실어 놓은 사본을 그대로 복사한다.
	// 이 사본이 매 부팅마다 설치 시스템에 적용되는 원본이라, 그걸 백업하는
	// 게 재부팅해도 같은 상태로 돌아올 수 있게 하는 유일한 길이다.
	if raw, err := os.ReadFile("/" + ramdisk.SynoinfoName); err == nil {
		if err := writeAtomic(filepath.Join(dir, backupSynoinfoName), raw, 0o600); err != nil {
			return fmt.Errorf("synoinfo: %w", err)
		}
	}

	m := currentManifest(root, values)
	out, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeAtomic(filepath.Join(dir, backupManifestName), out, 0o600)
}

// currentManifest gathers this boot's identity from the command line and from
// the installed system's VERSION file. These are the values the boot actually
// used, so whoever made the backup can later tell which point it is a snapshot
// of.
//
// currentManifest - 이번 부팅의 정체성을 커맨드라인과 설치 시스템의
// VERSION 파일에서 모아온다. 부팅될 때 사용된 값이라, 이 백업이 어느
// 시점의 스냅샷인지 만든이가 나중에 알아볼 수 있다.
func currentManifest(root string, values map[string]string) Manifest {
	m := Manifest{
		Model:     values["syno_hw_version"],
		Serial:    values["sn"],
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	for _, k := range []string{"mac1", "mac2", "mac3", "mac4"} {
		if v := values[k]; v != "" {
			m.MACs = append(m.MACs, v)
		}
	}
	m.Version = readInstalledVersion(root)
	if raw, err := os.ReadFile("/" + ramdisk.BuildIDName); err == nil {
		m.BuildID = strings.TrimSpace(string(raw))
	}
	return m
}

// backupCmdlineValues parses /proc/cmdline into key to value. An option with no
// value is left as an empty string.
//
// backupCmdlineValues - /proc/cmdline 을 key->value 로 파싱한다. 값 없는
// 옵션은 빈 문자열로 남는다.
func backupCmdlineValues() map[string]string {
	out := map[string]string{}
	for _, f := range cmdlineFields() {
		if name, val, ok := strings.Cut(f, "="); ok {
			out[name] = val
		} else {
			out[f] = ""
		}
	}
	return out
}

// buildIdentityCfg serialises the identity into the form GRUB reads back.
//
// The original identityTemplate leaves the values commented out (`# set K=V`)
// for the user to uncomment and override. A backup is the other way round: the
// values currently in use are written uncommented, so that as long as this file
// is on partition 1 the machine boots with this identity.
//
// buildIdentityCfg - GRUB 이 다시 읽을 형태로 정체성을 직렬화한다.
//
// 원본 identityTemplate 은 값들을 주석으로 (`# set K=V`) 남겨 사용자가
// uncomment 해서 덮어쓰게 한다. 백업은 반대다: 지금 살아 움직이는 값을
// uncomment 상태로 적어두어, 이 파일이 파티션 1 에 있는 한 이 정체성으로
// 다시 부팅된다.
func buildIdentityCfg(values map[string]string) []byte {
	var b strings.Builder
	b.WriteString("# vibeldr identity backup — 새 로더의 파티션 1 로 복사하면 복원.\n")
	b.WriteString("# GRUB 이 커널 시작 전에 읽음. 각 줄은 커맨드라인 값을 덮어쓴다.\n\n")
	var written int
	for _, k := range backupIdentityKeys {
		v, ok := values[k]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "set %s=%s\n", k, v)
		written++
	}
	if written == 0 {
		return nil
	}
	return []byte(b.String())
}

// readInstalledVersion reads productversion out of the installed DSM's VERSION
// file. The format is a shell variable file: productversion="7.2.1".
//
// readInstalledVersion - 설치된 DSM 의 VERSION 파일에서 productversion 을
// 읽는다. 형식은 shell 변수 파일이다: productversion="7.2.1".
func readInstalledVersion(root string) string {
	for _, rel := range []string{"/etc.defaults/VERSION", "/etc/VERSION"} {
		raw, err := os.ReadFile(root + rel)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "productversion") {
				continue
			}
			_, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// writeAtomic writes to a temporary file in the same directory and puts it in
// place with a rename, so a sudden shutdown never leaves a partial write at the
// target path.
//
// writeAtomic - 같은 디렉터리의 임시 파일에 쓴 뒤 rename 으로 자리 잡는다.
// 갑작스런 종료 시 대상 경로에 부분 쓰기가 남지 않게 하려는 것이다.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vibeldr-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if cerr != nil {
		os.Remove(name)
		return cerr
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
