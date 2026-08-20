package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"strings"
)

// extensoesVOD são os contêineres aceitos como VOD.
//
// Um item cuja URL não termina em nenhum deles é tratado como possivelmente não-VOD
// (risco R14 da doc 04): playlist de canal ao vivo entrando pelo M3U quebra o modelo.
var extensoesVOD = map[string]bool{
	"mp4": true, "mkv": true, "avi": true, "mov": true, "m4v": true,
	"ts": true, "mpg": true, "mpeg": true, "wmv": true, "flv": true, "webm": true,
}

// extensoesAoVivo são formatos de streaming contínuo: não são VOD por arquivo.
var extensoesAoVivo = map[string]bool{
	"m3u8": true, "mpd": true,
}

// NormalizeURLForKey produz a forma da URL usada para gerar a identidade de fallback
// de uma variante.
//
// Só é usada quando a fonte NÃO fornece um id próprio — decisão aprovada em docs/07 §4.1.
//
// LIMITAÇÃO CONHECIDA, pendente das amostras reais: removemos query string e fragmento
// inteiros, porque são o lugar habitual de tokens voláteis. Tokens embutidos no PATH
// (o padrão `/movie/usuario/senha/123.mp4`) NÃO são tratados aqui: sem ver as URLs reais
// das suas fontes, qualquer recorte de path seria adivinhação, e adivinhar errado
// significa ou inflar o catálogo a cada rotação, ou colidir variantes distintas.
// Ver docs/07 §8, pendência 1.
func NormalizeURLForKey(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil // credencial embutida jamais entra no material do hash
	return u.String(), true
}

// HashURL devolve o sha256 hexadecimal da URL normalizada.
func HashURL(raw string) (string, bool) {
	normalizada, ok := NormalizeURLForKey(raw)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(normalizada))
	return hex.EncodeToString(sum[:]), true
}

// ExtensionFromURL extrai a extensão declarada pela URL, em minúsculas e sem ponto.
func ExtensionFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
	if ext == "" || len(ext) > 5 {
		return ""
	}
	return ext
}

// ClassifyMediaURL informa se a URL aparenta ser VOD por arquivo.
//
// É uma checagem TEXTUAL. Nenhuma requisição é feita — nem HEAD, nem GET, nem DNS.
func ClassifyMediaURL(raw string) (ext string, isVOD bool, isLive bool) {
	ext = ExtensionFromURL(raw)
	switch {
	case extensoesAoVivo[ext]:
		return ext, false, true
	case extensoesVOD[ext]:
		return ext, true, false
	default:
		// Sem extensão reconhecível não afirmamos nada: o item segue como VOD por
		// omissão, e o container fica vazio. Afirmar "é ao vivo" aqui descartaria
		// catálogo legítimo de fontes que servem sem extensão.
		return ext, true, false
	}
}

// IsKnownVODExtension informa se a extensão é um contêiner VOD conhecido.
func IsKnownVODExtension(ext string) bool { return extensoesVOD[strings.ToLower(ext)] }
