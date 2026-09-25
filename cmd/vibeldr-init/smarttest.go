package main

import (
	"os"

	"vibeldr/internal/dsmconf"
)

// On certain drives behind an HBA - an LSI or Broadcom SAS expansion card, say
// - a SMART short or long self-test returns a non-fatal error code. Data
// integrity is genuinely fine, but DSM treats that return code as fatal, fails
// to mount the storage pool, and the UI shows the volume as "degraded".
//
// DSM honours `support_ignore_smart_test="yes"` in synoinfo.conf: with that
// flag on, a failed self-test counts as a warning only and the mount carries
// on. It is written into the live copy that holds the user's data
// (/etc/synoinfo.conf). /etc.defaults is left alone, because a DSM update
// overwrites it with its own copy and the change would vanish and the problem
// return. The live copy is the same place applyInstalledSettings rewrites on
// every boot, so this follows the same lifecycle as the other settings carried
// in through synoinfo.
//
// The file is named smarttest.go because of Go's naming rule: *_test.go is
// taken as a test file and left out of the build, so the underscore is dropped.
//
// HBA (LSI/Broadcom SAS 확장 카드 등) 뒤의 특정 드라이브에서 SMART short/long
// self-test 가 non-fatal error 코드를 리턴하는 경우가 있다. 실제 데이터
// 무결성엔 문제가 없지만 DSM 은 이 리턴 코드를 fatal 로 취급해 스토리지
// 풀을 마운트하지 못하고 UI 에서 볼륨이 "저하됨" 으로 뜬다.
//
// DSM 은 synoinfo.conf 의 `support_ignore_smart_test="yes"` 를 존중해서,
// 이 플래그가 켜져 있으면 self-test 실패를 경고로만 취급하고 마운트를
// 계속 진행한다. 사용자 데이터가 있는 라이브 사본 (/etc/synoinfo.conf)
// 에 기록한다. /etc.defaults 는 DSM 업데이트가 자기 사본으로 덮어쓰므로
// 손대지 않는다 (건드리면 업데이트 후 사라져 재발한다). 라이브 사본은
// applyInstalledSettings 가 매 부팅마다 다시 적용하는 자리와 같다.
// synoinfo 로 실려온 다른 설정과 같은 라이프사이클로 관리된다.
//
// 파일 이름이 smarttest.go 인 것은 Go 의 파일명 규칙 때문이다. Go 는
// *_test.go 를 test 파일로 인식해 빌드 대상에서 빼므로 밑줄을 뺐다.

// smartTestKey is the synoinfo key DSM reads.
// smartTestKey - DSM 이 참조하는 synoinfo 키.
const smartTestKey = "support_ignore_smart_test"

// smartTestValue is the value that means on; DSM recognises "yes" alone.
// smartTestValue - 켜짐 상태의 값. DSM 은 "yes" 만 인식한다.
const smartTestValue = "yes"

// enableSmartTestIgnore adds support_ignore_smart_test="yes" to
// /etc/synoinfo.conf, skipping when it is already there. It returns 1 if the
// file was actually changed and 0 otherwise. The boot has to carry on through
// any error, so an error is logged and 0 returned.
//
// enableSmartTestIgnore - /etc/synoinfo.conf 에 support_ignore_smart_test="yes"
// 를 추가한다. 이미 있으면 건너뛴다. 반환값은 실제로 파일을 수정했으면 1,
// 그 외에는 0 이다. 오류가 나도 부팅은 계속되어야 하므로 로그만 남기고
// 0 을 돌려준다.
func enableSmartTestIgnore() int {
	const path = "/etc/synoinfo.conf"
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("smart_test: %s: %v", path, err)
		}
		return 0
	}
	if v, ok := dsmconf.Get(string(raw), smartTestKey); ok && v == smartTestValue {
		return 0 // already the wanted value / 이미 원하는 값
	}
	out := dsmconf.Set(string(raw), smartTestKey, smartTestValue)
	if out == string(raw) {
		return 0
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		logf("smart_test: %s: %v", path, err)
		return 0
	}
	return 1
}
