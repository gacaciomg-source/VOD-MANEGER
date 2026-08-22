// Package sync orquestra a sincronização de catálogo de uma fonte.
//
// Fluxo (docs/03 §7): coleta em streaming → normalização → diff contra o que já existe
// → aplicação. Nada é apagado; itens que somem são marcados, não removidos.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"vodmanager/internal/cryptobox"
	"vodmanager/internal/ingest"
	"vodmanager/internal/sources"
	"vodmanager/internal/store"
)

// loteTouch é de quantos em quantos itens inalterados a marcação de "visto" é
// descarregada no banco. Escrita em lote é o que mantém barata uma sincronização em que
// nada mudou.
const loteTouch = 500

// tamanhoLoteEscrita é quantas inserções ou atualizações de variante acumulamos antes de
// escrever no banco.
//
// Quinhentos é onde o ganho por viagem satura: dobrar melhora pouco, dobra a memória
// segurada e aumenta o que se perde se a sincronização for cancelada no meio de um lote.
var tamanhoLoteEscrita = 500

// LoteEscritaParaTeste troca o tamanho do lote e devolve o anterior.
//
// Existe para o teste de desempenho poder comparar a escrita em lote com a escrita item a
// item: com tamanho 1, o comportamento volta a ser o de antes desta mudança.
func LoteEscritaParaTeste(novo int) int {
	anterior := tamanhoLoteEscrita
	tamanhoLoteEscrita = novo
	return anterior
}

// intervaloProgresso é a frequência máxima de publicação do progresso no banco. Um
// segundo é imperceptível para quem olha o painel e mantém a escrita irrelevante mesmo
// num catálogo enorme.
const intervaloProgresso = time.Second

// maxErrosSeguidos é quantos itens seguidos podem falhar antes de a execução inteira ser
// abortada.
//
// Erros isolados são normais e não devem derrubar a sincronização. Mas dezenas em
// sequência significam que o problema não é o item — é a fonte que foi excluída, o banco
// que caiu, ou algo igualmente estrutural. Sem esse limite, uma execução órfã continuava
// por minutos registrando milhares de erros e segurando a vaga de sincronização.
const maxErrosSeguidos = 25

// Orchestrator executa sincronizações.
type Orchestrator struct {
	store      *store.Store
	crypto     *cryptobox.Box
	normalizer *ingest.Normalizer
	providers  map[string]sources.Provider
	log        *slog.Logger
	nodeID     string

	// emAndamento permite cancelar uma sincronização em curso — necessário quando a
	// fonte é excluída no meio dela.
	mu          sync.Mutex
	emAndamento map[int64]context.CancelFunc
}

// Options configura o orquestrador.
type Options struct {
	Store      *store.Store
	Crypto     *cryptobox.Box
	Normalizer *ingest.Normalizer
	Providers  map[string]sources.Provider
	Log        *slog.Logger
	NodeID     string
}

// New cria o orquestrador.
func New(opts Options) *Orchestrator {
	return &Orchestrator{
		store:       opts.Store,
		crypto:      opts.Crypto,
		normalizer:  opts.Normalizer,
		providers:   opts.Providers,
		log:         opts.Log,
		nodeID:      opts.NodeID,
		emAndamento: map[int64]context.CancelFunc{},
	}
}

// Cancel interrompe a sincronização em andamento de uma fonte, se houver.
//
// Chamado antes de excluir a fonte: sem isso, a execução continua tentando gravar
// variantes de uma fonte que não existe mais, falhando em cada item e ocupando a vaga de
// sincronização por minutos.
func (o *Orchestrator) Cancel(sourceID int64) bool {
	o.mu.Lock()
	cancelar, existe := o.emAndamento[sourceID]
	o.mu.Unlock()
	if !existe {
		return false
	}
	o.log.Info("cancelando sincronização em andamento", "source_id", sourceID)
	cancelar()
	return true
}

// EmAndamento informa quais fontes estão sincronizando neste momento.
func (o *Orchestrator) EmAndamento() []int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := make([]int64, 0, len(o.emAndamento))
	for id := range o.emAndamento {
		ids = append(ids, id)
	}
	return ids
}

func (o *Orchestrator) registrar(sourceID int64, cancelar context.CancelFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.emAndamento[sourceID] = cancelar
}

func (o *Orchestrator) desregistrar(sourceID int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.emAndamento, sourceID)
}

// Report resume o resultado de uma sincronização.
type Report struct {
	RunID              int64  `json:"run_id"`
	SourceID           int64  `json:"source_id"`
	State              string `json:"state"`
	store.SyncCounters `json:"-"`
	Seen               int    `json:"items_seen"`
	New                int    `json:"items_new"`
	Updated            int    `json:"items_updated"`
	Unchanged          int    `json:"items_unchanged"`
	Missing            int    `json:"items_missing"`
	Rejected           int    `json:"items_rejected"`
	Requests           int    `json:"requests_made"`
	Partial            bool   `json:"partial"`
	Duration           string `json:"duration"`
	Error              string `json:"error,omitempty"`
}

// ErrJaEmExecucao indica que já há uma sincronização em andamento para a fonte.
var ErrJaEmExecucao = errors.New("já existe uma sincronização em andamento para esta fonte")

// preparo é tudo que precisa acontecer ANTES de a sincronização começar de fato.
//
// Separado da execução para que a versão assíncrona possa devolver o id da execução ao
// painel imediatamente, e só então trabalhar.
type preparo struct {
	src      *store.Source
	cfg      sources.Config
	provider sources.Provider
	run      *store.SyncRun
}

func (o *Orchestrator) preparar(ctx context.Context, sourceID int64, trigger string) (*preparo, error) {
	src, err := o.store.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("a fonte %q está desabilitada", src.Name)
	}

	provider, ok := o.providers[src.Kind]
	if !ok {
		return nil, fmt.Errorf("nenhum provider registrado para o tipo %q", src.Kind)
	}

	cfg, err := o.buildConfig(ctx, src)
	if err != nil {
		return nil, err
	}

	run, err := o.store.StartSyncRun(ctx, sourceID, o.nodeID, trigger, cfg.RequestBudget)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, ErrJaEmExecucao
		}
		return nil, err
	}
	return &preparo{src: src, cfg: cfg, provider: provider, run: run}, nil
}

// SyncSourceAsync abre a execução e devolve na hora, trabalhando em segundo plano.
//
// É o que o painel usa: um catálogo com milhares de itens levaria minutos, e segurar a
// requisição HTTP durante todo esse tempo estouraria qualquer timeout — além de perder o
// trabalho se o navegador fosse fechado.
func (o *Orchestrator) SyncSourceAsync(sourceID int64, trigger string, aoTerminar func()) (*store.SyncRun, error) {
	// Contexto próprio, desligado da requisição HTTP: a sincronização não pode ser
	// cancelada porque o navegador fechou a conexão que a disparou. Mas ela PRECISA ser
	// cancelável de propósito, se a fonte for excluída no meio.
	ctx, cancelar := context.WithCancel(context.Background())

	p, err := o.preparar(ctx, sourceID, trigger)
	if err != nil {
		cancelar()
		return nil, err
	}
	o.registrar(sourceID, cancelar)

	go func() {
		defer func() {
			o.desregistrar(sourceID)
			cancelar()
			if aoTerminar != nil {
				aoTerminar()
			}
			if r := recover(); r != nil {
				o.log.Error("pânico durante a sincronização", "source_id", sourceID, "erro", r)
				_ = o.store.FinishSyncRun(context.Background(), p.run.ID, "failed",
					store.SyncCounters{}, "erro interno durante a sincronização")
			}
		}()
		if _, err := o.executarPreparado(ctx, p); err != nil {
			o.log.Error("sincronização falhou", "source_id", sourceID, "erro", err)
		}
	}()

	return p.run, nil
}

// SyncSource executa a sincronização completa de uma fonte e só retorna ao terminar.
func (o *Orchestrator) SyncSource(ctx context.Context, sourceID int64, trigger string) (*Report, error) {
	p, err := o.preparar(ctx, sourceID, trigger)
	if err != nil {
		return nil, err
	}
	return o.executarPreparado(ctx, p)
}

func (o *Orchestrator) executarPreparado(ctx context.Context, p *preparo) (*Report, error) {
	src, cfg, provider, run := p.src, p.cfg, p.provider, p.run
	sourceID := src.ID
	trigger := run.Trigger

	inicio := time.Now()
	o.log.Info("sincronização iniciada",
		"run_id", run.ID, "source_id", sourceID, "source", src.Name, "trigger", trigger)

	rel, syncErr := o.executar(ctx, src, cfg, provider, run.ID, inicio)
	rel.RunID = run.ID
	rel.SourceID = sourceID
	rel.Duration = time.Since(inicio).Round(time.Millisecond).String()

	estado := "succeeded"
	msgErro := ""
	switch {
	case syncErr != nil:
		estado = "failed"
		// A mensagem passa pelo redator: erros de rede repetem a URL, que pode conter
		// credencial na query.
		msgErro = ingest.RedactString(syncErr.Error())
	case rel.Partial:
		estado = "partial"
		msgErro = "teto de requisições atingido: o catálogo veio incompleto"
	}
	// Cancelamento é um desfecho, não uma falha do sistema.
	if errors.Is(syncErr, context.Canceled) || (ctx.Err() != nil && syncErr != nil) {
		estado = "canceled"
		msgErro = "sincronização cancelada (a fonte foi excluída ou o processo encerrado)"
	}
	rel.State = estado
	rel.Error = msgErro

	contadores := store.SyncCounters{
		Seen: rel.Seen, New: rel.New, Updated: rel.Updated, Unchanged: rel.Unchanged,
		Missing: rel.Missing, Rejected: rel.Rejected, Requests: rel.Requests,
		Stats: map[string]any{"partial": rel.Partial, "duration": rel.Duration},
	}

	// A finalização usa um contexto DESLIGADO do cancelamento: se a execução foi
	// cancelada, é justamente aí que fechar a linha importa — deixá-la em 'running'
	// bloquearia a fonte pelo índice de execução única.
	fechamento := context.WithoutCancel(ctx)
	if err := o.store.FinishSyncRun(fechamento, run.ID, estado, contadores, msgErro); err != nil {
		o.log.Error("não foi possível fechar a execução", "run_id", run.ID, "erro", err)
	}

	o.registrarEvento(fechamento, sourceID, estado, rel)
	o.log.Info("sincronização concluída",
		"run_id", run.ID, "source", src.Name, "estado", estado,
		"vistos", rel.Seen, "novos", rel.New, "atualizados", rel.Updated,
		"inalterados", rel.Unchanged, "ausentes", rel.Missing, "rejeitados", rel.Rejected,
		"requisicoes", rel.Requests, "duracao", rel.Duration)

	if syncErr != nil {
		return rel, syncErr
	}
	return rel, nil
}

// indices são os mapas carregados uma vez por execução, que substituem as consultas
// item a item ao banco.
type indices struct {
	variantes  store.VariantIndex
	conteudos  *store.ContentIndex
	series     *store.SeriesIndex
	categorias store.CategoryIndex
	// principais são as pastas que o administrador escolheu manter; vinculos são as
	// decisões que ele já tomou para ESTA fonte. A sincronização não sai desses dois.
	principais map[string]int64
	vinculos   map[string]int64
	// apelidos são decisões tomadas POR NOME, e não por fonte: valem em qualquer fonte,
	// inclusive nas que ainda não existiam quando a decisão foi tomada. É o que faz uma
	// categoria unida não ressurgir como pendência na sincronização seguinte.
	apelidos map[string]int64
	// pendentes acumula o que apareceu sem destino, para registrar uma vez só no fim em
	// vez de a cada item que cair na mesma categoria.
	pendentes map[string]store.CategoriaPendente
}

// carregarIndices lê de uma vez tudo que a sincronização consultaria item a item.
//
// Três consultas no lugar de centenas de milhares. É a diferença entre ~100 e alguns
// milhares de itens por segundo num catálogo grande, porque o custo do caminho antigo era
// dominado pela latência de ida e volta ao banco, não pelo trabalho em si.
func (o *Orchestrator) carregarIndices(ctx context.Context, sourceID int64) (*indices, error) {
	inicio := time.Now()

	variantes, err := o.store.LoadVariantIndex(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	conteudos, err := o.store.LoadContentIndex(ctx)
	if err != nil {
		return nil, err
	}
	series, err := o.store.LoadSeriesIndex(ctx)
	if err != nil {
		return nil, err
	}
	categorias, err := o.store.LoadCategoryIndex(ctx)
	if err != nil {
		return nil, err
	}

	o.log.Info("índices carregados",
		"source_id", sourceID, "variantes", len(variantes), "categorias", len(categorias),
		"duracao", time.Since(inicio).Round(time.Millisecond).String())

	principais, err := o.store.CategoriasPrincipais(ctx)
	if err != nil {
		return nil, err
	}
	vinculos, err := o.store.VinculosDaFonte(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	apelidos, err := o.store.ApelidosCategoria(ctx)
	if err != nil {
		return nil, err
	}

	return &indices{
		variantes:  variantes,
		conteudos:  conteudos,
		series:     series,
		categorias: categorias,
		principais: principais,
		vinculos:   vinculos,
		apelidos:   apelidos,
		pendentes:  map[string]store.CategoriaPendente{},
	}, nil
}

func (o *Orchestrator) executar(ctx context.Context, src *store.Source, cfg sources.Config, provider sources.Provider, runID int64, inicio time.Time) (*Report, error) {
	rel := &Report{}

	idx, err := o.carregarIndices(ctx, src.ID)
	if err != nil {
		return rel, err
	}

	prev := sources.DecodeState(o.estadoAnterior(ctx, src.ID), provider.Kind())
	filtro := ingest.CategoryFilter{Allowed: src.AllowedCategories, Ignored: src.IgnoredCategories}

	// Escritor em lote: junta centenas de inserções e atualizações numa viagem só ao
	// banco. Antes, cada item novo custava duas viagens sequenciais.
	lote := novoEscritorEmLote(o.store, idx, tamanhoLoteEscrita)

	// Buffer de "vistos sem alteração", descarregado em lote.
	pendentes := make([]int64, 0, loteTouch)
	flush := func() error {
		// O lote de escrita vai primeiro: TouchVariants marca como vistas variantes que
		// podem ter acabado de ser criadas neste mesmo lote.
		if err := lote.Descarregar(ctx); err != nil {
			return err
		}
		if len(pendentes) == 0 {
			return nil
		}
		if err := o.store.TouchVariants(ctx, pendentes); err != nil {
			return err
		}
		pendentes = pendentes[:0]
		return nil
	}

	// Progresso ao vivo para o painel. Publicado no máximo uma vez por intervalo: uma
	// escrita por item transformaria um catálogo de 50 mil itens em 50 mil UPDATEs.
	ultimoProgresso := time.Now()
	publicarProgresso := func(forcar bool) {
		if !forcar && time.Since(ultimoProgresso) < intervaloProgresso {
			return
		}
		ultimoProgresso = time.Now()
		err := o.store.UpdateSyncProgress(ctx, runID, store.SyncCounters{
			Seen: rel.Seen, New: rel.New, Updated: rel.Updated,
			Unchanged: rel.Unchanged, Rejected: rel.Rejected,
		})
		if err != nil {
			o.log.Warn("falha ao publicar progresso", "run_id", runID, "erro", err)
		}
	}

	errosSeguidos := 0
	resultado, fetchErr := provider.FetchCatalog(ctx, cfg, prev, func(raw ingest.RawItem) error {
		// A fonte pode ter sido excluída no meio da execução.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("sincronização cancelada")
		}

		rel.Seen++
		item := o.normalizer.Normalize(src.ID, raw, filtro)

		if item.IsUnresolved() {
			rel.Rejected++
			return o.registrarNaoResolvido(ctx, src.ID, raw, item)
		}

		acao, err := o.aplicarItem(ctx, src, item, idx, lote)
		if err != nil {
			// Um item problemático não derruba a sincronização: ele é logado e a run
			// segue. Perder o catálogo todo por causa de uma linha seria pior.
			//
			// Mas uma sequência longa de falhas não é "itens ruins" — é a fonte que foi
			// excluída ou o banco que caiu. Aí insistir só produz milhares de erros
			// idênticos e segura a vaga de sincronização.
			errosSeguidos++
			if errosSeguidos >= maxErrosSeguidos {
				return fmt.Errorf("%d itens seguidos falharam ao ser gravados; "+
					"a fonte ainda existe? último erro: %w", errosSeguidos, err)
			}
			o.log.Warn("item ignorado por erro ao aplicar",
				"source_id", src.ID, "titulo", item.PrimaryTitle(), "erro", err)
			rel.Rejected++
			return nil
		}
		errosSeguidos = 0

		switch acao.tipo {
		case acaoNova:
			rel.New++
		case acaoAtualizada:
			rel.Updated++
		case acaoInalterada:
			rel.Unchanged++
			// Zero é o caso da entrada repetida dentro do lote atual: ela ainda não tem
			// id, e não há o que marcar como vista.
			if acao.variantID != 0 {
				pendentes = append(pendentes, acao.variantID)
			}
			if len(pendentes) >= loteTouch {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		publicarProgresso(false)
		return nil
	})

	if err := flush(); err != nil {
		return rel, err
	}
	// As categorias vistas são gravadas uma vez, no fim: uma escrita por categoria
	// distinta, e não por item que caiu nela.
	if err := o.gravarCategoriasVistas(ctx, src.ID, idx); err != nil {
		o.log.Warn("falha ao registrar categorias da fonte", "source_id", src.ID, "erro", err)
	}

	rel.Requests = resultado.Requests
	rel.Partial = resultado.Partial
	publicarProgresso(true)

	if fetchErr != nil {
		return rel, fetchErr
	}

	// Categorias declaradas pela fonte.
	if err := o.aplicarCategorias(ctx, src.ID, resultado.Categories, idx); err != nil {
		o.log.Warn("falha ao registrar categorias", "source_id", src.ID, "erro", err)
	}

	// Ausências: só depois de uma coleta COMPLETA. Marcar ausência a partir de um
	// catálogo parcial marcaria como sumido tudo o que não deu tempo de buscar.
	if !rel.Partial {
		ausentes, err := o.store.MarkMissingVariants(ctx, src.ID, inicio, src.MissingTolerance)
		if err != nil {
			return rel, err
		}
		rel.Missing = int(ausentes.Marked)
		if ausentes.Unavailable > 0 {
			o.log.Info("variantes marcadas como indisponíveis",
				"source_id", src.ID, "quantidade", ausentes.Unavailable)
		}
	} else {
		o.log.Warn("coleta parcial: a verificação de ausências foi pulada",
			"source_id", src.ID, "run_id", runID)
	}

	// Estado para o próximo incremental.
	if raw, err := resultado.State.Encode(); err == nil {
		if err := o.store.SaveSyncState(ctx, src.ID, raw, !rel.Partial); err != nil {
			o.log.Warn("falha ao gravar estado de sincronização", "source_id", src.ID, "erro", err)
		}
	}
	return rel, nil
}

// --- Aplicação de um item ----------------------------------------------------

type tipoAcao int

const (
	acaoNova tipoAcao = iota
	acaoAtualizada
	acaoInalterada
)

type acaoItem struct {
	tipo      tipoAcao
	variantID int64
}

func (o *Orchestrator) aplicarItem(ctx context.Context, src *store.Source, item ingest.NormalizedItem, idx *indices, lote *escritorEmLote) (acaoItem, error) {
	// Consulta em memória, não no banco: era uma ida ao banco por item.
	existente, achou := idx.variantes.Lookup(item.Variant.ExternalID, item.Variant.URLHash)

	nova := o.montarVariante(src.ID, item)
	chave := store.ChaveURL(nova.URLHash)
	if nova.ExternalID != "" {
		chave = store.ChaveExterna(nova.ExternalID)
	}

	// A mesma entrada repetida dentro do lote atual ainda não está no índice, porque a
	// escrita foi adiada. Sem esta checagem, ela viraria uma variante duplicada.
	if !achou && lote.Reservada(chave) {
		return acaoItem{tipo: acaoInalterada}, nil
	}

	if achou {
		if existente.Digest == item.Digest && item.Digest != "" {
			return acaoItem{tipo: acaoInalterada, variantID: existente.ID}, nil
		}
		nova.TargetKind, nova.TargetID = existente.TargetKind, existente.TargetID
		if err := lote.Atualizar(ctx, store.VarianteParaAtualizar{ID: existente.ID, Variante: nova}); err != nil {
			return acaoItem{}, err
		}
		// O índice precisa refletir o digest novo já: se a mesma entrada aparecer de
		// novo na lista, ela deve contar como inalterada, e não gerar outra escrita.
		existente.Digest = item.Digest
		idx.variantes[chave] = existente
		return acaoItem{tipo: acaoAtualizada, variantID: existente.ID}, nil
	}

	// Item novo: precisa de um alvo. É aqui que o matching acontece.
	alvo, decisao, err := o.resolverAlvo(ctx, item, idx)
	if err != nil {
		return acaoItem{}, err
	}
	nova.TargetKind, nova.TargetID = alvo.kind, alvo.id

	// A variante e a decisão de matching vão juntas para o lote: são duas escritas que
	// antes custavam duas viagens ao banco POR ITEM.
	//
	// O id só existe depois da descarga, então não há variantID para devolver aqui. Quem
	// precisa dele — o índice em memória — é alimentado pelo próprio lote ao gravar.
	err = lote.Criar(ctx, store.VarianteParaCriar{
		Variante: nova,
		Chave:    chave,
		Decisao:  decisaoParaBanco(decisao),
	})
	if err != nil {
		return acaoItem{}, err
	}
	return acaoItem{tipo: acaoNova}, nil
}

func (o *Orchestrator) montarVariante(sourceID int64, item ingest.NormalizedItem) store.NewVariant {
	nova := store.NewVariant{
		SourceID:      sourceID,
		ExternalID:    item.Variant.ExternalID,
		URLHash:       item.Variant.URLHash,
		OriginURL:     item.Media.OriginURL,
		ContainerExt:  item.Media.ContainerExt,
		DeclaredGroup: item.Category.DeclaredName,
		QualityTags:   item.Signals.QualityTags,
		LanguageTags:  item.Signals.LanguageTags,
		Digest:        item.Digest,
		RawPayload:    item.Payload,
	}
	switch {
	case item.Movie != nil:
		nova.DeclaredTitle = item.Movie.Title.Declared
	case item.Episode != nil:
		nova.DeclaredTitle = item.Episode.SeriesTitle.Declared
	}
	if item.Media.StreamRef != nil {
		if raw, err := json.Marshal(item.Media.StreamRef); err == nil {
			nova.StreamRef = raw
		}
	}
	return nova
}

type alvoVariante struct {
	kind string
	id   int64
}

// resolverAlvo decide a qual Content ou Episode a variante pertence.
func (o *Orchestrator) resolverAlvo(ctx context.Context, item ingest.NormalizedItem, idx *indices) (alvoVariante, ingest.MatchResult, error) {
	if item.Episode != nil {
		alvo, err := o.resolverEpisodio(ctx, item, idx)
		// Episódio é identificado por série + temporada + número: é correspondência
		// exata, não pontuação por similaridade.
		return alvo, ingest.MatchResult{
			Score: 100, Decision: ingest.DecisionGrouped,
			Signals: map[string]int{"serie_temporada_episodio": 100}, Notes: []string{},
		}, err
	}
	return o.resolverFilme(ctx, item, idx)
}

func (o *Orchestrator) resolverFilme(ctx context.Context, item ingest.NormalizedItem, idx *indices) (alvoVariante, ingest.MatchResult, error) {
	novo := store.NewContent{
		Type:            store.ContentMovie,
		Title:           item.Movie.Title.Display,
		NormalizedTitle: item.Movie.Title.Normalized,
		LanguageKey:     ingest.LanguageKey(item.Signals.LanguageTags),
		Year:            item.Movie.Year.Value,
		TMDBID:          textoOuNil(item.Signals.TMDBID),
		IMDBID:          textoOuNil(item.Signals.IMDBID),
		PosterURL:       item.Media.PosterURL,
		BackdropURL:     item.Media.BackdropURL,
		Plot:            item.Media.Plot,
		Rating:          item.Media.Rating,
		DurationSeconds: item.Media.DurationSeconds,
	}
	novo.LanguageKey = ingest.LanguageKey(item.Signals.LanguageTags)
	if id, err := o.categoriaDoItem(ctx, item, store.ContentMovie, idx); err == nil && id > 0 {
		novo.CategoryID = &id
	}

	// Índice em memória primeiro. Ele cobre o caso comum — título idêntico na mesma
	// versão de idioma, ou id externo igual — sem tocar no banco.
	candidatos := idx.conteudos.Candidatos(store.ContentMovie,
		item.Movie.Title.Normalized, novo.LanguageKey,
		item.Signals.TMDBID, item.Signals.IMDBID)

	// Só quando o índice não conhece o título é que vale pagar a busca por similaridade,
	// que é a consulta mais cara da sincronização.
	if len(candidatos) == 0 {
		var err error
		candidatos, err = o.store.FindContentCandidates(ctx, store.ContentMovie,
			item.Movie.Title.Normalized, item.Movie.Year.Value,
			novo.TMDBID, novo.IMDBID, 20)
		if err != nil {
			return alvoVariante{}, ingest.MatchResult{}, err
		}
	}

	alvoItem := ingest.CandidateFrom(item)
	melhorIdx, melhor := -1, ingest.MatchResult{Signals: map[string]int{}, Notes: []string{}}
	for i, c := range candidatos {
		r := ingest.Score(alvoItem, candidatoDoBanco(c))
		if melhorIdx == -1 || r.Score > melhor.Score {
			melhorIdx, melhor = i, r
		}
	}

	// ≥ 95: agrupa na existente e completa o que estiver faltando nela.
	if melhorIdx >= 0 && melhor.Decision == ingest.DecisionGrouped {
		id := candidatos[melhorIdx].ID
		if err := o.store.EnrichContent(ctx, id, novo); err != nil {
			o.log.Warn("falha ao enriquecer conteúdo", "content_id", id, "erro", err)
		}
		return alvoVariante{kind: store.TargetContent, id: id}, melhor, nil
	}

	// 80–94 e abaixo: cria conteúdo próprio. A faixa de revisão fica registrada na
	// decisão de matching, para o administrador resolver depois no painel.
	criado, err := o.store.CreateContent(ctx, novo)
	if err != nil {
		return alvoVariante{}, melhor, err
	}
	idx.conteudos.Add(store.ContentCandidate{
		ID: criado.ID, Type: criado.Type, NormalizedTitle: criado.NormalizedTitle,
		LanguageKey: criado.LanguageKey, Year: criado.Year,
		TMDBID: criado.TMDBID, IMDBID: criado.IMDBID,
	})
	if melhorIdx >= 0 && melhor.Decision == ingest.DecisionPendingReview {
		melhor.Notes = append(melhor.Notes,
			fmt.Sprintf("possível duplicata do conteúdo %d", candidatos[melhorIdx].ID))
	}
	return alvoVariante{kind: store.TargetContent, id: criado.ID}, melhor, nil
}

func (o *Orchestrator) resolverEpisodio(ctx context.Context, item ingest.NormalizedItem, idx *indices) (alvoVariante, error) {
	ep := item.Episode

	serieID, achou := idx.series.Serie(ep.SeriesTitle.Normalized)
	if !achou {
		novo := store.NewContent{
			Type:            store.ContentSeries,
			Title:           ep.SeriesTitle.Display,
			NormalizedTitle: ep.SeriesTitle.Normalized,
			Year:            ep.SeriesYear.Value,
			PosterURL:       item.Media.PosterURL,
		}
		if id, err := o.categoriaDoItem(ctx, item, store.ContentSeries, idx); err == nil && id > 0 {
			novo.CategoryID = &id
		}
		criada, err := o.store.CreateContent(ctx, novo)
		if err != nil {
			return alvoVariante{}, err
		}
		serieID = criada.ID
		idx.series.AddSerie(ep.SeriesTitle.Normalized, serieID)
	}

	temporadaID, achou := idx.series.Temporada(serieID, ep.Season)
	if !achou {
		var err error
		temporadaID, err = o.store.EnsureSeason(ctx, serieID, ep.Season)
		if err != nil {
			return alvoVariante{}, err
		}
		idx.series.AddTemporada(serieID, ep.Season, temporadaID)
	}

	episodioID, achou := idx.series.Episodio(temporadaID, ep.Episode)
	if !achou {
		var err error
		episodioID, err = o.store.EnsureEpisode(ctx, temporadaID, ep.Episode,
			ep.EpisodeTitle.Display, item.Media.Plot, item.Media.PosterURL, item.Media.DurationSeconds)
		if err != nil {
			return alvoVariante{}, err
		}
		idx.series.AddEpisodio(temporadaID, ep.Episode, episodioID)
	}
	return alvoVariante{kind: store.TargetEpisode, id: episodioID}, nil
}

// categoriaDoItem decide em que pasta o conteúdo entra.
//
// A sincronização NÃO cria pasta. Antes, cada nome de categoria vindo de uma fonte virava
// uma categoria canônica nova — e por isso "Filmes | Lancamentos" e "LANÇAMENTOS" viravam
// duas pastas que alguém tinha que mesclar depois, a cada fonte nova.
//
// Agora só existem três respostas possíveis:
//
//  1. Há vínculo decidido para esta fonte → usa ele. A decisão vale para sempre.
//  2. Há uma PRINCIPAL com o mesmo nome → vincula sozinho. Nome idêntico é a única
//     equivalência que não erra; as sugestões por semelhança produziam propostas absurdas.
//  3. Nenhuma das duas → vira pendência, e o conteúdo fica sem pasta até o administrador
//     decidir. Continua disponível e reproduzível; só não inventa uma pasta.
func (o *Orchestrator) categoriaDoItem(_ context.Context, item ingest.NormalizedItem, tipo string, idx *indices) (int64, error) {
	nome := item.Category.NormalizedName
	if nome == "" {
		return 0, nil
	}
	chave := store.ChaveCategoria(nome, tipo)

	if id, ok := idx.vinculos[chave]; ok {
		return id, nil
	}
	// Apelido antes de principal: o apelido é uma decisão que alguém tomou, e a
	// equivalência por nome idêntico é um palpite automático. Decisão ganha de palpite.
	if id, ok := idx.apelidos[chave]; ok {
		idx.vinculos[chave] = id
		idx.pendentes[chave] = store.CategoriaPendente{
			Declarado: item.Category.DeclaredName, Normalizado: nome, ContentType: tipo,
			SugestaoID: &id,
		}
		return id, nil
	}
	if id, ok := idx.principais[chave]; ok {
		// Vínculo automático por nome idêntico: registrado abaixo, no fim da execução.
		idx.vinculos[chave] = id
		idx.pendentes[chave] = store.CategoriaPendente{
			Declarado: item.Category.DeclaredName, Normalizado: nome, ContentType: tipo,
			SugestaoID: &id,
		}
		return id, nil
	}

	if _, jaVista := idx.pendentes[chave]; !jaVista {
		idx.pendentes[chave] = store.CategoriaPendente{
			Declarado: item.Category.DeclaredName, Normalizado: nome, ContentType: tipo,
		}
	}
	return 0, nil
}

// gravarCategoriasVistas registra, ao fim da execução, o que apareceu nesta fonte.
//
// Uma escrita por categoria distinta, e não por item: um catálogo de 250 mil episódios
// costuma ter algumas dezenas de categorias.
func (o *Orchestrator) gravarCategoriasVistas(ctx context.Context, sourceID int64, idx *indices) error {
	for _, p := range idx.pendentes {
		if p.SugestaoID != nil {
			// Casou por nome com uma principal: grava já vinculada, para não voltar a
			// aparecer como pendência.
			if err := o.store.UpsertSourceCategory(ctx, sourceID, "", p.Declarado,
				p.Normalizado, p.ContentType, *p.SugestaoID); err != nil {
				return err
			}
			continue
		}
		if err := o.store.RegistrarPendencia(ctx, sourceID, "", p.Declarado,
			p.Normalizado, p.ContentType); err != nil {
			return err
		}
	}
	return nil
}

// aplicarCategorias registra as categorias que a FONTE declara.
//
// Duas mudanças em relação ao que era feito antes:
//
//  1. Não cria categoria canônica. Criar uma pasta por nome declarado era o que produzia
//     pastas duplicadas a cada fonte nova.
//  2. Não registra nada com tipo "unknown". Quando a fonte não diz se a categoria é de
//     filme ou de série — o caso do M3U, que só tem group-title —, quem registra é o
//     caminho por item, que já conhece o tipo verdadeiro. Registrar aqui também criaria
//     duas pendências para a mesma categoria: uma "unknown" e outra com o tipo certo.
func (o *Orchestrator) aplicarCategorias(ctx context.Context, sourceID int64, cats []sources.Category, idx *indices) error {
	for _, c := range cats {
		nome := strings.TrimSpace(c.Name)
		if nome == "" {
			continue
		}
		tipo := c.ContentType
		if tipo != store.ContentMovie && tipo != store.ContentSeries {
			continue
		}

		normalizado := ingest.NormalizeName(nome)
		chave := store.ChaveCategoria(normalizado, tipo)

		// Já decidida antes: nada a fazer, e não volta a aparecer como pendência.
		if _, ok := idx.vinculos[chave]; ok {
			continue
		}
		// Apelido: decisão tomada por nome, em qualquer fonte. Vem antes da equivalência
		// automática por nome idêntico, que é palpite.
		if id, ok := idx.apelidos[chave]; ok {
			idx.vinculos[chave] = id
			if err := o.store.UpsertSourceCategory(ctx, sourceID, c.ExternalID, nome,
				normalizado, tipo, id); err != nil {
				return err
			}
			continue
		}
		// Nome idêntico ao de uma principal: vincula sozinho.
		if id, ok := idx.principais[chave]; ok {
			idx.vinculos[chave] = id
			if err := o.store.UpsertSourceCategory(ctx, sourceID, c.ExternalID, nome,
				normalizado, tipo, id); err != nil {
				return err
			}
			continue
		}
		if err := o.store.RegistrarPendencia(ctx, sourceID, c.ExternalID, nome,
			normalizado, tipo); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) registrarNaoResolvido(ctx context.Context, sourceID int64, raw ingest.RawItem, item ingest.NormalizedItem) error {
	chave := item.Variant.String()
	if chave == "url:" || chave == "id:" || chave == "" {
		// Sem identidade utilizável, usamos o título para não criar uma linha nova a
		// cada sincronização do mesmo item defeituoso.
		chave = "titulo:" + ingest.NormalizeName(raw.Title)
	}
	motivo, detalhe := "desconhecido", ""
	if item.Rejection != nil {
		motivo, detalhe = string(item.Rejection.Reason), item.Rejection.Detail
	}
	return o.store.UpsertUnresolved(ctx, sourceID, chave,
		raw.Title, raw.GroupTitle, motivo, detalhe, item.Payload)
}

func (o *Orchestrator) registrarEvento(ctx context.Context, sourceID int64, estado string, rel *Report) {
	nivel := "info"
	if estado == "failed" {
		nivel = "error"
	} else if estado == "partial" {
		nivel = "warn"
	}
	err := o.store.InsertEvent(ctx, store.NewEvent{
		NodeID: o.nodeID, Level: nivel, Category: "sync", SourceID: &sourceID,
		Message: fmt.Sprintf("sincronização %s: %d vistos, %d novos, %d atualizados, %d rejeitados",
			estado, rel.Seen, rel.New, rel.Updated, rel.Rejected),
		Data: map[string]any{
			"run_id": rel.RunID, "seen": rel.Seen, "new": rel.New, "updated": rel.Updated,
			"unchanged": rel.Unchanged, "missing": rel.Missing, "rejected": rel.Rejected,
			"requests": rel.Requests, "partial": rel.Partial, "duration": rel.Duration,
		},
	})
	if err != nil {
		o.log.Warn("falha ao registrar evento de sincronização", "erro", err)
	}
}

// --- Configuração e credenciais ---------------------------------------------

// buildConfig monta a configuração da fonte, decifrando a credencial.
//
// A credencial existe apenas nesta struct, em memória, durante a execução. Ela nunca é
// logada, persistida em payload, nem devolvida em API.
func (o *Orchestrator) buildConfig(ctx context.Context, src *store.Source) (sources.Config, error) {
	cfg := sources.Config{
		SourceID:      src.ID,
		Kind:          src.Kind,
		BaseURL:       src.BaseURL,
		RequestBudget: src.RequestBudget,
		// Prazo para a fonte RESPONDER e para ficar sem enviar dados — não um teto para
		// a transferência inteira. Baixar uma playlist grande leva minutos e isso é
		// normal; ficar parado por 90s não é.
		Timeout:   90 * time.Second,
		UserAgent: "VODManager/1.0",
	}

	cred, err := o.store.GetSourceCredential(ctx, src.ID)
	if errors.Is(err, store.ErrNotFound) {
		return cfg, nil // fonte pública, sem credencial
	}
	if err != nil {
		return cfg, err
	}

	claro, err := o.crypto.Open(cred.SecretEnc, cryptobox.SourceCredentialAAD(src.ID))
	if err != nil {
		return cfg, fmt.Errorf("não foi possível decifrar a credencial da fonte %q: %w", src.Name, err)
	}
	var segredo struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(claro, &segredo); err != nil {
		return cfg, fmt.Errorf("credencial da fonte %q em formato inesperado", src.Name)
	}
	cfg.Username = cred.Username
	cfg.Password = segredo.Password
	return cfg, nil
}

func (o *Orchestrator) estadoAnterior(ctx context.Context, sourceID int64) []byte {
	raw, err := o.store.GetSyncState(ctx, sourceID)
	if err != nil {
		return nil
	}
	return raw
}

// ResolveStreamURL materializa a URL de uma variante.
//
// Ponte entre o catálogo e a camada de transporte. Chamada apenas quando um cliente pede
// o vídeo — nunca durante a sincronização.
func (o *Orchestrator) ResolveStreamURL(ctx context.Context, variant *store.SourceVariant) (string, error) {
	src, err := o.store.GetSource(ctx, variant.SourceID)
	if err != nil {
		return "", err
	}
	provider, ok := o.providers[src.Kind]
	if !ok {
		return "", fmt.Errorf("nenhum provider registrado para o tipo %q", src.Kind)
	}
	cfg, err := o.buildConfig(ctx, src)
	if err != nil {
		return "", err
	}

	target := sources.StreamTarget{OriginURL: variant.OriginURL, ContainerExt: variant.ContainerExt}
	if len(variant.StreamRef) > 0 {
		var ref ingest.StreamRef
		if err := json.Unmarshal(variant.StreamRef, &ref); err == nil {
			target.StreamRef = &ref
		}
	}
	return provider.ResolveStreamURL(cfg, target)
}

// TestSource verifica se a fonte responde e se as credenciais funcionam.
func (o *Orchestrator) TestSource(ctx context.Context, sourceID int64) error {
	src, err := o.store.GetSource(ctx, sourceID)
	if err != nil {
		return err
	}
	provider, ok := o.providers[src.Kind]
	if !ok {
		return fmt.Errorf("nenhum provider registrado para o tipo %q", src.Kind)
	}
	cfg, err := o.buildConfig(ctx, src)
	if err != nil {
		return err
	}
	cfg.Timeout = 20 * time.Second
	return provider.Probe(ctx, cfg)
}

// --- auxiliares --------------------------------------------------------------

func textoOuNil(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func candidatoDoBanco(c store.ContentCandidate) ingest.MatchCandidate {
	mc := ingest.MatchCandidate{
		Kind:            ingest.ItemKindMovie,
		NormalizedTitle: c.NormalizedTitle,
		LanguageKey:     c.LanguageKey,
		Year:            c.Year,
	}
	if c.Type == store.ContentSeries {
		mc.Kind = ingest.ItemKindEpisode
	}
	if c.TMDBID != nil {
		mc.TMDBID = *c.TMDBID
	}
	if c.IMDBID != nil {
		mc.IMDBID = *c.IMDBID
	}
	return mc
}
