package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config stores pull sources, push targets, and local WordPress behavior.
type Config struct {
	Provider               string
	User                   string
	Host                   string
	Port                   string
	RemotePath             string
	RemoteTmpDir           string
	PushUser               string
	PushHost               string
	PushPort               string
	PushRemotePath         string
	PushRemoteTmpDir       string
	PushURL                string
	LocalWPPath            string
	CloneImages            bool
	PluginRemoveFile       string
	LocalURL               string
	PullDomainReplacements []DomainReplacement
	PushDomainReplacements []DomainReplacement
	SkipSearchReplace      bool
	CloneDBHost            string
	CloneDBName            string
	CloneDBUser            string
	CloneDBPassword        string
	CloneDBPrefix          string
	Integration            string
}

// DomainReplacement stores an old-to-new WordPress URL/domain replacement pair.
type DomainReplacement struct {
	Old string
	New string
}

var (
	validSSHPart    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	validProvider   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	validPluginPath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// defaultConfig returns silent-mode defaults shared by pull and push commands.
func defaultConfig() Config {
	return Config{
		Provider:         defaultProviderName,
		RemoteTmpDir:     "/tmp",
		PushRemoteTmpDir: "/tmp",
	}
}

// loadConfig reads the config file for the selected runtime and applies environment overrides.
func loadConfig(projectRoot string) (Config, error) {
	return loadConfigPath(projectConfigPath(projectRoot))
}

// loadStandaloneConfig reads the standalone config file and applies environment overrides.
func loadStandaloneConfig(root string) (Config, error) {
	return loadConfigPath(standaloneConfigPath(root))
}

func loadConfigPath(path string) (Config, error) {
	cfg := defaultConfig()
	if _, err := os.Stat(path); err == nil {
		loaded, err := readConfigFile(path)
		if err != nil {
			return cfg, err
		}
		cfg = mergeConfig(cfg, loaded)
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}

	cfg.applyEnv(os.Environ())
	return cfg, nil
}

// readConfigFile parses the small YAML subset emitted by writeConfigFile.
func readConfigFile(path string) (Config, error) {
	cfg := Config{}
	contents, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	lines := strings.Split(string(contents), "\n")
	for index := 0; index < len(lines); index++ {
		rawLine := lines[index]
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if leadingSpaces(rawLine) > 0 {
			return cfg, fmt.Errorf("unexpected nested config line in %s: %s", path, line)
		}

		key, raw, ok := strings.Cut(line, ":")
		if !ok {
			return cfg, fmt.Errorf("invalid config line in %s: %s", path, line)
		}

		key = strings.TrimSpace(key)
		value := strings.TrimSpace(raw)
		switch key {
		case "domain_replacements", "pull_domain_replacements", "push_domain_replacements":
			replacements, nextIndex, err := parseDomainReplacements(lines, index+1, path)
			if err != nil {
				return cfg, err
			}
			if key == "push_domain_replacements" {
				cfg.PushDomainReplacements = replacements
			} else {
				cfg.PullDomainReplacements = replacements
			}
			index = nextIndex - 1
			continue
		}

		if strings.HasPrefix(value, "#") {
			value = ""
		}

		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}

		switch key {
		case "provider":
			cfg.Provider = value
		case "pull_user":
			cfg.User = value
		case "pull_host":
			cfg.Host = value
		case "pull_port":
			cfg.Port = value
		case "pull_remote_path":
			cfg.RemotePath = value
		case "pull_remote_tmp_dir":
			cfg.RemoteTmpDir = value
		case "push_user":
			cfg.PushUser = value
		case "push_host":
			cfg.PushHost = value
		case "push_port":
			cfg.PushPort = value
		case "push_remote_path":
			cfg.PushRemotePath = value
		case "push_remote_tmp_dir":
			cfg.PushRemoteTmpDir = value
		case "push_url":
			cfg.PushURL = value
		case "local_wp_path":
			cfg.LocalWPPath = value
		case "clone_images":
			cfg.CloneImages = parseBool(value)
		case "plugin_remove_file":
			cfg.PluginRemoveFile = value
		case "local_url":
			cfg.LocalURL = value
		case "integration":
			cfg.Integration = value
		case "skip_search_replace":
			cfg.SkipSearchReplace = parseBool(value)
		case "clone_db_host":
			cfg.CloneDBHost = value
		case "clone_db_name":
			cfg.CloneDBName = value
		case "clone_db_user":
			cfg.CloneDBUser = value
		case "clone_db_password":
			cfg.CloneDBPassword = value
		case "clone_db_prefix":
			cfg.CloneDBPrefix = value
		default:
			return cfg, fmt.Errorf("unknown config key %q in %s", key, path)
		}
	}

	return cfg, nil
}

func parseDomainReplacements(lines []string, start int, source string) ([]DomainReplacement, int, error) {
	replacements := []DomainReplacement{}
	current := DomainReplacement{}
	seenItem := false

	flush := func() error {
		if !seenItem {
			return nil
		}
		if current.Old == "" || current.New == "" {
			return fmt.Errorf("domain_replacements entries in %s require old and new values", source)
		}
		replacements = append(replacements, current)
		current = DomainReplacement{}
		seenItem = false
		return nil
	}

	index := start
	for ; index < len(lines); index++ {
		rawLine := lines[index]
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if leadingSpaces(rawLine) == 0 {
			break
		}
		if !strings.HasPrefix(line, "-") {
			key, value, ok := parseNestedYAMLScalar(line)
			if !ok || !seenItem {
				return nil, index, fmt.Errorf("invalid domain_replacements line in %s: %s", source, line)
			}
			assignDomainReplacementValue(&current, key, value)
			continue
		}
		if err := flush(); err != nil {
			return nil, index, err
		}
		seenItem = true
		rest := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if rest == "" {
			continue
		}
		key, value, ok := parseNestedYAMLScalar(rest)
		if !ok {
			return nil, index, fmt.Errorf("invalid domain_replacements line in %s: %s", source, line)
		}
		assignDomainReplacementValue(&current, key, value)
	}
	if err := flush(); err != nil {
		return nil, index, err
	}
	return replacements, index, nil
}

func parseNestedYAMLScalar(line string) (string, string, bool) {
	key, raw, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	value := strings.TrimSpace(raw)
	if unquoted, err := strconv.Unquote(value); err == nil {
		value = unquoted
	}
	return strings.TrimSpace(key), value, true
}

func assignDomainReplacementValue(replacement *DomainReplacement, key string, value string) {
	switch key {
	case "old":
		replacement.Old = value
	case "new":
		replacement.New = value
	}
}

func leadingSpaces(value string) int {
	count := 0
	for _, char := range value {
		if char != ' ' {
			return count
		}
		count++
	}
	return count
}

// writeConfigFile persists only values that differ from the runtime defaults.
func writeConfigFile(path string, cfg Config, defaults Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	body := strings.Builder{}
	body.WriteString("# wp-ssh-bridge project config. Generated by `wp-ssh-bridge init`.\n")
	writeStringValue(&body, "provider", cfg.Provider, defaults.Provider)
	writeStringValue(&body, "pull_user", cfg.User, defaults.User)
	writeStringValue(&body, "pull_host", cfg.Host, defaults.Host)
	writeStringValue(&body, "pull_port", cfg.Port, defaults.Port)
	writeStringValue(&body, "pull_remote_path", cfg.RemotePath, defaults.RemotePath)
	writeStringValue(&body, "pull_remote_tmp_dir", defaultString(cfg.RemoteTmpDir, "/tmp"), defaultString(defaults.RemoteTmpDir, "/tmp"))
	writeStringValue(&body, "push_user", cfg.PushUser, defaults.PushUser)
	writeStringValue(&body, "push_host", cfg.PushHost, defaults.PushHost)
	writeStringValue(&body, "push_port", cfg.PushPort, defaults.PushPort)
	writeStringValue(&body, "push_remote_path", cfg.PushRemotePath, defaults.PushRemotePath)
	writeStringValue(&body, "push_remote_tmp_dir", defaultString(cfg.PushRemoteTmpDir, "/tmp"), defaultString(defaults.PushRemoteTmpDir, "/tmp"))
	writeStringValue(&body, "push_url", cfg.PushURL, defaults.PushURL)
	writeStringValue(&body, "local_wp_path", cfg.LocalWPPath, defaults.LocalWPPath)
	writeBoolValue(&body, "clone_images", cfg.CloneImages, defaults.CloneImages)
	writeStringValue(&body, "plugin_remove_file", cfg.PluginRemoveFile, defaults.PluginRemoveFile)
	writeStringValue(&body, "local_url", cfg.LocalURL, defaults.LocalURL)
	writeStringValue(&body, "integration", cfg.Integration, defaults.Integration)
	writeDomainReplacements(&body, "pull_domain_replacements", cfg.PullDomainReplacements)
	writeDomainReplacements(&body, "push_domain_replacements", cfg.PushDomainReplacements)
	writeBoolValue(&body, "skip_search_replace", cfg.SkipSearchReplace, defaults.SkipSearchReplace)
	writeStringValue(&body, "clone_db_host", cfg.CloneDBHost, defaults.CloneDBHost)
	writeStringValue(&body, "clone_db_name", cfg.CloneDBName, defaults.CloneDBName)
	writeStringValue(&body, "clone_db_user", cfg.CloneDBUser, defaults.CloneDBUser)
	writeStringValue(&body, "clone_db_password", cfg.CloneDBPassword, defaults.CloneDBPassword)
	writeStringValue(&body, "clone_db_prefix", cfg.CloneDBPrefix, defaults.CloneDBPrefix)

	return os.WriteFile(path, []byte(body.String()), 0o644)
}

func writeDomainReplacements(body *strings.Builder, key string, replacements []DomainReplacement) {
	if len(replacements) == 0 {
		return
	}
	body.WriteString(key + ":\n")
	for _, replacement := range replacements {
		if replacement.Old == "" || replacement.New == "" {
			continue
		}
		body.WriteString(fmt.Sprintf("  - old: %s\n", quoteYAML(replacement.Old)))
		body.WriteString(fmt.Sprintf("    new: %s\n", quoteYAML(replacement.New)))
	}
}

func writeStringValue(body *strings.Builder, key string, value string, defaultValue string) {
	if value == "" || value == defaultValue {
		return
	}
	body.WriteString(fmt.Sprintf("%s: %s\n", key, quoteYAML(value)))
}

func writeBoolValue(body *strings.Builder, key string, value bool, defaultValue bool) {
	if value == defaultValue {
		return
	}
	body.WriteString(fmt.Sprintf("%s: %t\n", key, value))
}

// applyEnv overlays WP_SSH_* variables for provider compatibility.
func (cfg *Config) applyEnv(env []string) {
	values := envMap(env)
	cfg.User = firstNonEmpty(values["WP_SSH_PULL_USER"], cfg.User)
	cfg.Host = firstNonEmpty(values["WP_SSH_PULL_HOST"], cfg.Host)
	cfg.Port = firstNonEmpty(values["WP_SSH_PULL_PORT"], cfg.Port)
	cfg.RemotePath = firstNonEmpty(values["WP_SSH_PULL_REMOTE_PATH"], cfg.RemotePath)
	cfg.RemoteTmpDir = firstNonEmpty(values["WP_SSH_PULL_REMOTE_TMP_DIR"], cfg.RemoteTmpDir)
	cfg.PushUser = firstNonEmpty(values["WP_SSH_PUSH_USER"], cfg.PushUser)
	cfg.PushHost = firstNonEmpty(values["WP_SSH_PUSH_HOST"], cfg.PushHost)
	cfg.PushPort = firstNonEmpty(values["WP_SSH_PUSH_PORT"], cfg.PushPort)
	cfg.PushRemotePath = firstNonEmpty(values["WP_SSH_PUSH_REMOTE_PATH"], cfg.PushRemotePath)
	cfg.PushRemoteTmpDir = firstNonEmpty(values["WP_SSH_PUSH_REMOTE_TMP_DIR"], cfg.PushRemoteTmpDir)
	cfg.PushURL = firstNonEmpty(values["WP_SSH_PUSH_URL"], cfg.PushURL)
	cfg.LocalWPPath = firstNonEmpty(values["WP_SSH_LOCAL_WP_PATH"], values["WP_SSH_PULL_LOCAL_WP_PATH"], cfg.LocalWPPath)
	cfg.PluginRemoveFile = firstNonEmpty(values["WP_SSH_PULL_PLUGIN_REMOVE_FILE"], cfg.PluginRemoveFile)
	cfg.LocalURL = firstNonEmpty(values["WP_SSH_PULL_LOCAL_URL"], cfg.LocalURL)
	cfg.Provider = firstNonEmpty(values["WP_SSH_PROVIDER"], cfg.Provider)
	cfg.CloneDBHost = firstNonEmpty(values["WP_SSH_CLONE_DB_HOST"], cfg.CloneDBHost)
	cfg.CloneDBName = firstNonEmpty(values["WP_SSH_CLONE_DB_NAME"], cfg.CloneDBName)
	cfg.CloneDBUser = firstNonEmpty(values["WP_SSH_CLONE_DB_USER"], cfg.CloneDBUser)
	cfg.CloneDBPassword = firstNonEmpty(values["WP_SSH_CLONE_DB_PASSWORD"], cfg.CloneDBPassword)
	cfg.CloneDBPrefix = firstNonEmpty(values["WP_SSH_CLONE_DB_PREFIX"], cfg.CloneDBPrefix)

	if value, ok := values["WP_SSH_PULL_CLONE_IMAGES"]; ok {
		cfg.CloneImages = parseBool(value)
	}
	if value, ok := values["WP_SSH_PULL_SKIP_SEARCH_REPLACE"]; ok {
		cfg.SkipSearchReplace = parseBool(value)
	}
	if value, ok := values["WP_SSH_PUSH_SKIP_SEARCH_REPLACE"]; ok {
		cfg.SkipSearchReplace = parseBool(value)
	}
}

// RemoteTarget stores the SSH and WordPress root fields for one operation.
type RemoteTarget struct {
	User         string
	Host         string
	Port         string
	RemotePath   string
	RemoteTmpDir string
}

// pullTarget returns the configured upstream source.
func (cfg Config) pullTarget() RemoteTarget {
	return RemoteTarget{
		User:         cfg.User,
		Host:         cfg.Host,
		Port:         cfg.Port,
		RemotePath:   cfg.RemotePath,
		RemoteTmpDir: defaultString(cfg.RemoteTmpDir, "/tmp"),
	}
}

// pushTarget returns the configured upstream deployment target.
func (cfg Config) pushTarget() RemoteTarget {
	return RemoteTarget{
		User:         cfg.PushUser,
		Host:         cfg.PushHost,
		Port:         cfg.PushPort,
		RemotePath:   cfg.PushRemotePath,
		RemoteTmpDir: defaultString(cfg.PushRemoteTmpDir, "/tmp"),
	}
}

// validatePullRequired ensures source commands cannot be built from unsafe values.
func (cfg Config) validatePullRequired() error {
	return cfg.pullTarget().validate("pull source", "configure pull_user, pull_host, and pull_remote_path in wp-ssh.yaml, set WP_SSH_PULL_* env vars, or pass --user, --host, and --remote-path")
}

// validatePushRequired ensures push commands cannot be built from unsafe values.
func (cfg Config) validatePushRequired() error {
	return cfg.pushTarget().validate("push target", "set push_* config or pass --push-user, --push-host, and --push-remote-path")
}

// validateConfiguredPush validates a push target only when one was configured.
func (cfg Config) validateConfiguredPush() error {
	if !cfg.pushTarget().configured() {
		return nil
	}
	return cfg.validatePushRequired()
}

// validateInitValues permits partial setup while rejecting unsafe provided values.
func (cfg Config) validateInitValues() error {
	if err := cfg.pullTarget().validateValues("pull source"); err != nil {
		return err
	}
	return cfg.pushTarget().validateValues("push target")
}

// validate checks that SSH and remote path values are safe for command construction.
func (target RemoteTarget) validate(label string, help string) error {
	if target.User == "" {
		return fmt.Errorf("missing SSH user for %s; %s", label, help)
	}
	if target.Host == "" {
		return fmt.Errorf("missing SSH host for %s; %s", label, help)
	}
	if target.RemotePath == "" {
		return fmt.Errorf("missing remote WordPress path for %s; %s", label, help)
	}
	return target.validateValues(label)
}

// validateValues checks provided target values without requiring a complete target.
func (target RemoteTarget) validateValues(label string) error {
	if target.User != "" && !validSSHPart.MatchString(target.User) {
		return fmt.Errorf("user for %s must contain only letters, numbers, dots, underscores, or hyphens", label)
	}
	if target.Host != "" && !validSSHPart.MatchString(target.Host) {
		return fmt.Errorf("host for %s must contain only letters, numbers, dots, underscores, or hyphens", label)
	}
	if target.Port != "" {
		if _, err := strconv.Atoi(target.Port); err != nil {
			return fmt.Errorf("port for %s must be numeric", label)
		}
	}
	if strings.ContainsAny(target.RemotePath, "\r\n") {
		return fmt.Errorf("remote path for %s must not contain newlines", label)
	}
	if target.RemotePath != "" && !strings.HasPrefix(target.RemotePath, "/") {
		return fmt.Errorf("remote path for %s must be an absolute path, for example /home/site/web", label)
	}
	if strings.ContainsAny(defaultString(target.RemoteTmpDir, "/tmp"), "\r\n") {
		return fmt.Errorf("remote tmp dir for %s must not contain newlines", label)
	}
	if target.RemoteTmpDir != "" && !strings.HasPrefix(target.RemoteTmpDir, "/") {
		return fmt.Errorf("remote tmp dir for %s must be an absolute path, for example /tmp", label)
	}
	return nil
}

// configured reports whether any field for the target has been provided.
func (target RemoteTarget) configured() bool {
	return target.User != "" || target.Host != "" || target.Port != "" || target.RemotePath != ""
}

// validateCloneDBCredentials catches unsafe or incomplete target wp-config.php values.
func (cfg Config) validateCloneDBCredentials(required bool) error {
	missing := []string{}
	if cfg.CloneDBHost == "" {
		missing = append(missing, "--db-host or clone_db_host")
	}
	if cfg.CloneDBName == "" {
		missing = append(missing, "--db-name or clone_db_name")
	}
	if cfg.CloneDBUser == "" {
		missing = append(missing, "--db-user or clone_db_user")
	}
	if cfg.CloneDBPassword == "" {
		missing = append(missing, "--db-password or clone_db_password")
	}
	if required && len(missing) > 0 {
		return fmt.Errorf("clone requires target database credentials: %s", strings.Join(missing, ", "))
	}
	if !required && len(missing) > 0 && cfg.hasAnyCloneDBCredential() {
		return fmt.Errorf("clone database credentials are incomplete; provide %s", strings.Join(missing, ", "))
	}

	values := map[string]string{
		"clone_db_host":     cfg.CloneDBHost,
		"clone_db_name":     cfg.CloneDBName,
		"clone_db_user":     cfg.CloneDBUser,
		"clone_db_password": cfg.CloneDBPassword,
		"clone_db_prefix":   cfg.CloneDBPrefix,
	}
	for key, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must not contain newlines", key)
		}
	}
	if cfg.CloneDBPrefix != "" && regexp.MustCompile(`[^A-Za-z0-9_]`).MatchString(cfg.CloneDBPrefix) {
		return fmt.Errorf("clone_db_prefix must contain only letters, numbers, and underscores")
	}
	return nil
}

func (cfg Config) hasAnyCloneDBCredential() bool {
	return cfg.CloneDBHost != "" ||
		cfg.CloneDBName != "" ||
		cfg.CloneDBUser != "" ||
		cfg.CloneDBPassword != "" ||
		cfg.CloneDBPrefix != ""
}

// validateProviderName protects generated provider paths and DDEV command names.
func validateProviderName(provider string) error {
	if provider == "" {
		return errors.New("provider name is required")
	}
	if !validProvider.MatchString(provider) {
		return errors.New("provider name must contain only letters, numbers, dots, underscores, or hyphens")
	}
	return nil
}

// envArgs converts configuration into DDEV's legacy --environment payload.
func (cfg Config) envArgs() string {
	pairs := []string{}
	add := func(key, value string) {
		if value != "" {
			pairs = append(pairs, key+"="+value)
		}
	}

	add("WP_SSH_PULL_USER", cfg.User)
	add("WP_SSH_PULL_HOST", cfg.Host)
	add("WP_SSH_PULL_PORT", cfg.Port)
	add("WP_SSH_PULL_REMOTE_PATH", cfg.RemotePath)
	add("WP_SSH_PULL_REMOTE_TMP_DIR", cfg.RemoteTmpDir)
	add("WP_SSH_PUSH_USER", cfg.PushUser)
	add("WP_SSH_PUSH_HOST", cfg.PushHost)
	add("WP_SSH_PUSH_PORT", cfg.PushPort)
	add("WP_SSH_PUSH_REMOTE_PATH", cfg.PushRemotePath)
	add("WP_SSH_PUSH_REMOTE_TMP_DIR", cfg.PushRemoteTmpDir)
	add("WP_SSH_PUSH_URL", cfg.PushURL)
	add("WP_SSH_LOCAL_WP_PATH", cfg.LocalWPPath)
	add("WP_SSH_PULL_LOCAL_WP_PATH", cfg.LocalWPPath)
	add("WP_SSH_PULL_PLUGIN_REMOVE_FILE", cfg.PluginRemoveFile)
	add("WP_SSH_PULL_LOCAL_URL", cfg.LocalURL)
	add("WP_SSH_PROVIDER", cfg.Provider)
	add("WP_SSH_CLONE_DB_HOST", cfg.CloneDBHost)
	add("WP_SSH_CLONE_DB_NAME", cfg.CloneDBName)
	add("WP_SSH_CLONE_DB_USER", cfg.CloneDBUser)
	add("WP_SSH_CLONE_DB_PASSWORD", cfg.CloneDBPassword)
	add("WP_SSH_CLONE_DB_PREFIX", cfg.CloneDBPrefix)
	pairs = append(pairs, fmt.Sprintf("WP_SSH_PULL_CLONE_IMAGES=%t", cfg.CloneImages))
	pairs = append(pairs, fmt.Sprintf("WP_SSH_PULL_SKIP_SEARCH_REPLACE=%t", cfg.SkipSearchReplace))
	pairs = append(pairs, fmt.Sprintf("WP_SSH_PUSH_SKIP_SEARCH_REPLACE=%t", cfg.SkipSearchReplace))

	return strings.Join(pairs, ",")
}

// mergeConfig overlays non-zero values while preserving explicit false booleans from the base.
func mergeConfig(base Config, overlay Config) Config {
	if overlay.Integration != "" {
		base.Integration = overlay.Integration
	}
	if overlay.Provider != "" {
		base.Provider = overlay.Provider
	}
	if overlay.User != "" {
		base.User = overlay.User
	}
	if overlay.Host != "" {
		base.Host = overlay.Host
	}
	if overlay.Port != "" {
		base.Port = overlay.Port
	}
	if overlay.RemotePath != "" {
		base.RemotePath = overlay.RemotePath
	}
	if overlay.RemoteTmpDir != "" {
		base.RemoteTmpDir = overlay.RemoteTmpDir
	}
	if overlay.PushUser != "" {
		base.PushUser = overlay.PushUser
	}
	if overlay.PushHost != "" {
		base.PushHost = overlay.PushHost
	}
	if overlay.PushPort != "" {
		base.PushPort = overlay.PushPort
	}
	if overlay.PushRemotePath != "" {
		base.PushRemotePath = overlay.PushRemotePath
	}
	if overlay.PushRemoteTmpDir != "" {
		base.PushRemoteTmpDir = overlay.PushRemoteTmpDir
	}
	if overlay.PushURL != "" {
		base.PushURL = overlay.PushURL
	}
	if overlay.LocalWPPath != "" {
		base.LocalWPPath = overlay.LocalWPPath
	}
	if overlay.PluginRemoveFile != "" {
		base.PluginRemoveFile = overlay.PluginRemoveFile
	}
	if overlay.LocalURL != "" {
		base.LocalURL = overlay.LocalURL
	}
	if len(overlay.PullDomainReplacements) > 0 {
		base.PullDomainReplacements = overlay.PullDomainReplacements
	}
	if len(overlay.PushDomainReplacements) > 0 {
		base.PushDomainReplacements = overlay.PushDomainReplacements
	}
	if overlay.CloneDBHost != "" {
		base.CloneDBHost = overlay.CloneDBHost
	}
	if overlay.CloneDBName != "" {
		base.CloneDBName = overlay.CloneDBName
	}
	if overlay.CloneDBUser != "" {
		base.CloneDBUser = overlay.CloneDBUser
	}
	if overlay.CloneDBPassword != "" {
		base.CloneDBPassword = overlay.CloneDBPassword
	}
	if overlay.CloneDBPrefix != "" {
		base.CloneDBPrefix = overlay.CloneDBPrefix
	}
	base.CloneImages = overlay.CloneImages || base.CloneImages
	base.SkipSearchReplace = overlay.SkipSearchReplace || base.SkipSearchReplace
	return base
}

// projectConfigPath returns the CLI-owned project config location.
func projectConfigPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".ddev", configFileName)
}

// standaloneConfigPath returns the config path used outside DDEV projects.
func standaloneConfigPath(root string) string {
	return filepath.Join(root, ".wp-ssh.yaml")
}

// pluginListPath returns the configured plugin block list override path.
func pluginListPath(projectRoot string, cfg Config) string {
	if cfg.PluginRemoveFile != "" {
		if filepath.IsAbs(cfg.PluginRemoveFile) {
			return cfg.PluginRemoveFile
		}
		return filepath.Join(projectRoot, cfg.PluginRemoveFile)
	}
	return ""
}

// quoteYAML writes a conservative scalar compatible with YAML parsers.
func quoteYAML(value string) string {
	return strconv.Quote(value)
}

// parseBool accepts the truthy values supported by the old shell provider.
func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// envMap converts os.Environ-style values into a lookup map.
func envMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
