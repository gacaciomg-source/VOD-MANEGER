# VOD Manager — 01. Visão Geral, Stack e Decisões Técnicas

> Status: **PROPOSTA DE ARQUITETURA**. Nada implementado. Aguardando aprovação.

---

## 1. Resumo do que o sistema é

Um **servidor VOD intermediário** que fica entre N fontes (M3U / Xtream) e um consumidor
(XC_VM / XuiManager / player), e que:

- normaliza catálogos de várias fontes em um catálogo lógico único (`Content`);
- mantém as várias origens do mesmo conteúdo como `SourceVariant`;
- serve bytes ao cliente por proxy, com **cache-while-streaming**;
- protege as fontes (dedup de download, limite de conexões, cache hit não toca a origem);
- preserva conteúdo que sumiu das fontes (orphan / quarantine / archive).

O sistema **não transcodifica no caminho padrão**. No caminho crítico ele é,
essencialmente, um copiador de bytes com um arquivo de cache no meio.

---

## 2. Princípio arquitetural central

> **O disco é o buffer. A RAM não é.**

Essa decisão isolada resolve quatro requisitos ao mesmo tempo (baixa RAM, download
compartilhado, streaming enquanto cacheia, tolerância a clientes lentos):

```
                        ┌──────────────► cliente 1 (lê o .part no seu ritmo)
FONTE ──1 conexão──►  .part (disco) ────┼──────────────► cliente 2
                        (fetch job)     └──────────────► cliente N
```

- Existe **um** job de download por variante em andamento (single-flight).
- O job escreve sequencialmente em um arquivo `.part`.
- Cada cliente é um leitor independente do mesmo `.part`, seguindo o "tail".
- Cliente lento **não** segura o download; cliente rápido **não** estoura RAM.
- Custo de RAM por cliente ≈ 1 buffer de cópia (64–256 KB), não o tamanho do vídeo.
- Se todos os clientes caírem, o job pode continuar (política configurável) e o
  cache é concluído mesmo assim.

Consequência direta: **N clientes no mesmo filme = 1 conexão externa**, sempre,
sem precisar de lógica de "compartilhamento de stream em memória".

---

## 3. Stack proposta

| Camada | Escolha | Justificativa |
|---|---|---|
| Linguagem do core | **Go 1.23+** | O caminho crítico é I/O puro. Goroutines dão milhares de streams concorrentes com stacks de KBs. `net/http` já implementa Range/206/`ServeContent` corretamente. `sendfile`/`splice` no cache hit → cópia kernel-to-kernel, CPU ≈ 0. Binário estático único, deploy trivial, footprint baixo. |
| Banco | **PostgreSQL 16** | Catálogo com centenas de milhares de linhas, escrita concorrente durante sync, `pg_trgm` para matching por similaridade de título, `jsonb` para o payload bruto do provider, `SKIP LOCKED` para fila de jobs. SQLite trava sob sync concorrente e inviabiliza multi-node (seção 40). |
| Fila de jobs | **Postgres (`FOR UPDATE SKIP LOCKED`)** | Sync, cache forçado, arquivamento e lifecycle são jobs. Não introduzir Redis/RabbitMQ na v1 — menos peça, menos modo de falha. |
| Coordenação distribuída | **Nenhuma na v1**; interface pronta p/ Redis | Single-flight e rate limit são in-process enquanto houver 1 nó. A interface `Coordinator` permite trocar por Redis na fase multi-node sem tocar no core. |
| Migrations | **SQL puro embutido + migrador próprio (~140 linhas)** | Schema versionado e revisável, sem mágica de ORM. *(Ajuste em relação à proposta inicial: `golang-migrate` traria uma árvore grande de dependências e um binário externo para fazer o que cabe em um arquivo auditável. O migrador aplica cada arquivo em uma transação, sob `pg_advisory_lock` — o lock já é o que permitirá vários nós subirem juntos sem corrida de schema.)* |
| Acesso a dados | **pgx/v5 + SQL escrito à mão** numa camada `store` fina | Sem ORM no caminho crítico. SQL explícito e auditável; as queries do streaming precisam ser previsíveis. *(Ajuste em relação à proposta inicial: `sqlc` exigiria um passo de codegen e uma ferramenta a mais no ambiente sem ganho real no volume de queries da Fase 1. Fica como adoção opcional quando a superfície de queries crescer — o `store` já isola isso.)* |
| HTTP/router | **chi** | Fino, compatível com a stdlib, sem framework pesado no hot path. |
| Frontend | **React 18 + TypeScript + Vite + TanStack Query + Tailwind** | O painel é CRUD + tabelas grandes + realtime. Build estático servido pelo próprio binário Go (`embed.FS`) → um container, sem nginx obrigatório. |
| Realtime do painel | **SSE** (`text/event-stream`) | Monitoramento é unidirecional. SSE é mais barato e mais simples que WebSocket. |
| Métricas | **Prometheus** (`/metrics`) + agregados no Postgres | O dashboard interno lê agregados; Grafana é opcional para quem quiser. |
| Mídia | **FFmpeg opcional, fora do caminho** | Invocado apenas sob demanda manual ("Analisar Qualidade") e em remux explícito. Nunca no proxy padrão. |
| Deploy | **Docker Compose** (app + postgres) | Fase 1. |

### Por que não outras opções

- **Node.js**: streams funcionam, mas backpressure é fácil de errar e o custo de CPU
  por conexão é maior. Perde no requisito 47.
- **Python (FastAPI)**: ótimo para o painel, ruim para centenas de streams simultâneos.
  Exigiria dois runtimes; a complexidade não se paga.
- **Rust**: seria o mais rápido, mas o ganho sobre Go neste workload (I/O-bound, não
  CPU-bound) é marginal, e o custo de desenvolvimento/manutenção é alto.
- **Nginx com `proxy_cache` na frente**: resolveria cache-while-streaming
  (`proxy_cache_lock`), mas não resolve seleção de fonte por conteúdo, failover,
  matching, lifecycle, quarentena nem contabilidade por variante. Viraria uma caixa-preta
  no meio da regra de negócio. **Rejeitado** — porém o modelo mental dele (single-flight +
  arquivo temporário) é exatamente o que reimplementamos de forma própria e controlável.

---

## 4. Formato do deploy: monólito modular

Um binário, vários módulos com fronteiras explícitas. **Não** microserviços na v1.

```
vodmanager (1 binário)
├── api        (REST admin + auth)          ┐ desligáveis por configuração
├── panel      (assets estáticos embutidos) ┘
├── xtream     (API de catálogo compatível)
├── edge       (streaming proxy)  ◄── o caminho crítico
├── sync       (workers de sincronização)
├── lifecycle  (workers de cache/orphan/quarentena/arquivo)
└── health     (probes e circuit breakers)
```

Cada módulo já nasce ativável por configuração (`ROLE=manager|node|all`).
Na fase multi-servidor, um "node" sobe com `ROLE=node` (só `edge` + `health`) e fala com
o manager por uma API interna. **A arquitetura permite; a v1 não implementa.**

---

## 5. Contrato de fronteiras

Cinco interfaces. Todo o resto é detalhe interno.

```go
// 1. De onde vem o catálogo
type SourceProvider interface {
    Kind() string                                   // "m3u" | "xtream"
    FetchCatalog(ctx, SourceConfig, emit func(RawItem) error) error // streaming, sem carregar tudo em RAM
    ResolveStreamURL(ctx, VariantRef) (ResolvedURL, error)
    Probe(ctx) (HealthSample, error)
}

// 2. Onde os bytes ficam
type StorageProvider interface {
    Kind() string                                   // "local" | "gdrive" | "s3"
    Stat(ctx, key) (ObjectInfo, error)
    Open(ctx, key, offset int64) (io.ReadCloser, error)
    Create(ctx, key, sizeHint int64) (WriteHandle, error)
    Delete(ctx, key) error
    Capabilities() StorageCaps                      // aceita range? é local (sendfile)? é lento?
}

// 3. Como os bytes chegam da origem
type OriginFetcher interface {
    Open(ctx, ResolvedURL, byteRange) (OriginStream, error) // headers, tamanho, aceita range?
}

// 4. Como decidimos qual fonte usar
type VariantSelector interface {
    Select(ctx, contentID, attempt int) (*SourceVariant, error)
}

// 5. Coordenação (single-flight, locks, slots) — local hoje, distribuída depois
type Coordinator interface {
    SingleFlight(ctx, key string, fn func() error) error
    AcquireSlot(ctx, sourceID, kind) (Release, error)
}
```

Regra de ouro: **o `edge` só conhece essas interfaces**. Google Drive, S3, novos tipos
de fonte e multi-node entram por trás delas, sem tocar no caminho crítico.

---

## 6. Decisões técnicas que precisam do seu aval

São os pontos com trade-off real.

### D1 — O cache é indexado por VARIANTE, mas o HIT é por CONTEÚDO
Bytes de fontes diferentes são arquivos diferentes (tamanho, container, dublagem).
Então cada `CacheEntry` aponta para uma `SourceVariant`.
Porém, ao servir: se **qualquer** variante do conteúdo estiver cacheada e completa, é
**CACHE HIT** — entregamos ela, mesmo que não seja a "principal".
A prioridade manual governa **de onde baixar**, não **o que servir do disco**.
*(Alternativa rejeitada: re-baixar da principal quando a secundária já está em disco —
gastaria banda e conexão para entregar o mesmo filme.)*

### D2 — Cache linear na v1, chunked na v2
O `.part` é preenchido **sequencialmente a partir do byte 0**. Simples, rápido, amigável
a `sendfile`. Requisições de Range além do ponto já baixado são atendidas por um
**side-channel**: uma conexão extra à origem, servida em passthrough e **não** gravada
no cache. O schema já carrega `cache_entries.layout ('linear'|'chunked')` para permitir,
na v2, cache esparso por chunks de 8 MB com bitmap — sem migração destrutiva.

### D3 — Failover NUNCA troca de fonte no meio dos bytes já entregues
- Falha **antes** do primeiro byte ir ao cliente → failover livre para a próxima variante.
- Falha **depois** → só é permitido retomar **a mesma variante** com `Range: bytes=N-`.
  Se não der, a conexão do cliente é encerrada e o evento é logado.

Trocar de arquivo no meio produz vídeo corrompido (offsets e índices não batem).
O player normalmente re-solicita, e aí sim escolhemos outra fonte, do zero.

### D4 — MP4 com `moov` no fim exige leitura da cauda
Muitos MP4 têm o índice (`moov`) no final do arquivo. O player pede os últimos KB
**antes** de tocar. Sem tratar isso, o "fast start" falha justamente em MP4.
Tratamento: ao iniciar um cache miss de `.mp4`, buscar a cauda via side-channel (D2) para
responder de imediato e seguir o download linear em paralelo.
MKV/TS não têm esse problema. Isso será **medido**, não presumido.

### D5 — Sincronização não abre nenhuma URL de vídeo
Regra dura (requisitos 5 e 6). O sync fala só com API/M3U. Qualquer análise de mídia é
ação manual, enfileirada, com limite de concorrência.

### D6 — Decisão manual de matching é permanente
Uma decisão do administrador (agrupar, desagrupar, definir principal) é gravada em
`match_decisions` como `locked` e o algoritmo **nunca** a reverte em syncs futuros.

### D7 — Duas formas de autenticar o stream — **APROVADA com requisito adicional**

**Modo A — URL assinada** (`?exp=&sig=` HMAC): curta duração, stateless, para clientes
diretos e testes.

**Modo B — Credencial de streaming estável e revogável**: para o XC_VM, que armazena o
link. Sem expiração por padrão. Requisito adicional aprovado: **revogável a qualquer
momento e sem qualquer relação com as credenciais das fontes.**

Desenho do Modo B:

- Entidade própria `stream_credentials`, separada de tudo. O par usuário/senha é
  **gerado aleatoriamente pelo sistema** (32 bytes de entropia), nunca digitado pelo
  administrador e nunca derivado de credencial de fonte.
- Verificação por `HMAC-SHA256(server_key, senha)` guardado no banco, comparado em tempo
  constante. Não usamos Argon2 aqui porque essa verificação ocorre a **cada requisição de
  stream** e precisa ser O(1) barata; como o segredo é de alta entropia gerado pela
  máquina, não há senha fraca a proteger contra força bruta offline.
- **Revogação instantânea**: `enabled=false` / `revoked_at`. As credenciais ficam num
  cache in-process com TTL curto (≤ 5 s) e invalidação imediata por broadcast interno na
  escrita — a revogação passa a valer em segundos, sem consultar o banco por byte
  servido. Sessões de stream já abertas com a credencial revogada são derrubadas.
- `expires_at` existe mas é **NULL por padrão** — a URL do XC_VM não expira sozinha.
- Escopo por credencial: máximo de conexões simultâneas, CIDRs permitidos, rate limit,
  e (opcional) restrição por categoria. Métricas e último uso por credencial.
- **Rotação sem downtime**: criar credencial nova → apontar o XC_VM para ela → revogar a
  antiga. Múltiplas credenciais ativas simultaneamente são suportadas por desenho.
- **Isolamento absoluto entre entrada e saída**: `source_credentials` (o que usamos para
  falar com as fontes) e `stream_credentials` (o que o XC_VM usa para falar conosco) são
  tabelas distintas, com finalidades distintas e **sem nenhum caminho de código entre
  elas**. Credencial de fonte nunca aparece em resposta pública, M3U de saída, URL, log
  ou mensagem de erro. A Fase 7 inclui um teste automatizado que varre todas as respostas
  públicas procurando qualquer valor de credencial de fonte e **falha** se encontrar.

Ambos os modos escondem a URL de origem. A URL original **nunca** sai para o cliente.

### D8 — XC_VM opera em modo PROXY na v1 — **APROVADA**

```
cliente final ──► XC_VM ──► VOD Manager ──► cache | fonte
```

O XC_VM continua dono do controle de clientes, conexões e contabilidade. O VOD Manager
é camada de origem/cache e enxerga o XC_VM como um único assinante (uma
`stream_credential`), não como N clientes.

Consequências assumidas:
- a banda de saída trafega duas vezes (VOD Manager → XC_VM → cliente);
- os limites por credencial devem ser dimensionados para o XC_VM inteiro, não por cliente;
- as métricas de "clientes ativos" refletem conexões do XC_VM, e isso será rotulado
  assim no painel para não induzir a erro.

**Entrega direta (redirect 302 do XC_VM para o VOD Manager) fica como opção futura**,
implementada apenas se os testes de carga demonstrarem vantagem clara. O desenho já a
suporta: bastaria emitir uma URL do Modo A na resposta de catálogo. Não será construída
agora.
