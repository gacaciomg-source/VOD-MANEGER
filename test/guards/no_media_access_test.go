// Package guards contém os testes de garantia estrutural da Fase 2.
//
// Eles não testam uma função específica: testam PROPRIEDADES que o sistema inteiro
// precisa manter, e que seriam fáceis de quebrar sem perceber.
package guards

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources/m3u"
	"vodmanager/internal/sources/xtream"
	"vodmanager/test/fixtures"
)

// origemFalsa registra toda requisição que receber. Nenhuma deveria chegar.
type origemFalsa struct {
	mu          sync.Mutex
	requisicoes []string
	server      *httptest.Server
}

func novaOrigemFalsa(t *testing.T) *origemFalsa {
	t.Helper()
	o := &origemFalsa{}
	o.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.requisicoes = append(o.requisicoes, r.Method+" "+r.URL.Path)
		o.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("bytes de video que nunca deveriam ser pedidos"))
	}))
	t.Cleanup(o.server.Close)
	return o
}

func (o *origemFalsa) recebidas() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.requisicoes...)
}

// TestSincronizacaoNaoAbreURLDeMidia é a garantia mais importante da Fase 2.
//
// Monta uma lista M3U cujas URLs apontam para um servidor real e controlado, roda o
// pipeline completo de ingestão (parse + normalização + matching) e exige que o servidor
// NÃO tenha recebido uma única requisição.
//
// Se alguém adicionar um HEAD "só para descobrir o tamanho", um probe de qualidade ou um
// seguimento de redirect no caminho de sincronização, este teste falha.
func TestSincronizacaoNaoAbreURLDeMidia(t *testing.T) {
	origem := novaOrigemFalsa(t)

	lista := strings.Join([]string{
		"#EXTM3U",
		`#EXTINF:-1 tvg-id="1" tvg-name="Interestelar (2014)" tvg-logo="` + origem.server.URL + `/img/1.jpg" group-title="FILMES",Interestelar (2014)`,
		origem.server.URL + "/movie/usuario/senha/1.mp4",
		`#EXTINF:-1 tvg-id="2" tvg-name="Breaking Bad S01E01" group-title="SERIES",Breaking Bad S01E01`,
		origem.server.URL + "/series/usuario/senha/2.mkv",
		`#EXTINF:-1 tvg-id="3" tvg-name="Canal Ao Vivo" group-title="CANAIS",Canal Ao Vivo`,
		origem.server.URL + "/live/usuario/senha/3.m3u8",
		`#EXTINF:-1 tvg-id="4" tvg-name="Serie Sem Numero - Temporada" group-title="SERIES",Serie Sem Numero - Temporada`,
		origem.server.URL + "/series/usuario/senha/4.mp4",
	}, "\n")

	normalizador, err := ingest.NewNormalizer()
	if err != nil {
		t.Fatalf("NewNormalizer: %v", err)
	}

	var normalizados []ingest.NormalizedItem
	if _, err := m3u.Parse(strings.NewReader(lista), m3u.ParseOptions{FetchedAt: time.Now()},
		func(raw ingest.RawItem) error {
			normalizados = append(normalizados, normalizador.Normalize(1, raw, ingest.CategoryFilter{}))
			return nil
		}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Matching entre todos os pares, para exercitar também esse caminho.
	for i := range normalizados {
		for j := range normalizados {
			ingest.Score(ingest.CandidateFrom(normalizados[i]), ingest.CandidateFrom(normalizados[j]))
		}
	}

	if len(normalizados) != 4 {
		t.Fatalf("itens normalizados = %d, esperava 4", len(normalizados))
	}
	if recebidas := origem.recebidas(); len(recebidas) > 0 {
		t.Fatalf("a sincronização fez %d requisição(ões) à fonte — nenhuma é permitida:\n%v",
			len(recebidas), recebidas)
	}
}

// O mesmo, pelo caminho do Xtream: o mapper recebe bytes já obtidos e não pode
// requisitar nada por conta própria.
func TestMapeamentoXtreamNaoFazRequisicao(t *testing.T) {
	origem := novaOrigemFalsa(t)

	normalizador, err := ingest.NewNormalizer()
	if err != nil {
		t.Fatalf("NewNormalizer: %v", err)
	}

	cats, err := xtream.ParseCategories(fixtures.Read(t, "xtream/vod_categories.json"), "movie")
	if err != nil {
		t.Fatalf("ParseCategories: %v", err)
	}
	mapa := map[string]string{}
	for _, c := range cats {
		mapa[c.ID] = c.Name
	}

	if _, err := xtream.ParseVODStreams(fixtures.Read(t, "xtream/vod_streams.json"), mapa, time.Now(),
		func(raw ingest.RawItem) error {
			normalizador.Normalize(1, raw, ingest.CategoryFilter{})
			return nil
		}); err != nil {
		t.Fatalf("ParseVODStreams: %v", err)
	}

	series, err := xtream.ParseSeriesList(fixtures.Read(t, "xtream/series.json"), mapa)
	if err != nil {
		t.Fatalf("ParseSeriesList: %v", err)
	}
	for _, s := range series {
		if s.ID != "5501" {
			continue
		}
		if _, err := xtream.ParseSeriesInfo(fixtures.Read(t, "xtream/series_info_5501.json"), s, time.Now(),
			func(raw ingest.RawItem) error {
				normalizador.Normalize(1, raw, ingest.CategoryFilter{})
				return nil
			}); err != nil {
			t.Fatalf("ParseSeriesInfo: %v", err)
		}
	}

	if recebidas := origem.recebidas(); len(recebidas) > 0 {
		t.Fatalf("o mapeamento Xtream fez requisições: %v", recebidas)
	}
}

// Nenhum item normalizado pode carregar credencial para fora da ingestão: nem no
// payload sanitizado, nem no detalhe de uma rejeição.
func TestItensNormalizadosNaoCarregamCredencial(t *testing.T) {
	const usuario = "usuario_secreto_9931"
	const senha = "senha_secreta_7742"

	lista := strings.Join([]string{
		"#EXTM3U",
		`#EXTINF:-1 tvg-id="1" tvg-name="Filme" group-title="FILMES",Filme`,
		"http://fonte.exemplo.tld/movie/" + usuario + "/" + senha + "/1.mp4",
		`#EXTINF:-1 tvg-id="2" tvg-name="Ao Vivo" group-title="CANAIS",Ao Vivo`,
		"http://fonte.exemplo.tld/live/" + usuario + "/" + senha + "/2.m3u8",
		`#EXTINF:-1 tvg-id="3" tvg-name="" group-title="FILMES",`,
		"http://fonte.exemplo.tld/movie/" + usuario + "/" + senha + "/3.mp4",
	}, "\n")

	normalizador, err := ingest.NewNormalizer()
	if err != nil {
		t.Fatalf("NewNormalizer: %v", err)
	}

	if _, err := m3u.Parse(strings.NewReader(lista), m3u.ParseOptions{}, func(raw ingest.RawItem) error {
		item := normalizador.Normalize(1, raw, ingest.CategoryFilter{})

		if strings.Contains(string(item.Payload), usuario) || strings.Contains(string(item.Payload), senha) {
			t.Errorf("credencial no payload sanitizado: %s", item.Payload)
		}
		if item.Rejection != nil {
			if strings.Contains(item.Rejection.Detail, usuario) || strings.Contains(item.Rejection.Detail, senha) {
				t.Errorf("credencial no detalhe da rejeição: %q", item.Rejection.Detail)
			}
		}
		// A URL bruta vive em Media.OriginURL, que é o único lugar autorizado — e é
		// justamente o campo marcado com `json:"-"`, fora de qualquer serialização.
		if item.Media.OriginURL == "" {
			t.Error("a URL de origem deveria estar preservada no campo dedicado")
		}
		return nil
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}
