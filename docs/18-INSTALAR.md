# VOD Manager — Guia de instalação

> Do zero ao sistema no ar, em um comando.
> Repositório: `https://github.com/gacaciomg-source/VOD-MANEGER`

Você vai precisar de: uma VPS Ubuntu 22.04/24.04 ou Debian 12, e o IP e a senha de root
que o provedor deu.

---

## 1. Instalar

Entre na VPS:

```bash
ssh root@SEU_IP
```

E rode:

```bash
curl -fsSL https://raw.githubusercontent.com/gacaciomg-source/VOD-MANEGER/main/scripts/instalar.sh | sudo bash
```

Ele vai pedir a **senha do painel** (mínimo 12 caracteres) e fazer o resto sozinho:
pacotes, Go, PostgreSQL, banco, usuário de sistema, compilação, chave de criptografia,
serviço, firewall e o endereço público.

Leva alguns minutos — a maior parte é a primeira compilação.

### O que aparece no fim

```
  Instalação concluída.

  Painel .......... http://SEU_IP:8080
  Usuário ......... admin
  Senha ........... a que você escolheu

  ┌──────────────────────────────────────────────────────────┐
  │  GUARDE A CHAVE DE CRIPTOGRAFIA FORA DESTA MÁQUINA...    │
  └──────────────────────────────────────────────────────────┘
```

> ## ⚠️ Copie a chave de criptografia agora
>
> Gerenciador de senhas, e-mail para você mesmo, papel no cofre — onde você não perca.
>
> Ela protege as credenciais das suas fontes e as senhas dos seus clientes. **Perdê-la
> torna esses dados irrecuperáveis, mesmo com backup.** É a única coisa em toda a
> instalação que não tem conserto.
>
> Se precisar dela depois: `sudo grep ENCRYPTION /etc/vodmanager.env`

### Se o painel não abrir pelo IP

Vários provedores — Hostinger entre eles — têm um **firewall fora da máquina**, no painel
deles. O instalador libera o `ufw`, mas não alcança esse. Procure o firewall no painel do
provedor e libere a porta **8080**.

---

## 2. Primeiro acesso

Abra `http://SEU_IP:8080` e entre com `admin` e a senha que você escolheu.

O **endereço público** já vem configurado pelo instalador — confira em **Configurações**
se está com o IP certo. É ele que aparece nos links de reprodução e nas listas M3U.

Recomendado, depois de entrar:

- **Configurações** → trocar a senha do painel
- Apagar as linhas `BOOTSTRAP_` do `/etc/vodmanager.env` (elas já não têm efeito, é
  higiene)

---

## 3. Colocar o catálogo para funcionar

1. **Fontes** → cadastre sua fonte M3U ou Xtream → **Sincronizar**
2. **Credenciais** → crie uma credencial. Usuário e senha podem ser escolhidos por você,
   ou gerados.
3. **Credenciais → Lista** → copie os endereços prontos:
   - lista M3U completa, já com a senha embutida
   - dados de servidor Xtream para cadastrar no XC_VM
   - listas parciais (só filmes, só séries)
4. **Sistema** → confira, no horário de pico, se a VPS dá conta

---

## 4. Trazer um catálogo que você já tem

*Opcional. Evita sincronizar tudo de novo numa máquina nova.*

Na máquina de origem:

```bash
set -a && . /etc/vodmanager.env && set +a && /opt/vodmanager/vodmanager backup
```

Na máquina de desenvolvimento Windows, na pasta do projeto:

```bash
VODM_DATABASE_URL="postgres://vodm:vodm@127.0.0.1:55432/vodm?sslmode=disable" VODM_ENCRYPTION_KEY="$(cat .vodm-dev/encryption.key)" go run ./cmd/vodmanager backup
```

Envie e restaure:

```bash
scp vodmanager-*.tar.gz root@SEU_IP:/root/
```

```bash
systemctl stop vodmanager && set -a && . /etc/vodmanager.env && set +a && /opt/vodmanager/vodmanager restaurar --arquivo /root/vodmanager-*.tar.gz && systemctl start vodmanager
```

> **A chave de criptografia da máquina nova precisa ser a MESMA da origem.** Se for
> diferente, a restauração recusa começar e avisa — de propósito, porque restaurar com a
> chave errada dá um sistema que parece íntegro e falha só quando alguém tenta assistir.
>
> Para usar a chave antiga: edite `VODM_ENCRYPTION_KEY` em `/etc/vodmanager.env` antes de
> restaurar, e reinicie o serviço.

Depois, ajuste o **endereço público** em Configurações — o backup traz o da máquina antiga.

> Medido num acervo de 16.391 filmes, 8.883 séries e 254.025 episódios: backup em 27s
> (45,5 MB), restauração em 1min27s.

---

## 5. Domínio e HTTPS

*Opcional. Só faz sentido com um domínio apontando para o IP da VPS.*

No seu provedor de domínio, crie um registro **A** (ex.: `vod`) apontando para o IP.

```bash
apt install -y nginx certbot python3-certbot-nginx
```

```bash
nano /etc/nginx/sites-available/vodmanager
```

Cole, trocando o domínio:

```
server {
    listen 80;
    server_name vod.seudominio.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
        client_max_body_size 0;
    }
}
```

`proxy_buffering off` não é detalhe: com buffering ligado, o Nginx tenta acumular a
resposta antes de repassar — e a resposta aqui é um filme inteiro. Sem
`proxy_read_timeout`, o vídeo corta depois de alguns minutos.

```bash
ln -s /etc/nginx/sites-available/vodmanager /etc/nginx/sites-enabled/ && nginx -t && systemctl reload nginx && certbot --nginx -d vod.seudominio.com
```

Ajuste três linhas em `/etc/vodmanager.env`:

```
VODM_HTTP_ADDR=127.0.0.1:8080
VODM_TRUST_PROXY=true
VODM_COOKIE_SECURE=true
```

`VODM_TRUST_PROXY=true` é necessário: sem ele, todos os clientes chegariam com o IP do
Nginx, e a restrição por faixa de IP das credenciais deixaria de funcionar.

```bash
systemctl restart vodmanager && ufw allow 'Nginx Full' && ufw delete allow 8080/tcp
```

E troque o endereço público em **Configurações** para `https://vod.seudominio.com`.

---

## 6. Backup automático

```bash
nano /etc/cron.daily/vodmanager-backup
```

```sh
#!/bin/sh
set -e
mkdir -p /var/backups/vodmanager
set -a; . /etc/vodmanager.env; set +a
/opt/vodmanager/vodmanager backup --arquivo "/var/backups/vodmanager/vodmanager-$(date +%F).tar.gz"
find /var/backups/vodmanager -name 'vodmanager-*.tar.gz' -mtime +14 -delete
```

```bash
chmod +x /etc/cron.daily/vodmanager-backup && /etc/cron.daily/vodmanager-backup && ls -lh /var/backups/vodmanager
```

O último comando testa antes de você confiar.

> **Backup na mesma máquina não é backup.** Se a VPS morrer, ele morre junto. Traga os
> arquivos de vez em quando: `scp root@SEU_IP:/var/backups/vodmanager/*.tar.gz .`
>
> E lembre que **a chave não está no backup**. Ela precisa estar guardada à parte.

---

## 7. Atualizar

No Windows, depois de mudar algo:

```bash
git add -A && git commit -m "descricao" && git push
```

Na VPS:

```bash
cd /opt/vodmanager-fonte && git pull && sudo bash scripts/atualizar.sh
```

O script faz **backup antes de tocar em qualquer coisa**, compila sem parar o serviço,
guarda o binário atual, troca, reinicia — e **se a versão nova não subir em 30 segundos,
volta sozinho para a anterior**.

Também dá para rodar o instalador de novo: ele é seguro para reinstalação, preserva os
dados e **nunca regenera a chave de criptografia**.

**Atualizar não apaga dados.** O código vive no binário, os dados no PostgreSQL, a chave no
`/etc/vodmanager.env`. Migrações de banco rodam sozinhas na partida, preservando o que
existe.

---

## Se algo der errado

| Sintoma | O que fazer |
|---|---|
| Serviço não sobe | `journalctl -u vodmanager -n 50 --no-pager` |
| Painel não abre pelo IP | Firewall do provedor, fora da máquina |
| Painel abre, vídeo não funciona fora da máquina | Endereço público errado (Configurações) |
| Vídeo corta após alguns minutos, com Nginx | Faltam `proxy_buffering off` e `proxy_read_timeout` |
| `Too many open files` | `LimitNOFILE` ausente no serviço |
| Clientes todos com o mesmo IP em Reproduções | Falta `VODM_TRUST_PROXY=true` |
| Esqueci a senha do painel | Não há recuperação. Me chame: dá para criar outro admin pelo banco |

```bash
systemctl status vodmanager
```

```bash
journalctl -u vodmanager -f
```

---

## Apêndice — instalação manual

O instalador cobre tudo. Se você precisar entender ou refazer passo a passo — por exemplo
para adaptar a um ambiente diferente —, os passos individuais estão em
[docs/10-instalacao-ubuntu-debian.md](10-instalacao-ubuntu-debian.md).

Para conviver com o aaPanel: [docs/16-hospedar-pelo-aapanel.md](16-hospedar-pelo-aapanel.md).

Sobre GitHub, envio de arquivos e atualizações:
[docs/17-github-e-atualizacoes.md](17-github-e-atualizacoes.md).
