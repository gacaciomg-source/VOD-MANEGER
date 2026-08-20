-- Fase 1 — esquema inicial.
-- Só entram tabelas que a Fase 1 realmente usa. As demais (contents, source_variants,
-- cache_entries, stream_credentials, ...) entram na fase que as utiliza.
-- Ver docs/02-modelo-de-dados.md.

-- ---------------------------------------------------------------------------
-- Usuários administrativos e autenticação
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      text        NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    role          text        NOT NULL DEFAULT 'admin'
                              CHECK (role IN ('admin', 'operator', 'viewer')),
    enabled       boolean     NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_username_nao_vazio CHECK (length(btrim(username)) > 0)
);

-- Sessões de painel. Guardamos apenas o SHA-256 do token; o valor em claro vive só
-- no cookie do administrador.
CREATE TABLE sessions (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL UNIQUE,
    user_agent   text,
    client_ip    text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    CONSTRAINT sessions_token_hash_sha256 CHECK (octet_length(token_hash) = 32)
);
CREATE INDEX sessions_user_id_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- Tokens de API para automação. Mesma regra: só o hash é persistido.
CREATE TABLE api_tokens (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text        NOT NULL,
    prefix       text        NOT NULL,
    token_hash   bytea       NOT NULL UNIQUE,
    enabled      boolean     NOT NULL DEFAULT true,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT api_tokens_token_hash_sha256 CHECK (octet_length(token_hash) = 32)
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

-- ---------------------------------------------------------------------------
-- Configuração persistida
-- ---------------------------------------------------------------------------

CREATE TABLE settings (
    key        text        PRIMARY KEY,
    value      jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Log estruturado de eventos de negócio (doc 03, seção 42 do documento original)
-- node_id já entra aqui para que a separação Manager/Nodes não exija migrar histórico.
-- ---------------------------------------------------------------------------

CREATE TABLE events (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts        timestamptz NOT NULL DEFAULT now(),
    node_id   text        NOT NULL,
    level     text        NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    category  text        NOT NULL,
    message   text        NOT NULL,
    actor     text,
    source_id bigint,
    data      jsonb       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX events_ts_idx           ON events (ts DESC);
CREATE INDEX events_category_ts_idx  ON events (category, ts DESC);
CREATE INDEX events_source_id_ts_idx ON events (source_id, ts DESC) WHERE source_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Fontes
-- ---------------------------------------------------------------------------

CREATE TABLE sources (
    id                       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                     text        NOT NULL UNIQUE,
    description              text        NOT NULL DEFAULT '',
    kind                     text        NOT NULL CHECK (kind IN ('m3u', 'xtream')),
    base_url                 text        NOT NULL,
    priority                 integer     NOT NULL DEFAULT 100,
    enabled                  boolean     NOT NULL DEFAULT true,
    status                   text        NOT NULL DEFAULT 'unknown'
                                         CHECK (status IN ('unknown', 'ok', 'degraded', 'down', 'disabled')),
    sync_interval_minutes    integer     NOT NULL DEFAULT 1440,
    max_connections          integer     NOT NULL DEFAULT 4,
    max_concurrent_downloads integer     NOT NULL DEFAULT 2,
    max_bandwidth_bps        bigint,
    allowed_categories       text[]      NOT NULL DEFAULT '{}',
    ignored_categories       text[]      NOT NULL DEFAULT '{}',
    last_sync_at             timestamptz,
    last_success_at          timestamptz,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sources_name_nao_vazio       CHECK (length(btrim(name)) > 0),
    CONSTRAINT sources_base_url_nao_vazia   CHECK (length(btrim(base_url)) > 0),
    CONSTRAINT sources_priority_positiva    CHECK (priority > 0),
    CONSTRAINT sources_sync_interval_valido CHECK (sync_interval_minutes > 0),
    CONSTRAINT sources_max_connections      CHECK (max_connections > 0),
    CONSTRAINT sources_max_downloads        CHECK (max_concurrent_downloads > 0),
    -- Baixar exige uma conexão: o limite de downloads não pode superar o de conexões.
    CONSTRAINT sources_downloads_ate_conexoes CHECK (max_concurrent_downloads <= max_connections),
    CONSTRAINT sources_bandwidth_positiva   CHECK (max_bandwidth_bps IS NULL OR max_bandwidth_bps > 0)
);
CREATE INDEX sources_priority_idx ON sources (priority, id);
CREATE INDEX sources_enabled_idx  ON sources (enabled) WHERE enabled;

-- Credenciais DE ENTRADA (usadas por nós para falar com a fonte). Cifradas com AES-GCM.
-- Nunca são serializadas em nenhuma resposta de API — nem para administrador.
-- Não confundir com stream_credentials (saída, D7), que entra na Fase 7.
CREATE TABLE source_credentials (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id   bigint      NOT NULL UNIQUE REFERENCES sources(id) ON DELETE CASCADE,
    username    text        NOT NULL DEFAULT '',
    secret_enc  bytea       NOT NULL,
    key_version integer     NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT source_credentials_secret_nao_vazio CHECK (octet_length(secret_enc) > 0)
);
