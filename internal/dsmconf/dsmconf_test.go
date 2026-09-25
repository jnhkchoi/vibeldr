package dsmconf

import (
	"strings"
	"testing"
)

const sample = `# Synology synoinfo.conf
unique="synology_epyc7002_sa6400"
maxdisks="12"
support_disk_compatibility="yes"

rss_server="http://update7.synology.com/autoupdate/genRSS.php"
`

func TestSetReplacesInPlace(t *testing.T) {
	out := Set(sample, "support_disk_compatibility", "no")
	if v, _ := Get(out, "support_disk_compatibility"); v != "no" {
		t.Fatalf("value is %q", v)
	}
	// Everything else has to survive, in the order it was in. A settings file
	// is something its owner reads, and a rewrite that reorders it makes every
	// later comparison useless.
	//
	// 나머지는 원래 순서 그대로 살아남아야 한다. 설정 파일은 주인이 읽는
	// 것이고, 순서를 바꾸는 재작성은 이후의 모든 비교를 쓸모없게 만든다.
	if !strings.HasPrefix(out, "# Synology synoinfo.conf\nunique=") {
		t.Errorf("the head of the file changed:\n%s", out)
	}
	if v, _ := Get(out, "maxdisks"); v != "12" {
		t.Errorf("maxdisks became %q", v)
	}
	if strings.Count(out, "support_disk_compatibility") != 1 {
		t.Error("the key was duplicated instead of replaced")
	}
	if !strings.Contains(out, "\n\nrss_server=") {
		t.Error("the blank line was lost")
	}
}

func TestSetAppendsWhenMissing(t *testing.T) {
	out := Set(sample, "support_memory_compatibility", "no")
	if v, ok := Get(out, "support_memory_compatibility"); !ok || v != "no" {
		t.Fatalf("got %q ok=%v", v, ok)
	}
	if !strings.HasSuffix(out, "support_memory_compatibility=\"no\"\n") {
		t.Errorf("not appended cleanly:\n%q", out[len(out)-80:])
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Error("a blank line was left at the end")
	}
}

func TestSetAppendsToFileWithoutTrailingNewline(t *testing.T) {
	out := Set("a=\"1\"", "b", "2")
	if out != "a=\"1\"\nb=\"2\"\n" {
		t.Errorf("got %q", out)
	}
}

func TestSetAll(t *testing.T) {
	out := SetAll(sample, [][2]string{
		{"rss_server", "http://127.0.0.1/autoupdate/genRSS.php"},
		{"maxdisks", "16"},
	})
	if v, _ := Get(out, "rss_server"); v != "http://127.0.0.1/autoupdate/genRSS.php" {
		t.Errorf("rss_server = %q", v)
	}
	if v, _ := Get(out, "maxdisks"); v != "16" {
		t.Errorf("maxdisks = %q", v)
	}
}

// Comments and anything that is not an assignment must not be mistaken for a
// key, or a commented-out line would be "found" and quietly uncommented.
//
// 주석이나 대입문이 아닌 줄을 키로 착각하면 안 된다. 그러면 주석 처리된 줄이
// "발견"되어 조용히 주석이 풀린다.
func TestSplitIgnoresNonAssignments(t *testing.T) {
	for _, l := range []string{"", "   ", "# maxdisks=\"1\"", "just words", "=nothing", "two words=\"x\""} {
		if _, _, ok := split(l); ok {
			t.Errorf("%q was read as an assignment", l)
		}
	}
	out := Set("# maxdisks=\"1\"\n", "maxdisks", "16")
	if !strings.Contains(out, "# maxdisks=\"1\"") {
		t.Error("the comment was overwritten")
	}
}

func TestGetUnquotes(t *testing.T) {
	if v, _ := Get(`k="v"`, "k"); v != "v" {
		t.Errorf("got %q", v)
	}
	if v, _ := Get(`k=v`, "k"); v != "v" {
		t.Errorf("unquoted value: got %q", v)
	}
}
