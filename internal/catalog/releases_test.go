package catalog

import (
	"strings"
	"testing"
)

// releases_test.go checks that the release catalogue does not contradict
// platforms.json, that the version strings keep a consistent format, and that
// the lookup methods honour the case-insensitivity and newest-first ordering
// conventions. It never touches the network.
//
// releases_test.go - 릴리스 카탈로그가 platforms.json 과 서로
// 어긋나지 않는지, 버전 문자열 형식이 일관되는지, 조회 메서드가
// 대소문자/최신순 정렬 규약을 지키는지 확인한다.
// 네트워크는 절대 건드리지 않는다.

func TestReleasesParse(t *testing.T) {
	r, err := loadReleases()
	if err != nil {
		t.Fatalf("loadReleases: %v", err)
	}
	if len(r) == 0 {
		t.Fatal("embed 된 releases.json 이 비어 있음")
	}
}

// Every model in the catalogue has to belong to some platform in
// platforms.json. Otherwise the user gets the odd flow where the fetch succeeds
// and validate then fails.
//
// 카탈로그에 실린 모든 모델은 platforms.json 의 어떤 플랫폼에도 속해야 한다.
// 그렇지 않으면 사용자가 fetch 는 성공하고 나서 validate 에서 실패하는
// 이상한 흐름이 만들어진다.
func TestReleaseModelsExistInPlatforms(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	all, err := loadReleases()
	if err != nil {
		t.Fatal(err)
	}
	for model := range all {
		if _, ok := c.PlatformForModel(model); !ok {
			t.Errorf("release 카탈로그의 모델 %q 가 platforms.json 에 없음", model)
		}
	}
}

// Every version string in the catalogue has to satisfy the "X.Y[.Z]-N" format.
// It is the same convention as the config package's regexp, so it has to parse
// here too.
//
// 카탈로그의 모든 버전 문자열은 "X.Y[.Z]-N" 형식을 만족해야 한다.
// config 패키지의 정규식과 같은 규약이라 여기서도 파싱이 되어야 한다.
func TestReleaseVersionsParse(t *testing.T) {
	all, err := loadReleases()
	if err != nil {
		t.Fatal(err)
	}
	for model, versions := range all {
		for v, rel := range versions {
			if _, ok := parseFullVersion(v); !ok {
				t.Errorf("%s: 버전 %q 이 X.Y[.Z]-N 형식이 아님", model, v)
			}
			if rel.URL == "" {
				t.Errorf("%s / %s: url 이 비어 있음", model, v)
			}
			if rel.Kernel == "" {
				t.Errorf("%s / %s: kernel 이 비어 있음", model, v)
			}
		}
	}
}

func TestReleaseForLookup(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// The exact spelling.
	// 정확한 표기.
	r, ok := c.ReleaseFor("SA6400", "7.4.1-90080")
	if !ok {
		t.Fatal("SA6400 7.4.1-90080 을 못 찾음")
	}
	if !strings.Contains(r.URL, "SA6400") || !strings.HasSuffix(r.URL, ".pat") {
		t.Errorf("이상한 URL: %q", r.URL)
	}
	if r.Kernel != "5.10.55" {
		t.Errorf("SA6400 커널 = %q, want 5.10.55", r.Kernel)
	}

	// Lower case and surrounding spaces are tolerated.
	// 소문자 / 공백 관용.
	if _, ok := c.ReleaseFor("  sa6400  ", "7.4.1-90080"); !ok {
		t.Error("모델 이름의 대소문자·공백은 흡수되어야 함")
	}

	// A combination that does not exist.
	// 없는 조합.
	if _, ok := c.ReleaseFor("SA6400", "9.9.9-99999"); ok {
		t.Error("존재하지 않는 버전은 false 를 돌려줘야 함")
	}
	if _, ok := c.ReleaseFor("NoSuchModel", "7.4.1-90080"); ok {
		t.Error("존재하지 않는 모델은 false 를 돌려줘야 함")
	}
}

func TestKnownVersionsSortedNewestFirst(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	vers := c.KnownVersions("DS918+")
	if len(vers) < 2 {
		t.Fatalf("DS918+ 는 최소 2 개 버전을 가져야 정렬을 검증할 수 있음, got %v", vers)
	}
	// The first has to be newer than all the rest.
	// 첫 번째가 나머지 모두보다 새로워야 한다.
	first, _ := parseFullVersion(vers[0])
	for _, v := range vers[1:] {
		other, ok := parseFullVersion(v)
		if !ok {
			t.Fatalf("%q 파싱 실패", v)
		}
		if !versionGreater(first, other) {
			t.Errorf("정렬이 최신순이 아님: %v (첫번째=%q vs %q)", vers, vers[0], v)
		}
	}
}

func TestKnownModelsIncludesSeeded(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	models := c.KnownModels()
	want := map[string]bool{"SA6400": false, "DS918+": false, "DS923+": false}
	for _, m := range models {
		if _, ok := want[m]; ok {
			want[m] = true
		}
	}
	for m, seen := range want {
		if !seen {
			t.Errorf("KnownModels 에 %q 가 빠져 있음", m)
		}
	}
}

func TestParseFullVersion(t *testing.T) {
	good := map[string][]int{
		"7.4.1-90080": {7, 4, 1, 90080},
		"7.2-72806":   {7, 2, 72806},
	}
	for in, want := range good {
		got, ok := parseFullVersion(in)
		if !ok {
			t.Errorf("%q: 파싱 실패", in)
			continue
		}
		if !intsEqual(got, want) {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "7.4.1", "-90080", "7.4.1-", "7.a.b-1", "7.4.1.2-3"} {
		if _, ok := parseFullVersion(bad); ok {
			t.Errorf("%q: 파싱이 성공하면 안 됨", bad)
		}
	}
}

func versionGreater(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return len(a) > len(b)
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
