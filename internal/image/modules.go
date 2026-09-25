// modules.go obtains the driver pack that goes on partition 4.
//
// The pack comes from the latest release of catalog.ModuleRepo. The release's
// asset list is queried each time and the asset chosen by name, so a new tag
// needs no change here. The download is checked against the release's
// SHA256SUMS before it is used.
//
// modules.go - 파티션 4 에 실을 드라이버 팩을 확보한다.
//
// 팩은 catalog.ModuleRepo 의 최신 릴리스에서 받는다. 릴리스 자산 목록을 매번
// 조회해서 이름으로 고르므로, 새 태그가 나와도 여기는 고칠 게 없다. 받은 것은
// 쓰기 전에 릴리스의 SHA256SUMS 와 대조한다.

package image

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"vibeldr/internal/catalog"
	"vibeldr/internal/kmod"
	"vibeldr/internal/ramdisk"
)

// PackInfo says where a fetched pack came from, for the caller to show.
// PackInfo - 받은 팩이 어디서 왔는지. 호출자가 보여 준다.
type PackInfo struct {
	// Tag is the release the pack belongs to; Asset its file name.
	// Tag 는 팩이 속한 릴리스, Asset 은 그 파일 이름.
	Tag, Asset string
	// FromCache is set when the copy kept from an earlier download was used.
	// FromCache 는 전에 받아 둔 사본을 썼을 때 켜진다.
	FromCache bool
	// Firmware is how many firmware files came with it; FirmwareErr says why
	// none did, when the firmware pack could not be had.
	//
	// Firmware 는 같이 온 펌웨어 파일 수. 펌웨어 팩을 못 얻었으면 FirmwareErr
	// 가 그 이유다.
	Firmware    int
	FirmwareErr error
}

// FetchModulePack returns the driver pack of a model, from the latest release
// of catalog.ModuleRepo. ModulePackCPIO turns it into the raw cpio that goes
// onto partition 4; it is left as a pack here so that happens once.
//
// The release carries the drivers and their firmware as two assets
// (catalog.ModulePackName, catalog.ModuleFirmwareName); both are fetched and
// the firmware entries are added to the drivers, so partition 4 still holds a
// single archive. The drivers are required. Without the firmware the pack
// still works - the cards that need it do not come up - so its failure is
// reported in PackInfo instead.
//
// With cacheDir set, each download is kept there under its release tag and
// used again while that release is the latest. When GitHub cannot be reached,
// the newest copy kept for each asset is used.
//
// FetchModulePack - 모델의 드라이버 팩을 catalog.ModuleRepo 의 최신 릴리스에서
// 받아 돌려준다. 파티션 4 에 쓸 raw cpio 는 ModulePackCPIO 가 만든다 - 그 일이 한
// 번만 일어나도록 여기서는 팩으로 둔다.
//
// 릴리스에는 드라이버와 그 펌웨어가 자산 둘로 있다 (catalog.ModulePackName,
// catalog.ModuleFirmwareName). 둘 다 받아 펌웨어 항목을 드라이버에 더하므로,
// 파티션 4 에는 여전히 아카이브 하나만 들어간다. 드라이버는 반드시 있어야 한다.
// 펌웨어가 없어도 팩은 돈다 - 그것이 필요한 카드만 안 올라온다 - 그래서 펌웨어
// 실패는 PackInfo 로 알린다.
//
// cacheDir 가 있으면 받은 것을 릴리스 태그와 함께 거기 남기고, 그 릴리스가
// 최신인 동안 다시 쓴다. GitHub 에 닿지 못하면 자산마다 남겨 둔 것 중 가장 새것을
// 쓴다.
func FetchModulePack(ctx context.Context, cacheDir, platform, model string) (kmod.Pack, PackInfo, error) {
	return FetchModulePackFrom(ctx, http.DefaultClient, "https://api.github.com", cacheDir,
		catalog.ModuleRepo, platform, model)
}

// FetchModulePackFrom is FetchModulePack with the client, API base and
// repository given, for tests.
//
// FetchModulePackFrom - 클라이언트, API 주소, 저장소를 받는 FetchModulePack.
// 테스트용이다.
func FetchModulePackFrom(ctx context.Context, client *http.Client, api, cacheDir, repo,
	platform, model string) (kmod.Pack, PackInfo, error) {

	rel := &release{ctx: ctx, client: client, cacheDir: cacheDir, repo: repo}
	rel.load(api)
	name := catalog.ModulePackName(platform, model)
	info := PackInfo{Asset: name}

	blob, fromCache, err := rel.fetch(name)
	info.Tag, info.FromCache = rel.tag, fromCache
	if err != nil {
		return nil, info, err
	}
	pack, err := ModulePackFromCPIO(blob)
	if err != nil {
		return nil, info, fmt.Errorf("%s: %w", name, err)
	}
	fw, _, ferr := rel.fetch(catalog.ModuleFirmwareName(platform, model))
	if ferr != nil {
		info.FirmwareErr = ferr
		return pack, info, nil
	}
	n, err := addFirmware(pack, fw)
	if err != nil {
		info.FirmwareErr = err
		return pack, info, nil
	}
	info.Firmware = n
	return pack, info, nil
}

// release is the latest release of a repository, looked up once.
// release - 저장소의 최신 릴리스. 한 번만 조회한다.
type release struct {
	ctx      context.Context
	client   *http.Client
	cacheDir string
	repo     string
	tag      string
	urls     map[string]string
	sums     []byte
	err      error // why the listing or SHA256SUMS could not be had / 목록이나 SHA256SUMS 를 못 얻은 이유
}

func (r *release) load(api string) {
	body, err := downloadBytes(r.ctx, r.client, api+"/repos/"+r.repo+"/releases/latest", time.Minute)
	if err != nil {
		r.err = fmt.Errorf("%s 릴리스 조회: %w", r.repo, err)
		return
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		r.err = fmt.Errorf("%s 릴리스 파싱: %w", r.repo, err)
		return
	}
	r.tag = rel.Tag
	r.urls = map[string]string{}
	for _, a := range rel.Assets {
		r.urls[a.Name] = a.URL
	}
	if r.urls[catalog.ModuleSums] == "" {
		r.err = fmt.Errorf("%s 릴리스 %s 에 %s 없음", r.repo, rel.Tag, catalog.ModuleSums)
		return
	}
	if r.sums, err = downloadBytes(r.ctx, r.client, r.urls[catalog.ModuleSums], time.Minute); err != nil {
		r.err = fmt.Errorf("%s 받기: %w", catalog.ModuleSums, err)
	}
}

// fetch returns one asset as a raw cpio, checked against SHA256SUMS, from the
// cache when it holds this release's copy. With the release out of reach the
// newest cached copy is used.
//
// fetch - 자산 하나를 SHA256SUMS 와 대조한 raw cpio 로 돌려준다. 캐시에 이
// 릴리스의 사본이 있으면 그것을 쓴다. 릴리스에 닿지 못하면 캐시의 가장 새 사본을 쓴다.
func (r *release) fetch(name string) (blob []byte, fromCache bool, err error) {
	if r.err != nil {
		if gz, tag, ok := newestCached(r.cacheDir, name); ok {
			if r.tag == "" {
				r.tag = tag
			}
			blob, err := gunzipPack(gz)
			return blob, true, err
		}
		return nil, false, r.err
	}
	if r.urls[name] == "" {
		return nil, false, fmt.Errorf("%s 릴리스 %s 에 %s 없음", r.repo, r.tag, name)
	}
	want := sumFor(r.sums, name)
	if want == "" {
		return nil, false, fmt.Errorf("%s 에 %s 가 없음", catalog.ModuleSums, name)
	}
	cached := ""
	if r.cacheDir != "" {
		cached = filepath.Join(r.cacheDir, cacheName(r.tag, name))
		if gz, err := os.ReadFile(cached); err == nil && sha256Hex(gz) == want {
			blob, err := gunzipPack(gz)
			return blob, true, err
		}
	}
	gz, err := downloadBytes(r.ctx, r.client, r.urls[name], 30*time.Minute)
	if err != nil {
		return nil, false, fmt.Errorf("%s 받기: %w", name, err)
	}
	if got := sha256Hex(gz); got != want {
		return nil, false, fmt.Errorf("%s 의 SHA-256 이 %s 와 다름 (받은 것 %s, 기대 %s)",
			name, catalog.ModuleSums, got, want)
	}
	if cached != "" {
		_ = os.MkdirAll(r.cacheDir, 0o755)
		_ = os.WriteFile(cached, gz, 0o644)
	}
	blob, err = gunzipPack(gz)
	return blob, false, err
}

// addFirmware puts the firmware/ entries of the firmware pack into pack, and
// says how many firmware files - not counting WHENCE and the license files -
// went in. An entry pack already has stays as it is. The entries share fw's
// memory.
//
// addFirmware - 펌웨어 팩의 firmware/ 항목을 pack 에 넣고, 들어간 펌웨어 파일 수를
// 돌려준다 (WHENCE 와 라이선스 파일은 세지 않는다). pack 에 이미 있는 항목은 그대로
// 둔다. 항목은 fw 의 메모리를 같이 쓴다.
func addFirmware(pack kmod.Pack, fw []byte) (int, error) {
	a, err := ramdisk.ReadCPIOShared(fw)
	if err != nil {
		return 0, fmt.Errorf("펌웨어 팩: %w", err)
	}
	var n int
	for _, name := range a.Names() {
		e, ok := a.Get(name)
		if !ok || !e.IsRegular() || !strings.HasPrefix(name, FirmwarePrefix) {
			continue
		}
		if _, have := pack[name]; have {
			continue
		}
		pack[name] = e.Data
		rest := strings.TrimPrefix(name, FirmwarePrefix)
		if rest != "WHENCE" && !strings.HasPrefix(rest, "LICENCE") && !strings.HasPrefix(rest, "LICENSE") {
			n++
		}
	}
	return n, nil
}

// cacheName is the file a release's asset is kept under.
// cacheName - 릴리스 자산을 남겨 두는 파일 이름.
func cacheName(tag, asset string) string {
	return "modpack-" + tag + "-" + asset
}

// newestCached finds the most recently written copy of asset in cacheDir.
// newestCached - cacheDir 에서 asset 의 사본 중 가장 최근에 쓴 것을 찾는다.
func newestCached(cacheDir, asset string) (gz []byte, tag string, ok bool) {
	if cacheDir == "" {
		return nil, "", false
	}
	matches, _ := filepath.Glob(filepath.Join(cacheDir, "modpack-*-"+asset))
	var best string
	var bestTime time.Time
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.ModTime().After(bestTime) {
			best, bestTime = m, st.ModTime()
		}
	}
	if best == "" {
		return nil, "", false
	}
	gz, err := os.ReadFile(best)
	if err != nil {
		return nil, "", false
	}
	tag = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(best), "modpack-"), "-"+asset)
	return gz, tag, true
}

// sumFor finds name's hash in a sha256sum listing ("<hex>  <name>", or
// "<hex> *<name>" in binary mode).
//
// sumFor - sha256sum 목록에서 name 의 해시를 찾는다 ("<hex>  <name>", 바이너리
// 모드면 "<hex> *<name>").
func sumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// gunzipPack undoes the release's gzip and checks that what is inside is a
// newc cpio, the format partition 4 carries.
//
// gunzipPack - 릴리스의 gzip 을 풀고, 안이 파티션 4 가 싣는 newc cpio 인지
// 확인한다.
func gunzipPack(gz []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer r.Close()
	blob, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if !bytes.HasPrefix(blob, []byte("070701")) {
		return nil, errors.New("팩이 newc cpio 가 아님")
	}
	return blob, nil
}

// downloadBytes fetches url whole, with a time limit per call - a pack is
// tens of megabytes, far past what one minute allows on a slow line.
//
// downloadBytes - url 을 통째로 받는다. 시간 한도는 호출마다 준다 - 팩은 수십
// MB 라 느린 회선에서는 1 분 한도를 훌쩍 넘는다.
func downloadBytes(ctx context.Context, client *http.Client, url string, limit time.Duration) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// RamdiskModules returns the modules picked out of the pack by name, together
// with the modules they depend on.
//
// It is for building the minimum set that goes into the ramdisk. Choosing by
// name alone is not enough: a missing dependency means the kernel cannot
// resolve a symbol and the load fails.
//
// RamdiskModules - 팩에서 이름으로 고른 모듈과 그것들이 의존하는 모듈을
// 함께 돌려준다.
//
// 램디스크에 넣을 최소 집합을 만드는 용도다. 의존 모듈이 빠지면 커널이
// 심볼을 못 찾아 로드가 실패하므로, 이름만 보고 고르면 안 된다.
func RamdiskModules(p kmod.Pack, names []string) kmod.Pack {
	// Index the pack so it can be searched by module name.
	// 팩 안을 모듈 이름으로 찾을 수 있게 색인한다.
	byName := map[string]string{} // normalised name -> file name in the pack / 정규화된 이름 -> 팩 안 파일 이름
	deps := map[string][]string{}
	for file, data := range p {
		m, err := kmod.ReadBytes(file, data)
		if err != nil {
			continue
		}
		byName[m.Name] = file
		deps[m.Name] = m.Depends
	}

	out := kmod.Pack{}
	var want func(string)
	want = func(n string) {
		n = strings.ReplaceAll(strings.TrimSuffix(n, ".ko"), "-", "_")
		file, ok := byName[n]
		if !ok {
			return
		}
		if _, done := out[file]; done {
			return
		}
		out[file] = p[file]
		for _, d := range deps[n] {
			want(d)
		}
	}
	for _, n := range names {
		want(n)
	}
	return out
}

// FirmwarePrefix marks the firmware entries of a pack; the rest of the name is
// the path under /lib/firmware (cmd/vibeldr-init reads them by it).
//
// FirmwarePrefix - 팩의 펌웨어 항목 표시. 나머지 이름이 /lib/firmware 아래 경로다
// (cmd/vibeldr-init 가 이것으로 읽는다).
const FirmwarePrefix = "firmware/"

// ModulePackFromCPIO pulls the .ko files, kmod.OriginalsList when there is one,
// and the firmware entries out of the raw cpio written to partition 4. It is
// the inverse of ModulePackCPIO. Modules are keyed by base name; firmware keeps
// its whole firmware/<path> name. The entries share blob's memory rather than
// copying it - a pack is a few hundred MB, read in a ramdisk - so blob must not
// be changed afterwards.
//
// ModulePackFromCPIO - 파티션 4 에 쓰인 raw cpio 에서 .ko, 있으면
// kmod.OriginalsList, 그리고 펌웨어 항목을 꺼낸다. ModulePackCPIO 의 역방향이다.
// 모듈의 키는 경로 없는 파일 이름이고, 펌웨어는 firmware/<경로> 이름을 그대로 둔다.
// 항목은 blob 을 복사하지 않고 그 메모리를 같이 쓴다 - 팩은 수백 MB 이고 램디스크에서
// 읽는다 - 그러니 뒤에 blob 을 바꾸면 안 된다.
func ModulePackFromCPIO(blob []byte) (kmod.Pack, error) {
	a, err := ramdisk.ReadCPIOShared(blob)
	if err != nil {
		return nil, err
	}
	pack := kmod.Pack{}
	for _, name := range a.Names() {
		firmware := strings.HasPrefix(name, FirmwarePrefix)
		if !firmware && !strings.HasSuffix(name, ".ko") && path.Base(name) != kmod.OriginalsList {
			continue
		}
		e, ok := a.Get(name)
		if !ok || !e.IsRegular() {
			continue
		}
		if firmware {
			pack[name] = e.Data
			continue
		}
		pack[path.Base(name)] = e.Data
	}
	if len(pack) == 0 {
		return nil, fmt.Errorf("cpio 안에 .ko 가 없음")
	}
	return pack, nil
}

// ModulePackCPIO builds the raw cpio written straight onto partition 4.
//
// No filesystem is used, because the Synology kernel refuses every vfat mount
// during the ramdisk stage. One archive is written across the whole partition
// and unpacked in memory during boot.
//
// ModulePackCPIO - 파티션 4 에 그대로 쓸 raw cpio 를 만든다.
//
// 파일시스템을 쓰지 않는 이유는 시놀로지 커널이 램디스크 단계에서 vfat
// 마운트를 전부 거부하기 때문이다. 아카이브 하나를 파티션에 통째로 쓰고
// 부팅 중에 메모리에서 푼다.
func ModulePackCPIO(p kmod.Pack) ([]byte, error) {
	a := ramdisk.NewArchive()
	for _, name := range p.Names() {
		if err := a.Add(name, ramdisk.ModeRegular|0o644, p[name]); err != nil {
			return nil, err
		}
	}
	return a.Bytes()
}
