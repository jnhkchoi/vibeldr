package catalog

// releases.go - the per-model DSM release catalog (URL + MD5 + kernel).
//
// Typing a URL and an MD5 into loader.yaml by hand is a trap: one typo costs
// hours. With this catalog a short coordinate like "SA6400, 7.4.1-90080" is
// enough for the tool to know where to download from and what to verify
// against.
//
// The data is embedded in releases.json, and the parsed result is cached here
// behind a sync.Once so that catalog.go's *Catalog does not have to change.
//
// releases.go - 모델별 DSM 릴리스 카탈로그 (URL + MD5 + 커널).
//
// loader.yaml 에 URL / MD5 를 손으로 타이핑하는 흐름은 오타 하나로 몇 시간을
// 날릴 수 있는 지뢰다. 이 카탈로그는 "SA6400 의 7.4.1-90080" 같이 짧은
// 좌표만 있으면 어디서 받아 무엇으로 검증할지 툴이 알게 해 준다.
//
// 데이터는 releases.json 에 embed 되어 있고, catalog.go 의 *Catalog 를
// 건드리지 않으려고 파싱 결과는 이 파일 안에서 sync.Once 로 캐시한다.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed releases.json
var releasesJSON []byte

// Release is the download coordinate for one model/version pair. An empty MD5
// means verification is skipped - the entry may simply not have been filled in
// yet (see README-RELEASES.md).
//
// Release - 특정 모델/버전 조합에 대한 다운로드 좌표. MD5 가 빈 문자열이면
// 검증을 건너뛰라는 뜻이다 (아직 채워 넣지 못한 항목일 수 있음 —
// README-RELEASES.md 참조).
type Release struct {
	URL    string `json:"url"`
	MD5    string `json:"md5"`
	Kernel string `json:"kernel"`
}

// Releases maps model to version to Release, matching the JSON structure.
// Releases - model → version → Release. JSON 구조와 그대로 일치.
type Releases map[string]map[string]Release

var (
	releasesOnce sync.Once
	releasesData Releases
	releasesErr  error
)

// loadReleases parses the embedded release catalog once. Corrupt data is a
// build-time mistake, so returning the same error from wherever it is called
// is the more useful behaviour.
//
// loadReleases - embed 된 릴리스 카탈로그를 한 번만 파싱한다. 데이터 자체가
// 깨져 있으면 빌드 시점 실수라 프로세스 어디서 부르든 같은 에러를 돌려주는
// 편이 낫다.
func loadReleases() (Releases, error) {
	releasesOnce.Do(func() {
		var r Releases
		if err := json.Unmarshal(releasesJSON, &r); err != nil {
			releasesErr = fmt.Errorf("embedded releases.json: %w", err)
			return
		}
		releasesData = r
	})
	return releasesData, releasesErr
}

// ReleaseFor is the release for a model and a full version ("7.4.1-90080").
// The model name is matched case-insensitively.
//
// ReleaseFor - 주어진 모델 / 전체 버전 (예: "7.4.1-90080") 조합의 릴리스.
// 모델 이름은 대소문자를 구별하지 않는다.
func (c *Catalog) ReleaseFor(model, version string) (Release, bool) {
	all, err := loadReleases()
	if err != nil {
		return Release{}, false
	}
	key, ok := findModelKey(all, model)
	if !ok {
		return Release{}, false
	}
	r, ok := all[key][strings.TrimSpace(version)]
	return r, ok
}

// KnownVersions lists every version the catalog holds for a model, newest
// first. A model that is not in the catalog gives nil.
//
// KnownVersions - 특정 모델이 카탈로그에 가지고 있는 전체 버전 목록.
// 최신순으로 정렬해 돌려준다. 모델이 없으면 nil.
func (c *Catalog) KnownVersions(model string) []string {
	all, err := loadReleases()
	if err != nil {
		return nil
	}
	key, ok := findModelKey(all, model)
	if !ok {
		return nil
	}
	vers := make([]string, 0, len(all[key]))
	for v := range all[key] {
		vers = append(vers, v)
	}
	sortVersionsDesc(vers)
	return vers
}

// KnownModels lists the model names the release catalog knows, spelled as the
// JSON spells them, sorted.
//
// KnownModels - 릴리스 카탈로그가 알고 있는 모델 이름 (JSON 상의 표기
// 그대로). 정렬된 결과를 돌려준다.
func (c *Catalog) KnownModels() []string {
	all, err := loadReleases()
	if err != nil {
		return nil
	}
	models := make([]string, 0, len(all))
	for m := range all {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}

// findModelKey finds the JSON key, absorbing differences in case and spacing.
// findModelKey - 대소문자 / 공백 차이를 흡수해 JSON 키를 찾는다.
func findModelKey(all Releases, model string) (string, bool) {
	m := strings.TrimSpace(model)
	if _, ok := all[m]; ok {
		return m, true
	}
	for k := range all {
		if strings.EqualFold(k, m) {
			return k, true
		}
	}
	return "", false
}

// sortVersionsDesc sorts "7.4.1-90080" style strings newest first. Entries that
// do not parse fall back to string comparison and are pushed to the end without
// disturbing the order of the rest.
//
// sortVersionsDesc - "7.4.1-90080" 형식의 버전 문자열을 최신순 정렬. 파싱에
// 실패한 항목은 문자열 비교로 밀려나가지만 나머지 순서를 망치지 않는다.
func sortVersionsDesc(vers []string) {
	sort.Slice(vers, func(i, j int) bool {
		pi, oi := parseFullVersion(vers[i])
		pj, oj := parseFullVersion(vers[j])
		if oi != oj {
			return oi && !oj // the one that parsed goes first / 파싱된 쪽이 앞으로
		}
		if !oi {
			return vers[i] > vers[j]
		}
		for k := 0; k < len(pi) && k < len(pj); k++ {
			if pi[k] != pj[k] {
				return pi[k] > pj[k]
			}
		}
		return len(pi) > len(pj)
	})
}

// parseFullVersion splits "X.Y[.Z]-N" into integers. A false second result
// means the string did not have that shape. internal/config has a similar
// regexp but does not export it, so the minimum is reimplemented here.
//
// parseFullVersion - "X.Y[.Z]-N" 을 정수 슬라이스로 쪼갠다. 두 번째 반환값이
// false 이면 형식이 안 맞았다는 뜻이다. internal/config 에 비슷한 정규식이
// 있지만 export 되어 있지 않아 카탈로그 안에서 최소한만 다시 구현했다.
func parseFullVersion(s string) ([]int, bool) {
	dash := strings.LastIndexByte(s, '-')
	if dash <= 0 || dash == len(s)-1 {
		return nil, false
	}
	head, tail := s[:dash], s[dash+1:]
	parts := strings.Split(head, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, false
	}
	out := make([]int, 0, len(parts)+1)
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	build, err := strconv.Atoi(tail)
	if err != nil {
		return nil, false
	}
	out = append(out, build)
	return out, true
}

// TargetProductVersion is the DSM line the loader installs.
//
// This is the one line the loader has been verified against. Letting the user
// pick would only multiply combinations that have never been tried, so the
// install screen uses the newest release of this line and nothing else.
//
// TargetProductVersion - 설치 대상 DSM 계열.
//
// 로더가 검증된 조합은 이 계열 하나다. 사용자가 고를 수 있게 열어두면
// 조합의 수만 늘고 대부분은 시험된 적이 없으므로, 설치 화면은 이 계열의
// 최신 릴리스만 쓴다.
const TargetProductVersion = "7.4"

// TargetRelease is the release a model will be installed with: the newest one
// in the TargetProductVersion line, or the empty string if it has none.
//
// TargetRelease - 모델의 설치 대상 릴리스. TargetProductVersion 계열에서
// 가장 최신인 것을 고른다. 그 계열이 없으면 빈 문자열.
func (c *Catalog) TargetRelease(model string) string {
	for _, v := range c.KnownVersions(model) {
		if strings.HasPrefix(v, TargetProductVersion+".") || v == TargetProductVersion {
			return v
		}
	}
	return ""
}
