-- Fase 2/3 — catálogo: Content, SourceVariant, séries, categorias, sync e matching.
-- Ver docs/02-modelo-de-dados.md e docs/07-contrato-normalizado.md.

-- Similaridade de título para geração barata de candidatos no matching.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------------------------------------------------------------------------
-- Estado de sincronização da fonte
-- ---------------------------------------------------------------------------

ALTER TABLE sources ADD COLUMN sync_state jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE sources ADD COLUMN request_budget integer NOT NULL DEFAULT 5000
    CHECK (request_budget > 0);
-- Tolerância antes de marcar um item como indisponível por não aparecer no sync.
ALTER TABLE sources ADD COLUMN missing_tolerance integer NOT NULL DEFAULT 2
    CHECK (missing_tolerance >= 0);

-- ---------------------------------------------------------------------------
-- Categorias
-- ---------------------------------------------------------------------------

-- Categoria canônica do catálogo (o que o cliente final vê).
CREATE TABLE categories (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            text        NOT NULL,
    normalized_name text        NOT NULL,
    content_type    text        NOT NULL DEFAULT 'unknown'
                                CHECK (content_type IN ('movie', 'series', 'unknown')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (normalized_name, content_type)
);

-- Categoria como a FONTE a declara. O mapeamento para a canônica é editável.
CREATE TABLE source_categories (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id       bigint      NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    external_id     text        NOT NULL DEFAULT '',
    declared_name   text        NOT NULL,
    normalized_name text        NOT NULL,
    content_type    text        NOT NULL DEFAULT 'unknown'
                                CHECK (content_type IN ('movie', 'series', 'unknown')),
    category_id     bigint      REFERENCES categories(id) ON DELETE SET NULL,
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, content_type, normalized_name)
);
CREATE INDEX source_categories_source_idx ON source_categories (source_id);

-- ---------------------------------------------------------------------------
-- Conteúdo lógico
-- ---------------------------------------------------------------------------

CREATE TABLE contents (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type             text        NOT NULL CHECK (type IN ('movie', 'series')),
    title            text        NOT NULL,
    normalized_title text        NOT NULL,
    year             integer,
    tmdb_id          text,
    imdb_id          text,
    poster_url       text        NOT NULL DEFAULT '',
    backdrop_url     text        NOT NULL DEFAULT '',
    plot             text        NOT NULL DEFAULT '',
    rating           numeric(4,2),
    duration_seconds integer,
    category_id      bigint      REFERENCES categories(id) ON DELETE SET NULL,
    status           text        NOT NULL DEFAULT 'active'
                                 CHECK (status IN ('active', 'orphan', 'quarantine', 'archived', 'deleted')),
    preserved        boolean     NOT NULL DEFAULT false,
    -- Overrides MANUAIS de prioridade. Nunca escritos por processo automático.
    primary_variant_id   bigint,
    secondary_variant_id bigint,
    tertiary_variant_id  bigint,
    access_count     bigint      NOT NULL DEFAULT 0,
    last_access_at   timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contents_year_plausivel CHECK (year IS NULL OR (year BETWEEN 1888 AND 2200))
);
CREATE INDEX contents_normalized_title_trgm ON contents USING gin (normalized_title gin_trgm_ops);
CREATE INDEX contents_title_year_idx ON contents (normalized_title, year);
CREATE INDEX contents_tmdb_idx   ON contents (tmdb_id) WHERE tmdb_id IS NOT NULL;
CREATE INDEX contents_imdb_idx   ON contents (imdb_id) WHERE imdb_id IS NOT NULL;
CREATE INDEX contents_status_idx ON contents (status);
CREATE INDEX contents_type_idx   ON contents (type);

CREATE TABLE seasons (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    series_content_id bigint      NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    season_number     integer     NOT NULL CHECK (season_number >= 0),
    title             text        NOT NULL DEFAULT '',
    poster_url        text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (series_content_id, season_number)
);

CREATE TABLE episodes (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    season_id        bigint      NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    episode_number   integer     NOT NULL CHECK (episode_number >= 0),
    title            text        NOT NULL DEFAULT '',
    plot             text        NOT NULL DEFAULT '',
    duration_seconds integer,
    poster_url       text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'active'
                                 CHECK (status IN ('active', 'orphan', 'quarantine', 'archived', 'deleted')),
    preserved        boolean     NOT NULL DEFAULT false,
    primary_variant_id   bigint,
    secondary_variant_id bigint,
    tertiary_variant_id  bigint,
    access_count     bigint      NOT NULL DEFAULT 0,
    last_access_at   timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (season_id, episode_number)
);
CREATE INDEX episodes_season_idx ON episodes (season_id);

-- ---------------------------------------------------------------------------
-- Variantes: uma origem de um conteúdo
-- ---------------------------------------------------------------------------

CREATE TABLE source_variants (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id    bigint      NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    target_kind  text        NOT NULL CHECK (target_kind IN ('content', 'episode')),
    target_id    bigint      NOT NULL,

    -- Identidade estável (docs/07 §4.1): external_id tem precedência sobre url_hash.
    external_id  text        NOT NULL DEFAULT '',
    url_hash     text        NOT NULL DEFAULT '',

    -- origin_url NUNCA é exposta em API pública. Vazia para fontes que exigem
    -- materialização (Xtream) — nesse caso stream_ref carrega o necessário.
    origin_url   text        NOT NULL DEFAULT '',
    stream_ref   jsonb,

    container_ext   text     NOT NULL DEFAULT '',
    declared_title  text     NOT NULL DEFAULT '',
    declared_group  text     NOT NULL DEFAULT '',
    quality_tags    text[]   NOT NULL DEFAULT '{}',
    language_tags   text[]   NOT NULL DEFAULT '{}',
    raw_payload     jsonb    NOT NULL DEFAULT '{}'::jsonb,

    digest       text        NOT NULL DEFAULT '',
    enabled      boolean     NOT NULL DEFAULT true,
    available    boolean     NOT NULL DEFAULT true,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    missing_since timestamptz,
    missing_count integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT source_variants_tem_identidade
        CHECK (external_id <> '' OR url_hash <> ''),
    CONSTRAINT source_variants_tem_midia
        CHECK (origin_url <> '' OR stream_ref IS NOT NULL)
);
-- Uma variante por identidade por fonte. Índices parciais porque só uma das duas
-- colunas de identidade é usada por vez.
CREATE UNIQUE INDEX source_variants_external_uq ON source_variants (source_id, external_id)
    WHERE external_id <> '';
CREATE UNIQUE INDEX source_variants_urlhash_uq ON source_variants (source_id, url_hash)
    WHERE external_id = '' AND url_hash <> '';
CREATE INDEX source_variants_target_idx ON source_variants (target_kind, target_id);
CREATE INDEX source_variants_source_idx ON source_variants (source_id, available);
CREATE INDEX source_variants_missing_idx ON source_variants (missing_since)
    WHERE missing_since IS NOT NULL;

-- As colunas de prioridade só podem apontar para variantes existentes.
ALTER TABLE contents
    ADD CONSTRAINT contents_primary_variant_fk   FOREIGN KEY (primary_variant_id)   REFERENCES source_variants(id) ON DELETE SET NULL,
    ADD CONSTRAINT contents_secondary_variant_fk FOREIGN KEY (secondary_variant_id) REFERENCES source_variants(id) ON DELETE SET NULL,
    ADD CONSTRAINT contents_tertiary_variant_fk  FOREIGN KEY (tertiary_variant_id)  REFERENCES source_variants(id) ON DELETE SET NULL;

ALTER TABLE episodes
    ADD CONSTRAINT episodes_primary_variant_fk   FOREIGN KEY (primary_variant_id)   REFERENCES source_variants(id) ON DELETE SET NULL,
    ADD CONSTRAINT episodes_secondary_variant_fk FOREIGN KEY (secondary_variant_id) REFERENCES source_variants(id) ON DELETE SET NULL,
    ADD CONSTRAINT episodes_tertiary_variant_fk  FOREIGN KEY (tertiary_variant_id)  REFERENCES source_variants(id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------------
-- Fila de itens não resolvidos (docs/07 §4.3)
-- ---------------------------------------------------------------------------

CREATE TABLE unresolved_items (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id      bigint      NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    identity_key   text        NOT NULL,
    declared_title text        NOT NULL DEFAULT '',
    declared_group text        NOT NULL DEFAULT '',
    reason         text        NOT NULL,
    detail         text        NOT NULL DEFAULT '',
    raw_payload    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    occurrences    integer     NOT NULL DEFAULT 1,
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at    timestamptz,
    resolution     text,
    UNIQUE (source_id, identity_key)
);
CREATE INDEX unresolved_items_pendentes_idx ON unresolved_items (source_id, reason)
    WHERE resolved_at IS NULL;

-- ---------------------------------------------------------------------------
-- Decisões de matching (auditoria + trava manual)
-- ---------------------------------------------------------------------------

CREATE TABLE match_decisions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    variant_id  bigint      NOT NULL REFERENCES source_variants(id) ON DELETE CASCADE,
    target_kind text        NOT NULL CHECK (target_kind IN ('content', 'episode')),
    target_id   bigint      NOT NULL,
    actor       text        NOT NULL CHECK (actor IN ('auto', 'admin')),
    decision    text        NOT NULL CHECK (decision IN ('grouped', 'pending_review', 'rejected')),
    confidence  integer     NOT NULL DEFAULT 0,
    signals     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- locked = decisão do administrador. O algoritmo NUNCA a revisa (docs/07 §7.5).
    locked      boolean     NOT NULL DEFAULT false,
    note        text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX match_decisions_variant_uq ON match_decisions (variant_id);
CREATE INDEX match_decisions_pendentes_idx ON match_decisions (decision)
    WHERE decision = 'pending_review';

-- ---------------------------------------------------------------------------
-- Execuções de sincronização
-- ---------------------------------------------------------------------------

CREATE TABLE sync_runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id       bigint      NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    node_id         text        NOT NULL DEFAULT '',
    trigger         text        NOT NULL DEFAULT 'manual'
                                CHECK (trigger IN ('manual', 'scheduled', 'startup')),
    state           text        NOT NULL DEFAULT 'running'
                                CHECK (state IN ('running', 'succeeded', 'partial', 'failed', 'canceled')),
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    items_seen      integer     NOT NULL DEFAULT 0,
    items_new       integer     NOT NULL DEFAULT 0,
    items_updated   integer     NOT NULL DEFAULT 0,
    items_unchanged integer     NOT NULL DEFAULT 0,
    items_missing   integer     NOT NULL DEFAULT 0,
    items_rejected  integer     NOT NULL DEFAULT 0,
    requests_made   integer     NOT NULL DEFAULT 0,
    request_budget  integer     NOT NULL DEFAULT 0,
    error_message   text        NOT NULL DEFAULT '',
    stats           jsonb       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX sync_runs_source_idx ON sync_runs (source_id, started_at DESC);
-- Uma sincronização por fonte de cada vez: duas simultâneas dobrariam as conexões
-- à fonte e produziriam diffs conflitantes.
CREATE UNIQUE INDEX sync_runs_uma_ativa_por_fonte ON sync_runs (source_id)
    WHERE state = 'running';
