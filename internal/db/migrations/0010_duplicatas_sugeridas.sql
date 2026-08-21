-- Sugestões de conteúdo duplicado, e as decisões do administrador sobre elas.
--
-- # Por que sugerir em vez de agrupar
--
-- Fontes marcam o mesmo filme de formas diferentes: "Um Grande Despertar" e "Um Grande
-- Despertar Lançamento" são o mesmo conteúdo, com o mesmo ano e o mesmo cartaz. Agrupá-los
-- sozinho seria tentador — e errado como regra geral, porque um dia aparece um filme cujo
-- nome contém "Lançamento" de verdade.
--
-- Então o sistema aponta e o administrador decide. É a mesma escolha feita no matching de
-- títulos: sugerir com evidência, nunca decidir no lugar de quem conhece o acervo.
--
-- # O que esta tabela guarda
--
-- Apenas as decisões NEGATIVAS: "estes dois são diferentes, não me pergunte de novo".
--
-- As positivas não precisam de registro — quando o administrador confirma que são o mesmo,
-- os conteúdos são efetivamente unidos, e o par deixa de existir.
CREATE TABLE duplicatas_ignoradas (
    -- Sempre gravado com o MENOR id primeiro. Sem essa normalização, o mesmo par poderia
    -- ser gravado duas vezes em ordens diferentes e voltar a aparecer.
    conteudo_a bigint      NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    conteudo_b bigint      NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    decidido_por bigint    REFERENCES users(id) ON DELETE SET NULL,
    decidido_em timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (conteudo_a, conteudo_b),
    CONSTRAINT duplicatas_ordem CHECK (conteudo_a < conteudo_b)
);

COMMENT ON TABLE duplicatas_ignoradas IS
    'Pares que o administrador declarou serem conteúdos DIFERENTES. Existem para a sugestão não reaparecer a cada visita à tela.';
