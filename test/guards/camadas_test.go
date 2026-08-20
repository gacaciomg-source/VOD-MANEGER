package guards

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"vodmanager/internal/ingest"
	"vodmanager/internal/sources/xtream"
	"vodmanager/test/fixtures"
)

// pacotesPuros são os pacotes de parsing e normalização. Por contrato (docs/07 §3), eles
// não fazem I/O de rede, não executam processo externo e não materializam URL de mídia.
var pacotesPuros = []string{
	"internal/ingest",
	"internal/sources/m3u",
	"internal/sources/xtream",
}

// importsProibidos são os pacotes cuja simples presença já quebraria uma garantia.
var importsProibidos = map[string]string{
	"net/http":                  "faria requisição durante o parsing — a sincronização nunca pode abrir URL de mídia",
	"net":                       "abriria conexão durante o parsing",
	"os/exec":                   "executaria processo externo; FFmpeg não pode entrar neste caminho",
	"database/sql":              "parsing e normalização são puros: nenhum acesso a banco",
	"vodmanager/internal/db":    "parsing e normalização são puros: nenhum acesso a banco",
	"vodmanager/internal/store": "parsing e normalização são puros: nenhum acesso a banco",
}

// TestPacotesDeParsingSaoPuros verifica a garantia no nível estrutural, não no
// comportamental.
//
// TestSincronizacaoNaoAbreURLDeMidia prova que HOJE nenhuma requisição é feita. Este aqui
// prova que ninguém CONSEGUE fazer uma sem antes adicionar um import proibido — o que
// falha aqui, na revisão, e não em produção contra a fonte de um cliente.
func TestPacotesDeParsingSaoPuros(t *testing.T) {
	raiz := fixtures.Root(t)

	for _, pkg := range pacotesPuros {
		dir := filepath.Join(raiz, filepath.FromSlash(pkg))
		entradas, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("lendo %s: %v", pkg, err)
		}

		arquivosVerificados := 0
		for _, e := range entradas {
			nome := e.Name()
			if e.IsDir() || !strings.HasSuffix(nome, ".go") || strings.HasSuffix(nome, "_test.go") {
				continue
			}
			caminho := filepath.Join(dir, nome)
			arquivo, err := parser.ParseFile(token.NewFileSet(), caminho, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("analisando %s: %v", caminho, err)
			}
			arquivosVerificados++

			for _, imp := range arquivo.Imports {
				caminhoImport, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				if motivo, proibido := importsProibidos[caminhoImport]; proibido {
					t.Errorf("%s/%s importa %q: %s", pkg, nome, caminhoImport, motivo)
				}
			}
		}
		if arquivosVerificados == 0 {
			t.Errorf("nenhum arquivo verificado em %s — a guarda não estaria protegendo nada", pkg)
		}
	}
}

// A guarda precisa realmente pegar um import proibido; senão é placebo.
func TestListaDeImportsProibidosNaoEstaVazia(t *testing.T) {
	if len(importsProibidos) == 0 {
		t.Fatal("a lista de imports proibidos está vazia")
	}
	for _, obrigatorio := range []string{"net/http", "os/exec"} {
		if _, ok := importsProibidos[obrigatorio]; !ok {
			t.Errorf("%q deveria estar na lista de imports proibidos", obrigatorio)
		}
	}
}

// TestXtreamNuncaMaterializaURL trava a regra reafirmada pelo administrador: a URL de
// mídia é montada apenas na camada de transporte.
//
// Roda o mapeamento e a normalização sobre todos os fixtures de Xtream e exige que
// nenhum item saia com URL preenchida — apenas com o StreamRef, que é a referência a
// resolver depois, sem credencial.
func TestXtreamNuncaMaterializaURL(t *testing.T) {
	normalizador, err := ingest.NewNormalizer()
	if err != nil {
		t.Fatalf("NewNormalizer: %v", err)
	}

	verificar := func(raw ingest.RawItem) error {
		if raw.StreamURL != "" {
			t.Errorf("o mapper Xtream produziu URL para %q: %q", raw.Title, raw.StreamURL)
		}
		if raw.StreamRef == nil {
			t.Errorf("o item %q saiu sem StreamRef — não haveria como resolvê-lo depois", raw.Title)
			return nil
		}
		if strings.Contains(raw.StreamRef.ID, "/") || strings.Contains(raw.StreamRef.ID, ":") {
			t.Errorf("StreamRef.ID parece uma URL, não um id: %q", raw.StreamRef.ID)
		}

		item := normalizador.Normalize(1, raw, ingest.CategoryFilter{})
		if item.Media.OriginURL != "" {
			t.Errorf("a normalização materializou URL para %q: %q", raw.Title, item.Media.OriginURL)
		}
		if item.Media.StreamRef == nil {
			t.Errorf("o item normalizado %q perdeu o StreamRef", raw.Title)
		}
		return nil
	}

	cats, err := xtream.ParseCategories(fixtures.Read(t, "xtream/vod_categories.json"), "movie")
	if err != nil {
		t.Fatalf("ParseCategories: %v", err)
	}
	mapa := map[string]string{}
	for _, c := range cats {
		mapa[c.ID] = c.Name
	}

	if _, err := xtream.ParseVODStreams(fixtures.Read(t, "xtream/vod_streams.json"), mapa, time.Now(), verificar); err != nil {
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
		if _, err := xtream.ParseSeriesInfo(fixtures.Read(t, "xtream/series_info_5501.json"), s, time.Now(), verificar); err != nil {
			t.Fatalf("ParseSeriesInfo: %v", err)
		}
	}
}

// O StreamRef que sai do parser não pode carregar credencial de forma alguma: ele é a
// única coisa que atravessa a fronteira até a camada de transporte.
func TestStreamRefNaoCarregaCredencial(t *testing.T) {
	series, err := xtream.ParseSeriesList(fixtures.Read(t, "xtream/series.json"), nil)
	if err != nil {
		t.Fatalf("ParseSeriesList: %v", err)
	}

	// "http" fica de fora porque `Kind` contém "xtream_..." e um dia poderia conter a
	// palavra legitimamente; "://" cobre o caso que importa, que é ter virado URL.
	suspeitos := []string{"usuario", "senha", "user", "pass", "token", "://"}
	verificar := func(raw ingest.RawItem) error {
		if raw.StreamRef == nil {
			return nil
		}
		campos := strings.ToLower(raw.StreamRef.ID + "|" + raw.StreamRef.Extension + "|" + string(raw.StreamRef.Kind))
		for _, s := range suspeitos {
			if strings.Contains(campos, s) {
				t.Errorf("StreamRef de %q contém %q: %q", raw.Title, s, campos)
			}
		}
		return nil
	}

	if _, err := xtream.ParseVODStreams(fixtures.Read(t, "xtream/vod_streams.json"), nil, time.Now(), verificar); err != nil {
		t.Fatalf("ParseVODStreams: %v", err)
	}
	for _, s := range series {
		if s.ID != "5501" {
			continue
		}
		if _, err := xtream.ParseSeriesInfo(fixtures.Read(t, "xtream/series_info_5501.json"), s, time.Now(), verificar); err != nil {
			t.Fatalf("ParseSeriesInfo: %v", err)
		}
	}
}
