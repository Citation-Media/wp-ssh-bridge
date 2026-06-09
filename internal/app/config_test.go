package app

import (
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

	if err := writeConfigFile(path, cfg); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	got, err := readConfigFile(path)
	if err != nil {
		t.Fatalf("readConfigFile() error = %v", err)
	}

	if got != cfg {
		t.Fatalf("read config mismatch\nwant: %#v\n got: %#v", cfg, got)
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
	listPath := filepath.Join(dir, ".ddev", "wp-ssh-plugins.txt")
	if err := os.WriteFile(listPath, []byte("updraftplus\n# comment\nplugin/file.php\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plugins, err := readPluginList(dir, Config{})
	if err != nil {
		t.Fatalf("readPluginList() error = %v", err)
	}
	if strings.Join(plugins, ",") != "updraftplus,plugin/file.php" {
		t.Fatalf("unexpected plugins: %#v", plugins)
	}

	if err := os.WriteFile(listPath, []byte("../secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPluginList(dir, Config{}); err == nil {
		t.Fatal("readPluginList() accepted traversal entry")
	}
}

func TestStandalonePathsDoNotUseDDEVDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := standaloneConfigPath(dir); got != filepath.Join(dir, ".wp-ssh.yaml") {
		t.Fatalf("standaloneConfigPath() = %q", got)
	}
	if got := pluginListPath(dir, Config{}); got != filepath.Join(dir, ".wp-ssh-plugins.txt") {
		t.Fatalf("standalone pluginListPath() = %q", got)
	}
	if got := downloadsDir(dir); got != filepath.Join(dir, ".wp-ssh", ".downloads") {
		t.Fatalf("standalone downloadsDir() = %q", got)
	}
}
