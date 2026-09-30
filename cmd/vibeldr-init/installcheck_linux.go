package main

import (
	"os"
	"time"
)

// installableCheckPass is the file the installer's scemd looks for in its
// drive-rule check for a fresh install (disk_rule_junior,
// barebone_installable_v2). With it present the check counts as passed. On the
// models with drive rules v2 (DS3622xs+ and SA6400) that check wants each
// drive listed as installable in Synology's compatibility data, which drives
// Synology never sold are not.
//
// installableCheckPass - 설치기의 scemd 가 새 설치용 드라이브 규칙 검사
// (disk_rule_junior, barebone_installable_v2) 에서 찾는 파일. 이 파일이 있으면
// 검사를 통과한 것으로 친다. 드라이브 규칙 v2 모델(DS3622xs+, SA6400)에서 그
// 검사는 드라이브마다 시놀로지 호환 데이터에 설치 가능으로 올라 있기를
// 바라는데, 시놀로지가 판 적 없는 드라이브는 거기 없다.
const installableCheckPass = "/tmp/installable_check_pass"

// keepInstallableCheckPass creates the file and checks once a second that it
// is still there, so it is back if anything removes it while the installer
// runs.
//
// keepInstallableCheckPass - 파일을 만들고 1 초마다 아직 있는지 본다. 설치기가
// 도는 동안 무엇이 지우더라도 다시 생긴다.
func keepInstallableCheckPass() {
	for {
		if _, err := os.Stat(installableCheckPass); os.IsNotExist(err) {
			if err := os.WriteFile(installableCheckPass, nil, 0o644); err != nil {
				logf("installable check: %v", err)
				return
			}
		}
		time.Sleep(time.Second)
	}
}
