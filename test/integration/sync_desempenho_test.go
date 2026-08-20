package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"vodmanager/internal/store"
	vsync "vodmanager/internal/sync"
)

// itensDoTesteDeCarga é quantos itens a lista sintética tem.
//
// Grande o bastante para o custo por viagem ao banco dominar o resultado, pequeno o
// bastante para o teste não virar um obstáculo no dia a dia.
const itensDoTesteDeCarga = 3000

// listaSintetica monta uma M3U com N filmes distintos.
//
// Sintética de propósito: medir contra a fonte real do administrador consumiria a banda
// dele e produziria números que variam com a rede.
func listaSintetica(n int, origem string) string {
	var b strings.Builder
	b.Grow(n * 160)
	b.WriteString("#EXTM3U\n")
	for i := range n {
		fmt.Fprintf(&b,
			"#EXTINF:-1 tvg-name=%q group-title=\"Filmes | Carga\",Filme de Carga %d\n%s/movie/u/s/%d.mp4\n",
			fmt.Sprintf("Filme de Carga %d (%d)", i, 1980+i%40), i, origem, i)
	}
	return b.String()
}

// medirSincronizacao devolve quanto tempo leva sincronizar a lista com um dado tamanho de
// lote de escrita.
func medirSincronizacao(t *testing.T, tamanhoLote, itens int) (time.Duration, int) {
	t.Helper()
	env := newTestEnv(t)
	ctx := context.Background()

	anterior := vsync.LoteEscritaParaTeste(tamanhoLote)
	defer vsync.LoteEscritaParaTeste(anterior)

	fonte := novaFonteM3U(t, listaSintetica(itens, "http://origem.exemplo.tld"))
	src := cadastrarFonte(t, env, fmt.Sprintf("Carga lote %d", tamanhoLote),
		store.SourceKindM3U, fonte.server.URL+"/lista.m3u", false)

	orch := montarOrquestrador(t, env)
	inicio := time.Now()
	rel, err := orch.SyncSource(ctx, src.ID, "manual")
	if err != nil {
		t.Fatalf("SyncSource (lote %d): %v", tamanhoLote, err)
	}
	decorrido := time.Since(inicio)

	if rel.New != itens {
		t.Fatalf("lote %d: criou %d itens, esperava %d", tamanhoLote, rel.New, itens)
	}
	return decorrido, rel.New
}

// TestEscritaEmLoteProduzOMesmoCatalogo é o teste que autoriza a mudança.
//
// A escrita em lote só vale se o resultado for indistinguível do item a item. Aqui os dois
// caminhos rodam sobre a MESMA lista, e os catálogos resultantes são comparados.
func TestEscritaEmLoteProduzOMesmoCatalogo(t *testing.T) {
	const itens = 400

	fotografar := func(tamanhoLote int) (int, int, int) {
		env := newTestEnv(t)
		ctx := context.Background()

		anterior := vsync.LoteEscritaParaTeste(tamanhoLote)
		defer vsync.LoteEscritaParaTeste(anterior)

		fonte := novaFonteM3U(t, listaSintetica(itens, "http://origem.exemplo.tld"))
		src := cadastrarFonte(t, env, "Comparacao", store.SourceKindM3U,
			fonte.server.URL+"/lista.m3u", false)
		orch := montarOrquestrador(t, env)
		if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
			t.Fatalf("SyncSource: %v", err)
		}

		contar := func(sql string) int {
			var n int
			if err := env.Pool.QueryRow(ctx, sql).Scan(&n); err != nil {
				t.Fatalf("contando: %v", err)
			}
			return n
		}
		return contar(`SELECT count(*) FROM contents`),
			contar(`SELECT count(*) FROM source_variants`),
			contar(`SELECT count(*) FROM match_decisions`)
	}

	// Lote 1 reproduz o comportamento anterior: uma escrita por item.
	c1, v1, d1 := fotografar(1)
	c500, v500, d500 := fotografar(500)

	if c1 != c500 || v1 != v500 || d1 != d500 {
		t.Fatalf("os catálogos divergem:\n item a item: %d conteúdos, %d variantes, %d decisões\n em lote:     %d conteúdos, %d variantes, %d decisões",
			c1, v1, d1, c500, v500, d500)
	}
	if v500 != itens {
		t.Errorf("variantes = %d, esperava %d", v500, itens)
	}
	// Toda variante precisa ter decisão registrada: antes desta mudança, algumas falhavam
	// em silêncio porque o erro era apenas logado.
	if d500 != v500 {
		t.Errorf("%d variantes mas %d decisões — alguma decisão não foi gravada", v500, d500)
	}
}

// A mesma entrada repetida na lista não pode virar duas variantes, nem quando as duas
// caem no mesmo lote ainda não gravado.
func TestEntradaRepetidaNoMesmoLoteNaoDuplica(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	anterior := vsync.LoteEscritaParaTeste(500)
	defer vsync.LoteEscritaParaTeste(anterior)

	// A MESMA linha três vezes, dentro de um único lote.
	linha := `#EXTINF:-1 tvg-name="Repetido (2020)" group-title="Filmes",Repetido (2020)` + "\n" +
		"http://origem.exemplo.tld/movie/u/s/1.mp4\n"
	lista := "#EXTM3U\n" + linha + linha + linha

	fonte := novaFonteM3U(t, lista)
	src := cadastrarFonte(t, env, "Repetida", store.SourceKindM3U,
		fonte.server.URL+"/lista.m3u", false)
	orch := montarOrquestrador(t, env)
	if _, err := orch.SyncSource(ctx, src.ID, "manual"); err != nil {
		t.Fatalf("SyncSource: %v", err)
	}

	var variantes int
	if err := env.Pool.QueryRow(ctx, `SELECT count(*) FROM source_variants`).Scan(&variantes); err != nil {
		t.Fatalf("contando: %v", err)
	}
	if variantes != 1 {
		t.Fatalf("variantes = %d, esperava 1 — a entrada repetida duplicou dentro do lote", variantes)
	}
}

// TestDesempenhoDaSincronizacao mede a taxa de itens por segundo na carga inicial.
//
// NÃO compara com o caminho antigo. Forçar o lote a 1 cria uma transação POR ITEM, o que é
// mais lento que o código anterior era — a comparação produziria um ganho inflado e
// desonesto. O número que vale é o da sincronização real, no acervo real.
//
// Rode com:
//
//	VODM_TESTE_DESEMPENHO=1 go test -run TestDesempenho -v ./test/integration/
func TestDesempenhoDaSincronizacao(t *testing.T) {
	if os.Getenv("VODM_TESTE_DESEMPENHO") == "" {
		t.Skip("defina VODM_TESTE_DESEMPENHO=1 para medir; é lento de propósito")
	}

	emLote, n := medirSincronizacao(t, 500, itensDoTesteDeCarga)
	t.Logf("\n  %d itens em %s  (%.0f itens/s)",
		n, emLote.Round(time.Millisecond), float64(n)/emLote.Seconds())
}
