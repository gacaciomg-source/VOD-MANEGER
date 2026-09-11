package store

import (
	"context"
	"sort"
)

// Quanto espaço o acervo inteiro precisaria.
//
// # O defeito da versão anterior
//
// Ela multiplicava, por fonte, o número de VARIANTES pelo tamanho médio. Só que variante não
// é título: uma fonte declara o mesmo filme em três pastas, outra fonte o declara de novo, e
// o filme entrava na conta cinco vezes. O sistema guarda UMA cópia de cada título — então a
// estimativa respondia a uma pergunta que ninguém fez, com um número várias vezes maior que
// o real. Uma decisão de compra de disco em cima disso compraria disco a mais.
//
// # A unidade certa
//
// O que ocupa espaço é o que o sistema guarda: um arquivo por filme e um por episódio. A
// conta começa por aí — cada título uma vez, venha de quantas fontes e pastas vier.
//
// # O peso de cada título, sem abrir arquivo nenhum
//
// Pela FAIXA DE QUALIDADE que a própria fonte declara no nome ("1080p", "4K", "SD"). Um
// filme 4K pesa dez vezes um SD, e tratar os dois pela mesma média erraria nos dois.
//
// O tamanho de cada faixa sai do que já foi baixado, quando há amostra; sem amostra, de uma
// referência típica de IPTV — e a resposta diz qual das duas foi usada, faixa por faixa.
// Número medido e número de referência não podem ter a mesma cara.

// Faixas de qualidade, da mais leve à mais pesada. "?" é a fonte que não disse.
const (
	FaixaSD           = "sd"
	FaixaHD           = "hd"
	FaixaFHD          = "fhd"
	Faixa4K           = "4k"
	FaixaDesconhecida = "?"
)

// ordemDasFaixas é o que "melhor qualidade" significa. A desconhecida fica fora: não dá para
// dizer que ela é melhor ou pior que nenhuma.
var ordemDasFaixas = map[string]int{FaixaSD: 1, FaixaHD: 2, FaixaFHD: 3, Faixa4K: 4}

// faixaSQL traduz as marcas de qualidade de uma variante em faixa.
//
// Em SQL, e não em Go, porque são centenas de milhares de variantes: trazer todas para a
// memória para classificar seria mover o catálogo inteiro pela rede a cada abertura do
// painel. A lista espelha o dicionário de marcas do ingest (dictionaries/tags.json).
const faixaSQL = `
	CASE
		WHEN v.quality_tags && '{2160p,4k,uhd}'::text[]                     THEN '4k'
		WHEN v.quality_tags && '{1080p,1080i,fhd}'::text[]                  THEN 'fhd'
		WHEN v.quality_tags && '{720p,hd,hq,hdrip,hdtv}'::text[]            THEN 'hd'
		WHEN v.quality_tags && '{576p,480p,360p,sd,dvdrip,cam,ts,tc}'::text[] THEN 'sd'
		ELSE '?'
	END`

// amostraMinimaPorFaixa é quantos arquivos baixados bastam para confiar na média medida.
//
// Abaixo disso, um único filme atípico desloca a média — e a referência típica erra menos
// que três pontos.
const amostraMinimaPorFaixa = 10

// referenciaPorFaixa é o tamanho típico de IPTV, usado enquanto não há amostra.
//
// Filme de duas horas, episódio de quarenta e cinco minutos, nas taxas que as fontes
// costumam usar. É ponto de partida, e não medição — por isso a resposta marca quando foi
// ele que valeu.
var referenciaPorFaixa = map[string]map[string]int64{
	TargetContent: {
		FaixaSD: 900 << 20, FaixaHD: 1600 << 20, FaixaFHD: 3 << 30, Faixa4K: 12 << 30,
		FaixaDesconhecida: 2 << 30,
	},
	TargetEpisode: {
		FaixaSD: 350 << 20, FaixaHD: 600 << 20, FaixaFHD: 1200 << 20, Faixa4K: 4 << 30,
		FaixaDesconhecida: 800 << 20,
	},
}

// PesoDaFaixa é quanto um título daquela faixa ocupa, e de onde saiu o número.
type PesoDaFaixa struct {
	Tipo    string `json:"tipo"` // "content" (filme) ou "episode"
	Faixa   string `json:"faixa"`
	Bytes   int64  `json:"bytes"`
	Amostra int64  `json:"amostra"`
	// Medido: a média veio do que já foi baixado. Falso é a referência típica.
	Medido bool `json:"medido"`
	// Titulos é quantos títulos caem nesta faixa seguindo a prioridade das fontes.
	Titulos int64 `json:"titulos"`
}

// CenarioDeArmazenamento é o total para uma regra de escolha de qual cópia guardar.
type CenarioDeArmazenamento struct {
	Bytes int64 `json:"bytes"`
	// JaGuardados é quanto disso já está no acervo. A diferença é o que falta.
	JaGuardadosBytes int64 `json:"ja_guardados_bytes"`
}

// EstimativaDoAcervo é a resposta inteira.
type EstimativaDoAcervo struct {
	Filmes      int64 `json:"filmes"`
	Episodios   int64 `json:"episodios"`
	JaGuardados int64 `json:"ja_guardados"`
	// Variantes é quantas cópias as fontes oferecem, somadas. Aparece para que a diferença
	// para os títulos — o que NÃO precisa ser baixado — fique visível.
	Variantes int64 `json:"variantes"`

	// Prioridade: guardando a cópia da fonte que toca primeiro. É o que o sistema faz.
	Prioridade CenarioDeArmazenamento `json:"prioridade"`
	// MaisLeve: guardando sempre a menor cópia disponível de cada título.
	MaisLeve CenarioDeArmazenamento `json:"mais_leve"`
	// MelhorQualidade: guardando sempre a de maior qualidade declarada.
	MelhorQualidade CenarioDeArmazenamento `json:"melhor_qualidade"`

	Faixas []PesoDaFaixa `json:"faixas"`
}

// EstimarAcervo calcula quanto o acervo inteiro ocuparia, contando cada título uma vez.
func (s *Store) EstimarAcervo(ctx context.Context) (*EstimativaDoAcervo, error) {
	pesos, err := s.pesosPorFaixa(ctx)
	if err != nil {
		return nil, err
	}

	// Um título por linha, agrupado pelo que importa para a conta.
	//
	// Para cada filme e cada episódio: a faixa da cópia que toca primeiro (a prioridade,
	// com a escolha manual do administrador na frente, na mesma ordem da reprodução), o
	// conjunto de faixas disponíveis entre todas as fontes, e se já está guardado.
	//
	// O agrupamento final colapsa milhares de títulos em algumas dezenas de combinações — é
	// isso que viaja pela rede, e não o catálogo.
	rows, err := s.pool.Query(ctx, `
		WITH candidatas AS (
			SELECT v.target_kind, v.target_id, `+faixaSQL+` AS faixa,
			       row_number() OVER (
			           PARTITION BY v.target_kind, v.target_id
			           -- coalesce: sem escolha manual o IN dá NULL, e NULL vem PRIMEIRO em ordem
							-- decrescente — poria na frente justamente quem não foi escolhido.
							ORDER BY coalesce(v.id IN (c.primary_variant_id, e.primary_variant_id), false) DESC,
			                    s.priority, v.id) AS ordem
			FROM source_variants v
			JOIN sources s ON s.id = v.source_id AND s.enabled
			LEFT JOIN contents c ON v.target_kind = 'content' AND c.id = v.target_id
			LEFT JOIN episodes e ON v.target_kind = 'episode' AND e.id = v.target_id
			WHERE v.enabled AND v.available
			  AND (
			      (v.target_kind = 'content' AND c.type = 'movie' AND c.status <> 'deleted')
			   OR (v.target_kind = 'episode' AND e.status <> 'deleted')
			  )
		),
		guardados AS (
			SELECT DISTINCT v.target_kind, v.target_id
			FROM arquivos_guardados a
			JOIN source_variants v ON v.id = a.variant_id
			WHERE a.estado = 'pronto'
		),
		titulos AS (
			SELECT c.target_kind, c.target_id,
			       max(c.faixa) FILTER (WHERE c.ordem = 1) AS faixa_prioridade,
			       array_agg(DISTINCT c.faixa ORDER BY c.faixa) AS faixas,
			       count(*) AS variantes
			FROM candidatas c
			GROUP BY c.target_kind, c.target_id
		)
		SELECT t.target_kind, t.faixa_prioridade, t.faixas,
		       (g.target_id IS NOT NULL) AS guardado,
		       count(*), sum(t.variantes)::bigint
		FROM titulos t
		LEFT JOIN guardados g ON g.target_kind = t.target_kind AND g.target_id = t.target_id
		GROUP BY 1, 2, 3, 4`)
	if err != nil {
		return nil, wrapErr("estimando o acervo", err)
	}
	defer rows.Close()

	out := &EstimativaDoAcervo{}
	for rows.Next() {
		var (
			tipo, prioridade string
			faixas           []string
			guardado         bool
			quantos, varis   int64
		)
		if err := rows.Scan(&tipo, &prioridade, &faixas, &guardado, &quantos, &varis); err != nil {
			return nil, wrapErr("estimando o acervo", err)
		}
		out.Variantes += varis
		if tipo == TargetEpisode {
			out.Episodios += quantos
		} else {
			out.Filmes += quantos
		}
		if guardado {
			out.JaGuardados += quantos
		}

		peso := func(faixa string) int64 { return pesos[tipo][faixa].Bytes }
		somar := func(cen *CenarioDeArmazenamento, bytes int64) {
			cen.Bytes += bytes * quantos
			if guardado {
				cen.JaGuardadosBytes += bytes * quantos
			}
		}

		somar(&out.Prioridade, peso(prioridade))
		somar(&out.MaisLeve, peso(maisLeve(faixas, peso)))
		somar(&out.MelhorQualidade, peso(melhorQualidade(faixas)))

		if p, ok := pesos[tipo][prioridade]; ok {
			p.Titulos += quantos
			pesos[tipo][prioridade] = p
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr("estimando o acervo", err)
	}

	for _, porFaixa := range pesos {
		for _, p := range porFaixa {
			out.Faixas = append(out.Faixas, p)
		}
	}
	sort.Slice(out.Faixas, func(i, j int) bool {
		a, b := out.Faixas[i], out.Faixas[j]
		if a.Tipo != b.Tipo {
			return a.Tipo == TargetContent // filmes primeiro
		}
		return ordemDasFaixas[a.Faixa] < ordemDasFaixas[b.Faixa]
	})
	return out, nil
}

// maisLeve é a faixa de menor peso entre as disponíveis.
//
// Pelo PESO, e não pela ordem das faixas: a desconhecida não tem lugar na ordem, mas tem
// peso — e pode ser justamente a mais leve.
func maisLeve(faixas []string, peso func(string) int64) string {
	melhor := ""
	for _, f := range faixas {
		if melhor == "" || peso(f) < peso(melhor) {
			melhor = f
		}
	}
	if melhor == "" {
		return FaixaDesconhecida
	}
	return melhor
}

// melhorQualidade é a faixa mais alta DECLARADA. Só a desconhecida disponível, fica ela.
func melhorQualidade(faixas []string) string {
	melhor := FaixaDesconhecida
	for _, f := range faixas {
		if ordemDasFaixas[f] > ordemDasFaixas[melhor] {
			melhor = f
		}
	}
	return melhor
}

// pesosPorFaixa mede o tamanho médio de cada faixa no que já foi baixado.
//
// Só cópias prontas vindas de fonte: o acervo próprio foi enviado por quem administra, em
// qualquer qualidade, e não diz nada sobre o que as fontes entregam.
func (s *Store) pesosPorFaixa(ctx context.Context) (map[string]map[string]PesoDaFaixa, error) {
	pesos := map[string]map[string]PesoDaFaixa{}
	for tipo, ref := range referenciaPorFaixa {
		pesos[tipo] = map[string]PesoDaFaixa{}
		for faixa, bytes := range ref {
			pesos[tipo][faixa] = PesoDaFaixa{Tipo: tipo, Faixa: faixa, Bytes: bytes}
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT v.target_kind, `+faixaSQL+` AS faixa, avg(a.bytes)::bigint, count(*)
		FROM arquivos_guardados a
		JOIN source_variants v ON v.id = a.variant_id
		WHERE a.estado = 'pronto' AND a.origem = 'fonte' AND a.bytes > 0
		  AND v.target_kind IN ('content', 'episode')
		GROUP BY 1, 2`)
	if err != nil {
		return nil, wrapErr("medindo o peso de cada qualidade", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tipo, faixa string
		var media, amostra int64
		if err := rows.Scan(&tipo, &faixa, &media, &amostra); err != nil {
			return nil, wrapErr("medindo o peso de cada qualidade", err)
		}
		p, ok := pesos[tipo][faixa]
		if !ok {
			continue
		}
		p.Amostra = amostra
		if amostra >= amostraMinimaPorFaixa {
			p.Bytes, p.Medido = media, true
		}
		pesos[tipo][faixa] = p
	}
	return pesos, wrapErr("medindo o peso de cada qualidade", rows.Err())
}
