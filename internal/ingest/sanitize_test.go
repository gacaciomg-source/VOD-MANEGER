package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizePayloadRemoveCredenciais(t *testing.T) {
	entrada := json.RawMessage(`{
		"stream_id": "123",
		"name": "Filme",
		"username": "usuario_real",
		"password": "senha_real",
		"custom_sid": "sid_real",
		"direct_source": "http://fonte.exemplo.tld/movie/usuario_real/senha_real/123.mp4",
		"info": {"token": "tok_real", "plot": "sinopse"},
		"lista": [{"api_key": "chave_real"}]
	}`)

	limpo := string(SanitizePayload(entrada))

	proibidos := []string{"usuario_real", "senha_real", "sid_real", "tok_real", "chave_real"}
	for _, p := range proibidos {
		if strings.Contains(limpo, p) {
			t.Errorf("o payload sanitizado ainda contém %q:\n%s", p, limpo)
		}
	}

	// A estrutura é preservada: é ela que serve para diagnosticar a fonte.
	for _, esperado := range []string{"stream_id", "username", "password", "info", "plot", "sinopse"} {
		if !strings.Contains(limpo, esperado) {
			t.Errorf("o payload perdeu a chave %q:\n%s", esperado, limpo)
		}
	}
	if !strings.Contains(limpo, Redacted) {
		t.Errorf("nenhum campo foi marcado como removido:\n%s", limpo)
	}
}

func TestSanitizePayloadComEntradaInvalida(t *testing.T) {
	if got := string(SanitizePayload(nil)); got != "{}" {
		t.Errorf("payload nulo = %q, esperava {}", got)
	}
	got := string(SanitizePayload(json.RawMessage(`isto não é json`)))
	if !strings.Contains(got, "_erro") {
		t.Errorf("payload inválido deveria virar marcador de erro, veio %q", got)
	}
	if strings.Contains(got, "isto não é json") {
		t.Error("payload inválido foi persistido cru — é exatamente o que queremos evitar")
	}
}

func TestRedactURL(t *testing.T) {
	tests := map[string]string{
		"http://fonte.exemplo.tld/movie/123.mp4":                     "http://fonte.exemplo.tld/movie/123.mp4",
		"http://usuario:senha@fonte.exemplo.tld/a.mp4":               "http://" + RedactedUser + "@fonte.exemplo.tld/a.mp4",
		"http://fonte.exemplo.tld/api?username=joao&password=abc123": "",
	}
	for entrada, esperado := range tests {
		got := RedactURL(entrada)
		if esperado != "" && got != esperado {
			t.Errorf("RedactURL(%q) = %q, esperava %q", entrada, got, esperado)
		}
		for _, segredo := range []string{"senha", "abc123", "joao"} {
			if strings.Contains(entrada, segredo) && strings.Contains(got, segredo) {
				t.Errorf("RedactURL(%q) vazou %q: %q", entrada, segredo, got)
			}
		}
	}
}

func TestRedactStringEmTextoLivre(t *testing.T) {
	entrada := "falha ao conectar em http://usuario:senha123@fonte.exemplo.tld/movie/1.mp4 após 3 tentativas"
	got := RedactString(entrada)
	if strings.Contains(got, "senha123") {
		t.Errorf("a senha vazou: %q", got)
	}
	if !strings.Contains(got, "3 tentativas") {
		t.Errorf("o texto útil foi destruído: %q", got)
	}
}

func TestRedactStringPreservaTextoSemURL(t *testing.T) {
	const entrada = "temporada e episódio não puderam ser determinados"
	if got := RedactString(entrada); got != entrada {
		t.Errorf("texto sem URL foi alterado: %q", got)
	}
}
