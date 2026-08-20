# VOD Manager — 03. Fluxos

Todos os fluxos exigidos nos itens 7 a 16 da seção 50 do documento original.

---

## 1. Serviços internos (quem faz o quê)

| Serviço | Responsabilidade | Roda onde |
|---|---|---|
| `AuthService` | usuários, sessões, tokens, assinatura HMAC de URL | api |
| `SourceService` | CRUD de fontes, credenciais, prioridade, limites | api |
| `SyncOrchestrator` | agenda e executa runs de sync por fonte | sync |
| `M3UProvider` / `XtreamProvider` | falam o protocolo da fonte, emitem `RawItem` | sync |
| `CatalogService` | aplica o diff do sync no catálogo | sync |
| `MatchingEngine` | normaliza, gera candidatos, pontua, agrupa | sync |
| `VariantSelector` | escolhe a variante para uma requisição | edge |
| `SourceHealthManager` | circuit breakers e amostras de saúde | health |
| `SlotManager` | semáforos de conexão/download e token bucket por fonte | edge |
| `DownloadCoordinator` | single-flight, cria/anexa `FetchJob` | edge |
| `FetchJob` | 1 conexão à origem → escreve `.part` → notifica leitores | edge |
| `CacheReader` | serve bytes de `.part`/arquivo completo ao cliente | edge |
| `CacheEngine` | índice, políticas LRU/LFU/TTL/Hybrid, eviction | lifecycle |
| `LifecycleManager` | máquina de estados ACTIVE→…→DELETED | lifecycle |
| `StorageManager` | roteia por provider, admission control de espaço | lifecycle/edge |
| `MetricsCollector` | TTFB, throughput, hit rate; agrega em lote | todos |
| `OutputService` | M3U de saída e API compatível com Xtream | xtream/api |

---

## 2. Fluxo geral de uma requisição VOD

```
GET /stream/{content_id}?exp&sig      (ou /movie/{user}/{pass}/{id}.{ext})
        │
        ├─ 1. Auth: valida HMAC/credencial + rate limit por IP e por assinante
        │        (falha → 401/429, custo ~0, nenhuma consulta pesada)
        │
        ├─ 2. Resolve alvo: content_id/episode_id → 1 SELECT já indexado
        │
        ├─ 3. CacheEngine.LookupAnyComplete(target)         ◄── decisão D1
        │        │
        │        ├─ COMPLETO no storage local ──────────► [CACHE HIT]  (§3)
        │        ├─ EM PREENCHIMENTO (.part) ───────────► [ATTACH]     (§6)
        │        └─ NADA ──────────────────────────────► [CACHE MISS]  (§4)
        │
        └─ 4. Registra `stream` + emite métricas (assíncrono, fora do caminho de bytes)
```

Custo do caminho feliz antes do primeiro byte: 1 verificação HMAC + 1–2 SELECTs
indexados + 1 `open()`. Meta de TTFB em cache hit: **< 20 ms**.

---

## 3. Fluxo CACHE HIT

```
Cliente ──► edge ──► CacheEngine (hit)
                        │
                        ▼
              http.ServeContent(file)
                        │
                        ▼  (sendfile / splice — sem cópia para userspace)
                     Cliente
```

- **Zero conexões à origem.** A fonte não é consultada nem para validar.
- Range, `206 Partial Content`, `If-Range`, seek: tudo nativo via `ServeContent`.
- `last_access_at` e `access_count` são atualizados **em lote**, fora do request.
- CPU por stream ≈ ruído. O gargalo passa a ser NIC e IOPS, não o processo.

---

## 4. Fluxo CACHE MISS

```
Cliente
  │
  ▼
edge ──► VariantSelector.Select(content, attempt=0)
  │         ordem: override manual (principal→secundária→terciária)
  │                senão prioridade da fonte
  │                filtro: variante habilitada, disponível, breaker fechado, slot livre
  ▼
SlotManager.Acquire(source, {connection, download})
  │  (sem slot → tenta próxima variante; sem nenhuma → 503 com Retry-After)
  ▼
StorageManager.Reserve(size_estimado)          ◄── admission control de disco
  │  (sem espaço → tenta eviction elegível; senão passthrough sem cache)
  ▼
DownloadCoordinator.GetOrCreate(variant_id)    ◄── single-flight
  │
  ▼
FetchJob:
  GET origin_url  (1 requisição; segue redirects; cacheia a URL resolvida por TTL curto)
  lê headers → size, accept-ranges, content-type
  cria cache_entry(state='filling') + arquivo .part (fallocate se tamanho conhecido)
  loop: read(origin) → write(.part) → fsync-lite → publica progresso
  │
  ├── ao atingir `initial_buffer_bytes` (default 1 MiB, configurável) ──► libera o cliente
  │
  ▼
CacheReader(cliente) segue o .part até EOF lógico
  │
  ▼
fim: .part → rename atômico para o arquivo final; state='complete'; checksum gravado
```

Pontos de projeto:

- O download **não** é interrompido quando o cliente desconecta, se
  `keep_download_on_disconnect=true` (default) **e** o progresso for maior que um limiar
  (default 5%). Abaixo disso, cancela — evita torrar banda por um zap de zapping.
- `initial_buffer_bytes` pequeno favorece TTFB; grande favorece estabilidade em fonte
  lenta. É configurável globalmente e por fonte.
- Se a fonte for mais lenta que o consumo do player, o leitor bloqueia esperando
  progresso (com timeout). Isso é *starvation*, não erro; é logado e medido.

---

## 5. Fluxo de FAILOVER

### 5.1 Antes do primeiro byte entregue (failover livre)
```
tentativa 0: variante principal   → connect timeout / 403 / 404 / 5xx / sem slot
        │
        ▼  registra amostra de saúde (alimenta o breaker)
tentativa 1: variante secundária  → falha
        │
        ▼
tentativa 2: variante terciária   → OK → segue o fluxo de cache miss
        │
        ▼  esgotou (limite configurável de tentativas): 502 + evento logado
```

### 5.2 Depois do primeiro byte (decisão D3)
```
origem cai no byte N
    │
    ├─ variante aceita Range? ──► reconecta MESMA variante com `Range: bytes=N-`
    │                             (backoff exponencial, máx. R tentativas)
    │                             sucesso → continua o mesmo .part, cliente nem percebe
    │
    └─ não aceita Range, ou R tentativas falharam
             │
             ▼
       .part é marcado `failed` (não vira `complete`, não é servido como cache)
       conexão do cliente encerrada; evento `failover_aborted` logado
       o player re-solicita → nova requisição cai em 5.1 e escolhe outra fonte
```
**Nunca** emendamos bytes de dois arquivos diferentes.

### 5.3 Circuit breaker (SourceHealthManager)
```
CLOSED ──(taxa de erro > X% em janela de N amostras, ou T timeouts seguidos)──► OPEN
OPEN   ──(cooldown configurável)──► HALF_OPEN ──(1 requisição de teste)
                                        ├─ sucesso ──► CLOSED (prioridade original restaurada)
                                        └─ falha   ──► OPEN (cooldown dobrado, com teto)
```
- Alimentado **primariamente por tráfego real** (custo zero de sondagem).
- Sonda ativa é leve e opcional: um `GET` da API do provider (Xtream) ou `HEAD` do M3U,
  no intervalo do sync. **Nunca** abre URLs de vídeo para sondar.
- O breaker **mascara temporariamente** a fonte. Ele **não escreve** em
  `primary_variant_id`. A escolha manual do administrador permanece intacta e volta a
  valer assim que o breaker fecha.

---

## 6. Fluxo de DOWNLOAD COMPARTILHADO (deduplicação)

```
t=0.0s  cliente 1 → miss → DownloadCoordinator.GetOrCreate("variant:551")
                            não existe → cria FetchJob(551) → 1 conexão à origem
t=0.4s  cliente 2 → LookupAnyComplete → encontra cache_entry state='filling'
                            → GetOrCreate devolve o MESMO FetchJob → attach (0 conexões)
t=0.9s  cliente 3 → attach
...
t=3.2s  cliente 10 → attach

Origem vê exatamente 1 conexão.
```

Mecânica do attach:

- `readers_count` do job é incrementado; cada leitor abre seu próprio descritor do `.part`.
- O progresso (`bytes_written`) é publicado por um broadcaster
  (`sync.Cond` / canal fechado por geração). Leitores acordam e continuam de onde estão.
- Um leitor que entra depois **não** começa do byte atual: começa do byte 0, porque os
  bytes anteriores já estão no `.part`. É download compartilhado *e* streaming
  independente por cliente ao mesmo tempo.
- Leitor que chega quando o download já terminou vira um cache hit normal.
- Chave do single-flight = `variant_id`. Quando duas variantes distintas do mesmo
  conteúdo forem pedidas, elas são arquivos diferentes — mas D1 faz o segundo cliente
  cair na variante que já está em preenchimento, então na prática o dedup é por conteúdo.

---

## 7. Fluxo de SINCRONIZAÇÃO

```
Scheduler (cron por fonte) ──► enfileira sync_job
        │
        ▼
Worker pega o job (SKIP LOCKED) → cria sync_run
        │
        ▼
Provider.FetchCatalog(streaming) ──► emit(RawItem) item a item
        │   M3U:    parse linha a linha do #EXTINF (nunca carrega o arquivo em RAM)
        │   Xtream: get_vod_categories, get_vod_streams, get_series,
        │           get_series_info (por série, com concorrência limitada)
        ▼
Staging: grava RawItems em `sync_staging` (tabela temporária por run)
        │
        ▼
DIFF contra source_variants da fonte:
        ├─ NOVO       → cria variant → MatchingEngine (§8)
        ├─ EXISTENTE  → compara hash do payload
        │                 igual    → só atualiza last_seen_at (barato)
        │                 diferente→ atualiza título/categoria/URL, loga alteração
        └─ AUSENTE    → NÃO apaga. missing_count++, missing_since=now (se null)
                         passado o período de tolerância → available=false
                            └─ se ficou indisponível em TODAS as fontes → §10 (órfão)
        ▼
Atualiza contadores, fecha sync_run, emite eventos
```

Garantias:
- **Nenhuma URL de vídeo é aberta** (D5).
- Consumo de RAM constante e independente do tamanho do catálogo (streaming + staging).
- Modo incremental: se o provider expõe algo comparável (contagem/timestamp), pula
  categorias intocadas; senão, o diff por hash já evita escrita desnecessária.
- Sync manual, por fonte, é o mesmo caminho com `trigger='manual'`.

---

## 8. Fluxo de MATCHING (identificação de conteúdo)

```
RawItem
  │
  ▼ normalização
    - minúsculas, remove acentos, colapsa espaços/pontuação
    - remove tags de qualidade/idioma: 1080p, 4K, FHD, HDR, DUAL, LEG, DUB, [L], (2160p)…
    - extrai ano de "(2014)" / ".2014." / " 2014"
    - normaliza numerais romanos e artigos iniciais
    - séries: detecta S01E02, 1x02, T01 EP02, "Temporada 1 Episódio 2"
  │
  ▼ geração de candidatos (barata, indexada)
    tmdb_id/imdb_id exatos  →  senão  →  pg_trgm similarity(normalized_title) > limiar
                                          restrito por ±1 ano quando há ano
  │
  ▼ pontuação (0–100) — pesos calibrados e verificados por tabela de casos
    +95 tmdb_id igual  | +90 imdb_id igual  | −70 ids conhecidos e divergentes
    +55 título normalizado idêntico | +0..25 por similaridade trigram
    +20 ano idêntico | +5 ano ±1 | −45 ano divergente >1
    +20 temporada+episódio idênticos | −70 temporada/episódio divergentes
    −60 tipo divergente (filme × episódio)
  │
  ▼ decisão
    ≥ 95  → agrupa automaticamente (match_decisions decision='grouped', actor='auto')
    80–94 → cria conteúdo próprio E marca 'pending_review' na fila de revisão do painel
    < 80  → cria conteúdo novo, sem agrupar
```

As três âncoras que fixam a escala (todas com teste):

| Situação | Score | Decisão |
|---|---|---|
| título idêntico + ano idêntico | 100 | agrupa |
| título idêntico + ano com 1 de diferença | 85 | revisão |
| título idêntico, nenhum dos dois tem ano | 80 | revisão |
| mesmo título, anos distantes (remake) | 35 | não agrupa |

> Os pesos propostos na primeira versão desta seção não alcançavam 95 nem no caso mais
> forte — o "mesmo filme em duas fontes" cairia em revisão manual para sempre. O erro
> apareceu ao tornar a regra executável e coberta por tabela de casos, e foi corrigido
> aqui e em `internal/ingest/match.go`.
- Nunca há hash/download/inspeção do vídeo (requisito 7).
- Decisão manual grava `locked=true` e passa a ter precedência absoluta (D6).
- Toda pontuação fica em `match_decisions.signals` (jsonb) para auditoria — dá pra
  explicar no painel *por que* dois itens foram agrupados.

---

## 9. Fluxo de CONTEÚDO ÓRFÃO

```
Sync marca a última variante disponível como available=false
        │
        ▼
LifecycleManager (tick periódico) detecta: conteúdo sem NENHUMA variante disponível
        │
        ├─ tem arquivo em cache? ── NÃO ──► status='orphan' (só metadado; nada a preservar)
        │                                    política pode remover após X dias
        │
        └─ SIM ──► NÃO APAGA NADA
                    cria orphan_record (data do sumiço, fontes anteriores, tamanho,
                                        última variante usada, localização atual)
                    contents.status = 'orphan'
                    cache_entry marcada como não-elegível para eviction automática
                    aparece na tela "Conteúdos Órfãos"
```

## 10. Fluxo de QUARENTENA

```
orphan  ──(política configurada)──►
   ├─ "excluir imediatamente"        → DELETED (exige confirmação; padrão desligado)
   ├─ "manter por X dias"            → mantém em cache, expira em X dias
   ├─ "manter em quarentena" (padrão)→ QUARANTINE com expires_at = now + N dias
   └─ "mover para storage"           → ARCHIVED direto

QUARANTINE, ao expirar:
   ├─ preserved=true ou pinned=true ────────► permanece indefinidamente (requisito 22/49)
   ├─ há storage de arquivamento configurado ► ARCHIVED (§11)
   └─ não há                                 ► ação conforme política:
                                               manter (padrão seguro) | excluir (explícito)
```
Nada sai de quarentena por pressão de espaço se estiver `preserved`/`pinned`/`em uso`.

## 11. Fluxo de ARQUIVAMENTO

```
QUARANTINE expirada (ou ação manual "Arquivar")
        │
        ▼
StorageManager escolhe o provider de arquivo (por prioridade/capacidade)
        │
        ▼
Copia local → provider (streaming, com verificação de checksum ao final)
        │
        ├─ checksum diverge → aborta, mantém local, loga erro. NUNCA apaga antes de validar.
        │
        ▼ ok
   archive_entries criado; cache_entry.state='archived'
   arquivo local removido → espaço liberado
   conteúdo continua no catálogo, marcado como "arquivado"
```
Reprodução de conteúdo arquivado: se o provider suporta range e latência aceitável,
o `edge` serve direto dele (é só outro `StorageProvider`); senão, restaura para local
primeiro (job assíncrono) e o cliente recebe 503 com `Retry-After` enquanto isso.
**Isso será medido no Google Drive antes de prometer streaming direto.**

## 12. Fluxo de RESTAURAÇÃO

```
Sync encontra item novo em qualquer fonte
        │
        ▼ MatchingEngine
    casa com um conteúdo em status orphan/quarantine/archived?
        │
        ▼ SIM
    nova source_variant criada e vinculada
    orphan_record fechado (resolution='restored')
    quarantine_entry cancelada
    contents.status = 'active'
        │
        ▼
    arquivo já existe no cache local e state='complete'?
        ├─ SIM  → REUTILIZA. Nenhum download. Nenhuma conexão. (requisito 21)
        └─ NÃO  → se estiver arquivado, permanece arquivado até ser pedido;
                  se não houver arquivo, volta a ser cache miss normal no 1º acesso
```

---

## 13. Fluxo de limitação de conexões (requisito 28)

`SlotManager` mantém, por fonte, três limitadores:
```
sem_connections  (max_connections)         — toda conexão à origem
sem_downloads    (max_concurrent_downloads)— apenas FetchJobs
bucket_bandwidth (max_bandwidth_bps)       — token bucket aplicado na leitura do FetchJob
```
Ordem de decisão quando o limite é atingido:
1. o conteúdo está em cache? → serve do cache (não consome slot algum);
2. há outra variante com slot livre? → usa a próxima fonte;
3. senão, espera até `wait_timeout` (configurável, default 5s);
4. estourou → `503` com `Retry-After`, evento logado.

**Nunca** existe caminho que abra conexão sem passar pelo `SlotManager`.
Cache hits e attaches consomem **zero** slots — é aí que a economia real acontece.
