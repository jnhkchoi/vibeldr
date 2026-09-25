// vibeldr is the CLI that builds a Synology DSM loader image from a single
// loader.yaml.
//
// Usage:
//
//	vibeldr <command> [flags]
//
// `vibeldr help` lists the commands.
//
// vibeldr - loader.yaml 하나로 시놀로지 DSM 로더 이미지를 만드는 CLI.
// 사용법은 위와 같고, `vibeldr help` 로 커맨드 목록을 본다.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vibeldr/internal/catalog"
	"vibeldr/internal/cmdline"
	"vibeldr/internal/config"
	"vibeldr/internal/hwscan"
	"vibeldr/internal/image"
	"vibeldr/internal/initbin"
	"vibeldr/internal/kpatch"
	"vibeldr/internal/lzma"
	"vibeldr/internal/pat"
	"vibeldr/internal/ramdisk"
	"vibeldr/internal/state"
	"vibeldr/internal/ui"
)

const defaultConfigPath = "loader.yaml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "validate":
		err = cmdValidate(args)
	case "platforms":
		err = cmdPlatforms(args)
	case "models":
		err = cmdModels(args)
	case "versions":
		err = cmdVersions(args)
	case "identity":
		err = cmdIdentity(args)
	case "cmdline":
		err = cmdCmdline(args)
	case "plan":
		err = cmdPlan(args)
	case "fetch":
		err = cmdFetch(args)
	case "extract":
		err = cmdExtract(args)
	case "patch":
		err = cmdPatch(args)
	case "image":
		err = cmdImage(args)
	case "bootstrap":
		err = cmdBootstrap(args)
	case "status":
		err = cmdStatus(args)
	case "help", "--help", "-h":
		usage()
		return
	default:
		ui.Fail("unknown command %q", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr)
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			ui.Fail("%s", ve.Error())
		} else {
			ui.Fail("%v", err)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf(`vibeldr - declarative Synology DSM loader builder

USAGE
  vibeldr <command> [flags]

SETUP
  init                 write a starter loader.yaml
  validate             check loader.yaml against the catalog

CATALOG
  platforms            list known platforms
  models [-p PLAT]     list known models
  versions <MODEL>     list DSM versions a model supports

BUILD
  plan                 show exactly what a build would do (changes nothing)
  identity             resolve and pin the serial / MAC addresses
  cmdline              render and validate the DSM kernel command line
  fetch                download the DSM .pat into the cache
  extract              unpack zImage / rd.gz from the cached .pat
  patch                put the disk-mapping helper into the DSM ramdisk
  image                write the 3 partition loader .img (no root needed)
  bootstrap            build a model/DSM-agnostic bootstrap image
                       (generic Alpine kernel + vibeldr-boot; the TUI on
                       first boot picks model/DSM and fills the rest)
  status               show how far the current build has progressed

COMMON FLAGS
  -c PATH              config file (default %s)

`, defaultConfigPath)
}

// ---------------------------------------------------------------------------
// shared helpers
// 공용 유틸
// ---------------------------------------------------------------------------

type env struct {
	cfg        *config.Config
	cat        *catalog.Catalog
	plat       *catalog.Platform
	st         *state.State
	configPath string
	configHash string
}

// load prepares everything a build command needs and fails early with one
// clear message rather than a nil dereference three calls deeper.
//
// load - 빌드 커맨드에 필요한 것을 모두 준비한다. 문제가 있으면 세 호출
// 아래에서 nil 역참조로 죽지 않고, 여기서 분명한 메시지 하나로 일찍 끝낸다.
func load(configPath string) (*env, error) {
	cat, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(configPath, cat)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found - run `vibeldr init` first", configPath)
		}
		return nil, err
	}
	plat, ok := cat.PlatformForModel(cfg.Model)
	if !ok {
		return nil, fmt.Errorf("model %q has no platform in the catalog", cfg.Model)
	}
	hash, err := state.HashConfig(configPath)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(cfg.Paths.Work)
	if err != nil {
		return nil, err
	}
	return &env{cfg: cfg, cat: cat, plat: plat, st: st, configPath: configPath, configHash: hash}, nil
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("c", defaultConfigPath, "path to loader.yaml")
}

// resolveIdentity pins this configuration's serial number and MAC addresses.
//
// An identity is generated once and kept in the state file. DSM treats a
// changed serial number or MAC as a different machine, so regenerating them on
// every build would reset the QuickConnect registration and the licence
// activations.
//
// resolveIdentity - 이 설정의 시리얼과 MAC 주소를 고정한다.
//
// identity 는 한 번만 생성해 state 에 저장한다. DSM 은 시리얼이나 MAC 이
// 바뀌면 다른 머신으로 취급하므로, 매 빌드마다 재생성하면 QuickConnect
// 등록과 라이선스 활성화가 초기화된다.
func (e *env) resolveIdentity() (cmdline.Identity, bool, error) {
	changed := false

	serial := e.cfg.Identity.Serial
	if serial == "" {
		if e.st.Serial != "" && !e.st.Stale(e.configHash) {
			serial = e.st.Serial
		} else {
			generated, err := e.cat.GenerateSerial(e.cfg.Model)
			if err != nil {
				return cmdline.Identity{}, false, fmt.Errorf("generate serial: %w", err)
			}
			serial = generated
			changed = true
		}
	}

	var macs []string
	if len(e.cfg.Identity.MACs) > 0 {
		for _, m := range e.cfg.Identity.MACs {
			norm, err := catalog.NormalizeMAC(m)
			if err != nil {
				return cmdline.Identity{}, false, err
			}
			macs = append(macs, norm)
		}
	} else if len(e.st.MACs) == e.cfg.Identity.NICCount && !e.st.Stale(e.configHash) {
		macs = e.st.MACs
	} else {
		generated, err := e.cat.GenerateMACs(e.cfg.Model, e.cfg.Identity.NICCount)
		if err != nil {
			return cmdline.Identity{}, false, fmt.Errorf("generate MACs: %w", err)
		}
		macs = generated
		changed = true
	}

	return cmdline.Identity{Serial: serial, MACs: macs}, changed, nil
}

func (e *env) buildCmdline(id cmdline.Identity) (*cmdline.Builder, error) {
	opts := cmdline.DefaultOptions()
	return cmdline.Build(e.cfg, e.plat, id, opts)
}

// resolveBootMethod, where Boot.Method is "auto" or empty, detects the
// hypervisor on this machine and settles on either kexec or direct. It returns
// the hypervisor detected, the method settled on and a message for the user,
// and it also writes that method back into e.cfg.Boot.Method so that everything
// downstream sees a concrete value.
//
// Where the user already stated "kexec" or "direct", it only detects and
// changes nothing - an explicit decision is not overturned.
//
// resolveBootMethod - Boot.Method 가 "auto" (또는 빈 값) 이면 이 머신에서
// 하이퍼바이저를 감지해서 kexec 아니면 direct 로 굳힌다. 결과 (감지된
// 하이퍼바이저, 확정된 method, 사용자에게 보여줄 메시지) 를 돌려주고,
// e.cfg.Boot.Method 도 그 값으로 바꿔서 downstream 이 concrete value 만
// 보게 한다.
//
// 이미 사용자가 "kexec" / "direct" 로 명시했으면 감지만 하고 건드리지
// 않는다. 명시적인 사용자 결정을 뒤집지 않는다.
func (e *env) resolveBootMethod() (hv hwscan.Hypervisor, method, note string) {
	hv = hwscan.DetectHypervisorSysfs()
	method = e.cfg.Boot.Method

	if method != "auto" && method != "" {
		// What the user stated explicitly is left alone; a risky combination only gets
		// a warning.
		//
		// 사용자가 명시했으면 그대로 두되, 위험한 조합이면 경고만 남긴다.
		if method == "kexec" && hv.NeedsDirectBoot() {
			note = fmt.Sprintf("boot.method=kexec on %s is known to hang; consider boot.method=auto or direct", hv)
		}
		return hv, method, note
	}

	if hv.NeedsDirectBoot() {
		method = "direct"
		note = fmt.Sprintf("hypervisor %s hangs on kexec handoff; forcing boot.method=direct", hv)
	} else {
		method = "kexec"
	}
	e.cfg.Boot.Method = method
	return hv, method, note
}

// patCacheName is the file name a .pat is cached under. Including the model and
// version keeps several builds side by side in one cache.
//
// patCacheName - .pat 을 캐시에 저장할 파일 이름. 모델과 버전을 넣어 두어
// 여러 빌드가 한 캐시에 나란히 있을 수 있다.
func (e *env) patCacheName() string {
	return fmt.Sprintf("%s-%s.pat", e.cfg.Model, e.cfg.DSM.Version)
}

func (e *env) patCachePath() string {
	return filepath.Join(e.cfg.Paths.Cache, e.patCacheName())
}

// ---------------------------------------------------------------------------
// commands
// 커맨드
// ---------------------------------------------------------------------------

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := configFlag(fs)
	force := fs.Bool("f", false, "overwrite an existing config")
	model := fs.String("model", "", "model to start from (default SA6400)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := os.Stat(*path); err == nil && !*force {
		return fmt.Errorf("%s already exists (use -f to overwrite)", *path)
	}

	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	cfg := config.Default()
	if *model != "" {
		if _, ok := cat.PlatformForModel(*model); !ok {
			return fmt.Errorf("unknown model %q (try `vibeldr models`)", *model)
		}
		cfg.Model = *model
	}

	// The newest DSM differs per model - 7.4.1 for one, still 7.2.2 for
	// another - so Default() leaves dsm.version empty and the release catalog
	// decides it here. A model with no release in the catalog is an error
	// rather than a half-written loader.yaml, so the question is answered now
	// instead of at the first build.
	//
	// 최신 DSM 은 모델마다 다르다 (어느 모델은 7.4.1, 어느 모델은 아직
	// 7.2.2). 그래서 Default() 는 dsm.version 을 비워 두고 여기서 릴리스
	// 카탈로그가 정한다. 카탈로그에 릴리스가 없는 모델은 미완성 loader.yaml
	// 을 남기지 않고 오류로 끝내, "왜 안 되지" 를 첫 빌드까지 미루지 않는다.
	if versions := cat.KnownVersions(cfg.Model); len(versions) > 0 {
		cfg.DSM.Version = versions[0]
		if rel, ok := cat.ReleaseFor(cfg.Model, versions[0]); ok {
			cfg.DSM.URL = rel.URL
			cfg.DSM.MD5 = rel.MD5
		}
	} else {
		return fmt.Errorf("모델 %q 의 릴리스가 카탈로그에 없습니다. "+
			"`vibeldr versions %s` 로 지원 버전을 확인하거나, "+
			"loader.yaml 을 손으로 열어 dsm.version 을 채워 주세요",
			cfg.Model, cfg.Model)
	}

	if err := cfg.Save(*path); err != nil {
		return err
	}
	ui.OK("wrote %s", *path)
	ui.Info("model %s, DSM %s", cfg.Model, cfg.DSM.Version)
	if cfg.DSM.URL == "" || cfg.DSM.MD5 == "" {
		ui.Warn("dsm.url / dsm.md5 가 비어있음 — 카탈로그에 아직 등록 안 됨. `vibeldr fetch --url ...` 로 직접 지정하거나 loader.yaml 을 편집하세요")
	}
	ui.Info("next: %s", ui.Cyan("vibeldr validate"))
	return nil
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	ui.Section("Configuration")
	ui.Field("File", *path)
	ui.Field("Model", e.cfg.Model)
	ui.Field("Platform", fmt.Sprintf("%s (device tree: %v)", e.plat.Name, e.plat.DT))
	ui.Field("DSM", e.cfg.DSM.Version)
	ui.Field("Kernel", e.plat.Kernels[e.cfg.ProductVersion()])
	fmt.Println()
	ui.OK("configuration is valid")

	if len(e.plat.Flags) > 0 {
		ui.Warn("platform %s requires CPU feature(s): %s - verify the target CPU has them",
			e.plat.Name, strings.Join(e.plat.Flags, ", "))
	}
	return nil
}

func cmdPlatforms(args []string) error {
	fs := flag.NewFlagSet("platforms", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	ui.Section("Platforms")
	fmt.Printf("  %-16s %-4s %-10s %-8s %s\n", "NAME", "DT", "KERNEL", "MODELS", "CPU FLAGS")
	for _, name := range cat.PlatformNames() {
		p, _ := cat.Platform(name)
		kernels := map[string]bool{}
		for _, k := range p.Kernels {
			kernels[k] = true
		}
		list := make([]string, 0, len(kernels))
		for k := range kernels {
			list = append(list, k)
		}
		sort.Strings(list)

		dt := "-"
		if p.DT {
			dt = "yes"
		}
		fmt.Printf("  %-16s %-4s %-10s %-8d %s\n",
			name, dt, strings.Join(list, ","), len(p.Models), strings.Join(p.Flags, ","))
	}
	return nil
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	platform := fs.String("p", "", "only show models of this platform")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	ui.Section("Models")
	for _, name := range cat.PlatformNames() {
		if *platform != "" && !strings.EqualFold(name, *platform) {
			continue
		}
		p, _ := cat.Platform(name)
		fmt.Printf("  %s\n", ui.Bold(name))
		for _, m := range p.Models {
			marker := " "
			if _, ok := cat.SerialRule(m); ok {
				// A model with a known serial rule can produce a serial that
				// unlocks the model-locked DSM features.
				//
				// 시리얼 규칙이 알려진 모델은 모델 잠금이 걸린 DSM 기능을 여는
				// 시리얼을 만들 수 있다.
				marker = "*"
			}
			fmt.Printf("    %s %s\n", marker, m)
		}
	}
	fmt.Println()
	ui.Info("%s = a serial number rule is known for this model", ui.Bold("*"))
	return nil
}

func cmdVersions(args []string) error {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: vibeldr versions <MODEL>")
	}
	model := fs.Arg(0)

	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	plat, ok := cat.PlatformForModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q (try `vibeldr models`)", model)
	}

	ui.Section(fmt.Sprintf("DSM versions for %s", model))
	ui.Field("Platform", plat.Name)
	fmt.Println()
	for _, v := range cat.ProductVersions(plat.Name) {
		fmt.Printf("    DSM %-6s kernel %s\n", v, plat.Kernels[v])
	}
	fmt.Println()
	ui.Info("set the exact build in loader.yaml, e.g. %s", ui.Cyan("dsm: { version: \"7.4.1-90080\" }"))
	return nil
}

func cmdIdentity(args []string) error {
	fs := flag.NewFlagSet("identity", flag.ExitOnError)
	path := configFlag(fs)
	regen := fs.Bool("regenerate", false, "discard the pinned identity and make a new one")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	if *regen {
		e.st.Serial = ""
		e.st.MACs = nil
	}

	id, changed, err := e.resolveIdentity()
	if err != nil {
		return err
	}

	ui.Section("Identity")
	ui.Field("Serial", id.Serial)
	for i, m := range id.MACs {
		ui.Field(fmt.Sprintf("MAC %d", i+1), formatMAC(m))
	}
	if _, ok := e.cat.SerialRule(e.cfg.Model); !ok {
		ui.Warn("no serial rule is known for %s, so this serial will not unlock model-locked features", e.cfg.Model)
	}

	if changed {
		e.st.Serial = id.Serial
		e.st.MACs = id.MACs
		e.st.Model = e.cfg.Model
		e.st.ConfigHash = e.configHash
		if err := e.st.Save(e.cfg.Paths.Work); err != nil {
			return err
		}
		fmt.Println()
		ui.OK("pinned in %s - rebuilds will reuse it", state.Path(e.cfg.Paths.Work))
	}
	return nil
}

func formatMAC(m string) string {
	if len(m) != 12 {
		return m
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, m[i:i+2])
	}
	return m + "  (" + strings.Join(parts, ":") + ")"
}

func cmdCmdline(args []string) error {
	fs := flag.NewFlagSet("cmdline", flag.ExitOnError)
	path := configFlag(fs)
	oneLine := fs.Bool("raw", false, "print only the command line, for piping")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}
	b, err := e.buildCmdline(id)
	if err != nil {
		return err
	}

	if *oneLine {
		fmt.Println(b.String())
		return nil
	}

	ui.Section("Kernel command line")
	for _, p := range b.Params() {
		if p.Value == "" {
			fmt.Printf("    %s\n", p.Key)
		} else {
			fmt.Printf("    %s=%s\n", p.Key, ui.Cyan(p.Value))
		}
	}
	fmt.Println()
	ui.Field("Rendered length", fmt.Sprintf("%d characters", len(b.String())))

	problems := b.Validate()
	if len(problems) == 0 {
		ui.OK("command line passes validation")
		return nil
	}
	fmt.Println()
	for _, p := range problems {
		ui.Fail("%s: %s", p.Key, p.Message)
	}
	return fmt.Errorf("%d problem(s) in the kernel command line", len(problems))
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}

	// Settle Boot.Method first. buildCmdline may depend on it, and showing an
	// already-decided method in the Target section needs it by then too.
	//
	// Boot.Method 를 먼저 확정한다. buildCmdline 이 이 값에 의존할 수도 있고,
	// Target 섹션에 이미 결정된 method 를 보여 주려면 순서상 여기가 맞다.
	hv, bootMethod, bootNote := e.resolveBootMethod()

	b, err := e.buildCmdline(id)
	if err != nil {
		return err
	}

	productVer := e.cfg.ProductVersion()

	ui.Section("Target")
	ui.Field("Model", e.cfg.Model)
	ui.Field("Platform", e.plat.Name)
	ui.Field("Device tree", fmt.Sprintf("%v", e.plat.DT))
	ui.Field("DSM", e.cfg.DSM.Version)
	ui.Field("Kernel", e.plat.Kernels[productVer])
	ui.Field("Serial", id.Serial)
	ui.Field("NICs", fmt.Sprintf("%d", len(id.MACs)))
	ui.Field("Hypervisor", string(hv))
	ui.Field("Boot method", bootMethod)
	if bootNote != "" {
		ui.Warn("%s", bootNote)
	}

	ui.Section("Steps")
	patPath := e.patCachePath()
	if _, err := os.Stat(patPath); err == nil {
		ui.OK("fetch    %s (already cached)", e.patCacheName())
	} else if e.cfg.DSM.URL == "" {
		ui.Warn("fetch    no dsm.url set - add one, or run `vibeldr fetch --url ...`")
	} else {
		ui.Info("fetch    %s", e.cfg.DSM.URL)
		ui.Info("         -> %s", patPath)
	}

	extractDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	ui.Info("extract  %s -> %s", e.patCacheName(), extractDir)
	for _, f := range pat.WantedFiles {
		ui.Info("           %s", f)
	}

	ui.Info("patch    rd.gz  -> initrd-dsm   %s", ui.Dim("(plan only)"))
	ui.Info("patch    zImage -> zImage-dsm   %s", ui.Dim("(plan only)"))
	ui.Info("image    -> %s", filepath.Join(e.cfg.Paths.Output, "loader.img"))

	ui.Section("Kernel command line")
	fmt.Printf("    %s\n", b.String())

	problems := b.Validate()
	fmt.Println()
	if len(problems) == 0 {
		ui.OK("plan is consistent")
	} else {
		for _, p := range problems {
			ui.Fail("%s: %s", p.Key, p.Message)
		}
		return fmt.Errorf("%d problem(s) found", len(problems))
	}

	if e.st.Stale(e.configHash) {
		ui.Warn("state.json was produced from a different loader.yaml; the next build will redo every step")
	}
	return nil
}

func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	path := configFlag(fs)
	url := fs.String("url", "", "override dsm.url")
	md5sum := fs.String("md5", "", "override dsm.md5")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	srcURL := e.cfg.DSM.URL
	if *url != "" {
		srcURL = *url
	}
	if srcURL == "" {
		return errors.New("no download URL: set dsm.url in loader.yaml or pass --url\n" +
			"  Synology publishes these at https://archive.synology.com/download/Os/DSM")
	}
	wantMD5 := e.cfg.DSM.MD5
	if *md5sum != "" {
		wantMD5 = *md5sum
	}

	// Ctrl+C leaves the .part file in place so the next run resumes.
	// Ctrl+C 로 끊어도 .part 파일이 남아 다음 실행이 이어서 받는다.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	dest := e.patCachePath()
	ui.Section("Fetch")
	ui.Field("URL", srcURL)
	ui.Field("Destination", dest)
	if wantMD5 == "" || wantMD5 == pat.EmptyMD5 {
		ui.Warn("no md5 configured - the download will not be content-verified")
	}
	fmt.Println()

	f := pat.NewFetcher()
	f.OnProgress = func(done, total int64) { ui.ProgressBar("downloading", done, total) }

	if err := f.Fetch(ctx, srcURL, dest, wantMD5); err != nil {
		ui.ClearLine()
		return err
	}
	ui.ClearLine()

	st, err := os.Stat(dest)
	if err != nil {
		return err
	}
	ui.OK("%s (%s)", filepath.Base(dest), ui.Bytes(st.Size()))

	e.st.Stage = state.StageFetched
	e.st.ConfigHash = e.configHash
	e.st.Model = e.cfg.Model
	e.st.Platform = e.plat.Name
	e.st.DSMVersion = e.cfg.DSM.Version
	e.st.PatPath = dest
	if wantMD5 != "" {
		e.st.PatMD5 = wantMD5
	}
	if err := e.st.Save(e.cfg.Paths.Work); err != nil {
		return err
	}

	// The cache file is kept - vibeldr-boot reuses it as it is. But if the format
	// turns out to be encrypted at this point, a later `vibeldr extract` can do
	// nothing with it, so that is said now.
	//
	// 캐시 파일은 남긴다 (vibeldr-boot 가 그대로 재사용한다). 다만 이 시점에서
	// 포맷이 암호화된 것이면 이후 `vibeldr extract` 는 아무것도 못 하니
	// 미리 알린다.
	format, ferr := pat.DetectFormat(dest)
	if ferr != nil {
		return nil
	}
	if format == pat.FormatEncrypted {
		fmt.Println()
		ui.Warn("암호화된 .pat 이 감지됨 (%s)", filepath.Base(dest))
		ui.Info("Windows 쪽 vibeldr 는 여기까지만 하고 끝낸다.")
		ui.Info("암호 해제와 zImage/rd.gz 추출은 vibeldr-boot TUI 가 VM 부팅 시점에 자동으로 처리.")
		return pat.ErrEncrypted
	}
	return nil
}

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	patPath := e.patCachePath()
	if _, err := os.Stat(patPath); err != nil {
		return fmt.Errorf("%s is not in the cache - run `vibeldr fetch` first", e.patCacheName())
	}

	destDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	ui.Section("Extract")
	ui.Field("Archive", patPath)
	ui.Field("Destination", destDir)

	format, err := pat.DetectFormat(patPath)
	if err != nil {
		return err
	}
	ui.Field("Format", format.String())
	fmt.Println()

	// An encrypted .pat is never touched from the Windows CLI. The cache is left as
	// it is, for vibeldr-boot to reuse, and this exits at once. The real decryption
	// and extraction happen inside the VM, in the vibeldr-boot TUI, with the scemd
	// pulled out of a seed DSM.
	//
	// 암호화된 .pat 은 Windows CLI 에서 절대 손대지 않는다. 캐시는 그대로
	// 두고 (vibeldr-boot 가 재사용) 즉시 종료한다. 실제 복호화·추출은 VM 안
	// vibeldr-boot TUI 가 seed DSM 에서 뽑은 scemd 로 처리한다.
	if format == pat.FormatEncrypted {
		ui.Fail("암호화된 .pat 은 Windows 쪽 vibeldr 에서 추출할 수 없다.")
		ui.Info("캐시 파일은 그대로 남겨둔다 (%s).", patPath)
		ui.Info("이 loader.yaml 로 이미지를 만들어 VM 을 부팅하면, vibeldr-boot TUI 가")
		ui.Info("seed DSM 에서 시놀로지 자체 추출기 (scemd) 를 뽑아 자동으로 처리.")
		return pat.ErrEncrypted
	}

	res, err := pat.Extract(patPath, destDir)
	if err != nil {
		return err
	}

	for _, name := range pat.WantedFiles {
		p, ok := res.Files[name]
		if !ok {
			ui.Warn("%-16s missing", name)
			continue
		}
		st, serr := os.Stat(p)
		size := "?"
		if serr == nil {
			size = ui.Bytes(st.Size())
		}
		ui.OK("%-16s %-10s %s", name, size, ui.Dim(res.Hashes[name][:16]+"..."))
	}

	// The VERSION inside the archive is the authoritative build number. Comparing
	// it against the config is what catches a URL that points at a different
	// build than the one configured - a mismatch that otherwise surfaces only
	// much later, after the wrong kernel has been patched.
	//
	// 아카이브 안의 VERSION 이 빌드 번호의 기준이다. 이것을 설정과 대조해야
	// 설정한 것과 다른 빌드를 가리키는 URL 을 잡을 수 있다. 여기서 못 잡으면
	// 엉뚱한 커널을 패치한 한참 뒤에야 드러난다.
	if vpath, ok := res.Files["VERSION"]; ok {
		info, verr := pat.ParseVersionFile(vpath)
		if verr != nil {
			ui.Warn("could not parse VERSION: %v", verr)
		} else {
			fmt.Println()
			ui.Field("Archive reports", info.Full)
			if info.ProductVer != e.cfg.ProductVersion() {
				return fmt.Errorf("archive is DSM %s but loader.yaml asks for %s - fix dsm.url or dsm.version",
					info.ProductVer, e.cfg.ProductVersion())
			}
			if info.Build != e.cfg.BuildNumber() {
				ui.Warn("build number differs: archive %s, config %s", info.Build, e.cfg.BuildNumber())
			}
			e.st.DSMVersion = info.Full
		}
	}

	e.st.Stage = state.StageExtracted
	e.st.ConfigHash = e.configHash
	e.st.ZImageSHA256 = res.Hashes["zImage"]
	e.st.RamdiskSHA256 = res.Hashes["rd.gz"]
	return e.st.Save(e.cfg.Paths.Work)
}

// patchedRamdisk and patchedZImage are both the file names the patched results
// are saved under and the names GRUB loads them by.
//
// patchedRamdisk / patchedZImage 는 패치된 결과물이 저장되는 파일명이자
// GRUB 이 로드할 때 부르는 이름이다.
const (
	patchedRamdisk = "initrd-dsm"
	patchedZImage  = "zImage-dsm"
)

// cmdPatch is the one place that touches the zImage and rd.gz Synology
// distributed.
//
// The ramdisk: one static helper binary is added and a few hook lines go into
// the boot script. The helper rewrites the PCI addresses in the model's device
// tree to this machine's real ones, loads the drivers the ramdisk did not bring
// along, and handles the pivot.
//
// The zImage: a few bytes inside vmlinux are changed - the boot parameter
// lock, so module.sig_enforce=0 on the command line takes effect, and the
// failure branch of the ramdisk signature check. Unsigned kernel modules can
// then be loaded and our driver pack works. The offsets are not hard-coded but
// found by analysing each vmlinux, so kernel 4.4 and 5.10 are handled alike.
// The original zImage is untouched; the result is saved as a new file beside
// it.
//
// cmdPatch 는 시놀로지가 배포한 zImage 와 rd.gz 에 우리 손을 대는 유일한
// 자리다.
//
// 램디스크: 정적 헬퍼 바이너리 하나가 추가되고 부팅 스크립트에 훅 몇 줄이
// 들어간다. 헬퍼는 모델의 device tree 안 PCI 주소를 이 머신 실제 주소로
// 바꾸고, 램디스크가 실어오지 않은 드라이버를 로드하고, pivot 을 처리한다.
//
// zImage: vmlinux 안의 몇 바이트만 바꾼다. 부트 파라미터 잠금을 풀어
// cmdline 의 module.sig_enforce=0 이 먹히게 하고, 램디스크 서명 검증의 실패
// 분기를 넘긴다. 서명 없는 커널 모듈을 로드할 수 있게 되어 우리 드라이버
// 팩이 통한다. 오프셋은 하드코딩이 아니라 매 vmlinux 를 분석해서 찾으므로
// kernel 4.4 / 5.10 을 가리지 않는다. 원본 zImage 는 손대지 않고 옆에 새
// 파일로 저장된다.
func cmdPatch(args []string) error {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := load(*path)
	if err != nil {
		return err
	}

	dsmDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	src := filepath.Join(dsmDir, "rd.gz")
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("%s is not there - run `vibeldr extract` first", src)
	}
	helper, err := initbin.Binary()
	if err != nil {
		return err
	}

	ui.Section("Patch")
	ui.Field("Ramdisk", src)
	ui.Field("Helper", fmt.Sprintf("/%s  %s  linux/amd64, static", ramdisk.InitName, ui.Bytes(int64(len(helper)))))
	fmt.Println()

	// Settings that belong to the installed system rather than to the ramdisk.
	// DSM lays its own copy down from the .pat, so these travel inside the
	// ramdisk and are applied on the other side of the pivot.
	//
	// 램디스크가 아니라 설치된 시스템에 속하는 설정들. DSM 은 .pat 에서 자기
	// 사본을 새로 깔기 때문에, 이 설정은 램디스크 안에 실려 가서 pivot 이후에
	// 적용된다.
	var extra []ramdisk.File
	if settings := renderSynoinfo(installedSettings(e.cfg.Synoinfo)); len(settings) > 0 {
		extra = append(extra, ramdisk.File{Name: ramdisk.SynoinfoName, Mode: 0o644, Data: settings})
	}
	// A bay order set by hand is sent along in the ramdisk. The init early in the
	// boot reads that file and uses it instead of the detection order.
	//
	// 베이 순서를 직접 정했으면 램디스크에 실어 보낸다. 부팅 초기의 init 이
	// 이 파일을 읽어 감지 순서 대신 쓴다.
	if plan := renderBayPlan(e.cfg.Storage.BayOrder); len(plan) > 0 {
		extra = append(extra, ramdisk.File{Name: "vibeldr-bay-plan", Mode: 0o644, Data: plan})
	}

	out, rep, err := ramdisk.Patch(raw, helper, extra...)
	if err != nil {
		return err
	}

	dst := filepath.Join(dsmDir, patchedRamdisk)
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return err
	}

	ui.OK("read %d files (%s compressed, %s unpacked)", rep.Entries,
		ui.Bytes(int64(len(raw))), ui.Bytes(int64(rep.Decompressed)))
	ui.OK("added /%s", ramdisk.InitName)
	if n := len(installedSettings(e.cfg.Synoinfo)); n > 0 {
		ui.OK("carrying %d setting(s) for the installed system", n)
	}
	// The same string the loader prints on every boot, so a boot log can be
	// matched to a build without anyone having to remember what was uploaded.
	//
	// 로더가 부팅할 때마다 찍는 것과 같은 문자열이다. 그래서 무엇을 올렸는지
	// 기억하지 않아도 부팅 로그와 빌드를 맞춰 볼 수 있다.
	ui.OK("build %s - the boot log will say `vibeldr: build %s`", rep.BuildID, rep.BuildID)
	for _, h := range rep.Hooks {
		ui.OK("hooked into %s at line %d", h.File, h.Line)
	}
	ui.OK("wrote %s (%s)", dst, ui.Bytes(int64(len(out))))
	fmt.Println()
	// The jump in size is intended, not a mistake. The kernel takes an
	// uncompressed cpio as an initramfs just as happily.
	//
	// 크기가 확 커지는 건 실수가 아니고 의도된 것이다. 커널은 압축 없는 cpio 도
	// 그대로 initramfs 로 받아들인다.
	ui.Info("repacked without compression - the kernel unpacks a plain cpio initramfs")

	// The zImage is patched too. A failure is logged but does not fail the whole
	// pipeline: unless our driver pack has to load, the original zImage still
	// boots.
	//
	// The identity is resolved here and handed to patchZImage, but none of the
	// sites it patches uses it: the serial and the MAC addresses reach the
	// kernel on the command line.
	//
	// zImage 도 패치한다. 실패는 로그로 남기되 파이프라인 전체를 실패시키지는
	// 않는다. 우리 드라이버 팩이 로드되어야 하는 경우가 아니라면 zImage 원본
	// 그대로도 부팅은 되기 때문이다.
	//
	// identity 를 여기서 해결해 patchZImage 에 넘기지만, 패치하는 사이트 중
	// 이 값을 쓰는 곳은 없다. 시리얼과 MAC 은 cmdline 으로 커널에 들어간다.
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}
	if err := patchZImage(dsmDir, e.cfg, e.plat, id); err != nil {
		ui.Warn("zImage patch skipped: %v", err)
		ui.Info("이 상태에선 서명 없는 커널 모듈이 EKEYREJECTED 로 거부됨")
	} else {
		ui.Info("next: %s", ui.Cyan("vibeldr image --donor <loader.img>"))
	}

	e.st.Stage = state.StagePatched
	e.st.ConfigHash = e.configHash
	return e.st.Save(e.cfg.Paths.Work)
}

// patchZImage reads the original zImage, analyses and patches the vmlinux
// inside it, and saves the new bzImage as zImage-dsm.
//
// The sites come from kpatch.Analyze alone; cfg, plat and id are accepted but
// not used, since the identity reaches the kernel on the command line.
//
// patchZImage 는 원본 zImage 를 읽어 vmlinux 를 분석/패치하고 새 bzImage 를
// zImage-dsm 으로 저장한다.
//
// 패치 사이트는 kpatch.Analyze 결과만으로 정해진다. cfg / plat / id 는 받기만
// 하고 쓰지 않는다. identity 는 cmdline 으로 커널에 들어가기 때문이다.
func patchZImage(dsmDir string, cfg *config.Config, plat *catalog.Platform, id cmdline.Identity) error {
	srcPath := filepath.Join(dsmDir, "zImage")
	dstPath := filepath.Join(dsmDir, patchedZImage)

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}
	bz, err := kpatch.ParseBzImage(raw)
	if err != nil {
		return err
	}
	vmData, err := bz.ExtractVMLinux()
	if err != nil {
		return fmt.Errorf("decompress payload: %w", err)
	}
	v, err := kpatch.ParseVMLinux(vmData)
	if err != nil {
		return fmt.Errorf("parse vmlinux: %w", err)
	}

	fmt.Println()
	ui.Section("Kernel patch")
	ui.Field("Source", srcPath)
	ui.Field("vmlinux", fmt.Sprintf("%s ELF, %d 섹션", ui.Bytes(int64(len(vmData))), 0))

	findings, err := kpatch.Analyze(v)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	if len(findings.Sites) == 0 {
		for _, m := range findings.Missing {
			ui.Warn("  못 찾음: %s", m)
		}
		return fmt.Errorf("no patch sites found")
	}
	res := kpatch.Apply(v, findings.Sites)
	for _, s := range res.Applied {
		ui.OK("  %s: 0x%02x -> 0x%02x @ file 0x%x  (%s)", s.Name, s.Old, s.New, s.FileOff, s.Why)
	}
	for _, s := range res.Skipped {
		ui.Warn("  %s: 건너뜀 - %s", s.Site.Name, s.Reason)
	}
	for _, m := range findings.Missing {
		ui.Warn("  못 찾음: %s", m)
	}
	if len(res.Applied) == 0 {
		return fmt.Errorf("찾은 사이트가 있지만 하나도 적용되지 않음")
	}

	// variant 0 is pb=2, the same as the original; variant 1 is pb=1, for a 4.4
	// kernel that overflows the slot. Rebuild takes the first result that fits.
	//
	// variant 0 은 원본과 같은 pb=2, variant 1 은 pb=1 (칸을 넘치는 4.4 커널용).
	// Rebuild 가 칸에 들어가는 첫 결과를 고른다.
	out, err := bz.Rebuild(v.Bytes, func(b []byte, variant int) ([]byte, error) {
		switch variant {
		case 0:
			return lzma.Encode(b, "9e")
		case 1:
			return lzma.Encode(b, "9-pb1")
		default:
			return nil, fmt.Errorf("시도할 압축 파라미터 없음 (variant %d)", variant)
		}
	})
	if err != nil {
		return fmt.Errorf("rebuild bzImage: %w", err)
	}
	if err := os.WriteFile(dstPath, out, 0o644); err != nil {
		return err
	}
	ui.OK("wrote %s (%s)", dstPath, ui.Bytes(int64(len(out))))
	return nil
}

// grubConfig renders the GRUB menu written onto partition 1.
//
// GRUB finds the payload by filesystem label rather than by disk number, so the
// same image boots wherever it is attached - SATA, USB or NVMe.
//
// The microcode parameter: true lists `/intel-ucode.img /amd-ucode.img` in
// front of the `initrd` line, and the kernel picks only the one matching its
// vendor. The order matters - the microcode must come before the real ramdisk.
//
// grubConfig - 파티션 1 에 쓸 GRUB 메뉴를 렌더한다.
//
// GRUB 은 디스크 번호가 아니라 파일시스템 라벨로 페이로드를 찾는다. 그래서
// 같은 이미지가 SATA, USB, NVMe 어디에 붙어도 그대로 부팅된다.
//
// microcode 파라미터: true 면 `initrd` 라인 앞에 `/intel-ucode.img
// /amd-ucode.img` 를 나열해서 커널이 벤더에 맞는 쪽만 골라 로드하게 한다.
// 순서가 중요하다 - 마이크로코드는 반드시 정규 램디스크 앞이다.
func grubConfig(cfg *config.Config, kernelCmdline string, defaultEntry string, builder bool, microcode bool) []byte {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("# Generated by vibeldr - do not edit; change loader.yaml and rebuild.")
	// Selecting a menu entry by hand needs an interactive terminal, which
	// rules out capturing the boot to a file at the same time. Naming the
	// default lets a diagnostic boot be scripted.
	//
	// 메뉴 항목을 손으로 고르려면 대화형 터미널이 있어야 하고, 그러면 부팅을
	// 동시에 파일로 받아 적을 수 없다. 기본 항목을 이름으로 정해 두면 진단용
	// 부팅을 스크립트로 돌릴 수 있다.
	if defaultEntry == "" {
		defaultEntry = "dsm"
	}
	w("set default=%s", defaultEntry)
	w("set timeout=5")
	w("")
	w("insmod part_msdos")
	w("insmod fat")
	w("insmod search_label")
	// Using the `[ ... ]` (test) command needs the test module.
	// `[ ... ]` (test) 명령을 쓰려면 test 모듈이 필요하다.
	w("insmod test")
	w("")
	w("if serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1; then")
	w("  terminal_input --append serial")
	w("  terminal_output --append serial")
	w("fi")
	w("")
	// The identity, kept where it can be changed without rebuilding anything.
	//
	// The serial and the MAC addresses are the two values most likely to need
	// changing after an image is written: a machine moved to different network
	// hardware, a second loader that must not look like the first, a MAC the
	// network was already set up around. Rebuilding the image for that is a
	// long way round, so they are GRUB variables with the built values as
	// defaults, and identity.cfg on this partition overrides them.
	//
	// That partition is FAT, which every operating system can mount, so the
	// file can be edited from the machine itself, from another computer, or
	// from the GRUB command line before the boot even starts.
	//
	// identity 는 아무것도 다시 빌드하지 않고 바꿀 수 있는 곳에 둔다.
	//
	// 이미지를 쓴 뒤 바꿀 일이 가장 많은 값이 시리얼과 MAC 주소다. 기계를 다른
	// 네트워크 장비로 옮겼거나, 두 번째 로더가 첫 번째와 같아 보이면 안 되거나,
	// 네트워크가 이미 특정 MAC 에 맞춰 설정돼 있는 경우다. 그때마다 이미지를
	// 다시 빌드하는 건 먼 길이라, 빌드한 값을 기본으로 하는 GRUB 변수로 두고
	// 이 파티션의 identity.cfg 가 덮어쓰게 한다.
	//
	// 이 파티션은 어느 운영체제든 마운트할 수 있는 FAT 이라, 그 기계에서든 다른
	// 컴퓨터에서든, 부팅이 시작되기 전 GRUB 명령줄에서든 파일을 고칠 수 있다.
	for _, kv := range identityVars(kernelCmdline) {
		w("set %s=%s", kv[0], kv[1])
	}
	w("if [ -f /%s ]; then", identityFile)
	w("  source /%s", identityFile)
	w("fi")
	w("")

	w("# The payload partition carries the patched kernel and ramdisk.")
	w("search --set=root --label %s --no-floppy", imageLabels[2])
	w("# Fall back to finding the kernel itself, so a missing or unreadable")
	w("# volume label cannot leave root pointing at the boot partition.")
	w("if [ ! -e /zImage-dsm ]; then")
	w("  search --set=root --file /zImage-dsm --no-floppy")
	w("fi")
	w("")
	kernelCmdline = withIdentityVars(kernelCmdline)

	// The microcode prefix: GRUB can join several files, as in `initrd A B C`, and
	// hand them over as one ramdisk. The kernel takes only the file whose vendor
	// string matches as early microcode and ignores the rest, so carrying both
	// vendors' images is safe. It has to come before the real ramdisk, because the
	// kernel reads the very front of the initramfs stream as microcode.
	//
	// microcode prefix: GRUB 은 `initrd A B C` 처럼 여러 파일을 이어붙여
	// 하나의 램디스크로 넘길 수 있다. 커널은 벤더 문자열이 매칭되는 파일만
	// 조기 마이크로코드로 먹고 나머지는 무시하므로, 두 벤더 이미지를 다
	// 실어도 안전하다. 반드시 정규 램디스크 앞에 와야 한다 - 커널은 initramfs
	// 스트림의 맨 앞을 마이크로코드로 해석하기 때문이다.
	dsmInitrd := "/initrd-dsm"
	if microcode {
		dsmInitrd = "/intel-ucode.img /amd-ucode.img /initrd-dsm"
	}

	w("menuentry 'DSM %s %s' --id dsm {", cfg.Model, cfg.DSM.Version)
	w("  echo 'Loading DSM kernel...'")
	w("  linux /zImage-dsm %s", kernelCmdline)
	w("  echo 'Loading DSM ramdisk...'")
	w("  initrd %s", dsmInitrd)
	// The Synology kernel writes nothing to the screen (VGA), so from the moment it
	// takes over the display looks stuck and black at 'Booting the kernel.'. These
	// lines are left on the GRUB screen right before the handover so it is not
	// mistaken for a fault; they stay visible until the kernel clears the screen.
	// The GRUB console font has no Korean glyphs and Korean comes out as ??, so
	// they are written in English.
	//
	// 시놀로지 커널은 화면(VGA)에 아무것도 못 쓴다. 그래서 커널로 넘어가는
	// 순간부터 화면이 'Booting the kernel.' 에서 검게 멈춘 것처럼 보인다.
	// 사용자가 고장으로 오해하지 않도록, 넘어가기 직전 GRUB 화면에 안내를
	// 남긴다. 이 줄들은 커널이 화면을 지우기 전까지 그대로 남아 보인다.
	// GRUB 콘솔 폰트에 한글 글리프가 없어 한글은 ?? 로 깨진다. 영어로 쓴다.
	w("  echo ''")
	w("  echo '=================================================================='")
	w("  echo ' Booting DSM now. The screen stays black from here - this is'")
	w("  echo ' normal. Synology kernels have no video console; DSM runs over'")
	w("  echo ' the network. From another PC on the same network, open:'")
	w("  echo '        http://find.synology.com'")
	w("  echo ' (or connect to this box IP on port 5000) to finish install.'")
	w("  echo '=================================================================='")
	w("}")
	w("")
	// The loader's own environment: same kernel, a ramdisk holding one
	// program. This is where the machine is configured and where a new
	// loader is built, so it comes before the reinstall entry - it is the
	// one somebody goes looking for.
	//
	// 로더 자체 환경: 같은 커널에 프로그램 하나만 든 램디스크. 기계를 설정하고
	// 새 로더를 빌드하는 곳이라 재설치 항목보다 앞에 둔다. 사람들이 찾아오는
	// 항목이 이것이다.
	if builder {
		w("menuentry 'vibeldr - configure and build' --id build {")
		w("  echo 'Loading kernel...'")
		// The same command line the DSM entry uses. It is the same kernel,
		// and a Synology kernel given none of its own parameters stops before
		// it can say why - root= and the rest are simply ignored once an
		// initramfs provides /init.
		//
		// DSM 항목과 같은 커맨드라인을 쓴다. 같은 커널이고, 시놀로지 커널은
		// 자기 파라미터를 하나도 못 받으면 이유도 말하기 전에 멈춘다. initramfs
		// 가 /init 을 주면 root= 같은 나머지는 그냥 무시된다.
		w("  linux /zImage-dsm %s", kernelCmdline)
		w("  echo 'Loading vibeldr...'")
		// The builder's ramdisk gets the microcode put in front the same way: it is the
		// same kernel, so the same CPU conditions apply.
		//
		// 빌더 램디스크에도 동일 방식으로 마이크로코드를 앞세운다. 같은 커널이라
		// CPU 조건이 똑같이 적용된다.
		if microcode {
			w("  initrd /intel-ucode.img /amd-ucode.img /%s", builderRamdisk)
		} else {
			w("  initrd /%s", builderRamdisk)
		}
		w("}")
		w("")
	}

	w("menuentry 'DSM %s %s (reinstall)' --id junior {", cfg.Model, cfg.DSM.Version)
	w("  echo 'Loading DSM kernel...'")
	w("  linux /zImage-dsm %s force_junior", kernelCmdline)
	w("  echo 'Loading DSM ramdisk...'")
	w("  initrd %s", dsmInitrd)
	w("}")
	w("")

	return []byte(b.String())
}

// The GRUB menu entry ids, used both for naming the entries and for the default
// selection.
//
// GRUB 메뉴 엔트리 id. 엔트리 이름 부여와 기본 선택 양쪽에 쓴다.
const (
	entryNormal = "dsm"
)

// imageLabels are the FAT volume labels, in partition order.
// imageLabels - FAT 볼륨 라벨, 파티션 순서대로.
var imageLabels = [3]string{"VIBELDR1", "VIBELDR2", "VIBELDR3"}

func cmdImage(args []string) error {
	fs := flag.NewFlagSet("image", flag.ExitOnError)
	path := configFlag(fs)
	out := fs.String("o", "", "output path (default <paths.output>/loader.img)")
	sizeMB := fs.Int("size", 1024, "total image size in MiB")
	p1MB := fs.Int("p1", 128, "partition 1 size in MiB (GRUB and config)")
	p2MB := fs.Int("p2", 128, "partition 2 size in MiB (untouched DSM originals)")
	donor := fs.String("donor", "", "existing loader .img to take the MBR boot code and GRUB core image from")
	// A diagnostic boot that shows what really needs patching: Synology's own
	// kernel and ramdisk, unmodified, with this loader's command line. Whatever
	// fails there is the real work list.
	//
	// 무엇을 정말 패치해야 하는지 보여 주는 진단용 부팅. 시놀로지의 커널과
	// 램디스크를 손대지 않은 채 이 로더의 커맨드라인으로 부팅한다. 거기서
	// 실패하는 것이 실제 할 일 목록이다.
	passthrough := fs.Bool("passthrough", false, "boot the unpatched DSM kernel and ramdisk (diagnostic)")
	// Which entry boots without anyone at the keyboard. A machine with a
	// serial console and no screen is the normal case here, so the default
	// has to be chosen at build time rather than picked from a menu.
	//
	// 키보드 앞에 아무도 없을 때 부팅할 항목. 화면 없이 시리얼 콘솔만 있는
	// 기계가 흔한 경우라, 메뉴에서 고르는 게 아니라 빌드할 때 기본값을 정해야
	// 한다.
	boot := fs.String("boot", entryNormal, "menu entry to boot by default: dsm | build | junior")
	// Drivers for hardware Synology never shipped one for.
	//
	// They go on the loader's own partition rather than into the ramdisk, and
	// the difference matters more than it looks. A ramdisk is unpacked into
	// memory and stays there for the life of the machine, so every driver put
	// in it is paid for whether or not the card exists; a partition is read
	// only when something asks. Several hundred drivers on disk cost nothing
	// until one of them turns out to be the one this machine needs.
	//
	// 시놀로지가 드라이버를 내놓은 적 없는 하드웨어용 드라이버.
	//
	// 램디스크가 아니라 로더 자체 파티션에 두는데, 이 차이는 보기보다 크다.
	// 램디스크는 메모리에 풀려 기계가 꺼질 때까지 남으므로, 거기 넣은
	// 드라이버는 카드가 있든 없든 전부 비용이 든다. 파티션은 누가 요청할 때만
	// 읽힌다. 디스크에 드라이버 수백 개가 있어도, 그중 하나가 이 기계에 필요한
	// 것으로 밝혀지기 전까지는 비용이 없다.
	modulesDir := fs.String("modules", "", "directory of .ko files to carry on the loader partition")
	noModules := fs.Bool("no-modules", false, "build without the driver partition")
	// -microcode: puts the intel and amd early-microcode initrds on partition 3 and
	// has GRUB list them in front of the real ramdisk. The default is
	// `boot.microcode` from loader.yaml, or false when that is absent. The config
	// is only overwritten when the flag was given explicitly on the CLI, so the
	// setting stays sticky without --microcode=... on every run.
	//
	// -microcode: intel/amd 조기 마이크로코드 initrd 를 파티션 3 에 얹고 GRUB
	// 이 정규 램디스크 앞에 나열하도록 한다. 기본값은 loader.yaml 의
	// `boot.microcode` (없으면 false). 플래그가 CLI 에서 명시적으로 주어진
	// 경우에만 config 를 덮어쓴다. 그래야 매번 --microcode=... 를 안 붙여도
	// 스티키 설정이 유지된다.
	microcodeFlag := fs.Bool("microcode", false, "carry intel/amd microcode images and prepend them to the DSM initrd (overrides boot.microcode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	microcodeExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "microcode" {
			microcodeExplicit = true
		}
	})

	e, err := load(*path)
	if err != nil {
		return err
	}
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}
	b, err := e.buildCmdline(id)
	if err != nil {
		return err
	}
	if problems := b.Validate(); len(problems) != 0 {
		for _, p := range problems {
			ui.Fail("%s: %s", p.Key, p.Message)
		}
		return fmt.Errorf("refusing to build an image from an invalid command line")
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(e.cfg.Paths.Output, "loader.img")
	}

	// Deciding the microcode option: an explicitly set CLI flag wins, otherwise
	// boot.microcode from loader.yaml does.
	//
	// 마이크로코드 옵션 결정: CLI 플래그가 명시적으로 켜졌으면 그 값이, 아니면
	// loader.yaml 의 boot.microcode 가 이긴다.
	microcode := e.cfg.Boot.Microcode
	if microcodeExplicit {
		microcode = *microcodeFlag
	}

	// The loader's own environment, if one has been built: the same kernel
	// with a ramdisk holding a single program. Booting it is how the machine
	// configures and rebuilds itself instead of being handed a finished image.
	//
	// 로더 자체 환경이 빌드돼 있으면 싣는다. 같은 커널에 프로그램 하나만 든
	// 램디스크다. 이것으로 부팅하면 완성된 이미지를 받는 대신 기계가 스스로
	// 설정하고 다시 빌드한다.
	bootRamdisk := filepath.Join(e.cfg.Paths.Work, "dsm", builderRamdisk)
	_, err = os.Stat(bootRamdisk)
	haveBuilder := err == nil

	// Partition 1: what the loader needs to boot and to remember its config.
	// 파티션 1: 로더가 부팅하고 설정을 기억하는 데 필요한 것.
	content := &image.Content{
		P1: []image.File{
			{Path: "boot/grub/grub.cfg", Data: grubConfig(e.cfg, b.String(), *boot, haveBuilder, microcode)},
			{Path: "loader.yaml", Source: e.configPath},
			{Path: "cmdline.txt", Data: []byte(b.String() + "\n")},
			// Editable without rebuilding anything: GRUB reads it before the
			// kernel starts, and this partition is FAT so any machine can
			// mount it.
			//
			// 다시 빌드하지 않고 고칠 수 있다. GRUB 이 커널 시작 전에 읽고, 이
			// 파티션은 FAT 이라 어느 기계에서든 마운트할 수 있다.
			{Path: identityFile, Data: identityTemplate(b.String())},
		},
	}

	// Partition 2: the untouched DSM originals, when they have been extracted.
	// 파티션 2: 추출돼 있으면 손대지 않은 DSM 원본.
	dsmDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	var haveDSM int
	for _, name := range pat.WantedFiles {
		src := filepath.Join(dsmDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		content.P2 = append(content.P2, image.File{Path: name, Source: src})
		haveDSM++
	}

	// Partition 3: the patched kernel and ramdisk by default. In passthrough mode
	// the originals go on unchanged, so GRUB boots exactly what Synology shipped.
	//
	// 파티션 3: 패치된 커널과 램디스크가 기본이다. passthrough 모드면 원본
	// 그대로 실어서 GRUB 이 시놀로지가 보낸 것 그대로 부팅한다.
	zImage := filepath.Join(dsmDir, patchedZImage)
	if _, err := os.Stat(zImage); err != nil {
		// With no patched zImage yet, fall back to the original. It still boots; only
		// unsigned modules cannot be loaded.
		//
		// 패치된 zImage 가 아직 없으면 원본으로 폴백한다 (부팅은 되고, 서명 없는
		// 모듈만 못 로드한다).
		zImage = filepath.Join(dsmDir, "zImage")
	}
	ramdiskSrc := filepath.Join(dsmDir, patchedRamdisk)
	needed := "`vibeldr patch`"
	if *passthrough {
		zImage = filepath.Join(dsmDir, "zImage")
		ramdiskSrc = filepath.Join(dsmDir, "rd.gz")
		needed = "`vibeldr extract`"
	}
	for _, f := range []string{zImage, ramdiskSrc} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("%s is missing; run %s first", filepath.Base(f), needed)
		}
	}
	// Whichever sources were picked above, they go on partition 3 under the
	// same two names, zImage-dsm and initrd-dsm, so the GRUB menu is the same
	// in every mode.
	//
	// 위에서 어느 원본을 골랐든 파티션 3 에는 zImage-dsm, initrd-dsm 이라는 같은
	// 두 이름으로 들어간다. 그래서 모드와 상관없이 GRUB 메뉴가 같다.
	content.P3 = append(content.P3,
		image.File{Path: "zImage-dsm", Source: zImage},
		image.File{Path: patchedRamdisk, Source: ramdiskSrc},
	)
	if haveBuilder {
		content.P3 = append(content.P3, image.File{Path: builderRamdisk, Source: bootRamdisk})
	}
	// The driver pack on partition 4. A directory given explicitly is used as it
	// is; otherwise the pack matching this model's platform and kernel is fetched.
	// A Synology kernel carries drivers only for the hardware they sell, so left
	// alone a machine with another NIC or controller sees neither network nor disk.
	//
	// 파티션 4 의 드라이버 팩. 디렉터리를 직접 주면 그걸 쓰고, 안 주면 이
	// 모델의 플랫폼/커널에 맞는 팩을 받아 온다. 시놀로지 커널은 자기네가
	// 파는 하드웨어의 드라이버만 담고 있어서, 그대로 두면 다른 NIC 이나
	// 컨트롤러를 꽂은 기계가 네트워크도 디스크도 못 잡는다.
	pack, packCount, err := modulePack(*modulesDir)
	if err != nil {
		return err
	}
	if pack == nil && !*noModules {
		name := catalog.ModulePackName(e.plat.Name, e.cfg.Model)
		ui.Info("드라이버 팩 확보 중 (%s 의 최신 릴리스, %s)...", catalog.ModuleRepo, name)
		mods, info, ferr := image.FetchModulePack(context.Background(), e.cfg.Paths.Cache, e.plat.Name, e.cfg.Model)
		if ferr != nil {
			return fmt.Errorf("드라이버 팩: %w\n  (--no-modules 로 건너뛰거나 --modules 로 직접 지정)", ferr)
		}
		if pack, err = image.ModulePackCPIO(mods); err != nil {
			return fmt.Errorf("드라이버 팩 %s: %w", name, err)
		}
		packCount = mods.Modules()
		from := info.Tag
		if info.FromCache {
			from += ", 캐시"
		}
		ui.OK("드라이버 %d 개 (%s) → %s", packCount, from, ui.Bytes(int64(len(pack))))
		if info.FirmwareErr != nil {
			ui.Warn("펌웨어 팩을 받지 못함: %v - 드라이버만 싣는다", info.FirmwareErr)
		} else {
			ui.OK("펌웨어 %d 개", info.Firmware)
		}
	}

	ui.Section("Image")
	ui.Field("Output", outPath)
	ui.Field("Size", fmt.Sprintf("%d MiB", *sizeMB))
	ui.Field("Layout", fmt.Sprintf("p1 %d MiB / p2 %d MiB / p3 rest (all FAT32)", *p1MB, *p2MB))
	if *donor != "" {
		ui.Field("Donor", *donor)
	}
	fmt.Println()

	builder := &image.Builder{
		TotalMB:       *sizeMB,
		P1MB:          *p1MB,
		P2MB:          *p2MB,
		P4MB:          packPartitionMB(len(pack)),
		P4:            pack,
		Labels:        imageLabels,
		DonorPath:     *donor,
		WithMicrocode: microcode,
	}
	res, err := builder.Build(outPath, content)
	if err != nil {
		return err
	}

	for i, p := range res.Partitions {
		// The fourth partition carries no filesystem and so has no label.
		// 네 번째 파티션은 파일시스템이 없어서 라벨도 없다.
		label := "(raw)"
		if i < len(imageLabels) {
			label = imageLabels[i]
		}
		ui.OK("p%d  %-9s %8s  LBA %d..%d", i+1, label,
			ui.Bytes(p.Bytes()), p.StartLBA, p.End()-1)
	}
	fmt.Println()
	if res.DonatedFiles > 0 {
		ui.OK("copied %d file(s) from the donor's boot partition (GRUB modules)", res.DonatedFiles)
	}
	ui.OK("wrote %s (%s)", outPath, ui.Bytes(res.SizeBytes))
	for _, w := range res.Warnings {
		ui.Warn("%s", w)
	}

	if haveDSM == 0 {
		ui.Warn("partition 2 is empty - run `vibeldr extract` to put the DSM kernel in it")
	}
	// Being explicit here matters: an image that looks complete but cannot boot
	// is worse than one that says so.
	//
	// 여기서 분명히 말하는 게 중요하다. 완성돼 보이는데 부팅이 안 되는 이미지는
	// 안 된다고 말해 주는 이미지보다 나쁘다.
	if *passthrough {
		ui.OK("passthrough: p3 carries Synology's unmodified kernel and ramdisk")
		ui.Info("this is the diagnostic boot - whatever fails is what actually needs patching")
	} else {
		if filepath.Base(zImage) == patchedZImage {
			ui.OK("p3 carries the patched kernel (%s) and the patched ramdisk", patchedZImage)
		} else {
			ui.OK("p3 carries the original kernel and the patched ramdisk")
			ui.Warn("kernel is unpatched - run `vibeldr patch` to enable signed-module bypass")
		}
		if packCount > 0 {
			ui.OK("p4 carries %d driver(s), %s, read without a filesystem", packCount, ui.Bytes(int64(len(pack))))
		}
	}
	if *donor == "" {
		ui.OK("GRUB %s, built into the image - no donor needed", image.GRUBVersion)
	}
	ui.Info("next: attach %s to a VM (any hypervisor; MBR SeaBIOS boot)", ui.Cyan(filepath.Join(e.cfg.Paths.Output, "loader.img")))
	return nil
}

// cmdBootstrap builds an "empty" bootstrap image, one where neither the model
// nor the DSM version has been decided yet.
//
// The sequence:
//  1. get an Alpine LTS kernel into the cache, downloading it only on the first
//     run.
//  2. read the Linux vibeldr-boot binary and wrap it in a cpio initrd holding
//     one init.
//  3. write a three-partition image: the GRUB configuration, the kernel and the
//     initrd on P1 (FAT); the pre-decrypted DSM kernels, per model
//     (zImage/rd.gz), on P2; and P3 reserved as an empty FAT. P4, the driver
//     pack, is not created: the hardware has not been decided, so there is
//     nothing to put on it.
//
// Attaching the resulting image to a VM has GRUB load the generic kernel, and
// vibeldr-boot brings up the TUI for the user to choose a model and a DSM.
// From there vibeldr-boot handles the download, the scemd decryption, the
// patching, the partition rewrite and the reboot.
//
// This command does not touch loader.yaml: a bootstrap image has no values to
// plant. The identity and the network preferences are either decided by
// vibeldr-boot at boot time or asked for in the TUI.
//
// cmdBootstrap - 모델/DSM 이 아직 결정되지 않은 "빈" 부트스트랩 이미지를
// 만든다. 실행 흐름은 위 영문 목록과 같다.
//
// 결과 이미지를 VM 에 붙이면 GRUB 이 제네릭 커널을 로드하고, vibeldr-boot
// 이 TUI 를 띄워 사용자가 모델/DSM 을 고른다. 그 다음은 vibeldr-boot 이
// 다운로드/scemd 복호화/patch/파티션 재작성/리부트까지 처리한다.
//
// loader.yaml 은 이 명령이 손대지 않는다. 부트스트랩 이미지에는 심을 값이
// 없다 (identity/네트워크 선호는 vibeldr-boot 이 부팅 시점에 결정하거나
// TUI 에서 물어본다).
func cmdBootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	out := fs.String("o", "", "output path (default work/out/bootstrap.img)")
	sizeMB := fs.Int("size", 1024, "total image size in MiB")
	p1MB := fs.Int("p1", 128, "partition 1 size in MiB (GRUB + kernel + initrd)")
	p2MB := fs.Int("p2", 128, "partition 2 size in MiB (reserved for DSM originals)")
	// Partition 4 is created empty. After booting, the TUI fetches the driver pack
	// for the chosen model, adds Synology's originals from the .pat and writes it
	// here. Without it created in advance there is nowhere to write, and changing
	// the partition table mid-boot is far more trouble. The default fits the
	// largest pack, SA6400's (394 MiB with the originals and firmware), and is
	// the most the helper reads from it (packMaxBytes in cmd/vibeldr-init).
	// Partition 3 is left with about 255 MiB, of which an install uses under
	// 50.
	//
	// 파티션 4 는 빈 채로 만들어 둔다. 부팅한 뒤 TUI 가 고른 모델의 드라이버 팩을
	// 받아 .pat 의 시놀 원본을 더해 여기에 쓴다. 미리 만들어 두지 않으면 쓸 자리가
	// 없고, 파티션 표를 부팅 중에 고치는 건 훨씬 성가시다. 기본값은 가장 큰 팩인
	// SA6400 의 것(원본·펌웨어 포함 394 MiB)이 들어가는 크기이고, 헬퍼가 팩에서
	// 읽는 최대치(cmd/vibeldr-init 의 packMaxBytes)와 같다. 파티션 3 에는 약
	// 255 MiB 가 남고, 설치는 그중 50 MiB 도 안 쓴다.
	p4MB := fs.Int("p4", 512, "partition 4 size in MiB (driver pack, filled after boot)")
	cacheDir := fs.String("cache", filepath.Join("work", "cache"), "kernel/apk cache directory")
	bootBin := fs.String("vibeldr-boot", "./vibeldr-boot", "path to vibeldr-boot linux/amd64 binary")
	noGzip := fs.Bool("no-gzip", false, "leave initrd as raw cpio (diagnostic; needs larger p1)")
	dsmCore := fs.String("dsm-core", filepath.Join("work", "dsmcore"), "미리 복호화한 DSM 커널(zImage/rd.gz/VERSION) 을 담은 디렉터리. P2 에 임베드해 설치 때 다운로드를 건너뛰게 한다.")
	noEmbed := fs.Bool("no-embed", false, "P2 에 DSM 커널을 임베드하지 않음 (설치 때 다운로드)")
	// A driver pack prepared in advance goes onto partition 4 as it is. Nothing has
	// to be fetched over the network during the install, which makes an offline
	// install possible.
	//
	// 미리 만들어 둔 드라이버 팩을 파티션 4 에 그대로 싣는다. 설치 때
	// 네트워크에서 받지 않아도 되므로 오프라인 설치가 가능해진다.
	driverPack := fs.String("driver-pack", "", "파티션 4 에 실을 드라이버 팩 (cpio). 비우면 설치 때 다운로드")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// If the sources are newer than the binaries, rebuild first.
	//
	// Without this check, code fixed a moment ago is burned into the image without
	// being in it. The burn looks normal and the machine boots, so a long while is
	// spent wondering why the fix has no effect.
	//
	// The ramdisk helper goes first. It is embedded into vibeldr-boot, and that
	// vibeldr-boot is what goes on the image. The other order leaves the old helper
	// in place.
	//
	// 소스가 바이너리보다 새로우면 먼저 다시 빌드한다.
	//
	// 이 확인이 없으면 방금 고친 코드가 이미지에 안 들어간 채로 구워진다.
	// 겉으로는 정상적으로 구워지고 부팅도 되기 때문에, 고친 내용이 왜
	// 반영되지 않는지 한참 헤매게 된다.
	//
	// 램디스크 헬퍼가 먼저다. 이게 vibeldr-boot 안에 embed 되고, 그
	// vibeldr-boot 이 이미지에 실린다. 순서가 뒤집히면 옛 헬퍼가 그대로 간다.
	if err := rebuildInitBinaryIfStale(); err != nil {
		return err
	}
	if err := rebuildBootBinaryIfStale(*bootBin); err != nil {
		return err
	}

	// The vibeldr-boot binary is checked first, so the run does not spend time
	// downloading a kernel only to fail at the very end.
	//
	// vibeldr-boot 바이너리를 먼저 확인해서, 커널 다운로드로 시간 쓴 뒤
	// 마지막에 실패하는 흐름을 피한다.
	bin, err := os.ReadFile(*bootBin)
	if err != nil {
		return fmt.Errorf("vibeldr-boot 바이너리를 열 수 없음 (%s): %w\n"+
			"  먼저 빌드하세요: GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o vibeldr-boot ./cmd/vibeldr-boot",
			*bootBin, err)
	}
	if len(bin) == 0 {
		return fmt.Errorf("vibeldr-boot 바이너리가 비어있음 (%s)", *bootBin)
	}

	// The only file the kernel opens on this early boot: one init. An uncompressed
	// newc cpio is taken as an initramfs by the kernel directly.
	//
	// 초기 부팅에서 커널이 열 유일한 파일: init 한 개.
	// 압축 없는 newc cpio 는 커널이 initramfs 로 그대로 받아들인다.
	a := ramdisk.NewArchive()
	if err := a.Add("init", ramdisk.ModeRegular|0o755, bin); err != nil {
		return fmt.Errorf("initrd 조립: %w", err)
	}
	initrd, err := a.Bytes()
	if err != nil {
		return fmt.Errorf("initrd 직렬화: %w", err)
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join("work", "out", "bootstrap.img")
	}

	ui.Section("Bootstrap image")
	ui.Field("Output", outPath)
	ui.Field("Size", fmt.Sprintf("%d MiB (p1 %d / p2 %d / p3 rest)", *sizeMB, *p1MB, *p2MB))
	ui.Field("vibeldr-boot", fmt.Sprintf("%s  %s", *bootBin, ui.Bytes(int64(len(bin)))))
	ui.Field("Kernel cache", *cacheDir)
	fmt.Println()

	ui.Info("Alpine LTS 커널 + virtio/scsi/nvme/net/vfat 모듈 확보 중...")
	kernelPath, mods, kver, err := image.FetchGenericKernelAndModules(*cacheDir)
	if err != nil {
		return fmt.Errorf("제네릭 커널/모듈 획득: %w", err)
	}
	kst, err := os.Stat(kernelPath)
	if err != nil {
		return err
	}
	ui.OK("kernel  %s  %s  (v%s)", filepath.Base(kernelPath), ui.Bytes(kst.Size()), kver)
	var modBytes int
	for _, m := range mods {
		modBytes += len(m.Data)
	}
	ui.OK("modules %d files  %s", len(mods), ui.Bytes(int64(modBytes)))
	// Reassembling the initrd: after init, every module is added at its real path
	// (lib/modules/VER/...).
	//
	// initrd 재조립: init 다음에 모든 모듈을 실제 경로 (lib/modules/VER/...) 로
	// 추가한다.
	a = ramdisk.NewArchive()
	if err := a.Add("init", ramdisk.ModeRegular|0o755, bin); err != nil {
		return fmt.Errorf("initrd 재조립: %w", err)
	}
	for _, m := range mods {
		if err := a.Add(m.Name, ramdisk.ModeRegular|m.Mode, m.Data); err != nil {
			return fmt.Errorf("모듈 %s add: %w", m.Name, err)
		}
	}
	// The boot environment fetches the .pat over HTTPS. With no trust store the
	// certificate check fails and nothing downloads, so a CA bundle goes along.
	//
	// 부트 환경은 .pat 을 HTTPS 로 받는다. 신뢰 저장소가 없으면 인증서
	// 검증이 실패해 다운로드가 안 되므로 CA 번들을 함께 싣는다.
	caPEM, err := image.FetchCABundle(*cacheDir)
	if err != nil {
		return fmt.Errorf("CA 번들 획득: %w", err)
	}
	if err := a.Add(catalog.AlpineCACertPath, ramdisk.ModeRegular|0o644, caPEM); err != nil {
		return fmt.Errorf("CA 번들 add: %w", err)
	}
	ui.OK("CA 번들 %s", ui.Bytes(int64(len(caPEM))))

	// From DSM 7.1 the .pat is locked and opening it needs Synology's own binary.
	// The boot environment has not even a libc, so that binary and the libraries it
	// opens at run time are extracted here and carried along.
	//
	// DSM 7.1 부터 .pat 이 잠겨 있어 여는 데 시놀로지 자체 바이너리가
	// 필요하다. 부트 환경에는 libc 조차 없으므로, 그 바이너리와 그것이
	// 실행 중에 여는 라이브러리를 여기서 뽑아 함께 싣는다.
	ui.Info("시놀로지 추출기 (scemd) 확보 중...")
	scemdFiles, err := image.FetchScemdBundle(*cacheDir)
	if err != nil {
		return fmt.Errorf("scemd 번들 획득: %w", err)
	}
	scemdNames := make([]string, 0, len(scemdFiles))
	for name := range scemdFiles {
		scemdNames = append(scemdNames, name)
	}
	sort.Strings(scemdNames)
	var scemdBytes int
	for _, name := range scemdNames {
		if err := a.Add(name, ramdisk.ModeRegular|0o755, scemdFiles[name]); err != nil {
			return fmt.Errorf("scemd 번들 %s add: %w", name, err)
		}
		scemdBytes += len(scemdFiles[name])
	}
	ui.OK("scemd  %d files  %s", len(scemdNames), ui.Bytes(int64(scemdBytes)))

	// Turning off the DSM kernel's module signature enforcement means flipping one
	// byte inside the kernel, and that byte is inside an LZMA blob, so the whole
	// thing has to be recompressed. A result larger than the original slot does not
	// go back in, and since the compressor Synology used was liblzma, the same
	// liblzma is carried along to produce the same size.
	//
	// DSM 커널의 모듈 서명 강제를 끄려면 커널 안 한 바이트를 뒤집어야 하는데,
	// 그 바이트가 LZMA 덩어리 안에 있어 통째로 다시 압축해야 한다. 결과가
	// 원본 칸보다 크면 넣을 수 없고, 시놀로지가 쓴 압축기가 liblzma 이므로
	// 같은 liblzma 를 실어 같은 크기를 낸다.
	xzFiles, err := image.FetchXZ(*cacheDir)
	if err != nil {
		return fmt.Errorf("xz 획득: %w", err)
	}
	xzNames := make([]string, 0, len(xzFiles))
	for name := range xzFiles {
		xzNames = append(xzNames, name)
	}
	sort.Strings(xzNames)
	var xzBytes int
	for _, name := range xzNames {
		if err := a.Add(name, ramdisk.ModeRegular|0o755, xzFiles[name]); err != nil {
			return fmt.Errorf("xz %s add: %w", name, err)
		}
		xzBytes += len(xzFiles[name])
	}
	ui.OK("xz     %d files  %s", len(xzNames), ui.Bytes(int64(xzBytes)))
	initrd, err = a.Bytes()
	if err != nil {
		return fmt.Errorf("initrd 재직렬화: %w", err)
	}
	// The kernel detects an initramfs cpio by its gzip, xz or lzma signature and
	// decompresses it automatically. A kernel module tree has low text density and
	// gzip gets it to 60-65%, which keeps p1 at its original 128MB. With --no-gzip
	// it stays a raw cpio, for debugging the kernel's parser.
	//
	// 커널은 initramfs cpio 를 gzip/xz/lzma 시그니처로 감지해 자동으로 압축을
	// 푼다. 커널 모듈 트리는 텍스트 밀도가 낮아 gzip 으로 60~65% 로 줄어든다.
	// p1 을 원래대로 128MB 로 유지할 수 있다. --no-gzip 이면 raw cpio 로 둔다
	// (커널 파서 문제 디버깅용).
	if !*noGzip {
		var buf bytes.Buffer
		zw, gzErr := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if gzErr != nil {
			return fmt.Errorf("initrd gzip writer: %w", gzErr)
		}
		if _, wErr := zw.Write(initrd); wErr != nil {
			return fmt.Errorf("initrd gzip write: %w", wErr)
		}
		if cErr := zw.Close(); cErr != nil {
			return fmt.Errorf("initrd gzip close: %w", cErr)
		}
		ui.OK("initrd  %s → %s (gzip)", ui.Bytes(int64(len(initrd))), ui.Bytes(int64(buf.Len())))
		initrd = buf.Bytes()
	} else {
		ui.OK("initrd  %s (raw cpio, no compression)", ui.Bytes(int64(len(initrd))))
	}

	content := &image.Content{
		P1: []image.File{
			{Path: "boot/grub/grub.cfg", Data: image.BootstrapGrubConfig()},
			{Path: image.BootstrapKernelName, Source: kernelPath},
			{Path: image.BootstrapInitrdName, Data: initrd},
		},
	}

	// Embed the pre-decrypted DSM kernel on P2. With it there, vibeldr-boot skips
	// both the .pat download and the scemd decryption entirely at install time.
	//
	// P2 에 미리 복호화해 둔 DSM 커널을 임베드한다. 설치 때 이게 있으면
	// vibeldr-boot 은 .pat 다운로드와 scemd 복호화를 통째로 건너뛴다.
	embedded := 0
	if !*noEmbed {
		embedded = embedDSMCore(*dsmCore, &content.P2)
		if embedded > 0 {
			ui.OK("P2 임베드: DSM 커널 %d 모델 (설치 때 다운로드 생략)", embedded)
		} else {
			ui.Warn("P2 임베드 없음 (%s 비어있음) - 설치 때 다운로드함", *dsmCore)
		}
	}

	var p4 []byte
	if *driverPack != "" {
		b, err := os.ReadFile(*driverPack)
		if err != nil {
			return fmt.Errorf("드라이버 팩을 읽지 못함: %w", err)
		}
		if need := packPartitionMB(len(b)); need > *p4MB {
			return fmt.Errorf("드라이버 팩이 %d MiB 인데 파티션 4 가 %d MiB - --p4 를 %d 이상으로",
				need, *p4MB, need)
		}
		p4 = b
		ui.OK("P4 에 드라이버 팩 %s 를 싣는다 (%s)", filepath.Base(*driverPack), ui.Bytes(int64(len(b))))
	}

	builder := &image.Builder{
		TotalMB:   *sizeMB,
		P1MB:      *p1MB,
		P2MB:      *p2MB,
		P4MB:      *p4MB,
		P4:        p4,
		Labels:    imageLabels,
		Bootstrap: true,
	}
	res, err := builder.Build(outPath, content)
	if err != nil {
		return err
	}

	for i, p := range res.Partitions {
		label := "(raw)"
		if i < len(imageLabels) {
			label = imageLabels[i]
		}
		ui.OK("p%d  %-9s %8s  LBA %d..%d", i+1, label,
			ui.Bytes(p.Bytes()), p.StartLBA, p.End()-1)
	}
	fmt.Println()
	ui.OK("wrote %s (%s)", outPath, ui.Bytes(res.SizeBytes))
	for _, w := range res.Warnings {
		ui.Warn("%s", w)
	}
	ui.OK("p1 carries: boot/grub/grub.cfg, %s, %s",
		image.BootstrapKernelName, image.BootstrapInitrdName)
	if embedded > 0 {
		ui.Info("p2 는 미리 복호화한 DSM 커널 %d 모델, p3 은 빈 FAT: 부팅 후 vibeldr-boot 이 패치본으로 채움", embedded)
	} else {
		ui.Info("p2, p3 은 빈 FAT: 부팅 후 vibeldr-boot 이 DSM 원본/패치본으로 채움")
	}
	ui.Info("다음: VM 에 %s 를 붙이고 시리얼 콘솔로 TUI 진입", ui.Cyan(outPath))
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	ui.Section("Status")
	ui.Field("Config", e.configPath)
	ui.Field("Work directory", e.cfg.Paths.Work)
	ui.Field("Stage", string(e.st.Stage))
	if e.st.Serial != "" {
		ui.Field("Pinned serial", e.st.Serial)
	}
	if !e.st.UpdatedAt.IsZero() {
		ui.Field("Last updated", e.st.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	}

	fmt.Println()
	for _, s := range state.Order[1:] {
		mark := ui.Dim("- ")
		if e.st.Stage.AtLeast(s) {
			mark = ui.Green("v ")
		}
		fmt.Printf("  %s%s\n", mark, s)
	}

	if e.st.Stale(e.configHash) {
		fmt.Println()
		ui.Warn("loader.yaml changed since this state was written")
		ui.Info("the next build will start over from fetch")
	}
	return nil
}

// renderSynoinfo writes the settings out in a form the helper can read back
// without a YAML parser, sorted so that two builds of the same configuration
// produce the same bytes.
//
// renderSynoinfo - 헬퍼가 YAML 파서 없이 다시 읽을 수 있는 형식으로 설정을
// 쓴다. 키를 정렬해서 같은 설정으로 두 번 빌드하면 같은 바이트가 나온다.
func renderSynoinfo(kv map[string]string) []byte {
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

// loaderPolicy is what a DSM built by this loader needs, whether or not anyone
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
// An entry of the same name in loader.yaml wins. The user had a reason, and
// they know that reason better than this does.
//
// loaderPolicy - 로더로 만든 DSM 이라면 누군가 요청했든 안 했든 필요한 것들.
//
// 이건 취향이 아니고, DSM 이 "시놀로지가 판 하드웨어에서 돈다" 를 가정하는
// 자리마다 다른 하드웨어에선 오답이 나오는 걸 바로잡는 값들이다. 두 항목의
// 자세한 사정은 위 영문 목록과 같다.
//
//   - support_disk_compatibility 는 켠 상태를 유지한다. 끄면 오히려 "미검증"
//     경고가 그대로 남는다. 조회는 켜 두고, 로더 에이전트가 이 머신의
//     드라이브를 그 조회 대상 목록에 넣어 답이 "예" 로 나오게 만든다.
//
//   - 세 개의 rss_server 는 DSM 이 업데이트가 있는지 묻는 자리다. 이 머신
//     자신을 가리키게 하면 질문이 갈 데가 없다. 자동 업데이트는 로더를
//     확실하게 망가뜨리는 유일한 이벤트다.
//
// loader.yaml 에 같은 이름 엔트리가 있으면 그쪽이 우선이다. 사용자가 지정한
// 이유가 있고, 그 이유는 당사자가 더 잘 안다.
var loaderPolicy = map[string]string{
	"support_disk_compatibility": "yes",
	"rss_server":                 "http://127.0.0.1/autoupdate/genRSS.php",
	"rss_server_ssl":             "https://127.0.0.1/autoupdate/genRSS.php",
	"rss_server_v2":              "http://127.0.0.1/autoupdate/v2/getList",
}

// installedSettings merges what the loader insists on with what the user asked
// for, the user winning.
//
// installedSettings - 로더가 꼭 넣어야 하는 값과 사용자가 요청한 값을
// 합친다. 겹치면 사용자 쪽이 이긴다.
func installedSettings(user map[string]string) map[string]string {
	out := make(map[string]string, len(loaderPolicy)+len(user))
	for k, v := range loaderPolicy {
		out[k] = v
	}
	for k, v := range user {
		out[k] = v
	}
	return out
}

// identityFile is the small editable file on the boot partition that can
// override the identity the image was built with.
//
// identityFile - 부트 파티션에 있는 작은 편집용 파일. 이미지를 빌드할 때 넣은
// identity 를 덮어쓸 수 있다.
const identityFile = "identity.cfg"

// identityKeys are the command-line parameters worth being able to change
// without a rebuild: everything that says which machine this is, rather than
// what it is made of.
//
// syno_hw_version is here with a limit worth knowing. Changing it picks a
// different model, and that only works between models built from the same .pat
// - the kernel and the ramdisk in this image belong to one of them. Across
// platforms it produces a machine that does not boot, and the fix is to build
// an image for that model instead.
//
// vid and pid are the USB identifiers DSM checks to decide whether it is
// looking at its own boot device. They matter when the loader is a real USB
// stick and DSM should recognise that stick in particular.
//
// identityKeys - 다시 빌드하지 않고 바꿀 수 있으면 좋은 커맨드라인
// 파라미터들. 기계가 무엇으로 만들어졌는지가 아니라 어느 기계인지를 말하는
// 값 전부다.
//
// syno_hw_version 에는 알아 둘 한계가 있다. 바꾸면 다른 모델이 되는데, 같은
// .pat 으로 만드는 모델끼리만 통한다. 이 이미지의 커널과 램디스크는 그중 한
// 모델의 것이기 때문이다. 플랫폼을 넘어가면 부팅이 안 되는 기계가 되고, 그땐
// 그 모델용 이미지를 따로 빌드해야 한다.
//
// vid 와 pid 는 DSM 이 자기 부트 장치인지 판단할 때 보는 USB 식별자다. 로더가
// 실제 USB 스틱이고 DSM 이 바로 그 스틱을 알아봐야 할 때 의미가 있다.
var identityKeys = []string{
	"syno_hw_version",
	"sn",
	"netif_num",
	"mac1", "mac2", "mac3", "mac4",
	"vid", "pid",
}

// identityVars pulls the built-in values out of the rendered command line, so
// that grub.cfg can declare them as defaults.
//
// identityVars - 렌더된 커맨드라인에서 빌드 때 넣은 값을 뽑아낸다. grub.cfg 가
// 이 값들을 기본값으로 선언할 수 있게 하기 위해서다.
func identityVars(cmdline string) [][2]string {
	var out [][2]string
	for _, k := range identityKeys {
		if v, ok := cmdlineValue(cmdline, k); ok {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// withIdentityVars replaces those values with references to the variables, so
// that whatever identity.cfg last set is what the kernel is told.
//
// withIdentityVars - 그 값들을 변수 참조로 바꾼다. 그래서 identity.cfg 가
// 마지막으로 정한 값이 커널에 전달된다.
func withIdentityVars(cmdline string) string {
	for _, k := range identityKeys {
		if v, ok := cmdlineValue(cmdline, k); ok {
			cmdline = strings.Replace(cmdline, k+"="+v, k+"=$"+k, 1)
		}
	}
	return cmdline
}

func cmdlineValue(cmdline, key string) (string, bool) {
	for _, f := range strings.Fields(cmdline) {
		if name, value, ok := strings.Cut(f, "="); ok && name == key {
			return value, true
		}
	}
	return "", false
}

// identityTemplate is written next to grub.cfg: the values the image was built
// with, commented out, so that changing one is a matter of deleting a #.
//
// identityTemplate - grub.cfg 옆에 쓰는 파일 내용. 이미지를 빌드할 때 쓴 값을
// 주석 처리해 담아 두어, 하나를 바꾸려면 # 만 지우면 된다.
func identityTemplate(cmdline string) []byte {
	var b strings.Builder
	b.WriteString(`# vibeldr identity - read by GRUB before the kernel starts.
#
# Uncomment a line and change it to override what this image was built with.
# Nothing here is checked or rebuilt: whatever is set is what the kernel is
# told, so a MAC belongs here in the same 12 hex digit form as the built value.
#
# DSM treats the serial and the MAC as the machine's identity. Changing either
# after DSM is installed makes it a different machine as far as Synology's own
# services are concerned.
#
# syno_hw_version only moves between models built from the same .pat - the
# kernel and ramdisk in this image belong to one of them. For another platform,
# build an image for that model instead.

`)
	for _, kv := range identityVars(cmdline) {
		fmt.Fprintf(&b, "# set %s=%s\n", kv[0], kv[1])
	}
	return []byte(b.String())
}

// builderRamdisk is the loader's own environment, one file holding one program.
//
// builderRamdisk - 로더 자체 환경. 프로그램 하나를 담은 파일 하나다.
const builderRamdisk = "initrd-vibeldr"

// modulePack turns a driver directory into one archive for partition 4.
//
// It is a cpio rather than a directory on a filesystem because this partition
// has no filesystem. The helper reads it whole off the block device and unpacks
// it in memory. It is the format the kernel itself uses for a ramdisk, and the
// loader already knows how to read and write it.
//
// modulePack - 드라이버 디렉터리를 파티션 4 용 아카이브 하나로 바꾼다.
//
// 파일시스템 위의 디렉터리가 아니라 cpio 인 이유는 이 파티션에 파일시스템이
// 없기 때문이다. 헬퍼가 블록 장치에서 통째로 읽어와 메모리에서 푼다. 커널
// 자체가 ramdisk 로 쓰는 그 포맷이고, 로더가 이미 읽고 쓸 줄 안다.
func modulePack(dir string) ([]byte, int, error) {
	if dir == "" {
		return nil, 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	a := ramdisk.NewArchive()
	var n int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ko") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, 0, err
		}
		if err := a.Add(e.Name(), ramdisk.ModeRegular|0o644, data); err != nil {
			return nil, 0, err
		}
		n++
	}
	if n == 0 {
		return nil, 0, fmt.Errorf("%s has no .ko files in it", dir)
	}
	out, err := a.Bytes()
	if err != nil {
		return nil, 0, err
	}
	return out, n, nil
}

// packPartitionMB is how much room to give the pack: its size rounded up, plus
// room for Synology's originals, which the install adds from the .pat (14 MiB
// at most on the three models), and for a slightly larger rebuild.
//
// packPartitionMB - 팩에 줄 자리. 크기를 올림하고, 설치가 .pat 에서 더하는
// 시놀 원본(세 모델에서 최대 14 MiB)과 조금 커진 재빌드가 들어갈 여유를 더한다.
func packPartitionMB(size int) int {
	if size == 0 {
		return 0
	}
	const mib = 1 << 20
	return (size+mib-1)/mib + 32
}

// rebuildBootBinaryIfStale rebuilds the vibeldr-boot binary when it is older
// than its sources.
//
// The image builder only reads a prebuilt binary and puts it in. So fixing the
// source and reburning only the image puts the old binary in unchanged. The
// difference shows up only inside the image, which takes a long time to notice.
//
// Where there is no Go toolchain there is no way to rebuild, so it warns and
// carries on with the binary it has.
//
// rebuildBootBinaryIfStale - vibeldr-boot 바이너리가 소스보다 오래됐으면
// 다시 빌드한다.
//
// 이미지 빌더는 미리 만들어 둔 바이너리를 읽어 넣기만 한다. 그래서 소스를
// 고치고 이미지만 다시 구우면 옛 바이너리가 그대로 들어간다. 이미지 안에서만
// 드러나는 차이라서 알아채기까지 오래 걸린다.
//
// Go 툴체인이 없는 환경에서는 다시 빌드할 방법이 없으므로, 경고만 하고 있는
// 바이너리로 진행한다.
func rebuildBootBinaryIfStale(binPath string) error {
	newest, err := newestSourceTime("cmd/vibeldr-boot", "internal")
	if err != nil {
		// With no source tree there is nothing to check.
		// 소스 트리가 없으면 확인할 것도 없다.
		return nil
	}
	// The ramdisk helper is not a .go file but an embedded binary. If it was just
	// reburned and vibeldr-boot is not rebuilt, the old helper goes onto the image
	// unchanged.
	//
	// 램디스크 헬퍼는 .go 가 아니라 embed 된 바이너리다. 이게 새로 구워졌는데
	// vibeldr-boot 을 안 다시 빌드하면 옛 헬퍼가 그대로 이미지에 실린다.
	if st, err := os.Stat(initBinPath); err == nil && st.ModTime().After(newest) {
		newest = st.ModTime()
	}
	st, statErr := os.Stat(binPath)
	if statErr == nil && st.ModTime().After(newest) {
		return nil
	}

	goBin, lookErr := exec.LookPath("go")
	if lookErr != nil {
		if statErr == nil {
			ui.Warn("vibeldr-boot 이 소스보다 오래됐지만 go 를 찾지 못해 그대로 씁니다 (%s)", binPath)
			return nil
		}
		return fmt.Errorf("vibeldr-boot 바이너리가 없고 go 도 찾지 못함: %w", lookErr)
	}

	ui.Info("vibeldr-boot 을 다시 빌드합니다 (소스가 더 최신)")
	cmd := exec.Command(goBin, "build", "-o", binPath, "./cmd/vibeldr-boot")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vibeldr-boot 빌드 실패: %w\n%s", err, out)
	}
	return nil
}

// initBinPath is where the ramdisk helper is embedded. internal/initbin reads
// this path with //go:embed.
//
// initBinPath - 램디스크 헬퍼가 embed 되는 자리. internal/initbin 이 여기를
// //go:embed 로 읽는다.
const initBinPath = "internal/initbin/vibeldr-init.bin"

// rebuildInitBinaryIfStale reburns the ramdisk helper when it is older than its
// sources.
//
// Why it is needed:
//
//	cmd/vibeldr-init is a separate program and building this tool does not
//	build it. A prebuilt binary is simply embedded. So fixing the helper's
//	code and reburning only the image leaves the fix out entirely, and the
//	burn still succeeds and the machine still boots. All that shows on the
//	outside is "fixed it and it does not take", which sends the search off in
//	the wrong direction.
//
// Unlike vibeldr-boot, this does not stop at a warning. A missing helper leaves
// an image that looks perfectly fine and behaves differently, which is not
// something to pass over quietly.
//
// rebuildInitBinaryIfStale - 램디스크 헬퍼가 소스보다 오래됐으면 다시 굽는다.
//
// 왜 필요한가는 위 영문과 같다. cmd/vibeldr-init 은 별개 프로그램이라 이
// 툴을 빌드해도 같이 빌드되지 않고, 미리 구워 둔 바이너리가 embed 될 뿐이다.
// 그래서 헬퍼 코드를 고치고 이미지만 다시 구우면 고친 내용이 통째로 빠진 채
// 정상적으로 구워지고 부팅까지 된다.
//
// vibeldr-boot 과 달리 경고로 끝내지 않는다. 헬퍼가 빠지면 이미지는 멀쩡해
// 보이는데 동작만 다르기 때문에, 조용히 넘어가면 안 된다.
func rebuildInitBinaryIfStale() error {
	newest, err := newestSourceTime("cmd/vibeldr-init", "internal")
	if err != nil {
		// With no source tree - only the distributed tool - there is nothing to check.
		// 소스 트리가 없으면 (배포된 툴만 있는 경우) 확인할 것도 없다.
		return nil
	}
	st, statErr := os.Stat(initBinPath)
	if statErr == nil && st.ModTime().After(newest) {
		return nil
	}

	goBin, lookErr := exec.LookPath("go")
	if lookErr != nil {
		if statErr == nil {
			ui.Warn("램디스크 헬퍼가 소스보다 오래됐지만 go 를 찾지 못해 그대로 씁니다 (%s)", initBinPath)
			return nil
		}
		return fmt.Errorf("램디스크 헬퍼가 없고 go 도 찾지 못함: %w", lookErr)
	}

	ui.Info("램디스크 헬퍼(vibeldr-init)를 다시 빌드합니다 (소스가 더 최신)")
	cmd := exec.Command(goBin, "build", "-o", initBinPath, "./cmd/vibeldr-init")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vibeldr-init 빌드 실패: %w: %s", err, out)
	}
	return nil
}

// newestSourceTime is the most recent modification time among the .go files
// under the given directories.
//
// newestSourceTime - 주어진 디렉터리들 아래 .go 파일 중 가장 최근 수정 시각.
func newestSourceTime(dirs ...string) (time.Time, error) {
	var newest time.Time
	seen := false
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			seen = true
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			return nil
		})
		if err != nil {
			return newest, err
		}
	}
	if !seen {
		return newest, fmt.Errorf("소스 파일을 찾지 못함")
	}
	return newest, nil
}

// renderBayPlan turns the settings' bay order into the file planted in the
// ramdisk.
//
// One "<PCIe path> <port number>" per line, and the order they are written in
// is bay 1, 2, 3 and so on. An empty list comes back empty, and then the order
// detected at boot is used as it is.
//
// renderBayPlan - 설정의 베이 순서를 램디스크에 심을 파일 내용으로 만든다.
//
// 한 줄에 "<PCIe 경로> <포트 번호>" 하나씩이고, 적힌 순서가 곧 베이
// 1, 2, 3 … 이다. 목록이 비면 빈 결과를 돌려주고, 그러면 부팅 때 감지한
// 순서가 그대로 쓰인다.
func renderBayPlan(order []string) []byte {
	var b strings.Builder
	for _, line := range order {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return nil
	}
	return []byte(b.String())
}

// embedDSMCore adds the pre-decrypted DSM kernels to P2's file list.
//
// Under coreDir are per-model directories - SA6400/, say - each holding zImage,
// rd.gz and VERSION. Putting those on P2 under "dsm/<model>/<file>" lets
// vibeldr-boot find its own model's folder after booting and use it directly,
// with no download.
//
// Only a model with all three files goes in. One missing and that model is
// skipped and falls back to downloading at install time as usual. It returns
// how many models went in.
//
// embedDSMCore - 미리 복호화한 DSM 커널을 P2 파일 목록에 추가한다.
//
// coreDir 아래는 모델별 디렉터리(예: SA6400/) 이고, 각 안에 zImage / rd.gz /
// VERSION 이 있다. 이걸 P2 에 "dsm/<모델>/<파일>" 경로로 넣으면, 부팅 후
// vibeldr-boot 이 자기 모델 폴더를 찾아 다운로드 없이 바로 쓴다.
//
// 세 파일이 다 있는 모델만 넣는다. 하나라도 없으면 그 모델은 건너뛰고,
// 설치 때 평소대로 다운로드로 떨어진다. 넣은 모델 수를 돌려준다.
func embedDSMCore(coreDir string, dst *[]image.File) int {
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		return 0
	}
	embedded := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		model := e.Name()
		want := []string{"zImage", "rd.gz", "VERSION"}
		ok := true
		for _, f := range want {
			if st, err := os.Stat(filepath.Join(coreDir, model, f)); err != nil || st.IsDir() {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, f := range want {
			*dst = append(*dst, image.File{
				Path:   "dsm/" + model + "/" + f,
				Source: filepath.Join(coreDir, model, f),
			})
		}
		embedded++
	}
	return embedded
}
