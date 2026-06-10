package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReadConfigFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".ddev", configFileName)
	cfg := Config{
		Provider:          "live",
		User:              "deploy",
		Host:              "example.com",
		Port:              "2222",
		RemotePath:        "/home/site/public_html",
		RemoteTmpDir:      "/var/tmp",
		PushUser:          "release",
		PushHost:          "staging.example.com",
		PushPort:          "2223",
		PushRemotePath:    "/home/staging/public_html",
		PushRemoteTmpDir:  "/tmp/push",
		PushURL:           "https://staging.example.com",
		LocalWPPath:       "public",
		CloneImages:       true,
		PluginRemoveFile:  ".ddev/plugins.txt",
		LocalURL:          "https://site.ddev.site",
		SkipSearchReplace: true,
	}

	if err := writeConfigFile(path, cfg, defaultConfig()); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"pull_user: \"deploy\"",
		"pull_host: \"example.com\"",
		"pull_port: \"2222\"",
		"pull_remote_path: \"/home/site/public_html\"",
		"pull_remote_tmp_dir: \"/var/tmp\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("written config missing %q:\n%s", want, text)
		}
	}
	for _, legacy := range []string{"\nuser:", "\nhost:", "\nport:", "\nremote_path:", "\nremote_tmp_dir:"} {
		if strings.Contains(text, legacy) {
			t.Fatalf("written config contains legacy pull key %q:\n%s", legacy, text)
		}
	}

	got, err := readConfigFile(path)
	if err != nil {
		t.Fatalf("readConfigFile() error = %v", err)
	}

	if got != cfg {
		t.Fatalf("read config mismatch\nwant: %#v\n got: %#v", cfg, got)
	}
}

func TestWriteConfigFileOmitsDefaultValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-ssh.yaml")
	cfg := defaultConfig()
	cfg.User = "deploy"
	cfg.Host = "example.com"
	cfg.RemotePath = "/home/site/web"

	if err := writeConfigFile(path, cfg, defaultConfig()); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, omitted := range []string{
		"provider:",
		"pull_remote_tmp_dir:",
		"push_remote_tmp_dir:",
		"clone_images:",
		"skip_search_replace:",
	} {
		if strings.Contains(text, omitted) {
			t.Fatalf("default key %q should be omitted:\n%s", omitted, text)
		}
	}
	for _, want := range []string{
		"pull_user: \"deploy\"",
		"pull_host: \"example.com\"",
		"pull_remote_path: \"/home/site/web\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("written config missing %q:\n%s", want, text)
		}
	}
}

func TestReadConfigFileRejectsLegacyPullKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-ssh.yaml")
	if err := os.WriteFile(path, []byte(`provider: "invalid"
user: "deploy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readConfigFile(path); err == nil {
		t.Fatal("readConfigFile() accepted legacy pull key")
	}
}

func TestValidateRequiredRejectsUnsafeSSHValues(t *testing.T) {
	t.Parallel()
	cfg := Config{User: "deploy;", Host: "example.com", RemotePath: "/var/www/html"}
	if err := cfg.validatePullRequired(); err == nil {
		t.Fatal("validatePullRequired() accepted unsafe user")
	}

	cfg = Config{User: "deploy", Host: "example.com", Port: "abc", RemotePath: "/var/www/html"}
	if err := cfg.validatePullRequired(); err == nil {
		t.Fatal("validatePullRequired() accepted non-numeric port")
	}

	cfg = Config{User: "deploy", Host: "example.com", RemotePath: "web"}
	if err := cfg.validatePullRequired(); err == nil {
		t.Fatal("validatePullRequired() accepted relative remote path")
	}

	cfg = Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html", RemoteTmpDir: "tmp"}
	if err := cfg.validatePullRequired(); err == nil {
		t.Fatal("validatePullRequired() accepted relative remote tmp dir")
	}
}

func TestValidatePushRequiredUsesPushTarget(t *testing.T) {
	t.Parallel()
	cfg := Config{User: "deploy", Host: "source.example.com", RemotePath: "/source"}
	if err := cfg.validatePushRequired(); err == nil {
		t.Fatal("validatePushRequired() accepted missing push target")
	}

	cfg.PushUser = "deploy"
	cfg.PushHost = "staging.example.com"
	cfg.PushRemotePath = "/home/staging/public_html"
	if err := cfg.validatePushRequired(); err != nil {
		t.Fatalf("validatePushRequired() error = %v", err)
	}
}

func TestValidateProviderNameRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"../live", "live/staging", "live;rm"} {
		if err := validateProviderName(provider); err == nil {
			t.Fatalf("validateProviderName(%q) accepted unsafe value", provider)
		}
	}
	if err := validateProviderName("live-staging_1"); err != nil {
		t.Fatalf("validateProviderName() rejected safe value: %v", err)
	}
}

func TestProviderInstallParserRejectsUnusedConfigFlags(t *testing.T) {
	t.Parallel()
	if _, err := parseProviderInstallCommand([]string{"--host", "example.com"}, io.Discard); err == nil {
		t.Fatal("parseProviderInstallCommand() accepted unused --host flag")
	}
}

func TestConfigOptionsRejectOperationFlags(t *testing.T) {
	t.Parallel()
	if err := (configOptions{SkipDB: true}).rejectOperationFlags("init"); err == nil {
		t.Fatal("rejectOperationFlags() accepted --skip-db for init")
	}
	if err := (configOptions{}).rejectOperationFlags("init"); err != nil {
		t.Fatalf("rejectOperationFlags() rejected empty flags: %v", err)
	}
}

func TestValidateInitValuesAllowsPartialConfig(t *testing.T) {
	t.Parallel()
	if err := (Config{}).validateInitValues(); err != nil {
		t.Fatalf("validateInitValues() rejected empty config: %v", err)
	}

	cfg := Config{User: "deploy;", Host: "", RemotePath: ""}
	if err := cfg.validateInitValues(); err == nil {
		t.Fatal("validateInitValues() accepted unsafe provided user")
	}

	cfg = Config{Port: "abc"}
	if err := cfg.validateInitValues(); err == nil {
		t.Fatal("validateInitValues() accepted unsafe provided port")
	}

	cfg = Config{RemotePath: "web"}
	if err := cfg.validateInitValues(); err == nil {
		t.Fatal("validateInitValues() accepted relative provided remote path")
	}
}

func TestEnvArgsIncludePullAndPushDefaults(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Provider:          "live",
		User:              "puller",
		Host:              "source.example.com",
		RemotePath:        "/source",
		PushUser:          "deployer",
		PushHost:          "staging.example.com",
		PushRemotePath:    "/target",
		PushURL:           "https://staging.example.com",
		LocalWPPath:       "public",
		SkipSearchReplace: true,
	}

	env := cfg.envArgs()
	for _, want := range []string{
		"WP_SSH_PULL_USER=puller",
		"WP_SSH_PUSH_USER=deployer",
		"WP_SSH_PUSH_REMOTE_PATH=/target",
		"WP_SSH_PUSH_URL=https://staging.example.com",
		"WP_SSH_LOCAL_WP_PATH=public",
		"WP_SSH_PUSH_SKIP_SEARCH_REPLACE=true",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env args missing %q:\n%s", want, env)
		}
	}
}

func TestReadPluginListValidatesEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(dir, ".ddev", "custom-plugins.txt")
	if err := os.WriteFile(listPath, []byte("updraftplus\n# comment\nplugin/file.php\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plugins, err := readPluginList(dir, Config{PluginRemoveFile: ".ddev/custom-plugins.txt"})
	if err != nil {
		t.Fatalf("readPluginList() error = %v", err)
	}
	joined := strings.Join(plugins, ",")
	for _, want := range []string{"wpvivid-backuprestore", "updraftplus", "plugin/file.php"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plugins missing %q: %#v", want, plugins)
		}
	}

	plugins, err = readPluginList(dir, Config{})
	if err != nil {
		t.Fatalf("readPluginList() default error = %v", err)
	}
	if !strings.Contains(strings.Join(plugins, ","), "wpvivid-backuprestore") {
		t.Fatalf("unexpected plugins: %#v", plugins)
	}

	if err := os.WriteFile(listPath, []byte("../secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPluginList(dir, Config{PluginRemoveFile: ".ddev/custom-plugins.txt"}); err == nil {
		t.Fatal("readPluginList() accepted traversal entry")
	}
}

func TestStandalonePathsDoNotUseDDEVDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := standaloneConfigPath(dir); got != filepath.Join(dir, ".wp-ssh.yaml") {
		t.Fatalf("standaloneConfigPath() = %q", got)
	}
	if got := pluginListPath(dir, Config{}); got != "" {
		t.Fatalf("standalone pluginListPath() = %q", got)
	}
	if got := pluginListPath(dir, Config{PluginRemoveFile: "plugins.txt"}); got != filepath.Join(dir, "plugins.txt") {
		t.Fatalf("configured pluginListPath() = %q", got)
	}
	if got := downloadsDir(dir); got != filepath.Join(dir, ".wp-ssh", ".downloads") {
		t.Fatalf("standalone downloadsDir() = %q", got)
	}
}
