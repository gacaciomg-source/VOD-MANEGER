# VOD Manager — 02. Modelo de Dados

> PostgreSQL 16. Nenhum byte de vídeo dentro do banco. URLs de origem sempre separadas
> das URLs públicas. Toda chave primária é `bigint identity`, exceto onde indicado.

---

## 1. Mapa das entidades

```
users ── sessions
         api_tokens
         stream_credentials   ── SAÍDA: o que o XC_VM usa para falar conosco
                                 (sem nenhuma ligação com source_credentials)

sources ── source_credentials  ── ENTRADA: cifrada, nunca sai em API
   │    ── source_categories
   │    ── source_health_samples / source_health_state
   │
   └──── source_variants ────┐
                             │  (N variantes → 1 conteúdo)
contents ────────────────────┘
   ├── movies         (1:1 com content quando type='movie')
   ├── series         (1:1 com content quando type='series')
   │     └── seasons ── episodes ── (episodes também têm source_variants)
   ├── content_categories → categories
   ├── content_stats  (contadores quentes, tabela separada)
   └── lifecycle_state

source_variants ── cache_entries ── storage_objects → storage_providers
                └── download_jobs

streams (sessões de reprodução ativas/históricas)
sync_jobs ── sync_runs ── sync_item_diffs
match_decisions
orphan_records ── quarantine_entries ── archive_entries
events (log estruturado)
```

**Regra de modelagem:** episódios são conteúdo de primeira classe. `source_variants`
aponta para **ou** um `content` (filme) **ou** um `episode`, nunca ambos — via
`target_kind` + `target_id` com constraint. Isso evita duplicar toda a pilha de
cache/download para episódios.

---

## 2. Tabelas principais

### `sources`
```
id, name, description, kind ('m3u'|'xtream'),
base_url, priority int, enabled bool, status ('ok'|'degraded'|'down'|'disabled'),
sync_interval_minutes int, sync_cron text null,
max_connections int, max_concurrent_downloads int, max_bandwidth_bps bigint null,
allowed_categories text[], ignored_categories text[],
last_sync_at, last_success_at, created_at, updated_at
```
`priority` define a ordem padrão (drag-and-drop no painel reescreve a coluna em lote).

### `source_credentials`
```
id, source_id, username, password_enc bytea, extra_enc jsonb,
key_version int, created_at
```
Cifrado com AES-GCM usando chave mestra vinda de env/arquivo. **Nunca** serializado em
resposta de API, nem para admin — o painel mostra apenas "configurado / não configurado".

### `stream_credentials` — autenticação de SAÍDA, revogável (decisão D7)
> Tabela definida aqui, **criada na Fase 7** (nada de tabela morta antes do uso).
```
id, name, description,
username text unique,          -- gerado pelo sistema
password_hmac bytea,           -- HMAC-SHA256(server_key, senha). Senha nunca em claro.
key_version int,
enabled bool default true,
revoked_at timestamptz null,   -- revogação instantânea
expires_at timestamptz null,   -- NULL = não expira (caso XC_VM)
max_connections int null,
allowed_cidrs inet[] null,
allowed_category_ids bigint[] null,
rate_limit_rps int null,
last_used_at, use_count bigint, bytes_served bigint,
created_by bigint, created_at, updated_at
```
- A senha em claro é exibida **uma única vez**, no momento da criação, e nunca mais.
- Sem relação, chave estrangeira ou caminho de código com `source_credentials`.
- Cache in-process com TTL ≤ 5 s + invalidação por broadcast na escrita: revogar tem
  efeito em segundos sem consultar o banco a cada byte servido.
- Revogar também derruba as sessões de `streams` abertas com aquela credencial.

### `contents`
```
id, type ('movie'|'series'), title, original_title, normalized_title,
year int null, tmdb_id text null, imdb_id text null,
poster_url, backdrop_url, plot, rating numeric, duration_seconds int null,
status ('active'|'orphan'|'quarantine'|'archived'|'deleted'),
preserved bool default false,
primary_variant_id null, secondary_variant_id null, tertiary_variant_id null,
created_at, updated_at
```
Índices: `gin (normalized_title gin_trgm_ops)`, `(tmdb_id)`, `(imdb_id)`,
`(normalized_title, year)`, `(status)`.

As três colunas de prioridade são **overrides manuais**. `null` = usar a ordem herdada
da prioridade da fonte. Nunca são escritas por processo automático.

### `seasons` / `episodes`
```
seasons:  id, series_content_id, season_number, title, poster_url
episodes: id, season_id, episode_number, title, plot, duration_seconds,
          status, preserved, primary_variant_id, secondary_variant_id, tertiary_variant_id
```
Unique: `(series_content_id, season_number)`, `(season_id, episode_number)`.

### `source_variants` — o coração do modelo
```
id, source_id,
target_kind ('content'|'episode'), target_id bigint,
external_id text,             -- id do item na fonte (stream_id do Xtream, etc.)
origin_url text,              -- URL BRUTA DA FONTE. Nunca exposta a cliente.
container_ext text,           -- mp4 | mkv | ts (declarado pela fonte)
raw_payload jsonb,            -- resposta original do provider, para auditoria
declared_title text, declared_group text,
enabled bool default true,
available bool default true,
first_seen_at, last_seen_at, missing_since timestamptz null, missing_count int,
created_at, updated_at
```
Unique: `(source_id, external_id)` quando `external_id` não é nulo; senão
`(source_id, md5(origin_url))`.
Índices: `(target_kind, target_id)`, `(source_id, available)`, `(missing_since)`.

> `origin_url` fica fora de qualquer view usada pelas APIs públicas. Só a API admin,
> em endpoint dedicado ("Ver URL Original"), retorna esse campo — e o acesso é logado.

### `cache_entries`
```
id, variant_id, storage_provider_id, object_key text,
state ('pending'|'filling'|'complete'|'failed'|'evicted'|'archived'),
layout ('linear'|'chunked') default 'linear',
size_bytes bigint null,        -- Content-Length declarado, se houver
bytes_written bigint,          -- progresso do fill
content_type text, etag text, origin_supports_range bool,
checksum text null,            -- sha256 calculado no fill (barato, streaming)
created_at, completed_at, last_access_at, access_count bigint,
pinned bool default false,     -- "Não remover este conteúdo do cache"
error_message text
```
Unique parcial: um único entry em estado `filling`/`complete` por `variant_id`.
Índices para eviction: `(state, pinned, last_access_at)`, `(state, access_count)`.

### `download_jobs`
```
id, variant_id, cache_entry_id, trigger ('client'|'manual'|'prefetch'),
state ('queued'|'running'|'succeeded'|'failed'|'canceled'),
bytes_total, bytes_done, speed_bps, readers_count,
attempt int, last_error text,
started_at, finished_at, created_at
```

### `storage_providers`
```
id, name, kind ('local'|'gdrive'|'s3'), config jsonb, config_enc bytea,
priority int, enabled bool, read_only bool,
capacity_bytes, used_bytes, free_bytes, reserved_bytes,
status, last_check_at
```
`reserved_bytes` = espaço já prometido a downloads em andamento (admission control).

### `streams` (sessões de reprodução)
```
id, content_id null, episode_id null, variant_id null, cache_entry_id null,
client_ip inet, user_agent, auth_subject text,
cache_result ('hit'|'miss'|'passthrough'),
state ('active'|'closed'|'error'),
bytes_sent bigint, ttfb_ms int, time_to_playback_ms int null,
started_at, ended_at, error_code text
```
Linhas ativas ficam quentes; um job move sessões encerradas para `streams_history`
particionada por dia depois de 24h. Evita inchar a tabela do monitoramento em tempo real.

### Lifecycle
```
orphan_records:     id, content_id|episode_id, disappeared_at, previous_source_ids bigint[],
                    last_used_variant_id, size_bytes, current_location, state
quarantine_entries: id, orphan_record_id, entered_at, expires_at, policy jsonb, resolution
archive_entries:    id, orphan_record_id, storage_provider_id, object_key,
                    archived_at, restored_at null, size_bytes
```

### `match_decisions` (auditoria + travas)
```
id, actor ('auto'|'admin'), variant_id, content_id|episode_id,
confidence numeric, signals jsonb, decision ('grouped'|'rejected'|'pending_review'),
locked bool, created_at, note
```

### `events` (log estruturado)
```
id, ts, level, category, source_id, content_id, variant_id, stream_id,
message, data jsonb
```
Particionada por dia, com retenção configurável. Categorias conforme a seção 42 do
documento original.

---

## 3. Contadores quentes ficam fora das tabelas principais

`content_stats(content_id, access_count, last_access_at, bytes_served)` e os contadores
de `cache_entries` recebem escrita a cada request. Para não gerar bloat/vacuum no
catálogo, o `edge` **não** faz `UPDATE` síncrono: ele emite eventos para um canal
in-process, e um agregador aplica `UPDATE`s em lote a cada N segundos.

**Nenhuma escrita no Postgres acontece dentro do caminho de bytes.**

---

## 4. Separação origem × público (requisito 41)

| Campo | Onde vive | Quem vê |
|---|---|---|
| `source_variants.origin_url` | banco, coluna dedicada | só admin, endpoint específico, com log |
| URL pública | **não é armazenada** — é derivada | qualquer cliente autorizado |

A URL pública é derivada em tempo de geração:
`/stream/{content_id}` (+ assinatura) ou `/movie/{user}/{pass}/{id}.{ext}`.
Não guardamos URL pública em tabela porque ela é função de (id, credencial, tempo) —
armazenar criaria estado a invalidar.
