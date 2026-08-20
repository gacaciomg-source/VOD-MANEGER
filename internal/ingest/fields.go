package ingest

// FieldLevel é o nível de garantia de um campo do contrato (docs/07 §2).
type FieldLevel string

const (
	// LevelGuaranteed: o VOD Manager SEMPRE preenche. Se a fonte não fornecer, derivamos.
	LevelGuaranteed FieldLevel = "G"
	// LevelOptional: pode estar vazio. Todo consumidor precisa tratar a ausência.
	LevelOptional FieldLevel = "O"
	// LevelVendor: específico do fornecedor. Vai inteiro para raw_payload.
	//
	// REGRA DURA: nenhum campo de nível V pode virar dependência de lógica de negócio.
	// Se um campo específico se mostrar necessário, ele é promovido a O por mudança
	// EXPLÍCITA em docs/07 — nunca acessado ad-hoc de dentro do jsonb.
	LevelVendor FieldLevel = "V"
)

// FieldSpec descreve um campo do contrato normalizado.
type FieldSpec struct {
	Path  string
	Level FieldLevel
	Note  string
}

// FieldCatalog é a classificação oficial dos campos do NormalizedItem.
//
// Este catálogo é a versão executável da tabela de docs/07 §2, e existe para que a
// classificação não fique só na documentação: há teste que verifica que todo campo
// marcado como G está de fato preenchido em toda saída do normalizador.
//
// Quando as amostras reais chegarem, esta lista é o lugar onde uma promoção V→O fica
// registrada em código — junto com a mudança em docs/07.
func FieldCatalog() []FieldSpec {
	return []FieldSpec{
		{"kind", LevelGuaranteed, "movie | episode | unresolved"},
		{"kind_provenance.source", LevelGuaranteed, "campo de origem da decisão de tipo"},
		{"kind_provenance.rule", LevelGuaranteed, "regra versionada aplicada"},

		{"variant.source_id", LevelGuaranteed, "id da fonte"},
		{"variant.external_id", LevelOptional, "id do item na fonte; precedência sobre url_hash"},
		{"variant.url_hash", LevelOptional, "fallback quando a fonte não tem id próprio"},
		{"variant.provenance", LevelGuaranteed, "qual das duas identidades foi usada"},

		{"movie.title.declared", LevelGuaranteed, "título bruto, nunca sobrescrito"},
		{"movie.title.display", LevelGuaranteed, "título limpo para exibição"},
		{"movie.title.normalized", LevelGuaranteed, "forma canônica para matching"},
		{"movie.title.provenance", LevelGuaranteed, "de qual campo veio e por qual regra"},
		{"movie.year.value", LevelOptional, "ano; pode não existir"},
		{"movie.year.provenance", LevelGuaranteed, "field | title | none"},

		{"episode.series_title.*", LevelGuaranteed, "mesmas três formas do título de filme"},
		{"episode.season", LevelGuaranteed, "sempre conhecido; senão o item é unresolved"},
		{"episode.episode", LevelGuaranteed, "idem"},
		{"episode.number_provenance", LevelGuaranteed, "campo próprio ou padrão textual reconhecido"},
		{"episode.series_year.value", LevelOptional, ""},
		{"episode.episode_title.*", LevelOptional, "muitas fontes não nomeiam episódios"},

		{"category.declared_name", LevelGuaranteed, "vazio quando a fonte não categoriza"},
		{"category.normalized_name", LevelGuaranteed, ""},
		{"category.source_category_id", LevelOptional, ""},
		{"category.content_type", LevelGuaranteed, "movie | series | unknown"},
		{"category.provenance", LevelGuaranteed, ""},

		{"media.origin_url", LevelOptional, "só em fontes com URL direta; nunca serializada"},
		{"media.stream_ref", LevelOptional, "só em fontes que exigem montagem da URL"},
		{"media.container_ext", LevelOptional, "DECLARATIVO: não verificado, não inspecionado"},
		{"media.container_provenance", LevelGuaranteed, ""},
		{"media.poster_url", LevelOptional, ""},
		{"media.backdrop_url", LevelOptional, ""},
		{"media.plot", LevelOptional, ""},
		{"media.rating", LevelOptional, ""},
		{"media.duration_seconds", LevelOptional, ""},

		{"signals.tmdb_id", LevelOptional, "só dígitos"},
		{"signals.imdb_id", LevelOptional, "formato tt#######"},
		{"signals.quality_tags", LevelGuaranteed, "lista, possivelmente vazia — nunca nula"},
		{"signals.language_tags", LevelGuaranteed, "idem"},
		{"signals.tags_provenance", LevelGuaranteed, ""},

		{"rejection.reason", LevelOptional, "presente somente quando kind == unresolved"},
		{"rejection.detail", LevelOptional, "legível; nunca contém URL nem credencial"},

		{"digest", LevelGuaranteed, "base do sync incremental"},

		// Tudo o que a fonte manda além disso.
		{"raw_payload.*", LevelVendor, "sanitizado; não pode virar dependência de lógica"},
	}
}

// VendorFields devolve os campos de nível V.
func VendorFields() []FieldSpec {
	var out []FieldSpec
	for _, f := range FieldCatalog() {
		if f.Level == LevelVendor {
			out = append(out, f)
		}
	}
	return out
}
