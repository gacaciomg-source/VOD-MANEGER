# VOD Manager — 14. Instalação na VPS (Hostinger, aaPanel) e backup

> Guia prático para colocar em produção e para trocar de máquina sem perder nada.
> Complementa o [guia geral de Ubuntu/Debian](10-instalacao-ubuntu-debian.md) com o que
> muda quando há aaPanel no meio.

---

## Parte 1 — aaPanel: usar ou não?

**Recomendação: instale sem o aaPanel.**

O aaPanel é um painel de administração que instala e gerencia Nginx, PHP, MySQL e sites.
Ele é ótimo quando você hospeda vários sites PHP na mesma máquina. Não é o seu caso, e
aqui ele atrapalha em três pontos concretos:

1. **Ele quer ser dono do Nginx.** Se você editar a configuração do Nginx por fora, o
   aaPanel pode sobrescrever na próxima alteração pelo painel. E a configuração que o VOD
   Manager precisa (`proxy_buffering off`, `proxy_read_timeout 1h`) não está na interface
   dele — tem que ser escrita à mão.
2. **Ele instala MySQL, não PostgreSQL.** O VOD Manager usa PostgreSQL. Você acabaria com
   um MySQL ocupando memória sem servir para nada.
3. **Ele abre uma porta administrativa a mais**, com senha, que é uma superfície de ataque
   que você não precisa ter.

O VOD Manager é **um binário e um serviço do systemd**. Não há nada para o aaPanel
gerenciar que `systemctl` não gerencie melhor.

**Se você já tem o aaPanel instalado e não quer removê-lo**, dá para conviver — a seção 4
explica como.

---

## Parte 2 — Instalação limpa na VPS Hostinger

### 2.1 Criar a VPS

No painel da Hostinger, escolha:

- **Sistema operacional:** Ubuntu 24.04 LTS, **sem painel de controle**. Se a opção vier
  como "Ubuntu 24.04 com aaPanel", escolha a versão limpa.
- **Localização:** a mais próxima dos seus clientes. Latência de rede é o que aparece no
  tempo de abertura do vídeo.

Guarde o IP e a senha de root que a Hostinger mostrar.

### 2.2 Entrar na máquina

Do seu computador (o PowerShell do Windows já tem `ssh`):

```bash
ssh root@SEU_IP
```

### 2.3 Primeiro acesso: proteger a máquina

Antes de instalar qualquer coisa. Rodar tudo como root é o erro que transforma um problema
pequeno em máquina perdida.

```bash
apt update && apt upgrade -y && apt install -y postgresql postgresql-contrib git ufw curl
```

Crie um usuário para você e dê a ele acesso administrativo:

```bash
adduser vodm && usermod -aG sudo vodm
```

Deste ponto em diante, entre como `vodm` e use `sudo` quando precisar.

### 2.4 O binário: compile no seu Windows, envie pronto

**Este é o caminho recomendado, e evita instalar o Go na VPS.**

O Go compila para outro sistema operacional a partir do seu, e o binário resultante é
**estaticamente ligado**: um único arquivo de ~17 MB, sem nenhuma biblioteca externa, com
o painel web embutido dentro dele. Não há pasta de assets para copiar.

Na pasta do projeto, no seu Windows:

```bash
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=1.0.0" -o vodmanager-linux ./cmd/vodmanager
```

Envie para a VPS:

```bash
scp vodmanager-linux vodm@SEU_IP:~/
```

E instale, já na VPS:

```bash
sudo install -o vodmanager -g vodmanager -m 0755 ~/vodmanager-linux /opt/vodmanager/vodmanager
```

Isso substitui o passo 4 do guia geral. A VPS não precisa de Go, nem de Git, nem do código
fonte — só do arquivo.

**Atualizar depois** vira: recompilar no Windows, `scp`, `systemctl stop`, `install`,
`systemctl start`.

> **Alternativa:** se preferir compilar na própria VPS, instale o Go lá
> (`curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xz`)
> e siga o guia geral. Funciona igual; só consome mais espaço e tempo na máquina.

### 2.5 Do banco em diante, siga o guia geral

Os passos de banco, usuário de sistema, chave, systemd, firewall e endereço
público estão em [docs/10-instalacao-ubuntu-debian.md](10-instalacao-ubuntu-debian.md), a
partir do passo 2. Eles valem igual na Hostinger.

**Um detalhe que muda:** na Hostinger, o firewall costuma existir também no painel deles,
fora da máquina. Se a porta 8080 não abrir mesmo com o `ufw` liberado, procure o firewall
no painel da Hostinger e libere lá também.

### 2.6 E o GitHub?

Não é necessário. Com o binário cross-compilado do passo 2.4, a VPS nunca vê o código
fonte — só o arquivo pronto.

Um repositório privado serve para histórico e backup do CÓDIGO, que é coisa diferente do
backup dos DADOS (Parte 3). Se um dia quiser, `git init` no seu Windows resolve.

---

## Parte 3 — Backup e troca de máquina

Esta é a parte que torna seguro testar em várias VPS.

### 3.1 Fazer um backup

Na máquina onde o sistema está rodando:

```bash
set -a && . /etc/vodmanager.env && set +a && /opt/vodmanager/vodmanager backup
```

O comando gera um arquivo `vodmanager-AAAA-MM-DD-HHMM.tar.gz` na pasta atual e mostra um
resumo.

Medido no acervo real (16.391 filmes, 8.883 séries, 254.025 episódios, 290.610 variantes,
876.334 linhas no total): **45,5 MB em 27 segundos**.

Para escolher onde salvar:

```bash
/opt/vodmanager/vodmanager backup --arquivo /root/backups/antes-da-migracao.tar.gz
```

### 3.2 As DUAS coisas que você precisa guardar

O backup contém o catálogo, as fontes, as categorias, as credenciais **cifradas**, os
usuários do painel e as configurações.

Ele **não contém a chave de criptografia**, e isso é de propósito: guardar a chave junto do
arquivo cifrado com ela anula a cifra.

> **Sem a chave, o backup restaura o catálogo mas NÃO as credenciais das suas fontes nem as
> senhas dos seus clientes.** Elas ficam ilegíveis para sempre.

A chave é o valor de `VODM_ENCRYPTION_KEY` em `/etc/vodmanager.env`. Para vê-la:

```bash
sudo grep ENCRYPTION /etc/vodmanager.env
```

Guarde-a **em outro lugar**: um gerenciador de senhas, um papel no cofre, o que preferir —
menos na mesma pasta do backup.

O backup registra uma **impressão** da chave (um identificador que não revela a chave). A
restauração compara as duas e recusa começar se forem diferentes, em vez de restaurar em
silêncio e falhar depois, quando alguém tentar assistir.

### 3.3 Trazer o backup para o seu computador

```bash
scp vodm@SEU_IP:~/vodmanager-2026-08-19-1858.tar.gz .
```

### 3.4 Restaurar numa máquina nova

1. Instale o VOD Manager na máquina nova seguindo a Parte 2. Deixe o serviço subir uma vez
   para criar o schema do banco.
2. **Coloque a MESMA chave** em `/etc/vodmanager.env` — a que você guardou no passo 3.2.
3. Envie o arquivo e restaure:

```bash
scp backup.tar.gz vodm@IP_DA_MAQUINA_NOVA:~/
```

```bash
sudo systemctl stop vodmanager && set -a && . /etc/vodmanager.env && set +a && /opt/vodmanager/vodmanager restaurar --arquivo ~/backup.tar.gz
```

O comando pede confirmação, porque **substitui tudo** que estiver no banco. Digite
`restaurar` para confirmar.

4. Suba o serviço e ajuste o endereço:

```bash
sudo systemctl start vodmanager
```

Depois, no painel → **Configurações**, troque o **endereço público** para o IP ou domínio
da máquina nova. Sem isso os links continuariam apontando para a máquina antiga.

Medido: restaurar as 876.334 linhas levou **1min27s**.

### 3.5 O que a restauração garante

- **Tudo ou nada.** É uma transação só. Se falhar no meio, o banco fica exatamente como
  estava — não existe estado pela metade.
- **Recusa a chave errada**, antes de escrever qualquer coisa.
- **Recusa um schema mais antigo** que o do backup, dizendo para atualizar o binário
  primeiro.
- **Reposiciona os contadores de id**, para o primeiro cadastro novo não colidir com uma
  linha restaurada.

O que **não** é restaurado, de propósito: sessões de login abertas, tokens de API e
reproduções em andamento. Nada disso faz sentido em outra máquina.

### 3.6 Backup automático diário

```bash
sudo tee /etc/cron.daily/vodmanager-backup >/dev/null <<'CRON'
#!/bin/sh
set -e
mkdir -p /var/backups/vodmanager
set -a; . /etc/vodmanager.env; set +a
/opt/vodmanager/vodmanager backup --arquivo "/var/backups/vodmanager/vodmanager-$(date +%F).tar.gz"
# Mantém 14 dias. Backup que enche o disco derruba o serviço que ele deveria proteger.
find /var/backups/vodmanager -name 'vodmanager-*.tar.gz' -mtime +14 -delete
CRON
sudo chmod +x /etc/cron.daily/vodmanager-backup
```

Teste antes de confiar:

```bash
sudo /etc/cron.daily/vodmanager-backup && ls -lh /var/backups/vodmanager
```

> **Um backup na mesma máquina não é backup.** Se a VPS morrer, ele morre junto. Copie os
> arquivos para fora periodicamente — pelo `scp` para o seu computador, ou para outro
> servidor.

### 3.7 Por que não usamos `pg_dump`

Ele é excelente e daria menos código. Mas cria um acoplamento que morde na hora errada: o
arquivo gerado pelo `pg_dump` de uma versão do PostgreSQL pode ser recusado por outra.
Trocar de VPS costuma significar trocar de versão do PostgreSQL junto — e descobrir isso
durante a migração, com o sistema fora do ar, é o pior momento possível.

O formato aqui é CSV dentro de um `.tar.gz`: legível por qualquer ferramenta, independente
de versão do banco, e gerado pela mesma conexão que a aplicação já usa.

---

## Parte 4 — Convivendo com o aaPanel (se você já tem)

Só leia esta parte se o aaPanel já estiver instalado e você não quiser removê-lo.

### 4.1 O PostgreSQL

O aaPanel oferece MySQL. **Não use.** Instale o PostgreSQL pelo `apt`, como no guia geral:

```bash
sudo apt install -y postgresql postgresql-contrib
```

Os dois convivem. Se a memória estiver apertada, desligar o MySQL que você não usa libera
bastante:

```bash
sudo systemctl disable --now mysqld
```

### 4.2 O Nginx

O aaPanel gerencia o Nginx dele. Duas opções:

**Opção A — deixe o aaPanel fora do caminho.** Rode o VOD Manager direto na porta 8080 e
acesse por `http://SEU_IP:8080`. É o mais simples e não briga com nada.

**Opção B — use o Nginx do aaPanel como proxy.** No painel, crie um site para o seu
domínio, e depois cole a configuração de proxy do
[guia geral](10-instalacao-ubuntu-debian.md#opcional-https-com-domínio) no campo de
configuração personalizada do site.

**Os três ajustes que a interface do aaPanel não tem e que você precisa escrever à mão:**

```
proxy_buffering off;
proxy_request_buffering off;
proxy_read_timeout 1h;
```

Sem `proxy_buffering off`, o Nginx tenta acumular a resposta antes de repassar — e a
resposta aqui é um filme inteiro. Sem `proxy_read_timeout`, o vídeo corta depois de alguns
minutos.

> Se o aaPanel sobrescrever a configuração numa alteração futura pelo painel, esses três
> ajustes somem e o vídeo volta a cortar. É o principal motivo da recomendação de não usar
> o aaPanel aqui.

### 4.3 O serviço

Crie o serviço do systemd normalmente, como no guia geral. Não o registre como "projeto"
do aaPanel: ele tentaria gerenciar o ciclo de vida por cima do systemd, e os dois se
atrapalham no restart.

---

## Resumo dos comandos

| O que | Comando |
|---|---|
| Ver o estado do serviço | `systemctl status vodmanager` |
| Ver os logs ao vivo | `sudo journalctl -u vodmanager -f` |
| Reiniciar | `sudo systemctl restart vodmanager` |
| Fazer backup | `set -a && . /etc/vodmanager.env && set +a && /opt/vodmanager/vodmanager backup` |
| Restaurar | `/opt/vodmanager/vodmanager restaurar --arquivo ARQUIVO.tar.gz` |
| Ver a chave | `sudo grep ENCRYPTION /etc/vodmanager.env` |
| Ver o consumo da máquina | painel → **Sistema** |
