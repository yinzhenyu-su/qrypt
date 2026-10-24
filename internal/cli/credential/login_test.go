package credential

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeLoginProvider struct {
	loginAccount  string
	resultCookie  string
	loginErr      error
	calls         int
	lastChallenge string
	afterLogin    func()
}

func (p *fakeLoginProvider) Login(_ context.Context, ui drive.LoginUI) (drive.LoginResult, error) {
	p.calls++
	if p.loginErr != nil {
		return drive.LoginResult{}, p.loginErr
	}
	if ui != nil {
		p.lastChallenge = "https://example.invalid/qr?token=secret-token"
		if err := ui.ShowChallenge(p.lastChallenge); err != nil {
			return drive.LoginResult{}, err
		}
	}
	if p.afterLogin != nil {
		p.afterLogin()
	}
	return drive.LoginResult{
		Params:  drive.Params{"cookie": p.resultCookie},
		Account: p.loginAccount,
	}, nil
}

func (p *fakeLoginProvider) Account(params drive.Params) (string, bool) {
	account := cookieAccountForTest(params["cookie"])
	return account, account != ""
}

func cookieAccountForTest(cookie string) string {
	for _, pair := range strings.Split(cookie, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && key == "__uid" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func installFakeLoginProvider(t *testing.T, provider *fakeLoginProvider) {
	t.Helper()
	previous, had := drive.LoginProviderFor("quark")
	drive.RegisterLoginProvider("quark", provider)
	t.Cleanup(func() {
		if had {
			drive.RegisterLoginProvider("quark", previous)
		}
	})
}

func loginRuntime(cfg *config.Config, path string) cliruntime.TestRuntime {
	rt := testRuntime(cfg, path)
	rt.ShutdownContextFn = func(*cobra.Command) (context.Context, func()) {
		return context.Background(), func() {}
	}
	return rt
}

func runLoginCommand(t *testing.T, rt cliruntime.Runtime, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(rt)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func withTerminal(t *testing.T, stdin, stdout bool) {
	t.Helper()
	oldIn, oldOut := stdinIsTerminal, stdoutIsTerminal
	stdinIsTerminal = func(*cobra.Command) bool { return stdin }
	stdoutIsTerminal = func(*cobra.Command) bool { return stdout }
	t.Cleanup(func() {
		stdinIsTerminal, stdoutIsTerminal = oldIn, oldOut
	})
}

func loginFixture() *config.Config {
	return &config.Config{
		Version: "1",
		Mount:   config.MountOptions{MountPoint: "~/Qrypt"},
		Credentials: []config.CredentialConfig{{
			Name:   "quark-main",
			Type:   "quark",
			Params: config.ParamMap{"cookie": "__uid=42; __pus=old"},
		}},
		Mounts: []config.MountConfig{{
			Name:       "docs",
			Type:       "quark",
			Credential: "quark-main",
			Params:     config.ParamMap{"root_path": "/Docs"},
		}},
	}
}

func TestCredentialLoginBootstrapsConfig(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "qrypt.toml")
	rt := loginRuntime(nil, path)

	out, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input",
		"--mount-name", "quark", "--mount-point", "~/Qrypt")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := config.Validate(loaded); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	cred, ok := loaded.CredentialFor("quark")
	if !ok || cred.Params["cookie"] != "__uid=42; __pus=new" {
		t.Fatalf("credential = %+v, want saved cookie", cred)
	}
	if _, ok := loaded.MountByName("quark"); !ok {
		t.Fatal("mount was not created")
	}
	if !strings.Contains(out, "qrypt mount") {
		t.Fatalf("output = %q, want next step", out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %o, want 700", parentInfo.Mode().Perm())
	}
	if strings.Contains(out, "__pus") || strings.Contains(out, "__uid") {
		t.Fatalf("output = %q, leaks credential material", out)
	}
}

func TestCredentialLoginReusesSameAccount(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	out, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if len(cfg.Credentials) != 1 {
		t.Fatalf("credentials = %d, want reuse of the existing one", len(cfg.Credentials))
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=new" {
		t.Fatalf("cookie = %q, want updated", cfg.Credentials[0].Params["cookie"])
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("mounts = %d, want no new mount", len(cfg.Mounts))
	}
	if !strings.Contains(out, "qrypt mount add") {
		t.Fatalf("output = %q, want mount add next step", out)
	}
}

func TestCredentialLoginRefusesDifferentAccount(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "99", resultCookie: "__uid=99; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--name", "quark-main", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "authenticated account") {
		t.Fatalf("err = %v, want account conflict", err)
	}
	if !strings.Contains(err.Error(), "qrypt credential login --type quark --name") {
		t.Fatalf("err = %v, want a runnable --name suggestion", err)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=old" {
		t.Fatal("credential changed on conflict")
	}
}

func TestCredentialLoginExplicitUnknownAccountWarns(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "99", resultCookie: "__uid=99; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	cfg.Credentials[0].Params = config.ParamMap{"cookie": "opaque"}
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	out, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--name", "quark-main", "--no-input")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if !strings.Contains(out, "could not be verified") {
		t.Fatalf("output = %q, want explicit-override warning", out)
	}
	if strings.Contains(out, "same account") {
		t.Fatalf("output = %q, must not claim same-account reuse", out)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=99; __pus=new" {
		t.Fatal("credential was not updated")
	}
}

func TestCredentialLoginNonInteractiveSkipsMount(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	out, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("mounts = %d, want no new mount", len(cfg.Mounts))
	}
	if !strings.Contains(out, "qrypt mount add") {
		t.Fatalf("output = %q, want mount add guidance", out)
	}
}

func TestCredentialLoginFreshNonInteractiveRequiresMount(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42"}
	installFakeLoginProvider(t, provider)
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(nil, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "--mount-name") {
		t.Fatalf("err = %v, want mount flag guidance", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 before login", provider.calls)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config was created despite the pre-check: %v", statErr)
	}
}

func TestCredentialLoginInteractiveWizard(t *testing.T) {
	withTerminal(t, true, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	out, err := runLoginCommand(t, rt, "\n\n\n", "login", "--type", "quark")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	mountCfg, ok := cfg.MountByName("quark-main")
	if !ok {
		t.Fatalf("mounts = %+v, want a new quark-main mount", cfg.Mounts)
	}
	if mountCfg.Params["root_path"] != "/" {
		t.Fatalf("root_path = %q, want /", mountCfg.Params["root_path"])
	}
	if cfg.EffectiveMountPoint() != "~/Qrypt" {
		t.Fatalf("mount point = %q, want ~/Qrypt", cfg.EffectiveMountPoint())
	}
	if !strings.Contains(out, "qrypt mount") {
		t.Fatalf("output = %q, want next step", out)
	}
}

func TestCredentialLoginMountFlagsCreateMount(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	out, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input",
		"--mount-name", "extra", "--root-path", "/Extra", "--mount-point", "/mnt/qrypt")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	mountCfg, ok := cfg.MountByName("extra")
	if !ok {
		t.Fatal("mount was not created")
	}
	if mountCfg.Params["root_path"] != "/Extra" || mountCfg.Credential != "quark-main" {
		t.Fatalf("mount = %+v, want root and reused credential", mountCfg)
	}
	if cfg.EffectiveMountPoint() != "/mnt/qrypt" {
		t.Fatalf("mount point = %q, want /mnt/qrypt", cfg.EffectiveMountPoint())
	}
}

func TestCredentialLoginRejectsMountCollision(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input", "--mount-name", "docs")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want mount collision", err)
	}
}

func TestCredentialLoginRejectsAccountMismatch(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "99", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "reported account") {
		t.Fatalf("err = %v, want provider inconsistency", err)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=old" {
		t.Fatal("credential changed despite the mismatch")
	}
}

func TestCredentialLoginSaveFailureLeavesConfigUnchanged(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	oldSave := saveExistingConfig
	saveCalls := 0
	saveExistingConfig = func(context.Context, cliruntime.ConfigState, func(*config.Config) error) error {
		saveCalls++
		return context.DeadlineExceeded
	}
	t.Cleanup(func() { saveExistingConfig = oldSave })

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil {
		t.Fatal("login succeeded, want save failure")
	}
	if saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", saveCalls)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=old" {
		t.Fatal("in-memory config changed despite save failure")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config exists despite save failure: %v", statErr)
	}
}

func TestCredentialListShowsConfigAccount(t *testing.T) {
	cfg := loginFixture()
	cfg.Storage = config.StorageConfig{WorkDir: t.TempDir()}
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := testRuntime(cfg, path)

	out, err := run(t, rt, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	fields := strings.Fields(strings.Split(strings.TrimSpace(out), "\n")[1])
	if len(fields) < 5 {
		t.Fatalf("list row = %q", out)
	}
	if fields[0] != "quark-main" || fields[2] != "42" || fields[3] != "config" || fields[4] != "-" {
		t.Fatalf("row = %v, want account 42 with source config and no version", fields)
	}

	show, err := run(t, rt, "show", "quark-main")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(show, "account: 42") || !strings.Contains(show, "source: config") {
		t.Fatalf("show = %q, want config account", show)
	}
	if !strings.Contains(show, "version: -") || !strings.Contains(show, "updated_at: -") {
		t.Fatalf("show = %q, want unknown session fields as -", show)
	}
}

func TestCredentialShowUnknownFields(t *testing.T) {
	cfg := loginFixture()
	cfg.Storage = config.StorageConfig{WorkDir: t.TempDir()}
	cfg.Credentials[0].Params = config.ParamMap{"cookie": "opaque"}
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := testRuntime(cfg, path)

	out, err := run(t, rt, "show", "quark-main")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"account: -", "source: -", "version: -", "updated_at: -"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show = %q, want %q", out, want)
		}
	}
}

func TestCredentialLoginProviderFailureSuggestsRetry(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginErr: errors.New("scan failed")}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "Retry: qrypt credential login --type quark") {
		t.Fatalf("err = %v, want a runnable retry", err)
	}
}

func TestCredentialLoginRequiresProvider(t *testing.T) {
	withTerminal(t, false, false)
	rt := loginRuntime(nil, "")
	_, err := runLoginCommand(t, rt, "", "login")
	if err == nil || !strings.Contains(err.Error(), "Available login providers") {
		t.Fatalf("err = %v, want provider guidance", err)
	}
}

func TestCredentialLoginRejectsUnsupportedProvider(t *testing.T) {
	withTerminal(t, false, false)
	rt := loginRuntime(nil, "")
	_, err := runLoginCommand(t, rt, "", "login", "--type", "localfs")
	if err == nil || !strings.Contains(err.Error(), "does not support login") {
		t.Fatalf("err = %v, want unsupported-provider error", err)
	}
}

func TestCredentialLoginDoesNotReuseUnknownAccount(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "99", resultCookie: "__uid=99; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	cfg.Credentials[0].Params = config.ParamMap{"cookie": "opaque"}
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	out, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if len(cfg.Credentials) != 2 {
		t.Fatalf("credentials = %d, want the old one plus a new derived credential", len(cfg.Credentials))
	}
	if cfg.Credentials[0].Params["cookie"] != "opaque" {
		t.Fatal("unknown-account credential was silently overwritten")
	}
	if _, ok := cfg.CredentialFor("quark"); !ok {
		t.Fatalf("credentials = %+v, want a derived quark credential", cfg.Credentials)
	}
}

func TestCredentialLoginWizardValidationDoesNotSave(t *testing.T) {
	withTerminal(t, true, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	oldSave := saveExistingConfig
	saveCalls := 0
	saveExistingConfig = func(context.Context, cliruntime.ConfigState, func(*config.Config) error) error {
		saveCalls++
		return nil
	}
	t.Cleanup(func() { saveExistingConfig = oldSave })

	_, err := runLoginCommand(t, rt, "bad/name\n\n\n", "login", "--type", "quark")
	if err == nil || !strings.Contains(err.Error(), "single path component") {
		t.Fatalf("err = %v, want mount-name validation", err)
	}
	if saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0", saveCalls)
	}
	if !strings.Contains(err.Error(), "Retry: qrypt credential login --type quark") {
		t.Fatalf("err = %v, want a runnable retry", err)
	}
}

func TestCredentialLoginWriteFailureKeepsOldFile(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("permission-based write failure is not reliable in this environment")
	}
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	dir := t.TempDir()
	path := filepath.Join(dir, "qrypt.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	rt := loginRuntime(cfg, path)

	_, err = runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil {
		t.Fatal("login succeeded, want write failure")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read config after failure: %v", readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("config changed despite a failed write")
	}
}

func TestDeriveMountNameAvoidsReservedAndExisting(t *testing.T) {
	if got := deriveMountName("list", nil); got != "list-2" {
		t.Fatalf("deriveMountName(list) = %q, want list-2", got)
	}
	cfg := &config.Config{Mounts: []config.MountConfig{{Name: "docs"}}}
	if got := deriveMountName("docs", cfg); got != "docs-2" {
		t.Fatalf("deriveMountName(docs) = %q, want docs-2", got)
	}
}

func TestTerminalLoginUIQRCode(t *testing.T) {
	var buf bytes.Buffer
	ui := terminalLoginUI{out: &buf, loc: i18n.New(i18n.LangEN), renderQR: true}
	if err := ui.ShowChallenge("https://example.invalid/qr?token=x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "https://example.invalid/qr?token=x") {
		t.Fatalf("output = %q, want URL", buf.String())
	}
	if !strings.ContainsAny(buf.String(), "█▀▄") {
		t.Fatalf("output = %q, want a rendered QR code", buf.String())
	}

	buf.Reset()
	ui.renderQR = false
	if err := ui.ShowChallenge("https://example.invalid/qr?token=x"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(buf.String(), "█▀▄") {
		t.Fatalf("output = %q, want no QR blocks when rendering is disabled", buf.String())
	}
}

func TestCredentialLoginInteractiveBootstrap(t *testing.T) {
	withTerminal(t, true, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	path := filepath.Join(t.TempDir(), "nested", "qrypt.toml")
	rt := loginRuntime(nil, path)

	out, err := runLoginCommand(t, rt, "\n\n\n", "login", "--type", "quark")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	mountCfg, ok := loaded.MountByName("quark")
	if !ok {
		t.Fatalf("mounts = %+v, want a quark mount", loaded.Mounts)
	}
	if mountCfg.Params["root_path"] != "/" {
		t.Fatalf("root_path = %q, want /", mountCfg.Params["root_path"])
	}
	if loaded.EffectiveMountPoint() != "~/Qrypt" {
		t.Fatalf("mount point = %q, want ~/Qrypt", loaded.EffectiveMountPoint())
	}
}

func TestCredentialLoginCancellationAfterProviderDoesNotSave(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	ctx, cancel := context.WithCancel(context.Background())
	rt := testRuntime(cfg, path)
	rt.ShutdownContextFn = func(*cobra.Command) (context.Context, func()) { return ctx, cancel }
	provider.afterLogin = cancel

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want cancellation before save", err)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=old" {
		t.Fatal("credential changed after cancellation")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config was written after cancellation: %v", statErr)
	}
}

func TestCredentialLoginTimeoutAfterProviderDoesNotSave(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	rt := testRuntime(cfg, path)
	rt.ShutdownContextFn = func(*cobra.Command) (context.Context, func()) { return ctx, cancel }

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout before save", err)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=old" {
		t.Fatal("credential changed after timeout")
	}
}

func TestPromptLoginValueCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	_, err := promptLoginValue(ctx, cmd, bufio.NewReader(reader), i18n.New(i18n.LangEN), "out.credential.login.mount_name", "mount")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want cancellation", err)
	}
}

func TestCredentialLoginCancellationDuringSaveDoesNotWrite(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginAccount: "42", resultCookie: "__uid=42; __pus=new"}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := testRuntime(cfg, path)
	rt.ShutdownContextFn = func(*cobra.Command) (context.Context, func()) { return ctx, cancel }

	oldSave := saveExistingConfig
	saveExistingConfig = func(_ context.Context, state cliruntime.ConfigState, mutate func(*config.Config) error) error {
		cancel()
		return mutate(state.Cfg)
	}
	t.Cleanup(func() { saveExistingConfig = oldSave })

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want cancellation inside the save step", err)
	}
	if cfg.Credentials[0].Params["cookie"] != "__uid=42; __pus=old" {
		t.Fatal("credential changed after cancellation inside the save step")
	}
}

func TestCredentialLoginRetryKeepsTimeout(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginErr: errors.New("scan failed")}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input", "--timeout", "90s")
	if err == nil || !strings.Contains(err.Error(), "--timeout 1m30s") {
		t.Fatalf("err = %v, want the custom timeout in the retry command", err)
	}
}

func TestCredentialLoginRedactsChallengeURL(t *testing.T) {
	withTerminal(t, false, false)
	provider := &fakeLoginProvider{loginErr: errors.New("cannot display https://su.quark.cn/4_eMHBJ?token=secret-token&client_id=532")}
	installFakeLoginProvider(t, provider)
	cfg := loginFixture()
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	rt := loginRuntime(cfg, path)

	_, err := runLoginCommand(t, rt, "", "login", "--type", "quark", "--no-input")
	if err == nil {
		t.Fatal("login succeeded, want provider error")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "su.quark.cn") {
		t.Fatalf("err = %v, leaks the challenge URL", err)
	}
	if !strings.Contains(err.Error(), "redacted-challenge-url") {
		t.Fatalf("err = %v, want a redaction marker", err)
	}
}
