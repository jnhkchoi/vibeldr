package main

import (
	"os"
	"strings"

	"vibeldr/internal/dsmconf"
	"vibeldr/internal/ramdisk"
)

// Writing the settings that belong to the installed system, on every boot.
//
// The ramdisk has a copy of synoinfo.conf too, but touching that one is
// pointless: DSM lays a fresh copy down from the .pat while installing and
// reads that one from then on. So the settings travel inside the ramdisk as a
// plain list, and go over to the installed system at the last moment the loader
// can still reach it - right before the pivot.
//
// Applying them on every boot rather than once is deliberate. A DSM update
// replaces these files, and a setting that survived the install but vanished at
// the first update would surface as a problem months later, with nobody
// connecting it back to the loader.
//
// 설치된 시스템에 속하는 설정을, 매 부팅마다 다시 쓴다.
//
// 램디스크에도 synoinfo.conf 사본이 있지만 거기 손대봐야 소용없다.
// DSM 은 인스톨할 때 .pat 에서 새 사본을 깔고, 그 뒤로는 그걸 읽는다.
// 그래서 설정은 램디스크 안에 평범한 목록으로 실어 나르고, 로더가 아직
// 손이 닿는 마지막 순간(pivot 직전) 에 설치된 시스템 쪽으로 써 넣는다.
//
// 한 번만이 아니라 매 부팅마다 적용하는 게 의도다. DSM 업데이트는 이
// 파일들을 갈아치우므로, 인스톨은 살아남았지만 첫 업데이트에서 사라진
// 설정이 몇 달 뒤에 문제로 튀어나오면 아무도 로더와 연결짓지 않는다.

// synoinfoFiles are the two copies DSM keeps. defaults is what a factory reset
// restores from, so fixing the live copy alone is undone by a reset.
//
// synoinfoFiles - DSM 이 유지하는 두 사본. defaults 는 공장 초기화가
// 복원해가는 자리라, 라이브 사본만 고치면 초기화로 원상복구된다.
var synoinfoFiles = []string{"/etc/synoinfo.conf", "/etc.defaults/synoinfo.conf"}

// applyInstalledSettings writes the settings carried along into the installed
// system.
//
// applyInstalledSettings - 실어 온 설정을 설치된 시스템에 쓴다.
func applyInstalledSettings(root string) {
	kv, err := carriedSettings()
	if err != nil {
		if !os.IsNotExist(err) {
			logf("settings: %v", err)
		}
		return
	}
	if len(kv) == 0 {
		return
	}

	var done int
	for _, rel := range synoinfoFiles {
		path := root + rel
		raw, err := os.ReadFile(path)
		if err != nil {
			// /etc.defaults is a symlink to /etc on some layouts, so one of the
			// two being absent is normal rather than a problem.
			//
			// 어떤 배치에서는 /etc.defaults 가 /etc 로의 심볼릭 링크라, 둘 중
			// 하나가 없는 것은 문제가 아니라 정상이다.
			continue
		}
		out := dsmconf.SetAll(string(raw), kv)
		if out == string(raw) {
			done++
			continue
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			logf("settings: %s: %v", path, err)
			continue
		}
		done++
	}
	if done == 0 {
		logf("settings: no synoinfo.conf could be written under %s", root)
		return
	}
	logf("settings: %d setting(s) applied to the installed system", len(kv))
}

// carriedSettings reads the list the build put into the ramdisk.
// carriedSettings - 빌드가 램디스크에 넣어 둔 목록을 읽는다.
func carriedSettings() ([][2]string, error) {
	raw, err := os.ReadFile("/" + ramdisk.SynoinfoName)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		out = append(out, [2]string{line[:i], line[i+1:]})
	}
	return out, nil
}
