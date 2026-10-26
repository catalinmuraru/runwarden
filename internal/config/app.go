package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// App is the server configuration, read from environment variables.
type App struct {
	ListenAddr        string        // LISTEN_ADDR, default ":8080"
	RunnersDir        string        // RUNNERS_DIR, default "./runners"
	BaseDir           string        // BASE_DIR, default working dir; resolves relative paths in configs
	DataDir           string        // DATA_DIR, default "./data"; history and uploads
	MaxConcurrentRuns int           // MAX_CONCURRENT_RUNS, default 10
	RunTimeout        time.Duration // RUN_TIMEOUT, default 60m
	RunTimeoutMax     time.Duration // RUN_TIMEOUT_MAX, default 24h; largest per-script "timeout"
	KillGrace         time.Duration // KILL_GRACE, default 10s
	MaxOutputBytes    int64         // MAX_OUTPUT_BYTES, default 10Mi
	MaxUploadBytes    int64         // MAX_UPLOAD_BYTES, default 100Mi
	AllowedOrigins    []string      // ALLOWED_ORIGINS, comma-separated hosts allowed for WebSockets
	TrustedProxies    []string      // TRUSTED_PROXIES, CIDRs whose X-Forwarded-For is believed
	HistoryMaxAge     time.Duration // HISTORY_MAX_AGE, default 90d; 0 keeps runs forever
	HistoryMaxRuns    int           // HISTORY_MAX_RUNS, default 0 (no limit)
	HealthAddr        string        // HEALTH_ADDR, default ":8081"; /healthz and /readyz
	MaxRunsPerUser    int           // MAX_RUNS_PER_USER, default 1; 0 means no per-user limit
	RunHome           string        // RUN_HOME: "per-run" (default) or "shared"
	HomeSkeleton      string        // HOME_SKELETON: directory copied into every per-run HOME
	SecretsDir        string        // SECRETS_DIR, default "/etc/runwarden/secrets"; <dir>/<environment>/<NAME>
	ValuesMaxPerUser  int           // VALUES_MAX_PER_USER, default 2; concurrent dropdown helpers per user
	ValuesMaxGlobal   int           // VALUES_MAX_GLOBAL, default 10
	ValuesCacheTTL    time.Duration // VALUES_CACHE_TTL, default 60s; 0 disables the cache
	ActivityMaxDay    int64         // ACTIVITY_MAX_DAY_BYTES, default 50Mi; then failed sign-ins are not kept
	Auth              Auth
	Access            Access
}

// Access configures the access policy (roles, rules and exceptions are edited in the Admin area).
type Access struct {
	// Environments seed a new policy (POLICY_ENVIRONMENTS, JSON); an existing policy keeps its own.
	Environments       []EnvironmentSeed
	UnknownEnvironment string // POLICY_UNKNOWN_ENVIRONMENT; empty: the last environment
	MaxExceptionDays   int    // POLICY_MAX_EXCEPTION_DAYS, default 90; 0: no limit
	Warnings           []string
}

// EnvironmentSeed is an environment from POLICY_ENVIRONMENTS.
type EnvironmentSeed struct {
	Name  string   `json:"name"`
	Match []string `json:"match"`
}

// DefaultEnvironments seed a new policy when POLICY_ENVIRONMENTS is not set.
var DefaultEnvironments = []EnvironmentSeed{
	{Name: "development", Match: []string{"dev*", "test*"}},
	{Name: "acceptance", Match: []string{"acc*", "uat*"}},
	{Name: "production", Match: []string{"prod*", "prd*"}},
}

// Per-run HOME modes.
const (
	RunHomePerRun = "per-run"
	RunHomeShared = "shared"
)

// Sign-in methods.
const (
	AuthOIDC   = "oidc"
	AuthGitLab = "gitlab"
	AuthLDAP   = "ldap"
	AuthNone   = "none"
)

// Auth configures sign-in and sessions.
type Auth struct {
	// Methods offered on the sign-in page, in order (AUTH_METHODS: gitlab, oidc, ldap). Empty
	// is allowed only with InitialAdmin.
	Methods      []string
	NoAuth       bool          // AUTH_METHODS=none (+ ALLOW_NO_AUTH=true): no sign-in, everyone is an admin
	InitialAdmin bool          // INITIAL_ADMIN: built-in administrator with a generated password
	PublicURL    string        // PUBLIC_URL, e.g. https://runwarden.example.com
	SessionKey   []byte        // SESSION_KEY: 32 bytes, base64
	SessionTTL   time.Duration // SESSION_TTL, default 12h
	CookieSecure bool          // COOKIE_SECURE, default true unless PUBLIC_URL is http
	// Failed sign-ins allowed before more attempts are refused, besides the per user+address limit.
	MaxFailuresPerIP  int // LOGIN_MAX_FAILURES_PER_IP, default 20 per 15 minutes
	MaxFailuresGlobal int // LOGIN_MAX_FAILURES_GLOBAL, default 100 per minute
	OIDC              OIDC
	GitLab            OIDC
	LDAP              LDAP
	// Warnings are problems that do not stop the server (e.g. deprecated settings).
	Warnings []string
}

// Enabled reports whether method is offered.
func (a Auth) Enabled(method string) bool { return slices.Contains(a.Methods, method) }

// OIDC configures OpenID Connect sign-in (also used for GitLab).
type OIDC struct {
	IssuerURL     string
	ClientID      string
	ClientSecret  string
	Scopes        []string
	UsernameClaim string
	GroupsClaims  []string // tried in order; the first one present wins
	Label         string   // shown on the sign-in page
	RequiredGroup string   // only members of this group (or its subgroups) may sign in
}

// LDAP configures LDAP sign-in.
type LDAP struct {
	URL            string
	StartTLS       bool
	BindDN         string
	BindPassword   string
	UserBaseDN     string
	UserFilter     string // {username} is replaced by the escaped login name
	GroupBaseDN    string
	GroupFilter    string // {dn} and {username} are replaced, escaped
	GroupNameAttr  string
	UserGroupsAttr string // read groups from this user attribute (e.g. memberOf) instead of searching
	Label          string // shown on the sign-in page
}

// LoadApp reads the configuration through getenv (usually os.Getenv). All problems are
// reported together.
func LoadApp(getenv func(string) string) (App, error) {
	wd, err := os.Getwd()
	if err != nil {
		return App{}, err
	}
	var errs []error
	str := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}
	dir := func(name, def string) string {
		p, err := filepath.Abs(str(name, def))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		return p
	}
	num := func(name string, def int64, parse func(string) (int64, error)) int64 {
		raw := str(name, "")
		if raw == "" {
			return def
		}
		v, err := parse(raw)
		if err != nil || v <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid value %q", name, raw))
			return def
		}
		return v
	}
	dur := func(name string, def time.Duration) time.Duration {
		return time.Duration(num(name, int64(def), func(s string) (int64, error) {
			d, err := time.ParseDuration(s)
			return int64(d), err
		}))
	}
	integer := func(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

	app := App{
		ListenAddr:        str("LISTEN_ADDR", ":8080"),
		RunnersDir:        dir("RUNNERS_DIR", "runners"),
		BaseDir:           dir("BASE_DIR", wd),
		DataDir:           dir("DATA_DIR", "data"),
		MaxConcurrentRuns: int(num("MAX_CONCURRENT_RUNS", 10, integer)),
		RunTimeout:        dur("RUN_TIMEOUT", 60*time.Minute),
		RunTimeoutMax:     dur("RUN_TIMEOUT_MAX", 24*time.Hour),
		KillGrace:         dur("KILL_GRACE", 10*time.Second),
		MaxOutputBytes:    num("MAX_OUTPUT_BYTES", 10<<20, parseBytes),
		MaxUploadBytes:    num("MAX_UPLOAD_BYTES", 100<<20, parseBytes),
	}
	app.AllowedOrigins = list(getenv("ALLOWED_ORIGINS"))
	app.TrustedProxies = list(getenv("TRUSTED_PROXIES"))
	app.HistoryMaxAge = 90 * 24 * time.Hour
	if raw := str("HISTORY_MAX_AGE", ""); raw != "" {
		d, err := parseDays(raw)
		if err != nil || d < 0 {
			errs = append(errs, fmt.Errorf("HISTORY_MAX_AGE: invalid value %q (e.g. 90d, 720h, 0 to keep forever)", raw))
		}
		app.HistoryMaxAge = d
	}
	if raw := str("HISTORY_MAX_RUNS", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("HISTORY_MAX_RUNS: invalid value %q", raw))
		}
		app.HistoryMaxRuns = n
	}
	for _, cidr := range app.TrustedProxies {
		if err := checkProxyCIDR(cidr); err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %w", err))
		}
	}
	// Settings where 0 is meaningful (disabled / unlimited).
	count := func(name string, def int) int {
		raw := str(name, "")
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s: invalid value %q", name, raw))
			return def
		}
		return n
	}
	app.HealthAddr = str("HEALTH_ADDR", ":8081")
	app.MaxRunsPerUser = count("MAX_RUNS_PER_USER", 1)
	app.ValuesMaxPerUser = int(num("VALUES_MAX_PER_USER", 2, integer))
	app.ValuesMaxGlobal = int(num("VALUES_MAX_GLOBAL", 10, integer))
	app.ValuesCacheTTL = 60 * time.Second
	if raw := str("VALUES_CACHE_TTL", ""); raw != "" {
		d, err := parseDays(raw)
		if err != nil || d < 0 {
			errs = append(errs, fmt.Errorf("VALUES_CACHE_TTL: invalid value %q (e.g. 60s, 0 to disable)", raw))
		}
		app.ValuesCacheTTL = d
	}
	app.ActivityMaxDay = num("ACTIVITY_MAX_DAY_BYTES", 50<<20, parseBytes)
	app.RunHome = strings.ToLower(str("RUN_HOME", RunHomePerRun))
	if app.RunHome != RunHomePerRun && app.RunHome != RunHomeShared {
		errs = append(errs, fmt.Errorf("RUN_HOME: unknown value %q (want per-run or shared)", app.RunHome))
	}
	app.SecretsDir = dir("SECRETS_DIR", "/etc/runwarden/secrets")
	if raw := str("HOME_SKELETON", ""); raw != "" {
		app.HomeSkeleton = dir("HOME_SKELETON", raw)
		if info, err := os.Stat(app.HomeSkeleton); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Errorf("HOME_SKELETON: %s is not a directory", app.HomeSkeleton))
		}
	}
	app.Auth = loadAuth(getenv, str, dur, &errs)
	app.Auth.MaxFailuresPerIP = count("LOGIN_MAX_FAILURES_PER_IP", 20)
	app.Auth.MaxFailuresGlobal = count("LOGIN_MAX_FAILURES_GLOBAL", 100)
	app.Access = loadAccess(str, &errs)
	app.Access.MaxExceptionDays = count("POLICY_MAX_EXCEPTION_DAYS", 90)
	return app, errors.Join(errs...)
}

func loadAccess(str func(string, string) string, errs *[]error) Access {
	a := Access{
		Environments:       DefaultEnvironments,
		UnknownEnvironment: str("POLICY_UNKNOWN_ENVIRONMENT", ""),
	}
	// Admins are only made in the Admin area: the built-in administrator (INITIAL_ADMIN) gives the
	// first devops user the role. Settings of older versions are reported, not applied.
	for _, name := range []string{"ADMIN_USERS", "ADMIN_GROUPS", "BOOTSTRAP_DEVOPS"} {
		if str(name, "") != "" {
			a.Warnings = append(a.Warnings, name+" is ignored: sign in as the built-in administrator (INITIAL_ADMIN=true) and give the devops role in the Admin area")
		}
	}
	if raw := str("POLICY_ENVIRONMENTS", ""); raw != "" {
		var envs []EnvironmentSeed
		if err := json.Unmarshal([]byte(raw), &envs); err != nil {
			*errs = append(*errs, fmt.Errorf(`POLICY_ENVIRONMENTS: must be JSON like [{"name":"development","match":["dev*"]}]: %w`, err))
		} else {
			a.Environments = envs
		}
	}
	return a
}

func loadAuth(getenv func(string) string, str func(string, string) string, dur func(string, time.Duration) time.Duration, errs *[]error) Auth {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(format, args...)) }
	flag := func(name string) bool {
		raw := str(name, "")
		if raw == "" {
			return false
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			fail("%s: invalid value %q", name, raw)
		}
		return v
	}
	a := Auth{
		PublicURL:    strings.TrimRight(str("PUBLIC_URL", ""), "/"),
		SessionTTL:   dur("SESSION_TTL", 12*time.Hour),
		InitialAdmin: flag("INITIAL_ADMIN"),
	}
	require := func(name, method string) string {
		v := str(name, "")
		if v == "" {
			fail("%s is required for the %s sign-in method", name, method)
		}
		return v
	}

	raw := strings.ToLower(str("AUTH_METHODS", ""))
	if raw == "" && str("AUTH_TYPE", "") != "" {
		raw = strings.ToLower(str("AUTH_TYPE", ""))
		a.Warnings = append(a.Warnings, "AUTH_TYPE is deprecated; use AUTH_METHODS (a comma-separated list)")
	}
	for _, m := range list(raw) {
		switch {
		case m != AuthGitLab && m != AuthOIDC && m != AuthLDAP && m != AuthNone:
			fail("AUTH_METHODS: unknown method %q (want gitlab, oidc, ldap, or none)", m)
		case slices.Contains(a.Methods, m):
			fail("AUTH_METHODS: %q is listed twice", m)
		default:
			a.Methods = append(a.Methods, m)
		}
	}
	if slices.Contains(a.Methods, AuthNone) {
		if len(a.Methods) > 1 || a.InitialAdmin {
			fail("AUTH_METHODS=none cannot be combined with other methods or INITIAL_ADMIN")
		}
		if v, _ := strconv.ParseBool(str("ALLOW_NO_AUTH", "")); !v {
			fail("AUTH_METHODS=none makes every visitor an admin; set ALLOW_NO_AUTH=true as well to confirm (local development only)")
		}
		a.Methods, a.NoAuth = nil, true
		return a
	}
	if len(a.Methods) == 0 && !a.InitialAdmin {
		fail("AUTH_METHODS is required: gitlab, oidc, ldap (comma-separated), or none for local development")
		return a
	}

	if raw := str("SESSION_KEY", ""); raw == "" {
		fail("SESSION_KEY is required (32 random bytes, base64: openssl rand -base64 32)")
	} else {
		key, err := decodeKey(raw)
		if err != nil || len(key) != 32 {
			fail("SESSION_KEY must be 32 random bytes, base64 encoded (e.g. openssl rand -base64 32)")
		}
		a.SessionKey = key
	}
	a.CookieSecure = !strings.HasPrefix(a.PublicURL, "http://")
	if raw := str("COOKIE_SECURE", ""); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			fail("COOKIE_SECURE: invalid value %q", raw)
		}
		a.CookieSecure = v
	}

	if a.Enabled(AuthGitLab) || a.Enabled(AuthOIDC) {
		if u, err := url.Parse(str("PUBLIC_URL", "")); err != nil || u.Scheme == "" || u.Host == "" {
			fail("PUBLIC_URL must be an absolute URL such as https://runwarden.example.com (needed by gitlab and oidc sign-in)")
		}
	}
	scopes := []string{"openid", "profile", "email"}
	for _, s := range strings.Fields(str("OIDC_SCOPES", "")) {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	if a.Enabled(AuthOIDC) {
		a.OIDC = OIDC{
			IssuerURL:     require("OIDC_ISSUER_URL", AuthOIDC),
			ClientID:      require("OIDC_CLIENT_ID", AuthOIDC),
			ClientSecret:  require("OIDC_CLIENT_SECRET", AuthOIDC),
			Scopes:        scopes,
			UsernameClaim: str("OIDC_USERNAME_CLAIM", "preferred_username"),
			GroupsClaims:  []string{str("OIDC_GROUPS_CLAIM", "groups")},
			Label:         str("OIDC_LABEL", "Single sign-on"),
			RequiredGroup: strings.Trim(str("OIDC_REQUIRED_GROUP", ""), "/"),
		}
	}
	if a.Enabled(AuthGitLab) {
		a.GitLab = OIDC{
			IssuerURL:     strings.TrimRight(str("GITLAB_URL", "https://gitlab.com"), "/"),
			ClientID:      require("GITLAB_CLIENT_ID", AuthGitLab),
			ClientSecret:  require("GITLAB_CLIENT_SECRET", AuthGitLab),
			Scopes:        []string{"openid", "profile", "email"},
			UsernameClaim: "nickname",
			GroupsClaims:  []string{"groups", "groups_direct"},
			Label:         str("GITLAB_LABEL", "GitLab"),
			RequiredGroup: strings.Trim(str("GITLAB_REQUIRED_GROUP", ""), "/"),
		}
	}
	if a.Enabled(AuthLDAP) {
		a.LDAP = LDAP{
			URL:            require("LDAP_URL", AuthLDAP),
			BindDN:         str("LDAP_BIND_DN", ""),
			BindPassword:   getenv("LDAP_BIND_PASSWORD"),
			UserBaseDN:     require("LDAP_USER_BASE_DN", AuthLDAP),
			UserFilter:     str("LDAP_USER_FILTER", "(uid={username})"),
			GroupBaseDN:    str("LDAP_GROUP_BASE_DN", ""),
			GroupFilter:    str("LDAP_GROUP_FILTER", "(member={dn})"),
			GroupNameAttr:  str("LDAP_GROUP_NAME_ATTR", "cn"),
			UserGroupsAttr: str("LDAP_USER_GROUPS_ATTR", ""),
			Label:          str("LDAP_LABEL", "Directory"),
			StartTLS:       flag("LDAP_START_TLS"),
		}
		if !strings.HasPrefix(a.LDAP.URL, "ldap://") && !strings.HasPrefix(a.LDAP.URL, "ldaps://") && a.LDAP.URL != "" {
			fail("LDAP_URL must start with ldap:// or ldaps://")
		}
		if strings.HasPrefix(a.LDAP.URL, "ldap://") && !a.LDAP.StartTLS && !flag("LDAP_INSECURE") {
			fail("LDAP_URL uses ldap:// without LDAP_START_TLS=true, so passwords would cross the network in clear text; use ldaps://, set LDAP_START_TLS=true, or LDAP_INSECURE=true for local testing")
		}
		if !strings.Contains(a.LDAP.UserFilter, "{username}") {
			fail("LDAP_USER_FILTER must contain {username}")
		}
	}
	return a
}

// checkProxyCIDR refuses proxy ranges so wide that clients could spoof their address.
func checkProxyCIDR(s string) error {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		a, aerr := netip.ParseAddr(s)
		if aerr != nil {
			return fmt.Errorf("%q is not an address or CIDR", s)
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if (p.Addr().Is4() && p.Bits() < 8) || (!p.Addr().Is4() && p.Bits() < 32) {
		return fmt.Errorf("%q is too wide: only your ingress or load balancer ranges may set X-Forwarded-For", s)
	}
	return nil
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// list splits a comma-separated value, dropping blanks.
func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseDays is time.ParseDuration plus a "d" (days) unit: 90d, 36h, 0.
func parseDays(s string) (time.Duration, error) {
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(rest)
		return time.Duration(n) * 24 * time.Hour, err
	}
	if s == "0" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// parseBytes accepts a byte count with an optional binary suffix: 512, 64Ki, 10Mi, 1Gi.
func parseBytes(s string) (int64, error) {
	mult := int64(1)
	for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30} {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			s, mult = rest, m
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}
