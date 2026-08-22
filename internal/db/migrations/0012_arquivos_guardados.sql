-- Arquivos guardados: o acervo que fica NESTA operação, e não na fonte.
--
-- # O que muda com isto
--
-- Até aqui o sistema nunca guardou vídeo. Ele intermedia: o espectador pede, o servidor
-- puxa da fonte e repassa. É simples, não ocupa disco, e tem duas consequências que só
-- aparecem com audiência — cada byte entregue é um byte comprado da fonte, e o dia em que
-- a fonte tira o filme do ar, ele some para todo mundo.
--
-- Esta tabela é o registro de uma CÓPIA. A partir dela, um conteúdo pode ser servido do
-- disco local ou da nuvem em vez da fonte.
--
-- # As duas origens, e por que elas não podem se misturar
--
-- 'fonte'  — cópia de algo que veio de uma fonte. É cache: existe para economizar banda e
--            acelerar. Pode ser apagada a qualquer momento, porque a fonte ainda tem o
--            original. Se apagarmos por engano, o custo é uma releitura.
--
-- 'proprio'— arquivo que o administrador colocou aqui. NÃO existe em lugar nenhum além
--            desta máquina. Apagar é perda definitiva.
--
-- Um único mecanismo de limpeza tratando as duas do mesmo jeito acabaria apagando acervo
-- próprio para liberar espaço — e ninguém perceberia até alguém tentar assistir. Por isso a
-- coluna existe, e por isso a limpeza automática NUNCA toca em 'proprio': o que ela faz com
-- esses é pedir uma decisão ao administrador.
CREATE TABLE arquivos_guardados (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- A variante que este arquivo copia. Nula para acervo próprio, que não veio de fonte
    -- nenhuma. ON DELETE CASCADE: a cópia de uma variante que deixou de existir é lixo.
    variant_id  bigint      REFERENCES source_variants(id) ON DELETE CASCADE,

    -- O conteúdo a que o arquivo pertence. Redundante com a variante no caso do cache, e
    -- indispensável no caso do acervo próprio — sem isto, um upload não teria a que se
    -- ligar.
    target_kind text        NOT NULL CHECK (target_kind IN ('content', 'episode')),
    target_id   bigint      NOT NULL,

    -- Onde o arquivo está. O nome do backend, e o endereço dele DENTRO daquele backend:
    -- um caminho no disco, um id de arquivo no Drive. O formato do localizador é assunto
    -- do backend, e o resto do sistema não o interpreta.
    backend     text        NOT NULL CHECK (backend IN ('local', 'gdrive')),
    localizador text        NOT NULL DEFAULT '',

    bytes          bigint   NOT NULL DEFAULT 0,
    -- bytes_baixados acompanha o progresso enquanto o estado é 'baixando'. Serve para a
    -- tela mostrar quanto falta, e para retomar de onde parou.
    bytes_baixados bigint   NOT NULL DEFAULT 0,
    -- bytes_totais é o tamanho ANUNCIADO pela origem, quando ela anuncia. Separado de
    -- `bytes` porque este é o tamanho real do que já está guardado: enquanto o download
    -- corre, os dois divergem, e é dessa diferença que sai a porcentagem.
    bytes_totais   bigint,
    container_ext  text     NOT NULL DEFAULT '',

    -- 'pendente'  — na fila, ninguém começou
    -- 'baixando'  — em andamento
    -- 'pronto'    — pode ser servido
    -- 'erro'      — falhou; `erro` diz o motivo
    -- 'removendo' — marcado para apagar, ainda não apagado do backend
    estado      text        NOT NULL DEFAULT 'pendente'
                            CHECK (estado IN ('pendente', 'baixando', 'pronto', 'erro', 'removendo')),
    erro        text        NOT NULL DEFAULT '',

    origem      text        NOT NULL CHECK (origem IN ('fonte', 'proprio')),

    -- protegido tira o arquivo da limpeza automática, mesmo sendo cache.
    --
    -- Existe para o caso concreto de um filme que a fonte tirou do ar: a cópia deixou de
    -- ser conveniência e virou a única que existe. Sem esta coluna, a limpeza apagaria
    -- exatamente o arquivo que passou a ser insubstituível.
    protegido   boolean     NOT NULL DEFAULT false,

    -- O que a limpeza usa para decidir. Guardados aqui, e não derivados dos eventos de
    -- reprodução, porque a decisão precisa ser barata: varrer o histórico de acessos de
    -- um acervo inteiro a cada limpeza custaria mais que o espaço economizado.
    acessos          bigint      NOT NULL DEFAULT 0,
    ultimo_acesso_em timestamptz,

    criado_em    timestamptz NOT NULL DEFAULT now(),
    concluido_em timestamptz,

    CONSTRAINT arquivos_proprio_sem_variante
        -- Acervo próprio não pode apontar para uma variante de fonte: seria dizer que o
        -- arquivo veio de um lugar de onde ele não veio, e a limpeza confiaria nisso.
        CHECK (origem = 'fonte' OR variant_id IS NULL),
    CONSTRAINT arquivos_pronto_tem_localizador
        CHECK (estado <> 'pronto' OR localizador <> '')
);

-- Uma cópia por variante. Duas cópias do mesmo vídeo é espaço gasto duas vezes, e a
-- segunda nunca seria escolhida.
CREATE UNIQUE INDEX arquivos_guardados_variante_uq ON arquivos_guardados (variant_id)
    WHERE variant_id IS NOT NULL;

-- A consulta do caminho quente: "existe cópia pronta para esta variante?". Ela roda a cada
-- pedido de reprodução, antes de decidir entre servir do disco ou puxar da fonte.
CREATE INDEX arquivos_guardados_prontos_idx ON arquivos_guardados (variant_id)
    WHERE estado = 'pronto';

-- A tela de acervo e a resolução de reprodução do conteúdo próprio.
CREATE INDEX arquivos_guardados_alvo_idx ON arquivos_guardados (target_kind, target_id);

-- A fila de trabalho do baixador.
CREATE INDEX arquivos_guardados_fila_idx ON arquivos_guardados (criado_em)
    WHERE estado IN ('pendente', 'baixando');

-- A ordem em que a limpeza escolhe o que apagar: o menos usado há mais tempo, primeiro.
-- Só cache desprotegido entra — acervo próprio nunca é apagado sozinho.
CREATE INDEX arquivos_guardados_limpeza_idx ON arquivos_guardados (ultimo_acesso_em NULLS FIRST, acessos)
    WHERE estado = 'pronto' AND origem = 'fonte' AND NOT protegido;

COMMENT ON TABLE arquivos_guardados IS
    'Cópias de mídia guardadas por esta operação: cache de fontes (descartável) e acervo próprio (insubstituível). A limpeza automática só toca no primeiro.';

-- ---------------------------------------------------------------------------
-- Guardar é decisão POR FONTE, não do sistema inteiro.
--
-- Fontes não são iguais. Uma cobra por banda e vale a pena copiar; outra é rápida e barata
-- e não vale o disco. Uma tem acervo estável, outra troca de link toda semana e a cópia
-- envelheceria mal.
--
-- O padrão é false, e é uma escolha: ligar o cache é uma decisão sobre custo de disco, e
-- ninguém deve descobrir que ela foi tomada por ele ao ver a partição cheia.
--
-- Há ainda uma chave geral, nas configurações, que desliga tudo de uma vez. As duas
-- precisam estar ligadas para uma cópia acontecer — a geral existe para poder parar o
-- sistema inteiro sem ter de lembrar de cada fonte que foi marcada meses atrás.
ALTER TABLE sources
    ADD COLUMN cache_habilitado boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN sources.cache_habilitado IS
    'Se o conteúdo desta fonte pode ser copiado para o armazenamento local ou na nuvem. Exige também a chave geral ligada.';
