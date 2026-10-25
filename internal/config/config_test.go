package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fileToken = "configuration-secret-token"
const envToken = "environment-secret-token"

func confFile(t *testing.T, contents string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "agent.conf")
	if err := os.WriteFile(name, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func environment(token string, present bool) func(string) (string, bool) {
	return func(key string) (string, bool) { return token, present && key == "SDN_TOKEN" }
}

func TestLegacyFlagsAndDefaults(t *testing.T) {
	o, err := Parse([]string{"-insecure", "-frr=false", "-redis", "redis.example:6379"}, environment(envToken, true), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.ConfigPath != "" || o.Redis.Enabled || o.Agent.Listen != ":9090" || o.Agent.Endpoint != ":9090" || o.Agent.StateDir != "/var/lib/sdn-agent" || o.Agent.FRR || o.Agent.Redis != "redis.example:6379" || o.Agent.Token != envToken {
		t.Fatalf("legacy options not retained: %+v", o.Redacted())
	}
}

func TestConfigurationPrecedenceAndPresence(t *testing.T) {
	name := confFile(t, `{"agent":{"listen":":19091","endpoint":"leaf.example:19091","controller":"control.example:19090","stateDir":"/var/lib/custom-agent","frr":true,"insecure":true,"token":"`+fileToken+`"},"redis":{"enabled":true,"port":16379}}`)
	o, err := Parse([]string{"-config", name, "-listen", "127.0.0.1:29091", "-frr=false", "-controller=", "-check-config"}, environment(envToken, true), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.Agent.Listen != "127.0.0.1:29091" || o.Agent.FRR || o.Agent.Controller != "" || o.Agent.Endpoint != "leaf.example:19091" || o.Agent.StateDir != "/var/lib/custom-agent" || o.Agent.Token != envToken || o.Agent.Redis != "127.0.0.1:16379" || !o.CheckOnly {
		t.Fatalf("incorrect merged configuration: %+v", o.Redacted())
	}
	o, err = Parse([]string{"-config=" + name}, environment("", false), io.Discard)
	if err != nil || o.Agent.Token != fileToken {
		t.Fatalf("file-only token was not loaded: %v", err)
	}
	encoded, err := json.Marshal(o.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), fileToken) || strings.Contains(string(encoded), `"token"`) {
		t.Fatal("redacted configuration contains credentials")
	}
	if _, err = Parse([]string{"-config", name}, environment("", true), io.Discard); err == nil {
		t.Fatal("an explicitly empty environment token must not fall back to file secret")
	}
}

func TestRejectAmbiguousOrInvalidConfiguration(t *testing.T) {
	cases := map[string]string{
		"unknown field":    `{"agent":{"typo":true}}`,
		"wrong type":       `{"agent":{"frr":"not-a-boolean"}}`,
		"null":             `{"agent":{"insecure":null}}`,
		"duplicate":        `{"agent":{"listen":":9090","listen":":9091"}}`,
		"case duplicate":   `{"agent":{"token":"first-sensitive-token","Token":"second-sensitive-token"}}`,
		"trailing object":  `{"agent":{}} {}`,
		"array root":       `[]`,
		"integer overflow": `{"redis":{"port":99999999999999999999999999}}`,
		"invalid json":     `{"agent":`,
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			name := confFile(t, body)
			_, err := Parse([]string{"-config", name, "-insecure"}, environment(envToken, true), io.Discard)
			if err == nil {
				t.Fatal("configuration accepted")
			}
			if strings.Contains(err.Error(), "sensitive-token") {
				t.Fatal("configuration validation exposed a duplicate token value")
			}
		})
	}
}

func TestBundledRedisSafetyAndMatching(t *testing.T) {
	cases := map[string]string{
		"public bind":        `{"redis":{"enabled":true,"bind":"0.0.0.0"}}`,
		"different endpoint": `{"agent":{"redis":"127.0.0.1:16379"},"redis":{"enabled":true}}`,
		"root directory":     `{"redis":{"enabled":true,"dataDir":"/"}}`,
		"system directory":   `{"redis":{"enabled":true,"dataDir":"/etc/redis"}}`,
		"overlapping state":  `{"redis":{"enabled":true,"dataDir":"/var/lib/sdn-agent/redis"}}`,
		"relative directory": `{"redis":{"enabled":true,"dataDir":"data"}}`,
		"invalid fsync":      `{"redis":{"enabled":true,"appendFsync":"never"}}`,
		"zero timeout":       `{"redis":{"enabled":true,"startupTimeoutSeconds":0}}`,
		"bad port":           `{"redis":{"enabled":true,"port":65536}}`,
		"port collision":     `{"agent":{"listen":":6379"},"redis":{"enabled":true}}`,
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			name := confFile(t, body)
			if _, err := Parse([]string{"-config", name, "-insecure"}, environment(envToken, true), io.Discard); err == nil {
				t.Fatal("unsafe or incoherent Redis configuration accepted")
			}
		})
	}
}

func TestValidateBeforeAnyStateWrite(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "must-not-exist")
	name := confFile(t, `{"agent":{"stateDir":"`+directory+`","cert":"/missing/certificate","key":"/missing/key","token":"`+fileToken+`"}}`)
	_, err := Parse([]string{"-config", name}, environment("", false), io.Discard)
	if err == nil {
		t.Fatal("unreadable certificate accepted")
	}
	if strings.Contains(err.Error(), fileToken) {
		t.Fatal("validation exposed token")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("validation created runtime state")
	}
}

func TestDockerDefaultUsesDurableLocalRedis(t *testing.T) {
	o, err := Parse([]string{"-config", "../../docker/agent.conf"}, environment(envToken, true), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Redis.Enabled || o.Redis.DataDir != "/var/lib/redis" || !o.Redis.AppendOnly || o.Redis.AppendFsync != "always" || o.Agent.Redis != "127.0.0.1:6379" {
		t.Fatalf("unexpected image defaults: %+v", o.Redacted())
	}
}
