// Package config loads application and bundled Redis settings from agent.conf.
package config

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Agent struct {
	Listen     string `json:"listen"`
	Endpoint   string `json:"endpoint"`
	Controller string `json:"controller"`
	StateDir   string `json:"stateDir"`
	Redis      string `json:"redis"`
	FRR        bool   `json:"frr"`
	Token      string `json:"token,omitempty"`
	Cert       string `json:"cert"`
	Key        string `json:"key"`
	CA         string `json:"ca"`
	Insecure   bool   `json:"insecure"`
}

type Redis struct {
	Enabled                bool   `json:"enabled"`
	Bind                   string `json:"bind"`
	Port                   int    `json:"port"`
	DataDir                string `json:"dataDir"`
	AppendOnly             bool   `json:"appendOnly"`
	AppendFsync            string `json:"appendFsync"`
	StartupTimeoutSeconds  int    `json:"startupTimeoutSeconds"`
	ShutdownTimeoutSeconds int    `json:"shutdownTimeoutSeconds"`
}

type Config struct {
	Agent Agent `json:"agent"`
	Redis Redis `json:"redis"`
}

type Options struct {
	Config
	ConfigPath string
	CheckOnly  bool
}

func Defaults() Config {
	return Config{
		Agent: Agent{Listen: ":9090", StateDir: "/var/lib/sdn-agent", FRR: true},
		Redis: Redis{Bind: "127.0.0.1", Port: 6379, DataDir: "/var/lib/redis", AppendOnly: true,
			AppendFsync: "always", StartupTimeoutSeconds: 30, ShutdownTimeoutSeconds: 30},
	}
}

// Parse retains the existing CLI defaults when no -config is provided. An
// explicitly present flag overrides its file value, including false/empty.
func Parse(args []string, getenv func(string) (string, bool), output io.Writer) (Options, error) {
	d := Defaults()
	var result Options
	fs := flag.NewFlagSet("sdn-agent", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&result.ConfigPath, "config", "", "strict JSON application and bundled Redis configuration")
	fs.BoolVar(&result.CheckOnly, "check-config", false, "validate configuration; print effective settings without token; do not start services")
	fs.StringVar(&d.Agent.Listen, "listen", d.Agent.Listen, "gRPC listener")
	fs.StringVar(&d.Agent.Endpoint, "endpoint", "", "advertised agent endpoint")
	fs.StringVar(&d.Agent.Controller, "controller", "", "controller endpoint")
	fs.StringVar(&d.Agent.StateDir, "state-dir", d.Agent.StateDir, "identity and development journal")
	fs.StringVar(&d.Agent.Redis, "redis", "", "Redis address (production journal)")
	fs.BoolVar(&d.Agent.FRR, "frr", true, "enable dedicated FRR adapter")
	fs.StringVar(&d.Agent.Cert, "cert", "", "TLS certificate")
	fs.StringVar(&d.Agent.Key, "key", "", "TLS private key")
	fs.StringVar(&d.Agent.CA, "ca", "", "controller CA")
	fs.BoolVar(&d.Agent.Insecure, "insecure", false, "allow plaintext in isolated lab")
	if err := fs.Parse(args); err != nil {
		return result, err
	}
	if fs.NArg() != 0 {
		return result, errors.New("unexpected positional argument")
	}
	result.Config = Defaults()
	if result.ConfigPath != "" {
		file, err := os.Open(result.ConfigPath)
		if err != nil {
			return result, fmt.Errorf("read agent configuration: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		if err != nil {
			return result, errors.New("cannot read agent configuration")
		}
		if len(data) > 1<<20 {
			return result, errors.New("agent configuration exceeds 1 MiB")
		}
		if err := decode(data, &result.Config); err != nil {
			return result, err
		}
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			result.Agent.Listen = d.Agent.Listen
		case "endpoint":
			result.Agent.Endpoint = d.Agent.Endpoint
		case "controller":
			result.Agent.Controller = d.Agent.Controller
		case "state-dir":
			result.Agent.StateDir = d.Agent.StateDir
		case "redis":
			result.Agent.Redis = d.Agent.Redis
		case "frr":
			result.Agent.FRR = d.Agent.FRR
		case "cert":
			result.Agent.Cert = d.Agent.Cert
		case "key":
			result.Agent.Key = d.Agent.Key
		case "ca":
			result.Agent.CA = d.Agent.CA
		case "insecure":
			result.Agent.Insecure = d.Agent.Insecure
		}
	})
	if token, present := getenv("SDN_TOKEN"); present {
		result.Agent.Token = token
	}
	if result.Agent.Endpoint == "" {
		result.Agent.Endpoint = result.Agent.Listen
	}
	if result.Redis.Enabled && result.Agent.Redis == "" {
		result.Agent.Redis = net.JoinHostPort(result.Redis.Bind, strconv.Itoa(result.Redis.Port))
	}
	return result, result.Validate()
}

func decode(data []byte, into *Config) error {
	// Reject duplicate keys and null rather than silently interpreting ambiguous
	// input. Error messages never include configuration values (which may be keys).
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("configuration nesting exceeds 32 levels")
		}
		t, err := d.Token()
		if err != nil {
			return errors.New("invalid configuration JSON")
		}
		if t == nil {
			return errors.New("configuration null values are not supported")
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return errors.New("invalid configuration JSON key")
				}
				name, ok := key.(string)
				folded := strings.ToLower(name)
				if !ok || seen[folded] {
					return errors.New("duplicate configuration JSON key")
				}
				seen[folded] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		} else if delim == '[' {
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		} else {
			return errors.New("invalid configuration JSON delimiter")
		}
		_, err = d.Token()
		if err != nil {
			return errors.New("invalid configuration JSON")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("configuration must contain one JSON object")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		// The decoder can echo an invalid string value; expose field names only.
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return fmt.Errorf("invalid configuration type for field %s", typeErr.Field)
		}
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return errors.New(err.Error())
		}
		return errors.New("invalid agent configuration JSON object")
	}
	return nil
}

func (c Config) Validate() error {
	checkAddress := func(label, value string, allowZero bool) error {
		_, port, err := net.SplitHostPort(value)
		if err != nil {
			return fmt.Errorf("%s must be host:port", label)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 0 || p > 65535 || (!allowZero && p == 0) {
			return fmt.Errorf("invalid %s port", label)
		}
		return nil
	}
	if err := checkAddress("agent.listen", c.Agent.Listen, !c.Redis.Enabled); err != nil {
		return err
	}
	if c.Agent.Controller != "" {
		if err := checkAddress("agent.controller", c.Agent.Controller, false); err != nil {
			return err
		}
	}
	if c.Agent.Redis != "" {
		if err := checkAddress("agent.redis", c.Agent.Redis, false); err != nil {
			return err
		}
	}
	if c.Agent.StateDir == "" {
		return errors.New("agent.stateDir cannot be empty")
	}
	if len(c.Agent.Token) < 16 {
		return errors.New("Agent token must contain at least 16 bytes (SDN_TOKEN or agent.token)")
	}
	if (c.Agent.Cert == "") != (c.Agent.Key == "") {
		return errors.New("agent.cert and agent.key must be supplied together")
	}
	if c.Agent.Cert == "" && !c.Agent.Insecure {
		return errors.New("TLS certificate/key required; enable agent.insecure or -insecure explicitly for an isolated lab")
	}
	if c.Agent.Cert != "" {
		if _, err := tls.LoadX509KeyPair(c.Agent.Cert, c.Agent.Key); err != nil {
			return errors.New("cannot load Agent TLS certificate/key")
		}
	}
	if c.Agent.Controller != "" && c.Agent.CA == "" && !c.Agent.Insecure {
		return errors.New("controller CA required unless insecure mode is enabled")
	}
	if c.Agent.CA != "" {
		data, err := os.ReadFile(c.Agent.CA)
		if err != nil || !x509.NewCertPool().AppendCertsFromPEM(data) {
			return errors.New("cannot load controller CA certificates")
		}
	}
	if c.Redis.Enabled {
		bind := net.ParseIP(c.Redis.Bind)
		if bind == nil || !bind.IsLoopback() {
			return errors.New("bundled Redis must bind to a loopback IP")
		}
		if c.Redis.Port < 1 || c.Redis.Port > 65535 {
			return errors.New("redis.port must be between 1 and 65535")
		}
		_, listenPort, _ := net.SplitHostPort(c.Agent.Listen)
		if port, _ := strconv.Atoi(listenPort); port == c.Redis.Port {
			return errors.New("Agent and bundled Redis must use different ports")
		}
		if !filepath.IsAbs(c.Redis.DataDir) || filepath.Clean(c.Redis.DataDir) == "/" || strings.ContainsAny(c.Redis.DataDir, "\x00\r\n") {
			return errors.New("redis.dataDir must be an absolute non-root directory")
		}
		dataDir := filepath.Clean(c.Redis.DataDir)
		within := func(child, parent string) bool {
			return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
		}
		for _, reserved := range []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/proc", "/sys", "/dev", "/run", "/boot", "/var/lib/docker", "/var/lib/frr"} {
			if within(dataDir, reserved) {
				return errors.New("redis.dataDir cannot use a system directory")
			}
		}
		for _, reserved := range []string{"/var", "/var/lib", "/var/log", "/opt", "/home", "/root", "/tmp"} {
			if dataDir == reserved {
				return errors.New("redis.dataDir must be a dedicated data directory")
			}
		}
		stateDir, err := filepath.Abs(c.Agent.StateDir)
		if err != nil || within(dataDir, stateDir) || within(stateDir, dataDir) {
			return errors.New("Redis and Agent state directories must not overlap")
		}
		if c.Redis.AppendFsync != "always" && c.Redis.AppendFsync != "everysec" && c.Redis.AppendFsync != "no" {
			return errors.New("unsupported redis.appendFsync policy")
		}
		if c.Redis.StartupTimeoutSeconds < 1 || c.Redis.StartupTimeoutSeconds > 300 || c.Redis.ShutdownTimeoutSeconds < 1 || c.Redis.ShutdownTimeoutSeconds > 300 {
			return errors.New("Redis startup/shutdown timeout must be between 1 and 300 seconds")
		}
		host, port, _ := net.SplitHostPort(c.Agent.Redis)
		ip := net.ParseIP(host)
		if ip == nil || !ip.Equal(bind) || port != strconv.Itoa(c.Redis.Port) {
			return errors.New("agent.redis must match the bundled Redis bind and port")
		}
	}
	return nil
}

func (c Config) Redacted() Config {
	c.Agent.Token = ""
	return c
}
