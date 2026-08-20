# VOD Manager — 04. Estrutura do Projeto, APIs, Riscos e Testes

---

## 1. Estrutura inicial de arquivos

```
vodmanager/
├── cmd/
│   └── vodmanager/main.go            # único binário; ROLE decide quais módulos sobem
├── internal/
│   ├── config/                       # env + arquivo, validado no boot
│   ├── db/
│   │   ├── migrations/               # SQL versionado (golang-migrate)
│   │   └── queries/                  # .sql → sqlc gera o código tipado
│   ├── domain/                       # tipos e regras puras, ZERO I/O (testável sem infra)
│   │   ├── content.go  variant.go  cache.go  lifecycle.go
│   │   └── matching/                 # normalização e scoring — 100% funções puras
│   ├── sources/
│   │   ├── provider.go               # interface SourceProvider
│   │   ├── m3u/                      # parser streaming + testes de golden file
│   │   └── xtream/                   # client da API + mapeamento
│   ├── sync/                         # orchestrator, diff, staging, scheduler
│   ├── catalog/                      # CatalogService, categorias, séries
│   ├── selector/                     # VariantSelector
│   ├── health/                       # amostras, circuit breaker
│   ├── slots/                        # SlotManager (semáforos + token bucket)
│   ├── origin/                       # OriginFetcher (HTTP client dedicado, pool por host)
│   ├── cache/
│   │   ├── engine.go                 # índice e lookup
│   │   ├── fetchjob.go               # 1 conexão → .part  ◄── caminho crítico
│   │   ├── reader.go                 # tail reader do .part ◄── caminho crítico
│   │   ├── coordinator.go            # single-flight
│   │   └── policy/                   # LRU, LFU, TTL, hybrid
│   ├── storage/
│   │   ├── provider.go               # interface StorageProvider
│   │   └── local/                    # LocalFilesystemProvider (v1)
│   ├── lifecycle/                    # máquina de estados, orphan, quarantine, archive
│   ├── edge/                         # handlers HTTP de streaming
│   ├── api/                          # REST admin (chi)
│   ├── xtreamapi/                    # API de catálogo compatível + M3U output
│   ├── auth/                         # usuários, sessões, tokens, HMAC de URL
│   ├── metrics/                      # coletor + exporter Prometheus
│   └── events/                       # log estruturado
├── web/                              # React + TS + Vite (build embutido via embed.FS)
├── testdata/                         # fixtures M3U, respostas Xtream, corpus de matching
├── test/
│   ├── fakeorigin/                   # servidor de origem controlável (ver §4)
│   ├── integration/                   # sobe Postgres real (dockertest)
│   └── load/                          # cenários k6
├── docs/                             # esta documentação
├── docker-compose.yml
├── Dockerfile
└── Makefile
```

Regra: `internal/domain` e `internal/domain/matching` **não importam nada de I/O**.
É onde mora a lógica que precisa de teste rápido e determinístico.

---

## 2. Superfície de API

### 2.1 Admin REST (`/api/v1`, autenticada)
```
POST   /auth/login                    GET  /auth/me            POST /auth/logout
GET    /sources                       POST /sources            PATCH /sources/{id}
DELETE /sources/{id}                  POST /sources/reorder    POST /sources/{id}/test
POST   /sources/{id}/sync             GET  /sources/{id}/health
GET    /sync/runs                     GET  /sync/runs/{id}
GET    /contents           (filtros: tipo, categoria, status, cache, busca)
GET    /contents/{id}                 PATCH /contents/{id}     (preserved, categoria…)
GET    /contents/{id}/variants
POST   /contents/{id}/variants/{vid}/set-role   {role: primary|secondary|tertiary|none}
POST   /contents/{id}/variants/{vid}/disable
GET    /contents/{id}/variants/{vid}/origin-url   (acesso logado)
POST   /contents/{id}/cache/force     DELETE /contents/{id}/cache
POST   /contents/{id}/analyze-quality  (manual, enfileirado)
GET    /series/{id}/seasons/{n}/episodes    (mesmas ações por episódio)
GET    /matching/pending              POST /matching/{id}/resolve
GET    /cache                         GET  /cache/policies     PUT /cache/policies
GET    /downloads                     POST /downloads/{id}/cancel
GET    /storages                      POST /storages           PATCH /storages/{id}
GET    /orphans                       POST /orphans/{id}/{keep|archive|preserve|delete}
GET    /quarantine                    PUT  /quarantine/policy
GET    /streams                       DELETE /streams/{id}     (derrubar sessão)
GET    /logs                          GET  /settings           PUT /settings
GET    /stats/dashboard
GET    /events/stream                 (SSE — monitoramento em tempo real)
```

### 2.2 Catálogo compatível com Xtream (para o XC_VM)
```
GET /player_api.php?username&password&action=
      get_vod_categories | get_vod_streams | get_vod_info
      get_series_categories | get_series | get_series_info
GET /get.php?username&password&type=m3u_plus&output=  (M3U de saída)
GET /movie/{user}/{pass}/{stream_id}.{ext}            (stream)
GET /series/{user}/{pass}/{episode_id}.{ext}          (stream)
```
Implementação **própria**, escrita a partir do formato de resposta observável do
protocolo — sem reaproveitar código, schema ou textos de nenhum sistema existente.
Só o subconjunto de VOD; live TV está fora de escopo.

### 2.3 Streaming direto
```
GET /stream/{content_id}?exp={unix}&sig={hmac}
GET /stream/e/{episode_id}?exp&sig
```
Ambos suportam `Range`, `HEAD`, `206`, `If-Range`, e respondem `Accept-Ranges: bytes`
quando o conteúdo está completo em cache.

---

## 3. Riscos técnicos e gargalos identificados

| # | Risco | Impacto | Mitigação |
|---|---|---|---|
| R1 | **Origem não aceita `Range`** | Sem seek, sem retomada, sem cauda de MP4 | Detectar no primeiro `GET` e gravar em `origin_supports_range`. Sem range: seek além do baixado espera ou retorna 416, e o failover pós-primeiro-byte é impossível (D3). Exibir isso no painel por fonte. |
| R2 | **MP4 com `moov` no fim** | Player não inicia até ter a cauda; "fast start" falha | Side-channel de cauda (D4). É o principal item a medir no protótipo da Fase 6. |
| R3 | **IOPS de disco** com muitos leitores no mesmo `.part` | Stalls, latência alta | Leitura sequencial + page cache cobre bem o caso de leitores próximos. Recomendar SSD/NVMe para o tier quente e HDD só para arquivo frio. Métrica de stall por leitor. |
| R4 | **Disco encher durante downloads simultâneos** | Falhas em cascata, cache corrompido | `StorageManager.Reserve()` com `fallocate`; admission control recusa novo download quando `free - reserved < margem`. Sem espaço → passthrough sem cache, nunca falha bruta. |
| R5 | **Descritores de arquivo / conexões** | `EMFILE`, queda geral | Limites explícitos: máx. streams globais, máx. leitores por entry, `ulimit` no container documentado. |
| R6 | **Fonte com rate limit / ban por abuso** | Perda da fonte | `SlotManager` + breaker + zero abertura de vídeo no sync + dedup. É o conjunto que protege a fonte. |
| R7 | **Falsos positivos no matching** | Filme errado tocando | Limiar alto (95), faixa de revisão manual, decisões travadas, auditoria dos sinais. |
| R8 | **API Xtream inconsistente entre painéis** | Sync quebra | Parsing tolerante (campos ausentes/tipos trocados), `raw_payload` guardado, run marcada como parcial em vez de derrubar tudo. |
| R9 | **CPU de TLS na saída** | É o maior custo de CPU real do sistema | Servir HTTP interno para o XC_VM quando estiver na mesma rede; TLS terminado só na borda pública. Documentar. |
| R10 | **Google Drive como storage de streaming** | Latência/quota podem inviabilizar Direct Stream | Tratado como storage **de arquivo**, não quente. Streaming direto dele só depois de medido. |
| R11 | **Cliente lento segurando `.part` inacabado** | Uso de disco e fds prolongado | Timeout de leitor ocioso; download conclui independente do leitor. |
| R12 | **Duas requisições concorrentes criando o mesmo `cache_entry`** | Arquivo duplicado/corrompido | Single-flight in-process + unique index parcial no banco como rede de segurança. |
| R13 | **Multi-node futuro quebra o single-flight** | Downloads duplicados entre nós | `Coordinator` já é interface; Redis/advisory lock do Postgres entra na fase multi-node. |
| R14 | **Conteúdo ao vivo/HLS entrando pelo M3U** | Não é VOD, quebra o modelo | Filtrar por extensão/tipo; itens não-VOD são ignorados e reportados na run. |

---

## 4. Como testar o sistema

### 4.1 `fakeorigin` — o teste mais importante do projeto
Um servidor HTTP de mentira, controlável, que emula fontes reais:
```
modos: normal | lento (bps configurável) | sem-range | 302-em-cadeia |
       corta-no-meio | 403-intermitente | timeout | content-length-mentiroso |
       rate-limited (recusa acima de N conexões)
```
Com ele, testamos failover, retomada por Range, dedup, breaker e limites **sem tocar em
nenhuma fonte real** e de forma determinística no CI.

### 4.2 Pirâmide
| Nível | O que cobre | Ferramenta |
|---|---|---|
| Unitário | normalização, scoring de matching, parser M3U, políticas LRU/LFU/TTL, máquina de estados do lifecycle | `go test`, table-driven, golden files |
| Concorrência | single-flight, tail reader, breaker | `go test -race`, testes com N goroutines |
| Integração | sync completo, diff, órfão→quarentena→arquivo→restauração, APIs | Postgres real via dockertest + `fakeorigin` |
| Contrato | respostas do `player_api.php` e do M3U de saída | snapshot tests |
| Carga | 100–500 clientes, muitos no mesmo conteúdo, fontes lentas/caindo, alta taxa de hit | k6 + coleta de TTFB/hit rate/CPU/RAM |

### 4.3 Cenários de carga obrigatórios (requisito 43)
1. 200 clientes, 200 conteúdos distintos, tudo cache miss → medir conexões à origem.
2. 200 clientes, **1** conteúdo, cache miss → deve resultar em **1** conexão à origem.
3. 500 clientes, tudo cache hit → medir CPU, RAM e banda de saída.
4. Fonte a 500 KB/s com player pedindo 3 MB/s → medir stalls e comportamento.
5. Fonte principal derrubada no meio da carga → medir tempo de recuperação e erros.
6. Disco a 98% de uso durante downloads → verificar admission control.

### 4.4 Definição de "concluído"
Uma funcionalidade só é considerada pronta quando: tem teste automatizado que falha se a
lógica quebrar, passa com `-race`, está documentada, e — se for do caminho crítico — tem
número medido (TTFB, CPU, RAM). Integração que não puder ser testada (ex.: uma fonte real
específica) será **declarada explicitamente como não testada** no relatório da fase.

---

## 5. Como garantimos baixa latência

1. **Cache hit não toca banco no caminho de bytes** — contadores são agregados em lote.
2. **`sendfile`** no hit: os bytes não passam por userspace.
3. **`initial_buffer_bytes` pequeno e configurável** (default 1 MiB) no miss.
4. **Pool de conexões keep-alive por host de origem** — evita DNS+TCP+TLS a cada request.
5. **Cache da URL resolvida** (após redirects) com TTL curto — corta 1 RTT no Xtream.
6. **Zero FFmpeg, zero probe, zero HEAD extra** antes de começar a servir.
7. **Autorização O(1)** — HMAC, sem consulta a banco para validar assinatura.
8. **Orçamento de latência medido e publicado**: alvo de TTFB < 20 ms em hit e
   < (RTT da origem + 200 ms) em miss. Se não bater, é bug, não característica.

---

## 6. Como evitamos conexões desnecessárias às fontes

| Mecanismo | Conexões que elimina |
|---|---|
| Cache hit por conteúdo (D1) | 100% das reproduções repetidas |
| Single-flight / attach | N−1 conexões quando N clientes pedem o mesmo item |
| Sync sem abrir vídeo (D5) | milhares de conexões por sincronização |
| Sem análise automática de qualidade | idem |
| `SlotManager` (conexões/downloads/banda) | garante teto duro por fonte |
| Circuit breaker | para de martelar fonte que já está caindo |
| Reutilização de arquivo na restauração | re-download inteiro de conteúdo que voltou |
| URL resolvida em cache | requisições de redirect repetidas |
| Keep-alive por host | handshakes TCP/TLS repetidos |
| Download continua após o cliente sair | evita re-baixar do zero na próxima vez |
