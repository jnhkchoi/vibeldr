package dsmconf

import (
	"fmt"
	"sort"
	"strings"
)

// LoaderPolicy is what a DSM built by this loader needs, whether or not anyone
// asked for it.
//
// This is not a matter of taste. These are the values that correct the places
// where DSM assumes it is running on hardware Synology sold, and therefore gets
// the wrong answer on anything else.
//
//   - support_disk_compatibility stays on. That looks backwards until you see
//     what turning it off does: with the lookup off, the storage service leaves
//     no verdict at all (the drive state becomes "disabled"), and the UI shows
//     a drive with no verdict as "unverified" - exactly the warning this set out
//     to remove. So the lookup stays on, and the loader's agent puts this
//     machine's drives into the list it looks up, so the answer comes back yes.
//
//   - the three rss_server entries are where DSM asks whether an update exists.
//     Pointing them at this machine leaves the question nowhere to go. An
//     automatic update is the one event that reliably breaks a loader: DSM
//     replaces the installation while the loader still carries the old kernel
//     and ramdisk, and the next boot drops into the installer. It looks like a
//     brick, and the cause was months ago.
//
// LoaderPolicy - 로더로 만든 DSM 이라면 누군가 요청했든 안 했든 필요한 것들.
//
// 이건 취향이 아니고, DSM 이 "시놀로지가 판 하드웨어에서 돈다" 를 가정하는
// 자리마다 다른 하드웨어에선 오답이 나오는 걸 바로잡는 값들이다.
//
//   - support_disk_compatibility 는 켠 상태를 유지한다. 거꾸로 보이지만, 끄면
//     저장소 서비스가 판정을 아예 남기지 않고(드라이브 상태가 "disabled"),
//     UI 는 판정 없는 드라이브를 "미검증" 으로 보여준다 - 없애려던 바로 그
//     경고다. 그래서 조회는 켜 두고, 로더 에이전트가 이 머신의 드라이브를 그
//     조회 대상 목록에 넣어 답이 "예" 로 나오게 만든다.
//
//   - 세 개의 rss_server 는 DSM 이 업데이트가 있는지 묻는 자리다. 이 머신
//     자신을 가리키게 하면 질문이 갈 데가 없다. 자동 업데이트는 로더를
//     확실하게 망가뜨리는 유일한 이벤트다. 로더가 옛 커널과 램디스크를 든 채로
//     DSM 이 설치를 갈아치우고, 다음 부팅은 설치기로 떨어진다. 벽돌처럼 보이고,
//     원인은 몇 달 전에 있다.
var LoaderPolicy = map[string]string{
	"support_disk_compatibility": "yes",
	"rss_server":                 "http://127.0.0.1/autoupdate/genRSS.php",
	"rss_server_ssl":             "https://127.0.0.1/autoupdate/genRSS.php",
	"rss_server_v2":              "http://127.0.0.1/autoupdate/v2/getList",
}

// InstalledOnly are the keys that belong to the installed system alone and are
// kept out of the ramdisk's own synoinfo.conf. The installer's get_state.cgi
// asks the rss_server entries whether a DSM version can be downloaded, and
// reports the answer as internet_ok; pointed at 127.0.0.1 there, the web
// installer offers only a manual .pat upload.
//
// InstalledOnly - 설치된 시스템에만 속해서 램디스크 자신의 synoinfo.conf 에는
// 넣지 않는 키들. 설치기의 get_state.cgi 는 rss_server 항목에 받을 수 있는 DSM
// 버전이 있는지 묻고 그 답을 internet_ok 로 알린다. 거기서 127.0.0.1 을 가리키면
// 웹 설치기는 .pat 수동 업로드만 내놓는다.
var InstalledOnly = map[string]bool{
	"rss_server":     true,
	"rss_server_ssl": true,
	"rss_server_v2":  true,
}

// Settings merges the synoinfo values the loader writes: LoaderPolicy, then
// the platform's own values, then what the user set in loader.yaml. A later
// layer wins, so the user has the last word - they had a reason, and they know
// that reason better than this does.
//
// Settings - 로더가 쓰는 synoinfo 값을 합친다. LoaderPolicy, 그다음 플랫폼
// 값, 그다음 사용자가 loader.yaml 에 적은 값 순이다. 뒤 층이 이기므로 마지막
// 말은 사용자에게 있다. 사용자가 지정한 이유가 있고, 그 이유는 당사자가 더 잘
// 안다.
func Settings(platform, user map[string]string) map[string]string {
	out := make(map[string]string, len(LoaderPolicy)+len(platform)+len(user))
	for _, layer := range []map[string]string{LoaderPolicy, platform, user} {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}

// RenderList writes settings in the form the ramdisk carries them: one
// key=value per line, keys sorted, so two builds of the same configuration
// produce the same bytes. The helper reads it back with ParseList, without a
// YAML parser.
//
// RenderList - 램디스크가 설정을 실어 나르는 형식으로 쓴다. 한 줄에
// key=value 하나, 키는 정렬해서 같은 설정으로 두 번 빌드하면 같은 바이트가
// 나온다. 헬퍼는 YAML 파서 없이 ParseList 로 다시 읽는다.
func RenderList(kv map[string]string) []byte {
	if len(kv) == 0 {
		return nil
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, kv[k])
	}
	return []byte(b.String())
}

// ParseList reads what RenderList wrote, in file order. Blank lines, lines
// starting with # and lines with no key are skipped.
//
// ParseList - RenderList 가 쓴 것을 파일 순서대로 읽는다. 빈 줄, # 로
// 시작하는 줄, 키가 없는 줄은 건너뛴다.
func ParseList(raw string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(raw, "\n") {
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
	return out
}
