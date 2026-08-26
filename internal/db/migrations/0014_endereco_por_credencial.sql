-- O endereço de entrega, escolhido por credencial.
--
-- # Por que uma instalação precisa de mais de um endereço
--
-- Domínio e IP não são a mesma porta com nomes diferentes. O domínio passa por proxy reverso
-- e TLS — uma camada a mais, que custa latência e é mais um lugar onde algo pode falhar. O IP
-- vai direto ao serviço.
--
-- Para a maioria dos clientes o domínio é o certo: ele sobrevive a uma troca de máquina, e o
-- IP não. Mas há casos em que a camada extra não se paga — poucos clientes, conhecidos, cujo
-- player não se importa com certificado e cuja reprodução ganha em ir direto.
--
-- Um único endereço global obriga a escolher um dos dois para todo mundo.
--
-- # Por que na credencial, e não numa configuração à parte
--
-- A credencial é o que o cliente tem em mãos. Ela já carrega o limite de conexões, a cota e a
-- validade — tudo que descreve o acordo com aquele cliente. O endereço por onde ele recebe é
-- da mesma natureza, e guardá-lo em outro lugar significaria manter duas listas em acordo.
--
-- Vazio é o caso normal: usa o endereço de conteúdo configurado, como sempre.

ALTER TABLE stream_credentials
    ADD COLUMN base_url_override text NOT NULL DEFAULT '';

COMMENT ON COLUMN stream_credentials.base_url_override IS
    'endereço usado nos links desta credencial; vazio usa o endereço de conteúdo global';

-- O endereço tem de ser absoluto, ou não serve como prefixo de link.
--
-- Sem isto, um valor como "vod.exemplo.com" — digitado sem o esquema, que é o erro natural —
-- produziria links relativos que o player não resolve. A lista baixaria, o catálogo
-- apareceria, e nada tocaria.
ALTER TABLE stream_credentials
    ADD CONSTRAINT stream_credentials_base_url_absoluta
    CHECK (base_url_override = '' OR base_url_override ~ '^https?://[^/]+');
