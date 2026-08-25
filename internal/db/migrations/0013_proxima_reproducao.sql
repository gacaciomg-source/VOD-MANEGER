-- Adiantar o próximo episódio.
--
-- # O comportamento que isto atende
--
-- Quem assiste série não escolhe um episódio: escolhe uma sequência. Terminado o 50, o 51
-- vem em seguida, quase sempre em segundos. E é exatamente aí que o cache é inútil da forma
-- como ele funciona sozinho — ele guarda o que JÁ foi assistido, então o episódio seguinte é
-- sempre o que ninguém tem.
--
-- Adiantando, a espera cai para o caso que importa: o espectador está no 50, e o 51 já está
-- no disco quando ele chegar lá.
--
-- # Por que uma coluna, e não uma tabela
--
-- O que se guarda é a mesma coisa — um arquivo no acervo, com o mesmo ciclo de vida. O que
-- muda é a PROCEDÊNCIA: veio de alguém assistindo, ou de uma aposta do sistema sobre o que
-- será assistido.
--
-- A distinção não é decorativa. Uma aposta pode estar errada — o espectador larga a série no
-- 50 e o 51 nunca é aberto —, e uma cópia que ninguém pediu não pode competir em pé de
-- igualdade com uma que já provou serventia na hora de liberar espaço.

ALTER TABLE arquivos_guardados
    ADD COLUMN adiantado boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN arquivos_guardados.adiantado IS
    'true quando a cópia foi baixada por antecipação (próximo episódio), e não por alguém ter assistido';

-- O índice serve à tela e à futura limpeza, que precisam separar as apostas ainda não
-- cobradas do acervo que já se pagou.
CREATE INDEX arquivos_adiantados_idx
    ON arquivos_guardados (adiantado, acessos)
    WHERE origem = 'fonte' AND adiantado;
