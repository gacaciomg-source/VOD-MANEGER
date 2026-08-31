-- A validade da assinatura em cada fonte.
--
-- # O dia que isto custou
--
-- Uma fonte venceu e o sistema não percebeu. Ela continuou aceitando conexões, respondendo
-- HTTP 200, e entregando — no lugar de cada filme — mil e seiscentos bytes com a frase "sua
-- lista está expirada".
--
-- Do lado do sistema, tudo parecia normal: conexão aceita, resposta bem-sucedida, conteúdo
-- recebido. O cache gravou aquilo como se fossem filmes. Do lado de quem assiste, todo
-- conteúdo daquela fonte abria com zero segundos.
--
-- Nada nos registros apontava para a causa, porque a causa não é um erro: é um contrato que
-- acabou. E é uma informação que a fonte SEMPRE dá, no `user_info` do player_api.
--
-- # Por que guardar, e não só conferir na hora
--
-- Conferir na hora responde "está vencida?". Guardar responde "quando vence?" — que é a
-- pergunta útil, porque permite avisar ANTES. Uma fonte que vence amanhã é um aviso a tempo;
-- uma que venceu ontem já é um dia inteiro de conteúdo quebrado e clientes reclamando.

ALTER TABLE sources
    ADD COLUMN assinatura_expira_em timestamptz,
    ADD COLUMN assinatura_status    text NOT NULL DEFAULT '',
    ADD COLUMN assinatura_vista_em  timestamptz;

COMMENT ON COLUMN sources.assinatura_expira_em IS
    'quando a assinatura nesta fonte vence, conforme ela mesma informa; nulo é "não informado"';
COMMENT ON COLUMN sources.assinatura_status IS
    'o status que a fonte declara sobre a conta: Active, Expired, Banned...';
COMMENT ON COLUMN sources.assinatura_vista_em IS
    'quando esta informação foi lida pela última vez; sem ela não há como saber se está velha';
