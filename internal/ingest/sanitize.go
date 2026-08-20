package ingest

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// chavesSensiveis são nomes de campo cujo VALOR nunca é persistido no raw_payload.
//
// Regra da Fase 2 (docs/07 §7.4): a credencial da fonte entra na montagem da requisição
// e em mais lugar nenhum — nem em log, nem em payload, nem em evento, nem em erro.
var chavesSensiveis = map[string]bool{
	"username": true, "user": true, "usuario": true, "login": true,
	"password": true, "pass": true, "senha": true, "pwd": true,
	"token": true, "auth": true, "authorization": true, "api_key": true, "apikey": true,
	"secret": true, "session": true, "sid": true, "custom_sid": true,
	"direct_source":   true, // costuma trazer a URL completa com credencial no path
	"server_protocol": false,
}

// Redacted é o valor que substitui um campo sensível.
const Redacted = "[REMOVIDO]"

// RedactedUser substitui o userinfo de uma URL. Sem colchetes, porque o userinfo é
// percent-encoded ao serializar.
const RedactedUser = "REMOVIDO"

// reURLComCredencial detecta URLs com usuário:senha embutidos.
var reURLComCredencial = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)

// SanitizePayload remove credenciais e URLs de mídia de um payload antes de persistir.
//
// Preserva a ESTRUTURA (as chaves continuam lá, com valor substituído) porque a
// estrutura é justamente o que serve para diagnosticar o comportamento de uma fonte.
func SanitizePayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Payload que não é JSON válido não é persistido: guardá-lo cru arriscaria
		// gravar exatamente aquilo que estamos tentando não gravar.
		return json.RawMessage(`{"_erro":"payload nao e JSON valido"}`)
	}
	limpo := sanitizeValue(v, false)
	out, err := json.Marshal(limpo)
	if err != nil {
		return json.RawMessage(`{"_erro":"falha ao serializar payload sanitizado"}`)
	}
	return out
}

func sanitizeValue(v any, paiSensivel bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			sensivel := chavesSensiveis[strings.ToLower(strings.TrimSpace(k))]
			if sensivel {
				out[k] = Redacted
				continue
			}
			out[k] = sanitizeValue(val, paiSensivel)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeValue(val, paiSensivel)
		}
		return out
	case string:
		return RedactString(t)
	default:
		return v
	}
}

// RedactString remove credenciais de um texto livre (mensagem de erro, log, detalhe de
// rejeição). Toda string que sai da ingestão para log ou banco passa por aqui.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	s = reURLComCredencial.ReplaceAllStringFunc(s, func(m string) string {
		idx := strings.Index(m, "://")
		return m[:idx+3] + RedactedUser + "@"
	})
	if !strings.Contains(s, "://") {
		return s
	}
	// Remove parâmetros de query sensíveis de qualquer URL presente no texto.
	return reURLQualquer.ReplaceAllStringFunc(s, func(m string) string {
		return RedactURL(m)
	})
}

var reURLQualquer = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)

// RedactURL devolve uma forma da URL segura para log: sem credencial no userinfo e sem
// valores de parâmetros sensíveis.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Redacted
	}
	if u.User != nil {
		// Sem colchetes: o userinfo é percent-encoded pela url.URL, e "[REMOVIDO]"
		// viraria "%5BREMOVIDO%5D" — ilegível justamente onde precisa ser óbvio.
		u.User = url.User(RedactedUser)
	}
	if q := u.Query(); len(q) > 0 {
		alterou := false
		for k := range q {
			if chavesSensiveis[strings.ToLower(k)] {
				q.Set(k, Redacted)
				alterou = true
			}
		}
		if alterou {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}
