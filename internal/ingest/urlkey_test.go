package ingest

import "testing"

func TestNormalizeURLForKey(t *testing.T) {
	tests := []struct {
		entrada  string
		esperado string
		ok       bool
	}{
		{"http://fonte.exemplo.tld/movie/u/s/123.mp4", "http://fonte.exemplo.tld/movie/u/s/123.mp4", true},
		{"HTTP://FONTE.EXEMPLO.TLD/Movie/123.mp4", "http://fonte.exemplo.tld/Movie/123.mp4", true},
		{"http://fonte.exemplo.tld/a.mp4?token=abc&exp=1", "http://fonte.exemplo.tld/a.mp4", true},
		{"http://fonte.exemplo.tld/a.mp4#frag", "http://fonte.exemplo.tld/a.mp4", true},
		{"http://usuario:senha@fonte.exemplo.tld/a.mp4", "http://fonte.exemplo.tld/a.mp4", true},
		{"/caminho/relativo.mp4", "", false},
		{"ftp://fonte.exemplo.tld/a.mp4", "", false},
		{"", "", false},
		{"não é url", "", false},
	}
	for _, tc := range tests {
		got, ok := NormalizeURLForKey(tc.entrada)
		if ok != tc.ok {
			t.Errorf("NormalizeURLForKey(%q): ok = %v, esperava %v", tc.entrada, ok, tc.ok)
			continue
		}
		if ok && got != tc.esperado {
			t.Errorf("NormalizeURLForKey(%q) = %q, esperava %q", tc.entrada, got, tc.esperado)
		}
	}
}

// A credencial embutida na URL nunca entra no material do hash.
func TestHashURLIgnoraCredencialNoUserinfo(t *testing.T) {
	comCred, ok1 := HashURL("http://usuario:senha@fonte.exemplo.tld/a.mp4")
	semCred, ok2 := HashURL("http://fonte.exemplo.tld/a.mp4")
	if !ok1 || !ok2 {
		t.Fatal("HashURL falhou em URL válida")
	}
	if comCred != semCred {
		t.Error("a credencial no userinfo entrou no hash")
	}
}

func TestHashURLEstavelEDiferenciador(t *testing.T) {
	a, _ := HashURL("http://fonte.exemplo.tld/a.mp4")
	b, _ := HashURL("http://fonte.exemplo.tld/a.mp4")
	c, _ := HashURL("http://fonte.exemplo.tld/b.mp4")

	if a != b {
		t.Error("a mesma URL gerou hashes diferentes")
	}
	if a == c {
		t.Error("URLs diferentes geraram o mesmo hash")
	}
	if _, ok := HashURL("relativo.mp4"); ok {
		t.Error("URL inválida deveria falhar")
	}
}

func TestExtensionFromURL(t *testing.T) {
	tests := map[string]string{
		"http://x.exemplo.tld/a.mp4":           "mp4",
		"http://x.exemplo.tld/a.MKV":           "mkv",
		"http://x.exemplo.tld/a.mp4?token=abc": "mp4",
		"http://x.exemplo.tld/stream/90007":    "",
		"http://x.exemplo.tld/a.extensaolonga": "",
		"não é url":                            "",
	}
	for entrada, esperado := range tests {
		if got := ExtensionFromURL(entrada); got != esperado {
			t.Errorf("ExtensionFromURL(%q) = %q, esperava %q", entrada, got, esperado)
		}
	}
}

func TestClassifyMediaURL(t *testing.T) {
	tests := []struct {
		url    string
		ext    string
		isVOD  bool
		isLive bool
	}{
		{"http://x.exemplo.tld/a.mp4", "mp4", true, false},
		{"http://x.exemplo.tld/a.mkv", "mkv", true, false},
		{"http://x.exemplo.tld/live/1.m3u8", "m3u8", false, true},
		{"http://x.exemplo.tld/live/1.mpd", "mpd", false, true},
		// Sem extensão não afirmamos nada: descartar aqui perderia catálogo legítimo.
		{"http://x.exemplo.tld/stream/90007", "", true, false},
	}
	for _, tc := range tests {
		ext, vod, live := ClassifyMediaURL(tc.url)
		if ext != tc.ext || vod != tc.isVOD || live != tc.isLive {
			t.Errorf("ClassifyMediaURL(%q) = (%q, %v, %v), esperava (%q, %v, %v)",
				tc.url, ext, vod, live, tc.ext, tc.isVOD, tc.isLive)
		}
	}
}

func TestIsKnownVODExtension(t *testing.T) {
	if !IsKnownVODExtension("MP4") {
		t.Error("a comparação deveria ignorar caixa")
	}
	if IsKnownVODExtension("m3u8") {
		t.Error("m3u8 não é contêiner VOD por arquivo")
	}
}
