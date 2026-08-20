# VOD Manager — 16. Hospedar pelo aaPanel

> Passo a passo para quem já tem o aaPanel na VPS e quer usá-lo.
>
> **Antes de começar, leia a próxima seção.** Ela não é para te convencer de nada — é
> para você saber com o que vai conviver, porque há um problema específico do aaPanel que
> quebra o vídeo de um jeito difícil de diagnosticar.

---

## O que muda com o aaPanel

O aaPanel é um painel para hospedar sites PHP: ele instala e gerencia Nginx, PHP e MySQL
pela interface. O VOD Manager não é um site PHP — é um binário e um serviço do systemd.

Três consequências práticas:

**1. O aaPanel reescreve a configuração do Nginx.** Quando você mexe num site pela
interface dele, ele regenera o arquivo de configuração daquele site. Os três ajustes que o
vídeo precisa (`proxy_buffering off`, `proxy_request_buffering off`, `proxy_read_timeout`)
**não existem na interface** — você escreve à mão, e uma alteração futura pelo painel pode
apagá-los.

O sintoma quando isso acontece: **o vídeo abre normalmente e corta depois de alguns
minutos**. Ninguém liga o corte a uma mudança feita no painel dias antes. É o motivo real
da recomendação de não usar o aaPanel aqui.

**2. Ele instala MySQL, o VOD Manager usa PostgreSQL.** Os dois convivem, mas o MySQL fica
ocupando memória sem servir para nada.

**3. Ele não gerencia o serviço.** O VOD Manager continua sendo controlado por
`systemctl`, não pelo painel — o aaPanel só entra como porta de entrada web.

Se isso estiver claro, siga. Funciona bem, desde que você saiba onde olhar quando algo
quebrar.

---

## Passo 1 — PostgreSQL

Pelo terminal (SSH), não pelo painel:

```bash
sudo apt update && sudo apt install -y postgresql postgresql-contrib
```

Gere uma senha e guarde:

```bash
openssl rand -base64 24
```

Crie o banco (troque `COLE_A_SENHA_AQUI`):

```bash
sudo -u postgres psql -c "CREATE ROLE vodmanager LOGIN PASSWORD 'COLE_A_SENHA_AQUI';" -c "CREATE DATABASE vodmanager OWNER vodmanager;"
```

Se a memória estiver apertada e você não usa o MySQL do aaPanel:

```bash
sudo systemctl disable --now mysqld
```

---

## Passo 2 — Instalar o VOD Manager

Igual a uma instalação normal. O aaPanel não participa desta parte.

```bash
sudo useradd --system --home /opt/vodmanager --shell /usr/sbin/nologin vodmanager && sudo mkdir -p /opt/vodmanager
```

Envie o binário (veja [docs/14, Parte 2](14-instalacao-na-vps-e-backup.md) para as formas
de enviar) e instale:

```bash
sudo install -o vodmanager -g vodmanager -m 0755 ~/vodmanager-linux /opt/vodmanager/vodmanager
```

Gere a chave de criptografia e **guarde fora da máquina**:

```bash
openssl rand -base64 32
```

Crie `/etc/vodmanager.env`. Note o `VODM_HTTP_ADDR`: o serviço escuta **só no localhost**,
porque quem fala com o mundo é o Nginx do aaPanel.

```
VODM_DATABASE_URL=postgres://vodmanager:SENHA_DO_BANCO@localhost:5432/vodmanager?sslmode=disable
VODM_ENCRYPTION_KEY=CHAVE_GERADA_ACIMA
VODM_HTTP_ADDR=127.0.0.1:8080
VODM_ROLE=all
VODM_TRUST_PROXY=true
VODM_BOOTSTRAP_ADMIN_USERNAME=admin
VODM_BOOTSTRAP_ADMIN_PASSWORD=uma-senha-longa-de-no-minimo-12-caracteres
VODM_LOG_FORMAT=json
```

`VODM_TRUST_PROXY=true` é necessário aqui: sem ele, todos os clientes chegariam com o IP do
Nginx, e a restrição por faixa de IP das credenciais deixaria de funcionar.

```bash
sudo chown root:vodmanager /etc/vodmanager.env && sudo chmod 640 /etc/vodmanager.env
```

Crie `/etc/systemd/system/vodmanager.service` com o conteúdo do
[guia geral, passo 6](10-instalacao-ubuntu-debian.md), e ative:

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now vodmanager && curl -fsS http://127.0.0.1:8080/healthz && echo
```

Se o `curl` responder, a parte difícil acabou.

---

## Passo 3 — Criar o site no aaPanel

Na interface do aaPanel:

1. **Website** → **Add site**
2. **Domain**: seu domínio (ex.: `vod.seudominio.com`). Se você não tem domínio, **pule
   todo o Passo 3 e vá para o Passo 6** — o aaPanel não ajuda em nada num acesso por IP.
3. **PHP version**: escolha **Static** ou **Pure static**. Não há PHP aqui.
4. **Database**: **não criar**. O banco já existe e é PostgreSQL.
5. Confirme.

O aaPanel vai criar uma pasta de site que ficará vazia. É esperado: o conteúdo não vem de
arquivos, vem do processo na porta 8080.

---

## Passo 4 — Apontar o site para o VOD Manager

Ainda no aaPanel: **Website** → clique no seu site → **Config file** (ou
**Configuration**).

Você verá o arquivo de configuração do Nginx daquele site. Dentro do bloco `server { }`,
**antes** de qualquer `location /` existente, cole:

```
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # ---- OS TRÊS AJUSTES QUE O VÍDEO PRECISA ----
        # Sem proxy_buffering off, o Nginx tenta acumular a resposta antes de repassar
        # — e a resposta aqui é um filme inteiro.
        proxy_buffering off;
        proxy_request_buffering off;
        # Sem este tempo, o vídeo corta depois de alguns minutos.
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
        client_max_body_size 0;
    }
```

Salve. O aaPanel recarrega o Nginx sozinho.

> **Anote estas linhas em algum lugar.** Se um dia o vídeo começar a cortar depois de
> alguns minutos, volte aqui e confira se elas continuam no arquivo — o aaPanel pode
> tê-las apagado ao regenerar a configuração.

Se o aaPanel tiver um campo separado de **"Configuração personalizada"** ou
**"Custom config"** para o site, prefira colar lá: esse campo costuma sobreviver às
regenerações, e o arquivo principal não.

---

## Passo 5 — HTTPS

No aaPanel: **Website** → seu site → **SSL** → **Let's Encrypt** → selecione o domínio →
**Apply**. Depois ligue **Force HTTPS**.

Feito isso, ajuste uma linha em `/etc/vodmanager.env`:

```
VODM_COOKIE_SECURE=true
```

E reinicie:

```bash
sudo systemctl restart vodmanager
```

---

## Passo 6 — Firewall

**Com domínio** (passos 3 a 5 feitos): abra só 80 e 443. A porta 8080 fica fechada, porque
o serviço só escuta no localhost.

No aaPanel: **Security** → libere `80` e `443`.

**Sem domínio**: você precisa expor a 8080 diretamente. Mude no `/etc/vodmanager.env`:

```
VODM_HTTP_ADDR=:8080
VODM_TRUST_PROXY=false
```

Reinicie o serviço e libere a porta 8080 no **Security** do aaPanel.

Em qualquer dos casos, confira também o firewall **no painel da Hostinger** — ele existe
fora da máquina e bloqueia mesmo com o aaPanel liberando.

---

## Passo 7 — Endereço público

Abra o painel do VOD Manager (`https://vod.seudominio.com` ou `http://SEU_IP:8080`),
entre com o usuário e a senha do `BOOTSTRAP_ADMIN`, e vá em **Configurações**.

Preencha o **endereço público** com o mesmo endereço pelo qual você acabou de entrar. É
ele que aparece nos links de reprodução e nas listas M3U — o padrão não serve, e o painel
avisa em vermelho enquanto estiver errado.

Depois, em **Configurações**, troque a senha do administrador e remova as duas linhas
`BOOTSTRAP_` do `/etc/vodmanager.env`.

---

## Quando algo der errado

| Sintoma | Onde olhar |
|---|---|
| Painel não abre, mas `curl http://127.0.0.1:8080/healthz` responde | A configuração do Nginx do site (Passo 4). |
| Nem o `curl` local responde | `sudo journalctl -u vodmanager -n 50 --no-pager` |
| **Vídeo abre e corta depois de alguns minutos** | Os três ajustes do Passo 4 sumiram. É o problema clássico do aaPanel. |
| Vídeo não abre em lugar nenhum, mas o catálogo aparece | Endereço público errado (Passo 7). |
| Todos os clientes aparecem com o mesmo IP nas Reproduções | Falta `VODM_TRUST_PROXY=true`. |
| Serviço não sobe depois de reiniciar a VPS | `sudo systemctl enable vodmanager` |

---

## Sobre o Docker do aaPanel

O aaPanel tem um gerenciador de Docker, e o projeto tem `Dockerfile` e
`docker-compose.yml`. Seria um caminho possível.

**Não recomendo por enquanto, e por um motivo honesto: esses arquivos ainda não foram
testados.** Eles foram escritos na Fase 1 e nunca rodaram — não havia Docker na máquina de
desenvolvimento. Documentar como caminho principal um trajeto que ninguém percorreu seria
te mandar depurar em produção.

Se você quiser o caminho Docker, me peça: eu testo antes e escrevo o guia com o que
realmente acontece.
