-- Versão de idioma como parte da identidade do conteúdo.
--
-- Em listas brasileiras, a versão dublada e a legendada da mesma obra ficam em
-- categorias diferentes e o espectador escolhe uma delas. Agrupá-las num único conteúdo
-- fazia a versão legendada desaparecer da categoria dela.
--
-- Qualidade (1080p, 4K) continua NÃO distinguindo: é a mesma obra, e o sistema deve
-- poder escolher a melhor fonte livremente.

ALTER TABLE contents ADD COLUMN language_key text NOT NULL DEFAULT '';

COMMENT ON COLUMN contents.language_key IS
    'Versão de áudio/legenda na forma canônica (leg, dub, dual, ...). Vazio = versão padrão da fonte.';

-- O índice de busca por título passa a considerar a versão: é a consulta que o matching
-- faz para gerar candidatos.
DROP INDEX IF EXISTS contents_title_year_idx;
CREATE INDEX contents_title_year_lang_idx ON contents (normalized_title, year, language_key);
