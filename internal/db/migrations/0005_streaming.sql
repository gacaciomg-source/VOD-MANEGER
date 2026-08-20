-- Fase 6/7 — camada de SAÍDA: como o mundo pede vídeo ao VOD Manager.
--
-- Ver docs/01 §6 D7 (credencial estável e revogável) e docs/02.

-- ---------------------------------------------------------------------------
-- Credenciais de SAÍDA
--
-- É o que o XC_VM (ou um player) usa para falar CONOSCO. Não tem nenhuma relação com
-- source_credentials, que é o que usamos para falar com as fontes: tabelas distintas,
-- finalidades distintas, sem caminho de código entre elas.
-- ---------------------------------------------------------------------------

CREATE TABLE stream_credentials (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          text        NOT NULL,
    description   text        NOT NULL DEFAULT '',

    username      text        NOT NULL UNIQUE,
    -- HMAC-SHA256(chave_do_servidor, senha). A senha em claro é exibida UMA vez, na
    -- criação, e nunca mais.
    --
    -- Não usamos Argon2 aqui de propósito: esta verificação roda a CADA requisição de
    -- vídeo e precisa ser O(1) barata. O segredo tem 256 bits gerados pela máquina, então
    -- não há senha fraca a proteger contra força bruta offline.
    password_hmac bytea       NOT NULL,
    key_version   integer     NOT NULL DEFAULT 1,

    enabled       boolean     NOT NULL DEFAULT true,
    -- Revogação instantânea. A verificação consulta estas colunas a cada uso.
    revoked_at    timestamptz,
    -- NULL = não expira. É o caso do XC_VM, que armazena o link.
    expires_at    timestamptz,

    max_connections integer,
    allowed_cidrs   text[]    NOT NULL DEFAULT '{}',

    last_used_at  timestamptz,
    use_count     bigint      NOT NULL DEFAULT 0,
    bytes_served  bigint      NOT NULL DEFAULT 0,

    created_by    bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT stream_credentials_username_nao_vazio CHECK (length(btrim(username)) > 0),
    CONSTRAINT stream_credentials_hmac_sha256        CHECK (octet_length(password_hmac) = 32),
    CONSTRAINT stream_credentials_max_conexoes       CHECK (max_connections IS NULL OR max_connections > 0)
);
CREATE INDEX stream_credentials_ativas_idx ON stream_credentials (username)
    WHERE enabled AND revoked_at IS NULL;

COMMENT ON TABLE stream_credentials IS
    'Autenticação de SAÍDA (D7): o que o XC_VM usa para pedir vídeo. Sem relação com source_credentials.';

-- ---------------------------------------------------------------------------
-- Sessões de reprodução
--
-- Uma linha por requisição de vídeo atendida. É a base do monitoramento em tempo real e
-- da contabilidade de banda.
-- ---------------------------------------------------------------------------

CREATE TABLE streams (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    node_id       text        NOT NULL DEFAULT '',

    content_id    bigint      REFERENCES contents(id) ON DELETE SET NULL,
    episode_id    bigint      REFERENCES episodes(id) ON DELETE SET NULL,
    variant_id    bigint      REFERENCES source_variants(id) ON DELETE SET NULL,
    source_id     bigint      REFERENCES sources(id) ON DELETE SET NULL,
    credential_id bigint      REFERENCES stream_credentials(id) ON DELETE SET NULL,

    client_ip     text        NOT NULL DEFAULT '',
    user_agent    text        NOT NULL DEFAULT '',

    -- Como o conteúdo foi servido. Enquanto o cache não existe, tudo é 'passthrough'.
    cache_result  text        NOT NULL DEFAULT 'passthrough'
                              CHECK (cache_result IN ('hit', 'miss', 'passthrough')),
    state         text        NOT NULL DEFAULT 'active'
                              CHECK (state IN ('active', 'closed', 'error')),

    bytes_sent    bigint      NOT NULL DEFAULT 0,
    -- Tempo até o primeiro byte sair para o cliente: a métrica central de latência.
    ttfb_ms       integer,
    range_header  text        NOT NULL DEFAULT '',
    status_code   integer,
    error_code    text        NOT NULL DEFAULT '',
    -- Quantas variantes foram tentadas antes de uma responder.
    attempts      integer     NOT NULL DEFAULT 1,

    started_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz
);
CREATE INDEX streams_ativos_idx    ON streams (started_at DESC) WHERE state = 'active';
CREATE INDEX streams_started_idx   ON streams (started_at DESC);
CREATE INDEX streams_content_idx   ON streams (content_id) WHERE content_id IS NOT NULL;
CREATE INDEX streams_variant_idx   ON streams (variant_id) WHERE variant_id IS NOT NULL;

-- Índices para as colunas de FK usadas em exclusão em cascata — o mesmo problema que
-- travou a exclusão de fontes na migração 0004.
CREATE INDEX streams_source_idx     ON streams (source_id)     WHERE source_id IS NOT NULL;
CREATE INDEX streams_episode_idx    ON streams (episode_id)    WHERE episode_id IS NOT NULL;
CREATE INDEX streams_credential_idx ON streams (credential_id) WHERE credential_id IS NOT NULL;
