package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Normalizer traduz RawItem em NormalizedItem.
//
// É totalmente puro: não faz I/O, não abre URL, não consulta banco. Dois RawItem iguais
// produzem dois NormalizedItem idênticos, inclusive no Digest.
type Normalizer struct {
	dict *TagDictionary
	now  func() time.Time
}

// NormalizerOption ajusta o normalizador.
type NormalizerOption func(*Normalizer)

// WithTagDictionary troca o dicionário de tags (o padrão é o embutido).
func WithTagDictionary(d *TagDictionary) NormalizerOption {
	return func(n *Normalizer) {
		if d != nil {
			n.dict = d
		}
	}
}

// WithClock injeta o relógio, para tornar a validação de ano determinística em teste.
func WithClock(now func() time.Time) NormalizerOption {
	return func(n *Normalizer) {
		if now != nil {
			n.now = now
		}
	}
}

// NewNormalizer cria o normalizador com o dicionário embutido.
func NewNormalizer(opts ...NormalizerOption) (*Normalizer, error) {
	dict, err := DefaultTagDictionary()
	if err != nil {
		return nil, err
	}
	n := &Normalizer{dict: dict, now: time.Now}
	for _, o := range opts {
		o(n)
	}
	return n, nil
}

// CategoryFilter aplica as listas allowed/ignored da fonte.
//
// O filtro roda ANTES do diff: item filtrado nunca chega ao banco e é contabilizado no
// relatório da run.
type CategoryFilter struct {
	Allowed []string
	Ignored []string
}

// Permite informa se uma categoria passa pelo filtro. Comparação por forma canônica.
func (f CategoryFilter) Permite(categoria string) bool {
	norm := NormalizeName(categoria)
	for _, ig := range f.Ignored {
		if NormalizeName(ig) == norm {
			return false
		}
	}
	if len(f.Allowed) == 0 {
		return true
	}
	for _, al := range f.Allowed {
		if NormalizeName(al) == norm {
			return true
		}
	}
	return false
}

// Normalize converte um RawItem. Nunca devolve erro: item problemático vira unresolved
// com motivo explícito, porque descartar em silêncio esconde o problema.
func (n *Normalizer) Normalize(sourceID int64, raw RawItem, filter CategoryFilter) NormalizedItem {
	item := NormalizedItem{
		Category: n.categoria(raw),
		Payload:  SanitizePayload(raw.Payload),
		Signals:  n.sinais(raw),
	}
	item.Variant = n.chaveVariante(sourceID, raw)
	item.Media = n.midia(raw)

	if reject := n.rejeicaoBasica(raw, item.Category, filter); reject != nil {
		item.Kind = ItemKindUnresolved
		item.KindProv = derived(RuleKindUnresolvedV1)
		item.Rejection = reject
		item.Digest = digest(item)
		return item
	}

	switch n.decidirTipo(raw) {
	case ItemKindEpisode:
		n.preencherEpisodio(&item, raw)
	case ItemKindMovie:
		n.preencherFilme(&item, raw)
	default:
		item.Kind = ItemKindUnresolved
		item.KindProv = derived(RuleKindUnresolvedV1)
		item.Rejection = &Rejection{
			Reason: RejectTipoIndeterminado,
			Detail: "não foi possível decidir entre filme e episódio com segurança",
		}
	}

	item.Digest = digest(item)
	return item
}

// decidirTipo escolhe entre filme e episódio.
//
// Ordem de confiança, da maior para a menor:
//
//  1. a fonte declara o tipo (API Xtream separa filmes de séries);
//  2. a fonte fornece temporada E episódio em campos próprios;
//  3. a CATEGORIA da fonte diz de que lado o item está;
//  4. indício textual no título;
//  5. na ausência de tudo, filme — o caso majoritário de um catálogo VOD.
//
// O passo 3 é o que impede o erro visto em produção: "Temporada de Caça" e "Star Wars:
// Episódio II" estão na categoria "Filmes | Ação" e "Filmes | Ficção". A categoria sabe o
// que o título ambíguo não diz, e ela vem da própria fonte — não é adivinhação nossa.
func (n *Normalizer) decidirTipo(raw RawItem) ItemKind {
	switch raw.Kind {
	case RawKindEpisode:
		return ItemKindEpisode
	case RawKindMovie:
		return ItemKindMovie
	}
	if raw.SeasonNum != nil && raw.EpisodeNum != nil {
		return ItemKindEpisode
	}

	switch TipoPelaCategoria(raw.GroupTitle) {
	case ItemKindMovie:
		// A categoria diz "Filmes". Só uma numeração COMPLETA no título derruba isso —
		// há listas que colocam episódios avulsos numa pasta de filmes.
		if _, completo := ParseSeasonEpisode(raw.Title); completo {
			return ItemKindEpisode
		}
		return ItemKindMovie
	case ItemKindEpisode:
		return ItemKindEpisode
	}

	if LooksLikeSeries(raw.Title) {
		return ItemKindEpisode
	}
	return ItemKindMovie
}

var (
	reCategoriaFilme = regexp.MustCompile(`(?i)\b(filmes?|movies?|cinema|lancamentos?|lançamentos?)\b`)
	reCategoriaSerie = regexp.MustCompile(`(?i)\b(s[eé]ries?|novelas?|animes?|doramas?|temporadas?|tv\s*shows?)\b`)
)

// TipoPelaCategoria interpreta o nome da categoria da fonte.
//
// Devolve ItemKindUnresolved quando a categoria não diz nada — ou quando diz as duas
// coisas ("Filmes e Séries"), caso em que ela não ajuda a decidir e o título volta a
// mandar.
func TipoPelaCategoria(grupo string) ItemKind {
	grupo = strings.TrimSpace(grupo)
	if grupo == "" {
		return ItemKindUnresolved
	}
	ehFilme := reCategoriaFilme.MatchString(grupo)
	ehSerie := reCategoriaSerie.MatchString(grupo)

	switch {
	case ehFilme && !ehSerie:
		return ItemKindMovie
	case ehSerie && !ehFilme:
		return ItemKindEpisode
	default:
		return ItemKindUnresolved
	}
}

func (n *Normalizer) preencherFilme(item *NormalizedItem, raw RawItem) {
	res := CleanTitle(raw.Title, n.dict, n.now())

	item.Kind = ItemKindMovie
	item.KindProv = n.procedenciaTipo(raw)
	item.Movie = &MovieFields{
		Title: TitleFields{
			Declared:   raw.Title,
			Display:    res.Display,
			Normalized: res.Normalized,
			Prov:       Provenance{Source: n.origemTitulo(raw), Rule: RuleTitleCleanupV1},
		},
		Year: n.ano(raw, res),
	}
	n.mesclarTags(item, res)

	if item.Movie.Title.Normalized == "" {
		item.Kind = ItemKindUnresolved
		item.KindProv = derived(RuleKindUnresolvedV1)
		item.Rejection = &Rejection{
			Reason: RejectSemTitulo,
			Detail: "o título ficou vazio após a normalização",
		}
	}
}

func (n *Normalizer) preencherEpisodio(item *NormalizedItem, raw RawItem) {
	var (
		season, episode int
		numberProv      Provenance
		serieBruta      = raw.Title
		tituloEpisodio  string
	)

	switch {
	case raw.SeasonNum != nil && raw.EpisodeNum != nil:
		// A fonte declarou os números: é o caminho confiável.
		season, episode = *raw.SeasonNum, *raw.EpisodeNum
		numberProv = Provenance{Source: n.origemNumeros(raw), Rule: RuleSeasonEpisodeFieldV1}
		if strings.TrimSpace(raw.SeriesTitle) != "" {
			// A fonte separa nome da série e título do episódio: o melhor caso.
			serieBruta = raw.SeriesTitle
			tituloEpisodio = raw.Title
		} else if m, ok := ParseSeasonEpisode(raw.Title); ok {
			// Os números também estão no título: usamos só para separar série de episódio.
			if m.Before != "" {
				serieBruta = m.Before
			}
			tituloEpisodio = m.After
		}
	default:
		m, ok := ParseSeasonEpisode(raw.Title)
		if !ok && strings.TrimSpace(raw.SeriesTitle) != "" {
			m, ok = ParseSeasonEpisode(raw.SeriesTitle + " " + raw.Title)
		}
		if !ok {
			// Decisão aprovada (docs/07 §4.3): NUNCA vira filme por descarte.
			item.Kind = ItemKindUnresolved
			item.KindProv = derived(RuleKindUnresolvedV1)
			item.Rejection = &Rejection{
				Reason: RejectTemporadaEpisodioAusente,
				Detail: "o item aparenta ser de série, mas temporada e/ou episódio não puderam ser determinados",
			}
			return
		}
		season, episode = m.Season, m.Episode
		numberProv = Provenance{
			Source: n.origemTitulo(raw),
			Rule:   RuleSeasonEpisodeTitleV1 + "#" + m.Pattern,
		}
		if m.Before != "" {
			serieBruta = m.Before
		}
		tituloEpisodio = m.After
	}

	if strings.TrimSpace(serieBruta) == "" {
		serieBruta = primeiroNaoVazio(raw.SeriesTitle, raw.Title)
	}

	serie := CleanTitle(serieBruta, n.dict, n.now())
	if serie.Normalized == "" {
		item.Kind = ItemKindUnresolved
		item.KindProv = derived(RuleKindUnresolvedV1)
		item.Rejection = &Rejection{
			Reason: RejectSemTitulo,
			Detail: "o nome da série ficou vazio após a normalização",
		}
		return
	}

	epi := CleanTitle(tituloEpisodio, n.dict, n.now())
	if strings.TrimSpace(tituloEpisodio) == "" {
		epi = TitleResult{QualityTags: []string{}, LanguageTags: []string{}}
	}

	item.Kind = ItemKindEpisode
	item.KindProv = n.procedenciaTipo(raw)
	item.Episode = &EpisodeFields{
		SeriesTitle: TitleFields{
			Declared:   serieBruta,
			Display:    serie.Display,
			Normalized: serie.Normalized,
			Prov:       Provenance{Source: n.origemTitulo(raw), Rule: RuleTitleCleanupV1},
		},
		SeriesYear: n.anoSerie(raw, serie),
		Season:     season,
		Episode:    episode,
		NumberProv: numberProv,
		EpisodeTitle: TitleFields{
			Declared:   tituloEpisodio,
			Display:    epi.Display,
			Normalized: epi.Normalized,
			Prov:       Provenance{Source: n.origemTituloEpisodio(raw), Rule: RuleTitleCleanupV1},
		},
	}
	n.mesclarTags(item, serie)
	n.mesclarTags(item, epi)
}

// rejeicaoBasica aplica as recusas que independem do tipo do item.
func (n *Normalizer) rejeicaoBasica(raw RawItem, cat CategoryRef, filter CategoryFilter) *Rejection {
	// Um episódio pode legitimamente não ter nome próprio — muitas fontes não nomeiam
	// episódios (docs/07 §4.3, episode_title é Opcional). Nesse caso o nome da série é
	// o identificador utilizável, e rejeitar por "sem título" descartaria catálogo
	// inteiro de séries. Bug encontrado pelo teste de sincronização ponta a ponta.
	if strings.TrimSpace(raw.Title) == "" && strings.TrimSpace(raw.SeriesTitle) == "" {
		return &Rejection{Reason: RejectSemTitulo, Detail: "a fonte não informou título"}
	}
	if !raw.HasMedia() {
		return &Rejection{Reason: RejectSemMidia, Detail: "o item não aponta para nenhuma mídia"}
	}
	if raw.StreamURL != "" {
		if _, ok := NormalizeURLForKey(raw.StreamURL); !ok {
			return &Rejection{
				Reason: RejectURLInvalida,
				Detail: "a URL da mídia não é http(s) absoluta e válida",
			}
		}
		if _, _, aoVivo := ClassifyMediaURL(raw.StreamURL); aoVivo {
			return &Rejection{
				Reason: RejectNaoEVOD,
				Detail: "a URL aponta para streaming contínuo (m3u8/mpd), não para VOD por arquivo",
			}
		}
	}
	if cat.DeclaredName != "" && !filter.Permite(cat.DeclaredName) {
		return &Rejection{
			Reason: RejectCategoriaFiltrada,
			Detail: "categoria " + cat.DeclaredName + " excluída pela configuração da fonte",
		}
	}
	return nil
}

// chaveVariante aplica a decisão D-variante: ExternalID tem precedência sobre URLHash.
func (n *Normalizer) chaveVariante(sourceID int64, raw RawItem) VariantKey {
	if id := strings.TrimSpace(raw.ExternalID); id != "" {
		return VariantKey{
			SourceID:   sourceID,
			ExternalID: id,
			Prov:       Provenance{Source: n.origemExternalID(raw), Rule: RuleVariantKeyExternalV1},
		}
	}
	if hash, ok := HashURL(raw.StreamURL); ok {
		return VariantKey{
			SourceID: sourceID,
			URLHash:  hash,
			Prov:     Provenance{Source: SourceM3UURL, Rule: RuleVariantKeyURLHashV1},
		}
	}
	return VariantKey{SourceID: sourceID, Prov: absent(RuleVariantKeyURLHashV1)}
}

func (n *Normalizer) midia(raw RawItem) MediaFields {
	m := MediaFields{
		OriginURL:       raw.StreamURL,
		StreamRef:       raw.StreamRef,
		PosterURL:       raw.PosterURL,
		BackdropURL:     raw.BackdropURL,
		Plot:            raw.Plot,
		Rating:          raw.Rating,
		DurationSeconds: raw.DurationSeconds,
	}
	switch {
	case strings.TrimSpace(raw.ContainerExt) != "":
		m.ContainerExt = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw.ContainerExt), "."))
		m.ContainerProv = Provenance{Source: n.origemContainer(raw), Rule: RuleContainerFromFieldV1}
	case raw.StreamURL != "":
		if ext := ExtensionFromURL(raw.StreamURL); ext != "" {
			m.ContainerExt = ext
			m.ContainerProv = Provenance{Source: SourceM3UURL, Rule: RuleContainerFromURLV1}
		} else {
			m.ContainerProv = absent(RuleContainerFromURLV1)
		}
	default:
		m.ContainerProv = absent(RuleContainerFromFieldV1)
	}
	return m
}

var (
	reIMDB    = regexp.MustCompile(`(?i)\btt\d{6,10}\b`)
	reDigitos = regexp.MustCompile(`^\d+$`)
)

func (n *Normalizer) sinais(raw RawItem) MatchSignals {
	s := MatchSignals{
		QualityTags:  []string{},
		LanguageTags: []string{},
		TagsProv:     derived(RuleTagsV1),
		TMDBProv:     absent(RuleTMDBNormalizeV1),
		IMDBProv:     absent(RuleIMDBNormalizeV1),
	}
	if id := strings.TrimSpace(raw.TMDBID); id != "" && reDigitos.MatchString(id) {
		s.TMDBID = id
		s.TMDBProv = Provenance{Source: SourceDerived, Rule: RuleTMDBNormalizeV1}
	}
	if id := reIMDB.FindString(raw.IMDBID); id != "" {
		s.IMDBID = strings.ToLower(id)
		s.IMDBProv = Provenance{Source: SourceDerived, Rule: RuleIMDBNormalizeV1}
	}
	return s
}

func (n *Normalizer) mesclarTags(item *NormalizedItem, res TitleResult) {
	item.Signals.QualityTags = dedup(append(item.Signals.QualityTags, res.QualityTags...))
	item.Signals.LanguageTags = dedup(append(item.Signals.LanguageTags, res.LanguageTags...))
}

func (n *Normalizer) categoria(raw RawItem) CategoryRef {
	nome := strings.TrimSpace(raw.GroupTitle)
	cat := CategoryRef{DeclaredName: nome, NormalizedName: NormalizeName(nome), ContentType: "unknown"}
	if len(raw.CategoryIDs) > 0 {
		cat.SourceCategoryID = raw.CategoryIDs[0]
		cat.Prov = Provenance{Source: SourceXtreamCategoryID, Rule: RuleCategoryFromFieldV1}
	} else if nome != "" {
		cat.Prov = Provenance{Source: SourceM3UGroupTitle, Rule: RuleCategoryFromGroupV1}
	} else {
		cat.Prov = absent(RuleCategoryFromGroupV1)
	}
	switch raw.Kind {
	case RawKindMovie:
		cat.ContentType = "movie"
	case RawKindSeries, RawKindEpisode:
		cat.ContentType = "series"
	}
	return cat
}

func (n *Normalizer) ano(raw RawItem, res TitleResult) YearField {
	if raw.Year != nil && *raw.Year >= anoMinimo && *raw.Year <= n.now().Year()+2 {
		v := *raw.Year
		return YearField{Value: &v, Prov: Provenance{Source: n.origemAno(raw), Rule: RuleYearFromFieldV1}}
	}
	if res.Year != nil {
		v := *res.Year
		return YearField{Value: &v, Prov: Provenance{Source: n.origemTitulo(raw), Rule: RuleYearFromTitleV1}}
	}
	// O título escolhido não tinha ano, mas a fonte forneceu um segundo título para o
	// mesmo item — e é comum só um dos dois carregar o ano. Sem esta tentativa, um filme
	// sem ano nunca agrupa entre fontes e o catálogo enche de duplicatas.
	if alt := strings.TrimSpace(raw.TitleAlt); alt != "" {
		if resAlt := CleanTitle(alt, n.dict, n.now()); resAlt.Year != nil {
			v := *resAlt.Year
			return YearField{Value: &v, Prov: Provenance{Source: SourceM3UDisplayName, Rule: RuleYearFromTitleV1}}
		}
	}
	return YearField{Prov: absent(RuleYearAusenteV1)}
}

func (n *Normalizer) anoSerie(raw RawItem, res TitleResult) YearField { return n.ano(raw, res) }

// --- Origem dos campos por provider -----------------------------------------

func (n *Normalizer) origemTitulo(raw RawItem) string {
	if raw.Origin.Provider == "xtream" {
		return SourceXtreamName
	}
	if raw.Attrs != nil {
		if _, ok := raw.Attrs["tvg-name"]; ok {
			return SourceM3UTvgName
		}
	}
	return SourceM3UDisplayName
}

func (n *Normalizer) origemTituloEpisodio(raw RawItem) string {
	if raw.Origin.Provider == "xtream" {
		return SourceXtreamEpisodeTitle
	}
	return n.origemTitulo(raw)
}

func (n *Normalizer) origemNumeros(raw RawItem) string {
	if raw.Origin.Provider == "xtream" {
		return SourceXtreamSeason + "," + SourceXtreamEpisodeNum
	}
	return n.origemTitulo(raw)
}

func (n *Normalizer) origemExternalID(raw RawItem) string {
	switch {
	case raw.Origin.Provider == "xtream" && raw.Kind == RawKindEpisode:
		return SourceXtreamEpisodeID
	case raw.Origin.Provider == "xtream" && raw.Kind == RawKindSeries:
		return SourceXtreamSeriesID
	case raw.Origin.Provider == "xtream":
		return SourceXtreamStreamID
	default:
		return SourceM3UTvgID
	}
}

func (n *Normalizer) origemContainer(raw RawItem) string {
	if raw.Origin.Provider == "xtream" {
		return SourceXtreamContainerExt
	}
	return SourceM3UDisplayName
}

func (n *Normalizer) origemAno(raw RawItem) string {
	if raw.Origin.Provider == "xtream" {
		return SourceXtreamReleaseDate
	}
	return SourceM3UDisplayName
}

func (n *Normalizer) procedenciaTipo(raw RawItem) Provenance {
	if raw.Kind == RawKindMovie || raw.Kind == RawKindEpisode || raw.Kind == RawKindSeries {
		return Provenance{Source: n.origemTitulo(raw), Rule: RuleKindFromFieldV1}
	}
	return Provenance{Source: n.origemTitulo(raw), Rule: RuleKindFromTitleV1}
}

// digest é o hash do conteúdo normalizado. Base do sync incremental: digest igual
// significa "nada mudou", e a sincronização só toca last_seen_at em lote.
//
// A procedência NÃO entra no digest: mudar a versão de uma regra não deve marcar todo o
// catálogo como alterado. O que importa é se o VALOR mudou.
func digest(item NormalizedItem) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00", item.Kind)
	fmt.Fprintf(h, "variant=%s\x00", item.Variant.String())
	if item.Movie != nil {
		fmt.Fprintf(h, "title=%s\x00year=%v\x00", item.Movie.Title.Normalized, deref(item.Movie.Year.Value))
	}
	if item.Episode != nil {
		fmt.Fprintf(h, "serie=%s\x00s=%d\x00e=%d\x00et=%s\x00",
			item.Episode.SeriesTitle.Normalized, item.Episode.Season,
			item.Episode.Episode, item.Episode.EpisodeTitle.Normalized)
	}
	fmt.Fprintf(h, "cat=%s\x00", item.Category.NormalizedName)
	fmt.Fprintf(h, "url=%s\x00ext=%s\x00", item.Media.OriginURL, item.Media.ContainerExt)
	if item.Media.StreamRef != nil {
		fmt.Fprintf(h, "ref=%s/%s/%s\x00", item.Media.StreamRef.Kind, item.Media.StreamRef.ID, item.Media.StreamRef.Extension)
	}
	fmt.Fprintf(h, "tmdb=%s\x00imdb=%s\x00", item.Signals.TMDBID, item.Signals.IMDBID)
	fmt.Fprintf(h, "q=%s\x00l=%s\x00",
		strings.Join(item.Signals.QualityTags, ","), strings.Join(item.Signals.LanguageTags, ","))
	if item.Rejection != nil {
		fmt.Fprintf(h, "rej=%s\x00", item.Rejection.Reason)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func deref(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func primeiroNaoVazio(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// DigestBytes é o hash estável de um payload bruto. Usado pelos providers para decidir
// se vale a pena buscar detalhes de um item — a base do sync incremental (docs/07 §6.1).
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
