package ingest

// Provenance registra POR QUE um campo normalizado ficou como ficou.
//
// Exemplo, para um filme cujo título veio do atributo tvg-name de um #EXTINF:
//
//	title.normalized = "interestelar"
//	title.source     = "m3u:tvg-name"
//	title.rule       = "title-cleanup-v1"
//
// Sem isso, meses depois ninguém consegue explicar por que dois conteúdos foram
// agrupados — ou por que não foram. A procedência é o que torna a normalização
// auditável e o que permite reprocessar sem re-sincronizar as fontes.
type Provenance struct {
	// Source é o campo de origem do dado, no formato "<provider>:<campo>".
	// Use SourceDerived quando o valor não veio de nenhum campo.
	Source string `json:"source"`
	// Rule é o identificador versionado da regra aplicada.
	Rule string `json:"rule"`
}

// Fontes de dado reconhecidas. O formato é "<provider>:<campo>" para que o valor
// continue legível quando aparecer num payload de API ou num log.
const (
	SourceDerived = "derived"
	SourceNenhum  = "none"

	SourceM3UTvgName     = "m3u:tvg-name"
	SourceM3UDisplayName = "m3u:display-name"
	SourceM3UGroupTitle  = "m3u:group-title"
	SourceM3UTvgID       = "m3u:tvg-id"
	SourceM3UURL         = "m3u:url"

	SourceXtreamName         = "xtream:name"
	SourceXtreamStreamID     = "xtream:stream_id"
	SourceXtreamSeriesID     = "xtream:series_id"
	SourceXtreamEpisodeID    = "xtream:episode.id"
	SourceXtreamSeason       = "xtream:season"
	SourceXtreamEpisodeNum   = "xtream:episode_num"
	SourceXtreamCategoryID   = "xtream:category_id"
	SourceXtreamCategoryName = "xtream:category_name"
	SourceXtreamContainerExt = "xtream:container_extension"
	SourceXtreamReleaseDate  = "xtream:releaseDate"
	SourceXtreamEpisodeTitle = "xtream:episode.title"
)

// Regras de normalização, versionadas.
//
// Mudar o COMPORTAMENTO de uma regra exige criar uma versão nova (v2) e manter a
// anterior enquanto houver dados normalizados por ela. Assim, uma linha antiga do banco
// continua explicável mesmo depois de o parser evoluir.
const (
	RuleTitleCleanupV1       = "title-cleanup-v1"
	RuleYearFromFieldV1      = "year-from-field-v1"
	RuleYearFromTitleV1      = "year-from-title-v1"
	RuleYearAusenteV1        = "year-absent-v1"
	RuleSeasonEpisodeFieldV1 = "season-episode-from-field-v1"
	RuleSeasonEpisodeTitleV1 = "season-episode-from-title-v1"
	RuleTagsV1               = "quality-language-tags-v1"
	RuleContainerFromFieldV1 = "container-from-field-v1"
	RuleContainerFromURLV1   = "container-from-url-v1"
	RuleIMDBNormalizeV1      = "imdb-id-normalize-v1"
	RuleTMDBNormalizeV1      = "tmdb-id-normalize-v1"
	RuleVariantKeyExternalV1 = "variant-key-external-id-v1"
	RuleVariantKeyURLHashV1  = "variant-key-url-hash-v1"
	RuleCategoryFromGroupV1  = "category-from-group-title-v1"
	RuleCategoryFromFieldV1  = "category-from-field-v1"
	RuleKindFromFieldV1      = "kind-from-field-v1"
	RuleKindFromTitleV1      = "kind-from-title-v1"
	RuleKindUnresolvedV1     = "kind-unresolved-v1"
)

// derived é o atalho para um valor que o sistema produziu, sem campo de origem.
func derived(rule string) Provenance {
	return Provenance{Source: SourceDerived, Rule: rule}
}

// absent é o atalho para um valor que simplesmente não existe na fonte.
func absent(rule string) Provenance {
	return Provenance{Source: SourceNenhum, Rule: rule}
}
