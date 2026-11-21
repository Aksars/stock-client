package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testKey = ed25519.NewKeyFromSeed(make([]byte, 32))

func useTestKey(t *testing.T) {
	old := TrustedKeys
	TrustedKeys = map[string]string{"test": base64.StdEncoding.EncodeToString(testKey.Public().(ed25519.PublicKey))}
	t.Cleanup(func() { TrustedKeys = old })
}

// signedRaw builds a server answer the way client_publish.signed does: body + signature over the exact payload bytes.
func signedRaw(d *Decision, k ed25519.PrivateKey) []byte {
	payload, _ := json.Marshal(d)
	var body map[string]any
	json.Unmarshal(payload, &body)
	body["signature"] = map[string]string{"alg": "ed25519", "key_id": "test",
		"payload": base64.StdEncoding.EncodeToString(payload), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(k, payload))}
	b, _ := json.Marshal(body)
	return b
}

func TestSignatureRequiredAndChecked(t *testing.T) {
	useTestKey(t)
	if d, err := parseDecision(signedRaw(dec(), testKey)); err != nil || d.DecisionID != "abc" {
		t.Fatal(err)
	}
	unsigned, _ := json.Marshal(dec())
	if _, err := parseDecision(unsigned); err == nil {
		t.Fatal("unsigned decision accepted")
	}
	other := ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32)))
	if _, err := parseDecision(signedRaw(dec(), other)); err == nil {
		t.Fatal("decision signed by another key accepted")
	}
	// tampering with the visible fields does not matter: only the signed payload is used
	var m map[string]any
	json.Unmarshal(signedRaw(dec(), testKey), &m)
	m["weights"] = map[string]float64{"X5": 0.5, "TPAY": 0.5}
	b, _ := json.Marshal(m)
	if d, err := parseDecision(b); err != nil || d.Weights["LKOH"] != 0.30 {
		t.Fatal("unsigned fields were used", err)
	}
	// tampering with the payload breaks the signature
	sg := m["signature"].(map[string]any)
	raw, _ := base64.StdEncoding.DecodeString(sg["payload"].(string))
	sg["payload"] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), "0.45", "0.49", 1)))
	b, _ = json.Marshal(m)
	if _, err := parseDecision(b); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestBuiltInKeyIsValid(t *testing.T) {
	for id, k := range TrustedKeys {
		if b, err := base64.StdEncoding.DecodeString(k); err != nil || len(b) != ed25519.PublicKeySize {
			t.Fatal("bad built-in key", id)
		}
	}
}

func TestLoginOverHTTPOnlyLocally(t *testing.T) {
	for _, u := range []string{"https://strategy.example.com", "http://127.0.0.1:8001", "http://172.17.0.4:8001", "http://192.168.1.5"} {
		if loginAllowed(u) != nil {
			t.Fatal("refused", u)
		}
	}
	if loginAllowed("http://strategy.example.com") == nil || loginAllowed("http://8.8.8.8") == nil {
		t.Fatal("password over public http allowed")
	}
}

func TestLoginSavesDeviceKeyAndLogoutRemovesIt(t *testing.T) {
	homeDir = t.TempDir()
	key := strings.Repeat("d", 43)
	var gotPassword, revoked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/login":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			gotPassword = b["password"]
			if b["password"] != "right password" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"key": key, "device_id": "d1", "login": b["login"]})
		case "/api/v1/logout":
			revoked = r.Header.Get("Authorization")
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	saveJSON("config.json", Config{Mode: "off", ServerURL: srv.URL})
	mux := routes()
	if w := req(mux, "POST", "/api/login", `{"login":"ann","password":"wrong","device":"pc"}`, app); w.Code != 401 || hasServerKey() {
		t.Fatal("wrong password accepted")
	}
	w := req(mux, "POST", "/api/login", `{"login":"ann","password":"right password","device":"pc"}`, app)
	if w.Code != 200 || readSecret("server-api-key.txt") != key || strings.Contains(w.Body.String(), key) || gotPassword != "right password" {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(readAll(t), "right password") {
		t.Fatal("password stored")
	}
	if w := req(mux, "POST", "/api/logout", `{}`, app); w.Code != 200 || hasServerKey() || revoked != "Bearer "+key {
		t.Fatal("logout")
	}
}

func hasServerKey() bool { return readSecret("server-api-key.txt") != "" }
