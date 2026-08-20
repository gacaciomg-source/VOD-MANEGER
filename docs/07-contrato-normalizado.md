# VOD Manager — 07. Contrato Normalizado da Ingestão (Fase 2)

> Status: **PROPOSTA — aguardando aprovação antes de implementar.**
> Nada aqui está codificado.

Este documento define a fronteira entre "o que a fonte diz" e "o que o sistema entende".
É o contrato que `M3UProvider` e `XtreamProvider` precisam cumprir, e o único formato que
o resto do sistema conhece. Adicionar um terceiro tipo de fonte no futuro significa
escrever um novo provider que emita este contrato — nada mais muda.

---

## 1. As três camadas e por que elas são separadas

```
FONTE ──► [1] RawItem ──► [2] NormalizedItem ──► [3] Entidades do catálogo
          (fiel à fonte)   (interpretado)         (Content / SourceVariant / ...)
```

**[1] RawItem** é fiel ao que a fonte mandou. Nenhuma interpretação, nenhuma limpeza.
Existe para que, quando uma fonte se comportar de forma inesperada, seja possível provar
o que ela realmente enviou — sem re-consultar a fonte.

**[2] NormalizedItem** é a interpretação: título limpo, ano extraído, temporada/episódio
resolvidos, tipo decidido. Toda decisão registra *de onde* veio (campo próprio da API vs.
adivinhado do título), porque isso muda a confiança do matching na Fase 3.

**[3] Entidades** são o que vai para o banco.

A separação é o que permite reprocessar a normalização (ex.: melhorar o parser de séries
em português) **sem re-sincronizar as fontes** — o `raw_payload` já está no banco.

---

## 2. Níveis de garantia dos campos

Todo campo deste contrato é classificado. É esta classificação que você pediu para eu
documentar comparando com as suas amostras reais.

| Nível | Significado | Consequência |
|---|---|---|
| **G — Garantido** | o VOD Manager **sempre** preenche; se a fonte não fornecer, derivamos | o resto do sistema pode confiar |
| **O — Opcional** | pode ser vazio/nulo; a fonte pode não ter | todo consumidor trata a ausência |
| **V — Vendor** | específico do fornecedor; não entra em campo tipado | vai inteiro para `raw_payload` (jsonb) |

Regra dura: **nenhum campo de nível V pode virar dependência de lógica de negócio.**
Se um campo específico de um fornecedor se mostrar necessário, ele é promovido a O por
uma mudança explícita neste documento — nunca acessado ad-hoc de dentro do jsonb.

---

## 3. RawItem — o que o provider emite

Emitido em streaming, um por vez, via `emit(RawItem) error`. O provider **nunca** monta
uma lista completa em memória.

```go
type RawItem struct {
    // --- Identidade na fonte ---
    Kind        RawKind   // G: "movie" | "series" | "episode" | "unknown"
    ExternalID  string    // O: id do item na fonte (stream_id, series_id, id do episódio)
    SeriesExtID string    // O: id da série, quando Kind == episode
    SeasonNum   *int      // O: temporada declarada pela fonte
    EpisodeNum  *int      // O: episódio declarado pela fonte

    // --- Como a fonte descreve ---
    Title       string    // G: título exatamente como veio, sem qualquer limpeza
    GroupTitle  string    // O: group-title do M3U / nome da categoria
    CategoryIDs []string  // O: ids de categoria na fonte

    // --- Onde estão os bytes ---
    StreamURL    string   // O: vazio para o "contêiner" série; obrigatório em movie/episode
    ContainerExt string   // O: mp4 | mkv | ts, quando declarado

    // --- Metadados que a fonte oferece ---
    TMDBID   string       // O
    IMDBID   string       // O
    Year     *int         // O: só quando vier em CAMPO PRÓPRIO, nunca adivinhado aqui
    PosterURL, BackdropURL, Plot string // O
    Rating   *float64     // O
    DurationSeconds *int  // O

    // --- Rastreabilidade ---
    Attrs   map[string]string // O: atributos brutos do #EXTINF (tvg-id, tvg-logo, ...)
    Payload json.RawMessage   // G: resposta original do provider para este item
    Origin  RawOrigin         // G: qual requisição gerou este item (endpoint, linha do M3U)
}
```

Contratos que o provider precisa cumprir:

1. `Title` e `Payload` **sempre** preenchidos. Item sem título é descartado e contabilizado.
2. `StreamURL` **nunca** é logado, nem em nível debug. Só existe em memória e na coluna
   `source_variants.origin_url`.
3. `Year` só é preenchido quando a fonte tem um campo de ano. Ano tirado do título é
   trabalho da camada [2], que marca a procedência.
4. O provider **não** decide se um item é duplicado, se casa com outro, ou se deve ser
   removido. Ele só relata o que viu.
5. **Nenhuma URL de vídeo é aberta.** `StreamURL` é copiada como texto, jamais requisitada.

---

## 4. NormalizedItem — o que o sistema entende

```go
type NormalizedItem struct {
    Kind      ItemKind  // G: movie | episode | unresolved
    Variant   VariantKey    // G: como esta variante é identificada de forma estável
    Movie     *MovieFields  // preenchido quando Kind == movie
    Episode   *EpisodeFields// preenchido quando Kind == episode
    Category  CategoryRef   // G
    Media     MediaFields   // G
    Signals   MatchSignals  // G: insumos para a Fase 3
    Rejection *Rejection    // preenchido quando Kind == unresolved
    Digest    [32]byte      // G: hash do conteúdo normalizado (base do sync incremental)
}
```

### 4.1 VariantKey — identidade estável de uma origem

```go
type VariantKey struct {
    SourceID   int64
    ExternalID string // G quando a fonte fornece id
    URLHash    string // G quando não fornece: sha256(origin_url normalizada)
}
```

Regra: **`ExternalID` tem precedência sobre `URLHash`.** Fontes trocam a URL do mesmo
item com frequência (rotação de CDN, token no path); se identificássemos pela URL, cada
rotação criaria uma variante nova e o catálogo incharia.

Quando não há `ExternalID`, a URL é normalizada antes do hash: remoção de query string de
sessão/token, minúsculas no host. **Precisa das suas amostras** para confirmar quais
parâmetros de query são voláteis nas suas fontes — está listado como pendência na §8.

### 4.2 MovieFields

```go
type MovieFields struct {
    DisplayTitle    string // G: título limpo para exibição
    DeclaredTitle   string // G: título bruto, preservado intacto
    NormalizedTitle string // G: forma canônica para matching (§5)
    Year            *int   // O
    YearSource      Provenance // G: "field" | "title" | "none"
}
```

`DeclaredTitle` nunca é sobrescrito. É o que o administrador vê quando pergunta "o que
essa fonte realmente mandou?".

### 4.3 EpisodeFields

```go
type EpisodeFields struct {
    SeriesDeclaredTitle   string // G
    SeriesNormalizedTitle string // G
    SeriesYear            *int   // O
    SeasonNumber          int    // G
    EpisodeNumber         int    // G
    EpisodeTitle          string // O
    NumberSource          Provenance // G: "field" | "title"
}
```

**Regra crítica:** um item só vira `episode` se temporada **e** episódio forem conhecidos.
Sem isso, ele **não** vira filme por descarte — vira `unresolved`. Adivinhar errado aqui
polui o catálogo de filmes com episódios soltos, e é caro de desfazer depois.

### 4.4 CategoryRef

```go
type CategoryRef struct {
    SourceCategoryID string // O
    DeclaredName     string // G: nome como veio (group-title ou category_name)
    NormalizedName   string // G
    ContentType      string // G: "movie" | "series" — inferido, com fallback documentado
}
```

Categorias são entidade própria por fonte (`source_categories`) e depois mapeadas para
categorias canônicas do catálogo. O filtro `allowed_categories` / `ignored_categories` da
fonte é aplicado **aqui**, antes do diff — item filtrado nunca chega ao banco e é
contabilizado no relatório da run.

### 4.5 MediaFields

```go
type MediaFields struct {
    OriginURL    string // G (movie/episode): guardada apenas na coluna dedicada
    ContainerExt string // O: normalizado para minúsculas; derivado da extensão da URL se ausente
    PosterURL, BackdropURL, Plot string // O
    Rating          *float64 // O
    DurationSeconds *int     // O
}
```

`ContainerExt` é **declarativo**. Não é verificado, não é confirmado por requisição, não
é inferido do conteúdo. Ele é uma dica para o `edge` na Fase 6, não uma verdade.

### 4.6 MatchSignals — insumo da Fase 3

```go
type MatchSignals struct {
    TMDBID string // O: só dígitos
    IMDBID string // O: normalizado para o formato tt#######
    QualityTags []string // G: tags removidas do título (1080p, DUAL, LEG, ...), preservadas
    LanguageTags []string // G: DUB, LEG, DUAL, quando detectáveis
}
```

As tags removidas do título são **guardadas, não descartadas**. Elas não entram no
matching (dois "Interestelar" em 1080p e 720p são o mesmo conteúdo), mas o administrador
precisa vê-las para escolher a fonte principal.

### 4.7 Rejection — por que um item não entrou

```go
type Rejection struct {
    Reason RejectReason // G: sem_titulo | sem_url | nao_e_vod | tipo_indeterminado |
                        //    categoria_filtrada | temporada_episodio_ausente | url_invalida
    Detail string       // G: explicação legível, SEM a URL
}
```

Todo item rejeitado é contabilizado por motivo no relatório da run e amostrado nos
eventos (limite configurável). Sincronização que descarta 40% do catálogo em silêncio é
um bug esperando para ser descoberto tarde.

---

## 5. Normalização de título — regra única e testável

Aplicada em ordem fixa, implementada como função pura e coberta por tabela de casos:

```
1. NFD → remoção de diacríticos → NFC          "Coração" → "coracao"
2. minúsculas
3. remoção de tags entre [] () {} que casem com o dicionário de qualidade/idioma
4. remoção de tokens soltos de qualidade: 1080p 720p 4k uhd fhd hd sd hdr 10bit
                                          web-dl webrip bluray bdrip remux
5. remoção de tokens de idioma: dub dubl dublado leg legendado dual nacional
6. numerais romanos → arábicos (II → 2), só em posição final
7. pontuação → espaço; espaços colapsados; trim
8. artigo inicial movido para o fim quando presente ("o senhor dos aneis" mantido;
   "senhor dos aneis, o" → "senhor dos aneis o")
```

Extração de ano: `(1888..anoAtual+2)` entre parênteses, colchetes, ou delimitado por
pontos/espaços no fim do título. Fora dessa faixa, ignorado.

**O dicionário de tags é dado, não código** — arquivo versionado em `testdata/` e
carregável por configuração, para você poder ajustar às convenções das suas fontes sem
recompilar.

---

## 6. Contrato do provider

```go
type SourceProvider interface {
    Kind() string
    Capabilities() Capabilities
    FetchCatalog(ctx context.Context, cfg SourceConfig, prev *SyncState,
                 emit func(RawItem) error) (SyncState, error)
    Probe(ctx context.Context, cfg SourceConfig) (HealthSample, error)
}

type Capabilities struct {
    HasCategories      bool
    HasSeries          bool
    HasStableIDs       bool // a fonte tem id próprio por item?
    SupportsIncremental bool
    ProvidesTMDBID     bool
    ProvidesIMDBID     bool
}
```

### 6.1 Orçamento de requisições — a regra que protege as suas fontes

Cada `FetchCatalog` declara e respeita um **orçamento de requisições**. O ponto crítico é
o Xtream: `get_series_info` custa **uma requisição por série**. Um catálogo com 3.000
séries custaria 3.000 requisições por sincronização — exatamente o que você não quer.

Política:

```
1ª sincronização de uma fonte  → busca series_info de todas as séries (inevitável)
Sincronizações seguintes       → busca series_info APENAS de séries cuja entrada na
                                 listagem `get_series` mudou (digest diferente) ou que
                                 são novas
```

Se a fonte expuser algum campo de última modificação, ele é usado; senão, o digest da
entrada da listagem serve como gatilho. **Precisa das suas amostras** para saber se as
suas fontes expõem esse campo (pendência §8).

Além disso: teto absoluto de requisições por run (configurável), concorrência limitada
pelo `SlotManager` da fonte, e a run é marcada como `parcial` — nunca falha inteira — se
o teto for atingido. Melhor um catálogo incompleto e honesto que uma fonte banida.

### 6.2 SyncState — o que torna o incremental possível

```go
type SyncState struct {
    ProviderKind string
    Version      int
    CatalogDigest string            // digest da listagem inteira: igual = nada mudou
    ItemDigests   map[string][32]byte // externalID → digest (para o gatilho de series_info)
    Cursor        json.RawMessage    // específico do provider
    FetchedAt     time.Time
}
```

Guardado em `sources.sync_state` (jsonb). Se `CatalogDigest` não mudou, a run termina em
uma requisição e zero escritas — só atualiza `last_seen_at` em lote.

---

## 7. Diff e aplicação — regras invioláveis

```
Para cada NormalizedItem:
  NOVO       → cria source_variant; encaminha para o MatchingEngine (Fase 3)
  EXISTENTE  → Digest igual?  → só last_seen_at (escrita em lote, sem UPDATE por item)
               Digest difere? → atualiza campos, registra evento de alteração
  AUSENTE    → NÃO apaga. missing_count++, missing_since = now se nulo
               após o período de tolerância → available = false
```

Garantias que a Fase 2 precisa preservar, verificadas por teste:

1. **Nenhuma URL de vídeo é aberta.** Teste: o `fakeorigin` falha a suíte se qualquer
   requisição chegar a uma rota de mídia durante um sync.
2. **Nada é baixado.** Nenhum byte de vídeo toca o disco na Fase 2.
3. **Nenhuma análise de qualidade, nenhum FFmpeg.** O binário não invoca processo externo.
4. **Credenciais não vazam.** As credenciais entram na montagem da requisição e em mais
   lugar nenhum: nem em log, nem em `raw_payload`, nem em evento, nem em mensagem de erro.
   O `raw_payload` é sanitizado antes de persistir.
5. **`locked` é intocável.** A Fase 2 não altera vínculo de matching. Qualquer decisão
   com `locked = true` é preservada — teste explícito na Fase 3.
6. **Reimportar não duplica.** Rodar o mesmo sync duas vezes produz zero linhas novas.
7. **RAM constante.** Fixture grande é processada em streaming; o teste mede o pico.

---

## 8. Comparação com as suas fontes reais — pendente

Assim que você enviar as seis amostras anonimizadas, entrego **antes de implementar o
importador definitivo** uma tabela `docs/08-campos-das-fontes.md` com:

| Campo do contrato | Amostra 1 (M3U filme) | Amostra 3 (player_api) | ... | Nível final |
|---|---|---|---|---|
| `Title` | presente | presente | | G |
| `Year` | ausente (só no título) | campo `year` | | O |
| ... | | | | |

E três respostas que só as suas amostras podem dar:

1. **Quais parâmetros de query da URL são voláteis** nas suas fontes (afeta o `URLHash`
   da §4.1 e, portanto, se o catálogo incha a cada rotação de token).
2. **Se as suas fontes expõem campo de última modificação** por série (afeta diretamente
   o custo do incremental da §6.1).
3. **Quais convenções de nomenclatura de episódio** aparecem de fato (`S01E02`, `1x02`,
   `T01 EP02`, `- 1 Temporada Ep 2`, ...) — cada uma vira caso de teste.

Enquanto isso, construo sobre fixtures sintéticas e **marco explicitamente** o que ainda
não foi confrontado com dado real.

### Política de fixtures — sem credenciais, verificado por teste

- Nenhum fixture conterá domínio, usuário, senha, token ou IP reais.
- Domínios de exemplo usam `exemplo.tld` / `fonte-a.exemplo.tld`.
- **Teste de guarda no CI**: varre `testdata/` procurando padrões de credencial
  (`username=`, `password=`, `/live/`, tokens longos em base64/hex, IPs públicos) e
  **falha a suíte** se encontrar. Assim, um vazamento acidental em fixture não passa.
- Se ao anonimizar uma amostra algum campo perder o sentido, prefira substituir por um
  valor fictício com o **mesmo formato** a apagar — o formato é a informação que preciso.

---

## 9. O que a Fase 2 NÃO faz

Para não haver ambiguidade: agrupar conteúdo de fontes diferentes é **Fase 3**. A Fase 2
termina com cada fonte tendo seu próprio conjunto de `source_variants` normalizados e
diferenciados, prontos para o `MatchingEngine`, mas ainda **sem** `Content` unificado.

---

## 10. Aprovação — **CONCEDIDA em 2026-08-18**

1. **`ExternalID` tem precedência sobre `URLHash`** na identidade da variante (§4.1). ✅
2. **Item sem temporada/episódio conhecidos vira `unresolved`**, nunca filme (§4.3). ✅
   *(com a fila visível no painel para revisão posterior)*
3. **Política de `get_series_info` por digest**, com teto de requisições e run marcada
   como `partial` quando o teto for atingido (§6.1). ✅
4. **Tags de qualidade/idioma saem do título mas são preservadas** em `MatchSignals`
   (§4.6). Elas **não determinam a identidade do conteúdo**. ✅

---

## 11. Restrições ativas — nada de heurística antes das amostras

Determinação do administrador (2026-08-18). Estas três coisas são **tecnicamente
possíveis e deliberadamente não implementadas**, para não transformar uma suposição sobre
um fornecedor em regra de negócio:

| Não implementar | Por quê | Quando reavaliar |
|---|---|---|
| Regra baseada em `last_modified` | não sabemos se as fontes reais o fornecem, nem se é confiável | após evidência nas amostras + promoção V→O documentada |
| Extrair id numérico do path da URL como `ExternalID` | é inferência sobre a estrutura de URL de um fornecedor específico | idem |
| Remover credencial embutida no path para o `URLHash` | recorte errado infla o catálogo ou colide variantes distintas | idem |

O campo `last_modified` **é lido e exposto** em `xtream.Series`, mas permanece nível
Vendor e não governa nenhuma decisão.

### Materialização de URL: restrita à camada de transporte

Regra reafirmada e agora travada por teste. Parsing e normalização **nunca** montam URL de
mídia. O que atravessa a fronteira é `StreamRef{Kind, ID, Extension}` — sem host, sem
credencial. Três guardas sustentam isso:

- `TestPacotesDeParsingSaoPuros` — os pacotes de parsing não podem sequer **importar**
  `net/http`, `net`, `os/exec`, `database/sql` ou as camadas de banco. Falha na revisão,
  não em produção.
- `TestXtreamNuncaMaterializaURL` — nenhum item sai do mapeamento ou da normalização com
  URL preenchida.
- `TestStreamRefNaoCarregaCredencial` — o `StreamRef` não contém usuário, senha, token
  nem `://`.

### Ordem de trabalho quando as amostras chegarem

Acordada e sem pular etapa:

1. amostras em `testdata/amostras/`;
2. análise dos campos;
3. produzir `docs/09-campos-das-fontes.md`;
4. classificar cada campo como Garantido / Opcional / Vendor;
5. **só então** registrar formalmente qualquer promoção Vendor → Opcional;
6. criar testes para as convenções encontradas;
7. ajustar os parsers;
8. **só então** avançar para HTTP client, scheduler, staging, diff e persistência.
