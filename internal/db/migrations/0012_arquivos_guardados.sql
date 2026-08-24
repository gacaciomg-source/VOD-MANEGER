-- Acervo: o vídeo que fica NESTA operação, e não na fonte.
--
-- # O que muda com isto
--
-- Até aqui o sistema nunca guardou vídeo. Ele intermedia: o espectador pede, o servidor
-- puxa da fonte e repassa. É simples, não ocupa disco, e tem duas consequências que só
-- aparecem com audiência — cada byte entregue é um byte comprado da fonte, e o dia em que
-- a fonte tira o filme do ar, ele some para todo mundo.
--
-- Duas tabelas: onde guardar, e o que está guardado.

-- ---------------------------------------------------------------------------
-- Contas de nuvem
-- ---------------------------------------------------------------------------
--
-- Uma linha por conta, cadastrada pelo painel. Não há conta embutida no código nem no
-- ambiente.
--
-- # Por que várias, e não uma
--
-- Espaço em nuvem se compra por conta, não por terabyte: cinco contas de 5 TB custam menos
-- que uma de 25 TB, e às vezes a de 25 nem existe. Quem cresce, cresce somando contas — e
-- um sistema que suporta exatamente uma obriga a escolher entre migrar tudo ou parar de
-- guardar.
--
-- Some-se a isso que conta de nuvem é o recurso mais frágil desta lista: ela é suspensa,
-- ela enche, ela perde o token. Com várias, isso vira "esta parou, as outras seguem". Com
-- uma, vira "o acervo saiu do ar".
--
-- # Por que `provedor` é texto, e não uma coluna por serviço
--
-- Hoje é o Google Drive. Amanhã pode ser outro. O que muda entre eles é como falar — e
-- isso vive no código, numa implementação por provedor. O banco só precisa saber que
-- existe uma conta, de que tipo ela é, e onde estão as credenciais dela.
CREATE TABLE nuvens (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- O nome que o administrador deu. É como ele distingue sete contas iguais na tela —
    -- "Drive principal", "Drive filmes antigos". O sistema nunca o interpreta.
    nome     text        NOT NULL UNIQUE,
    provedor text        NOT NULL CHECK (provedor IN ('gdrive')),

    -- As credenciais, cifradas com a chave mestra, como as das fontes.
    --
    -- Um token de acesso à nuvem de alguém vale tanto quanto a senha da conta: ele lê,
    -- escreve e apaga tudo o que houver lá. Guardá-lo em claro seria transformar um dump
    -- de banco no roubo de cinco terabytes de acervo.
    credenciais_enc bytea   NOT NULL,
    key_version     integer NOT NULL DEFAULT 1,

    -- A pasta dentro da conta onde o acervo vive. O formato é assunto do provedor: no
    -- Drive é um id de pasta.
    --
    -- Confinar numa pasta não é organização — é limite de dano. Uma conta pessoal tem
    -- outras coisas dentro, e um erro nosso não pode alcançá-las.
    pasta_raiz text NOT NULL DEFAULT '',

    ativa boolean NOT NULL DEFAULT true,
    -- somente_leitura para de gravar SEM parar de servir.
    --
    -- É o estado de uma conta que encheu, e é o estado que evita o pior desfecho: sem ele,
    -- a única forma de parar as gravações numa conta cheia seria desativá-la — e isso
    -- derrubaria de uma vez todo o acervo que já está lá dentro.
    somente_leitura boolean NOT NULL DEFAULT false,

    -- A ordem de preenchimento. A primeira conta ativa com espaço recebe o próximo arquivo.
    --
    -- Previsível de propósito. "A que tem mais espaço" espalharia o acervo entre todas as
    -- contas, e aí perder uma conta significaria perder um pedaço de tudo, em vez de perder
    -- as coisas mais antigas.
    ordem integer NOT NULL DEFAULT 100,

    -- Última medição de cota, para a tela e para a decisão de onde gravar. Nulo = ainda
    -- não medido.
    bytes_usados  bigint,
    bytes_totais  bigint,
    medida_em     timestamptz,

    -- O último erro que esta conta deu. Uma conta que perdeu o token falha em toda
    -- gravação, e sem isto a única pista seria o registro do serviço.
    ultimo_erro    text        NOT NULL DEFAULT '',
    ultimo_erro_em timestamptz,

    criado_em timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT nuvens_nome_nao_vazio CHECK (length(btrim(nome)) > 0),
    CONSTRAINT nuvens_credenciais_nao_vazias CHECK (octet_length(credenciais_enc) > 0)
);
CREATE INDEX nuvens_ordem_idx ON nuvens (ordem, id) WHERE ativa;

COMMENT ON TABLE nuvens IS
    'Contas de armazenamento em nuvem cadastradas pelo painel. Várias por instalação, adicionáveis e removíveis; as credenciais são cifradas com a chave mestra.';

-- ---------------------------------------------------------------------------
-- Arquivos guardados
-- ---------------------------------------------------------------------------
--
-- O registro de uma CÓPIA. A partir dela, um conteúdo pode ser servido do disco local ou de
-- uma das contas de nuvem, em vez da fonte.
--
-- # As duas origens, e por que elas não podem se misturar
--
-- 'fonte'  — cópia de algo que veio de uma fonte. É cache: existe para economizar banda e
--            acelerar. Pode ser apagada a qualquer momento, porque a fonte ainda tem o
--            original. Se apagarmos por engano, o custo é uma releitura.
--
-- 'proprio'— arquivo que o administrador colocou aqui. NÃO existe em lugar nenhum além
--            deste acervo. Apagar é perda definitiva.
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

    -- Onde o arquivo está: no disco desta máquina, ou numa das contas de nuvem.
    --
    -- 'nuvem' em vez do nome do provedor, de propósito: assim acrescentar um provedor novo
    -- é uma linha na tabela `nuvens` e uma implementação no código, e não uma migração que
    -- altera um CHECK em cima de uma tabela com milhões de linhas.
    backend  text   NOT NULL CHECK (backend IN ('local', 'nuvem')),
    -- ON DELETE RESTRICT: apagar uma conta que ainda guarda arquivos deixaria linhas
    -- apontando para lugar nenhum, e o painel entregaria o link de um vídeo que não existe
    -- mais. Quem remove uma conta tem de decidir antes o que fazer com o que está dentro.
    nuvem_id bigint REFERENCES nuvens(id) ON DELETE RESTRICT,

    -- O endereço do arquivo DENTRO daquele backend: um nome no disco, um id no Drive. O
    -- formato é assunto do backend, e o resto do sistema não o interpreta.
    localizador text NOT NULL DEFAULT '',

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

    -- O acervo próprio TAMBÉM tem variante, e precisa ter.
    --
    -- A primeira versão desta tabela proibia isso, com um raciocínio que parecia certo:
    -- acervo próprio não veio de fonte nenhuma, então não deveria apontar para uma. Só que
    -- é a variante que torna um conteúdo REPRODUZÍVEL — é dela que saem os links da lista
    -- M3U e da API Xtream. Sem variante, um arquivo enviado pelo painel ficaria guardado e
    -- invisível: ninguém conseguiria assistir.
    --
    -- A variante do acervo próprio pertence a uma fonte interna, criada pelo sistema, que a
    -- sincronização nunca visita. Assim todo o resto do catálogo — categorias, prioridade,
    -- exportação, reprodução — funciona sem saber que aquele conteúdo é diferente.
    --
    -- E a proteção não se perde: quem decide o que a limpeza pode apagar é a coluna
    -- `origem`, não a presença de variante.
    CONSTRAINT arquivos_nuvem_tem_conta
        -- Sem isto, um arquivo poderia dizer "estou na nuvem" sem dizer em qual — e não
        -- haveria como encontrá-lo nem como saber que ele se perdeu.
        CHECK ((backend = 'nuvem') = (nuvem_id IS NOT NULL)),
    CONSTRAINT arquivos_pronto_tem_localizador
        CHECK (estado <> 'pronto' OR localizador <> '')
);

-- Uma cópia por variante. Duas cópias do mesmo vídeo é espaço gasto duas vezes, e a
-- segunda nunca seria escolhida.
CREATE UNIQUE INDEX arquivos_guardados_variante_uq ON arquivos_guardados (variant_id)
    WHERE variant_id IS NOT NULL;

-- A consulta do caminho quente: "existe cópia pronta para esta variante?". Ela roda a cada
-- pedido de reprodução, antes de decidir entre servir do acervo ou puxar da fonte.
CREATE INDEX arquivos_guardados_prontos_idx ON arquivos_guardados (variant_id)
    WHERE estado = 'pronto';

-- A tela de acervo e a resolução de reprodução do conteúdo próprio.
CREATE INDEX arquivos_guardados_alvo_idx ON arquivos_guardados (target_kind, target_id);

-- A fila de trabalho do baixador.
CREATE INDEX arquivos_guardados_fila_idx ON arquivos_guardados (criado_em)
    WHERE estado IN ('pendente', 'baixando');

-- "O que esta conta guarda?" — a pergunta de quem vai remover uma conta, e a de quem quer
-- saber quanto cada uma está ocupando.
CREATE INDEX arquivos_guardados_nuvem_idx ON arquivos_guardados (nuvem_id)
    WHERE nuvem_id IS NOT NULL;

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
    'Se o conteúdo desta fonte pode ser copiado para o acervo. Exige também a chave geral ligada.';

-- ---------------------------------------------------------------------------
-- A fonte interna do acervo próprio
-- ---------------------------------------------------------------------------
--
-- Um arquivo enviado pelo painel precisa virar conteúdo reproduzível, e no catálogo o que
-- torna algo reproduzível é a VARIANTE — é dela que saem os links da lista M3U e da API
-- Xtream. Uma variante pertence a uma fonte; logo, o acervo próprio precisa de uma.
--
-- A alternativa seria ensinar exportação, reprodução, categorias e prioridade a lidar com
-- "conteúdo sem fonte". Seriam quatro caminhos novos, cada um com o seu jeito de estar
-- errado, para representar o que a estrutura existente já representa bem.
--
-- 'proprio' é um tipo de fonte que a sincronização nunca visita: não há URL para consultar
-- nem catálogo para ler. Ela existe para ser dona das variantes do acervo próprio, e é isso.
ALTER TABLE sources DROP CONSTRAINT sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check
    CHECK (kind IN ('m3u', 'xtream', 'proprio'));

COMMENT ON COLUMN sources.kind IS
    'm3u e xtream são fontes externas, sincronizadas. proprio é a fonte interna do acervo enviado pelo painel: nunca é sincronizada, existe para ser dona das variantes desse conteúdo.';
