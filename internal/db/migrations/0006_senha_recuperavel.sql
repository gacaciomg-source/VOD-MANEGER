-- Senha de saída recuperável pelo painel.
--
-- Até aqui a senha só existia como HMAC: verificável, nunca legível. Isso é o ideal para
-- uma senha que a PESSOA escolhe e memoriza — mas não é o caso aqui.
--
-- A senha de saída é lida por uma máquina. O administrador precisa entregá-la pronta ao
-- cliente, dentro de uma URL de lista M3U ou de um cadastro Xtream. Com ela irrecuperável,
-- o painel só podia mostrar um link com um marcador para substituir à mão — e uma senha
-- anotada num papel, ou perdida e trocada a cada uso, é pior para a segurança real do que
-- uma senha cifrada em repouso pela mesma chave que já protege as credenciais das fontes.
--
-- O HMAC continua sendo o que autentica cada requisição de vídeo: a verificação no caminho
-- crítico não decifra nada. A cópia cifrada só é aberta quando o painel pede o link.
ALTER TABLE stream_credentials
    ADD COLUMN password_enc bytea;

COMMENT ON COLUMN stream_credentials.password_enc IS
    'Senha cifrada (AES-256-GCM, AAD ligado ao id da linha). NULL nas credenciais criadas antes desta migração: para elas o painel continua pedindo "Nova senha".';
