# VOD Manager — 17. GitHub, envio de arquivos e atualizações

> Como levar o código para a VPS e como atualizar depois, sem perder dados.

---

## Sim, vale a pena usar o GitHub

Eu disse antes que ele *não é necessário para instalar* — e não é. Mas você está certo em
querer: para **atualizar**, ele muda bastante.

O que ele resolve:

- **Uma linha para atualizar** na VPS (`git pull`), em vez de copiar arquivo a cada
  mudança.
- **Histórico.** Quando algo quebrar depois de uma atualização, você consegue ver o que
  mudou e voltar.
- **Backup do código.** É diferente do backup dos dados (que é o
  `vodmanager backup`). Se seu computador morrer, o código continua existindo.
- **Testar em várias VPS**, como você quer fazer: cada uma faz `git clone` da mesma fonte.

Use um **repositório privado**. O código é seu.

> Uma coisa que o GitHub **não** faz: atualizar o sistema sozinho pelo painel. Isso não
> existe hoje. O que existe é o script da seção 4, que faz a atualização inteira com um
> comando — backup, compilação, troca e volta atrás se der errado.

---

## 1. Colocar o código no GitHub

### 1.1 Conferir o que NÃO vai junto

O projeto já tem um `.gitignore` que exclui o essencial. Confira antes do primeiro envio:

```bash
cat .gitignore
```

Precisa conter `.vodm-dev/` e `.env`. É o que impede a **chave de criptografia** de
desenvolvimento de ir para o repositório — com ela e um backup, qualquer pessoa lê as
credenciais das suas fontes.

> Regra que não tem exceção: **chave de criptografia e senha de banco nunca entram no
> repositório**, nem em privado. Elas vivem no `/etc/vodmanager.env` de cada máquina.

### 1.2 Criar o repositório

No site do GitHub: **New repository** → nome (ex.: `vodmanager`) → **Private** → criar.
Não marque nenhuma das opções de inicialização (README, .gitignore, licença).

### 1.3 Enviar, do seu Windows

Na pasta do projeto:

```bash
git init -b main
```

O repositório já está inicializado se este comando disser que sim — nesse caso, siga.
Prepare os arquivos:

```bash
git add -A
```

Antes de enviar, **confira que nenhum segredo entrou**:

```bash
git ls-files | grep -Ei "(^|/)\.env$|\.key$|(^|/)\.vodm-dev/|encryption|cookies\.txt|\.pem$" && echo "PARE: há segredo no commit" || echo "OK: nenhum segredo no commit"
```

Deve imprimir `OK`. Se listar algum arquivo, pare e me avise antes de continuar.

Para confirmar especificamente a chave de desenvolvimento:

```bash
git check-ignore -v .vodm-dev/encryption.key
```

Deve responder que o `.gitignore` a está ignorando.

Confirmado isso, faça o commit:

```bash
git commit -m "VOD Manager: versão inicial"
```

Agora conecte e envie:

```bash
git remote add origin https://github.com/gacaciomg-source/VOD-MANEGER.git && git push -u origin main
```

O GitHub vai pedir login. Use um **token de acesso pessoal** no lugar da senha:
**Settings → Developer settings → Personal access tokens → Tokens (classic)**, com a
permissão `repo`.

---

## 2. As duas formas de levar para a VPS

### Forma A — Enviar só o binário (mais simples)

A VPS não precisa de Go, nem de Git, nem do código. Só de um arquivo de 17 MB.

No seu Windows, na pasta do projeto:

```bash
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o vodmanager-linux ./cmd/vodmanager
```

Envie:

```bash
scp vodmanager-linux gustavo@SEU_IP:~/
```

O `scp` já vem no Windows 10/11 — funciona no PowerShell e no terminal do Git Bash. Ele
vai pedir a senha da VPS.

Instale, já na VPS:

```bash
sudo install -o vodmanager -g vodmanager -m 0755 ~/vodmanager-linux /opt/vodmanager/vodmanager
```

**Quando usar:** quando você quer o mínimo de coisas instaladas na VPS. Cada atualização é
recompilar e reenviar.

### Forma B — Clonar o repositório na VPS

Precisa do Go instalado lá:

```bash
sudo rm -rf /usr/local/go && curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xz && echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh && export PATH=$PATH:/usr/local/go/bin
```

Clone (num repositório privado, o GitHub vai pedir usuário e o token):

```bash
git clone https://github.com/gacaciomg-source/VOD-MANEGER.git /opt/vodmanager-fonte
```

Compile e instale:

```bash
cd /opt/vodmanager-fonte && go build -o vodmanager ./cmd/vodmanager && sudo install -o vodmanager -g vodmanager -m 0755 vodmanager /opt/vodmanager/vodmanager
```

**Quando usar:** quando você quer atualizar com um comando, sem transferir arquivo. É a
forma que combina melhor com o GitHub.

---

## 3. Digitar a senha toda vez cansa: chave SSH

Vale os dois minutos, e é mais seguro que senha.

No seu Windows, uma vez só:

```bash
ssh-keygen -t ed25519 -C "meu-windows"
```

Aceite o caminho padrão. Pode deixar a senha da chave em branco.

Copie para a VPS:

```bash
ssh-copy-id gustavo@SEU_IP
```

Se `ssh-copy-id` não existir no seu Windows:

```bash
cat ~/.ssh/id_ed25519.pub | ssh gustavo@SEU_IP "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys"
```

A partir daí, `ssh` e `scp` não pedem mais senha.

---

## 4. Atualizar: o script que faz na ordem certa

Atualizar tem uma ordem que importa, e errá-la só dói depois. O projeto traz
`scripts/atualizar.sh`, que faz:

1. **Backup dos dados antes de tocar em qualquer coisa** — se a versão nova tiver
   problema, você não descobre isso já sem rede de proteção.
2. **Compila sem parar o serviço** — o tempo fora do ar é só o restart, não o build.
3. **Guarda o binário atual.**
4. Troca e reinicia.
5. **Confere se subiu. Se não subiu em 30 segundos, volta para a versão anterior
   sozinho.**

### Usando a Forma B (código na VPS)

```bash
cd /opt/vodmanager-fonte && git pull && sudo ./scripts/atualizar.sh
```

Na primeira vez, dê permissão de execução:

```bash
chmod +x /opt/vodmanager-fonte/scripts/atualizar.sh
```

### Usando a Forma A (binário enviado)

No Windows:

```bash
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o vodmanager-linux ./cmd/vodmanager && scp vodmanager-linux gustavo@SEU_IP:~/
```

Na VPS:

```bash
sudo /opt/vodmanager-fonte/scripts/atualizar.sh --binario ~/vodmanager-linux
```

(O script pode ficar em qualquer lugar; ele só precisa do binário e do
`/etc/vodmanager.env`.)

### Se a atualização voltar atrás

O script avisa e restaura a versão anterior. Para descobrir o motivo:

```bash
sudo journalctl -u vodmanager -n 50 --no-pager
```

Se precisar voltar mais fundo, o backup do passo 1 está em `/var/backups/vodmanager/`:

```bash
sudo systemctl stop vodmanager && set -a && . /etc/vodmanager.env && set +a && /opt/vodmanager/vodmanager restaurar --arquivo /var/backups/vodmanager/antes-da-atualizacao-XXXX.tar.gz && sudo systemctl start vodmanager
```

---

## 5. As atualizações apagam meus dados?

**Não.** São coisas separadas:

| | Onde vive | O que a atualização faz |
|---|---|---|
| Código | binário em `/opt/vodmanager/` | substituído |
| Dados | banco PostgreSQL | intocados |
| Chave e senhas | `/etc/vodmanager.env` | intocado |

Quando uma versão nova precisa mudar a estrutura do banco, ela roda a migração sozinha no
boot, **preservando o que existe** — foi assim que as migrações 6 e 7 entraram sem você
perder nada.

O backup automático do script é para o outro caso: quando a versão nova tem um defeito e
você precisa voltar no tempo.

---

## 6. Resumo

**Para a primeira instalação:** Forma A (só o binário) é a mais simples.

**Para atualizar com frequência e testar em várias VPS:** GitHub privado + Forma B, e as
atualizações viram `git pull && sudo ./scripts/atualizar.sh`.

**Nunca commite:** `/etc/vodmanager.env`, a chave de criptografia, a senha do banco, os
arquivos de backup, a pasta `.vodm-dev/`.
