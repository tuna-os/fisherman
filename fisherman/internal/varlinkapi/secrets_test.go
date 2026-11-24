package varlinkapi

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	s := Secret("hunter2-TOPSECRET")
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	l.Info("x", "pw", s, "cfg", Encryption{Type: "t", Passphrase: &s}, "user", User{Username: "u", Password: &s},
		"config", Config{Encryption: &Encryption{Passphrase: &s}, User: &User{Password: &s}}, "enroll", EnrollParams{Password: &s, UnlockPassword: &s, RecoveryKey: &s}, "res", EnrollResult{RecoveryKey: &s})
	for _, out := range []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %s %q %#v", s, s, s, s, s), buf.String(),
		fmt.Sprintf("%v", Encryption{Type: "t", Passphrase: &s}),
	} {
		if strings.Contains(out, "TOPSECRET") {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	// the wire is the one place the value goes
	if string(mustJSON(t, s)) != `"hunter2-TOPSECRET"` {
		t.Fatal("secret must marshal to its value")
	}
}

// A full session with secrets in every place a client can put one, at debug
// log level, then a search of everything the daemon logged.
func TestSecretsNeverReachTheLog(t *testing.T) {
	const (
		luksPass = "luks-PASSPHRASE-9f3a"
		userPass = "user-PASSWORD-77c1"
		newPass  = "enrolled-PASS-5d2e"
	)
	for _, scenario := range []string{"no-tpm", "no-tpm,repart-error", "no-tpm,device-lost"} {
		e := newEnv(t, scenario)
		c := e.client(t)
		var out map[string]any
		c.Call(IfaceFisherman+".ConfigureTarget", map[string]any{
			"user": map[string]any{"username": "alice", "password": userPass}, "hostname": "tuna"}, &out)
		c.Call(IfaceFisherman+".Validate", map[string]any{"config": map[string]any{
			"encryption": map[string]any{"type": "luks_passphrase", "passphrase": luksPass},
			"user":       map[string]any{"username": "alice", "password": userPass}}}, &out)
		cfg := goodConfig()
		cfg["encryption"] = map[string]any{"type": "luks_passphrase", "passphrase": luksPass}
		cfg["user"] = map[string]any{"username": "alice", "password": userPass}
		install(t, e.client(t), cfg)

		var key struct{ RecoveryKey string }
		c.Call(IfaceCryptEnroll+".MakeRecoveryKey", nil, &key)
		// enroll against a node that may or may not exist, right and wrong
		for _, node := range []string{"/dev/nvme0n1", "/dev/nope"} {
			c.Call(IfaceCryptEnroll+".Enroll", map[string]any{"node": node, "mechanism": "password", "unlockPassword": luksPass, "password": newPass}, &out)
			c.Call(IfaceCryptEnroll+".Enroll", map[string]any{"node": node, "mechanism": "recovery", "unlockPassword": "wrong", "recoveryKey": key.RecoveryKey}, &out)
		}
		e.raw(t, runCall, 0)
		e.client(t).Call(IfaceCryptEnroll+".Enroll", map[string]any{"node": "/dev/nvme0n1", "mechanism": "recovery", "unlockPassword": "", "recoveryKey": key.RecoveryKey}, &out)

		logs := e.logs.String()
		if len(logs) < 50 {
			t.Fatalf("%s: expected a log to check, got %q", scenario, logs)
		}
		for _, secret := range []string{luksPass, userPass, newPass, key.RecoveryKey} {
			if secret == "" {
				t.Fatal("empty secret in the test")
			}
			if strings.Contains(logs, secret) {
				t.Errorf("%s: secret %q reached the log:\n%s", scenario, secret, logs)
			}
		}
	}
}

// The service never puts request parameters in an error reply either.
func TestErrorRepliesDoNotEchoSecrets(t *testing.T) {
	e := newEnv(t, "")
	frames := e.raw(t, `{"method":"io.systemd.CryptEnroll.Enroll","parameters":{"node":"/dev/nope","mechanism":"password","unlockPassword":"ECHO-ME-1","password":"ECHO-ME-2"}}`, 0)
	if strings.Contains(frames[0], "ECHO-ME") {
		t.Fatal(frames[0])
	}
}
