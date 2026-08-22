-- Apelidos de categoria: o nome que sempre cai numa pasta, venha de onde vier.
--
-- # O problema, exatamente como ele aparece
--
-- O administrador une "Vídeos 44" à pasta "Novelas". A união move o conteúdo e apaga a
-- pasta "Vídeos 44" — e daí em diante a categoria simplesmente não existe mais em lugar
-- nenhum. Some da tela, some do banco.
--
-- Aí a fonte sincroniza de novo. Ela continua declarando "Vídeos 44", porque nada mudou do
-- lado dela. O sistema não conhece esse nome, e a categoria volta: ou como pendência
-- esperando decisão, ou como conteúdo sem pasta. A decisão foi tomada, o trabalho foi
-- feito, e mesmo assim ele volta na sincronização seguinte.
--
-- Havia uma trava parcial: a união reapontava as linhas de source_categories que já
-- estavam vinculadas à pasta apagada. Só que a categoria unida normalmente NÃO está
-- vinculada a nada — é justamente por isso que ela está sendo unida. A linha dela em
-- source_categories tem category_id nulo, o UPDATE não a alcança, e ela continua pendente.
--
-- # A solução
--
-- Guardar o nome. Um apelido diz "toda categoria chamada assim, deste tipo, pertence a
-- esta pasta" — e vale para QUALQUER fonte, inclusive as que ainda não existem. É a
-- diferença entre decidir por fonte e decidir por nome, e é o que faz a decisão sobreviver
-- ao desaparecimento da categoria de origem.
--
-- # Por que uma tabela, e não uma coluna
--
-- Porque a categoria de origem deixa de existir. Não há linha onde pendurar a informação.
-- E porque a lista de apelidos é uma tela: o administrador precisa ver o que uniu, e poder
-- voltar atrás — reativar o nome como pasta própria, ou simplesmente soltá-lo para decidir
-- de novo. Sem esta tabela, unir é irreversível, e uma ação irreversível que se toma às
-- dezenas é uma armadilha.
CREATE TABLE category_aliases (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- A chave de busca é a mesma que a sincronização usa em todo o resto: nome
    -- normalizado + tipo. Um apelido de filme não pode capturar uma categoria de série.
    normalized_name text        NOT NULL,
    content_type    text        NOT NULL CHECK (content_type IN ('movie', 'series')),
    -- Para onde o nome aponta. ON DELETE CASCADE porque um apelido para uma pasta que não
    -- existe mais não tem sentido nenhum: seria um vínculo para o vazio.
    category_id     bigint      NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    -- O nome como a fonte escreve, guardado para a tela e para a reativação: normalizado
    -- não se mostra a ninguém, e recriar a pasta a partir dele daria um nome torto.
    declared_name   text        NOT NULL DEFAULT '',
    -- 'uniao'  — veio de unir uma pasta a outra;
    -- 'vinculo'— veio de resolver uma pendência escolhendo o destino.
    origem          text        NOT NULL DEFAULT 'uniao'
                                CHECK (origem IN ('uniao', 'vinculo')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Um nome aponta para um lugar só. Duas respostas para a mesma pergunta fariam a
    -- categoria cair em pastas diferentes conforme a ordem da leitura.
    UNIQUE (normalized_name, content_type)
);

CREATE INDEX category_aliases_destino_idx ON category_aliases (category_id);

COMMENT ON TABLE category_aliases IS
    'Nome de categoria que sempre cai numa pasta, em qualquer fonte. Sobrevive ao desaparecimento da categoria de origem, e é desfazível.';

-- As decisões que já foram tomadas viram apelidos.
--
-- Sem isto, a tabela nasce vazia e tudo o que o administrador já vinculou continua valendo
-- só para a fonte em que foi decidido — uma fonte nova declarando o mesmo nome pediria a
-- mesma decisão de novo, que é o incômodo que esta migração existe para acabar.
--
-- ON CONFLICT DO NOTHING resolve o caso de duas fontes terem mandado o mesmo nome para
-- pastas diferentes: a primeira decisão vira o apelido, e a outra continua valendo para a
-- fonte dela, porque o vínculo por fonte é consultado ANTES do apelido.
INSERT INTO category_aliases (normalized_name, content_type, category_id, declared_name, origem)
SELECT DISTINCT ON (sc.normalized_name, sc.content_type)
       sc.normalized_name, sc.content_type, sc.category_id, sc.declared_name, 'vinculo'
FROM source_categories sc
JOIN categories c ON c.id = sc.category_id
WHERE sc.category_id IS NOT NULL
  AND sc.content_type IN ('movie', 'series')
ORDER BY sc.normalized_name, sc.content_type, sc.first_seen_at
ON CONFLICT (normalized_name, content_type) DO NOTHING;
