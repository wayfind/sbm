package internal

import (
	"encoding/json"
	"testing"
)

// ── extractStatsUsers ───────────────────────────────────────────────────────

func TestExtractStatsUsersNormal(t *testing.T) {
	config := makeConfig(t, map[string]interface{}{
		"experimental": map[string]interface{}{
			"v2ray_api": map[string]interface{}{
				"listen": "127.0.0.1:8080",
				"stats": map[string]interface{}{
					"enabled": true,
					"users":   []interface{}{"alice", "bob"},
				},
			},
		},
	})
	users := extractStatsUsers(config)
	if len(users) != 2 || users[0] != "alice" || users[1] != "bob" {
		t.Fatalf("got %v", users)
	}
}

func TestExtractStatsUsersNoExperimental(t *testing.T) {
	config := makeConfig(t, map[string]interface{}{})
	users := extractStatsUsers(config)
	if len(users) != 0 {
		t.Fatalf("expected empty, got %v", users)
	}
}

func TestExtractStatsUsersClashAPI(t *testing.T) {
	// Old config with clash_api instead of v2ray_api
	config := makeConfig(t, map[string]interface{}{
		"experimental": map[string]interface{}{
			"clash_api": map[string]interface{}{
				"external_controller": "127.0.0.1:9090",
			},
		},
	})
	users := extractStatsUsers(config)
	if len(users) != 0 {
		t.Fatalf("expected empty, got %v", users)
	}
}

func TestExtractStatsUsersEmpty(t *testing.T) {
	config := makeConfig(t, map[string]interface{}{
		"experimental": map[string]interface{}{
			"v2ray_api": map[string]interface{}{
				"listen": "127.0.0.1:8080",
				"stats": map[string]interface{}{
					"enabled": true,
					"users":   []interface{}{},
				},
			},
		},
	})
	users := extractStatsUsers(config)
	if len(users) != 0 {
		t.Fatalf("expected empty, got %v", users)
	}
}

// ── syncStatsUsers ──────────────────────────────────────────────────────────

func TestSyncStatsUsersCreatesStructure(t *testing.T) {
	// Start with empty config — should create experimental.v2ray_api.stats.users
	config := makeConfig(t, map[string]interface{}{})
	users := []User{
		{Name: "alice"}, {Name: "bob"},
	}
	syncStatsUsers(config, users)

	got := extractStatsUsers(config)
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("got %v", got)
	}
}

func TestSyncStatsUsersUpdatesExisting(t *testing.T) {
	config := makeConfig(t, map[string]interface{}{
		"experimental": map[string]interface{}{
			"v2ray_api": map[string]interface{}{
				"listen": "127.0.0.1:8080",
				"stats": map[string]interface{}{
					"enabled": true,
					"users":   []interface{}{"old-user"},
				},
			},
		},
	})
	syncStatsUsers(config, []User{{Name: "new1"}, {Name: "new2"}})

	got := extractStatsUsers(config)
	if len(got) != 2 || got[0] != "new1" || got[1] != "new2" {
		t.Fatalf("got %v", got)
	}
}

func TestSyncStatsUsersPreservesListen(t *testing.T) {
	config := makeConfig(t, map[string]interface{}{
		"experimental": map[string]interface{}{
			"v2ray_api": map[string]interface{}{
				"listen": "127.0.0.1:9999",
				"stats":  map[string]interface{}{"enabled": true, "users": []interface{}{}},
			},
		},
	})
	syncStatsUsers(config, []User{{Name: "alice"}})

	// Verify listen address is preserved
	exp := config["experimental"].(map[string]interface{})
	api := exp["v2ray_api"].(map[string]interface{})
	if api["listen"] != "127.0.0.1:9999" {
		t.Fatalf("listen changed to %v", api["listen"])
	}
}

func TestSyncStatsUsersEmptyNoOp(t *testing.T) {
	config := makeConfig(t, map[string]interface{}{
		"experimental": map[string]interface{}{
			"v2ray_api": map[string]interface{}{
				"listen": "127.0.0.1:8080",
				"stats": map[string]interface{}{
					"enabled": true,
					"users":   []interface{}{"keep-me"},
				},
			},
		},
	})
	syncStatsUsers(config, nil) // empty users → no-op

	got := extractStatsUsers(config)
	if len(got) != 1 || got[0] != "keep-me" {
		t.Fatalf("empty sync should be no-op, got %v", got)
	}
}

// ── inboundUsersNeedUpdate (stats.users path) ───────────────────────────────

func TestInboundUsersNeedUpdateStatsUsersMismatch(t *testing.T) {
	config := fullEntryConfig([]string{"alice", "bob"}, []string{"alice"}) // inbound has both, stats only alice
	data, _ := json.Marshal(config)

	desired := []User{{Name: "alice"}, {Name: "bob"}}
	if !inboundUsersNeedUpdate(data, desired) {
		t.Fatal("should detect stats.users mismatch")
	}
}

func TestInboundUsersNeedUpdateAllInSync(t *testing.T) {
	config := fullEntryConfig([]string{"alice", "bob"}, []string{"alice", "bob"})
	data, _ := json.Marshal(config)

	desired := []User{{Name: "alice"}, {Name: "bob"}}
	if inboundUsersNeedUpdate(data, desired) {
		t.Fatal("should not need update when in sync")
	}
}

func TestInboundUsersNeedUpdateStatsUsersDuplicate(t *testing.T) {
	// stats.users has duplicate "alice" instead of "bob" — the set-based check catches this
	config := fullEntryConfig([]string{"alice", "bob"}, []string{"alice", "alice"})
	data, _ := json.Marshal(config)

	desired := []User{{Name: "alice"}, {Name: "bob"}}
	if !inboundUsersNeedUpdate(data, desired) {
		t.Fatal("should detect duplicate in stats.users")
	}
}

func TestInboundUsersNeedUpdateEmptyDesired(t *testing.T) {
	config := fullEntryConfig([]string{"alice"}, []string{"alice"})
	data, _ := json.Marshal(config)

	if inboundUsersNeedUpdate(data, nil) {
		t.Fatal("empty desired should return false (preserve existing)")
	}
	if inboundUsersNeedUpdate(data, []User{}) {
		t.Fatal("empty desired should return false (preserve existing)")
	}
}

func TestInboundUsersNeedUpdateExtraUser(t *testing.T) {
	config := fullEntryConfig([]string{"alice"}, []string{"alice"})
	data, _ := json.Marshal(config)

	desired := []User{{Name: "alice"}, {Name: "bob"}}
	if !inboundUsersNeedUpdate(data, desired) {
		t.Fatal("should detect missing user")
	}
}

// ── outboundsByTag: []string vs []interface{} consistency ───────────────────

func TestOutboundsByTagStringVsInterface(t *testing.T) {
	// Code-built path: outbounds as []interface{} (after fix in BuildEntryOutbounds)
	codeBuilt := []interface{}{
		map[string]interface{}{
			"type":      "urltest",
			"tag":       "gia-auto",
			"outbounds": []interface{}{"transit-shuzuan", "transit-metask", "transit-yl"},
		},
		map[string]interface{}{
			"type":      "selector",
			"tag":       "residential",
			"outbounds": []interface{}{"exit-lisa", "exit-bob"},
			"default":   "exit-lisa",
		},
	}

	// JSON-deserialized path: same data but reverse order (simulates reading from file)
	jsonRaw := `[
		{"type":"urltest","tag":"gia-auto","outbounds":["transit-yl","transit-metask","transit-shuzuan"]},
		{"type":"selector","tag":"residential","outbounds":["exit-bob","exit-lisa"],"default":"exit-lisa"}
	]`
	var fromJSON []interface{}
	if err := json.Unmarshal([]byte(jsonRaw), &fromJSON); err != nil {
		t.Fatal(err)
	}

	codeResult := outboundsByTag(codeBuilt)
	jsonResult := outboundsByTag(fromJSON)

	for _, tag := range []string{"gia-auto", "residential"} {
		if codeResult[tag] != jsonResult[tag] {
			t.Errorf("tag %q mismatch:\n  code: %s\n  json: %s", tag, codeResult[tag], jsonResult[tag])
		}
	}
}

// Defense: outboundsByTag still handles []string (from legacy or third-party callers)
func TestOutboundsByTagStringSliceDefense(t *testing.T) {
	withStringSlice := []interface{}{
		map[string]interface{}{
			"type":      "urltest",
			"tag":       "gia-auto",
			"outbounds": []string{"transit-b", "transit-a"},
		},
	}
	withIfaceSlice := []interface{}{
		map[string]interface{}{
			"type":      "urltest",
			"tag":       "gia-auto",
			"outbounds": []interface{}{"transit-a", "transit-b"},
		},
	}

	r1 := outboundsByTag(withStringSlice)
	r2 := outboundsByTag(withIfaceSlice)
	if r1["gia-auto"] != r2["gia-auto"] {
		t.Errorf("[]string vs []interface{} mismatch:\n  string: %s\n  iface:  %s", r1["gia-auto"], r2["gia-auto"])
	}
}

// ── Test helpers ────────────────────────────────────────────────────────────

// makeConfig round-trips through JSON to simulate a parsed sing-box config.
func makeConfig(t *testing.T, raw map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// fullEntryConfig builds a minimal entry config with vless-in inbound users and v2ray_api stats.users.
func fullEntryConfig(inboundUsers, statsUsers []string) map[string]interface{} {
	vlessUsers := make([]interface{}, len(inboundUsers))
	hy2Users := make([]interface{}, len(inboundUsers))
	for i, name := range inboundUsers {
		vlessUsers[i] = map[string]interface{}{"name": name, "uuid": "uuid-" + name, "flow": "xtls-rprx-vision"}
		hy2Users[i] = map[string]interface{}{"name": name, "password": "pass-" + name}
	}

	statsUsersIface := make([]interface{}, len(statsUsers))
	for i, name := range statsUsers {
		statsUsersIface[i] = name
	}

	return map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "vless", "tag": "vless-in", "users": vlessUsers},
			map[string]interface{}{"type": "hysteria2", "tag": "hy2-in", "users": hy2Users},
		},
		"experimental": map[string]interface{}{
			"v2ray_api": map[string]interface{}{
				"listen": "127.0.0.1:8080",
				"stats": map[string]interface{}{
					"enabled": true,
					"users":   statsUsersIface,
				},
			},
		},
	}
}
